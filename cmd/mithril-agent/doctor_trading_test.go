package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/readiness"
)

// stoppedTradingConfig builds a config whose control state exists and is in the
// stopped default, which is what a freshly set-up agent looks like.
func stoppedTradingConfig(t *testing.T) config {
	t.Helper()
	dir := t.TempDir()
	profile := testSwapProfile("3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh")
	cfg := config{Swap: &profile}
	cfg.Control.StatePath = filepath.Join(dir, "control.json")
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	// NewStateFile writes the stopped default when the file does not exist,
	// which is the state every agent starts in.
	if _, err := control.NewStateFile(cfg.Control.StatePath, fingerprint, false); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// Stopped is the correct default and it is also permanent. Reporting it as
// "waiting" claimed a condition was on its way to being met, so an operator
// whose agent had simply never been armed was told there was nothing to do —
// by the one command whose job is to say what to do.
func TestDoctorPointsAStoppedPilotAtTheOneActionDemo(t *testing.T) {
	check := doctorTradingCheck(stoppedTradingConfig(t), "/tmp/x.json", false)
	if check.State == readiness.Waiting {
		t.Fatalf("a stopped agent was reported as waiting for something: %+v", check)
	}
	if check.State != readiness.Blocked {
		t.Fatalf("state = %q, want blocked: %+v", check.State, check)
	}
	if !strings.Contains(check.Action, "mithril-agent demo") {
		t.Errorf("action does not name the bounded demonstration: %q", check.Action)
	}
	if strings.Contains(check.Action, "swap enable") || strings.Contains(check.Action, "max-actions") {
		t.Errorf("doctor told a demonstration user to arm the runner directly: %q", check.Action)
	}
	// The detail must keep saying stopped is SAFE. Naming the next step is not
	// a licence to make a correct default look like a fault.
	if !strings.Contains(check.Detail, "safe default") {
		t.Errorf("detail no longer says the stopped default is safe: %q", check.Detail)
	}
}

// A strategy deployment arms all three legs together. Repeating the demand
// here pointed at `swap enable`, which arms ONE leg by hand and leaves the
// other two dark — worse than saying nothing.
func TestTradingCheckDefersToTheStrategy(t *testing.T) {
	check := doctorTradingCheck(stoppedTradingConfig(t), "/tmp/x.json", true)
	if check.State == readiness.Blocked {
		t.Fatalf("the trading check duplicated the strategy's arming demand: %+v", check)
	}
	if strings.Contains(check.Action, "swap enable") {
		t.Errorf("advised arming a single leg on a strategy deployment: %q", check.Action)
	}
}

// Whatever it decides, the check has to satisfy the readiness contract, or the
// whole report is rejected at render time and doctor fails instead of advising.
func TestTradingCheckSatisfiesTheReadinessContract(t *testing.T) {
	for name, cfg := range map[string]config{
		"stopped":      stoppedTradingConfig(t),
		"unconfigured": {},
	} {
		t.Run(name, func(t *testing.T) {
			check := doctorTradingCheck(cfg, "/tmp/x.json", false)
			report := readiness.Report{Checks: []readiness.Check{check}}
			if err := report.Validate(); err != nil {
				t.Fatalf("check %+v violates the readiness contract: %v", check, err)
			}
		})
	}
}

// An action line exists to be pasted. doctor has already resolved the config to
// reach this check, so printing a literal "--config PATH" hands the operator a
// command that fails — which is exactly what a clean-run rehearsal hit on the
// very first command a new operator types.
func TestTheArmingActionNamesTheRealConfigPath(t *testing.T) {
	const path = "/var/lib/mithril-agent/agent/config.json"
	check := doctorTradingCheck(stoppedTradingConfig(t), path, false)
	if !strings.Contains(check.Action, path) {
		t.Errorf("the action does not name the resolved config: %q", check.Action)
	}
	if strings.Contains(check.Action, "--config PATH") {
		t.Errorf("the action still hands over a placeholder that fails when pasted: %q", check.Action)
	}
}
