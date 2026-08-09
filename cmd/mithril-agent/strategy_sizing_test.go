package main

import (
	"math"
	"testing"
)

// The whole point of the sizing: a round trip must return devUSDC to where it
// started so the gain lands in SOL, the only asset the sweep can move.
func TestBuySpendsExactlyWhatTheSellProduces(t *testing.T) {
	// 0.05 SOL at $250 is 12.50 devUSDC.
	got, err := buyInputForSell(50_000_000, 250_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if want := uint64(12_500_000); got != want {
		t.Fatalf("devUSDC = %d, want %d", got, want)
	}
	// And buying that back at $200 returns 0.0625 SOL, a gain of 0.0125.
	gain, err := roundTripGainLamports(50_000_000, 250_000_000, 200_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if want := uint64(12_500_000); gain != want {
		t.Fatalf("gain = %d lamports, want %d", gain, want)
	}
}

// Rounding up would size the buy to spend devUSDC the sell never produced,
// leaving the second leg unfundable forever waiting on a balance that cannot
// arrive.
func TestBuySizingFloorsRatherThanRoundingUp(t *testing.T) {
	// 1 lamport at $250.000001 is a fraction of a devUSDC base unit.
	got, err := buyInputForSell(4_000_000, 250_000_001)
	if err != nil {
		t.Fatal(err)
	}
	exact := 4_000_000 * 250_000_001 / 1_000_000_000
	if got != uint64(exact) {
		t.Fatalf("devUSDC = %d, want the floored %d", got, exact)
	}
	if got*1_000_000_000/250_000_001 > 4_000_000 {
		t.Error("the sized buy would need more SOL than the sell produced")
	}
}

// Overflow must refuse rather than panic inside bits.Div64 or silently
// saturate to a number nobody chose.
func TestSizingRefusesAmountsItCannotRepresent(t *testing.T) {
	if _, err := buyInputForSell(math.MaxUint64, math.MaxUint64); err == nil {
		t.Fatal("an unrepresentable size and price were accepted")
	}
	if _, err := roundTripGainLamports(math.MaxUint64, math.MaxUint64, 1); err == nil {
		t.Fatal("an unrepresentable round trip was accepted")
	}
	// The above stops inside buyInputForSell's FIRST guard, leaving the second
	// one — the devUSDC x 1e9 product — untested. This input reaches it.
	if _, err := roundTripGainLamports(1<<63, 4, 1); err == nil {
		t.Fatal("an unrepresentable buy-back was accepted")
	}
	for name, test := range map[string]struct{ size, sell uint64 }{
		"zero size":  {0, 250_000_000},
		"zero price": {50_000_000, 0},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := buyInputForSell(test.size, test.sell); err == nil {
				t.Fatal("a zero input was accepted")
			}
		})
	}
}

// A buy price at or above the sell price is not a rounding concern: equal nets
// a loss after fees, and inverted is a strategy that buys high and sells low.
func TestGainRefusesAnInvertedOrEqualPricePair(t *testing.T) {
	for name, prices := range map[string][2]uint64{
		"equal":    {250_000_000, 250_000_000},
		"inverted": {200_000_000, 250_000_000},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := roundTripGainLamports(50_000_000, prices[0], prices[1]); err == nil {
				t.Fatal("an unprofitable price pair was accepted")
			}
		})
	}
}

// A trade small enough that flooring eats the entire gain must be refused, not
// configured — otherwise the sweep minimum can never be met and the operator
// waits on a profit that mathematically cannot arrive.
func TestGainRefusesATradeTooSmallToGainAnything(t *testing.T) {
	// A size of 1 stops earlier, in "too small to produce any devUSDC", so it
	// never reached the branch this test is named for. These sizes do: they
	// yield 1 devUSDC base unit, which buys back no more than was sold.
	for _, size := range []uint64{4, 5, 6, 7} {
		if _, err := roundTripGainLamports(size, 250_000_000, 249_999_999); err == nil {
			t.Fatalf("a trade of %d lamports whose gain floors to nothing was accepted", size)
		}
	}
}

// The floor must reserve the buy leg BEFORE it exists, or configuring it later
// would require redoing the sweep — including re-signing the destination proof.
func TestStrategyFloorReservesBothLegsAndRefusesOverflow(t *testing.T) {
	got, err := strategyFloorLamports(100_300_000, 53_100_000)
	if err != nil {
		t.Fatal(err)
	}
	want := uint64(rentExemptFloorLamports) + 3*defaultSweepFee + 100_300_000 + 53_100_000
	if got != want {
		t.Fatalf("floor = %d, want %d", got, want)
	}
	if _, err := strategyFloorLamports(math.MaxUint64, 1); err == nil {
		t.Fatal("a floor beyond the lamport supply was accepted")
	}
}
