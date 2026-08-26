package solana

import (
	"bytes"
	"crypto/ed25519"
	"testing"
)

func FuzzV0DecodersNeverPanic(f *testing.F) {
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	payer := Encode(privateKey.Public().(ed25519.PublicKey))
	message, err := BuildV0Message(
		payer,
		Encode(bytes.Repeat([]byte{8}, 32)),
		[]Instruction{{
			Program:  Encode(make([]byte, 32)),
			Accounts: []AccountMeta{{Address: payer, Signer: true, Writable: true}},
			Data:     []byte{1},
		}},
		nil,
	)
	if err != nil {
		f.Fatal(err)
	}
	transaction, _, err := SignV0Message(privateKey, message, nil)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(message)
	f.Add(transaction)
	f.Add([]byte{0x80})

	f.Fuzz(func(t *testing.T, input []byte) {
		_, _ = DecodeV0Message(input, nil)
		_, _ = DecodeSignedV0Transaction(input, nil)
		_, _ = VerifySignedTransactionEnvelope(input)
	})
}
