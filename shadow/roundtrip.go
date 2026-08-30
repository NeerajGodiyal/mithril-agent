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
		})
	}
	return replayRoundTrip(policy, observations, quoteFor)
}

type roundTripObservation struct {
	at          time.Time
	priceMicros uint64
	observable  bool
	periodClose bool
}

type roundTripPending struct {
	priceMicros uint64
	quote       Quote
	sell        bool
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
	for _, observation := range observations {
		if observation.observable {
			openingPrice = observation.priceMicros
			break
		}
	}
	if openingPrice == 0 {
		return RoundTripResult{}, errors.New("a round trip needs an observable opening price")
	}
	ledger, err := NewLedger(policy, openingPrice)
	if err != nil {
		return RoundTripResult{}, err
	}
	result := RoundTripResult{Ledger: ledger}
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
		price := observation.priceMicros
		if price == 0 {
			return RoundTripResult{}, errZeroReference
		}
		marked, err := result.Ledger.Mark(price)
		if err != nil {
			return RoundTripResult{}, err
		}
		result.Ledger = marked

		rule := buyRule
		if sell {
			rule = sellRule
		}
		triggered := thresholdMet(rule, price)
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
			settlementQuote, err := quoteFor(price, pending.sell, pending.quote.InputAmount)
			if err != nil {
				return RoundTripResult{}, err
			}
			if settlementQuote.InputAmount != pending.quote.InputAmount {
				return RoundTripResult{}, errors.New("round-trip settlement quote changed the requested input amount")
			}
			fill, err := SettleRequotedFillDirected(
				policy, pending.quote, settlementQuote,
				pending.priceMicros, price, pending.sell,
			)
			pending = nil
			if err != nil {
				return RoundTripResult{}, err
			}
			applied, err := result.Ledger.Apply(fill, price)
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
			sell = !sell
			if sell {
				nextAmount = capSellAmount(
					nextAmount, result.Ledger.BaseUnits, roundTripFeeReserve(policy.FeeLamports),
				)
			}
			// A live observation that settles a decision cannot also open the
			// next leg. Reusing it would give the model information twice.
			continue
		}
		if !triggered {
			continue
		}
		// Spend what the previous leg produced, bounded by what is actually held,
		// so the book can never go short and the return leg cannot invent size.
		if !canFundAttempt(
			result.Ledger, sell, nextAmount, attemptFeeReserve(policy, sell),
		) {
			result.Counts.Missed++
			continue
		}
		decisionQuote, err := quoteFor(price, sell, nextAmount)
		if err != nil {
			return RoundTripResult{}, err
		}
		if decisionQuote.InputAmount != nextAmount {
			return RoundTripResult{}, errors.New("round-trip quote changed the requested input amount")
		}
		pending = &roundTripPending{
			priceMicros: price, quote: decisionQuote, sell: sell,
			settleAfter: observation.at.Add(policy.Settle()),
		}
	}

	for index := len(observations) - 1; index >= 0; index-- {
		if observations[index].observable {
			result.ClosingPrice = observations[index].priceMicros
			break
		}
	}
	return result, nil
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
