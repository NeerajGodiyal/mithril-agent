package policyauthority

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

// This fixture uses real protected claim/reservation/terminal/accounting journals.
// Only finalized recovery effects are injected at the existing private reader
// seam; it does not claim to test signed recovery construction or chain finality.
func strategyOutcomeFixture(t *testing.T, failed bool) (string, strategySeed, Policy, submitter.Policy, string, time.Time, func(strategySeed, string, time.Time) (WalletStrategyOutcome, error)) {
	t.Helper()
	path, seed, authority, request, now := strategyJournalFixture(t)
	p := authority.TransactionPolicy
	recovery := submitter.Policy{Cluster: p.Cluster, Profile: p.Profile, ProfileFingerprint: p.ProfileFingerprint,
		ControlStatePath: filepath.Join(filepath.Dir(path), "control.json"), Source: p.Source,
		MaxLamports: p.MaxLamports, MaxFeeLamports: p.MaxFeeLamports, ScheduleWindowSeconds: p.ScheduleWindowSeconds,
		ScheduleAnchorUnix: p.ScheduleAnchorUnix, MaxBlockHeightWindow: p.MaxBlockHeightWindow,
		RecoveryMode: submitter.MainnetRecoveryStopOnly, SubmitterPublicKey: p.SubmitterPublicKey,
		AttestationPublicKey: p.AttestationPublicKey, Evidence: *authority.JupiterProviders, Jupiter: p.Jupiter}
	if err := submitter.ValidateJupiterPolicy(recovery); err != nil {
		t.Fatal(err)
	}
	if _, err := InitializeStrategyJournal(path, seed.WalletPath, seed.ClaimPath, authority, recovery, seed.Policy, seed.Ticks, seed.Bounds, now); err != nil {
		t.Fatal(err)
	}
	wallet, err := ReadWalletInventory(seed.WalletPath, authority)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := signer.ValidateJupiterRequest(p, request)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := signer.RiskBinding(request, validated.MessageSHA256)
	if err != nil {
		t.Fatal(err)
	}
	b := submitter.JupiterFinalizedWalletEvidence{
		Finalized: submitter.JupiterFinalizedEvidence{ActionID: request.ActionID, RequestSHA256: binding.RequestSHA256,
			TransactionSHA256: strings.Repeat("d", 64), Verdict: txflow.VerdictFinalized, FinalizedSlot: 150,
			PrimaryEffectSlot: 150, SecondaryEffectSlot: 150, InputMint: p.Jupiter.InputMint, OutputMint: p.Jupiter.OutputMint,
			InputSpent: request.JupiterCandidate.Request.InputAmount, OutputReceived: request.JupiterCandidate.Quote.EstimatedOutput, FeeLamports: 5000},
		Payer: txflow.JupiterPayerEvidence{Version: 1, PreLamports: wallet.NativeLamports},
		Token: txflow.JupiterTokenEvidence{Version: 1, Account: wallet.Opening.TokenAccount, Mint: wallet.Opening.TokenMint, Owner: wallet.Opening.Owner, PreUnits: wallet.TokenUnits}}
	if failed {
		b.Finalized.Verdict, b.Finalized.InputSpent, b.Finalized.OutputReceived = txflow.VerdictFailed, 0, 0
	}
	b.Payer.PostLamports = b.Payer.PreLamports - b.Finalized.InputSpent - b.Finalized.FeeLamports
	b.Token.PostUnits = b.Token.PreUnits + b.Finalized.OutputReceived
	if _, err := recordPaperTerminal(seed.ClaimPath, authority, request, now.Add(time.Second), func() (submitter.JupiterFinalizedEvidence, error) { return b.Finalized, nil }); err != nil {
		t.Fatal(err)
	}
	readBalances := func() (submitter.JupiterFinalizedWalletEvidence, error) { return b, nil }
	accounted, err := applyFinalizedWalletClaim(seed.WalletPath, seed.ClaimPath, authority, request, now.Add(2*time.Second), readBalances)
	if err != nil {
		t.Fatal(err)
	}
	read := func(s strategySeed, id string, at time.Time) (WalletStrategyOutcome, error) {
		return readWalletStrategyOutcome(s.WalletPath, s.ClaimPath, id, authority, request, at, readBalances)
	}
	return path, seed, authority, recovery, accounted.HeadSHA256, now, read
}

func strategyOutcomeSamples(seed strategySeed, at time.Time) [4]pricetrigger.Sample {
	return [4]pricetrigger.Sample{
		{SourceSHA256: seed.Policy.Trigger.PrimarySourceSHA256, Feed: seed.Policy.Trigger.Feed, PriceMicros: 1_900_000_000, PublishedAt: at},
		{SourceSHA256: seed.Policy.Trigger.SecondarySourceSHA256, Feed: seed.Policy.Trigger.Feed, PriceMicros: 1_900_000_000, PublishedAt: at},
		{SourceSHA256: seed.Policy.QuotePeg.PrimarySourceSHA256, Feed: seed.Policy.QuotePeg.Feed, PriceMicros: 1_000_000, PublishedAt: at},
		{SourceSHA256: seed.Policy.QuotePeg.SecondarySourceSHA256, Feed: seed.Policy.QuotePeg.Feed, PriceMicros: 1_000_000, PublishedAt: at},
	}
}

func TestStrategyJournalOutcomeDeliveryAndIdempotence(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "fee-only failure"}[failed], func(t *testing.T) {
			path, seed, authority, recovery, id, now, read := strategyOutcomeFixture(t, failed)
			observedAt := now.Add(time.Minute)
			x := strategyOutcomeSamples(seed, observedAt)
			if _, err := ObserveStrategyJournal(path, authority, recovery, observedAt, x[0], x[1], x[2], x[3]); err != nil {
				t.Fatal(err)
			}
			original, err := ReadStrategyJournal(path, authority, recovery, observedAt)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := read(seed, id, observedAt)
			if err != nil || !outcome.KnownAt.Before(observedAt) {
				t.Fatal("fixture lacks delayed accounting", err)
			}
			delivery := observedAt.Add(time.Second)
			state, err := applyStrategyJournalOutcome(path, authority, recovery, id, delivery, read)
			if err != nil {
				t.Fatal(err)
			}
			if err := original.ApplyOutcome(outcome.Amounts, delivery); err != nil {
				t.Fatal(err)
			}
			if state.Pending() || state.NextSell() != failed || !reflect.DeepEqual(state, original) {
				t.Fatal("durable outcome changed actual continuation")
			}
			records, err := journal.ReadRecords(path)
			if err != nil {
				t.Fatal(err)
			}
			last := records[len(records)-1]
			var retained strategyOutcome
			if err := decodeStrategyPayload(last.Payload, &retained); err != nil {
				t.Fatal(err)
			}
			if !last.At.Equal(delivery) || retained.Outcome != outcome {
				t.Fatal("source accounting knowledge or original delivery was renewed")
			}
			before := walletAdmissionBytes(t, path)
			if _, err := applyStrategyJournalOutcome(path, authority, recovery, id, delivery.Add(time.Minute), read); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, walletAdmissionBytes(t, path)) {
				t.Fatal("repeat renewed outcome receipt")
			}
			later := delivery.Add(30 * time.Second)
			x = strategyOutcomeSamples(seed, later)
			decision, err := observeStrategyJournal(path, authority, recovery, later, x[0], x[1], x[2], x[3], read)
			if err != nil {
				t.Fatal(err)
			}
			if !failed && (decision.ReadyForQuote || decision.Decision.Reason != "cooldown") {
				t.Fatalf("successful delivery cooldown lost: %+v", decision)
			}
			if failed && decision.Decision.Reason == "cooldown" {
				t.Fatal("failed transaction started cooldown")
			}
			before = walletAdmissionBytes(t, path)
			again, err := applyStrategyJournalOutcome(path, authority, recovery, id, later.Add(time.Second), read)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := original.Observe(later, x[0], x[1], x[2], x[3]); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(again, original) {
				t.Fatal("restart lost strategy state")
			}
			if !bytes.Equal(before, walletAdmissionBytes(t, path)) {
				t.Fatal("old duplicate after observation appended or refreshed")
			}
			if _, err := os.Stat(authority.TransactionPolicy.AuthorizationLedgerPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("outcome touched signer allowance")
			}
		})
	}
}

func TestStrategyJournalOutcomeRefusesUnverifiedOrConflictingEvidence(t *testing.T) {
	for _, mode := range []string{"missing policy", "missing recovery", "wrong claim", "wrong head", "future source", "regressed delivery", "torn", "locked"} {
		t.Run(mode, func(t *testing.T) {
			path, seed, authority, recovery, id, now, read := strategyOutcomeFixture(t, false)
			delivery := now.Add(time.Minute)
			reader := read
			switch mode {
			case "missing policy":
				recovery = submitter.Policy{}
			case "missing recovery":
				reader = func(strategySeed, string, time.Time) (WalletStrategyOutcome, error) {
					return WalletStrategyOutcome{}, errors.New("recovery unavailable")
				}
			case "wrong claim", "wrong head", "future source":
				reader = func(s strategySeed, id string, at time.Time) (WalletStrategyOutcome, error) {
					o, err := read(s, id, at)
					if err != nil {
						return o, err
					}
					switch mode {
					case "wrong claim":
						o.ClaimSHA256 = strings.Repeat("f", 64)
					case "wrong head":
						o.PreviousHeadSHA256 = strings.Repeat("f", 64)
					case "future source":
						o.KnownAt = at.Add(time.Second)
					}
					return o, nil
				}
			case "regressed delivery":
				delivery = now.Add(-time.Second)
			case "torn":
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteString("torn"); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			case "locked":
				store, err := journal.OpenStrict(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := store.Close(); err != nil {
						t.Fatal(err)
					}
				})
			}
			before, walletBefore, claimBefore := walletAdmissionBytes(t, path), walletAdmissionBytes(t, seed.WalletPath), walletAdmissionBytes(t, seed.ClaimPath)
			state, err := applyStrategyJournalOutcome(path, authority, recovery, id, delivery, reader)
			if err == nil || state != nil {
				t.Fatal("invalid outcome accepted", mode)
			}
			if mode == "locked" && !errors.Is(err, journal.ErrLocked) {
				t.Fatalf("lock error: %v", err)
			}
			if !bytes.Equal(before, walletAdmissionBytes(t, path)) || !bytes.Equal(walletBefore, walletAdmissionBytes(t, seed.WalletPath)) || !bytes.Equal(claimBefore, walletAdmissionBytes(t, seed.ClaimPath)) {
				t.Fatal("failure changed durable evidence")
			}
		})
	}
}

func TestStrategyJournalOutcomeReplayRequiresOriginalRecovery(t *testing.T) {
	path, _, authority, recovery, id, now, read := strategyOutcomeFixture(t, false)
	at := now.Add(time.Minute)
	if _, err := applyStrategyJournalOutcome(path, authority, recovery, id, at, read); err != nil {
		t.Fatal(err)
	}
	before := walletAdmissionBytes(t, path)
	changed := recovery
	changed.MaxFeeLamports--
	if err := submitter.ValidateJupiterPolicy(changed); err != nil {
		t.Fatal("changed policy fixture invalid", err)
	}
	if _, err := applyStrategyJournalOutcome(path, authority, changed, id, at.Add(time.Second), read); err == nil {
		t.Fatal("changed recovery policy reused outcome")
	}
	if _, err := applyStrategyJournalOutcome(path, authority, recovery, id, at.Add(time.Second), func(s strategySeed, id string, at time.Time) (WalletStrategyOutcome, error) {
		o, err := read(s, id, at)
		o.TransactionSHA256 = strings.Repeat("e", 64)
		return o, err
	}); err == nil {
		t.Fatal("changed recovery evidence reused outcome")
	}
	// Public readers always require real finalized recovery: no public callback
	// or copied strategy payload can substitute for the absent signed artifact.
	if _, err := ReadStrategyJournal(path, authority, recovery, at.Add(time.Second)); err == nil {
		t.Fatal("public replay trusted stored amounts without recovery")
	}
	if _, err := ApplyStrategyJournalOutcome(path, authority, recovery, id, at.Add(time.Second)); err == nil {
		t.Fatal("public append trusted injected test evidence")
	}
	if !bytes.Equal(before, walletAdmissionBytes(t, path)) {
		t.Fatal("failed evidence recheck changed original outcome")
	}
}
