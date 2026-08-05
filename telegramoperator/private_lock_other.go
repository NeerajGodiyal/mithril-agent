//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package telegramoperator

import "os"

func acquirePrivateFileLock(string) (*os.File, error) {
	return nil, errPrivateLockUnavailable
}
