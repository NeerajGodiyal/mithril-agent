package signer

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"
)

func TestResponseAttestationBindsSignerSubmitterAndMetadata(t *testing.T) {
	policy, privateKey, request := signerFixture(t)
	response, err := signAt(
		policy, privateKey, request,
		time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyResponseAttestation(
		policy.Source, policy.SubmitterPublicKey, response,
	); err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(*Response){
		"action": func(value *Response) {
			value.ActionID = strings.Repeat("0", 64)
			value.SealedTransaction.Metadata.ActionID = value.ActionID
		},
		"message hash": func(value *Response) {
			value.MessageSHA256 = strings.Repeat("0", 64)
			value.SealedTransaction.Metadata.MessageSHA256 = value.MessageSHA256
		},
		"transaction hash": func(value *Response) {
			value.TransactionSHA256 = strings.Repeat("0", 64)
			value.SealedTransaction.Metadata.TransactionSHA256 = value.TransactionSHA256
		},
		"transaction signature": func(value *Response) {
			value.Signature = strings.Repeat("1", len(value.Signature))
			value.SealedTransaction.Metadata.Signature = value.Signature
		},
		"blockhash context": func(value *Response) {
			value.BlockhashContextSlot++
			value.SealedTransaction.Metadata.BlockhashContextSlot++
		},
		"fee": func(value *Response) {
			value.FeeLamports++
			value.SealedTransaction.Metadata.FeeLamports++
		},
		"last valid height": func(value *Response) {
			value.LastValidBlockHeight++
			value.SealedTransaction.Metadata.LastValidBlockHeight++
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := response
			mutate(&changed)
			if err := VerifyResponseAttestation(
				policy.Source, policy.SubmitterPublicKey, changed,
			); err == nil {
				t.Fatal("changed response kept its attestation")
			}
		})
	}

	missing := response
	missing.SignerAttestation = ResponseAttestation{}
	if err := VerifyResponseAttestation(
		policy.Source, policy.SubmitterPublicKey, missing,
	); err == nil {
		t.Fatal("missing response attestation was accepted")
	}

	wrongKey := signerTestKey("wrong-attestation-signer")
	wrong := response
	wrong.SignerAttestation, err = AttestResponse(
		ed25519.PrivateKey(wrongKey), policy.SubmitterPublicKey, wrong,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyResponseAttestation(
		policy.Source, policy.SubmitterPublicKey, wrong,
	); err == nil {
		t.Fatal("attestation from the wrong signer was accepted")
	}

	if err := VerifyResponseAttestation(
		policy.Source, strings.Repeat("0", 64), response,
	); err == nil {
		t.Fatal("attestation for a different submitter was accepted")
	}
}
