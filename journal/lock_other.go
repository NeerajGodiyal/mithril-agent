//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package journal

import (
	"errors"
	"os"
)

func lockFile(*os.File) error {
	return errors.New("journal locking is unsupported on this platform; use Linux or WSL2")
}

func lockReadFile(*os.File) error {
	return errors.New("journal locking is unsupported on this platform; use Linux or WSL2")
}

func openFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}

func openReadFile(path string) (*os.File, error) {
	return os.Open(path)
}
