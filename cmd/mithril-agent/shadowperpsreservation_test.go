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

func reservationForTest(t *testing.T, state string, at time.Time) shadowPerpsReservation {
	t.Helper()
	var output bytes.Buffer
	if err := runShadowPerpsReservation([]string{"--state-dir", state, "--symbol", "SOL"}, &output, func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output.Bytes(), []byte("rationale")) || bytes.Contains(output.Bytes(), []byte(state)) || output.Len() > 4096 {
		t.Fatalf("unsafe reservation projection: %s", output.String())
	}
	var result shadowPerpsReservation
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPerpsReservationReadOnlyBeforeAndAfterFreeze(t *testing.T) {
	args, state, first, at := perpsFreezeFixture(t)
	directory := filepath.Join(filepath.Dir(state), "proposals")
	result := reservationForTest(t, state, at)
	if result.Status != "unreserved" || result.TargetEpisode != "2" || !validLowerSHA256(result.EpisodePrefixSHA256) || result.ProposalSHA256 != "" || result.FrozenAt != nil {
		t.Fatalf("unreserved=%+v", result)
	}
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		t.Fatalf("read-only check created proposal directory: %v", err)
	}
	var frozen bytes.Buffer
	if err := runShadowPerpsFreeze(args, &frozen, func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
	var proposal shadowPerpsProposal
	if err := json.Unmarshal(frozen.Bytes(), &proposal); err != nil {
		t.Fatal(err)
	}
	result = reservationForTest(t, state, at)
	if result.Status != "reserved" || result.ProposalSHA256 != proposal.ContentSHA256 || result.HypothesisID != proposal.Input.HypothesisID || result.Strategy != proposal.Input.Strategy || result.RiskArm != proposal.Input.RiskArm || result.ContextSHA256 != "" || result.FrozenAt == nil || !result.FrozenAt.Equal(at) || !result.ObservedAt.Equal(at) || result.Authorized || result.Promotable || !result.PaperOnly {
		t.Fatalf("reserved=%+v", result)
	}
	path := filepath.Join(directory, "sol", proposal.Input.HypothesisID+".json")
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(frozen.Bytes(), after) {
		t.Fatalf("preflight changed receipt: %v", err)
	}
	// A later attempt changes count+1. Do not permanently block on an older
	// target, nor pretend the earlier preflight reserved anything itself.
	if err := first.finish(state, at.Add(time.Second), false); err != nil {
		t.Fatal(err)
	}
	next, err := beginShadowPerpsEpisode(state, episodeTestConfig(), at.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer next.store.Close()
	result = reservationForTest(t, state, at.Add(2*time.Second))
	if result.Status != "unreserved" || result.TargetEpisode != "3" {
		t.Fatalf("new target=%+v", result)
	}
}

func TestPerpsReservationRejectsInvalidEvidenceWithoutOutput(t *testing.T) {
	for _, mode := range []string{"corrupt", "misnamed", "duplicate", "future", "prefix", "symbol", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			args, state, _, at := perpsFreezeFixture(t)
			if err := runShadowPerpsFreeze(args, &bytes.Buffer{}, func() time.Time { return at }); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(filepath.Dir(state), "proposals", "sol")
			path := filepath.Join(directory, "test-proposal.json")
			symbol := "SOL"
			switch mode {
			case "corrupt":
				if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "misnamed":
				if err := os.Rename(path, filepath.Join(directory, "other.json")); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				proposal, _, err := readPerpsProposal(path)
				if err != nil {
					t.Fatal(err)
				}
				proposal.Input.HypothesisID = "other"
				raw, err := canonicalPerpsProposal(proposal)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, "other.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "future":
				at = at.Add(-time.Nanosecond)
			case "prefix":
				if err := os.WriteFile(filepath.Join(filepath.Dir(state), "current-episodes.jsonl.prefix.json"), []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symbol":
				symbol = "BTC"
			case "symlink":
				if err := os.Rename(directory, directory+"-real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(directory+"-real", directory); err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			if err := runShadowPerpsReservation([]string{"--state-dir", state, "--symbol", symbol}, &output, func() time.Time { return at }); err == nil || output.Len() != 0 {
				t.Fatalf("accepted %s: %v %s", mode, err, output.String())
			}
		})
	}
}

func TestPerpsReservationHelpAndArgumentBounds(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"shadow", "perps-reservation", "--help"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "creates no reservation") || !strings.Contains(output.String(), "need not come") {
		t.Fatal("help lost snapshot limitation")
	}
	for _, args := range [][]string{nil, {"--state-dir", "relative", "--symbol", string(perpspaper.SOL)}, {"--state-dir", "/tmp/state", "--symbol", "DOGE"}} {
		output.Reset()
		if err := runShadowPerpsReservation(args, &output, time.Now); err == nil || output.Len() != 0 {
			t.Fatalf("accepted invalid args: %v", args)
		}
	}
}
