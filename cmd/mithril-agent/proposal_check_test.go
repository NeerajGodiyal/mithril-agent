package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/operatorapproval"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/policyclient"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/signerclient"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/submitterclient"
	"github.com/Overclock-Validator/mithril-agent/turnkeycustody"
)

func TestProposalHelpShowsTheSafeOrderWithoutCombiningPrivateKeys(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"proposal", "help"}, &output); err != nil {
		t.Fatal(err)
	}
	help := output.String()
	wants := []string{
		"1. Qualify two independent read providers",
		"proposal evidence-check",
		"2. Build one keyless candidate",
		"proposal check",
		"proposal recheck",
		"3. Create public identities on their separate hosts",
		"proposal key-create",
		"proposal policy-create",
		"proposal policy-check",
		"proposal bundle-check",
		"4. Prove each installed identity",
		"proposal self-hosted-check",
		"proposal authority-check",
		"proposal submitter-check",
		"5. Prepare the exact unsigned request",
		"proposal prepare",
		"6. Decode and review that exact request",
		"proposal review",
		"7. Approve only that exact request",
		"proposal approval-create",
		"8. After separate grant, signing, and offline submitter preparation",
		"proposal canary-check",
		"None can submit a transaction",
	}
	last := -1
	for _, want := range wants {
		at := strings.Index(help, want)
		if at < 0 {
			t.Fatalf("proposal help is missing %q:\n%s", want, help)
		}
		if at < last {
			t.Fatalf("proposal help puts %q out of order:\n%s", want, help)
		}
		last = at
	}
	if strings.Contains(help, "create all keys") {
		t.Fatalf("proposal help combines separated private identities:\n%s", help)
	}
}

func TestProposalCheckHelpMakesTheAuthorityBoundaryClear(t *testing.T) {
	var output bytes.Buffer
	if err := runProposalCheck(t.Context(), []string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "cannot sign or send") || !strings.Contains(text, "no private key is read") {
		t.Fatalf("unsafe or unclear proposal help:\n%s", text)
	}
	if !strings.Contains(text, "must already exist") ||
		!strings.Contains(text, "funded canonical input account") ||
		!strings.Contains(text, "native SOL returns directly") {
		t.Fatalf("proposal help omits a direction-specific account boundary:\n%s", text)
	}
	if !strings.Contains(text, "--primary-trust-domain") ||
		!strings.Contains(text, "--secondary-trust-domain") ||
		!strings.Contains(text, "--archive-probe-signature") ||
		!strings.Contains(text, "--candidate-output") ||
		!strings.Contains(text, "--policy-output") ||
		!strings.Contains(text, "--result-output") ||
		!strings.Contains(text, "--input-mint") {
		t.Fatalf("proposal check does not require provider ownership labels:\n%s", text)
	}
	if err := runProposalCheck(t.Context(), nil, &output); err == nil {
		t.Fatal("proposal check accepted missing public inputs")
	}
	tokenMint := solana.Encode(bytes.Repeat([]byte{4}, 32))
	if err := runProposalCheck(t.Context(), []string{
		"--taker", solana.Encode(bytes.Repeat([]byte{3}, 32)),
		"--input-mint", tokenMint, "--output-mint", solana.Encode(bytes.Repeat([]byte{5}, 32)),
		"--amount", "1", "--minimum-output", "1", "--max-compute-units", "1",
		"--max-cu-price", "1", "--max-fee-lamports", "1", "--max-account-rent", "1",
		"--primary-trust-domain", "primary-provider",
		"--secondary-trust-domain", "secondary-provider",
		"--archive-probe-signature", solana.Encode(bytes.Repeat([]byte{6}, 64)),
	}, &output); err == nil {
		t.Fatal("proposal check accepted token-to-token input")
	}
	if err := runProposalCheck(t.Context(), []string{
		"--taker", "wallet", "--output-mint", "mint", "--amount", "1",
		"--minimum-output", "1", "--max-compute-units", "1", "--max-cu-price", "1",
		"--max-fee-lamports", "1", "--max-account-rent", "1",
		"--primary-trust-domain", "same-provider",
		"--secondary-trust-domain", "same-provider",
	}, &output); err == nil {
		t.Fatal("proposal check accepted one provider trust domain twice")
	}
	if err := runProposalCheck(t.Context(), []string{
		"--taker", solana.Encode(bytes.Repeat([]byte{1}, 32)),
		"--output-mint", solana.Encode(bytes.Repeat([]byte{2}, 32)),
		"--amount", "1", "--minimum-output", "1", "--max-compute-units", "1",
		"--max-cu-price", "1", "--max-fee-lamports", "1", "--max-account-rent", "1",
		"--primary-trust-domain", "primary-provider",
		"--secondary-trust-domain", "secondary-provider",
		"--candidate-output", "/private/tmp/candidate.json",
	}, &output); err == nil {
		t.Fatal("proposal check accepted a candidate output without its protected policy")
	}
}

func TestProposalEvidenceCheckNeedsNoWalletAndPrintsNoEndpoint(t *testing.T) {
	var output bytes.Buffer
	if err := runProposalEvidenceCheck(t.Context(), []string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"needs no wallet or provider account", "reads no signing key", "cannot sign or send",
		"not an availability or SLA qualification",
		"--primary-trust-domain", "--secondary-trust-domain", "--archive-probe-signature",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("proposal evidence-check help is missing %q:\n%s", want, output.String())
		}
	}
	if err := runProposalEvidenceCheck(t.Context(), nil, io.Discard); err == nil {
		t.Fatal("proposal evidence-check accepted missing provider bindings")
	}
	probe := solana.Encode(bytes.Repeat([]byte{9}, 64))
	if err := runProposalEvidenceCheck(t.Context(), []string{
		"--primary-trust-domain", "same-provider",
		"--secondary-trust-domain", "same-provider",
		"--archive-probe-signature", probe,
	}, io.Discard); err == nil {
		t.Fatal("proposal evidence-check accepted one provider owner twice")
	}

	primaryURL := "https://primary.invalid/private"
	secondaryURL := "https://secondary.invalid/private"
	t.Setenv("MITHRIL_AGENT_PRIMARY_RPC_URL", primaryURL)
	t.Setenv("MITHRIL_AGENT_SECONDARY_RPC_URL", secondaryURL)
	oldQualify := qualifyProposalEvidence
	t.Cleanup(func() { qualifyProposalEvidence = oldQualify })
	qualifyProposalEvidence = func(
		ctx context.Context,
		primary, secondary string,
		bindings proposalcheck.ProviderBindings,
	) (proposalcheck.ProviderBindings, error) {
		if ctx == nil || primary != primaryURL || secondary != secondaryURL ||
			bindings.PrimaryTrustDomain != "provider-one" ||
			bindings.SecondaryTrustDomain != "provider-two" ||
			bindings.ArchiveProbeSignature != probe || bindings.PrimaryOriginSHA256 != "" ||
			bindings.SecondaryOriginSHA256 != "" {
			t.Fatal("proposal evidence-check passed the wrong protected bindings")
		}
		bindings.PrimaryOriginSHA256 = strings.Repeat("1", 64)
		bindings.SecondaryOriginSHA256 = strings.Repeat("2", 64)
		return bindings, nil
	}
	output.Reset()
	if err := runProposalEvidenceCheck(t.Context(), []string{
		"--primary-trust-domain", "provider-one",
		"--secondary-trust-domain", "provider-two",
		"--archive-probe-signature", probe,
	}, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		`"status":"evidence_providers_verified"`,
		`"primary_origin_sha256":"` + strings.Repeat("1", 64) + `"`,
		`"secondary_origin_sha256":"` + strings.Repeat("2", 64) + `"`,
		`"vendor_account_required":false`, `"wallet_required":false`,
		`"sla_qualified":false`, `"production_ready":false`,
		`"can_sign":false`, `"can_submit":false`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("proposal evidence-check result is missing %q: %s", want, text)
		}
	}
	for _, private := range []string{primaryURL, secondaryURL, probe} {
		if strings.Contains(text, private) {
			t.Fatalf("proposal evidence-check printed protected input: %s", text)
		}
	}
}

func TestProposalRecheckHelpAndLocalValidationStayReadOnly(t *testing.T) {
	var output bytes.Buffer
	if err := runProposalRecheck(t.Context(), []string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		"cannot sign or send", "--candidate", "--policy", "--primary-origin-sha256",
		"--secondary-origin-sha256", "--archive-probe-signature",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("proposal recheck help is missing %q:\n%s", want, text)
		}
	}
	if err := runProposalRecheck(t.Context(), nil, &output); err == nil {
		t.Fatal("proposal recheck accepted missing protected inputs")
	}
}

func TestProposalPrepareHelpAndLocalValidationStayNonAuthorizing(t *testing.T) {
	var output bytes.Buffer
	if err := runProposalPrepare(
		t.Context(), []string{"--help"}, &output, time.Now,
	); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		"unsigned, ungranted", "cannot authorize, sign, or send",
		"--candidate", "--authority-policy", "--schedule-start",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("proposal prepare help is missing %q:\n%s", want, text)
		}
	}
	if err := runProposalPrepare(t.Context(), nil, &output, time.Now); err == nil {
		t.Fatal("proposal prepare accepted missing protected inputs")
	}
}

func TestProposalReviewDecodesExactRequestWithoutAuthority(t *testing.T) {
	var help bytes.Buffer
	if err := runProposalReview([]string{"--help"}, &help); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"does not validate or create authorization", "cannot sign or send",
		"--request", "--signer-policy",
	} {
		if !strings.Contains(help.String(), want) {
			t.Fatalf("proposal review help is missing %q:\n%s", want, help.String())
		}
	}
	if err := runProposalReview(nil, io.Discard); err == nil {
		t.Fatal("proposal review accepted missing protected inputs")
	}

	authority, policy, _ := testJupiterPolicySet(t)
	candidate := mainnetCandidateForPolicy(t, *policy.Jupiter)
	start := policy.ScheduleAnchorUnix + int64(policy.ScheduleWindowSeconds)
	actionID, err := jupiterswap.ComputeActionID(policy.ProfileFingerprint, start)
	if err != nil {
		t.Fatal(err)
	}
	request := signer.Request{
		Domain: jupiterswap.RequestDomain, Cluster: policy.Cluster, Profile: policy.Profile,
		ProfileVersion: policy.ProfileVersion, ProfileFingerprint: policy.ProfileFingerprint,
		ActionID: actionID, ScheduleWindowStartUnix: start,
		ScheduleWindowEndUnix: start + int64(policy.ScheduleWindowSeconds),
		MessageBase64:         candidate.MessageBase64, BlockhashContextSlot: 90,
		FeeLamports: 5_000, FeeMinContextSlot: 90,
		PrimaryFeeContextSlot: 90, SecondaryFeeContextSlot: 91,
		RecentBlockhash:     solana.Encode(bytes.Repeat([]byte{9}, 32)),
		ObservedBlockHeight: 100, LastValidBlockHeight: candidate.LastValidBlockHeight,
		JupiterCandidate: &candidate, JupiterProviders: authority.JupiterProviders,
	}
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "signer-policy.json")
	requestPath := filepath.Join(dir, "signer-request.json")
	writeJSON(t, policyPath, policy)
	writeJSON(t, requestPath, request)

	var output bytes.Buffer
	if err := runProposalReview([]string{
		"--request", requestPath, "--signer-policy", policyPath,
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result proposalReviewResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "request_matches_signer_policy_not_authorized" ||
		result.Direction != "buy_token_with_sol" || result.Source != policy.Source ||
		result.InputMint != policy.Jupiter.InputMint ||
		result.OutputMint != policy.Jupiter.OutputMint ||
		result.InputAmountBaseUnits != candidate.Request.InputAmount ||
		result.MinimumOutputBaseUnits != candidate.Quote.MinimumOutput ||
		result.TransactionFeeLamports != request.FeeLamports ||
		result.ScheduleWindowStartUnix != start || result.ActionID != actionID ||
		len(result.MessageSHA256) != sha256.Size*2 || !result.ReviewRequired ||
		result.AuthorizationChecked || result.SigningEnabled || result.SubmissionEnabled ||
		result.ProductionReady {
		t.Fatalf("proposal review result = %+v", result)
	}
	for _, protected := range []string{
		request.MessageBase64,
		request.JupiterProviders.PrimaryOriginSHA256,
		request.JupiterProviders.SecondaryOriginSHA256,
		request.JupiterProviders.ArchiveProbeSignature,
	} {
		if strings.Contains(output.String(), protected) {
			t.Fatal("proposal review printed protected request material")
		}
	}

	request.FeeLamports = policy.MaxFeeLamports + 1
	writeJSON(t, requestPath, request)
	if err := runProposalReview([]string{
		"--request", requestPath, "--signer-policy", policyPath,
	}, io.Discard); err == nil {
		t.Fatal("proposal review accepted a request outside signer policy")
	}
}

func TestActiveScheduleWindowStart(t *testing.T) {
	anchor := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	policy := signer.Policy{
		ScheduleWindowSeconds: 3_600,
		ScheduleAnchorUnix:    anchor.Unix(),
	}
	got, err := activeScheduleWindowStart(policy, anchor.Add(12*time.Hour+34*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if want := anchor.Add(12 * time.Hour).Unix(); got != want {
		t.Fatalf("active schedule window start = %d, want %d", got, want)
	}
	if _, err := activeScheduleWindowStart(policy, anchor.Add(-time.Second)); err == nil {
		t.Fatal("accepted a time before the protected schedule anchor")
	}
}

func TestProposalTurnkeyPolicyHelpAndValidationStayOffline(t *testing.T) {
	var output bytes.Buffer
	if err := runProposalTurnkeyPolicy([]string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		"unfunded", "not contact Turnkey", "install a policy",
		"--candidate", "--policy", "--api-user", "--out",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("proposal turnkey-policy help is missing %q:\n%s", want, text)
		}
	}
	if err := runProposalTurnkeyPolicy(nil, &output); err == nil {
		t.Fatal("proposal turnkey-policy accepted missing protected inputs")
	}
	if err := runProposalTurnkeyPolicy([]string{
		"--candidate", "/private/tmp/candidate.json",
		"--policy", "/private/tmp/policy.json",
		"--api-user", "non-root-user",
		"--out", "/private/tmp/candidate.json",
	}, &output); err == nil {
		t.Fatal("proposal turnkey-policy accepted an output that overwrites its candidate")
	}
}

func TestTurnkeyQualificationPolicyOutputCapIncludesJSONEnvelope(t *testing.T) {
	document := turnkeycustody.QualificationPolicy{
		PolicyName: "qualification",
		Effect:     "EFFECT_ALLOW",
		Consensus:  "approvers.any(user, user.id == 'qualification-user')",
		Condition:  strings.Repeat("x", 64<<10),
		Notes:      "unfunded qualification",
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "qualification.json")
	if err := writeTurnkeyQualificationPolicy(path, document); err != nil {
		t.Fatalf("write maximum-size qualification policy: %v", err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) <= maxInputBytes {
		t.Fatal("test policy does not exercise the JSON-envelope boundary")
	}
	if len(encoded) > maxTurnkeyQualificationPolicyBytes {
		t.Fatalf("qualification policy needs %d bytes, output cap is %d",
			len(encoded), maxTurnkeyQualificationPolicyBytes)
	}
}

func TestProposalTurnkeyCheckIsReadOnlyAndDoesNotPrintIdentifiers(t *testing.T) {
	var output bytes.Buffer
	if err := runProposalTurnkeyCheck(t.Context(), []string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"read-only", "creates no signing activity", "cannot sign or send",
		"--api-key-file", "--api-public-key", "--organization", "--sign-with",
		"--expected-address", "mode-0600 .private", "activity JSON is only a receipt",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("proposal turnkey-check help is missing %q:\n%s", want, output.String())
		}
	}
	if err := runProposalTurnkeyCheck(t.Context(), nil, io.Discard); err == nil {
		t.Fatal("proposal turnkey-check accepted missing identity inputs")
	}

	oldVerify := verifyTurnkeyIdentity
	t.Cleanup(func() { verifyTurnkeyIdentity = oldVerify })
	const (
		publicKey      = "registered-public-key"
		organizationID = "organization-id"
		signWith       = "signing-resource"
		expected       = "11111111111111111111111111111111"
	)
	apiKeyPath := filepath.Join(t.TempDir(), "turnkey.private")
	verifyTurnkeyIdentity = func(
		ctx context.Context,
		path, gotPublicKey string,
		config turnkeycustody.Config,
		expectedAddress string,
	) error {
		if ctx == nil || path != apiKeyPath || gotPublicKey != publicKey ||
			config.OrganizationID != organizationID || config.SignWith != signWith ||
			expectedAddress != expected {
			t.Fatalf("Turnkey identity check received the wrong binding")
		}
		return nil
	}
	output.Reset()
	if err := runProposalTurnkeyCheck(t.Context(), []string{
		"--api-key-file", apiKeyPath,
		"--api-public-key", publicKey,
		"--organization", organizationID,
		"--sign-with", signWith,
		"--expected-address", expected,
	}, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, `"status":"turnkey_identity_verified"`) ||
		!strings.Contains(text, `"signing_activity":false`) ||
		!strings.Contains(text, `"can_submit":false`) {
		t.Fatalf("Turnkey identity result = %s", text)
	}
	for _, private := range []string{
		apiKeyPath, publicKey, organizationID, signWith, expected,
	} {
		if strings.Contains(text, private) {
			t.Fatalf("Turnkey identity result printed a protected input: %s", text)
		}
	}
}

func TestProposalSelfHostedCheckIsAccountFreeAndReadOnly(t *testing.T) {
	var output bytes.Buffer
	if err := runProposalSelfHostedCheck(t.Context(), []string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"needs no vendor account", "requests no signature", "cannot send",
		"--identity-file", "--known-hosts", "--policy", "--wallet-public-key",
		"--attestation-public-key", "--submitter-public-key", "--profile-sha256",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("proposal self-hosted-check help is missing %q:\n%s", want, output.String())
		}
	}
	if err := runProposalSelfHostedCheck(t.Context(), nil, io.Discard); err == nil {
		t.Fatal("proposal self-hosted-check accepted missing pinned inputs")
	}

	oldVerify := verifySelfHostedSignerIdentity
	t.Cleanup(func() { verifySelfHostedSignerIdentity = oldVerify })
	root := t.TempDir()
	identityPath := filepath.Join(root, "transport-key")
	knownHostsPath := filepath.Join(root, "known-hosts")
	wallet := solana.Encode(bytes.Repeat([]byte{31}, 32))
	attestor := solana.Encode(bytes.Repeat([]byte{32}, 32))
	submitter := strings.Repeat("a", 64)
	profileSHA256 := strings.Repeat("b", 64)
	if err := validateSelfHostedIdentityPins(wallet, wallet, submitter); err == nil {
		t.Fatal("self-hosted identity check accepted a funded wallet as its attestor")
	}
	walletBytes, err := solana.Decode32(wallet)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSelfHostedIdentityPins(
		wallet, attestor, hex.EncodeToString(walletBytes[:]),
	); err == nil {
		t.Fatal("self-hosted identity check accepted the funded wallet as its submitter")
	}
	verifySelfHostedSignerIdentity = func(ctx context.Context, config signerclient.Config) error {
		if ctx == nil {
			t.Fatal("self-hosted identity check received a nil context")
		}
		deadline, bounded := ctx.Deadline()
		if !bounded || time.Until(deadline) <= 0 ||
			config.SSH == nil || config.SSH.Command != "/usr/bin/ssh" ||
			config.SSH.Host != "signer.example" || config.SSH.User != "mithril-signer" ||
			config.SSH.Port != 2222 || config.SSH.IdentityPath != identityPath ||
			config.SSH.KnownHostsPath != knownHostsPath ||
			config.ExpectedWalletPublicKey != wallet ||
			config.ExpectedAttestationPublicKey != attestor ||
			config.ExpectedSubmitterPublicKey != submitter ||
			config.ExpectedProfileSHA256 != profileSHA256 {
			t.Fatal("self-hosted identity check received the wrong binding")
		}
		return nil
	}
	output.Reset()
	if err := runProposalSelfHostedCheck(t.Context(), []string{
		"--host", "signer.example", "--user", "mithril-signer", "--port", "2222",
		"--identity-file", identityPath, "--known-hosts", knownHostsPath,
		"--wallet-public-key", wallet, "--attestation-public-key", attestor,
		"--submitter-public-key", submitter, "--profile-sha256", profileSHA256,
	}, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		`"status":"self_hosted_signer_identity_verified"`,
		`"vendor_account_required":false`, `"signing_activity":false`,
		`"can_submit":false`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("self-hosted identity result is missing %q: %s", want, text)
		}
	}
	for _, private := range []string{
		"signer.example", "mithril-signer", identityPath, knownHostsPath,
		wallet, attestor, submitter, profileSHA256,
	} {
		if strings.Contains(text, private) {
			t.Fatalf("self-hosted identity result printed a protected input: %s", text)
		}
	}
}

func TestProposalSelfHostedCheckDerivesEveryPinFromProtectedPolicy(t *testing.T) {
	_, signing, _ := testJupiterPolicySet(t)
	root := t.TempDir()
	policyPath := filepath.Join(root, "signer-policy.json")
	identityPath := filepath.Join(root, "transport-key")
	knownHostsPath := filepath.Join(root, "known-hosts")
	writeJSON(t, policyPath, signing)

	oldVerify := verifySelfHostedSignerIdentity
	t.Cleanup(func() { verifySelfHostedSignerIdentity = oldVerify })
	verifySelfHostedSignerIdentity = func(_ context.Context, config signerclient.Config) error {
		if config.ExpectedWalletPublicKey != signing.Source ||
			config.ExpectedAttestationPublicKey != signing.AttestationPublicKey ||
			config.ExpectedSubmitterPublicKey != signing.SubmitterPublicKey ||
			config.ExpectedProfileSHA256 != signing.ProfileFingerprint {
			t.Fatalf("self-hosted policy pins = %+v", config)
		}
		return nil
	}

	var output bytes.Buffer
	common := []string{
		"--host", "signer.example", "--user", "mithril-signer",
		"--identity-file", identityPath, "--known-hosts", knownHostsPath,
	}
	if err := runProposalSelfHostedCheck(
		t.Context(), append(common, "--policy", policyPath), &output,
	); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"status":"self_hosted_signer_identity_verified"`) {
		t.Fatalf("self-hosted policy check output = %s", output.String())
	}
	if err := runProposalSelfHostedCheck(t.Context(), append(
		common, "--policy", policyPath, "--wallet-public-key", signing.Source,
	), io.Discard); err == nil {
		t.Fatal("self-hosted check accepted both a protected policy and manual pins")
	}
	invalidPath := filepath.Join(root, "invalid-signer-policy.json")
	signing.ProfileFingerprint = strings.Repeat("0", 64)
	writeJSON(t, invalidPath, signing)
	if err := runProposalSelfHostedCheck(
		t.Context(), append(common, "--policy", invalidPath), io.Discard,
	); err == nil {
		t.Fatal("self-hosted check accepted an invalid signer policy")
	}
}

func TestProposalSubmitterCheckBindsTheInstalledKeyWithoutRPC(t *testing.T) {
	var help bytes.Buffer
	if err := runProposalSubmitterCheck(t.Context(), []string{"--help"}, &help); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"identity-only", "RPC environment", "cannot sign or submit", "--policy", "--key", "--command",
	} {
		if !strings.Contains(help.String(), want) {
			t.Fatalf("proposal submitter-check help is missing %q:\n%s", want, help.String())
		}
	}

	_, _, submission := testJupiterPolicySet(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(dir, "submitter-policy.json")
	keyPath := filepath.Join(dir, "submitter-key.json")
	commandPath := filepath.Join(dir, "submitter")
	writeJSON(t, policyPath, submission)
	if err := os.WriteFile(keyPath, []byte("test-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantIdentity := submitterclient.Identity{
		PublicKey:          submission.SubmitterPublicKey,
		ProfileFingerprint: submission.ProfileFingerprint,
		Source:             submission.Source,
	}
	writeIdentityCommand(t, commandPath, wantIdentity)
	t.Setenv("MITHRIL_AGENT_MITHRIL_RPC_URL", "must-not-reach-child")
	t.Setenv("MITHRIL_AGENT_PRIMARY_RPC_URL", "must-not-reach-child")
	t.Setenv("MITHRIL_AGENT_SECONDARY_RPC_URL", "must-not-reach-child")

	var output bytes.Buffer
	args := []string{
		"--command", commandPath, "--policy", policyPath, "--key", keyPath,
	}
	if err := runProposalSubmitterCheck(t.Context(), args, &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status          string `json:"status"`
		SigningActivity bool   `json:"signing_activity"`
		CanSubmit       bool   `json:"can_submit"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "submitter_identity_verified" || result.SigningActivity || result.CanSubmit {
		t.Fatalf("submitter-check result = %+v", result)
	}

	wrong := wantIdentity
	wrong.ProfileFingerprint = strings.Repeat("0", 64)
	writeIdentityCommand(t, commandPath, wrong)
	if err := runProposalSubmitterCheck(t.Context(), args, io.Discard); err == nil {
		t.Fatal("submitter-check accepted a key bound to a different profile")
	}
}

func TestProposalAuthorityCheckBindsTheInstalledKeyWithoutGranting(t *testing.T) {
	var help bytes.Buffer
	if err := runProposalAuthorityCheck(t.Context(), []string{"--help"}, &help); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"identity-only", "RPC environment", "cannot authorize, sign, or submit",
		"--policy", "--key", "--command",
	} {
		if !strings.Contains(help.String(), want) {
			t.Fatalf("proposal authority-check help is missing %q:\n%s", want, help.String())
		}
	}

	authority, _, _ := testJupiterPolicySet(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(dir, "authority-policy.json")
	keyPath := filepath.Join(dir, "risk-key.json")
	commandPath := filepath.Join(dir, "policy-authority")
	writeJSON(t, policyPath, authority)
	if err := os.WriteFile(keyPath, []byte("test-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantIdentity := policyclient.Identity{
		KeyID:     authority.TransactionPolicy.RiskAuthorityKeyID,
		PublicKey: authority.TransactionPolicy.RiskAuthorityPublicKey,
	}
	writeIdentityCommand(t, commandPath, wantIdentity)
	t.Setenv("MITHRIL_AGENT_MITHRIL_RPC_URL", "must-not-reach-child")
	t.Setenv("MITHRIL_AGENT_PRIMARY_RPC_URL", "must-not-reach-child")
	t.Setenv("MITHRIL_AGENT_SECONDARY_RPC_URL", "must-not-reach-child")

	var output bytes.Buffer
	args := []string{
		"--command", commandPath, "--policy", policyPath, "--key", keyPath,
	}
	if err := runProposalAuthorityCheck(t.Context(), args, &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status                string `json:"status"`
		AuthorizationActivity bool   `json:"authorization_activity"`
		CanSign               bool   `json:"can_sign"`
		CanSubmit             bool   `json:"can_submit"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "risk_authority_identity_verified" ||
		result.AuthorizationActivity || result.CanSign || result.CanSubmit {
		t.Fatalf("authority-check result = %+v", result)
	}

	wrong := wantIdentity
	wrong.KeyID = "different-authority"
	writeIdentityCommand(t, commandPath, wrong)
	if err := runProposalAuthorityCheck(t.Context(), args, io.Discard); err == nil {
		t.Fatal("authority-check accepted a different risk-authority key")
	}
}

func TestProposalCanaryCheckBindsStoppedControlAndPreparedReadiness(t *testing.T) {
	var help bytes.Buffer
	if err := runProposalCanaryCheck(t.Context(), []string{"--help"}, &help); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"operator socket policy", "stopped control revision", "reads no key",
		"cannot enable, sign, or", "does not approve profitability",
		"cannot be upgraded within the checked transaction", "provider SLAs",
		"--policy-dir", "--operator-socket", "--request", "--operator-approval",
		"--shadow-policy", "--shadow-dir",
		"--shadow-days", "--command",
	} {
		if !strings.Contains(help.String(), want) {
			t.Fatalf("proposal canary-check help is missing %q:\n%s", want, help.String())
		}
	}

	authority, signing, submission := testJupiterPolicySet(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(dir, proposalAuthorityPolicyName), authority)
	writeJSON(t, filepath.Join(dir, proposalSignerPolicyName), signing)
	writeJSON(t, filepath.Join(dir, proposalSubmitterPolicyName), submission)
	commandPath := filepath.Join(dir, "submitter")
	socketPath := filepath.Join(dir, "operator.sock")
	shadowPolicyPath := filepath.Join(dir, "shadow-policy.json")
	shadowDirectory := filepath.Join(dir, "shadow-evidence")
	requestPath := filepath.Join(dir, "approved-request.json")
	approvalPath := filepath.Join(dir, "operator-approval.json")
	request := approvalRequestForPolicy(t, authority, signing)
	validated, err := signer.ValidateJupiterRequest(signing, request)
	if err != nil {
		t.Fatal(err)
	}
	review, err := operatorapproval.BuildReview(authority.OperatorApprover, request, validated)
	if err != nil {
		t.Fatal(err)
	}
	approverSeed := sha256.Sum256([]byte("policy-check operator approver"))
	approval, err := operatorapproval.Create(
		authority.OperatorApprover, request, validated,
		solana.Encode(ed25519.Sign(ed25519.NewKeyFromSeed(approverSeed[:]), []byte(review.Challenge))),
	)
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(t, requestPath, request)
	writeJSON(t, approvalPath, approval)
	t.Setenv("MITHRIL_AGENT_MITHRIL_RPC_URL", "local-node")
	t.Setenv("MITHRIL_AGENT_PRIMARY_RPC_URL", "primary-provider")
	t.Setenv("MITHRIL_AGENT_SECONDARY_RPC_URL", "secondary-provider")

	originalInspect := inspectProposalCanaryControl
	originalReadiness := checkProposalMainnetReadiness
	originalShadow := checkProposalShadowEvidence
	t.Cleanup(func() {
		inspectProposalCanaryControl = originalInspect
		checkProposalMainnetReadiness = originalReadiness
		checkProposalShadowEvidence = originalShadow
	})
	revision := strings.Repeat("c", 64)
	actionID := request.ActionID
	shadowHash := strings.Repeat("e", 64)
	shadowCalls := 0
	checkProposalShadowEvidence = func(
		got signer.Policy, policyPath, directory string, days uint32, now time.Time,
	) (shadowReviewResult, error) {
		shadowCalls++
		if got.ProfileFingerprint != signing.ProfileFingerprint ||
			policyPath != shadowPolicyPath || directory != shadowDirectory ||
			days != 7 || now.Location() != time.UTC {
			t.Fatal("canary-check used the wrong shadow evidence binding")
		}
		return shadowReviewResult{PolicySHA256: shadowHash, CompleteDays: days}, nil
	}
	inspectProposalCanaryControl = func(
		socket string,
		expected submitterclient.Identity,
	) (control.Status, string, error) {
		if socket != socketPath || expected != (submitterclient.Identity{
			PublicKey:          submission.SubmitterPublicKey,
			ProfileFingerprint: submission.ProfileFingerprint,
			Source:             submission.Source,
		}) {
			t.Fatal("canary-check inspected the wrong protected operator binding")
		}
		return control.Status{Mode: control.ModeNoNewActions}, revision, nil
	}
	readinessCalls := 0
	checkProposalMainnetReadiness = func(
		ctx context.Context,
		command,
		policy string,
		environment []string,
	) (string, error) {
		readinessCalls++
		if ctx == nil || command != commandPath ||
			policy != filepath.Join(dir, proposalSubmitterPolicyName) {
			t.Fatal("canary-check used the wrong submitter command or policy")
		}
		joined := strings.Join(environment, "\n")
		for _, want := range []string{
			"MITHRIL_AGENT_MITHRIL_RPC_URL=local-node",
			"MITHRIL_AGENT_PRIMARY_RPC_URL=primary-provider",
			"MITHRIL_AGENT_SECONDARY_RPC_URL=secondary-provider",
		} {
			if !strings.Contains(joined, want) {
				t.Fatalf("canary-check environment is missing %q", want)
			}
		}
		return actionID, nil
	}

	args := []string{
		"--policy-dir", dir, "--operator-socket", socketPath, "--command", commandPath,
		"--request", requestPath, "--operator-approval", approvalPath,
		"--shadow-policy", shadowPolicyPath, "--shadow-dir", shadowDirectory,
		"--shadow-days", "7",
	}
	var output bytes.Buffer
	if err := runProposalCanaryCheck(t.Context(), args, &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status             string `json:"status"`
		ActionID           string `json:"action_id"`
		ControlRevision    string `json:"control_revision"`
		ControlEnabled     bool   `json:"control_enabled"`
		StrategyEvidence   bool   `json:"strategy_evidence_complete"`
		ShadowPolicy       string `json:"shadow_policy_sha256"`
		ShadowDays         uint32 `json:"shadow_complete_days"`
		StrategyApproved   bool   `json:"strategy_approved"`
		ApprovedRequest    string `json:"approved_request_sha256"`
		RecoveryMode       string `json:"recovery_mode"`
		ProductionReady    bool   `json:"production_ready"`
		RouteUpgradeAtomic bool   `json:"route_upgrade_atomic"`
		RouteProtection    string `json:"route_upgrade_protection"`
		CanSign            bool   `json:"can_sign"`
		CanSubmit          bool   `json:"can_submit"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`"production_ready":false`, `"route_upgrade_atomic":true`,
		`"route_upgrade_protection":"immutable_guard_exact_code_pinned"`,
	} {
		if !strings.Contains(output.String(), field) {
			t.Fatalf("canary-check omitted %s: %s", field, output.String())
		}
	}
	if result.Status != "mainnet_canary_evidence_ready_not_enabled" ||
		result.ActionID != actionID || result.ControlRevision != revision ||
		!result.StrategyEvidence || result.ShadowPolicy != shadowHash ||
		result.ShadowDays != 7 || result.RecoveryMode != submission.RecoveryMode ||
		result.ControlEnabled || !result.StrategyApproved ||
		result.ApprovedRequest != review.RequestSHA256 || result.ProductionReady ||
		!result.RouteUpgradeAtomic ||
		result.RouteProtection != "immutable_guard_exact_code_pinned" ||
		result.CanSign || result.CanSubmit || readinessCalls != 1 || shadowCalls != 1 {
		t.Fatalf("canary-check result = %+v, readiness calls = %d", result, readinessCalls)
	}

	inspectProposalCanaryControl = func(
		string, submitterclient.Identity,
	) (control.Status, string, error) {
		return control.Status{
			Mode: control.ModeMainnetCanary, ExpectedActionID: actionID,
			ExpiresAt:  time.Now().Add(time.Minute),
			MaxActions: 1, RemainingActions: 1,
		}, revision, nil
	}
	if err := runProposalCanaryCheck(t.Context(), args, io.Discard); err == nil {
		t.Fatal("canary-check accepted an already-enabled control state")
	}
	if readinessCalls != 1 {
		t.Fatal("canary-check reached providers after finding enabled control state")
	}
	if shadowCalls != 1 {
		t.Fatal("canary-check replayed shadow evidence after finding enabled control state")
	}
}

func writeIdentityCommand(t *testing.T, path string, identity any) {
	t.Helper()
	encoded, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"if [ -n \"$MITHRIL_AGENT_MITHRIL_RPC_URL$MITHRIL_AGENT_PRIMARY_RPC_URL$MITHRIL_AGENT_SECONDARY_RPC_URL\" ]; then exit 9; fi\n" +
		"if [ \"$5\" != \"--identity\" ]; then exit 10; fi\n" +
		"printf '%s\\n' '" + string(encoded) + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestProposalPolicyCheckRejectsCrossPolicyDriftOffline(t *testing.T) {
	var output bytes.Buffer
	if err := runProposalPolicyCheck([]string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"offline", "reads no key", "cannot authorize, sign, or send",
		"--authority-policy", "--signer-policy", "--submitter-policy",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("proposal policy-check help is missing %q:\n%s", want, output.String())
		}
	}

	authority, signing, submission := testJupiterPolicySet(t)
	run := func(
		t *testing.T,
		authority policyauthority.Policy,
		signing signer.Policy,
		submission submitter.Policy,
	) error {
		t.Helper()
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		paths := []string{
			filepath.Join(dir, "authority.json"),
			filepath.Join(dir, "signer.json"),
			filepath.Join(dir, "submitter.json"),
		}
		writeJSON(t, paths[0], authority)
		writeJSON(t, paths[1], signing)
		writeJSON(t, paths[2], submission)
		output.Reset()
		return runProposalPolicyCheck([]string{
			"--authority-policy", paths[0],
			"--signer-policy", paths[1],
			"--submitter-policy", paths[2],
		}, &output)
	}

	if err := run(t, authority, signing, submission); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status            string `json:"status"`
		RecoveryMode      string `json:"recovery_mode"`
		SigningEnabled    bool   `json:"signing_enabled"`
		SubmissionEnabled bool   `json:"submission_enabled"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "policies_consistent_not_authorized" ||
		result.RecoveryMode != submission.RecoveryMode ||
		result.SigningEnabled || result.SubmissionEnabled {
		t.Fatalf("policy check result = %+v", result)
	}

	tests := map[string]func(*policyauthority.Policy, *signer.Policy, *submitter.Policy){
		"authority transaction": func(authority *policyauthority.Policy, _ *signer.Policy, _ *submitter.Policy) {
			authority.TransactionPolicy.RiskAuthorityKeyID = "different authority"
		},
		"authority evidence": func(authority *policyauthority.Policy, _ *signer.Policy, _ *submitter.Policy) {
			authority.JupiterProviders.PrimaryTrustDomain = "different-primary"
		},
		"signer limit": func(_ *policyauthority.Policy, signing *signer.Policy, _ *submitter.Policy) {
			signing.DailyDebitCapLamports++
		},
		"signer route": func(_ *policyauthority.Policy, signing *signer.Policy, _ *submitter.Policy) {
			signing.Jupiter.MinOutputAmount++
		},
		"submitter limit": func(_ *policyauthority.Policy, _ *signer.Policy, submission *submitter.Policy) {
			submission.MaxLamports--
		},
		"submitter evidence": func(_ *policyauthority.Policy, _ *signer.Policy, submission *submitter.Policy) {
			submission.Evidence.SecondaryTrustDomain = "different-secondary"
		},
		"state path collision": func(_ *policyauthority.Policy, signing *signer.Policy, submission *submitter.Policy) {
			submission.ControlStatePath = signing.AuthorizationLedgerPath + ".lock"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			authority, signing, submission := testJupiterPolicySet(t)
			mutate(&authority, &signing, &submission)
			if err := run(t, authority, signing, submission); err == nil {
				t.Fatal("cross-policy drift was accepted")
			}
		})
	}

	authority, signing, submission = testJupiterPolicySet(t)
	route := *signing.Jupiter
	route.InputMint, route.OutputMint = route.OutputMint, route.InputMint
	fingerprint, err := route.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	signing.Jupiter = &route
	signing.ProfileFingerprint = fingerprint
	signing.MaxLamports = 0
	signing.DailyDebitCapLamports = 0
	signing.MaxInputTokenAmount = route.MaxInputAmount
	signing.DailyInputTokenCap = route.MaxInputAmount
	signing.DailyNativeFeeCapLamports = route.MaxFeeLamports
	authority.TransactionPolicy = signing
	authorityRoute := route
	authority.TransactionPolicy.Jupiter = &authorityRoute
	submissionRoute := route
	submission.Jupiter = &submissionRoute
	submission.ProfileFingerprint = fingerprint
	submission.MaxLamports = 0
	submission.MaxInputTokenAmount = route.MaxInputAmount - 1
	if err := run(t, authority, signing, submission); err == nil {
		t.Fatal("signer and submitter accepted different token-input caps")
	}
}

func TestProposalPolicyCheckRequiresDistinctAbsolutePaths(t *testing.T) {
	var output bytes.Buffer
	if err := runProposalPolicyCheck(nil, &output); err == nil {
		t.Fatal("policy check accepted missing paths")
	}
	path := filepath.Join(t.TempDir(), "same.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runProposalPolicyCheck([]string{
		"--authority-policy", path, "--signer-policy", path, "--submitter-policy", path,
	}, &output); err == nil {
		t.Fatal("policy check accepted the same file for three trust boundaries")
	}
}

func TestProposalBundleCheckValidatesCandidateAndPoliciesTogether(t *testing.T) {
	var help bytes.Buffer
	if err := runProposalBundleCheck([]string{"--help"}, &help); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"offline bundle", "reads no key or provider", "cannot", "--candidate", "--policy-dir",
	} {
		if !strings.Contains(help.String(), want) {
			t.Fatalf("proposal bundle-check help is missing %q:\n%s", want, help.String())
		}
	}

	authority, signing, submission := testJupiterPolicySet(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policyDir := filepath.Join(dir, "policies")
	if err := os.Mkdir(policyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(policyDir, proposalAuthorityPolicyName), authority)
	writeJSON(t, filepath.Join(policyDir, proposalSignerPolicyName), signing)
	writeJSON(t, filepath.Join(policyDir, proposalSubmitterPolicyName), submission)
	candidatePath := filepath.Join(dir, "candidate.json")
	candidate := mainnetCandidateForPolicy(t, *signing.Jupiter)
	encoded, err := proposalcheck.EncodeCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidatePath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runProposalBundleCheck([]string{
		"--candidate", candidatePath, "--policy-dir", policyDir,
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status            string `json:"status"`
		ProfileSHA256     string `json:"profile_sha256"`
		RecoveryMode      string `json:"recovery_mode"`
		Next              string `json:"next"`
		SigningEnabled    bool   `json:"signing_enabled"`
		SubmissionEnabled bool   `json:"submission_enabled"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "bundle_consistent_not_authorized" ||
		result.ProfileSHA256 != signing.ProfileFingerprint ||
		result.RecoveryMode != submission.RecoveryMode ||
		result.Next != "proposal_prepare" || result.SigningEnabled || result.SubmissionEnabled {
		t.Fatalf("bundle result = %+v", result)
	}

	changed := candidate
	changed.Policy.MaxInputAmount++
	changedPath := filepath.Join(dir, "changed-candidate.json")
	writeJSON(t, changedPath, changed)
	if err := runProposalBundleCheck([]string{
		"--candidate", changedPath, "--policy-dir", policyDir,
	}, io.Discard); err == nil {
		t.Fatal("bundle-check accepted candidate drift")
	}
}

func mainnetCandidateForPolicy(
	t *testing.T,
	policy jupiterswap.Policy,
) proposalcheck.Candidate {
	t.Helper()
	inputAccount, err := orcaswap.AssociatedTokenAddress(policy.Owner, policy.InputMint)
	if err != nil {
		t.Fatal(err)
	}
	outputAccount, err := orcaswap.AssociatedTokenAddress(policy.Owner, policy.OutputMint)
	if err != nil {
		t.Fatal(err)
	}
	request := jupiterquote.Request{
		Taker: policy.Owner, InputMint: policy.InputMint, OutputMint: policy.OutputMint,
		DestinationTokenAccount: outputAccount, InputAmount: policy.MaxInputAmount,
		SlippageBPS: policy.MaxSlippageBPS,
	}
	estimatedOutput := policy.MinOutputAmount * 10_000 /
		uint64(10_000-request.SlippageBPS)
	minimumOutput := (estimatedOutput*uint64(10_000-request.SlippageBPS) + 9_999) / 10_000
	quote := jupiterquote.Result{
		InputAmount: request.InputAmount, EstimatedOutput: estimatedOutput,
		MinimumOutput: minimumOutput,
	}
	transfer := make([]byte, 12)
	binary.LittleEndian.PutUint32(transfer[:4], 2)
	binary.LittleEndian.PutUint64(transfer[4:], request.InputAmount)
	routeData := []byte{187, 100, 250, 204, 49, 196, 175, 20}
	routeData = binary.LittleEndian.AppendUint64(routeData, request.InputAmount)
	routeData = binary.LittleEndian.AppendUint64(routeData, quote.EstimatedOutput)
	routeData = binary.LittleEndian.AppendUint16(routeData, request.SlippageBPS)
	routeData = binary.LittleEndian.AppendUint16(routeData, 0)
	routeData = binary.LittleEndian.AppendUint16(routeData, 0)
	routeData = binary.LittleEndian.AppendUint32(routeData, 1)
	routeData = append(routeData, 17, 1, 0x10, 0x27, 0, 1)
	limit, err := solana.SetComputeUnitLimitInstruction(100_000)
	if err != nil {
		t.Fatal(err)
	}
	priceData := make([]byte, 9)
	priceData[0] = 3
	binary.LittleEndian.PutUint64(priceData[1:], 1)
	message, err := jupiterswap.BuildGuardedPolicyV0Message(
		policy, policy.Owner, solana.Encode(bytes.Repeat([]byte{9}, 32)), []solana.Instruction{
			limit,
			{Program: solana.ComputeBudgetProgram, Data: priceData},
			{
				Program: orcaswap.AssociatedTokenProgram,
				Accounts: []solana.AccountMeta{
					{Address: policy.Owner, Signer: true, Writable: true},
					{Address: inputAccount, Writable: true}, {Address: policy.Owner},
					{Address: policy.InputMint}, {Address: orcaswap.SystemProgram},
					{Address: orcaswap.TokenProgram},
				},
				Data: []byte{1},
			},
			{
				Program: orcaswap.SystemProgram,
				Accounts: []solana.AccountMeta{
					{Address: policy.Owner, Signer: true, Writable: true},
					{Address: inputAccount, Writable: true},
				},
				Data: transfer,
			},
			{
				Program:  orcaswap.TokenProgram,
				Accounts: []solana.AccountMeta{{Address: inputAccount, Writable: true}},
				Data:     []byte{17},
			},
			{
				Program: jupiterswap.Program,
				Accounts: []solana.AccountMeta{
					{Address: policy.Owner, Signer: true},
					{Address: inputAccount, Writable: true},
					{Address: outputAccount, Writable: true},
					{Address: policy.InputMint}, {Address: policy.OutputMint},
					{Address: orcaswap.TokenProgram}, {Address: orcaswap.TokenProgram},
					{Address: outputAccount, Writable: true},
					{Address: "D8cy77BBepLMngZx6ZukaTff5hCt1HrWyKk3Hnd9oitf"},
					{Address: jupiterswap.Program},
					{Address: solana.Encode(bytes.Repeat([]byte{3}, 32)), Writable: true},
				},
				Data: routeData,
			},
			{
				Program: orcaswap.TokenProgram,
				Accounts: []solana.AccountMeta{
					{Address: inputAccount, Writable: true},
					{Address: policy.Owner, Writable: true},
					{Address: policy.Owner, Signer: true},
				},
				Data: []byte{9},
			},
		}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return proposalcheck.Candidate{
		Version: proposalcheck.CandidateVersion, Policy: policy,
		Request: request, Quote: quote,
		MessageBase64: base64.StdEncoding.EncodeToString(message), LastValidBlockHeight: 200,
	}
}

func TestGuardedRouteReceiptRejectsIdentityDrift(t *testing.T) {
	_, signing, submission := testJupiterPolicySet(t)
	if atomic, protection, err := guardedRouteReceipt(signing, submission, "action"); err != nil || !atomic || protection != "immutable_guard_exact_code_pinned" {
		t.Fatalf("valid guarded receipt = %t, %q, %v", atomic, protection, err)
	}

	drifted := *submission.Jupiter
	drifted.RouteGuard.CodeLength++
	submission.Jupiter = &drifted
	if _, _, err := guardedRouteReceipt(signing, submission, "action"); err == nil {
		t.Fatal("guarded receipt accepted route-guard identity drift")
	}
}

func testJupiterPolicySet(t *testing.T) (
	policyauthority.Policy,
	signer.Policy,
	submitter.Policy,
) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	owner := solana.Encode(bytes.Repeat([]byte{21}, 32))
	route := jupiterswap.Policy{
		Owner: owner, InputMint: orcaswap.WrappedSOLMint,
		OutputMint:     solana.Encode(bytes.Repeat([]byte{22}, 32)),
		MaxInputAmount: 1_000_000, MinOutputAmount: 10_000,
		MaxSlippageBPS: 50, MaxComputeUnits: 200_000,
		MaxComputeUnitPriceMicroLamport: 1_000, MaxFeeLamports: 20_000,
		MaxTokenAccountRentLamports: 3_000_000, RouteGuard: proposalTestRouteGuard(),
	}
	fingerprint, err := route.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	riskSeed := sha256.Sum256([]byte("policy-check risk authority"))
	riskKey := ed25519.NewKeyFromSeed(riskSeed[:])
	riskPublic, err := riskgrant.PublicKeyHex(riskKey)
	if err != nil {
		t.Fatal(err)
	}
	attestationSeed := sha256.Sum256([]byte("policy-check attestor"))
	attestationPublic := solana.Encode(
		ed25519.NewKeyFromSeed(attestationSeed[:]).Public().(ed25519.PublicKey),
	)
	submitterSeed := sha256.Sum256([]byte("policy-check submitter"))
	submitterPublic, err := sealedtx.PublicKey(hex.EncodeToString(submitterSeed[:]))
	if err != nil {
		t.Fatal(err)
	}
	anchor := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC).Unix()
	signing := signer.Policy{
		Cluster: "mainnet-beta", Profile: jupiterswap.ProfileName,
		ProfileVersion: jupiterswap.ProfileVersion, ProfileFingerprint: fingerprint,
		Source: owner, MaxLamports: route.MaxInputAmount,
		MaxFeeLamports: route.MaxFeeLamports, DailyDebitCapLamports: 4_020_000,
		AuthorizationLedgerPath: filepath.Join(root, "signer", "authorization.jsonl"),
		ScheduleWindowSeconds:   3_600, ScheduleAnchorUnix: anchor,
		MaxBlockHeightWindow: 150, RiskAuthorityKeyID: "mainnet-risk-v1",
		RiskAuthorityPublicKey: riskPublic, SubmitterPublicKey: submitterPublic,
		AttestationPublicKey: attestationPublic, Jupiter: &route,
	}
	providers := proposalcheck.ProviderBindings{
		PrimaryTrustDomain: "primary-provider", PrimaryOriginSHA256: strings.Repeat("1", 64),
		SecondaryTrustDomain: "secondary-provider", SecondaryOriginSHA256: strings.Repeat("2", 64),
		ArchiveProbeSignature: solana.Encode(bytes.Repeat([]byte{23}, 64)),
	}
	authoritySigning := signing
	authorityRoute := route
	authoritySigning.Jupiter = &authorityRoute
	approverSeed := sha256.Sum256([]byte("policy-check operator approver"))
	approverPublic := solana.Encode(
		ed25519.NewKeyFromSeed(approverSeed[:]).Public().(ed25519.PublicKey),
	)
	authority := policyauthority.Policy{
		TransactionPolicy: authoritySigning, JupiterProviders: &providers,
		OperatorApprover: approverPublic, GrantLifetimeSecs: 30,
	}
	submissionRoute := route
	submission := submitter.Policy{
		Cluster: signing.Cluster, Profile: signing.Profile,
		ProfileFingerprint: signing.ProfileFingerprint,
		ControlStatePath:   filepath.Join(root, "submitter", "control.json"),
		Source:             signing.Source, MaxLamports: signing.MaxLamports,
		MaxFeeLamports:        signing.MaxFeeLamports,
		ScheduleWindowSeconds: signing.ScheduleWindowSeconds,
		ScheduleAnchorUnix:    signing.ScheduleAnchorUnix,
		MaxBlockHeightWindow:  signing.MaxBlockHeightWindow,
		RecoveryMode:          submitter.MainnetRecoveryStopOnly,
		SubmitterPublicKey:    signing.SubmitterPublicKey,
		AttestationPublicKey:  signing.AttestationPublicKey,
		Evidence:              providers, Jupiter: &submissionRoute,
	}
	return authority, signing, submission
}

func proposalTestRouteGuard() jupiterswap.RouteGuardDeployment {
	code := []byte("command route guard")
	hash := sha256.Sum256(code)
	return jupiterswap.RouteGuardDeployment{
		Program:        solana.Encode(bytes.Repeat([]byte{71}, 32)),
		ProgramData:    solana.Encode(bytes.Repeat([]byte{72}, 32)),
		DeploymentSlot: 123, CodeLength: uint64(len(code)), CodeSHA256: hex.EncodeToString(hash[:]),
	}
}
