package execution

import (
	"context"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

// Compatibility aliases retain the existing public API and canonical value types.
type WalletReservation = policyauthority.WalletReservation
type WalletStrategyOutcome = policyauthority.WalletStrategyOutcome
type WalletInventory = policyauthority.WalletInventory
type WalletPaperClaim = policyauthority.WalletPaperClaim
type PaperSwapAccounting = policyauthority.PaperSwapAccounting

// RecordPaperTerminal forwards to the authority-owned unsigned journal implementation.
func RecordPaperTerminal(path string, authority policyauthority.Policy, request signer.Request,
	recoveryPolicy submitter.Policy, now time.Time,
) (submitter.JupiterFinalizedEvidence, error) {
	return policyauthority.RecordPaperTerminal(path, authority, request, recoveryPolicy, now)
}

// FinalizeWalletClaim forwards to the authority-owned unsigned journal implementation.
func FinalizeWalletClaim(path, claimPath string, policy policyauthority.Policy, request signer.Request,
	recoveryPolicy submitter.Policy, now time.Time,
) (WalletInventory, error) {
	return policyauthority.FinalizeWalletClaim(path, claimPath, policy, request, recoveryPolicy, now)
}

// ReserveWalletClaim forwards to the authority-owned unsigned journal implementation.
func ReserveWalletClaim(ctx context.Context, path, claimPath string, policy policyauthority.Policy,
	request signer.Request, lifecycle *txflow.Lifecycle, now time.Time,
) (WalletInventory, error) {
	return policyauthority.ReserveWalletClaim(ctx, path, claimPath, policy, request, lifecycle, now)
}

// ApplyFinalizedWalletClaim forwards to the authority-owned unsigned journal implementation.
func ApplyFinalizedWalletClaim(path, claimPath string, policy policyauthority.Policy, request signer.Request,
	recoveryPolicy submitter.Policy, now time.Time,
) (WalletInventory, error) {
	return policyauthority.ApplyFinalizedWalletClaim(path, claimPath, policy, request, recoveryPolicy, now)
}

// ApplyStrategyJournalOutcome forwards to the authority-owned unsigned journal implementation.
func ApplyStrategyJournalOutcome(path string, authority policyauthority.Policy, recoveryPolicy submitter.Policy,
	accountingSHA256 string, deliveryAt time.Time,
	historicalRecoveryPolicies ...submitter.Policy,
) (*shadow.AccountedStrategy, error) {
	return policyauthority.ApplyStrategyJournalOutcome(path, authority, recoveryPolicy, accountingSHA256, deliveryAt, historicalRecoveryPolicies...)
}

// ApplyStrategyContinuationOutcome forwards exact per-decision accounted outcomes.
func ApplyStrategyContinuationOutcome(path string, authority policyauthority.Policy,
	originalRecovery, recoveryPolicy submitter.Policy, decisionSHA, accountingSHA string,
	deliveryAt time.Time, historicalRecoveryPolicies ...submitter.Policy,
) (*shadow.AccountedStrategy, error) {
	return policyauthority.ApplyStrategyContinuationOutcome(path, authority, originalRecovery, recoveryPolicy,
		decisionSHA, accountingSHA, deliveryAt, historicalRecoveryPolicies...)
}

// ReadWalletStrategyOutcome forwards to the authority-owned unsigned journal implementation.
func ReadWalletStrategyOutcome(path, claimPath, accountingSHA256 string, policy policyauthority.Policy,
	request signer.Request, recoveryPolicy submitter.Policy, now time.Time,
) (WalletStrategyOutcome, error) {
	return policyauthority.ReadWalletStrategyOutcome(path, claimPath, accountingSHA256, policy, request, recoveryPolicy, now)
}

// AcquireWalletPaperClaim forwards to the authority-owned unsigned journal implementation.
func AcquireWalletPaperClaim(ctx context.Context, path string, policy policyauthority.Policy,
	paperPolicy shadow.Policy, ticks []shadow.Tick, bounds proposalcheck.PaperIntentBounds,
	request jupiterquote.Request, scheduleWindowStartUnix int64, now time.Time,
	maxDecisionAge, maxAcquisitionAge time.Duration, builder proposalcheck.Builder,
	evidence proposalcheck.NativeReserveEvidence, primary, secondary proposalcheck.FinalizedSlotReader,
	lifecycle *txflow.Lifecycle,
) (WalletPaperClaim, error) {
	return policyauthority.AcquireWalletPaperClaim(ctx, path, policy, paperPolicy, ticks, bounds, request, scheduleWindowStartUnix, now, maxDecisionAge, maxAcquisitionAge, builder, evidence, primary, secondary, lifecycle)
}

// InitializeStrategyJournal forwards to the authority-owned unsigned journal implementation.
func InitializeStrategyJournal(path, walletPath, claimPath string, authority policyauthority.Policy,
	recoveryPolicy submitter.Policy,
	policy shadow.Policy, ticks []shadow.Tick, bounds proposalcheck.PaperIntentBounds, now time.Time,
	historicalRecoveryPolicies ...submitter.Policy,
) (result *shadow.AccountedStrategy, err error) {
	return policyauthority.InitializeStrategyJournal(path, walletPath, claimPath, authority, recoveryPolicy, policy, ticks, bounds, now, historicalRecoveryPolicies...)
}

// ReadStrategyJournal forwards to the authority-owned unsigned journal implementation.
func ReadStrategyJournal(path string, authority policyauthority.Policy, recoveryPolicy submitter.Policy, now time.Time, historicalRecoveryPolicies ...submitter.Policy) (*shadow.AccountedStrategy, error) {
	return policyauthority.ReadStrategyJournal(path, authority, recoveryPolicy, now, historicalRecoveryPolicies...)
}

// ObserveStrategyJournal forwards to the authority-owned unsigned journal implementation.
func ObserveStrategyJournal(path string, authority policyauthority.Policy, recoveryPolicy submitter.Policy, at time.Time,
	primary, secondary, quotePrimary, quoteSecondary pricetrigger.Sample,
	historicalRecoveryPolicies ...submitter.Policy,
) (decision shadow.AccountedDecision, err error) {
	return policyauthority.ObserveStrategyJournal(path, authority, recoveryPolicy, at, primary, secondary, quotePrimary, quoteSecondary, historicalRecoveryPolicies...)
}

// ReadWalletInventory forwards to the authority-owned unsigned journal implementation.
func ReadWalletInventory(path string, policy policyauthority.Policy) (WalletInventory, error) {
	return policyauthority.ReadWalletInventory(path, policy)
}

// InitializeWalletInventory forwards to the authority-owned unsigned journal implementation.
func InitializeWalletInventory(ctx context.Context, path string, policy policyauthority.Policy,
	lifecycle *txflow.Lifecycle, now time.Time,
) (WalletInventory, error) {
	return policyauthority.InitializeWalletInventory(ctx, path, policy, lifecycle, now)
}

// RecoverWalletPaperClaim forwards to the authority-owned unsigned journal implementation.
func RecoverWalletPaperClaim(path string, policy policyauthority.Policy, now time.Time) (WalletPaperClaim, bool, error) {
	return policyauthority.RecoverWalletPaperClaim(path, policy, now)
}

// PrepareWalletPaperClaim forwards to the authority-owned unsigned journal implementation.
func PrepareWalletPaperClaim(ctx context.Context, path string, policy policyauthority.Policy,
	paperPolicy shadow.Policy, ticks []shadow.Tick, bounds proposalcheck.PaperIntentBounds,
	candidate proposalcheck.Candidate, scheduleWindowStartUnix int64, now time.Time,
	maxDecisionAge time.Duration, acquisitionPath string, maxAcquisitionAge time.Duration,
	evidence proposalcheck.NativeReserveEvidence, primary, secondary proposalcheck.FinalizedSlotReader,
	lifecycle *txflow.Lifecycle,
) (WalletPaperClaim, error) {
	return policyauthority.PrepareWalletPaperClaim(ctx, path, policy, paperPolicy, ticks, bounds, candidate, scheduleWindowStartUnix, now, maxDecisionAge, acquisitionPath, maxAcquisitionAge, evidence, primary, secondary, lifecycle)
}

// ReadPaperSwapAccounting forwards to the authority-owned unsigned journal implementation.
func ReadPaperSwapAccounting(path string, authority policyauthority.Policy, request signer.Request,
	recoveryPolicy submitter.Policy,
) (PaperSwapAccounting, error) {
	return policyauthority.ReadPaperSwapAccounting(path, authority, request, recoveryPolicy)
}
