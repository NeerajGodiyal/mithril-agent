package policyauthority

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

// PrepareStrategyWalletClaim binds a persisted post-accounting decision to one
// unsigned request and observed wallet reservation. An existing claim recovers
// before strategy or quote freshness checks. No approval, signing or submission
// is performed, and the original first-action history is never reconstructed.
func PrepareStrategyWalletClaim(ctx context.Context, walletPath, strategyPath string,
	originalAuthority Policy, recoveryPolicy submitter.Policy, nextPolicy Policy,
	scheduleWindowStartUnix int64, now time.Time, maxDecisionAge time.Duration,
	evidence proposalcheck.NativeReserveEvidence, primary, secondary proposalcheck.FinalizedSlotReader,
	lifecycle *txflow.Lifecycle,
	historicalRecoveryPolicies ...submitter.Policy,
) (WalletPaperClaim, error) {
	started := time.Now()
	if recovered, found, err := RecoverWalletPaperClaim(walletPath, nextPolicy, now); err != nil || found {
		return recovered, err
	}
	if lifecycle == nil {
		return WalletPaperClaim{}, errors.New("continuation reservation requires independent evidence")
	}
	// Verify immutable history before taking the wallet's exclusive lock: its
	// readers use shared locks. Both heads are compared again under write locks.
	records, err := journal.ReadRecords(strategyPath)
	if err != nil {
		return WalletPaperClaim{}, err
	}
	state, _, err := replayStrategyJournal(records, originalAuthority, recoveryPolicy, now.Add(time.Since(started)), strategyOutcomeReader(originalAuthority, recoveryPolicy), historicalRecoveryPolicies...)
	if err != nil {
		return WalletPaperClaim{}, err
	}
	var seed strategySeed
	if err := decodeStrategyPayload(records[0].Payload, &seed); err != nil {
		return WalletPaperClaim{}, err
	}
	if seed.WalletPath != walletPath || !state.Pending() {
		return WalletPaperClaim{}, errors.New("strategy has no pending decision for this wallet")
	}
	var decision strategyDecision
	var decisionRecord journal.Record
	for _, record := range records {
		if record.Type == strategyDecisionEvent {
			if err := decodeStrategyPayload(record.Payload, &decision); err != nil {
				return WalletPaperClaim{}, err
			}
			decisionRecord = record
		}
	}
	if decisionRecord.Hash == "" {
		return WalletPaperClaim{}, errors.New("strategy has no committed continuation decision")
	}
	expectedPolicy, _, err := paperRequestHashes(decision.Policy, signer.Request{})
	if err != nil {
		return WalletPaperClaim{}, err
	}
	actualPolicy, _, err := paperRequestHashes(nextPolicy, signer.Request{})
	if err != nil || expectedPolicy != actualPolicy {
		return WalletPaperClaim{}, errors.New("continuation policy differs from the committed decision")
	}
	actionID, err := jupiterswap.ComputeActionID(nextPolicy.TransactionPolicy.ProfileFingerprint, scheduleWindowStartUnix)
	if err != nil {
		return WalletPaperClaim{}, err
	}
	verifiedHead := records[len(records)-1].Hash
	return prepareWalletPaperClaim(walletPath, nextPolicy, now.Add(time.Since(started)),
		func(claimPath string, wallet WalletInventory) (request signer.Request, err error) {
			store, err := journal.OpenStrict(strategyPath)
			if err != nil {
				return signer.Request{}, err
			}
			defer func() {
				if closeErr := store.Close(); closeErr != nil {
					request = signer.Request{}
					err = errors.Join(err, closeErr)
				}
			}()
			current := store.Records()
			if len(current) == 0 || current[len(current)-1].Hash != verifiedHead || wallet.HeadSHA256 != decision.WalletHeadSHA256 || wallet.PendingSHA256 != "" {
				return signer.Request{}, errors.New("continuation strategy or available wallet head changed")
			}
			checkedAt := now.Add(time.Since(started))
			if err := checkStrategyClaimTime(seed, decision, decisionRecord.At, checkedAt, maxDecisionAge); err != nil {
				return signer.Request{}, err
			}
			acquired, err := proposalcheck.ReadAcquisition(decision.AcquisitionPath, checkedAt, time.Duration(decision.MaxAgeNS))
			if err != nil {
				return signer.Request{}, err
			}
			if acquired.SHA256 != decision.AcquisitionSHA256 || !acquired.ReceivedAt.Equal(decision.AcquiredAt) {
				return signer.Request{}, errors.New("continuation acquisition changed")
			}
			checked, err := proposalcheck.RecheckWithNativeReserve(ctx, evidence, primary, secondary,
				*nextPolicy.TransactionPolicy.Jupiter, *nextPolicy.JupiterProviders, acquired.Candidate, seed.Bounds.ReserveLamports)
			if err != nil {
				return signer.Request{}, err
			}
			reserve := checked.NativeReserve
			if reserve.ObservedBalanceLamports != wallet.NativeLamports || reserve.PrimaryContextSlot < wallet.LastFinalizedSlot || reserve.SecondaryContextSlot < wallet.LastFinalizedSlot {
				return signer.Request{}, errors.New("continuation reserve evidence differs from accounted wallet")
			}
			completed := now.Add(time.Since(started))
			if err := checkStrategyClaimTime(seed, decision, decisionRecord.At, completed, maxDecisionAge); err != nil {
				return signer.Request{}, err
			}
			receipt, err := proposalcheck.VerifyAcquisition(decision.AcquisitionPath, acquired.Candidate, completed, time.Duration(decision.MaxAgeNS))
			if err != nil || receipt != acquired.SHA256 {
				return signer.Request{}, errors.New("continuation acquisition changed or expired during preparation")
			}
			request, err = requestFromJupiterCheck(nextPolicy, acquired.Candidate, checked.Result, scheduleWindowStartUnix, completed)
			if err != nil {
				return signer.Request{}, err
			}
			intent, err := strategyClaimIntent(decisionRecord.Hash, decision.WalletHeadSHA256, receipt, request.ActionID, maxDecisionAge, seed.Bounds.ReserveLamports)
			if err != nil {
				return signer.Request{}, err
			}
			claims, err := journal.OpenStrict(claimPath)
			if err != nil {
				return signer.Request{}, err
			}
			err = claimPaperRequest(claims, nextPolicy, intent, request, completed, maxDecisionAge, receipt)
			err = errors.Join(err, claims.Close())
			if err != nil {
				return signer.Request{}, err
			}
			return request, nil
		}, func(value WalletInventory) (txflow.WalletObservation, error) {
			return observeWalletReservation(ctx, lifecycle, value)
		}, actionID)
}

func checkStrategyClaimTime(seed strategySeed, decision strategyDecision, committedAt, now time.Time, maxAge time.Duration) error {
	if seed.Policy.Adaptive == nil || maxAge <= 0 || maxAge > time.Duration(seed.Policy.Adaptive.MaxObservationGapSeconds)*time.Second || now.IsZero() {
		return errors.New("continuation decision age bound is invalid")
	}
	if _, frozen, err := strategyManagedDecisionAge(seed, decision, committedAt, ""); err != nil {
		return err
	} else if frozen != 0 {
		maxAge = min(maxAge, time.Duration(frozen))
	}
	for _, at := range []time.Time{decision.ObservationAt, decision.AcquiredAt, committedAt} {
		if at.IsZero() || at.After(now) || at.Before(now.Add(-maxAge)) {
			return errors.New("continuation decision or quote is outside its original recency bound")
		}
	}
	return nil
}

// Old standalone decisions have neither fields nor an intent. Existing legacy
// managed intents still constrain their original claim time; new decisions bind
// the intent hash so missing provenance cannot silently become standalone.
func strategyManagedDecisionAge(seed strategySeed, decision strategyDecision, committedAt time.Time, strategyPath string) (string, int64, error) {
	if (decision.ManagedIntentSHA256 == "") != (decision.MaxDecisionAgeNS == 0) || decision.MaxDecisionAgeNS < 0 {
		return "", 0, errors.New("managed strategy decision has incomplete age provenance")
	}
	if decision.AcquisitionPath == "" && decision.ManagedIntentSHA256 == "" {
		return "", 0, nil
	}
	if !filepath.IsAbs(decision.AcquisitionPath) || filepath.Clean(decision.AcquisitionPath) != decision.AcquisitionPath {
		return "", 0, errors.New("managed strategy acquisition requires an absolute clean path")
	}
	intentPath := decision.AcquisitionPath + ".intent.jsonl"
	if _, err := os.Lstat(intentPath); errors.Is(err, os.ErrNotExist) {
		if decision.ManagedIntentSHA256 != "" {
			return "", 0, errors.New("managed strategy decision intent is unavailable")
		}
		return "", 0, nil
	} else if err != nil {
		return "", 0, err
	}
	records, err := journal.ReadRecords(intentPath)
	if err != nil {
		return "", 0, err
	}
	intent, err := readStrategyRetirementIntent(records)
	if err != nil {
		return "", 0, err
	}
	if decision.ManagedIntentSHA256 != "" && (decision.ManagedIntentSHA256 != records[0].Hash || decision.MaxDecisionAgeNS != intent.MaxDecisionAgeNS) {
		return "", 0, errors.New("managed strategy decision age provenance changed")
	}
	policySHA, _, err := paperRequestHashes(decision.Policy, signer.Request{})
	if err != nil {
		return "", 0, err
	}
	if !filepath.IsAbs(intent.StrategyPath) || filepath.Clean(intent.StrategyPath) != intent.StrategyPath || strategyPath != "" && intent.StrategyPath != strategyPath ||
		intent.StrategyHeadSHA256 != decision.PreviousHeadSHA256 || intent.WalletHeadSHA256 != decision.WalletHeadSHA256 || intent.PolicySHA256 != policySHA ||
		intent.MaxAcquisitionAgeNS != decision.MaxAgeNS || intent.MaxDecisionAgeNS <= 0 || seed.Policy.Adaptive == nil ||
		time.Duration(intent.MaxDecisionAgeNS) > time.Duration(seed.Policy.Adaptive.MaxObservationGapSeconds)*time.Second ||
		records[0].At.Before(decision.ObservationAt) || records[0].At.After(decision.AcquiredAt) {
		return "", 0, errors.New("managed strategy intent differs from original decision")
	}
	acquired, err := proposalcheck.ReadAcquisition(decision.AcquisitionPath, committedAt, time.Duration(decision.MaxAgeNS))
	if err != nil {
		return "", 0, err
	}
	if acquired.SHA256 != decision.AcquisitionSHA256 || !acquired.ReceivedAt.Equal(decision.AcquiredAt) || acquired.Candidate.Request != intent.Request {
		return "", 0, errors.New("managed strategy intent differs from original acquisition")
	}
	return records[0].Hash, intent.MaxDecisionAgeNS, nil
}

func strategyClaimIntent(decision, wallet, acquisition, action string, maxAge time.Duration, reserve uint64) (proposalcheck.PaperIntent, error) {
	raw, err := json.Marshal(struct {
		Decision, Wallet, Acquisition, Action string
		MaxDecisionAgeNS                      int64
		ReserveLamports                       uint64
	}{decision, wallet, acquisition, action, int64(maxAge), reserve})
	if err != nil {
		return proposalcheck.PaperIntent{}, err
	}
	digest := sha256.Sum256(append([]byte("mithril-agent/strategy-claim-v1\x00"), raw...))
	return proposalcheck.PaperIntent{SHA256: hex.EncodeToString(digest[:])}, nil
}
