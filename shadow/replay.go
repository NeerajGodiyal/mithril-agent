package shadow

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

// A report you cannot recompute is a report you have to take on trust. Replay
// rebuilds the books from the recorded ticks alone, so a reviewer can derive
// the numbers themselves from the tamper-evident journal instead of believing
// the summary that shipped beside it.
//
// It deliberately does not read the stored opening record: the opening position
// is re-derived from the policy and the first observed price, exactly as the
// live run derived it. If the two disagree, that disagreement is the finding.

// Replayed is everything a report needs, recomputed from the record.
type Replayed struct {
	Ledger       Ledger
	Counts       Counts
	Stats        Stats
	ClosingPrice uint64
	// PeriodEnd is the last hash-chained record time. A clean stop writes an
	// explicit close record, so a report can reproduce its time boundary
	// without trusting the separate summary file.
	PeriodEnd            time.Time
	nextSell             bool
	nextAmount           uint64
	strategy             *adaptiveStrategy
	primaryPublishedAt   time.Time
	secondaryPublishedAt time.Time
	pending              *replayPending
}

type replayPending struct {
	price       uint64
	amount      uint64
	quote       Quote
	sell        bool
	riskExit    bool
	settleAfter time.Time
}

// Replay rebuilds the run from its ticks, in order.
func Replay(policy Policy, ticks []Tick) (Replayed, error) {
	if err := policy.Validate(); err != nil {
		return Replayed{}, err
	}
	if len(ticks) == 0 {
		return Replayed{}, errors.New("no ticks to replay")
	}
	strategy, err := newAdaptiveStrategy(policy.Adaptive)
	if err != nil {
		return Replayed{}, err
	}
	var (
		result               Replayed
		opened               bool
		pending              *replayPending
		nextSell             = policy.IsSell()
		nextAmount           = policy.InputAmount
		lastAt               time.Time
		lastReason           UnobservableReason
		replayErr            error
		primaryPublishedAt   time.Time
		secondaryPublishedAt time.Time
	)
	for _, tick := range ticks {
		if tick.At.IsZero() || !lastAt.IsZero() && tick.At.Before(lastAt) {
			return Replayed{}, errors.New("shadow ticks are not in chronological order")
		}
		lastAt = tick.At
		result.PeriodEnd = tick.At
		if tick.PeriodClose {
			if tick.Triggered || tick.Deferred || tick.Fill != nil || tick.DecisionMissed ||
				tick.DecisionQuote != nil || tick.Decision != nil || tick.Reason != "" ||
				tick.PrimaryPrice != nil || tick.SecondaryPrice != nil ||
				tick.QuoteLowerMicros != 0 || tick.QuoteUpperMicros != 0 {
				return Replayed{}, errors.New("a period-close record is malformed")
			}
			if tick.Event == EventClosed {
				if tick.PriceMicros != 0 || tick.EquityMicros != 0 || pending != nil {
					return Replayed{}, errors.New("a period-close record is malformed")
				}
				continue
			}
			if tick.Event != EventMissed {
				return Replayed{}, errors.New("a period-close record is malformed")
			}
		} else {
			result.Counts.Ticks++
		}
		if tick.Event == EventUnobservable {
			if err := validateUnobservableTick(tick); err != nil {
				return Replayed{}, err
			}
			if tick.Reason != "" {
				lastReason = tick.Reason
			}
			result.Counts.Unobservable++
			if tick.DecisionMissed {
				if pending == nil {
					return Replayed{}, errors.New("an unobservable tick missed no pending decision")
				}
				pending = nil
				result.Counts.Missed++
			} else if pending != nil && !tick.At.Before(pending.settleAfter) {
				return Replayed{}, errors.New("an expired decision remained pending on an unobservable tick")
			}
			continue
		}
		if tick.Reason != "" {
			return Replayed{}, errors.New("an observable tick has an unobservable reason")
		}
		if tick.DecisionMissed {
			return Replayed{}, errors.New("an observable tick has an unobservable missed decision")
		}
		if tick.PriceMicros == 0 {
			// Any other event without a price could not have been produced by a
			// live run, so the record is not one this can rebuild from.
			return Replayed{}, errors.New("a recorded tick is missing its price")
		}
		if tick.PrimaryPrice != nil || tick.SecondaryPrice != nil {
			if tick.PrimaryPrice == nil || tick.SecondaryPrice == nil {
				return Replayed{}, errors.New("a recorded tick has incomplete market source evidence")
			}
			evidence, evidenceErr := pricetrigger.Evaluate(
				triggerFor(policy, nextSell), *tick.PrimaryPrice, *tick.SecondaryPrice, tick.At,
			)
			if evidenceErr != nil || evidence.ConservativePrice != tick.PriceMicros {
				return Replayed{}, errors.New("a recorded tick price does not match its market source evidence")
			}
		}
		if strategy != nil && !tick.PeriodClose {
			if tick.PrimaryPrice == nil || tick.SecondaryPrice == nil ||
				!adaptiveSampleAdvances(
					primaryPublishedAt, secondaryPublishedAt,
					tick.PrimaryPrice.PublishedAt.UTC(), tick.SecondaryPrice.PublishedAt.UTC(),
				) {
				return Replayed{}, errors.New("an adaptive tick has no distinct chronological market sample")
			}
			primaryPublishedAt = tick.PrimaryPrice.PublishedAt.UTC()
			secondaryPublishedAt = tick.SecondaryPrice.PublishedAt.UTC()
		}
		if policy.QuotePeg != nil && !tick.PeriodClose {
			if tick.QuoteLowerMicros < policy.QuotePeg.MinimumMicros ||
				tick.QuoteUpperMicros > policy.QuotePeg.MaximumMicros ||
				tick.QuoteLowerMicros == 0 || tick.QuoteLowerMicros > tick.QuoteUpperMicros {
				return Replayed{}, errors.New("a recorded tick has invalid USDC/USD evidence")
			}
		} else if policy.QuotePeg == nil &&
			(tick.QuoteLowerMicros != 0 || tick.QuoteUpperMicros != 0) {
			return Replayed{}, errors.New("a devnet tick contains mainnet USDC/USD evidence")
		}
		if !opened {
			if result.Ledger, replayErr = NewLedger(policy, tick.PriceMicros); replayErr != nil {
				return Replayed{}, replayErr
			}
			opened = true
		}
		if result.Ledger, replayErr = result.Ledger.Mark(tick.PriceMicros); replayErr != nil {
			return Replayed{}, replayErr
		}
		if !tick.PeriodClose {
			expected := triggerMatches(triggerFor(policy, nextSell), tick.PriceMicros)
			if strategy != nil {
				decision, triggered, decisionErr := strategy.decide(
					tick.At, tick.PriceMicros, nextSell, result.Ledger,
				)
				if decisionErr != nil {
					return Replayed{}, decisionErr
				}
				expected = triggered
				if tick.Decision == nil || !reflect.DeepEqual(*tick.Decision, decision) {
					return Replayed{}, errors.New("a recorded adaptive decision is not supported by prior prices")
				}
			} else if tick.Decision != nil {
				return Replayed{}, errors.New("a fixed price rule contains an adaptive decision")
			}
			if tick.Triggered != expected || tick.Deferred && !tick.Triggered {
				if strategy != nil {
					return Replayed{}, errors.New("a recorded tick does not match its active adaptive strategy")
				}
				return Replayed{}, errors.New("a recorded tick does not match its active price rule")
			}
		}
		result.ClosingPrice = tick.PriceMicros

		if tick.Triggered {
			result.Counts.Signals++
			if tick.Deferred {
				result.Counts.Deferred++
			}
		}
		attemptAmount, feeReserve := paperAttempt(
			policy, result.Ledger, nextSell, nextAmount, tick.Decision,
		)
		switch tick.Event {
		case EventWaiting:
			if tick.Triggered || tick.Deferred || tick.DecisionQuote != nil ||
				tick.Fill != nil || tick.PeriodClose {
				return Replayed{}, errors.New("a waiting tick is malformed")
			}
			if pending != nil && !tick.At.Before(pending.settleAfter) {
				return Replayed{}, errors.New("an expired decision was recorded as waiting")
			}
		case EventSignal:
			if !tick.Triggered || tick.Fill != nil || tick.PeriodClose {
				return Replayed{}, errors.New("a signal tick is malformed")
			}
			if tick.Deferred {
				if tick.DecisionQuote != nil || pending == nil || !tick.At.Before(pending.settleAfter) {
					return Replayed{}, errors.New("a signal was deferred without an active decision")
				}
			} else {
				if pending != nil || tick.DecisionQuote == nil ||
					validateQuote(*tick.DecisionQuote) != nil ||
					tick.DecisionQuote.InputAmount != attemptAmount ||
					!canFundAttempt(result.Ledger, nextSell, attemptAmount, feeReserve) {
					return Replayed{}, errors.New("a new signal has invalid decision quote or state")
				}
				passes, guardErr := adaptiveQuotePasses(
					policy, tick.Decision, *tick.DecisionQuote, tick.PriceMicros, nextSell,
				)
				if guardErr != nil || !passes {
					return Replayed{}, errors.New("a new adaptive signal failed its executable quote gate")
				}
				pending = &replayPending{
					price: tick.PriceMicros, amount: attemptAmount, quote: *tick.DecisionQuote, sell: nextSell,
					riskExit:    tick.Decision != nil && tick.Decision.Strategy == StrategyRiskExit,
					settleAfter: tick.At.Add(policy.Settle()),
				}
			}
		case EventFiltered:
			if policy.Adaptive == nil || pending != nil || !tick.Triggered || tick.Deferred ||
				tick.Fill != nil || tick.PeriodClose || tick.DecisionQuote == nil ||
				validateQuote(*tick.DecisionQuote) != nil ||
				tick.DecisionQuote.InputAmount != attemptAmount ||
				!canFundAttempt(result.Ledger, nextSell, attemptAmount, feeReserve) {
				return Replayed{}, errors.New("a filtered tick has invalid quote or state")
			}
			passes, guardErr := adaptiveQuotePasses(
				policy, tick.Decision, *tick.DecisionQuote, tick.PriceMicros, nextSell,
			)
			if guardErr != nil || passes {
				return Replayed{}, errors.New("a filtered adaptive quote passed its executable quote gate")
			}
			result.Counts.Filtered++
		case EventMissed:
			if tick.DecisionQuote != nil || tick.Fill != nil {
				return Replayed{}, errors.New("a missed tick contains trade evidence")
			}
			result.Counts.Missed++
			if tick.PeriodClose {
				if pending == nil {
					return Replayed{}, errors.New("a period close missed no pending decision")
				}
				pending = nil
			} else if pending != nil {
				if tick.Deferred != tick.Triggered {
					return Replayed{}, errors.New("a pending decision recorded inconsistent deferred state")
				}
				pending = nil
			} else if !tick.Triggered || tick.Deferred {
				return Replayed{}, errors.New("a missed tick has no failed signal or pending decision")
			}
		case EventFilled, EventRefused:
			if pending == nil || tick.At.Before(pending.settleAfter) {
				return Replayed{}, errors.New("a fill settled without a mature pending decision")
			}
			if tick.DecisionQuote != nil || tick.Fill == nil || tick.Deferred != tick.Triggered {
				return Replayed{}, errors.New("a settled tick is malformed")
			}
			if policy.Adaptive != nil {
				_, bounded, guardErr := adaptiveQuoteImpact(
					policy, tick.Fill.SettlementQuote, tick.PriceMicros, pending.sell,
				)
				if guardErr != nil || !bounded {
					return Replayed{}, errors.New("a recorded settlement quote is outside the adaptive market bound")
				}
			}
			recomputed, fillErr := SettleRequotedFillDirected(
				policy, pending.quote, tick.Fill.SettlementQuote,
				pending.price, tick.PriceMicros, pending.sell,
			)
			if fillErr != nil || !reflect.DeepEqual(recomputed, *tick.Fill) ||
				(tick.Event == EventFilled) != tick.Fill.Filled ||
				tick.Fill.DecisionQuote != pending.quote ||
				tick.Fill.DecisionQuote.InputAmount != pending.amount {
				return Replayed{}, errors.New("a recorded fill is not supported by its decision quote")
			}
			if result.Ledger, replayErr = result.Ledger.Apply(*tick.Fill, tick.PriceMicros); replayErr != nil {
				return Replayed{}, replayErr
			}
			result.Stats.Settled++
			result.Stats.SumImpactBPS += int64(tick.Fill.ImpactBPS)
			result.Stats.SumSlippageBPS += int64(tick.Fill.SlippageBPS)
			if tick.Fill.SlippageBPS < result.Stats.WorstSlippageBPS {
				result.Stats.WorstSlippageBPS = tick.Fill.SlippageBPS
			}
			if tick.Fill.Filled {
				result.Counts.Fills++
				if policy.RoundTrip() {
					nextSell = !pending.sell
					nextAmount = tick.Fill.ReceivedUnits
					if nextSell {
						nextAmount = capSellAmount(
							nextAmount, result.Ledger.BaseUnits, roundTripFeeReserve(policy.FeeLamports),
						)
					}
				}
				if strategy != nil {
					strategy.filled(tick.At, pending.riskExit)
				}
			} else {
				result.Counts.Refused++
			}
			pending = nil
		default:
			return Replayed{}, errors.New("the record contains an unrecognised event")
		}
		equity, equityErr := result.Ledger.EquityMicros(tick.PriceMicros)
		if equityErr != nil || tick.EquityMicros != equity {
			return Replayed{}, errors.New("a recorded tick has an unsupported equity value")
		}
	}
	if !opened {
		if lastReason != "" {
			return Replayed{}, errors.New("the record contains no observable tick; latest reason: " + string(lastReason))
		}
		return Replayed{}, errors.New("the record contains no observable tick")
	}
	result.nextSell, result.nextAmount, result.strategy = nextSell, nextAmount, strategy
	result.primaryPublishedAt, result.secondaryPublishedAt = primaryPublishedAt, secondaryPublishedAt
	result.pending = pending
	return result, nil
}

func validateUnobservableTick(tick Tick) error {
	if tick.Event != EventUnobservable {
		return errors.New("the record does not contain an unobservable tick")
	}
	if tick.PriceMicros != 0 || tick.QuoteLowerMicros != 0 || tick.QuoteUpperMicros != 0 ||
		tick.Triggered || tick.Deferred || tick.DecisionQuote != nil || tick.Decision != nil ||
		tick.PrimaryPrice != nil || tick.SecondaryPrice != nil ||
		tick.Fill != nil || tick.EquityMicros != 0 ||
		tick.PeriodClose {
		return errors.New("an unobservable tick contains market evidence")
	}
	// Empty is accepted for journals written before bounded reason codes were
	// added. Any new value must be one produced by the runner.
	if tick.Reason != "" && !validUnobservableReason(tick.Reason) {
		return errors.New("an unobservable tick has an unsupported reason")
	}
	return nil
}

func triggerFor(policy Policy, sell bool) pricetrigger.Policy {
	if !policy.RoundTrip() || sell == policy.IsSell() {
		return policy.Trigger
	}
	return *policy.ReturnTrigger
}

func triggerMatches(trigger pricetrigger.Policy, price uint64) bool {
	if trigger.Direction == pricetrigger.SellAtOrAbove {
		return price >= trigger.ThresholdMicros
	}
	return price <= trigger.ThresholdMicros
}

// Disagreement names one field where a stored report differs from what the
// record actually supports. Values are JSON so time, string, boolean, signed,
// and unsigned fields can all be compared without lossy integer conversion.
type Disagreement struct {
	Field    string          `json:"field"`
	Stored   json.RawMessage `json:"stored"`
	Replayed json.RawMessage `json:"replayed"`
}

// Compare checks a stored report against a recomputed one. It reports every
// difference rather than the first, because a single mismatched field and a
// wholesale fabrication need to be distinguishable at a glance.
func Compare(stored Report, replayed Report) []Disagreement {
	// Report v4 predates the explicit label but already had these reset-daily
	// semantics. Preserve its verifiability while rejecting any unknown mode.
	if stored.EvaluationMode == "" && replayed.EvaluationMode == EvaluationResetDaily {
		stored.EvaluationMode = EvaluationResetDaily
	}
	return compareStruct("", reflect.ValueOf(stored), reflect.ValueOf(replayed))
}

func compareStruct(prefix string, stored, replayed reflect.Value) []Disagreement {
	typeInfo := stored.Type()
	var found []Disagreement
	for index := 0; index < stored.NumField(); index++ {
		fieldInfo := typeInfo.Field(index)
		name, _, _ := strings.Cut(fieldInfo.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			name = fieldInfo.Name
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		left, right := stored.Field(index), replayed.Field(index)
		if left.Kind() == reflect.Struct && left.Type() != reflect.TypeOf(time.Time{}) {
			found = append(found, compareStruct(path, left, right)...)
			continue
		}
		if reflect.DeepEqual(left.Interface(), right.Interface()) {
			continue
		}
		leftJSON, _ := json.Marshal(left.Interface())
		rightJSON, _ := json.Marshal(right.Interface())
		found = append(found, Disagreement{
			Field: path, Stored: leftJSON, Replayed: rightJSON,
		})
	}
	return found
}
