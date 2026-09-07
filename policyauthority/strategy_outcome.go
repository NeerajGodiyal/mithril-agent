package policyauthority

import (
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/submitter"
)

const strategyOutcomeEvent = "wallet.strategy-outcome-v1"

type strategyOutcome struct {
	RecoveryPolicySHA256 string                `json:"recovery_policy_sha256"`
	Outcome              WalletStrategyOutcome `json:"outcome"`
}

// ApplyStrategyJournalOutcome delivers the original claimed transaction's exact
// finalized accounting to strategy history. Source KnownAt is preserved; delivery
// time records when the strategy learned the result and starts its cooldown.
// An exact retry rechecks evidence but never renews the original delivery time.
// This supports only the first outcome, not subsequent claims or authorization.
func ApplyStrategyJournalOutcome(path string, authority Policy, recoveryPolicy submitter.Policy,
	accountingSHA256 string, deliveryAt time.Time,
	historicalRecoveryPolicies ...submitter.Policy,
) (*shadow.AccountedStrategy, error) {
	return applyStrategyJournalOutcome(path, authority, recoveryPolicy, accountingSHA256, deliveryAt, strategyOutcomeReader(authority, recoveryPolicy), historicalRecoveryPolicies...)
}

func strategyOutcomeReader(authority Policy, recoveryPolicy submitter.Policy) func(strategySeed, string, time.Time) (WalletStrategyOutcome, error) {
	return func(seed strategySeed, accounting string, at time.Time) (WalletStrategyOutcome, error) {
		request, err := ReadClaimedPaperRequest(seed.ClaimPath, authority)
		if err != nil {
			return WalletStrategyOutcome{}, err
		}
		return ReadWalletStrategyOutcome(seed.WalletPath, seed.ClaimPath, accounting, authority, request, recoveryPolicy, at)
	}
}

func applyStrategyJournalOutcome(path string, authority Policy, recoveryPolicy submitter.Policy,
	accountingSHA256 string, deliveryAt time.Time, readOutcome func(strategySeed, string, time.Time) (WalletStrategyOutcome, error),
	historicalRecoveryPolicies ...submitter.Policy,
) (result *shadow.AccountedStrategy, err error) {
	if _, err := journal.ReadRecords(path); err != nil {
		return nil, err
	}
	store, err := journal.OpenStrict(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			result = nil
			err = errors.Join(err, closeErr)
		}
	}()
	records := store.Records()
	state, _, err := replayStrategyJournal(records, authority, recoveryPolicy, deliveryAt, readOutcome, historicalRecoveryPolicies...)
	if err != nil {
		return nil, err
	}
	var seed strategySeed
	if err := decodeStrategyPayload(records[0].Payload, &seed); err != nil {
		return nil, err
	}
	verified, err := verifyStrategyOutcome(seed, authority, recoveryPolicy, accountingSHA256, deliveryAt, readOutcome)
	if err != nil {
		return nil, err
	}
	for _, record := range records[1:] {
		if record.Type != strategyOutcomeEvent {
			continue
		}
		var retained strategyOutcome
		if err := decodeStrategyPayload(record.Payload, &retained); err != nil {
			return nil, err
		}
		if retained != verified {
			return nil, errors.New("strategy first outcome conflicts with retained evidence")
		}
		return state, nil
	}
	if err := state.ApplyOutcome(verified.Outcome.Amounts, deliveryAt); err != nil {
		return nil, err
	}
	if _, err := store.Append(deliveryAt.UTC(), strategyOutcomeEvent, seed.ClaimSHA256, verified); err != nil {
		return nil, err
	}
	return state, nil
}

func verifyStrategyOutcome(seed strategySeed, authority Policy, recoveryPolicy submitter.Policy,
	accounting string, deliveryAt time.Time, readOutcome func(strategySeed, string, time.Time) (WalletStrategyOutcome, error),
) (strategyOutcome, error) {
	if !validHexDigest(accounting) || deliveryAt.IsZero() || readOutcome == nil {
		return strategyOutcome{}, errors.New("strategy outcome requires an exact accounting reference and delivery time")
	}
	policyHash, err := strategyRecoveryPolicyHash(recoveryPolicy)
	if err != nil {
		return strategyOutcome{}, err
	}
	value, err := readOutcome(seed, accounting, deliveryAt)
	if err != nil {
		return strategyOutcome{}, err
	}
	binding, _, _, err := walletInventoryBinding(authority)
	if err != nil {
		return strategyOutcome{}, err
	}
	request, err := ReadClaimedPaperRequest(seed.ClaimPath, authority)
	if err != nil {
		return strategyOutcome{}, err
	}
	if value.WalletBindingSHA256 != binding || value.PreviousHeadSHA256 != seed.OpeningSHA256 || value.ClaimSHA256 != seed.ClaimSHA256 ||
		value.ActionID != request.ActionID || value.Amounts.AccountingSHA256 != accounting || value.TerminalAt.IsZero() ||
		value.KnownAt.IsZero() || value.TerminalAt.After(value.KnownAt) || value.KnownAt.After(deliveryAt) {
		return strategyOutcome{}, errors.New("strategy outcome differs from original wallet claim or knowledge chronology")
	}
	return strategyOutcome{RecoveryPolicySHA256: policyHash, Outcome: value}, nil
}
