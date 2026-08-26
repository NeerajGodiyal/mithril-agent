package shadow

import (
	"errors"

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
	if !policy.RoundTrip() {
		return RoundTripResult{}, errors.New("policy has no return trigger; use Replay for one direction")
	}
	if len(prices) < 2 {
		return RoundTripResult{}, errors.New("a round trip needs at least two prices to settle against")
	}
	if quoteFor == nil {
		return RoundTripResult{}, errors.New("a quote function is required")
	}

	sellRule, buyRule := policy.Trigger, *policy.ReturnTrigger
	if !policy.IsSell() {
		sellRule, buyRule = buyRule, sellRule
	}

	ledger, err := NewLedger(policy, prices[0])
	if err != nil {
		return RoundTripResult{}, err
	}
	result := RoundTripResult{Ledger: ledger}
	// The leg the run is waiting to make.
	sell := policy.IsSell()
	nextAmount := policy.InputAmount

	// The last price has no later price to settle against, and settling a
	// decision against the price that produced it is the single easiest way to
	// make a paper result flatter itself.
	for index := 0; index < len(prices)-1; index++ {
		decision, settle := prices[index], prices[index+1]
		result.Counts.Ticks++
		if decision == 0 || settle == 0 {
			return RoundTripResult{}, errZeroReference
		}

		rule := buyRule
		if sell {
			rule = sellRule
		}
		if !thresholdMet(rule, decision) {
			continue
		}
		if sell {
			result.Counts.SellSignals++
		} else {
			result.Counts.BuySignals++
		}

		// Spend what the previous leg produced, bounded by what is actually held,
		// so the book can never go short and the return leg cannot invent size.
		held := result.Ledger.QuoteUnits
		if sell {
			held = result.Ledger.BaseUnits
		}
		if nextAmount == 0 || nextAmount > held {
			continue
		}
		quote, err := quoteFor(decision, sell, nextAmount)
		if err != nil {
			return RoundTripResult{}, err
		}
		if quote.InputAmount != nextAmount {
			return RoundTripResult{}, errors.New("round-trip quote changed the requested input amount")
		}

		fill, err := SettleFillDirected(policy, quote, decision, settle, sell)
		if err != nil {
			return RoundTripResult{}, err
		}
		if !fill.Filled {
			result.Counts.Refused++
			continue
		}
		// Into a TEMPORARY, because Apply returns a zero Ledger on failure and
		// assigning before checking the error wiped the entire book on the first
		// refused fill — every later tick then read as the opposite direction.
		applied, err := result.Ledger.Apply(fill, settle)
		if err != nil {
			// Not enough inventory is a real refusal, not a programming error:
			// the fee is charged out of the same balance the trade spends, so a
			// leg sized at the whole balance can never settle.
			if errors.Is(err, errInsufficientInventory) {
				result.Counts.Refused++
				continue
			}
			return RoundTripResult{}, err
		}
		result.Ledger = applied
		nextAmount = fill.ReceivedUnits
		if sell {
			result.Counts.Sells++
		} else {
			result.Counts.Buys++
		}
		// Handed over: the next leg is the other direction.
		sell = !sell
		if sell && nextAmount > result.Ledger.BaseUnits {
			nextAmount = result.Ledger.BaseUnits
		}
	}

	result.ClosingPrice = prices[len(prices)-1]
	if result.Ledger, err = result.Ledger.Mark(result.ClosingPrice); err != nil {
		return RoundTripResult{}, err
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
