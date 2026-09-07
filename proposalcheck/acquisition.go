package proposalcheck

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
)

const acquisitionEvent = "proposal.acquired-v1"

type acquisitionReceipt struct {
	CandidateSHA256 string        `json:"candidate_sha256"`
	ResponseSHA256  string        `json:"response_sha256"`
	ReceivedAt      time.Time     `json:"received_at"`
	MaxAge          time.Duration `json:"max_age_ns,string"`
	Candidate       *Candidate    `json:"candidate,omitempty"`
}

// Acquisition binds a portable candidate to its original protected host receipt.
// ReceivedAt is local receipt time, not a provider timestamp or renewed quote TTL.
// The digest is provenance, not permission to sign or submit.
type Acquisition struct {
	Candidate  Candidate
	SHA256     string
	ReceivedAt time.Time
}

// CheckAndRecordAcquisition checks a newly builder-acquired proposal and durably
// records its original receipt time before returning. The caller must protect
// path and configure a trusted Builder; this is host provenance, not cryptographic
// Jupiter authorship. Rechecks and imported candidates cannot create receipts.
// No authority, signature, submission or durable funds reservation is produced.
func CheckAndRecordAcquisition(ctx context.Context, path string, maxAge time.Duration,
	builder Builder, evidence Evidence, primary, secondary FinalizedSlotReader,
	primaryTrustDomain, secondaryTrustDomain, archiveProbeSignature string,
	policy jupiterswap.Policy, request jupiterquote.Request,
) (result Result, err error) {
	if maxAge <= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return Result{}, errors.New("acquisition requires a positive age bound and protected absolute journal path")
	}
	store, err := journal.OpenStrict(path)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			result = Result{}
			err = errors.Join(err, closeErr)
		}
	}()
	result, err = Check(ctx, builder, evidence, primary, secondary,
		primaryTrustDomain, secondaryTrustDomain, archiveProbeSignature, policy, request)
	if err != nil {
		return Result{}, err
	}
	candidate, err := result.Candidate()
	if err != nil {
		return Result{}, err
	}
	digest, err := acquisitionCandidateHash(candidate)
	if err != nil {
		return Result{}, err
	}
	encoded, err := EncodeCandidate(candidate)
	if err != nil {
		return Result{}, err
	}
	portable, err := DecodeCandidate(encoded)
	if err != nil {
		return Result{}, err
	}
	receipt := acquisitionReceipt{CandidateSHA256: digest, ResponseSHA256: result.quote.ResponseSHA256,
		ReceivedAt: result.quote.ReceivedAt, MaxAge: maxAge, Candidate: &portable}
	// Check may perform slow provider calls. Never compare a newly received
	// quote with a clock value captured before those calls.
	now := time.Now().UTC()
	if err := receipt.validate(candidate, now, maxAge); err != nil {
		return Result{}, err
	}
	if err := appendAcquisition(store, receipt, now); err != nil {
		return Result{}, err
	}
	return result, nil
}

func appendAcquisition(store *journal.Store, receipt acquisitionReceipt, now time.Time) error {
	payload, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if records := store.Records(); len(records) != 0 {
		if len(records) != 1 || records[0].Type != acquisitionEvent ||
			records[0].ActionID != receipt.CandidateSHA256 || !bytes.Equal(records[0].Payload, payload) {
			return errors.New("acquisition journal already contains different provenance")
		}
		return nil
	}
	_, err = store.Append(now, acquisitionEvent, receipt.CandidateSHA256, receipt)
	return err
}

// VerifyAcquisition reads a caller-protected host receipt without creating or
// renewing it. Its digest binds the exact candidate and original expiry; it is
// not an authorization token. Missing legacy provenance fails closed.
func VerifyAcquisition(path string, candidate Candidate, now time.Time, maxAge time.Duration) (string, error) {
	record, receipt, err := readAcquisition(path)
	if err != nil {
		return "", err
	}
	return verifyAcquisitionRecord(record, receipt, candidate, now, maxAge)
}

// ReadAcquiredCandidate returns only the exact portable candidate retained with
// its original acquisition receipt. It performs no RPC, writes or renewal.
// Historical receipts without candidate bytes cannot be reconstructed here.
func ReadAcquiredCandidate(path string, now time.Time, maxAge time.Duration) (Candidate, error) {
	acquired, err := ReadAcquisition(path, now, maxAge)
	return acquired.Candidate, err
}

// ReadAcquisition verifies candidate, receipt identity and original receipt time
// from one journal read. It never adds metadata to the portable candidate.
func ReadAcquisition(path string, now time.Time, maxAge time.Duration) (Acquisition, error) {
	record, receipt, err := readAcquisition(path)
	if err != nil {
		return Acquisition{}, err
	}
	if receipt.Candidate == nil {
		return Acquisition{}, errors.New("acquisition candidate was not retained")
	}
	digest, err := verifyAcquisitionRecord(record, receipt, *receipt.Candidate, now, maxAge)
	if err != nil {
		return Acquisition{}, err
	}
	return Acquisition{Candidate: *receipt.Candidate, SHA256: digest, ReceivedAt: receipt.ReceivedAt}, nil
}

func readAcquisition(path string) (journal.Record, acquisitionReceipt, error) {
	records, err := journal.ReadRecords(path)
	if err != nil {
		return journal.Record{}, acquisitionReceipt{}, err
	}
	if len(records) != 1 || records[0].Type != acquisitionEvent {
		return journal.Record{}, acquisitionReceipt{}, errors.New("acquisition journal must contain one original receipt")
	}
	var receipt acquisitionReceipt
	if err := strictjson.Decode(records[0].Payload, &receipt); err != nil {
		return journal.Record{}, acquisitionReceipt{}, err
	}
	return records[0], receipt, nil
}

func verifyAcquisitionRecord(record journal.Record, receipt acquisitionReceipt, candidate Candidate, now time.Time, maxAge time.Duration) (string, error) {
	if record.ActionID != receipt.CandidateSHA256 || record.At.Before(receipt.ReceivedAt) ||
		record.At.After(now) {
		return "", errors.New("acquisition journal time or identity is invalid")
	}
	if err := receipt.validate(candidate, now, maxAge); err != nil {
		return "", err
	}
	payload, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(payload, record.Payload) {
		return "", errors.New("acquisition receipt is not canonical")
	}
	digest := sha256.Sum256(append([]byte(acquisitionEvent+"\x00"), payload...))
	return hex.EncodeToString(digest[:]), nil
}

func (r acquisitionReceipt) validate(candidate Candidate, now time.Time, maxAge time.Duration) error {
	digest, err := acquisitionCandidateHash(candidate)
	if err != nil || digest != r.CandidateSHA256 || !validSHA256(r.ResponseSHA256) ||
		maxAge <= 0 || r.MaxAge != maxAge || now.IsZero() || r.ReceivedAt.IsZero() ||
		r.ReceivedAt.After(now) || r.ReceivedAt.Before(now.Add(-maxAge)) {
		return errors.New("acquisition provenance is missing, changed or expired")
	}
	if r.Candidate != nil {
		retained, err := acquisitionCandidateHash(*r.Candidate)
		if err != nil || retained != digest {
			return errors.New("retained acquisition candidate differs from receipt")
		}
	}
	return nil
}

func acquisitionCandidateHash(candidate Candidate) (string, error) {
	encoded, err := EncodeCandidate(candidate)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
