package policyauthority

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func TestCancelStrategyDecisionBoundariesAndRefusals(t *testing.T) {
	for _, mode := range []string{"expiry boundary", "expired", "wrong decision", "wrong wallet", "torn claim", "unsafe claim", "reserved"} {
		t.Run(mode, func(t *testing.T) {
			path, seed, authority, recovery, next, acquisition, expiry := strategyRetirementFixture(t, 5*time.Second)
			committedAt := expiry.Add(-4 * time.Second)
			if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, committedAt); err != nil {
				t.Fatal(err)
			}
			status, err := ReadStrategyJournalStatus(path, authority, recovery, committedAt)
			if err != nil {
				t.Fatal(err)
			}
			wallet, err := ReadWalletInventory(seed.WalletPath, next)
			if err != nil {
				t.Fatal(err)
			}
			claimPath := seed.WalletPath + ".claim-" + wallet.HeadSHA256 + ".jsonl"
			if mode == "torn claim" {
				if err := os.WriteFile(claimPath, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unsafe claim" {
				if err := os.Symlink(seed.ClaimPath, claimPath); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "reserved" {
				evidence := &paperReserveEvidence{Evidence: strategyClaimEvidence{jupiterEvidence: &jupiterEvidence{primary: wallet.Opening.PrimaryIdentity, secondary: wallet.Opening.SecondaryIdentity}, slot: wallet.LastFinalizedSlot, guard: next.TransactionPolicy.Jupiter.RouteGuard}, mutate: func(a *txflow.AccountEvidence) {
					a.PrimaryLamports, a.SecondaryLamports = wallet.NativeLamports, wallet.NativeLamports
				}}
				window, anchor := int64(next.TransactionPolicy.ScheduleWindowSeconds), next.TransactionPolicy.ScheduleAnchorUnix
				if _, err := PrepareStrategyWalletClaim(t.Context(), seed.WalletPath, path, authority, recovery, next, anchor+(committedAt.Unix()-anchor)/window*window, committedAt, 5*time.Second, evidence, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.PrimaryIdentity}, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.SecondaryIdentity}, strategyClaimLifecycle(t, wallet)); err != nil {
					t.Fatal(err)
				}
			}
			before := strategyReconcileSnapshot(t, path, seed.WalletPath, seed.ClaimPath, acquisition, acquisition+".intent.jsonl")
			at := expiry.Add(time.Nanosecond)
			if mode == "expiry boundary" {
				at = expiry
			}
			digest, walletPath := status.PendingDecisionSHA256, seed.WalletPath
			if mode == "wrong decision" {
				digest = strings.Repeat("a", 64)
			}
			if mode == "wrong wallet" {
				walletPath += ".wrong"
			}
			got, err := CancelStrategyDecision(path, walletPath, authority, recovery, digest, at)
			if mode != "expired" {
				if err == nil {
					t.Fatal("unsafe or unexpired cancellation succeeded")
				}
				assertStrategyReconcileUnchanged(t, before)
				return
			}
			if err != nil || got.State.Pending() || got.PendingDecisionSHA256 != "" {
				t.Fatalf("expired cancellation: %v", err)
			}
			delete(before, path)
			assertStrategyReconcileUnchanged(t, before)
			latest := strategyReconcileSnapshot(t, path)
			retry, err := CancelStrategyDecision(path, seed.WalletPath, authority, recovery, digest, at.Add(time.Hour))
			if err != nil || !reflect.DeepEqual(got, retry) {
				t.Fatalf("exact cancellation retry: %v", err)
			}
			assertStrategyReconcileUnchanged(t, latest)
		})
	}
}

func TestCancelStrategyDecisionThenAcquire(t *testing.T) {
	path, seed, authority, recovery, next, acquisition, expiry := strategyRetirementFixture(t, 5*time.Second)
	committedAt := expiry.Add(-4 * time.Second)
	if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, committedAt); err != nil {
		t.Fatal(err)
	}
	original, err := ReadStrategyJournalStatus(path, authority, recovery, committedAt)
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := CancelStrategyDecision(path, seed.WalletPath, authority, recovery, original.PendingDecisionSHA256, expiry.Add(time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	preserved := strategyReconcileSnapshot(t, acquisition, acquisition+".intent.jsonl", seed.ClaimPath)
	base := time.Now().UTC().Add(-121 * time.Second)
	for i, price := range []uint64{3_000_000_000, 2_500_000_000, 2_000_000_000} {
		at := base.Add(time.Duration(i) * time.Minute)
		samples := strategyOutcomeSamples(seed, at)
		samples[0].PriceMicros, samples[1].PriceMicros = price, price
		if _, err := ObserveStrategyJournal(path, authority, recovery, at, samples[0], samples[1], samples[2], samples[3]); err != nil {
			t.Fatal(err)
		}
	}
	wallet, err := ReadWalletInventory(seed.WalletPath, next)
	if err != nil {
		t.Fatal(err)
	}
	records, err := journal.ReadRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := strategyAcquisitionPath(seed.WalletPath, wallet.HeadSHA256, records)
	if err != nil || generation != acquisition+".after-"+canceled.HeadSHA256 {
		t.Fatalf("cancellation generation: %v", err)
	}
	acquired, err := proposalcheck.ReadAcquisition(acquisition, committedAt, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	proposal := WalletAdmissionBuildForTest(t, continuationCandidate(t, acquired.Candidate, "after canceled decision"))
	calls := 0
	builder := strategyAcquisitionBuilder(func(_ context.Context, request jupiterquote.Request) (jupiterquote.BuildResult, error) {
		calls++
		if request != acquired.Candidate.Request {
			t.Fatal("cancellation changed ready amount")
		}
		proposal.Quote.ReceivedAt = time.Now().UTC()
		proposal.Quote.ResponseSHA256 = strings.Repeat("a", 64)
		return proposal, nil
	})
	evidence := &paperReserveEvidence{Evidence: strategyClaimEvidence{jupiterEvidence: &jupiterEvidence{primary: wallet.Opening.PrimaryIdentity, secondary: wallet.Opening.SecondaryIdentity}, slot: wallet.LastFinalizedSlot, guard: next.TransactionPolicy.Jupiter.RouteGuard}, mutate: func(a *txflow.AccountEvidence) {
		a.PrimaryLamports, a.SecondaryLamports = wallet.NativeLamports, wallet.NativeLamports
	}}
	at := time.Now().UTC()
	window, anchor := int64(next.TransactionPolicy.ScheduleWindowSeconds), next.TransactionPolicy.ScheduleAnchorUnix
	claim, err := AcquireStrategyWalletClaim(t.Context(), seed.WalletPath, path, authority, recovery, next, anchor+(at.Unix()-anchor)/window*window, at, time.Minute, time.Minute, builder, evidence, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.PrimaryIdentity}, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.SecondaryIdentity}, strategyClaimLifecycle(t, wallet))
	if err != nil || calls != 1 || claim.Inventory.PendingSHA256 == "" {
		t.Fatalf("new claim after cancellation: %v", err)
	}
	before := strategyReconcileSnapshot(t, path, seed.WalletPath, claim.ClaimPath, generation, generation+".intent.jsonl")
	retry, err := CancelStrategyDecision(path, seed.WalletPath, authority, recovery, original.PendingDecisionSHA256, time.Now().UTC())
	if err != nil || !retry.State.Pending() || retry.PendingDecisionSHA256 == original.PendingDecisionSHA256 {
		t.Fatalf("old retry lost later pending: %v", err)
	}
	if _, err := ReadStrategyJournalStatus(path, authority, recovery, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	assertStrategyReconcileUnchanged(t, before)
	assertStrategyReconcileUnchanged(t, preserved)
	// Reuse signed, offline finalized recovery with actual output differing
	// from the quote. This exercises cancellation in the continuation scan.
	secondRecovery := continuationRecovery(t, next, claim, wallet, false)
	delivered := time.Now().UTC().Add(time.Second)
	accounted, err := FinalizeWalletClaim(seed.WalletPath, claim.ClaimPath, next, claim.Request, secondRecovery, delivered)
	if err != nil {
		t.Fatal(err)
	}
	state, err := ApplyStrategyContinuationOutcome(path, authority, recovery, secondRecovery, retry.PendingDecisionSHA256, accounted.HeadSHA256, delivered.Add(time.Second))
	if err != nil || state.Pending() || state.Ledger().BaseUnits != accounted.NativeLamports || state.Ledger().QuoteUnits != accounted.TokenUnits {
		t.Fatalf("accounted continuation after cancellation: %v", err)
	}
	restarted, err := ReadStrategyJournalStatus(path, authority, recovery, delivered.Add(2*time.Second), secondRecovery)
	if err != nil || restarted.State.Pending() {
		t.Fatalf("restart after canceled generation and outcome: %v", err)
	}
	final := strategyReconcileSnapshot(t, path, seed.WalletPath, claim.ClaimPath)
	if _, err := CancelStrategyDecision(path, seed.WalletPath, authority, recovery, original.PendingDecisionSHA256, delivered.Add(time.Hour), secondRecovery); err != nil {
		t.Fatal(err)
	}
	assertStrategyReconcileUnchanged(t, final)
	assertStrategyReconcileUnchanged(t, preserved)
}

func TestCancelStrategyDecisionCompiledCLI(t *testing.T) {
	binary := os.Getenv("MITHRIL_AGENT_QA_CLI")
	if binary == "" {
		t.Skip("set MITHRIL_AGENT_QA_CLI to an independently built CLI")
	}
	if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary {
		t.Fatal("QA CLI requires clean absolute path")
	}
	path, seed, authority, recovery, next, acquisition, expiry := strategyRetirementFixture(t, 5*time.Second)
	if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, expiry.Add(-4*time.Second)); err != nil {
		t.Fatal(err)
	}
	status, err := ReadStrategyJournalStatus(path, authority, recovery, expiry)
	if err != nil {
		t.Fatal(err)
	}
	policyPath, recoveryPath := path+".policy.json", path+".recovery.json"
	for file, value := range map[string]any{policyPath: authority, recoveryPath: recovery} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := strategyReconcileSnapshot(t, seed.WalletPath, seed.ClaimPath, acquisition, acquisition+".intent.jsonl")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "proposal", "strategy", "--operation", "cancel-decision", "--strategy", path, "--inventory", seed.WalletPath, "--authority-policy", policyPath, "--submitter-policy", recoveryPath, "--decision-sha256", status.PendingDecisionSHA256)
	command.Env = append(os.Environ(), "MITHRIL_AGENT_MITHRIL_RPC_URL=", "MITHRIL_AGENT_PRIMARY_RPC_URL=", "MITHRIL_AGENT_SECONDARY_RPC_URL=", "MITHRIL_AGENT_JUPITER_API_KEY=invalid\nkey")
	raw, err := command.Output()
	if err != nil {
		t.Fatalf("compiled cancellation: %v", err)
	}
	var output map[string]any
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatal(err)
	}
	if output["status"] != "strategy_decision_canceled_not_authorized" || output["can_sign"] != false || output["can_submit"] != false {
		t.Fatal("compiled cancellation output changed authority")
	}
	assertStrategyReconcileUnchanged(t, before)
}

func TestCancelStrategyDecisionConcurrentPrepare(t *testing.T) {
	path, seed, authority, recovery, next, acquisition, expiry := strategyRetirementFixture(t, 5*time.Second)
	preparedAt := expiry.Add(-4 * time.Second)
	if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, preparedAt); err != nil {
		t.Fatal(err)
	}
	status, err := ReadStrategyJournalStatus(path, authority, recovery, preparedAt)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := ReadWalletInventory(seed.WalletPath, next)
	if err != nil {
		t.Fatal(err)
	}
	evidence := &paperReserveEvidence{Evidence: strategyClaimEvidence{jupiterEvidence: &jupiterEvidence{primary: wallet.Opening.PrimaryIdentity, secondary: wallet.Opening.SecondaryIdentity}, slot: wallet.LastFinalizedSlot, guard: next.TransactionPolicy.Jupiter.RouteGuard}, mutate: func(a *txflow.AccountEvidence) {
		a.PrimaryLamports, a.SecondaryLamports = wallet.NativeLamports, wallet.NativeLamports
	}}
	lifecycle := strategyClaimLifecycle(t, wallet)
	window, anchor := int64(next.TransactionPolicy.ScheduleWindowSeconds), next.TransactionPolicy.ScheduleAnchorUnix
	before := strategyReconcileSnapshot(t, acquisition, acquisition+".intent.jsonl", seed.ClaimPath)
	start := make(chan struct{})
	prepared, canceled := make(chan error, 1), make(chan error, 1)
	go func() {
		<-start
		_, err := PrepareStrategyWalletClaim(t.Context(), seed.WalletPath, path, authority, recovery, next, anchor+(preparedAt.Unix()-anchor)/window*window, preparedAt, 5*time.Second, evidence, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.PrimaryIdentity}, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.SecondaryIdentity}, lifecycle)
		prepared <- err
	}()
	go func() {
		<-start
		_, err := CancelStrategyDecision(path, seed.WalletPath, authority, recovery, status.PendingDecisionSHA256, expiry.Add(time.Nanosecond))
		canceled <- err
	}()
	close(start)
	prepareErr, cancelErr := <-prepared, <-canceled
	if prepareErr == nil && cancelErr == nil {
		t.Fatal("claim preparation and cancellation both succeeded")
	}
	result, err := ReadStrategyJournalStatus(path, authority, recovery, expiry.Add(time.Second))
	if err != nil {
		t.Fatalf("race produced unverifiable history: %v", err)
	}
	current, err := ReadWalletInventory(seed.WalletPath, next)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := optionalStrategyRecords(seed.WalletPath + ".claim-" + wallet.HeadSHA256 + ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if !result.State.Pending() && (current.PendingSHA256 != "" || len(claims) != 0) {
		t.Fatal("cancellation coexists with claim or reservation")
	}
	if prepareErr != nil && cancelErr != nil && len(claims) == 0 {
		if _, err := CancelStrategyDecision(path, seed.WalletPath, authority, recovery, status.PendingDecisionSHA256, expiry.Add(time.Second)); err != nil {
			t.Fatalf("explicit retry after lock contention: %v", err)
		}
	}
	assertStrategyReconcileUnchanged(t, before)
}

func TestCanceledStrategyDecisionCannotReuseFreshReceipt(t *testing.T) {
	path, seed, authority, recovery, next, acquisition, expiry := strategyRetirementFixture(t, 5*time.Second)
	committedAt := expiry.Add(-4 * time.Second)
	if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, committedAt); err != nil {
		t.Fatal(err)
	}
	status, err := ReadStrategyJournalStatus(path, authority, recovery, committedAt)
	if err != nil {
		t.Fatal(err)
	}
	at := expiry.Add(time.Second)
	canceled, err := CancelStrategyDecision(path, seed.WalletPath, authority, recovery, status.PendingDecisionSHA256, at)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(acquisition)
	if err != nil {
		t.Fatal(err)
	}
	copied := acquisition + ".copied"
	if err := os.WriteFile(copied, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := proposalcheck.ReadAcquisition(copied, at, time.Minute); err != nil {
		t.Fatalf("receipt must still be fresh independently of canceled decision: %v", err)
	}
	before := strategyReconcileSnapshot(t, path, seed.WalletPath, seed.ClaimPath, acquisition, acquisition+".intent.jsonl", copied)
	if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, copied, time.Minute, at); err == nil || !strings.Contains(err.Error(), "strategy acquisition was retired") {
		t.Fatalf("copied canceled receipt rejected at wrong boundary: %v", err)
	}
	retry, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, at)
	if err != nil || retry.Pending() || !reflect.DeepEqual(retry, canceled.State) {
		t.Fatalf("original decision retry recreated pending work: %v", err)
	}
	assertStrategyReconcileUnchanged(t, before)
}

func TestCancelStandaloneDecisionUsesOriginalExpiry(t *testing.T) {
	path, seed, authority, recovery, next, acquisition, at := strategyDecisionFixture(t)
	if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, at); err != nil {
		t.Fatal(err)
	}
	status, err := ReadStrategyJournalStatus(path, authority, recovery, at)
	if err != nil {
		t.Fatal(err)
	}
	// This legacy standalone receipt has no managed sidecar. Its immutable
	// quote TTL and original seed observation ceiling still bound cancellation.
	observation := at.Add(-time.Second)
	age := min(time.Minute, time.Duration(seed.Policy.Adaptive.MaxObservationGapSeconds)*time.Second)
	expiry := observation.Add(age)
	before := strategyReconcileSnapshot(t, path, seed.WalletPath, seed.ClaimPath, acquisition)
	if _, err := CancelStrategyDecision(path, seed.WalletPath, authority, recovery, status.PendingDecisionSHA256, expiry); err == nil || !strings.Contains(err.Error(), "original decision expiry") {
		t.Fatalf("standalone exact boundary: %v", err)
	}
	assertStrategyReconcileUnchanged(t, before)
	got, err := CancelStrategyDecision(path, seed.WalletPath, authority, recovery, status.PendingDecisionSHA256, expiry.Add(time.Nanosecond))
	if err != nil || got.State.Pending() {
		t.Fatalf("standalone original expiry: %v", err)
	}
	delete(before, path)
	assertStrategyReconcileUnchanged(t, before)
}
