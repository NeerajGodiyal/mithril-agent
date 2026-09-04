package main

import (
	"bytes"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

func TestShadowPerpsCurrentProjectsNetAccounting(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	config := shadowPerpsTapeConfig{Symbol: perpspaper.SOL, RiskArm: perpspaper.Balanced, StartingCollateralMicros: 100_000_000, VenueMaxLeverage: 20}
	replay := perpspaper.TapeReplay{
		Results: []perpspaper.TapeResult{
			{Decision: perpspaper.Decision{Direction: perpspaper.Direction(perpspaper.Long), LeverageBPS: 20_000}, Action: "opened", Fill: &perpspaper.Fill{FilledQuantity: 100_000_000, AveragePriceMicros: 100_000_000}},
			{Decision: perpspaper.Decision{Direction: perpspaper.Direction(perpspaper.Long), LeverageBPS: 20_000}, Action: "marked"},
		},
		State: perpspaper.State{
			Initialized: true, StartingCollateralMicros: 100_000_000,
			BalanceMicros: 107_000_000,
			EquityMicros:  112_000_000, RealizedPnLMicros: 10_000_000,
			UnrealizedPnLMicros: 5_000_000, FundingPnLMicros: -1_000_000,
			FeesPaidMicros: 2_000_000, LastMarkPriceMicros: 105_000_000,
			Position: &perpspaper.Position{Side: perpspaper.Long, LeverageBPS: 20_000},
		},
	}
	current, summary, err := shadowPerpsCurrent(config, replay, now)
	if err != nil {
		t.Fatal(err)
	}
	if summary.RealizedMicros != 7_000_000 || summary.UnrealizedMicros != 5_000_000 ||
		summary.FeesMicros != 2_000_000 || summary.EquityMicros != 112_000_000 ||
		summary.Signals != 1 || summary.Trades != 1 || summary.TurnoverMicros != 10_000_000 ||
		summary.Instrument != "perpetual" || summary.RiskProfile != "balanced" ||
		summary.PositionDirection != "long" || summary.LeverageBPS != 20_000 ||
		!summary.FundingTracked || summary.FundingMicros != -1_000_000 ||
		!bytes.Contains([]byte(current), []byte("Result this run: up $12.00")) ||
		!bytes.Contains([]byte(current), []byte("Funding: -$1.00 · Fees: $2.00")) {
		t.Fatalf("current = %q, summary = %+v", current, summary)
	}
	snapshot := paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: now, Events: []paperstatus.Event{},
		Current: current, Summary: &summary,
	}
	if err := paperstatus.ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("projected snapshot is invalid: %v", err)
	}
}

func TestShadowPerpsSelectedPlanAttributionRequiresLaterBoundTape(t *testing.T) {
	digest := strings.Repeat("a", 64)
	tapeDigest := strings.Repeat("b", 64)
	config := shadowPerpsTapeConfig{
		Symbol: perpspaper.SOL, RiskArm: perpspaper.Balanced,
		StartingCollateralMicros: 100_000_000, VenueMaxLeverage: 20,
		DecisionMode: shadowPerpsDecisionSelected, Strategy: perpspaper.StrategyMomentum,
		PlanSHA256: digest,
	}
	replay := perpspaper.TapeReplay{State: perpspaper.State{
		Initialized: true, StartingCollateralMicros: 100_000_000,
		BalanceMicros: 102_000_000, EquityMicros: 99_000_000,
		UnrealizedPnLMicros: -3_000_000,
	}}
	_, summary, err := shadowPerpsCurrent(config, replay, time.Date(2026, 9, 4, 15, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if summary.DecisionSource != "selected_paper_plan" || summary.ProposalSource != "deterministic_search" ||
		summary.Strategy != "momentum" || summary.RunPlanSHA256 != digest {
		t.Fatalf("selected plan attribution = %+v", summary)
	}
	walkForward := &perpspaper.WalkForwardQualification{Tapes: []perpspaper.WalkForwardTapeEvidence{
		{ContentSHA256: strings.Repeat("c", 64)}, {ContentSHA256: tapeDigest},
	}}
	if got := shadowPerpsOutcomeTapeSHA256(config, tapeDigest, walkForward); got != tapeDigest {
		t.Fatalf("bound later outcome tape = %q", got)
	}
	if got := shadowPerpsOutcomeTapeSHA256(config, strings.Repeat("d", 64), walkForward); got != "" {
		t.Fatalf("non-final outcome tape was accepted: %q", got)
	}
	config.DecisionMode = shadowPerpsDecisionLegacy
	if got := shadowPerpsOutcomeTapeSHA256(config, tapeDigest, walkForward); got != "" {
		t.Fatalf("built-in plan received selected-plan outcome: %q", got)
	}
	if summary.RealizedMicros <= 0 || shadowPerpsOutcomeResult(summary.RealizedMicros+summary.UnrealizedMicros) != "loss" ||
		shadowPerpsOutcomeResult(0) != "flat" || shadowPerpsOutcomeResult(-1) != "loss" {
		t.Fatal("total paper-run outcome categories are incorrect")
	}
}

func TestShadowPerpsCurrentCapsPostLiquidationDeficitWithoutStoppingStatus(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	config := shadowPerpsTapeConfig{Symbol: perpspaper.SOL, RiskArm: perpspaper.Experimental, StartingCollateralMicros: 100_000_000, VenueMaxLeverage: 20}
	replay := perpspaper.TapeReplay{
		Results: []perpspaper.TapeResult{{
			Decision: perpspaper.Decision{Direction: perpspaper.Direction(perpspaper.Short), LeverageBPS: 50_000},
			Action:   "liquidated",
		}},
		State: perpspaper.State{
			Initialized: true, StartingCollateralMicros: 100_000_000,
			BalanceMicros: -2_000_000, EquityMicros: -2_000_000,
			RealizedPnLMicros: -100_000_000, FeesPaidMicros: 2_000_000,
			LastMarkPriceMicros: 120_000_000,
		},
	}
	current, summary, err := shadowPerpsCurrent(config, replay, now)
	if err != nil {
		t.Fatal(err)
	}
	if summary.EquityMicros != 0 || summary.DeficitMicros != 2_000_000 || summary.RealizedMicros != -102_000_000 ||
		!summary.RiskHalted || summary.State != "paused" || summary.Signals != 0 ||
		!strings.Contains(current, "Total paper value now: $0.00") ||
		!strings.Contains(current, "Result this run: down $102.00") ||
		!strings.Contains(current, "Simulated deficit after liquidation: -$2.00") {
		t.Fatalf("current = %q, summary = %+v", current, summary)
	}
	if err := paperstatus.ValidateSnapshot(paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: now, Events: []paperstatus.Event{},
		Current: current, Summary: &summary,
	}); err != nil {
		t.Fatalf("deficit projection is invalid: %v", err)
	}
}

func TestFormatPerpsResultHandlesMinimumInt64(t *testing.T) {
	if got := formatPerpsResult(math.MinInt64); got != "down $9223372036854.77" {
		t.Fatalf("minimum signed perps result = %q", got)
	}
}

func TestShadowPerpsCycleEmitsOnlyIdempotentPositionTransitions(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	config := shadowPerpsTapeConfig{Symbol: perpspaper.SOL, RiskArm: perpspaper.Balanced, StartingCollateralMicros: 100_000_000, VenueMaxLeverage: 20}
	for _, test := range []struct {
		action string
		kind   string
	}{
		{action: "opened", kind: paperstatus.KindOrderFilled},
		{action: "closed", kind: paperstatus.KindOrderFilled},
		{action: "liquidated", kind: paperstatus.KindRiskHalted},
	} {
		t.Run(test.action, func(t *testing.T) {
			directory := t.TempDir()
			var fill *perpspaper.Fill
			if test.action == "opened" || test.action == "closed" {
				fill = &perpspaper.Fill{FilledQuantity: 100_000_000, AveragePriceMicros: 100_000_000}
			}
			replay := perpspaper.TapeReplay{
				Results: []perpspaper.TapeResult{{
					Decision: perpspaper.Decision{Direction: perpspaper.Direction(perpspaper.Long), LeverageBPS: 20_000},
					Action:   test.action, Fill: fill,
				}},
				State: perpspaper.State{
					Initialized: true, StartingCollateralMicros: 100_000_000,
					BalanceMicros: 100_000_000, EquityMicros: 100_000_000,
				},
			}
			statusPath := filepath.Join(directory, "sol-status.json")
			paperPath := filepath.Join(directory, "sol-paper-status.json")
			status := buildShadowPerpsStatus(config, replay, 1, true, now)
			if err := writeShadowPerpsCycle(statusPath, paperPath, status, config, replay, now); err != nil {
				t.Fatal(err)
			}
			if err := writeShadowPerpsCycle(statusPath, paperPath, status, config, replay, now); err != nil {
				t.Fatal(err)
			}
			var snapshot paperstatus.Snapshot
			if err := readStrictJSON(paperPath, &snapshot); err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Events) != 1 || snapshot.Events[0].Kind != test.kind ||
				paperstatus.ValidateSnapshot(snapshot) != nil {
				t.Fatalf("snapshot = %+v", snapshot)
			}
			message := snapshot.Events[0].Message
			if (test.action == "opened" || test.action == "closed") &&
				(!strings.Contains(message, "Paper size: $10.00") ||
					!strings.Contains(message, "Filled near: $100.00")) {
				t.Fatalf("paper fill message = %q", message)
			}
			if test.action == "liquidated" && !strings.Contains(message, "maintenance-margin") {
				t.Fatalf("liquidation message = %q", message)
			}
		})
	}

	directory := t.TempDir()
	replay := perpspaper.TapeReplay{
		Results: []perpspaper.TapeResult{{Decision: perpspaper.Decision{Direction: perpspaper.Flat, LeverageBPS: 20_000}, Action: "flat"}},
		State:   perpspaper.State{Initialized: true, StartingCollateralMicros: 100_000_000, BalanceMicros: 100_000_000, EquityMicros: 100_000_000},
	}
	if err := writeShadowPerpsCycle(
		filepath.Join(directory, "sol-status.json"), filepath.Join(directory, "sol-paper-status.json"),
		buildShadowPerpsStatus(config, replay, 1, true, now), config, replay, now,
	); err != nil {
		t.Fatal(err)
	}
	var quiet paperstatus.Snapshot
	if err := readStrictJSON(filepath.Join(directory, "sol-paper-status.json"), &quiet); err != nil || len(quiet.Events) != 0 {
		t.Fatalf("quiet snapshot = %+v, %v", quiet, err)
	}
}

func TestShadowPerpsCycleReportsTheCompletedPositionResult(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	book, err := perpspaper.New(100_000_000)
	if err != nil {
		t.Fatal(err)
	}
	order := perpspaper.Order{
		ID: "paper-position", Symbol: perpspaper.SOL, Side: perpspaper.Long,
		Kind: perpspaper.Market, Quantity: 100_000_000, LeverageBPS: 20_000,
		EntryFeeBPS: 7, ExitFeeBPS: 7, MaintenanceMarginBPS: 750,
	}
	if _, err = book.Append(perpspaper.Command{Type: perpspaper.OrderPlaced, Order: &order}); err == nil {
		_, err = book.Append(perpspaper.Command{Type: perpspaper.OrderFilled, OrderID: order.ID, PriceMicros: 100_000_000})
	}
	if err == nil {
		_, err = book.Append(perpspaper.Command{Type: perpspaper.PositionClosed, PriceMicros: 110_000_000})
	}
	if err != nil {
		t.Fatal(err)
	}
	replay := perpspaper.TapeReplay{
		Results: []perpspaper.TapeResult{{
			Decision: perpspaper.Decision{Direction: perpspaper.Direction(perpspaper.Long), LeverageBPS: 20_000},
			Action:   "closed", Fill: &perpspaper.Fill{FilledQuantity: 100_000_000, AveragePriceMicros: 110_000_000},
		}},
		Records: book.Records(), State: book.State(),
	}
	directory := t.TempDir()
	statusPath, paperPath := filepath.Join(directory, "sol-status.json"), filepath.Join(directory, "sol-paper-status.json")
	config := shadowPerpsTapeConfig{Symbol: perpspaper.SOL, RiskArm: perpspaper.Balanced, StartingCollateralMicros: 100_000_000, VenueMaxLeverage: 20}
	if err := writeShadowPerpsCycle(statusPath, paperPath, buildShadowPerpsStatus(config, replay, 1, true, now), config, replay, now); err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := readStrictJSON(paperPath, &snapshot); err != nil {
		t.Fatal(err)
	}
	want := "This completed trade: up $0.9853"
	if len(snapshot.Events) != 1 || !strings.Contains(snapshot.Events[0].Message, "\n"+want+"\n") {
		t.Fatalf("completed perps message = %+v, want %q", snapshot.Events, want)
	}

	second := perpspaper.Order{
		ID: "second-position", Symbol: perpspaper.SOL, Side: perpspaper.Short,
		Kind: perpspaper.Market, Quantity: 100_000_000, LeverageBPS: 20_000,
		EntryFeeBPS: 7, ExitFeeBPS: 7, MaintenanceMarginBPS: 750,
	}
	if _, err = book.Append(perpspaper.Command{Type: perpspaper.OrderPlaced, Order: &second}); err == nil {
		_, err = book.Append(perpspaper.Command{Type: perpspaper.OrderFilled, OrderID: second.ID, PriceMicros: 110_000_000})
	}
	if err == nil {
		_, err = book.Append(perpspaper.Command{Type: perpspaper.FundingApplied, PriceMicros: 110_000_000, FundingPaymentMicros: 123_456})
	}
	if err == nil {
		_, err = book.Append(perpspaper.Command{Type: perpspaper.PositionClosed, PriceMicros: 90_000_000})
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := shadowPerpsCompletedTradeLine(book.Records()); got != "This completed trade: up $2.109456" {
		t.Fatalf("latest completed position = %q", got)
	}

	corrupt := book.Records()
	corrupt[len(corrupt)-1].SHA256 = strings.Repeat("0", 64)
	if got := shadowPerpsCompletedTradeLine(corrupt); got != "This completed trade: result unavailable" {
		t.Fatalf("corrupt completed position = %q", got)
	}
}

func TestShadowPerpsCompletedTradeResultCoversLiquidationAndRejectsOpenPosition(t *testing.T) {
	book, err := perpspaper.New(102_000_000)
	if err != nil {
		t.Fatal(err)
	}
	order := perpspaper.Order{
		ID: "liquidated-position", Symbol: perpspaper.SOL, Side: perpspaper.Long,
		Kind: perpspaper.Market, Quantity: 20_000_000_000, LeverageBPS: 200_000,
		EntryFeeBPS: 7, ExitFeeBPS: 7, MaintenanceMarginBPS: 400,
	}
	if _, err = book.Append(perpspaper.Command{Type: perpspaper.OrderPlaced, Order: &order}); err == nil {
		_, err = book.Append(perpspaper.Command{Type: perpspaper.OrderFilled, OrderID: order.ID, PriceMicros: 100_000_000})
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := shadowPerpsCompletedTradeLine(book.Records()); got != "This completed trade: result unavailable" {
		t.Fatalf("open position result = %q", got)
	}
	if _, err = book.Append(perpspaper.Command{Type: perpspaper.Marked, PriceMicros: 95_000_000}); err != nil {
		t.Fatal(err)
	}
	if state := book.State(); state.Position != nil || state.LastCloseReason != "liquidation" {
		t.Fatalf("liquidation state = %+v", state)
	}
	if got := shadowPerpsCompletedTradeLine(book.Records()); got != "This completed trade: down $102.73" {
		t.Fatalf("liquidated position result = %q", got)
	}
}
