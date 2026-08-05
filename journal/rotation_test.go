package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rotateAfter drives a rotating store past the rotation threshold. Rotation
// only ever happens inside ReleaseCapacity, so the helper mirrors what a
// runner does: reserve, append, release.
func rotateAfter(t *testing.T, store *Store, records int, start time.Time) time.Time {
	t.Helper()
	at := start
	for range records {
		if _, err := store.Append(at, "test.event", "", map[string]any{"n": 1}); err != nil {
			t.Fatalf("append: %v", err)
		}
		at = at.Add(time.Second)
	}
	if err := store.EnsureCapacity(1, 1024); err != nil {
		t.Fatalf("ensure capacity: %v", err)
	}
	if err := store.ReleaseCapacity(); err != nil {
		t.Fatalf("release capacity: %v", err)
	}
	return at
}

// forceRotate seals the active segment regardless of size, exercising exactly
// the production code path rather than a test-only shortcut.
func forceRotate(t *testing.T, store *Store) {
	t.Helper()
	store.mu.Lock()
	err := store.rotateLocked()
	store.mu.Unlock()
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
}

func openRotatingForTest(t *testing.T, path string) *Store {
	t.Helper()
	store, err := OpenRotating(path)
	if err != nil {
		t.Fatalf("open rotating: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// The first record of a new active segment must be the rotation marker, and
// it must chain from the sealed segment's head. That is what makes deleting
// the newest sealed segment detectable instead of silent.
func TestRotationSealsAndChains(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "events.jsonl")
	store := openRotatingForTest(t, path)
	start := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	rotateAfter(t, store, 4, start)
	forceRotate(t, store)

	sealed := segmentPath(path, 1)
	if _, err := os.Stat(sealed); err != nil {
		t.Fatalf("sealed segment missing: %v", err)
	}
	if _, err := os.Stat(path + stagedSuffix); !os.IsNotExist(err) {
		t.Fatal("the staged file must be gone once rotation completes")
	}
	records := store.Records()
	if len(records) != 5 {
		t.Fatalf("records after rotation = %d, want 5", len(records))
	}
	marker := records[4]
	if marker.Type != EventRotated {
		t.Fatalf("marker type = %q", marker.Type)
	}
	if marker.PrevHash != records[3].Hash {
		t.Fatal("the marker must chain from the sealed segment's head")
	}
	if marker.Sequence != records[3].Sequence+1 {
		t.Fatal("sequence must stay global across the boundary")
	}
	var payload rotationMarker
	if err := json.Unmarshal(marker.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SealedSegment != 1 || payload.SealedChainHead != records[3].Hash {
		t.Fatalf("marker payload = %+v", payload)
	}
}

// Reopening must reload every segment, continue the global sequence, and keep
// enforcing time monotonicity across a boundary.
func TestRotatedJournalReloadsWholeHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "events.jsonl")
	store := openRotatingForTest(t, path)
	start := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	at := rotateAfter(t, store, 3, start)
	forceRotate(t, store)
	at = rotateAfter(t, store, 2, at)
	forceRotate(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openRotatingForTest(t, path)
	records := reopened.Records()
	// 3 + marker + 2 + marker
	if len(records) != 7 {
		t.Fatalf("reloaded records = %d, want 7", len(records))
	}
	for index, record := range records {
		if record.Sequence != uint64(index+1) {
			t.Fatalf("record %d has sequence %d", index, record.Sequence)
		}
		if index > 0 && record.PrevHash != records[index-1].Hash {
			t.Fatalf("chain broken at %d", index)
		}
	}
	appended, err := reopened.Append(at.Add(time.Hour), "test.event", "", map[string]any{"n": 2})
	if err != nil {
		t.Fatalf("append after reload: %v", err)
	}
	if appended.Sequence != 8 {
		t.Fatalf("appended sequence = %d, want 8", appended.Sequence)
	}
	if _, err := reopened.Append(start, "test.event", "", map[string]any{"n": 3}); err == nil {
		t.Fatal("a record older than the tail must be refused across a boundary")
	}
}

// Concatenating the segments in order must reproduce a stream the ordinary
// single-file scanner accepts. This is the property that keeps the rotated
// journal verifiable by the same rules as the original.
func TestSegmentsConcatenateIntoAValidJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "events.jsonl")
	store := openRotatingForTest(t, path)
	start := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	at := rotateAfter(t, store, 3, start)
	forceRotate(t, store)
	rotateAfter(t, store, 2, at)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var combined []byte
	for _, file := range []string{segmentPath(path, 1), path} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		combined = append(combined, raw...)
	}
	merged := filepath.Join(t.TempDir(), "state", "merged.jsonl")
	if err := os.MkdirAll(filepath.Dir(merged), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(merged, combined, 0o600); err != nil {
		t.Fatal(err)
	}
	plain, err := Open(merged)
	if err != nil {
		t.Fatalf("the concatenation must satisfy the single-file scanner: %v", err)
	}
	defer plain.Close()
	if len(plain.Records()) != 6 {
		t.Fatalf("merged records = %d, want 6", len(plain.Records()))
	}
}

// A plain Open of a rotated journal would silently see only the newest
// segment, losing the history every fail-closed latch is derived from.
func TestPlainOpenRefusesRotatedJournals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "events.jsonl")
	store := openRotatingForTest(t, path)
	rotateAfter(t, store, 2, time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC))
	forceRotate(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "rotated segments") {
		t.Fatalf("plain open of a rotated journal = %v", err)
	}
}

// Crash recovery: the staged file is created and given its marker before
// either rename, so every interrupted state is completable or refusable
// without inventing a record.
func TestRecoveryCompletesInterruptedRotations(t *testing.T) {
	t.Run("crash before either rename", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state", "events.jsonl")
		store := openRotatingForTest(t, path)
		rotateAfter(t, store, 3, time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC))
		forceRotate(t, store)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		// Reconstruct the pre-rename state: unseal the segment and stage the
		// marker the crash would have left behind.
		markerBytes := lastLine(t, path)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(segmentPath(path, 1), path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+stagedSuffix, markerBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		recovered := openRotatingForTest(t, path)
		if len(recovered.Records()) != 4 {
			t.Fatalf("recovered records = %d, want 4", len(recovered.Records()))
		}
		if _, err := os.Stat(segmentPath(path, 1)); err != nil {
			t.Fatalf("recovery must complete the seal: %v", err)
		}
		if _, err := os.Stat(path + stagedSuffix); !os.IsNotExist(err) {
			t.Fatal("recovery must consume the staged file")
		}
	})

	t.Run("crash between the renames", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state", "events.jsonl")
		store := openRotatingForTest(t, path)
		rotateAfter(t, store, 3, time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC))
		forceRotate(t, store)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		// Seal landed, promotion did not: base becomes the staged file again.
		if err := os.Rename(path, path+stagedSuffix); err != nil {
			t.Fatal(err)
		}
		recovered := openRotatingForTest(t, path)
		if len(recovered.Records()) != 4 {
			t.Fatalf("recovered records = %d, want 4", len(recovered.Records()))
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("recovery must promote the staged file: %v", err)
		}
	})
}

// Recovery must refuse to invent history. Losing the active file, or losing a
// sealed segment, is corruption — silently rebuilding either would roll the
// journal back to an earlier state and unlatch whatever it recorded.
func TestRecoveryRefusesToInventHistory(t *testing.T) {
	t.Run("missing active segment", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state", "events.jsonl")
		store := openRotatingForTest(t, path)
		rotateAfter(t, store, 2, time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC))
		forceRotate(t, store)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenRotating(path); err == nil ||
			!strings.Contains(err.Error(), "missing") {
			t.Fatalf("a missing active segment must be refused, got %v", err)
		}
	})

	t.Run("removed sealed segment", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state", "events.jsonl")
		store := openRotatingForTest(t, path)
		at := rotateAfter(t, store, 2, time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC))
		forceRotate(t, store)
		rotateAfter(t, store, 2, at)
		forceRotate(t, store)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(segmentPath(path, 1)); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenRotating(path); err == nil ||
			!strings.Contains(err.Error(), "not contiguous") {
			t.Fatalf("a removed segment must be refused, got %v", err)
		}
	})

	t.Run("emptied active segment", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state", "events.jsonl")
		store := openRotatingForTest(t, path)
		rotateAfter(t, store, 2, time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC))
		forceRotate(t, store)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenRotating(path); err == nil {
			t.Fatal("an active segment stripped of its marker must be refused")
		}
	})
}

// A tampered sealed segment must fail verification, and Verify must report
// the whole history rather than only the active file.
func TestVerifyWalksEverySegment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "events.jsonl")
	store := openRotatingForTest(t, path)
	at := rotateAfter(t, store, 3, time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC))
	forceRotate(t, store)
	rotateAfter(t, store, 2, at)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	verification, err := Verify(path)
	if err != nil {
		t.Fatalf("verify rotated: %v", err)
	}
	if verification.Records != 6 {
		t.Fatalf("verified records = %d, want 6", verification.Records)
	}
	if verification.ChainHeadSHA256 == "" || verification.FileSHA256 == "" {
		t.Fatal("verification must report both digests")
	}

	raw, err := os.ReadFile(segmentPath(path, 1))
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte{}, raw...)
	tampered[len(tampered)/2] ^= 0x20
	if err := os.WriteFile(segmentPath(path, 1), tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(path); err == nil {
		t.Fatal("a single-byte change in a sealed segment must fail verification")
	}
}

// The record cap applies per segment: that is what lets a rotating journal
// outlive the 65,536-record ceiling a single file has.
func TestRecordCapAppliesPerSegment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "events.jsonl")
	store := openRotatingForTest(t, path)
	rotateAfter(t, store, 3, time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC))
	forceRotate(t, store)
	if store.activeRecords() != 1 {
		t.Fatalf("active records after rotation = %d, want 1 (the marker)", store.activeRecords())
	}
	if len(store.Records()) != 4 {
		t.Fatal("the full history must remain visible")
	}
	stats, err := store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Records != 4 {
		t.Fatalf("stats records = %d, want the whole history", stats.Records)
	}
}

// Two processes must not both write, and the guarantee has to survive the
// active file changing identity — which is why the lock lives on its own file.
func TestRotatingStoreHoldsAStableLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "events.jsonl")
	store := openRotatingForTest(t, path)
	rotateAfter(t, store, 2, time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC))
	forceRotate(t, store)
	if _, err := OpenRotating(path); err == nil {
		t.Fatal("a second rotating open must be refused while the first holds the lock")
	}
}

func lastLine(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(strings.TrimRight(string(raw), "\n"), "\n")
	return []byte(lines[len(lines)-1] + "\n")
}
