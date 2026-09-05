package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestObservedNativeCostBacktestIsOfflineAndProvenanceBound(t *testing.T) {
	p, err := buildAdaptiveJUPPolicy(25_000_000, 20_000_000, 3_000_000, 100, 100_000, "So11111111111111111111111111111111111111112", 60)
	if err != nil {
		t.Fatal(err)
	}
	p.Adaptive.FastWindow, p.Adaptive.SlowWindow = 2, 4
	dir := privateTestDirectory(t)
	policyPath := filepath.Join(dir, "policy.json")
	writeJSON(t, policyPath, p)
	primary := &shadowSearchReader{identity: p.Trigger.PrimarySourceSHA256}
	secondary := &shadowSearchReader{identity: p.Trigger.SecondarySourceSHA256}
	peg1 := &shadowSearchReader{identity: p.QuotePeg.PrimarySourceSHA256, price: 1_000_000}
	peg2 := &shadowSearchReader{identity: p.QuotePeg.SecondarySourceSHA256, price: 1_000_000}
	native1 := &shadowSearchReader{identity: p.NativeFeePrice.PrimarySourceSHA256, price: 100_000_000}
	native2 := &shadowSearchReader{identity: p.NativeFeePrice.SecondarySourceSHA256, price: 100_000_000}
	log, err := newDailyJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(); err != nil {
			t.Error(err)
		}
	})
	runner, err := shadow.NewRunner(p, primary, secondary, shadowSearchUnavailableQuoter{}, log, peg1, peg2, native1, native2)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		at := start.Add(time.Duration(i) * time.Minute)
		primary.price, secondary.price = 2_000_000+uint64(i)*6_000, 2_000_000+uint64(i)*6_000
		for _, reader := range []*shadowSearchReader{primary, secondary, peg1, peg2, native1, native2} {
			reader.at = at
		}
		if tick, err := runner.Step(t.Context(), at); err != nil || tick.Event != shadow.EventWaiting {
			t.Fatalf("baseline did not wait: %+v,%v", tick, err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(dir, "shadow-2026-09-05.jsonl")
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--policy", policyPath, "--dir", dir, "--day", "2026-09-05", "--spread-bps", "1", "--cost-experiment", shadow.ObservedNativeCostVersion}
	var output bytes.Buffer
	if err := runShadowBacktest(args, &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Version     string                 `json:"experiment"`
		PolicyHash  string                 `json:"policy_sha256"`
		HistoryHash string                 `json:"history_sha256"`
		Fee         string                 `json:"assumed_fee_lamports"`
		Model       bool                   `json:"pool_modelled"`
		Admission   bool                   `json:"admission_evidence"`
		Enabled     bool                   `json:"trading_enabled"`
		Baseline    shadow.RoundTripResult `json:"baseline"`
		Observed    shadow.RoundTripResult `json:"observed_native_cost"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	pin, err := p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != shadow.ObservedNativeCostVersion || result.PolicyHash != pin || len(result.HistoryHash) != 64 || result.Fee != "100000" || !result.Model || result.Admission || result.Enabled || result.Baseline.Counts.Buys != 0 || result.Observed.Counts.Buys == 0 {
		t.Fatalf("invalid experiment: %s", output.String())
	}
	after, err := os.ReadFile(journalPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("experiment changed journal")
	}
	for _, extra := range [][]string{{"--risk-lanes"}, {"--cost-experiment", "unknown"}} {
		if err := runShadowBacktest(append(append([]string(nil), args...), extra...), io.Discard); err == nil {
			t.Fatal("unsupported experiment combination accepted")
		}
	}
}

// roundTripFixture and reportFixture are the two values writeBacktest renders,
// built here so the rendering tests do not depend on a recorded journal.
func roundTripFixture() shadow.RoundTripResult {
	return shadow.RoundTripResult{
		Counts: shadow.RoundTripCounts{
			Ticks: 120, Sells: 2, Buys: 1, Refused: 1,
			SellSignals: 3, BuySignals: 2,
		},
		ClosingPrice: 21_000_000,
	}
}

func reportFixture() shadow.Report {
	return shadow.Report{
		Version: shadow.Version, Cluster: shadow.Devnet,
		From: time.Unix(1_700_000_000, 0).UTC(), To: time.Unix(1_700_086_400, 0).UTC(),
		ClosingPriceMicros:  21_000_000,
		OpeningEquityMicros: 20_000_000, ClosingEquityMicros: 21_000_000,
		RealizedMicros: 500_000, VersusHoldMicros: 250_000,
	}
}

// Recorded quotes exist only where the original rule made a decision, not at
// every price a hypothetical threshold might choose. A round trip over changed
// thresholds therefore has to model the pool, and the result must say so.
func TestBacktestAlwaysDeclaresThatThePoolWasModelled(t *testing.T) {
	var text bytes.Buffer
	writeBacktest(&text, false, "2026-08-06", 250, roundTripFixture(), reportFixture())
	screen := text.String()
	for _, required := range []string{"MODELLED", "250 bps", "swap discover"} {
		if !strings.Contains(screen, required) {
			t.Errorf("the report does not say %q:\n%s", required, screen)
		}
	}

	// And in the machine-readable form too, so an assistant reading this cannot
	// present it as observed either.
	var payload bytes.Buffer
	writeBacktest(&payload, true, "2026-08-06", 250, roundTripFixture(), reportFixture())
	var decoded backtestResult
	if err := json.Unmarshal(payload.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.PoolModelled || decoded.SpreadBPS != 250 {
		t.Errorf("JSON does not declare the model: %+v", decoded)
	}
}

// A loss that renders as a bare number reads as a profit at a glance, which is
// the one misreading this report must never invite.
func TestBacktestShowsTheSignOnEveryResult(t *testing.T) {
	loss := reportFixture()
	loss.RealizedMicros = -1_500_000
	loss.VersusHoldMicros = -4_250_000
	var text bytes.Buffer
	writeBacktest(&text, false, "2026-08-06", 100, roundTripFixture(), loss)
	screen := text.String()
	if !strings.Contains(screen, "$-1.500000") || !strings.Contains(screen, "$-4.250000") {
		t.Errorf("a loss did not render with its sign:\n%s", screen)
	}

	profit := reportFixture()
	profit.RealizedMicros = 2_000_000
	var gain bytes.Buffer
	writeBacktest(&gain, false, "2026-08-06", 100, roundTripFixture(), profit)
	if !strings.Contains(gain.String(), "$+2.000000") {
		t.Errorf("a profit did not render with its sign:\n%s", gain.String())
	}
}

// The modelled pool must be WORSE than the oracle in both directions. A model
// that quotes at the oracle makes every round trip look free, and one that
// quotes better than the oracle invents money.
func TestModelledPoolIsAlwaysWorseThanTheOracle(t *testing.T) {
	const price = uint64(21_000_000) // $21.00
	policy := validShadowPolicy()
	quote := modelledPool(policy, 100, 100) // 1% spread and 1% slippage

	sell, err := quote(price, true, 1_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	// Selling one SOL at $21 with no cost would yield 21_000_000 devUSDC units.
	if sell.EstimatedOutput >= 21_000_000 {
		t.Errorf("the modelled sell is not worse than the oracle: %d", sell.EstimatedOutput)
	}
	buy, err := quote(price, false, 21_000_000)
	if err != nil {
		t.Fatal(err)
	}
	// Spending 21 devUSDC at $21 with no cost would yield one whole SOL.
	if buy.EstimatedOutput >= 1_000_000_000 {
		t.Errorf("the modelled buy is not worse than the oracle: %d", buy.EstimatedOutput)
	}
	// A wider spread must always fill worse than a narrow one.
	wide, err := modelledPool(policy, 2_500, 100)(price, true, 1_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if wide.EstimatedOutput >= sell.EstimatedOutput {
		t.Errorf("a 25%% spread filled no worse than 1%%: %d vs %d",
			wide.EstimatedOutput, sell.EstimatedOutput)
	}
	// A zero price cannot be modelled rather than dividing by it.
	if _, err := quote(0, true, 1_000_000_000); err == nil {
		t.Error("a zero price was modelled instead of refused")
	}
	small, err := quote(price, true, 500_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if small.InputAmount != 500_000_000 || small.EstimatedOutput*2 != sell.EstimatedOutput {
		t.Errorf("model ignored the requested input: full=%+v half=%+v", sell, small)
	}
}

func TestModelledPoolUsesThePolicySlippageFloor(t *testing.T) {
	policy := validShadowPolicy()
	quote := modelledPool(policy, 100, policy.SlippageBPS)
	decision, err := quote(21_000_000, true, policy.InputAmount)
	if err != nil {
		t.Fatal(err)
	}
	inside, err := quote(20_900_000, true, policy.InputAmount)
	if err != nil {
		t.Fatal(err)
	}
	fill, err := shadow.SettleRequotedFillDirected(
		policy, decision, inside, 21_000_000, 20_900_000, true,
	)
	if err != nil || !fill.Filled {
		t.Fatalf("a move inside policy slippage was refused: fill=%+v err=%v", fill, err)
	}
	beyond, err := quote(20_000_000, true, policy.InputAmount)
	if err != nil {
		t.Fatal(err)
	}
	fill, err = shadow.SettleRequotedFillDirected(
		policy, decision, beyond, 21_000_000, 20_000_000, true,
	)
	if err != nil || fill.Filled {
		t.Fatalf("a move beyond policy slippage was not refused: fill=%+v err=%v", fill, err)
	}
}

func TestModelledPoolUsesJUPSixDecimalUnitsInBothDirections(t *testing.T) {
	policy, err := buildAdaptiveJUPPolicy(
		100_000_000, defaultTokenFeeReserveLamports, defaultTokenSetupRentLamports, 100, 5_000,
		"So11111111111111111111111111111111111111112", 60,
	)
	if err != nil {
		t.Fatal(err)
	}
	quote := modelledPool(policy, 0, policy.SlippageBPS)
	for _, sell := range []bool{true, false} {
		got, quoteErr := quote(1_000_000, sell, 100_000_000)
		if quoteErr != nil {
			t.Fatal(quoteErr)
		}
		if got.EstimatedOutput != 100_000_000 {
			t.Fatalf("JUP sell=%v output=%d, want 100000000", sell, got.EstimatedOutput)
		}
	}
}

// Gaps in the record are not prices. Carrying one forward would invent a
// decision the observer never had the evidence to make.
func TestBacktestSkipsUnobservableTicks(t *testing.T) {
	prices := observedPrices([]shadow.Tick{
		{PriceMicros: 21_000_000},
		{PriceMicros: 0, Event: shadow.EventUnobservable},
		{PriceMicros: 22_000_000},
		{PriceMicros: 22_000_000, Event: shadow.EventMissed, PeriodClose: true},
		{PriceMicros: 0},
		{PriceMicros: 20_500_000},
	})
	if len(prices) != 3 {
		t.Fatalf("kept %d prices, want the 3 observable ones: %v", len(prices), prices)
	}
	for _, price := range prices {
		if price == 0 {
			t.Error("an unobservable tick was kept as a price")
		}
	}
}

func TestBacktestRejectsAJournalStrictReplayCannotReproduce(t *testing.T) {
	policy := validShadowPolicy()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	roll, err := newDailyJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := roll.Record(at, shadow.EventOpened, shadow.Opening{
		Version: shadow.JournalVersion, PolicySHA256: fingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	for index, price := range []uint64{210_000_000, 190_000_000} {
		tickAt := at.Add(time.Duration(index) * time.Minute)
		if err := roll.Record(tickAt, shadow.EventWaiting, shadow.Tick{
			At: tickAt, Event: shadow.EventWaiting, PriceMicros: price, EquityMicros: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := roll.Close(); err != nil {
		t.Fatal(err)
	}
	ticks, err := readShadowTicks(filepath.Join(root, "shadow-2026-08-30.jsonl"), policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shadow.Replay(policy, ticks); err == nil {
		t.Fatal("strict replay accepted the malformed journal fixture")
	}
	if err := runShadowBacktest([]string{
		"--policy", writeShadowPolicy(t, policy), "--dir", root,
		"--buy-at-usd", "100", "--json",
	}, &bytes.Buffer{}); err == nil {
		t.Fatal("backtest accepted a journal strict replay rejects")
	}
}

func TestBacktestRejectsAJournalRenamedToAnotherDay(t *testing.T) {
	policy := validShadowPolicy()
	root := privateTestDirectory(t)
	writeShadowSearchDay(t, root, policy, "2026-08-29", []uint64{
		210_000_000, 190_000_000,
	})
	raw, err := os.ReadFile(filepath.Join(root, "shadow-2026-08-29.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "shadow-2026-08-30.jsonl"), raw, 0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := runShadowBacktest([]string{
		"--policy", writeShadowPolicy(t, policy), "--dir", root,
		"--day", "2026-08-30", "--buy-at-usd", "100", "--json",
	}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "different UTC day") {
		t.Fatalf("renamed backtest journal was accepted: %v", err)
	}
}

func TestPaperRiskPoliciesStayValidFundedAndDoNotMutateTheBase(t *testing.T) {
	base, err := buildAdaptiveShadowPolicy(
		shadow.Mainnet, 50_000_000, 100, 100_000,
		"So11111111111111111111111111111111111111112", 60, "", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	base.StartingInputUnits = 246_000_000
	original, err := base.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	lanes, err := paperRiskPolicies(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(lanes) != 3 || lanes[0].name != "Conservative" ||
		lanes[1].name != "Current" || lanes[2].name != "Aggressive" {
		t.Fatalf("unexpected paper lanes: %+v", lanes)
	}
	if lanes[0].policy.InputAmount != base.InputAmount/4 {
		t.Fatalf("conservative amount=%d, want %d", lanes[0].policy.InputAmount, base.InputAmount/4)
	}
	if lanes[2].policy.InputAmount != base.InputAmount*4 {
		t.Fatalf("aggressive amount=%d, want %d", lanes[2].policy.InputAmount, base.InputAmount*4)
	}
	for _, lane := range lanes {
		if err := lane.policy.Validate(); err != nil {
			t.Fatalf("%s lane is invalid: %v", lane.name, err)
		}
	}
	unchanged, err := base.Fingerprint()
	if err != nil || unchanged != original {
		t.Fatalf("paper lanes mutated the base policy: before=%s after=%s err=%v", original, unchanged, err)
	}
}

func TestAggressivePaperRiskPolicyLeavesLegacySOLFeesFunded(t *testing.T) {
	base, err := buildAdaptiveShadowPolicy(
		shadow.Devnet, 250_000_000, 100, 100_000,
		"So11111111111111111111111111111111111111112", 60,
		"11111111111111111111111111111111", "So11111111111111111111111111111111111111112",
		"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
	)
	if err != nil {
		t.Fatal(err)
	}
	lanes, err := paperRiskPolicies(base)
	if err != nil {
		t.Fatal(err)
	}
	aggressive := lanes[2].policy
	want := base.StartingInputUnits - 2*base.FeeLamports
	if aggressive.InputAmount != want {
		t.Fatalf("aggressive amount=%d, want fee-funded maximum %d", aggressive.InputAmount, want)
	}
	if err := aggressive.Validate(); err != nil {
		t.Fatalf("fee-funded aggressive policy is invalid: %v", err)
	}
}

func TestPaperRiskComparisonUsesOneLiquidationBenchmark(t *testing.T) {
	policy, err := buildAdaptiveShadowPolicy(
		shadow.Mainnet, 250_000_000, 100, 100_000,
		"So11111111111111111111111111111111111111112", 60, "", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	prices := []uint64{
		100_000_000, 100_200_000, 100_400_000, 100_600_000, 100_800_000,
		101_000_000, 101_200_000, 101_400_000, 101_600_000, 101_800_000,
		102_000_000, 102_200_000, 102_400_000, 102_600_000, 102_800_000,
		103_000_000, 103_200_000, 103_400_000, 103_600_000, 103_800_000,
		104_000_000, 103_000_000, 102_000_000, 101_000_000, 100_000_000,
		99_000_000, 98_000_000, 97_000_000, 96_000_000, 95_000_000,
	}
	ticks := make([]shadow.Tick, 0, len(prices))
	for index, price := range prices {
		at := start.Add(time.Duration(index) * time.Minute)
		primary, secondary := shadowSearchSamples(policy, price, at)
		primary.ConfidenceMicros = 1_500_000
		secondary.ConfidenceMicros = 1_500_000
		ticks = append(ticks, shadow.Tick{
			At: at, Event: shadow.EventWaiting, PriceMicros: price,
			PrimaryPrice: &primary, SecondaryPrice: &secondary,
		})
	}
	control, err := scoreHoldControl(policy, ticks)
	if err != nil {
		t.Fatal(err)
	}
	lanes, err := paperRiskPolicies(policy)
	if err != nil {
		t.Fatal(err)
	}
	liquidationPathExercised := false
	for _, candidate := range lanes {
		lane, err := scoreRiskLane(candidate, ticks, 100, control)
		if err != nil {
			t.Fatalf("%s: %v", candidate.name, err)
		}
		want, err := checkedDifference(lane.ProfitLossMicros, control.ProfitLossMicros)
		if err != nil {
			t.Fatal(err)
		}
		if lane.VersusHoldingMicros != want {
			t.Fatalf("%s versus hold=%d, want common-benchmark delta %d", candidate.name, lane.VersusHoldingMicros, want)
		}
		pool := modelledPool(candidate.policy, 100, candidate.policy.SlippageBPS)
		liquidation, err := shadow.ReplayRoundTripTicksWithLiquidationMarks(candidate.policy, ticks, pool)
		if err != nil {
			t.Fatal(err)
		}
		directional, err := shadow.ReplayRoundTripTicks(candidate.policy, ticks, pool)
		if err != nil {
			t.Fatal(err)
		}
		if liquidation.LiquidationMaxDrawdownMicros != directional.Ledger.MaxDrawdownMicros {
			liquidationPathExercised = true
			if lane.MaximumDrawdownMicros != liquidation.LiquidationMaxDrawdownMicros {
				t.Fatalf("%s lane drawdown=%d, want common liquidation drawdown %d",
					candidate.name, lane.MaximumDrawdownMicros, liquidation.LiquidationMaxDrawdownMicros)
			}
		}
	}
	if !liquidationPathExercised {
		t.Fatal("risk comparison fixture did not exercise a partial-inventory bid/ask mark difference")
	}
	var payload bytes.Buffer
	if err := writeRiskComparison(&payload, true, "2026-09-01", 100, policy, ticks); err != nil {
		t.Fatal(err)
	}
	var decoded riskComparisonResult
	if err := json.Unmarshal(payload.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.PoolModelled || decoded.SizeImpactModelled || len(decoded.Lanes) != 4 || decoded.Lanes[0].TradingEnabled {
		t.Fatalf("risk comparison safety labels are wrong: %+v", decoded)
	}
}

func TestPaperRiskComparisonBuyStartUsesOneLiquidationOpeningBenchmark(t *testing.T) {
	_, policy, _, _ := shadowPortfolioTestPolicies(t)
	// A buy-start book may already hold some base inventory. That inventory must
	// be valued at the same sell mark as the hold lane from the opening tick.
	policy.StartingOutputUnits = 1_000_000
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	prices := []uint64{
		100_000_000, 100_200_000, 100_400_000, 100_600_000, 100_800_000,
		101_000_000, 101_200_000, 101_400_000, 101_600_000, 101_800_000,
		102_000_000, 102_200_000, 102_400_000, 102_600_000, 102_800_000,
		103_000_000, 103_200_000, 103_400_000, 103_600_000, 103_800_000,
	}
	ticks := make([]shadow.Tick, 0, len(prices))
	for index, price := range prices {
		at := start.Add(time.Duration(index) * time.Minute)
		primary, secondary := shadowSearchSamples(policy, price, at)
		primary.ConfidenceMicros = 1_500_000
		secondary.ConfidenceMicros = 1_500_000
		nativePrimary := pricetrigger.Sample{
			SourceSHA256: policy.NativeFeePrice.PrimarySourceSHA256,
			Feed:         policy.NativeFeePrice.Feed,
			PriceMicros:  200_000_000,
			PublishedAt:  at,
		}
		nativeSecondary := nativePrimary
		nativeSecondary.SourceSHA256 = policy.NativeFeePrice.SecondarySourceSHA256
		ticks = append(ticks, shadow.Tick{
			At: at, Event: shadow.EventWaiting, PriceMicros: price,
			PrimaryPrice: &primary, SecondaryPrice: &secondary,
			NativeFeePriceMicros: 200_000_000,
			NativeFeePrimary:     &nativePrimary, NativeFeeSecondary: &nativeSecondary,
		})
	}
	control, err := scoreHoldControl(policy, ticks)
	if err != nil {
		t.Fatal(err)
	}
	lanes, err := paperRiskPolicies(policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range lanes {
		lane, err := scoreRiskLane(candidate, ticks, 100, control)
		if err != nil {
			t.Fatalf("%s: %v", candidate.name, err)
		}
		if lane.OpeningEquityMicros != control.OpeningEquityMicros {
			t.Fatalf("%s opening equity=%d, want liquidation benchmark %d",
				candidate.name, lane.OpeningEquityMicros, control.OpeningEquityMicros)
		}
	}
}

func TestPaperRiskPolicyArithmeticCapsInsteadOfWrapping(t *testing.T) {
	base, err := buildAdaptiveShadowPolicy(
		shadow.Mainnet, 250_000_000, 100, 100_000,
		"So11111111111111111111111111111111111111112", 5, "", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	adaptive := *base.Adaptive
	adaptive.FastWindow = 1_439
	adaptive.SlowWindow = 1_440
	adaptive.MinimumSignalBPS = 1_500
	adaptive.MaxVolatilityBPS = 5_000
	adaptive.CooldownSeconds = 86_400
	base.Adaptive = &adaptive
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	lanes, err := paperRiskPolicies(base)
	if err != nil {
		t.Fatal(err)
	}
	conservative := lanes[0].policy.Adaptive
	if conservative.FastWindow != 1_439 || conservative.SlowWindow != 1_440 ||
		conservative.MinimumSignalBPS != 2_000 || conservative.CooldownSeconds != 86_400 {
		t.Fatalf("conservative caps wrapped or drifted: %+v", conservative)
	}
}

func TestConservativePaperRiskWindowsFitTheUTCPeriod(t *testing.T) {
	base, err := buildAdaptiveShadowPolicy(
		shadow.Mainnet, 250_000_000, 100, 100_000,
		"So11111111111111111111111111111111111111112", 60, "", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	adaptive := *base.Adaptive
	adaptive.FastWindow = 500
	adaptive.SlowWindow = 1_000
	base.Adaptive = &adaptive
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	lanes, err := paperRiskPolicies(base)
	if err != nil {
		t.Fatal(err)
	}
	if got := lanes[0].policy.Adaptive.SlowWindow; got != 1_439 {
		t.Fatalf("conservative slow window=%d, want final full-day-valid window 1439", got)
	}
	if err := lanes[0].policy.Validate(); err != nil {
		t.Fatalf("conservative policy exceeds its UTC period: %v", err)
	}
}
