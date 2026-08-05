package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
)

func TestJournalVerifyCommandUsesAllowlistedOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	store, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(
		time.Now(),
		"private.record.type",
		"private-action-id",
		map[string]string{"token": "private-payload"},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := run([]string{"journal", "verify", "--path", path}, &output); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		path, "private.record.type", "private-action-id", "private-payload",
	} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("journal summary disclosed %q: %s", secret, output.String())
		}
	}
	var summary map[string]any
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary) != 8 || summary["status"] != "valid" ||
		summary["format"] != journal.Format || summary["records"] != float64(1) {
		t.Fatalf("journal summary = %v", summary)
	}
	for _, field := range []string{
		"bytes", "chain_head_sha256", "file_sha256",
		"send_started_records", "submitted_records",
	} {
		if _, ok := summary[field]; !ok {
			t.Fatalf("journal summary is missing %q: %v", field, summary)
		}
	}
}

func TestJournalVerifyCommandErrorsDoNotWriteSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	store, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var output bytes.Buffer
	err = run([]string{"journal", "verify", "--path", path}, &output)
	if err == nil || !strings.Contains(err.Error(), "stop the runner") {
		t.Fatalf("active journal command error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("failed journal verification wrote output: %q", output.String())
	}
}

func TestJournalCommandUsageAndArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"unknown":        {"journal", "unknown"},
		"missing path":   {"journal", "verify"},
		"extra argument": {"journal", "verify", "--path", "/tmp/events.jsonl", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(args, io.Discard); err == nil {
				t.Fatal("invalid journal command was accepted")
			}
		})
	}
	var output bytes.Buffer
	if err := run([]string{"journal", "--help"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "journal verify --path ABSOLUTE_PATH") {
		t.Fatalf("journal help = %q", output.String())
	}
}

func TestJournalVerifyCommandPropagatesWriterFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	store, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	want := errors.New("write failed")
	err = run(
		[]string{"journal", "verify", "--path", path},
		writerFunc(func([]byte) (int, error) { return 0, want }),
	)
	if !errors.Is(err, want) {
		t.Fatalf("journal writer error = %v", err)
	}
}

type writerFunc func([]byte) (int, error)

func (write writerFunc) Write(data []byte) (int, error) {
	return write(data)
}
