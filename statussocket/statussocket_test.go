package statussocket

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/journal"
)

type snapshotStub struct {
	snapshot operatorstatus.Snapshot
	err      error
	reads    int
}

func (s *snapshotStub) Read() (operatorstatus.Snapshot, error) {
	s.reads++
	return s.snapshot, s.err
}

func TestUnixBridgeServesAtMostOneValidatedSnapshotWithoutARequest(t *testing.T) {
	path := shortSocketPath(t, "operator-status.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	reader := &snapshotStub{snapshot: validSnapshot(time.Now().UTC())}
	serverError := make(chan error, 1)
	go func() { serverError <- Serve(ctx, listener, reader) }()

	client, err := newReader(path, defaultTimeout, false)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.Read()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Profile != reader.snapshot.Profile ||
		snapshot.Result.Decision != "stopped" ||
		snapshot.Strategy.FundedTradesPerDay != 3 || reader.reads != 1 {
		t.Fatalf("snapshot=%+v reads=%d", snapshot, reader.reads)
	}
	if err := <-serverError; err != nil {
		t.Fatal(err)
	}
	cancel()
}

func TestCredentialReaderAcceptsSystemdServiceOwnedCredential(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(validSnapshot(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "operator-status")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewCredentialReader(directory, "operator-status")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reader.Read(); err != nil || got.Profile == "" {
		t.Fatalf("service-owned credential = %+v, %v", got, err)
	}
}

func TestServeConnectionReturnsNothingForUnavailableOrInvalidStatus(t *testing.T) {
	for name, reader := range map[string]*snapshotStub{
		"unavailable": {err: errors.New("private source detail")},
		"invalid":     {snapshot: operatorstatus.Snapshot{}},
	} {
		t.Run(name, func(t *testing.T) {
			server, client := net.Pipe()
			done := make(chan struct{})
			go func() {
				serveConnection(server, reader)
				close(done)
			}()
			buffer := make([]byte, 1)
			if count, err := client.Read(buffer); count != 0 || err == nil {
				t.Fatalf("response count=%d err=%v", count, err)
			}
			_ = client.Close()
			<-done
		})
	}
}

func TestReaderRejectsMalformedAndOversizedWireData(t *testing.T) {
	tests := map[string][]byte{
		"unknown field": []byte(`{"version":1,"unexpected":true}`),
		"oversized":     []byte(strings.Repeat("x", maxWireBytes+1)),
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			path, closeServer := oneShotServer(t, response, 0)
			defer closeServer()
			reader, err := newReader(path, defaultTimeout, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.Read(); err == nil {
				t.Fatal("invalid wire response was accepted")
			}
		})
	}
}

func TestReaderHasATightDeadline(t *testing.T) {
	path, closeServer := oneShotServer(t, nil, time.Second)
	defer closeServer()
	reader, err := newReader(path, 100*time.Millisecond, false)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := reader.Read(); err == nil || time.Since(started) > time.Second {
		t.Fatalf("deadline err=%v elapsed=%s", err, time.Since(started))
	}
}

func TestProductionReaderRejectsSocketUnderUserControlledDirectory(t *testing.T) {
	path, closeServer := oneShotServer(t, []byte(`{}`), 0)
	defer closeServer()
	reader, err := NewReader(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(); err == nil ||
		!strings.Contains(err.Error(), "directory is not protected") {
		t.Fatalf("untrusted socket error = %v", err)
	}
}

func TestLiveProtectedStatusSocket(t *testing.T) {
	path := os.Getenv("MITHRIL_AGENT_STATUS_SOCKET_LIVE")
	if path == "" {
		t.Skip("live status socket is not configured")
	}
	reader, err := NewReader(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reader.Read()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Cluster != "devnet" || snapshot.Version != operatorstatus.Version {
		t.Fatalf("unexpected live status identity: version=%d cluster=%q", snapshot.Version, snapshot.Cluster)
	}
}

func oneShotServer(t *testing.T, response []byte, delay time.Duration) (string, func()) {
	t.Helper()
	path := shortSocketPath(t, "one-shot.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		if delay > 0 {
			timer := time.NewTimer(delay)
			<-timer.C
		}
		_, _ = connection.Write(response)
	}()
	return path, func() {
		_ = listener.Close()
		<-done
	}
}

func shortSocketPath(t *testing.T, name string) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "mithril-status-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return filepath.Join(directory, name)
}

func validSnapshot(at time.Time) operatorstatus.Snapshot {
	return operatorstatus.Snapshot{
		Version: operatorstatus.Version, ObservedAt: at,
		Profile: "orca_devnet_swap_v1", ProfileVersion: 1, Cluster: "devnet",
		Result:  execution.Result{Decision: "stopped", Reason: "Devnet actions are not enabled"},
		Journal: journal.Stats{MaxRecords: 100, MaxBytes: 1024},
		Control: control.Status{Mode: control.ModeNoNewActions},
		Strategy: operatorstatus.StrategyProjection{
			Configured: true, Direction: "sell", InputAmount: 50_000_000,
			DailyCap: 150_300_000, MaxFeeLamports: 100_000, FundedTradesPerDay: 3,
		},
	}
}

func TestWireEncodingStaysWithinTheBound(t *testing.T) {
	encoded, err := json.Marshal(validSnapshot(time.Now().UTC()))
	if err != nil || len(encoded) > maxSnapshotBytes {
		t.Fatalf("encoded bytes=%d err=%v", len(encoded), err)
	}
}
