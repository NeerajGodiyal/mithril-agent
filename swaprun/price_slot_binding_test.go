package swaprun

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/clockcheck"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

// slotRequiringTrigger behaves like the real slot-bound sources: PythPush
// refuses an unproved slot (pricesource/pythpush.go), and the RPC layer beneath
// it refuses one again (solanarpc/client.go). The engine's own interface says
// so too: "Passing zero means no slot was proved and such a source must
// refuse." Every stub in engine_test.go ignores the slot, which is why a
// pre-start read that passed zero could never start an action in production
// while every test still passed.
type slotRequiringTrigger struct {
	evidence pricetrigger.Evidence
	slots    []uint64
}

// Evaluate is the unbound form. A slot-bound source refuses it outright, which
// is what makes the engine's choice of read the thing under test.
func (s *slotRequiringTrigger) Evaluate(
	ctx context.Context, policy pricetrigger.Policy,
) (pricetrigger.Evidence, error) {
	return s.EvaluateAtSlot(ctx, policy, 0)
}

func (s *slotRequiringTrigger) EvaluateAtSlot(
	_ context.Context, _ pricetrigger.Policy, minContextSlot uint64,
) (pricetrigger.Evidence, error) {
	s.slots = append(s.slots, minContextSlot)
	if minContextSlot == 0 {
		return pricetrigger.Evidence{}, errors.New("authorizing read requires a proven context slot")
	}
	return s.evidence, nil
}

// A configured price trigger must be able to start an action when the only
// available source refuses an unproved slot.
func TestPriceTriggerStartsWithSlotRequiringSource(t *testing.T) {
	profile := testProfile()
	policy := testPriceTriggerPolicy()
	policy.ThresholdMicros = 20_000_000
	profile.PriceTrigger = &policy
	start := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	now := start
	store := testStore(t)
	observer := &observerStub{observation: healthyObservation(profile, now)}
	trigger := &slotRequiringTrigger{
		evidence: testPriceEvidence(t, policy, now, 25_000_000),
	}
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
	if _, err := engine.RunOnce(t.Context(), profile); err != nil {
		t.Fatalf("second cycle: %v", err)
	}

	for _, slot := range trigger.slots {
		if slot == 0 {
			t.Fatalf("a read passed an unproved slot, which such a source must refuse: %v", trigger.slots)
		}
	}
	started := false
	for _, record := range store.Records() {
		if record.Type == EventStarted {
			started = true
		}
	}
	if !started {
		t.Fatalf("a triggered price never started an action; reads: %v", trigger.slots)
	}
}
