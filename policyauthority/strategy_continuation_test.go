package policyauthority

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

// The live preparation API already refuses expired acquisition. This regression
// reconstructs a canonical historical claim at the boundary to prove that
// outcome replay independently enforces the same rule, rather than trusting its
// writer. All subsequent reservation, signed recovery and accounting are real
// public code over protected offline fixture journals.
func TestStrategyContinuationChecksAcquisitionAtClaimCreation(t *testing.T) {
	for _, excess := range []time.Duration{0, time.Nanosecond} {
		t.Run(excess.String(), func(t *testing.T) {
			path, seed, authority, originalRecovery, next, originalAcquisition, at := strategyDecisionFixture(t)
			acquired, err := proposalcheck.ReadAcquisition(originalAcquisition, at, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			candidate := continuationCandidate(t, acquired.Candidate, "historical acquisition expiry transaction")
			acquisition := filepath.Join(filepath.Dir(path), "expiry-acquisition.jsonl")
			seedStrategyAcquisition(t, acquisition, candidate, acquired.ReceivedAt, time.Minute)
			if _, err := CommitStrategyJournalDecision(path, authority, originalRecovery, next, acquisition, time.Minute, at); err != nil {
				t.Fatal(err)
			}
			strategyRecords, err := journal.ReadRecords(path)
			if err != nil {
				t.Fatal(err)
			}
			decision := strategyRecords[len(strategyRecords)-1].Hash
			wallet, err := ReadWalletInventory(seed.WalletPath, next)
			if err != nil {
				t.Fatal(err)
			}
			originalWallet, err := journal.ReadRecords(seed.WalletPath)
			if err != nil {
				t.Fatal(err)
			}
			evidence := &paperReserveEvidence{Evidence: strategyClaimEvidence{
				jupiterEvidence: &jupiterEvidence{primary: wallet.Opening.PrimaryIdentity, secondary: wallet.Opening.SecondaryIdentity},
				slot:            wallet.LastFinalizedSlot, guard: next.TransactionPolicy.Jupiter.RouteGuard,
			}, mutate: func(account *txflow.AccountEvidence) {
				account.PrimaryLamports, account.SecondaryLamports = wallet.NativeLamports, wallet.NativeLamports
			}}
			window, anchor := int64(next.TransactionPolicy.ScheduleWindowSeconds), next.TransactionPolicy.ScheduleAnchorUnix
			start := anchor + (at.Unix()-anchor)/window*window
			claim, err := PrepareStrategyWalletClaim(t.Context(), seed.WalletPath, path, authority, originalRecovery, next,
				start, at, 5*time.Minute, evidence,
				jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.PrimaryIdentity},
				jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.SecondaryIdentity}, strategyClaimLifecycle(t, wallet))
			if err != nil {
				t.Fatalf("initial concrete claim: %v", err)
			}
			claims, err := journal.ReadRecords(claim.ClaimPath)
			if err != nil || len(claims) != 1 {
				t.Fatalf("read original claim: %v", err)
			}
			claimAt := acquired.ReceivedAt.Add(time.Minute + excess)
			claims[0].At = claimAt
			rewriteStrategyExpiryJournal(t, claim.ClaimPath, claims)
			rewriteStrategyExpiryJournal(t, seed.WalletPath, originalWallet)
			if _, err := ReadClaimedPaperRequest(claim.ClaimPath, next); err != nil {
				t.Fatalf("historical claim remains independently canonical: %v", err)
			}
			if _, err := ReserveWalletClaim(t.Context(), seed.WalletPath, claim.ClaimPath, next, claim.Request,
				strategyClaimLifecycle(t, wallet), claimAt); err != nil {
				t.Fatalf("historical reservation: %v", err)
			}
			recovery := continuationRecovery(t, next, claim, wallet, true)
			accounted, err := FinalizeWalletClaim(seed.WalletPath, claim.ClaimPath, next, claim.Request, recovery, claimAt.Add(time.Second))
			if err != nil {
				t.Fatalf("historical accounting: %v", err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			result, err := ApplyStrategyContinuationOutcome(path, authority, originalRecovery, recovery,
				decision, accounted.HeadSHA256, claimAt.Add(2*time.Second))
			if excess == 0 {
				if err != nil || result == nil || result.Pending() {
					t.Fatalf("exact acquisition expiry boundary rejected: %v", err)
				}
				return
			}
			if err == nil || result != nil {
				t.Fatal("historical claim created 1ns after acquisition expiry was accepted")
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatal("expired historical claim changed strategy history")
			}
		})
	}
}

func rewriteStrategyExpiryJournal(t *testing.T, path string, records []journal.Record) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	store, err := journal.OpenStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if _, err := store.Append(record.At, record.Type, record.ActionID, record.Payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
