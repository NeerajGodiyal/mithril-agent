package swaprun

import (
	"bytes"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/clockcheck"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

// A price rule spends most of its life declining to trade. Deciding that after
// the transaction was built cost a blockhash, a block height, deployment and
// rent evidence, a fee quote against BOTH evidence providers, a simulation, and
// two journal records — every window — and then left a transaction to sit until
// its blockhash expired and it was canceled. The executable minimum is a pure
// function of the quote and the operator's threshold, so it is decided against
// the quote instead.
func TestPriceWaitDoesNotBuildOrSimulate(t *testing.T) {
	profile := testProfile()
	policy := testPriceTriggerPolicy()
	// Above what the quote can execute at, but below the oracle price, so the
	// oracle condition passes and only the executable condition declines.
	policy.ThresholdMicros = 22_000_000
	profile.PriceTrigger = &policy
	start := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	now := start
	store := testStore(t)
	observer := &observerStub{observation: healthyObservation(profile, now)}
	trigger := &priceTriggerStub{evidence: testPriceEvidence(t, policy, now, 25_000_000)}
	engine, err := New(
		store, observer, quoteStub{result: swapQuote(profile)},
		blockhashStub{latest: solanarpc.LatestBlockhash{
			ContextSlot: 101, Blockhash: solana.Encode(bytes.Repeat([]byte{7}, 32)),
			LastValidBlockHeight: 250,
		}, height: 100}, authorityStub{}, signerStub{valid: true}, &submitterStub{},
		&transactorStub{}, &stopStub{}, func() time.Time { return now },
		WithPriceTrigger(trigger),
	)
	if err != nil {
		t.Fatal(err)
	}
	monotonic := uint64(time.Second)
	engine.clock = func() (clockcheck.Sample, error) {
		monotonic++
		return clockcheck.Sample{
			WallTime: now, BootID: "00000000-0000-0000-0000-000000000001",
			MonotonicNanos:   monotonic + uint64(now.Sub(start)),
			UncertaintyNanos: uint64(10 * time.Millisecond),
		}, nil
	}

	if _, err := engine.RunOnce(t.Context(), profile); err != nil {
		t.Fatalf("opening cycle: %v", err)
	}
	now = start.Add(6 * time.Second)
	observer.observation = healthyObservation(profile, now)
	observer.observation.Account.Slot++
	trigger.evidence = testPriceEvidence(t, policy, now, 25_000_000)
	result, err := engine.RunOnce(t.Context(), profile)
	if err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	if result.Decision != "waiting" {
		t.Fatalf("decision = %q, want waiting", result.Decision)
	}
	if result.PriceTrigger == nil || result.PriceTrigger.ExecutableCondition {
		t.Fatalf("expected the executable condition to decline: %+v", result.PriceTrigger)
	}
	// The status must survive the early return, or a configured rule reads as
	// absent to metrics and to the operator on every waiting cycle.
	if !result.PriceTrigger.ConditionMet {
		t.Error("the oracle condition should still be reported as met")
	}
	for _, record := range store.Records() {
		if record.Type == EventBuilt || record.Type == EventSimulated {
			t.Fatalf("a declined window still wrote %q", record.Type)
		}
	}
}
