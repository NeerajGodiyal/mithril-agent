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
		if !strings.HasPrefix(event.Message, "PAPER ·") ||
			strings.Contains(event.Message, "explorer.solana.com") ||
			len(event.Message) > paperstatus.MaxMessageBytes {
			t.Fatalf("unsafe paper alert: %q", event.Message)
		}
	}
	if message := snapshot.Events[0].Message; message !=
		"PAPER · 🧠 Strategy on\nFixed · SELL ≥ $200 · SOL/USDC · initial 0.001 SOL" {
		t.Fatalf("unreadable strategy alert: %q", message)
	}
	if message := snapshot.Events[1].Message; message !=
		"PAPER · 🟡 SELL signal\n0.001 SOL · ref $200" {
		t.Fatalf("unreadable opened-order alert: %q", message)
	}
	if message := snapshot.Events[2].Message; message !=
		"PAPER · 🟢 SELL filled\n0.001 SOL → 0.2 USDC · ref $200\nEquity $1.25" {
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

func TestStrategyActivationReturnsAfterACleanSameDayStop(t *testing.T) {
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
	start := time.Date(2026, 8, 30, 1, 0, 0, 0, time.UTC)
	if err := run.alertStrategy(start, paperstatus.KindStrategyActive); err != nil {
		t.Fatal(err)
	}
	if err := run.alertStrategy(start.Add(time.Minute), paperstatus.KindStrategyActive); err != nil {
		t.Fatal(err)
	}
	if err := run.alertUnavailableReport(start, start.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	run.activationSequence = 1
	if err := run.alertStrategy(start.Add(time.Hour+time.Minute), paperstatus.KindStrategyActive); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || len(snapshot.Events) != 3 {
		t.Fatalf("activation snapshot=%+v err=%v", snapshot, err)
	}
	if snapshot.Events[0].Kind != paperstatus.KindStrategyActive ||
		snapshot.Events[1].Kind != paperstatus.KindPeriodClosed ||
		snapshot.Events[2].Kind != paperstatus.KindStrategyActive {
		t.Fatalf("same-day activation lifecycle = %+v", snapshot.Events)
	}
}

func TestShadowThresholdsKeepTheInclusiveBuyRule(t *testing.T) {
	policy := validShadowPolicy()
	buy := policy.Trigger
	buy.Direction = pricetrigger.BuyAtOrBelow
	buy.ThresholdMicros = 100_000_000
	policy.ReturnTrigger = &buy
	if got := shadowThresholds(policy); got != "Fixed · sell ≥ $200 · buy ≤ $100" {
		t.Fatalf("thresholds = %q", got)
	}
}

func TestAdaptiveShadowAlertExplainsTheSelectedActionBriefly(t *testing.T) {
	policy := validShadowPolicy()
	buy := policy.Trigger
	buy.Direction = pricetrigger.BuyAtOrBelow
	buy.ThresholdMicros = 1
	policy.ReturnTrigger = &buy
	adaptive, err := shadow.DefaultAdaptivePolicy(
		policy.SlippageBPS, policy.FeeLamports, policy.InputAmount, policy.TickSeconds,
	)
	if err != nil {
		t.Fatal(err)
	}
	policy.Adaptive = &adaptive
	decision := &shadow.AdaptiveDecision{
		Regime: shadow.RegimeDowntrend, Strategy: shadow.StrategyMomentum,
		Reason: "trend_aligned_sell", SignalBPS: -825,
	}
	if got := shadowThresholds(policy); got != "Adaptive" {
		t.Fatalf("adaptive summary = %q", got)
	}
	if got := adaptiveDecisionLine(decision); got != "\nDowntrend · momentum" {
		t.Fatalf("adaptive decision line = %q", got)
	}
	decision.Regime = shadow.RegimeRisk
	decision.Strategy = shadow.StrategyRiskExit
	if got := adaptiveDecisionLine(decision); got != "\nRisk limit · exit" {
		t.Fatalf("risk-exit decision line = %q", got)
	}
}

func TestAdaptiveShadowAlertReportsTheRiskPauseOnce(t *testing.T) {
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
	run := shadowRun{policy: policy, policySHA256: strings.Repeat("a", 64), alerts: writer}
	now := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	for index, reason := range []string{"drawdown_halt", "risk_halt"} {
		if err := run.alertTick(shadow.Tick{
			At: now.Add(time.Duration(index) * time.Second), Event: shadow.EventWaiting,
			Decision: &shadow.AdaptiveDecision{Reason: reason},
		}, false); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(directory, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || len(snapshot.Events) != 1 {
		t.Fatalf("risk snapshot=%+v err=%v", snapshot, err)
	}
	event := snapshot.Events[0]
	if event.Kind != paperstatus.KindRiskHalted ||
		event.Message != "PAPER · 🔴 Trading paused\nDaily drawdown limit reached" {
		t.Fatalf("risk alert = %+v", event)
	}

	secondWriter, err := paperstatus.OpenWriter(filepath.Join(directory, "risk-exit-alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	riskExitRun := shadowRun{
		policy: policy, policySHA256: strings.Repeat("b", 64), alerts: secondWriter,
	}
	if err := riskExitRun.alertTick(shadow.Tick{
		At: now, Event: shadow.EventWaiting,
		Decision: &shadow.AdaptiveDecision{Reason: "risk_halt"},
	}, false); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(directory, "risk-exit-alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil || len(snapshot.Events) != 1 ||
		snapshot.Events[0].Kind != paperstatus.KindRiskHalted {
		t.Fatalf("risk-exit pause snapshot=%+v err=%v", snapshot, err)
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
	if !strings.Contains(refused, "PAPER · ⚪ BUY refused\n") ||
		!strings.Contains(refused, "settlement crossed the slippage floor") {
		t.Fatalf("unreadable buy refusal: %q", refused)
	}
	missed := snapshot.Events[1].Message
	if !strings.Contains(missed, "PAPER · ⚠️ Order missed\n") ||
		!strings.Contains(missed, "market price unavailable") {
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

func TestShadowAlertReconciliationDoesNotRepeatAPeriodCloseMiss(t *testing.T) {
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
	now := time.Date(2026, 8, 30, 23, 59, 59, 0, time.UTC)
	if err := run.alertStrategy(now, paperstatus.KindStrategyActive); err != nil {
		t.Fatal(err)
	}
	periodClose := shadow.Tick{
		At: now, Event: shadow.EventMissed, PriceMicros: 200_000_000,
		PeriodClose: true,
	}
	if err := run.reconcileAlertTicks([]shadow.Tick{periodClose}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || len(snapshot.Events) != 1 ||
		snapshot.Events[0].Kind != paperstatus.KindStrategyActive {
		t.Fatalf("period close emitted a delayed missed alert: %+v, %v", snapshot, err)
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
	if !strings.Contains(last, "🟡 SELL signal\n") || strings.Contains(last, "🟡 BUY signal\n") {
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
		snapshot.Events[0].Message != "PAPER · ⚠️ Period stopped\n0 fills · 3 data gaps\nTest P&L +0 · vs hold +0 devUSDC\nCoverage 0.00% · incomplete · daily reset" ||
		snapshot.Events[1].Message != "PAPER · ⚠️ Day closed\n0 fills\nTest P&L +0 · vs hold +0 devUSDC\nCoverage 0.00% · incomplete · daily reset" {
		t.Fatalf("period alerts = %+v", snapshot.Events)
	}
}

func TestMainnetDayAlertShowsTrustworthyPnLAndCoverage(t *testing.T) {
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
	run := shadowRun{policy: policy, policySHA256: strings.Repeat("c", 64), alerts: writer}
	from := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	report := shadow.Report{
		Cluster: shadow.Mainnet, EvaluationMode: shadow.EvaluationResetDaily,
		From: from, To: from.Add(24 * time.Hour), Counts: shadow.Counts{Ticks: 100, Fills: 3, Filtered: 2, Missed: 1},
		OpeningEquityMicros: 10_000_000, ClosingEquityMicros: 10_800_000,
		VersusHoldMicros: 200_000, ExpectedTicks: 100, ObservableBPS: 10_000,
		QuotePegMinimumMicros: pricetrigger.USDCBandMinimumMicros,
		QuotePegMaximumMicros: pricetrigger.USDCBandMaximumMicros,
	}
	if err := run.alertReport(report); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || len(snapshot.Events) != 1 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	want := "PAPER · 📊 Day closed\n3 fills · 2 filtered · 1 missed\n" +
		"P&L +$0.8 · vs hold +$0.2\nCoverage 100.00% · daily reset"
	if snapshot.Events[0].Message != want {
		t.Fatalf("day alert = %q, want %q", snapshot.Events[0].Message, want)
	}
}

func TestShadowReportAlertOmitsInternalPolicyID(t *testing.T) {
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
		strings.Contains(snapshot.Events[0].Message, "Policy") {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}
