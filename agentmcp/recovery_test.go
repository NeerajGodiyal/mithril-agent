package agentmcp

import (
	"strings"
	"testing"
)

// The guidance exists so an assistant reading status suggests the correct
// action instead of improvising. The dangerous state must carry its own
// warning, since that is the one where a plausible-sounding suggestion does
// real damage.
func TestRecoveryGuidanceWarnsAgainstRetryingAnUnknownOutcome(t *testing.T) {
	steps := StandardRecoveryGuidance()
	if len(steps) == 0 {
		t.Fatal("no recovery guidance")
	}
	var unresolved string
	for _, step := range steps {
		if strings.Contains(strings.ToLower(step.State), "unknown") ||
			strings.Contains(strings.ToLower(step.State), "unresolved") {
			unresolved = step.Action
		}
	}
	if unresolved == "" {
		t.Fatal("no guidance for an unknown outcome, which is the state that most needs it")
	}
	lower := strings.ToLower(unresolved)
	if !strings.Contains(lower, "not retry") && !strings.Contains(lower, "don't retry") {
		t.Fatalf("unknown-outcome guidance does not forbid retrying: %q", unresolved)
	}
}

// No step may describe something this surface could perform, or an assistant
// could read it as permission.
func TestRecoveryGuidanceNeverImpliesThisSurfaceCanAct(t *testing.T) {
	for _, step := range StandardRecoveryGuidance() {
		lower := strings.ToLower(step.Action)
		for _, forbidden := range []string{
			"i will", "automatically", "the agent will retry", "sign ", "submit the",
		} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("guidance for %q implies action: %q", step.State, step.Action)
			}
		}
		if step.State == "" || step.Action == "" {
			t.Errorf("incomplete recovery step: %+v", step)
		}
	}
}

// Guidance must not tell an operator to weaken a gate to make progress.
func TestRecoveryGuidanceNeverSuggestsRelaxingASafetyLimit(t *testing.T) {
	for _, step := range StandardRecoveryGuidance() {
		lower := strings.ToLower(step.Action)
		for _, forbidden := range []string{"increase the limit", "disable the check", "lower the threshold", "skip the"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("guidance for %q suggests weakening a gate: %q", step.State, step.Action)
			}
		}
	}
}
