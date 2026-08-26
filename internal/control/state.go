package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

const (
	maxStateFileBytes        = 4 << 10
	maxReasonBytes           = 256
	maxActivationLifetime    = 24 * time.Hour
	maxActivationActions     = 100
	maxMainnetCanaryLifetime = time.Hour
	maxMainnetCanaryActions  = 1
	stateVersion             = 3

	ModeNoNewActions  = "no_new_actions"
	ModeDevnetEnabled = "devnet_enabled"
	ModeMainnetCanary = "mainnet_canary"
)

// ErrRecoveryPending means an automatic stop refused to erase the only marker
// for a transaction that may already have landed.
var ErrRecoveryPending = errors.New("control state has pending recovery")

type StateFile struct {
	path               string
	profileFingerprint string
	now                func() time.Time
	startedAt          time.Time
	requireFresh       bool
	activeMode         string
}

type Status struct {
	Mode             string    `json:"mode"`
	RecoveryPending  bool      `json:"recovery_pending,omitempty"`
	ExpectedActionID string    `json:"expected_action_id,omitempty"`
	TerminalActionID string    `json:"terminal_action_id,omitempty"`
	TerminalOutcome  string    `json:"terminal_outcome,omitempty"`
	ExpiresAt        time.Time `json:"expires_at,omitzero"`
	MaxActions       uint32    `json:"max_actions,omitempty"`
	RemainingActions uint32    `json:"remaining_actions,omitempty"`
}

type stateDocument struct {
	Version            uint32    `json:"version"`
	Mode               string    `json:"mode"`
	ProfileFingerprint string    `json:"profile_sha256,omitempty"`
	ExpectedActionID   string    `json:"expected_action_id,omitempty"`
	RecoveryActionID   string    `json:"recovery_action_id,omitempty"`
	TerminalActionID   string    `json:"terminal_action_id,omitempty"`
	TerminalOutcome    string    `json:"terminal_outcome,omitempty"`
	IssuedAt           time.Time `json:"issued_at,omitzero"`
	ExpiresAt          time.Time `json:"expires_at,omitzero"`
	MaxActions         uint32    `json:"max_actions,omitempty"`
	RemainingActions   uint32    `json:"remaining_actions,omitempty"`
	Reason             string    `json:"reason"`
}

// ValidateStatus checks the bounded Devnet control projection shared with
// current operators and submitter clients.
func ValidateStatus(status Status) error {
	return validateStatus(status, ModeDevnetEnabled)
}

// ValidateMainnetCanaryStatus checks the disabled package-only Mainnet
// projection without widening any current Devnet client boundary.
func ValidateMainnetCanaryStatus(status Status) error {
	return validateStatus(status, ModeMainnetCanary)
}

func validateStatus(status Status, activeMode string) error {
	switch status.Mode {
	case ModeNoNewActions:
		if !status.ExpiresAt.IsZero() || status.MaxActions != 0 ||
			status.RemainingActions != 0 || status.ExpectedActionID != "" ||
			(status.RecoveryPending && status.TerminalActionID != "") ||
			!validOptionalTerminalLatch(status.TerminalActionID, status.TerminalOutcome) {
			return errors.New("stopped control status is invalid")
		}
	case activeMode:
		_, actionLimit, ok := activationLimits(activeMode)
		if !ok {
			return errors.New("control status mode is invalid")
		}
		expectedActionValid := status.ExpectedActionID == ""
		if activeMode == ModeMainnetCanary {
			expectedActionValid = validDigest(status.ExpectedActionID)
		}
		if !expectedActionValid || status.ExpiresAt.IsZero() || status.MaxActions == 0 ||
			status.MaxActions > actionLimit || status.RemainingActions == 0 ||
			status.RemainingActions > status.MaxActions || status.TerminalActionID != "" ||
			status.TerminalOutcome != "" || status.RecoveryPending {
			return errors.New("enabled control status is invalid")
		}
	default:
		return errors.New("control status mode is invalid")
	}
	return nil
}

func NewStateFile(path, profileFingerprint string, requireFresh bool) (*StateFile, error) {
	return newStateFile(path, profileFingerprint, requireFresh, ModeDevnetEnabled)
}

// NewMainnetCanaryStateFile creates the one-action Mainnet gate. The root-only
// operator socket can prepare this state, but no command or service can submit
// through it yet.
func NewMainnetCanaryStateFile(
	path, profileFingerprint string,
	requireFresh bool,
) (*StateFile, error) {
	return newStateFile(path, profileFingerprint, requireFresh, ModeMainnetCanary)
}

func newStateFile(
	path, profileFingerprint string,
	requireFresh bool,
	activeMode string,
) (*StateFile, error) {
	path, err := validateStatePath(path)
	if err != nil {
		return nil, err
	}
	decoded, err := hex.DecodeString(profileFingerprint)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != profileFingerprint {
		return nil, errors.New("profile fingerprint is invalid")
	}
	now := time.Now
	return &StateFile{
		path:               path,
		profileFingerprint: profileFingerprint,
		now:                now,
		startedAt:          now().UTC(),
		requireFresh:       requireFresh,
		activeMode:         activeMode,
	}, nil
}

func validateStatePath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("control state path must be absolute")
	}
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	if err := secureexec.ValidateProtectedDirectory(parent); err != nil {
		return "", errors.New("control state directory ancestry is not trusted")
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return "", errors.New("inspect control state directory")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() ||
		info.Mode().Perm()&0o022 != 0 || !fileowner.Trusted(info) {
		return "", errors.New("control state directory must not be writable by group or others or be a symlink")
	}
	return path, nil
}

func (s *StateFile) NoNewActions() (bool, error) {
	var blocked bool
	err := withStateLock(s.path, func() error {
		var err error
		blocked, err = s.noNewActionsUnlocked()
		return err
	})
	return blocked, err
}

func (s *StateFile) Status() (Status, error) {
	if _, err := os.Lstat(s.path); errors.Is(err, os.ErrNotExist) {
		// An absent file means fail-safe stopped. Avoid creating a lock file for
		// a read that has no state to serialize with.
		return Status{Mode: ModeNoNewActions}, nil
	} else if err != nil {
		return Status{}, errors.New("inspect control state")
	}
	var status Status
	err := withStateLock(s.path, func() error {
		var err error
		status, err = s.statusUnlocked()
		return err
	})
	return status, err
}

func (s *StateFile) statusUnlocked() (Status, error) {
	document, err := s.readStateUnlocked()
	if err != nil {
		return Status{}, err
	}
	if document == nil {
		return Status{Mode: ModeNoNewActions}, nil
	}
	blocked, err := s.validateState(*document)
	if err != nil {
		return Status{}, err
	}
	if blocked {
		status := Status{Mode: ModeNoNewActions}
		if document.Mode == ModeNoNewActions {
			status.TerminalActionID = document.TerminalActionID
			status.TerminalOutcome = document.TerminalOutcome
		} else if document.RecoveryActionID != "" {
			status.RecoveryPending = true
		}
		return status, nil
	}
	return Status{
		Mode:             document.Mode,
		ExpectedActionID: document.ExpectedActionID,
		ExpiresAt:        document.ExpiresAt,
		MaxActions:       document.MaxActions,
		RemainingActions: document.RemainingActions,
	}, nil
}

// Revision returns a non-secret digest used to detect concurrent state changes.
func (s *StateFile) Revision() (string, error) {
	return stateRevision(s.path)
}

// StopForTerminal records a bounded terminal outcome and blocks new actions.
func (s *StateFile) StopForTerminal(actionID, outcome string) error {
	if !validTerminalLatch(actionID, outcome) {
		return errors.New("terminal outcome is invalid")
	}
	return withStateLock(s.path, func() error {
		current, err := s.readStateUnlocked()
		if err != nil {
			return err
		}
		if current != nil {
			if _, err := s.validateState(*current); err != nil {
				return err
			}
			if current.TerminalActionID != "" || current.TerminalOutcome != "" {
				if current.TerminalActionID != actionID {
					return errors.New("another terminal action requires acknowledgement")
				}
				if current.TerminalOutcome == outcome {
					return nil
				}
			}
		}
		return replaceStateDocument(s.path, stateDocument{
			Version:          stateVersion,
			Mode:             ModeNoNewActions,
			TerminalActionID: actionID,
			TerminalOutcome:  outcome,
			Reason:           "terminal action " + outcome,
		})
	})
}

// TerminalLatch returns the exact terminal stop, if one is present.
func (s *StateFile) TerminalLatch() (string, string, error) {
	status, err := s.Status()
	if err != nil {
		return "", "", err
	}
	return status.TerminalActionID, status.TerminalOutcome, nil
}

// ClearTerminalForFinalized removes the matching recovery or terminal marker.
// It preserves the remaining action budget and never grants new authority.
func (s *StateFile) ClearTerminalForFinalized(actionID string) error {
	if !validDigest(actionID) {
		return errors.New("finalized action ID is invalid")
	}
	return withStateLock(s.path, func() error {
		document, err := s.readStateUnlocked()
		if err != nil {
			return err
		}
		if document == nil {
			return nil
		}
		if _, err := s.validateState(*document); err != nil {
			return err
		}
		switch document.Mode {
		case s.activeMode:
			if document.RecoveryActionID == "" {
				return nil
			}
			if document.RecoveryActionID != actionID {
				return errors.New("another action requires recovery")
			}
			document.RecoveryActionID = ""
			return replaceStateDocument(s.path, *document)
		case ModeNoNewActions:
			if document.TerminalActionID == "" && document.TerminalOutcome == "" {
				return nil
			}
			if document.TerminalActionID != actionID {
				return errors.New("another terminal action requires acknowledgement")
			}
			return replaceStateDocument(s.path, stateDocument{
				Version: stateVersion,
				Mode:    ModeNoNewActions,
				Reason:  "finalized action reconciled",
			})
		default:
			return errors.New("control state mode is invalid")
		}
	})
}

// Stop blocks new actions while preserving any unacknowledged terminal outcome.
func (s *StateFile) Stop(reason string) error {
	return WriteNoNewActions(s.path, reason)
}

// StopPreservingRecovery blocks new actions without treating an automatic
// shutdown as acknowledgement of a transaction that may already have landed.
func (s *StateFile) StopPreservingRecovery(reason string) error {
	return writeNoNewActions(s.path, reason, true)
}

// AcknowledgeTerminal clears a matching failed-action latch without enabling work.
// Halted actions are ambiguous and remain latched for the lifetime of the setup.
func (s *StateFile) AcknowledgeTerminal(actionID, outcome, reason string) (Status, error) {
	if !validTerminalLatch(actionID, outcome) {
		return Status{}, errors.New("terminal acknowledgement outcome is invalid")
	}
	if outcome == "halted" {
		return Status{}, errors.New("halted terminal actions cannot be cleared")
	}
	if !validReason(reason) {
		return Status{}, errors.New("control reason is invalid")
	}
	acknowledged := false
	err := withStateLock(s.path, func() error {
		document, err := s.readStateUnlocked()
		if err != nil {
			return err
		}
		if document == nil || document.Mode != ModeNoNewActions {
			return errors.New("terminal acknowledgement requires stopped control state")
		}
		if _, err := s.validateState(*document); err != nil {
			return err
		}
		if document.TerminalOutcome == "" && document.TerminalActionID == "" {
			acknowledged = true
			return nil
		}
		if document.TerminalOutcome != outcome || document.TerminalActionID != actionID {
			return errors.New("terminal acknowledgement outcome does not match")
		}
		if err := replaceStateDocument(s.path, stateDocument{
			Version: stateVersion,
			Mode:    ModeNoNewActions,
			Reason:  reason,
		}); err != nil {
			return err
		}
		acknowledged = true
		return nil
	})
	if err != nil {
		return Status{}, err
	}
	if !acknowledged {
		return Status{}, errors.New("terminal outcome was not acknowledged")
	}
	return s.Status()
}

func (s *StateFile) WithSendBarrier(actionID string, operation func() error) (bool, error) {
	if !validDigest(actionID) {
		return false, errors.New("send barrier action ID is invalid")
	}
	if operation == nil {
		return false, errors.New("send barrier operation is required")
	}
	var blocked bool
	err := withStateLock(s.path, func() error {
		document, err := s.readStateUnlocked()
		if err != nil {
			return err
		}
		if document == nil {
			blocked = true
			return nil
		}
		blocked, err = s.validateState(*document)
		if err != nil || blocked {
			return err
		}
		if document.Mode == ModeMainnetCanary && document.ExpectedActionID != actionID {
			blocked = true
			return nil
		}
		// Consume the bounded activation slot before the durable send marker.
		// A crash can therefore lose capacity, but can never create extra
		// authority. Re-enabling always requires a fresh operator action.
		document.RemainingActions--
		document.RecoveryActionID = actionID
		if err := replaceStateDocument(s.path, *document); err != nil {
			return err
		}
		return operation()
	})
	return blocked, err
}

// WithRecoverySendBarrier serializes an exact-byte recovery send with operator
// stop without consuming authority for a second action. The caller must first
// validate the durable send marker and signed transaction being recovered.
func (s *StateFile) WithRecoverySendBarrier(
	actionID string,
	operation func() error,
) (bool, error) {
	if !validDigest(actionID) {
		return false, errors.New("recovery send barrier action ID is invalid")
	}
	if operation == nil {
		return false, errors.New("recovery send barrier operation is required")
	}
	var blocked bool
	err := withStateLock(s.path, func() error {
		document, err := s.readStateUnlocked()
		if err != nil {
			return err
		}
		if document == nil {
			blocked = true
			return nil
		}
		allowed, err := s.recoverySendAllowed(*document, actionID)
		if err != nil {
			return err
		}
		if !allowed {
			blocked = true
			return nil
		}
		return operation()
	})
	return blocked, err
}

// WithStoppedBarrier runs an operator maintenance operation only while the
// control state is durably stopped. Holding the state lock prevents a new
// activation from racing maintenance of state bound to this control file.
func (s *StateFile) WithStoppedBarrier(operation func() error) error {
	if operation == nil {
		return errors.New("stopped-state operation is required")
	}
	return withStateLock(s.path, func() error {
		document, err := s.readStateUnlocked()
		if err != nil {
			return err
		}
		if document == nil {
			return errors.New("control state is missing")
		}
		if err := validateStoredStateDocument(*document, s.now()); err != nil {
			return err
		}
		if document.Mode != ModeNoNewActions || document.TerminalActionID != "" ||
			document.TerminalOutcome != "" {
			return errors.New("control state must be fully stopped")
		}
		return operation()
	})
}

func (s *StateFile) noNewActionsUnlocked() (bool, error) {
	document, err := s.readStateUnlocked()
	if err != nil {
		return false, err
	}
	if document == nil {
		return true, nil
	}
	return s.validateState(*document)
}

func (s *StateFile) readStateUnlocked() (*stateDocument, error) {
	return readStateDocument(s.path)
}

func readStateDocument(path string) (*stateDocument, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("inspect control state")
	}
	data, err := securefile.ReadPrivate(path, maxStateFileBytes)
	if err != nil {
		return nil, errors.New("read control state")
	}
	var document stateDocument
	if err := strictjson.Decode(data, &document); err != nil {
		return nil, errors.New("decode control state")
	}
	return &document, nil
}

func (s *StateFile) validateState(document stateDocument) (bool, error) {
	if document.Version != stateVersion || !validReason(document.Reason) {
		return false, errors.New("control state is invalid")
	}
	if document.Mode == ModeNoNewActions {
		if document.ProfileFingerprint != "" || !document.IssuedAt.IsZero() ||
			!document.ExpiresAt.IsZero() || document.MaxActions != 0 ||
			document.RemainingActions != 0 || document.ExpectedActionID != "" ||
			document.RecoveryActionID != "" ||
			!validOptionalTerminalLatch(document.TerminalActionID, document.TerminalOutcome) {
			return false, errors.New("stopped control state has enabling fields")
		}
		return true, nil
	}
	if document.Mode != s.activeMode {
		return false, errors.New("control mode is invalid")
	}
	now := s.now().UTC()
	if err := validateActivationDocument(document, now, s.profileFingerprint); err != nil {
		return false, err
	}
	// A durable send marker blocks every new action, even when the operator
	// granted more capacity. Otherwise a second send can overwrite the only
	// action ID that permits exact-byte recovery of the first transaction.
	if document.RemainingActions == 0 || document.RecoveryActionID != "" ||
		!document.ExpiresAt.After(now) ||
		(s.requireFresh && document.IssuedAt.Before(s.startedAt)) {
		return true, nil
	}
	return false, nil
}

func (s *StateFile) recoverySendAllowed(
	document stateDocument,
	actionID string,
) (bool, error) {
	if document.Version != stateVersion || !validReason(document.Reason) {
		return false, errors.New("control state is invalid")
	}
	if document.Mode == ModeNoNewActions {
		if document.ProfileFingerprint != "" || !document.IssuedAt.IsZero() ||
			!document.ExpiresAt.IsZero() || document.MaxActions != 0 ||
			document.RemainingActions != 0 || document.ExpectedActionID != "" ||
			document.RecoveryActionID != "" ||
			!validOptionalTerminalLatch(document.TerminalActionID, document.TerminalOutcome) {
			return false, errors.New("stopped control state has enabling fields")
		}
		return false, nil
	}
	if document.Mode != s.activeMode {
		return false, errors.New("control mode is invalid")
	}
	now := s.now().UTC()
	if err := validateActivationDocument(document, now, s.profileFingerprint); err != nil {
		return false, err
	}
	if document.RecoveryActionID != actionID || !document.ExpiresAt.After(now) {
		return false, nil
	}
	return true, nil
}

func activationLimits(mode string) (time.Duration, uint32, bool) {
	switch mode {
	case ModeDevnetEnabled:
		return maxActivationLifetime, maxActivationActions, true
	case ModeMainnetCanary:
		return maxMainnetCanaryLifetime, maxMainnetCanaryActions, true
	default:
		return 0, 0, false
	}
}

func validateActivationDocument(
	document stateDocument,
	now time.Time,
	expectedFingerprint string,
) error {
	lifetimeLimit, actionLimit, ok := activationLimits(document.Mode)
	if !ok {
		return errors.New("control mode is invalid")
	}
	fingerprintValid := validDigest(document.ProfileFingerprint)
	if expectedFingerprint != "" {
		fingerprintValid = document.ProfileFingerprint == expectedFingerprint
	}
	expectedActionValid := document.ExpectedActionID == ""
	if document.Mode == ModeMainnetCanary {
		expectedActionValid = validDigest(document.ExpectedActionID)
	}
	if !fingerprintValid || !expectedActionValid ||
		document.IssuedAt.IsZero() || document.ExpiresAt.IsZero() ||
		document.MaxActions == 0 || document.MaxActions > actionLimit ||
		document.RemainingActions > document.MaxActions || document.TerminalActionID != "" ||
		document.TerminalOutcome != "" ||
		(document.RecoveryActionID != "" && !validDigest(document.RecoveryActionID)) ||
		document.IssuedAt.After(now.UTC()) ||
		!document.ExpiresAt.After(document.IssuedAt) ||
		document.ExpiresAt.Sub(document.IssuedAt) > lifetimeLimit {
		return errors.New("control activation is invalid")
	}
	return nil
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

func validReason(reason string) bool {
	return strings.TrimSpace(reason) == reason && reason != "" &&
		len(reason) <= maxReasonBytes &&
		strings.IndexFunc(reason, unicode.IsControl) < 0
}

func validTerminalLatch(actionID, outcome string) bool {
	return validDigest(actionID) && (outcome == "failed" || outcome == "halted")
}

func validOptionalTerminalLatch(actionID, outcome string) bool {
	return actionID == "" && outcome == "" || validTerminalLatch(actionID, outcome)
}

// validateStoredStateDocument checks the persisted shape without deciding
// whether an otherwise valid activation is currently usable. This lets a
// fail-safe stop replace expired authority while refusing to erase malformed
// state or an unacknowledged terminal outcome.
func validateStoredStateDocument(document stateDocument, now time.Time) error {
	if document.Version != stateVersion || !validReason(document.Reason) {
		return errors.New("control state is invalid")
	}
	if document.Mode == ModeNoNewActions {
		if document.ProfileFingerprint != "" || !document.IssuedAt.IsZero() ||
			!document.ExpiresAt.IsZero() || document.MaxActions != 0 ||
			document.RemainingActions != 0 || document.ExpectedActionID != "" ||
			document.RecoveryActionID != "" ||
			!validOptionalTerminalLatch(document.TerminalActionID, document.TerminalOutcome) {
			return errors.New("stopped control state has enabling fields")
		}
		return nil
	}
	return validateActivationDocument(document, now, "")
}

func WriteNoNewActions(path, reason string) error {
	return writeNoNewActions(path, reason, false)
}

func writeNoNewActions(path, reason string, preserveRecovery bool) error {
	path, err := validateStatePath(path)
	if err != nil {
		return err
	}
	if !validReason(reason) {
		return errors.New("control reason is invalid")
	}
	return withStateLock(path, func() error {
		terminalActionID := ""
		terminalOutcome := ""
		current, err := readStateDocument(path)
		if err != nil {
			return err
		}
		if current != nil {
			if err := validateStoredStateDocument(*current, time.Now()); err != nil {
				return err
			}
			if preserveRecovery && current.RecoveryActionID != "" {
				return ErrRecoveryPending
			}
			if validTerminalLatch(current.TerminalActionID, current.TerminalOutcome) {
				terminalActionID = current.TerminalActionID
				terminalOutcome = current.TerminalOutcome
			}
			// RecoveryActionID is deliberately NOT carried over. A stop revokes
			// the ability to send, and an explicit operator stop is treated as
			// the operator taking responsibility for whatever was in flight.
			// Supervisor-facing callers must refuse this operation while recovery
			// is pending; a service transition is not an operator acknowledgement.
		}
		return replaceStateDocument(path, stateDocument{
			Version:          stateVersion,
			Mode:             ModeNoNewActions,
			TerminalActionID: terminalActionID,
			TerminalOutcome:  terminalOutcome,
			Reason:           reason,
		})
	})
}

func WriteDevnetActivation(
	path,
	profileFingerprint string,
	issuedAt,
	expiresAt time.Time,
	maxActions uint32,
	reason string,
) error {
	document, err := activationDocument(
		ModeDevnetEnabled, profileFingerprint, "", issuedAt, expiresAt, maxActions, reason,
	)
	if err != nil {
		return err
	}
	return writeState(path, document)
}

func WriteDevnetActivationIfRevision(
	path,
	profileFingerprint,
	expectedRevision string,
	issuedAt,
	expiresAt time.Time,
	maxActions uint32,
	reason string,
) (bool, error) {
	return writeActivationIfRevision(
		path,
		ModeDevnetEnabled,
		profileFingerprint,
		expectedRevision,
		"",
		issuedAt,
		expiresAt,
		maxActions,
		reason,
	)
}

// WriteMainnetCanaryActivationForActionIfRevision creates one short-lived
// Mainnet admission grant for exactly the reviewed action and only if the
// protected state has not changed since review.
func WriteMainnetCanaryActivationForActionIfRevision(
	path,
	profileFingerprint,
	expectedRevision,
	expectedActionID string,
	issuedAt,
	expiresAt time.Time,
	maxActions uint32,
	reason string,
) (bool, error) {
	return writeActivationIfRevision(
		path,
		ModeMainnetCanary,
		profileFingerprint,
		expectedRevision,
		expectedActionID,
		issuedAt,
		expiresAt,
		maxActions,
		reason,
	)
}

func writeActivationIfRevision(
	path,
	mode,
	profileFingerprint,
	expectedRevision,
	expectedActionID string,
	issuedAt,
	expiresAt time.Time,
	maxActions uint32,
	reason string,
) (bool, error) {
	if !validDigest(expectedRevision) {
		return false, errors.New("control state revision is invalid")
	}
	document, err := activationDocument(
		mode, profileFingerprint, expectedActionID,
		issuedAt, expiresAt, maxActions, reason,
	)
	if err != nil {
		return false, err
	}
	path, err = validateStatePath(path)
	if err != nil {
		return false, err
	}
	written := false
	err = withStateLock(path, func() error {
		current, err := stateRevision(path)
		if err != nil {
			return err
		}
		if current != expectedRevision {
			return nil
		}
		currentDocument, err := readStateDocument(path)
		if err != nil {
			return err
		}
		if err := validateActivationReplacement(currentDocument, document.Mode, time.Now()); err != nil {
			return err
		}
		if err := replaceStateDocument(path, document); err != nil {
			return err
		}
		written = true
		return nil
	})
	return written, err
}

func activationDocument(
	mode,
	profileFingerprint,
	expectedActionID string,
	issuedAt,
	expiresAt time.Time,
	maxActions uint32,
	reason string,
) (stateDocument, error) {
	lifetimeLimit, actionLimit, ok := activationLimits(mode)
	if !ok {
		return stateDocument{}, errors.New("control mode is invalid")
	}
	decoded, err := hex.DecodeString(profileFingerprint)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != profileFingerprint {
		return stateDocument{}, errors.New("profile fingerprint is invalid")
	}
	if (mode == ModeMainnetCanary && !validDigest(expectedActionID)) ||
		(mode != ModeMainnetCanary && expectedActionID != "") {
		return stateDocument{}, errors.New("control expected action ID is invalid")
	}
	issuedAt = issuedAt.UTC()
	expiresAt = expiresAt.UTC()
	if issuedAt.IsZero() || issuedAt.After(time.Now().UTC()) ||
		!expiresAt.After(issuedAt) ||
		expiresAt.Sub(issuedAt) > lifetimeLimit {
		return stateDocument{}, errors.New("control activation lifetime is invalid")
	}
	if maxActions == 0 || maxActions > actionLimit {
		return stateDocument{}, errors.New("control activation action limit is invalid")
	}
	if !validReason(reason) {
		return stateDocument{}, errors.New("control reason is invalid")
	}
	return stateDocument{
		Version:            stateVersion,
		Mode:               mode,
		ProfileFingerprint: profileFingerprint,
		ExpectedActionID:   expectedActionID,
		IssuedAt:           issuedAt,
		ExpiresAt:          expiresAt,
		MaxActions:         maxActions,
		RemainingActions:   maxActions,
		Reason:             reason,
	}, nil
}

func stateRevision(path string) (string, error) {
	data, err := securefile.ReadPrivate(path, maxStateFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		data = nil
	} else if err != nil {
		return "", errors.New("read control state revision")
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// blocksReplacement reports whether an existing document must not be replaced by
// a fresh activation. Replacing a LIVE grant would silently restore its spent
// capacity, so that is refused; an EXHAUSTED one is refused too, because
// enabling over it would erase a RecoveryActionID belonging to a send that may
// yet land. A grant whose clock simply ran out authorises nothing and is
// replaceable — refusing it told operators to stop something already stopped.
//
// The RecoveryActionID clause is NOT covered by the exhausted one. A grant can
// hold an unresolved send and still have actions left: three granted, one
// spent, that one goes ambiguous, then the clock runs out with two remaining.
// Blocking only on exhaustion let that document be overwritten, erasing the ID
// of a send that may yet land — precisely the loss the exhausted case exists to
// prevent. Expiry says the authority ended, not that the chain agreed on what
// it did; only an acknowledgement can say that.
//
// Every activation writer reaches this through validateActivationReplacement.
// The condition previously drifted when it was duplicated between writers.
func blocksReplacement(document stateDocument, now time.Time) bool {
	_, _, active := activationLimits(document.Mode)
	return active &&
		(now.Before(document.ExpiresAt) ||
			document.RemainingActions == 0 ||
			document.RecoveryActionID != "")
}

func validateActivationReplacement(
	current *stateDocument,
	nextMode string,
	now time.Time,
) error {
	if current == nil {
		return nil
	}
	if err := validateStoredStateDocument(*current, now); err != nil {
		return err
	}
	if validTerminalLatch(current.TerminalActionID, current.TerminalOutcome) {
		return errors.New("terminal action requires acknowledgement")
	}
	if current.Mode != ModeNoNewActions && current.Mode != nextMode {
		return errors.New("stop the current activation before changing control modes")
	}
	if blocksReplacement(*current, now.UTC()) {
		return errors.New("stop the current activation before enabling another")
	}
	return nil
}

func writeState(path string, document stateDocument) error {
	path, err := validateStatePath(path)
	if err != nil {
		return err
	}
	if !validReason(document.Reason) {
		return errors.New("control reason is invalid")
	}
	return withStateLock(path, func() error {
		current, err := readStateDocument(path)
		if err != nil {
			return err
		}
		if err := validateActivationReplacement(current, document.Mode, time.Now()); err != nil {
			return err
		}
		return replaceStateDocument(path, document)
	})
}

func replaceStateDocument(path string, document stateDocument) error {
	encoded, err := json.Marshal(document)
	if err != nil {
		return errors.New("encode control state")
	}
	return replaceState(path, append(encoded, '\n'))
}

func replaceState(path string, encoded []byte) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("control state target is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("inspect control state target")
	}
	parent := filepath.Dir(path)
	tempPattern := "." + filepath.Base(path) + ".*.tmp"
	file, err := os.CreateTemp(parent, tempPattern)
	if err != nil {
		return errors.New("create control state temporary file")
	}
	tempPath := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(tempPath)
	}
	if err := writeAll(file, encoded); err != nil {
		cleanup()
		return errors.New("write control state")
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return errors.New("sync control state")
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tempPath)
		return errors.New("close control state")
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return errors.New("replace control state")
	}
	directory, err := os.Open(parent)
	if err != nil {
		return errors.New("open control state directory")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("sync control state directory")
	}
	return nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
