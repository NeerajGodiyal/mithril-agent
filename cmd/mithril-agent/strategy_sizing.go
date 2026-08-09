package main

import (
	"errors"
	"math/bits"
)

// The sweep can only move native SOL, but a completed sell leaves the proceeds
// in devUSDC. Left to the operator, that means the profit sits in a token the
// sweep cannot send, and the obvious fix — teaching the sweep to move SPL
// tokens — is a new transaction shape and a new signer policy.
//
// Sizing the buy to spend exactly what one sell produces avoids all of it: the
// round trip returns devUSDC to where it started and leaves the entire gain in
// SOL, which is the one asset the sweep already handles. The arithmetic below
// is what makes that true, so it is integer-only and refuses to wrap.

// buyInputForSell is the devUSDC a sell of sizeLamports produces at sellAtMicros.
//
//	devUSDC base units = lamports × (micros per SOL) / 1e9
//
// It floors. Rounding up would size the buy to spend devUSDC the sell never
// produced, so the second leg would sit unfundable forever waiting on a balance
// that cannot arrive.
func buyInputForSell(sizeLamports, sellAtMicros uint64) (uint64, error) {
	if sizeLamports == 0 || sellAtMicros == 0 {
		return 0, errors.New("both the trade size and the sell price must be above zero")
	}
	high, low := bits.Mul64(sizeLamports, sellAtMicros)
	if high >= lamportsPerSOL {
		// Div64 panics when the quotient will not fit. Refusing beats a panic,
		// and beats a saturate: a saturated size is a number nobody chose.
		return 0, errors.New("that trade size and price multiply out beyond what the agent can represent")
	}
	amount, _ := bits.Div64(high, low, lamportsPerSOL)
	if amount == 0 {
		return 0, errors.New("that trade is too small to produce any devUSDC at this price")
	}
	return amount, nil
}

// roundTripGainLamports is the SOL a completed round trip adds, given that the
// buy spends exactly what the sell produced.
//
//	SOL back = devUSDC × 1e9 / buyAtMicros
//	gain     = SOL back − what was sold
//
// A gain is only guaranteed because both legs are price-gated: the sell cannot
// fire below sellAt and the buy cannot fire above buyAt. It is not a market
// prediction — it is what the configured bounds permit.
func roundTripGainLamports(sizeLamports, sellAtMicros, buyAtMicros uint64) (uint64, error) {
	if buyAtMicros == 0 {
		return 0, errors.New("the buy price must be above zero")
	}
	if buyAtMicros >= sellAtMicros {
		// Equal prices net a loss once fees are paid, and an inverted pair is a
		// strategy that buys high and sells low. Neither is a rounding concern.
		return 0, errors.New("the buy price must be below the sell price")
	}
	devUSDC, err := buyInputForSell(sizeLamports, sellAtMicros)
	if err != nil {
		return 0, err
	}
	high, low := bits.Mul64(devUSDC, lamportsPerSOL)
	if high >= buyAtMicros {
		return 0, errors.New("that trade size and price multiply out beyond what the agent can represent")
	}
	back, _ := bits.Div64(high, low, buyAtMicros)
	if back <= sizeLamports {
		// Reachable through flooring alone on a small enough trade, which is
		// exactly the case an operator would not predict.
		return 0, errors.New("that trade is too small to gain anything after rounding")
	}
	return back - sizeLamports, nil
}

// strategyFloorLamports is what the sweep must leave behind: rent, fee headroom,
// and the wallet requirement of BOTH legs — including the buy leg, which does
// not exist yet at setup time. Reserving it in advance is what lets the buy be
// written later without redoing the sweep, whose destination proof would
// otherwise have to be re-signed.
func strategyFloorLamports(sellNeed, buyNeed uint64) (uint64, error) {
	base := uint64(rentExemptFloorLamports) + 3*defaultSweepFee
	total, err := addLamports(base, sellNeed)
	if err != nil {
		return 0, err
	}
	return addLamports(total, buyNeed)
}
