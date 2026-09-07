package shadow

import (
	"errors"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

// AccountedDecision is a quote opportunity, not authorization or a fill.
type AccountedDecision struct {
	Decision      AdaptiveDecision
	Sell          bool
	InputAmount   uint64
	ReadyForQuote bool
}

// AccountedStrategy is in-memory continuation over caller-verified outcomes.
// It performs no IO or authorization. Its caller must durably bind observations,
// decisions and exact finalized outcomes; paper settlement deadlines never clear
// actual pending work. Instances are not safe for concurrent use.
type AccountedStrategy struct {
	policy                 Policy
	ledger                 Ledger
	strategy               adaptiveStrategy
	pending                *pending
	nextSell               bool
	nextAmount             uint64
	at                     time.Time
	price                  uint64
	primaryAt, secondaryAt time.Time
	ready                  AccountedDecision
	outcomes               map[string]accountedStrategyOutcome
}

type accountedStrategyOutcome struct {
	outcome AccountedOutcome
	at      time.Time
}

// NewAccountedStrategy preserves a full first-action replay, never a sliced
// history or an opening reconstructed from simulated proceeds.
func NewAccountedStrategy(policy Policy, ticks []Tick) (*AccountedStrategy, error) {
	if err := policy.ValidateForRun(); err != nil {
		return nil, err
	}
	if policy.Adaptive == nil || policy.Cluster != Mainnet ||
		(policy.Market != "" && policy.Market != MarketSOLUSDC) ||
		baseDecimalsFor(policy) != 9 || quoteDecimalsFor(policy) != 6 ||
		policy.NativeFeePrice != nil || policy.StartingFeeReserveLamports != 0 || policy.OneTimeSetupRentLamports != 0 || len(ticks) == 0 {
		return nil, errors.New("accounted strategy requires adaptive unsplit SOL/USDC first-action history")
	}
	for i, tick := range ticks {
		if tick.Fill != nil || tick.DecisionMissed || tick.Event == EventMissed || (i < len(ticks)-1 && tick.Event == EventSignal) {
			return nil, errors.New("accounted strategy cannot seed from earlier actions or simulated fills")
		}
	}
	last := ticks[len(ticks)-1]
	if last.Event != EventSignal || !last.Triggered || last.Deferred || last.DecisionQuote == nil {
		return nil, errors.New("accounted strategy requires an exact pending first signal")
	}
	policy = copyAccountedPolicy(policy)
	replayed, err := Replay(policy, ticks)
	if err != nil {
		return nil, err
	}
	if replayed.pending == nil || replayed.strategy == nil {
		return nil, errors.New("accounted strategy replay lacks pending adaptive state")
	}
	p := replayed.pending
	return &AccountedStrategy{policy: policy, ledger: replayed.Ledger, strategy: *replayed.strategy,
		pending:  &pending{decidedAt: last.At, priceMicros: p.price, quote: p.quote, sell: p.sell, riskExit: p.riskExit, settleAfter: p.settleAfter},
		nextSell: p.sell, nextAmount: p.amount, at: last.At, price: last.PriceMicros,
		primaryAt: replayed.primaryPublishedAt, secondaryAt: replayed.secondaryPublishedAt}, nil
}

// Ledger returns detached books; their opening and risk history are unchanged.
func (s *AccountedStrategy) Ledger() Ledger {
	l := s.ledger
	l.Policy = copyAccountedPolicy(l.Policy)
	return l
}

// Pending reports unresolved actual work, independent of paper deadlines.
func (s *AccountedStrategy) Pending() bool { return s.pending != nil }

// PendingQuote returns the exact unresolved decision quote, without authority.
func (s *AccountedStrategy) PendingQuote() (Quote, bool) {
	if s.pending == nil {
		return Quote{}, false
	}
	return s.pending.quote, true
}

// NextSell reports the direction retained from actual outcomes.
func (s *AccountedStrategy) NextSell() bool { return s.nextSell }

// RiskHalted reports the persistent adaptive risk latch.
func (s *AccountedStrategy) RiskHalted() bool { return s.strategy.riskHalted }

// ApplyOutcome accepts an exact caller-verified terminal at its knowledge time.
// Failures charge their actual fee without advancing direction or cooldown.
func (s *AccountedStrategy) ApplyOutcome(outcome AccountedOutcome, at time.Time) error {
	if len(outcome.AccountingSHA256) != 64 || strings.Trim(outcome.AccountingSHA256, "0123456789abcdef") != "" {
		return errors.New("accounted outcome requires an exact accounting digest")
	}
	if prior, ok := s.outcomes[outcome.AccountingSHA256]; ok {
		if prior.outcome != outcome || !prior.at.Equal(at) {
			return errors.New("accounted outcome digest was reused with different evidence")
		}
		return nil
	}
	if s.pending == nil || at.IsZero() || at.Before(s.at) || at.Before(s.pending.quote.ReceivedAt) ||
		outcome.Sell != s.pending.sell || (outcome.Success && (outcome.SpentUnits != s.pending.quote.InputAmount || outcome.ReceivedUnits < s.pending.quote.MinimumOutput)) {
		return errors.New("accounted outcome does not match pending decision or chronology")
	}
	ledger, err := s.ledger.ApplyAccounted(outcome, s.price)
	if err != nil {
		return err
	}
	next := *s
	next.ledger, next.at, next.pending, next.ready = ledger, at.UTC(), nil, AccountedDecision{}
	if outcome.Success {
		next.strategy.filled(at, s.pending.riskExit)
		if s.policy.RoundTrip() {
			next.nextSell, next.nextAmount = !outcome.Sell, outcome.ReceivedUnits
		}
	}
	if next.outcomes == nil {
		next.outcomes = make(map[string]accountedStrategyOutcome)
	}
	next.outcomes[outcome.AccountingSHA256] = accountedStrategyOutcome{outcome: outcome, at: at.UTC()}
	*s = next
	return nil
}

// Observe advances validated market history even while an outcome is pending.
// Errors leave all state unchanged; readiness never grants trading authority.
func (s *AccountedStrategy) Observe(at time.Time, primary, secondary, quotePrimary, quoteSecondary pricetrigger.Sample) (AccountedDecision, error) {
	if at.IsZero() || !at.After(s.at) {
		return AccountedDecision{}, errors.New("accounted observation time did not advance")
	}
	if s.policy.QuotePeg != nil {
		band, err := pricetrigger.EvaluateBand(*s.policy.QuotePeg, quotePrimary, quoteSecondary, at)
		if err != nil || !band.InBand {
			return AccountedDecision{}, errors.New("accounted quote currency evidence is invalid")
		}
	}
	evidence, err := pricetrigger.Evaluate(triggerFor(s.policy, s.nextSell), primary, secondary, at)
	if err != nil {
		return AccountedDecision{}, err
	}
	if !AdaptiveSampleAdvances(s.primaryAt, s.secondaryAt, primary.PublishedAt, secondary.PublishedAt) {
		return AccountedDecision{}, errors.New("accounted market samples did not advance")
	}
	next := *s
	next.strategy.prices = append([]uint64(nil), s.strategy.prices...)
	next.ledger, err = s.ledger.Mark(evidence.ConservativePrice)
	if err != nil {
		return AccountedDecision{}, err
	}
	decision, triggered, err := next.strategy.decide(at, evidence.ConservativePrice, s.nextSell, next.ledger)
	if err != nil {
		return AccountedDecision{}, err
	}
	amount, reserve := paperAttempt(s.policy, next.ledger, s.nextSell, s.nextAmount, evidence.ConservativePrice, &decision)
	if s.nextSell {
		if next.ledger.BaseUnits <= reserve {
			amount = 0
		} else {
			amount = min(amount, next.ledger.BaseUnits-reserve)
		}
	} else {
		amount = min(amount, next.ledger.QuoteUnits)
	}
	amount, reserve = paperAttempt(s.policy, next.ledger, s.nextSell, amount, evidence.ConservativePrice, &decision)
	next.ready = AccountedDecision{Decision: decision, Sell: s.nextSell, InputAmount: amount,
		ReadyForQuote: triggered && s.pending == nil && amount != 0 && canFundAttempt(next.ledger, s.nextSell, amount, reserve)}
	next.at, next.price = at.UTC(), evidence.ConservativePrice
	next.primaryAt, next.secondaryAt = primary.PublishedAt.UTC(), secondary.PublishedAt.UTC()
	*s = next
	return next.ready, nil
}

// CommitDecision binds a quote only to the latest ready observation. It creates
// pending in-memory state, not a claim, signature, fill or spending permission.
func (s *AccountedStrategy) CommitDecision(quote Quote, at time.Time) error {
	if s.pending != nil || !s.ready.ReadyForQuote || at.IsZero() || at.Before(s.at) ||
		at.Sub(s.at) > time.Duration(s.policy.Adaptive.MaxObservationGapSeconds)*time.Second ||
		validateQuote(quote) != nil || quote.InputAmount != s.ready.InputAmount || quote.ReceivedAt.IsZero() ||
		quote.ReceivedAt.Before(s.at) || quote.ReceivedAt.After(at) || !quoteMatchesSlippage(s.policy.SlippageBPS, quote) {
		return errors.New("accounted quote differs from last ready observation")
	}
	passes, err := adaptiveQuotePasses(s.policy, &s.ready.Decision, quote, s.price, s.nextSell)
	if err != nil || !passes {
		return errors.New("accounted quote failed the adaptive cost gate")
	}
	s.pending = &pending{decidedAt: s.at, priceMicros: s.price, quote: quote, sell: s.nextSell,
		riskExit: s.ready.Decision.Strategy == StrategyRiskExit}
	s.at, s.ready = at.UTC(), AccountedDecision{}
	return nil
}

// CancelPendingDecision clears only an exact caller-verified unclaimed decision.
// The caller must prove expiry and absence of a claim or reservation; this
// in-memory operation performs no IO and grants no authority. Market history,
// actual books, direction, cooldown and risk state remain unchanged.
func (s *AccountedStrategy) CancelPendingDecision(quote Quote, sell bool, observationAt, at time.Time) error {
	if s.pending == nil || s.pending.quote != quote || s.pending.sell != sell ||
		!s.pending.decidedAt.Equal(observationAt) || at.IsZero() || at.Before(s.at) || at.Before(quote.ReceivedAt) {
		return errors.New("cancellation differs from pending decision or chronology")
	}
	s.pending, s.ready, s.at = nil, AccountedDecision{}, at.UTC()
	return nil
}

func copyAccountedPolicy(p Policy) Policy {
	if p.Adaptive != nil {
		value := *p.Adaptive
		p.Adaptive = &value
	}
	if p.QuotePeg != nil {
		value := *p.QuotePeg
		p.QuotePeg = &value
	}
	if p.ReturnTrigger != nil {
		value := *p.ReturnTrigger
		p.ReturnTrigger = &value
	}
	if p.NativeFeePrice != nil {
		value := *p.NativeFeePrice
		p.NativeFeePrice = &value
	}
	return p
}
