package journal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerifyJournalReadOnlySummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC)
	eventTypes := []string{"swap.send_started", "swap.submitted"}
	for index, eventType := range eventTypes {
		if _, err := store.Append(
			now.Add(time.Duration(index)*time.Second),
			eventType,
			fmt.Sprintf("private-action-%d", index),
			map[string]any{"secret": fmt.Sprintf("private-payload-%d", index)},
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantFileHash := sha256.Sum256(data)
	verified, err := Verify(path)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Records != 2 || verified.Bytes != int64(len(data)) ||
		verified.FileSHA256 != hex.EncodeToString(wantFileHash[:]) ||
		verified.ChainHeadSHA256 == "" || verified.SendStartedRecords != 1 ||
		verified.SubmittedRecords != 1 {
		t.Fatalf("verification = %+v", verified)
	}
}

func TestVerifyJournalRejectsActiveWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := Verify(path); err == nil || !errors.Is(err, ErrLocked) {
		t.Fatalf("active journal verification error = %v", err)
	}
}

func TestVerifyJournalRejectsTornTailWithoutRepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(time.Now(), "test.event", "", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"sequence":2`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(path); err == nil || !strings.Contains(err.Error(), "incomplete final record") {
		t.Fatalf("torn journal verification error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("verification changed the torn journal")
	}
}

func TestVerifyJournalDoesNotCreateMissingPath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "missing", "events.jsonl")
	if _, err := Verify(path); err == nil {
		t.Fatal("missing journal was accepted")
	}
	if _, err := os.Lstat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verification created the journal directory: %v", err)
	}
}

func TestVerifyJournalIgnoresReserveAndAcceptsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureCapacity(1, 4096); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reserveBefore, err := os.ReadFile(path + ".reserve")
	if err != nil {
		t.Fatal(err)
	}
	verified, err := Verify(path)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Records != 0 || verified.Bytes != 0 ||
		verified.ChainHeadSHA256 != "" ||
		verified.FileSHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty journal verification = %+v", verified)
	}
	reserveAfter, err := os.ReadFile(path + ".reserve")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reserveBefore, reserveAfter) {
		t.Fatal("verification changed the journal reserve")
	}
}

func TestVerifyJournalRejectsUnsafeFiles(t *testing.T) {
	root := t.TempDir()
	public := filepath.Join(root, "public.jsonl")
	if err := os.WriteFile(public, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.jsonl")
	if err := os.Symlink(public, link); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"public mode": public,
		"symlink":     link,
		"directory":   root,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Verify(path); err == nil {
				t.Fatal("unsafe journal was accepted")
			}
		})
	}
}

func TestJournalRoundTripTamperAndLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "events.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC)
	if _, err := store.Append(now, "test.event", "action", map[string]any{"amount": 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil || !errors.Is(err, ErrLocked) ||
		!strings.Contains(err.Error(), "already open") {
		t.Fatalf("second open error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	records := reopened.Records()
	if len(records) != 1 || records[0].Sequence != 1 {
		t.Fatalf("records = %+v", records)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(data[:len(data)-1], &record); err != nil {
		t.Fatal(err)
	}
	record["type"] = "tampered"
	tampered, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(tampered, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "hash does not match") {
		t.Fatalf("tampered open error = %v", err)
	}
	if _, err := Verify(path); err == nil || !strings.Contains(err.Error(), "hash does not match") {
		t.Fatalf("tampered verification error = %v", err)
	}
}

func TestJournalRecoversOnlyAnIncompleteTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(time.Now(), "test.event", "", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"sequence":2`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || len(recovered.Records()) != 1 {
		t.Fatalf("recovery size/records = %d/%d, want %d/1", after.Size(), len(recovered.Records()), before.Size())
	}
}

func TestJournalRejectsWritableOrSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	public := filepath.Join(root, "public")
	if err := os.Mkdir(public, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(public, "events.jsonl")); err == nil {
		t.Fatal("journal accepted a world-writable directory")
	}

	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(link, "events.jsonl")); err == nil {
		t.Fatal("journal accepted a symlink directory")
	}
}

func TestJournalRejectsUnknownRecordFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(time.Now(), "test.event", "", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &record); err != nil {
		t.Fatal(err)
	}
	record["unprotected"] = true
	changed, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(changed, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("unknown record field was accepted")
	}
}

func TestJournalRejectsDuplicatePayloadFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	record := Record{
		Sequence: 1,
		At:       time.Now().UTC(),
		Type:     "test.event",
		Payload:  json.RawMessage(`{"slot":1,"Slot":2}`),
	}
	hash, err := recordHash(record)
	if err != nil {
		t.Fatal(err)
	}
	record.Hash = hash
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("duplicate payload fields were accepted")
	}
}

func TestJournalRejectsRegressingRecordTimes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := store.Append(now, "test.first", "", struct{}{}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(now.Add(-time.Second), "test.second", "", struct{}{}); err == nil {
		t.Fatal("regressing append time was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("rejected append changed the journal")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	first := Record{Sequence: 1, At: now, Type: "test.first", Payload: json.RawMessage(`{}`)}
	first.Hash, err = recordHash(first)
	if err != nil {
		t.Fatal(err)
	}
	second := Record{
		Sequence: 2, At: now.Add(-time.Second), Type: "test.second",
		Payload: json.RawMessage(`{}`), PrevHash: first.Hash,
	}
	second.Hash, err = recordHash(second)
	if err != nil {
		t.Fatal(err)
	}
	firstLine, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondLine, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	encoded := append(append(firstLine, '\n'), append(secondLine, '\n')...)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "time regressed") {
		t.Fatalf("regressing journal open error = %v", err)
	}
}

func TestJournalCapacityCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureCapacity(3, 3<<20); err != nil {
		t.Fatal(err)
	}
	reserveInfo, err := os.Stat(path + ".reserve")
	if err != nil {
		t.Fatal(err)
	}
	if reserveInfo.Size() != 3<<20 || reserveInfo.Mode().Perm() != 0o600 {
		t.Fatalf("journal reserve = size %d mode %s", reserveInfo.Size(), reserveInfo.Mode())
	}
	if err := store.EnsureCapacity(maxRecords+1, 1); err == nil {
		t.Fatal("impossible record reservation was accepted")
	}
	if err := store.EnsureCapacity(1, maxJournalBytes+1); err == nil {
		t.Fatal("impossible byte reservation was accepted")
	}
	if _, err := store.Append(time.Now(), "test.event", "", struct{}{}); err != nil {
		t.Fatal(err)
	}
	stats, err := store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Records != 1 || stats.Bytes <= 0 ||
		stats.ReservedBytes <= 0 || stats.ReservedBytes >= 3<<20 ||
		stats.MaxRecords != maxRecords || stats.MaxBytes != maxJournalBytes {
		t.Fatalf("journal stats = %+v", stats)
	}
	remaining := stats.ReservedBytes
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	stats, err = reopened.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.ReservedBytes != remaining {
		t.Fatalf("reopened journal reserve = %d, want %d", stats.ReservedBytes, remaining)
	}
	if err := reopened.ReleaseCapacity(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path + ".reserve"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released journal reserve still exists: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestJournalFailsClosedOnPhysicalENOSPC(t *testing.T) {
	root := os.Getenv("MITHRIL_AGENT_ENOSPC_TEST_DIR")
	if root == "" {
		t.Skip("requires an isolated size-limited filesystem")
	}
	dir := filepath.Join(root, "journal")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureCapacity(1, 8<<20); err == nil {
		t.Fatal("journal capacity reservation succeeded beyond filesystem capacity")
	}
	stats, err := store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.ReservedBytes != 0 {
		t.Fatalf("failed reservation retained %d bytes", stats.ReservedBytes)
	}
	if _, err := store.Append(time.Now(), "test.after_enospc", "", struct{}{}); err != nil {
		t.Fatalf("journal was unusable after a failed reservation: %v", err)
	}
}
