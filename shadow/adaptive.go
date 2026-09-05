package shadow

import (
	"errors"
	"math"
	"math/bits"
	"time"
)

// AdaptiveVersion identifies the current adaptive decision schema bound into a
// policy. Version 1 remains replayable because its signal hurdle treated the
// maximum executable slippage as a certain cost. Version 2 keeps that bound as
// a fill refusal and prices expected movement through observed volatility.
const (
	AdaptiveVersion       = uint32(2)
	adaptiveLegacyVersion = uint32(1)
)

const (
	RegimeWarming   = "warming"
	RegimeUptrend   = "uptrend"
	RegimeDowntrend = "downtrend"
	RegimeRange     = "range"
	RegimeVolatile  = "volatile"
	RegimeRisk      = "risk"

	StrategyObserve        = "observe"
	StrategyMomentum       = "momentum"
	StrategyRangeReversion = "range_reversion"
	StrategyRiskExit       = "risk_exit"
)

// AdaptivePolicy defines a price-relative paper strategy. It contains no
// absolute market price: every decision is derived from the current rolling
// baseline, observed volatility, inventory, costs, and drawdown.
type AdaptivePolicy struct {
	Version                  uint32 `json:"version"`
	FastWindow               uint16 `json:"fast_window"`
	SlowWindow               uint16 `json:"slow_window"`
	MinimumSignalBPS         uint16 `json:"minimum_signal_bps"`
	MaxVolatilityBPS         uint16 `json:"max_volatility_bps"`
	MaxQuoteImpactBPS        uint16 `json:"max_quote_impact_bps"`
	MaxDrawdownBPS           uint16 `json:"max_drawdown_bps"`
	CooldownSeconds          uint64 `json:"cooldown_seconds"`
	MaxObservationGapSeconds uint64 `json:"max_observation_gap_seconds"`
}

// DefaultAdaptivePolicy returns a deterministic regime-aware policy whose
// opening signal hurdle covers both transaction fees and a small safety margin
// for one paper round trip. The separate slippage bound remains a hard fill
// refusal; recent volatility and the executable quote cover expected movement
// and price impact without pretending the full tolerance is always paid.
func DefaultAdaptivePolicy(
	slippageBPS uint16, feeLamports, inputAmount, tickSeconds uint64,
) (AdaptivePolicy, error) {
	if tickSeconds == 0 || tickSeconds > 43_200 {
		return AdaptivePolicy{}, errors.New("adaptive observation interval is out of range")
	}
	minimumSignal, err := adaptiveSignalCostFloorBPS(
		AdaptiveVersion, slippageBPS, feeLamports, inputAmount,
	)
	if err != nil {
		return AdaptivePolicy{}, err
	}
	maxVolatility := max(uint32(500), minimumSignal+100)
	policy := AdaptivePolicy{
		Version:    AdaptiveVersion,
		FastWindow: 5, SlowWindow: 20,
		MinimumSignalBPS:         uint16(minimumSignal),
		MaxVolatilityBPS:         uint16(maxVolatility),
		MaxQuoteImpactBPS:        500,
		MaxDrawdownBPS:           300,
		CooldownSeconds:          300,
		MaxObservationGapSeconds: tickSeconds * 2,
	}
	return policy, policy.Validate()
}

// DefaultAdaptiveQuotePolicy builds the same controller for a stable-quote
// opening budget while valuing native SOL fees at a conservative USD ceiling.
// It is used by non-SOL paper markets; the ceiling is policy-bound and keeps a
// future SOL rally from making the configured edge smaller than its costs.
func DefaultAdaptiveQuotePolicy(
	slippageBPS uint16,
	feeLamports, nativeFeePriceCeilingMicros, quoteAmount uint64,
	quoteDecimals uint8,
	tickSeconds uint64,
) (AdaptivePolicy, error) {
	if tickSeconds == 0 || tickSeconds > 43_200 {
		return AdaptivePolicy{}, errors.New("adaptive observation interval is out of range")
	}
	minimumSignal, err := adaptiveQuoteSignalCostFloorBPS(
		AdaptiveVersion, slippageBPS, feeLamports, nativeFeePriceCeilingMicros,
		quoteAmount, quoteDecimals,
	)
	if err != nil {
		return AdaptivePolicy{}, err
	}
	maxVolatility := max(uint32(500), minimumSignal+100)
	policy := AdaptivePolicy{
		Version: AdaptiveVersion, FastWindow: 5, SlowWindow: 20,
		MinimumSignalBPS: uint16(minimumSignal), MaxVolatilityBPS: uint16(maxVolatility),
		MaxQuoteImpactBPS: 500, MaxDrawdownBPS: 300, CooldownSeconds: 300,
		MaxObservationGapSeconds: tickSeconds * 2,
	}
	return policy, policy.Validate()
}

// Validate rejects adaptive settings that cannot produce bounded, cost-aware
// and replayable paper decisions.
func (p AdaptivePolicy) Validate() error {
	if p.Version != adaptiveLegacyVersion && p.Version != AdaptiveVersion {
		return errors.New("adaptive policy version is not supported")
	}
	if p.FastWindow < 2 || p.SlowWindow <= p.FastWindow || p.SlowWindow > 1_440 {
		return errors.New("adaptive windows must satisfy 2 <= fast < slow <= 1440")
	}
	if p.MinimumSignalBPS == 0 || p.MinimumSignalBPS > 2_000 {
		return errors.New("adaptive minimum signal must be between 1 and 2000 basis points")
	}
	if p.MaxVolatilityBPS <= p.MinimumSignalBPS || p.MaxVolatilityBPS > 5_000 {
		return errors.New("adaptive volatility limit must exceed the minimum signal and be at most 5000 basis points")
	}
	if p.MaxQuoteImpactBPS == 0 || p.MaxQuoteImpactBPS > 5_000 {
		return errors.New("adaptive adverse quote limit must be between 1 and 5000 basis points")
	}
	if p.MaxDrawdownBPS == 0 || p.MaxDrawdownBPS > 5_000 {
		return errors.New("adaptive drawdown limit must be between 1 and 5000 basis points")
	}
	if p.CooldownSeconds > 86_400 {
		return errors.New("adaptive cooldown cannot exceed one day")
	}
	if p.MaxObservationGapSeconds == 0 || p.MaxObservationGapSeconds > 86_400 {
		return errors.New("adaptive observation gap must be between 1 second and one day")
	}
	return nil
}

// AdaptiveDecision is the complete, replayable explanation for one market
// observation. SignalBPS is a moving-average or range deviation, not a
// forecast return or probability, and it never overrides a risk gate.
type AdaptiveDecision struct {
	Regime            string `json:"regime"`
	Strategy          string `json:"strategy"`
	Reason            string `json:"reason"`
	FastAverageMicros uint64 `json:"fast_average_micros,omitempty"`
	SlowAverageMicros uint64 `json:"slow_average_micros,omitempty"`
	SignalBPS         int32  `json:"signal_bps,omitempty"`
	VolatilityBPS     uint16 `json:"volatility_bps,omitempty"`
}

type adaptiveStrategy struct {
	policy          AdaptivePolicy
	prices          []uint64
	lastFill        time.Time
	lastObservation time.Time
	riskHalted      bool
}

func newAdaptiveStrategy(policy *AdaptivePolicy) (*adaptiveStrategy, error) {
	if policy == nil {
		return nil, nil
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &adaptiveStrategy{policy: *policy}, nil
}

func (s *adaptiveStrategy) decide(
	at time.Time, price uint64, nextSell bool, ledger Ledger,
) (AdaptiveDecision, bool, error) {
	if s == nil {
		return AdaptiveDecision{}, false, errors.New("adaptive decision needs a strategy, price, and time")
	}
	return s.decideWithHurdle(at, price, nextSell, ledger, s.policy.MinimumSignalBPS)
}

// decideWithHurdle is shared with the offline cost experiment. Runtime callers
// retain their versioned policy hurdle through decide; risk exits remain first.
func (s *adaptiveStrategy) decideWithHurdle(
	at time.Time, price uint64, nextSell bool, ledger Ledger, minimumSignal uint16,
) (AdaptiveDecision, bool, error) {
	if s == nil || price == 0 || at.IsZero() {
		return AdaptiveDecision{}, false, errors.New("adaptive decision needs a strategy, price, and time")
	}
	if !s.lastObservation.IsZero() {
		if at.Before(s.lastObservation) {
			return AdaptiveDecision{}, false, errors.New("adaptive observations are not chronological")
		}
		if at.Sub(s.lastObservation) > time.Duration(s.policy.MaxObservationGapSeconds)*time.Second {
			s.prices = nil
		}
	}
	s.lastObservation = at
	s.prices = append(s.prices, price)
	if len(s.prices) > int(s.policy.SlowWindow) {
		s.prices = s.prices[len(s.prices)-int(s.policy.SlowWindow):]
	}

	decision := AdaptiveDecision{
		Regime: RegimeWarming, Strategy: StrategyObserve,
		Reason: "collecting_history",
	}
	if s.riskHalted {
		decision.Regime = RegimeRisk
		if nextSell {
			decision.Strategy = StrategyRiskExit
			decision.Reason = "drawdown_limit"
			return decision, true, nil
		}
		decision.Reason = "risk_halt"
		return decision, false, nil
	}
	drawdown, err := currentDrawdownBPS(ledger, price)
	if err != nil {
		return AdaptiveDecision{}, false, err
	}
	if drawdown >= s.policy.MaxDrawdownBPS {
		s.riskHalted = true
		decision.Regime = RegimeRisk
		if nextSell {
			decision.Strategy = StrategyRiskExit
			decision.Reason = "drawdown_limit"
			return decision, true, nil
		}
		decision.Reason = "drawdown_halt"
		return decision, false, nil
	}
	if len(s.prices) < int(s.policy.SlowWindow) {
		return decision, false, nil
	}

	fast := meanPrice(s.prices[len(s.prices)-int(s.policy.FastWindow):])
	slow := meanPrice(s.prices)
	volatility := returnVolatilityBPS(s.prices)
	signal := relativeBPS(fast, slow)
	decision.FastAverageMicros = fast
	decision.SlowAverageMicros = slow
	decision.SignalBPS = signal
	decision.VolatilityBPS = volatility

	trendEdge := max(uint16(volatility/2), minimumSignal)
	rangeEdge := max(volatility, minimumSignal)
	if volatility > s.policy.MaxVolatilityBPS {
		decision.Regime = RegimeVolatile
		decision.Reason = "volatility_limit"
		return decision, false, nil
	}
	if !s.lastFill.IsZero() && at.Before(s.lastFill.Add(time.Duration(s.policy.CooldownSeconds)*time.Second)) {
		decision.Regime = classifyRegime(signal, trendEdge)
		decision.Reason = "cooldown"
		return decision, false, nil
	}

	decision.Regime = classifyRegime(signal, trendEdge)
	switch decision.Regime {
	case RegimeUptrend:
		decision.Strategy = StrategyMomentum
		if !nextSell {
			decision.Reason = "trend_aligned_buy"
			return decision, true, nil
		}
		decision.Reason = "sell_leg_waiting"
	case RegimeDowntrend:
		decision.Strategy = StrategyMomentum
		if nextSell {
			decision.Reason = "trend_aligned_sell"
			return decision, true, nil
		}
		decision.Reason = "buy_leg_waiting"
	default:
		decision.Strategy = StrategyRangeReversion
		deviation := relativeBPS(price, slow)
		decision.SignalBPS = deviation
		if nextSell && deviation >= int32(rangeEdge) {
			decision.Reason = "range_high_sell"
			return decision, true, nil
		}
		if !nextSell && deviation <= -int32(rangeEdge) {
			decision.Reason = "range_low_buy"
			return decision, true, nil
		}
		decision.Reason = "signal_below_cost_hurdle"
	}
	return decision, false, nil
}

func (s *adaptiveStrategy) filled(at time.Time, riskExit bool) {
	if s != nil {
		s.lastFill = at.UTC()
		s.riskHalted = s.riskHalted || riskExit
	}
}

func classifyRegime(signal int32, edge uint16) string {
	if signal >= int32(edge) {
		return RegimeUptrend
	}
	if signal <= -int32(edge) {
		return RegimeDowntrend
	}
	return RegimeRange
}

func meanPrice(prices []uint64) uint64 {
	var total float64
	for _, price := range prices {
		total += float64(price)
	}
	return uint64(math.Round(total / float64(len(prices))))
}

// returnVolatilityBPS measures dispersion of one-period returns. Measuring
// price levels instead would label a smooth trend as volatile merely because
// the window has a slope.
func returnVolatilityBPS(prices []uint64) uint16 {
	if len(prices) < 2 {
		return 0
	}
	returns := make([]int32, 0, len(prices)-1)
	var mean float64
	for index := 1; index < len(prices); index++ {
		value := relativeBPS(prices[index], prices[index-1])
		returns = append(returns, value)
		mean += float64(value)
	}
	mean /= float64(len(returns))
	var sum float64
	for _, value := range returns {
		delta := float64(value) - mean
		sum += delta * delta
	}
	value := math.Sqrt(sum / float64(len(returns)))
	if value >= math.MaxUint16 {
		return math.MaxUint16
	}
	return uint16(math.Round(value))
}

func adaptiveCostFloorBPS(
	slippageBPS uint16, feeLamports, inputAmount uint64,
) (uint32, error) {
	return adaptiveValueCostFloorBPS(slippageBPS, feeLamports, inputAmount)
}

func adaptiveSignalCostFloorBPS(
	version uint32, slippageBPS uint16, feeUnits, inputUnits uint64,
) (uint32, error) {
	if version == adaptiveLegacyVersion {
		return adaptiveValueCostFloorBPS(slippageBPS, feeUnits, inputUnits)
	}
	if version != AdaptiveVersion {
		return 0, errors.New("adaptive policy version is not supported")
	}
	return adaptiveValueCostFloorBPS(0, feeUnits, inputUnits)
}

func adaptiveValueCostFloorBPS(
	slippageBPS uint16, feeUnits, inputUnits uint64,
) (uint32, error) {
	if inputUnits == 0 {
		return 0, errors.New("adaptive cost floor needs a positive trade amount")
	}
	high, low := bits.Mul64(feeUnits, 10_000)
	if high >= inputUnits {
		return 0, errors.New("adaptive fee cost is outside the supported range")
	}
	feeBPS, remainder := bits.Div64(high, low, inputUnits)
	if remainder != 0 {
		if feeBPS == math.MaxUint64 {
			return 0, errors.New("adaptive fee cost is outside the supported range")
		}
		feeBPS++
	}
	base := uint64(slippageBPS)*2 + 10
	if base > 2_000 || feeBPS > (2_000-base)/2 {
		return 0, errors.New("adaptive round-trip cost exceeds the supported signal limit")
	}
	cost := base + feeBPS*2
	return uint32(cost), nil
}

func adaptiveQuoteCostFloorBPS(
	slippageBPS uint16,
	feeLamports, nativeFeePriceMicros, quoteAmount uint64,
	quoteDecimals uint8,
) (uint32, error) {
	return adaptiveQuoteSignalCostFloorBPS(
		adaptiveLegacyVersion, slippageBPS, feeLamports, nativeFeePriceMicros,
		quoteAmount, quoteDecimals,
	)
}

func adaptiveQuoteSignalCostFloorBPS(
	version uint32, slippageBPS uint16,
	feeLamports, nativeFeePriceMicros, quoteAmount uint64,
	quoteDecimals uint8,
) (uint32, error) {
	feeMicros, err := valueAt(feeLamports, nativeFeePriceMicros, 9)
	if err != nil || feeMicros == 0 {
		return 0, errors.New("adaptive native fee value is outside the supported range")
	}
	inputMicros, err := scaleToMicros(quoteAmount, quoteDecimals)
	if err != nil || inputMicros == 0 {
		return 0, errors.New("adaptive quote budget is outside the supported range")
	}
	return adaptiveSignalCostFloorBPS(version, slippageBPS, feeMicros, inputMicros)
}

func adaptiveQuotePasses(
	policy Policy, decision *AdaptiveDecision, quote Quote, price uint64, sell bool,
) (bool, error) {
	return adaptiveQuotePassesWithHurdle(policy, decision, quote, price, sell, 0)
}

func adaptiveQuotePassesWithHurdle(
	policy Policy, decision *AdaptiveDecision, quote Quote, price uint64, sell bool, hurdle uint32,
) (bool, error) {
	if policy.Adaptive == nil {
		return true, nil
	}
	if decision == nil {
		return false, errors.New("adaptive quote guard needs its market decision")
	}
	if !quoteMatchesSlippage(policy.SlippageBPS, quote) {
		return false, nil
	}
	impact, bounded, err := adaptiveQuoteImpact(policy, quote, price, sell)
	if err != nil {
		return false, err
	}
	if !bounded {
		return false, nil
	}
	if decision.Strategy == StrategyRiskExit {
		return true, nil
	}
	if hurdle == 0 {
		dynamicFloor, floorErr := adaptiveTradeCostFloorBPS(policy, quote, price, sell)
		if floorErr != nil {
			return false, nil
		}
		hurdle = max(uint32(policy.Adaptive.MinimumSignalBPS), dynamicFloor)
	}
	required := uint64(hurdle)
	if impact < 0 {
		required += uint64(-(int64(impact)))
	}
	return uint64(magnitude32(decision.SignalBPS)) >= required, nil
}

// observedNativeCostHurdle changes only fee valuation, retaining the baseline's
// extra selectivity above its ceiling-derived opening cost floor. This is used
// only by the explicit offline experiment, never policy validation or runners.
func observedNativeCostHurdle(policy Policy, nativePrice, marketPrice, amount uint64, sell bool) (uint32, error) {
	if policy.Adaptive == nil || policy.Adaptive.Version != AdaptiveVersion || policy.NativeFeePrice == nil ||
		nativePrice == 0 || nativePrice > policy.NativeFeePriceCeilingMicros || amount == 0 {
		return 0, errors.New("observed native cost needs bounded native price and input")
	}
	openingFloor, err := adaptiveQuoteSignalCostFloorBPS(AdaptiveVersion, policy.SlippageBPS,
		policy.FeeLamports, policy.NativeFeePriceCeilingMicros, policy.InputAmount, policy.InputDecimals)
	if err != nil || uint32(policy.Adaptive.MinimumSignalBPS) < openingFloor {
		return 0, errors.New("observed native cost needs a valid baseline hurdle")
	}
	inputValue, err := scaleToMicros(amount, quoteDecimalsFor(policy))
	if sell {
		inputValue, err = valueAt(amount, marketPrice, baseDecimalsFor(policy))
	}
	if err != nil {
		return 0, err
	}
	// Round native fees up, not down to a USD micro before the basis-point
	// ceiling: a fractional micro can cross an exact per-leg bps boundary.
	high, low := bits.Mul64(policy.FeeLamports, nativePrice)
	if high >= 1_000_000_000 {
		return 0, errors.New("observed native fee value overflows")
	}
	feeValue, remainder := bits.Div64(high, low, 1_000_000_000)
	if remainder != 0 {
		if feeValue == math.MaxUint64 {
			return 0, errors.New("observed native fee value overflows")
		}
		feeValue++
	}
	if feeValue == 0 {
		return 0, errors.New("observed native fee value is invalid")
	}
	floor, err := adaptiveSignalCostFloorBPS(AdaptiveVersion, policy.SlippageBPS, feeValue, inputValue)
	if err != nil {
		return 0, err
	}
	return floor + uint32(policy.Adaptive.MinimumSignalBPS) - openingFloor, nil
}

func adaptiveTradeCostFloorBPS(
	policy Policy, quote Quote, marketPrice uint64, sell bool,
) (uint32, error) {
	if !usesSeparateNativePrice(policy) {
		baseAmount := quote.InputAmount
		if !sell {
			baseAmount = quote.MinimumOutput
		}
		return adaptiveSignalCostFloorBPS(
			policy.Adaptive.Version, policy.SlippageBPS, policy.FeeLamports, baseAmount,
		)
	}
	feeMicros, err := valueAt(
		policy.FeeLamports, policy.NativeFeePriceCeilingMicros, 9,
	)
	if err != nil {
		return 0, err
	}
	var inputMicros uint64
	if sell {
		inputMicros, err = valueAt(quote.InputAmount, marketPrice, policy.InputDecimals)
	} else {
		inputMicros, err = scaleToMicros(quote.InputAmount, policy.InputDecimals)
	}
	if err != nil || inputMicros == 0 {
		return 0, errors.New("adaptive trade value is outside the supported range")
	}
	return adaptiveSignalCostFloorBPS(
		policy.Adaptive.Version, policy.SlippageBPS, feeMicros, inputMicros,
	)
}

// adaptiveQuoteImpact checks both sides of the independently observed market.
// A venue quote that is impossibly favorable is as unusable for paper evidence
// as one that is too adverse: either can manufacture a result that real
// execution could never obtain. The minimum output is checked too so the
// executable floor cannot hide outside the same bound.
func adaptiveQuoteImpact(
	policy Policy, quote Quote, price uint64, sell bool,
) (int32, bool, error) {
	if policy.Adaptive == nil {
		return 0, true, nil
	}
	quoted, err := quotedPriceMicrosDirected(policy, quote, sell)
	if err != nil {
		return 0, false, err
	}
	impact, err := AdvantageBPS(price, quoted, sell)
	if err != nil {
		return 0, false, err
	}
	worst := quote
	worst.EstimatedOutput = quote.MinimumOutput
	worstQuoted, err := quotedPriceMicrosDirected(policy, worst, sell)
	if err != nil {
		return 0, false, err
	}
	worstImpact, err := AdvantageBPS(price, worstQuoted, sell)
	if err != nil {
		return 0, false, err
	}
	limit := uint32(policy.Adaptive.MaxQuoteImpactBPS)
	return impact, magnitude32(impact) <= limit && magnitude32(worstImpact) <= limit, nil
}

func quoteMatchesSlippage(slippageBPS uint16, quote Quote) bool {
	high, low := bits.Mul64(quote.EstimatedOutput, uint64(10_000-slippageBPS))
	minimum, remainder := bits.Div64(high, low, 10_000)
	if remainder != 0 {
		minimum++
	}
	return quote.MinimumOutput >= minimum
}

func relativeBPS(value, reference uint64) int32 {
	if reference == 0 {
		return 0
	}
	result := (float64(value) - float64(reference)) * 10_000 / float64(reference)
	if result > math.MaxInt32 {
		return math.MaxInt32
	}
	if result < math.MinInt32 {
		return math.MinInt32
	}
	return int32(math.Round(result))
}

func currentDrawdownBPS(ledger Ledger, price uint64) (uint16, error) {
	if ledger.PeakEquityMicros == 0 {
		return 0, nil
	}
	equity, err := ledger.EquityMicros(price)
	if err != nil {
		return 0, err
	}
	if equity >= ledger.PeakEquityMicros {
		return 0, nil
	}
	return drawdownBPS(ledger.PeakEquityMicros, equity), nil
}

func magnitude32(value int32) uint32 {
	if value < 0 {
		return uint32(-(int64(value)))
	}
	return uint32(value)
}
