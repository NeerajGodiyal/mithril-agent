package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
)

// showEveryLeg records a sell and a buy leg and returns the rendered screen.
func showEveryLeg(t *testing.T, args ...string) (string, strategyView) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	buy := triggeredLeg(t, t.TempDir(), true, 0)
	if err := recordStrategy(strategyPaths{sell: sell, buy: buy}); err != nil {
		t.Fatal(err)
	}
	var text bytes.Buffer
	if err := strategyShow(args, &text); err != nil {
		t.Fatal(err)
	}
	var payload bytes.Buffer
	if err := strategyShow(append(append([]string{}, args...), "--json"), &payload); err != nil {
		t.Fatal(err)
	}
	var view strategyView
	if err := json.Unmarshal(payload.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return text.String(), view
}

// This screen claims to show everything configured. It resolved a SINGLE
// trading leg through the single-config pointer, so a round trip's buy leg was
// invisible here while doctor counted it, the runner ran it, and it could
// spend. Somebody checking their setup would conclude the buy leg was missing.
func TestShowDisplaysEveryTradingLeg(t *testing.T) {
	screen, view := showEveryLeg(t)
	if len(view.Legs) != 2 {
		t.Fatalf("showed %d trading leg(s), want both:\n%s", len(view.Legs), screen)
	}
	if !strings.Contains(screen, "sell SOL for devUSDC") {
		t.Errorf("the sell leg is missing:\n%s", screen)
	}
	if !strings.Contains(screen, "buy SOL with devUSDC") {
		t.Errorf("the buy leg is missing — the exact bug this guards:\n%s", screen)
	}
	// Two blocks that differ only by a direction line four rows down is not a
	// screen anybody reads correctly; the role belongs in the heading.
	for _, heading := range []string{"SELL LEG", "BUY LEG"} {
		if !strings.Contains(screen, heading) {
			t.Errorf("the heading does not name the %s:\n%s", heading, screen)
		}
	}
}

// Each leg must carry its OWN config path. Sharing one path across the blocks
// would print a stop command that stops the wrong leg.
func TestShowGivesEachLegItsOwnStopCommand(t *testing.T) {
	screen, view := showEveryLeg(t)
	if len(view.Legs) != 2 {
		t.Fatalf("want 2 legs, got %d", len(view.Legs))
	}
	if view.Legs[0].ConfigPath == view.Legs[1].ConfigPath {
		t.Fatalf("both legs report the same config path: %q", view.Legs[0].ConfigPath)
	}
	for _, leg := range view.Legs {
		if leg.ConfigPath == "" {
			t.Fatalf("a leg has no config path: %+v", leg)
		}
		if !strings.Contains(screen, "swap stop --config "+leg.ConfigPath) {
			t.Errorf("no stop command for %s:\n%s", leg.ConfigPath, screen)
		}
	}
}

// An explicit --config still means exactly that one leg: someone inspecting a
// single config must not be shown the whole strategy.
func TestShowHonoursAnExplicitConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	buy := triggeredLeg(t, t.TempDir(), true, 0)
	if err := recordStrategy(strategyPaths{sell: sell, buy: buy}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := strategyShow([]string{"--config", buy, "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var view strategyView
	if err := json.Unmarshal(out.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Legs) != 1 || view.Legs[0].ConfigPath != buy {
		t.Fatalf("--config did not select exactly the named leg: %+v", view.Legs)
	}
}

// A deployment with nothing configured must say so rather than render an empty
// frame that looks like a working strategy.
func TestShowSaysWhenNothingIsConfigured(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := strategyShow(nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing configured yet") {
		t.Errorf("an empty deployment did not say so:\n%s", out.String())
	}
}

func TestShowDoesNotExposeTheSweepProfileName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sweep := writeStrategyConfig(t, config{
		Profile: testSweepProfileForStrategy(
			"3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh",
			"Ht5yBt2UDAFyHPcTmRrDgE5Q2QodrmYKYwKrokwNmAdM",
			time.Now().UTC().Unix(),
		),
	})
	if err := recordStrategy(strategyPaths{sweep: sweep}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := strategyShow(nil, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "treasury_sweep_v1") {
		t.Fatalf("human output contains an internal profile name:\n%s", out.String())
	}
}

// A leg whose trades are spent read as a plain "devnet_enabled" on this screen
// while the runner refused it every ten seconds. The grant, with its "(cannot
// act)" note, was computed for trading legs and then never printed — only the
// sweep row showed it. An operator checking before going away saw enabled.
func TestStrategyShowSaysWhenATradingLegCanNoLongerAct(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	cfg, err := readConfig(sell)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := cfg.Swap.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	// Enabled, unexpired, and out of actions: exactly what a completed trade
	// leaves behind.
	now := time.Now().UTC()
	if err := control.WriteDevnetActivation(
		cfg.Control.StatePath, fingerprint, now, now.Add(time.Hour), 1, "spent",
	); err != nil {
		t.Fatal(err)
	}
	// Arming refuses a zero limit, so the state is spent the way trading spends
	// it: the grant stays enabled and unexpired, with no actions left.
	spendOneAction(t, cfg.Control.StatePath)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := strategyShow(nil, &out); err != nil {
		t.Fatal(err)
	}
	var line string
	for _, candidate := range strings.Split(out.String(), "\n") {
		if strings.Contains(candidate, "TRADING") && strings.Contains(candidate, "control:") {
			line = candidate
		}
	}
	if line == "" {
		t.Fatalf("no trading control line was printed:\n%s", out.String())
	}
	if !strings.Contains(line, "cannot act") {
		t.Errorf("a spent grant was reported as able to trade: %q", line)
	}
}

// spendOneAction drives the recorded grant to the state a completed trade
// leaves: still enabled, still unexpired, nothing left to spend.
func spendOneAction(t *testing.T, statePath string) {
	t.Helper()
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state["remaining_actions"] = 0
	updated, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, updated, 0o600); err != nil {
		t.Fatal(err)
	}
}
