package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/submitter"
)

const paperTerminalEvent = "paper.request-terminal-v1"

type paperTerminal struct {
	ClaimSHA256 string                             `json:"claim_sha256"`
	Finalized   submitter.JupiterFinalizedEvidence `json:"finalized"`
}

// RecordPaperTerminal records finalized effects for one exact original claim.
// This offline composition requires host ownership permitting both private
// journals to be read under existing checks; it is not cross-service transport.
// It never accepts caller-supplied effects, releases a claim, credits inventory,
// signs or submits. The first-action lock remains permanent after completion.
func RecordPaperTerminal(path string, authority policyauthority.Policy, request signer.Request,
	recoveryPolicy submitter.Policy, now time.Time,
) (submitter.JupiterFinalizedEvidence, error) {
	return recordPaperTerminal(path, authority, request, now, func() (submitter.JupiterFinalizedEvidence, error) {
		return submitter.ReadJupiterFinalizedEvidence(recoveryPolicy, request)
	})
}

func recordPaperTerminal(path string, authority policyauthority.Policy, request signer.Request,
	now time.Time, readFinalized func() (submitter.JupiterFinalizedEvidence, error),
) (result submitter.JupiterFinalizedEvidence, err error) {
	// Refuse missing/invalid evidence without creating or repairing a journal.
	records, err := journal.ReadRecords(path)
	if err != nil {
		return result, err
	}
	if _, err := paperTerminalClaim(records, authority, request, now); err != nil {
		return result, err
	}
	store, err := journal.Open(path)
	if err != nil {
		return result, err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			result = submitter.JupiterFinalizedEvidence{}
			err = errors.Join(err, closeErr)
		}
	}()
	claim, err := paperTerminalClaim(store.Records(), authority, request, now)
	if err != nil {
		return result, err
	}
	result, err = readFinalized()
	if err != nil {
		return submitter.JupiterFinalizedEvidence{}, err
	}
	if err := appendPaperTerminal(store, claim, request.ActionID, result, now); err != nil {
		return submitter.JupiterFinalizedEvidence{}, err
	}
	return result, nil
}

func paperTerminalClaim(records []journal.Record, authority policyauthority.Policy, request signer.Request, now time.Time) (string, error) {
	if len(records) < 1 || len(records) > 2 || now.IsZero() || now.Before(records[len(records)-1].At) ||
		(len(records) == 2 && (records[1].Type != paperTerminalEvent || records[1].ActionID != request.ActionID)) {
		return "", errors.New("paper terminal journal state is invalid")
	}
	return policyauthority.ValidatePaperRequestClaim(records[0], authority, request)
}

func appendPaperTerminal(store *journal.Store, claim, actionID string, evidence submitter.JupiterFinalizedEvidence, now time.Time) error {
	terminal := paperTerminal{ClaimSHA256: claim, Finalized: evidence}
	payload, err := json.Marshal(terminal)
	if err != nil {
		return err
	}
	records := store.Records()
	if len(records) == 2 {
		if records[1].Type != paperTerminalEvent || records[1].ActionID != actionID || !bytes.Equal(records[1].Payload, payload) {
			return errors.New("paper terminal evidence conflicts with the existing marker")
		}
		return nil
	}
	if len(records) != 1 {
		return errors.New("paper terminal requires one original claim")
	}
	_, err = store.Append(now.UTC(), paperTerminalEvent, actionID, terminal)
	return err
}
