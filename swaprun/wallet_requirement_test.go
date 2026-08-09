package swaprun

import (
	"testing"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
)

// The sweep decides how much SOL may leave the wallet by asking this. The rent
// term is direction-dependent, and a caller that reaches for the sell route's
// field on a buy profile reads zero — silently under-reserving by the whole
// temporary-account rent and starving the buy runner forever. Testing a
// free-standing copy of this arithmetic is what let that ship green, so this
// test drives the profile itself.
func TestWalletRequirementUsesTheDirectionsOwnRent(t *testing.T) {
	sell := testProfile()
	sell.ReserveLamports = 50_000_000
	sell.InputLamports = 1_000_000
	sell.MaxFeeLamports = 100_000
	sell.Route.MaxOutputAccountRentLamports = 3_000_000
	if got, want := sell.WalletRequirementLamports(), uint64(54_100_000); got != want {
		t.Errorf("sell requirement = %d, want %d", got, want)
	}

	// A buy carries its rent on BuyRoute and leaves Route zeroed, which
	// swaprun/profile.go requires. Reading Route here would report 3,000,000
	// less than the runner demands.
	buy := sell
	buy.Name, buy.Version = orcaswap.BuyProfileName, orcaswap.BuyProfileVersion
	buy.Route = orcaswap.Policy{}
	buy.BuyRoute = &orcaswap.BuyPolicyV2{MaxTemporaryRentLamports: 3_000_000}
	got := buy.WalletRequirementLamports()
	if want := uint64(53_100_000); got != want {
		t.Errorf("buy requirement = %d, want %d", got, want)
	}
	// The specific regression: reading the sell route's field would give
	// 50,100,000 and under-reserve by exactly the temporary-account rent.
	if got == 50_100_000 {
		t.Error("buy requirement ignored BuyRoute rent — the sweep would starve the buy runner")
	}
}
