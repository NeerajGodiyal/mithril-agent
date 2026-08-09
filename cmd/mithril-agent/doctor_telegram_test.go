package main

import (
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/readiness"
	"github.com/Overclock-Validator/mithril-agent/telegramoperator"
)

// The Telegram check reads the ENVIRONMENT, not the config, so gating it behind
// a readable config hid it during exactly the state an operator is in while
// wiring up alerts — and "telegram is not working" is the complaint it exists
// to answer.
func TestDoctorReportsTelegramBeforeAConfigExists(t *testing.T) {
	t.Setenv(telegramoperator.BotTokenEnvironment, "")
	t.Setenv(telegramoperator.AllowedIDsEnvironment, "")
	report := buildDoctorReport(t.Context(), "")
	var found bool
	for _, check := range report.Checks {
		if check.Name == "telegram" {
			found = true
			// It must not claim the bot is broken: it only saw this shell.
			if !strings.Contains(check.Detail, "this shell") {
				t.Errorf("detail overclaims what was checked: %q", check.Detail)
			}
			// The next action lives in Detail: readiness rejects a non-blocking
			// check that carries an Action, and render never prints one.
			if !strings.Contains(check.Detail, "mithril-agent-telegram test") {
				t.Errorf("the check offers no way to find out: %q", check.Detail)
			}
		}
	}
	if !found {
		t.Fatal("doctor omitted Telegram when no config was supplied")
	}
}

// Variables being set is not a working alert channel: nothing has been sent.
// Reporting Ready without saying so is what made the wizard's claim misleading
// in the first place.
func TestDoctorDoesNotCallConfiguredTelegramProven(t *testing.T) {
	t.Setenv(telegramoperator.BotTokenEnvironment, "123:token")
	t.Setenv(telegramoperator.AllowedIDsEnvironment, "42")
	check := doctorTelegramCheck()
	if check.State != readiness.Ready {
		t.Fatalf("state = %v, want Ready when both are set", check.State)
	}
	if !strings.Contains(check.Detail, "starts nothing") {
		t.Errorf("detail implies alerts are running: %q", check.Detail)
	}
	if !strings.Contains(check.Detail, "mithril-agent-telegram test") {
		t.Errorf("detail does not point at the command that proves delivery: %q", check.Detail)
	}
}

// A check readiness itself rejects makes the whole report invalid, and the only
// machine that hits it is an operator's — the one that followed the wizard and
// exported both variables. CI never exports them, so this was green everywhere
// except where it mattered.
func TestDoctorTelegramCheckIsAValidReportInBothStates(t *testing.T) {
	for name, set := range map[string]bool{"configured": true, "unset": false} {
		t.Run(name, func(t *testing.T) {
			if set {
				t.Setenv(telegramoperator.BotTokenEnvironment, "123:token")
				t.Setenv(telegramoperator.AllowedIDsEnvironment, "42")
			} else {
				t.Setenv(telegramoperator.BotTokenEnvironment, "")
				t.Setenv(telegramoperator.AllowedIDsEnvironment, "")
			}
			report := readiness.NewReport([]readiness.Check{doctorTelegramCheck()})
			if err := report.Validate(); err != nil {
				t.Fatalf("doctor built a report readiness rejects: %v", err)
			}
		})
	}
}
