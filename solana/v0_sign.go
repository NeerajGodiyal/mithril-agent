package solana

import (
	"bytes"
	"crypto/ed25519"
	"errors"
)

// SignedV0Transaction is one verified, single-signer version-0 transaction.
type SignedV0Transaction struct {
	Signature [64]byte
	Message   V0Message
	Raw       []byte
}

// SignedTransactionEnvelope is the signature and raw message common to legacy
// and version-0 transactions. Version-0 lookup contents are deliberately not
// resolved here; semantic consumers must still supply independently verified
// tables to DecodeSignedV0Transaction.
type SignedTransactionEnvelope struct {
	Signature [64]byte
	FeePayer  [32]byte
	Message   []byte
	Version   int
}

// VerifySignedTransactionEnvelope accepts the two transaction versions this
// agent understands and verifies the sole fee-payer signature.
func VerifySignedTransactionEnvelope(transaction []byte) (SignedTransactionEnvelope, error) {
	legacy, legacyErr := DecodeSignedLegacyTransaction(transaction)
	if legacyErr == nil {
		return SignedTransactionEnvelope{
			Signature: legacy.Signature, FeePayer: legacy.Message.AccountKeys[0],
			Message: bytes.Clone(legacy.Message.Raw), Version: -1,
		}, nil
	}
	if len(transaction) < 1+ed25519.SignatureSize+1 || len(transaction) > maxTransactionBytes ||
		transaction[0] != 1 {
		return SignedTransactionEnvelope{}, errors.New("transaction signature framing is invalid")
	}
	message := transaction[1+ed25519.SignatureSize:]
	if len(message) < 1+3+1+32 || message[0] != 0x80 {
		return SignedTransactionEnvelope{}, errors.New("transaction version is unsupported")
	}
	reader := byteReader{data: message, offset: 1}
	required, err := reader.byte()
	if err != nil {
		return SignedTransactionEnvelope{}, err
	}
	readonlySigned, err := reader.byte()
	if err != nil {
		return SignedTransactionEnvelope{}, err
	}
	readonlyUnsigned, err := reader.byte()
	if err != nil {
		return SignedTransactionEnvelope{}, err
	}
	staticCount, err := reader.shortVec()
	if err != nil || required != 1 || readonlySigned != 0 || staticCount == 0 ||
		staticCount > 256 || int(readonlyUnsigned) > staticCount-1 {
		return SignedTransactionEnvelope{}, errors.New("version-0 transaction signer header is invalid")
	}
	var feePayer [32]byte
	if err := reader.fixed(feePayer[:]); err != nil {
		return SignedTransactionEnvelope{}, errors.New("version-0 transaction fee payer is missing")
	}
	var signature [64]byte
	copy(signature[:], transaction[1:1+ed25519.SignatureSize])
	if !ed25519.Verify(ed25519.PublicKey(feePayer[:]), message, signature[:]) {
		return SignedTransactionEnvelope{}, errors.New("version-0 transaction signature is invalid")
	}
	return SignedTransactionEnvelope{
		Signature: signature, FeePayer: feePayer,
		Message: bytes.Clone(message), Version: 0,
	}, nil
}

// SignV0Message signs a previously compiled version-0 message after resolving
// its lookup tables and requiring the private key to be its sole fee payer.
func SignV0Message(
	privateKey ed25519.PrivateKey,
	message []byte,
	addressTables map[[32]byte][][32]byte,
) ([]byte, [64]byte, error) {
	var signature [64]byte
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, signature, errors.New("v0 signer private key is invalid")
	}
	decoded, err := DecodeV0Message(message, addressTables)
	if err != nil {
		return nil, signature, err
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || !bytes.Equal(publicKey, decoded.AccountKeys[0][:]) {
		return nil, signature, errors.New("v0 signer key does not match the fee payer")
	}
	if err := ValidateV0MessageForSigner(decoded, Encode(publicKey)); err != nil {
		return nil, signature, err
	}
	copy(signature[:], ed25519.Sign(privateKey, message))
	transaction := make([]byte, 0, 1+ed25519.SignatureSize+len(message))
	transaction = append(transaction, 1)
	transaction = append(transaction, signature[:]...)
	transaction = append(transaction, message...)
	if len(transaction) > maxTransactionBytes {
		return nil, [64]byte{}, errors.New("v0 transaction exceeds Solana packet limit")
	}
	if _, err := DecodeSignedV0Transaction(transaction, addressTables); err != nil {
		return nil, [64]byte{}, err
	}
	return transaction, signature, nil
}

// DecodeSignedV0Transaction validates canonical one-signature framing, the
// resolved message, and the Ed25519 signature.
func DecodeSignedV0Transaction(
	transaction []byte,
	addressTables map[[32]byte][][32]byte,
) (SignedV0Transaction, error) {
	if len(transaction) < 1+ed25519.SignatureSize+1 || transaction[0] != 1 {
		return SignedV0Transaction{}, errors.New("v0 transaction signature framing is invalid")
	}
	messageBytes := transaction[1+ed25519.SignatureSize:]
	message, err := DecodeV0Message(messageBytes, addressTables)
	if err != nil {
		return SignedV0Transaction{}, err
	}
	if message.RequiredSignatures != 1 || len(message.AccountKeys) == 0 {
		return SignedV0Transaction{}, errors.New("v0 transaction must have one signer")
	}
	var signature [64]byte
	copy(signature[:], transaction[1:1+ed25519.SignatureSize])
	if !ed25519.Verify(ed25519.PublicKey(message.AccountKeys[0][:]), messageBytes, signature[:]) {
		return SignedV0Transaction{}, errors.New("v0 transaction signature is invalid")
	}
	return SignedV0Transaction{
		Signature: signature, Message: message, Raw: bytes.Clone(transaction),
	}, nil
}
