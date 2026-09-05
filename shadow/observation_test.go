package shadow

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

func TestObservationFailureCancelsPeerAndStopsLaterPairs(t *testing.T) {
	for _, failedRole := range []string{"primary", "secondary"} {
		t.Run(failedRole, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			peerStarted := make(chan struct{})
			peerStopped := make(chan error, 1)
			peer := &pairedSource{read: func(ctx context.Context, _ string) (pricetrigger.Sample, error) {
				close(peerStarted)
				<-ctx.Done()
				peerStopped <- ctx.Err()
				return pricetrigger.Sample{}, ctx.Err()
			}}
			failed := &pairedSource{read: func(ctx context.Context, _ string) (pricetrigger.Sample, error) {
				select {
				case <-peerStarted:
					return pricetrigger.Sample{}, errors.New("provider unavailable")
				case <-ctx.Done():
					return pricetrigger.Sample{}, ctx.Err()
				}
			}}
			var laterReads atomic.Uint32
			later := &pairedSource{read: func(context.Context, string) (pricetrigger.Sample, error) {
				laterReads.Add(1)
				return pricetrigger.Sample{}, errors.New("later pair must not run")
			}}
			runner := &Runner{policy: jupBuyPolicy(t), primary: failed, secondary: peer,
				quotePrimary: later, quoteSecondary: later, nativePrimary: later, nativeSecondary: later}
			if failedRole == "secondary" {
				runner.primary, runner.secondary = peer, failed
			}
			got := runner.Observe(ctx)
			if got != (Observation{unavailable: ReasonMarketPriceUnavailable}) || laterReads.Load() != 0 {
				t.Fatalf("failed pair leaked a snapshot or reached later pairs: %+v, reads=%d", got, laterReads.Load())
			}
			select {
			case err := <-peerStopped:
				if !errors.Is(err, context.Canceled) || ctx.Err() != nil {
					t.Fatalf("peer was not canceled by pair failure: peer=%v parent=%v", err, ctx.Err())
				}
			case <-ctx.Done():
				t.Fatal("failed pair left its peer running")
			}
		})
	}
}

func TestObservationAlreadyCanceledStartsNoReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var reads atomic.Uint32
		source := &pairedSource{read: func(context.Context, string) (pricetrigger.Sample, error) {
			reads.Add(1)
			return pricetrigger.Sample{}, errors.New("canceled request must not read")
		}}
		runner := &Runner{policy: jupBuyPolicy(t), primary: source, secondary: source,
			quotePrimary: source, quoteSecondary: source, nativePrimary: source, nativeSecondary: source}
		got := runner.Observe(ctx)
		// Let any incorrectly launched worker finish before checking the count.
		synctest.Wait()
		if got != (Observation{unavailable: ReasonMarketPriceUnavailable}) || reads.Load() != 0 {
			t.Fatalf("already-canceled observation started reads: %+v, reads=%d", got, reads.Load())
		}
	})
}

func TestObservationJoinsCanceledPeerBeforeReturning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		var cleaned atomic.Bool
		peer := &pairedSource{read: func(ctx context.Context, _ string) (pricetrigger.Sample, error) {
			close(started)
			<-ctx.Done()
			// A canceled reader can still be releasing transport or rate-gate
			// resources. Fake time makes this ordering assertion deterministic.
			time.Sleep(time.Second)
			cleaned.Store(true)
			return pricetrigger.Sample{}, ctx.Err()
		}}
		failed := &pairedSource{read: func(context.Context, string) (pricetrigger.Sample, error) {
			<-started
			return pricetrigger.Sample{}, errors.New("provider unavailable")
		}}
		runner := &Runner{policy: jupBuyPolicy(t), primary: failed, secondary: peer}
		got := runner.Observe(t.Context())
		// Do not wait here: completion must already hold when Observe returns.
		if got != (Observation{unavailable: ReasonMarketPriceUnavailable}) || !cleaned.Load() {
			t.Fatalf("Observe returned before canceled reader cleanup: %+v, cleaned=%v", got, cleaned.Load())
		}
	})
}
