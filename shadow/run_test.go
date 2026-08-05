package shadow

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

type stubSource struct {
	identity string
	price    uint64
	err      error
	at       time.Time
}

func (s *stubSource) IdentitySHA256() string { return s.identity }

func (s *stubSource) Latest(context.Context, string) (pricetrigger.Sample, error) {
	if s.err != nil {
		return pricetrigger.Sample{}, s.err
	}
	return pricetrigger.Sample{
		SourceSHA256: s.identity, Feed: pricetrigger.FeedSOLUSD,
		PriceMicros: s.price, ConfidenceMicros: s.price / 10_000,
		PublishedAt: s.at,
	}, nil
}

type stubQuoter struct {
	estimated uint64
	err       error
	calls     int
}

func (q *stubQuoter) Quote(context.Context, string, uint64, uint16) (Quote, error) {
	q.calls++
	if q.err != nil {
		return Quote{}, q.err
	}
	return Quote{
		InputAmount: 1_000_000, EstimatedOutput: q.estimated,
		MinimumOutput: q.estimated - q.estimated/100,
	}, nil
}

type stubRecorder struct {
	types []string
	err   error
}

func (r *stubRecorder) Record(_ time.Time, eventType string, _ any) error {
	if r.err != nil {
		return r.err
	}
	r.types = append(r.types, eventType)
	return nil
}

func newTestRunner(t *testing.T, price uint64, at time.Time) (
	*Runner, *stubSource, *stubSource, *stubQuoter, *stubRecorder,
) {
	t.Helper()
	policy := sellPolicy()
	policy.Trigger.ThresholdMicros = 22_000_000
	policy.StartingInputUnits = 1_000_000_000
	primary := &stubSource{identity: policy.Trigger.PrimarySourceSHA256, price: price, at: at}
	secondary := &stubSource{identity: policy.Trigger.SecondarySourceSHA256, price: price, at: at}
	quoter := &stubQuoter{estimated: 21_525}
	recorder := &stubRecorder{}
	runner, err := NewRunner(policy, primary, secondary, quoter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	return runner, primary, secondary, quoter, recorder
}

// The single most important property of this package, enforced structurally
// rather than by review: nothing here can reach code that signs or submits.
func TestShadowCannotReachASigner(t *testing.T) {
	forbidden := []string{
		"mithril-agent/signer", "mithril-agent/signerclient",
		"mithril-agent/submitter", "mithril-agent/submitterclient",
		"mithril-agent/sealedtx", "mithril-agent/policyauthority",
		"mithril-agent/riskgrant", "mithril-agent/txflow",
		"crypto/ed25519",
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, imported := range file.Imports {
			path := strings.Trim(imported.Path.Value, `"`)
			for _, banned := range forbidden {
				if strings.Contains(path, banned) {
					t.Errorf("%s imports %s: shadow mode must not be able to sign",
						entry.Name(), path)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("the import guard checked no files, so it proves nothing")
	}
}

// The shadow package must not name a private key anywhere, even in a comment
// that a later change could turn into a field.
func TestShadowDeclaresNoKeyMaterial(t *testing.T) {
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range entries {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"Keypair", "PrivateKey", "Sign(", "Submit("} {
			if strings.Contains(string(source), banned) {
				t.Errorf("%s mentions %q", name, banned)
			}
		}
	}
}

// Below the threshold the strategy waits, and waiting must never quote: a quote
// is a network call, and a rule that quotes every tick is a rule that costs
// money and rate limit to run.
func TestBelowThresholdWaitsWithoutQuoting(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	runner, _, _, quoter, recorder := newTestRunner(t, 20_000_000, now)

	tick, err := runner.Step(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventWaiting || tick.Triggered {
		t.Fatalf("below the threshold produced %+v", tick)
	}
	if quoter.calls != 0 {
		t.Errorf("waiting called the quoter %d times", quoter.calls)
	}
	if len(recorder.types) != 2 || recorder.types[0] != EventOpened {
		t.Errorf("records = %v, want the opening record then a wait", recorder.types)
	}
}

// A signal is never scored on the price that produced it. It settles later,
// against a price observed after the decision.
func TestASignalSettlesAgainstALaterPrice(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	runner, primary, secondary, _, recorder := newTestRunner(t, 23_000_000, now)

	tick, err := runner.Step(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventSignal {
		t.Fatalf("above the threshold produced %q", tick.Event)
	}
	if runner.Counts().Fills != 0 {
		t.Fatal("a decision was scored in the same instant it was made")
	}

	// Too early: still nothing settles.
	early := now.Add(10 * time.Second)
	primary.at, secondary.at = early, early
	if tick, err = runner.Step(t.Context(), early); err != nil {
		t.Fatal(err)
	}
	if tick.Event == EventFilled {
		t.Fatal("a decision settled before its own settlement delay elapsed")
	}

	// After the delay, at a slightly higher price, it fills.
	later := now.Add(31 * time.Second)
	primary.at, secondary.at = later, later
	primary.price, secondary.price = 23_100_000, 23_100_000
	if tick, err = runner.Step(t.Context(), later); err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventFilled || tick.Fill == nil || !tick.Fill.Filled {
		t.Fatalf("the decision did not settle: %+v", tick)
	}
	if tick.Fill.SettlePriceMicros == tick.Fill.DecisionPriceMicros {
		t.Error("the fill was scored at the decision price")
	}
	if runner.Counts().Fills != 1 {
		t.Errorf("fills = %d, want 1", runner.Counts().Fills)
	}
	if !strings.Contains(strings.Join(recorder.types, ","), EventFilled) {
		t.Error("the fill was not journalled")
	}
}

// A signal that cannot be quoted is a missed signal, and must be counted. Not
// counting it is how a shadow result claims a trade rate it never had.
func TestAnUnquotableSignalIsCountedAsMissed(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	runner, _, _, quoter, _ := newTestRunner(t, 23_000_000, now)
	quoter.err = errors.New("pool unavailable")

	tick, err := runner.Step(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventMissed {
		t.Fatalf("an unquotable signal produced %q", tick.Event)
	}
	counts := runner.Counts()
	if counts.Missed != 1 || counts.Signals != 1 || counts.Fills != 0 {
		t.Fatalf("counts = %+v", counts)
	}
}

// An unreadable market must never be scored. It is recorded as unobservable so
// the report can say how much of the period it could not see.
func TestAnUnreadableMarketIsNeverScored(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	runner, primary, _, quoter, _ := newTestRunner(t, 23_000_000, now)
	primary.err = errors.New("endpoint refused the connection")

	tick, err := runner.Step(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventUnobservable || tick.PriceMicros != 0 {
		t.Fatalf("an unreadable market produced %+v", tick)
	}
	if runner.Counts().Unobservable != 1 || quoter.calls != 0 {
		t.Fatalf("counts = %+v, quotes = %d", runner.Counts(), quoter.calls)
	}
}

// Only one decision is ever in flight, matching the real engine. A second
// signal while one is settling is deferred, not stacked.
func TestOnlyOneDecisionIsInFlight(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	runner, primary, secondary, quoter, _ := newTestRunner(t, 23_000_000, now)

	if _, err := runner.Step(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	next := now.Add(5 * time.Second)
	primary.at, secondary.at = next, next
	if _, err := runner.Step(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	if quoter.calls != 1 {
		t.Fatalf("quoted %d times while one decision was already in flight", quoter.calls)
	}
	if runner.Counts().Deferred != 1 {
		t.Errorf("deferred = %d, want 1", runner.Counts().Deferred)
	}
}

// The runner must refuse a configuration whose sources are not the ones the
// policy pinned, or the result describes a strategy nobody configured.
func TestRunnerRefusesMismatchedSources(t *testing.T) {
	policy := sellPolicy()
	recorder := &stubRecorder{}
	quoter := &stubQuoter{estimated: 21_525}
	right := &stubSource{identity: policy.Trigger.PrimarySourceSHA256}
	wrong := &stubSource{identity: strings.Repeat("c", 64)}

	if _, err := NewRunner(policy, right, wrong, quoter, recorder); err == nil {
		t.Error("a source that does not match the policy was accepted")
	}
	same := &stubSource{identity: policy.Trigger.PrimarySourceSHA256}
	if _, err := NewRunner(policy, right, same, quoter, recorder); err == nil {
		t.Error("two identical sources were accepted as independent")
	}
	if _, err := NewRunner(policy, right, nil, quoter, recorder); err == nil {
		t.Error("a missing source was accepted")
	}
}

// A journal that cannot be written must stop the run. Continuing would produce
// a report with silent holes in it.
func TestARecordingFailureStopsTheRun(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	runner, _, _, _, recorder := newTestRunner(t, 20_000_000, now)
	recorder.err = errors.New("disk full")

	if _, err := runner.Step(t.Context(), now); err == nil {
		t.Fatal("the run continued after the journal refused a record")
	}
}

// A tick spent settling is still a tick in which the market signalled. Not
// counting it would shrink the denominator the report divides by and quietly
// flatter how much of the market the strategy could act on.
func TestASignalOnASettlingTickIsStillCounted(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	runner, primary, secondary, _, _ := newTestRunner(t, 23_000_000, now)

	if _, err := runner.Step(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	before := runner.Counts()
	if before.Signals != 1 {
		t.Fatalf("signals after the first tick = %d, want 1", before.Signals)
	}

	// Settle on a tick where the market is still above the threshold.
	later := now.Add(31 * time.Second)
	primary.at, secondary.at = later, later
	tick, err := runner.Step(t.Context(), later)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventFilled {
		t.Fatalf("the decision did not settle: %q", tick.Event)
	}
	after := runner.Counts()
	if after.Signals != before.Signals+1 {
		t.Errorf("signals = %d, want %d: the settling tick's signal went uncounted",
			after.Signals, before.Signals+1)
	}
	if after.Deferred != before.Deferred+1 {
		t.Errorf("deferred = %d, want %d", after.Deferred, before.Deferred+1)
	}
	// One record per tick still holds: settling and signalling share the tick.
	if after.Ticks != 2 {
		t.Errorf("ticks = %d, want 2", after.Ticks)
	}
}

// Below the threshold, a settling tick must not invent a signal.
func TestASettlingTickInventsNoSignalWhenTheRuleIsQuiet(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	runner, primary, secondary, _, _ := newTestRunner(t, 23_000_000, now)

	if _, err := runner.Step(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	before := runner.Counts()

	// The market falls below the threshold before the decision settles.
	later := now.Add(31 * time.Second)
	primary.at, secondary.at = later, later
	primary.price, secondary.price = 21_000_000, 21_000_000
	if _, err := runner.Step(t.Context(), later); err != nil {
		t.Fatal(err)
	}
	if got := runner.Counts().Signals; got != before.Signals {
		t.Errorf("signals = %d, want %d: a quiet market produced a signal", got, before.Signals)
	}
}

// A trough that happens while a decision is pending is still a trough. Marking
// only on the branches that happen to remember meant the reported worst fall
// could only ever understate the real one.
func TestDrawdownSeesTroughsOnEveryObservableTick(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	runner, primary, secondary, _, _ := newTestRunner(t, 30_000_000, now)

	// Opens a decision at $30 on a 1 SOL book.
	if _, err := runner.Step(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	// Still above the $22 threshold, so this tick defers rather than settling.
	dip := now.Add(10 * time.Second)
	primary.at, secondary.at = dip, dip
	primary.price, secondary.price = 24_000_000, 24_000_000
	tick, err := runner.Step(t.Context(), dip)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventSignal || runner.Counts().Deferred != 1 {
		t.Fatalf("expected a deferred tick, got %q (deferred=%d)",
			tick.Event, runner.Counts().Deferred)
	}
	// The rule never uses the raw price: it uses the conservative one, which
	// deducts each source's confidence. At a 1-in-10,000 confidence that is
	// ($30.00 - $0.003) down to ($24.00 - $0.0024) on one whole SOL.
	const wantFall = uint64(29_997_000 - 23_997_600)
	if got := runner.Ledger().MaxDrawdownMicros; got != wantFall {
		t.Fatalf("max drawdown = %d micros, want %d", got, wantFall)
	}
}

// The same must hold when the quote fails: the tick was observable, so it
// revalues even though no decision could be opened.
func TestDrawdownSeesTroughsOnAMissedTick(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	runner, primary, secondary, quoter, _ := newTestRunner(t, 30_000_000, now)
	quoter.err = errors.New("pool unavailable")

	if _, err := runner.Step(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	dip := now.Add(10 * time.Second)
	primary.at, secondary.at = dip, dip
	primary.price, secondary.price = 24_000_000, 24_000_000
	tick, err := runner.Step(t.Context(), dip)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventMissed {
		t.Fatalf("expected a missed tick, got %q", tick.Event)
	}
	// The rule never uses the raw price: it uses the conservative one, which
	// deducts each source's confidence. At a 1-in-10,000 confidence that is
	// ($30.00 - $0.003) down to ($24.00 - $0.0024) on one whole SOL.
	const wantFall = uint64(29_997_000 - 23_997_600)
	if got := runner.Ledger().MaxDrawdownMicros; got != wantFall {
		t.Fatalf("max drawdown = %d micros, want %d", got, wantFall)
	}
}
