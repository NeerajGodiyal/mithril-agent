package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/rootedindex"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

func TestProgramWorkspaceCreateCheckAndReuse(t *testing.T) {
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
		"program", "workspace-create", "--json", "--dir", dir,
		"--program", programCommandAddress, "--cluster", "devnet",
		"--node-rpc", "http://127.0.0.1:8899", "--accounts", accounts,
	}
	var output bytes.Buffer
	if err := runContext(t.Context(), args, &output); err != nil {
		t.Fatal(err)
	}
	var created programWorkspaceReport
	if err := json.Unmarshal(output.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Status != "Program workspace created" || created.WalletLoaded || created.ExplorerRequired ||
		len(created.NextSteps) != 4 || !strings.Contains(created.NextSteps[1], "rooted-index ingests") {
		t.Fatalf("report = %+v", created)
	}
	for _, path := range []string{dir, created.Registry, created.StateIndex, created.ActivityIndex} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode = %v", path, info.Mode().Perm())
		}
	}
	info, err := os.Stat(created.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("workspace mode = %v", info.Mode().Perm())
	}

	output.Reset()
	if err := runContext(t.Context(), args, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"status":"Program workspace already ready"`) {
		t.Fatalf("reuse output = %s", output.String())
	}

	output.Reset()
	if err := runContext(t.Context(), []string{
		"program", "workspace-check", "--workspace", created.Workspace,
	}, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "127.0.0.1") || strings.Contains(output.String(), accounts) ||
		!strings.Contains(output.String(), "Wallet loaded: no") ||
		!strings.Contains(output.String(), "Explorer required: no") ||
		!strings.Contains(output.String(), "Next steps:") ||
		!strings.Contains(output.String(), "program workspace-doctor") {
		t.Fatalf("check output = %s", output.String())
	}
}

func TestClassicProgramWorkspaceDoesNotRequireAccountsDB(t *testing.T) {
	for _, cluster := range []string{"devnet", "testnet", "mainnet-beta"} {
		t.Run(cluster, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			workspace := filepath.Join(root, "workspace")
			args := []string{
				"program", "workspace-create", "--dir", workspace,
				"--program", programCommandAddress, "--cluster", cluster,
				"--node-rpc", "http://127.0.0.1:8899",
			}
			if err := runContext(t.Context(), args, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if err := runContext(t.Context(), []string{
				"program", "workspace-check", "--workspace", filepath.Join(workspace, programWorkspaceFile),
			}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProgramWorkspaceRefusesRemoteRPCAndOpenDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	accounts := filepath.Join(root, "accounts")
	if err := os.Mkdir(accounts, 0o700); err != nil {
		t.Fatal(err)
	}
	base := []string{
		"program", "workspace-create", "--dir", filepath.Join(root, "workspace"),
		"--program", programCommandAddress, "--cluster", "devnet", "--accounts", accounts,
	}
	if err := runContext(t.Context(), append(base, "--node-rpc", "https://rpc.example"), &bytes.Buffer{}); err == nil {
		t.Fatal("remote Mithril RPC was accepted")
	}
	if err := os.Chmod(accounts, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := runContext(t.Context(), append(base, "--node-rpc", "http://127.0.0.1:8899"), &bytes.Buffer{}); err == nil {
		t.Fatal("open accounts directory was accepted")
	}
}

func TestProgramWorkspaceRefusesDifferentExistingConfiguration(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	accounts := filepath.Join(root, "accounts")
	if err := os.Mkdir(accounts, 0o700); err != nil {
		t.Fatal(err)
	}
	base := []string{
		"program", "workspace-create", "--dir", filepath.Join(root, "workspace"),
		"--program", programCommandAddress, "--node-rpc", "http://127.0.0.1:8899",
		"--accounts", accounts,
	}
	if err := runContext(t.Context(), append(base, "--cluster", "devnet"), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := runContext(t.Context(), append(base, "--cluster", "mainnet-beta"), &bytes.Buffer{}); err == nil {
		t.Fatal("different existing workspace configuration was accepted")
	}
}

func TestProgramWorkspaceRequiresPinnedAlpenglowGenesis(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	accounts := filepath.Join(root, "accounts")
	if err := os.Mkdir(accounts, 0o700); err != nil {
		t.Fatal(err)
	}
	base := []string{
		"program", "workspace-create", "--dir", filepath.Join(root, "workspace"),
		"--program", programCommandAddress, "--cluster", "alpenglow",
		"--node-rpc", "http://127.0.0.1:8899", "--accounts", accounts,
	}
	if err := runContext(t.Context(), base, &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), "genesis-hash") {
		t.Fatalf("missing Alpenglow genesis err = %v", err)
	}
	withoutAccounts := []string{
		"program", "workspace-create", "--dir", filepath.Join(root, "without-accounts"),
		"--program", programCommandAddress, "--cluster", "alpenglow",
		"--node-rpc", "http://127.0.0.1:8899", "--genesis-hash", programCommandAddress,
	}
	if err := runContext(t.Context(), withoutAccounts, &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), "--accounts") {
		t.Fatalf("missing Alpenglow accounts err = %v", err)
	}
	if err := runContext(t.Context(), append(base, "--genesis-hash", programCommandAddress), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func TestProgramPinUsesWorkspaceProgramAndRegistry(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	accounts := filepath.Join(root, "accounts")
	if err := os.Mkdir(accounts, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "workspace")
	if err := runContext(t.Context(), []string{
		"program", "workspace-create", "--dir", dir,
		"--program", programCommandAddress, "--cluster", "devnet",
		"--node-rpc", "http://127.0.0.1:8899", "--accounts", accounts,
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run([]string{
		"program", "pin", "--json", "--workspace", filepath.Join(dir, programWorkspaceFile),
		"--idl", writeProgramCommandIDL(t),
	}, &output); err != nil {
		t.Fatal(err)
	}
	var pinned struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(output.Bytes(), &pinned); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pinned.Path, filepath.Join(dir, "interfaces", programCommandAddress)+string(os.PathSeparator)) {
		t.Fatalf("pin path = %q", pinned.Path)
	}
}

func TestProgramWorkspaceDoctorChecksClusterAndFreshnessWithoutSimulation(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace", programWorkspaceFile)
	if err := runContext(t.Context(), []string{
		"program", "workspace-create", "--dir", filepath.Dir(workspace),
		"--program", programCommandAddress, "--cluster", "mainnet-beta",
		"--node-rpc", "http://127.0.0.1:8899",
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeProgramSimulationNode{}
	original := openProgramSimulationNode
	openProgramSimulationNode = func(string) (programSimulationNode, error) { return fake, nil }
	t.Cleanup(func() { openProgramSimulationNode = original })

	var output bytes.Buffer
	if err := runContext(t.Context(), []string{
		"program", "workspace-doctor", "--json", "--workspace", workspace,
		"--min-context-slot", "10",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var report programWorkspaceDoctorReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Ready || report.Status != "ready" || report.ContextSlot != 10 || report.Cluster != "mainnet-beta" ||
		report.Provenance != programProcessedProvenance || report.Finality != programProcessedFinality ||
		report.WalletLoaded || report.ExplorerRequired ||
		fake.genesisCalls != 1 || fake.blockhashCalls != 1 || fake.simulateCalls != 0 {
		t.Fatalf("report = %+v, node = %+v", report, fake)
	}
}

func TestProgramWorkspaceDoctorAcceptsClassicFinalizedIndexes(t *testing.T) {
	workspace, _ := createProgramMCPWorkspaceForCluster(
		t, "mainnet-beta", solana.MainnetBetaGenesisHash,
	)
	seedProgramMCPIndexesFromSource(t, workspace, testClassicRootedSource())
	fake := &fakeProgramSimulationNode{
		genesis: solana.MainnetBetaGenesisHash,
		rootedFeed: solanarpc.RootedFeedStatus{
			Enabled: true, AccountsDBRootRunID: testClassicRootedSource().AccountsDBRootRunID,
		},
	}
	original := openProgramSimulationNode
	openProgramSimulationNode = func(string) (programSimulationNode, error) { return fake, nil }
	t.Cleanup(func() { openProgramSimulationNode = original })

	var output bytes.Buffer
	if err := runContext(t.Context(), []string{
		"program", "workspace-doctor", "--json", "--workspace", workspace,
		"--min-context-slot", "2",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var report programWorkspaceDoctorReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Ready || !report.RootedReady || report.RootedThroughSlot != 2 ||
		report.RootedProvenance != rootedindex.ClassicFinalizedRootedProvenance ||
		report.RootedFinality != rootedindex.RootedFinality {
		t.Fatalf("report = %+v", report)
	}
	fake.rootedFeed.AccountsDBRootRunID = "89abcdef"
	output.Reset()
	err := runContext(t.Context(), []string{
		"program", "workspace-doctor", "--json", "--workspace", workspace,
		"--min-context-slot", "2",
	}, &output)
	if !errors.Is(err, errProgramWorkspaceNeedsAttention) {
		t.Fatalf("mismatched live lineage error = %v", err)
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Ready || report.Reason != "rooted program indexes do not match the running Mithril AccountsDB lineage" {
		t.Fatalf("mismatched live lineage report = %+v", report)
	}
}

func TestProgramWorkspaceDoctorRequiresFreshSourceBoundAlpenglowIndexes(t *testing.T) {
	workspace, _ := createProgramMCPWorkspace(t)
	seedProgramMCPIndexes(t, workspace)
	fake := &fakeProgramSimulationNode{
		genesis: testRootedSource().GenesisHash,
		rootedFeed: solanarpc.RootedFeedStatus{
			Enabled: true, AccountsDBRootRunID: testRootedSource().AccountsDBRootRunID,
		},
	}
	original := openProgramSimulationNode
	openProgramSimulationNode = func(string) (programSimulationNode, error) { return fake, nil }
	t.Cleanup(func() { openProgramSimulationNode = original })

	var output bytes.Buffer
	if err := runContext(t.Context(), []string{
		"program", "workspace-doctor", "--json", "--workspace", workspace,
		"--min-context-slot", "2",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var report programWorkspaceDoctorReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Ready || report.Status != "ready" || !report.RootedReady || report.RootedThroughSlot != 2 ||
		report.StateIndexThroughSlot != 2 || report.ActivityIndexThroughSlot != 2 ||
		report.RootedProvenance != rootedindex.RootedProvenance ||
		report.RootedFinality != rootedindex.RootedFinality ||
		report.AccountsDBRootRunID != testRootedSource().AccountsDBRootRunID {
		t.Fatalf("report = %+v", report)
	}
	output.Reset()
	err := runContext(t.Context(), []string{
		"program", "workspace-doctor", "--json", "--workspace", workspace,
		"--min-context-slot", "3",
	}, &output)
	if !errors.Is(err, errProgramWorkspaceNeedsAttention) {
		t.Fatalf("stale rooted indexes error = %v", err)
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Ready || report.Status != "attention_required" ||
		report.Reason != "rooted program indexes have not reached the requested minimum slot" ||
		len(report.NextSteps) == 0 {
		t.Fatalf("stale rooted indexes report = %+v", report)
	}
}

func TestProgramWorkspaceDoctorFailureOutputIsBounded(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "private-workspace")
	workspace := filepath.Join(dir, programWorkspaceFile)
	if err := runContext(t.Context(), []string{
		"program", "workspace-create", "--dir", dir,
		"--program", programCommandAddress, "--cluster", "mainnet-beta",
		"--node-rpc", "http://127.0.0.1:8899",
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	original := openProgramSimulationNode
	openProgramSimulationNode = func(string) (programSimulationNode, error) {
		return nil, errors.New("private provider detail")
	}
	t.Cleanup(func() { openProgramSimulationNode = original })

	for _, jsonOutput := range []bool{true, false} {
		args := []string{
			"program", "workspace-doctor", "--workspace", workspace,
			"--min-context-slot", "10",
		}
		if jsonOutput {
			args = append(args, "--json")
		}
		var output bytes.Buffer
		err := runContext(t.Context(), args, &output)
		if !errors.Is(err, errProgramWorkspaceNeedsAttention) {
			t.Fatalf("json=%t error = %v", jsonOutput, err)
		}
		for _, secret := range []string{workspace, "127.0.0.1", "private provider detail"} {
			if strings.Contains(output.String(), secret) {
				t.Fatalf("json=%t output contains %q: %s", jsonOutput, secret, output.String())
			}
		}
		if jsonOutput {
			var report programWorkspaceDoctorReport
			if err := json.Unmarshal(output.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if report.Ready || report.Status != "attention_required" ||
				report.Reason != "configured Mithril node is unavailable or does not match the workspace" ||
				len(report.NextSteps) == 0 {
				t.Fatalf("failure report = %+v", report)
			}
		} else if !strings.Contains(output.String(), "Safe recovery:") {
			t.Fatalf("human recovery output = %s", output.String())
		}
	}
}
