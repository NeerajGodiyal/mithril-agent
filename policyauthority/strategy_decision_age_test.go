package policyauthority

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func TestAcquireStrategyWalletClaimCannotExtendCommittedDecisionAge(t *testing.T) {
	path, seed, authority, recovery, next, original, historicalAt := strategyDecisionFixture(t)
	wallet, err := ReadWalletInventory(seed.WalletPath, next)
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := proposalcheck.ReadAcquisition(original, historicalAt, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-121 * time.Second)
	for i, price := range []uint64{3_000_000_000, 2_500_000_000, 2_000_000_000} {
		observed := base.Add(time.Duration(i) * time.Minute)
		samples := strategyOutcomeSamples(seed, observed)
		samples[0].PriceMicros, samples[1].PriceMicros = price, price
		if _, err := ObserveStrategyJournal(path, authority, recovery, observed, samples[0], samples[1], samples[2], samples[3]); err != nil {
			t.Fatal(err)
		}
	}
	claimPath := seed.WalletPath + ".claim-" + wallet.HeadSHA256 + ".jsonl"
	acquisition := claimPath + ".acquisition.jsonl"
	proposal := WalletAdmissionBuildForTest(t, continuationCandidate(t, acquired.Candidate, "committed decision original age"))
	var held *journal.Store
	t.Cleanup(func() {
		if held != nil {
			if err := held.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	calls := 0
	builder := strategyAcquisitionBuilder(func(_ context.Context, request jupiterquote.Request) (jupiterquote.BuildResult, error) {
		calls++
		if request != acquired.Candidate.Request {
			t.Fatal("builder changed the ready request")
		}
		var err error
		held, err = journal.OpenStrict(claimPath)
		if err != nil {
			return jupiterquote.BuildResult{}, err
		}
		proposal.Quote.ReceivedAt = time.Now().UTC()
		proposal.Quote.ResponseSHA256 = strings.Repeat("a", 64)
		return proposal, nil
	})
	evidence := &paperReserveEvidence{Evidence: strategyClaimEvidence{
		jupiterEvidence: &jupiterEvidence{primary: wallet.Opening.PrimaryIdentity, secondary: wallet.Opening.SecondaryIdentity},
		slot:            wallet.LastFinalizedSlot, guard: next.TransactionPolicy.Jupiter.RouteGuard,
	}, mutate: func(account *txflow.AccountEvidence) {
		account.PrimaryLamports, account.SecondaryLamports = wallet.NativeLamports, wallet.NativeLamports
	}}
	at := time.Now().UTC()
	window, anchor := int64(next.TransactionPolicy.ScheduleWindowSeconds), next.TransactionPolicy.ScheduleAnchorUnix
	start := anchor + (at.Unix()-anchor)/window*window
	_, err = AcquireStrategyWalletClaim(t.Context(), seed.WalletPath, path, authority, recovery, next,
		start, at, 5*time.Second, time.Minute, builder, evidence,
		jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.PrimaryIdentity},
		jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.SecondaryIdentity}, strategyClaimLifecycle(t, wallet))
	if !errors.Is(err, journal.ErrLocked) || calls != 1 || held == nil {
		t.Fatalf("did not interrupt after acquisition at claim lock: calls=%d, %v", calls, err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	held = nil
	retryAt := at.Add(10 * time.Second)
	status, err := ReadStrategyJournalStatus(path, authority, recovery, retryAt)
	if err != nil || status.State == nil || !status.State.Pending() || status.PendingDecisionSHA256 == "" {
		t.Fatalf("interruption did not retain the committed decision: %v", err)
	}
	if _, err := proposalcheck.ReadAcquisition(acquisition, retryAt, time.Minute); err != nil {
		t.Fatalf("quote must independently remain fresh: %v", err)
	}
	before := strategyReconcileSnapshot(t, path, seed.WalletPath, seed.ClaimPath, claimPath, acquisition, acquisition+".intent.jsonl")
	for _, age := range []time.Duration{5 * time.Second, 30 * time.Second} {
		t.Run(age.String(), func(t *testing.T) {
			// Derive a valid current window so schedule expiry cannot mask the
			// decision-age regression if the fixture straddles a window boundary.
			start := anchor + (retryAt.Unix()-anchor)/window*window
			claim, err := AcquireStrategyWalletClaim(t.Context(), seed.WalletPath, path, authority, recovery, next,
				start, retryAt, age, time.Minute, nil, evidence,
				jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.PrimaryIdentity},
				jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.SecondaryIdentity}, strategyClaimLifecycle(t, wallet))
			if err == nil || !strings.Contains(err.Error(), "original recency bound") || claim.Request.ActionID != "" {
				t.Fatalf("retry extended original 5s decision age to %s and admitted a claim: %v", age, err)
			}
			assertStrategyReconcileUnchanged(t, before)
		})
	}
}

func TestCommitStrategyDecisionHonorsManagedAge(t *testing.T) {
	for _, timely := range []bool{true, false} {
		t.Run(map[bool]string{true: "timely", false: "expired"}[timely], func(t *testing.T) {
			path, seed, authority, recovery, next, acquisition, expiry := strategyRetirementFixture(t, 5*time.Second)
			at := expiry.Add(time.Second)
			if timely {
				at = expiry.Add(-time.Second)
			}
			if _, err := proposalcheck.ReadAcquisition(acquisition, at, time.Minute); err != nil {
				t.Fatal(err)
			}
			before := strategyReconcileSnapshot(t, path, seed.WalletPath, seed.ClaimPath, acquisition, acquisition+".intent.jsonl")
			state, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, at)
			if timely {
				if err != nil || state == nil || !state.Pending() {
					t.Fatalf("timely managed commit: %v", err)
				}
				delete(before, path)
			} else if err == nil || !strings.Contains(err.Error(), "original recency bound") {
				t.Fatalf("expired managed commit passed original age: %v", err)
			}
			assertStrategyReconcileUnchanged(t, before)
		})
	}
}

func TestManagedStrategyDecisionEffectiveAgeBoundary(t *testing.T) {
	path, seed, authority, recovery, next, acquisition, expiry := strategyRetirementFixture(t, 5*time.Second)
	committedAt := expiry.Add(-4 * time.Second)
	if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, committedAt); err != nil {
		t.Fatal(err)
	}
	records, err := journal.ReadRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	var decision strategyDecision
	if err := decodeStrategyPayload(records[len(records)-1].Payload, &decision); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "managed metadata", true: "legacy sidecar"}[legacy], func(t *testing.T) {
			value := decision
			if legacy {
				value.ManagedIntentSHA256, value.MaxDecisionAgeNS = "", 0
			}
			for _, test := range []struct {
				name  string
				age   time.Duration
				at    time.Time
				valid bool
			}{
				{"original boundary", 30 * time.Second, expiry, true},
				{"original expired", 30 * time.Second, expiry.Add(time.Nanosecond), false},
				{"stricter boundary", 2 * time.Second, value.ObservationAt.Add(2 * time.Second), true},
				{"stricter expired", 2 * time.Second, value.ObservationAt.Add(2*time.Second + time.Nanosecond), false},
			} {
				t.Run(test.name, func(t *testing.T) {
					err := checkStrategyClaimTime(seed, value, committedAt, test.at, test.age)
					if test.valid && err != nil {
						t.Fatalf("exact effective boundary rejected: %v", err)
					}
					if !test.valid && (err == nil || !strings.Contains(err.Error(), "original recency bound")) {
						t.Fatalf("elapsed effective boundary accepted or rejected elsewhere: %v", err)
					}
				})
			}
		})
	}
}

func TestManagedStrategyDecisionProvenanceAndLegacy(t *testing.T) {
	for _, mode := range []string{"missing intent", "empty intent", "torn intent", "hash mismatch", "age mismatch", "legacy managed", "legacy standalone"} {
		t.Run(mode, func(t *testing.T) {
			path, seed, authority, recovery, next, acquisition, expiry := strategyRetirementFixture(t, 5*time.Second)
			if mode == "legacy standalone" {
				if err := os.Remove(acquisition + ".intent.jsonl"); err != nil {
					t.Fatal(err)
				}
			}
			at := expiry.Add(-time.Second)
			if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, at); err != nil {
				t.Fatal(err)
			}
			if mode == "missing intent" {
				if err := os.Remove(acquisition + ".intent.jsonl"); err != nil {
					t.Fatal(err)
				}
			} else if mode == "torn intent" || mode == "empty intent" {
				var raw []byte
				if mode == "torn intent" {
					raw = []byte("{")
				}
				if err := os.WriteFile(acquisition+".intent.jsonl", raw, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				records, err := journal.ReadRecords(path)
				if err != nil {
					t.Fatal(err)
				}
				var payload strategyDecision
				if err := decodeStrategyPayload(records[len(records)-1].Payload, &payload); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "hash mismatch":
					payload.ManagedIntentSHA256 = strings.Repeat("b", 64)
				case "age mismatch":
					payload.MaxDecisionAgeNS = int64(30 * time.Second)
				default:
					payload.ManagedIntentSHA256 = ""
					payload.MaxDecisionAgeNS = 0
				}
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				records[len(records)-1].Payload = raw
				// Reseal the offline fixture to test semantic provenance, not merely
				// the journal's independent hash-integrity rejection.
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				store, err := journal.OpenStrict(path)
				if err != nil {
					t.Fatal(err)
				}
				for _, record := range records {
					if _, err := store.Append(record.At, record.Type, record.ActionID, record.Payload); err != nil {
						t.Fatal(errors.Join(err, store.Close()))
					}
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			before := strategyReconcileSnapshot(t, path, seed.WalletPath, seed.ClaimPath, acquisition)
			status, err := ReadStrategyJournalStatus(path, authority, recovery, expiry.Add(time.Second))
			legacy := mode == "legacy managed" || mode == "legacy standalone"
			if !legacy {
				if err == nil {
					t.Fatal("invalid managed provenance replayed")
				}
				want := map[string]string{"missing intent": "managed strategy decision intent is unavailable", "empty intent": "original acquisition intent", "torn intent": "incomplete final record", "hash mismatch": "managed strategy decision age provenance changed", "age mismatch": "managed strategy decision age provenance changed"}[mode]
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("wrong provenance rejection: %v; want %q", err, want)
				}
			} else {
				if err != nil || status.State == nil || !status.State.Pending() {
					t.Fatalf("legacy replay changed: %v", err)
				}
				wallet, err := ReadWalletInventory(seed.WalletPath, next)
				if err != nil {
					t.Fatal(err)
				}
				evidence := &paperReserveEvidence{Evidence: strategyClaimEvidence{jupiterEvidence: &jupiterEvidence{primary: wallet.Opening.PrimaryIdentity, secondary: wallet.Opening.SecondaryIdentity}, slot: wallet.LastFinalizedSlot, guard: next.TransactionPolicy.Jupiter.RouteGuard}, mutate: func(account *txflow.AccountEvidence) {
					account.PrimaryLamports, account.SecondaryLamports = wallet.NativeLamports, wallet.NativeLamports
				}}
				retryAt := expiry.Add(time.Second)
				window, anchor := int64(next.TransactionPolicy.ScheduleWindowSeconds), next.TransactionPolicy.ScheduleAnchorUnix
				claim, err := PrepareStrategyWalletClaim(t.Context(), seed.WalletPath, path, authority, recovery, next, anchor+(retryAt.Unix()-anchor)/window*window, retryAt, 30*time.Second, evidence, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.PrimaryIdentity}, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.SecondaryIdentity}, strategyClaimLifecycle(t, wallet))
				if mode == "legacy managed" {
					if err == nil || !strings.Contains(err.Error(), "original recency bound") {
						t.Fatalf("legacy sidecar age was lost: %v", err)
					}
				} else {
					if err != nil || claim.Inventory.PendingSHA256 == "" {
						t.Fatalf("legacy standalone preparation changed: %v", err)
					}
					delete(before, seed.WalletPath)
				}
			}
			assertStrategyReconcileUnchanged(t, before)
		})
	}
}
