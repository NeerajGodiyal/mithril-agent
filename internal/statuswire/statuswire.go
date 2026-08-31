// Package statuswire transports one bounded, validated JSON snapshot over a
// protected local Unix socket. The protocol has no request or path input.
package statuswire

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
)

const DefaultTimeout = 2 * time.Second

type Validator func([]byte) error

type CredentialReader struct {
	path     string
	maxBytes int64
	validate Validator
}

func NewCredentialReader(
	directory, name string, maxBytes int64, validate Validator,
) (*CredentialReader, error) {
	if !cleanPath(directory) || filepath.Base(name) != name || name == "." || name == "" ||
		maxBytes <= 0 || validate == nil {
		return nil, errors.New("status credential location is invalid")
	}
	return &CredentialReader{
		path: filepath.Join(directory, name), maxBytes: maxBytes, validate: validate,
	}, nil
}

func (r *CredentialReader) ReadJSON() ([]byte, error) {
	if r == nil || !cleanPath(r.path) || r.maxBytes <= 0 || r.validate == nil {
		return nil, errors.New("status credential reader is invalid")
	}
	directory := filepath.Dir(r.path)
	if err := secureexec.ValidateProtectedDirectory(directory); err != nil {
		return nil, errors.New("status credential directory is unsafe")
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil || directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() ||
		!fileowner.Trusted(directoryInfo) || directoryInfo.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("status credential directory is unsafe")
	}
	before, err := os.Lstat(r.path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		!fileowner.Trusted(before) || before.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("status credential is unsafe")
	}
	file, err := os.Open(r.path)
	if err != nil {
		return nil, errors.New("open status credential")
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.New("status credential changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, r.maxBytes+1))
	if err != nil || len(data) == 0 || int64(len(data)) > r.maxBytes {
		return nil, errors.New("read status credential")
	}
	final, err := file.Stat()
	if err != nil || final.Size() != after.Size() || final.Mode() != after.Mode() ||
		!final.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("status credential changed while reading")
	}
	if err := r.validate(data); err != nil {
		return nil, errors.New("validate status credential")
	}
	return data, nil
}

type Reader struct {
	path              string
	maxBytes          int64
	timeout           time.Duration
	requireRootedPath bool
	validate          Validator
	dial              func(context.Context, string) (net.Conn, error)
}

func NewReader(path string, maxBytes int64, validate Validator) (*Reader, error) {
	return NewReaderWithTrust(path, maxBytes, DefaultTimeout, true, validate)
}

// NewReaderWithTrust exists for package tests that create sockets below a
// temporary user-owned directory. Production callers must use NewReader.
func NewReaderWithTrust(
	path string, maxBytes int64, timeout time.Duration, requireRootedPath bool, validate Validator,
) (*Reader, error) {
	if !cleanPath(path) || maxBytes <= 0 || timeout <= 0 || timeout > 5*time.Second || validate == nil {
		return nil, errors.New("status socket is invalid")
	}
	reader := &Reader{
		path: path, maxBytes: maxBytes, timeout: timeout,
		requireRootedPath: requireRootedPath, validate: validate,
	}
	reader.dial = func(ctx context.Context, path string) (net.Conn, error) {
		return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "unix", path)
	}
	return reader, nil
}

func (r *Reader) ReadJSON() ([]byte, error) {
	if r == nil || r.dial == nil || !cleanPath(r.path) || r.maxBytes <= 0 ||
		r.timeout <= 0 || r.timeout > 5*time.Second || r.validate == nil {
		return nil, errors.New("status socket reader is invalid")
	}
	if r.requireRootedPath {
		if err := validateRootOwnedAncestry(filepath.Dir(r.path)); err != nil {
			return nil, err
		}
	}
	before, err := validateSocket(r.path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	connection, err := r.dial(ctx, r.path)
	if err != nil {
		return nil, errors.New("connect to status socket")
	}
	defer connection.Close()
	after, err := os.Lstat(r.path)
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.New("status socket changed while connecting")
	}
	if err := connection.SetDeadline(time.Now().Add(r.timeout)); err != nil {
		return nil, errors.New("bound status socket deadline")
	}
	data, err := io.ReadAll(io.LimitReader(connection, r.maxBytes+2))
	if err != nil || len(data) == 0 || int64(len(data)) > r.maxBytes+1 {
		return nil, errors.New("read status socket")
	}
	if err := r.validate(data); err != nil {
		return nil, errors.New("validate status socket response")
	}
	return data, nil
}

type Source interface {
	ReadJSON() ([]byte, error)
}

// Serve accepts at most one connection and returns after serving it.
func Serve(ctx context.Context, listener net.Listener, source Source, maxBytes int64) error {
	if ctx == nil || listener == nil || source == nil || maxBytes <= 0 ||
		listener.Addr() == nil || listener.Addr().Network() != "unix" {
		return errors.New("activated Unix listener and fixed status source are required")
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
		return errors.New("accept status connection")
	}
	ServeConnection(connection, source, maxBytes)
	return nil
}

func ServeConnection(connection net.Conn, source Source, maxBytes int64) {
	defer connection.Close()
	if connection == nil || source == nil || maxBytes <= 0 ||
		connection.SetDeadline(time.Now().Add(DefaultTimeout)) != nil {
		return
	}
	data, err := source.ReadJSON()
	if err != nil || len(data) == 0 || int64(len(data)) > maxBytes {
		return
	}
	if data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	for len(data) > 0 {
		written, err := connection.Write(data)
		if err != nil || written <= 0 {
			return
		}
		data = data[written:]
	}
}

func cleanPath(path string) bool {
	return path != "" && path != string(filepath.Separator) &&
		filepath.IsAbs(path) && filepath.Clean(path) == path
}

func validateSocket(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 ||
		info.Mode().Perm()&0o007 != 0 {
		return nil, errors.New("status socket is not protected")
	}
	return info, nil
}

func validateRootOwnedAncestry(path string) error {
	for {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() ||
			!fileowner.RootOwned(info) || info.Mode().Perm()&0o022 != 0 {
			return errors.New("status socket directory is not protected")
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}
