package policyauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const walletOpeningEvent = "wallet.inventory-opening-v1"

func validHexDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size &&
		hex.EncodeToString(decoded) == value &&
		!bytes.Equal(decoded, make([]byte, sha256.Size))
}

// WalletInventory retains the opening and the latest durably accounted balances.
// It is not a fresh balance quotation or permission to sign a transaction.
type WalletInventory struct {
	BindingSHA256     string                   `json:"binding_sha256"`
	HeadSHA256        string                   `json:"head_sha256"`
	OpenedAt          time.Time                `json:"opened_at"`
	Opening           txflow.WalletObservation `json:"opening"`
	NativeLamports    uint64                   `json:"native_lamports,string"`
	TokenUnits        uint64                   `json:"token_units,string"`
	LastFinalizedSlot uint64                   `json:"last_finalized_slot"`
	Pending           WalletReservation        `json:"pending"`
	PendingSHA256     string                   `json:"pending_sha256,omitempty"`
	PendingAt         time.Time                `json:"pending_at,omitempty"`
}

// ReadWalletInventory verifies and folds an existing protected inventory journal.
// It performs no RPC, writes, balance refresh, or authorization.
func ReadWalletInventory(path string, policy Policy) (WalletInventory, error) {
	binding, owner, mint, err := walletInventoryBinding(policy)
	if err != nil {
		return WalletInventory{}, err
	}
	records, err := journal.ReadRecords(path)
	if err != nil {
		return WalletInventory{}, err
	}
	return openingWalletInventory(records, policy, binding, owner, mint)
}

// InitializeWalletInventory creates one protected opening record, or folds the
// existing journal without refreshing its balances. It uses the configured
// independent providers and never resets inventory, signs, sends or releases a claim.
func InitializeWalletInventory(ctx context.Context, path string, policy Policy,
	lifecycle *txflow.Lifecycle, now time.Time,
) (WalletInventory, error) {
	if lifecycle == nil {
		return WalletInventory{}, errors.New("wallet inventory requires independent evidence")
	}
	return initializeWalletInventory(path, policy, now, func(owner, mint string) (txflow.WalletObservation, error) {
		primary, secondary := lifecycle.EvidenceProviderIdentities()
		if primary != policy.JupiterProviders.PrimaryOriginSHA256 || secondary != policy.JupiterProviders.SecondaryOriginSHA256 {
			return txflow.WalletObservation{}, errors.New("wallet inventory providers do not match policy")
		}
		return lifecycle.ObserveWallet(ctx, owner, mint, solana.MainnetBetaGenesisHash, 0, proposalcheck.MaxEvidenceSlotSkew)
	})
}

func initializeWalletInventory(path string, policy Policy, now time.Time,
	observe func(string, string) (txflow.WalletObservation, error),
) (result WalletInventory, err error) {
	started := time.Now()
	binding, owner, mint, err := walletInventoryBinding(policy)
	if err != nil || now.IsZero() {
		return WalletInventory{}, errors.New("wallet inventory policy or time is invalid")
	}
	store, err := journal.OpenStrict(path)
	if err != nil {
		return WalletInventory{}, err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			result = WalletInventory{}
			err = errors.Join(err, closeErr)
		}
	}()
	if records := store.Records(); len(records) != 0 {
		return openingWalletInventory(records, policy, binding, owner, mint)
	}
	observation, err := observe(owner, mint)
	if err != nil {
		return WalletInventory{}, err
	}
	if err := validateWalletObservation(observation, policy, owner, mint); err != nil {
		return WalletInventory{}, err
	}
	if _, err := store.Append(now.Add(time.Since(started)).UTC(), walletOpeningEvent, binding, observation); err != nil {
		return WalletInventory{}, err
	}
	return openingWalletInventory(store.Records(), policy, binding, owner, mint)
}

func walletInventoryBinding(policy Policy) (string, string, string, error) {
	if err := policy.Validate(); err != nil || policy.TransactionPolicy.Jupiter == nil {
		return "", "", "", errors.New("wallet inventory requires a protected Jupiter policy")
	}
	p := policy.TransactionPolicy.Jupiter
	mint := p.InputMint
	if p.NativeInput() {
		mint = p.OutputMint
	}
	// Direction and per-order limits remain bound by each claim and signer policy.
	// The wallet identity must remain stable when trading the same pair in reverse.
	identity := struct {
		Owner     string
		Mint      string
		Genesis   string
		Providers proposalcheck.ProviderBindings
	}{p.Owner, mint, solana.MainnetBetaGenesisHash, *policy.JupiterProviders}
	raw, err := json.Marshal(identity)
	if err != nil {
		return "", "", "", err
	}
	hash := sha256.Sum256(append([]byte(walletOpeningEvent+"\x00"), raw...))
	return hex.EncodeToString(hash[:]), p.Owner, mint, nil
}

func openingWalletInventory(records []journal.Record, policy Policy, binding, owner, mint string) (WalletInventory, error) {
	if len(records) == 0 || records[0].Type != walletOpeningEvent || records[0].ActionID != binding || records[0].At.IsZero() {
		return WalletInventory{}, errors.New("wallet opening journal has conflicting or unsupported records")
	}
	var observation txflow.WalletObservation
	if err := strictjson.Decode(records[0].Payload, &observation); err != nil {
		return WalletInventory{}, err
	}
	if err := validateWalletObservation(observation, policy, owner, mint); err != nil {
		return WalletInventory{}, err
	}
	canonical, err := json.Marshal(observation)
	if err != nil || !bytes.Equal(canonical, records[0].Payload) {
		return WalletInventory{}, errors.New("wallet opening observation is not canonical")
	}
	value := WalletInventory{BindingSHA256: binding, HeadSHA256: records[0].Hash, OpenedAt: records[0].At, Opening: observation,
		NativeLamports: observation.NativeLamports, TokenUnits: observation.TokenUnits,
		LastFinalizedSlot: walletObservationSlot(observation)}
	return foldWalletAccounting(records[1:], value, policy)
}

func validateWalletObservation(value txflow.WalletObservation, policy Policy, owner, mint string) error {
	address, err := orcaswap.AssociatedTokenAddress(owner, mint)
	if err != nil || value.Owner != owner || value.TokenMint != mint || value.TokenAccount != address ||
		value.GenesisHash != solana.MainnetBetaGenesisHash ||
		value.PrimaryIdentity != policy.JupiterProviders.PrimaryOriginSHA256 ||
		value.SecondaryIdentity != policy.JupiterProviders.SecondaryOriginSHA256 ||
		value.MinimumContextSlot == 0 || value.MaximumContextSlot < value.MinimumContextSlot ||
		value.MaximumContextSlot-value.MinimumContextSlot != proposalcheck.MaxEvidenceSlotSkew {
		return errors.New("wallet observation does not match protected identity and context bounds")
	}
	for _, slot := range []uint64{value.NativePrimarySlot, value.NativeSecondarySlot, value.TokenPrimarySlot, value.TokenSecondarySlot} {
		if slot < value.MinimumContextSlot || slot > value.MaximumContextSlot {
			return errors.New("wallet observation contains an out-of-range account context")
		}
	}
	return nil
}
