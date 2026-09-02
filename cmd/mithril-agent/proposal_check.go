package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/policyclient"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/signerclient"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/submitterclient"
	"github.com/Overclock-Validator/mithril-agent/turnkeycustody"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const (
	maxTurnkeyQualificationPolicyBytes = 128 << 10
	selfHostedIdentityTimeout          = 15 * time.Second
)

const proposalUsage = `Usage:
  mithril-agent proposal help

Mainnet rehearsal, in order:
  1. Qualify two independent read providers:
  mithril-agent proposal evidence-check [options]
  2. Build one keyless candidate and review it:
  mithril-agent proposal check [options]
  mithril-agent proposal recheck [options]
  3. Create public identities on their separate hosts, then write and verify policies:
  mithril-agent proposal key-create [options]
  mithril-agent proposal policy-create [options]
  mithril-agent proposal policy-check [options]
  mithril-agent proposal bundle-check [options]
  4. Prove each installed identity on its separate host:
  mithril-agent proposal self-hosted-check [options]
  mithril-agent proposal authority-check [options]
  mithril-agent proposal submitter-check [options]
  mithril-agent proposal turnkey-check [options]
  mithril-agent proposal turnkey-policy [options]
  5. Prepare the exact unsigned request for the separate policy authority:
  mithril-agent proposal prepare [options]
  6. Decode and review that exact request against the signer policy:
  mithril-agent proposal review [options]
  7. Approve only that exact request with the separate operator wallet:
  mithril-agent proposal approval-create [options]
  8. After separate grant, signing, and offline submitter preparation, prove
     the exact one-action canary is ready but still disabled:
  mithril-agent proposal canary-check [options]

Every command here is read-only or offline. None can submit a transaction.
Create each private identity only on the separate host that will retain it;
proposal policy-create accepts public identities and never creates a key.`

const proposalEvidenceCheckUsage = `Usage: mithril-agent proposal evidence-check [options]

Verifies that two independent Mainnet RPCs agree on genesis and can recover one
pinned historical version-0 transaction. It needs no wallet or provider account,
reads no signing key, and cannot sign or send.
It is a point-in-time functional check, not an availability or SLA qualification.

  --primary-trust-domain NAME    operator name for the primary evidence provider
  --secondary-trust-domain NAME  operator name for the secondary evidence provider
  --archive-probe-signature SIGNATURE
                                  protected old finalized v0 transaction signature

Protected environment:
  MITHRIL_AGENT_PRIMARY_RPC_URL
  MITHRIL_AGENT_SECONDARY_RPC_URL`

const proposalCheckUsage = `Usage: mithril-agent proposal check [options]

Builds and checks one Mainnet Jupiter proposal without a signing key.
It cannot sign or send.

For SOL input, the canonical output token account is derived automatically and
must already exist. For token input, the funded canonical input account is
verified and native SOL returns directly to the protected wallet.

  --taker ADDRESS        public wallet address to model; no private key is read
  --input-mint ADDRESS   input token mint (default wrapped SOL)
  --output-mint ADDRESS  output token mint
  --amount N             input amount in base units
  --minimum-output N     operator's minimum output in base units
  --slippage-bps N       maximum quote slippage in basis points (default 50)
  --max-compute-units N  maximum transaction compute units
  --max-cu-price N       maximum micro-lamports per compute unit
  --max-fee-lamports N   maximum total transaction fee in lamports
  --max-account-rent N   maximum rent for either token account in lamports
  --route-guard-program ADDRESS
                          reviewed immutable route-guard program
  --route-guard-program-data ADDRESS
                          route-guard ProgramData account
  --route-guard-deployment-slot N
                          exact immutable deployment slot
  --route-guard-code-length N
                          exact deployed code length in bytes
  --route-guard-code-sha256 HEX
                          exact deployed code SHA-256
  --primary-trust-domain NAME
                          operator name for the primary evidence provider
  --secondary-trust-domain NAME
                          operator name for the secondary evidence provider
  --archive-probe-signature SIGNATURE
                          protected old finalized v0 transaction signature
  --candidate-output PATH optional absolute path for the private candidate JSON;
                          must be used with --policy-output
  --policy-output PATH    optional absolute path for the protected policy JSON;
                          must be used with --candidate-output
  --result-output PATH    optional absolute private path for the checked result;
                          use with policy-create to avoid copying provider pins

Protected environment:
  MITHRIL_AGENT_JUPITER_API_KEY  optional for tests; Jupiter recommends an API
                                 key for continuous production use
  MITHRIL_AGENT_MITHRIL_RPC_URL
  MITHRIL_AGENT_PRIMARY_RPC_URL
  MITHRIL_AGENT_SECONDARY_RPC_URL`

const proposalRecheckUsage = `Usage: mithril-agent proposal recheck [options]

Rechecks one private candidate against protected policy and providers. It cannot sign or send.

  --candidate PATH               absolute private candidate JSON path
  --policy PATH                  absolute private expected policy JSON path
  --primary-trust-domain NAME    primary evidence provider owner
  --primary-origin-sha256 HEX    pinned primary credential-free origin hash
  --secondary-trust-domain NAME  secondary evidence provider owner
  --secondary-origin-sha256 HEX  pinned secondary credential-free origin hash
  --archive-probe-signature SIGNATURE
                                  protected old finalized v0 transaction signature

Protected environment:
  MITHRIL_AGENT_MITHRIL_RPC_URL
  MITHRIL_AGENT_PRIMARY_RPC_URL
  MITHRIL_AGENT_SECONDARY_RPC_URL`

const proposalPrepareUsage = `Usage: mithril-agent proposal prepare [options]

Rechecks one private candidate under the authority's protected policy and
prints the exact unsigned, ungranted request. It cannot authorize, sign, or send.

  --candidate PATH         absolute private candidate JSON path
  --authority-policy PATH  absolute protected authority policy JSON path
  --schedule-start N       optional UTC schedule-window start as Unix seconds;
                           defaults to the active protected-policy window

Protected environment:
  MITHRIL_AGENT_MITHRIL_RPC_URL
  MITHRIL_AGENT_PRIMARY_RPC_URL
  MITHRIL_AGENT_SECONDARY_RPC_URL`

const proposalReviewUsage = `Usage: mithril-agent proposal review [options]

Independently decodes one exact unsigned or granted Mainnet signer request,
checks it against the protected signer policy, and prints a bounded review
receipt. It reads no key and does not validate or create authorization.
It cannot sign or send.

  --request PATH        absolute private signer-request JSON path
  --signer-policy PATH  absolute protected signer-policy JSON path`

type proposalReviewResult struct {
	Status                     string `json:"status"`
	Direction                  string `json:"direction"`
	Source                     string `json:"source"`
	InputMint                  string `json:"input_mint"`
	OutputMint                 string `json:"output_mint"`
	InputAmountBaseUnits       uint64 `json:"input_amount_base_units"`
	MinimumOutputBaseUnits     uint64 `json:"minimum_output_base_units"`
	MaximumNativeDebitLamports uint64 `json:"maximum_native_debit_lamports"`
	TransactionFeeLamports     uint64 `json:"transaction_fee_lamports"`
	TemporaryRentLamports      uint64 `json:"temporary_rent_lamports"`
	ScheduleWindowStartUnix    int64  `json:"schedule_window_start_unix"`
	ScheduleWindowEndUnix      int64  `json:"schedule_window_end_unix"`
	LastValidBlockHeight       uint64 `json:"last_valid_block_height"`
	ActionID                   string `json:"action_id"`
	MessageSHA256              string `json:"message_sha256"`
	ReviewRequired             bool   `json:"review_required"`
	AuthorizationChecked       bool   `json:"authorization_checked"`
	SigningEnabled             bool   `json:"signing_enabled"`
	SubmissionEnabled          bool   `json:"submission_enabled"`
	ProductionReady            bool   `json:"production_ready"`
}

const proposalTurnkeyPolicyUsage = `Usage: mithril-agent proposal turnkey-policy [options]

Writes an unfunded, retained-candidate Turnkey qualification policy. It does
not contact Turnkey, install a policy, read a key, sign, or send.

  --candidate PATH  absolute private candidate JSON path
  --policy PATH     absolute protected Jupiter policy JSON path
  --api-user ID     dedicated non-root Turnkey API user's ID
  --out PATH        absolute private output path for the policy JSON`

const proposalTurnkeyCheckUsage = `Usage: mithril-agent proposal turnkey-check [options]

Authenticates one Turnkey API identity and verifies that its signing resource
belongs to the expected Solana account. This check is read-only.
It creates no signing activity and cannot sign or send.

  --api-key-file PATH       absolute mode-0600 .private file generated by
                            Turnkey CLI; an activity JSON is only a receipt
  --api-public-key KEY      matching registered Turnkey API public key
  --organization ID         Turnkey organization ID
  --sign-with RESOURCE      Solana signing address or private-key ID
  --expected-address ADDRESS
                            public Solana account the signer must control`

const proposalSelfHostedCheckUsage = `Usage: mithril-agent proposal self-hosted-check [options]

Verifies a self-hosted signer's pinned identities through hardened OpenSSH.
It needs no vendor account, requests no signature, and cannot send a transaction.

  --host HOST                   signer host name or IP address
  --user USER                   dedicated signer SSH user
  --port N                      signer SSH port (default 22)
  --identity-file PATH          absolute mode-0600 SSH transport private key
  --known-hosts PATH            absolute protected pinned known-hosts file
  --policy PATH                 absolute protected signer policy; derives all
                                four expected identity and policy pins

Explicit pins (use only when --policy is omitted):
  --wallet-public-key ADDRESS   expected Solana wallet address
  --attestation-public-key KEY  expected zero-funds attestation address
  --submitter-public-key HEX    expected submitter encryption public key
  --profile-sha256 HEX          expected signer policy fingerprint
  --ssh-command PATH            absolute OpenSSH client path (default /usr/bin/ssh)`

const proposalSubmitterCheckUsage = `Usage: mithril-agent proposal submitter-check [options]

Runs the isolated submitter in identity-only mode and verifies that its
protected key, Mainnet policy, source wallet, and profile agree. It receives no
RPC environment or transaction and cannot sign or submit.

  --policy PATH    absolute protected submitter policy
  --key PATH       absolute protected submitter encryption key
  --command PATH   absolute submitter executable
                   (default /usr/local/bin/mithril-agent-submitter)`

const proposalAuthorityCheckUsage = `Usage: mithril-agent proposal authority-check [options]

Runs the isolated policy authority in identity-only mode and verifies that its
protected key and Mainnet policy agree. It receives no RPC environment or
request and cannot authorize, sign, or submit.

  --policy PATH    absolute protected authority policy
  --key PATH       absolute protected risk-authority keypair
  --command PATH   absolute policy-authority executable
                   (default /usr/local/bin/mithril-agent-policy)`

const proposalPolicyCheckUsage = `Usage: mithril-agent proposal policy-check [options]

Validates that the protected Mainnet authority, signer, and submitter policies
describe the same bounded Jupiter profile. It is offline, reads no key, and
cannot authorize, sign, or send.

  --authority-policy PATH  absolute protected authority policy JSON path
  --signer-policy PATH     absolute protected signer policy JSON path
  --submitter-policy PATH  absolute protected submitter policy JSON path`

const proposalBundleCheckUsage = `Usage: mithril-agent proposal bundle-check [options]

Validates one private candidate and the complete generated Mainnet policy
directory as one offline bundle. It reads no key or provider and cannot
authorize, sign, or send.

  --candidate PATH   absolute private candidate JSON path
  --policy-dir DIR   absolute protected directory from proposal policy-create`

const proposalCanaryCheckUsage = `Usage: mithril-agent proposal canary-check [options]

Verifies the complete protected policy set, the keyless operator socket policy
identity, stopped control revision, the exact strategy's consecutive shadow
evidence, and one offline-prepared Mainnet action against Mithril and both
independent evidence providers. It reads no key and cannot enable, sign, or
submit. It verifies evidence completeness but does not approve profitability.
It proves the pinned route cannot be upgraded within the checked transaction,
but does not qualify production custody, provider SLAs, off-host audit, or the
external deadman.

  --policy-dir DIR        absolute protected directory from proposal policy-create
  --operator-socket PATH  absolute root-only submitter operator socket
  --request PATH           exact private signer request approved by the operator
  --operator-approval PATH detached approval from proposal approval-create
  --shadow-policy PATH    exact Mainnet shadow policy used for the strategy
  --shadow-dir DIR        directory containing its daily shadow journals
  --shadow-days N         consecutive complete UTC days required (1..3650)
  --command PATH          absolute submitter executable
                          (default /usr/local/bin/mithril-agent-submitter)

Protected environment:
  MITHRIL_AGENT_MITHRIL_RPC_URL
  MITHRIL_AGENT_PRIMARY_RPC_URL
  MITHRIL_AGENT_SECONDARY_RPC_URL`

func runProposalCheck(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("proposal check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	taker := flags.String("taker", "", "public wallet address")
	inputMint := flags.String("input-mint", orcaswap.WrappedSOLMint, "input token mint")
	outputMint := flags.String("output-mint", "", "output token mint")
	amount := flags.Uint64("amount", 0, "input amount in base units")
	minimumOutput := flags.Uint64("minimum-output", 0, "minimum output in base units")
	slippage := flags.Uint("slippage-bps", 50, "maximum quote slippage")
	maxComputeUnits := flags.Uint("max-compute-units", 0, "maximum compute units")
	maxComputeUnitPrice := flags.Uint64("max-cu-price", 0, "maximum micro-lamports per compute unit")
	maxFeeLamports := flags.Uint64("max-fee-lamports", 0, "maximum total transaction fee")
	maxAccountRent := flags.Uint64("max-account-rent", 0, "maximum token-account rent")
	routeGuardProgram := flags.String("route-guard-program", "", "immutable route-guard program")
	routeGuardProgramData := flags.String("route-guard-program-data", "", "route-guard ProgramData")
	routeGuardDeploymentSlot := flags.Uint64("route-guard-deployment-slot", 0, "route-guard deployment slot")
	routeGuardCodeLength := flags.Uint64("route-guard-code-length", 0, "route-guard code length")
	routeGuardCodeSHA256 := flags.String("route-guard-code-sha256", "", "route-guard code SHA-256")
	primaryTrustDomain := flags.String("primary-trust-domain", "", "primary evidence provider trust domain")
	secondaryTrustDomain := flags.String("secondary-trust-domain", "", "secondary evidence provider trust domain")
	archiveProbeSignature := flags.String("archive-probe-signature", "", "protected historical v0 transaction")
	candidateOutput := flags.String("candidate-output", "", "private candidate JSON output path")
	policyOutput := flags.String("policy-output", "", "private policy JSON output path")
	resultOutput := flags.String("result-output", "", "private checked-result JSON output path")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, proposalCheckUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *taker == "" || *outputMint == "" ||
		*amount == 0 || *minimumOutput == 0 || *slippage == 0 || *slippage > 500 ||
		*maxComputeUnits == 0 || *maxComputeUnits > uint(solana.MaxComputeUnitLimit) ||
		*maxComputeUnitPrice == 0 || *maxFeeLamports == 0 || *maxAccountRent == 0 ||
		*routeGuardProgram == "" || *routeGuardProgramData == "" ||
		*routeGuardDeploymentSlot == 0 || *routeGuardCodeLength == 0 ||
		!validSHA256(*routeGuardCodeSHA256) ||
		!validTrustDomain(*primaryTrustDomain) || !validTrustDomain(*secondaryTrustDomain) ||
		*primaryTrustDomain == *secondaryTrustDomain ||
		!validTransactionSignature(*archiveProbeSignature) ||
		(*candidateOutput == "") != (*policyOutput == "") ||
		(*resultOutput != "" && (!filepath.IsAbs(*resultOutput) ||
			filepath.Clean(*resultOutput) != *resultOutput ||
			*resultOutput == *candidateOutput || *resultOutput == *policyOutput)) {
		return errors.New("proposal check requires valid wallet, mint, amount, output, slippage, compute limits, immutable route-guard identity, provider trust domains, archive probe, and paired candidate/policy outputs")
	}
	policy := jupiterswap.Policy{
		Owner: *taker, InputMint: *inputMint, OutputMint: *outputMint,
		MaxInputAmount: *amount, MinOutputAmount: *minimumOutput,
		MaxSlippageBPS: uint16(*slippage), MaxComputeUnits: uint32(*maxComputeUnits),
		MaxComputeUnitPriceMicroLamport: *maxComputeUnitPrice,
		MaxFeeLamports:                  *maxFeeLamports,
		MaxTokenAccountRentLamports:     *maxAccountRent,
		RouteGuard: jupiterswap.RouteGuardDeployment{
			Program: *routeGuardProgram, ProgramData: *routeGuardProgramData,
			DeploymentSlot: *routeGuardDeploymentSlot, CodeLength: *routeGuardCodeLength,
			CodeSHA256: *routeGuardCodeSHA256,
		},
	}
	if err := policy.Validate(); err != nil {
		return errors.New("proposal check requires exactly one native-SOL side and valid protected route limits")
	}
	outputAccount := ""
	if !policy.NativeOutput() {
		var err error
		outputAccount, err = orcaswap.AssociatedTokenAddress(*taker, *outputMint)
		if err != nil {
			return errors.New("derive canonical output token account")
		}
	}
	builder, err := jupiterquote.New(os.Getenv(jupiterAPIKeyEnvironment))
	if err != nil {
		return err
	}
	providers, err := openUnboundRPCProviders(
		os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL"),
		os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL"),
		os.Getenv("MITHRIL_AGENT_SECONDARY_RPC_URL"),
	)
	if err != nil {
		return err
	}
	lifecycle, err := txflow.New(providers.mithril, providers.primary, providers.secondary)
	if err != nil {
		return err
	}
	result, err := proposalcheck.Check(
		ctx, builder, lifecycle, providers.primary, providers.secondary,
		*primaryTrustDomain, *secondaryTrustDomain, *archiveProbeSignature,
		policy,
		jupiterquote.Request{
			Taker: *taker, InputMint: *inputMint, OutputMint: *outputMint,
			DestinationTokenAccount: outputAccount,
			InputAmount:             *amount, SlippageBPS: uint16(*slippage),
		},
	)
	if err != nil {
		return err
	}
	if *policyOutput != "" {
		encoded, err := json.Marshal(policy)
		if err != nil {
			return errors.New("encode Jupiter policy")
		}
		if err := securefile.ReplacePrivate(
			*policyOutput, append(encoded, '\n'), maxInputBytes,
		); err != nil {
			return errors.New("write private Jupiter policy")
		}
	}
	if *candidateOutput != "" {
		candidate, err := result.Candidate()
		if err != nil {
			return err
		}
		encoded, err := proposalcheck.EncodeCandidate(candidate)
		if err != nil {
			return err
		}
		if err := securefile.ReplacePrivate(
			*candidateOutput, append(encoded, '\n'), 1<<20,
		); err != nil {
			return errors.New("write private Jupiter candidate")
		}
	}
	if *resultOutput != "" {
		encoded, err := json.Marshal(result)
		if err != nil {
			return errors.New("encode checked Jupiter result")
		}
		if err := securefile.ReplacePrivate(
			*resultOutput, append(encoded, '\n'), maxInputBytes,
		); err != nil {
			return errors.New("write private checked Jupiter result")
		}
	}
	return json.NewEncoder(output).Encode(result)
}

func runProposalRecheck(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("proposal recheck", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	candidatePath := flags.String("candidate", "", "private candidate JSON path")
	policyPath := flags.String("policy", "", "private expected policy JSON path")
	primaryTrustDomain := flags.String("primary-trust-domain", "", "primary evidence provider trust domain")
	primaryOrigin := flags.String("primary-origin-sha256", "", "pinned primary evidence origin")
	secondaryTrustDomain := flags.String("secondary-trust-domain", "", "secondary evidence provider trust domain")
	secondaryOrigin := flags.String("secondary-origin-sha256", "", "pinned secondary evidence origin")
	archiveProbeSignature := flags.String("archive-probe-signature", "", "protected historical v0 transaction")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, proposalRecheckUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *candidatePath == "" || *policyPath == "" ||
		!validTrustDomain(*primaryTrustDomain) || !validTrustDomain(*secondaryTrustDomain) ||
		*primaryTrustDomain == *secondaryTrustDomain ||
		!validSHA256(*primaryOrigin) || !validSHA256(*secondaryOrigin) ||
		*primaryOrigin == *secondaryOrigin || !validTransactionSignature(*archiveProbeSignature) {
		return errors.New("proposal recheck requires a private candidate, protected policy, and distinct pinned provider bindings")
	}
	var expectedPolicy jupiterswap.Policy
	if err := readStrictJSON(*policyPath, &expectedPolicy); err != nil || expectedPolicy.Validate() != nil {
		return errors.New("read private Jupiter policy")
	}
	raw, err := securefile.ReadPrivate(*candidatePath, 1<<20)
	if err != nil {
		return errors.New("read private Jupiter candidate")
	}
	candidate, err := proposalcheck.DecodeCandidate(raw)
	if err != nil {
		return err
	}
	providers, err := openUnboundRPCProviders(
		os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL"),
		os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL"),
		os.Getenv("MITHRIL_AGENT_SECONDARY_RPC_URL"),
	)
	if err != nil {
		return err
	}
	lifecycle, err := txflow.New(providers.mithril, providers.primary, providers.secondary)
	if err != nil {
		return err
	}
	result, err := proposalcheck.Recheck(
		ctx, lifecycle, providers.primary, providers.secondary,
		expectedPolicy,
		proposalcheck.ProviderBindings{
			PrimaryTrustDomain: *primaryTrustDomain, PrimaryOriginSHA256: *primaryOrigin,
			SecondaryTrustDomain: *secondaryTrustDomain, SecondaryOriginSHA256: *secondaryOrigin,
			ArchiveProbeSignature: *archiveProbeSignature,
		},
		candidate,
	)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

func validTransactionSignature(value string) bool {
	_, err := solana.Decode64(value)
	return err == nil
}

func runProposalPrepare(
	ctx context.Context,
	args []string,
	output io.Writer,
	now func() time.Time,
) error {
	flags := flag.NewFlagSet("proposal prepare", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	candidatePath := flags.String("candidate", "", "private candidate JSON path")
	policyPath := flags.String("authority-policy", "", "protected authority policy JSON path")
	scheduleStart := flags.Int64("schedule-start", 0, "UTC schedule-window start")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, proposalPrepareUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *candidatePath == "" || *policyPath == "" || now == nil {
		return errors.New("proposal prepare requires a private candidate and protected authority policy")
	}
	if *scheduleStart < 0 {
		return errors.New("proposal prepare schedule start cannot be negative")
	}
	var policy policyauthority.Policy
	if err := readStrictJSON(*policyPath, &policy); err != nil || policy.Validate() != nil ||
		policy.TransactionPolicy.Jupiter == nil || policy.JupiterProviders == nil {
		return errors.New("read protected Jupiter authority policy")
	}
	raw, err := securefile.ReadPrivate(*candidatePath, 1<<20)
	if err != nil {
		return errors.New("read private Jupiter candidate")
	}
	candidate, err := proposalcheck.DecodeCandidate(raw)
	if err != nil {
		return err
	}
	providers, err := openUnboundRPCProviders(
		os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL"),
		os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL"),
		os.Getenv("MITHRIL_AGENT_SECONDARY_RPC_URL"),
	)
	if err != nil {
		return err
	}
	lifecycle, err := txflow.New(providers.mithril, providers.primary, providers.secondary)
	if err != nil {
		return err
	}
	current := now().UTC()
	if *scheduleStart == 0 {
		*scheduleStart, err = activeScheduleWindowStart(policy.TransactionPolicy, current)
		if err != nil {
			return err
		}
	}
	request, err := policyauthority.PrepareJupiterRequest(
		ctx, policy, candidate, *scheduleStart, current,
		lifecycle, providers.primary, providers.secondary,
	)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(request)
}

func runProposalReview(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("proposal review", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	requestPath := flags.String("request", "", "private signer-request JSON path")
	policyPath := flags.String("signer-policy", "", "protected signer-policy JSON path")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, proposalReviewUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *requestPath == "" || *policyPath == "" {
		return errors.New("proposal review requires a private signer request and protected signer policy")
	}
	var policy signer.Policy
	if err := readStrictJSON(*policyPath, &policy); err != nil {
		return errors.New("read protected signer policy")
	}
	raw, err := securefile.ReadPrivate(*requestPath, signer.MaxRequestBytes)
	if err != nil {
		return errors.New("read private signer request")
	}
	defer clear(raw)
	var request signer.Request
	if err := strictjson.Decode(raw, &request); err != nil {
		return errors.New("decode private signer request")
	}
	validated, err := signer.ValidateJupiterRequest(policy, request)
	if err != nil {
		return err
	}
	defer clear(validated.Message)
	direction := "sell_token_for_sol"
	if policy.Jupiter.NativeInput() {
		direction = "buy_token_with_sol"
	}
	return json.NewEncoder(output).Encode(proposalReviewResult{
		Status:                     "request_matches_signer_policy_not_authorized",
		Direction:                  direction,
		Source:                     policy.Source,
		InputMint:                  validated.InputMint,
		OutputMint:                 validated.OutputMint,
		InputAmountBaseUnits:       validated.InputAmount,
		MinimumOutputBaseUnits:     validated.MinimumOutput,
		MaximumNativeDebitLamports: validated.DebitLamports,
		TransactionFeeLamports:     request.FeeLamports,
		TemporaryRentLamports:      validated.TemporaryRentLamports,
		ScheduleWindowStartUnix:    request.ScheduleWindowStartUnix,
		ScheduleWindowEndUnix:      request.ScheduleWindowEndUnix,
		LastValidBlockHeight:       request.LastValidBlockHeight,
		ActionID:                   request.ActionID,
		MessageSHA256:              validated.MessageSHA256,
		ReviewRequired:             true,
	})
}

func activeScheduleWindowStart(policy signer.Policy, at time.Time) (int64, error) {
	window := int64(policy.ScheduleWindowSeconds)
	nowUnix := at.UTC().Unix()
	if window <= 0 || policy.ScheduleAnchorUnix <= 0 || nowUnix < policy.ScheduleAnchorUnix {
		return 0, errors.New("protected authority policy has no active schedule window")
	}
	return policy.ScheduleAnchorUnix +
		(nowUnix-policy.ScheduleAnchorUnix)/window*window, nil
}

func runProposalTurnkeyPolicy(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("proposal turnkey-policy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	candidatePath := flags.String("candidate", "", "private candidate JSON path")
	policyPath := flags.String("policy", "", "private expected policy JSON path")
	apiUserID := flags.String("api-user", "", "dedicated non-root Turnkey API user ID")
	outPath := flags.String("out", "", "private Turnkey qualification policy output path")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, proposalTurnkeyPolicyUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *candidatePath == "" || *policyPath == "" ||
		*apiUserID == "" || *outPath == "" ||
		!filepath.IsAbs(*candidatePath) || !filepath.IsAbs(*policyPath) || !filepath.IsAbs(*outPath) ||
		filepath.Clean(*outPath) == filepath.Clean(*candidatePath) ||
		filepath.Clean(*outPath) == filepath.Clean(*policyPath) {
		return errors.New("proposal turnkey-policy requires a private candidate, protected policy, non-root API user ID, and private output")
	}
	var policy jupiterswap.Policy
	if err := readStrictJSON(*policyPath, &policy); err != nil || policy.Validate() != nil {
		return errors.New("read private Jupiter policy")
	}
	raw, err := securefile.ReadPrivate(*candidatePath, 1<<20)
	if err != nil {
		return errors.New("read private Jupiter candidate")
	}
	defer clear(raw)
	candidate, err := proposalcheck.DecodeCandidate(raw)
	if err != nil {
		return err
	}
	document, err := turnkeycustody.BuildJupiterQualificationPolicy(policy, candidate, *apiUserID)
	if err != nil {
		return err
	}
	if err := writeTurnkeyQualificationPolicy(*outPath, document); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Status      string `json:"status"`
		Operational bool   `json:"operational"`
	}{Status: "turnkey_qualification_policy_written", Operational: false})
}

func writeTurnkeyQualificationPolicy(
	path string,
	document turnkeycustody.QualificationPolicy,
) error {
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return errors.New("encode Turnkey qualification policy")
	}
	if err := securefile.ReplacePrivate(
		path, append(encoded, '\n'), maxTurnkeyQualificationPolicyBytes,
	); err != nil {
		return errors.New("write private Turnkey qualification policy")
	}
	return nil
}

var verifyTurnkeyIdentity = func(
	ctx context.Context,
	path, publicKey string,
	config turnkeycustody.Config,
	expectedAddress string,
) error {
	_, err := turnkeycustody.NewVerifiedFromAPIKeyFile(
		ctx, path, publicKey, config, expectedAddress,
	)
	return err
}

var verifySelfHostedSignerIdentity = func(
	ctx context.Context,
	config signerclient.Config,
) error {
	client, err := signerclient.New(config)
	if err != nil {
		return err
	}
	_, err = client.Identity(ctx)
	return err
}

var verifyProposalAuthorityIdentity = func(
	ctx context.Context,
	config policyclient.Config,
) error {
	client, err := policyclient.New(config)
	if err != nil {
		return err
	}
	_, err = client.Identity(ctx)
	return err
}

var verifyProposalSubmitterIdentity = func(
	ctx context.Context,
	config submitterclient.Config,
	expected submitterclient.Identity,
) error {
	client, err := submitterclient.New(config)
	if err != nil {
		return err
	}
	identity, err := client.Identity(ctx)
	if err != nil || identity != expected {
		return errors.New("submitter identity does not match protected policy")
	}
	return nil
}

var inspectProposalCanaryControl = func(
	socketPath string,
	expected submitterclient.Identity,
) (control.Status, string, error) {
	client, err := submitterclient.New(submitterclient.Config{
		SocketPath: socketPath, ControlMode: control.ModeMainnetCanary,
	})
	if err != nil {
		return control.Status{}, "", err
	}
	identity, status, revision, err := client.OperatorSnapshot()
	if err != nil || identity != expected {
		return control.Status{}, "", errors.New("operator socket policy does not match protected policy")
	}
	return status, revision, nil
}

var checkProposalMainnetReadiness = submitterclient.CheckMainnetReadiness

var qualifyProposalEvidence = func(
	ctx context.Context,
	primaryURL, secondaryURL string,
	bindings proposalcheck.ProviderBindings,
) (proposalcheck.ProviderBindings, error) {
	primary, secondary, err := openEvidenceProviders(primaryURL, secondaryURL)
	if err != nil {
		return proposalcheck.ProviderBindings{}, errors.New("evidence RPC configuration is invalid")
	}
	bindings.PrimaryOriginSHA256 = primary.Identity()
	bindings.SecondaryOriginSHA256 = secondary.Identity()
	if err := bindings.ValidateArchiveProbe(); err != nil {
		return proposalcheck.ProviderBindings{}, err
	}
	lifecycle, err := txflow.NewEvidenceLifecycle(primary, secondary)
	if err != nil {
		return proposalcheck.ProviderBindings{}, err
	}
	if err := lifecycle.VerifyEvidenceGenesis(ctx, solana.MainnetBetaGenesisHash); err != nil {
		return proposalcheck.ProviderBindings{}, err
	}
	if err := lifecycle.VerifyFinalizedV0History(ctx, bindings.ArchiveProbeSignature); err != nil {
		return proposalcheck.ProviderBindings{}, err
	}
	return bindings, nil
}

func runProposalEvidenceCheck(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("proposal evidence-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	primaryTrust := flags.String("primary-trust-domain", "", "primary evidence provider owner")
	secondaryTrust := flags.String("secondary-trust-domain", "", "secondary evidence provider owner")
	archiveProbe := flags.String("archive-probe-signature", "", "protected historical v0 transaction")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, proposalEvidenceCheckUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *primaryTrust == "" || *secondaryTrust == "" ||
		*primaryTrust == *secondaryTrust {
		return errors.New("proposal evidence-check requires two distinct provider owners and an archive probe")
	}
	if _, err := solana.Decode64(*archiveProbe); err != nil {
		return errors.New("proposal evidence-check requires a valid archive probe signature")
	}
	bindings, err := qualifyProposalEvidence(
		ctx,
		os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL"),
		os.Getenv("MITHRIL_AGENT_SECONDARY_RPC_URL"),
		proposalcheck.ProviderBindings{
			PrimaryTrustDomain: *primaryTrust, SecondaryTrustDomain: *secondaryTrust,
			ArchiveProbeSignature: *archiveProbe,
		},
	)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Status                string `json:"status"`
		PrimaryTrustDomain    string `json:"primary_trust_domain"`
		PrimaryOriginSHA256   string `json:"primary_origin_sha256"`
		SecondaryTrustDomain  string `json:"secondary_trust_domain"`
		SecondaryOriginSHA256 string `json:"secondary_origin_sha256"`
		VendorAccountRequired bool   `json:"vendor_account_required"`
		WalletRequired        bool   `json:"wallet_required"`
		SLAQualified          bool   `json:"sla_qualified"`
		ProductionReady       bool   `json:"production_ready"`
		CanSign               bool   `json:"can_sign"`
		CanSubmit             bool   `json:"can_submit"`
	}{
		Status:                "evidence_providers_verified",
		PrimaryTrustDomain:    bindings.PrimaryTrustDomain,
		PrimaryOriginSHA256:   bindings.PrimaryOriginSHA256,
		SecondaryTrustDomain:  bindings.SecondaryTrustDomain,
		SecondaryOriginSHA256: bindings.SecondaryOriginSHA256,
	})
}

func runProposalSelfHostedCheck(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("proposal self-hosted-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	command := flags.String("ssh-command", "/usr/bin/ssh", "absolute OpenSSH client path")
	host := flags.String("host", "", "signer host")
	user := flags.String("user", "", "dedicated signer SSH user")
	port := flags.Uint("port", 22, "signer SSH port")
	identityPath := flags.String("identity-file", "", "protected SSH transport private key")
	knownHostsPath := flags.String("known-hosts", "", "protected pinned known-hosts file")
	policyPath := flags.String("policy", "", "protected signer policy")
	walletPublicKey := flags.String("wallet-public-key", "", "expected Solana wallet")
	attestationPublicKey := flags.String("attestation-public-key", "", "expected attestation identity")
	submitterPublicKey := flags.String("submitter-public-key", "", "expected submitter identity")
	profileSHA256 := flags.String("profile-sha256", "", "expected signer policy fingerprint")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, proposalSelfHostedCheckUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *host == "" || *user == "" || *port == 0 || *port > 65535 ||
		!filepath.IsAbs(*command) || !filepath.IsAbs(*identityPath) ||
		!filepath.IsAbs(*knownHostsPath) {
		return errors.New("proposal self-hosted-check requires a host, user, valid port, and absolute SSH paths")
	}
	wallet, attestor, submitterKey, profile :=
		*walletPublicKey, *attestationPublicKey, *submitterPublicKey, *profileSHA256
	hasExplicitPins := wallet != "" || attestor != "" || submitterKey != "" || profile != ""
	if *policyPath != "" {
		if hasExplicitPins || !filepath.IsAbs(*policyPath) || filepath.Clean(*policyPath) != *policyPath {
			return errors.New("proposal self-hosted-check requires either one absolute protected signer policy or four explicit pins")
		}
		var signing signer.Policy
		if err := readStrictJSON(*policyPath, &signing); err != nil {
			return errors.New("read protected signer policy")
		}
		if err := signer.ValidateJupiterPolicy(signing); err != nil {
			return errors.New("signer policy is not a valid Mainnet Jupiter policy")
		}
		wallet, attestor = signing.Source, signing.AttestationPublicKey
		submitterKey, profile = signing.SubmitterPublicKey, signing.ProfileFingerprint
	} else if wallet == "" || attestor == "" || submitterKey == "" || !validSHA256(profile) {
		return errors.New("proposal self-hosted-check requires either one absolute protected signer policy or four explicit pins")
	}
	if err := validateSelfHostedIdentityPins(
		wallet, attestor, submitterKey,
	); err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, selfHostedIdentityTimeout)
	defer cancel()
	if err := verifySelfHostedSignerIdentity(checkCtx, signerclient.Config{
		SSH: &signerclient.SSHTransport{
			Command: *command, Host: *host, User: *user, Port: uint16(*port),
			IdentityPath: *identityPath, KnownHostsPath: *knownHostsPath,
		},
		ExpectedWalletPublicKey:      wallet,
		ExpectedAttestationPublicKey: attestor,
		ExpectedSubmitterPublicKey:   submitterKey,
		ExpectedProfileSHA256:        profile,
	}); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Status                string `json:"status"`
		VendorAccountRequired bool   `json:"vendor_account_required"`
		SigningActivity       bool   `json:"signing_activity"`
		CanSubmit             bool   `json:"can_submit"`
	}{Status: "self_hosted_signer_identity_verified"})
}

func runProposalSubmitterCheck(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("proposal submitter-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	command := flags.String(
		"command", "/usr/local/bin/mithril-agent-submitter", "absolute submitter executable",
	)
	policyPath := flags.String("policy", "", "protected submitter policy")
	keyPath := flags.String("key", "", "protected submitter encryption key")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, proposalSubmitterCheckUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || !distinctAbsolutePaths(*command, *policyPath, *keyPath) {
		return errors.New("proposal submitter-check requires an absolute command and distinct protected policy and key paths")
	}
	var policy submitter.Policy
	if err := readStrictJSON(*policyPath, &policy); err != nil {
		return errors.New("read protected submitter policy")
	}
	if err := submitter.ValidateJupiterPolicy(policy); err != nil {
		return errors.New("submitter policy is not a valid Mainnet Jupiter policy")
	}
	checkCtx, cancel := context.WithTimeout(ctx, selfHostedIdentityTimeout)
	defer cancel()
	if err := verifyProposalSubmitterIdentity(
		checkCtx,
		submitterclient.Config{
			Command: *command, PolicyPath: *policyPath, PrivateKeyPath: *keyPath,
		},
		submitterclient.Identity{
			PublicKey:          policy.SubmitterPublicKey,
			ProfileFingerprint: policy.ProfileFingerprint,
			Source:             policy.Source,
		},
	); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Status          string `json:"status"`
		SigningActivity bool   `json:"signing_activity"`
		CanSubmit       bool   `json:"can_submit"`
	}{Status: "submitter_identity_verified"})
}

func runProposalAuthorityCheck(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("proposal authority-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	command := flags.String(
		"command", "/usr/local/bin/mithril-agent-policy", "absolute policy-authority executable",
	)
	policyPath := flags.String("policy", "", "protected authority policy")
	keyPath := flags.String("key", "", "protected risk-authority keypair")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, proposalAuthorityCheckUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || !distinctAbsolutePaths(*command, *policyPath, *keyPath) {
		return errors.New("proposal authority-check requires an absolute command and distinct protected policy and key paths")
	}
	var policy policyauthority.Policy
	if err := readStrictJSON(*policyPath, &policy); err != nil {
		return errors.New("read protected authority policy")
	}
	if err := policy.Validate(); err != nil || policy.TransactionPolicy.Jupiter == nil ||
		policy.JupiterProviders == nil {
		return errors.New("authority policy is not a valid Mainnet Jupiter policy")
	}
	checkCtx, cancel := context.WithTimeout(ctx, selfHostedIdentityTimeout)
	defer cancel()
	if err := verifyProposalAuthorityIdentity(checkCtx, policyclient.Config{
		Command: *command, PolicyPath: *policyPath, KeypairPath: *keyPath,
		KeyID:     policy.TransactionPolicy.RiskAuthorityKeyID,
		PublicKey: policy.TransactionPolicy.RiskAuthorityPublicKey,
	}); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Status                string `json:"status"`
		AuthorizationActivity bool   `json:"authorization_activity"`
		CanSign               bool   `json:"can_sign"`
		CanSubmit             bool   `json:"can_submit"`
	}{Status: "risk_authority_identity_verified"})
}

func validateSelfHostedIdentityPins(wallet, attestor, submitter string) error {
	walletKey, walletErr := solana.Decode32(wallet)
	attestorKey, attestorErr := solana.Decode32(attestor)
	submitterKey, submitterErr := hex.DecodeString(submitter)
	if walletErr != nil || attestorErr != nil || submitterErr != nil ||
		len(submitterKey) != 32 || solana.Encode(walletKey[:]) != wallet ||
		solana.Encode(attestorKey[:]) != attestor ||
		hex.EncodeToString(submitterKey) != submitter || walletKey == attestorKey ||
		bytes.Equal(walletKey[:], submitterKey) || bytes.Equal(attestorKey[:], submitterKey) {
		return errors.New("self-hosted wallet, attestation, and submitter identities must be valid, canonical, and different")
	}
	return nil
}

func runProposalTurnkeyCheck(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("proposal turnkey-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	apiKeyPath := flags.String("api-key-file", "", "protected Turnkey API private-key file")
	apiPublicKey := flags.String("api-public-key", "", "registered Turnkey API public key")
	organizationID := flags.String("organization", "", "Turnkey organization ID")
	signWith := flags.String("sign-with", "", "Turnkey Solana signing address or private-key ID")
	expectedAddress := flags.String("expected-address", "", "expected public Solana account")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, proposalTurnkeyCheckUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*apiKeyPath) || *apiPublicKey == "" ||
		*organizationID == "" || *signWith == "" || *expectedAddress == "" {
		return errors.New("proposal turnkey-check requires an absolute protected API key file, matching public key, organization, signing resource, and expected Solana address")
	}
	if err := verifyTurnkeyIdentity(
		ctx,
		*apiKeyPath,
		*apiPublicKey,
		turnkeycustody.Config{OrganizationID: *organizationID, SignWith: *signWith},
		*expectedAddress,
	); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Status          string `json:"status"`
		SigningActivity bool   `json:"signing_activity"`
		CanSubmit       bool   `json:"can_submit"`
	}{Status: "turnkey_identity_verified", SigningActivity: false, CanSubmit: false})
}

func runProposalPolicyCheck(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("proposal policy-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	authorityPath := flags.String("authority-policy", "", "protected authority policy JSON path")
	signerPath := flags.String("signer-policy", "", "protected signer policy JSON path")
	submitterPath := flags.String("submitter-policy", "", "protected submitter policy JSON path")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, proposalPolicyCheckUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || !distinctAbsolutePaths(*authorityPath, *signerPath, *submitterPath) {
		return errors.New("proposal policy-check requires three distinct absolute protected policy paths")
	}
	var authority policyauthority.Policy
	if err := readStrictJSON(*authorityPath, &authority); err != nil {
		return errors.New("read protected authority policy")
	}
	var signing signer.Policy
	if err := readStrictJSON(*signerPath, &signing); err != nil {
		return errors.New("read protected signer policy")
	}
	var submission submitter.Policy
	if err := readStrictJSON(*submitterPath, &submission); err != nil {
		return errors.New("read protected submitter policy")
	}
	if err := validateJupiterPolicySet(authority, signing, submission); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Status            string `json:"status"`
		Cluster           string `json:"cluster"`
		Profile           string `json:"profile"`
		ProfileSHA256     string `json:"profile_sha256"`
		RecoveryMode      string `json:"recovery_mode"`
		SigningEnabled    bool   `json:"signing_enabled"`
		SubmissionEnabled bool   `json:"submission_enabled"`
	}{
		Status: "policies_consistent_not_authorized", Cluster: signing.Cluster,
		Profile: signing.Profile, ProfileSHA256: signing.ProfileFingerprint,
		RecoveryMode: submission.RecoveryMode,
	})
}

func runProposalBundleCheck(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("proposal bundle-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	candidatePath := flags.String("candidate", "", "private candidate JSON path")
	policyDir := flags.String("policy-dir", "", "generated Mainnet policy directory")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, proposalBundleCheckUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || !cleanAbsolutePath(*candidatePath) ||
		!cleanAbsolutePath(*policyDir) || *candidatePath == *policyDir {
		return errors.New("proposal bundle-check requires an absolute private candidate and protected policy directory")
	}

	authority, signing, submission, err := readProposalPolicyDirectory(*policyDir)
	if err != nil {
		return err
	}
	if err := validateJupiterPolicySet(authority, signing, submission); err != nil {
		return err
	}
	raw, err := securefile.ReadPrivate(*candidatePath, 1<<20)
	if err != nil {
		return errors.New("read private Jupiter candidate")
	}
	candidate, err := proposalcheck.DecodeCandidate(raw)
	if err != nil {
		return err
	}
	if _, _, err := proposalcheck.ValidateCandidateMaterial(
		*signing.Jupiter, candidate,
	); err != nil {
		return errors.New("candidate and generated Mainnet policies do not match")
	}
	return json.NewEncoder(output).Encode(struct {
		Status            string `json:"status"`
		Cluster           string `json:"cluster"`
		Profile           string `json:"profile"`
		ProfileSHA256     string `json:"profile_sha256"`
		RecoveryMode      string `json:"recovery_mode"`
		Next              string `json:"next"`
		SigningEnabled    bool   `json:"signing_enabled"`
		SubmissionEnabled bool   `json:"submission_enabled"`
	}{
		Status: "bundle_consistent_not_authorized", Cluster: signing.Cluster,
		Profile: signing.Profile, ProfileSHA256: signing.ProfileFingerprint,
		RecoveryMode: submission.RecoveryMode, Next: "proposal_prepare",
	})
}

func runProposalCanaryCheck(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("proposal canary-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyDir := flags.String("policy-dir", "", "generated Mainnet policy directory")
	operatorSocket := flags.String("operator-socket", "", "root-only submitter operator socket")
	requestPath := flags.String("request", "", "exact approved signer request")
	approvalPath := flags.String("operator-approval", "", "detached exact-request approval")
	shadowPolicyPath := flags.String("shadow-policy", "", "exact Mainnet shadow policy")
	shadowDirectory := flags.String("shadow-dir", "", "daily shadow journal directory")
	shadowDays := flags.Uint("shadow-days", 0, "required consecutive complete UTC days")
	command := flags.String(
		"command", "/usr/local/bin/mithril-agent-submitter", "absolute submitter executable",
	)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, proposalCanaryCheckUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *shadowDays == 0 || *shadowDays > 3650 ||
		!distinctAbsolutePaths(
			*policyDir, *operatorSocket, *requestPath, *approvalPath,
			*shadowPolicyPath, *shadowDirectory, *command,
		) {
		return errors.New("proposal canary-check requires distinct absolute policy, operator, exact approval, shadow evidence, and command paths plus --shadow-days from 1 to 3650")
	}
	authority, signing, submission, err := readProposalPolicyDirectory(*policyDir)
	if err != nil {
		return err
	}
	if err := validateJupiterPolicySet(authority, signing, submission); err != nil {
		return err
	}
	approved, err := verifyOperatorApprovalFiles(authority, *requestPath, *approvalPath)
	if err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	status, revision, err := inspectProposalCanaryControl(
		*operatorSocket,
		submitterclient.Identity{
			PublicKey:          submission.SubmitterPublicKey,
			ProfileFingerprint: submission.ProfileFingerprint,
			Source:             submission.Source,
		},
	)
	if err != nil {
		return err
	}
	if status.Mode != control.ModeNoNewActions || status.RecoveryPending ||
		status.TerminalActionID != "" || status.TerminalOutcome != "" {
		return errors.New("Mainnet canary control is not safely stopped")
	}
	strategyEvidence, err := checkProposalShadowEvidence(
		signing, *shadowPolicyPath, *shadowDirectory, uint32(*shadowDays), time.Now().UTC(),
	)
	if err != nil {
		return err
	}
	environment, err := mainnetReadinessEnvironment()
	if err != nil {
		return err
	}
	actionID, err := checkProposalMainnetReadiness(
		checkCtx,
		*command,
		filepath.Join(*policyDir, proposalSubmitterPolicyName),
		environment,
	)
	if err != nil {
		return err
	}
	if actionID != approved.ActionID {
		return errors.New("prepared Mainnet action does not match operator approval")
	}
	routeAtomic, routeProtection, err := guardedRouteReceipt(signing, submission, actionID)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
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
	}{
		Status: "mainnet_canary_evidence_ready_not_enabled", ActionID: actionID,
		ControlRevision: revision, StrategyEvidence: true,
		ShadowPolicy:    strategyEvidence.PolicySHA256,
		ShadowDays:      strategyEvidence.CompleteDays,
		ApprovedRequest: approved.RequestSHA256, RecoveryMode: submission.RecoveryMode,
		RouteUpgradeAtomic: routeAtomic, RouteProtection: routeProtection,
	})
}

func guardedRouteReceipt(
	signing signer.Policy,
	submission submitter.Policy,
	readinessActionID string,
) (bool, string, error) {
	if readinessActionID == "" || signing.Jupiter == nil || submission.Jupiter == nil ||
		signing.Profile != jupiterswap.ProfileName ||
		signing.ProfileVersion != jupiterswap.ProfileVersion ||
		*signing.Jupiter != *submission.Jupiter || signing.Jupiter.RouteGuard.Validate() != nil {
		return false, "", errors.New("Mainnet readiness did not verify one guarded v7 policy")
	}
	return true, "immutable_guard_exact_code_pinned", nil
}

func mainnetReadinessEnvironment() ([]string, error) {
	names := []string{
		"MITHRIL_AGENT_MITHRIL_RPC_URL",
		"MITHRIL_AGENT_PRIMARY_RPC_URL",
		"MITHRIL_AGENT_SECONDARY_RPC_URL",
	}
	environment := make([]string, 0, len(names))
	for _, name := range names {
		value := os.Getenv(name)
		if value == "" {
			return nil, errors.New("Mithril and two independent evidence RPCs are required")
		}
		environment = append(environment, name+"="+value)
	}
	return environment, nil
}

func readProposalPolicyDirectory(
	policyDir string,
) (policyauthority.Policy, signer.Policy, submitter.Policy, error) {
	var authority policyauthority.Policy
	var signing signer.Policy
	var submission submitter.Policy
	if err := readStrictJSON(
		filepath.Join(policyDir, proposalAuthorityPolicyName), &authority,
	); err != nil {
		return authority, signing, submission, errors.New("read protected authority policy")
	}
	if err := readStrictJSON(
		filepath.Join(policyDir, proposalSignerPolicyName), &signing,
	); err != nil {
		return authority, signing, submission, errors.New("read protected signer policy")
	}
	if err := readStrictJSON(
		filepath.Join(policyDir, proposalSubmitterPolicyName), &submission,
	); err != nil {
		return authority, signing, submission, errors.New("read protected submitter policy")
	}
	return authority, signing, submission, nil
}

func cleanAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func distinctAbsolutePaths(paths ...string) bool {
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return false
		}
		if _, exists := seen[path]; exists {
			return false
		}
		seen[path] = struct{}{}
	}
	return true
}

func validateJupiterPolicySet(
	authority policyauthority.Policy,
	signing signer.Policy,
	submission submitter.Policy,
) error {
	if err := authority.Validate(); err != nil {
		return errors.New("authority policy is not a valid Mainnet Jupiter policy")
	}
	if err := signer.ValidateJupiterPolicy(signing); err != nil {
		return errors.New("signer policy is not a valid Mainnet Jupiter policy")
	}
	if err := submitter.ValidateJupiterPolicy(submission); err != nil {
		return errors.New("submitter policy is not a valid Mainnet Jupiter policy")
	}
	if !sameJupiterSignerPolicy(authority.TransactionPolicy, signing) {
		return errors.New("authority and signer policies do not match")
	}
	if authority.JupiterProviders == nil || *authority.JupiterProviders != submission.Evidence {
		return errors.New("authority and submitter evidence providers do not match")
	}
	if signing.Jupiter == nil || submission.Jupiter == nil ||
		*signing.Jupiter != *submission.Jupiter ||
		submission.Cluster != signing.Cluster ||
		submission.Profile != signing.Profile ||
		submission.ProfileFingerprint != signing.ProfileFingerprint ||
		submission.Source != signing.Source ||
		submission.MaxLamports != signing.MaxLamports ||
		submission.MaxInputTokenAmount != signing.MaxInputTokenAmount ||
		submission.MaxFeeLamports != signing.MaxFeeLamports ||
		submission.ScheduleWindowSeconds != signing.ScheduleWindowSeconds ||
		submission.ScheduleAnchorUnix != signing.ScheduleAnchorUnix ||
		submission.MaxBlockHeightWindow != signing.MaxBlockHeightWindow ||
		submission.SubmitterPublicKey != signing.SubmitterPublicKey ||
		submission.AttestationPublicKey != signing.AttestationPublicKey {
		return errors.New("signer and submitter policies do not match")
	}
	if policyStatePathsCollide(signing.AuthorizationLedgerPath, submission.ControlStatePath) {
		return errors.New("signer and submitter policy state paths collide")
	}
	return nil
}

func sameJupiterSignerPolicy(left, right signer.Policy) bool {
	if left.Jupiter == nil || right.Jupiter == nil || *left.Jupiter != *right.Jupiter {
		return false
	}
	left.Jupiter = nil
	right.Jupiter = nil
	return left == right
}

func policyStatePathsCollide(ledgerPath, controlPath string) bool {
	recoveryPath := filepath.Join(filepath.Dir(controlPath), "submission-recovery.json")
	for _, signerPath := range []string{ledgerPath, ledgerPath + ".lock", ledgerPath + ".reserve"} {
		for _, submitterPath := range []string{
			controlPath, controlPath + ".lock", recoveryPath, recoveryPath + ".lock",
		} {
			if signerPath == submitterPath {
				return true
			}
		}
	}
	return false
}
