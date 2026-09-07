package submitter

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

// JupiterSubmitterFixture exposes the existing fixture only to external tests.
var JupiterSubmitterFixture = jupiterSubmitterFixture

// CheckJupiterComposedRecovery retains the private recovery assertions used by
// the external authority/custody/submitter composition test.
func CheckJupiterComposedRecovery(t *testing.T, submitterPolicy Policy, submitterKey string, request signer.Request, response signer.Response) {
	t.Helper()
	recoveryDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(recoveryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	submitterPolicy.ControlStatePath = filepath.Join(recoveryDir, "control.json")
	submitterPolicy.RecoveryMode = MainnetRecoveryExactRetry
	transaction, err := sealedtx.OpenConfidential(
		submitterKey, response.SealedTransaction,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(transaction)
	if err := PrepareJupiterRecovery(
		submitterPolicy, submitterKey, request, response,
	); err != nil {
		t.Fatal(err)
	}
	unsigned := request
	unsigned.RiskGrant = signer.Request{}.RiskGrant
	if _, err := ReadJupiterFinalizedEvidence(submitterPolicy, unsigned); err == nil {
		t.Fatal("prepared-only response became finalized claim evidence")
	}
	record, persisted, _, err := readRecovery(submitterPolicy)
	clear(persisted)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := control.NewMainnetCanaryStateFile(submitterPolicy.ControlStatePath, submitterPolicy.ProfileFingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	evidence := newJupiterReadinessEvidence(submitterPolicy)
	node := &jupiterSubmitNode{identity: evidence.nodeIdentity, returned: record.Submission.Signature,
		sendErr: errors.New("offline ambiguous transport")}
	primarySlot := &jupiterFinalizedReader{identity: submitterPolicy.Evidence.PrimaryOriginSHA256, slot: 110}
	secondarySlot := &jupiterFinalizedReader{identity: submitterPolicy.Evidence.SecondaryOriginSHA256, slot: 111}
	clock := jupiterRecoveryNow(request)
	if _, err := submitPreparedJupiterAt(t.Context(), submitterPolicy, node, evidence, primarySlot, secondarySlot, clock); !errors.Is(err, ErrControlBlocked) || node.sendCalls != 0 {
		t.Fatalf("stopped composed send: calls=%d, err=%v", node.sendCalls, err)
	}
	revision, err := gate.Revision()
	if err != nil {
		t.Fatal(err)
	}
	issuedAt := time.Now().UTC()
	written, err := control.WriteMainnetCanaryActivationForActionIfRevision(submitterPolicy.ControlStatePath,
		submitterPolicy.ProfileFingerprint, revision, request.ActionID, issuedAt, issuedAt.Add(time.Hour), 1, "offline composed test")
	if err != nil || !written {
		t.Fatalf("activate isolated test control: written=%v, err=%v", written, err)
	}
	first, err := submitPreparedJupiterAt(t.Context(), submitterPolicy, node, evidence, primarySlot, secondarySlot, clock)
	if err != nil || first.State != txflow.StateAmbiguous || node.sendCalls != 1 || !bytes.Equal(node.transactions[0], transaction) {
		t.Fatalf("composed ambiguous send: calls=%d, err=%v", node.sendCalls, err)
	}
	record, persisted, _, err = readRecovery(submitterPolicy)
	clear(persisted)
	if err != nil || !record.SendStarted || record.SendAttempts != 1 || record.Finalized {
		t.Fatalf("composed durable send state: err=%v", err)
	}
	// Recreate the node and call again using only durable recovery, without a
	// second approval, grant or custody call. The payload must remain exact.
	restarted := &jupiterSubmitNode{identity: evidence.nodeIdentity, returned: record.Submission.Signature}
	second, err := submitPreparedJupiterAt(t.Context(), submitterPolicy, restarted, evidence, primarySlot, secondarySlot, clock)
	if err != nil || second.State != txflow.StateAccepted || second.Signature != first.Signature || restarted.sendCalls != 1 ||
		!bytes.Equal(restarted.transactions[0], node.transactions[0]) {
		t.Fatalf("composed restarted exact send: calls=%d, err=%v", restarted.sendCalls, err)
	}
	if _, err := submitPreparedJupiterAt(t.Context(), submitterPolicy, restarted, evidence, primarySlot, secondarySlot, clock); !errors.Is(err, ErrControlBlocked) || restarted.sendCalls != 1 {
		t.Fatalf("composed send exceeded retry cap: calls=%d, err=%v", restarted.sendCalls, err)
	}
	if _, err := ReadJupiterFinalizedEvidence(submitterPolicy, unsigned); err == nil {
		t.Fatal("accepted broadcast became finalized claim evidence before reconciliation")
	}
	status := solanarpc.SignatureStatus{
		Found: true, Slot: 150, ConfirmationStatus: "finalized",
	}
	effect := jupiterRecoveryEffect(t, submitterPolicy, request, transaction, 150)
	primary := &recoveryEvidence{
		identity: submitterPolicy.Evidence.PrimaryOriginSHA256,
		status:   status, effect: effect,
	}
	secondary := &recoveryEvidence{
		identity: submitterPolicy.Evidence.SecondaryOriginSHA256,
		status:   status, effect: effect,
	}
	lifecycle, err := txflow.NewEvidenceLifecycle(primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	actionID, result, err := ReconcileRecovery(t.Context(), submitterPolicy, lifecycle)
	if err != nil || actionID != request.ActionID || result.Verdict != txflow.VerdictFinalized {
		t.Fatalf("composed Jupiter recovery = %q, %+v, %v", actionID, result, err)
	}
	if _, err := ReadJupiterFinalizedEvidence(submitterPolicy, unsigned); err != nil {
		t.Fatalf("read exact unsigned claim outcome: %v", err)
	}
	changed := unsigned
	changed.FeeLamports++
	if _, err := ReadJupiterFinalizedEvidence(submitterPolicy, changed); err == nil {
		t.Fatal("finalized evidence accepted a different unsigned request")
	}
	before, err := os.ReadFile(recoveryPath(submitterPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReconcileRecovery(t.Context(), submitterPolicy, lifecycle); err != nil {
		t.Fatalf("repeat composed reconciliation: %v", err)
	}
	after, err := os.ReadFile(recoveryPath(submitterPolicy))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("repeat reconciliation changed recovery bytes: %v", err)
	}
	if _, err := submitPreparedJupiterAt(t.Context(), submitterPolicy, restarted, evidence, primarySlot, secondarySlot, clock); err == nil || restarted.sendCalls != 1 {
		t.Fatalf("finalized composed action was resent: calls=%d, err=%v", restarted.sendCalls, err)
	}
}
