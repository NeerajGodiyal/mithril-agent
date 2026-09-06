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
	store, err := journal.Open(path)
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
	receipt := acquisitionReceipt{CandidateSHA256: digest, ResponseSHA256: result.quote.ResponseSHA256,
		ReceivedAt: result.quote.ReceivedAt, MaxAge: maxAge}
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
	records, err := journal.ReadRecords(path)
	if err != nil {
		return "", err
	}
	if len(records) != 1 || records[0].Type != acquisitionEvent {
		return "", errors.New("acquisition journal must contain one original receipt")
	}
	var receipt acquisitionReceipt
	if err := strictjson.Decode(records[0].Payload, &receipt); err != nil {
		return "", err
	}
	if records[0].ActionID != receipt.CandidateSHA256 || records[0].At.Before(receipt.ReceivedAt) ||
		records[0].At.After(now) {
		return "", errors.New("acquisition journal time or identity is invalid")
	}
	if err := receipt.validate(candidate, now, maxAge); err != nil {
		return "", err
	}
	payload, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(payload, records[0].Payload) {
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
