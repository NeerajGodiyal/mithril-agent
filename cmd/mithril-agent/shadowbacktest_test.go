package main

import (
	"bytes"
	"encoding/json"
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

// A recorded tick carries the PRICE, not the quote that was available at the
// time, so a round trip scored over recorded prices has to model the pool. That
// is legitimate — and it is only legitimate if the result says so. A backtest
// that presents a modelled number as an observed one is worse than no backtest,
// because somebody will size a real position on it.
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
	quote := modelledPool(100)       // 1% each way

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
	wide, err := modelledPool(2_500)(price, true, 1_000_000_000)
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

// Gaps in the record are not prices. Carrying one forward would invent a
// decision the observer never had the evidence to make.
func TestBacktestSkipsUnobservableTicks(t *testing.T) {
	prices := observedPrices([]shadow.Tick{
		{PriceMicros: 21_000_000},
		{PriceMicros: 0, Event: shadow.EventUnobservable},
		{PriceMicros: 22_000_000},
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
