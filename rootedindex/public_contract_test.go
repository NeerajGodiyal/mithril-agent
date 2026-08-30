package rootedindex

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"testing"

	solanago "github.com/gagliardetto/solana-go"
)

func TestPublicMithrilRootedContractFixture(t *testing.T) {
	path := os.Getenv("MITHRIL_ROOTED_CONTRACT_FIXTURE")
	if path == "" {
		t.Skip("set MITHRIL_ROOTED_CONTRACT_FIXTURE for cross-repository QA")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	source, start, remaining, err := ReadPreamble(file)
	if err != nil {
		t.Fatal(err)
	}
	if source.Cluster != "alpenglow" || source.GenesisHash != testBankhash ||
		source.AccountsDBRootRunID != "0123456789abcdef0123456789abcdef" ||
		start.After != nil {
		t.Fatalf("public contract preamble = %+v, %+v", source, start)
	}
	dir := t.TempDir()
	index, err := Open(dir, source, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if err := index.BeginStream(start.After); err != nil {
		t.Fatal(err)
	}
	if stored, err := Ingest(t.Context(), index, remaining); err != nil || stored != 2 {
		t.Fatalf("public contract ingest = %d, %v", stored, err)
	}
	status, err := ReadCompleteStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Complete || status.SchemaVersion != IndexSchemaVersion ||
		status.EventSchemaVersion != EventSchemaVersion || status.Provenance != AlpenglowRootedProvenance ||
		status.Transactions != 0 || status.Accounts != 1 || status.Roots != 1 ||
		status.Batches != 1 || status.Start == nil || status.Start.After != nil ||
		status.FirstBatch == nil || status.FirstBatch.ManifestSequence != 1 || status.LastBatch == nil ||
		status.LastBatch.ManifestSequence != 1 || status.LastRoot == nil ||
		*status.LastRoot != (Cursor{Slot: 30, Ordinal: 1}) {
		t.Fatalf("public contract status = %+v", status)
	}

	accounts, err := QueryAccounts(dir, Query{Limit: 10, IncludeData: true})
	if err != nil || len(accounts) != 1 || accounts[0].Cursor != (Cursor{Slot: 30, Ordinal: 0}) ||
		accounts[0].Pubkey != "11111111111111111111111111111111" ||
		accounts[0].Owner != "ComputeBudget111111111111111111111111111111" ||
		accounts[0].Lamports != 7 || accounts[0].Executable || accounts[0].RentEpoch != 9 ||
		accounts[0].Tombstone || !bytes.Equal(accounts[0].Data, []byte("golden")) {
		t.Fatalf("public contract accounts = %+v, %v", accounts, err)
	}

	transactions, err := QueryTransactions(dir, TransactionQuery{Limit: 10, IncludePayload: true})
	if err != nil || len(transactions) != 0 {
		t.Fatalf("public contract transactions = %+v, %v", transactions, err)
	}
}

func TestPublicMithrilV1TransactionContractFixture(t *testing.T) {
	path := os.Getenv("MITHRIL_ROOTED_V1_CONTRACT_FIXTURE")
	if path == "" {
		t.Skip("set MITHRIL_ROOTED_V1_CONTRACT_FIXTURE for cross-repository QA")
	}
	wire, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(wire), []byte{'\n'})
	if len(lines) != 5 {
		t.Fatalf("v1 public contract has %d lines, want 5", len(lines))
	}
	var expected Event
	if err := json.Unmarshal(lines[3], &expected); err != nil {
		t.Fatal(err)
	}
	if expected.SchemaVersion != EventSchemaVersion || expected.Transaction == nil {
		t.Fatalf("v1 public transaction event = %+v", expected)
	}

	source, start, remaining, err := ReadPreamble(bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	if source.Cluster != "alpenglow" || source.GenesisHash != "c8fpTXm3XTRgE5maYQ24Li4L65wMYvAFomzXknxVEx7" ||
		source.AccountsDBRootRunID != "0123456789abcdef0123456789abcdef" || start.After != nil {
		t.Fatalf("v1 public contract preamble = %+v, %+v", source, start)
	}
	dir := t.TempDir()
	index, err := Open(dir, source, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if err := index.BeginStream(start.After); err != nil {
		t.Fatal(err)
	}
	if stored, err := Ingest(t.Context(), index, remaining); err != nil || stored != 2 {
		t.Fatalf("v1 public contract ingest = %d, %v", stored, err)
	}
	status, err := ReadCompleteStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Complete || status.SchemaVersion != IndexSchemaVersion ||
		status.EventSchemaVersion != EventSchemaVersion || status.Transactions != 1 || status.Accounts != 0 ||
		status.Roots != 1 || status.Batches != 1 || status.LastRoot == nil ||
		*status.LastRoot != (Cursor{Slot: 40, Ordinal: 1}) {
		t.Fatalf("v1 public contract status = %+v", status)
	}

	transactions, err := QueryTransactions(dir, TransactionQuery{Limit: 1, IncludePayload: true})
	if err != nil || len(transactions) != 1 {
		t.Fatalf("v1 public contract transactions = %+v, %v", transactions, err)
	}
	got := transactions[0]
	if got.Cursor != (Cursor{Slot: 40, Ordinal: 0}) || got.Version != TransactionVersionV1 ||
		got.Signature != expected.Transaction.Signature || got.MessageHash != expected.Transaction.MessageHash ||
		!bytes.Equal(got.Transaction, expected.Transaction.Transaction) ||
		!slices.Equal(got.AccountKeys, expected.Transaction.AccountKeys) {
		t.Fatalf("v1 public contract transaction = %+v", got)
	}
	decoded, err := solanago.TransactionFromBytes(got.Transaction)
	if err != nil {
		t.Fatal(err)
	}
	if err := decoded.Sanitize(); err != nil {
		t.Fatal(err)
	}
	if err := decoded.VerifySignatures(); err != nil {
		t.Fatal(err)
	}
	wantMessage, err := decoded.Message.MarshalBinary()
	if err != nil || !bytes.Equal(got.Message, wantMessage) {
		t.Fatalf("v1 public contract message = %x, %v", got.Message, err)
	}
	config := decoded.Message.TransactionConfig
	if decoded.Message.GetVersion() != solanago.MessageVersionV1 ||
		config.PriorityFee == nil || *config.PriorityFee != 9_001 ||
		config.ComputeUnitLimit == nil || *config.ComputeUnitLimit != 300_000 ||
		config.LoadedAccountsDataSizeLimit == nil || *config.LoadedAccountsDataSizeLimit != 65_536 ||
		config.HeapSize == nil || *config.HeapSize != 64*1024 {
		t.Fatalf("v1 public contract decoded message = %+v", decoded.Message)
	}
	messageHash, err := transactionMessageHash(decoded)
	if err != nil || solanago.Hash(messageHash).String() != got.MessageHash {
		t.Fatalf("v1 public contract message hash = %s, %v", solanago.Hash(messageHash).String(), err)
	}

	metadata, err := QueryTransactions(dir, TransactionQuery{Limit: 1})
	if err != nil || len(metadata) != 1 || metadata[0].Version != TransactionVersionV1 ||
		metadata[0].MessageHash != got.MessageHash || metadata[0].Message != nil || metadata[0].Transaction != nil {
		t.Fatalf("v1 public contract metadata-only result = %+v, %v", metadata, err)
	}
}
