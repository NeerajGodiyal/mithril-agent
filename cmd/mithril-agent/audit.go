package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
)

const (
	auditSnapshotFormat = "mithril-agent/audit-snapshot-v1"
	auditUsage          = `Usage:
  mithril-agent audit snapshot --config ABSOLUTE_PATH`
)

type auditSnapshot struct {
	Format         string       `json:"format"`
	CapturedAt     time.Time    `json:"captured_at"`
	AgentVersion   string       `json:"agent_version"`
	Profile        string       `json:"profile"`
	ProfileVersion uint32       `json:"profile_version"`
	ProfileSHA256  string       `json:"profile_sha256"`
	Cluster        string       `json:"cluster"`
	Status         auditStatus  `json:"status"`
	Journal        auditJournal `json:"journal"`
}

type auditStatus struct {
	ObservedAt        time.Time `json:"observed_at"`
	SHA256            string    `json:"sha256"`
	RunnerState       string    `json:"runner_state"`
	Decision          string    `json:"decision"`
	ControlMode       string    `json:"control_mode"`
	RecoveryPending   bool      `json:"recovery_pending"`
	TerminalOutcome   string    `json:"terminal_outcome,omitempty"`
	AttentionRequired bool      `json:"attention_required"`
}

type auditJournal struct {
	Format          string `json:"format"`
	Records         int    `json:"records"`
	Bytes           int64  `json:"bytes"`
	ChainHeadSHA256 string `json:"chain_head_sha256,omitempty"`
	FileSHA256      string `json:"file_sha256"`
	SendStarted     int    `json:"send_started_records"`
	Submitted       int    `json:"submitted_records"`
}

func runAudit(args []string, output io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, err := fmt.Fprintln(output, auditUsage)
		return err
	}
	if args[0] != "snapshot" {
		return fmt.Errorf("unknown audit command %q; run mithril-agent audit --help", args[0])
	}
	return runAuditSnapshot(args[1:], output, time.Now)
}

func runAuditSnapshot(args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("audit snapshot", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "absolute agent config path")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, auditUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" {
		return errors.New("audit snapshot requires --config")
	}
	if !filepath.IsAbs(*configPath) || filepath.Clean(*configPath) != *configPath {
		return errors.New("audit config path must be a clean absolute path")
	}
	cfg, err := readConfig(*configPath)
	if err != nil {
		return errors.New("audit config is unavailable or invalid")
	}
	profile, profileVersion, cluster, profileSHA256, err := cfg.activeProfile()
	if err != nil {
		return fmt.Errorf("audit profile: %w", err)
	}
	if cfg.Journal.Path == "" {
		return errors.New("audit config has no journal path")
	}

	statusPath := operatorstatus.Path(cfg.Journal.Path)
	status, before, err := readAuditStatus(statusPath)
	if err != nil {
		return err
	}
	verified, err := verifyJournal(cfg.Journal.Path)
	if err != nil {
		return err
	}
	_, after, err := readAuditStatus(statusPath)
	if err != nil {
		return err
	}
	if !bytes.Equal(before, after) {
		return errors.New("operator status changed while capturing the audit snapshot")
	}
	if status.Profile != profile || status.ProfileVersion != profileVersion ||
		status.Cluster != cluster {
		return errors.New("operator status does not match the configured profile")
	}
	if cfg.Swap != nil {
		strategy, err := strategyProjection(*cfg.Swap, *configPath)
		if err != nil || status.Strategy != strategy {
			return errors.New("operator status does not match the configured strategy")
		}
	}
	if status.Journal.Records != verified.Records ||
		status.Journal.SendStartedRecords != verified.SendStartedRecords ||
		status.Journal.SubmittedRecords != verified.SubmittedRecords {
		return errors.New("operator status does not match the verified journal")
	}
	capturedAt := now().UTC()
	view, err := operatorstatus.ViewFromSnapshot(status, capturedAt)
	if err != nil {
		return fmt.Errorf("audit operator status: %w", err)
	}
	statusHash := sha256.Sum256(before)
	snapshot := auditSnapshot{
		Format: auditSnapshotFormat, CapturedAt: capturedAt,
		AgentVersion: agentVersion(), Profile: profile,
		ProfileVersion: profileVersion, ProfileSHA256: profileSHA256, Cluster: cluster,
		Status: auditStatus{
			ObservedAt: status.ObservedAt, SHA256: hex.EncodeToString(statusHash[:]),
			RunnerState: view.RunnerState, Decision: view.Result.Decision,
			ControlMode: view.Control.Mode, RecoveryPending: view.Control.RecoveryPending,
			TerminalOutcome:   view.Control.TerminalOutcome,
			AttentionRequired: view.AttentionRequired,
		},
		Journal: auditJournal{
			Format: journal.Format, Records: verified.Records, Bytes: verified.Bytes,
			ChainHeadSHA256: verified.ChainHeadSHA256, FileSHA256: verified.FileSHA256,
			SendStarted: verified.SendStartedRecords, Submitted: verified.SubmittedRecords,
		},
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(snapshot)
}

func readAuditStatus(path string) (operatorstatus.Snapshot, []byte, error) {
	data, err := securefile.ReadPrivate(path, maxInputBytes)
	if err != nil {
		return operatorstatus.Snapshot{}, nil, errors.New("operator status is unavailable or unsafe")
	}
	var snapshot operatorstatus.Snapshot
	if err := strictjson.Decode(data, &snapshot); err != nil {
		return operatorstatus.Snapshot{}, nil, errors.New("operator status is not valid JSON")
	}
	if err := operatorstatus.ValidateSnapshot(snapshot); err != nil {
		return operatorstatus.Snapshot{}, nil, err
	}
	return snapshot, data, nil
}
