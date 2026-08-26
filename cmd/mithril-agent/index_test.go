package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/rootedindex"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func testRootedSource() rootedindex.SourceDescriptor {
	return rootedindex.SourceDescriptor{
		Cluster:             "alpenglow",
		GenesisHash:         "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG",
		AccountsDBRootRunID: "0123abcd",
	}
}

func testClassicRootedSource() rootedindex.SourceDescriptor {
	return rootedindex.SourceDescriptor{
		Cluster:             "mainnet-beta",
		GenesisHash:         solana.MainnetBetaGenesisHash,
		AccountsDBRootRunID: "0123abcd",
	}
}

func beginRootedBatch(t *testing.T, index *rootedindex.Index, sequence, from, through uint64) {
	t.Helper()
	_, err := index.BeginBatch(rootedindex.BatchDescriptor{
		ManifestSequence: sequence, SidecarVersion: rootedindex.SupportedSidecarVersion,
		FromSlot: from, ThroughSlot: through, SHA256: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeRootedFrames(t *testing.T, output *bytes.Buffer, sequence, from, through uint64, events []rootedindex.Event) {
	t.Helper()
	encoder := json.NewEncoder(output)
	if err := encoder.Encode(map[string]any{
		"record_type": rootedindex.SourceRecordType, "source": testRootedSource(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(map[string]any{
		"record_type": rootedindex.StartRecordType, "start": rootedindex.StartDescriptor{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(map[string]any{
		"record_type": rootedindex.BatchRecordType,
		"batch": rootedindex.BatchDescriptor{
			ManifestSequence: sequence, SidecarVersion: rootedindex.SupportedSidecarVersion,
			FromSlot: from, ThroughSlot: through, SHA256: strings.Repeat("a", 64),
		},
	}); err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIndexCLIIngestStatusAndQuery(t *testing.T) {
	dir := t.TempDir()
	events := []rootedindex.Event{
		{
			SchemaVersion: rootedindex.SchemaVersion,
			Cursor:        rootedindex.Cursor{Slot: 50, Ordinal: 0},
			Kind:          "transaction_executed",
			Transaction: &rootedindex.Transaction{
				Signature: strings.Repeat("1", 64), Message: []byte("message"),
				AccountKeys: []string{"ComputeBudget111111111111111111111111111111"},
				Succeeded:   true, Logs: []string{"Program success"},
			},
		},
		{
			SchemaVersion: rootedindex.SchemaVersion,
			Cursor:        rootedindex.Cursor{Slot: 50, Ordinal: 1},
			Kind:          "account_updated",
			Account: &rootedindex.AccountUpdate{
				Pubkey:   "11111111111111111111111111111111",
				Owner:    "ComputeBudget111111111111111111111111111111",
				Lamports: 1,
				Data:     []byte("indexed"),
			},
		},
		{
			SchemaVersion: rootedindex.SchemaVersion,
			Cursor:        rootedindex.Cursor{Slot: 50, Ordinal: 2},
			Kind:          "slot_rooted",
			Root: &rootedindex.RootedSlot{
				ParentSlot: 49, Bankhash: "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG",
				TransactionCount: 1, AccountCount: 1,
			},
		},
	}
	var input bytes.Buffer
	writeRootedFrames(t, &input, 1, 50, 50, events)
	var output bytes.Buffer
	if err := runIndex(context.Background(), []string{
		"ingest", "--dir", dir, "--cluster", "alpenglow",
		"--genesis-hash", testRootedSource().GenesisHash, "--json",
	}, &input, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"stored":3`) {
		t.Fatalf("ingest output = %s", output.String())
	}
	input.Reset()
	writeRootedFrames(t, &input, 1, 50, 50, events)
	output.Reset()
	if err := runIndex(context.Background(), []string{
		"ingest", "--dir", dir, "--cluster", "alpenglow",
		"--genesis-hash", testRootedSource().GenesisHash,
	}, &input, &output); err == nil || !strings.Contains(err.Error(), "durable cursor") {
		t.Fatalf("full replay error = %v", err)
	}

	output.Reset()
	if err := runIndex(context.Background(), []string{"status", "--dir", dir}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Transactions: 1") ||
		!strings.Contains(output.String(), "Account updates: 1") ||
		!strings.Contains(output.String(), "Last root: 50:2") {
		t.Fatalf("status output = %s", output.String())
	}

	output.Reset()
	if err := runIndex(context.Background(), []string{"query", "--dir", dir, "--include-data"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	var query indexAccountQueryResult
	if err := json.Unmarshal(output.Bytes(), &query); err != nil {
		t.Fatal(err)
	}
	if query.Provenance != rootedindex.RootedProvenance || query.Finality != rootedindex.RootedFinality ||
		len(query.Results) != 1 || string(query.Results[0].Data) != "indexed" {
		t.Fatalf("query results = %+v", query)
	}

	output.Reset()
	if err := runIndex(context.Background(), []string{
		"transactions", "--dir", dir, "--include-payload",
	}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	var transactions indexTransactionQueryResult
	if err := json.Unmarshal(output.Bytes(), &transactions); err != nil {
		t.Fatal(err)
	}
	if transactions.Provenance != rootedindex.RootedProvenance ||
		transactions.Finality != rootedindex.RootedFinality ||
		len(transactions.Results) != 1 || string(transactions.Results[0].Message) != "message" {
		t.Fatalf("transaction results = %+v", transactions)
	}
}

func TestIndexCLIUsageAndValidation(t *testing.T) {
	var output bytes.Buffer
	if err := runIndex(context.Background(), []string{"help"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "index ingest") || !strings.Contains(output.String(), "index doctor") || !strings.Contains(output.String(), "No command loads a wallet") {
		t.Fatalf("usage = %s", output.String())
	}
	if err := runIndex(context.Background(), []string{"query", "--dir", t.TempDir(), "--after", "bad"}, strings.NewReader(""), &output); err == nil {
		t.Fatal("invalid cursor was accepted")
	}
	output.Reset()
	if err := runIndex(context.Background(), []string{"doctor", "--dir", "relative"}, strings.NewReader(""), &output); err == nil || output.Len() != 0 {
		t.Fatalf("invalid doctor path output = %q, err = %v", output.String(), err)
	}
}

func TestIndexCLIWorkspaceDerivesSourcePathAndFilter(t *testing.T) {
	root := t.TempDir()
	accounts := filepath.Join(root, "accounts-root")
	if err := os.Mkdir(accounts, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceDir := filepath.Join(root, "workspace")
	if err := runProgramWorkspaceCreate([]string{
		"--dir", workspaceDir, "--program", "ComputeBudget111111111111111111111111111111",
		"--cluster", "alpenglow", "--genesis-hash", testRootedSource().GenesisHash,
		"--node-rpc", "http://127.0.0.1:8899", "--accounts", accounts,
	}, io.Discard); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(workspaceDir, programWorkspaceFile)
	rootEvent := rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion, Cursor: rootedindex.Cursor{Slot: 1},
		Kind: "slot_rooted", Root: &rootedindex.RootedSlot{Bankhash: testRootedSource().GenesisHash},
	}
	var input, output bytes.Buffer
	writeRootedFrames(t, &input, 1, 1, 1, []rootedindex.Event{rootEvent})
	if err := runIndex(t.Context(), []string{
		"ingest", "--workspace", workspace, "--kind", "state", "--json",
	}, &input, &output); err != nil {
		t.Fatal(err)
	}
	status, err := rootedindex.ReadCompleteStatus(filepath.Join(workspaceDir, "state-index"))
	if err != nil {
		t.Fatal(err)
	}
	if status.Source != testRootedSource() || status.Filter.Owner != "ComputeBudget111111111111111111111111111111" {
		t.Fatalf("workspace-derived index = %+v", status)
	}
}

func TestIndexDoctorReadyAndFailedRecoveryIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	index, err := rootedindex.Open(dir, testRootedSource(), rootedindex.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginRootedBatch(t, index, 1, 50, 50)
	if _, err := index.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion,
		Cursor:        rootedindex.Cursor{Slot: 50, Ordinal: 0},
		Kind:          "slot_rooted",
		Root: &rootedindex.RootedSlot{
			ParentSlot: 49, Bankhash: "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runIndex(context.Background(), []string{"doctor", "--dir", dir, "--json"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	var ready indexDoctorResult
	if err := json.Unmarshal(output.Bytes(), &ready); err != nil {
		t.Fatal(err)
	}
	if !ready.Ready || ready.Status != "ready" || ready.Index == nil || ready.Index.Roots != 1 {
		t.Fatalf("ready doctor = %+v", ready)
	}

	journalPath := filepath.Join(dir, "events.jsonl")
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), before...)
	tampered[len(tampered)/2] ^= 1
	if err := os.WriteFile(journalPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	err = runIndex(context.Background(), []string{"doctor", "--dir", dir}, strings.NewReader(""), &output)
	if !errors.Is(err, errIndexNeedsAttention) ||
		!strings.Contains(output.String(), "Keep the existing directory unchanged") ||
		!strings.Contains(output.String(), "create a new private directory") {
		t.Fatalf("failed doctor output = %q, err = %v", output.String(), err)
	}
	after, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, tampered) {
		t.Fatal("doctor changed the failed index")
	}
}

func TestIndexDoctorExplainsMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	var output bytes.Buffer
	err := runIndex(context.Background(), []string{"doctor", "--dir", dir, "--json"}, strings.NewReader(""), &output)
	if !errors.Is(err, errIndexNeedsAttention) {
		t.Fatalf("missing doctor error = %v", err)
	}
	var result indexDoctorResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Ready || result.Reason != "index directory does not exist" || len(result.NextSteps) == 0 {
		t.Fatalf("missing doctor = %+v", result)
	}
}

func TestIndexDoctorExplainsEmptyWorkspaceDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateDirectory(dir); err != nil {
		t.Fatalf("private test directory: %v", err)
	}
	var output bytes.Buffer
	err := runIndex(context.Background(), []string{"doctor", "--dir", dir, "--json"}, strings.NewReader(""), &output)
	if !errors.Is(err, errIndexNeedsAttention) {
		t.Fatalf("empty doctor error = %v", err)
	}
	var result indexDoctorResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Ready || result.Reason != "index has not been initialized" ||
		len(result.NextSteps) == 0 || !strings.Contains(result.NextSteps[0], "ingest") {
		t.Fatalf("empty doctor = %+v", result)
	}

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	err = runIndex(context.Background(), []string{"doctor", "--dir", dir, "--json"}, strings.NewReader(""), &output)
	if !errors.Is(err, errIndexNeedsAttention) {
		t.Fatalf("open empty doctor error = %v", err)
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Reason == "index has not been initialized" {
		t.Fatalf("open empty directory was treated as trusted: %+v", result)
	}
}

func TestIndexDoctorReportsInterruptedSlotForResume(t *testing.T) {
	dir := t.TempDir()
	index, err := rootedindex.Open(dir, testRootedSource(), rootedindex.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginRootedBatch(t, index, 1, 51, 51)
	if _, err := index.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion,
		Cursor:        rootedindex.Cursor{Slot: 51, Ordinal: 0},
		Kind:          "account_updated",
		Account: &rootedindex.AccountUpdate{
			Pubkey:   "11111111111111111111111111111111",
			Owner:    "ComputeBudget111111111111111111111111111111",
			Lamports: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	err = runIndex(context.Background(), []string{"doctor", "--dir", dir, "--json"}, strings.NewReader(""), &output)
	if !errors.Is(err, errIndexNeedsAttention) {
		t.Fatalf("doctor error = %v", err)
	}
	var result indexDoctorResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Ready || result.Index == nil || result.Index.Complete || result.Index.LastCursor == nil ||
		!strings.Contains(result.Reason, "root marker") || len(result.NextSteps) == 0 {
		t.Fatalf("interrupted doctor = %+v", result)
	}
}

func TestIndexDoctorPreservesNonzeroStartWhenBatchHasNoEvents(t *testing.T) {
	dir := t.TempDir()
	index, err := rootedindex.Open(dir, testRootedSource(), rootedindex.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	start := rootedindex.Cursor{Slot: 50, Ordinal: 0}
	if err := index.BeginStream(&start); err != nil {
		t.Fatal(err)
	}
	beginRootedBatch(t, index, 2, 51, 51)
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	err = runIndex(context.Background(), []string{"doctor", "--dir", dir, "--json"}, strings.NewReader(""), &output)
	if !errors.Is(err, errIndexNeedsAttention) {
		t.Fatalf("doctor error = %v", err)
	}
	var result indexDoctorResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	steps := strings.Join(result.NextSteps, " ")
	if result.Index == nil || result.Index.LastCursor != nil || result.Index.LastBatch == nil ||
		!strings.Contains(steps, "index.start.after") || strings.Contains(steps, "replay the retained feed from the beginning") {
		t.Fatalf("batch-only recovery advice = %+v", result)
	}
}

func TestIndexCLIRejectsEmptyFirstIngestAndAcceptsEmptyResume(t *testing.T) {
	dir := t.TempDir()
	var output bytes.Buffer
	if err := runIndex(context.Background(), []string{
		"ingest", "--dir", dir, "--cluster", "alpenglow",
		"--genesis-hash", testRootedSource().GenesisHash,
	}, strings.NewReader(""), &output); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty first ingest error = %v", err)
	}

	root := rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion,
		Cursor:        rootedindex.Cursor{Slot: 0, Ordinal: 0},
		Kind:          "slot_rooted",
		Root: &rootedindex.RootedSlot{
			Bankhash: "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG",
		},
	}
	var input bytes.Buffer
	writeRootedFrames(t, &input, 1, 0, 0, []rootedindex.Event{root})
	output.Reset()
	if err := runIndex(context.Background(), []string{
		"ingest", "--dir", dir, "--cluster", "alpenglow",
		"--genesis-hash", testRootedSource().GenesisHash,
	}, &input, &output); err != nil {
		t.Fatalf("retry after empty first ingest: %v", err)
	}
	if !strings.Contains(output.String(), "Rooted events indexed: 1") {
		t.Fatalf("retry output = %s", output.String())
	}

	input.Reset()
	if err := json.NewEncoder(&input).Encode(map[string]any{
		"record_type": rootedindex.SourceRecordType, "source": testRootedSource(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(&input).Encode(map[string]any{
		"record_type": rootedindex.StartRecordType,
		"start":       rootedindex.StartDescriptor{After: &rootedindex.Cursor{Slot: 0, Ordinal: 0}},
	}); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := runIndex(context.Background(), []string{
		"ingest", "--dir", dir, "--cluster", "alpenglow",
		"--genesis-hash", testRootedSource().GenesisHash,
	}, &input, &output); err != nil {
		t.Fatalf("empty resume page: %v", err)
	}
	if !strings.Contains(output.String(), "Rooted events indexed: 0") {
		t.Fatalf("empty resume output = %s", output.String())
	}
}

func TestIndexCLIRejectsMidBatchStartForNewIndex(t *testing.T) {
	var input bytes.Buffer
	encoder := json.NewEncoder(&input)
	root := rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion,
		Cursor:        rootedindex.Cursor{Slot: 50, Ordinal: 1},
		Kind:          "slot_rooted",
		Root: &rootedindex.RootedSlot{
			ParentSlot: 49, Bankhash: testRootedSource().GenesisHash, AccountCount: 1,
		},
	}
	for _, frame := range []any{
		map[string]any{
			"record_type": rootedindex.SourceRecordType, "source": testRootedSource(),
		},
		map[string]any{
			"record_type": rootedindex.StartRecordType,
			"start":       rootedindex.StartDescriptor{After: &rootedindex.Cursor{Slot: 50}},
		},
		map[string]any{
			"record_type": rootedindex.BatchRecordType,
			"batch": rootedindex.BatchDescriptor{
				ManifestSequence: 1, SidecarVersion: rootedindex.SupportedSidecarVersion,
				FromSlot: 50, ThroughSlot: 50, SHA256: strings.Repeat("a", 64),
			},
		},
	} {
		if err := encoder.Encode(frame); err != nil {
			t.Fatal(err)
		}
	}
	if err := encoder.Encode(root); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	err := runIndex(t.Context(), []string{
		"ingest", "--dir", dir, "--cluster", "alpenglow",
		"--genesis-hash", testRootedSource().GenesisHash,
	}, &input, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "inside a selected batch") {
		t.Fatalf("mid-batch ingest error = %v", err)
	}
	status, statusErr := rootedindex.ReadStatus(dir)
	if statusErr != nil || status.Complete {
		t.Fatalf("mid-batch status = %+v, %v", status, statusErr)
	}

	input.Reset()
	writeRootedFrames(t, &input, 1, 50, 50, []rootedindex.Event{{
		SchemaVersion: rootedindex.SchemaVersion,
		Cursor:        rootedindex.Cursor{Slot: 50},
		Kind:          "account_updated",
		Account: &rootedindex.AccountUpdate{
			Pubkey:   "11111111111111111111111111111111",
			Owner:    "ComputeBudget111111111111111111111111111111",
			Lamports: 1,
		},
	}, root})
	if err := runIndex(t.Context(), []string{
		"ingest", "--dir", dir, "--cluster", "alpenglow",
		"--genesis-hash", testRootedSource().GenesisHash,
	}, &input, io.Discard); err != nil {
		t.Fatalf("retry full retained stream after rejected cursor: %v", err)
	}
	if status, err := rootedindex.ReadCompleteStatus(dir); err != nil || status.Accounts != 1 || status.Roots != 1 {
		t.Fatalf("retried status = %+v, %v", status, err)
	}
}

func TestIndexMCPConfigBindsOnePrivateIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	index, err := rootedindex.Open(dir, testRootedSource(), rootedindex.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginRootedBatch(t, index, 1, 1, 1)
	if _, err := index.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion,
		Cursor:        rootedindex.Cursor{Slot: 1, Ordinal: 0},
		Kind:          "slot_rooted",
		Root: &rootedindex.RootedSlot{
			Bankhash: "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runIndex(context.Background(), []string{
		"mcp-config", "--dir", dir, "--name", "program-state",
	}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, `"program-state"`) ||
		!strings.Contains(text, `"index"`) || !strings.Contains(text, `"mcp"`) ||
		!json.Valid(output.Bytes()) {
		t.Fatalf("MCP config = %s", text)
	}
	if err := runIndex(context.Background(), []string{
		"mcp-config", "--dir", dir, "--name", "bad name",
	}, strings.NewReader(""), &output); err == nil {
		t.Fatal("invalid MCP server name was accepted")
	}
}
