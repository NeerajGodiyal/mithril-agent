package submitter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

type jupiterFinalizedReader struct {
	identity string
	slot     uint64
	calls    int
}

type jupiterSubmitNode struct {
	identity        string
	returned        string
	sendErr         error
	sendCalls       int
	lastContextSlot uint64
	transactions    [][]byte
}

func (r *jupiterFinalizedReader) Identity() string { return r.identity }

func (r *jupiterFinalizedReader) FinalizedSlot(context.Context) (uint64, error) {
	r.calls++
	return r.slot, nil
}

type jupiterReadinessEvidence struct {
	nodeIdentity      string
	primaryIdentity   string
	secondaryIdentity string
	nodeHeight        uint64
	failStage         string
	failStageCall     int
	calls             map[string]int
}

func newJupiterReadinessEvidence(policy Policy) *jupiterReadinessEvidence {
	return &jupiterReadinessEvidence{
		nodeIdentity:      strings.Repeat("3", 64),
		primaryIdentity:   policy.Evidence.PrimaryOriginSHA256,
		secondaryIdentity: policy.Evidence.SecondaryOriginSHA256,
		nodeHeight:        150,
		calls:             make(map[string]int),
	}
}

func (e *jupiterReadinessEvidence) check(stage string) error {
	e.calls[stage]++
	if e.failStage == stage && (e.failStageCall == 0 || e.calls[stage] == e.failStageCall) {
		return errors.New("injected readiness failure")
	}
	return nil
}

func (e *jupiterReadinessEvidence) MithrilNodeIdentity() string { return e.nodeIdentity }

func (e *jupiterReadinessEvidence) EvidenceProviderIdentities() (string, string) {
	return e.primaryIdentity, e.secondaryIdentity
}

func (e *jupiterReadinessEvidence) VerifyGenesis(context.Context, string) error {
	return e.check("genesis")
}

func (e *jupiterReadinessEvidence) VerifyFinalizedV0History(context.Context, string) error {
	return e.check("archive")
}

func (e *jupiterReadinessEvidence) VerifyUpgradeableProgramDeployment(
	context.Context, string, string, string, uint64, uint64,
) error {
	return e.check("deployment")
}

func (e *jupiterReadinessEvidence) VerifyImmutableProgramDeployment(
	context.Context, string, string, uint64, uint64, string, uint64,
) error {
	return e.check("immutable_deployment")
}

func (e *jupiterReadinessEvidence) VerifyAddressLookupTables(
	_ context.Context,
	tables map[[32]byte][][32]byte,
	_ uint64,
) (map[[32]byte][][32]byte, error) {
	if err := e.check("address_tables"); err != nil {
		return nil, err
	}
	return tables, nil
}

func (e *jupiterReadinessEvidence) FeeForV0Message(
	_ context.Context,
	_ []byte,
	_ map[[32]byte][][32]byte,
	_ string,
	minContextSlot uint64,
) (txflow.FeeEvidence, error) {
	if err := e.check("fee"); err != nil {
		return txflow.FeeEvidence{}, err
	}
	return txflow.FeeEvidence{
		Lamports: 5_000, MinContextSlot: minContextSlot,
		PrimaryContextSlot: minContextSlot, SecondaryContextSlot: minContextSlot,
	}, nil
}

func (e *jupiterReadinessEvidence) SimulateV0(
	_ context.Context,
	_ []byte,
	_ map[[32]byte][][32]byte,
	_ string,
	minContextSlot uint64,
) (txflow.LegacySimulationEvidence, error) {
	if err := e.check("simulation"); err != nil {
		return txflow.LegacySimulationEvidence{}, err
	}
	return txflow.LegacySimulationEvidence{
		ProviderIdentity: e.nodeIdentity, MinContextSlot: minContextSlot,
		ContextSlot: minContextSlot, UnitsConsumed: 50_000,
		LogsSHA256: strings.Repeat("a", 64),
	}, nil
}

func (e *jupiterReadinessEvidence) NodeBlockHeight(context.Context, uint64) (uint64, error) {
	if err := e.check("node_height"); err != nil {
		return 0, err
	}
	return e.nodeHeight, nil
}

func (e *jupiterReadinessEvidence) VerifyTokenInputAccount(
	context.Context, string, string, string, uint64, uint64,
) (txflow.TokenAccountEvidence, error) {
	if err := e.check("token_input"); err != nil {
		return txflow.TokenAccountEvidence{}, err
	}
	return txflow.TokenAccountEvidence{}, nil
}

func (e *jupiterReadinessEvidence) VerifyTokenOutputAccount(
	context.Context, string, string, string, uint64,
) (txflow.TokenAccountEvidence, error) {
	if err := e.check("token_output"); err != nil {
		return txflow.TokenAccountEvidence{}, err
	}
	return txflow.TokenAccountEvidence{}, nil
}

func (e *jupiterReadinessEvidence) VerifyTokenAccountRent(
	context.Context, uint64,
) (txflow.RentEvidence, error) {
	if err := e.check("rent"); err != nil {
		return txflow.RentEvidence{}, err
	}
	return txflow.RentEvidence{
		Lamports: 2_039_280, PrimaryLamports: 2_039_280, SecondaryLamports: 2_039_280,
	}, nil
}

func (e *jupiterReadinessEvidence) VerifyIndependentBlockhashValidity(
	context.Context, string, uint64,
) error {
	return e.check("blockhash")
}

func (n *jupiterSubmitNode) Identity() string { return n.identity }

func (n *jupiterSubmitNode) SendTransaction(
	_ context.Context,
	transaction []byte,
	minContextSlot uint64,
) (string, error) {
	n.sendCalls++
	n.lastContextSlot = minContextSlot
	n.transactions = append(n.transactions, bytes.Clone(transaction))
	return n.returned, n.sendErr
}

func jupiterRecoveryNow(request signer.Request) func() time.Time {
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	return func() time.Time { return now }
}

func TestJupiterRecoveryPersistsAndReconcilesExactV0Evidence(t *testing.T) {
	policy, privateKey, request, response := jupiterSubmitterFixture(t)
	recoveryDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(recoveryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy.ControlStatePath = filepath.Join(recoveryDir, "control.json")
	request.RiskGrant = riskgrant.Grant{
		Claims:          riskgrant.Claims{Version: riskgrant.Version},
		SignatureBase64: "not-persisted",
	}
	if err := PrepareJupiterRecovery(policy, privateKey, request, response); err != nil {
		t.Fatal(err)
	}
	if err := PrepareJupiterRecovery(policy, privateKey, request, response); err != nil {
		t.Fatalf("exact Jupiter recovery retry was not idempotent: %v", err)
	}
	unsigned := request
	unsigned.RiskGrant = riskgrant.Grant{}
	if got, err := ReadJupiterFinalizedEvidence(policy, unsigned); err == nil || got != (JupiterFinalizedEvidence{}) {
		t.Fatalf("pending preparation exposed finality: %+v, %v", got, err)
	}
	transaction, err := sealedtx.OpenConfidential(privateKey, response.SealedTransaction)
	if err != nil {
		t.Fatal(err)
	}
	record, persisted, decoded, err := readRecovery(policy)
	if err != nil {
		t.Fatal(err)
	}
	if record.Version != jupiterRecoveryVersion || record.JupiterRequest == nil ||
		record.JupiterRequest.RiskGrant != (riskgrant.Grant{}) || record.Finalized ||
		record.SendStarted || record.SendAttempts != 0 ||
		!bytes.Equal(persisted, transaction) || decoded.jupiter == nil ||
		decoded.jupiter.TransactionSHA256 != response.TransactionSHA256 {
		t.Fatalf("Jupiter recovery evidence = %+v", record)
	}
	record.Version = legacyJupiterRecoveryVersion
	if err := writeRecovery(policy, record); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(recoveryPath(policy))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("Jupiter recovery permissions = %v, %v", info, err)
	}

	status := solanarpc.SignatureStatus{
		Found: true, Slot: 150, ConfirmationStatus: "finalized",
	}
	effect := jupiterRecoveryEffect(t, policy, *record.JupiterRequest, transaction, 150)
	primary := &recoveryEvidence{
		identity: policy.Evidence.PrimaryOriginSHA256, status: status, effect: effect,
	}
	secondary := &recoveryEvidence{
		identity: policy.Evidence.SecondaryOriginSHA256, status: status, effect: effect,
	}
	lifecycle, err := txflow.NewEvidenceLifecycle(primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	markJupiterSendStarted(t, policy)
	actionID, result, err := ReconcileRecovery(t.Context(), policy, lifecycle)
	if err != nil || actionID != response.ActionID || result.Verdict != txflow.VerdictFinalized ||
		result.JupiterEffects == nil || result.JupiterEffects.OutputAmount != 20 {
		t.Fatalf("Jupiter recovery result = %q, %+v, %v", actionID, result, err)
	}
	record, _, _, err = readRecovery(policy)
	if err != nil || record.Version != jupiterRecoveryVersion || !record.Finalized ||
		record.Reconciliation == nil ||
		record.Reconciliation.Verdict != txflow.VerdictFinalized ||
		record.Reconciliation.JupiterEffects == nil ||
		record.Reconciliation.JupiterEffects.OutputAmount != 20 {
		t.Fatalf("finalized Jupiter recovery was not durable: %+v, %v", record, err)
	}
	active, err := securefile.ReadPrivate(recoveryPath(policy), maxRecoveryBytes)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := securefile.ReadPrivate(
		finalizedRecoveryPath(policy, record.ActionID), maxRecoveryBytes,
	)
	if err != nil || !bytes.Equal(active, archived) {
		t.Fatalf("finalized recovery archive mismatch: %v", err)
	}
	statusResult, err := ReadJupiterRecoveryStatus(policy)
	if err != nil || statusResult.Format != jupiterRecoveryStatus ||
		statusResult.FinalizedVerdict != txflow.VerdictFinalized {
		t.Fatalf("finalized recovery status = %+v, %v", statusResult, err)
	}
	t.Run("read-only finalized projection", func(t *testing.T) {
		checkJupiterFinalizedProjection(t, policy, unsigned, response, active)
	})
	if err := prepareJupiterRecovery(policy, request, response, transaction); err == nil {
		t.Fatal("finalized Jupiter action was reopened for submission")
	}
	if err := os.Remove(recoveryPath(policy)); err != nil {
		t.Fatal(err)
	}
	if err := prepareJupiterRecovery(policy, request, response, transaction); err == nil {
		t.Fatal("archived finalized Jupiter action was reopened for submission")
	}
}

func checkJupiterFinalizedProjection(t *testing.T, policy Policy, request signer.Request, response signer.Response, original []byte) {
	t.Helper()
	got, err := ReadJupiterFinalizedEvidence(policy, request)
	if err != nil || got.ActionID != request.ActionID || got.RequestSHA256 != response.RequestSHA256 ||
		got.TransactionSHA256 != response.TransactionSHA256 || got.Verdict != txflow.VerdictFinalized ||
		got.FinalizedSlot != 150 || got.PrimaryEffectSlot != 150 || got.SecondaryEffectSlot != 150 ||
		got.InputMint != policy.Jupiter.InputMint || got.OutputMint != policy.Jupiter.OutputMint ||
		got.InputSpent != request.JupiterCandidate.Request.InputAmount || got.OutputReceived != 20 ||
		got.FeeLamports != response.FeeLamports {
		t.Fatalf("finalized projection = %+v, %v", got, err)
	}
	encoded, err := json.Marshal(got)
	if err != nil || bytes.Contains(encoded, []byte("transaction_base64")) || bytes.Contains(encoded, []byte("signature")) ||
		!bytes.Contains(encoded, []byte(`"input_spent":"`)) || !bytes.Contains(encoded, []byte(`"fee_lamports":"`)) {
		t.Fatalf("projection exposed material or lossy amounts: %s, %v", encoded, err)
	}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"action", "window", "fee context", "amount", "provider", "grant"} {
		t.Run(name, func(t *testing.T) {
			var changed signer.Request
			if err := json.Unmarshal(requestBytes, &changed); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "action":
				changed.ActionID = strings.Repeat("a", 64)
			case "window":
				changed.ScheduleWindowEndUnix++
			case "fee context":
				changed.PrimaryFeeContextSlot++
			case "amount":
				changed.JupiterCandidate.Request.InputAmount++
			case "provider":
				changed.JupiterProviders.PrimaryTrustDomain = "different-provider"
			case "grant":
				changed.RiskGrant.Claims.Version = riskgrant.Version
			}
			if got, err := ReadJupiterFinalizedEvidence(policy, changed); err == nil || got != (JupiterFinalizedEvidence{}) {
				t.Fatalf("changed unsigned request exposed evidence: %+v, %v", got, err)
			}
		})
	}
	for _, name := range []string{"legacy", "pending", "unresolved"} {
		t.Run(name, func(t *testing.T) {
			var record recoveryRecord
			if err := json.Unmarshal(original, &record); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "legacy":
				record.Version = legacyJupiterRecoveryVersion
				record.Reconciliation = nil
			case "pending":
				record.Finalized = false
				record.Reconciliation = nil
			case "unresolved":
				record.Reconciliation.Verdict = txflow.VerdictUnresolved
			}
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := securefile.ReplacePrivate(recoveryPath(policy), data, maxRecoveryBytes); err != nil {
				t.Fatal(err)
			}
			if got, err := ReadJupiterFinalizedEvidence(policy, request); err == nil || got != (JupiterFinalizedEvidence{}) {
				t.Fatalf("nonterminal/legacy record exposed evidence: %+v, %v", got, err)
			}
			after, err := securefile.ReadPrivate(recoveryPath(policy), maxRecoveryBytes)
			if err != nil || !bytes.Equal(data, after) {
				t.Fatalf("projection changed recovery: %v", err)
			}
		})
	}
	if err := securefile.ReplacePrivate(recoveryPath(policy), original, maxRecoveryBytes); err != nil {
		t.Fatal(err)
	}
	again, err := ReadJupiterFinalizedEvidence(policy, request)
	if err != nil || again != got {
		t.Fatalf("reopened projection changed: %+v, %v", again, err)
	}
	after, err := securefile.ReadPrivate(recoveryPath(policy), maxRecoveryBytes)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatalf("projection rewrote recovered record: %v", err)
	}
}

func TestJupiterRecoveryRejectsTamperedDurableFinality(t *testing.T) {
	policy, privateKey, request, response := jupiterSubmitterFixture(t)
	recoveryDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(recoveryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy.ControlStatePath = filepath.Join(recoveryDir, "control.json")
	transaction, err := sealedtx.OpenConfidential(privateKey, response.SealedTransaction)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareJupiterRecovery(policy, request, response, transaction); err != nil {
		t.Fatal(err)
	}
	status := solanarpc.SignatureStatus{
		Found: true, Slot: 150, ConfirmationStatus: "finalized",
	}
	effect := jupiterRecoveryEffect(t, policy, request, transaction, 150)
	primary := &recoveryEvidence{
		identity: policy.Evidence.PrimaryOriginSHA256, status: status, effect: effect,
	}
	secondary := &recoveryEvidence{
		identity: policy.Evidence.SecondaryOriginSHA256, status: status, effect: effect,
	}
	lifecycle, err := txflow.NewEvidenceLifecycle(primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	markJupiterSendStarted(t, policy)
	if _, _, err := ReconcileRecovery(t.Context(), policy, lifecycle); err != nil {
		t.Fatal(err)
	}
	record, transaction, _, err := readRecovery(policy)
	clear(transaction)
	if err != nil || record.Reconciliation == nil || record.Reconciliation.JupiterEffects == nil {
		t.Fatalf("finalized recovery = %+v, %v", record, err)
	}
	original, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*recoveryRecord){
		"effects": func(value *recoveryRecord) {
			value.Reconciliation.JupiterEffects.OutputAmount = 0
		},
		"success error fingerprint": func(value *recoveryRecord) {
			value.Reconciliation.PrimaryErrorFingerprint = strings.Repeat("a", 64)
			value.Reconciliation.SecondaryErrorFingerprint = strings.Repeat("a", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			var changed recoveryRecord
			if err := json.Unmarshal(original, &changed); err != nil {
				t.Fatal(err)
			}
			mutate(&changed)
			data, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if err := securefile.ReplacePrivate(
				recoveryPath(policy), data, maxRecoveryBytes,
			); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := readRecovery(policy); err == nil {
				t.Fatal("tampered finalized recovery evidence was accepted")
			}
			request.RiskGrant = riskgrant.Grant{}
			if got, err := ReadJupiterFinalizedEvidence(policy, request); err == nil || got != (JupiterFinalizedEvidence{}) {
				t.Fatalf("tampered effects exposed finality: %+v, %v", got, err)
			}
		})
	}
}

func TestJupiterRecoveryPersistsFinalizedFailureEvidence(t *testing.T) {
	policy, privateKey, request, response := jupiterSubmitterFixture(t)
	recoveryDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(recoveryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy.ControlStatePath = filepath.Join(recoveryDir, "control.json")
	transaction, err := sealedtx.OpenConfidential(privateKey, response.SealedTransaction)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareJupiterRecovery(policy, request, response, transaction); err != nil {
		t.Fatal(err)
	}
	status := solanarpc.SignatureStatus{
		Found: true, Slot: 150, ConfirmationStatus: "finalized",
		Failed: true, ErrorFingerprint: strings.Repeat("e", 64),
	}
	effect := jupiterRecoveryEffect(t, policy, request, transaction, 150)
	effect.Failed = true
	effect.ErrorFingerprint = status.ErrorFingerprint
	effect.PostBalances = append([]uint64(nil), effect.PreBalances...)
	effect.PostBalances[0] -= response.FeeLamports
	effect.PostTokenBalances = append([]solanarpc.TokenBalance(nil), effect.PreTokenBalances...)
	primary := &recoveryEvidence{
		identity: policy.Evidence.PrimaryOriginSHA256, status: status, effect: effect,
	}
	secondary := &recoveryEvidence{
		identity: policy.Evidence.SecondaryOriginSHA256, status: status, effect: effect,
	}
	lifecycle, err := txflow.NewEvidenceLifecycle(primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	markJupiterSendStarted(t, policy)
	_, result, err := ReconcileRecovery(t.Context(), policy, lifecycle)
	if err != nil || result.Verdict != txflow.VerdictFailed {
		t.Fatalf("failed recovery = %+v, %v", result, err)
	}
	record, transaction, _, err := readRecovery(policy)
	clear(transaction)
	if err != nil || record.Reconciliation == nil ||
		record.Reconciliation.Verdict != txflow.VerdictFailed ||
		record.Reconciliation.JupiterEffects == nil ||
		record.Reconciliation.JupiterEffects.OutputAmount != 0 {
		t.Fatalf("durable failed recovery = %+v, %v", record, err)
	}
	request.RiskGrant = riskgrant.Grant{}
	projected, err := ReadJupiterFinalizedEvidence(policy, request)
	if err != nil || projected.Verdict != txflow.VerdictFailed || projected.InputSpent != 0 ||
		projected.OutputReceived != 0 || projected.OutputAccountRent != 0 || projected.FeeLamports != response.FeeLamports {
		t.Fatalf("finalized failure reported a traded input/output or wrong fee: %+v, %v", projected, err)
	}
	record.Reconciliation.PrimaryErrorFingerprint = ""
	record.Reconciliation.SecondaryErrorFingerprint = ""
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := securefile.ReplacePrivate(recoveryPath(policy), data, maxRecoveryBytes); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readRecovery(policy); err == nil {
		t.Fatal("failed recovery without an error fingerprint was accepted")
	}
}

func TestJupiterRecoveryReadinessIsReadOnlyAndRequiresFreshIndependentEvidence(t *testing.T) {
	policy, privateKey, request, response := jupiterSubmitterFixture(t)
	recoveryDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(recoveryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy.ControlStatePath = filepath.Join(recoveryDir, "control.json")
	if err := PrepareJupiterRecovery(policy, privateKey, request, response); err != nil {
		t.Fatal(err)
	}
	primary := &jupiterFinalizedReader{
		identity: policy.Evidence.PrimaryOriginSHA256, slot: 110,
	}
	secondary := &jupiterFinalizedReader{
		identity: policy.Evidence.SecondaryOriginSHA256, slot: 111,
	}
	evidence := newJupiterReadinessEvidence(policy)

	clock := jupiterRecoveryNow(request)
	actionID, err := checkJupiterRecoveryReadinessAt(
		t.Context(), policy, evidence, primary, secondary, false, clock,
	)
	if err != nil || actionID != response.ActionID {
		t.Fatalf("readiness = %q, %v", actionID, err)
	}
	for _, stage := range []string{
		"genesis", "archive", "deployment", "immutable_deployment", "token_output", "rent", "fee",
		"simulation", "node_height", "blockhash",
	} {
		if evidence.calls[stage] != 1 {
			t.Fatalf("readiness stage %q calls = %d", stage, evidence.calls[stage])
		}
	}
	if primary.calls != 1 || secondary.calls != 1 {
		t.Fatalf("finalized slot calls = %d/%d", primary.calls, secondary.calls)
	}
	expired := func() time.Time {
		return time.Unix(request.ScheduleWindowEndUnix, 0).UTC()
	}
	if _, err := checkJupiterRecoveryReadinessAt(
		t.Context(), policy, evidence, primary, secondary, false, expired,
	); err == nil || err.Error() != "Mainnet proposal approval window is not currently valid" {
		t.Fatalf("expired approval readiness = %v", err)
	}
	if primary.calls != 1 || secondary.calls != 1 || evidence.calls["genesis"] != 1 {
		t.Fatal("expired approval reached readiness providers")
	}
	record, transaction, _, err := readRecovery(policy)
	clear(transaction)
	if err != nil || record.SendStarted || record.Finalized {
		t.Fatalf("readiness mutated recovery = %+v, %v", record, err)
	}
	recoveryPrimary := &recoveryEvidence{identity: policy.Evidence.PrimaryOriginSHA256}
	recoverySecondary := &recoveryEvidence{identity: policy.Evidence.SecondaryOriginSHA256}
	lifecycle, err := txflow.NewEvidenceLifecycle(recoveryPrimary, recoverySecondary)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReconcileRecovery(t.Context(), policy, lifecycle); err == nil ||
		err.Error() != "Mainnet submission has not started" {
		t.Fatalf("pre-send reconciliation = %v", err)
	}
	if recoveryPrimary.statusCalls != 0 || recoverySecondary.statusCalls != 0 {
		t.Fatal("pre-send reconciliation reached finality providers")
	}

	evidence.failStage = "blockhash"
	if _, err := checkJupiterRecoveryReadinessAt(
		t.Context(), policy, evidence, primary, secondary, false, clock,
	); err == nil {
		t.Fatal("disagreeing blockhash validity was accepted")
	}
	evidence.failStage = "simulation"
	if _, err := checkJupiterRecoveryReadinessAt(
		t.Context(), policy, evidence, primary, secondary, false, clock,
	); err == nil {
		t.Fatal("failed exact-message simulation was accepted")
	}
	evidence.failStage = ""
	evidence.nodeHeight = response.LastValidBlockHeight
	if _, err := checkJupiterRecoveryReadinessAt(
		t.Context(), policy, evidence, primary, secondary, false, clock,
	); err == nil {
		t.Fatal("transaction without block-height headroom was ready")
	}
	evidence.nodeHeight = 150
	evidence.failStage = "genesis"
	if _, err := checkJupiterRecoveryReadinessAt(
		t.Context(), policy, evidence, primary, secondary, false, clock,
	); err == nil {
		t.Fatal("wrong Mithril cluster was ready")
	}
	evidence.failStage = ""
	markJupiterSendStarted(t, policy)
	if _, err := checkJupiterRecoveryReadinessAt(
		t.Context(), policy, evidence, primary, secondary, false, clock,
	); err == nil {
		t.Fatal("already-started submission was ready as a first submission")
	}
}

func TestPreparedJupiterStopOnlyPolicyCannotRetryOrBeWidenedAfterward(t *testing.T) {
	policy, privateKey, request, response := jupiterSubmitterFixture(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy.ControlStatePath = filepath.Join(dir, "control.json")
	if err := PrepareJupiterRecovery(policy, privateKey, request, response); err != nil {
		t.Fatal(err)
	}
	record, transaction, _, err := readRecovery(policy)
	clear(transaction)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := control.NewMainnetCanaryStateFile(
		policy.ControlStatePath, policy.ProfileFingerprint, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := gate.Revision()
	if err != nil {
		t.Fatal(err)
	}
	issuedAt := time.Now().UTC()
	written, err := control.WriteMainnetCanaryActivationForActionIfRevision(
		policy.ControlStatePath, policy.ProfileFingerprint, revision, record.ActionID,
		issuedAt, issuedAt.Add(time.Minute), 1, "stop-only recovery test",
	)
	if err != nil || !written {
		t.Fatalf("activate stop-only canary = %t, %v", written, err)
	}
	interrupted := errors.New("interrupted before send")
	if blocked, err := gate.WithSendBarrier(
		record.ActionID, func() error { return interrupted },
	); blocked || !errors.Is(err, interrupted) {
		t.Fatalf("create recovery marker = blocked %t, %v", blocked, err)
	}

	evidence := newJupiterReadinessEvidence(policy)
	node := &jupiterSubmitNode{identity: evidence.nodeIdentity, returned: record.Submission.Signature}
	primary := &jupiterFinalizedReader{identity: policy.Evidence.PrimaryOriginSHA256, slot: 110}
	secondary := &jupiterFinalizedReader{identity: policy.Evidence.SecondaryOriginSHA256, slot: 111}
	clock := jupiterRecoveryNow(request)
	if _, err := submitPreparedJupiterAt(
		t.Context(), policy, node, evidence, primary, secondary, clock,
	); !errors.Is(err, ErrControlBlocked) || node.sendCalls != 0 {
		t.Fatalf("stop-only recovery sent: calls=%d, err=%v", node.sendCalls, err)
	}

	widened := policy
	widened.RecoveryMode = MainnetRecoveryExactRetry
	if _, err := submitPreparedJupiterAt(
		t.Context(), widened, node, evidence, primary, secondary, clock,
	); err == nil || node.sendCalls != 0 {
		t.Fatalf("post-crash recovery widening sent: calls=%d, err=%v", node.sendCalls, err)
	}
}

func TestPreparedJupiterSubmissionIsAtomicBoundedAndRecoverable(t *testing.T) {
	for _, test := range []struct {
		name      string
		sendErr   error
		wantState string
	}{
		{name: "accepted", wantState: txflow.StateAccepted},
		{name: "ambiguous", sendErr: errors.New("transport failed"), wantState: txflow.StateAmbiguous},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, privateKey, request, response := jupiterSubmitterFixture(t)
			policy.RecoveryMode = MainnetRecoveryExactRetry
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			policy.ControlStatePath = filepath.Join(dir, "control.json")
			if err := PrepareJupiterRecovery(policy, privateKey, request, response); err != nil {
				t.Fatal(err)
			}
			record, transaction, _, err := readRecovery(policy)
			clear(transaction)
			if err != nil {
				t.Fatal(err)
			}
			gate, err := control.NewMainnetCanaryStateFile(
				policy.ControlStatePath, policy.ProfileFingerprint, false,
			)
			if err != nil {
				t.Fatal(err)
			}
			wrongDir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(wrongDir, 0o700); err != nil {
				t.Fatal(err)
			}
			wrongPath := filepath.Join(wrongDir, "control.json")
			wrongGate, err := control.NewMainnetCanaryStateFile(
				wrongPath, policy.ProfileFingerprint, false,
			)
			if err != nil {
				t.Fatal(err)
			}
			wrongRevision, err := wrongGate.Revision()
			if err != nil {
				t.Fatal(err)
			}
			issuedAt := time.Now().UTC()
			written, err := control.WriteMainnetCanaryActivationForActionIfRevision(
				wrongPath, policy.ProfileFingerprint, wrongRevision, record.ActionID,
				issuedAt, issuedAt.Add(time.Hour), 1, "Mainnet canary test",
			)
			if err != nil || !written {
				t.Fatalf("activate unrelated Mainnet canary = %v, %v", written, err)
			}
			primary := &jupiterFinalizedReader{
				identity: policy.Evidence.PrimaryOriginSHA256, slot: 110,
			}
			secondary := &jupiterFinalizedReader{
				identity: policy.Evidence.SecondaryOriginSHA256, slot: 111,
			}
			evidence := newJupiterReadinessEvidence(policy)
			node := &jupiterSubmitNode{
				identity: evidence.nodeIdentity,
				returned: record.Submission.Signature,
				sendErr:  test.sendErr,
			}
			clock := jupiterRecoveryNow(request)
			if _, err := submitPreparedJupiterAt(
				t.Context(), policy, node, evidence, primary, secondary, clock,
			); !errors.Is(err, ErrControlBlocked) || node.sendCalls != 0 {
				t.Fatalf("unrelated control gate authorized submission: calls=%d, err=%v", node.sendCalls, err)
			}
			revision, err := gate.Revision()
			if err != nil {
				t.Fatal(err)
			}
			written, err = control.WriteMainnetCanaryActivationForActionIfRevision(
				policy.ControlStatePath, policy.ProfileFingerprint, revision, record.ActionID,
				issuedAt, issuedAt.Add(time.Hour), 1, "Mainnet canary test",
			)
			if err != nil || !written {
				t.Fatalf("activate Mainnet canary = %v, %v", written, err)
			}

			node.identity = strings.Repeat("4", 64)
			if _, err := submitPreparedJupiterAt(
				t.Context(), policy, node, evidence, primary, secondary, clock,
			); err == nil {
				t.Fatal("submission accepted a different Mithril broadcast node")
			}
			status, err := gate.Status()
			if err != nil || status.RemainingActions != 1 || node.sendCalls != 0 {
				t.Fatalf("node mismatch changed authority: %+v, calls=%d, err=%v", status, node.sendCalls, err)
			}
			node.identity = evidence.nodeIdentity

			evidence.failStage = "blockhash"
			if _, err := submitPreparedJupiterAt(
				t.Context(), policy, node, evidence, primary, secondary, clock,
			); err == nil {
				t.Fatal("submission consumed a canary with disagreeing readiness evidence")
			}
			status, err = gate.Status()
			if err != nil || status.Mode != control.ModeMainnetCanary ||
				status.RemainingActions != 1 || node.sendCalls != 0 {
				t.Fatalf("failed readiness changed authority: %+v, calls=%d, err=%v", status, node.sendCalls, err)
			}
			evidence.failStage = "node_height"
			if _, err := submitPreparedJupiterAt(
				t.Context(), policy, node, evidence, primary, secondary, clock,
			); err == nil {
				t.Fatal("submission consumed a canary while Mithril was behind the proposal context")
			}
			status, err = gate.Status()
			if err != nil || status.RemainingActions != 1 || node.sendCalls != 0 {
				t.Fatalf("behind-node readiness changed authority: %+v, calls=%d, err=%v", status, node.sendCalls, err)
			}
			evidence.failStage = ""
			approvalChecks := 0
			expiringClock := func() time.Time {
				approvalChecks++
				if approvalChecks == 1 {
					return time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
				}
				return time.Unix(request.ScheduleWindowEndUnix, 0).UTC()
			}
			if _, err := submitPreparedJupiterAt(
				t.Context(), policy, node, evidence, primary, secondary, expiringClock,
			); err == nil {
				t.Fatal("approval expiry during the final readiness check was accepted")
			}
			status, err = gate.Status()
			if err != nil || status.Mode != control.ModeNoNewActions ||
				!status.RecoveryPending || node.sendCalls != 0 || approvalChecks != 2 {
				t.Fatalf(
					"interrupted canary status = %+v, calls=%d, approval_checks=%d, err=%v",
					status, node.sendCalls, approvalChecks, err,
				)
			}
			submission, err := submitPreparedJupiterAt(
				t.Context(), policy, node, evidence, primary, secondary, clock,
			)
			if err != nil || submission.State != test.wantState ||
				submission.Signature != record.Submission.Signature || node.sendCalls != 1 ||
				node.lastContextSlot != record.BlockhashContext {
				t.Fatalf("submission = %+v, calls=%d, context=%d, err=%v", submission, node.sendCalls, node.lastContextSlot, err)
			}
			record, transaction, _, err = readRecovery(policy)
			clear(transaction)
			if err != nil || !record.SendStarted || record.SendAttempts != 1 || record.Finalized {
				t.Fatalf("send-started recovery = %+v, %v", record, err)
			}
			recoveryStatus, err := ReadJupiterRecoveryStatus(policy)
			if err != nil || recoveryStatus.ActionID != record.ActionID ||
				recoveryStatus.RecoveryMode != MainnetRecoveryExactRetry ||
				recoveryStatus.SendAttempts != 1 || recoveryStatus.SendAttemptLimit != 2 ||
				recoveryStatus.SendAttemptsRemaining != 1 || !recoveryStatus.SendStarted ||
				recoveryStatus.Finalized {
				t.Fatalf("bounded recovery status = %+v, %v", recoveryStatus, err)
			}
			status, err = gate.Status()
			if err != nil || status.Mode != control.ModeNoNewActions || !status.RecoveryPending {
				t.Fatalf("consumed canary status = %+v, %v", status, err)
			}
			retry, err := submitPreparedJupiterAt(
				t.Context(), policy, node, evidence, primary, secondary, clock,
			)
			if err != nil || retry != submission || node.sendCalls != 2 ||
				len(node.transactions) != 2 ||
				!bytes.Equal(node.transactions[0], node.transactions[1]) {
				t.Fatalf(
					"exact retry = %+v, calls=%d, payloads=%d, err=%v",
					retry, node.sendCalls, len(node.transactions), err,
				)
			}
			record, transaction, _, err = readRecovery(policy)
			clear(transaction)
			if err != nil || record.SendAttempts != maxJupiterSendAttempts {
				t.Fatalf("exact retry was not recorded: %+v, %v", record, err)
			}
			recoveryStatus, err = ReadJupiterRecoveryStatus(policy)
			if err != nil || recoveryStatus.SendAttemptsRemaining != 0 ||
				recoveryStatus.SendAttempts != recoveryStatus.SendAttemptLimit {
				t.Fatalf("exhausted recovery status = %+v, %v", recoveryStatus, err)
			}
			if _, err := submitPreparedJupiterAt(
				t.Context(), policy, node, evidence, primary, secondary, clock,
			); !errors.Is(err, ErrControlBlocked) || node.sendCalls != 2 {
				t.Fatalf("retry limit = calls=%d, err=%v", node.sendCalls, err)
			}
		})
	}
}

func TestRetireUnstartedJupiterRecoveryRequiresStoppedControlAndKeepsAuditCopy(t *testing.T) {
	policy, privateKey, request, response := jupiterSubmitterFixture(t)
	recoveryDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(recoveryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy.ControlStatePath = filepath.Join(recoveryDir, "control.json")
	if err := PrepareJupiterRecovery(policy, privateKey, request, response); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	gate, err := control.NewMainnetCanaryStateFile(
		policy.ControlStatePath, policy.ProfileFingerprint, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := gate.Revision()
	if err != nil {
		t.Fatal(err)
	}
	written, err := control.WriteMainnetCanaryActivationForActionIfRevision(
		policy.ControlStatePath, policy.ProfileFingerprint,
		revision, response.ActionID, now, now.Add(time.Hour), 1, "retirement test",
	)
	if err != nil || !written {
		t.Fatalf("activate Mainnet canary = %t, %v", written, err)
	}
	if _, err := RetireUnstartedJupiterRecovery(policy); err == nil {
		t.Fatal("active Mainnet proposal was retired")
	}
	if _, _, _, err := readRecovery(policy); err != nil {
		t.Fatalf("active rejection changed recovery: %v", err)
	}
	if err := gate.Stop("operator rejected proposal"); err != nil {
		t.Fatal(err)
	}
	actionID, err := RetireUnstartedJupiterRecovery(policy)
	if err != nil || actionID != response.ActionID {
		t.Fatalf("retire recovery = %q, %v", actionID, err)
	}
	if _, err := os.Lstat(recoveryPath(policy)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active recovery remains after retirement: %v", err)
	}
	archive := retiredRecoveryPath(policy, response.ActionID)
	data, err := securefile.ReadPrivate(archive, maxRecoveryBytes)
	if err != nil || len(data) == 0 {
		t.Fatalf("retired audit evidence = %d bytes, %v", len(data), err)
	}
	clear(data)
	if err := PrepareJupiterRecovery(policy, privateKey, request, response); err == nil {
		t.Fatal("retired Mainnet action was prepared again")
	}
}

func TestRetireJupiterRecoveryRefusesAnyStartedSubmission(t *testing.T) {
	policy, privateKey, request, response := jupiterSubmitterFixture(t)
	recoveryDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(recoveryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy.ControlStatePath = filepath.Join(recoveryDir, "control.json")
	if err := PrepareJupiterRecovery(policy, privateKey, request, response); err != nil {
		t.Fatal(err)
	}
	if err := control.WriteNoNewActions(policy.ControlStatePath, "recovery review"); err != nil {
		t.Fatal(err)
	}
	markJupiterSendStarted(t, policy)
	if _, err := RetireUnstartedJupiterRecovery(policy); err == nil ||
		err.Error() != "Mainnet submission has already started" {
		t.Fatalf("started Mainnet retirement = %v", err)
	}
	record, transaction, _, err := readRecovery(policy)
	clear(transaction)
	if err != nil || !record.SendStarted {
		t.Fatalf("started recovery changed = %+v, %v", record, err)
	}
}

func TestJupiterRecoveryRejectsChangedPortableEvidenceBeforeRPC(t *testing.T) {
	policy, privateKey, request, response := jupiterSubmitterFixture(t)
	recoveryDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(recoveryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy.ControlStatePath = filepath.Join(recoveryDir, "control.json")
	transaction, err := sealedtx.OpenConfidential(privateKey, response.SealedTransaction)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareJupiterRecovery(policy, request, response, transaction); err != nil {
		t.Fatal(err)
	}
	record, _, _, err := readRecovery(policy)
	if err != nil {
		t.Fatal(err)
	}
	record.JupiterRequest.JupiterCandidate.Quote.MinimumOutput++
	if err := writeRecovery(policy, record); err != nil {
		t.Fatal(err)
	}
	primary := &recoveryEvidence{identity: policy.Evidence.PrimaryOriginSHA256}
	secondary := &recoveryEvidence{identity: policy.Evidence.SecondaryOriginSHA256}
	lifecycle, err := txflow.NewEvidenceLifecycle(primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReconcileRecovery(t.Context(), policy, lifecycle); err == nil {
		t.Fatal("changed Jupiter recovery evidence was accepted")
	}
	if primary.statusCalls != 0 || secondary.statusCalls != 0 {
		t.Fatal("changed Jupiter recovery evidence reached an RPC provider")
	}
}

func TestJupiterRecoveryRejectsMatchingButWrongFinalizedEffects(t *testing.T) {
	policy, privateKey, request, response := jupiterSubmitterFixture(t)
	recoveryDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(recoveryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy.ControlStatePath = filepath.Join(recoveryDir, "control.json")
	transaction, err := sealedtx.OpenConfidential(privateKey, response.SealedTransaction)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareJupiterRecovery(policy, request, response, transaction); err != nil {
		t.Fatal(err)
	}
	status := solanarpc.SignatureStatus{
		Found: true, Slot: 150, ConfirmationStatus: "finalized",
	}
	effect := jupiterRecoveryEffect(t, policy, request, transaction, 150)
	effect.PostTokenBalances[0].Amount--
	primary := &recoveryEvidence{
		identity: policy.Evidence.PrimaryOriginSHA256, status: status, effect: effect,
	}
	secondary := &recoveryEvidence{
		identity: policy.Evidence.SecondaryOriginSHA256, status: status, effect: effect,
	}
	lifecycle, err := txflow.NewEvidenceLifecycle(primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	markJupiterSendStarted(t, policy)
	actionID, result, err := ReconcileRecovery(t.Context(), policy, lifecycle)
	if err != nil || actionID != response.ActionID ||
		result.Verdict != txflow.VerdictDiverged ||
		result.DivergenceKind != txflow.DivergenceEffects {
		t.Fatalf("wrong Jupiter recovery effects = %q, %+v, %v", actionID, result, err)
	}
	record, _, _, err := readRecovery(policy)
	if err != nil || record.Finalized {
		t.Fatalf("wrong Jupiter effects finalized recovery: %+v, %v", record, err)
	}
}

func jupiterRecoveryEffect(
	t *testing.T,
	policy Policy,
	request signer.Request,
	transaction []byte,
	slot uint64,
) solanarpc.TransactionEffect {
	t.Helper()
	_, tables, err := validateJupiterRequest(policy, request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := solana.DecodeSignedV0Transaction(transaction, tables)
	if err != nil {
		t.Fatal(err)
	}
	inputAccount, err := orcaswap.AssociatedTokenAddress(policy.Source, orcaswap.WrappedSOLMint)
	if err != nil {
		t.Fatal(err)
	}
	outputAccount, err := orcaswap.AssociatedTokenAddress(policy.Source, policy.Jupiter.OutputMint)
	if err != nil {
		t.Fatal(err)
	}
	inputIndex := recoveryAccountIndex(decoded.Message.AccountKeys, inputAccount)
	outputIndex := recoveryAccountIndex(decoded.Message.AccountKeys, outputAccount)
	if inputIndex <= 0 || outputIndex <= 0 {
		t.Fatal("Jupiter recovery accounts are missing from the message")
	}
	pre := make([]uint64, len(decoded.Message.AccountKeys))
	post := make([]uint64, len(decoded.Message.AccountKeys))
	for index := range pre {
		pre[index], post[index] = uint64(100_000_000+index), uint64(100_000_000+index)
	}
	const rent = uint64(2_039_280)
	pre[0], pre[inputIndex], post[inputIndex] = 1_000_000_000, rent, 0
	post[0] = pre[0] + rent - request.JupiterCandidate.Request.InputAmount - request.FeeLamports
	pre[outputIndex], post[outputIndex] = rent, rent
	return solanarpc.TransactionEffect{
		Slot: slot, Transaction: transaction, FeeLamports: request.FeeLamports,
		PreBalances: pre, PostBalances: post,
		PreTokenBalances: []solanarpc.TokenBalance{
			{AccountIndex: uint16(inputIndex), Mint: orcaswap.WrappedSOLMint, Owner: policy.Source},
			{AccountIndex: uint16(outputIndex), Mint: policy.Jupiter.OutputMint, Owner: policy.Source, Amount: 100},
		},
		PostTokenBalances: []solanarpc.TokenBalance{
			{AccountIndex: uint16(outputIndex), Mint: policy.Jupiter.OutputMint, Owner: policy.Source, Amount: 120},
		},
	}
}

func markJupiterSendStarted(t *testing.T, policy Policy) {
	t.Helper()
	if err := withRecoveryLock(policy, func() error {
		record, transaction, _, err := readRecovery(policy)
		clear(transaction)
		if err != nil {
			return err
		}
		record.SendStarted = true
		record.SendAttempts = 1
		return writeRecovery(policy, record)
	}); err != nil {
		t.Fatal(err)
	}
}

func recoveryAccountIndex(accounts [][32]byte, address string) int {
	want, err := solana.Decode32(address)
	if err != nil {
		return -1
	}
	for index, account := range accounts {
		if account == want {
			return index
		}
	}
	return -1
}
