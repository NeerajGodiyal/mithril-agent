package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/readiness"
	"github.com/Overclock-Validator/mithril-agent/telegramoperator"
)

// doctor answers "is this ready, and if not what do I do" using the shared
// readiness package, so the CLI, the guided TUI, MCP and Telegram cannot drift
// into different definitions of ready.
//
// It never signs, submits, or grants anything. Every check is a read.
const doctorUsage = `Usage: mithril-agent doctor [--config PATH] [--json]

Reports whether the system is ready to act and, for anything that is not,
exactly what to do about it. Read-only: it starts nothing and authorises
nothing.`

func runDoctor(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON (optional)")
	asJSON := flags.Bool("json", false, "emit stable JSON for automation")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, doctorUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("doctor takes no positional arguments")
	}
	// With no --config, use the one setup recorded. This is only path
	// discovery: the configuration is still read and validated below.
	found := ""
	if *configPath == "" {
		found = discoverCurrentConfig()
		*configPath = found
	}

	report := buildDoctorReport(ctx, *configPath)
	if *asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	if _, err := fmt.Fprintf(output, "Mithril Agent\n\n"); err != nil {
		return err
	}
	if found != "" {
		if _, err := fmt.Fprintf(output, "Using %s\n\n", found); err != nil {
			return err
		}
	}
	return report.Render(output)
}

// buildDoctorReport degrades honestly: a check it cannot evaluate is Unknown,
// never Ready. It deliberately does not fail the whole command when one input
// is missing, because a partial picture is what an operator needs while they
// are still setting things up.
func buildDoctorReport(ctx context.Context, configPath string) readiness.Report {
	checks := []readiness.Check{doctorClockCheck()}

	if configPath == "" {
		checks = append(checks, readiness.Check{
			Name: "configuration", Title: "Configuration", State: readiness.Blocked,
			Detail: "no config supplied",
			Action: "Run: mithril-agent setup, then this command again",
		})
		return readiness.NewReport(checks)
	}
	configCheck := doctorConfigCheck(configPath)
	checks = append(checks, configCheck)
	if configCheck.State != readiness.Ready {
		// Everything below reads the configuration, so reporting them as
		// unknown would be noise. The operator has one thing to fix first.
		return readiness.NewReport(checks)
	}

	var cfg config
	if clean, err := cleanExistingPath(configPath); err == nil {
		_ = readStrictJSON(clean, &cfg)
	}
	checks = append(checks,
		doctorAccountCheck(ctx, cfg.Signer.KeypairPath),
		doctorFundingCheck(ctx, cfg.Signer.KeypairPath),
		doctorTradingCheck(cfg),
		doctorTelegramCheck(),
	)
	return readiness.NewReport(checks)
}

// doctorFundingCheck reports whether the agent account can actually pay for an
// action. It reads only a public address and a public balance.
func doctorFundingCheck(ctx context.Context, keypairPath string) readiness.Check {
	if keypairPath == "" {
		return readiness.Check{
			Name: "funding", Title: "Account funding", State: readiness.Skipped,
			Detail: "no account configured yet",
		}
	}
	address, err := walletAddress(keypairPath)
	if err != nil {
		return readiness.Check{
			Name: "funding", Title: "Account funding", State: readiness.Skipped,
			Detail: "account not readable",
		}
	}
	var result struct {
		Result struct {
			Value uint64 `json:"value"`
		} `json:"result"`
	}
	if err := walletRPC(ctx, "getBalance", []any{address}, &result); err != nil {
		return readiness.Check{
			Name: "funding", Title: "Account funding", State: readiness.Unknown,
			Detail: "balance could not be read",
			Action: "Check network access, then re-run: mithril-agent doctor --config PATH",
		}
	}
	lamports := result.Result.Value
	if lamports == 0 {
		return readiness.Check{
			Name: "funding", Title: "Account funding", State: readiness.Blocked,
			Detail: "empty",
			Action: "Fund " + address + " at https://faucet.solana.com (Devnet SOL has no value)",
		}
	}
	return readiness.Check{
		Name: "funding", Title: "Account funding", State: readiness.Ready,
		Detail: fmt.Sprintf("%d.%09d SOL", lamports/lamportsPerSOL, lamports%lamportsPerSOL),
	}
}

// doctorTradingCheck reports the control state. Stopped is the correct default
// and is reported as waiting, not as a problem: an agent that is not armed is
// behaving properly.
func doctorTradingCheck(cfg config) readiness.Check {
	unreadable := readiness.Check{
		Name: "trading", Title: "Trading", State: readiness.Unknown,
		Detail: "control state unreadable",
		Action: "Inspect the control state file; never assume stopped without reading it",
	}
	if cfg.Control.StatePath == "" || cfg.Swap == nil {
		return readiness.Check{
			Name: "trading", Title: "Trading", State: readiness.Skipped,
			Detail: "no control state configured",
		}
	}
	fingerprint, err := cfg.Swap.Fingerprint()
	if err != nil {
		return unreadable
	}
	// requireFresh is false: this is a read, and refusing to report on a
	// slightly stale file would leave the operator with less information.
	state, err := control.NewStateFile(cfg.Control.StatePath, fingerprint, false)
	if err != nil {
		return unreadable
	}
	blocked, err := state.NoNewActions()
	if err != nil {
		return readiness.Check{
			Name: "trading", Title: "Trading", State: readiness.Unknown,
			Detail: "control state unreadable",
			Action: "Inspect the control state file; never assume stopped without reading it",
		}
	}
	if blocked {
		return readiness.Check{
			Name: "trading", Title: "Trading", State: readiness.Waiting,
			Detail: "stopped — no action is authorised (the safe default)",
		}
	}
	return readiness.Check{
		Name: "trading", Title: "Trading", State: readiness.Ready,
		Detail: "enabled for a bounded action",
	}
}

// doctorTelegramCheck reports only whether the operator surface is configured.
// It never reads, prints, or validates the token itself.
func doctorTelegramCheck() readiness.Check {
	if os.Getenv(telegramoperator.BotTokenEnvironment) == "" ||
		os.Getenv(telegramoperator.AllowedIDsEnvironment) == "" {
		return readiness.Check{
			Name: "telegram", Title: "Telegram", State: readiness.Skipped,
			Detail: "not configured (optional)",
		}
	}
	return readiness.Check{
		Name: "telegram", Title: "Telegram", State: readiness.Ready,
		Detail: "configured (read-only status only)",
	}
}

func doctorConfigCheck(configPath string) readiness.Check {
	clean, err := cleanExistingPath(configPath)
	if err != nil {
		return readiness.Check{
			Name: "configuration", Title: "Configuration", State: readiness.Blocked,
			Detail: "not readable at that path",
			Action: "Check the path, or run: mithril-agent setup",
		}
	}
	var cfg config
	if err := readStrictJSON(clean, &cfg); err != nil {
		return readiness.Check{
			Name: "configuration", Title: "Configuration", State: readiness.Blocked,
			Detail: "present but not valid",
			Action: "Regenerate it with mithril-agent setup; do not hand-edit it",
		}
	}
	if cfg.Swap == nil {
		return readiness.Check{
			Name: "configuration", Title: "Configuration", State: readiness.Blocked,
			Detail: "no swap profile configured",
			Action: "Run: mithril-agent setup",
		}
	}
	return readiness.Check{
		Name: "configuration", Title: "Configuration", State: readiness.Ready,
		Detail: "valid, cluster " + cfg.Swap.Cluster,
	}
}

// doctorClockCheck reports trusted time, which gates signing. It is reported
// rather than hidden because it is the check most likely to surprise someone
// running on a laptop.
func doctorClockCheck() readiness.Check {
	if preflightOperatingSystem != "linux" {
		return readiness.Check{
			Name: "clock", Title: "Trusted clock", State: readiness.Skipped,
			Detail: "not available on " + preflightOperatingSystem + " (Linux only)",
		}
	}
	sample, err := preflightClockSample()
	if err != nil {
		return readiness.Check{
			Name: "clock", Title: "Trusted clock", State: readiness.Unknown,
			Detail: "could not be read",
			Action: "Check the time-synchronisation service; signing needs provable time",
		}
	}
	if sample.UncertaintyNanos == 0 {
		return readiness.Check{
			Name: "clock", Title: "Trusted clock", State: readiness.Unknown,
			Detail: "no uncertainty reported",
			Action: "Check the time-synchronisation service; signing needs provable time",
		}
	}
	return readiness.Check{
		Name: "clock", Title: "Trusted clock", State: readiness.Ready,
		Detail: fmt.Sprintf("synchronised (±%s)",
			time.Duration(sample.UncertaintyNanos).Round(time.Millisecond)),
	}
}

// doctorAccountCheck reports the dedicated agent account. It reads only the
// public address and never the secret.
func doctorAccountCheck(ctx context.Context, keypairPath string) readiness.Check {
	if keypairPath == "" {
		return readiness.Check{
			Name: "account", Title: "Agent account", State: readiness.Blocked,
			Detail: "not configured",
			Action: "Create a Devnet account with mithril-agent wallet new --file PATH",
		}
	}
	if !filepath.IsAbs(keypairPath) {
		return readiness.Check{
			Name: "account", Title: "Agent account", State: readiness.Blocked,
			Detail: "path is not absolute",
			Action: "Point the configuration at an absolute keypair path",
		}
	}
	if _, err := os.Lstat(keypairPath); err != nil {
		return readiness.Check{
			Name: "account", Title: "Agent account", State: readiness.Blocked,
			Detail: "keypair file is missing",
			Action: "Create one with mithril-agent wallet new --file " + keypairPath,
		}
	}
	address, err := walletAddress(keypairPath)
	if err != nil {
		return readiness.Check{
			Name: "account", Title: "Agent account", State: readiness.Blocked,
			Detail: "keypair is unreadable or not private",
			Action: "Check the file is a 64-byte keypair readable only by this user",
		}
	}
	return readiness.Check{
		Name: "account", Title: "Agent account", State: readiness.Ready,
		Detail: address,
	}
}
