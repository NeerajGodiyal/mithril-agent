package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Overclock-Validator/mithril-agent/programinterface"
)

const (
	programProcessedProvenance = "mithril_processed_bank"
	programProcessedFinality   = "processed"
)

const programUsage = `Usage:
  mithril-agent program workspace-create --dir PATH --program ADDRESS \
    --cluster devnet|testnet|mainnet-beta|alpenglow [--genesis-hash HASH] \
    --node-rpc LOOPBACK_URL [--accounts PATH] [--json]
  mithril-agent program workspace-check --workspace PATH [--json]
  mithril-agent program workspace-doctor --workspace PATH --min-context-slot N [--json]
  mithril-agent program mcp --workspace PATH --sha256 HEX
  mithril-agent program mcp-config --workspace PATH --sha256 HEX --name NAME
  mithril-agent program inspect --idl PATH --program ADDRESS [--json]
  mithril-agent program pin --idl PATH --program ADDRESS --registry PATH [--json]
  mithril-agent program fetch (--workspace PATH | --program ADDRESS --registry PATH \
    --cluster devnet|testnet|mainnet-beta|alpenglow [--genesis-hash HASH] \
    --node-rpc LOOPBACK_URL) --min-context-slot N [--json]
  mithril-agent program show --program ADDRESS --sha256 HEX --registry PATH [--json]
  mithril-agent program evidence-pin --program ADDRESS --registry PATH \
    --kind repository|decompiler|simulation --file PATH --review PATH [--json]
  mithril-agent program evidence-show --program ADDRESS --registry PATH \
    --kind repository|decompiler|simulation --sha256 HEX [--json]
  mithril-agent program build --registry PATH --program ADDRESS --sha256 HEX \
    --instruction NAME --fee-payer ADDRESS --recent-blockhash HASH \
    --account NAME=ADDRESS ... [--arg NAME=JSON ...] [--json]
  mithril-agent program decode-account --registry PATH --program ADDRESS --sha256 HEX \
    --account-type NAME (--data PATH | --index-dir PATH --account ADDRESS) [--json]
  mithril-agent program decode-instruction --registry PATH --program ADDRESS --sha256 HEX \
    --instruction NAME --data PATH [--json]
  mithril-agent program decode-instruction --registry PATH --program ADDRESS --sha256 HEX \
    --instruction NAME --index-dir PATH --signature SIGNATURE --outer-index N [--json]
  mithril-agent program decode-instruction --registry PATH --program ADDRESS --sha256 HEX \
    --instruction NAME --index-dir PATH --signature SIGNATURE \
    --inner-group N --inner-index N [--json]
  mithril-agent program decode-event --registry PATH --program ADDRESS --sha256 HEX \
    --event-type NAME --index-dir PATH --signature SIGNATURE [--json]
  mithril-agent program read-account (--workspace PATH | --registry PATH --program ADDRESS \
    --cluster devnet|testnet|mainnet-beta|alpenglow [--genesis-hash HASH] \
    --node-rpc LOOPBACK_URL) --sha256 HEX \
    --account-type NAME --account ADDRESS --min-context-slot N [--json]
  mithril-agent program simulate (--workspace PATH | --registry PATH --program ADDRESS \
    --cluster devnet|testnet|mainnet-beta|alpenglow [--genesis-hash HASH] \
    --node-rpc LOOPBACK_URL) --sha256 HEX \
    --instruction NAME --fee-payer ADDRESS --account NAME=ADDRESS ... [--arg NAME=JSON ...] \
    --min-context-slot N [--json]

Validates a Solana IDL spec 0.1.0 or Codama 1.x interface against its program
address and exact SHA-256.
Pin stores the unchanged bytes in a private, immutable local registry. Fetch
loads a canonical direct or external-account Program Metadata IDL from the local
Mithril node and pins its exact JSON. Evidence-pin stores a reviewed repository
analysis, decompiler artifact, or simulation result by exact content hash. Its
strict version-3 review records the reviewer, decision, bounded summary, source
revision, tool version, pinned interface, genesis, exact processed bank, and
immutable deployment identity. Simulation reviews also require the exact
message SHA-256. Versions 1 and 2 remain readable historical pins but are never
exposed to the workspace MCP. Evidence is never executed or interpreted; it is
stored only for review. Show commands revalidate both files before displaying
a pin.
Decode-account decodes raw account bytes locally using the pinned Borsh type.
Read-account does the same from a fresh loopback Mithril read. Decode-event
reads one exact rooted transaction from the local index and decodes matching
program-data logs with the pinned Borsh event type. Decode-instruction accepts
either a local data file or one exact outer instruction from a rooted indexed
transaction. It can also decode one recorded CPI using its parent outer group
and inner index. Rooted results include the transaction outcome and provenance;
only outer instructions are part of the signed message.
Event decoding is limited to successful rooted transactions because failed
transaction logs describe reverted execution.
No program command loads a wallet, signs, or submits a transaction.

Legacy pre-0.30 Anchor IDLs must be converted to the current Solana IDL shape
with a top-level address before they can be pinned. Current Codama 1.x interfaces
are pinned unchanged and support the bounded deterministic codecs used here.
Unknown codecs and custom serialization fail closed. Instructions with dynamic
remaining accounts can be inspected and decoded, but construction requires a
reviewed dedicated adapter. Every account binding is required, including optional
accounts, rather than guessing an address.

Commands requiring both --registry and --program also accept --workspace PATH
instead. Explicit values cannot be mixed with a workspace. Alpenglow community
clusters may regenerate, so their exact genesis hash must be pinned explicitly;
Devnet and Mainnet use their built-in fixed identities. Workspace-doctor is
read-only and fails closed; on an operational failure it prints a bounded
reason and safe recovery steps. Its JSON output never includes configured
endpoints, local paths, raw provider errors, wallets, or keys.`

func runProgram(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, err := fmt.Fprintln(output, programUsage)
		return err
	}
	switch args[0] {
	case "workspace-create":
		return runProgramWorkspaceCreate(args[1:], output)
	case "workspace-check":
		return runProgramWorkspaceCheck(args[1:], output)
	case "workspace-doctor":
		return runProgramWorkspaceDoctor(ctx, args[1:], output)
	case "mcp":
		return runProgramMCP(ctx, args[1:], os.Stdin, output)
	case "mcp-config":
		return runProgramMCPConfig(args[1:], output)
	case "inspect":
		return runProgramInspect(args[1:], output)
	case "pin":
		return runProgramPin(args[1:], output)
	case "fetch":
		return runProgramFetch(ctx, args[1:], output)
	case "show":
		return runProgramShow(args[1:], output)
	case "evidence-pin":
		return runProgramEvidencePin(args[1:], output)
	case "evidence-show":
		return runProgramEvidenceShow(args[1:], output)
	case "build":
		return runProgramBuild(args[1:], output)
	case "decode-account":
		return runProgramDecodeAccount(args[1:], output)
	case "decode-instruction":
		return runProgramDecodeInstruction(args[1:], output)
	case "decode-event":
		return runProgramDecodeEvent(args[1:], output)
	case "read-account":
		return runProgramReadAccount(ctx, args[1:], output)
	case "simulate":
		return runProgramSimulate(ctx, args[1:], output)
	default:
		return errors.New("program requires the workspace-create, workspace-check, workspace-doctor, mcp, mcp-config, inspect, pin, fetch, show, evidence-pin, evidence-show, build, decode-account, decode-instruction, decode-event, read-account, or simulate subcommand")
	}
}

func runProgramEvidencePin(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("program evidence-pin", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspacePath := flags.String("workspace", "", "program workspace.json path")
	file := flags.String("file", "", "reviewed local evidence file")
	reviewPath := flags.String("review", "", "strict local review JSON")
	program := flags.String("program", "", "Solana program address")
	registry := flags.String("registry", "", "local program interface registry")
	kind := flags.String("kind", "", "repository, decompiler, or simulation")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, programUsage)
			return writeErr
		}
		return err
	}
	if err := applyLocalProgramWorkspace(*workspacePath, registry, program); err != nil {
		return fmt.Errorf("program evidence-pin: %w", err)
	}
	if flags.NArg() != 0 || *file == "" || *reviewPath == "" || *program == "" ||
		*registry == "" || *kind == "" {
		return errors.New("program evidence-pin requires --file, --review, --program, --registry, and --kind")
	}
	data, err := programinterface.ReadEvidence(*file)
	if err != nil {
		return err
	}
	review, err := programinterface.ReadEvidenceReview(*reviewPath, *kind)
	if err != nil {
		return err
	}
	result, err := programinterface.PinEvidence(*registry, *program, *kind, data, review)
	if err != nil {
		return err
	}
	return writeProgramEvidence(output, result, *jsonOutput)
}

func runProgramEvidenceShow(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("program evidence-show", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspacePath := flags.String("workspace", "", "program workspace.json path")
	program := flags.String("program", "", "Solana program address")
	registry := flags.String("registry", "", "local program interface registry")
	kind := flags.String("kind", "", "repository, decompiler, or simulation")
	sha256 := flags.String("sha256", "", "pinned evidence SHA-256")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, programUsage)
			return writeErr
		}
		return err
	}
	if err := applyLocalProgramWorkspace(*workspacePath, registry, program); err != nil {
		return fmt.Errorf("program evidence-show: %w", err)
	}
	if flags.NArg() != 0 || *program == "" || *registry == "" || *kind == "" || *sha256 == "" {
		return errors.New("program evidence-show requires --program, --registry, --kind, and --sha256")
	}
	result, err := programinterface.LoadEvidence(*registry, *program, *kind, *sha256)
	if err != nil {
		return err
	}
	return writeProgramEvidence(output, result, *jsonOutput)
}

func writeProgramEvidence(output io.Writer, result programinterface.EvidenceResult, jsonOutput bool) error {
	if jsonOutput {
		return json.NewEncoder(output).Encode(result)
	}
	status := "Program evidence verified"
	if result.Created {
		status = "Program evidence pinned"
	}
	interfaceSHA256 := result.Attestation.InterfaceSHA256
	if interfaceSHA256 == "" {
		interfaceSHA256 = "unbound legacy review"
	}
	_, err := fmt.Fprintf(output,
		"%s\nProgram: %s\nKind: %s\nSHA-256: %s\nBytes: %d\nDecision: %s\n"+
			"Reviewer: %s\nSummary: %s\nSource revision: %s\nTool: %s %s\nInterface SHA-256: %s\n"+
			"Genesis hash: %s\nContext slot: %d\nProcessed bank hash: %s\nDeployment SHA-256: %s\n"+
			"Result SHA-256: %s\nPin: %q\n",
		status, result.Program, result.Kind, result.SHA256, result.Bytes,
		result.Attestation.Decision, result.Attestation.Reviewer,
		result.Attestation.Summary, result.Attestation.SourceRevision, result.Attestation.Tool,
		result.Attestation.ToolVersion, interfaceSHA256, result.Attestation.GenesisHash,
		result.Attestation.ContextSlot, result.Attestation.Bankhash, result.Attestation.DeploymentSHA256,
		result.Attestation.ResultSHA256, result.Path)
	return err
}

func runProgramInspect(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("program inspect", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	idlPath := flags.String("idl", "", "Solana IDL JSON path")
	program := flags.String("program", "", "expected Solana program address")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, programUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *idlPath == "" || *program == "" {
		return errors.New("program inspect requires --idl and --program")
	}
	data, err := programinterface.Read(*idlPath)
	if err != nil {
		return err
	}
	report, err := programinterface.Inspect(data, *program)
	if err != nil {
		return err
	}
	return writeProgramReport(output, "Program interface verified", report, "", false, *jsonOutput)
}

func runProgramPin(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("program pin", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspacePath := flags.String("workspace", "", "program workspace.json path")
	idlPath := flags.String("idl", "", "Solana IDL JSON path")
	program := flags.String("program", "", "expected Solana program address")
	registry := flags.String("registry", "", "local program interface registry")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, programUsage)
			return writeErr
		}
		return err
	}
	if err := applyLocalProgramWorkspace(*workspacePath, registry, program); err != nil {
		return fmt.Errorf("program pin: %w", err)
	}
	if flags.NArg() != 0 || *idlPath == "" || *program == "" || *registry == "" {
		return errors.New("program pin requires --idl, --program, and --registry")
	}
	data, err := programinterface.Read(*idlPath)
	if err != nil {
		return err
	}
	result, err := programinterface.Pin(*registry, *program, data)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(output).Encode(result)
	}
	status := "Program interface already pinned"
	if result.Created {
		status = "Program interface pinned"
	}
	return writeProgramReport(output, status, result.Report, result.Path, result.Created, false)
}

func runProgramShow(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("program show", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspacePath := flags.String("workspace", "", "program workspace.json path")
	program := flags.String("program", "", "expected Solana program address")
	sha256 := flags.String("sha256", "", "pinned interface SHA-256")
	registry := flags.String("registry", "", "local program interface registry")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, programUsage)
			return writeErr
		}
		return err
	}
	if err := applyLocalProgramWorkspace(*workspacePath, registry, program); err != nil {
		return fmt.Errorf("program show: %w", err)
	}
	if flags.NArg() != 0 || *program == "" || *sha256 == "" || *registry == "" {
		return errors.New("program show requires --program, --sha256, and --registry")
	}
	report, path, err := programinterface.Load(*registry, *program, *sha256)
	if err != nil {
		return err
	}
	return writeProgramReport(output, "Pinned program interface verified", report, path, false, *jsonOutput)
}

func writeProgramReport(
	output io.Writer,
	status string,
	report programinterface.Report,
	path string,
	created, jsonOutput bool,
) error {
	if jsonOutput {
		return json.NewEncoder(output).Encode(struct {
			Status  string `json:"status"`
			Path    string `json:"path,omitempty"`
			Created bool   `json:"created,omitempty"`
			programinterface.Report
		}{Status: status, Path: path, Created: created, Report: report})
	}
	if _, err := fmt.Fprintf(output, "%s\nProgram: %s\nSHA-256: %s\n", status, report.Program, report.SHA256); err != nil {
		return err
	}
	if path != "" {
		if _, err := fmt.Fprintf(output, "Pin: %q\n", path); err != nil {
			return err
		}
	}
	if report.Name != "" || report.Version != "" || report.Spec != "" {
		if _, err := fmt.Fprintf(output, "Interface: name=%q version=%q spec=%q\n", report.Name, report.Version, report.Spec); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(output, "Instructions (%d):\n", len(report.Instructions)); err != nil {
		return err
	}
	for _, instruction := range report.Instructions {
		if _, err := fmt.Fprintf(output, "  - %s  discriminator=%s  accounts=%d  args=%d\n",
			instruction.Name, instruction.Discriminator, len(instruction.Accounts), len(instruction.Args)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(output, "Walletless: no key loaded, no network request, no transaction submitted.")
	return err
}
