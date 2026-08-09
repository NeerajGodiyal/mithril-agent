package main

import "testing"

// The route floor is otherwise pinned to the exact quote seen at setup, so the
// price impact of the agent's own trade disqualifies every later quote. Live on
// Devnet a single 0.001 SOL sell moved the floor one base unit under and the
// runner then refused 25 windows in a row.
func TestRelaxRouteFloorGivesRoomForAdverseDrift(t *testing.T) {
	const discovered = 21_314
	if got, err := relaxRouteFloor(discovered, 0); err != nil || got != discovered {
		t.Fatalf("zero tolerance must not move the floor: %d, %v", got, err)
	}
	relaxed, err := relaxRouteFloor(discovered, 500)
	if err != nil {
		t.Fatal(err)
	}
	if relaxed >= discovered {
		t.Fatalf("relaxed floor %d must sit below the discovered %d", relaxed, discovered)
	}
	// 5% of 21314 is ~1066, so a one-unit adverse tick is comfortably absorbed
	// where a zero-tolerance floor would refuse.
	if want := uint64(20_248); relaxed != want {
		t.Errorf("relaxed = %d, want %d", relaxed, want)
	}
	if discovered-1 < relaxed {
		t.Error("a one-unit adverse move should still clear the relaxed floor")
	}
}

func TestRelaxRouteFloorRefusesNonsense(t *testing.T) {
	if _, err := relaxRouteFloor(21_314, 10_000); err == nil {
		t.Error("a 100 percent tolerance erases the floor and must be refused")
	}
	if _, err := relaxRouteFloor(1, 9_999); err == nil {
		t.Error("a tolerance that rounds the floor to zero must be refused")
	}
}
