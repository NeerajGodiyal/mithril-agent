package readiness

import "testing"

// The package's own rule is that unreadable evidence is never good news:
// "Unknown ... is never treated as ready". A Check whose State was simply never
// set is even less evidence than Unknown, yet the zero value fell through the
// derivation switch and left the report Ready — so a check added with a
// forgotten field silently permits an action.
func TestUnsetStateDoesNotReportReady(t *testing.T) {
	report := NewReport([]Check{
		{Name: "node", Title: "Mithril node", State: Ready},
		{Name: "forgotten", Title: "Someone added a check"}, // State never set
	})
	if report.CanAct() {
		t.Fatalf("a check with no state permitted an action; overall = %q", report.Overall)
	}
	if report.Overall != Unknown {
		t.Errorf("overall = %q, want %q", report.Overall, Unknown)
	}
}

// An unrecognised state is the same problem arriving by a different route —
// a typo, or a value from a newer writer this binary does not know.
func TestUnrecognisedStateDoesNotReportReady(t *testing.T) {
	report := NewReport([]Check{
		{Name: "typo", Title: "Typed state", State: State("redy")},
	})
	if report.CanAct() {
		t.Fatalf("an unrecognised state permitted an action; overall = %q", report.Overall)
	}
}

// Skipped genuinely does not apply to the configuration, so it must keep
// passing. Closing the zero-value hole must not close this one.
func TestSkippedStillPermitsAction(t *testing.T) {
	report := NewReport([]Check{
		{Name: "node", Title: "Mithril node", State: Ready},
		{Name: "telegram", Title: "Telegram", State: Skipped},
	})
	if !report.CanAct() {
		t.Fatalf("a skipped check blocked an action; overall = %q", report.Overall)
	}
}

// Validate is the boundary check, and it should name the fault rather than let
// a malformed report travel further.
func TestValidateRejectsAnUnsetState(t *testing.T) {
	err := Report{Checks: []Check{{Name: "x", Title: "X"}}}.Validate()
	if err == nil {
		t.Fatal("Validate accepted a check with no state")
	}
}
