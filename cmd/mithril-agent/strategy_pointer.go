package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
)

// A strategy is a sell leg, a buy leg and a sweep on one wallet. Three setups
// existed before this file; nothing recorded that they belonged together, so
// every command that wanted "the strategy" resolved a SINGLE-valued pointer
// (recordCurrentConfig) and therefore named at most one of them.
//
// Like that pointer, this is a convenience for FINDING files and never an
// authority. It stores paths, each path is re-validated on read by the same
// usableConfigPath, and every gate downstream still reads and validates the
// configuration itself. A tampered pointer can at worst name a file the
// operator must still have written and the profile checks must still accept.
const (
	strategyPointerName = "strategy"
	// Three absolute paths, each prefixed and newline-terminated. Anything
	// larger is not a strategy pointer.
	maxStrategyPointerBytes = int64(12288)
)

// strategyPaths names the legs of one strategy. Any of them may be empty: a
// strategy with no buy leg yet is the ordinary state on a wallet that has
// never held devUSDC, because the buy leg pins the token account it spends
// from and that account does not exist until the first sell creates it.
type strategyPaths struct {
	sell  string
	buy   string
	sweep string
	// Empty keeps the legacy behavior: Telegram alerts enabled.
	telegram string
}

func (p strategyPaths) empty() bool {
	return p.sell == "" && p.buy == "" && p.sweep == ""
}

// configuredLeg names one leg that is actually present.
type configuredLeg struct{ leg, path string }

// configured lists the legs that are actually present, in the order an
// operator reads them: what it sells, what it buys back, where the profit goes.
func (p strategyPaths) configured() []configuredLeg {
	var legs []configuredLeg
	for _, item := range []configuredLeg{
		{"sell", p.sell}, {"buy", p.buy}, {"sweep", p.sweep},
	} {
		if item.path != "" {
			legs = append(legs, item)
		}
	}
	return legs
}

func strategyPointerPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("no home directory")
	}
	return filepath.Join(home, ".mithril-agent", strategyPointerName), nil
}

// recordStrategy notes the legs a strategy setup just wrote. Failing to record
// is not fatal for the same reason recordCurrentConfig is not: every command
// still accepts explicit paths, so a lost pointer costs discoverability, never
// the ability to operate.
func recordStrategy(paths strategyPaths) error {
	for _, item := range paths.configured() {
		if !filepath.IsAbs(item.path) || filepath.Clean(item.path) != item.path {
			return errors.New("strategy leg paths must be absolute and clean")
		}
	}
	if paths.empty() {
		return errors.New("a strategy needs at least one leg")
	}
	if paths.telegram != "" && paths.telegram != "enabled" && paths.telegram != "disabled" {
		return errors.New("Telegram state must be enabled or disabled")
	}
	pointer, err := strategyPointerPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pointer), 0o700); err != nil {
		return errors.New("could not create the agent directory")
	}
	var document strings.Builder
	for _, item := range paths.configured() {
		document.WriteString(item.leg + " " + item.path + "\n")
	}
	if paths.telegram != "" {
		document.WriteString("telegram " + paths.telegram + "\n")
	}
	// The same hardened write the single pointer uses, so a pre-planted symlink
	// at this location cannot redirect it.
	return securefile.ReplacePrivate(
		pointer, []byte(document.String()), maxStrategyPointerBytes)
}

// discoverStrategy reads the recorded legs and, separately, the ones that were
// recorded but no longer resolve to a regular file.
//
// The second return exists because "not configured" and "configured but I
// cannot see it" are opposite facts for a BRAKE. Collapsing them let
// `strategy stop` skip a vanished leg and exit 0 while that leg was still armed
// and still trading.
func discoverStrategy() (strategyPaths, []string) {
	pointer, err := strategyPointerPath()
	if err != nil {
		return strategyPaths{}, nil
	}
	raw, err := securefile.ReadPrivate(pointer, maxStrategyPointerBytes)
	if err != nil {
		return strategyPaths{}, nil
	}
	var paths strategyPaths
	var unreadable []string
	for _, line := range strings.Split(string(raw), "\n") {
		leg, candidate, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found {
			continue
		}
		if leg == "telegram" {
			if candidate == "enabled" || candidate == "disabled" {
				paths.telegram = candidate
			}
			continue
		}
		if !usableConfigPath(candidate) {
			// Recorded but gone: deleted, moved, on an unmounted volume, or
			// replaced by a symlink. Dropping it silently let `strategy stop`
			// report success while that leg kept trading — its runner holds the
			// profile in memory and never re-reads the config.
			unreadable = append(unreadable, leg+" "+candidate)
			continue
		}
		switch leg {
		case "sell":
			paths.sell = candidate
		case "buy":
			paths.buy = candidate
		case "sweep":
			paths.sweep = candidate
		}
	}
	return paths, unreadable
}
