package shadow

import (
	"errors"
	"math"
	"math/big"
	"math/bits"
)

// The ledger is kept in two assets, named for their role rather than their
// ticker: base is the thing whose price moves (SOL), quote is what that price
// is denominated in (a dollar stable). A sell spends base and receives quote; a
// buy does the reverse. Naming them this way means the accounting does not have
// to branch on direction in more than one place.
//
// Values are integers throughout. Equity, cost basis and profit are all carried
// in USD micros; inventory is carried in each asset's own base units.

// Ledger is a value, not a mutable account. Applying a fill returns a new
// ledger, so a caller cannot accidentally half-apply one and keep going with a
// ledger that never existed.
type Ledger struct {
	Policy Policy `json:"-"`

	BaseUnits  uint64 `json:"base_units"`
	QuoteUnits uint64 `json:"quote_units"`

	// CostBasisMicros is the exact total cost of the base currently held.
	// Carrying the total rather than a per-unit average matters: re-deriving a
	// running average from an already-truncated average rounds down every time,
	// and because the bias only ever goes one way it compounds — understating
	// cost, and so overstating profit, a little more with every trade.
	CostBasisMicros uint64 `json:"cost_basis_micros"`
	// AverageCostMicros is that basis per whole unit, derived for display.
	// Opening inventory is marked at the first observed price so realized profit
	// measures what the strategy did, not what the market did before it began.
	AverageCostMicros uint64 `json:"average_cost_micros"`

	RealizedMicros int64  `json:"realized_micros"`
	FeesMicros     int64  `json:"fees_micros"`
	TurnoverMicros uint64 `json:"turnover_micros"`
	Fills          uint64 `json:"fills"`

	PeakEquityMicros    uint64 `json:"peak_equity_micros"`
	MaxDrawdownMicros   uint64 `json:"max_drawdown_micros"`
	OpeningEquityMicros uint64 `json:"opening_equity_micros"`
	openingBaseUnits    uint64
	openingQuoteUnits   uint64
}

var (
	errInsufficientInventory = errors.New("not enough inventory to make this trade")
	errUnrepresentable       = errors.New("value is too large to account for")
)

// signed converts a magnitude to a signed amount, refusing rather than wrapping.
// A silent wrap here would turn an enormous profit into an enormous loss, which
// is the single worst thing an accounting bug can do to a report.
func signed(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, errUnrepresentable
	}
	return int64(value), nil
}

// NewLedger opens the books, marking the starting inventory at the first price
// actually observed.
func NewLedger(policy Policy, openingPriceMicros uint64) (Ledger, error) {
	if err := policy.Validate(); err != nil {
		return Ledger{}, err
	}
	if openingPriceMicros == 0 {
		return Ledger{}, errZeroReference
	}
	base, quote := policy.StartingInputUnits, policy.StartingOutputUnits
	if !policy.IsSell() {
		base, quote = policy.StartingOutputUnits, policy.StartingInputUnits
	}
	opening, err := valueAt(base, openingPriceMicros, baseDecimalsFor(policy))
	if err != nil {
		return Ledger{}, err
	}
	ledger := Ledger{
		Policy: policy, BaseUnits: base, QuoteUnits: quote,
		CostBasisMicros: opening, AverageCostMicros: openingPriceMicros,
	}
	equity, err := ledger.EquityMicros(openingPriceMicros)
	if err != nil {
		return Ledger{}, err
	}
	ledger.OpeningEquityMicros, ledger.PeakEquityMicros = equity, equity
	ledger.openingBaseUnits, ledger.openingQuoteUnits = base, quote
	return ledger, nil
}

// Apply books a settled attempt and returns the resulting ledger. A refused
// submitted attempt moves no traded inventory but still pays its network fee.
func (l Ledger) Apply(fill Fill, markPriceMicros uint64) (Ledger, error) {
	if markPriceMicros == 0 {
		return Ledger{}, errZeroReference
	}
	// A transaction fee is withdrawn before its instructions execute. Bought
	// SOL therefore cannot fund its own fee, and a sell must leave the fee.
	if fill.FeeLamports > l.BaseUnits ||
		(fill.Filled && fill.Sell && fill.SpentUnits > l.BaseUnits-fill.FeeLamports) {
		return Ledger{}, errInsufficientInventory
	}
	next := l
	if !fill.Filled {
		if next, err := next.chargeFee(fill.FeeLamports, markPriceMicros); err != nil {
			return Ledger{}, err
		} else {
			return next.mark(markPriceMicros)
		}
	}
	var err error
	if next, err = next.chargeFee(fill.FeeLamports, markPriceMicros); err != nil {
		return Ledger{}, err
	}
	quoteDecimals := next.quoteDecimals()

	if fill.Sell {
		if fill.SpentUnits > next.BaseUnits {
			return Ledger{}, errInsufficientInventory
		}
		proceeds, err := scaleToMicros(fill.ReceivedUnits, quoteDecimals)
		if err != nil {
			return Ledger{}, err
		}
		// Proportional to what is actually held, so the basis is exact rather
		// than reconstructed from a rounded average.
		cost := shareOf(next.CostBasisMicros, fill.SpentUnits, next.BaseUnits)
		signedProceeds, err := signed(proceeds)
		if err != nil {
			return Ledger{}, err
		}
		signedCost, err := signed(cost)
		if err != nil {
			return Ledger{}, err
		}
		quoteUnits, err := addUnits(next.QuoteUnits, fill.ReceivedUnits)
		if err != nil {
			return Ledger{}, err
		}
		realized, err := addSigned(next.RealizedMicros, signedProceeds-signedCost)
		if err != nil {
			return Ledger{}, err
		}
		turnover, err := addMagnitude(next.TurnoverMicros, proceeds)
		if err != nil {
			return Ledger{}, err
		}
		next.BaseUnits -= fill.SpentUnits
		next.CostBasisMicros -= cost
		next.QuoteUnits = quoteUnits
		next.RealizedMicros = realized
		next.TurnoverMicros = turnover
	} else {
		if fill.SpentUnits > next.QuoteUnits {
			return Ledger{}, errInsufficientInventory
		}
		spent, err := scaleToMicros(fill.SpentUnits, quoteDecimals)
		if err != nil {
			return Ledger{}, err
		}
		baseUnits, err := addUnits(next.BaseUnits, fill.ReceivedUnits)
		if err != nil {
			return Ledger{}, err
		}
		costBasis, err := addMagnitude(next.CostBasisMicros, spent)
		if err != nil {
			return Ledger{}, err
		}
		turnover, err := addMagnitude(next.TurnoverMicros, spent)
		if err != nil {
			return Ledger{}, err
		}
		// Buying simply adds what it cost to the basis of everything held.
		next.BaseUnits = baseUnits
		next.CostBasisMicros = costBasis
		next.QuoteUnits -= fill.SpentUnits
		next.TurnoverMicros = turnover
	}

	if next.AverageCostMicros, err = averageCost(
		next.CostBasisMicros, next.BaseUnits, next.baseDecimals(),
	); err != nil {
		return Ledger{}, err
	}
	fills, err := addUnits(next.Fills, 1)
	if err != nil {
		return Ledger{}, err
	}
	next.Fills = fills
	return next.mark(markPriceMicros)
}

// chargeFee removes the native fee and its proportional cost basis. It is
// shared by fills and modeled post-submit refusals so every execution path uses
// identical accounting.
func (l Ledger) chargeFee(feeLamports, markPriceMicros uint64) (Ledger, error) {
	if feeLamports == 0 {
		return l, nil
	}
	if feeLamports > l.BaseUnits {
		return Ledger{}, errInsufficientInventory
	}
	feeMicros, err := valueAt(feeLamports, markPriceMicros, l.baseDecimals())
	if err != nil {
		return Ledger{}, err
	}
	basis := shareOf(l.CostBasisMicros, feeLamports, l.BaseUnits)
	signedBasis, err := signed(basis)
	if err != nil {
		return Ledger{}, err
	}
	signedFee, err := signed(feeMicros)
	if err != nil {
		return Ledger{}, err
	}
	fees, err := addSigned(l.FeesMicros, signedFee)
	if err != nil {
		return Ledger{}, err
	}
	realized, err := addSigned(l.RealizedMicros, -signedBasis)
	if err != nil {
		return Ledger{}, err
	}
	// Treat the fee as a disposal at the current mark followed by an equal fee
	// expense. The mark terms cancel, so the net realized change is the removed
	// basis. FeesMicros still records the fee's current value for reporting.
	next := l
	next.CostBasisMicros -= basis
	next.BaseUnits -= feeLamports
	next.FeesMicros = fees
	next.RealizedMicros = realized
	if next.AverageCostMicros, err = averageCost(
		next.CostBasisMicros, next.BaseUnits, l.baseDecimals(),
	); err != nil {
		return Ledger{}, err
	}
	return next, nil
}

func canFundAttempt(ledger Ledger, sell bool, amount, reserveLamports uint64) bool {
	if amount == 0 || reserveLamports > ledger.BaseUnits {
		return false
	}
	if sell {
		return amount <= ledger.BaseUnits-reserveLamports
	}
	return amount <= ledger.QuoteUnits
}

func capSellAmount(amount, baseUnits, reserveLamports uint64) uint64 {
	if reserveLamports >= baseUnits {
		return 0
	}
	return min(amount, baseUnits-reserveLamports)
}

func roundTripFeeReserve(feeLamports uint64) uint64 { return 2 * feeLamports }

func attemptFeeReserve(policy Policy, sell bool) uint64 {
	if sell && policy.RoundTrip() {
		return roundTripFeeReserve(policy.FeeLamports)
	}
	return policy.FeeLamports
}

// mark revalues the book at the current price and updates the high-water mark
// and the worst peak-to-trough fall seen so far.
func (l Ledger) mark(priceMicros uint64) (Ledger, error) {
	if priceMicros == 0 {
		return Ledger{}, errZeroReference
	}
	equity, err := l.EquityMicros(priceMicros)
	if err != nil {
		return Ledger{}, err
	}
	next := l
	if equity > next.PeakEquityMicros {
		next.PeakEquityMicros = equity
	}
	if fall := next.PeakEquityMicros - min(equity, next.PeakEquityMicros); fall > next.MaxDrawdownMicros {
		next.MaxDrawdownMicros = fall
	}
	return next, nil
}

// Mark revalues without trading, so a flat day still records its drawdown.
func (l Ledger) Mark(priceMicros uint64) (Ledger, error) { return l.mark(priceMicros) }

// EquityMicros is everything held, valued in USD micros at the given price.
func (l Ledger) EquityMicros(priceMicros uint64) (uint64, error) {
	base, err := valueAt(l.BaseUnits, priceMicros, l.baseDecimals())
	if err != nil {
		return 0, err
	}
	quote, err := scaleToMicros(l.QuoteUnits, l.quoteDecimals())
	if err != nil {
		return 0, err
	}
	return addMagnitude(base, quote)
}

// UnrealizedMicros is the profit sitting in inventory that has not been sold.
func (l Ledger) UnrealizedMicros(priceMicros uint64) (int64, error) {
	current, err := valueAt(l.BaseUnits, priceMicros, l.baseDecimals())
	if err != nil {
		return 0, err
	}
	signedCurrent, err := signed(current)
	if err != nil {
		return 0, err
	}
	signedCost, err := signed(l.CostBasisMicros)
	if err != nil {
		return 0, err
	}
	return signedCurrent - signedCost, nil
}

// HoldBenchmarkMicros is what doing nothing at all would have been worth. A
// strategy that does not beat it has not earned the risk it took.
func (l Ledger) HoldBenchmarkMicros(priceMicros uint64) (uint64, error) {
	base, err := valueAt(l.openingBaseUnits, priceMicros, l.baseDecimals())
	if err != nil {
		return 0, err
	}
	quote, err := scaleToMicros(l.openingQuoteUnits, l.quoteDecimals())
	if err != nil {
		return 0, err
	}
	return addMagnitude(base, quote)
}

func addUnits(left, right uint64) (uint64, error) {
	value, carry := bits.Add64(left, right, 0)
	if carry != 0 {
		return 0, errUnrepresentable
	}
	return value, nil
}

// USD micros eventually appear beside signed profit and loss, so magnitudes
// outside int64 are not representable even when uint64 addition itself fits.
func addMagnitude(left, right uint64) (uint64, error) {
	value, err := addUnits(left, right)
	if err != nil || value > math.MaxInt64 {
		return 0, errUnrepresentable
	}
	return value, nil
}

func addSigned(left, right int64) (int64, error) {
	if (right > 0 && left > math.MaxInt64-right) ||
		(right < 0 && left < math.MinInt64-right) {
		return 0, errUnrepresentable
	}
	return left + right, nil
}

func (l Ledger) baseDecimals() uint8 { return baseDecimalsFor(l.Policy) }

func baseDecimalsFor(policy Policy) uint8 {
	if policy.IsSell() {
		return policy.InputDecimals
	}
	return policy.OutputDecimals
}

// shareOf allocates a total in proportion to a part, exactly and only once.
func shareOf(total, part, whole uint64) uint64 {
	if whole == 0 || part == 0 {
		return 0
	}
	if part >= whole {
		return total
	}
	value := new(big.Int).SetUint64(total)
	value.Mul(value, new(big.Int).SetUint64(part))
	value.Div(value, new(big.Int).SetUint64(whole))
	if !value.IsUint64() {
		return total
	}
	return value.Uint64()
}

func (l Ledger) quoteDecimals() uint8 {
	if l.Policy.IsSell() {
		return l.Policy.OutputDecimals
	}
	return l.Policy.InputDecimals
}

// valueAt converts an amount of an asset into USD micros at a price expressed
// in USD micros per whole unit.
func valueAt(units, priceMicros uint64, decimals uint8) (uint64, error) {
	value := new(big.Int).SetUint64(units)
	value.Mul(value, new(big.Int).SetUint64(priceMicros))
	value.Div(value, pow10(uint(decimals)))
	if !value.IsUint64() {
		return 0, errPriceRange
	}
	return value.Uint64(), nil
}

// scaleToMicros converts an amount of a dollar-denominated asset into USD
// micros, which is a pure change of scale.
func scaleToMicros(units uint64, decimals uint8) (uint64, error) {
	value := new(big.Int).SetUint64(units)
	value.Mul(value, big.NewInt(1_000_000))
	value.Div(value, pow10(uint(decimals)))
	if !value.IsUint64() {
		return 0, errPriceRange
	}
	return value.Uint64(), nil
}

func averageCost(totalMicros, units uint64, decimals uint8) (uint64, error) {
	if units == 0 {
		return 0, nil
	}
	average := new(big.Int).SetUint64(totalMicros)
	average.Mul(average, pow10(uint(decimals)))
	average.Div(average, new(big.Int).SetUint64(units))
	if !average.IsUint64() {
		return 0, errPriceRange
	}
	return average.Uint64(), nil
}
