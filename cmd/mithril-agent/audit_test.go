package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/journal"
)

func TestAuditSnapshotBindsStatusToTheVerifiedJournal(t *testing.T) {
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	configPath, journalPath, owner := writeAuditFixture(t, now)

	var output bytes.Buffer
	if err := runAuditSnapshot(
		[]string{"--config", configPath}, &output, func() time.Time { return now },
	); err != nil {
		t.Fatal(err)
	}
	var snapshot auditSnapshot
	if err := json.Unmarshal(output.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Format != auditSnapshotFormat || snapshot.CapturedAt != now ||
		snapshot.Profile == "" || snapshot.ProfileVersion == 0 ||
		len(snapshot.ProfileSHA256) != 64 || snapshot.Cluster != "devnet" {
		t.Fatalf("audit identity = %+v", snapshot)
	}
	if snapshot.Status.ObservedAt != now.Add(-time.Second) ||
		snapshot.Status.RunnerState != "recent" || snapshot.Status.Decision != "stopped" ||
		snapshot.Status.ControlMode != control.ModeNoNewActions || snapshot.Status.AttentionRequired ||
		len(snapshot.Status.SHA256) != 64 {
		t.Fatalf("audit status = %+v", snapshot.Status)
	}
	if snapshot.Journal.Format != journal.Format || snapshot.Journal.Records != 2 ||
		snapshot.Journal.SendStarted != 1 || snapshot.Journal.Submitted != 1 ||
		len(snapshot.Journal.FileSHA256) != 64 || len(snapshot.Journal.ChainHeadSHA256) != 64 {
		t.Fatalf("audit journal = %+v", snapshot.Journal)
	}
	for _, private := range []string{
		configPath, journalPath, owner, "private-action-id", "private-payload",
		"/private/submitter-key.json",
	} {
		if strings.Contains(output.String(), private) {
			t.Fatalf("audit snapshot disclosed %q: %s", private, output.String())
		}
	}
}

func TestAuditSnapshotRejectsStatusThatDoesNotMatchJournal(t *testing.T) {
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	configPath, journalPath, _ := writeAuditFixture(t, now)
	statusPath := operatorstatus.Path(journalPath)
	status, err := operatorstatus.Read(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	status.Journal.Records--
	status.Journal.ActiveRecords--
	if err := operatorstatus.Write(statusPath, status); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = runAuditSnapshot(
		[]string{"--config", configPath}, &output, func() time.Time { return now },
	)
	if err == nil || !strings.Contains(err.Error(), "does not match the verified journal") {
		t.Fatalf("audit mismatch error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("failed audit wrote output: %q", output.String())
	}
}

func TestAuditSnapshotPreservesActionableControlState(t *testing.T) {
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	for name, controlStatus := range map[string]control.Status{
		"recovery pending": {
			Mode: control.ModeNoNewActions, RecoveryPending: true,
		},
		"finalized failure": {
			Mode: control.ModeNoNewActions, TerminalActionID: strings.Repeat("a", 64),
			TerminalOutcome: "failed",
		},
	} {
		t.Run(name, func(t *testing.T) {
			configPath, journalPath, _ := writeAuditFixture(t, now)
			statusPath := operatorstatus.Path(journalPath)
			status, err := operatorstatus.Read(statusPath)
			if err != nil {
				t.Fatal(err)
			}
			status.Control = controlStatus
			if err := operatorstatus.Write(statusPath, status); err != nil {
				t.Fatal(err)
			}

			var output bytes.Buffer
			if err := runAuditSnapshot(
				[]string{"--config", configPath}, &output, func() time.Time { return now },
			); err != nil {
				t.Fatal(err)
			}
			var snapshot auditSnapshot
			if err := json.Unmarshal(output.Bytes(), &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Status.RecoveryPending != controlStatus.RecoveryPending ||
				snapshot.Status.TerminalOutcome != controlStatus.TerminalOutcome ||
				!snapshot.Status.AttentionRequired {
				t.Fatalf("audit control status = %+v", snapshot.Status)
			}
			if strings.Contains(output.String(), controlStatus.TerminalActionID) &&
				controlStatus.TerminalActionID != "" {
				t.Fatal("audit snapshot disclosed the terminal action ID")
			}
		})
	}
}

func TestAuditSnapshotRejectsStatusThatDoesNotMatchStrategy(t *testing.T) {
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	configPath, journalPath, _ := writeAuditFixture(t, now)
	statusPath := operatorstatus.Path(journalPath)
	status, err := operatorstatus.Read(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	status.Strategy.InputAmount++
	if err := operatorstatus.Write(statusPath, status); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = runAuditSnapshot(
		[]string{"--config", configPath}, &output, func() time.Time { return now },
	)
	if err == nil || !strings.Contains(err.Error(), "does not match the configured strategy") {
		t.Fatalf("audit strategy mismatch error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("failed audit wrote output: %q", output.String())
	}
}

func TestAuditSnapshotRefusesAnActiveJournal(t *testing.T) {
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	configPath, journalPath, _ := writeAuditFixture(t, now)
	store, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var output bytes.Buffer
	err = runAuditSnapshot(
		[]string{"--config", configPath}, &output, func() time.Time { return now },
	)
	if err == nil || !strings.Contains(err.Error(), "stop the runner") {
		t.Fatalf("active audit error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("active audit wrote output: %q", output.String())
	}
}

func TestAuditCommandUsageAndArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"unknown":         {"audit", "unknown"},
		"missing config":  {"audit", "snapshot"},
		"relative config": {"audit", "snapshot", "--config", "config.json"},
		"extra argument":  {"audit", "snapshot", "--config", "/tmp/config.json", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(args, &bytes.Buffer{}); err == nil {
				t.Fatal("invalid audit command was accepted")
			}
		})
	}
	var output bytes.Buffer
	if err := run([]string{"audit", "--help"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "audit snapshot --config ABSOLUTE_PATH") {
		t.Fatalf("audit help = %q", output.String())
	}
}

func writeAuditFixture(t *testing.T, now time.Time) (configPath, journalPath, owner string) {
	t.Helper()
	dir := t.TempDir()
	owner = "3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh"
	profile := testSwapProfile(owner)
	cfg := config{Swap: &profile}
	cfg.Journal.Path = filepath.Join(dir, "events.jsonl")
	cfg.Submitter.PrivateKeyPath = "/private/submitter-key.json"
	configPath = filepath.Join(dir, "config.json")
	writeJSON(t, configPath, cfg)

	store, err := journal.Open(cfg.Journal.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"swap.send_started", "swap.submitted"} {
		if _, err := store.Append(
			now.Add(-time.Minute), event, "private-action-id",
			map[string]string{"secret": "private-payload"},
		); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	stats, err := store.Stats()
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	strategy, err := strategyProjection(profile, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := operatorstatus.Write(operatorstatus.Path(cfg.Journal.Path), operatorstatus.Snapshot{
		Version: operatorstatus.Version, ObservedAt: now.Add(-time.Second),
		Profile: profile.Name, ProfileVersion: profile.Version, Cluster: profile.Cluster,
		Result: operatorstatus.Result{Decision: "stopped"}, Journal: stats,
		Control:  control.Status{Mode: control.ModeNoNewActions},
		Strategy: strategy,
	}); err != nil {
		t.Fatal(err)
	}
	return configPath, cfg.Journal.Path, owner
}
