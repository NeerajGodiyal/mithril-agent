package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/big"
	"math/bits"
	"path/filepath"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

// A continuous shadow run scores the rule as it happens. This command reruns a
// sell-then-buy-back rule over prices the observer already recorded, which lets
// an operator compare thresholds without waiting for another live period. The
// second leg spends exactly what the first produced and the spread plus two
// fees comes out of one book.
//
// One thing it does NOT do is pretend to know the pool. The recorded quotes
// belong only to decisions the original policy actually made; hypothetical
// thresholds have no venue quote at every tick. Their fills therefore have to
// be modelled from a spread the operator supplies — and the report says so on
// its own face. A backtest that hides its assumptions is worse than no backtest.
const shadowBacktestUsage = `Usage: mithril-agent shadow backtest --policy PATH --dir PATH
                              [--buy-at-usd PRICE] [--spread-bps N] [--day YYYY-MM-DD]
                              [--risk-lanes] [--json]

Scores a sell-then-buy-back round trip against the prices a shadow run already
recorded, on ONE set of books, with the same ledger and report the live observer
uses.

  --policy PATH     the shadow policy whose sell rule and sizing to reuse
  --dir PATH        the directory holding recorded shadow journals
  --buy-at-usd P    fixed policies only: buy back at or below this price;
                    adaptive policies derive both directions from the market
  --spread-bps N    how much worse than the oracle the pool is assumed to fill,
                    each way (default 100 = 1%). This is a MODEL, not a quote.
  --day DATE        which recorded UTC day to score (default: the latest)
  --risk-lanes      adaptive policies only: compare holding, conservative,
                    current, and aggressive paper settings on the same ticks
  --json            emit the report as JSON

  --cost-experiment observed-native-cost-v1
                    offline v2/v3 non-SOL adaptive comparison using verified
                    journaled SOL/USD for the fee hurdle; cannot combine with
                    --risk-lanes. Retains fees, reserves, loss/source/slippage
                    limits and extra selectivity above the original cost floor.
                    Outputs modeled baseline and experiment as JSON. Missing
                    historical venue quotes are modeled using --spread-bps;
                    this is not admission evidence and activates nothing.

The result is only as honest as --spread-bps. Read the pool's real quote with
"mithril-agent swap discover" and set it from what you actually see.`

func runShadowBacktest(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow backtest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "shadow policy JSON")
	directory := flags.String("dir", "", "journal directory")
	buyAtUSD := flags.String("buy-at-usd", "", "buy back at or below this price")
	spreadBPS := flags.Uint("spread-bps", 100, "assumed pool cost each way, in basis points")
	day := flags.String("day", "", "UTC day, YYYY-MM-DD")
	riskLanes := flags.Bool("risk-lanes", false, "compare paper risk profiles")
	costExperiment := flags.String("cost-experiment", "", "offline observed-native-cost-v1 comparison")
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowBacktestUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *policyPath == "" || *directory == "" {
		return errors.New("shadow backtest requires --policy and --dir")
	}
	if *costExperiment != "" && (*costExperiment != shadow.ObservedNativeCostVersion || *riskLanes || *buyAtUSD != "") {
		return errors.New("cost experiment must be observed-native-cost-v1 without --risk-lanes or --buy-at-usd")
	}
	// A pool that costs nothing is the single easiest way to make a paper
	// result flatter itself, and 100% would consume every trade.
	if *spreadBPS == 0 || *spreadBPS >= 10_000 {
		return errors.New("--spread-bps must be between 1 and 9999")
	}
	policy, err := loadShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	if *riskLanes && policy.Adaptive == nil {
		return errors.New("--risk-lanes requires an adaptive shadow policy")
	}
	if *riskLanes && *buyAtUSD != "" {
		return errors.New("--risk-lanes cannot be combined with --buy-at-usd")
	}
	journalPolicy := policy
	if policy.Adaptive == nil {
		if *buyAtUSD == "" {
			return errors.New("a fixed shadow policy requires --buy-at-usd")
		}
		buyAtMicros, parseErr := parseUSDThreshold(*buyAtUSD, "buy price")
		if parseErr != nil {
			return parseErr
		}
		// The return leg is the policy's own rule with the direction flipped, so a
		// round trip cannot silently read a different feed or source pair.
		returnLeg := policy.Trigger
		returnLeg.Direction = pricetrigger.BuyAtOrBelow
		returnLeg.ThresholdMicros = buyAtMicros
		if !policy.IsSell() {
			returnLeg.Direction = pricetrigger.SellAtOrAbove
		}
		policy.ReturnTrigger = &returnLeg
	} else if *buyAtUSD != "" {
		return errors.New("an adaptive shadow policy does not accept --buy-at-usd")
	}

	chosen, err := chooseShadowDay(*directory, *day)
	if err != nil {
		return err
	}
	ticks, err := readShadowTicks(
		filepath.Join(*directory, "shadow-"+chosen+".jsonl"), journalPolicy,
	)
	if err != nil {
		return err
	}
	if _, err := shadow.Replay(journalPolicy, ticks); err != nil {
		return fmt.Errorf("replay source shadow journal: %w", err)
	}
	prices := observedPrices(ticks)
	if len(prices) < 2 {
		return errors.New("that day recorded fewer than two observable prices to score")
	}
	if *costExperiment != "" {
		return writeNativeCostExperiment(output, chosen, uint64(*spreadBPS), policy, ticks)
	}
	if *riskLanes {
		return writeRiskComparison(output, *asJSON, chosen, uint64(*spreadBPS), policy, ticks)
	}

	result, err := shadow.ReplayRoundTripTicks(
		policy, ticks, modelledPool(policy, uint64(*spreadBPS), policy.SlippageBPS),
	)
	if err != nil {
		return err
	}
	report, err := shadow.BuildReport(
		policy, result.Ledger, shadow.Counts{}, shadow.Stats{},
		result.ClosingPrice, ticks[0].At, ticks[len(ticks)-1].At,
	)
	if err != nil {
		return err
	}
	return writeBacktest(output, *asJSON, chosen, uint64(*spreadBPS), result, report)
}

// writeNativeCostExperiment writes only stdout. The policy and source journal
// remain unchanged; neither model is an executable quote or market admission.
func writeNativeCostExperiment(output io.Writer, day string, spreadBPS uint64, policy shadow.Policy, ticks []shadow.Tick) error {
	quote := modelledPool(policy, spreadBPS, policy.SlippageBPS)
	baseline, experiment, err := shadow.ReplayObservedNativeCostComparison(policy, ticks, quote)
	if err != nil {
		return err
	}
	policyHash, err := policy.Fingerprint()
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(ticks)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(append([]byte("mithril-agent/cost-experiment-history-v1\x00"), encoded...))
	return json.NewEncoder(output).Encode(struct {
		Version           string                 `json:"experiment"`
		Day               string                 `json:"day"`
		PolicySHA256      string                 `json:"policy_sha256"`
		HistorySHA256     string                 `json:"history_sha256"`
		FeeLamports       uint64                 `json:"assumed_fee_lamports,string"`
		SpreadBPS         uint64                 `json:"assumed_spread_bps"`
		PoolModelled      bool                   `json:"pool_modelled"`
		AdmissionEvidence bool                   `json:"admission_evidence"`
		TradingEnabled    bool                   `json:"trading_enabled"`
		Limitation        string                 `json:"limitation"`
		Baseline          shadow.RoundTripResult `json:"baseline"`
		Observed          shadow.RoundTripResult `json:"observed_native_cost"`
	}{Version: shadow.ObservedNativeCostVersion, Day: day, PolicySHA256: policyHash, HistorySHA256: hex.EncodeToString(digest[:]),
		FeeLamports: policy.FeeLamports, SpreadBPS: spreadBPS, PoolModelled: true,
		Limitation: "Counterfactual venue quotes are modeled, not historical fills; fresh forward paper quotes are required before admission.",
		Baseline:   baseline, Observed: experiment})
}

// observedPrices keeps only ticks where the market could actually be read. A
// gap is not a price, and carrying one forward would invent a decision the
// observer never had the evidence to make.
func observedPrices(ticks []shadow.Tick) []uint64 {
	prices := make([]uint64, 0, len(ticks))
	for _, tick := range ticks {
		if tick.PriceMicros != 0 && !tick.PeriodClose {
			prices = append(prices, tick.PriceMicros)
		}
	}
	return prices
}

// modelledPool turns an oracle price into the quote a pool is ASSUMED to give,
// worse than the oracle by spreadBPS in whichever direction the trade goes.
func modelledPool(
	policy shadow.Policy, spreadBPS uint64, slippageBPS uint16,
) func(uint64, bool, uint64) (shadow.Quote, error) {
	baseDecimals, quoteDecimals := policy.InputDecimals, policy.OutputDecimals
	if !policy.IsSell() {
		baseDecimals, quoteDecimals = quoteDecimals, baseDecimals
	}
	return func(price uint64, sell bool, input uint64) (shadow.Quote, error) {
		if price == 0 || input == 0 {
			return shadow.Quote{}, errors.New("cannot model a fill at a zero price")
		}
		if spreadBPS >= 10_000 || slippageBPS == 0 || slippageBPS >= 10_000 ||
			baseDecimals > 18 || quoteDecimals > 18 {
			return shadow.Quote{}, errors.New("cannot model a fill with invalid slippage")
		}
		numerator := new(big.Int).SetUint64(input)
		denominator := new(big.Int)
		baseScale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(baseDecimals)), nil)
		quoteScale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(quoteDecimals)), nil)
		if sell {
			numerator.Mul(numerator, new(big.Int).SetUint64(price))
			numerator.Mul(numerator, quoteScale)
			denominator.Mul(baseScale, big.NewInt(1_000_000))
		} else {
			numerator.Mul(numerator, baseScale)
			numerator.Mul(numerator, big.NewInt(1_000_000))
			denominator.Mul(new(big.Int).SetUint64(price), quoteScale)
		}
		numerator.Div(numerator, denominator)
		if !numerator.IsUint64() {
			return shadow.Quote{}, errors.New("the modelled fill is out of range")
		}
		out := numerator.Uint64()
		var ok bool
		out, ok = boundedMulDiv(out, 10_000-spreadBPS, 10_000)
		if !ok || out == 0 {
			return shadow.Quote{}, errors.New("the modelled fill rounds to nothing at this price")
		}
		minimum, ok := boundedMulDivCeil(out, 10_000-uint64(slippageBPS), 10_000)
		if !ok || minimum == 0 {
			return shadow.Quote{}, errors.New("the modelled slippage floor is out of range")
		}
		return shadow.Quote{
			InputAmount: input, EstimatedOutput: out, MinimumOutput: minimum,
		}, nil
	}
}

func boundedMulDiv(value, multiplier, divisor uint64) (uint64, bool) {
	high, low := bits.Mul64(value, multiplier)
	if divisor == 0 || high >= divisor {
		return 0, false
	}
	result, _ := bits.Div64(high, low, divisor)
	return result, true
}

func boundedMulDivCeil(value, multiplier, divisor uint64) (uint64, bool) {
	high, low := bits.Mul64(value, multiplier)
	if divisor == 0 || high >= divisor {
		return 0, false
	}
	result, remainder := bits.Div64(high, low, divisor)
	if remainder != 0 {
		if result == ^uint64(0) {
			return 0, false
		}
		result++
	}
	return result, true
}

type backtestResult struct {
	Day string `json:"day"`
	// PoolModelled is stated in the payload, not just the prose, so a machine
	// reading this cannot present it as an observed result either.
	PoolModelled   bool                   `json:"pool_modelled"`
	SpreadBPS      uint64                 `json:"assumed_spread_bps"`
	Counts         shadow.RoundTripCounts `json:"counts"`
	RealizedMicros int64                  `json:"realized_micros"`
	VersusHold     int64                  `json:"versus_hold_micros"`
	ClosingEquity  uint64                 `json:"closing_equity_micros"`
	OpeningEquity  uint64                 `json:"opening_equity_micros"`
}

type riskLaneResult struct {
	Name                  string                 `json:"name"`
	Description           string                 `json:"description"`
	TradingEnabled        bool                   `json:"trading_enabled"`
	InputAmount           uint64                 `json:"input_amount"`
	ExposureBPS           uint64                 `json:"trade_size_bps"`
	MinimumSignalBPS      uint16                 `json:"minimum_signal_bps,omitempty"`
	MaxDrawdownLimitBPS   uint16                 `json:"max_drawdown_limit_bps,omitempty"`
	CooldownSeconds       uint64                 `json:"cooldown_seconds,omitempty"`
	Counts                shadow.RoundTripCounts `json:"counts"`
	OpeningEquityMicros   uint64                 `json:"opening_equity_micros"`
	ClosingEquityMicros   uint64                 `json:"closing_equity_micros"`
	ProfitLossMicros      int64                  `json:"profit_loss_micros"`
	VersusHoldingMicros   int64                  `json:"versus_holding_micros"`
	MaximumDrawdownMicros uint64                 `json:"maximum_drawdown_micros"`
	MaximumDrawdownBPS    uint64                 `json:"maximum_drawdown_bps"`
	FeesMicros            int64                  `json:"fees_micros"`
	TurnoverMicros        uint64                 `json:"turnover_micros"`
}

type riskComparisonResult struct {
	Day                string           `json:"day"`
	Market             string           `json:"market"`
	PoolModelled       bool             `json:"pool_modelled"`
	SizeImpactModelled bool             `json:"size_impact_modelled"`
	SpreadBPS          uint64           `json:"assumed_spread_bps"`
	Lanes              []riskLaneResult `json:"lanes"`
}

type namedRiskPolicy struct {
	name        string
	description string
	policy      shadow.Policy
}

func writeRiskComparison(
	output io.Writer,
	asJSON bool,
	day string,
	spreadBPS uint64,
	base shadow.Policy,
	ticks []shadow.Tick,
) error {
	control, err := scoreHoldControl(base, ticks)
	if err != nil {
		return err
	}
	lanes := []riskLaneResult{control}
	policies, err := paperRiskPolicies(base)
	if err != nil {
		return err
	}
	for _, candidate := range policies {
		lane, err := scoreRiskLane(candidate, ticks, spreadBPS, control)
		if err != nil {
			return fmt.Errorf("score %s paper lane: %w", candidate.name, err)
		}
		lanes = append(lanes, lane)
	}
	comparison := riskComparisonResult{
		Day: day, Market: base.Market, PoolModelled: true,
		SizeImpactModelled: false, SpreadBPS: spreadBPS, Lanes: lanes,
	}
	if asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(comparison)
	}
	fmt.Fprintf(output, "\nPaper risk comparison — %s · %s\n", base.Market, day)
	fmt.Fprintf(output, "  Same recorded prices and a modelled %d bps venue cost for every trading lane.\n\n", spreadBPS)
	for _, lane := range lanes {
		fmt.Fprintf(output, "  %-12s P/L $%s · vs holding $%s · %d fills · drawdown %d.%02d%%\n",
			lane.Name, formatSignedMicros(lane.ProfitLossMicros),
			formatSignedMicros(lane.VersusHoldingMicros),
			lane.Counts.Sells+lane.Counts.Buys,
			lane.MaximumDrawdownBPS/100, lane.MaximumDrawdownBPS%100,
		)
		fmt.Fprintf(output, "               %s\n", lane.Description)
	}
	fmt.Fprintln(output, "\n  Paper only. Holding is a no-trade control, not a risk-free asset.")
	fmt.Fprintln(output, "  Larger-order market impact is not modelled; do not treat size results as executable quotes.")
	_, err = fmt.Fprintln(output, "  Compare repeated out-of-sample days; one winning lane does not prove an edge.")
	return err
}

func paperRiskPolicies(base shadow.Policy) ([]namedRiskPolicy, error) {
	if err := base.Validate(); err != nil {
		return nil, fmt.Errorf("paper risk comparison policy: %w", err)
	}
	if base.Adaptive == nil {
		return nil, errors.New("paper risk comparison requires an adaptive policy")
	}
	conservative := base
	conservative.InputAmount = max(uint64(1), base.InputAmount/4)
	conservativeAdaptive := *base.Adaptive
	dayWindow := min(uint64(1_440), (uint64(86_399)-base.SettleSeconds)/base.TickSeconds+1)
	conservativeAdaptive.FastWindow = uint16(min(dayWindow-1, max(uint64(2), uint64(base.Adaptive.FastWindow)*2)))
	conservativeAdaptive.SlowWindow = uint16(min(
		dayWindow,
		max(uint64(conservativeAdaptive.FastWindow)+1, uint64(base.Adaptive.SlowWindow)*2),
	))
	conservativeAdaptive.MaxQuoteImpactBPS = min(base.Adaptive.MaxQuoteImpactBPS, uint16(250))
	conservativeAdaptive.MaxDrawdownBPS = min(base.Adaptive.MaxDrawdownBPS, uint16(100))
	conservativeCooldown := max(base.TickSeconds, base.Adaptive.CooldownSeconds)
	if conservativeCooldown > 86_400/3 {
		conservativeCooldown = 86_400
	} else {
		conservativeCooldown *= 3
	}
	conservativeAdaptive.CooldownSeconds = conservativeCooldown
	conservativeSignal := min(uint32(2_000), max(uint32(100), uint32(base.Adaptive.MinimumSignalBPS)*2))
	conservativeAdaptive.MaxVolatilityBPS = uint16(max(
		uint32(base.Adaptive.MaxVolatilityBPS), conservativeSignal+1,
	))
	conservative.Adaptive = &conservativeAdaptive
	if err := fitMinimumSignal(&conservative, conservativeSignal); err != nil {
		return nil, fmt.Errorf("build conservative paper lane: %w", err)
	}

	aggressive := base
	aggressiveLimit := base.StartingInputUnits
	if base.IsSell() && base.StartingFeeReserveLamports == 0 {
		fees := base.FeeLamports
		if base.RoundTrip() {
			fees *= 2 // Policy.Validate already proved this multiplication safe.
		}
		aggressiveLimit -= fees
	}
	aggressive.InputAmount = aggressiveLimit
	if base.InputAmount <= math.MaxUint64/4 {
		aggressive.InputAmount = min(aggressive.InputAmount, base.InputAmount*4)
	}
	aggressiveAdaptive := *base.Adaptive
	aggressiveAdaptive.FastWindow = max(uint16(2), base.Adaptive.FastWindow/2)
	aggressiveAdaptive.SlowWindow = max(aggressiveAdaptive.FastWindow+1, base.Adaptive.SlowWindow/2)
	aggressiveAdaptive.MaxVolatilityBPS = 5_000
	aggressiveAdaptive.MaxQuoteImpactBPS = 5_000
	aggressiveAdaptive.MaxDrawdownBPS = 5_000
	aggressiveAdaptive.CooldownSeconds = 0
	aggressive.Adaptive = &aggressiveAdaptive
	if err := fitMinimumSignal(&aggressive, 1); err != nil {
		return nil, fmt.Errorf("build aggressive paper lane: %w", err)
	}
	return []namedRiskPolicy{
		{name: "Conservative", description: "Quarter-size trades, slower signals, at most a 1% loss stop, longer pause.", policy: conservative},
		{name: "Current", description: "The exact policy used to record these prices.", policy: base},
		{name: "Aggressive", description: "Up to four-times size, fastest valid signal, no cooldown, 50% loss stop.", policy: aggressive},
	}, nil
}

func fitMinimumSignal(policy *shadow.Policy, start uint32) error {
	if policy == nil || policy.Adaptive == nil {
		return errors.New("adaptive paper policy is missing")
	}
	for signal := max(uint32(1), start); signal <= 2_000; signal++ {
		adaptive := *policy.Adaptive
		adaptive.MinimumSignalBPS = uint16(signal)
		policy.Adaptive = &adaptive
		if policy.Validate() == nil {
			return nil
		}
	}
	return errors.New("fees and limits leave no valid signal threshold")
}

func scoreRiskLane(
	candidate namedRiskPolicy,
	ticks []shadow.Tick,
	spreadBPS uint64,
	control riskLaneResult,
) (riskLaneResult, error) {
	result, err := shadow.ReplayRoundTripTicksWithLiquidationMarks(
		candidate.policy, ticks,
		modelledPool(candidate.policy, spreadBPS, candidate.policy.SlippageBPS),
	)
	if err != nil {
		return riskLaneResult{}, err
	}
	closingPrice, err := lastLiquidationPrice(candidate.policy, ticks)
	if err != nil {
		return riskLaneResult{}, err
	}
	comparison := result.LiquidationLedger
	closing, err := comparison.EquityMicros(closingPrice)
	if err != nil {
		return riskLaneResult{}, err
	}
	if comparison.OpeningEquityMicros != control.OpeningEquityMicros {
		return riskLaneResult{}, errors.New("paper lanes do not share one opening portfolio")
	}
	profitLoss, err := unsignedDifference(closing, comparison.OpeningEquityMicros)
	if err != nil {
		return riskLaneResult{}, err
	}
	versusHolding, err := unsignedDifference(closing, control.ClosingEquityMicros)
	if err != nil {
		return riskLaneResult{}, err
	}
	exposure, ok := boundedMulDiv(candidate.policy.InputAmount, 10_000, candidate.policy.StartingInputUnits)
	if !ok {
		return riskLaneResult{}, errors.New("paper trade size is out of range")
	}
	return riskLaneResult{
		Name: candidate.name, Description: candidate.description, TradingEnabled: true,
		InputAmount: candidate.policy.InputAmount, ExposureBPS: exposure,
		MinimumSignalBPS:    candidate.policy.Adaptive.MinimumSignalBPS,
		MaxDrawdownLimitBPS: candidate.policy.Adaptive.MaxDrawdownBPS,
		CooldownSeconds:     candidate.policy.Adaptive.CooldownSeconds,
		Counts:              result.Counts, OpeningEquityMicros: comparison.OpeningEquityMicros,
		ClosingEquityMicros: closing, ProfitLossMicros: profitLoss,
		VersusHoldingMicros:   versusHolding,
		MaximumDrawdownMicros: result.LiquidationMaxDrawdownMicros,
		MaximumDrawdownBPS:    uint64(result.LiquidationMaxDrawdownBPS), FeesMicros: result.Ledger.FeesMicros,
		TurnoverMicros: result.Ledger.TurnoverMicros,
	}, nil
}

func scoreHoldControl(policy shadow.Policy, ticks []shadow.Tick) (riskLaneResult, error) {
	var ledger shadow.Ledger
	opened := false
	closingPrice := uint64(0)
	for _, tick := range ticks {
		if tick.PeriodClose || tick.PriceMicros == 0 {
			continue
		}
		native := []uint64(nil)
		if policy.NativeFeePrice != nil {
			if tick.NativeFeePriceMicros == 0 {
				return riskLaneResult{}, errors.New("no-trade control lacks native fee-price evidence")
			}
			native = []uint64{tick.NativeFeePriceMicros}
		}
		price, err := liquidationPrice(policy, tick)
		if err != nil {
			return riskLaneResult{}, err
		}
		if !opened {
			ledger, err = shadow.NewLedger(policy, price, native...)
			opened = err == nil
		} else {
			ledger, err = ledger.Mark(price, native...)
		}
		if err != nil {
			return riskLaneResult{}, err
		}
		closingPrice = price
	}
	if !opened || closingPrice == 0 {
		return riskLaneResult{}, errors.New("no-trade control needs observable prices")
	}
	closing, err := ledger.EquityMicros(closingPrice)
	if err != nil {
		return riskLaneResult{}, err
	}
	profitLoss, err := unsignedDifference(closing, ledger.OpeningEquityMicros)
	if err != nil {
		return riskLaneResult{}, err
	}
	return riskLaneResult{
		Name: "Hold", Description: "No trades; keeps the opening assets as the control.",
		OpeningEquityMicros: ledger.OpeningEquityMicros, ClosingEquityMicros: closing,
		ProfitLossMicros: profitLoss, MaximumDrawdownMicros: ledger.MaxDrawdownMicros,
		MaximumDrawdownBPS: uint64(ledger.MaxDrawdownBPS),
	}, nil
}

func lastLiquidationPrice(policy shadow.Policy, ticks []shadow.Tick) (uint64, error) {
	for index := len(ticks) - 1; index >= 0; index-- {
		if !ticks[index].PeriodClose && ticks[index].PriceMicros != 0 {
			return liquidationPrice(policy, ticks[index])
		}
	}
	return 0, errors.New("paper comparison needs an observable liquidation price")
}

// liquidationPrice values every lane as if its remaining base asset were sold
// at the same conservative source price. A buy-side mark would otherwise make
// a lane look richer merely because it happened to finish waiting to buy.
func liquidationPrice(policy shadow.Policy, tick shadow.Tick) (uint64, error) {
	if tick.PrimaryPrice == nil && tick.SecondaryPrice == nil {
		return tick.PriceMicros, nil
	}
	if tick.PrimaryPrice == nil || tick.SecondaryPrice == nil {
		return 0, errors.New("paper comparison has incomplete market source evidence")
	}
	sell := policy.Trigger
	if sell.Direction != pricetrigger.SellAtOrAbove {
		if policy.ReturnTrigger == nil || policy.ReturnTrigger.Direction != pricetrigger.SellAtOrAbove {
			return 0, errors.New("paper comparison policy has no liquidation direction")
		}
		sell = *policy.ReturnTrigger
	}
	evidence, err := pricetrigger.Evaluate(sell, *tick.PrimaryPrice, *tick.SecondaryPrice, tick.At)
	if err != nil {
		return 0, err
	}
	return evidence.ConservativePrice, nil
}

func unsignedDifference(left, right uint64) (int64, error) {
	if left > math.MaxInt64 || right > math.MaxInt64 {
		return 0, errors.New("paper result is too large to compare")
	}
	return checkedDifference(int64(left), int64(right))
}

func writeBacktest(
	output io.Writer, asJSON bool, day string, spreadBPS uint64,
	result shadow.RoundTripResult, report shadow.Report,
) error {
	if asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(backtestResult{
			Day: day, PoolModelled: true, SpreadBPS: spreadBPS,
			Counts:         result.Counts,
			RealizedMicros: report.RealizedMicros,
			VersusHold:     report.VersusHoldMicros,
			ClosingEquity:  report.ClosingEquityMicros,
			OpeningEquity:  report.OpeningEquityMicros,
		})
	}
	w := func(format string, args ...any) { fmt.Fprintf(output, format, args...) }
	w("\nRound trip over recorded prices — %s\n", day)
	w("  legs       %d sell(s), %d buy(s), %d refused, %d filtered\n",
		result.Counts.Sells, result.Counts.Buys, result.Counts.Refused, result.Counts.Filtered)
	w("  signals    %d sell, %d buy\n",
		result.Counts.SellSignals, result.Counts.BuySignals)
	w("  realized   $%s\n", formatSignedMicros(report.RealizedMicros))
	w("  vs holding $%s   <- the only number that answers \"was this worth doing\"\n",
		formatSignedMicros(report.VersusHoldMicros))
	// The assumption goes last, where a reader stops, rather than buried above
	// the numbers it produced.
	w("\n  The pool was MODELLED at %d bps each way, not quoted.\n", spreadBPS)
	w("  Read the real number with: mithril-agent swap discover --direction sell ...\n")
	return nil
}

// formatSignedMicros prints a signed USD-micros value with its sign, because a
// loss that renders as a bare number reads as a profit at a glance.
func formatSignedMicros(value int64) string {
	if value < 0 {
		// -(MinInt64) overflows an int64. Convert the magnitude without ever
		// constructing that unrepresentable positive value.
		magnitude := uint64(-(value + 1)) + 1
		return "-" + formatUnits(magnitude, 6)
	}
	return "+" + formatUnits(uint64(value), 6)
}
