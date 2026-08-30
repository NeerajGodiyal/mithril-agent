package shadow

import (
	"context"
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

// Record types. They share the swap engine's naming so one journal reader can
// handle both, but nothing here can ever produce a swap.signed or
// swap.submitted record — those stages do not exist in this package.
const (
	EventOpened       = "shadow.opened"
	EventWaiting      = "shadow.waiting"
	EventSignal       = "shadow.signal"
	EventFiltered     = "shadow.filtered"
	EventFilled       = "shadow.filled"
	EventRefused      = "shadow.refused"
	EventMissed       = "shadow.missed"
	EventUnobservable = "shadow.unobservable"
	EventClosed       = "shadow.closed"
)

// UnobservableReason identifies the failed stage without retaining a provider
// error, endpoint, or response body in the shareable journal.
type UnobservableReason string

const (
	ReasonMarketPriceUnavailable     UnobservableReason = "market_price_unavailable"
	ReasonMarketPriceInvalid         UnobservableReason = "market_price_invalid"
	ReasonMarketPriceNotAdvanced     UnobservableReason = "market_price_not_advanced"
	ReasonQuoteCurrencyUnavailable   UnobservableReason = "quote_currency_price_unavailable"
	ReasonQuoteCurrencyInvalid       UnobservableReason = "quote_currency_price_invalid"
	ReasonQuoteCurrencyOutsidePolicy UnobservableReason = "quote_currency_outside_policy"
)

func validUnobservableReason(reason UnobservableReason) bool {
	switch reason {
	case ReasonMarketPriceUnavailable,
		ReasonMarketPriceInvalid,
		ReasonMarketPriceNotAdvanced,
		ReasonQuoteCurrencyUnavailable,
		ReasonQuoteCurrencyInvalid,
		ReasonQuoteCurrencyOutsidePolicy:
		return true
	default:
		return false
	}
}

// PriceReader is one independently identified price source. It is the whole of
// what shadow mode is allowed to do with the network besides asking for a
// quote: read.
type PriceReader interface {
	IdentitySHA256() string
	Latest(ctx context.Context, feed string) (pricetrigger.Sample, error)
}

// Quoter asks a pool what a trade would cost. Read-only by construction: it
// returns amounts, never an instruction or a transaction.
type Quoter interface {
	Quote(ctx context.Context, owner string, sell bool, inputAmount uint64, slippageBPS uint16) (Quote, error)
}

// Recorder is the append-only log. A runner may restore its own accounting and
// direction from a verified history after restart; it never derives a new
// signal from that history.
type Recorder interface {
	Record(at time.Time, eventType string, payload any) error
}

// Counts is what happened, in the categories that matter when judging whether a
// shadow result is trustworthy. Missed and Unobservable are reported rather
// than hidden: a strategy that looks profitable but could only act on half its
// signals has not been shown to work.
type Counts struct {
	Ticks        uint64 `json:"ticks"`
	Signals      uint64 `json:"signals"`
	Filtered     uint64 `json:"filtered"`
	Fills        uint64 `json:"fills"`
	Refused      uint64 `json:"refused"`
	Missed       uint64 `json:"missed"`
	Deferred     uint64 `json:"deferred"`
	Unobservable uint64 `json:"unobservable"`
}

// Stats accumulates the per-decision measurements the report averages. They
// are kept over every settled decision, fills and refusals alike, so the
// denominator a reader sees is the number of decisions actually reached.
type Stats struct {
	Settled          uint64 `json:"settled"`
	SumImpactBPS     int64  `json:"sum_impact_bps"`
	SumSlippageBPS   int64  `json:"sum_slippage_bps"`
	WorstSlippageBPS int32  `json:"worst_slippage_bps"`
}

// MeanImpactBPS is what the pool costs against the reference price.
func (s Stats) MeanImpactBPS() int32 { return mean(s.SumImpactBPS, s.Settled) }

// MeanSlippageBPS is what the settlement delay costs — the latency adjustment,
// measured rather than assumed.
func (s Stats) MeanSlippageBPS() int32 { return mean(s.SumSlippageBPS, s.Settled) }

func mean(total int64, count uint64) int32 {
	if count == 0 {
		return 0
	}
	return int32(total / int64(count))
}

// pending is a decision waiting to be scored. There is at most one, mirroring
// the real engine's one-action-at-a-time discipline: a shadow that can hold ten
// simultaneous positions is not shadowing the thing that would actually run.
type pending struct {
	decidedAt   time.Time
	priceMicros uint64
	quote       Quote
	sell        bool
	riskExit    bool
	settleAfter time.Time
}

// Runner is the shadow loop. It holds no key, no signer, and no submitter,
// because this package defines no such thing.
type Runner struct {
	policy          Policy
	primary         PriceReader
	secondary       PriceReader
	quotePrimary    PriceReader
	quoteSecondary  PriceReader
	nativePrimary   PriceReader
	nativeSecondary PriceReader
	quoter          Quoter
	recorder        Recorder

	ledger   Ledger
	started  bool
	opened   bool
	counts   Counts
	stats    Stats
	waiting  *pending
	strategy *adaptiveStrategy
	decision *AdaptiveDecision
	// resumePending means the prior process recorded a decision but not its
	// settlement. The recorded deadline decides whether it can still settle or
	// must become missed on the first fresh observation.
	resumePending         bool
	nextSell              bool
	nextAmount            uint64
	quoteLowerMicros      uint64
	quoteUpperMicros      uint64
	primaryPublishedAt    time.Time
	secondaryPublishedAt  time.Time
	primarySample         pricetrigger.Sample
	secondarySample       pricetrigger.Sample
	nativePrimarySample   pricetrigger.Sample
	nativeSecondarySample pricetrigger.Sample
}

// Tick is the outcome of one observation, for a caller that wants to print
// progress without reading the journal back.
type Tick struct {
	At          time.Time `json:"at"`
	Event       string    `json:"event"`
	PriceMicros uint64    `json:"price_micros,omitempty"`
	Triggered   bool      `json:"triggered"`
	// Decision explains an adaptive observation and is absent for fixed price
	// policies. Replay recomputes it from prior prices rather than trusting it.
	Decision *AdaptiveDecision `json:"decision,omitempty"`
	// Source publication times make a rolling adaptive observation distinct and
	// replayable. Repeated polls of one still-fresh sample must not manufacture
	// a full strategy window.
	PrimaryPrice         *pricetrigger.Sample `json:"primary_price,omitempty"`
	SecondaryPrice       *pricetrigger.Sample `json:"secondary_price,omitempty"`
	NativeFeePriceMicros uint64               `json:"native_fee_price_micros,omitempty"`
	NativeFeePrimary     *pricetrigger.Sample `json:"native_fee_primary,omitempty"`
	NativeFeeSecondary   *pricetrigger.Sample `json:"native_fee_secondary,omitempty"`
	// Deferred marks a signal that arrived while a decision was already in
	// flight. Without it the journal cannot distinguish a deferred signal from
	// an acted one, and the report is no longer re-derivable from the record.
	Deferred bool `json:"deferred,omitempty"`
	// DecisionQuote is present only on a new signal. It binds replay of the
	// later fill to the exact quote already recorded with the decision.
	DecisionQuote *Quote `json:"decision_quote,omitempty"`
	Fill          *Fill  `json:"fill,omitempty"`
	EquityMicros  uint64 `json:"equity_micros,omitempty"`
	// QuoteLowerMicros and QuoteUpperMicros are the widest independently
	// supported USDC/USD interval on a Mainnet observation. USD accounting is
	// valid only while this whole interval remains inside policy.
	QuoteLowerMicros uint64 `json:"quote_lower_micros,omitempty"`
	QuoteUpperMicros uint64 `json:"quote_upper_micros,omitempty"`
	// DecisionMissed records that an in-flight decision became impossible to
	// score while the market was unobservable. It carries no invented price.
	DecisionMissed bool `json:"decision_missed,omitempty"`
	// Reason is a fixed, non-sensitive classification. Raw provider failures
	// are never written to stdout or the journal because they may contain an
	// endpoint, credential, or response payload.
	Reason UnobservableReason `json:"reason,omitempty"`
	// PeriodClose commits a clean stop or UTC boundary to the hash chain. It is
	// not a market observation and replay must not count it as one.
	PeriodClose bool `json:"period_close,omitempty"`
}

func NewRunner(
	policy Policy,
	primary, secondary PriceReader,
	quoter Quoter,
	recorder Recorder,
	quoteReaders ...PriceReader,
) (*Runner, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if primary == nil || secondary == nil || quoter == nil || recorder == nil {
		return nil, errors.New("shadow runner needs two price sources, a quoter, and a recorder")
	}
	if primary.IdentitySHA256() == secondary.IdentitySHA256() {
		return nil, errors.New("shadow price sources must be independent")
	}
	if primary.IdentitySHA256() != policy.Trigger.PrimarySourceSHA256 ||
		secondary.IdentitySHA256() != policy.Trigger.SecondarySourceSHA256 {
		return nil, errors.New("shadow price sources do not match the policy")
	}
	var quotePrimary, quoteSecondary, nativePrimary, nativeSecondary PriceReader
	nextReader := 0
	if policy.QuotePeg != nil {
		if len(quoteReaders) < 2 || quoteReaders[0] == nil || quoteReaders[1] == nil {
			return nil, errors.New("mainnet shadow runner needs two USDC/USD sources")
		}
		quotePrimary, quoteSecondary = quoteReaders[0], quoteReaders[1]
		nextReader = 2
		if quotePrimary.IdentitySHA256() == quoteSecondary.IdentitySHA256() {
			return nil, errors.New("shadow USDC/USD sources must be independent")
		}
		if quotePrimary.IdentitySHA256() != policy.QuotePeg.PrimarySourceSHA256 ||
			quoteSecondary.IdentitySHA256() != policy.QuotePeg.SecondarySourceSHA256 {
			return nil, errors.New("shadow USDC/USD sources do not match the policy")
		}
	}
	if policy.NativeFeePrice != nil {
		if len(quoteReaders) != nextReader+2 || quoteReaders[nextReader] == nil ||
			quoteReaders[nextReader+1] == nil {
			return nil, errors.New("non-SOL shadow runner needs two native SOL/USD sources")
		}
		nativePrimary, nativeSecondary = quoteReaders[nextReader], quoteReaders[nextReader+1]
		if nativePrimary.IdentitySHA256() == nativeSecondary.IdentitySHA256() ||
			nativePrimary.IdentitySHA256() != policy.NativeFeePrice.PrimarySourceSHA256 ||
			nativeSecondary.IdentitySHA256() != policy.NativeFeePrice.SecondarySourceSHA256 {
			return nil, errors.New("shadow native SOL/USD sources do not match the policy")
		}
		nextReader += 2
	}
	if len(quoteReaders) != nextReader {
		return nil, errors.New("devnet shadow runner does not accept a USDC/USD guard")
	}
	strategy, err := newAdaptiveStrategy(policy.Adaptive)
	if err != nil {
		return nil, err
	}
	return &Runner{
		policy: policy, primary: primary, secondary: secondary,
		quotePrimary: quotePrimary, quoteSecondary: quoteSecondary,
		nativePrimary: nativePrimary, nativeSecondary: nativeSecondary,
		quoter: quoter, recorder: recorder,
		nextSell: policy.IsSell(), nextAmount: policy.InputAmount,
		strategy: strategy,
	}, nil
}

// ResumeRunner restores the decision state that can be proven from a verified
// journal. An in-flight decision remains pending until its recorded deadline;
// after that deadline the first fresh observation records it as missed.
func ResumeRunner(
	policy Policy,
	primary, secondary PriceReader,
	quoter Quoter,
	recorder Recorder,
	ticks []Tick,
	quoteReaders ...PriceReader,
) (*Runner, error) {
	runner, err := NewRunner(policy, primary, secondary, quoter, recorder, quoteReaders...)
	if err != nil {
		return nil, err
	}
	runner.started = true
	if len(ticks) == 0 {
		return runner, nil
	}

	observable := false
	for _, tick := range ticks {
		if tick.Event != EventUnobservable && tick.Event != EventClosed {
			observable = true
			break
		}
	}
	if !observable {
		var lastAt time.Time
		var observations uint64
		for _, tick := range ticks {
			if tick.At.IsZero() || !lastAt.IsZero() && tick.At.Before(lastAt) {
				return nil, errors.New("shadow ticks are not in chronological order")
			}
			if tick.Event == EventClosed {
				if !tick.PeriodClose || tick.PriceMicros != 0 || tick.EquityMicros != 0 ||
					tick.Triggered || tick.Deferred || tick.DecisionQuote != nil ||
					tick.Decision != nil || tick.Fill != nil || tick.DecisionMissed || tick.Reason != "" ||
					tick.NativeFeePriceMicros != 0 || tick.NativeFeePrimary != nil || tick.NativeFeeSecondary != nil ||
					tick.QuoteLowerMicros != 0 || tick.QuoteUpperMicros != 0 {
					return nil, errors.New("a period-close record is malformed")
				}
				lastAt = tick.At
				continue
			}
			if err := validateUnobservableTick(tick); err != nil {
				return nil, err
			}
			if tick.DecisionMissed {
				return nil, errors.New("an unobservable tick missed no pending decision")
			}
			observations++
			lastAt = tick.At
		}
		runner.counts.Ticks = observations
		runner.counts.Unobservable = observations
		return runner, nil
	}

	replayed, err := Replay(policy, ticks)
	if err != nil {
		return nil, err
	}
	runner.ledger = replayed.Ledger
	runner.opened = true
	runner.counts = replayed.Counts
	runner.stats = replayed.Stats
	runner.strategy = replayed.strategy
	runner.primaryPublishedAt = replayed.primaryPublishedAt
	runner.secondaryPublishedAt = replayed.secondaryPublishedAt

	if policy.RoundTrip() {
		runner.nextSell, runner.nextAmount = replayed.nextSell, replayed.nextAmount
	}

	if replayed.pending != nil {
		restored := replayed.pending
		runner.waiting = &pending{
			decidedAt:   restored.settleAfter.Add(-policy.Settle()),
			priceMicros: restored.price, quote: restored.quote,
			sell: restored.sell, riskExit: restored.riskExit,
			settleAfter: restored.settleAfter,
		}
		runner.resumePending = true
	}
	return runner, nil
}

// Counts reports what has happened so far.
func (r *Runner) Counts() Counts { return r.counts }

// Ledger reports the books as they currently stand.
func (r *Runner) Ledger() Ledger { return r.ledger }

// Stats reports the per-decision measurements gathered so far.
func (r *Runner) Stats() Stats { return r.stats }

// RiskHalted reports the adaptive strategy's latched daily risk stop. It stays
// true across unobservable ticks, whose Tick intentionally carries no decision.
func (r *Runner) RiskHalted() bool {
	return r.strategy != nil && r.strategy.riskHalted
}

// NextSell reports the direction the next accepted signal would paper-trade.
// It exposes no action surface; callers use it only to label an alert emitted
// after Step has already committed the decision to the journal.
func (r *Runner) NextSell() bool { return r.nextSell }

// Observation is an opaque, read-only market snapshot. Separating acquisition
// from application lets the production loop establish the event time after
// every source returns, while still checking a UTC rollover before mutating or
// journaling the old day's runner.
type Observation struct {
	primary, secondary             pricetrigger.Sample
	quotePrimary, quoteSecondary   pricetrigger.Sample
	nativePrimary, nativeSecondary pricetrigger.Sample
	unavailable                    UnobservableReason
}

// Observe reads every source needed for one Step without changing runner
// state. Provider errors are reduced to a bounded reason for the journal.
func (r *Runner) Observe(ctx context.Context) Observation {
	primary, secondary, err := r.read(ctx)
	if err != nil {
		return Observation{unavailable: ReasonMarketPriceUnavailable}
	}
	observation := Observation{primary: primary, secondary: secondary}
	if r.policy.QuotePeg == nil {
		return observation
	}
	quotePrimary, quoteSecondary, err := r.readQuotePeg(ctx)
	if err != nil {
		observation.unavailable = ReasonQuoteCurrencyUnavailable
		return observation
	}
	observation.quotePrimary, observation.quoteSecondary = quotePrimary, quoteSecondary
	if r.policy.NativeFeePrice != nil {
		nativePrimary, nativeSecondary, err := r.readNativeFeePrice(ctx)
		if err != nil {
			observation.unavailable = ReasonMarketPriceUnavailable
			return observation
		}
		observation.nativePrimary, observation.nativeSecondary = nativePrimary, nativeSecondary
	}
	return observation
}

// Step performs one observation at a fixed time. Tests and replay callers can
// run a week of market in a millisecond; production uses Observe followed by
// StepObservation with a fresh post-read time.
func (r *Runner) Step(ctx context.Context, now time.Time) (Tick, error) {
	return r.StepObservation(ctx, now, r.Observe(ctx))
}

// StepObservation applies an acquired snapshot at one authoritative event
// time. It performs no price-source reads.
func (r *Runner) StepObservation(
	ctx context.Context, now time.Time, observation Observation,
) (Tick, error) {
	now = now.UTC()
	if now.IsZero() {
		return Tick{}, errors.New("shadow observation time is required")
	}
	if !r.started {
		fingerprint, err := r.policy.Fingerprint()
		if err != nil {
			return Tick{}, err
		}
		if err := r.recorder.Record(now, EventOpened, Opening{
			Version: JournalVersionFor(r.policy), PolicySHA256: fingerprint,
		}); err != nil {
			return Tick{}, err
		}
		r.started = true
	}
	r.counts.Ticks++
	r.quoteLowerMicros, r.quoteUpperMicros = 0, 0
	r.decision = nil

	if observation.unavailable != "" {
		return r.emitUnobservable(now, observation.unavailable)
	}
	if r.policy.QuotePeg != nil {
		quoteEvidence, quoteErr := pricetrigger.EvaluateBand(
			*r.policy.QuotePeg, observation.quotePrimary, observation.quoteSecondary, now,
		)
		if quoteErr != nil {
			return r.emitUnobservable(now, ReasonQuoteCurrencyInvalid)
		}
		if !quoteEvidence.InBand {
			return r.emitUnobservable(now, ReasonQuoteCurrencyOutsidePolicy)
		}
		r.quoteLowerMicros = quoteEvidence.LowerMicros
		r.quoteUpperMicros = quoteEvidence.UpperMicros
	}
	evidence, err := pricetrigger.Evaluate(
		r.activeTrigger(), observation.primary, observation.secondary, now,
	)
	if err != nil {
		return r.emitUnobservable(now, ReasonMarketPriceInvalid)
	}
	if r.strategy != nil {
		primaryAt := observation.primary.PublishedAt.UTC()
		secondaryAt := observation.secondary.PublishedAt.UTC()
		if !adaptiveSampleAdvances(
			r.primaryPublishedAt, r.secondaryPublishedAt, primaryAt, secondaryAt,
		) {
			return r.emitUnobservable(now, ReasonMarketPriceNotAdvanced)
		}
		r.primaryPublishedAt, r.secondaryPublishedAt = primaryAt, secondaryAt
	}
	r.primarySample, r.secondarySample = observation.primary, observation.secondary
	price := evidence.ConservativePrice
	nativePrice := price
	if r.policy.NativeFeePrice != nil {
		nativeEvidence, nativeErr := pricetrigger.Evaluate(
			*r.policy.NativeFeePrice, observation.nativePrimary, observation.nativeSecondary, now,
		)
		if nativeErr != nil || nativeEvidence.ConservativePrice > r.policy.NativeFeePriceCeilingMicros {
			return r.emitUnobservable(now, ReasonMarketPriceInvalid)
		}
		nativePrice = nativeEvidence.ConservativePrice
		r.nativePrimarySample, r.nativeSecondarySample = observation.nativePrimary, observation.nativeSecondary
	}

	if !r.opened {
		if r.ledger, err = NewLedger(r.policy, price, nativePrice); err != nil {
			return Tick{}, err
		}
		r.opened = true
	}

	// Revalue once, here, before any branch. Marking inside the branches meant a
	// trough that happened while a decision was pending — or on a tick whose
	// quote failed — never reached the high-water mark, so the reported worst
	// fall could only ever understate the real one.
	if r.ledger, err = r.ledger.Mark(price, nativePrice); err != nil {
		return Tick{}, err
	}
	triggered := evidence.Triggered
	if r.strategy != nil {
		decision, adaptiveTriggered, decisionErr := r.strategy.decide(
			now, price, r.nextSell, r.ledger,
		)
		if decisionErr != nil {
			return Tick{}, decisionErr
		}
		r.decision = &decision
		triggered = adaptiveTriggered
	}

	if r.resumePending && r.waiting != nil && !now.After(r.waiting.settleAfter) {
		r.resumePending = false
	} else if r.resumePending {
		r.resumePending = false
		r.waiting = nil
		r.counts.Missed++
		if triggered {
			r.counts.Signals++
			r.counts.Deferred++
		}
		return r.emit(now, Tick{
			At: now, Event: EventMissed, PriceMicros: price,
			Triggered: triggered, Deferred: triggered,
		}, nil)
	}

	if settled, done, err := r.settle(ctx, now, price, triggered); err != nil {
		return Tick{}, err
	} else if done {
		return settled, nil
	}

	if !triggered {
		return r.emit(now, Tick{At: now, Event: EventWaiting, PriceMicros: price}, nil)
	}

	r.counts.Signals++
	if r.waiting != nil {
		// A signal while a decision is still settling is not a missed
		// opportunity; it is the same opportunity, still in flight.
		r.counts.Deferred++
		return r.emit(now, Tick{
			At: now, Event: EventSignal, PriceMicros: price,
			Triggered: true, Deferred: true,
		}, nil)
	}
	attemptAmount, feeReserve := paperAttempt(
		r.policy, r.ledger, r.nextSell, r.nextAmount, r.decision,
	)
	if !canFundAttempt(r.ledger, r.nextSell, attemptAmount, feeReserve) {
		r.counts.Missed++
		return r.emit(now, Tick{
			At: now, Event: EventMissed, PriceMicros: price, Triggered: true,
		}, nil)
	}

	quote, err := r.quoter.Quote(
		ctx, r.policy.Observe, r.nextSell, attemptAmount, r.policy.SlippageBPS,
	)
	if err != nil {
		// The rule fired and the trade could not be priced. A real run would
		// have missed it too, so it is counted, not quietly dropped.
		r.counts.Missed++
		return r.emit(now, Tick{At: now, Event: EventMissed, PriceMicros: price, Triggered: true}, nil)
	}
	if validateQuote(quote) != nil || quote.InputAmount != attemptAmount {
		// A quote for a larger amount silently changes the strategy's size; one
		// for a smaller amount silently flatters its fill quality. Either is not
		// the decision this run was asked to measure.
		r.counts.Missed++
		return r.emit(now, Tick{At: now, Event: EventMissed, PriceMicros: price, Triggered: true}, nil)
	}
	receivedAt := quote.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = now
	} else if receivedAt.Before(now) {
		r.counts.Missed++
		return r.emit(now, Tick{At: now, Event: EventMissed, PriceMicros: price, Triggered: true}, nil)
	}
	quote.ReceivedAt = receivedAt
	passes, guardErr := adaptiveQuotePasses(
		r.policy, r.decision, quote, price, r.nextSell,
	)
	if guardErr != nil {
		r.counts.Missed++
		return r.emit(now, Tick{
			At: now, Event: EventMissed, PriceMicros: price, Triggered: true,
		}, nil)
	}
	if !passes {
		r.counts.Filtered++
		decisionQuote := quote
		return r.emit(now, Tick{
			At: now, Event: EventFiltered, PriceMicros: price, Triggered: true,
			DecisionQuote: &decisionQuote,
		}, nil)
	}
	r.waiting = &pending{
		decidedAt: now, priceMicros: price, quote: quote, sell: r.nextSell,
		riskExit:    r.decision != nil && r.decision.Strategy == StrategyRiskExit,
		settleAfter: receivedAt.Add(r.policy.Settle()),
	}
	decisionQuote := quote
	return r.emit(now, Tick{
		At: now, Event: EventSignal, PriceMicros: price, Triggered: true,
		DecisionQuote: &decisionQuote,
	}, nil)
}

func (r *Runner) emitUnobservable(now time.Time, reason UnobservableReason) (Tick, error) {
	r.counts.Unobservable++
	missed := r.waiting != nil && !now.Before(r.waiting.settleAfter)
	if missed {
		r.resumePending = false
		r.waiting = nil
		r.counts.Missed++
	} else if r.resumePending {
		// Reaching the runner before the deadline proves the restored decision
		// is live again, even if this particular market poll is unavailable.
		r.resumePending = false
	}
	return r.emit(now, Tick{
		At: now, Event: EventUnobservable, DecisionMissed: missed, Reason: reason,
	}, nil)
}

// ClosePeriod commits a clean stop or day rollover to the journal. It accounts
// for an unsettled decision without inventing a market observation or fill.
func (r *Runner) ClosePeriod(now time.Time, lastPrice uint64) error {
	if !r.started {
		return nil
	}
	if r.waiting == nil && !r.resumePending {
		_, err := r.emit(now, Tick{
			At: now, Event: EventClosed, PeriodClose: true,
		}, nil)
		return err
	}
	if !r.opened || lastPrice == 0 {
		return errors.New("cannot close a pending shadow decision without an observed price")
	}
	r.waiting = nil
	r.resumePending = false
	r.counts.Missed++
	_, err := r.emit(now, Tick{
		At: now, Event: EventMissed, PriceMicros: lastPrice, PeriodClose: true,
	}, nil)
	return err
}

// settle scores a decision once enough time has passed, against a price that
// was observed strictly after the decision was made.
//
// A tick that settles is spent settling, so a signal arriving on the same tick
// is still counted — as a deferred one. Not counting it would shrink the
// denominator the report divides by and quietly flatter how much of the market
// the strategy could actually act on.
func (r *Runner) settle(
	ctx context.Context, now time.Time, price uint64, triggered bool,
) (Tick, bool, error) {
	if r.waiting == nil || now.Before(r.waiting.settleAfter) {
		return Tick{}, false, nil
	}
	decision := *r.waiting
	r.waiting = nil
	if triggered {
		r.counts.Signals++
		r.counts.Deferred++
	}

	settlementQuote, err := r.quoter.Quote(
		ctx, r.policy.Observe, decision.sell,
		decision.quote.InputAmount, r.policy.SlippageBPS,
	)
	if err == nil && settlementQuote.InputAmount != decision.quote.InputAmount {
		err = errors.New("settlement quote changed the requested input amount")
	}
	settlementReceivedAt := settlementQuote.ReceivedAt
	if settlementReceivedAt.IsZero() {
		settlementReceivedAt = now
	}
	settlementQuote.ReceivedAt = settlementReceivedAt
	if err == nil && (settlementReceivedAt.Before(now) ||
		settlementReceivedAt.Before(decision.settleAfter)) {
		err = errors.New("settlement quote predates the settlement observation or deadline")
	}
	if err == nil && r.policy.Adaptive != nil {
		if validateQuote(settlementQuote) != nil {
			err = errors.New("settlement quote is invalid")
		} else if _, bounded, guardErr := adaptiveQuoteImpact(
			r.policy, settlementQuote, price, decision.sell,
		); guardErr != nil {
			err = guardErr
		} else if !bounded {
			err = errors.New("settlement quote is outside the adaptive market bound")
		}
	}
	if err != nil {
		r.counts.Missed++
		tick, emitErr := r.emit(now, Tick{
			At: now, Event: EventMissed, PriceMicros: price,
			Triggered: triggered, Deferred: triggered,
		}, nil)
		return tick, true, emitErr
	}

	fill, err := SettleRequotedFillDirected(
		r.policy, decision.quote, settlementQuote,
		decision.priceMicros, price, decision.sell,
	)
	if err != nil {
		r.counts.Missed++
		tick, emitErr := r.emit(now, Tick{
			At: now, Event: EventMissed, PriceMicros: price,
			Triggered: triggered, Deferred: triggered,
		}, nil)
		return tick, true, emitErr
	}
	var nativePrice []uint64
	if r.policy.NativeFeePrice != nil {
		nativePrice = []uint64{r.ledger.NativeFeePriceMicros}
	}
	updated, err := r.ledger.Apply(fill, price, nativePrice...)
	if err != nil {
		// The books refused the trade — most often because the inventory ran
		// out. That is a real constraint a live run would also have hit.
		r.counts.Missed++
		tick, emitErr := r.emit(now, Tick{
			At: now, Event: EventMissed, PriceMicros: price,
			Triggered: triggered, Deferred: triggered,
		}, nil)
		return tick, true, emitErr
	}
	r.ledger = updated
	if fill.Filled && r.policy.RoundTrip() {
		r.nextSell = !decision.sell
		r.nextAmount = fill.ReceivedUnits
		// A sell following a buy leaves native fees for itself and the next buy,
		// so the repeating round trip cannot strand its return leg.
		if r.nextSell {
			reserve := nextSellFeeReserve(r.policy)
			if r.ledger, err = r.ledger.replenishFeeReserve(
				reserve,
			); err != nil {
				return Tick{}, false, err
			}
			r.nextAmount = capSellAmount(r.nextAmount, r.ledger, reserve)
		}
		if r.strategy != nil {
			r.strategy.filled(now, decision.riskExit)
		}
	}

	r.stats.Settled++
	r.stats.SumImpactBPS += int64(fill.ImpactBPS)
	r.stats.SumSlippageBPS += int64(fill.SlippageBPS)
	if fill.SlippageBPS < r.stats.WorstSlippageBPS {
		r.stats.WorstSlippageBPS = fill.SlippageBPS
	}

	event := EventRefused
	if fill.Filled {
		event = EventFilled
		r.counts.Fills++
	} else {
		r.counts.Refused++
	}
	// Triggered records whether the RULE fired on this tick, not whether a
	// decision settled on it. Hardcoding true here made the journal claim a
	// signal on quiet ticks, so the record no longer matched the run.
	tick, emitErr := r.emit(now, Tick{
		At: now, Event: event, PriceMicros: price,
		Triggered: triggered, Deferred: triggered,
	}, &fill)
	return tick, true, emitErr
}

func (r *Runner) activeTrigger() pricetrigger.Policy {
	return triggerFor(r.policy, r.nextSell)
}

func (r *Runner) read(ctx context.Context) (pricetrigger.Sample, pricetrigger.Sample, error) {
	primary, err := r.primary.Latest(ctx, r.policy.Trigger.Feed)
	if err != nil {
		return pricetrigger.Sample{}, pricetrigger.Sample{}, err
	}
	secondary, err := r.secondary.Latest(ctx, r.policy.Trigger.Feed)
	if err != nil {
		return pricetrigger.Sample{}, pricetrigger.Sample{}, err
	}
	return primary, secondary, nil
}

func (r *Runner) readQuotePeg(ctx context.Context) (pricetrigger.Sample, pricetrigger.Sample, error) {
	primary, err := r.quotePrimary.Latest(ctx, r.policy.QuotePeg.Feed)
	if err != nil {
		return pricetrigger.Sample{}, pricetrigger.Sample{}, err
	}
	secondary, err := r.quoteSecondary.Latest(ctx, r.policy.QuotePeg.Feed)
	if err != nil {
		return pricetrigger.Sample{}, pricetrigger.Sample{}, err
	}
	return primary, secondary, nil
}

func (r *Runner) readNativeFeePrice(ctx context.Context) (pricetrigger.Sample, pricetrigger.Sample, error) {
	primary, err := r.nativePrimary.Latest(ctx, r.policy.NativeFeePrice.Feed)
	if err != nil {
		return pricetrigger.Sample{}, pricetrigger.Sample{}, err
	}
	secondary, err := r.nativeSecondary.Latest(ctx, r.policy.NativeFeePrice.Feed)
	if err != nil {
		return pricetrigger.Sample{}, pricetrigger.Sample{}, err
	}
	return primary, secondary, nil
}

// emit records the tick and returns it, so every outcome reaches the journal by
// exactly one path.
func (r *Runner) emit(now time.Time, tick Tick, fill *Fill) (Tick, error) {
	tick.Fill = fill
	if r.strategy != nil && tick.PriceMicros != 0 && !tick.PeriodClose {
		tick.Decision = r.decision
	}
	if tick.PriceMicros != 0 && !tick.PeriodClose {
		primary, secondary := r.primarySample, r.secondarySample
		if !primary.PublishedAt.IsZero() && !secondary.PublishedAt.IsZero() {
			tick.PrimaryPrice, tick.SecondaryPrice = &primary, &secondary
		}
	}
	if r.policy.NativeFeePrice != nil && tick.PriceMicros != 0 && !tick.PeriodClose {
		primary, secondary := r.nativePrimarySample, r.nativeSecondarySample
		if !primary.PublishedAt.IsZero() && !secondary.PublishedAt.IsZero() {
			tick.NativeFeePriceMicros = r.ledger.NativeFeePriceMicros
			tick.NativeFeePrimary, tick.NativeFeeSecondary = &primary, &secondary
		}
	}
	if r.policy.QuotePeg != nil && !tick.PeriodClose && tick.Event != EventUnobservable {
		tick.QuoteLowerMicros = r.quoteLowerMicros
		tick.QuoteUpperMicros = r.quoteUpperMicros
	}
	if r.opened && tick.PriceMicros != 0 {
		equity, err := r.ledger.EquityMicros(tick.PriceMicros)
		if err != nil {
			return Tick{}, err
		}
		tick.EquityMicros = equity
	}
	if err := r.recorder.Record(now, tick.Event, tick); err != nil {
		return Tick{}, err
	}
	return tick, nil
}

func adaptiveSampleAdvances(previousPrimary, previousSecondary, primary, secondary time.Time) bool {
	if primary.IsZero() || secondary.IsZero() ||
		!previousPrimary.IsZero() && primary.Before(previousPrimary) ||
		!previousSecondary.IsZero() && secondary.Before(previousSecondary) {
		return false
	}
	return previousPrimary.IsZero() || previousSecondary.IsZero() ||
		primary.After(previousPrimary) || secondary.After(previousSecondary)
}
