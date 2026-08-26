package solana

import (
	"bytes"
	"crypto/ed25519"
	"testing"
)

func TestSignAndDecodeV0Message(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	blockhash := v0FilledKey(2)
	message, err := BuildV0Message(
		Encode(publicKey), Encode(blockhash[:]),
		[]Instruction{{Program: v0Address(3), Data: []byte{7}}}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction, signature, err := SignV0Message(privateKey, message, nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSignedV0Transaction(transaction, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Signature != signature || !bytes.Equal(decoded.Message.Raw, message) ||
		!bytes.Equal(decoded.Raw, transaction) {
		t.Fatal("signed v0 transaction did not round trip")
	}
}

func TestSignedV0RejectsWrongSignerAndMutations(t *testing.T) {
	firstPublic, firstPrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, wrongPrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	blockhash := v0FilledKey(2)
	message, err := BuildV0Message(
		Encode(firstPublic), Encode(blockhash[:]),
		[]Instruction{{Program: v0Address(3), Data: []byte{7}}}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := SignV0Message(wrongPrivate, message, nil); err == nil {
		t.Fatal("wrong v0 signer was accepted")
	}
	transaction, _, err := SignV0Message(firstPrivate, message, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func([]byte){
		"signature count": func(value []byte) { value[0] = 2 },
		"signature":       func(value []byte) { value[1] ^= 1 },
		"message":         func(value []byte) { value[len(value)-1] ^= 1 },
	} {
		t.Run(name, func(t *testing.T) {
			copy := bytes.Clone(transaction)
			mutate(copy)
			if _, err := DecodeSignedV0Transaction(copy, nil); err == nil {
				t.Fatal("mutated v0 transaction was accepted")
			}
		})
	}
}

func TestVerifySignedTransactionEnvelopeAcceptsVersionZero(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	blockhash := v0FilledKey(8)
	message, err := BuildV0Message(
		Encode(publicKey), Encode(blockhash[:]),
		[]Instruction{{Program: ComputeBudgetProgram, Data: []byte{1}}}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction, signature, err := SignV0Message(privateKey, message, nil)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := VerifySignedTransactionEnvelope(transaction)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Version != 0 || envelope.Signature != signature ||
		envelope.FeePayer != [32]byte(publicKey) || !bytes.Equal(envelope.Message, message) {
		t.Fatalf("envelope = %+v", envelope)
	}
	tampered := bytes.Clone(transaction)
	tampered[1] ^= 1
	if _, err := VerifySignedTransactionEnvelope(tampered); err == nil {
		t.Fatal("accepted a tampered version-0 envelope")
	}
}
