package turnkeycustody

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestJupiterQualificationPolicyPinsCandidateButNotBlockhash(t *testing.T) {
	policy, candidate, table := qualificationCandidate(t)
	document, err := BuildJupiterQualificationPolicy(
		policy, candidate, "user-qualification-123",
	)
	if err != nil {
		t.Fatal(err)
	}
	again, err := BuildJupiterQualificationPolicy(policy, candidate, "user-qualification-123")
	if err != nil || document != again {
		t.Fatal("Turnkey qualification policy is not deterministic")
	}
	message, tables, err := proposalcheck.ValidateCandidateMaterial(policy, candidate)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := solana.DecodeV0Message(message, tables)
	if err != nil {
		t.Fatal(err)
	}
	_, routeLookups, err := qualificationInstructionShape(decoded, decoded.Instructions[5])
	if err != nil || len(routeLookups) == 0 {
		t.Fatalf("resolve retained route lookups: %v", err)
	}
	blockhash := solana.Encode(bytes.Repeat([]byte{0x51}, 32))
	for _, want := range []string{
		"activity.type == 'ACTIVITY_TYPE_SIGN_TRANSACTION_V2'",
		"activity.params.type == 'TRANSACTION_TYPE_SOLANA'",
		fmt.Sprintf("solana.tx.account_keys.count() == %d", len(decoded.StaticAccountKeys)),
		"address_table_key == '" + solana.Encode(table[:]) + "'",
		fmt.Sprintf("solana.tx.instructions[5].address_table_lookups.count() == %d", len(routeLookups)),
		fmt.Sprintf("solana.tx.instructions[5].address_table_lookups[0].index == %d", routeLookups[0].Index),
		fmt.Sprintf("solana.tx.instructions[5].address_table_lookups[0].writable == %t", routeLookups[0].Writable),
		"instruction_data_hex == '0200000040420f0000000000'",
	} {
		if !strings.Contains(document.Condition, want) {
			t.Fatalf("qualification condition is missing %q", want)
		}
	}
	if strings.Contains(document.Condition, blockhash) {
		t.Fatal("qualification policy pins the candidate blockhash")
	}
	for index, key := range decoded.StaticAccountKeys {
		want := fmt.Sprintf("solana.tx.account_keys[%d] == '%s'", index, solana.Encode(key[:]))
		if !strings.Contains(document.Condition, want) {
			t.Fatalf("qualification condition does not pin static account %d", index)
		}
	}
	if strings.Contains(document.Condition, "account_key == 'ADDRESS_TABLE_LOOKUP'") {
		t.Fatal("qualification condition puts lookup-loaded accounts in Turnkey's static account list")
	}
	for instructionIndex := range decoded.Instructions {
		prefix := fmt.Sprintf("solana.tx.instructions[%d].address_table_lookups", instructionIndex)
		for _, field := range []string{"writable_indexes", "readonly_indexes"} {
			if strings.Contains(document.Condition, prefix+"[0]."+field) {
				t.Fatalf("qualification condition uses transaction-level field %q on an instruction lookup", field)
			}
		}
	}
	if document.Effect != "EFFECT_ALLOW" ||
		document.Consensus != "approvers.any(user, user.id == 'user-qualification-123')" ||
		!strings.Contains(document.Notes, "Never use as a funded operational policy") {
		t.Fatalf("unsafe qualification document: %+v", document)
	}
}

func TestQualificationInstructionShapeMatchesTurnkeyParser(t *testing.T) {
	tableA := [32]byte{1}
	tableB := [32]byte{2}
	message := solana.V0Message{
		StaticAccountKeys: make([][32]byte, 2),
		AccountKeys:       make([][32]byte, 6),
		AddressTableLookups: []solana.MessageAddressTableLookup{
			{AccountKey: tableA, WritableIndexes: []uint8{4}, ReadonlyIndexes: []uint8{7}},
			{AccountKey: tableB, WritableIndexes: []uint8{9}, ReadonlyIndexes: []uint8{11}},
		},
	}
	staticAccounts, lookups, err := qualificationInstructionShape(message, solana.CompiledInstruction{
		Accounts: []uint8{1, 3, 0, 4, 2, 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(staticAccounts) != "[1 0]" {
		t.Fatalf("wrong static instruction accounts: %v", staticAccounts)
	}
	want := []qualificationInstructionLookup{
		{AddressTableKey: tableB, Index: 9, Writable: true},
		{AddressTableKey: tableA, Index: 7},
		{AddressTableKey: tableA, Index: 4, Writable: true},
		{AddressTableKey: tableB, Index: 11},
	}
	if fmt.Sprint(lookups) != fmt.Sprint(want) {
		t.Fatalf("wrong per-instruction lookup order: got %v want %v", lookups, want)
	}
}

func TestJupiterQualificationPolicyRejectsWrongInputs(t *testing.T) {
	policy, candidate, _ := qualificationCandidate(t)
	changed := policy
	changed.MaxInputAmount++
	if _, err := BuildJupiterQualificationPolicy(changed, candidate, "user-123"); err == nil {
		t.Fatal("qualification policy accepted a candidate under different protected policy")
	}
	for _, user := range []string{"", "user' || true", strings.Repeat("a", 129)} {
		if _, err := BuildJupiterQualificationPolicy(policy, candidate, user); err == nil {
			t.Fatalf("qualification policy accepted API user %q", user)
		}
	}
}

func qualificationCandidate(t *testing.T) (
	jupiterswap.Policy,
	proposalcheck.Candidate,
	[32]byte,
) {
	t.Helper()
	owner := solana.Encode(bytes.Repeat([]byte{1}, 32))
	outputMint := solana.Encode(bytes.Repeat([]byte{2}, 32))
	sourceAccount, err := orcaswap.AssociatedTokenAddress(owner, orcaswap.WrappedSOLMint)
	if err != nil {
		t.Fatal(err)
	}
	destinationAccount, err := orcaswap.AssociatedTokenAddress(owner, outputMint)
	if err != nil {
		t.Fatal(err)
	}
	policy := jupiterswap.Policy{
		Owner: owner, InputMint: orcaswap.WrappedSOLMint, OutputMint: outputMint,
		MaxInputAmount: 1_000_000, MinOutputAmount: 995, MaxSlippageBPS: 50,
		MaxComputeUnits: 100_000, MaxComputeUnitPriceMicroLamport: 100,
		MaxFeeLamports: 10_000, MaxTokenAccountRentLamports: 3_000_000,
		RouteGuard: turnkeyRouteGuard(),
	}
	request := jupiterquote.Request{
		Taker: owner, InputMint: orcaswap.WrappedSOLMint, OutputMint: outputMint,
		DestinationTokenAccount: destinationAccount,
		InputAmount:             1_000_000, SlippageBPS: 50,
	}
	quote := jupiterquote.Result{
		InputAmount: 1_000_000, EstimatedOutput: 1_000, MinimumOutput: 995,
	}
	limit, err := solana.SetComputeUnitLimitInstruction(60_000)
	if err != nil {
		t.Fatal(err)
	}
	priceData := make([]byte, 9)
	priceData[0] = 3
	binary.LittleEndian.PutUint64(priceData[1:], 50)
	transferData := make([]byte, 12)
	binary.LittleEndian.PutUint32(transferData[:4], 2)
	binary.LittleEndian.PutUint64(transferData[4:], request.InputAmount)
	routeData := make([]byte, 35)
	copy(routeData, []byte{187, 100, 250, 204, 49, 196, 175, 20})
	binary.LittleEndian.PutUint64(routeData[8:], request.InputAmount)
	binary.LittleEndian.PutUint64(routeData[16:], quote.EstimatedOutput)
	binary.LittleEndian.PutUint16(routeData[24:], request.SlippageBPS)
	binary.LittleEndian.PutUint32(routeData[30:], 1)
	routeData[34] = 1
	extra := solana.Encode(bytes.Repeat([]byte{10}, 32))
	instructions := []solana.Instruction{
		limit,
		{Program: solana.ComputeBudgetProgram, Data: priceData},
		{Program: orcaswap.AssociatedTokenProgram, Accounts: []solana.AccountMeta{
			{Address: owner, Signer: true, Writable: true},
			{Address: sourceAccount, Writable: true}, {Address: owner},
			{Address: orcaswap.WrappedSOLMint}, {Address: orcaswap.SystemProgram},
			{Address: orcaswap.TokenProgram},
		}, Data: []byte{1}},
		{Program: orcaswap.SystemProgram, Accounts: []solana.AccountMeta{
			{Address: owner, Signer: true, Writable: true},
			{Address: sourceAccount, Writable: true},
		}, Data: transferData},
		{Program: orcaswap.TokenProgram, Accounts: []solana.AccountMeta{
			{Address: sourceAccount, Writable: true},
		}, Data: []byte{17}},
		{Program: jupiterswap.Program, Accounts: []solana.AccountMeta{
			{Address: owner, Signer: true},
			{Address: sourceAccount, Writable: true},
			{Address: destinationAccount, Writable: true},
			{Address: orcaswap.WrappedSOLMint}, {Address: outputMint},
			{Address: orcaswap.TokenProgram}, {Address: orcaswap.TokenProgram},
			{Address: destinationAccount, Writable: true},
			{Address: "D8cy77BBepLMngZx6ZukaTff5hCt1HrWyKk3Hnd9oitf"},
			{Address: jupiterswap.Program}, {Address: extra},
		}, Data: routeData},
		{Program: orcaswap.TokenProgram, Accounts: []solana.AccountMeta{
			{Address: sourceAccount, Writable: true}, {Address: owner, Writable: true},
			{Address: owner, Signer: true},
		}, Data: []byte{9}},
	}
	table := [32]byte{9}
	tableAccounts := [][32]byte{mustDecode32(t, extra)}
	for index := byte(0); index < 53; index++ {
		account := [32]byte{0x70, index + 1}
		tableAccounts = append(tableAccounts, account)
		instructions[5].Accounts = append(instructions[5].Accounts, solana.AccountMeta{
			Address: solana.Encode(account[:]),
		})
	}
	tables := map[[32]byte][][32]byte{table: tableAccounts}
	blockhash := solana.Encode(bytes.Repeat([]byte{0x51}, 32))
	message, err := jupiterswap.BuildGuardedPolicyV0Message(
		policy, owner, blockhash, instructions, tables,
	)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := jupiterswap.EncodeAddressTables(tables)
	if err != nil {
		t.Fatal(err)
	}
	candidate := proposalcheck.Candidate{
		Version: proposalcheck.CandidateVersion, Policy: policy, Request: request, Quote: quote,
		MessageBase64: base64.StdEncoding.EncodeToString(message), AddressTables: evidence,
		LastValidBlockHeight: 100,
	}
	if _, err := proposalcheck.EncodeCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	return policy, candidate, table
}

func turnkeyRouteGuard() jupiterswap.RouteGuardDeployment {
	code := []byte("turnkey route guard")
	hash := sha256.Sum256(code)
	return jupiterswap.RouteGuardDeployment{
		Program:        solana.Encode(bytes.Repeat([]byte{71}, 32)),
		ProgramData:    solana.Encode(bytes.Repeat([]byte{72}, 32)),
		DeploymentSlot: 123, CodeLength: uint64(len(code)), CodeSHA256: hex.EncodeToString(hash[:]),
	}
}

func mustDecode32(t *testing.T, value string) [32]byte {
	t.Helper()
	decoded, err := solana.Decode32(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
