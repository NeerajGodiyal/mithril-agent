//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package submitter

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoveryLockSerializesWritersAndRejectsSymlinks(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := Policy{ControlStatePath: filepath.Join(directory, "control.json")}
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- withRecoveryLock(policy, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- withRecoveryLock(policy, func() error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("second recovery writer entered while the first held the lock")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for name, done := range map[string]<-chan error{
		"first": firstDone, "second": secondDone,
	} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s recovery lock = %v", name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s recovery lock did not finish", name)
		}
	}

	if err := os.Remove(recoveryPath(policy) + ".lock"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, recoveryPath(policy)+".lock"); err != nil {
		t.Fatal(err)
	}
	if err := withRecoveryLock(policy, func() error { return nil }); err == nil {
		t.Fatal("unsafe recovery lock was accepted")
	}
}
