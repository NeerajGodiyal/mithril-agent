package policyauthority

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func seedRetainedWalletClaim(t *testing.T, path string, policy Policy, request signer.Request, at time.Time) {
	t.Helper()
	// Reuse the canonical host fixture, adding only the retained request field.
	// This tests admission composition, not ClaimPaperRequest's acquisition/replay.
	scratch := filepath.Join(t.TempDir(), "original.jsonl")
	seedWalletAccountingClaim(t, scratch, policy, request, at)
	records, err := journal.ReadRecords(scratch)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(bytes.TrimSuffix(records[0].Payload, []byte("}")), []byte(",\"request\":")...)
	payload = append(append(payload, raw...), '}')
	store, err := journal.OpenStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(at, records[0].Type, request.ActionID, json.RawMessage(payload)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := ReadClaimedPaperRequest(path, policy)
	if err != nil || !reflect.DeepEqual(got, request) {
		t.Fatalf("retained fixture: %+v, %v", got, err)
	}
}

func walletAdmissionBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestWalletAdmissionPublicRecoveryAndSizing(t *testing.T) {
	path, _, policy, request, opening, _, now := walletAccountingFixture(t)
	claimPath := path + ".claim-" + opening.HeadSHA256 + ".jsonl"
	before := walletAdmissionBytes(t, path)
	if _, found, err := RecoverWalletPaperClaim(path, policy, now); err != nil || found {
		t.Fatalf("empty recovery: found=%v, %v", found, err)
	}
	oversize := proposalcheck.PaperIntentBounds{NativeBudgetLamports: opening.NativeLamports + 1}
	_, err := PrepareWalletPaperClaim(t.Context(), path, policy, shadow.Policy{}, nil, oversize,
		proposalcheck.Candidate{}, request.ScheduleWindowStartUnix, now, 0, "", 0, nil, nil, nil, nil)
	if err == nil || err.Error() != "paper claim sizing exceeds accounted wallet balances" {
		t.Fatalf("oversized claim did not fail at inventory sizing: %v", err)
	}
	if _, err := os.Stat(claimPath); !errors.Is(err, os.ErrNotExist) || !bytes.Equal(before, walletAdmissionBytes(t, path)) {
		t.Fatal("recovery or failed sizing created state")
	}
	seedRetainedWalletClaim(t, claimPath, policy, request, now)
	expired := time.Unix(request.ScheduleWindowEndUnix, 0).UTC().Add(time.Hour)
	got, err := PrepareWalletPaperClaim(t.Context(), path, policy, shadow.Policy{}, nil, oversize,
		proposalcheck.Candidate{}, 0, expired, 0, "", 0, nil, nil, nil, nil)
	if err != nil || !got.Recovered || !reflect.DeepEqual(got.Request, request) {
		t.Fatalf("public expired recovery used new inputs: %+v, %v", got, err)
	}
	if _, found, err := RecoverWalletPaperClaim(path, policy, now.Add(-time.Second)); err == nil || found {
		t.Fatal("future retained claim was accepted")
	}
	store, err := journal.OpenStrict(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(now.Add(time.Second), "unexpected.tail", request.ActionID, struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	claimBefore := walletAdmissionBytes(t, claimPath)
	if _, found, err := RecoverWalletPaperClaim(path, policy, expired); err == nil || found {
		t.Fatal("unknown claim suffix was accepted")
	}
	if !bytes.Equal(claimBefore, walletAdmissionBytes(t, claimPath)) || !bytes.Equal(before, walletAdmissionBytes(t, path)) {
		t.Fatal("rejected recovery changed durable state")
	}
}

func TestWalletAdmissionRegressedClockRejectsBeforeCreation(t *testing.T) {
	path, _, policy, _, opening, _, now := walletAccountingFixture(t)
	before := walletAdmissionBytes(t, path)
	called := false
	_, err := prepareWalletPaperClaim(path, policy, now.Add(-2*time.Second),
		func(string, WalletInventory) (signer.Request, error) {
			called = true
			return signer.Request{}, errors.New("unexpected creation")
		}, nil)
	if err == nil || called {
		t.Fatalf("regressed clock reached creation: called=%v, err=%v", called, err)
	}
	if _, err := os.Stat(path + ".claim-" + opening.HeadSHA256 + ".jsonl"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("regressed clock created a claim")
	}
	if !bytes.Equal(before, walletAdmissionBytes(t, path)) {
		t.Fatal("regressed clock changed inventory")
	}
}

func TestWalletAdmissionCrashRetainsOriginalWithoutRenewal(t *testing.T) {
	for _, crash := range []string{"after claim", "during observation"} {
		t.Run(crash, func(t *testing.T) {
			path, _, policy, request, opening, _, now := walletAccountingFixture(t)
			claimPath := path + ".claim-" + opening.HeadSHA256 + ".jsonl"
			before := walletAdmissionBytes(t, path)
			failure := errors.New("interrupted admission")
			_, err := prepareWalletPaperClaim(path, policy, now, func(actual string, _ WalletInventory) (signer.Request, error) {
				if actual != claimPath {
					t.Fatalf("claim path = %q", actual)
				}
				seedRetainedWalletClaim(t, actual, policy, request, now)
				if crash == "after claim" {
					return signer.Request{}, failure
				}
				return request, nil
			}, func(WalletInventory) (txflow.WalletObservation, error) { return txflow.WalletObservation{}, failure })
			if !errors.Is(err, failure) {
				t.Fatalf("interruption = %v", err)
			}
			claimBefore := walletAdmissionBytes(t, claimPath)
			got, err := prepareWalletPaperClaim(path, policy, time.Unix(request.ScheduleWindowEndUnix, 0).UTC().Add(time.Hour),
				func(string, WalletInventory) (signer.Request, error) {
					t.Fatal("expired retained claim recreated from new inputs")
					return signer.Request{}, nil
				}, func(WalletInventory) (txflow.WalletObservation, error) {
					t.Fatal("recovery refreshed or reserved balances")
					return txflow.WalletObservation{}, nil
				})
			if err != nil || !got.Recovered || got.ClaimPath != claimPath || !reflect.DeepEqual(got.Request, request) || got.Inventory != opening {
				t.Fatalf("recovery = %+v, %v", got, err)
			}
			if !bytes.Equal(before, walletAdmissionBytes(t, path)) || !bytes.Equal(claimBefore, walletAdmissionBytes(t, claimPath)) {
				t.Fatal("recovery rewrote original claim or inventory")
			}
			if _, err := os.Stat(policy.TransactionPolicy.AuthorizationLedgerPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("admission touched signer ledger: %v", err)
			}
		})
	}
}

func TestWalletAdmissionPendingUsesOriginalHeadAndFinalizationAdvances(t *testing.T) {
	path, _, policy, request, opening, balances, now := walletAccountingFixture(t)
	policyBefore, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	got, err := prepareWalletPaperClaim(path, policy, now, func(actual string, current WalletInventory) (signer.Request, error) {
		if current != opening {
			t.Fatal("claim creation did not receive locked inventory")
		}
		if _, found, err := RecoverWalletPaperClaim(path, policy, now); !errors.Is(err, journal.ErrLocked) || found {
			t.Fatalf("concurrent recovery bypassed admission lock: found=%v, err=%v", found, err)
		}
		seedRetainedWalletClaim(t, actual, policy, request, now)
		return request, nil
	}, func(value WalletInventory) (txflow.WalletObservation, error) {
		if value != opening {
			t.Fatal("observation did not use opening head")
		}
		return opening.Opening, nil
	})
	if err != nil || got.Recovered || got.Inventory.PendingSHA256 == "" {
		t.Fatalf("admit = %+v, %v", got, err)
	}
	before := walletAdmissionBytes(t, path)
	repeated, err := prepareWalletPaperClaim(path, policy, now.Add(time.Hour), nil, nil)
	if err != nil || !repeated.Recovered || repeated.ClaimPath != got.ClaimPath || repeated.Inventory != got.Inventory || !reflect.DeepEqual(repeated.Request, request) {
		t.Fatalf("pending recovery = %+v, %v", repeated, err)
	}
	if !bytes.Equal(before, walletAdmissionBytes(t, path)) {
		t.Fatal("pending recovery changed inventory")
	}
	final, err := finalizeWalletClaim(path, got.ClaimPath, policy, request, now.Add(time.Second),
		func() (submitter.JupiterFinalizedWalletEvidence, error) { return balances, nil })
	if err != nil {
		t.Fatal(err)
	}
	claimBefore := walletAdmissionBytes(t, got.ClaimPath)
	accountedBefore := walletAdmissionBytes(t, path)
	_, err = PrepareWalletPaperClaim(t.Context(), path, policy, shadow.Policy{}, nil,
		proposalcheck.PaperIntentBounds{}, proposalcheck.Candidate{}, request.ScheduleWindowStartUnix,
		now.Add(2*time.Second), 0, "", 0, nil, nil, nil, nil)
	if err == nil || err.Error() != "wallet action was already claimed; no new claim created" {
		t.Fatalf("same-window action reached new preparation: %v", err)
	}
	if _, err := os.Stat(path + ".claim-" + final.HeadSHA256 + ".jsonl"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("duplicate action left a new-head claim")
	}
	if !bytes.Equal(accountedBefore, walletAdmissionBytes(t, path)) {
		t.Fatal("duplicate action changed accounted inventory")
	}
	stop := errors.New("next request not yet prepared")
	_, err = prepareWalletPaperClaim(path, policy, now.Add(2*time.Second), func(next string, current WalletInventory) (signer.Request, error) {
		if current != final {
			t.Fatal("next claim did not receive accounted balances")
		}
		if next == got.ClaimPath || next != path+".claim-"+final.HeadSHA256+".jsonl" {
			t.Fatalf("next claim was not bound to finalized head: %q", next)
		}
		return signer.Request{}, stop
	}, nil)
	if !errors.Is(err, stop) || !bytes.Equal(claimBefore, walletAdmissionBytes(t, got.ClaimPath)) {
		t.Fatalf("next preparation changed original claim: %v", err)
	}
	policyAfter, err := json.Marshal(policy)
	if err != nil || !bytes.Equal(policyBefore, policyAfter) {
		t.Fatal("admission changed protected spending policy")
	}
}

func TestWalletAdmissionRejectsTornAndConflictingClaimsWithoutWrites(t *testing.T) {
	for _, damage := range []string{"claim torn", "inventory torn", "legacy claim", "pending missing", "pending conflict"} {
		t.Run(damage, func(t *testing.T) {
			path, originalPath, policy, request, opening, _, now := walletAccountingFixture(t)
			claimPath := path + ".claim-" + opening.HeadSHA256 + ".jsonl"
			if damage == "legacy claim" {
				seedWalletAccountingClaim(t, claimPath, policy, request, now)
			} else {
				seedRetainedWalletClaim(t, claimPath, policy, request, now)
			}
			if damage == "pending missing" || damage == "pending conflict" {
				reserved := claimPath
				if damage == "pending conflict" {
					reserved = originalPath
				}
				if _, err := reserveWalletClaim(path, reserved, policy, request, now,
					func(WalletInventory) (txflow.WalletObservation, error) { return opening.Opening, nil }); err != nil {
					t.Fatal(err)
				}
				if damage == "pending missing" {
					if err := os.Remove(claimPath); err != nil {
						t.Fatal(err)
					}
				}
			} else if damage != "legacy claim" {
				damaged := claimPath
				if damage == "inventory torn" {
					damaged = path
				}
				if err := os.WriteFile(damaged, append(walletAdmissionBytes(t, damaged), '{'), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := walletAdmissionBytes(t, path)
			var claimBefore []byte
			if damage != "pending missing" {
				claimBefore = walletAdmissionBytes(t, claimPath)
			}
			_, err := prepareWalletPaperClaim(path, policy, now.Add(time.Hour), func(string, WalletInventory) (signer.Request, error) {
				t.Fatal("invalid retained state reached new claim creation")
				return signer.Request{}, nil
			}, func(WalletInventory) (txflow.WalletObservation, error) {
				t.Fatal("invalid retained state reached observation")
				return txflow.WalletObservation{}, nil
			})
			if err == nil || !bytes.Equal(before, walletAdmissionBytes(t, path)) {
				t.Fatalf("invalid state accepted or changed: %v", err)
			}
			if damage != "pending missing" && !bytes.Equal(claimBefore, walletAdmissionBytes(t, claimPath)) {
				t.Fatal("invalid claim was repaired or replaced")
			}
			if damage == "pending missing" {
				if _, err := os.Stat(claimPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("missing pending claim was recreated")
				}
			}
		})
	}
}
