package programinterface

import (
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

type Binding struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

type UnsignedCall struct {
	Message     []byte
	Transaction []byte
}

// ArgumentBinding binds one pinned argument name to one exact JSON value.
type ArgumentBinding struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}

// BuildNoArgs builds the exact unsigned v0 transaction used for a walletless
// simulation of one pinned no-argument instruction. The existing one-signer
// compiler intentionally limits signer accounts to the public fee-payer
// address; no private key is accepted or loaded here.
func BuildNoArgs(
	report Report,
	instructionName,
	feePayer,
	recentBlockhash string,
	bindings []Binding,
) (UnsignedCall, error) {
	return Build(report, instructionName, feePayer, recentBlockhash, bindings, nil)
}

// Build constructs an unsigned v0 transaction from the exact pinned
// instruction shape. Argument values are JSON at the CLI boundary and are
// encoded deterministically with the IDL's default Borsh representation.
func Build(
	report Report,
	instructionName,
	feePayer,
	recentBlockhash string,
	bindings []Binding,
	arguments []ArgumentBinding,
) (UnsignedCall, error) {
	if _, err := solana.Decode32(report.Program); err != nil {
		return UnsignedCall{}, errors.New("program interface report is invalid")
	}
	if len(report.SHA256) != 64 {
		return UnsignedCall{}, errors.New("program interface hash is invalid")
	}
	if _, err := hex.DecodeString(report.SHA256); err != nil {
		return UnsignedCall{}, errors.New("program interface hash is invalid")
	}
	var instruction *Instruction
	for index := range report.Instructions {
		if report.Instructions[index].Name == instructionName {
			if instruction != nil {
				return UnsignedCall{}, errors.New("program interface instruction is ambiguous")
			}
			instruction = &report.Instructions[index]
		}
	}
	if instruction == nil {
		return UnsignedCall{}, errors.New("program interface has no instruction with that exact name")
	}
	for _, account := range instruction.Accounts {
		if account.SignerMode != "" {
			return UnsignedCall{}, errors.New("instruction uses conditional signer accounts; walletless construction requires a reviewed dedicated adapter")
		}
	}
	memoAdapter := false
	if instruction.DynamicRemainingAccounts {
		if err := validateUnsignedMemoAdapter(report, *instruction, bindings); err != nil {
			return UnsignedCall{}, errors.New("instruction uses dynamic remaining accounts; fixed walletless construction requires a reviewed dedicated adapter")
		}
		memoAdapter = true
	}
	if len(bindings) != len(instruction.Accounts) {
		return UnsignedCall{}, errors.New("account bindings do not match the pinned instruction")
	}
	bound := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		if binding.Name == "" || binding.Address == "" {
			return UnsignedCall{}, errors.New("account binding name and address are required")
		}
		if _, exists := bound[binding.Name]; exists {
			return UnsignedCall{}, errors.New("account binding names must be unique")
		}
		if _, err := solana.Decode32(binding.Address); err != nil {
			return UnsignedCall{}, errors.New("account binding address is invalid")
		}
		bound[binding.Name] = binding.Address
	}
	metas := make([]solana.AccountMeta, 0, len(instruction.Accounts))
	for _, expected := range instruction.Accounts {
		address, ok := bound[expected.Name]
		if !ok {
			return UnsignedCall{}, errors.New("account bindings do not match the pinned instruction")
		}
		if expected.Address != "" && expected.Address != address {
			return UnsignedCall{}, errors.New("account binding does not match the IDL fixed address")
		}
		if expected.Signer && address != feePayer {
			return UnsignedCall{}, errors.New("instruction requires an additional signer; walletless simulation does not guess signer identities")
		}
		metas = append(metas, solana.AccountMeta{
			Address: address, Signer: expected.Signer, Writable: expected.Writable,
		})
		delete(bound, expected.Name)
	}
	if len(bound) != 0 {
		return UnsignedCall{}, errors.New("account bindings include names absent from the pinned instruction")
	}
	data, err := hex.DecodeString(instruction.Discriminator)
	if err != nil {
		return UnsignedCall{}, errors.New("program interface discriminator is invalid")
	}
	encodedArguments, err := EncodeArguments(*instruction, report.Types, arguments)
	if err != nil {
		return UnsignedCall{}, err
	}
	if memoAdapter && len(encodedArguments) > maxUnsignedMemoBytes {
		return UnsignedCall{}, errors.New("Memo instruction exceeds the reviewed 566-byte unsigned limit")
	}
	data = append(data, encodedArguments...)
	message, err := solana.BuildV0Message(feePayer, recentBlockhash, []solana.Instruction{{
		Program: report.Program, Accounts: metas, Data: data,
	}}, nil)
	if err != nil {
		return UnsignedCall{}, err
	}
	transaction, err := solana.BuildV0SimulationTransaction(message, nil)
	if err != nil {
		return UnsignedCall{}, err
	}
	return UnsignedCall{Message: message, Transaction: transaction}, nil
}
