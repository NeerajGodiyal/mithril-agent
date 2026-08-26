package solana

import (
	"bytes"
	"crypto/ed25519"
	"testing"
)

func TestBuildV0MessageUsesLookupTablesWithoutMovingPrograms(t *testing.T) {
	feePayer := v0Address(1)
	blockhash := v0Address(2)
	program := v0Address(3)
	writable := v0FilledKey(4)
	readonly := v0FilledKey(5)
	tableID := v0FilledKey(6)
	tables := map[[32]byte][][32]byte{tableID: {writable, readonly, v0FilledKey(3)}}
	instructions := []Instruction{{
		Program: program,
		Accounts: []AccountMeta{
			{Address: Encode(writable[:]), Writable: true},
			{Address: feePayer, Signer: true, Writable: true},
			{Address: Encode(readonly[:])},
		},
		Data: []byte{1, 2, 3},
	}}

	message, err := BuildV0Message(feePayer, blockhash, instructions, tables)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeV0Message(message, tables)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.StaticAccountKeys) != 2 || decoded.StaticAccountKeys[0] != v0FilledKey(1) ||
		decoded.StaticAccountKeys[1] != v0FilledKey(3) || len(decoded.AddressTableLookups) != 1 ||
		!bytes.Equal(decoded.AddressTableLookups[0].WritableIndexes, []byte{0}) ||
		!bytes.Equal(decoded.AddressTableLookups[0].ReadonlyIndexes, []byte{1}) {
		t.Fatalf("unexpected v0 layout: %+v", decoded)
	}
	compiled := decoded.Instructions[0]
	if compiled.ProgramIndex != 1 || !bytes.Equal(compiled.Accounts, []byte{2, 0, 3}) ||
		!decoded.IsWritable(2) || decoded.IsWritable(3) {
		t.Fatalf("unexpected compiled instruction: %+v", compiled)
	}
	if err := ValidateV0MessageForSigner(decoded, feePayer); err != nil {
		t.Fatal(err)
	}
	transaction, err := BuildUnsignedV0Transaction(message, tables)
	if err != nil {
		t.Fatal(err)
	}
	if len(transaction) != 1+ed25519.SignatureSize+len(message) || transaction[0] != 1 ||
		!bytes.Equal(transaction[1:1+ed25519.SignatureSize], make([]byte, ed25519.SignatureSize)) ||
		!bytes.Equal(transaction[1+ed25519.SignatureSize:], message) {
		t.Fatal("unsigned transaction framing is wrong")
	}
	simulation, err := BuildV0SimulationTransaction(message, tables)
	if err != nil || !bytes.Equal(simulation, transaction) {
		t.Fatal("simulation did not use the canonical unsigned transaction")
	}
}

func TestBuildV0MessageKeepsRequestedPolicyAccountsStatic(t *testing.T) {
	feePayer := v0Address(1)
	blockhash := v0Address(2)
	program := v0Address(3)
	writable := v0FilledKey(4)
	readonly := v0FilledKey(5)
	tableID := v0FilledKey(6)
	tables := map[[32]byte][][32]byte{tableID: {writable, readonly}}
	instructions := []Instruction{{
		Program: program,
		Accounts: []AccountMeta{
			{Address: Encode(writable[:]), Writable: true},
			{Address: Encode(readonly[:])},
		},
		Data: []byte{1},
	}}

	message, err := BuildV0MessageWithStaticAccounts(
		feePayer, blockhash, instructions, tables, []string{Encode(readonly[:])},
	)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeV0Message(message, tables)
	if err != nil {
		t.Fatal(err)
	}
	static := make(map[[32]byte]struct{}, len(decoded.StaticAccountKeys))
	for _, key := range decoded.StaticAccountKeys {
		static[key] = struct{}{}
	}
	_, hasReadonly := static[readonly]
	_, hasProgram := static[v0FilledKey(3)]
	if len(decoded.StaticAccountKeys) != 3 || decoded.StaticAccountKeys[0] != v0FilledKey(1) ||
		!hasReadonly || !hasProgram || len(decoded.AddressTableLookups) != 1 ||
		!bytes.Equal(decoded.AddressTableLookups[0].WritableIndexes, []byte{0}) ||
		len(decoded.AddressTableLookups[0].ReadonlyIndexes) != 0 {
		t.Fatalf("unexpected forced-static v0 layout: %+v", decoded)
	}
	if _, err := BuildV0MessageWithStaticAccounts(
		feePayer, blockhash, instructions, tables, []string{v0Address(9)},
	); err == nil {
		t.Fatal("unused forced-static account was accepted")
	}
	if _, err := BuildV0MessageWithStaticAccounts(
		feePayer, blockhash, instructions, tables, []string{"invalid"},
	); err == nil {
		t.Fatal("invalid forced-static account was accepted")
	}
}

func TestBuildV0MessageIsDeterministicAcrossMapOrder(t *testing.T) {
	feePayerKey := v0FilledKey(1)
	blockhashKey := v0FilledKey(2)
	firstID, secondID := v0FilledKey(10), v0FilledKey(20)
	firstAccount, secondAccount := v0FilledKey(11), v0FilledKey(21)
	instructions := []Instruction{{
		Program: v0Address(3),
		Accounts: []AccountMeta{
			{Address: Encode(firstAccount[:]), Writable: true},
			{Address: Encode(secondAccount[:])},
		},
		Data: []byte{7},
	}}
	first := map[[32]byte][][32]byte{firstID: {firstAccount}, secondID: {secondAccount}}
	second := map[[32]byte][][32]byte{secondID: {secondAccount}, firstID: {firstAccount}}

	one, err := BuildV0Message(Encode(feePayerKey[:]), Encode(blockhashKey[:]), instructions, first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := BuildV0Message(Encode(feePayerKey[:]), Encode(blockhashKey[:]), instructions, second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one, two) {
		t.Fatal("v0 compilation depends on Go map iteration order")
	}
}

func TestBuildV0MessageRejectsAdditionalSignerAndOversize(t *testing.T) {
	feePayer := v0Address(1)
	blockhash := v0Address(2)
	program := v0Address(3)
	if _, err := BuildV0Message(feePayer, blockhash, []Instruction{{
		Program: program,
		Accounts: []AccountMeta{{
			Address: v0Address(4), Signer: true, Writable: true,
		}},
	}}, nil); err == nil {
		t.Fatal("additional signer was accepted")
	}
	if _, err := BuildV0Message(feePayer, blockhash, []Instruction{{
		Program: program, Data: make([]byte, maxTransactionBytes),
	}}, nil); err == nil {
		t.Fatal("oversize v0 message was accepted")
	}
}

func v0Address(value byte) string {
	key := v0FilledKey(value)
	return Encode(key[:])
}
