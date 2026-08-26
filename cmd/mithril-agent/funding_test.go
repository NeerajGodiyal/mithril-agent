package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/readiness"
	"github.com/Overclock-Validator/mithril-agent/squads"
)

// An account that cannot be read must never be reported as a sound boundary.
// "I could not check" and "it is fine" are the two answers that must never be
// confused here.
func TestUnreadableLimitIsNeverReportedAsSound(t *testing.T) {
	report := fundingReport(squads.SpendingLimit{}, squads.Expectation{},
		errors.New("unreadable"))
	if report.CanAct() {
		t.Fatal("an unreadable spending limit was reported as ready")
	}
	if len(report.Blocking()) == 0 {
		t.Fatal("an unreadable spending limit produced no blocker")
	}
	for _, check := range report.Blocking() {
		if !strings.Contains(check.Action, "mithril-agent") {
			t.Errorf("blocker %q gives no command to run: %q", check.Title, check.Action)
		}
	}
}

// Every finding must reach the operator with a title they can act on, not a
// bare field name.
func TestEveryFindingBecomesAnActionableCheck(t *testing.T) {
	vaultIndex := uint8(0)
	limit := squads.SpendingLimit{
		Multisig: "vault-a", Mint: squads.NativeMint, Amount: 9_000_000_000,
		Period: squads.Monthly, Members: []string{"member"},
	}
	expect := squads.Expectation{
		Multisig: "vault-b", VaultIndex: &vaultIndex,
		Destination: "agent", Mint: squads.NativeMint,
		MaxAmount: 1_000_000, AllowedPeriods: []squads.Period{squads.Daily},
	}
	report := fundingReport(limit, expect, nil)
	if report.CanAct() {
		t.Fatal("a boundary with several problems was reported as ready")
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("the report is internally inconsistent: %v", err)
	}
	for _, check := range report.Checks {
		if check.Title == "" || check.Title == check.Name {
			t.Errorf("check %q has no human title", check.Name)
		}
		if check.State == readiness.Blocked && check.Action == "" {
			t.Errorf("blocker %q has no action", check.Name)
		}
	}
}

// A sound boundary must report the cap, so the operator sees the worst case
// rather than just a green line.
func TestASoundBoundaryReportsItsCap(t *testing.T) {
	vaultIndex := uint8(0)
	limit := squads.SpendingLimit{
		Multisig: "vault", Mint: squads.NativeMint, Amount: 500_000_000,
		Period: squads.Daily, Members: []string{"member"},
		Destinations: []string{"agent"},
	}
	expect := squads.Expectation{
		Multisig: "vault", VaultIndex: &vaultIndex,
		Destination: "agent", Mint: squads.NativeMint,
		MaxAmount: 1_000_000_000, AllowedPeriods: []squads.Period{squads.Daily},
	}
	report := fundingReport(limit, expect, nil)
	if !report.CanAct() {
		t.Fatal("a sound boundary was reported as not ready")
	}
	if !strings.Contains(report.Checks[0].Detail, "500000000") {
		t.Errorf("the cap is not shown: %q", report.Checks[0].Detail)
	}
}

// The command surface must reject anything it does not understand rather than
// guessing, and must never offer a way to move funds.
func TestFundingCommandOffersNoWayToMoveFunds(t *testing.T) {
	var out bytes.Buffer
	if err := runFunding(t.Context(), []string{"help"}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "Read-only") ||
		!strings.Contains(text, "moves nothing") {
		t.Error("the usage does not state it is read-only and moves nothing")
	}
	// "spending limit" is the Squads noun and is expected to appear; what must
	// not appear is a subcommand that acts. Only `check` may exist.
	for _, verb := range []string{"use", "transfer", "send", "create", "fund "} {
		if strings.Contains(strings.ToLower(text), "funding "+verb) {
			t.Errorf("the usage advertises a funding %q command", verb)
		}
		if err := runFunding(t.Context(), []string{verb}, &bytes.Buffer{}); err == nil {
			t.Errorf("funding accepted the subcommand %q", verb)
		}
	}
	if err := runFundingCheck(t.Context(), []string{"now"}, &bytes.Buffer{}); err == nil {
		t.Fatal("funding check accepted a positional argument")
	}
}

// The JSON surface is a contract for automation and must stay stable.
func TestFundingJSONSurfaceIsStable(t *testing.T) {
	// A well-formed but non-existent address: the report must still be valid
	// JSON with a blocked verdict, rather than the command erroring out.
	var out bytes.Buffer
	if err := runFundingCheck(t.Context(), []string{
		"--json", "--spending-limit", "11111111111111111111111111111112",
	}, &out); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Overall string `json:"overall"`
		Checks  []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("funding check --json is not valid JSON: %v", err)
	}
	if decoded.Overall != string(readiness.Blocked) || len(decoded.Checks) == 0 {
		t.Fatalf("a check with no arguments should be blocked: %s", out.String())
	}
}

// Period names are the operator's vocabulary; an unknown one must be an error
// rather than silently narrowing what is accepted.
func TestUnknownPeriodNameIsRejected(t *testing.T) {
	if _, err := parsePeriods("daily, one-time"); err != nil {
		t.Errorf("a valid list was rejected: %v", err)
	}
	for _, bad := range []string{"", "hourly", "daily,hourly", ","} {
		if _, err := parsePeriods(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// A missing flag is a usage error. Reporting it as "could not be read" with the
// action "check the network" sends somebody to debug their connection over a
// flag they never typed.
func TestMissingSpendingLimitIsAUsageErrorNotANetworkOne(t *testing.T) {
	err := runFundingCheck(t.Context(), []string{
		"--multisig", "A", "--destination", "B", "--max-lamports", "1",
	}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("funding check ran with no --spending-limit")
	}
	if !strings.Contains(err.Error(), "--spending-limit") {
		t.Errorf("the error does not name the missing flag: %v", err)
	}
	for _, misleading := range []string{"network", "could not be read"} {
		if strings.Contains(strings.ToLower(err.Error()), misleading) {
			t.Errorf("a missing flag was reported as %q: %v", misleading, err)
		}
	}
}

// When a read genuinely fails, the reason has to reach the operator. These
// reasons name only public addresses and program IDs, so there is nothing to
// leak by being specific.
func TestAFailedReadReportsWhyNotJustThatItFailed(t *testing.T) {
	report := fundingReport(squads.SpendingLimit{}, squads.Expectation{},
		errors.New("that account is not owned by the Squads program"))
	if report.CanAct() {
		t.Fatal("a failed read was reported as ready")
	}
	if !strings.Contains(report.Checks[0].Detail, "not owned by the Squads program") {
		t.Errorf("the real reason was discarded: %q", report.Checks[0].Detail)
	}
	if strings.Contains(report.Checks[0].Action, "network") {
		t.Errorf("a wrong-account error blames the network: %q", report.Checks[0].Action)
	}
}
