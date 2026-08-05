//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package telegramoperator

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"golang.org/x/sys/unix"
)

func acquirePrivateFileLock(path string) (*os.File, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		secureexec.ValidateProtectedDirectory(filepath.Dir(path)) != nil {
		return nil, errPrivateLockUnavailable
	}
	fd, err := unix.Open(
		path,
		unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, errPrivateLockUnavailable
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errPrivateLockUnavailable
	}
	fail := func(err error) (*os.File, error) {
		_ = file.Close()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 ||
		!fileowner.Trusted(info) {
		return fail(errPrivateLockUnavailable)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return fail(errPrivateLockHeld)
		}
		return fail(errPrivateLockUnavailable)
	}
	return file, nil
}
