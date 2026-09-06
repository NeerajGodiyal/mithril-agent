package submitter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const (
	recoveryVersion              = uint32(1)
	legacyJupiterRecoveryVersion = uint32(5)
	jupiterRecoveryVersion       = uint32(6)
	jupiterRecoveryStatus        = "mithril-agent/mainnet-recovery-status-v2"
	maxRecoveryBytes             = int64(signer.MaxRequestBytes + 64<<10)
	maxTransaction               = 2048
	maxJupiterSendAttempts       = uint32(2)
)

type recoveryRecord struct {
	Version            uint32                     `json:"version"`
	ActionID           string                     `json:"action_id"`
	ProfileFingerprint string                     `json:"profile_sha256"`
	TransactionBase64  string                     `json:"transaction_base64"`
	FeeLamports        uint64                     `json:"fee_lamports"`
	RequestSHA256      string                     `json:"request_sha256"`
	BlockhashContext   uint64                     `json:"blockhash_context_slot"`
	SignerAttestation  signer.ResponseAttestation `json:"signer_attestation"`
	RecoveryMode       string                     `json:"recovery_mode,omitempty"`
	Submission         txflow.Submission          `json:"submission"`
	JupiterRequest     *signer.Request            `json:"jupiter_request,omitempty"`
	SendStarted        bool                       `json:"send_started,omitempty"`
	SendAttempts       uint32                     `json:"send_attempts,omitempty"`
	Finalized          bool                       `json:"finalized"`
	Reconciliation     *txflow.Reconciliation     `json:"reconciliation,omitempty"`
}

type decodedTransaction struct {
	message       []byte
	signature     string
	requestSHA256 string
	transfer      *txflow.ExpectedTransaction
	swap          *txflow.ExpectedSwap
	buy           *txflow.ExpectedBuy
	jupiter       *txflow.ExpectedJupiter
}

// JupiterRecoveryStatus is the bounded, non-secret operator projection of one
// validated submitter-owned Mainnet recovery record.
type JupiterRecoveryStatus struct {
	Format                string `json:"format"`
	ActionID              string `json:"action_id"`
	RecoveryMode          string `json:"recovery_mode"`
	SendStarted           bool   `json:"send_started"`
	SendAttempts          uint32 `json:"send_attempts"`
	SendAttemptLimit      uint32 `json:"send_attempt_limit"`
	SendAttemptsRemaining uint32 `json:"send_attempts_remaining"`
	Finalized             bool   `json:"finalized"`
	FinalizedVerdict      string `json:"finalized_verdict,omitempty"`
}

// JupiterReadinessEvidence is the complete read-only boundary used to
// requalify an immutable Mainnet proposal immediately before submission.
type JupiterReadinessEvidence interface {
	proposalcheck.Evidence
	MithrilNodeIdentity() string
	VerifyIndependentBlockhashValidity(context.Context, string, uint64) error
}

// JupiterSubmitNode adds the one broadcast capability used only after the
// Mainnet canary and durable recovery barriers have both been crossed.
type JupiterSubmitNode interface {
	Identity() string
	SendTransaction(context.Context, []byte, uint64) (string, error)
}

// ReconcileRecovery independently verifies the exact transaction persisted by
// the submitter. Only a finalized success or failure with matching effects is
// made durable.
func ReconcileRecovery(
	ctx context.Context,
	policy Policy,
	lifecycle *txflow.Lifecycle,
) (string, txflow.Reconciliation, error) {
	if err := validateRecoveryPolicy(policy); err != nil {
		return "", txflow.Reconciliation{}, err
	}
	if err := validateRecoveryEvidence(policy); err != nil {
		return "", txflow.Reconciliation{}, err
	}
	if lifecycle == nil {
		return "", txflow.Reconciliation{}, errors.New("recovery lifecycle is required")
	}
	primary, secondary := lifecycle.EvidenceProviderIdentities()
	if primary != policy.Evidence.PrimaryOriginSHA256 ||
		secondary != policy.Evidence.SecondaryOriginSHA256 {
		return "", txflow.Reconciliation{}, errors.New("recovery evidence providers do not match policy")
	}
	var actionID string
	var result txflow.Reconciliation
	err := withRecoveryLock(policy, func() error {
		record, transaction, decoded, err := readRecovery(policy)
		defer clear(transaction)
		if err != nil {
			return err
		}
		if decoded.jupiter != nil && !record.SendStarted {
			return errors.New("Mainnet submission has not started")
		}
		switch {
		case decoded.transfer != nil:
			result, err = lifecycle.ReconcileExpected(
				ctx, record.Submission, *decoded.transfer, record.FeeLamports,
			)
		case decoded.swap != nil:
			result, err = lifecycle.ReconcileSwapExpected(
				ctx, record.Submission, *decoded.swap, record.FeeLamports,
			)
		case decoded.buy != nil:
			result, err = lifecycle.ReconcileBuyExpected(
				ctx, record.Submission, *decoded.buy, record.FeeLamports,
			)
		case decoded.jupiter != nil:
			result, err = lifecycle.ReconcileJupiterExpected(
				ctx, record.Submission, *decoded.jupiter, record.FeeLamports,
			)
		default:
			return errors.New("recovery transaction type is invalid")
		}
		actionID = record.ActionID
		if err != nil || result.Verdict != txflow.VerdictFinalized &&
			result.Verdict != txflow.VerdictFailed {
			return err
		}
		record.Finalized = true
		if decoded.jupiter != nil {
			record.Version = jupiterRecoveryVersion
			record.Reconciliation = &result
		}
		if err := writeRecovery(policy, record); err != nil {
			actionID = ""
			result = txflow.Reconciliation{}
			return errors.New("persist finalized recovery evidence")
		}
		if decoded.jupiter != nil {
			if err := archiveFinalizedRecovery(policy, record.ActionID); err != nil {
				actionID = ""
				result = txflow.Reconciliation{}
				return err
			}
		}
		return nil
	})
	return actionID, result, err
}

// CheckJupiterRecoveryReadiness revalidates one offline-prepared Mainnet record
// against Mithril and both bound evidence providers. It returns only the public
// action ID, never transaction bytes, and cannot mark or submit the action.
func CheckJupiterRecoveryReadiness(
	ctx context.Context,
	policy Policy,
	evidence JupiterReadinessEvidence,
	primary, secondary proposalcheck.FinalizedSlotReader,
) (string, error) {
	return checkJupiterRecoveryReadinessAt(
		ctx, policy, evidence, primary, secondary, false, time.Now,
	)
}

// ReadJupiterRecoveryStatus returns only the public action identifier and
// bounded send state. It does not open an RPC, change recovery, or expose the
// transaction, signature, signer request, policy, or attestation.
func ReadJupiterRecoveryStatus(policy Policy) (JupiterRecoveryStatus, error) {
	if err := ValidateJupiterPolicy(policy); err != nil {
		return JupiterRecoveryStatus{}, err
	}
	var status JupiterRecoveryStatus
	err := withRecoveryLock(policy, func() error {
		record, transaction, decoded, err := readRecovery(policy)
		defer clear(transaction)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return errors.New("Mainnet recovery record is unavailable")
			}
			return errors.New("Mainnet recovery record is invalid or unsafe")
		}
		if decoded.jupiter == nil {
			return errors.New("Mainnet recovery record is invalid")
		}
		limit := uint32(1)
		if record.RecoveryMode == MainnetRecoveryExactRetry {
			limit = maxJupiterSendAttempts
		}
		remaining := limit - record.SendAttempts
		if record.Finalized {
			remaining = 0
		}
		verdict := ""
		if record.Reconciliation != nil {
			verdict = record.Reconciliation.Verdict
		}
		status = JupiterRecoveryStatus{
			Format: jupiterRecoveryStatus, ActionID: record.ActionID,
			RecoveryMode: record.RecoveryMode, SendStarted: record.SendStarted,
			SendAttempts: record.SendAttempts, SendAttemptLimit: limit,
			SendAttemptsRemaining: remaining, Finalized: record.Finalized,
			FinalizedVerdict: verdict,
		}
		return nil
	})
	return status, err
}

// JupiterFinalizedEvidence is a read-only terminal projection, not inventory or
// permission to release a claim. InputSpent and OutputReceived are zero on
// finalized failure; FeeLamports still records the verified paid fee. Amounts
// are base units. Fees/rent are separate from swap amounts, not net wallet deltas.
type JupiterFinalizedEvidence struct {
	ActionID            string `json:"action_id"`
	RequestSHA256       string `json:"request_sha256"`
	TransactionSHA256   string `json:"transaction_sha256"`
	Verdict             string `json:"verdict"`
	FinalizedSlot       uint64 `json:"finalized_slot"`
	PrimaryEffectSlot   uint64 `json:"primary_effect_slot"`
	SecondaryEffectSlot uint64 `json:"secondary_effect_slot"`
	InputMint           string `json:"input_mint"`
	OutputMint          string `json:"output_mint"`
	InputSpent          uint64 `json:"input_spent,string"`
	OutputReceived      uint64 `json:"output_received,string"`
	FeeLamports         uint64 `json:"fee_lamports,string"`
	OutputAccountRent   uint64 `json:"output_account_rent_lamports,string"`
}

// ReadJupiterFinalizedEvidence binds validated durable terminal effects to the
// caller's exact unsigned request. It uses no RPC and cannot sign, submit,
// modify recovery, release a claim or credit strategy inventory.
func ReadJupiterFinalizedEvidence(policy Policy, expected signer.Request) (JupiterFinalizedEvidence, error) {
	if err := ValidateJupiterPolicy(policy); err != nil {
		return JupiterFinalizedEvidence{}, err
	}
	if expected.RiskGrant != (riskgrant.Grant{}) {
		return JupiterFinalizedEvidence{}, errors.New("finalized evidence requires an unsigned ungranted request")
	}
	var result JupiterFinalizedEvidence
	err := withRecoveryLock(policy, func() error {
		record, transaction, decoded, err := readRecovery(policy)
		defer clear(transaction)
		if err != nil {
			return err
		}
		if decoded.jupiter == nil || record.Version != jupiterRecoveryVersion || !record.Finalized || record.Reconciliation == nil {
			return errors.New("verified Jupiter terminal effects are unavailable")
		}
		messageHash := sha256.Sum256(decoded.message)
		binding, err := signer.RiskBinding(expected, hex.EncodeToString(messageHash[:]))
		if err != nil || binding.RequestSHA256 != record.RequestSHA256 || binding.ActionID != record.ActionID {
			return errors.New("finalized Jupiter evidence does not match the expected request")
		}
		reconciliation := record.Reconciliation
		effects := reconciliation.JupiterEffects
		result = JupiterFinalizedEvidence{
			ActionID: record.ActionID, RequestSHA256: record.RequestSHA256,
			TransactionSHA256: effects.TransactionSHA256, Verdict: reconciliation.Verdict,
			FinalizedSlot: reconciliation.Slot, PrimaryEffectSlot: effects.PrimaryEffectSlot,
			SecondaryEffectSlot: effects.SecondaryEffectSlot,
			InputMint:           decoded.jupiter.Policy.InputMint, OutputMint: decoded.jupiter.Policy.OutputMint,
			OutputReceived: effects.OutputAmount, FeeLamports: effects.FeeLamports,
			OutputAccountRent: effects.OutputAccountRent,
		}
		if reconciliation.Verdict == txflow.VerdictFinalized {
			result.InputSpent = effects.InputAmount
		}
		return nil
	})
	if err != nil {
		return JupiterFinalizedEvidence{}, err
	}
	return result, nil
}

func checkJupiterRecoveryReadinessAt(
	ctx context.Context,
	policy Policy,
	evidence JupiterReadinessEvidence,
	primary, secondary proposalcheck.FinalizedSlotReader,
	allowStarted bool,
	now func() time.Time,
) (string, error) {
	if err := ValidateJupiterPolicy(policy); err != nil {
		return "", err
	}
	if now == nil {
		return "", errors.New("trusted submitter time is unavailable")
	}
	if evidence == nil || primary == nil || secondary == nil {
		return "", errors.New("Mithril node and independent evidence are required")
	}
	primaryIdentity, secondaryIdentity := evidence.EvidenceProviderIdentities()
	if primaryIdentity != policy.Evidence.PrimaryOriginSHA256 ||
		secondaryIdentity != policy.Evidence.SecondaryOriginSHA256 ||
		primary.Identity() != primaryIdentity || secondary.Identity() != secondaryIdentity {
		return "", errors.New("recovery evidence providers do not match policy")
	}
	var actionID string
	err := withRecoveryLock(policy, func() error {
		record, transaction, decoded, err := readRecovery(policy)
		defer clear(transaction)
		if err != nil {
			return err
		}
		if record.SendStarted && !allowStarted {
			return ErrControlBlocked
		}
		if allowStarted && record.SendAttempts >= maxJupiterSendAttempts {
			return ErrControlBlocked
		}
		if record.Finalized || decoded.jupiter == nil || record.JupiterRequest == nil {
			return errors.New("Mainnet recovery record is not ready for first submission")
		}
		if err := checkJupiterReadinessUnlocked(
			ctx, policy, record, decoded, evidence, primary, secondary, now(),
		); err != nil {
			return err
		}
		actionID = record.ActionID
		return nil
	})
	return actionID, err
}

// RetireUnstartedJupiterRecovery archives an expired or otherwise rejected
// prepared proposal so a different one can be prepared. It is deliberately
// unavailable after a send has started and runs under both the stopped control
// barrier and the recovery lock.
func RetireUnstartedJupiterRecovery(policy Policy) (string, error) {
	if err := ValidateJupiterPolicy(policy); err != nil {
		return "", err
	}
	gate, err := control.NewMainnetCanaryStateFile(
		policy.ControlStatePath, policy.ProfileFingerprint, false,
	)
	if err != nil {
		return "", errors.New("Mainnet control gate is invalid")
	}
	var actionID string
	err = gate.WithStoppedBarrier(func() error {
		return withRecoveryLock(policy, func() error {
			record, transaction, decoded, err := readRecovery(policy)
			defer clear(transaction)
			if err != nil {
				return err
			}
			if record.SendStarted {
				return errors.New("Mainnet submission has already started")
			}
			if record.Finalized || record.JupiterRequest == nil || decoded.jupiter == nil {
				return errors.New("Mainnet recovery record cannot be retired")
			}
			if err := archiveRecovery(policy, record.ActionID); err != nil {
				return err
			}
			actionID = record.ActionID
			return nil
		})
	})
	return actionID, err
}

// submitPreparedJupiterAt crosses the internal one-action Mainnet canary
// boundary. It repeats every readiness check while holding both barriers,
// durably marks send-started, and only then attempts an exact-byte broadcast.
// An exact-retry policy may rebroadcast only those same persisted bytes while
// the original recovery marker and blockhash remain valid. No command or
func submitPreparedJupiterAt(
	ctx context.Context,
	policy Policy,
	node JupiterSubmitNode,
	evidence JupiterReadinessEvidence,
	primary, secondary proposalcheck.FinalizedSlotReader,
	now func() time.Time,
) (txflow.Submission, error) {
	if err := ValidateJupiterPolicy(policy); err != nil {
		return txflow.Submission{}, err
	}
	if now == nil {
		return txflow.Submission{}, errors.New("trusted submitter time is unavailable")
	}
	if node == nil || evidence == nil || primary == nil || secondary == nil {
		return txflow.Submission{}, errors.New("Mainnet node and independent evidence are required")
	}
	if node.Identity() == "" || node.Identity() != evidence.MithrilNodeIdentity() {
		return txflow.Submission{}, errors.New("Mainnet submitter and readiness checks must use the same Mithril node")
	}
	gate, err := control.NewMainnetCanaryStateFile(
		policy.ControlStatePath, policy.ProfileFingerprint, false,
	)
	if err != nil {
		return txflow.Submission{}, errors.New("Mainnet control gate is invalid")
	}
	status, err := gate.Status()
	if err != nil {
		return txflow.Submission{}, err
	}
	if err := control.ValidateMainnetCanaryStatus(status); err != nil {
		return txflow.Submission{}, errors.New("Mainnet canary control mode is invalid")
	}
	var sendBarrier func(string, func() error) (bool, error)
	switch {
	case status.Mode == control.ModeMainnetCanary:
		sendBarrier = gate.WithSendBarrier
	case status.RecoveryPending:
		if policy.RecoveryMode != MainnetRecoveryExactRetry {
			return txflow.Submission{}, ErrControlBlocked
		}
		sendBarrier = gate.WithRecoverySendBarrier
	default:
		return txflow.Submission{}, ErrControlBlocked
	}
	recoveryRetry := status.RecoveryPending
	actionID, err := checkJupiterRecoveryReadinessAt(
		ctx, policy, evidence, primary, secondary, recoveryRetry, now,
	)
	if err != nil {
		return txflow.Submission{}, err
	}
	var submission txflow.Submission
	blocked, err := sendBarrier(actionID, func() error {
		return withRecoveryLock(policy, func() error {
			record, transaction, decoded, err := readRecovery(policy)
			defer clear(transaction)
			if err != nil {
				return err
			}
			if record.ActionID != actionID || record.Finalized ||
				(record.SendStarted && !recoveryRetry) ||
				(recoveryRetry && record.SendAttempts >= maxJupiterSendAttempts) ||
				decoded.jupiter == nil || record.JupiterRequest == nil {
				return errors.New("Mainnet recovery record changed before submission")
			}
			if err := checkJupiterReadinessUnlocked(
				ctx, policy, record, decoded, evidence, primary, secondary, now(),
			); err != nil {
				return err
			}
			record.SendStarted = true
			record.SendAttempts++
			if err := writeRecovery(policy, record); err != nil {
				return errors.New("persist Mainnet send-started recovery evidence")
			}
			sendBytes := bytes.Clone(transaction)
			defer clear(sendBytes)
			returned, sendErr := node.SendTransaction(ctx, sendBytes, record.BlockhashContext)
			state := txflow.StateAccepted
			if sendErr != nil || returned != record.Submission.Signature {
				state = txflow.StateAmbiguous
			}
			submission = txflow.Submission{
				Signature:            record.Submission.Signature,
				LastValidBlockHeight: record.Submission.LastValidBlockHeight,
				State:                state,
			}
			return nil
		})
	})
	if err != nil {
		return txflow.Submission{}, err
	}
	if blocked {
		return txflow.Submission{}, ErrControlBlocked
	}
	return submission, nil
}

func checkJupiterReadinessUnlocked(
	ctx context.Context,
	policy Policy,
	record recoveryRecord,
	decoded decodedTransaction,
	evidence JupiterReadinessEvidence,
	primary, secondary proposalcheck.FinalizedSlotReader,
	now time.Time,
) error {
	if err := signer.ValidateScheduleWindowAt(*record.JupiterRequest, now); err != nil {
		return errors.New("Mainnet proposal approval window is not currently valid")
	}
	result, err := proposalcheck.Recheck(
		ctx, evidence, primary, secondary, *policy.Jupiter, policy.Evidence,
		*record.JupiterRequest.JupiterCandidate,
	)
	if err != nil {
		return fmt.Errorf("revalidate exact Mainnet proposal before submission: %w", err)
	}
	if result.MinimumContextSlot < record.BlockhashContext ||
		result.LastValidBlockHeight != record.Submission.LastValidBlockHeight {
		return errors.New("Mainnet proposal readiness evidence is inconsistent")
	}
	if err := evidence.VerifyIndependentBlockhashValidity(
		ctx, decoded.jupiter.RecentBlockhash, result.MinimumContextSlot,
	); err != nil {
		return fmt.Errorf("independent providers reject the Mainnet blockhash: %w", err)
	}
	return nil
}

func prepareRecovery(policy Policy, response signer.Response, transaction []byte) error {
	record := recoveryRecord{
		Version: recoveryVersion, ActionID: response.ActionID,
		ProfileFingerprint: policy.ProfileFingerprint,
		TransactionBase64:  base64.StdEncoding.EncodeToString(transaction),
		FeeLamports:        response.FeeLamports,
		RequestSHA256:      response.RequestSHA256,
		BlockhashContext:   response.BlockhashContextSlot,
		SignerAttestation:  response.SignerAttestation,
		Submission: txflow.Submission{
			Signature: response.Signature, LastValidBlockHeight: response.LastValidBlockHeight,
			State: txflow.StateAmbiguous,
		},
	}
	return persistRecovery(policy, record)
}

// prepareJupiterRecovery durably preserves the exact v0 transaction and all
// address-table evidence needed for independent restart reconciliation. It is
// not connected to a Mainnet send path.
func prepareJupiterRecovery(
	policy Policy,
	request signer.Request,
	response signer.Response,
	transaction []byte,
) error {
	request.RiskGrant = signer.Request{}.RiskGrant
	decoded, err := decodeJupiterRecovery(policy, request, transaction)
	if err != nil {
		return errors.New("submission recovery transaction is outside policy")
	}
	record := recoveryRecord{
		Version: jupiterRecoveryVersion, ActionID: response.ActionID,
		ProfileFingerprint: policy.ProfileFingerprint,
		TransactionBase64:  base64.StdEncoding.EncodeToString(transaction),
		FeeLamports:        response.FeeLamports,
		RequestSHA256:      response.RequestSHA256,
		BlockhashContext:   response.BlockhashContextSlot,
		SignerAttestation:  response.SignerAttestation,
		RecoveryMode:       policy.RecoveryMode,
		Submission: txflow.Submission{
			Signature: decoded.signature, LastValidBlockHeight: response.LastValidBlockHeight,
			State: txflow.StateAmbiguous,
		},
		JupiterRequest: &request,
	}
	return persistRecovery(policy, record)
}

func persistRecovery(policy Policy, record recoveryRecord) error {
	if _, _, err := validateRecovery(policy, record); err != nil {
		return err
	}
	return withRecoveryLock(policy, func() error {
		if policy.Jupiter != nil {
			if _, err := os.Lstat(finalizedRecoveryPath(policy, record.ActionID)); err == nil {
				return errors.New("a finalized action cannot be reopened for submission")
			} else if !errors.Is(err, os.ErrNotExist) {
				return errors.New("inspect finalized submission recovery evidence")
			}
		}
		if _, err := os.Lstat(retiredRecoveryPath(policy, record.ActionID)); err == nil {
			return errors.New("a retired submission cannot be prepared again")
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("inspect retired submission recovery evidence")
		}
		current, transaction, _, err := readRecovery(policy)
		defer clear(transaction)
		if err == nil {
			same, compareErr := sameRecoveryRecord(current, record)
			if compareErr != nil {
				return compareErr
			}
			if same {
				return nil
			}
			if !current.Finalized {
				return errors.New("a different submission still requires recovery")
			}
			if current.ActionID == record.ActionID {
				return errors.New("a finalized action cannot be reopened for submission")
			}
			if policy.Jupiter != nil {
				if err := archiveFinalizedRecovery(policy, current.ActionID); err != nil {
					return err
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return writeRecovery(policy, record)
	})
}

func sameRecoveryRecord(a, b recoveryRecord) (bool, error) {
	left, err := json.Marshal(a)
	if err != nil {
		return false, errors.New("encode existing submission recovery evidence")
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false, errors.New("encode replacement submission recovery evidence")
	}
	return bytes.Equal(left, right), nil
}

func readRecovery(policy Policy) (recoveryRecord, []byte, decodedTransaction, error) {
	data, err := securefile.ReadPrivate(recoveryPath(policy), maxRecoveryBytes)
	if err != nil {
		return recoveryRecord{}, nil, decodedTransaction{}, err
	}
	defer clear(data)
	var record recoveryRecord
	if err := strictjson.Decode(data, &record); err != nil {
		return recoveryRecord{}, nil, decodedTransaction{}, errors.New("decode submission recovery evidence")
	}
	transaction, decoded, err := validateRecovery(policy, record)
	if err != nil {
		return recoveryRecord{}, nil, decodedTransaction{}, err
	}
	return record, transaction, decoded, nil
}

func validateRecovery(policy Policy, record recoveryRecord) ([]byte, decodedTransaction, error) {
	mainnet := policy.Jupiter != nil
	if (mainnet && ((record.Version != legacyJupiterRecoveryVersion &&
		record.Version != jupiterRecoveryVersion) || record.JupiterRequest == nil)) ||
		(!mainnet && (record.Version != recoveryVersion || record.JupiterRequest != nil ||
			record.SendStarted || record.SendAttempts != 0 || record.RecoveryMode != "" ||
			record.Reconciliation != nil)) ||
		!validHash(record.ActionID) ||
		record.ProfileFingerprint != policy.ProfileFingerprint || record.FeeLamports == 0 ||
		(mainnet && (record.RecoveryMode != policy.RecoveryMode ||
			record.SendStarted != (record.SendAttempts > 0) ||
			record.SendAttempts > maxJupiterSendAttempts ||
			(record.RecoveryMode == MainnetRecoveryStopOnly && record.SendAttempts > 1) ||
			(record.Finalized && !record.SendStarted))) ||
		record.FeeLamports > policy.MaxFeeLamports || record.Submission.State != txflow.StateAmbiguous ||
		record.Submission.LastValidBlockHeight == 0 {
		return nil, decodedTransaction{}, errors.New("submission recovery evidence is invalid")
	}
	transaction, err := base64.StdEncoding.Strict().DecodeString(record.TransactionBase64)
	if err != nil || len(transaction) == 0 || len(transaction) > maxTransaction ||
		base64.StdEncoding.EncodeToString(transaction) != record.TransactionBase64 {
		return nil, decodedTransaction{}, errors.New("submission recovery transaction is invalid")
	}
	var decoded decodedTransaction
	if mainnet {
		decoded, err = decodeJupiterRecovery(policy, *record.JupiterRequest, transaction)
	} else {
		decoded, err = decodeTransaction(policy, transaction)
	}
	if err != nil || decoded.signature != record.Submission.Signature ||
		(decoded.jupiter != nil && (decoded.requestSHA256 != record.RequestSHA256 ||
			!validJupiterReconciliation(record, *decoded.jupiter))) {
		return nil, decodedTransaction{}, errors.New("submission recovery transaction is outside policy")
	}
	messageHash := sha256.Sum256(decoded.message)
	transactionHash := sha256.Sum256(transaction)
	responseSignature := decoded.signature
	if mainnet {
		responseSignature = ""
	}
	response := signer.Response{
		ActionID: record.ActionID, RequestSHA256: record.RequestSHA256,
		Signature: responseSignature, MessageSHA256: hex.EncodeToString(messageHash[:]),
		TransactionSHA256:    hex.EncodeToString(transactionHash[:]),
		BlockhashContextSlot: record.BlockhashContext,
		FeeLamports:          record.FeeLamports,
		LastValidBlockHeight: record.Submission.LastValidBlockHeight,
		SignerAttestation:    record.SignerAttestation,
	}
	response.SealedTransaction.Metadata = responseMetadata(response)
	attestor := policy.Source
	if mainnet {
		attestor = policy.AttestationPublicKey
	}
	if signer.VerifyResponseAttestation(attestor, policy.SubmitterPublicKey, response) != nil {
		return nil, decodedTransaction{}, errors.New("submission recovery attestation is invalid")
	}
	return transaction, decoded, nil
}

// validJupiterReconciliation verifies the durable terminal projection against
// the exact transaction evidence already decoded from the recovery record.
func validJupiterReconciliation(record recoveryRecord, expected txflow.ExpectedJupiter) bool {
	if record.Version == legacyJupiterRecoveryVersion {
		return record.Reconciliation == nil
	}
	result := record.Reconciliation
	if !record.Finalized {
		return result == nil
	}
	if result == nil || result.Signature != record.Submission.Signature || result.Slot == 0 ||
		!result.PrimaryFound || !result.SecondaryFound ||
		result.PrimarySlot != result.Slot || result.SecondarySlot != result.Slot ||
		result.PrimaryStatus != "finalized" || result.SecondaryStatus != "finalized" ||
		result.PrimaryErrorFingerprint != result.SecondaryErrorFingerprint ||
		result.PrimaryBlockHeight != 0 || result.SecondaryBlockHeight != 0 ||
		result.DivergenceKind != "" || result.Effects != nil || result.SwapEffects != nil ||
		result.BuyEffects != nil || result.JupiterEffects == nil {
		return false
	}
	failed := result.Verdict == txflow.VerdictFailed
	if result.Verdict != txflow.VerdictFinalized && !failed ||
		result.PrimaryFailed != failed || result.SecondaryFailed != failed {
		return false
	}
	if failed {
		if !validHash(result.PrimaryErrorFingerprint) {
			return false
		}
	} else if result.PrimaryErrorFingerprint != "" {
		return false
	}
	effects := result.JupiterEffects
	if effects.TransactionSHA256 != expected.TransactionSHA256 ||
		effects.FeeLamports != record.FeeLamports ||
		effects.InputAmount != expected.InputAmount ||
		effects.MinimumOutput != expected.MinimumOutput ||
		effects.PrimaryEffectSlot != result.Slot ||
		effects.SecondaryEffectSlot != result.Slot ||
		effects.OutputAccountRent > expected.Policy.MaxTokenAccountRentLamports {
		return false
	}
	if failed {
		return effects.OutputAmount == 0 && effects.OutputAccountRent == 0
	}
	return effects.OutputAmount >= expected.MinimumOutput
}

func decodeJupiterRecovery(
	policy Policy,
	request signer.Request,
	transaction []byte,
) (decodedTransaction, error) {
	message, tables, err := validateJupiterRequest(policy, request)
	if err != nil {
		return decodedTransaction{}, err
	}
	decoded, err := solana.DecodeSignedV0Transaction(transaction, tables)
	if err != nil || !bytes.Equal(decoded.Message.Raw, message) {
		return decodedTransaction{}, errors.New("submission recovery Jupiter transaction is invalid")
	}
	messageHash := sha256.Sum256(message)
	binding, err := signer.RiskBinding(request, hex.EncodeToString(messageHash[:]))
	if err != nil {
		return decodedTransaction{}, err
	}
	transactionHash := sha256.Sum256(transaction)
	signature := solana.Encode(decoded.Signature[:])
	return decodedTransaction{
		message: message, signature: signature, requestSHA256: binding.RequestSHA256,
		jupiter: &txflow.ExpectedJupiter{
			Signature: signature, TransactionSHA256: hex.EncodeToString(transactionHash[:]),
			RecentBlockhash:      request.RecentBlockhash,
			LastValidBlockHeight: request.LastValidBlockHeight,
			Policy:               *policy.Jupiter,
			InputAmount:          request.JupiterCandidate.Request.InputAmount,
			EstimatedOutput:      request.JupiterCandidate.Quote.EstimatedOutput,
			MinimumOutput:        request.JupiterCandidate.Quote.MinimumOutput,
			SlippageBPS:          request.JupiterCandidate.Request.SlippageBPS,
			AddressTables: append(
				[]jupiterswap.AddressTableEvidence(nil),
				request.JupiterCandidate.AddressTables...,
			),
		},
	}, nil
}

func validateRecoveryPolicy(policy Policy) error {
	if policy.Jupiter != nil {
		return ValidateJupiterPolicy(policy)
	}
	return policy.Validate()
}

func validateRecoveryEvidence(policy Policy) error {
	if policy.Jupiter != nil {
		return policy.Evidence.ValidateArchiveProbe()
	}
	return policy.Evidence.Validate()
}

func writeRecovery(policy Policy, record recoveryRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return errors.New("encode submission recovery evidence")
	}
	return securefile.ReplacePrivate(recoveryPath(policy), data, maxRecoveryBytes)
}

func recoveryPath(policy Policy) string {
	return filepath.Join(filepath.Dir(policy.ControlStatePath), "submission-recovery.json")
}

func retiredRecoveryPath(policy Policy, actionID string) string {
	// ponytail: retired canaries are intentionally retained without rotation;
	// move them to the off-host audit store before adding a retention policy.
	return filepath.Join(
		filepath.Dir(policy.ControlStatePath),
		"submission-recovery."+actionID+".retired.json",
	)
}

func finalizedRecoveryPath(policy Policy, actionID string) string {
	return filepath.Join(
		filepath.Dir(policy.ControlStatePath),
		"submission-recovery."+actionID+".finalized.json",
	)
}

func archiveFinalizedRecovery(policy Policy, actionID string) error {
	return linkRecoveryArchive(
		policy, finalizedRecoveryPath(policy, actionID),
		"archive finalized Mainnet recovery evidence",
		"read finalized Mainnet recovery evidence",
		"a different finalized Mainnet recovery record already exists",
	)
}

func archiveRecovery(policy Policy, actionID string) error {
	source := recoveryPath(policy)
	destination := retiredRecoveryPath(policy, actionID)
	if err := linkRecoveryArchive(
		policy, destination,
		"archive unstarted Mainnet recovery evidence",
		"read retired Mainnet recovery evidence",
		"a different retired Mainnet recovery record already exists",
	); err != nil {
		return err
	}
	if err := os.Remove(source); err != nil {
		return errors.New("remove retired Mainnet recovery evidence from active state")
	}
	return syncRecoveryDirectory(policy)
}

func linkRecoveryArchive(
	policy Policy,
	destination, linkError, readError, mismatchError string,
) error {
	source := recoveryPath(policy)
	if err := os.Link(source, destination); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return errors.New(linkError)
		}
		sourceData, readErr := securefile.ReadPrivate(source, maxRecoveryBytes)
		if readErr != nil {
			return errors.New(readError)
		}
		defer clear(sourceData)
		destinationData, readErr := securefile.ReadPrivate(destination, maxRecoveryBytes)
		if readErr != nil {
			return errors.New(readError)
		}
		defer clear(destinationData)
		if !bytes.Equal(sourceData, destinationData) {
			return errors.New(mismatchError)
		}
	}
	return syncRecoveryDirectory(policy)
}

func syncRecoveryDirectory(policy Policy) error {
	directory, err := os.Open(filepath.Dir(recoveryPath(policy)))
	if err != nil {
		return errors.New("open Mainnet recovery directory")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("sync Mainnet recovery directory")
	}
	return nil
}

func decodeTransaction(policy Policy, transaction []byte) (decodedTransaction, error) {
	hash := sha256.Sum256(transaction)
	transactionHash := hex.EncodeToString(hash[:])
	if policy.OrcaSwap == nil && policy.OrcaBuy == nil {
		decoded, err := solana.DecodeSignedTransfer(transaction)
		if err != nil {
			return decodedTransaction{}, fmt.Errorf("decode sealed transaction: %w", err)
		}
		source := solana.Encode(decoded.Source[:])
		destination := solana.Encode(decoded.Destination[:])
		if source != policy.Source || destination != policy.Destination ||
			decoded.Lamports == 0 || decoded.Lamports > policy.MaxLamports {
			return decodedTransaction{}, errors.New("sealed transaction is outside submitter policy")
		}
		signature := solana.Encode(decoded.Signature[:])
		return decodedTransaction{
			message: decoded.Message, signature: signature,
			transfer: &txflow.ExpectedTransaction{
				Signature: signature, TransactionSHA256: transactionHash,
				Source: source, Destination: destination, AmountLamports: decoded.Lamports,
			},
		}, nil
	}

	decoded, err := solana.DecodeSignedLegacyTransaction(transaction)
	if err != nil {
		return decodedTransaction{}, fmt.Errorf("decode sealed transaction: %w", err)
	}
	signature := solana.Encode(decoded.Signature[:])
	if policy.OrcaSwap != nil {
		intent, err := orcaswap.DecodeMessage(*policy.OrcaSwap, decoded.Message.Raw)
		if err != nil || intent.Owner != policy.Source || intent.InputAmount != policy.MaxLamports {
			return decodedTransaction{}, errors.New("sealed Orca swap is outside submitter policy")
		}
		return decodedTransaction{
			message: decoded.Message.Raw, signature: signature,
			swap: &txflow.ExpectedSwap{
				Signature: signature, TransactionSHA256: transactionHash, Policy: *policy.OrcaSwap,
				InputAmount: intent.InputAmount, MinimumOutput: intent.MinimumOutput,
			},
		}, nil
	}
	intent, err := orcaswap.DecodeBuyMessageV2(*policy.OrcaBuy, decoded.Message.Raw)
	if err != nil || intent.Owner != policy.Source || intent.InputAmount != policy.MaxInputTokenAmount {
		return decodedTransaction{}, errors.New("sealed Orca buy is outside submitter policy")
	}
	return decodedTransaction{
		message: decoded.Message.Raw, signature: signature,
		buy: &txflow.ExpectedBuy{
			Signature: signature, TransactionSHA256: transactionHash, Policy: *policy.OrcaBuy,
			InputAmount: intent.InputAmount, MinimumOutput: intent.MinimumOutputLamports,
		},
	}, nil
}

func validHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}
