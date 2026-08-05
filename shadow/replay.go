package shadow

import "errors"

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
}

// Replay rebuilds the run from its ticks, in order.
func Replay(policy Policy, ticks []Tick) (Replayed, error) {
	if err := policy.Validate(); err != nil {
		return Replayed{}, err
	}
	if len(ticks) == 0 {
		return Replayed{}, errors.New("no ticks to replay")
	}
	var (
		result Replayed
		opened bool
		err    error
	)
	for _, tick := range ticks {
		result.Counts.Ticks++
		if tick.Event == EventUnobservable {
			result.Counts.Unobservable++
			continue
		}
		if tick.PriceMicros == 0 {
			// Any other event without a price could not have been produced by a
			// live run, so the record is not one this can rebuild from.
			return Replayed{}, errors.New("a recorded tick is missing its price")
		}
		if !opened {
			if result.Ledger, err = NewLedger(policy, tick.PriceMicros); err != nil {
				return Replayed{}, err
			}
			opened = true
		}
		result.ClosingPrice = tick.PriceMicros

		if tick.Triggered {
			result.Counts.Signals++
			if tick.Deferred {
				result.Counts.Deferred++
			}
		}
		switch tick.Event {
		case EventWaiting, EventSignal:
			if result.Ledger, err = result.Ledger.Mark(tick.PriceMicros); err != nil {
				return Replayed{}, err
			}
		case EventMissed:
			result.Counts.Missed++
			if result.Ledger, err = result.Ledger.Mark(tick.PriceMicros); err != nil {
				return Replayed{}, err
			}
		case EventFilled, EventRefused:
			if tick.Fill == nil {
				return Replayed{}, errors.New("a settled tick was recorded without its fill")
			}
			if result.Ledger, err = result.Ledger.Mark(tick.PriceMicros); err != nil {
				return Replayed{}, err
			}
			if result.Ledger, err = result.Ledger.Apply(*tick.Fill, tick.PriceMicros); err != nil {
				return Replayed{}, err
			}
			result.Stats.Settled++
			result.Stats.SumImpactBPS += int64(tick.Fill.ImpactBPS)
			result.Stats.SumSlippageBPS += int64(tick.Fill.SlippageBPS)
			if tick.Fill.SlippageBPS < result.Stats.WorstSlippageBPS {
				result.Stats.WorstSlippageBPS = tick.Fill.SlippageBPS
			}
			if tick.Fill.Filled {
				result.Counts.Fills++
			} else {
				result.Counts.Refused++
			}
		case EventOpened:
			// The opening record carries the ledger, not a tick. It is counted
			// by the writer as part of the first tick, so it is skipped here.
			result.Counts.Ticks--
		default:
			return Replayed{}, errors.New("the record contains an unrecognised event")
		}
	}
	if !opened {
		return Replayed{}, errors.New("the record contains no observable tick")
	}
	return result, nil
}

// Disagreement names one field where a stored report differs from what the
// record actually supports.
type Disagreement struct {
	Field    string `json:"field"`
	Stored   int64  `json:"stored"`
	Replayed int64  `json:"replayed"`
}

// Compare checks a stored report against a recomputed one. It reports every
// difference rather than the first, because a single mismatched field and a
// wholesale fabrication need to be distinguishable at a glance.
func Compare(stored Report, replayed Report) []Disagreement {
	var found []Disagreement
	check := func(field string, a, b int64) {
		if a != b {
			found = append(found, Disagreement{Field: field, Stored: a, Replayed: b})
		}
	}
	check("counts.ticks", int64(stored.Counts.Ticks), int64(replayed.Counts.Ticks))
	check("counts.signals", int64(stored.Counts.Signals), int64(replayed.Counts.Signals))
	check("counts.fills", int64(stored.Counts.Fills), int64(replayed.Counts.Fills))
	check("counts.refused", int64(stored.Counts.Refused), int64(replayed.Counts.Refused))
	check("counts.missed", int64(stored.Counts.Missed), int64(replayed.Counts.Missed))
	check("counts.deferred", int64(stored.Counts.Deferred), int64(replayed.Counts.Deferred))
	check("counts.unobservable", int64(stored.Counts.Unobservable), int64(replayed.Counts.Unobservable))
	check("stats.settled", int64(stored.Stats.Settled), int64(replayed.Stats.Settled))
	check("realized_micros", stored.RealizedMicros, replayed.RealizedMicros)
	check("unrealized_micros", stored.UnrealizedMicros, replayed.UnrealizedMicros)
	check("fees_micros", stored.FeesMicros, replayed.FeesMicros)
	check("turnover_micros", int64(stored.TurnoverMicros), int64(replayed.TurnoverMicros))
	check("max_drawdown_micros", int64(stored.MaxDrawdownMicros), int64(replayed.MaxDrawdownMicros))
	check("closing_equity_micros", int64(stored.ClosingEquityMicros), int64(replayed.ClosingEquityMicros))
	check("versus_hold_micros", stored.VersusHoldMicros, replayed.VersusHoldMicros)
	return found
}
