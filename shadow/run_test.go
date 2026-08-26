package shadow

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"math"
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

func (s *stubSource) Latest(_ context.Context, feed string) (pricetrigger.Sample, error) {
	if s.err != nil {
		return pricetrigger.Sample{}, s.err
	}
	return pricetrigger.Sample{
		SourceSHA256: s.identity, Feed: feed,
		PriceMicros: s.price, ConfidenceMicros: s.price / 10_000,
		PublishedAt: s.at,
	}, nil
}

func mainnetPolicy() Policy {
	policy := sellPolicy()
	policy.Cluster = Mainnet
	policy.QuoteRoute = QuoteRoute{
		Provider: QuoteJupiter, InputMint: wrappedSOLMint, OutputMint: mainnetUSDCMint,
	}
	policy.QuotePeg = &pricetrigger.BandPolicy{
		Version: pricetrigger.Version, Feed: pricetrigger.FeedUSDCUSD,
		MinimumMicros: pricetrigger.USDCBandMinimumMicros,
		MaximumMicros: pricetrigger.USDCBandMaximumMicros,
		MaxAgeSeconds: 60, MaxSourceSkewSeconds: 30,
		MaxDeviationBPS: 50, MaxConfidenceBPS: 50,
		PrimarySourceSHA256:   strings.Repeat("d", 64),
		SecondarySourceSHA256: strings.Repeat("e", 64),
	}
	return policy
}

type stubQuoter struct {
	estimated  uint64
	err        error
	calls      int
	directions []bool
	amounts    []uint64
	quote      func(bool, uint64) Quote
}

func (q *stubQuoter) Quote(_ context.Context, _ string, sell bool, amount uint64, _ uint16) (Quote, error) {
	q.calls++
	q.directions = append(q.directions, sell)
	q.amounts = append(q.amounts, amount)
	if q.err != nil {
		return Quote{}, q.err
	}
	if q.quote != nil {
		return q.quote(sell, amount), nil
	}
	return Quote{
		InputAmount: amount, EstimatedOutput: q.estimated,
		MinimumOutput: q.estimated - q.estimated/100,
	}, nil
}

type stubRecorder struct {
	types []string
	ticks []Tick
	err   error
}

func (r *stubRecorder) Record(_ time.Time, eventType string, payload any) error {
	if r.err != nil {
		return r.err
	}
	r.types = append(r.types, eventType)
	if tick, ok := payload.(Tick); ok {
		r.ticks = append(r.ticks, tick)
	}
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

func TestContinuousRunnerAlternatesARoundTripAndReplaysIt(t *testing.T) {
	policy := sellPolicy()
	policy.Trigger.ThresholdMicros = 22_000_000
	buy := policy.Trigger
	buy.Direction = pricetrigger.BuyAtOrBelow
	buy.ThresholdMicros = 20_000_000
	policy.ReturnTrigger = &buy
	now := time.Unix(1_700_000_000, 0).UTC()
	primary := &stubSource{
		identity: policy.Trigger.PrimarySourceSHA256, price: 23_000_000, at: now,
	}
	secondary := &stubSource{
		identity: policy.Trigger.SecondarySourceSHA256, price: 23_000_000, at: now,
	}
	quoter := &stubQuoter{quote: func(sell bool, amount uint64) Quote {
		output := amount * 23_000_000 / 1_000_000_000
		if !sell {
			output = amount * 1_000_000_000 / 19_000_000
		}
		return Quote{InputAmount: amount, EstimatedOutput: output, MinimumOutput: output * 99 / 100}
	}}
	recorder := &stubRecorder{}
	runner, err := NewRunner(policy, primary, secondary, quoter, recorder)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = runner.Step(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	settledSell := now.Add(policy.Settle())
	primary.at, secondary.at = settledSell, settledSell
	if tick, stepErr := runner.Step(t.Context(), settledSell); stepErr != nil {
		t.Fatal(stepErr)
	} else if tick.Fill == nil || !tick.Fill.Sell || !tick.Fill.Filled {
		t.Fatalf("first leg did not settle as a sell: %+v", tick)
	}

	buyAt := settledSell.Add(time.Second)
	primary.at, secondary.at = buyAt, buyAt
	primary.price, secondary.price = 19_000_000, 19_000_000
	if _, err = runner.Step(t.Context(), buyAt); err != nil {
		t.Fatal(err)
	}
	settledBuy := buyAt.Add(policy.Settle())
	primary.at, secondary.at = settledBuy, settledBuy
	if tick, stepErr := runner.Step(t.Context(), settledBuy); stepErr != nil {
		t.Fatal(stepErr)
	} else if tick.Fill == nil || tick.Fill.Sell || !tick.Fill.Filled {
		t.Fatalf("return leg did not settle as a buy: %+v", tick)
	}

	if len(quoter.directions) != 2 || !quoter.directions[0] || quoter.directions[1] {
		t.Fatalf("quote directions = %v, want sell then buy", quoter.directions)
	}
	if quoter.amounts[1] != quoter.quote(true, quoter.amounts[0]).EstimatedOutput {
		t.Fatalf("return leg spent %d, want the first leg's %d output",
			quoter.amounts[1], quoter.quote(true, quoter.amounts[0]).EstimatedOutput)
	}
	replayed, err := Replay(policy, recorder.ticks)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Ledger.BaseUnits != runner.Ledger().BaseUnits ||
		replayed.Ledger.QuoteUnits != runner.Ledger().QuoteUnits ||
		replayed.Ledger.RealizedMicros != runner.Ledger().RealizedMicros {
		t.Fatalf("replayed ledger %+v does not match live ledger %+v",
			replayed.Ledger, runner.Ledger())
	}
}

func TestPolicyFingerprintBindsEveryDecisionField(t *testing.T) {
	policy := sellPolicy()
	first, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	second, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 64 {
		t.Fatalf("fingerprint is not stable SHA-256: %q then %q", first, second)
	}
	policy.Trigger.ThresholdMicros++
	changed, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("changing the trading threshold did not change the policy fingerprint")
	}
}

func TestResumeRestoresRoundTripDirectionAmountAndBooks(t *testing.T) {
	policy := sellPolicy()
	policy.Trigger.ThresholdMicros = 22_000_000
	buy := policy.Trigger
	buy.Direction = pricetrigger.BuyAtOrBelow
	buy.ThresholdMicros = 20_000_000
	policy.ReturnTrigger = &buy
	now := time.Unix(1_700_000_000, 0).UTC()
	primary := &stubSource{
		identity: policy.Trigger.PrimarySourceSHA256, price: 23_000_000, at: now,
	}
	secondary := &stubSource{
		identity: policy.Trigger.SecondarySourceSHA256, price: 23_000_000, at: now,
	}
	quote := func(sell bool, amount uint64) Quote {
		output := amount * 23_000_000 / 1_000_000_000
		if !sell {
			output = amount * 1_000_000_000 / 19_000_000
		}
		return Quote{InputAmount: amount, EstimatedOutput: output, MinimumOutput: output * 99 / 100}
	}
	firstQuoter := &stubQuoter{quote: quote}
	firstRecord := &stubRecorder{}
	first, err := NewRunner(policy, primary, secondary, firstQuoter, firstRecord)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Step(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	settled := now.Add(policy.Settle())
	primary.at, secondary.at = settled, settled
	if tick, err := first.Step(t.Context(), settled); err != nil {
		t.Fatal(err)
	} else if tick.Fill == nil || !tick.Fill.Filled || !tick.Fill.Sell {
		t.Fatalf("sell leg did not fill: %+v", tick)
	}
	wantAmount := firstRecord.ticks[len(firstRecord.ticks)-1].Fill.ReceivedUnits
	wantLedger := first.Ledger()

	restartedAt := settled.Add(time.Second)
	primary.price, secondary.price = 19_000_000, 19_000_000
	primary.at, secondary.at = restartedAt, restartedAt
	secondQuoter := &stubQuoter{quote: quote}
	secondRecord := &stubRecorder{}
	resumed, err := ResumeRunner(
		policy, primary, secondary, secondQuoter, secondRecord, firstRecord.ticks,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := resumed.Ledger(); got.BaseUnits != wantLedger.BaseUnits ||
		got.QuoteUnits != wantLedger.QuoteUnits || got.RealizedMicros != wantLedger.RealizedMicros {
		t.Fatalf("resumed ledger %+v, want %+v", got, wantLedger)
	}
	if tick, err := resumed.Step(t.Context(), restartedAt); err != nil {
		t.Fatal(err)
	} else if tick.Event != EventSignal {
		t.Fatalf("resumed return leg = %q, want signal", tick.Event)
	}
	if len(secondQuoter.directions) != 1 || secondQuoter.directions[0] {
		t.Fatalf("resumed direction = %v, want buy", secondQuoter.directions)
	}
	if secondQuoter.amounts[0] != wantAmount {
		t.Fatalf("resumed amount = %d, want prior leg output %d", secondQuoter.amounts[0], wantAmount)
	}
	if len(secondRecord.types) != 1 || secondRecord.types[0] != EventSignal {
		t.Fatalf("resume wrote a second opening header: %v", secondRecord.types)
	}
}

func TestResumeRecordsAnUnsettledDecisionAsMissedBeforeContinuing(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	first, primary, secondary, _, firstRecord := newTestRunner(t, 23_000_000, now)
	if tick, err := first.Step(t.Context(), now); err != nil {
		t.Fatal(err)
	} else if tick.Event != EventSignal {
		t.Fatalf("first tick = %q, want signal", tick.Event)
	}

	restartedAt := now.Add(time.Second)
	primary.at, secondary.at = restartedAt, restartedAt
	quoter := &stubQuoter{estimated: 21_525}
	record := &stubRecorder{}
	resumed, err := ResumeRunner(
		first.policy, primary, secondary, quoter, record, firstRecord.ticks,
	)
	if err != nil {
		t.Fatal(err)
	}
	tick, err := resumed.Step(t.Context(), restartedAt)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventMissed || !tick.Triggered || !tick.Deferred {
		t.Fatalf("unsettled recovery = %+v, want a deferred missed decision", tick)
	}
	if quoter.calls != 0 {
		t.Fatalf("recovery invented a replacement quote (%d calls)", quoter.calls)
	}
	counts := resumed.Counts()
	if counts.Signals != 2 || counts.Deferred != 1 || counts.Missed != 1 {
		t.Fatalf("recovered counts = %+v", counts)
	}

	next := restartedAt.Add(time.Second)
	primary.at, secondary.at = next, next
	if tick, err = resumed.Step(t.Context(), next); err != nil {
		t.Fatal(err)
	} else if tick.Event != EventSignal || quoter.calls != 1 {
		t.Fatalf("runner did not continue after recovery: tick=%+v calls=%d", tick, quoter.calls)
	}
}

func TestClosePeriodMarksPendingWithoutInventingAnObservation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	runner, primary, secondary, quoter, recorder := newTestRunner(t, 23_000_000, now)
	if _, err := runner.Step(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	price := recorder.ticks[0].PriceMicros
	if err := runner.ClosePeriod(now.Add(time.Minute), price); err != nil {
		t.Fatal(err)
	}
	last := recorder.ticks[len(recorder.ticks)-1]
	if last.Event != EventMissed || !last.PeriodClose {
		t.Fatalf("period close = %+v", last)
	}
	replayed, err := Replay(runner.policy, recorder.ticks)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Counts.Ticks != 1 || replayed.Counts.Signals != 1 || replayed.Counts.Missed != 1 {
		t.Fatalf("period close distorted the observation counts: %+v", replayed.Counts)
	}
	quietEnd := now.Add(2 * time.Minute)
	if err := runner.ClosePeriod(quietEnd, price); err != nil {
		t.Fatal(err)
	}
	last = recorder.ticks[len(recorder.ticks)-1]
	if last.Event != EventClosed || !last.PeriodClose || last.PriceMicros != 0 {
		t.Fatalf("quiet period close = %+v", last)
	}
	replayed, err = Replay(runner.policy, recorder.ticks)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.PeriodEnd.Equal(quietEnd) || replayed.Counts.Ticks != 1 {
		t.Fatalf("quiet close period end = %s counts=%+v", replayed.PeriodEnd, replayed.Counts)
	}
	next := quietEnd.Add(time.Second)
	primary.at, secondary.at = next, next
	resumed, err := ResumeRunner(
		runner.policy, primary, secondary, quoter, &stubRecorder{}, recorder.ticks,
	)
	if err != nil {
		t.Fatal(err)
	}
	if tick, err := resumed.Step(t.Context(), next); err != nil || tick.Event != EventSignal {
		t.Fatalf("runner did not resume after a clean close: tick=%+v err=%v", tick, err)
	}
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

func TestAQuoteCannotChangeTheConfiguredTradeSize(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	runner, _, _, quoter, _ := newTestRunner(t, 23_000_000, now)
	quoter.quote = func(_ bool, amount uint64) Quote {
		return Quote{
			InputAmount: amount + 1, EstimatedOutput: 21_525, MinimumOutput: 21_310,
		}
	}

	tick, err := runner.Step(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventMissed || runner.Counts().Missed != 1 {
		t.Fatalf("a resized quote produced %+v with counts %+v", tick, runner.Counts())
	}
	if runner.waiting != nil {
		t.Fatal("a resized quote became a pending decision")
	}
}

// An unreadable market must never be scored. It is recorded as unobservable so
// the report can say how much of the period it could not see.
func TestAnUnreadableMarketIsNeverScored(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	runner, primary, secondary, quoter, recorder := newTestRunner(t, 23_000_000, now)
	primary.err = errors.New("endpoint refused the connection")

	tick, err := runner.Step(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventUnobservable || tick.PriceMicros != 0 ||
		tick.Reason != ReasonMarketPriceUnavailable {
		t.Fatalf("an unreadable market produced %+v", tick)
	}
	if runner.Counts().Unobservable != 1 || quoter.calls != 0 {
		t.Fatalf("counts = %+v, quotes = %d", runner.Counts(), quoter.calls)
	}
	if _, err := Replay(runner.policy, recorder.ticks); err == nil ||
		!strings.Contains(err.Error(), string(ReasonMarketPriceUnavailable)) {
		t.Fatalf("the safe failure reason was not available to report: %v", err)
	}
	tampered := append([]Tick(nil), recorder.ticks...)
	tampered[0].Reason = UnobservableReason("endpoint-with-sensitive-detail")
	if _, err := ResumeRunner(
		runner.policy, primary, secondary, quoter, recorder, tampered,
	); err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("a tampered failure reason was accepted or echoed: %v", err)
	}
}

func TestInvalidMarketEvidenceHasABoundedReason(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	runner, _, secondary, _, _ := newTestRunner(t, 23_000_000, now)
	secondary.price = 20_000_000

	tick, err := runner.Step(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventUnobservable || tick.Reason != ReasonMarketPriceInvalid {
		t.Fatalf("invalid market evidence produced %+v", tick)
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

func TestMainnetRunnerRequiresAndBindsUSDCUSDSources(t *testing.T) {
	policy := mainnetPolicy()
	primary := &stubSource{identity: policy.Trigger.PrimarySourceSHA256}
	secondary := &stubSource{identity: policy.Trigger.SecondarySourceSHA256}
	quoter := &stubQuoter{estimated: 21_525}
	recorder := &stubRecorder{}
	if _, err := NewRunner(policy, primary, secondary, quoter, recorder); err == nil {
		t.Fatal("mainnet runner accepted no USDC/USD evidence")
	}
	quotePrimary := &stubSource{identity: policy.QuotePeg.PrimarySourceSHA256}
	wrong := &stubSource{identity: strings.Repeat("f", 64)}
	if _, err := NewRunner(
		policy, primary, secondary, quoter, recorder, quotePrimary, wrong,
	); err == nil {
		t.Fatal("mainnet runner accepted the wrong USDC/USD source")
	}
	quoteSecondary := &stubSource{identity: policy.QuotePeg.SecondarySourceSHA256}
	if _, err := NewRunner(
		policy, primary, secondary, quoter, recorder, quotePrimary, quoteSecondary,
	); err != nil {
		t.Fatalf("mainnet runner rejected its bound USDC/USD sources: %v", err)
	}
}

func TestMainnetRunnerFailsClosedOnUSDCDepegAndRecordsItsBound(t *testing.T) {
	now := time.Now().UTC()
	policy := mainnetPolicy()
	policy.Trigger.ThresholdMicros = 22_000_000
	primary := &stubSource{
		identity: policy.Trigger.PrimarySourceSHA256, price: 23_000_000, at: now,
	}
	secondary := &stubSource{
		identity: policy.Trigger.SecondarySourceSHA256, price: 23_000_000, at: now,
	}
	quotePrimary := &stubSource{
		identity: policy.QuotePeg.PrimarySourceSHA256, price: 980_000, at: now,
	}
	quoteSecondary := &stubSource{
		identity: policy.QuotePeg.SecondarySourceSHA256, price: 980_000, at: now,
	}
	quoter := &stubQuoter{estimated: 21_525}
	recorder := &stubRecorder{}
	runner, err := NewRunner(
		policy, primary, secondary, quoter, recorder, quotePrimary, quoteSecondary,
	)
	if err != nil {
		t.Fatal(err)
	}
	tick, err := runner.Step(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventUnobservable || tick.PriceMicros != 0 || quoter.calls != 0 ||
		tick.Reason != ReasonQuoteCurrencyOutsidePolicy {
		t.Fatalf("depeg was not a fail-closed tick: %+v, quotes=%d", tick, quoter.calls)
	}

	quotePrimary.price, quoteSecondary.price = 1_000_000, 1_000_000
	now = now.Add(time.Second)
	primary.at, secondary.at, quotePrimary.at, quoteSecondary.at = now, now, now, now
	tick, err = runner.Step(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventSignal || tick.QuoteLowerMicros < policy.QuotePeg.MinimumMicros ||
		tick.QuoteUpperMicros > policy.QuotePeg.MaximumMicros {
		t.Fatalf("healthy peg evidence was not journalled: %+v", tick)
	}
	if _, err := Replay(policy, recorder.ticks); err != nil {
		t.Fatalf("honest peg evidence did not replay: %v", err)
	}
	tampered := append([]Tick(nil), recorder.ticks...)
	tampered[len(tampered)-1].QuoteLowerMicros = policy.QuotePeg.MinimumMicros - 1
	if _, err := Replay(policy, tampered); err == nil {
		t.Fatal("journaled out-of-band peg evidence replayed")
	}
}

func TestUnavailableQuoteCurrencyEvidenceHasABoundedReason(t *testing.T) {
	now := time.Now().UTC()
	policy := mainnetPolicy()
	primary := &stubSource{
		identity: policy.Trigger.PrimarySourceSHA256, price: 23_000_000, at: now,
	}
	secondary := &stubSource{
		identity: policy.Trigger.SecondarySourceSHA256, price: 23_000_000, at: now,
	}
	quotePrimary := &stubSource{
		identity: policy.QuotePeg.PrimarySourceSHA256, err: errors.New("provider payload"),
	}
	quoteSecondary := &stubSource{
		identity: policy.QuotePeg.SecondarySourceSHA256, price: 1_000_000, at: now,
	}
	runner, err := NewRunner(
		policy, primary, secondary, &stubQuoter{estimated: 21_525}, &stubRecorder{},
		quotePrimary, quoteSecondary,
	)
	if err != nil {
		t.Fatal(err)
	}
	tick, err := runner.Step(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventUnobservable || tick.Reason != ReasonQuoteCurrencyUnavailable ||
		strings.Contains(string(tick.Reason), "provider") {
		t.Fatalf("unavailable quote-currency evidence produced %+v", tick)
	}
}

func TestUnobservableSettlementBecomesOneMissedDecision(t *testing.T) {
	now := time.Now().UTC()
	runner, primary, secondary, quoter, recorder := newTestRunner(t, 23_000_000, now)
	if tick, err := runner.Step(t.Context(), now); err != nil || tick.Event != EventSignal {
		t.Fatalf("opening decision = %+v, %v", tick, err)
	}
	settleAt := now.Add(runner.policy.Settle())
	primary.err = errors.New("price source unavailable")
	secondary.at = settleAt
	tick, err := runner.Step(t.Context(), settleAt)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventUnobservable || !tick.DecisionMissed || runner.waiting != nil {
		t.Fatalf("unobservable settlement = %+v, waiting=%+v", tick, runner.waiting)
	}
	if runner.Counts().Missed != 1 || runner.Counts().Unobservable != 1 || quoter.calls != 1 {
		t.Fatalf("counts=%+v quotes=%d", runner.Counts(), quoter.calls)
	}
	replayed, err := Replay(runner.policy, recorder.ticks)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Counts.Missed != 1 || replayed.Counts.Unobservable != 1 {
		t.Fatalf("replayed counts = %+v", replayed.Counts)
	}

	// A later recovery cannot invent the expired fill or count it twice.
	primary.err = nil
	later := settleAt.Add(time.Minute)
	primary.at, secondary.at = later, later
	if _, err := runner.Step(t.Context(), later); err != nil {
		t.Fatal(err)
	}
	if runner.Counts().Missed != 1 {
		t.Fatalf("expired decision was counted twice: %+v", runner.Counts())
	}
}

func TestSettlementErrorKeepsConcurrentSignalCountsReplayable(t *testing.T) {
	now := time.Now().UTC()
	runner, primary, secondary, quoter, recorder := newTestRunner(t, 23_000_000, now)
	quoter.quote = func(bool, uint64) Quote {
		return Quote{
			InputAmount:     runner.policy.InputAmount,
			EstimatedOutput: math.MaxUint64,
			MinimumOutput:   math.MaxUint64 - 1,
		}
	}
	if tick, err := runner.Step(t.Context(), now); err != nil || tick.Event != EventSignal {
		t.Fatalf("opening decision = %+v, %v", tick, err)
	}
	settleAt := now.Add(runner.policy.Settle())
	primary.at, secondary.at = settleAt, settleAt
	tick, err := runner.Step(t.Context(), settleAt)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Event != EventMissed || !tick.Triggered || !tick.Deferred {
		t.Fatalf("failed settlement lost its concurrent signal: %+v", tick)
	}
	replayed, err := Replay(runner.policy, recorder.ticks)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Counts != runner.Counts() {
		t.Fatalf("replayed counts %+v, live counts %+v", replayed.Counts, runner.Counts())
	}
}

func TestReplayRejectsUnsupportedFillEvidence(t *testing.T) {
	now := time.Now().UTC()
	runner, primary, secondary, _, recorder := newTestRunner(t, 23_000_000, now)
	if _, err := runner.Step(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	settleAt := now.Add(runner.policy.Settle())
	primary.at, secondary.at = settleAt, settleAt
	if _, err := runner.Step(t.Context(), settleAt); err != nil {
		t.Fatal(err)
	}
	if len(recorder.ticks) != 2 || recorder.ticks[1].Fill == nil {
		t.Fatalf("test did not produce signal and fill: %+v", recorder.ticks)
	}

	t.Run("fill without decision", func(t *testing.T) {
		if _, err := Replay(runner.policy, recorder.ticks[1:]); err == nil {
			t.Fatal("a fill without a prior decision replayed")
		}
	})
	t.Run("rewritten decision quote", func(t *testing.T) {
		tampered := append([]Tick(nil), recorder.ticks...)
		altered := *tampered[1].Fill
		altered.DecisionQuote.EstimatedOutput++
		tampered[1].Fill = &altered
		if _, err := Replay(runner.policy, tampered); err == nil {
			t.Fatal("a fill derived from a rewritten quote replayed")
		}
	})
	t.Run("reversed time", func(t *testing.T) {
		tampered := append([]Tick(nil), recorder.ticks...)
		tampered[1].At = tampered[0].At.Add(-time.Nanosecond)
		if _, err := Replay(runner.policy, tampered); err == nil {
			t.Fatal("out-of-order shadow evidence replayed")
		}
	})
}

func TestResumeDoesNotRestoreAnUnobservablyMissedDecision(t *testing.T) {
	now := time.Now().UTC()
	runner, primary, secondary, quoter, recorder := newTestRunner(t, 23_000_000, now)
	if tick, err := runner.Step(t.Context(), now); err != nil || tick.Event != EventSignal {
		t.Fatalf("opening decision = %+v, %v", tick, err)
	}
	settleAt := now.Add(runner.policy.Settle())
	primary.err = errors.New("price source unavailable")
	secondary.at = settleAt
	if tick, err := runner.Step(t.Context(), settleAt); err != nil ||
		tick.Event != EventUnobservable || !tick.DecisionMissed {
		t.Fatalf("unobservable settlement = %+v, %v", tick, err)
	}

	primary.err = nil
	later := settleAt.Add(time.Minute)
	primary.at, secondary.at = later, later
	resumed, err := ResumeRunner(
		runner.policy, primary, secondary, quoter, recorder, recorder.ticks,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.resumePending {
		t.Fatal("a decision already recorded as missed was restored as pending")
	}
	if _, err := resumed.Step(t.Context(), later); err != nil {
		t.Fatal(err)
	}
	if resumed.Counts().Missed != 1 {
		t.Fatalf("resumed runner counted the missed decision twice: %+v", resumed.Counts())
	}
}

func TestPolicyRequiresPegEvidenceOnlyForMainnet(t *testing.T) {
	mainnet := sellPolicy()
	mainnet.Cluster = Mainnet
	if err := mainnet.Validate(); err == nil {
		t.Fatal("mainnet policy accepted no USDC/USD evidence")
	}
	devnet := sellPolicy()
	peg := mainnetPolicy().QuotePeg
	devnet.QuotePeg = peg
	if err := devnet.Validate(); err == nil {
		t.Fatal("devnet policy claimed a USDC/USD guard for its test token")
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
