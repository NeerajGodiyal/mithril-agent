package policyauthority

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

// WalletPaperClaim is unsigned accounting state, never signing permission.
// Recovered means the original request is for recovery only, including after
// expiry; no new claim, reservation, quote or schedule was created or renewed.
type WalletPaperClaim struct {
	Inventory WalletInventory `json:"inventory"`
	ClaimPath string          `json:"claim_path"`
	Request   signer.Request  `json:"request"`
	Recovered bool            `json:"recovered"`
}

// RecoverWalletPaperClaim returns only an existing original request. It does
// not query providers, prepare a claim, reserve balances or repair journals.
// found is false only for an absent/empty claim with no pending reservation.
func RecoverWalletPaperClaim(path string, policy Policy, now time.Time) (WalletPaperClaim, bool, error) {
	missing := errors.New("wallet claim not yet prepared")
	result, err := prepareWalletPaperClaim(path, policy, now, func(string, WalletInventory) (signer.Request, error) {
		return signer.Request{}, missing
	}, nil)
	// A joined journal-close error must still propagate.
	if err == missing {
		return WalletPaperClaim{}, false, nil
	}
	if err != nil {
		return WalletPaperClaim{}, false, err
	}
	return result, true, nil
}

// PrepareWalletPaperClaim serializes preparation with inventory accounting.
// A durable claim survives a failed reservation and is returned recovery-only
// on retry. The caller must retain one protected inventory path across restarts;
// changing paths is not a way to release pending actions or signer spending caps.
func PrepareWalletPaperClaim(ctx context.Context, path string, policy Policy,
	paperPolicy shadow.Policy, ticks []shadow.Tick, bounds proposalcheck.PaperIntentBounds,
	candidate proposalcheck.Candidate, scheduleWindowStartUnix int64, now time.Time,
	maxDecisionAge time.Duration, acquisitionPath string, maxAcquisitionAge time.Duration,
	evidence proposalcheck.NativeReserveEvidence, primary, secondary proposalcheck.FinalizedSlotReader,
	lifecycle *txflow.Lifecycle,
) (WalletPaperClaim, error) {
	started := time.Now()
	actionID, actionErr := jupiterswap.ComputeActionID(policy.TransactionPolicy.ProfileFingerprint, scheduleWindowStartUnix)
	return prepareWalletPaperClaim(path, policy, now, func(claimPath string, value WalletInventory) (signer.Request, error) {
		if actionErr != nil {
			return signer.Request{}, actionErr
		}
		if bounds.NativeBudgetLamports > value.NativeLamports ||
			(!policy.TransactionPolicy.Jupiter.NativeInput() && candidate.Request.InputAmount > value.TokenUnits) {
			return signer.Request{}, errors.New("paper claim sizing exceeds accounted wallet balances")
		}
		return ClaimPaperRequest(ctx, claimPath, policy, paperPolicy, ticks, bounds,
			candidate, scheduleWindowStartUnix, now.Add(time.Since(started)), maxDecisionAge, acquisitionPath, maxAcquisitionAge,
			evidence, primary, secondary)
	}, func(value WalletInventory) (txflow.WalletObservation, error) {
		return observeWalletReservation(ctx, lifecycle, value)
	}, actionID)
}

func prepareWalletPaperClaim(path string, policy Policy, now time.Time,
	createClaim func(string, WalletInventory) (signer.Request, error), observe func(WalletInventory) (txflow.WalletObservation, error),
	freshActionID ...string,
) (WalletPaperClaim, error) {
	started := time.Now()
	var result WalletPaperClaim
	inventory, err := updateWalletInventory(path, policy, now, func(store *journal.Store, value WalletInventory) error {
		head := value.HeadSHA256
		if value.PendingSHA256 != "" {
			head = value.Pending.PreviousHeadSHA256
		}
		if !validHexDigest(head) {
			return errors.New("wallet claim head is invalid")
		}
		result.ClaimPath = path + ".claim-" + head + ".jsonl"
		var records []journal.Record
		if _, err := os.Lstat(result.ClaimPath); err == nil {
			records, err = journal.ReadRecords(result.ClaimPath)
			if err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if len(records) != 0 {
			request, err := ReadClaimedPaperRequest(result.ClaimPath, policy)
			if err != nil {
				return err
			}
			claim, err := paperTerminalClaim(records, policy, request, now)
			if err != nil {
				return err
			}
			if value.PendingSHA256 != "" && (value.Pending.ClaimSHA256 != claim || value.Pending.ActionID != request.ActionID) {
				return errors.New("retained claim differs from pending wallet inventory")
			}
			result.Request, result.Recovered = request, true
			return nil
		}
		if value.PendingSHA256 != "" {
			return errors.New("pending wallet claim journal is missing or empty")
		}
		if now.Before(store.Records()[len(store.Records())-1].At) {
			return errors.New("wallet admission time predates the inventory journal")
		}
		if len(freshActionID) != 0 {
			for _, record := range store.Records()[1:] {
				if record.ActionID == freshActionID[0] {
					return errors.New("wallet action was already claimed; no new claim created")
				}
			}
		}
		request, err := createClaim(result.ClaimPath, value)
		if err != nil {
			return err
		}
		records, err = journal.ReadRecords(result.ClaimPath)
		if err != nil {
			return err
		}
		if len(records) != 1 {
			return errors.New("new wallet claim is not a single original record")
		}
		if now.Add(time.Since(started)).Before(records[0].At) {
			return errors.New("new wallet claim timestamp is in the future")
		}
		claim, err := ValidatePaperRequestClaim(records[0], policy, request)
		if err != nil {
			return err
		}
		if err := reserveWalletClaimLocked(store, value, policy, request.ActionID, claim, now, started, observe); err != nil {
			return err
		}
		result.Request = request
		return nil
	})
	if err != nil {
		return WalletPaperClaim{}, err
	}
	result.Inventory = inventory
	return result, nil
}
