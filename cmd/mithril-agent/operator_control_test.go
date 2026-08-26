package main

import (
	"errors"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
)

// withDirectOperatorControl keeps command tests focused on command behavior.
// The submitter command tests exercise the real root-only socket protocol.
func withDirectOperatorControl(t *testing.T, cfg config) {
	t.Helper()
	_, _, _, fingerprint, err := cfg.activeProfile()
	if err != nil {
		t.Fatal(err)
	}
	state, err := control.NewStateFile(cfg.Control.StatePath, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	oldEnable := enableOperatorControl
	oldStatus := operatorControlStatus
	oldAcknowledge := acknowledgeOperatorControl
	enableOperatorControl = func(
		_, expectedRevision string,
		issuedAt, expiresAt time.Time,
		maxActions uint32,
		reason string,
	) error {
		written, err := control.WriteDevnetActivationIfRevision(
			cfg.Control.StatePath, fingerprint, expectedRevision,
			issuedAt, expiresAt, maxActions, reason,
		)
		if err != nil {
			return err
		}
		if !written {
			return errors.New("control state changed while enabling; inspect status and retry")
		}
		return nil
	}
	operatorControlStatus = func(string) (control.Status, string, error) {
		status, err := state.Status()
		if err != nil {
			return control.Status{}, "", err
		}
		revision, err := state.Revision()
		return status, revision, err
	}
	acknowledgeOperatorControl = func(
		_, actionID, outcome, reason string,
	) (control.Status, error) {
		if err := state.StopForTerminal(actionID, outcome); err != nil {
			return control.Status{}, err
		}
		if outcome == "halted" {
			return state.Status()
		}
		return state.AcknowledgeTerminal(actionID, outcome, reason)
	}
	t.Cleanup(func() {
		enableOperatorControl = oldEnable
		operatorControlStatus = oldStatus
		acknowledgeOperatorControl = oldAcknowledge
	})
}
