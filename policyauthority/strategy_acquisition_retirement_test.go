package policyauthority

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func strategyRetirementFixture(t *testing.T, decisionAges ...time.Duration) (string, strategySeed, Policy, submitter.Policy, Policy, string, time.Time) {
	t.Helper()
	decisionAge := time.Minute
	if len(decisionAges) != 0 {
		decisionAge = decisionAges[0]
	}
	path, seed, authority, recovery, next, original, at := strategyDecisionFixture(t)
	wallet, err := ReadWalletInventory(seed.WalletPath, next)
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := proposalcheck.ReadAcquisition(original, at, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	records, err := journal.ReadRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	acquisition, err := strategyAcquisitionPath(seed.WalletPath, wallet.HeadSHA256, records)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(original, acquisition); err != nil {
		t.Fatal(err)
	}
	last := records[len(records)-1]
	policySHA, _, err := paperRequestHashes(next, signer.Request{})
	if err != nil {
		t.Fatal(err)
	}
	intent := strategyAcquisitionIntent{StrategyPath: path, StrategyHeadSHA256: last.Hash, WalletHeadSHA256: wallet.HeadSHA256,
		PolicySHA256: policySHA, Request: acquired.Candidate.Request, MaxDecisionAgeNS: int64(decisionAge), MaxAcquisitionAgeNS: int64(time.Minute)}
	store, err := journal.OpenStrict(acquisition + ".intent.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(acquired.ReceivedAt, strategyAcquisitionEvent, last.Hash, intent); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return path, seed, authority, recovery, next, acquisition, acquired.ReceivedAt.Add(decisionAge)
}

func TestRetiredStrategyAcquisitionDigestCannotBeCopied(t *testing.T) {
	path, seed, authority, recovery, next, acquisition, expiry := strategyRetirementFixture(t, 30*time.Second)
	at := expiry.Add(time.Nanosecond)
	if _, err := RetireStrategyAcquisition(path, seed.WalletPath, acquisition, authority, recovery, next, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	samples := strategyOutcomeSamples(seed, at)
	samples[0].PriceMicros, samples[1].PriceMicros = 2_000_000_000, 2_000_000_000
	ready, err := ObserveStrategyJournal(path, authority, recovery, at, samples[0], samples[1], samples[2], samples[3])
	if err != nil || !ready.ReadyForQuote {
		t.Fatalf("new observation was not ready: %v", err)
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
		t.Fatalf("copied receipt must still be otherwise fresh: %v", err)
	}
	before := strategyReconcileSnapshot(t, path, seed.WalletPath, seed.ClaimPath, acquisition, acquisition+".intent.jsonl", copied)
	if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, copied, time.Minute, at); err == nil || !strings.Contains(err.Error(), "strategy acquisition was retired") {
		t.Fatalf("fresh copied receipt did not fail at retirement boundary: %v", err)
	}
	assertStrategyReconcileUnchanged(t, before)
	if _, err := ReadStrategyJournalStatus(path, authority, recovery, at); err != nil {
		t.Fatalf("restart after retirement and newer observation: %v", err)
	}
}

func TestRetireStrategyAcquisitionConcurrentCommit(t *testing.T) {
	path, seed, authority, recovery, next, acquisition, expiry := strategyRetirementFixture(t, 30*time.Second)
	at := expiry.Add(time.Second)
	// The observation's original 30-second deadline has elapsed, while the
	// receipt remains independently valid under its original 60-second bound.
	// Race a commitment delivered before expiry against a later retirement;
	// otherwise both operations would no longer be independently eligible.
	commitAt := expiry.Add(-time.Second)
	if _, err := proposalcheck.ReadAcquisition(acquisition, at, time.Minute); err != nil {
		t.Fatalf("race receipt must remain fresh: %v", err)
	}
	before := strategyReconcileSnapshot(t, seed.WalletPath, seed.ClaimPath, acquisition, acquisition+".intent.jsonl")
	prior, err := journal.ReadRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	committed, retired := make(chan error, 1), make(chan error, 1)
	go func() {
		<-start
		_, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, commitAt)
		committed <- err
	}()
	go func() {
		<-start
		_, err := RetireStrategyAcquisition(path, seed.WalletPath, acquisition, authority, recovery, next, at)
		retired <- err
	}()
	close(start)
	commitErr, retireErr := <-committed, <-retired
	if commitErr == nil && retireErr == nil {
		t.Fatal("concurrent commit and retirement both succeeded")
	}
	status, err := ReadStrategyJournalStatus(path, authority, recovery, at)
	if err != nil || status.State == nil {
		t.Fatalf("concurrent result cannot be replayed: %v", err)
	}
	if commitErr != nil && retireErr != nil && !status.State.Pending() {
		// Nonblocking evidence locks may reject both competitors. One explicit
		// retry must resolve the still-idle opportunity (or recover its retirement).
		if _, err := RetireStrategyAcquisition(path, seed.WalletPath, acquisition, authority, recovery, next, at); err != nil {
			t.Fatalf("explicit retirement retry after contention: %v", err)
		}
	}
	records, err := journal.ReadRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != len(prior)+1 {
		t.Fatalf("race appended %d records, want exactly one terminal choice", len(records)-len(prior))
	}
	last := records[len(records)-1]
	if last.Type != strategyDecisionEvent && last.Type != strategyAcquisitionRetirementEvent {
		t.Fatalf("unexpected race event: %s", last.Type)
	}
	status, err = ReadStrategyJournalStatus(path, authority, recovery, at)
	if err != nil || status.State == nil || status.State.Pending() != (last.Type == strategyDecisionEvent) {
		t.Fatalf("replayed state disagrees with exclusive race winner: %v", err)
	}
	assertStrategyReconcileUnchanged(t, before)
	wallet, err := ReadWalletInventory(seed.WalletPath, next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(seed.WalletPath + ".claim-" + wallet.HeadSHA256 + ".jsonl"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("commit/retirement race created a claim: %v", err)
	}
}

func TestRetireStrategyAcquisitionSuccessiveGenerations(t *testing.T) {
	path, seed, authority, recovery, next, first, expiry := strategyRetirementFixture(t)
	firstStatus, err := RetireStrategyAcquisition(path, seed.WalletPath, first, authority, recovery, next, expiry.Add(time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := ReadWalletInventory(seed.WalletPath, next)
	if err != nil {
		t.Fatal(err)
	}
	observed := expiry.Add(time.Second)
	samples := strategyOutcomeSamples(seed, observed)
	samples[0].PriceMicros, samples[1].PriceMicros = 2_000_000_000, 2_000_000_000
	ready, err := ObserveStrategyJournal(path, authority, recovery, observed, samples[0], samples[1], samples[2], samples[3])
	if err != nil || !ready.ReadyForQuote {
		t.Fatalf("second generation needs a new ready observation: %v", err)
	}
	records, err := journal.ReadRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := strategyAcquisitionPath(seed.WalletPath, wallet.HeadSHA256, records)
	if err != nil || second != first+".after-"+firstStatus.HeadSHA256 {
		t.Fatalf("second generation identity: %v", err)
	}
	acquired, err := proposalcheck.ReadAcquisition(first, expiry.Add(-time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	policySHA, _, err := paperRequestHashes(next, signer.Request{})
	if err != nil {
		t.Fatal(err)
	}
	last := records[len(records)-1]
	intent := strategyAcquisitionIntent{StrategyPath: path, StrategyHeadSHA256: last.Hash, WalletHeadSHA256: wallet.HeadSHA256,
		PolicySHA256: policySHA, Request: acquired.Candidate.Request, MaxDecisionAgeNS: int64(time.Minute), MaxAcquisitionAgeNS: int64(time.Minute)}
	store, err := journal.OpenStrict(second + ".intent.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	_, appendErr := store.Append(observed, strategyAcquisitionEvent, last.Hash, intent)
	if err := errors.Join(appendErr, store.Close()); err != nil {
		t.Fatal(err)
	}
	// The second attempt crashed before any builder receipt. Retirement must
	// replay the first generation without recursively holding its intent lock.
	before := strategyReconcileSnapshot(t, seed.WalletPath, seed.ClaimPath, first, first+".intent.jsonl", second+".intent.jsonl")
	at := observed.Add(time.Minute + time.Nanosecond)
	secondStatus, err := RetireStrategyAcquisition(path, seed.WalletPath, second, authority, recovery, next, at)
	if err != nil {
		t.Fatalf("second retirement failed through prior-generation replay: %v", err)
	}
	restarted, err := ReadStrategyJournalStatus(path, authority, recovery, at)
	if err != nil || !reflect.DeepEqual(restarted, secondStatus) {
		t.Fatalf("restart after two retirements: %v", err)
	}
	records, err = journal.ReadRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	third, err := strategyAcquisitionPath(seed.WalletPath, wallet.HeadSHA256, records)
	if err != nil || third == first || third == second || third != first+".after-"+secondStatus.HeadSHA256 {
		t.Fatalf("third generation is not distinct and deterministically bound: %v", err)
	}
	latest := strategyReconcileSnapshot(t, path)
	for _, retired := range []string{first, second} {
		retry, err := RetireStrategyAcquisition(path, seed.WalletPath, retired, authority, recovery, next, at.Add(time.Hour))
		if err != nil || !reflect.DeepEqual(retry, secondStatus) {
			t.Fatalf("old generation retry lost latest status: %v", err)
		}
	}
	assertStrategyReconcileUnchanged(t, before)
	assertStrategyReconcileUnchanged(t, latest)
	for _, missing := range []string{second, third, third + ".intent.jsonl"} {
		if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retirement created unobserved acquisition evidence: %v", err)
		}
	}
}

func TestRetireStrategyAcquisitionOriginalExpiryAndRetry(t *testing.T) {
	for _, intentOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "canonical receipt", true: "intent only crash"}[intentOnly], func(t *testing.T) {
			path, seed, authority, recovery, next, acquisition, expiry := strategyRetirementFixture(t)
			preserved := []string{seed.WalletPath, seed.ClaimPath, acquisition + ".intent.jsonl"}
			if intentOnly {
				if err := os.Remove(acquisition); err != nil {
					t.Fatal(err)
				}
			} else {
				preserved = append(preserved, acquisition)
			}
			before := strategyReconcileSnapshot(t, append(preserved, path)...)
			if _, err := RetireStrategyAcquisition(path, seed.WalletPath, acquisition, authority, recovery, next, expiry); err == nil {
				t.Fatal("retired at still-valid exact expiry boundary")
			}
			assertStrategyReconcileUnchanged(t, before)
			retiredAt := expiry.Add(time.Nanosecond)
			status, err := RetireStrategyAcquisition(path, seed.WalletPath, acquisition, authority, recovery, next, retiredAt)
			if err != nil || status.State == nil || status.State.Pending() || status.HeadSHA256 == "" {
				t.Fatalf("expired retirement: %v", err)
			}
			for _, p := range preserved {
				assertStrategyReconcileUnchanged(t, map[string][]byte{p: before[p]})
			}
			if intentOnly {
				if _, err := os.Lstat(acquisition); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("intent-only retirement created receipt: %v", err)
				}
			}
			restored, err := ReadStrategyJournalStatus(path, authority, recovery, retiredAt)
			if err != nil || !reflect.DeepEqual(status, restored) {
				t.Fatalf("retirement restart differs: %v", err)
			}
			all := strategyReconcileSnapshot(t, append(preserved, path)...)
			retried, err := RetireStrategyAcquisition(path, seed.WalletPath, acquisition, authority, recovery, next, retiredAt.Add(time.Hour))
			if err != nil || !reflect.DeepEqual(status, retried) {
				t.Fatalf("retirement exact retry: %v", err)
			}
			assertStrategyReconcileUnchanged(t, all)
			records, err := journal.ReadRecords(path)
			if err != nil {
				t.Fatal(err)
			}
			wallet, err := ReadWalletInventory(seed.WalletPath, next)
			if err != nil {
				t.Fatal(err)
			}
			generation, err := strategyAcquisitionPath(seed.WalletPath, wallet.HeadSHA256, records)
			if err != nil || generation != acquisition+".after-"+status.HeadSHA256 {
				t.Fatalf("retired generation did not advance deterministically: %q %v", generation, err)
			}
			if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, expiry.Add(time.Second)); err == nil || !strings.Contains(err.Error(), "strategy acquisition was retired") {
				t.Fatalf("direct commit did not reject the retired path before checking expiry: %v", err)
			}
			assertStrategyReconcileUnchanged(t, all)
		})
	}
}

func TestRetireStrategyAcquisitionRefusesUncertainOrCommittedState(t *testing.T) {
	for _, mode := range []string{"torn receipt", "missing intent", "torn intent", "wrong wallet", "changed policy", "nonempty claim", "committed decision", "reserved claim"} {
		t.Run(mode, func(t *testing.T) {
			path, seed, authority, recovery, next, acquisition, expiry := strategyRetirementFixture(t)
			walletPath := seed.WalletPath
			files := []string{path, seed.WalletPath, seed.ClaimPath, acquisition, acquisition + ".intent.jsonl"}
			switch mode {
			case "torn receipt":
				if err := os.WriteFile(acquisition, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing intent":
				if err := os.Remove(acquisition + ".intent.jsonl"); err != nil {
					t.Fatal(err)
				}
				files = files[:len(files)-1]
			case "torn intent":
				if err := os.WriteFile(acquisition+".intent.jsonl", []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong wallet":
				walletPath += ".wrong"
			case "changed policy":
				next.GrantLifetimeSecs++
			case "nonempty claim":
				wallet, err := ReadWalletInventory(seed.WalletPath, next)
				if err != nil {
					t.Fatal(err)
				}
				claim := seed.WalletPath + ".claim-" + wallet.HeadSHA256 + ".jsonl"
				if err := os.WriteFile(claim, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
				files = append(files, claim)
			case "committed decision", "reserved claim":
				at := expiry.Add(-time.Minute + time.Second)
				if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, at); err != nil {
					t.Fatal(err)
				}
				if mode == "reserved claim" {
					wallet, err := ReadWalletInventory(seed.WalletPath, next)
					if err != nil {
						t.Fatal(err)
					}
					evidence := &paperReserveEvidence{Evidence: strategyClaimEvidence{jupiterEvidence: &jupiterEvidence{primary: wallet.Opening.PrimaryIdentity, secondary: wallet.Opening.SecondaryIdentity}, slot: wallet.LastFinalizedSlot, guard: next.TransactionPolicy.Jupiter.RouteGuard}, mutate: func(a *txflow.AccountEvidence) {
						a.PrimaryLamports, a.SecondaryLamports = wallet.NativeLamports, wallet.NativeLamports
					}}
					window, anchor := int64(next.TransactionPolicy.ScheduleWindowSeconds), next.TransactionPolicy.ScheduleAnchorUnix
					claim, err := PrepareStrategyWalletClaim(t.Context(), seed.WalletPath, path, authority, recovery, next, anchor+(at.Unix()-anchor)/window*window, at, time.Minute, evidence, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.PrimaryIdentity}, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.SecondaryIdentity}, strategyClaimLifecycle(t, wallet))
					if err != nil {
						t.Fatal(err)
					}
					files = append(files, claim.ClaimPath)
				}
			}
			before := strategyReconcileSnapshot(t, files...)
			if _, err := RetireStrategyAcquisition(path, walletPath, acquisition, authority, recovery, next, expiry.Add(time.Second)); err == nil {
				t.Fatal("unsafe retirement accepted")
			}
			assertStrategyReconcileUnchanged(t, before)
		})
	}
}

func TestRetiredStrategyAcquisitionAllowsNextGenerationBuilder(t *testing.T) {
	path, seed, authority, recovery, next, acquisition, expiry := strategyRetirementFixture(t)
	if _, err := RetireStrategyAcquisition(path, seed.WalletPath, acquisition, authority, recovery, next, expiry.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-121 * time.Second)
	for i, price := range []uint64{3_000_000_000, 2_500_000_000, 2_000_000_000} {
		at := base.Add(time.Duration(i) * time.Minute)
		samples := strategyOutcomeSamples(seed, at)
		samples[0].PriceMicros, samples[1].PriceMicros = price, price
		ready, err := ObserveStrategyJournal(path, authority, recovery, at, samples[0], samples[1], samples[2], samples[3])
		if err != nil {
			t.Fatal(err)
		}
		if i == 2 && !ready.ReadyForQuote {
			t.Fatal("next generation lacks actual ready observation")
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
	if err != nil {
		t.Fatal(err)
	}
	old := strategyReconcileSnapshot(t, acquisition, acquisition+".intent.jsonl", seed.WalletPath, seed.ClaimPath)
	calls := 0
	builder := strategyAcquisitionBuilder(func(context.Context, jupiterquote.Request) (jupiterquote.BuildResult, error) {
		calls++
		return jupiterquote.BuildResult{}, errors.New("offline builder stopped after next-generation admission")
	})
	evidence := &paperReserveEvidence{Evidence: strategyClaimEvidence{jupiterEvidence: &jupiterEvidence{primary: wallet.Opening.PrimaryIdentity, secondary: wallet.Opening.SecondaryIdentity}, slot: wallet.LastFinalizedSlot, guard: next.TransactionPolicy.Jupiter.RouteGuard}, mutate: func(value *txflow.AccountEvidence) {
		value.PrimaryLamports, value.SecondaryLamports = wallet.NativeLamports, wallet.NativeLamports
	}}
	at := time.Now().UTC()
	window, anchor := int64(next.TransactionPolicy.ScheduleWindowSeconds), next.TransactionPolicy.ScheduleAnchorUnix
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = AcquireStrategyWalletClaim(ctx, seed.WalletPath, path, authority, recovery, next, anchor+(at.Unix()-anchor)/window*window, at, time.Minute, time.Minute, builder, evidence, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.PrimaryIdentity}, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.SecondaryIdentity}, strategyClaimLifecycle(t, wallet))
	if err == nil || calls != 1 {
		t.Fatalf("next generation did not reach exactly one builder: calls=%d, %v", calls, err)
	}
	assertStrategyReconcileUnchanged(t, old)
	intent, err := journal.ReadRecords(generation + ".intent.jsonl")
	if err != nil || len(intent) != 1 {
		t.Fatalf("new-generation intent: %d %v", len(intent), err)
	}
	// Retry the retained new-generation intent with an actual canonical offline
	// build. Success traverses Commit and Prepare, including their replay locks.
	acquired, err := proposalcheck.ReadAcquisition(acquisition, expiry.Add(-time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	proposal := WalletAdmissionBuildForTest(t, continuationCandidate(t, acquired.Candidate, "retired acquisition next generation"))
	successCalls := 0
	builder = strategyAcquisitionBuilder(func(_ context.Context, request jupiterquote.Request) (jupiterquote.BuildResult, error) {
		successCalls++
		if request != acquired.Candidate.Request {
			t.Fatal("new generation changed the actual ready direction or amount")
		}
		proposal.Quote.ReceivedAt = time.Now().UTC()
		proposal.Quote.ResponseSHA256 = strings.Repeat("a", 64)
		return proposal, nil
	})
	ctx, cancelSuccess := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancelSuccess()
	claim, err := AcquireStrategyWalletClaim(ctx, seed.WalletPath, path, authority, recovery, next, anchor+(at.Unix()-anchor)/window*window, time.Now().UTC(), time.Minute, time.Minute, builder, evidence, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.PrimaryIdentity}, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.SecondaryIdentity}, strategyClaimLifecycle(t, wallet))
	if err != nil || successCalls != 1 || claim.Recovered || claim.Inventory.PendingSHA256 == "" {
		t.Fatalf("next generation did not produce its unsigned reservation: calls=%d, %v", successCalls, err)
	}
	if claim.ClaimPath != seed.WalletPath+".claim-"+wallet.HeadSHA256+".jsonl" || claim.Inventory.Pending.PreviousHeadSHA256 != wallet.HeadSHA256 {
		t.Fatal("new generation changed the original wallet-head claim binding")
	}
	delete(old, seed.WalletPath) // The one new reservation intentionally advances the wallet.
	assertStrategyReconcileUnchanged(t, old)
	completed := strategyReconcileSnapshot(t, path, seed.WalletPath, claim.ClaimPath, generation, generation+".intent.jsonl")
	retirementRetry, err := RetireStrategyAcquisition(path, seed.WalletPath, acquisition, authority, recovery, next, time.Now().UTC())
	if err != nil || retirementRetry.State == nil || !retirementRetry.State.Pending() {
		t.Fatalf("old retirement retry lost later pending claim: %v", err)
	}
	assertStrategyReconcileUnchanged(t, completed)
	assertStrategyReconcileUnchanged(t, old)
	retry, err := AcquireStrategyWalletClaim(t.Context(), seed.WalletPath, path, Policy{}, recovery, next, 0, time.Now().Add(24*time.Hour), 0, 0, nil, nil, nil, nil, nil)
	if err != nil || !retry.Recovered || !reflect.DeepEqual(retry.Request, claim.Request) || !reflect.DeepEqual(retry.Inventory, claim.Inventory) {
		t.Fatalf("new-generation claim did not recover without fresh evidence: %v", err)
	}
	assertStrategyReconcileUnchanged(t, completed)
	for _, file := range []string{path, seed.WalletPath, generation + ".intent.jsonl"} {
		store, err := journal.OpenStrict(file)
		if err != nil {
			t.Fatalf("next-generation lock leaked for %s: %v", filepath.Base(file), err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRetireStrategyAcquisitionCompiledCLI(t *testing.T) {
	binary := os.Getenv("MITHRIL_AGENT_QA_CLI")
	if binary == "" {
		t.Skip("set MITHRIL_AGENT_QA_CLI to an independently built CLI")
	}
	if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary {
		t.Fatal("QA CLI must be a clean absolute path")
	}
	path, seed, authority, recovery, next, acquisition, expiry := strategyRetirementFixture(t)
	if !time.Now().After(expiry) {
		t.Fatal("compiled CLI fixture has not expired under real host time")
	}
	root := filepath.Dir(path)
	authorityPath, recoveryPath, nextPath := filepath.Join(root, "retire-authority.json"), filepath.Join(root, "retire-recovery.json"), filepath.Join(root, "retire-next.json")
	files := []string{seed.WalletPath, seed.ClaimPath, acquisition, acquisition + ".intent.jsonl"}
	for file, value := range map[string]any{authorityPath: authority, recoveryPath: recovery, nextPath: next} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	before := strategyReconcileSnapshot(t, files...)
	args := []string{"proposal", "strategy", "--operation", "retire-acquisition", "--strategy", path, "--inventory", seed.WalletPath, "--acquisition", acquisition, "--authority-policy", authorityPath, "--submitter-policy", recoveryPath, "--next-authority-policy", nextPath}
	invoke := func() []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = append(os.Environ(), "MITHRIL_AGENT_SHADOW_RPC_URL=http://untrusted.invalid/private-test-endpoint", "MITHRIL_AGENT_MITHRIL_RPC_URL=", "MITHRIL_AGENT_PRIMARY_RPC_URL=", "MITHRIL_AGENT_SECONDARY_RPC_URL=", "MITHRIL_AGENT_JUPITER_API_KEY=invalid\nkey")
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := command.Run(); err != nil {
			t.Fatalf("compiled retirement failed: %v (%s)", err, stderr.String())
		}
		return stdout.Bytes()
	}
	output := invoke()
	var result map[string]any
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 4 || result["status"] != "strategy_acquisition_retired_not_authorized" || result["can_sign"] != false || result["can_submit"] != false {
		t.Fatal("compiled retirement output claims authority or leaks extra fields")
	}
	status, err := ReadStrategyJournalStatus(path, authority, recovery, time.Now())
	if err != nil || result["current_head_sha256"] != status.HeadSHA256 {
		t.Fatalf("compiled retirement did not preserve verified head: %v", err)
	}
	assertStrategyReconcileUnchanged(t, before)
	retired := strategyReconcileSnapshot(t, path)
	if retry := invoke(); !bytes.Equal(output, retry) {
		t.Fatal("compiled exact retirement retry changed output")
	}
	assertStrategyReconcileUnchanged(t, before)
	assertStrategyReconcileUnchanged(t, retired)
}
