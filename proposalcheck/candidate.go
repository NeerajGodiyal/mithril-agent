package proposalcheck

import (
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
)

const (
	CandidateVersion  = uint32(1)
	maxCandidateBytes = 512 << 10
)

// Candidate is the portable, keyless input to a separate policy authority.
// Provider identities are deliberately absent: protected authority
// configuration chooses the providers used by Recheck.
type Candidate struct {
	Version              uint32                             `json:"version"`
	Policy               jupiterswap.Policy                 `json:"policy"`
	Request              jupiterquote.Request               `json:"request"`
	Quote                jupiterquote.Result                `json:"quote"`
	MessageBase64        string                             `json:"message_base64"`
	AddressTables        []jupiterswap.AddressTableEvidence `json:"address_tables,omitempty"`
	LastValidBlockHeight uint64                             `json:"last_valid_block_height"`
}

// Candidate returns a detached copy of the exact checked proposal.
func (r Result) Candidate() (Candidate, error) {
	tables, err := jupiterswap.EncodeAddressTables(r.addressTables)
	if err != nil {
		return Candidate{}, errors.New("encode checked address tables")
	}
	return Candidate{
		Version: CandidateVersion, Policy: r.policy, Request: r.request, Quote: r.quote,
		MessageBase64:        base64.StdEncoding.EncodeToString(r.message),
		AddressTables:        append([]jupiterswap.AddressTableEvidence(nil), tables...),
		LastValidBlockHeight: r.lastValidBlockHeight,
	}, nil
}

// EncodeCandidate validates before producing a portable JSON artifact.
func EncodeCandidate(candidate Candidate) ([]byte, error) {
	if _, _, err := candidate.material(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(candidate)
	if err != nil || len(encoded) > maxCandidateBytes {
		return nil, errors.New("encode Jupiter candidate")
	}
	return encoded, nil
}

// DecodeCandidate strictly decodes and semantically validates a portable
// proposal. It rejects unknown or duplicate fields before any provider call.
func DecodeCandidate(data []byte) (Candidate, error) {
	if len(data) == 0 || len(data) > maxCandidateBytes {
		return Candidate{}, errors.New("Jupiter candidate size is invalid")
	}
	var candidate Candidate
	if err := strictjson.Decode(data, &candidate); err != nil {
		return Candidate{}, errors.New("decode Jupiter candidate")
	}
	if _, _, err := candidate.material(); err != nil {
		return Candidate{}, err
	}
	candidate.AddressTables = append(
		[]jupiterswap.AddressTableEvidence(nil), candidate.AddressTables...,
	)
	return candidate, nil
}

// ValidateCandidateMaterial requires a candidate to match independently
// protected policy before returning detached canonical message material.
func ValidateCandidateMaterial(
	expectedPolicy jupiterswap.Policy,
	candidate Candidate,
) ([]byte, map[[32]byte][][32]byte, error) {
	if err := expectedPolicy.Validate(); err != nil || candidate.Policy != expectedPolicy {
		return nil, nil, errors.New("Jupiter candidate does not match protected policy")
	}
	return candidate.material()
}

func (candidate Candidate) material() (
	[]byte,
	map[[32]byte][][32]byte,
	error,
) {
	if candidate.Version != CandidateVersion || candidate.LastValidBlockHeight == 0 {
		return nil, nil, errors.New("Jupiter candidate version or lifetime is invalid")
	}
	if err := candidate.Quote.Validate(candidate.Request); err != nil {
		return nil, nil, errors.New("Jupiter candidate quote is invalid")
	}
	message, err := base64.StdEncoding.Strict().DecodeString(candidate.MessageBase64)
	if err != nil || len(message) == 0 ||
		base64.StdEncoding.EncodeToString(message) != candidate.MessageBase64 {
		return nil, nil, errors.New("Jupiter candidate message is invalid")
	}
	tables, err := jupiterswap.DecodeAddressTables(candidate.AddressTables)
	if err != nil {
		return nil, nil, errors.New("Jupiter candidate address tables are invalid")
	}
	if _, err := jupiterswap.ValidateV0Message(
		candidate.Policy, candidate.Request, candidate.Quote, message, tables,
	); err != nil {
		return nil, nil, errors.New("Jupiter candidate is not canonical")
	}
	return message, tables, nil
}
