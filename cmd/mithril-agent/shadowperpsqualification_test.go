package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

func TestShadowPerpsQualificationReadsVerifiedTapeWithoutChangingIt(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 5, 30, 0, time.UTC)
	directory := filepath.Join(t.TempDir(), "perps")
	if err := runShadowPerpsPaperWith(t.Context(), []string{
		"--state-dir", directory, "--symbols", "SOL", "--once",
	}, &bytes.Buffer{}, func() time.Time { return now }, func(perpspaper.Environment) (shadowPerpsReader, error) {
		return validStubShadowPerpsReader(now), nil
	}); err != nil {
		t.Fatal(err)
	}
	tapePath := filepath.Join(directory, "sol-tape.json")
	before, err := os.ReadFile(tapePath)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runShadowPerpsQualification([]string{"--tape", tapePath}, &output); err != nil {
		t.Fatal(err)
	}
	var qualification perpspaper.Qualification
	if err := json.Unmarshal(output.Bytes(), &qualification); err != nil {
		t.Fatalf("qualification JSON = %q: %v", output.String(), err)
	}
	if !qualification.PaperOnly || qualification.Authorized || qualification.Promotable ||
		qualification.Outcome != "insufficient_evidence" || qualification.Frames != 1 {
		t.Fatalf("qualification = %+v", qualification)
	}
	after, err := os.ReadFile(tapePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("qualification changed tape: %v", err)
	}
}

func TestShadowPerpsQualificationRejectsInvalidInput(t *testing.T) {
	for _, args := range [][]string{nil, {"--tape", "relative"}, {"--tape", filepath.Join(t.TempDir(), "missing")}} {
		if err := runShadowPerpsQualification(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("arguments accepted: %v", args)
		}
	}
	var output bytes.Buffer
	if err := run([]string{"shadow", "perps-qualify", "--help"}, &output); err != nil || !strings.Contains(output.String(), "held-out replay") {
		t.Fatalf("help = %q, %v", output.String(), err)
	}
}
