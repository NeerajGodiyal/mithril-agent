package control

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Refusing to overwrite a LIVE activation is the point: widening a bound an
// operator set must require stopping first. But the guard tested only the mode,
// and a grant whose clock has run out is not live — it authorises nothing. An
// operator whose grant simply expired without ever sending was told to stop
// something that had already stopped itself.
func TestExpiredGrantThatSentNothingCanBeReEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.json")
	fingerprint := strings.Repeat("a", 64)
	now := time.Now().UTC()

	// A grant that has already run out of clock, with nothing in flight.
	if err := WriteDevnetActivation(
		path, fingerprint, now.Add(-2*time.Hour), now.Add(-time.Hour), 2, "first",
	); err != nil {
		t.Fatal(err)
	}
	if err := WriteDevnetActivation(
		path, fingerprint, now.Add(-time.Minute), now.Add(time.Hour), 2, "second",
	); err != nil {
		t.Fatalf("an expired grant blocked a fresh one: %v", err)
	}
}

// A live grant must still refuse, or the bound is not a bound.
func TestLiveGrantStillRefusesReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.json")
	fingerprint := strings.Repeat("a", 64)
	now := time.Now().UTC()

	if err := WriteDevnetActivation(
		path, fingerprint, now.Add(-time.Minute), now.Add(time.Hour), 2, "first",
	); err != nil {
		t.Fatal(err)
	}
	if err := WriteDevnetActivation(
		path, fingerprint, now.Add(-time.Minute), now.Add(2*time.Hour), 5, "widen",
	); err == nil {
		t.Fatal("a live grant was silently widened")
	}
}

// The two activation writers used to carry the same condition written out
// twice, and they drifted the moment one was fixed: writeState learned that an
// expired grant is replaceable while WriteDevnetActivationIfRevision — the path
// `swap enable` actually takes — kept refusing it. Both now call one predicate,
// so this covers both.
func TestBothActivationWritersAgreeOnReplaceability(t *testing.T) {
	now := time.Now().UTC()
	for name, test := range map[string]struct {
		document stateDocument
		blocks   bool
	}{
		"live grant": {stateDocument{
			Mode: ModeDevnetEnabled, ExpiresAt: now.Add(time.Hour), RemainingActions: 2,
		}, true},
		"clock expired, actions left": {stateDocument{
			Mode: ModeDevnetEnabled, ExpiresAt: now.Add(-time.Hour), RemainingActions: 2,
		}, false},
		// Exhausted stays blocked even once expired: enabling over it would
		// erase a RecoveryActionID for a send that may yet land.
		"exhausted": {stateDocument{
			Mode: ModeDevnetEnabled, ExpiresAt: now.Add(-time.Hour), RemainingActions: 0,
		}, true},
		// The case exhaustion does not cover: an unresolved send on a grant
		// that still had actions left when its clock ran out. Blocking only on
		// RemainingActions == 0 let this be overwritten, erasing the recovery
		// ID for a transaction that may still land.
		"expired, actions left, send unresolved": {stateDocument{
			Mode:             ModeDevnetEnabled,
			ExpiresAt:        now.Add(-time.Hour),
			RemainingActions: 2,
			RecoveryActionID: strings.Repeat("b", 64),
		}, true},
		"stopped": {stateDocument{Mode: ModeNoNewActions}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := blocksReplacement(test.document, now); got != test.blocks {
				t.Fatalf("blocksReplacement = %v, want %v", got, test.blocks)
			}
		})
	}
}
