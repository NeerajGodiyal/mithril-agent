package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/internal/clockcheck"
	"github.com/Overclock-Validator/mithril-agent/internal/runmetrics"
)

// On 2026-08-07 an armed strategy reported "operation_failed" on all three
// legs, every ten seconds, for five minutes. The cause was the HOST clock: an
// NTP poll interval of 8m32s let the kernel's uncertainty bound drift to 705ms
// against a 500ms policy. The clock itself was accurate to 501 microseconds.
//
// Nothing in the runner's output said "clock". Only running preflight by hand
// found it. A category an operator cannot act on is worse than no category,
// because it reads as broken trading code.
func TestAClockFailureIsNamedNotSweptIntoOperationFailed(t *testing.T) {
	// The shape the host actually produced: the specific sentence, wrapped.
	err := fmt.Errorf("%w: kernel clock uncertainty exceeds policy", clockcheck.ErrClockUnusable)

	reason := cycleFailureReason(err, false)
	if reason == "operation_failed" {
		t.Fatal("a host clock failure still reads as operation_failed; " +
			"this is the exact five-minute diagnosis that motivated the category")
	}
	if reason != "clock_unusable" {
		t.Fatalf("reason = %q, want clock_unusable", reason)
	}

	// Wrapping must not destroy the specific cause: the operator needs to know
	// WHICH clock property failed, not merely that one did.
	if !strings.Contains(err.Error(), "uncertainty exceeds policy") {
		t.Errorf("the specific cause was lost in wrapping: %v", err)
	}
	if !errors.Is(err, clockcheck.ErrClockUnusable) {
		t.Error("the sentinel does not match its own wrapped error")
	}
}

// A category the metrics allowlist does not know is exported as "unknown" —
// which is less useful than the operation_failed it replaced. The dashboard
// must be MOST informative exactly when a new cause was worth naming.
func TestTheClockCategoryIsExportableNotUnknown(t *testing.T) {
	found := false
	for _, category := range runmetrics.FailureCategories() {
		if category == "clock_unusable" {
			found = true
		}
	}
	if !found {
		t.Fatal("clock_unusable is not in the metrics allowlist, so it exports as \"unknown\"")
	}
}

// Every clock refusal must carry the sentinel, not just the uncertainty one.
// An operator whose clock is unsynchronised entirely deserves the same answer
// as one whose bound merely drifted.
func TestEveryClockRefusalCarriesTheSentinel(t *testing.T) {
	for _, cause := range []string{
		"kernel clock is not synchronized with normal leap state",
		"kernel clock offset exceeds policy",
		"kernel clock uncertainty exceeds policy",
	} {
		err := fmt.Errorf("%w: %s", clockcheck.ErrClockUnusable, cause)
		if got := cycleFailureReason(err, false); got != "clock_unusable" {
			t.Errorf("%q classified as %q, want clock_unusable", cause, got)
		}
	}
}
