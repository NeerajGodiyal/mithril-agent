package shadow

import (
	"testing"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

// roundTripPolicy is a sell-then-buy-back rule on one book: start holding SOL,
// sell at or above $22, buy back at or below $18.
func roundTripPolicy(t *testing.T, spreadBPS uint16) Policy {
	t.Helper()
	policy := sellPolicy()
	policy.StartingInputUnits = 2_000_000_000 // 2 SOL, trading 1 at a time
	policy.StartingOutputUnits = 0
	policy.Trigger.ThresholdMicros = 22_000_000
	buy := policy.Trigger
	buy.Direction = pricetrigger.BuyAtOrBelow
	buy.ThresholdMicros = 18_000_000
	policy.ReturnTrigger = &buy
	policy.SlippageBPS = spreadBPS
	return policy
}

// A round trip is not the sum of two independent decisions: the second leg
// spends exactly what the first produced, and the spread plus two fees comes
// out of one book. Running the legs separately cannot show that, which is why
// a one-directional shadow run could never answer "does buy-low-sell-high
// actually make money here".
func TestRoundTripProfitsWhenTheSpreadIsSmallerThanTheSwing(t *testing.T) {
	policy := roundTripPolicy(t, 100)
	// Sell high, then buy back low: $23 -> $24 (sell) -> $17 -> $16 (buy) -> $20.
	prices := []uint64{23_000_000, 24_000_000, 17_000_000, 16_000_000, 20_000_000}

	baseQuote := tightQuote()
	var quotes []Quote
	result, err := ReplayRoundTrip(policy, prices, func(price uint64, sell bool, amount uint64) (Quote, error) {
		quote, quoteErr := baseQuote(price, sell, amount)
		quotes = append(quotes, quote)
		return quote, quoteErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts.Sells != 1 || result.Counts.Buys != 1 {
		t.Fatalf("counts = %+v, want exactly one sell and one buy", result.Counts)
	}
	if len(quotes) != 2 {
		t.Fatalf("round trip produced %d quotes, want 2: %+v", len(quotes), quotes)
	}
	settledSellOutput := quotes[0].EstimatedOutput * prices[1] / prices[0]
	if quotes[1].InputAmount != settledSellOutput {
		t.Fatalf("return leg did not spend the sell proceeds: %+v", quotes)
	}
	// The return leg spends the sell proceeds at the lower price, so the book
	// must hold more SOL than it opened with.
	if result.Ledger.BaseUnits <= policy.StartingInputUnits {
		t.Errorf("round trip did not grow the position: %d base units, opened with %d",
			result.Ledger.BaseUnits, policy.StartingInputUnits)
	}
	if result.Ledger.RealizedMicros <= 0 {
		t.Errorf("realized = %d micros, want a profit", result.Ledger.RealizedMicros)
	}
}

func TestRoundTripRefusesAQuoteThatChangesItsSize(t *testing.T) {
	policy := roundTripPolicy(t, 100)
	_, err := ReplayRoundTrip(policy, []uint64{23_000_000, 24_000_000},
		func(_ uint64, _ bool, amount uint64) (Quote, error) {
			return Quote{InputAmount: amount + 1, EstimatedOutput: 1, MinimumOutput: 1}, nil
		})
	if err == nil {
		t.Fatal("a pool model changed the configured trade size")
	}
}

// The honest half: on a pool whose spread exceeds the swing, the same rule
// LOSES money. A harness that cannot produce this answer is not a test, it is
// an advertisement.
func TestRoundTripLosesWhenTheSpreadExceedsTheSwing(t *testing.T) {
	policy := roundTripPolicy(t, 100)
	// A shallow swing: sell at $22.1, buy back at $17.9, against a 25% spread.
	prices := []uint64{22_100_000, 22_100_000, 17_900_000, 17_900_000, 20_000_000}

	result, err := ReplayRoundTrip(policy, prices, wideQuote())
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts.Sells == 0 {
		t.Fatal("the sell leg never fired, so the test proves nothing")
	}
	// Either the floor refused the fills, or they happened and lost money.
	// Both are honest outcomes; silently reporting a profit would not be.
	if result.Counts.Refused == 0 && result.Ledger.RealizedMicros > 0 {
		t.Errorf("a 25%% spread produced a profit: realized=%d refused=%d",
			result.Ledger.RealizedMicros, result.Counts.Refused)
	}
}

// A completed leg hands over to the opposite rule, so spare opening inventory
// cannot accidentally arm the same sell twice.
func TestRoundTripNeverSellsWhatItDoesNotHold(t *testing.T) {
	policy := roundTripPolicy(t, 100)
	// A price path that sits above the sell threshold the whole way: after the
	// first sell the runner is waiting to buy, so later high ticks do nothing.
	prices := []uint64{25_000_000, 25_000_000, 26_000_000, 27_000_000, 25_000_000}

	result, err := ReplayRoundTrip(policy, prices, tightQuote())
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts.Sells > 1 {
		t.Errorf("sold %d times holding one lot: %+v", result.Counts.Sells, result.Counts)
	}
	if result.Ledger.BaseUnits > policy.StartingInputUnits {
		t.Errorf("book grew without a buy: %d base units", result.Ledger.BaseUnits)
	}
}

// Guardrails on the policy itself. Each of these would produce a number
// somebody would believe.
func TestRoundTripPolicyRefusesIncoherentRules(t *testing.T) {
	base := roundTripPolicy(t, 100)

	same := base
	both := *base.ReturnTrigger
	both.Direction = pricetrigger.SellAtOrAbove
	same.ReturnTrigger = &both
	if err := same.Validate(); err == nil {
		t.Error("two sell rules were accepted as a round trip")
	}

	overlap := base
	high := *base.ReturnTrigger
	high.ThresholdMicros = 22_000_000 // buy at the same price it sells
	overlap.ReturnTrigger = &high
	if err := overlap.Validate(); err == nil {
		t.Error("a buy threshold at the sell price was accepted")
	}

	otherFeed := base
	feed := *base.ReturnTrigger
	feed.PrimarySourceSHA256 = ""
	otherFeed.ReturnTrigger = &feed
	if err := otherFeed.Validate(); err == nil {
		t.Error("two different price feeds were accepted for one round trip")
	}

	// And a one-directional policy must be refused by the round-trip replay
	// rather than silently scored as something it is not.
	single := sellPolicy()
	if _, err := ReplayRoundTrip(single, []uint64{20_000_000, 21_000_000}, tightQuote()); err == nil {
		t.Error("a policy with no return trigger was replayed as a round trip")
	}
}

// tightQuote prices both directions at the oracle, with a 10bps cost. It is the
// "does the accounting work" case.
func tightQuote() func(uint64, bool, uint64) (Quote, error) {
	return quoteWithSpread(10)
}

// wideQuote charges 2500bps (25%) each way — a pool far worse than the swing.
func wideQuote() func(uint64, bool, uint64) (Quote, error) {
	return quoteWithSpread(2_500)
}

// quoteWithSpread converts an oracle price into a pool quote that is worse than
// the oracle by spreadBPS, in whichever direction the trade goes.
//
// SOL has 9 decimals and devUSDC 6, and price is USD-micros per whole SOL, so
// selling n lamports yields n*price/1e9 devUSDC units and spending u devUSDC
// units yields u*1e9/price lamports.
func quoteWithSpread(spreadBPS uint64) func(uint64, bool, uint64) (Quote, error) {
	const lamportsPerSOL = uint64(1_000_000_000)
	return func(price uint64, sell bool, in uint64) (Quote, error) {
		var out uint64
		if sell {
			out = in * price / lamportsPerSOL
		} else {
			out = in * lamportsPerSOL / price
		}
		out = out * (10_000 - spreadBPS) / 10_000
		if out == 0 {
			out = 1
		}
		return Quote{InputAmount: in, EstimatedOutput: out, MinimumOutput: out}, nil
	}
}

// Apply returns a ZERO Ledger alongside its error, so assigning its result
// before checking the error wipes the entire book. My first version did exactly
// that: the first refused leg silently reset inventory to nothing, every later
// tick then read as the opposite direction, and the run reported a plausible
// set of counts computed from a book that had ceased to exist.
//
// Nothing else in this package catches it — the other tests never refuse a leg
// inside Apply — so this is the only thing standing between that bug and a
// believable wrong answer.
func TestARefusedLegLeavesTheBookIntact(t *testing.T) {
	policy := roundTripPolicy(t, 100)
	// Start with the buy leg and no separate SOL fee reserve. A deliberately
	// tiny modelled output cannot pay the fee, so Ledger.Apply refuses after
	// the quote has settled — the exact error path that once zeroed the book.
	sell := policy.Trigger
	policy.Trigger = *policy.ReturnTrigger
	policy.ReturnTrigger = &sell
	policy.InputDecimals, policy.OutputDecimals = 6, 9
	policy.StartingInputUnits = 1_000_000
	policy.StartingOutputUnits = 0
	policy.InputAmount = 1_000_000
	prices := []uint64{17_000_000, 17_000_000, 17_000_000}

	result, err := ReplayRoundTrip(policy, prices,
		func(_ uint64, _ bool, amount uint64) (Quote, error) {
			return Quote{InputAmount: amount, EstimatedOutput: 1, MinimumOutput: 1}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts.Refused == 0 {
		t.Fatal("no leg was refused, so this test is not exercising the path it names")
	}
	if result.Counts.Buys != 0 {
		t.Errorf("a refused leg was counted as a buy: %+v", result.Counts)
	}
	// The book must be exactly as it opened. Zero here is the bug.
	if result.Ledger.QuoteUnits != policy.StartingInputUnits {
		t.Fatalf("a refused leg changed the book: %d quote units, opened with %d",
			result.Ledger.QuoteUnits, policy.StartingInputUnits)
	}
	if result.Ledger.RealizedMicros != 0 || result.Ledger.Fills != 0 {
		t.Errorf("a refused leg was accounted for: realized=%d fills=%d",
			result.Ledger.RealizedMicros, result.Ledger.Fills)
	}
}
