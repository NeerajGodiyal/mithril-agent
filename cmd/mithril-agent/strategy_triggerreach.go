package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

// Setup checked an operator's prices against the POOL and never against the
// ORACLE — and the trigger fires on the oracle.
//
// Those are the same number on a liquid market and wildly different on Devnet,
// where the pool is unarbitraged toy liquidity: the oracle read $73.37/SOL
// while the pool paid $20.71. A config of sell-at-$20.50 / buy-at-$18.50 was
// therefore accepted, printed an "estimated gain per round trip", and could
// never produce one — the sell fired on every single cycle because $73 is
// always above $20.50, and the buy could not fire at all because $73 is never
// below $18.50. The operator got one trade and silence, with nothing anywhere
// saying why.
//
// This does NOT refuse. A buy far below spot is exactly what a limit order is,
// and blocking it would break the feature. What was missing is the one fact an
// operator cannot obtain for themselves: where their thresholds sit against the
// feed that will actually decide, at the moment they are choosing them.
//
// It is advisory, so every failure to read a price degrades to a printed note.
// Setup must not start depending on a live oracle to write a profile.

// triggerReach is where one threshold sits against the live conservative price
// the evaluator would compute for that direction.
type triggerReach struct {
	// Unusable means the evaluator would reject this evidence outright, so no
	// statement about the threshold can be made from it.
	Unusable bool
	// FiresNow is what the evaluator would decide on this reading.
	FiresNow bool
	// MovePercent is how far the price must travel, as a percentage of the
	// current price, before it could fire. Zero when it already fires.
	MovePercent float64
}

// reachOf applies the SAME comparison pricetrigger.Evaluate applies, including
// the conservative confidence adjustment, so the advice cannot disagree with
// the runner it is describing.
//
// Sell uses min(price - confidence) and fires at or above the threshold; buy
// uses max(price + confidence) and fires at or below it. Using the raw mid
// price here would quietly overstate how close a threshold is.
func reachOf(direction pricetrigger.Direction, primary, secondary pricetrigger.Sample, threshold uint64) triggerReach {
	// pricetrigger.Evaluate runs validateSample BEFORE this subtraction, and
	// validateSample rejects any sample whose confidence is not below its price.
	// This function reads its samples straight from the feeds, which check
	// staleness and sign but never that relationship — so without the same
	// guard the unsigned arithmetic below wraps to ~1.8e19 during a confidence
	// blow-out and EVERY threshold reads "fires now". That is not merely wrong,
	// it is inverted: the operator is told the leg is about to fill at the exact
	// moment the runner will refuse the reading as invalid evidence.
	if primary.ConfidenceMicros >= primary.PriceMicros ||
		secondary.ConfidenceMicros >= secondary.PriceMicros {
		return triggerReach{Unusable: true}
	}
	var conservative uint64
	if direction == pricetrigger.SellAtOrAbove {
		conservative = min(
			primary.PriceMicros-primary.ConfidenceMicros,
			secondary.PriceMicros-secondary.ConfidenceMicros,
		)
		if conservative >= threshold {
			return triggerReach{FiresNow: true}
		}
		return triggerReach{MovePercent: percentGap(conservative, threshold)}
	}
	conservative = max(
		primary.PriceMicros+primary.ConfidenceMicros,
		secondary.PriceMicros+secondary.ConfidenceMicros,
	)
	if conservative <= threshold {
		return triggerReach{FiresNow: true}
	}
	return triggerReach{MovePercent: percentGap(conservative, threshold)}
}

// percentGap is how far `from` must move to reach `to`, relative to `from`.
func percentGap(from, to uint64) float64 {
	if from == 0 {
		return 0
	}
	difference := float64(to) - float64(from)
	if difference < 0 {
		difference = -difference
	}
	return difference / float64(from) * 100
}

// describeTriggerReach reports where the configured thresholds sit against the
// live feed. It returns advisory text and never an error: a strategy must stay
// configurable when an oracle is unreachable.
func describeTriggerReach(ctx context.Context, plan strategyPlan) string {
	if plan.atMarket {
		return ""
	}
	primary, secondary, err := liveTriggerSamples(ctx)
	if err != nil {
		return fmt.Sprintf(
			"\nCould not read the live SOL/USD feed to check your prices against it (%v).\n"+
				"The legs are still written; the runner reads this feed itself every cycle.\n", err)
	}

	sell := reachOf(pricetrigger.SellAtOrAbove, primary, secondary, plan.sellAtMicros)
	buy := reachOf(pricetrigger.BuyAtOrBelow, primary, secondary, plan.buyAtMicros)

	var text strings.Builder
	fmt.Fprintf(&text,
		"\nYour prices against the SOL/USD feed the trigger uses ($%s now)\n"+
			"----------------------------------------------------------------------\n"+
			"(read here through your primary RPC; the runner reads the same Pyth\n"+
			"account through your own Mithril node, so treat this as guidance)\n",
		formatUnits(primary.PriceMicros, 6))
	fmt.Fprintf(&text, "  sell at $%-12s %s\n",
		formatUnits(plan.sellAtMicros, 6), describeReach(sell, "rise"))
	fmt.Fprintf(&text, "  buy  at $%-12s %s\n",
		formatUnits(plan.buyAtMicros, 6), describeReach(buy, "fall"))

	// The two shapes worth naming, because each looks like a working strategy
	// and behaves like something else.
	if sell.Unusable || buy.Unusable {
		text.WriteString("\nThe runner would refuse this reading as invalid evidence, so it says\nnothing about whether your prices are reachable. Try again shortly.\n")
		return text.String()
	}
	switch {
	case sell.FiresNow && !buy.FiresNow:
		fmt.Fprintf(&text,
			"\nAt today's prices this completes NO round trip: it sells on the next\n"+
				"cycle and then holds devUSDC until the price falls %.1f%%. The estimated\n"+
				"gain above assumes both legs fill. If you meant to trade the pool rather\n"+
				"than wait for the oracle, omit both prices to trade at market.\n",
			buy.MovePercent)
	case sell.FiresNow:
		fmt.Fprintf(&text,
			"\nThe sell fires on the next cycle — the feed is already above your\n"+
				"threshold, so this is a floor on the fill rather than a wait for a price.\n")
	}
	return text.String()
}

func describeReach(reach triggerReach, movement string) string {
	if reach.Unusable {
		return "cannot be judged — the feed's confidence is too wide to be usable evidence"
	}
	if reach.FiresNow {
		return "fires now — the feed already satisfies it"
	}
	return fmt.Sprintf("waits — the feed must %s %.1f%% before it can fire", movement, reach.MovePercent)
}

// liveTriggerSamples reads the same two sources the written profile names —
// the on-chain Pyth account and Kraken — so the advice is computed from the
// evidence the runner will be judged on.
//
// It reaches Pyth over the primary RPC, NOT through the operator's Mithril node
// the way the runner does. That is a deliberate trade: wiring the node here
// would make writing a profile depend on a running node, and the difference is
// a slot or two on the same account.
//
// It means a hostile RPC could skew this text. Nothing is signed, spent, or
// stored from it — the runner re-reads trust-minimised every cycle and decides
// for itself — so the worst case is misleading guidance, which is why the
// output says where the number came from rather than implying the node.
func liveTriggerSamples(ctx context.Context) (pricetrigger.Sample, pricetrigger.Sample, error) {
	endpoint := os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL")
	if endpoint == "" {
		return pricetrigger.Sample{}, pricetrigger.Sample{},
			fmt.Errorf("MITHRIL_AGENT_PRIMARY_RPC_URL is not set")
	}
	push, err := pricesource.NewPythPush(publicAccountReader(endpoint), time.Now)
	if err != nil {
		return pricetrigger.Sample{}, pricetrigger.Sample{}, err
	}
	// Bounded: an advisory line must never hang a setup.
	readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	primary, err := push.Latest(readCtx, pricetrigger.FeedSOLUSD)
	if err != nil {
		return pricetrigger.Sample{}, pricetrigger.Sample{}, err
	}
	secondary, err := pricesource.NewKrakenSOL(&http.Client{Timeout: 15 * time.Second}).
		Latest(readCtx, pricetrigger.FeedSOLUSD)
	if err != nil {
		return pricetrigger.Sample{}, pricetrigger.Sample{}, err
	}
	return primary, secondary, nil
}
