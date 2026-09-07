package shadow

import (
	"errors"
	"math/big"
	"time"
)

// AttributedSettlement describes one validated settlement's accounting change.
// RealizedMicros includes the ledger's fee cost basis; FeesMicros is the fee's
// current valuation, reported separately and never subtracted a second time.
// This is not round-trip profit or evidence that an entry label caused a return.
type AttributedSettlement struct {
	OriginAt       time.Time `json:"origin_at"`
	SettledAt      time.Time `json:"settled_at"`
	Strategy       string    `json:"strategy"`
	Regime         string    `json:"regime"`
	OriginKnown    bool      `json:"origin_known"`
	Sell           bool      `json:"sell"`
	Filled         bool      `json:"filled"`
	RealizedMicros int64     `json:"realized_micros,string"`
	FeesMicros     int64     `json:"fees_micros,string"`
}

// ReplayAttribution is opt-in, read-only accounting attribution. Invalid or
// unmatched settlement evidence fails replay; missing adaptive labels remain
// explicitly unknown rather than borrowing a later observation's decision.
type ReplayAttribution struct {
	Settlements          []AttributedSettlement `json:"settlements"`
	MissedOrigins        uint64                 `json:"missed_origins"`
	PendingOrigins       uint64                 `json:"pending_origins"`
	UnknownOrigins       uint64                 `json:"unknown_origins"`
	UnmatchedSettlements uint64                 `json:"unmatched_settlements"`
	origin               *AttributedSettlement
}

// ReplayWithAttribution runs the same strict replay as Replay and additionally
// collects settlement deltas. Errors return no partial accounting or attribution.
func ReplayWithAttribution(policy Policy, ticks []Tick) (Replayed, ReplayAttribution, error) {
	var attribution ReplayAttribution
	result, err := replayWithAttribution(policy, ticks, &attribution)
	if err != nil {
		return Replayed{}, ReplayAttribution{}, err
	}
	if attribution.origin != nil {
		attribution.PendingOrigins = 1
	}
	attribution.origin = nil
	return result, attribution, nil
}

func (a *ReplayAttribution) signal(tick Tick) {
	origin := AttributedSettlement{OriginAt: tick.At, Strategy: "unknown", Regime: "unknown"}
	if tick.Decision != nil {
		origin.Strategy, origin.Regime, origin.OriginKnown = tick.Decision.Strategy, tick.Decision.Regime, true
	}
	a.origin = &origin
}

func (a *ReplayAttribution) miss() {
	if a.origin != nil {
		a.MissedOrigins++
	}
	a.origin = nil
}

func (a *ReplayAttribution) settle(tick Tick, before, after Ledger) error {
	if a.origin == nil {
		return errors.New("attribution settlement has no verified origin")
	}
	row := *a.origin
	row.SettledAt, row.Sell, row.Filled = tick.At, tick.Fill.Sell, tick.Fill.Filled
	delta := new(big.Int).Sub(big.NewInt(after.RealizedMicros), big.NewInt(before.RealizedMicros))
	if !delta.IsInt64() {
		return errors.New("attribution realized delta is out of range")
	}
	row.RealizedMicros = delta.Int64()
	row.FeesMicros = after.FeesMicros - before.FeesMicros
	if !row.OriginKnown {
		a.UnknownOrigins++
	}
	a.Settlements = append(a.Settlements, row)
	a.origin = nil
	return nil
}
