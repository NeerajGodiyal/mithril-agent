//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package main

import "errors"

func withShadowLifecycleLock(string, func() error) error {
	return errors.New("paper lifecycle locking requires Linux, macOS, or BSD")
}
