package policyauthority

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
)

// strategyDecisionFixture retains actual signed test recovery and a canonical
// acquired candidate. The acquisition receipt is offline host fixture evidence,
// not a claim that this test contacted Jupiter or checked live chain accounts.
func strategyDecisionFixture(t *testing.T) (string, strategySeed, Policy, submitter.Policy, Policy, string, time.Time) {
	t.Helper()
	walletSeed := sha256.Sum256([]byte("strategy decision test wallet"))
	walletKey := ed25519.NewKeyFromSeed(walletSeed[:])
	attestorSeed := sha256.Sum256([]byte("strategy decision test attestor"))
	attestorKey := ed25519.NewKeyFromSeed(attestorSeed[:])
	path, seed, authority, request, now := strategyJournalFixtureWithIdentity(t,
		solana.Encode(walletKey.Public().(ed25519.PublicKey)), solana.Encode(attestorKey.Public().(ed25519.PublicKey)))
	recovery, _ := strategyOutcomeRecoveryFixture(t, authority, request, walletKey, attestorKey, true)
	if _, err := InitializeStrategyJournal(path, seed.WalletPath, seed.ClaimPath, authority, recovery, seed.Policy, seed.Ticks, seed.Bounds, now); err != nil {
		t.Fatal(err)
	}
	wallet, err := FinalizeWalletClaim(seed.WalletPath, seed.ClaimPath, authority, request, recovery, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyStrategyJournalOutcome(path, authority, recovery, wallet.HeadSHA256, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Advance to the next existing schedule window without changing any signer
	// caps or profile. A real observation gap resets only the engine's price
	// warmup, not its ledger, costs or risk state.
	start := time.Unix(request.ScheduleWindowEndUnix, 0).UTC()
	var at time.Time
	for i, price := range []uint64{3_000_000_000, 2_500_000_000, 2_000_000_000} {
		at = start.Add(time.Duration(i) * time.Minute)
		x := strategyOutcomeSamples(seed, at)
		x[0].PriceMicros, x[1].PriceMicros = price, price
		ready, err := ObserveStrategyJournal(path, authority, recovery, at, x[0], x[1], x[2], x[3])
		if err != nil {
			t.Fatal(err)
		}
		if i == 2 && (!ready.ReadyForQuote || !ready.Sell || ready.InputAmount != request.JupiterCandidate.Request.InputAmount) {
			t.Fatalf("actual post-failure decision is not ready: %+v", ready)
		}
	}
	next := authority
	candidate := *request.JupiterCandidate
	acquisitionPath := filepath.Join(filepath.Dir(path), "next-acquisition.jsonl")
	seedStrategyAcquisition(t, acquisitionPath, candidate, at, time.Minute)
	return path, seed, authority, recovery, next, acquisitionPath, at.Add(time.Second)
}

func seedStrategyAcquisition(t *testing.T, path string, candidate proposalcheck.Candidate, at time.Time, maxAge time.Duration) {
	t.Helper()
	raw, err := proposalcheck.EncodeCandidate(candidate)
	if err != nil {
		t.Fatalf("acquired message fixture must independently validate: %v", err)
	}
	portable, err := proposalcheck.DecodeCandidate(raw)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	receipt := struct {
		CandidateSHA256 string                   `json:"candidate_sha256"`
		ResponseSHA256  string                   `json:"response_sha256"`
		ReceivedAt      time.Time                `json:"received_at"`
		MaxAge          time.Duration            `json:"max_age_ns,string"`
		Candidate       *proposalcheck.Candidate `json:"candidate,omitempty"`
	}{hex.EncodeToString(digest[:]), strings.Repeat("a", 64), at, maxAge, &portable}
	store, err := journal.OpenStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(at, "proposal.acquired-v1", receipt.CandidateSHA256, receipt); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := proposalcheck.ReadAcquisition(path, at, maxAge); err != nil {
		t.Fatalf("acquisition fixture: %v", err)
	}
}

func TestStrategyDecisionPersistsOriginalAcquisition(t *testing.T) {
	path, seed, authority, recovery, next, acquisition, at := strategyDecisionFixture(t)
	before, err := ReadStrategyJournal(path, authority, recovery, at)
	if err != nil {
		t.Fatal(err)
	}
	state, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, at)
	if err != nil || state == nil || !state.Pending() {
		t.Fatalf("commit acquired decision: %v", err)
	}
	if !reflect.DeepEqual(state.Ledger(), before.Ledger()) {
		t.Fatal("quote commit changed accounted ledger")
	}
	records, err := journal.ReadRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	last := records[len(records)-1]
	var receipt strategyDecision
	if err := decodeStrategyPayload(last.Payload, &receipt); err != nil || last.Type != strategyDecisionEvent ||
		receipt.PreviousHeadSHA256 != records[len(records)-2].Hash || !receipt.AcquiredAt.Equal(at.Add(-time.Second)) || !last.At.Equal(at) {
		t.Fatalf("original decision provenance: %+v, %v", receipt, err)
	}
	observed := at.Add(30 * time.Second)
	x := strategyOutcomeSamples(seed, observed)
	if _, err := ObserveStrategyJournal(path, authority, recovery, observed, x[0], x[1], x[2], x[3]); err != nil {
		t.Fatal(err)
	}
	state, err = ReadStrategyJournal(path, authority, recovery, observed)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, at.Add(time.Hour))
	if err != nil || !reflect.DeepEqual(state, retried) {
		t.Fatalf("expired exact retry renewed or lost state: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("retry changed original decision or later observations")
	}
	if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute+1, at.Add(time.Hour)); err == nil {
		t.Fatal("changed age accepted as exact retry")
	}
}

func TestStrategyDecisionRejectsMissingOrStaleEvidence(t *testing.T) {
	for _, name := range []string{"missing", "expired", "policy", "predates observation", "torn", "wallet lock"} {
		t.Run(name, func(t *testing.T) {
			path, _, authority, recovery, next, acquisition, at := strategyDecisionFixture(t)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "missing":
				acquisition += ".missing"
			case "expired":
				at = at.Add(time.Minute)
			case "policy":
				next.JupiterProviders = &proposalcheck.ProviderBindings{}
			case "predates observation":
				x := strategyOutcomeSamples(strategySeedFromRecords(t, path), at)
				if _, err := ObserveStrategyJournal(path, authority, recovery, at, x[0], x[1], x[2], x[3]); err != nil {
					t.Fatal(err)
				}
				before, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			case "torn":
				f, err := os.OpenFile(acquisition, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteString("{"); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			case "wallet lock":
				seed := strategySeedFromRecords(t, path)
				lock, err := journal.OpenStrict(seed.WalletPath)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := lock.Close(); err != nil {
						t.Error(err)
					}
				})
			}
			result, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, at)
			if err == nil || result != nil {
				t.Fatalf("invalid decision accepted: %v", err)
			}
			if name == "wallet lock" && !errors.Is(err, journal.ErrLocked) {
				t.Fatalf("wallet lock failure: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("invalid decision mutated strategy history")
			}
		})
	}
}

func strategySeedFromRecords(t *testing.T, path string) strategySeed {
	t.Helper()
	records, err := journal.ReadRecords(path)
	if err != nil || len(records) == 0 {
		t.Fatalf("read seed: %v", err)
	}
	var seed strategySeed
	if err := decodeStrategyPayload(records[0].Payload, &seed); err != nil {
		t.Fatal(err)
	}
	return seed
}

func TestStrategyDecisionRejectsConcurrentHistoryChange(t *testing.T) {
	path, seed, authority, recovery, _, _, at := strategyDecisionFixture(t)
	verified, err := journal.ReadRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	x := strategyOutcomeSamples(seed, at)
	if _, err := ObserveStrategyJournal(path, authority, recovery, at, x[0], x[1], x[2], x[3]); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendStrategyDecision(path, verified, journal.Record{At: at, ActionID: seed.ClaimSHA256}, strategyDecision{}); err == nil {
		t.Fatal("stale verified strategy head was appended")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("concurrent history was modified")
	}
}

func TestStrategyDecisionRejectsExcessFeeBudget(t *testing.T) {
	path, seed, authority, recovery, next, acquisition, at := strategyDecisionFixture(t)
	acquired, err := proposalcheck.ReadAcquisition(acquisition, at, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	candidate := acquired.Candidate
	candidate.Policy.MaxFeeLamports = seed.Policy.FeeLamports + 1
	next.TransactionPolicy.Jupiter = &candidate.Policy
	next.TransactionPolicy.MaxFeeLamports = candidate.Policy.MaxFeeLamports
	next.TransactionPolicy.ProfileFingerprint, err = candidate.Policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Validate(); err != nil {
		t.Fatalf("fee regression requires independently valid protected policy: %v", err)
	}
	acquisition += ".fee"
	seedStrategyAcquisition(t, acquisition, candidate, acquired.ReceivedAt, time.Minute)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, at); err == nil || !strings.Contains(err.Error(), "cost bounds") {
		t.Fatalf("excess strategy fee budget: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("fee rejection mutated history")
	}
}
