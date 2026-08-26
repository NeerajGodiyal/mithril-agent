package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
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
  --buy-at-usd N    buy when SOL falls to this price
                    give both for a repeating sell-then-buy-back round trip
  --amount N        how much to trade per action, in the input asset's
                    base units (default 1000000)
  --slippage-bps N  conservative fill allowance (default 100)
  --fee-lamports N  fee charged to every hypothetical fill (default 5000)
  --observe ADDR    the address to quote against; watch-only, never signed for
  --tick-seconds N  how often to look at the market (default 60)

Devnet Orca only:
  --pool ADDRESS        pool whose executable prices will be measured
  --input-mint ADDRESS  mint spent by the first leg
  --output-mint ADDRESS mint received by the first leg`

func runShadowPolicy(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow policy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	outPath := flags.String("out", "", "where to write the policy")
	cluster := flags.String("cluster", shadow.Mainnet, "mainnet-beta or devnet")
	sellAt := flags.String("sell-at-usd", "", "sell when SOL reaches this price")
	buyAt := flags.String("buy-at-usd", "", "buy when SOL falls to this price")
	amount := flags.Uint64("amount", 1_000_000, "trade size in input base units")
	slippageBPS := flags.Uint("slippage-bps", 100, "conservative fill allowance")
	feeLamports := flags.Uint64("fee-lamports", 5_000, "fee charged to each fill")
	observe := flags.String("observe", "", "address to quote against (watch-only)")
	tickSeconds := flags.Uint64("tick-seconds", 60, "seconds between observations")
	pool := flags.String("pool", "", "Devnet Orca pool")
	inputMint := flags.String("input-mint", "", "Devnet first-leg input mint")
	outputMint := flags.String("output-mint", "", "Devnet first-leg output mint")
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
	if *slippageBPS == 0 || *slippageBPS > 500 {
		return errors.New("--slippage-bps must be between 1 and 500")
	}
	if *sellAt == "" && *buyAt == "" {
		return errors.New("give --sell-at-usd, --buy-at-usd, or both for a round trip")
	}

	policy, err := buildShadowPolicy(
		*cluster, *sellAt, *buyAt, *amount, uint16(*slippageBPS), *feeLamports,
		*observe, *tickSeconds,
		*pool, *inputMint, *outputMint,
	)
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
	_, err = fmt.Fprintf(output, "Wrote %s\n\n%s\n\n"+
		"It reads the market and writes down what the rule would have done.\n"+
		"It holds no wallet signing key and cannot sign, submit, or spend anything.\n",
		*outPath, shadowRunHint(policy, *outPath))
	return err
}

func shadowRunHint(policy shadow.Policy, path string) string {
	if policy.Cluster == shadow.Mainnet {
		return fmt.Sprintf("Run it with:\n  mithril-agent shadow run --policy %s --dir DIR\n\n"+
			"After N complete UTC days, verify them with:\n  mithril-agent shadow review --policy %s --dir DIR --days N\n\n"+
			"Keyless Jupiter access needs no account and is suitable for testing. For continuous production operation, Jupiter recommends a free or paid API key; keep MITHRIL_AGENT_JUPITER_API_KEY scoped to this read-only service.",
			path,
			path)
	}
	return fmt.Sprintf("Run the Devnet Orca route stored in this policy:\n"+
		"  mithril-agent shadow run --policy %s --dir DIR \\\n"+
		"    --node-command PATH --quote-script PATH", path)
}

// buildShadowPolicy fills in every field a person could not reasonably supply,
// and validates the result so a bad threshold fails here rather than at the
// start of a run somebody expected to leave going.
func buildShadowPolicy(
	cluster, sellAt, buyAt string,
	amount uint64, slippageBPS uint16, feeLamports uint64,
	observe string,
	tickSeconds uint64,
	pool, inputMint, outputMint string,
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
	startingInput := max(uint64(1_000_000_000), amount)
	startingOutput := uint64(0)
	if direction == pricetrigger.SellAtOrAbove {
		if amount > math.MaxUint64-feeLamports {
			return shadow.Policy{}, errors.New("shadow trade amount is too large")
		}
		startingInput = max(startingInput, amount+feeLamports)
	} else {
		// The notional book pays Solana fees in SOL even when its first trade
		// spends USDC. Carry a fee reserve so a tiny valid buy is not refused for
		// a bookkeeping balance the generator itself omitted.
		startingOutput = feeLamports
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
		SlippageBPS: slippageBPS, FeeLamports: feeLamports,
		TickSeconds: tickSeconds, SettleSeconds: 60,
		// A notional opening position, so the result has something to be
		// measured against and a hold benchmark to be compared with.
		StartingInputUnits: startingInput, StartingOutputUnits: startingOutput,
	}
	if cluster == shadow.Mainnet {
		if pool != "" || inputMint != "" || outputMint != "" {
			return shadow.Policy{}, errors.New("Mainnet shadow policy uses the fixed Jupiter SOL/USDC route; omit Devnet route flags")
		}
		policy.QuoteRoute = shadow.MainnetQuoteRoute(direction == pricetrigger.SellAtOrAbove)
		policy.QuotePeg = &pricetrigger.BandPolicy{
			Version: pricetrigger.Version, Feed: pricetrigger.FeedUSDCUSD,
			MinimumMicros: pricetrigger.USDCBandMinimumMicros,
			MaximumMicros: pricetrigger.USDCBandMaximumMicros,
			MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90,
			MaxDeviationBPS: 100, MaxConfidenceBPS: 100,
			PrimarySourceSHA256:   pricesource.PythPushUSDCIdentitySHA256(),
			SecondarySourceSHA256: pricesource.KrakenIdentitySHA256(),
		}
	} else {
		if pool == "" || inputMint == "" || outputMint == "" {
			return shadow.Policy{}, errors.New("Devnet shadow policy requires --pool, --input-mint, and --output-mint")
		}
		policy.QuoteRoute = shadow.QuoteRoute{
			Provider: shadow.QuoteOrca, Pool: pool,
			InputMint: inputMint, OutputMint: outputMint,
		}
	}
	if sellAt != "" && buyAt != "" {
		buyMicros, parseErr := parseUSDThreshold(buyAt, "buy-back price threshold")
		if parseErr != nil {
			return shadow.Policy{}, parseErr
		}
		returnTrigger := policy.Trigger
		returnTrigger.Direction = pricetrigger.BuyAtOrBelow
		returnTrigger.ThresholdMicros = buyMicros
		policy.ReturnTrigger = &returnTrigger
	}
	if err := policy.Validate(); err != nil {
		return shadow.Policy{}, err
	}
	return policy, nil
}
