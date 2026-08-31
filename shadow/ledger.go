package shadow

import (
	"errors"
	"math"
	"math/big"
	"math/bits"
)

// The ledger is kept in two assets, named for their role rather than their
// ticker: base is the thing whose price moves, quote is what that price is
// denominated in (normally a dollar stable). A sell spends base and receives quote; a
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
	// FeeReserveLamports is the liquid native SOL for modeled transaction fees
	// and setup rent that has not yet been locked. It is separate from BaseUnits
	// so another base asset cannot pay native costs out of its token inventory.
	FeeReserveLamports   uint64 `json:"fee_reserve_lamports,omitempty"`
	NativeFeePriceMicros uint64 `json:"native_fee_price_micros,omitempty"`
	// LockedRentLamports is native capital retained in user-owned token setup
	// accounts. It remains equity and is never reported as a trading fee.
	LockedRentLamports uint64 `json:"locked_rent_lamports,omitempty"`

	// CostBasisMicros is the exact total cost of the base currently held.
	// Carrying the total rather than a per-unit average matters: re-deriving a
	// running average from an already-truncated average rounds down every time,
	// and because the bias only ever goes one way it compounds — understating
	// cost, and so overstating profit, a little more with every trade.
	CostBasisMicros uint64 `json:"cost_basis_micros"`
	// FeeReserveCostBasisMicros tracks the basis of the remaining native SOL
	// reserve. Together with CostBasisMicros it preserves exact total basis when
	// SOL moves between traded inventory and the fee reserve.
	FeeReserveCostBasisMicros uint64 `json:"fee_reserve_cost_basis_micros,omitempty"`
	LockedRentCostBasisMicros uint64 `json:"locked_rent_cost_basis_micros,omitempty"`
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
	openingFeeReserve   uint64
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
func NewLedger(policy Policy, openingPriceMicros uint64, nativePrice ...uint64) (Ledger, error) {
	if err := policy.Validate(); err != nil {
		return Ledger{}, err
	}
	if openingPriceMicros == 0 {
		return Ledger{}, errZeroReference
	}
	nativePriceMicros, err := nativeFeePrice(policy, openingPriceMicros, nativePrice)
	if err != nil {
		return Ledger{}, err
	}
	base, quote := policy.StartingInputUnits, policy.StartingOutputUnits
	if !policy.IsSell() {
		base, quote = policy.StartingOutputUnits, policy.StartingInputUnits
	}
	reserve := policy.StartingFeeReserveLamports
	baseBasis, reserveBasis := uint64(0), uint64(0)
	if usesSeparateNativePrice(policy) {
		baseBasis, err = valueAt(base, openingPriceMicros, baseDecimalsFor(policy))
		if err != nil {
			return Ledger{}, err
		}
		reserveBasis, err = valueAt(reserve, nativePriceMicros, 9)
		if err != nil {
			return Ledger{}, err
		}
	} else {
		baseWithReserve, addErr := addUnits(base, reserve)
		if addErr != nil {
			return Ledger{}, addErr
		}
		opening, valueErr := valueAt(baseWithReserve, openingPriceMicros, baseDecimalsFor(policy))
		if valueErr != nil {
			return Ledger{}, valueErr
		}
		baseBasis = opening
		if reserve != 0 {
			baseBasis = shareOf(opening, base, baseWithReserve)
			reserveBasis = opening - baseBasis
		}
	}
	average, err := averageCost(baseBasis, base, baseDecimalsFor(policy))
	if err != nil {
		return Ledger{}, err
	}
	storedNativePrice := uint64(0)
	if usesSeparateNativePrice(policy) {
		storedNativePrice = nativePriceMicros
	}
	ledger := Ledger{
		Policy: policy, BaseUnits: base, QuoteUnits: quote,
		FeeReserveLamports: reserve, NativeFeePriceMicros: storedNativePrice,
		CostBasisMicros: baseBasis, FeeReserveCostBasisMicros: reserveBasis,
		AverageCostMicros: average,
	}
	equity, err := ledger.EquityMicros(openingPriceMicros)
	if err != nil {
		return Ledger{}, err
	}
	ledger.OpeningEquityMicros, ledger.PeakEquityMicros = equity, equity
	ledger.openingBaseUnits, ledger.openingQuoteUnits = base, quote
	ledger.openingFeeReserve = reserve
	return ledger, nil
}

// Apply books a settled attempt and returns the resulting ledger. A refused
// submitted attempt moves no traded inventory but still pays its network fee.
func (l Ledger) Apply(fill Fill, markPriceMicros uint64, nativePrice ...uint64) (Ledger, error) {
	if markPriceMicros == 0 {
		return Ledger{}, errZeroReference
	}
	nativePriceMicros, err := nativeFeePrice(l.Policy, markPriceMicros, nativePrice)
	if err != nil {
		return Ledger{}, err
	}
	// A transaction fee is withdrawn before its instructions execute. Bought
	// SOL therefore cannot fund its own fee, and a sell must leave the fee.
	setupRent := l.setupRentFor(fill)
	nativeDebit, debitErr := addUnits(fill.FeeLamports, setupRent)
	if debitErr != nil {
		return Ledger{}, debitErr
	}
	if l.separateFeeReserve() {
		if nativeDebit > l.FeeReserveLamports ||
			fill.Filled && fill.Sell && fill.SpentUnits > l.BaseUnits {
			return Ledger{}, errInsufficientInventory
		}
	} else if fill.FeeLamports > l.BaseUnits ||
		(fill.Filled && fill.Sell && fill.SpentUnits > l.BaseUnits-fill.FeeLamports) {
		return Ledger{}, errInsufficientInventory
	}
	next := l
	if usesSeparateNativePrice(l.Policy) {
		next.NativeFeePriceMicros = nativePriceMicros
	}
	if !fill.Filled {
		if next, err := next.chargeFee(fill.FeeLamports, nativePriceMicros); err != nil {
			return Ledger{}, err
		} else {
			return next.mark(markPriceMicros, nativePriceMicros)
		}
	}
	if next, err = next.chargeFee(fill.FeeLamports, nativePriceMicros); err != nil {
		return Ledger{}, err
	}
	if next, err = next.lockSetupRent(setupRent); err != nil {
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
	return next.mark(markPriceMicros, nativePriceMicros)
}

// chargeFee removes the native fee and its proportional cost basis. It is
// shared by fills and modeled post-submit refusals so every execution path uses
// identical accounting.
func (l Ledger) chargeFee(feeLamports, markPriceMicros uint64) (Ledger, error) {
	if feeLamports == 0 {
		return l, nil
	}
	available := l.BaseUnits
	basisTotal := l.CostBasisMicros
	if l.separateFeeReserve() {
		available = l.FeeReserveLamports
		basisTotal = l.FeeReserveCostBasisMicros
	}
	if feeLamports > available {
		return Ledger{}, errInsufficientInventory
	}
	feeDecimals := l.baseDecimals()
	if l.separateFeeReserve() {
		feeDecimals = 9
	}
	feeMicros, err := valueAt(feeLamports, markPriceMicros, feeDecimals)
	if err != nil {
		return Ledger{}, err
	}
	basis := shareOf(basisTotal, feeLamports, available)
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
	if l.separateFeeReserve() {
		next.FeeReserveCostBasisMicros -= basis
		next.FeeReserveLamports -= feeLamports
	} else {
		next.CostBasisMicros -= basis
		next.BaseUnits -= feeLamports
	}
	next.FeesMicros = fees
	next.RealizedMicros = realized
	if !l.separateFeeReserve() {
		if next.AverageCostMicros, err = averageCost(
			next.CostBasisMicros, next.BaseUnits, l.baseDecimals(),
		); err != nil {
			return Ledger{}, err
		}
	}
	return next, nil
}

func (l Ledger) setupRentFor(fill Fill) uint64 {
	if fill.Filled && l.Policy.OneTimeSetupRentLamports != 0 && l.LockedRentLamports == 0 {
		return l.Policy.OneTimeSetupRentLamports
	}
	return 0
}

func (l Ledger) lockSetupRent(rentLamports uint64) (Ledger, error) {
	if rentLamports == 0 {
		return l, nil
	}
	if !l.separateFeeReserve() || rentLamports > l.FeeReserveLamports ||
		l.LockedRentLamports != 0 {
		return Ledger{}, errInsufficientInventory
	}
	basis := shareOf(l.FeeReserveCostBasisMicros, rentLamports, l.FeeReserveLamports)
	next := l
	next.FeeReserveLamports -= rentLamports
	next.FeeReserveCostBasisMicros -= basis
	next.LockedRentLamports = rentLamports
	next.LockedRentCostBasisMicros = basis
	return next, nil
}

func canFundAttempt(ledger Ledger, sell bool, amount, reserveLamports uint64) bool {
	if amount == 0 {
		return false
	}
	if ledger.separateFeeReserve() {
		if reserveLamports > ledger.FeeReserveLamports {
			return false
		}
		if sell {
			return amount <= ledger.BaseUnits
		}
		return amount <= ledger.QuoteUnits
	}
	if reserveLamports > ledger.BaseUnits {
		return false
	}
	if sell {
		return amount <= ledger.BaseUnits-reserveLamports
	}
	return amount <= ledger.QuoteUnits
}

func capSellAmount(amount uint64, ledger Ledger, reserveLamports uint64) uint64 {
	if ledger.separateFeeReserve() {
		if reserveLamports > ledger.FeeReserveLamports {
			return 0
		}
		return min(amount, ledger.BaseUnits)
	}
	if reserveLamports >= ledger.BaseUnits {
		return 0
	}
	return min(amount, ledger.BaseUnits-reserveLamports)
}

// replenishFeeReserve moves newly bought SOL into the native fee bucket. The
// total units and total basis do not change; only their role in the paper book
// does. A small fill tops up as far as it can and the next funding check then
// refuses another leg honestly.
func (l Ledger) replenishFeeReserve(targetLamports uint64) (Ledger, error) {
	if !l.separateFeeReserve() || usesSeparateNativePrice(l.Policy) ||
		l.FeeReserveLamports >= targetLamports {
		return l, nil
	}
	move := min(targetLamports-l.FeeReserveLamports, l.BaseUnits)
	if move == 0 {
		return l, nil
	}
	basis := shareOf(l.CostBasisMicros, move, l.BaseUnits)
	next := l
	next.BaseUnits -= move
	next.FeeReserveLamports += move
	next.CostBasisMicros -= basis
	reserveBasis, err := addMagnitude(next.FeeReserveCostBasisMicros, basis)
	if err != nil {
		return Ledger{}, err
	}
	next.FeeReserveCostBasisMicros = reserveBasis
	if next.AverageCostMicros, err = averageCost(
		next.CostBasisMicros, next.BaseUnits, next.baseDecimals(),
	); err != nil {
		return Ledger{}, err
	}
	return next, nil
}

func roundTripFeeReserve(feeLamports uint64) uint64 { return 2 * feeLamports }

func nextSellFeeReserve(policy Policy) uint64 {
	if usesSeparateNativePrice(policy) {
		return policy.FeeLamports
	}
	return roundTripFeeReserve(policy.FeeLamports)
}

func attemptFeeReserve(policy Policy, sell bool) uint64 {
	if sell && policy.RoundTrip() {
		return roundTripFeeReserve(policy.FeeLamports)
	}
	return policy.FeeLamports
}

func paperAttempt(
	policy Policy, ledger Ledger, sell bool, normalAmount uint64, decision *AdaptiveDecision,
) (uint64, uint64) {
	if decision != nil && decision.Strategy == StrategyRiskExit && sell {
		if ledger.separateFeeReserve() {
			reserve := policy.FeeLamports
			if ledger.LockedRentLamports == 0 {
				if policy.OneTimeSetupRentLamports > math.MaxUint64-reserve {
					return 0, math.MaxUint64
				}
				reserve += policy.OneTimeSetupRentLamports
			}
			if ledger.FeeReserveLamports < reserve {
				return 0, reserve
			}
			return ledger.BaseUnits, reserve
		}
		if ledger.BaseUnits <= policy.FeeLamports {
			return 0, policy.FeeLamports
		}
		return ledger.BaseUnits - policy.FeeLamports, policy.FeeLamports
	}
	reserve := attemptFeeReserve(policy, sell)
	if ledger.separateFeeReserve() {
		reserve = policy.FeeLamports
	}
	if policy.OneTimeSetupRentLamports != 0 && ledger.LockedRentLamports == 0 {
		if policy.OneTimeSetupRentLamports > math.MaxUint64-reserve {
			return 0, math.MaxUint64
		}
		reserve += policy.OneTimeSetupRentLamports
	}
	// Admission measures the buy route at one exact USDC notional. Profits may
	// grow later quote inventory, but they cannot silently increase the next
	// risk-adding buy beyond the amount that was qualified.
	if policy.Version == AdmittedVersion && !sell {
		normalAmount = min(normalAmount, policy.InputAmount)
	}
	return normalAmount, reserve
}

// mark revalues the book at the current price and updates the high-water mark
// and the worst peak-to-trough fall seen so far.
func (l Ledger) mark(priceMicros uint64, nativePrice ...uint64) (Ledger, error) {
	if priceMicros == 0 {
		return Ledger{}, errZeroReference
	}
	nativePriceMicros, err := nativeFeePrice(l.Policy, priceMicros, nativePrice)
	if err != nil {
		return Ledger{}, err
	}
	next := l
	if usesSeparateNativePrice(l.Policy) {
		next.NativeFeePriceMicros = nativePriceMicros
	}
	equity, err := next.EquityMicros(priceMicros)
	if err != nil {
		return Ledger{}, err
	}
	if equity > next.PeakEquityMicros {
		next.PeakEquityMicros = equity
	}
	if fall := next.PeakEquityMicros - min(equity, next.PeakEquityMicros); fall > next.MaxDrawdownMicros {
		next.MaxDrawdownMicros = fall
	}
	return next, nil
}

// Mark revalues without trading, so a flat day still records its drawdown.
func (l Ledger) Mark(priceMicros uint64, nativePrice ...uint64) (Ledger, error) {
	return l.mark(priceMicros, nativePrice...)
}

// EquityMicros is everything held, valued in USD micros at the given price.
func (l Ledger) EquityMicros(priceMicros uint64) (uint64, error) {
	var base uint64
	var err error
	if usesSeparateNativePrice(l.Policy) {
		base, err = valueAt(l.BaseUnits, priceMicros, l.baseDecimals())
		if err == nil {
			var reserve uint64
			var nativeUnits uint64
			nativeUnits, err = addUnits(l.FeeReserveLamports, l.LockedRentLamports)
			if err == nil {
				reserve, err = valueAt(nativeUnits, l.NativeFeePriceMicros, 9)
			}
			if err == nil {
				base, err = addMagnitude(base, reserve)
			}
		}
	} else {
		var baseUnits uint64
		baseUnits, err = addUnits(l.BaseUnits, l.FeeReserveLamports)
		if err == nil {
			baseUnits, err = addUnits(baseUnits, l.LockedRentLamports)
		}
		if err == nil {
			base, err = valueAt(baseUnits, priceMicros, l.baseDecimals())
		}
	}
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
	var current uint64
	var err error
	if usesSeparateNativePrice(l.Policy) {
		current, err = valueAt(l.BaseUnits, priceMicros, l.baseDecimals())
		if err == nil {
			var reserve uint64
			var nativeUnits uint64
			nativeUnits, err = addUnits(l.FeeReserveLamports, l.LockedRentLamports)
			if err == nil {
				reserve, err = valueAt(nativeUnits, l.NativeFeePriceMicros, 9)
			}
			if err == nil {
				current, err = addMagnitude(current, reserve)
			}
		}
	} else {
		var baseUnits uint64
		baseUnits, err = addUnits(l.BaseUnits, l.FeeReserveLamports)
		if err == nil {
			baseUnits, err = addUnits(baseUnits, l.LockedRentLamports)
		}
		if err == nil {
			current, err = valueAt(baseUnits, priceMicros, l.baseDecimals())
		}
	}
	if err != nil {
		return 0, err
	}
	signedCurrent, err := signed(current)
	if err != nil {
		return 0, err
	}
	totalBasis, err := addMagnitude(l.CostBasisMicros, l.FeeReserveCostBasisMicros)
	if err == nil {
		totalBasis, err = addMagnitude(totalBasis, l.LockedRentCostBasisMicros)
	}
	if err != nil {
		return 0, err
	}
	signedCost, err := signed(totalBasis)
	if err != nil {
		return 0, err
	}
	return signedCurrent - signedCost, nil
}

// HoldBenchmarkMicros is what doing nothing at all would have been worth. A
// strategy that does not beat it has not earned the risk it took.
func (l Ledger) HoldBenchmarkMicros(priceMicros uint64) (uint64, error) {
	var base uint64
	var err error
	if usesSeparateNativePrice(l.Policy) {
		base, err = valueAt(l.openingBaseUnits, priceMicros, l.baseDecimals())
		if err == nil {
			var reserve uint64
			reserve, err = valueAt(l.openingFeeReserve, l.NativeFeePriceMicros, 9)
			if err == nil {
				base, err = addMagnitude(base, reserve)
			}
		}
	} else {
		var baseUnits uint64
		baseUnits, err = addUnits(l.openingBaseUnits, l.openingFeeReserve)
		if err == nil {
			base, err = valueAt(baseUnits, priceMicros, l.baseDecimals())
		}
	}
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

func (l Ledger) separateFeeReserve() bool {
	return l.Policy.StartingFeeReserveLamports != 0
}

func usesSeparateNativePrice(policy Policy) bool { return policy.NativeFeePrice != nil }

func nativeFeePrice(policy Policy, marketPrice uint64, provided []uint64) (uint64, error) {
	if !usesSeparateNativePrice(policy) {
		if len(provided) > 1 || len(provided) == 1 && provided[0] != marketPrice {
			return 0, errors.New("SOL paper fee price must match its market price")
		}
		return marketPrice, nil
	}
	if len(provided) != 1 || provided[0] == 0 ||
		provided[0] > policy.NativeFeePriceCeilingMicros {
		return 0, errors.New("non-SOL paper accounting needs a bounded native fee price")
	}
	return provided[0], nil
}

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
