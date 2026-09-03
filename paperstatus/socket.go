package paperstatus

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/statuswire"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

const defaultTimeout = statuswire.DefaultTimeout

type SnapshotReader interface {
	Read() (Snapshot, error)
}

type CredentialReader struct {
	reader *statuswire.CredentialReader
}

func NewCredentialReader(directory, name string) (*CredentialReader, error) {
	reader, err := statuswire.NewCredentialReader(
		directory, name, maxSnapshotBytes, validateJSON,
	)
	if err != nil {
		return nil, err
	}
	return &CredentialReader{reader: reader}, nil
}

func (r *CredentialReader) Read() (Snapshot, error) {
	if r == nil || r.reader == nil {
		return Snapshot{}, errors.New("paper status credential reader is invalid")
	}
	data, err := r.reader.ReadJSON()
	if err != nil {
		return Snapshot{}, err
	}
	return decode(data)
}

type SocketReader struct {
	reader   *statuswire.Reader
	sourceID string
	label    string
}

func NewSocketReader(path string) (*SocketReader, error) {
	return newSocketReader(path, "", defaultTimeout, true)
}

func NewLabeledSocketReader(path, label string) (*SocketReader, error) {
	if !validSourceLabel(label) {
		return nil, errors.New("paper status source label is invalid")
	}
	return newSocketReader(path, label, defaultTimeout, true)
}

func newSocketReader(
	path, label string, timeout time.Duration, requireRootedPath bool,
) (*SocketReader, error) {
	reader, err := statuswire.NewReaderWithTrust(
		path, maxSnapshotBytes, timeout, requireRootedPath, validateJSON,
	)
	if err != nil {
		return nil, err
	}
	return &SocketReader{reader: reader, sourceID: path, label: label}, nil
}

func (r *SocketReader) SourceLabel() string {
	if r == nil {
		return ""
	}
	return r.label
}

func validSourceLabel(label string) bool {
	if label == "" || len(label) > 32 {
		return false
	}
	for _, character := range label {
		if character != '/' && character != '-' && character != '_' &&
			(character < '0' || character > '9') &&
			(character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') {
			return false
		}
	}
	return true
}

// SourceID is the stable local endpoint identity used to deduplicate alerts
// when multiple paper sources are reordered. Telegram may show only a short
// digest-derived source tag; the path itself is never sent.
func (r *SocketReader) SourceID() string {
	if r == nil {
		return ""
	}
	return r.sourceID
}

func (r *SocketReader) Read() (Snapshot, error) {
	if r == nil || r.reader == nil {
		return Snapshot{}, errors.New("paper status socket reader is invalid")
	}
	data, err := r.reader.ReadJSON()
	if err != nil {
		return Snapshot{}, err
	}
	return decode(data)
}

func Serve(ctx context.Context, listener net.Listener, reader SnapshotReader) error {
	if reader == nil {
		return errors.New("fixed paper status reader is required")
	}
	return statuswire.Serve(ctx, listener, snapshotSource{reader}, maxSnapshotBytes)
}

type snapshotSource struct {
	reader SnapshotReader
}

func (s snapshotSource) ReadJSON() ([]byte, error) {
	snapshot, err := s.reader.Read()
	if err != nil || ValidateSnapshot(snapshot) != nil {
		return nil, errors.New("paper status snapshot is invalid")
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) == 0 || len(encoded) > maxSnapshotBytes {
		return nil, errors.New("encode paper status snapshot")
	}
	return encoded, nil
}

func validateJSON(data []byte) error {
	_, err := decode(data)
	return err
}

func decode(data []byte) (Snapshot, error) {
	var snapshot Snapshot
	if err := strictjson.Decode(data, &snapshot); err != nil || ValidateSnapshot(snapshot) != nil {
		return Snapshot{}, errors.New("paper status snapshot is invalid")
	}
	normalizeLegacySnapshot(&snapshot)
	return snapshot, nil
}
