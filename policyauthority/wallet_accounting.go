package policyauthority

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const (
	walletReservationEvent = "wallet.claim-reserved-v1"
	walletAccountingEvent  = "wallet.claim-accounted-v1"
)

// WalletReservation binds one existing claim to a freshly observed inventory
// head. It does not replace signer spending limits or grant signing permission.
type WalletReservation struct {
	ClaimSHA256        string                   `json:"claim_sha256"`
	ActionID           string                   `json:"action_id"`
	PreviousHeadSHA256 string                   `json:"previous_head_sha256"`
	Observation        txflow.WalletObservation `json:"observation"`
}

type walletAccounting struct {
	ReservationSHA256 string                                   `json:"reservation_sha256"`
	TerminalSHA256    string                                   `json:"terminal_sha256"`
	TerminalAt        time.Time                                `json:"terminal_at"`
	Balances          submitter.JupiterFinalizedWalletEvidence `json:"balances"`
}

// FinalizeWalletClaim records verified terminal evidence and accounts the exact
// reserved claim. A crash between these two durable steps is safe to retry.
// It cannot reconcile the network, sign, send or release the original claim.
func FinalizeWalletClaim(path, claimPath string, policy Policy, request signer.Request,
	recoveryPolicy submitter.Policy, now time.Time,
) (WalletInventory, error) {
	return finalizeWalletClaim(path, claimPath, policy, request, now, func() (submitter.JupiterFinalizedWalletEvidence, error) {
		return submitter.ReadJupiterFinalizedWalletEvidence(recoveryPolicy, request)
	})
}

func finalizeWalletClaim(path, claimPath string, policy Policy, request signer.Request, now time.Time,
	readBalances func() (submitter.JupiterFinalizedWalletEvidence, error),
) (WalletInventory, error) {
	value, err := ReadWalletInventory(path, policy)
	if err != nil {
		return WalletInventory{}, err
	}
	claims, err := journal.ReadRecords(claimPath)
	if err != nil || len(claims) == 0 {
		return WalletInventory{}, errors.New("original wallet claim is unavailable")
	}
	claim, err := ValidatePaperRequestClaim(claims[0], policy, request)
	if err != nil {
		return WalletInventory{}, err
	}
	if value.PendingSHA256 != "" && value.Pending.ActionID == request.ActionID && value.Pending.ClaimSHA256 == claim {
		if _, err := recordPaperTerminal(claimPath, policy, request, now, func() (submitter.JupiterFinalizedEvidence, error) {
			balances, err := readBalances()
			return balances.Finalized, err
		}); err != nil {
			return WalletInventory{}, err
		}
	}
	// Without that pending claim, only an already-accounted exact replay may
	// succeed. No terminal is appended for a foreign or unreserved claim.
	return applyFinalizedWalletClaim(path, claimPath, policy, request, now, readBalances)
}

// ReserveWalletClaim admits one exact existing claim to inventory accounting.
// Unexplained balances, another pending claim, or previously accounted actions
// are rejected. The original claim remains locked; this does not sign or send.
func ReserveWalletClaim(ctx context.Context, path, claimPath string, policy Policy,
	request signer.Request, lifecycle *txflow.Lifecycle, now time.Time,
) (WalletInventory, error) {
	if lifecycle == nil {
		return WalletInventory{}, errors.New("wallet reservation requires independent evidence")
	}
	return reserveWalletClaim(path, claimPath, policy, request, now, func(value WalletInventory) (txflow.WalletObservation, error) {
		return observeWalletReservation(ctx, lifecycle, value)
	})
}

func observeWalletReservation(ctx context.Context, lifecycle *txflow.Lifecycle, value WalletInventory) (txflow.WalletObservation, error) {
	primary, secondary := lifecycle.EvidenceProviderIdentities()
	if primary != value.Opening.PrimaryIdentity || secondary != value.Opening.SecondaryIdentity {
		return txflow.WalletObservation{}, errors.New("wallet reservation providers do not match")
	}
	return lifecycle.ObserveWallet(ctx, value.Opening.Owner, value.Opening.TokenMint,
		value.Opening.GenesisHash, value.LastFinalizedSlot, proposalcheck.MaxEvidenceSlotSkew)
}

func reserveWalletClaim(path, claimPath string, policy Policy, request signer.Request, now time.Time,
	observe func(WalletInventory) (txflow.WalletObservation, error),
) (WalletInventory, error) {
	started := time.Now()
	claims, err := journal.ReadRecords(claimPath)
	if err != nil || len(claims) != 1 {
		return WalletInventory{}, errors.New("wallet reservation requires one unfinished original claim")
	}
	claim, err := ValidatePaperRequestClaim(claims[0], policy, request)
	if err != nil || now.Before(claims[0].At) {
		return WalletInventory{}, errors.New("wallet reservation claim or time is invalid")
	}
	return updateWalletInventory(path, policy, now, func(store *journal.Store, value WalletInventory) error {
		return reserveWalletClaimLocked(store, value, policy, request.ActionID, claim, now, started, observe)
	})
}

func reserveWalletClaimLocked(store *journal.Store, value WalletInventory, policy Policy,
	actionID, claim string, now, started time.Time, observe func(WalletInventory) (txflow.WalletObservation, error),
) error {
	if value.PendingSHA256 != "" {
		if value.Pending.ClaimSHA256 == claim && value.Pending.ActionID == actionID {
			return nil
		}
		return errors.New("wallet inventory already has a pending claim")
	}
	for _, record := range store.Records()[1:] {
		if record.ActionID == actionID {
			return errors.New("wallet claim was already accounted")
		}
	}
	observation, err := observe(value)
	if err != nil {
		return err
	}
	reservation := WalletReservation{ClaimSHA256: claim, ActionID: actionID,
		PreviousHeadSHA256: value.HeadSHA256, Observation: observation}
	if err := validateWalletReservation(value, reservation, policy); err != nil {
		return err
	}
	_, err = store.Append(now.Add(time.Since(started)).UTC(), walletReservationEvent, actionID, reservation)
	return err
}

// ApplyFinalizedWalletClaim accounts one reserved claim using its exact terminal
// and recovery records. Exact repeats do not append or credit again. Failed
// transactions charge their verified fees; no signer allowance is refunded.
func ApplyFinalizedWalletClaim(path, claimPath string, policy Policy, request signer.Request,
	recoveryPolicy submitter.Policy, now time.Time,
) (WalletInventory, error) {
	return applyFinalizedWalletClaim(path, claimPath, policy, request, now, func() (submitter.JupiterFinalizedWalletEvidence, error) {
		return submitter.ReadJupiterFinalizedWalletEvidence(recoveryPolicy, request)
	})
}

func applyFinalizedWalletClaim(path, claimPath string, policy Policy, request signer.Request, now time.Time,
	readBalances func() (submitter.JupiterFinalizedWalletEvidence, error),
) (WalletInventory, error) {
	started := time.Now()
	return updateWalletInventory(path, policy, now, func(store *journal.Store, value WalletInventory) error {
		var balances submitter.JupiterFinalizedWalletEvidence
		delta, err := readPaperSwapAccounting(claimPath, policy, request, func() (submitter.JupiterFinalizedEvidence, error) {
			var err error
			balances, err = readBalances()
			return balances.Finalized, err
		})
		if err != nil {
			return err
		}
		completed := now.Add(time.Since(started)).UTC()
		if completed.Before(delta.TerminalAt) {
			return errors.New("wallet accounting time predates the finalized terminal")
		}
		for _, record := range store.Records()[1:] {
			if record.Type != walletAccountingEvent {
				continue
			}
			var previous walletAccounting
			if err := strictjson.Decode(record.Payload, &previous); err != nil {
				return err
			}
			if record.ActionID != request.ActionID {
				if previous.Balances.Finalized.TransactionSHA256 == balances.Finalized.TransactionSHA256 ||
					previous.Balances.Finalized.RequestSHA256 == balances.Finalized.RequestSHA256 {
					return errors.New("wallet transaction or request was already accounted")
				}
				continue
			}
			if previous.Balances != balances || previous.TerminalSHA256 != delta.TerminalSHA256 || previous.TerminalAt != delta.TerminalAt {
				return errors.New("wallet accounting conflicts with the completed action")
			}
			return nil
		}
		if value.PendingSHA256 == "" || value.Pending.ActionID != request.ActionID || value.Pending.ClaimSHA256 != delta.ClaimSHA256 {
			return errors.New("wallet accounting requires the exact pending claim")
		}
		receipt := walletAccounting{ReservationSHA256: value.PendingSHA256, TerminalSHA256: delta.TerminalSHA256,
			TerminalAt: delta.TerminalAt, Balances: balances}
		if err := validateWalletAccounting(value, receipt); err != nil {
			return err
		}
		_, err = store.Append(completed, walletAccountingEvent, request.ActionID, receipt)
		return err
	})
}

func updateWalletInventory(path string, policy Policy, now time.Time,
	update func(*journal.Store, WalletInventory) error,
) (result WalletInventory, err error) {
	binding, owner, mint, err := walletInventoryBinding(policy)
	if err != nil || now.IsZero() {
		return WalletInventory{}, errors.New("wallet accounting policy or time is invalid")
	}
	// Never create an opening or repair an incomplete accounting attempt here.
	if _, err := journal.ReadRecords(path); err != nil {
		return WalletInventory{}, err
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
	value, err := openingWalletInventory(store.Records(), policy, binding, owner, mint)
	if err != nil {
		return WalletInventory{}, err
	}
	if err := update(store, value); err != nil {
		return WalletInventory{}, err
	}
	return openingWalletInventory(store.Records(), policy, binding, owner, mint)
}

func foldWalletAccounting(records []journal.Record, value WalletInventory, policy Policy) (WalletInventory, error) {
	seen := make(map[string]bool)
	transactions, requests := make(map[string]bool), make(map[string]bool)
	for _, record := range records {
		if record.PrevHash != value.HeadSHA256 || !validHexDigest(record.ActionID) {
			return WalletInventory{}, errors.New("wallet accounting chain is invalid")
		}
		var payload any
		switch record.Type {
		case walletReservationEvent:
			var reservation WalletReservation
			if err := strictjson.Decode(record.Payload, &reservation); err != nil {
				return WalletInventory{}, err
			}
			if seen[record.ActionID] || reservation.ActionID != record.ActionID {
				return WalletInventory{}, errors.New("wallet claim identity is repeated or mismatched")
			}
			if err := validateWalletReservation(value, reservation, policy); err != nil {
				return WalletInventory{}, err
			}
			seen[record.ActionID] = true
			value.Pending, value.PendingSHA256, payload = reservation, record.Hash, reservation
			value.PendingAt = record.At
		case walletAccountingEvent:
			var receipt walletAccounting
			if err := strictjson.Decode(record.Payload, &receipt); err != nil {
				return WalletInventory{}, err
			}
			if record.ActionID != value.Pending.ActionID {
				return WalletInventory{}, errors.New("wallet accounting action differs from reservation")
			}
			if err := validateWalletAccounting(value, receipt); err != nil {
				return WalletInventory{}, err
			}
			if record.At.Before(receipt.TerminalAt) || transactions[receipt.Balances.Finalized.TransactionSHA256] ||
				requests[receipt.Balances.Finalized.RequestSHA256] {
				return WalletInventory{}, errors.New("wallet accounting chronology or unique identity is invalid")
			}
			transactions[receipt.Balances.Finalized.TransactionSHA256] = true
			requests[receipt.Balances.Finalized.RequestSHA256] = true
			value.NativeLamports, value.TokenUnits = receipt.Balances.Payer.PostLamports, receipt.Balances.Token.PostUnits
			value.LastFinalizedSlot = receipt.Balances.Finalized.FinalizedSlot
			value.Pending, value.PendingSHA256, payload = WalletReservation{}, "", receipt
			value.PendingAt = time.Time{}
		default:
			return WalletInventory{}, errors.New("wallet accounting event is unsupported")
		}
		canonical, err := json.Marshal(payload)
		if err != nil || !bytes.Equal(canonical, record.Payload) {
			return WalletInventory{}, errors.New("wallet accounting event is not canonical")
		}
		value.HeadSHA256 = record.Hash
	}
	return value, nil
}

func validateWalletReservation(value WalletInventory, reservation WalletReservation, policy Policy) error {
	if value.PendingSHA256 != "" || !validHexDigest(reservation.ClaimSHA256) || !validHexDigest(reservation.ActionID) ||
		reservation.PreviousHeadSHA256 != value.HeadSHA256 {
		return errors.New("wallet reservation does not match the available inventory head")
	}
	o := reservation.Observation
	if err := validateWalletObservation(o, policy, value.Opening.Owner, value.Opening.TokenMint); err != nil {
		return err
	}
	if o.MinimumContextSlot < value.LastFinalizedSlot || o.NativeLamports != value.NativeLamports || o.TokenUnits != value.TokenUnits {
		return errors.New("wallet balances changed outside accounted trades or observation is stale")
	}
	return nil
}

func validateWalletAccounting(value WalletInventory, receipt walletAccounting) error {
	b, f := receipt.Balances, receipt.Balances.Finalized
	if value.PendingSHA256 == "" || receipt.ReservationSHA256 != value.PendingSHA256 || !validHexDigest(receipt.TerminalSHA256) ||
		receipt.TerminalAt.IsZero() || receipt.TerminalAt.Before(value.PendingAt) ||
		f.ActionID != value.Pending.ActionID || !validHexDigest(f.RequestSHA256) || !validHexDigest(f.TransactionSHA256) ||
		f.FinalizedSlot <= walletObservationSlot(value.Pending.Observation) ||
		f.PrimaryEffectSlot != f.FinalizedSlot || f.SecondaryEffectSlot != f.FinalizedSlot ||
		b.Payer.PreLamports != value.NativeLamports || b.Token.PreUnits != value.TokenUnits {
		return errors.New("wallet finalized evidence does not continue the reserved inventory")
	}
	delta, err := normalizedPaperSwapAccounting(f)
	if err != nil || delta.TokenMint != value.Opening.TokenMint || b.Token.Account != value.Opening.TokenAccount || f.FeeLamports == 0 {
		return errors.New("wallet finalized swap identity is invalid")
	}
	p := jupiterswap.Policy{Owner: value.Opening.Owner, InputMint: f.InputMint, OutputMint: f.OutputMint}
	effects := txflow.JupiterEffectEvidence{InputAmount: f.InputSpent, OutputAmount: f.OutputReceived,
		FeeLamports: f.FeeLamports, OutputAccountRent: f.OutputAccountRent, Payer: &b.Payer, Token: &b.Token}
	failed := f.Verdict == txflow.VerdictFailed
	if !effects.ValidPayerEffects(p.NativeInput(), failed) || !effects.ValidTokenEffects(p, failed) {
		return errors.New("wallet finalized balance arithmetic is invalid")
	}
	return nil
}

func walletObservationSlot(value txflow.WalletObservation) uint64 {
	return max(value.NativePrimarySlot, value.NativeSecondarySlot, value.TokenPrimarySlot, value.TokenSecondarySlot)
}
