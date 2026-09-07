package policyauthority

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/submitter"
)

const strategyDecisionEvent = "wallet.strategy-decision-v1"

type strategyDecision struct {
	PreviousHeadSHA256  string    `json:"previous_head_sha256"`
	WalletHeadSHA256    string    `json:"wallet_head_sha256"`
	ObservationAt       time.Time `json:"observation_at"`
	AcquisitionPath     string    `json:"acquisition_path"`
	AcquisitionSHA256   string    `json:"acquisition_sha256"`
	AcquiredAt          time.Time `json:"acquired_at"`
	MaxAgeNS            int64     `json:"max_age_ns,string"`
	Policy              Policy    `json:"policy"`
	ManagedIntentSHA256 string    `json:"managed_intent_sha256,omitempty"`
	MaxDecisionAgeNS    int64     `json:"max_decision_age_ns,string,omitempty"`
}

// CommitStrategyJournalDecision persists an acquired quote after exact
// accounting. It neither creates a claim nor authorizes signing. Retried and
// historical reads retain the original acquisition and decision times.
// At is the logical strategy delivery time, not an extension of quote lifetime:
// concrete claim preparation must separately check expiry after its own work.
func CommitStrategyJournalDecision(path string, authority Policy, recoveryPolicy submitter.Policy,
	nextPolicy Policy, acquisitionPath string, maxAge time.Duration, at time.Time,
	historicalRecoveryPolicies ...submitter.Policy,
) (*shadow.AccountedStrategy, error) {
	return commitStrategyJournalDecision(path, authority, recoveryPolicy, nextPolicy, acquisitionPath, maxAge, at, "", historicalRecoveryPolicies...)
}

func commitStrategyJournalDecision(path string, authority Policy, recoveryPolicy submitter.Policy,
	nextPolicy Policy, acquisitionPath string, maxAge time.Duration, at time.Time, expectedHead string,
	historicalRecoveryPolicies ...submitter.Policy,
) (*shadow.AccountedStrategy, error) {
	records, err := journal.ReadRecords(path)
	if err != nil {
		return nil, err
	}
	// Acquisition binds the opportunity selected before the builder call. The
	// same verified head is compared again by the locked append below.
	if expectedHead != "" && (len(records) == 0 || records[len(records)-1].Hash != expectedHead) {
		return nil, errors.New("strategy opportunity changed during acquisition")
	}
	state, ready, err := replayStrategyJournal(records, authority, recoveryPolicy, at, strategyOutcomeReader(authority, recoveryPolicy), historicalRecoveryPolicies...)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.Type != strategyDecisionEvent {
			continue
		}
		var retained strategyDecision
		if err := decodeStrategyPayload(record.Payload, &retained); err != nil {
			return nil, err
		}
		if retained.AcquisitionPath != acquisitionPath {
			continue
		}
		want, err := json.Marshal(nextPolicy)
		if err != nil {
			return nil, err
		}
		got, err := json.Marshal(retained.Policy)
		if err != nil || !bytes.Equal(want, got) || retained.AcquisitionPath != acquisitionPath || retained.MaxAgeNS != int64(maxAge) {
			return nil, errors.New("strategy decision conflicts with retained acquisition or policy")
		}
		return state, nil
	}
	if retired, err := strategyAcquisitionRetired(records, acquisitionPath, ""); err != nil {
		return nil, err
	} else if retired {
		return nil, errors.New("strategy acquisition was retired")
	}
	var seed strategySeed
	if err := decodeStrategyPayload(records[0].Payload, &seed); err != nil {
		return nil, err
	}
	wallet, err := ReadWalletInventory(seed.WalletPath, authority)
	if err != nil {
		return nil, err
	}
	acquired, err := proposalcheck.ReadAcquisition(acquisitionPath, at, maxAge)
	if err != nil {
		return nil, err
	}
	last := records[len(records)-1]
	value := strategyDecision{PreviousHeadSHA256: last.Hash, WalletHeadSHA256: wallet.HeadSHA256,
		ObservationAt: last.At, AcquisitionPath: acquisitionPath, AcquisitionSHA256: acquired.SHA256,
		AcquiredAt: acquired.ReceivedAt, MaxAgeNS: int64(maxAge), Policy: nextPolicy}
	value.ManagedIntentSHA256, value.MaxDecisionAgeNS, err = strategyManagedDecisionAge(seed, value, at, path)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	record := journal.Record{At: at.UTC(), Type: strategyDecisionEvent, ActionID: seed.ClaimSHA256, PrevHash: last.Hash, Payload: raw}
	if err := verifyStrategyDecision(seed, authority, records, state, ready, record); err != nil {
		return nil, err
	}
	// Verification reads the wallet without holding its writer lock. Then take
	// wallet -> strategy locks and require the exact verified heads unchanged;
	// concurrent changes fail closed rather than replaying under inverted locks.
	_, err = updateWalletInventory(seed.WalletPath, authority, at, func(_ *journal.Store, current WalletInventory) error {
		if current.HeadSHA256 != value.WalletHeadSHA256 || current.PendingSHA256 != "" {
			return errors.New("strategy decision wallet head changed or is pending")
		}
		return appendStrategyDecision(path, records, record, value)
	})
	if err != nil {
		return nil, err
	}
	return state, nil
}

func appendStrategyDecision(path string, verified []journal.Record, record journal.Record, value strategyDecision) (err error) {
	return appendStrategyEvent(path, verified, record, value)
}

func appendStrategyEvent(path string, verified []journal.Record, record journal.Record, value any) (err error) {
	store, err := journal.OpenStrict(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	current := store.Records()
	if len(current) != len(verified) || len(current) == 0 || current[len(current)-1].Hash != verified[len(verified)-1].Hash {
		return errors.New("strategy history changed before decision persistence")
	}
	_, err = store.Append(record.At, record.Type, record.ActionID, value)
	return err
}

func verifyStrategyDecision(seed strategySeed, authority Policy, prior []journal.Record, state *shadow.AccountedStrategy,
	ready shadow.AccountedDecision, record journal.Record,
) error {
	var value strategyDecision
	if err := decodeStrategyPayload(record.Payload, &value); err != nil {
		return err
	}
	// Consult the preceding strategy prefix only. Historical valid decisions
	// remain readable; fresh callers cannot reuse a retired path or receipt.
	if retired, err := strategyAcquisitionRetired(prior, value.AcquisitionPath, value.AcquisitionSHA256); err != nil {
		return err
	} else if retired {
		return errors.New("strategy acquisition was retired")
	}
	if len(prior) == 0 || !ready.ReadyForQuote || state.Pending() || value.MaxAgeNS <= 0 ||
		!filepath.IsAbs(value.AcquisitionPath) || filepath.Clean(value.AcquisitionPath) != value.AcquisitionPath ||
		value.AcquisitionPath == seed.WalletPath || value.AcquisitionPath == seed.ClaimPath {
		return errors.New("strategy decision requires a ready observation and protected acquisition")
	}
	last := prior[len(prior)-1]
	if last.Type != strategyObservationEvent || record.PrevHash != last.Hash || value.PreviousHeadSHA256 != last.Hash ||
		!value.ObservationAt.Equal(last.At) || record.At.Before(last.At) {
		return errors.New("strategy decision differs from its exact observation prefix")
	}
	accounting := ""
	for _, previous := range prior {
		if previous.Type == strategyDecisionEvent {
			accounting = ""
		}
		if previous.Type == strategyOutcomeEvent {
			var outcome strategyOutcome
			if err := decodeStrategyPayload(previous.Payload, &outcome); err != nil {
				return err
			}
			accounting = outcome.Outcome.Amounts.AccountingSHA256
		}
		if previous.Type == strategyCancellationEvent {
			var canceled strategyCancellation
			if err := decodeStrategyPayload(previous.Payload, &canceled); err != nil {
				return err
			}
			accounting = canceled.WalletHeadSHA256
		}
		if previous.Type == strategyContinuationEvent {
			var outcome strategyContinuation
			if err := decodeStrategyPayload(previous.Payload, &outcome); err != nil {
				return err
			}
			accounting = outcome.Outcome.Amounts.AccountingSHA256
		}
	}
	if accounting == "" || value.WalletHeadSHA256 != accounting {
		return errors.New("strategy decision is not bound to its delivered accounting head")
	}
	binding, owner, mint, err := walletInventoryBinding(authority)
	if err != nil {
		return err
	}
	nextBinding, _, _, err := walletInventoryBinding(value.Policy)
	if err != nil || nextBinding != binding {
		return errors.New("strategy decision changed protected wallet or providers")
	}
	walletRecords, err := journal.ReadRecords(seed.WalletPath)
	if err != nil {
		return err
	}
	var prefix []journal.Record
	for i, candidate := range walletRecords {
		if candidate.Hash == value.WalletHeadSHA256 {
			prefix = walletRecords[:i+1]
			break
		}
	}
	if len(prefix) == 0 || prefix[len(prefix)-1].At.After(record.At) {
		return errors.New("strategy decision accounting prefix is unavailable or future")
	}
	wallet, err := openingWalletInventory(prefix, authority, binding, owner, mint)
	if err != nil {
		return err
	}
	ledger := state.Ledger()
	if wallet.PendingSHA256 != "" || ledger.BaseUnits != wallet.NativeLamports || ledger.QuoteUnits != wallet.TokenUnits {
		return errors.New("strategy decision balances differ from exact accounted inventory")
	}
	acquired, err := proposalcheck.ReadAcquisition(value.AcquisitionPath, record.At, time.Duration(value.MaxAgeNS))
	if err != nil {
		return err
	}
	if acquired.SHA256 != value.AcquisitionSHA256 || !acquired.ReceivedAt.Equal(value.AcquiredAt) || acquired.ReceivedAt.Before(value.ObservationAt) {
		return errors.New("strategy acquisition differs from original receipt or predates observation")
	}
	if _, age, err := strategyManagedDecisionAge(seed, value, record.At, ""); err != nil {
		return err
	} else if age != 0 {
		if err := checkStrategyClaimTime(seed, value, record.At, record.At, time.Duration(age)); err != nil {
			return err
		}
	}
	route := *value.Policy.TransactionPolicy.Jupiter
	if _, _, err := proposalcheck.ValidateCandidateMaterial(route, acquired.Candidate); err != nil {
		return err
	}
	request := acquired.Candidate.Request
	if route.NativeInput() != ready.Sell || request.Taker != seed.Policy.Observe || request.InputAmount != ready.InputAmount ||
		request.SlippageBPS > seed.Policy.SlippageBPS || route.MaxFeeLamports > seed.Policy.FeeLamports {
		return errors.New("strategy acquisition differs from exact direction, amount or cost bounds")
	}
	quote := acquired.Candidate.Quote
	return state.CommitDecision(shadow.Quote{InputAmount: quote.InputAmount, EstimatedOutput: quote.EstimatedOutput,
		MinimumOutput: quote.MinimumOutput, ReceivedAt: acquired.ReceivedAt}, record.At)
}
