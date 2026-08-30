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
	reader *statuswire.Reader
}

func NewSocketReader(path string) (*SocketReader, error) {
	return newSocketReader(path, defaultTimeout, true)
}

func newSocketReader(
	path string, timeout time.Duration, requireRootedPath bool,
) (*SocketReader, error) {
	reader, err := statuswire.NewReaderWithTrust(
		path, maxSnapshotBytes, timeout, requireRootedPath, validateJSON,
	)
	if err != nil {
		return nil, err
	}
	return &SocketReader{reader: reader}, nil
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
	return snapshot, nil
}
