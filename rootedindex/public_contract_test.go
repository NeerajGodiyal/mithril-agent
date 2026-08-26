package rootedindex

import (
	"bytes"
	"os"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/solana"
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
	if source.Cluster != "mainnet-beta" || source.GenesisHash != solana.MainnetBetaGenesisHash ||
		source.AccountsDBRootRunID != "0123456789abcdef0123456789abcdef" ||
		start.After == nil || *start.After != (Cursor{Slot: 29}) {
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
	if stored, err := Ingest(t.Context(), index, remaining); err != nil || stored != 5 {
		t.Fatalf("public contract ingest = %d, %v", stored, err)
	}
	status, err := ReadCompleteStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Complete || status.Provenance != ClassicFinalizedRootedProvenance ||
		status.Transactions != 2 || status.Accounts != 1 || status.Roots != 2 ||
		status.Batches != 2 || status.Start == nil || status.Start.After == nil ||
		*status.Start.After != (Cursor{Slot: 29}) || status.FirstBatch == nil ||
		status.FirstBatch.ManifestSequence != 1 || status.LastBatch == nil ||
		status.LastBatch.ManifestSequence != 2 || status.LastRoot == nil ||
		*status.LastRoot != (Cursor{Slot: 31, Ordinal: 1}) {
		t.Fatalf("public contract status = %+v", status)
	}

	accounts, err := QueryAccounts(dir, Query{Limit: 10, IncludeData: true})
	if err != nil || len(accounts) != 1 || accounts[0].Cursor != (Cursor{Slot: 30, Ordinal: 1}) ||
		accounts[0].Pubkey != "11111111111111111111111111111111" ||
		accounts[0].Owner != "ComputeBudget111111111111111111111111111111" ||
		accounts[0].Lamports != 7 || accounts[0].Executable || accounts[0].RentEpoch != 9 ||
		accounts[0].Tombstone || !bytes.Equal(accounts[0].Data, []byte("golden")) {
		t.Fatalf("public contract accounts = %+v, %v", accounts, err)
	}

	transactions, err := QueryTransactions(dir, TransactionQuery{Limit: 10, IncludePayload: true})
	if err != nil || len(transactions) != 2 {
		t.Fatalf("public contract transactions = %+v, %v", transactions, err)
	}
	failed, succeeded := transactions[0], transactions[1]
	if failed.Cursor != (Cursor{Slot: 31}) || failed.Index != 0 ||
		failed.Signature != "2AFv15MNPuA84RmU66xw2uMzGipcVxNpzAffoacGVvjFue3CBmf633fAWuiP9cwL9C3z3CJiGgRSFjJfeEcA6QX" ||
		failed.Succeeded || failed.Failure != "InstructionError(0, Custom(7))" ||
		failed.ComputeUnits != 77 || len(failed.AccountKeys) != 2 ||
		failed.AccountKeys[0] != "11111111111111111111111111111111" ||
		failed.AccountKeys[1] != "ComputeBudget111111111111111111111111111111" ||
		!failed.LogsTruncated || len(failed.Logs) != 2 || failed.Logs[0] != "Program log: failed" ||
		failed.Logs[1] != "Log truncated" || len(failed.Inner) != 0 || failed.ReturnData != nil {
		t.Fatalf("public contract failed transaction = %+v", failed)
	}
	if succeeded.Cursor != (Cursor{Slot: 30}) || succeeded.Index != 0 ||
		succeeded.Signature != "1111111111111111111111111111111111111111111111111111111111111111" ||
		!succeeded.Succeeded || succeeded.Failure != "" || succeeded.ComputeUnits != 123 ||
		len(succeeded.AccountKeys) != 2 ||
		succeeded.AccountKeys[0] != "11111111111111111111111111111111" ||
		succeeded.AccountKeys[1] != "ComputeBudget111111111111111111111111111111" ||
		succeeded.LogsTruncated || len(succeeded.Logs) != 1 || succeeded.Logs[0] != "Program log: ok" ||
		len(succeeded.Inner) != 1 || succeeded.Inner[0].Index != 0 ||
		len(succeeded.Inner[0].Instructions) != 1 ||
		succeeded.Inner[0].Instructions[0].ProgramIDIndex != 1 ||
		len(succeeded.Inner[0].Instructions[0].Accounts) != 1 ||
		succeeded.Inner[0].Instructions[0].Accounts[0] != 0 ||
		!bytes.Equal(succeeded.Inner[0].Instructions[0].Data, []byte{9, 8}) ||
		succeeded.ReturnData == nil ||
		succeeded.ReturnData.ProgramID != "ComputeBudget111111111111111111111111111111" ||
		!bytes.Equal(succeeded.ReturnData.Data, []byte{7, 6}) {
		t.Fatalf("public contract successful transaction = %+v", succeeded)
	}

	legacy, err := solana.DecodeLegacyMessage(succeeded.Message)
	if err != nil || len(legacy.AccountKeys) != 2 || len(legacy.Instructions) != 1 ||
		solana.Encode(legacy.AccountKeys[0][:]) != succeeded.AccountKeys[0] ||
		solana.Encode(legacy.AccountKeys[1][:]) != succeeded.AccountKeys[1] ||
		legacy.Instructions[0].ProgramIndex != 1 ||
		!bytes.Equal(legacy.Instructions[0].Accounts, []uint8{0}) ||
		!bytes.Equal(legacy.Instructions[0].Data, []byte{9, 8}) {
		t.Fatalf("public contract legacy message = %+v, %v", legacy, err)
	}
	v0, err := solana.DecodeV0Message(failed.Message, nil)
	if err != nil || len(v0.StaticAccountKeys) != 2 || len(v0.AccountKeys) != 2 ||
		len(v0.AddressTableLookups) != 0 || len(v0.Instructions) != 1 ||
		solana.Encode(v0.AccountKeys[0][:]) != failed.AccountKeys[0] ||
		solana.Encode(v0.AccountKeys[1][:]) != failed.AccountKeys[1] ||
		v0.Instructions[0].ProgramIndex != 1 ||
		!bytes.Equal(v0.Instructions[0].Accounts, []uint8{0}) ||
		!bytes.Equal(v0.Instructions[0].Data, []byte{9, 8}) {
		t.Fatalf("public contract v0 message = %+v, %v", v0, err)
	}
}
