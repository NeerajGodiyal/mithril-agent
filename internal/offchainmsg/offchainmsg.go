// Package offchainmsg verifies signatures over Solana off-chain messages.
//
// The off-chain message envelope is what `solana sign-offchain-message` and
// hardware wallets actually sign: a signing-domain prefix that can never be
// confused with a transaction, a version byte, a format byte, and a length,
// followed by the message text. The format is implemented here from the
// reference (anza-xyz/solana-sdk, offchain-message/src/lib.rs) rather than
// imported, because the agent needs only construction and verification.
//
// Verification also accepts a signature over the raw message bytes, because
// browser-wallet signMessage implementations sign exactly the bytes shown.
// Both forms bind the same text; neither can be replayed as a transaction.
//
// A successful verification proves more than approval: ed25519 verification
// fails for any public key that is not a valid curve point, so the signer's
// address is provably a real, spendable key — not an off-curve program
// address and not a mistyped address, neither of which can produce a
// signature. That property is why destination proofs are built on this
// package instead of on any decode-and-look check.
package offchainmsg

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
)

const (
	signingDomain = "\xffsolana offchain"
	version       = 0
	// formatRestrictedASCII covers printable ASCII (0x20..0x7e) up to the
	// hardware-wallet length budget. Messages built by this repository stay
	// within it so every signer, including a Ledger, can display them.
	formatRestrictedASCII = 0
	// maxLedgerMessage mirrors the reference MAX_LEN_LEDGER: the largest
	// message hardware wallets accept.
	maxLedgerMessage = 1212
)

// Envelope wraps a printable-ASCII message exactly as the reference
// serializer does. It refuses anything outside restricted ASCII rather than
// silently switching formats: a message this repository cannot show on a
// hardware wallet is a message it should not ask anyone to sign.
func Envelope(message string) ([]byte, error) {
	if len(message) == 0 || len(message) > maxLedgerMessage {
		return nil, errors.New("off-chain message length is out of range")
	}
	for i := 0; i < len(message); i++ {
		if message[i] < 0x20 || message[i] > 0x7e {
			return nil, errors.New("off-chain message must be printable ASCII")
		}
	}
	sealed := make([]byte, 0, len(signingDomain)+4+len(message))
	sealed = append(sealed, signingDomain...)
	sealed = append(sealed, version, formatRestrictedASCII)
	sealed = binary.LittleEndian.AppendUint16(sealed, uint16(len(message)))
	sealed = append(sealed, message...)
	return sealed, nil
}

// Verify checks a 64-byte signature over the message for the 32-byte public
// key, accepting the enveloped form (solana CLI, hardware wallets) first and
// the raw form (browser-wallet signMessage) second.
func Verify(publicKey [32]byte, message string, signature [64]byte) (bool, error) {
	sealed, err := Envelope(message)
	if err != nil {
		return false, err
	}
	public := ed25519.PublicKey(publicKey[:])
	if ed25519.Verify(public, sealed, signature[:]) {
		return true, nil
	}
	return ed25519.Verify(public, []byte(message), signature[:]), nil
}
