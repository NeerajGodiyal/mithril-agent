package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/swaprun"
)

// The runner reports only bounded reasons, so a sentinel that stops surviving
// the wrap chain would silently regress the operator's only diagnostic.
func TestFloorErrorClassifiedThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("build: %w", fmt.Errorf("route: %w", orcaswap.ErrQuoteBelowFloor))
	if got := cycleFailureReason(wrapped, false); got != "price_below_floor" {
		t.Errorf("wrapped sentinel = %q, want price_below_floor", got)
	}
	if got := cycleFailureReason(fmt.Errorf("unrelated"), false); got != "operation_failed" {
		t.Errorf("unrelated error = %q, want operation_failed", got)
	}
	// A timeout is the more urgent fact and must not be masked by the price
	// classification when both could apply.
	if got := cycleFailureReason(orcaswap.ErrQuoteBelowFloor, true); got != "operation_timeout" {
		t.Errorf("timed-out cycle = %q, want operation_timeout", got)
	}
}

// A freshly configured sweep spends its first 24-48h before its anchor. That is
// what a CORRECT setup does, and reporting it as operation_failed every cycle
// gave the operator a runner that looked broken and no way to tell otherwise.
func TestWaitingForTheFirstSweepWindowIsNotAFailure(t *testing.T) {
	if got := cycleFailureReason(agent.ErrBeforeScheduleAnchor, false); got != "before_schedule_anchor" {
		t.Fatalf("pre-anchor reason = %q, want before_schedule_anchor", got)
	}
	// Wrapped the way the engine returns it.
	wrapped := fmt.Errorf("sweep cycle: %w", agent.ErrBeforeScheduleAnchor)
	if got := cycleFailureReason(wrapped, false); got != "before_schedule_anchor" {
		t.Fatalf("wrapped pre-anchor reason = %q", got)
	}
	// A timeout still wins: an agent that is both early AND hung is hung.
	if got := cycleFailureReason(agent.ErrBeforeScheduleAnchor, true); got != "operation_timeout" {
		t.Fatalf("timeout lost to the anchor reason: %q", got)
	}
}

// A transaction built but not submitted before its blockhash aged out is a
// LATENCY fault: the next cycle rebuilds. Flattened into operation_failed it
// read as a broken agent — observed live, where the journal said "blockhash
// expired" while the operator saw only a generic failure.
func TestBlockhashExpiryIsNamedNotGeneric(t *testing.T) {
	if got := cycleFailureReason(swaprun.ErrBlockhashExpired, false); got != "blockhash_expired" {
		t.Fatalf("reason = %q, want blockhash_expired", got)
	}
	wrapped := fmt.Errorf("submit swap: %w", swaprun.ErrBlockhashExpired)
	if got := cycleFailureReason(wrapped, false); got != "blockhash_expired" {
		t.Fatalf("wrapped reason = %q", got)
	}
	// An unrelated failure must still be generic, or the label means nothing.
	if got := cycleFailureReason(errors.New("something else"), false); got != "operation_failed" {
		t.Fatalf("unrelated reason = %q", got)
	}
}
