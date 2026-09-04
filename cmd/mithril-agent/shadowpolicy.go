package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/marketadmission"
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
  --adaptive        derive actions from rolling trend, volatility, and range;
                    do not provide fixed buy or sell prices
  --market NAME     paper mandate market: SOL/USDC, JUP/USDC, WIF/USDC,
                    JTO/USDC, or PYTH/USDC
	--admission-artifact PATH
	                    qualified evidence for an admitted candidate market
	--admission-journal PATH
	                    exact journal bound by that artifact
	--provisional-artifact PATH
	                    six-hour paper-only evidence for a candidate market
	--provisional-journal PATH
	                    exact journal bound by that checkpoint
  --budget-sol N    total simulated SOL budget, including fees and setup rent
  --budget-usdc N   simulated USDC budget for non-SOL token markets
  --fee-reserve-sol N
                    simulated native reserve for token markets (default 0.080)
  --setup-rent-sol N
                    one-time native capital locked by token setup (default 0.003)
  --drawdown-stop-bps N
                    pause the daily paper strategy after this drawdown;
                    not a guaranteed maximum loss
  --sell-at-usd N   sell when SOL reaches this price
  --buy-at-usd N    buy when SOL falls to this price
                    give both for a repeating sell-then-buy-back round trip
  --amount N        initial lot in the input asset's base units; later
                    round-trip legs use simulated proceeds (default 1000000)
  --slippage-bps N  conservative fill allowance (default 100)
  --fee-lamports N  conservative recurring cost per attempt (default 5000;
                    paper mandates default 100000)
  --observe ADDR    the address to quote against; watch-only, never signed for
  --tick-seconds N  how often to look at the market (default 60)

Devnet Orca only:
  --pool ADDRESS        pool whose executable prices will be measured
  --input-mint ADDRESS  mint spent by the first leg
  --output-mint ADDRESS mint received by the first leg`

const (
	defaultPaperFeeLamports        = uint64(100_000)
	defaultPaperFeeReserveLamports = uint64(1_000_000) // 0.001 SOL
	defaultPaperMandateReserve     = uint64(4_000_000) // 0.004 SOL
	// 0.003 SOL setup rent plus 770 attempts at the conservative default fee
	// covers a full day even when every settled signal is submitted and refused.
	defaultTokenFeeReserveLamports = uint64(80_000_000) // 0.080 SOL
	defaultTokenSetupRentLamports  = uint64(3_000_000)  // 0.003 SOL
)

func runShadowPolicy(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow policy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	outPath := flags.String("out", "", "where to write the policy")
	cluster := flags.String("cluster", shadow.Mainnet, "mainnet-beta or devnet")
	adaptive := flags.Bool("adaptive", false, "use the adaptive paper strategy")
	market := flags.String("market", "", "paper mandate market")
	admissionArtifact := flags.String("admission-artifact", "", "qualified market evidence")
	admissionJournal := flags.String("admission-journal", "", "market evidence journal")
	provisionalArtifact := flags.String("provisional-artifact", "", "six-hour paper-only market evidence")
	provisionalJournal := flags.String("provisional-journal", "", "provisional market evidence journal")
	budgetSOL := flags.String("budget-sol", "", "total simulated SOL budget")
	budgetUSDC := flags.String("budget-usdc", "", "simulated USDC budget")
	feeReserveSOL := flags.String(
		"fee-reserve-sol", formatShadowAmount(defaultTokenFeeReserveLamports, 9),
		"simulated native fee reserve",
	)
	setupRentSOL := flags.String(
		"setup-rent-sol", formatShadowAmount(defaultTokenSetupRentLamports, 9),
		"one-time token setup rent",
	)
	drawdownStopBPS := flags.Uint("drawdown-stop-bps", 0, "daily paper drawdown stop")
	sellAt := flags.String("sell-at-usd", "", "sell when SOL reaches this price")
	buyAt := flags.String("buy-at-usd", "", "buy when SOL falls to this price")
	amount := flags.Uint64("amount", 1_000_000, "trade size in input base units")
	slippageBPS := flags.Uint("slippage-bps", 100, "conservative fill allowance")
	feeLamports := flags.Uint64("fee-lamports", 5_000, "recurring cost charged to each attempt")
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
	explicit := make(map[string]bool)
	flags.Visit(func(item *flag.Flag) { explicit[item.Name] = true })
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
	if *adaptive && (*sellAt != "" || *buyAt != "") {
		return errors.New("--adaptive cannot be combined with fixed buy or sell prices")
	}
	if !*adaptive && *sellAt == "" && *buyAt == "" {
		return errors.New("give --sell-at-usd, --buy-at-usd, or both for a round trip")
	}
	mandate := explicit["market"] || explicit["budget-sol"] || explicit["budget-usdc"] ||
		explicit["fee-reserve-sol"] || explicit["setup-rent-sol"] || explicit["drawdown-stop-bps"]
	qualifiedEvidence := *admissionArtifact != "" || *admissionJournal != ""
	provisionalEvidence := *provisionalArtifact != "" || *provisionalJournal != ""
	if (*admissionArtifact == "") != (*admissionJournal == "") ||
		(*provisionalArtifact == "") != (*provisionalJournal == "") {
		return errors.New("each market evidence artifact requires its matching journal")
	}
	if qualifiedEvidence && provisionalEvidence {
		return errors.New("choose qualified or provisional market evidence, not both")
	}
	if !mandate && (qualifiedEvidence || provisionalEvidence) {
		return errors.New("market admission flags require an admitted paper mandate")
	}
	var admission marketadmission.Artifact
	var provisional marketadmission.ProvisionalArtifact
	if mandate && !explicit["fee-lamports"] {
		*feeLamports = defaultPaperFeeLamports
	}
	tradeAmount := *amount
	budgetLamports := uint64(0)
	feeReserveLamports := uint64(0)
	setupRentLamports := uint64(0)
	if mandate {
		if !*adaptive || *cluster != shadow.Mainnet {
			return errors.New("paper mandate flags require --adaptive on mainnet-beta")
		}
		if *market != shadow.MarketSOLUSDC && *market != shadow.MarketJUPUSDC &&
			!shadow.AdmittedMarket(*market) {
			return errors.New("--market must be SOL/USDC, JUP/USDC, WIF/USDC, JTO/USDC, or PYTH/USDC")
		}
		admitted := shadow.AdmittedMarket(*market)
		if admitted {
			if provisionalEvidence {
				var err error
				provisional, err = loadProvisionalMarketAdmission(
					*provisionalArtifact, *provisionalJournal, time.Now(),
				)
				if err != nil {
					return err
				}
			} else {
				var err error
				admission, err = loadQualifiedMarketAdmission(
					*admissionArtifact, *admissionJournal, time.Now(),
				)
				if err != nil {
					return err
				}
			}
			candidate := admission.Candidate
			admissionObserve := admission.Observe
			if provisionalEvidence {
				candidate, admissionObserve = provisional.Candidate, provisional.Observe
			}
			if candidate.Market != *market {
				return errors.New("market admission artifact does not match --market")
			}
			if *observe != admissionObserve {
				return errors.New("--observe must match the market evidence")
			}
			if *slippageBPS != uint(candidate.QuoteSlippageBPS) {
				return errors.New("--slippage-bps must match the market evidence")
			}
		} else if qualifiedEvidence || provisionalEvidence {
			return errors.New("market admission flags are only for admitted candidate markets")
		}
		if !explicit["drawdown-stop-bps"] {
			return errors.New("paper mandate requires --drawdown-stop-bps")
		}
		if explicit["amount"] {
			return errors.New("paper mandate budgets cannot be combined with --amount")
		}
		if *drawdownStopBPS == 0 || *drawdownStopBPS > 5_000 {
			return errors.New("--drawdown-stop-bps must be between 1 and 5000")
		}
		if *market == shadow.MarketSOLUSDC {
			if *budgetSOL == "" || explicit["budget-usdc"] || explicit["fee-reserve-sol"] {
				return errors.New("SOL/USDC paper mandate requires --budget-sol and no separate fee-reserve flag")
			}
			var err error
			budgetLamports, err = parseDecimalUnits9(*budgetSOL, "paper SOL budget")
			if err != nil {
				return err
			}
			if *feeLamports > math.MaxUint64/2 {
				return errors.New("paper transaction fee is too large")
			}
			setupRentLamports, err = parseDecimalUnits9(*setupRentSOL, "paper token setup rent")
			if err != nil {
				return err
			}
			if setupRentLamports == 0 || setupRentLamports > math.MaxUint64-2*(*feeLamports) {
				return errors.New("paper token setup rent and transaction fees are too large")
			}
			feeReserveLamports = max(
				defaultPaperMandateReserve, setupRentLamports+2*(*feeLamports),
			)
			if budgetLamports <= feeReserveLamports {
				return errors.New("paper SOL budget must exceed its setup-rent and fee reserve")
			}
			tradeAmount = budgetLamports - feeReserveLamports
		} else {
			if *budgetUSDC == "" || explicit["budget-sol"] {
				return errors.New("non-SOL paper mandate requires --budget-usdc")
			}
			var err error
			tradeAmount, err = parseDecimalUnits(*budgetUSDC, "paper USDC budget", ^uint64(0))
			if err != nil {
				return err
			}
			quoteNotional := admission.Candidate.QuoteNotionalUSDC
			if provisionalEvidence {
				quoteNotional = provisional.Candidate.QuoteNotionalUSDC
			}
			if admitted && tradeAmount != quoteNotional {
				return errors.New("admitted paper budget must match the qualified quote notional")
			}
			feeReserveLamports, err = parseDecimalUnits9(*feeReserveSOL, "paper SOL fee reserve")
			if err != nil {
				return err
			}
			setupRentLamports, err = parseDecimalUnits9(*setupRentSOL, "paper token setup rent")
			if err != nil {
				return err
			}
			if setupRentLamports == 0 || *feeLamports > math.MaxUint64/2 ||
				setupRentLamports > math.MaxUint64-2*(*feeLamports) ||
				feeReserveLamports < setupRentLamports+2*(*feeLamports) {
				return errors.New("paper SOL reserve must fund token setup rent and at least two transaction fees")
			}
			if *pool != "" || *inputMint != "" || *outputMint != "" {
				return errors.New("Mainnet paper mandates use a pinned route; omit Devnet route flags")
			}
		}
	}

	var policy shadow.Policy
	var err error
	if *adaptive {
		if mandate && (*market == shadow.MarketJUPUSDC || shadow.AdmittedMarket(*market)) {
			if shadow.AdmittedMarket(*market) {
				if provisionalEvidence {
					policy, err = buildAdaptiveProvisionalPolicy(
						provisional, tradeAmount, feeReserveLamports, setupRentLamports,
						uint16(*slippageBPS), *feeLamports, *observe, *tickSeconds,
					)
				} else {
					policy, err = buildAdaptiveAdmittedPolicy(
						admission, tradeAmount, feeReserveLamports, setupRentLamports,
						uint16(*slippageBPS), *feeLamports, *observe, *tickSeconds,
					)
				}
			} else {
				policy, err = buildAdaptiveJUPPolicy(
					tradeAmount, feeReserveLamports, setupRentLamports,
					uint16(*slippageBPS), *feeLamports,
					*observe, *tickSeconds,
				)
			}
		} else {
			policy, err = buildAdaptiveShadowPolicy(
				*cluster, tradeAmount, uint16(*slippageBPS), *feeLamports,
				*observe, *tickSeconds, *pool, *inputMint, *outputMint,
			)
		}
	} else {
		policy, err = buildShadowPolicy(
			*cluster, *sellAt, *buyAt, *amount, uint16(*slippageBPS), *feeLamports,
			*observe, *tickSeconds, *pool, *inputMint, *outputMint,
		)
	}
	if err != nil {
		return err
	}
	if mandate {
		if *market == shadow.MarketSOLUSDC {
			policy.StartingFeeReserveLamports = feeReserveLamports
			policy.OneTimeSetupRentLamports = setupRentLamports
		}
		policy.Adaptive.MaxDrawdownBPS = uint16(*drawdownStopBPS)
		if err := policy.Validate(); err != nil {
			return err
		}
	}
	encoded, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	if err := securefile.ReplacePrivate(*outPath, append(encoded, '\n'), maxInputBytes); err != nil {
		return errors.New("could not write the policy")
	}
	mandateSummary := ""
	if mandate {
		budgetText := formatShadowAmount(budgetLamports, 9) + " SOL"
		if *market != shadow.MarketSOLUSDC {
			budgetText = formatShadowAmount(tradeAmount, 6) + " USDC · native reserve " +
				formatShadowAmount(feeReserveLamports, 9) + " SOL · setup locks " +
				formatShadowAmount(setupRentLamports, 9) + " SOL"
		} else {
			budgetText += " · setup locks " + formatShadowAmount(setupRentLamports, 9) + " SOL"
		}
		mandateSummary = fmt.Sprintf(
			"Paper mandate: %s · budget %s · daily drawdown stop %s%%\n"+
				"The simulated book resets at 00:00 UTC; the stop is not a guaranteed maximum loss.\n\n",
			*market, budgetText,
			formatShadowAmount(uint64(*drawdownStopBPS), 2),
		)
	}
	_, err = fmt.Fprintf(output, "Wrote %s\n\n%s%s\n\n"+
		"It reads the market and writes down what the rule would have done.\n"+
		"It holds no wallet signing key and cannot sign, submit, or spend anything.\n",
		*outPath, mandateSummary, shadowRunHint(
			policy, *outPath, *admissionArtifact, *admissionJournal,
			*provisionalArtifact, *provisionalJournal,
		))
	return err
}

func buildAdaptiveShadowPolicy(
	cluster string,
	amount uint64, slippageBPS uint16, feeLamports uint64,
	observe string,
	tickSeconds uint64,
	pool, inputMint, outputMint string,
) (shadow.Policy, error) {
	// The trigger pair remains only as the feed/source and inventory-direction
	// contract. Unreachable boundary prices ensure no fixed threshold can become
	// an accidental fallback if the adaptive policy is ever absent.
	policy, err := buildShadowPolicy(
		cluster, "1000000", "0.000001", amount, slippageBPS, feeLamports,
		observe, tickSeconds, pool, inputMint, outputMint,
	)
	if err != nil {
		return shadow.Policy{}, err
	}
	if feeLamports > math.MaxUint64/2 || amount > math.MaxUint64-2*feeLamports {
		return shadow.Policy{}, errors.New("adaptive paper inventory is too large")
	}
	// The adaptive controller switches this one paper position between SOL and
	// USDC. Native fees stay in their own bucket so traded inventory and fee
	// funding cannot be confused when more markets are added later.
	policy.StartingInputUnits = amount
	policy.StartingOutputUnits = 0
	if cluster == shadow.Mainnet {
		policy.StartingFeeReserveLamports = max(
			defaultPaperFeeReserveLamports, 2*feeLamports,
		)
	} else {
		policy.StartingInputUnits += 2 * feeLamports
	}
	adaptive, err := shadow.DefaultAdaptivePolicy(slippageBPS, feeLamports, amount, tickSeconds)
	if err != nil {
		return shadow.Policy{}, err
	}
	policy.Adaptive = &adaptive
	if err := policy.Validate(); err != nil {
		return shadow.Policy{}, err
	}
	return policy, nil
}

func buildAdaptiveJUPPolicy(
	quoteBudget, feeReserveLamports, setupRentLamports uint64,
	slippageBPS uint16, feeLamports uint64,
	observe string,
	tickSeconds uint64,
) (shadow.Policy, error) {
	return buildAdaptiveQuoteMarketPolicy(
		shadow.Version, shadow.MarketJUPUSDC, pricetrigger.FeedJUPUSD,
		pricesource.PythPushJUPIdentitySHA256(), pricesource.KrakenJUPIdentitySHA256(), "",
		quoteBudget, feeReserveLamports, setupRentLamports,
		slippageBPS, feeLamports, observe, tickSeconds,
	)
}

func buildAdaptiveAdmittedPolicy(
	artifact marketadmission.Artifact,
	quoteBudget, feeReserveLamports, setupRentLamports uint64,
	slippageBPS uint16, feeLamports uint64,
	observe string,
	tickSeconds uint64,
) (shadow.Policy, error) {
	if !artifact.OperationallyQualified || artifact.Validate() != nil {
		return shadow.Policy{}, errors.New("market admission artifact is not qualified")
	}
	if observe != artifact.Observe || quoteBudget != artifact.Candidate.QuoteNotionalUSDC ||
		slippageBPS != artifact.Candidate.QuoteSlippageBPS {
		return shadow.Policy{}, errors.New("admitted policy inputs do not match market evidence")
	}
	primary, err := artifact.Candidate.Pyth.IdentitySHA256()
	if err != nil {
		return shadow.Policy{}, err
	}
	secondary, err := artifact.Candidate.Kraken.IdentitySHA256()
	if err != nil {
		return shadow.Policy{}, err
	}
	policy, err := buildAdaptiveQuoteMarketPolicy(
		shadow.AdmittedVersion, artifact.Candidate.Market, artifact.Candidate.Pyth.Feed,
		primary, secondary, artifact.ContentSHA256,
		quoteBudget, feeReserveLamports, setupRentLamports,
		slippageBPS, feeLamports, observe, tickSeconds,
	)
	if err != nil {
		return shadow.Policy{}, err
	}
	policy.MarketEvidenceClass = shadow.MarketEvidenceLongRun
	return policy, policy.Validate()
}

func buildAdaptiveProvisionalPolicy(
	artifact marketadmission.ProvisionalArtifact,
	quoteBudget, feeReserveLamports, setupRentLamports uint64,
	slippageBPS uint16, feeLamports uint64,
	observe string,
	tickSeconds uint64,
) (shadow.Policy, error) {
	if !artifact.ProvisionalPaperReady || artifact.Validate() != nil {
		return shadow.Policy{}, errors.New("provisional market evidence is not ready for paper testing")
	}
	if observe != artifact.Observe || quoteBudget != artifact.Candidate.QuoteNotionalUSDC ||
		slippageBPS != artifact.Candidate.QuoteSlippageBPS {
		return shadow.Policy{}, errors.New("provisional policy inputs do not match market evidence")
	}
	primary, err := artifact.Candidate.Pyth.IdentitySHA256()
	if err != nil {
		return shadow.Policy{}, err
	}
	secondary, err := artifact.Candidate.Kraken.IdentitySHA256()
	if err != nil {
		return shadow.Policy{}, err
	}
	policy, err := buildAdaptiveQuoteMarketPolicy(
		shadow.AdmittedVersion, artifact.Candidate.Market, artifact.Candidate.Pyth.Feed,
		primary, secondary, artifact.ContentSHA256,
		quoteBudget, feeReserveLamports, setupRentLamports,
		slippageBPS, feeLamports, observe, tickSeconds,
	)
	if err != nil {
		return shadow.Policy{}, err
	}
	policy.MarketEvidenceClass = shadow.MarketEvidenceDevelopmentProvisional
	return policy, policy.Validate()
}

func buildAdaptiveQuoteMarketPolicy(
	version uint32,
	market, feed, primarySource, secondarySource, marketEvidence string,
	quoteBudget, feeReserveLamports, setupRentLamports uint64,
	slippageBPS uint16, feeLamports uint64,
	observe string,
	tickSeconds uint64,
) (shadow.Policy, error) {
	const nativeFeePriceCeilingMicros = uint64(1_000_000_000) // $1,000/SOL
	baseDecimals, ok := shadow.MainnetMarketBaseDecimals(market)
	if !ok {
		return shadow.Policy{}, errors.New("paper market decimals are unsupported")
	}
	triggerVersion := pricetrigger.MultiFeedVersion
	if version == shadow.AdmittedVersion {
		triggerVersion = pricetrigger.AdmittedFeedVersion
	}
	trigger := pricetrigger.Policy{
		Version: triggerVersion, Feed: feed,
		Direction: pricetrigger.BuyAtOrBelow, ThresholdMicros: 1,
		MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90,
		MaxDeviationBPS: 200, MaxConfidenceBPS: 200,
		PrimarySourceSHA256: primarySource, SecondarySourceSHA256: secondarySource,
	}
	if version == shadow.AdmittedVersion {
		trigger.MaxSourceSkewSeconds = 30
	}
	returnTrigger := trigger
	returnTrigger.Direction = pricetrigger.SellAtOrAbove
	returnTrigger.ThresholdMicros = pricetrigger.MaxPriceMicros
	nativeFeePrice := pricetrigger.Policy{
		Version: pricetrigger.MultiFeedVersion, Feed: pricetrigger.FeedSOLUSD,
		Direction: pricetrigger.BuyAtOrBelow, ThresholdMicros: 1,
		MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90,
		MaxDeviationBPS: 200, MaxConfidenceBPS: 200,
		PrimarySourceSHA256:   pricesource.PythPushIdentitySHA256(),
		SecondarySourceSHA256: pricesource.KrakenSOLIdentitySHA256(),
	}
	if version == shadow.AdmittedVersion {
		nativeFeePrice.MaxSourceSkewSeconds = 30
	}
	adaptive, err := shadow.DefaultAdaptiveQuotePolicy(
		slippageBPS, feeLamports, nativeFeePriceCeilingMicros,
		quoteBudget, 6, tickSeconds,
	)
	if err != nil {
		return shadow.Policy{}, err
	}
	policy := shadow.Policy{
		Version: version, Cluster: shadow.Mainnet, Market: market,
		MarketEvidenceSHA256: marketEvidence,
		Adaptive:             &adaptive, Trigger: trigger, ReturnTrigger: &returnTrigger,
		QuoteRoute:  shadow.MainnetMarketQuoteRoute(market, false),
		Observe:     observe,
		InputAmount: quoteBudget, InputDecimals: 6, OutputDecimals: baseDecimals,
		SlippageBPS: slippageBPS, FeeLamports: feeLamports,
		TickSeconds: tickSeconds, SettleSeconds: 60,
		StartingInputUnits:         quoteBudget,
		StartingFeeReserveLamports: feeReserveLamports,
		OneTimeSetupRentLamports:   setupRentLamports,
		NativeFeePrice:             &nativeFeePrice, NativeFeePriceCeilingMicros: nativeFeePriceCeilingMicros,
		QuotePeg: &pricetrigger.BandPolicy{
			Version: pricetrigger.Version, Feed: pricetrigger.FeedUSDCUSD,
			MinimumMicros: pricetrigger.USDCBandMinimumMicros,
			MaximumMicros: pricetrigger.USDCBandMaximumMicros,
			MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90,
			MaxDeviationBPS: 100, MaxConfidenceBPS: 100,
			PrimarySourceSHA256:   pricesource.PythPushUSDCIdentitySHA256(),
			SecondarySourceSHA256: pricesource.KrakenIdentitySHA256(),
		},
	}
	if version == shadow.AdmittedVersion {
		policy.QuotePeg.MaxSourceSkewSeconds = 30
	}
	if err := policy.Validate(); err != nil {
		return shadow.Policy{}, err
	}
	return policy, nil
}

func shadowRunHint(
	policy shadow.Policy,
	path, admissionArtifact, admissionJournal, provisionalArtifact, provisionalJournal string,
) string {
	if policy.Cluster == shadow.Mainnet {
		if policy.Version == shadow.AdmittedVersion &&
			policy.MarketEvidenceClass == shadow.MarketEvidenceDevelopmentProvisional {
			return fmt.Sprintf("Check this base policy before running it:\n"+
				"  mithril-agent shadow market paper-check --policy %s --provisional-artifact %s --journal %s --result-out PAPER_CHECK --candidate-policy-out CHECKED_POLICY\n\n"+
				"Run only the checked policy with:\n"+
				"  mithril-agent shadow run --policy CHECKED_POLICY --dir DIR --portfolio PORTFOLIO --portfolio-book BOOK --provisional-artifact %s --provisional-journal %s --paper-check-artifact PAPER_CHECK",
				path, provisionalArtifact, provisionalJournal,
				provisionalArtifact, provisionalJournal)
		}
		admission := ""
		if policy.Version == shadow.AdmittedVersion {
			admission = fmt.Sprintf(" --admission-artifact %s --admission-journal %s",
				admissionArtifact, admissionJournal)
		}
		portfolio := ""
		if policy.Version == shadow.AdmittedVersion {
			portfolio = " --portfolio PORTFOLIO --portfolio-book BOOK"
		}
		return fmt.Sprintf("Run it with:\n  mithril-agent shadow run --policy %s --dir DIR%s%s\n\n"+
			"After N complete UTC days, verify them with:\n  mithril-agent shadow review --policy %s --dir DIR --days N\n\n"+
			"Keyless Jupiter access needs no account and is suitable for testing. For continuous production operation, Jupiter recommends a free or paid API key; keep MITHRIL_AGENT_JUPITER_API_KEY scoped to this read-only service.",
			path, portfolio, admission,
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
	feeReserve := feeLamports
	if direction == pricetrigger.SellAtOrAbove {
		if sellAt != "" && buyAt != "" {
			if feeLamports > math.MaxUint64/2 {
				return shadow.Policy{}, errors.New("shadow transaction fee is too large")
			}
			feeReserve *= 2
		}
		if cluster == shadow.Mainnet {
			feeReserve = max(defaultPaperFeeReserveLamports, feeReserve)
		}
		if cluster == shadow.Devnet {
			if amount > math.MaxUint64-feeReserve {
				return shadow.Policy{}, errors.New("shadow trade amount is too large")
			}
			startingInput = max(startingInput, amount+feeReserve)
		}
	} else {
		// The notional book pays Solana fees in SOL even when its first trade
		// spends USDC. Devnet keeps the original embedded reserve; Mainnet uses
		// the explicit reserve below so it cannot be mistaken for bought SOL.
		if cluster == shadow.Devnet {
			startingOutput = feeLamports
		}
		if cluster == shadow.Mainnet {
			feeReserve = max(defaultPaperFeeReserveLamports, feeReserve)
		}
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
			SecondarySourceSHA256: pricesource.KrakenSOLIdentitySHA256(),
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
		policy.Market = shadow.MarketSOLUSDC
		policy.StartingFeeReserveLamports = feeReserve
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
