package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"github.com/Overclock-Validator/mithril-agent/internal/mcpstdio"
	"github.com/Overclock-Validator/mithril-agent/programinterface"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const programMCPMaxResultBytes = 256 << 10

type programMCPNoInput struct{}

type programMCPSummary struct {
	Program          string                         `json:"program"`
	InterfaceSHA256  string                         `json:"interface_sha256"`
	Name             string                         `json:"name,omitempty"`
	Version          string                         `json:"version,omitempty"`
	Spec             string                         `json:"spec,omitempty"`
	Instructions     []programMCPInstructionSummary `json:"instructions"`
	Accounts         []string                       `json:"accounts"`
	Events           []string                       `json:"events"`
	Evidence         []programMCPEvidenceSummary    `json:"evidence,omitempty"`
	Provenance       string                         `json:"provenance"`
	Finality         string                         `json:"finality"`
	WalletLoaded     bool                           `json:"wallet_loaded"`
	ExplorerRequired bool                           `json:"explorer_required"`
}

type programMCPEvidenceSummary struct {
	Kind             string `json:"kind"`
	SHA256           string `json:"sha256"`
	Bytes            int    `json:"bytes"`
	Reviewer         string `json:"reviewer"`
	Decision         string `json:"decision"`
	Summary          string `json:"summary,omitempty"`
	SourceRevision   string `json:"source_revision"`
	Tool             string `json:"tool"`
	ToolVersion      string `json:"tool_version"`
	InterfaceSHA256  string `json:"interface_sha256,omitempty"`
	MessageSHA256    string `json:"message_sha256,omitempty"`
	GenesisHash      string `json:"genesis_hash"`
	ContextSlot      uint64 `json:"context_slot,omitempty"`
	Bankhash         string `json:"bankhash"`
	DeploymentSHA256 string `json:"deployment_sha256"`
	ResultSHA256     string `json:"result_sha256"`
}

type programMCPInstructionSummary struct {
	Name                string                      `json:"name"`
	Discriminator       string                      `json:"discriminator"`
	Accounts            []programMCPAccountSummary  `json:"accounts"`
	Arguments           []programMCPArgumentSummary `json:"arguments"`
	ConstructionBlocked string                      `json:"construction_blocked,omitempty"`
}

type programMCPAccountSummary struct {
	Name       string `json:"name"`
	Writable   bool   `json:"writable"`
	Signer     bool   `json:"signer"`
	SignerMode string `json:"signer_mode,omitempty"`
	Optional   bool   `json:"optional"`
	Address    string `json:"address,omitempty"`
}

type programMCPArgumentSummary struct {
	Name     string `json:"name"`
	TypeJSON string `json:"type_json"`
}

type programMCPBuildInput struct {
	Instruction     string            `json:"instruction" jsonschema:"Exact instruction name from the pinned interface"`
	FeePayer        string            `json:"fee_payer" jsonschema:"Public fee-payer address; no private key is loaded"`
	RecentBlockhash string            `json:"recent_blockhash" jsonschema:"Recent Solana blockhash used only to build unsigned bytes"`
	Accounts        map[string]string `json:"accounts" jsonschema:"Account-name to Solana-address bindings"`
	Arguments       map[string]string `json:"arguments,omitempty" jsonschema:"Argument-name to exact JSON text bindings, for example 513 or [1,2]"`
}

type programMCPBuildOutput struct {
	Status                    string               `json:"status"`
	Program                   string               `json:"program"`
	InterfaceSHA256           string               `json:"interface_sha256"`
	Instruction               string               `json:"instruction"`
	MessageBase64             string               `json:"message_base64"`
	MessageSHA256             string               `json:"message_sha256"`
	UnsignedTransactionBase64 string               `json:"unsigned_transaction_base64"`
	UnsignedTransactionSHA256 string               `json:"unsigned_transaction_sha256"`
	Review                    programMCPCallReview `json:"review"`
	Walletless                bool                 `json:"walletless"`
}

type programMCPCallReview struct {
	FeePayer  string                     `json:"fee_payer"`
	Accounts  []programAccountReview     `json:"accounts,omitempty"`
	Arguments []programMCPArgumentReview `json:"arguments,omitempty"`
}

type programMCPArgumentReview struct {
	Name      string `json:"name"`
	ValueJSON string `json:"value_json"`
}

type programMCPSimulateInput struct {
	Instruction    string            `json:"instruction" jsonschema:"Exact instruction name from the pinned interface"`
	FeePayer       string            `json:"fee_payer" jsonschema:"Public fee-payer address; no private key is loaded"`
	Accounts       map[string]string `json:"accounts" jsonschema:"Account-name to Solana-address bindings"`
	Arguments      map[string]string `json:"arguments,omitempty" jsonschema:"Argument-name to exact JSON text bindings, for example 513 or [1,2]"`
	MinContextSlot uint64            `json:"min_context_slot" jsonschema:"Minimum processed Mithril context slot"`
}

type programMCPSimulationOutput struct {
	Status                    string               `json:"status"`
	Cluster                   string               `json:"cluster"`
	GenesisHash               string               `json:"genesis_hash"`
	Provenance                string               `json:"provenance"`
	Finality                  string               `json:"finality"`
	Program                   string               `json:"program"`
	InterfaceSHA256           string               `json:"interface_sha256"`
	Instruction               string               `json:"instruction"`
	MessageSHA256             string               `json:"message_sha256"`
	UnsignedTransactionSHA256 string               `json:"unsigned_transaction_sha256"`
	ContextSlot               uint64               `json:"context_slot"`
	Bankhash                  string               `json:"bankhash"`
	DeploymentSHA256          string               `json:"deployment_sha256"`
	UnitsConsumed             uint64               `json:"units_consumed"`
	LogsSHA256                string               `json:"logs_sha256"`
	Review                    programMCPCallReview `json:"review"`
	Walletless                bool                 `json:"walletless"`
}

type programMCPReadAccountInput struct {
	AccountType    string `json:"account_type" jsonschema:"Exact account type from the pinned interface"`
	Account        string `json:"account" jsonschema:"Solana account address"`
	MinContextSlot uint64 `json:"min_context_slot" jsonschema:"Minimum processed Mithril context slot"`
}

type programMCPDecodeInstructionInput struct {
	Instruction string `json:"instruction" jsonschema:"Exact instruction name from the pinned interface"`
	DataBase64  string `json:"data_base64" jsonschema:"Base64-encoded instruction data, at most 4096 decoded bytes"`
}

type programMCPDecodeRootedInstructionInput struct {
	Instruction string `json:"instruction" jsonschema:"Exact instruction name from the pinned interface"`
	Signature   string `json:"signature" jsonschema:"Exact transaction signature in the workspace rooted activity index"`
	OuterIndex  int    `json:"outer_index" jsonschema:"Zero-based outer instruction index in the signed transaction"`
}

type programMCPDecodeRootedInnerInstructionInput struct {
	Instruction string `json:"instruction" jsonschema:"Exact instruction name from the pinned interface"`
	Signature   string `json:"signature" jsonschema:"Exact transaction signature in the workspace rooted activity index"`
	InnerGroup  int    `json:"inner_group" jsonschema:"Zero-based parent outer instruction index for the CPI group"`
	InnerIndex  int    `json:"inner_index" jsonschema:"Zero-based instruction index inside the CPI group"`
}

type programMCPDecodeAccountInput struct {
	AccountType string `json:"account_type" jsonschema:"Exact account type from the pinned interface"`
	Account     string `json:"account" jsonschema:"Solana account address in the workspace rooted owner-history index"`
}

type programMCPDecodeEventInput struct {
	EventType string `json:"event_type" jsonschema:"Exact event type from the pinned interface"`
	Signature string `json:"signature" jsonschema:"Exact transaction signature in the workspace rooted activity index"`
}

type programMCPRootedDecoded = programDecodedOutput

type programMCPRootedEvents struct {
	Provenance string                `json:"provenance"`
	Finality   string                `json:"finality"`
	Events     []decodedProgramEvent `json:"events"`
}

func runProgramMCP(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("program mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspace := flags.String("workspace", "", "private program workspace.json path")
	sha256 := flags.String("sha256", "", "exact pinned interface SHA-256")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *workspace == "" || *sha256 == "" {
		return errors.New("program mcp requires --workspace and --sha256")
	}
	closer, ok := input.(io.ReadCloser)
	if !ok {
		return errors.New("program MCP input must be closable stdio")
	}
	return serveProgramMCP(ctx, *workspace, *sha256, closer, output)
}

func runProgramMCPConfig(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("program mcp-config", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspace := flags.String("workspace", "", "private program workspace.json path")
	sha256 := flags.String("sha256", "", "exact pinned interface SHA-256")
	name := flags.String("name", "", "MCP server name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *workspace == "" || *sha256 == "" || !indexMCPName.MatchString(*name) {
		return errors.New("program mcp-config requires --workspace, --sha256, and a 1-64 character letter, number, underscore, or hyphen --name")
	}
	if _, _, _, err := loadProgramMCPWorkspace(*workspace, *sha256); err != nil {
		return err
	}
	agentPath, err := resolvedAgentExecutable()
	if err != nil {
		return err
	}
	entry := map[string]any{"mcpServers": map[string]any{
		*name: map[string]any{"command": agentPath, "args": []string{
			"program", "mcp", "--workspace", *workspace, "--sha256", *sha256,
		}},
	}}
	encoded, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, string(encoded))
	return err
}

func serveProgramMCP(
	ctx context.Context,
	workspacePath, interfaceSHA256 string,
	input io.ReadCloser,
	output io.Writer,
) error {
	if input == nil || output == nil {
		return errors.New("program MCP stdio is required")
	}
	workspace, workspaceView, report, err := loadProgramMCPWorkspace(workspacePath, interfaceSHA256)
	if err != nil {
		return err
	}
	rootedProvenance, rootedFinality := "", ""
	if workspace.Accounts != "" {
		stateIndex, activityIndex, err := readProgramWorkspaceIndexes(workspacePath)
		if err != nil {
			return err
		}
		if stateIndex.Provenance != activityIndex.Provenance || stateIndex.Finality != activityIndex.Finality {
			return errors.New("program workspace indexes disagree on rooted evidence provenance")
		}
		rootedProvenance, rootedFinality = stateIndex.Provenance, stateIndex.Finality
	}
	evidence, err := programinterface.ListEvidence(workspaceView.Registry, report.Program)
	if err != nil {
		return err
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name: "mithril-program", Title: "Mithril walletless program workspace", Version: "0.1.0",
	}, &mcpsdk.ServerOptions{Instructions: "Use one operator-authorized private program workspace and one exact pinned interface. Tools inspect, decode, build unsigned bytes, read through loopback Mithril, or simulate. No tool loads a wallet, signs, submits, opens a listener, or uses an explorer."})
	server.AddReceivingMiddleware(mcpstdio.LimitToolCalls(4))
	closedWorld, openWorld := false, true
	local := &mcpsdk.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld}
	live := &mcpsdk.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &openWorld}

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name: "mithril_program_interface", Title: "Pinned Program Interface",
		Description: "Summarize the exact interface and only reviewed evidence whose genesis and current deployment still match the workspace Mithril node.", Annotations: live,
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, _ programMCPNoInput) (*mcpsdk.CallToolResult, programMCPSummary, error) {
		applicable, err := applicableProgramEvidence(ctx, workspace, report.SHA256, evidence)
		if err != nil {
			return nil, programMCPSummary{}, err
		}
		summary := summarizeProgramInterface(report, applicable)
		return nil, summary, requireProgramMCPSize(summary)
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name: "mithril_program_build_unsigned", Title: "Build Unsigned Program Call",
		Description: "Build and review unsigned transaction bytes from the pinned interface. No key is loaded and nothing is submitted.", Annotations: local,
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input programMCPBuildInput) (*mcpsdk.CallToolResult, programMCPBuildOutput, error) {
		args, err := programMCPCallArgs(workspacePath, interfaceSHA256, input.Instruction, input.FeePayer, input.RecentBlockhash, input.Accounts, input.Arguments)
		if err != nil {
			return nil, programMCPBuildOutput{}, err
		}
		result, err := runBoundedProgramJSON[programBuildOutput](func(out io.Writer) error {
			return runProgramBuild(append(args, "--json"), out)
		})
		return nil, programMCPBuildView(result), err
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name: "mithril_program_simulate", Title: "Simulate Program Call",
		Description: "Build and simulate an unsigned call through the workspace loopback Mithril node. Returns processed-bank provenance, never a rooted claim.", Annotations: live,
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input programMCPSimulateInput) (*mcpsdk.CallToolResult, programMCPSimulationOutput, error) {
		args, err := programMCPCallArgs(workspacePath, interfaceSHA256, input.Instruction, input.FeePayer, "", input.Accounts, input.Arguments)
		if err != nil {
			return nil, programMCPSimulationOutput{}, err
		}
		args = append(args, "--min-context-slot", fmt.Sprint(input.MinContextSlot), "--json")
		result, err := runBoundedProgramJSON[programSimulationOutput](func(out io.Writer) error {
			return runProgramSimulate(ctx, args, out)
		})
		return nil, programMCPSimulationView(result), err
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name: "mithril_program_read_account", Title: "Read Program Account",
		Description: "Read and decode one live program-owned account through the workspace loopback Mithril node. Returns processed-bank provenance, never a rooted claim.", Annotations: live,
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input programMCPReadAccountInput) (*mcpsdk.CallToolResult, programReadOutput, error) {
		result, err := runBoundedProgramJSON[programReadOutput](func(out io.Writer) error {
			return runProgramReadAccount(ctx, []string{
				"--workspace", workspacePath, "--sha256", interfaceSHA256,
				"--account-type", input.AccountType, "--account", input.Account,
				"--min-context-slot", fmt.Sprint(input.MinContextSlot), "--json",
			}, out)
		})
		return nil, result, err
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name: "mithril_program_decode_instruction", Title: "Decode Instruction Data",
		Description: "Decode bounded base64 instruction data locally with the exact pinned interface.", Annotations: local,
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input programMCPDecodeInstructionInput) (*mcpsdk.CallToolResult, programDecodedOutput, error) {
		if len(input.DataBase64) > base64.StdEncoding.EncodedLen(programinterface.MaxInstructionDataBytes) {
			return nil, programDecodedOutput{}, errors.New("instruction data exceeds 4096 decoded bytes")
		}
		data, err := base64.StdEncoding.Strict().DecodeString(input.DataBase64)
		if err != nil {
			return nil, programDecodedOutput{}, errors.New("instruction data is not canonical base64")
		}
		decoded, err := programinterface.DecodeInstruction(report, input.Instruction, data)
		if err != nil {
			return nil, programDecodedOutput{}, err
		}
		result := programDecodedOutput{
			Provenance: programLocalProvenance, Finality: programLocalFinality,
			DecodedData: decoded,
		}
		if err := requireProgramMCPSize(result); err != nil {
			return nil, programDecodedOutput{}, err
		}
		return nil, result, nil
	})
	if rootedProvenance != "" {
		mcpsdk.AddTool(server, &mcpsdk.Tool{
			Name: "mithril_program_decode_rooted_instruction", Title: "Decode Rooted Program Instruction",
			Description: "Decode one exact outer instruction from a rooted indexed transaction. Returns the transaction outcome and rooted provenance.", Annotations: local,
		}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input programMCPDecodeRootedInstructionInput) (*mcpsdk.CallToolResult, decodedProgramInstruction, error) {
			decoded, err := runBoundedProgramJSON[decodedProgramInstruction](func(out io.Writer) error {
				return runProgramDecodeInstruction([]string{
					"--workspace", workspacePath, "--sha256", interfaceSHA256,
					"--instruction", input.Instruction, "--index-dir", workspaceView.ActivityIndex,
					"--signature", input.Signature, "--outer-index", fmt.Sprint(input.OuterIndex), "--json",
				}, out)
			})
			return nil, decoded, err
		})
		mcpsdk.AddTool(server, &mcpsdk.Tool{
			Name: "mithril_program_decode_rooted_inner_instruction", Title: "Decode Rooted Inner Program Instruction",
			Description: "Decode one recorded CPI from a rooted transaction. This is rooted runtime evidence, not part of the signed outer message.", Annotations: local,
		}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input programMCPDecodeRootedInnerInstructionInput) (*mcpsdk.CallToolResult, decodedProgramInstruction, error) {
			decoded, err := runBoundedProgramJSON[decodedProgramInstruction](func(out io.Writer) error {
				return runProgramDecodeInstruction([]string{
					"--workspace", workspacePath, "--sha256", interfaceSHA256,
					"--instruction", input.Instruction, "--index-dir", workspaceView.ActivityIndex,
					"--signature", input.Signature, "--inner-group", fmt.Sprint(input.InnerGroup),
					"--inner-index", fmt.Sprint(input.InnerIndex), "--json",
				}, out)
			})
			return nil, decoded, err
		})
		mcpsdk.AddTool(server, &mcpsdk.Tool{
			Name: "mithril_program_decode_rooted_account", Title: "Decode Rooted Owner-History Account",
			Description: "Decode the newest rooted record whose post-state owner matched this program. The result is historical evidence and current=false; use read_account for current processed state.", Annotations: local,
		}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input programMCPDecodeAccountInput) (*mcpsdk.CallToolResult, programMCPRootedDecoded, error) {
			decoded, err := runBoundedProgramJSON[programMCPRootedDecoded](func(out io.Writer) error {
				return runProgramDecodeAccount([]string{
					"--workspace", workspacePath, "--sha256", interfaceSHA256,
					"--account-type", input.AccountType, "--index-dir", workspaceView.StateIndex,
					"--account", input.Account, "--json",
				}, out)
			})
			return nil, decoded, err
		})
		mcpsdk.AddTool(server, &mcpsdk.Tool{
			Name: "mithril_program_decode_rooted_event", Title: "Decode Rooted Program Event",
			Description: "Decode matching logs from one exact transaction in this workspace's rooted activity index.", Annotations: local,
		}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input programMCPDecodeEventInput) (*mcpsdk.CallToolResult, programMCPRootedEvents, error) {
			events, err := runBoundedProgramJSON[[]decodedProgramEvent](func(out io.Writer) error {
				return runProgramDecodeEvent([]string{
					"--workspace", workspacePath, "--sha256", interfaceSHA256,
					"--event-type", input.EventType, "--index-dir", workspaceView.ActivityIndex,
					"--signature", input.Signature, "--json",
				}, out)
			})
			return nil, programMCPRootedEvents{Provenance: rootedProvenance, Finality: rootedFinality, Events: events}, err
		})
	}

	err = server.Run(ctx, &mcpsdk.IOTransport{
		Reader: mcpstdio.NewReader(input), Writer: mcpstdio.WriteCloser{Writer: output},
	})
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) ||
		errors.Is(err, mcpsdk.ErrConnectionClosed) || err.Error() == "server is closing" ||
		err.Error() == "server is closing: EOF" {
		return nil
	}
	return err
}

func loadProgramMCPWorkspace(path, interfaceSHA256 string) (programWorkspace, programWorkspaceReport, programinterface.Report, error) {
	workspace, err := loadProgramWorkspace(path)
	if err != nil {
		return programWorkspace{}, programWorkspaceReport{}, programinterface.Report{}, err
	}
	cleanPath, err := cleanProgramWorkspacePath(path, "workspace path")
	if err != nil {
		return programWorkspace{}, programWorkspaceReport{}, programinterface.Report{}, err
	}
	view := workspaceReport("", cleanPath, workspace)
	if err := validatePrivateDirectory(filepath.Dir(cleanPath)); err != nil {
		return programWorkspace{}, programWorkspaceReport{}, programinterface.Report{}, errors.New("program MCP workspace must be private with mode 0700")
	}
	for _, dir := range []string{view.Registry, view.StateIndex, view.ActivityIndex} {
		if err := validatePrivateDirectory(dir); err != nil {
			return programWorkspace{}, programWorkspaceReport{}, programinterface.Report{}, errors.New("program MCP workspace directory layout is not private and complete")
		}
	}
	report, _, err := programinterface.Load(view.Registry, workspace.Program, interfaceSHA256)
	if err != nil {
		return programWorkspace{}, programWorkspaceReport{}, programinterface.Report{}, err
	}
	return workspace, view, report, nil
}

func programMCPBuildView(result programBuildOutput) programMCPBuildOutput {
	return programMCPBuildOutput{
		Status: result.Status, Program: result.Program, InterfaceSHA256: result.InterfaceSHA256,
		Instruction: result.Instruction, MessageBase64: base64.StdEncoding.EncodeToString(result.Message),
		MessageSHA256:             result.MessageSHA256,
		UnsignedTransactionBase64: base64.StdEncoding.EncodeToString(result.UnsignedTransaction),
		UnsignedTransactionSHA256: result.UnsignedTransactionSHA256,
		Review:                    programMCPReview(result.Review), Walletless: result.Walletless,
	}
}

func programMCPSimulationView(result programSimulationOutput) programMCPSimulationOutput {
	return programMCPSimulationOutput{
		Status: result.Status, Cluster: result.Cluster, Provenance: result.Provenance,
		GenesisHash: result.GenesisHash,
		Finality:    result.Finality, Program: result.Program, InterfaceSHA256: result.InterfaceSHA256,
		Instruction: result.Instruction, MessageSHA256: result.MessageSHA256,
		UnsignedTransactionSHA256: result.UnsignedTransactionSHA256,
		ContextSlot:               result.ContextSlot, UnitsConsumed: result.UnitsConsumed,
		Bankhash: result.Bankhash, DeploymentSHA256: result.DeploymentSHA256,
		LogsSHA256: result.LogsSHA256, Review: programMCPReview(result.Review),
		Walletless: result.Walletless,
	}
}

func programMCPReview(review programCallReview) programMCPCallReview {
	result := programMCPCallReview{FeePayer: review.FeePayer, Accounts: review.Accounts}
	for _, argument := range review.Arguments {
		result.Arguments = append(result.Arguments, programMCPArgumentReview{
			Name: argument.Name, ValueJSON: string(argument.Value),
		})
	}
	return result
}

func summarizeProgramInterface(report programinterface.Report, evidence []programinterface.EvidenceResult) programMCPSummary {
	summary := programMCPSummary{
		Program: report.Program, InterfaceSHA256: report.SHA256, Name: report.Name,
		Version: report.Version, Spec: report.Spec, Provenance: "private_pinned_interface",
		Finality: "not_applicable", WalletLoaded: false, ExplorerRequired: false,
	}
	for _, instruction := range report.Instructions {
		item := programMCPInstructionSummary{
			Name: instruction.Name, Discriminator: instruction.Discriminator,
		}
		if instruction.DynamicRemainingAccounts {
			item.ConstructionBlocked = "dynamic remaining accounts require a reviewed dedicated adapter"
		}
		for _, account := range instruction.Accounts {
			if account.SignerMode != "" {
				item.ConstructionBlocked = "conditional signer accounts require a reviewed dedicated adapter"
			}
			item.Accounts = append(item.Accounts, programMCPAccountSummary{
				Name: account.Name, Writable: account.Writable, Signer: account.Signer, SignerMode: account.SignerMode,
				Optional: account.Optional, Address: account.Address,
			})
		}
		for _, argument := range instruction.Args {
			item.Arguments = append(item.Arguments, programMCPArgumentSummary{
				Name: argument.Name, TypeJSON: string(argument.Type),
			})
		}
		summary.Instructions = append(summary.Instructions, item)
	}
	for _, account := range report.Accounts {
		summary.Accounts = append(summary.Accounts, account.Name)
	}
	for _, event := range report.Events {
		summary.Events = append(summary.Events, event.Name)
	}
	for _, item := range evidence {
		if item.Attestation.Version != 3 || item.Attestation.InterfaceSHA256 != report.SHA256 {
			continue
		}
		summary.Evidence = append(summary.Evidence, programMCPEvidenceSummary{
			Kind: item.Kind, SHA256: item.SHA256, Bytes: item.Bytes,
			Reviewer: item.Attestation.Reviewer, Decision: item.Attestation.Decision,
			Summary:        item.Attestation.Summary,
			SourceRevision: item.Attestation.SourceRevision,
			Tool:           item.Attestation.Tool, ToolVersion: item.Attestation.ToolVersion,
			InterfaceSHA256: item.Attestation.InterfaceSHA256,
			MessageSHA256:   item.Attestation.MessageSHA256,
			GenesisHash:     item.Attestation.GenesisHash, ContextSlot: item.Attestation.ContextSlot,
			Bankhash: item.Attestation.Bankhash, DeploymentSHA256: item.Attestation.DeploymentSHA256,
			ResultSHA256: item.Attestation.ResultSHA256,
		})
	}
	return summary
}

func programMCPCallArgs(
	workspace, interfaceSHA256, instruction, feePayer, recentBlockhash string,
	accounts map[string]string,
	arguments map[string]string,
) ([]string, error) {
	args := []string{"--workspace", workspace, "--sha256", interfaceSHA256,
		"--instruction", instruction, "--fee-payer", feePayer}
	if recentBlockhash != "" {
		args = append(args, "--recent-blockhash", recentBlockhash)
	}
	accountNames := make([]string, 0, len(accounts))
	for name := range accounts {
		accountNames = append(accountNames, name)
	}
	sort.Strings(accountNames)
	for _, name := range accountNames {
		args = append(args, "--account", name+"="+accounts[name])
	}
	argumentNames := make([]string, 0, len(arguments))
	for name := range arguments {
		argumentNames = append(argumentNames, name)
	}
	sort.Strings(argumentNames)
	for _, name := range argumentNames {
		encoded := arguments[name]
		if !json.Valid([]byte(encoded)) {
			return nil, errors.New("program argument is not valid JSON")
		}
		args = append(args, "--arg", name+"="+encoded)
	}
	return args, nil
}

type programMCPCappedBuffer struct{ bytes.Buffer }

func (buffer *programMCPCappedBuffer) Write(data []byte) (int, error) {
	if buffer.Len()+len(data) > programMCPMaxResultBytes {
		return 0, errors.New("program MCP result exceeds 256 KiB; use the local CLI for this result")
	}
	return buffer.Buffer.Write(data)
}

func runBoundedProgramJSON[T any](run func(io.Writer) error) (T, error) {
	var zero T
	var output programMCPCappedBuffer
	if err := run(&output); err != nil {
		return zero, err
	}
	var result T
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		return zero, errors.New("program MCP internal result is invalid")
	}
	return result, nil
}

func requireProgramMCPSize(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return errors.New("program MCP result cannot be encoded")
	}
	if len(encoded) > programMCPMaxResultBytes {
		return errors.New("program MCP result exceeds 256 KiB; use the local CLI for this result")
	}
	return nil
}
