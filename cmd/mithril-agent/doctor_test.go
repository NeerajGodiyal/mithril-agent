package main

import (
	"bytes"
	"encoding/json"
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

// Stopped is the safe default and must read as waiting, not as a fault. An
// operator who sees "BLOCKED" for a correctly-stopped agent will try to "fix"
// something that is working as designed.
func TestDoctorTreatsStoppedTradingAsWaitingNotBlocked(t *testing.T) {
	var cfg config
	check := doctorTradingCheck(cfg)
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
