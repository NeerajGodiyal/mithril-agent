package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Overclock-Validator/mithril-agent/readiness"
)

// The root help lists about twenty commands across six sections. Somebody who
// just wants their agent trading has to know which four of those apply to them,
// in what order, and which of the four they are currently between. That is a
// reasonable ask of the person who built it and an unreasonable one of anybody
// else — and it is the actual reason getting a trade out took a whole day.
//
// This command answers one question: what do I do next. It runs the same
// readiness checks `doctor` runs, then names the single next step, in the
// operator's words, with the exact command to type.
//
// It deliberately does NOT arm anything. Arming is the moment spending
// authority is granted, and a command whose whole purpose is "just make it
// work" is precisely the wrong place to hide that. What it removes is the
// guessing, not the consent.
const startUsage = `Usage: mithril-agent start [--config PATH] [--json]

Says where your agent is and the one thing to do next.

Run it whenever you are not sure. It changes nothing and can be run as often as
you like; arming a wallet stays a separate, explicit command.`

func runStart(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("start", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON (optional)")
	asJSON := flags.Bool("json", false, "emit the readiness report as JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, startUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("start takes no positional arguments")
	}
	if *configPath == "" {
		*configPath = discoverCurrentConfig()
	}
	report := buildDoctorReport(ctx, *configPath)
	if *asJSON {
		return writeReadinessJSON(output, report)
	}
	return writeNextStep(output, report)
}

// writeNextStep prints the state in one line and the next command in another.
// Anything longer gets skimmed, and a skimmed instruction is an unfollowed one.
func writeNextStep(output io.Writer, report readiness.Report) error {
	w := func(format string, args ...any) error {
		_, err := fmt.Fprintf(output, format, args...)
		return err
	}
	if err := w("\nMithril Agent\n\n"); err != nil {
		return err
	}

	// The FIRST blocked check is the next step. Listing every problem at once
	// invites picking the easy one, and these have a natural order: nothing
	// downstream can be fixed while something upstream is broken.
	for _, check := range report.Checks {
		if check.State == readiness.Blocked && check.Action != "" {
			if err := w("  %s\n", check.Detail); err != nil {
				return err
			}
			return w("\n  Next:  %s\n\n", check.Action)
		}
	}
	// Waiting is not a problem to solve, it is a thing to wait for — and saying
	// so plainly is what stops somebody "fixing" a healthy system.
	for _, check := range report.Checks {
		if check.State == readiness.Waiting {
			if err := w("  %s — %s\n", check.Title, check.Detail); err != nil {
				return err
			}
			if check.Action != "" {
				return w("\n  Next:  %s\n\n", check.Action)
			}
			return w("\n  Nothing to do. It proceeds on its own.\n\n")
		}
	}
	// Unknown means evidence could not be read. It must never render as ready.
	for _, check := range report.Checks {
		if check.State == readiness.Unknown {
			if err := w("  %s could not be checked — %s\n", check.Title, check.Detail); err != nil {
				return err
			}
			return w("\n  Next:  mithril-agent doctor      (shows every check)\n\n")
		}
	}
	if err := w("  Everything is ready.\n\n"); err != nil {
		return err
	}
	// The one step this command will not take for you. Derive its bound from the
	// signed profiles: setup may fund only one trade, and suggesting four turns
	// an otherwise ready install into an immediate refusal.
	command := "strategy enable --duration 8h --max-trades 1 --reason TEXT"
	if paths, unreadable := discoverStrategy(); !paths.empty() && len(unreadable) == 0 {
		if suggested, err := suggestedStrategyEnableCommand(paths); err == nil {
			command = suggested
		}
	}
	return w("  Next:  mithril-agent %s\n"+
		"         That grants spending authority, so it stays yours to type.\n\n", command)
}

func writeReadinessJSON(output io.Writer, report readiness.Report) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
