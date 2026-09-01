package paperdashboard

import (
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/researchpacket"
)

type Research struct {
	HypothesisID    string                           `json:"hypothesis_id"`
	Market          string                           `json:"market"`
	Disposition     string                           `json:"disposition"`
	CreatedAt       time.Time                        `json:"created_at"`
	ValidUntil      time.Time                        `json:"valid_until"`
	Current         bool                             `json:"current"`
	Actionable      bool                             `json:"actionable"`
	VerifiedFacts   int                              `json:"verified_facts"`
	Sources         int                              `json:"sources"`
	RiskDecision    string                           `json:"risk_decision"`
	RiskReason      string                           `json:"risk_reason"`
	ProposedChanges []researchpacket.ParameterChange `json:"proposed_changes,omitempty"`
	ContentSHA256   string                           `json:"content_sha256"`
}

func (s *Server) EnableResearch(path string) error {
	if !cleanAbsolutePath(path) {
		return errors.New("paper research path must be a clean absolute path")
	}
	s.researchPath = path
	return nil
}

func readResearch(path string, now time.Time) (*Research, error) {
	if !cleanAbsolutePath(path) {
		return nil, errors.New("paper research path must be a clean absolute path")
	}
	data, err := securefile.ReadPrivate(path, researchpacket.MaxBytes)
	if err != nil {
		return nil, err
	}
	packet, err := researchpacket.DecodeStored(data)
	if err != nil {
		return nil, err
	}
	status := packet.StatusAt(now)
	return &Research{
		HypothesisID: packet.HypothesisID, Market: packet.Market,
		Disposition: packet.Disposition, CreatedAt: packet.CreatedAt,
		ValidUntil: packet.ValidUntil, Current: status.Current,
		Actionable: status.Actionable, VerifiedFacts: status.VerifiedFacts,
		Sources: status.Sources, RiskDecision: packet.RiskVeto.Decision,
		RiskReason: packet.RiskVeto.Reason,
		ProposedChanges: append([]researchpacket.ParameterChange(nil),
			packet.CandidateParameterDiff...),
		ContentSHA256: packet.ContentSHA256,
	}, nil
}
