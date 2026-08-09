package swaprun

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

const stoppedAdvisorySlot = 4_242

func stoppedPriceEngine(t *testing.T, trigger *priceTriggerStub, now time.Time) *Engine {
	t.Helper()
	profile := testProfile()
	engine, err := New(
		testStore(t),
		&observerStub{observation: healthyObservation(profile, now)},
		quoteStub{result: swapQuote(profile)},
		blockhashStub{latest: solanarpc.LatestBlockhash{
			ContextSlot:          stoppedAdvisorySlot,
			Blockhash:            solana.Encode(bytes.Repeat([]byte{7}, 32)),
			LastValidBlockHeight: 250,
		}, height: 100},
		authorityStub{}, signerStub{}, &submitterStub{}, &transactorStub{},
		&stopStub{stopped: true}, func() time.Time { return now },
		WithPriceTrigger(trigger),
	)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

// A stopped agent is exactly the operator who needs to be told their price
// target was reached: they are not armed, and that alert exists to tell them to
// arm. Passing slot 0 made the default on-chain feed refuse the read outright,
// so PriceTrigger.Available was permanently false while stopped. That killed
// three things at once — MithrilAgentPriceTargetReached requires stopped AND
// available simultaneously, previously impossible; the notify-only
// price-above/below slots read the same field; and EvidenceAvailable pinned
// false so MithrilAgentAlertEvidenceUnavailable fired forever.
//
// The read must be bound to a real slot, but a CHEAP one: an observation spawns
// a node subprocess and a stopped agent can idle for days.
func TestStoppedAgentReadsPriceAtARealSlot(t *testing.T) {
	profile := testProfile()
	policy := testPriceTriggerPolicy()
	profile.PriceTrigger = &policy
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()

	trigger := &priceTriggerStub{evidence: testPriceEvidence(t, policy, now, 149_000_000)}
	result, err := stoppedPriceEngine(t, trigger, now).RunOnce(t.Context(), profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != "stopped" {
		t.Fatalf("decision = %q, want stopped", result.Decision)
	}
	if len(trigger.slots) == 0 {
		t.Fatal("a stopped agent never read the price at all")
	}
	for index, slot := range trigger.slots {
		if slot != stoppedAdvisorySlot {
			t.Fatalf("price read %d used slot %d, want the blockhash context slot %d",
				index, slot, stoppedAdvisorySlot)
		}
	}
	if result.PriceTrigger == nil || !result.PriceTrigger.Available {
		t.Fatalf("stopped price status = %+v, want an available reading", result.PriceTrigger)
	}
}

// When the feed itself cannot answer, the status must degrade to a VALID
// unavailable reading rather than a zero value: a zero Status fails
// ValidateStatus, and writing one previously exited the process.
func TestStoppedPriceDegradesToAValidUnavailableStatus(t *testing.T) {
	profile := testProfile()
	policy := testPriceTriggerPolicy()
	profile.PriceTrigger = &policy
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()

	trigger := &priceTriggerStub{err: errors.New("feed is unreachable")}
	result, err := stoppedPriceEngine(t, trigger, now).RunOnce(t.Context(), profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != "stopped" {
		t.Fatalf("decision = %q, want stopped", result.Decision)
	}
	if result.PriceTrigger == nil {
		t.Fatal("a stopped agent reported no price status at all")
	}
	if result.PriceTrigger.Available {
		t.Fatal("an unreadable price was reported as available")
	}
	if err := pricetrigger.ValidateStatus(*result.PriceTrigger); err != nil {
		t.Fatalf("stopped price status is not a valid status: %v", err)
	}
}
