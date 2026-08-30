package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestShadowAlertsAreBoundedExplicitAndContainNoLiveAuthority(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "alerts.json")
	writer, err := paperstatus.OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	policy := validShadowPolicy()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	run := &shadowRun{policy: policy, policySHA256: fingerprint, alerts: writer}
	now := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	if err := run.alertStrategy(now, paperstatus.KindStrategyActive); err != nil {
		t.Fatal(err)
	}
	if err := run.alertTick(shadow.Tick{
		At: now.Add(time.Second), Event: shadow.EventSignal, PriceMicros: 200_000_000,
		DecisionQuote: &shadow.Quote{
			InputAmount: 1_000_000, EstimatedOutput: 200_000, MinimumOutput: 199_000,
		},
	}, true); err != nil {
		t.Fatal(err)
	}
	fill := shadow.Fill{
		Filled: true, Sell: true, SpentUnits: 1_000_000, ReceivedUnits: 200_000,
		FeeLamports: 5_000, ImpactBPS: -4, SlippageBPS: -2, SettlePriceMicros: 200_000_000,
	}
	if err := run.alertTick(shadow.Tick{
		At: now.Add(2 * time.Second), Event: shadow.EventFilled,
		Fill: &fill, EquityMicros: 1_250_000,
	}, true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || len(snapshot.Events) != 3 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	for _, event := range snapshot.Events {
		if !strings.HasPrefix(event.Message, "PAPER SIMULATION") ||
			!strings.Contains(event.Message, "No transaction was signed or submitted") ||
			strings.Contains(event.Message, "explorer.solana.com") ||
			len(event.Message) > paperstatus.MaxMessageBytes {
			t.Fatalf("unsafe paper alert: %q", event.Message)
		}
	}
	if message := snapshot.Events[0].Message; !strings.Contains(message, "PAPER SIMULATION — 🧠 STRATEGY ACTIVE\n") ||
		!strings.Contains(message, "mainnet-beta · SOL/USDC · trigger at or above $200\n") ||
		!strings.Contains(message, "Size 0.001 SOL · Policy ") {
		t.Fatalf("unreadable strategy alert: %q", message)
	}
	if message := snapshot.Events[1].Message; !strings.Contains(message, "PAPER SIMULATION — 🟡 ORDER PLACED\n") ||
		!strings.Contains(message, "SELL SOL · reference $200\n") ||
		!strings.Contains(message, "0.001 SOL → about 0.2 USDC\n") {
		t.Fatalf("unreadable opened-order alert: %q", message)
	}
	if message := snapshot.Events[2].Message; !strings.Contains(message, "PAPER SIMULATION — 🟢 SOLD\n") ||
		!strings.Contains(message, "0.001 SOL → 0.2 USDC · reference $200\n") ||
		!strings.Contains(message, "Daily paper equity $1.25\n") {
		t.Fatalf("unreadable filled-order alert: %q", message)
	}
}

func TestShadowAlertsIgnoreRoutineWaitingTicks(t *testing.T) {
	policy := validShadowPolicy()
	run := &shadowRun{policy: policy}
	if err := run.alertTick(shadow.Tick{
		At: time.Now().UTC(), Event: shadow.EventWaiting,
	}, policy.IsSell()); err != nil {
		t.Fatal(err)
	}
}

func TestShadowThresholdsKeepTheInclusiveBuyRule(t *testing.T) {
	policy := validShadowPolicy()
	buy := policy.Trigger
	buy.Direction = pricetrigger.BuyAtOrBelow
	buy.ThresholdMicros = 100_000_000
	policy.ReturnTrigger = &buy
	if got := shadowThresholds(policy); got != "sell at or above $200 and buy at or below $100" {
		t.Fatalf("thresholds = %q", got)
	}
}

func TestShadowAlertsFormatBuyRefusalAndMissedEvidence(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "alerts.json")
	writer, err := paperstatus.OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	policy := validShadowPolicy()
	buy := policy.Trigger
	buy.Direction = pricetrigger.BuyAtOrBelow
	buy.ThresholdMicros = 100_000_000
	policy.ReturnTrigger = &buy
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	run := shadowRun{policy: policy, policySHA256: fingerprint, alerts: writer}
	now := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	if err := run.alertTick(shadow.Tick{
		At: now, Event: shadow.EventRefused,
		Fill: &shadow.Fill{
			Sell: false, Refusal: "settlement crossed the slippage floor",
			FeeLamports: 5_000, ImpactBPS: 3, SlippageBPS: 4,
		},
	}, false); err != nil {
		t.Fatal(err)
	}
	if err := run.alertTick(shadow.Tick{
		At: now.Add(time.Second), Event: shadow.EventUnobservable, DecisionMissed: true,
		Reason: shadow.ReasonMarketPriceUnavailable,
	}, false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || len(snapshot.Events) != 2 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	refused := snapshot.Events[0].Message
	if !strings.Contains(refused, "PAPER SIMULATION — ⚪ ORDER REFUSED\n") ||
		!strings.Contains(refused, "BUY · settlement crossed the slippage floor\n") {
		t.Fatalf("unreadable buy refusal: %q", refused)
	}
	missed := snapshot.Events[1].Message
	if !strings.Contains(missed, "PAPER SIMULATION — ⚠️ ORDER MISSED\n") ||
		!strings.Contains(missed, "market price unavailable\n") {
		t.Fatalf("unreadable missed order: %q", missed)
	}
}

func TestShadowAlertReconciliationRecoversACommittedDecisionOnce(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "alerts.json")
	writer, err := paperstatus.OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	policy := validShadowPolicy()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	run := shadowRun{policy: policy, policySHA256: fingerprint, alerts: writer}
	now := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	if err := run.alertStrategy(now, paperstatus.KindStrategyActive); err != nil {
		t.Fatal(err)
	}
	ticks := []shadow.Tick{{
		At: now.Add(time.Second), Event: shadow.EventSignal, PriceMicros: 200_000_000,
		DecisionQuote: &shadow.Quote{
			InputAmount: 1_000_000, EstimatedOutput: 200_000, MinimumOutput: 199_000,
		},
	}}
	for range 2 {
		if err := run.reconcileAlertTicks(ticks); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 2 || snapshot.Events[1].Kind != paperstatus.KindOrderOpened {
		t.Fatalf("reconciled snapshot = %+v", snapshot)
	}
}

func TestShadowAlertReconciliationKeepsOneWayDirectionAfterFill(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writer, err := paperstatus.OpenWriter(filepath.Join(directory, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	policy := validShadowPolicy()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	run := shadowRun{policy: policy, policySHA256: fingerprint, alerts: writer}
	now := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	fill := shadow.Fill{Filled: true, Sell: true, SpentUnits: 1_000_000, ReceivedUnits: 200_000}
	ticks := []shadow.Tick{
		{At: now, Event: shadow.EventSignal, DecisionQuote: &shadow.Quote{InputAmount: 1_000_000}},
		{At: now.Add(time.Second), Event: shadow.EventFilled, Fill: &fill},
		{At: now.Add(2 * time.Second), Event: shadow.EventSignal, DecisionQuote: &shadow.Quote{InputAmount: 1_000_000}},
	}
	if err := run.reconcileAlertTicks(ticks); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	last := snapshot.Events[len(snapshot.Events)-1].Message
	if !strings.Contains(last, "SELL SOL · reference") || strings.Contains(last, "BUY SOL · reference") {
		t.Fatalf("one-way restart changed direction: %q", last)
	}
}

func TestPartialAndCompletePeriodReportsHaveDistinctAlerts(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writer, err := paperstatus.OpenWriter(filepath.Join(directory, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	policy := validShadowPolicy()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	run := shadowRun{policy: policy, policySHA256: fingerprint, alerts: writer}
	from := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	partial := shadow.Report{From: from, To: from.Add(time.Hour), Counts: shadow.Counts{Unobservable: 3}}
	complete := shadow.Report{From: from, To: from.Add(24 * time.Hour)}
	if err := run.alertReport(partial); err != nil {
		t.Fatal(err)
	}
	if err := run.alertReport(complete); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 2 ||
		!strings.Contains(snapshot.Events[0].Message, "PERIOD CLOSED EARLY") ||
		!strings.Contains(snapshot.Events[0].Message, "3 unavailable") ||
		!strings.Contains(snapshot.Events[1].Message, "UTC DAY CLOSED") {
		t.Fatalf("period alerts = %+v", snapshot.Events)
	}
}

func TestShadowReportAlertDerivesMissingCachedPolicyID(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writer, err := paperstatus.OpenWriter(filepath.Join(directory, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	policy := validShadowPolicy()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	run := shadowRun{policy: policy, alerts: writer}
	from := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	if err := run.alertReport(shadow.Report{From: from, To: from.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || len(snapshot.Events) != 1 ||
		!strings.Contains(snapshot.Events[0].Message, "Policy "+fingerprint[:12]) {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}
