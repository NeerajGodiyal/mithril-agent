package offchainmsg

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"strings"
	"testing"
)

func newKey(t *testing.T) ([32]byte, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	copy(key[:], public)
	return key, private
}

// The envelope must match the reference serializer byte for byte — signing
// domain, version 0, format 0, u16 little-endian length, message — or every
// CLI-produced signature is silently rejected.
func TestEnvelopeMatchesReferenceLayout(t *testing.T) {
	message := "Test Message"
	sealed, err := Envelope(message)
	if err != nil {
		t.Fatal(err)
	}
	if string(sealed[:16]) != "\xffsolana offchain" {
		t.Fatalf("signing domain: %q", sealed[:16])
	}
	if sealed[16] != 0 || sealed[17] != 0 {
		t.Fatalf("version/format: %d/%d", sealed[16], sealed[17])
	}
	if binary.LittleEndian.Uint16(sealed[18:20]) != uint16(len(message)) {
		t.Fatal("length prefix is wrong")
	}
	if string(sealed[20:]) != message {
		t.Fatal("message body is wrong")
	}
}

func TestEnvelopeRefusesWhatWalletsCannotDisplay(t *testing.T) {
	if _, err := Envelope(""); err == nil {
		t.Fatal("an empty message must be refused")
	}
	if _, err := Envelope(strings.Repeat("a", 1213)); err == nil {
		t.Fatal("a message beyond the hardware budget must be refused")
	}
	if _, err := Envelope("line\nbreak"); err == nil {
		t.Fatal("a control character must be refused, not reformatted")
	}
	if _, err := Envelope("café"); err == nil {
		t.Fatal("non-ASCII must be refused, not reformatted")
	}
	if _, err := Envelope(strings.Repeat("a", 1212)); err != nil {
		t.Fatalf("the boundary length must be accepted: %v", err)
	}
}

func TestVerifyAcceptsEnvelopeAndRawForms(t *testing.T) {
	public, private := newKey(t)
	message := "mithril-agent test message|abc|def"

	sealed, err := Envelope(message)
	if err != nil {
		t.Fatal(err)
	}
	var cliSig, rawSig [64]byte
	copy(cliSig[:], ed25519.Sign(private, sealed))
	copy(rawSig[:], ed25519.Sign(private, []byte(message)))

	for name, sig := range map[string][64]byte{"envelope": cliSig, "raw": rawSig} {
		ok, err := Verify(public, message, sig)
		if err != nil || !ok {
			t.Fatalf("%s form rejected: ok=%v err=%v", name, ok, err)
		}
	}
}

func TestVerifyRejectsSubstitutions(t *testing.T) {
	public, private := newKey(t)
	otherPublic, otherPrivate := newKey(t)
	message := "mithril-agent test message|abc|def"
	sealed, _ := Envelope(message)

	var goodSig, otherSig [64]byte
	copy(goodSig[:], ed25519.Sign(private, sealed))
	copy(otherSig[:], ed25519.Sign(otherPrivate, sealed))

	if ok, _ := Verify(public, message, otherSig); ok {
		t.Fatal("a signature from a different key must fail")
	}
	if ok, _ := Verify(otherPublic, message, goodSig); ok {
		t.Fatal("a different claimed signer must fail")
	}
	if ok, _ := Verify(public, message+"x", goodSig); ok {
		t.Fatal("a changed message must fail")
	}
	// A key that is not a valid curve point can never verify — this is what
	// makes signature proof subsume the on-curve check. All-zero bytes are
	// not a valid point encoding's y-coordinate with a valid sign bit combo
	// that lies on the curve.
	var offCurve [32]byte
	if ok, _ := Verify(offCurve, message, goodSig); ok {
		t.Fatal("an invalid public key must never verify")
	}
}
