package swaprun

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/bits"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/internal/clockcheck"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/swapbuilder"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const (
	EventStarted              = "swap.started"
	EventObserved             = "swap.observed"
	EventObservationFailed    = "swap.observation_failed"
	EventBuilt                = "swap.built"
	EventSimulated            = "swap.simulated"
	EventSigned               = "swap.signed"
	EventPreSendObserved      = "swap.pre_send_observed"
	EventSendStarted          = "swap.send_started"
	EventSubmitted            = "swap.submitted"
	EventReconciled           = "swap.reconciled"
	EventCanceled             = "swap.canceled"
	EventStatusProjected      = "swap.status_projected"
	EventTerminalAcknowledged = "swap.terminal_acknowledged"

	remainingRecords    = 8
	terminalRecords     = 5
	remainingBytes      = 4 << 20
	recoveryRecords     = 3
	recoveryBytes       = 1 << 20
	reconcileInterval   = 2 * time.Second
	maxSwapNodeLagSlots = 150
	clockEvent          = "clock.accepted"
	clockJournalEvery   = time.Minute
)

var errPriceTriggerNotSatisfied = errors.New("price trigger is not satisfied")

type observationValidationError struct {
	stage   string
	message string
}

func (e *observationValidationError) Error() string { return e.message }

// ObservationFailure returns the bounded policy stage for an observation error.
func ObservationFailure(err error) string {
	var validationErr *observationValidationError
	if errors.As(err, &validationErr) {
		return validationErr.stage
	}
	return ""
}

func observationError(stage, message string) error {
	return &observationValidationError{stage: stage, message: message}
}

type Observer interface {
	Observe(context.Context, string) (agent.NodeObservation, error)
}

type QuoteBuilder interface {
	Quote(context.Context, swapbuilder.Request) (swapbuilder.Result, error)
}

type PriceTriggerEvaluator interface {
	Evaluate(context.Context, pricetrigger.Policy) (pricetrigger.Evidence, error)
	// EvaluateAtSlot binds a slot-bound source, such as an on-chain oracle read
	// through our own node, to a slot already proved to be advancing. Passing
	// zero means no slot was proved and such a source must refuse.
	EvaluateAtSlot(context.Context, pricetrigger.Policy, uint64) (pricetrigger.Evidence, error)
}

type BlockhashProvider interface {
	LatestBlockhash(context.Context, uint64) (solanarpc.LatestBlockhash, error)
	BlockHeight(context.Context) (uint64, error)
}

type PolicyAuthority interface {
	Authorize(context.Context, signer.Request) (riskgrant.Grant, error)
}

type Signer interface {
	Sign(context.Context, signer.Request) (signer.Response, error)
}

type Submitter interface {
	Submit(context.Context, signer.Response, uint64) (txflow.Submission, error)
}

type StopChecker interface {
	NoNewActions() (bool, error)
	StopForTerminal(string, string) error
	TerminalLatch() (string, string, error)
	ClearTerminalForFinalized(string) error
	WithSendBarrier(string, func() error) (bool, error)
	WithRecoverySendBarrier(string, func() error) (bool, error)
}

type Transactor interface {
	VerifyGenesis(context.Context, string) error
	VerifyEvidenceGenesis(context.Context, string) error
	VerifyWhirlpoolDeployment(context.Context, orcaswap.Policy, uint64) error
	VerifyTokenAccountRent(context.Context, uint64) (txflow.RentEvidence, error)
	FeeForMessage(context.Context, []byte, uint64) (txflow.FeeEvidence, error)
	SimulateLegacy(context.Context, []byte, uint64) (txflow.LegacySimulationEvidence, error)
	BlockhashExpired(context.Context, uint64) (bool, error)
	ReconcileSwapExpected(
		context.Context,
		txflow.Submission,
		txflow.ExpectedSwap,
		uint64,
	) (txflow.Reconciliation, error)
}

type Engine struct {
	store           *journal.Store
	observer        Observer
	quotes          QuoteBuilder
	blockhash       BlockhashProvider
	authority       PolicyAuthority
	signer          Signer
	submitter       Submitter
	tx              Transactor
	stop            StopChecker
	now             func() time.Time
	clock           func() (clockcheck.Sample, error)
	releaseCapacity func() error
	priceTrigger    PriceTriggerEvaluator

	// balanceMu guards the last validated balance observation. It is held
	// here rather than threaded through every Result construction site so a
	// cycle that stops early (stopped, degraded, waiting) still leaves the
	// most recent validated reading available, stamped with its own time.
	balanceMu         sync.Mutex
	balanceLamports   uint64
	balanceObservedAt time.Time
}

// recordBalance keeps the balance from an observation that passed validation.
func (e *Engine) recordBalance(observation agent.NodeObservation) {
	e.balanceMu.Lock()
	defer e.balanceMu.Unlock()
	e.balanceLamports = observation.Account.BalanceLamports
	e.balanceObservedAt = observation.Account.ObservedAt.UTC()
}

// LastBalance reports the most recent validated balance observation and when
// it was made. ok is false until the first validated observation, so a
// balance of zero is distinguishable from "never observed".
func (e *Engine) LastBalance() (lamports uint64, observedUnix int64, ok bool) {
	e.balanceMu.Lock()
	defer e.balanceMu.Unlock()
	if e.balanceObservedAt.IsZero() {
		return 0, 0, false
	}
	return e.balanceLamports, e.balanceObservedAt.Unix(), true
}

// Option configures an Engine dependency.
type Option func(*Engine) error

// WithClockSample replaces the kernel clock sampler.
func WithClockSample(sample func() (clockcheck.Sample, error)) Option {
	return func(engine *Engine) error {
		if sample == nil {
			return errors.New("swap clock sample is required")
		}
		engine.clock = sample
		return nil
	}
}

func WithPriceTrigger(evaluator PriceTriggerEvaluator) Option {
	return func(engine *Engine) error {
		if evaluator == nil {
			return errors.New("price trigger evaluator is required")
		}
		engine.priceTrigger = evaluator
		return nil
	}
}

type Result struct {
	ActionID                     string               `json:"action_id,omitempty"`
	Decision                     string               `json:"decision"`
	Reason                       string               `json:"reason,omitempty"`
	InputLamports                uint64               `json:"input_lamports,omitempty"`
	InputAmount                  uint64               `json:"input_amount,omitempty"`
	InputAsset                   string               `json:"input_asset,omitempty"`
	OutputAsset                  string               `json:"output_asset,omitempty"`
	MinimumOutput                uint64               `json:"minimum_output,omitempty"`
	OutputAmount                 uint64               `json:"output_amount,omitempty"`
	Signature                    string               `json:"signature,omitempty"`
	Submitted                    bool                 `json:"submitted,omitempty"`
	Verdict                      string               `json:"verdict,omitempty"`
	Recovered                    bool                 `json:"recovered,omitempty"`
	PendingSinceUnix             int64                `json:"pending_since_unix,omitempty"`
	ReconciliationTimeoutSeconds uint64               `json:"reconciliation_timeout_seconds,omitempty"`
	PriceTrigger                 *pricetrigger.Status `json:"price_trigger,omitempty"`
}

type startedRecord struct {
	ProfileFingerprint      string                 `json:"profile_sha256"`
	ScheduleWindowStartUnix int64                  `json:"schedule_window_start_unix"`
	ScheduleWindowEndUnix   int64                  `json:"schedule_window_end_unix"`
	ObservationSlot         uint64                 `json:"observation_slot"`
	PriceEvidence           *pricetrigger.Evidence `json:"price_evidence,omitempty"`
}

type builtRecord struct {
	MessageBase64           string `json:"message_base64"`
	RecentBlockhash         string `json:"recent_blockhash"`
	BlockhashContextSlot    uint64 `json:"blockhash_context_slot"`
	ObservedBlockHeight     uint64 `json:"observed_block_height"`
	LastValidBlockHeight    uint64 `json:"last_valid_block_height"`
	FeeLamports             uint64 `json:"fee_lamports"`
	FeeMinContextSlot       uint64 `json:"fee_min_context_slot"`
	PrimaryFeeContextSlot   uint64 `json:"primary_fee_context_slot"`
	SecondaryFeeContextSlot uint64 `json:"secondary_fee_context_slot"`
	MinimumOutput           uint64 `json:"minimum_output"`
	OutputAccountCreated    bool   `json:"output_account_created,omitempty"`
	OutputAccountRent       uint64 `json:"output_account_rent_lamports,omitempty"`
	TemporaryAccountRent    uint64 `json:"temporary_account_rent_lamports,omitempty"`
	InputTokenBalance       uint64 `json:"input_token_balance,omitempty"`
	PrimaryInputTokenSlot   uint64 `json:"primary_input_token_slot,omitempty"`
	SecondaryInputTokenSlot uint64 `json:"secondary_input_token_slot,omitempty"`
}

type signedRecord struct {
	Response signer.Response `json:"response"`
}

type sendStartedRecord struct {
	Signature         string                 `json:"signature"`
	TransactionSHA256 string                 `json:"transaction_sha256"`
	PriceEvidence     *pricetrigger.Evidence `json:"price_evidence,omitempty"`
}

type canceledRecord struct {
	Reason string `json:"reason"`
}

type terminalAcknowledgement struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

type statusProjection struct {
	Decision string `json:"decision"`
	Verdict  string `json:"verdict,omitempty"`
}

type observationFailure struct {
	Reason string `json:"reason"`
}

type state struct {
	observations       []agent.NodeObservation
	observationFailed  bool
	started            *startedRecord
	built              *builtRecord
	simulation         *txflow.LegacySimulationEvidence
	signed             *signedRecord
	preSendObservation *agent.NodeObservation
	sendStarted        *sendStartedRecord
	sendStartedAt      time.Time
	submission         *txflow.Submission
	reconciliation     *txflow.Reconciliation
	canceled           *canceledRecord
	terminalAt         time.Time
	statusProjection   *statusProjection
	acknowledgement    *terminalAcknowledgement
	lastAt             time.Time
}

type journalProjection struct {
	actionIDs []string
	states    map[string]*state
}

func New(
	store *journal.Store,
	observer Observer,
	quotes QuoteBuilder,
	blockhash BlockhashProvider,
	authority PolicyAuthority,
	signerClient Signer,
	submitterClient Submitter,
	transactor Transactor,
	stop StopChecker,
	now func() time.Time,
	options ...Option,
) (*Engine, error) {
	if store == nil || observer == nil || quotes == nil || blockhash == nil ||
		authority == nil || signerClient == nil || submitterClient == nil ||
		transactor == nil || stop == nil {
		return nil, errors.New("swap engine dependencies are required")
	}
	if now == nil {
		now = time.Now
	}
	engine := &Engine{
		store: store, observer: observer, quotes: quotes, blockhash: blockhash,
		authority: authority, signer: signerClient, submitter: submitterClient,
		tx: transactor, stop: stop, now: now, clock: clockcheck.SystemSample,
		releaseCapacity: store.ReleaseCapacity,
	}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("swap engine option is nil")
		}
		if err := option(engine); err != nil {
			return nil, err
		}
	}
	return engine, nil
}

func (e *Engine) RunOnce(ctx context.Context, profile Profile) (Result, error) {
	if err := profile.Validate(); err != nil {
		return Result{}, err
	}
	if profile.PriceTrigger != nil && e.priceTrigger == nil {
		return Result{}, errors.New("price trigger evaluator is not configured")
	}
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		return Result{}, err
	}
	projection, err := e.projectJournal()
	if err != nil {
		return Result{}, err
	}
	terminalID, terminalState, err := terminalRequiringControlStop(projection)
	if err != nil {
		return Result{}, err
	}
	if terminalState != nil {
		if err := validateStartedAction(terminalID, terminalState, profile, fingerprint); err != nil ||
			validateStarted(profile, *terminalState.started, e.now().UTC()) != nil {
			return Result{}, errors.New("unacknowledged terminal swap is invalid")
		}
		outcome := terminalOutcome(terminalState.reconciliation.Verdict)
		if err := e.stop.StopForTerminal(terminalID, outcome); err != nil {
			return Result{}, errors.New("restore terminal control state")
		}
		return stateResult(terminalID, profile, terminalState, true), nil
	}
	repairedID, repairedState, repaired, err := e.repairFinalizedTerminal(
		projection, profile, fingerprint, e.now().UTC(),
	)
	if err != nil {
		return Result{}, err
	}
	if repaired {
		return stateResult(repairedID, profile, repairedState, true), nil
	}
	projectedID, projectedState := latestUnprojectedStatus(projection)
	if projectedState != nil {
		if err := validateStartedAction(projectedID, projectedState, profile, fingerprint); err != nil ||
			validateStarted(profile, *projectedState.started, e.now().UTC()) != nil {
			return Result{}, errors.New("unprojected terminal swap is invalid")
		}
		return stateResult(projectedID, profile, projectedState, true), nil
	}
	pendingID, pending, err := pendingState(projection)
	if err != nil {
		return Result{}, err
	}
	if pending == nil {
		if err := e.releaseReservedCapacity(); err != nil {
			return Result{}, errors.New("release completed swap journal reserve")
		}
	}
	blocked, err := e.stop.NoNewActions()
	if err != nil {
		return Result{}, err
	}
	if blocked && pending == nil {
		result := Result{Decision: "stopped", Reason: "Devnet actions are not enabled"}
		if profile.PriceTrigger != nil {
			// Advisory: this only renders stopped-state status and cannot
			// authorize anything, so no proven slot is required.
			_, status, _, priceErr := e.observePrice(ctx, *profile.PriceTrigger, 0)
			if priceErr != nil {
				unavailable := pricetrigger.Unavailable(*profile.PriceTrigger)
				result.PriceTrigger = &unavailable
			} else {
				result.PriceTrigger = &status
			}
		}
		return result, nil
	}
	if blocked && pending.sendStarted == nil {
		return e.cancel(pendingID, profile, pending, "operator stopped before submission")
	}
	if pending != nil && pending.sendStarted != nil {
		if err := validateRecoveredSwap(pendingID, profile, pending, e.now().UTC()); err != nil {
			return Result{}, err
		}
		if err := validateStarted(profile, *pending.started, e.now().UTC()); err != nil {
			return Result{}, err
		}
		if err := e.tx.VerifyEvidenceGenesis(ctx, solana.DevnetGenesisHash); err != nil {
			return Result{}, err
		}
		return e.submitAndReconcile(ctx, pendingID, profile, pending, true)
	}
	now := e.now().UTC()
	if err := e.checkClock(profile, now); err != nil {
		return Result{}, err
	}
	windowStart, windowEnd, err := profile.Window(now)
	if err != nil {
		return Result{}, err
	}
	actionID, current, recovered, err := recoverState(
		projection, profile, fingerprint, windowStart,
	)
	if err != nil {
		return Result{}, err
	}
	if current.started != nil {
		windowStart = current.started.ScheduleWindowStartUnix
		windowEnd = current.started.ScheduleWindowEndUnix
	}
	if current.acknowledgement != nil && current.reconciliation != nil {
		return Result{
			Decision: "waiting",
			Reason:   "the current swap window is already complete",
		}, nil
	}
	if current.reconciliation != nil || current.canceled != nil {
		return stateResult(actionID, profile, current, true), nil
	}
	var startPriceEvidence *pricetrigger.Evidence
	var startPriceStatus *pricetrigger.Status
	if current.started == nil && profile.PriceTrigger != nil {
		// Advisory: decides only whether to keep waiting. The authorizing
		// re-check before signing binds to a proven observation slot.
		evidence, status, evaluatedAt, err := e.observePrice(ctx, *profile.PriceTrigger, 0)
		if err != nil {
			return Result{
				ActionID: actionID, Decision: "degraded",
				Reason: "price evidence is temporarily unavailable", PriceTrigger: &status,
			}, nil
		}
		if err := e.checkClock(profile, evaluatedAt); err != nil {
			return Result{}, err
		}
		observedWindowStart, observedWindowEnd, err := profile.Window(evaluatedAt)
		if err != nil {
			return Result{}, err
		}
		if observedWindowStart != windowStart || observedWindowEnd != windowEnd {
			return Result{
				Decision: "waiting", Reason: "swap schedule window changed during price observation",
				PriceTrigger: &status,
			}, nil
		}
		if !evidence.Triggered {
			return Result{
				ActionID: actionID, Decision: "waiting",
				Reason: "price trigger has not been reached", PriceTrigger: &status,
			}, nil
		}
		startPriceEvidence = &evidence
		startPriceStatus = &status
	}
	if current.sendStarted == nil {
		if err := e.tx.VerifyGenesis(ctx, solana.DevnetGenesisHash); err != nil {
			return Result{}, err
		}
	} else if err := e.tx.VerifyEvidenceGenesis(ctx, solana.DevnetGenesisHash); err != nil {
		return Result{}, err
	}
	if current.started == nil {
		blocked, err := e.stop.NoNewActions()
		if err != nil {
			return Result{}, err
		}
		if blocked {
			return Result{Decision: "stopped", Reason: "Devnet actions are not enabled"}, nil
		}
		observation, err := e.observer.Observe(ctx, profile.owner())
		if err != nil {
			if appendErr := e.recordObservationFailure(actionID, current, "observer_error"); appendErr != nil {
				return Result{}, appendErr
			}
			return Result{}, err
		}
		observationNow := e.now().UTC()
		if err := ValidateObservation(profile, observation, observationNow); err != nil {
			if appendErr := e.recordObservationFailure(actionID, current, "invalid_observation"); appendErr != nil {
				return Result{}, appendErr
			}
			return Result{Decision: "degraded", Reason: err.Error()}, nil
		}
		e.recordBalance(observation)
		ready, err := e.sustainedHealthReady(
			actionID, profile, current, observation, observationNow,
		)
		if err != nil {
			return Result{}, err
		}
		if !ready {
			return Result{
				ActionID: actionID, Decision: "waiting",
				Reason: "Mithril health has not met the sustained observation gate",
			}, nil
		}
		startedAt := e.now().UTC()
		if err := e.checkClock(profile, startedAt); err != nil {
			return Result{}, err
		}
		if err := ValidateObservation(profile, observation, startedAt); err != nil {
			return Result{Decision: "degraded", Reason: err.Error()}, nil
		}
		e.recordBalance(observation)
		observedWindowStart, observedWindowEnd, err := profile.Window(startedAt)
		if err != nil {
			return Result{}, err
		}
		if observedWindowStart != windowStart || observedWindowEnd != windowEnd {
			return Result{
				Decision:     "waiting",
				Reason:       "swap schedule window changed during Mithril observation",
				PriceTrigger: startPriceStatus,
			}, nil
		}
		if profile.PriceTrigger != nil {
			if startPriceEvidence == nil ||
				validateLivePriceEvidence(*profile.PriceTrigger, *startPriceEvidence, startedAt) != nil {
				return Result{}, errors.New("price trigger evidence expired during Mithril observation")
			}
		}
		if err := e.store.EnsureCapacity(remainingRecords, remainingBytes); err != nil {
			return Result{}, err
		}
		started := startedRecord{
			ProfileFingerprint:      fingerprint,
			ScheduleWindowStartUnix: windowStart,
			ScheduleWindowEndUnix:   windowEnd,
			ObservationSlot:         observation.Account.Slot,
			PriceEvidence:           startPriceEvidence,
		}
		if _, err := e.store.Append(startedAt, EventStarted, actionID, started); err != nil {
			return Result{}, err
		}
		current.started = &started
	}
	if current.started.ProfileFingerprint != fingerprint ||
		current.started.ScheduleWindowStartUnix != windowStart ||
		current.started.ScheduleWindowEndUnix != windowEnd {
		return Result{}, errors.New("recovered swap does not match the active profile window")
	}
	if err := validateStarted(profile, *current.started, now); err != nil {
		return Result{}, err
	}
	if current.sendStarted == nil && now.Unix() >= windowEnd {
		return e.cancel(actionID, profile, current, "swap schedule window expired before submission")
	}

	if current.built == nil {
		quote, err := e.quotes.Quote(ctx, swapbuilder.Request{
			Owner: profile.owner(), Pool: profile.pool(),
			InputMint: profile.inputMint(), InputAmount: profile.inputAmount(),
			SlippageBPS: profile.SlippageBPS,
		})
		if err != nil {
			return Result{}, err
		}
		if quote.TradeEnableTimestamp.After(now) {
			return Result{ActionID: actionID, Decision: "waiting", Reason: "Orca pool is not trading yet"}, nil
		}
		quotedIntent, err := validateRouteQuote(profile, quote)
		if err != nil {
			return Result{}, err
		}
		if quotedIntent.InputAmount != profile.inputAmount() ||
			quotedIntent.MinimumOutput != quote.TokenMinOut {
			return Result{}, errors.New("Orca quote does not match the active profile")
		}
		latest, err := e.blockhash.LatestBlockhash(ctx, current.started.ObservationSlot)
		if err != nil {
			return Result{}, err
		}
		if latest.ContextSlot < current.started.ObservationSlot {
			return Result{}, errors.New("swap blockhash predates the node observation")
		}
		height, err := e.blockhash.BlockHeight(ctx)
		if err != nil {
			return Result{}, err
		}
		if height == 0 || height >= latest.LastValidBlockHeight ||
			latest.LastValidBlockHeight-height > profile.MaxBlockHeightWindow {
			return Result{}, errors.New("swap blockhash validity window is outside policy")
		}
		if err := verifyRouteDeployment(ctx, e.tx, profile, latest.ContextSlot); err != nil {
			return Result{}, err
		}
		inputEvidence, err := verifyBuyInput(ctx, e.tx, profile, latest.ContextSlot)
		if err != nil {
			return Result{}, err
		}
		message, err := solana.BuildLegacyMessage(
			profile.owner(), latest.Blockhash, quote.Instructions,
		)
		if err != nil {
			return Result{}, err
		}
		intent, err := decodeRouteMessage(profile, message)
		if err != nil || intent.InputAmount != profile.inputAmount() ||
			intent.MinimumOutput != quote.TokenMinOut {
			return Result{}, errors.New("compiled Orca swap does not match the approved quote")
		}
		var rent txflow.RentEvidence
		if intent.OutputAccountMade || profile.isBuy() {
			rent, err = e.tx.VerifyTokenAccountRent(
				ctx, profile.maxRouteRent(),
			)
			if err != nil {
				return Result{}, err
			}
			if profile.isBuy() && rent.Lamports != intent.RentLamports {
				return Result{}, errors.New("temporary account rent does not match independent evidence")
			}
		}
		fee, err := e.tx.FeeForMessage(ctx, message, latest.ContextSlot)
		if err != nil {
			return Result{}, err
		}
		if fee.Lamports == 0 || fee.Lamports > profile.MaxFeeLamports {
			return Result{}, errors.New("swap fee exceeds the active profile")
		}
		built := builtRecord{
			MessageBase64:           base64.StdEncoding.EncodeToString(message),
			RecentBlockhash:         latest.Blockhash,
			BlockhashContextSlot:    latest.ContextSlot,
			ObservedBlockHeight:     height,
			LastValidBlockHeight:    latest.LastValidBlockHeight,
			FeeLamports:             fee.Lamports,
			FeeMinContextSlot:       fee.MinContextSlot,
			PrimaryFeeContextSlot:   fee.PrimaryContextSlot,
			SecondaryFeeContextSlot: fee.SecondaryContextSlot,
			MinimumOutput:           quote.TokenMinOut,
			OutputAccountCreated:    intent.OutputAccountMade,
			OutputAccountRent:       rent.Lamports,
			InputTokenBalance:       inputEvidence.Amount,
			PrimaryInputTokenSlot:   inputEvidence.PrimaryContextSlot,
			SecondaryInputTokenSlot: inputEvidence.SecondaryContextSlot,
		}
		if profile.isBuy() {
			built.OutputAccountRent = 0
			built.TemporaryAccountRent = rent.Lamports
		}
		if _, err := e.store.Append(e.now().UTC(), EventBuilt, actionID, built); err != nil {
			return Result{}, err
		}
		current.built = &built
	}
	message, err := base64.StdEncoding.Strict().DecodeString(current.built.MessageBase64)
	if err != nil {
		return Result{}, errors.New("recovered swap message is invalid")
	}
	intent, err := decodeRouteMessage(profile, message)
	if err != nil || intent.InputAmount != profile.inputAmount() ||
		intent.MinimumOutput != current.built.MinimumOutput ||
		intent.RecentBlockhash != current.built.RecentBlockhash ||
		intent.OutputAccountMade != current.built.OutputAccountCreated ||
		(intent.OutputAccountMade &&
			(current.built.OutputAccountRent == 0 ||
				current.built.OutputAccountRent > profile.maxRouteRent())) ||
		(!intent.OutputAccountMade && current.built.OutputAccountRent != 0) ||
		(profile.isBuy() && (current.built.TemporaryAccountRent != intent.RentLamports ||
			current.built.TemporaryAccountRent == 0 ||
			current.built.TemporaryAccountRent > profile.maxRouteRent())) ||
		(!profile.isBuy() && current.built.TemporaryAccountRent != 0) {
		return Result{}, errors.New("recovered swap message is outside policy")
	}
	if profile.isBuy() && (current.built.InputTokenBalance < profile.InputTokenAmount ||
		current.built.PrimaryInputTokenSlot < current.built.BlockhashContextSlot ||
		current.built.SecondaryInputTokenSlot < current.built.BlockhashContextSlot) ||
		!profile.isBuy() && (current.built.InputTokenBalance != 0 ||
			current.built.PrimaryInputTokenSlot != 0 || current.built.SecondaryInputTokenSlot != 0) {
		return Result{}, errors.New("recovered input-token evidence is outside policy")
	}
	if current.sendStarted == nil {
		expired, err := e.tx.BlockhashExpired(ctx, current.built.LastValidBlockHeight)
		if err != nil {
			return Result{}, err
		}
		if expired {
			return e.cancel(actionID, profile, current, "transaction blockhash expired before submission")
		}
	}
	if current.simulation == nil {
		simulation, err := e.tx.SimulateLegacy(ctx, message, current.built.BlockhashContextSlot)
		if err != nil {
			return Result{}, err
		}
		if _, err := e.store.Append(e.now().UTC(), EventSimulated, actionID, simulation); err != nil {
			return Result{}, err
		}
		current.simulation = &simulation
	}
	signedBeforeRun := current.signed != nil
	if current.signed == nil {
		if profile.PriceTrigger != nil {
			_, status, err := e.validatePriceForAction(ctx, profile, current)
			if err != nil {
				decision := "degraded"
				reason := "price evidence is temporarily unavailable"
				if errors.Is(err, errPriceTriggerNotSatisfied) {
					decision = "waiting"
					reason = "price trigger or executable minimum is not satisfied"
				}
				return Result{
					ActionID: actionID, Decision: decision, Reason: reason,
					PriceTrigger: &status,
				}, nil
			}
		}
		request := signer.Request{
			Domain: profile.requestDomain(), Cluster: profile.Cluster,
			Profile: profile.Name, ProfileVersion: profile.Version,
			ProfileFingerprint: fingerprint, ActionID: actionID,
			ScheduleWindowStartUnix: windowStart, ScheduleWindowEndUnix: windowEnd,
			MessageBase64:           current.built.MessageBase64,
			BlockhashContextSlot:    current.built.BlockhashContextSlot,
			FeeLamports:             current.built.FeeLamports,
			FeeMinContextSlot:       current.built.FeeMinContextSlot,
			PrimaryFeeContextSlot:   current.built.PrimaryFeeContextSlot,
			SecondaryFeeContextSlot: current.built.SecondaryFeeContextSlot,
			RecentBlockhash:         current.built.RecentBlockhash,
			ObservedBlockHeight:     current.built.ObservedBlockHeight,
			LastValidBlockHeight:    current.built.LastValidBlockHeight,
		}
		grant, err := e.authority.Authorize(ctx, request)
		if err != nil {
			return Result{}, err
		}
		request.RiskGrant = grant
		response, err := e.signer.Sign(ctx, request)
		if err != nil {
			return Result{}, err
		}
		message, err := base64.StdEncoding.Strict().DecodeString(current.built.MessageBase64)
		if err != nil {
			return Result{}, errors.New("swap message is invalid")
		}
		if _, err := validateSwapSignerResponse(
			actionID, profile, *current.built, message, response,
		); err != nil {
			return Result{}, err
		}
		signed := signedRecord{Response: response}
		if _, err := e.store.Append(e.now().UTC(), EventSigned, actionID, signed); err != nil {
			return Result{}, err
		}
		current.signed = &signed
	}
	if signedBeforeRun {
		message, err := base64.StdEncoding.Strict().DecodeString(current.built.MessageBase64)
		if err != nil {
			return Result{}, errors.New("recovered swap message is invalid")
		}
		if _, err := validateSwapSignerResponse(
			actionID, profile, *current.built, message, current.signed.Response,
		); err != nil {
			return Result{}, err
		}
	}
	if current.sendStarted == nil {
		priceEvidence, priceStatus, err := e.validateBeforeSend(ctx, actionID, profile, current)
		if err != nil {
			if priceStatus != nil {
				decision := "degraded"
				reason := "price evidence is temporarily unavailable"
				if errors.Is(err, errPriceTriggerNotSatisfied) {
					decision = "waiting"
					reason = "price trigger or executable minimum is not satisfied"
				}
				return Result{
					ActionID: actionID, Decision: decision, Reason: reason,
					PriceTrigger: priceStatus,
				}, nil
			}
			return Result{}, err
		}
		blocked, err := e.stop.WithSendBarrier(actionID, func() error {
			record := sendStartedRecord{
				Signature:         current.signed.Response.Signature,
				TransactionSHA256: current.signed.Response.TransactionSHA256,
				PriceEvidence:     priceEvidence,
			}
			sentAt := e.now().UTC()
			if _, err := e.store.Append(sentAt, EventSendStarted, actionID, record); err != nil {
				return err
			}
			current.sendStarted = &record
			current.sendStartedAt = sentAt
			return nil
		})
		if err != nil {
			return Result{}, err
		}
		if blocked {
			return Result{ActionID: actionID, Decision: "stopped", Reason: "Devnet actions are not enabled"}, nil
		}
		submission, err := e.submitter.Submit(
			ctx, current.signed.Response, current.built.BlockhashContextSlot,
		)
		if errors.Is(err, submitter.ErrControlBlocked) {
			return pendingResult(actionID, profile, current, txflow.VerdictPending, recovered), nil
		}
		if err != nil {
			return Result{}, err
		}
		if _, err := e.store.Append(e.now().UTC(), EventSubmitted, actionID, submission); err != nil {
			return Result{}, err
		}
		current.submission = &submission
	}
	return e.submitAndReconcile(ctx, actionID, profile, current, recovered)
}

func (e *Engine) validateBeforeSend(
	ctx context.Context,
	actionID string,
	profile Profile,
	current *state,
) (*pricetrigger.Evidence, *pricetrigger.Status, error) {
	observation, err := e.observer.Observe(ctx, profile.owner())
	if err != nil {
		return nil, nil, err
	}
	observedAt := e.now().UTC()
	if err := ValidateObservation(profile, observation, observedAt); err != nil {
		return nil, nil, err
	}
	e.recordBalance(observation)
	if observation.Account.Slot < current.started.ObservationSlot {
		return nil, nil, errors.New("Mithril account observation regressed before submission")
	}
	if err := e.tx.VerifyGenesis(ctx, solana.DevnetGenesisHash); err != nil {
		return nil, nil, err
	}
	minDeploymentSlot := current.built.BlockhashContextSlot
	if observation.Account.Slot > minDeploymentSlot {
		minDeploymentSlot = observation.Account.Slot
	}
	if err := verifyRouteDeployment(ctx, e.tx, profile, minDeploymentSlot); err != nil {
		return nil, nil, err
	}
	if _, err := verifyBuyInput(ctx, e.tx, profile, minDeploymentSlot); err != nil {
		return nil, nil, err
	}
	if err := verifyRouteRent(ctx, e.tx, profile, current.built); err != nil {
		return nil, nil, err
	}
	expired, err := e.tx.BlockhashExpired(ctx, current.built.LastValidBlockHeight)
	if err != nil {
		return nil, nil, err
	}
	if expired {
		return nil, nil, errors.New("transaction blockhash expired before submission")
	}
	var priceEvidence *pricetrigger.Evidence
	var priceStatus *pricetrigger.Status
	authorizedAt := e.now().UTC()
	if profile.PriceTrigger != nil {
		evidence, status, evaluatedAt, priceErr := e.observePrice(
			ctx, *profile.PriceTrigger, current.started.ObservationSlot)
		priceStatus = &status
		if priceErr != nil {
			return nil, priceStatus, priceErr
		}
		authorizedAt = evaluatedAt
		if err := validatePriceEvidenceProgress(*current.started.PriceEvidence, evidence); err != nil {
			return nil, priceStatus, err
		}
		if err := attachExecutableMinimum(profile, current.built.MinimumOutput, priceStatus); err != nil {
			return nil, priceStatus, err
		}
		if !evidence.Triggered || !priceStatus.ExecutableCondition {
			return nil, priceStatus, errPriceTriggerNotSatisfied
		}
		priceEvidence = &evidence
	}
	if err := e.checkClock(profile, authorizedAt); err != nil {
		return nil, nil, err
	}
	if err := ValidateObservation(profile, observation, authorizedAt); err != nil {
		return nil, nil, err
	}
	e.recordBalance(observation)
	windowStart, windowEnd, err := profile.Window(authorizedAt)
	if err != nil {
		return nil, nil, err
	}
	if windowStart != current.started.ScheduleWindowStartUnix ||
		windowEnd != current.started.ScheduleWindowEndUnix {
		return nil, nil, errors.New("swap schedule window changed before submission")
	}
	if err := e.store.EnsureCapacity(terminalRecords, remainingBytes); err != nil {
		return nil, nil, err
	}
	_, err = e.store.Append(authorizedAt, EventPreSendObserved, actionID, observation)
	if err == nil {
		current.preSendObservation = &observation
	}
	return priceEvidence, priceStatus, err
}

func (e *Engine) checkClock(profile Profile, now time.Time) error {
	sample, err := e.clock()
	if err != nil {
		return err
	}
	if err := ValidateClockSample(sample, now, profile.ClockUncertaintyLimit()); err != nil {
		return err
	}
	var previous *clockcheck.Sample
	for _, record := range e.store.Records() {
		if record.Type != clockEvent {
			continue
		}
		var value clockcheck.Sample
		if err := strictjson.Decode(record.Payload, &value); err != nil ||
			validateClockShape(value, clockcheck.MaxUncertaintyCap) != nil {
			return errors.New("journal clock sample is invalid")
		}
		copy := value
		previous = &copy
	}
	if previous != nil {
		if sample.WallTime.Before(previous.WallTime) {
			return errors.New("wall clock moved backward")
		}
		if previous.BootID == sample.BootID {
			if sample.MonotonicNanos <= previous.MonotonicNanos {
				return errors.New("monotonic clock moved backward")
			}
			wallDelta := sample.WallTime.Sub(previous.WallTime)
			wallDeltaNanos := uint64(wallDelta)
			monotonicDeltaNanos := sample.MonotonicNanos - previous.MonotonicNanos
			difference := wallDeltaNanos - monotonicDeltaNanos
			if monotonicDeltaNanos > wallDeltaNanos {
				difference = monotonicDeltaNanos - wallDeltaNanos
			}
			allowed := uint64(clockcheck.MaxOffset) +
				previous.UncertaintyNanos + sample.UncertaintyNanos
			if difference > allowed {
				return errors.New("wall clock stepped relative to the monotonic clock")
			}
			if wallDelta < clockJournalEvery {
				return nil
			}
		}
	}
	_, err = e.store.Append(now, clockEvent, "", sample)
	return err
}

// ValidateClockSample verifies the kernel clock evidence used by swap readiness.
func ValidateClockSample(sample clockcheck.Sample, now time.Time, maxUncertainty time.Duration) error {
	if err := validateClockShape(sample, maxUncertainty); err != nil {
		return err
	}
	wallTime := sample.WallTime.UTC()
	if wallTime.After(now.Add(clockcheck.MaxOffset)) ||
		now.Sub(wallTime) > clockcheck.MaxSampleAge {
		return errors.New("clock sample is stale")
	}
	return nil
}

func validateClockShape(sample clockcheck.Sample, maxUncertainty time.Duration) error {
	if sample.WallTime.IsZero() || len(sample.BootID) != 36 || sample.MonotonicNanos == 0 ||
		sample.OffsetNanos < -int64(clockcheck.MaxOffset) ||
		sample.OffsetNanos > int64(clockcheck.MaxOffset) ||
		sample.UncertaintyNanos > uint64(maxUncertainty) {
		return errors.New("system clock is outside policy")
	}
	return nil
}

func validateStarted(profile Profile, started startedRecord, now time.Time) error {
	window := int64(profile.ScheduleWindowSeconds)
	if started.ObservationSlot == 0 ||
		started.ScheduleWindowStartUnix < profile.ScheduleAnchorUnix ||
		(started.ScheduleWindowStartUnix-profile.ScheduleAnchorUnix)%window != 0 ||
		started.ScheduleWindowEndUnix-started.ScheduleWindowStartUnix != window {
		return errors.New("recovered swap schedule window is invalid")
	}
	if now.Unix() < started.ScheduleWindowStartUnix {
		return errors.New("recovered swap schedule window is in the future")
	}
	if profile.PriceTrigger == nil {
		if started.PriceEvidence != nil {
			return errors.New("unconfigured price evidence is present")
		}
		return nil
	}
	if started.PriceEvidence == nil ||
		started.PriceEvidence.ObservedAt.Unix() < started.ScheduleWindowStartUnix ||
		started.PriceEvidence.ObservedAt.Unix() >= started.ScheduleWindowEndUnix {
		return errors.New("recovered swap price evidence is outside its schedule window")
	}
	if err := validateStoredPriceEvidence(*profile.PriceTrigger, *started.PriceEvidence); err != nil {
		return err
	}
	return nil
}

func validateLivePriceEvidence(
	policy pricetrigger.Policy,
	evidence pricetrigger.Evidence,
	now time.Time,
) error {
	now = now.UTC()
	if evidence.ObservedAt.IsZero() || evidence.ObservedAt.After(now.Add(time.Second)) ||
		now.Sub(evidence.ObservedAt) > time.Duration(policy.MaxAgeSeconds)*time.Second {
		return errors.New("price trigger evidence is stale")
	}
	evaluated, err := pricetrigger.Evaluate(
		policy, evidence.Primary, evidence.Secondary, evidence.ObservedAt,
	)
	if err != nil || evidence.Triggered != evaluated.Triggered ||
		evidence.ConservativePrice != evaluated.ConservativePrice {
		return errors.New("price trigger evidence is invalid")
	}
	return nil
}

func (e *Engine) observePrice(
	ctx context.Context,
	policy pricetrigger.Policy,
	provenSlot uint64,
) (pricetrigger.Evidence, pricetrigger.Status, time.Time, error) {
	evidence, err := e.priceTrigger.EvaluateAtSlot(ctx, policy, provenSlot)
	if err != nil {
		return pricetrigger.Evidence{}, pricetrigger.Status{}, time.Time{}, err
	}
	evaluatedAt := e.now().UTC()
	if err := validateLivePriceEvidence(policy, evidence, evaluatedAt); err != nil {
		return pricetrigger.Evidence{}, pricetrigger.Status{}, time.Time{}, err
	}
	status, err := pricetrigger.Project(policy, evidence)
	if err != nil {
		return pricetrigger.Evidence{}, pricetrigger.Status{}, time.Time{}, err
	}
	return evidence, status, evaluatedAt, nil
}

func validatePriceEvidenceProgress(initial, current pricetrigger.Evidence) error {
	if current.ObservedAt.Before(initial.ObservedAt) ||
		current.Primary.PublishedAt.Before(initial.Primary.PublishedAt) ||
		current.Secondary.PublishedAt.Before(initial.Secondary.PublishedAt) {
		return errors.New("price trigger evidence regressed")
	}
	return nil
}

func (e *Engine) validatePriceForAction(
	ctx context.Context,
	profile Profile,
	current *state,
) (pricetrigger.Evidence, pricetrigger.Status, error) {
	policy := *profile.PriceTrigger
	evidence, status, evaluatedAt, err := e.observePrice(ctx, policy, current.started.ObservationSlot)
	if err != nil {
		return pricetrigger.Evidence{}, status, err
	}
	if current.started == nil || current.started.PriceEvidence == nil {
		return pricetrigger.Evidence{}, status, errors.New("swap has no initial price evidence")
	}
	if err := validatePriceEvidenceProgress(*current.started.PriceEvidence, evidence); err != nil {
		return pricetrigger.Evidence{}, status, err
	}
	if err := attachExecutableMinimum(profile, current.built.MinimumOutput, &status); err != nil {
		return pricetrigger.Evidence{}, status, err
	}
	if !evidence.Triggered || !status.ExecutableCondition {
		return evidence, status, errPriceTriggerNotSatisfied
	}
	if err := e.checkClock(profile, evaluatedAt); err != nil {
		return pricetrigger.Evidence{}, status, err
	}
	windowStart, windowEnd, err := profile.Window(evaluatedAt)
	if err != nil {
		return pricetrigger.Evidence{}, status, err
	}
	if windowStart != current.started.ScheduleWindowStartUnix ||
		windowEnd != current.started.ScheduleWindowEndUnix {
		return pricetrigger.Evidence{}, status, errors.New("swap schedule window changed before signing")
	}
	return evidence, status, nil
}

func executableMinimumSatisfies(profile Profile, minimumOutput uint64) bool {
	if profile.PriceTrigger == nil {
		return true
	}
	if minimumOutput == 0 || profile.inputAmount() == 0 ||
		(profile.isBuy() && profile.PriceTrigger.Direction != pricetrigger.BuyAtOrBelow) ||
		(!profile.isBuy() && profile.PriceTrigger.Direction != pricetrigger.SellAtOrAbove) {
		return false
	}
	priceMicros, err := executablePriceMicros(profile, minimumOutput)
	if err != nil {
		return false
	}
	if profile.isBuy() {
		return priceMicros <= profile.PriceTrigger.ThresholdMicros
	}
	return priceMicros >= profile.PriceTrigger.ThresholdMicros
}

func attachExecutableMinimum(profile Profile, minimumOutput uint64, status *pricetrigger.Status) error {
	if status == nil || profile.PriceTrigger == nil {
		return errors.New("price trigger status is unavailable")
	}
	priceMicros, err := executablePriceMicros(profile, minimumOutput)
	if err != nil {
		return err
	}
	status.ExecutableMinimum = priceMicros
	if profile.isBuy() {
		status.ExecutableCondition = priceMicros <= profile.PriceTrigger.ThresholdMicros
	} else {
		status.ExecutableCondition = priceMicros >= profile.PriceTrigger.ThresholdMicros
	}
	return pricetrigger.ValidateStatus(*status)
}

func priceMicrosForOutput(outputAmount, inputLamports uint64) (uint64, error) {
	if outputAmount == 0 || inputLamports == 0 {
		return 0, errors.New("price amounts are invalid")
	}
	high, low := bits.Mul64(outputAmount, 1_000_000_000)
	if high >= inputLamports {
		return 0, errors.New("price conversion overflows")
	}
	priceMicros, _ := bits.Div64(high, low, inputLamports)
	if priceMicros == 0 || priceMicros > pricetrigger.MaxPriceMicros {
		return 0, errors.New("price conversion is outside policy")
	}
	return priceMicros, nil
}

func validateStoredPriceEvidence(
	policy pricetrigger.Policy,
	evidence pricetrigger.Evidence,
) error {
	evaluated, err := pricetrigger.Evaluate(
		policy, evidence.Primary, evidence.Secondary, evidence.ObservedAt,
	)
	if err != nil || !evidence.Triggered || !evaluated.Triggered ||
		evidence.ConservativePrice != evaluated.ConservativePrice {
		return errors.New("price trigger evidence is invalid")
	}
	return nil
}

func (e *Engine) recoverState(
	profile Profile,
	fingerprint string,
	currentWindowStart int64,
) (string, *state, bool, error) {
	projection, err := e.projectJournal()
	if err != nil {
		return "", nil, false, err
	}
	return recoverState(projection, profile, fingerprint, currentWindowStart)
}

func recoverState(
	projection *journalProjection,
	profile Profile,
	fingerprint string,
	currentWindowStart int64,
) (string, *state, bool, error) {
	pendingID, pending, err := pendingState(projection)
	if err != nil {
		return "", nil, false, err
	}
	if pending != nil {
		if err := validatePendingAction(pendingID, pending, profile, fingerprint); err != nil {
			return "", nil, false, err
		}
		return pendingID, pending, true, nil
	}
	actionID, err := routeActionID(profile, fingerprint, currentWindowStart)
	if err != nil {
		return "", nil, false, err
	}
	current, found := projection.states[actionID]
	if !found {
		current = &state{}
	}
	return actionID, current, current.started != nil, nil
}

func validatePendingAction(
	actionID string,
	current *state,
	profile Profile,
	fingerprint string,
) error {
	if err := validateStartedAction(actionID, current, profile, fingerprint); err != nil {
		return errors.New("unfinished swap is invalid")
	}
	return nil
}

func validateStartedAction(
	actionID string,
	current *state,
	profile Profile,
	fingerprint string,
) error {
	if current == nil || current.started == nil {
		return errors.New("swap has no start record")
	}
	if current.started.ProfileFingerprint != fingerprint {
		return errors.New("swap uses a different profile")
	}
	expected, err := routeActionID(profile, fingerprint, current.started.ScheduleWindowStartUnix)
	if err != nil || expected != actionID {
		return errors.New("swap action ID is invalid")
	}
	return nil
}

func pendingState(projection *journalProjection) (string, *state, error) {
	var pendingID string
	var pending *state
	for _, actionID := range projection.actionIDs {
		current := projection.states[actionID]
		if current.started == nil {
			continue
		}
		if current.canceled != nil || current.reconciliation != nil {
			continue
		}
		if pending != nil {
			return "", nil, errors.New("multiple unfinished swaps require operator review")
		}
		pendingID, pending = actionID, current
	}
	return pendingID, pending, nil
}

// ValidateNoUnresolvedActions verifies that the journal contains no action
// which could still be sent, reconciled, or acknowledged. The caller must hold
// the journal writer lock so the result remains stable while changing setup.
func ValidateNoUnresolvedActions(store *journal.Store) error {
	if store == nil {
		return errors.New("swap journal is required")
	}
	engine := &Engine{store: store}
	projection, err := engine.projectJournal()
	if err != nil {
		return err
	}
	if _, pending, err := pendingState(projection); err != nil {
		return err
	} else if pending != nil {
		return errors.New("unfinished swap requires operator review")
	}
	if _, terminal, err := terminalRequiringControlStop(projection); err != nil {
		return err
	} else if terminal != nil {
		return errors.New("terminal swap requires operator review")
	}
	return nil
}

// terminalRequiringControlStop returns an unreviewed terminal action, or any
// halted action. Review can release journal reserve, but ambiguity must remain
// fail-closed even if the separate control file is lost or reinitialized.
func terminalRequiringControlStop(projection *journalProjection) (string, *state, error) {
	var terminalID string
	var terminalState *state
	for _, actionID := range projection.actionIDs {
		current := projection.states[actionID]
		if current.reconciliation == nil {
			continue
		}
		outcome := terminalOutcome(current.reconciliation.Verdict)
		if outcome == "" || current.acknowledgement != nil && outcome != "halted" {
			continue
		}
		if terminalState != nil {
			return "", nil, errors.New("multiple terminal swaps require operator review")
		}
		terminalID, terminalState = actionID, current
	}
	return terminalID, terminalState, nil
}

func latestUnprojectedStatus(projection *journalProjection) (string, *state) {
	if len(projection.actionIDs) == 0 {
		return "", nil
	}
	actionID := projection.actionIDs[len(projection.actionIDs)-1]
	current := projection.states[actionID]
	if current == nil || current.statusProjection != nil {
		return "", nil
	}
	decision, _, ok := projectableStatus(current)
	if !ok || decision != "complete" && decision != "canceled" {
		return "", nil
	}
	return actionID, current
}

func (e *Engine) submitAndReconcile(
	ctx context.Context,
	actionID string,
	profile Profile,
	current *state,
	recovered bool,
) (Result, error) {
	if current.submission == nil {
		if !recovered {
			return Result{}, errors.New("new swap reached reconciliation without a submission")
		}
		ambiguous := txflow.Submission{
			Signature:            current.signed.Response.Signature,
			LastValidBlockHeight: current.built.LastValidBlockHeight,
			State:                txflow.StateAmbiguous,
		}
		if recovered {
			reconciled, err := reconcileRoute(
				ctx, e.tx, profile, ambiguous,
				current.signed.Response.Signature,
				current.signed.Response.TransactionSHA256,
				current.built.MinimumOutput, current.built.FeeLamports,
			)
			if err != nil {
				return Result{}, err
			}
			if terminalVerdict(reconciled.Verdict) {
				return e.recordReconciliation(actionID, profile, current, reconciled, true)
			}
		}
		expired := false
		blocked, err := e.stop.WithRecoverySendBarrier(actionID, func() error {
			var validateErr error
			expired, validateErr = e.validateBeforeResend(ctx, profile, current)
			return validateErr
		})
		if err != nil {
			return Result{}, err
		}
		if blocked {
			return pendingResult(actionID, profile, current, txflow.VerdictPending, true), nil
		}
		if expired {
			reconciled, err := reconcileRoute(
				ctx, e.tx, profile, ambiguous,
				current.signed.Response.Signature,
				current.signed.Response.TransactionSHA256,
				current.built.MinimumOutput, current.built.FeeLamports,
			)
			if err != nil {
				return Result{}, err
			}
			if terminalVerdict(reconciled.Verdict) {
				return e.recordReconciliation(actionID, profile, current, reconciled, true)
			}
			return Result{}, errors.New("expired submitted swap remains unresolved")
		}
		submission, submitErr := e.submitter.Submit(
			ctx, current.signed.Response, current.built.BlockhashContextSlot,
		)
		if errors.Is(submitErr, submitter.ErrControlBlocked) {
			return pendingResult(actionID, profile, current, txflow.VerdictPending, true), nil
		}
		if submitErr != nil {
			return Result{}, submitErr
		}
		if _, appendErr := e.store.Append(
			e.now().UTC(), EventSubmitted, actionID, submission,
		); appendErr != nil {
			return Result{}, appendErr
		}
		current.submission = &submission
	}
	if current.sendStartedAt.IsZero() {
		return Result{}, errors.New("submitted swap has no durable send time")
	}
	deadline := current.sendStartedAt.Add(
		time.Duration(profile.MaxReconciliationSeconds) * time.Second,
	)
	for {
		reconciled, err := reconcileRoute(
			ctx, e.tx, profile, *current.submission,
			current.signed.Response.Signature,
			current.signed.Response.TransactionSHA256,
			current.built.MinimumOutput, current.built.FeeLamports,
		)
		if err != nil {
			return Result{}, err
		}
		if terminalVerdict(reconciled.Verdict) {
			return e.recordReconciliation(actionID, profile, current, reconciled, recovered)
		}
		if !e.now().UTC().Before(deadline) {
			return pendingResult(actionID, profile, current, reconciled.Verdict, recovered), nil
		}
		timer := time.NewTimer(reconcileInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return pendingResult(actionID, profile, current, reconciled.Verdict, recovered), nil
		case <-timer.C:
		}
	}
}

func (e *Engine) validateBeforeResend(
	ctx context.Context,
	profile Profile,
	current *state,
) (bool, error) {
	now := e.now().UTC()
	if err := e.checkClock(profile, now); err != nil {
		return false, err
	}
	windowStart, windowEnd, err := profile.Window(now)
	if err != nil {
		return false, err
	}
	if windowStart != current.started.ScheduleWindowStartUnix ||
		windowEnd != current.started.ScheduleWindowEndUnix {
		return false, errors.New("swap schedule window changed before resubmission")
	}
	observation, err := e.observer.Observe(ctx, profile.owner())
	if err != nil {
		return false, err
	}
	observedAt := e.now().UTC()
	if err := ValidateObservation(profile, observation, observedAt); err != nil {
		return false, err
	}
	e.recordBalance(observation)
	if observation.Account.Slot < current.started.ObservationSlot {
		return false, errors.New("Mithril account observation regressed before resubmission")
	}
	if err := e.tx.VerifyGenesis(ctx, solana.DevnetGenesisHash); err != nil {
		return false, err
	}
	minDeploymentSlot := current.built.BlockhashContextSlot
	if observation.Account.Slot > minDeploymentSlot {
		minDeploymentSlot = observation.Account.Slot
	}
	if err := verifyRouteDeployment(ctx, e.tx, profile, minDeploymentSlot); err != nil {
		return false, err
	}
	if _, err := verifyBuyInput(ctx, e.tx, profile, minDeploymentSlot); err != nil {
		return false, err
	}
	if err := verifyRouteRent(ctx, e.tx, profile, current.built); err != nil {
		return false, err
	}
	if err := e.store.EnsureCapacity(recoveryRecords, recoveryBytes); err != nil {
		return false, err
	}
	return e.tx.BlockhashExpired(ctx, current.built.LastValidBlockHeight)
}

func pendingResult(
	actionID string,
	profile Profile,
	current *state,
	verdict string,
	recovered bool,
) Result {
	result := stateResult(actionID, profile, current, recovered)
	result.Verdict = verdict
	return result
}

func (e *Engine) projectJournal() (*journalProjection, error) {
	projection := &journalProjection{states: make(map[string]*state)}
	for _, record := range e.store.Records() {
		if !strings.HasPrefix(record.Type, "swap.") {
			continue
		}
		if !validActionID(record.ActionID) {
			return nil, errors.New("swap journal event has an invalid action ID")
		}
		current, ok := projection.states[record.ActionID]
		if !ok {
			current = &state{}
			projection.states[record.ActionID] = current
			projection.actionIDs = append(projection.actionIDs, record.ActionID)
		}
		if err := applyRecord(current, record); err != nil {
			return nil, err
		}
	}
	return projection, nil
}

func applyRecord(current *state, record journal.Record) error {
	if !current.lastAt.IsZero() && record.At.Before(current.lastAt) {
		return errors.New("swap journal event time regressed")
	}
	current.lastAt = record.At.UTC()
	var target any
	switch record.Type {
	case EventObserved:
		if current.started != nil {
			return errors.New("swap observation follows its start event")
		}
		var observation agent.NodeObservation
		if err := strictjson.Decode(record.Payload, &observation); err != nil {
			return errors.New("decode swap journal event")
		}
		current.observations = append(current.observations, observation)
		current.observationFailed = false
		return nil
	case EventObservationFailed:
		if current.started != nil {
			return errors.New("swap observation failure follows its start event")
		}
		var failure observationFailure
		if err := strictjson.Decode(record.Payload, &failure); err != nil ||
			(failure.Reason != "observer_error" && failure.Reason != "invalid_observation") {
			return errors.New("decode swap observation failure")
		}
		current.observations = nil
		current.observationFailed = true
		return nil
	case EventStarted:
		if current.started != nil {
			return errors.New("duplicate swap start")
		}
		current.started = &startedRecord{}
		target = current.started
	case EventBuilt:
		if current.started == nil || current.built != nil {
			return errors.New("invalid swap build order")
		}
		current.built = &builtRecord{}
		target = current.built
	case EventSimulated:
		if current.built == nil || current.simulation != nil {
			return errors.New("invalid swap simulation order")
		}
		current.simulation = &txflow.LegacySimulationEvidence{}
		target = current.simulation
	case EventSigned:
		if current.simulation == nil || current.signed != nil {
			return errors.New("invalid swap signing order")
		}
		current.signed = &signedRecord{}
		target = current.signed
	case EventPreSendObserved:
		if current.signed == nil || current.sendStarted != nil {
			return errors.New("invalid pre-send observation order")
		}
		// A failed send barrier leaves the preceding validation durable. A
		// retry must revalidate and may therefore append a newer observation
		// before the first send marker. Keep only the latest one in memory.
		current.preSendObservation = &agent.NodeObservation{}
		target = current.preSendObservation
	case EventSendStarted:
		if current.signed == nil || current.preSendObservation == nil ||
			current.sendStarted != nil {
			return errors.New("invalid swap send order")
		}
		current.sendStarted = &sendStartedRecord{}
		current.sendStartedAt = record.At
		target = current.sendStarted
	case EventSubmitted:
		if current.sendStarted == nil || current.submission != nil {
			return errors.New("invalid swap submission order")
		}
		current.submission = &txflow.Submission{}
		target = current.submission
	case EventReconciled:
		if current.sendStarted == nil || current.reconciliation != nil {
			return errors.New("invalid swap reconciliation order")
		}
		current.reconciliation = &txflow.Reconciliation{}
		current.terminalAt = record.At.UTC()
		target = current.reconciliation
	case EventCanceled:
		if current.sendStarted != nil || current.canceled != nil {
			return errors.New("invalid swap cancellation order")
		}
		current.canceled = &canceledRecord{}
		current.terminalAt = record.At.UTC()
		target = current.canceled
	case EventStatusProjected:
		if current.statusProjection != nil ||
			(current.canceled == nil && current.reconciliation == nil) {
			return errors.New("invalid status projection order")
		}
		current.statusProjection = &statusProjection{}
		target = current.statusProjection
	case EventTerminalAcknowledged:
		if current.reconciliation == nil || current.acknowledgement != nil {
			return errors.New("invalid terminal acknowledgement order")
		}
		current.acknowledgement = &terminalAcknowledgement{}
		target = current.acknowledgement
	default:
		return fmt.Errorf("unknown swap journal event %q", record.Type)
	}
	if err := strictjson.Decode(record.Payload, target); err != nil {
		return errors.New("decode swap journal event")
	}
	if record.Type == EventTerminalAcknowledged &&
		(current.acknowledgement.Outcome != terminalOutcome(current.reconciliation.Verdict) ||
			!validAcknowledgementReason(current.acknowledgement.Reason)) {
		return errors.New("terminal acknowledgement is invalid")
	}
	if record.Type == EventStatusProjected {
		decision, verdict, ok := projectableStatus(current)
		if !ok || current.statusProjection.Decision != decision ||
			current.statusProjection.Verdict != verdict {
			return errors.New("status projection is invalid")
		}
	}
	return nil
}

func (e *Engine) cancel(
	actionID string,
	profile Profile,
	current *state,
	reason string,
) (Result, error) {
	record := canceledRecord{Reason: reason}
	if _, err := e.store.Append(e.now().UTC(), EventCanceled, actionID, record); err != nil {
		return Result{}, err
	}
	current.canceled = &record
	return stateResult(actionID, profile, current, false), nil
}

func (e *Engine) recordReconciliation(
	actionID string,
	profile Profile,
	current *state,
	reconciled txflow.Reconciliation,
	recovered bool,
) (Result, error) {
	outcome := terminalOutcome(reconciled.Verdict)
	if outcome != "" {
		if err := e.stop.StopForTerminal(actionID, outcome); err != nil {
			return Result{}, errors.New("stop new actions before terminal reconciliation")
		}
	}
	if _, err := e.store.Append(e.now().UTC(), EventReconciled, actionID, reconciled); err != nil {
		return Result{}, err
	}
	current.reconciliation = &reconciled
	// Failed and halted actions retain enough reserved capacity for the
	// operator's durable acknowledgement. A finalized action keeps its reserve
	// until the operator status projection is durable.
	if outcome == "" {
		if err := e.stop.ClearTerminalForFinalized(actionID); err != nil {
			return Result{}, errors.New("clear provisional terminal control state")
		}
	}
	return stateResult(actionID, profile, current, recovered), nil
}

func (e *Engine) repairFinalizedTerminal(
	projection *journalProjection,
	profile Profile,
	fingerprint string,
	now time.Time,
) (string, *state, bool, error) {
	actionID, _, err := e.stop.TerminalLatch()
	if err != nil || actionID == "" {
		return "", nil, false, err
	}
	current := projection.states[actionID]
	if current == nil || current.reconciliation == nil ||
		current.reconciliation.Verdict != txflow.VerdictFinalized {
		return "", nil, false, nil
	}
	if err := validateStartedAction(actionID, current, profile, fingerprint); err != nil ||
		validateStarted(profile, *current.started, now) != nil {
		return "", nil, false, errors.New("finalized terminal swap is invalid")
	}
	if err := e.stop.ClearTerminalForFinalized(actionID); err != nil {
		return "", nil, false, errors.New("repair finalized terminal control state")
	}
	return actionID, current, true, nil
}

func (e *Engine) releaseReservedCapacity() error {
	if e.releaseCapacity != nil {
		return e.releaseCapacity()
	}
	return e.store.ReleaseCapacity()
}

func stateResult(actionID string, profile Profile, current *state, recovered bool) Result {
	result := Result{
		ActionID: actionID, InputLamports: profile.InputLamports,
		InputAmount: profile.inputAmount(), Recovered: recovered,
		ReconciliationTimeoutSeconds: profile.MaxReconciliationSeconds,
	}
	if profile.isBuy() {
		result.InputAsset, result.OutputAsset = "devUSDC", "SOL"
	} else {
		result.InputAsset, result.OutputAsset = "SOL", "devUSDC"
	}
	if current.built != nil {
		result.MinimumOutput = current.built.MinimumOutput
	}
	if profile.PriceTrigger != nil {
		var evidence *pricetrigger.Evidence
		if current.sendStarted != nil {
			evidence = current.sendStarted.PriceEvidence
		} else if current.started != nil {
			evidence = current.started.PriceEvidence
		}
		if evidence != nil {
			if status, err := pricetrigger.Project(*profile.PriceTrigger, *evidence); err == nil {
				result.PriceTrigger = &status
			}
		}
	}
	if current.signed != nil {
		result.Signature = current.signed.Response.Signature
	}
	result.Submitted = current.submission != nil || current.reconciliation != nil
	if !current.sendStartedAt.IsZero() {
		result.PendingSinceUnix = current.sendStartedAt.Unix()
	}
	if current.canceled != nil {
		result.Decision = "canceled"
		result.Reason = current.canceled.Reason
		return result
	}
	if current.reconciliation != nil {
		result.Verdict = current.reconciliation.Verdict
		switch result.Verdict {
		case txflow.VerdictFinalized:
			result.Decision = "complete"
		case txflow.VerdictFailed:
			result.Decision = "failed"
		case txflow.VerdictUnresolved, txflow.VerdictDiverged:
			result.Decision = "halted"
		default:
			result.Decision = "pending"
		}
		if current.reconciliation.SwapEffects != nil {
			result.OutputAmount = current.reconciliation.SwapEffects.OutputAmount
		} else if current.reconciliation.BuyEffects != nil {
			result.OutputAmount = current.reconciliation.BuyEffects.OutputLamports
		}
		return result
	}
	result.Decision = "pending"
	return result
}

func (e *Engine) recordObservationFailure(
	actionID string,
	current *state,
	reason string,
) error {
	if current.observationFailed {
		return nil
	}
	if _, err := e.store.Append(
		e.now().UTC(),
		EventObservationFailed,
		actionID,
		observationFailure{Reason: reason},
	); err != nil {
		return err
	}
	current.observations = nil
	current.observationFailed = true
	return nil
}

func (e *Engine) sustainedHealthReady(
	actionID string,
	profile Profile,
	current *state,
	observation agent.NodeObservation,
	recordedAt time.Time,
) (bool, error) {
	appendObservation := func() error {
		if _, err := e.store.Append(
			recordedAt.UTC(), EventObserved, actionID, observation,
		); err != nil {
			return err
		}
		current.observations = append(current.observations, observation)
		current.observationFailed = false
		return nil
	}
	if len(current.observations) == 0 {
		return false, appendObservation()
	}
	previous := current.observations[len(current.observations)-1]
	previousAt := previous.Account.ObservedAt.UTC()
	if err := ValidateObservation(profile, previous, previousAt); err != nil {
		return false, errors.New("journal swap observation is invalid")
	}
	currentAt := observation.Account.ObservedAt.UTC()
	interval := currentAt.Sub(previousAt)
	if interval < 0 || observation.Account.Slot < previous.Account.Slot {
		return false, errors.New("Mithril health observation regressed")
	}
	if interval > time.Duration(profile.MaxObservationAgeSeconds)*time.Second {
		return false, appendObservation()
	}
	if interval < time.Duration(profile.MinHealthyObservationSeconds)*time.Second ||
		observation.Account.Slot-previous.Account.Slot < profile.MinHealthySlotAdvance {
		return false, nil
	}
	return true, appendObservation()
}

func ValidateObservation(profile Profile, observation agent.NodeObservation, now time.Time) error {
	if observation.Account.Cluster != profile.Cluster ||
		observation.Account.Source != profile.owner() || observation.Account.Slot == 0 ||
		observation.Account.EvidenceSource != "mithril_mcp" ||
		observation.Account.Finality != "local_unfinalized" ||
		observation.Account.Consistency != "node_reported_non_atomic" {
		return observationError(
			"observation_identity",
			"Mithril account observation does not match the swap profile",
		)
	}
	observedAt := observation.Account.ObservedAt.UTC()
	if observedAt.IsZero() || observedAt.After(now.Add(time.Second)) ||
		now.Sub(observedAt) > time.Duration(profile.MaxObservationAgeSeconds)*time.Second {
		return observationError("observation_freshness", "Mithril account observation is stale")
	}
	needed := profile.ReserveLamports + profile.MaxFeeLamports + profile.maxRouteRent()
	if !profile.isBuy() {
		needed += profile.InputLamports
	}
	if observation.Account.BalanceLamports < needed {
		return observationError("wallet_balance", "wallet balance is below the swap reserve")
	}
	health := observation.Health
	healthObservedAt := health.ObservedAt.UTC()
	if health.CrossCheck == nil || health.CrossCheck.ReferenceCommitment != "confirmed" ||
		health.CrossCheck.MithrilView != "local_unfinalized_head" {
		return observationError("mithril_cross_check_contract", "Mithril slot comparison is not ready for the swap")
	}
	if health.CrossCheck.Threshold == 0 || health.CrossCheck.Threshold > maxSwapNodeLagSlots {
		return observationError("mithril_cross_check_policy", "Mithril slot comparison is not ready for the swap")
	}
	if health.CrossCheck.Status != "in_sync" {
		return observationError(
			"mithril_cross_check_"+health.CrossCheck.Status,
			"Mithril slot comparison is not ready for the swap",
		)
	}
	if health.Status != "healthy" {
		stage := "mithril_health_status"
		switch health.Status {
		case "unknown", "degraded", "critical":
			stage += "_" + health.Status
		}
		if issue := boundedHealthIssue(health.Issues); issue != "" {
			stage += "_" + issue
		}
		return observationError(stage, "Mithril health evidence is not ready for the swap")
	}
	if health.AssessmentScope != "point_in_time_snapshot" {
		return observationError("mithril_health_scope", "Mithril health evidence is not ready for the swap")
	}
	if health.SafeForAutomation {
		return observationError("mithril_health_automation_flag", "Mithril health evidence is not ready for the swap")
	}
	if !health.EvidenceComplete {
		return observationError("mithril_health_evidence", "Mithril health evidence is not ready for the swap")
	}
	if health.DivergenceArtifacts != 0 {
		return observationError("mithril_divergence", "Mithril health evidence is not ready for the swap")
	}
	if healthObservedAt.IsZero() || healthObservedAt.After(now.Add(time.Second)) ||
		now.Sub(healthObservedAt) > time.Duration(profile.MaxObservationAgeSeconds)*time.Second {
		return observationError("mithril_health_freshness", "Mithril health evidence is not ready for the swap")
	}
	return nil
}

func boundedHealthIssue(issues []agent.HealthIssue) string {
	for _, issue := range issues {
		switch issue.Name {
		case "metrics", "runtime_provenance", "verification", "block_source", "rpc",
			"state", "state_evidence", "shutdown", "runtime_state_agreement",
			"divergence_artifacts", "host", "turbine_receiver", "logs", "replay",
			"cross_check":
			return issue.Name
		}
	}
	return ""
}

func terminalVerdict(verdict string) bool {
	return verdict == txflow.VerdictFinalized || verdict == txflow.VerdictFailed ||
		verdict == txflow.VerdictUnresolved || verdict == txflow.VerdictDiverged
}

func terminalOutcome(verdict string) string {
	switch verdict {
	case txflow.VerdictFailed:
		return "failed"
	case txflow.VerdictUnresolved, txflow.VerdictDiverged:
		return "halted"
	default:
		return ""
	}
}

func validActionID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

func validAcknowledgementReason(reason string) bool {
	return reason != "" && strings.TrimSpace(reason) == reason && len(reason) <= 256 &&
		strings.IndexFunc(reason, unicode.IsControl) < 0
}

func projectableStatus(current *state) (string, string, bool) {
	if current == nil {
		return "", "", false
	}
	if current.canceled != nil {
		return "canceled", "", true
	}
	if current.reconciliation == nil {
		return "", "", false
	}
	switch current.reconciliation.Verdict {
	case txflow.VerdictFinalized:
		return "complete", txflow.VerdictFinalized, true
	case txflow.VerdictFailed:
		return "failed", txflow.VerdictFailed, true
	case txflow.VerdictUnresolved:
		return "halted", txflow.VerdictUnresolved, true
	case txflow.VerdictDiverged:
		return "halted", txflow.VerdictDiverged, true
	default:
		return "", "", false
	}
}

// LatestDurableAction rebuilds the newest terminal action from the journal so
// the derived status file can be recreated after deletion or corruption.
func LatestDurableAction(
	store *journal.Store,
	profile Profile,
	now time.Time,
) (Result, time.Time, bool, error) {
	if store == nil || now.IsZero() {
		return Result{}, time.Time{}, false, errors.New("journal is required")
	}
	if err := profile.Validate(); err != nil {
		return Result{}, time.Time{}, false, err
	}
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		return Result{}, time.Time{}, false, err
	}
	engine := &Engine{store: store}
	projection, err := engine.projectJournal()
	if err != nil {
		return Result{}, time.Time{}, false, err
	}
	records := store.Records()
	for index := len(records) - 1; index >= 0; index-- {
		record := records[index]
		if record.Type != EventReconciled && record.Type != EventCanceled {
			continue
		}
		current := projection.states[record.ActionID]
		if _, _, ok := projectableStatus(current); !ok {
			return Result{}, time.Time{}, false, errors.New("terminal journal action is invalid")
		}
		if err := validateStartedAction(record.ActionID, current, profile, fingerprint); err != nil ||
			validateStarted(profile, *current.started, now.UTC()) != nil {
			return Result{}, time.Time{}, false, errors.New("terminal journal action is invalid")
		}
		if current.terminalAt.IsZero() {
			return Result{}, time.Time{}, false, errors.New("terminal journal time is invalid")
		}
		windowStart := time.Unix(current.started.ScheduleWindowStartUnix, 0).UTC()
		if current.terminalAt.Before(windowStart) ||
			current.terminalAt.After(now.UTC().Add(5*time.Second)) {
			return Result{}, time.Time{}, false, errors.New("terminal journal time is invalid")
		}
		return stateResult(record.ActionID, profile, current, false),
			current.terminalAt, true, nil
	}
	return Result{}, time.Time{}, false, nil
}

// MarkStatusProjected records that an operator-visible completed or canceled
// result was durably written. The marker lets a restart replay a missing
// projection exactly once without repeatedly announcing historical actions.
func MarkStatusProjected(
	store *journal.Store,
	actionID,
	decision,
	verdict string,
	at time.Time,
) (bool, error) {
	if store == nil || !validActionID(actionID) || at.IsZero() ||
		(decision != "complete" && decision != "canceled") {
		return false, errors.New("status projection is invalid")
	}
	engine := &Engine{store: store}
	projection, err := engine.projectJournal()
	if err != nil {
		return false, err
	}
	current := projection.states[actionID]
	wantDecision, wantVerdict, ok := projectableStatus(current)
	if !ok || decision != wantDecision || verdict != wantVerdict {
		return false, errors.New("status projection does not match the action")
	}
	if current.statusProjection != nil {
		if current.statusProjection.Decision != decision ||
			current.statusProjection.Verdict != verdict {
			return false, errors.New("status projection does not match the action")
		}
		_ = store.ReleaseCapacity()
		return false, nil
	}
	if _, err := store.Append(
		at.UTC(), EventStatusProjected, actionID,
		statusProjection{Decision: decision, Verdict: verdict},
	); err != nil {
		return false, err
	}
	_ = store.ReleaseCapacity()
	return true, nil
}

// AcknowledgeTerminal appends an exact, idempotent acknowledgement for a
// failed or halted swap. The caller must hold the journal's writer lock.
func AcknowledgeTerminal(
	store *journal.Store,
	actionID,
	outcome string,
	reason string,
	at time.Time,
) (bool, error) {
	if at.IsZero() {
		return false, errors.New("terminal acknowledgement is invalid")
	}
	alreadyAcknowledged, err := ValidateTerminalAcknowledgement(
		store, actionID, outcome, reason,
	)
	if err != nil || alreadyAcknowledged {
		return false, err
	}
	if _, err := store.Append(
		at.UTC(), EventTerminalAcknowledged, actionID,
		terminalAcknowledgement{Outcome: outcome, Reason: reason},
	); err != nil {
		return false, err
	}
	_ = store.ReleaseCapacity()
	return true, nil
}

// ValidateTerminalAcknowledgement verifies an exact offline acknowledgement.
// The returned boolean reports whether the same acknowledgement is durable.
func ValidateTerminalAcknowledgement(
	store *journal.Store,
	actionID,
	outcome string,
	reason string,
) (bool, error) {
	if store == nil || !validActionID(actionID) ||
		(outcome != "failed" && outcome != "halted") ||
		!validAcknowledgementReason(reason) {
		return false, errors.New("terminal acknowledgement is invalid")
	}
	engine := &Engine{store: store}
	projection, err := engine.projectJournal()
	if err != nil {
		return false, err
	}
	current, ok := projection.states[actionID]
	if !ok || current.reconciliation == nil ||
		terminalOutcome(current.reconciliation.Verdict) != outcome {
		return false, errors.New("terminal acknowledgement does not match the action")
	}
	terminalID, terminal, err := terminalRequiringControlStop(projection)
	if err != nil {
		return false, err
	}
	if current.acknowledgement != nil {
		if terminal != nil && terminalID != actionID {
			return false, errors.New("another terminal swap requires acknowledgement")
		}
		if current.acknowledgement.Outcome != outcome ||
			current.acknowledgement.Reason != reason {
			return false, errors.New("terminal action was acknowledged differently")
		}
		return true, nil
	}
	if terminal == nil || terminalID != actionID {
		return false, errors.New("another terminal swap requires acknowledgement")
	}
	return false, nil
}
