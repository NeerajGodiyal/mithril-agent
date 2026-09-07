package policyauthority

import (
	"errors"
	"os"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/submitter"
)

// StrategyReconciliation reports verified strategy knowledge, never permission
// to execute. A blocked claim is not replaced, automatically reserved or released.
type StrategyReconciliation struct {
	Strategy         StrategyJournalStatus
	ClaimPath        string
	AcquisitionPath  string
	AccountingSHA256 string
	BlockedReason    string
}

// ReconcileStrategyJournal delivers an exact pending claim's already-finalized
// recovery evidence to inventory and strategy. It performs no network reads,
// signing or submission. The original first claim uses originalRecovery; a
// continuation uses the explicitly supplied recoveryPolicy or one uniquely
// matching protected historical policy. Selection never probes alternative
// recovery archives. Missing or invalid finality returns an error and cannot
// authorize another observation or acquisition. Completed accounting survives a
// delivery failure and is safely retried with its original knowledge timestamp.
func ReconcileStrategyJournal(path, walletPath string, originalAuthority Policy,
	originalRecovery, recoveryPolicy submitter.Policy, now time.Time,
	historicalRecoveryPolicies ...submitter.Policy,
) (StrategyReconciliation, error) {
	started := time.Now()
	checkedAt := func() time.Time { return now.Add(time.Since(started)) }
	policies := append([]submitter.Policy(nil), historicalRecoveryPolicies...)
	if recoveryPolicy != (submitter.Policy{}) {
		policies = append(policies, recoveryPolicy)
	}
	status, err := ReadStrategyJournalStatus(path, originalAuthority, originalRecovery, now, policies...)
	if err != nil {
		return StrategyReconciliation{}, err
	}
	records, err := journal.ReadRecords(path)
	if err != nil {
		return StrategyReconciliation{}, err
	}
	if len(records) == 0 || records[len(records)-1].Hash != status.HeadSHA256 {
		return StrategyReconciliation{}, errors.New("strategy history changed during reconciliation")
	}
	var seed strategySeed
	if err := decodeStrategyPayload(records[0].Payload, &seed); err != nil {
		return StrategyReconciliation{}, err
	}
	if seed.WalletPath != walletPath {
		return StrategyReconciliation{}, errors.New("strategy reconciliation wallet differs from original seed")
	}
	result := StrategyReconciliation{Strategy: status}
	if !status.State.Pending() {
		return reconcileStrategyIdle(result, path, walletPath, originalAuthority, checkedAt())
	}
	authority, recovery := originalAuthority, originalRecovery
	claimPath, previousHead := seed.ClaimPath, seed.OpeningSHA256
	var decision strategyDecision
	var decisionRecord journal.Record
	if status.PendingDecisionSHA256 != "" {
		for _, record := range records {
			if record.Hash == status.PendingDecisionSHA256 {
				decisionRecord = record
				break
			}
		}
		if decisionRecord.Type != strategyDecisionEvent {
			return StrategyReconciliation{}, errors.New("pending strategy decision is unavailable")
		}
		if err := decodeStrategyPayload(decisionRecord.Payload, &decision); err != nil {
			return StrategyReconciliation{}, err
		}
		authority, recovery = decision.Policy, recoveryPolicy
		previousHead = decision.WalletHeadSHA256
		claimPath = walletPath + ".claim-" + previousHead + ".jsonl"
	}
	result.ClaimPath = claimPath
	wallet, err := ReadWalletInventory(walletPath, authority)
	if err != nil {
		return StrategyReconciliation{}, err
	}
	claims, err := journal.ReadRecords(claimPath)
	if errors.Is(err, os.ErrNotExist) || err == nil && len(claims) == 0 {
		if wallet.PendingSHA256 != "" || wallet.HeadSHA256 != previousHead || decisionRecord.Hash == "" {
			return StrategyReconciliation{}, errors.New("reserved strategy claim is missing")
		}
		result.BlockedReason = "claim_not_prepared"
		return result, nil
	}
	if err != nil {
		return StrategyReconciliation{}, err
	}
	request, err := ReadClaimedPaperRequest(claimPath, authority)
	if err != nil {
		return StrategyReconciliation{}, err
	}
	claimSHA, err := paperTerminalClaim(claims, authority, request, checkedAt())
	if err != nil {
		return StrategyReconciliation{}, err
	}
	if decisionRecord.Hash == "" {
		if claimSHA != seed.ClaimSHA256 {
			return StrategyReconciliation{}, errors.New("original strategy claim identity changed")
		}
	} else {
		// Prove strategy ownership before terminal/accounting writes, not merely
		// after them when the outcome reader verifies the resulting receipt.
		var claim paperRequestClaim
		if err := decodeStrategyPayload(claims[0].Payload, &claim); err != nil {
			return StrategyReconciliation{}, err
		}
		if claims[0].At.Before(decisionRecord.At) || claim.AcquisitionSHA256 != decision.AcquisitionSHA256 {
			return StrategyReconciliation{}, errors.New("strategy claim acquisition or chronology differs")
		}
		if err := checkStrategyClaimTime(seed, decision, decisionRecord.At, claims[0].At, time.Duration(claim.MaxDecisionAgeNS)); err != nil {
			return StrategyReconciliation{}, err
		}
		intent, err := strategyClaimIntent(decisionRecord.Hash, previousHead, decision.AcquisitionSHA256,
			request.ActionID, time.Duration(claim.MaxDecisionAgeNS), seed.Bounds.ReserveLamports)
		if err != nil {
			return StrategyReconciliation{}, err
		}
		if _, err := ValidatePaperRequestIntent(claims[0], authority, request, intent); err != nil {
			return StrategyReconciliation{}, err
		}
		if request.JupiterCandidate == nil {
			return StrategyReconciliation{}, errors.New("strategy claim candidate is unavailable")
		}
		acquisition, err := proposalcheck.VerifyAcquisition(decision.AcquisitionPath, *request.JupiterCandidate,
			claims[0].At, time.Duration(decision.MaxAgeNS))
		if err != nil || acquisition != decision.AcquisitionSHA256 {
			return StrategyReconciliation{}, errors.New("strategy claim differs from original acquired candidate")
		}
	}
	accounting, err := strategyClaimAccounting(walletPath, request.ActionID)
	if err != nil {
		return StrategyReconciliation{}, err
	}
	if accounting == "" {
		if wallet.PendingSHA256 == "" && wallet.HeadSHA256 == previousHead {
			result.BlockedReason = "claim_not_reserved"
			return result, nil
		}
		if wallet.PendingSHA256 == "" || wallet.Pending.ClaimSHA256 != claimSHA ||
			wallet.Pending.ActionID != request.ActionID || wallet.Pending.PreviousHeadSHA256 != previousHead {
			return StrategyReconciliation{}, errors.New("strategy claim differs from pending wallet reservation")
		}
	}
	if decisionRecord.Hash != "" && recovery == (submitter.Policy{}) {
		recovery, err = pendingStrategyRecoveryPolicy(authority, historicalRecoveryPolicies)
		if err != nil {
			return StrategyReconciliation{}, err
		}
	}
	if _, err := strategyRecoveryPolicyHash(recovery); err != nil {
		return StrategyReconciliation{}, err
	}
	if _, err := FinalizeWalletClaim(walletPath, claimPath, authority, request, recovery, checkedAt()); err != nil {
		return StrategyReconciliation{}, err
	}
	accounting, err = strategyClaimAccounting(walletPath, request.ActionID)
	if err != nil || accounting == "" {
		return StrategyReconciliation{}, errors.New("exact strategy wallet accounting is unavailable")
	}
	if decisionRecord.Hash == "" {
		_, err = ApplyStrategyJournalOutcome(path, originalAuthority, originalRecovery, accounting, checkedAt(), policies...)
	} else {
		_, err = ApplyStrategyContinuationOutcome(path, originalAuthority, originalRecovery, recovery,
			decisionRecord.Hash, accounting, checkedAt(), policies...)
	}
	if err != nil {
		return StrategyReconciliation{}, err
	}
	result.Strategy, err = ReadStrategyJournalStatus(path, originalAuthority, originalRecovery, checkedAt(), policies...)
	if err != nil {
		return StrategyReconciliation{}, err
	}
	result.AccountingSHA256 = accounting
	return reconcileStrategyIdle(result, path, walletPath, originalAuthority, checkedAt())
}

// A strategy with no pending quote is not necessarily an idle wallet: another
// caller may have reserved a claim, or crashed between claim and reservation.
// Do not decode that claim using the original strategy's possibly older policy.
func reconcileStrategyIdle(result StrategyReconciliation, strategyPath, walletPath string, authority Policy, at time.Time) (StrategyReconciliation, error) {
	if result.Strategy.State.Pending() {
		return result, nil
	}
	bind, owner, mint, err := walletInventoryBinding(authority)
	if err != nil {
		return StrategyReconciliation{}, err
	}
	records, err := journal.ReadRecords(walletPath)
	if err != nil {
		return StrategyReconciliation{}, err
	}
	wallet, err := openingWalletInventory(records, authority, bind, owner, mint)
	if err != nil {
		return StrategyReconciliation{}, err
	}
	if records[len(records)-1].At.After(at) {
		return StrategyReconciliation{}, errors.New("idle strategy wallet contains future records")
	}
	if wallet.PendingSHA256 != "" {
		result.BlockedReason = "wallet_has_pending_claim"
		return result, nil
	}
	ledger := result.Strategy.State.Ledger()
	if ledger.BaseUnits != wallet.NativeLamports || ledger.QuoteUnits != wallet.TokenUnits {
		result.BlockedReason = "wallet_strategy_mismatch"
		return result, nil
	}
	claimPath := walletPath + ".claim-" + wallet.HeadSHA256 + ".jsonl"
	strategyRecords, err := journal.ReadRecords(strategyPath)
	if err != nil {
		return StrategyReconciliation{}, err
	}
	if len(strategyRecords) == 0 || strategyRecords[len(strategyRecords)-1].Hash != result.Strategy.HeadSHA256 {
		return StrategyReconciliation{}, errors.New("strategy history changed during idle reconciliation")
	}
	acquisitionPath, err := strategyAcquisitionPath(walletPath, wallet.HeadSHA256, strategyRecords)
	if err != nil {
		return StrategyReconciliation{}, err
	}
	result.AcquisitionPath = acquisitionPath
	var retainedClaim, retainedAcquisition bool
	for index, path := range []string{claimPath, acquisitionPath, acquisitionPath + ".intent.jsonl"} {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return StrategyReconciliation{}, err
		}
		records, err := journal.ReadRecords(path)
		if err != nil {
			return StrategyReconciliation{}, err
		}
		if len(records) != 0 {
			if index == 0 {
				retainedClaim = true
			} else {
				retainedAcquisition = true
			}
		}
	}
	if retainedClaim {
		result.ClaimPath = claimPath
		result.BlockedReason = "claim_not_reserved"
	} else if retainedAcquisition {
		// Presence is not qualification. Acquire must verify the original
		// intent/receipt together before resuming, without a new observation.
		result.BlockedReason = "acquisition_pending"
	}
	return result, nil
}

func strategyClaimAccounting(path, action string) (string, error) {
	records, err := journal.ReadRecords(path)
	if err != nil {
		return "", err
	}
	for _, record := range records {
		if record.Type == walletAccountingEvent && record.ActionID == action {
			return record.Hash, nil
		}
	}
	return "", nil
}
