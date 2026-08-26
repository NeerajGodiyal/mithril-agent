package jupiterswap

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

// ValidateExactInSOL permits one narrow Jupiter plan: wrap an exact amount of
// native SOL in the taker's canonical account, execute one supported route_v2
// swap into the taker's canonical classic-token account, then close the
// wrapped-SOL account back to the taker. Any additional instruction is rejected.
func ValidateExactInSOL(
	request jupiterquote.Request,
	quote jupiterquote.Result,
	instructions []solana.Instruction,
) (Intent, error) {
	if request.InputMint != orcaswap.WrappedSOLMint {
		return Intent{}, errors.New("Jupiter plan only supports native SOL input")
	}
	inputAccount, err := orcaswap.AssociatedTokenAddress(request.Taker, request.InputMint)
	if err != nil {
		return Intent{}, errors.New("derive Jupiter input token account")
	}
	outputAccount, err := orcaswap.AssociatedTokenAddress(request.Taker, request.OutputMint)
	if err != nil {
		return Intent{}, errors.New("derive Jupiter output token account")
	}

	routeIndex := -1
	for index, instruction := range instructions {
		if instruction.Program != Program {
			continue
		}
		if routeIndex >= 0 {
			return Intent{}, errors.New("Jupiter plan contains multiple routes")
		}
		routeIndex = index
	}
	if routeIndex < 0 {
		return Intent{}, errors.New("Jupiter plan has no supported route")
	}
	intent, err := ValidateRouteV2(request, quote, instructions[routeIndex])
	if err != nil {
		return Intent{}, err
	}
	if intent.SourceTokenAccount != inputAccount ||
		intent.DestinationTokenAccount != outputAccount {
		return Intent{}, errors.New("Jupiter route does not use the taker's canonical token accounts")
	}

	seenInputATA, seenOutputATA := false, false
	seenTransfer, seenSync := false, false
	for _, instruction := range instructions[:routeIndex] {
		switch {
		case matchesATA(instruction, request.Taker, inputAccount, request.InputMint):
			if seenInputATA || seenTransfer {
				return Intent{}, errors.New("Jupiter wrapped-SOL account setup is duplicated or out of order")
			}
			seenInputATA = true
		case matchesATA(instruction, request.Taker, outputAccount, request.OutputMint):
			if seenOutputATA {
				return Intent{}, errors.New("Jupiter output-token account setup is duplicated")
			}
			seenOutputATA = true
		case matchesSystemTransfer(instruction, request.Taker, inputAccount, request.InputAmount):
			if !seenInputATA || seenTransfer || seenSync {
				return Intent{}, errors.New("Jupiter wrapped-SOL funding is duplicated or out of order")
			}
			seenTransfer = true
		case matchesTokenInstruction(instruction, 17, []solana.AccountMeta{
			{Address: inputAccount, Writable: true},
		}):
			if !seenTransfer || seenSync {
				return Intent{}, errors.New("Jupiter wrapped-SOL sync is duplicated or out of order")
			}
			seenSync = true
		default:
			return Intent{}, errors.New("Jupiter plan contains an unsupported setup instruction")
		}
	}
	if !seenInputATA || !seenTransfer || !seenSync {
		return Intent{}, errors.New("Jupiter wrapped-SOL setup is incomplete")
	}
	if len(instructions[routeIndex+1:]) != 1 ||
		!matchesTokenInstruction(instructions[routeIndex+1], 9, []solana.AccountMeta{
			{Address: inputAccount, Writable: true},
			{Address: request.Taker, Writable: true},
			{Address: request.Taker, Signer: true},
		}) {
		return Intent{}, errors.New("Jupiter wrapped-SOL cleanup is outside policy")
	}
	intent.OutputAccountCreated = seenOutputATA
	return intent, nil
}

// RemoveRedundantOutputAccountSetup removes only the canonical idempotent ATA
// setup for a native-input route. Callers must first independently verify that
// the destination account already exists; all other setup remains visible to
// ValidateProposal and is rejected there.
func RemoveRedundantOutputAccountSetup(
	request jupiterquote.Request,
	instructions []solana.Instruction,
) ([]solana.Instruction, error) {
	if request.InputMint != orcaswap.WrappedSOLMint {
		return instructions, nil
	}
	outputAccount, err := orcaswap.AssociatedTokenAddress(request.Taker, request.OutputMint)
	if err != nil || request.DestinationTokenAccount != outputAccount {
		return nil, errors.New("Jupiter output account is not the canonical pre-created account")
	}
	remove := -1
	for index, instruction := range instructions {
		if !matchesATA(instruction, request.Taker, outputAccount, request.OutputMint) {
			continue
		}
		if remove >= 0 {
			return nil, errors.New("Jupiter output-account setup is duplicated")
		}
		remove = index
	}
	if remove < 0 {
		return instructions, nil
	}
	result := make([]solana.Instruction, 0, len(instructions)-1)
	result = append(result, instructions[:remove]...)
	return append(result, instructions[remove+1:]...), nil
}

// validateExactInTokenToSOL validates the token-input/native-output plan used
// by the v6 policy. Token spend and native fees are capped independently.
func validateExactInTokenToSOL(
	request jupiterquote.Request,
	quote jupiterquote.Result,
	instructions []solana.Instruction,
) (Intent, error) {
	if request.InputMint == orcaswap.WrappedSOLMint ||
		request.OutputMint != orcaswap.WrappedSOLMint {
		return Intent{}, errors.New("Jupiter reverse plan requires token input and native SOL output")
	}
	inputAccount, err := orcaswap.AssociatedTokenAddress(request.Taker, request.InputMint)
	if err != nil {
		return Intent{}, errors.New("derive Jupiter input token account")
	}
	outputAccount, err := orcaswap.AssociatedTokenAddress(request.Taker, request.OutputMint)
	if err != nil {
		return Intent{}, errors.New("derive Jupiter output token account")
	}

	routeIndex := -1
	for index, instruction := range instructions {
		if instruction.Program != Program {
			continue
		}
		if routeIndex >= 0 {
			return Intent{}, errors.New("Jupiter plan contains multiple routes")
		}
		routeIndex = index
	}
	if routeIndex < 0 {
		return Intent{}, errors.New("Jupiter plan has no supported route")
	}
	intent, err := ValidateRouteV2(request, quote, instructions[routeIndex])
	if err != nil {
		return Intent{}, err
	}
	if intent.SourceTokenAccount != inputAccount ||
		intent.DestinationTokenAccount != outputAccount {
		return Intent{}, errors.New("Jupiter route does not use the taker's canonical token accounts")
	}
	if routeIndex != 1 {
		return Intent{}, errors.New("Jupiter wrapped-SOL output setup is not immediately before the route")
	}
	if !matchesATA(instructions[0], request.Taker, outputAccount, request.OutputMint) {
		return Intent{}, errors.New("Jupiter wrapped-SOL output setup is outside policy")
	}
	if len(instructions[routeIndex+1:]) != 1 ||
		!matchesTokenInstruction(instructions[routeIndex+1], 9, []solana.AccountMeta{
			{Address: outputAccount, Writable: true},
			{Address: request.Taker, Writable: true},
			{Address: request.Taker, Signer: true},
		}) {
		return Intent{}, errors.New("Jupiter wrapped-SOL output cleanup is outside policy")
	}
	return intent, nil
}

func matchesATA(instruction solana.Instruction, owner, account, mint string) bool {
	return matchesInstruction(instruction, orcaswap.AssociatedTokenProgram, []solana.AccountMeta{
		{Address: owner, Signer: true, Writable: true},
		{Address: account, Writable: true},
		{Address: owner},
		{Address: mint},
		{Address: orcaswap.SystemProgram},
		{Address: orcaswap.TokenProgram},
	}, []byte{1})
}

func matchesSystemTransfer(instruction solana.Instruction, owner, account string, amount uint64) bool {
	if instruction.Program != orcaswap.SystemProgram || len(instruction.Accounts) != 2 ||
		instruction.Accounts[0] != (solana.AccountMeta{Address: owner, Signer: true, Writable: true}) ||
		instruction.Accounts[1] != (solana.AccountMeta{Address: account, Writable: true}) ||
		len(instruction.Data) != 12 || binary.LittleEndian.Uint32(instruction.Data[:4]) != 2 {
		return false
	}
	return binary.LittleEndian.Uint64(instruction.Data[4:]) == amount
}

func matchesTokenInstruction(
	instruction solana.Instruction,
	discriminator byte,
	accounts []solana.AccountMeta,
) bool {
	return matchesInstruction(instruction, orcaswap.TokenProgram, accounts, []byte{discriminator})
}

func matchesInstruction(
	instruction solana.Instruction,
	program string,
	accounts []solana.AccountMeta,
	data []byte,
) bool {
	if instruction.Program != program || len(instruction.Accounts) != len(accounts) ||
		!bytes.Equal(instruction.Data, data) {
		return false
	}
	for index := range accounts {
		if instruction.Accounts[index] != accounts[index] {
			return false
		}
	}
	return true
}
