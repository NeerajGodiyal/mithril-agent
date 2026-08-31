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
		"PAPER · 🧠 PLAN STARTED\nSells at $200\nStarts with 1 SOL" {
		t.Fatalf("unreadable strategy alert: %q", message)
	}
	if message := snapshot.Events[1].Message; message !=
		"PAPER · 🟠 SELL ORDER OPEN\nSelling up to 0.001 SOL\nWaiting to see if it fills\n"+
			"Reference price: $200" {
		t.Fatalf("unreadable open-order alert: %q", message)
	}
	if message := snapshot.Events[2].Message; message !=
		"PAPER · 🔵 SOLD\nSold 0.001 SOL\nReceived 0.2 USDC\nTotal paper account: $1.25" {
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

func TestPaperStartingSizeUsesBuyFirstOpeningInventory(t *testing.T) {
	policy := validShadowPolicy()
	policy.Cluster = shadow.Mainnet
	policy.Market = shadow.MarketJUPUSDC
	policy.Trigger.Direction = pricetrigger.BuyAtOrBelow
	policy.InputDecimals = 6
	policy.OutputDecimals = 6
	policy.StartingInputUnits = 250_000_000
	policy.StartingOutputUnits = 0
	if got := paperStartingSize(policy); got != "250 USDC" {
		t.Fatalf("starting size = %q, want %q", got, "250 USDC")
	}
}

func TestPaperCurrentMakesAQuietAdaptiveLoopVisible(t *testing.T) {
	policy := validShadowPolicy()
	adaptive, err := shadow.DefaultAdaptivePolicy(
		policy.SlippageBPS, policy.FeeLamports, policy.InputAmount, policy.TickSeconds,
	)
	if err != nil {
		t.Fatal(err)
	}
	policy.Adaptive = &adaptive
	tick := shadow.Tick{
		At: time.Now().UTC(), Event: shadow.EventWaiting, PriceMicros: 106_550_000,
		Decision: &shadow.AdaptiveDecision{
			Regime: shadow.RegimeRange, Strategy: shadow.StrategyRangeReversion,
			Reason: "signal_below_cost_hurdle", SignalBPS: -2,
		},
	}
	want := "PAPER · 👀 LOOKING TO SELL\nSOL $106.55 · no good price yet"
	if got := paperCurrentMessage(policy, tick, true); got != want {
		t.Fatalf("current status = %q, want %q", got, want)
	}
	policy.Market = shadow.MarketJUPUSDC
	if got := paperCurrentMessage(policy, tick, true); got !=
		"PAPER · 👀 LOOKING TO SELL\nJUP $106.55 · no good price yet" {
		t.Fatalf("JUP current status = %q", got)
	}
	policy.Market = shadow.MarketSOLUSDC
	if got := paperCurrentMessage(policy, shadow.Tick{
		At: tick.At, Event: shadow.EventUnobservable,
	}, true); got != "PAPER · ⚠️ WAITING FOR PRICES\nNo new orders until prices return" {
		t.Fatalf("unobservable status = %q", got)
	}
	buy := policy.Trigger
	buy.Direction = pricetrigger.BuyAtOrBelow
	policy.ReturnTrigger = &buy
	if got := paperCurrentMessage(policy, shadow.Tick{
		At: tick.At, Event: shadow.EventSignal, PriceMicros: 106_550_000,
		DecisionQuote: &shadow.Quote{InputAmount: 25_000_000},
	}, false); got != "PAPER · 🟡 BUY ORDER OPEN\nBuying with up to 25 USDC\nReference price: $106.55" {
		t.Fatalf("buy pending status = %q", got)
	}
	risk := &shadow.AdaptiveDecision{Regime: shadow.RegimeRisk, Strategy: shadow.StrategyRiskExit}
	if got := paperCurrentMessage(policy, shadow.Tick{
		At: tick.At, Event: shadow.EventFilled, EquityMicros: 900_000,
		Decision: risk, Fill: &shadow.Fill{Filled: true, Sell: true},
	}, true); got != "PAPER · ⏸ PAUSED\nLast action: sold\nTotal paper account: $0.9" {
		t.Fatalf("risk-exit fill status = %q", got)
	}
	if got := paperCurrentMessage(policy, shadow.Tick{
		At: tick.At, Event: shadow.EventRefused, Decision: risk,
	}, true); got != "PAPER · ⏸ PAUSED\nLast order did not fill" {
		t.Fatalf("risk-exit refusal status = %q", got)
	}
	if got := paperCurrentMessage(policy, shadow.Tick{
		At: tick.At, Event: shadow.EventSignal, Decision: risk,
	}, true); got != "PAPER · ⏸ PAUSED\nWaiting to see if the open order fills" {
		t.Fatalf("deferred risk-exit status = %q", got)
	}
	if got := paperCurrentMessage(policy, shadow.Tick{
		At: tick.At, Event: shadow.EventSignal, Decision: risk,
		DecisionQuote: &shadow.Quote{InputAmount: 1},
	}, true); got != "PAPER · 🟠 SELL ORDER OPEN\nSelling to reduce risk\nWaiting to see if it fills" {
		t.Fatalf("pending risk-exit status = %q", got)
	}
}

func TestPaperSummaryStateIsBoundedAndOperatorReadable(t *testing.T) {
	for _, test := range []struct {
		tick shadow.Tick
		want string
	}{
		{shadow.Tick{Event: shadow.EventUnobservable}, "waiting for data"},
		{shadow.Tick{Event: shadow.EventSignal}, "order pending"},
		{shadow.Tick{Event: shadow.EventWaiting}, "watching"},
		{shadow.Tick{Event: shadow.EventWaiting, Decision: &shadow.AdaptiveDecision{
			Regime: shadow.RegimeWarming,
		}}, "warming"},
		{shadow.Tick{Event: shadow.EventWaiting, Decision: &shadow.AdaptiveDecision{
			Regime: shadow.RegimeRange,
		}}, "range"},
		{shadow.Tick{Event: shadow.EventWaiting, Decision: &shadow.AdaptiveDecision{
			Regime: shadow.RegimeRisk,
		}}, "paused"},
	} {
		if got := paperSummaryState(test.tick); got != test.want {
			t.Errorf("paper summary state = %q, want %q for %+v", got, test.want, test.tick)
		}
	}
}

func TestPaperCurrentPutsTodayResultBeforeStrategyDetail(t *testing.T) {
	status := "PAPER · 👀 LOOKING TO SELL\nSOL $106.55 · no good price yet"
	status = addPaperPerformance(status, "123 price checks · 1 filled paper order")
	performance := paperPerformanceLine(100_000_000, 101_250_000, 101_000_000, "USD")
	want := "PAPER · 👀 LOOKING TO SELL\nPaper gain/loss today: up $1.25 · $0.25 better than holding\n" +
		"123 price checks · 1 filled paper order\n" +
		"SOL $106.55 · no good price yet"
	if got := addPaperPerformance(status, performance); got != want {
		t.Fatalf("current performance = %q, want %q", got, want)
	}
}

func TestPaperResultLanguageExplainsGainLossAndComparison(t *testing.T) {
	for _, test := range []struct {
		got  string
		want string
	}{
		{formatPaperChange(1_000_000, 1_250_000, "USD"), "up $0.25"},
		{formatPaperChange(1_000_000, 750_000, "USD"), "down $0.25"},
		{formatPaperChange(1_000_000, 1_000_000, "USD"), "unchanged"},
		{formatPaperComparison(1_000_000, 1_250_000, "USD"), "$0.25 better than holding"},
		{formatPaperComparison(1_000_000, 750_000, "USD"), "$0.25 worse than holding"},
	} {
		if test.got != test.want {
			t.Errorf("paper result language = %q, want %q", test.got, test.want)
		}
	}
}

func TestPaperFillAndAccountLinesSeparateAmountsFromGainLoss(t *testing.T) {
	usdc := shadowAsset{name: "USDC", decimals: 6}
	sol := shadowAsset{name: "SOL", decimals: 9}
	if got := paperFillLine(shadow.Fill{
		SpentUnits: 25_000_000, ReceivedUnits: 250_000_000,
	}, usdc, sol); got != "Bought 0.25 SOL\nPaid 25 USDC" {
		t.Fatalf("buy fill line = %q", got)
	}
	if got := paperFillLine(shadow.Fill{
		Sell: true, SpentUnits: 250_000_000, ReceivedUnits: 25_000_000,
	}, sol, usdc); got != "Sold 0.25 SOL\nReceived 25 USDC" {
		t.Fatalf("sell fill line = %q", got)
	}
	policy := validShadowPolicy()
	policy.Cluster = shadow.Mainnet
	if got := paperFillPriceLines(policy, shadow.Fill{
		Sell: true, SpentUnits: 250_000_000, ReceivedUnits: 24_750_000,
		DecisionQuote: shadow.Quote{InputAmount: 250_000_000, EstimatedOutput: 25_000_000},
	}, sol, usdc); got != "Expected price: $100\nFilled price: $99" {
		t.Fatalf("paper fill prices = %q", got)
	}
	if got := paperAccountLine(policy, 100_000_000, 101_000_000); got !=
		"Total paper account: $101\nGain/loss today: up $1" {
		t.Fatalf("paper account line = %q", got)
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
	if got := shadowSize(policy); got != "budget 1 SOL · stop 3% drawdown" {
		t.Fatalf("adaptive mandate summary = %q", got)
	}
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
			Decision: &shadow.AdaptiveDecision{Regime: shadow.RegimeRisk, Reason: reason},
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
		event.Message != "PAPER · ⏸ NEW BUYS PAUSED\nToday's paper safety limit was reached\nSells can still reduce risk" {
		t.Fatalf("risk alert = %+v", event)
	}

	secondWriter, err := paperstatus.OpenWriter(filepath.Join(directory, "risk-exit-alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	riskExitRun := shadowRun{
		policy: policy, policySHA256: strings.Repeat("b", 64), alerts: secondWriter,
	}
	riskExit := &shadow.AdaptiveDecision{
		Regime: shadow.RegimeRisk, Strategy: shadow.StrategyRiskExit, Reason: "risk_halt",
	}
	for index, event := range []string{
		shadow.EventSignal, shadow.EventRefused, shadow.EventMissed,
	} {
		var quote *shadow.Quote
		if event == shadow.EventSignal {
			quote = &shadow.Quote{InputAmount: policy.StartingInputUnits}
		}
		if err := riskExitRun.alertTick(shadow.Tick{
			At: now.Add(time.Duration(index) * time.Second), Event: event,
			Decision: riskExit, DecisionQuote: quote,
		}, true); err != nil {
			t.Fatal(err)
		}
	}
	raw, err = os.ReadFile(filepath.Join(directory, "risk-exit-alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil || len(snapshot.Events) != 4 ||
		snapshot.Events[0].Kind != paperstatus.KindRiskHalted ||
		snapshot.Events[0].Message != "PAPER · 🛡 SAFETY SELL ACTIVE\nToday's paper safety limit was reached\nSelling to reduce risk" {
		t.Fatalf("risk-exit pause snapshot=%+v err=%v", snapshot, err)
	}
	for index, kind := range []string{
		paperstatus.KindOrderOpened, paperstatus.KindOrderRefused, paperstatus.KindOrderMissed,
	} {
		if snapshot.Events[index+1].Kind != kind {
			t.Fatalf("risk-exit event %d = %+v, want %s", index+1, snapshot.Events[index+1], kind)
		}
	}
}

func TestShadowAlertsExplainAnUnfilledOrderBriefly(t *testing.T) {
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
	if err := run.alertStrategy(now, paperstatus.KindStrategyActive); err != nil {
		t.Fatal(err)
	}
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
	if err := json.Unmarshal(raw, &snapshot); err != nil || len(snapshot.Events) != 3 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if snapshot.Events[1].Kind != paperstatus.KindOrderRefused ||
		snapshot.Events[1].Message != "PAPER · ⚪ BUY NOT FILLED\nPrice moved past the limit" {
		t.Fatalf("unfilled order alert = %+v", snapshot.Events)
	}
	if snapshot.Events[2].Kind != paperstatus.KindOrderMissed ||
		snapshot.Events[2].Message != "PAPER · ⏭ BUY SKIPPED\nTrade could not be completed" {
		t.Fatalf("unobservable order alert = %+v", snapshot.Events)
	}
}

func TestShadowAlertReconciliationEndsAnUnobservableOrderOnce(t *testing.T) {
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
	run := shadowRun{
		policy: validShadowPolicy(), policySHA256: strings.Repeat("a", 64), alerts: writer,
	}
	now := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	ticks := []shadow.Tick{
		{
			At: now, Event: shadow.EventSignal,
			DecisionQuote: &shadow.Quote{InputAmount: 1_000_000},
		},
		{
			At: now.Add(time.Second), Event: shadow.EventUnobservable,
			DecisionMissed: true, Reason: shadow.ReasonMarketPriceUnavailable,
		},
	}
	for range 2 {
		if err := run.reconcileAlertTicks(ticks); err != nil {
			t.Fatal(err)
		}
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
		snapshot.Events[0].Kind != paperstatus.KindOrderOpened ||
		snapshot.Events[1].Kind != paperstatus.KindOrderMissed {
		t.Fatalf("unobservable order lifecycle = %+v", snapshot.Events)
	}
}

func TestShadowAlertsOnlyOnSustainedDataLossAndRecovery(t *testing.T) {
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
	run := shadowRun{
		policy: validShadowPolicy(), policySHA256: strings.Repeat("a", 64), alerts: writer,
	}
	start := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	ticks := []shadow.Tick{
		{At: start, Event: shadow.EventUnobservable},
		{At: start.Add(time.Minute), Event: shadow.EventUnobservable},
		{At: start.Add(2 * time.Minute), Event: shadow.EventUnobservable},
		{At: start.Add(3 * time.Minute), Event: shadow.EventUnobservable},
		{At: start.Add(4 * time.Minute), Event: shadow.EventWaiting, PriceMicros: 100_000_000},
	}
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
	if len(snapshot.Events) != 2 ||
		snapshot.Events[0].Kind != paperstatus.KindDataUnavailable ||
		snapshot.Events[1].Kind != paperstatus.KindDataRestored ||
		!strings.Contains(snapshot.Events[0].Message, "PRICE DATA DELAYED") ||
		!strings.Contains(snapshot.Events[1].Message, "PRICE DATA BACK") {
		t.Fatalf("market-data alert lifecycle = %+v", snapshot.Events)
	}
}

func TestShadowAlertReconciliationRecoversACommittedFillOnce(t *testing.T) {
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
		At: now.Add(time.Second), Event: shadow.EventFilled, EquityMicros: 1_250_000,
		Fill: &shadow.Fill{
			Filled: true, Sell: true, SpentUnits: 1_000_000, ReceivedUnits: 200_000,
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
	if len(snapshot.Events) != 2 || snapshot.Events[1].Kind != paperstatus.KindOrderFilled {
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
	if len(snapshot.Events) != 3 ||
		!strings.Contains(snapshot.Events[0].Message, "🟠 SELL ORDER OPEN\n") ||
		!strings.Contains(snapshot.Events[1].Message, "🔵 SOLD\n") ||
		!strings.Contains(snapshot.Events[2].Message, "🟠 SELL ORDER OPEN\n") {
		t.Fatalf("reconciliation emitted noisy or wrong-direction alerts: %+v", snapshot.Events)
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
		snapshot.Events[0].Message != "PAPER · ⚠️ STOPPED\nPaper gain/loss: unchanged · same as holding · 0 filled paper orders\nPrice data 0.00% · some data missing" ||
		snapshot.Events[1].Message != "PAPER · ⚠️ DAY FINISHED\nPaper gain/loss: unchanged · same as holding · 0 filled paper orders\nPrice data 0.00% · some data missing" {
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
	want := "PAPER · 📊 DAY FINISHED\nPaper gain/loss: up $0.8 · $0.2 better than holding · 3 filled paper orders\n" +
		"Price data 100.00%"
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
