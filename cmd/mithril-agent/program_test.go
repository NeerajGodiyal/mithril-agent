package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/programinterface"
	"github.com/Overclock-Validator/mithril-agent/rootedindex"
	solanago "github.com/solana-foundation/solana-go/v2"
)

const programCommandAddress = "11111111111111111111111111111111"

func writeProgramCommandIDL(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "counter.json")
	idl := `{"address":"11111111111111111111111111111111","metadata":{"name":"counter","version":"0.1.0","spec":"0.1.0"},"instructions":[{"name":"increment","discriminator":[11,18,104,9,104,174,59,33],"accounts":[{"name":"state","writable":true}],"args":[]}]}`
	if err := os.WriteFile(path, []byte(idl), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProgramDecodeAccountFromPinnedInterface(t *testing.T) {
	registry, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	idl := []byte(`{
  "address":"11111111111111111111111111111111",
  "metadata":{"name":"counter","version":"0.1.0","spec":"0.1.0"},
  "instructions":[],
  "accounts":[{"name":"Counter","discriminator":[1,2,3,4,5,6,7,8]}],
  "types":[{"name":"Counter","type":{"kind":"struct","fields":[{"name":"value","type":"u64"}]}}]
}`)
	pin, err := programinterface.Pin(registry, programCommandAddress, idl)
	if err != nil {
		t.Fatal(err)
	}
	data := binary.LittleEndian.AppendUint64([]byte{1, 2, 3, 4, 5, 6, 7, 8}, 42)
	dataPath := filepath.Join(t.TempDir(), "account.bin")
	if err := os.WriteFile(dataPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run([]string{
		"program", "decode-account", "--json", "--registry", registry,
		"--program", programCommandAddress, "--sha256", pin.SHA256,
		"--account-type", "Counter", "--data", dataPath,
	}, &output); err != nil {
		t.Fatal(err)
	}
	var decoded programDecodedOutput
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	value, ok := decoded.Value.(map[string]any)
	if !ok || value["value"] != "42" || decoded.Kind != "account" {
		t.Fatalf("decoded = %+v", decoded)
	}
	if decoded.Provenance != programLocalProvenance || decoded.Finality != programLocalFinality || decoded.Cursor != nil {
		t.Fatalf("local decode provenance = %+v", decoded)
	}
	if decoded.Scope != "local_file" || decoded.Current {
		t.Fatalf("local decode scope = %+v", decoded)
	}
}

func TestProgramDecodeInstructionFromPinnedInterface(t *testing.T) {
	registry, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	idl, err := os.ReadFile(writeProgramCommandIDL(t))
	if err != nil {
		t.Fatal(err)
	}
	pin, err := programinterface.Pin(registry, programCommandAddress, idl)
	if err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Join(t.TempDir(), "instruction.bin")
	if err := os.WriteFile(dataPath, []byte{11, 18, 104, 9, 104, 174, 59, 33}, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run([]string{
		"program", "decode-instruction", "--json", "--registry", registry,
		"--program", programCommandAddress, "--sha256", pin.SHA256,
		"--instruction", "increment", "--data", dataPath,
	}, &output); err != nil {
		t.Fatal(err)
	}
	var decoded programDecodedOutput
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Kind != "instruction" || decoded.Name != "increment" || decoded.Bytes != 8 {
		t.Fatalf("decoded = %+v", decoded)
	}
	if decoded.Provenance != programLocalProvenance || decoded.Finality != programLocalFinality {
		t.Fatalf("instruction decode provenance = %+v", decoded)
	}
}

func TestProgramDecodeInstructionFromRootedV1Transaction(t *testing.T) {
	registry, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	idl := []byte(`{
  "address":"11111111111111111111111111111111",
  "metadata":{"name":"large","version":"0.1.0","spec":"0.1.0"},
  "instructions":[{"name":"write","discriminator":[1],"accounts":[{"name":"payer","signer":true}],"args":[{"name":"data","type":"bytes"}]}]
}`)
	pin, err := programinterface.Pin(registry, programCommandAddress, idl)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 1300)
	payload[0] = 1
	binary.LittleEndian.PutUint32(payload[1:5], uint32(len(payload)-5))
	transaction := testRootedTransactionWithInstructionVersion(
		t, programCommandAddress, payload, nil, solanago.MessageVersionV1,
	)
	indexDir := filepath.Join(t.TempDir(), "activity")
	index, err := rootedindex.Open(indexDir, testRootedSource(), rootedindex.Filter{Mention: programCommandAddress})
	if err != nil {
		t.Fatal(err)
	}
	beginRootedBatch(t, index, 1, 2, 2)
	if _, err := index.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion, Cursor: rootedindex.Cursor{Slot: 2},
		Kind: "transaction_executed", Transaction: transaction,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion, Cursor: rootedindex.Cursor{Slot: 2, Ordinal: 1},
		Kind: "slot_rooted", Root: testRootedSlot(testRootedSource(), 1, 1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}

	args := []string{
		"program", "decode-instruction", "--json", "--registry", registry,
		"--program", programCommandAddress, "--sha256", pin.SHA256,
		"--instruction", "write", "--index-dir", indexDir,
		"--signature", transaction.Signature, "--outer-index", "0",
	}
	var output bytes.Buffer
	if err := run(args, &output); err != nil {
		t.Fatal(err)
	}
	var decoded decodedProgramInstruction
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Version != rootedindex.TransactionVersionV1 || decoded.Bytes != len(payload) ||
		decoded.Signature != transaction.Signature || decoded.MessageHash != transaction.MessageHash ||
		decoded.Provenance != rootedindex.AlpenglowRootedProvenance ||
		decoded.Finality != rootedindex.RootedFinality || !decoded.Succeeded ||
		decoded.Scope != "rooted_outer_instruction" || decoded.Location != "outer" || !decoded.Signed ||
		decoded.Current || decoded.InnerIndex != nil || len(decoded.Accounts) != 1 {
		t.Fatalf("decoded rooted v1 instruction = %+v", decoded)
	}
	output.Reset()
	humanArgs := append([]string(nil), args[:2]...)
	humanArgs = append(humanArgs, args[3:]...)
	if err := run(humanArgs, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Version: v1", "Message hash: " + transaction.MessageHash,
		"Provenance: " + rootedindex.AlpenglowRootedProvenance,
		"Finality: " + rootedindex.RootedFinality, "Accounts:\n  - ", "Arguments:\n",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("human rooted decode omits %q:\n%s", want, output.String())
		}
	}
	args[len(args)-1] = "1"
	if err := run(args, io.Discard); err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("out-of-range outer instruction error = %v", err)
	}
}

func TestDecodeRootedCPIInstructionUsesRuntimeLimit(t *testing.T) {
	report, err := programinterface.Inspect([]byte(`{
  "address":"11111111111111111111111111111111",
  "metadata":{"name":"large","version":"0.1.0","spec":"0.1.0"},
  "instructions":[{"name":"write","discriminator":[1],"accounts":[],"args":[{"name":"data","type":"bytes"}]}]
}`), programCommandAddress)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 5000)
	payload[0] = 1
	binary.LittleEndian.PutUint32(payload[1:5], uint32(len(payload)-5))
	transaction := testRootedTransaction(t, programCommandAddress, nil)
	transaction.Inner = []rootedindex.InnerInstructions{{Index: 0, Instructions: []rootedindex.CompiledInstruction{{
		ProgramIDIndex: 1, Data: payload,
	}}}}
	decoded, err := decodeRootedProgramInnerInstruction(
		report, "write", rootedindex.TransactionResult{
			Cursor: rootedindex.Cursor{Slot: 7}, Signature: transaction.Signature,
			Version: rootedindex.TransactionVersionLegacy, MessageHash: transaction.MessageHash,
			AccountKeys: transaction.AccountKeys, Succeeded: true,
			Transaction: transaction.Transaction, Inner: transaction.Inner,
		}, 0, 0, rootedindex.AlpenglowRootedProvenance, rootedindex.RootedFinality,
	)
	if err != nil || decoded.Bytes != len(payload) || decoded.Signed {
		t.Fatalf("decoded large CPI instruction = %+v, %v", decoded, err)
	}
}

func TestDecodeRootedV0InstructionUsesRecordedLookupAddresses(t *testing.T) {
	report, err := programinterface.Inspect([]byte(`{
  "address":"11111111111111111111111111111111",
  "metadata":{"name":"lookup","version":"0.1.0","spec":"0.1.0"},
  "instructions":[{"name":"write","discriminator":[1],"accounts":[{"name":"loaded"}],"args":[]}]
}`), programCommandAddress)
	if err != nil {
		t.Fatal(err)
	}
	transaction := testRootedV0LookupTransaction(t, programCommandAddress, programSimulationState, []byte{1})
	result := rootedindex.TransactionResult{
		Cursor: rootedindex.Cursor{Slot: 7}, Signature: transaction.Signature,
		Version: rootedindex.TransactionVersionV0, MessageHash: transaction.MessageHash,
		AccountKeys: transaction.AccountKeys, Succeeded: true, Transaction: transaction.Transaction,
		Inner: []rootedindex.InnerInstructions{{Index: 0, Instructions: []rootedindex.CompiledInstruction{{
			ProgramIDIndex: 2, Accounts: []uint16{0}, Data: []byte{1},
		}}}},
	}
	decoded, err := decodeRootedProgramInstruction(
		report, "write", result, 0, rootedindex.AlpenglowRootedProvenance, rootedindex.RootedFinality,
	)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Version != rootedindex.TransactionVersionV0 || len(decoded.Accounts) != 1 ||
		decoded.Accounts[0] != programSimulationState {
		t.Fatalf("decoded v0 lookup instruction = %+v", decoded)
	}
	result.Succeeded = false
	result.Failure = "instruction error"
	failed, err := decodeRootedProgramInstruction(report, "write", result, 0, "", "")
	if err != nil || failed.Succeeded || failed.Failure != result.Failure {
		t.Fatalf("failed rooted outer instruction = %+v, %v", failed, err)
	}
	result.Succeeded = true
	result.Failure = ""
	report.Program = programSimulationState
	inner, err := decodeRootedProgramInnerInstruction(report, "write", result, 0, 0, "", "")
	if err != nil || inner.Location != "inner" || inner.Signed || inner.InnerIndex == nil ||
		inner.Accounts[0] != result.AccountKeys[0] {
		t.Fatalf("decoded v0 lookup program CPI = %+v, %v", inner, err)
	}
	if _, err := decodeRootedProgramInstruction(report, "write", result, 0, "", ""); err == nil || !strings.Contains(err.Error(), "pinned program") {
		t.Fatalf("wrong-program rooted instruction error = %v", err)
	}
}

func TestProgramDecodeAccountFromRootedIndex(t *testing.T) {
	registry, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	idl := []byte(`{
  "address":"11111111111111111111111111111111",
  "metadata":{"name":"counter","version":"0.1.0","spec":"0.1.0"},
  "instructions":[],
  "accounts":[{"name":"Counter","discriminator":[1,2,3,4,5,6,7,8]}],
  "types":[{"name":"Counter","type":{"kind":"struct","fields":[{"name":"value","type":"u64"}]}}]
}`)
	pin, err := programinterface.Pin(registry, programCommandAddress, idl)
	if err != nil {
		t.Fatal(err)
	}
	data := binary.LittleEndian.AppendUint64([]byte{1, 2, 3, 4, 5, 6, 7, 8}, 42)
	indexDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	index, err := rootedindex.Open(indexDir, testRootedSource(), rootedindex.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginRootedBatch(t, index, 1, 1, 1)
	account := "SysvarC1ock11111111111111111111111111111111"
	if _, err := index.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion,
		Cursor:        rootedindex.Cursor{Slot: 1},
		Kind:          "account_updated",
		Account: &rootedindex.AccountUpdate{
			Pubkey: account, Owner: programCommandAddress, Lamports: 1, Data: data,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion,
		Cursor:        rootedindex.Cursor{Slot: 1, Ordinal: 1},
		Kind:          "slot_rooted",
		Root:          testRootedSlot(testRootedSource(), 0, 0, 1),
	}); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run([]string{
		"program", "decode-account", "--json", "--registry", registry,
		"--program", programCommandAddress, "--sha256", pin.SHA256,
		"--account-type", "Counter", "--index-dir", indexDir, "--account", account,
	}, &output); err != nil {
		t.Fatal(err)
	}
	var decoded programDecodedOutput
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	value, ok := decoded.Value.(map[string]any)
	if !ok || value["value"] != "42" {
		t.Fatalf("decoded = %+v", decoded)
	}
	if decoded.Provenance != rootedindex.RootedProvenance || decoded.Finality != rootedindex.RootedFinality || decoded.Cursor == nil {
		t.Fatalf("rooted decode provenance = %+v", decoded)
	}
	if decoded.Scope != "current_account_state" || !decoded.Current {
		t.Fatalf("rooted decode scope = %+v", decoded)
	}
}

func TestProgramAccountIndexScopeDoesNotCallOwnerHistoryCurrent(t *testing.T) {
	if scope, current := programAccountIndexScope(
		rootedindex.Filter{Owner: programCommandAddress}, programCommandAddress,
	); scope != "owner_matching_history" || current {
		t.Fatalf("owner-filtered scope = %q, current = %t", scope, current)
	}
	if scope, current := programAccountIndexScope(
		rootedindex.Filter{Account: programCommandAddress}, programCommandAddress,
	); scope != "current_account_state" || !current {
		t.Fatalf("exact-account scope = %q, current = %t", scope, current)
	}
}

func TestProgramDecodeEventFromRootedTransaction(t *testing.T) {
	registry, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	idl := []byte(`{
  "address":"11111111111111111111111111111111",
  "metadata":{"name":"counter","version":"0.1.0","spec":"0.1.0"},
  "instructions":[],
  "events":[{"name":"Changed","discriminator":[9,10,11,12,13,14,15,16]}],
  "types":[{"name":"Changed","type":{"kind":"struct","fields":[{"name":"value","type":"u64"}]}}]
}`)
	pin, err := programinterface.Pin(registry, programCommandAddress, idl)
	if err != nil {
		t.Fatal(err)
	}
	payload := binary.LittleEndian.AppendUint64([]byte{9, 10, 11, 12, 13, 14, 15, 16}, 42)
	encoded := base64.StdEncoding.EncodeToString(payload)
	indexDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	index, err := rootedindex.Open(indexDir, testRootedSource(), rootedindex.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	beginRootedBatch(t, index, 1, 2, 2)
	transaction := testRootedTransaction(t, programCommandAddress, []string{
		"Program ComputeBudget111111111111111111111111111111 invoke [1]",
		"Program data: " + base64.StdEncoding.EncodeToString([]byte("other")) + " " + encoded,
		"Program ComputeBudget111111111111111111111111111111 success",
		"Program " + programCommandAddress + " invoke [1]",
		"Program data: " + base64.StdEncoding.EncodeToString([]byte("other")) + " " + encoded,
		"Program " + programCommandAddress + " success",
	})
	signature := transaction.Signature
	if _, err := index.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion,
		Cursor:        rootedindex.Cursor{Slot: 2},
		Kind:          "transaction_executed",
		Transaction:   transaction,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion,
		Cursor:        rootedindex.Cursor{Slot: 2, Ordinal: 1},
		Kind:          "slot_rooted",
		Root:          testRootedSlot(testRootedSource(), 1, 1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := run([]string{
		"program", "decode-event", "--json", "--registry", registry,
		"--program", programCommandAddress, "--sha256", pin.SHA256,
		"--event-type", "Changed", "--index-dir", indexDir,
		"--signature", signature,
	}, &output); err != nil {
		t.Fatal(err)
	}
	var decoded []decodedProgramEvent
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	value, ok := decoded[0].Value.(map[string]any)
	if len(decoded) != 1 || !ok || value["value"] != "42" || decoded[0].LogIndex != 4 {
		t.Fatalf("decoded = %+v", decoded)
	}
	if decoded[0].Provenance != rootedindex.RootedProvenance || decoded[0].Finality != rootedindex.RootedFinality {
		t.Fatalf("event provenance = %+v", decoded[0])
	}
}

func TestDecodeProgramEventsRejectsInconsistentProgramStack(t *testing.T) {
	_, err := decodeProgramEvents(programinterface.Report{
		Program: programCommandAddress,
		Events:  []programinterface.DataDefinition{{Name: "Changed", Discriminator: "01"}},
	}, "Changed", rootedindex.TransactionResult{
		Succeeded: true,
		Logs: []string{
			"Program " + programCommandAddress + " invoke [1]",
			"Program ComputeBudget111111111111111111111111111111 success",
		},
	}, rootedindex.RootedProvenance, rootedindex.RootedFinality)
	if err == nil || !strings.Contains(err.Error(), "stack is inconsistent") {
		t.Fatalf("inconsistent stack error = %v", err)
	}
}

func TestDecodeProgramEventsRejectsIncompleteProgramStack(t *testing.T) {
	_, err := decodeProgramEvents(programinterface.Report{
		Program: programCommandAddress,
		Events:  []programinterface.DataDefinition{{Name: "Changed", Discriminator: "01"}},
	}, "Changed", rootedindex.TransactionResult{
		Succeeded: true,
		Logs:      []string{"Program " + programCommandAddress + " invoke [1]"},
	}, rootedindex.RootedProvenance, rootedindex.RootedFinality)
	if err == nil || !strings.Contains(err.Error(), "stack is incomplete") {
		t.Fatalf("incomplete stack error = %v", err)
	}
}

func TestDecodeProgramEventsRejectsMatchingPrefixWhenLogsAreTruncated(t *testing.T) {
	_, err := decodeProgramEvents(programinterface.Report{
		Program: programCommandAddress,
		Events:  []programinterface.DataDefinition{{Name: "Changed", Discriminator: "01"}},
	}, "Changed", rootedindex.TransactionResult{
		Succeeded:     true,
		LogsTruncated: true,
		Logs: []string{
			"Program " + programCommandAddress + " invoke [1]",
			"Program data: AQ==",
		},
	}, rootedindex.RootedProvenance, rootedindex.RootedFinality)
	if err == nil || !strings.Contains(err.Error(), "cannot prove a complete result") {
		t.Fatalf("truncated matching-prefix error = %v", err)
	}
}

func TestDecodeProgramEventsRejectsFailedTransaction(t *testing.T) {
	_, err := decodeProgramEvents(programinterface.Report{}, "", rootedindex.TransactionResult{Succeeded: false}, "", "")
	if err == nil || !strings.Contains(err.Error(), "transaction failed") {
		t.Fatalf("failed transaction error = %v", err)
	}
}

func TestProgramInspectPinAndShow(t *testing.T) {
	idlPath := writeProgramCommandIDL(t)
	registry, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var inspect bytes.Buffer
	if err := run([]string{"program", "inspect", "--idl", idlPath, "--program", programCommandAddress}, &inspect); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Program interface verified", "increment", "Walletless: no key loaded"} {
		if !strings.Contains(inspect.String(), want) {
			t.Fatalf("inspect output omitted %q: %q", want, inspect.String())
		}
	}

	var pin bytes.Buffer
	if err := run([]string{"program", "pin", "--json", "--idl", idlPath,
		"--program", programCommandAddress, "--registry", registry}, &pin); err != nil {
		t.Fatal(err)
	}
	var pinned programinterface.PinResult
	if err := json.Unmarshal(pin.Bytes(), &pinned); err != nil {
		t.Fatal(err)
	}
	if !pinned.Created || pinned.SHA256 == "" || pinned.Path == "" {
		t.Fatalf("pin result = %+v", pinned)
	}

	var show bytes.Buffer
	if err := run([]string{"program", "show", "--program", programCommandAddress,
		"--sha256", pinned.SHA256, "--registry", registry}, &show); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(show.String(), "Pinned program interface verified") ||
		!strings.Contains(show.String(), pinned.SHA256) {
		t.Fatalf("show output = %q", show.String())
	}
}

func TestProgramHelpAndArguments(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"program", "--help"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"program workspace-create", "program workspace-check", "program workspace-doctor", "program mcp", "program mcp-config", "program inspect", "program pin", "program fetch", "program show", "program build", "program decode-account", "program decode-instruction", "program decode-event", "program read-account", "program simulate", "--workspace PATH", "loads a wallet"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("program help omitted %q: %q", want, output.String())
		}
	}
	if err := run([]string{"program", "inspect"}, &bytes.Buffer{}); err == nil {
		t.Fatal("incomplete inspect command was accepted")
	}
}
