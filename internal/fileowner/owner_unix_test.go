//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package fileowner

import (
	"io/fs"
	"os"
	"syscall"
	"testing"
	"time"
)

type ownerInfo struct{}

func (ownerInfo) Name() string       { return "fixture" }
func (ownerInfo) Size() int64        { return 0 }
func (ownerInfo) Mode() fs.FileMode  { return 0o600 }
func (ownerInfo) ModTime() time.Time { return time.Time{} }
func (ownerInfo) IsDir() bool        { return false }
func (ownerInfo) Sys() any           { return &syscall.Stat_t{Uid: 0} }

type foreignOwnerInfo struct {
	ownerInfo
}

func (info foreignOwnerInfo) Sys() any {
	uid := uint32(os.Geteuid()) + 1
	if uid == 0 {
		uid = 1
	}
	return &syscall.Stat_t{Uid: uid}
}

func TestTrustedAcceptsCurrentOrRootAndRejectsForeignOwner(t *testing.T) {
	if !Trusted(ownerInfo{}) {
		t.Fatal("root-owned file was rejected")
	}
	if !RootOwned(ownerInfo{}) {
		t.Fatal("root ownership was not recognized")
	}
	if os.Geteuid() != 0 {
		current := fileInfoWithUID{uid: uint32(os.Geteuid())}
		if !Trusted(current) {
			t.Fatal("current-user file was rejected")
		}
	}
	if Trusted(foreignOwnerInfo{}) {
		t.Fatal("foreign-owned file was accepted")
	}
	if RootOwned(foreignOwnerInfo{}) {
		t.Fatal("foreign owner was reported as root")
	}
}

func TestCurrentOwnedOnlyAcceptsTheEffectiveUser(t *testing.T) {
	current := fileInfoWithUID{uid: uint32(os.Geteuid())}
	if !CurrentOwned(current) {
		t.Fatal("current owner was rejected")
	}
	foreign := fileInfoWithUID{uid: uint32(os.Geteuid()) + 1}
	if foreign.uid == uint32(os.Geteuid()) {
		foreign.uid++
	}
	if CurrentOwned(foreign) {
		t.Fatal("foreign owner was accepted")
	}
}

func TestTrustedGroupAcceptsCurrentGroup(t *testing.T) {
	info := fileInfoWithOwner{uid: uint32(os.Geteuid()) + 1, gid: uint32(os.Getegid())}
	if !TrustedGroup(info) {
		t.Fatal("current-group file was rejected")
	}
	info.gid++
	groups := append([]int{os.Getegid()}, mustGroups(t)...)
	for {
		found := false
		for _, group := range groups {
			if info.gid == uint32(group) {
				info.gid++
				found = true
				break
			}
		}
		if !found {
			break
		}
	}
	if TrustedGroup(info) {
		t.Fatal("foreign-group file was accepted")
	}
}

func mustGroups(t *testing.T) []int {
	t.Helper()
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	return groups
}

type fileInfoWithUID struct {
	uid uint32
}

func (fileInfoWithUID) Name() string       { return "fixture" }
func (fileInfoWithUID) Size() int64        { return 0 }
func (fileInfoWithUID) Mode() fs.FileMode  { return 0o600 }
func (fileInfoWithUID) ModTime() time.Time { return time.Time{} }
func (fileInfoWithUID) IsDir() bool        { return false }
func (info fileInfoWithUID) Sys() any      { return &syscall.Stat_t{Uid: info.uid} }

type fileInfoWithOwner struct {
	uid uint32
	gid uint32
}

func (fileInfoWithOwner) Name() string       { return "fixture" }
func (fileInfoWithOwner) Size() int64        { return 0 }
func (fileInfoWithOwner) Mode() fs.FileMode  { return 0o660 }
func (fileInfoWithOwner) ModTime() time.Time { return time.Time{} }
func (fileInfoWithOwner) IsDir() bool        { return false }
func (info fileInfoWithOwner) Sys() any {
	return &syscall.Stat_t{Uid: info.uid, Gid: info.gid}
}
