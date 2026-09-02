//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package jupiterquote

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileRequestGateCoordinatesIndependentClients(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jupiter-rate")
	first := newFileRequestGate(path, time.Hour)
	second := newFileRequestGate(path, time.Hour)
	if err := first.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := second.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second process wait = %v, want deadline", err)
	}
}

func TestFileRequestGatePersistsAndDoesNotAdvanceACancelledReservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jupiter-rate")
	gate := newFileRequestGate(path, time.Hour)
	before := time.Now()
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil || len(first) != 8 {
		t.Fatalf("persisted gate = %x, %v", first, err)
	}
	reserved := int64(binary.BigEndian.Uint64(first))
	if reserved < before.UnixNano() || reserved > time.Now().UnixNano() {
		t.Fatalf("persisted gate timestamp = %d", reserved)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gate.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, first) {
		t.Fatalf("cancelled wait changed state: before %x, after %x, error %v", first, after, err)
	}
}

func TestFileRequestGateRejectsMalformedAndUnsafeState(t *testing.T) {
	for name, prepare := range map[string]func(string) error{
		"malformed": func(path string) error { return os.WriteFile(path, []byte("bad"), 0o600) },
		"unsafe permissions": func(path string) error {
			if err := os.WriteFile(path, make([]byte, 8), 0o600); err != nil {
				return err
			}
			return os.Chmod(path, 0o644)
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jupiter-rate")
			if err := prepare(path); err != nil {
				t.Fatal(err)
			}
			if err := newFileRequestGate(path, time.Second).Wait(context.Background()); err == nil {
				t.Fatal("invalid shared request state was accepted")
			}
		})
	}
}
