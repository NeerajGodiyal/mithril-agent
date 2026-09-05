package main

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/researchpacket"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestResearchObservationsBindPreviousDayWithoutRenewingAge(t *testing.T) {
	policy := validShadowResearchPolicy()
	directory := privateTestDirectory(t)
	now := time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC)
	writeShadowResearchDay(t, directory, policy, "2026-09-04", []uint64{100_000_000})
	artifact, err := buildResearchObservations(policy, directory, now)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Validate() != nil || artifact.Market != "SOL/USDC" ||
		artifact.ObservedFrom != now.Truncate(24*time.Hour).Add(-24*time.Hour) ||
		artifact.ObservedThrough != now.Truncate(24*time.Hour).Add(-time.Nanosecond) ||
		artifact.Metrics.ObservableBPS != 23*10_000/24 || artifact.Metrics.Signals != 0 ||
		artifact.Metrics.Fills != 0 || artifact.Metrics.VersusHoldMicros != 0 ||
		artifact.Metrics.MaxDrawdownMicros != 0 {
		t.Fatalf("unexpected recorded observations: %+v", artifact)
	}
	later, err := buildResearchObservations(policy, directory, now.Add(22*time.Hour))
	if err != nil || artifact.ContentSHA256 != later.ContentSHA256 ||
		!artifact.ObservedThrough.Equal(later.ObservedThrough) {
		t.Fatalf("regeneration renewed evidence: %+v, %v", later, err)
	}
	if err := verifyResearchObservations(artifact, policy, directory, now); err != nil {
		t.Fatal(err)
	}
	altered := artifact
	altered.Metrics.Signals++
	altered, err = altered.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyResearchObservations(altered, policy, directory, now); err == nil {
		t.Fatal("self-consistent invented metrics passed journal verification")
	}
	changedPolicy := policy
	changedPolicy.FeeLamports++
	if err := verifyResearchObservations(artifact, changedPolicy, directory, now); err == nil {
		t.Fatal("another policy's observations were accepted")
	}
	changedDirectory := privateTestDirectory(t)
	writeShadowResearchDay(t, changedDirectory, policy, "2026-09-04", []uint64{101_000_000})
	if err := verifyResearchObservations(artifact, policy, changedDirectory, now); err == nil {
		t.Fatal("changed journal provenance was accepted")
	}
	if _, err := buildResearchObservations(policy, directory, now.Add(24*time.Hour)); err == nil {
		t.Fatal("missing yesterday silently reused an older day")
	}
	var output bytes.Buffer
	if err := runResearchObservations([]string{"--policy", writeShadowPolicy(t, policy), "--journal-dir", directory}, &output, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	var decoded researchpacket.RecordedObservations
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil || decoded.ContentSHA256 != artifact.ContentSHA256 {
		t.Fatalf("CLI artifact differs: %+v, %v", decoded, err)
	}
	if err := runResearchObservations([]string{"--policy", "relative", "--journal-dir", directory}, io.Discard, func() time.Time { return now }); err == nil {
		t.Fatal("relative policy path was accepted")
	}
	otherCluster := policy
	otherCluster.Cluster = "devnet"
	if _, err := buildResearchObservations(otherCluster, directory, now); err == nil {
		t.Fatal("non-Mainnet policy was accepted")
	}
	if _, err := buildResearchObservations(policy, directory, time.Time{}); err == nil {
		t.Fatal("missing clock was accepted")
	}
}

func TestResearchObservationsRejectSparseIncompleteAndUnpairedJournals(t *testing.T) {
	policy := validShadowResearchPolicy()
	now := time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC)
	for _, closed := range []bool{false, true} {
		directory := privateTestDirectory(t)
		writeShadowSearchJournal(t, directory, policy, "2026-09-04", []uint64{100_000_000}, closed)
		if _, err := buildResearchObservations(policy, directory, now); err == nil {
			t.Fatalf("sparse/incomplete journal was accepted, closed=%t", closed)
		}
	}
	source := privateTestDirectory(t)
	writeShadowResearchDay(t, source, policy, "2026-09-04", []uint64{100_000_000})
	records, err := journal.ReadRecords(filepath.Join(source, "shadow-2026-09-04.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	directory := privateTestDirectory(t)
	store, err := journal.Open(filepath.Join(directory, "shadow-2026-09-04.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		payload := record.Payload
		if record.Type == shadow.EventWaiting {
			var tick shadow.Tick
			if err := json.Unmarshal(payload, &tick); err != nil {
				t.Fatal(err)
			}
			tick.SecondaryPrice = nil
			payload, err = json.Marshal(tick)
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.Append(record.At, record.Type, record.ActionID, payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := buildResearchObservations(policy, directory, now); err == nil {
		t.Fatal("hash-valid journal without paired source evidence was accepted")
	}
}
