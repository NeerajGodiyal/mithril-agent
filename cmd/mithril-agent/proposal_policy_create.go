package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/submitter"
)

const (
	proposalAuthorityPolicyName = "authority-policy.json"
	proposalSignerPolicyName    = "signer-policy.json"
	proposalSubmitterPolicyName = "submitter-policy.json"
)

const proposalPolicyCreateUsage = `Usage: mithril-agent proposal policy-create [options]

Writes one matching authority, signer, and submitter policy set from a checked
Mainnet route and result. It is offline and needs no provider account. It writes no secret key
and cannot authorize, sign, or send.

  --route-policy PATH             absolute protected policy from proposal check
  --check-result PATH             absolute protected result from proposal check
  --out DIR                       new absolute private policy directory
  --risk-key-id ID                public risk-authority key label
  --risk-public-key HEX           public Ed25519 risk-authority key
  --attestation-public-key ADDR   public zero-funds Solana attestation address
  --submitter-public-key HEX      public submitter encryption key
  --operator-approver ADDR        Solana wallet allowed to approve this exact action
  --schedule-window-seconds N     one-action window; divides one day (default 86400)
  --schedule-anchor-unix N        UTC-day boundary; defaults to today's UTC midnight
  --grant-lifetime-seconds N      short-lived authority grant (default 30)
  --max-block-height-window N     transaction lifetime ceiling (default 150)
  --recovery-mode MODE            stop_only (default) or exact_retry`

type proposalPolicyCreateResult struct {
	Status              string `json:"status"`
	Directory           string `json:"directory"`
	AuthorityPolicy     string `json:"authority_policy"`
	SignerPolicy        string `json:"signer_policy"`
	SubmitterPolicy     string `json:"submitter_policy"`
	OperatorApprover    string `json:"operator_approver"`
	ScheduleAnchorUnix  int64  `json:"schedule_anchor_unix"`
	ScheduleWindowSecs  uint64 `json:"schedule_window_seconds"`
	DailyDebitCap       uint64 `json:"daily_debit_cap_lamports,omitempty"`
	DailyInputTokenCap  uint64 `json:"daily_input_token_cap,omitempty"`
	DailyNativeFeeCap   uint64 `json:"daily_native_fee_cap_lamports,omitempty"`
	VendorAccountNeeded bool   `json:"vendor_account_required"`
	KeysGenerated       bool   `json:"keys_generated"`
	SigningEnabled      bool   `json:"signing_enabled"`
	SubmissionEnabled   bool   `json:"submission_enabled"`
	RecoveryMode        string `json:"recovery_mode"`
}

func runProposalPolicyCreate(
	args []string,
	output io.Writer,
	now func() time.Time,
) error {
	flags := flag.NewFlagSet("proposal policy-create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	routePath := flags.String("route-policy", "", "protected Jupiter route policy")
	resultPath := flags.String("check-result", "", "protected proposal check result")
	out := flags.String("out", "", "new private policy directory")
	riskKeyID := flags.String("risk-key-id", "", "risk-authority key label")
	riskPublicKey := flags.String("risk-public-key", "", "risk-authority public key")
	attestationPublicKey := flags.String("attestation-public-key", "", "attestation public key")
	submitterPublicKey := flags.String("submitter-public-key", "", "submitter public key")
	operatorApprover := flags.String("operator-approver", "", "operator approval wallet")
	scheduleWindow := flags.Uint64("schedule-window-seconds", 86_400, "schedule window")
	scheduleAnchor := flags.Int64("schedule-anchor-unix", 0, "UTC schedule anchor")
	grantLifetime := flags.Uint64("grant-lifetime-seconds", 30, "risk grant lifetime")
	maxBlockHeightWindow := flags.Uint64("max-block-height-window", 150, "block-height window")
	recoveryMode := flags.String(
		"recovery-mode", submitter.MainnetRecoveryStopOnly, "Mainnet recovery mode",
	)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, proposalPolicyCreateUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || !distinctAbsolutePaths(*routePath, *resultPath, *out) ||
		!validTrustDomain(*riskKeyID) || *riskPublicKey == "" || *attestationPublicKey == "" ||
		*submitterPublicKey == "" || *operatorApprover == "" {
		return errors.New("proposal policy-create requires distinct absolute inputs, a new output directory, a short lowercase risk-key label, three service public keys, and an operator approval wallet")
	}
	if _, err := os.Lstat(*out); err == nil {
		return errors.New("policy output directory already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("inspect policy output directory")
	}
	if err := secureexec.ValidateProtectedDirectory(filepath.Dir(*out)); err != nil {
		return errors.New("policy output parent directory is not protected")
	}

	var route jupiterswap.Policy
	if err := readStrictJSON(*routePath, &route); err != nil || route.Validate() != nil {
		return errors.New("read protected Jupiter route policy")
	}
	var checked proposalcheck.Result
	if err := readStrictJSON(*resultPath, &checked); err != nil {
		return errors.New("read protected Jupiter check result")
	}
	providers, err := checkedProviderBindings(route, checked)
	if err != nil {
		return err
	}
	anchor := *scheduleAnchor
	if anchor == 0 {
		if now == nil {
			return errors.New("trusted policy time is unavailable")
		}
		unix := now().UTC().Unix()
		if unix <= 0 {
			return errors.New("trusted policy time is unavailable")
		}
		anchor = unix - unix%86_400
	}
	fingerprint, err := route.Fingerprint()
	if err != nil {
		return errors.New("fingerprint Jupiter route policy")
	}

	ledgerPath := filepath.Join(*out, "state", signerStateDirName, "authorizations.jsonl")
	controlPath := filepath.Join(*out, "state", controlStateDirName, "control.json")
	signing := signer.Policy{
		Cluster: "mainnet-beta", Profile: jupiterswap.ProfileName,
		ProfileVersion: jupiterswap.ProfileVersion, ProfileFingerprint: fingerprint,
		Source: route.Owner, MaxFeeLamports: route.MaxFeeLamports,
		AuthorizationLedgerPath: ledgerPath,
		ScheduleWindowSeconds:   *scheduleWindow, ScheduleAnchorUnix: anchor,
		MaxBlockHeightWindow: *maxBlockHeightWindow,
		RiskAuthorityKeyID:   *riskKeyID, RiskAuthorityPublicKey: *riskPublicKey,
		SubmitterPublicKey:   *submitterPublicKey,
		AttestationPublicKey: *attestationPublicKey, Jupiter: &route,
	}
	if route.NativeInput() {
		dailyCap, ok := addUint64(route.MaxInputAmount, route.MaxFeeLamports)
		if !ok {
			return errors.New("Jupiter policy debit cap overflows")
		}
		dailyCap, ok = addUint64(dailyCap, route.MaxTokenAccountRentLamports)
		if !ok {
			return errors.New("Jupiter policy debit cap overflows")
		}
		signing.MaxLamports = route.MaxInputAmount
		signing.DailyDebitCapLamports = dailyCap
	} else {
		signing.MaxInputTokenAmount = route.MaxInputAmount
		signing.DailyInputTokenCap = route.MaxInputAmount
		signing.DailyNativeFeeCapLamports = route.MaxFeeLamports
	}
	authoritySigning := signing
	authorityRoute := route
	authoritySigning.Jupiter = &authorityRoute
	authority := policyauthority.Policy{
		TransactionPolicy: authoritySigning,
		JupiterProviders:  &providers,
		OperatorApprover:  *operatorApprover,
		GrantLifetimeSecs: *grantLifetime,
	}
	submissionRoute := route
	submission := submitter.Policy{
		Cluster: signing.Cluster, Profile: signing.Profile,
		ProfileFingerprint: signing.ProfileFingerprint,
		ControlStatePath:   controlPath, Source: signing.Source,
		MaxLamports: signing.MaxLamports, MaxFeeLamports: signing.MaxFeeLamports,
		MaxInputTokenAmount:   signing.MaxInputTokenAmount,
		ScheduleWindowSeconds: signing.ScheduleWindowSeconds,
		ScheduleAnchorUnix:    signing.ScheduleAnchorUnix,
		MaxBlockHeightWindow:  signing.MaxBlockHeightWindow,
		RecoveryMode:          *recoveryMode,
		SubmitterPublicKey:    signing.SubmitterPublicKey,
		AttestationPublicKey:  signing.AttestationPublicKey,
		Evidence:              providers, Jupiter: &submissionRoute,
	}
	if err := validateJupiterPolicySet(authority, signing, submission); err != nil {
		return err
	}
	if err := installSwapSetup(*out, map[string]any{
		proposalAuthorityPolicyName: authority,
		proposalSignerPolicyName:    signing,
		proposalSubmitterPolicyName: submission,
	}); err != nil {
		return fmt.Errorf("write protected policy bundle: %w", err)
	}
	return json.NewEncoder(output).Encode(proposalPolicyCreateResult{
		Status: "policies_written_not_authorized", Directory: *out,
		AuthorityPolicy:    filepath.Join(*out, proposalAuthorityPolicyName),
		SignerPolicy:       filepath.Join(*out, proposalSignerPolicyName),
		SubmitterPolicy:    filepath.Join(*out, proposalSubmitterPolicyName),
		OperatorApprover:   authority.OperatorApprover,
		ScheduleAnchorUnix: anchor, ScheduleWindowSecs: *scheduleWindow,
		DailyDebitCap:      signing.DailyDebitCapLamports,
		DailyInputTokenCap: signing.DailyInputTokenCap,
		DailyNativeFeeCap:  signing.DailyNativeFeeCapLamports,
		RecoveryMode:       submission.RecoveryMode,
	})
}

func checkedProviderBindings(
	route jupiterswap.Policy,
	checked proposalcheck.Result,
) (proposalcheck.ProviderBindings, error) {
	providers := proposalcheck.ProviderBindings{
		PrimaryTrustDomain:    checked.PrimaryTrustDomain,
		PrimaryOriginSHA256:   checked.PrimaryOriginSHA256,
		SecondaryTrustDomain:  checked.SecondaryTrustDomain,
		SecondaryOriginSHA256: checked.SecondaryOriginSHA256,
		ArchiveProbeSignature: checked.ArchiveProbeSignature,
	}
	fingerprint, err := route.Fingerprint()
	if err != nil || providers.ValidateArchiveProbe() != nil ||
		checked.Status != proposalcheck.StatusCheckedNotAuthorized ||
		checked.Reason != proposalcheck.ReasonSigningPolicyAbsent ||
		checked.Cluster != "mainnet-beta" || checked.PolicySHA256 != fingerprint ||
		checked.InputMint != route.InputMint || checked.OutputMint != route.OutputMint ||
		checked.InputAmount == 0 || checked.InputAmount > route.MaxInputAmount ||
		checked.MinimumOutput < route.MinOutputAmount ||
		checked.FeeLamports == 0 || checked.FeeLamports > route.MaxFeeLamports ||
		checked.SigningEnabled || checked.SubmissionEnabled {
		return proposalcheck.ProviderBindings{}, errors.New("proposal check result does not match the protected unauthorized route")
	}
	return providers, nil
}

func addUint64(left, right uint64) (uint64, bool) {
	if left > ^uint64(0)-right {
		return 0, false
	}
	return left + right, true
}
