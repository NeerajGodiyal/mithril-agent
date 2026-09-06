package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

func episodeTestConfig() shadowPerpsEpisodeConfig {
	return shadowPerpsEpisodeConfig{Environment: perpspaper.Mainnet, Symbols: []perpspaper.Symbol{perpspaper.SOL}, RiskArm: perpspaper.Balanced, Collateral: 100_000_000, Cadence: 15 * time.Second, Duration: 30 * time.Minute, Archived: true}
}

func readEpisodePrefix(t *testing.T, path string) []journal.Record {
	t.Helper()
	raw, err := os.ReadFile(path + ".prefix.json")
	if err != nil {
		t.Fatal(err)
	}
	var prefix journal.DurablePrefix
	if err := json.Unmarshal(raw, &prefix); err != nil {
		t.Fatal(err)
	}
	records, err := journal.ReadDurablePrefix(path, prefix)
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func TestPerpsEpisodeInterruptedRestartAndConcurrentPrefix(t *testing.T) {
	state := filepath.Join(t.TempDir(), "current")
	at := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	episode, err := beginShadowPerpsEpisode(state, episodeTestConfig(), at)
	if err != nil {
		t.Fatal(err)
	}
	records := readEpisodePrefix(t, episode.path)
	if pending, count, err := foldShadowPerpsEpisodes(records); err != nil || pending == nil || count != 1 {
		t.Fatalf("pending=%v count=%d err=%v", pending, count, err)
	}
	if other, err := beginShadowPerpsEpisode(state, episodeTestConfig(), at); !errors.Is(err, journal.ErrLocked) {
		if other != nil {
			_ = other.store.Close()
		}
		t.Fatalf("concurrent open=%v", err)
	}
	// Closing without a terminal simulates ownership released by process death.
	if err := episode.store.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := beginShadowPerpsEpisode(state, episodeTestConfig(), at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	records = readEpisodePrefix(t, next.path)
	if len(records) != 3 || records[1].ActionID != "1" || records[2].ActionID != "2" {
		t.Fatalf("records=%+v", records)
	}
	var interrupted shadowPerpsEpisodeEvent
	if err := json.Unmarshal(records[1].Payload, &interrupted); err != nil || interrupted.Outcome != "process_interrupted" {
		t.Fatalf("interrupted=%+v err=%v", interrupted, err)
	}
	if err := next.finish(state, at.Add(2*time.Minute), false); err != nil {
		t.Fatal(err)
	}
	if pending, count, err := foldShadowPerpsEpisodes(readEpisodePrefix(t, next.path)); err != nil || pending != nil || count != 2 {
		t.Fatalf("pending=%v count=%d err=%v", pending, count, err)
	}
}

func TestPerpsEpisodeRecordsBeforeReaderFailure(t *testing.T) {
	state := filepath.Join(t.TempDir(), "current")
	at := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(filepath.Dir(state), "current-episodes.jsonl")
	failure := errors.New("reader unavailable")
	err := runShadowPerpsPaperWith(t.Context(), []string{"--state-dir", state, "--symbols", "SOL", "--once"}, &bytes.Buffer{}, func() time.Time { return at }, func(perpspaper.Environment) (shadowPerpsReader, error) {
		if len(readEpisodePrefix(t, path)) != 1 {
			t.Fatal("reader called before durable start")
		}
		return nil, failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	records := readEpisodePrefix(t, path)
	var end shadowPerpsEpisodeEvent
	if len(records) != 2 {
		t.Fatalf("records=%d", len(records))
	}
	if err := json.Unmarshal(records[1].Payload, &end); err != nil || end.Outcome != "incomplete" || len(end.Tapes) != 0 {
		t.Fatalf("end=%+v err=%v", end, err)
	}
}

func TestPerpsEpisodeEmptyAndNonArchive(t *testing.T) {
	for _, archived := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonarchive", true: "emptyarchive"}[archived], func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "current")
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			config := episodeTestConfig()
			config.Archived = archived
			config.Once = true
			episode, err := beginShadowPerpsEpisode(state, config, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := episode.finish(state, time.Now(), true); err != nil {
				t.Fatal(err)
			}
			records := readEpisodePrefix(t, episode.path)
			if pending, _, err := foldShadowPerpsEpisodes(records); pending != nil || err != nil {
				t.Fatal(err)
			}
			var end shadowPerpsEpisodeEvent
			if err := json.Unmarshal(records[1].Payload, &end); err != nil {
				t.Fatal(err)
			}
			if end.Outcome != "finished" || archived && (len(end.Tapes) != 1 || end.Tapes[0].TapeSHA256 != "") || !archived && len(end.Tapes) != 0 {
				t.Fatalf("end=%+v", end)
			}
		})
	}
}

func TestPerpsEpisodeFoldRejectsMalformedLifecycle(t *testing.T) {
	state := filepath.Join(t.TempDir(), "current")
	at := time.Now()
	episode, err := beginShadowPerpsEpisode(state, episodeTestConfig(), at)
	if err != nil {
		t.Fatal(err)
	}
	if err := episode.finish(state, at.Add(time.Second), false); err != nil {
		t.Fatal(err)
	}
	valid := readEpisodePrefix(t, episode.path)
	for _, name := range []string{"time", "id", "start_hash", "duplicate", "order", "config", "authority"} {
		t.Run(name, func(t *testing.T) {
			records := append([]journal.Record(nil), valid...)
			var end shadowPerpsEpisodeEvent
			if err := json.Unmarshal(records[1].Payload, &end); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "time":
				records[1].At = at.Add(-time.Second)
			case "id":
				records[1].ActionID = "2"
			case "start_hash":
				end.StartSHA256 = "wrong"
			case "duplicate":
				records = append(records, records[1])
			case "order":
				records[0], records[1] = records[1], records[0]
			case "config":
				c := episodeTestConfig()
				end.Config = &c
			case "authority":
				end.Authorized = true
			}
			if name == "start_hash" || name == "config" || name == "authority" {
				records[1].Payload, err = json.Marshal(end)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := foldShadowPerpsEpisodes(records); err == nil {
				t.Fatal("accepted malformed lifecycle")
			}
		})
	}
}

func TestPerpsEpisodeAppendFailureLeavesUnresolvedPrefix(t *testing.T) {
	state := filepath.Join(t.TempDir(), "current")
	episode, err := beginShadowPerpsEpisode(state, episodeTestConfig(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := episode.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := episode.finish(state, time.Now(), true); err == nil {
		t.Fatal("closed journal reported success")
	}
	if pending, _, err := foldShadowPerpsEpisodes(readEpisodePrefix(t, episode.path)); err != nil || pending == nil {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
}

func TestPerpsEpisodePrefixFailurePreventsExternalReader(t *testing.T) {
	parent := t.TempDir()
	state := filepath.Join(parent, "current")
	path := filepath.Join(parent, "current-episodes.jsonl")
	if err := os.Mkdir(path+".prefix.json", 0700); err != nil {
		t.Fatal(err)
	}
	called := false
	err := runShadowPerpsPaperWith(t.Context(), []string{"--state-dir", state, "--symbols", "SOL", "--once"}, &bytes.Buffer{}, time.Now,
		func(perpspaper.Environment) (shadowPerpsReader, error) {
			called = true
			return nil, errors.New("unexpected reader")
		})
	if err == nil || called {
		t.Fatalf("err=%v reader_called=%v", err, called)
	}
	records, err := journal.ReadRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	if pending, count, err := foldShadowPerpsEpisodes(records); err != nil || pending == nil || count != 1 {
		t.Fatalf("pending=%v count=%d err=%v", pending, count, err)
	}
}

func TestPerpsEpisodeOneFrameBindingAndStaleReceiptRefused(t *testing.T) {
	parent := t.TempDir()
	state := filepath.Join(parent, "current")
	at := time.Date(2026, 9, 5, 12, 5, 30, 0, time.UTC)
	reader := validStubShadowPerpsReader(at)
	err := runShadowPerpsPaperWith(t.Context(), []string{
		"--state-dir", state, "--archive-dir", filepath.Join(parent, "runs"), "--symbols", "SOL", "--once",
	}, &bytes.Buffer{}, func() time.Time { return at }, func(perpspaper.Environment) (shadowPerpsReader, error) { return reader, nil })
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "current-episodes.jsonl")
	records := readEpisodePrefix(t, path)
	var end shadowPerpsEpisodeEvent
	if len(records) != 2 {
		t.Fatalf("records=%d", len(records))
	}
	if err := json.Unmarshal(records[1].Payload, &end); err != nil {
		t.Fatal(err)
	}
	if end.Outcome != "finished" || len(end.Tapes) != 1 || end.Tapes[0].Frames != 1 || !validLowerSHA256(end.Tapes[0].FinalizationSHA256) {
		t.Fatalf("one-frame outcome=%+v", end)
	}
	tapePath := filepath.Join(state, "sol-tape.json")
	before, err := os.ReadFile(tapePath)
	if err != nil {
		t.Fatal(err)
	}
	// Even an equal host clock cannot make an existing finalization new evidence.
	next, err := beginShadowPerpsEpisode(state, episodeTestConfig(), at)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.finish(state, at, true); err == nil {
		t.Fatal("stale tape/receipt accepted for new episode")
	}
	after, err := os.ReadFile(tapePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("tape changed: %v", err)
	}
	records = readEpisodePrefix(t, path)
	// Unmarshal into a fresh value because an omitted tapes field is not a reset.
	end = shadowPerpsEpisodeEvent{}
	if err := json.Unmarshal(records[len(records)-1].Payload, &end); err != nil || end.Outcome != "incomplete" || len(end.Tapes) != 0 {
		t.Fatalf("stale outcome=%+v err=%v", end, err)
	}
}

func TestPerpsEpisodeRejectsFinalizationAfterTerminal(t *testing.T) {
	parent := t.TempDir()
	state := filepath.Join(parent, "current")
	at := time.Date(2026, 9, 5, 12, 5, 30, 0, time.UTC)
	reader := validStubShadowPerpsReader(at)
	calls := 0
	clock := func() time.Time {
		calls++
		switch calls {
		case 6: // Finalization follows start, setup, cycle, context and book reads.
			return at.Add(2 * time.Second)
		case 7: // The episode's later clock read regresses, but not before its start.
			return at.Add(time.Second)
		default:
			return at
		}
	}
	err := runShadowPerpsPaperWith(t.Context(), []string{
		"--state-dir", state, "--archive-dir", filepath.Join(parent, "runs"), "--symbols", "SOL", "--once",
	}, &bytes.Buffer{}, clock, func(perpspaper.Environment) (shadowPerpsReader, error) { return reader, nil })
	if calls != 7 {
		t.Fatalf("clock calls=%d, want 7", calls)
	}
	finalizations, readErr := journal.ReadRecords(shadowPerpsFinalizationJournalPath(state, perpspaper.SOL))
	if readErr != nil || len(finalizations) != 1 || !finalizations[0].At.Equal(at.Add(2*time.Second)) {
		t.Fatalf("finalizations=%+v err=%v", finalizations, readErr)
	}
	if err == nil {
		t.Fatal("episode accepted finalization after its terminal time")
	}
	records := readEpisodePrefix(t, filepath.Join(parent, "current-episodes.jsonl"))
	if len(records) != 2 || !records[0].At.Equal(at) || !records[1].At.Equal(at.Add(time.Second)) {
		t.Fatalf("episode records=%+v", records)
	}
	var end shadowPerpsEpisodeEvent
	if err := json.Unmarshal(records[1].Payload, &end); err != nil || end.Outcome != "incomplete" || len(end.Tapes) != 0 {
		t.Fatalf("terminal=%+v err=%v", end, err)
	}
}
