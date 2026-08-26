package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Overclock-Validator/mithril-agent/programinterface"
)

type programBuildOutput struct {
	Status                    string            `json:"status"`
	Program                   string            `json:"program"`
	InterfaceSHA256           string            `json:"interface_sha256"`
	Instruction               string            `json:"instruction"`
	Message                   []byte            `json:"message"`
	MessageSHA256             string            `json:"message_sha256"`
	UnsignedTransaction       []byte            `json:"unsigned_transaction"`
	UnsignedTransactionSHA256 string            `json:"unsigned_transaction_sha256"`
	Review                    programCallReview `json:"review"`
	Walletless                bool              `json:"walletless"`
}

type programCallReview struct {
	FeePayer  string                  `json:"fee_payer"`
	Accounts  []programAccountReview  `json:"accounts,omitempty"`
	Arguments []programArgumentReview `json:"arguments,omitempty"`
}

type programAccountReview struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Writable bool   `json:"writable"`
	Signer   bool   `json:"signer"`
}

type programArgumentReview struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}

func runProgramBuild(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("program build", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspacePath := flags.String("workspace", "", "program workspace.json path")
	registry := flags.String("registry", "", "local program interface registry")
	program := flags.String("program", "", "expected Solana program address")
	interfaceHash := flags.String("sha256", "", "pinned interface SHA-256")
	instruction := flags.String("instruction", "", "exact instruction name")
	feePayer := flags.String("fee-payer", "", "public fee-payer address; no key is loaded")
	recentBlockhash := flags.String("recent-blockhash", "", "recent Solana blockhash")
	jsonOutput := flags.Bool("json", false, "include the unsigned bytes as base64 JSON")
	var accounts programAccountFlags
	var arguments programArgumentFlags
	flags.Var(&accounts, "account", "pinned account binding NAME=ADDRESS; repeat in any order")
	flags.Var(&arguments, "arg", "pinned argument binding NAME=JSON; repeat as needed")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, programUsage)
			return writeErr
		}
		return err
	}
	if err := applyLocalProgramWorkspace(*workspacePath, registry, program); err != nil {
		return fmt.Errorf("program build: %w", err)
	}
	if flags.NArg() != 0 || *registry == "" || *program == "" || *interfaceHash == "" ||
		*instruction == "" || *feePayer == "" || *recentBlockhash == "" {
		return errors.New("program build requires the pinned interface, instruction, fee payer, and recent blockhash")
	}
	report, _, err := programinterface.Load(*registry, *program, *interfaceHash)
	if err != nil {
		return err
	}
	call, err := programinterface.Build(
		report, *instruction, *feePayer, *recentBlockhash, accounts, arguments,
	)
	if err != nil {
		return err
	}
	review, err := reviewProgramCall(report, *instruction, *feePayer, accounts, arguments)
	if err != nil {
		return err
	}
	messageHash := sha256.Sum256(call.Message)
	transactionHash := sha256.Sum256(call.Transaction)
	result := programBuildOutput{
		Status: "built", Program: report.Program, InterfaceSHA256: report.SHA256,
		Instruction: *instruction, Message: call.Message,
		MessageSHA256:             hex.EncodeToString(messageHash[:]),
		UnsignedTransaction:       call.Transaction,
		UnsignedTransactionSHA256: hex.EncodeToString(transactionHash[:]),
		Review:                    review,
		Walletless:                true,
	}
	if *jsonOutput {
		return json.NewEncoder(output).Encode(result)
	}
	if _, err = fmt.Fprintf(output, `Unsigned program call built
Program: %s
Instruction: %s
Interface SHA-256: %s
Message SHA-256: %s
Unsigned transaction SHA-256: %s
`, result.Program, result.Instruction, result.InterfaceSHA256,
		result.MessageSHA256, result.UnsignedTransactionSHA256); err != nil {
		return err
	}
	if err := writeProgramCallReview(output, result.Review); err != nil {
		return err
	}
	_, err = fmt.Fprint(output, `Walletless: no key loaded and no transaction submitted.
Use --json only when the next local step needs the base64-encoded unsigned bytes.
`)
	return err
}

func reviewProgramCall(
	report programinterface.Report,
	instructionName, feePayer string,
	bindings []programinterface.Binding,
	arguments []programinterface.ArgumentBinding,
) (programCallReview, error) {
	var instruction *programinterface.Instruction
	for index := range report.Instructions {
		if report.Instructions[index].Name == instructionName {
			instruction = &report.Instructions[index]
			break
		}
	}
	if instruction == nil {
		return programCallReview{}, errors.New("review instruction is absent from the pinned interface")
	}
	bound := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		bound[binding.Name] = binding.Address
	}
	values := make(map[string]json.RawMessage, len(arguments))
	for _, argument := range arguments {
		values[argument.Name] = argument.Value
	}
	review := programCallReview{FeePayer: feePayer}
	for _, account := range instruction.Accounts {
		review.Accounts = append(review.Accounts, programAccountReview{
			Name: account.Name, Address: bound[account.Name],
			Writable: account.Writable, Signer: account.Signer,
		})
	}
	for _, argument := range instruction.Args {
		review.Arguments = append(review.Arguments, programArgumentReview{
			Name: argument.Name, Value: values[argument.Name],
		})
	}
	return review, nil
}

func writeProgramCallReview(output io.Writer, review programCallReview) error {
	if _, err := fmt.Fprintf(output, "Review:\n  Fee payer: %s\n", review.FeePayer); err != nil {
		return err
	}
	for _, account := range review.Accounts {
		if _, err := fmt.Fprintf(output,
			"  Account %s: %s (writable=%t signer=%t)\n",
			account.Name, account.Address, account.Writable, account.Signer); err != nil {
			return err
		}
	}
	for _, argument := range review.Arguments {
		if _, err := fmt.Fprintf(output, "  Argument %s: %s\n", argument.Name, argument.Value); err != nil {
			return err
		}
	}
	return nil
}
