package policyauthority

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/submitter"
)

const (
	strategySeedEvent        = "wallet.strategy-seed-v1"
	strategyObservationEvent = "wallet.strategy-observation-v1"
)

type strategySeed struct {
	WalletPath        string                          `json:"wallet_path"`
	ClaimPath         string                          `json:"claim_path"`
	OpeningSHA256     string                          `json:"opening_sha256"`
	ReservationSHA256 string                          `json:"reservation_sha256"`
	ClaimSHA256       string                          `json:"claim_sha256"`
	Policy            shadow.Policy                   `json:"policy"`
	Ticks             []shadow.Tick                   `json:"ticks"`
	Bounds            proposalcheck.PaperIntentBounds `json:"bounds"`
}

type strategyObservation struct {
	Primary        pricetrigger.Sample `json:"primary"`
	Secondary      pricetrigger.Sample `json:"secondary"`
	QuotePrimary   pricetrigger.Sample `json:"quote_primary"`
	QuoteSecondary pricetrigger.Sample `json:"quote_secondary"`
}

// InitializeStrategyJournal persists the original first-action replay and its
// reserved wallet opening. Repeats retain the original journal and chronology.
// The journal supports observations and alternating acquired decisions and exact
// accounted outcomes. Initialization does not authorize or submit.
func InitializeStrategyJournal(path, walletPath, claimPath string, authority Policy,
	recoveryPolicy submitter.Policy,
	policy shadow.Policy, ticks []shadow.Tick, bounds proposalcheck.PaperIntentBounds, now time.Time,
	historicalRecoveryPolicies ...submitter.Policy,
) (result *shadow.AccountedStrategy, err error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == walletPath || path == claimPath {
		return nil, errors.New("strategy journal requires a distinct absolute clean path")
	}
	seed := strategySeed{WalletPath: walletPath, ClaimPath: claimPath, Policy: policy, Ticks: ticks, Bounds: bounds}
	if _, err := verifyStrategySeed(&seed, authority, now, false); err != nil {
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
	raw, err := json.Marshal(seed)
	if err != nil {
		return nil, err
	}
	if records := store.Records(); len(records) != 0 {
		if !bytes.Equal(records[0].Payload, raw) {
			return nil, errors.New("strategy journal has a different original seed")
		}
	} else {
		if _, err := verifyStrategySeed(&seed, authority, now, true); err != nil {
			return nil, err
		}
		if _, err := store.Append(now.UTC(), strategySeedEvent, seed.ClaimSHA256, seed); err != nil {
			return nil, err
		}
	}
	result, _, err = replayStrategyJournal(store.Records(), authority, recoveryPolicy, now, strategyOutcomeReader(authority, recoveryPolicy), historicalRecoveryPolicies...)
	return result, err
}

// ReadStrategyJournal reconstructs history without writes or refreshing balances.
// Returned state is detached; changing it does not change the durable journal.
func ReadStrategyJournal(path string, authority Policy, recoveryPolicy submitter.Policy, now time.Time, historicalRecoveryPolicies ...submitter.Policy) (*shadow.AccountedStrategy, error) {
	status, err := ReadStrategyJournalStatus(path, authority, recoveryPolicy, now, historicalRecoveryPolicies...)
	return status.State, err
}

// StrategyJournalStatus binds the replayed state and pending decision identity
// to one verified journal snapshot. The original first action has no decision
// record; its pending state is available through State.Pending.
type StrategyJournalStatus struct {
	State                 *shadow.AccountedStrategy
	HeadSHA256            string
	PendingDecisionSHA256 string
}

// ReadStrategyJournalStatus verifies the entire history before exposing its
// current identities. It does not create, repair, or update any journal.
func ReadStrategyJournalStatus(path string, authority Policy, recoveryPolicy submitter.Policy, now time.Time, historicalRecoveryPolicies ...submitter.Policy) (StrategyJournalStatus, error) {
	records, err := journal.ReadRecords(path)
	if err != nil {
		return StrategyJournalStatus{}, err
	}
	state, _, err := replayStrategyJournal(records, authority, recoveryPolicy, now, strategyOutcomeReader(authority, recoveryPolicy), historicalRecoveryPolicies...)
	if err != nil {
		return StrategyJournalStatus{}, err
	}
	status := StrategyJournalStatus{State: state, HeadSHA256: records[len(records)-1].Hash}
	for _, record := range records {
		switch record.Type {
		case strategyDecisionEvent:
			status.PendingDecisionSHA256 = record.Hash
		case strategyOutcomeEvent, strategyContinuationEvent, strategyCancellationEvent:
			status.PendingDecisionSHA256 = ""
		}
	}
	return status, nil
}

// ObserveStrategyJournal verifies and persists one chronological observation.
// An exact repeat of the last observation is read-only. Unresolved decisions
// remain pending regardless of the original paper settlement deadline.
func ObserveStrategyJournal(path string, authority Policy, recoveryPolicy submitter.Policy, at time.Time,
	primary, secondary, quotePrimary, quoteSecondary pricetrigger.Sample,
	historicalRecoveryPolicies ...submitter.Policy,
) (decision shadow.AccountedDecision, err error) {
	return observeStrategyJournal(path, authority, recoveryPolicy, at, primary, secondary, quotePrimary, quoteSecondary, strategyOutcomeReader(authority, recoveryPolicy), historicalRecoveryPolicies...)
}

func observeStrategyJournal(path string, authority Policy, recoveryPolicy submitter.Policy, at time.Time,
	primary, secondary, quotePrimary, quoteSecondary pricetrigger.Sample,
	readOutcome func(strategySeed, string, time.Time) (WalletStrategyOutcome, error),
	historicalRecoveryPolicies ...submitter.Policy,
) (decision shadow.AccountedDecision, err error) {
	// Never create a new journal for an observation, or repair a torn tail.
	if _, err := journal.ReadRecords(path); err != nil {
		return decision, err
	}
	store, err := journal.OpenStrict(path)
	if err != nil {
		return decision, err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			decision = shadow.AccountedDecision{}
			err = errors.Join(err, closeErr)
		}
	}()
	records := store.Records()
	state, previous, err := replayStrategyJournal(records, authority, recoveryPolicy, at, readOutcome, historicalRecoveryPolicies...)
	if err != nil {
		return decision, err
	}
	observation := strategyObservation{primary, secondary, quotePrimary, quoteSecondary}
	raw, err := json.Marshal(observation)
	if err != nil {
		return decision, err
	}
	last := records[len(records)-1]
	if last.Type == strategyObservationEvent && last.At.Equal(at) && bytes.Equal(last.Payload, raw) {
		return previous, nil
	}
	decision, err = state.Observe(at, primary, secondary, quotePrimary, quoteSecondary)
	if err != nil {
		return shadow.AccountedDecision{}, err
	}
	if _, err := store.Append(at.UTC(), strategyObservationEvent, records[0].ActionID, observation); err != nil {
		return shadow.AccountedDecision{}, err
	}
	return decision, nil
}

func replayStrategyJournal(records []journal.Record, authority Policy, recoveryPolicy submitter.Policy, now time.Time,
	readOutcome func(strategySeed, string, time.Time) (WalletStrategyOutcome, error),
	historicalRecoveryPolicies ...submitter.Policy,
) (*shadow.AccountedStrategy, shadow.AccountedDecision, error) {
	// ponytail: each append replays the full journal within its record/byte
	// limits; add verified checkpoints/rotation only when that ceiling matters.
	var decision shadow.AccountedDecision
	if len(records) == 0 || records[0].Type != strategySeedEvent || now.IsZero() {
		return nil, decision, errors.New("strategy journal has no supported seed")
	}
	var seed strategySeed
	if err := decodeStrategyPayload(records[0].Payload, &seed); err != nil {
		return nil, decision, err
	}
	if records[0].ActionID != seed.ClaimSHA256 || records[0].At.After(now) {
		return nil, decision, errors.New("strategy seed identity or time is invalid")
	}
	state, err := verifyStrategySeed(&seed, authority, records[0].At, true)
	if err != nil {
		return nil, decision, err
	}
	seenOutcome := false
	observations := make(map[string]shadow.AccountedDecision)
	for index, record := range records[1:] {
		if record.ActionID != seed.ClaimSHA256 || record.At.After(now) {
			return nil, decision, errors.New("strategy observation identity or time is invalid")
		}
		if record.Type == strategyOutcomeEvent {
			if seenOutcome {
				return nil, decision, errors.New("strategy journal contains more than its first outcome")
			}
			var retained strategyOutcome
			if err := decodeStrategyPayload(record.Payload, &retained); err != nil {
				return nil, decision, err
			}
			verified, err := verifyStrategyOutcome(seed, authority, recoveryPolicy, retained.Outcome.Amounts.AccountingSHA256, record.At, readOutcome)
			if err != nil {
				return nil, decision, err
			}
			if verified != retained {
				return nil, decision, errors.New("strategy outcome differs from original finalized evidence")
			}
			if err := state.ApplyOutcome(retained.Outcome.Amounts, record.At); err != nil {
				return nil, decision, err
			}
			seenOutcome, decision = true, shadow.AccountedDecision{}
			continue
		}
		if record.Type == strategyContinuationEvent {
			var retained strategyContinuation
			if err := decodeStrategyPayload(record.Payload, &retained); err != nil {
				return nil, decision, err
			}
			verified, err := verifyStrategyContinuation(seed, records[:index+1], retained.DecisionSHA256,
				retained.Outcome.Amounts.AccountingSHA256, retained.RecoveryPolicySHA256, record.At, recoveryPolicy, historicalRecoveryPolicies...)
			if err != nil {
				return nil, decision, err
			}
			if verified != retained {
				return nil, decision, errors.New("strategy continuation differs from original finalized evidence")
			}
			if err := state.ApplyOutcome(retained.Outcome.Amounts, record.At); err != nil {
				return nil, decision, err
			}
			decision = shadow.AccountedDecision{}
			continue
		}
		if record.Type == strategyCancellationEvent {
			var value strategyCancellation
			if err := decodeStrategyPayload(record.Payload, &value); err != nil {
				return nil, decision, err
			}
			if err := verifyStrategyCancellation(seed, authority, records[:index+1], state, value, record.At); err != nil {
				return nil, decision, err
			}
			decision = shadow.AccountedDecision{}
			continue
		}
		if record.Type == strategyAcquisitionRetirementEvent {
			if err := verifyStrategyAcquisitionRetirement(seed, authority, records[:index+1], state, observations, record); err != nil {
				return nil, decision, err
			}
			decision = shadow.AccountedDecision{}
			continue
		}
		if record.Type == strategyDecisionEvent {
			if err := verifyStrategyDecision(seed, authority, records[:index+1], state, decision, record); err != nil {
				return nil, decision, err
			}
			decision = shadow.AccountedDecision{}
			continue
		}
		if record.Type != strategyObservationEvent {
			return nil, decision, errors.New("strategy journal contains an unsupported event")
		}
		var observation strategyObservation
		if err := decodeStrategyPayload(record.Payload, &observation); err != nil {
			return nil, decision, err
		}
		decision, err = state.Observe(record.At, observation.Primary, observation.Secondary, observation.QuotePrimary, observation.QuoteSecondary)
		if err != nil {
			return nil, decision, err
		}
		observations[record.Hash] = decision
	}
	return state, decision, nil
}

func decodeStrategyPayload(raw []byte, value any) error {
	if err := strictjson.Decode(raw, value); err != nil {
		return err
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, raw) {
		return errors.New("strategy journal payload is not canonical")
	}
	return nil
}

func verifyStrategySeed(seed *strategySeed, authority Policy, now time.Time, bound bool) (*shadow.AccountedStrategy, error) {
	if now.IsZero() {
		return nil, errors.New("strategy seed requires a knowledge time")
	}
	if !filepath.IsAbs(seed.WalletPath) || filepath.Clean(seed.WalletPath) != seed.WalletPath || !filepath.IsAbs(seed.ClaimPath) || filepath.Clean(seed.ClaimPath) != seed.ClaimPath || seed.WalletPath == seed.ClaimPath {
		return nil, errors.New("strategy dependencies require distinct absolute clean paths")
	}
	request, err := ReadClaimedPaperRequest(seed.ClaimPath, authority)
	if err != nil {
		return nil, err
	}
	claims, err := journal.ReadRecords(seed.ClaimPath)
	if err != nil || len(claims) == 0 {
		return nil, errors.New("strategy original claim is unavailable")
	}
	if len(claims) > 2 || len(claims) == 2 && (claims[1].Type != paperTerminalEvent || claims[1].ActionID != request.ActionID) {
		return nil, errors.New("strategy claim journal has unsupported trailing records")
	}
	if _, err := paperTerminalClaim(claims[:1], authority, request, now); err != nil {
		return nil, err
	}
	if request.JupiterCandidate == nil || authority.TransactionPolicy.Jupiter == nil {
		return nil, errors.New("strategy seed has no retained acquisition candidate")
	}
	intent, err := proposalcheck.CheckPaperIntent(seed.Policy, seed.Ticks, *authority.TransactionPolicy.Jupiter, *request.JupiterCandidate, seed.Bounds)
	if err != nil {
		return nil, err
	}
	claim, err := ValidatePaperRequestIntent(claims[0], authority, request, intent)
	if err != nil {
		return nil, err
	}
	if intent.At.After(claims[0].At) {
		return nil, errors.New("strategy decision postdates original claim")
	}
	binding, owner, mint, err := walletInventoryBinding(authority)
	if err != nil {
		return nil, err
	}
	records, err := journal.ReadRecords(seed.WalletPath)
	if err != nil {
		return nil, err
	}
	if _, err := openingWalletInventory(records, authority, binding, owner, mint); err != nil {
		return nil, err
	}
	if len(records) < 2 || records[1].Type != walletReservationEvent || records[1].At.After(now) {
		return nil, errors.New("strategy requires the original reserved wallet opening")
	}
	if records[0].At.After(seed.Ticks[0].At) || records[1].At.Before(claims[0].At) {
		return nil, errors.New("strategy opening or reservation chronology is invalid")
	}
	var reservation WalletReservation
	if err := strictjson.Decode(records[1].Payload, &reservation); err != nil {
		return nil, err
	}
	if reservation.ClaimSHA256 != claim || records[1].ActionID != request.ActionID || reservation.PreviousHeadSHA256 != records[0].Hash {
		return nil, errors.New("strategy seed differs from first wallet reservation")
	}
	if bound && (seed.OpeningSHA256 != records[0].Hash || seed.ReservationSHA256 != records[1].Hash || seed.ClaimSHA256 != claim) {
		return nil, errors.New("strategy original wallet or claim identity changed")
	}
	state, err := shadow.NewAccountedStrategy(seed.Policy, seed.Ticks)
	if err != nil {
		return nil, err
	}
	ledger := state.Ledger()
	if seed.Policy.Observe != owner || mint != shadow.MainnetMarketQuoteRoute(shadow.MarketSOLUSDC, true).OutputMint || ledger.BaseUnits != reservation.Observation.NativeLamports || ledger.QuoteUnits != reservation.Observation.TokenUnits {
		return nil, errors.New("strategy replay does not match original wallet balances")
	}
	seed.OpeningSHA256, seed.ReservationSHA256, seed.ClaimSHA256 = records[0].Hash, records[1].Hash, claim
	return state, nil
}
