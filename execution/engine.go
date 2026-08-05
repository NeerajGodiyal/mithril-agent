package execution

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/independentdecode"
	"github.com/Overclock-Validator/mithril-agent/internal/clockcheck"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const (
	EventExecutionStarted       = "execution.started"
	EventExecutionCanceled      = "execution.canceled"
	EventClockAccepted          = "clock.accepted"
	EventNodeObserved           = "node.observed"
	EventNodeObservationFailed  = "node.observation_failed"
	EventTransactionBuilt       = "transaction.built"
	EventTransactionSimulated   = "transaction.simulated"
	EventPolicyGranted          = "policy.granted"
	EventTransactionSigned      = "transaction.signed"
	EventTransactionQuarantined = "transaction.signed_quarantined"
	EventQuarantineResolved     = "transaction.quarantine_resolved"
	EventTransactionSendStarted = "transaction.send_started"
	EventTransactionSubmitted   = "transaction.submitted"
	EventTransactionReconciled  = "transaction.reconciled"

	maxBlockHeightWindow       = 300
	minClockJournalInterval    = time.Minute
	terminalJournalRecordCount = 3
	terminalJournalByteReserve = 3 << 20
)

var (
	errNodeLagExceeded   = errors.New("Mithril node lag exceeds the active profile")
	errFeeBudgetExceeded = errors.New("transaction fee exceeds the active profile")
)

type Observer interface {
	Observe(context.Context, string) (agent.NodeObservation, error)
}

type BlockhashProvider interface {
	LatestBlockhash(context.Context, uint64) (solanarpc.LatestBlockhash, error)
	BlockHeight(context.Context) (uint64, error)
}

type Signer interface {
	Sign(context.Context, signer.Request) (signer.Response, error)
}

type Submitter interface {
	Submit(context.Context, signer.Response, uint64) (txflow.Submission, error)
}

type PolicyAuthority interface {
	Authorize(context.Context, signer.Request) (riskgrant.Grant, error)
	VerifyAt(signer.Request, riskgrant.Grant, time.Time) error
}

type StopChecker interface {
	NoNewActions() (bool, error)
	WithSendBarrier(string, func() error) (bool, error)
}

type Transactor interface {
	VerifyGenesis(context.Context, string) error
	VerifyEvidenceGenesis(context.Context, string) error
	AccountsForTransfer(
		context.Context,
		string,
		string,
		uint64,
	) (txflow.TransferAccountEvidence, error)
	FeeForMessage(context.Context, []byte, uint64) (txflow.FeeEvidence, error)
	Simulate(context.Context, []byte, uint64) (txflow.SimulationEvidence, error)
	BlockhashExpired(context.Context, uint64) (bool, error)
	ReconcileExpected(
		context.Context,
		txflow.Submission,
		txflow.ExpectedTransaction,
		uint64,
	) (txflow.Reconciliation, error)
}

type Engine struct {
	store           *journal.Store
	observer        Observer
	blockhash       BlockhashProvider
	authority       PolicyAuthority
	signer          Signer
	submitter       Submitter
	tx              Transactor
	stop            StopChecker
	now             func() time.Time
	clock           func() (clockcheck.Sample, error)
	releaseCapacity func() error

	// balanceMu guards the last dual-evidence-bounded balance. Held on the
	// engine rather than threaded through every Result construction so a
	// cycle that stops early still leaves the most recent bounded reading
	// available, stamped with its own time.
	balanceMu         sync.Mutex
	balanceLamports   uint64
	balanceObservedAt time.Time
}

// recordBalance keeps the conservative dual-evidence-bounded balance the
// proposal engine is about to act on — not the raw local reading.
func (e *Engine) recordBalance(lamports uint64, observedAt time.Time) {
	e.balanceMu.Lock()
	defer e.balanceMu.Unlock()
	e.balanceLamports = lamports
	e.balanceObservedAt = observedAt.UTC()
}

// LastBalance reports the most recent bounded balance and when it was
// observed. ok is false until the first bounded observation, so a balance of
// zero is distinguishable from "never observed".
func (e *Engine) LastBalance() (lamports uint64, observedUnix int64, ok bool) {
	e.balanceMu.Lock()
	defer e.balanceMu.Unlock()
	if e.balanceObservedAt.IsZero() {
		return 0, 0, false
	}
	return e.balanceLamports, e.balanceObservedAt.Unix(), true
}

type Result = operatorstatus.Result

type builtTransaction struct {
	MessageBase64           string `json:"message_base64"`
	RecentBlockhash         string `json:"recent_blockhash"`
	BlockhashContextSlot    uint64 `json:"blockhash_context_slot"`
	ObservedBlockHeight     uint64 `json:"observed_block_height"`
	LastValidBlockHeight    uint64 `json:"last_valid_block_height"`
	FeeLamports             uint64 `json:"fee_lamports"`
	FeeMinContextSlot       uint64 `json:"fee_min_context_slot"`
	PrimaryFeeContextSlot   uint64 `json:"primary_fee_context_slot"`
	SecondaryFeeContextSlot uint64 `json:"secondary_fee_context_slot"`
}

type signedTransaction struct {
	SignerResponse signer.Response `json:"signer_response"`
}

type sendStarted struct {
	Signature                string                         `json:"signature"`
	AuthorizedAt             time.Time                      `json:"authorized_at"`
	LocalObservation         agent.NodeObservation          `json:"local_observation"`
	EffectiveBalanceLamports uint64                         `json:"effective_balance_lamports"`
	AccountEvidence          txflow.TransferAccountEvidence `json:"account_evidence"`
}

type executionStarted struct {
	Mode                     string                         `json:"mode"`
	LocalAvailableLamports   uint64                         `json:"local_available_lamports"`
	EffectiveBalanceLamports uint64                         `json:"effective_balance_lamports"`
	AccountEvidence          txflow.TransferAccountEvidence `json:"account_evidence"`
	Health                   agent.NodeHealth               `json:"health"`
}

type executionCanceled struct {
	Reason string `json:"reason"`
}

type transactionQuarantined struct {
	Reason string `json:"reason"`
}

type quarantineResolved struct {
	QuarantineReason     string `json:"quarantine_reason"`
	ObservedBlockHeight  uint64 `json:"observed_block_height"`
	LastValidBlockHeight uint64 `json:"last_valid_block_height"`
}

type nodeObservationFailed struct {
	Reason string `json:"reason"`
}

type executionState struct {
	proposal           agent.Proposal
	started            *executionStarted
	canceled           bool
	cancelReason       string
	built              *builtTransaction
	simulated          *txflow.SimulationEvidence
	grant              *riskgrant.Grant
	grantAt            time.Time
	grants             []recordedGrant
	signed             *signedTransaction
	quarantined        bool
	quarantineReason   string
	quarantineResolved *quarantineResolved
	sendStarted        *sendStarted
	sendStartedAt      time.Time
	submission         *txflow.Submission
	reconciliation     *txflow.Reconciliation
}

type recordedGrant struct {
	grant riskgrant.Grant
	at    time.Time
}

type executionGuard struct {
	finalized *executionState
	halted    bool
}

func New(
	store *journal.Store,
	observer Observer,
	blockhash BlockhashProvider,
	policyAuthority PolicyAuthority,
	signerClient Signer,
	submitterClient Submitter,
	transactor Transactor,
	stopChecker StopChecker,
	now func() time.Time,
) (*Engine, error) {
	if store == nil || observer == nil || blockhash == nil || policyAuthority == nil ||
		signerClient == nil || submitterClient == nil || transactor == nil {
		return nil, errors.New("journal, observer, blockhash provider, policy authority, signer, submitter, and transactor are required")
	}
	if now == nil {
		now = time.Now
	}
	return &Engine{
		store:           store,
		observer:        observer,
		blockhash:       blockhash,
		authority:       policyAuthority,
		signer:          signerClient,
		submitter:       submitterClient,
		tx:              transactor,
		stop:            stopChecker,
		now:             now,
		clock:           clockcheck.SystemSample,
		releaseCapacity: store.ReleaseCapacity,
	}, nil
}

func (e *Engine) RunOnce(ctx context.Context, profile agent.Profile) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := profile.Validate(); err != nil {
		return Result{}, err
	}
	if profile.Cluster != "devnet" {
		return Result{}, errors.New("transaction execution is restricted to devnet")
	}
	state, recovered, err := e.activeState()
	if err != nil {
		return Result{}, err
	}
	if state != nil && state.sendStarted != nil {
		if err := e.tx.VerifyEvidenceGenesis(ctx, solana.DevnetGenesisHash); err != nil {
			return Result{}, err
		}
	} else if err := e.tx.VerifyGenesis(ctx, solana.DevnetGenesisHash); err != nil {
		return Result{}, err
	}
	if state == nil {
		if err := e.releaseReservedCapacity(); err != nil {
			return Result{}, err
		}
	}
	clockChecked := false
	if state == nil {
		guard, err := e.guard()
		if err != nil {
			return Result{}, err
		}
		if guard.halted {
			return Result{}, errors.New("journal contains an unresolved transaction; operator review is required")
		}
		nodeObservation, err := e.observe(ctx, profile.Source)
		if err != nil {
			return Result{}, err
		}
		observation := nodeObservation.Account
		if observation.EvidenceSource != "mithril_mcp" ||
			observation.Finality != "local_unfinalized" ||
			observation.Consistency != "node_reported_non_atomic" {
			return Result{}, errors.New("execution requires a Mithril MCP account observation")
		}
		if guard.finalized != nil &&
			observation.Slot <= guard.finalized.reconciliation.Slot {
			if observation.Slot == guard.finalized.proposal.ObservationSlot &&
				validateProposalForProfile(guard.finalized.proposal, profile) == nil {
				if err := validateCompletedState(guard.finalized, nil); err != nil {
					return Result{}, err
				}
				return resultFromState(guard.finalized, true), nil
			}
			return Result{
				Decision: "waiting",
				Reason:   "Mithril account evidence has not advanced past the last finalized transfer",
			}, nil
		}
		ready, err := e.sustainedHealthReady(profile, nodeObservation)
		if err != nil {
			return Result{}, err
		}
		if !healthyNodeObservation(nodeObservation, profile, e.now().UTC()) {
			return Result{
				Decision: "degraded",
				Reason:   "Mithril health evidence is not complete and healthy",
			}, nil
		}
		stopped, err := e.noNewActions()
		if err != nil {
			return Result{}, err
		}
		if stopped {
			return Result{Decision: "stopped", Reason: "no_new_actions is active"}, nil
		}
		if err := e.checkClock(profile.ClockUncertaintyLimit()); err != nil {
			return Result{}, err
		}
		clockChecked = true
		if !safeForUTCRollover(
			e.now().UTC(),
			profile.ClockUncertaintyLimit(),
		) {
			return Result{
				Decision: "waiting",
				Reason:   "UTC rollover clock guard is active",
			}, nil
		}
		if !ready {
			return Result{
				Decision: "waiting",
				Reason:   "waiting for consecutive healthy Mithril observations with slot progress",
			}, nil
		}
		localAvailable := observation.BalanceLamports
		accountEvidence, err := e.tx.AccountsForTransfer(
			ctx,
			profile.Source,
			profile.Destination,
			observation.Slot,
		)
		if err != nil {
			return Result{}, err
		}
		if guard.finalized != nil &&
			accountEvidence.CommonFinalizedFloor < guard.finalized.reconciliation.Slot {
			return Result{}, errors.New(
				"independent finalized floor regressed behind the last transfer",
			)
		}
		observation.BalanceLamports = boundedBalance(localAvailable, accountEvidence.Source)
		e.recordBalance(observation.BalanceLamports, observation.ObservedAt)
		proposalEngine, err := agent.NewEngine(e.store, e.now)
		if err != nil {
			return Result{}, err
		}
		proposed, err := proposalEngine.Propose(profile, observation)
		if err != nil {
			return Result{}, err
		}
		if proposed.Decision == "skipped" {
			return Result{Decision: proposed.Decision}, nil
		}
		if proposalDayExpired(proposed.Proposal, e.now().UTC()) {
			return Result{}, errors.New("recovered proposal belongs to an earlier UTC day")
		}
		previous, err := e.stateForAction(proposed.Proposal.ActionID)
		if err != nil {
			return Result{}, err
		}
		if previous != nil && previous.started != nil {
			if previous.canceled ||
				previous.quarantineResolved != nil ||
				(previous.reconciliation != nil && terminalVerdict(previous.reconciliation.Verdict)) {
				if previous.quarantineResolved != nil {
					if err := validateResolvedQuarantine(previous); err != nil {
						return Result{}, err
					}
				} else {
					if err := validateCompletedState(previous, &profile); err != nil {
						return Result{}, err
					}
				}
				return resultFromState(previous, true), nil
			}
			return Result{}, errors.New("journal active execution was not selected for recovery")
		}
		started := executionStarted{
			Mode:                     "devnet",
			LocalAvailableLamports:   localAvailable,
			EffectiveBalanceLamports: observation.BalanceLamports,
			AccountEvidence:          accountEvidence,
			Health:                   nodeObservation.Health,
		}
		if err := validateExecutionStart(proposed.Proposal, started); err != nil {
			return Result{}, err
		}
		if _, err := e.store.Append(
			e.now().UTC(),
			EventExecutionStarted,
			proposed.Proposal.ActionID,
			started,
		); err != nil {
			return Result{}, err
		}
		state = &executionState{proposal: proposed.Proposal, started: &started}
		recovered = proposed.Recovered
	}
	if state.sendStarted == nil {
		if err := validateProposalForProfile(state.proposal, profile); err != nil {
			return Result{}, err
		}
	} else if err := validateProposalShape(state.proposal); err != nil {
		return Result{}, err
	}
	if state.canceled {
		if err := validateCompletedState(state, &profile); err != nil {
			return Result{}, err
		}
		return Result{
			ActionID:       state.proposal.ActionID,
			Decision:       "canceled",
			Reason:         state.cancelReason,
			AmountLamports: state.proposal.AmountLamports,
			Recovered:      true,
		}, nil
	}
	if state.quarantined {
		if err := validateQuarantinedState(state); err != nil {
			return Result{}, err
		}
		if state.quarantineResolved == nil {
			height, err := e.blockhash.BlockHeight(ctx)
			if err != nil {
				return Result{}, err
			}
			if height > state.built.LastValidBlockHeight {
				resolved := quarantineResolved{
					QuarantineReason:     state.quarantineReason,
					ObservedBlockHeight:  height,
					LastValidBlockHeight: state.built.LastValidBlockHeight,
				}
				if _, err := e.store.Append(
					e.now().UTC(),
					EventQuarantineResolved,
					state.proposal.ActionID,
					resolved,
				); err != nil {
					return Result{}, err
				}
				state.quarantineResolved = &resolved
			}
		}
		return resultFromState(state, true), nil
	}
	if state.reconciliation != nil && terminalVerdict(state.reconciliation.Verdict) {
		if err := validateCompletedState(state, nil); err != nil {
			return Result{}, err
		}
		return resultFromState(state, true), nil
	}
	if recovered && !clockChecked && state.sendStarted == nil {
		stopped, err := e.noNewActions()
		if err != nil {
			return Result{}, err
		}
		if stopped {
			if state.signed != nil {
				if err := e.quarantine(state, "operator_stop_before_submission"); err != nil {
					return Result{}, err
				}
				return resultFromState(state, true), nil
			}
			return Result{
				ActionID:       state.proposal.ActionID,
				Decision:       "stopped",
				Reason:         "no_new_actions is active",
				AmountLamports: state.proposal.AmountLamports,
				Recovered:      true,
			}, nil
		}
		if err := e.checkClock(profile.ClockUncertaintyLimit()); err != nil {
			return Result{}, err
		}
		if !safeForUTCRollover(
			e.now().UTC(),
			profile.ClockUncertaintyLimit(),
		) {
			return Result{
				ActionID:       state.proposal.ActionID,
				Decision:       "waiting",
				Reason:         "UTC rollover clock guard is active",
				AmountLamports: state.proposal.AmountLamports,
				Recovered:      true,
			}, nil
		}
	}
	if state.sendStarted == nil && proposalDayExpired(state.proposal, e.now().UTC()) {
		reason := "reservation_day_expired_before_submission"
		if state.signed != nil {
			if err := e.quarantine(state, reason); err != nil {
				return Result{}, err
			}
			return resultFromState(state, recovered), nil
		}
		if err := e.cancel(state, reason); err != nil {
			return Result{}, err
		}
		return resultFromState(state, recovered), nil
	}
	if state.sendStarted == nil && proposalWindowExpired(state.proposal, e.now().UTC()) {
		reason := "schedule_window_expired_before_submission"
		if state.built == nil {
			reason = "schedule_window_expired_before_build"
		}
		if state.signed != nil {
			if err := e.quarantine(state, reason); err != nil {
				return Result{}, err
			}
			return resultFromState(state, recovered), nil
		}
		if err := e.cancel(state, reason); err != nil {
			return Result{}, err
		}
		return resultFromState(state, recovered), nil
	}
	if state.built == nil && proposalExpired(state.proposal, profile, e.now().UTC()) {
		if err := e.cancel(state, "observation_expired_before_build"); err != nil {
			return Result{}, err
		}
		return resultFromState(state, recovered), nil
	}

	if state.built == nil {
		built, err := e.build(ctx, state.proposal)
		if err != nil {
			if errors.Is(err, errNodeLagExceeded) {
				if err := e.cancel(state, "node_lag_exceeded_before_build"); err != nil {
					return Result{}, err
				}
				return resultFromState(state, recovered), nil
			}
			if errors.Is(err, errFeeBudgetExceeded) {
				if err := e.cancel(state, "fee_exceeds_profile"); err != nil {
					return Result{}, err
				}
				return resultFromState(state, recovered), nil
			}
			return Result{}, err
		}
		if _, err := e.store.Append(e.now().UTC(), EventTransactionBuilt, state.proposal.ActionID, built); err != nil {
			return Result{}, err
		}
		state.built = &built
	} else if err := validateBuilt(state.proposal, *state.built); err != nil {
		return Result{}, err
	}

	if state.simulated == nil {
		message, err := base64.StdEncoding.Strict().DecodeString(state.built.MessageBase64)
		if err != nil {
			return Result{}, errors.New("journal message is not canonical base64")
		}
		simulation, err := e.tx.Simulate(ctx, message, state.built.BlockhashContextSlot)
		if err != nil {
			return Result{}, err
		}
		if err := validateSimulation(*state.built, simulation); err != nil {
			return Result{}, err
		}
		if _, err := e.store.Append(
			e.now().UTC(),
			EventTransactionSimulated,
			state.proposal.ActionID,
			simulation,
		); err != nil {
			return Result{}, err
		}
		state.simulated = &simulation
	} else if err := validateSimulation(*state.built, *state.simulated); err != nil {
		return Result{}, err
	}

	if state.signed == nil {
		expired, err := e.tx.BlockhashExpired(ctx, state.built.LastValidBlockHeight)
		if err != nil {
			return Result{}, err
		}
		if expired {
			if err := e.cancel(state, "blockhash_expired_before_signing"); err != nil {
				return Result{}, err
			}
			return resultFromState(state, recovered), nil
		}
		reason, _, _, _, err := e.authorizeUnsent(ctx, profile, state)
		if err != nil {
			return Result{}, err
		}
		if reason != "" {
			if err := e.cancel(state, reason+"_before_signing"); err != nil {
				return Result{}, err
			}
			return resultFromState(state, recovered), nil
		}
		signing := signingRequest(state.proposal, *state.built)
		if state.grant == nil {
			grant, err := e.authority.Authorize(ctx, signing)
			if err != nil {
				return Result{}, err
			}
			grantAt := e.now().UTC()
			if err := e.authority.VerifyAt(signing, grant, grantAt); err != nil {
				return Result{}, err
			}
			record, err := e.store.Append(
				grantAt,
				EventPolicyGranted,
				state.proposal.ActionID,
				grant,
			)
			if err != nil {
				return Result{}, err
			}
			state.grant = &grant
			state.grantAt = record.At
			state.grants = append(state.grants, recordedGrant{grant: grant, at: record.At})
		} else {
			if err := e.authority.VerifyAt(signing, *state.grant, state.grantAt); err != nil {
				return Result{}, err
			}
			if e.now().UTC().Unix() >= state.grant.Claims.ExpiresAtUnix {
				grant, err := e.authority.Authorize(ctx, signing)
				if err != nil {
					return Result{}, err
				}
				grantAt := e.now().UTC()
				if err := e.authority.VerifyAt(signing, grant, grantAt); err != nil {
					return Result{}, err
				}
				record, err := e.store.Append(
					grantAt,
					EventPolicyGranted,
					state.proposal.ActionID,
					grant,
				)
				if err != nil {
					return Result{}, err
				}
				state.grant = &grant
				state.grantAt = record.At
				state.grants = append(state.grants, recordedGrant{grant: grant, at: record.At})
			}
		}
		signing.RiskGrant = *state.grant
		response, err := e.signer.Sign(ctx, signing)
		if err != nil {
			return Result{}, err
		}
		signed := signedTransaction{SignerResponse: response}
		if err := validateSigned(state.proposal, *state.built, signed); err != nil {
			return Result{}, err
		}
		if _, err := e.store.Append(e.now().UTC(), EventTransactionSigned, state.proposal.ActionID, signed); err != nil {
			return Result{}, err
		}
		state.signed = &signed
	} else if err := validateSigned(state.proposal, *state.built, *state.signed); err != nil {
		return Result{}, err
	}

	if state.sendStarted == nil {
		expired, err := e.tx.BlockhashExpired(ctx, state.built.LastValidBlockHeight)
		if err != nil {
			return Result{}, err
		}
		if expired {
			if err := e.quarantine(state, "blockhash_expired_before_submission"); err != nil {
				return Result{}, err
			}
			return resultFromState(state, recovered), nil
		}
		reason, localObservation, accountEvidence, effectiveBalance, err := e.authorizeUnsent(
			ctx,
			profile,
			state,
		)
		if err != nil {
			return Result{}, err
		}
		if reason != "" {
			if err := e.quarantine(state, reason+"_before_submission"); err != nil {
				return Result{}, err
			}
			return resultFromState(state, recovered), nil
		}
		expired, err = e.tx.BlockhashExpired(ctx, state.built.LastValidBlockHeight)
		if err != nil {
			return Result{}, err
		}
		if expired {
			if err := e.quarantine(state, "blockhash_expired_before_submission"); err != nil {
				return Result{}, err
			}
			return resultFromState(state, recovered), nil
		}
		var boundaryReason string
		var authorizedAt time.Time
		blocked, err := e.withSendBarrier(state.proposal.ActionID, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			boundaryReason, _, err = e.authorizeSendBoundary(ctx, profile, state)
			if err != nil || boundaryReason != "" {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := e.store.EnsureCapacity(
				terminalJournalRecordCount,
				terminalJournalByteReserve,
			); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			expired, err := e.tx.BlockhashExpired(ctx, state.built.LastValidBlockHeight)
			if err != nil {
				return err
			}
			if expired {
				boundaryReason = "blockhash_expired"
				return nil
			}
			boundaryReason, authorizedAt, err = e.authorizeSendBoundary(ctx, profile, state)
			if err != nil || boundaryReason != "" {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			started := sendStarted{
				Signature:                state.signed.SignerResponse.Signature,
				AuthorizedAt:             authorizedAt,
				LocalObservation:         localObservation,
				EffectiveBalanceLamports: effectiveBalance,
				AccountEvidence:          accountEvidence,
			}
			record, err := e.store.Append(
				authorizedAt,
				EventTransactionSendStarted,
				state.proposal.ActionID,
				started,
			)
			if err != nil {
				return err
			}
			state.sendStarted = &started
			state.sendStartedAt = record.At
			return nil
		})
		if err != nil {
			return Result{}, err
		}
		if blocked {
			boundaryReason = "operator_stop"
		}
		if boundaryReason != "" {
			if err := e.quarantine(state, boundaryReason+"_before_submission"); err != nil {
				return Result{}, err
			}
			return resultFromState(state, recovered), nil
		}
		submission, err := e.submitter.Submit(
			ctx,
			state.signed.SignerResponse,
			state.built.BlockhashContextSlot,
		)
		if err != nil {
			return Result{}, err
		}
		if _, err := e.store.Append(
			e.now().UTC(),
			EventTransactionSubmitted,
			state.proposal.ActionID,
			submission,
		); err != nil {
			return Result{}, err
		}
		state.submission = &submission
	} else if err := validateSendStarted(state); err != nil {
		return Result{}, err
	}

	if state.submission == nil {
		submission := txflow.Submission{
			Signature:            state.signed.SignerResponse.Signature,
			LastValidBlockHeight: state.built.LastValidBlockHeight,
			State:                txflow.StateAmbiguous,
		}
		if _, err := e.store.Append(e.now().UTC(), EventTransactionSubmitted, state.proposal.ActionID, submission); err != nil {
			return Result{}, err
		}
		state.submission = &submission
	} else if err := validateSubmission(*state.submission, *state.signed, *state.built); err != nil {
		return Result{}, err
	}
	if state.reconciliation != nil {
		if err := validateReconciliation(*state.reconciliation, state); err != nil {
			return Result{}, err
		}
	}

	reconciled, err := e.tx.ReconcileExpected(
		ctx,
		*state.submission,
		expectedTransaction(state),
		state.built.FeeLamports,
	)
	if err != nil {
		return Result{}, err
	}
	if shouldRecordReconciliation(state.reconciliation, reconciled) {
		if _, err := e.store.Append(e.now().UTC(), EventTransactionReconciled, state.proposal.ActionID, reconciled); err != nil {
			return Result{}, err
		}
	}
	state.reconciliation = &reconciled
	if terminalVerdict(reconciled.Verdict) {
		// Reconciliation is already durable. Cleanup failure must not hide its
		// terminal result.
		_ = e.releaseReservedCapacity()
	}
	return resultFromState(state, recovered), nil
}

func (e *Engine) releaseReservedCapacity() error {
	if e.releaseCapacity != nil {
		return e.releaseCapacity()
	}
	return e.store.ReleaseCapacity()
}

func (e *Engine) noNewActions() (bool, error) {
	if e.stop == nil {
		return false, nil
	}
	return e.stop.NoNewActions()
}

func (e *Engine) withSendBarrier(
	actionID string,
	operation func() error,
) (bool, error) {
	if e.stop == nil {
		return false, operation()
	}
	return e.stop.WithSendBarrier(actionID, operation)
}

func (e *Engine) authorizeSendBoundary(
	ctx context.Context,
	profile agent.Profile,
	state *executionState,
) (string, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, err
	}
	if err := e.checkClock(profile.ClockUncertaintyLimit()); err != nil {
		return "", time.Time{}, err
	}
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, err
	}
	now := e.now().UTC()
	if proposalDayExpired(state.proposal, now) {
		return "reservation_day_expired", now, nil
	}
	if proposalWindowExpired(state.proposal, now) {
		return "schedule_window_expired", now, nil
	}
	if proposalExpired(state.proposal, profile, now) {
		return "observation_expired", now, nil
	}
	if !safeForUTCRollover(now, profile.ClockUncertaintyLimit()) {
		return "utc_rollover_guard", now, nil
	}
	return "", now, nil
}

func (e *Engine) authorizeUnsent(
	ctx context.Context,
	profile agent.Profile,
	state *executionState,
) (string, agent.NodeObservation, txflow.TransferAccountEvidence, uint64, error) {
	now := e.now().UTC()
	if proposalDayExpired(state.proposal, now) {
		return "reservation_day_expired", agent.NodeObservation{}, txflow.TransferAccountEvidence{}, 0, nil
	}
	if proposalWindowExpired(state.proposal, now) {
		return "schedule_window_expired", agent.NodeObservation{}, txflow.TransferAccountEvidence{}, 0, nil
	}
	if proposalExpired(state.proposal, profile, now) {
		return "observation_expired", agent.NodeObservation{}, txflow.TransferAccountEvidence{}, 0, nil
	}
	if !safeForUTCRollover(now, profile.ClockUncertaintyLimit()) {
		return "utc_rollover_guard", agent.NodeObservation{}, txflow.TransferAccountEvidence{}, 0, nil
	}
	stopped, err := e.noNewActions()
	if err != nil {
		return "", agent.NodeObservation{}, txflow.TransferAccountEvidence{}, 0, err
	}
	if stopped {
		return "operator_stop", agent.NodeObservation{}, txflow.TransferAccountEvidence{}, 0, nil
	}
	if err := e.checkClock(profile.ClockUncertaintyLimit()); err != nil {
		return "", agent.NodeObservation{}, txflow.TransferAccountEvidence{}, 0, err
	}
	localObservation, err := e.observe(ctx, profile.Source)
	if err != nil {
		return "", agent.NodeObservation{}, txflow.TransferAccountEvidence{}, 0, err
	}
	if !healthyNodeObservation(localObservation, profile, e.now().UTC()) {
		return "mithril_health_changed", localObservation, txflow.TransferAccountEvidence{}, 0, nil
	}
	if localObservation.Account.Slot < state.proposal.ObservationSlot {
		return "", agent.NodeObservation{}, txflow.TransferAccountEvidence{}, 0,
			errors.New("fresh Mithril account observation regressed")
	}
	evidence, err := e.tx.AccountsForTransfer(
		ctx,
		profile.Source,
		profile.Destination,
		localObservation.Account.Slot,
	)
	if err != nil {
		return "", agent.NodeObservation{}, txflow.TransferAccountEvidence{}, 0, err
	}
	if err := validateTransferAccountEvidenceAt(
		state.proposal,
		evidence,
		localObservation.Account.Slot,
	); err != nil {
		return "", agent.NodeObservation{}, txflow.TransferAccountEvidence{}, 0, err
	}
	guard, err := e.guard()
	if err != nil {
		return "", agent.NodeObservation{}, txflow.TransferAccountEvidence{}, 0, err
	}
	if guard.finalized != nil &&
		evidence.CommonFinalizedFloor < guard.finalized.reconciliation.Slot {
		return "", agent.NodeObservation{}, txflow.TransferAccountEvidence{}, 0,
			errors.New("independent finalized floor regressed behind the last transfer")
	}
	effective := boundedBalance(localObservation.Account.BalanceLamports, evidence.Source)
	fee := state.built.FeeLamports
	if state.proposal.AmountLamports > ^uint64(0)-fee {
		return "", agent.NodeObservation{}, txflow.TransferAccountEvidence{}, 0,
			errors.New("pre-send debit overflows")
	}
	debit := state.proposal.AmountLamports + fee
	if state.proposal.ReserveLamports > ^uint64(0)-debit {
		return "", agent.NodeObservation{}, txflow.TransferAccountEvidence{}, 0,
			errors.New("pre-send reserve requirement overflows")
	}
	if effective < state.proposal.ReserveLamports+debit {
		return "balance_changed", localObservation, evidence, effective, nil
	}
	return "", localObservation, evidence, effective, nil
}

func (e *Engine) observe(ctx context.Context, source string) (agent.NodeObservation, error) {
	observation, err := e.observer.Observe(ctx, source)
	if err != nil {
		appendErr := e.recordObservationFailure("observer_error")
		if appendErr != nil {
			return agent.NodeObservation{}, fmt.Errorf("record failed Mithril observation: %w", appendErr)
		}
		return agent.NodeObservation{}, err
	}
	if err := validateNodeObservationShape(observation, source); err != nil {
		if appendErr := e.recordObservationFailure("invalid_observation"); appendErr != nil {
			return agent.NodeObservation{}, fmt.Errorf(
				"record invalid Mithril observation: %w",
				appendErr,
			)
		}
		return agent.NodeObservation{}, err
	}
	return observation, nil
}

func (e *Engine) recordObservationFailure(reason string) error {
	records := e.store.Records()
	for index := len(records) - 1; index >= 0; index-- {
		if records[index].Type == EventNodeObservationFailed {
			return nil
		}
		if records[index].Type == EventNodeObserved {
			break
		}
	}
	_, err := e.store.Append(
		e.now().UTC(),
		EventNodeObservationFailed,
		"",
		nodeObservationFailed{Reason: reason},
	)
	return err
}

func (e *Engine) checkClock(maxUncertainty time.Duration) error {
	sample, err := e.clock()
	if err != nil {
		return err
	}
	if err := validateClockSample(sample, e.now().UTC(), maxUncertainty); err != nil {
		return err
	}
	var previous *clockcheck.Sample
	for _, record := range e.store.Records() {
		if record.Type != EventClockAccepted {
			continue
		}
		var value clockcheck.Sample
		if err := decodePayload(record, &value); err != nil ||
			validateClockSampleShape(value, clockcheck.MaxUncertaintyCap) != nil {
			return errors.New("journal clock sample is invalid")
		}
		copy := value
		previous = &copy
	}
	if previous != nil {
		if sample.WallTime.Before(previous.WallTime) {
			return errors.New("wall clock moved backward")
		}
	}
	if previous != nil && previous.BootID == sample.BootID {
		if sample.MonotonicNanos <= previous.MonotonicNanos {
			return errors.New("monotonic clock moved backward")
		}
		wallDelta := sample.WallTime.Sub(previous.WallTime)
		monotonicDelta := time.Duration(sample.MonotonicNanos - previous.MonotonicNanos)
		difference := wallDelta - monotonicDelta
		if difference < 0 {
			difference = -difference
		}
		allowed := clockcheck.MaxOffset +
			time.Duration(previous.UncertaintyNanos) +
			time.Duration(sample.UncertaintyNanos)
		if difference > allowed {
			return errors.New("wall clock stepped relative to the monotonic clock")
		}
		if wallDelta < minClockJournalInterval {
			return nil
		}
	}
	_, err = e.store.Append(e.now().UTC(), EventClockAccepted, "", sample)
	return err
}

func validateClockSample(
	sample clockcheck.Sample,
	now time.Time,
	maxUncertainty time.Duration,
) error {
	if err := validateClockSampleShape(sample, maxUncertainty); err != nil {
		return err
	}
	if sample.WallTime.After(now.Add(clockcheck.MaxOffset)) ||
		now.Sub(sample.WallTime) > clockcheck.MaxSampleAge {
		return errors.New("kernel clock sample is stale")
	}
	return nil
}

func validateClockSampleShape(
	sample clockcheck.Sample,
	maxUncertainty time.Duration,
) error {
	if maxUncertainty < clockcheck.InitialMaxUncertainty ||
		maxUncertainty > clockcheck.MaxUncertaintyCap {
		return errors.New("kernel clock uncertainty policy is invalid")
	}
	if sample.WallTime.IsZero() || len(sample.BootID) != 36 || sample.MonotonicNanos == 0 ||
		sample.OffsetNanos < -int64(clockcheck.MaxOffset) ||
		sample.OffsetNanos > int64(clockcheck.MaxOffset) ||
		sample.UncertaintyNanos > uint64(maxUncertainty) {
		return errors.New("kernel clock sample is outside policy")
	}
	return nil
}

func safeForUTCRollover(now time.Time, maxUncertainty time.Duration) bool {
	now = now.UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	margin := clockcheck.MaxOffset + maxUncertainty
	return now.Sub(start) >= margin && end.Sub(now) > margin
}

func (e *Engine) sustainedHealthReady(
	profile agent.Profile,
	current agent.NodeObservation,
) (bool, error) {
	var previous *agent.NodeObservation
	lastWasFailure := false
	for _, record := range e.store.Records() {
		switch record.Type {
		case EventNodeObservationFailed:
			var failed nodeObservationFailed
			if err := decodePayload(record, &failed); err != nil ||
				(failed.Reason != "observer_error" && failed.Reason != "invalid_observation") {
				return false, errors.New("journal node observation failure is invalid")
			}
			previous = nil
			lastWasFailure = true
		case EventNodeObserved:
			var observation agent.NodeObservation
			if err := decodePayload(record, &observation); err != nil {
				return false, err
			}
			if err := validateNodeObservationShape(observation, profile.Source); err != nil {
				return false, err
			}
			copy := observation
			previous = &copy
			lastWasFailure = false
		}
	}
	if !healthyNodeObservation(current, profile, e.now().UTC()) {
		if previous != nil && healthyNodeObservation(*previous, profile, e.now().UTC()) {
			_, err := e.store.Append(e.now().UTC(), EventNodeObserved, "", current)
			return false, err
		}
		return false, nil
	}
	if previous == nil || lastWasFailure ||
		!healthyNodeObservation(*previous, profile, e.now().UTC()) {
		_, err := e.store.Append(e.now().UTC(), EventNodeObserved, "", current)
		return false, err
	}
	interval := current.Account.ObservedAt.Sub(previous.Account.ObservedAt)
	if interval > time.Duration(profile.MaxObservationAgeSeconds)*time.Second {
		_, err := e.store.Append(e.now().UTC(), EventNodeObserved, "", current)
		return false, err
	}
	if interval < time.Duration(profile.MinHealthyObservationSeconds)*time.Second ||
		current.Account.Slot < previous.Account.Slot ||
		current.Account.Slot-previous.Account.Slot < profile.MinHealthySlotAdvance {
		return false, nil
	}
	return true, nil
}

func validateNodeObservationShape(observation agent.NodeObservation, source string) error {
	account := observation.Account
	health := observation.Health
	if account.Cluster != "devnet" || account.Source != source || account.Slot == 0 ||
		account.ObservedAt.IsZero() || account.EvidenceSource != "mithril_mcp" ||
		account.Finality != "local_unfinalized" ||
		account.Consistency != "node_reported_non_atomic" {
		return errors.New("Mithril account observation is invalid")
	}
	if health.AssessmentScope != "point_in_time_snapshot" || health.ObservedAt.IsZero() ||
		health.SafeForAutomation || health.DivergenceArtifacts < 0 {
		return errors.New("Mithril health observation is invalid")
	}
	switch health.Status {
	case "healthy", "degraded", "critical", "unknown":
		return nil
	default:
		return errors.New("Mithril health status is invalid")
	}
}

func healthyNodeObservation(observation agent.NodeObservation, profile agent.Profile, now time.Time) bool {
	health := observation.Health
	account := observation.Account
	if health.Status != "healthy" || !health.EvidenceComplete ||
		health.DivergenceArtifacts != 0 {
		return false
	}
	now = now.UTC()
	accountAt := account.ObservedAt.UTC()
	healthAt := health.ObservedAt.UTC()
	maxAge := time.Duration(profile.MaxObservationAgeSeconds) * time.Second
	if accountAt.After(now.Add(5*time.Second)) || healthAt.After(now.Add(5*time.Second)) ||
		now.Sub(accountAt) > maxAge || now.Sub(healthAt) > maxAge ||
		healthAt.After(accountAt.Add(5*time.Second)) {
		return false
	}
	return true
}

func (e *Engine) guard() (executionGuard, error) {
	states, order, err := e.executionStates()
	if err != nil {
		return executionGuard{}, err
	}
	result := executionGuard{}
	for _, actionID := range order {
		state := states[actionID]
		if state.reconciliation == nil {
			continue
		}
		if terminalVerdict(state.reconciliation.Verdict) {
			if err := validateCompletedState(state, nil); err != nil {
				return executionGuard{}, err
			}
		}
		switch state.reconciliation.Verdict {
		case txflow.VerdictDiverged, txflow.VerdictUnresolved:
			result.halted = true
		case txflow.VerdictFinalized, txflow.VerdictFailed:
			if result.finalized == nil ||
				state.reconciliation.Slot > result.finalized.reconciliation.Slot {
				result.finalized = state
			}
		}
	}
	return result, nil
}

func (e *Engine) cancel(state *executionState, reason string) error {
	event := executionCanceled{Reason: reason}
	if _, err := e.store.Append(e.now().UTC(), EventExecutionCanceled, state.proposal.ActionID, event); err != nil {
		return err
	}
	state.canceled = true
	state.cancelReason = reason
	return nil
}

func (e *Engine) quarantine(state *executionState, reason string) error {
	if state.signed == nil || state.sendStarted != nil || !validQuarantineReason(reason) {
		return errors.New("signed transaction cannot enter quarantine")
	}
	event := transactionQuarantined{Reason: reason}
	if _, err := e.store.Append(
		e.now().UTC(),
		EventTransactionQuarantined,
		state.proposal.ActionID,
		event,
	); err != nil {
		return err
	}
	state.quarantined = true
	state.quarantineReason = reason
	return nil
}

func (e *Engine) build(ctx context.Context, proposal agent.Proposal) (builtTransaction, error) {
	latest, err := e.blockhash.LatestBlockhash(ctx, proposal.ObservationSlot)
	if err != nil {
		return builtTransaction{}, err
	}
	if latest.ContextSlot < proposal.ObservationSlot {
		return builtTransaction{}, errors.New("blockhash predates the node observation")
	}
	height, err := e.blockhash.BlockHeight(ctx)
	if err != nil {
		return builtTransaction{}, err
	}
	if height == 0 || height >= latest.LastValidBlockHeight ||
		latest.LastValidBlockHeight-height > maxBlockHeightWindow {
		return builtTransaction{}, errors.New("blockhash validity window is unsafe")
	}
	if latest.ContextSlot > proposal.ObservationSlot &&
		latest.ContextSlot-proposal.ObservationSlot > proposal.MaxNodeLagSlots {
		return builtTransaction{}, errNodeLagExceeded
	}
	message, err := solana.BuildTransferMessage(
		proposal.Source,
		proposal.Destination,
		latest.Blockhash,
		proposal.AmountLamports,
	)
	if err != nil {
		return builtTransaction{}, err
	}
	fee, err := e.tx.FeeForMessage(ctx, message, latest.ContextSlot)
	if err != nil {
		return builtTransaction{}, err
	}
	if fee.Lamports > proposal.FeeBudgetLamports {
		return builtTransaction{}, errFeeBudgetExceeded
	}
	return builtTransaction{
		MessageBase64:           base64.StdEncoding.EncodeToString(message),
		RecentBlockhash:         latest.Blockhash,
		BlockhashContextSlot:    latest.ContextSlot,
		ObservedBlockHeight:     height,
		LastValidBlockHeight:    latest.LastValidBlockHeight,
		FeeLamports:             fee.Lamports,
		FeeMinContextSlot:       fee.MinContextSlot,
		PrimaryFeeContextSlot:   fee.PrimaryContextSlot,
		SecondaryFeeContextSlot: fee.SecondaryContextSlot,
	}, nil
}

func (e *Engine) activeState() (*executionState, bool, error) {
	states, order, err := e.executionStates()
	if err != nil {
		return nil, false, err
	}
	var active *executionState
	for _, actionID := range order {
		state := states[actionID]
		if state.started == nil || state.canceled ||
			state.quarantineResolved != nil ||
			(state.reconciliation != nil && terminalVerdict(state.reconciliation.Verdict)) {
			continue
		}
		if active != nil {
			return nil, false, errors.New("journal contains multiple active executions")
		}
		active = state
	}
	return active, active != nil, nil
}

func (e *Engine) stateForAction(actionID string) (*executionState, error) {
	states, _, err := e.executionStates()
	if err != nil {
		return nil, err
	}
	return states[actionID], nil
}

func (e *Engine) executionStates() (map[string]*executionState, []string, error) {
	states := map[string]*executionState{}
	order := make([]string, 0)
	for _, record := range e.store.Records() {
		switch record.Type {
		case agent.EventActionShadowProposed, agent.EventActionShadowed,
			EventClockAccepted, EventNodeObserved, EventNodeObservationFailed,
			// The rotation marker belongs to the journal, not to any action.
			// It must be skipped here rather than typed as an event: it
			// carries no action ID, so the check below would reject it.
			journal.EventRotated:
			continue
		case agent.EventActionProposed,
			EventExecutionStarted,
			EventExecutionCanceled,
			EventTransactionBuilt,
			EventTransactionSimulated,
			EventPolicyGranted,
			EventTransactionSigned,
			EventTransactionQuarantined,
			EventQuarantineResolved,
			EventTransactionSendStarted,
			EventTransactionSubmitted,
			EventTransactionReconciled:
		default:
			return nil, nil, fmt.Errorf(
				"unknown journal event %q at sequence %d",
				record.Type,
				record.Sequence,
			)
		}
		if record.ActionID == "" {
			return nil, nil, fmt.Errorf(
				"journal event %q has no action ID at sequence %d",
				record.Type,
				record.Sequence,
			)
		}
		state := states[record.ActionID]
		if state == nil {
			state = &executionState{}
			states[record.ActionID] = state
			order = append(order, record.ActionID)
		}
		switch record.Type {
		case agent.EventActionProposed:
			if state.proposal.ActionID != "" || state.started != nil ||
				decodePayload(record, &state.proposal) != nil {
				return nil, nil, fmt.Errorf("invalid proposal at journal sequence %d", record.Sequence)
			}
		case EventExecutionStarted:
			if state.started != nil || state.proposal.ActionID == "" {
				return nil, nil, errors.New("journal execution start is out of order")
			}
			var value executionStarted
			if err := decodePayload(record, &value); err != nil ||
				validateExecutionStart(state.proposal, value) != nil {
				return nil, nil, errors.New("journal execution start is invalid")
			}
			state.started = &value
		case EventExecutionCanceled:
			if state.canceled || state.started == nil || state.sendStarted != nil ||
				state.quarantined {
				return nil, nil, errors.New("journal execution cancellation is out of order")
			}
			var value executionCanceled
			if err := decodePayload(record, &value); err != nil || !validCancelReason(value.Reason) {
				return nil, nil, errors.New("journal execution cancellation is invalid")
			}
			state.canceled = true
			state.cancelReason = value.Reason
		case EventTransactionBuilt:
			if state.built != nil || state.started == nil || state.canceled {
				return nil, nil, errors.New("journal transaction build is out of order")
			}
			var value builtTransaction
			if err := decodePayload(record, &value); err != nil {
				return nil, nil, err
			}
			state.built = &value
		case EventTransactionSimulated:
			if state.simulated != nil || state.built == nil || state.canceled {
				return nil, nil, errors.New("journal transaction simulation is out of order")
			}
			var value txflow.SimulationEvidence
			if err := decodePayload(record, &value); err != nil {
				return nil, nil, err
			}
			state.simulated = &value
		case EventPolicyGranted:
			if state.simulated == nil || state.signed != nil || state.canceled {
				return nil, nil, errors.New("journal policy grant is out of order")
			}
			var value riskgrant.Grant
			if err := decodePayload(record, &value); err != nil {
				return nil, nil, err
			}
			if state.grant != nil &&
				(record.At.UTC().Unix() < state.grant.Claims.ExpiresAtUnix ||
					value.Claims.IssuedAtUnix <= state.grant.Claims.IssuedAtUnix ||
					value.Claims.ExpiresAtUnix <= state.grant.Claims.ExpiresAtUnix) {
				return nil, nil, errors.New("journal replacement policy grant is invalid")
			}
			state.grant = &value
			state.grantAt = record.At
			state.grants = append(state.grants, recordedGrant{grant: value, at: record.At})
		case EventTransactionSigned:
			if state.signed != nil || state.grant == nil || state.canceled {
				return nil, nil, errors.New("journal transaction signature is out of order")
			}
			var value signedTransaction
			if err := decodePayload(record, &value); err != nil {
				return nil, nil, err
			}
			state.signed = &value
		case EventTransactionQuarantined:
			if state.quarantined || state.signed == nil || state.sendStarted != nil ||
				state.canceled {
				return nil, nil, errors.New("journal signed quarantine is out of order")
			}
			var value transactionQuarantined
			if err := decodePayload(record, &value); err != nil ||
				!validQuarantineReason(value.Reason) {
				return nil, nil, errors.New("journal signed quarantine is invalid")
			}
			state.quarantined = true
			state.quarantineReason = value.Reason
		case EventQuarantineResolved:
			if state.quarantineResolved != nil || !state.quarantined ||
				state.sendStarted != nil || state.submission != nil ||
				state.reconciliation != nil {
				return nil, nil, errors.New("journal quarantine resolution is out of order")
			}
			var value quarantineResolved
			if err := decodePayload(record, &value); err != nil {
				return nil, nil, err
			}
			state.quarantineResolved = &value
		case EventTransactionSendStarted:
			if state.sendStarted != nil || state.signed == nil || state.canceled ||
				state.quarantined {
				return nil, nil, errors.New("journal send marker is out of order")
			}
			var value sendStarted
			if err := decodePayload(record, &value); err != nil {
				return nil, nil, err
			}
			state.sendStarted = &value
			state.sendStartedAt = record.At
		case EventTransactionSubmitted:
			if state.submission != nil || state.sendStarted == nil {
				return nil, nil, errors.New("journal submission is out of order")
			}
			var value txflow.Submission
			if err := decodePayload(record, &value); err != nil {
				return nil, nil, err
			}
			state.submission = &value
		case EventTransactionReconciled:
			if state.submission == nil {
				return nil, nil, errors.New("journal reconciliation has no submission")
			}
			var value txflow.Reconciliation
			if err := decodePayload(record, &value); err != nil {
				return nil, nil, err
			}
			if state.reconciliation != nil && terminalVerdict(state.reconciliation.Verdict) {
				return nil, nil, errors.New("journal contains a transition after terminal reconciliation")
			}
			state.reconciliation = &value
		}
	}
	for _, actionID := range order {
		state := states[actionID]
		if state.started != nil && state.proposal.ActionID == "" {
			return nil, nil, errors.New("execution start has no proposal")
		}
		if state.built != nil && state.started == nil {
			return nil, nil, errors.New("built transaction has no execution start")
		}
		if state.simulated != nil && state.built == nil {
			return nil, nil, errors.New("simulated transaction has no built transaction")
		}
		if state.signed != nil && state.simulated == nil {
			return nil, nil, errors.New("signed transaction has no simulation")
		}
		if state.grant != nil && state.simulated == nil {
			return nil, nil, errors.New("policy grant has no simulation")
		}
		if state.signed != nil && state.grant == nil {
			return nil, nil, errors.New("signed transaction has no policy grant")
		}
		if err := e.verifyGrantHistory(state); err != nil {
			return nil, nil, err
		}
		if state.sendStarted != nil && state.signed == nil {
			return nil, nil, errors.New("send marker has no signed transaction")
		}
		if state.sendStarted != nil && validateSendStarted(state) != nil {
			return nil, nil, errors.New("send marker authorization is invalid")
		}
		if state.submission != nil && state.sendStarted == nil {
			return nil, nil, errors.New("submission has no send marker")
		}
		if state.reconciliation != nil && state.submission == nil {
			return nil, nil, errors.New("reconciliation has no submission")
		}
		if state.canceled && state.sendStarted != nil {
			return nil, nil, errors.New("canceled execution contains a send marker")
		}
		if state.quarantined && state.sendStarted != nil {
			return nil, nil, errors.New("quarantined execution contains a send marker")
		}
		if state.quarantined && validateQuarantinedState(state) != nil {
			return nil, nil, errors.New("signed quarantine is invalid")
		}
		if state.quarantineResolved != nil &&
			validateResolvedQuarantine(state) != nil {
			return nil, nil, errors.New("quarantine resolution is invalid")
		}
	}

	return states, order, nil
}

func decodePayload(record journal.Record, output any) error {
	if err := strictjson.Decode(record.Payload, output); err != nil {
		return fmt.Errorf("decode %s at journal sequence %d", record.Type, record.Sequence)
	}
	return nil
}

func signingRequest(proposal agent.Proposal, built builtTransaction) signer.Request {
	return signer.Request{
		Domain:                  signer.RequestDomain,
		Cluster:                 proposal.Cluster,
		Profile:                 proposal.Profile,
		ProfileVersion:          proposal.ProfileVersion,
		ProfileFingerprint:      proposal.ProfileFingerprint,
		ActionID:                proposal.ActionID,
		ScheduleWindowStartUnix: proposal.ScheduleWindowStartUnix,
		ScheduleWindowEndUnix:   proposal.ScheduleWindowEndUnix,
		MessageBase64:           built.MessageBase64,
		BlockhashContextSlot:    built.BlockhashContextSlot,
		FeeLamports:             built.FeeLamports,
		FeeMinContextSlot:       built.FeeMinContextSlot,
		PrimaryFeeContextSlot:   built.PrimaryFeeContextSlot,
		SecondaryFeeContextSlot: built.SecondaryFeeContextSlot,
		RecentBlockhash:         built.RecentBlockhash,
		ObservedBlockHeight:     built.ObservedBlockHeight,
		LastValidBlockHeight:    built.LastValidBlockHeight,
	}
}

func (e *Engine) verifyGrantHistory(state *executionState) error {
	if len(state.grants) == 0 {
		return nil
	}
	if state.built == nil {
		return errors.New("policy grant has no built transaction")
	}
	request := signingRequest(state.proposal, *state.built)
	for _, recorded := range state.grants {
		if err := e.authority.VerifyAt(request, recorded.grant, recorded.at); err != nil {
			return errors.New("journal policy grant verification failed")
		}
	}
	return nil
}

func boundedBalance(local uint64, evidence txflow.AccountEvidence) uint64 {
	balance := local
	if evidence.PrimaryLamports < balance {
		balance = evidence.PrimaryLamports
	}
	if evidence.SecondaryLamports < balance {
		balance = evidence.SecondaryLamports
	}
	return balance
}

func validateExecutionStart(proposal agent.Proposal, started executionStarted) error {
	evidence := started.AccountEvidence
	if started.Mode != "devnet" ||
		validateTransferAccountEvidence(proposal, evidence) != nil ||
		started.EffectiveBalanceLamports != boundedBalance(
			started.LocalAvailableLamports,
			evidence.Source,
		) {
		return errors.New("execution start balance evidence is invalid")
	}
	if proposal.ReserveLamports > ^uint64(0)-proposal.ReservedLamports ||
		started.EffectiveBalanceLamports < proposal.ReserveLamports+proposal.ReservedLamports {
		return errors.New("execution start balance is below the reserved amount")
	}
	healthAt := started.Health.ObservedAt.UTC()
	observationAt := time.Unix(proposal.ObservationUnix, 0).UTC()
	if started.Health.Status != "healthy" ||
		started.Health.AssessmentScope != "point_in_time_snapshot" ||
		started.Health.SafeForAutomation || !started.Health.EvidenceComplete ||
		started.Health.DivergenceArtifacts != 0 || healthAt.IsZero() ||
		healthAt.After(observationAt.Add(5*time.Second)) ||
		observationAt.Sub(healthAt) > 2*time.Minute {
		return errors.New("execution start health evidence is invalid")
	}
	return nil
}

func validateTransferAccountEvidence(
	proposal agent.Proposal,
	evidence txflow.TransferAccountEvidence,
) error {
	return validateTransferAccountEvidenceAt(proposal, evidence, proposal.ObservationSlot)
}

func validateTransferAccountEvidenceAt(
	proposal agent.Proposal,
	evidence txflow.TransferAccountEvidence,
	observationSlot uint64,
) error {
	if observationSlot < proposal.ObservationSlot ||
		evidence.ObservationSlot != observationSlot ||
		evidence.CommonFinalizedFloor == 0 ||
		evidence.PrimaryFinalizedSlot < evidence.CommonFinalizedFloor ||
		evidence.SecondaryFinalizedSlot < evidence.CommonFinalizedFloor ||
		validateAccountEvidence(
			evidence.Source,
			proposal.Source,
			evidence.CommonFinalizedFloor,
		) != nil ||
		validateAccountEvidence(
			evidence.Destination,
			proposal.Destination,
			evidence.CommonFinalizedFloor,
		) != nil {
		return errors.New("transfer account evidence is invalid")
	}
	if observationSlot > evidence.CommonFinalizedFloor &&
		observationSlot-evidence.CommonFinalizedFloor > proposal.MaxNodeLagSlots {
		return errors.New("independent finalized balance floor is too far behind Mithril")
	}
	return nil
}

func validateAccountEvidence(
	evidence txflow.AccountEvidence,
	address string,
	commonFloor uint64,
) error {
	systemProgram := solana.Encode(make([]byte, 32))
	if evidence.Address != address ||
		evidence.PrimaryContextSlot < commonFloor ||
		evidence.SecondaryContextSlot < commonFloor ||
		evidence.PrimaryLamports != evidence.SecondaryLamports ||
		evidence.PrimaryOwner != systemProgram ||
		evidence.SecondaryOwner != systemProgram ||
		evidence.PrimaryExecutable || evidence.SecondaryExecutable ||
		evidence.PrimaryDataLength != 0 || evidence.SecondaryDataLength != 0 {
		return errors.New("account evidence is invalid")
	}
	return nil
}

func validateProposalForProfile(proposal agent.Proposal, profile agent.Profile) error {
	if err := validateProposalShape(proposal); err != nil {
		return err
	}
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		return err
	}
	if proposal.Profile != profile.Name ||
		proposal.ProfileVersion != profile.Version || proposal.Cluster != profile.Cluster ||
		proposal.ProfileFingerprint != fingerprint ||
		proposal.Source != profile.Source || proposal.Destination != profile.Destination ||
		proposal.AmountLamports < profile.MinTransferLamports ||
		proposal.AmountLamports > profile.MaxTransferLamports ||
		proposal.FeeBudgetLamports != profile.MaxFeeLamports ||
		proposal.ReserveLamports != profile.ReserveLamports ||
		proposal.ScheduleWindowEndUnix-proposal.ScheduleWindowStartUnix !=
			int64(profile.ScheduleWindowSeconds) ||
		proposal.ScheduleWindowStartUnix < profile.ScheduleAnchorUnix ||
		(proposal.ScheduleWindowStartUnix-profile.ScheduleAnchorUnix)%
			int64(profile.ScheduleWindowSeconds) != 0 ||
		proposal.MaxObservationAgeSeconds != profile.MaxObservationAgeSeconds ||
		proposal.MaxNodeLagSlots != profile.MaxNodeLagSlots ||
		proposal.MaxReconciliationSeconds != profile.MaxReconciliationSeconds {
		return errors.New("journal proposal is outside the active profile")
	}
	if proposal.ObservedBalanceLamports < proposal.ReserveLamports ||
		proposal.ObservedBalanceLamports-proposal.ReserveLamports < proposal.ReservedLamports {
		return errors.New("journal proposal would violate the reserve")
	}
	return nil
}

func validateProposalShape(proposal agent.Proposal) error {
	if proposal.ActionID == "" || proposal.Profile != agent.ProfileTreasurySweepV1 ||
		proposal.ProfileVersion != 1 || proposal.Cluster != "devnet" ||
		proposal.AmountLamports == 0 || proposal.FeeBudgetLamports == 0 ||
		proposal.ReservedLamports < proposal.AmountLamports ||
		proposal.ReservedLamports-proposal.AmountLamports != proposal.FeeBudgetLamports ||
		proposal.ReserveLamports > ^uint64(0)-proposal.ReservedLamports ||
		proposal.ObservedBalanceLamports == 0 ||
		proposal.ObservedBalanceLamports < proposal.ReserveLamports+proposal.ReservedLamports ||
		proposal.ObservationSlot == 0 ||
		proposal.ObservationUnix == 0 || proposal.ReservationDayUTC == "" ||
		proposal.ScheduleWindowStartUnix <= 0 ||
		proposal.ScheduleWindowEndUnix <= proposal.ScheduleWindowStartUnix ||
		proposal.MaxObservationAgeSeconds == 0 ||
		proposal.MaxObservationAgeSeconds > 15*60 ||
		proposal.MaxNodeLagSlots == 0 || proposal.MaxNodeLagSlots > 1_000 ||
		proposal.MaxReconciliationSeconds < 30 ||
		proposal.MaxReconciliationSeconds > 3_600 {
		return errors.New("journal proposal is invalid")
	}
	expectedActionID, err := agent.ComputeActionID(
		proposal.ProfileFingerprint,
		proposal.ScheduleWindowStartUnix,
	)
	if err != nil || expectedActionID != proposal.ActionID {
		return errors.New("journal proposal action identity is invalid")
	}
	if _, err := solana.Decode32(proposal.Source); err != nil {
		return errors.New("journal proposal source is invalid")
	}
	if _, err := solana.Decode32(proposal.Destination); err != nil ||
		proposal.Source == proposal.Destination {
		return errors.New("journal proposal destination is invalid")
	}
	return nil
}

func validateBuilt(proposal agent.Proposal, built builtTransaction) error {
	if built.BlockhashContextSlot < proposal.ObservationSlot {
		return errors.New("journal blockhash predates the node observation")
	}
	if built.ObservedBlockHeight == 0 ||
		built.ObservedBlockHeight >= built.LastValidBlockHeight ||
		built.LastValidBlockHeight-built.ObservedBlockHeight > maxBlockHeightWindow {
		return errors.New("journal blockhash validity window is unsafe")
	}
	if built.BlockhashContextSlot == 0 ||
		built.FeeLamports == 0 || built.FeeLamports > proposal.FeeBudgetLamports ||
		built.FeeMinContextSlot != built.BlockhashContextSlot ||
		built.PrimaryFeeContextSlot < built.FeeMinContextSlot ||
		built.SecondaryFeeContextSlot < built.FeeMinContextSlot {
		return errors.New("journal message fee evidence is invalid")
	}
	message, err := base64.StdEncoding.Strict().DecodeString(built.MessageBase64)
	if err != nil {
		return errors.New("journal message is not canonical base64")
	}
	decoded, err := solana.DecodeTransferMessage(message)
	if err != nil {
		return errors.New("journal transfer message is invalid")
	}
	independent, err := independentdecode.DecodeMessage(message)
	if err != nil ||
		independent.Source != decoded.Source ||
		independent.Destination != decoded.Destination ||
		independent.RecentBlockhash != decoded.RecentBlockhash ||
		independent.Lamports != decoded.Lamports {
		return errors.New("independent transfer decode disagrees")
	}
	if solana.Encode(decoded.Source[:]) != proposal.Source ||
		solana.Encode(decoded.Destination[:]) != proposal.Destination ||
		solana.Encode(decoded.RecentBlockhash[:]) != built.RecentBlockhash ||
		decoded.Lamports != proposal.AmountLamports {
		return errors.New("journal transfer message does not match proposal")
	}
	return nil
}

func validateSimulation(built builtTransaction, simulation txflow.SimulationEvidence) error {
	if simulation.ProviderIdentity == "" ||
		simulation.MinContextSlot != built.BlockhashContextSlot ||
		simulation.ContextSlot < simulation.MinContextSlot ||
		simulation.SourcePostLamports == 0 ||
		simulation.DestinationPostLamports == 0 ||
		!validHexDigest(simulation.LogsSHA256) ||
		!validHexDigest(simulation.AccountsSHA256) {
		return errors.New("journal transaction simulation evidence is invalid")
	}
	return nil
}

func validHexDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size &&
		hex.EncodeToString(decoded) == value &&
		!bytes.Equal(decoded, make([]byte, sha256.Size))
}

func validateSigned(proposal agent.Proposal, built builtTransaction, signed signedTransaction) error {
	response := signed.SignerResponse
	if response.ActionID != proposal.ActionID ||
		response.BlockhashContextSlot != built.BlockhashContextSlot ||
		response.LastValidBlockHeight != built.LastValidBlockHeight ||
		response.FeeLamports != built.FeeLamports {
		return errors.New("journal signer response has the wrong identity")
	}
	message, err := base64.StdEncoding.Strict().DecodeString(built.MessageBase64)
	if err != nil {
		return errors.New("journal message is not canonical base64")
	}
	messageHash := sha256.Sum256(message)
	if response.MessageSHA256 != hex.EncodeToString(messageHash[:]) ||
		!validHexDigest(response.TransactionSHA256) {
		return errors.New("journal signer response has invalid hashes")
	}
	signature, err := solana.Decode64(response.Signature)
	if err != nil {
		return errors.New("journal signer response has an invalid signature")
	}
	source, err := solana.Decode32(proposal.Source)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(source[:]), message, signature[:]) {
		return errors.New("journal signer signature does not match its message")
	}
	metadata := sealedtx.Metadata{
		Version:              sealedtx.Version,
		Domain:               sealedtx.Domain,
		ActionID:             response.ActionID,
		MessageSHA256:        response.MessageSHA256,
		TransactionSHA256:    response.TransactionSHA256,
		Signature:            response.Signature,
		BlockhashContextSlot: response.BlockhashContextSlot,
		FeeLamports:          response.FeeLamports,
		LastValidBlockHeight: response.LastValidBlockHeight,
	}
	if response.SealedTransaction.Metadata != metadata ||
		response.SealedTransaction.EphemeralPublicKeyBase64 == "" ||
		response.SealedTransaction.NonceBase64 == "" ||
		response.SealedTransaction.CiphertextBase64 == "" {
		return errors.New("journal sealed transaction does not match signer response")
	}
	if err := signer.VerifyResponseAttestation(
		proposal.Source, response.SignerAttestation.SubmitterPublicKey, response,
	); err != nil {
		return errors.New("journal sealed transaction attestation is invalid")
	}
	return nil
}

func expectedTransaction(state *executionState) txflow.ExpectedTransaction {
	return txflow.ExpectedTransaction{
		Signature:         state.signed.SignerResponse.Signature,
		TransactionSHA256: state.signed.SignerResponse.TransactionSHA256,
		Source:            state.proposal.Source,
		Destination:       state.proposal.Destination,
		AmountLamports:    state.proposal.AmountLamports,
	}
}

func validateSubmission(submission txflow.Submission, signed signedTransaction, built builtTransaction) error {
	if submission.Signature != signed.SignerResponse.Signature ||
		submission.LastValidBlockHeight != built.LastValidBlockHeight ||
		(submission.State != txflow.StateAccepted && submission.State != txflow.StateAmbiguous) {
		return errors.New("journal submission does not match the signed transaction")
	}
	return nil
}

func validateSendStarted(state *executionState) error {
	if state.started == nil || state.built == nil || state.signed == nil ||
		state.sendStarted == nil {
		return errors.New("send marker is missing prerequisite state")
	}
	started := state.sendStarted
	if started.Signature != state.signed.SignerResponse.Signature ||
		started.AuthorizedAt.IsZero() ||
		started.AuthorizedAt.Before(time.Unix(state.proposal.ObservationUnix, 0).UTC()) ||
		validateFreshNodeObservation(
			state.proposal,
			started.LocalObservation,
			started.AuthorizedAt,
		) != nil ||
		validateTransferAccountEvidenceAt(
			state.proposal,
			started.AccountEvidence,
			started.LocalObservation.Account.Slot,
		) != nil ||
		started.EffectiveBalanceLamports != boundedBalance(
			started.LocalObservation.Account.BalanceLamports,
			started.AccountEvidence.Source,
		) {
		return errors.New("send marker authorization is invalid")
	}
	if state.proposal.AmountLamports > ^uint64(0)-state.built.FeeLamports {
		return errors.New("send marker debit overflows")
	}
	debit := state.proposal.AmountLamports + state.built.FeeLamports
	if state.proposal.ReserveLamports > ^uint64(0)-debit ||
		started.EffectiveBalanceLamports < state.proposal.ReserveLamports+debit {
		return errors.New("send marker balance is below the authorized debit")
	}
	return nil
}

func validateFreshNodeObservation(
	proposal agent.Proposal,
	observation agent.NodeObservation,
	authorizedAt time.Time,
) error {
	if err := validateNodeObservationShape(observation, proposal.Source); err != nil {
		return err
	}
	if observation.Account.Slot < proposal.ObservationSlot {
		return errors.New("fresh Mithril observation regressed")
	}
	authorizedAt = authorizedAt.UTC()
	accountAt := observation.Account.ObservedAt.UTC()
	healthAt := observation.Health.ObservedAt.UTC()
	maxAge := time.Duration(proposal.MaxObservationAgeSeconds) * time.Second
	if observation.Health.Status != "healthy" ||
		!observation.Health.EvidenceComplete ||
		observation.Health.DivergenceArtifacts != 0 ||
		accountAt.After(authorizedAt.Add(5*time.Second)) ||
		healthAt.After(authorizedAt.Add(5*time.Second)) ||
		authorizedAt.Sub(accountAt) > maxAge ||
		authorizedAt.Sub(healthAt) > maxAge ||
		healthAt.After(accountAt.Add(5*time.Second)) {
		return errors.New("fresh Mithril observation is unhealthy or stale")
	}
	return nil
}

func validateReconciliation(reconciliation txflow.Reconciliation, state *executionState) error {
	if state.submission == nil || reconciliation.Signature != state.submission.Signature {
		return errors.New("journal reconciliation has the wrong signature")
	}
	switch reconciliation.Verdict {
	case txflow.VerdictPending:
		if reconciliation.Effects != nil {
			return errors.New("journal pending reconciliation contains effects")
		}
		return nil
	case txflow.VerdictFinalized:
		if !matchingFinalizedEvidence(reconciliation, false) ||
			validateEffectEvidence(reconciliation, state, false) != nil {
			return errors.New("journal finalized reconciliation lacks matching evidence")
		}
		return nil
	case txflow.VerdictFailed:
		if !matchingFinalizedEvidence(reconciliation, true) ||
			reconciliation.PrimaryErrorFingerprint == "" ||
			reconciliation.PrimaryErrorFingerprint != reconciliation.SecondaryErrorFingerprint ||
			validateEffectEvidence(reconciliation, state, true) != nil {
			return errors.New("journal failed reconciliation lacks matching evidence")
		}
		return nil
	case txflow.VerdictUnresolved:
		if reconciliation.Effects != nil ||
			reconciliation.PrimaryBlockHeight <= state.submission.LastValidBlockHeight ||
			reconciliation.SecondaryBlockHeight <= state.submission.LastValidBlockHeight ||
			(reconciliation.PrimaryFound && reconciliation.SecondaryFound) {
			return errors.New("journal unresolved reconciliation lacks expiry evidence")
		}
		return nil
	case txflow.VerdictDiverged:
		if reconciliation.Effects != nil ||
			!reconciliation.PrimaryFound || !reconciliation.SecondaryFound {
			return errors.New("journal divergence lacks conflicting evidence")
		}
		switch reconciliation.DivergenceKind {
		case txflow.DivergenceStatus:
			if reconciliation.PrimarySlot == reconciliation.SecondarySlot &&
				reconciliation.PrimaryFailed == reconciliation.SecondaryFailed &&
				reconciliation.PrimaryErrorFingerprint == reconciliation.SecondaryErrorFingerprint {
				return errors.New("journal status divergence lacks conflicting evidence")
			}
			return nil
		case txflow.DivergenceEffects:
			if !matchingFinalizedEvidence(reconciliation, reconciliation.PrimaryFailed) {
				return errors.New("journal effect divergence lacks matching finalized status")
			}
			return nil
		default:
			return errors.New("journal divergence kind is invalid")
		}
	default:
		return errors.New("journal reconciliation has an invalid verdict")
	}
}

func validateEffectEvidence(
	reconciliation txflow.Reconciliation,
	state *executionState,
	failed bool,
) error {
	if reconciliation.Effects == nil || state.built == nil || state.signed == nil {
		return errors.New("transaction effect evidence is missing")
	}
	effects := reconciliation.Effects
	if effects.TransactionSHA256 != state.signed.SignerResponse.TransactionSHA256 ||
		effects.FeeLamports != state.built.FeeLamports ||
		effects.PrimaryEffectSlot != reconciliation.Slot ||
		effects.SecondaryEffectSlot != reconciliation.Slot ||
		effects.SourcePostLamports > effects.SourcePreLamports ||
		effects.DestinationPostLamports < effects.DestinationPreLamports {
		return errors.New("transaction effect evidence is inconsistent")
	}
	sourceDebit := state.built.FeeLamports
	destinationCredit := uint64(0)
	if !failed {
		if state.proposal.AmountLamports > ^uint64(0)-sourceDebit {
			return errors.New("transaction effect debit overflows")
		}
		sourceDebit += state.proposal.AmountLamports
		destinationCredit = state.proposal.AmountLamports
	}
	if effects.SourcePreLamports-effects.SourcePostLamports != sourceDebit ||
		effects.DestinationPostLamports-effects.DestinationPreLamports != destinationCredit {
		return errors.New("transaction effects do not match the proposal")
	}
	return nil
}

func matchingFinalizedEvidence(reconciliation txflow.Reconciliation, failed bool) bool {
	fingerprintsMatch := reconciliation.PrimaryErrorFingerprint == "" &&
		reconciliation.SecondaryErrorFingerprint == ""
	if failed {
		fingerprintsMatch = reconciliation.PrimaryErrorFingerprint != "" &&
			reconciliation.PrimaryErrorFingerprint == reconciliation.SecondaryErrorFingerprint
	}
	return reconciliation.Slot > 0 &&
		reconciliation.PrimaryFound && reconciliation.SecondaryFound &&
		reconciliation.PrimarySlot == reconciliation.Slot &&
		reconciliation.SecondarySlot == reconciliation.Slot &&
		reconciliation.PrimaryStatus == "finalized" &&
		reconciliation.SecondaryStatus == "finalized" &&
		reconciliation.PrimaryFailed == failed &&
		reconciliation.SecondaryFailed == failed &&
		fingerprintsMatch
}

func validateCompletedState(state *executionState, profile *agent.Profile) error {
	if profile != nil {
		if err := validateProposalForProfile(state.proposal, *profile); err != nil {
			return err
		}
	} else if err := validateProposalShape(state.proposal); err != nil {
		return err
	}
	if state.canceled {
		if !validCancelReason(state.cancelReason) || state.sendStarted != nil ||
			state.submission != nil || state.reconciliation != nil ||
			state.signed != nil || state.quarantined {
			return errors.New("canceled execution contains invalid state")
		}
		if state.built != nil {
			if err := validateBuilt(state.proposal, *state.built); err != nil {
				return err
			}
		}
		if state.simulated != nil {
			if state.built == nil {
				return errors.New("canceled execution simulated without a built transaction")
			}
			if err := validateSimulation(*state.built, *state.simulated); err != nil {
				return err
			}
		}
		return nil
	}
	if state.built == nil || state.simulated == nil || state.signed == nil ||
		state.sendStarted == nil || state.sendStartedAt.IsZero() ||
		state.submission == nil || state.reconciliation == nil {
		return errors.New("terminal execution is missing transaction state")
	}
	if err := validateBuilt(state.proposal, *state.built); err != nil {
		return err
	}
	if err := validateSimulation(*state.built, *state.simulated); err != nil {
		return err
	}
	if err := validateSigned(state.proposal, *state.built, *state.signed); err != nil {
		return err
	}
	if err := validateSendStarted(state); err != nil {
		return err
	}
	if err := validateSubmission(*state.submission, *state.signed, *state.built); err != nil {
		return err
	}
	return validateReconciliation(*state.reconciliation, state)
}

func validateQuarantinedState(state *executionState) error {
	if !state.quarantined || !validQuarantineReason(state.quarantineReason) ||
		state.started == nil || state.built == nil || state.simulated == nil ||
		state.signed == nil || state.canceled || state.sendStarted != nil ||
		state.submission != nil || state.reconciliation != nil {
		return errors.New("signed quarantine contains invalid state")
	}
	if err := validateProposalShape(state.proposal); err != nil {
		return err
	}
	if err := validateExecutionStart(state.proposal, *state.started); err != nil {
		return err
	}
	if err := validateBuilt(state.proposal, *state.built); err != nil {
		return err
	}
	if err := validateSimulation(*state.built, *state.simulated); err != nil {
		return err
	}
	return validateSigned(state.proposal, *state.built, *state.signed)
}

func validateResolvedQuarantine(state *executionState) error {
	if err := validateQuarantinedState(state); err != nil {
		return err
	}
	resolved := state.quarantineResolved
	if resolved == nil ||
		resolved.QuarantineReason != state.quarantineReason ||
		resolved.LastValidBlockHeight != state.built.LastValidBlockHeight ||
		resolved.ObservedBlockHeight <= resolved.LastValidBlockHeight {
		return errors.New("quarantine resolution lacks blockhash-expiry evidence")
	}
	return nil
}

func validCancelReason(reason string) bool {
	switch reason {
	case "observation_expired_before_build",
		"observation_expired_before_signing",
		"observation_expired_before_submission",
		"node_lag_exceeded_before_build",
		"fee_exceeds_profile",
		"reservation_day_expired_before_signing",
		"reservation_day_expired_before_submission",
		"schedule_window_expired_before_build",
		"schedule_window_expired_before_signing",
		"schedule_window_expired_before_submission",
		"utc_rollover_guard_before_signing",
		"utc_rollover_guard_before_submission",
		"operator_stop_before_signing",
		"operator_stop_before_submission",
		"balance_changed_before_signing",
		"balance_changed_before_submission",
		"blockhash_expired_before_signing",
		"blockhash_expired_before_submission":
		return true
	default:
		return false
	}
}

func validQuarantineReason(reason string) bool {
	switch reason {
	case "observation_expired_before_submission",
		"reservation_day_expired_before_submission",
		"schedule_window_expired_before_submission",
		"utc_rollover_guard_before_submission",
		"operator_stop_before_submission",
		"balance_changed_before_submission",
		"blockhash_expired_before_submission":
		return true
	default:
		return false
	}
}

func proposalExpired(proposal agent.Proposal, profile agent.Profile, now time.Time) bool {
	observedAt := time.Unix(proposal.ObservationUnix, 0).UTC()
	return now.Sub(observedAt) > time.Duration(profile.MaxObservationAgeSeconds)*time.Second
}

func proposalDayExpired(proposal agent.Proposal, now time.Time) bool {
	return proposal.ReservationDayUTC != now.UTC().Format(time.DateOnly)
}

func proposalWindowExpired(proposal agent.Proposal, now time.Time) bool {
	unix := now.UTC().Unix()
	return unix < proposal.ScheduleWindowStartUnix ||
		unix >= proposal.ScheduleWindowEndUnix
}

func terminalVerdict(verdict string) bool {
	switch verdict {
	case txflow.VerdictFinalized, txflow.VerdictFailed,
		txflow.VerdictUnresolved, txflow.VerdictDiverged:
		return true
	default:
		return false
	}
}

func shouldRecordReconciliation(previous *txflow.Reconciliation, current txflow.Reconciliation) bool {
	if !terminalVerdict(current.Verdict) {
		return false
	}
	if previous == nil {
		return true
	}
	return previous.Verdict != current.Verdict ||
		previous.Slot != current.Slot ||
		previous.PrimaryStatus != current.PrimaryStatus ||
		previous.SecondaryStatus != current.SecondaryStatus
}

func resultFromState(state *executionState, recovered bool) Result {
	result := Result{
		ActionID:       state.proposal.ActionID,
		Decision:       "executing",
		AmountLamports: state.proposal.AmountLamports,
		Recovered:      recovered,
	}
	if state.canceled {
		result.Decision = "canceled"
		result.Reason = state.cancelReason
	}
	if state.quarantined {
		result.Decision = "halted"
		result.Reason = state.quarantineReason
	}
	if state.quarantineResolved != nil {
		result.Decision = "canceled"
		result.Reason = "quarantine_resolved_after_blockhash_expiry"
	}
	if state.signed != nil && !state.quarantined {
		result.Signature = state.signed.SignerResponse.Signature
	}
	result.Submitted = state.submission != nil
	if state.reconciliation != nil {
		result.Verdict = state.reconciliation.Verdict
		switch result.Verdict {
		case txflow.VerdictFinalized:
			result.Decision = "complete"
		case txflow.VerdictFailed:
			result.Decision = "failed"
		case txflow.VerdictUnresolved, txflow.VerdictDiverged:
			result.Decision = "halted"
		}
	}
	if result.Decision == "executing" && !state.sendStartedAt.IsZero() {
		result.PendingSinceUnix = state.sendStartedAt.Unix()
		result.ReconciliationTimeoutSeconds = state.proposal.MaxReconciliationSeconds
	}
	return result
}
