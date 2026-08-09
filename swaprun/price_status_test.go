package swaprun

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/clockcheck"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

// A price-source outage is routine, and the runner writes whatever status the
// result carries into operator status. A status that fails ValidateStatus makes
// that write fail, which ends the runner process — so an outage would take the
// agent down rather than leave it idle. The stopped path already substitutes
// pricetrigger.Unavailable for exactly this reason.
func TestPriceOutageResultCarriesValidStatus(t *testing.T) {
	profile := testProfile()
	triggerPolicy := testPriceTriggerPolicy()
	profile.PriceTrigger = &triggerPolicy
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	store := testStore(t)
	observer := &observerStub{observation: healthyObservation(profile, now)}
	trigger := &priceTriggerStub{err: errors.New("primary price source is unavailable")}
	engine, err := New(
		store, observer, quoteStub{result: swapQuote(profile)},
		blockhashStub{latest: solanarpc.LatestBlockhash{
			ContextSlot: 101, Blockhash: solana.Encode(bytes.Repeat([]byte{7}, 32)),
			LastValidBlockHeight: 250,
		}, height: 100}, authorityStub{}, signerStub{}, &submitterStub{},
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
			MonotonicNanos:   monotonic,
			UncertaintyNanos: uint64(10 * time.Millisecond),
		}, nil
	}

	result, err := engine.RunOnce(t.Context(), profile)
	if err != nil {
		t.Fatalf("a price outage must not be a hard error: %v", err)
	}
	if result.PriceTrigger == nil {
		t.Fatal("a price-triggered profile must report a trigger status")
	}
	if err := pricetrigger.ValidateStatus(*result.PriceTrigger); err != nil {
		t.Fatalf("status would fail the operator-status write and end the runner: %v", err)
	}
	if result.PriceTrigger.Available {
		t.Error("an outage must not report the trigger as available")
	}
}
