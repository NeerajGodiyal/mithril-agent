package sealedtx

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	Domain             = "mithril-agent/sealed-transaction-v2"
	Version            = 2
	maxTransactionSize = 8 << 10
)

type Metadata struct {
	Version              uint32 `json:"version"`
	Domain               string `json:"domain"`
	ActionID             string `json:"action_id"`
	MessageSHA256        string `json:"message_sha256"`
	TransactionSHA256    string `json:"transaction_sha256"`
	Signature            string `json:"signature"`
	BlockhashContextSlot uint64 `json:"blockhash_context_slot"`
	FeeLamports          uint64 `json:"fee_lamports"`
	LastValidBlockHeight uint64 `json:"last_valid_block_height"`
}

type Envelope struct {
	Metadata                 Metadata `json:"metadata"`
	EphemeralPublicKeyBase64 string   `json:"ephemeral_public_key_base64"`
	NonceBase64              string   `json:"nonce_base64"`
	CiphertextBase64         string   `json:"ciphertext_base64"`
}

func Seal(
	recipientPublicKey string,
	metadata Metadata,
	transaction []byte,
	random io.Reader,
) (Envelope, error) {
	if random == nil {
		random = rand.Reader
	}
	if len(transaction) == 0 || len(transaction) > maxTransactionSize {
		return Envelope{}, errors.New("signed transaction size is invalid")
	}
	transactionHash := sha256.Sum256(transaction)
	if metadata.TransactionSHA256 != hex.EncodeToString(transactionHash[:]) {
		return Envelope{}, errors.New("sealed transaction hash does not match metadata")
	}
	if err := validateMetadata(metadata); err != nil {
		return Envelope{}, err
	}
	curve := ecdh.X25519()
	recipientBytes, err := hex.DecodeString(recipientPublicKey)
	if err != nil || len(recipientBytes) != 32 || hex.EncodeToString(recipientBytes) != recipientPublicKey {
		return Envelope{}, errors.New("submitter public key is invalid")
	}
	recipient, err := curve.NewPublicKey(recipientBytes)
	if err != nil {
		return Envelope{}, errors.New("submitter public key is invalid")
	}
	ephemeral, err := curve.GenerateKey(random)
	if err != nil {
		return Envelope{}, errors.New("generate sealed transaction key")
	}
	shared, err := ephemeral.ECDH(recipient)
	if err != nil {
		return Envelope{}, errors.New("derive sealed transaction key")
	}
	key := deriveKey(shared, ephemeral.PublicKey().Bytes(), recipient.Bytes())
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return Envelope{}, errors.New("create sealed transaction cipher")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Envelope{}, errors.New("create sealed transaction envelope")
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(random, nonce); err != nil {
		return Envelope{}, errors.New("generate sealed transaction nonce")
	}
	aad, err := json.Marshal(metadata)
	if err != nil {
		return Envelope{}, errors.New("encode sealed transaction metadata")
	}
	ciphertext := aead.Seal(nil, nonce, transaction, aad)
	return Envelope{
		Metadata:                 metadata,
		EphemeralPublicKeyBase64: base64.StdEncoding.EncodeToString(ephemeral.PublicKey().Bytes()),
		NonceBase64:              base64.StdEncoding.EncodeToString(nonce),
		CiphertextBase64:         base64.StdEncoding.EncodeToString(ciphertext),
	}, nil
}

func Open(recipientPrivateKey string, envelope Envelope) ([]byte, error) {
	if err := validateMetadata(envelope.Metadata); err != nil {
		return nil, err
	}
	curve := ecdh.X25519()
	privateBytes, err := hex.DecodeString(recipientPrivateKey)
	if err != nil || len(privateBytes) != 32 || hex.EncodeToString(privateBytes) != recipientPrivateKey {
		return nil, errors.New("submitter private key is invalid")
	}
	privateKey, err := curve.NewPrivateKey(privateBytes)
	if err != nil {
		return nil, errors.New("submitter private key is invalid")
	}
	ephemeralBytes, err := base64.StdEncoding.Strict().DecodeString(
		envelope.EphemeralPublicKeyBase64,
	)
	if err != nil || len(ephemeralBytes) != 32 {
		return nil, errors.New("sealed transaction ephemeral key is invalid")
	}
	ephemeral, err := curve.NewPublicKey(ephemeralBytes)
	if err != nil {
		return nil, errors.New("sealed transaction ephemeral key is invalid")
	}
	shared, err := privateKey.ECDH(ephemeral)
	if err != nil {
		return nil, errors.New("derive sealed transaction key")
	}
	key := deriveKey(shared, ephemeral.Bytes(), privateKey.PublicKey().Bytes())
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, errors.New("create sealed transaction cipher")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("open sealed transaction envelope")
	}
	nonce, err := base64.StdEncoding.Strict().DecodeString(envelope.NonceBase64)
	if err != nil || len(nonce) != aead.NonceSize() {
		return nil, errors.New("sealed transaction nonce is invalid")
	}
	ciphertext, err := base64.StdEncoding.Strict().DecodeString(envelope.CiphertextBase64)
	if err != nil || len(ciphertext) <= aead.Overhead() || len(ciphertext) > maxTransactionSize+aead.Overhead() {
		return nil, errors.New("sealed transaction ciphertext is invalid")
	}
	aad, err := json.Marshal(envelope.Metadata)
	if err != nil {
		return nil, errors.New("encode sealed transaction metadata")
	}
	transaction, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, errors.New("sealed transaction authentication failed")
	}
	digest := sha256.Sum256(transaction)
	if envelope.Metadata.TransactionSHA256 != hex.EncodeToString(digest[:]) {
		return nil, errors.New("opened transaction hash does not match metadata")
	}
	return transaction, nil
}

func GenerateKey(random io.Reader) (privateKey, publicKey string, err error) {
	if random == nil {
		random = rand.Reader
	}
	key, err := ecdh.X25519().GenerateKey(random)
	if err != nil {
		return "", "", errors.New("generate submitter key")
	}
	return hex.EncodeToString(key.Bytes()), hex.EncodeToString(key.PublicKey().Bytes()), nil
}

func PublicKey(privateKey string) (string, error) {
	decoded, err := hex.DecodeString(privateKey)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != privateKey {
		return "", errors.New("submitter private key is invalid")
	}
	key, err := ecdh.X25519().NewPrivateKey(decoded)
	if err != nil {
		return "", errors.New("submitter private key is invalid")
	}
	return hex.EncodeToString(key.PublicKey().Bytes()), nil
}

func validateMetadata(metadata Metadata) error {
	if metadata.Version != Version || metadata.Domain != Domain ||
		!validDigest(metadata.ActionID) || !validDigest(metadata.MessageSHA256) ||
		!validDigest(metadata.TransactionSHA256) || metadata.FeeLamports == 0 ||
		metadata.BlockhashContextSlot == 0 || metadata.LastValidBlockHeight == 0 {
		return errors.New("sealed transaction metadata is invalid")
	}
	if _, err := solana.Decode64(metadata.Signature); err != nil {
		return errors.New("sealed transaction signature is invalid")
	}
	return nil
}

func deriveKey(shared, ephemeralPublic, recipientPublic []byte) [32]byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte(Domain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(shared)
	_, _ = hash.Write(ephemeralPublic)
	_, _ = hash.Write(recipientPublic)
	var key [32]byte
	copy(key[:], hash.Sum(nil))
	return key
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}
