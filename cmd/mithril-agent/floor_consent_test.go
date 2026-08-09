package main

import (
	"errors"
	"testing"
)

// The floor tolerance was once applied AFTER the confirmation gate, so the
// operator agreed to 21312 while the policy received 20246 — the one gate whose
// entire purpose is that a human agrees to the exact number that gets signed.
//
// Testing relaxRouteFloor alone cannot catch that: the arithmetic was never
// wrong, the call order was. So this drives the caller and asserts the only
// property that matters — what is shown is what is returned to be written.
func TestOperatorAgreesToTheFloorThatIsWritten(t *testing.T) {
	for _, tolerance := range []uint16{0, 1, 250, 500, 2_000} {
		var shown quoteConfirmation
		options := swapSetupOptions{
			floorToleranceBPS: tolerance,
			slippageBPS:       100,
			confirmQuote: func(q quoteConfirmation) error {
				shown = q
				return nil
			},
		}
		written, err := options.agreeOnFloor(21_312, "sell", "0.001 SOL", "devUSDC", 6)
		if err != nil {
			t.Fatalf("tolerance %d: %v", tolerance, err)
		}
		if shown.MinOutput != written {
			t.Errorf(
				"tolerance %d: operator agreed to %d but the policy would record %d",
				tolerance, shown.MinOutput, written,
			)
		}
		// The printed text is the only form most operators actually read, so it
		// has to carry the same number as the field.
		if want := formatUnits(written, 6) + " devUSDC"; shown.OutputText != want {
			t.Errorf("tolerance %d: shown text %q, want %q", tolerance, shown.OutputText, want)
		}
		if tolerance > 0 && written >= 21_312 {
			t.Errorf("tolerance %d did not lower the floor: %d", tolerance, written)
		}
	}
}

// A refused confirmation must abandon the setup rather than fall through to a
// floor the operator declined.
func TestDeclinedConfirmationWritesNoFloor(t *testing.T) {
	refused := errors.New("operator declined")
	options := swapSetupOptions{
		floorToleranceBPS: 500,
		confirmQuote:      func(quoteConfirmation) error { return refused },
	}
	written, err := options.agreeOnFloor(21_312, "sell", "0.001 SOL", "devUSDC", 6)
	if !errors.Is(err, refused) {
		t.Fatalf("declined confirmation = %v, want the refusal", err)
	}
	if written != 0 {
		t.Errorf("declined confirmation still produced a floor: %d", written)
	}
}

// The numeric form of the gate compares against the relaxed floor, so an
// operator passing the pre-tolerance number they saw in `swap discover` is
// refused rather than silently given a lower floor.
func TestNumericConfirmationComparesTheRelaxedFloor(t *testing.T) {
	options := swapSetupOptions{floorToleranceBPS: 500, confirmedMinOut: 21_312}
	if _, err := options.agreeOnFloor(21_312, "sell", "0.001 SOL", "devUSDC", 6); err == nil {
		t.Error("stale pre-tolerance confirmation was accepted")
	}
	relaxed, err := relaxRouteFloor(21_312, 500)
	if err != nil {
		t.Fatal(err)
	}
	options.confirmedMinOut = relaxed
	written, err := options.agreeOnFloor(21_312, "sell", "0.001 SOL", "devUSDC", 6)
	if err != nil || written != relaxed {
		t.Errorf("matching confirmation = %d, %v; want %d", written, err, relaxed)
	}
}
