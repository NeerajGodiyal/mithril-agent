package orcaswap

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

func testPolicy() Policy {
	return Policy{
		Owner:                        "3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh",
		Pool:                         DevnetPool,
		InputMint:                    WrappedSOLMint,
		OutputMint:                   DevnetUSDCMint,
		InputTokenAccount:            "6ypgTYMnFZxR4iDLZfVQdeWWNjtXM67qGRbbMATRdv3w",
		OutputTokenAccount:           "AxPxVBmYMB44y2RdzLtGQfJgTbUdQ4DeEzy8cZQUmyQv",
		TokenVaultA:                  "C9zLV5zWF66j3rZj3uuhDqvfuA8esJyWnruGzDW9qEj2",
		TokenVaultB:                  "7DM3RMz2yzUB8yPRQM3FMZgdFrwZGMsabsfsKopWktoX",
		Oracle:                       "2KEWNc3b6EfqoWQpfKQMHh4mhRyKXYRdPbtGRTJX3Cip",
		ProgramData:                  WhirlpoolProgramData,
		UpgradeAuthority:             WhirlpoolUpgradeAuth,
		DeploymentSlot:               WhirlpoolDeploySlot,
		MaxInputLamports:             1_000_000,
		MinOutputAmount:              21_525,
		MaxSlippageBPS:               100,
		MaxOutputAccountRentLamports: DefaultMaxOutputAccountRentLamports,
	}
}

func TestDiscoverPolicyValidatesSDKRoute(t *testing.T) {
	want := testPolicy()
	got, err := DiscoverPolicy(want.Owner, Quote{
		InputAmount: 1_000_000, EstimatedOutput: 21_742,
		MinimumOutput: 21_525, SlippageBPS: 100,
	}, testInstructions())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("discovered policy = %+v, want %+v", got, want)
	}
}

func TestOracleAddressMatchesPinnedPool(t *testing.T) {
	got, err := OracleAddress(DevnetPool)
	if err != nil {
		t.Fatal(err)
	}
	if got != testPolicy().Oracle {
		t.Fatalf("oracle = %s, want %s", got, testPolicy().Oracle)
	}
}

func TestPolicyPinsPilotMarket(t *testing.T) {
	for name, mutate := range map[string]func(*Policy){
		"pool": func(policy *Policy) {
			policy.Pool = policy.TokenVaultA
		},
		"input mint": func(policy *Policy) {
			policy.InputMint = policy.TokenVaultA
		},
		"output mint": func(policy *Policy) {
			policy.OutputMint = policy.TokenVaultA
		},
		"input token account": func(policy *Policy) {
			policy.InputTokenAccount = policy.TokenVaultA
		},
		"output token account": func(policy *Policy) {
			policy.OutputTokenAccount = policy.TokenVaultB
		},
		"program data": func(policy *Policy) {
			policy.ProgramData = policy.TokenVaultB
		},
		"upgrade authority": func(policy *Policy) {
			policy.UpgradeAuthority = policy.TokenVaultB
		},
		"deployment slot": func(policy *Policy) {
			policy.DeploymentSlot++
		},
		"output account rent": func(policy *Policy) {
			policy.MaxOutputAccountRentLamports = 0
		},
		"oracle": func(policy *Policy) {
			policy.Oracle = policy.TokenVaultA
		},
	} {
		t.Run(name, func(t *testing.T) {
			policy := testPolicy()
			mutate(&policy)
			if err := policy.Validate(); err == nil {
				t.Fatal("different pilot market was accepted")
			}
		})
	}
}

func TestDiscoverPolicyRejectsConsistentlyRedirectedOutput(t *testing.T) {
	instructions := testInstructions()
	instructions[3].Accounts[9].Address = instructions[3].Accounts[11].Address
	if _, err := DiscoverPolicy(testPolicy().Owner, Quote{
		InputAmount: 1_000_000, EstimatedOutput: 21_742,
		MinimumOutput: 21_525, SlippageBPS: 100,
	}, instructions); err == nil {
		t.Fatal("adapter-controlled output account was accepted")
	}
}

func TestDiscoverPolicyBindsAbsoluteOutputFloor(t *testing.T) {
	policy, err := DiscoverPolicy(testPolicy().Owner, Quote{
		InputAmount: 1_000_000, EstimatedOutput: 21_742,
		MinimumOutput: 21_525, SlippageBPS: 100,
	}, testInstructions())
	if err != nil {
		t.Fatal(err)
	}
	if policy.MinOutputAmount != 21_525 {
		t.Fatalf("minimum output = %d, want 21525", policy.MinOutputAmount)
	}
}

func testInstructions() []solana.Instruction {
	policy := testPolicy()
	ata := func(tokenAccount, mint string) solana.Instruction {
		return solana.Instruction{
			Program: AssociatedTokenProgram,
			Accounts: []solana.AccountMeta{
				{Address: policy.Owner, Signer: true, Writable: true},
				{Address: tokenAccount, Writable: true},
				{Address: policy.Owner}, {Address: mint},
				{Address: SystemProgram}, {Address: TokenProgram},
			},
			Data: []byte{1},
		}
	}
	transferData := make([]byte, 12)
	binary.LittleEndian.PutUint32(transferData[:4], 2)
	binary.LittleEndian.PutUint64(transferData[4:], 1_000_000)
	swapData := make([]byte, 49)
	copy(swapData, swapV2Discriminator)
	binary.LittleEndian.PutUint64(swapData[8:16], 1_000_000)
	binary.LittleEndian.PutUint64(swapData[16:24], 21_525)
	copy(swapData[40:], []byte{1, 1, 1, 1, 0, 0, 0, 6, 2})
	return []solana.Instruction{
		ata(policy.InputTokenAccount, policy.InputMint),
		{
			Program: SystemProgram,
			Accounts: []solana.AccountMeta{
				{Address: policy.Owner, Signer: true, Writable: true},
				{Address: policy.InputTokenAccount, Writable: true},
			},
			Data: transferData,
		},
		{
			Program:  TokenProgram,
			Accounts: []solana.AccountMeta{{Address: policy.InputTokenAccount, Writable: true}},
			Data:     []byte{17},
		},
		{
			Program: WhirlpoolProgram,
			Accounts: []solana.AccountMeta{
				{Address: TokenProgram}, {Address: TokenProgram}, {Address: MemoProgram},
				{Address: policy.Owner, Signer: true},
				{Address: policy.Pool, Writable: true},
				{Address: policy.InputMint}, {Address: policy.OutputMint},
				{Address: policy.InputTokenAccount, Writable: true},
				{Address: policy.TokenVaultA, Writable: true},
				{Address: policy.OutputTokenAccount, Writable: true},
				{Address: policy.TokenVaultB, Writable: true},
				{Address: "7knZZ461yySGbSEHeBUwEpg3VtAkQy8B9tp78RGgyUHE", Writable: true},
				{Address: "CpoSFo3ajrizueggtJr2ZjvYgdtkgugXtvhqcwkyCkKP", Writable: true},
				{Address: "9iGzy4mQtJadZDuH8seBFQGiqcb6wyp2KW4M6NKHvsAW", Writable: true},
				{Address: policy.Oracle, Writable: true},
				{Address: "3aBJJLAR3QxGcGsesNXeW3f64Rv3TckF7EQ6sXtAuvGM", Writable: true},
				{Address: "A1vrG379E5ttoaWmyQBiunsMdyrpoUp7mSQwu8DgLcip", Writable: true},
			},
			Data: swapData,
		},
		{
			Program: TokenProgram,
			Accounts: []solana.AccountMeta{
				{Address: policy.InputTokenAccount, Writable: true},
				{Address: policy.Owner, Writable: true},
				{Address: policy.Owner, Signer: true},
			},
			Data: []byte{9},
		},
	}
}

func TestValidateInstructionsAndCompiledMessage(t *testing.T) {
	policy := testPolicy()
	instructions := testInstructions()
	intent, err := ValidateInstructions(policy, Quote{
		InputAmount: 1_000_000, EstimatedOutput: 21_742,
		MinimumOutput: 21_525, SlippageBPS: 100,
	}, instructions)
	if err != nil {
		t.Fatal(err)
	}
	if intent.InputAmount != 1_000_000 || intent.MinimumOutput != 21_525 {
		t.Fatalf("intent = %+v", intent)
	}
	message, err := solana.BuildLegacyMessage(
		policy.Owner,
		solana.Encode(bytes.Repeat([]byte{9}, 32)),
		instructions,
	)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeMessage(policy, message)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.RecentBlockhash != solana.Encode(bytes.Repeat([]byte{9}, 32)) {
		t.Fatalf("recent blockhash = %q", decoded.RecentBlockhash)
	}

	withOutputCreate := append([]solana.Instruction{instructions[0]},
		ataInstruction(policy, policy.OutputTokenAccount, policy.OutputMint))
	withOutputCreate = append(withOutputCreate, instructions[1:]...)
	created, err := ValidateInstructions(policy, Quote{
		InputAmount: 1_000_000, EstimatedOutput: 21_742,
		MinimumOutput: 21_525, SlippageBPS: 100,
	}, withOutputCreate)
	if err != nil || !created.OutputAccountCreated {
		t.Fatalf("canonical output token account setup = %+v, %v", created, err)
	}
	withOutputCreate[1].Accounts[1].Address = policy.TokenVaultB
	if _, err := ValidateInstructions(policy, Quote{
		InputAmount: 1_000_000, EstimatedOutput: 21_742,
		MinimumOutput: 21_525, SlippageBPS: 100,
	}, withOutputCreate); err == nil {
		t.Fatal("redirected output token account setup was accepted")
	}
}

func TestDecodeMessageRejectsExpandedOrReducedGlobalPrivileges(t *testing.T) {
	policy := testPolicy()
	tests := map[string]func([]solana.Instruction){
		"writable input mint": func(instructions []solana.Instruction) {
			instructions[0].Accounts[3].Writable = true
		},
		"writable output mint": func(instructions []solana.Instruction) {
			instructions[3].Accounts[6].Writable = true
		},
		"readonly pool": func(instructions []solana.Instruction) {
			instructions[3].Accounts[4].Writable = false
		},
		"readonly vault": func(instructions []solana.Instruction) {
			instructions[3].Accounts[8].Writable = false
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			instructions := testInstructions()
			mutate(instructions)
			message, err := solana.BuildLegacyMessage(
				policy.Owner, solana.Encode(bytes.Repeat([]byte{9}, 32)), instructions,
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeMessage(policy, message); err == nil {
				t.Fatal("noncanonical global privileges were accepted")
			}
		})
	}
}

func ataInstruction(policy Policy, tokenAccount, mint string) solana.Instruction {
	return solana.Instruction{
		Program: AssociatedTokenProgram,
		Accounts: []solana.AccountMeta{
			{Address: policy.Owner, Signer: true, Writable: true},
			{Address: tokenAccount, Writable: true},
			{Address: policy.Owner}, {Address: mint},
			{Address: SystemProgram}, {Address: TokenProgram},
		},
		Data: []byte{1},
	}
}

func TestValidateInstructionsRejectsAuthorityAndAmountMutations(t *testing.T) {
	policy := testPolicy()
	for name, mutate := range map[string]func([]solana.Instruction){
		"extra signer": func(instructions []solana.Instruction) {
			instructions[3].Accounts[11].Signer = true
		},
		"redirected output": func(instructions []solana.Instruction) {
			instructions[3].Accounts[9].Address = instructions[3].Accounts[11].Address
		},
		"lower threshold": func(instructions []solana.Instruction) {
			binary.LittleEndian.PutUint64(instructions[3].Data[16:24], 0)
		},
		"different program": func(instructions []solana.Instruction) {
			instructions[3].Program = SystemProgram
		},
		"oracle": func(instructions []solana.Instruction) {
			instructions[3].Accounts[14].Address = policy.UpgradeAuthority
		},
		"wrong close target": func(instructions []solana.Instruction) {
			instructions[4].Accounts[1].Address = instructions[3].Accounts[11].Address
		},
	} {
		t.Run(name, func(t *testing.T) {
			instructions := testInstructions()
			mutate(instructions)
			if _, err := ValidateInstructions(policy, Quote{
				InputAmount: 1_000_000, EstimatedOutput: 21_742,
				MinimumOutput: 21_525, SlippageBPS: 100,
			}, instructions); err == nil {
				t.Fatal("mutated swap was accepted")
			}
		})
	}
}
