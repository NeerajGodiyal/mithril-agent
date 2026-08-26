package jupiterswap

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestValidateV0MessageRevalidatesTheCanonicalWirePlan(t *testing.T) {
	request, quote, plan := exactInSOLFixture(t)
	policy := Policy{
		Owner: request.Taker, InputMint: request.InputMint, OutputMint: request.OutputMint, MaxInputAmount: request.InputAmount,
		MinOutputAmount: quote.MinimumOutput, MaxSlippageBPS: request.SlippageBPS,
		MaxComputeUnits: 300_000, MaxComputeUnitPriceMicroLamport: 10_000, MaxFeeLamports: 20_000,
		MaxTokenAccountRentLamports: 3_000_000, RouteGuard: routeGuardFixture(),
	}
	limit, err := solana.SetComputeUnitLimitInstruction(250_000)
	if err != nil {
		t.Fatal(err)
	}
	priceData := make([]byte, 9)
	priceData[0] = 3
	binary.LittleEndian.PutUint64(priceData[1:], 5_000)
	price := solana.Instruction{Program: solana.ComputeBudgetProgram, Data: priceData}
	instructions := append([]solana.Instruction{limit, price}, plan...)
	blockhash := solana.Encode(bytes.Repeat([]byte{9}, 32))
	message, err := BuildGuardedPolicyV0Message(policy, request.Taker, blockhash, instructions, nil)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := ValidateV0Message(policy, request, quote, message, nil)
	if err != nil {
		t.Fatal(err)
	}
	if intent.InputAmount != request.InputAmount || intent.ComputeUnits != 250_000 ||
		intent.ComputeUnitPriceMicroLamport != 5_000 || intent.RecentBlockhash != blockhash {
		t.Fatalf("intent = %+v", intent)
	}

	mutations := map[string]func([]byte) []byte{
		"global privilege elevation": func(value []byte) []byte {
			copy := append([]byte(nil), value...)
			if copy[3] == 0 {
				t.Fatal("fixture has no readonly unsigned accounts")
			}
			copy[3]--
			return copy
		},
		"compute limit": func(value []byte) []byte {
			decoded, decodeErr := solana.DecodeV0Message(value, nil)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			copy := append([]byte(nil), value...)
			needle := decoded.Instructions[0].Data
			at := bytes.Index(copy, needle)
			if at < 0 {
				t.Fatal("compute limit bytes not found")
			}
			binary.LittleEndian.PutUint32(copy[at+1:], policy.MaxComputeUnits+1)
			return copy
		},
		"instruction order": func(value []byte) []byte {
			copy := append([]byte(nil), value...)
			// The canonical compiler places the two compute programs first; making
			// the first instruction a price instruction must fail closed.
			decoded, decodeErr := solana.DecodeV0Message(copy, nil)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			needle := decoded.Instructions[0].Data
			at := bytes.Index(copy, needle)
			if at < 0 {
				t.Fatal("compute instruction bytes not found")
			}
			copy[at] = 3
			return copy
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateV0Message(policy, request, quote, mutate(message), nil); err == nil {
				t.Fatal("accepted mutated Jupiter v0 message")
			}
		})
	}
}

func TestGuardedProfileRejectsDirectJupiterMessage(t *testing.T) {
	request, quote, plan := exactInSOLFixture(t)
	policy := Policy{
		Owner: request.Taker, InputMint: request.InputMint, OutputMint: request.OutputMint,
		MaxInputAmount: request.InputAmount, MinOutputAmount: quote.MinimumOutput,
		MaxSlippageBPS: request.SlippageBPS, MaxComputeUnits: 300_000,
		MaxComputeUnitPriceMicroLamport: 10_000, MaxFeeLamports: 20_000,
		MaxTokenAccountRentLamports: 3_000_000, RouteGuard: routeGuardFixture(),
	}
	limit, _ := solana.SetComputeUnitLimitInstruction(250_000)
	priceData := make([]byte, 9)
	priceData[0] = 3
	binary.LittleEndian.PutUint64(priceData[1:], 5_000)
	instructions := append(
		[]solana.Instruction{limit, {Program: solana.ComputeBudgetProgram, Data: priceData}},
		plan...,
	)
	message, err := BuildPolicyV0Message(
		request.Taker, solana.Encode(bytes.Repeat([]byte{9}, 32)), instructions, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateV0Message(policy, request, quote, message, nil); err == nil {
		t.Fatal("guarded profile accepted a direct Jupiter message")
	}
}

func TestValidateV0MessageRevalidatesTokenInputWirePlan(t *testing.T) {
	request, quote, plan := exactInTokenToSOLFixture(t)
	policy := Policy{
		Owner: request.Taker, InputMint: request.InputMint, OutputMint: request.OutputMint,
		MaxInputAmount: request.InputAmount, MinOutputAmount: quote.MinimumOutput,
		MaxSlippageBPS: request.SlippageBPS, MaxComputeUnits: 300_000,
		MaxComputeUnitPriceMicroLamport: 10_000, MaxFeeLamports: 20_000,
		MaxTokenAccountRentLamports: 3_000_000, RouteGuard: routeGuardFixture(),
	}
	limit, err := solana.SetComputeUnitLimitInstruction(250_000)
	if err != nil {
		t.Fatal(err)
	}
	priceData := make([]byte, 9)
	priceData[0] = 3
	binary.LittleEndian.PutUint64(priceData[1:], 5_000)
	instructions := append(
		[]solana.Instruction{limit, {Program: solana.ComputeBudgetProgram, Data: priceData}},
		plan...,
	)
	blockhash := solana.Encode(bytes.Repeat([]byte{9}, 32))
	message, err := BuildGuardedPolicyV0Message(policy, request.Taker, blockhash, instructions, nil)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := ValidateV0Message(policy, request, quote, message, nil)
	if err != nil {
		t.Fatal(err)
	}
	if intent.InputAmount != request.InputAmount || intent.MinimumOutput != quote.MinimumOutput ||
		intent.ComputeUnits != 250_000 || intent.ComputeUnitPriceMicroLamport != 5_000 ||
		intent.RecentBlockhash != blockhash || intent.OutputAccountCreated {
		t.Fatalf("token-input message intent = %+v", intent)
	}
}

func TestBuildPolicyV0MessageKeepsRoutePolicyAccountsStatic(t *testing.T) {
	request, quote, plan := exactInSOLFixture(t)
	policy := Policy{
		Owner: request.Taker, InputMint: request.InputMint, OutputMint: request.OutputMint,
		MaxInputAmount: request.InputAmount, MinOutputAmount: quote.MinimumOutput,
		MaxSlippageBPS: request.SlippageBPS, MaxComputeUnits: 300_000,
		MaxComputeUnitPriceMicroLamport: 10_000, MaxFeeLamports: 20_000,
		MaxTokenAccountRentLamports: 3_000_000, RouteGuard: routeGuardFixture(),
	}
	limit, _ := solana.SetComputeUnitLimitInstruction(250_000)
	priceData := make([]byte, 9)
	priceData[0] = 3
	binary.LittleEndian.PutUint64(priceData[1:], 5_000)
	instructions := append(
		[]solana.Instruction{limit, {Program: solana.ComputeBudgetProgram, Data: priceData}},
		plan...,
	)
	route := plan[3]
	tableID := [32]byte{9}
	table := make([][32]byte, 0, 6)
	for _, index := range []int{1, 2, 3, 4, 8, 10} {
		key, err := solana.Decode32(route.Accounts[index].Address)
		if err != nil {
			t.Fatal(err)
		}
		table = append(table, key)
	}
	tables := map[[32]byte][][32]byte{tableID: table}
	message, err := BuildGuardedPolicyV0Message(
		policy, request.Taker, solana.Encode(bytes.Repeat([]byte{9}, 32)), instructions, tables,
	)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := solana.DecodeV0Message(message, tables)
	if err != nil {
		t.Fatal(err)
	}
	static := make(map[[32]byte]struct{}, len(decoded.StaticAccountKeys))
	for _, key := range decoded.StaticAccountKeys {
		static[key] = struct{}{}
	}
	for _, account := range route.Accounts[:10] {
		key, _ := solana.Decode32(account.Address)
		if _, ok := static[key]; !ok {
			t.Fatal("hosted-policy route account was moved into a lookup table")
		}
	}
	remaining, _ := solana.Decode32(route.Accounts[10].Address)
	if _, ok := static[remaining]; !ok || len(decoded.AddressTableLookups) != 0 {
		t.Fatal("a route account was hidden in a lookup table even though the message fits statically")
	}
	usedTables, err := UsedAddressTables(message, tables)
	if err != nil {
		t.Fatal(err)
	}
	if len(usedTables) != 0 {
		t.Fatal("static policy message retained an unused address table")
	}
	if _, err := ValidateV0Message(policy, request, quote, message, usedTables); err != nil {
		t.Fatal(err)
	}
	duplicate := append(append([]solana.Instruction(nil), instructions...), route)
	if _, err := BuildGuardedPolicyV0Message(
		policy, request.Taker, solana.Encode(bytes.Repeat([]byte{9}, 32)), duplicate, tables,
	); err == nil {
		t.Fatal("multiple Jupiter route_v2 instructions were accepted")
	}
}

func TestPolicyMessageValidatesAndExposesSharedRouteAccounts(t *testing.T) {
	request, quote, plan := exactInSOLFixture(t)
	policy := Policy{
		Owner: request.Taker, InputMint: request.InputMint, OutputMint: request.OutputMint,
		MaxInputAmount: request.InputAmount, MinOutputAmount: quote.MinimumOutput,
		MaxSlippageBPS: request.SlippageBPS, MaxComputeUnits: 300_000,
		MaxComputeUnitPriceMicroLamport: 10_000, MaxFeeLamports: 20_000,
		MaxTokenAccountRentLamports: 3_000_000, RouteGuard: routeGuardFixture(),
	}
	inputAccount, _ := orcaswap.AssociatedTokenAddress(request.Taker, request.InputMint)
	outputAccount, _ := orcaswap.AssociatedTokenAddress(request.Taker, request.OutputMint)
	plan[3] = planSharedAccountsRouteV2Fixture(t, request, quote, inputAccount, outputAccount)
	limit, _ := solana.SetComputeUnitLimitInstruction(250_000)
	priceData := make([]byte, 9)
	priceData[0] = 3
	binary.LittleEndian.PutUint64(priceData[1:], 5_000)
	instructions := append(
		[]solana.Instruction{limit, {Program: solana.ComputeBudgetProgram, Data: priceData}},
		plan...,
	)
	message, err := BuildGuardedPolicyV0Message(
		policy, request.Taker, solana.Encode(bytes.Repeat([]byte{9}, 32)), instructions, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := solana.DecodeV0Message(message, nil)
	if err != nil {
		t.Fatal(err)
	}
	static := make(map[[32]byte]struct{}, len(decoded.StaticAccountKeys))
	for _, key := range decoded.StaticAccountKeys {
		static[key] = struct{}{}
	}
	for _, account := range plan[3].Accounts[:12] {
		key, _ := solana.Decode32(account.Address)
		if _, ok := static[key]; !ok {
			t.Fatal("hosted-policy shared route account was moved into a lookup table")
		}
	}
	if _, err := ValidateV0Message(policy, request, quote, message, nil); err != nil {
		t.Fatal(err)
	}

	plan[3].Accounts[12] = solana.AccountMeta{Address: plan[3].Accounts[0].Address, Writable: true}
	instructions = append(
		[]solana.Instruction{limit, {Program: solana.ComputeBudgetProgram, Data: priceData}},
		plan...,
	)
	message, err = BuildGuardedPolicyV0Message(
		policy, request.Taker, solana.Encode(bytes.Repeat([]byte{9}, 32)), instructions, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateV0Message(policy, request, quote, message, nil); err == nil {
		t.Fatal("accepted a transaction-wide writable elevation of the shared authority")
	}
}

func TestValidateSignedV0TransactionChecksSignatureAndSemantics(t *testing.T) {
	seed := sha256.Sum256([]byte("Jupiter signer"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	taker := solana.Encode(privateKey.Public().(ed25519.PublicKey))
	request, quote, plan := exactInSOLFixtureForTaker(t, taker)
	policy := Policy{
		Owner: taker, InputMint: request.InputMint, OutputMint: request.OutputMint, MaxInputAmount: request.InputAmount,
		MinOutputAmount: quote.MinimumOutput, MaxSlippageBPS: request.SlippageBPS,
		MaxComputeUnits: 300_000, MaxComputeUnitPriceMicroLamport: 10_000, MaxFeeLamports: 20_000,
		MaxTokenAccountRentLamports: 3_000_000, RouteGuard: routeGuardFixture(),
	}
	limit, _ := solana.SetComputeUnitLimitInstruction(250_000)
	priceData := make([]byte, 9)
	priceData[0] = 3
	binary.LittleEndian.PutUint64(priceData[1:], 5_000)
	instructions := append(
		[]solana.Instruction{limit, {Program: solana.ComputeBudgetProgram, Data: priceData}}, plan...,
	)
	message, err := BuildGuardedPolicyV0Message(
		policy, taker, solana.Encode(bytes.Repeat([]byte{9}, 32)), instructions, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction, signature, err := solana.SignV0Message(privateKey, message, nil)
	if err != nil {
		t.Fatal(err)
	}
	intent, gotSignature, err := ValidateSignedV0Transaction(policy, request, quote, transaction, nil)
	if err != nil {
		t.Fatal(err)
	}
	if intent.InputAmount != request.InputAmount || gotSignature != solana.Encode(signature[:]) {
		t.Fatalf("intent/signature = %+v / %s", intent, gotSignature)
	}
	tampered := append([]byte(nil), transaction...)
	tampered[1] ^= 1
	if _, _, err := ValidateSignedV0Transaction(policy, request, quote, tampered, nil); err == nil {
		t.Fatal("accepted a transaction with a tampered signature")
	}
}

func TestValidateV0MessageRejectsUnboundAddressTableEvidence(t *testing.T) {
	request, quote, plan := exactInSOLFixture(t)
	policy := Policy{
		Owner: request.Taker, InputMint: request.InputMint, OutputMint: request.OutputMint, MaxInputAmount: request.InputAmount,
		MinOutputAmount: quote.MinimumOutput, MaxSlippageBPS: request.SlippageBPS,
		MaxComputeUnits: 300_000, MaxComputeUnitPriceMicroLamport: 10_000, MaxFeeLamports: 20_000,
		MaxTokenAccountRentLamports: 3_000_000, RouteGuard: routeGuardFixture(),
	}
	limit, _ := solana.SetComputeUnitLimitInstruction(250_000)
	priceData := make([]byte, 9)
	priceData[0] = 3
	binary.LittleEndian.PutUint64(priceData[1:], 5_000)
	instructions := append(
		[]solana.Instruction{limit, {Program: solana.ComputeBudgetProgram, Data: priceData}},
		plan...,
	)
	message, err := BuildGuardedPolicyV0Message(
		policy, request.Taker, solana.Encode(bytes.Repeat([]byte{9}, 32)), instructions, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	var unused [32]byte
	copy(unused[:], bytes.Repeat([]byte{8}, 32))
	if _, err := ValidateV0Message(
		policy, request, quote, message, map[[32]byte][][32]byte{unused: {unused}},
	); err == nil {
		t.Fatal("accepted address-table evidence not referenced by the message")
	}
}

func TestValidateV0MessageRejectsGlobalPrivilegeElevation(t *testing.T) {
	request, quote, plan := exactInSOLFixture(t)
	policy := Policy{
		Owner: request.Taker, InputMint: request.InputMint, OutputMint: request.OutputMint, MaxInputAmount: request.InputAmount,
		MinOutputAmount: quote.MinimumOutput, MaxSlippageBPS: request.SlippageBPS,
		MaxComputeUnits: 300_000, MaxComputeUnitPriceMicroLamport: 10_000, MaxFeeLamports: 20_000,
		MaxTokenAccountRentLamports: 3_000_000, RouteGuard: routeGuardFixture(),
	}
	// Repeating a fixed readonly mint as a writable remaining account elevates
	// that key for the whole transaction. Canonical recompilation alone cannot
	// detect this because the compiler merges duplicate-key privileges.
	plan[3].Accounts[10] = solana.AccountMeta{Address: request.OutputMint, Writable: true}
	limit, _ := solana.SetComputeUnitLimitInstruction(250_000)
	priceData := make([]byte, 9)
	priceData[0] = 3
	binary.LittleEndian.PutUint64(priceData[1:], 5_000)
	instructions := append(
		[]solana.Instruction{limit, {Program: solana.ComputeBudgetProgram, Data: priceData}},
		plan...,
	)
	message, err := BuildGuardedPolicyV0Message(
		policy, request.Taker, solana.Encode(bytes.Repeat([]byte{9}, 32)), instructions, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateV0Message(policy, request, quote, message, nil); err == nil {
		t.Fatal("accepted a transaction-wide writable elevation of a fixed readonly account")
	}
}
