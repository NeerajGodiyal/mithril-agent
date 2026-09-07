package policyauthority

import (
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/submitter"
)

const strategyCancellationEvent = "wallet.strategy-decision-canceled-v1"

type strategyCancellation struct {
	DecisionSHA256    string `json:"decision_sha256"`
	WalletHeadSHA256  string `json:"wallet_head_sha256"`
	AcquisitionPath   string `json:"acquisition_path"`
	AcquisitionSHA256 string `json:"acquisition_sha256"`
}

// CancelStrategyDecision records expiry of an exact unclaimed continuation.
// It preserves all evidence and balances; it neither releases a claim nor grants
// permission to sign. Exact retries return the current state, including later work.
func CancelStrategyDecision(path, walletPath string, authority Policy, originalRecovery submitter.Policy,
	decisionSHA string, now time.Time, histories ...submitter.Policy,
) (StrategyJournalStatus, error) {
	if !validHexDigest(decisionSHA) || now.IsZero() {
		return StrategyJournalStatus{}, errors.New("cancellation requires exact decision identity and time")
	}
	records, err := journal.ReadRecords(path)
	if err != nil {
		return StrategyJournalStatus{}, err
	}
	state, _, err := replayStrategyJournal(records, authority, originalRecovery, now, strategyOutcomeReader(authority, originalRecovery), histories...)
	if err != nil {
		return StrategyJournalStatus{}, err
	}
	var seed strategySeed
	if err := decodeStrategyPayload(records[0].Payload, &seed); err != nil {
		return StrategyJournalStatus{}, err
	}
	if seed.WalletPath != walletPath {
		return StrategyJournalStatus{}, errors.New("cancellation wallet differs from original strategy")
	}
	for _, record := range records {
		if record.Type != strategyCancellationEvent {
			continue
		}
		var value strategyCancellation
		if err := decodeStrategyPayload(record.Payload, &value); err != nil {
			return StrategyJournalStatus{}, err
		}
		if value.DecisionSHA256 == decisionSHA {
			return ReadStrategyJournalStatus(path, authority, originalRecovery, now, histories...)
		}
	}
	_, decision, err := strategyCancelableDecision(records, decisionSHA)
	if err != nil {
		return StrategyJournalStatus{}, err
	}
	value := strategyCancellation{decisionSHA, decision.WalletHeadSHA256, decision.AcquisitionPath, decision.AcquisitionSHA256}
	last := records[len(records)-1]
	// Verify outside the writer locks: replay reads historical wallet prefixes.
	if err := verifyStrategyCancellation(seed, authority, records, state, value, now); err != nil {
		return StrategyJournalStatus{}, err
	}
	_, err = updateWalletInventory(walletPath, authority, now, func(_ *journal.Store, current WalletInventory) error {
		if current.HeadSHA256 != value.WalletHeadSHA256 || current.PendingSHA256 != "" {
			return errors.New("cancellation wallet changed or is pending")
		}
		claims, err := optionalStrategyRecords(walletPath + ".claim-" + current.HeadSHA256 + ".jsonl")
		if err != nil {
			return err
		}
		if len(claims) != 0 {
			return errors.New("cancellation cannot discard an existing claim")
		}
		return appendStrategyEvent(path, records, journal.Record{At: now.UTC(), Type: strategyCancellationEvent, ActionID: seed.ClaimSHA256, PrevHash: last.Hash}, value)
	})
	if err != nil {
		return StrategyJournalStatus{}, err
	}
	return ReadStrategyJournalStatus(path, authority, originalRecovery, now, histories...)
}

func strategyCancelableDecision(records []journal.Record, digest string) (journal.Record, strategyDecision, error) {
	var pending journal.Record
	for _, record := range records {
		switch record.Type {
		case strategyDecisionEvent:
			pending = record
		case strategyContinuationEvent, strategyCancellationEvent:
			pending = journal.Record{}
		}
	}
	var value strategyDecision
	if pending.Hash == "" || pending.Hash != digest {
		return pending, value, errors.New("cancellation does not match unresolved continuation decision")
	}
	if err := decodeStrategyPayload(pending.Payload, &value); err != nil {
		return pending, value, err
	}
	return pending, value, nil
}

func verifyStrategyCancellation(seed strategySeed, authority Policy, prior []journal.Record, state *shadow.AccountedStrategy,
	value strategyCancellation, at time.Time,
) error {
	if len(prior) == 0 || at.Before(prior[len(prior)-1].At) || !state.Pending() {
		return errors.New("cancellation requires chronological pending continuation")
	}
	pending, decision, err := strategyCancelableDecision(prior, value.DecisionSHA256)
	if err != nil {
		return err
	}
	if value.WalletHeadSHA256 != decision.WalletHeadSHA256 || value.AcquisitionPath != decision.AcquisitionPath || value.AcquisitionSHA256 != decision.AcquisitionSHA256 {
		return errors.New("cancellation differs from original decision evidence")
	}
	_, age, err := strategyManagedDecisionAge(seed, decision, pending.At, "")
	if err != nil {
		return err
	}
	if age == 0 {
		age = int64(time.Duration(seed.Policy.Adaptive.MaxObservationGapSeconds) * time.Second)
	}
	expiry := decision.AcquiredAt.Add(time.Duration(decision.MaxAgeNS))
	if age > 0 && decision.ObservationAt.Add(time.Duration(age)).Before(expiry) {
		expiry = decision.ObservationAt.Add(time.Duration(age))
	}
	if !at.After(expiry) {
		return errors.New("cancellation requires original decision expiry")
	}
	bind, owner, mint, err := walletInventoryBinding(authority)
	if err != nil {
		return err
	}
	walletRecords, err := journal.ReadRecords(seed.WalletPath)
	if err != nil {
		return err
	}
	if _, err := openingWalletInventory(walletRecords, authority, bind, owner, mint); err != nil {
		return err
	}
	var prefix []journal.Record
	for i, record := range walletRecords {
		if record.Hash == decision.WalletHeadSHA256 {
			prefix = walletRecords[:i+1]
			continue
		}
		if prefix != nil && !record.At.After(at) {
			return errors.New("cancellation wallet already advanced")
		}
	}
	if len(prefix) == 0 {
		return errors.New("cancellation wallet prefix is unavailable")
	}
	wallet, err := openingWalletInventory(prefix, authority, bind, owner, mint)
	if err != nil {
		return err
	}
	ledger := state.Ledger()
	if wallet.PendingSHA256 != "" || ledger.BaseUnits != wallet.NativeLamports || ledger.QuoteUnits != wallet.TokenUnits {
		return errors.New("cancellation differs from available accounted balances")
	}
	claims, err := optionalStrategyRecords(seed.WalletPath + ".claim-" + decision.WalletHeadSHA256 + ".jsonl")
	if err != nil {
		return err
	}
	// Later generations reuse this wallet-head claim path. They cannot erase
	// the historical fact that no claim existed at cancellation delivery.
	if len(claims) > 0 && !claims[0].At.After(at) {
		return errors.New("cancellation cannot discard an existing claim")
	}
	acquired, err := proposalcheck.ReadAcquisition(decision.AcquisitionPath, pending.At, time.Duration(decision.MaxAgeNS))
	if err != nil {
		return err
	}
	quote := acquired.Candidate.Quote
	return state.CancelPendingDecision(shadow.Quote{InputAmount: quote.InputAmount, EstimatedOutput: quote.EstimatedOutput, MinimumOutput: quote.MinimumOutput, ReceivedAt: acquired.ReceivedAt}, decision.Policy.TransactionPolicy.Jupiter.NativeInput(), decision.ObservationAt, at)
}
