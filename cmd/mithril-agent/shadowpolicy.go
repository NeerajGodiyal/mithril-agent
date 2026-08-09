package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

// A shadow policy needs the SHA-256 identity of each price source, and nobody
// can be expected to produce a 64-character digest by hand. Refusing to run
// without one is right — a policy that names the wrong source is measuring
// something other than what its author believes. But making a person type it is
// not, when the program already knows both values.
//
// This writes a complete, valid policy from a threshold and an amount, filling
// in every field a person could not reasonably supply.
const shadowPolicyUsage = `Usage: mithril-agent shadow policy --out PATH [options]

Writes a complete shadow policy. The price-source identities are filled in for
you; everything else has a working default.

  --out PATH        where to write the policy
  --cluster NAME    mainnet-beta (default) or devnet
  --sell-at-usd N   sell when SOL reaches this price
  --buy-at-usd N    buy when SOL falls to this price (use one or the other)
  --amount N        how much to trade per action, in the input asset's
                    base units (default 1000000)
  --observe ADDR    the address to quote against; watch-only, never signed for
  --tick-seconds N  how often to look at the market (default 60)`

func runShadowPolicy(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow policy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	outPath := flags.String("out", "", "where to write the policy")
	cluster := flags.String("cluster", shadow.Mainnet, "mainnet-beta or devnet")
	sellAt := flags.String("sell-at-usd", "", "sell when SOL reaches this price")
	buyAt := flags.String("buy-at-usd", "", "buy when SOL falls to this price")
	amount := flags.Uint64("amount", 1_000_000, "trade size in input base units")
	observe := flags.String("observe", "", "address to quote against (watch-only)")
	tickSeconds := flags.Uint64("tick-seconds", 60, "seconds between observations")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowPolicyUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("shadow policy takes no positional arguments")
	}
	if *outPath == "" {
		return errors.New("shadow policy requires --out PATH, where to write the policy")
	}
	if !filepath.IsAbs(*outPath) || filepath.Clean(*outPath) != *outPath {
		return errors.New("--out must be an absolute, clean path: " + *outPath)
	}
	if *observe == "" {
		return errors.New("shadow policy requires --observe ADDRESS to quote against")
	}
	if (*sellAt == "") == (*buyAt == "") {
		return errors.New("give exactly one of --sell-at-usd or --buy-at-usd")
	}

	policy, err := buildShadowPolicy(*cluster, *sellAt, *buyAt, *amount, *observe, *tickSeconds)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	if err := securefile.ReplacePrivate(*outPath, append(encoded, '\n'), maxInputBytes); err != nil {
		return errors.New("could not write the policy")
	}
	_, err = fmt.Fprintf(output,
		"Wrote %s\n\nRun it with:\n  mithril-agent shadow run --policy %s --dir DIR \\\n"+
			"    --node-command PATH --quote-script PATH --pool ADDRESS --input-mint ADDRESS\n\n"+
			"It reads the market and writes down what the rule would have done.\n"+
			"It holds no key and cannot sign, submit, or spend anything.\n",
		*outPath, *outPath)
	return err
}

// buildShadowPolicy fills in every field a person could not reasonably supply,
// and validates the result so a bad threshold fails here rather than at the
// start of a run somebody expected to leave going.
func buildShadowPolicy(
	cluster, sellAt, buyAt string,
	amount uint64,
	observe string,
	tickSeconds uint64,
) (shadow.Policy, error) {
	direction, threshold := pricetrigger.SellAtOrAbove, sellAt
	if sellAt == "" {
		direction, threshold = pricetrigger.BuyAtOrBelow, buyAt
	}
	micros, err := parseUSDThreshold(threshold, "price threshold")
	if err != nil {
		return shadow.Policy{}, err
	}
	// Selling spends SOL for a dollar stable; buying does the reverse, so the
	// decimals swap with the direction.
	inputDecimals, outputDecimals := uint8(9), uint8(6)
	if direction == pricetrigger.BuyAtOrBelow {
		inputDecimals, outputDecimals = 6, 9
	}
	policy := shadow.Policy{
		Version: shadow.Version, Cluster: cluster,
		Trigger: pricetrigger.Policy{
			Version: pricetrigger.Version, Feed: pricetrigger.FeedSOLUSD,
			Direction: direction, ThresholdMicros: micros,
			MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90,
			MaxDeviationBPS: 200, MaxConfidenceBPS: 200,
			// The two identities nobody can type. The sponsored on-chain feed
			// read through a node, cross-checked against a public exchange:
			// neither needs a paid subscription.
			PrimarySourceSHA256:   pricesource.PythPushIdentitySHA256(),
			SecondarySourceSHA256: pricesource.CoinbaseIdentitySHA256(),
		},
		Observe:     observe,
		InputAmount: amount, InputDecimals: inputDecimals, OutputDecimals: outputDecimals,
		SlippageBPS: 100, FeeLamports: 5_000,
		TickSeconds: tickSeconds, SettleSeconds: 60,
		// A notional opening position, so the result has something to be
		// measured against and a hold benchmark to be compared with.
		StartingInputUnits: 1_000_000_000,
	}
	if err := policy.Validate(); err != nil {
		return shadow.Policy{}, err
	}
	return policy, nil
}
