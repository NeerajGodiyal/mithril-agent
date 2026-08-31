// Package indexmcp exposes bounded, read-only rooted-index queries over local
// stdio MCP. The operator's OS identity and private index directory are the
// authorization boundary; the server opens no network listener.
package indexmcp

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/mcpstdio"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/rootedindex"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxMCPResults = 1000

type noInput struct{}

type accountQuery struct {
	Owner   string `json:"owner,omitempty" jsonschema:"Solana owner program address"`
	Account string `json:"account,omitempty" jsonschema:"Exact Solana account address"`
	After   string `json:"after,omitempty" jsonschema:"Exclusive rooted cursor as SLOT:ORDINAL"`
	Limit   int    `json:"limit,omitempty" jsonschema:"Maximum results from 1 to 1000"`
}

type accountResults struct {
	Provenance string          `json:"provenance"`
	Finality   string          `json:"finality"`
	Order      string          `json:"order"`
	NextAfter  string          `json:"next_after,omitempty"`
	Results    []accountResult `json:"results"`
}

type accountResult struct {
	Cursor     rootedindex.Cursor `json:"cursor"`
	Pubkey     string             `json:"pubkey"`
	Owner      string             `json:"owner"`
	Lamports   uint64             `json:"lamports"`
	Executable bool               `json:"executable"`
	RentEpoch  uint64             `json:"rent_epoch"`
	Tombstone  bool               `json:"tombstone"`
	DataSHA256 string             `json:"data_sha256"`
	DataBytes  int                `json:"data_bytes"`
}

type transactionQuery struct {
	Signature string `json:"signature,omitempty" jsonschema:"Exact Solana transaction signature"`
	Mention   string `json:"mention,omitempty" jsonschema:"Exact mentioned Solana address"`
	After     string `json:"after,omitempty" jsonschema:"Exclusive rooted cursor as SLOT:ORDINAL"`
	Limit     int    `json:"limit,omitempty" jsonschema:"Maximum results from 1 to 1000"`
}

type transactionResults struct {
	Provenance string              `json:"provenance"`
	Finality   string              `json:"finality"`
	Order      string              `json:"order"`
	NextAfter  string              `json:"next_after,omitempty"`
	Results    []transactionResult `json:"results"`
}

type transactionResult struct {
	Cursor        rootedindex.Cursor             `json:"cursor"`
	Index         uint32                         `json:"index"`
	Signature     string                         `json:"signature"`
	Version       rootedindex.TransactionVersion `json:"version"`
	MessageHash   string                         `json:"message_hash"`
	AccountKeys   []string                       `json:"account_keys"`
	Succeeded     bool                           `json:"succeeded"`
	Failure       string                         `json:"failure,omitempty"`
	ComputeUnits  uint64                         `json:"compute_units"`
	LogsTruncated bool                           `json:"logs_truncated,omitempty"`
}

// Serve runs one local, read-only MCP server bound to one private index.
func Serve(ctx context.Context, dir string, input io.ReadCloser, output io.Writer) error {
	return ServeWithMaxRecordAge(ctx, dir, input, output, 0)
}

// ServeWithMaxRecordAge runs Serve and, when maxRecordAge is positive,
// rechecks the verified journal timestamp before startup and every tool call.
func ServeWithMaxRecordAge(
	ctx context.Context,
	dir string,
	input io.ReadCloser,
	output io.Writer,
	maxRecordAge time.Duration,
) error {
	if input == nil || output == nil {
		return errors.New("index MCP stdio is required")
	}
	if err := ValidateDirectory(dir); err != nil {
		return err
	}
	if _, err := readStatus(dir, maxRecordAge); err != nil {
		return err
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name: "mithril-rooted-index", Title: "Mithril rooted program index", Version: "0.1.0",
	}, &mcpsdk.ServerOptions{
		Instructions: "Read one operator-authorized local Mithril rooted index. Tools are bounded and omit raw account data, signed transactions, logs, CPI, and return data. No tool loads a wallet, signs, submits, or contacts a network.",
	})
	server.AddReceivingMiddleware(mcpstdio.LimitToolCalls(4))
	closedWorld := false
	annotations := &mcpsdk.ToolAnnotations{
		ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld,
	}
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name: "mithril_index_status", Title: "Rooted Index Status",
		Description: "Verify the local index chain and return its filter, counts, last rooted cursor, and chain-head hash.",
		Annotations: annotations,
	}, func(context.Context, *mcpsdk.CallToolRequest, noInput) (*mcpsdk.CallToolResult, rootedindex.Status, error) {
		status, err := readStatus(dir, maxRecordAge)
		return nil, status, err
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name: "mithril_index_accounts", Title: "Rooted Account Updates",
		Description: "Query bounded rooted account metadata. Without after it is newest-first; with after it is oldest-unseen-first and returns next_after for lossless burst paging. Raw account bytes are never returned.",
		Annotations: annotations,
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input accountQuery) (*mcpsdk.CallToolResult, accountResults, error) {
		status, err := readStatus(dir, maxRecordAge)
		if err != nil {
			return nil, accountResults{}, err
		}
		query, err := buildAccountQuery(input)
		if err != nil {
			return nil, accountResults{}, err
		}
		results, err := rootedindex.QueryAccounts(dir, query)
		var last rootedindex.Cursor
		if len(results) > 0 {
			last = results[len(results)-1].Cursor
		}
		order, nextAfter := indexMCPPage(query.After, last, len(results) > 0)
		return nil, accountResults{
			Provenance: status.Provenance, Finality: status.Finality,
			Order: order, NextAfter: nextAfter, Results: accountMetadata(results),
		}, err
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name: "mithril_index_transactions", Title: "Rooted Transactions",
		Description: "Query bounded rooted transaction metadata. Without after it is newest-first; with after it is oldest-unseen-first and returns next_after for lossless burst paging. Signed transactions, logs, CPI, and return data are never returned.",
		Annotations: annotations,
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input transactionQuery) (*mcpsdk.CallToolResult, transactionResults, error) {
		status, err := readStatus(dir, maxRecordAge)
		if err != nil {
			return nil, transactionResults{}, err
		}
		query, err := buildTransactionQuery(input)
		if err != nil {
			return nil, transactionResults{}, err
		}
		results, err := rootedindex.QueryTransactions(dir, query)
		var last rootedindex.Cursor
		if len(results) > 0 {
			last = results[len(results)-1].Cursor
		}
		order, nextAfter := indexMCPPage(query.After, last, len(results) > 0)
		return nil, transactionResults{
			Provenance: status.Provenance, Finality: status.Finality,
			Order: order, NextAfter: nextAfter, Results: transactionMetadata(results),
		}, err
	})
	err := server.Run(ctx, &mcpsdk.IOTransport{
		Reader: mcpstdio.NewReader(input), Writer: mcpstdio.WriteCloser{Writer: output},
	})
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) ||
		errors.Is(err, mcpsdk.ErrConnectionClosed) || err.Error() == "server is closing" ||
		err.Error() == "server is closing: EOF" {
		return nil
	}
	return err
}

func readStatus(dir string, maxRecordAge time.Duration) (rootedindex.Status, error) {
	status, err := rootedindex.ReadCompleteStatus(dir)
	if err != nil || maxRecordAge <= 0 {
		return status, err
	}
	return status, rootedindex.RequireFresh(status, time.Now().UTC(), maxRecordAge)
}

func indexMCPPage(
	after *rootedindex.Cursor,
	last rootedindex.Cursor,
	hasResults bool,
) (string, string) {
	if after == nil {
		return "newest_first", ""
	}
	if !hasResults {
		return "oldest_unseen_first", ""
	}
	return "oldest_unseen_first", last.String()
}

// ValidateDirectory verifies the local identity and permission boundary used
// by both the server and generated MCP client configuration.
func ValidateDirectory(dir string) error {
	if err := secureexec.ValidateProtectedDirectory(dir); err != nil {
		return errors.New("index MCP directory is not private and trusted")
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("index MCP directory must be private with mode 0700")
	}
	return nil
}

func buildAccountQuery(input accountQuery) (rootedindex.Query, error) {
	after, err := parseAfter(input.After)
	if err != nil {
		return rootedindex.Query{}, err
	}
	limit, err := queryLimit(input.Limit)
	if err != nil {
		return rootedindex.Query{}, err
	}
	return rootedindex.Query{
		Owner: input.Owner, Account: input.Account, After: after, Limit: limit,
	}, nil
}

func buildTransactionQuery(input transactionQuery) (rootedindex.TransactionQuery, error) {
	after, err := parseAfter(input.After)
	if err != nil {
		return rootedindex.TransactionQuery{}, err
	}
	limit, err := queryLimit(input.Limit)
	if err != nil {
		return rootedindex.TransactionQuery{}, err
	}
	return rootedindex.TransactionQuery{
		Signature: input.Signature, Mention: input.Mention, After: after, Limit: limit,
	}, nil
}

func parseAfter(value string) (*rootedindex.Cursor, error) {
	if value == "" {
		return nil, nil
	}
	cursor, err := rootedindex.ParseCursor(value)
	if err != nil {
		return nil, err
	}
	return &cursor, nil
}

func queryLimit(value int) (int, error) {
	if value == 0 {
		return 100, nil
	}
	if value < 1 || value > maxMCPResults {
		return 0, errors.New("index MCP limit must be between 1 and 1000")
	}
	return value, nil
}

func accountMetadata(results []rootedindex.Result) []accountResult {
	metadata := make([]accountResult, len(results))
	for index, result := range results {
		metadata[index] = accountResult{
			Cursor: result.Cursor, Pubkey: result.Pubkey, Owner: result.Owner,
			Lamports: result.Lamports, Executable: result.Executable,
			RentEpoch: result.RentEpoch, Tombstone: result.Tombstone,
			DataSHA256: result.DataSHA256, DataBytes: result.DataBytes,
		}
	}
	return metadata
}

func transactionMetadata(results []rootedindex.TransactionResult) []transactionResult {
	metadata := make([]transactionResult, len(results))
	for index, result := range results {
		metadata[index] = transactionResult{
			Cursor: result.Cursor, Index: result.Index, Signature: result.Signature,
			Version: result.Version, MessageHash: result.MessageHash,
			AccountKeys: append([]string(nil), result.AccountKeys...),
			Succeeded:   result.Succeeded, Failure: result.Failure,
			ComputeUnits: result.ComputeUnits, LogsTruncated: result.LogsTruncated,
		}
	}
	return metadata
}
