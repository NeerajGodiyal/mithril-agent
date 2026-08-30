//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"golang.org/x/sys/unix"
)

func withShadowLifecycleLock(path string, operation func() error) error {
	if !absoluteClean(path) || operation == nil {
		return errors.New("paper lifecycle lock path and operation are required")
	}
	if err := secureexec.ValidateProtectedDirectory(filepath.Dir(path)); err != nil {
		return errors.New("paper lifecycle lock directory is not trusted")
	}
	fd, err := unix.Open(
		path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600,
	)
	if err != nil {
		return errors.New("could not open the paper lifecycle lock")
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("could not open the paper lifecycle lock")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !fileowner.Trusted(info) ||
		info.Mode().Perm()&0o077 != 0 {
		return errors.New("paper lifecycle lock is unsafe")
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		return errors.New("could not lock the paper lifecycle")
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	return operation()
}
