// Package statussocket transports one validated operatorstatus snapshot over
// a fixed local Unix socket. The protocol has no request or path input.
package statussocket

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

const (
	maxSnapshotBytes = 32 << 10
	maxWireBytes     = maxSnapshotBytes + 1
	defaultTimeout   = 2 * time.Second
)

// SnapshotReader is the bridge's fixed status source.
type SnapshotReader interface {
	Read() (operatorstatus.Snapshot, error)
}

// CredentialReader reads the root-owned, read-only copy created for a service
// by systemd LoadCredential. The service receives no path to the original
// agent state and cannot use this reader to select another file.
type CredentialReader struct {
	path string
}

func NewCredentialReader(directory, name string) (*CredentialReader, error) {
	if !cleanSocketPath(directory) || filepath.Base(name) != name || name == "." || name == "" {
		return nil, errors.New("operator status credential location is invalid")
	}
	return &CredentialReader{path: filepath.Join(directory, name)}, nil
}

func (r *CredentialReader) Read() (operatorstatus.Snapshot, error) {
	if r == nil || !cleanSocketPath(r.path) {
		return operatorstatus.Snapshot{}, errors.New("operator status credential reader is invalid")
	}
	directory := filepath.Dir(r.path)
	if err := secureexec.ValidateProtectedDirectory(directory); err != nil {
		return operatorstatus.Snapshot{}, errors.New("operator status credential directory is unsafe")
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil || directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() ||
		!fileowner.RootOwned(directoryInfo) || directoryInfo.Mode().Perm()&0o022 != 0 {
		return operatorstatus.Snapshot{}, errors.New("operator status credential directory is unsafe")
	}
	before, err := os.Lstat(r.path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		!fileowner.RootOwned(before) || before.Mode().Perm()&0o222 != 0 ||
		before.Mode().Perm()&0o007 != 0 {
		return operatorstatus.Snapshot{}, errors.New("operator status credential is unsafe")
	}
	file, err := os.Open(r.path)
	if err != nil {
		return operatorstatus.Snapshot{}, errors.New("open operator status credential")
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return operatorstatus.Snapshot{}, errors.New("operator status credential changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSnapshotBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxSnapshotBytes {
		return operatorstatus.Snapshot{}, errors.New("read operator status credential")
	}
	final, err := file.Stat()
	if err != nil || final.Size() != after.Size() || final.Mode() != after.Mode() ||
		!final.ModTime().Equal(after.ModTime()) {
		return operatorstatus.Snapshot{}, errors.New("operator status credential changed while reading")
	}
	var snapshot operatorstatus.Snapshot
	if err := strictjson.Decode(data, &snapshot); err != nil {
		return operatorstatus.Snapshot{}, errors.New("decode operator status credential")
	}
	if err := operatorstatus.ValidateSnapshot(snapshot); err != nil {
		return operatorstatus.Snapshot{}, errors.New("validate operator status credential")
	}
	return snapshot, nil
}

// Reader implements the Telegram operator's read-only StatusReader shape.
type Reader struct {
	path              string
	timeout           time.Duration
	requireRootedPath bool
	dial              func(context.Context, string) (net.Conn, error)
}

func NewReader(path string) (*Reader, error) {
	return newReader(path, defaultTimeout, true)
}

func newReader(path string, timeout time.Duration, requireRootedPath bool) (*Reader, error) {
	if !cleanSocketPath(path) {
		return nil, errors.New("operator status socket must be a clean absolute path")
	}
	if timeout <= 0 || timeout > 5*time.Second {
		return nil, errors.New("operator status socket timeout is invalid")
	}
	reader := &Reader{path: path, timeout: timeout, requireRootedPath: requireRootedPath}
	reader.dial = func(ctx context.Context, path string) (net.Conn, error) {
		return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "unix", path)
	}
	return reader, nil
}

func (r *Reader) Read() (operatorstatus.Snapshot, error) {
	if r == nil || r.dial == nil || !cleanSocketPath(r.path) ||
		r.timeout <= 0 || r.timeout > 5*time.Second {
		return operatorstatus.Snapshot{}, errors.New("operator status socket reader is invalid")
	}
	if r.requireRootedPath {
		if err := validateRootOwnedAncestry(filepath.Dir(r.path)); err != nil {
			return operatorstatus.Snapshot{}, err
		}
	}
	before, err := validateSocket(r.path)
	if err != nil {
		return operatorstatus.Snapshot{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	connection, err := r.dial(ctx, r.path)
	if err != nil {
		return operatorstatus.Snapshot{}, errors.New("connect to operator status socket")
	}
	defer connection.Close()
	after, err := os.Lstat(r.path)
	if err != nil || !os.SameFile(before, after) {
		return operatorstatus.Snapshot{}, errors.New("operator status socket changed while connecting")
	}
	if err := connection.SetDeadline(time.Now().Add(r.timeout)); err != nil {
		return operatorstatus.Snapshot{}, errors.New("bound operator status socket deadline")
	}
	data, err := io.ReadAll(io.LimitReader(connection, maxWireBytes+1))
	if err != nil {
		return operatorstatus.Snapshot{}, errors.New("read operator status socket")
	}
	if len(data) == 0 || len(data) > maxWireBytes {
		return operatorstatus.Snapshot{}, errors.New("operator status socket response is invalid")
	}
	var snapshot operatorstatus.Snapshot
	if err := strictjson.Decode(data, &snapshot); err != nil {
		return operatorstatus.Snapshot{}, errors.New("decode operator status socket response")
	}
	if err := operatorstatus.ValidateSnapshot(snapshot); err != nil {
		return operatorstatus.Snapshot{}, errors.New("validate operator status socket response")
	}
	return snapshot, nil
}

// Serve accepts at most one connection and returns after serving it. The
// systemd socket remains active and starts a fresh, mount-isolated bridge for
// the next connection. Serve never reads client data, so the wire protocol
// cannot select a file or supply configuration, journal, key, or RPC input.
func Serve(ctx context.Context, listener net.Listener, reader SnapshotReader) error {
	if ctx == nil || listener == nil || reader == nil || listener.Addr() == nil ||
		listener.Addr().Network() != "unix" {
		return errors.New("activated Unix listener and fixed status reader are required")
	}
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-closed:
		}
	}()
	connection, err := listener.Accept()
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("accept operator status connection")
	}
	serveConnection(connection, reader)
	return nil
}

func serveConnection(connection net.Conn, reader SnapshotReader) {
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(defaultTimeout)); err != nil {
		return
	}
	snapshot, err := reader.Read()
	if err != nil || operatorstatus.ValidateSnapshot(snapshot) != nil {
		return
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) == 0 || len(encoded) > maxSnapshotBytes {
		return
	}
	encoded = append(encoded, '\n')
	for len(encoded) > 0 {
		written, err := connection.Write(encoded)
		if err != nil || written <= 0 {
			return
		}
		encoded = encoded[written:]
	}
}

func cleanSocketPath(path string) bool {
	return path != "" && path != string(filepath.Separator) &&
		filepath.IsAbs(path) && filepath.Clean(path) == path
}

func validateSocket(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("inspect operator status socket")
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 ||
		info.Mode().Perm()&0o007 != 0 {
		return nil, errors.New("operator status socket is not protected")
	}
	return info, nil
}

func validateRootOwnedAncestry(path string) error {
	for {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() ||
			!fileowner.RootOwned(info) || info.Mode().Perm()&0o022 != 0 {
			return errors.New("operator status socket directory is not protected")
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}
