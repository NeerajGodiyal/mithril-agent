//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package control

import "errors"

func withStateLock(string, func() error) error {
	return errors.New("control state locking is unsupported on this platform; use Linux or WSL2")
}
