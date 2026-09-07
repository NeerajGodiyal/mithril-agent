package policyauthority

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/submitter"
)

const strategyContinuationEvent = "wallet.strategy-claim-outcome-v1"

type strategyContinuation struct {
	DecisionSHA256       string                `json:"decision_sha256"`
	RecoveryPolicySHA256 string                `json:"recovery_policy_sha256"`
	Outcome              WalletStrategyOutcome `json:"outcome"`
}

// ApplyStrategyContinuationOutcome delivers exact accounting for a recorded
// continuation decision. Recovery policies are independently supplied, matched
// by their original digest, and never serialized or replaced with current policy.
// This records knowledge only; it does not sign, send or replenish spending caps.
func ApplyStrategyContinuationOutcome(path string, originalAuthority Policy,
	originalRecovery, recoveryPolicy submitter.Policy, decisionSHA, accountingSHA string,
	deliveryAt time.Time, historicalRecoveryPolicies ...submitter.Policy,
) (*shadow.AccountedStrategy, error) {
	policies := append(append([]submitter.Policy(nil), historicalRecoveryPolicies...), recoveryPolicy)
	records, err := journal.ReadRecords(path)
	if err != nil {
		return nil, err
	}
	state, _, err := replayStrategyJournal(records, originalAuthority, originalRecovery, deliveryAt,
		strategyOutcomeReader(originalAuthority, originalRecovery), policies...)
	if err != nil {
		return nil, err
	}
	digest, err := strategyRecoveryPolicyHash(recoveryPolicy)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.Type != strategyContinuationEvent {
			continue
		}
		var retained strategyContinuation
		if err := decodeStrategyPayload(record.Payload, &retained); err != nil {
			return nil, err
		}
		if retained.DecisionSHA256 == decisionSHA || retained.Outcome.Amounts.AccountingSHA256 == accountingSHA {
			if retained.DecisionSHA256 != decisionSHA || retained.Outcome.Amounts.AccountingSHA256 != accountingSHA || retained.RecoveryPolicySHA256 != digest {
				return nil, errors.New("strategy continuation conflicts with original outcome")
			}
			return state, nil
		}
	}
	var seed strategySeed
	if err := decodeStrategyPayload(records[0].Payload, &seed); err != nil {
		return nil, err
	}
	value, err := verifyStrategyContinuation(seed, records, decisionSHA, accountingSHA, digest,
		deliveryAt, originalRecovery, policies...)
	if err != nil {
		return nil, err
	}
	if err := state.ApplyOutcome(value.Outcome.Amounts, deliveryAt); err != nil {
		return nil, err
	}
	// Reverification precedes writer locks. Under wallet -> strategy locks only
	// compare the exact verified prefixes; never acquire wallet reads in reverse.
	_, err = updateWalletInventory(seed.WalletPath, originalAuthority, deliveryAt, func(wallet *journal.Store, _ WalletInventory) error {
		found := false
		for _, record := range wallet.Records() {
			if record.Hash == accountingSHA {
				found = true
				break
			}
		}
		if !found {
			return errors.New("strategy accounting prefix changed before delivery")
		}
		last := records[len(records)-1]
		return appendStrategyEvent(path, records, journal.Record{At: deliveryAt.UTC(), Type: strategyContinuationEvent,
			ActionID: seed.ClaimSHA256, PrevHash: last.Hash}, value)
	})
	if err != nil {
		return nil, err
	}
	return state, nil
}

func strategyRecoveryPolicyHash(policy submitter.Policy) (string, error) {
	if err := submitter.ValidateJupiterPolicy(policy); err != nil {
		return "", err
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte(strategyOutcomeEvent+"/recovery-policy\x00"), raw...))
	return hex.EncodeToString(digest[:]), nil
}

func strategyRecoveryPolicy(digest string, original submitter.Policy, historical ...submitter.Policy) (submitter.Policy, error) {
	for _, policy := range append([]submitter.Policy{original}, historical...) {
		hash, err := strategyRecoveryPolicyHash(policy)
		if err != nil {
			return submitter.Policy{}, err
		}
		if hash == digest {
			return policy, nil
		}
	}
	return submitter.Policy{}, errors.New("original strategy recovery policy is unavailable")
}

func verifyStrategyContinuation(seed strategySeed, prior []journal.Record, decisionSHA, accountingSHA, recoverySHA string,
	deliveryAt time.Time, originalRecovery submitter.Policy, historical ...submitter.Policy,
) (strategyContinuation, error) {
	if !validHexDigest(decisionSHA) || !validHexDigest(accountingSHA) || deliveryAt.IsZero() {
		return strategyContinuation{}, errors.New("strategy continuation requires exact decision and accounting identities")
	}
	var pending journal.Record
	for _, record := range prior {
		switch record.Type {
		case strategyDecisionEvent:
			if pending.Hash != "" {
				return strategyContinuation{}, errors.New("strategy has overlapping decisions")
			}
			pending = record
		case strategyContinuationEvent:
			var completed strategyContinuation
			if err := decodeStrategyPayload(record.Payload, &completed); err != nil {
				return strategyContinuation{}, err
			}
			if completed.DecisionSHA256 != pending.Hash || completed.Outcome.Amounts.AccountingSHA256 == accountingSHA {
				return strategyContinuation{}, errors.New("strategy continuation ordering or accounting identity conflicts")
			}
			pending = journal.Record{}
		case strategyCancellationEvent:
			var canceled strategyCancellation
			if err := decodeStrategyPayload(record.Payload, &canceled); err != nil {
				return strategyContinuation{}, err
			}
			if canceled.DecisionSHA256 != pending.Hash {
				return strategyContinuation{}, errors.New("strategy cancellation differs from pending decision")
			}
			pending = journal.Record{}
		case strategyOutcomeEvent:
			var first strategyOutcome
			if err := decodeStrategyPayload(record.Payload, &first); err != nil {
				return strategyContinuation{}, err
			}
			if first.Outcome.Amounts.AccountingSHA256 == accountingSHA {
				return strategyContinuation{}, errors.New("strategy first accounting cannot be reused")
			}
		}
	}
	if pending.Hash != decisionSHA {
		return strategyContinuation{}, errors.New("strategy outcome does not match its unresolved decision")
	}
	var decision strategyDecision
	if err := decodeStrategyPayload(pending.Payload, &decision); err != nil {
		return strategyContinuation{}, err
	}
	claimPath := seed.WalletPath + ".claim-" + decision.WalletHeadSHA256 + ".jsonl"
	request, err := ReadClaimedPaperRequest(claimPath, decision.Policy)
	if err != nil {
		return strategyContinuation{}, err
	}
	claims, err := journal.ReadRecords(claimPath)
	if err != nil || len(claims) == 0 {
		return strategyContinuation{}, errors.New("strategy continuation claim is unavailable")
	}
	var claim paperRequestClaim
	if err := decodeStrategyPayload(claims[0].Payload, &claim); err != nil {
		return strategyContinuation{}, err
	}
	if claims[0].At.Before(pending.At) || claim.AcquisitionSHA256 != decision.AcquisitionSHA256 {
		return strategyContinuation{}, errors.New("strategy claim predates decision or differs from its acquisition")
	}
	if err := checkStrategyClaimTime(seed, decision, pending.At, claims[0].At, time.Duration(claim.MaxDecisionAgeNS)); err != nil {
		return strategyContinuation{}, err
	}
	intent, err := strategyClaimIntent(decisionSHA, decision.WalletHeadSHA256, decision.AcquisitionSHA256,
		request.ActionID, time.Duration(claim.MaxDecisionAgeNS), seed.Bounds.ReserveLamports)
	if err != nil {
		return strategyContinuation{}, err
	}
	claimSHA, err := ValidatePaperRequestIntent(claims[0], decision.Policy, request, intent)
	if err != nil {
		return strategyContinuation{}, err
	}
	if request.JupiterCandidate == nil {
		return strategyContinuation{}, errors.New("strategy continuation lacks original candidate")
	}
	acquisitionSHA, err := proposalcheck.VerifyAcquisition(decision.AcquisitionPath, *request.JupiterCandidate,
		claims[0].At, time.Duration(decision.MaxAgeNS))
	if err != nil || acquisitionSHA != decision.AcquisitionSHA256 {
		return strategyContinuation{}, errors.New("strategy continuation candidate differs from original acquisition")
	}
	recovery, err := strategyRecoveryPolicy(recoverySHA, originalRecovery, historical...)
	if err != nil {
		return strategyContinuation{}, err
	}
	value, err := ReadWalletStrategyOutcome(seed.WalletPath, claimPath, accountingSHA, decision.Policy, request, recovery, deliveryAt)
	if err != nil {
		return strategyContinuation{}, err
	}
	if value.PreviousHeadSHA256 != decision.WalletHeadSHA256 || value.ClaimSHA256 != claimSHA || value.ActionID != request.ActionID ||
		value.Amounts.AccountingSHA256 != accountingSHA || value.TerminalAt.IsZero() || value.TerminalAt.After(value.KnownAt) || value.KnownAt.After(deliveryAt) {
		return strategyContinuation{}, errors.New("strategy continuation differs from original claim or knowledge chronology")
	}
	return strategyContinuation{DecisionSHA256: decisionSHA, RecoveryPolicySHA256: recoverySHA, Outcome: value}, nil
}
