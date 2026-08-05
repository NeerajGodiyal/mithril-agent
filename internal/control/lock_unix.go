//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package control

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func withStateLock(path string, operation func() error) error {
	lockPath := path + ".lock"
	fd, err := unix.Open(
		lockPath,
		unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return errors.New("open control state lock")
	}
	file := os.NewFile(uintptr(fd), lockPath)
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("open control state lock")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("control state lock is unsafe")
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		return errors.New("lock control state")
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	return operation()
}
