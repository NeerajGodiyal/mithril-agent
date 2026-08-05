package execution

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/internal/clockcheck"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

type fakeObserver struct {
	observation agent.Observation
	health      agent.NodeHealth
	responses   []agent.NodeObservation
	err         error
	calls       int
}

func (f *fakeObserver) Observe(context.Context, string) (agent.NodeObservation, error) {
	f.calls++
	if len(f.responses) > 0 {
		response := f.responses[0]
		f.responses = f.responses[1:]
		return response, f.err
	}
	return agent.NodeObservation{Account: f.observation, Health: f.health}, f.err
}

type fakeBlockhash struct {
	latest        solanarpc.LatestBlockhash
	height        uint64
	latestMinSlot *uint64
}

func TestBuildAndRecoveryRejectBlockhashBeforeObservation(t *testing.T) {
	proposal := agent.Proposal{ObservationSlot: 100}
	engine := &Engine{blockhash: fakeBlockhash{latest: solanarpc.LatestBlockhash{ContextSlot: 99}}}
	if _, err := engine.build(t.Context(), proposal); err == nil {
		t.Fatal("live build accepted a blockhash before its observation")
	}
	if err := validateBuilt(proposal, builtTransaction{BlockhashContextSlot: 99}); err == nil {
		t.Fatal("recovery accepted a blockhash before its observation")
	}
	engine = &Engine{blockhash: fakeBlockhash{latest: solanarpc.LatestBlockhash{
		ContextSlot: 100, LastValidBlockHeight: 200,
	}}}
	if _, err := engine.build(t.Context(), proposal); err == nil {
		t.Fatal("live build accepted a zero block height")
	}
	if err := validateBuilt(proposal, builtTransaction{
		BlockhashContextSlot: 100, LastValidBlockHeight: 200,
	}); err == nil {
		t.Fatal("recovery accepted a zero observed block height")
	}
}

func TestValidateSignedBindsOuterAndSealedBlockhashContext(t *testing.T) {
	fixture := newExecutionFixture(t)
	proposalEngine, err := agent.NewEngine(fixture.store, func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := proposalEngine.Propose(fixture.profile, fixture.observer.observation)
	if err != nil {
		t.Fatal(err)
	}
	message, err := solana.BuildTransferMessage(
		fixture.profile.Source, fixture.profile.Destination,
		fixture.blockhash.latest.Blockhash, proposed.Proposal.AmountLamports,
	)
	if err != nil {
		t.Fatal(err)
	}
	built := builtTransaction{
		MessageBase64:        base64.StdEncoding.EncodeToString(message),
		RecentBlockhash:      fixture.blockhash.latest.Blockhash,
		BlockhashContextSlot: fixture.blockhash.latest.ContextSlot,
		ObservedBlockHeight:  fixture.blockhash.height,
		LastValidBlockHeight: fixture.blockhash.latest.LastValidBlockHeight,
		FeeLamports:          fixture.tx.fee, FeeMinContextSlot: fixture.blockhash.latest.ContextSlot,
		PrimaryFeeContextSlot:   fixture.blockhash.latest.ContextSlot,
		SecondaryFeeContextSlot: fixture.blockhash.latest.ContextSlot,
	}
	fixture.authority.now = func() time.Time { return fixture.now }
	fixture.signer.now = func() time.Time { return fixture.now }
	request := signingRequest(proposed.Proposal, built)
	request.RiskGrant, err = fixture.authority.Authorize(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := fixture.signer.Sign(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSigned(proposed.Proposal, built, signedTransaction{SignerResponse: response}); err != nil {
		t.Fatal(err)
	}
	outer := response
	outer.BlockhashContextSlot++
	if err := validateSigned(proposed.Proposal, built, signedTransaction{SignerResponse: outer}); err == nil {
		t.Fatal("outer blockhash context drift was accepted")
	}
	sealed := response
	sealed.SealedTransaction.Metadata.BlockhashContextSlot++
	if err := validateSigned(proposed.Proposal, built, signedTransaction{SignerResponse: sealed}); err == nil {
		t.Fatal("sealed blockhash context drift was accepted")
	}
	wrongSignature := response
	wrongSignature.Signature = solana.Encode(make([]byte, ed25519.SignatureSize))
	wrongSignature.SealedTransaction.Metadata.Signature = wrongSignature.Signature
	wrongSignature.SignerAttestation, err = signer.AttestResponse(
		fixture.signer.key,
		wrongSignature.SignerAttestation.SubmitterPublicKey,
		wrongSignature,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSigned(
		proposed.Proposal, built, signedTransaction{SignerResponse: wrongSignature},
	); err == nil {
		t.Fatal("attested invalid Solana signature was accepted")
	}
}

func (f fakeBlockhash) LatestBlockhash(
	_ context.Context,
	minContextSlot uint64,
) (solanarpc.LatestBlockhash, error) {
	if f.latestMinSlot != nil {
		*f.latestMinSlot = minContextSlot
	}
	return f.latest, nil
}

func (f fakeBlockhash) BlockHeight(context.Context) (uint64, error) {
	return f.height, nil
}

type localSigner struct {
	policy           signer.Policy
	key              ed25519.PrivateKey
	now              func() time.Time
	err              error
	loseNextResponse bool
	calls            int
}

func (s *localSigner) Sign(_ context.Context, request signer.Request) (signer.Response, error) {
	s.calls++
	if s.err != nil {
		return signer.Response{}, s.err
	}
	var (
		response signer.Response
		err      error
	)
	response, err = signer.AuthorizeAndSign(s.policy, s.key, request, s.now())
	if err != nil {
		return signer.Response{}, err
	}
	if s.loseNextResponse {
		s.loseNextResponse = false
		return signer.Response{}, errors.New("signer response lost")
	}
	return response, nil
}

type localPolicyAuthority struct {
	key             ed25519.PrivateKey
	keyID           string
	now             func() time.Time
	rejectSignature string
	calls           int
}

func (a *localPolicyAuthority) Authorize(
	_ context.Context,
	request signer.Request,
) (riskgrant.Grant, error) {
	a.calls++
	message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
	if err != nil {
		return riskgrant.Grant{}, err
	}
	digest := sha256.Sum256(message)
	binding, err := signer.RiskBinding(request, hex.EncodeToString(digest[:]))
	if err != nil {
		return riskgrant.Grant{}, err
	}
	return riskgrant.Sign(
		a.key,
		a.keyID,
		binding,
		a.now(),
		30*time.Second,
	)
}

func (a *localPolicyAuthority) VerifyAt(
	request signer.Request,
	grant riskgrant.Grant,
	at time.Time,
) error {
	if grant.SignatureBase64 == a.rejectSignature {
		return errors.New("rejected test grant")
	}
	message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(message)
	binding, err := signer.RiskBinding(request, hex.EncodeToString(digest[:]))
	if err != nil {
		return err
	}
	return riskgrant.Verify(
		a.key.Public().(ed25519.PublicKey),
		a.keyID,
		binding,
		grant,
		at,
	)
}

type fakeTransactor struct {
	submitterPrivateKey          string
	submitCalls                  int
	reconcile                    []txflow.Reconciliation
	genesisErr                   error
	evidenceGenesisErr           error
	evidenceGenesisCalls         int
	fee                          uint64
	feeErr                       error
	feeCalls                     int
	simulationErr                error
	simulationCalls              int
	primaryBalance               uint64
	secondaryBalance             uint64
	balanceErr                   error
	balanceCalls                 int
	balanceResponses             []balanceResponse
	commonFinalizedFloor         uint64
	expired                      []bool
	expiredCalls                 int
	beforeExpiryCheck            func(int)
	duringSubmit                 func()
	waitForReconcileCancellation bool
	reconcileStarted             chan struct{}
}

type balanceResponse struct {
	primary   uint64
	secondary uint64
}

type fakeStopChecker struct {
	stopped       bool
	err           error
	responses     []bool
	calls         int
	beforeBarrier func()
}

func (f *fakeStopChecker) NoNewActions() (bool, error) {
	f.calls++
	if len(f.responses) > 0 {
		stopped := f.responses[0]
		f.responses = f.responses[1:]
		return stopped, f.err
	}
	return f.stopped, f.err
}

func (f *fakeStopChecker) WithSendBarrier(
	_ string,
	operation func() error,
) (bool, error) {
	stopped, err := f.NoNewActions()
	if err != nil || stopped {
		return stopped, err
	}
	if f.beforeBarrier != nil {
		f.beforeBarrier()
	}
	return false, operation()
}

func (f *fakeTransactor) VerifyGenesis(context.Context, string) error {
	return f.genesisErr
}

func (f *fakeTransactor) VerifyEvidenceGenesis(context.Context, string) error {
	f.evidenceGenesisCalls++
	return f.evidenceGenesisErr
}

func (f *fakeTransactor) AccountsForTransfer(
	_ context.Context,
	source,
	destination string,
	minContextSlot uint64,
) (txflow.TransferAccountEvidence, error) {
	f.balanceCalls++
	primaryBalance := f.primaryBalance
	secondaryBalance := f.secondaryBalance
	if len(f.balanceResponses) > 0 {
		primaryBalance = f.balanceResponses[0].primary
		secondaryBalance = f.balanceResponses[0].secondary
		f.balanceResponses = f.balanceResponses[1:]
	}
	commonFloor := minContextSlot
	if f.commonFinalizedFloor != 0 {
		commonFloor = f.commonFinalizedFloor
	}
	systemProgram := solana.Encode(make([]byte, 32))
	return txflow.TransferAccountEvidence{
		ObservationSlot:        minContextSlot,
		CommonFinalizedFloor:   commonFloor,
		PrimaryFinalizedSlot:   commonFloor,
		SecondaryFinalizedSlot: commonFloor + 1,
		Source: txflow.AccountEvidence{
			Address:              source,
			PrimaryContextSlot:   commonFloor,
			PrimaryLamports:      primaryBalance,
			PrimaryOwner:         systemProgram,
			SecondaryContextSlot: commonFloor + 1,
			SecondaryLamports:    secondaryBalance,
			SecondaryOwner:       systemProgram,
		},
		Destination: txflow.AccountEvidence{
			Address:              destination,
			PrimaryContextSlot:   commonFloor,
			PrimaryLamports:      1,
			PrimaryOwner:         systemProgram,
			SecondaryContextSlot: commonFloor + 1,
			SecondaryLamports:    1,
			SecondaryOwner:       systemProgram,
		},
	}, f.balanceErr
}

func (f *fakeTransactor) BlockhashExpired(context.Context, uint64) (bool, error) {
	f.expiredCalls++
	if f.beforeExpiryCheck != nil {
		f.beforeExpiryCheck(f.expiredCalls)
	}
	if len(f.expired) == 0 {
		return false, nil
	}
	expired := f.expired[0]
	f.expired = f.expired[1:]
	return expired, nil
}

func (f *fakeTransactor) FeeForMessage(_ context.Context, _ []byte, minContextSlot uint64) (txflow.FeeEvidence, error) {
	f.feeCalls++
	return txflow.FeeEvidence{
		Lamports:             f.fee,
		MinContextSlot:       minContextSlot,
		PrimaryContextSlot:   minContextSlot,
		SecondaryContextSlot: minContextSlot + 1,
	}, f.feeErr
}

func (f *fakeTransactor) Simulate(_ context.Context, _ []byte, minContextSlot uint64) (txflow.SimulationEvidence, error) {
	f.simulationCalls++
	return txflow.SimulationEvidence{
		ProviderIdentity:        "mithril-node",
		MinContextSlot:          minContextSlot,
		ContextSlot:             minContextSlot,
		UnitsConsumed:           150,
		SourcePostLamports:      900,
		DestinationPostLamports: 50,
		LogsSHA256:              hex.EncodeToString(bytes.Repeat([]byte{1}, sha256.Size)),
		AccountsSHA256:          hex.EncodeToString(bytes.Repeat([]byte{2}, sha256.Size)),
	}, f.simulationErr
}

func (f *fakeTransactor) Submit(
	_ context.Context,
	response signer.Response,
	minContextSlot uint64,
) (txflow.Submission, error) {
	f.submitCalls++
	if f.duringSubmit != nil {
		f.duringSubmit()
	}
	if minContextSlot == 0 {
		return txflow.Submission{}, errors.New("minimum context slot is required")
	}
	transaction, err := sealedtx.Open(f.submitterPrivateKey, response.SealedTransaction)
	if err != nil {
		return txflow.Submission{}, err
	}
	decoded, err := solana.DecodeSignedTransfer(transaction)
	if err != nil {
		return txflow.Submission{}, err
	}
	return txflow.Submission{
		Signature:            solana.Encode(decoded.Signature[:]),
		LastValidBlockHeight: response.LastValidBlockHeight,
		State:                txflow.StateAccepted,
	}, nil
}

func TestSubmissionRunsInsideStopBarrier(t *testing.T) {
	fixture := newExecutionFixture(t)
	inside := false
	checker := &fakeStopChecker{}
	checker.beforeBarrier = func() { inside = true }
	fixture.tx.duringSubmit = func() {
		if !inside {
			t.Fatal("submission ran outside the stop barrier")
		}
		inside = false
	}
	engine := fixture.engine(t)
	engine.stop = checker

	if _, err := engine.RunOnce(t.Context(), fixture.profile); err != nil {
		t.Fatal(err)
	}
	if fixture.tx.submitCalls != 1 {
		t.Fatalf("submission calls = %d, want 1", fixture.tx.submitCalls)
	}
}

func (f *fakeTransactor) ReconcileExpected(
	ctx context.Context,
	submission txflow.Submission,
	expected txflow.ExpectedTransaction,
	feeLamports uint64,
) (txflow.Reconciliation, error) {
	if f.waitForReconcileCancellation {
		close(f.reconcileStarted)
		<-ctx.Done()
		return txflow.Reconciliation{}, ctx.Err()
	}
	if len(f.reconcile) == 0 {
		return txflow.Reconciliation{
			Signature: submission.Signature,
			Verdict:   txflow.VerdictPending,
		}, nil
	}
	result := f.reconcile[0]
	f.reconcile = f.reconcile[1:]
	result.Signature = submission.Signature
	switch result.Verdict {
	case txflow.VerdictFinalized:
		result.PrimaryFound = true
		result.SecondaryFound = true
		result.PrimarySlot = result.Slot
		result.SecondarySlot = result.Slot
		result.PrimaryStatus = "finalized"
		result.SecondaryStatus = "finalized"
		result.Effects = testEffectEvidence(expected, feeLamports, result.Slot, false)
	case txflow.VerdictFailed:
		result.PrimaryFound = true
		result.SecondaryFound = true
		result.PrimarySlot = result.Slot
		result.SecondarySlot = result.Slot
		result.PrimaryStatus = "finalized"
		result.SecondaryStatus = "finalized"
		result.PrimaryFailed = true
		result.SecondaryFailed = true
		result.PrimaryErrorFingerprint = "failure"
		result.SecondaryErrorFingerprint = "failure"
		result.Effects = testEffectEvidence(expected, feeLamports, result.Slot, true)
	case txflow.VerdictUnresolved:
		result.PrimaryBlockHeight = submission.LastValidBlockHeight + 1
		result.SecondaryBlockHeight = submission.LastValidBlockHeight + 1
	case txflow.VerdictDiverged:
		result.PrimaryFound = true
		result.SecondaryFound = true
		if result.DivergenceKind == txflow.DivergenceEffects {
			result.PrimarySlot = result.Slot
			result.SecondarySlot = result.Slot
			result.PrimaryStatus = "finalized"
			result.SecondaryStatus = "finalized"
		} else {
			result.PrimarySlot = 1
			result.SecondarySlot = 2
			result.DivergenceKind = txflow.DivergenceStatus
		}
	}
	return result, nil
}

func testEffectEvidence(
	expected txflow.ExpectedTransaction,
	feeLamports uint64,
	slot uint64,
	failed bool,
) *txflow.EffectEvidence {
	sourcePre := uint64(1_000_000)
	destinationPre := uint64(10)
	sourceDebit := feeLamports
	destinationCredit := uint64(0)
	if !failed {
		sourceDebit += expected.AmountLamports
		destinationCredit = expected.AmountLamports
	}
	return &txflow.EffectEvidence{
		TransactionSHA256:       expected.TransactionSHA256,
		FeeLamports:             feeLamports,
		SourcePreLamports:       sourcePre,
		SourcePostLamports:      sourcePre - sourceDebit,
		DestinationPreLamports:  destinationPre,
		DestinationPostLamports: destinationPre + destinationCredit,
		PrimaryEffectSlot:       slot,
		SecondaryEffectSlot:     slot,
	}
}

func TestRunOnceCompletesAndDoesNotRepeatTerminalAction(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.tx.reconcile = []txflow.Reconciliation{{Verdict: txflow.VerdictFinalized, Slot: 120}}
	engine := fixture.engine(t)
	first, err := engine.RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if first.Decision != "complete" || first.Verdict != txflow.VerdictFinalized ||
		fixture.tx.submitCalls != 1 || fixture.signer.calls != 1 {
		t.Fatalf("first run = %+v, submit=%d sign=%d", first, fixture.tx.submitCalls, fixture.signer.calls)
	}
	stats, err := fixture.store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.ReservedBytes != 0 {
		t.Fatalf("terminal execution retained %d reserved bytes", stats.ReservedBytes)
	}
	recordCount := len(fixture.store.Records())
	second, err := engine.RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Recovered || second.ActionID != first.ActionID ||
		fixture.tx.submitCalls != 1 || fixture.signer.calls != 1 ||
		len(fixture.store.Records()) != recordCount {
		t.Fatalf("terminal action repeated: %+v, records=%d/%d", second, len(fixture.store.Records()), recordCount)
	}
}

func TestTerminalResultSurvivesReserveCleanupFailure(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.tx.reconcile = []txflow.Reconciliation{{Verdict: txflow.VerdictFailed, Slot: 120}}
	engine := fixture.engine(t)
	releaseCalls := 0
	engine.releaseCapacity = func() error {
		releaseCalls++
		if releaseCalls == 1 {
			return fixture.store.ReleaseCapacity()
		}
		return errors.New("reserve cleanup failed")
	}
	result, err := engine.RunOnce(t.Context(), fixture.profile)
	if err != nil || result.Decision != "failed" || result.Verdict != txflow.VerdictFailed ||
		result.ActionID == "" || releaseCalls != 2 {
		t.Fatalf("terminal result = %+v, release calls=%d, err=%v", result, releaseCalls, err)
	}
}

func TestOperatorStopIsRecheckedBeforeSigningAndSubmission(t *testing.T) {
	tests := []struct {
		name          string
		stopResponses []bool
		wantDecision  string
		wantReason    string
		wantSigns     int
	}{
		{
			name:          "before signing",
			stopResponses: []bool{false, true},
			wantDecision:  "canceled",
			wantReason:    "operator_stop_before_signing",
		},
		{
			name:          "after signing before submission",
			stopResponses: []bool{false, false, false, true},
			wantDecision:  "halted",
			wantReason:    "operator_stop_before_submission",
			wantSigns:     1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutionFixture(t)
			checker := &fakeStopChecker{responses: test.stopResponses}
			engine := fixture.engine(t)
			engine.stop = checker
			result, err := engine.RunOnce(t.Context(), fixture.profile)
			if err != nil {
				t.Fatal(err)
			}
			if result.Decision != test.wantDecision || result.Reason != test.wantReason ||
				fixture.signer.calls != test.wantSigns || fixture.tx.submitCalls != 0 {
				t.Fatalf(
					"result=%+v signs=%d submits=%d stop_checks=%d",
					result,
					fixture.signer.calls,
					fixture.tx.submitCalls,
					checker.calls,
				)
			}
		})
	}
}

func TestCancellationAtSendBarrierDoesNotWriteMarkerOrSubmit(t *testing.T) {
	fixture := newExecutionFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	checker := &fakeStopChecker{beforeBarrier: cancel}
	engine := fixture.engine(t)
	engine.stop = checker

	if _, err := engine.RunOnce(ctx, fixture.profile); !errors.Is(err, context.Canceled) {
		t.Fatalf("send-boundary cancellation = %v", err)
	}
	if fixture.tx.submitCalls != 0 {
		t.Fatalf("submission calls = %d", fixture.tx.submitCalls)
	}
	for _, record := range fixture.store.Records() {
		if record.Type == EventTransactionSendStarted ||
			record.Type == EventTransactionSubmitted {
			t.Fatalf("canceled send wrote %s", record.Type)
		}
	}
}

func TestCancellationDuringFinalSendAuthorizationDoesNotWriteMarkerOrSubmit(t *testing.T) {
	fixture := newExecutionFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	engine := fixture.engine(t)
	goodClock := engine.clock
	clockChecks := 0
	engine.clock = func() (clockcheck.Sample, error) {
		clockChecks++
		sample, err := goodClock()
		if clockChecks == 4 {
			cancel()
		}
		return sample, err
	}

	if _, err := engine.RunOnce(ctx, fixture.profile); !errors.Is(err, context.Canceled) {
		t.Fatalf("final send authorization cancellation = %v", err)
	}
	if clockChecks != 4 {
		t.Fatalf("clock checks = %d, want 4", clockChecks)
	}
	if fixture.tx.submitCalls != 0 {
		t.Fatalf("submission calls = %d", fixture.tx.submitCalls)
	}
	for _, record := range fixture.store.Records() {
		if record.Type == EventTransactionSendStarted ||
			record.Type == EventTransactionSubmitted {
			t.Fatalf("canceled send wrote %s", record.Type)
		}
	}
}

func TestSendBoundaryRechecksTimeAfterCapacityReservation(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.tx.beforeExpiryCheck = func(call int) {
		if call == 4 {
			fixture.now = fixture.now.Add(61 * time.Minute)
		}
	}
	result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != "halted" ||
		result.Reason != "schedule_window_expired_before_submission" ||
		fixture.signer.calls != 1 || fixture.tx.submitCalls != 0 {
		t.Fatalf(
			"final send-boundary expiry = %+v signs=%d submits=%d",
			result,
			fixture.signer.calls,
			fixture.tx.submitCalls,
		)
	}
	for _, record := range fixture.store.Records() {
		if record.Type == EventTransactionSendStarted ||
			record.Type == EventTransactionSubmitted {
			t.Fatalf("expired send wrote %s", record.Type)
		}
	}
}

func TestSignedQuarantineSurvivesRestartAndCannotSubmit(t *testing.T) {
	fixture := newExecutionFixture(t)
	checker := &fakeStopChecker{responses: []bool{false, false, true}}
	engine := fixture.engine(t)
	engine.stop = checker
	first, err := engine.RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	checker.stopped = false
	checker.responses = nil
	second, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if first.Decision != "halted" || second.Decision != "halted" ||
		!second.Recovered || second.ActionID != first.ActionID ||
		fixture.signer.calls != 1 || fixture.tx.submitCalls != 0 {
		t.Fatalf(
			"quarantine recovery first=%+v second=%+v signs=%d submits=%d",
			first,
			second,
			fixture.signer.calls,
			fixture.tx.submitCalls,
		)
	}
}

func TestSignedQuarantineRetiresOnlyAfterMithrilBlockhashExpiry(t *testing.T) {
	fixture := newExecutionFixture(t)
	engine := fixture.engine(t)
	engine.stop = &fakeStopChecker{responses: []bool{false, false, true}}
	first, err := engine.RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if first.Decision != "halted" || fixture.tx.submitCalls != 0 {
		t.Fatalf("initial quarantine = %+v, submits=%d", first, fixture.tx.submitCalls)
	}

	fixture.blockhash.height = fixture.blockhash.latest.LastValidBlockHeight + 1
	resolved, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Decision != "canceled" ||
		resolved.Reason != "quarantine_resolved_after_blockhash_expiry" ||
		!resolved.Recovered || fixture.tx.submitCalls != 0 {
		t.Fatalf("quarantine resolution = %+v, submits=%d", resolved, fixture.tx.submitCalls)
	}
	recovered, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Decision != "canceled" ||
		recovered.ActionID != resolved.ActionID ||
		fixture.signer.calls != 1 || fixture.tx.submitCalls != 0 {
		t.Fatalf(
			"resolved quarantine recovery = %+v, signs=%d submits=%d",
			recovered,
			fixture.signer.calls,
			fixture.tx.submitCalls,
		)
	}
}

func TestBalanceIsRecheckedBeforeSigningAndSubmission(t *testing.T) {
	tests := []struct {
		name         string
		balances     []balanceResponse
		wantDecision string
		wantReason   string
		wantSigns    int
	}{
		{
			name: "before signing",
			balances: []balanceResponse{
				{primary: 1_000, secondary: 1_000},
				{primary: 100, secondary: 100},
			},
			wantDecision: "canceled",
			wantReason:   "balance_changed_before_signing",
		},
		{
			name: "after signing before submission",
			balances: []balanceResponse{
				{primary: 1_000, secondary: 1_000},
				{primary: 1_000, secondary: 1_000},
				{primary: 100, secondary: 100},
			},
			wantDecision: "halted",
			wantReason:   "balance_changed_before_submission",
			wantSigns:    1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutionFixture(t)
			fixture.tx.balanceResponses = test.balances
			result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
			if err != nil {
				t.Fatal(err)
			}
			if result.Decision != test.wantDecision || result.Reason != test.wantReason ||
				fixture.signer.calls != test.wantSigns || fixture.tx.submitCalls != 0 {
				t.Fatalf(
					"result=%+v signs=%d submits=%d account_checks=%d",
					result,
					fixture.signer.calls,
					fixture.tx.submitCalls,
					fixture.tx.balanceCalls,
				)
			}
		})
	}
}

func TestFinalizedFloorAheadOfMithrilDoesNotUnderflowLagCheck(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.tx.commonFinalizedFloor = fixture.observer.observation.Slot + 50
	fixture.tx.reconcile = []txflow.Reconciliation{{
		Verdict: txflow.VerdictFinalized,
		Slot:    fixture.tx.commonFinalizedFloor + 1,
	}}
	result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != "complete" || fixture.signer.calls != 1 ||
		fixture.tx.submitCalls != 1 {
		t.Fatalf("ahead finalized floor result=%+v", result)
	}
}

func TestClockIsRecheckedBeforeSigningAndSubmission(t *testing.T) {
	tests := []struct {
		name         string
		failOnSample int
		wantSigns    int
	}{
		{name: "before signing", failOnSample: 2},
		{name: "before account refresh", failOnSample: 3, wantSigns: 1},
		{name: "at durable send boundary", failOnSample: 4, wantSigns: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutionFixture(t)
			engine := fixture.engine(t)
			goodClock := engine.clock
			calls := 0
			engine.clock = func() (clockcheck.Sample, error) {
				calls++
				sample, err := goodClock()
				if calls == test.failOnSample {
					sample.UncertaintyNanos =
						uint64(fixture.profile.ClockUncertaintyLimit()) + 1
				}
				return sample, err
			}
			if _, err := engine.RunOnce(t.Context(), fixture.profile); err == nil {
				t.Fatal("unsafe clock sample was accepted")
			}
			if fixture.signer.calls != test.wantSigns || fixture.tx.submitCalls != 0 {
				t.Fatalf(
					"signs=%d submits=%d clock_checks=%d",
					fixture.signer.calls,
					fixture.tx.submitCalls,
					calls,
				)
			}
		})
	}
}

func TestExecutionWaitsForSustainedHealthySlotProgress(t *testing.T) {
	fixture := newExecutionFixture(t)
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	emptyStore, err := journal.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = emptyStore.Close() })
	fixture.store = emptyStore
	fixture.tx.reconcile = []txflow.Reconciliation{{Verdict: txflow.VerdictFinalized, Slot: 120}}

	first, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if first.Decision != "waiting" || fixture.signer.calls != 0 || fixture.tx.submitCalls != 0 {
		t.Fatalf("first health sample authorized execution: %+v", first)
	}
	fixture.now = fixture.now.Add(5 * time.Second)
	fixture.observer.observation.Slot++
	fixture.observer.observation.ObservedAt = fixture.now
	fixture.observer.health.ObservedAt = fixture.now
	second, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if second.Verdict != txflow.VerdictFinalized || fixture.signer.calls != 1 ||
		fixture.tx.submitCalls != 1 {
		t.Fatalf("sustained health did not authorize bounded execution: %+v", second)
	}
}

func TestUnhealthyObservationResetsSustainedHealth(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.observer.health.Status = "critical"
	first, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if first.Decision != "degraded" {
		t.Fatalf("critical health result = %+v", first)
	}
	fixture.observer.health.Status = "healthy"
	for index := 0; index < 2; index++ {
		fixture.now = fixture.now.Add(5 * time.Second)
		fixture.observer.observation.Slot++
		fixture.observer.observation.ObservedAt = fixture.now
		fixture.observer.health.ObservedAt = fixture.now
		result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 && result.Decision != "waiting" {
			t.Fatalf("one healthy sample after critical state authorized execution: %+v", result)
		}
		if index == 1 && result.Decision == "waiting" {
			t.Fatalf("two healthy samples did not recover the gate: %+v", result)
		}
	}
}

func TestClockGateRejectsRollbackAndUnsafeSamples(t *testing.T) {
	fixture := newExecutionFixture(t)
	engine := fixture.engine(t)
	limit := fixture.profile.ClockUncertaintyLimit()
	if err := engine.checkClock(limit); err != nil {
		t.Fatal(err)
	}
	previousMonotonic := uint64(fixture.now.UnixNano())
	fixture.now = fixture.now.Add(time.Second)
	engine.clock = func() (clockcheck.Sample, error) {
		return clockcheck.Sample{
			WallTime:         fixture.now,
			BootID:           "11111111-1111-1111-1111-111111111111",
			MonotonicNanos:   previousMonotonic,
			OffsetNanos:      int64(time.Millisecond),
			UncertaintyNanos: uint64(time.Millisecond),
		}, nil
	}
	if err := engine.checkClock(limit); err == nil {
		t.Fatal("monotonic rollback was accepted")
	}
	unsafe := clockcheck.Sample{
		WallTime:         fixture.now,
		BootID:           "11111111-1111-1111-1111-111111111111",
		MonotonicNanos:   previousMonotonic + uint64(time.Second),
		OffsetNanos:      int64(clockcheck.MaxOffset) + 1,
		UncertaintyNanos: uint64(time.Millisecond),
	}
	if err := validateClockSample(unsafe, fixture.now, limit); err == nil {
		t.Fatal("excessive clock offset was accepted")
	}
}

func TestClockGateCoalescesJournalSamples(t *testing.T) {
	fixture := newExecutionFixture(t)
	engine := fixture.engine(t)
	limit := fixture.profile.ClockUncertaintyLimit()
	if err := engine.checkClock(limit); err != nil {
		t.Fatal(err)
	}
	recordCount := len(fixture.store.Records())
	fixture.now = fixture.now.Add(time.Second)
	if err := engine.checkClock(limit); err != nil {
		t.Fatal(err)
	}
	if len(fixture.store.Records()) != recordCount {
		t.Fatal("clock sample inside the journal interval was persisted")
	}
	fixture.now = fixture.now.Add(minClockJournalInterval)
	if err := engine.checkClock(limit); err != nil {
		t.Fatal(err)
	}
	if len(fixture.store.Records()) != recordCount+1 {
		t.Fatal("clock sample beyond the journal interval was not persisted")
	}
}

func TestClockGateRejectsRollbackAcrossBoots(t *testing.T) {
	fixture := newExecutionFixture(t)
	engine := fixture.engine(t)
	limit := fixture.profile.ClockUncertaintyLimit()
	if err := engine.checkClock(limit); err != nil {
		t.Fatal(err)
	}
	previousWall := fixture.now
	fixture.now = fixture.now.Add(500 * time.Millisecond)
	engine.clock = func() (clockcheck.Sample, error) {
		return clockcheck.Sample{
			WallTime:         previousWall.Add(-500 * time.Millisecond),
			BootID:           "22222222-2222-2222-2222-222222222222",
			MonotonicNanos:   uint64(time.Second),
			OffsetNanos:      int64(time.Millisecond),
			UncertaintyNanos: uint64(time.Millisecond),
		}, nil
	}
	if err := engine.checkClock(limit); err == nil {
		t.Fatal("wall-clock rollback across a reboot was accepted")
	}
}

func TestUTCRolloverClockGuard(t *testing.T) {
	day := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	maxUncertainty := 100 * time.Millisecond
	margin := clockcheck.MaxOffset + maxUncertainty
	tests := []struct {
		at   time.Time
		safe bool
	}{
		{at: day, safe: false},
		{at: day.Add(margin - time.Nanosecond), safe: false},
		{at: day.Add(margin), safe: true},
		{at: day.Add(24*time.Hour - margin - time.Nanosecond), safe: true},
		{at: day.Add(24*time.Hour - margin), safe: false},
	}
	for _, test := range tests {
		if got := safeForUTCRollover(test.at, maxUncertainty); got != test.safe {
			t.Fatalf("safeForUTCRollover(%s) = %t", test.at, got)
		}
	}
}

func TestNoNewActionsBlocksRiskButAllowsReconciliation(t *testing.T) {
	t.Run("blocks a new action", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		engine := fixture.engine(t)
		engine.stop = &fakeStopChecker{stopped: true}
		result, err := engine.RunOnce(t.Context(), fixture.profile)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "stopped" || fixture.signer.calls != 0 || fixture.tx.submitCalls != 0 {
			t.Fatalf("stopped result = %+v", result)
		}
	})
	t.Run("does not strand an already submitted transaction", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.tx.reconcile = []txflow.Reconciliation{
			{Verdict: txflow.VerdictPending},
			{Verdict: txflow.VerdictFinalized, Slot: 120},
		}
		engine := fixture.engine(t)
		stop := &fakeStopChecker{}
		engine.stop = stop
		first, err := engine.RunOnce(t.Context(), fixture.profile)
		if err != nil {
			t.Fatal(err)
		}
		stop.stopped = true
		second, err := engine.RunOnce(t.Context(), fixture.profile)
		if err != nil {
			t.Fatal(err)
		}
		if first.Verdict != txflow.VerdictPending || second.Verdict != txflow.VerdictFinalized ||
			fixture.tx.submitCalls != 1 {
			t.Fatalf("reconciliation under stop failed: first=%+v second=%+v", first, second)
		}
	})
}

func TestPendingActionRecoversBeforeNewObservation(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.tx.reconcile = []txflow.Reconciliation{
		{Verdict: txflow.VerdictPending},
		{Verdict: txflow.VerdictFinalized, Slot: 121},
	}
	engine := fixture.engine(t)
	first, err := engine.RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	pendingStats, err := fixture.store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if pendingStats.ReservedBytes == 0 {
		t.Fatal("pending execution did not retain its journal reservation")
	}
	fixture.observer.observation.Slot++
	second, err := engine.RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if first.Verdict != txflow.VerdictPending || second.Verdict != txflow.VerdictFinalized ||
		first.ActionID != second.ActionID || fixture.observer.calls != 3 || fixture.tx.submitCalls != 1 {
		t.Fatalf("pending recovery failed: first=%+v second=%+v observer=%d submit=%d",
			first, second, fixture.observer.calls, fixture.tx.submitCalls)
	}
	stats, err := fixture.store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.ReservedBytes != 0 {
		t.Fatalf("reconciled execution retained %d reserved bytes", stats.ReservedBytes)
	}
	if first.PendingSinceUnix != fixture.now.Unix() ||
		first.ReconciliationTimeoutSeconds != fixture.profile.MaxReconciliationSeconds ||
		second.PendingSinceUnix != 0 || second.ReconciliationTimeoutSeconds != 0 {
		t.Fatalf("pending telemetry did not follow durable lifecycle: first=%+v second=%+v", first, second)
	}
}

func TestFinalizedTransferWaitsForCausallyNewMithrilEvidence(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.profile.ReserveLamports = 895
	fixture.profile.MinTransferLamports = 1
	fixture.profile.MaxTransferLamports = 100
	fixture.profile.DailyCapLamports = 220
	fixture.signer.policy.MaxLamports = 100
	fixture.syncSignerPolicy(t)
	fixture.tx.reconcile = []txflow.Reconciliation{{Verdict: txflow.VerdictFinalized, Slot: 150}}
	engine := fixture.engine(t)
	if _, err := engine.RunOnce(t.Context(), fixture.profile); err != nil {
		t.Fatal(err)
	}
	fixture.observer.observation.Slot = 101
	result, err := engine.RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != "waiting" || fixture.tx.submitCalls != 1 || fixture.signer.calls != 1 {
		t.Fatalf("stale post-transfer evidence was reused: %+v submit=%d sign=%d",
			result, fixture.tx.submitCalls, fixture.signer.calls)
	}
	fixture.observer.observation.Slot = 151
	fixture.tx.primaryBalance = fixture.profile.ReserveLamports
	fixture.tx.secondaryBalance = fixture.profile.ReserveLamports
	fixture.now = fixture.now.Add(5 * time.Second)
	fixture.observer.observation.ObservedAt = fixture.now
	fixture.observer.health.ObservedAt = fixture.now
	result, err = engine.RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != "complete" || !result.Recovered ||
		fixture.tx.submitCalls != 1 || fixture.signer.calls != 1 {
		t.Fatalf("schedule window repeated a completed transfer: %+v submit=%d sign=%d",
			result, fixture.tx.submitCalls, fixture.signer.calls)
	}
}

func TestFinalizedFailureWaitsForCausallyNewMithrilEvidence(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.tx.reconcile = []txflow.Reconciliation{{Verdict: txflow.VerdictFailed, Slot: 150}}
	engine := fixture.engine(t)
	first, err := engine.RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if first.Verdict != txflow.VerdictFailed {
		t.Fatalf("failed execution result = %+v", first)
	}
	fixture.observer.observation.Slot = 101
	result, err := engine.RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != "waiting" ||
		fixture.tx.submitCalls != 1 || fixture.signer.calls != 1 {
		t.Fatalf(
			"stale post-failure evidence was reused: %+v submit=%d sign=%d",
			result,
			fixture.tx.submitCalls,
			fixture.signer.calls,
		)
	}
}

func TestUnresolvedTransactionLatchesExecutionHalt(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.tx.reconcile = []txflow.Reconciliation{{Verdict: txflow.VerdictUnresolved}}
	engine := fixture.engine(t)
	first, err := engine.RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if first.Decision != "halted" {
		t.Fatalf("first result = %+v", first)
	}
	fixture.observer.observation.Slot = 1_000
	if _, err := engine.RunOnce(t.Context(), fixture.profile); err == nil {
		t.Fatal("execution continued after an unresolved transaction")
	}
	if fixture.tx.submitCalls != 1 || fixture.signer.calls != 1 {
		t.Fatalf("halted execution performed another action: submit=%d sign=%d",
			fixture.tx.submitCalls, fixture.signer.calls)
	}
}

func TestEffectDivergenceLatchesExecutionHalt(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.tx.reconcile = []txflow.Reconciliation{{
		Verdict:        txflow.VerdictDiverged,
		Slot:           150,
		DivergenceKind: txflow.DivergenceEffects,
	}}
	engine := fixture.engine(t)
	first, err := engine.RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if first.Decision != "halted" {
		t.Fatalf("effect divergence result = %+v", first)
	}
	if _, err := engine.RunOnce(t.Context(), fixture.profile); err == nil {
		t.Fatal("execution continued after finalized effects diverged")
	}
	if fixture.tx.submitCalls != 1 || fixture.signer.calls != 1 {
		t.Fatalf("halted execution repeated risk: submit=%d sign=%d",
			fixture.tx.submitCalls, fixture.signer.calls)
	}
}

func TestSentTransactionRecoversAcrossProfileChange(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.tx.reconcile = []txflow.Reconciliation{
		{Verdict: txflow.VerdictPending},
		{Verdict: txflow.VerdictFinalized, Slot: 150},
	}
	engine := fixture.engine(t)
	if _, err := engine.RunOnce(t.Context(), fixture.profile); err != nil {
		t.Fatal(err)
	}
	otherSeed := sha256.Sum256([]byte("other destination"))
	fixture.profile.Destination = solana.Encode(
		ed25519.NewKeyFromSeed(otherSeed[:]).Public().(ed25519.PublicKey),
	)
	result, err := engine.RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != txflow.VerdictFinalized || fixture.tx.submitCalls != 1 {
		t.Fatalf("sent transaction was stranded by profile change: %+v", result)
	}
}

func TestRecoveredSendMarkerIsNeverSubmittedAgain(t *testing.T) {
	fixture := newExecutionFixture(t)
	proposalEngine, err := agent.NewEngine(fixture.store, func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := proposalEngine.Propose(fixture.profile, fixture.observer.observation)
	if err != nil {
		t.Fatal(err)
	}
	actionID := proposed.Proposal.ActionID
	executionStart := fixture.executionStart(proposed.Proposal)
	appendEvent(t, fixture.store, fixture.now, EventExecutionStarted, actionID, executionStart)
	message, err := solana.BuildTransferMessage(
		fixture.profile.Source,
		fixture.profile.Destination,
		fixture.blockhash.latest.Blockhash,
		proposed.Proposal.AmountLamports,
	)
	if err != nil {
		t.Fatal(err)
	}
	built := builtTransaction{
		MessageBase64:           base64.StdEncoding.EncodeToString(message),
		RecentBlockhash:         fixture.blockhash.latest.Blockhash,
		BlockhashContextSlot:    fixture.blockhash.latest.ContextSlot,
		ObservedBlockHeight:     fixture.blockhash.height,
		LastValidBlockHeight:    fixture.blockhash.latest.LastValidBlockHeight,
		FeeLamports:             fixture.tx.fee,
		FeeMinContextSlot:       fixture.blockhash.latest.ContextSlot,
		PrimaryFeeContextSlot:   fixture.blockhash.latest.ContextSlot,
		SecondaryFeeContextSlot: fixture.blockhash.latest.ContextSlot + 1,
	}
	appendEvent(t, fixture.store, fixture.now, EventTransactionBuilt, actionID, built)
	appendEvent(t, fixture.store, fixture.now, EventTransactionSimulated, actionID, txflow.SimulationEvidence{
		ProviderIdentity:        "mithril-node",
		MinContextSlot:          built.BlockhashContextSlot,
		ContextSlot:             built.BlockhashContextSlot,
		UnitsConsumed:           150,
		SourcePostLamports:      900,
		DestinationPostLamports: 50,
		LogsSHA256:              hex.EncodeToString(bytes.Repeat([]byte{1}, sha256.Size)),
		AccountsSHA256:          hex.EncodeToString(bytes.Repeat([]byte{2}, sha256.Size)),
	})
	fixture.authority.now = func() time.Time { return fixture.now }
	fixture.signer.now = func() time.Time { return fixture.now }
	request := signingRequest(proposed.Proposal, built)
	grant, err := fixture.authority.Authorize(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	appendEvent(t, fixture.store, fixture.now, EventPolicyGranted, actionID, grant)
	request.RiskGrant = grant
	response, err := fixture.signer.Sign(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	signed := signedTransaction{SignerResponse: response}
	appendEvent(t, fixture.store, fixture.now, EventTransactionSigned, actionID, signed)
	appendEvent(t, fixture.store, fixture.now, EventTransactionSendStarted, actionID, sendStarted{
		Signature:    response.Signature,
		AuthorizedAt: fixture.now,
		LocalObservation: agent.NodeObservation{
			Account: fixture.observer.observation,
			Health:  fixture.observer.health,
		},
		EffectiveBalanceLamports: executionStart.EffectiveBalanceLamports,
		AccountEvidence:          executionStart.AccountEvidence,
	})

	fixture.tx.genesisErr = errors.New("local node unavailable")
	fixture.tx.reconcile = []txflow.Reconciliation{{Verdict: txflow.VerdictFinalized, Slot: 122}}
	result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != txflow.VerdictFinalized || fixture.tx.submitCalls != 0 ||
		fixture.observer.calls != 0 || fixture.tx.evidenceGenesisCalls != 1 {
		t.Fatalf(
			"recovered send was not independently reconciled: %+v observer=%d submit=%d evidence_genesis=%d",
			result,
			fixture.observer.calls,
			fixture.tx.submitCalls,
			fixture.tx.evidenceGenesisCalls,
		)
	}
}

func TestCancellationDuringReconciliationRecoversWithoutResubmission(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.tx.waitForReconcileCancellation = true
	fixture.tx.reconcileStarted = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	resultChannel := make(chan error, 1)
	go func() {
		_, err := fixture.engine(t).RunOnce(ctx, fixture.profile)
		resultChannel <- err
	}()
	select {
	case <-fixture.tx.reconcileStarted:
	case <-time.After(time.Second):
		t.Fatal("execution did not reach reconciliation")
	}
	cancel()
	if err := <-resultChannel; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reconciliation error = %v", err)
	}
	if fixture.tx.submitCalls != 1 {
		t.Fatalf("submission calls after cancellation = %d", fixture.tx.submitCalls)
	}

	fixture.tx.waitForReconcileCancellation = false
	fixture.tx.reconcile = []txflow.Reconciliation{{
		Verdict: txflow.VerdictFinalized,
		Slot:    122,
	}}
	result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != txflow.VerdictFinalized || fixture.tx.submitCalls != 1 ||
		!result.Recovered {
		t.Fatalf("recovered reconciliation = %+v, submissions=%d", result, fixture.tx.submitCalls)
	}
}

func TestExecutionRejectsNonMCPEvidenceAndMainnet(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.observer.observation.EvidenceSource = "manual"
	if _, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile); err == nil {
		t.Fatal("manual evidence was accepted")
	}
	fixture.profile.Cluster = "mainnet-beta"
	if _, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile); err == nil {
		t.Fatal("mainnet execution was accepted")
	}
}

func TestExecutionDoesNotSignOrSubmitExpiredBlockhash(t *testing.T) {
	t.Run("before signing", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.tx.expired = []bool{true}
		result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "canceled" || fixture.signer.calls != 0 || fixture.tx.submitCalls != 0 {
			t.Fatalf("expired action = %+v, sign=%d submit=%d", result, fixture.signer.calls, fixture.tx.submitCalls)
		}
	})
	t.Run("before submission", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.tx.expired = []bool{false, true}
		result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "halted" ||
			result.Reason != "blockhash_expired_before_submission" ||
			fixture.signer.calls != 1 || fixture.tx.submitCalls != 0 {
			t.Fatalf("expired action = %+v, sign=%d submit=%d", result, fixture.signer.calls, fixture.tx.submitCalls)
		}
	})
	t.Run("after account refresh", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.tx.expired = []bool{false, false, true}
		result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "halted" ||
			result.Reason != "blockhash_expired_before_submission" ||
			fixture.signer.calls != 1 || fixture.tx.submitCalls != 0 ||
			fixture.tx.balanceCalls != 3 {
			t.Fatalf(
				"expired action = %+v, sign=%d submit=%d balances=%d",
				result,
				fixture.signer.calls,
				fixture.tx.submitCalls,
				fixture.tx.balanceCalls,
			)
		}
	})
}

func TestBlockhashRequestIsBoundToMithrilObservation(t *testing.T) {
	fixture := newExecutionFixture(t)
	var minContextSlot uint64
	fixture.blockhash.latestMinSlot = &minContextSlot
	fixture.tx.reconcile = []txflow.Reconciliation{{
		Verdict: txflow.VerdictFinalized,
		Slot:    150,
	}}
	if _, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile); err != nil {
		t.Fatal(err)
	}
	if minContextSlot != fixture.observer.observation.Slot {
		t.Fatalf(
			"blockhash min context slot = %d, want %d",
			minContextSlot,
			fixture.observer.observation.Slot,
		)
	}
}

func TestSimulationFailureSurvivesRestartWithoutSigning(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.tx.simulationErr = errors.New("simulation unavailable")
	if _, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile); err == nil {
		t.Fatal("simulation failure was accepted")
	}
	if fixture.signer.calls != 0 || fixture.tx.submitCalls != 0 {
		t.Fatal("execution advanced after simulation failure")
	}
	fixture.tx.simulationErr = nil
	fixture.tx.reconcile = []txflow.Reconciliation{{Verdict: txflow.VerdictFinalized, Slot: 120}}
	fixture.now = fixture.now.Add(time.Second)
	result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != txflow.VerdictFinalized || fixture.signer.calls != 1 ||
		fixture.tx.submitCalls != 1 || fixture.tx.simulationCalls != 2 {
		t.Fatalf("simulation recovery = %+v", result)
	}
}

func TestUnsignedRecoveryRenewsExpiredRiskGrant(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.profile.MaxObservationAgeSeconds = 120
	fixture.syncSignerPolicy(t)
	ledgerDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ledgerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.signer.policy.DailyDebitCapLamports = 100_000
	fixture.signer.policy.AuthorizationLedgerPath = filepath.Join(ledgerDir, "authorization.jsonl")
	fixture.signer.loseNextResponse = true

	if _, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile); err == nil {
		t.Fatal("initial signer failure was not returned")
	}
	if got := countExecutionEvents(fixture.store, EventPolicyGranted); got != 1 {
		t.Fatalf("initial policy grants = %d, want 1", got)
	}
	if got := countExecutionEvents(fixture.store, EventTransactionSigned); got != 0 {
		t.Fatalf("initial signed events = %d, want 0", got)
	}

	fixture.now = fixture.now.Add(31 * time.Second)
	fixture.observer.observation.ObservedAt = fixture.now
	fixture.observer.observation.Slot++
	fixture.observer.health.ObservedAt = fixture.now
	fixture.tx.reconcile = []txflow.Reconciliation{{
		Verdict: txflow.VerdictFinalized,
		Slot:    fixture.observer.observation.Slot + 1,
	}}

	result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != txflow.VerdictFinalized || fixture.authority.calls != 2 ||
		fixture.signer.calls != 2 || countExecutionEvents(fixture.store, EventPolicyGranted) != 2 ||
		countExecutionEvents(fixture.store, EventTransactionSigned) != 1 {
		t.Fatalf(
			"renewed recovery = %+v authority=%d signer=%d grants=%d signed=%d",
			result,
			fixture.authority.calls,
			fixture.signer.calls,
			countExecutionEvents(fixture.store, EventPolicyGranted),
			countExecutionEvents(fixture.store, EventTransactionSigned),
		)
	}
	authorizationStore, err := journal.Open(fixture.signer.policy.AuthorizationLedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(authorizationStore.Records()); got != 2 {
		_ = authorizationStore.Close()
		t.Fatalf("authorization records = %d, want header plus one reservation", got)
	}
	if err := authorizationStore.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestJournalRejectsEarlyReplacementRiskGrant(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.signer.err = errors.New("signer unavailable")
	if _, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile); err == nil {
		t.Fatal("initial signer failure was not returned")
	}
	state, _, err := fixture.engine(t).activeState()
	if err != nil {
		t.Fatal(err)
	}
	fixture.now = fixture.now.Add(time.Second)
	fixture.authority.now = func() time.Time { return fixture.now }
	replacement, err := fixture.authority.Authorize(
		t.Context(),
		signingRequest(state.proposal, *state.built),
	)
	if err != nil {
		t.Fatal(err)
	}
	appendEvent(
		t,
		fixture.store,
		fixture.now,
		EventPolicyGranted,
		state.proposal.ActionID,
		replacement,
	)
	if _, _, err := fixture.engine(t).activeState(); err == nil ||
		!strings.Contains(err.Error(), "replacement policy grant") {
		t.Fatalf("early replacement error = %v", err)
	}
}

func TestJournalVerifiesEveryReplacementRiskGrant(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.signer.err = errors.New("signer unavailable")
	if _, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile); err == nil {
		t.Fatal("initial signer failure was not returned")
	}
	state, _, err := fixture.engine(t).activeState()
	if err != nil {
		t.Fatal(err)
	}
	firstSignature := state.grant.SignatureBase64
	fixture.now = fixture.now.Add(31 * time.Second)
	request := signingRequest(state.proposal, *state.built)
	fixture.authority.now = func() time.Time { return fixture.now }
	replacement, err := fixture.authority.Authorize(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	appendEvent(
		t,
		fixture.store,
		fixture.now,
		EventPolicyGranted,
		state.proposal.ActionID,
		replacement,
	)
	fixture.authority.rejectSignature = firstSignature
	if _, _, err := fixture.engine(t).activeState(); err == nil ||
		!strings.Contains(err.Error(), "policy grant verification") {
		t.Fatalf("replacement verification error = %v", err)
	}
}

func countExecutionEvents(store *journal.Store, eventType string) int {
	count := 0
	for _, record := range store.Records() {
		if record.Type == eventType {
			count++
		}
	}
	return count
}

func TestObservationFailureResetsHealthGate(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.observer.err = errors.New("MCP unavailable")
	if _, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile); err == nil {
		t.Fatal("observer failure was accepted")
	}
	fixture.observer.err = nil
	for index := 0; index < 2; index++ {
		fixture.now = fixture.now.Add(5 * time.Second)
		fixture.observer.observation.Slot++
		fixture.observer.observation.ObservedAt = fixture.now
		fixture.observer.health.ObservedAt = fixture.now
		result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 && result.Decision != "waiting" {
			t.Fatalf("first healthy sample after failure authorized execution: %+v", result)
		}
		if index == 1 && result.Decision == "waiting" {
			t.Fatalf("second healthy sample did not recover the gate: %+v", result)
		}
	}
}

func TestExecutionCancelsUnsentProposalAfterUTCDayChanges(t *testing.T) {
	fixture := newExecutionFixture(t)
	proposalEngine, err := agent.NewEngine(fixture.store, func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := proposalEngine.Propose(fixture.profile, fixture.observer.observation)
	if err != nil {
		t.Fatal(err)
	}
	appendEvent(
		t,
		fixture.store,
		fixture.now,
		EventExecutionStarted,
		proposed.Proposal.ActionID,
		fixture.executionStart(proposed.Proposal),
	)
	fixture.now = fixture.now.Add(24 * time.Hour)
	result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != "canceled" || fixture.signer.calls != 0 || fixture.tx.submitCalls != 0 {
		t.Fatalf("cross-day proposal result = %+v", result)
	}
}

func TestExecutionCancelsUnsentProposalAfterScheduleWindow(t *testing.T) {
	fixture := newExecutionFixture(t)
	proposalEngine, err := agent.NewEngine(
		fixture.store,
		func() time.Time { return fixture.now },
	)
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := proposalEngine.Propose(fixture.profile, fixture.observer.observation)
	if err != nil {
		t.Fatal(err)
	}
	appendEvent(
		t,
		fixture.store,
		fixture.now,
		EventExecutionStarted,
		proposed.Proposal.ActionID,
		fixture.executionStart(proposed.Proposal),
	)
	fixture.now = fixture.now.Add(time.Hour)
	result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != "canceled" ||
		result.Reason != "schedule_window_expired_before_build" ||
		fixture.signer.calls != 0 || fixture.tx.submitCalls != 0 {
		t.Fatalf("expired schedule result = %+v", result)
	}
}

func TestExecutionCancelsWhenMithrilSlotIsTooFarBehind(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.profile.MaxNodeLagSlots = 5
	fixture.blockhash.latest.ContextSlot = fixture.observer.observation.Slot + 6
	result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != "canceled" || fixture.signer.calls != 0 || fixture.tx.submitCalls != 0 {
		t.Fatalf("lagged node action = %+v, sign=%d submit=%d",
			result, fixture.signer.calls, fixture.tx.submitCalls)
	}
}

func TestExecutionRequiresBoundedIndependentFeeEvidence(t *testing.T) {
	t.Run("below budget", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.profile.MaxFeeLamports = 6
		fixture.signer.policy.MaxFeeLamports = 6
		fixture.syncSignerPolicy(t)
		fixture.tx.reconcile = []txflow.Reconciliation{{Verdict: txflow.VerdictFinalized, Slot: 150}}
		result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
		if err != nil {
			t.Fatal(err)
		}
		if result.Verdict != txflow.VerdictFinalized ||
			fixture.signer.calls != 1 || fixture.tx.submitCalls != 1 {
			t.Fatalf("fee below budget result = %+v", result)
		}
	})
	t.Run("query failure", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.tx.feeErr = errors.New("provider unavailable")
		if _, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile); err == nil {
			t.Fatal("fee query failure was accepted")
		}
		if fixture.signer.calls != 0 || fixture.tx.submitCalls != 0 {
			t.Fatal("transaction advanced after fee query failure")
		}
	})
	t.Run("above profile", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.tx.fee = fixture.profile.MaxFeeLamports + 1
		result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "canceled" || fixture.signer.calls != 0 || fixture.tx.submitCalls != 0 {
			t.Fatalf("excessive fee result = %+v", result)
		}
	})
	t.Run("built evidence survives recovery", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.tx.reconcile = []txflow.Reconciliation{
			{Verdict: txflow.VerdictPending},
			{Verdict: txflow.VerdictFinalized, Slot: 150},
		}
		engine := fixture.engine(t)
		if _, err := engine.RunOnce(t.Context(), fixture.profile); err != nil {
			t.Fatal(err)
		}
		fixture.tx.feeErr = errors.New("must not be queried")
		if _, err := engine.RunOnce(t.Context(), fixture.profile); err != nil {
			t.Fatal(err)
		}
		if fixture.tx.feeCalls != 1 || fixture.tx.submitCalls != 1 {
			t.Fatalf("recovery requoted or resubmitted: fee=%d send=%d",
				fixture.tx.feeCalls, fixture.tx.submitCalls)
		}
	})
}

func TestExecutionBoundsLocalBalanceWithIndependentRPCs(t *testing.T) {
	t.Run("lower agreed provider balance limits transfer", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.tx.primaryBalance = 120
		fixture.tx.secondaryBalance = 120
		fixture.tx.reconcile = []txflow.Reconciliation{{Verdict: txflow.VerdictFinalized, Slot: 150}}

		result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
		if err != nil {
			t.Fatal(err)
		}
		if result.AmountLamports != 15 || result.Verdict != txflow.VerdictFinalized {
			t.Fatalf("bounded balance result = %+v", result)
		}
	})
	t.Run("fresh Mithril debit blocks submission", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		initial := agent.NodeObservation{
			Account: fixture.observer.observation,
			Health:  fixture.observer.health,
		}
		beforeSigning := initial
		beforeSubmission := initial
		beforeSubmission.Account.BalanceLamports = fixture.profile.ReserveLamports
		fixture.observer.responses = []agent.NodeObservation{
			initial,
			beforeSigning,
			beforeSubmission,
		}

		result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "halted" ||
			result.Reason != "balance_changed_before_submission" ||
			fixture.signer.calls != 1 || fixture.tx.submitCalls != 0 ||
			fixture.observer.calls != 3 {
			t.Fatalf(
				"fresh local debit result=%+v sign=%d submit=%d observations=%d",
				result,
				fixture.signer.calls,
				fixture.tx.submitCalls,
				fixture.observer.calls,
			)
		}
	})
	t.Run("provider failure stops before proposal", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.tx.balanceErr = errors.New("provider unavailable")
		recordCount := len(fixture.store.Records())

		if _, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile); err == nil {
			t.Fatal("missing independent balance evidence was accepted")
		}
		if len(fixture.store.Records()) != recordCount+1 ||
			fixture.signer.calls != 0 || fixture.tx.submitCalls != 0 {
			t.Fatal("execution advanced without independent balance evidence")
		}
	})
	t.Run("balance below reserve skips without signing", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.tx.secondaryBalance = fixture.profile.ReserveLamports

		result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "skipped" ||
			fixture.signer.calls != 0 || fixture.tx.submitCalls != 0 {
			t.Fatalf("low external balance result = %+v", result)
		}
	})
}

func TestExecutionRejectsInvalidPersistedBalanceEvidence(t *testing.T) {
	fixture := newExecutionFixture(t)
	proposalEngine, err := agent.NewEngine(fixture.store, func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := proposalEngine.Propose(fixture.profile, fixture.observer.observation)
	if err != nil {
		t.Fatal(err)
	}
	started := fixture.executionStart(proposed.Proposal)
	started.AccountEvidence.Source.SecondaryLamports = fixture.profile.ReserveLamports
	appendEvent(
		t,
		fixture.store,
		fixture.now,
		EventExecutionStarted,
		proposed.Proposal.ActionID,
		started,
	)

	if _, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile); err == nil {
		t.Fatal("inconsistent persisted balance evidence was accepted")
	}
	if fixture.signer.calls != 0 || fixture.tx.submitCalls != 0 {
		t.Fatal("execution advanced with invalid persisted balance evidence")
	}
}

func TestExecutionRejectsUnknownJournalEvents(t *testing.T) {
	fixture := newExecutionFixture(t)
	appendEvent(t, fixture.store, fixture.now, "execution.typo", "unknown", struct{}{})

	if _, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile); err == nil {
		t.Fatal("unknown journal event was accepted")
	}
	if fixture.observer.calls != 0 || fixture.signer.calls != 0 || fixture.tx.submitCalls != 0 {
		t.Fatal("execution advanced after an unknown journal event")
	}
}

func TestExecutionIgnoresShadowOnlyRecords(t *testing.T) {
	fixture := newExecutionFixture(t)
	shadow, err := agent.NewEngine(fixture.store, func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shadow.RunShadow(fixture.profile, fixture.observer.observation); err != nil {
		t.Fatal(err)
	}
	fixture.tx.reconcile = []txflow.Reconciliation{{Verdict: txflow.VerdictFinalized, Slot: 150}}

	result, err := fixture.engine(t).RunOnce(t.Context(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != txflow.VerdictFinalized ||
		fixture.signer.calls != 1 || fixture.tx.submitCalls != 1 {
		t.Fatalf("shadow records affected live execution: %+v", result)
	}
}

type executionFixture struct {
	now       time.Time
	monotonic uint64
	store     *journal.Store
	profile   agent.Profile
	observer  *fakeObserver
	blockhash fakeBlockhash
	authority *localPolicyAuthority
	signer    *localSigner
	tx        *fakeTransactor
}

func newExecutionFixture(t *testing.T) executionFixture {
	t.Helper()
	store, err := journal.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	seed := sha256.Sum256([]byte("source"))
	key := ed25519.NewKeyFromSeed(seed[:])
	source := solana.Encode(key.Public().(ed25519.PublicKey))
	destinationSeed := sha256.Sum256([]byte("destination"))
	destination := solana.Encode(ed25519.NewKeyFromSeed(destinationSeed[:]).Public().(ed25519.PublicKey))
	blockhash := solana.Encode(bytes.Repeat([]byte{6}, 32))
	profile := agent.Profile{
		Name:                         agent.ProfileTreasurySweepV1,
		Version:                      1,
		Cluster:                      "devnet",
		Source:                       source,
		Destination:                  destination,
		ReserveLamports:              100,
		MinTransferLamports:          10,
		MaxTransferLamports:          50,
		DailyCapLamports:             80,
		MaxFeeLamports:               5,
		ScheduleWindowSeconds:        3_600,
		ScheduleAnchorUnix:           time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC).Unix(),
		MaxClockUncertaintyMillis:    100,
		MaxObservationAgeSeconds:     30,
		MinHealthyObservationSeconds: 5,
		MinHealthySlotAdvance:        1,
		MaxNodeLagSlots:              150,
		MaxReconciliationSeconds:     180,
	}
	observation := agent.Observation{
		Cluster:         "devnet",
		Source:          source,
		BalanceLamports: 1000,
		Slot:            100,
		ObservedAt:      now,
		EvidenceSource:  "mithril_mcp",
		Finality:        "local_unfinalized",
		Consistency:     "node_reported_non_atomic",
	}
	health := agent.NodeHealth{
		Status:              "healthy",
		AssessmentScope:     "point_in_time_snapshot",
		ObservedAt:          now,
		EvidenceComplete:    true,
		DivergenceArtifacts: 0,
	}
	previous := agent.NodeObservation{Account: observation, Health: health}
	previous.Account.Slot--
	previous.Account.ObservedAt = now.Add(-5 * time.Second)
	previous.Health.ObservedAt = now.Add(-5 * time.Second)
	if _, err := store.Append(now.Add(-5*time.Second), EventNodeObserved, "", previous); err != nil {
		t.Fatal(err)
	}
	profileFingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	authoritySeed := sha256.Sum256([]byte("risk-authority"))
	authorityKey := ed25519.NewKeyFromSeed(authoritySeed[:])
	authorityPublic, err := riskgrant.PublicKeyHex(authorityKey)
	if err != nil {
		t.Fatal(err)
	}
	submitterSeed := sha256.Sum256([]byte("submitter"))
	submitterPrivateKey, submitterPublicKey, err := sealedtx.GenerateKey(
		bytes.NewReader(bytes.Repeat(submitterSeed[:], 2)),
	)
	if err != nil {
		t.Fatal(err)
	}
	signerLedgerDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(signerLedgerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := executionFixture{
		now:       now,
		monotonic: uint64(now.Add(-time.Second).UnixNano()),
		store:     store,
		profile:   profile,
		observer:  &fakeObserver{observation: observation, health: health},
		blockhash: fakeBlockhash{
			latest: solanarpc.LatestBlockhash{
				ContextSlot:          110,
				Blockhash:            blockhash,
				LastValidBlockHeight: 250,
			},
			height: 100,
		},
		authority: &localPolicyAuthority{
			key:   authorityKey,
			keyID: "test-risk-authority",
		},
		signer: &localSigner{
			policy: signer.Policy{
				Cluster:               "devnet",
				Profile:               agent.ProfileTreasurySweepV1,
				ProfileVersion:        1,
				ProfileFingerprint:    profileFingerprint,
				Source:                source,
				Destination:           destination,
				MaxLamports:           50,
				MaxFeeLamports:        5,
				DailyDebitCapLamports: 1_000_000,
				AuthorizationLedgerPath: filepath.Join(
					signerLedgerDir, "authorization.jsonl",
				),
				ScheduleWindowSeconds:  profile.ScheduleWindowSeconds,
				ScheduleAnchorUnix:     profile.ScheduleAnchorUnix,
				MaxBlockHeightWindow:   200,
				RiskAuthorityKeyID:     "test-risk-authority",
				RiskAuthorityPublicKey: authorityPublic,
				SubmitterPublicKey:     submitterPublicKey,
			},
			key: key,
		},
		tx: &fakeTransactor{
			submitterPrivateKey: submitterPrivateKey,
			fee:                 5,
			primaryBalance:      1_000,
			secondaryBalance:    1_000,
		},
	}
	return fixture
}

func (f *executionFixture) engine(t *testing.T) *Engine {
	t.Helper()
	f.authority.now = func() time.Time { return f.now }
	f.signer.now = func() time.Time { return f.now }
	engine, err := New(
		f.store,
		f.observer,
		f.blockhash,
		f.authority,
		f.signer,
		f.tx,
		f.tx,
		nil,
		func() time.Time { return f.now },
	)
	if err != nil {
		t.Fatal(err)
	}
	engine.clock = func() (clockcheck.Sample, error) {
		wallNanos := uint64(f.now.UnixNano())
		if wallNanos > f.monotonic {
			f.monotonic = wallNanos
		} else {
			f.monotonic += uint64(time.Millisecond)
		}
		return clockcheck.Sample{
			WallTime:         f.now,
			BootID:           "11111111-1111-1111-1111-111111111111",
			MonotonicNanos:   f.monotonic,
			OffsetNanos:      int64(time.Millisecond),
			UncertaintyNanos: uint64(time.Millisecond),
		}, nil
	}
	return engine
}

func (f *executionFixture) syncSignerPolicy(t *testing.T) {
	t.Helper()
	fingerprint, err := f.profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	f.signer.policy.ProfileFingerprint = fingerprint
	f.signer.policy.ScheduleWindowSeconds = f.profile.ScheduleWindowSeconds
	f.signer.policy.ScheduleAnchorUnix = f.profile.ScheduleAnchorUnix
}

func (f executionFixture) executionStart(proposal agent.Proposal) executionStarted {
	return executionStarted{
		Mode:                     "devnet",
		LocalAvailableLamports:   proposal.ObservedBalanceLamports,
		EffectiveBalanceLamports: proposal.ObservedBalanceLamports,
		AccountEvidence: txflow.TransferAccountEvidence{
			ObservationSlot:        proposal.ObservationSlot,
			CommonFinalizedFloor:   proposal.ObservationSlot,
			PrimaryFinalizedSlot:   proposal.ObservationSlot,
			SecondaryFinalizedSlot: proposal.ObservationSlot + 1,
			Source: txflow.AccountEvidence{
				Address:              proposal.Source,
				PrimaryContextSlot:   proposal.ObservationSlot,
				PrimaryLamports:      proposal.ObservedBalanceLamports,
				PrimaryOwner:         solana.Encode(make([]byte, 32)),
				SecondaryContextSlot: proposal.ObservationSlot + 1,
				SecondaryLamports:    proposal.ObservedBalanceLamports,
				SecondaryOwner:       solana.Encode(make([]byte, 32)),
			},
			Destination: txflow.AccountEvidence{
				Address:              proposal.Destination,
				PrimaryContextSlot:   proposal.ObservationSlot,
				PrimaryLamports:      1,
				PrimaryOwner:         solana.Encode(make([]byte, 32)),
				SecondaryContextSlot: proposal.ObservationSlot + 1,
				SecondaryLamports:    1,
				SecondaryOwner:       solana.Encode(make([]byte, 32)),
			},
		},
		Health: f.observer.health,
	}
}

func appendEvent(t *testing.T, store *journal.Store, at time.Time, event, actionID string, payload any) {
	t.Helper()
	if _, err := store.Append(at, event, actionID, payload); err != nil {
		t.Fatal(err)
	}
}
