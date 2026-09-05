package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

func perpsFreezeFixture(t *testing.T) ([]string, string, *shadowPerpsEpisode, time.Time) {
	t.Helper()
	state, config, at := shadowPerpsPlanFixture(t)
	if _, _, err := loadOrCreateShadowPerpsPlan(state, perpspaper.Mainnet, config, perpspaper.Balanced, at); err != nil {
		t.Fatal(err)
	}
	qualification := qualifiedShadowPerpsWalkForward(t, state, config, 3)
	// Only the final tape in that fixture is real and finalized; do not use its
	// synthetic earlier tournament evidence as proposal provenance.
	digest := qualification.Tapes[len(qualification.Tapes)-1].ContentSHA256
	tape := filepath.Join(shadowPerpsCorpusDir(state, perpspaper.SOL), digest+".json")
	episode, err := beginShadowPerpsEpisode(state, episodeTestConfig(), at.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = episode.store.Close() })
	input := shadowPerpsProposalInput{HypothesisID: "test-proposal", Symbol: perpspaper.SOL, RiskArm: perpspaper.Conservative, Strategy: perpspaper.StrategyRegime, Rationale: "Test a frozen existing key on a later attempt."}
	inputPath := filepath.Join(filepath.Dir(state), "input.json")
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inputPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return []string{"--state-dir", state, "--in", inputPath, "--tape", tape}, state, episode, at.Add(3 * time.Second)
}

func TestPerpsFreezeCommandPinsLegacyBaselineAndOriginalRetry(t *testing.T) {
	args, state, episode, at := perpsFreezeFixture(t)
	active := shadowPerpsActivePlanPath(state, perpspaper.SOL)
	before, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	tapeBefore, err := os.ReadFile(args[5])
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runShadowPerpsFreeze(args, &output, func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
	var frozen shadowPerpsProposal
	if err := json.Unmarshal(output.Bytes(), &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen.Status != "pending_advisory" || !frozen.PaperOnly || frozen.Authorized || frozen.Promotable || frozen.TargetEpisode != "2" || frozen.Baseline.DecisionMode != shadowPerpsDecisionLegacy || len(frozen.Training) != 1 || !frozen.FrozenAt.Equal(at) {
		t.Fatalf("frozen=%+v", frozen)
	}
	if err := episode.finish(state, at.Add(time.Second), false); err != nil {
		t.Fatal(err)
	}
	next, err := beginShadowPerpsEpisode(state, episodeTestConfig(), at.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := next.finish(state, at.Add(3*time.Second), false); err != nil {
		t.Fatal(err)
	}
	// Retry must not depend on a currently readable baseline or training corpus.
	if err := os.Rename(active, active+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(args[5], args[5]+".saved"); err != nil {
		t.Fatal(err)
	}
	var retried bytes.Buffer
	if err := runShadowPerpsFreeze(args, &retried, func() time.Time { t.Fatal("retry renewed host time"); return at }); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), retried.Bytes()) {
		t.Fatal("retry changed receipt")
	}
	after, err := os.ReadFile(active + ".saved")
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("baseline changed: %v", err)
	}
	tapeAfter, err := os.ReadFile(args[5] + ".saved")
	if err != nil || !bytes.Equal(tapeBefore, tapeAfter) {
		t.Fatalf("training tape changed: %v", err)
	}
}

func TestPerpsFreezeRejectsConflictsAndUntrustedInput(t *testing.T) {
	for _, mode := range []string{"unknown_field", "future_finalization", "missing_tape", "bad_digest", "conflicting_id", "changed_input", "nonarchive", "too_many_tapes"} {
		t.Run(mode, func(t *testing.T) {
			args, state, episode, at := perpsFreezeFixture(t)
			var first bytes.Buffer
			if mode == "conflicting_id" || mode == "changed_input" {
				if err := runShadowPerpsFreeze(args, &first, func() time.Time { return at }); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "unknown_field":
				raw, err := os.ReadFile(args[3])
				if err != nil {
					t.Fatal(err)
				}
				raw = append(raw[:len(raw)-1], []byte(",\"frozen_at\":\"2020-01-01T00:00:00Z\"}")...)
				if err := os.WriteFile(args[3], raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "future_finalization":
				path := shadowPerpsFinalizationJournalPath(state, perpspaper.SOL)
				records, err := journal.ReadRecords(path)
				if err != nil {
					t.Fatal(err)
				}
				if len(records) != 1 {
					t.Fatalf("fixture finalizations=%d", len(records))
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				store, err := journal.OpenRotating(path)
				if err != nil {
					t.Fatal(err)
				}
				_, appendErr := store.Append(at.Add(time.Hour), records[0].Type, records[0].ActionID, records[0].Payload)
				closeErr := store.Close()
				if appendErr != nil || closeErr != nil {
					t.Fatalf("append=%v close=%v", appendErr, closeErr)
				}
			case "missing_tape":
				if err := os.Remove(args[5]); err != nil {
					t.Fatal(err)
				}
			case "bad_digest":
				raw, err := os.ReadFile(args[5])
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(args[5], append(raw, ' '), 0600); err != nil {
					t.Fatal(err)
				}
			case "conflicting_id", "changed_input":
				raw, err := os.ReadFile(args[3])
				if err != nil {
					t.Fatal(err)
				}
				if mode == "conflicting_id" {
					raw = bytes.Replace(raw, []byte("test-proposal"), []byte("other-proposal"), 1)
				} else {
					raw = bytes.Replace(raw, []byte("Test a frozen"), []byte("Change a frozen"), 1)
				}
				if err := os.WriteFile(args[3], raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "nonarchive":
				if err := episode.finish(state, at, false); err != nil {
					t.Fatal(err)
				}
				config := episodeTestConfig()
				config.Archived = false
				next, err := beginShadowPerpsEpisode(state, config, at)
				if err != nil {
					t.Fatal(err)
				}
				defer next.store.Close()
			case "too_many_tapes":
				for i := 0; i < 64; i++ {
					args = append(args, "--tape", args[5])
				}
			}
			var output bytes.Buffer
			err := runShadowPerpsFreeze(args, &output, func() time.Time { return at })
			if err == nil || output.Len() != 0 {
				t.Fatalf("err=%v output=%s", err, output.String())
			}
			if mode == "future_finalization" && !strings.Contains(err.Error(), "training is invalid") {
				t.Fatalf("did not reach training known-at guard: %v", err)
			}
		})
	}
}

func TestPerpsFreezeHelpIsRegisteredAndNonAuthorizing(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"shadow", "perps-freeze", "--help"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"pending", "unauthorized", "nonpromotable", "not claimed to be latest", "use perps-evaluate"} {
		if !strings.Contains(output.String(), text) {
			t.Fatalf("help missing %q", text)
		}
	}
}

func TestPerpsFreezeRejectsHostileReceiptBeforeChoosingTime(t *testing.T) {
	args, state, _, at := perpsFreezeFixture(t)
	directory := filepath.Join(filepath.Dir(state), "proposals", "sol")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(filepath.Dir(state), "untouched.json")
	if err := os.WriteFile(victim, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(directory, "test-proposal.json")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runShadowPerpsFreeze(args, &output, func() time.Time { t.Fatal("hostile receipt reached time selection"); return at }); err == nil || output.Len() != 0 {
		t.Fatalf("err=%v output=%s", err, output.String())
	}
	raw, err := os.ReadFile(victim)
	if err != nil || string(raw) != "untouched" {
		t.Fatalf("target changed: %v", err)
	}
}

func TestPerpsFreezeStalePrefixNeverAdvancesTarget(t *testing.T) {
	args, state, episode, at := perpsFreezeFixture(t)
	prefixPath := episode.path + ".prefix.json"
	oldPrefix, err := os.ReadFile(prefixPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := episode.finish(state, at, false); err != nil {
		t.Fatal(err)
	}
	next, err := beginShadowPerpsEpisode(state, episodeTestConfig(), at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer next.store.Close()
	// A delayed published projection must not silently move the chosen target.
	if err := os.WriteFile(prefixPath, oldPrefix, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runShadowPerpsFreeze(args, &output, func() time.Time { return at.Add(2 * time.Second) }); err != nil {
		t.Fatal(err)
	}
	var proposal shadowPerpsProposal
	if err := json.Unmarshal(output.Bytes(), &proposal); err != nil {
		t.Fatal(err)
	}
	if proposal.TargetEpisode != "2" || proposal.Status != "pending_advisory" || proposal.Authorized || proposal.Promotable || !proposal.FrozenAt.After(next.start.At) {
		t.Fatalf("proposal=%+v", proposal)
	}
	// Episode 2 is already too early. Evaluation must report unevaluable
	// rather than choose episode 3.
}
