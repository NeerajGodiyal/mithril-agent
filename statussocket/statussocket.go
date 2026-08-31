// Package statussocket transports one validated operator status snapshot over
// the shared bounded, request-free Unix-socket protocol.
package statussocket

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/internal/statuswire"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

const (
	maxSnapshotBytes = 32 << 10
	maxWireBytes     = maxSnapshotBytes + 1
	defaultTimeout   = statuswire.DefaultTimeout
)

type SnapshotReader interface {
	Read() (operatorstatus.Snapshot, error)
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

func (r *CredentialReader) Read() (operatorstatus.Snapshot, error) {
	if r == nil || r.reader == nil {
		return operatorstatus.Snapshot{}, errors.New("operator status credential reader is invalid")
	}
	data, err := r.reader.ReadJSON()
	if err != nil {
		return operatorstatus.Snapshot{}, err
	}
	return decode(data)
}

type Reader struct {
	reader *statuswire.Reader
}

func NewReader(path string) (*Reader, error) {
	return newReader(path, defaultTimeout, true)
}

func newReader(path string, timeout time.Duration, requireRootedPath bool) (*Reader, error) {
	reader, err := statuswire.NewReaderWithTrust(
		path, maxSnapshotBytes, timeout, requireRootedPath, validateJSON,
	)
	if err != nil {
		return nil, err
	}
	return &Reader{reader: reader}, nil
}

func (r *Reader) Read() (operatorstatus.Snapshot, error) {
	if r == nil || r.reader == nil {
		return operatorstatus.Snapshot{}, errors.New("operator status socket reader is invalid")
	}
	data, err := r.reader.ReadJSON()
	if err != nil {
		return operatorstatus.Snapshot{}, err
	}
	return decode(data)
}

func Serve(ctx context.Context, listener net.Listener, reader SnapshotReader) error {
	if reader == nil {
		return errors.New("fixed operator status reader is required")
	}
	return statuswire.Serve(ctx, listener, snapshotSource{reader}, maxSnapshotBytes)
}

func serveConnection(connection net.Conn, reader SnapshotReader) {
	if reader == nil {
		_ = connection.Close()
		return
	}
	statuswire.ServeConnection(connection, snapshotSource{reader}, maxSnapshotBytes)
}

type snapshotSource struct {
	reader SnapshotReader
}

func (s snapshotSource) ReadJSON() ([]byte, error) {
	snapshot, err := s.reader.Read()
	if err != nil || operatorstatus.ValidateSnapshot(snapshot) != nil {
		return nil, errors.New("operator status snapshot is invalid")
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) == 0 || len(encoded) > maxSnapshotBytes {
		return nil, errors.New("encode operator status snapshot")
	}
	return encoded, nil
}

func validateJSON(data []byte) error {
	_, err := decode(data)
	return err
}

func decode(data []byte) (operatorstatus.Snapshot, error) {
	var snapshot operatorstatus.Snapshot
	if err := strictjson.Decode(data, &snapshot); err != nil ||
		operatorstatus.ValidateSnapshot(snapshot) != nil {
		return operatorstatus.Snapshot{}, errors.New("operator status snapshot is invalid")
	}
	return snapshot, nil
}
