package paperdashboard

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

const mithrilEvidenceVersion = uint32(1)

type MithrilEvidence struct {
	Version             uint32    `json:"version"`
	CheckedAt           time.Time `json:"checked_at"`
	AvailableAtCheck    bool      `json:"available_at_check"`
	MaxRecordAgeSeconds uint64    `json:"max_record_age_seconds"`
}

func (s *Server) EnableMithrilEvidence(path string) error {
	if !cleanAbsolutePath(path) {
		return errors.New("Mithril evidence status path must be a clean absolute path")
	}
	s.mithrilEvidencePath = path
	return nil
}

func RecordMithrilEvidence(path string, available bool, now time.Time) error {
	if !cleanAbsolutePath(path) || now.IsZero() {
		return errors.New("Mithril evidence status path and time are required")
	}
	status := MithrilEvidence{
		Version: mithrilEvidenceVersion, CheckedAt: now.UTC(),
		AvailableAtCheck: available, MaxRecordAgeSeconds: 900,
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return securefile.ReplacePrivate(path, append(encoded, '\n'), 2048)
}

func readMithrilEvidence(path string, now time.Time) (*MithrilEvidence, error) {
	if !cleanAbsolutePath(path) {
		return nil, errors.New("Mithril evidence status path must be a clean absolute path")
	}
	data, err := securefile.ReadPrivate(path, 2048)
	if err != nil {
		return nil, err
	}
	var status MithrilEvidence
	if err := strictjson.Decode(data, &status); err != nil || status.Version != mithrilEvidenceVersion ||
		status.CheckedAt.IsZero() || status.CheckedAt.Location() != time.UTC ||
		status.CheckedAt.After(now.UTC().Add(2*time.Minute)) || status.MaxRecordAgeSeconds != 900 {
		return nil, errors.New("Mithril evidence status is invalid")
	}
	return &status, nil
}
