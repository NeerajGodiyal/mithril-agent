package swaprun

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/clockcheck"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func TestValidateRecoveredSwapAcceptsExactState(t *testing.T) {
	now := time.Unix(testProfile().ScheduleAnchorUnix+3_601, 0).UTC()
	profile, actionID, current := recoveredSwapFixture(t, now)
	if err := validateRecoveredSwap(actionID, profile, current, now); err != nil {
		t.Fatal(err)
	}
	current.submission = &txflow.Submission{
		Signature:            current.signed.Response.Signature,
		LastValidBlockHeight: current.built.LastValidBlockHeight,
		State:                txflow.StateAccepted,
	}
	if err := validateRecoveredSwap(actionID, profile, current, now); err != nil {
		t.Fatal(err)
	}
}

func TestJournaledV1SignedSwapStopsBeforeSendBarrier(t *testing.T) {
	now := time.Unix(testProfile().ScheduleAnchorUnix+3_601, 0).UTC()
	profile, actionID, current := recoveredSwapFixture(t, now)
	current.signed.Response.BlockhashContextSlot = 0
	current.signed.Response.SealedTransaction.Metadata.BlockhashContextSlot = 0
	store := testStore(t)
	for _, record := range []struct {
		typeName string
		payload  any
	}{
		{EventStarted, *current.started},
		{EventBuilt, *current.built},
		{EventSimulated, *current.simulation},
		{EventSigned, *current.signed},
	} {
		if _, err := store.Append(now.Add(-time.Second), record.typeName, actionID, record.payload); err != nil {
			t.Fatal(err)
		}
	}
	stop := &stopStub{}
	submitter := &submitterStub{barrier: stop}
	engine := &Engine{
		store: store, stop: stop, submitter: submitter, tx: &transactorStub{},
		now:   func() time.Time { return now },
		clock: func() (clockcheck.Sample, error) { return validClockSample(now), nil },
	}
	if _, err := engine.RunOnce(t.Context(), profile); err == nil {
		t.Fatal("journaled v1 signer response was accepted")
	}
	if stop.barriers != 0 || submitter.calls != 0 {
		t.Fatalf("v1 response reached send boundary: barriers=%d submit=%d", stop.barriers, submitter.calls)
	}
	for _, record := range store.Records() {
		if record.Type == EventSendStarted {
			t.Fatal("v1 response wrote a send marker")
		}
	}
}

func TestRecoveredSwapRejectsBrokenBindingsBeforeRPC(t *testing.T) {
	testPolicy := testProfile()
	now := time.Unix(testPolicy.ScheduleAnchorUnix+3_601, 0).UTC()
	tests := map[string]func(*state){
		"zero observation context": func(current *state) {
			current.started.ObservationSlot = 0
		},
		"zero observed block height": func(current *state) {
			current.built.ObservedBlockHeight = 0
		},
		"blockhash before observation": func(current *state) {
			current.built.BlockhashContextSlot = current.started.ObservationSlot - 1
			current.built.FeeMinContextSlot = current.built.BlockhashContextSlot
			current.simulation.MinContextSlot = current.built.BlockhashContextSlot
		},
		"message": func(current *state) {
			current.built.MessageBase64 = base64.StdEncoding.EncodeToString([]byte("wrong message"))
		},
		"recent blockhash": func(current *state) {
			current.built.RecentBlockhash = solana.Encode(bytes.Repeat([]byte{8}, 32))
		},
		"fee": func(current *state) {
			current.built.FeeLamports++
		},
		"fee cap": func(current *state) {
			fee := testPolicy.MaxFeeLamports + 1
			current.built.FeeLamports = fee
			current.signed.Response.FeeLamports = fee
			current.signed.Response.SealedTransaction.Metadata.FeeLamports = fee
		},
		"block height": func(current *state) {
			current.built.LastValidBlockHeight++
		},
		"block height window": func(current *state) {
			height := current.built.ObservedBlockHeight + testPolicy.MaxBlockHeightWindow + 1
			current.built.LastValidBlockHeight = height
			current.signed.Response.LastValidBlockHeight = height
			current.signed.Response.SealedTransaction.Metadata.LastValidBlockHeight = height
		},
		"simulation": func(current *state) {
			current.simulation.MinContextSlot++
		},
		"pre-send observation": func(current *state) {
			current.preSendObservation.Account.Source = current.built.RecentBlockhash
		},
		"future send marker": func(current *state) {
			future := now.Add(time.Second)
			current.sendStartedAt = future
			current.preSendObservation.Account.ObservedAt = future
			current.preSendObservation.Health.ObservedAt = future
		},
		"response action": func(current *state) {
			current.signed.Response.ActionID = strings.Repeat("a", 64)
		},
		"request hash": func(current *state) {
			current.signed.Response.RequestSHA256 = strings.Repeat("a", 64)
		},
		"response blockhash context": func(current *state) {
			current.signed.Response.BlockhashContextSlot++
		},
		"sealed blockhash context": func(current *state) {
			current.signed.Response.SealedTransaction.Metadata.BlockhashContextSlot++
		},
		"message hash": func(current *state) {
			current.signed.Response.MessageSHA256 = strings.Repeat("a", 64)
		},
		"transaction hash": func(current *state) {
			current.signed.Response.TransactionSHA256 = strings.Repeat("a", 64)
		},
		"signature": func(current *state) {
			current.signed.Response.Signature = solana.Encode(make([]byte, ed25519.SignatureSize))
		},
		"sealed metadata": func(current *state) {
			current.signed.Response.SealedTransaction.Metadata.FeeLamports++
		},
		"sealed payload": func(current *state) {
			current.signed.Response.SealedTransaction.CiphertextBase64 = "invalid"
		},
		"send marker signature": func(current *state) {
			current.sendStarted.Signature = solana.Encode(make([]byte, ed25519.SignatureSize))
		},
		"send marker hash": func(current *state) {
			current.sendStarted.TransactionSHA256 = strings.Repeat("a", 64)
		},
		"submission signature": func(current *state) {
			current.submission = &txflow.Submission{
				Signature:            strings.Repeat("a", 64),
				LastValidBlockHeight: current.built.LastValidBlockHeight,
				State:                txflow.StateAccepted,
			}
		},
		"submission block height": func(current *state) {
			current.submission = &txflow.Submission{
				Signature:            current.signed.Response.Signature,
				LastValidBlockHeight: current.built.LastValidBlockHeight + 1,
				State:                txflow.StateAccepted,
			}
		},
		"submission state": func(current *state) {
			current.submission = &txflow.Submission{
				Signature:            current.signed.Response.Signature,
				LastValidBlockHeight: current.built.LastValidBlockHeight,
				State:                "unknown",
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			profile, actionID, current := recoveredSwapFixture(t, now)
			mutate(current)
			store := testStore(t)
			appendRecoveredSwap(t, store, actionID, current)
			transactor := &transactorStub{}
			submitter := &submitterStub{}
			engine := &Engine{
				store: store, tx: transactor, submitter: submitter,
				stop: &stopStub{}, now: func() time.Time { return now },
			}
			if _, err := engine.RunOnce(context.Background(), profile); err == nil {
				t.Fatal("invalid recovered swap was accepted")
			}
			if transactor.verifyEvidenceGenesisCalls != 0 ||
				transactor.reconciliationCalls != 0 || submitter.calls != 0 {
				t.Fatalf(
					"invalid recovery reached RPC or submitter: genesis=%d reconcile=%d submit=%d",
					transactor.verifyEvidenceGenesisCalls,
					transactor.reconciliationCalls,
					submitter.calls,
				)
			}
		})
	}
}

func TestRecoveredBuyCrashBoundariesPreserveOneAction(t *testing.T) {
	now := time.Unix(testBuyProfile(t).ScheduleAnchorUnix+3_601, 0).UTC()
	for _, test := range []struct {
		name            string
		lastEvent       string
		wantSubmissions int
		wantReconciles  int
	}{
		{name: "built", lastEvent: EventBuilt, wantSubmissions: 1, wantReconciles: 1},
		{name: "simulated", lastEvent: EventSimulated, wantSubmissions: 1, wantReconciles: 1},
		{name: "signed", lastEvent: EventSigned, wantSubmissions: 1, wantReconciles: 1},
		{name: "pre-send observed", lastEvent: EventPreSendObserved, wantSubmissions: 1, wantReconciles: 1},
		{name: "send started", lastEvent: EventSendStarted, wantReconciles: 1},
		{name: "submitted", lastEvent: EventSubmitted, wantReconciles: 1},
		{name: "reconciled", lastEvent: EventReconciled},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile, actionID, current := recoveredBuyFixture(t, now)
			store := testStore(t)
			appendRecoveredBuyPrefix(t, store, actionID, current, test.lastEvent)
			stop := &stopStub{}
			submitter := &submitterStub{barrier: stop}
			transactor := &transactorStub{}
			monotonic := uint64(time.Second)
			engine := &Engine{
				store: store, observer: &observerStub{observation: healthyObservation(profile, now)},
				authority: authorityStub{}, signer: signerStub{response: current.signed.Response},
				submitter: submitter, tx: transactor, stop: stop,
				now: func() time.Time { return now },
				clock: func() (clockcheck.Sample, error) {
					monotonic++
					sample := validClockSample(now)
					sample.MonotonicNanos = monotonic
					return sample, nil
				},
				releaseCapacity: store.ReleaseCapacity,
			}
			result, err := engine.RunOnce(t.Context(), profile)
			if err != nil {
				t.Fatal(err)
			}
			if result.Decision != "complete" || result.Verdict != txflow.VerdictFinalized ||
				!result.Submitted || result.InputAsset != "devUSDC" || result.OutputAsset != "SOL" {
				t.Fatalf("recovered buy result = %+v", result)
			}
			if submitter.calls != test.wantSubmissions ||
				transactor.reconciliationCalls != test.wantReconciles ||
				stop.terminalCalls != 0 {
				t.Fatalf(
					"submissions=%d reconciliations=%d terminal_stops=%d",
					submitter.calls, transactor.reconciliationCalls, stop.terminalCalls,
				)
			}
			counts := make(map[string]int)
			for _, record := range store.Records() {
				counts[record.Type]++
			}
			if counts[EventBuilt] != 1 || counts[EventSigned] != 1 ||
				counts[EventSendStarted] != 1 || counts[EventReconciled] != 1 {
				t.Fatalf("recovery duplicated a durable boundary: %+v", counts)
			}
		})
	}
}

func TestRecoveredBuySendMarkerReconcilesThenResubmitsExactResponse(t *testing.T) {
	now := time.Unix(testBuyProfile(t).ScheduleAnchorUnix+3_601, 0).UTC()
	profile, actionID, current := recoveredBuyFixture(t, now)
	path := filepath.Join(t.TempDir(), "buy-recovery.jsonl")
	store, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	appendRecoveredBuyPrefix(t, store, actionID, current, EventSendStarted)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	pending := txflow.Reconciliation{
		Signature: current.signed.Response.Signature, Verdict: txflow.VerdictPending,
	}
	finalized := *current.reconciliation
	trace := make([]string, 0, 3)
	transactor := &transactorStub{
		reconciliations: []txflow.Reconciliation{pending, finalized}, trace: &trace,
	}
	stop := &stopStub{}
	submitter := &submitterStub{barrier: stop, trace: &trace}
	monotonic := uint64(time.Second)
	engine := &Engine{
		store: store, observer: &observerStub{observation: healthyObservation(profile, now)},
		submitter: submitter, tx: transactor, stop: stop,
		now: func() time.Time { return now },
		clock: func() (clockcheck.Sample, error) {
			monotonic++
			sample := validClockSample(now)
			sample.MonotonicNanos = monotonic
			return sample, nil
		},
		releaseCapacity: store.ReleaseCapacity,
	}
	result, err := engine.RunOnce(t.Context(), profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != "complete" || result.Verdict != txflow.VerdictFinalized ||
		!result.Recovered || result.OutputAmount != profile.BuyRoute.MinOutputLamports+7 {
		t.Fatalf("recovered result = %+v", result)
	}
	if submitter.calls != 1 || len(submitter.responses) != 1 ||
		submitter.responses[0] != current.signed.Response ||
		len(submitter.minSlots) != 1 || submitter.minSlots[0] != current.built.BlockhashContextSlot ||
		!submitter.outsideBarrier || stop.barriers != 1 {
		t.Fatalf("submitter calls=%d responses=%d slots=%v outside=%t barriers=%d",
			submitter.calls, len(submitter.responses), submitter.minSlots,
			submitter.outsideBarrier, stop.barriers)
	}
	if transactor.reconciliationCalls != 2 || transactor.verifyEvidenceGenesisCalls != 1 ||
		transactor.verifyGenesisCalls != 1 || transactor.verifyDeploymentCalls != 1 ||
		transactor.verifyTokenInputCalls != 1 || transactor.verifyRentCalls != 1 {
		t.Fatalf("reconcile=%d evidence_genesis=%d genesis=%d deployment=%d token=%d rent=%d",
			transactor.reconciliationCalls, transactor.verifyEvidenceGenesisCalls,
			transactor.verifyGenesisCalls, transactor.verifyDeploymentCalls,
			transactor.verifyTokenInputCalls, transactor.verifyRentCalls)
	}
	wantTrace := []string{"reconcile:pending", "submit", "reconcile:finalized"}
	if len(trace) != len(wantTrace) {
		t.Fatalf("recovery call order = %v", trace)
	}
	for index := range wantTrace {
		if trace[index] != wantTrace[index] {
			t.Fatalf("recovery call order = %v", trace)
		}
	}
	if len(transactor.buySubmissions) != 2 || len(transactor.buyExpected) != 2 ||
		len(transactor.buyFees) != 2 {
		t.Fatalf("captured reconciliation inputs = %d/%d/%d",
			len(transactor.buySubmissions), len(transactor.buyExpected), len(transactor.buyFees))
	}
	wantExpected := txflow.ExpectedBuy{
		Signature:         current.signed.Response.Signature,
		TransactionSHA256: current.signed.Response.TransactionSHA256,
		Policy:            *profile.BuyRoute, InputAmount: profile.InputTokenAmount,
		MinimumOutput: current.built.MinimumOutput,
	}
	for index, submission := range transactor.buySubmissions {
		wantState := txflow.StateAmbiguous
		if index == 1 {
			wantState = txflow.StateAccepted
		}
		if submission.Signature != current.signed.Response.Signature ||
			submission.LastValidBlockHeight != current.built.LastValidBlockHeight ||
			submission.State != wantState || transactor.buyExpected[index] != wantExpected ||
			transactor.buyFees[index] != current.built.FeeLamports {
			t.Fatalf("reconciliation input %d = %+v %+v fee=%d",
				index, submission, transactor.buyExpected[index], transactor.buyFees[index])
		}
	}
	counts := make(map[string]int)
	for _, record := range store.Records() {
		counts[record.Type]++
	}
	if counts[EventSubmitted] != 1 || counts[EventReconciled] != 1 ||
		counts[EventSigned] != 1 || counts[EventSendStarted] != 1 {
		t.Fatalf("recovery event counts = %+v", counts)
	}
	second, err := engine.RunOnce(t.Context(), profile)
	if err != nil || !second.Recovered || second.Decision != "complete" ||
		submitter.calls != 1 || transactor.reconciliationCalls != 2 {
		t.Fatalf("second result=%+v error=%v submissions=%d reconciliations=%d",
			second, err, submitter.calls, transactor.reconciliationCalls)
	}
}

func TestRecoveredBuyRejectsRouteEvidenceDriftBeforeRPC(t *testing.T) {
	now := time.Unix(testBuyProfile(t).ScheduleAnchorUnix+3_601, 0).UTC()
	tests := map[string]func(*Profile, *state){
		"input balance": func(_ *Profile, current *state) {
			current.built.InputTokenBalance--
		},
		"input evidence slot": func(_ *Profile, current *state) {
			current.built.PrimaryInputTokenSlot--
		},
		"temporary rent": func(_ *Profile, current *state) {
			current.built.TemporaryAccountRent++
		},
		"sealed context": func(_ *Profile, current *state) {
			current.signed.Response.SealedTransaction.Metadata.BlockhashContextSlot++
		},
		"route fingerprint": func(profile *Profile, _ *state) {
			profile.BuyRoute.DeploymentSlot++
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			profile, actionID, current := recoveredBuyFixture(t, now)
			mutate(&profile, current)
			store := testStore(t)
			appendRecoveredBuyPrefix(t, store, actionID, current, EventSendStarted)
			transactor := &transactorStub{}
			submitter := &submitterStub{}
			engine := &Engine{
				store: store, tx: transactor, submitter: submitter,
				stop: &stopStub{}, now: func() time.Time { return now },
			}
			if _, err := engine.RunOnce(t.Context(), profile); err == nil {
				t.Fatal("drifted buy recovery was accepted")
			}
			if transactor.verifyEvidenceGenesisCalls != 0 ||
				transactor.reconciliationCalls != 0 || submitter.calls != 0 {
				t.Fatalf(
					"drift reached RPC: genesis=%d reconcile=%d submit=%d",
					transactor.verifyEvidenceGenesisCalls, transactor.reconciliationCalls,
					submitter.calls,
				)
			}
		})
	}
}

func recoveredBuyFixture(t *testing.T, now time.Time) (Profile, string, *state) {
	t.Helper()
	profile := testBuyProfile(t)
	profile.PriceTrigger = nil
	profile.BuyRoute.MinOutputLamports = 45_348
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	windowStart := profile.ScheduleAnchorUnix + 3_600
	actionID, err := orcaswap.ComputeBuyActionID(fingerprint, windowStart)
	if err != nil {
		t.Fatal(err)
	}
	recentBlockhash := solana.Encode(bytes.Repeat([]byte{7}, 32))
	message, err := solana.BuildLegacyMessage(
		profile.owner(), recentBlockhash, buyQuote(t, profile).Instructions,
	)
	if err != nil {
		t.Fatal(err)
	}
	ownerSeed := sha256.Sum256([]byte("buy-profile-owner"))
	ownerKey := ed25519.NewKeyFromSeed(ownerSeed[:])
	transaction, signature, err := solana.SignLegacyMessage(
		ownerKey, message,
	)
	if err != nil {
		t.Fatal(err)
	}
	messageHash := sha256.Sum256(message)
	transactionHash := sha256.Sum256(transaction)
	binding, err := signer.RiskBinding(signer.Request{
		Domain: orcaswap.BuyRequestDomain, Cluster: profile.Cluster,
		Profile: profile.Name, ProfileVersion: profile.Version,
		ProfileFingerprint: fingerprint, ActionID: actionID,
		ScheduleWindowStartUnix: windowStart,
		ScheduleWindowEndUnix:   windowStart + int64(profile.ScheduleWindowSeconds),
		MessageBase64:           base64.StdEncoding.EncodeToString(message), BlockhashContextSlot: 101,
		FeeLamports: 5_000, FeeMinContextSlot: 101,
		PrimaryFeeContextSlot: 101, SecondaryFeeContextSlot: 101,
		RecentBlockhash: recentBlockhash, ObservedBlockHeight: 100, LastValidBlockHeight: 250,
	}, hex.EncodeToString(messageHash[:]))
	if err != nil {
		t.Fatal(err)
	}
	response := signer.Response{
		ActionID: actionID, Signature: solana.Encode(signature[:]),
		RequestSHA256:        binding.RequestSHA256,
		MessageSHA256:        hex.EncodeToString(messageHash[:]),
		TransactionSHA256:    hex.EncodeToString(transactionHash[:]),
		BlockhashContextSlot: 101, FeeLamports: 5_000, LastValidBlockHeight: 250,
	}
	_, recipientPublicKey, err := sealedtx.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	response.SealedTransaction, err = sealedtx.Seal(recipientPublicKey, sealedtx.Metadata{
		Version: sealedtx.Version, Domain: sealedtx.Domain,
		ActionID: actionID, MessageSHA256: response.MessageSHA256,
		TransactionSHA256: response.TransactionSHA256, Signature: response.Signature,
		BlockhashContextSlot: response.BlockhashContextSlot,
		FeeLamports:          response.FeeLamports, LastValidBlockHeight: response.LastValidBlockHeight,
	}, transaction, nil)
	if err != nil {
		t.Fatal(err)
	}
	response.SignerAttestation, err = signer.AttestResponse(
		ownerKey, recipientPublicKey, response,
	)
	if err != nil {
		t.Fatal(err)
	}
	sentAt := now.Add(-time.Second)
	current := &state{
		started: &startedRecord{
			ProfileFingerprint: fingerprint, ScheduleWindowStartUnix: windowStart,
			ScheduleWindowEndUnix: windowStart + int64(profile.ScheduleWindowSeconds),
			ObservationSlot:       100,
		},
		built: &builtRecord{
			MessageBase64: base64.StdEncoding.EncodeToString(message), RecentBlockhash: recentBlockhash,
			BlockhashContextSlot: 101, ObservedBlockHeight: 100, LastValidBlockHeight: 250,
			FeeLamports: 5_000, FeeMinContextSlot: 101,
			PrimaryFeeContextSlot: 101, SecondaryFeeContextSlot: 101,
			MinimumOutput:        profile.BuyRoute.MinOutputLamports,
			TemporaryAccountRent: 2_039_280, InputTokenBalance: profile.InputTokenAmount,
			PrimaryInputTokenSlot: 101, SecondaryInputTokenSlot: 101,
		},
		simulation: &txflow.LegacySimulationEvidence{
			ProviderIdentity: "mithril", MinContextSlot: 101, ContextSlot: 101,
			LogsSHA256: strings.Repeat("1", 64),
		},
		signed:             &signedRecord{Response: response},
		preSendObservation: pointerTo(healthyObservation(profile, sentAt)),
		sendStarted: &sendStartedRecord{
			Signature: response.Signature, TransactionSHA256: response.TransactionSHA256,
		},
		sendStartedAt: sentAt,
		submission: &txflow.Submission{
			Signature: response.Signature, LastValidBlockHeight: 250, State: txflow.StateAccepted,
		},
	}
	current.reconciliation = &txflow.Reconciliation{
		Signature: response.Signature, Verdict: txflow.VerdictFinalized, Slot: 120,
		PrimaryFound: true, SecondaryFound: true,
		PrimarySlot: 120, SecondarySlot: 120,
		PrimaryStatus: "finalized", SecondaryStatus: "finalized",
		BuyEffects: &txflow.BuyEffectEvidence{
			TransactionSHA256: response.TransactionSHA256, FeeLamports: 5_000,
			InputAmount: profile.InputTokenAmount, MinimumOutput: profile.BuyRoute.MinOutputLamports,
			OutputLamports:    profile.BuyRoute.MinOutputLamports + 7,
			PrimaryEffectSlot: 120, SecondaryEffectSlot: 120,
		},
	}
	return profile, actionID, current
}

func appendRecoveredBuyPrefix(
	t *testing.T,
	store *journal.Store,
	actionID string,
	current *state,
	lastEvent string,
) {
	t.Helper()
	records := []struct {
		typeName string
		payload  any
	}{
		{EventStarted, *current.started}, {EventBuilt, *current.built},
		{EventSimulated, *current.simulation}, {EventSigned, *current.signed},
		{EventPreSendObserved, *current.preSendObservation},
		{EventSendStarted, *current.sendStarted}, {EventSubmitted, *current.submission},
		{EventReconciled, *current.reconciliation},
	}
	for _, record := range records {
		at := current.sendStartedAt
		if record.typeName == EventStarted {
			at = time.Unix(current.started.ScheduleWindowStartUnix, 0).UTC()
		}
		if _, err := store.Append(at, record.typeName, actionID, record.payload); err != nil {
			t.Fatal(err)
		}
		if record.typeName == lastEvent {
			return
		}
	}
	t.Fatalf("unknown recovery boundary %q", lastEvent)
}

func recoveredSwapFixture(t *testing.T, now time.Time) (Profile, string, *state) {
	t.Helper()
	profile := testProfile()
	ownerSeed := sha256.Sum256([]byte("swaprun recovered owner"))
	ownerKey := ed25519.NewKeyFromSeed(ownerSeed[:])
	profile.Route.Owner = solana.Encode(ownerKey.Public().(ed25519.PublicKey))
	var err error
	profile.Route.InputTokenAccount, err = orcaswap.AssociatedTokenAddress(
		profile.Route.Owner, profile.Route.InputMint,
	)
	if err != nil {
		t.Fatal(err)
	}
	profile.Route.OutputTokenAccount, err = orcaswap.AssociatedTokenAddress(
		profile.Route.Owner, profile.Route.OutputMint,
	)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	windowStart := profile.ScheduleAnchorUnix + 3_600
	actionID, err := orcaswap.ComputeActionID(fingerprint, windowStart)
	if err != nil {
		t.Fatal(err)
	}
	recentBlockhash := solana.Encode(bytes.Repeat([]byte{7}, 32))
	message, err := solana.BuildLegacyMessage(
		profile.Route.Owner, recentBlockhash, swapQuote(profile).Instructions,
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction, signature, err := solana.SignLegacyMessage(ownerKey, message)
	if err != nil {
		t.Fatal(err)
	}
	messageHash := sha256.Sum256(message)
	transactionHash := sha256.Sum256(transaction)
	binding, err := signer.RiskBinding(signer.Request{
		Domain: orcaswap.RequestDomain, Cluster: profile.Cluster,
		Profile: profile.Name, ProfileVersion: profile.Version,
		ProfileFingerprint: fingerprint, ActionID: actionID,
		ScheduleWindowStartUnix: windowStart,
		ScheduleWindowEndUnix:   windowStart + int64(profile.ScheduleWindowSeconds),
		MessageBase64:           base64.StdEncoding.EncodeToString(message), BlockhashContextSlot: 101,
		FeeLamports: 5_000, FeeMinContextSlot: 101,
		PrimaryFeeContextSlot: 101, SecondaryFeeContextSlot: 101,
		RecentBlockhash: recentBlockhash, ObservedBlockHeight: 100, LastValidBlockHeight: 250,
	}, hex.EncodeToString(messageHash[:]))
	if err != nil {
		t.Fatal(err)
	}
	response := signer.Response{
		ActionID:             actionID,
		RequestSHA256:        binding.RequestSHA256,
		Signature:            solana.Encode(signature[:]),
		MessageSHA256:        hex.EncodeToString(messageHash[:]),
		TransactionSHA256:    hex.EncodeToString(transactionHash[:]),
		BlockhashContextSlot: 101,
		FeeLamports:          5_000,
		LastValidBlockHeight: 250,
	}
	_, recipientPublicKey, err := sealedtx.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	response.SealedTransaction, err = sealedtx.Seal(
		recipientPublicKey,
		sealedtx.Metadata{
			Version:              sealedtx.Version,
			Domain:               sealedtx.Domain,
			ActionID:             response.ActionID,
			MessageSHA256:        response.MessageSHA256,
			TransactionSHA256:    response.TransactionSHA256,
			Signature:            response.Signature,
			BlockhashContextSlot: response.BlockhashContextSlot,
			FeeLamports:          response.FeeLamports,
			LastValidBlockHeight: response.LastValidBlockHeight,
		},
		transaction,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response.SignerAttestation, err = signer.AttestResponse(
		ownerKey, recipientPublicKey, response,
	)
	if err != nil {
		t.Fatal(err)
	}
	sentAt := now.Add(-time.Second)
	return profile, actionID, &state{
		started: &startedRecord{
			ProfileFingerprint:      fingerprint,
			ScheduleWindowStartUnix: windowStart,
			ScheduleWindowEndUnix:   windowStart + int64(profile.ScheduleWindowSeconds),
			ObservationSlot:         100,
		},
		built: &builtRecord{
			MessageBase64:           base64.StdEncoding.EncodeToString(message),
			RecentBlockhash:         recentBlockhash,
			BlockhashContextSlot:    101,
			ObservedBlockHeight:     100,
			LastValidBlockHeight:    response.LastValidBlockHeight,
			FeeLamports:             response.FeeLamports,
			FeeMinContextSlot:       101,
			PrimaryFeeContextSlot:   101,
			SecondaryFeeContextSlot: 101,
			MinimumOutput:           21_525,
		},
		simulation: &txflow.LegacySimulationEvidence{
			ProviderIdentity: "mithril", MinContextSlot: 101, ContextSlot: 101,
			LogsSHA256: strings.Repeat("1", 64),
		},
		signed:             &signedRecord{Response: response},
		preSendObservation: pointerTo(healthyObservation(profile, sentAt)),
		sendStarted: &sendStartedRecord{
			Signature: response.Signature, TransactionSHA256: response.TransactionSHA256,
		},
		sendStartedAt: sentAt,
	}
}

func appendRecoveredSwap(
	t *testing.T,
	store *journal.Store,
	actionID string,
	current *state,
) {
	t.Helper()
	records := []struct {
		typeName string
		payload  any
	}{
		{EventStarted, *current.started},
		{EventBuilt, *current.built},
		{EventSimulated, *current.simulation},
		{EventSigned, *current.signed},
		{EventPreSendObserved, *current.preSendObservation},
		{EventSendStarted, *current.sendStarted},
	}
	if current.submission != nil {
		records = append(records, struct {
			typeName string
			payload  any
		}{EventSubmitted, *current.submission})
	}
	for index, record := range records {
		at := current.sendStartedAt
		if index == 0 {
			at = time.Unix(current.started.ScheduleWindowStartUnix, 0).UTC()
		}
		if _, err := store.Append(at, record.typeName, actionID, record.payload); err != nil {
			t.Fatal(err)
		}
	}
}

func pointerTo[T any](value T) *T {
	return &value
}
