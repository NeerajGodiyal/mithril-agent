package main

import (
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
)

// A quarantined transaction was signed and never broadcast, so it cannot have
// an ambiguous on-chain outcome — the engine resolves it to "canceled" as soon
// as the blockhash expires (execution/engine.go:453-473).
//
// The runner latched the control state the moment it saw "halted", one cycle
// before that resolution, and AcknowledgeTerminal refuses "halted" outright
// with no acknowledge command on the devnet path at all. The result: pressing
// stop between signing and submission — reason operator_stop_before_submission
// — permanently bricked the setup.
func TestUnsentHaltIsNotTerminal(t *testing.T) {
	quarantined := operatorstatus.Result{
		ActionID: "a", Decision: "halted",
		Reason: "operator_stop_before_submission", Submitted: false,
	}
	if terminalForControl(quarantined) {
		t.Error("a signed-but-unsent transaction latched the control state permanently")
	}
}

// A halt after the transaction was broadcast is the genuinely uncertain case —
// unresolved or diverged reconciliation. That must still latch.
func TestSubmittedHaltStaysTerminal(t *testing.T) {
	reconciled := operatorstatus.Result{
		ActionID: "a", Decision: "halted", Verdict: "unresolved", Submitted: true,
	}
	if !terminalForControl(reconciled) {
		t.Error("an ambiguous outcome for a broadcast transaction must latch")
	}
	// A verdict can only exist for a broadcast transaction — it is set solely
	// when state.reconciliation is non-nil — so it must latch on its own, even
	// where a caller did not set the flag.
	verdictOnly := operatorstatus.Result{
		ActionID: "a", Decision: "halted", Verdict: "diverged",
	}
	if !terminalForControl(verdictOnly) {
		t.Error("a reconciliation verdict is evidence of submission and must latch")
	}
}

// Not latching must not mean not telling anyone.
//
// RequiresAttention does treat "halted" as needing attention regardless of
// Submitted, but that alone does NOT keep Prometheus covered, and believing it
// did was wrong: MithrilAgentAttentionRequired carries `for: 5m`, while an
// unsent halt self-resolves to "canceled" as soon as the blockhash expires
// (~60-150s), and canceled-with-Submitted=false clears attention. The umbrella
// alert therefore never fires for this case. Coverage comes from the dedicated
// MithrilAgentActionHalted rule instead.
//
// This still pins the in-process half — if RequiresAttention ever becomes
// conditional on Submitted, /status and the Telegram surfaces go quiet too.
func TestUnsentHaltStillRaisesAttention(t *testing.T) {
	quarantined := operatorstatus.Result{
		ActionID: "a", Decision: "halted",
		Reason: "operator_stop_before_submission", Submitted: false,
	}
	healthy := control.Status{Mode: control.ModeNoNewActions}
	if control.ValidateStatus(healthy) != nil {
		t.Skip("control status fixture is not valid; attention would be true vacuously")
	}
	if !operatorstatus.RequiresAttention(
		execution.Result{Decision: quarantined.Decision},
		healthy,
		time.Now().UTC(),
	) {
		t.Error("an unsent halt neither latches nor raises attention: it is silent")
	}
}

func TestFailedStaysTerminalAndEmptyActionNeverLatches(t *testing.T) {
	failed := operatorstatus.Result{ActionID: "a", Decision: "failed", Submitted: true}
	if !terminalForControl(failed) {
		t.Error("a failed action must latch")
	}
	// No action ID means there is nothing to latch against.
	if terminalForControl(operatorstatus.Result{Decision: "halted", Submitted: true}) {
		t.Error("latched without an action ID")
	}
	if terminalForControl(operatorstatus.Result{ActionID: "a", Decision: "waiting"}) {
		t.Error("a waiting cycle latched the control state")
	}
}
