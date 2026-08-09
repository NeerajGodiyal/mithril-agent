//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package telegramoperator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
)

func TestSeparateServicesCannotShareFileCursorConsumer(t *testing.T) {
	directory := protectedTempDir(t)
	if err := secureexec.ValidateProtectedDirectory(directory); err != nil {
		t.Fatalf("temporary lock directory is not protected: %v", err)
	}
	cursor := FileCursor(filepath.Join(directory, "telegram-offset.json"))
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		t.Fatalf("temporary lock directory is unavailable: %v", err)
	}
	firstBot := &blockingBot{started: make(chan struct{})}
	first, err := New(Config{
		Bot: firstBot, Cursor: cursor, Sources: []StatusReader{&statusStub{}},
		AllowedChatIDs: []int64{123},
	})
	if err != nil {
		t.Fatal(err)
	}
	secondBot := &blockingBot{started: make(chan struct{})}
	second, err := New(Config{
		Bot: secondBot, Cursor: cursor, Sources: []StatusReader{&statusStub{}},
		AllowedChatIDs: []int64{123},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- first.Run(ctx) }()
	select {
	case <-firstBot.started:
	case err := <-done:
		t.Fatalf("first service did not reach Telegram polling: %v", err)
	case <-t.Context().Done():
		t.Fatal("first service did not reach Telegram polling")
	}
	if err := second.Run(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "already active") {
		t.Fatalf("second service consumer error = %v", err)
	}
	select {
	case <-secondBot.started:
		t.Fatal("second service reached Telegram polling")
	default:
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("first service shutdown = %v", err)
	}

	released, release := context.WithCancel(t.Context())
	release()
	if err := second.Run(released); err != nil {
		t.Fatalf("consumer lock was not released: %v", err)
	}
}
