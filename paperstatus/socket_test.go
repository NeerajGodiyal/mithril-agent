package paperstatus

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCredentialReaderAcceptsSystemdServiceOwnedCredential(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	snapshot := Snapshot{
		Version: Version, ObservedAt: now,
		Events: []Event{{
			ID: eventID(KindOrderFilled, "credential"), At: now, Kind: KindOrderFilled,
			Message: "PAPER SIMULATION — filled. No transaction was signed or submitted.",
		}},
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "paper-status")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewCredentialReader(directory, "paper-status")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reader.Read(); err != nil || len(got.Events) != 1 {
		t.Fatalf("service-owned credential = %+v, %v", got, err)
	}
}

type snapshotStub struct {
	snapshot Snapshot
	reads    int
}

func (s *snapshotStub) Read() (Snapshot, error) {
	s.reads++
	return s.snapshot, nil
}

func TestSocketServesOneValidatedSnapshotWithoutRequest(t *testing.T) {
	path := shortSocketPath(t)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	stub := &snapshotStub{snapshot: Snapshot{
		Version: Version, ObservedAt: now,
		Events: []Event{{
			ID: eventID(KindOrderFilled, "one"), At: now, Kind: KindOrderFilled,
			Message: "PAPER SIMULATION — filled. No transaction was signed or submitted.",
		}},
	}}
	serverError := make(chan error, 1)
	go func() { serverError <- Serve(t.Context(), listener, stub) }()
	reader, err := newSocketReader(path, defaultTimeout, false)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reader.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 1 || snapshot.Events[0].Kind != KindOrderFilled || stub.reads != 1 {
		t.Fatalf("snapshot=%+v reads=%d", snapshot, stub.reads)
	}
	if err := <-serverError; err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(); err == nil {
		t.Fatal("one-shot bridge accepted a second connection")
	}
}

func TestServeStopsOnContextCancellation(t *testing.T) {
	path := shortSocketPath(t)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Serve(ctx, listener, &snapshotStub{}); err != nil {
		t.Fatal(err)
	}
}

func shortSocketPath(t *testing.T) string {
	t.Helper()
	file, err := os.CreateTemp("/tmp", "paper-status-")
	if err != nil {
		t.Fatal(err)
	}
	path := file.Name() + ".sock"
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file.Name()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	return filepath.Clean(path)
}
