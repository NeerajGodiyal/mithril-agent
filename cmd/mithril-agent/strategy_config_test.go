package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Only the wizard ever records the pointer, and it is single-valued. A leg
// built by `swap setup` — until recently the only way to set a price at all —
// was therefore invisible to every strategy command, which answered "nothing
// configured yet" about a profile sitting on disk.
func TestStrategyShowReadsAnExplicitConfig(t *testing.T) {
	profile := testSwapProfile(reserveOwner)
	path := writeStrategyConfig(t, config{Swap: &profile})
	var output bytes.Buffer
	if err := strategyShow([]string{"--config", path}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), path) {
		t.Fatalf("the named config was not used:\n%s", output.String())
	}
	if strings.Contains(output.String(), "nothing configured yet") {
		t.Error("a config given by path was reported as nothing configured")
	}
}

// The alerts verb parses its own flags after the verb, so --config has to be
// consumed before it. A malformed one must say so rather than being read as a
// subcommand.
func TestStrategyAlertsRejectsAConfigFlagWithoutAPath(t *testing.T) {
	for _, args := range [][]string{
		{"--config"},
		{"--config="},
		{"--config", ""},
	} {
		if err := strategyAlerts(args, &bytes.Buffer{}); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
}

// `setup strategy --dir elsewhere` writes the sweep under that directory, not
// the fixed default. Falling straight to the default meant this screen could
// never see it while claiming to show everything configured.
func TestStrategyShowFindsASweepOutsideTheDefaultDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	profile := testSweepProfileForStrategy(reserveOwner, otherOwner,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC).Unix())
	cfg := config{Profile: profile}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sweep := filepath.Join(dir, "config.json")
	if err := os.WriteFile(sweep, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordStrategy(strategyPaths{sweep: sweep}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := strategyShow(nil, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), sweep) {
		t.Fatalf("the strategy's own sweep was not shown:\n%s", output.String())
	}
}
