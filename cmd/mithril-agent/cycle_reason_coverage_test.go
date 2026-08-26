package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/internal/runmetrics"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/signerclient"
	"github.com/Overclock-Validator/mithril-agent/swapbuilder"
	"github.com/Overclock-Validator/mithril-agent/swaprun"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

// A category cycleFailureReason returns but runmetrics does not register is
// exported as "unknown" — so naming a new cause improves the log line and
// silently degrades the dashboard at the same time. Three categories were in
// exactly that state: before_schedule_anchor, which a correctly configured
// sweep reports for its whole first day, blockhash_expired, and signer_refused.
//
// Every error below is a real return path, so this fails if a category is
// added in one list and forgotten in the other.
//
// It is a table of error -> EXPECTED category, not error -> "is in some list".
// Membership alone constrained almost nothing: "operation_failed" is itself a
// registered category, so a classifier that named nothing at all still passed
// every assertion. Deleting any branch in cycleFailureReason left this test
// green. Naming the expected category is what makes it a real constraint.
func TestFailureCategoriesCoverEveryCycleReason(t *testing.T) {
	expected := map[string]struct {
		err      error
		category string
	}{
		"timeout":        {context.DeadlineExceeded, "operation_timeout"},
		"quote":          {swapbuilder.ErrQuoteTemporarilyUnavailable, "quote_unavailable"},
		"floor":          {orcaswap.ErrQuoteBelowFloor, "price_below_floor"},
		"anchor":         {agent.ErrBeforeScheduleAnchor, "before_schedule_anchor"},
		"blockhash":      {swaprun.ErrBlockhashExpired, "blockhash_expired"},
		"signer refusal": {signerclient.ErrSignerRefused, "signer_refused"},
		"node down":      {txflow.ErrNodeUnavailable, "node_unavailable"},
		"Mithril observer": {
			execution.ErrObservationUnavailable,
			"observation_not_ready",
		},
		"anything else": {errors.New("anything else"), "operation_failed"},
	}
	for name, want := range expected {
		t.Run(name, func(t *testing.T) {
			if got := cycleFailureReason(want.err, false); got != want.category {
				t.Errorf("cycleFailureReason = %q, want %q", got, want.category)
			}
			// And the category has to be one runmetrics actually exports, or the
			// log line improves while the dashboard silently degrades to unknown.
			if !slices.Contains(runmetrics.FailureCategories(), want.category) {
				t.Errorf("%q is not a registered category; runmetrics exports it as unknown",
					want.category)
			}
		})
	}
	// Wrapping must not lose the classification: the signer client returns its
	// sentinel wrapped with the signer's own sentence.
	wrapped := fmt.Errorf("%w: daily cap reached", signerclient.ErrSignerRefused)
	if got := cycleFailureReason(wrapped, false); got != "signer_refused" {
		t.Errorf("wrapped refusal = %q, want signer_refused", got)
	}
	// The runner also sets this one directly when the control state cannot be
	// read, without going through cycleFailureReason.
	if !slices.Contains(runmetrics.FailureCategories(), "control_state_unavailable") {
		t.Error("control_state_unavailable is not a registered category")
	}
}

// A refusal must be distinguishable from a crash. Collapsing them is what made
// a spent daily cap look like a broken binary for seven hours.
func TestSignerRefusalIsNotReportedAsAGenericFailure(t *testing.T) {
	refusal := signerclient.ErrSignerRefused
	if got := cycleFailureReason(refusal, false); got != "signer_refused" {
		t.Fatalf("bare refusal = %q, want signer_refused", got)
	}
	// Wrapped with the signer's own reason, which is how the client returns it.
	wrapped := errors.Join(signerclient.ErrSignerRefused,
		errors.New("signer daily input-token cap would be exceeded"))
	if got := cycleFailureReason(wrapped, false); got != "signer_refused" {
		t.Fatalf("wrapped refusal = %q, want signer_refused", got)
	}
	// A timeout still wins: a refusal that never returned is a timeout.
	if got := cycleFailureReason(refusal, true); got != "operation_timeout" {
		t.Fatalf("timed-out refusal = %q, want operation_timeout", got)
	}
}

// The pre-trade observation gate returns typed, stage-labelled errors. Two of
// its call sites render them as a readable "degraded" result; the ones inside
// validateBeforeSend returned them raw, so the SAME condition — wallet below
// the reserve, stale evidence, the node's cross-check behind — read as a named
// reason at the start of an action and as operation_failed moments later.
//
// The error here comes from the real exported ValidateObservation, not a
// hand-built lookalike, so the classification is pinned to the actual type.
func TestObservationFailuresAreNamedNotGeneric(t *testing.T) {
	profile := testSwapProfile(strategyTestOwner(t))
	// An observation from the wrong cluster is the cheapest way to make the real
	// validator produce a real stage-labelled error.
	err := swaprun.ValidateObservation(profile, agent.NodeObservation{}, time.Now().UTC())
	if err == nil {
		t.Fatal("ValidateObservation accepted an empty observation")
	}
	if swaprun.ObservationFailure(err) == "" {
		t.Fatalf("the validator did not produce a stage-labelled error: %v", err)
	}
	if got := cycleFailureReason(err, false); got != "observation_not_ready" {
		t.Fatalf("observation failure = %q, want observation_not_ready", got)
	}
	if !slices.Contains(runmetrics.FailureCategories(), "observation_not_ready") {
		t.Error("observation_not_ready is not a registered category")
	}
	// The stage token itself must never become the category: it is composed from
	// live status and issue names, so as a metric label it would collapse to
	// "unknown" — strictly worse than the operation_failed it replaces.
	if slices.Contains(runmetrics.FailureCategories(), swaprun.ObservationFailure(err)) {
		t.Errorf("the open-ended stage token %q was registered as a category",
			swaprun.ObservationFailure(err))
	}
}
