package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
)

func TestShadowJournalResearchPrefixRemainsReadable(t *testing.T) {
	d, err := newDailyJournal(privateTestDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	at := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	if err := d.Record(at, "test", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := d.publishResearchPrefix(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(d.directory, "shadow-"+dayKey(at)+".jsonl")
	raw, err := os.ReadFile(path + ".prefix.json")
	if err != nil {
		t.Fatal(err)
	}
	var prefix journal.DurablePrefix
	if err := json.Unmarshal(raw, &prefix); err != nil {
		t.Fatal(err)
	}
	for _, appendMore := range []bool{false, true} {
		if appendMore {
			if err := d.Record(at.Add(time.Second), "test", struct{}{}); err != nil {
				t.Fatal(err)
			}
		}
		records, err := journal.ReadDurablePrefix(path, prefix)
		if err != nil || len(records) != 1 || records[0].Hash != prefix.ChainHeadSHA256 {
			t.Fatalf("active writer prefix: records=%v err=%v", records, err)
		}
	}
	if err := d.Record(at.Add(24*time.Hour), "test", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := d.publishResearchPrefix(); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(path + ".prefix.json")
	if err != nil || !bytes.Equal(old, raw) {
		t.Fatal("rollover changed old prefix")
	}
	newPath := filepath.Join(d.directory, "shadow-"+dayKey(at.Add(24*time.Hour))+".jsonl")
	newRaw, err := os.ReadFile(newPath + ".prefix.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(newRaw, &prefix); err != nil {
		t.Fatal(err)
	}
	if records, err := journal.ReadDurablePrefix(newPath, prefix); err != nil || len(records) != 1 {
		t.Fatalf("new day prefix: %v %v", records, err)
	}
}

func TestShadowDriveResearchPrefixOptIn(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "enabled"}[enabled], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				run := cadenceRun(t, func() {})
				defer run.roll.Close()
				run.publishResearchPrefix = enabled
				if err := run.drive(t.Context(), true, io.Discard); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(run.roll.directory, "shadow-"+run.roll.Day()+".jsonl.prefix.json")
				_, err := os.Stat(path)
				if enabled && err != nil || !enabled && !os.IsNotExist(err) {
					t.Fatalf("enabled=%v: %v", enabled, err)
				}
			})
		})
	}
	var output bytes.Buffer
	if err := runShadowRun(t.Context(), []string{"--publish-research-prefix", "--help"}, &output); err != nil || !strings.Contains(output.String(), "--publish-research-prefix") {
		t.Fatalf("prefix flag/help: %v %s", err, output.String())
	}
}

func TestShadowDriveResearchPrefixFailureStopsPublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		run := cadenceRun(t, func() {})
		defer run.roll.Close()
		run.publishResearchPrefix = true
		path := filepath.Join(run.roll.directory, "shadow-"+run.roll.Day()+".jsonl.prefix.json")
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		statusPath := filepath.Join(run.roll.directory, "alerts.json")
		before, beforeErr := os.ReadFile(statusPath)
		var output bytes.Buffer
		if err := run.drive(t.Context(), true, &output); err == nil {
			t.Fatal("publication failure was ignored")
		}
		after, afterErr := os.ReadFile(statusPath)
		if output.Len() != 0 || !bytes.Equal(before, after) || (beforeErr == nil) != (afterErr == nil) {
			t.Fatal("failed prefix publication exposed current status")
		}
		if run.runner.Counts().Ticks != 1 {
			t.Fatal("observation was not retained before publication failure")
		}
	})
}
