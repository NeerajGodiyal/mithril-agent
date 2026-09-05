// Package researchpacket validates the bounded, source-cited output of the
// isolated Hermes research process. A packet is advisory evidence only: it has
// no field for a key, wallet, transaction, service, or executable command.
package researchpacket

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

const (
	Version  = uint32(1)
	MaxBytes = 64 << 10
)

var acceptedSourceOwners = map[string]bool{
	"circle.com":    true,
	"coinbase.com":  true,
	"github.com":    true,
	"helius.dev":    true,
	"jito.wtf":      true,
	"jup.ag":        true,
	"kraken.com":    true,
	"pyth.network":  true,
	"solana.com":    true,
	"statuspage.io": true,
}

const (
	DispositionCandidate = "candidate"
	DispositionNoChange  = "no_change"
	DispositionBlocked   = "blocked"

	FactVerified     = "verified"
	FactSingleSource = "single_source"
	FactContradicted = "contradicted"
	FactUnverified   = "unverified"

	VetoPass   = "pass"
	VetoReject = "reject"
)

type Source struct {
	URL         string    `json:"url"`
	RetrievedAt time.Time `json:"retrieved_at"`
	PublishedAt time.Time `json:"published_at,omitzero"`
}

type Fact struct {
	ID      string   `json:"id"`
	Claim   string   `json:"claim"`
	Status  string   `json:"status"`
	Sources []Source `json:"sources"`
}

type RiskVeto struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

type ParameterChange struct {
	Name     string `json:"name"`
	Current  uint64 `json:"current"`
	Proposed uint64 `json:"proposed"`
}

type Packet struct {
	Version                uint32                `json:"version"`
	HypothesisID           string                `json:"hypothesis_id"`
	CreatedAt              time.Time             `json:"created_at"`
	ValidUntil             time.Time             `json:"valid_until"`
	Market                 string                `json:"market"`
	Disposition            string                `json:"disposition"`
	VerifiedFacts          []Fact                `json:"verified_facts"`
	BullCase               string                `json:"bull_case"`
	BearCase               string                `json:"bear_case"`
	NoTradeCase            string                `json:"no_trade_case"`
	ExecutionCostCase      string                `json:"execution_cost_case"`
	RiskVeto               RiskVeto              `json:"risk_veto"`
	CandidateParameterDiff []ParameterChange     `json:"candidate_parameter_diff"`
	RejectionConditions    []string              `json:"rejection_conditions"`
	OutOfSampleTest        string                `json:"out_of_sample_test"`
	RecordedEvidence       *RecordedReference    `json:"recorded_evidence,omitempty"`
	RecordedObservations   *RecordedObservations `json:"recorded_observations,omitempty"`
	ContentSHA256          string                `json:"content_sha256,omitempty"`
}

type Status struct {
	Current       bool
	Actionable    bool
	VerifiedFacts int
	Sources       int
}

// Parse accepts one strict JSON object from Hermes and adds the content hash.
func Parse(data []byte, now time.Time) (Packet, error) {
	return ParseWithRecorded(data, nil, now)
}

// ParseWithRecorded binds a host-reconstructed observation artifact for a v2
// packet. It never accepts a model-supplied artifact. Web-only v1 stays unchanged.
func ParseWithRecorded(data []byte, observation *RecordedObservations, now time.Time) (Packet, error) {
	if len(data) == 0 || len(data) > MaxBytes {
		return Packet{}, errors.New("research packet size is invalid")
	}
	var packet Packet
	if err := strictjson.Decode(data, &packet); err != nil {
		return Packet{}, errors.New("research packet JSON is invalid")
	}
	if packet.ContentSHA256 != "" || packet.RecordedObservations != nil {
		return Packet{}, errors.New("research packet digest and recorded observations must be assigned by mithril-agent")
	}
	if packet.Version == RecordedVersion {
		if observation == nil || !observation.CurrentAt(now) {
			return Packet{}, errors.New("research packet needs current host-recorded observations")
		}
		bound := *observation
		packet.RecordedObservations = &bound
		if packet.VerifiedFacts == nil {
			packet.VerifiedFacts = []Fact{}
		}
	}
	if err := packet.validate(now.UTC(), true); err != nil {
		return Packet{}, err
	}
	var err error
	packet.ContentSHA256, err = packet.fingerprint()
	return packet, err
}

// DecodeStored validates a previously accepted packet and its content hash.
func DecodeStored(data []byte) (Packet, error) {
	if len(data) == 0 || len(data) > MaxBytes {
		return Packet{}, errors.New("stored research packet size is invalid")
	}
	var packet Packet
	if err := strictjson.Decode(data, &packet); err != nil || packet.Validate() != nil {
		return Packet{}, errors.New("stored research packet is invalid")
	}
	return packet, nil
}

func (packet Packet) Validate() error {
	if err := packet.validate(time.Time{}, false); err != nil {
		return err
	}
	want, err := packet.fingerprint()
	if err != nil || want != packet.ContentSHA256 {
		return errors.New("research packet digest does not match")
	}
	return nil
}

func (packet Packet) StatusAt(now time.Time) Status {
	status := Status{}
	if packet.Validate() != nil {
		return status
	}
	for _, fact := range packet.VerifiedFacts {
		if fact.Status == FactVerified {
			status.VerifiedFacts++
			status.Sources += len(fact.Sources)
		}
	}
	status.Current = !now.UTC().Before(packet.CreatedAt) && now.UTC().Before(packet.ValidUntil)
	if packet.RecordedObservations != nil {
		status.Current = status.Current && packet.RecordedObservations.CurrentAt(now)
	}
	status.Actionable = status.Current && packet.Disposition == DispositionCandidate &&
		packet.RiskVeto.Decision == VetoPass && len(packet.CandidateParameterDiff) != 0
	return status
}

func (packet Packet) validate(now time.Time, fresh bool) error {
	if (packet.Version != Version && packet.Version != RecordedVersion) || !safeID(packet.HypothesisID) ||
		packet.CreatedAt.IsZero() || packet.ValidUntil.IsZero() ||
		!packet.CreatedAt.Equal(packet.CreatedAt.UTC()) ||
		!packet.ValidUntil.Equal(packet.ValidUntil.UTC()) ||
		!packet.ValidUntil.After(packet.CreatedAt) ||
		packet.ValidUntil.Sub(packet.CreatedAt) > 12*time.Hour ||
		!validMarket(packet.Market) || len(packet.VerifiedFacts) > 12 ||
		len(packet.CandidateParameterDiff) > 8 ||
		len(packet.RejectionConditions) == 0 || len(packet.RejectionConditions) > 12 ||
		!boundedText(packet.BullCase, 2_000) || !boundedText(packet.BearCase, 2_000) ||
		!boundedText(packet.NoTradeCase, 2_000) ||
		!boundedText(packet.ExecutionCostCase, 2_000) ||
		!boundedText(packet.OutOfSampleTest, 2_000) ||
		!boundedText(packet.RiskVeto.Reason, 1_000) {
		return errors.New("research packet envelope is invalid")
	}
	if fresh && (packet.CreatedAt.Before(now.Add(-20*time.Minute)) ||
		packet.CreatedAt.After(now.Add(2*time.Minute)) || !now.Before(packet.ValidUntil)) {
		return errors.New("research packet creation time is not current")
	}
	if packet.Version == Version {
		if packet.RecordedEvidence != nil || packet.RecordedObservations != nil {
			return errors.New("web-only research packet contains recorded evidence")
		}
	} else if packet.RecordedEvidence == nil || packet.RecordedObservations == nil ||
		!packet.RecordedObservations.CurrentAt(packet.CreatedAt) || packet.RecordedObservations.Market != packet.Market ||
		packet.RecordedEvidence.validate(*packet.RecordedObservations) != nil {
		return errors.New("research packet recorded evidence binding is invalid")
	}
	for _, condition := range packet.RejectionConditions {
		if !boundedText(condition, 600) {
			return errors.New("research packet rejection condition is invalid")
		}
	}
	allVerified := len(packet.VerifiedFacts) != 0 || packet.Version == RecordedVersion
	seenFacts := make(map[string]struct{}, len(packet.VerifiedFacts))
	latestSourceTime := packet.ValidUntil
	if fresh && now.Add(2*time.Minute).Before(latestSourceTime) {
		latestSourceTime = now.Add(2 * time.Minute)
	}
	for _, fact := range packet.VerifiedFacts {
		if !safeID(fact.ID) || !boundedText(fact.Claim, 800) || len(fact.Sources) > 4 {
			return errors.New("research packet fact is invalid")
		}
		if _, duplicate := seenFacts[fact.ID]; duplicate {
			return errors.New("research packet fact is duplicated")
		}
		seenFacts[fact.ID] = struct{}{}
		if err := validateFact(fact, packet.CreatedAt, latestSourceTime); err != nil {
			return err
		}
		allVerified = allVerified && fact.Status == FactVerified
	}
	if err := validateChanges(packet.CandidateParameterDiff); err != nil {
		return err
	}
	switch packet.Disposition {
	case DispositionCandidate:
		if packet.RiskVeto.Decision != VetoPass || len(packet.CandidateParameterDiff) == 0 ||
			!allVerified {
			return errors.New("research candidate lacks verified evidence or a Hermes-reported pass")
		}
	case DispositionNoChange, DispositionBlocked:
		if packet.RiskVeto.Decision != VetoReject || len(packet.CandidateParameterDiff) != 0 {
			return errors.New("blocked research packet cannot change parameters")
		}
	default:
		return errors.New("research packet disposition is invalid")
	}
	return nil
}

func validateFact(fact Fact, createdAt, latestSourceTime time.Time) error {
	switch fact.Status {
	case FactVerified:
		if len(fact.Sources) < 2 {
			return errors.New("verified research fact needs two sources")
		}
	case FactSingleSource:
		if len(fact.Sources) != 1 {
			return errors.New("single-source research fact needs one source")
		}
	case FactContradicted:
		if len(fact.Sources) < 2 {
			return errors.New("contradicted research fact needs two sources")
		}
	case FactUnverified:
		if len(fact.Sources) != 0 {
			return errors.New("unverified research fact cannot cite accepted sources")
		}
	default:
		return errors.New("research fact status is invalid")
	}
	owners := make(map[string]struct{}, len(fact.Sources))
	for _, source := range fact.Sources {
		owner, err := validateSource(source, createdAt, latestSourceTime)
		if err != nil {
			return err
		}
		owners[owner] = struct{}{}
	}
	if (fact.Status == FactVerified || fact.Status == FactContradicted) && len(owners) < 2 {
		return errors.New("research fact sources are not independent")
	}
	return nil
}

func validateSource(source Source, createdAt, latestSourceTime time.Time) (string, error) {
	parsed, err := url.Parse(source.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Fragment != "" || net.ParseIP(parsed.Hostname()) != nil {
		return "", errors.New("research source URL is invalid")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".local") ||
		source.RetrievedAt.IsZero() || !source.RetrievedAt.Equal(source.RetrievedAt.UTC()) ||
		source.RetrievedAt.Before(createdAt.Add(-12*time.Hour)) ||
		source.RetrievedAt.After(latestSourceTime) ||
		!source.PublishedAt.IsZero() && (!source.PublishedAt.Equal(source.PublishedAt.UTC()) ||
			source.PublishedAt.After(source.RetrievedAt.Add(2*time.Minute))) {
		return "", errors.New("research source timing or host is invalid")
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return "", errors.New("research source host is invalid")
	}
	owner := strings.Join(labels[len(labels)-2:], ".")
	if !acceptedSourceOwners[owner] {
		return "", errors.New("research source is outside the reviewed primary-source roster")
	}
	if owner == "github.com" {
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) < 2 || !strings.EqualFold(parts[0], "anza-xyz") ||
			!strings.EqualFold(parts[1], "agave") {
			return "", errors.New("research GitHub source is outside the reviewed repository")
		}
	}
	if owner == "statuspage.io" && host != "helius.statuspage.io" {
		return "", errors.New("research status source is outside the reviewed provider")
	}
	return owner, nil
}

func validateChanges(changes []ParameterChange) error {
	limits := map[string][2]uint64{
		"fast_window":        {2, 120},
		"slow_window":        {3, 240},
		"minimum_signal_bps": {1, 5_000},
		"cooldown_seconds":   {0, 86_400},
	}
	seen := make(map[string]struct{}, len(changes))
	proposed := make(map[string]uint64, len(changes))
	for _, change := range changes {
		limit, ok := limits[change.Name]
		if !ok || change.Current == change.Proposed || change.Proposed < limit[0] ||
			change.Proposed > limit[1] {
			return errors.New("research parameter change is invalid")
		}
		if _, duplicate := seen[change.Name]; duplicate {
			return errors.New("research parameter change is duplicated")
		}
		seen[change.Name] = struct{}{}
		proposed[change.Name] = change.Proposed
	}
	if fast, fastOK := proposed["fast_window"]; fastOK {
		if slow, slowOK := proposed["slow_window"]; slowOK && fast >= slow {
			return errors.New("research price windows are invalid")
		}
	}
	return nil
}

func validMarket(value string) bool {
	if value == "all" {
		return true
	}
	base, ok := strings.CutSuffix(value, "/USDC")
	if !ok || len(base) < 2 || len(base) > 12 {
		return false
	}
	for _, character := range base {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func safeID(value string) bool {
	if len(value) < 3 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character != '-' && character != '_' && (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func boundedText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 && character != '\n' && character != '\t' || character == 0x7f {
			return false
		}
	}
	return true
}

func (packet Packet) fingerprint() (string, error) {
	packet.ContentSHA256 = ""
	encoded, err := json.Marshal(packet)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}
