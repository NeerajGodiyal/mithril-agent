package paperstatus

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

func TestWriterIsPrivateBoundedAndDeduplicated(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "alerts.json")
	writer, err := OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	for index := 0; index < MaxEvents+2; index++ {
		if err := writer.Append(start.Add(time.Duration(index)*time.Second), KindOrderFilled,
			string(rune('a'+index)), "PAPER SIMULATION — filled. No transaction was signed or submitted."); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Append(start.Add((MaxEvents+1)*time.Second), KindOrderFilled,
		string(rune('a'+MaxEvents+1)), "PAPER SIMULATION — duplicate. No transaction was signed or submitted."); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
	data, err := securefile.ReadPrivate(path, maxSnapshotBytes)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot Snapshot
	if err := strictjson.Decode(data, &snapshot); err != nil || ValidateSnapshot(snapshot) != nil {
		t.Fatalf("invalid snapshot: %v", err)
	}
	if len(snapshot.Events) != MaxEvents || snapshot.DroppedEvents != 2 ||
		snapshot.Events[0].At != start.Add(2*time.Second) {
		t.Fatalf("events=%d dropped=%d first=%s", len(snapshot.Events), snapshot.DroppedEvents, snapshot.Events[0].At)
	}
	gap, ok := TruncationEvent(snapshot)
	if !ok || gap.Kind != "history_truncated" || !strings.Contains(gap.Message, "hash-chained journal") {
		t.Fatalf("truncation event = %+v, %v", gap, ok)
	}
}

func TestWriterRejectsUnsafeOrMalformedEvents(t *testing.T) {
	if _, err := OpenWriter("relative.json"); err == nil {
		t.Fatal("relative path accepted")
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(filepath.Join(directory, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		kind, key, message string
	}{
		{"unknown", "key", "PAPER SIMULATION — test"},
		{KindOrderFilled, "", "PAPER SIMULATION — test"},
		{KindOrderFilled, "key", "looks live"},
		{KindOrderFilled, "key", "PAPER SIMULATION — disclaimer omitted"},
	} {
		if err := writer.Append(time.Now().UTC(), test.kind, test.key, test.message); err == nil {
			t.Fatalf("accepted %+v", test)
		}
	}
}

func TestWriterAcceptsAnOlderDuplicateDuringReconciliation(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(filepath.Join(directory, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	message := "PAPER SIMULATION — filled. No transaction was signed or submitted."
	if err := writer.Append(start, KindOrderFilled, "first", message); err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(start.Add(time.Second), KindOrderFilled, "second", message); err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(start, KindOrderFilled, "first", message); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
}

func TestReconciliationSkipsEventsOlderThanTheBoundedProjection(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "alerts.json")
	writer, err := OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	message := "PAPER SIMULATION — filled. No transaction was signed or submitted."
	for index := 0; index < MaxEvents+2; index++ {
		if err := writer.Append(start.Add(time.Duration(index)*time.Second),
			KindOrderFilled, fmt.Sprintf("event/%d", index), message); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < MaxEvents+2; index++ {
		if err := writer.Reconcile(start.Add(time.Duration(index)*time.Second),
			KindOrderFilled, fmt.Sprintf("event/%d", index), message); err != nil {
			t.Fatal(err)
		}
	}
	data, err := securefile.ReadPrivate(path, maxSnapshotBytes)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot Snapshot
	if err := strictjson.Decode(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != MaxEvents || snapshot.DroppedEvents != 2 {
		t.Fatalf("events=%d dropped=%d", len(snapshot.Events), snapshot.DroppedEvents)
	}
}
