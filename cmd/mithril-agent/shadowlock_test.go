//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestShadowLifecycleLockSerializesPointerWriters(t *testing.T) {
	path := filepath.Join(privateTestDirectory(t), "lifecycle.lock")
	entered := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- withShadowLifecycleLock(path, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	waiterStarted := make(chan struct{})
	waiterEntered := make(chan struct{})
	waiterDone := make(chan error, 1)
	go func() {
		close(waiterStarted)
		waiterDone <- withShadowLifecycleLock(path, func() error {
			close(waiterEntered)
			return nil
		})
	}()
	<-waiterStarted
	select {
	case <-waiterEntered:
		t.Fatal("a second paper pointer writer entered the protected lifecycle")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-waiterEntered:
	case <-time.After(time.Second):
		t.Fatal("the waiting paper pointer writer did not enter after release")
	}
	if err := <-waiterDone; err != nil {
		t.Fatal(err)
	}
}
