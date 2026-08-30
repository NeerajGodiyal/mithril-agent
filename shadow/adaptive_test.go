package shadow

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

func adaptiveTestPolicy() Policy {
	policy := sellPolicy()
	policy.SlippageBPS = 40
	policy.FeeLamports = 500
	policy.Trigger.ThresholdMicros = pricetrigger.MaxPriceMicros
	buy := policy.Trigger
	buy.Direction = pricetrigger.BuyAtOrBelow
	buy.ThresholdMicros = 1
	policy.ReturnTrigger = &buy
	policy.StartingInputUnits = policy.InputAmount + 2*policy.FeeLamports
	policy.Adaptive = &AdaptivePolicy{
		Version:    AdaptiveVersion,
		FastWindow: 2, SlowWindow: 4,
		MinimumSignalBPS:         100,
		MaxVolatilityBPS:         1_000,
		MaxQuoteImpactBPS:        500,
		MaxDrawdownBPS:           300,
		MaxObservationGapSeconds: 120,
	}
	return policy
}

func TestAdaptiveStrategyChangesWithTheMarketInsteadOfAnAbsolutePrice(t *testing.T) {
	policy := adaptiveTestPolicy()
	policy.Adaptive.MaxDrawdownBPS = 5_000
	left, err := newAdaptiveStrategy(policy.Adaptive)
	if err != nil {
		t.Fatal(err)
	}
	right, err := newAdaptiveStrategy(policy.Adaptive)
	if err != nil {
		t.Fatal(err)
	}
	leftLedger, err := NewLedger(policy, 100_000_000)
	if err != nil {
		t.Fatal(err)
	}
	rightLedger, err := NewLedger(policy, 200_000_000)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	leftPrices := []uint64{100_000_000, 99_000_000, 98_000_000, 97_000_000}
	rightPrices := []uint64{200_000_000, 198_000_000, 196_000_000, 194_000_000}
	var leftDecision, rightDecision AdaptiveDecision
	var leftTriggered, rightTriggered bool
	for index := range leftPrices {
		leftLedger, err = leftLedger.Mark(leftPrices[index])
		if err != nil {
			t.Fatal(err)
		}
		rightLedger, err = rightLedger.Mark(rightPrices[index])
		if err != nil {
			t.Fatal(err)
		}
		at := now.Add(time.Duration(index) * time.Minute)
		leftDecision, leftTriggered, err = left.decide(at, leftPrices[index], true, leftLedger)
		if err != nil {
			t.Fatal(err)
		}
		rightDecision, rightTriggered, err = right.decide(at, rightPrices[index], true, rightLedger)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !leftTriggered || !rightTriggered || leftDecision.Regime != RegimeDowntrend ||
		leftDecision.Strategy != StrategyMomentum {
		t.Fatalf("scaled downtrends did not produce sell decisions: left=%+v/%v right=%+v/%v",
			leftDecision, leftTriggered, rightDecision, rightTriggered)
	}
	leftDecision.FastAverageMicros, leftDecision.SlowAverageMicros = 0, 0
	rightDecision.FastAverageMicros, rightDecision.SlowAverageMicros = 0, 0
	if !reflect.DeepEqual(leftDecision, rightDecision) {
		t.Fatalf("the same relative market at another price produced a different decision:\nleft=%+v\nright=%+v",
			leftDecision, rightDecision)
	}
}

func TestAdaptiveStrategySelectsMomentumMeanReversionAndRiskPause(t *testing.T) {
	policy := adaptiveTestPolicy()
	policy.Adaptive.MaxDrawdownBPS = 5_000
	strategy, err := newAdaptiveStrategy(policy.Adaptive)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewLedger(policy, 100_000_000)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	for index, price := range []uint64{100_000_000, 99_000_000, 98_000_000, 97_000_000} {
		ledger, err = ledger.Mark(price)
		if err != nil {
			t.Fatal(err)
		}
		decision, triggered, decisionErr := strategy.decide(
			now.Add(time.Duration(index)*time.Minute), price, true, ledger,
		)
		if decisionErr != nil {
			t.Fatal(decisionErr)
		}
		if index == 3 && (!triggered || decision.Strategy != StrategyMomentum) {
			t.Fatalf("downtrend decision = %+v triggered=%v", decision, triggered)
		}
	}
	strategy, err = newAdaptiveStrategy(policy.Adaptive)
	if err != nil {
		t.Fatal(err)
	}
	var rangeDecision AdaptiveDecision
	var rangeTriggered bool
	for index, price := range []uint64{100_000_000, 100_000_000, 100_000_000, 102_500_000} {
		rangeDecision, rangeTriggered, err = strategy.decide(
			now.Add(time.Duration(index)*time.Minute), price, true, ledger,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !rangeTriggered || rangeDecision.Regime != RegimeRange ||
		rangeDecision.Strategy != StrategyRangeReversion {
		t.Fatalf("range high did not select range reversion: %+v triggered=%v",
			rangeDecision, rangeTriggered)
	}

	volatile := *policy.Adaptive
	volatile.MaxVolatilityBPS = 300
	strategy, err = newAdaptiveStrategy(&volatile)
	if err != nil {
		t.Fatal(err)
	}
	var decision AdaptiveDecision
	var triggered bool
	for index, price := range []uint64{100_000_000, 80_000_000, 120_000_000, 90_000_000} {
		decision, triggered, err = strategy.decide(
			now.Add(time.Duration(index)*time.Minute), price, false, ledger,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	if triggered || decision.Regime != RegimeVolatile || decision.Reason != "volatility_limit" {
		t.Fatalf("volatile market was not paused: %+v triggered=%v", decision, triggered)
	}

	risk := *policy.Adaptive
	risk.MaxDrawdownBPS = 300
	strategy, err = newAdaptiveStrategy(&risk)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err = NewLedger(policy, 100_000_000)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err = ledger.Mark(95_000_000)
	if err != nil {
		t.Fatal(err)
	}
	decision, triggered, err = strategy.decide(now, 95_000_000, true, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if !triggered || decision.Strategy != StrategyRiskExit || decision.Reason != "drawdown_limit" {
		t.Fatalf("drawdown did not produce a risk exit: %+v triggered=%v", decision, triggered)
	}
}

func TestAdaptiveRunnerJournalReplaysAndRejectsAChangedDecision(t *testing.T) {
	policy := adaptiveTestPolicy()
	policy.Adaptive.MaxDrawdownBPS = 5_000
	now := time.Unix(1_700_000_000, 0).UTC()
	primary := &stubSource{identity: policy.Trigger.PrimarySourceSHA256, at: now}
	secondary := &stubSource{identity: policy.Trigger.SecondarySourceSHA256, at: now}
	quoter := &stubQuoter{quote: func(sell bool, amount uint64) Quote {
		output := amount / 10
		if !sell {
			output = amount * 10
		}
		return Quote{InputAmount: amount, EstimatedOutput: output, MinimumOutput: output * 996 / 1_000}
	}}
	recorder := &stubRecorder{}
	runner, err := NewRunner(policy, primary, secondary, quoter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	for index, price := range []uint64{100_000_000, 99_000_000, 98_000_000, 97_000_000} {
		at := now.Add(time.Duration(index) * time.Minute)
		primary.price, primary.at = price, at
		secondary.price, secondary.at = price, at
		if _, err := runner.Step(t.Context(), at); err != nil {
			t.Fatal(err)
		}
	}
	settledAt := now.Add(3*time.Minute + policy.Settle())
	primary.at, secondary.at = settledAt, settledAt
	if tick, err := runner.Step(t.Context(), settledAt); err != nil {
		t.Fatal(err)
	} else if tick.Decision == nil || tick.Fill == nil {
		t.Fatalf("adaptive settlement did not retain its decision and fill: %+v", tick)
	}
	if _, err := Replay(policy, recorder.ticks); err != nil {
		t.Fatalf("replay rejected the runner's adaptive journal: %v", err)
	}
	tampered := append([]Tick(nil), recorder.ticks...)
	for index := range tampered {
		if tampered[index].Decision != nil {
			changed := *tampered[index].Decision
			changed.Reason = "invented"
			tampered[index].Decision = &changed
			break
		}
	}
	if _, err := Replay(policy, tampered); err == nil {
		t.Fatal("replay accepted a changed adaptive decision")
	}
}

func TestAdaptivePendingPeriodCloseReplaysWithoutInventedMarketEvidence(t *testing.T) {
	policy := adaptiveTestPolicy()
	policy.Adaptive.MaxDrawdownBPS = 5_000
	now := time.Unix(1_700_000_000, 0).UTC()
	primary := &stubSource{identity: policy.Trigger.PrimarySourceSHA256}
	secondary := &stubSource{identity: policy.Trigger.SecondarySourceSHA256}
	recorder := &stubRecorder{}
	runner, err := NewRunner(
		policy, primary, secondary,
		&stubQuoter{quote: func(sell bool, amount uint64) Quote {
			output := amount / 10
			if !sell {
				output = amount * 10
			}
			return Quote{InputAmount: amount, EstimatedOutput: output, MinimumOutput: output * 996 / 1_000}
		}},
		recorder,
	)
	if err != nil {
		t.Fatal(err)
	}
	prices := []uint64{100_000_000, 99_000_000, 98_000_000, 97_000_000}
	for index, price := range prices {
		at := now.Add(time.Duration(index) * time.Minute)
		primary.price, primary.at = price, at
		secondary.price, secondary.at = price, at
		if _, err := runner.Step(t.Context(), at); err != nil {
			t.Fatal(err)
		}
	}
	if runner.waiting == nil {
		t.Fatal("adaptive test did not leave a decision pending")
	}
	if err := runner.ClosePeriod(now.Add(3*time.Minute+time.Second), prices[len(prices)-1]); err != nil {
		t.Fatal(err)
	}
	replayed, err := Replay(policy, recorder.ticks)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Counts.Ticks != uint64(len(prices)) || replayed.Counts.Missed != 1 || replayed.pending != nil {
		t.Fatalf("adaptive close replay = counts=%+v pending=%+v", replayed.Counts, replayed.pending)
	}
}

func TestAdaptivePolicyIsFingerprintBoundAndRequiresBothDirections(t *testing.T) {
	policy := adaptiveTestPolicy()
	want, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	policy.Adaptive.MinimumSignalBPS++
	changed, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if changed == want {
		t.Fatal("adaptive configuration was not bound into the policy fingerprint")
	}
	policy.ReturnTrigger = nil
	if err := policy.Validate(); err == nil {
		t.Fatal("adaptive policy without a return direction was accepted")
	}
}

func TestAdaptiveStrategyUsesNoFuturePrices(t *testing.T) {
	policy := adaptiveTestPolicy()
	policy.Adaptive.MaxDrawdownBPS = 5_000
	left, err := newAdaptiveStrategy(policy.Adaptive)
	if err != nil {
		t.Fatal(err)
	}
	right, err := newAdaptiveStrategy(policy.Adaptive)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewLedger(policy, 100_000_000)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	prefix := []uint64{100_000_000, 99_000_000, 98_000_000, 97_000_000}
	var leftDecision, rightDecision AdaptiveDecision
	var leftTriggered, rightTriggered bool
	for index, price := range prefix {
		at := now.Add(time.Duration(index) * time.Minute)
		leftDecision, leftTriggered, err = left.decide(at, price, true, ledger)
		if err != nil {
			t.Fatal(err)
		}
		rightDecision, rightTriggered, err = right.decide(at, price, true, ledger)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Only after the compared decision do the paths receive opposite futures.
	if _, _, err := left.decide(now.Add(4*time.Minute), 50_000_000, true, ledger); err != nil {
		t.Fatal(err)
	}
	if _, _, err := right.decide(now.Add(4*time.Minute), 150_000_000, true, ledger); err != nil {
		t.Fatal(err)
	}
	if leftTriggered != rightTriggered || !reflect.DeepEqual(leftDecision, rightDecision) {
		t.Fatalf("the same prefix produced different decisions before divergent futures: %+v/%v %+v/%v",
			leftDecision, leftTriggered, rightDecision, rightTriggered)
	}
}

func TestAdaptiveStrategyReversesOnlyAfterTheMarketAndInventoryReverse(t *testing.T) {
	policy := adaptiveTestPolicy()
	policy.Adaptive.MaxDrawdownBPS = 5_000
	strategy, err := newAdaptiveStrategy(policy.Adaptive)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewLedger(policy, 100_000_000)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	var decision AdaptiveDecision
	var triggered bool
	for index, price := range []uint64{100_000_000, 99_000_000, 98_000_000, 97_000_000} {
		decision, triggered, err = strategy.decide(
			now.Add(time.Duration(index)*time.Minute), price, true, ledger,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !triggered || decision.Reason != "trend_aligned_sell" {
		t.Fatalf("downtrend did not select the sell leg: %+v triggered=%v", decision, triggered)
	}
	strategy.filled(now.Add(3*time.Minute), false)
	for index, price := range []uint64{98_000_000, 99_000_000, 100_000_000, 101_000_000} {
		decision, triggered, err = strategy.decide(
			now.Add(time.Duration(index+4)*time.Minute), price, false, ledger,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !triggered || decision.Reason != "trend_aligned_buy" {
		t.Fatalf("reversing uptrend did not select the buy leg: %+v triggered=%v", decision, triggered)
	}
}

func TestAdaptiveReturnVolatilityDoesNotTreatASmoothTrendAsNoise(t *testing.T) {
	smooth := make([]uint64, 20)
	for index := range smooth {
		smooth[index] = uint64(100+index) * 1_000_000
	}
	if got := returnVolatilityBPS(smooth); got >= 10 {
		t.Fatalf("smooth trend return volatility = %d bps, want less than 10", got)
	}
	noisy := []uint64{100_000_000, 110_000_000, 90_000_000, 110_000_000}
	if got := returnVolatilityBPS(noisy); got <= 500 {
		t.Fatalf("noisy return volatility = %d bps, want more than 500", got)
	}
}

func TestDefaultAdaptivePolicyCoversBothSlippageBoundsAndFixedFees(t *testing.T) {
	policy, err := DefaultAdaptivePolicy(100, 5_000, 1_000_000, 60)
	if err != nil {
		t.Fatal(err)
	}
	// 2*100 bps slippage + 2*50 bps fixed fee + 10 bps safety.
	if policy.MinimumSignalBPS != 310 {
		t.Fatalf("minimum signal = %d bps, want 310", policy.MinimumSignalBPS)
	}
	full := adaptiveTestPolicy()
	full.SlippageBPS = 100
	full.FeeLamports = 5_000
	full.StartingInputUnits = full.InputAmount + 2*full.FeeLamports
	full.Adaptive = &policy
	full.Adaptive.MinimumSignalBPS--
	if err := full.Validate(); err == nil {
		t.Fatal("policy accepted an edge below its configured round-trip cost floor")
	}
}

func TestAdaptiveRunnerRejectsAnExecutableQuoteThatConsumesTheSignalEdge(t *testing.T) {
	policy := adaptiveTestPolicy()
	policy.Adaptive.MaxDrawdownBPS = 5_000
	now := time.Unix(1_700_000_000, 0).UTC()
	primary := &stubSource{identity: policy.Trigger.PrimarySourceSHA256, at: now}
	secondary := &stubSource{identity: policy.Trigger.SecondarySourceSHA256, at: now}
	quoter := &stubQuoter{quote: func(_ bool, amount uint64) Quote {
		return Quote{InputAmount: amount, EstimatedOutput: 95_000, MinimumOutput: 94_620}
	}}
	recorder := &stubRecorder{}
	runner, err := NewRunner(policy, primary, secondary, quoter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	var last Tick
	for index, price := range []uint64{100_000_000, 99_000_000, 98_000_000, 97_000_000} {
		at := now.Add(time.Duration(index) * time.Minute)
		primary.price, primary.at = price, at
		secondary.price, secondary.at = price, at
		last, err = runner.Step(t.Context(), at)
		if err != nil {
			t.Fatal(err)
		}
	}
	if last.Event != EventFiltered || !last.Triggered || last.DecisionQuote == nil {
		t.Fatalf("edge-consuming quote was not recorded as filtered: %+v", last)
	}
	if _, err := Replay(policy, recorder.ticks); err != nil {
		t.Fatalf("replay rejected a valid no-edge journal: %v", err)
	}
	tampered := append([]Tick(nil), recorder.ticks...)
	for index := range tampered {
		if tampered[index].DecisionQuote != nil {
			quote := *tampered[index].DecisionQuote
			quote.EstimatedOutput = 100_000
			quote.MinimumOutput = 99_600
			tampered[index].DecisionQuote = &quote
			break
		}
	}
	if _, err := Replay(policy, tampered); err == nil {
		t.Fatal("replay accepted a missed quote that was changed to have enough edge")
	}
}

func TestAdaptiveRunnerRecordsQuoteMathErrorsAsReplayableMisses(t *testing.T) {
	policy := adaptiveTestPolicy()
	policy.Adaptive.MaxDrawdownBPS = 5_000
	now := time.Unix(1_700_000_000, 0).UTC()
	primary := &stubSource{identity: policy.Trigger.PrimarySourceSHA256, at: now}
	secondary := &stubSource{identity: policy.Trigger.SecondarySourceSHA256, at: now}
	quoter := &stubQuoter{quote: func(_ bool, amount uint64) Quote {
		maximum := ^uint64(0)
		return Quote{InputAmount: amount, EstimatedOutput: maximum, MinimumOutput: maximum}
	}}
	recorder := &stubRecorder{}
	runner, err := NewRunner(policy, primary, secondary, quoter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	var last Tick
	for index, price := range []uint64{100_000_000, 99_000_000, 98_000_000, 97_000_000} {
		at := now.Add(time.Duration(index) * time.Minute)
		primary.price, primary.at = price, at
		secondary.price, secondary.at = price, at
		last, err = runner.Step(t.Context(), at)
		if err != nil {
			t.Fatal(err)
		}
	}
	if last.Event != EventMissed || last.DecisionQuote != nil {
		t.Fatalf("unrepresentable quote was not reduced to a replayable miss: %+v", last)
	}
	if _, err := Replay(policy, recorder.ticks); err != nil {
		t.Fatalf("runner wrote an unreplayable quote-error journal: %v", err)
	}
}

func TestAdaptiveReplayRejectsAFilteredQuoteTheRunnerCouldNotFund(t *testing.T) {
	policy := adaptiveTestPolicy()
	policy.Adaptive.MaxDrawdownBPS = 5_000
	now := time.Unix(1_700_000_000, 0).UTC()
	primary := &stubSource{identity: policy.Trigger.PrimarySourceSHA256, at: now}
	secondary := &stubSource{identity: policy.Trigger.SecondarySourceSHA256, at: now}
	quoteCall := 0
	quoter := &stubQuoter{quote: func(_ bool, amount uint64) Quote {
		quoteCall++
		if quoteCall == 1 {
			return Quote{InputAmount: amount, EstimatedOutput: 100_000, MinimumOutput: 99_600}
		}
		return Quote{InputAmount: amount, EstimatedOutput: 99_000, MinimumOutput: 98_604}
	}}
	recorder := &stubRecorder{}
	runner, err := NewRunner(policy, primary, secondary, quoter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		at    time.Time
		price uint64
	}{
		{now, 100_000_000},
		{now.Add(time.Minute), 99_000_000},
		{now.Add(2 * time.Minute), 98_000_000},
		{now.Add(3 * time.Minute), 97_000_000},
		{now.Add(210 * time.Second), 97_000_000},
		{now.Add(4 * time.Minute), 90_000_000},
	}
	var last Tick
	for _, step := range steps {
		primary.price, primary.at = step.price, step.at
		secondary.price, secondary.at = step.price, step.at
		last, err = runner.Step(t.Context(), step.at)
		if err != nil {
			t.Fatal(err)
		}
	}
	if last.Event != EventMissed || last.DecisionQuote != nil {
		t.Fatalf("unfunded retry was not missed before quoting: %+v", last)
	}
	if quoteCall != 2 {
		t.Fatalf("unfunded retry reached the quoter: calls=%d", quoteCall)
	}
	if _, err := Replay(policy, recorder.ticks); err != nil {
		t.Fatalf("replay rejected the original unfunded journal: %v", err)
	}
	tampered := append([]Tick(nil), recorder.ticks...)
	quote := Quote{InputAmount: policy.InputAmount, EstimatedOutput: 94_000, MinimumOutput: 93_624}
	tampered[len(tampered)-1].Event = EventFiltered
	tampered[len(tampered)-1].DecisionQuote = &quote
	if _, err := Replay(policy, tampered); err == nil {
		t.Fatal("replay accepted a filtered quote the runner could not have funded")
	}
}

func TestAdaptiveRoundTripUsesTheSameExecutableQuoteGuardAsTheRunner(t *testing.T) {
	policy := adaptiveTestPolicy()
	policy.Adaptive.MaxDrawdownBPS = 5_000
	prices := []uint64{100_000_000, 99_000_000, 98_000_000, 97_000_000, 97_000_000}
	adverse, err := ReplayRoundTrip(
		policy, prices,
		func(_ uint64, _ bool, amount uint64) (Quote, error) {
			return Quote{InputAmount: amount, EstimatedOutput: 95_000, MinimumOutput: 94_620}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if adverse.Counts.Sells != 0 || adverse.Counts.Filtered == 0 {
		t.Fatalf("round-trip accepted an edge-consuming quote: %+v", adverse.Counts)
	}
	favorable, err := ReplayRoundTrip(
		policy, prices,
		func(price uint64, _ bool, amount uint64) (Quote, error) {
			output := price / 1_000
			return Quote{InputAmount: amount, EstimatedOutput: output, MinimumOutput: output * 996 / 1_000}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if favorable.Counts.Sells != 1 {
		t.Fatalf("round-trip rejected a quote that preserved the signal edge: %+v", favorable.Counts)
	}
}

func TestAdaptiveCostFloorRejectsOverflowInsteadOfWrapping(t *testing.T) {
	if cost, err := adaptiveCostFloorBPS(1, 922_337_203_685_477_581, 1_000); err == nil {
		t.Fatalf("overflowing fee ratio was accepted as %d bps", cost)
	}
	if cost, err := adaptiveCostFloorBPS(1, 239_807_672_958_224_171, 130); err == nil {
		t.Fatalf("overflowing fee ceiling was accepted as %d bps", cost)
	}
}

func TestAdaptiveQuoteGateRecomputesFeesForTheCurrentPosition(t *testing.T) {
	policy := adaptiveTestPolicy()
	decision := &AdaptiveDecision{Strategy: StrategyMomentum, SignalBPS: -150}
	opening := Quote{InputAmount: 1_000_000, EstimatedOutput: 100_000, MinimumOutput: 99_600}
	if passes, err := adaptiveQuotePasses(policy, decision, opening, 100_000_000, true); err != nil || !passes {
		t.Fatalf("opening-size quote did not pass: passes=%v err=%v", passes, err)
	}
	shrunk := Quote{InputAmount: 10_000, EstimatedOutput: 1_000, MinimumOutput: 996}
	if passes, err := adaptiveQuotePasses(policy, decision, shrunk, 100_000_000, true); err != nil || passes {
		t.Fatalf("shrunk position ignored its larger fee burden: passes=%v err=%v", passes, err)
	}
	buyDecision := &AdaptiveDecision{Strategy: StrategyMomentum, SignalBPS: 150}
	openingBuy := Quote{InputAmount: 100_000, EstimatedOutput: 1_000_000, MinimumOutput: 996_000}
	if passes, err := adaptiveQuotePasses(policy, buyDecision, openingBuy, 100_000_000, false); err != nil || !passes {
		t.Fatalf("opening-size buy quote did not pass: passes=%v err=%v", passes, err)
	}
	shrunkBuy := Quote{InputAmount: 1_000, EstimatedOutput: 10_000, MinimumOutput: 9_960}
	if passes, err := adaptiveQuotePasses(policy, buyDecision, shrunkBuy, 100_000_000, false); err != nil || passes {
		t.Fatalf("shrunk buy ignored its larger fee burden: passes=%v err=%v", passes, err)
	}
}

func TestAdaptiveRiskExitRejectsAnArbitrarilyBadInitialQuote(t *testing.T) {
	policy := adaptiveTestPolicy()
	decision := &AdaptiveDecision{Strategy: StrategyRiskExit}
	hiddenFloor := Quote{InputAmount: 1_000_000, EstimatedOutput: 95_000, MinimumOutput: 1}
	if passes, err := adaptiveQuotePasses(policy, decision, hiddenFloor, 100_000_000, true); err != nil || passes {
		t.Fatalf("risk exit accepted a near-zero hidden floor: passes=%v err=%v", passes, err)
	}
	bad := Quote{InputAmount: 1_000_000, EstimatedOutput: 10_000, MinimumOutput: 9_960}
	if passes, err := adaptiveQuotePasses(policy, decision, bad, 100_000_000, true); err != nil || passes {
		t.Fatalf("risk exit accepted a 90%% adverse quote: passes=%v err=%v", passes, err)
	}
	bounded := Quote{InputAmount: 1_000_000, EstimatedOutput: 96_000, MinimumOutput: 95_616}
	if passes, err := adaptiveQuotePasses(policy, decision, bounded, 100_000_000, true); err != nil || !passes {
		t.Fatalf("risk exit rejected a quote inside its cap: passes=%v err=%v", passes, err)
	}
}

func TestAdaptiveQuoteGateRejectsAnImpossiblyFavorableQuote(t *testing.T) {
	policy := adaptiveTestPolicy()
	decision := &AdaptiveDecision{Strategy: StrategyMomentum, SignalBPS: -100}
	quote := Quote{
		InputAmount: policy.InputAmount, EstimatedOutput: 1_000_000_000,
		MinimumOutput: 996_000_000,
	}
	if passes, err := adaptiveQuotePasses(
		policy, decision, quote, 100_000_000, true,
	); err != nil || passes {
		t.Fatalf("impossibly favorable quote passed: passes=%v err=%v", passes, err)
	}
}

func TestAdaptiveQuoteGateBindsTheMinimumToConfiguredSlippage(t *testing.T) {
	if quoteMatchesSlippage(40, Quote{EstimatedOutput: 100, MinimumOutput: 99}) {
		t.Fatal("integer rounding widened a 40 bps slippage bound to 100 bps")
	}
	if !quoteMatchesSlippage(40, Quote{EstimatedOutput: 100, MinimumOutput: 100}) {
		t.Fatal("exact strict minimum was rejected")
	}
	policy := adaptiveTestPolicy()
	decision := &AdaptiveDecision{Strategy: StrategyMomentum, SignalBPS: -100}
	loose := Quote{InputAmount: 1_000_000, EstimatedOutput: 100_000, MinimumOutput: 95_000}
	if passes, err := adaptiveQuotePasses(policy, decision, loose, 100_000_000, true); err != nil || passes {
		t.Fatalf("quote hid 500 bps behind a 40 bps policy: passes=%v err=%v", passes, err)
	}
	bounded := Quote{InputAmount: 1_000_000, EstimatedOutput: 100_000, MinimumOutput: 99_600}
	if passes, err := adaptiveQuotePasses(policy, decision, bounded, 100_000_000, true); err != nil || !passes {
		t.Fatalf("quote matching the 40 bps policy failed: passes=%v err=%v", passes, err)
	}
}

func TestAdaptiveRiskExitLatchesThroughReplayAndResume(t *testing.T) {
	policy := adaptiveTestPolicy()
	policy.Adaptive.MaxDrawdownBPS = 300
	now := time.Unix(1_700_000_000, 0).UTC()
	primary := &stubSource{identity: policy.Trigger.PrimarySourceSHA256, at: now}
	secondary := &stubSource{identity: policy.Trigger.SecondarySourceSHA256, at: now}
	quoter := &stubQuoter{quote: func(_ bool, amount uint64) Quote {
		return Quote{InputAmount: amount, EstimatedOutput: 95_000, MinimumOutput: 94_620}
	}}
	recorder := &stubRecorder{}
	runner, err := NewRunner(policy, primary, secondary, quoter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		at    time.Time
		price uint64
	}{
		{now, 100_000_000},
		{now.Add(time.Minute), 95_000_000},
		{now.Add(90 * time.Second), 95_000_000},
		{now.Add(2 * time.Minute), 96_000_000},
	}
	var last Tick
	for _, step := range steps {
		primary.price, primary.at = step.price, step.at
		secondary.price, secondary.at = step.price, step.at
		last, err = runner.Step(t.Context(), step.at)
		if err != nil {
			t.Fatal(err)
		}
	}
	if last.Event != EventWaiting || last.Triggered || last.Decision == nil ||
		last.Decision.Reason != "risk_halt" || !runner.RiskHalted() {
		t.Fatalf("filled drawdown exit did not latch risk-off: %+v", last)
	}
	replayed, err := Replay(policy, recorder.ticks)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.strategy == nil || !replayed.strategy.riskHalted {
		t.Fatal("replay did not restore the risk-off latch")
	}
	resumed, err := ResumeRunner(policy, primary, secondary, quoter, recorder, recorder.ticks)
	if err != nil {
		t.Fatal(err)
	}
	resumedAt := now.Add(3 * time.Minute)
	primary.price, primary.at = 97_000_000, resumedAt
	secondary.price, secondary.at = 97_000_000, resumedAt
	resumedTick, err := resumed.Step(t.Context(), resumedAt)
	if err != nil {
		t.Fatal(err)
	}
	if resumedTick.Triggered || resumedTick.Decision == nil || resumedTick.Decision.Reason != "risk_halt" {
		t.Fatalf("restart cleared the risk-off latch: %+v", resumedTick)
	}
	primary.err = errors.New("source offline")
	unobservable, err := resumed.Step(t.Context(), resumedAt.Add(time.Minute))
	if err != nil || unobservable.Event != EventUnobservable || !resumed.RiskHalted() {
		t.Fatalf("unobservable tick cleared the risk-off latch: tick=%+v halted=%v err=%v",
			unobservable, resumed.RiskHalted(), err)
	}
}

func TestAdaptiveDrawdownLatchesWhileANormalSellSettles(t *testing.T) {
	policy := adaptiveTestPolicy()
	now := time.Unix(1_700_000_000, 0).UTC()
	primary := &stubSource{identity: policy.Trigger.PrimarySourceSHA256}
	secondary := &stubSource{identity: policy.Trigger.SecondarySourceSHA256}
	quoter := &stubQuoter{quote: func(sell bool, amount uint64) Quote {
		if !sell {
			return Quote{InputAmount: amount, EstimatedOutput: amount * 10, MinimumOutput: amount * 996 / 100}
		}
		return Quote{InputAmount: amount, EstimatedOutput: 100_000, MinimumOutput: 99_600}
	}}
	recorder := &stubRecorder{}
	runner, err := NewRunner(policy, primary, secondary, quoter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	step := func(at time.Time, price uint64) Tick {
		t.Helper()
		primary.price, primary.at = price, at
		secondary.price, secondary.at = price, at
		tick, stepErr := runner.Step(t.Context(), at)
		if stepErr != nil {
			t.Fatal(stepErr)
		}
		return tick
	}
	for index, price := range []uint64{100_000_000, 100_000_000, 97_200_000} {
		step(now.Add(time.Duration(index)*time.Minute), price)
	}
	decisionAt := now.Add(3 * time.Minute)
	decision := step(decisionAt, 97_100_000)
	if decision.Event != EventSignal || decision.Decision == nil ||
		decision.Decision.Strategy == StrategyRiskExit {
		t.Fatalf("normal sell decision = %+v", decision)
	}
	settled := step(decisionAt.Add(policy.Settle()), 96_900_000)
	if settled.Event != EventFilled || settled.Fill == nil || !settled.Fill.Sell ||
		settled.Decision == nil || settled.Decision.Strategy != StrategyRiskExit ||
		runner.strategy == nil || !runner.strategy.riskHalted {
		t.Fatalf("drawdown settlement did not latch risk-off: %+v halted=%v",
			settled, runner.strategy != nil && runner.strategy.riskHalted)
	}
	next := step(decisionAt.Add(policy.Settle()+time.Minute), 100_000_000)
	if next.Triggered || next.Decision == nil || next.Decision.Reason != "risk_halt" {
		t.Fatalf("post-settlement rebound escaped risk-off: %+v", next)
	}
	replayed, err := Replay(policy, recorder.ticks)
	if err != nil || replayed.strategy == nil || !replayed.strategy.riskHalted {
		t.Fatalf("replay lost the settlement drawdown latch: halted=%v err=%v",
			replayed.strategy != nil && replayed.strategy.riskHalted, err)
	}
}

func TestAdaptiveDrawdownWhileHoldingQuoteLatchesRiskOff(t *testing.T) {
	policy := adaptiveTestPolicy()
	strategy, err := newAdaptiveStrategy(policy.Adaptive)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewLedger(policy, 100_000_000)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err = ledger.Apply(Fill{
		Filled: true, Sell: true, SpentUnits: policy.InputAmount,
		ReceivedUnits: 95_000, FeeLamports: policy.FeeLamports,
	}, 100_000_000)
	if err != nil {
		t.Fatal(err)
	}
	decision, triggered, err := strategy.decide(
		time.Unix(1_700_000_000, 0).UTC(), 100_000_000, false, ledger,
	)
	if err != nil {
		t.Fatal(err)
	}
	if triggered || !strategy.riskHalted || decision.Regime != RegimeRisk ||
		decision.Strategy != StrategyObserve || decision.Reason != "drawdown_halt" {
		t.Fatalf("quote-side drawdown did not latch risk-off: %+v triggered=%v halted=%v",
			decision, triggered, strategy.riskHalted)
	}
}

func TestAdaptiveRiskPauseLiquidatesABuyThatSettledDuringDrawdown(t *testing.T) {
	policy := adaptiveTestPolicy()
	policy.SlippageBPS = 1
	policy.FeeLamports = 10_000
	policy.StartingInputUnits = 1_020_000
	policy.Adaptive = &AdaptivePolicy{
		Version: AdaptiveVersion, FastWindow: 2, SlowWindow: 4,
		MinimumSignalBPS: 212, MaxVolatilityBPS: 1_000,
		MaxQuoteImpactBPS: 500, MaxDrawdownBPS: 750,
		MaxObservationGapSeconds: 120,
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	primary := &stubSource{identity: policy.Trigger.PrimarySourceSHA256}
	secondary := &stubSource{identity: policy.Trigger.SecondarySourceSHA256}
	quoter := &stubQuoter{quote: func(sell bool, amount uint64) Quote {
		output := amount * primary.price / 1_000_000_000
		if !sell {
			output = amount * 1_000_000_000 / primary.price
		}
		minimum := (output*9_999 + 9_999) / 10_000
		return Quote{InputAmount: amount, EstimatedOutput: output, MinimumOutput: minimum}
	}}
	recorder := &stubRecorder{}
	runner, err := NewRunner(policy, primary, secondary, quoter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	step := func(offset time.Duration, price uint64) Tick {
		t.Helper()
		at := now.Add(offset)
		primary.price, primary.at = price, at
		secondary.price, secondary.at = price, at
		tick, stepErr := runner.Step(t.Context(), at)
		if stepErr != nil {
			t.Fatal(stepErr)
		}
		return tick
	}

	step(0, 100_000_000)
	step(time.Minute, 100_000_000)
	step(2*time.Minute, 97_000_000)
	if tick := step(3*time.Minute, 94_000_000); tick.Event != EventSignal {
		t.Fatalf("sell decision = %+v", tick)
	}
	if tick := step(3*time.Minute+policy.Settle(), 94_000_000); tick.Event != EventFilled || tick.Fill == nil || !tick.Fill.Sell {
		t.Fatalf("sell settlement = %+v", tick)
	}
	step(4*time.Minute+policy.Settle(), 100_000_000)
	if tick := step(5*time.Minute+policy.Settle(), 106_000_000); tick.Event != EventSignal {
		t.Fatalf("buy decision = %+v", tick)
	}
	buyFill := step(6*time.Minute, 1_000_000)
	if buyFill.Event != EventFilled || buyFill.Fill == nil || buyFill.Fill.Sell ||
		buyFill.Decision == nil || buyFill.Decision.Reason != "drawdown_halt" {
		t.Fatalf("buy did not settle into the risk pause: %+v", buyFill)
	}
	riskExit := step(7*time.Minute, 1_000_000)
	if riskExit.Event != EventSignal || riskExit.Decision == nil ||
		riskExit.Decision.Strategy != StrategyRiskExit || riskExit.DecisionQuote == nil ||
		riskExit.DecisionQuote.InputAmount <= policy.InputAmount {
		t.Fatalf("risk pause did not liquidate the acquired SOL: %+v", riskExit)
	}
	if tick := step(7*time.Minute+policy.Settle(), 1_000_000); tick.Event != EventFilled || tick.Fill == nil || !tick.Fill.Sell {
		t.Fatalf("risk exit settlement = %+v", tick)
	}
	if runner.Ledger().BaseUnits != 0 {
		t.Fatalf("risk exit left %d base units", runner.Ledger().BaseUnits)
	}
	replayed, err := Replay(policy, recorder.ticks)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Ledger != runner.Ledger() || replayed.Counts != runner.Counts() || replayed.nextSell {
		t.Fatalf("risk-exit replay diverged: replay=%+v/%+v sell=%v live=%+v/%+v",
			replayed.Ledger, replayed.Counts, replayed.nextSell, runner.Ledger(), runner.Counts())
	}
}

func TestAdaptiveRunnerDoesNotLearnFromDuplicateOrRegressedSamples(t *testing.T) {
	policy := adaptiveTestPolicy()
	policy.Adaptive.MaxDrawdownBPS = 5_000
	now := time.Unix(1_700_000_000, 0).UTC()
	primary := &stubSource{
		identity: policy.Trigger.PrimarySourceSHA256, price: 100_000_000, at: now,
	}
	secondary := &stubSource{
		identity: policy.Trigger.SecondarySourceSHA256, price: 100_000_000, at: now,
	}
	recorder := &stubRecorder{}
	runner, err := NewRunner(policy, primary, secondary, &stubQuoter{estimated: 100_000}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	if tick, stepErr := runner.Step(t.Context(), now); stepErr != nil || tick.Event != EventWaiting {
		t.Fatalf("first sample = %+v, %v", tick, stepErr)
	}
	for index := 1; index < int(policy.Adaptive.SlowWindow); index++ {
		tick, stepErr := runner.Step(t.Context(), now.Add(time.Duration(index)*5*time.Second))
		if stepErr != nil {
			t.Fatal(stepErr)
		}
		if tick.Event != EventUnobservable || tick.Reason != ReasonMarketPriceNotAdvanced {
			t.Fatalf("duplicate sample %d was accepted: %+v", index, tick)
		}
	}
	primary.at, secondary.at = now.Add(-time.Second), now.Add(-time.Second)
	tick, err := runner.Step(t.Context(), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventUnobservable || tick.Reason != ReasonMarketPriceNotAdvanced ||
		len(runner.strategy.prices) != 1 {
		t.Fatalf("regressed sample changed adaptive history: tick=%+v prices=%v", tick, runner.strategy.prices)
	}
	if _, err := Replay(policy, recorder.ticks); err != nil {
		t.Fatalf("duplicate-sample journal did not replay: %v", err)
	}
}

func TestAdaptiveWarmupMustFitInsideItsUTCPeriod(t *testing.T) {
	policy := adaptiveTestPolicy()
	policy.TickSeconds = 3_600
	policy.SettleSeconds = 600
	policy.Adaptive.MaxObservationGapSeconds = 7_200
	policy.Adaptive.SlowWindow = 25
	policy.Adaptive.FastWindow = 5
	if err := policy.Validate(); err == nil {
		t.Fatal("accepted an adaptive policy that can never warm before its UTC reset")
	}
	policy.Adaptive.SlowWindow = 24
	if err := policy.Validate(); err != nil {
		t.Fatalf("rejected a policy whose last warmup observation can still settle: %v", err)
	}
}

func TestAdaptiveStrategyRewarmsAfterADataGap(t *testing.T) {
	policy := adaptiveTestPolicy()
	policy.Adaptive.MaxDrawdownBPS = 5_000
	strategy, err := newAdaptiveStrategy(policy.Adaptive)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewLedger(policy, 100_000_000)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	for index := 0; index < 3; index++ {
		if _, _, err := strategy.decide(
			now.Add(time.Duration(index)*time.Minute), 100_000_000, false, ledger,
		); err != nil {
			t.Fatal(err)
		}
	}
	decision, triggered, err := strategy.decide(now.Add(10*time.Minute), 101_000_000, false, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if triggered || decision.Regime != RegimeWarming || decision.Reason != "collecting_history" ||
		len(strategy.prices) != 1 {
		t.Fatalf("long data gap reused stale history: %+v triggered=%v prices=%v",
			decision, triggered, strategy.prices)
	}
}

func TestAdaptiveRoundTripTreatsMalformedRiskExitQuoteAsMissed(t *testing.T) {
	policy := adaptiveTestPolicy()
	policy.Adaptive.MaxDrawdownBPS = 300
	decisionResult, err := ReplayRoundTrip(
		policy, []uint64{100_000_000, 95_000_000, 95_000_000},
		func(_ uint64, _ bool, _ uint64) (Quote, error) { return Quote{}, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if decisionResult.Counts.Missed == 0 || decisionResult.Counts.Sells != 0 {
		t.Fatalf("malformed decision quote did not match live missed behavior: %+v", decisionResult.Counts)
	}
	errorResult, err := ReplayRoundTrip(
		policy, []uint64{100_000_000, 95_000_000, 95_000_000},
		func(_ uint64, _ bool, _ uint64) (Quote, error) {
			return Quote{}, errors.New("route unavailable")
		},
	)
	if err != nil || errorResult.Counts.Missed == 0 {
		t.Fatalf("decision quote error did not match live missed behavior: counts=%+v err=%v",
			errorResult.Counts, err)
	}
	resizedResult, err := ReplayRoundTrip(
		policy, []uint64{100_000_000, 95_000_000, 95_000_000},
		func(_ uint64, _ bool, amount uint64) (Quote, error) {
			return Quote{InputAmount: amount + 1, EstimatedOutput: 95_000, MinimumOutput: 94_620}, nil
		},
	)
	if err != nil || resizedResult.Counts.Missed == 0 {
		t.Fatalf("resized decision quote did not match live missed behavior: counts=%+v err=%v",
			resizedResult.Counts, err)
	}
	call := 0
	settlementResult, err := ReplayRoundTrip(
		policy, []uint64{100_000_000, 95_000_000, 95_000_000},
		func(_ uint64, _ bool, amount uint64) (Quote, error) {
			call++
			if call == 1 {
				return Quote{InputAmount: amount, EstimatedOutput: 95_000, MinimumOutput: 94_620}, nil
			}
			return Quote{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if settlementResult.Counts.Missed == 0 || settlementResult.Counts.Sells != 0 {
		t.Fatalf("malformed settlement quote did not match live missed behavior: %+v", settlementResult.Counts)
	}
}
