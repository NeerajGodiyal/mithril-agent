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
		"PAPER · 🔵 SOLD\nSold 0.001 SOL\nReceived 0.2 USDC\nTotal paper value now: $1.25\nPaper cash + current value of paper holdings" {
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

func TestPaperSettingsExposeOnlyBoundedUserFacingLimits(t *testing.T) {
	policy := validShadowPolicy()
	policy.Cluster = shadow.Mainnet
	policy.Market = shadow.MarketJUPUSDC
	policy.Trigger.Direction = pricetrigger.BuyAtOrBelow
	policy.InputAmount = 250_000_000
	policy.MinimumOrderValueMicros = 10_000_000
	policy.MaximumOrderValueMicros = 250_000_000
	policy.InputDecimals = 6
	policy.OutputDecimals = 6
	policy.FeeLamports = 100_000
	policy.StartingFeeReserveLamports = 32_000_000
	policy.OneTimeSetupRentLamports = 3_000_000
	adaptive, err := shadow.DefaultAdaptivePolicy(
		policy.SlippageBPS, policy.FeeLamports, policy.InputAmount, policy.TickSeconds,
	)
	if err != nil {
		t.Fatal(err)
	}
	policy.Adaptive = &adaptive
	summary := &paperstatus.CurrentSummary{}
	addPaperSettings(summary, policy)
	addPaperFeeBudget(summary, policy, shadow.Ledger{FeeReserveLamports: 32_000_000})
	if summary.InitialLotUnits != 250_000_000 || summary.InitialLotDecimals != 6 ||
		summary.InitialLotAsset != "USDC" || summary.FeeReserveLamports != 32_000_000 ||
		summary.MinimumOrderValueMicros != 10_000_000 ||
		summary.MaximumOrderValueMicros != 250_000_000 ||
		summary.FeeLamports != 100_000 || !summary.FeeBudgetTracked ||
		summary.RemainingFeeReserveLamports != 29_000_000 ||
		summary.EstimatedFillsRemaining != 290 ||
		summary.SlippageBPS != policy.SlippageBPS || summary.SettleSeconds != policy.SettleSeconds ||
		summary.FastWindow != adaptive.FastWindow || summary.SlowWindow != adaptive.SlowWindow ||
		summary.MaxDrawdownBPS != adaptive.MaxDrawdownBPS ||
		summary.CooldownSeconds != adaptive.CooldownSeconds {
		t.Fatalf("paper settings = %+v", summary)
	}
}

func TestPaperFeeBudgetDoesNotCountLockedSetupRentTwice(t *testing.T) {
	policy := validShadowPolicy()
	policy.FeeLamports = 100_000
	policy.StartingFeeReserveLamports = 32_000_000
	policy.OneTimeSetupRentLamports = 3_000_000
	remaining, fills, ok := paperFeeBudget(policy, shadow.Ledger{
		FeeReserveLamports: 28_900_000, LockedRentLamports: 3_000_000,
	})
	if !ok || remaining != 28_900_000 || fills != 289 {
		t.Fatalf("fee budget = %d lamports, %d fills, ok=%t", remaining, fills, ok)
	}
	remaining, fills, ok = paperFeeBudget(policy, shadow.Ledger{FeeReserveLamports: 3_000_000})
	if !ok || remaining != 0 || fills != 0 {
		t.Fatalf("pre-setup exhausted budget = %d lamports, %d fills, ok=%t", remaining, fills, ok)
	}
}

func TestPaperDecisionReasonExplainsQuietAndBlockedTicks(t *testing.T) {
	for name, test := range map[string]struct {
		tick   shadow.Tick
		budget bool
		want   string
	}{
		"adaptive wait": {
			tick: shadow.Tick{Event: shadow.EventWaiting, Decision: &shadow.AdaptiveDecision{
				Reason: "signal_below_cost_hurdle",
			}}, want: "signal_below_cost_hurdle",
		},
		"route cost": {tick: shadow.Tick{Event: shadow.EventFiltered}, want: "route_cost_limit"},
		"fill limit": {tick: shadow.Tick{Event: shadow.EventRefused}, want: "fill_limit"},
		"fee budget": {tick: shadow.Tick{Event: shadow.EventMissed}, budget: true, want: "fee_budget_used"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := paperDecisionReason(test.tick, test.budget); got != test.want {
				t.Fatalf("reason = %q, want %q", got, test.want)
			}
		})
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
	buy.ThresholdMicros = policy.Trigger.ThresholdMicros - 1
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
	}, true); got != "PAPER · ⏸ PAUSED\nLast action: sold\nTotal paper value now: $0.9\nPaper cash + current value of paper holdings" {
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

func TestPaperCurrentPutsRunResultBeforeStrategyDetail(t *testing.T) {
	status := "PAPER · 👀 LOOKING TO SELL\nSOL $106.55 · no good price yet"
	status = addPaperPerformance(status, "123 price checks · 1 filled paper order")
	performance := paperPerformanceLine(100_000_000, 101_250_000, 101_000_000, "USD")
	want := "PAPER · 👀 LOOKING TO SELL\nPaper result this run: up $1.25 · $0.25 better than holding\n" +
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
		"Total paper value now: $101\nPaper cash + current value of paper holdings\nPaper result this run: up $1" {
		t.Fatalf("paper account line = %q", got)
	}
	if got := paperBalanceLines(policy, shadow.Ledger{
		BaseUnits: 320_000_000, QuoteUnits: 26_000_000,
	}); got != "Paper cash left: 26 USDC\nTrading position: 0.32 SOL" {
		t.Fatalf("paper balance lines = %q", got)
	}
	if got := paperBalanceLines(policy, shadow.Ledger{
		BaseUnits: 320_000_000, QuoteUnits: 26_000_000,
		FeeReserveLamports: 1_000_000, LockedRentLamports: 2_000_000,
	}); got != "Paper cash left: 26 USDC\nTrading position: 0.32 SOL\n"+
		"SOL set aside for paper fees/setup: 0.003 SOL" {
		t.Fatalf("SOL paper balance lines = %q", got)
	}
	policy.Market = shadow.MarketJUPUSDC
	policy.InputDecimals = 6
	policy.OutputDecimals = 6
	if got := paperBalanceLines(policy, shadow.Ledger{
		BaseUnits: 320_000_000, QuoteUnits: 26_000_000,
		FeeReserveLamports: 1_000_000, LockedRentLamports: 2_000_000,
	}); got != "Paper cash left: 26 USDC\nTrading position: 320 JUP\n"+
		"SOL set aside for paper fees/setup: 0.003 SOL" {
		t.Fatalf("JUP paper balance lines = %q", got)
	}
	positive, negative, flat := int64(2_140_000), int64(-750_000), int64(0)
	for _, test := range []struct {
		result *int64
		want   string
	}{
		{nil, "Trade result: still open\nProfit or loss appears after the matching order"},
		{&positive, "This completed buy + sell: up $2.14"},
		{&negative, "This completed buy + sell: down $0.75"},
		{&flat, "This completed buy + sell: unchanged"},
	} {
		if got := paperRoundTripLine(test.result, "USD"); got != test.want {
			t.Errorf("round-trip result line = %q, want %q", got, test.want)
		}
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
		snapshot.Events[1].Kind != paperstatus.KindExperimentDone ||
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
		event.Message != "PAPER · ⏸ NEW BUYS PAUSED\nThis run's paper safety limit was reached\nSells can still reduce risk" {
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
		snapshot.Events[0].Message != "PAPER · 🛡 SAFETY SELL ACTIVE\nThis run's paper safety limit was reached\nSelling to reduce risk" {
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

func TestRecoveredFillDoesNotClaimTheFinalLedgerBalance(t *testing.T) {
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
	primary := &shadowSearchReader{identity: policy.Trigger.PrimarySourceSHA256}
	secondary := &shadowSearchReader{identity: policy.Trigger.SecondarySourceSHA256}
	quotePrimary := &shadowSearchReader{identity: policy.QuotePeg.PrimarySourceSHA256}
	quoteSecondary := &shadowSearchReader{identity: policy.QuotePeg.SecondarySourceSHA256}
	roll, err := newDailyJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer roll.Close()
	runner, err := shadow.NewRunner(
		policy, primary, secondary, shadowSearchUnavailableQuoter{}, roll,
		quotePrimary, quoteSecondary,
	)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	run := shadowRun{
		policy: policy, policySHA256: fingerprint, alerts: writer, runner: runner,
		reconcilingAlerts: true,
	}
	fill := shadow.Fill{
		Filled: true, Sell: true, SpentUnits: 1_000_000, ReceivedUnits: 200_000,
	}
	if err := run.alertTick(shadow.Tick{
		At: time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC), Event: shadow.EventFilled,
		EquityMicros: 1_250_000, Fill: &fill,
	}, true); err != nil {
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
	message := snapshot.Events[0].Message
	if strings.Contains(message, "Paper cash left:") || strings.Contains(message, "Trading position:") {
		t.Fatalf("recovered fill claimed a later ledger balance: %q", message)
	}
	if !strings.Contains(message, "Total paper value now: $1.25") {
		t.Fatalf("recovered fill lost its recorded value: %q", message)
	}
}

func TestLegacyRecoveredRoundTripSaysItsResultIsUnavailable(t *testing.T) {
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
	buy := policy.Trigger
	buy.Direction = pricetrigger.BuyAtOrBelow
	buy.ThresholdMicros = policy.Trigger.ThresholdMicros - 1
	policy.ReturnTrigger = &buy
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	run := shadowRun{policy: policy, policySHA256: fingerprint, alerts: writer}
	now := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	ticks := []shadow.Tick{
		{At: now, Event: shadow.EventFilled, EquityMicros: 1_250_000, Fill: &shadow.Fill{
			Filled: true, Sell: true, SpentUnits: 1_000_000, ReceivedUnits: 200_000,
		}},
		{At: now.Add(time.Second), Event: shadow.EventFilled, EquityMicros: 1_260_000, Fill: &shadow.Fill{
			Filled: true, Sell: false, SpentUnits: 200_000, ReceivedUnits: 1_000_000,
		}},
	}
	if err := run.reconcileAlertTicks(ticks); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || len(snapshot.Events) != 2 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if !strings.Contains(snapshot.Events[0].Message, "Trade result: still open") ||
		!strings.Contains(snapshot.Events[1].Message, "result unavailable for this recovered older record") {
		t.Fatalf("legacy recovered round trip = %+v", snapshot.Events)
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
		snapshot.Events[0].Kind != paperstatus.KindExperimentDone ||
		snapshot.Events[1].Kind != paperstatus.KindPeriodClosed ||
		snapshot.Events[0].Message != "PAPER · ⚠️ STOPPED\nPaper gain/loss: unchanged · same as holding · 0 filled paper orders\nNot enough price information · 0.00% available" ||
		snapshot.Events[1].Message != "PAPER · ⚠️ DAY FINISHED\nPaper gain/loss: unchanged · same as holding · 0 filled paper orders\nNot enough price information · 0.00% available" {
		t.Fatalf("period alerts = %+v", snapshot.Events)
	}
}

func TestStoppedPaperAlertShowsTrustworthyPnLAndCoverage(t *testing.T) {
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
		From: from, To: from.Add(6 * time.Hour), Counts: shadow.Counts{Ticks: 100, Signals: 4, Fills: 3, Filtered: 2, Missed: 1},
		OpeningEquityMicros: 10_000_000, ClosingEquityMicros: 10_800_000,
		RealizedMicros: 800_000, HoldBenchmarkMicros: 10_600_000,
		ClosingPriceMicros: 200_000_000,
		VersusHoldMicros:   200_000, ExpectedTicks: 100, ObservableBPS: 10_000,
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
	want := "PAPER · 📊 STOPPED\nPaper gain/loss: up $0.8 · $0.2 better than holding · 3 filled paper orders\n" +
		"Price information available: 100.00%"
	if snapshot.Events[0].Kind != paperstatus.KindExperimentDone || snapshot.Events[0].Message != want {
		t.Fatalf("day alert = %q, want %q", snapshot.Events[0].Message, want)
	}
	if snapshot.Summary == nil || snapshot.Summary.State != "completed" ||
		snapshot.Summary.EquityMicros != report.ClosingEquityMicros ||
		snapshot.Current != want {
		t.Fatalf("terminal paper status = %+v", snapshot)
	}
}

func TestCompleteUTCReportDoesNotSeedTheNextDayHistory(t *testing.T) {
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
	prior := &paperstatus.CurrentSummary{
		Market: shadowMarketPair(policy), ValueUnit: paperValueUnit(policy),
		Day: from.Format("2006-01-02"), TickSeconds: policy.TickSeconds,
		OpeningEquityMicros: 10_000_000, EquityMicros: 10_700_000,
		HoldBenchmarkMicros: 10_500_000, AccountingTracked: true,
		RealizedMicros: 700_000, Checks: 99, Signals: 4, Trades: 3,
		PriceMicros: 199_000_000, State: "watching", Strategy: "fixed",
	}
	if err := writer.UpdateCurrentSummary(from.Add(23*time.Hour), "PAPER · Watching market", prior); err != nil {
		t.Fatal(err)
	}
	report := shadow.Report{
		From: from, To: from.Add(24 * time.Hour),
		Counts:              shadow.Counts{Ticks: 100, Signals: 4, Fills: 3},
		OpeningEquityMicros: 10_000_000, ClosingEquityMicros: 10_800_000,
		RealizedMicros: 800_000, HoldBenchmarkMicros: 10_600_000,
		ClosingPriceMicros: 200_000_000,
	}
	if err := run.alertReport(report); err != nil {
		t.Fatal(err)
	}
	var closed paperstatus.Snapshot
	raw, err := os.ReadFile(filepath.Join(directory, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &closed); err != nil || closed.Summary != nil || len(closed.History) != 1 {
		t.Fatalf("full-day terminal status = %+v, %v", closed, err)
	}
	nextAt := report.To.Add(time.Minute)
	next := &paperstatus.CurrentSummary{
		Market: shadowMarketPair(policy), ValueUnit: paperValueUnit(policy),
		Day: report.To.Format("2006-01-02"), TickSeconds: policy.TickSeconds,
		OpeningEquityMicros: 20_000_000, EquityMicros: 20_000_000,
		HoldBenchmarkMicros: 20_000_000, AccountingTracked: true,
		Checks: 1, PriceMicros: 200_000_000, State: "warming", Strategy: "fixed",
	}
	if err := writer.UpdateCurrentSummary(nextAt, "PAPER · Learning recent prices", next); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(directory, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var restarted paperstatus.Snapshot
	if err := json.Unmarshal(raw, &restarted); err != nil || len(restarted.History) != 1 ||
		!restarted.History[0].At.Equal(nextAt) || restarted.History[0].EquityMicros != next.EquityMicros {
		t.Fatalf("next-day history = %+v, %v", restarted.History, err)
	}
}

func TestProvisionalUTCReportEndsAsACompletedExperiment(t *testing.T) {
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
	policy.Version = shadow.AdmittedVersion
	policy.Market = shadow.MarketWIFUSDC
	policy.MarketEvidenceSHA256 = strings.Repeat("d", 64)
	policy.MarketEvidenceClass = shadow.MarketEvidenceDevelopmentProvisional
	run := shadowRun{policy: policy, policySHA256: strings.Repeat("c", 64), alerts: writer}
	from := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	report := shadow.Report{
		From: from, To: from.Add(24 * time.Hour),
		Counts:              shadow.Counts{Ticks: 100, Signals: 4, Fills: 3},
		OpeningEquityMicros: 10_000_000, ClosingEquityMicros: 10_800_000,
		RealizedMicros: 800_000, HoldBenchmarkMicros: 10_600_000,
		ClosingPriceMicros: 220_000,
	}
	if err := run.alertReport(report); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || len(snapshot.Events) != 1 ||
		snapshot.Events[0].Kind != paperstatus.KindExperimentDone || snapshot.Summary == nil ||
		snapshot.Summary.State != "completed" || snapshot.Summary.Day != from.Format("2006-01-02") ||
		len(snapshot.History) != 1 || !snapshot.History[0].At.Equal(report.To.Add(-time.Nanosecond)) {
		t.Fatalf("provisional terminal snapshot = %+v, %v", snapshot, err)
	}
	unavailableFrom := from.Add(48 * time.Hour)
	if err := run.alertUnavailableReport(unavailableFrom, unavailableFrom.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(directory, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot = paperstatus.Snapshot{}
	if err := json.Unmarshal(raw, &snapshot); err != nil ||
		snapshot.Events[len(snapshot.Events)-1].Kind != paperstatus.KindExperimentDone {
		t.Fatalf("unavailable provisional terminal event = %+v, %v", snapshot.Events, err)
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
