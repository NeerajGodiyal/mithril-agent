package policyauthority

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func strategyReconcileSnapshot(t *testing.T, paths ...string) map[string][]byte {
	t.Helper()
	result := make(map[string][]byte)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		result[path] = data
	}
	return result
}

func assertStrategyReconcileUnchanged(t *testing.T, before map[string][]byte) {
	t.Helper()
	for path, want := range before {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(want, got) {
			t.Fatalf("reconciliation changed preserved journal %s: %v", filepath.Base(path), err)
		}
	}
}

func TestReconcileStrategyJournalFirstOutcome(t *testing.T) {
	for _, stage := range []string{"pending", "terminal recorded", "accounted before delivery", "no finalized evidence"} {
		t.Run(stage, func(t *testing.T) {
			walletSeed, attestorSeed := sha256.Sum256([]byte("reconcile wallet")), sha256.Sum256([]byte("reconcile attestor"))
			walletKey, attestorKey := ed25519.NewKeyFromSeed(walletSeed[:]), ed25519.NewKeyFromSeed(attestorSeed[:])
			path, seed, authority, request, at := strategyJournalFixtureWithIdentity(t, solana.Encode(walletKey.Public().(ed25519.PublicKey)), solana.Encode(attestorKey.Public().(ed25519.PublicKey)))
			recovery, recoveryPath := strategyOutcomeRecoveryFixture(t, authority, request, walletKey, attestorKey, false)
			if _, err := InitializeStrategyJournal(path, seed.WalletPath, seed.ClaimPath, authority, recovery, seed.Policy, seed.Ticks, seed.Bounds, at); err != nil {
				t.Fatal(err)
			}
			if stage == "terminal recorded" {
				if _, err := RecordPaperTerminal(seed.ClaimPath, authority, request, recovery, at.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "accounted before delivery" {
				if _, err := FinalizeWalletClaim(seed.WalletPath, seed.ClaimPath, authority, request, recovery, at.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "no finalized evidence" {
				if err := os.Rename(recoveryPath, recoveryPath+".unavailable"); err != nil {
					t.Fatal(err)
				}
			}
			before := strategyReconcileSnapshot(t, path, seed.WalletPath, seed.ClaimPath)
			if _, err := ReconcileStrategyJournal(path, seed.WalletPath+".wrong", authority, recovery, submitter.Policy{}, at.Add(2*time.Second)); err == nil {
				t.Fatal("wrong inventory path accepted")
			}
			assertStrategyReconcileUnchanged(t, before)
			result, err := ReconcileStrategyJournal(path, seed.WalletPath, authority, recovery, submitter.Policy{}, at.Add(2*time.Second))
			if stage == "no finalized evidence" {
				if err == nil {
					t.Fatal("missing finalized evidence accepted")
				}
				assertStrategyReconcileUnchanged(t, before)
				return
			}
			if err != nil || result.BlockedReason != "" || result.ClaimPath != seed.ClaimPath || result.AccountingSHA256 == "" || result.Strategy.State == nil || result.Strategy.State.Pending() {
				t.Fatalf("first reconciliation: %+v, %v", result, err)
			}
			wallet, err := ReadWalletInventory(seed.WalletPath, authority)
			if err != nil || wallet.PendingSHA256 != "" || wallet.HeadSHA256 != result.AccountingSHA256 || result.Strategy.State.Ledger().BaseUnits != wallet.NativeLamports || result.Strategy.State.Ledger().QuoteUnits != wallet.TokenUnits {
				t.Fatalf("first accounted balances: %v", err)
			}
			if stage == "accounted before delivery" {
				assertStrategyReconcileUnchanged(t, map[string][]byte{seed.WalletPath: before[seed.WalletPath], seed.ClaimPath: before[seed.ClaimPath]})
			}
			before = strategyReconcileSnapshot(t, path, seed.WalletPath, seed.ClaimPath)
			retried, err := ReconcileStrategyJournal(path, seed.WalletPath, authority, recovery, submitter.Policy{}, at.Add(time.Hour))
			if err != nil || retried.BlockedReason != "" || !reflect.DeepEqual(result.Strategy, retried.Strategy) {
				t.Fatalf("completed retry: %v", err)
			}
			assertStrategyReconcileUnchanged(t, before)
		})
	}
}

func TestReconcileStrategyJournalIdleDoesNotIgnoreRetainedClaim(t *testing.T) {
	path, seed, authority, recovery, _, _, at := strategyDecisionFixture(t)
	status, err := ReadStrategyJournalStatus(path, authority, recovery, at)
	if err != nil || status.State == nil || status.State.Pending() {
		t.Fatalf("fixture is not an idle completed strategy: %v", err)
	}
	wallet, err := ReadWalletInventory(seed.WalletPath, authority)
	if err != nil || wallet.PendingSHA256 != "" {
		t.Fatalf("fixture wallet is not available: %v", err)
	}
	claimPath := seed.WalletPath + ".claim-" + wallet.HeadSHA256 + ".jsonl"
	// A crash left nonempty, unverified bytes at the exact current-head claim
	// path. Idle strategy state must not erase this uncertainty or allow a new
	// acquisition. This is deliberately malformed evidence, not a fake claim.
	if err := os.WriteFile(claimPath, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	before := strategyReconcileSnapshot(t, path, seed.WalletPath, seed.ClaimPath, claimPath)
	result, err := ReconcileStrategyJournal(path, seed.WalletPath, authority, recovery, submitter.Policy{}, at.Add(time.Second))
	if err == nil && result.BlockedReason == "" {
		t.Fatal("idle strategy ignored nonempty unresolved current-head claim")
	}
	assertStrategyReconcileUnchanged(t, before)
}

func TestReconcileStrategyJournalIdleReportsPendingAcquisition(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipt without intent", true: "valid intent and torn receipt"}[malformed], func(t *testing.T) {
			path, seed, authority, recovery, next, original, at := strategyDecisionFixture(t)
			wallet, err := ReadWalletInventory(seed.WalletPath, authority)
			if err != nil {
				t.Fatal(err)
			}
			acquired, err := proposalcheck.ReadAcquisition(original, at, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			acquisition := seed.WalletPath + ".claim-" + wallet.HeadSHA256 + ".jsonl.acquisition.jsonl"
			if err := os.Rename(original, acquisition); err != nil {
				t.Fatal(err)
			}
			paths := []string{path, seed.WalletPath, seed.ClaimPath, acquisition}
			if malformed {
				records, err := journal.ReadRecords(path)
				if err != nil {
					t.Fatal(err)
				}
				last := records[len(records)-1]
				policySHA, _, err := paperRequestHashes(next, signer.Request{})
				if err != nil {
					t.Fatal(err)
				}
				intent := strategyAcquisitionIntent{StrategyPath: path, StrategyHeadSHA256: last.Hash, WalletHeadSHA256: wallet.HeadSHA256,
					PolicySHA256: policySHA, Request: acquired.Candidate.Request, MaxDecisionAgeNS: int64(time.Minute), MaxAcquisitionAgeNS: int64(time.Minute)}
				intentPath := acquisition + ".intent.jsonl"
				store, err := journal.OpenStrict(intentPath)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Append(acquired.ReceivedAt, strategyAcquisitionEvent, last.Hash, intent); err != nil {
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(acquisition, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, intentPath)
			}
			before := strategyReconcileSnapshot(t, paths...)
			result, err := ReconcileStrategyJournal(path, seed.WalletPath, authority, recovery, submitter.Policy{}, at)
			if malformed {
				if err == nil {
					t.Fatal("valid intent hid malformed acquisition receipt")
				}
			} else if err != nil || result.BlockedReason != "acquisition_pending" {
				t.Fatalf("idle strategy ignored retained acquisition: blocked=%q, %v", result.BlockedReason, err)
			}
			assertStrategyReconcileUnchanged(t, before)
		})
	}
}

func TestReconcileStrategyJournalContinuation(t *testing.T) {
	for _, stage := range []string{"pending", "accounted before delivery", "wrong leg recovery", "unreserved claim",
		"historical policy", "historical duplicate", "historical accounted", "historical missing",
		"historical ambiguous", "historical no finality", "historical successful", "explicit wrong with history"} {
		t.Run(stage, func(t *testing.T) {
			path, seed, authority, originalRecovery, next, originalAcquisition, at := strategyDecisionFixture(t)
			acquired, err := proposalcheck.ReadAcquisition(originalAcquisition, at, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			candidate := continuationCandidate(t, acquired.Candidate, "reconciled second transaction")
			acquisition := filepath.Join(filepath.Dir(path), "reconcile-acquisition.jsonl")
			seedStrategyAcquisition(t, acquisition, candidate, at.Add(-time.Second), time.Minute)
			if _, err := CommitStrategyJournalDecision(path, authority, originalRecovery, next, acquisition, time.Minute, at); err != nil {
				t.Fatal(err)
			}
			wallet, err := ReadWalletInventory(seed.WalletPath, next)
			if err != nil {
				t.Fatal(err)
			}
			before := strategyReconcileSnapshot(t, path, seed.WalletPath, seed.ClaimPath)
			blocked, err := ReconcileStrategyJournal(path, seed.WalletPath, authority, originalRecovery, originalRecovery, at)
			if err != nil || blocked.BlockedReason != "claim_not_prepared" || blocked.Strategy.State == nil || !blocked.Strategy.State.Pending() {
				t.Fatalf("unprepared decision: %+v, %v", blocked, err)
			}
			assertStrategyReconcileUnchanged(t, before)
			evidence := &paperReserveEvidence{Evidence: strategyClaimEvidence{jupiterEvidence: &jupiterEvidence{primary: wallet.Opening.PrimaryIdentity, secondary: wallet.Opening.SecondaryIdentity}, slot: wallet.LastFinalizedSlot, guard: next.TransactionPolicy.Jupiter.RouteGuard}, mutate: func(account *txflow.AccountEvidence) {
				account.PrimaryLamports, account.SecondaryLamports = wallet.NativeLamports, wallet.NativeLamports
			}}
			window, anchor := int64(next.TransactionPolicy.ScheduleWindowSeconds), next.TransactionPolicy.ScheduleAnchorUnix
			start := anchor + (at.Unix()-anchor)/window*window
			observedWallet := wallet
			if stage == "unreserved claim" {
				observedWallet.NativeLamports--
			}
			claim, err := PrepareStrategyWalletClaim(t.Context(), seed.WalletPath, path, authority, originalRecovery, next, start, at, time.Minute, evidence, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.PrimaryIdentity}, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.SecondaryIdentity}, strategyClaimLifecycle(t, observedWallet))
			if stage == "unreserved claim" {
				if err == nil {
					t.Fatal("changed wallet unexpectedly reserved")
				}
				claimPath := seed.WalletPath + ".claim-" + wallet.HeadSHA256 + ".jsonl"
				if _, err := ReadClaimedPaperRequest(claimPath, next); err != nil {
					t.Fatal(err)
				}
				before := strategyReconcileSnapshot(t, path, seed.WalletPath, seed.ClaimPath, claimPath)
				blocked, err := ReconcileStrategyJournal(path, seed.WalletPath, authority, originalRecovery, originalRecovery, at.Add(time.Hour))
				if err != nil || blocked.BlockedReason != "claim_not_reserved" || blocked.ClaimPath != claimPath || blocked.Strategy.State == nil || !blocked.Strategy.State.Pending() {
					t.Fatalf("unreserved claim was repaired or lost: %+v, %v", blocked, err)
				}
				assertStrategyReconcileUnchanged(t, before)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			legRecovery := continuationRecovery(t, next, claim, wallet, stage != "historical successful")
			if stage == "pending" {
				for name, mutate := range map[string]func(*submitter.Policy){
					"fee cap":   func(p *submitter.Policy) { p.MaxFeeLamports++ },
					"schedule":  func(p *submitter.Policy) { p.ScheduleAnchorUnix++ },
					"submitter": func(p *submitter.Policy) { p.SubmitterPublicKey = p.AttestationPublicKey },
					"providers": func(p *submitter.Policy) {
						p.Evidence.PrimaryTrustDomain, p.Evidence.SecondaryTrustDomain = p.Evidence.SecondaryTrustDomain, p.Evidence.PrimaryTrustDomain
					},
				} {
					changed := legRecovery
					mutate(&changed)
					if _, err := pendingStrategyRecoveryPolicy(next, []submitter.Policy{changed}); err == nil {
						t.Fatalf("same-profile %s mismatch accepted", name)
					}
				}
			}
			if stage == "accounted before delivery" || stage == "historical accounted" {
				if _, err := FinalizeWalletClaim(seed.WalletPath, claim.ClaimPath, next, claim.Request, legRecovery, at.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			before = strategyReconcileSnapshot(t, path, seed.WalletPath, seed.ClaimPath, claim.ClaimPath)
			selectedRecovery := legRecovery
			var historical []submitter.Policy
			wantFailure := false
			switch stage {
			case "historical policy", "historical duplicate", "historical accounted", "historical missing", "historical ambiguous", "historical no finality", "historical successful":
				selectedRecovery = submitter.Policy{}
				historical = []submitter.Policy{legRecovery}
				if stage == "historical duplicate" {
					duplicate := legRecovery
					route := *legRecovery.Jupiter
					duplicate.Jupiter = &route
					historical = append(historical, duplicate)
				}
				if stage == "historical missing" {
					historical = nil
					wantFailure = true
				}
				if stage == "historical ambiguous" || stage == "historical no finality" {
					other := legRecovery
					other.ControlStatePath = filepath.Join(filepath.Dir(path), "absent-recovery", "control.json")
					if stage == "historical ambiguous" {
						historical = append(historical, other)
					} else {
						historical = []submitter.Policy{other}
					}
					wantFailure = true
				}
			case "explicit wrong with history":
				selectedRecovery = originalRecovery
				historical = []submitter.Policy{legRecovery}
				wantFailure = true
			}
			if stage == "wrong leg recovery" {
				selectedRecovery = originalRecovery
				wantFailure = true
			}
			result, err := ReconcileStrategyJournal(path, seed.WalletPath, authority, originalRecovery, selectedRecovery, at.Add(2*time.Second), historical...)
			if wantFailure {
				if err == nil {
					t.Fatal("unavailable or ambiguous leg recovery accepted")
				}
				if stage == "historical ambiguous" && !strings.Contains(err.Error(), "ambiguous") {
					t.Fatalf("ambiguous policies reached recovery evidence before selection: %v", err)
				}
				assertStrategyReconcileUnchanged(t, before)
				return
			}
			if err != nil || result.BlockedReason != "" || result.ClaimPath != claim.ClaimPath || result.AccountingSHA256 == "" || result.Strategy.State == nil || result.Strategy.State.Pending() {
				t.Fatalf("continuation reconciliation: %+v, %v", result, err)
			}
			if stage == "historical successful" {
				if result.Strategy.State.NextSell() {
					t.Fatal("successful sell recovery did not switch the next direction to buy")
				}
				before = strategyReconcileSnapshot(t, path, seed.WalletPath, seed.ClaimPath, claim.ClaimPath)
				retried, err := ReconcileStrategyJournal(path, seed.WalletPath, authority, originalRecovery, submitter.Policy{}, at.Add(time.Hour), historical...)
				if err != nil || !reflect.DeepEqual(result.Strategy, retried.Strategy) {
					t.Fatalf("inferred successful recovery did not survive restart: %v", err)
				}
				assertStrategyReconcileUnchanged(t, before)
				return
			}
			if stage == "accounted before delivery" || stage == "historical accounted" {
				assertStrategyReconcileUnchanged(t, map[string][]byte{seed.WalletPath: before[seed.WalletPath], claim.ClaimPath: before[claim.ClaimPath]})
			}
			// Advance to a later still-unprepared decision. Reconciliation must
			// neither replay the old outcome into it nor clear its pending state.
			thirdAt := time.Unix(claim.Request.ScheduleWindowEndUnix, 0).UTC()
			for i, price := range []uint64{3_000_000_000, 2_500_000_000, 2_000_000_000} {
				observed := thirdAt.Add(time.Duration(i) * time.Minute)
				samples := strategyOutcomeSamples(seed, observed)
				samples[0].PriceMicros, samples[1].PriceMicros = price, price
				if _, err := ObserveStrategyJournal(path, authority, originalRecovery, observed, samples[0], samples[1], samples[2], samples[3], legRecovery); err != nil {
					t.Fatal(err)
				}
			}
			thirdAt = thirdAt.Add(2 * time.Minute)
			thirdPath := filepath.Join(filepath.Dir(path), "third-reconcile-acquisition.jsonl")
			seedStrategyAcquisition(t, thirdPath, continuationCandidate(t, candidate, "third reconciled transaction"), thirdAt, time.Minute)
			thirdAt = thirdAt.Add(time.Second)
			if _, err := CommitStrategyJournalDecision(path, authority, originalRecovery, next, thirdPath, time.Minute, thirdAt, legRecovery); err != nil {
				t.Fatal(err)
			}
			status, err := ReadStrategyJournalStatus(path, authority, originalRecovery, thirdAt, legRecovery)
			if err != nil {
				t.Fatal(err)
			}
			before = strategyReconcileSnapshot(t, path, seed.WalletPath, seed.ClaimPath, claim.ClaimPath)
			if _, err := ReconcileStrategyJournal(path, seed.WalletPath, authority, originalRecovery, originalRecovery, thirdAt); err == nil {
				t.Fatal("missing historical recovery accepted")
			}
			assertStrategyReconcileUnchanged(t, before)
			later, err := ReconcileStrategyJournal(path, seed.WalletPath, authority, originalRecovery, originalRecovery, thirdAt.Add(time.Hour), legRecovery)
			if err != nil || later.BlockedReason != "claim_not_prepared" || !reflect.DeepEqual(status, later.Strategy) {
				t.Fatalf("later pending decision changed: %+v, %v", later, err)
			}
			assertStrategyReconcileUnchanged(t, before)
		})
	}
}
