package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/readiness"
)

// armedLegWithStatus writes a strategy leg, arms it, and stamps its operator
// status file to a chosen age. Age is the runner's heartbeat: the file is
// rewritten every cycle, so how old it is says whether anything is executing.
func armedLegWithStatus(t *testing.T, dir string, buy bool, statusAge time.Duration) string {
	t.Helper()
	path := triggeredLeg(t, dir, buy, 0)
	cfg, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := cfg.Swap.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := control.WriteDevnetActivation(
		cfg.Control.StatePath, fingerprint, now, now.Add(time.Hour), 4, "armed for the test",
	); err != nil {
		t.Fatal(err)
	}
	// The control file is isolated one directory below the stable state. Keep the
	// runner-owned journal in the stable parent, matching setup.
	cfg.Journal.Path = filepath.Join(filepath.Dir(filepath.Dir(cfg.Control.StatePath)), "events.jsonl")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := now.Add(-statusAge)
	writeStrategyStatus(t, cfg, stamp, control.Status{
		Mode: control.ModeDevnetEnabled, ExpiresAt: now.Add(time.Hour),
		MaxActions: 4, RemainingActions: 4,
	})
	return path
}

func writeFreshStatus(t *testing.T, configPath string) {
	t.Helper()
	cfg, err := readConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Journal.Path == "" {
		cfg.Journal.Path = filepath.Join(filepath.Dir(configPath), "events.jsonl")
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeStrategyStatus(t, cfg, time.Now().UTC(), control.Status{Mode: control.ModeNoNewActions})
}

func writeStrategyStatus(t *testing.T, cfg config, observedAt time.Time, status control.Status) {
	t.Helper()
	profile, version, cluster := configStatusIdentity(cfg)
	snapshot := operatorstatus.Snapshot{
		Version: operatorstatus.Version, ObservedAt: observedAt.UTC(),
		Profile: profile, ProfileVersion: version, Cluster: cluster,
		Result:  operatorstatus.Result{Decision: "stopped"},
		Journal: journal.Stats{MaxRecords: 65_536, MaxBytes: 64 << 20},
		Control: status,
	}
	if err := operatorstatus.Write(operatorstatus.Path(cfg.Journal.Path), snapshot); err != nil {
		t.Fatal(err)
	}
}

// The state that cost the most time today, and the one nothing could see:
// spending authority granted while no process is executing for it. The runner
// lived only in a tmux session, so every session kill left the legs armed and
// idle, silently, with no supervisor to notice.
func TestDoctorFlagsArmedLegsWithNoRunner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := armedLegWithStatus(t, t.TempDir(), false, 30*time.Minute)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	check := doctorStrategyCheck(time.Now)
	if check.State != readiness.Blocked {
		t.Fatalf("state = %q, want blocked: %+v", check.State, check)
	}
	if !strings.Contains(check.Detail, "no runner has reported") {
		t.Errorf("detail does not name the cause: %q", check.Detail)
	}
	// A blocked check that does not say what to do is a dead end — and telling
	// the operator to re-run it by hand rebuilds the same fragility.
	if !strings.Contains(check.Action, "service install") {
		t.Errorf("action does not point at supervising the runner: %q", check.Action)
	}
	for _, want := range []string{"--output", "mithril-agent-run.service"} {
		if !strings.Contains(check.Action, want) {
			t.Errorf("action omits %q, so it would only print a unit: %q", want, check.Action)
		}
	}
	if strings.Contains(check.Action, "strategy run") {
		t.Errorf("action tells the operator to recreate the unsupervised runner: %q", check.Action)
	}
}

// A leg whose runner IS reporting must read ready, or the check cries wolf and
// gets ignored the one time it matters.
func TestDoctorPassesWhenTheRunnerIsReporting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := armedLegWithStatus(t, t.TempDir(), false, 5*time.Second)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	check := doctorStrategyCheck(time.Now)
	if check.State != readiness.Ready {
		t.Fatalf("state = %q, want ready: %+v", check.State, check)
	}
	if check.Action != "" {
		t.Errorf("a ready check must not demand an action: %q", check.Action)
	}
}

// Configured but not armed cannot trade and never will on its own. Reporting
// that as "waiting" reads as "it is coming", which is how an operator sits in
// front of a set-up-but-never-armed agent wondering why nothing happens.
func TestDoctorTellsTheOperatorToArmConfiguredLegs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	writeFreshStatus(t, sell)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	check := doctorStrategyCheck(time.Now)
	if check.State == readiness.Waiting {
		t.Fatalf("an unarmed strategy was reported as waiting for something: %+v", check)
	}
	if check.State != readiness.Blocked {
		t.Fatalf("state = %q, want blocked: %+v", check.State, check)
	}
	if !strings.Contains(check.Action, "strategy enable") {
		t.Errorf("action does not name the arming command: %q", check.Action)
	}
	if !strings.Contains(check.Action, "--max-trades 6") {
		t.Errorf("action exceeds the profile's funded trade cap: %q", check.Action)
	}
}

func TestDoctorInstallsTheRunnerBeforeArming(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	check := doctorStrategyCheck(time.Now)
	if !strings.Contains(check.Action, "service install") {
		t.Fatalf("action = %q, want service installation", check.Action)
	}
	for _, want := range []string{"--output", "mithril-agent-run.service"} {
		if !strings.Contains(check.Action, want) {
			t.Fatalf("action omits %q, so it would only print a unit: %q", want, check.Action)
		}
	}
	if strings.Contains(check.Action, "strategy enable") {
		t.Fatalf("fresh setup grants authority before the runner is installed: %q", check.Action)
	}
}

func TestDoctorUsesTheProfilesFundedTradeLimit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	cfg, err := readConfig(sell)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Swap.DailyDebitCapLamports = cfg.Swap.InputLamports +
		cfg.Swap.MaxFeeLamports + cfg.Swap.Route.MaxOutputAccountRentLamports
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sell, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFreshStatus(t, sell)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	check := doctorStrategyCheck(time.Now)
	if !strings.Contains(check.Action, "--max-trades 1") {
		t.Errorf("action does not use the profile's funded trade limit: %q", check.Action)
	}
}

func TestDoctorNamesTheExtraConsentForMarketTrading(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	cfg, err := readConfig(sell)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Swap.PriceTrigger = nil
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sell, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFreshStatus(t, sell)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	check := doctorStrategyCheck(time.Now)
	if !strings.Contains(check.Action, "--allow-any-price") {
		t.Errorf("market-mode action omits the required consent: %q", check.Action)
	}
}

// Most deployments are single-leg. A permanent "no strategy configured" line
// would train operators to skip the section, so it is skipped instead.
func TestDoctorSaysNothingWithoutAStrategy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	check := doctorStrategyCheck(time.Now)
	if check.State != readiness.Skipped {
		t.Fatalf("state = %q, want skipped: %+v", check.State, check)
	}
}

// A leg the pointer names but nothing can read may still be armed, and its
// runner holds the profile in memory regardless of what happened to the file.
func TestDoctorFlagsLegsItCannotRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	sell := triggeredLeg(t, dir, false, 0)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sell); err != nil {
		t.Fatal(err)
	}
	check := doctorStrategyCheck(time.Now)
	if check.State != readiness.Blocked {
		t.Fatalf("state = %q, want blocked: %+v", check.State, check)
	}
	if !strings.Contains(check.Detail, "cannot be read") {
		t.Errorf("detail does not name the cause: %q", check.Detail)
	}
}

// A leg that has never run has no status file, and calling that a fault would
// fire on every fresh setup.
func TestMissingStatusFileIsNotStale(t *testing.T) {
	if statusIsStale(filepath.Join(t.TempDir(), "events.jsonl"), time.Now()) {
		t.Error("a leg that has never run was reported as stale")
	}
	if statusIsStale("", time.Now()) {
		t.Error("an empty journal path was reported as stale")
	}
}

func TestDoctorRejectsFreshButInvalidOrWrongProfileStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := armedLegWithStatus(t, t.TempDir(), false, time.Second)
	cfg, err := readConfig(sell)
	if err != nil {
		t.Fatal(err)
	}
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}

	statusPath := operatorstatus.Path(cfg.Journal.Path)
	if err := os.WriteFile(statusPath, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if check := doctorStrategyCheck(time.Now); check.State != readiness.Blocked {
		t.Fatalf("invalid fresh status reported a running strategy: %+v", check)
	}

	writeStrategyStatus(t, cfg, time.Now().UTC(), control.Status{Mode: control.ModeDevnetEnabled,
		ExpiresAt: time.Now().UTC().Add(time.Hour), MaxActions: 4, RemainingActions: 4})
	snapshot, err := operatorstatus.Read(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Profile = orcaswap.BuyProfileName
	snapshot.ProfileVersion = orcaswap.BuyProfileVersion
	if err := operatorstatus.Write(statusPath, snapshot); err != nil {
		t.Fatal(err)
	}
	if check := doctorStrategyCheck(time.Now); check.State != readiness.Blocked {
		t.Fatalf("wrong-profile status reported a running strategy: %+v", check)
	}
}

// Every check doctor emits must satisfy the readiness contract, or the whole
// report is rejected at render time — which is how a doctor check once failed
// the entire command on any machine that had the Telegram env vars set.
func TestStrategyCheckAlwaysSatisfiesTheReadinessContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, build := range []func(){
		func() {},
		func() {
			sell := triggeredLeg(t, t.TempDir(), false, 0)
			_ = recordStrategy(strategyPaths{sell: sell})
		},
		func() {
			sell := armedLegWithStatus(t, t.TempDir(), false, time.Hour)
			_ = recordStrategy(strategyPaths{sell: sell})
		},
		func() {
			sell := armedLegWithStatus(t, t.TempDir(), false, time.Second)
			_ = recordStrategy(strategyPaths{sell: sell})
		},
	} {
		build()
		check := doctorStrategyCheck(time.Now)
		report := readiness.Report{Checks: []readiness.Check{check}}
		if err := report.Validate(); err != nil {
			t.Fatalf("check %+v violates the readiness contract: %v", check, err)
		}
	}
}
