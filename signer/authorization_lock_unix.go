//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package signer

import (
	"errors"
	"os"
	"syscall"
)

func acquireAuthorizationLock(path string) (*os.File, error) {
	lockPath := path + ".lock"
	fd, err := syscall.Open(
		lockPath,
		syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, errors.New("authorization ledger lock is invalid or unavailable")
	}
	file := os.NewFile(uintptr(fd), lockPath)
	fail := func(err error) (*os.File, error) {
		_ = file.Close()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 ||
		!ledgerOwnedByCurrentUser(info) {
		return fail(errors.New("authorization ledger lock is invalid or unavailable"))
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return fail(errors.New("authorization ledger is already in use"))
		}
		return fail(errors.New("authorization ledger lock is invalid or unavailable"))
	}
	return file, nil
}
