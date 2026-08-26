// Package submittertransport defines the bounded local protocol between an
// agent runner and its isolated transaction submitter.
package submittertransport

import (
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const (
	Version = 1

	OperationIdentity = "identity"
	OperationSubmit   = "submit"
	OperationPrepare  = "prepare_mainnet"
	OperationStatus   = "status"
	OperationReserve  = "reserve"
	OperationRecover  = "recovery_allowed"
	OperationStop     = "stop"
	OperationTerminal = "stop_terminal"
	OperationLatch    = "terminal_latch"

	OperationOperatorStatus      = "operator_status"
	OperationOperatorSnapshot    = "operator_snapshot"
	OperationEnable              = "enable"
	OperationAcknowledgeTerminal = "acknowledge_terminal"

	StatusOK              = "ok"
	StatusRefused         = "refused"
	StatusRecoveryPending = "recovery_pending"
	StatusConflict        = "conflict"
	StatusFailed          = "failed"
)

// Request asks one socket-activated submitter process to identify itself,
// prepare one Mainnet handoff, or submit one Devnet transaction.
type Request struct {
	Version          uint32           `json:"version"`
	Operation        string           `json:"operation"`
	SignerRequest    *signer.Request  `json:"signer_request,omitempty"`
	SignerResponse   *signer.Response `json:"signer_response,omitempty"`
	MinContextSlot   uint64           `json:"min_context_slot,omitempty"`
	ActionID         string           `json:"action_id,omitempty"`
	Outcome          string           `json:"outcome,omitempty"`
	Reason           string           `json:"reason,omitempty"`
	ExpectedRevision string           `json:"expected_revision,omitempty"`
	IssuedAt         time.Time        `json:"issued_at,omitzero"`
	ExpiresAt        time.Time        `json:"expires_at,omitzero"`
	MaxActions       uint32           `json:"max_actions,omitempty"`
}

type Identity struct {
	PublicKey          string `json:"public_key"`
	ProfileFingerprint string `json:"profile_sha256"`
	Source             string `json:"source"`
}

// Response carries exactly one successful result or a bounded status. Service
// errors never cross the isolation boundary.
type Response struct {
	Version    uint32             `json:"version"`
	Status     string             `json:"status"`
	Identity   *Identity          `json:"identity,omitempty"`
	Submission *txflow.Submission `json:"submission,omitempty"`
	Control    *control.Status    `json:"control,omitempty"`
	ActionID   string             `json:"action_id,omitempty"`
	Outcome    string             `json:"outcome,omitempty"`
	Revision   string             `json:"revision,omitempty"`
}
