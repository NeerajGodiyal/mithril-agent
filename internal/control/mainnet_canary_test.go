package control

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMainnetCanaryIsOneShortLivedCompareAndSwapAction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.json")
	fingerprint := strings.Repeat("a", 64)
	gate, err := NewMainnetCanaryStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := gate.Revision()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Second)
	written, err := WriteMainnetCanaryActivationForActionIfRevision(
		path, fingerprint, revision, testActionID,
		now, now.Add(30*time.Minute), 1, "one Mainnet canary",
	)
	if err != nil || !written {
		t.Fatalf("write canary = %t, %v", written, err)
	}
	status, err := gate.Status()
	if err != nil || status.Mode != ModeMainnetCanary || status.MaxActions != 1 ||
		status.RemainingActions != 1 || status.ExpectedActionID != testActionID {
		t.Fatalf("canary status = %+v, %v", status, err)
	}
	if err := ValidateMainnetCanaryStatus(status); err != nil {
		t.Fatalf("validate canary status: %v", err)
	}
	if err := ValidateStatus(status); err == nil {
		t.Fatal("Devnet status validator accepted Mainnet canary status")
	}
	staleWrite, err := WriteMainnetCanaryActivationForActionIfRevision(
		path, fingerprint, revision, testActionID,
		now, now.Add(time.Minute), 1, "stale review",
	)
	if err != nil || staleWrite {
		t.Fatalf("stale revision write = %t, %v", staleWrite, err)
	}

	for name, test := range map[string]struct {
		lifetime time.Duration
		actions  uint32
	}{
		"more than one action": {lifetime: time.Minute, actions: 2},
		"more than one hour":   {lifetime: time.Hour + time.Second, actions: 1},
	} {
		t.Run(name, func(t *testing.T) {
			otherPath := filepath.Join(t.TempDir(), "control.json")
			other, err := NewMainnetCanaryStateFile(otherPath, fingerprint, false)
			if err != nil {
				t.Fatal(err)
			}
			revision, err := other.Revision()
			if err != nil {
				t.Fatal(err)
			}
			written, err := WriteMainnetCanaryActivationForActionIfRevision(
				otherPath, fingerprint, revision, testActionID, now, now.Add(test.lifetime),
				test.actions, "invalid canary",
			)
			if err == nil || written {
				t.Fatalf("invalid canary = %t, %v", written, err)
			}
		})
	}

	invalidPath := filepath.Join(t.TempDir(), "control.json")
	invalidGate, err := NewMainnetCanaryStateFile(invalidPath, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	invalidRevision, err := invalidGate.Revision()
	if err != nil {
		t.Fatal(err)
	}
	written, err = WriteMainnetCanaryActivationForActionIfRevision(
		invalidPath, fingerprint, invalidRevision, "not-an-action-id",
		now, now.Add(time.Minute), 1, "invalid action binding",
	)
	if err == nil || written {
		t.Fatalf("invalid action binding = %t, %v", written, err)
	}
}

func TestMainnetAndDevnetControlDocumentsCannotCrossGates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.json")
	fingerprint := strings.Repeat("b", 64)
	mainnet, err := NewMainnetCanaryStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := mainnet.Revision()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Second)
	written, err := WriteMainnetCanaryActivationForActionIfRevision(
		path, fingerprint, revision, testActionID,
		now, now.Add(time.Minute), 1, "mode separation",
	)
	if err != nil || !written {
		t.Fatalf("write canary = %t, %v", written, err)
	}

	devnet, err := NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := devnet.Status(); err == nil {
		t.Fatal("Devnet gate accepted Mainnet canary state")
	}
	if err := WriteDevnetActivation(
		path, fingerprint, now, now.Add(time.Hour), 1, "wrong mode replacement",
	); err == nil {
		t.Fatal("Devnet activation replaced Mainnet canary state")
	}

	devnetPath := filepath.Join(t.TempDir(), "control.json")
	if err := WriteDevnetActivation(
		devnetPath, fingerprint, now, now.Add(time.Hour), 1, "devnet mode",
	); err != nil {
		t.Fatal(err)
	}
	mainnetOnDevnetPath, err := NewMainnetCanaryStateFile(devnetPath, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mainnetOnDevnetPath.Status(); err == nil {
		t.Fatal("Mainnet gate accepted Devnet state")
	}

	expiredPath := filepath.Join(t.TempDir(), "control.json")
	expiredGate, err := NewMainnetCanaryStateFile(expiredPath, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	expiredRevision, err := expiredGate.Revision()
	if err != nil {
		t.Fatal(err)
	}
	written, err = WriteMainnetCanaryActivationForActionIfRevision(
		expiredPath, fingerprint, expiredRevision, testActionID,
		now.Add(-2*time.Minute), now.Add(-time.Minute), 1, "expired canary",
	)
	if err != nil || !written {
		t.Fatalf("write expired canary = %t, %v", written, err)
	}
	if err := WriteDevnetActivation(
		expiredPath, fingerprint, now, now.Add(time.Hour), 1, "cross-mode activation",
	); err == nil || !strings.Contains(err.Error(), "changing control modes") {
		t.Fatalf("expired cross-mode replacement = %v", err)
	}
	if err := WriteNoNewActions(expiredPath, "explicit mode transition"); err != nil {
		t.Fatal(err)
	}
	if err := WriteDevnetActivation(
		expiredPath, fingerprint, now, now.Add(time.Hour), 1, "after explicit stop",
	); err != nil {
		t.Fatalf("activation after explicit stop: %v", err)
	}
}

func TestMainnetCanaryConsumesOnceAndPreservesExactRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.json")
	fingerprint := strings.Repeat("c", 64)
	gate, err := NewMainnetCanaryStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := gate.Revision()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Second)
	written, err := WriteMainnetCanaryActivationForActionIfRevision(
		path, fingerprint, revision, testActionID,
		now, now.Add(time.Minute), 1, "bounded recovery",
	)
	if err != nil || !written {
		t.Fatalf("write canary = %t, %v", written, err)
	}
	wrongAction := strings.Repeat("d", 64)
	performed := false
	blocked, err := gate.WithSendBarrier(wrongAction, func() error {
		performed = true
		return nil
	})
	if err != nil || !blocked || performed {
		t.Fatalf("wrong action = blocked %t, performed %t, error %v", blocked, performed, err)
	}
	status, err := gate.Status()
	if err != nil || status.RemainingActions != 1 || status.ExpectedActionID != testActionID {
		t.Fatalf("wrong action consumed canary = %+v, %v", status, err)
	}

	performed = false
	blocked, err = gate.WithSendBarrier(testActionID, func() error {
		performed = true
		return nil
	})
	if err != nil || blocked || !performed {
		t.Fatalf("first barrier = blocked %t, performed %t, error %v", blocked, performed, err)
	}
	status, err = gate.Status()
	if err != nil || status.Mode != ModeNoNewActions || !status.RecoveryPending {
		t.Fatalf("pending canary = %+v, %v", status, err)
	}

	recovered := false
	blocked, err = gate.WithRecoverySendBarrier(testActionID, func() error {
		recovered = true
		return nil
	})
	if err != nil || blocked || !recovered {
		t.Fatalf("exact recovery = blocked %t, recovered %t, error %v", blocked, recovered, err)
	}
	if err := gate.ClearTerminalForFinalized(testActionID); err != nil {
		t.Fatal(err)
	}
	status, err = gate.Status()
	if err != nil || status.Mode != ModeNoNewActions || status.RecoveryPending {
		t.Fatalf("finalized canary = %+v, %v", status, err)
	}

	performed = false
	blocked, err = gate.WithSendBarrier(strings.Repeat("d", 64), func() error {
		performed = true
		return nil
	})
	if err != nil || !blocked || performed {
		t.Fatalf("second barrier = blocked %t, performed %t, error %v", blocked, performed, err)
	}
}
