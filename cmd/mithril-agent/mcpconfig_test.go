package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ma-mcp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// The printer must refuse when the socket is absent rather than fall back to
// the wider --config surface, and must emit only the read-only form when it
// exists.
func TestMCPConfigPrintsOnlyTheReadOnlySurface(t *testing.T) {
	var out bytes.Buffer
	dir := shortSocketDir(t)
	missingSocket := filepath.Join(dir, "missing.sock")
	if err := runMCPConfig([]string{"--socket", missingSocket}, &out); err == nil {
		t.Fatal("a missing socket must be an error, never a --config fallback")
	}
	socket := filepath.Join(dir, "status.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	out.Reset()
	if err := runMCPConfig([]string{"--socket", socket}, &out); err != nil {
		t.Fatalf("with a socket present: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "--status-socket") || strings.Contains(text, "--config") {
		t.Fatalf("output must use the status-socket form only:\n%s", text)
	}
}

func TestMCPConfigDiscoversEveryStrategyLeg(t *testing.T) {
	original := supervisedMCPStatusSockets
	t.Cleanup(func() { supervisedMCPStatusSockets = original })
	supervisedMCPStatusSockets = nil
	var listeners []net.Listener
	dir := shortSocketDir(t)
	for _, name := range []string{"sell", "buy", "sweep"} {
		path := filepath.Join(dir, name+".sock")
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
		supervisedMCPStatusSockets = append(supervisedMCPStatusSockets,
			mcpStatusSocket{Name: "mithril-agent-" + name, Path: path})
	}
	t.Cleanup(func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	})

	var out bytes.Buffer
	if err := runMCPConfig(nil, &out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mithril-agent-sell", "mithril-agent-buy", "mithril-agent-sweep"} {
		if !strings.Contains(out.String(), `"`+name+`"`) {
			t.Errorf("multi-leg MCP config is missing %s", name)
		}
	}
}
