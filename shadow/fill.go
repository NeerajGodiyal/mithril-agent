package shadow

import (
	"errors"
	"math/big"
)

// Everything here is integer arithmetic on base units. Floating point would be
// easier to read and would quietly round the numbers a reader is being asked to
// trust, so it is not used anywhere in the accounting.

// Quote is the read-only result of asking the pool what a trade would cost.
type Quote struct {
	InputAmount     uint64 `json:"input_amount"`
	EstimatedOutput uint64 `json:"estimated_output"`
	MinimumOutput   uint64 `json:"minimum_output"`
}

// Fill is what would have happened. A refused fill is a first-class outcome:
// the slippage floor that protects a real trade also has to be allowed to stop
// a shadow one, or the shadow result overstates how often the strategy trades.
type Fill struct {
	Filled bool `json:"filled"`
	// Sell records which way THIS fill moved inventory. Direction used to be a
	// property of the policy, which is true for a one-directional run and false
	// for a round trip: holding SOL the only possible action is a sell, holding
	// devUSDC it is a buy, and both happen against one set of books.
	//
	// It does NOT decide which asset is "base". That stays policy-level and is
	// already right either way: base is the asset whose price moves.
	Sell bool `json:"sell"`
	// Refusal names why nothing happened, in the same terms the real guard
	// would have used. Empty when Filled.
	Refusal string `json:"refusal,omitempty"`

	SpentUnits    uint64 `json:"spent_units"`
	ReceivedUnits uint64 `json:"received_units"`
	FeeLamports   uint64 `json:"fee_lamports"`
	// DecisionQuote is the exact read-only quote used to derive this fill. It
	// lets replay prove every amount instead of trusting already-derived P&L.
	DecisionQuote Quote `json:"decision_quote"`

	DecisionPriceMicros uint64 `json:"decision_price_micros"`
	SettlePriceMicros   uint64 `json:"settle_price_micros"`
	QuotedPriceMicros   uint64 `json:"quoted_price_micros"`

	// ImpactBPS is what the pool costs against the oracle mid at decision time:
	// negative means the pool quoted worse than the reference price.
	ImpactBPS int32 `json:"impact_bps"`
	// SlippageBPS is what the delay costs: the difference between the quote
	// taken at decision time and what settled later. Negative is a loss.
	SlippageBPS int32 `json:"slippage_bps"`
}

const bpsScale = 10_000

var (
	errZeroReference = errors.New("reference price is zero")
	errPriceRange    = errors.New("price is outside the representable range")
)

// PriceMicros converts a quoted pair of base-unit amounts into a price for one
// whole unit of the input asset, in millionths of one output unit.
//
//	price = (output / 10^outputDecimals) / (input / 10^inputDecimals) * 1e6
//
// It is computed with big integers and truncated once at the end, so the result
// is reproducible on any machine and never rounds twice.
func PriceMicros(input, output uint64, inputDecimals, outputDecimals uint8) (uint64, error) {
	if input == 0 {
		return 0, errors.New("input amount is zero")
	}
	if inputDecimals > maxDecimals || outputDecimals > maxDecimals {
		return 0, errors.New("decimals are out of range")
	}
	numerator := new(big.Int).SetUint64(output)
	numerator.Mul(numerator, pow10(uint(inputDecimals)))
	numerator.Mul(numerator, big.NewInt(1_000_000))

	denominator := new(big.Int).SetUint64(input)
	denominator.Mul(denominator, pow10(uint(outputDecimals)))

	price := numerator.Div(numerator, denominator)
	if !price.IsUint64() {
		return 0, errPriceRange
	}
	return price.Uint64(), nil
}

// AdvantageBPS reports how much better than the reference a price is, from the
// trader's point of view, so the sign always means the same thing regardless of
// direction: positive is good for the trader, negative is a cost.
func AdvantageBPS(reference, actual uint64, isSell bool) (int32, error) {
	if reference == 0 {
		return 0, errZeroReference
	}
	difference := new(big.Int).SetUint64(actual)
	difference.Sub(difference, new(big.Int).SetUint64(reference))
	if !isSell {
		// Buying, a lower price is the good outcome.
		difference.Neg(difference)
	}
	difference.Mul(difference, big.NewInt(bpsScale))
	difference.Quo(difference, new(big.Int).SetUint64(reference))
	if !difference.IsInt64() {
		return 0, errPriceRange
	}
	value := difference.Int64()
	// Clamp rather than wrap: a nonsense input must not become a small number.
	if value > int64(^uint32(0)>>1) {
		value = int64(^uint32(0) >> 1)
	}
	if value < -int64(^uint32(0)>>1) {
		value = -int64(^uint32(0) >> 1)
	}
	return int32(value), nil
}

// QuotedPriceMicros expresses what the pool is charging in the SAME units as
// the oracle reference — the numeraire per one unit of the asset whose price
// moves.
//
// This has to invert with direction. Selling, the pool's output IS the
// numeraire, so output-per-input is already the right way round. Buying, the
// pool's output is the asset and its input is the numeraire, so the naive
// output-per-input is the reciprocal of the reference price. Comparing that to
// the oracle compares two quantities that are not commensurable, and the
// resulting "impact" is meaningless.
func QuotedPriceMicros(policy Policy, quote Quote) (uint64, error) {
	return quotedPriceMicrosDirected(policy, quote, policy.IsSell())
}

// quotedPriceMicrosDirected is QuotedPriceMicros for an explicitly named leg.
// A round-trip policy's input/output decimals describe its first leg, so using
// them unchanged for the return leg compares reciprocal quantities.
func quotedPriceMicrosDirected(policy Policy, quote Quote, sell bool) (uint64, error) {
	baseDecimals := baseDecimalsFor(policy)
	quoteDecimals := policy.OutputDecimals
	if !policy.IsSell() {
		quoteDecimals = policy.InputDecimals
	}
	if sell {
		return PriceMicros(
			quote.InputAmount, quote.EstimatedOutput,
			baseDecimals, quoteDecimals,
		)
	}
	return PriceMicros(
		quote.EstimatedOutput, quote.InputAmount,
		baseDecimals, quoteDecimals,
	)
}

// SettleFill scores a decision against a price observed strictly after it was
// made.
//
// The output the trade actually receives moves with the market during that
// delay: selling, a higher later price means more received; buying, a higher
// later price means less. If the result lands under the minimum the policy
// demanded, a real transaction would have been refused by its own slippage
// guard — so this refuses too, rather than booking an optimistic fill.
func SettleFill(policy Policy, quote Quote, decisionPrice, settlePrice uint64) (Fill, error) {
	return SettleFillDirected(policy, quote, decisionPrice, settlePrice, policy.IsSell())
}

// SettleFillDirected is SettleFill with the direction named explicitly, which a
// round trip needs: the same books take a sell while holding SOL and a buy
// while holding devUSDC, so direction cannot come from the policy.
func SettleFillDirected(
	policy Policy, quote Quote, decisionPrice, settlePrice uint64, sell bool,
) (Fill, error) {
	if decisionPrice == 0 || settlePrice == 0 {
		return Fill{}, errZeroReference
	}
	if quote.InputAmount == 0 || quote.EstimatedOutput == 0 || quote.MinimumOutput == 0 {
		return Fill{}, errors.New("quote is incomplete")
	}
	if quote.MinimumOutput > quote.EstimatedOutput {
		return Fill{}, errors.New("quote minimum exceeds its own estimate")
	}
	quoted, err := quotedPriceMicrosDirected(policy, quote, sell)
	if err != nil {
		return Fill{}, err
	}
	impact, err := AdvantageBPS(decisionPrice, quoted, sell)
	if err != nil {
		return Fill{}, err
	}

	received := new(big.Int).SetUint64(quote.EstimatedOutput)
	if sell {
		received.Mul(received, new(big.Int).SetUint64(settlePrice))
		received.Div(received, new(big.Int).SetUint64(decisionPrice))
	} else {
		received.Mul(received, new(big.Int).SetUint64(decisionPrice))
		received.Div(received, new(big.Int).SetUint64(settlePrice))
	}
	if !received.IsUint64() {
		return Fill{}, errPriceRange
	}

	fill := Fill{
		Sell:                sell,
		SpentUnits:          quote.InputAmount,
		FeeLamports:         policy.FeeLamports,
		DecisionQuote:       quote,
		DecisionPriceMicros: decisionPrice,
		SettlePriceMicros:   settlePrice,
		QuotedPriceMicros:   quoted,
		ImpactBPS:           impact,
	}
	settled := received.Uint64()
	slippage, err := AdvantageBPS(quote.EstimatedOutput, settled, true)
	if err != nil {
		return Fill{}, err
	}
	fill.SlippageBPS = slippage

	if settled < quote.MinimumOutput {
		// The trade never happens, so nothing is spent and no fee is paid.
		fill.Refusal = "the price moved past the slippage floor before it could settle"
		fill.SpentUnits, fill.FeeLamports = 0, 0
		return fill, nil
	}
	fill.Filled = true
	fill.ReceivedUnits = settled
	return fill, nil
}

func pow10(exponent uint) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), new(big.Int).SetUint64(uint64(exponent)), nil)
}
