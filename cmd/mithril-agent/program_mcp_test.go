package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/programinterface"
	"github.com/Overclock-Validator/mithril-agent/rootedindex"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProgramMCPBindsOneWorkspaceAndPinnedInterface(t *testing.T) {
	workspace, pin := createProgramMCPWorkspace(t)
	deploymentAccount, deployment := testLegacyProgramDeployment(t, 10, fakeProgramBankhash)
	evidence, err := programinterface.PinEvidence(
		filepath.Join(filepath.Dir(workspace), "interfaces"), programCommandAddress,
		"repository", []byte("reviewed repository analysis"),
		programinterface.EvidenceReview{
			Version: 3, Reviewer: "operator", Decision: "approved",
			Summary:        "The reviewed repository implements one bounded increment instruction.",
			SourceRevision: "0123456789abcdef", Tool: "repository-review", ToolVersion: "1.0.0",
			InterfaceSHA256: pin.SHA256, GenesisHash: solana.DevnetGenesisHash,
			ContextSlot: 10, Bankhash: fakeProgramBankhash, DeploymentSHA256: deployment.SHA256,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	rootedAccount, rootedSignature := seedProgramMCPIndexes(t, workspace)
	simulationNode := &fakeProgramSimulationNode{genesis: solana.DevnetGenesisHash}
	originalSimulationNode := openProgramSimulationNode
	openProgramSimulationNode = func(string) (programSimulationNode, error) { return simulationNode, nil }
	accountData := binary.LittleEndian.AppendUint64([]byte{1, 2, 3, 4, 5, 6, 7, 8}, 42)
	readNode := &fakeProgramPMPNode{
		genesis:    solana.DevnetGenesisHash,
		deployment: &deploymentAccount,
		account: solanarpc.AccountDataSlice{
			ContextSlot: 10, Owner: programCommandAddress,
			DataLength: uint64(len(accountData)), Data: accountData,
		},
	}
	originalReadNode := openProgramPMPNode
	openProgramPMPNode = func(string) (programPMPNode, error) { return readNode, nil }
	t.Cleanup(func() {
		openProgramSimulationNode = originalSimulationNode
		openProgramPMPNode = originalReadNode
	})
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- serveProgramMCP(ctx, workspace, pin.SHA256, serverReader, serverWriter)
	}()
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
	if err != nil || len(tools.Tools) != 9 {
		t.Fatalf("tools = %+v, %v", tools, err)
	}
	liveTools := map[string]bool{
		"mithril_program_interface": true, "mithril_program_simulate": true,
		"mithril_program_read_account": true,
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint ||
			!tool.Annotations.IdempotentHint || tool.Annotations.OpenWorldHint == nil {
			t.Fatalf("tool %q lacks read-only MCP annotations", tool.Name)
		}
		if *tool.Annotations.OpenWorldHint != liveTools[tool.Name] {
			t.Fatalf("tool %q open-world annotation = %t", tool.Name, *tool.Annotations.OpenWorldHint)
		}
	}

	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "mithril_program_interface"})
	if err != nil || result.IsError {
		t.Fatalf("interface = %+v, %v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var summary programMCPSummary
	if err := json.Unmarshal(encoded, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Program != programCommandAddress || summary.InterfaceSHA256 != pin.SHA256 ||
		summary.Provenance != "private_pinned_interface" || summary.Finality != "not_applicable" ||
		summary.WalletLoaded || summary.ExplorerRequired || len(summary.Instructions) != 1 ||
		summary.Instructions[0].Name != "increment" || len(summary.Instructions[0].Accounts) != 1 ||
		summary.Instructions[0].Accounts[0].Name != "state" || len(summary.Accounts) != 1 ||
		len(summary.Evidence) != 1 || summary.Evidence[0].Kind != "repository" ||
		summary.Evidence[0].SHA256 != evidence.SHA256 || summary.Evidence[0].Bytes != evidence.Bytes ||
		summary.Evidence[0].Decision != "approved" || summary.Evidence[0].Reviewer != "operator" ||
		summary.Evidence[0].Summary != "The reviewed repository implements one bounded increment instruction." ||
		summary.Evidence[0].GenesisHash != solana.DevnetGenesisHash ||
		summary.Evidence[0].Bankhash != fakeProgramBankhash ||
		summary.Evidence[0].DeploymentSHA256 != deployment.SHA256 ||
		summary.Evidence[0].ResultSHA256 != evidence.SHA256 {
		t.Fatalf("summary = %+v", summary)
	}

	result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "mithril_program_build_unsigned",
		Arguments: map[string]any{
			"instruction": "increment", "fee_payer": programSimulationFeePayer,
			"recent_blockhash": programSimulationState,
			"accounts":         map[string]any{"state": programSimulationState},
			"arguments":        map[string]any{"amount": "513"},
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("build = %+v, %v", result, err)
	}
	encoded, err = json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var build programMCPBuildOutput
	if err := json.Unmarshal(encoded, &build); err != nil {
		t.Fatal(err)
	}
	if build.Status != "built" || !build.Walletless || build.UnsignedTransactionBase64 == "" ||
		build.InterfaceSHA256 != pin.SHA256 || len(build.Review.Arguments) != 1 ||
		build.Review.Arguments[0].ValueJSON != "513" {
		t.Fatalf("build = %+v", build)
	}

	result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "mithril_program_simulate",
		Arguments: map[string]any{
			"instruction": "increment", "fee_payer": programSimulationFeePayer,
			"accounts":  map[string]any{"state": programSimulationState},
			"arguments": map[string]any{"amount": "513"}, "min_context_slot": 10,
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("simulate = %+v, %v", result, err)
	}
	encoded, err = json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var simulation programMCPSimulationOutput
	if err := json.Unmarshal(encoded, &simulation); err != nil {
		t.Fatal(err)
	}
	if simulation.Status != "simulated" || simulation.ContextSlot != 10 ||
		simulation.Bankhash != programSimulationState ||
		simulation.GenesisHash != solana.DevnetGenesisHash || simulation.DeploymentSHA256 != deployment.SHA256 ||
		simulation.Provenance != programProcessedProvenance || simulation.Finality != programProcessedFinality ||
		len(simulation.Review.Arguments) != 1 || simulation.Review.Arguments[0].ValueJSON != "513" {
		t.Fatalf("simulation = %+v", simulation)
	}

	result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "mithril_program_read_account",
		Arguments: map[string]any{
			"account_type": "Counter", "account": programSimulationState, "min_context_slot": 10,
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("read account = %+v, %v", result, err)
	}
	encoded, err = json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var account programReadOutput
	if err := json.Unmarshal(encoded, &account); err != nil {
		t.Fatal(err)
	}
	if account.ContextSlot != 10 || account.Provenance != programProcessedProvenance ||
		account.Bankhash != fakeProgramBankhash ||
		account.GenesisHash != solana.DevnetGenesisHash || account.DeploymentSHA256 != deployment.SHA256 ||
		account.Finality != programProcessedFinality || account.Name != "Counter" {
		t.Fatalf("account = %+v", account)
	}

	instructionData := binary.LittleEndian.AppendUint64([]byte{11, 18, 104, 9, 104, 174, 59, 33}, 513)
	result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "mithril_program_decode_instruction",
		Arguments: map[string]any{
			"instruction": "increment", "data_base64": base64.StdEncoding.EncodeToString(instructionData),
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("decode instruction = %+v, %v", result, err)
	}
	encoded, err = json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var decodedInstruction programDecodedOutput
	if err := json.Unmarshal(encoded, &decodedInstruction); err != nil {
		t.Fatal(err)
	}
	if decodedInstruction.Provenance != programLocalProvenance ||
		decodedInstruction.Finality != programLocalFinality || decodedInstruction.Name != "increment" {
		t.Fatalf("decoded instruction = %+v", decodedInstruction)
	}
	result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "mithril_program_decode_instruction",
		Arguments: map[string]any{
			"instruction": "increment",
			"data_base64": base64.StdEncoding.EncodeToString(make([]byte, programinterface.MaxInstructionDataBytes+1)),
		},
	})
	if err != nil || !result.IsError {
		t.Fatalf("oversized instruction = %+v, %v", result, err)
	}

	result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "mithril_program_decode_rooted_instruction",
		Arguments: map[string]any{
			"instruction": "increment", "signature": rootedSignature, "outer_index": 0,
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("decode rooted instruction = %+v, %v", result, err)
	}
	encoded, err = json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var rootedInstruction decodedProgramInstruction
	if err := json.Unmarshal(encoded, &rootedInstruction); err != nil {
		t.Fatal(err)
	}
	if rootedInstruction.Provenance != rootedindex.AlpenglowRootedProvenance ||
		rootedInstruction.Finality != rootedindex.RootedFinality ||
		rootedInstruction.Scope != "rooted_outer_instruction" || rootedInstruction.Current ||
		!rootedInstruction.Succeeded || rootedInstruction.Signature != rootedSignature ||
		rootedInstruction.Location != "outer" || !rootedInstruction.Signed ||
		rootedInstruction.OuterIndex != 0 || rootedInstruction.InnerIndex != nil ||
		rootedInstruction.Name != "increment" ||
		len(rootedInstruction.Accounts) != 1 {
		t.Fatalf("rooted instruction = %+v", rootedInstruction)
	}
	result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "mithril_program_decode_rooted_instruction",
		Arguments: map[string]any{
			"instruction": "increment", "signature": rootedSignature, "outer_index": 1,
		},
	})
	if err != nil || !result.IsError {
		t.Fatalf("out-of-range rooted instruction = %+v, %v", result, err)
	}
	result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "mithril_program_decode_rooted_inner_instruction",
		Arguments: map[string]any{
			"instruction": "increment", "signature": rootedSignature,
			"inner_group": 0, "inner_index": 0,
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("decode rooted inner instruction = %+v, %v", result, err)
	}
	encoded, err = json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var rootedInnerInstruction decodedProgramInstruction
	if err := json.Unmarshal(encoded, &rootedInnerInstruction); err != nil {
		t.Fatal(err)
	}
	if rootedInnerInstruction.Scope != "rooted_inner_instruction" ||
		rootedInnerInstruction.Provenance != rootedindex.AlpenglowRootedProvenance ||
		rootedInnerInstruction.Finality != rootedindex.RootedFinality ||
		rootedInnerInstruction.Location != "inner" || rootedInnerInstruction.Signed ||
		rootedInnerInstruction.OuterIndex != 0 || rootedInnerInstruction.InnerIndex == nil ||
		*rootedInnerInstruction.InnerIndex != 0 || rootedInnerInstruction.Name != "increment" ||
		len(rootedInnerInstruction.Accounts) != 1 {
		t.Fatalf("rooted inner instruction = %+v", rootedInnerInstruction)
	}

	result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "mithril_program_decode_rooted_account",
		Arguments: map[string]any{
			"account_type": "Counter", "account": rootedAccount,
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("decode rooted account = %+v, %v", result, err)
	}
	encoded, err = json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var rootedValue programMCPRootedDecoded
	if err := json.Unmarshal(encoded, &rootedValue); err != nil {
		t.Fatal(err)
	}
	if rootedValue.Provenance != rootedindex.AlpenglowRootedProvenance ||
		rootedValue.Finality != rootedindex.RootedFinality || rootedValue.Scope != "owner_matching_history" ||
		rootedValue.Current || rootedValue.Name != "Counter" || rootedValue.Cursor == nil {
		t.Fatalf("rooted account = %+v", rootedValue)
	}

	result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "mithril_program_decode_rooted_event",
		Arguments: map[string]any{
			"event_type": "Changed", "signature": rootedSignature,
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("decode rooted event = %+v, %v", result, err)
	}
	encoded, err = json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var rootedEvents programMCPRootedEvents
	if err := json.Unmarshal(encoded, &rootedEvents); err != nil {
		t.Fatal(err)
	}
	if rootedEvents.Provenance != rootedindex.AlpenglowRootedProvenance ||
		rootedEvents.Finality != rootedindex.RootedFinality || len(rootedEvents.Events) != 1 ||
		rootedEvents.Events[0].Name != "Changed" {
		t.Fatalf("rooted events = %+v", rootedEvents)
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
		t.Fatal("program MCP server did not stop")
	}
}

func TestProgramMCPClassicWorkspaceOmitsRootedTools(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "workspace")
	if err := runContext(t.Context(), []string{
		"program", "workspace-create", "--dir", dir,
		"--program", programCommandAddress, "--cluster", "mainnet-beta",
		"--node-rpc", "http://127.0.0.1:8899",
	}, io.Discard); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(dir, programWorkspaceFile)
	pin, err := programinterface.Pin(filepath.Join(dir, "interfaces"), programCommandAddress,
		[]byte(`{"address":"11111111111111111111111111111111","metadata":{"name":"counter","version":"0.1.0","spec":"0.1.0"},"instructions":[]}`))
	if err != nil {
		t.Fatal(err)
	}

	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- serveProgramMCP(ctx, workspace, pin.SHA256, serverReader, serverWriter)
	}()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.IOTransport{Reader: clientReader, Writer: clientWriter}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 5 {
		t.Fatalf("tools = %+v, %v", tools, err)
	}
	for _, tool := range tools.Tools {
		if strings.Contains(tool.Name, "rooted") {
			t.Fatalf("classic workspace advertised %q", tool.Name)
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
		t.Fatal("program MCP server did not stop")
	}
}

func TestProgramMCPClassicWorkspaceWithIndexesExposesRootedTools(t *testing.T) {
	workspace, pin := createProgramMCPWorkspaceForCluster(
		t, "mainnet-beta", solana.MainnetBetaGenesisHash,
	)
	account, signature := seedProgramMCPIndexesFromSource(t, workspace, testClassicRootedSource())
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- serveProgramMCP(ctx, workspace, pin.SHA256, serverReader, serverWriter)
	}()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.IOTransport{Reader: clientReader, Writer: clientWriter}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 9 {
		t.Fatalf("tools = %+v, %v", tools, err)
	}
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "mithril_program_decode_rooted_account",
		Arguments: map[string]any{
			"account_type": "Counter", "account": account,
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("decode rooted account = %+v, %v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var decoded programMCPRootedDecoded
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Provenance != rootedindex.ClassicFinalizedRootedProvenance ||
		decoded.Finality != rootedindex.RootedFinality || decoded.Scope != "owner_matching_history" ||
		decoded.Current || decoded.Cursor == nil {
		t.Fatalf("rooted account = %+v", decoded)
	}
	result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "mithril_program_decode_rooted_event",
		Arguments: map[string]any{
			"event_type": "Changed", "signature": signature,
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("decode rooted event = %+v, %v", result, err)
	}
	encoded, err = json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var events programMCPRootedEvents
	if err := json.Unmarshal(encoded, &events); err != nil {
		t.Fatal(err)
	}
	if events.Provenance != rootedindex.ClassicFinalizedRootedProvenance ||
		events.Finality != rootedindex.RootedFinality || len(events.Events) != 1 ||
		events.Events[0].Provenance != rootedindex.ClassicFinalizedRootedProvenance {
		t.Fatalf("rooted events = %+v", events)
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
		t.Fatal("program MCP server did not stop")
	}
}

func TestProgramMCPSummaryReportsCodamaConstructionBlocker(t *testing.T) {
	report, err := programinterface.Inspect([]byte(`{
  "kind":"rootNode","standard":"codama","version":"1.0.0",
  "program":{"kind":"programNode","name":"memo",
    "publicKey":"11111111111111111111111111111111","version":"1.0.0",
    "definedTypes":[],"accounts":[],"events":[],
    "instructions":[{"kind":"instructionNode","name":"write",
      "accounts":[],"arguments":[],"discriminators":[],
      "remainingAccounts":[{"kind":"instructionRemainingAccountsNode"}]}]}
}`), programCommandAddress)
	if err != nil {
		t.Fatal(err)
	}
	summary := summarizeProgramInterface(report, nil)
	if summary.Spec != "codama/1.0.0" || len(summary.Instructions) != 1 ||
		summary.Instructions[0].ConstructionBlocked !=
			"dynamic remaining accounts require a reviewed dedicated adapter" {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestProgramMCPSummaryReportsConditionalSignerBlocker(t *testing.T) {
	report, err := programinterface.Inspect([]byte(`{
  "kind":"rootNode","standard":"codama","version":"1.0.0",
  "program":{"kind":"programNode","name":"token",
    "publicKey":"11111111111111111111111111111111","version":"1.0.0",
    "definedTypes":[],"accounts":[],"events":[],
    "instructions":[{"kind":"instructionNode","name":"transfer",
      "accounts":[{"kind":"instructionAccountNode","name":"authority","isWritable":false,"isSigner":"either","isOptional":false}],
      "arguments":[],"discriminators":[]}]}
}`), programCommandAddress)
	if err != nil {
		t.Fatal(err)
	}
	summary := summarizeProgramInterface(report, nil)
	if len(summary.Instructions) != 1 || len(summary.Instructions[0].Accounts) != 1 ||
		summary.Instructions[0].Accounts[0].SignerMode != "either" ||
		summary.Instructions[0].ConstructionBlocked !=
			"conditional signer accounts require a reviewed dedicated adapter" {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestApplicableProgramEvidenceRequiresCurrentDeployment(t *testing.T) {
	workspacePath, pin := createProgramMCPWorkspace(t)
	workspace, err := loadProgramWorkspace(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	deploymentAccount, deployment := testLegacyProgramDeployment(t, 12, fakeProgramBankhash)
	node := &fakeProgramPMPNode{
		genesis: solana.DevnetGenesisHash, deployment: &deploymentAccount,
	}
	original := openProgramPMPNode
	openProgramPMPNode = func(string) (programPMPNode, error) { return node, nil }
	t.Cleanup(func() { openProgramPMPNode = original })
	item := programinterface.EvidenceResult{Attestation: programinterface.EvidenceAttestation{
		Version: 3, InterfaceSHA256: pin.SHA256, GenesisHash: solana.DevnetGenesisHash,
		ContextSlot: 10, Bankhash: fakeProgramBankhash, DeploymentSHA256: deployment.SHA256,
	}}
	applicable, err := applicableProgramEvidence(t.Context(), workspace, pin.SHA256, []programinterface.EvidenceResult{item})
	if err != nil || len(applicable) != 1 {
		t.Fatalf("matching evidence = %+v, %v", applicable, err)
	}
	item.Attestation.DeploymentSHA256 = strings.Repeat("f", 64)
	applicable, err = applicableProgramEvidence(t.Context(), workspace, pin.SHA256, []programinterface.EvidenceResult{item})
	if err != nil || len(applicable) != 0 {
		t.Fatalf("stale deployment evidence = %+v, %v", applicable, err)
	}
	item.Attestation.Version = 2
	applicable, err = applicableProgramEvidence(t.Context(), workspace, pin.SHA256, []programinterface.EvidenceResult{item})
	if err != nil || len(applicable) != 0 {
		t.Fatalf("legacy evidence was applicable = %+v, %v", applicable, err)
	}
}

func TestProgramMCPConfigContainsOnlyPinnedLocalCommand(t *testing.T) {
	workspace, pin := createProgramMCPWorkspace(t)
	var output bytes.Buffer
	if err := runProgramMCPConfig([]string{
		"--workspace", workspace, "--sha256", pin.SHA256, "--name", "counter-program",
	}, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, `"counter-program"`) ||
		!strings.Contains(text, `"program"`) || !strings.Contains(text, `"mcp"`) ||
		!strings.Contains(text, pin.SHA256) || !json.Valid(output.Bytes()) ||
		strings.Contains(text, "127.0.0.1") || strings.Contains(text, "accounts") {
		t.Fatalf("MCP config = %s", text)
	}
	if err := runProgramMCPConfig([]string{
		"--workspace", workspace, "--sha256", strings.Repeat("0", 64), "--name", "counter-program",
	}, io.Discard); err == nil {
		t.Fatal("unpinned interface hash was accepted")
	}
}

func TestProgramMCPRejectsOpenWorkspaceAndOversizedResult(t *testing.T) {
	workspace, pin := createProgramMCPWorkspace(t)
	if err := os.Chmod(filepath.Dir(workspace), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := loadProgramMCPWorkspace(workspace, pin.SHA256); err == nil {
		t.Fatal("non-private program workspace was accepted")
	}
	if err := requireProgramMCPSize(bytes.Repeat([]byte("x"), programMCPMaxResultBytes)); err == nil {
		t.Fatal("oversized MCP result was accepted")
	}
}

func TestProgramWorkspaceRejectsMixedRootedIndexLineage(t *testing.T) {
	workspacePath, _ := createProgramMCPWorkspace(t)
	workspace, err := loadProgramWorkspace(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	view := workspaceReport("", workspacePath, workspace)
	state, err := rootedindex.Open(view.StateIndex, testRootedSource(), rootedindex.Filter{Owner: programCommandAddress})
	if err != nil {
		t.Fatal(err)
	}
	beginRootedBatch(t, state, 1, 1, 1)
	if _, err := state.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion, Cursor: rootedindex.Cursor{Slot: 1},
		Kind: "slot_rooted", Root: testRootedSlot(testRootedSource(), 0, 0, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	otherSource := testRootedSource()
	otherSource.AccountsDBRootRunID = "deadbeef"
	activity, err := rootedindex.Open(view.ActivityIndex, otherSource, rootedindex.Filter{Mention: programCommandAddress})
	if err != nil {
		t.Fatal(err)
	}
	beginRootedBatch(t, activity, 1, 1, 1)
	if _, err := activity.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion, Cursor: rootedindex.Cursor{Slot: 1},
		Kind: "slot_rooted", Root: testRootedSlot(otherSource, 0, 0, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := activity.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateProgramWorkspaceIndexes(workspacePath); err == nil || !strings.Contains(err.Error(), "different") {
		t.Fatalf("mixed source lineage error = %v", err)
	}
}

func createProgramMCPWorkspace(t *testing.T) (string, programinterface.PinResult) {
	return createProgramMCPWorkspaceForCluster(t, "alpenglow", testRootedSource().GenesisHash)
}

func createProgramMCPWorkspaceForCluster(
	t *testing.T,
	cluster, genesisHash string,
) (string, programinterface.PinResult) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	accounts := filepath.Join(root, "accounts")
	if err := os.Mkdir(accounts, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "workspace")
	args := []string{
		"program", "workspace-create", "--dir", dir,
		"--program", programCommandAddress, "--cluster", cluster,
	}
	if cluster == "alpenglow" {
		args = append(args, "--genesis-hash", genesisHash)
	}
	args = append(args, "--node-rpc", "http://127.0.0.1:8899", "--accounts", accounts)
	if err := runContext(context.Background(), args, io.Discard); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(dir, programWorkspaceFile)
	idl := []byte(`{"address":"11111111111111111111111111111111","metadata":{"name":"counter","version":"0.1.0","spec":"0.1.0"},"instructions":[{"name":"increment","discriminator":[11,18,104,9,104,174,59,33],"accounts":[{"name":"state","writable":true}],"args":[{"name":"amount","type":"u64"}]}],"accounts":[{"name":"Counter","discriminator":[1,2,3,4,5,6,7,8]}],"events":[{"name":"Changed","discriminator":[9,10,11,12,13,14,15,16]}],"types":[{"name":"Counter","type":{"kind":"struct","fields":[{"name":"value","type":"u64"}]}},{"name":"Changed","type":{"kind":"struct","fields":[{"name":"value","type":"u64"}]}}]}`)
	pin, err := programinterface.Pin(filepath.Join(dir, "interfaces"), programCommandAddress, idl)
	if err != nil {
		t.Fatal(err)
	}
	return workspace, pin
}

func seedProgramMCPIndexes(t *testing.T, workspacePath string) (string, string) {
	return seedProgramMCPIndexesFromSource(t, workspacePath, testRootedSource())
}

func seedProgramMCPIndexesFromSource(
	t *testing.T,
	workspacePath string,
	source rootedindex.SourceDescriptor,
) (string, string) {
	t.Helper()
	workspace, err := loadProgramWorkspace(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	view := workspaceReport("", workspacePath, workspace)
	accountAddress := programSimulationState
	accountData := binary.LittleEndian.AppendUint64([]byte{1, 2, 3, 4, 5, 6, 7, 8}, 42)
	state, err := rootedindex.Open(view.StateIndex, source, rootedindex.Filter{Owner: programCommandAddress})
	if err != nil {
		t.Fatal(err)
	}
	beginRootedBatch(t, state, 1, 1, 2)
	if _, err := state.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion, Cursor: rootedindex.Cursor{Slot: 1},
		Kind: "account_updated", Account: &rootedindex.AccountUpdate{
			Pubkey: accountAddress, Owner: programCommandAddress, Lamports: 1, Data: accountData,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion, Cursor: rootedindex.Cursor{Slot: 1, Ordinal: 1},
		Kind: "slot_rooted", Root: testRootedSlot(source, 0, 0, 1),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion, Cursor: rootedindex.Cursor{Slot: 2},
		Kind: "slot_rooted", Root: testRootedSlot(source, 1, 0, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	eventData := binary.LittleEndian.AppendUint64([]byte{9, 10, 11, 12, 13, 14, 15, 16}, 7)
	instructionData := binary.LittleEndian.AppendUint64([]byte{11, 18, 104, 9, 104, 174, 59, 33}, 513)
	transaction := testRootedTransactionWithInstruction(t, programCommandAddress, instructionData, []string{
		"Program " + programCommandAddress + " invoke [1]",
		"Program data: " + base64.StdEncoding.EncodeToString(eventData),
		"Program " + programCommandAddress + " success",
	})
	transaction.Inner = []rootedindex.InnerInstructions{{
		Index: 0, Instructions: []rootedindex.CompiledInstruction{{
			ProgramIDIndex: 1, Accounts: []uint16{0}, Data: instructionData,
		}},
	}}
	signature := transaction.Signature
	activity, err := rootedindex.Open(view.ActivityIndex, source, rootedindex.Filter{Mention: programCommandAddress})
	if err != nil {
		t.Fatal(err)
	}
	beginRootedBatch(t, activity, 1, 1, 2)
	if _, err := activity.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion, Cursor: rootedindex.Cursor{Slot: 1},
		Kind: "slot_rooted", Root: testRootedSlot(source, 0, 0, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := activity.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion, Cursor: rootedindex.Cursor{Slot: 2},
		Kind: "transaction_executed", Transaction: transaction,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := activity.Append(rootedindex.Event{
		SchemaVersion: rootedindex.SchemaVersion, Cursor: rootedindex.Cursor{Slot: 2, Ordinal: 1},
		Kind: "slot_rooted", Root: testRootedSlot(source, 1, 1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := activity.Close(); err != nil {
		t.Fatal(err)
	}
	return accountAddress, signature
}
