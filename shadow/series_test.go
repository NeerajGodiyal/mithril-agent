package shadow

import (
	"context"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

// walkSource replays a fixed price series. It is deterministic on purpose: a
// shadow result that is not reproducible from its inputs cannot be audited, and
// an unauditable result is not evidence of anything.
type walkSource struct {
	identity string
	prices   []uint64
	index    int
	at       time.Time
}

func (w *walkSource) IdentitySHA256() string { return w.identity }

func (w *walkSource) Latest(context.Context, string) (pricetrigger.Sample, error) {
	price := w.prices[w.index]
	return pricetrigger.Sample{
		SourceSHA256: w.identity, Feed: pricetrigger.FeedSOLUSD,
		PriceMicros: price, ConfidenceMicros: price / 10_000, PublishedAt: w.at,
	}, nil
}

func (w *walkSource) advance(at time.Time) {
	w.at = at
	if w.index < len(w.prices)-1 {
		w.index++
	}
}

// A deterministic series with a rally, a crash, and a recovery: enough shape to
// exercise a trigger, a refusal, and a drawdown.
func priceSeries(count int) []uint64 {
	prices := make([]uint64, count)
	price := uint64(20_000_000)
	for index := range prices {
		switch {
		case index < count/3:
			price += 60_000
		case index < 2*count/3:
			price -= 90_000
		default:
			price += 40_000
		}
		prices[index] = price
	}
	return prices
}

func runSeries(t *testing.T, ticks int) (*Runner, *stubRecorder) {
	t.Helper()
	start := time.Unix(1_700_000_000, 0).UTC()
	prices := priceSeries(ticks)
	policy := sellPolicy()
	policy.Trigger.ThresholdMicros = 21_000_000
	policy.StartingInputUnits = 1_000_000_000

	primary := &walkSource{identity: policy.Trigger.PrimarySourceSHA256, prices: prices, at: start}
	secondary := &walkSource{identity: policy.Trigger.SecondarySourceSHA256, prices: prices, at: start}
	recorder := &stubRecorder{}
	runner, err := NewRunner(policy, primary, secondary, &stubQuoter{estimated: 21_525}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	for tick := range ticks {
		now := start.Add(time.Duration(tick) * policy.Tick())
		primary.advance(now)
		secondary.advance(now)
		if _, err := runner.Step(t.Context(), now); err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
	}
	return runner, recorder
}

// Over a long series the counters must add up. A category that quietly goes
// uncounted is how a shadow report ends up describing a run that did not happen.
func TestALongSeriesKeepsItsBooksBalanced(t *testing.T) {
	const ticks = 400
	runner, recorder := runSeries(t, ticks)
	counts := runner.Counts()

	if counts.Ticks != ticks {
		t.Fatalf("ticks = %d, want %d", counts.Ticks, ticks)
	}
	if got := runner.Stats().Settled; got != counts.Fills+counts.Refused {
		t.Errorf("settled = %d, but fills + refused = %d", got, counts.Fills+counts.Refused)
	}
	if counts.Deferred > counts.Signals {
		t.Errorf("deferred %d exceeds signals %d", counts.Deferred, counts.Signals)
	}
	// Every tick is journalled exactly once, plus the one opening record.
	if uint64(len(recorder.types)) != counts.Ticks+1 {
		t.Errorf("journalled %d records for %d ticks", len(recorder.types), counts.Ticks)
	}
	if recorder.types[0] != EventOpened {
		t.Errorf("the first record is %q, not the opening record", recorder.types[0])
	}
}

// The same inputs must produce the same books, every time. Without this a
// disputed result cannot be re-derived from the journal.
func TestTheSameSeriesAlwaysProducesTheSameBooks(t *testing.T) {
	first, _ := runSeries(t, 200)
	second, _ := runSeries(t, 200)

	if first.Counts() != second.Counts() {
		t.Fatalf("counts differ:\n%+v\n%+v", first.Counts(), second.Counts())
	}
	if first.Stats() != second.Stats() {
		t.Fatalf("stats differ:\n%+v\n%+v", first.Stats(), second.Stats())
	}
	left, right := first.Ledger(), second.Ledger()
	if left.BaseUnits != right.BaseUnits || left.QuoteUnits != right.QuoteUnits ||
		left.RealizedMicros != right.RealizedMicros ||
		left.MaxDrawdownMicros != right.MaxDrawdownMicros {
		t.Fatalf("ledgers differ:\n%+v\n%+v", left, right)
	}
}

// A crash in the middle of the series must be recorded as a drawdown. A report
// that shows no drawdown across a 15% fall is not measuring anything.
func TestALongSeriesRecordsItsDrawdown(t *testing.T) {
	runner, _ := runSeries(t, 400)
	if runner.Ledger().MaxDrawdownMicros == 0 {
		t.Fatal("a series containing a sustained fall recorded no drawdown")
	}
	if runner.Ledger().PeakEquityMicros == 0 {
		t.Fatal("no peak equity was ever recorded")
	}
}

// The report built from a long run must be internally consistent with the books
// it was built from.
func TestReportAgreesWithTheBooksItCameFrom(t *testing.T) {
	runner, _ := runSeries(t, 400)
	ledger := runner.Ledger()
	from := time.Unix(1_700_000_000, 0).UTC()
	closing := uint64(21_000_000)

	report, err := BuildReport(sellPolicyWithInventory(), ledger,
		runner.Counts(), runner.Stats(), closing, from, from.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	equity, err := ledger.EquityMicros(closing)
	if err != nil {
		t.Fatal(err)
	}
	if report.ClosingEquityMicros != equity {
		t.Errorf("report equity %d does not match the ledger's %d",
			report.ClosingEquityMicros, equity)
	}
	if report.RealizedMicros != ledger.RealizedMicros ||
		report.MaxDrawdownMicros != ledger.MaxDrawdownMicros ||
		report.TurnoverMicros != ledger.TurnoverMicros {
		t.Errorf("the report restated the books:\n%+v\n%+v", report, ledger)
	}
	if report.ObservableBPS != bpsScale {
		t.Errorf("a fully observable run reported %d bps of coverage", report.ObservableBPS)
	}
}

func sellPolicyWithInventory() Policy {
	policy := sellPolicy()
	policy.StartingInputUnits = 1_000_000_000
	return policy
}

// recordingTicks captures the tick stream a run produced, which is exactly what
// the journal stores.
type recordingTicks struct{ ticks []Tick }

func (r *recordingTicks) Record(_ time.Time, _ string, payload any) error {
	if tick, ok := payload.(Tick); ok {
		r.ticks = append(r.ticks, tick)
	}
	return nil
}

// The strongest property this package can offer: the report is re-derivable
// from the record. A reviewer should never have to trust the summary that
// shipped beside the journal — they should be able to recompute it.
func TestAReportIsFullyRederivableFromItsRecord(t *testing.T) {
	const ticks = 300
	start := time.Unix(1_700_000_000, 0).UTC()
	prices := priceSeries(ticks)
	policy := sellPolicy()
	policy.Trigger.ThresholdMicros = 21_000_000
	policy.StartingInputUnits = 1_000_000_000

	primary := &walkSource{identity: policy.Trigger.PrimarySourceSHA256, prices: prices, at: start}
	secondary := &walkSource{identity: policy.Trigger.SecondarySourceSHA256, prices: prices, at: start}
	recorder := &recordingTicks{}
	runner, err := NewRunner(policy, primary, secondary, &stubQuoter{estimated: 21_525}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	var lastPrice uint64
	for tick := range ticks {
		now := start.Add(time.Duration(tick) * policy.Tick())
		primary.advance(now)
		secondary.advance(now)
		observed, err := runner.Step(t.Context(), now)
		if err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		if observed.PriceMicros != 0 {
			lastPrice = observed.PriceMicros
		}
	}

	replayed, err := Replay(policy, recorder.ticks)
	if err != nil {
		t.Fatalf("the record could not be replayed: %v", err)
	}
	if replayed.Counts != runner.Counts() {
		t.Errorf("counts differ:\n  live    %+v\n  replay  %+v", runner.Counts(), replayed.Counts)
	}
	if replayed.Stats != runner.Stats() {
		t.Errorf("stats differ:\n  live    %+v\n  replay  %+v", runner.Stats(), replayed.Stats)
	}
	live := runner.Ledger()
	if replayed.Ledger.BaseUnits != live.BaseUnits ||
		replayed.Ledger.QuoteUnits != live.QuoteUnits ||
		replayed.Ledger.RealizedMicros != live.RealizedMicros ||
		replayed.Ledger.FeesMicros != live.FeesMicros ||
		replayed.Ledger.TurnoverMicros != live.TurnoverMicros ||
		replayed.Ledger.MaxDrawdownMicros != live.MaxDrawdownMicros ||
		replayed.Ledger.CostBasisMicros != live.CostBasisMicros {
		t.Fatalf("ledgers differ:\n  live    %+v\n  replay  %+v", live, replayed.Ledger)
	}
	if replayed.ClosingPrice != lastPrice {
		t.Errorf("closing price = %d, want %d", replayed.ClosingPrice, lastPrice)
	}

	// And the two reports must agree field for field.
	from := start
	liveReport, err := BuildReport(policy, live, runner.Counts(), runner.Stats(),
		lastPrice, from, from.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	replayReport, err := BuildReport(policy, replayed.Ledger, replayed.Counts,
		replayed.Stats, replayed.ClosingPrice, from, from.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if found := Compare(liveReport, replayReport); len(found) != 0 {
		t.Fatalf("the recomputed report disagrees with the live one: %+v", found)
	}
}

// A tampered record must produce a different report, or re-derivation proves
// nothing.
func TestATamperedRecordChangesTheRecomputedReport(t *testing.T) {
	start := time.Unix(1_700_000_000, 0).UTC()
	prices := priceSeries(120)
	policy := sellPolicy()
	policy.Trigger.ThresholdMicros = 21_000_000
	policy.StartingInputUnits = 1_000_000_000

	primary := &walkSource{identity: policy.Trigger.PrimarySourceSHA256, prices: prices, at: start}
	secondary := &walkSource{identity: policy.Trigger.SecondarySourceSHA256, prices: prices, at: start}
	recorder := &recordingTicks{}
	runner, err := NewRunner(policy, primary, secondary, &stubQuoter{estimated: 21_525}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	for tick := range 120 {
		now := start.Add(time.Duration(tick) * policy.Tick())
		primary.advance(now)
		secondary.advance(now)
		if _, err := runner.Step(t.Context(), now); err != nil {
			t.Fatal(err)
		}
	}
	honest, err := Replay(policy, recorder.ticks)
	if err != nil {
		t.Fatal(err)
	}

	// Flatter one fill by claiming it received more than it did.
	tampered := make([]Tick, len(recorder.ticks))
	copy(tampered, recorder.ticks)
	changed := false
	for index, tick := range tampered {
		if tick.Fill != nil && tick.Fill.Filled {
			altered := *tick.Fill
			altered.ReceivedUnits += 1_000_000
			tampered[index].Fill = &altered
			changed = true
			break
		}
	}
	if !changed {
		t.Skip("the series produced no fill to tamper with")
	}
	dishonest, err := Replay(policy, tampered)
	if err != nil {
		return // refusing outright is an equally good outcome
	}
	if dishonest.Ledger.QuoteUnits == honest.Ledger.QuoteUnits {
		t.Fatal("tampering with a recorded fill did not change the recomputed books")
	}
}
