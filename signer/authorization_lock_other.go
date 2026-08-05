//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package signer

import (
	"errors"
	"os"
)

func acquireAuthorizationLock(string) (*os.File, error) {
	return nil, errors.New("authorization ledger locking is unsupported on this platform")
}
