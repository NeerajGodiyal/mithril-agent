package shadow

import (
	"context"
	"errors"
	"testing"
)

func TestReadPricePairRequiresReadersAndHonorsCancellation(t *testing.T) {
	if _, _, err := ReadPricePair(t.Context(), nil, nil, "SOL/USD"); err == nil {
		t.Fatal("missing readers accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := ReadPricePair(ctx, &stubSource{}, &stubSource{}, "SOL/USD"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled price pair: %v", err)
	}
}
