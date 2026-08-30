package shadow

import (
	"errors"
	"math"
	"testing"
)

func TestLedgerArithmeticRefusesEveryIntegerBoundaryWrap(t *testing.T) {
	if _, err := addUnits(math.MaxUint64, 1); err == nil {
		t.Fatal("unsigned addition wrapped")
	}
	if _, err := addMagnitude(math.MaxInt64, 1); err == nil {
		t.Fatal("USD magnitude crossed the signed reporting range")
	}
	if _, err := addSigned(math.MaxInt64, 1); err == nil {
		t.Fatal("positive signed addition wrapped")
	}
	if _, err := addSigned(math.MinInt64, -1); err == nil {
		t.Fatal("negative signed addition wrapped")
	}
}

func TestLedgerRefusesOpeningEquityOverflow(t *testing.T) {
	policy := sellPolicy()
	policy.StartingInputUnits = 1_000_000_000
	policy.StartingOutputUnits = ^uint64(0)
	if _, err := NewLedger(policy, 20_000_000); err == nil {
		t.Fatal("opening equity wrapped instead of being refused")
	}
}

func TestLedgerRefusesInventoryOverflow(t *testing.T) {
	policy := sellPolicy()
	policy.OutputDecimals = 18
	policy.StartingInputUnits = 1_000_000_000
	policy.StartingOutputUnits = ^uint64(0) - 10
	ledger, err := NewLedger(policy, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ledger.Apply(Fill{
		Filled: true, Sell: true, SpentUnits: 1, ReceivedUnits: 11, FeeLamports: 1,
	}, 20_000_000)
	if err == nil {
		t.Fatal("quote inventory wrapped instead of being refused")
	}
}

// A sell books the proceeds, drops the inventory, and charges the fee. The
// numbers are checked against hand arithmetic, not against the code.
func TestSellBooksProceedsCostAndFee(t *testing.T) {
	policy := sellPolicy()
	policy.StartingInputUnits = 1_000_000_000 // 1 SOL
	const opening = uint64(20_000_000)        // $20.00

	ledger, err := NewLedger(policy, opening)
	if err != nil {
		t.Fatal(err)
	}
	// Opening equity is one whole SOL at $20.
	if ledger.OpeningEquityMicros != 20_000_000 {
		t.Fatalf("opening equity = %d, want 20000000 ($20)", ledger.OpeningEquityMicros)
	}

	// Sell 0.1 SOL at $22, receiving 2.2 USDC.
	fill := Fill{
		Filled: true, Sell: true, SpentUnits: 100_000_000, ReceivedUnits: 2_200_000,
		FeeLamports: 5_000,
	}
	after, err := ledger.Apply(fill, 22_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if after.BaseUnits != 1_000_000_000-100_000_000-5_000 {
		t.Errorf("base units = %d", after.BaseUnits)
	}
	if after.QuoteUnits != 2_200_000 {
		t.Errorf("quote units = %d, want 2200000", after.QuoteUnits)
	}
	// Sold 0.1 SOL that cost $2 for $2.20. The fee removes its proportional
	// opening basis from realized profit while FeesMicros records its value at
	// the current mark.
	feeBasisMicros := int64(5_000) * 20_000_000 / 1_000_000_000
	if want := int64(200_000) - feeBasisMicros; after.RealizedMicros != want {
		t.Errorf("realized = %d micros, want %d", after.RealizedMicros, want)
	}
	feeMicros := int64(5_000) * 22_000_000 / 1_000_000_000
	if after.FeesMicros != feeMicros {
		t.Errorf("fees = %d micros, want %d", after.FeesMicros, feeMicros)
	}
	if after.TurnoverMicros != 2_200_000 {
		t.Errorf("turnover = %d, want 2200000", after.TurnoverMicros)
	}
	if after.Fills != 1 {
		t.Errorf("fills = %d, want 1", after.Fills)
	}
}

// The fee must always be charged, including when the trade is otherwise
// break-even. Forgetting it is what makes paper results beat live ones.
func TestFeeAlwaysReducesRealizedProfit(t *testing.T) {
	policy := sellPolicy()
	policy.StartingInputUnits = 1_000_000_000
	ledger, err := NewLedger(policy, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	// Sell at exactly the cost basis: no gain, so the result must be the fee.
	after, err := ledger.Apply(Fill{
		Filled: true, Sell: true, SpentUnits: 100_000_000, ReceivedUnits: 2_000_000, FeeLamports: 5_000,
	}, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if after.RealizedMicros >= 0 {
		t.Fatalf("a break-even trade realized %d micros; the fee was not charged", after.RealizedMicros)
	}
	if after.FeesMicros <= 0 {
		t.Error("no fee was recorded")
	}
}

// A refusal that never reached submission has no fee and only revalues the
// book. Post-submit slippage refusals carry a modeled fee separately.
func TestRefusedFillMovesNoInventory(t *testing.T) {
	policy := sellPolicy()
	policy.StartingInputUnits = 1_000_000_000
	ledger, err := NewLedger(policy, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ledger.Apply(Fill{Refusal: "slippage"}, 21_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if after.BaseUnits != ledger.BaseUnits || after.QuoteUnits != ledger.QuoteUnits ||
		after.RealizedMicros != ledger.RealizedMicros || after.Fills != 0 {
		t.Fatalf("a refused fill changed the books: %+v", after)
	}
}

func TestSubmittedRefusalChargesOnlyTheFee(t *testing.T) {
	policy := sellPolicy()
	policy.StartingInputUnits = 1_000_000_000
	ledger, err := NewLedger(policy, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ledger.Apply(Fill{
		Sell: true, Refusal: "slippage", FeeLamports: policy.FeeLamports,
	}, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if after.BaseUnits != ledger.BaseUnits-policy.FeeLamports ||
		after.QuoteUnits != ledger.QuoteUnits || after.TurnoverMicros != 0 ||
		after.Fills != 0 || after.FeesMicros <= 0 || after.RealizedMicros >= 0 {
		t.Fatalf("submitted refusal accounting = %+v", after)
	}
}

func TestSubmittedRefusalKeepsProfitBreakdownReconciledAtChangedMark(t *testing.T) {
	policy := sellPolicy()
	policy.StartingInputUnits = 1_000_000_000 // 1 SOL
	ledger, err := NewLedger(policy, 100_000_000)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ledger.Apply(Fill{
		Sell: true, Refusal: "slippage", FeeLamports: 100_000_000,
	}, 200_000_000)
	if err != nil {
		t.Fatal(err)
	}
	closing, err := after.EquityMicros(200_000_000)
	if err != nil {
		t.Fatal(err)
	}
	unrealized, err := after.UnrealizedMicros(200_000_000)
	if err != nil {
		t.Fatal(err)
	}
	change := int64(closing) - int64(after.OpeningEquityMicros)
	if after.RealizedMicros+unrealized != change {
		t.Fatalf(
			"profit breakdown does not reconcile: realized=%d unrealized=%d change=%d",
			after.RealizedMicros, unrealized, change,
		)
	}
}

func TestBuyChargesFeeBeforeAddingBoughtInventory(t *testing.T) {
	policy := sellPolicy()
	policy.StartingInputUnits = 100_000_000  // 0.1 SOL
	policy.StartingOutputUnits = 200_000_000 // 200 USDC
	ledger, err := NewLedger(policy, 100_000_000)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ledger.Apply(Fill{
		Filled: true, Sell: false,
		SpentUnits: 200_000_000, ReceivedUnits: 1_000_000_000,
		FeeLamports: 100_000_000,
	}, 200_000_000)
	if err != nil {
		t.Fatal(err)
	}
	unrealized, err := after.UnrealizedMicros(200_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if after.BaseUnits != 1_000_000_000 || after.CostBasisMicros != 200_000_000 ||
		after.AverageCostMicros != 200_000_000 || after.RealizedMicros != -10_000_000 ||
		unrealized != 0 {
		t.Fatalf("buy charged fee against bought inventory: ledger=%+v unrealized=%d", after, unrealized)
	}
}

func TestBuyCannotFundItsOwnFee(t *testing.T) {
	policy := sellPolicy()
	policy.Trigger.Direction = "buy_at_or_below"
	policy.InputDecimals, policy.OutputDecimals = 6, 9
	policy.StartingInputUnits = 1_000_000
	policy.StartingOutputUnits = policy.FeeLamports
	ledger, err := NewLedger(policy, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	ledger.BaseUnits = 0 // exercise Apply's defense even if policy creation was bypassed
	if _, err := ledger.Apply(Fill{
		Filled: true, SpentUnits: 1_000_000, ReceivedUnits: 50_000_000,
		FeeLamports: policy.FeeLamports,
	}, 20_000_000); !errors.Is(err, errInsufficientInventory) {
		t.Fatalf("buy funded its fee from its output: %v", err)
	}
	ledger.BaseUnits = policy.FeeLamports
	after, err := ledger.Apply(Fill{
		Filled: true, SpentUnits: 1_000_000, ReceivedUnits: 50_000_000,
		FeeLamports: policy.FeeLamports,
	}, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if after.BaseUnits != 50_000_000 {
		t.Fatalf("buy base = %d, want received amount 50000000", after.BaseUnits)
	}
}

// Selling more than is held is impossible and must be an error rather than an
// inventory that silently goes negative or wraps.
func TestLedgerRefusesToSpendWhatItDoesNotHold(t *testing.T) {
	policy := sellPolicy()
	policy.StartingInputUnits = 3_000_000
	ledger, err := NewLedger(policy, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(Fill{
		Filled: true, Sell: true, SpentUnits: 4_000_000, ReceivedUnits: 80_000, FeeLamports: 5_000,
	}, 20_000_000); err == nil {
		t.Fatal("the ledger sold inventory it did not have")
	}
}

// Drawdown is peak-to-trough and must survive a recovery: the worst moment
// still happened.
func TestMaxDrawdownRemembersTheWorstMoment(t *testing.T) {
	policy := sellPolicy()
	policy.StartingInputUnits = 1_000_000_000
	ledger, err := NewLedger(policy, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	for _, price := range []uint64{25_000_000, 15_000_000, 26_000_000} {
		if ledger, err = ledger.Mark(price); err != nil {
			t.Fatal(err)
		}
	}
	// Peak $25, trough $15, so the worst fall is $10 on one whole SOL.
	if ledger.MaxDrawdownMicros != 10_000_000 {
		t.Fatalf("max drawdown = %d micros, want 10000000 ($10)", ledger.MaxDrawdownMicros)
	}
	if ledger.PeakEquityMicros != 26_000_000 {
		t.Errorf("peak equity = %d, want 26000000", ledger.PeakEquityMicros)
	}
}

// The benchmark is what holding would have been worth, and must not move when
// the strategy trades — otherwise it is not a benchmark.
func TestHoldBenchmarkIgnoresWhatTheStrategyDid(t *testing.T) {
	policy := sellPolicy()
	policy.StartingInputUnits = 1_000_000_000
	ledger, err := NewLedger(policy, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	before, err := ledger.HoldBenchmarkMicros(30_000_000)
	if err != nil {
		t.Fatal(err)
	}
	traded, err := ledger.Apply(Fill{
		Filled: true, Sell: true, SpentUnits: 500_000_000, ReceivedUnits: 10_000_000, FeeLamports: 5_000,
	}, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	after, err := traded.HoldBenchmarkMicros(30_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if before != after || before != 30_000_000 {
		t.Fatalf("benchmark moved with the strategy: %d then %d, want 30000000 both", before, after)
	}
	// Selling half before a rally must now lag simply holding.
	equity, err := traded.EquityMicros(30_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if equity >= after {
		t.Errorf("selling before a rally beat holding: %d vs %d", equity, after)
	}
}

// Buying re-averages the cost of everything held; unrealized profit is measured
// against that average, not against the last price paid.
func TestBuyingReAveragesCostBasis(t *testing.T) {
	policy := sellPolicy()
	policy.Trigger.Direction = "buy_at_or_below"
	policy.InputDecimals, policy.OutputDecimals = 6, 9
	policy.StartingInputUnits = 100_000_000 // 100 USDC to spend
	policy.StartingOutputUnits = 1_000_000_000
	ledger, err := NewLedger(policy, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if ledger.BaseUnits != 1_000_000_000 || ledger.QuoteUnits != 100_000_000 {
		t.Fatalf("a buy policy mapped its inventory the wrong way round: %+v", ledger)
	}
	// The fee first removes 0.000005 of the original SOL and its $20 basis,
	// then one whole SOL is bought at $10.
	after, err := ledger.Apply(Fill{
		Filled: true, Sell: false, SpentUnits: 10_000_000, ReceivedUnits: 1_000_000_000, FeeLamports: 5_000,
	}, 10_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if after.AverageCostMicros != 14_999_987 {
		t.Fatalf("average cost = %d micros, want 14999987", after.AverageCostMicros)
	}
	unrealized, err := after.UnrealizedMicros(after.AverageCostMicros)
	if err != nil {
		t.Fatal(err)
	}
	if unrealized > 0 {
		t.Errorf("holding at the average cost showed a profit of %d micros", unrealized)
	}
}

// A policy that would not be allowed to run must not be allowed to open books
// either, or an invalid run produces numbers that look official.
func TestLedgerRefusesAnInvalidPolicy(t *testing.T) {
	if _, err := NewLedger(Policy{}, 20_000_000); err == nil {
		t.Error("books were opened on an empty policy")
	}
	if _, err := NewLedger(sellPolicy(), 0); err == nil {
		t.Error("books were opened with no opening price")
	}
}

func TestBuyPolicyRequiresAPreexistingFeeReserve(t *testing.T) {
	policy := sellPolicy()
	policy.Trigger.Direction = "buy_at_or_below"
	policy.InputDecimals, policy.OutputDecimals = 6, 9
	policy.StartingOutputUnits = policy.FeeLamports - 1
	if err := policy.Validate(); err == nil {
		t.Fatal("buy policy opened without enough SOL to fund its first fee")
	}
	policy.StartingOutputUnits++
	if err := policy.Validate(); err != nil {
		t.Fatalf("exact fee reserve was refused: %v", err)
	}
}

func TestRoundTripRejectsAnOverflowingFeeReserve(t *testing.T) {
	policy := sellPolicy()
	buy := policy.Trigger
	buy.Direction = "buy_at_or_below"
	policy.ReturnTrigger = &buy
	policy.FeeLamports = math.MaxUint64/2 + 1
	if err := policy.Validate(); err == nil {
		t.Fatal("round trip accepted a two-fee reserve that overflows")
	}
}

// An amount too large to represent must be refused, never wrapped. A silent
// wrap here turns an enormous gain into an enormous loss, which is the worst
// thing an accounting bug can do to a report somebody believes.
func TestLedgerRefusesUnrepresentableAmountsInsteadOfWrapping(t *testing.T) {
	policy := sellPolicy()
	policy.InputDecimals, policy.OutputDecimals = 0, 0
	policy.StartingInputUnits = ^uint64(0)
	ledger, err := NewLedger(policy, 1)
	if err != nil {
		// Refusing to open the books at all is an equally correct outcome.
		return
	}
	_, err = ledger.Apply(Fill{
		Filled: true, Sell: true, SpentUnits: 1, ReceivedUnits: ^uint64(0), FeeLamports: 1,
	}, 1)
	if err == nil {
		t.Fatal("an unrepresentable trade was booked instead of refused")
	}
}

// The signed conversion is the guard the accounting relies on.
func TestSignedRefusesAboveTheInt64Range(t *testing.T) {
	if _, err := signed(1 << 62); err != nil {
		t.Errorf("a representable value was refused: %v", err)
	}
	if _, err := signed(^uint64(0)); err == nil {
		t.Error("an unrepresentable value was converted instead of refused")
	}
	got, err := signed(0)
	if err != nil || got != 0 {
		t.Errorf("zero converted to %d, %v", got, err)
	}
}

// Averaging a price with itself must be a no-op. Re-deriving a running average
// from an already-rounded average drifts in one direction only, so it compounds
// — understating cost basis and therefore overstating profit, a little more
// with every trade.
func TestRepeatedBuysAtOnePriceDoNotDriftTheCostBasis(t *testing.T) {
	policy := sellPolicy()
	policy.Trigger.Direction = "buy_at_or_below"
	policy.InputDecimals, policy.OutputDecimals = 6, 9
	policy.StartingInputUnits = 1_000_000_000 // devUSDC to spend
	policy.StartingOutputUnits = 1_000_000_000

	const price = uint64(21_000_000) // $21.00
	ledger, err := NewLedger(policy, price)
	if err != nil {
		t.Fatal(err)
	}
	// Buy 1 devUSDC worth of SOL at exactly $21, twenty times over.
	for round := range 20 {
		ledger, err = ledger.Apply(Fill{
			Filled: true, Sell: false, SpentUnits: 1_000_000,
			ReceivedUnits: 47_619_047, FeeLamports: 5_000,
		}, price)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}
	// Buying at the same price the book already holds must leave the average
	// where it started, to the micro.
	if drift := int64(ledger.AverageCostMicros) - int64(price); drift < -1 || drift > 1 {
		t.Fatalf("average cost drifted %d micros over 20 identical-price buys "+
			"(%d, want %d)", drift, ledger.AverageCostMicros, price)
	}
}

// The basis must be consumed exactly in proportion, so selling everything
// leaves nothing behind to inflate a later average.
func TestSellingEverythingLeavesNoCostBasisBehind(t *testing.T) {
	policy := sellPolicy()
	policy.StartingInputUnits = 1_000_000_000
	ledger, err := NewLedger(policy, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if ledger.CostBasisMicros != 20_000_000 {
		t.Fatalf("opening basis = %d, want 20000000", ledger.CostBasisMicros)
	}
	// Sell all but the fee.
	after, err := ledger.Apply(Fill{
		Filled: true, Sell: true, SpentUnits: 999_995_000, ReceivedUnits: 20_000_000, FeeLamports: 5_000,
	}, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if after.BaseUnits != 0 {
		t.Fatalf("base units = %d, want 0", after.BaseUnits)
	}
	if after.CostBasisMicros != 0 {
		t.Errorf("cost basis = %d after selling everything, want 0", after.CostBasisMicros)
	}
	unrealized, err := after.UnrealizedMicros(30_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if unrealized != 0 {
		t.Errorf("an empty book showed %d micros of unrealized profit", unrealized)
	}
}
