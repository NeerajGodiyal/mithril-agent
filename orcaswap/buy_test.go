package orcaswap

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

func testBuyPolicyV2() BuyPolicyV2 {
	return BuyPolicyV2{
		Owner:                    "3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh",
		Pool:                     DevnetPool,
		TokenMintA:               WrappedSOLMint,
		TokenMintB:               DevnetUSDCMint,
		InputTokenAccount:        "AxPxVBmYMB44y2RdzLtGQfJgTbUdQ4DeEzy8cZQUmyQv",
		TokenVaultA:              "C9zLV5zWF66j3rZj3uuhDqvfuA8esJyWnruGzDW9qEj2",
		TokenVaultB:              "7DM3RMz2yzUB8yPRQM3FMZgdFrwZGMsabsfsKopWktoX",
		Oracle:                   "2KEWNc3b6EfqoWQpfKQMHh4mhRyKXYRdPbtGRTJX3Cip",
		ProgramData:              WhirlpoolProgramData,
		UpgradeAuthority:         WhirlpoolUpgradeAuth,
		DeploymentSlot:           WhirlpoolDeploySlot,
		MaxInputTokenAmount:      1_000,
		MinOutputLamports:        45_348,
		MaxSlippageBPS:           100,
		MaxTemporaryRentLamports: DefaultMaxTemporaryRentLamports,
	}
}

func TestDiscoverBuyPolicyV2ValidatesPinnedSDKSteadyStateRoute(t *testing.T) {
	policy := testBuyPolicyV2()
	quote := Quote{
		InputAmount: 1_000, EstimatedOutput: 45_807,
		MinimumOutput: 45_348, SlippageBPS: 100,
	}
	got, err := DiscoverBuyPolicyV2(policy.Owner, quote, testBuyInstructionsV2(t))
	if err != nil {
		t.Fatal(err)
	}
	if got != policy {
		t.Fatalf("discovered buy policy = %+v, want %+v", got, policy)
	}
	message, err := solana.BuildLegacyMessage(
		policy.Owner,
		solana.Encode(make([]byte, 32)),
		testBuyInstructionsV2(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeBuyMessageV2(policy, message); err == nil {
		t.Fatal("buy decoder accepted a zero blockhash")
	}
	message, err = solana.BuildLegacyMessage(
		policy.Owner,
		solana.Encode(bytes.Repeat([]byte{1}, 32)),
		testBuyInstructionsV2(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := DecodeBuyMessageV2(policy, message)
	if err != nil {
		t.Fatal(err)
	}
	if intent.InputAmount != 1_000 || intent.MinimumOutputLamports != 45_348 ||
		intent.TemporaryRentLamports != 2_039_280 || intent.TemporarySeed != "1785688960889" ||
		intent.TemporaryWSOLAccount != "9pp3xc9GoMHQBXaQK8avrihn6A5q3Jxr2pMCYaPnDFgQ" {
		t.Fatalf("decoded buy intent = %+v", intent)
	}
	if _, err := DecodeMessage(testPolicy(), message); err == nil {
		t.Fatal("sell-only decoder accepted a buy message")
	}
}

func TestBuyActionIDCannotReplayAsSell(t *testing.T) {
	fingerprint := strings.Repeat("01", 32)
	buy, err := ComputeBuyActionID(fingerprint, 1_785_369_600)
	if err != nil {
		t.Fatal(err)
	}
	sell, err := ComputeActionID(fingerprint, 1_785_369_600)
	if err != nil {
		t.Fatal(err)
	}
	if buy == sell {
		t.Fatal("buy and sell action IDs match")
	}
}

func TestBuyPolicyV2RejectsRouteAndLimitMutations(t *testing.T) {
	mutations := map[string]func(*BuyPolicyV2){
		"pool":                  func(p *BuyPolicyV2) { p.Pool = p.TokenVaultA },
		"token mint A":          func(p *BuyPolicyV2) { p.TokenMintA = p.TokenVaultA },
		"token mint B":          func(p *BuyPolicyV2) { p.TokenMintB = p.TokenVaultB },
		"input token account":   func(p *BuyPolicyV2) { p.InputTokenAccount = p.TokenVaultB },
		"oracle":                func(p *BuyPolicyV2) { p.Oracle = p.UpgradeAuthority },
		"program data":          func(p *BuyPolicyV2) { p.ProgramData = p.TokenVaultA },
		"upgrade authority":     func(p *BuyPolicyV2) { p.UpgradeAuthority = p.TokenVaultB },
		"deployment slot":       func(p *BuyPolicyV2) { p.DeploymentSlot++ },
		"input cap":             func(p *BuyPolicyV2) { p.MaxInputTokenAmount = 0 },
		"output floor":          func(p *BuyPolicyV2) { p.MinOutputLamports = 0 },
		"slippage":              func(p *BuyPolicyV2) { p.MaxSlippageBPS = 501 },
		"temporary rent":        func(p *BuyPolicyV2) { p.MaxTemporaryRentLamports = 0 },
		"excess temporary rent": func(p *BuyPolicyV2) { p.MaxTemporaryRentLamports = 10_000_001 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			policy := testBuyPolicyV2()
			mutate(&policy)
			if err := policy.Validate(); err == nil {
				t.Fatal("mutated buy policy was accepted")
			}
		})
	}
}

func TestBuyPolicyV2RejectsInputAccountCreation(t *testing.T) {
	policy := testBuyPolicyV2()
	setup := solana.Instruction{
		Program: AssociatedTokenProgram,
		Accounts: []solana.AccountMeta{
			{Address: policy.Owner, Signer: true, Writable: true},
			{Address: policy.InputTokenAccount, Writable: true},
			{Address: policy.Owner}, {Address: policy.TokenMintB},
			{Address: SystemProgram}, {Address: TokenProgram},
		},
		Data: []byte{1},
	}
	instructions := append([]solana.Instruction{setup}, testBuyInstructionsV2(t)...)
	if _, err := DiscoverBuyPolicyV2(policy.Owner, Quote{
		InputAmount: 1_000, EstimatedOutput: 45_807,
		MinimumOutput: 45_348, SlippageBPS: 100,
	}, instructions); err == nil {
		t.Fatal("buy policy accepted permanent input-account creation")
	}
}

func TestBuyQuoteV2RejectsLimitMutations(t *testing.T) {
	policy := testBuyPolicyV2()
	base := Quote{
		InputAmount: 1_000, EstimatedOutput: 45_807,
		MinimumOutput: 45_348, SlippageBPS: 100,
	}
	mutations := map[string]func(*Quote){
		"input above cap":        func(q *Quote) { q.InputAmount++ },
		"estimate zero":          func(q *Quote) { q.EstimatedOutput = 0 },
		"minimum above estimate": func(q *Quote) { q.MinimumOutput = q.EstimatedOutput + 1 },
		"minimum below floor":    func(q *Quote) { q.MinimumOutput = policy.MinOutputLamports - 1 },
		"slippage zero":          func(q *Quote) { q.SlippageBPS = 0 },
		"slippage above cap":     func(q *Quote) { q.SlippageBPS = policy.MaxSlippageBPS + 1 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			quote := base
			mutate(&quote)
			if _, err := ValidateBuyInstructionsV2(policy, quote, testBuyInstructionsV2(t)); err == nil {
				t.Fatal("buy quote outside policy was accepted")
			}
		})
	}
}

func TestBuyInstructionsV2RejectMutations(t *testing.T) {
	policy := testBuyPolicyV2()
	quote := Quote{
		InputAmount: 1_000, EstimatedOutput: 45_807,
		MinimumOutput: 45_348, SlippageBPS: 100,
	}
	mutations := map[string]func([]solana.Instruction) []solana.Instruction{
		"missing instruction": func(value []solana.Instruction) []solana.Instruction {
			return value[:3]
		},
		"temporary account": func(value []solana.Instruction) []solana.Instruction {
			value[0].Accounts[1].Address = policy.Owner
			return value
		},
		"seed": func(value []solana.Instruction) []solana.Instruction {
			value[0].Data[44] = '2'
			return value
		},
		"seed character": func(value []solana.Instruction) []solana.Instruction {
			value[0].Data[44] = 'x'
			return value
		},
		"seed length": func(value []solana.Instruction) []solana.Instruction {
			value[0].Data[36] = 12
			return value
		},
		"create base": func(value []solana.Instruction) []solana.Instruction {
			value[0].Data[4]++
			return value
		},
		"rent": func(value []solana.Instruction) []solana.Instruction {
			value[0].Data[64] = 0xff
			return value
		},
		"rent zero": func(value []solana.Instruction) []solana.Instruction {
			clear(value[0].Data[57:65])
			return value
		},
		"space": func(value []solana.Instruction) []solana.Instruction {
			value[0].Data[65]++
			return value
		},
		"create owner": func(value []solana.Instruction) []solana.Instruction {
			value[0].Data[len(value[0].Data)-1]++
			return value
		},
		"create trailing data": func(value []solana.Instruction) []solana.Instruction {
			value[0].Data = append(value[0].Data, 0)
			return value
		},
		"initialize mint": func(value []solana.Instruction) []solana.Instruction {
			value[1].Accounts[1].Address = policy.TokenMintB
			return value
		},
		"initialize owner": func(value []solana.Instruction) []solana.Instruction {
			value[1].Data[len(value[1].Data)-1]++
			return value
		},
		"input token account": func(value []solana.Instruction) []solana.Instruction {
			value[2].Accounts[9].Address = policy.TokenVaultB
			return value
		},
		"oracle": func(value []solana.Instruction) []solana.Instruction {
			value[2].Accounts[14].Address = policy.UpgradeAuthority
			return value
		},
		"direction": func(value []solana.Instruction) []solana.Instruction {
			value[2].Data[41] = 1
			return value
		},
		"input amount": func(value []solana.Instruction) []solana.Instruction {
			value[2].Data[8]++
			return value
		},
		"minimum output": func(value []solana.Instruction) []solana.Instruction {
			value[2].Data[16]++
			return value
		},
		"exact output": func(value []solana.Instruction) []solana.Instruction {
			value[2].Data[40] = 0
			return value
		},
		"extra signer": func(value []solana.Instruction) []solana.Instruction {
			value[2].Accounts[11].Signer = true
			return value
		},
		"close destination": func(value []solana.Instruction) []solana.Instruction {
			value[3].Accounts[1].Address = policy.TokenVaultA
			return value
		},
		"close authority": func(value []solana.Instruction) []solana.Instruction {
			value[3].Accounts[2].Address = policy.TokenVaultA
			return value
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			instructions := mutate(testBuyInstructionsV2(t))
			if _, err := ValidateBuyInstructionsV2(policy, quote, instructions); err == nil {
				t.Fatal("mutated buy instructions were accepted")
			}
		})
	}
}

func TestDecodeBuyMessageV2RejectsGlobalPrivilegeAndAccountMutations(t *testing.T) {
	policy := testBuyPolicyV2()
	mutations := map[string]func([]solana.Instruction){
		"writable mint": func(value []solana.Instruction) {
			value[2].Accounts[5].Writable = true
		},
		"readonly pool": func(value []solana.Instruction) {
			value[2].Accounts[4].Writable = false
		},
		"readonly input": func(value []solana.Instruction) {
			value[2].Accounts[9].Writable = false
		},
		"readonly temporary account": func(value []solana.Instruction) {
			value[0].Accounts[1].Writable = false
			value[1].Accounts[0].Writable = false
			value[2].Accounts[7].Writable = false
			value[3].Accounts[0].Writable = false
		},
		"writable program": func(value []solana.Instruction) {
			value[2].Accounts[0].Writable = true
		},
		"duplicate dynamic account": func(value []solana.Instruction) {
			value[2].Accounts[15].Address = value[2].Accounts[11].Address
		},
		"different oracle": func(value []solana.Instruction) {
			value[2].Accounts[14].Address = policy.UpgradeAuthority
		},
		"extra signer": func(value []solana.Instruction) {
			value[2].Accounts[11].Signer = true
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			instructions := testBuyInstructionsV2(t)
			mutate(instructions)
			message, err := solana.BuildLegacyMessage(
				policy.Owner,
				solana.Encode(bytes.Repeat([]byte{1}, 32)),
				instructions,
			)
			if err != nil {
				return
			}
			if _, err := DecodeBuyMessageV2(policy, message); err == nil {
				t.Fatal("mutated buy message was accepted")
			}
		})
	}
}

func FuzzDecodeBuyMessageV2(f *testing.F) {
	policy := testBuyPolicyV2()
	message, err := solana.BuildLegacyMessage(
		policy.Owner,
		solana.Encode(bytes.Repeat([]byte{1}, 32)),
		testBuyInstructionsV2(f),
	)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(message)
	f.Fuzz(func(t *testing.T, value []byte) {
		_, _ = DecodeBuyMessageV2(policy, value)
	})
}

func testBuyInstructionsV2(t testing.TB) []solana.Instruction {
	t.Helper()
	owner := testBuyPolicyV2().Owner
	temporary := "9pp3xc9GoMHQBXaQK8avrihn6A5q3Jxr2pMCYaPnDFgQ"
	data := func(value string) []byte {
		decoded, err := base64.StdEncoding.Strict().DecodeString(value)
		if err != nil {
			t.Fatal(err)
		}
		return decoded
	}
	return []solana.Instruction{
		{
			Program: SystemProgram,
			Accounts: []solana.AccountMeta{
				{Address: owner, Signer: true, Writable: true},
				{Address: temporary, Writable: true},
				{Address: owner, Signer: true},
			},
			Data: data("AwAAACoqKioqKioqKioqKioqKioqKioqKioqKioqKioqKioqDQAAAAAAAAAxNzg1Njg4OTYwODg58B0fAAAAAAClAAAAAAAAAAbd9uHXZaGT2cvhRs7reawctIXtX1s3kTqM9YV+/wCp"),
		},
		{
			Program: TokenProgram,
			Accounts: []solana.AccountMeta{
				{Address: temporary, Writable: true}, {Address: WrappedSOLMint},
			},
			Data: data("EioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKioq"),
		},
		{
			Program: WhirlpoolProgram,
			Accounts: []solana.AccountMeta{
				{Address: TokenProgram}, {Address: TokenProgram}, {Address: MemoProgram},
				{Address: owner, Signer: true}, {Address: DevnetPool, Writable: true},
				{Address: WrappedSOLMint}, {Address: DevnetUSDCMint},
				{Address: temporary, Writable: true},
				{Address: "C9zLV5zWF66j3rZj3uuhDqvfuA8esJyWnruGzDW9qEj2", Writable: true},
				{Address: "AxPxVBmYMB44y2RdzLtGQfJgTbUdQ4DeEzy8cZQUmyQv", Writable: true},
				{Address: "7DM3RMz2yzUB8yPRQM3FMZgdFrwZGMsabsfsKopWktoX", Writable: true},
				{Address: "7knZZ461yySGbSEHeBUwEpg3VtAkQy8B9tp78RGgyUHE", Writable: true},
				{Address: "CpoSFo3ajrizueggtJr2ZjvYgdtkgugXtvhqcwkyCkKP", Writable: true},
				{Address: "9iGzy4mQtJadZDuH8seBFQGiqcb6wyp2KW4M6NKHvsAW", Writable: true},
				{Address: "2KEWNc3b6EfqoWQpfKQMHh4mhRyKXYRdPbtGRTJX3Cip", Writable: true},
				{Address: "3aBJJLAR3QxGcGsesNXeW3f64Rv3TckF7EQ6sXtAuvGM", Writable: true},
				{Address: "A1vrG379E5ttoaWmyQBiunsMdyrpoUp7mSQwu8DgLcip", Writable: true},
			},
			Data: data("KwTtCxrJHmLoAwAAAAAAACSxAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAEAAQEAAAAGAg=="),
		},
		{
			Program: TokenProgram,
			Accounts: []solana.AccountMeta{
				{Address: temporary, Writable: true},
				{Address: owner, Writable: true},
				{Address: owner, Signer: true},
			},
			Data: []byte{9},
		},
	}
}
