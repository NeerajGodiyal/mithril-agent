package telegramoperator

import (
	"strings"
	"testing"
)

// Every state the engine can report must have operator language. An unmapped
// state would surface a bare internal token to a non-specialist.
func TestEveryEngineDecisionHasOperatorWording(t *testing.T) {
	// These are the decisions swaprun can put in a status snapshot.
	for _, decision := range []string{
		"waiting", "ready", "complete", "stopped", "canceled",
		"degraded", "failed", "halted",
	} {
		text := describeDecision(decision)
		if text == decision {
			t.Errorf("decision %q has no operator wording", decision)
		}
		if strings.ContainsAny(text, "_") {
			t.Errorf("wording for %q still looks like an identifier: %q", decision, text)
		}
	}
	for _, verdict := range []string{
		"pending", "finalized", "failed", "unresolved", "diverged",
	} {
		if describeVerdict(verdict) == verdict {
			t.Errorf("verdict %q has no operator wording", verdict)
		}
	}
}

// An unknown state must still be visible rather than silently blank, because
// hiding a state we did not anticipate is worse than showing a raw token.
func TestUnknownStatesRemainVisible(t *testing.T) {
	if got := describeDecision("some_future_state"); got != "some_future_state" {
		t.Errorf("unknown decision rendered as %q, want the raw token", got)
	}
	if got := describeDecision(""); got != "unknown" {
		t.Errorf("empty decision rendered as %q, want \"unknown\"", got)
	}
	if got := describeVerdict("some_future_verdict"); got != "some_future_verdict" {
		t.Errorf("unknown verdict rendered as %q, want the raw token", got)
	}
}

// The states an operator must act on have to read as needing action, and the
// safe ones must not. This is the whole point of the mapping.
func TestActionableStatesReadAsActionable(t *testing.T) {
	for _, decision := range []string{"failed", "halted"} {
		if !strings.Contains(strings.ToLower(describeDecision(decision)), "review") {
			t.Errorf("%q does not tell the operator it needs review: %q", decision, describeDecision(decision))
		}
	}
	if strings.Contains(strings.ToLower(describeDecision("complete")), "review") {
		t.Error("a completed action should not read as needing review")
	}
	// An unresolved send is the one an operator must never blindly retry.
	if !strings.Contains(strings.ToLower(describeVerdict("unresolved")), "do not retry") {
		t.Errorf("unresolved outcome omits the do-not-retry instruction: %q", describeVerdict("unresolved"))
	}
}
