package paperdashboard

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMithrilEvidenceStatusIsStrictPrivateAndHostProduced(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "mithril.json")
	now := time.Date(2026, 9, 2, 1, 2, 3, 0, time.UTC)
	if err := RecordMithrilEvidence(path, true, now); err != nil {
		t.Fatal(err)
	}
	status, err := readMithrilEvidence(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if !status.AvailableAtCheck || !status.CheckedAt.Equal(now) || status.MaxRecordAgeSeconds != 900 {
		t.Fatalf("status = %+v", status)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"checked_at":"2026-09-02T01:02:03Z","available_at_check":true,"max_record_age_seconds":900,"claimed_by_agent":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readMithrilEvidence(path, now); err == nil {
		t.Fatal("status with an agent-authored field was accepted")
	}
}

func TestMithrilEvidenceRejectsFutureAndNonUTCChecks(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "mithril.json")
	now := time.Date(2026, 9, 2, 1, 2, 3, 0, time.UTC)
	write := func(checkedAt string) {
		t.Helper()
		body := []byte(`{"version":1,"checked_at":"` + checkedAt + `","available_at_check":true,"max_record_age_seconds":900}`)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("2026-09-02T01:04:04Z")
	if _, err := readMithrilEvidence(path, now); err == nil {
		t.Fatal("future Mithril evidence check was accepted")
	}
	write("2026-09-02T06:32:03+05:30")
	if _, err := readMithrilEvidence(path, now); err == nil {
		t.Fatal("non-UTC Mithril evidence check was accepted")
	}
}
