package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

type unixAddr struct{}

func (unixAddr) Network() string { return "unix" }
func (unixAddr) String() string  { return "/run/test-dashboard.sock" }

type blockingListener struct {
	accepted chan struct{}
	closed   chan struct{}
}

func (l *blockingListener) Accept() (net.Conn, error) {
	select {
	case <-l.accepted:
	default:
		close(l.accepted)
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *blockingListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (*blockingListener) Addr() net.Addr { return unixAddr{} }

func TestRunServesOnlyFromActivatedUnixSocket(t *testing.T) {
	listener := &blockingListener{accepted: make(chan struct{}), closed: make(chan struct{})}
	previous := activatedListener
	activatedListener = func() (net.Listener, error) { return listener, nil }
	t.Cleanup(func() { activatedListener = previous })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{
			"--paper-status-socket", "SOL/USDC=/run/mithril-agent-paper-status.sock",
			"--paper-status-socket", "JUP/USDC=/run/mithril-agent-paper-jup-status.sock",
			"--research-packet-path", "/var/lib/mithril-agent-dashboard/research.json",
		}, &bytes.Buffer{})
	}()
	select {
	case <-listener.accepted:
	case <-time.After(time.Second):
		t.Fatal("HTTP server did not accept on the activated listener")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP server did not stop")
	}
}

func TestSocketPathsRejectUnsafeOrAmbiguousValues(t *testing.T) {
	var paths socketPaths
	if err := paths.Set("SOL/USDC=/run/sol.sock"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"/run/missing-label.sock", "bad label=/run/bad.sock", "JUP/USDC=relative.sock",
		"SOL/USDC=/run/other.sock", "JUP/USDC=/run/sol.sock",
	} {
		if err := paths.Set(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	if got := paths.String(); got != "SOL/USDC=/run/sol.sock" {
		t.Fatalf("paths = %q", got)
	}
}

func TestRunRequiresSourcesAndActivatedSocket(t *testing.T) {
	if err := run(t.Context(), nil, &bytes.Buffer{}); err == nil {
		t.Fatal("missing source accepted")
	}
	previous := activatedListener
	activatedListener = func() (net.Listener, error) { return nil, errors.New("not activated") }
	t.Cleanup(func() { activatedListener = previous })
	if err := run(t.Context(), []string{
		"--paper-status-socket", "SOL/USDC=/run/sol.sock",
	}, &bytes.Buffer{}); err == nil || err.Error() != "not activated" {
		t.Fatalf("activation error = %v", err)
	}
}

func TestRenderInstructionModeDoesNotOpenAListener(t *testing.T) {
	previous := activatedListener
	activatedListener = func() (net.Listener, error) {
		t.Fatal("render mode opened a listener")
		return nil, errors.New("unexpected listener")
	}
	t.Cleanup(func() { activatedListener = previous })
	var output bytes.Buffer
	if err := run(t.Context(), []string{"--render-instruction", "/tmp/missing-paper-instruction.json"}, &output); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("missing instruction output = %q", output.String())
	}
	if err := run(t.Context(), []string{
		"--render-instruction", "/tmp/missing-paper-instruction.json",
		"--paper-status-socket", "SOL/USDC=/run/sol.sock",
	}, &output); err == nil {
		t.Fatal("mixed render and server flags accepted")
	}
	if err := run(t.Context(), []string{
		"--render-instruction", "/tmp/missing-paper-instruction.json",
		"--research-packet-path", "/tmp/research.json",
	}, &output); err == nil {
		t.Fatal("render mode accepted a research projection")
	}
}
