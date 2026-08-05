package txflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/bits"
	"slices"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

const (
	StateAccepted  = "accepted"
	StateAmbiguous = "ambiguous"

	VerdictPending    = "pending"
	VerdictFinalized  = "finalized"
	VerdictFailed     = "failed"
	VerdictUnresolved = "unresolved"
	VerdictDiverged   = "diverged"

	DivergenceStatus  = "status"
	DivergenceEffects = "effects"
)

var errFinalizedEffectsDiverged = errors.New("finalized transaction effects diverged")

type NodeProvider interface {
	Identity() string
	GenesisHash(context.Context) (string, error)
	BlockHeight(context.Context) (uint64, error)
	SimulateTransfer(context.Context, []byte, uint64) (solanarpc.Simulation, error)
	SendTransaction(context.Context, []byte, uint64) (string, error)
}

type EvidenceProvider interface {
	Identity() string
	GenesisHash(context.Context) (string, error)
	FinalizedSlot(context.Context) (uint64, error)
	Account(context.Context, string, uint64) (solanarpc.AccountQuote, error)
	AccountSlice(context.Context, string, uint64, uint64, uint64) (solanarpc.AccountDataSlice, error)
	MinimumBalanceForRentExemption(context.Context, uint64) (uint64, error)
	FeeForMessage(context.Context, []byte, uint64) (solanarpc.FeeQuote, error)
	SignatureStatus(context.Context, string) (solanarpc.SignatureStatus, error)
	TransactionEffect(context.Context, string) (solanarpc.TransactionEffect, error)
	BlockHeight(context.Context) (uint64, error)
}

type RentEvidence struct {
	Lamports          uint64 `json:"lamports"`
	PrimaryLamports   uint64 `json:"primary_lamports"`
	SecondaryLamports uint64 `json:"secondary_lamports"`
}

type TokenAccountEvidence struct {
	Amount               uint64 `json:"amount"`
	PrimaryContextSlot   uint64 `json:"primary_context_slot"`
	SecondaryContextSlot uint64 `json:"secondary_context_slot"`
}

func (l *Lifecycle) VerifyTokenInputAccount(
	ctx context.Context,
	address,
	mint,
	owner string,
	minimumAmount,
	minContextSlot uint64,
) (TokenAccountEvidence, error) {
	if _, err := solana.Decode32(address); err != nil {
		return TokenAccountEvidence{}, errors.New("token account address is invalid")
	}
	wantMint, mintErr := solana.Decode32(mint)
	wantOwner, ownerErr := solana.Decode32(owner)
	if mintErr != nil || ownerErr != nil || minimumAmount == 0 || minContextSlot == 0 {
		return TokenAccountEvidence{}, errors.New("token account policy is invalid")
	}
	const tokenAccountLength = uint64(165)
	primary, secondary, err := queryPair(
		ctx,
		func(ctx context.Context) (solanarpc.AccountDataSlice, error) {
			return l.primary.AccountSlice(ctx, address, minContextSlot, 0, tokenAccountLength)
		},
		func(ctx context.Context) (solanarpc.AccountDataSlice, error) {
			return l.secondary.AccountSlice(ctx, address, minContextSlot, 0, tokenAccountLength)
		},
	)
	if err != nil {
		return TokenAccountEvidence{}, errors.New("query independent token account evidence")
	}
	if !accountSlicesEqual(primary, secondary, minContextSlot, int(tokenAccountLength)) ||
		primary.DataLength != tokenAccountLength || secondary.DataLength != tokenAccountLength ||
		primary.Owner != orcaswap.TokenProgram || primary.Executable ||
		!bytes.Equal(primary.Data[:32], wantMint[:]) ||
		!bytes.Equal(primary.Data[32:64], wantOwner[:]) ||
		primary.Data[108] != 1 || binary.LittleEndian.Uint32(primary.Data[109:113]) != 0 {
		return TokenAccountEvidence{}, errors.New("token account identity does not match the pinned profile")
	}
	amount := binary.LittleEndian.Uint64(primary.Data[64:72])
	if amount < minimumAmount {
		return TokenAccountEvidence{}, errors.New("token account balance is below the active profile")
	}
	return TokenAccountEvidence{
		Amount: amount, PrimaryContextSlot: primary.ContextSlot,
		SecondaryContextSlot: secondary.ContextSlot,
	}, nil
}

func (l *Lifecycle) VerifyTokenAccountRent(ctx context.Context, maximum uint64) (RentEvidence, error) {
	if maximum == 0 {
		return RentEvidence{}, errors.New("maximum token-account rent is required")
	}
	const tokenAccountSize = uint64(165)
	primary, secondary, err := queryPair(
		ctx,
		func(ctx context.Context) (uint64, error) {
			return l.primary.MinimumBalanceForRentExemption(ctx, tokenAccountSize)
		},
		func(ctx context.Context) (uint64, error) {
			return l.secondary.MinimumBalanceForRentExemption(ctx, tokenAccountSize)
		},
	)
	if err != nil {
		return RentEvidence{}, errors.New("query independent token-account rent")
	}
	if primary == 0 || primary != secondary || primary > maximum {
		return RentEvidence{}, errors.New("token-account rent is outside the pinned profile")
	}
	return RentEvidence{
		Lamports: primary, PrimaryLamports: primary, SecondaryLamports: secondary,
	}, nil
}

func (l *Lifecycle) VerifyWhirlpoolDeployment(
	ctx context.Context,
	policy orcaswap.Policy,
	minContextSlot uint64,
) error {
	if policy.Validate() != nil {
		return errors.New("Whirlpool deployment policy is invalid")
	}
	return l.verifyWhirlpoolDeployment(
		ctx, policy.ProgramData, policy.UpgradeAuthority, policy.DeploymentSlot, minContextSlot,
	)
}

func (l *Lifecycle) VerifyWhirlpoolBuyDeployment(
	ctx context.Context,
	policy orcaswap.BuyPolicyV2,
	minContextSlot uint64,
) error {
	if policy.Validate() != nil {
		return errors.New("Whirlpool buy deployment policy is invalid")
	}
	return l.verifyWhirlpoolDeployment(
		ctx, policy.ProgramData, policy.UpgradeAuthority, policy.DeploymentSlot, minContextSlot,
	)
}

func (l *Lifecycle) verifyWhirlpoolDeployment(
	ctx context.Context,
	programData,
	upgradeAuthority string,
	deploymentSlot,
	minContextSlot uint64,
) error {
	if minContextSlot == 0 {
		return errors.New("minimum deployment context slot is required")
	}
	programA, programB, err := queryPair(
		ctx,
		func(ctx context.Context) (solanarpc.AccountDataSlice, error) {
			return l.primary.AccountSlice(ctx, orcaswap.WhirlpoolProgram, minContextSlot, 0, 36)
		},
		func(ctx context.Context) (solanarpc.AccountDataSlice, error) {
			return l.secondary.AccountSlice(ctx, orcaswap.WhirlpoolProgram, minContextSlot, 0, 36)
		},
	)
	if err != nil {
		return errors.New("query independent Whirlpool program evidence")
	}
	if !accountSlicesEqual(programA, programB, minContextSlot, 36) ||
		programA.Owner != orcaswap.UpgradeableLoader || !programA.Executable ||
		binary.LittleEndian.Uint32(programA.Data[:4]) != 2 ||
		solana.Encode(programA.Data[4:]) != programData {
		return errors.New("Whirlpool program deployment does not match the pinned profile")
	}
	dataA, dataB, err := queryPair(
		ctx,
		func(ctx context.Context) (solanarpc.AccountDataSlice, error) {
			return l.primary.AccountSlice(ctx, programData, minContextSlot, 0, 45)
		},
		func(ctx context.Context) (solanarpc.AccountDataSlice, error) {
			return l.secondary.AccountSlice(ctx, programData, minContextSlot, 0, 45)
		},
	)
	if err != nil {
		return errors.New("query independent Whirlpool program-data evidence")
	}
	if !accountSlicesEqual(dataA, dataB, minContextSlot, 45) ||
		dataA.Owner != orcaswap.UpgradeableLoader || dataA.Executable ||
		binary.LittleEndian.Uint32(dataA.Data[:4]) != 3 ||
		binary.LittleEndian.Uint64(dataA.Data[4:12]) != deploymentSlot ||
		dataA.Data[12] != 1 || solana.Encode(dataA.Data[13:45]) != upgradeAuthority {
		return errors.New("Whirlpool program-data deployment does not match the pinned profile")
	}
	return nil
}

func accountSlicesEqual(left, right solanarpc.AccountDataSlice, minContextSlot uint64, length int) bool {
	return left.ContextSlot >= minContextSlot && right.ContextSlot >= minContextSlot &&
		left.Owner == right.Owner && left.Executable == right.Executable &&
		len(left.Data) == length && len(right.Data) == length &&
		bytes.Equal(left.Data, right.Data)
}

func (l *Lifecycle) VerifyGenesis(ctx context.Context, expected string) error {
	if err := solana.ValidateBase58(expected, 64); err != nil {
		return errors.New("expected genesis hash is invalid")
	}
	nodeHash, err := l.node.GenesisHash(ctx)
	if err != nil {
		return errors.New("Mithril node RPC is unavailable or not ready")
	}
	if nodeHash != expected {
		return errors.New("RPC provider genesis hash does not match the configured cluster")
	}
	return l.VerifyEvidenceGenesis(ctx, expected)
}

// VerifyEvidenceGenesis checks the two independent providers without requiring
// the local node. It is used only to reconcile a transaction after a durable
// send marker already exists.
func (l *Lifecycle) VerifyEvidenceGenesis(ctx context.Context, expected string) error {
	if err := solana.ValidateBase58(expected, 64); err != nil {
		return errors.New("expected genesis hash is invalid")
	}
	a, b, err := queryPair(
		ctx,
		l.primary.GenesisHash,
		l.secondary.GenesisHash,
	)
	if err != nil {
		return errors.New("query independent genesis hashes")
	}
	if a != expected || b != expected {
		return errors.New("RPC provider genesis hash does not match the configured cluster")
	}
	return nil
}

type AccountEvidence struct {
	Address              string `json:"address"`
	PrimaryContextSlot   uint64 `json:"primary_context_slot"`
	PrimaryLamports      uint64 `json:"primary_lamports"`
	PrimaryOwner         string `json:"primary_owner"`
	PrimaryExecutable    bool   `json:"primary_executable"`
	PrimaryDataLength    uint64 `json:"primary_data_length"`
	SecondaryContextSlot uint64 `json:"secondary_context_slot"`
	SecondaryLamports    uint64 `json:"secondary_lamports"`
	SecondaryOwner       string `json:"secondary_owner"`
	SecondaryExecutable  bool   `json:"secondary_executable"`
	SecondaryDataLength  uint64 `json:"secondary_data_length"`
}

type TransferAccountEvidence struct {
	ObservationSlot        uint64          `json:"observation_slot"`
	CommonFinalizedFloor   uint64          `json:"common_finalized_floor"`
	PrimaryFinalizedSlot   uint64          `json:"primary_finalized_slot"`
	SecondaryFinalizedSlot uint64          `json:"secondary_finalized_slot"`
	Source                 AccountEvidence `json:"source"`
	Destination            AccountEvidence `json:"destination"`
}

func (l *Lifecycle) AccountsForTransfer(
	ctx context.Context,
	source,
	destination string,
	observationSlot uint64,
) (TransferAccountEvidence, error) {
	if _, err := solana.Decode32(source); err != nil {
		return TransferAccountEvidence{}, errors.New("source address is invalid")
	}
	if _, err := solana.Decode32(destination); err != nil {
		return TransferAccountEvidence{}, errors.New("destination address is invalid")
	}
	if source == destination {
		return TransferAccountEvidence{}, errors.New("source and destination must differ")
	}
	if observationSlot == 0 {
		return TransferAccountEvidence{}, errors.New("Mithril observation slot is required")
	}
	primaryFinalized, secondaryFinalized, err := queryPair(
		ctx,
		l.primary.FinalizedSlot,
		l.secondary.FinalizedSlot,
	)
	if err != nil {
		return TransferAccountEvidence{}, errors.New("query independent finalized slots")
	}
	if primaryFinalized == 0 || secondaryFinalized == 0 {
		return TransferAccountEvidence{}, errors.New("independent finalized slot is zero")
	}
	commonFloor := primaryFinalized
	if secondaryFinalized < commonFloor {
		commonFloor = secondaryFinalized
	}
	sourceEvidence, err := l.accountEvidence(ctx, source, commonFloor)
	if err != nil {
		return TransferAccountEvidence{}, fmt.Errorf("source: %w", err)
	}
	destinationEvidence, err := l.accountEvidence(ctx, destination, commonFloor)
	if err != nil {
		return TransferAccountEvidence{}, fmt.Errorf("destination: %w", err)
	}
	return TransferAccountEvidence{
		ObservationSlot:        observationSlot,
		CommonFinalizedFloor:   commonFloor,
		PrimaryFinalizedSlot:   primaryFinalized,
		SecondaryFinalizedSlot: secondaryFinalized,
		Source:                 sourceEvidence,
		Destination:            destinationEvidence,
	}, nil
}

func (l *Lifecycle) accountEvidence(
	ctx context.Context,
	address string,
	commonFloor uint64,
) (AccountEvidence, error) {
	a, b, err := queryPair(
		ctx,
		func(ctx context.Context) (solanarpc.AccountQuote, error) {
			return l.primary.Account(ctx, address, commonFloor)
		},
		func(ctx context.Context) (solanarpc.AccountQuote, error) {
			return l.secondary.Account(ctx, address, commonFloor)
		},
	)
	if err != nil {
		return AccountEvidence{}, errors.New("query independent account information")
	}
	if a.ContextSlot < commonFloor || b.ContextSlot < commonFloor {
		return AccountEvidence{}, errors.New("independent account context is stale")
	}
	systemProgram := solana.Encode(make([]byte, 32))
	if a.Lamports != b.Lamports || a.Owner != b.Owner ||
		a.Executable != b.Executable || a.DataLength != b.DataLength {
		return AccountEvidence{}, errors.New("RPC providers disagree on account information")
	}
	if a.Owner != systemProgram || a.Executable || a.DataLength != 0 {
		return AccountEvidence{}, errors.New("account is not a plain System account")
	}
	return AccountEvidence{
		Address:              address,
		PrimaryContextSlot:   a.ContextSlot,
		PrimaryLamports:      a.Lamports,
		PrimaryOwner:         a.Owner,
		PrimaryExecutable:    a.Executable,
		PrimaryDataLength:    a.DataLength,
		SecondaryContextSlot: b.ContextSlot,
		SecondaryLamports:    b.Lamports,
		SecondaryOwner:       b.Owner,
		SecondaryExecutable:  b.Executable,
		SecondaryDataLength:  b.DataLength,
	}, nil
}

type FeeEvidence struct {
	Lamports             uint64
	MinContextSlot       uint64
	PrimaryContextSlot   uint64
	SecondaryContextSlot uint64
}

func (l *Lifecycle) FeeForMessage(ctx context.Context, message []byte, minContextSlot uint64) (FeeEvidence, error) {
	if minContextSlot == 0 {
		return FeeEvidence{}, errors.New("minimum fee context slot is required")
	}
	a, b, err := queryPair(
		ctx,
		func(ctx context.Context) (solanarpc.FeeQuote, error) {
			return l.primary.FeeForMessage(ctx, bytes.Clone(message), minContextSlot)
		},
		func(ctx context.Context) (solanarpc.FeeQuote, error) {
			return l.secondary.FeeForMessage(ctx, bytes.Clone(message), minContextSlot)
		},
	)
	if err != nil {
		return FeeEvidence{}, errors.New("query independent message fees")
	}
	if a.ContextSlot < minContextSlot || b.ContextSlot < minContextSlot ||
		a.Lamports == 0 || a.Lamports != b.Lamports {
		return FeeEvidence{}, errors.New("RPC providers disagree on message fee")
	}
	return FeeEvidence{
		Lamports:             a.Lamports,
		MinContextSlot:       minContextSlot,
		PrimaryContextSlot:   a.ContextSlot,
		SecondaryContextSlot: b.ContextSlot,
	}, nil
}

type SimulationEvidence struct {
	ProviderIdentity        string `json:"provider_identity"`
	MinContextSlot          uint64 `json:"min_context_slot"`
	ContextSlot             uint64 `json:"context_slot"`
	UnitsConsumed           uint64 `json:"units_consumed,omitempty"`
	SourcePostLamports      uint64 `json:"source_post_lamports"`
	DestinationPostLamports uint64 `json:"destination_post_lamports"`
	LogsSHA256              string `json:"logs_sha256"`
	AccountsSHA256          string `json:"accounts_sha256"`
}

type LegacySimulationEvidence struct {
	ProviderIdentity string `json:"provider_identity"`
	MinContextSlot   uint64 `json:"min_context_slot"`
	ContextSlot      uint64 `json:"context_slot"`
	UnitsConsumed    uint64 `json:"units_consumed,omitempty"`
	LogsSHA256       string `json:"logs_sha256"`
}

func (l *Lifecycle) Simulate(ctx context.Context, message []byte, minContextSlot uint64) (SimulationEvidence, error) {
	if minContextSlot == 0 {
		return SimulationEvidence{}, errors.New("minimum simulation context slot is required")
	}
	simulation, err := l.node.SimulateTransfer(ctx, bytes.Clone(message), minContextSlot)
	if err != nil {
		return SimulationEvidence{}, err
	}
	if simulation.ContextSlot < minContextSlot {
		return SimulationEvidence{}, errors.New("transaction simulation context is stale")
	}
	if simulation.DestinationPostLamports == 0 ||
		!validSHA256(simulation.LogsSHA256) ||
		!validSHA256(simulation.AccountsSHA256) {
		return SimulationEvidence{}, errors.New("transaction simulation evidence is incomplete")
	}
	return SimulationEvidence{
		ProviderIdentity:        l.node.Identity(),
		MinContextSlot:          minContextSlot,
		ContextSlot:             simulation.ContextSlot,
		UnitsConsumed:           simulation.UnitsConsumed,
		SourcePostLamports:      simulation.SourcePostLamports,
		DestinationPostLamports: simulation.DestinationPostLamports,
		LogsSHA256:              simulation.LogsSHA256,
		AccountsSHA256:          simulation.AccountsSHA256,
	}, nil
}

func (l *Lifecycle) SimulateLegacy(
	ctx context.Context,
	message []byte,
	minContextSlot uint64,
) (LegacySimulationEvidence, error) {
	if minContextSlot == 0 {
		return LegacySimulationEvidence{}, errors.New("minimum simulation context slot is required")
	}
	simulator, ok := l.node.(interface {
		SimulateLegacy(context.Context, []byte, uint64) (solanarpc.LegacySimulation, error)
	})
	if !ok {
		return LegacySimulationEvidence{}, errors.New("Mithril RPC does not support legacy simulation")
	}
	simulation, err := simulator.SimulateLegacy(ctx, bytes.Clone(message), minContextSlot)
	if err != nil {
		return LegacySimulationEvidence{}, err
	}
	if simulation.ContextSlot < minContextSlot || !validSHA256(simulation.LogsSHA256) {
		return LegacySimulationEvidence{}, errors.New("legacy transaction simulation evidence is incomplete")
	}
	return LegacySimulationEvidence{
		ProviderIdentity: l.node.Identity(),
		MinContextSlot:   minContextSlot,
		ContextSlot:      simulation.ContextSlot,
		UnitsConsumed:    simulation.UnitsConsumed,
		LogsSHA256:       simulation.LogsSHA256,
	}, nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size &&
		hex.EncodeToString(decoded) == value &&
		!bytes.Equal(decoded, make([]byte, sha256.Size))
}

type Lifecycle struct {
	node      NodeProvider
	primary   EvidenceProvider
	secondary EvidenceProvider
}

type Submission struct {
	Signature            string `json:"signature"`
	LastValidBlockHeight uint64 `json:"last_valid_block_height"`
	State                string `json:"state"`
}

type ExpectedTransaction struct {
	Signature         string `json:"signature"`
	TransactionSHA256 string `json:"transaction_sha256"`
	Source            string `json:"source"`
	Destination       string `json:"destination"`
	AmountLamports    uint64 `json:"amount_lamports"`
}

type Reconciliation struct {
	Signature                 string              `json:"signature"`
	Verdict                   string              `json:"verdict"`
	Slot                      uint64              `json:"slot,omitempty"`
	PrimaryFound              bool                `json:"primary_found"`
	SecondaryFound            bool                `json:"secondary_found"`
	PrimarySlot               uint64              `json:"primary_slot,omitempty"`
	SecondarySlot             uint64              `json:"secondary_slot,omitempty"`
	PrimaryStatus             string              `json:"primary_status,omitempty"`
	SecondaryStatus           string              `json:"secondary_status,omitempty"`
	PrimaryFailed             bool                `json:"primary_failed"`
	SecondaryFailed           bool                `json:"secondary_failed"`
	PrimaryErrorFingerprint   string              `json:"primary_error_fingerprint,omitempty"`
	SecondaryErrorFingerprint string              `json:"secondary_error_fingerprint,omitempty"`
	PrimaryBlockHeight        uint64              `json:"primary_block_height,omitempty"`
	SecondaryBlockHeight      uint64              `json:"secondary_block_height,omitempty"`
	DivergenceKind            string              `json:"divergence_kind,omitempty"`
	Effects                   *EffectEvidence     `json:"effects,omitempty"`
	SwapEffects               *SwapEffectEvidence `json:"swap_effects,omitempty"`
	BuyEffects                *BuyEffectEvidence  `json:"buy_effects,omitempty"`
}

type EffectEvidence struct {
	TransactionSHA256       string `json:"transaction_sha256"`
	FeeLamports             uint64 `json:"fee_lamports"`
	SourcePreLamports       uint64 `json:"source_pre_lamports"`
	SourcePostLamports      uint64 `json:"source_post_lamports"`
	DestinationPreLamports  uint64 `json:"destination_pre_lamports"`
	DestinationPostLamports uint64 `json:"destination_post_lamports"`
	PrimaryEffectSlot       uint64 `json:"primary_effect_slot"`
	SecondaryEffectSlot     uint64 `json:"secondary_effect_slot"`
}

type ExpectedSwap struct {
	Signature         string          `json:"signature"`
	TransactionSHA256 string          `json:"transaction_sha256"`
	Policy            orcaswap.Policy `json:"policy"`
	InputAmount       uint64          `json:"input_amount"`
	MinimumOutput     uint64          `json:"minimum_output"`
}

type SwapEffectEvidence struct {
	TransactionSHA256   string `json:"transaction_sha256"`
	FeeLamports         uint64 `json:"fee_lamports"`
	InputAmount         uint64 `json:"input_amount"`
	MinimumOutput       uint64 `json:"minimum_output"`
	OutputAmount        uint64 `json:"output_amount"`
	PrimaryEffectSlot   uint64 `json:"primary_effect_slot"`
	SecondaryEffectSlot uint64 `json:"secondary_effect_slot"`
}

type ExpectedBuy struct {
	Signature         string               `json:"signature"`
	TransactionSHA256 string               `json:"transaction_sha256"`
	Policy            orcaswap.BuyPolicyV2 `json:"policy"`
	InputAmount       uint64               `json:"input_amount"`
	MinimumOutput     uint64               `json:"minimum_output_lamports"`
}

type BuyEffectEvidence struct {
	TransactionSHA256   string `json:"transaction_sha256"`
	FeeLamports         uint64 `json:"fee_lamports"`
	InputAmount         uint64 `json:"input_amount"`
	MinimumOutput       uint64 `json:"minimum_output_lamports"`
	OutputLamports      uint64 `json:"output_lamports"`
	PrimaryEffectSlot   uint64 `json:"primary_effect_slot"`
	SecondaryEffectSlot uint64 `json:"secondary_effect_slot"`
}

func New(
	node NodeProvider,
	primary,
	secondary EvidenceProvider,
) (*Lifecycle, error) {
	if node == nil || primary == nil || secondary == nil {
		return nil, errors.New("Mithril RPC and two evidence providers are required")
	}
	nodeIdentity := node.Identity()
	primaryIdentity := primary.Identity()
	secondaryIdentity := secondary.Identity()
	if nodeIdentity == "" || primaryIdentity == "" || secondaryIdentity == "" ||
		nodeIdentity == primaryIdentity || nodeIdentity == secondaryIdentity ||
		primaryIdentity == secondaryIdentity {
		return nil, errors.New("Mithril RPC and evidence providers must have distinct identities")
	}
	return &Lifecycle{node: node, primary: primary, secondary: secondary}, nil
}

func (l *Lifecycle) Submit(
	ctx context.Context,
	transaction []byte,
	lastValidBlockHeight,
	minContextSlot uint64,
) (Submission, error) {
	if lastValidBlockHeight == 0 || minContextSlot == 0 {
		return Submission{}, errors.New("last valid block height and minimum context slot are required")
	}
	decoded, err := solana.DecodeSignedTransfer(transaction)
	if err != nil {
		return Submission{}, fmt.Errorf("decode transaction: %w", err)
	}
	expected := solana.Encode(decoded.Signature[:])
	returned, err := l.node.SendTransaction(ctx, bytes.Clone(transaction), minContextSlot)
	if err != nil {
		return Submission{
			Signature:            expected,
			LastValidBlockHeight: lastValidBlockHeight,
			State:                StateAmbiguous,
		}, nil
	}
	if returned != expected {
		return Submission{}, errors.New("RPC returned a signature different from the signed transaction")
	}
	return Submission{
		Signature:            expected,
		LastValidBlockHeight: lastValidBlockHeight,
		State:                StateAccepted,
	}, nil
}

func (l *Lifecycle) BlockhashExpired(ctx context.Context, lastValidBlockHeight uint64) (bool, error) {
	if lastValidBlockHeight == 0 {
		return false, errors.New("last valid block height is required")
	}
	height, err := l.node.BlockHeight(ctx)
	if err != nil {
		return false, err
	}
	return height > lastValidBlockHeight, nil
}

func (l *Lifecycle) Reconcile(
	ctx context.Context,
	submission Submission,
	transaction []byte,
	expectedFeeLamports uint64,
) (Reconciliation, error) {
	expected, err := expectedTransaction(transaction)
	if err != nil {
		return Reconciliation{}, err
	}
	return l.ReconcileExpected(ctx, submission, expected, expectedFeeLamports)
}

func (l *Lifecycle) ReconcileExpected(
	ctx context.Context,
	submission Submission,
	expected ExpectedTransaction,
	expectedFeeLamports uint64,
) (Reconciliation, error) {
	if submission.State != StateAccepted && submission.State != StateAmbiguous {
		return Reconciliation{}, errors.New("submission state is invalid")
	}
	if submission.LastValidBlockHeight == 0 {
		return Reconciliation{}, errors.New("last valid block height is required")
	}
	if _, err := solana.Decode64(submission.Signature); err != nil {
		return Reconciliation{}, errors.New("submission signature is invalid")
	}
	if err := validateExpectedTransaction(expected); err != nil ||
		expected.Signature != submission.Signature {
		return Reconciliation{}, errors.New("expected transaction does not match the submission")
	}
	if expectedFeeLamports == 0 {
		return Reconciliation{}, errors.New("expected transaction fee is required")
	}
	primary, secondary, err := queryStatuses(ctx, l.primary, l.secondary, submission.Signature)
	if err != nil {
		return Reconciliation{}, err
	}
	result := Reconciliation{
		Signature:                 submission.Signature,
		Verdict:                   VerdictPending,
		PrimaryFound:              primary.Found,
		SecondaryFound:            secondary.Found,
		PrimarySlot:               primary.Slot,
		SecondarySlot:             secondary.Slot,
		PrimaryStatus:             primary.ConfirmationStatus,
		SecondaryStatus:           secondary.ConfirmationStatus,
		PrimaryFailed:             primary.Failed,
		SecondaryFailed:           secondary.Failed,
		PrimaryErrorFingerprint:   primary.ErrorFingerprint,
		SecondaryErrorFingerprint: secondary.ErrorFingerprint,
	}
	if primary.Found && secondary.Found {
		if primary.Slot != secondary.Slot ||
			primary.Failed != secondary.Failed ||
			primary.ErrorFingerprint != secondary.ErrorFingerprint {
			result.Verdict = VerdictDiverged
			result.DivergenceKind = DivergenceStatus
			return result, nil
		}
		result.Slot = primary.Slot
		if primary.ConfirmationStatus == "finalized" &&
			secondary.ConfirmationStatus == "finalized" {
			if primary.Failed {
				result.Verdict = VerdictFailed
			} else {
				result.Verdict = VerdictFinalized
			}
			effects, err := l.verifyEffects(
				ctx,
				submission.Signature,
				expected,
				expectedFeeLamports,
				result.Slot,
				result.Verdict == VerdictFailed,
				primary.ErrorFingerprint,
			)
			if err != nil {
				if errors.Is(err, errFinalizedEffectsDiverged) {
					result.Verdict = VerdictDiverged
					result.DivergenceKind = DivergenceEffects
					return result, nil
				}
				return Reconciliation{}, err
			}
			result.Effects = &effects
			return result, nil
		}
	}

	primaryHeight, secondaryHeight, err := queryBlockHeights(ctx, l.primary, l.secondary)
	if err != nil {
		return Reconciliation{}, err
	}
	result.PrimaryBlockHeight = primaryHeight
	result.SecondaryBlockHeight = secondaryHeight
	if primaryHeight > submission.LastValidBlockHeight &&
		secondaryHeight > submission.LastValidBlockHeight {
		if !primary.Found || !secondary.Found {
			result.Verdict = VerdictUnresolved
		}
	}
	return result, nil
}

func (l *Lifecycle) ReconcileSwapExpected(
	ctx context.Context,
	submission Submission,
	expected ExpectedSwap,
	expectedFeeLamports uint64,
) (Reconciliation, error) {
	if submission.State != StateAccepted && submission.State != StateAmbiguous {
		return Reconciliation{}, errors.New("submission state is invalid")
	}
	if submission.LastValidBlockHeight == 0 || expectedFeeLamports == 0 ||
		expected.Signature != submission.Signature || expected.Policy.Validate() != nil ||
		expected.InputAmount == 0 || expected.InputAmount > expected.Policy.MaxInputLamports ||
		expected.MinimumOutput < expected.Policy.MinOutputAmount ||
		!validSHA256(expected.TransactionSHA256) {
		return Reconciliation{}, errors.New("expected swap does not match the submission")
	}
	if _, err := solana.Decode64(expected.Signature); err != nil {
		return Reconciliation{}, errors.New("expected swap signature is invalid")
	}
	primary, secondary, err := queryStatuses(ctx, l.primary, l.secondary, submission.Signature)
	if err != nil {
		return Reconciliation{}, err
	}
	result := Reconciliation{
		Signature:                 submission.Signature,
		Verdict:                   VerdictPending,
		PrimaryFound:              primary.Found,
		SecondaryFound:            secondary.Found,
		PrimarySlot:               primary.Slot,
		SecondarySlot:             secondary.Slot,
		PrimaryStatus:             primary.ConfirmationStatus,
		SecondaryStatus:           secondary.ConfirmationStatus,
		PrimaryFailed:             primary.Failed,
		SecondaryFailed:           secondary.Failed,
		PrimaryErrorFingerprint:   primary.ErrorFingerprint,
		SecondaryErrorFingerprint: secondary.ErrorFingerprint,
	}
	if primary.Found && secondary.Found {
		if primary.Slot != secondary.Slot || primary.Failed != secondary.Failed ||
			primary.ErrorFingerprint != secondary.ErrorFingerprint {
			result.Verdict = VerdictDiverged
			result.DivergenceKind = DivergenceStatus
			return result, nil
		}
		result.Slot = primary.Slot
		if primary.ConfirmationStatus == "finalized" && secondary.ConfirmationStatus == "finalized" {
			if primary.Failed {
				result.Verdict = VerdictFailed
			} else {
				result.Verdict = VerdictFinalized
			}
			effects, err := l.verifySwapEffects(
				ctx,
				submission.Signature,
				expected,
				expectedFeeLamports,
				result.Slot,
				primary.Failed,
				primary.ErrorFingerprint,
			)
			if err != nil {
				if errors.Is(err, errFinalizedEffectsDiverged) {
					result.Verdict = VerdictDiverged
					result.DivergenceKind = DivergenceEffects
					return result, nil
				}
				return Reconciliation{}, err
			}
			result.SwapEffects = &effects
			return result, nil
		}
	}
	primaryHeight, secondaryHeight, err := queryBlockHeights(ctx, l.primary, l.secondary)
	if err != nil {
		return Reconciliation{}, err
	}
	result.PrimaryBlockHeight = primaryHeight
	result.SecondaryBlockHeight = secondaryHeight
	if primaryHeight > submission.LastValidBlockHeight &&
		secondaryHeight > submission.LastValidBlockHeight &&
		(!primary.Found || !secondary.Found) {
		result.Verdict = VerdictUnresolved
	}
	return result, nil
}

func (l *Lifecycle) ReconcileBuyExpected(
	ctx context.Context,
	submission Submission,
	expected ExpectedBuy,
	expectedFeeLamports uint64,
) (Reconciliation, error) {
	if submission.State != StateAccepted && submission.State != StateAmbiguous {
		return Reconciliation{}, errors.New("submission state is invalid")
	}
	if submission.LastValidBlockHeight == 0 || expectedFeeLamports == 0 ||
		expected.Signature != submission.Signature || expected.Policy.Validate() != nil ||
		expected.InputAmount == 0 || expected.InputAmount > expected.Policy.MaxInputTokenAmount ||
		expected.MinimumOutput < expected.Policy.MinOutputLamports ||
		!validSHA256(expected.TransactionSHA256) {
		return Reconciliation{}, errors.New("expected buy does not match the submission")
	}
	if _, err := solana.Decode64(expected.Signature); err != nil {
		return Reconciliation{}, errors.New("expected buy signature is invalid")
	}
	primary, secondary, err := queryStatuses(ctx, l.primary, l.secondary, submission.Signature)
	if err != nil {
		return Reconciliation{}, err
	}
	result := Reconciliation{
		Signature:                 submission.Signature,
		Verdict:                   VerdictPending,
		PrimaryFound:              primary.Found,
		SecondaryFound:            secondary.Found,
		PrimarySlot:               primary.Slot,
		SecondarySlot:             secondary.Slot,
		PrimaryStatus:             primary.ConfirmationStatus,
		SecondaryStatus:           secondary.ConfirmationStatus,
		PrimaryFailed:             primary.Failed,
		SecondaryFailed:           secondary.Failed,
		PrimaryErrorFingerprint:   primary.ErrorFingerprint,
		SecondaryErrorFingerprint: secondary.ErrorFingerprint,
	}
	if primary.Found && secondary.Found {
		if primary.Slot != secondary.Slot || primary.Failed != secondary.Failed ||
			primary.ErrorFingerprint != secondary.ErrorFingerprint {
			result.Verdict = VerdictDiverged
			result.DivergenceKind = DivergenceStatus
			return result, nil
		}
		result.Slot = primary.Slot
		if primary.ConfirmationStatus == "finalized" && secondary.ConfirmationStatus == "finalized" {
			if primary.Failed {
				result.Verdict = VerdictFailed
			} else {
				result.Verdict = VerdictFinalized
			}
			effects, err := l.verifyBuyEffects(
				ctx, submission.Signature, expected, expectedFeeLamports,
				result.Slot, primary.Failed, primary.ErrorFingerprint,
			)
			if err != nil {
				if errors.Is(err, errFinalizedEffectsDiverged) {
					result.Verdict = VerdictDiverged
					result.DivergenceKind = DivergenceEffects
					return result, nil
				}
				return Reconciliation{}, err
			}
			result.BuyEffects = &effects
			return result, nil
		}
	}
	primaryHeight, secondaryHeight, err := queryBlockHeights(ctx, l.primary, l.secondary)
	if err != nil {
		return Reconciliation{}, err
	}
	result.PrimaryBlockHeight = primaryHeight
	result.SecondaryBlockHeight = secondaryHeight
	if primaryHeight > submission.LastValidBlockHeight &&
		secondaryHeight > submission.LastValidBlockHeight &&
		(!primary.Found || !secondary.Found) {
		result.Verdict = VerdictUnresolved
	}
	return result, nil
}

func (l *Lifecycle) verifyBuyEffects(
	ctx context.Context,
	signature string,
	expected ExpectedBuy,
	expectedFeeLamports uint64,
	finalizedSlot uint64,
	failed bool,
	errorFingerprint string,
) (BuyEffectEvidence, error) {
	a, b, err := queryPair(
		ctx,
		func(ctx context.Context) (solanarpc.TransactionEffect, error) {
			return l.primary.TransactionEffect(ctx, signature)
		},
		func(ctx context.Context) (solanarpc.TransactionEffect, error) {
			return l.secondary.TransactionEffect(ctx, signature)
		},
	)
	if err != nil {
		return BuyEffectEvidence{}, errors.New("query independent finalized buy effects")
	}
	if a.Slot != finalizedSlot || b.Slot != finalizedSlot ||
		a.Failed != failed || b.Failed != failed ||
		a.ErrorFingerprint != errorFingerprint || b.ErrorFingerprint != errorFingerprint ||
		a.FeeLamports != expectedFeeLamports || b.FeeLamports != expectedFeeLamports ||
		!bytes.Equal(a.Transaction, b.Transaction) ||
		!slices.Equal(a.PreBalances, b.PreBalances) ||
		!slices.Equal(a.PostBalances, b.PostBalances) ||
		!tokenBalancesEqual(a.PreTokenBalances, b.PreTokenBalances) ||
		!tokenBalancesEqual(a.PostTokenBalances, b.PostTokenBalances) {
		return BuyEffectEvidence{}, errFinalizedEffectsDiverged
	}
	decoded, err := solana.DecodeSignedLegacyTransaction(a.Transaction)
	if err != nil || solana.Encode(decoded.Signature[:]) != expected.Signature {
		return BuyEffectEvidence{}, errFinalizedEffectsDiverged
	}
	intent, err := orcaswap.DecodeBuyMessageV2(expected.Policy, decoded.Message.Raw)
	if err != nil || intent.InputAmount != expected.InputAmount ||
		intent.MinimumOutputLamports != expected.MinimumOutput {
		return BuyEffectEvidence{}, errFinalizedEffectsDiverged
	}
	transactionHash := sha256.Sum256(a.Transaction)
	if hex.EncodeToString(transactionHash[:]) != expected.TransactionSHA256 {
		return BuyEffectEvidence{}, errFinalizedEffectsDiverged
	}
	if len(a.PreBalances) != len(a.PostBalances) ||
		len(a.PreBalances) != len(decoded.Message.AccountKeys) ||
		!tokenBalanceIndexesValid(a.PreTokenBalances, len(decoded.Message.AccountKeys)) ||
		!tokenBalanceIndexesValid(a.PostTokenBalances, len(decoded.Message.AccountKeys)) {
		return BuyEffectEvidence{}, errFinalizedEffectsDiverged
	}
	if failed {
		if a.PreBalances[0] < a.PostBalances[0] ||
			a.PreBalances[0]-a.PostBalances[0] != expectedFeeLamports ||
			!tokenBalancesEqual(a.PreTokenBalances, a.PostTokenBalances) {
			return BuyEffectEvidence{}, errFinalizedEffectsDiverged
		}
		for index := 1; index < len(a.PreBalances); index++ {
			if a.PreBalances[index] != a.PostBalances[index] {
				return BuyEffectEvidence{}, errFinalizedEffectsDiverged
			}
		}
		return BuyEffectEvidence{
			TransactionSHA256: expected.TransactionSHA256, FeeLamports: expectedFeeLamports,
			InputAmount: expected.InputAmount, MinimumOutput: expected.MinimumOutput,
			PrimaryEffectSlot: a.Slot, SecondaryEffectSlot: b.Slot,
		}, nil
	}
	inputIndex := messageAccountIndex(decoded.Message.AccountKeys, expected.Policy.InputTokenAccount)
	temporaryIndex := messageAccountIndex(decoded.Message.AccountKeys, intent.TemporaryWSOLAccount)
	if inputIndex <= 0 || temporaryIndex <= 0 || inputIndex == temporaryIndex ||
		inputIndex > int(^uint16(0)) || temporaryIndex > int(^uint16(0)) {
		return BuyEffectEvidence{}, errFinalizedEffectsDiverged
	}
	if a.PreBalances[inputIndex] == 0 ||
		a.PreBalances[inputIndex] != a.PostBalances[inputIndex] ||
		a.PreBalances[temporaryIndex] != 0 || a.PostBalances[temporaryIndex] != 0 {
		return BuyEffectEvidence{}, errFinalizedEffectsDiverged
	}
	preInput, preFound, err := tokenAmount(
		a.PreTokenBalances, uint16(inputIndex), expected.Policy.TokenMintB, expected.Policy.Owner,
	)
	if err != nil || !preFound {
		return BuyEffectEvidence{}, errFinalizedEffectsDiverged
	}
	postInput, postFound, err := tokenAmount(
		a.PostTokenBalances, uint16(inputIndex), expected.Policy.TokenMintB, expected.Policy.Owner,
	)
	if err != nil || !postFound || preInput < postInput ||
		preInput-postInput != expected.InputAmount {
		return BuyEffectEvidence{}, errFinalizedEffectsDiverged
	}
	if hasTokenBalanceAt(a.PreTokenBalances, uint16(temporaryIndex)) ||
		hasTokenBalanceAt(a.PostTokenBalances, uint16(temporaryIndex)) ||
		!ownerTokenBalancesUnchanged(
			a.PreTokenBalances, a.PostTokenBalances, expected.Policy.Owner, uint16(inputIndex),
		) {
		return BuyEffectEvidence{}, errFinalizedEffectsDiverged
	}
	ownerAfterFee, carry := bits.Add64(a.PostBalances[0], expectedFeeLamports, 0)
	if carry != 0 || ownerAfterFee < a.PreBalances[0] {
		return BuyEffectEvidence{}, errFinalizedEffectsDiverged
	}
	outputLamports := ownerAfterFee - a.PreBalances[0]
	if outputLamports < expected.MinimumOutput {
		return BuyEffectEvidence{}, errFinalizedEffectsDiverged
	}
	return BuyEffectEvidence{
		TransactionSHA256: expected.TransactionSHA256, FeeLamports: expectedFeeLamports,
		InputAmount: expected.InputAmount, MinimumOutput: expected.MinimumOutput,
		OutputLamports:    outputLamports,
		PrimaryEffectSlot: a.Slot, SecondaryEffectSlot: b.Slot,
	}, nil
}

func hasTokenBalanceAt(balances []solanarpc.TokenBalance, index uint16) bool {
	for _, balance := range balances {
		if balance.AccountIndex == index {
			return true
		}
	}
	return false
}

func ownerTokenBalancesUnchanged(
	pre,
	post []solanarpc.TokenBalance,
	owner string,
	allowed uint16,
) bool {
	for _, before := range pre {
		if before.Owner != owner || before.AccountIndex == allowed {
			continue
		}
		matched := false
		for _, after := range post {
			if after.AccountIndex == before.AccountIndex && after == before {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, after := range post {
		if after.Owner != owner || after.AccountIndex == allowed {
			continue
		}
		matched := false
		for _, before := range pre {
			if before.AccountIndex == after.AccountIndex && before == after {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func (l *Lifecycle) verifySwapEffects(
	ctx context.Context,
	signature string,
	expected ExpectedSwap,
	expectedFeeLamports uint64,
	finalizedSlot uint64,
	failed bool,
	errorFingerprint string,
) (SwapEffectEvidence, error) {
	a, b, err := queryPair(
		ctx,
		func(ctx context.Context) (solanarpc.TransactionEffect, error) {
			return l.primary.TransactionEffect(ctx, signature)
		},
		func(ctx context.Context) (solanarpc.TransactionEffect, error) {
			return l.secondary.TransactionEffect(ctx, signature)
		},
	)
	if err != nil {
		return SwapEffectEvidence{}, errors.New("query independent finalized swap effects")
	}
	if a.Slot != finalizedSlot || b.Slot != finalizedSlot ||
		a.Failed != failed || b.Failed != failed ||
		a.ErrorFingerprint != errorFingerprint || b.ErrorFingerprint != errorFingerprint ||
		a.FeeLamports != expectedFeeLamports || b.FeeLamports != expectedFeeLamports ||
		!bytes.Equal(a.Transaction, b.Transaction) ||
		!slices.Equal(a.PreBalances, b.PreBalances) ||
		!slices.Equal(a.PostBalances, b.PostBalances) ||
		!tokenBalancesEqual(a.PreTokenBalances, b.PreTokenBalances) ||
		!tokenBalancesEqual(a.PostTokenBalances, b.PostTokenBalances) {
		return SwapEffectEvidence{}, errFinalizedEffectsDiverged
	}
	decoded, err := solana.DecodeSignedLegacyTransaction(a.Transaction)
	if err != nil || solana.Encode(decoded.Signature[:]) != expected.Signature {
		return SwapEffectEvidence{}, errFinalizedEffectsDiverged
	}
	intent, err := orcaswap.DecodeMessage(expected.Policy, decoded.Message.Raw)
	if err != nil || intent.InputAmount != expected.InputAmount ||
		intent.MinimumOutput != expected.MinimumOutput {
		return SwapEffectEvidence{}, errFinalizedEffectsDiverged
	}
	transactionHash := sha256.Sum256(a.Transaction)
	if hex.EncodeToString(transactionHash[:]) != expected.TransactionSHA256 {
		return SwapEffectEvidence{}, errFinalizedEffectsDiverged
	}
	if len(a.PreBalances) != len(a.PostBalances) || len(a.PreBalances) != len(decoded.Message.AccountKeys) {
		return SwapEffectEvidence{}, errFinalizedEffectsDiverged
	}
	if !tokenBalanceIndexesValid(a.PreTokenBalances, len(decoded.Message.AccountKeys)) ||
		!tokenBalanceIndexesValid(a.PostTokenBalances, len(decoded.Message.AccountKeys)) {
		return SwapEffectEvidence{}, errFinalizedEffectsDiverged
	}
	if failed {
		if a.PreBalances[0] < a.PostBalances[0] ||
			a.PreBalances[0]-a.PostBalances[0] != expectedFeeLamports ||
			!tokenBalancesEqual(a.PreTokenBalances, a.PostTokenBalances) {
			return SwapEffectEvidence{}, errFinalizedEffectsDiverged
		}
		for index := 1; index < len(a.PreBalances); index++ {
			if a.PreBalances[index] != a.PostBalances[index] {
				return SwapEffectEvidence{}, errFinalizedEffectsDiverged
			}
		}
		return SwapEffectEvidence{
			TransactionSHA256:   expected.TransactionSHA256,
			FeeLamports:         expectedFeeLamports,
			InputAmount:         expected.InputAmount,
			MinimumOutput:       expected.MinimumOutput,
			PrimaryEffectSlot:   a.Slot,
			SecondaryEffectSlot: b.Slot,
		}, nil
	}
	inputIndex := messageAccountIndex(decoded.Message.AccountKeys, expected.Policy.InputTokenAccount)
	outputIndex := messageAccountIndex(decoded.Message.AccountKeys, expected.Policy.OutputTokenAccount)
	if inputIndex <= 0 || outputIndex <= 0 || inputIndex == outputIndex ||
		inputIndex > int(^uint16(0)) || outputIndex > int(^uint16(0)) {
		return SwapEffectEvidence{}, errors.New("finalized swap output account is missing")
	}
	if a.PostBalances[inputIndex] != 0 {
		return SwapEffectEvidence{}, errFinalizedEffectsDiverged
	}
	preAmount, preFound, err := tokenAmount(
		a.PreTokenBalances, uint16(outputIndex), expected.Policy.OutputMint, expected.Policy.Owner,
	)
	if err != nil {
		return SwapEffectEvidence{}, errFinalizedEffectsDiverged
	}
	postAmount, postFound, err := tokenAmount(
		a.PostTokenBalances, uint16(outputIndex), expected.Policy.OutputMint, expected.Policy.Owner,
	)
	if err != nil || !postFound || postAmount < preAmount ||
		postAmount-preAmount < expected.MinimumOutput {
		return SwapEffectEvidence{}, errFinalizedEffectsDiverged
	}
	outputRent := uint64(0)
	if intent.OutputAccountCreated {
		if a.PreBalances[outputIndex] == 0 {
			if preFound || a.PostBalances[outputIndex] == 0 ||
				a.PostBalances[outputIndex] > expected.Policy.MaxOutputAccountRentLamports {
				return SwapEffectEvidence{}, errFinalizedEffectsDiverged
			}
			outputRent = a.PostBalances[outputIndex]
		} else if !preFound || a.PreBalances[outputIndex] != a.PostBalances[outputIndex] {
			return SwapEffectEvidence{}, errFinalizedEffectsDiverged
		}
	} else if !preFound || a.PreBalances[outputIndex] != a.PostBalances[outputIndex] {
		return SwapEffectEvidence{}, errFinalizedEffectsDiverged
	}
	// The input WSOL account is closed into the owner. It may already contain
	// rent before execution, so compare the complete native-lamport equation
	// instead of assuming the owner's wallet only decreases.
	ownerFunds, carry := bits.Add64(a.PreBalances[0], a.PreBalances[inputIndex], 0)
	ownerCosts, carryCosts := bits.Add64(a.PostBalances[0], expectedFeeLamports, 0)
	ownerCosts, carryInput := bits.Add64(ownerCosts, expected.InputAmount, 0)
	ownerCosts, carryRent := bits.Add64(ownerCosts, outputRent, 0)
	if carry != 0 || carryCosts != 0 || carryInput != 0 || carryRent != 0 ||
		ownerFunds != ownerCosts {
		return SwapEffectEvidence{}, errFinalizedEffectsDiverged
	}
	return SwapEffectEvidence{
		TransactionSHA256:   expected.TransactionSHA256,
		FeeLamports:         expectedFeeLamports,
		InputAmount:         expected.InputAmount,
		MinimumOutput:       expected.MinimumOutput,
		OutputAmount:        postAmount - preAmount,
		PrimaryEffectSlot:   a.Slot,
		SecondaryEffectSlot: b.Slot,
	}, nil
}

func messageAccountIndex(keys [][32]byte, address string) int {
	for index, key := range keys {
		if solana.Encode(key[:]) == address {
			return index
		}
	}
	return -1
}

func tokenBalancesEqual(left, right []solanarpc.TokenBalance) bool {
	if len(left) != len(right) {
		return false
	}
	byIndex := make(map[uint16]solanarpc.TokenBalance, len(left))
	for _, balance := range left {
		if _, exists := byIndex[balance.AccountIndex]; exists {
			return false
		}
		byIndex[balance.AccountIndex] = balance
	}
	for _, balance := range right {
		if expected, ok := byIndex[balance.AccountIndex]; !ok || expected != balance {
			return false
		}
		delete(byIndex, balance.AccountIndex)
	}
	return len(byIndex) == 0
}

func tokenBalanceIndexesValid(balances []solanarpc.TokenBalance, accountCount int) bool {
	for _, balance := range balances {
		if int(balance.AccountIndex) >= accountCount {
			return false
		}
	}
	return true
}

func tokenAmount(
	balances []solanarpc.TokenBalance,
	accountIndex uint16,
	mint,
	owner string,
) (uint64, bool, error) {
	for _, balance := range balances {
		if balance.AccountIndex != accountIndex {
			continue
		}
		if balance.Mint != mint || balance.Owner != owner {
			return 0, false, errors.New("token balance identity does not match")
		}
		return balance.Amount, true, nil
	}
	return 0, false, nil
}

func (l *Lifecycle) verifyEffects(
	ctx context.Context,
	signature string,
	expected ExpectedTransaction,
	expectedFeeLamports uint64,
	finalizedSlot uint64,
	failed bool,
	errorFingerprint string,
) (EffectEvidence, error) {
	a, b, err := queryPair(
		ctx,
		func(ctx context.Context) (solanarpc.TransactionEffect, error) {
			return l.primary.TransactionEffect(ctx, signature)
		},
		func(ctx context.Context) (solanarpc.TransactionEffect, error) {
			return l.secondary.TransactionEffect(ctx, signature)
		},
	)
	if err != nil {
		return EffectEvidence{}, errors.New("query independent finalized transaction effects")
	}
	if a.Slot != finalizedSlot || b.Slot != finalizedSlot ||
		a.Failed != failed || b.Failed != failed ||
		a.ErrorFingerprint != errorFingerprint ||
		b.ErrorFingerprint != errorFingerprint ||
		a.FeeLamports != expectedFeeLamports ||
		b.FeeLamports != expectedFeeLamports ||
		!bytes.Equal(a.Transaction, b.Transaction) ||
		!slices.Equal(a.PreBalances, b.PreBalances) ||
		!slices.Equal(a.PostBalances, b.PostBalances) ||
		len(a.PreBalances) != 3 || len(a.PostBalances) != 3 {
		return EffectEvidence{}, errFinalizedEffectsDiverged
	}
	decoded, err := solana.DecodeSignedTransfer(a.Transaction)
	if err != nil || solana.Encode(decoded.Signature[:]) != expected.Signature ||
		solana.Encode(decoded.Source[:]) != expected.Source ||
		solana.Encode(decoded.Destination[:]) != expected.Destination ||
		decoded.Lamports != expected.AmountLamports {
		return EffectEvidence{}, errFinalizedEffectsDiverged
	}
	transactionHash := sha256.Sum256(a.Transaction)
	if hex.EncodeToString(transactionHash[:]) != expected.TransactionSHA256 {
		return EffectEvidence{}, errFinalizedEffectsDiverged
	}
	sourcePre, destinationPre := a.PreBalances[0], a.PreBalances[1]
	sourcePost, destinationPost := a.PostBalances[0], a.PostBalances[1]
	if a.PreBalances[2] != a.PostBalances[2] {
		return EffectEvidence{}, errFinalizedEffectsDiverged
	}
	sourceDebit := expectedFeeLamports
	destinationCredit := uint64(0)
	if !failed {
		if decoded.Lamports > ^uint64(0)-sourceDebit {
			return EffectEvidence{}, errors.New("finalized transaction debit overflows")
		}
		sourceDebit += decoded.Lamports
		destinationCredit = decoded.Lamports
	}
	if sourcePost > sourcePre || sourcePre-sourcePost != sourceDebit ||
		destinationPost < destinationPre ||
		destinationPost-destinationPre != destinationCredit {
		return EffectEvidence{}, errFinalizedEffectsDiverged
	}
	return EffectEvidence{
		TransactionSHA256:       expected.TransactionSHA256,
		FeeLamports:             expectedFeeLamports,
		SourcePreLamports:       sourcePre,
		SourcePostLamports:      sourcePost,
		DestinationPreLamports:  destinationPre,
		DestinationPostLamports: destinationPost,
		PrimaryEffectSlot:       a.Slot,
		SecondaryEffectSlot:     b.Slot,
	}, nil
}

func expectedTransaction(transaction []byte) (ExpectedTransaction, error) {
	decoded, err := solana.DecodeSignedTransfer(transaction)
	if err != nil {
		return ExpectedTransaction{}, errors.New("signed transaction is invalid")
	}
	digest := sha256.Sum256(transaction)
	return ExpectedTransaction{
		Signature:         solana.Encode(decoded.Signature[:]),
		TransactionSHA256: hex.EncodeToString(digest[:]),
		Source:            solana.Encode(decoded.Source[:]),
		Destination:       solana.Encode(decoded.Destination[:]),
		AmountLamports:    decoded.Lamports,
	}, nil
}

func validateExpectedTransaction(expected ExpectedTransaction) error {
	if _, err := solana.Decode64(expected.Signature); err != nil {
		return errors.New("expected transaction signature is invalid")
	}
	if !validSHA256(expected.TransactionSHA256) {
		return errors.New("expected transaction hash is invalid")
	}
	if _, err := solana.Decode32(expected.Source); err != nil {
		return errors.New("expected transaction source is invalid")
	}
	if _, err := solana.Decode32(expected.Destination); err != nil ||
		expected.Source == expected.Destination || expected.AmountLamports == 0 {
		return errors.New("expected transaction effect is invalid")
	}
	return nil
}

type pairResult[T any] struct {
	index int
	value T
	err   error
}

func queryPair[T any](
	ctx context.Context,
	first,
	second func(context.Context) (T, error),
) (T, T, error) {
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan pairResult[T], 2)
	calls := []func(context.Context) (T, error){first, second}
	for index, call := range calls {
		go func() {
			value, err := call(child)
			results <- pairResult[T]{index: index, value: value, err: err}
		}()
	}
	var values [2]T
	var firstErr error
	for range calls {
		select {
		case <-ctx.Done():
			return values[0], values[1], ctx.Err()
		case result := <-results:
			if result.err != nil {
				if firstErr == nil {
					firstErr = result.err
					cancel()
				}
				continue
			}
			values[result.index] = result.value
		}
	}
	return values[0], values[1], firstErr
}

func queryStatuses(
	ctx context.Context,
	first,
	second EvidenceProvider,
	signature string,
) (solanarpc.SignatureStatus, solanarpc.SignatureStatus, error) {
	a, b, err := queryPair(
		ctx,
		func(ctx context.Context) (solanarpc.SignatureStatus, error) {
			return first.SignatureStatus(ctx, signature)
		},
		func(ctx context.Context) (solanarpc.SignatureStatus, error) {
			return second.SignatureStatus(ctx, signature)
		},
	)
	if err != nil {
		return solanarpc.SignatureStatus{}, solanarpc.SignatureStatus{},
			errors.New("query independent signature status")
	}
	return a, b, nil
}

func queryBlockHeights(
	ctx context.Context,
	first,
	second EvidenceProvider,
) (uint64, uint64, error) {
	a, b, err := queryPair(ctx, first.BlockHeight, second.BlockHeight)
	if err != nil {
		return 0, 0, errors.New("query independent block heights")
	}
	if a == 0 || b == 0 {
		return 0, 0, errors.New("independent block height is zero")
	}
	return a, b, nil
}
