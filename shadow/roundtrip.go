package shadow

import (
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

// A one-directional shadow run answers "was selling at this price good", which
// is only half the question. The other half — "and could I buy back low enough
// for the round trip to clear its own costs" — cannot be answered by running
// the two legs separately, because a round trip is not the sum of two
// independent decisions: the second leg spends exactly what the first produced,
// and the spread plus two fees is paid out of one book.
//
// ReplayRoundTrip is that missing half. It reuses the same Ledger, the same
// fill accounting and the same report, so a round-trip result is directly
// comparable with a one-directional one and with the hold benchmark.
//
// Direction alternates rather than being chosen per tick. The first leg is
// whichever direction the policy's primary trigger names, and each leg that
// actually fills hands over to the other — so the two rules can never both fire
// on one tick, and the run cannot sell twice in a row against a book that only
// funded one round trip.
//
// Inferring it from inventory instead looks tempting and is wrong: a book
// holding two lots still holds the asset after selling one, so "holding the
// asset means sell" would sell again and never buy back.

// RoundTripCounts is what happened, in the terms that matter for judging
// whether the rule is worth running at all.
type RoundTripCounts struct {
	Ticks uint64 `json:"ticks"`
	// Sells and Buys are completed legs. A round trip is one of each; an odd
	// total means the run ended holding the wrong asset, which the closing
	// mark-to-market accounts for but which is worth seeing.
	Sells uint64 `json:"sells"`
	Buys  uint64 `json:"buys"`
	// Refused counts legs the slippage floor stopped. High refusal with high
	// signal means the thresholds are reachable but the pool cannot fill them.
	Refused uint64 `json:"refused"`
	// Filtered counts signals deliberately rejected by their executable-quote
	// gate. They are valid no-trades, not failed observations or executions.
	Filtered uint64 `json:"filtered"`
	// Missed counts a decision whose settlement time arrived without an
	// observable market price, matching the live runner's fail-closed behavior.
	Missed uint64 `json:"missed"`
	// SellSignals and BuySignals are ticks where the active rule fired. Comparing
	// them with completed legs and refusals shows whether the route could act on
	// the prices the strategy selected.
	SellSignals uint64 `json:"sell_signals"`
	BuySignals  uint64 `json:"buy_signals"`
}

// RoundTripResult is the recomputed run, ready for BuildReport.
type RoundTripResult struct {
	Ledger       Ledger
	Counts       RoundTripCounts
	ClosingPrice uint64
}

// ReplayRoundTrip scores a price series against both legs on one book.
//
// quoteFor supplies the pool's answer at a given price, direction, and exact
// input amount. It is a parameter rather than a field so a caller can drive
// this from recorded live quotes, from a constant spread, or from a model — the
// accounting is identical and the honesty of the result is entirely a property
// of what this returns.
func ReplayRoundTrip(
	policy Policy,
	prices []uint64,
	quoteFor func(priceMicros uint64, sell bool, inputAmount uint64) (Quote, error),
) (RoundTripResult, error) {
	if err := policy.Validate(); err != nil {
		return RoundTripResult{}, err
	}
	if policy.NativeFeePrice != nil {
		return RoundTripResult{}, errors.New("non-SOL round-trip replay needs journaled native fee-price evidence")
	}
	observations := make([]roundTripObservation, len(prices))
	at := time.Unix(0, 0).UTC()
	for index, price := range prices {
		if index != 0 {
			at = at.Add(policy.Tick())
		}
		observations[index] = roundTripObservation{at: at, priceMicros: price, observable: true}
	}
	return replayRoundTrip(policy, observations, quoteFor)
}

// ReplayRoundTripTicks uses the journal's actual observation times. This is
// the production backtest/search path: missing market reads must not shorten a
// configured settlement delay.
func ReplayRoundTripTicks(
	policy Policy,
	ticks []Tick,
	quoteFor func(priceMicros uint64, sell bool, inputAmount uint64) (Quote, error),
) (RoundTripResult, error) {
	if err := policy.Validate(); err != nil {
		return RoundTripResult{}, err
	}
	observations := make([]roundTripObservation, 0, len(ticks))
	for _, tick := range ticks {
		if tick.PeriodClose {
			if tick.Event != EventClosed && tick.Event != EventMissed {
				return RoundTripResult{}, errors.New("round-trip period close is invalid")
			}
			observations = append(observations, roundTripObservation{
				at: tick.At, periodClose: true,
			})
			continue
		}
		if tick.PriceMicros == 0 && tick.Event != EventUnobservable ||
			tick.PriceMicros != 0 && tick.Event == EventUnobservable {
			return RoundTripResult{}, errors.New("round-trip tick has inconsistent observability")
		}
		observations = append(observations, roundTripObservation{
			at: tick.At, priceMicros: tick.PriceMicros, observable: tick.PriceMicros != 0,
			primary: tick.PrimaryPrice, secondary: tick.SecondaryPrice,
			nativePriceMicros: tick.NativeFeePriceMicros,
			nativePrimary:     tick.NativeFeePrimary, nativeSecondary: tick.NativeFeeSecondary,
		})
	}
	return replayRoundTrip(policy, observations, quoteFor)
}

type roundTripObservation struct {
	at                time.Time
	priceMicros       uint64
	observable        bool
	periodClose       bool
	primary           *pricetrigger.Sample
	secondary         *pricetrigger.Sample
	nativePriceMicros uint64
	nativePrimary     *pricetrigger.Sample
	nativeSecondary   *pricetrigger.Sample
}

type roundTripPending struct {
	priceMicros uint64
	quote       Quote
	sell        bool
	riskExit    bool
	settleAfter time.Time
}

func replayRoundTrip(
	policy Policy,
	observations []roundTripObservation,
	quoteFor func(priceMicros uint64, sell bool, inputAmount uint64) (Quote, error),
) (RoundTripResult, error) {
	if !policy.RoundTrip() {
		return RoundTripResult{}, errors.New("policy has no return trigger; use Replay for one direction")
	}
	if len(observations) < 2 {
		return RoundTripResult{}, errors.New("a round trip needs at least two prices to settle against")
	}
	if quoteFor == nil {
		return RoundTripResult{}, errors.New("a quote function is required")
	}

	sellRule, buyRule := policy.Trigger, *policy.ReturnTrigger
	if !policy.IsSell() {
		sellRule, buyRule = buyRule, sellRule
	}

	openingPrice := uint64(0)
	var openingNativePrice []uint64
	for _, observation := range observations {
		if observation.observable {
			var priceErr error
			openingPrice, priceErr = roundTripObservationPrice(policy, policy.IsSell(), observation)
			if priceErr != nil {
				return RoundTripResult{}, priceErr
			}
			openingNativePrice, priceErr = roundTripNativePrice(policy, observation)
			if priceErr != nil {
				return RoundTripResult{}, priceErr
			}
			break
		}
	}
	if openingPrice == 0 {
		return RoundTripResult{}, errors.New("a round trip needs an observable opening price")
	}
	ledger, err := NewLedger(policy, openingPrice, openingNativePrice...)
	if err != nil {
		return RoundTripResult{}, err
	}
	result := RoundTripResult{Ledger: ledger}
	strategy, err := newAdaptiveStrategy(policy.Adaptive)
	if err != nil {
		return RoundTripResult{}, err
	}
	// The leg the run is waiting to make.
	sell := policy.IsSell()
	nextAmount := policy.InputAmount
	var pending *roundTripPending

	var previous time.Time
	for _, observation := range observations {
		if observation.at.IsZero() || !previous.IsZero() && observation.at.Before(previous) {
			return RoundTripResult{}, errors.New("round-trip observations are not chronological")
		}
		previous = observation.at
		if observation.periodClose {
			// The journal close describes the original policy. Hypothetical
			// thresholds can have different pending state, so use only its time
			// boundary and close the candidate's own decision if it has one.
			if pending != nil {
				pending = nil
				result.Counts.Missed++
			}
			continue
		}
		result.Counts.Ticks++
		if !observation.observable {
			if pending != nil && !observation.at.Before(pending.settleAfter) {
				pending = nil
				result.Counts.Missed++
			}
			continue
		}
		price, err := roundTripObservationPrice(policy, sell, observation)
		if err != nil {
			return RoundTripResult{}, err
		}
		if price == 0 {
			return RoundTripResult{}, errZeroReference
		}
		nativePrice, err := roundTripNativePrice(policy, observation)
		if err != nil {
			return RoundTripResult{}, err
		}
		marked, err := result.Ledger.Mark(price, nativePrice...)
		if err != nil {
			return RoundTripResult{}, err
		}
		result.Ledger = marked

		rule := buyRule
		if sell {
			rule = sellRule
		}
		triggered := thresholdMet(rule, price)
		var decision *AdaptiveDecision
		if strategy != nil {
			adaptiveDecision, adaptiveTriggered, decisionErr := strategy.decide(
				observation.at, price, sell, result.Ledger,
			)
			if decisionErr != nil {
				return RoundTripResult{}, decisionErr
			}
			decision = &adaptiveDecision
			triggered = adaptiveTriggered
		}
		if triggered {
			if sell {
				result.Counts.SellSignals++
			} else {
				result.Counts.BuySignals++
			}
		}

		if pending != nil {
			if observation.at.Before(pending.settleAfter) {
				continue
			}
			settling := *pending
			pending = nil
			settlementQuote, err := quoteFor(price, settling.sell, settling.quote.InputAmount)
			if err != nil {
				if strategy != nil {
					result.Counts.Missed++
					continue
				}
				return RoundTripResult{}, err
			}
			if settlementQuote.InputAmount != settling.quote.InputAmount {
				if strategy != nil {
					result.Counts.Missed++
					continue
				}
				return RoundTripResult{}, errors.New("round-trip settlement quote changed the requested input amount")
			}
			if strategy != nil {
				if validateQuote(settlementQuote) != nil {
					result.Counts.Missed++
					continue
				}
				_, bounded, guardErr := adaptiveQuoteImpact(
					policy, settlementQuote, price, settling.sell,
				)
				if guardErr != nil || !bounded {
					result.Counts.Missed++
					continue
				}
			}
			fill, err := SettleRequotedFillDirected(
				policy, settling.quote, settlementQuote,
				settling.priceMicros, price, settling.sell,
			)
			if err != nil {
				if strategy != nil {
					result.Counts.Missed++
					continue
				}
				return RoundTripResult{}, err
			}
			applied, err := result.Ledger.Apply(fill, price, nativePrice...)
			if err != nil {
				if errors.Is(err, errInsufficientInventory) {
					result.Counts.Missed++
					continue
				}
				return RoundTripResult{}, err
			}
			result.Ledger = applied
			if !fill.Filled {
				result.Counts.Refused++
				continue
			}
			nextAmount = fill.ReceivedUnits
			if sell {
				result.Counts.Sells++
			} else {
				result.Counts.Buys++
			}
			if strategy != nil {
				strategy.filled(observation.at, settling.riskExit)
			}
			sell = !sell
			if sell {
				reserve := nextSellFeeReserve(policy)
				if result.Ledger, err = result.Ledger.replenishFeeReserve(
					reserve,
				); err != nil {
					return RoundTripResult{}, err
				}
				nextAmount = capSellAmount(nextAmount, result.Ledger, reserve)
			}
			// A live observation that settles a decision cannot also open the
			// next leg. Reusing it would give the model information twice.
			continue
		}
		if !triggered {
			continue
		}
		attemptAmount, feeReserve := paperAttempt(
			policy, result.Ledger, sell, nextAmount, decision,
		)
		// Spend what the previous leg produced, bounded by what is actually held,
		// so the book can never go short and the return leg cannot invent size.
		if !canFundAttempt(result.Ledger, sell, attemptAmount, feeReserve) {
			result.Counts.Missed++
			continue
		}
		decisionQuote, err := quoteFor(price, sell, attemptAmount)
		if err != nil {
			if strategy != nil {
				result.Counts.Missed++
				continue
			}
			return RoundTripResult{}, err
		}
		if quoteErr := validateQuote(decisionQuote); quoteErr != nil {
			if strategy != nil {
				result.Counts.Missed++
				continue
			}
			return RoundTripResult{}, quoteErr
		}
		if decisionQuote.InputAmount != attemptAmount {
			if strategy != nil {
				result.Counts.Missed++
				continue
			}
			return RoundTripResult{}, errors.New("round-trip quote changed the requested input amount")
		}
		passes, guardErr := adaptiveQuotePasses(
			policy, decision, decisionQuote, price, sell,
		)
		if guardErr != nil {
			result.Counts.Missed++
			continue
		}
		if !passes {
			result.Counts.Filtered++
			continue
		}
		pending = &roundTripPending{
			priceMicros: price, quote: decisionQuote, sell: sell,
			riskExit:    decision != nil && decision.Strategy == StrategyRiskExit,
			settleAfter: observation.at.Add(policy.Settle()),
		}
	}

	for index := len(observations) - 1; index >= 0; index-- {
		if observations[index].observable {
			result.ClosingPrice, err = roundTripObservationPrice(
				policy, sell, observations[index],
			)
			if err != nil {
				return RoundTripResult{}, err
			}
			break
		}
	}
	return result, nil
}

func roundTripObservationPrice(
	policy Policy, sell bool, observation roundTripObservation,
) (uint64, error) {
	if observation.primary == nil && observation.secondary == nil {
		return observation.priceMicros, nil
	}
	if observation.primary == nil || observation.secondary == nil {
		return 0, errors.New("round-trip observation has incomplete market source evidence")
	}
	evidence, err := pricetrigger.Evaluate(
		triggerFor(policy, sell), *observation.primary, *observation.secondary, observation.at,
	)
	if err != nil {
		return 0, err
	}
	return evidence.ConservativePrice, nil
}

func roundTripNativePrice(policy Policy, observation roundTripObservation) ([]uint64, error) {
	if policy.NativeFeePrice == nil {
		return nil, nil
	}
	if observation.nativePrimary == nil || observation.nativeSecondary == nil ||
		observation.nativePriceMicros == 0 {
		return nil, errors.New("round-trip observation has incomplete native fee-price evidence")
	}
	evidence, err := pricetrigger.Evaluate(
		*policy.NativeFeePrice, *observation.nativePrimary, *observation.nativeSecondary, observation.at,
	)
	if err != nil || evidence.ConservativePrice != observation.nativePriceMicros ||
		observation.nativePriceMicros > policy.NativeFeePriceCeilingMicros {
		return nil, errors.New("round-trip observation has invalid native fee-price evidence")
	}
	return []uint64{observation.nativePriceMicros}, nil
}

// thresholdMet applies the rule's own comparison, in the same direction the
// live trigger uses. It deliberately reads only the threshold: staleness,
// source agreement and confidence are properties of a live feed, and a replay
// over a given price series has already decided what the price was.
func thresholdMet(rule pricetrigger.Policy, priceMicros uint64) bool {
	if rule.Direction == pricetrigger.SellAtOrAbove {
		return priceMicros >= rule.ThresholdMicros
	}
	return priceMicros <= rule.ThresholdMicros
}
