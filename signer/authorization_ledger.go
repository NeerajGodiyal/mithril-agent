package signer

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
)

const (
	authorizationLedgerVersion = 4
	authorizationHeaderType    = "signer.authorization.initialized"
	authorizationReserveType   = "signer.authorization.reserved"
	authorizationRequestDomain = "mithril-agent/signer-authorization-request-v2"
	secondsPerDay              = int64(86_400)
)

// errLedgerPolicyChanged separates "you edited a cap" from "this file is
// damaged". Both used to arrive as "invalid or unavailable", which is the one
// pair an operator most needs told apart: one is something they just did on
// purpose, the other is a fault. Worse, the two call for opposite reactions,
// and the reaction to a cap edit has a consequence nothing announced.
var errLedgerPolicyChanged = errors.New("authorization ledger policy binding does not match")

type authorizationHeader struct {
	Version      uint32 `json:"version"`
	PolicySHA256 string `json:"policy_sha256"`
}

type authorizationReservation struct {
	Version                 uint32 `json:"version"`
	RequestSHA256           string `json:"request_sha256"`
	MessageSHA256           string `json:"message_sha256"`
	AmountLamports          uint64 `json:"amount_lamports"`
	FeeLamports             uint64 `json:"fee_lamports"`
	ExtraDebitLamports      uint64 `json:"extra_debit_lamports,omitempty"`
	DebitLamports           uint64 `json:"debit_lamports"`
	DayStartUnix            int64  `json:"day_start_unix"`
	ScheduleWindowStartUnix int64  `json:"schedule_window_start_unix"`
	ScheduleWindowEndUnix   int64  `json:"schedule_window_end_unix"`
	CustodyTimestampMS      int64  `json:"custody_timestamp_ms,omitempty"`
}

type authorizationLedger struct {
	store        *journal.Store
	policy       Policy
	reservations map[string]authorizationReservation
	dailyDebits  map[int64]uint64
}

// AuthorizeAndSign is the stateful signer entry point. It holds the
// signer-owned ledger lock while validating and signing, then fsyncs the
// reservation before returning the signature.
func AuthorizeAndSign(
	policy Policy,
	privateKey ed25519.PrivateKey,
	request Request,
	now time.Time,
) (Response, error) {
	if err := policy.validateAuthorization(); err != nil {
		return Response{}, err
	}
	if policy.Profile == orcaswap.BuyProfileName {
		return authorizeAndSignBuy(policy, privateKey, request, now)
	}
	if err := ValidateScheduleWindowAt(request, now); err != nil {
		return Response{}, err
	}
	now = now.UTC()

	ledger, err := openAuthorizationLedger(policy, now)
	if err != nil {
		return Response{}, err
	}
	response, signErr := signAt(policy, privateKey, request, now)
	if signErr != nil {
		_ = ledger.close()
		return Response{}, signErr
	}
	reservation, err := reservationFor(policy, request, response, now)
	if err != nil {
		_ = ledger.close()
		return Response{}, err
	}
	if err := ledger.reserve(now, request.ActionID, reservation); err != nil {
		_ = ledger.close()
		return Response{}, err
	}
	if err := ledger.close(); err != nil {
		return Response{}, err
	}
	return response, nil
}

func openAuthorizationLedger(policy Policy, now time.Time) (*authorizationLedger, error) {
	if err := validateLedgerPath(policy.AuthorizationLedgerPath); err != nil {
		return nil, err
	}
	_, statErr := os.Lstat(policy.AuthorizationLedgerPath)
	existed := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, errors.New("authorization ledger is invalid or unavailable")
	}
	store, err := journal.OpenRotating(policy.AuthorizationLedgerPath)
	if err != nil {
		if errors.Is(err, journal.ErrLocked) {
			return nil, errors.New("authorization ledger is already in use")
		}
		return nil, errors.New("authorization ledger is invalid or unavailable")
	}
	fail := func() (*authorizationLedger, error) {
		_ = store.Close()
		return nil, errors.New("authorization ledger is invalid or unavailable")
	}
	if err := validateOpenedLedger(policy.AuthorizationLedgerPath); err != nil {
		return fail()
	}

	policyHash, err := authorizationPolicyHash(policy)
	if err != nil {
		return fail()
	}
	records := store.Records()
	if len(records) == 0 {
		if existed {
			// Stays rejected: a truncated real ledger is indistinguishable from
			// a crash between journal.Open's create and the header append. Name
			// the state so the operator can inspect and remove the file rather
			// than reading this as generic corruption.
			_ = store.Close()
			return nil, errors.New("authorization ledger is empty and cannot be reinitialized automatically; inspect and remove it if this followed a first-run crash")
		}
		header := authorizationHeader{
			Version:      authorizationLedgerVersion,
			PolicySHA256: policyHash,
		}
		if _, err := store.Append(now.UTC(), authorizationHeaderType, "", header); err != nil {
			return fail()
		}
		records = store.Records()
	}

	ledger := &authorizationLedger{
		store:        store,
		policy:       policy,
		reservations: make(map[string]authorizationReservation),
		dailyDebits:  make(map[int64]uint64),
	}
	if err := ledger.load(records, policyHash); err != nil {
		if errors.Is(err, errLedgerPolicyChanged) {
			_ = store.Close()
			return nil, ledgerPolicyChangedError()
		}
		return fail()
	}
	return ledger, nil
}

// ledgerPolicyChangedError names the exposure an operator cannot infer from a
// hash mismatch: rebuilding before the UTC reset starts another full allowance.
func ledgerPolicyChangedError() error {
	return errors.New(
		"this ledger recorded today's spending under different caps, so a cap was " +
			"edited. The day's accumulated spend cannot carry across that edit: a new " +
			"setup starts today at zero, so tightening a cap now RAISES what today " +
			"still allows, until 00:00 UTC. Wait for the reset to tighten safely, or " +
			"accept the wider day deliberately")
}

func validateLedgerPath(path string) error {
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() ||
		info.Mode().Perm()&0o077 != 0 || !ledgerOwnedByCurrentUser(info) {
		return errors.New("authorization ledger directory is not a private signer-owned directory")
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return errors.New("authorization ledger directory must not traverse a symlink")
	}
	return validateLedgerObject(path, true)
}

func validateOpenedLedger(path string) error {
	return validateLedgerObject(path, false)
}

func validateLedgerObject(path string, allowMissing bool) error {
	info, err := os.Lstat(path)
	if allowMissing && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Mode().Perm()&0o077 != 0 || !ledgerOwnedByCurrentUser(info) {
		return errors.New("authorization ledger must be a private signer-owned regular file")
	}
	return nil
}

func authorizationPolicyHash(policy Policy) (string, error) {
	encoded, err := json.Marshal(policy)
	if err != nil {
		return "", errors.New("encode authorization policy binding")
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func (l *authorizationLedger) load(records []journal.Record, policyHash string) error {
	if len(records) == 0 || records[0].Type != authorizationHeaderType ||
		records[0].ActionID != "" {
		return errors.New("authorization ledger header is missing")
	}
	var header authorizationHeader
	if err := strictjson.Decode(records[0].Payload, &header); err != nil ||
		header.Version != authorizationLedgerVersion {
		return errors.New("authorization ledger header is invalid")
	}
	if header.PolicySHA256 != policyHash {
		return errLedgerPolicyChanged
	}
	for _, record := range records[1:] {
		if record.Type == journal.EventRotated && record.ActionID == "" {
			continue
		}
		if record.Type != authorizationReserveType || record.ActionID == "" {
			return errors.New("authorization ledger has an invalid record type")
		}
		var reservation authorizationReservation
		if err := strictjson.Decode(record.Payload, &reservation); err != nil ||
			!validReservation(record, reservation) {
			return errors.New("authorization ledger has an invalid reservation")
		}
		if _, exists := l.reservations[record.ActionID]; exists {
			return errors.New("authorization ledger repeats an action ID")
		}
		total := l.dailyDebits[reservation.DayStartUnix]
		if total > l.policy.DailyDebitCapLamports ||
			reservation.DebitLamports > l.policy.DailyDebitCapLamports-total {
			return errors.New("authorization ledger exceeds its policy cap")
		}
		l.dailyDebits[reservation.DayStartUnix] = total + reservation.DebitLamports
		l.reservations[record.ActionID] = reservation
	}
	return nil
}

func validReservation(record journal.Record, reservation authorizationReservation) bool {
	if reservation.Version != authorizationLedgerVersion ||
		!validDigest(reservation.RequestSHA256) ||
		!validDigest(reservation.MessageSHA256) ||
		reservation.AmountLamports == 0 ||
		reservation.FeeLamports == 0 ||
		reservation.AmountLamports > ^uint64(0)-reservation.FeeLamports ||
		reservation.AmountLamports+reservation.FeeLamports > ^uint64(0)-reservation.ExtraDebitLamports ||
		reservation.DebitLamports != reservation.AmountLamports+reservation.FeeLamports+
			reservation.ExtraDebitLamports ||
		reservation.DayStartUnix <= 0 ||
		reservation.DayStartUnix%secondsPerDay != 0 ||
		reservation.ScheduleWindowStartUnix >= reservation.ScheduleWindowEndUnix {
		return false
	}
	at := record.At.UTC().Unix()
	return at >= reservation.DayStartUnix &&
		at < reservation.DayStartUnix+secondsPerDay &&
		at >= reservation.ScheduleWindowStartUnix &&
		at < reservation.ScheduleWindowEndUnix
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size &&
		hex.EncodeToString(decoded) == value
}

func reservationFor(
	policy Policy,
	request Request,
	response Response,
	now time.Time,
) (authorizationReservation, error) {
	message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
	if err != nil {
		return authorizationReservation{}, errors.New("decode authorization message")
	}
	messageHash := sha256.Sum256(message)
	if response.MessageSHA256 != hex.EncodeToString(messageHash[:]) {
		return authorizationReservation{}, errors.New("signed message binding does not match")
	}
	validated, err := validateAuthorizationRequest(policy, request)
	if err != nil {
		return authorizationReservation{}, errors.New("decode authorization debit")
	}
	return reservationForValidated(request, validated, now)
}

func reservationForValidated(
	request Request,
	validated ValidatedRequest,
	now time.Time,
) (authorizationReservation, error) {
	requestHash, err := immutableRequestHash(request)
	if err != nil {
		return authorizationReservation{}, err
	}
	baseDebit := validated.AmountLamports + request.FeeLamports
	if validated.DebitLamports < baseDebit {
		return authorizationReservation{}, errors.New("authorization debit overflows")
	}
	nowUnix := now.UTC().Unix()
	dayStart := nowUnix - nowUnix%secondsPerDay
	return authorizationReservation{
		Version:                 authorizationLedgerVersion,
		RequestSHA256:           requestHash,
		MessageSHA256:           validated.MessageSHA256,
		AmountLamports:          validated.AmountLamports,
		FeeLamports:             request.FeeLamports,
		ExtraDebitLamports:      validated.DebitLamports - baseDebit,
		DebitLamports:           validated.DebitLamports,
		DayStartUnix:            dayStart,
		ScheduleWindowStartUnix: request.ScheduleWindowStartUnix,
		ScheduleWindowEndUnix:   request.ScheduleWindowEndUnix,
	}, nil
}

func validateAuthorizationRequest(policy Policy, request Request) (ValidatedRequest, error) {
	if policy.Jupiter != nil {
		return ValidateJupiterRequest(policy, request)
	}
	return ValidateRequest(policy, request)
}

func immutableRequestHash(request Request) (string, error) {
	unsigned := struct {
		Domain                  string                          `json:"domain"`
		Cluster                 string                          `json:"cluster"`
		Profile                 string                          `json:"profile"`
		ProfileVersion          uint32                          `json:"profile_version"`
		ProfileFingerprint      string                          `json:"profile_sha256"`
		ActionID                string                          `json:"action_id"`
		ScheduleWindowStartUnix int64                           `json:"schedule_window_start_unix"`
		ScheduleWindowEndUnix   int64                           `json:"schedule_window_end_unix"`
		MessageBase64           string                          `json:"message_base64"`
		BlockhashContextSlot    uint64                          `json:"blockhash_context_slot"`
		FeeLamports             uint64                          `json:"fee_lamports"`
		FeeMinContextSlot       uint64                          `json:"fee_min_context_slot"`
		PrimaryFeeContextSlot   uint64                          `json:"primary_fee_context_slot"`
		SecondaryFeeContextSlot uint64                          `json:"secondary_fee_context_slot"`
		RecentBlockhash         string                          `json:"recent_blockhash"`
		ObservedBlockHeight     uint64                          `json:"observed_block_height"`
		LastValidBlockHeight    uint64                          `json:"last_valid_block_height"`
		JupiterCandidate        *proposalcheck.Candidate        `json:"jupiter_candidate,omitempty"`
		JupiterProviders        *proposalcheck.ProviderBindings `json:"jupiter_providers,omitempty"`
	}{
		Domain: request.Domain, Cluster: request.Cluster,
		Profile: request.Profile, ProfileVersion: request.ProfileVersion,
		ProfileFingerprint: request.ProfileFingerprint, ActionID: request.ActionID,
		ScheduleWindowStartUnix: request.ScheduleWindowStartUnix,
		ScheduleWindowEndUnix:   request.ScheduleWindowEndUnix,
		MessageBase64:           request.MessageBase64, BlockhashContextSlot: request.BlockhashContextSlot,
		FeeLamports: request.FeeLamports, FeeMinContextSlot: request.FeeMinContextSlot,
		PrimaryFeeContextSlot:   request.PrimaryFeeContextSlot,
		SecondaryFeeContextSlot: request.SecondaryFeeContextSlot,
		RecentBlockhash:         request.RecentBlockhash, ObservedBlockHeight: request.ObservedBlockHeight,
		LastValidBlockHeight: request.LastValidBlockHeight,
		JupiterCandidate:     request.JupiterCandidate,
		JupiterProviders:     request.JupiterProviders,
	}
	encoded, err := json.Marshal(unsigned)
	if err != nil {
		return "", errors.New("encode authorization request binding")
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(authorizationRequestDomain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(encoded)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (l *authorizationLedger) reserve(
	now time.Time,
	actionID string,
	reservation authorizationReservation,
) error {
	_, err := l.reserveEffective(now, actionID, reservation)
	return err
}

func (l *authorizationLedger) reserveEffective(
	now time.Time,
	actionID string,
	reservation authorizationReservation,
) (authorizationReservation, error) {
	if existing, ok := l.reservations[actionID]; ok {
		if sameAuthorizationReservation(existing, reservation) {
			return existing, l.releaseCapacity()
		}
		return authorizationReservation{}, errors.New("action ID is already reserved for a different request")
	}
	total := l.dailyDebits[reservation.DayStartUnix]
	if total > l.policy.DailyDebitCapLamports ||
		reservation.DebitLamports > l.policy.DailyDebitCapLamports-total {
		return authorizationReservation{}, refused("signer daily debit cap would be exceeded")
	}
	if _, err := l.store.Append(now.UTC(), authorizationReserveType, actionID, reservation); err != nil {
		return authorizationReservation{}, errors.New("authorization reservation could not be made durable")
	}
	l.reservations[actionID] = reservation
	l.dailyDebits[reservation.DayStartUnix] = total + reservation.DebitLamports
	return reservation, l.releaseCapacity()
}

func sameAuthorizationReservation(first, second authorizationReservation) bool {
	first.CustodyTimestampMS = 0
	second.CustodyTimestampMS = 0
	return first == second
}

func (l *authorizationLedger) releaseCapacity() error {
	if err := l.store.ReleaseCapacity(); err != nil {
		return errors.New("authorization ledger rotation could not be made durable")
	}
	return nil
}

func (l *authorizationLedger) close() error {
	if l == nil {
		return nil
	}
	var closeErr error
	if l.store != nil {
		closeErr = l.store.Close()
		l.store = nil
	}
	if closeErr != nil {
		return errors.New("authorization ledger could not be closed safely")
	}
	return nil
}
