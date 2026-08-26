package submitter

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func TestSubmitPersistsExactRecoveryBeforeAmbiguousSend(t *testing.T) {
	policy, privateKey, response, transaction := submitterFixture(t)
	node := &submitterTestNode{err: errors.New("response lost")}
	node.duringSend = func() {
		record, persisted, decoded, err := readRecovery(policy)
		if err != nil {
			t.Fatalf("read recovery evidence during send: %v", err)
		}
		if record.ActionID != response.ActionID || record.Finalized ||
			record.Submission.Signature != response.Signature ||
			!strings.EqualFold(decoded.signature, response.Signature) ||
			string(persisted) != string(transaction) {
			t.Fatalf("recovery evidence = %+v", record)
		}
		info, err := os.Stat(recoveryPath(policy))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("recovery evidence permissions = %v, %v", info, err)
		}
	}
	submission, err := submitWithGate(
		t.Context(), policy, privateKey, node,
		submitterTestGate{allowed: true}, response, 90,
	)
	if err != nil || submission.State != txflow.StateAmbiguous {
		t.Fatalf("ambiguous submission = %+v, %v", submission, err)
	}
}

func TestRecoveryRequiresMatchingFinalizedEffects(t *testing.T) {
	policy, privateKey, response, transaction := submitterFixture(t)
	policy.Evidence = proposalcheck.ProviderBindings{
		PrimaryTrustDomain: "provider-one", PrimaryOriginSHA256: strings.Repeat("b", 64),
		SecondaryTrustDomain: "provider-two", SecondaryOriginSHA256: strings.Repeat("c", 64),
	}
	node := &submitterTestNode{returned: response.Signature}
	if _, err := submitWithGate(
		t.Context(), policy, privateKey, node,
		submitterTestGate{allowed: true}, response, 90,
	); err != nil {
		t.Fatal(err)
	}
	status := solanarpc.SignatureStatus{
		Found: true, Slot: 321, ConfirmationStatus: "finalized",
	}
	effect := solanarpc.TransactionEffect{
		Slot: 321, Transaction: transaction, FeeLamports: response.FeeLamports,
		PreBalances:  []uint64{10_000, 100, 1},
		PostBalances: []uint64{4_958, 142, 1},
	}
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
	secondary.effect.PostBalances = []uint64{4_958, 141, 1}
	actionID, result, err := ReconcileRecovery(t.Context(), policy, lifecycle)
	if err != nil || actionID != response.ActionID || result.Verdict != txflow.VerdictDiverged {
		t.Fatalf("divergent recovery = %q, %+v, %v", actionID, result, err)
	}
	record, _, _, err := readRecovery(policy)
	if err != nil || record.Finalized {
		t.Fatalf("divergent evidence finalized recovery = %+v, %v", record, err)
	}
	secondary.effect = effect
	actionID, result, err = ReconcileRecovery(t.Context(), policy, lifecycle)
	if err != nil || actionID != response.ActionID || result.Verdict != txflow.VerdictFinalized {
		t.Fatalf("finalized recovery = %q, %+v, %v", actionID, result, err)
	}
	record, _, _, err = readRecovery(policy)
	if err != nil || !record.Finalized {
		t.Fatalf("finalized evidence was not durable = %+v, %v", record, err)
	}
	if err := prepareRecovery(policy, response, transaction); err == nil {
		t.Fatal("finalized action was reopened for submission")
	}
}

func TestRecoveryPersistsFinalizedFailure(t *testing.T) {
	policy, privateKey, response, transaction := submitterFixture(t)
	policy.Evidence = proposalcheck.ProviderBindings{
		PrimaryTrustDomain: "provider-one", PrimaryOriginSHA256: strings.Repeat("b", 64),
		SecondaryTrustDomain: "provider-two", SecondaryOriginSHA256: strings.Repeat("c", 64),
	}
	if _, err := submitWithGate(
		t.Context(), policy, privateKey,
		&submitterTestNode{returned: response.Signature},
		submitterTestGate{allowed: true}, response, 90,
	); err != nil {
		t.Fatal(err)
	}
	status := solanarpc.SignatureStatus{
		Found: true, Slot: 321, ConfirmationStatus: "finalized",
		Failed: true, ErrorFingerprint: "program_error",
	}
	effect := solanarpc.TransactionEffect{
		Slot: 321, Transaction: transaction, FeeLamports: response.FeeLamports,
		Failed: true, ErrorFingerprint: "program_error",
		PreBalances:  []uint64{response.FeeLamports + 10_000, 100, 1},
		PostBalances: []uint64{10_000, 100, 1},
	}
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
	actionID, result, err := ReconcileRecovery(t.Context(), policy, lifecycle)
	if err != nil || actionID != response.ActionID || result.Verdict != txflow.VerdictFailed {
		t.Fatalf("failed recovery = %q, %+v, %v", actionID, result, err)
	}
	record, _, _, err := readRecovery(policy)
	if err != nil || !record.Finalized {
		t.Fatalf("finalized failure was not durable = %+v, %v", record, err)
	}
}

func TestRecoveryRejectsAChangedActionID(t *testing.T) {
	policy, privateKey, response, _ := submitterFixture(t)
	policy.Evidence = proposalcheck.ProviderBindings{
		PrimaryTrustDomain: "provider-one", PrimaryOriginSHA256: strings.Repeat("b", 64),
		SecondaryTrustDomain: "provider-two", SecondaryOriginSHA256: strings.Repeat("c", 64),
	}
	if _, err := submitWithGate(
		t.Context(), policy, privateKey,
		&submitterTestNode{returned: response.Signature},
		submitterTestGate{allowed: true}, response, 90,
	); err != nil {
		t.Fatal(err)
	}
	record, _, _, err := readRecovery(policy)
	if err != nil {
		t.Fatal(err)
	}
	record.ActionID = strings.Repeat("d", 64)
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
		t.Fatal("changed recovery action ID was accepted")
	}
	if primary.statusCalls != 0 || secondary.statusCalls != 0 {
		t.Fatal("changed recovery evidence reached an RPC provider")
	}
}

func TestRecoveryNeverTrustsTheLocalFinalizedFlag(t *testing.T) {
	policy, privateKey, response, _ := submitterFixture(t)
	policy.Evidence = proposalcheck.ProviderBindings{
		PrimaryTrustDomain: "provider-one", PrimaryOriginSHA256: strings.Repeat("b", 64),
		SecondaryTrustDomain: "provider-two", SecondaryOriginSHA256: strings.Repeat("c", 64),
	}
	if _, err := submitWithGate(
		t.Context(), policy, privateKey,
		&submitterTestNode{returned: response.Signature},
		submitterTestGate{allowed: true}, response, 90,
	); err != nil {
		t.Fatal(err)
	}
	record, _, _, err := readRecovery(policy)
	if err != nil {
		t.Fatal(err)
	}
	record.Finalized = true
	if err := writeRecovery(policy, record); err != nil {
		t.Fatal(err)
	}
	primary := &recoveryEvidence{identity: policy.Evidence.PrimaryOriginSHA256}
	secondary := &recoveryEvidence{identity: policy.Evidence.SecondaryOriginSHA256}
	lifecycle, err := txflow.NewEvidenceLifecycle(primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	_, result, _ := ReconcileRecovery(t.Context(), policy, lifecycle)
	if result.Verdict == txflow.VerdictFinalized ||
		primary.statusCalls == 0 || secondary.statusCalls == 0 {
		t.Fatalf(
			"local finalized flag bypassed independent evidence: result=%+v calls=%d/%d",
			result, primary.statusCalls, secondary.statusCalls,
		)
	}
}

func TestRecoveryRequiresThePinnedEvidenceProviders(t *testing.T) {
	policy, privateKey, response, transaction := submitterFixture(t)
	policy.Evidence = proposalcheck.ProviderBindings{
		PrimaryTrustDomain: "provider-one", PrimaryOriginSHA256: strings.Repeat("b", 64),
		SecondaryTrustDomain: "provider-two", SecondaryOriginSHA256: strings.Repeat("c", 64),
	}
	if _, err := submitWithGate(
		t.Context(), policy, privateKey,
		&submitterTestNode{returned: response.Signature},
		submitterTestGate{allowed: true}, response, 90,
	); err != nil {
		t.Fatal(err)
	}
	status := solanarpc.SignatureStatus{
		Found: true, Slot: 321, ConfirmationStatus: "finalized",
	}
	effect := solanarpc.TransactionEffect{
		Slot: 321, Transaction: transaction, FeeLamports: response.FeeLamports,
		PreBalances:  []uint64{10_000, 100, 1},
		PostBalances: []uint64{4_958, 142, 1},
	}
	primary := &recoveryEvidence{identity: strings.Repeat("d", 64), status: status, effect: effect}
	secondary := &recoveryEvidence{identity: strings.Repeat("e", 64), status: status, effect: effect}
	lifecycle, err := txflow.NewEvidenceLifecycle(primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReconcileRecovery(t.Context(), policy, lifecycle); err == nil {
		t.Fatal("recovery accepted evidence from providers outside the pinned policy")
	}
	if primary.statusCalls != 0 || secondary.statusCalls != 0 {
		t.Fatal("unbound evidence providers were queried")
	}
}

type recoveryEvidence struct {
	identity          string
	genesis           string
	genesisErr        error
	status            solanarpc.SignatureStatus
	effect            solanarpc.TransactionEffect
	blockhashValidity solanarpc.BlockhashValidity
	blockhashValidErr error
	statusCalls       int
	validityCalls     int
}

func (e *recoveryEvidence) Identity() string { return e.identity }
func (e *recoveryEvidence) GenesisHash(context.Context) (string, error) {
	return e.genesis, e.genesisErr
}
func (e *recoveryEvidence) FinalizedSlot(context.Context) (uint64, error) {
	return 0, nil
}
func (e *recoveryEvidence) Account(context.Context, string, uint64) (solanarpc.AccountQuote, error) {
	return solanarpc.AccountQuote{}, nil
}
func (e *recoveryEvidence) AccountSlice(
	context.Context, string, uint64, uint64, uint64,
) (solanarpc.AccountDataSlice, error) {
	return solanarpc.AccountDataSlice{}, nil
}
func (e *recoveryEvidence) MinimumBalanceForRentExemption(context.Context, uint64) (uint64, error) {
	return 0, nil
}
func (e *recoveryEvidence) FeeForMessage(context.Context, []byte, uint64) (solanarpc.FeeQuote, error) {
	return solanarpc.FeeQuote{}, nil
}
func (e *recoveryEvidence) SignatureStatus(context.Context, string) (solanarpc.SignatureStatus, error) {
	e.statusCalls++
	return e.status, nil
}
func (e *recoveryEvidence) TransactionEffect(context.Context, string) (solanarpc.TransactionEffect, error) {
	return e.effect, nil
}
func (e *recoveryEvidence) BlockHeight(context.Context) (uint64, error) {
	return 100, nil
}
func (e *recoveryEvidence) BlockhashValid(
	_ context.Context,
	_ string,
	minContextSlot uint64,
) (solanarpc.BlockhashValidity, error) {
	e.validityCalls++
	value := e.blockhashValidity
	if value.ContextSlot == 0 {
		value.ContextSlot = minContextSlot
	}
	return value, e.blockhashValidErr
}
