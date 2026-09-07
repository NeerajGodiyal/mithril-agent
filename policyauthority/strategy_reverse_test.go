package policyauthority

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

// This is a separately bounded reverse policy, not a reset of the original
// authorization ledger. The canonical route unwraps SOL and leaves the existing
// protected USDC input account open.
func strategyReverseCandidate(t *testing.T, original Policy, input uint64) (Policy, proposalcheck.Candidate) {
	t.Helper()
	next := original
	route := *original.TransactionPolicy.Jupiter
	route.InputMint, route.OutputMint = route.OutputMint, route.InputMint
	route.MaxInputAmount, route.MinOutputAmount = input, 6_268_500
	next.TransactionPolicy.Jupiter = &route
	next.TransactionPolicy.MaxLamports, next.TransactionPolicy.DailyDebitCapLamports = 0, 0
	next.TransactionPolicy.MaxInputTokenAmount, next.TransactionPolicy.DailyInputTokenCap = input, input
	next.TransactionPolicy.DailyNativeFeeCapLamports = original.TransactionPolicy.MaxFeeLamports + route.MaxTokenAccountRentLamports
	var err error
	next.TransactionPolicy.ProfileFingerprint, err = route.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	owner := route.Owner
	inputATA, err := orcaswap.AssociatedTokenAddress(owner, route.InputMint)
	if err != nil {
		t.Fatal(err)
	}
	outputATA, err := orcaswap.AssociatedTokenAddress(owner, route.OutputMint)
	if err != nil {
		t.Fatal(err)
	}
	request := jupiterquote.Request{Taker: owner, InputMint: route.InputMint, OutputMint: route.OutputMint, InputAmount: input, SlippageBPS: 50}
	quote := jupiterquote.Result{InputAmount: input, EstimatedOutput: 6_300_000, MinimumOutput: route.MinOutputAmount}
	data := []byte{187, 100, 250, 204, 49, 196, 175, 20}
	data = binary.LittleEndian.AppendUint64(data, input)
	data = binary.LittleEndian.AppendUint64(data, quote.EstimatedOutput)
	data = binary.LittleEndian.AppendUint16(data, request.SlippageBPS)
	data = append(data, 0, 0, 0, 0, 1, 0, 0, 0, 17, 1, 0x10, 0x27, 0, 1)
	limit, err := solana.SetComputeUnitLimitInstruction(route.MaxComputeUnits)
	if err != nil {
		t.Fatal(err)
	}
	blockhash := sha256.Sum256([]byte("third actual reverse strategy transaction"))
	message, err := jupiterswap.BuildGuardedPolicyV0Message(route, owner, solana.Encode(blockhash[:]), []solana.Instruction{
		limit, {Program: solana.ComputeBudgetProgram, Data: binary.LittleEndian.AppendUint64([]byte{3}, 1)},
		{Program: orcaswap.AssociatedTokenProgram, Data: []byte{1}, Accounts: []solana.AccountMeta{
			{Address: owner, Signer: true, Writable: true}, {Address: outputATA, Writable: true}, {Address: owner},
			{Address: route.OutputMint}, {Address: orcaswap.SystemProgram}, {Address: orcaswap.TokenProgram}}},
		{Program: jupiterswap.Program, Data: data, Accounts: []solana.AccountMeta{
			{Address: owner, Signer: true}, {Address: inputATA, Writable: true}, {Address: outputATA, Writable: true},
			{Address: route.InputMint}, {Address: route.OutputMint}, {Address: orcaswap.TokenProgram}, {Address: orcaswap.TokenProgram},
			{Address: jupiterswap.Program}, {Address: "D8cy77BBepLMngZx6ZukaTff5hCt1HrWyKk3Hnd9oitf"}, {Address: jupiterswap.Program},
			{Address: solana.Encode(bytes.Repeat([]byte{3}, 32)), Writable: true}}},
		{Program: orcaswap.TokenProgram, Data: []byte{9}, Accounts: []solana.AccountMeta{
			{Address: outputATA, Writable: true}, {Address: owner, Writable: true}, {Address: owner, Signer: true}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidate := proposalcheck.Candidate{Version: proposalcheck.CandidateVersion, Policy: route, Request: request, Quote: quote,
		MessageBase64: base64.StdEncoding.EncodeToString(message), LastValidBlockHeight: 200}
	if _, err := proposalcheck.EncodeCandidate(candidate); err != nil {
		t.Fatalf("canonical reverse candidate: %v", err)
	}
	return next, candidate
}

type strategyReverseEvidence struct {
	strategyClaimEvidence
	wallet WalletInventory
}

func (e strategyReverseEvidence) VerifyTokenInputAccount(_ context.Context, account, mint, owner string, minimum, slot uint64) (txflow.TokenAccountEvidence, error) {
	if account != e.wallet.Opening.TokenAccount || mint != e.wallet.Opening.TokenMint || owner != e.wallet.Opening.Owner ||
		minimum > e.wallet.TokenUnits || slot == 0 || slot > e.wallet.LastFinalizedSlot {
		return txflow.TokenAccountEvidence{}, fmt.Errorf("reverse fixture token account does not match actual inventory")
	}
	return txflow.TokenAccountEvidence{Amount: e.wallet.TokenUnits, PrimaryContextSlot: e.slot, SecondaryContextSlot: e.slot}, nil
}

// All observations and keys are offline fixtures. Unlike the native-input
// fixture, this records a token debit and native output net of the actual fee.
func strategyReverseRecovery(t *testing.T, authority Policy, claim WalletPaperClaim, wallet WalletInventory) submitter.Policy {
	t.Helper()
	request, p := claim.Request, authority.TransactionPolicy
	walletSeed := sha256.Sum256([]byte("strategy decision test wallet"))
	attestorSeed := sha256.Sum256([]byte("strategy decision test attestor"))
	dir := t.TempDir()
	recovery := submitter.Policy{Cluster: p.Cluster, Profile: p.Profile, ProfileFingerprint: p.ProfileFingerprint,
		ControlStatePath: filepath.Join(dir, "control.json"), Source: p.Source, MaxInputTokenAmount: p.MaxInputTokenAmount,
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
	transaction, signature, err := solana.SignV0Message(ed25519.NewKeyFromSeed(walletSeed[:]), message, nil)
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
	response.SignerAttestation, err = signer.AttestResponse(ed25519.NewKeyFromSeed(attestorSeed[:]), recovery.SubmitterPublicKey, response)
	if err != nil {
		t.Fatal(err)
	}
	quote, slot := request.JupiterCandidate.Quote, wallet.LastFinalizedSlot+10
	actualOutput := quote.EstimatedOutput + 77
	effects := &txflow.JupiterEffectEvidence{TransactionSHA256: response.TransactionSHA256, FeeLamports: request.FeeLamports,
		InputAmount: quote.InputAmount, MinimumOutput: quote.MinimumOutput, OutputAmount: actualOutput,
		PrimaryEffectSlot: slot, SecondaryEffectSlot: slot,
		Payer: &txflow.JupiterPayerEvidence{Version: 1, PreLamports: wallet.NativeLamports, PostLamports: wallet.NativeLamports - request.FeeLamports + actualOutput},
		Token: &txflow.JupiterTokenEvidence{Version: 1, Account: wallet.Opening.TokenAccount, Mint: p.Jupiter.InputMint,
			Owner: p.Source, PreUnits: wallet.TokenUnits, PostUnits: wallet.TokenUnits - quote.InputAmount}}
	result := txflow.Reconciliation{Signature: solana.Encode(signature[:]), Verdict: txflow.VerdictFinalized, Slot: slot,
		PrimaryFound: true, SecondaryFound: true, PrimarySlot: slot, SecondarySlot: slot,
		PrimaryStatus: "finalized", SecondaryStatus: "finalized", JupiterEffects: effects}
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
	if err := securefile.ReplacePrivate(filepath.Join(dir, "submission-recovery.json"), raw, 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := submitter.ReadJupiterFinalizedWalletEvidence(recovery, request); err != nil {
		t.Fatalf("concrete reverse recovery: %v", err)
	}
	return recovery
}
