package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/readiness"
)

func TestFreshStartRoutesToTheAllInOneSetup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := runStart(t.Context(), nil, &out); err != nil {
		t.Fatal(err)
	}
	screen := out.String()
	if !strings.Contains(screen, "mithril-agent setup strategy") {
		t.Fatalf("fresh start did not name the all-in-one setup:\n%s", screen)
	}
	if strings.Contains(screen, "Next:  Run: mithril-agent setup,") {
		t.Fatalf("fresh start still names the legacy setup:\n%s", screen)
	}
}

// The root help lists about twenty commands across six sections. This one
// answers "what do I do next" and must answer it with exactly one thing —
// a list of five problems is a list somebody picks the easiest item from.
func TestStartNamesExactlyOneNextStep(t *testing.T) {
	report := readiness.NewReport([]readiness.Check{
		{Name: "clock", Title: "Trusted clock", State: readiness.Ready, Detail: "in sync"},
		{
			Name: "configuration", Title: "Configuration", State: readiness.Blocked,
			Detail: "no config supplied", Action: "Run: mithril-agent setup",
		},
		{
			Name: "strategy", Title: "Strategy legs", State: readiness.Blocked,
			Detail: "armed but no runner", Action: "start the runner: mithril-agent strategy run",
		},
	})
	var out bytes.Buffer
	if err := writeNextStep(&out, report); err != nil {
		t.Fatal(err)
	}
	screen := out.String()
	if count := strings.Count(screen, "Next:"); count != 1 {
		t.Fatalf("printed %d next steps, want exactly 1:\n%s", count, screen)
	}
	// The FIRST blocked check wins: nothing downstream can be fixed while
	// something upstream is broken, so config comes before the runner.
	if !strings.Contains(screen, "mithril-agent setup") {
		t.Errorf("did not name the first blocker:\n%s", screen)
	}
	if strings.Contains(screen, "strategy run") {
		t.Errorf("named a downstream step while an upstream one is blocked:\n%s", screen)
	}
}

// Waiting is not a problem to solve. Presenting it as one is how somebody
// "fixes" a healthy system that was simply mid-catch-up.
func TestStartSaysThereIsNothingToDoWhileWaiting(t *testing.T) {
	report := readiness.NewReport([]readiness.Check{
		{Name: "clock", Title: "Trusted clock", State: readiness.Ready, Detail: "in sync"},
		{
			Name: "strategy", Title: "Strategy legs", State: readiness.Waiting,
			Detail: "3 leg(s) configured, none armed",
		},
	})
	var out bytes.Buffer
	if err := writeNextStep(&out, report); err != nil {
		t.Fatal(err)
	}
	screen := out.String()
	if !strings.Contains(screen, "Nothing to do") {
		t.Errorf("a waiting state did not say so plainly:\n%s", screen)
	}
	if strings.Contains(screen, "doctor") {
		t.Errorf("a waiting state sent the operator to debug something:\n%s", screen)
	}
}

// Evidence that could not be read must never render as ready. An operator who
// reads "everything is ready" over an unreadable check will arm a wallet on it.
func TestStartNeverReportsReadyOnUnknownEvidence(t *testing.T) {
	report := readiness.NewReport([]readiness.Check{
		{Name: "clock", Title: "Trusted clock", State: readiness.Ready, Detail: "in sync"},
		{Name: "trading", Title: "Trading", State: readiness.Unknown, Detail: "cannot reach the node"},
	})
	var out bytes.Buffer
	if err := writeNextStep(&out, report); err != nil {
		t.Fatal(err)
	}
	screen := out.String()
	if strings.Contains(screen, "Everything is ready") {
		t.Fatalf("unknown evidence was reported as ready:\n%s", screen)
	}
	if !strings.Contains(screen, "could not be checked") {
		t.Errorf("did not say the evidence was unreadable:\n%s", screen)
	}
}

// When everything really is ready, the one remaining step is the one this
// command will not take: granting spending authority. It must say so, and say
// why it is not doing it.
func TestStartLeavesArmingToTheOperator(t *testing.T) {
	report := readiness.NewReport([]readiness.Check{
		{Name: "clock", Title: "Trusted clock", State: readiness.Ready, Detail: "in sync"},
		{Name: "strategy", Title: "Strategy legs", State: readiness.Ready, Detail: "3 armed"},
	})
	var out bytes.Buffer
	if err := writeNextStep(&out, report); err != nil {
		t.Fatal(err)
	}
	screen := out.String()
	if !strings.Contains(screen, "Everything is ready") {
		t.Fatalf("a ready system did not say so:\n%s", screen)
	}
	if !strings.Contains(screen, "strategy enable") {
		t.Errorf("did not name the arming command:\n%s", screen)
	}
	// Naming the command is not enough; it has to say why it is the operator's
	// to type, or this reads as an oversight rather than a boundary.
	if !strings.Contains(screen, "spending authority") {
		t.Errorf("did not explain why arming is left to the operator:\n%s", screen)
	}
}

// Whatever the state, the output has to be short enough to actually read. The
// failure mode this command exists to fix is a wall of options nobody parses.
func TestStartOutputStaysShortInEveryState(t *testing.T) {
	for name, checks := range map[string][]readiness.Check{
		"blocked": {{
			Name: "configuration", Title: "Configuration", State: readiness.Blocked,
			Detail: "no config supplied", Action: "Run: mithril-agent setup",
		}},
		"waiting": {{
			Name: "strategy", Title: "Strategy legs", State: readiness.Waiting,
			Detail: "none armed",
		}},
		"unknown": {{
			Name: "trading", Title: "Trading", State: readiness.Unknown, Detail: "no node",
		}},
		"ready": {{
			Name: "strategy", Title: "Strategy legs", State: readiness.Ready, Detail: "3 armed",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := writeNextStep(&out, readiness.NewReport(checks)); err != nil {
				t.Fatal(err)
			}
			if lines := strings.Count(out.String(), "\n"); lines > 10 {
				t.Errorf("%d lines is too much to read at a glance:\n%s", lines, out.String())
			}
		})
	}
}
