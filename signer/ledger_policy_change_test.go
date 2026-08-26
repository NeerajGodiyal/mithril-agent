package signer

import (
	"strings"
	"testing"
	"time"
)

// Changing any cap changes the signer policy, and the ledger header binds the
// hash of that whole policy (authorization_ledger.go:205-223). So the ledger a
// setup accumulated its day's spend in becomes unusable the moment a cap moves.
//
// Combined with Profile.Fingerprint() marshalling the entire profile, an
// operator who NARROWS a cap mid-day must build a fresh setup, whose ledger
// starts at zero — handing them a brand-new full day's allowance. The safest
// action an operator can take is the one that temporarily increases exposure.
//
// This pins the current behaviour so the fix is provable rather than asserted.
func TestTighteningACapInvalidatesTheDaysLedger(t *testing.T) {
	policy, privateKey, request := signerFixture(t)
	policy.DailyDebitCapLamports = 10_000
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	if _, err := AuthorizeAndSign(policy, privateKey, request, now); err != nil {
		t.Fatalf("first authorization: %v", err)
	}

	// The operator decides the fee ceiling is too generous and lowers it —
	// strictly safer in every dimension. The ledger refuses the same file.
	tightened := policy
	tightened.MaxFeeLamports = policy.MaxFeeLamports / 2

	_, err := openAuthorizationLedger(tightened, now)
	if err == nil {
		t.Fatal("tightening a cap unexpectedly reused the ledger — behaviour changed, update this test and the fix")
	}
	// The refusal must separate "you edited a cap" from "this file is damaged".
	// Both used to arrive as "invalid or unavailable" — the one pair an
	// operator most needs told apart, because one is something they just did
	// deliberately and the other is a fault.
	if !strings.Contains(err.Error(), "a cap was edited") {
		t.Fatalf("refusal does not name the real cause: %v", err)
	}
	if strings.Contains(err.Error(), "invalid or unavailable") {
		t.Fatalf("a deliberate cap edit is still reported as corruption: %v", err)
	}
	// And it must state the consequence, which is the part an operator cannot
	// work out: the day's accumulated debit is unreachable, so a fresh ledger
	// begins at zero and the cap they just LOWERED governs a full, untouched
	// day. Tightening mid-day widens it.
	if !strings.Contains(err.Error(), "RAISES what today still allows") {
		t.Fatalf("refusal does not warn that tightening widens the day: %v", err)
	}
	if !strings.Contains(err.Error(), "00:00 UTC") {
		t.Fatalf("refusal does not say when it is safe to tighten: %v", err)
	}
}

// Loosening is the direction that increases exposure, and it is refused the
// same way. Recorded so a future fix that carries spend forward is explicit
// about treating the two directions differently.
func TestLooseningACapAlsoInvalidatesTheLedger(t *testing.T) {
	policy, privateKey, request := signerFixture(t)
	policy.DailyDebitCapLamports = 10_000
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	if _, err := AuthorizeAndSign(policy, privateKey, request, now); err != nil {
		t.Fatalf("first authorization: %v", err)
	}

	loosened := policy
	loosened.DailyDebitCapLamports = policy.DailyDebitCapLamports * 10
	if _, err := openAuthorizationLedger(loosened, now); err == nil {
		t.Fatal("loosening a cap reused the ledger; that must stay a deliberate act")
	}
}

func TestTighteningABuyCapReportsTheDailyReset(t *testing.T) {
	policy, privateKey, request := buySignerFixture(t)
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	if _, err := AuthorizeAndSign(policy, privateKey, request, now); err != nil {
		t.Fatalf("first buy authorization: %v", err)
	}

	tightened := policy
	tightened.DailyNativeFeeCapLamports--
	_, err := openBuyAuthorizationLedger(tightened, now)
	if err == nil {
		t.Fatal("tightening a buy cap unexpectedly reused the ledger")
	}
	for _, want := range []string{"a cap was edited", "RAISES what today still allows", "00:00 UTC"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("buy-cap refusal %q does not contain %q", err, want)
		}
	}
}
