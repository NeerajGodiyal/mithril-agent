package policyauthority

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
)

func TestStrategyJournalStatusVerifiedSnapshot(t *testing.T) {
	path, seed, authority, recovery, next, acquisition, at := strategyDecisionFixture(t)
	for _, committed := range []bool{false, true} {
		if committed {
			if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, at); err != nil {
				t.Fatal(err)
			}
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		records, err := journal.ReadRecords(path)
		if err != nil {
			t.Fatal(err)
		}
		status, err := ReadStrategyJournalStatus(path, authority, recovery, at)
		if err != nil {
			t.Fatal(err)
		}
		wantDecision := ""
		if committed {
			wantDecision = records[len(records)-1].Hash
		}
		if status.State == nil || status.State.Pending() != committed || status.HeadSHA256 != records[len(records)-1].Hash || status.PendingDecisionSHA256 != wantDecision {
			t.Fatalf("wrong verified status for committed=%v: %+v", committed, status)
		}
		replayed, err := ReadStrategyJournal(path, authority, recovery, at)
		if err != nil || !reflect.DeepEqual(replayed, status.State) {
			t.Fatalf("status diverges from public replay: %v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("status read changed journal: %v", err)
		}
	}
	pending, err := ReadStrategyJournalStatus(path, authority, recovery, at)
	if err != nil {
		t.Fatal(err)
	}
	observed := at.Add(time.Second)
	samples := strategyOutcomeSamples(seed, observed)
	if _, err := ObserveStrategyJournal(path, authority, recovery, observed, samples[0], samples[1], samples[2], samples[3]); err != nil {
		t.Fatal(err)
	}
	updated, err := ReadStrategyJournalStatus(path, authority, recovery, observed)
	if err != nil || !updated.State.Pending() || updated.HeadSHA256 == pending.HeadSHA256 || updated.PendingDecisionSHA256 != pending.PendingDecisionSHA256 {
		t.Fatalf("observation replaced pending decision identity: %v", err)
	}
	status, err := ReadStrategyJournalStatus(path, authority, recovery, at.Add(-24*time.Hour))
	if err == nil || status.State != nil || status.HeadSHA256 != "" || status.PendingDecisionSHA256 != "" {
		t.Fatal("invalid chronology exposed unverified status")
	}
	missing := filepath.Join(t.TempDir(), "missing.jsonl")
	if _, err := ReadStrategyJournalStatus(missing, authority, recovery, at); err == nil {
		t.Fatal("missing journal accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("status created missing journal: %v", err)
	}
}
