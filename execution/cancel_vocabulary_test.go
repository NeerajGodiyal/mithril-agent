package execution

import "testing"

// authorizeUnsent produces a reason; the two call sites suffix it and hand it
// to cancel (pre-signing) or quarantine (pre-submission). cancel does not
// validate before appending, so a reason the replay vocabulary does not know
// is written DURABLY and then rejected on every subsequent replay — the
// journal becomes unreadable and the setup is unrecoverable without deleting
// its own audit trail.
//
// Enumerating the producer's outputs and asserting both consumers accept them
// catches the whole class, not just the one reason that was missing.
func TestEveryUnsentReasonSurvivesReplay(t *testing.T) {
	// Every value authorizeUnsent can return. Keep in step with it.
	for _, reason := range []string{
		"mithril_health_changed",
		"observation_expired",
		"operator_stop",
		"reservation_day_expired",
		"schedule_window_expired",
		"utc_rollover_guard",
	} {
		if !validCancelReason(reason + "_before_signing") {
			t.Errorf("cancel would write %q_before_signing and then fail its own replay", reason)
		}
		if !validQuarantineReason(reason + "_before_submission") {
			t.Errorf("quarantine refuses %q_before_submission, stranding a signed transaction", reason)
		}
	}
}

// The other producers of cancel reasons, so the vocabulary stays complete.
func TestDirectCancelReasonsAreKnown(t *testing.T) {
	for _, reason := range []string{
		"observation_expired_before_build",
		"node_lag_exceeded_before_build",
		"fee_exceeds_profile",
		"schedule_window_expired_before_build",
		"balance_changed_before_signing",
		"balance_changed_before_submission",
		"blockhash_expired_before_signing",
		"blockhash_expired_before_submission",
	} {
		if !validCancelReason(reason) {
			t.Errorf("%q is written by the engine but rejected on replay", reason)
		}
	}
}

// A reason outside the vocabulary must be refused at WRITE time. Refusing only
// at replay converts a coding slip into permanent data corruption.
func TestCancelRefusesAnUnknownReasonBeforeWriting(t *testing.T) {
	if validCancelReason("invented_reason_before_signing") {
		t.Fatal("the vocabulary accepts an invented reason")
	}
}
