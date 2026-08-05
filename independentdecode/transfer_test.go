package independentdecode

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestIndependentDecoderAgreesOnExactTransfer(t *testing.T) {
	seed := sha256.Sum256([]byte("independent source"))
	key := ed25519.NewKeyFromSeed(seed[:])
	source := solana.Encode(key.Public().(ed25519.PublicKey))
	destinationSeed := sha256.Sum256([]byte("independent destination"))
	destination := solana.Encode(
		ed25519.NewKeyFromSeed(destinationSeed[:]).Public().(ed25519.PublicKey),
	)
	blockhash := solana.Encode(bytes.Repeat([]byte{7}, 32))
	message, err := solana.BuildTransferMessage(source, destination, blockhash, 42)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	if solana.Encode(decoded.Source[:]) != source ||
		solana.Encode(decoded.Destination[:]) != destination ||
		solana.Encode(decoded.RecentBlockhash[:]) != blockhash ||
		decoded.Lamports != 42 {
		t.Fatalf("decoded transfer = %+v", decoded)
	}
	transaction, _, err := solana.SignTransferMessage(key, message)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := DecodeSigned(transaction)
	if err != nil {
		t.Fatal(err)
	}
	if signed.Transfer != decoded {
		t.Fatal("message and transaction decoders disagree")
	}
}

func TestIndependentDecoderRejectsEveryFixedFieldMutation(t *testing.T) {
	seed := sha256.Sum256([]byte("independent source"))
	key := ed25519.NewKeyFromSeed(seed[:])
	message, err := solana.BuildTransferMessage(
		solana.Encode(key.Public().(ed25519.PublicKey)),
		solana.Encode(bytes.Repeat([]byte{8}, 32)),
		solana.Encode(bytes.Repeat([]byte{7}, 32)),
		42,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{0, 1, 2, 3, 68, 99, 132, 133, 134, 135, 136, 137, 138} {
		mutated := bytes.Clone(message)
		mutated[offset] ^= 1
		if _, err := DecodeMessage(mutated); err == nil {
			t.Fatalf("mutation at fixed offset %d was accepted", offset)
		}
	}
	zeroAmount := bytes.Clone(message)
	clear(zeroAmount[142:])
	if _, err := DecodeMessage(zeroAmount); err == nil {
		t.Fatal("zero amount was accepted")
	}
	if _, err := DecodeMessage(append(bytes.Clone(message), 0)); err == nil {
		t.Fatal("trailing byte was accepted")
	}
}

func FuzzIndependentDecoderNeverDisagreesOnAcceptance(f *testing.F) {
	seed := sha256.Sum256([]byte("independent source"))
	key := ed25519.NewKeyFromSeed(seed[:])
	message, err := solana.BuildTransferMessage(
		solana.Encode(key.Public().(ed25519.PublicKey)),
		solana.Encode(bytes.Repeat([]byte{8}, 32)),
		solana.Encode(bytes.Repeat([]byte{7}, 32)),
		42,
	)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(message)
	f.Fuzz(func(t *testing.T, candidate []byte) {
		_, independentErr := DecodeMessage(candidate)
		_, existingErr := solana.DecodeTransferMessage(candidate)
		if (independentErr == nil) != (existingErr == nil) {
			t.Fatalf(
				"decoder disagreement: independent=%v existing=%v bytes=%x",
				independentErr,
				existingErr,
				candidate,
			)
		}
	})
}
