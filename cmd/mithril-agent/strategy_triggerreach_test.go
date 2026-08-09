package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

const (
	reachPrimarySource   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	reachSecondarySource = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func sample(source string, priceMicros, confidenceMicros uint64) pricetrigger.Sample {
	return pricetrigger.Sample{
		SourceSHA256: source, Feed: pricetrigger.FeedSOLUSD, PriceMicros: priceMicros,
		ConfidenceMicros: confidenceMicros, PublishedAt: time.Unix(1_700_000_000, 0).UTC(),
	}
}

// The advice must agree with the evaluator it describes, or it is worse than
// nothing: an operator would configure against one rule and be judged by
// another. This checks reachOf against pricetrigger.Evaluate itself rather
// than against a second copy of the comparison written here.
func TestReachAgreesWithTheEvaluatorItDescribes(t *testing.T) {
	primary, secondary := sample(reachPrimarySource, 73_370_000, 30_000), sample(reachSecondarySource, 73_400_000, 25_000)
	for _, threshold := range []uint64{
		1_000_000, 18_500_000, 20_500_000, 73_000_000, 73_339_999, 73_340_000,
		// Inside the confidence band, where the conservative bound and the raw
		// mid price disagree. Without these the test passes even if the advice
		// drops the confidence adjustment the evaluator applies.
		73_350_000, 73_370_000, 73_400_000, 73_410_000, 73_424_999,
		73_425_000, 80_000_000, 200_000_000,
	} {
		for _, direction := range []pricetrigger.Direction{
			pricetrigger.SellAtOrAbove, pricetrigger.BuyAtOrBelow,
		} {
			policy := pricetrigger.Policy{
				Version: pricetrigger.Version, Feed: pricetrigger.FeedSOLUSD,
				Direction: direction, ThresholdMicros: threshold,
				MaxAgeSeconds: 60, MaxSourceSkewSeconds: 5,
				MaxDeviationBPS: 100, MaxConfidenceBPS: 100,
				PrimarySourceSHA256:   primary.SourceSHA256,
				SecondarySourceSHA256: secondary.SourceSHA256,
			}
			evidence, err := pricetrigger.Evaluate(
				policy, primary, secondary, time.Unix(1_700_000_000, 0).UTC())
			if err != nil {
				t.Fatalf("threshold %d %v: %v", threshold, direction, err)
			}
			got := reachOf(direction, primary, secondary, threshold)
			if got.FiresNow != evidence.Triggered {
				t.Errorf(
					"threshold %d direction %v: advice says firesNow=%v, the evaluator says %v",
					threshold, direction, got.FiresNow, evidence.Triggered)
			}
		}
	}
}

// The exact configuration that produced one Telegram message and silence: a
// sell the feed always satisfies and a buy it never can. Setup printed an
// estimated gain per round trip for it.
func TestAdviceNamesAConfigurationThatCompletesNoRoundTrip(t *testing.T) {
	primary, secondary := sample(reachPrimarySource, 73_370_000, 30_000), sample(reachSecondarySource, 73_400_000, 25_000)
	sell := reachOf(pricetrigger.SellAtOrAbove, primary, secondary, 20_500_000)
	buy := reachOf(pricetrigger.BuyAtOrBelow, primary, secondary, 18_500_000)
	if !sell.FiresNow {
		t.Error("a sell threshold far below the feed was not reported as firing now")
	}
	if buy.FiresNow {
		t.Error("a buy threshold far below the feed was reported as firing now")
	}
	// The operator needs the size of the gap, not just that one exists: "waits"
	// alone cannot distinguish an hour from never.
	if buy.MovePercent < 70 || buy.MovePercent > 80 {
		t.Errorf("the required fall reads %.1f%%, want roughly 75%%", buy.MovePercent)
	}
}

// A threshold the feed already satisfies must not be reported as needing a move,
// and one it does not must not be reported as firing. Both directions.
func TestReachReportsBothDirectionsHonestly(t *testing.T) {
	primary, secondary := sample(reachPrimarySource, 73_370_000, 30_000), sample(reachSecondarySource, 73_400_000, 25_000)
	if reach := reachOf(pricetrigger.BuyAtOrBelow, primary, secondary, 80_000_000); !reach.FiresNow {
		t.Error("a buy threshold above the feed should fire immediately")
	}
	if reach := reachOf(pricetrigger.SellAtOrAbove, primary, secondary, 80_000_000); reach.FiresNow {
		t.Error("a sell threshold above the feed should not fire")
	} else if reach.MovePercent < 8 || reach.MovePercent > 10 {
		t.Errorf("the required rise reads %.1f%%, want roughly 9%%", reach.MovePercent)
	}
}

// Market mode has no thresholds to place against a feed, and reading one would
// be a network call for nothing.
func TestMarketModeGetsNoPriceAdvice(t *testing.T) {
	if text := describeTriggerReach(t.Context(), strategyPlan{atMarket: true}); text != "" {
		t.Errorf("market mode produced price advice:\n%s", text)
	}
}

// The advice is advisory. An unreachable oracle must degrade to a note, never
// stop a strategy being written — setup must not acquire a live dependency.
func TestAnUnreadableFeedDoesNotBlockSetup(t *testing.T) {
	t.Setenv("MITHRIL_AGENT_PRIMARY_RPC_URL", "")
	text := describeTriggerReach(t.Context(), strategyPlan{
		sellAtMicros: 20_500_000, buyAtMicros: 18_500_000,
	})
	if !strings.Contains(text, "Could not read the live SOL/USD feed") {
		t.Errorf("an unreadable feed was not reported:\n%s", text)
	}
	if !strings.Contains(text, "still written") {
		t.Errorf("the note does not say the legs are still written:\n%s", text)
	}
}

// A Pyth confidence blow-out — confidence at or above the price — is exactly
// what pricetrigger.Evaluate rejects via validateSample BEFORE it subtracts.
// This function copied the subtraction and not the validation, so the unsigned
// arithmetic wrapped to ~1.8e19 and every threshold read "fires now": the
// operator was told the leg was about to fill at the moment the runner would
// refuse the very same reading as invalid evidence.
func TestAWideConfidenceReadingIsNotReportedAsFiring(t *testing.T) {
	// Confidence >= price, the shape validateSample refuses.
	primary := sample(reachPrimarySource, 73_370_000, 73_370_000)
	secondary := sample(reachSecondarySource, 73_400_000, 80_000_000)

	for _, direction := range []pricetrigger.Direction{
		pricetrigger.SellAtOrAbove, pricetrigger.BuyAtOrBelow,
	} {
		reach := reachOf(direction, primary, secondary, 20_500_000)
		if reach.FiresNow {
			t.Errorf("%v: unusable evidence was reported as firing", direction)
		}
		if !reach.Unusable {
			t.Errorf("%v: unusable evidence was not marked unusable: %+v", direction, reach)
		}
	}

	// And the operator is told the reading says nothing, rather than being given
	// a confident number derived from a wrapped subtraction.
	if got := describeReach(reachOf(pricetrigger.SellAtOrAbove, primary, secondary, 20_500_000), "rise"); !strings.Contains(got, "cannot be judged") {
		t.Errorf("the line does not say the reading cannot be judged: %q", got)
	}
}
