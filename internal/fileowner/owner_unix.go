//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package fileowner

import (
	"os"
	"syscall"
)

// Trusted reports whether a file is controlled by this process or root.
func Trusted(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && (stat.Uid == 0 || stat.Uid == uint32(os.Geteuid()))
}

// TrustedGroup reports whether a file is controlled by one of this process's
// groups. Callers must still enforce the exact group permission bits they need.
func TrustedGroup(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	groups, err := os.Getgroups()
	if err != nil {
		return false
	}
	groups = append(groups, os.Getegid())
	for _, group := range groups {
		if stat.Gid == uint32(group) {
			return true
		}
	}
	return false
}

// RootOwned reports whether a file is controlled by the operating system.
func RootOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}
