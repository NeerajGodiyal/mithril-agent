package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/programinterface"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestProgramEvidenceCLI(t *testing.T) {
	registry := t.TempDir()
	file := filepath.Join(t.TempDir(), "analysis.md")
	if err := os.WriteFile(file, []byte("reviewed repository analysis"), 0o600); err != nil {
		t.Fatal(err)
	}
	idl, err := os.ReadFile(writeProgramCommandIDL(t))
	if err != nil {
		t.Fatal(err)
	}
	pin, err := programinterface.Pin(registry, programCommandAddress, idl)
	if err != nil {
		t.Fatal(err)
	}
	review := filepath.Join(t.TempDir(), "review.json")
	writeReview := func(interfaceSHA256 string) {
		t.Helper()
		if err := os.WriteFile(review, []byte(fmt.Sprintf(`{
  "version": 3,
  "reviewer": "operator",
  "decision": "approved",
  "summary": "The reviewed repository implements the deployed instruction set.",
  "source_revision": "0123456789abcdef",
  "tool": "repository-review",
  "tool_version": "1.0.0",
  "interface_sha256": %q,
  "genesis_hash": %q,
  "context_slot": 42,
  "bankhash": %q,
  "deployment_sha256": %q
}
	`, interfaceSHA256, solana.DevnetGenesisHash, programSimulationState, strings.Repeat("d", 64))), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeReview(strings.Repeat("b", 64))
	var output bytes.Buffer
	args := []string{
		"evidence-pin", "--program", "11111111111111111111111111111111",
		"--registry", registry, "--kind", "repository", "--file", file,
		"--review", review, "--json",
	}
	if err := runProgram(t.Context(), args, &output); err == nil {
		t.Fatal("evidence for an unpinned interface was accepted")
	}
	if _, err := os.Stat(filepath.Join(registry, programCommandAddress, "evidence")); !os.IsNotExist(err) {
		t.Fatalf("rejected evidence created storage: %v", err)
	}
	writeReview(pin.SHA256)
	if err := runProgram(t.Context(), args, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"kind":"repository"`) ||
		!strings.Contains(output.String(), `"created":true`) ||
		!strings.Contains(output.String(), `"decision":"approved"`) ||
		!strings.Contains(output.String(), `"summary":"The reviewed repository implements the deployed instruction set."`) {
		t.Fatalf("evidence output = %s", output.String())
	}
	var hash string
	for _, field := range strings.Split(output.String(), `"`) {
		if len(field) == 64 {
			hash = field
		}
	}
	if hash == "" {
		t.Fatal("evidence output has no content hash")
	}
	output.Reset()
	if err := runProgram(t.Context(), []string{
		"evidence-show", "--program", "11111111111111111111111111111111",
		"--registry", registry, "--kind", "repository", "--sha256", hash,
	}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Program evidence verified") {
		t.Fatalf("evidence show output = %s", output.String())
	}
	if !strings.Contains(output.String(), "Interface SHA-256: "+pin.SHA256) {
		t.Fatalf("evidence show output omits its pinned interface = %s", output.String())
	}
	if !strings.Contains(programUsage, "never executed or interpreted") {
		t.Fatal("program usage does not state the evidence execution boundary")
	}
}

func TestProgramEvidenceLabelsLegacyReviewUnbound(t *testing.T) {
	var output bytes.Buffer
	if err := writeProgramEvidence(&output, programinterface.EvidenceResult{
		Attestation: programinterface.EvidenceAttestation{Version: 1},
	}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Interface SHA-256: unbound legacy review") {
		t.Fatalf("legacy evidence output = %s", output.String())
	}
}
