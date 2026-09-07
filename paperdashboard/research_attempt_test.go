package paperdashboard

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResearchAttemptPrivateRoundTripAndStateInvariants(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "attempt.json")
	now := time.Date(2026, 9, 8, 1, 2, 3, 0, time.UTC)
	start, finish := now.Add(-time.Minute), now.Add(-time.Second)
	base := ResearchAttempt{Version: 1, CheckedAt: now, State: "running", InvocationID: strings.Repeat("a", 32), StartedAt: &start}
	for _, state := range []string{"preparing", "running", "finishing", "completed", "failed", "idle", "unknown"} {
		status := base
		status.State = state
		switch state {
		case "preparing":
			status.StartedAt = nil
		case "completed", "failed":
			status.FinishedAt = &finish
		case "idle", "unknown":
			status.StartedAt = nil
			status.InvocationID = ""
		}
		if err := RecordResearchAttempt(path, status); err != nil {
			t.Fatalf("%s: %v", state, err)
		}
		got, err := readResearchAttempt(path, now)
		if err != nil || got.State != state {
			t.Fatalf("%s: %+v %v", state, got, err)
		}
	}
	if err := RecordResearchAttempt(path, base); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private file: %v %v", info, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ResearchAttempt){
		func(s *ResearchAttempt) { s.Version = 2 },
		func(s *ResearchAttempt) { s.State = "invented" },
		func(s *ResearchAttempt) { s.InvocationID = "" },
		func(s *ResearchAttempt) { s.InvocationID = strings.Repeat("A", 32) },
		func(s *ResearchAttempt) { s.InvocationID = strings.Repeat("z", 32) },
		func(s *ResearchAttempt) { s.StartedAt = nil },
		func(s *ResearchAttempt) { s.FinishedAt = &finish },
		func(s *ResearchAttempt) { s.State = "preparing" },
		func(s *ResearchAttempt) { s.State = "idle" },
		func(s *ResearchAttempt) { s.State = "completed" },
		func(s *ResearchAttempt) { future := now.Add(3 * time.Second); s.StartedAt = &future },
		func(s *ResearchAttempt) { s.CheckedAt = now.In(time.FixedZone("other", 3600)) },
		func(s *ResearchAttempt) { s.State = "completed"; old := start.Add(-time.Second); s.FinishedAt = &old },
	} {
		bad := base
		mutate(&bad)
		if err := RecordResearchAttempt(path, bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("invalid write changed saved status")
		}
	}
	for _, state := range []string{"failed", "finishing"} {
		status := base
		status.State = state
		status.StartedAt = nil
		if err := RecordResearchAttempt(path, status); err != nil {
			t.Fatalf("pre-start %s: %v", state, err)
		}
	}
}

func TestResearchAttemptReaderRejectsStaleMalformedAndUnsafeFiles(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "attempt.json")
	now := time.Date(2026, 9, 8, 1, 2, 3, 0, time.UTC)
	status := ResearchAttempt{Version: 1, CheckedAt: now, State: "idle"}
	if err := RecordResearchAttempt(path, status); err != nil {
		t.Fatal(err)
	}
	if _, err := readResearchAttempt(path, now.Add(90*time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now.Add(90*time.Second + time.Nanosecond), now.Add(-3 * time.Second)} {
		if got, err := readResearchAttempt(path, at); err == nil || got != nil {
			t.Fatal("returned stale/future data")
		}
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{
		append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"version":1}`)...),
		append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"private_prompt":"not allowed"}`)...),
		[]byte(`{"version":true}`), []byte(`{`), bytes.Repeat([]byte(" "), 2049),
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := readResearchAttempt(path, now); err == nil || got != nil {
			t.Fatal("returned malformed data")
		}
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := readResearchAttempt(path, now); err == nil || got != nil {
		t.Fatal("public file accepted")
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readResearchAttempt(link, now); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestResearchAttemptSnapshotIndependentFromSavedResearch(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "attempt.json")
	now := time.Date(2026, 9, 8, 1, 2, 3, 0, time.UTC)
	s := &Server{}
	if err := s.EnableResearchAttempt("relative"); err == nil {
		t.Fatal("relative path accepted")
	}
	if err := s.EnableResearchAttempt(path); err != nil {
		t.Fatal(err)
	}
	view := s.readSnapshot(now)
	if !view.ResearchAttemptEnabled || view.ResearchAttempt != nil || view.ResearchAttemptError || view.ResearchEnabled || view.ResearchError {
		t.Fatalf("missing: %+v", view)
	}
	start := now.Add(-time.Minute)
	if err := RecordResearchAttempt(path, ResearchAttempt{Version: 1, CheckedAt: now, State: "running", InvocationID: strings.Repeat("a", 32), StartedAt: &start}); err != nil {
		t.Fatal(err)
	}
	view = s.readSnapshot(now)
	if view.ResearchAttempt == nil || view.ResearchAttempt.State != "running" || view.ResearchAttemptError || view.Research != nil || view.ResearchError {
		t.Fatalf("running: %+v", view)
	}
	view = s.readSnapshot(now.Add(91 * time.Second))
	if view.ResearchAttempt != nil || !view.ResearchAttemptError || view.ResearchError {
		t.Fatalf("stale: %+v", view)
	}
}
