package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/submitter"
)

func TestProposalPolicyCreateWritesOneConsistentAccountFreePolicySet(t *testing.T) {
	authorityFixture, signingFixture, _ := testJupiterPolicySet(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	routePath := filepath.Join(root, "route.json")
	resultPath := filepath.Join(root, "result.json")
	out := filepath.Join(root, "policies")
	route := *signingFixture.Jupiter
	writeJSON(t, routePath, route)
	writeJSON(t, resultPath, checkedResultForPolicyCreate(t, route, authorityFixture))

	fixedNow := time.Date(2026, 8, 14, 12, 34, 56, 0, time.UTC)
	var output bytes.Buffer
	if err := runProposalPolicyCreate([]string{
		"--route-policy", routePath,
		"--check-result", resultPath,
		"--out", out,
		"--risk-key-id", signingFixture.RiskAuthorityKeyID,
		"--risk-public-key", signingFixture.RiskAuthorityPublicKey,
		"--attestation-public-key", signingFixture.AttestationPublicKey,
		"--submitter-public-key", signingFixture.SubmitterPublicKey,
		"--operator-approver", authorityFixture.OperatorApprover,
		"--schedule-window-seconds", "3600",
	}, &output, func() time.Time { return fixedNow }); err != nil {
		t.Fatal(err)
	}

	var result proposalPolicyCreateResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	wantCap := route.MaxInputAmount + route.MaxFeeLamports + route.MaxTokenAccountRentLamports
	if result.Status != "policies_written_not_authorized" ||
		result.Directory != out || result.ScheduleWindowSecs != 3_600 ||
		result.ScheduleAnchorUnix != time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC).Unix() ||
		result.DailyDebitCap != wantCap || result.VendorAccountNeeded || result.KeysGenerated ||
		result.SigningEnabled || result.SubmissionEnabled {
		t.Fatalf("policy-create result = %+v", result)
	}

	var authority policyauthority.Policy
	var signing signer.Policy
	var submission submitter.Policy
	if err := readStrictJSON(result.AuthorityPolicy, &authority); err != nil {
		t.Fatal(err)
	}
	if err := readStrictJSON(result.SignerPolicy, &signing); err != nil {
		t.Fatal(err)
	}
	if err := readStrictJSON(result.SubmitterPolicy, &submission); err != nil {
		t.Fatal(err)
	}
	if err := validateJupiterPolicySet(authority, signing, submission); err != nil {
		t.Fatal(err)
	}
	if signing.DailyDebitCapLamports != wantCap ||
		authority.OperatorApprover != authorityFixture.OperatorApprover ||
		signing.AuthorizationLedgerPath != filepath.Join(out, "state", signerStateDirName, "authorizations.jsonl") ||
		submission.ControlStatePath != filepath.Join(out, "state", controlStateDirName, "control.json") ||
		submission.RecoveryMode != submitter.MainnetRecoveryStopOnly ||
		result.RecoveryMode != submitter.MainnetRecoveryStopOnly {
		t.Fatalf("generated state policy is wrong: signer=%+v submitter=%+v", signing, submission)
	}
	for _, path := range []string{out, filepath.Join(out, "state"), filepath.Join(out, "state", signerStateDirName), filepath.Join(out, "state", controlStateDirName)} {
		assertFileMode(t, path, 0o700)
	}
	for _, path := range []string{result.AuthorityPolicy, result.SignerPolicy, result.SubmitterPolicy} {
		assertFileMode(t, path, 0o600)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("policy bundle contains %d top-level entries, want 4", len(entries))
	}
	for _, entry := range entries {
		if strings.Contains(strings.ToLower(entry.Name()), "key") {
			t.Fatalf("policy-create generated key material: %s", entry.Name())
		}
	}
}

func TestProposalPolicyCreateWritesTokenInputCaps(t *testing.T) {
	authorityFixture, signingFixture, _ := testJupiterPolicySet(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	route := *signingFixture.Jupiter
	route.InputMint, route.OutputMint = route.OutputMint, route.InputMint
	routePath := filepath.Join(root, "route.json")
	resultPath := filepath.Join(root, "result.json")
	out := filepath.Join(root, "policies")
	writeJSON(t, routePath, route)
	writeJSON(t, resultPath, checkedResultForPolicyCreate(t, route, authorityFixture))

	var output bytes.Buffer
	if err := runProposalPolicyCreate([]string{
		"--route-policy", routePath,
		"--check-result", resultPath,
		"--out", out,
		"--risk-key-id", signingFixture.RiskAuthorityKeyID,
		"--risk-public-key", signingFixture.RiskAuthorityPublicKey,
		"--attestation-public-key", signingFixture.AttestationPublicKey,
		"--submitter-public-key", signingFixture.SubmitterPublicKey,
		"--operator-approver", authorityFixture.OperatorApprover,
		"--recovery-mode", submitter.MainnetRecoveryExactRetry,
	}, &output, func() time.Time { return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC) }); err != nil {
		t.Fatal(err)
	}

	var result proposalPolicyCreateResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.DailyDebitCap != 0 || result.DailyInputTokenCap != route.MaxInputAmount ||
		result.DailyNativeFeeCap != route.MaxFeeLamports ||
		result.RecoveryMode != submitter.MainnetRecoveryExactRetry {
		t.Fatalf("token-input policy result = %+v", result)
	}
	var authority policyauthority.Policy
	var signing signer.Policy
	var submission submitter.Policy
	if err := readStrictJSON(result.AuthorityPolicy, &authority); err != nil {
		t.Fatal(err)
	}
	if err := readStrictJSON(result.SignerPolicy, &signing); err != nil {
		t.Fatal(err)
	}
	if err := readStrictJSON(result.SubmitterPolicy, &submission); err != nil {
		t.Fatal(err)
	}
	if signing.MaxLamports != 0 || signing.DailyDebitCapLamports != 0 ||
		signing.MaxInputTokenAmount != route.MaxInputAmount ||
		signing.DailyInputTokenCap != route.MaxInputAmount ||
		signing.DailyNativeFeeCapLamports != route.MaxFeeLamports ||
		submission.MaxLamports != 0 ||
		submission.MaxInputTokenAmount != route.MaxInputAmount ||
		submission.RecoveryMode != submitter.MainnetRecoveryExactRetry {
		t.Fatalf("generated token-input caps are wrong: signer=%+v submitter=%+v", signing, submission)
	}
	if err := validateJupiterPolicySet(authority, signing, submission); err != nil {
		t.Fatal(err)
	}
}

func TestProposalPolicyCreateRejectsAuthorizedOrMismatchedResult(t *testing.T) {
	authorityFixture, signingFixture, _ := testJupiterPolicySet(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	route := *signingFixture.Jupiter
	routePath := filepath.Join(root, "route.json")
	resultPath := filepath.Join(root, "result.json")
	out := filepath.Join(root, "policies")
	writeJSON(t, routePath, route)
	checked := checkedResultForPolicyCreate(t, route, authorityFixture)
	checked.SigningEnabled = true
	writeJSON(t, resultPath, checked)

	err = runProposalPolicyCreate([]string{
		"--route-policy", routePath,
		"--check-result", resultPath,
		"--out", out,
		"--risk-key-id", signingFixture.RiskAuthorityKeyID,
		"--risk-public-key", signingFixture.RiskAuthorityPublicKey,
		"--attestation-public-key", signingFixture.AttestationPublicKey,
		"--submitter-public-key", signingFixture.SubmitterPublicKey,
		"--operator-approver", authorityFixture.OperatorApprover,
	}, io.Discard, time.Now)
	if err == nil {
		t.Fatal("policy-create accepted a result that claimed signing authority")
	}
	if _, statErr := os.Lstat(out); !os.IsNotExist(statErr) {
		t.Fatalf("policy-create left output after rejection: %v", statErr)
	}
}

func TestProposalPolicyCreateRejectsUnknownRecoveryModeBeforeWriting(t *testing.T) {
	authorityFixture, signingFixture, _ := testJupiterPolicySet(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	route := *signingFixture.Jupiter
	routePath := filepath.Join(root, "route.json")
	resultPath := filepath.Join(root, "result.json")
	out := filepath.Join(root, "policies")
	writeJSON(t, routePath, route)
	writeJSON(t, resultPath, checkedResultForPolicyCreate(t, route, authorityFixture))

	err = runProposalPolicyCreate([]string{
		"--route-policy", routePath, "--check-result", resultPath, "--out", out,
		"--risk-key-id", signingFixture.RiskAuthorityKeyID,
		"--risk-public-key", signingFixture.RiskAuthorityPublicKey,
		"--attestation-public-key", signingFixture.AttestationPublicKey,
		"--submitter-public-key", signingFixture.SubmitterPublicKey,
		"--operator-approver", authorityFixture.OperatorApprover,
		"--recovery-mode", "retry_anything",
	}, io.Discard, time.Now)
	if err == nil {
		t.Fatal("policy-create accepted an unknown recovery mode")
	}
	if _, statErr := os.Lstat(out); !os.IsNotExist(statErr) {
		t.Fatalf("policy-create left output after recovery-mode rejection: %v", statErr)
	}
}

func TestProposalPolicyCreateHelpStatesItsBoundary(t *testing.T) {
	var output bytes.Buffer
	if err := runProposalPolicyCreate([]string{"--help"}, &output, time.Now); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"offline", "needs no provider account", "writes no secret key",
		"cannot authorize, sign, or send", "--route-policy", "--check-result", "--out",
		"--operator-approver", "--recovery-mode", "stop_only", "exact_retry",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("proposal policy-create help is missing %q:\n%s", want, output.String())
		}
	}
}

func checkedResultForPolicyCreate(
	t *testing.T,
	route jupiterswap.Policy,
	authority policyauthority.Policy,
) proposalcheck.Result {
	t.Helper()
	fingerprint, err := route.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return proposalcheck.Result{
		Status:  proposalcheck.StatusCheckedNotAuthorized,
		Reason:  proposalcheck.ReasonSigningPolicyAbsent,
		Cluster: "mainnet-beta", PolicySHA256: fingerprint,
		InputMint: route.InputMint, OutputMint: route.OutputMint,
		InputAmount:           route.MaxInputAmount,
		MinimumOutput:         route.MinOutputAmount,
		FeeLamports:           route.MaxFeeLamports,
		PrimaryTrustDomain:    authority.JupiterProviders.PrimaryTrustDomain,
		PrimaryOriginSHA256:   authority.JupiterProviders.PrimaryOriginSHA256,
		SecondaryTrustDomain:  authority.JupiterProviders.SecondaryTrustDomain,
		SecondaryOriginSHA256: authority.JupiterProviders.SecondaryOriginSHA256,
		ArchiveProbeSignature: authority.JupiterProviders.ArchiveProbeSignature,
	}
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
