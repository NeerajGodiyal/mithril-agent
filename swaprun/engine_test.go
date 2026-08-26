package swaprun

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/internal/clockcheck"
	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/swapbuilder"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func testProfile() Profile {
	seed := sha256.Sum256([]byte("swap-profile-owner"))
	key := ed25519.NewKeyFromSeed(seed[:])
	owner := solana.Encode(key.Public().(ed25519.PublicKey))
	input, err := orcaswap.AssociatedTokenAddress(owner, orcaswap.WrappedSOLMint)
	if err != nil {
		panic(err)
	}
	output, err := orcaswap.AssociatedTokenAddress(owner, orcaswap.DevnetUSDCMint)
	if err != nil {
		panic(err)
	}
	return Profile{
		Name: orcaswap.ProfileName, Version: orcaswap.ProfileVersion, Cluster: "devnet",
		Route: orcaswap.Policy{
			Owner:                        owner,
			Pool:                         "3KBZiL2g8C7tiJ32hTv5v3KM7aK9htpqTw4cTXz1HvPt",
			InputMint:                    orcaswap.WrappedSOLMint,
			OutputMint:                   "BRjpCHtyQLNCo8gqRUr8jtdAj5AjPYQaoqbvcZiHok1k",
			InputTokenAccount:            input,
			OutputTokenAccount:           output,
			TokenVaultA:                  "C9zLV5zWF66j3rZj3uuhDqvfuA8esJyWnruGzDW9qEj2",
			TokenVaultB:                  "7DM3RMz2yzUB8yPRQM3FMZgdFrwZGMsabsfsKopWktoX",
			Oracle:                       "2KEWNc3b6EfqoWQpfKQMHh4mhRyKXYRdPbtGRTJX3Cip",
			ProgramData:                  orcaswap.WhirlpoolProgramData,
			UpgradeAuthority:             orcaswap.WhirlpoolUpgradeAuth,
			DeploymentSlot:               orcaswap.WhirlpoolDeploySlot,
			MaxInputLamports:             1_000_000,
			MinOutputAmount:              1,
			MaxSlippageBPS:               100,
			MaxOutputAccountRentLamports: orcaswap.DefaultMaxOutputAccountRentLamports,
		},
		InputLamports: 1_000_000, SlippageBPS: 100,
		ReserveLamports: 50_000_000, MaxFeeLamports: 100_000,
		DailyDebitCapLamports: 4_100_000,
		ScheduleWindowSeconds: 3_600, ScheduleAnchorUnix: 1_785_369_600,
		MaxClockUncertaintyMillis: 100,
		MaxObservationAgeSeconds:  30, MinHealthyObservationSeconds: 5,
		MinHealthySlotAdvance: 1, MaxBlockHeightWindow: 200,
		MaxReconciliationSeconds: 30,
	}
}

func testStore(t *testing.T) *journal.Store {
	t.Helper()
	store, err := journal.Open(t.TempDir() + "/journal.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func appendStart(t *testing.T, store *journal.Store, profile Profile, windowStart int64) string {
	t.Helper()
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	actionID, err := orcaswap.ComputeActionID(fingerprint, windowStart)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Append(time.Unix(windowStart, 0).UTC(), EventStarted, actionID, startedRecord{
		ProfileFingerprint: fingerprint, ScheduleWindowStartUnix: windowStart,
		ScheduleWindowEndUnix: windowStart + int64(profile.ScheduleWindowSeconds),
		ObservationSlot:       100,
	})
	if err != nil {
		t.Fatal(err)
	}
	return actionID
}

func appendTerminalSwap(
	t *testing.T,
	store *journal.Store,
	profile Profile,
	windowStart int64,
	verdict string,
) string {
	t.Helper()
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return appendTerminalSwapWithStart(t, store, profile, startedRecord{
		ProfileFingerprint:      fingerprint,
		ScheduleWindowStartUnix: windowStart,
		ScheduleWindowEndUnix:   windowStart + int64(profile.ScheduleWindowSeconds),
		ObservationSlot:         100,
	}, verdict)
}

func appendTerminalSwapWithStart(
	t *testing.T,
	store *journal.Store,
	profile Profile,
	started startedRecord,
	verdict string,
) string {
	t.Helper()
	actionID, err := orcaswap.ComputeActionID(
		started.ProfileFingerprint, started.ScheduleWindowStartUnix,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(
		time.Unix(started.ScheduleWindowStartUnix, 0).UTC(),
		EventStarted,
		actionID,
		started,
	); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(started.ScheduleWindowStartUnix+1, 0).UTC()
	for _, event := range []struct {
		typeName string
		payload  any
	}{
		{EventBuilt, builtRecord{MinimumOutput: 1}},
		{EventSimulated, txflow.LegacySimulationEvidence{}},
		{EventSigned, signedRecord{Response: signer.Response{
			Signature: "signature", TransactionSHA256: strings.Repeat("1", 64),
		}}},
		{EventPreSendObserved, healthyObservation(profile, at)},
	} {
		if _, err := store.Append(at, event.typeName, actionID, event.payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.EnsureCapacity(terminalRecords, remainingBytes); err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		typeName string
		payload  any
	}{
		{EventSendStarted, sendStartedRecord{
			Signature: "signature", TransactionSHA256: strings.Repeat("1", 64),
		}},
		{EventSubmitted, txflow.Submission{Signature: "signature", State: txflow.StateAccepted}},
		{EventReconciled, txflow.Reconciliation{Signature: "signature", Verdict: verdict}},
	} {
		if _, err := store.Append(at, event.typeName, actionID, event.payload); err != nil {
			t.Fatal(err)
		}
	}
	return actionID
}

func TestRecoverStatePrefersUnfinishedPreviousWindow(t *testing.T) {
	profile := testProfile()
	store := testStore(t)
	previousStart := profile.ScheduleAnchorUnix + 3_600
	wantID := appendStart(t, store, profile, previousStart)
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{store: store}
	gotID, state, recovered, err := engine.recoverState(profile, fingerprint, previousStart+3_600)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered || gotID != wantID || state.started == nil ||
		state.started.ScheduleWindowStartUnix != previousStart {
		t.Fatalf("recovered=%t action=%q state=%+v", recovered, gotID, state)
	}
}

func TestSustainedHealthUsesOneJournalTimestamp(t *testing.T) {
	profile := testProfile()
	store := testStore(t)
	base := time.Unix(profile.ScheduleAnchorUnix+60, 0).UTC()
	previous := healthyObservation(profile, base.Add(-6*time.Second))
	currentObservation := healthyObservation(profile, base)
	currentObservation.Account.Slot++
	actionID := strings.Repeat("a", 64)
	if _, err := store.Append(
		base.Add(-6*time.Second), EventObserved, actionID, previous,
	); err != nil {
		t.Fatal(err)
	}
	current := &state{observations: []agent.NodeObservation{previous}}
	engine := &Engine{store: store, now: func() time.Time { return base.Add(2 * time.Second) }}
	recordedAt := base.Add(time.Second)
	ready, err := engine.sustainedHealthReady(
		actionID, profile, current, currentObservation, recordedAt,
	)
	if err != nil || !ready {
		t.Fatalf("sustained health ready=%t err=%v", ready, err)
	}
	if _, err := store.Append(recordedAt, EventStarted, actionID, struct{}{}); err != nil {
		t.Fatalf("causal journal timestamp regressed: %v", err)
	}
}

func TestRecoverStateRejectsMultipleUnfinishedSwaps(t *testing.T) {
	profile := testProfile()
	store := testStore(t)
	appendStart(t, store, profile, profile.ScheduleAnchorUnix+3_600)
	appendStart(t, store, profile, profile.ScheduleAnchorUnix+7_200)
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := (&Engine{store: store}).recoverState(
		profile, fingerprint, profile.ScheduleAnchorUnix+10_800,
	); err == nil {
		t.Fatal("multiple unfinished swaps were accepted")
	}
}

func TestValidateObservationRejectsFutureHealthEvidence(t *testing.T) {
	profile := testProfile()
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	observation := agent.NodeObservation{
		Account: agent.Observation{
			Cluster: "devnet", Source: profile.Route.Owner,
			BalanceLamports: profile.InputLamports + profile.ReserveLamports + profile.MaxFeeLamports +
				profile.Route.MaxOutputAccountRentLamports,
			Slot: 100, ObservedAt: now, EvidenceSource: "mithril_mcp",
			Finality: "local_unfinalized", Consistency: "node_reported_non_atomic",
		},
		Health: agent.NodeHealth{
			Status: "healthy", AssessmentScope: "point_in_time_snapshot",
			ObservedAt: now.Add(2 * time.Second), EvidenceComplete: true,
			CrossCheck: &agent.SlotComparison{
				MithrilSlot: 100, ReferenceSlot: 100, ReferenceCommitment: "confirmed",
				MithrilView: "local_unfinalized_head", Threshold: 150, Status: "in_sync",
			},
		},
	}
	err := ValidateObservation(profile, observation, now)
	if err == nil {
		t.Fatal("future Mithril health evidence was accepted")
	}
	if got := ObservationFailure(err); got != "mithril_health_freshness" {
		t.Fatalf("observation failure = %q", got)
	}
}

func TestValidateObservationClassifiesHealthStatus(t *testing.T) {
	profile := testProfile()
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	observation := healthyObservation(profile, now)
	observation.Health.Status = "critical"
	observation.Health.Issues = []agent.HealthIssue{{
		Name: "runtime_state_agreement", Status: "critical",
	}}
	err := ValidateObservation(profile, observation, now)
	if got := ObservationFailure(err); got != "mithril_health_status_critical_runtime_state_agreement" {
		t.Fatalf("observation failure = %q", got)
	}
}

func TestEngineReportsInvalidHealthAsDegraded(t *testing.T) {
	profile := testProfile()
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	observation := healthyObservation(profile, now)
	observation.Health.Status = "critical"
	engine := &Engine{
		store: testStore(t), observer: &observerStub{observation: observation},
		tx: &transactorStub{}, stop: &stopStub{}, now: func() time.Time { return now },
		clock: func() (clockcheck.Sample, error) {
			return clockcheck.Sample{
				WallTime: now, BootID: "00000000-0000-0000-0000-000000000001",
				MonotonicNanos:   uint64(time.Second),
				UncertaintyNanos: uint64(10 * time.Millisecond),
			}, nil
		},
	}
	result, err := engine.RunOnce(context.Background(), profile)
	if err != nil || result.Decision != "degraded" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestEngineValidatesObservationAtReceiptTime(t *testing.T) {
	profile := testProfile()
	startedAt := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	observedAt := startedAt.Add(2 * time.Second)
	now := startedAt
	observer := &observerStub{observation: healthyObservation(profile, observedAt)}
	observer.afterObserve = func() { now = observedAt }
	engine := &Engine{
		store: testStore(t), observer: observer,
		tx: &transactorStub{}, stop: &stopStub{}, now: func() time.Time { return now },
		clock: func() (clockcheck.Sample, error) {
			return clockcheck.Sample{
				WallTime: startedAt, BootID: "00000000-0000-0000-0000-000000000001",
				MonotonicNanos:   uint64(time.Second),
				UncertaintyNanos: uint64(10 * time.Millisecond),
			}, nil
		},
	}
	result, err := engine.RunOnce(context.Background(), profile)
	if err != nil || result.Decision != "waiting" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestValidateClockSampleRejectsFutureAndMalformedEvidence(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	valid := clockcheck.Sample{
		WallTime: now, BootID: "00000000-0000-0000-0000-000000000001",
		MonotonicNanos: uint64(time.Hour), UncertaintyNanos: uint64(10 * time.Millisecond),
	}
	if err := ValidateClockSample(valid, now, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	future := valid
	future.WallTime = now.Add(clockcheck.MaxOffset + time.Nanosecond)
	if err := ValidateClockSample(future, now, 100*time.Millisecond); err == nil {
		t.Fatal("future clock evidence was accepted")
	}
	malformed := valid
	malformed.BootID = "boot"
	if err := ValidateClockSample(malformed, now, 100*time.Millisecond); err == nil {
		t.Fatal("malformed boot identity was accepted")
	}
	invalidOffset := valid
	invalidOffset.OffsetNanos = math.MinInt64
	if err := ValidateClockSample(invalidOffset, now, 100*time.Millisecond); err == nil {
		t.Fatal("minimum clock offset was accepted")
	}
	invalidUncertainty := valid
	invalidUncertainty.UncertaintyNanos = math.MaxUint64
	if err := ValidateClockSample(invalidUncertainty, now, 100*time.Millisecond); err == nil {
		t.Fatal("maximum clock uncertainty was accepted")
	}
	if err := ValidateClockSample(valid, now, -time.Nanosecond); err == nil {
		t.Fatal("negative clock uncertainty policy was accepted")
	}
	if err := ValidateClockSample(valid, now, clockcheck.MaxUncertaintyCap+1); err == nil {
		t.Fatal("excessive clock uncertainty policy was accepted")
	}
}

func TestEngineCompletesAndRecoversOneSwapWithoutResubmission(t *testing.T) {
	profile := testProfile()
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	store := testStore(t)
	stop := &stopStub{}
	submitter := &submitterStub{barrier: stop}
	transactor := &transactorStub{}
	observer := &observerStub{observation: healthyObservation(profile, now)}
	engine, err := New(
		store,
		observer,
		quoteStub{result: swapQuote(profile)},
		blockhashStub{
			latest: solanarpc.LatestBlockhash{
				ContextSlot: 101, Blockhash: solana.Encode(bytes.Repeat([]byte{7}, 32)),
				LastValidBlockHeight: 250,
			},
			height: 100,
		},
		authorityStub{},
		signerStub{valid: true},
		submitter,
		transactor,
		stop,
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	clockStart := now
	monotonic := uint64(time.Second)
	engine.clock = func() (clockcheck.Sample, error) {
		monotonic++
		return clockcheck.Sample{
			WallTime: now, BootID: "00000000-0000-0000-0000-000000000001",
			MonotonicNanos:   monotonic + uint64(now.Sub(clockStart)),
			UncertaintyNanos: uint64(10 * time.Millisecond),
		}, nil
	}

	first, err := engine.RunOnce(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	if first.Decision != "waiting" || submitter.calls != 0 || stop.barriers != 0 || observer.calls != 1 {
		t.Fatalf("first health result = %+v, submissions=%d barriers=%d", first, submitter.calls, stop.barriers)
	}
	now = now.Add(6 * time.Second)
	observer.observation = healthyObservation(profile, now)
	observer.observation.Account.Slot++
	completed, err := engine.RunOnce(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Decision != "complete" || completed.Verdict != txflow.VerdictFinalized ||
		completed.OutputAmount != 21_600 || completed.MinimumOutput != 21_525 ||
		!completed.Submitted || completed.Recovered || submitter.calls != 1 || stop.barriers != 1 {
		t.Fatalf("completed result = %+v, submissions=%d barriers=%d", completed, submitter.calls, stop.barriers)
	}
	if !submitter.outsideBarrier || observer.calls != 3 {
		t.Fatalf("outside_barrier=%t observations=%d", submitter.outsideBarrier, observer.calls)
	}
	wantEvents := []string{
		clockEvent, EventObserved, EventObserved,
		EventStarted, EventBuilt, EventSimulated, EventSigned,
		EventPreSendObserved, EventSendStarted, EventSubmitted, EventReconciled,
	}
	records := store.Records()
	if len(records) != len(wantEvents) {
		t.Fatalf("journal has %d records, want %d", len(records), len(wantEvents))
	}
	for index, want := range wantEvents {
		if records[index].Type != want {
			t.Fatalf("journal event %d = %q, want %q", index, records[index].Type, want)
		}
	}

	now = now.Add(time.Second)
	second, err := engine.RunOnce(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Recovered || !second.Submitted || second.ActionID != completed.ActionID ||
		second.Decision != "complete" || submitter.calls != 1 || stop.barriers != 1 {
		t.Fatalf("recovered result = %+v, submissions=%d barriers=%d", second, submitter.calls, stop.barriers)
	}
}

func TestEngineCompletesAndRecoversBuyWithoutResubmission(t *testing.T) {
	profile := testBuyProfile(t)
	profile.PriceTrigger = nil
	profile.BuyRoute.MinOutputLamports = 45_348
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	store := testStore(t)
	stop := &stopStub{}
	submitter := &submitterStub{barrier: stop}
	transactor := &transactorStub{}
	observer := &observerStub{observation: healthyObservation(profile, now)}
	engine, err := New(
		store, observer, quoteStub{result: buyQuote(t, profile)},
		blockhashStub{latest: solanarpc.LatestBlockhash{
			ContextSlot: 101, Blockhash: solana.Encode(bytes.Repeat([]byte{7}, 32)),
			LastValidBlockHeight: 250,
		}, height: 100}, authorityStub{}, signerStub{valid: true},
		submitter, transactor, stop, func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	clockStart := now
	monotonic := uint64(time.Second)
	engine.clock = func() (clockcheck.Sample, error) {
		monotonic++
		return clockcheck.Sample{
			WallTime: now, BootID: "00000000-0000-0000-0000-000000000001",
			MonotonicNanos:   monotonic + uint64(now.Sub(clockStart)),
			UncertaintyNanos: uint64(10 * time.Millisecond),
		}, nil
	}
	first, err := engine.RunOnce(t.Context(), profile)
	if err != nil || first.Decision != "waiting" || submitter.calls != 0 {
		t.Fatalf("initial buy result = %+v, %v", first, err)
	}
	now = now.Add(6 * time.Second)
	observer.observation = healthyObservation(profile, now)
	observer.observation.Account.Slot++
	completed, err := engine.RunOnce(t.Context(), profile)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Decision != "complete" || completed.Verdict != txflow.VerdictFinalized ||
		completed.InputAmount != profile.InputTokenAmount ||
		completed.OutputAmount != profile.BuyRoute.MinOutputLamports+7 ||
		completed.InputAsset != "devUSDC" || completed.OutputAsset != "SOL" ||
		submitter.calls != 1 || stop.barriers != 1 || transactor.reconciliationCalls != 1 {
		t.Fatalf("completed buy = %+v submissions=%d barriers=%d reconciliations=%d", completed, submitter.calls, stop.barriers, transactor.reconciliationCalls)
	}
	now = now.Add(time.Second)
	recovered, err := engine.RunOnce(t.Context(), profile)
	if err != nil || !recovered.Recovered || recovered.ActionID != completed.ActionID ||
		submitter.calls != 1 || transactor.reconciliationCalls != 1 {
		t.Fatalf("recovered buy = %+v, %v", recovered, err)
	}
}

func TestEngineRetriesAfterSendBarrierFailure(t *testing.T) {
	now := time.Unix(testProfile().ScheduleAnchorUnix+3_601, 0).UTC()
	profile, _, recovered := recoveredSwapFixture(t, now)
	store := testStore(t)
	stop := &stopStub{barrierErr: errors.New("send barrier unavailable")}
	submitter := &submitterStub{barrier: stop}
	observer := &observerStub{observation: healthyObservation(profile, now)}
	engine, err := New(
		store, observer, quoteStub{result: swapQuote(profile)},
		blockhashStub{latest: solanarpc.LatestBlockhash{
			ContextSlot: 101, Blockhash: solana.Encode(bytes.Repeat([]byte{7}, 32)),
			LastValidBlockHeight: 250,
		}, height: 100}, authorityStub{}, signerStub{response: recovered.signed.Response},
		submitter, &transactorStub{}, stop, func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	clockStart := now
	engine.clock = func() (clockcheck.Sample, error) {
		return clockcheck.Sample{
			WallTime: now, BootID: "00000000-0000-0000-0000-000000000001",
			MonotonicNanos:   uint64(time.Second + now.Sub(clockStart)),
			UncertaintyNanos: uint64(10 * time.Millisecond),
		}, nil
	}
	if result, err := engine.RunOnce(t.Context(), profile); err != nil || result.Decision != "waiting" {
		t.Fatalf("first observation = %+v, %v", result, err)
	}
	now = now.Add(6 * time.Second)
	observer.observation = healthyObservation(profile, now)
	observer.observation.Account.Slot++
	if _, err := engine.RunOnce(t.Context(), profile); err == nil ||
		!strings.Contains(err.Error(), "send barrier unavailable") {
		t.Fatalf("barrier error = %v", err)
	}
	if submitter.calls != 0 {
		t.Fatal("failed barrier reached the submitter")
	}
	stop.barrierErr = nil
	now = now.Add(time.Second)
	observer.observation = healthyObservation(profile, now)
	observer.observation.Account.Slot += 2
	result, err := engine.RunOnce(t.Context(), profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != "complete" || !result.Submitted || submitter.calls != 1 {
		t.Fatalf("retry result = %+v, submissions=%d", result, submitter.calls)
	}
	var preSend, sendStarted, submitted int
	for _, record := range store.Records() {
		switch record.Type {
		case EventPreSendObserved:
			preSend++
		case EventSendStarted:
			sendStarted++
		case EventSubmitted:
			submitted++
		}
	}
	if preSend != 2 || sendStarted != 1 || submitted != 1 {
		t.Fatalf("events pre_send=%d send_started=%d submitted=%d", preSend, sendStarted, submitted)
	}
}

func TestTriggeredProfileRequiresEvaluator(t *testing.T) {
	profile := testProfile()
	trigger := testPriceTriggerPolicy()
	profile.PriceTrigger = &trigger
	if _, err := (&Engine{}).RunOnce(t.Context(), profile); err == nil ||
		!strings.Contains(err.Error(), "evaluator is not configured") {
		t.Fatalf("missing evaluator error = %v", err)
	}

	trigger.Direction = pricetrigger.BuyAtOrBelow
	profile.PriceTrigger = &trigger
	if err := profile.Validate(); err == nil || !strings.Contains(err.Error(), "sell-at-or-above") {
		t.Fatalf("unsupported route trigger error = %v", err)
	}
}

func TestEngineWaitsForPriceBeforeStarting(t *testing.T) {
	profile := testProfile()
	triggerPolicy := testPriceTriggerPolicy()
	profile.PriceTrigger = &triggerPolicy
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	store := testStore(t)
	observer := &observerStub{observation: healthyObservation(profile, now)}
	trigger := &priceTriggerStub{}
	trigger.evidence = testPriceEvidence(t, triggerPolicy, now, 149_000_000)
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
	clockStart := now
	monotonic := uint64(time.Second)
	engine.clock = func() (clockcheck.Sample, error) {
		monotonic++
		return clockcheck.Sample{
			WallTime: now, BootID: "00000000-0000-0000-0000-000000000001",
			MonotonicNanos:   monotonic + uint64(now.Sub(clockStart)),
			UncertaintyNanos: uint64(10 * time.Millisecond),
		}, nil
	}
	// The price read now happens AFTER the node observation, because binding it
	// to a proven slot is what makes it authorizable — and BEFORE the health
	// gate returns, so the operator can still see the price while the node is
	// warming up. So the first cycle observes once, reads the price once, and
	// reports the health gate as the reason while carrying a price status.
	result, err := engine.RunOnce(t.Context(), profile)
	if err != nil || result.Decision != "waiting" ||
		result.PriceTrigger == nil || trigger.calls != 1 || observer.calls != 1 {
		t.Fatalf("first price check = %+v, error=%v calls=%d/%d", result, err, trigger.calls, observer.calls)
	}
	records := len(store.Records())
	now = now.Add(6 * time.Second)
	observer.observation = healthyObservation(profile, now)
	observer.observation.Account.Slot++
	trigger.evidence = testPriceEvidence(t, triggerPolicy, now, 149_000_000)
	result, err = engine.RunOnce(t.Context(), profile)
	if err != nil {
		t.Fatal(err)
	}
	// Every cycle observes the node and then reads the price against that
	// observation's slot, so both counters advance together. What must not
	// advance is the journal: an unreached price starts nothing.
	if result.Decision != "waiting" || result.Reason != "price trigger has not been reached" ||
		trigger.calls != 2 || observer.calls != 2 || len(store.Records()) != records {
		t.Fatalf("price wait result = %+v, trigger calls = %d, observer calls = %d",
			result, trigger.calls, observer.calls)
	}
	for _, record := range store.Records() {
		if record.Type == EventStarted {
			t.Fatal("price miss created a swap action")
		}
	}
}

func TestEngineRechecksPriceBeforeSigningAndSending(t *testing.T) {
	for _, test := range []struct {
		name            string
		threshold       uint64
		prices          []uint64
		wantSigned      bool
		wantMarketMet   bool
		wantExecutable  bool
		wantTriggerCall int
	}{
		{name: "before signing", threshold: 20_000_000, prices: []uint64{25_000_000, 25_000_000, 19_000_000}, wantExecutable: true, wantTriggerCall: 3},
		{name: "before sending", threshold: 20_000_000, prices: []uint64{25_000_000, 25_000_000, 25_000_000, 25_000_000, 19_000_000}, wantSigned: true, wantExecutable: true, wantTriggerCall: 5},
		{name: "executable minimum", threshold: 22_000_000, prices: []uint64{25_000_000, 25_000_000, 25_000_000}, wantMarketMet: true, wantTriggerCall: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := testProfile()
			policy := testPriceTriggerPolicy()
			policy.ThresholdMicros = test.threshold
			profile.PriceTrigger = &policy
			start := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
			now := start
			later := start.Add(6 * time.Second)
			trigger := &priceTriggerStub{}
			for index, price := range test.prices {
				at := later
				if index == 0 {
					at = start
				}
				trigger.evidences = append(trigger.evidences, testPriceEvidence(t, policy, at, price))
			}
			store := testStore(t)
			stop := &stopStub{}
			submitter := &submitterStub{barrier: stop}
			observer := &observerStub{observation: healthyObservation(profile, now)}
			engine, err := New(
				store, observer, quoteStub{result: swapQuote(profile)},
				blockhashStub{latest: solanarpc.LatestBlockhash{
					ContextSlot: 101, Blockhash: solana.Encode(bytes.Repeat([]byte{7}, 32)),
					LastValidBlockHeight: 250,
				}, height: 100}, authorityStub{}, signerStub{valid: true},
				submitter, &transactorStub{}, stop, func() time.Time { return now },
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
			if result, err := engine.RunOnce(t.Context(), profile); err != nil || result.Decision != "waiting" {
				t.Fatalf("initial observation = %+v, %v", result, err)
			}
			now = later
			observer.observation = healthyObservation(profile, now)
			observer.observation.Account.Slot++
			result, err := engine.RunOnce(t.Context(), profile)
			if err != nil || result.Decision != "waiting" ||
				result.PriceTrigger == nil || result.PriceTrigger.ConditionMet != test.wantMarketMet ||
				result.PriceTrigger.ExecutableCondition != test.wantExecutable ||
				trigger.calls != test.wantTriggerCall || submitter.calls != 0 || stop.barriers != 0 {
				t.Fatalf("price recheck result = %+v, error=%v calls=%d submissions=%d barriers=%d",
					result, err, trigger.calls, submitter.calls, stop.barriers)
			}
			signed := false
			for _, record := range store.Records() {
				if record.Type == EventSigned {
					signed = true
				}
			}
			if signed != test.wantSigned {
				t.Fatalf("signed=%t, want %t", signed, test.wantSigned)
			}
			// Reads taken while deciding whether to wait are advisory and carry
			// no slot; the re-check that authorizes signing must be bound to the
			// slot already proved, so a node that stalled afterwards cannot
			// supply the authorizing price.
			if len(trigger.slots) != test.wantTriggerCall {
				t.Fatalf("recorded %d price reads, want %d", len(trigger.slots), test.wantTriggerCall)
			}
			provenSlot := observer.observation.Account.Slot
			// The pre-start read binds the slot of the observation it was taken
			// with, which is one behind the slot proven for the action itself.
			// Both are proven slots; what must never appear is an unbound read
			// on a path that can authorize, or a slot from nowhere.
			bound := 0
			for index, slot := range trigger.slots {
				switch slot {
				case 0, provenSlot - 1:
				case provenSlot:
					bound++
				default:
					t.Fatalf("price read %d used slot %d, which is neither the observed nor the proven slot %d",
						index, slot, provenSlot)
				}
			}
			if bound == 0 {
				t.Fatalf("no price read was bound to the proven slot (reads: %v)", trigger.slots)
			}
		})
	}
}

func TestEngineRejectsBlockhashBeforeObservation(t *testing.T) {
	profile := testProfile()
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	observer := &observerStub{observation: healthyObservation(profile, now)}
	submitter := &submitterStub{}
	transactor := &transactorStub{}
	engine, err := New(
		testStore(t), observer, quoteStub{result: swapQuote(profile)},
		blockhashStub{latest: solanarpc.LatestBlockhash{
			ContextSlot:          99,
			Blockhash:            solana.Encode(bytes.Repeat([]byte{7}, 32)),
			LastValidBlockHeight: 250,
		}, height: 100}, authorityStub{}, signerStub{}, submitter, transactor,
		&stopStub{}, func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	clockStart := now
	engine.clock = func() (clockcheck.Sample, error) {
		return clockcheck.Sample{
			WallTime: now, BootID: "00000000-0000-0000-0000-000000000001",
			MonotonicNanos:   uint64(time.Second + now.Sub(clockStart)),
			UncertaintyNanos: uint64(10 * time.Millisecond),
		}, nil
	}
	if result, err := engine.RunOnce(t.Context(), profile); err != nil || result.Decision != "waiting" {
		t.Fatalf("first observation = %+v, %v", result, err)
	}
	now = now.Add(6 * time.Second)
	observer.observation = healthyObservation(profile, now)
	observer.observation.Account.Slot++
	if _, err := engine.RunOnce(t.Context(), profile); err == nil ||
		!strings.Contains(err.Error(), "blockhash predates") {
		t.Fatalf("stale blockhash error = %v", err)
	}
	if submitter.calls != 0 || transactor.verifyDeploymentCalls != 0 {
		t.Fatalf("stale blockhash reached transaction path: submissions=%d deployment_checks=%d",
			submitter.calls, transactor.verifyDeploymentCalls)
	}
}

func TestEngineStopsAtEachWhirlpoolDeploymentGate(t *testing.T) {
	for name, deploymentErrors := range map[string][]error{
		"before build": {errors.New("deployment changed before build")},
		"before send":  {nil, errors.New("deployment changed before send")},
	} {
		t.Run(name, func(t *testing.T) {
			profile := testProfile()
			now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
			stop := &stopStub{}
			submitter := &submitterStub{barrier: stop}
			transactor := &transactorStub{verifyDeploymentErrors: deploymentErrors}
			observer := &observerStub{observation: healthyObservation(profile, now)}
			engine, err := New(
				testStore(t), observer, quoteStub{result: swapQuote(profile)},
				blockhashStub{latest: solanarpc.LatestBlockhash{
					ContextSlot:          101,
					Blockhash:            solana.Encode(bytes.Repeat([]byte{7}, 32)),
					LastValidBlockHeight: 250,
				}, height: 100}, authorityStub{}, signerStub{valid: true},
				submitter, transactor, stop, func() time.Time { return now },
			)
			if err != nil {
				t.Fatal(err)
			}
			clockStart := now
			engine.clock = func() (clockcheck.Sample, error) {
				return clockcheck.Sample{
					WallTime: now, BootID: "00000000-0000-0000-0000-000000000001",
					MonotonicNanos:   uint64(time.Second + now.Sub(clockStart)),
					UncertaintyNanos: uint64(10 * time.Millisecond),
				}, nil
			}
			if result, err := engine.RunOnce(t.Context(), profile); err != nil || result.Decision != "waiting" {
				t.Fatalf("first observation = %+v, %v", result, err)
			}
			now = now.Add(6 * time.Second)
			observer.observation = healthyObservation(profile, now)
			observer.observation.Account.Slot++
			_, err = engine.RunOnce(t.Context(), profile)
			wantErr := deploymentErrors[len(deploymentErrors)-1]
			if !errors.Is(err, wantErr) || submitter.calls != 0 ||
				transactor.verifyDeploymentCalls != len(deploymentErrors) || stop.barriers != 0 {
				t.Fatalf(
					"error=%v deployment_checks=%d barriers=%d submissions=%d",
					err, transactor.verifyDeploymentCalls, stop.barriers, submitter.calls,
				)
			}
		})
	}
}

func TestEngineStopsAtEachOutputAccountRentGate(t *testing.T) {
	for name, rentErrors := range map[string][]error{
		"before build": {errors.New("rent unavailable before build")},
		"before send":  {nil, errors.New("rent changed before send")},
	} {
		t.Run(name, func(t *testing.T) {
			profile := testProfile()
			now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
			stop := &stopStub{}
			submitter := &submitterStub{barrier: stop}
			transactor := &transactorStub{verifyRentErrors: rentErrors}
			observer := &observerStub{observation: healthyObservation(profile, now)}
			engine, err := New(
				testStore(t), observer, quoteStub{result: swapQuoteWithOutputSetup(profile)},
				blockhashStub{latest: solanarpc.LatestBlockhash{
					ContextSlot:          101,
					Blockhash:            solana.Encode(bytes.Repeat([]byte{7}, 32)),
					LastValidBlockHeight: 250,
				}, height: 100}, authorityStub{}, signerStub{valid: true},
				submitter, transactor, stop, func() time.Time { return now },
			)
			if err != nil {
				t.Fatal(err)
			}
			clockStart := now
			engine.clock = func() (clockcheck.Sample, error) {
				return clockcheck.Sample{
					WallTime: now, BootID: "00000000-0000-0000-0000-000000000001",
					MonotonicNanos:   uint64(time.Second + now.Sub(clockStart)),
					UncertaintyNanos: uint64(10 * time.Millisecond),
				}, nil
			}
			if result, err := engine.RunOnce(t.Context(), profile); err != nil || result.Decision != "waiting" {
				t.Fatalf("first observation = %+v, %v", result, err)
			}
			now = now.Add(6 * time.Second)
			observer.observation = healthyObservation(profile, now)
			observer.observation.Account.Slot++
			_, err = engine.RunOnce(t.Context(), profile)
			wantErr := rentErrors[len(rentErrors)-1]
			if !errors.Is(err, wantErr) || submitter.calls != 0 ||
				transactor.verifyRentCalls != len(rentErrors) || stop.barriers != 0 {
				t.Fatalf(
					"error=%v rent_checks=%d barriers=%d submissions=%d",
					err, transactor.verifyRentCalls, stop.barriers, submitter.calls,
				)
			}
		})
	}
}

func TestFinalizedReconciliationSurvivesReserveCleanupFailure(t *testing.T) {
	store := testStore(t)
	engine := &Engine{
		store: store, now: time.Now, stop: &stopStub{},
		releaseCapacity: func() error { return errors.New("reserve cleanup failed") },
	}
	result, err := engine.recordReconciliation(
		strings.Repeat("a", 64), testProfile(), &state{},
		txflow.Reconciliation{Verdict: txflow.VerdictFinalized}, false,
	)
	if err != nil || result.Decision != "complete" || result.Verdict != txflow.VerdictFinalized {
		t.Fatalf("finalized result = %+v, %v", result, err)
	}
}

func TestTerminalReconciliationStopsBeforeJournalAppend(t *testing.T) {
	store := testStore(t)
	stop := &stopStub{terminalErr: errors.New("control unavailable")}
	engine := &Engine{store: store, now: time.Now, stop: stop}
	_, err := engine.recordReconciliation(
		strings.Repeat("a", 64), testProfile(), &state{},
		txflow.Reconciliation{Verdict: txflow.VerdictFailed}, false,
	)
	if err == nil || len(store.Records()) != 0 || stop.terminalCalls != 1 {
		t.Fatalf("terminal error=%v records=%d stops=%d", err, len(store.Records()), stop.terminalCalls)
	}
}

func TestTerminalAcknowledgementIsExactDurableAndIdempotent(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "journal.jsonl")
	store, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	profile := testProfile()
	actionID := appendTerminalSwap(
		t, store, profile, profile.ScheduleAnchorUnix+3_600, txflow.VerdictDiverged,
	)
	stats, err := store.Stats()
	if err != nil || stats.ReservedBytes == 0 {
		t.Fatalf("terminal reserve = %+v, %v", stats, err)
	}
	if _, err := ValidateTerminalAcknowledgement(
		store, strings.Repeat("f", 64), "halted", "reviewed divergence",
	); err == nil {
		t.Fatal("wrong action ID was accepted")
	}
	if _, err := ValidateTerminalAcknowledgement(
		store, actionID, "failed", "reviewed divergence",
	); err == nil {
		t.Fatal("wrong outcome was accepted")
	}
	if _, err := ValidateTerminalAcknowledgement(store, actionID, "halted", " bad"); err == nil {
		t.Fatal("invalid reason was accepted")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	stats, err = store.Stats()
	if err != nil || stats.ReservedBytes == 0 {
		t.Fatalf("reopened terminal reserve = %+v, %v", stats, err)
	}
	acknowledgedAt := time.Unix(profile.ScheduleAnchorUnix+3_602, 0).UTC()
	appended, err := AcknowledgeTerminal(
		store, actionID, "halted", "reviewed divergence", acknowledgedAt,
	)
	if err != nil || !appended {
		t.Fatalf("first acknowledgement appended=%v err=%v", appended, err)
	}
	stats, err = store.Stats()
	if err != nil || stats.ReservedBytes != 0 {
		t.Fatalf("acknowledged reserve = %+v, %v", stats, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	appended, err = AcknowledgeTerminal(
		store, actionID, "halted", "reviewed divergence", acknowledgedAt,
	)
	if err != nil || appended {
		t.Fatalf("idempotent acknowledgement appended=%v err=%v", appended, err)
	}
	if _, err := AcknowledgeTerminal(
		store, actionID, "halted", "different review", acknowledgedAt,
	); err == nil {
		t.Fatal("different acknowledgement retry was accepted")
	}
	acknowledgements := 0
	for _, record := range store.Records() {
		if record.Type == EventTerminalAcknowledged {
			acknowledgements++
		}
	}
	if acknowledgements != 1 {
		t.Fatalf("acknowledgement records = %d", acknowledgements)
	}
	projection, err := (&Engine{store: store}).projectJournal()
	if err != nil || projection.states[actionID].acknowledgement.Reason != "reviewed divergence" {
		t.Fatalf("durable acknowledgement = %+v, %v", projection.states[actionID].acknowledgement, err)
	}
	now := acknowledgedAt
	stop := &stopStub{}
	engine := &Engine{
		store: store, stop: stop, now: func() time.Time { return now },
		clock: func() (clockcheck.Sample, error) { return validClockSample(now), nil },
	}
	result, err := engine.RunOnce(context.Background(), profile)
	if err != nil || result.Decision != "halted" || result.ActionID != actionID ||
		!result.Recovered || stop.terminalCalls != 1 ||
		stop.terminalAction != actionID || stop.terminalOutcome != "halted" {
		t.Fatalf("acknowledged current window = %+v, stops=%d, err=%v", result, stop.terminalCalls, err)
	}
}

func TestUnacknowledgedTerminalPrecedesWindowAndStopChecks(t *testing.T) {
	profile := testProfile()
	store := testStore(t)
	actionID := appendTerminalSwap(
		t, store, profile, profile.ScheduleAnchorUnix+3_600, txflow.VerdictFailed,
	)
	stop := &stopStub{stopped: true}
	engine := &Engine{store: store, stop: stop, now: func() time.Time {
		return time.Unix(profile.ScheduleAnchorUnix+50_000, 0).UTC()
	}}
	result, err := engine.RunOnce(context.Background(), profile)
	if err != nil || result.ActionID != actionID || result.Decision != "failed" || !result.Recovered {
		t.Fatalf("recovered terminal = %+v, %v", result, err)
	}
	if stop.terminalCalls != 1 || stop.terminalAction != actionID ||
		stop.terminalOutcome != "failed" {
		t.Fatalf("engine terminal repair = %+v", stop)
	}
}

func TestTerminalJournalRestoresMissingAndProvisionalControl(t *testing.T) {
	for _, test := range []struct {
		name           string
		initialOutcome string
		verdict        string
		wantOutcome    string
	}{
		{name: "missing to failed", verdict: txflow.VerdictFailed, wantOutcome: "failed"},
		{name: "halted to failed", initialOutcome: "halted", verdict: txflow.VerdictFailed, wantOutcome: "failed"},
		{name: "failed to halted", initialOutcome: "failed", verdict: txflow.VerdictDiverged, wantOutcome: "halted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := testProfile()
			store := testStore(t)
			actionID := appendTerminalSwap(
				t, store, profile, profile.ScheduleAnchorUnix+3_600, test.verdict,
			)
			fingerprint, err := profile.Fingerprint()
			if err != nil {
				t.Fatal(err)
			}
			stateFile, err := control.NewStateFile(
				filepath.Join(t.TempDir(), "control.json"), fingerprint, false,
			)
			if err != nil {
				t.Fatal(err)
			}
			if test.initialOutcome != "" {
				if err := stateFile.StopForTerminal(actionID, test.initialOutcome); err != nil {
					t.Fatal(err)
				}
			}
			engine := &Engine{store: store, stop: stateFile, now: time.Now}
			result, err := engine.RunOnce(context.Background(), profile)
			if err != nil || result.ActionID != actionID || !result.Recovered {
				t.Fatalf("restored result = %+v, %v", result, err)
			}
			status, err := stateFile.Status()
			if err != nil || status.Mode != control.ModeNoNewActions ||
				status.TerminalActionID != actionID ||
				status.TerminalOutcome != test.wantOutcome {
				t.Fatalf("restored status = %+v, %v", status, err)
			}
		})
	}
}

func TestReviewedHaltRestoresControlAfterStateLoss(t *testing.T) {
	profile := testProfile()
	store := testStore(t)
	actionID := appendTerminalSwap(
		t, store, profile, profile.ScheduleAnchorUnix+3_600, txflow.VerdictDiverged,
	)
	if appended, err := AcknowledgeTerminal(
		store, actionID, "halted", "reviewed divergent evidence", time.Now().UTC(),
	); err != nil || !appended {
		t.Fatalf("halt review appended=%v err=%v", appended, err)
	}
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "control.json")
	oldState, err := control.NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := oldState.StopForTerminal(actionID, "halted"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	reinitialized, err := control.NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{store: store, stop: reinitialized, now: time.Now}
	result, err := engine.RunOnce(context.Background(), profile)
	if err != nil || result.ActionID != actionID || result.Decision != "halted" || !result.Recovered {
		t.Fatalf("restored reviewed halt = %+v, %v", result, err)
	}
	status, err := reinitialized.Status()
	if err != nil || status.TerminalActionID != actionID || status.TerminalOutcome != "halted" {
		t.Fatalf("restored halted control = %+v, %v", status, err)
	}
	now := time.Now().UTC()
	if err := control.WriteDevnetActivation(
		path, fingerprint, now, now.Add(time.Minute), 1, "must remain blocked",
	); err == nil {
		t.Fatal("reviewed halted setup was enabled after control-state loss")
	}
}

func TestFinalizedJournalClearsMatchingProvisionalControl(t *testing.T) {
	profile := testProfile()
	store := testStore(t)
	actionID := appendTerminalSwap(
		t, store, profile, profile.ScheduleAnchorUnix+3_600, txflow.VerdictFinalized,
	)
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	stateFile, err := control.NewStateFile(
		filepath.Join(t.TempDir(), "control.json"), fingerprint, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := stateFile.StopForTerminal(actionID, "halted"); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{store: store, stop: stateFile, now: time.Now}
	result, err := engine.RunOnce(context.Background(), profile)
	if err != nil || result.Decision != "waiting" ||
		result.Reason != "independent recovery finalizer is pending" ||
		result.ActionID != actionID {
		t.Fatalf("finalized repair result = %+v, %v", result, err)
	}
	status, err := stateFile.Status()
	if err != nil || status.Mode != control.ModeNoNewActions ||
		status.TerminalActionID != actionID || status.TerminalOutcome != "halted" {
		t.Fatalf("finalized repair status = %+v, %v", status, err)
	}
}

func TestStatusProjectionClosesCompletedActionCrashWindow(t *testing.T) {
	profile := testProfile()
	store := testStore(t)
	actionID := appendTerminalSwap(
		t, store, profile, profile.ScheduleAnchorUnix+3_600, txflow.VerdictFinalized,
	)
	stats, err := store.Stats()
	if err != nil || stats.ReservedBytes == 0 {
		t.Fatalf("completed reserve = %+v, %v", stats, err)
	}
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	stateFile, err := control.NewStateFile(
		filepath.Join(t.TempDir(), "control.json"), fingerprint, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{store: store, stop: stateFile, now: time.Now}
	result, err := engine.RunOnce(context.Background(), profile)
	if err != nil || result.Decision != "complete" || !result.Recovered ||
		result.ActionID != actionID {
		t.Fatalf("unprojected result = %+v, %v", result, err)
	}
	appended, err := MarkStatusProjected(
		store, actionID, result.Decision, result.Verdict, time.Now().UTC(),
	)
	if err != nil || !appended {
		t.Fatalf("status marker appended=%v err=%v", appended, err)
	}
	stats, err = store.Stats()
	if err != nil || stats.ReservedBytes != 0 {
		t.Fatalf("projected reserve = %+v, %v", stats, err)
	}
	appended, err = MarkStatusProjected(
		store, actionID, result.Decision, result.Verdict, time.Now().UTC(),
	)
	if err != nil || appended {
		t.Fatalf("idempotent marker appended=%v err=%v", appended, err)
	}
	durable, durableAt, found, err := LatestDurableAction(
		store, profile, time.Now().UTC(),
	)
	if err != nil || !found || durable.ActionID != actionID ||
		durable.Decision != "complete" || durable.Verdict != txflow.VerdictFinalized ||
		durable.Recovered || durableAt.IsZero() {
		t.Fatalf("durable action = %+v at %s, found=%v err=%v", durable, durableAt, found, err)
	}
	result, err = engine.RunOnce(context.Background(), profile)
	if err != nil || result.Decision != "stopped" || result.ActionID != "" {
		t.Fatalf("post-projection result = %+v, %v", result, err)
	}
}

func TestRunOnceReleasesReserveLeftAfterProjectionCrash(t *testing.T) {
	profile := testProfile()
	store := testStore(t)
	actionID := appendTerminalSwap(
		t, store, profile, profile.ScheduleAnchorUnix+3_600, txflow.VerdictFinalized,
	)
	if _, err := store.Append(
		time.Now().UTC(), EventStatusProjected, actionID,
		statusProjection{Decision: "complete", Verdict: txflow.VerdictFinalized},
	); err != nil {
		t.Fatal(err)
	}
	stats, err := store.Stats()
	if err != nil || stats.ReservedBytes == 0 {
		t.Fatalf("crash reserve = %+v, %v", stats, err)
	}
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	stateFile, err := control.NewStateFile(
		filepath.Join(t.TempDir(), "control.json"), fingerprint, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{store: store, stop: stateFile, now: time.Now}
	result, err := engine.RunOnce(context.Background(), profile)
	if err != nil || result.Decision != "stopped" {
		t.Fatalf("reserve repair result = %+v, %v", result, err)
	}
	stats, err = store.Stats()
	if err != nil || stats.ReservedBytes != 0 {
		t.Fatalf("repaired reserve = %+v, %v", stats, err)
	}
}

func TestCanceledStatusIsReprojectedOnce(t *testing.T) {
	profile := testProfile()
	store := testStore(t)
	actionID := appendStart(t, store, profile, profile.ScheduleAnchorUnix+3_600)
	if err := store.EnsureCapacity(remainingRecords, remainingBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(
		time.Now().UTC(), EventCanceled, actionID,
		canceledRecord{Reason: "operator stopped before submission"},
	); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	stateFile, err := control.NewStateFile(
		filepath.Join(t.TempDir(), "control.json"), fingerprint, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{store: store, stop: stateFile, now: time.Now}
	result, err := engine.RunOnce(context.Background(), profile)
	if err != nil || result.Decision != "canceled" || !result.Recovered {
		t.Fatalf("unprojected cancellation = %+v, %v", result, err)
	}
	if _, err := MarkStatusProjected(
		store, actionID, result.Decision, result.Verdict, time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
	result, err = engine.RunOnce(context.Background(), profile)
	if err != nil || result.Decision != "stopped" {
		t.Fatalf("projected cancellation = %+v, %v", result, err)
	}
}

func TestTerminalJournalCannotReplaceDifferentActionLatch(t *testing.T) {
	profile := testProfile()
	store := testStore(t)
	actionID := appendTerminalSwap(
		t, store, profile, profile.ScheduleAnchorUnix+3_600, txflow.VerdictFailed,
	)
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	stateFile, err := control.NewStateFile(
		filepath.Join(t.TempDir(), "control.json"), fingerprint, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	otherActionID := strings.Repeat("f", 64)
	if otherActionID == actionID {
		t.Fatal("test action IDs unexpectedly match")
	}
	if err := stateFile.StopForTerminal(otherActionID, "halted"); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{store: store, stop: stateFile, now: time.Now}
	if _, err := engine.RunOnce(context.Background(), profile); err == nil {
		t.Fatal("journal terminal replaced a different action latch")
	}
	status, err := stateFile.Status()
	if err != nil || status.TerminalActionID != otherActionID ||
		status.TerminalOutcome != "halted" {
		t.Fatalf("preserved terminal status = %+v, %v", status, err)
	}
}

func TestTerminalRecoveryRejectsMalformedSchedule(t *testing.T) {
	profile := testProfile()
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	window := int64(profile.ScheduleWindowSeconds)
	base := profile.ScheduleAnchorUnix + window
	for _, shape := range []struct {
		name    string
		started startedRecord
		now     time.Time
	}{
		{name: "wrong end", started: startedRecord{
			ProfileFingerprint: fingerprint, ScheduleWindowStartUnix: base,
			ScheduleWindowEndUnix: base + window + 1, ObservationSlot: 100,
		}, now: time.Unix(base+10, 0).UTC()},
		{name: "unaligned start", started: startedRecord{
			ProfileFingerprint: fingerprint, ScheduleWindowStartUnix: base + 1,
			ScheduleWindowEndUnix: base + 1 + window, ObservationSlot: 100,
		}, now: time.Unix(base+10, 0).UTC()},
		{name: "future start", started: startedRecord{
			ProfileFingerprint: fingerprint, ScheduleWindowStartUnix: base + 2*window,
			ScheduleWindowEndUnix: base + 3*window, ObservationSlot: 100,
		}, now: time.Unix(base+10, 0).UTC()},
	} {
		for _, recovery := range []struct {
			name        string
			verdict     string
			provisional bool
		}{
			{name: "unacknowledged", verdict: txflow.VerdictFailed},
			{name: "unprojected finalized", verdict: txflow.VerdictFinalized},
			{name: "provisional finalized", verdict: txflow.VerdictFinalized, provisional: true},
		} {
			t.Run(shape.name+"/"+recovery.name, func(t *testing.T) {
				store := testStore(t)
				actionID := appendTerminalSwapWithStart(
					t, store, profile, shape.started, recovery.verdict,
				)
				stateFile, err := control.NewStateFile(
					filepath.Join(t.TempDir(), "control.json"), fingerprint, false,
				)
				if err != nil {
					t.Fatal(err)
				}
				if recovery.provisional {
					if err := stateFile.StopForTerminal(actionID, "halted"); err != nil {
						t.Fatal(err)
					}
				}
				engine := &Engine{store: store, stop: stateFile, now: func() time.Time {
					return shape.now
				}}
				if _, err := engine.RunOnce(context.Background(), profile); err == nil {
					t.Fatal("malformed terminal schedule was accepted")
				}
				status, err := stateFile.Status()
				if err != nil {
					t.Fatal(err)
				}
				if recovery.provisional &&
					(status.TerminalActionID != actionID || status.TerminalOutcome != "halted") {
					t.Fatalf("provisional latch changed = %+v", status)
				}
			})
		}
	}
}

func TestLatestDurableActionRejectsInvalidJournalTimes(t *testing.T) {
	profile := testProfile()
	windowStart := profile.ScheduleAnchorUnix + int64(profile.ScheduleWindowSeconds)
	t.Run("terminal before start event", func(t *testing.T) {
		store := testStore(t)
		actionID := appendStart(t, store, profile, windowStart)
		before := len(store.Records())
		if _, err := store.Append(
			time.Unix(windowStart-1, 0).UTC(), EventCanceled, actionID,
			canceledRecord{Reason: "operator stopped before submission"},
		); err == nil {
			t.Fatal("reversed terminal timestamp was accepted")
		}
		if len(store.Records()) != before {
			t.Fatal("rejected terminal changed the journal")
		}
	})

	t.Run("terminal in future", func(t *testing.T) {
		store := testStore(t)
		actionID := appendStart(t, store, profile, windowStart)
		future := time.Unix(windowStart+3_600, 0).UTC()
		if _, err := store.Append(
			future, EventCanceled, actionID,
			canceledRecord{Reason: "operator stopped before submission"},
		); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := LatestDurableAction(
			store, profile, time.Unix(windowStart+10, 0).UTC(),
		); err == nil {
			t.Fatal("future terminal timestamp was accepted")
		}
	})

	t.Run("projection time before terminal", func(t *testing.T) {
		store := testStore(t)
		actionID := appendTerminalSwap(
			t, store, profile, windowStart, txflow.VerdictFinalized,
		)
		before := len(store.Records())
		if _, err := MarkStatusProjected(
			store, actionID, "complete", txflow.VerdictFinalized,
			time.Unix(windowStart, 0).UTC(),
		); err == nil {
			t.Fatal("reversed projection timestamp was accepted")
		}
		if len(store.Records()) != before {
			t.Fatal("rejected projection changed the journal")
		}
	})

	t.Run("acknowledgement time before terminal", func(t *testing.T) {
		store := testStore(t)
		actionID := appendTerminalSwap(
			t, store, profile, windowStart, txflow.VerdictFailed,
		)
		before := len(store.Records())
		if _, err := AcknowledgeTerminal(
			store, actionID, "failed", "reviewed failure",
			time.Unix(windowStart, 0).UTC(),
		); err == nil {
			t.Fatal("reversed acknowledgement timestamp was accepted")
		}
		if len(store.Records()) != before {
			t.Fatal("rejected acknowledgement changed the journal")
		}
	})
}

func BenchmarkProjectJournalManyActions(b *testing.B) {
	store, err := journal.Open(filepath.Join(b.TempDir(), "journal.jsonl"))
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	for index := 0; index < 10_000; index++ {
		if _, err := store.Append(
			time.Unix(int64(index+1), 0).UTC(), EventObservationFailed,
			fmt.Sprintf("%064x", index+1), observationFailure{Reason: "observer_error"},
		); err != nil {
			b.Fatal(err)
		}
	}
	engine := &Engine{store: store}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := engine.projectJournal(); err != nil {
			b.Fatal(err)
		}
	}
}

func TestRecoveredSendRechecksLocalGenesisBeforeResubmission(t *testing.T) {
	profile := testProfile()
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	store := testStore(t)
	stop := &stopStub{}
	submitter := &submitterStub{barrier: stop}
	wantErr := errors.New("wrong local genesis")
	transactor := &transactorStub{
		verifyGenesisErr: wantErr,
		reconciliation: txflow.Reconciliation{
			Signature: "signature", Verdict: txflow.VerdictPending,
		},
	}
	engine := &Engine{
		store: store, observer: &observerStub{observation: healthyObservation(profile, now)},
		submitter: submitter, tx: transactor, stop: stop,
		now: func() time.Time { return now },
		clock: func() (clockcheck.Sample, error) {
			return validClockSample(now), nil
		},
	}
	current := recoveredSendState(t, profile, now)
	_, err := engine.submitAndReconcile(
		context.Background(), strings.Repeat("a", 64), profile, current, true,
	)
	if !errors.Is(err, wantErr) || transactor.verifyGenesisCalls != 1 || submitter.calls != 0 {
		t.Fatalf(
			"recovery error=%v genesis_checks=%d submissions=%d",
			err, transactor.verifyGenesisCalls, submitter.calls,
		)
	}
}

func TestRecoveredSendRechecksWhirlpoolBeforeResubmission(t *testing.T) {
	profile := testProfile()
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	stop := &stopStub{}
	submitter := &submitterStub{barrier: stop}
	wantErr := errors.New("Whirlpool deployment changed")
	transactor := &transactorStub{
		verifyDeploymentErr: wantErr,
		reconciliation: txflow.Reconciliation{
			Signature: "signature", Verdict: txflow.VerdictPending,
		},
	}
	engine := &Engine{
		store: testStore(t), observer: &observerStub{observation: healthyObservation(profile, now)},
		submitter: submitter, tx: transactor, stop: stop,
		now: func() time.Time { return now },
		clock: func() (clockcheck.Sample, error) {
			return validClockSample(now), nil
		},
	}
	current := recoveredSendState(t, profile, now)
	_, err := engine.submitAndReconcile(
		t.Context(), strings.Repeat("a", 64), profile, current, true,
	)
	if !errors.Is(err, wantErr) || transactor.verifyDeploymentCalls != 1 || submitter.calls != 0 {
		t.Fatalf(
			"error=%v deployment_checks=%d submissions=%d",
			err, transactor.verifyDeploymentCalls, submitter.calls,
		)
	}
}

func TestRecoveredSendRechecksOutputAccountRentBeforeResubmission(t *testing.T) {
	profile := testProfile()
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	stop := &stopStub{}
	submitter := &submitterStub{barrier: stop}
	wantErr := errors.New("token-account rent changed")
	transactor := &transactorStub{
		verifyRentErr: wantErr,
		reconciliation: txflow.Reconciliation{
			Signature: "signature", Verdict: txflow.VerdictPending,
		},
	}
	engine := &Engine{
		store: testStore(t), observer: &observerStub{observation: healthyObservation(profile, now)},
		submitter: submitter, tx: transactor, stop: stop,
		now: func() time.Time { return now },
		clock: func() (clockcheck.Sample, error) {
			return validClockSample(now), nil
		},
	}
	current := recoveredSendState(t, profile, now)
	current.built.OutputAccountCreated = true
	current.built.OutputAccountRent = 2_039_280
	_, err := engine.submitAndReconcile(
		t.Context(), strings.Repeat("a", 64), profile, current, true,
	)
	if !errors.Is(err, wantErr) || transactor.verifyRentCalls != 1 || submitter.calls != 0 {
		t.Fatalf(
			"error=%v rent_checks=%d submissions=%d",
			err, transactor.verifyRentCalls, submitter.calls,
		)
	}
}

func TestStoppedRecoveredSendReconcilesWithoutClockOrResubmission(t *testing.T) {
	profile := testProfile()
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	profile, actionID, current := recoveredSwapFixture(t, now)
	store := testStore(t)
	appendRecoveredSwap(t, store, actionID, current)
	clockCalls := 0
	submitter := &submitterStub{}
	transactor := &transactorStub{}
	engine := &Engine{
		store: store, submitter: submitter, tx: transactor,
		stop: &stopStub{stopped: true}, now: func() time.Time { return now },
		clock: func() (clockcheck.Sample, error) {
			clockCalls++
			return clockcheck.Sample{}, errors.New("clock unavailable")
		},
	}

	result, err := engine.RunOnce(context.Background(), profile)
	if err != nil || result.Decision != "complete" || result.Verdict != txflow.VerdictFinalized {
		t.Fatalf("recovered result = %+v, %v", result, err)
	}
	if clockCalls != 0 || submitter.calls != 0 || transactor.verifyEvidenceGenesisCalls != 1 {
		t.Fatalf(
			"reconciliation used clock %d times, submitter %d times, evidence genesis %d times",
			clockCalls, submitter.calls, transactor.verifyEvidenceGenesisCalls,
		)
	}
}

func TestRecoveredResubmissionReleasesRunnerBarrierBeforeSubmitter(t *testing.T) {
	profile := testProfile()
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	store := testStore(t)
	stop := &stopStub{}
	submitter := &submitterStub{barrier: stop}
	transactor := &transactorStub{reconciliation: txflow.Reconciliation{
		Signature: "signature", Verdict: txflow.VerdictPending,
	}}
	engine := &Engine{
		store: store, observer: &observerStub{observation: healthyObservation(profile, now)},
		submitter: submitter, tx: transactor, stop: stop,
		now: func() time.Time { return now },
		clock: func() (clockcheck.Sample, error) {
			return validClockSample(now), nil
		},
	}
	current := recoveredSendState(t, profile, now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := engine.submitAndReconcile(
		ctx, strings.Repeat("a", 64), profile, current, true,
	)
	if err != nil || result.Decision != "pending" || submitter.calls != 1 ||
		stop.barriers != 1 || !submitter.outsideBarrier {
		t.Fatalf(
			"result=%+v error=%v submissions=%d barriers=%d outside=%t",
			result, err, submitter.calls, stop.barriers, submitter.outsideBarrier,
		)
	}
}

func TestRecoveredResubmissionUsesConsumedRealControlAction(t *testing.T) {
	profile := testProfile()
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	controlNow := time.Now().UTC()
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	actionID, err := orcaswap.ComputeActionID(
		fingerprint,
		profile.ScheduleAnchorUnix+3_600,
	)
	if err != nil {
		t.Fatal(err)
	}
	controlPath := filepath.Join(t.TempDir(), "control.json")
	if err := control.WriteDevnetActivation(
		controlPath,
		fingerprint,
		controlNow.Add(-time.Minute),
		controlNow.Add(time.Hour),
		1,
		"one recovery test action",
	); err != nil {
		t.Fatal(err)
	}
	admission, err := control.NewStateFile(controlPath, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if blocked, err := admission.WithSendBarrier(actionID, func() error { return nil }); err != nil || blocked {
		t.Fatalf("initial send barrier = %v, %v", blocked, err)
	}
	recovery, err := control.NewStateFile(controlPath, fingerprint, true)
	if err != nil {
		t.Fatal(err)
	}
	submitter := &submitterStub{}
	engine := &Engine{
		store: testStore(t), observer: &observerStub{observation: healthyObservation(profile, now)},
		submitter: submitter,
		tx: &transactorStub{reconciliation: txflow.Reconciliation{
			Signature: "signature", Verdict: txflow.VerdictPending,
		}},
		stop: recovery, now: func() time.Time { return now },
		clock: func() (clockcheck.Sample, error) {
			return validClockSample(now), nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := engine.submitAndReconcile(
		ctx, actionID, profile, recoveredSendState(t, profile, now), true,
	)
	if err != nil || result.Decision != "pending" || submitter.calls != 1 {
		t.Fatalf("recovered result = %+v, error %v, submissions %d", result, err, submitter.calls)
	}
	if blocked, err := recovery.WithRecoverySendBarrier(actionID, func() error { return nil }); err != nil || blocked {
		t.Fatalf("recovery consumed or lost original authority = %v, %v", blocked, err)
	}
}

func TestStoppedRecoveredPendingSendDoesNotResubmit(t *testing.T) {
	profile := testProfile()
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	submitter := &submitterStub{}
	engine := &Engine{
		store: testStore(t), submitter: submitter,
		tx: &transactorStub{reconciliation: txflow.Reconciliation{
			Signature: "signature", Verdict: txflow.VerdictPending,
		}},
		stop: &stopStub{stopped: true}, now: func() time.Time { return now },
	}

	result, err := engine.submitAndReconcile(
		context.Background(), strings.Repeat("a", 64), profile,
		recoveredSendState(t, profile, now), true,
	)
	if err != nil || result.Decision != "pending" || result.Submitted ||
		result.Verdict != txflow.VerdictPending || submitter.calls != 0 {
		t.Fatalf("result=%+v error=%v submissions=%d", result, err, submitter.calls)
	}
}

func recoveredSendState(t *testing.T, profile Profile, now time.Time) *state {
	t.Helper()
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return &state{
		started: &startedRecord{
			ProfileFingerprint:      fingerprint,
			ScheduleWindowStartUnix: profile.ScheduleAnchorUnix + 3_600,
			ScheduleWindowEndUnix:   profile.ScheduleAnchorUnix + 7_200,
			ObservationSlot:         100,
		},
		built: &builtRecord{
			LastValidBlockHeight: 200, FeeLamports: 5_000,
			BlockhashContextSlot: 100, MinimumOutput: 21_525,
		},
		signed: &signedRecord{Response: signer.Response{
			Signature: "signature", TransactionSHA256: strings.Repeat("1", 64),
		}},
		sendStarted:   &sendStartedRecord{Signature: "signature"},
		sendStartedAt: now.Add(-time.Second),
	}
}

func validClockSample(now time.Time) clockcheck.Sample {
	return clockcheck.Sample{
		WallTime: now, BootID: "00000000-0000-0000-0000-000000000001",
		MonotonicNanos: uint64(time.Second), UncertaintyNanos: uint64(10 * time.Millisecond),
	}
}

type observerStub struct {
	observation  agent.NodeObservation
	afterObserve func()
	calls        int
}

func (stub *observerStub) Observe(context.Context, string) (agent.NodeObservation, error) {
	stub.calls++
	if stub.afterObserve != nil {
		stub.afterObserve()
	}
	return stub.observation, nil
}

type quoteStub struct{ result swapbuilder.Result }

func (stub quoteStub) Quote(context.Context, swapbuilder.Request) (swapbuilder.Result, error) {
	return stub.result, nil
}

type priceTriggerStub struct {
	evidence  pricetrigger.Evidence
	evidences []pricetrigger.Evidence
	err       error
	calls     int
	// slots records the slot supplied to each call so a test can prove the
	// authorizing read was bound and the advisory read was not.
	slots []uint64
}

func (stub *priceTriggerStub) Evaluate(
	context.Context,
	pricetrigger.Policy,
) (pricetrigger.Evidence, error) {
	stub.calls++
	if len(stub.evidences) != 0 {
		index := stub.calls - 1
		if index >= len(stub.evidences) {
			index = len(stub.evidences) - 1
		}
		return stub.evidences[index], stub.err
	}
	return stub.evidence, stub.err
}

func (stub *priceTriggerStub) EvaluateAtSlot(
	ctx context.Context,
	policy pricetrigger.Policy,
	minContextSlot uint64,
) (pricetrigger.Evidence, error) {
	stub.slots = append(stub.slots, minContextSlot)
	return stub.Evaluate(ctx, policy)
}

type blockhashStub struct {
	latest solanarpc.LatestBlockhash
	height uint64
}

// The real client REFUSES a zero minimum context slot (solanarpc/client.go:253):
// binding one is how an authorizing caller proves which slot it read at. A stub
// that ignored the argument let a caller pass 0 and still look correct in
// tests while always failing in production.
func (stub blockhashStub) LatestBlockhash(_ context.Context, minContextSlot uint64) (solanarpc.LatestBlockhash, error) {
	if minContextSlot == 0 {
		return solanarpc.LatestBlockhash{}, errors.New("minimum blockhash context slot is required")
	}
	return stub.latest, nil
}

func (stub blockhashStub) BlockHeight(context.Context) (uint64, error) { return stub.height, nil }

type authorityStub struct{}

func (authorityStub) Authorize(context.Context, signer.Request) (riskgrant.Grant, error) {
	return riskgrant.Grant{}, nil
}

type signerStub struct {
	response signer.Response
	valid    bool
	mutate   func(signer.Request, *signer.Response) error
}

func (stub signerStub) Sign(_ context.Context, request signer.Request) (signer.Response, error) {
	if stub.valid {
		message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
		if err != nil {
			return signer.Response{}, err
		}
		seedText := "swap-profile-owner"
		if request.Profile == orcaswap.BuyProfileName {
			seedText = "buy-profile-owner"
		}
		seed := sha256.Sum256([]byte(seedText))
		privateKey := ed25519.NewKeyFromSeed(seed[:])
		transaction, signature, err := solana.SignLegacyMessage(privateKey, message)
		if err != nil {
			return signer.Response{}, err
		}
		messageHash := sha256.Sum256(message)
		transactionHash := sha256.Sum256(transaction)
		binding, err := signer.RiskBinding(request, hex.EncodeToString(messageHash[:]))
		if err != nil {
			return signer.Response{}, err
		}
		response := signer.Response{
			ActionID: request.ActionID, Signature: solana.Encode(signature[:]),
			RequestSHA256:        binding.RequestSHA256,
			MessageSHA256:        hex.EncodeToString(messageHash[:]),
			TransactionSHA256:    hex.EncodeToString(transactionHash[:]),
			BlockhashContextSlot: request.BlockhashContextSlot,
			FeeLamports:          request.FeeLamports, LastValidBlockHeight: request.LastValidBlockHeight,
		}
		_, submitterPublicKey, err := sealedtx.GenerateKey(nil)
		if err != nil {
			return signer.Response{}, err
		}
		response.SealedTransaction, err = sealedtx.Seal(
			submitterPublicKey, sealedtx.Metadata{
				Version: sealedtx.Version, Domain: sealedtx.Domain,
				ActionID: response.ActionID, MessageSHA256: response.MessageSHA256,
				TransactionSHA256: response.TransactionSHA256, Signature: response.Signature,
				BlockhashContextSlot: response.BlockhashContextSlot,
				FeeLamports:          response.FeeLamports,
				LastValidBlockHeight: response.LastValidBlockHeight,
			}, transaction, nil,
		)
		if err != nil {
			return signer.Response{}, err
		}
		response.SignerAttestation, err = signer.AttestResponse(
			privateKey, submitterPublicKey, response,
		)
		if stub.mutate != nil {
			if err := stub.mutate(request, &response); err != nil {
				return signer.Response{}, err
			}
		}
		return response, err
	}
	response := stub.response
	if response.BlockhashContextSlot == 0 {
		response.BlockhashContextSlot = request.BlockhashContextSlot
	}
	return response, nil
}

func TestEngineRejectsFreshMalformedSignerResponseBeforeJournal(t *testing.T) {
	profile := testProfile()
	now := time.Unix(profile.ScheduleAnchorUnix+3_601, 0).UTC()
	store := testStore(t)
	stop := &stopStub{}
	submitter := &submitterStub{barrier: stop}
	observer := &observerStub{observation: healthyObservation(profile, now)}
	engine, err := New(
		store, observer, quoteStub{result: swapQuote(profile)},
		blockhashStub{latest: solanarpc.LatestBlockhash{
			ContextSlot: 101, Blockhash: solana.Encode(bytes.Repeat([]byte{7}, 32)),
			LastValidBlockHeight: 250,
		}, height: 100}, authorityStub{}, signerStub{valid: true, mutate: func(
			request signer.Request,
			response *signer.Response,
		) error {
			message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
			if err != nil {
				return err
			}
			invalidSignature := make([]byte, ed25519.SignatureSize)
			transaction := append([]byte{1}, invalidSignature...)
			transaction = append(transaction, message...)
			transactionHash := sha256.Sum256(transaction)
			response.Signature = solana.Encode(invalidSignature)
			response.TransactionSHA256 = hex.EncodeToString(transactionHash[:])
			response.SealedTransaction.Metadata.Signature = response.Signature
			response.SealedTransaction.Metadata.TransactionSHA256 = response.TransactionSHA256
			response.SealedTransaction, err = sealedtx.Seal(
				response.SignerAttestation.SubmitterPublicKey,
				response.SealedTransaction.Metadata,
				transaction,
				nil,
			)
			if err != nil {
				return err
			}
			seed := sha256.Sum256([]byte("swap-profile-owner"))
			response.SignerAttestation, err = signer.AttestResponse(
				ed25519.NewKeyFromSeed(seed[:]),
				response.SignerAttestation.SubmitterPublicKey,
				*response,
			)
			return err
		}}, submitter, &transactorStub{}, stop, func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	start := now
	monotonic := uint64(time.Second)
	engine.clock = func() (clockcheck.Sample, error) {
		monotonic++
		return clockcheck.Sample{
			WallTime: now, BootID: "00000000-0000-0000-0000-000000000001",
			MonotonicNanos:   monotonic + uint64(now.Sub(start)),
			UncertaintyNanos: uint64(10 * time.Millisecond),
		}, nil
	}
	if result, err := engine.RunOnce(t.Context(), profile); err != nil || result.Decision != "waiting" {
		t.Fatalf("first result = %+v, %v", result, err)
	}
	now = now.Add(6 * time.Second)
	observer.observation = healthyObservation(profile, now)
	observer.observation.Account.Slot++
	if _, err := engine.RunOnce(t.Context(), profile); err == nil {
		t.Fatal("malformed fresh signer response was accepted")
	}
	if submitter.calls != 0 || stop.barriers != 0 {
		t.Fatalf("malformed response reached send: submissions=%d barriers=%d", submitter.calls, stop.barriers)
	}
	for _, record := range store.Records() {
		if record.Type == EventSigned || record.Type == EventSendStarted {
			t.Fatalf("malformed response wrote %s", record.Type)
		}
	}
}

type submitterStub struct {
	calls          int
	barrier        *stopStub
	outsideBarrier bool
	responses      []signer.Response
	minSlots       []uint64
	trace          *[]string
}

func (stub *submitterStub) Submit(
	_ context.Context,
	response signer.Response,
	minContextSlot uint64,
) (txflow.Submission, error) {
	stub.calls++
	stub.responses = append(stub.responses, response)
	stub.minSlots = append(stub.minSlots, minContextSlot)
	if stub.trace != nil {
		*stub.trace = append(*stub.trace, "submit")
	}
	if stub.barrier != nil && !stub.barrier.inside {
		stub.outsideBarrier = true
	}
	return txflow.Submission{
		Signature: response.Signature, LastValidBlockHeight: response.LastValidBlockHeight,
		State: txflow.StateAccepted,
	}, nil
}

type stopStub struct {
	barriers        int
	inside          bool
	stopped         bool
	barrierErr      error
	terminalCalls   int
	terminalAction  string
	terminalOutcome string
	terminalErr     error
	clearCalls      int
	clearErr        error
}

func (stub *stopStub) NoNewActions() (bool, error) { return stub.stopped, nil }

func (stub *stopStub) StopForTerminal(actionID, outcome string) error {
	stub.terminalCalls++
	stub.terminalAction = actionID
	stub.terminalOutcome = outcome
	if stub.terminalErr != nil {
		return stub.terminalErr
	}
	stub.stopped = true
	return nil
}

func (stub *stopStub) TerminalLatch() (string, string, error) {
	return stub.terminalAction, stub.terminalOutcome, nil
}

func (stub *stopStub) ClearTerminalForFinalized(actionID string) error {
	stub.clearCalls++
	if stub.clearErr != nil {
		return stub.clearErr
	}
	if stub.terminalAction == "" {
		return nil
	}
	if stub.terminalAction != actionID {
		return errors.New("different terminal action")
	}
	stub.terminalAction = ""
	stub.terminalOutcome = ""
	stub.stopped = true
	return nil
}

func (stub *stopStub) WithSendBarrier(_ string, operation func() error) (bool, error) {
	if stub.stopped {
		return true, nil
	}
	stub.barriers++
	if stub.barrierErr != nil {
		return false, stub.barrierErr
	}
	stub.inside = true
	defer func() { stub.inside = false }()
	return false, operation()
}

func (stub *stopStub) WithRecoverySendBarrier(
	_ string,
	operation func() error,
) (bool, error) {
	return stub.WithSendBarrier("", operation)
}

func TestStoppedFreshRunSkipsClockAndRPC(t *testing.T) {
	profile := testProfile()
	clockCalls := 0
	transactor := &transactorStub{}
	engine := &Engine{
		store: testStore(t), tx: transactor, stop: &stopStub{stopped: true},
		now: time.Now,
		clock: func() (clockcheck.Sample, error) {
			clockCalls++
			return clockcheck.Sample{}, errors.New("clock unavailable")
		},
	}

	result, err := engine.RunOnce(context.Background(), profile)
	if err != nil || result.Decision != "stopped" {
		t.Fatalf("stopped result = %+v, %v", result, err)
	}
	if clockCalls != 0 || transactor.verifyGenesisCalls != 0 {
		t.Fatalf("stopped run used clock %d times and RPC %d times", clockCalls, transactor.verifyGenesisCalls)
	}
}

func TestStopCancelsRecoveredUnsentSwapBeforeClockAndRPC(t *testing.T) {
	profile := testProfile()
	store := testStore(t)
	actionID := appendStart(t, store, profile, profile.ScheduleAnchorUnix+3_600)
	clockCalls := 0
	transactor := &transactorStub{}
	engine := &Engine{
		store: store, tx: transactor, stop: &stopStub{stopped: true}, now: time.Now,
		clock: func() (clockcheck.Sample, error) {
			clockCalls++
			return clockcheck.Sample{}, errors.New("clock unavailable")
		},
	}

	result, err := engine.RunOnce(context.Background(), profile)
	if err != nil || result.Decision != "canceled" || result.ActionID != actionID ||
		result.Reason != "operator stopped before submission" {
		t.Fatalf("canceled result = %+v, %v", result, err)
	}
	if clockCalls != 0 || transactor.verifyGenesisCalls != 0 {
		t.Fatalf("canceled run used clock %d times and RPC %d times", clockCalls, transactor.verifyGenesisCalls)
	}
	records := store.Records()
	if records[len(records)-1].Type != EventCanceled {
		t.Fatalf("last event = %q", records[len(records)-1].Type)
	}
}

type transactorStub struct {
	verifyGenesisErr           error
	verifyGenesisCalls         int
	verifyEvidenceGenesisCalls int
	verifyDeploymentErr        error
	verifyDeploymentErrors     []error
	verifyDeploymentCalls      int
	verifyDeploymentSlots      []uint64
	verifyRentErr              error
	verifyRentErrors           []error
	verifyRentCalls            int
	reconciliation             txflow.Reconciliation
	reconciliations            []txflow.Reconciliation
	reconciliationCalls        int
	trace                      *[]string
	buySubmissions             []txflow.Submission
	buyExpected                []txflow.ExpectedBuy
	buyFees                    []uint64
	verifyTokenInputCalls      int
}

func (stub *transactorStub) VerifyGenesis(context.Context, string) error {
	stub.verifyGenesisCalls++
	return stub.verifyGenesisErr
}
func (stub *transactorStub) VerifyWhirlpoolDeployment(
	_ context.Context,
	_ orcaswap.Policy,
	slot uint64,
) error {
	stub.verifyDeploymentCalls++
	stub.verifyDeploymentSlots = append(stub.verifyDeploymentSlots, slot)
	if len(stub.verifyDeploymentErrors) != 0 {
		err := stub.verifyDeploymentErrors[0]
		stub.verifyDeploymentErrors = stub.verifyDeploymentErrors[1:]
		return err
	}
	return stub.verifyDeploymentErr
}
func (stub *transactorStub) VerifyWhirlpoolBuyDeployment(
	_ context.Context,
	_ orcaswap.BuyPolicyV2,
	slot uint64,
) error {
	stub.verifyDeploymentCalls++
	stub.verifyDeploymentSlots = append(stub.verifyDeploymentSlots, slot)
	return stub.verifyDeploymentErr
}
func (stub *transactorStub) VerifyTokenInputAccount(
	_ context.Context,
	_, _, _ string,
	minimumAmount,
	minContextSlot uint64,
) (txflow.TokenAccountEvidence, error) {
	stub.verifyTokenInputCalls++
	return txflow.TokenAccountEvidence{
		Amount:             minimumAmount,
		PrimaryContextSlot: minContextSlot, SecondaryContextSlot: minContextSlot,
	}, nil
}
func (stub *transactorStub) VerifyTokenAccountRent(context.Context, uint64) (txflow.RentEvidence, error) {
	stub.verifyRentCalls++
	if len(stub.verifyRentErrors) != 0 {
		err := stub.verifyRentErrors[0]
		stub.verifyRentErrors = stub.verifyRentErrors[1:]
		if err != nil {
			return txflow.RentEvidence{}, err
		}
	}
	return txflow.RentEvidence{
		Lamports: 2_039_280, PrimaryLamports: 2_039_280, SecondaryLamports: 2_039_280,
	}, stub.verifyRentErr
}
func (stub *transactorStub) VerifyEvidenceGenesis(context.Context, string) error {
	stub.verifyEvidenceGenesisCalls++
	return nil
}
func (*transactorStub) BlockhashExpired(context.Context, uint64) (bool, error) {
	return false, nil
}
func (*transactorStub) FeeForMessage(context.Context, []byte, uint64) (txflow.FeeEvidence, error) {
	return txflow.FeeEvidence{
		Lamports: 5_000, MinContextSlot: 101,
		PrimaryContextSlot: 101, SecondaryContextSlot: 101,
	}, nil
}
func (*transactorStub) SimulateLegacy(
	context.Context,
	[]byte,
	uint64,
) (txflow.LegacySimulationEvidence, error) {
	return txflow.LegacySimulationEvidence{
		ProviderIdentity: "mithril", MinContextSlot: 101, ContextSlot: 101,
		LogsSHA256: "1111111111111111111111111111111111111111111111111111111111111111",
	}, nil
}
func (stub *transactorStub) ReconcileSwapExpected(
	_ context.Context,
	submission txflow.Submission,
	expected txflow.ExpectedSwap,
	fee uint64,
) (txflow.Reconciliation, error) {
	stub.reconciliationCalls++
	if len(stub.reconciliations) != 0 {
		result := stub.reconciliations[0]
		stub.reconciliations = stub.reconciliations[1:]
		if stub.trace != nil {
			*stub.trace = append(*stub.trace, "reconcile:"+result.Verdict)
		}
		return result, nil
	}
	if stub.reconciliation.Verdict != "" {
		return stub.reconciliation, nil
	}
	return txflow.Reconciliation{
		Signature: submission.Signature, Verdict: txflow.VerdictFinalized, Slot: 120,
		PrimaryFound: true, SecondaryFound: true,
		PrimarySlot: 120, SecondarySlot: 120,
		PrimaryStatus: "finalized", SecondaryStatus: "finalized",
		SwapEffects: &txflow.SwapEffectEvidence{
			TransactionSHA256: expected.TransactionSHA256, FeeLamports: fee,
			InputAmount: expected.InputAmount, MinimumOutput: expected.MinimumOutput,
			OutputAmount: 21_600, PrimaryEffectSlot: 120, SecondaryEffectSlot: 120,
		},
	}, nil
}

func (stub *transactorStub) ReconcileBuyExpected(
	_ context.Context,
	submission txflow.Submission,
	expected txflow.ExpectedBuy,
	fee uint64,
) (txflow.Reconciliation, error) {
	stub.reconciliationCalls++
	stub.buySubmissions = append(stub.buySubmissions, submission)
	stub.buyExpected = append(stub.buyExpected, expected)
	stub.buyFees = append(stub.buyFees, fee)
	if len(stub.reconciliations) != 0 {
		result := stub.reconciliations[0]
		stub.reconciliations = stub.reconciliations[1:]
		if stub.trace != nil {
			*stub.trace = append(*stub.trace, "reconcile:"+result.Verdict)
		}
		return result, nil
	}
	if stub.reconciliation.Verdict != "" {
		return stub.reconciliation, nil
	}
	return txflow.Reconciliation{
		Signature: submission.Signature, Verdict: txflow.VerdictFinalized, Slot: 120,
		PrimaryFound: true, SecondaryFound: true,
		PrimarySlot: 120, SecondarySlot: 120,
		PrimaryStatus: "finalized", SecondaryStatus: "finalized",
		BuyEffects: &txflow.BuyEffectEvidence{
			TransactionSHA256: expected.TransactionSHA256, FeeLamports: fee,
			InputAmount: expected.InputAmount, MinimumOutput: expected.MinimumOutput,
			OutputLamports:    expected.MinimumOutput + 7,
			PrimaryEffectSlot: 120, SecondaryEffectSlot: 120,
		},
	}, nil
}

func healthyObservation(profile Profile, now time.Time) agent.NodeObservation {
	needed := profile.ReserveLamports + profile.MaxFeeLamports + profile.maxRouteRent()
	if !profile.isBuy() {
		needed += profile.InputLamports
	}
	return agent.NodeObservation{
		Account: agent.Observation{
			Cluster: profile.Cluster, Source: profile.owner(), BalanceLamports: needed,
			Slot: 100, ObservedAt: now, EvidenceSource: "mithril_mcp",
			Finality: "local_unfinalized", Consistency: "node_reported_non_atomic",
		},
		Health: agent.NodeHealth{
			Status: "healthy", AssessmentScope: "point_in_time_snapshot",
			ObservedAt: now, EvidenceComplete: true,
			CrossCheck: &agent.SlotComparison{
				MithrilSlot: 100, ReferenceSlot: 100, ReferenceCommitment: "confirmed",
				MithrilView: "local_unfinalized_head", Threshold: 150, Status: "in_sync",
			},
		},
	}
}

func swapQuote(profile Profile) swapbuilder.Result {
	ata := func(tokenAccount, mint string) solana.Instruction {
		return solana.Instruction{
			Program: orcaswap.AssociatedTokenProgram,
			Accounts: []solana.AccountMeta{
				{Address: profile.Route.Owner, Signer: true, Writable: true},
				{Address: tokenAccount, Writable: true},
				{Address: profile.Route.Owner}, {Address: mint},
				{Address: orcaswap.SystemProgram}, {Address: orcaswap.TokenProgram},
			},
			Data: []byte{1},
		}
	}
	transferData := make([]byte, 12)
	binary.LittleEndian.PutUint32(transferData[:4], 2)
	binary.LittleEndian.PutUint64(transferData[4:], profile.InputLamports)
	swapData := make([]byte, 49)
	copy(swapData, []byte{43, 4, 237, 11, 26, 201, 30, 98})
	binary.LittleEndian.PutUint64(swapData[8:16], profile.InputLamports)
	binary.LittleEndian.PutUint64(swapData[16:24], 21_525)
	copy(swapData[40:], []byte{1, 1, 1, 1, 0, 0, 0, 6, 2})
	instructions := []solana.Instruction{
		ata(profile.Route.InputTokenAccount, profile.Route.InputMint),
		{Program: orcaswap.SystemProgram, Accounts: []solana.AccountMeta{
			{Address: profile.Route.Owner, Signer: true, Writable: true},
			{Address: profile.Route.InputTokenAccount, Writable: true},
		}, Data: transferData},
		{Program: orcaswap.TokenProgram, Accounts: []solana.AccountMeta{
			{Address: profile.Route.InputTokenAccount, Writable: true},
		}, Data: []byte{17}},
		{Program: orcaswap.WhirlpoolProgram, Accounts: []solana.AccountMeta{
			{Address: orcaswap.TokenProgram}, {Address: orcaswap.TokenProgram},
			{Address: orcaswap.MemoProgram}, {Address: profile.Route.Owner, Signer: true},
			{Address: profile.Route.Pool, Writable: true},
			{Address: profile.Route.InputMint}, {Address: profile.Route.OutputMint},
			{Address: profile.Route.InputTokenAccount, Writable: true},
			{Address: profile.Route.TokenVaultA, Writable: true},
			{Address: profile.Route.OutputTokenAccount, Writable: true},
			{Address: profile.Route.TokenVaultB, Writable: true},
			{Address: "7knZZ461yySGbSEHeBUwEpg3VtAkQy8B9tp78RGgyUHE", Writable: true},
			{Address: "CpoSFo3ajrizueggtJr2ZjvYgdtkgugXtvhqcwkyCkKP", Writable: true},
			{Address: "9iGzy4mQtJadZDuH8seBFQGiqcb6wyp2KW4M6NKHvsAW", Writable: true},
			{Address: profile.Route.Oracle, Writable: true},
			{Address: "3aBJJLAR3QxGcGsesNXeW3f64Rv3TckF7EQ6sXtAuvGM", Writable: true},
			{Address: "A1vrG379E5ttoaWmyQBiunsMdyrpoUp7mSQwu8DgLcip", Writable: true},
		}, Data: swapData},
		{Program: orcaswap.TokenProgram, Accounts: []solana.AccountMeta{
			{Address: profile.Route.InputTokenAccount, Writable: true},
			{Address: profile.Route.Owner, Writable: true},
			{Address: profile.Route.Owner, Signer: true},
		}, Data: []byte{9}},
	}
	return swapbuilder.Result{
		Instructions: instructions, TokenIn: profile.InputLamports,
		TokenEstOut: 21_742, TokenMinOut: 21_525,
		TradeEnableTimestamp: time.Unix(0, 0).UTC(),
	}
}

func buyQuote(t *testing.T, profile Profile) swapbuilder.Result {
	t.Helper()
	policy := profile.BuyRoute
	if policy == nil {
		t.Fatal("buy route is missing")
	}
	owner, err := solana.Decode32(policy.Owner)
	if err != nil {
		t.Fatal(err)
	}
	tokenProgram, err := solana.Decode32(orcaswap.TokenProgram)
	if err != nil {
		t.Fatal(err)
	}
	seed := []byte("1785688960889")
	hash := sha256.New()
	_, _ = hash.Write(owner[:])
	_, _ = hash.Write(seed)
	_, _ = hash.Write(tokenProgram[:])
	temporary := solana.Encode(hash.Sum(nil))
	create := binary.LittleEndian.AppendUint32(nil, 3)
	create = append(create, owner[:]...)
	create = binary.LittleEndian.AppendUint64(create, uint64(len(seed)))
	create = append(create, seed...)
	create = binary.LittleEndian.AppendUint64(create, 2_039_280)
	create = binary.LittleEndian.AppendUint64(create, 165)
	create = append(create, tokenProgram[:]...)
	initialize := append([]byte{18}, owner[:]...)
	swapData := make([]byte, 49)
	copy(swapData, []byte{43, 4, 237, 11, 26, 201, 30, 98})
	binary.LittleEndian.PutUint64(swapData[8:16], profile.InputTokenAmount)
	binary.LittleEndian.PutUint64(swapData[16:24], policy.MinOutputLamports)
	copy(swapData[40:], []byte{1, 0, 1, 1, 0, 0, 0, 6, 2})
	return swapbuilder.Result{
		TokenIn:              profile.InputTokenAmount,
		TokenEstOut:          policy.MinOutputLamports + 459,
		TokenMinOut:          policy.MinOutputLamports,
		TradeEnableTimestamp: time.Unix(0, 0).UTC(),
		Instructions: []solana.Instruction{
			{Program: orcaswap.SystemProgram, Accounts: []solana.AccountMeta{
				{Address: policy.Owner, Signer: true, Writable: true},
				{Address: temporary, Writable: true}, {Address: policy.Owner, Signer: true},
			}, Data: create},
			{Program: orcaswap.TokenProgram, Accounts: []solana.AccountMeta{
				{Address: temporary, Writable: true}, {Address: policy.TokenMintA},
			}, Data: initialize},
			{Program: orcaswap.WhirlpoolProgram, Accounts: []solana.AccountMeta{
				{Address: orcaswap.TokenProgram}, {Address: orcaswap.TokenProgram},
				{Address: orcaswap.MemoProgram}, {Address: policy.Owner, Signer: true},
				{Address: policy.Pool, Writable: true}, {Address: policy.TokenMintA},
				{Address: policy.TokenMintB}, {Address: temporary, Writable: true},
				{Address: policy.TokenVaultA, Writable: true},
				{Address: policy.InputTokenAccount, Writable: true},
				{Address: policy.TokenVaultB, Writable: true},
				{Address: "7knZZ461yySGbSEHeBUwEpg3VtAkQy8B9tp78RGgyUHE", Writable: true},
				{Address: "CpoSFo3ajrizueggtJr2ZjvYgdtkgugXtvhqcwkyCkKP", Writable: true},
				{Address: "9iGzy4mQtJadZDuH8seBFQGiqcb6wyp2KW4M6NKHvsAW", Writable: true},
				{Address: policy.Oracle, Writable: true},
				{Address: "3aBJJLAR3QxGcGsesNXeW3f64Rv3TckF7EQ6sXtAuvGM", Writable: true},
				{Address: "A1vrG379E5ttoaWmyQBiunsMdyrpoUp7mSQwu8DgLcip", Writable: true},
			}, Data: swapData},
			{Program: orcaswap.TokenProgram, Accounts: []solana.AccountMeta{
				{Address: temporary, Writable: true}, {Address: policy.Owner, Writable: true},
				{Address: policy.Owner, Signer: true},
			}, Data: []byte{9}},
		},
	}
}

func testPriceTriggerPolicy() pricetrigger.Policy {
	return pricetrigger.Policy{
		Version: pricetrigger.Version, Feed: pricetrigger.FeedSOLUSD,
		Direction: pricetrigger.SellAtOrAbove, ThresholdMicros: 150_000_000,
		MaxAgeSeconds: 15, MaxSourceSkewSeconds: 5,
		MaxDeviationBPS: 100, MaxConfidenceBPS: 100,
		PrimarySourceSHA256:   strings.Repeat("a", 64),
		SecondarySourceSHA256: strings.Repeat("b", 64),
	}
}

func testPriceEvidence(
	t *testing.T,
	policy pricetrigger.Policy,
	at time.Time,
	price uint64,
) pricetrigger.Evidence {
	t.Helper()
	evidence, err := pricetrigger.Evaluate(
		policy,
		pricetrigger.Sample{
			SourceSHA256: policy.PrimarySourceSHA256, Feed: policy.Feed,
			PriceMicros: price, ConfidenceMicros: 100_000, PublishedAt: at,
		},
		pricetrigger.Sample{
			SourceSHA256: policy.SecondarySourceSHA256, Feed: policy.Feed,
			PriceMicros: price, ConfidenceMicros: 100_000, PublishedAt: at,
		},
		at,
	)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func swapQuoteWithOutputSetup(profile Profile) swapbuilder.Result {
	quote := swapQuote(profile)
	setup := solana.Instruction{
		Program: orcaswap.AssociatedTokenProgram,
		Accounts: []solana.AccountMeta{
			{Address: profile.Route.Owner, Signer: true, Writable: true},
			{Address: profile.Route.OutputTokenAccount, Writable: true},
			{Address: profile.Route.Owner}, {Address: profile.Route.OutputMint},
			{Address: orcaswap.SystemProgram}, {Address: orcaswap.TokenProgram},
		},
		Data: []byte{1},
	}
	quote.Instructions = append(
		append([]solana.Instruction{}, quote.Instructions[0], setup),
		quote.Instructions[1:]...,
	)
	return quote
}

// An unavailable price source must not consume the action allowance: the
// operator's grant has to survive a provider outage, or a transient blip
// silently burns the one action they authorised.
func TestEnginePriceSourceOutageConsumesNoAction(t *testing.T) {
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

	before := len(store.Records())
	result, err := engine.RunOnce(t.Context(), profile)
	if err != nil {
		t.Fatalf("a price outage must not be a hard error: %v", err)
	}
	if result.Decision != "degraded" {
		t.Fatalf("decision = %q, want degraded", result.Decision)
	}
	// Recording clock evidence is correct; anything that advances the action is
	// not. Naming the allowed type means a future change that starts writing
	// action records here fails rather than passing quietly.
	for _, record := range store.Records()[before:] {
		if record.Type != clockEvent {
			t.Fatalf("price outage wrote %q, which advances the action", record.Type)
		}
	}
	for _, record := range store.Records() {
		if record.Type == EventStarted {
			t.Fatal("price outage created a swap action")
		}
	}
	// The node IS observed: the price read is bound to a proven slot, and the
	// observation is where that slot comes from. A read bound to slot 0 is
	// exactly what the default on-chain feed refuses. What a price outage must
	// not do is spend anything — asserted above by the journal staying clock-
	// only and no action starting.
	if observer.calls != 1 {
		t.Fatalf("price outage observed the node %d times, want exactly one", observer.calls)
	}
}
