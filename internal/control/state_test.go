package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testActionID = "9e5bff7152eb7da45d239cc3751589f94eec9d495902491b8932857747179052"

func TestStateFileDefaultsStoppedAndBoundsActivation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	fingerprint := "c23f7a4c8d169646c7582a8ce5ef4b97e20b1b5984ce09633b620842b5634694"
	state, err := NewStateFile(path, fingerprint, true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return now }
	state.startedAt = now.Add(-time.Minute)
	blocked, err := state.NoNewActions()
	if err != nil || !blocked {
		t.Fatalf("absent control state = %v, %v", blocked, err)
	}
	if err := os.WriteFile(path, []byte(
		`{"version":3,"mode":"no_new_actions","reason":"operator stop"}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}
	blocked, err = state.NoNewActions()
	if err != nil || !blocked {
		t.Fatalf("valid stopped state = %v, %v", blocked, err)
	}
	enabled, err := json.Marshal(stateDocument{
		Version:            3,
		Mode:               ModeDevnetEnabled,
		ProfileFingerprint: fingerprint,
		IssuedAt:           now.Add(-time.Second),
		ExpiresAt:          now.Add(time.Hour),
		MaxActions:         2,
		RemainingActions:   2,
		Reason:             "bounded devnet test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, enabled, 0o600); err != nil {
		t.Fatal(err)
	}
	blocked, err = state.NoNewActions()
	if err != nil || blocked {
		t.Fatalf("valid devnet activation = %v, %v", blocked, err)
	}
	state.startedAt = now
	blocked, err = state.NoNewActions()
	if err != nil || !blocked {
		t.Fatalf("activation from before restart = %v, %v", blocked, err)
	}
	state.requireFresh = false
	blocked, err = state.NoNewActions()
	if err != nil || blocked {
		t.Fatalf("manual activation after restart = %v, %v", blocked, err)
	}
	state.now = func() time.Time { return now.Add(2 * time.Hour) }
	blocked, err = state.NoNewActions()
	if err != nil || !blocked {
		t.Fatalf("expired activation = %v, %v", blocked, err)
	}
	state.now = func() time.Time { return now }
	for _, content := range []string{
		`{"version":3,"mode":"devnet_enabled","reason":"missing binding"}`,
		`{"version":3,"mode":"devnet_enabled","profile_sha256":"c23f7a4c8d169646c7582a8ce5ef4b97e20b1b5984ce09633b620842b5634694","issued_at":"2026-07-30T11:59:59Z","expires_at":"2026-07-30T13:00:00Z","max_actions":1,"remaining_actions":2,"reason":"too many remaining"}`,
		`{"version":3,"mode":"no_new_actions","reason":""}`,
		`{"version":2,"mode":"no_new_actions","reason":"old version"}`,
		`{"version":3,"mode":"no_new_actions","reason":"stop","extra":1}`,
		`{"version":3,"Mode":"devnet_enabled","mode":"no_new_actions","reason":"stop"}`,
		`{"version":3,"mode":"no_new_actions","profile_sha256":"unexpected","reason":"stop"}`,
		`{"version":3,"mode":"no_new_actions","terminal_outcome":"complete","reason":"stop"}`,
		`{"version":3,"mode":"no_new_actions","terminal_outcome":"halted","reason":"stop"}`,
		`{"version":3,"mode":"no_new_actions","terminal_action_id":"9e5bff7152eb7da45d239cc3751589f94eec9d495902491b8932857747179052","reason":"stop"}`,
		`{"version":3,"mode":"no_new_actions","terminal_action_id":"NOT-A-DIGEST","terminal_outcome":"failed","reason":"stop"}`,
		`{"version":3,"mode":"devnet_enabled","profile_sha256":"c23f7a4c8d169646c7582a8ce5ef4b97e20b1b5984ce09633b620842b5634694","terminal_outcome":"halted","issued_at":"2026-07-30T11:59:59Z","expires_at":"2026-07-30T13:00:00Z","max_actions":1,"remaining_actions":1,"reason":"invalid terminal marker"}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if blocked, err := state.NoNewActions(); err == nil || blocked {
			t.Fatalf("invalid control state was accepted: %s", content)
		}
	}
}

func TestStatusSerializesWithStateReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "control.json")
	fingerprint := strings.Repeat("a", 64)
	state, err := NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteDevnetActivation(
		path,
		fingerprint,
		time.Now().UTC().Add(-time.Second),
		time.Now().UTC().Add(time.Hour),
		1,
		"concurrency test",
	); err != nil {
		t.Fatal(err)
	}

	const iterations = 100
	errorsFound := make(chan error, iterations*2)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for index := 0; index < iterations; index++ {
			if _, err := state.Status(); err != nil {
				errorsFound <- err
			}
		}
	}()
	go func() {
		defer workers.Done()
		for index := 0; index < iterations; index++ {
			if err := WriteNoNewActions(path, "concurrency test"); err != nil {
				errorsFound <- err
			}
			if err := WriteDevnetActivation(
				path,
				fingerprint,
				time.Now().UTC().Add(-time.Second),
				time.Now().UTC().Add(time.Hour),
				1,
				"concurrency test",
			); err != nil {
				errorsFound <- err
			}
		}
	}()
	workers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
}

func TestStatusReportsEffectiveModeWithoutReason(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	fingerprint := "c23f7a4c8d169646c7582a8ce5ef4b97e20b1b5984ce09633b620842b5634694"
	state, err := NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return now }

	status, err := state.Status()
	if err != nil || status.Mode != ModeNoNewActions || !status.ExpiresAt.IsZero() {
		t.Fatalf("absent status = %+v, %v", status, err)
	}
	if _, err := os.Lstat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status read created a lock file: %v", err)
	}
	if err := WriteDevnetActivation(
		path,
		fingerprint,
		now.Add(-time.Second),
		now.Add(time.Hour),
		2,
		"private operator reason",
	); err != nil {
		t.Fatal(err)
	}
	status, err = state.Status()
	if err != nil || status.Mode != ModeDevnetEnabled ||
		!status.ExpiresAt.Equal(now.Add(time.Hour)) ||
		status.MaxActions != 2 || status.RemainingActions != 2 {
		t.Fatalf("enabled status = %+v, %v", status, err)
	}
	state.now = func() time.Time { return now.Add(2 * time.Hour) }
	status, err = state.Status()
	if err != nil || status.Mode != ModeNoNewActions || !status.ExpiresAt.IsZero() {
		t.Fatalf("expired status = %+v, %v", status, err)
	}
}

func TestTerminalStopIsDurableAndClearedByFreshActivation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	fingerprint := "c23f7a4c8d169646c7582a8ce5ef4b97e20b1b5984ce09633b620842b5634694"
	state, err := NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	actionID := testActionID
	if err := state.StopForTerminal(actionID, "halted"); err != nil {
		t.Fatal(err)
	}
	status, err := state.Status()
	if err != nil || status.Mode != ModeNoNewActions || status.TerminalActionID != actionID ||
		status.TerminalOutcome != "halted" {
		t.Fatalf("terminal status = %+v, %v", status, err)
	}
	if err := state.StopForTerminal(actionID, "complete"); err == nil {
		t.Fatal("invalid terminal outcome was accepted")
	}
	status, err = state.Status()
	if err != nil || status.TerminalOutcome != "halted" {
		t.Fatalf("invalid update changed terminal status = %+v, %v", status, err)
	}
	if err := WriteNoNewActions(path, "ordinary operator stop"); err != nil {
		t.Fatal(err)
	}
	status, err = state.Status()
	if err != nil || status.TerminalOutcome != "halted" {
		t.Fatalf("ordinary stop cleared terminal status = %+v, %v", status, err)
	}
	if _, err := state.AcknowledgeTerminal(testActionID, "failed", "wrong action"); err == nil {
		t.Fatal("mismatched terminal acknowledgement was accepted")
	}
	if _, err := state.AcknowledgeTerminal(actionID, "halted", "reviewed terminal action"); err == nil || err.Error() != "halted terminal actions cannot be cleared" {
		t.Fatalf("halted acknowledgement error = %v", err)
	}
	status, err = state.Status()
	if err != nil || status.TerminalActionID != actionID || status.TerminalOutcome != "halted" {
		t.Fatalf("halted terminal status changed = %+v, %v", status, err)
	}
	now := time.Now().UTC()
	if err := WriteDevnetActivation(
		path, fingerprint, now.Add(-time.Second), now.Add(time.Hour), 1, "must stay blocked",
	); err == nil {
		t.Fatal("halted setup was re-enabled")
	}
}

func TestTerminalStopCanReconcileSameActionWithoutOpeningAuthority(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	fingerprint := "c23f7a4c8d169646c7582a8ce5ef4b97e20b1b5984ce09633b620842b5634694"
	state, err := NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.StopForTerminal(testActionID, "halted"); err != nil {
		t.Fatal(err)
	}
	if err := state.StopForTerminal(testActionID, "failed"); err != nil {
		t.Fatal(err)
	}
	status, err := state.Status()
	if err != nil || status.Mode != ModeNoNewActions ||
		status.TerminalActionID != testActionID || status.TerminalOutcome != "failed" {
		t.Fatalf("updated terminal status = %+v, %v", status, err)
	}
	otherActionID := "3fc17e5d2bc3c229233bc38e11f02b41566753db05b1c9be93aececd816a3d6d"
	if err := state.StopForTerminal(otherActionID, "halted"); err == nil {
		t.Fatal("different terminal action replaced the active latch")
	}
	if err := state.ClearTerminalForFinalized(otherActionID); err == nil {
		t.Fatal("different finalized action cleared the active latch")
	}
	if err := state.ClearTerminalForFinalized(testActionID); err != nil {
		t.Fatal(err)
	}
	status, err = state.Status()
	if err != nil || status.Mode != ModeNoNewActions ||
		status.TerminalActionID != "" || status.TerminalOutcome != "" {
		t.Fatalf("cleared terminal status = %+v, %v", status, err)
	}
}

func TestFinalizedActionClearsRecoveryWithoutRestoringAuthority(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	fingerprint := "c23f7a4c8d169646c7582a8ce5ef4b97e20b1b5984ce09633b620842b5634694"
	now := time.Now().UTC()
	if err := WriteDevnetActivation(
		path, fingerprint, now.Add(-time.Second), now.Add(time.Hour), 2, "two actions",
	); err != nil {
		t.Fatal(err)
	}
	state, err := NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if blocked, err := state.WithSendBarrier(testActionID, func() error { return nil }); err != nil || blocked {
		t.Fatalf("send barrier: blocked=%v err=%v", blocked, err)
	}
	if err := state.ClearTerminalForFinalized(testActionID); err != nil {
		t.Fatal(err)
	}
	status, err := state.Status()
	if err != nil || status.Mode != ModeDevnetEnabled || status.RemainingActions != 1 ||
		status.RecoveryPending {
		t.Fatalf("finalized status = %+v, %v", status, err)
	}
	lastActionID := "3fc17e5d2bc3c229233bc38e11f02b41566753db05b1c9be93aececd816a3d6d"
	if blocked, err := state.WithSendBarrier(lastActionID, func() error { return nil }); err != nil || blocked {
		t.Fatalf("last send barrier: blocked=%v err=%v", blocked, err)
	}
	if err := state.ClearTerminalForFinalized(lastActionID); err != nil {
		t.Fatal(err)
	}
	status, err = state.Status()
	if err != nil || status.Mode != ModeNoNewActions || status.RecoveryPending {
		t.Fatalf("exhausted finalized status = %+v, %v", status, err)
	}
	document, err := state.readStateUnlocked()
	if err != nil || document == nil || document.RemainingActions != 0 ||
		document.RecoveryActionID != "" {
		t.Fatalf("finalized state = %+v, %v", document, err)
	}
}

func TestStateWritersDoNotEraseInvalidOrTerminalState(t *testing.T) {
	fingerprint := "c23f7a4c8d169646c7582a8ce5ef4b97e20b1b5984ce09633b620842b5634694"
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	futureIssued := time.Now().UTC().Add(time.Hour)
	futureState, err := json.Marshal(stateDocument{
		Version:            stateVersion,
		Mode:               ModeDevnetEnabled,
		ProfileFingerprint: fingerprint,
		IssuedAt:           futureIssued,
		ExpiresAt:          futureIssued.Add(time.Hour),
		MaxActions:         1,
		RemainingActions:   1,
		Reason:             "future activation",
	})
	if err != nil {
		t.Fatal(err)
	}
	invalidStates := map[string]string{
		"malformed JSON":              `{`,
		"unknown field":               `{"version":3,"mode":"no_new_actions","reason":"stop","unknown":true}`,
		"duplicate field":             `{"version":3,"mode":"no_new_actions","mode":"devnet_enabled","reason":"stop"}`,
		"invalid stopped latch":       `{"version":3,"mode":"no_new_actions","terminal_outcome":"halted","reason":"stop"}`,
		"invalid activation capacity": `{"version":3,"mode":"devnet_enabled","profile_sha256":"c23f7a4c8d169646c7582a8ce5ef4b97e20b1b5984ce09633b620842b5634694","issued_at":"2026-07-30T11:59:59Z","expires_at":"2026-07-30T13:00:00Z","max_actions":1,"remaining_actions":2,"reason":"invalid capacity"}`,
		"future activation":           string(futureState),
	}
	for name, content := range invalidStates {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "control.json")
			before := []byte(content)
			if err := os.WriteFile(path, before, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := WriteNoNewActions(path, "operator stop"); err == nil {
				t.Fatal("stop overwrote invalid state")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatal("stop changed invalid state")
			}
			if err := WriteDevnetActivation(
				path, fingerprint, now, now.Add(time.Hour), 1, "bounded test",
			); err == nil {
				t.Fatal("activation overwrote invalid state")
			}
			after, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatal("activation changed invalid state")
			}
		})
	}

	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	state, err := NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.StopForTerminal(testActionID, "failed"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteDevnetActivation(
		path, fingerprint, now, now.Add(time.Hour), 1, "must acknowledge first",
	); err == nil {
		t.Fatal("activation cleared an unacknowledged terminal latch")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("activation changed terminal state")
	}
}

func TestStopCanReplaceValidExpiredActivation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	fingerprint := "c23f7a4c8d169646c7582a8ce5ef4b97e20b1b5984ce09633b620842b5634694"
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	if err := WriteDevnetActivation(
		path, fingerprint, now.Add(-2*time.Hour), now.Add(-time.Hour), 1, "expired",
	); err != nil {
		t.Fatal(err)
	}
	if err := WriteNoNewActions(path, "operator stop"); err != nil {
		t.Fatal(err)
	}
	state, err := NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	status, err := state.Status()
	if err != nil || status.Mode != ModeNoNewActions {
		t.Fatalf("stopped status = %+v, %v", status, err)
	}
}

func TestConditionalActivationCannotOverwriteConcurrentTerminalStop(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	fingerprint := "c23f7a4c8d169646c7582a8ce5ef4b97e20b1b5984ce09633b620842b5634694"
	if err := WriteNoNewActions(path, "initial stop"); err != nil {
		t.Fatal(err)
	}
	state, err := NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := state.Revision()
	if err != nil {
		t.Fatal(err)
	}
	actionID := testActionID
	if err := state.StopForTerminal(actionID, "failed"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	written, err := WriteDevnetActivationIfRevision(
		path, fingerprint, revision, now, now.Add(time.Hour), 1, "stale enable",
	)
	if err != nil || written {
		t.Fatalf("stale activation written=%v err=%v", written, err)
	}
	status, err := state.Status()
	if err != nil || status.TerminalOutcome != "failed" {
		t.Fatalf("terminal stop was overwritten = %+v, %v", status, err)
	}
	revision, err = state.Revision()
	if err != nil {
		t.Fatal(err)
	}
	written, err = WriteDevnetActivationIfRevision(
		path, fingerprint, revision, now, now.Add(time.Hour), 1, "acknowledged enable",
	)
	if err == nil || written {
		t.Fatalf("terminal activation written=%v err=%v", written, err)
	}
	if _, err := state.AcknowledgeTerminal(actionID, "failed", "reviewed terminal action"); err != nil {
		t.Fatal(err)
	}
	revision, err = state.Revision()
	if err != nil {
		t.Fatal(err)
	}
	written, err = WriteDevnetActivationIfRevision(
		path, fingerprint, revision, now, now.Add(time.Hour), 1, "acknowledged enable",
	)
	if err != nil || !written {
		t.Fatalf("acknowledged activation written=%v err=%v", written, err)
	}
	status, err = state.Status()
	if err != nil || status.Mode != ModeDevnetEnabled || status.TerminalOutcome != "" {
		t.Fatalf("fresh activation = %+v, %v", status, err)
	}
}

func TestConditionalActivationCannotOverwriteExistingActivation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	fingerprint := "c23f7a4c8d169646c7582a8ce5ef4b97e20b1b5984ce09633b620842b5634694"
	now := time.Now().UTC()
	if err := WriteDevnetActivation(
		path, fingerprint, now, now.Add(time.Hour), 1, "first activation",
	); err != nil {
		t.Fatal(err)
	}
	state, err := NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := state.Revision()
	if err != nil {
		t.Fatal(err)
	}

	written, err := WriteDevnetActivationIfRevision(
		path, fingerprint, revision, now, now.Add(2*time.Hour), 1, "replacement",
	)
	if err == nil || written || !strings.Contains(err.Error(), "stop the current activation") {
		t.Fatalf("replacement written=%v err=%v", written, err)
	}
	status, err := state.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Mode != ModeDevnetEnabled || status.RemainingActions != 1 ||
		!status.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("existing activation changed: %+v", status)
	}
	blocked, err := state.WithSendBarrier(testActionID, func() error { return nil })
	if err != nil || blocked {
		t.Fatalf("send barrier blocked=%v err=%v", blocked, err)
	}
	revision, err = state.Revision()
	if err != nil {
		t.Fatal(err)
	}
	written, err = WriteDevnetActivationIfRevision(
		path, fingerprint, revision, now, now.Add(2*time.Hour), 1, "recovery replacement",
	)
	if err == nil || written || !strings.Contains(err.Error(), "stop the current activation") {
		t.Fatalf("recovery replacement written=%v err=%v", written, err)
	}
	document, err := readStateDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	if document == nil || document.RemainingActions != 0 ||
		document.RecoveryActionID != testActionID {
		t.Fatalf("recovery binding changed: %+v", document)
	}
}

func TestStateWritersArePrivateAtomicAndFailClosed(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	if err := WriteNoNewActions(path, "operator stop"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("control mode = %s", info.Mode())
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(before, []byte(`"mode":"no_new_actions"`)) {
		t.Fatalf("stopped control = %s", before)
	}

	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteNoNewActions(path, "second stop"); err != nil {
		t.Fatalf("stale unrelated temporary file blocked control update: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, after) || !bytes.Contains(after, []byte(`"reason":"second stop"`)) {
		t.Fatal("successful state replacement did not change the active control")
	}
	stale, err := os.ReadFile(tempPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(stale) != "stale" {
		t.Fatal("control writer changed an unrelated temporary file")
	}
	leftovers, err := filepath.Glob(filepath.Join(directory, ".control.json.*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("control writer left temporary files: %v", leftovers)
	}
	if err := os.Remove(tempPath); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := WriteNoNewActions(path, "must not follow"); err == nil {
		t.Fatal("control writer replaced a symlink target")
	}
	targetData, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(targetData) != "target" {
		t.Fatal("control writer changed the symlink target")
	}
}

func TestActivationWriterValidatesLifetimeAndReason(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	fingerprint := "c23f7a4c8d169646c7582a8ce5ef4b97e20b1b5984ce09633b620842b5634694"
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	if err := WriteDevnetActivation(
		path,
		fingerprint,
		now,
		now.Add(time.Hour),
		1,
		"bounded test",
	); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		expiresAt time.Time
		reason    string
	}{
		{name: "zero lifetime", expiresAt: now, reason: "test"},
		{name: "unbounded lifetime", expiresAt: now.Add(24*time.Hour + time.Second), reason: "test"},
		{name: "control character", expiresAt: now.Add(time.Hour), reason: "bad\nreason"},
		{name: "surrounding space", expiresAt: now.Add(time.Hour), reason: " bad"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := WriteDevnetActivation(
				path,
				fingerprint,
				now,
				test.expiresAt,
				1,
				test.reason,
			); err == nil {
				t.Fatal("invalid activation was written")
			}
		})
	}
	futureIssued := time.Now().UTC().Add(time.Hour)
	if err := WriteDevnetActivation(
		path,
		fingerprint,
		futureIssued,
		futureIssued.Add(time.Hour),
		1,
		"future activation",
	); err == nil {
		t.Fatal("future activation was written")
	}
	for _, limit := range []uint32{0, maxActivationActions + 1} {
		if err := WriteDevnetActivation(
			path,
			fingerprint,
			now,
			now.Add(time.Hour),
			limit,
			"bounded test",
		); err == nil {
			t.Fatalf("invalid action limit %d was accepted", limit)
		}
	}
}

func TestSendBarrierConsumesCapacityBeforeOperation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	fingerprint := "c23f7a4c8d169646c7582a8ce5ef4b97e20b1b5984ce09633b620842b5634694"
	now := time.Now().UTC()
	if err := WriteDevnetActivation(
		path,
		fingerprint,
		now.Add(-time.Second),
		now.Add(time.Hour),
		1,
		"one bounded action",
	); err != nil {
		t.Fatal(err)
	}
	state, err := NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	operationErr := errors.New("operation failed after admission")
	blocked, err := state.WithSendBarrier(testActionID, func() error { return operationErr })
	if blocked || !errors.Is(err, operationErr) {
		t.Fatalf("first barrier = %v, %v", blocked, err)
	}
	status, err := state.Status()
	if err != nil || status.Mode != ModeNoNewActions || !status.RecoveryPending {
		t.Fatalf("consumed activation status = %+v, %v", status, err)
	}
	if err := ValidateStatus(status); err != nil {
		t.Fatalf("consumed activation projection = %v", err)
	}
	called := false
	blocked, err = state.WithSendBarrier(testActionID, func() error {
		called = true
		return nil
	})
	if err != nil || !blocked || called {
		t.Fatalf("exhausted barrier = %v, %v, called=%v", blocked, err, called)
	}
}

func TestRecoverySendBarrierRequiresLiveBoundedOriginalAuthority(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	fingerprint := "c23f7a4c8d169646c7582a8ce5ef4b97e20b1b5984ce09633b620842b5634694"
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	if err := WriteDevnetActivation(
		path, fingerprint, now.Add(-time.Second), now.Add(time.Hour), 1, "one action",
	); err != nil {
		t.Fatal(err)
	}
	state, err := NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	state.now = func() time.Time { return now }

	called := false
	blocked, err := state.WithRecoverySendBarrier(testActionID, func() error {
		called = true
		return nil
	})
	if err != nil || !blocked || called {
		t.Fatalf("unbound recovery = blocked %v, error %v, called %v", blocked, err, called)
	}
	blocked, err = state.WithSendBarrier(testActionID, func() error { return nil })
	if err != nil || blocked {
		t.Fatalf("initial send barrier = %v, %v", blocked, err)
	}
	state.requireFresh = true
	state.startedAt = now.Add(time.Minute)
	document, err := state.readStateUnlocked()
	if err != nil || document == nil || document.RemainingActions != 0 ||
		document.RecoveryActionID != testActionID {
		t.Fatalf("consumed activation = %+v, %v", document, err)
	}
	called = false
	blocked, err = state.WithRecoverySendBarrier(testActionID, func() error {
		called = true
		return nil
	})
	if err != nil || blocked || !called {
		t.Fatalf("bound recovery = blocked %v, error %v, called %v", blocked, err, called)
	}
	document, err = state.readStateUnlocked()
	if err != nil || document.RemainingActions != 0 {
		t.Fatalf("recovery consumed capacity = %+v, %v", document, err)
	}
	otherAction := "3fc17e5d2bc3c229233bc38e11f02b41566753db05b1c9be93aececd816a3d6d"
	blocked, err = state.WithRecoverySendBarrier(otherAction, func() error {
		t.Fatal("wrong action entered recovery barrier")
		return nil
	})
	if err != nil || !blocked {
		t.Fatalf("wrong recovery action = %v, %v", blocked, err)
	}
	state.now = func() time.Time { return now.Add(2 * time.Hour) }
	blocked, err = state.WithRecoverySendBarrier(testActionID, func() error {
		t.Fatal("expired activation entered recovery barrier")
		return nil
	})
	if err != nil || !blocked {
		t.Fatalf("expired recovery = %v, %v", blocked, err)
	}
	if err := WriteNoNewActions(path, "operator stop"); err != nil {
		t.Fatal(err)
	}
	state.now = func() time.Time { return now }
	blocked, err = state.WithRecoverySendBarrier(testActionID, func() error {
		t.Fatal("stopped state entered recovery barrier")
		return nil
	})
	if err != nil || !blocked {
		t.Fatalf("stopped recovery = %v, %v", blocked, err)
	}
}

func TestRecoverySendBarrierDoesNotConsumeRemainingCapacity(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	fingerprint := "c23f7a4c8d169646c7582a8ce5ef4b97e20b1b5984ce09633b620842b5634694"
	now := time.Now().UTC()
	if err := WriteDevnetActivation(
		path, fingerprint, now.Add(-time.Second), now.Add(time.Hour), 2, "two actions",
	); err != nil {
		t.Fatal(err)
	}
	state, err := NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if blocked, err := state.WithSendBarrier(testActionID, func() error { return nil }); err != nil || blocked {
		t.Fatalf("initial barrier = %v, %v", blocked, err)
	}
	if blocked, err := state.WithRecoverySendBarrier(testActionID, func() error { return nil }); err != nil || blocked {
		t.Fatalf("recovery barrier = %v, %v", blocked, err)
	}
	document, err := state.readStateUnlocked()
	if err != nil || document == nil || document.RemainingActions != 1 {
		t.Fatalf("remaining activation = %+v, %v", document, err)
	}
}

func TestStopWaitsForSendBarrier(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	fingerprint := "c23f7a4c8d169646c7582a8ce5ef4b97e20b1b5984ce09633b620842b5634694"
	now := time.Now().UTC()
	if err := WriteDevnetActivation(
		path,
		fingerprint,
		now.Add(-time.Second),
		now.Add(time.Hour),
		1,
		"bounded test",
	); err != nil {
		t.Fatal(err)
	}
	state, err := NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	barrierDone := make(chan error, 1)
	go func() {
		blocked, err := state.WithSendBarrier(testActionID, func() error {
			close(entered)
			<-release
			return nil
		})
		if err == nil && blocked {
			err = errors.New("enabled state blocked the send barrier")
		}
		barrierDone <- err
	}()
	<-entered
	stopDone := make(chan error, 1)
	go func() {
		stopDone <- WriteNoNewActions(path, "operator stop")
	}()
	select {
	case err := <-stopDone:
		t.Fatalf("stop returned before the send barrier ended: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-barrierDone; err != nil {
		t.Fatal(err)
	}
	status, err := state.Status()
	if err != nil || status.Mode != ModeNoNewActions {
		t.Fatalf("consumed activation status = %+v, %v", status, err)
	}
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
	blocked, err := state.WithSendBarrier(testActionID, func() error {
		t.Fatal("stopped state entered the send barrier")
		return nil
	})
	if err != nil || !blocked {
		t.Fatalf("stopped barrier = %v, %v", blocked, err)
	}
	info, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("control lock mode = %s", info.Mode())
	}
}

// A multi-action activation is the autonomy dial: it must permit exactly the
// number of sends it granted and then stop, without a human intervening to
// stop it and without any path to a further send.
func TestMultiActionActivationSpendsExactlyItsGrant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.json")
	fingerprint := strings.Repeat("b", 64)
	state, err := NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	const granted = 3
	if err := WriteDevnetActivation(
		path, fingerprint,
		time.Now().UTC().Add(-time.Second), time.Now().UTC().Add(time.Hour),
		granted, "multi-action test",
	); err != nil {
		t.Fatal(err)
	}

	for index := range granted {
		actionID := strings.Repeat(string(rune('a'+index)), 64)
		performed := false
		blocked, err := state.WithSendBarrier(actionID, func() error {
			performed = true
			return nil
		})
		if err != nil {
			t.Fatalf("send %d: %v", index+1, err)
		}
		if blocked || !performed {
			t.Fatalf("send %d was blocked while the activation still had capacity", index+1)
		}
		status, err := state.Status()
		if err != nil {
			t.Fatal(err)
		}
		if want := uint32(granted - index - 1); status.RemainingActions != want {
			t.Fatalf("after send %d: remaining = %d, want %d",
				index+1, status.RemainingActions, want)
		}
	}

	// The grant is spent. Nothing further may send, and it must fail closed
	// rather than by erroring — an exhausted activation is a normal end state.
	performed := false
	blocked, err := state.WithSendBarrier(strings.Repeat("f", 64), func() error {
		performed = true
		return nil
	})
	if err != nil {
		t.Fatalf("exhausted activation: %v", err)
	}
	if !blocked || performed {
		t.Fatal("an exhausted activation must block every further send")
	}
}

// Capacity is consumed before the durable send marker, so an interrupted send
// loses a slot rather than leaving one that could be spent twice.
func TestInterruptedSendConsumesCapacityRatherThanReusingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.json")
	fingerprint := strings.Repeat("c", 64)
	state, err := NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteDevnetActivation(
		path, fingerprint,
		time.Now().UTC().Add(-time.Second), time.Now().UTC().Add(time.Hour),
		2, "interrupted send test",
	); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("send failed after the barrier")
	if _, err := state.WithSendBarrier(strings.Repeat("d", 64), func() error {
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("the operation's error must surface: %v", err)
	}
	status, err := state.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.RemainingActions != 1 {
		t.Fatalf("a failed send must still consume its slot: remaining = %d",
			status.RemainingActions)
	}
}

// An activation is the operator's stated bound. Replacing a live one would
// silently restore its spent capacity, so every writer must be refused —
// including the plain WriteDevnetActivation the sweep path uses, not just the
// revision-checked variant the swap path uses.
func TestNoWriterMayReplaceALiveActivation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.json")
	fingerprint := strings.Repeat("e", 64)
	state, err := NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	issued := time.Now().UTC().Add(-time.Second)
	expires := time.Now().UTC().Add(time.Hour)
	if err := WriteDevnetActivation(path, fingerprint, issued, expires, 3, "first grant"); err != nil {
		t.Fatal(err)
	}
	// Spend one slot so a silent replacement would visibly restore capacity.
	if _, err := state.WithSendBarrier(strings.Repeat("a", 64), func() error { return nil }); err != nil {
		t.Fatal(err)
	}

	if err := WriteDevnetActivation(path, fingerprint, issued, expires, 100, "widen"); err == nil ||
		!strings.Contains(err.Error(), "stop the current activation") {
		t.Fatalf("replacing a live activation must be refused, got %v", err)
	}
	status, err := state.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.MaxActions != 3 || status.RemainingActions != 2 {
		t.Fatalf("the original grant must be intact: max=%d remaining=%d",
			status.MaxActions, status.RemainingActions)
	}

	// After an explicit stop, a fresh grant is allowed — the bound is a
	// deliberate gate, not a permanent lock.
	if err := WriteNoNewActions(path, "operator stop"); err != nil {
		t.Fatal(err)
	}
	if err := WriteDevnetActivation(path, fingerprint, issued, expires, 5, "second grant"); err != nil {
		t.Fatalf("a fresh grant after stopping must succeed: %v", err)
	}
}
