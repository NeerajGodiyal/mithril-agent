package paperdashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/researchpacket"
)

const (
	researchEvidenceVersion  = uint32(1)
	maxResearchEvidenceBytes = 128 << 10
	maxPerpsResearchBytes    = 256 << 10
)

var errResearchEvidenceUnavailable = errors.New("research session evidence is unavailable")

type ResearchToolCount struct {
	Name  string `json:"name"`
	Count uint64 `json:"count"`
}

type researchEvidence struct {
	Version               uint32              `json:"version"`
	CreatedAt             time.Time           `json:"created_at"`
	PacketSHA256          string              `json:"packet_sha256"`
	SessionExportSHA256   string              `json:"session_export_sha256"`
	SessionCount          uint64              `json:"session_count"`
	ToolCalls             []ResearchToolCount `json:"tool_calls"`
	SuccessfulWebSearches uint64              `json:"successful_web_searches"`
	RetrievedURLs         []string            `json:"retrieved_urls"`
	OfficialPagesChecked  uint64              `json:"official_pages_checked"`
}

type Research struct {
	HypothesisID          string                           `json:"hypothesis_id"`
	Market                string                           `json:"market"`
	Disposition           string                           `json:"disposition"`
	CreatedAt             time.Time                        `json:"created_at"`
	ValidUntil            time.Time                        `json:"valid_until"`
	Current               bool                             `json:"current"`
	Actionable            bool                             `json:"actionable"`
	TwoSourceClaims       int                              `json:"two_source_claims"`
	RetrievedCitations    int                              `json:"retrieved_citations"`
	SourcesChecked        int                              `json:"sources_checked"`
	OfficialPagesChecked  uint64                           `json:"official_pages_checked"`
	RetrievedPages        uint64                           `json:"retrieved_pages"`
	ResearchSessions      uint64                           `json:"research_sessions"`
	ResearchToolCalls     []ResearchToolCount              `json:"research_tool_calls,omitempty"`
	SuccessfulWebSearches uint64                           `json:"successful_web_searches"`
	SingleSource          int                              `json:"single_source_facts"`
	Contradicted          int                              `json:"contradicted_facts"`
	Unverified            int                              `json:"unverified_facts"`
	RiskDecision          string                           `json:"risk_decision"`
	RiskReason            string                           `json:"risk_reason"`
	ProposedChanges       []researchpacket.ParameterChange `json:"proposed_changes,omitempty"`
	ContentSHA256         string                           `json:"content_sha256"`
}

type perpsResearchMarket struct {
	Market                        string                             `json:"market"`
	PaperStatusSHA256             string                             `json:"paper_status_sha256"`
	DecisionSource                string                             `json:"decision_source,omitempty"`
	ProposalSource                string                             `json:"proposal_source,omitempty"`
	RunStrategy                   string                             `json:"run_strategy,omitempty"`
	RunRiskProfile                string                             `json:"run_risk_profile,omitempty"`
	RunPlanSHA256                 string                             `json:"run_plan_sha256,omitempty"`
	PerpsPlanOutcome              *paperstatus.PerpsPlanOutcome      `json:"perps_plan_outcome,omitempty"`
	QualificationInputSHA256      string                             `json:"qualification_input_sha256"`
	QualificationOutcome          string                             `json:"qualification_outcome"`
	QualificationTapes            uint64                             `json:"qualification_tapes"`
	QualificationFrames           uint64                             `json:"qualification_frames"`
	QualificationMinimumFrames    uint64                             `json:"qualification_minimum_frames"`
	QualificationTrainingFrames   uint64                             `json:"qualification_training_frames"`
	QualificationHoldoutFrames    uint64                             `json:"qualification_holdout_frames"`
	QualificationStrategy         string                             `json:"qualification_strategy,omitempty"`
	QualificationRiskProfile      string                             `json:"qualification_risk_profile,omitempty"`
	QualificationHoldoutEvaluated bool                               `json:"qualification_holdout_evaluated"`
	QualificationStressEvaluated  bool                               `json:"qualification_stress_evaluated"`
	QualificationHoldoutScored    bool                               `json:"qualification_holdout_scored"`
	QualificationStressScored     bool                               `json:"qualification_stress_scored"`
	QualificationHoldoutMicros    int64                              `json:"qualification_holdout_micros"`
	QualificationStressMicros     int64                              `json:"qualification_stress_micros"`
	QualificationAttempts         []paperstatus.QualificationAttempt `json:"qualification_attempts,omitempty"`
}

type perpsResearchSummary struct {
	Version       uint32                `json:"version"`
	PaperOnly     bool                  `json:"paper_only"`
	AdvisoryOnly  bool                  `json:"advisory_only"`
	Authorized    bool                  `json:"authorized"`
	Promotable    bool                  `json:"promotable"`
	ObservedAt    time.Time             `json:"observed_at"`
	Markets       []perpsResearchMarket `json:"markets"`
	ContentSHA256 string                `json:"content_sha256,omitempty"`
}

// RenderPerpsResearch returns a content-bound, read-only projection of one
// completed three-market paper qualification. It deliberately excludes account
// state, events, human text, paths, policies, and every execution capability.
func RenderPerpsResearch(paths map[string]string) ([]byte, error) {
	expected := [...]string{"SOL-PERP", "BTC-PERP", "ETH-PERP"}
	if len(paths) != len(expected) {
		return nil, errors.New("perps research requires exactly three paper status paths")
	}
	result := perpsResearchSummary{
		Version: 2, PaperOnly: true, AdvisoryOnly: true,
		Markets: make([]perpsResearchMarket, 0, len(expected)),
	}
	for _, market := range expected {
		path, ok := paths[market]
		if !ok || !cleanAbsolutePath(path) {
			return nil, errors.New("perps research paper status paths are invalid")
		}
		raw, err := securefile.ReadPrivate(path, maxPerpsResearchBytes)
		if err != nil {
			return nil, errors.New("read perps research paper status")
		}
		var snapshot paperstatus.Snapshot
		if strictjson.Decode(raw, &snapshot) != nil || paperstatus.ValidateSnapshot(snapshot) != nil ||
			snapshot.Summary == nil || snapshot.Summary.Market != market ||
			snapshot.Summary.Instrument != "perpetual" || !snapshot.Summary.QualificationTracked {
			return nil, errors.New("perps research paper status is invalid")
		}
		if result.ObservedAt.IsZero() {
			result.ObservedAt = snapshot.ObservedAt
		} else if !result.ObservedAt.Equal(snapshot.ObservedAt) {
			return nil, errors.New("perps research paper statuses are from different runs")
		}
		summary := snapshot.Summary
		sourceDigest := sha256.Sum256(raw)
		var outcome *paperstatus.PerpsPlanOutcome
		if summary.PerpsPlanOutcome != nil {
			copy := *summary.PerpsPlanOutcome
			outcome = &copy
		}
		result.Markets = append(result.Markets, perpsResearchMarket{
			Market:                        market,
			PaperStatusSHA256:             hex.EncodeToString(sourceDigest[:]),
			DecisionSource:                summary.DecisionSource,
			ProposalSource:                summary.ProposalSource,
			RunStrategy:                   summary.Strategy,
			RunRiskProfile:                summary.RiskProfile,
			RunPlanSHA256:                 summary.RunPlanSHA256,
			PerpsPlanOutcome:              outcome,
			QualificationInputSHA256:      summary.QualificationSHA256,
			QualificationOutcome:          summary.QualificationOutcome,
			QualificationTapes:            summary.QualificationTapes,
			QualificationFrames:           summary.QualificationFrames,
			QualificationMinimumFrames:    summary.QualificationMinimumFrames,
			QualificationTrainingFrames:   summary.QualificationTrainingFrames,
			QualificationHoldoutFrames:    summary.QualificationHoldoutFrames,
			QualificationStrategy:         summary.QualificationStrategy,
			QualificationRiskProfile:      summary.QualificationRiskProfile,
			QualificationHoldoutEvaluated: summary.QualificationHoldoutEvaluated,
			QualificationStressEvaluated:  summary.QualificationStressEvaluated,
			QualificationHoldoutScored:    summary.QualificationHoldoutScored,
			QualificationStressScored:     summary.QualificationStressScored,
			QualificationHoldoutMicros:    summary.QualificationHoldoutMicros,
			QualificationStressMicros:     summary.QualificationStressMicros,
			QualificationAttempts: append([]paperstatus.QualificationAttempt(nil),
				summary.QualificationAttempts...),
		})
	}
	digest, err := perpsResearchFingerprint(result)
	if err != nil {
		return nil, errors.New("encode perps research summary")
	}
	result.ContentSHA256 = digest
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, errors.New("encode perps research summary")
	}
	return append(encoded, '\n'), nil
}

func perpsResearchFingerprint(summary perpsResearchSummary) (string, error) {
	summary.ContentSHA256 = ""
	encoded, err := json.Marshal(summary)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
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
	evidence, err := readResearchEvidence(filepath.Join(filepath.Dir(path), "research-evidence.json"), packet)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errResearchEvidenceUnavailable, err)
	}
	status := packet.StatusAt(now)
	sources := make(map[string]struct{})
	singleSource, contradicted, unverified := 0, 0, 0
	for _, fact := range packet.VerifiedFacts {
		for _, source := range fact.Sources {
			sources[source.URL] = struct{}{}
		}
		switch fact.Status {
		case researchpacket.FactSingleSource:
			singleSource++
		case researchpacket.FactContradicted:
			contradicted++
		case researchpacket.FactUnverified:
			unverified++
		}
	}
	return &Research{
		HypothesisID: packet.HypothesisID, Market: packet.Market,
		Disposition: packet.Disposition, CreatedAt: packet.CreatedAt,
		ValidUntil: packet.ValidUntil, Current: status.Current,
		Actionable: status.Actionable, TwoSourceClaims: status.VerifiedFacts,
		RetrievedCitations: status.Sources, SourcesChecked: len(sources),
		OfficialPagesChecked:  evidence.OfficialPagesChecked,
		RetrievedPages:        uint64(len(evidence.RetrievedURLs)),
		ResearchSessions:      evidence.SessionCount,
		ResearchToolCalls:     append([]ResearchToolCount(nil), evidence.ToolCalls...),
		SuccessfulWebSearches: evidence.SuccessfulWebSearches,
		SingleSource:          singleSource, Contradicted: contradicted, Unverified: unverified,
		RiskDecision: packet.RiskVeto.Decision,
		RiskReason:   packet.RiskVeto.Reason,
		ProposedChanges: append([]researchpacket.ParameterChange(nil),
			packet.CandidateParameterDiff...),
		ContentSHA256: packet.ContentSHA256,
	}, nil
}

func readResearchEvidence(path string, packet researchpacket.Packet) (researchEvidence, error) {
	data, err := securefile.ReadPrivate(path, maxResearchEvidenceBytes)
	if err != nil {
		return researchEvidence{}, err
	}
	var evidence researchEvidence
	if strictjson.Decode(data, &evidence) != nil || evidence.Version != researchEvidenceVersion ||
		evidence.CreatedAt.IsZero() || !evidence.CreatedAt.Equal(evidence.CreatedAt.UTC()) ||
		!evidence.CreatedAt.Equal(packet.CreatedAt) || evidence.PacketSHA256 != packet.ContentSHA256 ||
		!validSHA256(evidence.SessionExportSHA256) || evidence.SessionCount == 0 ||
		evidence.SessionCount > 64 || len(evidence.ToolCalls) == 0 ||
		len(evidence.ToolCalls) > 128 || len(evidence.RetrievedURLs) == 0 ||
		len(evidence.RetrievedURLs) > 1_024 || evidence.OfficialPagesChecked > uint64(len(evidence.RetrievedURLs)) {
		return researchEvidence{}, errors.New("research session evidence is invalid")
	}
	seenTools := make(map[string]struct{}, len(evidence.ToolCalls))
	var totalCalls, webSearchCalls uint64
	for _, tool := range evidence.ToolCalls {
		if !validToolName(tool.Name) || tool.Count == 0 || tool.Count > 16_384 {
			return researchEvidence{}, errors.New("research tool count is invalid")
		}
		if _, duplicate := seenTools[tool.Name]; duplicate {
			return researchEvidence{}, errors.New("research tool count is duplicated")
		}
		seenTools[tool.Name] = struct{}{}
		if totalCalls > 16_384-tool.Count {
			return researchEvidence{}, errors.New("research tool count is excessive")
		}
		totalCalls += tool.Count
		if tool.Name == "web_search" {
			webSearchCalls = tool.Count
		}
	}
	if evidence.SuccessfulWebSearches > webSearchCalls {
		return researchEvidence{}, errors.New("successful web search count is invalid")
	}
	retrieved := make(map[string]struct{}, len(evidence.RetrievedURLs))
	for _, rawURL := range evidence.RetrievedURLs {
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil ||
			parsed.Fragment != "" || net.ParseIP(parsed.Hostname()) != nil || len(rawURL) > 2_048 {
			return researchEvidence{}, errors.New("retrieved research URL is invalid")
		}
		if _, duplicate := retrieved[rawURL]; duplicate {
			return researchEvidence{}, errors.New("retrieved research URL is duplicated")
		}
		retrieved[rawURL] = struct{}{}
	}
	cited := make(map[string]struct{})
	for _, fact := range packet.VerifiedFacts {
		for _, source := range fact.Sources {
			if _, ok := retrieved[source.URL]; !ok {
				return researchEvidence{}, errors.New("packet source lacks a successful retrieval")
			}
			cited[source.URL] = struct{}{}
		}
	}
	if evidence.OfficialPagesChecked != uint64(len(cited)) {
		return researchEvidence{}, errors.New("official page count does not match the packet")
	}
	return evidence, nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}

func validToolName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character != '_' && character != '-' && character != '.' && character != ':' &&
			(character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') {
			return false
		}
	}
	return true
}
