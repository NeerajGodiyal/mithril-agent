package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The printer must refuse when the socket is absent rather than fall back to
// the wider --config surface, and must emit only the read-only form when it
// exists.
func TestMCPConfigPrintsOnlyTheReadOnlySurface(t *testing.T) {
	var out bytes.Buffer
	if err := runMCPConfig(nil, &out); err == nil {
		t.Fatal("a missing socket must be an error, never a --config fallback")
	}
	socket := filepath.Join(t.TempDir(), "status.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runMCPConfig([]string{"--socket", socket}, &out); err != nil {
		t.Fatalf("with a socket present: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "--status-socket") || strings.Contains(text, "--config") {
		t.Fatalf("output must use the status-socket form only:\n%s", text)
	}
}
