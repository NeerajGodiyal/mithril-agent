package execution

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func TestPaperTerminalComposition(t *testing.T) {
	policy, request := paperTerminalRequest(t)
	now := time.Unix(request.ScheduleWindowEndUnix, 0).UTC().Add(time.Hour)
	path := filepath.Join(t.TempDir(), "claim.jsonl")
	// Seed canonical host evidence, not ClaimPaperRequest's replay/acquisition
	// workflow (covered in policyauthority). Public validation must accept it.
	const event = "paper.unsigned-request-claim-v1"
	hash := func(domain string, value any) string {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(append([]byte(event+"/"+domain+"\x00"), data...))
		return hex.EncodeToString(digest[:])
	}
	claim := struct {
		PaperIntentSHA256 string `json:"paper_intent_sha256"`
		PolicySHA256      string `json:"policy_sha256"`
		RequestSHA256     string `json:"request_sha256"`
		MaxDecisionAgeNS  int64  `json:"max_decision_age_ns,string"`
		AcquisitionSHA256 string `json:"acquisition_sha256"`
	}{strings.Repeat("a", 64), hash("policy", policy), hash("request", request), int64(time.Minute), strings.Repeat("b", 64)}
	store, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Append(time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC(), event, request.ActionID, claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := policyauthority.ValidatePaperRequestClaim(record, policy, request); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	recoveryErr := errors.New("terminal recovery unavailable")
	got, err := recordPaperTerminal(path, policy, request, now, func() (submitter.JupiterFinalizedEvidence, error) {
		return submitter.JupiterFinalizedEvidence{ActionID: request.ActionID}, recoveryErr
	})
	if !errors.Is(err, recoveryErr) || got != (submitter.JupiterFinalizedEvidence{}) {
		t.Fatalf("recovery failure: %+v, %v", got, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed recovery changed claim: %v", err)
	}
	validated, err := signer.ValidateJupiterRequest(policy.TransactionPolicy, request)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := signer.RiskBinding(request, validated.MessageSHA256)
	if err != nil {
		t.Fatal(err)
	}
	evidence := submitter.JupiterFinalizedEvidence{ActionID: request.ActionID, RequestSHA256: binding.RequestSHA256,
		TransactionSHA256: strings.Repeat("d", 64), Verdict: txflow.VerdictFinalized, FinalizedSlot: 150,
		PrimaryEffectSlot: 150, SecondaryEffectSlot: 150, InputMint: policy.TransactionPolicy.Jupiter.InputMint,
		OutputMint: policy.TransactionPolicy.Jupiter.OutputMint, InputSpent: 10, OutputReceived: 20, FeeLamports: 5000}
	// The concrete reader's exact recovery binding is tested in submitter.
	// Here verify it runs while the real claim journal is exclusively locked.
	reads := 0
	read := func() (submitter.JupiterFinalizedEvidence, error) {
		reads++
		other, err := journal.Open(path)
		if other != nil {
			if err := other.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if !errors.Is(err, journal.ErrLocked) {
			t.Fatalf("reader ran without claim lock: %v", err)
		}
		return evidence, nil
	}
	for i := range 2 {
		got, err := recordPaperTerminal(path, policy, request, now.Add(time.Duration(i)*time.Hour), read)
		if err != nil || got != evidence {
			t.Fatalf("terminal attempt %d: %+v, %v", i, got, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			before = data
		} else if !bytes.Equal(before, data) {
			t.Fatal("restart repeat changed terminal bytes")
		}
	}
	records, err := journal.ReadRecords(path)
	if err != nil || len(records) != 2 || reads != 2 {
		t.Fatalf("terminal records=%d reads=%d: %v", len(records), reads, err)
	}
	var terminal paperTerminal
	if err := json.Unmarshal(records[1].Payload, &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.ClaimSHA256 != record.Hash || terminal.Finalized != evidence {
		t.Fatalf("terminal binding changed: %+v", terminal)
	}
}

// paperTerminalRequest uses the same minimal guarded SOL route as signer tests;
// all identities and amounts are synthetic and no signatures are produced.
func paperTerminalRequest(t *testing.T) (policyauthority.Policy, signer.Request) {
	t.Helper()
	key := func(b byte) string { return solana.Encode(bytes.Repeat([]byte{b}, 32)) }
	owner, outputMint, blockhash := key(1), key(2), key(9)
	input, err := orcaswap.AssociatedTokenAddress(owner, orcaswap.WrappedSOLMint)
	if err != nil {
		t.Fatal(err)
	}
	output, err := orcaswap.AssociatedTokenAddress(owner, outputMint)
	if err != nil {
		t.Fatal(err)
	}
	quoteRequest := jupiterquote.Request{Taker: owner, InputMint: orcaswap.WrappedSOLMint, OutputMint: outputMint, DestinationTokenAccount: output, InputAmount: 10, SlippageBPS: 50}
	quote := jupiterquote.Result{InputAmount: 10, EstimatedOutput: 20, MinimumOutput: 20}
	data := []byte{187, 100, 250, 204, 49, 196, 175, 20}
	data = binary.LittleEndian.AppendUint64(data, 10)
	data = binary.LittleEndian.AppendUint64(data, 20)
	data = binary.LittleEndian.AppendUint16(data, 50)
	data = append(data, 0, 0, 0, 0, 1, 0, 0, 0, 17, 1, 0x10, 0x27, 0, 1)
	transfer := binary.LittleEndian.AppendUint32(nil, 2)
	transfer = binary.LittleEndian.AppendUint64(transfer, 10)
	limit, err := solana.SetComputeUnitLimitInstruction(100_000)
	if err != nil {
		t.Fatal(err)
	}
	price := binary.LittleEndian.AppendUint64([]byte{3}, 1)
	route := jupiterswap.Policy{Owner: owner, InputMint: quoteRequest.InputMint, OutputMint: outputMint,
		MaxInputAmount: 10, MinOutputAmount: 19, MaxSlippageBPS: 50, MaxComputeUnits: 100_000,
		MaxComputeUnitPriceMicroLamport: 1, MaxFeeLamports: 10_000, MaxTokenAccountRentLamports: 3_000_000,
		RouteGuard: jupiterswap.RouteGuardDeployment{Program: key(71), ProgramData: key(72), DeploymentSlot: 123, CodeLength: 1, CodeSHA256: strings.Repeat("a", 64)}}
	message, err := jupiterswap.BuildGuardedPolicyV0Message(route, owner, blockhash, []solana.Instruction{
		limit, {Program: solana.ComputeBudgetProgram, Data: price},
		{Program: orcaswap.AssociatedTokenProgram, Data: []byte{1}, Accounts: []solana.AccountMeta{
			{Address: owner, Signer: true, Writable: true}, {Address: input, Writable: true}, {Address: owner},
			{Address: quoteRequest.InputMint}, {Address: orcaswap.SystemProgram}, {Address: orcaswap.TokenProgram}}},
		{Program: orcaswap.SystemProgram, Data: transfer, Accounts: []solana.AccountMeta{{Address: owner, Signer: true, Writable: true}, {Address: input, Writable: true}}},
		{Program: orcaswap.TokenProgram, Data: []byte{17}, Accounts: []solana.AccountMeta{{Address: input, Writable: true}}},
		{Program: jupiterswap.Program, Data: data, Accounts: []solana.AccountMeta{
			{Address: owner, Signer: true}, {Address: input, Writable: true}, {Address: output, Writable: true},
			{Address: quoteRequest.InputMint}, {Address: outputMint}, {Address: orcaswap.TokenProgram}, {Address: orcaswap.TokenProgram},
			{Address: output, Writable: true}, {Address: "D8cy77BBepLMngZx6ZukaTff5hCt1HrWyKk3Hnd9oitf"}, {Address: jupiterswap.Program}, {Address: key(3), Writable: true}}},
		{Program: orcaswap.TokenProgram, Data: []byte{9}, Accounts: []solana.AccountMeta{{Address: input, Writable: true}, {Address: owner, Writable: true}, {Address: owner, Signer: true}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := route.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	anchor := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC).Unix()
	action, err := jupiterswap.ComputeActionID(fingerprint, anchor+3600)
	if err != nil {
		t.Fatal(err)
	}
	providers := &proposalcheck.ProviderBindings{PrimaryTrustDomain: "primary", PrimaryOriginSHA256: strings.Repeat("1", 64),
		SecondaryTrustDomain: "secondary", SecondaryOriginSHA256: strings.Repeat("2", 64), ArchiveProbeSignature: solana.Encode(bytes.Repeat([]byte{7}, 64))}
	policy := policyauthority.Policy{TransactionPolicy: signer.Policy{
		Cluster: "mainnet-beta", Profile: jupiterswap.ProfileName, ProfileVersion: jupiterswap.ProfileVersion, ProfileFingerprint: fingerprint,
		Source: owner, MaxLamports: 10, MaxFeeLamports: 10_000, DailyDebitCapLamports: 3_010_010,
		AuthorizationLedgerPath: filepath.Join(t.TempDir(), "authorization.jsonl"), ScheduleWindowSeconds: 3600, ScheduleAnchorUnix: anchor,
		MaxBlockHeightWindow: 150, RiskAuthorityKeyID: "test", RiskAuthorityPublicKey: strings.Repeat("04", 32),
		SubmitterPublicKey: strings.Repeat("05", 32), AttestationPublicKey: key(6), Jupiter: &route},
		JupiterProviders: providers, OperatorApprover: key(8), GrantLifetimeSecs: 30}
	candidate := &proposalcheck.Candidate{Version: proposalcheck.CandidateVersion, Policy: route, Request: quoteRequest, Quote: quote,
		MessageBase64: base64.StdEncoding.EncodeToString(message), LastValidBlockHeight: 200}
	request := signer.Request{Domain: jupiterswap.RequestDomain, Cluster: "mainnet-beta", Profile: jupiterswap.ProfileName,
		ProfileVersion: jupiterswap.ProfileVersion, ProfileFingerprint: fingerprint, ActionID: action,
		ScheduleWindowStartUnix: anchor + 3600, ScheduleWindowEndUnix: anchor + 7200, MessageBase64: candidate.MessageBase64,
		BlockhashContextSlot: 90, FeeLamports: 5000, FeeMinContextSlot: 90, PrimaryFeeContextSlot: 90, SecondaryFeeContextSlot: 91,
		RecentBlockhash: blockhash, ObservedBlockHeight: 100, LastValidBlockHeight: 200, JupiterCandidate: candidate, JupiterProviders: providers}
	return policy, request
}

func TestPaperTerminalRefusesBeforeRecoveryRead(t *testing.T) {
	for _, name := range []string{"missing", "foreign", "excess records"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "claim.jsonl")
			if name != "missing" {
				store, err := journal.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				count := 1
				if name == "excess records" {
					count = 3
				}
				for range count {
					if _, err := store.Append(time.Now(), "foreign", "", struct{}{}); err != nil {
						t.Fatal(err)
					}
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			before, readErr := os.ReadFile(path)
			if readErr != nil && !(name == "missing" && errors.Is(readErr, os.ErrNotExist)) {
				t.Fatal(readErr)
			}
			called := false
			got, err := recordPaperTerminal(path, policyauthority.Policy{}, signer.Request{}, time.Now(), func() (submitter.JupiterFinalizedEvidence, error) {
				called = true
				return submitter.JupiterFinalizedEvidence{}, errors.New("must not read recovery")
			})
			if err == nil || called || got != (submitter.JupiterFinalizedEvidence{}) {
				t.Fatalf("invalid claim reached recovery: called=%v got=%+v err=%v", called, got, err)
			}
			after, readErr := os.ReadFile(path)
			if !bytes.Equal(before, after) || (name == "missing" && !errors.Is(readErr, os.ErrNotExist)) {
				t.Fatalf("invalid/missing journal was changed: %v", readErr)
			}
		})
	}
}

func TestPaperTerminalAppendIsDurableIdempotentAndPermanent(t *testing.T) {
	for _, verdict := range []string{txflow.VerdictFinalized, txflow.VerdictFailed} {
		t.Run(verdict, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "claim.jsonl")
			store, err := journal.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			action := strings.Repeat("a", 64)
			// This helper test starts after authority validation. The authority
			// package tests the real private claim schema; do not duplicate it.
			claim, err := store.Append(now, "already-validated-test-claim", action, struct{}{})
			if err != nil {
				t.Fatal(err)
			}
			evidence := submitter.JupiterFinalizedEvidence{ActionID: action, RequestSHA256: strings.Repeat("b", 64),
				TransactionSHA256: strings.Repeat("c", 64), Verdict: verdict, FinalizedSlot: 150,
				PrimaryEffectSlot: 150, SecondaryEffectSlot: 150, FeeLamports: 5000}
			if verdict == txflow.VerdictFinalized {
				evidence.InputSpent, evidence.OutputReceived = 10, 20
			}
			if err := appendPaperTerminal(store, claim.Hash, action, evidence, now); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			store, err = journal.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if concurrent, err := journal.Open(path); !errors.Is(err, journal.ErrLocked) {
				if concurrent != nil {
					if closeErr := concurrent.Close(); closeErr != nil {
						t.Fatal(closeErr)
					}
				}
				t.Fatalf("concurrent terminal writer was not blocked: %v", err)
			}
			if err := appendPaperTerminal(store, claim.Hash, action, evidence, now.Add(time.Hour)); err != nil {
				t.Fatalf("exact restart repeat rejected: %v", err)
			}
			changed := evidence
			changed.FeeLamports++
			if err := appendPaperTerminal(store, claim.Hash, action, changed, now); err == nil {
				t.Fatal("changed finalized evidence replaced terminal marker")
			}
			if err := appendPaperTerminal(store, strings.Repeat("d", 64), action, evidence, now); err == nil {
				t.Fatal("terminal marker rebound to another claim")
			}
			if len(store.Records()) != 2 {
				t.Fatal("terminal append reset or expanded first-action journal")
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("repeat/conflict changed terminal bytes: %v", err)
			}
		})
	}
}

func TestPaperTerminalAppendFailure(t *testing.T) {
	store, err := journal.Open(filepath.Join(t.TempDir(), "claim.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(time.Now(), "already-validated-test-claim", "action", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := appendPaperTerminal(store, "claim", "action", submitter.JupiterFinalizedEvidence{}, time.Now()); err == nil {
		t.Fatal("failed terminal append succeeded")
	}
	if len(store.Records()) != 1 {
		t.Fatal("failed append changed journal records")
	}
}
