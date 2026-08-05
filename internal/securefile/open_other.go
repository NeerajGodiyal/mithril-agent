//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package securefile

import "os"

func openReadOnlyNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}
