package solana

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"strings"
	"testing"
)

func testKey(seedText string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte(seedText))
	return ed25519.NewKeyFromSeed(seed[:])
}

func TestTransferBuildSignDecode(t *testing.T) {
	privateKey := testKey("source")
	source := Encode(privateKey.Public().(ed25519.PublicKey))
	destination := Encode(testKey("destination").Public().(ed25519.PublicKey))
	blockhash := Encode(bytes.Repeat([]byte{7}, 32))
	message, err := BuildTransferMessage(source, destination, blockhash, 42)
	if err != nil {
		t.Fatal(err)
	}
	transaction, signature, err := SignTransferMessage(privateKey, message)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSignedTransfer(transaction)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Lamports != 42 || Encode(decoded.Source[:]) != source ||
		Encode(decoded.Destination[:]) != destination ||
		Encode(decoded.RecentBlockhash[:]) != blockhash ||
		decoded.Signature != signature {
		t.Fatalf("decoded transfer = %+v", decoded)
	}
}

func TestTransferDecoderRejectsSemanticMutation(t *testing.T) {
	privateKey := testKey("source")
	source := Encode(privateKey.Public().(ed25519.PublicKey))
	destination := Encode(testKey("destination").Public().(ed25519.PublicKey))
	blockhash := Encode(bytes.Repeat([]byte{7}, 32))
	message, err := BuildTransferMessage(source, destination, blockhash, 42)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func([]byte){
		"header signer count":  func(data []byte) { data[0] = 2 },
		"program id":           func(data []byte) { data[3+1+64] = 1 },
		"program index":        func(data []byte) { data[3+1+3*32+32+1] = 1 },
		"source account index": func(data []byte) { data[3+1+3*32+32+1+1+1] = 1 },
		"opcode":               func(data []byte) { data[len(data)-12] = 3 },
		"zero amount":          func(data []byte) { clear(data[len(data)-8:]) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			copy := bytes.Clone(message)
			mutate(copy)
			if _, err := DecodeTransferMessage(copy); err == nil {
				t.Fatal("mutated message was accepted")
			}
		})
	}
}

func TestSignedTransferRejectsSignatureAndTrailingBytes(t *testing.T) {
	privateKey := testKey("source")
	message, err := BuildTransferMessage(
		Encode(privateKey.Public().(ed25519.PublicKey)),
		Encode(testKey("destination").Public().(ed25519.PublicKey)),
		Encode(bytes.Repeat([]byte{7}, 32)),
		42,
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction, _, err := SignTransferMessage(privateKey, message)
	if err != nil {
		t.Fatal(err)
	}
	badSignature := bytes.Clone(transaction)
	badSignature[1] ^= 1
	if _, err := DecodeSignedTransfer(badSignature); err == nil {
		t.Fatal("bad signature was accepted")
	}
	withTrailing := append(bytes.Clone(transaction), 0)
	if _, err := DecodeSignedTransfer(withTrailing); err == nil {
		t.Fatal("trailing byte was accepted")
	}
}

func TestBuildSimulationTransactionUsesOneEmptySignature(t *testing.T) {
	sourceKey := testKey("source")
	message, err := BuildTransferMessage(
		Encode(sourceKey.Public().(ed25519.PublicKey)),
		Encode(testKey("destination").Public().(ed25519.PublicKey)),
		Encode(bytes.Repeat([]byte{3}, 32)),
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := BuildSimulationTransaction(message)
	if err != nil {
		t.Fatal(err)
	}
	if len(transaction) != 1+ed25519.SignatureSize+len(message) ||
		transaction[0] != 1 ||
		!bytes.Equal(transaction[1:1+ed25519.SignatureSize], make([]byte, ed25519.SignatureSize)) ||
		!bytes.Equal(transaction[1+ed25519.SignatureSize:], message) {
		t.Fatal("simulation transaction has the wrong wire layout")
	}
	if _, err := DecodeSignedTransfer(transaction); err == nil {
		t.Fatal("unsigned simulation transaction passed signed-transaction validation")
	}
}

func TestBase58RoundTripAndBounds(t *testing.T) {
	for _, data := range [][]byte{
		make([]byte, 32),
		bytes.Repeat([]byte{1}, 32),
		append([]byte{0, 0}, bytes.Repeat([]byte{9}, 30)...),
	} {
		encoded := Encode(data)
		decoded, err := Decode32(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded[:], data) {
			t.Fatalf("round trip mismatch for %q", encoded)
		}
	}
	for _, invalid := range []string{"", "0OIl", "111", Encode(bytes.Repeat([]byte{1}, 33))} {
		if _, err := Decode32(invalid); err == nil {
			t.Fatalf("Decode32(%q) succeeded", invalid)
		}
	}
}

func TestValidateVariableLengthBase58(t *testing.T) {
	if _, err := Decode32(DevnetGenesisHash); err != nil {
		t.Fatalf("devnet genesis hash: %v", err)
	}
	for _, value := range []string{
		"EtWTRABZaYq6iMfeYKouRu166VU2xqa1",
		"5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp",
		"4uhcVJyU9pJkvQyS88uRDiswHXSCkY3zQawwpjk2NsNY",
	} {
		if err := ValidateBase58(value, 64); err != nil {
			t.Fatalf("%q: %v", value, err)
		}
	}
	for _, value := range []string{"", "0", strings.Repeat("a", 65)} {
		if err := ValidateBase58(value, 64); err == nil {
			t.Fatalf("invalid value %q was accepted", value)
		}
	}
}
