package jupiterquote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

type requestGateFunc func(context.Context) error

func (wait requestGateFunc) Wait(ctx context.Context) error { return wait(ctx) }

func TestMemoryRequestGateReservesRequests(t *testing.T) {
	gate := &memoryRequestGate{spacing: time.Hour}
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := gate.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second wait = %v, want deadline", err)
	}
}

func TestRequestGateUsesDocumentedTierSpacing(t *testing.T) {
	keyless := newRequestGate("", false).(*memoryRequestGate)
	keyed := newRequestGate("", true).(*memoryRequestGate)
	if keyless.spacing != keylessRequestSpacing || keyed.spacing != freeKeyRequestSpacing ||
		keyless.spacing <= keyed.spacing {
		t.Fatalf("request spacing = keyless %s, keyed %s", keyless.spacing, keyed.spacing)
	}
}

func TestClientFetchWaitsForTheRequestGate(t *testing.T) {
	called := 0
	client, err := newClient("http://jupiter.test/build", "", &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if called != 1 {
				t.Fatal("request reached Jupiter before reserving the shared gate")
			}
			body := []byte(`{"inputMint":"` + testInputMint +
				`","outputMint":"` + testOutputMint +
				`","inAmount":"1000000","outAmount":"150000","otherAmountThreshold":"149250",` +
				`"swapMode":"ExactIn","slippageBps":50,"routePlan":[{"bps":10000}]}`)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
				Request:    request,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	client.gate = requestGateFunc(func(context.Context) error {
		called++
		return nil
	})
	_, err = client.Quote(t.Context(), Request{
		Taker: testTaker, InputMint: testInputMint, OutputMint: testOutputMint,
		InputAmount: 1_000_000, SlippageBPS: 50,
	})
	if err != nil || called != 1 {
		t.Fatalf("quote gate calls = %d, error = %v", called, err)
	}
}

func TestCancelledMemoryRequestDoesNotAdvanceTheGate(t *testing.T) {
	gate := &memoryRequestGate{spacing: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gate.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait = %v", err)
	}
	if !gate.next.IsZero() {
		t.Fatalf("cancelled request advanced gate to %s", gate.next)
	}
}

func TestQueuedCancellationDoesNotReserveAnotherSlot(t *testing.T) {
	gate := &memoryRequestGate{spacing: time.Hour}
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	reserved := gate.next
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := gate.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued wait = %v, want deadline", err)
	}
	if !gate.next.Equal(reserved) {
		t.Fatalf("cancelled queued request advanced gate from %s to %s", reserved, gate.next)
	}
}
