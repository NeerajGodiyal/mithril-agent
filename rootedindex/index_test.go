package rootedindex

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	solanago "github.com/solana-foundation/solana-go/v2"
)

const (
	testAccount  = "11111111111111111111111111111111"
	testOwner    = "ComputeBudget111111111111111111111111111111"
	otherOwner   = "Vote111111111111111111111111111111111111111"
	testBankhash = "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG"
)

func testSource() SourceDescriptor {
	return SourceDescriptor{
		Cluster: "alpenglow", GenesisHash: testBankhash, AccountsDBRootRunID: "0123abcd",
	}
}

func TestProvenanceForCluster(t *testing.T) {
	for _, test := range []struct {
		cluster string
		want    string
	}{
		{cluster: "alpenglow", want: AlpenglowRootedProvenance},
		{cluster: "devnet", want: ClassicFinalizedRootedProvenance},
		{cluster: "testnet", want: ClassicFinalizedRootedProvenance},
		{cluster: "mainnet-beta", want: ClassicFinalizedRootedProvenance},
	} {
		got, err := ProvenanceForCluster(test.cluster)
		if err != nil || got != test.want {
			t.Fatalf("ProvenanceForCluster(%q) = %q, %v; want %q", test.cluster, got, err, test.want)
		}
	}
	if _, err := ProvenanceForCluster("unknown"); err == nil {
		t.Fatal("unsupported rooted source cluster was accepted")
	}
}

func TestOpenAcceptsNewAccountsDBLineageID(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := testSource()
	source.AccountsDBRootRunID = "0123456789abcdef0123456789abcdef"
	index, err := Open(dir, source, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClassicIndexUsesDistinctProvenance(t *testing.T) {
	source := testSource()
	source.Cluster = "devnet"
	index, err := Open(t.TempDir(), source, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index, 1, 1, 1)
	root := rootEvent(1, 0, 0)
	root.Root.BlockID, root.Root.ParentBlockID = "", ""
	root.Root.FinalitySource = FinalityRPCFinalized
	if _, err := index.Append(root); err != nil {
		t.Fatal(err)
	}
	dir := index.dir
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	status, err := ReadCompleteStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if status.Provenance != ClassicFinalizedRootedProvenance || status.Finality != RootedFinality {
		t.Fatalf("classic status = %+v", status)
	}
}

func TestIndexRejectsRootFinalityFromAnotherClusterMode(t *testing.T) {
	for _, test := range []struct {
		name    string
		cluster string
		classic bool
	}{
		{name: "classic root on Alpenglow", cluster: "alpenglow", classic: true},
		{name: "Alpenglow root on classic", cluster: "devnet"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := testSource()
			source.Cluster = test.cluster
			index, err := Open(t.TempDir(), source, Filter{})
			if err != nil {
				t.Fatal(err)
			}
			defer index.Close()
			beginTestBatch(t, index, 1, 1, 1)
			root := rootEvent(1, 0, 0)
			if test.classic {
				root.Root.FinalitySource = FinalityRPCFinalized
				root.Root.BlockID, root.Root.ParentBlockID = "", ""
			}
			if _, err := index.Append(root); err == nil || !strings.Contains(err.Error(), "bound") {
				t.Fatalf("cross-mode root error = %v", err)
			}
		})
	}
}

func beginTestBatch(t *testing.T, index *Index, sequence, from, through uint64) {
	t.Helper()
	if _, err := index.BeginBatch(BatchDescriptor{
		ManifestSequence: sequence, SidecarVersion: SupportedSidecarVersion,
		FromSlot: from, ThroughSlot: through, SHA256: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIndexRestartQueryAndIdempotence(t *testing.T) {
	dir := t.TempDir()
	filter := Filter{Owner: testOwner}
	index, err := Open(dir, testSource(), filter)
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index, 1, 10, 11)
	first := accountEvent(10, 0, []byte("first"))
	if added, err := index.Append(first); err != nil || !added {
		t.Fatalf("append first = %v, %v", added, err)
	}
	if added, err := index.Append(rootEvent(10, 1, 9)); err != nil || !added {
		t.Fatalf("append root = %v, %v", added, err)
	}
	second := accountEvent(11, 0, []byte("second"))
	if added, err := index.Append(second); err != nil || !added {
		t.Fatalf("append second = %v, %v", added, err)
	}
	if added, err := index.Append(rootEvent(11, 1, 10)); err != nil || !added {
		t.Fatalf("append second root = %v, %v", added, err)
	}
	if added, err := index.Append(first); err != nil || added {
		t.Fatalf("duplicate append = %v, %v", added, err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}

	status, err := ReadStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if status.Provenance != "mithril_alpenglow_rooted_feed" || status.Finality != "rooted" ||
		status.Accounts != 2 || status.Roots != 2 || status.LastRoot == nil || status.LastRoot.Slot != 11 {
		t.Fatalf("status = %+v", status)
	}
	results, err := QueryAccounts(dir, Query{Owner: testOwner, Limit: 10, IncludeData: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Cursor.Slot != 11 || string(results[0].Data) != "second" ||
		results[1].Cursor.Slot != 10 || string(results[1].Data) != "first" {
		t.Fatalf("results = %+v", results)
	}

	reopened, err := Open(dir, testSource(), filter)
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, reopened, 1, 10, 11)
	if added, err := reopened.Append(second); err != nil || added {
		t.Fatalf("duplicate after reopen = %v, %v", added, err)
	}
	beginTestBatch(t, reopened, 2, 12, 12)
	if added, err := reopened.Append(accountEvent(12, 0, nil)); err != nil || !added {
		t.Fatalf("append after reopen = %v, %v", added, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, testSource(), Filter{Owner: otherOwner}); err == nil {
		t.Fatal("filter mismatch was accepted")
	}
}

func TestQueriesPageAfterCursorWithoutSkippingBursts(t *testing.T) {
	accountDir := t.TempDir()
	accounts, err := Open(accountDir, testSource(), Filter{Owner: testOwner})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, accounts, 1, 10, 13)
	for slot := uint64(10); slot <= 13; slot++ {
		if _, err := accounts.Append(accountEvent(slot, 0, []byte{byte(slot)})); err != nil {
			t.Fatal(err)
		}
		if _, err := accounts.Append(rootEvent(slot, 1, slot-1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := accounts.Close(); err != nil {
		t.Fatal(err)
	}
	latest, err := QueryAccounts(accountDir, Query{Owner: testOwner, Limit: 2})
	if err != nil || len(latest) != 2 || latest[0].Cursor.Slot != 13 || latest[1].Cursor.Slot != 12 {
		t.Fatalf("latest accounts = %+v, %v", latest, err)
	}
	after := Cursor{Slot: 10, Ordinal: 0}
	page, err := QueryAccounts(accountDir, Query{Owner: testOwner, After: &after, Limit: 2})
	if err != nil || len(page) != 2 || page[0].Cursor.Slot != 11 || page[1].Cursor.Slot != 12 {
		t.Fatalf("paged accounts = %+v, %v", page, err)
	}

	transactionDir := t.TempDir()
	transactions, err := Open(transactionDir, testSource(), Filter{Mention: testOwner})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, transactions, 1, 20, 23)
	for slot := uint64(20); slot <= 23; slot++ {
		if _, err := transactions.Append(transactionEvent(slot, 0)); err != nil {
			t.Fatal(err)
		}
		if _, err := transactions.Append(rootEvent(slot, 1, slot-1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := transactions.Close(); err != nil {
		t.Fatal(err)
	}
	after = Cursor{Slot: 20, Ordinal: 0}
	transactionPage, err := QueryTransactions(transactionDir, TransactionQuery{
		Mention: testOwner, After: &after, Limit: 2,
	})
	if err != nil || len(transactionPage) != 2 ||
		transactionPage[0].Cursor.Slot != 21 || transactionPage[1].Cursor.Slot != 22 {
		t.Fatalf("paged transactions = %+v, %v", transactionPage, err)
	}
}

func TestQueriesUseTheLastCompleteSnapshotWhileIngestContinues(t *testing.T) {
	dir := t.TempDir()
	index, err := Open(dir, testSource(), Filter{Owner: testOwner})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index, 1, 10, 10)
	if _, err := index.Append(accountEvent(10, 0, []byte("first"))); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Append(rootEvent(10, 1, 9)); err != nil {
		t.Fatal(err)
	}
	results, err := QueryAccounts(dir, Query{Owner: testOwner, Limit: 10, IncludeData: true})
	if err != nil || len(results) != 1 || string(results[0].Data) != "first" {
		t.Fatalf("active-writer query = %+v, %v", results, err)
	}

	beginTestBatch(t, index, 2, 11, 11)
	if _, err := index.Append(accountEvent(11, 0, []byte("second"))); err != nil {
		t.Fatal(err)
	}
	results, err = QueryAccounts(dir, Query{Owner: testOwner, Limit: 10, IncludeData: true})
	if err != nil || len(results) != 1 || results[0].Cursor.Slot != 10 {
		t.Fatalf("incomplete batch escaped published snapshot = %+v, %v", results, err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := QueryAccounts(dir, Query{Owner: testOwner, Limit: 10}); err == nil ||
		!strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("stopped writer hid its incomplete durable journal: %v", err)
	}

	index, err = Open(dir, testSource(), Filter{Owner: testOwner})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	beginTestBatch(t, index, 2, 11, 11)
	if _, err := index.Append(rootEvent(11, 1, 10)); err != nil {
		t.Fatal(err)
	}
	results, err = QueryAccounts(dir, Query{Owner: testOwner, Limit: 10, IncludeData: true})
	if err != nil || len(results) != 2 || results[0].Cursor.Slot != 11 || string(results[0].Data) != "second" {
		t.Fatalf("updated active-writer query = %+v, %v", results, err)
	}
}

func TestIndexAcceptsFilteredOrdinalsAndRejectsAddressRegression(t *testing.T) {
	index, err := Open(t.TempDir(), testSource(), Filter{Owner: testOwner})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index, 1, 15, 15)
	defer index.Close()
	filtered := accountEvent(15, 1, []byte("only matching update"))
	if _, err := index.Append(filtered); err != nil {
		t.Fatal(err)
	}
	root := rootEvent(15, 3, 14)
	if _, err := index.Append(root); err != nil {
		t.Fatalf("filtered stream root was rejected: %v", err)
	}

	index2, err := Open(t.TempDir(), testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index2, 1, 16, 16)
	defer index2.Close()
	high := accountEvent(16, 0, nil)
	high.Account.Pubkey = testOwner
	if _, err := index2.Append(high); err != nil {
		t.Fatal(err)
	}
	low := accountEvent(16, 1, nil)
	if _, err := index2.Append(low); err == nil || !strings.Contains(err.Error(), "ascending") {
		t.Fatalf("address regression error = %v", err)
	}
}

func TestIndexTransactionRestartQueryAndValidation(t *testing.T) {
	dir := t.TempDir()
	filter := Filter{Mention: testOwner}
	index, err := Open(dir, testSource(), filter)
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index, 1, 60, 60)
	first := transactionEvent(60, 2)
	if added, err := index.Append(first); err != nil || !added {
		t.Fatalf("append transaction = %v, %v", added, err)
	}
	root := rootEvent(60, 5, 59)
	root.Root.TransactionCount, root.Root.AccountCount = 3, 2
	if added, err := index.Append(root); err != nil || !added {
		t.Fatalf("append transaction root = %v, %v", added, err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}

	results, err := QueryTransactions(dir, TransactionQuery{
		Mention: testOwner, Limit: 10, IncludePayload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Signature != first.Transaction.Signature ||
		results[0].Version != TransactionVersionLegacy || results[0].MessageHash != first.Transaction.MessageHash ||
		len(results[0].Logs) != 1 || !bytes.Equal(results[0].Transaction, first.Transaction.Transaction) {
		t.Fatalf("transaction results = %+v", results)
	}
	status, err := ReadStatus(dir)
	if err != nil || status.Transactions != 1 {
		t.Fatalf("transaction status = %+v, %v", status, err)
	}

	reopened, err := Open(dir, testSource(), filter)
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, reopened, 1, 60, 60)
	defer reopened.Close()
	if added, err := reopened.Append(first); err != nil || added {
		t.Fatalf("duplicate transaction after restart = %v, %v", added, err)
	}

	malformed := transactionEvent(61, 0)
	malformed.Transaction.LogsTruncated = true
	beginTestBatch(t, reopened, 2, 61, 61)
	if _, err := reopened.Append(malformed); err == nil || !strings.Contains(err.Error(), "truncated-log") {
		t.Fatalf("malformed truncated logs error = %v", err)
	}
}

func TestTransactionVersionsValidationAndPayloadGate(t *testing.T) {
	for _, test := range []struct {
		name    string
		version solanago.MessageVersion
		want    TransactionVersion
		data    int
	}{
		{name: "legacy", version: solanago.MessageVersionLegacy, want: TransactionVersionLegacy, data: 1},
		{name: "v0", version: solanago.MessageVersionV0, want: TransactionVersionV0, data: 1},
		{name: "v1_over_legacy_limit", version: solanago.MessageVersionV1, want: TransactionVersionV1, data: 1300},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := transactionEventVersion(80, 0, test.version, test.data)
			got, err := validateTransaction(*event.Transaction, Filter{})
			if err != nil || got != test.want {
				t.Fatalf("validate version = %q, %v; want %q", got, err, test.want)
			}
			if test.version == solanago.MessageVersionV1 && len(event.Transaction.Transaction) <= maxLegacyTransactionBytes {
				t.Fatalf("v1 fixture has only %d bytes", len(event.Transaction.Transaction))
			}
		})
	}

	oversizedV0 := transactionEventVersion(80, 0, solanago.MessageVersionV0, 1300)
	if _, err := validateTransaction(*oversizedV0.Transaction, Filter{}); err == nil ||
		!strings.Contains(err.Error(), "legacy/v0") {
		t.Fatalf("oversized v0 error = %v", err)
	}
	dataBytes := 1
	var maximum Event
	for range 4 {
		maximum = transactionEventVersion(80, 0, solanago.MessageVersionV1, dataBytes)
		delta := solanago.MaxTransactionSizeV1 - len(maximum.Transaction.Transaction)
		if delta == 0 {
			break
		}
		dataBytes += delta
	}
	if len(maximum.Transaction.Transaction) != solanago.MaxTransactionSizeV1 {
		t.Fatalf("maximum v1 fixture has %d bytes", len(maximum.Transaction.Transaction))
	}
	if got, err := validateTransaction(*maximum.Transaction, Filter{}); err != nil || got != TransactionVersionV1 {
		t.Fatalf("maximum v1 validation = %q, %v", got, err)
	}
	overMaximum := cloneTransaction(maximum.Transaction)
	overMaximum.Transaction = append(overMaximum.Transaction, 0)
	if _, err := validateTransaction(overMaximum, Filter{}); err == nil ||
		!strings.Contains(err.Error(), "allowed range") {
		t.Fatalf("over-maximum v1 error = %v", err)
	}

	dir := t.TempDir()
	index, err := Open(dir, testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index, 1, 80, 80)
	v1 := transactionEventVersion(80, 0, solanago.MessageVersionV1, 1300)
	if _, err := index.Append(v1); err != nil {
		t.Fatal(err)
	}
	root := rootEvent(80, 1, 79)
	root.Root.TransactionCount, root.Root.AccountCount = 1, 0
	if _, err := index.Append(root); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	metadata, err := QueryTransactions(dir, TransactionQuery{Limit: 1})
	if err != nil || len(metadata) != 1 || metadata[0].Version != TransactionVersionV1 ||
		metadata[0].MessageHash != v1.Transaction.MessageHash || metadata[0].Transaction != nil ||
		metadata[0].Message != nil ||
		metadata[0].Logs != nil || metadata[0].Inner != nil || metadata[0].ReturnData != nil {
		t.Fatalf("metadata-only transaction = %+v, %v", metadata, err)
	}
	payload, err := QueryTransactions(dir, TransactionQuery{Limit: 1, IncludePayload: true})
	decoded, decodeErr := solanago.TransactionFromBytes(v1.Transaction.Transaction)
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	wantMessage, messageErr := decoded.Message.MarshalBinary()
	if err != nil || messageErr != nil || len(payload) != 1 ||
		!bytes.Equal(payload[0].Message, wantMessage) ||
		!bytes.Equal(payload[0].Transaction, v1.Transaction.Transaction) ||
		!slices.Equal(payload[0].Logs, v1.Transaction.Logs) {
		t.Fatalf("payload transaction = %+v, %v", payload, err)
	}
}

func TestTransactionPayloadPreservesMessageForEveryVersion(t *testing.T) {
	for _, version := range []solanago.MessageVersion{
		solanago.MessageVersionLegacy,
		solanago.MessageVersionV0,
		solanago.MessageVersionV1,
	} {
		t.Run(fmt.Sprintf("version_%d", version), func(t *testing.T) {
			dir := t.TempDir()
			index, err := Open(dir, testSource(), Filter{})
			if err != nil {
				t.Fatal(err)
			}
			beginTestBatch(t, index, 1, 90, 90)
			event := transactionEventVersion(90, 0, version, 1)
			if _, err := index.Append(event); err != nil {
				t.Fatal(err)
			}
			root := rootEvent(90, 1, 89)
			root.Root.TransactionCount, root.Root.AccountCount = 1, 0
			if _, err := index.Append(root); err != nil {
				t.Fatal(err)
			}
			if err := index.Close(); err != nil {
				t.Fatal(err)
			}

			metadata, err := QueryTransactions(dir, TransactionQuery{Limit: 1})
			if err != nil || len(metadata) != 1 || metadata[0].Message != nil || metadata[0].Transaction != nil {
				t.Fatalf("metadata-only result = %+v, %v", metadata, err)
			}
			payload, err := QueryTransactions(dir, TransactionQuery{Limit: 1, IncludePayload: true})
			if err != nil || len(payload) != 1 {
				t.Fatalf("payload result = %+v, %v", payload, err)
			}
			decoded, err := solanago.TransactionFromBytes(event.Transaction.Transaction)
			if err != nil {
				t.Fatal(err)
			}
			message, err := decoded.Message.MarshalBinary()
			if err != nil || !bytes.Equal(payload[0].Message, message) ||
				!bytes.Equal(payload[0].Transaction, event.Transaction.Transaction) {
				t.Fatalf("payload message/transaction mismatch: %+v, %v", payload[0], err)
			}
		})
	}
}

func TestTransactionRejectsWireIdentityAndAccountMismatches(t *testing.T) {
	valid := transactionEvent(81, 0).Transaction
	for _, test := range []struct {
		name string
		edit func(*Transaction)
		want string
	}{
		{name: "trailing bytes", edit: func(tx *Transaction) {
			tx.Transaction = append(tx.Transaction, 0)
		}, want: "canonical"},
		{name: "invalid signature", edit: func(tx *Transaction) {
			tx.Transaction = bytes.Clone(tx.Transaction)
			tx.Transaction[1] ^= 1
		}, want: "signature verification"},
		{name: "message hash", edit: func(tx *Transaction) {
			tx.MessageHash = testBankhash
		}, want: "message hash"},
		{name: "static account", edit: func(tx *Transaction) {
			tx.AccountKeys = append([]string(nil), tx.AccountKeys...)
			tx.AccountKeys[0] = testAccount
		}, want: "static account"},
		{name: "inner parent", edit: func(tx *Transaction) {
			tx.Inner = []InnerInstructions{{Index: 1, Instructions: []CompiledInstruction{{
				ProgramIDIndex: 1, Accounts: []uint16{0}, Data: []byte{1},
			}}}}
		}, want: "parent outer index"},
	} {
		t.Run(test.name, func(t *testing.T) {
			transaction := cloneTransaction(valid)
			test.edit(&transaction)
			if _, err := validateTransaction(transaction, Filter{}); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("mismatch error = %v", err)
			}
		})
	}
}

func TestTransactionV0BindsResolvedAccountCountAndStaticPrefix(t *testing.T) {
	privateKey := solanago.PrivateKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, ed25519.SeedSize)))
	payer := privateKey.PublicKey()
	program, err := solanago.PublicKeyFromBase58(testOwner)
	if err != nil {
		t.Fatal(err)
	}
	table, err := solanago.PublicKeyFromBase58(testBankhash)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := solanago.PublicKeyFromBase58(testAccount)
	if err != nil {
		t.Fatal(err)
	}
	message := solanago.Message{
		Header:       solanago.MessageHeader{NumRequiredSignatures: 1, NumReadonlyUnsignedAccounts: 1},
		AccountKeys:  solanago.PublicKeySlice{payer, program},
		Instructions: []solanago.CompiledInstruction{{ProgramIDIndex: 1, Accounts: []uint16{0}, Data: []byte{1}}},
	}
	if _, err := message.SetVersion(solanago.MessageVersionV0); err != nil {
		t.Fatal(err)
	}
	message.SetAddressTableLookups([]solanago.MessageAddressTableLookup{{
		AccountKey: table, WritableIndexes: []byte{0},
	}})
	transaction := &solanago.Transaction{Message: message}
	if _, err := transaction.Sign(func(key solanago.PublicKey) *solanago.PrivateKey {
		if key == payer {
			return &privateKey
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wire, err := transaction.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	messageHash, err := transactionMessageHash(transaction)
	if err != nil {
		t.Fatal(err)
	}
	record := Transaction{
		Signature: transaction.Signatures[0].String(), Transaction: wire,
		MessageHash: solanago.Hash(messageHash).String(),
		AccountKeys: []string{payer.String(), program.String(), loaded.String()}, Succeeded: true,
	}
	if version, err := validateTransaction(record, Filter{}); err != nil || version != TransactionVersionV0 {
		t.Fatalf("valid v0 resolved accounts = %q, %v", version, err)
	}
	missingLoaded := cloneTransaction(&record)
	missingLoaded.AccountKeys = missingLoaded.AccountKeys[:2]
	if _, err := validateTransaction(missingLoaded, Filter{}); err == nil || !strings.Contains(err.Error(), "account keys") {
		t.Fatalf("missing resolved account error = %v", err)
	}
	wrongStatic := cloneTransaction(&record)
	wrongStatic.AccountKeys[0] = testAccount
	if _, err := validateTransaction(wrongStatic, Filter{}); err == nil || !strings.Contains(err.Error(), "static account") {
		t.Fatalf("wrong static prefix error = %v", err)
	}
}

func TestTransactionRejectsUnsanitizedV1Wire(t *testing.T) {
	event := transactionEventVersion(82, 0, solanago.MessageVersionV1, 1)
	transaction, err := solanago.TransactionFromBytes(event.Transaction.Transaction)
	if err != nil {
		t.Fatal(err)
	}
	transaction.Message.Header.NumReadonlySignedAccounts = 1
	privateKey := solanago.PrivateKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize)))
	if _, err := transaction.Sign(func(key solanago.PublicKey) *solanago.PrivateKey {
		if key == privateKey.PublicKey() {
			return &privateKey
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wire, err := transaction.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	messageHash, err := transactionMessageHash(transaction)
	if err != nil {
		t.Fatal(err)
	}
	bad := cloneTransaction(event.Transaction)
	bad.Signature = transaction.Signatures[0].String()
	bad.Transaction = wire
	bad.MessageHash = solanago.Hash(messageHash).String()
	if _, err := validateTransaction(bad, Filter{}); err == nil || !strings.Contains(err.Error(), "structural validation") {
		t.Fatalf("unsanitized v1 error = %v", err)
	}
}

func TestIndexRejectsRootHashAndBlockIDLineageBreaks(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*RootedSlot)
		want string
	}{
		{name: "parent blockhash", edit: func(root *RootedSlot) {
			root.ParentBlockhash = testOwner
		}, want: "parent blockhash"},
		{name: "parent block ID", edit: func(root *RootedSlot) {
			root.ParentBlockID = testOwner
		}, want: "parent block ID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			index, err := Open(t.TempDir(), testSource(), Filter{})
			if err != nil {
				t.Fatal(err)
			}
			defer index.Close()
			beginTestBatch(t, index, 1, 90, 90)
			if _, err := index.Append(rootEvent(90, 0, 89)); err != nil {
				t.Fatal(err)
			}
			beginTestBatch(t, index, 2, 91, 91)
			second := rootEvent(91, 0, 90)
			test.edit(second.Root)
			if _, err := index.Append(second); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("lineage error = %v", err)
			}
		})
	}
}

func TestIndexRejectsTransactionAfterAccount(t *testing.T) {
	index, err := Open(t.TempDir(), testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index, 1, 70, 70)
	defer index.Close()
	if _, err := index.Append(accountEvent(70, 1, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Append(transactionEvent(70, 2)); err == nil ||
		!strings.Contains(err.Error(), "after an account") {
		t.Fatalf("transaction ordering error = %v", err)
	}
}

func TestIndexRejectsBrokenLineageAndConflictingCursor(t *testing.T) {
	index, err := Open(t.TempDir(), testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index, 1, 20, 21)
	defer index.Close()
	first := accountEvent(20, 0, []byte("one"))
	if _, err := index.Append(first); err != nil {
		t.Fatal(err)
	}
	conflict := first
	conflict.Account = cloneAccount(first.Account)
	conflict.Account.Data = []byte("two")
	if _, err := index.Append(conflict); err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("conflicting cursor error = %v", err)
	}
	if _, err := index.Append(accountEvent(21, 0, nil)); err == nil || !strings.Contains(err.Error(), "prior slot root") {
		t.Fatalf("missing root error = %v", err)
	}
	if _, err := index.Append(rootEvent(20, 1, 19)); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Append(accountEvent(21, 0, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Append(rootEvent(21, 1, 18)); err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("wrong parent error = %v", err)
	}
}

func TestIndexBindsSourceAndManifestBatchLineage(t *testing.T) {
	dir := t.TempDir()
	index, err := Open(dir, testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index, 7, 10, 10)
	if _, err := index.Append(rootEvent(10, 0, 9)); err != nil {
		t.Fatal(err)
	}
	if _, err := index.BeginBatch(BatchDescriptor{
		ManifestSequence: 9, SidecarVersion: SupportedSidecarVersion,
		FromSlot: 11, ThroughSlot: 11, SHA256: strings.Repeat("b", 64),
	}); err == nil || !strings.Contains(err.Error(), "sequence") {
		t.Fatalf("skipped batch error = %v", err)
	}
	if _, err := index.BeginBatch(BatchDescriptor{
		ManifestSequence: 8, SidecarVersion: SupportedSidecarVersion,
		FromSlot: 10, ThroughSlot: 11, SHA256: strings.Repeat("b", 64),
	}); err == nil || !strings.Contains(err.Error(), "range") {
		t.Fatalf("overlapping batch error = %v", err)
	}
	// Skipped Solana slots create gaps between otherwise contiguous manifests.
	if _, err := index.BeginBatch(BatchDescriptor{
		ManifestSequence: 8, SidecarVersion: SupportedSidecarVersion,
		FromSlot: 12, ThroughSlot: 12, SHA256: strings.Repeat("b", 64),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Append(rootEvent(12, 0, 10)); err != nil {
		t.Fatalf("root after skipped slot: %v", err)
	}
	if _, err := index.Append(rootEvent(13, 0, 12)); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("out-of-batch event error = %v", err)
	}
	if _, err := index.BeginBatch(BatchDescriptor{
		ManifestSequence: 9, SidecarVersion: SupportedSidecarVersion,
		FromSlot: 14, ThroughSlot: 14, SHA256: strings.Repeat("c", 64),
	}); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}

	wrong := testSource()
	wrong.AccountsDBRootRunID = "deadbeef"
	if _, err := Open(dir, wrong, Filter{}); err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("source mismatch error = %v", err)
	}
	reopened, err := Open(dir, testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.BeginBatch(BatchDescriptor{
		ManifestSequence: 10, SidecarVersion: SupportedSidecarVersion,
		FromSlot: 15, ThroughSlot: 15, SHA256: strings.Repeat("d", 64),
	}); err == nil || !strings.Contains(err.Error(), "terminal root") {
		t.Fatalf("advanced incomplete batch error = %v", err)
	}
}

func TestReadPreambleRejectsRawEventsAndPreservesBufferedInput(t *testing.T) {
	var framed bytes.Buffer
	encoder := json.NewEncoder(&framed)
	if err := encoder.Encode(sourceFrame{RecordType: SourceRecordType, Source: testSource()}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(startFrame{RecordType: StartRecordType, Start: &StartDescriptor{}}); err != nil {
		t.Fatal(err)
	}
	batch := BatchDescriptor{
		ManifestSequence: 1, SidecarVersion: SupportedSidecarVersion,
		FromSlot: 30, ThroughSlot: 30, SHA256: strings.Repeat("a", 64),
	}
	if err := encoder.Encode(batchFrame{RecordType: BatchRecordType, Batch: batch}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(rootEvent(30, 0, 29)); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(sourceFrame{RecordType: SourceRecordType, Source: testSource()}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(startFrame{
		RecordType: StartRecordType,
		Start:      &StartDescriptor{After: &Cursor{Slot: 30, Ordinal: 0}},
	}); err != nil {
		t.Fatal(err)
	}
	nextBatch := batch
	nextBatch.ManifestSequence = 2
	nextBatch.FromSlot, nextBatch.ThroughSlot = 31, 31
	nextBatch.SHA256 = strings.Repeat("b", 64)
	if err := encoder.Encode(batchFrame{RecordType: BatchRecordType, Batch: nextBatch}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(rootEvent(31, 0, 30)); err != nil {
		t.Fatal(err)
	}
	source, start, remaining, err := ReadPreamble(&framed)
	if err != nil || source != testSource() || start.After != nil {
		t.Fatalf("preamble = %+v %+v, %v", source, start, err)
	}
	index, err := Open(t.TempDir(), source, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if err := index.BeginStream(start.After); err != nil {
		t.Fatal(err)
	}
	if stored, err := Ingest(t.Context(), index, remaining); err != nil || stored != 2 {
		t.Fatalf("framed ingest = %d, %v", stored, err)
	}

	raw, err := json.Marshal(rootEvent(30, 0, 29))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ReadPreamble(bytes.NewReader(raw)); err == nil || !strings.Contains(err.Error(), "source frame") {
		t.Fatalf("raw stream error = %v", err)
	}
	sourceJSON, err := json.Marshal(sourceFrame{RecordType: SourceRecordType, Source: testSource()})
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		`{"record_type":"mithril.rooted_start"}`,
		`{"record_type":"mithril.rooted_start","start":null}`,
	} {
		input := string(sourceJSON) + "\n" + invalid + "\n"
		if _, _, _, err := ReadPreamble(strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), "start frame") {
			t.Fatalf("invalid start %s error = %v", invalid, err)
		}
	}
}

func TestIngestRequiresBatchAfterRepeatedStreamStart(t *testing.T) {
	index, err := Open(t.TempDir(), testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	beginTestBatch(t, index, 1, 30, 30)
	if _, err := index.Append(accountEvent(30, 0, []byte("durable"))); err != nil {
		t.Fatal(err)
	}

	var input bytes.Buffer
	encoder := json.NewEncoder(&input)
	if err := encoder.Encode(sourceFrame{RecordType: SourceRecordType, Source: testSource()}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(startFrame{
		RecordType: StartRecordType,
		Start:      &StartDescriptor{After: &Cursor{Slot: 30, Ordinal: 0}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(sourceFrame{RecordType: SourceRecordType, Source: testSource()}); err != nil {
		t.Fatal(err)
	}
	if stored, err := Ingest(t.Context(), index, &input); stored != 0 || err == nil || !strings.Contains(err.Error(), "batch frame") {
		t.Fatalf("missing repeated batch stored=%d error=%v", stored, err)
	}
}

func TestNewIndexRejectsMidBatchStartAndAllowsNextBatchBoundary(t *testing.T) {
	midBatchDir := t.TempDir()
	midBatch, err := Open(midBatchDir, testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if err := midBatch.BeginStream(&Cursor{Slot: 30, Ordinal: 0}); err != nil {
		t.Fatal(err)
	}
	batch := BatchDescriptor{
		ManifestSequence: 2, SidecarVersion: SupportedSidecarVersion,
		FromSlot: 30, ThroughSlot: 30, SHA256: strings.Repeat("a", 64),
	}
	if _, err := midBatch.BeginBatch(batch); err == nil || !strings.Contains(err.Error(), "inside") {
		t.Fatalf("mid-batch start error = %v", err)
	}
	if err := midBatch.BeginStream(nil); err != nil {
		t.Fatalf("rebind empty index: %v", err)
	}
	if _, err := midBatch.BeginBatch(batch); err != nil {
		t.Fatalf("begin full retained batch after rebind: %v", err)
	}
	if _, err := midBatch.Append(rootEvent(30, 0, 29)); err != nil {
		t.Fatal(err)
	}
	if err := midBatch.Close(); err != nil {
		t.Fatal(err)
	}
	status, err := ReadCompleteStatus(midBatchDir)
	if err != nil || status.Start == nil || status.Start.After != nil {
		t.Fatalf("rebound status = %+v, %v", status, err)
	}

	dir := t.TempDir()
	boundary, err := Open(dir, testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if err := boundary.BeginStream(&Cursor{Slot: 30, Ordinal: 0}); err != nil {
		t.Fatal(err)
	}
	batch.ManifestSequence = 3
	batch.FromSlot, batch.ThroughSlot = 31, 31
	batch.SHA256 = strings.Repeat("b", 64)
	if _, err := boundary.BeginBatch(batch); err != nil {
		t.Fatal(err)
	}
	if _, err := boundary.Append(accountEvent(31, 0, []byte("boundary"))); err != nil {
		t.Fatal(err)
	}
	if _, err := boundary.Append(rootEvent(31, 1, 29)); err == nil ||
		!strings.Contains(err.Error(), "stream start") {
		t.Fatalf("disconnected first root error = %v", err)
	}
	if _, err := boundary.Append(rootEvent(31, 1, 30)); err != nil {
		t.Fatal(err)
	}
	if err := boundary.Close(); err != nil {
		t.Fatal(err)
	}
	status, err = ReadCompleteStatus(dir)
	if err != nil || status.Start == nil || status.Start.After == nil ||
		*status.Start.After != (Cursor{Slot: 30, Ordinal: 0}) {
		t.Fatalf("boundary status = %+v, %v", status, err)
	}
}

func TestExistingIndexRequiresExactResumeCursor(t *testing.T) {
	dir := t.TempDir()
	index, err := Open(dir, testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	batch := BatchDescriptor{
		ManifestSequence: 1, SidecarVersion: SupportedSidecarVersion,
		FromSlot: 34, ThroughSlot: 34, SHA256: strings.Repeat("c", 64),
	}
	if _, err := index.BeginBatch(batch); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Append(accountEvent(34, 0, []byte("durable"))); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}

	resumed, err := Open(dir, testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if err := resumed.BeginStream(&Cursor{Slot: 34, Ordinal: 1}); err == nil || !strings.Contains(err.Error(), "durable cursor") {
		t.Fatalf("wrong resume error = %v", err)
	}
	if err := resumed.BeginStream(&Cursor{Slot: 34, Ordinal: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := resumed.BeginBatch(batch); err != nil {
		t.Fatal(err)
	}
	if _, err := resumed.Append(rootEvent(34, 1, 33)); err != nil {
		t.Fatal(err)
	}
}

func TestExistingIndexAcceptsEmptyPageAtExactResumeCursor(t *testing.T) {
	dir := t.TempDir()
	index, err := Open(dir, testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index, 1, 34, 34)
	if _, err := index.Append(rootEvent(34, 0, 33)); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := ReadCompleteStatus(dir)
	if err != nil {
		t.Fatal(err)
	}

	var framed bytes.Buffer
	encoder := json.NewEncoder(&framed)
	if err := encoder.Encode(sourceFrame{RecordType: SourceRecordType, Source: testSource()}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(startFrame{
		RecordType: StartRecordType,
		Start:      &StartDescriptor{After: cloneCursorPtr(before.LastCursor)},
	}); err != nil {
		t.Fatal(err)
	}
	source, start, remaining, err := ReadPreamble(&framed)
	if err != nil || source != testSource() || !sameCursor(start.After, before.LastCursor) {
		t.Fatalf("empty page preamble = %+v %+v, %v", source, start, err)
	}

	resumed, err := Open(dir, source, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if err := resumed.BeginStream(start.After); err != nil {
		t.Fatal(err)
	}
	if stored, err := Ingest(t.Context(), resumed, remaining); err != nil || stored != 0 {
		t.Fatalf("empty page ingest = %d, %v", stored, err)
	}
	after, err := ReadCompleteStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !sameCursor(after.LastCursor, before.LastCursor) || after.ChainHead != before.ChainHead || after.Records != before.Records {
		t.Fatalf("empty page changed durable watermark: before=%+v after=%+v", before, after)
	}
}

func TestNewIndexAcceptsEmptyLatestPageThenNextBatch(t *testing.T) {
	dir := t.TempDir()
	tail := &Cursor{Slot: 40, Ordinal: 0}
	index, err := Open(dir, testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if err := index.BeginStream(tail); err != nil {
		t.Fatal(err)
	}
	if stored, err := Ingest(t.Context(), index, strings.NewReader("\n")); err != nil || stored != 0 {
		t.Fatalf("empty latest ingest = %d, %v", stored, err)
	}
	if index.last != nil || index.activeBatch != nil || len(index.batches) != 0 {
		t.Fatalf("empty latest page advanced index: last=%v active=%v batches=%d", index.last, index.activeBatch, len(index.batches))
	}

	var next bytes.Buffer
	encoder := json.NewEncoder(&next)
	for range 2 {
		if err := encoder.Encode(sourceFrame{RecordType: SourceRecordType, Source: testSource()}); err != nil {
			t.Fatal(err)
		}
		if err := encoder.Encode(startFrame{RecordType: StartRecordType, Start: &StartDescriptor{After: tail}}); err != nil {
			t.Fatal(err)
		}
	}
	batch := BatchDescriptor{
		ManifestSequence: 1, SidecarVersion: SupportedSidecarVersion,
		FromSlot: 41, ThroughSlot: 41, SHA256: strings.Repeat("a", 64),
	}
	if err := encoder.Encode(batchFrame{RecordType: BatchRecordType, Batch: batch}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(rootEvent(41, 0, 40)); err != nil {
		t.Fatal(err)
	}
	if stored, err := Ingest(t.Context(), index, &next); err != nil || stored != 1 {
		t.Fatalf("repeated empty pages then batch = %d, %v", stored, err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	status, err := ReadCompleteStatus(dir)
	if err != nil || !status.Complete || status.LastCursor == nil || status.LastCursor.Slot != 41 {
		t.Fatalf("next batch status = %+v, %v", status, err)
	}
}

func TestIngestStrictJSONAndFilter(t *testing.T) {
	index, err := Open(t.TempDir(), testSource(), Filter{Owner: testOwner})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index, 1, 30, 30)
	defer index.Close()
	bad := `{"schema_version":1,"Schema_Version":1}`
	if _, err := Ingest(context.Background(), index, strings.NewReader(bad)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate JSON error = %v", err)
	}
	event := accountEvent(30, 0, []byte("ok"))
	event.Account.Owner = otherOwner
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Ingest(context.Background(), index, bytes.NewReader(data)); err == nil || !strings.Contains(err.Error(), "filter") {
		t.Fatalf("filter error = %v", err)
	}
}

func TestIngestProgressReportsCountsWithoutEventData(t *testing.T) {
	index, err := Open(t.TempDir(), testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index, 1, 31, 31)
	defer index.Close()

	var input bytes.Buffer
	for _, event := range []Event{
		accountEvent(31, 0, []byte("private payload")),
		rootEvent(31, 1, 30),
	} {
		if err := json.NewEncoder(&input).Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	var counts []int
	stored, err := IngestWithProgress(context.Background(), index, &input, func(count int) {
		counts = append(counts, count)
	})
	if err != nil || stored != 2 || !slices.Equal(counts, []int{1, 2}) {
		t.Fatalf("stored=%d counts=%v error=%v", stored, counts, err)
	}
}

func TestIngestSyncsValidPrefixBeforeReturningAnInputError(t *testing.T) {
	dir := t.TempDir()
	index, err := Open(dir, testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index, 1, 32, 32)
	var input bytes.Buffer
	if err := json.NewEncoder(&input).Encode(accountEvent(32, 0, []byte("valid prefix"))); err != nil {
		t.Fatal(err)
	}
	input.WriteString("{not-json}\n")
	stored, err := Ingest(context.Background(), index, &input)
	if err == nil || stored != 1 {
		t.Fatalf("stored=%d error=%v", stored, err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	status, err := ReadStatus(dir)
	if err != nil || status.Complete || status.LastCursor == nil || status.LastCursor.Slot != 32 {
		t.Fatalf("incomplete status = %+v, %v", status, err)
	}
	if _, err := QueryAccounts(dir, Query{Limit: 1}); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete query error = %v", err)
	}
	reopened, err := Open(dir, testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, reopened, 1, 32, 32)
	defer reopened.Close()
	if added, err := reopened.Append(accountEvent(32, 0, []byte("valid prefix"))); err != nil || added {
		t.Fatalf("durable prefix replay = %t, %v", added, err)
	}
	if added, err := reopened.Append(rootEvent(32, 1, 31)); err != nil || !added {
		t.Fatalf("complete durable prefix = %t, %v", added, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	status, err = ReadCompleteStatus(dir)
	if err != nil || !status.Complete {
		t.Fatalf("completed status = %+v, %v", status, err)
	}
}

func TestIngestRejectsIncompleteEOFAndResumesAtRoot(t *testing.T) {
	dir := t.TempDir()
	index, err := Open(dir, testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index, 1, 34, 34)
	var input bytes.Buffer
	if err := json.NewEncoder(&input).Encode(accountEvent(34, 0, []byte("durable"))); err != nil {
		t.Fatal(err)
	}
	stored, err := Ingest(context.Background(), index, &input)
	if stored != 1 || err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete EOF stored=%d error=%v", stored, err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir, testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, reopened, 1, 34, 34)
	input.Reset()
	if err := json.NewEncoder(&input).Encode(rootEvent(34, 1, 33)); err != nil {
		t.Fatal(err)
	}
	stored, err = Ingest(context.Background(), reopened, &input)
	if stored != 1 || err != nil {
		t.Fatalf("resume root stored=%d error=%v", stored, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if status, err := ReadCompleteStatus(dir); err != nil || !status.Complete {
		t.Fatalf("resumed status = %+v, %v", status, err)
	}
}

func TestCompleteStatusRejectsBatchRootedBeforeThroughSlot(t *testing.T) {
	dir := t.TempDir()
	index, err := Open(dir, testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index, 1, 34, 35)
	if _, err := index.Append(rootEvent(34, 0, 33)); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}

	status, err := ReadStatus(dir)
	if err != nil || status.Complete {
		t.Fatalf("status = %+v, %v", status, err)
	}
	if _, err := ReadCompleteStatus(dir); err == nil || !strings.Contains(err.Error(), "terminal root at slot 35") {
		t.Fatalf("complete status error = %v", err)
	}
	if _, err := QueryAccounts(dir, Query{Limit: 1}); err == nil || !strings.Contains(err.Error(), "terminal root at slot 35") {
		t.Fatalf("query error = %v", err)
	}
}

func TestAppendUsesCachedJournalTimestamp(t *testing.T) {
	index, err := Open(t.TempDir(), testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index, 1, 33, 33)
	defer index.Close()

	future := time.Now().UTC().Add(time.Hour)
	index.lastRecordAt = future
	if added, err := index.Append(accountEvent(33, 0, nil)); err != nil || !added {
		t.Fatalf("append = %t, %v", added, err)
	}
	records := index.store.Records()
	if got := records[len(records)-1].At; got.Before(future) {
		t.Fatalf("record time = %s, want at least %s", got, future)
	}
}

func TestQueryDetectsBlobTampering(t *testing.T) {
	dir := t.TempDir()
	index, err := Open(dir, testSource(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginTestBatch(t, index, 1, 40, 40)
	event := accountEvent(40, 0, []byte("trusted"))
	if _, err := index.Append(event); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(event.Account.Data)
	path := filepath.Join(dir, "blobs", hex.EncodeToString(sum[:])+".bin")
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStatus(dir); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("status accepted tampered blob: %v", err)
	}
	if _, err := QueryAccounts(dir, Query{Limit: 1, IncludeData: true}); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("tampered blob error = %v", err)
	}
	if _, err := Open(dir, testSource(), Filter{}); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("reopen accepted tampered blob: %v", err)
	}
}

func TestReadStatusRejectsSemanticallyInvalidHashChainedRecord(t *testing.T) {
	dir := t.TempDir()
	store, err := journal.OpenRotating(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	if _, err := store.Append(at, eventHeader, "", header{
		IndexSchemaVersion: IndexSchemaVersion, EventSchemaVersion: EventSchemaVersion,
		Source: testSource(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(at, eventStart, "", StartDescriptor{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(at, eventBatch, "1", BatchDescriptor{
		ManifestSequence: 1, SidecarVersion: SupportedSidecarVersion,
		FromSlot: 1, ThroughSlot: 1, SHA256: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(at, eventAccount, "1:0", accountRecord{
		Cursor: Cursor{Slot: 1}, Pubkey: "not-an-address", Owner: testOwner,
		Lamports: 1, DataSHA256: strings.Repeat("0", 64), SourceSHA256: strings.Repeat("0", 64),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStatus(dir); err == nil || !strings.Contains(err.Error(), "account address") {
		t.Fatalf("semantically invalid record error = %v", err)
	}
}

func TestReadStatusRejectsRecoveredRootFinalityFromAnotherClusterMode(t *testing.T) {
	for _, test := range []struct {
		name    string
		cluster string
		classic bool
	}{
		{name: "classic root on Alpenglow", cluster: "alpenglow", classic: true},
		{name: "Alpenglow root on classic", cluster: "devnet"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := journal.OpenRotating(filepath.Join(dir, "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			at := time.Now().UTC()
			source := testSource()
			source.Cluster = test.cluster
			for _, record := range []struct {
				typ, key string
				payload  any
			}{
				{eventHeader, "", header{IndexSchemaVersion: IndexSchemaVersion, EventSchemaVersion: EventSchemaVersion, Source: source}},
				{eventStart, "", StartDescriptor{}},
				{eventBatch, "1", BatchDescriptor{ManifestSequence: 1, SidecarVersion: SupportedSidecarVersion, FromSlot: 1, ThroughSlot: 1, SHA256: strings.Repeat("a", 64)}},
			} {
				if _, err := store.Append(at, record.typ, record.key, record.payload); err != nil {
					t.Fatal(err)
				}
			}
			root := rootEvent(1, 0, 0)
			if test.classic {
				root.Root.FinalitySource = FinalityRPCFinalized
				root.Root.BlockID, root.Root.ParentBlockID = "", ""
			}
			if _, err := store.Append(at, eventRoot, "1:0", rootRecord{
				Cursor: root.Cursor, RootedSlot: *root.Root, SourceSHA256: strings.Repeat("b", 64),
			}); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadStatus(dir); err == nil || !strings.Contains(err.Error(), "bound") {
				t.Fatalf("recovered cross-mode root error = %v", err)
			}
		})
	}
}

func TestReadStatusRejectsRecoveredDerivedTransactionVersion(t *testing.T) {
	dir := t.TempDir()
	store, err := journal.OpenRotating(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	event := transactionEvent(80, 0)
	sourceHash, version, err := validateEvent(event, Filter{}, "alpenglow")
	if err != nil || version != TransactionVersionLegacy {
		t.Fatalf("validate fixture = %q, %v", version, err)
	}
	for _, record := range []struct {
		typ, key string
		payload  any
	}{
		{eventHeader, "", header{IndexSchemaVersion: IndexSchemaVersion, EventSchemaVersion: EventSchemaVersion, Source: testSource()}},
		{eventStart, "", StartDescriptor{}},
		{eventBatch, "1", BatchDescriptor{ManifestSequence: 1, SidecarVersion: SupportedSidecarVersion, FromSlot: 80, ThroughSlot: 80, SHA256: strings.Repeat("a", 64)}},
		{eventTransaction, event.Cursor.String(), transactionRecord{
			Cursor: event.Cursor, indexedTransaction: indexedTransaction(*event.Transaction),
			Version: TransactionVersionV0, SourceSHA256: sourceHash,
		}},
	} {
		if _, err := store.Append(at, record.typ, record.key, record.payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStatus(dir); err == nil || !strings.Contains(err.Error(), "derived version") {
		t.Fatalf("derived transaction version error = %v", err)
	}
}

func TestReadStatusRejectsRecoveredRootLineageBreaks(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*RootedSlot)
		want string
	}{
		{name: "parent blockhash", edit: func(root *RootedSlot) {
			root.ParentBlockhash = testOwner
		}, want: "parent blockhash"},
		{name: "parent block ID", edit: func(root *RootedSlot) {
			root.ParentBlockID = testOwner
		}, want: "parent block ID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := journal.OpenRotating(filepath.Join(dir, "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			at := time.Now().UTC()
			for _, record := range []struct {
				typ, key string
				payload  any
			}{
				{eventHeader, "", header{IndexSchemaVersion: IndexSchemaVersion, EventSchemaVersion: EventSchemaVersion, Source: testSource()}},
				{eventStart, "", StartDescriptor{}},
				{eventBatch, "1", BatchDescriptor{ManifestSequence: 1, SidecarVersion: SupportedSidecarVersion, FromSlot: 90, ThroughSlot: 91, SHA256: strings.Repeat("a", 64)}},
			} {
				if _, err := store.Append(at, record.typ, record.key, record.payload); err != nil {
					t.Fatal(err)
				}
			}
			first := rootEvent(90, 0, 89)
			second := rootEvent(91, 0, 90)
			test.edit(second.Root)
			for _, event := range []Event{first, second} {
				if _, err := store.Append(at, eventRoot, event.Cursor.String(), rootRecord{
					Cursor: event.Cursor, RootedSlot: *event.Root, SourceSHA256: strings.Repeat("b", 64),
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadStatus(dir); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("recovered lineage error = %v", err)
			}
		})
	}
}

func TestReadStatusExplainsSourceLessIndexMigration(t *testing.T) {
	dir := t.TempDir()
	store, err := journal.OpenRotating(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(time.Now().UTC(), eventHeader, "", map[string]any{
		"schema_version": uint32(2), "filter": Filter{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStatus(dir); err == nil || !strings.Contains(err.Error(), "preserve it for audit") ||
		!strings.Contains(err.Error(), "framed rooted feed") {
		t.Fatalf("source-less migration error = %v", err)
	}
}

func TestReadStatusRejectsV3WithoutStreamStartBinding(t *testing.T) {
	dir := t.TempDir()
	store, err := journal.OpenRotating(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(time.Now().UTC(), eventHeader, "", header{
		IndexSchemaVersion: 3, Source: testSource(), Filter: Filter{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStatus(dir); err == nil || !strings.Contains(err.Error(), "private v5 index") {
		t.Fatalf("v3 migration error = %v", err)
	}
}

func TestReadStatusExplainsV4Rebuild(t *testing.T) {
	dir := t.TempDir()
	store, err := journal.OpenRotating(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(time.Now().UTC(), eventHeader, "", map[string]any{
		"index_schema_version": uint32(4), "source": testSource(), "filter": Filter{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStatus(dir); err == nil || !strings.Contains(err.Error(), "schema v4") ||
		!strings.Contains(err.Error(), "preserve it for audit") ||
		!strings.Contains(err.Error(), "event-schema-v3") {
		t.Fatalf("v4 migration error = %v", err)
	}
}

func TestHeaderAndEventVersionsFailClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		header header
	}{
		{name: "old index", header: header{IndexSchemaVersion: IndexSchemaVersion - 1, EventSchemaVersion: EventSchemaVersion}},
		{name: "future index", header: header{IndexSchemaVersion: IndexSchemaVersion + 1, EventSchemaVersion: EventSchemaVersion}},
		{name: "old event", header: header{IndexSchemaVersion: IndexSchemaVersion, EventSchemaVersion: EventSchemaVersion - 1}},
		{name: "future event", header: header{IndexSchemaVersion: IndexSchemaVersion, EventSchemaVersion: EventSchemaVersion + 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateHeaderVersions(test.header); err == nil {
				t.Fatal("unsupported header versions were accepted")
			}
		})
	}
	for _, schema := range []uint32{EventSchemaVersion - 1, EventSchemaVersion + 1} {
		if _, _, err := validateEvent(Event{SchemaVersion: schema}, Filter{}, "alpenglow"); err == nil ||
			!strings.Contains(err.Error(), "schema version") {
			t.Fatalf("event schema %d error = %v", schema, err)
		}
	}
}

func TestParseCursor(t *testing.T) {
	cursor, err := ParseCursor("42:7")
	if err != nil || cursor != (Cursor{Slot: 42, Ordinal: 7}) {
		t.Fatalf("cursor = %+v, %v", cursor, err)
	}
	for _, value := range []string{"", "42", "042:7", "42:07", "42:7:1", "-1:0"} {
		if _, err := ParseCursor(value); err == nil {
			t.Fatalf("invalid cursor %q accepted", value)
		}
	}
}

func accountEvent(slot uint64, ordinal uint32, data []byte) Event {
	return Event{
		SchemaVersion: SchemaVersion,
		Cursor:        Cursor{Slot: slot, Ordinal: ordinal},
		Kind:          "account_updated",
		Account: &AccountUpdate{
			Pubkey: testAccount, Owner: testOwner, Lamports: 7,
			RentEpoch: 9, Data: data,
		},
	}
}

func rootEvent(slot uint64, ordinal uint32, parent uint64) Event {
	return Event{
		SchemaVersion: SchemaVersion,
		Cursor:        Cursor{Slot: slot, Ordinal: ordinal},
		Kind:          "slot_rooted",
		Root: &RootedSlot{
			ParentSlot: parent, Blockhash: testBankhash, ParentBlockhash: testBankhash,
			Bankhash: testBankhash, BlockID: testBankhash, ParentBlockID: testBankhash,
			FinalitySource: FinalityAlpenglowCertificate, AccountCount: ordinal,
		},
	}
}

func transactionEvent(slot uint64, index uint32) Event {
	return transactionEventVersion(slot, index, solanago.MessageVersionLegacy, 1)
}

func transactionEventVersion(slot uint64, index uint32, version solanago.MessageVersion, dataBytes int) Event {
	privateKey := solanago.PrivateKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize)))
	payer := privateKey.PublicKey()
	program, err := solanago.PublicKeyFromBase58(testOwner)
	if err != nil {
		panic(err)
	}
	recentBlockhash, err := solanago.HashFromBase58(testBankhash)
	if err != nil {
		panic(err)
	}
	message := solanago.Message{
		Header: solanago.MessageHeader{
			NumRequiredSignatures: 1, NumReadonlyUnsignedAccounts: 1,
		},
		AccountKeys:     solanago.PublicKeySlice{payer, program},
		RecentBlockhash: recentBlockhash,
		Instructions: []solanago.CompiledInstruction{{
			ProgramIDIndex: 1, Accounts: []uint16{0}, Data: bytes.Repeat([]byte{1}, dataBytes),
		}},
	}
	if _, err := message.SetVersion(version); err != nil {
		panic(err)
	}
	transaction := &solanago.Transaction{Message: message}
	if _, err := transaction.Sign(func(key solanago.PublicKey) *solanago.PrivateKey {
		if key == payer {
			return &privateKey
		}
		return nil
	}); err != nil {
		panic(err)
	}
	wire, err := transaction.MarshalBinary()
	if err != nil {
		panic(err)
	}
	messageHash, err := transactionMessageHash(transaction)
	if err != nil {
		panic(err)
	}
	return Event{
		SchemaVersion: SchemaVersion,
		Cursor:        Cursor{Slot: slot, Ordinal: index},
		Kind:          "transaction_executed",
		Transaction: &Transaction{
			Index: index, Signature: transaction.Signatures[0].String(),
			Transaction: wire, MessageHash: solanago.Hash(messageHash).String(),
			AccountKeys: []string{payer.String(), testOwner},
			Succeeded:   true, ComputeUnits: 12, Logs: []string{"Program success"},
		},
	}
}

func cloneAccount(value *AccountUpdate) *AccountUpdate {
	copy := *value
	copy.Data = bytes.Clone(value.Data)
	return &copy
}

func cloneTransaction(value *Transaction) Transaction {
	copy := *value
	copy.Transaction = bytes.Clone(value.Transaction)
	copy.AccountKeys = append([]string(nil), value.AccountKeys...)
	copy.Logs = append([]string(nil), value.Logs...)
	copy.Inner = cloneInner(value.Inner)
	copy.ReturnData = cloneReturnData(value.ReturnData)
	return copy
}
