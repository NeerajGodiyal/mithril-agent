package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/operatorapproval"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestProposalApprovalCreateWritesOnlyAnExactDetachedApproval(t *testing.T) {
	var help bytes.Buffer
	if err := runProposalApprovalCreate(t.Context(), []string{"--help"}, &help); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"separate operator", "off-chain message", "cannot sign or send",
		"--request", "--authority-policy", "--out", "--signature",
	} {
		if !strings.Contains(help.String(), want) {
			t.Fatalf("proposal approval-create help is missing %q:\n%s", want, help.String())
		}
	}
	if err := runProposalApprovalCreate(t.Context(), nil, io.Discard); err == nil {
		t.Fatal("proposal approval-create accepted missing protected inputs")
	}

	authority, policy, _ := testJupiterPolicySet(t)
	request := approvalRequestForPolicy(t, authority, policy)
	validated, err := signer.ValidateJupiterRequest(policy, request)
	if err != nil {
		t.Fatal(err)
	}
	review, err := operatorapproval.BuildReview(authority.OperatorApprover, request, validated)
	if err != nil {
		t.Fatal(err)
	}
	approverSeed := sha256.Sum256([]byte("policy-check operator approver"))
	approverKey := ed25519.NewKeyFromSeed(approverSeed[:])
	signature := solana.Encode(ed25519.Sign(approverKey, []byte(review.Challenge)))

	dir := t.TempDir()
	policyPath := filepath.Join(dir, "authority-policy.json")
	requestPath := filepath.Join(dir, "signer-request.json")
	approvalPath := filepath.Join(dir, "operator-approval.json")
	writeJSON(t, policyPath, authority)
	writeJSON(t, requestPath, request)
	var output bytes.Buffer
	if err := runProposalApprovalCreate(t.Context(), []string{
		"--request", requestPath,
		"--authority-policy", policyPath,
		"--out", approvalPath,
		"--signature", signature,
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result proposalApprovalCreateResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "exact_request_approved_not_authorized" ||
		result.ApprovalPath != approvalPath ||
		result.Review.RequestSHA256 != review.RequestSHA256 ||
		result.AuthorizationMade || result.TransactionSigned ||
		result.TransactionSent || result.ProductionEnabled {
		t.Fatalf("approval result = %+v", result)
	}
	var approval operatorapproval.Approval
	if err := readStrictJSON(approvalPath, &approval); err != nil {
		t.Fatal(err)
	}
	if err := operatorapproval.Verify(
		authority.OperatorApprover, request, validated, approval,
	); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(approvalPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("approval permissions = %o, want 600", info.Mode().Perm())
	}
	for _, protected := range []string{
		request.MessageBase64,
		request.JupiterProviders.PrimaryOriginSHA256,
		request.JupiterProviders.SecondaryOriginSHA256,
		request.JupiterProviders.ArchiveProbeSignature,
		signature,
	} {
		if strings.Contains(output.String(), protected) {
			t.Fatal("proposal approval-create printed protected request or signature material")
		}
	}
	if err := runProposalApprovalCreate(t.Context(), []string{
		"--request", requestPath,
		"--authority-policy", policyPath,
		"--out", approvalPath,
		"--signature", signature,
	}, io.Discard); err == nil {
		t.Fatal("proposal approval-create replaced an existing approval")
	}
}

func approvalRequestForPolicy(
	t *testing.T,
	authorityPolicy policyauthority.Policy,
	policy signer.Policy,
) signer.Request {
	t.Helper()
	candidate := mainnetCandidateForPolicy(t, *policy.Jupiter)
	start := policy.ScheduleAnchorUnix + int64(policy.ScheduleWindowSeconds)
	actionID, err := jupiterswap.ComputeActionID(policy.ProfileFingerprint, start)
	if err != nil {
		t.Fatal(err)
	}
	return signer.Request{
		Domain: jupiterswap.RequestDomain, Cluster: policy.Cluster, Profile: policy.Profile,
		ProfileVersion: policy.ProfileVersion, ProfileFingerprint: policy.ProfileFingerprint,
		ActionID: actionID, ScheduleWindowStartUnix: start,
		ScheduleWindowEndUnix: start + int64(policy.ScheduleWindowSeconds),
		MessageBase64:         candidate.MessageBase64, BlockhashContextSlot: 90,
		FeeLamports: 5_000, FeeMinContextSlot: 90,
		PrimaryFeeContextSlot: 90, SecondaryFeeContextSlot: 91,
		RecentBlockhash:     solana.Encode(bytes.Repeat([]byte{9}, 32)),
		ObservedBlockHeight: 100, LastValidBlockHeight: candidate.LastValidBlockHeight,
		JupiterCandidate: &candidate, JupiterProviders: authorityPolicy.JupiterProviders,
	}
}
