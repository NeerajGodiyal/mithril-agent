package readiness

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func check(name string, state State, action string) Check {
	return Check{Name: name, Title: strings.ToUpper(name[:1]) + name[1:], State: state, Detail: "detail", Action: action}
}

// Unreadable evidence must never be reported as ready or as merely waiting.
// "Waiting" reads to an operator as "nothing to do", which is the wrong
// response to evidence we could not read.
func TestUnknownNeverDegradesToReadyOrWaiting(t *testing.T) {
	report := NewReport([]Check{
		check("node", Ready, ""),
		check("price", Unknown, "check the data source"),
		check("rule", Waiting, ""),
	})
	if report.Overall != Unknown {
		t.Fatalf("overall = %q, want unknown", report.Overall)
	}
	if report.CanAct() {
		t.Fatal("a report with unreadable evidence permitted acting")
	}
	if !strings.Contains(report.Summary(), "Price") {
		t.Fatalf("summary does not name the unreadable check: %q", report.Summary())
	}
}

// Blocked outranks everything, including unknown.
func TestBlockedAlwaysWins(t *testing.T) {
	report := NewReport([]Check{
		check("node", Unknown, "restart the node"),
		check("wallet", Blocked, "fund the account"),
		check("rule", Waiting, ""),
	})
	if report.Overall != Blocked {
		t.Fatalf("overall = %q, want blocked", report.Overall)
	}
	if report.CanAct() {
		t.Fatal("a blocked report permitted acting")
	}
}

// Waiting is not readiness: a condition that has not been met is a reason not
// to act, even though nothing is wrong.
func TestWaitingIsNotReadiness(t *testing.T) {
	report := NewReport([]Check{check("node", Ready, ""), check("rule", Waiting, "")})
	if report.Overall != Waiting {
		t.Fatalf("overall = %q, want waiting", report.Overall)
	}
	if report.CanAct() {
		t.Fatal("waiting was treated as ready")
	}
	if len(report.Blocking()) != 0 {
		t.Fatal("waiting was reported as something the operator must fix")
	}
}

func TestAllReadyPermitsActing(t *testing.T) {
	report := NewReport([]Check{check("node", Ready, ""), check("wallet", Ready, "")})
	if !report.CanAct() {
		t.Fatal("an all-ready report did not permit acting")
	}
	if report.Overall != Ready {
		t.Fatalf("overall = %q, want ready", report.Overall)
	}
}

// A blocked check with no action is a dead end for whoever reads it.
func TestValidateRejectsMisleadingChecks(t *testing.T) {
	if err := (Report{Checks: []Check{{Name: "n", Title: "T", State: Blocked}}}).Validate(); !errors.Is(err, errBlockedWithoutAction) {
		t.Fatalf("blocked-without-action error = %v", err)
	}
	if err := (Report{Checks: []Check{{Name: "n", Title: "T", State: Ready, Action: "do something"}}}).Validate(); !errors.Is(err, errReadyWithAction) {
		t.Fatalf("ready-with-action error = %v", err)
	}
	if err := (Report{Checks: []Check{{State: Ready}}}).Validate(); !errors.Is(err, errMissingIdentity) {
		t.Fatalf("missing-identity error = %v", err)
	}
	valid := NewReport([]Check{check("node", Blocked, "start the node")})
	if err := valid.Validate(); err != nil {
		t.Fatalf("a well-formed report was rejected: %v", err)
	}
}

// The rendered form must lead with what to do, not bury it under green lines.
func TestRenderShowsTheActionForEveryBlocker(t *testing.T) {
	report := NewReport([]Check{
		check("node", Ready, ""),
		check("wallet", Blocked, "fund the agent account at faucet.solana.com"),
	})
	var out bytes.Buffer
	if err := report.Render(&out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "BLOCKED") {
		t.Errorf("render does not mark the blocker: %q", text)
	}
	if !strings.Contains(text, "faucet.solana.com") {
		t.Errorf("render omits the action: %q", text)
	}
	if !strings.Contains(text, "in order") {
		t.Errorf("render does not tell the operator to work through them: %q", text)
	}
}

func TestRenderOnAReadySystemSaysSoPlainly(t *testing.T) {
	var out bytes.Buffer
	if err := NewReport([]Check{check("node", Ready, "")}).Render(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "in place") {
		t.Fatalf("ready render = %q", out.String())
	}
}
