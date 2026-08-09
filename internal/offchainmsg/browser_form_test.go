package offchainmsg

import (
	"crypto/ed25519"
	"testing"
)

// A browser wallet (Solflare, Phantom) signs the RAW utf-8 bytes; the Solana
// CLI and hardware wallets sign the enveloped form. Both must verify, or the
// destination ceremony has exactly one route — a keypair FILE — which somebody
// holding their wallet in an extension does not have and must not be asked to
// export. deploy/sign-destination-proof.html depends on this.
func TestBothTheBrowserAndCLISigningFormsVerify(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = byte(index + 1)
	}
	key := ed25519.NewKeyFromSeed(seed)
	var public [32]byte
	copy(public[:], key.Public().(ed25519.PublicKey))

	message := "mithril-agent sweep destination proof v1|agent:A|destination:B|nonce:c|issued:d"
	sealed, err := Envelope(message)
	if err != nil {
		t.Fatal(err)
	}

	for name, signed := range map[string][]byte{
		"browser signMessage (raw bytes)": ed25519.Sign(key, []byte(message)),
		"solana CLI (enveloped)":          ed25519.Sign(key, sealed),
	} {
		t.Run(name, func(t *testing.T) {
			var signature [64]byte
			copy(signature[:], signed)
			ok, err := Verify(public, message, signature)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatal("a legitimate signature was rejected")
			}
		})
	}

	// And a signature over DIFFERENT text must still fail, or the ceremony
	// proves nothing at all.
	var wrong [64]byte
	copy(wrong[:], ed25519.Sign(key, []byte(message+"x")))
	if ok, _ := Verify(public, message, wrong); ok {
		t.Fatal("a signature over other text was accepted")
	}
}
