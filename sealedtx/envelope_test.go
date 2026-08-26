package sealedtx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestEnvelopeRoundTripAndAuthentication(t *testing.T) {
	privateKey, publicKey := envelopeTestKey(t, "recipient")
	transaction := []byte("signed transaction")
	transactionHash := sha256.Sum256(transaction)
	metadata := Metadata{
		Version:              Version,
		Domain:               Domain,
		ActionID:             strings.Repeat("1", 64),
		MessageSHA256:        strings.Repeat("2", 64),
		TransactionSHA256:    hex.EncodeToString(transactionHash[:]),
		Signature:            solana.Encode(bytes.Repeat([]byte{3}, 64)),
		BlockhashContextSlot: 90,
		FeeLamports:          5_000,
		LastValidBlockHeight: 100,
	}
	envelope, err := Seal(publicKey, metadata, transaction, nil)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := Open(privateKey, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, transaction) {
		t.Fatalf("opened transaction = %q", opened)
	}

	tests := map[string]func(*Envelope){
		"metadata":   func(value *Envelope) { value.Metadata.FeeLamports++ },
		"context":    func(value *Envelope) { value.Metadata.BlockhashContextSlot++ },
		"nonce":      func(value *Envelope) { value.NonceBase64 = "invalid" },
		"ciphertext": func(value *Envelope) { value.CiphertextBase64 = "invalid" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := envelope
			mutate(&changed)
			if _, err := Open(privateKey, changed); err == nil {
				t.Fatal("modified envelope opened")
			}
		})
	}
	wrongPrivateKey, _ := envelopeTestKey(t, "wrong recipient")
	if _, err := Open(wrongPrivateKey, envelope); err == nil {
		t.Fatal("wrong recipient opened envelope")
	}
}

func TestConfidentialEnvelopeKeepsSignatureInside(t *testing.T) {
	privateKey, publicKey := envelopeTestKey(t, "confidential recipient")
	transaction := []byte("signed transaction")
	transactionHash := sha256.Sum256(transaction)
	metadata := Metadata{
		Version: Version, Domain: Domain,
		ActionID: strings.Repeat("1", 64), MessageSHA256: strings.Repeat("2", 64),
		TransactionSHA256:    hex.EncodeToString(transactionHash[:]),
		BlockhashContextSlot: 90, FeeLamports: 5_000, LastValidBlockHeight: 100,
	}
	envelope, err := SealConfidential(publicKey, metadata, transaction, nil)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Metadata.Signature != "" {
		t.Fatal("confidential envelope exposed its transaction signature")
	}
	opened, err := OpenConfidential(privateKey, envelope)
	if err != nil || !bytes.Equal(opened, transaction) {
		t.Fatal("confidential envelope did not round trip")
	}
	if _, err := Open(privateKey, envelope); err == nil {
		t.Fatal("legacy opener accepted an envelope without public signature metadata")
	}
	envelope.Metadata.Signature = solana.Encode(bytes.Repeat([]byte{3}, 64))
	if _, err := OpenConfidential(privateKey, envelope); err == nil {
		t.Fatal("confidential opener accepted a public transaction signature")
	}
}

func TestSealRejectsMismatchedTransactionHash(t *testing.T) {
	_, publicKey := envelopeTestKey(t, "recipient")
	metadata := Metadata{
		Version:              Version,
		Domain:               Domain,
		ActionID:             strings.Repeat("1", 64),
		MessageSHA256:        strings.Repeat("2", 64),
		TransactionSHA256:    strings.Repeat("3", 64),
		Signature:            solana.Encode(bytes.Repeat([]byte{4}, 64)),
		BlockhashContextSlot: 1,
		FeeLamports:          1,
		LastValidBlockHeight: 1,
	}
	if _, err := Seal(publicKey, metadata, []byte("different"), nil); err == nil {
		t.Fatal("mismatched transaction hash was sealed")
	}
}

func TestValidatePublicKeyRejectsLowOrderX25519Keys(t *testing.T) {
	_, valid := envelopeTestKey(t, "recipient")
	if err := ValidatePublicKey(valid); err != nil {
		t.Fatal(err)
	}
	lowOrderOne := make([]byte, 32)
	lowOrderOne[0] = 1
	for name, value := range map[string]string{
		"zero": strings.Repeat("0", 64),
		"one":  hex.EncodeToString(lowOrderOne),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePublicKey(value); err == nil {
				t.Fatal("low-order submitter public key validated")
			}
		})
	}
}

func TestV2RejectsMissingContextAndV1Metadata(t *testing.T) {
	privateKey, publicKey := envelopeTestKey(t, "recipient")
	transaction := []byte("signed transaction")
	transactionHash := sha256.Sum256(transaction)
	metadata := Metadata{
		Version: Version, Domain: Domain,
		ActionID: strings.Repeat("1", 64), MessageSHA256: strings.Repeat("2", 64),
		TransactionSHA256: hex.EncodeToString(transactionHash[:]),
		Signature:         solana.Encode(bytes.Repeat([]byte{3}, 64)),
		FeeLamports:       5_000, LastValidBlockHeight: 100,
	}
	if _, err := Seal(publicKey, metadata, transaction, nil); err == nil {
		t.Fatal("metadata without a blockhash context slot was sealed")
	}
	metadata.BlockhashContextSlot = 90
	envelope, err := Seal(publicKey, metadata, transaction, nil)
	if err != nil {
		t.Fatal(err)
	}
	envelope.Metadata.Version = 1
	envelope.Metadata.Domain = "mithril-agent/sealed-transaction-v1"
	if _, err := Open(privateKey, envelope); err == nil {
		t.Fatal("v1 metadata was accepted by the v2 envelope reader")
	}
}

func envelopeTestKey(t *testing.T, label string) (string, string) {
	t.Helper()
	seed := sha256.Sum256([]byte(label))
	privateKey := hex.EncodeToString(seed[:])
	publicKey, err := PublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return privateKey, publicKey
}
