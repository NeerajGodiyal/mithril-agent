//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package jupiterquote

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"golang.org/x/sys/unix"
)

type fileRequestGate struct {
	path    string
	spacing time.Duration
}

func newFileRequestGate(path string, spacing time.Duration) requestGate {
	return &fileRequestGate{path: path, spacing: spacing}
}

func (gate *fileRequestGate) Wait(ctx context.Context) error {
	if gate == nil || gate.path == "" || gate.path == string(filepath.Separator) ||
		!filepath.IsAbs(gate.path) || filepath.Clean(gate.path) != gate.path || gate.spacing <= 0 {
		return errors.New("Jupiter rate state path is invalid")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	fd, err := unix.Open(gate.path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return errors.New("open Jupiter rate state")
	}
	file := os.NewFile(uintptr(fd), gate.path)
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("open Jupiter rate state")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !fileowner.Trusted(info) {
		return errors.New("Jupiter rate state is unsafe")
	}
	if err := lockRequestState(ctx, fd); err != nil {
		return err
	}
	defer unix.Flock(fd, unix.LOCK_UN)

	var encoded [8]byte
	n, err := file.ReadAt(encoded[:], 0)
	if err != nil && !errors.Is(err, io.EOF) || n != 0 && n != len(encoded) {
		return errors.New("Jupiter rate state is invalid")
	}
	if n == len(encoded) {
		last := int64(binary.BigEndian.Uint64(encoded[:]))
		now := time.Now()
		if last < 0 || last > now.Add(10*time.Second).UnixNano() {
			return errors.New("Jupiter rate state time is invalid")
		}
		if wait := time.Until(time.Unix(0, last).Add(gate.spacing)); wait > 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	binary.BigEndian.PutUint64(encoded[:], uint64(time.Now().UnixNano()))
	if _, err := file.WriteAt(encoded[:], 0); err != nil {
		return errors.New("write Jupiter rate state")
	}
	if err := file.Truncate(int64(len(encoded))); err != nil || file.Sync() != nil {
		return errors.New("persist Jupiter rate state")
	}
	return nil
}

func lockRequestState(ctx context.Context, fd int) error {
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return errors.New("lock Jupiter rate state")
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
