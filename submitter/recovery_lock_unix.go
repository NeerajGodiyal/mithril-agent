//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package submitter

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"golang.org/x/sys/unix"
)

func withRecoveryLock(policy Policy, operation func() error) error {
	path := recoveryPath(policy) + ".lock"
	if secureexec.ValidateProtectedDirectory(filepath.Dir(path)) != nil {
		return errors.New("submission recovery lock directory is unsafe")
	}
	fd, err := unix.Open(
		path,
		unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return errors.New("open submission recovery lock")
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("open submission recovery lock")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 ||
		!fileowner.Trusted(info) {
		return errors.New("submission recovery lock is unsafe")
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		return errors.New("lock submission recovery evidence")
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	return operation()
}
