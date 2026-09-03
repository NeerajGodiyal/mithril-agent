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

func TestShadowPerpsTournamentReadsVerifiedTapeWithoutChangingIt(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 5, 30, 0, time.UTC)
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
	if err := runShadowPerpsTournament([]string{"--tape", tapePath}, &output); err != nil {
		t.Fatal(err)
	}
	var tournament perpspaper.Tournament
	if err := json.Unmarshal(output.Bytes(), &tournament); err != nil {
		t.Fatalf("tournament JSON = %q: %v", output.String(), err)
	}
	if !tournament.PaperOnly || tournament.Authorized || tournament.Promotable || tournament.Frames != 1 {
		t.Fatalf("tournament = %+v", tournament)
	}
	after, err := os.ReadFile(tapePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("tournament changed tape: %v", err)
	}
}

func TestShadowPerpsTournamentRejectsInvalidInput(t *testing.T) {
	for _, args := range [][]string{nil, {"--tape", "relative"}, {"--tape", filepath.Join(t.TempDir(), "missing")}} {
		if err := runShadowPerpsTournament(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("arguments accepted: %v", args)
		}
	}
	var output bytes.Buffer
	if err := run([]string{"shadow", "perps-tournament", "--help"}, &output); err != nil || !strings.Contains(output.String(), "only prints JSON") {
		t.Fatalf("help = %q, %v", output.String(), err)
	}
}
