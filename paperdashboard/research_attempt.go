package paperdashboard

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

// ResearchAttempt is a host observation of the research service, not a model result.
type ResearchAttempt struct {
	Version      uint32     `json:"version"`
	CheckedAt    time.Time  `json:"checked_at"`
	State        string     `json:"state"`
	InvocationID string     `json:"invocation_id,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

// EnableResearchAttempt enables the separately delivered host status projection.
func (s *Server) EnableResearchAttempt(path string) error {
	if !cleanAbsolutePath(path) {
		return errors.New("research attempt status path must be a clean absolute path")
	}
	s.researchAttemptPath = path
	return nil
}

// RecordResearchAttempt atomically writes a bounded, private host observation.
func RecordResearchAttempt(path string, status ResearchAttempt) error {
	if !cleanAbsolutePath(path) || !validResearchAttempt(status) {
		return errors.New("research attempt status is invalid")
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return securefile.ReplacePrivate(path, append(encoded, '\n'), 2048)
}

func validResearchAttempt(status ResearchAttempt) bool {
	if status.Version != 1 || status.CheckedAt.IsZero() || status.CheckedAt.Location() != time.UTC {
		return false
	}
	if status.InvocationID != "" {
		if len(status.InvocationID) != 32 || strings.ToLower(status.InvocationID) != status.InvocationID {
			return false
		}
		if _, err := hex.DecodeString(status.InvocationID); err != nil {
			return false
		}
	}
	for _, at := range []*time.Time{status.StartedAt, status.FinishedAt} {
		if at != nil && (at.IsZero() || at.Location() != time.UTC || at.After(status.CheckedAt.Add(2*time.Second))) {
			return false
		}
	}
	if status.StartedAt != nil && status.FinishedAt != nil && status.FinishedAt.Before(*status.StartedAt) {
		return false
	}
	switch status.State {
	case "preparing":
		return status.InvocationID != "" && status.StartedAt == nil && status.FinishedAt == nil
	case "running":
		return status.InvocationID != "" && status.StartedAt != nil && status.FinishedAt == nil
	case "finishing":
		return status.InvocationID != ""
	case "completed":
		return status.InvocationID != "" && status.StartedAt != nil && status.FinishedAt != nil
	case "failed":
		return status.InvocationID != "" || (status.StartedAt == nil && status.FinishedAt == nil)
	case "idle", "unknown":
		return status.InvocationID == "" && status.StartedAt == nil && status.FinishedAt == nil
	default:
		return false
	}
}

func readResearchAttempt(path string, now time.Time) (*ResearchAttempt, error) {
	if !cleanAbsolutePath(path) {
		return nil, errors.New("research attempt status path must be a clean absolute path")
	}
	data, err := securefile.ReadPrivate(path, 2048)
	if err != nil {
		return nil, err
	}
	var status ResearchAttempt
	if err := strictjson.Decode(data, &status); err != nil || !validResearchAttempt(status) || now.IsZero() ||
		status.CheckedAt.After(now.Add(2*time.Second)) || now.Sub(status.CheckedAt) > 90*time.Second {
		return nil, errors.New("research attempt status is invalid or stale")
	}
	return &status, nil
}
