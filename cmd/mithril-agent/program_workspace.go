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

	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/rootedindex"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

const (
	programWorkspaceVersion  = 1
	programWorkspaceFile     = "workspace.json"
	programWorkspaceMaxBytes = 16 << 10
)

type programWorkspace struct {
	Version     int    `json:"version"`
	Program     string `json:"program"`
	Cluster     string `json:"cluster"`
	GenesisHash string `json:"genesis_hash,omitempty"`
	NodeRPC     string `json:"node_rpc"`
	Accounts    string `json:"accounts,omitempty"`
}

type programWorkspaceReport struct {
	Status           string   `json:"status"`
	Workspace        string   `json:"workspace"`
	Program          string   `json:"program"`
	Cluster          string   `json:"cluster"`
	GenesisHash      string   `json:"genesis_hash"`
	Registry         string   `json:"registry"`
	StateIndex       string   `json:"state_index"`
	ActivityIndex    string   `json:"activity_index"`
	WalletLoaded     bool     `json:"wallet_loaded"`
	ExplorerRequired bool     `json:"explorer_required"`
	NextSteps        []string `json:"next_steps"`
}

type programWorkspaceDoctorReport struct {
	Status                   string   `json:"status"`
	Ready                    bool     `json:"ready"`
	Reason                   string   `json:"reason,omitempty"`
	NextSteps                []string `json:"next_steps,omitempty"`
	Program                  string   `json:"program"`
	Cluster                  string   `json:"cluster"`
	GenesisHash              string   `json:"genesis_hash"`
	ContextSlot              uint64   `json:"context_slot"`
	Provenance               string   `json:"provenance"`
	Finality                 string   `json:"finality"`
	RootedReady              bool     `json:"rooted_ready"`
	RootedThroughSlot        uint64   `json:"rooted_through_slot,omitempty"`
	RootedProvenance         string   `json:"rooted_provenance,omitempty"`
	RootedFinality           string   `json:"rooted_finality,omitempty"`
	AccountsDBRootRunID      string   `json:"accountsdb_root_run_id,omitempty"`
	StateIndexThroughSlot    uint64   `json:"state_index_through_slot,omitempty"`
	ActivityIndexThroughSlot uint64   `json:"activity_index_through_slot,omitempty"`
	WalletLoaded             bool     `json:"wallet_loaded"`
	ExplorerRequired         bool     `json:"explorer_required"`
}

var errProgramWorkspaceNeedsAttention = errors.New("program workspace needs attention; follow the recovery steps above")

func runProgramWorkspaceCreate(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("program workspace-create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("dir", "", "private workspace directory")
	program := flags.String("program", "", "Solana program address")
	cluster := flags.String("cluster", "", "devnet, testnet, mainnet-beta, or alpenglow")
	genesisHash := flags.String("genesis-hash", "", "required exact genesis hash for alpenglow")
	nodeRPC := flags.String("node-rpc", "", "literal loopback Mithril RPC URL")
	accounts := flags.String("accounts", "", "private Mithril AccountsDB storage root (parent of accounts/)")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, programUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *dir == "" || *program == "" || *cluster == "" ||
		*nodeRPC == "" {
		return errors.New("program workspace-create requires --dir, --program, --cluster, and --node-rpc")
	}
	if *cluster == "alpenglow" && *accounts == "" {
		return errors.New("program workspace-create requires --accounts for Alpenglow rooted indexing")
	}
	workspace := programWorkspace{
		Version: programWorkspaceVersion, Program: *program, Cluster: *cluster,
		GenesisHash: *genesisHash, NodeRPC: *nodeRPC, Accounts: *accounts,
	}
	if err := validateProgramWorkspace(workspace); err != nil {
		return err
	}
	directory, err := cleanProgramWorkspacePath(*dir, "workspace directory")
	if err != nil {
		return err
	}
	if err := createPrivateDirectoryTree(directory); err != nil {
		return err
	}
	for _, child := range []string{"interfaces", "state-index", "activity-index"} {
		if err := createPrivateDirectoryTree(filepath.Join(directory, child)); err != nil {
			return err
		}
	}
	encoded, err := json.MarshalIndent(workspace, "", "  ")
	if err != nil {
		return errors.New("encode program workspace")
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(directory, programWorkspaceFile)
	created := true
	if err := securefile.CreatePrivate(path, encoded, programWorkspaceMaxBytes); err != nil {
		current, readErr := loadProgramWorkspace(path)
		if readErr != nil || current != workspace {
			return errors.New("program workspace already exists with different or unreadable settings")
		}
		created = false
	}
	status := "Program workspace already ready"
	if created {
		status = "Program workspace created"
	}
	return writeProgramWorkspaceReport(output, workspaceReport(status, path, workspace), *jsonOutput)
}

func runProgramWorkspaceCheck(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("program workspace-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("workspace", "", "program workspace.json path")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, programUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *path == "" {
		return errors.New("program workspace-check requires --workspace")
	}
	workspace, err := loadProgramWorkspace(*path)
	if err != nil {
		return err
	}
	cleanPath, err := cleanProgramWorkspacePath(*path, "workspace path")
	if err != nil {
		return err
	}
	for _, child := range []string{"interfaces", "state-index", "activity-index"} {
		if err := validatePrivateDirectory(filepath.Join(filepath.Dir(cleanPath), child)); err != nil {
			return errors.New("program workspace directory layout is not private and complete")
		}
	}
	return writeProgramWorkspaceReport(
		output, workspaceReport("Program workspace ready", cleanPath, workspace), *jsonOutput,
	)
}

func runProgramWorkspaceDoctor(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("program workspace-doctor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("workspace", "", "program workspace.json path")
	minContextSlot := flags.Uint64("min-context-slot", 0, "minimum retained Mithril slot")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, programUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *path == "" || *minContextSlot == 0 {
		return errors.New("program workspace-doctor requires --workspace and --min-context-slot")
	}
	workspace, err := loadProgramWorkspace(*path)
	if err != nil {
		return writeProgramWorkspaceDoctorFailure(output, *jsonOutput,
			"workspace configuration is invalid or unreadable",
			[]string{
				"Run program workspace-check with the same workspace file.",
				"Confirm workspace.json and its parent directories are still private and readable by this operator.",
				"Do not edit the existing workspace to change its identity; restore it from a trusted backup or create a new private workspace.",
			})
	}
	node, err := openProgramSimulationNode(workspace.NodeRPC)
	if err != nil {
		return writeProgramWorkspaceDoctorFailure(output, *jsonOutput,
			"configured Mithril node is unavailable or does not match the workspace",
			programWorkspaceNodeRecoverySteps)
	}
	expectedGenesis, err := expectedProgramGenesis(workspace.Cluster, workspace.GenesisHash)
	if err != nil {
		return writeProgramWorkspaceDoctorFailure(output, *jsonOutput,
			"workspace configuration is invalid or unreadable",
			[]string{
				"Run program workspace-check with the same workspace file.",
				"Create a new private workspace with the intended cluster and exact genesis hash.",
			})
	}
	if err := verifyProgramNodeCluster(ctx, node, workspace.Cluster, workspace.GenesisHash); err != nil {
		return writeProgramWorkspaceDoctorFailure(output, *jsonOutput,
			"configured Mithril node is unavailable or does not match the workspace",
			programWorkspaceNodeRecoverySteps)
	}
	blockhash, err := node.LatestBlockhash(ctx, *minContextSlot)
	if err != nil || blockhash.ContextSlot < *minContextSlot {
		return writeProgramWorkspaceDoctorFailure(output, *jsonOutput,
			"Mithril node has not reached the requested minimum slot",
			[]string{
				"Keep actions disabled and confirm the intended Mithril node is healthy and advancing.",
				"Wait for the node to retain the requested slot, then run workspace-doctor again.",
				"Use a lower minimum slot only when deliberately checking older evidence.",
			})
	}
	report := programWorkspaceDoctorReport{
		Status: "ready", Ready: true, Program: workspace.Program,
		Cluster: workspace.Cluster, GenesisHash: expectedGenesis, ContextSlot: blockhash.ContextSlot,
		Provenance: programProcessedProvenance, Finality: programProcessedFinality,
		WalletLoaded: false, ExplorerRequired: false,
	}
	if workspace.Accounts != "" {
		feed, err := node.RootedFeedStatus(ctx)
		if err != nil || !feed.Enabled {
			return writeProgramWorkspaceDoctorFailure(output, *jsonOutput,
				"configured Mithril node is not publishing a source-bound rooted feed",
				programWorkspaceRootedFeedRecoverySteps)
		}
		stateIndex, activityIndex, err := readProgramWorkspaceIndexes(*path)
		if err != nil {
			return writeProgramWorkspaceDoctorFailure(output, *jsonOutput,
				"rooted program indexes are incomplete or do not match the workspace",
				programWorkspaceIndexRecoverySteps)
		}
		if stateIndex.Source.AccountsDBRootRunID != feed.AccountsDBRootRunID {
			return writeProgramWorkspaceDoctorFailure(output, *jsonOutput,
				"rooted program indexes do not match the running Mithril AccountsDB lineage",
				programWorkspaceRootedFeedRecoverySteps)
		}
		if stateIndex.LastBatch == nil || activityIndex.LastBatch == nil ||
			stateIndex.LastBatch.ThroughSlot < *minContextSlot ||
			activityIndex.LastBatch.ThroughSlot < *minContextSlot {
			return writeProgramWorkspaceDoctorFailure(output, *jsonOutput,
				"rooted program indexes have not reached the requested minimum slot",
				[]string{
					"Keep both trusted workspace ingests running, or resume them exactly as directed by index doctor.",
					"Wait until both indexes publish a complete rooted batch at or beyond the requested slot.",
					"Run index doctor for both indexes, then run workspace-doctor again.",
				})
		}
		report.RootedReady = true
		report.StateIndexThroughSlot = stateIndex.LastBatch.ThroughSlot
		report.ActivityIndexThroughSlot = activityIndex.LastBatch.ThroughSlot
		report.RootedThroughSlot = min(report.StateIndexThroughSlot, report.ActivityIndexThroughSlot)
		report.RootedProvenance = stateIndex.Provenance
		report.RootedFinality = stateIndex.Finality
		report.AccountsDBRootRunID = stateIndex.Source.AccountsDBRootRunID
	}
	if *jsonOutput {
		return json.NewEncoder(output).Encode(report)
	}
	_, err = fmt.Fprintf(output, `Program workspace and Mithril node ready
Program: %s
Cluster: %s
Genesis hash: %s
Node context slot: %d
Node context provenance: Mithril processed bank (not rooted)
Rooted indexes ready: %t
Rooted through slot: %d
Wallet loaded: no
Explorer required: no
`, report.Program, report.Cluster, report.GenesisHash, report.ContextSlot,
		report.RootedReady, report.RootedThroughSlot)
	return err
}

var programWorkspaceNodeRecoverySteps = []string{
	"Keep actions disabled and confirm the intended Mithril node is running, advancing, and reachable only through loopback.",
	"Confirm the node uses the workspace cluster and genesis; do not change the workspace identity to silence a mismatch.",
	"Run workspace-doctor again after the node is healthy.",
}

var programWorkspaceIndexRecoverySteps = []string{
	"Keep both existing index directories unchanged.",
	"Run index doctor for the private state-index and activity-index directories beside workspace.json; program workspace-check prints their paths when the layout is intact.",
	"Resume only the same trusted Mithril source and permanent workspace filters as directed by index doctor.",
	"Run workspace-doctor again after both index doctors report ready.",
}

var programWorkspaceRootedFeedRecoverySteps = []string{
	"Keep the existing indexes unchanged and keep actions disabled.",
	"Confirm the matched Mithril build is running with storage.rooted_events enabled for this workspace's AccountsDB.",
	"If the node was rebuilt, create new private indexes from its framed rooted feed; do not relabel indexes from the prior AccountsDB lineage.",
}

func writeProgramWorkspaceDoctorFailure(output io.Writer, jsonOutput bool, reason string, next []string) error {
	report := programWorkspaceDoctorReport{
		Status: "attention_required", Ready: false, Reason: reason, NextSteps: next,
		WalletLoaded: false, ExplorerRequired: false,
	}
	if jsonOutput {
		if err := json.NewEncoder(output).Encode(report); err != nil {
			return err
		}
		return errProgramWorkspaceNeedsAttention
	}
	if _, err := fmt.Fprintf(output, "Program workspace needs attention\nReason: %s\nSafe recovery:\n", reason); err != nil {
		return err
	}
	for i, step := range next {
		if _, err := fmt.Fprintf(output, "%d. %s\n", i+1, step); err != nil {
			return err
		}
	}
	return errProgramWorkspaceNeedsAttention
}

func loadProgramWorkspace(path string) (programWorkspace, error) {
	path, err := cleanProgramWorkspacePath(path, "workspace path")
	if err != nil {
		return programWorkspace{}, err
	}
	if filepath.Base(path) != programWorkspaceFile {
		return programWorkspace{}, errors.New("program workspace path must end in workspace.json")
	}
	raw, err := securefile.ReadPrivate(path, programWorkspaceMaxBytes)
	if err != nil {
		return programWorkspace{}, errors.New("read private program workspace")
	}
	var workspace programWorkspace
	if err := strictjson.Decode(raw, &workspace); err != nil {
		return programWorkspace{}, errors.New("program workspace JSON is invalid")
	}
	if err := validateProgramWorkspace(workspace); err != nil {
		return programWorkspace{}, err
	}
	return workspace, nil
}

func applyProgramWorkspace(path string, registry, program, cluster, genesisHash, nodeRPC *string) error {
	if path == "" {
		return nil
	}
	if *registry != "" || *program != "" || *cluster != "" || *genesisHash != "" || *nodeRPC != "" {
		return errors.New("--workspace cannot be combined with --registry, --program, --cluster, --genesis-hash, or --node-rpc")
	}
	workspace, err := loadProgramWorkspace(path)
	if err != nil {
		return err
	}
	cleanPath, err := cleanProgramWorkspacePath(path, "workspace path")
	if err != nil {
		return err
	}
	report := workspaceReport("", cleanPath, workspace)
	*registry, *program, *cluster, *genesisHash, *nodeRPC = report.Registry, workspace.Program,
		workspace.Cluster, workspace.GenesisHash, workspace.NodeRPC
	return nil
}

func applyLocalProgramWorkspace(path string, registry, program *string) error {
	if path == "" {
		return nil
	}
	if *registry != "" || *program != "" {
		return errors.New("--workspace cannot be combined with --registry or --program")
	}
	workspace, err := loadProgramWorkspace(path)
	if err != nil {
		return err
	}
	cleanPath, err := cleanProgramWorkspacePath(path, "workspace path")
	if err != nil {
		return err
	}
	*registry, *program = workspaceReport("", cleanPath, workspace).Registry, workspace.Program
	return nil
}

func validateProgramWorkspace(workspace programWorkspace) error {
	if workspace.Version != programWorkspaceVersion {
		return errors.New("program workspace version is unsupported")
	}
	if _, err := solana.Decode32(workspace.Program); err != nil {
		return errors.New("program workspace program is not a canonical Solana address")
	}
	if _, err := expectedProgramGenesis(workspace.Cluster, workspace.GenesisHash); err != nil {
		return err
	}
	if _, err := solanarpc.NewMithrilNode(workspace.NodeRPC, nil); err != nil {
		return errors.New("program workspace Mithril RPC must be a literal loopback URL")
	}
	if workspace.Accounts == "" {
		if workspace.Cluster == "alpenglow" {
			return errors.New("program workspace Alpenglow AccountsDB storage root is required")
		}
		return nil
	}
	accounts, err := cleanProgramWorkspacePath(workspace.Accounts, "Mithril AccountsDB storage root")
	if err != nil || accounts != workspace.Accounts || secureexec.ValidateProtectedDirectory(accounts) != nil {
		return errors.New("program workspace AccountsDB storage root is not trusted")
	}
	return nil
}

func validateProgramWorkspaceIndex(workspacePath, indexDir, kind string) (rootedindex.Status, error) {
	workspace, err := loadProgramWorkspace(workspacePath)
	if err != nil {
		return rootedindex.Status{}, err
	}
	cleanPath, err := cleanProgramWorkspacePath(workspacePath, "workspace path")
	if err != nil {
		return rootedindex.Status{}, err
	}
	view := workspaceReport("", cleanPath, workspace)
	expectedDir := view.StateIndex
	expectedFilter := rootedindex.Filter{Owner: workspace.Program}
	if kind == "activity" {
		expectedDir = view.ActivityIndex
		expectedFilter = rootedindex.Filter{Mention: workspace.Program}
	} else if kind != "state" {
		return rootedindex.Status{}, errors.New("program workspace index kind is invalid")
	}
	if indexDir != expectedDir {
		return rootedindex.Status{}, errors.New("rooted index is not the workspace's fixed " + kind + " index")
	}
	status, err := rootedindex.ReadCompleteStatus(indexDir)
	if err != nil {
		return rootedindex.Status{}, err
	}
	expectedGenesis, err := expectedProgramGenesis(workspace.Cluster, workspace.GenesisHash)
	if err != nil || status.Source.Cluster != workspace.Cluster || status.Source.GenesisHash != expectedGenesis {
		return rootedindex.Status{}, errors.New("rooted index source does not match the program workspace")
	}
	if status.Filter != expectedFilter {
		return rootedindex.Status{}, errors.New("rooted index filter does not match the program workspace")
	}
	return status, nil
}

func readProgramWorkspaceIndexes(workspacePath string) (rootedindex.Status, rootedindex.Status, error) {
	state, err := validateProgramWorkspaceIndex(workspacePath,
		filepath.Join(filepath.Dir(workspacePath), "state-index"), "state")
	if err != nil {
		return rootedindex.Status{}, rootedindex.Status{}, err
	}
	activity, err := validateProgramWorkspaceIndex(workspacePath,
		filepath.Join(filepath.Dir(workspacePath), "activity-index"), "activity")
	if err != nil {
		return rootedindex.Status{}, rootedindex.Status{}, err
	}
	if state.Source != activity.Source {
		return rootedindex.Status{}, rootedindex.Status{},
			errors.New("program workspace indexes come from different Mithril AccountsDB lineages")
	}
	for _, pair := range [][2]*rootedindex.BatchDescriptor{
		{state.FirstBatch, activity.FirstBatch}, {state.LastBatch, activity.LastBatch},
	} {
		if pair[0] != nil && pair[1] != nil && pair[0].ManifestSequence == pair[1].ManifestSequence &&
			*pair[0] != *pair[1] {
			return rootedindex.Status{}, rootedindex.Status{},
				errors.New("program workspace indexes disagree on a Mithril rooted batch")
		}
	}
	return state, activity, nil
}

func validateProgramWorkspaceIndexes(workspacePath string) error {
	_, _, err := readProgramWorkspaceIndexes(workspacePath)
	return err
}

func workspaceReport(status, path string, workspace programWorkspace) programWorkspaceReport {
	dir := filepath.Dir(path)
	genesisHash, _ := expectedProgramGenesis(workspace.Cluster, workspace.GenesisHash)
	next := []string{
		"Pin a reviewed local interface with program pin, or fetch canonical Program Metadata with program fetch.",
		"Run program workspace-doctor after the Mithril node reaches the minimum slot you intend to use.",
		"Use program build, program simulate, or program mcp only after the pinned interface and doctor checks pass.",
	}
	if workspace.Accounts != "" {
		next = []string{
			"Pin a reviewed local interface with program pin, or fetch canonical Program Metadata with program fetch.",
			"Start the documented state and activity rooted-index ingests with this workspace's permanent filters.",
			"Run index doctor for both indexes, then run program workspace-doctor at the intended minimum slot.",
			"Use program build, program simulate, or program mcp only after the pinned interface and doctor checks pass.",
		}
	}
	return programWorkspaceReport{
		Status: status, Workspace: path, Program: workspace.Program, Cluster: workspace.Cluster,
		GenesisHash: genesisHash,
		Registry:    filepath.Join(dir, "interfaces"), StateIndex: filepath.Join(dir, "state-index"),
		ActivityIndex: filepath.Join(dir, "activity-index"), WalletLoaded: false, ExplorerRequired: false,
		NextSteps: next,
	}
}

func writeProgramWorkspaceReport(output io.Writer, report programWorkspaceReport, jsonOutput bool) error {
	if jsonOutput {
		return json.NewEncoder(output).Encode(report)
	}
	_, err := fmt.Fprintf(output, `%s
Program: %s
Cluster: %s
Genesis hash: %s
Workspace: %q
Interface registry: %q
State index: %q
Activity index: %q
Wallet loaded: no
Explorer required: no
`, report.Status, report.Program, report.Cluster, report.GenesisHash, report.Workspace, report.Registry,
		report.StateIndex, report.ActivityIndex)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, "Next steps:"); err != nil {
		return err
	}
	for index, step := range report.NextSteps {
		if _, err := fmt.Fprintf(output, "%d. %s\n", index+1, step); err != nil {
			return err
		}
	}
	return nil
}

func cleanProgramWorkspacePath(path, label string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("%s must be an absolute clean path", label)
	}
	return path, nil
}

func createPrivateDirectoryTree(path string) error {
	path, err := cleanProgramWorkspacePath(path, "private directory")
	if err != nil {
		return err
	}
	existing := path
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("inspect private directory ancestry")
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return errors.New("private directory has no existing ancestor")
		}
		existing = parent
	}
	if err := secureexec.ValidateProtectedDirectory(existing); err != nil {
		return errors.New("private directory ancestry is not trusted")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return errors.New("create private directory")
	}
	return validatePrivateDirectory(path)
}

func validatePrivateDirectory(path string) error {
	if err := secureexec.ValidateProtectedDirectory(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("directory must be private mode 0700")
	}
	return nil
}
