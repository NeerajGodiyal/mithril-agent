package main

import (
	"strings"
	"testing"
)

func TestExplainStageCoversTheRealGateTokens(t *testing.T) {
	// The exact token the live gate produced when evidence was unreadable.
	got := explainStage("mithril_health_status_unknown_divergence_artifacts")
	for _, want := range []string{"unknown", "divergence", "unreadable"} {
		if !strings.Contains(got, want) {
			t.Errorf("explanation %q does not mention %q", got, want)
		}
	}

	if got := explainStage("mithril_health_status_critical"); !strings.Contains(got, "critical") {
		t.Errorf("bare health state not explained: %q", got)
	}
	if got := explainStage("clock"); !strings.Contains(got, "time") {
		t.Errorf("clock stage not explained: %q", got)
	}
	if got := explainStage(""); got != "" {
		t.Errorf("empty stage produced %q", got)
	}
	for _, stage := range []string{
		"catalog", "info", "genesis", "state_call", "state_tool", "state_identity", "diagnosis", "account",
	} {
		got := explainStage("mithril_observation_" + stage)
		if strings.Contains(got, "See the operator guide") {
			t.Errorf("MCP %s stage fell back to the generic explanation: %q", stage, got)
		}
	}
}

// An unrecognised stage must not be described as if it were understood, and
// must not suggest relaxing the gate.
func TestExplainStageFallsBackHonestly(t *testing.T) {
	got := explainStage("some_future_stage_we_have_not_seen")
	if strings.Contains(got, "some_future_stage") {
		t.Errorf("fallback echoed the raw token: %q", got)
	}
	if !strings.Contains(got, "do not retry blindly") {
		t.Errorf("fallback omits the safety instruction: %q", got)
	}
}

// Presentation must never imply the gate can be worked around.
func TestExplanationsNeverSuggestBypassingTheGate(t *testing.T) {
	all := []string{}
	for _, v := range stageMeaning {
		all = append(all, v)
	}
	for _, v := range healthIssueMeaning {
		all = append(all, v)
	}
	for _, text := range all {
		lower := strings.ToLower(text)
		for _, banned := range []string{"ignore", "override", "disable the check", "force"} {
			if strings.Contains(lower, banned) {
				t.Errorf("explanation invites bypassing the gate: %q", text)
			}
		}
	}
}

func TestWalletExplanationUsesTheDedicatedWalletModel(t *testing.T) {
	got := strings.ToLower(explainStage("wallet_balance"))
	if !strings.Contains(got, "dedicated") || strings.Contains(got, "disposable") {
		t.Errorf("wallet explanation contradicts the dedicated wallet model: %q", got)
	}
}
