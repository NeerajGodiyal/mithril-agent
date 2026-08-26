package jupiterswap

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestValidateExactInSOLAcceptsOnlyTheBoundedPlan(t *testing.T) {
	request, quote, instructions := exactInSOLFixture(t)
	intent, err := ValidateExactInSOL(request, quote, instructions)
	if err != nil {
		t.Fatal(err)
	}
	if intent.InputAmount != request.InputAmount || intent.MinimumOutput != quote.MinimumOutput {
		t.Fatalf("intent = %+v", intent)
	}

	mutations := map[string]func([]solana.Instruction) []solana.Instruction{
		"extra instruction": func(value []solana.Instruction) []solana.Instruction {
			return append([]solana.Instruction{{Program: orcaswap.SystemProgram}}, value...)
		},
		"missing input setup": func(value []solana.Instruction) []solana.Instruction {
			return append(value[:0:0], value[1:]...)
		},
		"wrong transfer amount": func(value []solana.Instruction) []solana.Instruction {
			binary.LittleEndian.PutUint64(value[1].Data[4:], request.InputAmount+1)
			return value
		},
		"sync before funding": func(value []solana.Instruction) []solana.Instruction {
			value[1], value[2] = value[2], value[1]
			return value
		},
		"multiple routes": func(value []solana.Instruction) []solana.Instruction {
			return append(value, value[3])
		},
		"missing cleanup": func(value []solana.Instruction) []solana.Instruction {
			return value[:len(value)-1]
		},
		"wrong cleanup destination": func(value []solana.Instruction) []solana.Instruction {
			value[len(value)-1].Accounts[1].Address = value[3].Accounts[2].Address
			return value
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			copy := cloneInstructions(instructions)
			if _, err := ValidateExactInSOL(request, quote, mutate(copy)); err == nil {
				t.Fatal("accepted mutated Jupiter plan")
			}
		})
	}
}

func TestValidateExactInSOLAllowsIdempotentOutputSetup(t *testing.T) {
	request, quote, instructions := exactInSOLFixture(t)
	outputAccount, err := orcaswap.AssociatedTokenAddress(request.Taker, request.OutputMint)
	if err != nil {
		t.Fatal(err)
	}
	outputATA := solana.Instruction{
		Program: orcaswap.AssociatedTokenProgram,
		Accounts: []solana.AccountMeta{
			{Address: request.Taker, Signer: true, Writable: true},
			{Address: outputAccount, Writable: true},
			{Address: request.Taker},
			{Address: request.OutputMint},
			{Address: orcaswap.SystemProgram},
			{Address: orcaswap.TokenProgram},
		},
		Data: []byte{1},
	}
	instructions = append(instructions[:3], append([]solana.Instruction{outputATA}, instructions[3:]...)...)
	if _, err := ValidateExactInSOL(request, quote, instructions); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveRedundantOutputAccountSetupKeepsThePrecreatedPolicyShape(t *testing.T) {
	request, quote, instructions := exactInSOLFixture(t)
	outputAccount, err := orcaswap.AssociatedTokenAddress(request.Taker, request.OutputMint)
	if err != nil {
		t.Fatal(err)
	}
	outputATA := solana.Instruction{
		Program: orcaswap.AssociatedTokenProgram,
		Accounts: []solana.AccountMeta{
			{Address: request.Taker, Signer: true, Writable: true},
			{Address: outputAccount, Writable: true},
			{Address: request.Taker},
			{Address: request.OutputMint},
			{Address: orcaswap.SystemProgram},
			{Address: orcaswap.TokenProgram},
		},
		Data: []byte{1},
	}
	withSetup := append(instructions[:3:3], append([]solana.Instruction{outputATA}, instructions[3:]...)...)
	withoutSetup, err := RemoveRedundantOutputAccountSetup(request, withSetup)
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutSetup) != len(instructions) {
		t.Fatalf("normalized instruction count = %d, want %d", len(withoutSetup), len(instructions))
	}
	intent, err := ValidateExactInSOL(request, quote, withoutSetup)
	if err != nil {
		t.Fatal(err)
	}
	if intent.OutputAccountCreated {
		t.Fatal("normalized pre-created output account still consumes rent")
	}
	if _, err := RemoveRedundantOutputAccountSetup(
		request, append(withSetup, outputATA),
	); err == nil {
		t.Fatal("duplicate output-account setup was normalized")
	}
	wrong := request
	wrong.DestinationTokenAccount = request.Taker
	if _, err := RemoveRedundantOutputAccountSetup(wrong, withSetup); err == nil {
		t.Fatal("non-canonical output account was normalized")
	}
}

func TestValidateExactInSOLAllowsTheBoundedSharedRoute(t *testing.T) {
	request, quote, instructions := exactInSOLFixture(t)
	inputAccount, err := orcaswap.AssociatedTokenAddress(request.Taker, request.InputMint)
	if err != nil {
		t.Fatal(err)
	}
	outputAccount, err := orcaswap.AssociatedTokenAddress(request.Taker, request.OutputMint)
	if err != nil {
		t.Fatal(err)
	}
	instructions[3] = planSharedAccountsRouteV2Fixture(
		t, request, quote, inputAccount, outputAccount,
	)
	if _, err := ValidateExactInSOL(request, quote, instructions); err != nil {
		t.Fatal(err)
	}
}

func TestValidateExactInTokenToSOLAcceptsOnlyTheBoundedPlan(t *testing.T) {
	request, quote, instructions := exactInTokenToSOLFixture(t)
	intent, err := validateExactInTokenToSOL(request, quote, instructions)
	if err != nil {
		t.Fatal(err)
	}
	if intent.InputAmount != request.InputAmount || intent.MinimumOutput != quote.MinimumOutput ||
		intent.OutputAccountCreated {
		t.Fatalf("intent = %+v", intent)
	}

	mutations := map[string]func([]solana.Instruction) []solana.Instruction{
		"extra setup": func(value []solana.Instruction) []solana.Instruction {
			return append([]solana.Instruction{{Program: orcaswap.SystemProgram}}, value...)
		},
		"missing setup": func(value []solana.Instruction) []solana.Instruction {
			return append(value[:0:0], value[1:]...)
		},
		"source setup": func(value []solana.Instruction) []solana.Instruction {
			inputAccount, _ := orcaswap.AssociatedTokenAddress(request.Taker, request.InputMint)
			value[0].Accounts[1].Address = inputAccount
			value[0].Accounts[3].Address = request.InputMint
			return value
		},
		"multiple routes": func(value []solana.Instruction) []solana.Instruction {
			return append(value, value[1])
		},
		"missing cleanup": func(value []solana.Instruction) []solana.Instruction {
			return value[:len(value)-1]
		},
		"wrong cleanup destination": func(value []solana.Instruction) []solana.Instruction {
			value[len(value)-1].Accounts[1].Address = value[1].Accounts[1].Address
			return value
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := validateExactInTokenToSOL(
				request, quote, mutate(cloneInstructions(instructions)),
			); err == nil {
				t.Fatal("accepted mutated Jupiter reverse plan")
			}
		})
	}
}

func TestValidateExactInTokenToSOLAllowsTheBoundedSharedRoute(t *testing.T) {
	request, quote, instructions := exactInTokenToSOLFixture(t)
	inputAccount, err := orcaswap.AssociatedTokenAddress(request.Taker, request.InputMint)
	if err != nil {
		t.Fatal(err)
	}
	outputAccount, err := orcaswap.AssociatedTokenAddress(request.Taker, request.OutputMint)
	if err != nil {
		t.Fatal(err)
	}
	instructions[1] = planSharedAccountsRouteV2Fixture(
		t, request, quote, inputAccount, outputAccount,
	)
	if _, err := validateExactInTokenToSOL(request, quote, instructions); err != nil {
		t.Fatal(err)
	}
}

func exactInSOLFixture(t *testing.T) (jupiterquote.Request, jupiterquote.Result, []solana.Instruction) {
	t.Helper()
	return exactInSOLFixtureForTaker(t, solana.Encode(bytes.Repeat([]byte{1}, 32)))
}

func exactInTokenToSOLFixture(
	t *testing.T,
) (jupiterquote.Request, jupiterquote.Result, []solana.Instruction) {
	t.Helper()
	taker := solana.Encode(bytes.Repeat([]byte{1}, 32))
	inputMint := solana.Encode(bytes.Repeat([]byte{2}, 32))
	inputAccount, err := orcaswap.AssociatedTokenAddress(taker, inputMint)
	if err != nil {
		t.Fatal(err)
	}
	outputAccount, err := orcaswap.AssociatedTokenAddress(taker, orcaswap.WrappedSOLMint)
	if err != nil {
		t.Fatal(err)
	}
	request := jupiterquote.Request{
		Taker: taker, InputMint: inputMint, OutputMint: orcaswap.WrappedSOLMint,
		InputAmount: 20, SlippageBPS: 50,
	}
	quote := jupiterquote.Result{InputAmount: 20, EstimatedOutput: 10, MinimumOutput: 10}
	return request, quote, []solana.Instruction{
		{
			Program: orcaswap.AssociatedTokenProgram,
			Accounts: []solana.AccountMeta{
				{Address: taker, Signer: true, Writable: true},
				{Address: outputAccount, Writable: true},
				{Address: taker},
				{Address: request.OutputMint},
				{Address: orcaswap.SystemProgram},
				{Address: orcaswap.TokenProgram},
			},
			Data: []byte{1},
		},
		planRouteV2Fixture(request, quote, inputAccount, outputAccount),
		{
			Program: orcaswap.TokenProgram,
			Accounts: []solana.AccountMeta{
				{Address: outputAccount, Writable: true},
				{Address: taker, Writable: true},
				{Address: taker, Signer: true},
			},
			Data: []byte{9},
		},
	}
}

func exactInSOLFixtureForTaker(
	t *testing.T,
	taker string,
) (jupiterquote.Request, jupiterquote.Result, []solana.Instruction) {
	t.Helper()
	outputMint := solana.Encode(bytes.Repeat([]byte{2}, 32))
	inputAccount, err := orcaswap.AssociatedTokenAddress(taker, orcaswap.WrappedSOLMint)
	if err != nil {
		t.Fatal(err)
	}
	outputAccount, err := orcaswap.AssociatedTokenAddress(taker, outputMint)
	if err != nil {
		t.Fatal(err)
	}
	request := jupiterquote.Request{
		Taker: taker, InputMint: orcaswap.WrappedSOLMint, OutputMint: outputMint,
		DestinationTokenAccount: outputAccount, InputAmount: 10, SlippageBPS: 50,
	}
	quote := jupiterquote.Result{InputAmount: 10, EstimatedOutput: 20, MinimumOutput: 20}
	transfer := make([]byte, 12)
	binary.LittleEndian.PutUint32(transfer[:4], 2)
	binary.LittleEndian.PutUint64(transfer[4:], request.InputAmount)
	return request, quote, []solana.Instruction{
		{
			Program: orcaswap.AssociatedTokenProgram,
			Accounts: []solana.AccountMeta{
				{Address: taker, Signer: true, Writable: true},
				{Address: inputAccount, Writable: true},
				{Address: taker},
				{Address: request.InputMint},
				{Address: orcaswap.SystemProgram},
				{Address: orcaswap.TokenProgram},
			},
			Data: []byte{1},
		},
		{
			Program: orcaswap.SystemProgram,
			Accounts: []solana.AccountMeta{
				{Address: taker, Signer: true, Writable: true},
				{Address: inputAccount, Writable: true},
			},
			Data: transfer,
		},
		{
			Program:  orcaswap.TokenProgram,
			Accounts: []solana.AccountMeta{{Address: inputAccount, Writable: true}},
			Data:     []byte{17},
		},
		planRouteV2Fixture(request, quote, inputAccount, outputAccount),
		{
			Program: orcaswap.TokenProgram,
			Accounts: []solana.AccountMeta{
				{Address: inputAccount, Writable: true},
				{Address: taker, Writable: true},
				{Address: taker, Signer: true},
			},
			Data: []byte{9},
		},
	}
}

func planRouteV2Fixture(
	request jupiterquote.Request,
	quote jupiterquote.Result,
	inputAccount, outputAccount string,
) solana.Instruction {
	destination := solana.AccountMeta{Address: Program}
	if request.DestinationTokenAccount != "" {
		destination = solana.AccountMeta{
			Address: request.DestinationTokenAccount, Writable: true,
		}
	}
	data := append([]byte(nil), routeV2Discriminator[:]...)
	data = binary.LittleEndian.AppendUint64(data, request.InputAmount)
	data = binary.LittleEndian.AppendUint64(data, quote.EstimatedOutput)
	data = binary.LittleEndian.AppendUint16(data, request.SlippageBPS)
	data = binary.LittleEndian.AppendUint16(data, 0)
	data = binary.LittleEndian.AppendUint16(data, 0)
	data = binary.LittleEndian.AppendUint32(data, 1)
	data = append(data, 17, 1, 0x10, 0x27, 0, 1)
	return solana.Instruction{
		Program: Program,
		Accounts: []solana.AccountMeta{
			{Address: request.Taker, Signer: true},
			{Address: inputAccount, Writable: true},
			{Address: outputAccount, Writable: true},
			{Address: request.InputMint},
			{Address: request.OutputMint},
			{Address: tokenProgram},
			{Address: tokenProgram},
			destination,
			{Address: eventAuthority},
			{Address: Program},
			{Address: solana.Encode(bytes.Repeat([]byte{3}, 32)), Writable: true},
		},
		Data: data,
	}
}

func planSharedAccountsRouteV2Fixture(
	t *testing.T,
	request jupiterquote.Request,
	quote jupiterquote.Result,
	inputAccount, outputAccount string,
) solana.Instruction {
	t.Helper()
	const authorityID = byte(9)
	authority, _, err := solana.FindProgramAddress(
		[][]byte{[]byte("authority"), {authorityID}}, Program,
	)
	if err != nil {
		t.Fatal(err)
	}
	programSource, err := orcaswap.AssociatedTokenAddress(authority, request.InputMint)
	if err != nil {
		t.Fatal(err)
	}
	programDestination, err := orcaswap.AssociatedTokenAddress(authority, request.OutputMint)
	if err != nil {
		t.Fatal(err)
	}
	data := append([]byte(nil), sharedAccountsRouteV2Discriminator[:]...)
	data = append(data, authorityID)
	data = binary.LittleEndian.AppendUint64(data, request.InputAmount)
	data = binary.LittleEndian.AppendUint64(data, quote.EstimatedOutput)
	data = binary.LittleEndian.AppendUint16(data, request.SlippageBPS)
	data = binary.LittleEndian.AppendUint16(data, 0)
	data = binary.LittleEndian.AppendUint16(data, 0)
	data = binary.LittleEndian.AppendUint32(data, 1)
	data = append(data, 17, 1, 0x10, 0x27, 0, 1)
	return solana.Instruction{
		Program: Program,
		Accounts: []solana.AccountMeta{
			{Address: authority},
			{Address: request.Taker, Signer: true},
			{Address: inputAccount, Writable: true},
			{Address: programSource, Writable: true},
			{Address: programDestination, Writable: true},
			{Address: outputAccount, Writable: true},
			{Address: request.InputMint},
			{Address: request.OutputMint},
			{Address: tokenProgram},
			{Address: tokenProgram},
			{Address: eventAuthority},
			{Address: Program},
			{Address: solana.Encode(bytes.Repeat([]byte{3}, 32)), Writable: true},
		},
		Data: data,
	}
}

func cloneInstructions(value []solana.Instruction) []solana.Instruction {
	copy := make([]solana.Instruction, len(value))
	for index := range value {
		copy[index] = value[index]
		copy[index].Accounts = append([]solana.AccountMeta(nil), value[index].Accounts...)
		copy[index].Data = append([]byte(nil), value[index].Data...)
	}
	return copy
}
