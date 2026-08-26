package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/internal/control"
)

// armedSwapLeg writes a swap config with a live grant and returns its path.
func armedSwapLeg(t *testing.T, dir string) (string, string) {
	t.Helper()
	profile := testSwapProfile(reserveOwner)
	statePath := filepath.Join(dir, "control.json")
	cfg := config{Swap: &profile}
	cfg.Evidence.PrimaryTrustDomain = "primary.test"
	cfg.Evidence.SecondaryTrustDomain = "secondary.test"
	cfg.Control.StatePath = statePath
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := control.WriteDevnetActivation(
		statePath, fingerprint, now, now.Add(time.Hour), 4, "armed for the test",
	); err != nil {
		t.Fatal(err)
	}
	return configPath, statePath
}

func modeAt(t *testing.T, statePath string) string {
	t.Helper()
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	return document.Mode
}

// The brake exists for the moment an operator needs every leg stopped at once.
// Doing it per-leg meant remembering three paths under time pressure.
func TestStrategyStopStopsEveryConfiguredLeg(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sellDir, buyDir := t.TempDir(), t.TempDir()
	sellConfig, sellState := armedSwapLeg(t, sellDir)
	buyConfig, buyState := armedSwapLeg(t, buyDir)
	if err := recordStrategy(strategyPaths{sell: sellConfig, buy: buyConfig}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := strategyStop([]string{"--reason", "done for now"}, &output); err != nil {
		t.Fatal(err)
	}
	for name, statePath := range map[string]string{"sell": sellState, "buy": buyState} {
		if mode := modeAt(t, statePath); mode == "devnet_enabled" {
			t.Errorf("%s leg is still armed (mode=%s)", name, mode)
		}
	}
	if !strings.Contains(output.String(), "sell") || !strings.Contains(output.String(), "buy") {
		t.Errorf("the report did not name both legs:\n%s", output.String())
	}
}

// One unstoppable leg must not hide the others: with three armed legs, aborting
// at the first failure leaves the operator believing a brake worked when it
// half worked.
func TestStrategyStopContinuesPastAFailingLeg(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	goodDir := t.TempDir()
	goodConfig, goodState := armedSwapLeg(t, goodDir)
	missing := filepath.Join(t.TempDir(), "gone.json")
	if err := os.WriteFile(missing, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordStrategy(strategyPaths{sell: missing, buy: goodConfig}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := strategyStop([]string{"--reason", "brake"}, &output)
	if err == nil {
		t.Fatal("a leg that could not be stopped reported success")
	}
	if mode := modeAt(t, goodState); mode == "devnet_enabled" {
		t.Errorf("the reachable leg was skipped after the other failed (mode=%s)", mode)
	}
	if !strings.Contains(output.String(), "STILL ARMED") {
		t.Errorf("the failing leg was not called out:\n%s", output.String())
	}
}

// The sweep is a legacy profile with a different stop path. Routing on the
// config keeps a mistyped path from stopping the wrong kind of thing and
// reporting success.
func TestStrategyStopHandlesTheSweepProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	statePath := filepath.Join(dir, "control.json")
	profile := testSweepProfileForStrategy(reserveOwner, otherOwner,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC).Unix())
	cfg := config{Profile: profile}
	cfg.Control.StatePath = statePath
	raw, marshalErr := json.Marshal(cfg)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	sweepConfig := filepath.Join(dir, "config.json")
	if err := os.WriteFile(sweepConfig, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := control.WriteDevnetActivation(
		statePath, fingerprint, time.Now().UTC(), time.Now().UTC().Add(time.Hour), 2, "armed sweep",
	); err != nil {
		t.Fatal(err)
	}
	if err := recordStrategy(strategyPaths{sweep: sweepConfig}); err != nil {
		t.Fatal(err)
	}
	if err := strategyStop([]string{"--reason", "stop the sweep"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if mode := modeAt(t, statePath); mode == "devnet_enabled" {
		t.Errorf("the sweep is still armed (mode=%s)", mode)
	}
}

// A brake with no reason recorded is not a brake anybody can audit later.
func TestStrategyStopRequiresAReason(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := strategyStop(nil, &bytes.Buffer{}); err == nil {
		t.Fatal("strategy stop ran without a reason")
	}
}

// A service restart is not an operator acknowledgement. If a process died
// after the durable send marker, clearing that marker in ExecStartPre hid the
// only evidence that the transaction might have reached the network.
func TestAutomaticServiceStopPreservesPendingRecovery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configPath, statePath := armedSwapLeg(t, t.TempDir())
	cfg, err := readSwapConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := cfg.Swap.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	state, err := control.NewStateFile(statePath, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	actionID := strings.Repeat("a", 64)
	blocked, err := state.WithSendBarrier(actionID, func() error { return nil })
	if err != nil || blocked {
		t.Fatalf("mark send boundary: blocked=%t error=%v", blocked, err)
	}

	if err := stopStrategyLeg("sell", configPath, "service_start"); err == nil ||
		!strings.Contains(err.Error(), "review the transaction") {
		t.Fatalf("automatic service stop = %v", err)
	}
	status, err := state.Status()
	if err != nil || !status.RecoveryPending {
		t.Fatalf("automatic stop lost recovery evidence: status=%+v error=%v", status, err)
	}

	if err := stopStrategyLeg("sell", configPath, "operator reviewed the pending action"); err != nil {
		t.Fatal(err)
	}
	status, err = state.Status()
	if err != nil || status.RecoveryPending || status.Mode != control.ModeNoNewActions {
		t.Fatalf("explicit stop did not resolve recovery: status=%+v error=%v", status, err)
	}
}

func TestAutomaticServiceStopPreservesPendingSweepRecovery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	statePath := filepath.Join(dir, "control.json")
	profile := testSweepProfileForStrategy(
		reserveOwner, otherOwner, time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC).Unix(),
	)
	cfg := config{Profile: profile}
	cfg.Control.StatePath = statePath
	configPath := filepath.Join(dir, "config.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := control.WriteDevnetActivation(
		statePath, fingerprint, now, now.Add(time.Hour), 2, "armed sweep",
	); err != nil {
		t.Fatal(err)
	}
	state, err := control.NewStateFile(statePath, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	actionID := strings.Repeat("b", 64)
	if blocked, err := state.WithSendBarrier(actionID, func() error { return nil }); err != nil || blocked {
		t.Fatalf("mark sweep send boundary: blocked=%t error=%v", blocked, err)
	}

	if err := stopStrategyLeg("sweep", configPath, "service_stop"); err == nil ||
		!strings.Contains(err.Error(), "review the transaction") {
		t.Fatalf("automatic sweep stop = %v", err)
	}
	status, err := state.Status()
	if err != nil || !status.RecoveryPending {
		t.Fatalf("automatic sweep stop lost recovery evidence: status=%+v error=%v", status, err)
	}
	if err := stopStrategyLeg("sweep", configPath, "operator reviewed the pending sweep"); err != nil {
		t.Fatal(err)
	}
}

// Nothing configured must say so rather than reporting a successful stop of
// nothing at all.
func TestStrategyStopSaysWhenThereIsNothingToStop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := strategyStop([]string{"--reason", "nothing here"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "no configured legs") {
		t.Fatalf("error = %v, want a complaint about no configured legs", err)
	}
}

// testSweepProfileForStrategy is a valid legacy sweep profile. anchorUnix is a
// parameter because "before its first window" is a case the strategy commands
// must handle, not an edge case.
func testSweepProfileForStrategy(source, destination string, anchorUnix int64) agent.Profile {
	return agent.Profile{
		Name: agent.ProfileTreasurySweepV1, Version: 1, Cluster: "devnet",
		Source: source, Destination: destination,
		ReserveLamports: 1_000_000, MinTransferLamports: 10_000,
		MaxTransferLamports: 1_000_000, DailyCapLamports: 2_000_000,
		MaxFeeLamports: 100_000, ScheduleWindowSeconds: 3_600,
		ScheduleAnchorUnix:        anchorUnix,
		MaxClockUncertaintyMillis: 100, MaxObservationAgeSeconds: 30,
		MinHealthyObservationSeconds: 5, MinHealthySlotAdvance: 1,
		MaxNodeLagSlots: 150, MaxReconciliationSeconds: 180,
	}
}

// A leg the pointer names but nothing can read may still be armed, and its
// runner holds the profile in memory — it keeps trading whatever happened to
// the file. Dropping it silently let the brake report success while one leg
// was still live, which is the precise failure this command exists to prevent.
func TestStrategyStopFailsOnALegItCannotEvenRead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	goodConfig, goodState := armedSwapLeg(t, t.TempDir())
	vanished := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(vanished, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordStrategy(strategyPaths{sell: vanished, buy: goodConfig}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(vanished); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	err := strategyStop([]string{"--reason", "brake"}, &output)
	if err == nil {
		t.Fatal("a leg that could not be read was reported as stopped")
	}
	if !strings.Contains(output.String(), "CANNOT BE READ") {
		t.Errorf("the unreadable leg was not named:\n%s", output.String())
	}
	// The reachable leg must still be stopped: a partial brake is still a brake.
	if mode := modeAt(t, goodState); mode == "devnet_enabled" {
		t.Errorf("the readable leg was skipped (mode=%s)", mode)
	}
}

// The worst case for a brake is EVERY leg unreadable, and that was the one case
// it stayed silent: the empty-paths early return fired first and discarded the
// unreadable list, so the operator saw nothing while every grant stayed live.
func TestStrategyStopSpeaksWhenEveryLegIsUnreadable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sell := filepath.Join(dir, "sell.json")
	buy := filepath.Join(dir, "buy.json")
	for _, path := range []string{sell, buy} {
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := recordStrategy(strategyPaths{sell: sell, buy: buy}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{sell, buy} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}

	var output bytes.Buffer
	err := strategyStop([]string{"--reason", "everything gone"}, &output)
	if err == nil {
		t.Fatal("a strategy whose every leg vanished reported a successful stop")
	}
	if strings.Contains(err.Error(), "no configured legs") {
		t.Fatalf("vanished legs were reported as never configured: %v", err)
	}
	if strings.Count(output.String(), "CANNOT BE READ") != 2 {
		t.Fatalf("both vanished legs should be named:\n%s", output.String())
	}
}
