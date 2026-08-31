package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/indexmcp"
	"github.com/Overclock-Validator/mithril-agent/rootedindex"
)

const indexUsage = `Usage:
  mithril-agent index ingest --dir ABSOLUTE_PATH --cluster NAME --genesis-hash HASH \
    [--owner ADDRESS] [--account ADDRESS] [--mention ADDRESS] [--json]
  mithril-agent index ingest --workspace ABSOLUTE_WORKSPACE_JSON --kind state|activity [--json]
  mithril-agent index doctor --dir ABSOLUTE_PATH [--max-record-age DURATION] [--json]
  mithril-agent index status --dir ABSOLUTE_PATH [--json]
  mithril-agent index query --dir ABSOLUTE_PATH [--owner ADDRESS] [--account ADDRESS] \
    [--after SLOT:ORDINAL] [--limit N] [--include-data] [--json]
  mithril-agent index transactions --dir ABSOLUTE_PATH [--signature SIGNATURE] \
    [--mention ADDRESS] [--after SLOT:ORDINAL] [--limit N] [--include-payload] [--json]
  mithril-agent index mcp --dir ABSOLUTE_PATH [--max-record-age DURATION]
  mithril-agent index mcp-config --dir ABSOLUTE_PATH --name NAME

Ingest reads Mithril's framed rooted-event JSONL from standard input into a
private, hash-chained local index. The expected cluster, genesis, source
AccountsDB lineage, selected sidecar hashes, and owner/account filter are
permanently bound to a new index and must match when it is reopened. Exact
replays are safe. Raw events, mixed sources, skipped batches, conflicting
cursors, broken slot lineage, malformed input, and changed blobs fail closed.

Doctor, status, query, and MCP are read-only. While ingest is active they verify
and read only its latest completely published manifest batch; they never expose
an in-progress batch. Stop ingest only for an exact recovery or archival view.
Doctor gives a fail-closed recovery plan and never edits or deletes an index.
Query returns newest matching account updates first and is capped at 10,000
results. MCP is local stdio only, requires a private mode-0700 index, caps each
query at 1,000 results, and omits raw payloads. Its client is authorized by the
operator's OS account and explicit MCP client configuration. Local MCP opens no
listener. No command loads a wallet, signs, submits, or contacts a network.`

func runIndex(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, err := fmt.Fprintln(output, indexUsage)
		return err
	}
	switch args[0] {
	case "ingest":
		return runIndexIngest(ctx, args[1:], input, output)
	case "doctor":
		return runIndexDoctor(args[1:], output)
	case "status":
		return runIndexStatus(args[1:], output)
	case "query":
		return runIndexQuery(args[1:], output)
	case "transactions":
		return runIndexTransactions(args[1:], output)
	case "mcp":
		return runIndexMCP(ctx, args[1:], input, output)
	case "mcp-config":
		return runIndexMCPConfig(args[1:], output)
	default:
		return errors.New("index requires the ingest, doctor, status, query, transactions, mcp, or mcp-config subcommand")
	}
}

var errIndexNeedsAttention = errors.New("rooted index needs attention; follow the recovery steps above")

type indexDoctorResult struct {
	Status    string              `json:"status"`
	Ready     bool                `json:"ready"`
	Reason    string              `json:"reason,omitempty"`
	Index     *rootedindex.Status `json:"index,omitempty"`
	NextSteps []string            `json:"next_steps,omitempty"`
}

type indexAccountQueryResult struct {
	Provenance string               `json:"provenance"`
	Finality   string               `json:"finality"`
	Order      string               `json:"order"`
	NextAfter  *rootedindex.Cursor  `json:"next_after,omitempty"`
	Results    []rootedindex.Result `json:"results"`
}

type indexTransactionQueryResult struct {
	Provenance string                          `json:"provenance"`
	Finality   string                          `json:"finality"`
	Order      string                          `json:"order"`
	NextAfter  *rootedindex.Cursor             `json:"next_after,omitempty"`
	Results    []rootedindex.TransactionResult `json:"results"`
}

func runIndexDoctor(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("index doctor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("dir", "", "private index directory")
	maxRecordAge := flags.Duration("max-record-age", 0, "maximum age of the latest verified journal record")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, indexUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *dir == "" {
		return errors.New("index doctor requires --dir")
	}
	if !filepath.IsAbs(*dir) || filepath.Clean(*dir) != *dir {
		return errors.New("index doctor directory must be a clean absolute path")
	}
	if *maxRecordAge < 0 {
		return errors.New("index doctor --max-record-age cannot be negative")
	}
	status, err := rootedindex.ReadCompleteStatus(*dir)
	var freshnessErr error
	if err == nil && *maxRecordAge > 0 {
		freshnessErr = rootedindex.RequireFresh(status, time.Now().UTC(), *maxRecordAge)
		err = freshnessErr
	}
	if err == nil {
		result := indexDoctorResult{Status: "ready", Ready: true, Index: &status}
		if *jsonOutput {
			return json.NewEncoder(output).Encode(result)
		}
		_, err = fmt.Fprintf(output,
			"Rooted index is ready\nProvenance: %s\nRecords: %d\nLast cursor: %s\n",
			status.Provenance, status.Records, printableCursor(status.LastCursor))
		return err
	}

	reason := "integrity validation failed"
	next := []string{
		"Stop the ingest process, then run this doctor again.",
		"Keep the existing directory unchanged; do not edit or delete its journal, segments, lock file, query snapshot, or blobs.",
		"Confirm the Mithril source, cluster, and permanent filters are the intended ones.",
		"If the check still fails, create a new private directory and backfill it from retained rooted events; use --latest only for a deliberately future-only index.",
		"Keep the old directory for audit and comparison until the replacement is verified.",
	}
	if migrationReason, ok := rootedindex.SchemaMigrationReason(err); ok {
		reason = migrationReason
		next = []string{
			"Stop ingest and keep the existing index unchanged for audit.",
			"Create a new private v5 index with the intended source, cluster, and permanent filters.",
			"Backfill it from Mithril's event-schema-v3 framed rooted feed, then run doctor on the replacement before switching readers.",
		}
	}
	var checked *rootedindex.Status
	if freshnessErr != nil {
		reason = freshnessErr.Error()
		checked = &status
		next = []string{
			"Confirm the supervised rooted-event ingester is healthy and advancing this exact index.",
			"Run index doctor again after a complete rooted batch has been stored.",
			"Do not expose this index as fresh evidence until the age check passes.",
		}
	}
	if status.SchemaVersion != 0 && !status.Complete {
		reason = "ingest stopped before the terminal slot root marker"
		checked = &status
		resumeStep := "Resume after the reported last cursor; if it is none, replay the retained feed from the beginning."
		if status.LastCursor == nil && status.LastBatch != nil && status.Start != nil && status.Start.After != nil {
			resumeStep = "No event cursor was stored after the batch began; resume with the exact recorded start cursor in index.start.after, not from the beginning."
		}
		next = []string{
			"Keep this index unchanged and resume the same trusted Mithril rooted-event stream.",
			resumeStep,
			"Use the same permanent filters, then run doctor again after the slot root marker is stored.",
		}
	}
	if _, statErr := os.Lstat(*dir); errors.Is(statErr, os.ErrNotExist) {
		reason = "index directory does not exist"
	} else if validatePrivateDirectory(*dir) == nil {
		if entries, readErr := os.ReadDir(*dir); readErr == nil && len(entries) == 0 {
			reason = "index has not been initialized"
			next = []string{
				"Run the documented Mithril rooted-event ingest command for this workspace.",
				"If you expected existing records, stop and confirm that this is the intended index directory.",
			}
		}
	}
	result := indexDoctorResult{
		Status: "attention_required", Ready: false, Reason: reason, Index: checked, NextSteps: next,
	}
	if *jsonOutput {
		if encodeErr := json.NewEncoder(output).Encode(result); encodeErr != nil {
			return encodeErr
		}
		return errIndexNeedsAttention
	}
	if _, writeErr := fmt.Fprintf(output, "Rooted index needs attention\nReason: %s\nSafe recovery:\n", reason); writeErr != nil {
		return writeErr
	}
	for i, step := range next {
		if _, writeErr := fmt.Fprintf(output, "%d. %s\n", i+1, step); writeErr != nil {
			return writeErr
		}
	}
	return errIndexNeedsAttention
}

func printableCursor(cursor *rootedindex.Cursor) string {
	if cursor == nil {
		return "none"
	}
	return cursor.String()
}

func runIndexMCP(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("index mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("dir", "", "private index directory")
	maxRecordAge := flags.Duration("max-record-age", 0, "maximum age of the latest verified journal record")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *dir == "" {
		return errors.New("index mcp requires --dir")
	}
	if *maxRecordAge < 0 {
		return errors.New("index mcp --max-record-age cannot be negative")
	}
	closer, ok := input.(io.ReadCloser)
	if !ok {
		return errors.New("index MCP input must be closable stdio")
	}
	return indexmcp.ServeWithMaxRecordAge(ctx, *dir, closer, output, *maxRecordAge)
}

var indexMCPName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func runIndexMCPConfig(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("index mcp-config", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("dir", "", "private index directory")
	name := flags.String("name", "", "MCP server name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *dir == "" || !indexMCPName.MatchString(*name) {
		return errors.New("index mcp-config requires --dir and a 1-64 character letter, number, underscore, or hyphen --name")
	}
	if err := indexmcp.ValidateDirectory(*dir); err != nil {
		return err
	}
	if _, err := rootedindex.ReadCompleteStatus(*dir); err != nil {
		return err
	}
	agentPath, err := resolvedAgentExecutable()
	if err != nil {
		return err
	}
	entry := map[string]any{"mcpServers": map[string]any{
		*name: map[string]any{"command": agentPath, "args": []string{"index", "mcp", "--dir", *dir}},
	}}
	encoded, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, string(encoded))
	return err
}

func runIndexIngest(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("index ingest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("dir", "", "private index directory")
	cluster := flags.String("cluster", "", "expected Mithril cluster")
	genesisHash := flags.String("genesis-hash", "", "expected cluster genesis hash")
	workspacePath := flags.String("workspace", "", "private program workspace.json path")
	kind := flags.String("kind", "", "workspace index kind: state or activity")
	owner := flags.String("owner", "", "owner program filter")
	account := flags.String("account", "", "account address filter")
	mention := flags.String("mention", "", "transaction address filter")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, indexUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("index ingest received unexpected positional arguments")
	}
	if *workspacePath != "" {
		if *dir != "" || *cluster != "" || *genesisHash != "" || *owner != "" || *account != "" || *mention != "" {
			return errors.New("index ingest --workspace cannot be combined with directory, source, or filter flags")
		}
		workspace, err := loadProgramWorkspace(*workspacePath)
		if err != nil {
			return err
		}
		cleanPath, err := cleanProgramWorkspacePath(*workspacePath, "workspace path")
		if err != nil {
			return err
		}
		view := workspaceReport("", cleanPath, workspace)
		*cluster, *genesisHash = workspace.Cluster, view.GenesisHash
		switch *kind {
		case "state":
			*dir, *owner = view.StateIndex, workspace.Program
		case "activity":
			*dir, *mention = view.ActivityIndex, workspace.Program
		default:
			return errors.New("index ingest --workspace requires --kind state or --kind activity")
		}
	} else if *kind != "" {
		return errors.New("index ingest --kind requires --workspace")
	}
	if *dir == "" || *cluster == "" || *genesisHash == "" {
		return errors.New("index ingest requires --dir, --cluster, and --genesis-hash")
	}
	source, start, remaining, err := rootedindex.ReadPreamble(input)
	if err != nil {
		return err
	}
	if source.Cluster != *cluster || source.GenesisHash != *genesisHash {
		return errors.New("Mithril rooted source does not match the expected cluster and genesis hash")
	}
	index, err := rootedindex.Open(*dir, source, rootedindex.Filter{
		Owner: *owner, Account: *account, Mention: *mention,
	})
	if err != nil {
		return err
	}
	if err := index.BeginStream(start.After); err != nil {
		_ = index.Close()
		return err
	}
	hadEvents := index.HasEvents()
	progress := func(stored int) {
		if !*jsonOutput && stored%10_000 == 0 {
			_, _ = fmt.Fprintf(output, "Indexed %d rooted events...\n", stored)
		}
	}
	if !*jsonOutput {
		_, _ = fmt.Fprintln(output, "Indexing rooted events...")
	}
	stored, ingestErr := rootedindex.IngestWithProgress(ctx, index, remaining, progress)
	closeErr := index.Close()
	if ingestErr != nil {
		return ingestErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !hadEvents && stored == 0 && start.After == nil {
		return errors.New("Mithril produced no rooted events; check the source command and its error before retrying this index")
	}
	result := struct {
		Status string `json:"status"`
		Stored int    `json:"stored"`
	}{Status: "rooted events indexed", Stored: stored}
	if *jsonOutput {
		return json.NewEncoder(output).Encode(result)
	}
	_, err = fmt.Fprintf(output, "Rooted events indexed: %d\nIndex: %q\n", stored, *dir)
	return err
}

func runIndexStatus(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("index status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("dir", "", "private index directory")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, indexUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *dir == "" {
		return errors.New("index status requires --dir")
	}
	status, err := rootedindex.ReadStatus(*dir)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(output).Encode(status)
	}
	last, root, recordedAt := "none", "none", "none"
	if status.LastCursor != nil {
		last = status.LastCursor.String()
	}
	if status.LastRoot != nil {
		root = status.LastRoot.String()
	}
	if status.LastRecordedAt != nil {
		recordedAt = status.LastRecordedAt.Format(time.RFC3339Nano)
	}
	_, err = fmt.Fprintf(output,
		"Rooted index integrity verified\nSnapshot complete: %t\nSchemas: index=%d event=%d\nProvenance: %s (%s)\nTransactions: %d\nAccount updates: %d\nRooted slots: %d\nLast cursor: %s\nLast root: %s\nLast recorded at: %s\nChain head: %s\n",
		status.Complete, status.SchemaVersion, status.EventSchemaVersion, status.Provenance, status.Finality,
		status.Transactions, status.Accounts, status.Roots, last, root, recordedAt, status.ChainHead)
	return err
}

func runIndexQuery(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("index query", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("dir", "", "private index directory")
	owner := flags.String("owner", "", "owner program filter")
	account := flags.String("account", "", "account address filter")
	afterText := flags.String("after", "", "exclusive rooted cursor")
	limit := flags.Int("limit", 100, "maximum results")
	includeData := flags.Bool("include-data", false, "include base64 account data in JSON")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, indexUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *dir == "" {
		return errors.New("index query requires --dir")
	}
	var after *rootedindex.Cursor
	if *afterText != "" {
		cursor, err := rootedindex.ParseCursor(*afterText)
		if err != nil {
			return err
		}
		after = &cursor
	}
	status, err := rootedindex.ReadCompleteStatus(*dir)
	if err != nil {
		return err
	}
	results, err := rootedindex.QueryAccounts(*dir, rootedindex.Query{
		Owner: *owner, Account: *account, After: after,
		Limit: *limit, IncludeData: *includeData,
	})
	if err != nil {
		return err
	}
	order, nextAfter := indexQueryPage(after, results)
	if *jsonOutput || *includeData {
		return json.NewEncoder(output).Encode(indexAccountQueryResult{
			Provenance: status.Provenance,
			Finality:   status.Finality,
			Order:      order,
			NextAfter:  nextAfter,
			Results:    results,
		})
	}
	if _, err := fmt.Fprintf(output,
		"Account updates (%d, %s)\nProvenance: %s (%s)\n",
		len(results), strings.ReplaceAll(order, "_", " "), status.Provenance, status.Finality); err != nil {
		return err
	}
	for _, result := range results {
		if _, err := fmt.Fprintf(output,
			"  %s  account=%s owner=%s lamports=%d bytes=%d data_sha256=%s tombstone=%t\n",
			result.Cursor, result.Pubkey, result.Owner, result.Lamports,
			result.DataBytes, result.DataSHA256, result.Tombstone); err != nil {
			return err
		}
	}
	return nil
}

func runIndexTransactions(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("index transactions", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("dir", "", "private index directory")
	signature := flags.String("signature", "", "transaction signature")
	mention := flags.String("mention", "", "mentioned address")
	afterText := flags.String("after", "", "exclusive rooted cursor")
	limit := flags.Int("limit", 100, "maximum results")
	includePayload := flags.Bool("include-payload", false, "include signed transaction, logs, CPI, and return data")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, indexUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *dir == "" {
		return errors.New("index transactions requires --dir")
	}
	var after *rootedindex.Cursor
	if *afterText != "" {
		cursor, err := rootedindex.ParseCursor(*afterText)
		if err != nil {
			return err
		}
		after = &cursor
	}
	status, err := rootedindex.ReadCompleteStatus(*dir)
	if err != nil {
		return err
	}
	results, err := rootedindex.QueryTransactions(*dir, rootedindex.TransactionQuery{
		Signature: *signature, Mention: *mention, After: after,
		Limit: *limit, IncludePayload: *includePayload,
	})
	if err != nil {
		return err
	}
	order, nextAfter := indexTransactionQueryPage(after, results)
	if *jsonOutput || *includePayload {
		return json.NewEncoder(output).Encode(indexTransactionQueryResult{
			Provenance: status.Provenance,
			Finality:   status.Finality,
			Order:      order,
			NextAfter:  nextAfter,
			Results:    results,
		})
	}
	if _, err := fmt.Fprintf(output,
		"Transactions (%d, %s)\nProvenance: %s (%s)\n",
		len(results), strings.ReplaceAll(order, "_", " "), status.Provenance, status.Finality); err != nil {
		return err
	}
	for _, result := range results {
		if _, err := fmt.Fprintf(output,
			"  %s  signature=%s version=%s message_hash=%s succeeded=%t compute_units=%d failure=%s logs_truncated=%t\n",
			result.Cursor, result.Signature, result.Version, result.MessageHash, result.Succeeded, result.ComputeUnits,
			result.Failure, result.LogsTruncated); err != nil {
			return err
		}
	}
	return nil
}

func indexQueryPage(
	after *rootedindex.Cursor,
	results []rootedindex.Result,
) (string, *rootedindex.Cursor) {
	if after == nil {
		return "newest_first", nil
	}
	if len(results) == 0 {
		return "oldest_unseen_first", nil
	}
	next := results[len(results)-1].Cursor
	return "oldest_unseen_first", &next
}

func indexTransactionQueryPage(
	after *rootedindex.Cursor,
	results []rootedindex.TransactionResult,
) (string, *rootedindex.Cursor) {
	if after == nil {
		return "newest_first", nil
	}
	if len(results) == 0 {
		return "oldest_unseen_first", nil
	}
	next := results[len(results)-1].Cursor
	return "oldest_unseen_first", &next
}
