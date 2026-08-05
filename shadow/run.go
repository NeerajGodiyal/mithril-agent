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
)

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
	Quote(ctx context.Context, owner string, inputAmount uint64, slippageBPS uint16) (Quote, error)
}

// Recorder is the append-only log. Shadow never reads its own history back to
// make a decision, so a compromised or truncated log can distort the report but
// cannot change what the strategy does.
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
	settleAfter time.Time
}

// Runner is the shadow loop. It holds no key, no signer, and no submitter,
// because this package defines no such thing.
type Runner struct {
	policy    Policy
	primary   PriceReader
	secondary PriceReader
	quoter    Quoter
	recorder  Recorder

	ledger  Ledger
	opened  bool
	counts  Counts
	stats   Stats
	waiting *pending
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
}

func NewRunner(
	policy Policy,
	primary, secondary PriceReader,
	quoter Quoter,
	recorder Recorder,
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
	return &Runner{
		policy: policy, primary: primary, secondary: secondary,
		quoter: quoter, recorder: recorder,
	}, nil
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
	r.counts.Ticks++

	primary, secondary, err := r.read(ctx)
	if err != nil {
		r.counts.Unobservable++
		// The reason is deliberately not journalled: a source error can carry an
		// endpoint or a credential, and this file is meant to be shareable.
		return r.emit(now, Tick{At: now, Event: EventUnobservable}, nil)
	}
	evidence, err := pricetrigger.Evaluate(r.policy.Trigger, primary, secondary, now)
	if err != nil {
		r.counts.Unobservable++
		return r.emit(now, Tick{At: now, Event: EventUnobservable}, nil)
	}
	price := evidence.ConservativePrice

	if !r.opened {
		if r.ledger, err = NewLedger(r.policy, price); err != nil {
			return Tick{}, err
		}
		r.opened = true
		if err := r.recorder.Record(now, EventOpened, r.ledger); err != nil {
			return Tick{}, err
		}
	}

	// Revalue once, here, before any branch. Marking inside the branches meant a
	// trough that happened while a decision was pending — or on a tick whose
	// quote failed — never reached the high-water mark, so the reported worst
	// fall could only ever understate the real one.
	if r.ledger, err = r.ledger.Mark(price); err != nil {
		return Tick{}, err
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

	quote, err := r.quoter.Quote(ctx, r.policy.Observe, r.policy.InputAmount, r.policy.SlippageBPS)
	if err != nil {
		// The rule fired and the trade could not be priced. A real run would
		// have missed it too, so it is counted, not quietly dropped.
		r.counts.Missed++
		return r.emit(now, Tick{At: now, Event: EventMissed, PriceMicros: price, Triggered: true}, nil)
	}
	r.waiting = &pending{
		decidedAt: now, priceMicros: price, quote: quote,
		settleAfter: now.Add(r.policy.Settle()),
	}
	return r.emit(now, Tick{At: now, Event: EventSignal, PriceMicros: price, Triggered: true}, nil)
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

	fill, err := SettleFill(r.policy, decision.quote, decision.priceMicros, price)
	if err != nil {
		r.counts.Missed++
		tick, emitErr := r.emit(now, Tick{At: now, Event: EventMissed, PriceMicros: price}, nil)
		return tick, true, emitErr
	}
	updated, err := r.ledger.Apply(fill, price)
	if err != nil {
		// The books refused the trade — most often because the inventory ran
		// out. That is a real constraint a live run would also have hit.
		r.counts.Missed++
		tick, emitErr := r.emit(now, Tick{At: now, Event: EventMissed, PriceMicros: price}, nil)
		return tick, true, emitErr
	}
	r.ledger = updated

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

// emit records the tick and returns it, so every outcome reaches the journal by
// exactly one path.
func (r *Runner) emit(now time.Time, tick Tick, fill *Fill) (Tick, error) {
	tick.Fill = fill
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
