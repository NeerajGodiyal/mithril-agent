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
	ReasonQuoteCurrencyUnavailable   UnobservableReason = "quote_currency_price_unavailable"
	ReasonQuoteCurrencyInvalid       UnobservableReason = "quote_currency_price_invalid"
	ReasonQuoteCurrencyOutsidePolicy UnobservableReason = "quote_currency_outside_policy"
)

func validUnobservableReason(reason UnobservableReason) bool {
	switch reason {
	case ReasonMarketPriceUnavailable,
		ReasonMarketPriceInvalid,
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
	settleAfter time.Time
}

// Runner is the shadow loop. It holds no key, no signer, and no submitter,
// because this package defines no such thing.
type Runner struct {
	policy         Policy
	primary        PriceReader
	secondary      PriceReader
	quotePrimary   PriceReader
	quoteSecondary PriceReader
	quoter         Quoter
	recorder       Recorder

	ledger  Ledger
	started bool
	opened  bool
	counts  Counts
	stats   Stats
	waiting *pending
	// resumePending means the prior process recorded a decision but not its
	// settlement. The quote is intentionally not persisted, so the first fresh
	// observation records that decision as missed instead of inventing a fill.
	resumePending    bool
	nextSell         bool
	nextAmount       uint64
	quoteLowerMicros uint64
	quoteUpperMicros uint64
}

// Tick is the outcome of one observation, for a caller that wants to print
// progress without reading the journal back.
type Tick struct {
	At          time.Time `json:"at"`
	Event       string    `json:"event"`
	PriceMicros uint64    `json:"price_micros,omitempty"`
	Triggered   bool      `json:"triggered"`
	// Deferred marks a signal that arrived while a decision was already in
	// flight. Without it the journal cannot distinguish a deferred signal from
	// an acted one, and the report is no longer re-derivable from the record.
	Deferred     bool   `json:"deferred,omitempty"`
	Fill         *Fill  `json:"fill,omitempty"`
	EquityMicros uint64 `json:"equity_micros,omitempty"`
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
	var quotePrimary, quoteSecondary PriceReader
	if policy.QuotePeg != nil {
		if len(quoteReaders) != 2 || quoteReaders[0] == nil || quoteReaders[1] == nil {
			return nil, errors.New("mainnet shadow runner needs two USDC/USD sources")
		}
		quotePrimary, quoteSecondary = quoteReaders[0], quoteReaders[1]
		if quotePrimary.IdentitySHA256() == quoteSecondary.IdentitySHA256() {
			return nil, errors.New("shadow USDC/USD sources must be independent")
		}
		if quotePrimary.IdentitySHA256() != policy.QuotePeg.PrimarySourceSHA256 ||
			quoteSecondary.IdentitySHA256() != policy.QuotePeg.SecondarySourceSHA256 {
			return nil, errors.New("shadow USDC/USD sources do not match the policy")
		}
	} else if len(quoteReaders) != 0 {
		return nil, errors.New("devnet shadow runner does not accept a USDC/USD guard")
	}
	return &Runner{
		policy: policy, primary: primary, secondary: secondary,
		quotePrimary: quotePrimary, quoteSecondary: quoteSecondary,
		quoter: quoter, recorder: recorder,
		nextSell: policy.IsSell(), nextAmount: policy.InputAmount,
	}, nil
}

// ResumeRunner restores the decision state that can be proven from a verified
// journal. An in-flight quote cannot be reconstructed honestly because quotes
// are intentionally not stored, so it becomes one missed decision on the first
// fresh observation.
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
					tick.Triggered || tick.Deferred || tick.Fill != nil || tick.DecisionMissed ||
					tick.Reason != "" || tick.QuoteLowerMicros != 0 || tick.QuoteUpperMicros != 0 {
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

	if policy.RoundTrip() {
		for index := len(ticks) - 1; index >= 0; index-- {
			fill := ticks[index].Fill
			if ticks[index].Event != EventFilled || fill == nil || !fill.Filled {
				continue
			}
			runner.nextSell = !fill.Sell
			runner.nextAmount = fill.ReceivedUnits
			if runner.nextSell && runner.nextAmount > runner.ledger.BaseUnits {
				runner.nextAmount = runner.ledger.BaseUnits
			}
			break
		}
	}

	for _, tick := range ticks {
		switch tick.Event {
		case EventSignal:
			if tick.Triggered && !tick.Deferred {
				runner.resumePending = true
			}
		case EventFilled, EventRefused, EventMissed:
			runner.resumePending = false
		case EventUnobservable:
			if tick.DecisionMissed {
				runner.resumePending = false
			}
		}
	}
	return runner, nil
}

// Counts reports what has happened so far.
func (r *Runner) Counts() Counts { return r.counts }

// Ledger reports the books as they currently stand.
func (r *Runner) Ledger() Ledger { return r.ledger }

// Stats reports the per-decision measurements gathered so far.
func (r *Runner) Stats() Stats { return r.stats }

// Step performs one observation. It never blocks on a wall clock: the caller
// supplies the time, so a test can run a week of market in a millisecond and
// the production loop is the only thing that has to know about sleeping.
func (r *Runner) Step(ctx context.Context, now time.Time) (Tick, error) {
	if !r.started {
		fingerprint, err := r.policy.Fingerprint()
		if err != nil {
			return Tick{}, err
		}
		if err := r.recorder.Record(now, EventOpened, Opening{
			Version: JournalVersion, PolicySHA256: fingerprint,
		}); err != nil {
			return Tick{}, err
		}
		r.started = true
	}
	r.counts.Ticks++
	r.quoteLowerMicros, r.quoteUpperMicros = 0, 0

	primary, secondary, err := r.read(ctx)
	if err != nil {
		return r.emitUnobservable(now, ReasonMarketPriceUnavailable)
	}
	if r.policy.QuotePeg != nil {
		quotePrimary, quoteSecondary, quoteErr := r.readQuotePeg(ctx)
		if quoteErr != nil {
			return r.emitUnobservable(now, ReasonQuoteCurrencyUnavailable)
		}
		quoteEvidence, quoteErr := pricetrigger.EvaluateBand(
			*r.policy.QuotePeg, quotePrimary, quoteSecondary, now,
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
	evidence, err := pricetrigger.Evaluate(r.activeTrigger(), primary, secondary, now)
	if err != nil {
		return r.emitUnobservable(now, ReasonMarketPriceInvalid)
	}
	price := evidence.ConservativePrice

	if !r.opened {
		if r.ledger, err = NewLedger(r.policy, price); err != nil {
			return Tick{}, err
		}
		r.opened = true
	}

	// Revalue once, here, before any branch. Marking inside the branches meant a
	// trough that happened while a decision was pending — or on a tick whose
	// quote failed — never reached the high-water mark, so the reported worst
	// fall could only ever understate the real one.
	if r.ledger, err = r.ledger.Mark(price); err != nil {
		return Tick{}, err
	}

	if r.resumePending {
		r.resumePending = false
		r.counts.Missed++
		if evidence.Triggered {
			r.counts.Signals++
			r.counts.Deferred++
		}
		return r.emit(now, Tick{
			At: now, Event: EventMissed, PriceMicros: price,
			Triggered: evidence.Triggered, Deferred: evidence.Triggered,
		}, nil)
	}

	if settled, done, err := r.settle(now, price, evidence.Triggered); err != nil {
		return Tick{}, err
	} else if done {
		return settled, nil
	}

	if !evidence.Triggered {
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

	quote, err := r.quoter.Quote(
		ctx, r.policy.Observe, r.nextSell, r.nextAmount, r.policy.SlippageBPS,
	)
	if err != nil {
		// The rule fired and the trade could not be priced. A real run would
		// have missed it too, so it is counted, not quietly dropped.
		r.counts.Missed++
		return r.emit(now, Tick{At: now, Event: EventMissed, PriceMicros: price, Triggered: true}, nil)
	}
	if quote.InputAmount != r.nextAmount {
		// A quote for a larger amount silently changes the strategy's size; one
		// for a smaller amount silently flatters its fill quality. Either is not
		// the decision this run was asked to measure.
		r.counts.Missed++
		return r.emit(now, Tick{At: now, Event: EventMissed, PriceMicros: price, Triggered: true}, nil)
	}
	r.waiting = &pending{
		decidedAt: now, priceMicros: price, quote: quote, sell: r.nextSell,
		settleAfter: now.Add(r.policy.Settle()),
	}
	return r.emit(now, Tick{At: now, Event: EventSignal, PriceMicros: price, Triggered: true}, nil)
}

func (r *Runner) emitUnobservable(now time.Time, reason UnobservableReason) (Tick, error) {
	r.counts.Unobservable++
	missed := r.resumePending || r.waiting != nil && !now.Before(r.waiting.settleAfter)
	if missed {
		r.resumePending = false
		r.waiting = nil
		r.counts.Missed++
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
func (r *Runner) settle(now time.Time, price uint64, triggered bool) (Tick, bool, error) {
	if r.waiting == nil || now.Before(r.waiting.settleAfter) {
		return Tick{}, false, nil
	}
	decision := *r.waiting
	r.waiting = nil
	if triggered {
		r.counts.Signals++
		r.counts.Deferred++
	}

	fill, err := SettleFillDirected(
		r.policy, decision.quote, decision.priceMicros, price, decision.sell,
	)
	if err != nil {
		r.counts.Missed++
		tick, emitErr := r.emit(now, Tick{
			At: now, Event: EventMissed, PriceMicros: price,
			Triggered: triggered, Deferred: triggered,
		}, nil)
		return tick, true, emitErr
	}
	updated, err := r.ledger.Apply(fill, price)
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
		// The native fee leaves the base side of the book. When the next leg
		// sells base, cap it to what is actually left so a completed buy can
		// always hand over to a valid sell instead of failing by exactly one fee.
		if r.nextSell && r.nextAmount > r.ledger.BaseUnits {
			r.nextAmount = r.ledger.BaseUnits
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

// emit records the tick and returns it, so every outcome reaches the journal by
// exactly one path.
func (r *Runner) emit(now time.Time, tick Tick, fill *Fill) (Tick, error) {
	tick.Fill = fill
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
