package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/submittertransport"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func TestApplyRecoveryResultStopsFinalizedFailureForAcknowledgement(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "control.json")
	fingerprint := strings.Repeat("a", 64)
	actionID := strings.Repeat("b", 64)
	now := time.Now().UTC()
	if err := control.WriteDevnetActivation(
		path, fingerprint, now.Add(-time.Second), now.Add(time.Hour), 2, "recovery test",
	); err != nil {
		t.Fatal(err)
	}
	gate, err := control.NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if blocked, err := gate.WithSendBarrier(actionID, func() error { return nil }); err != nil || blocked {
		t.Fatalf("send barrier = blocked %t, error %v", blocked, err)
	}
	if err := applyRecoveryResult(gate, actionID, txflow.VerdictFailed); err != nil {
		t.Fatal(err)
	}
	status, err := gate.Status()
	if err != nil || status.Mode != control.ModeNoNewActions || status.RecoveryPending ||
		status.TerminalActionID != actionID || status.TerminalOutcome != txflow.VerdictFailed {
		t.Fatalf("failed recovery status = %+v, error %v", status, err)
	}
	status, err = gate.AcknowledgeTerminal(actionID, txflow.VerdictFailed, "reviewed failure")
	if err != nil || status.Mode != control.ModeNoNewActions || status.TerminalActionID != "" ||
		status.TerminalOutcome != "" || status.RecoveryPending {
		t.Fatalf("acknowledged failure status = %+v, error %v", status, err)
	}
}

func TestSubmitterIdentityIsOfflineAndRequestsAreBounded(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	privateKey, publicKey, err := sealedtx.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(dir, "policy.json")
	keyPath := filepath.Join(dir, "key.json")
	policy := submitter.Policy{
		Cluster: "devnet", ProfileFingerprint: strings.Repeat("0", 64),
		ControlStatePath: filepath.Join(dir, "control.json"),
		Source:           "11111111111111111111111111111111",
		Destination:      "Stake11111111111111111111111111111111111111",
		MaxLamports:      1, MaxFeeLamports: 1, SubmitterPublicKey: publicKey,
	}
	writePrivateJSON(t, policyPath, policy)
	writePrivateJSON(t, keyPath, submitter.KeyDocument{Version: 1, PrivateKey: privateKey})
	t.Setenv("MITHRIL_AGENT_MITHRIL_RPC_URL", "")

	var output bytes.Buffer
	if err := run(t.Context(), []string{
		"--policy", policyPath, "--key", keyPath, "--identity",
	}, bytes.NewReader(nil), &output); err != nil {
		t.Fatal(err)
	}
	var identity struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(output.Bytes(), &identity); err != nil || identity.PublicKey != publicKey {
		t.Fatalf("identity = %+v, error = %v", identity, err)
	}

	tooLarge := bytes.NewReader(bytes.Repeat([]byte{'x'}, maxRequestBytes+1))
	if err := run(t.Context(), []string{
		"--policy", policyPath, "--key", keyPath,
	}, tooLarge, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized request error = %v", err)
	}

	if err := run(t.Context(), []string{
		"--policy", policyPath, "--key", keyPath, "--prepare-mainnet",
		"--signer-request", filepath.Join(dir, "request.json"),
	}, bytes.NewReader(nil), &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), "required together") {
		t.Fatalf("unpaired Mainnet prepare files error = %v", err)
	}
	if err := run(t.Context(), []string{
		"--policy", policyPath, "--key", keyPath, "--check-mainnet",
	}, bytes.NewReader(nil), &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), "must not receive --key") {
		t.Fatalf("Mainnet check accepted a private key: %v", err)
	}
	if err := run(t.Context(), []string{
		"--policy", policyPath, "--check-mainnet",
	}, bytes.NewReader(nil), &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), "requires a Jupiter Mainnet policy") {
		t.Fatalf("Mainnet check accepted a Devnet policy: %v", err)
	}
	if err := run(t.Context(), []string{
		"--policy", policyPath, "--recovery-status",
	}, bytes.NewReader(nil), &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), "requires a Jupiter Mainnet policy") {
		t.Fatalf("Mainnet recovery status accepted a Devnet policy: %v", err)
	}

	for _, operation := range []string{
		submittertransport.OperationPrepare,
		submittertransport.OperationEnable,
		submittertransport.OperationAcknowledgeTerminal,
	} {
		t.Run("runtime rejects "+operation, func(t *testing.T) {
			request, err := json.Marshal(submittertransport.Request{
				Version: submittertransport.Version, Operation: operation,
			})
			if err != nil {
				t.Fatal(err)
			}
			var response bytes.Buffer
			err = run(t.Context(), []string{
				"--policy", policyPath, "--key", keyPath, "--socket",
			}, bytes.NewReader(request), &response)
			if err == nil {
				t.Fatalf("runtime accepted %q", operation)
			}
			var envelope submittertransport.Response
			if decodeErr := json.Unmarshal(response.Bytes(), &envelope); decodeErr != nil ||
				envelope.Status != submittertransport.StatusFailed {
				t.Fatalf("runtime %q response = %+v, %v", operation, envelope, decodeErr)
			}
		})
	}

	var statusOutput bytes.Buffer
	statusRequest, err := json.Marshal(submittertransport.Request{
		Version:   submittertransport.Version,
		Operation: submittertransport.OperationOperatorStatus,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{
		"--policy", policyPath, "--operator-socket",
	}, bytes.NewReader(statusRequest), &statusOutput); err != nil {
		t.Fatal(err)
	}
	var status submittertransport.Response
	if err := json.Unmarshal(statusOutput.Bytes(), &status); err != nil ||
		status.Status != submittertransport.StatusOK || status.Control == nil ||
		status.Control.Mode != control.ModeNoNewActions || len(status.Revision) != 64 ||
		status.Identity != nil {
		t.Fatalf("operator status = %+v, %v", status, err)
	}

	snapshotRequest, err := json.Marshal(submittertransport.Request{
		Version: submittertransport.Version, Operation: submittertransport.OperationOperatorSnapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	var snapshotOutput bytes.Buffer
	if err := run(t.Context(), []string{
		"--policy", policyPath, "--operator-socket",
	}, bytes.NewReader(snapshotRequest), &snapshotOutput); err != nil {
		t.Fatal(err)
	}
	var snapshot submittertransport.Response
	if err := json.Unmarshal(snapshotOutput.Bytes(), &snapshot); err != nil ||
		snapshot.Status != submittertransport.StatusOK || snapshot.Control == nil ||
		snapshot.Control.Mode != control.ModeNoNewActions || len(snapshot.Revision) != 64 ||
		snapshot.Identity == nil || snapshot.Identity.PublicKey != policy.SubmitterPublicKey ||
		snapshot.Identity.ProfileFingerprint != policy.ProfileFingerprint ||
		snapshot.Identity.Source != policy.Source {
		t.Fatalf("operator snapshot = %+v, %v", snapshot, err)
	}

	issuedAt := time.Now().UTC()
	enableRequest, err := json.Marshal(submittertransport.Request{
		Version: submittertransport.Version, Operation: submittertransport.OperationEnable,
		ExpectedRevision: status.Revision, IssuedAt: issuedAt,
		ExpiresAt: issuedAt.Add(time.Hour), MaxActions: 1, Reason: "operator test",
	})
	if err != nil {
		t.Fatal(err)
	}
	var enabled bytes.Buffer
	if err := run(t.Context(), []string{
		"--policy", policyPath, "--operator-socket",
	}, bytes.NewReader(enableRequest), &enabled); err != nil {
		t.Fatal(err)
	}
	var enabledResponse submittertransport.Response
	if err := json.Unmarshal(enabled.Bytes(), &enabledResponse); err != nil ||
		enabledResponse.Status != submittertransport.StatusOK {
		t.Fatalf("operator enable = %+v, %v", enabledResponse, err)
	}

	var conflict bytes.Buffer
	if err := run(t.Context(), []string{
		"--policy", policyPath, "--operator-socket",
	}, bytes.NewReader(enableRequest), &conflict); err != nil {
		t.Fatal(err)
	}
	var conflictResponse submittertransport.Response
	if err := json.Unmarshal(conflict.Bytes(), &conflictResponse); err != nil ||
		conflictResponse.Status != submittertransport.StatusConflict {
		t.Fatalf("stale operator enable = %+v, %v", conflictResponse, err)
	}

	if err := run(t.Context(), []string{
		"--policy", policyPath, "--key", keyPath, "--operator-socket",
	}, bytes.NewReader(statusRequest), &bytes.Buffer{}); err == nil {
		t.Fatal("operator socket accepted a private key credential")
	}
}

func writePrivateJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestControlHelpersSelectPolicyMode(t *testing.T) {
	for _, test := range []struct {
		name string
		mode string
	}{
		{name: "Devnet", mode: control.ModeDevnetEnabled},
		{name: "Mainnet canary", mode: control.ModeMainnetCanary},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			policy := submitter.Policy{
				ProfileFingerprint: strings.Repeat("a", 64),
				ControlStatePath:   filepath.Join(dir, "control.json"),
			}
			if test.mode == control.ModeMainnetCanary {
				policy.Jupiter = &jupiterswap.Policy{}
			}
			gate, err := newControlGate(policy)
			if err != nil {
				t.Fatal(err)
			}
			revision, err := gate.Revision()
			if err != nil {
				t.Fatal(err)
			}
			issuedAt := time.Now().UTC()
			request := submittertransport.Request{
				ExpectedRevision: revision, IssuedAt: issuedAt,
				ExpiresAt: issuedAt.Add(time.Hour), MaxActions: 1,
				Reason: "operator mode test",
			}
			if test.mode == control.ModeMainnetCanary {
				request.ActionID = strings.Repeat("b", 64)
			}
			written, err := writeControlActivation(policy, request)
			if err != nil || !written {
				t.Fatalf("write %s activation = %v, %v", test.mode, written, err)
			}
			status, err := gate.Status()
			if err != nil || status.Mode != test.mode || status.RemainingActions != 1 ||
				(test.mode == control.ModeMainnetCanary &&
					status.ExpectedActionID != request.ActionID) {
				t.Fatalf("%s status = %+v, %v", test.mode, status, err)
			}
			written, err = writeControlActivation(policy, request)
			if err != nil || written {
				t.Fatalf("stale %s revision = %v, %v", test.mode, written, err)
			}
		})
	}
}

func TestRecoverAcceptsAKeylessMainnetPolicy(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := mainnetSubmitterPolicy(t, dir)
	policyPath := filepath.Join(dir, "policy.json")
	writePrivateJSON(t, policyPath, policy)
	t.Setenv("MITHRIL_AGENT_PRIMARY_RPC_URL", "")
	t.Setenv("MITHRIL_AGENT_SECONDARY_RPC_URL", "")

	err = run(t.Context(), []string{
		"--policy", policyPath, "--recover",
	}, bytes.NewReader(nil), &bytes.Buffer{})
	if err == nil || err.Error() != "two evidence RPCs are required" {
		t.Fatalf("keyless Mainnet recovery boundary = %v", err)
	}
	if err := run(t.Context(), []string{
		"--policy", policyPath, "--key", filepath.Join(dir, "key.json"), "--recover",
	}, bytes.NewReader(nil), &bytes.Buffer{}); err == nil ||
		err.Error() != "--recover must not receive --key" {
		t.Fatalf("Mainnet recovery accepted a key: %v", err)
	}
}

func TestMainnetSubmitterRuntimeIsPrepareOnly(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	privateKey, publicKey, err := sealedtx.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	policy := mainnetSubmitterPolicy(t, dir)
	policy.SubmitterPublicKey = publicKey
	policyPath := filepath.Join(dir, "policy.json")
	keyPath := filepath.Join(dir, "key.json")
	writePrivateJSON(t, policyPath, policy)
	writePrivateJSON(t, keyPath, submitter.KeyDocument{Version: 1, PrivateKey: privateKey})

	request, err := json.Marshal(submittertransport.Request{
		Version: submittertransport.Version, Operation: submittertransport.OperationSubmit,
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = run(t.Context(), []string{
		"--policy", policyPath, "--key", keyPath, "--socket",
	}, bytes.NewReader(request), &output)
	if err == nil || err.Error() != "Mainnet submitter operation is disabled" {
		t.Fatalf("Mainnet runtime submission = %v", err)
	}
	var response submittertransport.Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil ||
		response.Status != submittertransport.StatusFailed {
		t.Fatalf("Mainnet runtime refusal = %+v, %v", response, err)
	}

	if err := run(t.Context(), []string{
		"--policy", policyPath, "--key", keyPath,
	}, bytes.NewReader([]byte("{}")), &bytes.Buffer{}); err == nil ||
		err.Error() != "submitter policy is restricted to devnet" {
		t.Fatalf("direct Mainnet submission = %v", err)
	}

	if err := run(t.Context(), []string{
		"--policy", policyPath, "--key", keyPath, "--prepare-mainnet",
	}, bytes.NewReader([]byte(`{"unknown":true}`)), &bytes.Buffer{}); err == nil ||
		err.Error() != "decode Mainnet prepare request" {
		t.Fatalf("unknown Mainnet prepare input = %v", err)
	}

	t.Setenv("MITHRIL_AGENT_PRIMARY_RPC_URL", "")
	t.Setenv("MITHRIL_AGENT_SECONDARY_RPC_URL", "")
	if err := run(t.Context(), []string{
		"--policy", policyPath, "--check-mainnet",
	}, bytes.NewReader(nil), &bytes.Buffer{}); err == nil ||
		err.Error() != "two evidence RPCs are required" {
		t.Fatalf("Mainnet readiness without independent evidence = %v", err)
	}
	if err := run(t.Context(), []string{
		"--policy", policyPath, "--key", keyPath, "--retire-mainnet",
	}, bytes.NewReader(nil), &bytes.Buffer{}); err == nil ||
		err.Error() != "--retire-mainnet must not receive --key" {
		t.Fatalf("Mainnet retirement accepted a private key: %v", err)
	}
	if err := run(t.Context(), []string{
		"--policy", policyPath, "--key", keyPath, "--recovery-status",
	}, bytes.NewReader(nil), &bytes.Buffer{}); err == nil ||
		err.Error() != "--recovery-status must not receive --key" {
		t.Fatalf("Mainnet recovery status accepted a private key: %v", err)
	}
	var recoveryOutput bytes.Buffer
	if err := run(t.Context(), []string{
		"--policy", policyPath, "--recovery-status",
	}, bytes.NewReader(nil), &recoveryOutput); err == nil ||
		err.Error() != "Mainnet recovery record is unavailable" || recoveryOutput.Len() != 0 {
		t.Fatalf("missing Mainnet recovery status = %q, %v", recoveryOutput.String(), err)
	}
	if err := run(t.Context(), []string{
		"--policy", policyPath, "--retire-mainnet",
	}, bytes.NewReader(nil), &bytes.Buffer{}); err == nil ||
		err.Error() != "control state is missing" {
		t.Fatalf("Mainnet retirement without stopped control = %v", err)
	}

	statusRequest, err := json.Marshal(submittertransport.Request{
		Version:   submittertransport.Version,
		Operation: submittertransport.OperationOperatorStatus,
	})
	if err != nil {
		t.Fatal(err)
	}
	var statusOutput bytes.Buffer
	if err := run(t.Context(), []string{
		"--policy", policyPath, "--operator-socket",
	}, bytes.NewReader(statusRequest), &statusOutput); err != nil {
		t.Fatal(err)
	}
	var status submittertransport.Response
	if err := json.Unmarshal(statusOutput.Bytes(), &status); err != nil ||
		status.Status != submittertransport.StatusOK || len(status.Revision) != 64 {
		t.Fatalf("Mainnet operator status = %+v, %v", status, err)
	}
	issuedAt := time.Now().UTC()
	enable := submittertransport.Request{
		Version: submittertransport.Version, Operation: submittertransport.OperationEnable,
		ExpectedRevision: status.Revision, IssuedAt: issuedAt,
		ExpiresAt: issuedAt.Add(time.Hour), MaxActions: 1, Reason: "reviewed canary",
	}
	request, err = json.Marshal(enable)
	if err != nil {
		t.Fatal(err)
	}
	var missingAction bytes.Buffer
	err = run(t.Context(), []string{
		"--policy", policyPath, "--operator-socket",
	}, bytes.NewReader(request), &missingAction)
	if err == nil || err.Error() != "operator enable request is invalid" {
		t.Fatalf("unbound Mainnet enable error = %v", err)
	}
	var refusal submittertransport.Response
	if err := json.Unmarshal(missingAction.Bytes(), &refusal); err != nil ||
		refusal.Status != submittertransport.StatusFailed {
		t.Fatalf("unbound Mainnet enable = %+v, %v", refusal, err)
	}
	enable.ActionID = strings.Repeat("c", 64)
	request, err = json.Marshal(enable)
	if err != nil {
		t.Fatal(err)
	}
	var enabled bytes.Buffer
	if err := run(t.Context(), []string{
		"--policy", policyPath, "--operator-socket",
	}, bytes.NewReader(request), &enabled); err != nil {
		t.Fatal(err)
	}
	var enabledResponse submittertransport.Response
	if err := json.Unmarshal(enabled.Bytes(), &enabledResponse); err != nil ||
		enabledResponse.Status != submittertransport.StatusOK {
		t.Fatalf("action-bound Mainnet enable = %+v, %v", enabledResponse, err)
	}
	gate, err := control.NewMainnetCanaryStateFile(
		policy.ControlStatePath, policy.ProfileFingerprint, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	controlStatus, err := gate.Status()
	if err != nil || controlStatus.ExpectedActionID != enable.ActionID {
		t.Fatalf("Mainnet grant action binding = %+v, %v", controlStatus, err)
	}
}

func mainnetSubmitterPolicy(t *testing.T, dir string) submitter.Policy {
	t.Helper()
	owner := solana.Encode(bytes.Repeat([]byte{1}, 32))
	route := jupiterswap.Policy{
		Owner: owner, InputMint: orcaswap.WrappedSOLMint,
		OutputMint:     solana.Encode(bytes.Repeat([]byte{2}, 32)),
		MaxInputAmount: 1, MinOutputAmount: 1, MaxSlippageBPS: 1,
		MaxComputeUnits: 1, MaxComputeUnitPriceMicroLamport: 1,
		MaxFeeLamports: 1, MaxTokenAccountRentLamports: 1,
		RouteGuard: commandSubmitterRouteGuard(),
	}
	fingerprint, err := route.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	_, submitterPublicKey, err := sealedtx.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return submitter.Policy{
		Cluster: "mainnet-beta", Profile: jupiterswap.ProfileName,
		ProfileFingerprint: fingerprint, ControlStatePath: filepath.Join(dir, "control.json"),
		Source: owner, MaxLamports: 1, MaxFeeLamports: 1,
		ScheduleWindowSeconds: 3_600, ScheduleAnchorUnix: 86_400,
		MaxBlockHeightWindow: 150, RecoveryMode: submitter.MainnetRecoveryStopOnly,
		SubmitterPublicKey:   submitterPublicKey,
		AttestationPublicKey: solana.Encode(bytes.Repeat([]byte{3}, 32)),
		Evidence: proposalcheck.ProviderBindings{
			PrimaryTrustDomain: "primary", PrimaryOriginSHA256: strings.Repeat("1", 64),
			SecondaryTrustDomain: "secondary", SecondaryOriginSHA256: strings.Repeat("2", 64),
			ArchiveProbeSignature: solana.Encode(bytes.Repeat([]byte{4}, 64)),
		},
		Jupiter: &route,
	}
}

func commandSubmitterRouteGuard() jupiterswap.RouteGuardDeployment {
	return jupiterswap.RouteGuardDeployment{
		Program:        solana.Encode(bytes.Repeat([]byte{71}, 32)),
		ProgramData:    solana.Encode(bytes.Repeat([]byte{72}, 32)),
		DeploymentSlot: 123, CodeLength: 1, CodeSHA256: strings.Repeat("1", 64),
	}
}
