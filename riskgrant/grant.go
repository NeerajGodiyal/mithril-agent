package riskgrant

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

const (
	Domain       = "mithril-agent/risk-grant-v2"
	Version      = 2
	MaxLifetime  = 5 * time.Minute
	maxClockSkew = 5 * time.Second
)

type Binding struct {
	ActionID             string
	ProfileFingerprint   string
	MessageSHA256        string
	RequestSHA256        string
	FeeLamports          uint64
	LastValidBlockHeight uint64
}

type Claims struct {
	Version              uint32 `json:"version"`
	Domain               string `json:"domain"`
	KeyID                string `json:"key_id"`
	IssuedAtUnix         int64  `json:"issued_at_unix"`
	ExpiresAtUnix        int64  `json:"expires_at_unix"`
	ActionID             string `json:"action_id"`
	ProfileFingerprint   string `json:"profile_sha256"`
	MessageSHA256        string `json:"message_sha256"`
	RequestSHA256        string `json:"request_sha256"`
	FeeLamports          uint64 `json:"fee_lamports"`
	LastValidBlockHeight uint64 `json:"last_valid_block_height"`
}

type Grant struct {
	Claims          Claims `json:"claims"`
	SignatureBase64 string `json:"signature_base64"`
}

func Sign(
	privateKey ed25519.PrivateKey,
	keyID string,
	binding Binding,
	now time.Time,
	lifetime time.Duration,
) (Grant, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return Grant{}, errors.New("risk authority private key is invalid")
	}
	if lifetime <= 0 || lifetime > MaxLifetime {
		return Grant{}, errors.New("risk grant lifetime is invalid")
	}
	now = now.UTC()
	claims := Claims{
		Version:              Version,
		Domain:               Domain,
		KeyID:                keyID,
		IssuedAtUnix:         now.Unix(),
		ExpiresAtUnix:        now.Add(lifetime).Unix(),
		ActionID:             binding.ActionID,
		ProfileFingerprint:   binding.ProfileFingerprint,
		MessageSHA256:        binding.MessageSHA256,
		RequestSHA256:        binding.RequestSHA256,
		FeeLamports:          binding.FeeLamports,
		LastValidBlockHeight: binding.LastValidBlockHeight,
	}
	if err := validateClaims(claims, now); err != nil {
		return Grant{}, err
	}
	message, err := signedMessage(claims)
	if err != nil {
		return Grant{}, err
	}
	return Grant{
		Claims:          claims,
		SignatureBase64: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, message)),
	}, nil
}

func Verify(
	publicKey ed25519.PublicKey,
	keyID string,
	binding Binding,
	grant Grant,
	now time.Time,
) error {
	if len(publicKey) != ed25519.PublicKeySize || grant.Claims.KeyID != keyID ||
		grant.Claims.ActionID != binding.ActionID ||
		grant.Claims.ProfileFingerprint != binding.ProfileFingerprint ||
		grant.Claims.MessageSHA256 != binding.MessageSHA256 ||
		grant.Claims.RequestSHA256 != binding.RequestSHA256 ||
		grant.Claims.FeeLamports != binding.FeeLamports ||
		grant.Claims.LastValidBlockHeight != binding.LastValidBlockHeight {
		return errors.New("risk grant binding does not match")
	}
	if err := validateClaims(grant.Claims, now.UTC()); err != nil {
		return err
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(grant.SignatureBase64)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("risk grant signature is invalid")
	}
	message, err := signedMessage(grant.Claims)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, message, signature) {
		return errors.New("risk grant signature does not verify")
	}
	return nil
}

func DecodePublicKey(value string) (ed25519.PublicKey, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != ed25519.PublicKeySize ||
		hex.EncodeToString(decoded) != value {
		return nil, errors.New("risk authority public key is invalid")
	}
	return ed25519.PublicKey(decoded), nil
}

func PublicKeyHex(privateKey ed25519.PrivateKey) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", errors.New("risk authority private key is invalid")
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return "", errors.New("risk authority public key is invalid")
	}
	return hex.EncodeToString(publicKey), nil
}

func validateClaims(claims Claims, now time.Time) error {
	if claims.Version != Version || claims.Domain != Domain || claims.KeyID == "" ||
		!validDigest(claims.ActionID) || !validDigest(claims.ProfileFingerprint) ||
		!validDigest(claims.MessageSHA256) || !validDigest(claims.RequestSHA256) ||
		claims.FeeLamports == 0 || claims.LastValidBlockHeight == 0 ||
		claims.IssuedAtUnix <= 0 ||
		claims.ExpiresAtUnix <= claims.IssuedAtUnix ||
		claims.ExpiresAtUnix-claims.IssuedAtUnix > int64(MaxLifetime/time.Second) {
		return errors.New("risk grant claims are invalid")
	}
	nowUnix := now.Unix()
	if nowUnix < claims.IssuedAtUnix-int64(maxClockSkew/time.Second) ||
		nowUnix >= claims.ExpiresAtUnix {
		return errors.New("risk grant is not currently valid")
	}
	return nil
}

func signedMessage(claims Claims) ([]byte, error) {
	encoded, err := json.Marshal(claims)
	if err != nil {
		return nil, errors.New("encode risk grant claims")
	}
	domainHash := sha256.Sum256([]byte(Domain))
	message := make([]byte, 0, len(domainHash)+1+len(encoded))
	message = append(message, domainHash[:]...)
	message = append(message, 0)
	message = append(message, encoded...)
	return message, nil
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}
