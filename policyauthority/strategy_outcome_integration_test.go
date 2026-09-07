package policyauthority

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

// TestStrategyOutcomePublicRecovery exercises the concrete retained-evidence
// reader. All keys, balances and finality observations are offline test data;
// neither a network provider nor a transaction broadcaster is used.
func TestStrategyOutcomePublicRecovery(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failed transaction"}[failed], func(t *testing.T) {
			walletSeed := sha256.Sum256([]byte("strategy outcome test wallet"))
			walletKey := ed25519.NewKeyFromSeed(walletSeed[:])
			attestorSeed := sha256.Sum256([]byte("strategy outcome test attestor"))
			attestorKey := ed25519.NewKeyFromSeed(attestorSeed[:])
			path, seed, authority, request, now := strategyJournalFixtureWithIdentity(t,
				solana.Encode(walletKey.Public().(ed25519.PublicKey)), solana.Encode(attestorKey.Public().(ed25519.PublicKey)))
			recovery, recoveryPath := strategyOutcomeRecoveryFixture(t, authority, request, walletKey, attestorKey, failed)
			state, err := InitializeStrategyJournal(path, seed.WalletPath, seed.ClaimPath, authority, recovery,
				seed.Policy, seed.Ticks, seed.Bounds, now)
			if err != nil {
				t.Fatal(err)
			}
			before := state.Ledger()
			accounted, err := FinalizeWalletClaim(seed.WalletPath, seed.ClaimPath, authority, request, recovery, now.Add(time.Minute))
			if err != nil {
				t.Fatalf("concrete wallet accounting: %v", err)
			}
			at := now.Add(2 * time.Minute)
			samples := [4]pricetrigger.Sample{
				{SourceSHA256: seed.Policy.Trigger.PrimarySourceSHA256, Feed: seed.Policy.Trigger.Feed, PriceMicros: 1_900_000_000, PublishedAt: at},
				{SourceSHA256: seed.Policy.Trigger.SecondarySourceSHA256, Feed: seed.Policy.Trigger.Feed, PriceMicros: 1_900_000_000, PublishedAt: at},
				{SourceSHA256: seed.Policy.QuotePeg.PrimarySourceSHA256, Feed: seed.Policy.QuotePeg.Feed, PriceMicros: 1_000_000, PublishedAt: at},
				{SourceSHA256: seed.Policy.QuotePeg.SecondarySourceSHA256, Feed: seed.Policy.QuotePeg.Feed, PriceMicros: 1_000_000, PublishedAt: at},
			}
			if _, err := ObserveStrategyJournal(path, authority, recovery, at, samples[0], samples[1], samples[2], samples[3]); err != nil {
				t.Fatal(err)
			}
			delivered := at.Add(time.Minute)
			result, err := ApplyStrategyJournalOutcome(path, authority, recovery, accounted.HeadSHA256, delivered)
			if err != nil {
				t.Fatalf("concrete strategy delivery: %v", err)
			}
			if result.Pending() || result.Ledger().BaseUnits != accounted.NativeLamports || result.Ledger().QuoteUnits != accounted.TokenUnits ||
				result.Ledger().OpeningEquityMicros != before.OpeningEquityMicros || result.NextSell() != failed {
				t.Fatal("outcome did not preserve original books and advance only actual fills")
			}
			if failed && result.Ledger().Fills != 0 || !failed && result.Ledger().Fills != 1 {
				t.Fatal("actual fill count differs from transaction result")
			}
			records, err := journal.ReadRecords(path)
			if err != nil {
				t.Fatal(err)
			}
			var receipt strategyOutcome
			last := records[len(records)-1]
			if err := json.Unmarshal(last.Payload, &receipt); err != nil || last.Type != strategyOutcomeEvent ||
				!last.At.Equal(delivered) || !receipt.Outcome.KnownAt.Before(at) {
				t.Fatalf("source accounting time was confused with delivery: %v", err)
			}
			readAt := delivered.Add(time.Minute)
			if !failed {
				ready := false
				for i, price := range []uint64{2_000_000_000, 2_100_000_000, 2_200_000_000, 2_300_000_000} {
					readAt = delivered.Add(time.Duration(i+1) * time.Minute)
					for index := range samples {
						samples[index].PublishedAt = readAt
					}
					samples[0].PriceMicros, samples[1].PriceMicros = price, price
					decision, err := ObserveStrategyJournal(path, authority, recovery, readAt, samples[0], samples[1], samples[2], samples[3])
					if err != nil {
						t.Fatal(err)
					}
					if decision.ReadyForQuote {
						if decision.Sell || decision.InputAmount != receipt.Outcome.Amounts.ReceivedUnits {
							t.Fatal("next quote opportunity did not use actual received proceeds")
						}
						ready = true
						break
					}
				}
				if !ready {
					t.Fatal("post-outcome market history did not produce the reverse quote opportunity")
				}
				result, err = ReadStrategyJournal(path, authority, recovery, readAt)
				if err != nil {
					t.Fatal(err)
				}
			}
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// The concrete reader must also work from the action-specific archive.
			archive := filepath.Join(filepath.Dir(recoveryPath), "submission-recovery."+request.ActionID+".finalized.json")
			if err := os.Rename(recoveryPath, archive); err != nil {
				t.Fatal(err)
			}
			restored, err := ReadStrategyJournal(path, authority, recovery, readAt)
			if err != nil || !reflect.DeepEqual(result, restored) {
				t.Fatalf("archive-backed restart changed strategy: %v", err)
			}
			if _, err := ApplyStrategyJournalOutcome(path, authority, recovery, accounted.HeadSHA256, readAt.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(original, after) {
				t.Fatal("exact retry rewrote strategy history")
			}
			if err := os.Rename(archive, archive+".saved"); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadStrategyJournal(path, authority, recovery, readAt.Add(2*time.Minute)); err == nil {
				t.Fatal("restart trusted copied outcome without original recovery evidence")
			}
		})
	}
}

func strategyOutcomeRecoveryFixture(t *testing.T, authority Policy, request signer.Request,
	walletKey, attestorKey ed25519.PrivateKey, failed bool,
) (submitter.Policy, string) {
	t.Helper()
	p := authority.TransactionPolicy
	dir := t.TempDir()
	recovery := submitter.Policy{Cluster: p.Cluster, Profile: p.Profile, ProfileFingerprint: p.ProfileFingerprint,
		ControlStatePath: filepath.Join(dir, "control.json"), Source: p.Source, MaxLamports: p.MaxLamports,
		MaxFeeLamports: p.MaxFeeLamports, ScheduleWindowSeconds: p.ScheduleWindowSeconds, ScheduleAnchorUnix: p.ScheduleAnchorUnix,
		MaxBlockHeightWindow: p.MaxBlockHeightWindow, RecoveryMode: submitter.MainnetRecoveryStopOnly,
		SubmitterPublicKey: p.SubmitterPublicKey, AttestationPublicKey: p.AttestationPublicKey, Evidence: *authority.JupiterProviders, Jupiter: p.Jupiter}
	if err := submitter.ValidateJupiterPolicy(recovery); err != nil {
		t.Fatal(err)
	}
	message, err := base64.StdEncoding.DecodeString(request.MessageBase64)
	if err != nil {
		t.Fatal(err)
	}
	transaction, signature, err := solana.SignV0Message(walletKey, message, nil)
	if err != nil {
		t.Fatal(err)
	}
	messageHash, transactionHash := sha256.Sum256(message), sha256.Sum256(transaction)
	binding, err := signer.RiskBinding(request, hex.EncodeToString(messageHash[:]))
	if err != nil {
		t.Fatal(err)
	}
	response := signer.Response{ActionID: request.ActionID, RequestSHA256: binding.RequestSHA256,
		MessageSHA256: hex.EncodeToString(messageHash[:]), TransactionSHA256: hex.EncodeToString(transactionHash[:]),
		BlockhashContextSlot: request.BlockhashContextSlot, FeeLamports: request.FeeLamports, LastValidBlockHeight: request.LastValidBlockHeight}
	response.SealedTransaction.Metadata = sealedtx.Metadata{Version: sealedtx.Version, Domain: sealedtx.Domain,
		ActionID: response.ActionID, MessageSHA256: response.MessageSHA256, TransactionSHA256: response.TransactionSHA256,
		BlockhashContextSlot: response.BlockhashContextSlot, FeeLamports: response.FeeLamports, LastValidBlockHeight: response.LastValidBlockHeight}
	response.SignerAttestation, err = signer.AttestResponse(attestorKey, recovery.SubmitterPublicKey, response)
	if err != nil {
		t.Fatal(err)
	}
	quote := request.JupiterCandidate.Quote
	effects := &txflow.JupiterEffectEvidence{TransactionSHA256: response.TransactionSHA256, FeeLamports: request.FeeLamports,
		InputAmount: quote.InputAmount, MinimumOutput: quote.MinimumOutput, OutputAmount: quote.EstimatedOutput,
		PrimaryEffectSlot: 150, SecondaryEffectSlot: 150,
		Payer: &txflow.JupiterPayerEvidence{Version: 1, PreLamports: 20_000_000, PostLamports: 20_000_000 - quote.InputAmount - request.FeeLamports},
		Token: &txflow.JupiterTokenEvidence{Version: 1, Account: request.JupiterCandidate.Request.DestinationTokenAccount,
			Mint: p.Jupiter.OutputMint, Owner: p.Source, PreUnits: 100, PostUnits: 100 + quote.EstimatedOutput}}
	result := txflow.Reconciliation{Signature: solana.Encode(signature[:]), Verdict: txflow.VerdictFinalized, Slot: 150,
		PrimaryFound: true, SecondaryFound: true, PrimarySlot: 150, SecondarySlot: 150,
		PrimaryStatus: "finalized", SecondaryStatus: "finalized", JupiterEffects: effects}
	if failed {
		result.Verdict, result.PrimaryFailed, result.SecondaryFailed = txflow.VerdictFailed, true, true
		result.PrimaryErrorFingerprint, result.SecondaryErrorFingerprint = strings.Repeat("a", 64), strings.Repeat("a", 64)
		effects.OutputAmount = 0
		effects.Payer.PostLamports = effects.Payer.PreLamports - request.FeeLamports
		effects.Token.PostUnits = effects.Token.PreUnits
	}
	// Seed the documented durable recovery format, then require the public reader
	// to verify its transaction, attestation, exact request and balance equations.
	record := map[string]any{"version": 6, "action_id": request.ActionID, "profile_sha256": p.ProfileFingerprint,
		"transaction_base64": base64.StdEncoding.EncodeToString(transaction), "fee_lamports": request.FeeLamports,
		"request_sha256": binding.RequestSHA256, "blockhash_context_slot": request.BlockhashContextSlot,
		"signer_attestation": response.SignerAttestation, "recovery_mode": recovery.RecoveryMode,
		"submission":      txflow.Submission{State: txflow.StateAmbiguous, Signature: result.Signature, LastValidBlockHeight: request.LastValidBlockHeight},
		"jupiter_request": request, "send_started": true, "send_attempts": 1, "finalized": true, "reconciliation": result}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "submission-recovery.json")
	if err := securefile.ReplacePrivate(path, raw, 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := submitter.ReadJupiterFinalizedWalletEvidence(recovery, request); err != nil {
		t.Fatalf("concrete recovery fixture: %v", err)
	}
	return recovery, path
}
