package proposalcheck

import (
	"context"
	"errors"

	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

// NativeReserveEvidence adds only a read-only account check to proposal evidence.
type NativeReserveEvidence interface {
	Evidence
	VerifyNativeReserve(context.Context, string, uint64, uint64, uint64, uint64) (txflow.AccountEvidence, error)
}

// NativeReserveReview is an advisory balance observation, not a durable funds
// reservation or execution-readiness verdict. Amounts use lossless JSON strings.
type NativeReserveReview struct {
	AdvisoryOnly            bool   `json:"advisory_only"`
	Owner                   string `json:"owner"`
	RetainedReserveLamports uint64 `json:"retained_reserve_lamports,string"`
	MaximumUpfrontLamports  uint64 `json:"maximum_upfront_lamports,string"`
	ObservedBalanceLamports uint64 `json:"observed_balance_lamports,string"`
	PrimaryContextSlot      uint64 `json:"primary_context_slot"`
	SecondaryContextSlot    uint64 `json:"secondary_context_slot"`
}

// NativeReserveResult retains the ordinary unsigned recheck and its exact
// message/policy/provider identities alongside the optional balance review.
type NativeReserveResult struct {
	Result
	NativeReserve NativeReserveReview `json:"native_reserve"`
}

// RecheckWithNativeReserve repeats the existing exact-proposal checks before
// comparing current independent native balances with the checked upfront cost.
// The reserve is an operator-supplied review bound, not protected spending
// authority. No existing signing, authorization or submission path calls this.
func RecheckWithNativeReserve(
	ctx context.Context,
	evidence NativeReserveEvidence,
	primary, secondary FinalizedSlotReader,
	expectedPolicy jupiterswap.Policy,
	expectedProviders ProviderBindings,
	candidate Candidate,
	reserveLamports uint64,
) (NativeReserveResult, error) {
	if reserveLamports == 0 || evidence == nil {
		return NativeReserveResult{}, errors.New("retained native reserve must be positive")
	}
	checked, err := Recheck(ctx, evidence, primary, secondary, expectedPolicy, expectedProviders, candidate)
	if err != nil {
		return NativeReserveResult{}, err
	}
	if checked.MinimumContextSlot > ^uint64(0)-MaxEvidenceSlotSkew {
		return NativeReserveResult{}, errors.New("native reserve context interval overflows")
	}
	minimumSlot := max(checked.MinimumContextSlot, checked.PrimaryFeeContextSlot,
		checked.SecondaryFeeContextSlot, checked.SimulationContextSlot)
	maximumSlot := checked.MinimumContextSlot + MaxEvidenceSlotSkew
	account, err := evidence.VerifyNativeReserve(ctx, expectedPolicy.Owner,
		checked.MaximumUpfrontLamports, reserveLamports, minimumSlot, maximumSlot)
	if err != nil {
		return NativeReserveResult{}, err
	}
	// Retain the trust-boundary checks even for alternate Evidence implementations.
	if account.Address != expectedPolicy.Owner || account.PrimaryLamports != account.SecondaryLamports ||
		account.PrimaryOwner != "11111111111111111111111111111111" || account.SecondaryOwner != account.PrimaryOwner ||
		account.PrimaryExecutable || account.SecondaryExecutable || account.PrimaryDataLength != 0 || account.SecondaryDataLength != 0 ||
		account.PrimaryContextSlot < minimumSlot || account.SecondaryContextSlot < minimumSlot ||
		account.PrimaryContextSlot > maximumSlot || account.SecondaryContextSlot > maximumSlot ||
		account.PrimaryLamports < reserveLamports || account.PrimaryLamports-reserveLamports < checked.MaximumUpfrontLamports {
		return NativeReserveResult{}, errors.New("native reserve evidence does not match the checked proposal")
	}
	return NativeReserveResult{Result: checked, NativeReserve: NativeReserveReview{
		AdvisoryOnly: true, Owner: expectedPolicy.Owner, RetainedReserveLamports: reserveLamports,
		MaximumUpfrontLamports: checked.MaximumUpfrontLamports, ObservedBalanceLamports: account.PrimaryLamports,
		PrimaryContextSlot: account.PrimaryContextSlot, SecondaryContextSlot: account.SecondaryContextSlot,
	}}, nil
}
