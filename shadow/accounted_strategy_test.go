package shadow

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

func accountedStrategyFixture(t *testing.T) (*AccountedStrategy, []Tick) {
	t.Helper()
	p := adaptiveTestPolicy()
	mainnet := mainnetPolicy()
	p.Cluster, p.Market, p.QuoteRoute, p.QuotePeg = mainnet.Cluster, mainnet.Market, mainnet.QuoteRoute, mainnet.QuotePeg
	p.StartingInputUnits = 2_000_000
	p.Adaptive.MaxDrawdownBPS = 5_000
	p.Adaptive.CooldownSeconds = 60
	primary := &stubSource{identity: p.Trigger.PrimarySourceSHA256}
	secondary := &stubSource{identity: p.Trigger.SecondarySourceSHA256}
	peg1 := &stubSource{identity: p.QuotePeg.PrimarySourceSHA256, price: 1_000_000}
	peg2 := &stubSource{identity: p.QuotePeg.SecondarySourceSHA256, price: 1_000_000}
	quoter := &stubQuoter{quote: func(_ bool, amount uint64) Quote {
		output := amount * primary.price / 1_000_000_000
		return Quote{InputAmount: amount, EstimatedOutput: output, MinimumOutput: (output*9960 + 9999) / 10000}
	}}
	recorder := &stubRecorder{}
	runner, err := NewRunner(p, primary, secondary, quoter, recorder, peg1, peg2)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	for _, price := range []uint64{100_000_000, 99_000_000, 98_000_000, 97_000_000, 96_000_000} {
		primary.at, secondary.at, peg1.at, peg2.at = at, at, at, at
		primary.price, secondary.price = price, price
		tick, err := runner.Step(context.Background(), at)
		if err != nil {
			t.Fatal(err)
		}
		if tick.Event == EventSignal {
			break
		}
		at = at.Add(time.Minute)
	}
	s, err := NewAccountedStrategy(p, recorder.ticks)
	if err != nil {
		t.Fatal(err)
	}
	return s, recorder.ticks
}

func accountedStrategySamples(s *AccountedStrategy, at time.Time, price uint64) [4]pricetrigger.Sample {
	return [4]pricetrigger.Sample{
		{SourceSHA256: s.policy.Trigger.PrimarySourceSHA256, Feed: s.policy.Trigger.Feed, PublishedAt: at, PriceMicros: price},
		{SourceSHA256: s.policy.Trigger.SecondarySourceSHA256, Feed: s.policy.Trigger.Feed, PublishedAt: at, PriceMicros: price},
		{SourceSHA256: s.policy.QuotePeg.PrimarySourceSHA256, Feed: s.policy.QuotePeg.Feed, PublishedAt: at, PriceMicros: 1_000_000},
		{SourceSHA256: s.policy.QuotePeg.SecondarySourceSHA256, Feed: s.policy.QuotePeg.Feed, PublishedAt: at, PriceMicros: 1_000_000},
	}
}

func accountedPendingOutcome(s *AccountedStrategy, success bool, id string) AccountedOutcome {
	q, _ := s.PendingQuote()
	l := s.Ledger()
	o := AccountedOutcome{AccountingSHA256: strings.Repeat(id, 64), TokenMint: mainnetUSDCMint,
		Success: success, Sell: s.NextSell(), FeeLamports: 500,
		PreBaseUnits: l.BaseUnits, PreQuoteUnits: l.QuoteUnits,
		PostBaseUnits: l.BaseUnits - 500, PostQuoteUnits: l.QuoteUnits}
	if success {
		o.SpentUnits, o.ReceivedUnits = q.InputAmount, q.EstimatedOutput
		if o.Sell {
			o.PostBaseUnits -= o.SpentUnits
			o.PostQuoteUnits += o.ReceivedUnits
		} else {
			o.PostQuoteUnits -= o.SpentUnits
			o.PostBaseUnits += o.ReceivedUnits
		}
	}
	return o
}

func TestAccountedStrategyKeepsPendingHistoryAndExactOutcomes(t *testing.T) {
	s, ticks := accountedStrategyFixture(t)
	opening := s.Ledger()
	q, ok := s.PendingQuote()
	if !ok || q != *ticks[len(ticks)-1].DecisionQuote {
		t.Fatal("first pending quote changed")
	}
	at := s.at.Add(time.Minute) // Beyond the ordinary paper settlement deadline.
	samples := accountedStrategySamples(s, at, 95_000_000)
	decision, err := s.Observe(at, samples[0], samples[1], samples[2], samples[3])
	if err != nil || decision.ReadyForQuote || !s.Pending() || !s.strategy.lastObservation.Equal(at) {
		t.Fatalf("pending observation lost: %+v %v", decision, err)
	}
	outcome := accountedPendingOutcome(s, true, "a")
	knownAt := at.Add(time.Second)
	belowFloor := outcome
	belowFloor.ReceivedUnits = q.MinimumOutput - 1
	belowFloor.PostQuoteUnits = belowFloor.PreQuoteUnits + belowFloor.ReceivedUnits
	if err := s.ApplyOutcome(belowFloor, knownAt); err == nil || !s.Pending() {
		t.Fatal("successful outcome below pending floor accepted")
	}
	if err := s.ApplyOutcome(outcome, knownAt); err != nil {
		t.Fatal(err)
	}
	if s.Pending() || s.NextSell() || s.nextAmount != outcome.ReceivedUnits || !s.strategy.lastFill.Equal(knownAt) {
		t.Fatal("actual outcome did not continue the original strategy")
	}
	if s.Ledger().openingBaseUnits != opening.openingBaseUnits {
		t.Fatal("opening ledger was reset")
	}
	before := s.Ledger()
	if err := s.ApplyOutcome(outcome, knownAt); err != nil || !reflect.DeepEqual(before, s.Ledger()) {
		t.Fatal("repeat charged twice", err)
	}
	changed := outcome
	changed.FeeLamports++
	if err := s.ApplyOutcome(changed, knownAt); err == nil {
		t.Fatal("changed duplicate accepted")
	}
	if err := s.ApplyOutcome(outcome, knownAt.Add(time.Nanosecond)); err == nil {
		t.Fatal("duplicate knowledge time renewed")
	}
	if _, err := s.Observe(at, samples[0], samples[1], samples[2], samples[3]); err == nil {
		t.Fatal("future outcome used for earlier observation")
	}
}

func TestAccountedStrategyFailureAndTransactionalGuards(t *testing.T) {
	s, _ := accountedStrategyFixture(t)
	before := s.Ledger()
	outcome := accountedPendingOutcome(s, false, "b")
	bad := outcome
	bad.PostBaseUnits++
	if err := s.ApplyOutcome(bad, s.at.Add(time.Second)); err == nil || !s.Pending() || !reflect.DeepEqual(before, s.Ledger()) {
		t.Fatal("invalid balance mutated strategy")
	}
	bad = outcome
	bad.AccountingSHA256 = strings.Repeat("A", 64)
	if err := s.ApplyOutcome(bad, s.at.Add(time.Second)); err == nil {
		t.Fatal("noncanonical accounting digest accepted")
	}
	firstAt := s.at.Add(time.Second)
	if err := s.ApplyOutcome(outcome, firstAt); err != nil {
		t.Fatal(err)
	}
	if s.Pending() || !s.NextSell() || !s.strategy.lastFill.IsZero() || s.Ledger().BaseUnits != before.BaseUnits-500 {
		t.Fatal("failed transaction advanced successful-fill state")
	}
	for _, price := range []uint64{94_000_000, 92_000_000, 90_000_000} {
		at := s.at.Add(time.Minute)
		x := accountedStrategySamples(s, at, price)
		d, err := s.Observe(at, x[0], x[1], x[2], x[3])
		if err != nil {
			t.Fatal(err)
		}
		if !d.ReadyForQuote {
			continue
		}
		output := d.InputAmount * price / 1_000_000_000
		q := Quote{InputAmount: d.InputAmount, EstimatedOutput: output, MinimumOutput: (output*9960 + 9999) / 10000, ReceivedAt: at}
		wrong := q
		wrong.InputAmount++
		if err := s.CommitDecision(wrong, at); err == nil || s.Pending() {
			t.Fatal("wrong quote mutated pending")
		}
		late := q
		late.ReceivedAt = at.Add(time.Duration(s.policy.Adaptive.MaxObservationGapSeconds)*time.Second + time.Nanosecond)
		if err := s.CommitDecision(late, late.ReceivedAt); err == nil || s.Pending() {
			t.Fatal("new quote renewed an expired strategy observation")
		}
		if err := s.CommitDecision(q, at); err != nil {
			t.Fatal(err)
		}
		second := accountedPendingOutcome(s, false, "c")
		if err := s.ApplyOutcome(second, at.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		ledger := s.Ledger()
		if err := s.ApplyOutcome(outcome, firstAt); err != nil || !reflect.DeepEqual(ledger, s.Ledger()) {
			t.Fatal("older duplicate was not a no-op", err)
		}
		return
	}
	t.Fatal("history did not produce another quote opportunity")
}

func TestAccountedStrategySeedAndObservationIsolation(t *testing.T) {
	s, ticks := accountedStrategyFixture(t)
	for _, change := range []func([]Tick){
		func(x []Tick) { x[0].DecisionMissed = true },
		func(x []Tick) { x[0].Fill = &Fill{} },
		func(x []Tick) { x[0].Event = EventSignal },
		func(x []Tick) { x[len(x)-1].DecisionQuote = nil },
	} {
		bad := append([]Tick(nil), ticks...)
		change(bad)
		if _, err := NewAccountedStrategy(s.policy, bad); err == nil {
			t.Fatal("non-first-action seed accepted")
		}
	}
	p := copyAccountedPolicy(s.policy)
	other, err := NewAccountedStrategy(p, ticks)
	if err != nil {
		t.Fatal(err)
	}
	p.Adaptive.MaxDrawdownBPS = 1
	view := other.Ledger()
	view.Policy.Adaptive.MaxDrawdownBPS = 2
	if other.policy.Adaptive.MaxDrawdownBPS != 5000 {
		t.Fatal("caller mutated retained policy")
	}
	before := append([]uint64(nil), other.strategy.prices...)
	at := other.at.Add(time.Minute)
	x := accountedStrategySamples(other, at, 90_000_000)
	x[3].PriceMicros = 2_000_000
	if _, err := other.Observe(at, x[0], x[1], x[2], x[3]); err == nil || !reflect.DeepEqual(before, other.strategy.prices) {
		t.Fatal("invalid peg mutated adaptive history")
	}
	x = accountedStrategySamples(other, at, 40_000_000)
	if _, err := other.Observe(at, x[0], x[1], x[2], x[3]); err != nil {
		t.Fatal(err)
	}
	if !other.RiskHalted() || !other.Pending() {
		t.Fatal("pending observation lost risk latch")
	}
	o := accountedPendingOutcome(other, false, "d")
	if err := other.ApplyOutcome(o, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if !other.RiskHalted() {
		t.Fatal("failed outcome reset risk latch")
	}
}

func TestAccountedStrategyCancellationPreservesActualHistory(t *testing.T) {
	s, _ := accountedStrategyFixture(t)
	first := accountedPendingOutcome(s, true, "a")
	firstAt := s.at.Add(time.Second)
	if err := s.ApplyOutcome(first, firstAt); err != nil {
		t.Fatal(err)
	}
	var quote Quote
	var observationAt time.Time
	for _, price := range []uint64{101_000_000, 103_000_000, 105_000_000, 107_000_000, 109_000_000} {
		at := s.at.Add(time.Minute)
		x := accountedStrategySamples(s, at, price)
		d, err := s.Observe(at, x[0], x[1], x[2], x[3])
		if err != nil {
			t.Fatal(err)
		}
		if !d.ReadyForQuote {
			continue
		}
		if d.Sell || d.InputAmount != first.ReceivedUnits {
			t.Fatal("reverse opportunity did not retain actual proceeds")
		}
		output := d.InputAmount * 1_000_000_000 / price
		quote = Quote{InputAmount: d.InputAmount, EstimatedOutput: output, MinimumOutput: (output*9960 + 9999) / 10000, ReceivedAt: at}
		if err := s.CommitDecision(quote, at.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		observationAt = at
		break
	}
	if !s.Pending() {
		t.Fatal("history did not produce a committed reverse decision")
	}
	observed := s.at.Add(time.Minute)
	x := accountedStrategySamples(s, observed, s.price+2_000_000)
	if d, err := s.Observe(observed, x[0], x[1], x[2], x[3]); err != nil || d.ReadyForQuote || !s.Pending() {
		t.Fatalf("pending market observation: %+v %v", d, err)
	}
	// Detach reference fields so an accidental in-place reset also fails the
	// comparison, rather than mutating both the actual and expected snapshots.
	before := *s
	before.policy, before.ledger = copyAccountedPolicy(s.policy), s.Ledger()
	before.strategy.prices = append([]uint64(nil), s.strategy.prices...)
	pendingCopy := *s.pending
	before.pending = &pendingCopy
	before.outcomes = make(map[string]accountedStrategyOutcome, len(s.outcomes))
	for id, value := range s.outcomes {
		before.outcomes[id] = value
	}
	at := observed.Add(time.Second).In(time.FixedZone("operator", 3600))
	for _, mode := range []string{"input", "output", "minimum", "receipt time", "direction", "observation", "zero time", "backdated"} {
		t.Run(mode, func(t *testing.T) {
			q, sell, sourceAt, cancelledAt := quote, false, observationAt, at
			switch mode {
			case "input":
				q.InputAmount++
			case "output":
				q.EstimatedOutput++
			case "minimum":
				q.MinimumOutput++
			case "receipt time":
				q.ReceivedAt = q.ReceivedAt.Add(time.Nanosecond)
			case "direction":
				sell = true
			case "observation":
				sourceAt = sourceAt.Add(time.Nanosecond)
			case "zero time":
				cancelledAt = time.Time{}
			case "backdated":
				cancelledAt = observed.Add(-time.Nanosecond)
			}
			if err := s.CancelPendingDecision(q, sell, sourceAt, cancelledAt); err == nil || !reflect.DeepEqual(*s, before) {
				t.Fatalf("invalid cancellation changed state: %v", err)
			}
		})
	}
	if err := s.CancelPendingDecision(quote, false, observationAt, at); err != nil {
		t.Fatal(err)
	}
	want := before
	want.pending, want.ready, want.at = nil, AccountedDecision{}, at.UTC()
	if !reflect.DeepEqual(*s, want) {
		t.Fatal("cancellation changed fields beyond pending, readiness and knowledge time")
	}
	if err := s.CancelPendingDecision(quote, false, observationAt, at.Add(time.Second)); err == nil || !reflect.DeepEqual(*s, want) {
		t.Fatal("missing pending decision was accepted or mutated")
	}
	if err := s.CommitDecision(quote, at.Add(time.Second)); err == nil || !reflect.DeepEqual(*s, want) {
		t.Fatal("cancellation retained stale quote readiness")
	}
	freshAt := at.Add(time.Minute).UTC()
	x = accountedStrategySamples(s, freshAt, s.price+2_000_000)
	d, err := s.Observe(freshAt, x[0], x[1], x[2], x[3])
	if err != nil || !d.ReadyForQuote || d.Sell || d.InputAmount != first.ReceivedUnits {
		t.Fatalf("fresh observation did not preserve reverse opportunity: %+v %v", d, err)
	}
	if s.Ledger().Fills != before.ledger.Fills || s.Ledger().BaseUnits != before.ledger.BaseUnits || s.Ledger().QuoteUnits != before.ledger.QuoteUnits ||
		!s.strategy.lastFill.Equal(firstAt) || !reflect.DeepEqual(s.outcomes, before.outcomes) {
		t.Fatal("post-cancellation observation fabricated a fill or reset accounting history")
	}
}

func TestAccountedStrategyCancellationPreservesRiskLatch(t *testing.T) {
	s, _ := accountedStrategyFixture(t)
	quote, _ := s.PendingQuote()
	sourceAt := s.pending.decidedAt
	at := s.at.Add(time.Minute)
	x := accountedStrategySamples(s, at, 40_000_000)
	if _, err := s.Observe(at, x[0], x[1], x[2], x[3]); err != nil || !s.RiskHalted() {
		t.Fatalf("fixture did not latch actual observed drawdown: %v", err)
	}
	before := *s
	if err := s.CancelPendingDecision(quote, s.NextSell(), sourceAt, at); err != nil {
		t.Fatal(err)
	}
	before.pending, before.ready = nil, AccountedDecision{}
	if !reflect.DeepEqual(*s, before) || !s.RiskHalted() {
		t.Fatal("cancellation reset observed risk history")
	}
}

func TestAccountedStrategyTwoSuccessLegsReplayExactly(t *testing.T) {
	seed, ticks := accountedStrategyFixture(t)
	run := func() *AccountedStrategy {
		s, err := NewAccountedStrategy(seed.policy, ticks)
		if err != nil {
			t.Fatal(err)
		}
		first := accountedPendingOutcome(s, true, "e")
		firstAt := s.at.Add(time.Second)
		if err := s.ApplyOutcome(first, firstAt); err != nil {
			t.Fatal(err)
		}
		cooldownAt := firstAt.Add(30 * time.Second)
		x := accountedStrategySamples(s, cooldownAt, 99_000_000)
		d, err := s.Observe(cooldownAt, x[0], x[1], x[2], x[3])
		if err != nil || d.ReadyForQuote || d.Decision.Reason != "cooldown" {
			t.Fatalf("cooldown lost: %+v %v", d, err)
		}
		for _, price := range []uint64{101_000_000, 103_000_000, 105_000_000, 107_000_000, 109_000_000} {
			at := s.at.Add(time.Minute)
			x = accountedStrategySamples(s, at, price)
			d, err = s.Observe(at, x[0], x[1], x[2], x[3])
			if err != nil {
				t.Fatal(err)
			}
			if !d.ReadyForQuote {
				continue
			}
			if d.Sell || d.InputAmount != first.ReceivedUnits {
				t.Fatal("reverse leg did not use actual proceeds")
			}
			output := d.InputAmount * 1_000_000_000 / price
			q := Quote{InputAmount: d.InputAmount, EstimatedOutput: output, MinimumOutput: (output*9960 + 9999) / 10000, ReceivedAt: at}
			if err := s.CommitDecision(q, at); err != nil {
				t.Fatal(err)
			}
			second := accountedPendingOutcome(s, true, "f")
			if err := s.ApplyOutcome(second, at.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if !s.NextSell() || s.Pending() || s.nextAmount != second.ReceivedUnits || s.Ledger().Fills != 2 {
				t.Fatal("two actual legs did not continue inventory")
			}
			return s
		}
		t.Fatal("reverse history did not produce a buy opportunity")
		return nil
	}
	first, rebuilt := run(), run()
	if !reflect.DeepEqual(first, rebuilt) {
		t.Fatal("same original replay and exact actual events changed continuation")
	}
}
