package signer

import (
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
)

// The buy leg has two independent daily bounds: how much devUSDC it may spend
// and how much native SOL it may burn in fees. Either one deleted left the
// whole signer package passing.
//
// The cause was the fixture, not an omission: DailyInputTokenCap is 1_000 and
// one trade spends exactly 1_000, DailyNativeFeeCapLamports is 5_000 and one
// trade burns exactly 5_000. Both bounds are therefore breached by the same
// second call, so whichever survives supplies the error and covers for the one
// that was removed. The single existing test also asserts only that an error
// occurred, never which bound produced it.
//
// These raise the OTHER cap out of reach so exactly one bound can refuse, and
// assert the refusal names it. A test that accepts any error cannot tell a cap
// from an unrelated guard, which is how both of these came to be unpinned.

// secondBuyInTheSameDay advances the request into the next schedule window,
// leaving the UTC day unchanged so the daily ledger still applies, and re-issues
// the risk grant so the request stays internally consistent. Without the fresh
// grant the signer refuses at grant verification and never consults a cap.
func secondBuyInTheSameDay(t *testing.T, policy Policy, request *Request, now time.Time) time.Time {
	t.Helper()
	request.ScheduleWindowStartUnix += 3_600
	request.ScheduleWindowEndUnix += 3_600
	actionID, err := orcaswap.ComputeBuyActionID(
		policy.ProfileFingerprint, request.ScheduleWindowStartUnix,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.ActionID = actionID
	next := now.Add(3_600 * time.Second)
	grantSignerRequest(t, policy, request, next)
	return next
}

func TestBuyDailyInputTokenCapRefusesOnItsOwn(t *testing.T) {
	policy, privateKey, request := buySignerFixture(t)
	// Fees can never bind, so only the input-token cap is left to refuse.
	policy.DailyNativeFeeCapLamports *= 1_000
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	if _, err := AuthorizeAndSign(policy, privateKey, request, now); err != nil {
		t.Fatalf("first buy of the day was refused: %v", err)
	}

	next := secondBuyInTheSameDay(t, policy, &request, now)
	_, err := AuthorizeAndSign(policy, privateKey, request, next)
	if err == nil {
		t.Fatal("a second buy spent past the daily input-token cap")
	}
	if !strings.Contains(err.Error(), "daily input-token cap") {
		t.Fatalf("refused, but not by the input-token cap: %v", err)
	}
}

func TestBuyDailyNativeFeeCapRefusesOnItsOwn(t *testing.T) {
	policy, privateKey, request := buySignerFixture(t)
	// Input tokens can never bind, so only the native-fee cap is left.
	policy.DailyInputTokenCap *= 1_000
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	if _, err := AuthorizeAndSign(policy, privateKey, request, now); err != nil {
		t.Fatalf("first buy of the day was refused: %v", err)
	}

	next := secondBuyInTheSameDay(t, policy, &request, now)
	_, err := AuthorizeAndSign(policy, privateKey, request, next)
	if err == nil {
		t.Fatal("a second buy burned past the daily native-debit cap")
	}
	if !strings.Contains(err.Error(), "daily native-debit cap") {
		t.Fatalf("refused, but not by the native-debit cap: %v", err)
	}
}

// Both caps raised means a second buy must be allowed — otherwise the two tests
// above would be satisfied by a signer that refuses every repeat trade for some
// unrelated reason.
func TestASecondBuyIsAllowedWhenNeitherDailyCapBinds(t *testing.T) {
	policy, privateKey, request := buySignerFixture(t)
	policy.DailyInputTokenCap *= 1_000
	policy.DailyNativeFeeCapLamports *= 1_000
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	if _, err := AuthorizeAndSign(policy, privateKey, request, now); err != nil {
		t.Fatalf("first buy of the day was refused: %v", err)
	}

	next := secondBuyInTheSameDay(t, policy, &request, now)
	if _, err := AuthorizeAndSign(policy, privateKey, request, next); err != nil {
		t.Fatalf("a second buy was refused with both caps far from binding: %v", err)
	}
}
