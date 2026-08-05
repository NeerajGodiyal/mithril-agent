package pricesource

import (
	"os"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

func TestLiveSOLUSDPriceSources(t *testing.T) {
	if os.Getenv("MITHRIL_AGENT_LIVE_PRICE_TEST") != "1" {
		t.Skip("set MITHRIL_AGENT_LIVE_PRICE_TEST=1 for public source smoke test")
	}
	pyth, err := NewPyth(nil, os.Getenv("MITHRIL_AGENT_PYTH_API_KEY"))
	if err != nil {
		t.Skip("set MITHRIL_AGENT_PYTH_API_KEY for authenticated Pyth smoke test")
	}
	evaluator, err := pricetrigger.NewEvaluator(pyth, NewCoinbase(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := evaluator.Evaluate(t.Context(), pricetrigger.Policy{
		Version: pricetrigger.Version, Feed: pricetrigger.FeedSOLUSD,
		Direction: pricetrigger.SellAtOrAbove, ThresholdMicros: 1,
		MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90,
		MaxDeviationBPS: 200, MaxConfidenceBPS: 200,
		PrimarySourceSHA256:   PythIdentitySHA256(),
		SecondarySourceSHA256: CoinbaseIdentitySHA256(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Triggered || evidence.ObservedAt.Before(time.Now().UTC().Add(-30*time.Second)) {
		t.Fatalf("live evidence is not current and triggered")
	}
}

// TestLivePythPushMatchesCoinbase proves the no-subscription baseline against
// the real sponsored accounts. It records agreement within a band rather than
// asserting equality: two independent sources sampled at different instants
// legitimately differ.
func TestLivePythPushMatchesCoinbase(t *testing.T) {
	endpoint := os.Getenv("MITHRIL_AGENT_LIVE_SOLANA_RPC")
	if os.Getenv("MITHRIL_AGENT_LIVE_PRICE_TEST") != "1" || endpoint == "" {
		t.Skip("set MITHRIL_AGENT_LIVE_PRICE_TEST=1 and MITHRIL_AGENT_LIVE_SOLANA_RPC for the on-chain push smoke test")
	}
	push, err := NewPythPush(liveAccountReader{endpoint: endpoint}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	pushSample, err := push.LatestAtSlot(t.Context(), pricetrigger.FeedSOLUSD, 1)
	if err != nil {
		t.Fatalf("on-chain push read failed: %v", err)
	}
	coinbaseSample, err := NewCoinbase(nil).Latest(t.Context(), pricetrigger.FeedSOLUSD)
	if err != nil {
		t.Fatalf("Coinbase read failed: %v", err)
	}

	age := time.Since(pushSample.PublishedAt)
	t.Logf("push=%d micros age=%s conf=%d micros; coinbase=%d micros",
		pushSample.PriceMicros, age.Round(time.Second),
		pushSample.ConfidenceMicros, coinbaseSample.PriceMicros)

	// The sponsored heartbeat is about one minute, so a healthy feed can
	// legitimately be that old; only reject clearly dead evidence here.
	if age > 150*time.Second {
		t.Fatalf("on-chain push price is %s old", age.Round(time.Second))
	}
	if err := requireCloseEnough(pushSample.PriceMicros, coinbaseSample.PriceMicros, 200); err != nil {
		t.Fatalf("independent sources disagree beyond 200bps: %v", err)
	}
}
