package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
)

// strategyStop is the brake. A strategy is several independently armed legs, so
// stopping "the strategy" previously meant remembering every config path and
// running a different command per leg — under time pressure, which is the only
// time anyone reaches for a brake.
//
// It can only ever NARROW authority. Both primitives it calls refuse to create
// a grant, refuse to clear a terminal latch, and refuse to widen anything; the
// worst outcome of a bug here is that a leg stays armed, and the command says
// so and exits non-zero. It deliberately does not stop at the first failure:
// with three armed legs, aborting after one would leave the operator believing
// they had pressed a brake that only half worked.
//
// It does not kill the runner. A stopped leg with a live runner keeps
// observing and reporting, which is what an operator wants after pulling a
// brake: the agent stays visible and stays unable to act.
func strategyStop(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("strategy stop", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	reason := flags.String("reason", "", "why the strategy is being stopped (recorded)")
	sellPath := flags.String("sell-config", "", "override the recorded sell leg")
	buyPath := flags.String("buy-config", "", "override the recorded buy leg")
	sweepPath := flags.String("sweep-config", "", "override the recorded sweep")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, strategyUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *reason == "" {
		return errors.New("strategy stop requires --reason TEXT")
	}

	paths, unreadable := discoverStrategy()
	if *sellPath != "" {
		paths.sell = *sellPath
	}
	if *buyPath != "" {
		paths.buy = *buyPath
	}
	if *sweepPath != "" {
		paths.sweep = *sweepPath
	}
	// Fall back to the single recorded config so `strategy stop` still works for
	// somebody who set up one leg the old way and never ran a strategy setup.
	if paths.empty() {
		paths.sell = discoverCurrentConfig()
	}
	// "nothing configured" and "configured but I cannot see it" are opposite
	// facts for a brake. Collapsing them meant that when EVERY leg vanished —
	// the worst case — this returned early and printed nothing, while the
	// grants stayed live in their own control files and the runners kept
	// trading from profiles they hold in memory.
	if paths.empty() && len(unreadable) == 0 {
		return errors.New(
			"no configured legs were found; name them with --sell-config/--buy-config/--sweep-config")
	}

	failures := len(unreadable)
	for _, entry := range unreadable {
		// A leg the pointer names but nothing can read may still be armed, and
		// its runner holds the profile in memory — it keeps trading regardless
		// of what happened to the file. Reporting success here is the exact
		// "brake that only half worked" this command exists to prevent.
		if _, err := fmt.Fprintf(output,
			"  %-6s CANNOT BE READ — it may still be armed: %s\n", "?", entry); err != nil {
			return err
		}
	}
	for _, leg := range paths.configured() {
		if err := stopStrategyLeg(leg.leg, leg.path, *reason); err != nil {
			failures++
			if _, writeErr := fmt.Fprintf(output,
				"  %-6s STILL ARMED — %v\n", leg.leg, err); writeErr != nil {
				return writeErr
			}
			continue
		}
		if _, err := fmt.Fprintf(output, "  %-6s stopped\n", leg.leg); err != nil {
			return err
		}
	}
	if failures != 0 {
		return fmt.Errorf("%d leg(s) could not be stopped and may still act", failures)
	}
	return nil
}

// stopStrategyLeg routes to whichever stop the leg's profile needs. A swap leg
// carries a fingerprinted swap profile and stops through the same path
// `swap stop` uses; the sweep is a legacy profile whose control file is written
// directly. Reading the config to decide is what keeps a mistyped path from
// stopping the wrong kind of thing and reporting success.
func stopStrategyLeg(leg, path, reason string) error {
	cfg, err := readConfig(path)
	if err != nil {
		return err
	}
	if cfg.Swap != nil {
		_, err := stopSwap(cfg, reason)
		return err
	}
	if !cfg.hasLegacyProfile() {
		return errors.New("this config has no profile to stop")
	}
	if cfg.Control.StatePath == "" {
		return errors.New("this config records no control state path")
	}
	return control.WriteNoNewActions(cfg.Control.StatePath, reason)
}
