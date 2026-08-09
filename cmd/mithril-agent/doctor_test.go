package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/readiness"
	"github.com/Overclock-Validator/mithril-agent/telegramoperator"
)

// Every blocker must arrive with the command that fixes it, or a reviewer is
// stuck with a red line and no next step.
func TestDoctorAlwaysGivesTheOperatorANextStep(t *testing.T) {
	report := buildDoctorReport(t.Context(), "")
	if err := report.Validate(); err != nil {
		t.Fatalf("doctor built a misleading report: %v", err)
	}
	blocking := report.Blocking()
	if len(blocking) == 0 {
		t.Fatal("doctor with no configuration reported nothing to fix")
	}
	for _, check := range blocking {
		if !strings.Contains(check.Action, "mithril-agent") {
			t.Errorf("blocker %q does not name a command to run: %q", check.Title, check.Action)
		}
	}
}

// A missing or unreadable input must never render as ready.
func TestDoctorNeverReportsReadyWithoutEvidence(t *testing.T) {
	if report := buildDoctorReport(t.Context(), ""); report.CanAct() {
		t.Fatal("doctor permitted acting with no configuration at all")
	}
	if report := buildDoctorReport(t.Context(), "/nonexistent/path/config.json"); report.CanAct() {
		t.Fatal("doctor permitted acting with an unreadable configuration")
	}
}

// The JSON surface is a contract for MCP and automation; the field names and
// the state vocabulary must stay stable.
func TestDoctorJSONSurfaceIsStable(t *testing.T) {
	// No --config, so isolate the home directory: the shape of the report must
	// not depend on whether the machine running the tests has a real setup.
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := runDoctor(t.Context(), []string{"--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Overall string `json:"overall"`
		Checks  []struct {
			Name   string `json:"name"`
			Title  string `json:"title"`
			State  string `json:"state"`
			Detail string `json:"detail"`
			Action string `json:"action"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("doctor --json is not valid JSON: %v", err)
	}
	if decoded.Overall == "" || len(decoded.Checks) == 0 {
		t.Fatalf("doctor --json is empty: %s", out.String())
	}
	valid := map[string]bool{
		string(readiness.Ready): true, string(readiness.Blocked): true,
		string(readiness.Waiting): true, string(readiness.Unknown): true,
		string(readiness.Skipped): true,
	}
	if !valid[decoded.Overall] {
		t.Errorf("unknown overall state %q", decoded.Overall)
	}
	for _, check := range decoded.Checks {
		if !valid[check.State] {
			t.Errorf("check %q has unknown state %q", check.Name, check.State)
		}
		if check.Name == "" || check.Title == "" {
			t.Errorf("check is missing identity: %+v", check)
		}
	}
}

// doctor is diagnostic only: it must not be able to start or authorise
// anything, and must not require a positional argument that could be misread
// as a target.
func TestDoctorRejectsPositionalArguments(t *testing.T) {
	if err := runDoctor(t.Context(), []string{"enable"}, &bytes.Buffer{}); err == nil {
		t.Fatal("doctor accepted a positional argument")
	}
}

// An agent with no control state configured has nothing to report about
// trading. That is "does not apply", not "stopped" — and an optional check
// that demands an action would block a demonstration that never needed one.
func TestDoctorSkipsTradingWithNoControlState(t *testing.T) {
	var cfg config
	check := doctorTradingCheck(cfg, "/tmp/x.json", false)
	if check.State != readiness.Skipped {
		t.Fatalf("unconfigured control state = %q, want skipped", check.State)
	}
	if check.Action != "" {
		t.Errorf("an unconfigured optional check demanded an action: %q", check.Action)
	}
}

// Telegram is optional, so its absence must not block a demonstration.
func TestDoctorTelegramIsOptionalAndNeverReadsTheToken(t *testing.T) {
	t.Setenv(telegramoperator.BotTokenEnvironment, "")
	t.Setenv(telegramoperator.AllowedIDsEnvironment, "")
	check := doctorTelegramCheck()
	if check.State != readiness.Skipped {
		t.Fatalf("absent Telegram = %q, want skipped (optional)", check.State)
	}

	const secret = "123456:SUPER-SECRET-BOT-TOKEN"
	t.Setenv(telegramoperator.BotTokenEnvironment, secret)
	t.Setenv(telegramoperator.AllowedIDsEnvironment, "111")
	check = doctorTelegramCheck()
	if check.State != readiness.Ready {
		t.Fatalf("configured Telegram = %q, want ready", check.State)
	}
	// The token must never reach the report, which is printed and serialised.
	if strings.Contains(check.Detail+check.Action, secret) {
		t.Fatal("the doctor report leaked the Telegram bot token")
	}
	if !strings.Contains(check.Detail, "read-only") {
		t.Errorf("Telegram detail does not state it is read-only: %q", check.Detail)
	}
}

// An unfunded account is a real blocker and must carry the funding action.
func TestDoctorFundingBlocksWithAFundingActionWhenEmpty(t *testing.T) {
	check := doctorFundingCheck(t.Context(), "")
	if check.State != readiness.Skipped {
		t.Fatalf("no account configured = %q, want skipped", check.State)
	}
	// An unreadable account must not be reported as funded.
	check = doctorFundingCheck(t.Context(), "/nonexistent/agent.json")
	if check.State == readiness.Ready {
		t.Fatal("an unreadable account was reported as funded")
	}
}

// A missing account is a blocker with a concrete command, not a bare error.
func TestDoctorAccountCheckNamesTheCommandThatFixesIt(t *testing.T) {
	check := doctorAccountCheck(t.Context(), "")
	if check.State != readiness.Blocked {
		t.Fatalf("missing account = %q, want blocked", check.State)
	}
	if !strings.Contains(check.Action, "wallet new") {
		t.Errorf("action does not name the command: %q", check.Action)
	}
	check = doctorAccountCheck(t.Context(), "relative/path.json")
	if check.State != readiness.Blocked {
		t.Errorf("relative keypair path = %q, want blocked", check.State)
	}
}

// Every blocker must point at the guided command, not the scripted expert one.
// Telling somebody who just ran `setup` to run `swap setup` reads as if what
// they did was wrong, and it is the exact moment a non-technical reviewer gives
// up.
func TestDoctorSendsPeopleToTheGuidedCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	report := buildDoctorReport(t.Context(), "")
	blocking := report.Blocking()
	if len(blocking) == 0 {
		t.Fatal("doctor with no configuration reported nothing to fix")
	}
	for _, check := range blocking {
		if strings.Contains(check.Action, "swap setup") {
			t.Errorf("blocker %q sends the reviewer to the expert command: %q",
				check.Title, check.Action)
		}
		if !strings.Contains(check.Action, "mithril-agent setup") {
			t.Errorf("blocker %q does not name the guided command: %q",
				check.Title, check.Action)
		}
	}
}

// "Ready" next to a balance smaller than the floor on the same screen is the
// most dangerous shape this report can take: the operator goes away, the sweep
// can never fire, the trades start failing for insufficient balance, and every
// check says the account is fine.
func TestFundingIsNotReadyBelowTheSweepFloor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sweep := t.TempDir()
	writeSweepFloorConfig(t, sweep, 166_600_000)
	if err := recordStrategy(strategyPaths{sweep: filepath.Join(sweep, "config.json")}); err != nil {
		t.Fatal(err)
	}
	floor, ok := configuredSweepFloor()
	if !ok || floor != 166_600_000 {
		t.Fatalf("floor = %d, %v; want the recorded sweep reserve", floor, ok)
	}
}

// Most deployments have no sweep. Inventing a floor for them would block every
// one of them, so the absence has to stay silent.
func TestNoSweepMeansNoFloorToCompareAgainst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, ok := configuredSweepFloor(); ok {
		t.Error("a floor was reported with no sweep configured")
	}
}

func writeSweepFloorConfig(t *testing.T, dir string, reserve uint64) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"profile": map[string]any{"reserve_lamports": reserve},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
