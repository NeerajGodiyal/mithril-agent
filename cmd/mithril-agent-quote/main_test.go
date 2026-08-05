package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunValidatesArgumentsAndHelp(t *testing.T) {
	if err := run(t.Context(), nil, &bytes.Buffer{}); err == nil {
		t.Fatal("missing quote service arguments were accepted")
	}
	var output bytes.Buffer
	if err := run(t.Context(), []string{"--help"}, &output); err != nil ||
		!strings.Contains(output.String(), "Usage: mithril-agent-quote") {
		t.Fatalf("help = %q, %v", output.String(), err)
	}
}

func TestSystemdReadinessNotification(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "mithril-agent-notify-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "notify.sock")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("NOTIFY_SOCKET", path)
	if err := notifySystemdReady(); err != nil {
		t.Fatal(err)
	}
	if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	message := make([]byte, 32)
	count, _, err := listener.ReadFromUnix(message)
	if err != nil {
		t.Fatal(err)
	}
	if string(message[:count]) != "READY=1" {
		t.Fatalf("notification = %q", message[:count])
	}
}

func TestRunStartsAndStopsPrivateSocket(t *testing.T) {
	temporary, err := os.MkdirTemp("/tmp", "mithril-agent-quote-command-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	directory, err := filepath.EvalSymlinks(temporary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	nodeCommand := filepath.Join(directory, "node")
	if err := os.WriteFile(
		nodeCommand,
		[]byte("#!/bin/sh\nprintf '{\"status\":\"ok\"}\\n'\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	quoteScript := filepath.Join(directory, "quote.mjs")
	if err := os.WriteFile(quoteScript, []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MITHRIL_AGENT_QUOTE_RPC_URL", "https://quote.invalid/devnet")
	ctx, cancel := context.WithCancel(t.Context())
	socketPath := filepath.Join(directory, "quote.sock")
	runDone := make(chan error, 1)
	go func() {
		runDone <- run(ctx, []string{
			"--socket", socketPath,
			"--node-command", nodeCommand,
			"--quote-script", quoteScript,
		}, &bytes.Buffer{})
	}()
	for deadline := time.Now().Add(time.Second); ; {
		if info, err := os.Lstat(socketPath); err == nil && info.Mode()&os.ModeSocket != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("quote socket was not created")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-runDone; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quote socket remained after shutdown: %v", err)
	}
}
