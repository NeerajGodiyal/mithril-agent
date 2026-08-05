//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package fileowner

import "os"

func Trusted(os.FileInfo) bool {
	return false
}

func TrustedGroup(os.FileInfo) bool {
	return false
}

func RootOwned(os.FileInfo) bool {
	return false
}
