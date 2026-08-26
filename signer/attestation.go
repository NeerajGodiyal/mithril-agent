package signer

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	responseAttestationDomain  = "mithril-agent/signer-response-attestation-v2"
	responseAttestationVersion = 2
)

type ResponseAttestation struct {
	Version            uint32 `json:"version"`
	Domain             string `json:"domain"`
	SubmitterPublicKey string `json:"submitter_public_key"`
	SignatureBase64    string `json:"signature_base64"`
}

type responseAttestationClaims struct {
	Version            uint32            `json:"version"`
	Domain             string            `json:"domain"`
	SubmitterPublicKey string            `json:"submitter_public_key"`
	RequestSHA256      string            `json:"request_sha256"`
	Metadata           sealedtx.Metadata `json:"metadata"`
}

// AttestResponse authenticates a response produced by a compatible signer implementation.
func AttestResponse(
	privateKey ed25519.PrivateKey,
	submitterPublicKey string,
	response Response,
) (ResponseAttestation, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return ResponseAttestation{}, errors.New("response attestation key is invalid")
	}
	return attestResponse(submitterPublicKey, response, func(message []byte) ([]byte, error) {
		return ed25519.Sign(privateKey, message), nil
	})
}

// AttestResponseWith delegates response authentication to a separate service
// identity. A managed transaction wallet therefore never needs arbitrary
// message-signing authority, which would bypass its transaction policy.
func AttestResponseWith(
	expectedSigner string,
	submitterPublicKey string,
	response Response,
	sign func([]byte) ([]byte, error),
) (ResponseAttestation, error) {
	if sign == nil {
		return ResponseAttestation{}, errors.New("response attestation signer is unavailable")
	}
	attestation, err := attestResponse(submitterPublicKey, response, sign)
	if err != nil {
		return ResponseAttestation{}, err
	}
	checked := response
	checked.SignerAttestation = attestation
	if err := VerifyResponseAttestation(expectedSigner, submitterPublicKey, checked); err != nil {
		return ResponseAttestation{}, err
	}
	return attestation, nil
}

func attestResponse(
	submitterPublicKey string,
	response Response,
	sign func([]byte) ([]byte, error),
) (ResponseAttestation, error) {
	attestation := ResponseAttestation{
		Version: responseAttestationVersion, Domain: responseAttestationDomain,
		SubmitterPublicKey: submitterPublicKey,
	}
	if response.SealedTransaction.Metadata != responseMetadata(response) {
		return ResponseAttestation{}, errors.New("signer response metadata does not match")
	}
	message, err := responseAttestationMessage(
		attestation, response.RequestSHA256, response.SealedTransaction.Metadata,
	)
	if err != nil {
		return ResponseAttestation{}, err
	}
	signature, err := sign(message)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return ResponseAttestation{}, errors.New("sign response attestation")
	}
	attestation.SignatureBase64 = base64.StdEncoding.EncodeToString(signature)
	return attestation, nil
}

// VerifyResponseAttestation proves who signed the response and which submitter may open it.
func VerifyResponseAttestation(
	expectedSigner string,
	expectedSubmitter string,
	response Response,
) error {
	if response.SignerAttestation.SubmitterPublicKey != expectedSubmitter ||
		response.SealedTransaction.Metadata != responseMetadata(response) {
		return errors.New("signer response attestation binding does not match")
	}
	publicKey, err := solana.Decode32(expectedSigner)
	if err != nil {
		return errors.New("signer response attestation public key is invalid")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(
		response.SignerAttestation.SignatureBase64,
	)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("signer response attestation is invalid")
	}
	message, err := responseAttestationMessage(
		response.SignerAttestation, response.RequestSHA256,
		response.SealedTransaction.Metadata,
	)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey[:]), message, signature) {
		return errors.New("signer response attestation does not verify")
	}
	return nil
}

func responseAttestationMessage(
	attestation ResponseAttestation,
	requestSHA256 string,
	metadata sealedtx.Metadata,
) ([]byte, error) {
	if attestation.Version != responseAttestationVersion ||
		attestation.Domain != responseAttestationDomain ||
		attestation.SubmitterPublicKey == "" || !validDigest(requestSHA256) {
		return nil, errors.New("signer response attestation is invalid")
	}
	claims := responseAttestationClaims{
		Version: attestation.Version, Domain: attestation.Domain,
		SubmitterPublicKey: attestation.SubmitterPublicKey,
		RequestSHA256:      requestSHA256, Metadata: metadata,
	}
	encoded, err := json.Marshal(claims)
	if err != nil {
		return nil, errors.New("encode signer response attestation")
	}
	domainHash := sha256.Sum256([]byte(responseAttestationDomain))
	message := make([]byte, 0, len(domainHash)+1+len(encoded))
	message = append(message, domainHash[:]...)
	message = append(message, 0)
	message = append(message, encoded...)
	return message, nil
}

func responseMetadata(response Response) sealedtx.Metadata {
	return sealedtx.Metadata{
		Version: sealedtx.Version, Domain: sealedtx.Domain,
		ActionID: response.ActionID, MessageSHA256: response.MessageSHA256,
		TransactionSHA256: response.TransactionSHA256, Signature: response.Signature,
		BlockhashContextSlot: response.BlockhashContextSlot,
		FeeLamports:          response.FeeLamports,
		LastValidBlockHeight: response.LastValidBlockHeight,
	}
}
