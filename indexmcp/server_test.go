package indexmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/rootedindex"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var testSource = rootedindex.SourceDescriptor{
	Cluster:             "alpenglow",
	GenesisHash:         "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG",
	AccountsDBRootRunID: "0123abcd",
}

func beginBatch(t *testing.T, index *rootedindex.Index, sequence, slot uint64) {
	t.Helper()
	_, err := index.BeginBatch(rootedindex.BatchDescriptor{
		ManifestSequence: sequence, SidecarVersion: rootedindex.SupportedSidecarVersion,
		FromSlot: slot, ThroughSlot: slot, SHA256: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestServerExposesOnlyBoundedMetadataQueries(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	index, err := rootedindex.Open(dir, testSource, rootedindex.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	beginBatch(t, index, 1, 50)
	account := "11111111111111111111111111111111"
	owner := "ComputeBudget111111111111111111111111111111"
	if _, err := index.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion,
		Cursor:        rootedindex.Cursor{Slot: 50, Ordinal: 0},
		Kind:          "account_updated",
		Account: &rootedindex.AccountUpdate{
			Pubkey: account, Owner: owner, Lamports: 1, Data: []byte("private"),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion,
		Cursor:        rootedindex.Cursor{Slot: 50, Ordinal: 1},
		Kind:          "slot_rooted",
		Root: &rootedindex.RootedSlot{
			ParentSlot: 49, Bankhash: "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG",
			AccountCount: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- Serve(ctx, dir, serverReader, serverWriter) }()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.IOTransport{
		Reader: clientReader, Writer: clientWriter,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := session.InitializeResult().ProtocolVersion; got != "2026-07-28" {
		t.Fatalf("MCP protocol version = %q, want current 2026-07-28", got)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 3 {
		t.Fatalf("tools = %+v, %v", tools, err)
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint ||
			!tool.Annotations.IdempotentHint || tool.Annotations.OpenWorldHint == nil ||
			*tool.Annotations.OpenWorldHint {
			t.Fatalf("tool %q is not closed, read-only, and idempotent", tool.Name)
		}
	}
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "mithril_index_accounts", Arguments: map[string]any{"account": account, "limit": 1},
	})
	if err != nil || result.IsError {
		t.Fatalf("account query = %+v, %v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var results accountResults
	if err := json.Unmarshal(encoded, &results); err != nil {
		t.Fatal(err)
	}
	if results.Provenance != rootedindex.RootedProvenance ||
		results.Finality != rootedindex.RootedFinality ||
		len(results.Results) != 1 || results.Results[0].Pubkey != account {
		t.Fatalf("account results = %+v", results)
	}

	// Keep the MCP session and writer alive together, advance one more complete
	// manifest batch, and prove repeated calls move atomically to the new root.
	beginBatch(t, index, 2, 51)
	if _, err := index.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion,
		Cursor:        rootedindex.Cursor{Slot: 51, Ordinal: 0},
		Kind:          "account_updated",
		Account: &rootedindex.AccountUpdate{
			Pubkey: account, Owner: owner, Lamports: 2, Data: []byte("new-private"),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion,
		Cursor:        rootedindex.Cursor{Slot: 51, Ordinal: 1},
		Kind:          "slot_rooted",
		Root: &rootedindex.RootedSlot{
			ParentSlot: 50, Bankhash: "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG",
			AccountCount: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	for range 25 {
		result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{
			Name: "mithril_index_accounts", Arguments: map[string]any{"account": account, "limit": 1},
		})
		if err != nil || result.IsError {
			t.Fatalf("online account query = %+v, %v", result, err)
		}
		encoded, err = json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		decodeErr := json.Unmarshal(encoded, &results)
		if decodeErr != nil ||
			len(results.Results) != 1 || results.Results[0].Cursor.Slot != 51 {
			t.Fatalf("online account results = %+v, %v", results, decodeErr)
		}
	}
	if tools.Tools[0].OutputSchema == nil {
		t.Fatal("MCP tool has no output schema")
	}
	schema, err := json.Marshal(tools.Tools)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"include_data", "include_payload", "return_data", "inner_instructions"} {
		if bytes.Contains(schema, []byte(`"`+forbidden+`"`)) {
			t.Fatalf("MCP schema exposes forbidden payload field %q", forbidden)
		}
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = clientWriter.Close()
	_ = clientReader.Close()
	_ = serverReader.Close()
	_ = serverWriter.Close()
	select {
	case err := <-serverErrors:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("index MCP server did not stop")
	}
}

func TestServerRequiresPrivateDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	if err := Serve(t.Context(), dir, reader, io.Discard); err == nil {
		t.Fatal("non-private index directory was accepted")
	}
}

func TestServerRejectsIncompleteTerminalSlot(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	index, err := rootedindex.Open(dir, testSource, rootedindex.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginBatch(t, index, 1, 51)
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
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	if err := Serve(t.Context(), dir, reader, io.Discard); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete index MCP error = %v", err)
	}
}
