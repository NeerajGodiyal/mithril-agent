// Package proposalcheck validates a Jupiter proposal without signing or
// submitting it.
package proposalcheck

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const (
	maxBlockHeightWindow = uint64(150)

	// MaxEvidenceSlotSkew is the largest accepted distance between independent
	// evidence observations for one proposal.
	MaxEvidenceSlotSkew = uint64(150)

	// StatusCheckedNotAuthorized means every read-only check passed, but the
	// proposal has not passed a Mainnet signing policy.
	StatusCheckedNotAuthorized = "checked_not_authorized"
	// ReasonSigningPolicyAbsent is the permanent reason this read-only command
	// cannot turn a successful check into signing authority.
	ReasonSigningPolicyAbsent = "mainnet_signing_policy_not_configured"
)

// Builder returns one bounded, untrusted Jupiter transaction proposal.
type Builder interface {
	Build(context.Context, jupiterquote.Request) (jupiterquote.BuildResult, error)
}

// Evidence is the read-only chain evidence required to inspect a proposal.
type Evidence interface {
	EvidenceProviderIdentities() (string, string)
	VerifyGenesis(context.Context, string) error
	VerifyFinalizedV0History(context.Context, string) error
	VerifyUpgradeableProgramDeployment(context.Context, string, string, string, uint64, uint64) error
	VerifyImmutableProgramDeployment(context.Context, string, string, uint64, uint64, string, uint64) error
	VerifyAddressLookupTables(context.Context, map[[32]byte][][32]byte, uint64) (map[[32]byte][][32]byte, error)
	FeeForV0Message(context.Context, []byte, map[[32]byte][][32]byte, string, uint64) (txflow.FeeEvidence, error)
	SimulateV0(context.Context, []byte, map[[32]byte][][32]byte, string, uint64) (txflow.LegacySimulationEvidence, error)
	NodeBlockHeight(context.Context, uint64) (uint64, error)
	VerifyTokenInputAccount(context.Context, string, string, string, uint64, uint64) (txflow.TokenAccountEvidence, error)
	VerifyTokenOutputAccount(context.Context, string, string, string, uint64) (txflow.TokenAccountEvidence, error)
	VerifyTokenAccountRent(context.Context, uint64) (txflow.RentEvidence, error)
}

// FinalizedSlotReader supplies an independent finalized slot.
type FinalizedSlotReader interface {
	FinalizedSlot(context.Context) (uint64, error)
	Identity() string
}

// ProviderBindings pins the two independent evidence-provider owners and
// credential-free origins selected by protected operator configuration.
type ProviderBindings struct {
	PrimaryTrustDomain    string `json:"primary_trust_domain"`
	PrimaryOriginSHA256   string `json:"primary_origin_sha256"`
	SecondaryTrustDomain  string `json:"secondary_trust_domain"`
	SecondaryOriginSHA256 string `json:"secondary_origin_sha256"`
	ArchiveProbeSignature string `json:"archive_probe_signature,omitempty"`
}

// Validate requires two distinct named providers with canonical origin hashes.
func (p ProviderBindings) Validate() error {
	if !validTrustDomain(p.PrimaryTrustDomain) ||
		!validTrustDomain(p.SecondaryTrustDomain) ||
		p.PrimaryTrustDomain == p.SecondaryTrustDomain ||
		!validSHA256(p.PrimaryOriginSHA256) ||
		!validSHA256(p.SecondaryOriginSHA256) ||
		p.PrimaryOriginSHA256 == p.SecondaryOriginSHA256 {
		return errors.New("evidence providers require distinct protected bindings")
	}
	if p.ArchiveProbeSignature != "" {
		if _, err := solana.Decode64(p.ArchiveProbeSignature); err != nil {
			return errors.New("archive probe signature is invalid")
		}
	}
	return nil
}

// ValidateArchiveProbe additionally requires the protected historical
// transaction used to qualify both Mainnet evidence providers.
func (p ProviderBindings) ValidateArchiveProbe() error {
	if err := p.Validate(); err != nil {
		return err
	}
	if _, err := solana.Decode64(p.ArchiveProbeSignature); err != nil {
		return errors.New("evidence providers require a valid archive probe signature")
	}
	return nil
}

// Result is a sanitized proof that the read-only checks completed. It never
// represents authorization to sign or submit the proposal.
type Result struct {
	Status                  string `json:"status"`
	Reason                  string `json:"reason"`
	Cluster                 string `json:"cluster"`
	PolicySHA256            string `json:"policy_sha256"`
	InputMint               string `json:"input_mint"`
	OutputMint              string `json:"output_mint"`
	MinimumContextSlot      uint64 `json:"minimum_context_slot"`
	InputAmount             uint64 `json:"input_amount"`
	EstimatedOutput         uint64 `json:"estimated_output"`
	MinimumOutput           uint64 `json:"minimum_output"`
	InstructionCount        int    `json:"instruction_count"`
	AddressTableCount       int    `json:"address_table_count"`
	MessageSHA256           string `json:"message_sha256"`
	FeeLamports             uint64 `json:"fee_lamports"`
	FeeMinContextSlot       uint64 `json:"fee_min_context_slot"`
	PrimaryFeeContextSlot   uint64 `json:"primary_fee_context_slot"`
	SecondaryFeeContextSlot uint64 `json:"secondary_fee_context_slot"`
	TokenAccountRent        uint64 `json:"token_account_rent_lamports"`
	OutputAccountRent       uint64 `json:"output_account_rent_lamports,omitempty"`
	MaximumDebitLamports    uint64 `json:"maximum_debit_lamports"`
	MaximumUpfrontLamports  uint64 `json:"maximum_upfront_lamports"`
	SimulationContextSlot   uint64 `json:"simulation_context_slot"`
	SimulationUnits         uint64 `json:"simulation_units,omitempty"`
	ComputeUnitLimit        uint32 `json:"compute_unit_limit"`
	ComputeUnitPrice        uint64 `json:"compute_unit_price_micro_lamports"`
	SimulationLogsSHA256    string `json:"simulation_logs_sha256"`
	LastValidBlockHeight    uint64 `json:"last_valid_block_height"`
	ObservedBlockHeight     uint64 `json:"observed_block_height"`
	PrimaryTrustDomain      string `json:"primary_trust_domain"`
	PrimaryOriginSHA256     string `json:"primary_origin_sha256"`
	SecondaryTrustDomain    string `json:"secondary_trust_domain"`
	SecondaryOriginSHA256   string `json:"secondary_origin_sha256"`
	ArchiveProbeSignature   string `json:"archive_probe_signature"`
	SigningEnabled          bool   `json:"signing_enabled"`
	SubmissionEnabled       bool   `json:"submission_enabled"`

	message              []byte
	addressTables        map[[32]byte][][32]byte
	policy               jupiterswap.Policy
	request              jupiterquote.Request
	quote                jupiterquote.Result
	lastValidBlockHeight uint64
	providers            ProviderBindings
}

// Message returns the exact checked message bytes. It does not authorize
// signing, and callers receive a copy so the evidence result cannot be
// mutated after checking.
func (r Result) Message() []byte { return append([]byte(nil), r.message...) }

// AddressTables returns the independently verified lookup-table contents used
// to compile Message. It does not authorize signing.
func (r Result) AddressTables() map[[32]byte][][32]byte {
	tables := make(map[[32]byte][][32]byte, len(r.addressTables))
	for table, addresses := range r.addressTables {
		tables[table] = append([][32]byte(nil), addresses...)
	}
	return tables
}

// Check validates and simulates one proposal without signing or submitting it.
func Check(
	ctx context.Context,
	builder Builder,
	evidence Evidence,
	primary, secondary FinalizedSlotReader,
	primaryTrustDomain, secondaryTrustDomain, archiveProbeSignature string,
	policy jupiterswap.Policy,
	request jupiterquote.Request,
) (Result, error) {
	if builder == nil || evidence == nil || primary == nil || secondary == nil {
		return Result{}, errors.New("proposal checker dependencies are required")
	}
	if err := policy.Validate(); err != nil {
		return Result{}, errors.New("Jupiter proposal policy is invalid")
	}
	providers, err := bindProviders(
		evidence, primary, secondary, primaryTrustDomain, secondaryTrustDomain,
		archiveProbeSignature,
	)
	if err != nil {
		return Result{}, err
	}
	minimumSlot, newestSlot, err := verifyEnvironment(
		ctx, evidence, primary, secondary, providers.ArchiveProbeSignature, policy,
	)
	if err != nil {
		return Result{}, err
	}

	proposal, err := builder.Build(ctx, request)
	if err != nil {
		return Result{}, errors.New("build Jupiter proposal")
	}
	if err := verifyTradeAccount(ctx, evidence, policy, request, minimumSlot); err != nil {
		return Result{}, err
	}
	proposal.Instructions, err = jupiterswap.RemoveRedundantOutputAccountSetup(
		request, proposal.Instructions,
	)
	if err != nil {
		return Result{}, errors.New("normalize pre-created Jupiter output account")
	}
	_, err = jupiterswap.ValidateProposal(
		policy, request, proposal.Quote, proposal.ComputeBudget, proposal.Instructions,
	)
	if err != nil {
		return Result{}, fmt.Errorf(
			"Jupiter returned an unsupported Exact-In plan; no action was taken; retry later: %w",
			err,
		)
	}
	estimateLimit, err := solana.SetComputeUnitLimitInstruction(solana.MaxComputeUnitLimit)
	if err != nil {
		return Result{}, errors.New("build maximum compute unit limit")
	}
	estimateInstructions := make([]solana.Instruction, 1, len(proposal.Instructions)+1)
	estimateInstructions[0] = estimateLimit
	estimateInstructions = append(estimateInstructions, proposal.Instructions...)
	var verifiedTables map[[32]byte][][32]byte
	tablesVerified := false
	verifyTables := func() error {
		if tablesVerified {
			return nil
		}
		var verifyErr error
		verifiedTables, verifyErr = evidence.VerifyAddressLookupTables(
			ctx, cloneAddressTables(proposal.ClaimedAddressTables), minimumSlot,
		)
		if verifyErr != nil {
			return verifyErr
		}
		tablesVerified = true
		return nil
	}
	estimateMessage, err := jupiterswap.BuildGuardedPolicyV0Message(
		policy, request.Taker,
		solana.Encode(proposal.RecentBlockhash[:]),
		estimateInstructions,
		nil,
	)
	if err != nil {
		if verifyTables() != nil {
			return Result{}, errors.New("verify proposal address tables")
		}
		estimateMessage, err = jupiterswap.BuildGuardedPolicyV0Message(
			policy, request.Taker,
			solana.Encode(proposal.RecentBlockhash[:]),
			estimateInstructions,
			verifiedTables,
		)
		if err != nil {
			return Result{}, fmt.Errorf("compile compute estimate proposal: %w", err)
		}
	}
	estimateTables, err := jupiterswap.UsedAddressTables(
		estimateMessage, verifiedTables,
	)
	if err != nil {
		return Result{}, errors.New("select compute estimate address tables")
	}
	estimate, err := evidence.SimulateV0(
		ctx, bytes.Clone(estimateMessage), cloneAddressTables(estimateTables), request.Taker, minimumSlot,
	)
	if err != nil {
		return Result{}, errors.New("estimate proposal compute units on Mithril")
	}
	if estimate.UnitsConsumed == 0 || estimate.UnitsConsumed > uint64(solana.MaxComputeUnitLimit) {
		return Result{}, errors.New("Mithril compute estimate is invalid")
	}
	computeUnits := (estimate.UnitsConsumed*6 + 4) / 5
	if computeUnits > uint64(solana.MaxComputeUnitLimit) {
		computeUnits = uint64(solana.MaxComputeUnitLimit)
	}
	if computeUnits > uint64(policy.MaxComputeUnits) {
		return Result{}, errors.New("proposal compute estimate exceeds policy")
	}
	finalLimit, err := solana.SetComputeUnitLimitInstruction(uint32(computeUnits))
	if err != nil {
		return Result{}, errors.New("build final compute unit limit")
	}
	finalInstructions := make([]solana.Instruction, 1, 1+len(proposal.ComputeBudget)+len(proposal.Instructions))
	finalInstructions[0] = finalLimit
	finalInstructions = append(finalInstructions, proposal.ComputeBudget...)
	finalInstructions = append(finalInstructions, proposal.Instructions...)
	message, err := jupiterswap.BuildGuardedPolicyV0Message(
		policy, request.Taker,
		solana.Encode(proposal.RecentBlockhash[:]),
		finalInstructions,
		verifiedTables,
	)
	if err != nil && !tablesVerified {
		if verifyTables() != nil {
			return Result{}, errors.New("verify proposal address tables")
		}
		message, err = jupiterswap.BuildGuardedPolicyV0Message(
			policy, request.Taker,
			solana.Encode(proposal.RecentBlockhash[:]),
			finalInstructions,
			verifiedTables,
		)
	}
	if err != nil {
		return Result{}, fmt.Errorf("compile verified v0 proposal: %w", err)
	}
	usedTables, err := jupiterswap.UsedAddressTables(message, verifiedTables)
	if err != nil {
		return Result{}, errors.New("select verified proposal address tables")
	}
	return verifyCandidateAt(
		ctx, evidence, policy, request, proposal.Quote, message, usedTables,
		minimumSlot, newestSlot, proposal.LastValidBlockHeight, providers,
	)
}

// Recheck repeats every external and Mithril check over the exact immutable
// candidate retained by Check. It remains read-only and non-authorizing; a
// future policy authority can consume this value without asking Jupiter to
// build different bytes.
func Recheck(
	ctx context.Context,
	evidence Evidence,
	primary, secondary FinalizedSlotReader,
	expectedPolicy jupiterswap.Policy,
	expectedProviders ProviderBindings,
	candidate Candidate,
) (Result, error) {
	if evidence == nil || primary == nil || secondary == nil {
		return Result{}, errors.New("checked Jupiter candidate is unavailable")
	}
	message, addressTables, err := ValidateCandidateMaterial(expectedPolicy, candidate)
	if err != nil {
		return Result{}, err
	}
	providers, err := bindProviders(
		evidence, primary, secondary,
		expectedProviders.PrimaryTrustDomain,
		expectedProviders.SecondaryTrustDomain,
		expectedProviders.ArchiveProbeSignature,
	)
	if err != nil {
		return Result{}, err
	}
	if providers != expectedProviders {
		return Result{}, errors.New("evidence provider bindings changed")
	}
	minimumSlot, newestSlot, err := verifyEnvironment(
		ctx, evidence, primary, secondary, providers.ArchiveProbeSignature, expectedPolicy,
	)
	if err != nil {
		return Result{}, err
	}
	verifiedTables := addressTables
	if len(addressTables) != 0 {
		verifiedTables, err = evidence.VerifyAddressLookupTables(
			ctx, cloneAddressTables(addressTables), minimumSlot,
		)
		if err != nil {
			return Result{}, errors.New("reverify proposal address tables")
		}
	}
	if err := verifyTradeAccount(
		ctx, evidence, candidate.Policy, candidate.Request, minimumSlot,
	); err != nil {
		return Result{}, err
	}
	return verifyCandidateAt(
		ctx, evidence, candidate.Policy, candidate.Request, candidate.Quote,
		message, verifiedTables, minimumSlot, newestSlot,
		candidate.LastValidBlockHeight, providers,
	)
}

func bindProviders(
	evidence Evidence,
	primary, secondary FinalizedSlotReader,
	primaryTrustDomain, secondaryTrustDomain, archiveProbeSignature string,
) (ProviderBindings, error) {
	if !validTrustDomain(primaryTrustDomain) || !validTrustDomain(secondaryTrustDomain) ||
		primaryTrustDomain == secondaryTrustDomain {
		return ProviderBindings{}, errors.New("evidence providers require distinct trust domains")
	}
	bindings := ProviderBindings{
		PrimaryTrustDomain: primaryTrustDomain, PrimaryOriginSHA256: primary.Identity(),
		SecondaryTrustDomain: secondaryTrustDomain, SecondaryOriginSHA256: secondary.Identity(),
		ArchiveProbeSignature: archiveProbeSignature,
	}
	if err := bindings.ValidateArchiveProbe(); err != nil {
		return ProviderBindings{}, err
	}
	evidencePrimary, evidenceSecondary := evidence.EvidenceProviderIdentities()
	if evidencePrimary != bindings.PrimaryOriginSHA256 ||
		evidenceSecondary != bindings.SecondaryOriginSHA256 {
		return ProviderBindings{}, errors.New("evidence provider identities do not match slot readers")
	}
	return bindings, nil
}

func validTrustDomain(value string) bool {
	if len(value) < 2 || len(value) > 64 {
		return false
	}
	for index, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' ||
			index > 0 && (char == '-' || char == '_' || char == '.') {
			continue
		}
		return false
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func verifyEnvironment(
	ctx context.Context,
	evidence Evidence,
	primary, secondary FinalizedSlotReader,
	archiveProbeSignature string,
	policy jupiterswap.Policy,
) (uint64, uint64, error) {
	if err := evidence.VerifyGenesis(ctx, solana.MainnetBetaGenesisHash); err != nil {
		return 0, 0, errors.New("verify Mainnet RPC identities")
	}
	primarySlot, err := primary.FinalizedSlot(ctx)
	if err != nil {
		return 0, 0, errors.New("read primary finalized slot")
	}
	secondarySlot, err := secondary.FinalizedSlot(ctx)
	if err != nil {
		return 0, 0, errors.New("read secondary finalized slot")
	}
	minimumSlot, newestSlot := min(primarySlot, secondarySlot), max(primarySlot, secondarySlot)
	if minimumSlot == 0 {
		return 0, 0, errors.New("independent finalized slot is zero")
	}
	if newestSlot-minimumSlot > MaxEvidenceSlotSkew {
		return 0, 0, errors.New("independent finalized slots are too far apart")
	}
	if err := evidence.VerifyUpgradeableProgramDeployment(
		ctx, jupiterswap.Program, jupiterswap.ProgramData,
		jupiterswap.UpgradeAuthority, jupiterswap.DeploymentSlot, minimumSlot,
	); err != nil {
		return 0, 0, errors.New("verify pinned Jupiter deployment")
	}
	if err := evidence.VerifyImmutableProgramDeployment(
		ctx, policy.RouteGuard.Program, policy.RouteGuard.ProgramData,
		policy.RouteGuard.DeploymentSlot, policy.RouteGuard.CodeLength,
		policy.RouteGuard.CodeSHA256, minimumSlot,
	); err != nil {
		return 0, 0, errors.New("verify immutable route guard deployment")
	}
	if err := evidence.VerifyFinalizedV0History(ctx, archiveProbeSignature); err != nil {
		return 0, 0, errors.New("verify independent finalized archive probe")
	}
	return minimumSlot, newestSlot, nil
}

func verifyCandidateAt(
	ctx context.Context,
	evidence Evidence,
	policy jupiterswap.Policy,
	request jupiterquote.Request,
	quote jupiterquote.Result,
	message []byte,
	verifiedTables map[[32]byte][][32]byte,
	minimumSlot, newestSlot, lastValidBlockHeight uint64,
	providers ProviderBindings,
) (Result, error) {
	candidateMessage := bytes.Clone(message)
	candidateTables := cloneAddressTables(verifiedTables)
	intent, err := jupiterswap.ValidateV0Message(
		policy, request, quote, candidateMessage, candidateTables,
	)
	if err != nil {
		return Result{}, errors.New("compiled Jupiter proposal is not canonical")
	}
	rent, err := evidence.VerifyTokenAccountRent(ctx, policy.MaxTokenAccountRentLamports)
	if err != nil {
		return Result{}, errors.New("verify token-account rent")
	}
	fee, err := evidence.FeeForV0Message(
		ctx, bytes.Clone(candidateMessage), cloneAddressTables(candidateTables), request.Taker, minimumSlot,
	)
	if err != nil {
		return Result{}, errors.New("verify proposal fee")
	}
	if fee.Lamports == 0 || fee.Lamports > policy.MaxFeeLamports {
		return Result{}, errors.New("proposal fee exceeds policy")
	}
	outputRent, maximumDebit, maximumUpfront, err := jupiterLamportBounds(
		policy, intent.OutputAccountCreated, request.InputAmount, fee.Lamports, rent.Lamports,
	)
	if err != nil {
		return Result{}, err
	}
	oldestFeeSlot := min(fee.PrimaryContextSlot, fee.SecondaryContextSlot)
	newestFeeSlot := max(fee.PrimaryContextSlot, fee.SecondaryContextSlot)
	if oldestFeeSlot == 0 || newestFeeSlot-oldestFeeSlot > MaxEvidenceSlotSkew {
		return Result{}, errors.New("independent fee contexts are too far apart")
	}
	simulation, err := evidence.SimulateV0(
		ctx, bytes.Clone(candidateMessage), cloneAddressTables(candidateTables), request.Taker, minimumSlot,
	)
	if err != nil {
		return Result{}, errors.New("simulate final proposal on Mithril")
	}
	decoded, err := solana.DecodeV0Message(candidateMessage, candidateTables)
	if err != nil || len(decoded.Instructions) == 0 || len(decoded.Instructions[0].Data) != 5 {
		return Result{}, errors.New("decode final proposal compute limit")
	}
	computeUnits := binary.LittleEndian.Uint32(decoded.Instructions[0].Data[1:])
	if simulation.UnitsConsumed == 0 || simulation.UnitsConsumed > uint64(computeUnits) {
		return Result{}, errors.New("final proposal compute evidence is invalid")
	}
	oldestEvidenceSlot := min(minimumSlot, oldestFeeSlot, simulation.ContextSlot)
	newestEvidenceSlot := max(newestSlot, newestFeeSlot, simulation.ContextSlot)
	if newestEvidenceSlot-oldestEvidenceSlot > MaxEvidenceSlotSkew {
		return Result{}, errors.New("Mithril simulation context is too far from independent evidence")
	}
	blockHeight, err := evidence.NodeBlockHeight(ctx, minimumSlot)
	if err != nil {
		return Result{}, errors.New("read Mithril block height")
	}
	if lastValidBlockHeight <= blockHeight ||
		lastValidBlockHeight-blockHeight > maxBlockHeightWindow {
		return Result{}, errors.New("proposal blockhash lifetime is outside policy")
	}
	if _, err := jupiterswap.ValidateV0Message(
		policy, request, quote, candidateMessage, candidateTables,
	); err != nil {
		return Result{}, errors.New("retained Jupiter proposal is not canonical")
	}
	digest := sha256.Sum256(candidateMessage)
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		return Result{}, errors.New("fingerprint Jupiter proposal policy")
	}
	return Result{
		Status: StatusCheckedNotAuthorized, Reason: ReasonSigningPolicyAbsent,
		Cluster: "mainnet-beta", PolicySHA256: policyFingerprint,
		InputMint: policy.InputMint, OutputMint: policy.OutputMint,
		MinimumContextSlot: minimumSlot,
		InputAmount:        quote.InputAmount, EstimatedOutput: quote.EstimatedOutput,
		MinimumOutput:    quote.MinimumOutput,
		InstructionCount: len(decoded.Instructions), AddressTableCount: len(candidateTables),
		MessageSHA256: hex.EncodeToString(digest[:]), FeeLamports: fee.Lamports,
		FeeMinContextSlot:       minimumSlot,
		PrimaryFeeContextSlot:   fee.PrimaryContextSlot,
		SecondaryFeeContextSlot: fee.SecondaryContextSlot,
		TokenAccountRent:        rent.Lamports, OutputAccountRent: outputRent,
		MaximumDebitLamports: maximumDebit, MaximumUpfrontLamports: maximumUpfront,
		SimulationContextSlot: simulation.ContextSlot, SimulationUnits: simulation.UnitsConsumed,
		ComputeUnitLimit:     computeUnits,
		ComputeUnitPrice:     intent.ComputeUnitPriceMicroLamport,
		SimulationLogsSHA256: simulation.LogsSHA256,
		LastValidBlockHeight: lastValidBlockHeight, ObservedBlockHeight: blockHeight,
		PrimaryTrustDomain:    providers.PrimaryTrustDomain,
		PrimaryOriginSHA256:   providers.PrimaryOriginSHA256,
		SecondaryTrustDomain:  providers.SecondaryTrustDomain,
		SecondaryOriginSHA256: providers.SecondaryOriginSHA256,
		ArchiveProbeSignature: providers.ArchiveProbeSignature,
		SigningEnabled:        false, SubmissionEnabled: false,
		message: candidateMessage, addressTables: candidateTables,
		policy: policy, request: request, quote: quote,
		lastValidBlockHeight: lastValidBlockHeight,
		providers:            providers,
	}, nil
}

func verifyTradeAccount(
	ctx context.Context,
	evidence Evidence,
	policy jupiterswap.Policy,
	request jupiterquote.Request,
	minimumSlot uint64,
) error {
	if policy.NativeInput() {
		if _, err := evidence.VerifyTokenOutputAccount(
			ctx, request.DestinationTokenAccount, policy.OutputMint, policy.Owner, minimumSlot,
		); err != nil {
			return errors.New("verify pre-created canonical output token account")
		}
		return nil
	}
	inputAccount, err := orcaswap.AssociatedTokenAddress(policy.Owner, policy.InputMint)
	if err != nil {
		return errors.New("derive canonical input token account")
	}
	if _, err := evidence.VerifyTokenInputAccount(
		ctx, inputAccount, policy.InputMint, policy.Owner, request.InputAmount, minimumSlot,
	); err != nil {
		return errors.New("verify funded canonical input token account")
	}
	return nil
}

func jupiterLamportBounds(
	policy jupiterswap.Policy,
	outputAccountCreated bool,
	inputAmount, feeLamports, tokenAccountRent uint64,
) (outputRent, maximumDebit, maximumUpfront uint64, err error) {
	if inputAmount == 0 || feeLamports == 0 || tokenAccountRent == 0 {
		return 0, 0, 0, errors.New("proposal debit inputs are invalid")
	}
	if policy.NativeInput() {
		if outputAccountCreated {
			outputRent = tokenAccountRent
		}
		if inputAmount > ^uint64(0)-feeLamports ||
			inputAmount+feeLamports > ^uint64(0)-outputRent {
			return 0, 0, 0, errors.New("proposal maximum debit overflows")
		}
		maximumDebit = inputAmount + feeLamports + outputRent
	} else {
		if !policy.NativeOutput() || outputAccountCreated {
			return 0, 0, 0, errors.New("proposal token-input accounting is invalid")
		}
		maximumDebit = feeLamports
	}
	if maximumDebit > ^uint64(0)-tokenAccountRent {
		return 0, 0, 0, errors.New("proposal maximum upfront balance overflows")
	}
	return outputRent, maximumDebit, maximumDebit + tokenAccountRent, nil
}

func cloneAddressTables(tables map[[32]byte][][32]byte) map[[32]byte][][32]byte {
	cloned := make(map[[32]byte][][32]byte, len(tables))
	for table, addresses := range tables {
		cloned[table] = append([][32]byte(nil), addresses...)
	}
	return cloned
}
