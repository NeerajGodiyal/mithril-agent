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
	responseAttestationDomain  = "mithril-agent/signer-response-attestation-v1"
	responseAttestationVersion = 1
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
	attestation := ResponseAttestation{
		Version: responseAttestationVersion, Domain: responseAttestationDomain,
		SubmitterPublicKey: submitterPublicKey,
	}
	if response.SealedTransaction.Metadata != responseMetadata(response) {
		return ResponseAttestation{}, errors.New("signer response metadata does not match")
	}
	message, err := responseAttestationMessage(attestation, response.SealedTransaction.Metadata)
	if err != nil {
		return ResponseAttestation{}, err
	}
	attestation.SignatureBase64 = base64.StdEncoding.EncodeToString(
		ed25519.Sign(privateKey, message),
	)
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
		response.SignerAttestation,
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
	metadata sealedtx.Metadata,
) ([]byte, error) {
	if attestation.Version != responseAttestationVersion ||
		attestation.Domain != responseAttestationDomain ||
		attestation.SubmitterPublicKey == "" {
		return nil, errors.New("signer response attestation is invalid")
	}
	claims := responseAttestationClaims{
		Version: attestation.Version, Domain: attestation.Domain,
		SubmitterPublicKey: attestation.SubmitterPublicKey, Metadata: metadata,
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
