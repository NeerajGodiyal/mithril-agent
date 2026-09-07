package policyauthority

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

// Each offline acquisition uses a new blockhash and therefore a distinct signed
// transaction. The canonical candidate validator still checks the whole message.
func continuationCandidate(t *testing.T, candidate proposalcheck.Candidate, label string) proposalcheck.Candidate {
	t.Helper()
	message, err := base64.StdEncoding.DecodeString(candidate.MessageBase64)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := solana.DecodeV0Message(message, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(message, decoded.RecentBlockhash[:]) != 1 {
		t.Fatal("fixture blockhash is not unique")
	}
	next := sha256.Sum256([]byte(label))
	message = bytes.Replace(message, decoded.RecentBlockhash[:], next[:], 1)
	candidate.MessageBase64 = base64.StdEncoding.EncodeToString(message)
	if _, err := proposalcheck.EncodeCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	return candidate
}

// Unimplemented provider methods deliberately panic if reconciliation starts
// making unexpected calls. These fixtures never open a network connection.
type continuationFinalityProvider struct {
	txflow.EvidenceProvider
	t         *testing.T
	identity  string
	signature string
	status    solanarpc.SignatureStatus
	effect    solanarpc.TransactionEffect
}

func (p *continuationFinalityProvider) Identity() string { return p.identity }

func (p *continuationFinalityProvider) SignatureStatus(_ context.Context, signature string) (solanarpc.SignatureStatus, error) {
	if signature != p.signature {
		p.t.Error("reconciliation requested another signature")
	}
	return p.status, nil
}

func (p *continuationFinalityProvider) TransactionEffect(_ context.Context, signature string) (solanarpc.TransactionEffect, error) {
	if signature != p.signature {
		p.t.Error("reconciliation requested another transaction")
	}
	return p.effect, nil
}

// Reuse the signed offline transaction, but derive durable finalized evidence
// through the real reconciler from two raw provider observations.
func continuationRecovery(t *testing.T, authority Policy, claim WalletPaperClaim, wallet WalletInventory, failed bool, protectedPaths ...string) submitter.Policy {
	t.Helper()
	walletSeed := sha256.Sum256([]byte("strategy decision test wallet"))
	attestorSeed := sha256.Sum256([]byte("strategy decision test attestor"))
	policy, path := strategyOutcomeRecoveryFixture(t, authority, claim.Request,
		ed25519.NewKeyFromSeed(walletSeed[:]), ed25519.NewKeyFromSeed(attestorSeed[:]), failed)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	var result txflow.Reconciliation
	if err := json.Unmarshal(record["reconciliation"], &result); err != nil {
		t.Fatal(err)
	}
	slot := wallet.LastFinalizedSlot + 10
	result.Slot, result.PrimarySlot, result.SecondarySlot = slot, slot, slot
	effects := result.JupiterEffects
	effects.PrimaryEffectSlot, effects.SecondaryEffectSlot = slot, slot
	effects.Payer.PreLamports = wallet.NativeLamports
	effects.Payer.PostLamports = wallet.NativeLamports - claim.Request.FeeLamports
	effects.Token.PreUnits, effects.Token.PostUnits = wallet.TokenUnits, wallet.TokenUnits
	if !failed {
		// Actual finalized proceeds intentionally differ from the quotation.
		effects.OutputAmount += 123
		effects.Payer.PostLamports -= effects.InputAmount
		effects.Token.PostUnits += effects.OutputAmount
	}
	var transactionBase64 string
	if err := json.Unmarshal(record["transaction_base64"], &transactionBase64); err != nil {
		t.Fatal(err)
	}
	transaction, err := base64.StdEncoding.DecodeString(transactionBase64)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := solana.DecodeSignedV0Transaction(transaction, nil)
	if err != nil {
		t.Fatal(err)
	}
	inputAccount, err := orcaswap.AssociatedTokenAddress(policy.Source, orcaswap.WrappedSOLMint)
	if err != nil {
		t.Fatal(err)
	}
	inputIndex, outputIndex := -1, -1
	for index, key := range decoded.Message.AccountKeys {
		address := solana.Encode(key[:])
		if address == inputAccount {
			inputIndex = index
		}
		if address == effects.Token.Account {
			outputIndex = index
		}
	}
	if inputIndex <= 0 || outputIndex <= 0 {
		t.Fatal("continuation transaction lacks protected token accounts")
	}
	pre, post := make([]uint64, len(decoded.Message.AccountKeys)), make([]uint64, len(decoded.Message.AccountKeys))
	pre[0], post[0] = effects.Payer.PreLamports, effects.Payer.PostLamports
	pre[outputIndex], post[outputIndex] = 2_039_280, 2_039_280
	rawEffect := solanarpc.TransactionEffect{Slot: slot, Transaction: transaction, FeeLamports: claim.Request.FeeLamports,
		Failed: failed, ErrorFingerprint: result.PrimaryErrorFingerprint, PreBalances: pre, PostBalances: post,
		PreTokenBalances:  []solanarpc.TokenBalance{{AccountIndex: uint16(outputIndex), Mint: effects.Token.Mint, Owner: policy.Source, Amount: wallet.TokenUnits}},
		PostTokenBalances: []solanarpc.TokenBalance{{AccountIndex: uint16(outputIndex), Mint: effects.Token.Mint, Owner: policy.Source, Amount: effects.Token.PostUnits}}}
	// The temporary wrapped-SOL account is absent before and after this swap;
	// no unseen rent refund is introduced into the strategy's opening capital.
	record["finalized"] = json.RawMessage("false")
	delete(record, "reconciliation")
	raw, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := securefile.ReplacePrivate(path, raw, 1<<20); err != nil {
		t.Fatal(err)
	}
	status := solanarpc.SignatureStatus{Found: true, Slot: slot, ConfirmationStatus: "finalized", Failed: failed, ErrorFingerprint: result.PrimaryErrorFingerprint}
	primary := &continuationFinalityProvider{t: t, identity: policy.Evidence.PrimaryOriginSHA256, signature: result.Signature, status: status, effect: rawEffect}
	secondary := &continuationFinalityProvider{t: t, identity: policy.Evidence.SecondaryOriginSHA256, signature: result.Signature, status: status, effect: rawEffect}
	lifecycle, err := txflow.NewEvidenceLifecycle(primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	secondary.status.Slot++
	if _, result, err := submitter.ReconcileRecovery(t.Context(), policy, lifecycle); err != nil || result.Verdict != txflow.VerdictDiverged {
		t.Fatalf("divergent continuation finality: verdict=%s, err=%v", result.Verdict, err)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, after) {
		t.Fatalf("divergent finality mutated recovery: %v", err)
	}
	archive := filepath.Join(filepath.Dir(path), "submission-recovery."+claim.Request.ActionID+".finalized.json")
	if _, err := os.Lstat(archive); !os.IsNotExist(err) {
		t.Fatalf("divergent finality created an archive: %v", err)
	}
	if _, err := submitter.ReadJupiterFinalizedWalletEvidence(policy, claim.Request); err == nil {
		t.Fatal("divergent finality became accountable wallet evidence")
	}
	if len(protectedPaths) != 0 {
		if len(protectedPaths) != 2 {
			t.Fatal("negative continuation check requires wallet and strategy paths")
		}
		paths := append(append([]string(nil), protectedPaths...), claim.ClaimPath)
		before := make([][]byte, len(paths))
		for i, protected := range paths {
			before[i], err = os.ReadFile(protected)
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := FinalizeWalletClaim(protectedPaths[0], claim.ClaimPath, authority, claim.Request, policy, time.Unix(claim.Request.ScheduleWindowEndUnix, 0)); err == nil {
			t.Fatal("divergent continuation finality advanced wallet accounting")
		}
		for i, protected := range paths {
			after, err := os.ReadFile(protected)
			if err != nil || !bytes.Equal(before[i], after) {
				t.Fatalf("rejected finality changed protected journal %d: %v", i, err)
			}
		}
	}
	secondary.status = status
	if _, actual, err := submitter.ReconcileRecovery(t.Context(), policy, lifecycle); err != nil || actual.Verdict != result.Verdict {
		t.Fatalf("raw continuation reconciliation: verdict=%s, want=%s, err=%v", actual.Verdict, result.Verdict, err)
	}
	if _, err := submitter.ReadJupiterFinalizedWalletEvidence(policy, claim.Request); err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestStrategyContinuationPublicRepeatedFlow(t *testing.T) {
	for _, failed := range []bool{true, false} {
		t.Run(map[bool]string{true: "failed second leg then third decision", false: "successful second leg then actual reverse sizing"}[failed], func(t *testing.T) {
			path, seed, authority, originalRecovery, next, originalAcquisition, at := strategyDecisionFixture(t)
			acquired, err := proposalcheck.ReadAcquisition(originalAcquisition, at, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			candidate := continuationCandidate(t, acquired.Candidate, "second actual strategy transaction")
			acquisition := filepath.Join(filepath.Dir(path), "second-distinct-acquisition.jsonl")
			seedStrategyAcquisition(t, acquisition, candidate, at.Add(-time.Second), time.Minute)
			if _, err := CommitStrategyJournalDecision(path, authority, originalRecovery, next, acquisition, time.Minute, at); err != nil {
				t.Fatal(err)
			}
			records, err := journal.ReadRecords(path)
			if err != nil {
				t.Fatal(err)
			}
			decision := records[len(records)-1].Hash
			wallet, err := ReadWalletInventory(seed.WalletPath, next)
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
			claim, err := PrepareStrategyWalletClaim(t.Context(), seed.WalletPath, path, authority, originalRecovery, next, start, at, time.Minute, evidence,
				jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.PrimaryIdentity}, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.SecondaryIdentity}, strategyClaimLifecycle(t, wallet))
			if err != nil || claim.Inventory.PendingSHA256 == "" {
				t.Fatalf("second concrete claim: %v", err)
			}
			secondRecovery := continuationRecovery(t, next, claim, wallet, failed, seed.WalletPath, path)
			finalized, err := submitter.ReadJupiterFinalizedWalletEvidence(secondRecovery, claim.Request)
			if err != nil {
				t.Fatal(err)
			}
			accounted, err := FinalizeWalletClaim(seed.WalletPath, claim.ClaimPath, next, claim.Request, secondRecovery, at.Add(time.Second))
			if err != nil {
				t.Fatalf("second concrete accounting: %v", err)
			}
			delivered := at.Add(2 * time.Second)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, wrong := range []struct{ decision, accounting string }{{strings.Repeat("a", 64), accounted.HeadSHA256}, {decision, wallet.HeadSHA256}} {
				if _, err := ApplyStrategyContinuationOutcome(path, authority, originalRecovery, secondRecovery, wrong.decision, wrong.accounting, delivered); err == nil {
					t.Fatal("crossed continuation identity accepted")
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("invalid continuation mutated history: %v", err)
				}
			}
			state, err := ApplyStrategyContinuationOutcome(path, authority, originalRecovery, secondRecovery, decision, accounted.HeadSHA256, delivered)
			if err != nil {
				t.Fatalf("second strategy outcome: %v", err)
			}
			if state.Pending() || state.NextSell() != failed || state.Ledger().BaseUnits != accounted.NativeLamports || state.Ledger().QuoteUnits != accounted.TokenUnits {
				t.Fatal("second outcome did not fold actual wallet balances")
			}
			status, err := ReadStrategyJournalStatus(path, authority, originalRecovery, delivered, secondRecovery)
			if err != nil || status.PendingDecisionSHA256 != "" || !reflect.DeepEqual(status.State, state) {
				t.Fatalf("completed outcome retained a pending decision identity: %v", err)
			}
			wantFills := uint64(1)
			if failed {
				wantFills = 0
			}
			if uint64(state.Ledger().Fills) != wantFills {
				t.Fatal("failed transactions counted as strategy fills")
			}
			readAt := time.Unix(claim.Request.ScheduleWindowEndUnix, 0).UTC()
			prices := []uint64{3_000_000_000, 2_500_000_000, 2_000_000_000}
			if !failed {
				prices = []uint64{2_000_000_000, 2_100_000_000, 2_200_000_000, 2_300_000_000}
			}
			ready := false
			for i, price := range prices {
				observed := readAt.Add(time.Duration(i) * time.Minute)
				samples := strategyOutcomeSamples(seed, observed)
				samples[0].PriceMicros, samples[1].PriceMicros = price, price
				quote, err := ObserveStrategyJournal(path, authority, originalRecovery, observed, samples[0], samples[1], samples[2], samples[3], secondRecovery)
				if err != nil {
					t.Fatal(err)
				}
				if quote.ReadyForQuote {
					want := claim.Request.JupiterCandidate.Request.InputAmount
					if !failed {
						want = finalized.Finalized.OutputReceived
					}
					if quote.Sell != failed || quote.InputAmount != want {
						t.Fatal("next opportunity ignored actual outcome direction or proceeds")
					}
					ready, readAt = true, observed
					break
				}
			}
			if !ready {
				t.Fatal("no next opportunity after second accounted outcome")
			}
			history := []submitter.Policy{secondRecovery}
			if failed {
				third := continuationCandidate(t, candidate, "third actual strategy transaction")
				thirdPath := filepath.Join(filepath.Dir(path), "third-acquisition.jsonl")
				seedStrategyAcquisition(t, thirdPath, third, readAt, time.Minute)
				readAt = readAt.Add(time.Second)
				if _, err := CommitStrategyJournalDecision(path, authority, originalRecovery, next, thirdPath, time.Minute, readAt, secondRecovery); err != nil {
					t.Fatalf("third committed decision: %v", err)
				}
			} else {
				reverse, third := strategyReverseCandidate(t, next, finalized.Finalized.OutputReceived)
				if reverse.TransactionPolicy.AuthorizationLedgerPath != authority.TransactionPolicy.AuthorizationLedgerPath ||
					reverse.TransactionPolicy.ScheduleWindowSeconds != authority.TransactionPolicy.ScheduleWindowSeconds {
					t.Fatal("reverse fixture reset the authorization ledger or schedule")
				}
				thirdPath := filepath.Join(filepath.Dir(path), "third-reverse-acquisition.jsonl")
				seedStrategyAcquisition(t, thirdPath, third, readAt, time.Minute)
				readAt = readAt.Add(time.Second)
				committed, err := CommitStrategyJournalDecision(path, authority, originalRecovery, reverse, thirdPath, time.Minute, readAt, secondRecovery)
				if err != nil || !committed.Pending() {
					t.Fatalf("third reverse committed decision: %v", err)
				}
				thirdStatus, err := ReadStrategyJournalStatus(path, authority, originalRecovery, readAt, secondRecovery)
				if err != nil || thirdStatus.PendingDecisionSHA256 == "" {
					t.Fatalf("third reverse decision identity: %v", err)
				}
				beforeReverse := accounted
				reverseEvidence := &paperReserveEvidence{Evidence: strategyReverseEvidence{strategyClaimEvidence: strategyClaimEvidence{
					jupiterEvidence: &jupiterEvidence{primary: beforeReverse.Opening.PrimaryIdentity, secondary: beforeReverse.Opening.SecondaryIdentity},
					slot:            beforeReverse.LastFinalizedSlot, guard: reverse.TransactionPolicy.Jupiter.RouteGuard,
				}, wallet: beforeReverse}, mutate: func(account *txflow.AccountEvidence) {
					account.PrimaryLamports, account.SecondaryLamports = beforeReverse.NativeLamports, beforeReverse.NativeLamports
				}}
				thirdStart := anchor + (readAt.Unix()-anchor)/window*window
				thirdClaim, err := PrepareStrategyWalletClaim(t.Context(), seed.WalletPath, path, authority, originalRecovery, reverse, thirdStart, readAt, time.Minute, reverseEvidence,
					jupiterSlot{slot: beforeReverse.LastFinalizedSlot, identity: beforeReverse.Opening.PrimaryIdentity},
					jupiterSlot{slot: beforeReverse.LastFinalizedSlot, identity: beforeReverse.Opening.SecondaryIdentity}, strategyClaimLifecycle(t, beforeReverse), secondRecovery)
				if err != nil || thirdClaim.Inventory.PendingSHA256 == "" {
					t.Fatalf("third reverse concrete claim: %v", err)
				}
				if thirdClaim.Request.JupiterCandidate.Request.InputAmount != finalized.Finalized.OutputReceived ||
					thirdClaim.Request.JupiterCandidate.Request.DestinationTokenAccount != "" || thirdClaim.Request.ActionID == claim.Request.ActionID {
					t.Fatal("reverse claim did not bind exact received token units and native output")
				}
				thirdRecovery := strategyReverseRecovery(t, reverse, thirdClaim, beforeReverse)
				thirdFinalized, err := submitter.ReadJupiterFinalizedWalletEvidence(thirdRecovery, thirdClaim.Request)
				if err != nil {
					t.Fatal(err)
				}
				thirdAccounted, err := FinalizeWalletClaim(seed.WalletPath, thirdClaim.ClaimPath, reverse, thirdClaim.Request, thirdRecovery, readAt.Add(time.Second))
				if err != nil {
					t.Fatalf("third reverse concrete accounting: %v", err)
				}
				readAt = readAt.Add(2 * time.Second)
				thirdState, err := ApplyStrategyContinuationOutcome(path, authority, originalRecovery, thirdRecovery,
					thirdStatus.PendingDecisionSHA256, thirdAccounted.HeadSHA256, readAt, secondRecovery)
				if err != nil {
					t.Fatalf("third reverse strategy delivery: %v", err)
				}
				if thirdState.Pending() || !thirdState.NextSell() || thirdState.Ledger().Fills != 2 ||
					thirdState.Ledger().BaseUnits != thirdAccounted.NativeLamports || thirdState.Ledger().QuoteUnits != thirdAccounted.TokenUnits ||
					thirdAccounted.TokenUnits != beforeReverse.TokenUnits-finalized.Finalized.OutputReceived ||
					thirdAccounted.NativeLamports != beforeReverse.NativeLamports-thirdClaim.Request.FeeLamports+thirdFinalized.Finalized.OutputReceived {
					t.Fatal("reverse accounting did not apply exact token debit, native proceeds and fee")
				}
				history = append(history, thirdRecovery)
				strategyBytes, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				walletBytes, err := os.ReadFile(seed.WalletPath)
				if err != nil {
					t.Fatal(err)
				}
				restarted, err := ReadStrategyJournal(path, authority, originalRecovery, readAt, history...)
				if err != nil || !reflect.DeepEqual(thirdState, restarted) {
					t.Fatalf("reverse restart changed full strategy state: %v", err)
				}
				if _, err := FinalizeWalletClaim(seed.WalletPath, thirdClaim.ClaimPath, reverse, thirdClaim.Request, thirdRecovery, readAt.Add(time.Minute)); err != nil {
					t.Fatalf("reverse accounting retry: %v", err)
				}
				retried, err := ApplyStrategyContinuationOutcome(path, authority, originalRecovery, thirdRecovery,
					thirdStatus.PendingDecisionSHA256, thirdAccounted.HeadSHA256, readAt.Add(time.Minute), secondRecovery)
				if err != nil || !reflect.DeepEqual(thirdState, retried) {
					t.Fatalf("reverse delivery retry changed state: %v", err)
				}
				for file, want := range map[string][]byte{path: strategyBytes, seed.WalletPath: walletBytes} {
					got, err := os.ReadFile(file)
					if err != nil || !bytes.Equal(want, got) {
						t.Fatalf("reverse retry rewrote %s: %v", file, err)
					}
				}
			}
			state, err = ReadStrategyJournal(path, authority, originalRecovery, readAt, history...)
			if err != nil || state.Pending() != failed {
				t.Fatalf("restart pending state: %v", err)
			}
			before, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			retried, err := ApplyStrategyContinuationOutcome(path, authority, originalRecovery, secondRecovery, decision, accounted.HeadSHA256, readAt.Add(time.Hour), history...)
			if err != nil || !reflect.DeepEqual(state, retried) {
				t.Fatalf("historical outcome retry changed current state: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("historical outcome retry rewrote bytes")
			}
			if _, err := ReadStrategyJournal(path, authority, originalRecovery, readAt); err == nil {
				t.Fatal("missing historical recovery policy accepted")
			}
			changed := secondRecovery
			changed.ControlStatePath = filepath.Join(t.TempDir(), "other-control.json")
			if _, err := ReadStrategyJournal(path, authority, originalRecovery, readAt, changed); err == nil {
				t.Fatal("changed historical recovery policy accepted")
			}
		})
	}
}
