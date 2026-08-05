//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package signer

import "os"

func ledgerOwnedByCurrentUser(os.FileInfo) bool {
	return false
}
