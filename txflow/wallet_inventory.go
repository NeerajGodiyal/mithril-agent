package txflow

import (
	"context"
	"errors"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

// WalletObservation records independent finalized reads of a plain SOL wallet
// and one existing canonical classic-token account. Context slots are retained
// individually: these reads are not an atomic multi-account snapshot.
type WalletObservation struct {
	Owner               string `json:"owner"`
	TokenMint           string `json:"token_mint"`
	TokenAccount        string `json:"token_account"`
	GenesisHash         string `json:"genesis_hash"`
	PrimaryIdentity     string `json:"primary_identity"`
	SecondaryIdentity   string `json:"secondary_identity"`
	NativeLamports      uint64 `json:"native_lamports,string"`
	TokenUnits          uint64 `json:"token_units,string"`
	MinimumContextSlot  uint64 `json:"minimum_context_slot"`
	MaximumContextSlot  uint64 `json:"maximum_context_slot"`
	NativePrimarySlot   uint64 `json:"native_primary_slot"`
	NativeSecondarySlot uint64 `json:"native_secondary_slot"`
	TokenPrimarySlot    uint64 `json:"token_primary_slot"`
	TokenSecondarySlot  uint64 `json:"token_secondary_slot"`
}

// ObserveWallet obtains finalized balances without signing or sending. Missing
// token accounts are errors, never inferred zero holdings. The caller must bind
// provider identities and the owner to protected configuration before using the
// observation, and detect external balance changes when reconciling trades.
func (l *Lifecycle) ObserveWallet(ctx context.Context, owner, mint, genesis string,
	minimumSlot, maximumSlotSkew uint64,
) (WalletObservation, error) {
	if l == nil || l.primary == nil || l.secondary == nil || maximumSlotSkew == 0 ||
		mint == orcaswap.WrappedSOLMint {
		return WalletObservation{}, errors.New("wallet observation inputs are invalid")
	}
	address, err := orcaswap.AssociatedTokenAddress(owner, mint)
	if err != nil {
		return WalletObservation{}, err
	}
	if _, err := solana.Decode32(genesis); err != nil {
		return WalletObservation{}, errors.New("wallet observation genesis is invalid")
	}
	if err := l.VerifyEvidenceGenesis(ctx, genesis); err != nil {
		return WalletObservation{}, err
	}
	a, b, err := queryPair(ctx, l.primary.FinalizedSlot, l.secondary.FinalizedSlot)
	if err != nil || a == 0 || b == 0 || max(a, b)-min(a, b) > maximumSlotSkew {
		return WalletObservation{}, errors.New("wallet finalized contexts are unavailable or divergent")
	}
	floor := max(a, b, minimumSlot)
	if floor > ^uint64(0)-maximumSlotSkew {
		return WalletObservation{}, errors.New("wallet context interval overflows")
	}
	ceiling := floor + maximumSlotSkew
	native, err := l.accountEvidence(ctx, owner, floor)
	if err != nil {
		return WalletObservation{}, err
	}
	token, err := l.verifyTokenAccountAt(ctx, address, mint, owner, 0, floor, true)
	if err != nil {
		return WalletObservation{}, err
	}
	if native.PrimaryContextSlot > ceiling || native.SecondaryContextSlot > ceiling ||
		token.PrimaryContextSlot > ceiling || token.SecondaryContextSlot > ceiling {
		return WalletObservation{}, errors.New("wallet balances exceed the finalized context interval")
	}
	return WalletObservation{
		Owner: owner, TokenMint: mint, TokenAccount: address, GenesisHash: genesis,
		PrimaryIdentity: l.primary.Identity(), SecondaryIdentity: l.secondary.Identity(),
		NativeLamports: native.PrimaryLamports, TokenUnits: token.Amount,
		MinimumContextSlot: floor, MaximumContextSlot: ceiling,
		NativePrimarySlot: native.PrimaryContextSlot, NativeSecondarySlot: native.SecondaryContextSlot,
		TokenPrimarySlot: token.PrimaryContextSlot, TokenSecondarySlot: token.SecondaryContextSlot,
	}, nil
}
