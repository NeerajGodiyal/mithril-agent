package policyauthority

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/submitter"
)

const strategyAcquisitionRetirementEvent = "wallet.strategy-acquisition-retired-v1"

type strategyAcquisitionRetirement struct {
	StrategyPath            string `json:"strategy_path"`
	WalletHeadSHA256        string `json:"wallet_head_sha256"`
	AcquisitionPath         string `json:"acquisition_path"`
	IntentSHA256            string `json:"intent_sha256"`
	AcquisitionSHA256       string `json:"acquisition_sha256,omitempty"`
	AcquisitionRecordSHA256 string `json:"acquisition_record_sha256,omitempty"`
	Policy                  Policy `json:"policy"`
}

// RetireStrategyAcquisition records expiry of an uncommitted opportunity. It
// preserves all original evidence and changes no wallet balance, reservation,
// spending limit or strategy risk state. Committed decisions cannot be retired.
func RetireStrategyAcquisition(strategyPath, walletPath, acquisitionPath string,
	originalAuthority Policy, originalRecovery submitter.Policy, nextPolicy Policy, now time.Time,
	histories ...submitter.Policy,
) (result StrategyJournalStatus, err error) {
	records, err := journal.ReadRecords(strategyPath)
	if err != nil {
		return result, err
	}
	state, _, err := replayStrategyJournal(records, originalAuthority, originalRecovery, now, strategyOutcomeReader(originalAuthority, originalRecovery), histories...)
	if err != nil {
		return result, err
	}
	var seed strategySeed
	if err := decodeStrategyPayload(records[0].Payload, &seed); err != nil {
		return result, err
	}
	if seed.WalletPath != walletPath {
		return result, errors.New("retirement wallet differs from original strategy")
	}
	for _, record := range records {
		if record.Type != strategyAcquisitionRetirementEvent {
			continue
		}
		var prior strategyAcquisitionRetirement
		if err := decodeStrategyPayload(record.Payload, &prior); err != nil {
			return result, err
		}
		if prior.AcquisitionPath != acquisitionPath {
			continue
		}
		want, _, err := paperRequestHashes(nextPolicy, signer.Request{})
		if err != nil {
			return result, err
		}
		got, _, err := paperRequestHashes(prior.Policy, signer.Request{})
		if err != nil || want != got {
			return result, errors.New("retirement retry changed protected policy")
		}
		return ReadStrategyJournalStatus(strategyPath, originalAuthority, originalRecovery, now, histories...)
	}
	if state.Pending() {
		return result, errors.New("committed strategy work cannot be retired")
	}
	wallet, err := ReadWalletInventory(walletPath, originalAuthority)
	if err != nil {
		return result, err
	}
	expected, err := strategyAcquisitionPath(walletPath, wallet.HeadSHA256, records)
	if err != nil || acquisitionPath != expected {
		return result, errors.New("retirement differs from current acquisition generation")
	}
	intentRecords, err := journal.ReadRecords(acquisitionPath + ".intent.jsonl")
	if err != nil {
		return result, err
	}
	intent, err := readStrategyRetirementIntent(intentRecords)
	if err != nil {
		return result, err
	}
	var source []journal.Record
	for i, record := range records {
		if record.Hash == intent.StrategyHeadSHA256 {
			source = records[:i+1]
			break
		}
	}
	if len(source) == 0 {
		return result, errors.New("retirement original observation is unavailable")
	}
	_, ready, err := replayStrategyJournal(source, originalAuthority, originalRecovery, now, strategyOutcomeReader(originalAuthority, originalRecovery), histories...)
	if err != nil {
		return result, err
	}
	value := strategyAcquisitionRetirement{StrategyPath: strategyPath, WalletHeadSHA256: wallet.HeadSHA256, AcquisitionPath: acquisitionPath, IntentSHA256: intentRecords[0].Hash, Policy: nextPolicy}
	acquiredRecords, err := optionalStrategyRecords(acquisitionPath)
	if err != nil {
		return result, err
	}
	if len(acquiredRecords) != 0 {
		acquired, err := proposalcheck.ReadAcquisition(acquisitionPath, acquiredRecords[0].At, time.Duration(intent.MaxAcquisitionAgeNS))
		if err != nil {
			return result, err
		}
		value.AcquisitionSHA256, value.AcquisitionRecordSHA256 = acquired.SHA256, acquiredRecords[0].Hash
	}
	last := records[len(records)-1]
	encoded, err := json.Marshal(value)
	if err != nil {
		return result, err
	}
	event := journal.Record{At: now.UTC(), Type: strategyAcquisitionRetirementEvent, ActionID: seed.ClaimSHA256, PrevHash: last.Hash, Payload: encoded}
	if err := verifyStrategyAcquisitionRetirement(seed, originalAuthority, records, state,
		map[string]shadow.AccountedDecision{intent.StrategyHeadSHA256: ready}, event); err != nil {
		return result, err
	}
	// Acquisition already holds this outer lock through Commit/Prepare. Never
	// replay while holding it: historical readers take their own shared locks.
	intents, err := journal.OpenStrict(acquisitionPath + ".intent.jsonl")
	if err != nil {
		return result, err
	}
	closed := false
	defer func() {
		if !closed {
			if closeErr := intents.Close(); closeErr != nil {
				result = StrategyJournalStatus{}
				err = errors.Join(err, closeErr)
			}
		}
	}()
	current := intents.Records()
	if len(current) != 1 || current[0].Hash != value.IntentSHA256 {
		return result, errors.New("retirement intent changed")
	}
	currentAcquired, err := optionalStrategyRecords(acquisitionPath)
	if err != nil {
		return result, err
	}
	if len(currentAcquired) != len(acquiredRecords) || len(currentAcquired) > 0 && currentAcquired[0].Hash != value.AcquisitionRecordSHA256 {
		return result, errors.New("retirement acquisition changed")
	}
	_, err = updateWalletInventory(walletPath, originalAuthority, now, func(_ *journal.Store, current WalletInventory) error {
		if current.HeadSHA256 != value.WalletHeadSHA256 || current.PendingSHA256 != "" {
			return errors.New("retirement wallet changed or is pending")
		}
		claims, err := optionalStrategyRecords(walletPath + ".claim-" + current.HeadSHA256 + ".jsonl")
		if err != nil {
			return err
		}
		if len(claims) != 0 {
			return errors.New("retirement cannot discard an existing claim")
		}
		return appendStrategyEvent(strategyPath, records, event, value)
	})
	if err != nil {
		return result, err
	}
	// Close before the public reader revalidates the retired intent.
	closeErr := intents.Close()
	closed = true
	if closeErr != nil {
		return result, closeErr
	}
	return ReadStrategyJournalStatus(strategyPath, originalAuthority, originalRecovery, now, histories...)
}

func strategyAcquisitionPath(walletPath, walletHead string, records []journal.Record) (string, error) {
	base := walletPath + ".claim-" + walletHead + ".jsonl.acquisition.jsonl"
	path := base
	for _, record := range records {
		if record.Type == strategyCancellationEvent {
			var canceled strategyCancellation
			if err := decodeStrategyPayload(record.Payload, &canceled); err != nil {
				return "", err
			}
			if canceled.WalletHeadSHA256 == walletHead {
				path = base + ".after-" + record.Hash
			}
			continue
		}
		if record.Type != strategyAcquisitionRetirementEvent {
			continue
		}
		var value strategyAcquisitionRetirement
		if err := decodeStrategyPayload(record.Payload, &value); err != nil {
			return "", err
		}
		if value.WalletHeadSHA256 == walletHead {
			path = base + ".after-" + record.Hash
		}
	}
	return path, nil
}

func optionalStrategyRecords(path string) ([]journal.Record, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return journal.ReadRecords(path)
}

func readStrategyRetirementIntent(records []journal.Record) (strategyAcquisitionIntent, error) {
	var intent strategyAcquisitionIntent
	if len(records) != 1 || records[0].Type != strategyAcquisitionEvent {
		return intent, errors.New("retirement requires one original acquisition intent")
	}
	if err := decodeStrategyPayload(records[0].Payload, &intent); err != nil {
		return intent, err
	}
	encoded, err := json.Marshal(intent)
	if err != nil || !bytes.Equal(encoded, records[0].Payload) || records[0].ActionID != intent.StrategyHeadSHA256 {
		return intent, errors.New("retirement intent is not canonical")
	}
	return intent, nil
}

func verifyStrategyAcquisitionRetirement(seed strategySeed, authority Policy, prior []journal.Record, state *shadow.AccountedStrategy,
	observations map[string]shadow.AccountedDecision, record journal.Record,
) error {
	var value strategyAcquisitionRetirement
	if err := decodeStrategyPayload(record.Payload, &value); err != nil {
		return err
	}
	if len(prior) == 0 || record.PrevHash != prior[len(prior)-1].Hash || record.At.Before(prior[len(prior)-1].At) || state.Pending() {
		return errors.New("retirement requires chronological uncommitted strategy work")
	}
	expected, err := strategyAcquisitionPath(seed.WalletPath, value.WalletHeadSHA256, prior)
	if err != nil || value.AcquisitionPath != expected || !filepath.IsAbs(expected) || filepath.Clean(expected) != expected {
		return errors.New("retirement acquisition generation is invalid")
	}
	intents, err := journal.ReadRecords(value.AcquisitionPath + ".intent.jsonl")
	if err != nil {
		return err
	}
	intent, err := readStrategyRetirementIntent(intents)
	if err != nil {
		return err
	}
	ready, ok := observations[intent.StrategyHeadSHA256]
	var observation journal.Record
	for _, earlier := range prior {
		if earlier.Hash == intent.StrategyHeadSHA256 {
			observation = earlier
		}
		if earlier.Type == strategyDecisionEvent {
			var decision strategyDecision
			if err := decodeStrategyPayload(earlier.Payload, &decision); err != nil {
				return err
			}
			if decision.AcquisitionPath == value.AcquisitionPath || value.AcquisitionSHA256 != "" && decision.AcquisitionSHA256 == value.AcquisitionSHA256 {
				return errors.New("committed acquisition cannot be retired")
			}
		}
	}
	policySHA, _, err := paperRequestHashes(value.Policy, signer.Request{})
	if err != nil {
		return err
	}
	binding, owner, mint, err := walletInventoryBinding(authority)
	if err != nil {
		return err
	}
	nextBinding, _, _, err := walletInventoryBinding(value.Policy)
	if err != nil || nextBinding != binding {
		return errors.New("retirement changed protected wallet or providers")
	}
	if !ok || !ready.ReadyForQuote || observation.Type != strategyObservationEvent || intent.StrategyPath == "" ||
		intent.StrategyPath != value.StrategyPath || !filepath.IsAbs(value.StrategyPath) || filepath.Clean(value.StrategyPath) != value.StrategyPath || intent.WalletHeadSHA256 != value.WalletHeadSHA256 ||
		intents[0].Hash != value.IntentSHA256 || intent.PolicySHA256 != policySHA || intents[0].At.Before(observation.At) || intents[0].At.After(record.At) ||
		intent.MaxDecisionAgeNS <= 0 || seed.Policy.Adaptive == nil || time.Duration(intent.MaxDecisionAgeNS) > time.Duration(seed.Policy.Adaptive.MaxObservationGapSeconds)*time.Second || intent.MaxAcquisitionAgeNS <= 0 {
		return errors.New("retirement differs from original opportunity")
	}
	route := value.Policy.TransactionPolicy.Jupiter
	request := intent.Request
	destination := ""
	if route.NativeInput() {
		destination, err = orcaswap.AssociatedTokenAddress(route.Owner, route.OutputMint)
		if err != nil {
			return err
		}
	}
	if request.Taker != seed.Policy.Observe || request.InputMint != route.InputMint || request.OutputMint != route.OutputMint ||
		request.DestinationTokenAccount != destination || request.InputAmount != ready.InputAmount || route.NativeInput() != ready.Sell || request.InputAmount > route.MaxInputAmount ||
		request.SlippageBPS != min(seed.Policy.SlippageBPS, route.MaxSlippageBPS) || route.MaxFeeLamports > seed.Policy.FeeLamports {
		return errors.New("retirement request differs from protected opportunity")
	}
	expires := observation.At.Add(time.Duration(intent.MaxDecisionAgeNS))
	acquiredRecords, err := optionalStrategyRecords(value.AcquisitionPath)
	if err != nil {
		return err
	}
	if len(acquiredRecords) == 0 {
		if value.AcquisitionSHA256 != "" || value.AcquisitionRecordSHA256 != "" {
			return errors.New("retired acquisition evidence is missing")
		}
	} else {
		acquired, err := proposalcheck.ReadAcquisition(value.AcquisitionPath, acquiredRecords[0].At, time.Duration(intent.MaxAcquisitionAgeNS))
		if err != nil {
			return err
		}
		if acquired.SHA256 != value.AcquisitionSHA256 || acquiredRecords[0].Hash != value.AcquisitionRecordSHA256 || acquired.ReceivedAt.Before(intents[0].At) || acquiredRecords[0].At.After(record.At) || acquired.Candidate.Request != request {
			return errors.New("retired acquisition differs from original receipt")
		}
		if _, _, err := proposalcheck.ValidateCandidateMaterial(*route, acquired.Candidate); err != nil {
			return err
		}
		if deadline := acquired.ReceivedAt.Add(time.Duration(intent.MaxAcquisitionAgeNS)); deadline.Before(expires) {
			expires = deadline
		}
	}
	if !record.At.After(expires) {
		return errors.New("uncommitted acquisition has not expired")
	}
	walletRecords, err := journal.ReadRecords(seed.WalletPath)
	if err != nil {
		return err
	}
	var walletPrefix []journal.Record
	for i, earlier := range walletRecords {
		if earlier.Hash == value.WalletHeadSHA256 {
			walletPrefix = walletRecords[:i+1]
			break
		}
	}
	if len(walletPrefix) == 0 || walletPrefix[len(walletPrefix)-1].At.After(record.At) {
		return errors.New("retirement wallet prefix is unavailable or future")
	}
	wallet, err := openingWalletInventory(walletPrefix, authority, binding, owner, mint)
	if err != nil {
		return err
	}
	if wallet.PendingSHA256 != "" || wallet.NativeLamports != state.Ledger().BaseUnits || wallet.TokenUnits != state.Ledger().QuoteUnits {
		return errors.New("retirement wallet is pending or differs from strategy")
	}
	claims, err := optionalStrategyRecords(seed.WalletPath + ".claim-" + value.WalletHeadSHA256 + ".jsonl")
	if err != nil {
		return err
	}
	if len(claims) > 0 && !claims[0].At.After(record.At) {
		return errors.New("retirement conflicts with an existing claim")
	}
	return nil
}

func strategyAcquisitionRetired(records []journal.Record, path, digest string) (bool, error) {
	for _, record := range records {
		if record.Type == strategyCancellationEvent {
			var canceled strategyCancellation
			if err := decodeStrategyPayload(record.Payload, &canceled); err != nil {
				return false, err
			}
			if canceled.AcquisitionPath == path || digest != "" && canceled.AcquisitionSHA256 == digest {
				return true, nil
			}
			continue
		}
		if record.Type != strategyAcquisitionRetirementEvent {
			continue
		}
		var value strategyAcquisitionRetirement
		if err := decodeStrategyPayload(record.Payload, &value); err != nil {
			return false, err
		}
		if value.AcquisitionPath == path || digest != "" && value.AcquisitionSHA256 == digest {
			return true, nil
		}
	}
	return false, nil
}
