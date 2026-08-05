package signer

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const buyAuthorizationLedgerVersion = 5

type buyAuthorizationReservation struct {
	Version                 uint32 `json:"version"`
	RequestSHA256           string `json:"request_sha256"`
	MessageSHA256           string `json:"message_sha256"`
	InputMint               string `json:"input_mint"`
	InputAmount             uint64 `json:"input_amount"`
	OutputMint              string `json:"output_mint"`
	MinimumOutputLamports   uint64 `json:"minimum_output_lamports"`
	FeeLamports             uint64 `json:"fee_lamports"`
	TemporaryRentLamports   uint64 `json:"temporary_rent_lamports"`
	DayStartUnix            int64  `json:"day_start_unix"`
	ScheduleWindowStartUnix int64  `json:"schedule_window_start_unix"`
	ScheduleWindowEndUnix   int64  `json:"schedule_window_end_unix"`
}

type buyAuthorizationLedger struct {
	store        *journal.Store
	lock         *os.File
	policy       Policy
	reservations map[string]buyAuthorizationReservation
	dailyInputs  map[int64]uint64
	dailyFees    map[int64]uint64
}

func authorizeAndSignBuy(
	policy Policy,
	privateKey ed25519.PrivateKey,
	request Request,
	now time.Time,
) (Response, error) {
	now = now.UTC()
	if now.IsZero() {
		return Response{}, errors.New("trusted signer time is unavailable")
	}
	nowUnix := now.Unix()
	if nowUnix < request.ScheduleWindowStartUnix || nowUnix >= request.ScheduleWindowEndUnix {
		return Response{}, errors.New("signing request schedule window does not include current UTC time")
	}
	ledger, err := openBuyAuthorizationLedger(policy, now)
	if err != nil {
		return Response{}, err
	}
	response, err := signAt(policy, privateKey, request, now)
	if err != nil {
		_ = ledger.close()
		return Response{}, err
	}
	reservation, err := buyReservationFor(policy, request, response, now)
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

func openBuyAuthorizationLedger(policy Policy, now time.Time) (*buyAuthorizationLedger, error) {
	if err := validateLedgerPath(policy.AuthorizationLedgerPath); err != nil {
		return nil, err
	}
	lock, err := acquireAuthorizationLock(policy.AuthorizationLedgerPath)
	if err != nil {
		return nil, err
	}
	closeLock := func() { _ = lock.Close() }
	_, statErr := os.Lstat(policy.AuthorizationLedgerPath)
	existed := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		closeLock()
		return nil, errors.New("authorization ledger is invalid or unavailable")
	}
	store, err := journal.Open(policy.AuthorizationLedgerPath)
	if err != nil {
		closeLock()
		if strings.Contains(err.Error(), "already open") {
			return nil, errors.New("authorization ledger is already in use")
		}
		return nil, errors.New("authorization ledger is invalid or unavailable")
	}
	fail := func() (*buyAuthorizationLedger, error) {
		_ = store.Close()
		closeLock()
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
			return fail()
		}
		header := authorizationHeader{
			Version: buyAuthorizationLedgerVersion, PolicySHA256: policyHash,
		}
		if _, err := store.Append(now.UTC(), authorizationHeaderType, "", header); err != nil {
			return fail()
		}
		records = store.Records()
	}
	ledger := &buyAuthorizationLedger{
		store: store, lock: lock, policy: policy,
		reservations: make(map[string]buyAuthorizationReservation),
		dailyInputs:  make(map[int64]uint64),
		dailyFees:    make(map[int64]uint64),
	}
	if err := ledger.load(records, policyHash); err != nil {
		return fail()
	}
	return ledger, nil
}

func (l *buyAuthorizationLedger) load(records []journal.Record, policyHash string) error {
	if len(records) == 0 || records[0].Type != authorizationHeaderType || records[0].ActionID != "" {
		return errors.New("authorization ledger header is missing")
	}
	var header authorizationHeader
	if err := strictjson.Decode(records[0].Payload, &header); err != nil ||
		header.Version != buyAuthorizationLedgerVersion || header.PolicySHA256 != policyHash {
		return errors.New("authorization ledger policy binding does not match")
	}
	for _, record := range records[1:] {
		if record.Type != authorizationReserveType || record.ActionID == "" {
			return errors.New("authorization ledger has an invalid record type")
		}
		var reservation buyAuthorizationReservation
		if err := strictjson.Decode(record.Payload, &reservation); err != nil ||
			!l.validReservation(record, reservation) {
			return errors.New("authorization ledger has an invalid reservation")
		}
		if _, exists := l.reservations[record.ActionID]; exists {
			return errors.New("authorization ledger repeats an action ID")
		}
		input := l.dailyInputs[reservation.DayStartUnix]
		fees := l.dailyFees[reservation.DayStartUnix]
		if input > l.policy.DailyInputTokenCap ||
			reservation.InputAmount > l.policy.DailyInputTokenCap-input ||
			fees > l.policy.DailyNativeFeeCapLamports ||
			reservation.FeeLamports > l.policy.DailyNativeFeeCapLamports-fees {
			return errors.New("authorization ledger exceeds its policy cap")
		}
		l.dailyInputs[reservation.DayStartUnix] = input + reservation.InputAmount
		l.dailyFees[reservation.DayStartUnix] = fees + reservation.FeeLamports
		l.reservations[record.ActionID] = reservation
	}
	return nil
}

func (l *buyAuthorizationLedger) validReservation(
	record journal.Record,
	reservation buyAuthorizationReservation,
) bool {
	if reservation.Version != buyAuthorizationLedgerVersion ||
		!validDigest(reservation.RequestSHA256) || !validDigest(reservation.MessageSHA256) ||
		reservation.InputMint != l.policy.OrcaBuy.TokenMintB ||
		reservation.OutputMint != l.policy.OrcaBuy.TokenMintA ||
		reservation.InputAmount != l.policy.MaxInputTokenAmount ||
		reservation.MinimumOutputLamports < l.policy.OrcaBuy.MinOutputLamports ||
		reservation.FeeLamports == 0 || reservation.FeeLamports > l.policy.MaxFeeLamports ||
		reservation.TemporaryRentLamports == 0 ||
		reservation.TemporaryRentLamports > l.policy.OrcaBuy.MaxTemporaryRentLamports ||
		reservation.DayStartUnix <= 0 || reservation.DayStartUnix%secondsPerDay != 0 ||
		reservation.ScheduleWindowStartUnix >= reservation.ScheduleWindowEndUnix {
		return false
	}
	if _, err := solana.Decode32(reservation.InputMint); err != nil {
		return false
	}
	if _, err := solana.Decode32(reservation.OutputMint); err != nil {
		return false
	}
	at := record.At.UTC().Unix()
	return at >= reservation.DayStartUnix && at < reservation.DayStartUnix+secondsPerDay &&
		at >= reservation.ScheduleWindowStartUnix && at < reservation.ScheduleWindowEndUnix
}

func buyReservationFor(
	policy Policy,
	request Request,
	response Response,
	now time.Time,
) (buyAuthorizationReservation, error) {
	requestHash, err := immutableRequestHash(request)
	if err != nil {
		return buyAuthorizationReservation{}, err
	}
	message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
	if err != nil {
		return buyAuthorizationReservation{}, errors.New("decode authorization message")
	}
	messageHash := sha256.Sum256(message)
	if response.MessageSHA256 != hex.EncodeToString(messageHash[:]) {
		return buyAuthorizationReservation{}, errors.New("signed message binding does not match")
	}
	validated, err := ValidateRequest(policy, request)
	if err != nil || validated.InputAmount == 0 || validated.InputMint == "" ||
		validated.OutputMint == "" || validated.MinimumOutput == 0 ||
		validated.NativeDebitLamports != request.FeeLamports ||
		validated.TemporaryRentLamports == 0 {
		return buyAuthorizationReservation{}, errors.New("decode buy authorization debit")
	}
	nowUnix := now.UTC().Unix()
	dayStart := nowUnix - nowUnix%secondsPerDay
	return buyAuthorizationReservation{
		Version:       buyAuthorizationLedgerVersion,
		RequestSHA256: requestHash, MessageSHA256: response.MessageSHA256,
		InputMint: validated.InputMint, InputAmount: validated.InputAmount,
		OutputMint: validated.OutputMint, MinimumOutputLamports: validated.MinimumOutput,
		FeeLamports:             request.FeeLamports,
		TemporaryRentLamports:   validated.TemporaryRentLamports,
		DayStartUnix:            dayStart,
		ScheduleWindowStartUnix: request.ScheduleWindowStartUnix,
		ScheduleWindowEndUnix:   request.ScheduleWindowEndUnix,
	}, nil
}

func (l *buyAuthorizationLedger) reserve(
	now time.Time,
	actionID string,
	reservation buyAuthorizationReservation,
) error {
	if existing, ok := l.reservations[actionID]; ok {
		if existing == reservation {
			return nil
		}
		return errors.New("action ID is already reserved for a different request")
	}
	input := l.dailyInputs[reservation.DayStartUnix]
	fees := l.dailyFees[reservation.DayStartUnix]
	if input > l.policy.DailyInputTokenCap ||
		reservation.InputAmount > l.policy.DailyInputTokenCap-input {
		return errors.New("signer daily input-token cap would be exceeded")
	}
	if fees > l.policy.DailyNativeFeeCapLamports ||
		reservation.FeeLamports > l.policy.DailyNativeFeeCapLamports-fees {
		return errors.New("signer daily native-debit cap would be exceeded")
	}
	if _, err := l.store.Append(now.UTC(), authorizationReserveType, actionID, reservation); err != nil {
		return errors.New("authorization reservation could not be made durable")
	}
	l.reservations[actionID] = reservation
	l.dailyInputs[reservation.DayStartUnix] = input + reservation.InputAmount
	l.dailyFees[reservation.DayStartUnix] = fees + reservation.FeeLamports
	return nil
}

func (l *buyAuthorizationLedger) close() error {
	if l == nil {
		return nil
	}
	var closeErr error
	if l.store != nil {
		closeErr = l.store.Close()
		l.store = nil
	}
	if l.lock != nil {
		if err := l.lock.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
		l.lock = nil
	}
	if closeErr != nil {
		return errors.New("authorization ledger could not be closed safely")
	}
	return nil
}
