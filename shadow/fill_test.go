package shadow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

func sellPolicy() Policy {
	return Policy{
		Version: Version, Cluster: Devnet,
		QuoteRoute: QuoteRoute{
			Provider: QuoteOrca, Pool: "11111111111111111111111111111111",
			InputMint: wrappedSOLMint, OutputMint: mainnetUSDCMint,
		},
		Trigger: pricetrigger.Policy{
			Version: pricetrigger.Version, Feed: pricetrigger.FeedSOLUSD,
			Direction: pricetrigger.SellAtOrAbove, ThresholdMicros: 20_000_000,
			MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90,
			MaxDeviationBPS: 200, MaxConfidenceBPS: 200,
			PrimarySourceSHA256:   strings.Repeat("a", 64),
			SecondarySourceSHA256: strings.Repeat("b", 64),
		},
		Observe:     "watch-only",
		InputAmount: 1_000_000, InputDecimals: 9, OutputDecimals: 6,
		SlippageBPS: 100, FeeLamports: 5_000,
		TickSeconds: 60, SettleSeconds: 30,
		StartingInputUnits: 1_000_000_000,
	}
}

func jupBuyPolicy(t *testing.T) Policy {
	t.Helper()
	policy := sellPolicy()
	policy.Cluster = Mainnet
	policy.Market = MarketJUPUSDC
	policy.Trigger.Version = pricetrigger.MultiFeedVersion
	policy.Trigger.Feed = pricetrigger.FeedJUPUSD
	policy.Trigger.Direction = pricetrigger.BuyAtOrBelow
	policy.Trigger.ThresholdMicros = 2_000_000
	policy.QuoteRoute = MainnetMarketQuoteRoute(MarketJUPUSDC, false)
	policy.InputAmount = 100_000_000
	policy.InputDecimals, policy.OutputDecimals = 6, 6
	policy.StartingInputUnits = policy.InputAmount
	policy.StartingOutputUnits = 0
	policy.OneTimeSetupRentLamports = 3_000_000
	policy.StartingFeeReserveLamports = policy.OneTimeSetupRentLamports + 2*policy.FeeLamports
	policy.QuotePeg = &pricetrigger.BandPolicy{
		Version: pricetrigger.Version, Feed: pricetrigger.FeedUSDCUSD,
		MinimumMicros: pricetrigger.USDCBandMinimumMicros,
		MaximumMicros: pricetrigger.USDCBandMaximumMicros,
		MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90,
		MaxDeviationBPS: 100, MaxConfidenceBPS: 100,
		PrimarySourceSHA256:   strings.Repeat("c", 64),
		SecondarySourceSHA256: strings.Repeat("d", 64),
	}
	policy.NativeFeePrice = &pricetrigger.Policy{
		Version: pricetrigger.MultiFeedVersion, Feed: pricetrigger.FeedSOLUSD,
		Direction: pricetrigger.BuyAtOrBelow, ThresholdMicros: 1,
		MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90,
		MaxDeviationBPS: 200, MaxConfidenceBPS: 200,
		PrimarySourceSHA256:   strings.Repeat("e", 64),
		SecondarySourceSHA256: strings.Repeat("f", 64),
	}
	policy.NativeFeePriceCeilingMicros = 1_000_000_000
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestJUPPolicyBoundsSetupRentAndKeepsV5Readable(t *testing.T) {
	policy := jupBuyPolicy(t)
	policy.StartingFeeReserveLamports = policy.OneTimeSetupRentLamports + 2*policy.FeeLamports - 1
	if err := policy.Validate(); err == nil {
		t.Fatal("JUP policy accepted a reserve that cannot fund setup and two attempts")
	}
	policy = jupBuyPolicy(t)
	policy.OneTimeSetupRentLamports = ^uint64(0)
	if err := policy.Validate(); err == nil {
		t.Fatal("JUP policy accepted overflowing setup rent")
	}
	policy = jupBuyPolicy(t)
	policy.Version = NativeFeeVersion
	policy.OneTimeSetupRentLamports = 0
	policy.StartingFeeReserveLamports = 2 * policy.FeeLamports
	if err := policy.Validate(); err != nil || JournalVersionFor(policy) != NativeFeeJournalVersion {
		t.Fatalf("v5 JUP policy is no longer readable: journal=%d err=%v", JournalVersionFor(policy), err)
	}
}

// The quoted route is evidence, not launch configuration. If it is outside
// the policy fingerprint, a process can restart against a different pool or
// token pair and append incomparable observations to the same journal.
func TestPolicyFingerprintBindsTheQuotedRoute(t *testing.T) {
	policy := sellPolicy()
	want, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	policy.QuoteRoute.InputMint, policy.QuoteRoute.OutputMint =
		policy.QuoteRoute.OutputMint, policy.QuoteRoute.InputMint
	changed, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if changed == want {
		t.Fatal("changing the quoted token pair did not change the policy fingerprint")
	}
}

func TestPolicyFingerprintBindsTheSeparateFeeReserve(t *testing.T) {
	policy := separateFeePolicy(t)
	want, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	policy.StartingFeeReserveLamports++
	changed, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if changed == want {
		t.Fatal("changing the native fee reserve did not change the policy fingerprint")
	}
}

func TestLegacyPolicyEncodingOmitsTheSeparateFeeReserve(t *testing.T) {
	encoded, err := json.Marshal(sellPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "starting_fee_reserve_lamports") {
		t.Fatalf("legacy policy encoding changed: %s", encoded)
	}
}

func TestPolicyRejectsTheLegacySettlementAccountingVersion(t *testing.T) {
	policy := sellPolicy()
	policy.Version = 3
	if err := policy.Validate(); err == nil {
		t.Fatal("a v3 policy could resume under v4 settlement accounting")
	}
}

func TestLegacyV4PolicyAndJournalContractRemainReadable(t *testing.T) {
	policy := sellPolicy()
	policy.Version = LegacyVersion
	if err := policy.Validate(); err != nil {
		t.Fatalf("legacy v4 policy no longer validates: %v", err)
	}
	if got := JournalVersionFor(policy); got != LegacyJournalVersion {
		t.Fatalf("legacy journal version = %d, want %d", got, LegacyJournalVersion)
	}
	policy.Version = Version
	if got := JournalVersionFor(policy); got != JournalVersion {
		t.Fatalf("current journal version = %d, want %d", got, JournalVersion)
	}
	policy.Version = LegacyVersion
	policy.Market = MarketSOLUSDC
	if err := policy.Validate(); err == nil {
		t.Fatal("legacy policy accepted a v5 market field")
	}
}

// The price conversion is the foundation everything else is measured in, so it
// is pinned to a worked example rather than to itself.
func TestPriceMicrosMatchesAWorkedExample(t *testing.T) {
	// 0.001 SOL in, 0.021525 devUSDC out is $21.525 per SOL.
	price, err := PriceMicros(1_000_000, 21_525, 9, 6)
	if err != nil {
		t.Fatal(err)
	}
	if price != 21_525_000 {
		t.Fatalf("price = %d micros, want 21525000 ($21.525)", price)
	}
	// One whole SOL for one whole USDC is $1.
	if price, err = PriceMicros(1_000_000_000, 1_000_000, 9, 6); err != nil || price != 1_000_000 {
		t.Fatalf("unit price = %d, %v; want 1000000", price, err)
	}
}

// A price that cannot be computed must be an error, never a zero that would be
// silently accounted as a free trade.
func TestPriceMicrosRefusesRatherThanReturningZero(t *testing.T) {
	if _, err := PriceMicros(0, 21_525, 9, 6); err == nil {
		t.Error("a zero input produced a price")
	}
	if _, err := PriceMicros(1_000_000, 21_525, 40, 6); err == nil {
		t.Error("out-of-range decimals produced a price")
	}
	// A huge output against a tiny input must overflow loudly, not wrap.
	if _, err := PriceMicros(1, ^uint64(0), 18, 0); err == nil {
		t.Error("an unrepresentable price was returned instead of an error")
	}
}

// Positive must always mean "good for the trader", in both directions, or every
// aggregate built on it flips sign somewhere and nobody notices.
func TestAdvantageSignIsAlwaysFromTheTradersPointOfView(t *testing.T) {
	better, err := AdvantageBPS(100_000, 101_000, true)
	if err != nil || better != 100 {
		t.Fatalf("selling higher = %d bps, %v; want +100", better, err)
	}
	worse, err := AdvantageBPS(100_000, 99_000, true)
	if err != nil || worse != -100 {
		t.Fatalf("selling lower = %d bps, %v; want -100", worse, err)
	}
	// Buying reverses which direction is good.
	if got, _ := AdvantageBPS(100_000, 99_000, false); got != 100 {
		t.Errorf("buying lower = %d bps, want +100", got)
	}
	if got, _ := AdvantageBPS(100_000, 101_000, false); got != -100 {
		t.Errorf("buying higher = %d bps, want -100", got)
	}
	if _, err := AdvantageBPS(0, 100, true); err == nil {
		t.Error("a zero reference produced an advantage instead of an error")
	}
}

// The central honesty property: a shadow fill is scored against a price
// observed after the decision, and it moves the way the market moved.
func TestSettleFillScoresAgainstTheLaterPrice(t *testing.T) {
	policy := sellPolicy()
	quote := Quote{InputAmount: 1_000_000, EstimatedOutput: 21_525, MinimumOutput: 21_310}

	// The market rose 1% between deciding and settling: selling, that is more
	// received, not less.
	fill, err := SettleFill(policy, quote, 21_525_000, 21_740_250)
	if err != nil {
		t.Fatal(err)
	}
	if !fill.Filled {
		t.Fatalf("a favourable move refused the fill: %s", fill.Refusal)
	}
	if fill.ReceivedUnits <= quote.EstimatedOutput {
		t.Errorf("received %d, expected more than the quote's %d", fill.ReceivedUnits, quote.EstimatedOutput)
	}
	if fill.SlippageBPS <= 0 {
		t.Errorf("a favourable move recorded %d bps of slippage", fill.SlippageBPS)
	}
	if fill.SettlePriceMicros == fill.DecisionPriceMicros {
		t.Error("the fill was scored against the price that produced it")
	}
}

// A move past the slippage floor is a refusal, not a fill at the floor. Booking
// it as a fill is how a paper result invents trades that could never happen.
func TestSettleFillRefusesRatherThanFillingAtTheFloor(t *testing.T) {
	policy := sellPolicy()
	quote := Quote{InputAmount: 1_000_000, EstimatedOutput: 21_525, MinimumOutput: 21_310}

	// Down 5%: far past the 100 bps the policy allows.
	fill, err := SettleFill(policy, quote, 21_525_000, 20_448_750)
	if err != nil {
		t.Fatal(err)
	}
	if fill.Filled {
		t.Fatal("a fill was booked below the policy's own minimum output")
	}
	if fill.ReceivedUnits != 0 || fill.SpentUnits != 0 || fill.FeeLamports != policy.FeeLamports {
		t.Errorf("a refused trade still moved value: %+v", fill)
	}
	if fill.Refusal == "" {
		t.Error("a refusal was recorded with no reason")
	}
	if fill.SlippageBPS >= 0 {
		t.Errorf("an adverse move recorded %d bps", fill.SlippageBPS)
	}
}

func TestRequotedFillKeepsTheOriginalDecisionFloor(t *testing.T) {
	policy := sellPolicy()
	decision := Quote{
		InputAmount: 1_000_000, EstimatedOutput: 21_525, MinimumOutput: 21_310,
	}
	// The venue now offers 21,200 and would accept 21,000 under a newly reset
	// floor. The original decision committed to at least 21,310, so paper mode
	// must refuse instead of erasing the adverse delay.
	settlement := Quote{
		InputAmount: 1_000_000, EstimatedOutput: 21_200, MinimumOutput: 21_000,
	}
	fill, err := SettleRequotedFillDirected(
		policy, decision, settlement, 21_525_000, 21_200_000, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if fill.Filled || fill.SpentUnits != 0 || fill.ReceivedUnits != 0 ||
		fill.FeeLamports != policy.FeeLamports {
		t.Fatalf("fresh quote reset the original slippage floor: %+v", fill)
	}
}

// Buying is the mirror image: a rising price buys less of the asset.
func TestSettleFillInvertsForABuy(t *testing.T) {
	policy := sellPolicy()
	policy.Trigger.Direction = pricetrigger.BuyAtOrBelow
	policy.InputDecimals, policy.OutputDecimals = 6, 9
	quote := Quote{InputAmount: 1_000_000, EstimatedOutput: 46_000_000, MinimumOutput: 45_540_000}

	rising, err := SettleFill(policy, quote, 21_525_000, 21_600_000)
	if err != nil {
		t.Fatal(err)
	}
	if !rising.Filled {
		t.Fatalf("a small move refused the buy: %s", rising.Refusal)
	}
	if rising.ReceivedUnits >= quote.EstimatedOutput {
		t.Errorf("buying into a rising price received %d, not less than %d",
			rising.ReceivedUnits, quote.EstimatedOutput)
	}
}

// Every fill carries the fee. A shadow result that forgets it is the classic
// way paper trading beats reality.
func TestSettleFillAlwaysChargesTheFee(t *testing.T) {
	policy := sellPolicy()
	quote := Quote{InputAmount: 1_000_000, EstimatedOutput: 21_525, MinimumOutput: 21_310}
	fill, err := SettleFill(policy, quote, 21_525_000, 21_525_000)
	if err != nil {
		t.Fatal(err)
	}
	if !fill.Filled || fill.FeeLamports != policy.FeeLamports {
		t.Fatalf("fee = %d, want %d", fill.FeeLamports, policy.FeeLamports)
	}
}

// Malformed inputs must produce an error, never a plausible-looking fill.
func TestSettleFillRejectsIncoherentInput(t *testing.T) {
	policy := sellPolicy()
	good := Quote{InputAmount: 1_000_000, EstimatedOutput: 21_525, MinimumOutput: 21_310}
	for name, broken := range map[string]struct {
		quote            Quote
		decision, settle uint64
	}{
		"no decision price": {good, 0, 21_525_000},
		"no settle price":   {good, 21_525_000, 0},
		"empty quote":       {Quote{}, 21_525_000, 21_525_000},
		"minimum above estimate": {
			Quote{InputAmount: 1_000_000, EstimatedOutput: 21_000, MinimumOutput: 21_310},
			21_525_000, 21_525_000,
		},
	} {
		if _, err := SettleFill(policy, broken.quote, broken.decision, broken.settle); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The pool's price has to be expressed in the same units as the oracle it is
// compared against, or the reported impact is not a number at all. Buying
// inverts: the pool's output is the asset, its input is the numeraire.
func TestQuotedPriceIsAlwaysInTheOraclesUnits(t *testing.T) {
	// Selling 1 SOL for 21.739130 devUSDC is $21.739130 per SOL.
	sell := sellPolicy()
	quoted, err := QuotedPriceMicros(sell, Quote{
		InputAmount: 1_000_000_000, EstimatedOutput: 21_739_130, MinimumOutput: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if quoted != 21_739_130 {
		t.Fatalf("sell quoted = %d, want 21739130 ($21.739130/SOL)", quoted)
	}

	// The same trade the other way round: spending 1 devUSDC for 0.046 SOL is
	// also about $21.74 per SOL, not $0.046.
	buy := sellPolicy()
	buy.Trigger.Direction = pricetrigger.BuyAtOrBelow
	buy.InputDecimals, buy.OutputDecimals = 6, 9
	quoted, err = QuotedPriceMicros(buy, Quote{
		InputAmount: 1_000_000, EstimatedOutput: 46_000_000, MinimumOutput: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if quoted != 21_739_130 {
		t.Fatalf("buy quoted = %d, want 21739130 ($21.739130/SOL)", quoted)
	}
}

// A buy priced about 1% worse than the oracle must report about -100 bps of
// impact, not a nonsensical near-+10000.
func TestBuyImpactIsMeasuredAgainstTheOracleNotItsReciprocal(t *testing.T) {
	policy := sellPolicy()
	policy.Trigger.Direction = pricetrigger.BuyAtOrBelow
	policy.InputDecimals, policy.OutputDecimals = 6, 9

	fill, err := SettleFill(policy, Quote{
		InputAmount: 1_000_000, EstimatedOutput: 46_000_000, MinimumOutput: 45_540_000,
	}, 21_525_000, 21_525_000)
	if err != nil {
		t.Fatal(err)
	}
	if fill.ImpactBPS < -200 || fill.ImpactBPS > 0 {
		t.Fatalf("impact = %d bps; a fill ~1%% worse than the oracle must be a small negative",
			fill.ImpactBPS)
	}
	if fill.QuotedPriceMicros < 20_000_000 || fill.QuotedPriceMicros > 23_000_000 {
		t.Errorf("quoted price = %d micros; not in the oracle's units", fill.QuotedPriceMicros)
	}
}

// A round trip keeps one policy and changes direction per leg. Its return leg
// must use the base/quote decimals, not reinterpret the first leg's input and
// output decimals as though the direction had never changed.
func TestDirectedReturnLegUsesTheReturnDirection(t *testing.T) {
	policy := sellPolicy()
	fill, err := SettleFillDirected(policy, Quote{
		InputAmount: 1_000_000, EstimatedOutput: 46_000_000, MinimumOutput: 45_540_000,
	}, 21_525_000, 21_525_000, false)
	if err != nil {
		t.Fatal(err)
	}
	if fill.QuotedPriceMicros != 21_739_130 {
		t.Fatalf("return-leg quoted price = %d, want 21739130", fill.QuotedPriceMicros)
	}
	if fill.ImpactBPS < -200 || fill.ImpactBPS > 0 {
		t.Fatalf("return-leg impact = %d bps, want a small negative cost", fill.ImpactBPS)
	}
}
