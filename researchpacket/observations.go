package researchpacket

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// RecordedVersion distinguishes host-recorded observations from web-only packets.
const RecordedVersion = uint32(2)

// RecordedJournal binds observations to one exact completed journal stream.
type RecordedJournal struct {
	Day             string `json:"day"`
	Records         int    `json:"records"`
	ChainHeadSHA256 string `json:"chain_head_sha256"`
}

// ObservationMetrics are computed by the host from recorded paper ticks.
// Monetary values are millionths of USD, not real wallet results.
type ObservationMetrics struct {
	ObservableBPS     uint32 `json:"observable_bps"`
	Signals           uint64 `json:"signals"`
	Fills             uint64 `json:"fills"`
	VersusHoldMicros  int64  `json:"versus_hold_micros,string"`
	MaxDrawdownMicros uint64 `json:"max_drawdown_micros,string"`
}

// RecordedObservations retain retrospective paper measurements. The digest
// proves content identity, not provenance: callers must reconstruct it from
// their protected policy and verified journal before trusting its contents.
type RecordedObservations struct {
	Version         uint32             `json:"version"`
	Kind            string             `json:"kind"`
	PaperOnly       bool               `json:"paper_only"`
	AdvisoryOnly    bool               `json:"advisory_only"`
	Authorized      bool               `json:"authorized"`
	Promotable      bool               `json:"promotable"`
	Market          string             `json:"market"`
	PolicySHA256    string             `json:"policy_sha256"`
	ObservedFrom    time.Time          `json:"observed_from"`
	ObservedThrough time.Time          `json:"observed_through"`
	Journal         RecordedJournal    `json:"journal"`
	Metrics         ObservationMetrics `json:"metrics"`
	ContentSHA256   string             `json:"content_sha256,omitempty"`
}

// RecordedReference is the only recorded-evidence data Hermes may supply.
// Values, journal paths, observation windows and policies are host-owned.
type RecordedReference struct {
	ContentSHA256 string   `json:"content_sha256"`
	MetricIDs     []string `json:"metric_ids"`
}

func (observation RecordedObservations) validateEnvelope() error {
	day, err := time.Parse("2006-01-02", observation.Journal.Day)
	if err != nil || observation.Version != 1 || observation.Kind != "recorded_paper_observations" ||
		!observation.PaperOnly || !observation.AdvisoryOnly || observation.Authorized || observation.Promotable ||
		(observation.Market != "SOL/USDC" && observation.Market != "JUP/USDC") ||
		!observation.ObservedFrom.Equal(day) || !observation.ObservedThrough.Equal(day.Add(24*time.Hour-time.Nanosecond)) ||
		observation.ObservedFrom.Location() != time.UTC || observation.ObservedThrough.Location() != time.UTC ||
		!lowerSHA256(observation.PolicySHA256) || !lowerSHA256(observation.Journal.ChainHeadSHA256) ||
		observation.Journal.Records < 2 || observation.Metrics.ObservableBPS < 9_500 || observation.Metrics.ObservableBPS > 10_000 ||
		observation.Metrics.Signals > uint64(observation.Journal.Records) || observation.Metrics.Fills > uint64(observation.Journal.Records) {
		return errors.New("recorded paper observation envelope is invalid")
	}
	return nil
}

func (observation RecordedObservations) fingerprint() (string, error) {
	observation.ContentSHA256 = ""
	encoded, err := json.Marshal(observation)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// Seal assigns a deterministic content digest without adding a generation time.
func (observation RecordedObservations) Seal() (RecordedObservations, error) {
	if err := observation.validateEnvelope(); err != nil {
		return RecordedObservations{}, err
	}
	digest, err := observation.fingerprint()
	if err != nil {
		return RecordedObservations{}, err
	}
	observation.ContentSHA256 = digest
	return observation, nil
}

// Validate checks shape and content identity, not filesystem-backed provenance.
func (observation RecordedObservations) Validate() error {
	if err := observation.validateEnvelope(); err != nil {
		return err
	}
	digest, err := observation.fingerprint()
	if err != nil || observation.ContentSHA256 != digest {
		return errors.New("recorded paper observation digest differs")
	}
	return nil
}

// CurrentAt accepts only the previous complete UTC day. Recreating an artifact
// cannot renew its age, and a research run spanning midnight must start again.
func (observation RecordedObservations) CurrentAt(now time.Time) bool {
	return !now.IsZero() && observation.Validate() == nil && observation.ObservedFrom.Equal(now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1))
}

func (reference RecordedReference) validate(observation RecordedObservations) error {
	if !lowerSHA256(reference.ContentSHA256) || reference.ContentSHA256 != observation.ContentSHA256 ||
		len(reference.MetricIDs) == 0 || len(reference.MetricIDs) > 5 {
		return errors.New("recorded evidence reference is invalid")
	}
	seen := make(map[string]bool)
	for _, id := range reference.MetricIDs {
		switch id {
		case "observable_bps", "signals", "fills", "versus_hold_micros", "max_drawdown_micros":
		default:
			return errors.New("recorded evidence metric is unsupported")
		}
		if seen[id] {
			return errors.New("recorded evidence metric is duplicated")
		}
		seen[id] = true
	}
	return nil
}

func lowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
