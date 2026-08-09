package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
)

// A round trip is two ordinary swap setups and an optional sweep, named
// together. It is deliberately NOT one profile holding both directions: the
// signer policy binds exactly one route, and a single grant authorising two
// transaction shapes would double the blast radius of any validation gap. Two
// setups keep two independent signer policies, two ledgers and two sets of
// caps, and the only thing added here is the knowledge that they are a pair.
//
// Nothing in this file arms anything. It reports what the wallet holds and
// which leg that makes actionable; the operator still runs `swap enable`.
const roundTripReadTimeout = 20 * time.Second

// legState is one leg as the operator needs to see it: what it would trade,
// whether the wallet can currently fund it, and whether it is armed.
type legState struct {
	Direction   string `json:"direction"`
	ConfigPath  string `json:"config_path"`
	Input       string `json:"input"`
	MinimumOut  string `json:"minimum_output"`
	Funded      bool   `json:"funded"`
	Shortfall   string `json:"shortfall,omitempty"`
	ControlMode string `json:"control_mode"`
	Grant       string `json:"grant,omitempty"`
	Armed       bool   `json:"armed"`
}

type roundTripState struct {
	SOL         string    `json:"sol_balance"`
	DevUSDC     string    `json:"devusdc_balance"`
	Sell        *legState `json:"sell,omitempty"`
	Buy         *legState `json:"buy,omitempty"`
	Next        string    `json:"next"`
	Warnings    []string  `json:"warnings,omitempty"`
	SweepArmed  *bool     `json:"sweep_armed,omitempty"`
	SweepConfig string    `json:"sweep_config,omitempty"`
}

// evaluateRoundTrip derives position from BALANCES rather than from a stored
// cursor. A cursor can disagree with the chain after a crash, a manual trade or
// a partial fill; the wallet either holds the input for a leg or it does not.
func evaluateRoundTrip(
	ctx context.Context, sellPath, buyPath, sweepPath string,
) (roundTripState, error) {
	sellCfg, sellErr := readSwapConfig(sellPath)
	buyCfg, buyErr := readSwapConfig(buyPath)
	if sellErr != nil {
		return roundTripState{}, fmt.Errorf("sell leg: %w", sellErr)
	}
	if buyErr != nil {
		return roundTripState{}, fmt.Errorf("buy leg: %w", buyErr)
	}
	if sellCfg.Swap.IsBuy() {
		return roundTripState{}, errors.New("the sell leg config is a buy profile")
	}
	if !buyCfg.Swap.IsBuy() {
		return roundTripState{}, errors.New("the buy leg config is a sell profile")
	}
	owner := sellCfg.Swap.Owner()
	if owner != buyCfg.Swap.Owner() {
		return roundTripState{}, errors.New("round trip legs must share one wallet")
	}

	lamports, err := walletLamports(ctx, owner)
	if err != nil {
		return roundTripState{}, err
	}
	tokens, err := walletTokenAmount(ctx, owner, orcaswap.DevnetUSDCMint)
	if err != nil {
		return roundTripState{}, err
	}
	paths := roundTripPaths{sell: sellPath, buy: buyPath, sweep: sweepPath}
	return deriveRoundTrip(sellCfg, buyCfg, paths, lamports, tokens), nil
}

// deriveRoundTrip is the judgement, split from the chain reads so it can be
// tested at all: the reads post to a deliberately hardcoded endpoint, which
// left every branch below at zero coverage and three real bugs unpinned.
type roundTripPaths struct{ sell, buy, sweep string }

func deriveRoundTrip(
	sellCfg, buyCfg config, paths roundTripPaths, lamports, tokens uint64,
) roundTripState {
	state := roundTripState{SweepConfig: paths.sweep}
	state.SOL = formatUnits(lamports, 9) + " SOL"
	state.DevUSDC = formatUnits(tokens, 6) + " devUSDC"

	// A sell spends SOL, so it needs the wallet requirement its own profile
	// computes — reserve, fee and rent included, not just the input.
	sellNeed := sellCfg.Swap.WalletRequirementLamports()
	state.Sell = &legState{
		Direction: "sell", ConfigPath: paths.sell,
		Input:      formatUnits(sellCfg.Swap.InputLamports, 9) + " SOL",
		MinimumOut: formatUnits(sellCfg.Swap.MinimumOutput(), 6) + " devUSDC",
		Funded:     lamports >= sellNeed,
	}
	if lamports < sellNeed {
		state.Sell.Shortfall = formatUnits(sellNeed-lamports, 9) + " SOL"
	}

	buyNeed := buyCfg.Swap.InputTokenAmount
	state.Buy = &legState{
		Direction: "buy", ConfigPath: paths.buy,
		Input:      formatUnits(buyNeed, 6) + " devUSDC",
		MinimumOut: formatUnits(buyCfg.Swap.MinimumOutput(), 9) + " SOL",
		Funded:     tokens >= buyNeed,
	}
	if tokens < buyNeed {
		state.Buy.Shortfall = formatUnits(buyNeed-tokens, 6) + " devUSDC"
	}
	// The buy also needs native SOL for its fee and the temporary account rent,
	// which is easy to miss because its INPUT is a token: a wallet full of
	// devUSDC and empty of SOL cannot buy.
	if buyNative := buyCfg.Swap.WalletRequirementLamports(); lamports < buyNative {
		state.Buy.Funded = false
		state.Buy.Shortfall = strings.TrimSpace(state.Buy.Shortfall + " " +
			formatUnits(buyNative-lamports, 9) + " SOL for fee and rent")
	}

	// "Armed" must mean the leg can actually act. A grant whose clock has run
	// out, or whose actions are spent, still reports mode devnet_enabled — so
	// reading the mode alone showed a dead grant as live and would have had an
	// operator waiting on a leg that could never fire.
	state.Sell.ControlMode, state.Sell.Grant, state.Sell.Armed = controlGrantAt(sellCfg.Control.StatePath)
	state.Buy.ControlMode, state.Buy.Grant, state.Buy.Armed = controlGrantAt(buyCfg.Control.StatePath)

	// Both legs armed against one wallet is the hazard this view exists to
	// catch: they race for the same lamports, and whichever loses reports a
	// failure the operator has to diagnose.
	if state.Sell.Armed && state.Buy.Armed {
		state.Warnings = append(state.Warnings,
			"both legs are armed against the same wallet; stop one before it races the other")
	}
	if paths.sweep != "" {
		sweepCfg, err := readConfig(paths.sweep)
		switch {
		case err != nil:
		case sweepCfg.Profile.Destination == "":
			// Nothing checked that the sweep leg held a SWEEP. Pointed at a swap
			// setup it read that setup's control state and reported it as the
			// sweep's, so an armed trade would have displayed as an armed sweep.
			err = errors.New("it is not a sweep profile")
		case sweepCfg.Profile.Source != sellCfg.Swap.Owner():
			// A sweep on ANOTHER wallet cannot take these lamports, and reporting
			// it here says the legs are protected by a sweep that is not watching
			// them — while this wallet's real sweep goes unmentioned.
			err = fmt.Errorf("it sweeps %s, not this wallet", sweepCfg.Profile.Source)
		}
		if err != nil {
			// Silently dropping the sweep would read as "no sweep configured",
			// which is a different and far more comforting fact than "the sweep
			// could not be used". The cause travels with it: "unreadable" and
			// "readable but the wrong file" need different fixes.
			state.Warnings = append(state.Warnings,
				"the sweep setup was not used: "+paths.sweep+": "+err.Error())
		} else {
			_, _, armed := controlGrantAt(sweepCfg.Control.StatePath)
			state.SweepArmed = &armed
		}
	}
	state.Next = nextLeg(state)
	return state
}

// nextLeg names the leg the wallet can actually fund. Holding both sides is
// normal mid-cycle, and it is not this command's place to guess which the
// operator wants; it says so instead of inventing an answer.
func nextLeg(state roundTripState) string {
	switch {
	case state.Sell.Funded && state.Buy.Funded:
		return "either: the wallet can fund both legs"
	case state.Sell.Funded:
		return "sell"
	case state.Buy.Funded:
		return "buy"
	default:
		return "neither: the wallet cannot fund either leg"
	}
}

func renderRoundTrip(output io.Writer, state roundTripState) error {
	lines := []string{
		"Wallet:     " + state.SOL + " · " + state.DevUSDC,
		"",
	}
	for _, leg := range []*legState{state.Sell, state.Buy} {
		if leg == nil {
			continue
		}
		status := "not funded"
		if leg.Funded {
			status = "fundable"
		}
		if leg.Armed {
			status += ", ARMED"
		}
		control := orUnknown(leg.ControlMode)
		if leg.Grant != "" {
			control += " · " + leg.Grant
		}
		lines = append(lines,
			fmt.Sprintf("%-5s %s -> at least %s", leg.Direction, leg.Input, leg.MinimumOut),
			fmt.Sprintf("      %s (%s)", status, control),
		)
		if leg.Shortfall != "" {
			lines = append(lines, "      short by "+leg.Shortfall)
		}
	}
	lines = append(lines, "", "Next: "+state.Next)
	if state.SweepArmed != nil {
		sweep := "idle"
		if *state.SweepArmed {
			sweep = "ARMED"
		}
		// Named here because this screen is where an operator looks after a sell
		// and asks why the proceeds are not being swept: they are devUSDC.
		lines = append(lines, "Sweep: "+sweep+" (native SOL only)")
	}
	for _, warning := range state.Warnings {
		lines = append(lines, "", "WARNING: "+warning)
	}
	_, err := fmt.Fprintln(output, strings.Join(lines, "\n"))
	return err
}

// strategyRoundTrip reports only. Arming stays a separate, deliberate act
// through `swap enable`, so reading the strategy can never start a trade.
func strategyRoundTrip(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("strategy round-trip", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sellPath := flags.String("sell-config", "", "the sell leg's config.json")
	buyPath := flags.String("buy-config", "", "the buy leg's config.json")
	sweepPath := flags.String("sweep-config", "", "optional sweep config.json")
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, strategyUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *sellPath == "" || *buyPath == "" {
		return errors.New("strategy round-trip requires --sell-config and --buy-config")
	}
	ctx, cancel := context.WithTimeout(context.Background(), roundTripReadTimeout)
	defer cancel()
	state, err := evaluateRoundTrip(ctx, *sellPath, *buyPath, *sweepPath)
	if err != nil {
		return err
	}
	if *asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(state)
	}
	return renderRoundTrip(output, state)
}
