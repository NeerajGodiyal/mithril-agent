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
	"strings"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
)

// The strategy command is one place to see everything the agent is allowed
// to do — caps, triggers, alerts, sweep — and to edit the one class of
// setting that is safe to edit live: notify-only alert thresholds.
//
// Everything that widens spending authority (caps, triggers, the sweep
// floor, the destination) deliberately routes through a full re-setup
// instead, because those live inside fingerprinted profiles where an edit
// re-keys action IDs, control binding, and the signer ledger. The show
// screen says which is which, so nobody discovers the difference by
// surprise.
const strategyUsage = `Usage:
  mithril-agent strategy [show] [--config PATH] [--sweep-config PATH] [--json]
                                             one screen: caps, triggers, alerts, sweep
  mithril-agent strategy alerts [--config PATH] set
                                    [--price-above USD] [--price-below USD]
                                    [--balance-above SOL] [--balance-below SOL]
  mithril-agent strategy alerts clear        remove every alert threshold
  mithril-agent strategy init [PATH]         write one file holding every setting
  mithril-agent strategy edit PATH [--raw]   guided questions; --raw opens $EDITOR
  mithril-agent strategy run [--interval D] [--metrics-base-port N]
                             [--quote-socket PATH]
                             [--signer-socket-prefix PATH]
                             [--risk-socket-prefix PATH]
                             [--submitter-socket-prefix PATH]
                                             one process, every leg; leave running
  mithril-agent strategy enable --duration D [--max-trades N] --reason TEXT
                                             arm EVERY configured leg at once
                                             D is at most 24h; --max-trades may
                                             not exceed the trades_per_day the
                                             legs' spending caps actually fund
  mithril-agent strategy stop --reason TEXT  stop EVERY configured leg at once
  mithril-agent strategy round-trip --sell-config PATH --buy-config PATH
                                    [--sweep-config PATH] [--json]
  mithril-agent strategy dca-plan (--total-sol SOL |
                                   --budget-usd USD --reference-sol-usd USD)
                                  --days N [--trades-per-day N]
                                             calculate a bounded sell schedule;
                                             never arms, signs, or submits

Alerts are notify-only: they message the operator through the deployed
alerting stack and can never trade or move funds. Edits take effect on the
runner's next cycle without a restart.

Caps, price triggers, and the sweep floor are part of the signed profile;
change them by running setup again. The sweep destination additionally
requires a fresh proof-of-control ceremony (setup sweep).`

func runStrategy(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 {
		return strategyShow(args, output)
	}
	switch args[0] {
	case "show":
		return strategyShow(args[1:], output)
	case "edit":
		return runStrategyEdit(args[1:], output)
	case "init":
		return runStrategyInit(args[1:], output)
	case "run":
		return strategyRun(ctx, args[1:], output)
	case "enable":
		return strategyEnable(args[1:], output)
	case "stop":
		return strategyStop(args[1:], output)
	case "round-trip":
		return strategyRoundTrip(args[1:], output)
	case "dca-plan":
		return strategyDCAPlan(args[1:], output)
	case "alerts":
		return strategyAlerts(args[1:], output)
	case "help", "-h", "--help":
		_, err := fmt.Fprintln(output, strategyUsage)
		return err
	default:
		return fmt.Errorf("unknown strategy command %q; run mithril-agent strategy --help", args[0])
	}
}

// strategyView is the machine-readable form of the show screen.
//
// Legs is a LIST because a strategy is a sell, a buy and a sweep. This held a
// single swap, resolved through the single-config pointer, so a round trip's
// buy leg was invisible on the one screen that claims to show everything
// configured — while doctor counted it, the runner ran it, and it could spend.
//
// The sweep below already reads the strategy pointer, with a comment about
// exactly this failure. The trading legs never got the same fix.
type strategyView struct {
	Legs            []swapLegView      `json:"legs,omitempty"`
	Alerts          *alertsConfig      `json:"alerts,omitempty"`
	SweepConfigPath string             `json:"sweep_config_path,omitempty"`
	Sweep           *sweepStrategyView `json:"sweep,omitempty"`
}

// swapLegView is one trading leg and where it came from. Leg is empty for a
// single-leg deployment, which has no role to name.
type swapLegView struct {
	Leg        string `json:"leg,omitempty"`
	ConfigPath string `json:"config_path"`
	swapStrategyView
}

type swapStrategyView struct {
	Profile        string `json:"profile"`
	Direction      string `json:"direction"`
	InputPerAction string `json:"input_per_action"`
	DailyCap       string `json:"daily_cap"`
	// FundedTradesPerDay is the daily cap expressed as trades. It is the number
	// that decides whether an unattended strategy keeps working after its first
	// trade, and reading it off the cap meant doing the division by hand.
	FundedTradesPerDay uint64 `json:"funded_trades_per_day"`
	MaxFee             string `json:"max_fee"`
	TriggerDirection   string `json:"trigger_direction,omitempty"`
	TriggerUSD         string `json:"trigger_usd,omitempty"`
	ControlMode        string `json:"control_mode,omitempty"`
	ControlGrant       string `json:"control_grant,omitempty"`
}

type sweepStrategyView struct {
	Destination string `json:"destination"`
	ProvenAt    string `json:"proven_at,omitempty"`
	ActiveAfter string `json:"active_after,omitempty"`
	ProofValid  bool   `json:"proof_valid"`
	Floor       string `json:"floor"`
	MaxPerSweep string `json:"max_per_sweep"`
	DailyCap    string `json:"daily_cap"`
	ControlMode string `json:"control_mode,omitempty"`
}

// tradingLegsToShow names the trading legs this screen must display. An
// explicit --config means exactly that one. Otherwise it is every trading leg
// the strategy recorded — NOT the single-config pointer, which names one leg
// and hid the other.
//
// The sweep is excluded: it is not a trade and is rendered on its own below.
func tradingLegsToShow(explicit string) []configuredLeg {
	if explicit != "" {
		return []configuredLeg{{path: explicit}}
	}
	paths, _ := discoverStrategy()
	var legs []configuredLeg
	for _, leg := range paths.configured() {
		if leg.leg != "sweep" {
			legs = append(legs, leg)
		}
	}
	if len(legs) != 0 {
		return legs
	}
	// A single-leg deployment has no strategy pointer and no role to name.
	if single := discoverCurrentConfig(); single != "" {
		return []configuredLeg{{path: single}}
	}
	return nil
}

func buildSwapLegView(leg, path string, cfg config) swapLegView {
	view := swapLegView{Leg: leg, ConfigPath: path}
	view.Profile = cfg.Swap.Name
	view.Direction = "sell SOL for devUSDC"
	if cfg.Swap.IsBuy() {
		view.Direction = "buy SOL with devUSDC"
		view.InputPerAction = formatUnits(cfg.Swap.InputTokenAmount, 6) + " devUSDC"
		view.DailyCap = formatUnits(cfg.Swap.DailyInputTokenCap, 6) + " devUSDC"
	} else {
		view.InputPerAction = formatUnits(cfg.Swap.InputLamports, 9) + " SOL"
		view.DailyCap = formatUnits(cfg.Swap.DailyDebitCapLamports, 9) + " SOL"
	}
	view.FundedTradesPerDay = cfg.Swap.FundedTradesPerDay()
	view.MaxFee = formatUnits(cfg.Swap.MaxFeeLamports, 9) + " SOL"
	if trigger := cfg.Swap.PriceTrigger; trigger != nil {
		view.TriggerDirection = string(trigger.Direction)
		view.TriggerUSD = "$" + formatUnits(trigger.ThresholdMicros, 6)
	}
	var live bool
	view.ControlMode, view.ControlGrant, live = controlGrantAt(cfg.Control.StatePath)
	if view.ControlGrant != "" && !live {
		// The sweep row says this; the swap row dropping it let one screen call
		// the same spent grant both enabled and unable to act, depending on
		// which line the operator read.
		view.ControlGrant += " (cannot act)"
	}
	return view
}

func strategyShow(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("strategy show", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "print the view as JSON")
	swapConfig := flags.String("config", "", "swap config path (default: the setup recorded by the wizard)")
	sweepConfig := flags.String("sweep-config", "", "sweep config path (default ~/.mithril-agent/sweep/config.json)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, strategyUsage)
			return writeErr
		}
		return err
	}

	var view strategyView

	for _, leg := range tradingLegsToShow(*swapConfig) {
		cfg, err := readSwapConfig(leg.path)
		if err != nil || cfg.Swap == nil {
			continue
		}
		view.Legs = append(view.Legs, buildSwapLegView(leg.leg, leg.path, cfg))
		if view.Alerts == nil && !cfg.Alerts.empty() {
			alerts := cfg.Alerts
			view.Alerts = &alerts
		}
	}

	sweepPath := *sweepConfig
	if sweepPath == "" {
		// A strategy records where its sweep actually lives. Falling straight to
		// the fixed default meant `setup strategy --dir elsewhere` wrote a sweep
		// this screen could never see, while claiming to show everything
		// configured.
		if strategy, _ := discoverStrategy(); strategy.sweep != "" {
			sweepPath = strategy.sweep
		}
	}
	if sweepPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			sweepPath = filepath.Join(home, ".mithril-agent", "sweep", "config.json")
		}
	}
	if sweepPath != "" {
		if cfg, err := readConfig(sweepPath); err == nil && cfg.hasLegacyProfile() &&
			cfg.Profile.Validate() == nil {
			view.SweepConfigPath = sweepPath
			sweep := &sweepStrategyView{
				Destination: cfg.Profile.Destination,
				Floor:       formatUnits(cfg.Profile.ReserveLamports, 9) + " SOL",
				MaxPerSweep: formatUnits(cfg.Profile.MaxTransferLamports, 9) + " SOL",
				DailyCap:    formatUnits(cfg.Profile.DailyCapLamports, 9) + " SOL",
				ControlMode: sweepControlDescription(cfg.Control.StatePath),
			}
			anchor := time.Unix(cfg.Profile.ScheduleAnchorUnix, 0).UTC()
			sweep.ActiveAfter = anchor.Format(time.RFC3339)
			// Re-verify the recorded proof on every show. A registration
			// that no longer verifies is a finding, not a display detail.
			if proof, err := readDestinationProof(filepath.Dir(sweepPath)); err == nil {
				sweep.ProvenAt = proof.IssuedAt
				sweep.ProofValid = verifySweepDestinationProof(
					proof.AgentAccount, proof.Destination,
					proof.Nonce, proof.IssuedAt, proof.SignatureBase58,
				) == nil && proof.AgentAccount == cfg.Profile.Source &&
					proof.Destination == cfg.Profile.Destination
			}
			view.Sweep = sweep
		}
	}

	if *asJSON {
		encoded, err := json.MarshalIndent(view, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, string(encoded))
		return err
	}
	return renderStrategy(output, view)
}

func renderStrategy(output io.Writer, view strategyView) error {
	w := func(format string, args ...any) { _, _ = fmt.Fprintf(output, format, args...) }
	w("STRATEGY\n")
	if len(view.Legs) == 0 && view.Sweep == nil {
		// Every other strategy command names 'setup strategy'. Sending the
		// operator to the single-leg wizard from the screen that shows a whole
		// strategy is how someone ends up with one leg and no idea why.
		w("  nothing configured yet — run: mithril-agent setup strategy\n")
		return nil
	}
	for _, swap := range view.Legs {
		// The role goes in the heading. Two TRADING blocks that differ only by a
		// direction line four rows down is not a screen anybody reads correctly.
		heading := "TRADING"
		if swap.Leg != "" {
			heading = "TRADING · " + strings.ToUpper(swap.Leg) + " LEG"
		}
		// The grant was computed with its "(cannot act)" note and then never
		// printed, so a leg whose trades were spent read as plain "devnet_enabled"
		// while the runner refused it every ten seconds. An operator checking
		// before going away saw enabled and believed it would trade.
		w("\n%s (%s)  control: %s\n", heading, swap.Profile,
			orUnknown(joinControl(swap.ControlMode, swap.ControlGrant)))
		w("  direction  %s\n", swap.Direction)
		if swap.TriggerUSD != "" {
			w("  trigger    %s at %s\n", swap.TriggerDirection, swap.TriggerUSD)
		} else {
			w("  trigger    none (acts whenever enabled)\n")
		}
		w("  caps       %s per action · %s daily · fee %s\n",
			swap.InputPerAction, swap.DailyCap, swap.MaxFee)
		// The cap in trades, not just in SOL. Left as a raw amount, the one
		// number that decides whether an unattended strategy survives its first
		// trade of the day was something the operator had to divide out by hand.
		w("  funds      %d trade(s) per day; the cap resets at 00:00 UTC\n",
			swap.FundedTradesPerDay)
		if swap.ControlGrant != "" {
			w("  authority  %s\n", swap.ControlGrant)
		}
		w("  stop with  mithril-agent swap stop --config %s --reason TEXT\n", swap.ConfigPath)
		w("  config     %s\n", swap.ConfigPath)
	}
	if sweep := view.Sweep; sweep != nil {
		w("\nSWEEP  control: %s\n", orUnknown(sweep.ControlMode))
		proof := "NOT VERIFIED — re-run setup sweep"
		if sweep.ProofValid {
			proof = "proven " + sweep.ProvenAt
		}
		w("  to         %s (%s)\n", sweep.Destination, proof)
		w("  active     from %s\n", sweep.ActiveAfter)
		w("  bounds     keep %s · at most %s per sweep · %s daily\n",
			sweep.Floor, sweep.MaxPerSweep, sweep.DailyCap)
		w("  config     %s\n", view.SweepConfigPath)
	}
	w("\nALERTS (notify only — cannot trade, cannot move funds)\n")
	if view.Alerts == nil {
		w("  none — add one: mithril-agent strategy alerts set --price-above USD\n")
	} else {
		alerts := view.Alerts
		if alerts.PriceAboveMicroUSD != 0 {
			w("  price  >= $%s\n", formatUnits(alerts.PriceAboveMicroUSD, 6))
		}
		if alerts.PriceBelowMicroUSD != 0 {
			w("  price  <= $%s\n", formatUnits(alerts.PriceBelowMicroUSD, 6))
		}
		if alerts.BalanceAboveLamports != 0 {
			w("  balance >= %s SOL\n", formatUnits(alerts.BalanceAboveLamports, 9))
		}
		if alerts.BalanceBelowLamports != 0 {
			w("  balance <= %s SOL\n", formatUnits(alerts.BalanceBelowLamports, 9))
		}
		w("  delivery relies on the deployed Prometheus alerting stack; edits apply next cycle\n")
	}
	w("\nCaps, triggers, and the sweep floor are part of the signed profile: change them\nwith setup. Alert edits above are live.\n")
	return nil
}

// joinControl renders a mode and its grant the way the sweep row already does,
// so both rows describe the same document the same way.
func joinControl(mode, grant string) string {
	if mode == "" || grant == "" {
		return mode
	}
	return mode + " · " + grant
}

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

// sweepControlDescription reports the sweep's control state the way the swap row
// already does. Printing the bare mode called a spent or expired grant enabled,
// so `strategy show` and `mithril-agent status` disagreed about one file.
func sweepControlDescription(statePath string) string {
	mode, grant, live := controlGrantAt(statePath)
	switch {
	case mode == "":
		return ""
	case grant == "":
		return mode
	case live:
		return mode + " · " + grant
	default:
		return mode + " · " + grant + " (cannot act)"
	}
}

// controlGrantAt returns the control mode, a human description of the grant,
// and whether that grant can still act. A standing authority nobody can see the
// end of is what scoped delegation exists to avoid, so the expiry and remaining
// actions are read from the same document as the mode.
//
// The third value is STRUCTURAL on purpose: deciding liveness by matching the
// description's wording made the answer hostage to its phrasing, and a document
// with no expiry at all read as live. It is also read without a lock — the show
// screen is read-only, and a racing writer at worst dates the answer.
func controlGrantAt(statePath string) (mode string, grant string, live bool) {
	if statePath == "" {
		return "", "", false
	}
	raw, err := os.ReadFile(statePath)
	if err != nil || len(raw) > 1<<20 {
		return "", "", false
	}
	var state struct {
		Mode             string    `json:"mode"`
		ExpiresAt        time.Time `json:"expires_at"`
		RemainingActions uint32    `json:"remaining_actions"`
	}
	if json.Unmarshal(raw, &state) != nil {
		return "", "", false
	}
	return describeControlGrant(
		state.Mode, state.ExpiresAt, state.RemainingActions, time.Now().UTC())
}

// describeControlGrant renders the bounded control projection shared by the
// config and status-socket MCP providers. Keeping this pure prevents the two
// read-only surfaces from disagreeing about whether the same grant is live.
func describeControlGrant(
	mode string,
	expiresAt time.Time,
	remainingActions uint32,
	now time.Time,
) (string, string, bool) {
	// A grant can only act while its clock runs AND actions remain. An absent
	// expiry is not "forever": control validates enabled documents as expiring.
	live := mode == control.ModeDevnetEnabled && !expiresAt.IsZero() &&
		expiresAt.After(now) && remainingActions > 0
	if expiresAt.IsZero() {
		return mode, "", live
	}
	remaining := expiresAt.Sub(now).Round(time.Minute)
	if remaining <= 0 {
		return mode, "expired " + expiresAt.UTC().Format(time.RFC3339), live
	}
	return mode, fmt.Sprintf(
		"%d action(s) left, expires %s (in %s)",
		remainingActions, expiresAt.UTC().Format(time.RFC3339), remaining,
	), live
}

func readDestinationProof(dir string) (destinationProof, error) {
	raw, err := securefile.ReadPrivate(filepath.Join(dir, "destination-proof.json"), 1<<20)
	if err != nil {
		return destinationProof{}, err
	}
	var proof destinationProof
	if err := json.Unmarshal(raw, &proof); err != nil {
		return destinationProof{}, errors.New("destination proof file is unreadable")
	}
	return proof, nil
}

// strategyAlerts edits ONLY the alerts section, atomically, after validating
// the whole resulting config. The file is re-marshaled from the strictly
// decoded struct, so unknown fields were already refused at read time and a
// concurrent hand-edit is the only thing that can be lost.
func strategyAlerts(args []string, output io.Writer) error {
	for _, arg := range args {
		if arg == "help" || arg == "-h" || arg == "--help" {
			_, err := fmt.Fprintln(output, strategyUsage)
			return err
		}
	}
	if len(args) == 0 {
		return errors.New("strategy alerts requires set or clear")
	}
	// --config first, before the subcommand's own flags, so alerts can be edited
	// on a leg that `swap setup` built rather than only on the wizard's one.
	var explicit string
	if args[0] == "--config" || strings.HasPrefix(args[0], "--config=") {
		value, rest, err := takeConfigFlag(args)
		if err != nil {
			return err
		}
		explicit, args = value, rest
		if len(args) == 0 {
			return errors.New("strategy alerts requires set or clear")
		}
	}
	pointer := firstNonEmpty(explicit, discoverCurrentConfig())
	if pointer == "" {
		return errors.New("no configured setup was found; run mithril-agent setup first, " +
			"or name one with --config PATH")
	}
	cfg, err := readSwapConfig(pointer)
	if err != nil {
		return err
	}
	switch args[0] {
	case "set":
		flags := flag.NewFlagSet("strategy alerts set", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		priceAbove := flags.String("price-above", "", "notify when SOL/USD reaches this")
		priceBelow := flags.String("price-below", "", "notify when SOL/USD falls to this")
		balanceAbove := flags.String("balance-above", "", "notify when the balance reaches this, in SOL")
		balanceBelow := flags.String("balance-below", "", "notify when the balance falls to this, in SOL")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *priceAbove == "" && *priceBelow == "" && *balanceAbove == "" && *balanceBelow == "" {
			return errors.New("strategy alerts set requires at least one threshold flag")
		}
		if *priceAbove != "" {
			if cfg.Alerts.PriceAboveMicroUSD, err = parseUSDThreshold(*priceAbove, "price-above alert"); err != nil {
				return err
			}
		}
		if *priceBelow != "" {
			if cfg.Alerts.PriceBelowMicroUSD, err = parseUSDThreshold(*priceBelow, "price-below alert"); err != nil {
				return err
			}
		}
		if *balanceAbove != "" {
			if cfg.Alerts.BalanceAboveLamports, err = parseDecimalUnits9(*balanceAbove, "balance-above"); err != nil {
				return err
			}
		}
		if *balanceBelow != "" {
			if cfg.Alerts.BalanceBelowLamports, err = parseDecimalUnits9(*balanceBelow, "balance-below"); err != nil {
				return err
			}
		}
	case "clear":
		cfg.Alerts = alertsConfig{}
	default:
		return fmt.Errorf("unknown alerts command %q", args[0])
	}
	if err := cfg.Alerts.validate(cfg.Swap); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return errors.New("encode the updated config")
	}
	encoded = append(encoded, '\n')
	if err := securefile.ReplacePrivate(pointer, encoded, maxSetupFileBytes); err != nil {
		return errors.New("write the updated config")
	}
	_, err = fmt.Fprintf(output,
		"Alerts updated in %s.\nThe runner picks this up on its next cycle; no restart needed.\n",
		pointer)
	return err
}

// takeConfigFlag pulls a leading --config off the argument list. The alerts
// subcommands parse their own flags after the verb, so the config has to be
// consumed before the verb is read.
func takeConfigFlag(args []string) (string, []string, error) {
	if value, found := strings.CutPrefix(args[0], "--config="); found {
		if value == "" {
			return "", nil, errors.New("--config needs a path")
		}
		return value, args[1:], nil
	}
	if len(args) < 2 || args[1] == "" {
		return "", nil, errors.New("--config needs a path")
	}
	return args[1], args[2:], nil
}
