//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package submitter

import "errors"

func withRecoveryLock(Policy, func() error) error {
	return errors.New("submission recovery locking is unsupported on this platform")
}
