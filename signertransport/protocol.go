// Package signertransport defines the bounded local protocol between an agent
// runner and its isolated signer process.
package signertransport

import "github.com/Overclock-Validator/mithril-agent/signer"

const (
	Version = 1

	OperationIdentity = "identity"
	OperationSign     = "sign"

	StatusOK      = "ok"
	StatusRefused = "refused"
	StatusFailed  = "failed"
)

// Request asks one socket-activated signer process to identify itself or sign
// one already-authorized request.
type Request struct {
	Version   uint32          `json:"version"`
	Operation string          `json:"operation"`
	Sign      *signer.Request `json:"sign_request,omitempty"`
}

// Identity is the public wallet identity held by the signer.
type Identity struct {
	PublicKey            string `json:"public_key"`
	AttestationPublicKey string `json:"attestation_public_key,omitempty"`
	SubmitterPublicKey   string `json:"submitter_public_key,omitempty"`
	ProfileSHA256        string `json:"profile_sha256"`
}

// Response carries exactly one successful result or a bounded refusal/failure
// status. Internal errors are never serialized across the signer boundary.
type Response struct {
	Version  uint32           `json:"version"`
	Status   string           `json:"status"`
	Identity *Identity        `json:"identity,omitempty"`
	Signed   *signer.Response `json:"signed_response,omitempty"`
	Reason   string           `json:"reason,omitempty"`
}
