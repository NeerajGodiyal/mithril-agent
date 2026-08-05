package solana

import (
	"bytes"
	"crypto/ed25519"
	"testing"
)

func TestLegacyMessageBuildSignDecode(t *testing.T) {
	privateKey := testKey("legacy-fee-payer")
	feePayer := Encode(privateKey.Public().(ed25519.PublicKey))
	writable := Encode(testKey("legacy-writable").Public().(ed25519.PublicKey))
	readonly := Encode(testKey("legacy-readonly").Public().(ed25519.PublicKey))
	program := Encode(testKey("legacy-program").Public().(ed25519.PublicKey))
	blockhash := Encode(bytes.Repeat([]byte{7}, 32))
	message, err := BuildLegacyMessage(feePayer, blockhash, []Instruction{{
		Program: program,
		Accounts: []AccountMeta{
			{Address: readonly},
			{Address: feePayer, Signer: true, Writable: true},
			{Address: writable, Writable: true},
			{Address: readonly, Writable: true},
		},
		Data: []byte{1, 2, 3},
	}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeLegacyMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.RequiredSignatures != 1 || decoded.ReadonlySigned != 0 ||
		decoded.ReadonlyUnsigned != 1 || len(decoded.AccountKeys) != 4 ||
		Encode(decoded.AccountKeys[0][:]) != feePayer ||
		Encode(decoded.AccountKeys[1][:]) != readonly ||
		Encode(decoded.AccountKeys[2][:]) != writable ||
		Encode(decoded.AccountKeys[3][:]) != program {
		t.Fatalf("decoded legacy message has unexpected account layout: %+v", decoded)
	}
	if len(decoded.Instructions) != 1 || decoded.Instructions[0].ProgramIndex != 3 ||
		!bytes.Equal(decoded.Instructions[0].Accounts, []byte{1, 0, 2, 1}) ||
		!bytes.Equal(decoded.Instructions[0].Data, []byte{1, 2, 3}) {
		t.Fatalf("decoded instruction = %+v", decoded.Instructions)
	}
	transaction, signature, err := SignLegacyMessage(privateKey, message)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := DecodeSignedLegacyTransaction(transaction)
	if err != nil {
		t.Fatal(err)
	}
	if signed.Signature != signature || !bytes.Equal(signed.Message.Raw, message) {
		t.Fatal("signed transaction did not round trip")
	}
	simulation, err := BuildLegacySimulationTransaction(message)
	if err != nil {
		t.Fatal(err)
	}
	if len(simulation) != 1+ed25519.SignatureSize+len(message) || simulation[0] != 1 ||
		!bytes.Equal(simulation[1:65], make([]byte, ed25519.SignatureSize)) {
		t.Fatal("simulation transaction has the wrong wire layout")
	}
}

func TestLegacyMessageRejectsAdditionalSignerAndBadWireData(t *testing.T) {
	privateKey := testKey("legacy-fee-payer")
	feePayer := Encode(privateKey.Public().(ed25519.PublicKey))
	other := Encode(testKey("legacy-other").Public().(ed25519.PublicKey))
	program := Encode(testKey("legacy-program").Public().(ed25519.PublicKey))
	blockhash := Encode(bytes.Repeat([]byte{8}, 32))
	if _, err := BuildLegacyMessage(feePayer, blockhash, []Instruction{{
		Program:  program,
		Accounts: []AccountMeta{{Address: other, Signer: true}},
		Data:     []byte{1},
	}}); err == nil {
		t.Fatal("additional signer was accepted")
	}
	message, err := BuildLegacyMessage(feePayer, blockhash, []Instruction{{
		Program: program,
		Accounts: []AccountMeta{
			{Address: feePayer, Signer: true, Writable: true},
			{Address: other, Writable: true},
		},
		Data: []byte{1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func([]byte){
		"signer count":     func(data []byte) { data[0] = 2 },
		"duplicate key":    func(data []byte) { copy(data[4+32:4+64], data[4:4+32]) },
		"trailing byte":    func([]byte) {},
		"program as payer": func(data []byte) { data[len(data)-5] = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			copyMessage := bytes.Clone(message)
			if name == "trailing byte" {
				copyMessage = append(copyMessage, 0)
			} else {
				mutate(copyMessage)
			}
			if _, err := DecodeLegacyMessage(copyMessage); err == nil {
				t.Fatal("mutated message was accepted")
			}
		})
	}
	transaction, _, err := SignLegacyMessage(privateKey, message)
	if err != nil {
		t.Fatal(err)
	}
	transaction[1] ^= 1
	if _, err := DecodeSignedLegacyTransaction(transaction); err == nil {
		t.Fatal("invalid signature was accepted")
	}
}
