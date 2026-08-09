package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLeg(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A strategy is three setups on one wallet. The single-valued pointer could
// name at most one of them, which is why every command that wanted "the
// strategy" silently operated on a fraction of it.
func TestStrategyPointerRoundTripsEveryLeg(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	want := strategyPaths{
		sell:  writeLeg(t, dir, "sell"),
		buy:   writeLeg(t, dir, "buy"),
		sweep: writeLeg(t, dir, "sweep"),
	}
	if err := recordStrategy(want); err != nil {
		t.Fatal(err)
	}
	if got, _ := discoverStrategy(); got != want {
		t.Fatalf("discovered %+v, want %+v", got, want)
	}
}

func TestStrategyPointerPreservesTelegramChoice(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := recordStrategy(strategyPaths{
		sell: writeLeg(t, dir, "sell"), telegram: "disabled",
	}); err != nil {
		t.Fatal(err)
	}
	got, unreadable := discoverStrategy()
	if len(unreadable) != 0 {
		t.Fatalf("unexpected unreadable entries: %v", unreadable)
	}
	if got.telegram != "disabled" {
		t.Fatalf("Telegram choice was lost: %q", got.telegram)
	}
}

// A wallet that has never held devUSDC cannot have a buy leg yet, so a
// two-leg strategy is the ordinary state, not an error.
func TestStrategyPointerHandlesAMissingBuyLeg(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	want := strategyPaths{sell: writeLeg(t, dir, "sell"), sweep: writeLeg(t, dir, "sweep")}
	if err := recordStrategy(want); err != nil {
		t.Fatal(err)
	}
	got, _ := discoverStrategy()
	if got != want || got.buy != "" {
		t.Fatalf("discovered %+v, want %+v", got, want)
	}
	if legs := got.configured(); len(legs) != 2 {
		t.Fatalf("configured legs = %d, want 2", len(legs))
	}
}

// A leg whose file has been removed must read as "not configured" rather than
// surfacing later as a confusing failure from inside a gate.
func TestStrategyPointerDropsStaleLegs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	sell := writeLeg(t, dir, "sell")
	buy := writeLeg(t, dir, "buy")
	if err := recordStrategy(strategyPaths{sell: sell, buy: buy}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(buy); err != nil {
		t.Fatal(err)
	}
	got, unreadable := discoverStrategy()
	if got.sell != sell || got.buy != "" {
		t.Fatalf("stale leg survived: %+v", got)
	}
	// Silently dropping it is what let `strategy stop` report success while a
	// vanished leg was still armed and still trading.
	if len(unreadable) != 1 || !strings.Contains(unreadable[0], buy) {
		t.Fatalf("the vanished leg was not reported: %v", unreadable)
	}
}

// The pointer names paths and nothing else. A relative or unclean path must be
// refused at write time so it can never be handed onward.
func TestStrategyPointerRefusesUnsafePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for name, paths := range map[string]strategyPaths{
		"relative": {sell: "sell/config.json"},
		"unclean":  {sell: "/tmp/../tmp/config.json"},
		"empty":    {},
	} {
		t.Run(name, func(t *testing.T) {
			if err := recordStrategy(paths); err == nil {
				t.Fatal("an unsafe strategy pointer was written")
			}
		})
	}
}

// Nothing recorded is not an error: it is a machine where no strategy has been
// set up, and every command must be able to say so.
func TestStrategyDiscoveryIsEmptyWithNoPointer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got, _ := discoverStrategy(); !got.empty() {
		t.Fatalf("discovered %+v on a fresh machine", got)
	}
}

// A strategy records its legs in its own pointer and nothing else. Without a
// fallback, doctor, preflight, strategy show/alerts and the swap read-only
// commands all reported "nothing configured" while a real strategy was armed
// and able to trade — the diagnostic surface blind exactly when it mattered.
func TestASetUpStrategyIsVisibleToTheSingleConfigFinder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	sell := writeLeg(t, dir, "sell")
	if err := recordStrategy(strategyPaths{
		sell: sell, sweep: writeLeg(t, dir, "sweep"),
	}); err != nil {
		t.Fatal(err)
	}
	if got := discoverCurrentConfig(); got != sell {
		t.Fatalf("discoverCurrentConfig() = %q, want the strategy's sell leg %q", got, sell)
	}
}

// The single recorded pointer still wins: an operator who set up one leg the
// old way must not have it silently replaced by a strategy leg.
func TestTheRecordedSingleConfigStillWinsOverAStrategy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	single := writeLeg(t, dir, "single")
	if err := recordCurrentConfig(single); err != nil {
		t.Fatal(err)
	}
	if err := recordStrategy(strategyPaths{sell: writeLeg(t, dir, "sell")}); err != nil {
		t.Fatal(err)
	}
	if got := discoverCurrentConfig(); got != single {
		t.Fatalf("discoverCurrentConfig() = %q, want the explicitly recorded %q", got, single)
	}
}
