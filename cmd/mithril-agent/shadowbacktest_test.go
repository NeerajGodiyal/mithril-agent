package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/shadow"
)

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
		100_000_000, defaultJUPFeeReserveLamports, defaultJUPSetupRentLamports, 100, 5_000,
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
