package swaprun

import (
	"math/big"
	"testing"
)

// The derived executable price decides whether a quote clears the operator's
// threshold, so every rounding choice has to go against the trade. A sell needs
// price >= threshold, so its price must never be rounded UP into qualifying; a
// buy needs price <= threshold, so its price must never be rounded DOWN.
//
// Both legs satisfy this today by opposite means — the sell truncates and the
// buy takes a ceiling — which is easy to "simplify" into a single shared helper
// and silently break one side. Stated as one property, that is caught.
func TestDerivedPriceAlwaysRoundsAgainstTheTrade(t *testing.T) {
	scale := big.NewInt(1_000_000_000)

	t.Run("sell never rounds up into qualifying", func(t *testing.T) {
		profile := testProfile()
		// Deliberately indivisible, so truncation is actually exercised.
		for _, input := range []uint64{3, 7, 999_999, 1_000_003} {
			profile.InputLamports = input
			for _, minimumOutput := range []uint64{1, 2, 21_525, 999_997} {
				derived, err := executablePriceMicros(profile, minimumOutput)
				if err != nil {
					continue // outside policy bounds; not a rounding case
				}
				// derived <= minimumOutput*1e9/input, i.e.
				// derived*input <= minimumOutput*1e9
				left := new(big.Int).Mul(new(big.Int).SetUint64(derived), new(big.Int).SetUint64(input))
				right := new(big.Int).Mul(new(big.Int).SetUint64(minimumOutput), scale)
				if left.Cmp(right) > 0 {
					t.Errorf(
						"sell price rounded up: input=%d minOut=%d derived=%d",
						input, minimumOutput, derived,
					)
				}
			}
		}
	})

	t.Run("buy never rounds down into qualifying", func(t *testing.T) {
		profile := testBuyProfile(t)
		for _, tokens := range []uint64{3, 7, 999_999, 1_000_003} {
			profile.InputTokenAmount = tokens
			for _, minimumOutput := range []uint64{1, 2, 21_525, 999_997} {
				derived, err := executablePriceMicros(profile, minimumOutput)
				if err != nil {
					continue
				}
				// derived >= tokens*1e9/minimumOutput, i.e.
				// derived*minimumOutput >= tokens*1e9
				left := new(big.Int).Mul(new(big.Int).SetUint64(derived), new(big.Int).SetUint64(minimumOutput))
				right := new(big.Int).Mul(new(big.Int).SetUint64(tokens), scale)
				if left.Cmp(right) < 0 {
					t.Errorf(
						"buy price rounded down: tokens=%d minOut=%d derived=%d",
						tokens, minimumOutput, derived,
					)
				}
			}
		}
	})
}
