package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
  mithril-agent strategy [show] [--json]     one screen: caps, triggers, alerts, sweep
  mithril-agent strategy alerts set [--price-above USD] [--price-below USD]
                                    [--balance-above SOL] [--balance-below SOL]
  mithril-agent strategy alerts clear        remove every alert threshold

Alerts are notify-only: they message the operator through the deployed
alerting stack and can never trade or move funds. Edits take effect on the
runner's next cycle without a restart.

Caps, price triggers, and the sweep floor are part of the signed profile;
change them by running setup again. The sweep destination additionally
requires a fresh proof-of-control ceremony (setup sweep).`

func runStrategy(args []string, output io.Writer) error {
	if len(args) == 0 {
		return strategyShow(args, output)
	}
	switch args[0] {
	case "show":
		return strategyShow(args[1:], output)
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
type strategyView struct {
	SwapConfigPath  string             `json:"swap_config_path,omitempty"`
	Swap            *swapStrategyView  `json:"swap,omitempty"`
	Alerts          *alertsConfig      `json:"alerts,omitempty"`
	SweepConfigPath string             `json:"sweep_config_path,omitempty"`
	Sweep           *sweepStrategyView `json:"sweep,omitempty"`
}

type swapStrategyView struct {
	Profile          string `json:"profile"`
	Direction        string `json:"direction"`
	InputPerAction   string `json:"input_per_action"`
	DailyCap         string `json:"daily_cap"`
	MaxFee           string `json:"max_fee"`
	TriggerDirection string `json:"trigger_direction,omitempty"`
	TriggerUSD       string `json:"trigger_usd,omitempty"`
	ControlMode      string `json:"control_mode,omitempty"`
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

func strategyShow(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("strategy show", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "print the view as JSON")
	sweepConfig := flags.String("sweep-config", "", "sweep config path (default ~/.mithril-agent/sweep/config.json)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, strategyUsage)
			return writeErr
		}
		return err
	}

	var view strategyView

	if pointer := discoverCurrentConfig(); pointer != "" {
		if cfg, err := readSwapConfig(pointer); err == nil && cfg.Swap != nil {
			view.SwapConfigPath = pointer
			swap := &swapStrategyView{Profile: cfg.Swap.Name, Direction: "sell SOL for devUSDC"}
			if cfg.Swap.IsBuy() {
				swap.Direction = "buy SOL with devUSDC"
				swap.InputPerAction = formatUnits(cfg.Swap.InputTokenAmount, 6) + " devUSDC"
				swap.DailyCap = formatUnits(cfg.Swap.DailyInputTokenCap, 6) + " devUSDC"
			} else {
				swap.InputPerAction = formatUnits(cfg.Swap.InputLamports, 9) + " SOL"
				swap.DailyCap = formatUnits(cfg.Swap.DailyDebitCapLamports, 9) + " SOL"
			}
			swap.MaxFee = formatUnits(cfg.Swap.MaxFeeLamports, 9) + " SOL"
			if trigger := cfg.Swap.PriceTrigger; trigger != nil {
				swap.TriggerDirection = string(trigger.Direction)
				swap.TriggerUSD = "$" + formatUnits(trigger.ThresholdMicros, 6)
			}
			swap.ControlMode = controlModeAt(cfg.Control.StatePath)
			view.Swap = swap
			if !cfg.Alerts.empty() {
				alerts := cfg.Alerts
				view.Alerts = &alerts
			}
		}
	}

	sweepPath := *sweepConfig
	if sweepPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			sweepPath = filepath.Join(home, ".mithril-agent", "sweep", "config.json")
		}
	}
	if sweepPath != "" {
		if cfg, err := readConfig(sweepPath); err == nil && cfg.hasLegacyProfile() {
			view.SweepConfigPath = sweepPath
			sweep := &sweepStrategyView{
				Destination: cfg.Profile.Destination,
				Floor:       formatUnits(cfg.Profile.ReserveLamports, 9) + " SOL",
				MaxPerSweep: formatUnits(cfg.Profile.MaxTransferLamports, 9) + " SOL",
				DailyCap:    formatUnits(cfg.Profile.DailyCapLamports, 9) + " SOL",
				ControlMode: controlModeAt(cfg.Control.StatePath),
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
				) == nil && proof.Destination == cfg.Profile.Destination
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
	if view.Swap == nil && view.Sweep == nil {
		w("  nothing configured yet — run: mithril-agent setup\n")
		return nil
	}
	if swap := view.Swap; swap != nil {
		w("\nTRADING (%s)  control: %s\n", swap.Profile, orUnknown(swap.ControlMode))
		w("  direction  %s\n", swap.Direction)
		if swap.TriggerUSD != "" {
			w("  trigger    %s at %s\n", swap.TriggerDirection, swap.TriggerUSD)
		} else {
			w("  trigger    none (acts whenever enabled)\n")
		}
		w("  caps       %s per action · %s daily · fee %s\n",
			swap.InputPerAction, swap.DailyCap, swap.MaxFee)
		w("  config     %s\n", view.SwapConfigPath)
	}
	if sweep := view.Sweep; sweep != nil {
		w("\nSWEEP (treasury_sweep_v1)  control: %s\n", orUnknown(sweep.ControlMode))
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

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

// controlModeAt reads the control mode without taking any lock; the show
// screen is read-only and a racing writer at worst dates the answer.
func controlModeAt(statePath string) string {
	if statePath == "" {
		return ""
	}
	raw, err := os.ReadFile(statePath)
	if err != nil || len(raw) > 1<<20 {
		return ""
	}
	var state struct {
		Mode string `json:"mode"`
	}
	if json.Unmarshal(raw, &state) != nil {
		return ""
	}
	return state.Mode
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
	if len(args) == 0 {
		return errors.New("strategy alerts requires set or clear")
	}
	pointer := discoverCurrentConfig()
	if pointer == "" {
		return errors.New("no configured setup was found; run mithril-agent setup first")
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
			if cfg.Alerts.PriceAboveMicroUSD, err = parseUSDThreshold(*priceAbove); err != nil {
				return err
			}
		}
		if *priceBelow != "" {
			if cfg.Alerts.PriceBelowMicroUSD, err = parseUSDThreshold(*priceBelow); err != nil {
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
