package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/programinterface"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

const (
	programSimulationFeePayer = "SysvarC1ock11111111111111111111111111111111"
	programSimulationState    = "SysvarRent111111111111111111111111111111111"
)

type fakeProgramSimulationNode struct {
	genesis         string
	genesisErr      error
	verification    solanarpc.VerificationStatus
	verificationErr error
	rootedFeed      solanarpc.RootedFeedStatus
	rootedFeedErr   error
	genesisCalls    int
	blockhashCalls  int
	simulateCalls   int
	deploymentCalls int
	message         []byte
	bankhash        string
	deployment      *solanarpc.AccountDataSlice
}

func (node *fakeProgramSimulationNode) RootedFeedStatus(context.Context) (solanarpc.RootedFeedStatus, error) {
	return node.rootedFeed, node.rootedFeedErr
}

func (node *fakeProgramSimulationNode) VerificationStatus(context.Context) (solanarpc.VerificationStatus, error) {
	if node.verificationErr != nil {
		return solanarpc.VerificationStatus{}, node.verificationErr
	}
	if node.verification.State != "" {
		return node.verification, nil
	}
	return solanarpc.VerificationStatus{State: "complete", Required: true, Healthy: true, EvidenceServed: true}, nil
}

func (node *fakeProgramSimulationNode) GenesisHash(context.Context) (string, error) {
	node.genesisCalls++
	if node.genesisErr != nil {
		return "", node.genesisErr
	}
	if node.genesis != "" {
		return node.genesis, nil
	}
	return solana.MainnetBetaGenesisHash, nil
}

func TestVerifyProgramNodeClusterExplainsUnavailableNode(t *testing.T) {
	err := verifyProgramNodeCluster(t.Context(), &fakeProgramSimulationNode{
		verificationErr: context.DeadlineExceeded,
	}, "mainnet-beta", "")
	if err == nil || !strings.Contains(err.Error(), "compatible Mithril node") {
		t.Fatalf("cluster error = %v", err)
	}
}

func TestVerifyProgramNodeClusterRejectsClosedEvidenceGate(t *testing.T) {
	err := verifyProgramNodeCluster(t.Context(), &fakeProgramSimulationNode{
		verification: solanarpc.VerificationStatus{State: "diverged", Required: true, Reason: "diverged"},
	}, "mainnet-beta", "")
	if err == nil || !strings.Contains(err.Error(), "not serving verified evidence") {
		t.Fatalf("verification error = %v", err)
	}
}

func (node *fakeProgramSimulationNode) LatestBlockhash(
	_ context.Context,
	minContextSlot uint64,
) (solanarpc.LatestBlockhash, error) {
	node.blockhashCalls++
	return solanarpc.LatestBlockhash{
		ContextSlot: minContextSlot, Blockhash: programSimulationState, LastValidBlockHeight: 100,
	}, nil
}

func (node *fakeProgramSimulationNode) SimulateV0(
	_ context.Context,
	message []byte,
	_ map[[32]byte][][32]byte,
	minContextSlot uint64,
) (solanarpc.LegacySimulation, error) {
	node.simulateCalls++
	node.message = append([]byte(nil), message...)
	if len(message) == 0 {
		return solanarpc.LegacySimulation{}, context.Canceled
	}
	bankhash := node.bankhash
	if bankhash == "" {
		bankhash = programSimulationState
	}
	return solanarpc.LegacySimulation{
		ContextSlot: minContextSlot, Bankhash: bankhash, UnitsConsumed: 321,
		LogsSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, nil
}

func (node *fakeProgramSimulationNode) AccountData(
	_ context.Context, _ string, minContextSlot, _ uint64,
) (solanarpc.AccountDataSlice, error) {
	node.deploymentCalls++
	if node.deployment != nil {
		return *node.deployment, nil
	}
	bankhash := node.bankhash
	if bankhash == "" {
		bankhash = programSimulationState
	}
	return testLegacyProgramDeploymentAccount(minContextSlot, bankhash), nil
}

func TestProgramSimulateEncodesTypedArgument(t *testing.T) {
	registry, pin := pinProgramTypedIDL(t)
	fake := &fakeProgramSimulationNode{}
	original := openProgramSimulationNode
	openProgramSimulationNode = func(string) (programSimulationNode, error) { return fake, nil }
	t.Cleanup(func() { openProgramSimulationNode = original })

	if err := runContext(t.Context(), []string{
		"program", "simulate", "--registry", registry,
		"--program", programCommandAddress, "--sha256", pin.SHA256,
		"--instruction", "increment", "--fee-payer", programSimulationFeePayer,
		"--account", "state=" + programSimulationState, "--arg", "amount=513",
		"--cluster", "mainnet-beta", "--node-rpc", "http://127.0.0.1:8899",
		"--min-context-slot", "10",
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	decoded, err := solana.DecodeV0Message(fake.message, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{1, 1, 2, 0, 0, 0, 0, 0, 0}
	if len(decoded.Instructions) != 1 ||
		!bytes.Equal(decoded.Instructions[0].Data, want) {
		t.Fatalf("instruction data = %v", decoded.Instructions[0].Data)
	}
}

func TestProgramBuildProducesUnsignedBytesWithoutNetwork(t *testing.T) {
	registry, pin := pinProgramTypedIDL(t)
	var output bytes.Buffer
	if err := run([]string{
		"program", "build", "--json", "--registry", registry,
		"--program", programCommandAddress, "--sha256", pin.SHA256,
		"--instruction", "increment", "--fee-payer", programSimulationFeePayer,
		"--recent-blockhash", programSimulationState,
		"--account", "state=" + programSimulationState, "--arg", "amount=513",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result programBuildOutput
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	decoded, err := solana.DecodeV0Message(result.Message, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{1, 1, 2, 0, 0, 0, 0, 0, 0}
	if result.Status != "built" || !result.Walletless ||
		len(result.UnsignedTransaction) == 0 || result.MessageSHA256 == "" ||
		result.Review.FeePayer != programSimulationFeePayer ||
		len(result.Review.Accounts) != 1 || result.Review.Accounts[0].Name != "state" ||
		len(result.Review.Arguments) != 1 || string(result.Review.Arguments[0].Value) != "513" ||
		len(decoded.Instructions) != 1 ||
		!bytes.Equal(decoded.Instructions[0].Data, want) {
		t.Fatalf("result = %+v, instruction = %+v", result, decoded.Instructions)
	}
}

func TestProgramSimulateBuildsAndRunsWithoutSigning(t *testing.T) {
	registry, pin := pinProgramSimulationIDL(t)
	fake := &fakeProgramSimulationNode{}
	original := openProgramSimulationNode
	openProgramSimulationNode = func(string) (programSimulationNode, error) { return fake, nil }
	t.Cleanup(func() { openProgramSimulationNode = original })

	var output bytes.Buffer
	if err := runContext(t.Context(), []string{
		"program", "simulate", "--json",
		"--registry", registry,
		"--program", programCommandAddress,
		"--sha256", pin.SHA256,
		"--instruction", "increment",
		"--fee-payer", programSimulationFeePayer,
		"--account", "state=" + programSimulationState,
		"--cluster", "mainnet-beta",
		"--node-rpc", "http://127.0.0.1:8899",
		"--min-context-slot", "10",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result programSimulationOutput
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "simulated" || !result.Walletless || result.ContextSlot != 10 ||
		result.Bankhash != programSimulationState ||
		result.Provenance != programProcessedProvenance || result.Finality != programProcessedFinality ||
		result.UnitsConsumed != 321 || result.MessageSHA256 == "" ||
		result.Review.FeePayer != programSimulationFeePayer || len(result.Review.Accounts) != 1 ||
		result.GenesisHash != solana.MainnetBetaGenesisHash || result.DeploymentSHA256 == "" ||
		fake.genesisCalls != 1 || fake.blockhashCalls != 1 || fake.simulateCalls != 1 || fake.deploymentCalls != 1 {
		t.Fatalf("result = %+v, node = %+v", result, fake)
	}
}

func TestProgramMainnetWalletlessRehearsal(t *testing.T) {
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
	var pinOutput bytes.Buffer
	if err := run([]string{
		"program", "pin", "--json", "--workspace", workspace,
		"--idl", writeProgramCommandIDL(t),
	}, &pinOutput); err != nil {
		t.Fatal(err)
	}
	var pin programinterface.PinResult
	if err := json.Unmarshal(pinOutput.Bytes(), &pin); err != nil {
		t.Fatal(err)
	}
	fake := &fakeProgramSimulationNode{}
	original := openProgramSimulationNode
	openProgramSimulationNode = func(string) (programSimulationNode, error) { return fake, nil }
	t.Cleanup(func() { openProgramSimulationNode = original })

	var output bytes.Buffer
	if err := runContext(t.Context(), []string{
		"program", "simulate", "--json", "--workspace", workspace,
		"--sha256", pin.SHA256, "--instruction", "increment",
		"--fee-payer", programSimulationFeePayer,
		"--account", "state=" + programSimulationState,
		"--min-context-slot", "10",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result programSimulationOutput
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Cluster != "mainnet-beta" || !result.Walletless ||
		result.Bankhash != programSimulationState ||
		result.Review.FeePayer != programSimulationFeePayer ||
		result.GenesisHash != solana.MainnetBetaGenesisHash || result.DeploymentSHA256 == "" ||
		fake.genesisCalls != 1 || fake.blockhashCalls != 1 || fake.simulateCalls != 1 || fake.deploymentCalls != 1 {
		t.Fatalf("result = %+v, node = %+v", result, fake)
	}
}

func TestProgramSimulateRefusesClusterMismatch(t *testing.T) {
	registry, pin := pinProgramSimulationIDL(t)
	fake := &fakeProgramSimulationNode{}
	original := openProgramSimulationNode
	openProgramSimulationNode = func(string) (programSimulationNode, error) { return fake, nil }
	t.Cleanup(func() { openProgramSimulationNode = original })

	if err := runContext(t.Context(), []string{
		"program", "simulate", "--registry", registry,
		"--program", programCommandAddress, "--sha256", pin.SHA256,
		"--instruction", "increment", "--fee-payer", programSimulationFeePayer,
		"--account", "state=" + programSimulationState,
		"--cluster", "devnet", "--node-rpc", "http://127.0.0.1:8899",
		"--min-context-slot", "10",
	}, &bytes.Buffer{}); err == nil {
		t.Fatal("missing pin or cluster mismatch was accepted")
	}
	if fake.genesisCalls != 1 || fake.blockhashCalls != 0 || fake.simulateCalls != 0 {
		t.Fatalf("mismatch calls = %+v", fake)
	}
}

func TestProgramSimulateRejectsMissingProcessedBankIdentity(t *testing.T) {
	registry, pin := pinProgramSimulationIDL(t)
	fake := &fakeProgramSimulationNode{genesis: solana.DevnetGenesisHash, bankhash: programCommandAddress}
	original := openProgramSimulationNode
	openProgramSimulationNode = func(string) (programSimulationNode, error) { return fake, nil }
	t.Cleanup(func() { openProgramSimulationNode = original })

	err := runContext(t.Context(), []string{
		"program", "simulate", "--registry", registry,
		"--program", programCommandAddress, "--sha256", pin.SHA256,
		"--instruction", "increment", "--fee-payer", programSimulationFeePayer,
		"--account", "state=" + programSimulationState,
		"--cluster", "devnet", "--node-rpc", "http://127.0.0.1:8899",
		"--min-context-slot", "10",
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "evidence is incomplete") {
		t.Fatalf("missing bank identity error = %v", err)
	}
}

func TestProgramSimulateRejectsDeploymentFromAnotherProcessedBank(t *testing.T) {
	registry, pin := pinProgramSimulationIDL(t)
	deployment := testLegacyProgramDeploymentAccount(10, fakeProgramBankhash)
	fake := &fakeProgramSimulationNode{deployment: &deployment}
	original := openProgramSimulationNode
	openProgramSimulationNode = func(string) (programSimulationNode, error) { return fake, nil }
	t.Cleanup(func() { openProgramSimulationNode = original })

	err := runContext(t.Context(), []string{
		"program", "simulate", "--registry", registry,
		"--program", programCommandAddress, "--sha256", pin.SHA256,
		"--instruction", "increment", "--fee-payer", programSimulationFeePayer,
		"--account", "state=" + programSimulationState,
		"--cluster", "mainnet-beta", "--node-rpc", "http://127.0.0.1:8899",
		"--min-context-slot", "10",
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not from the simulated processed bank") {
		t.Fatalf("deployment bank mismatch = %v", err)
	}
}

func TestProgramSimulatePinsAlpenglowGenesis(t *testing.T) {
	registry, pin := pinProgramSimulationIDL(t)
	fake := &fakeProgramSimulationNode{genesis: programCommandAddress}
	original := openProgramSimulationNode
	openProgramSimulationNode = func(string) (programSimulationNode, error) { return fake, nil }
	t.Cleanup(func() { openProgramSimulationNode = original })

	if err := runContext(t.Context(), []string{
		"program", "simulate", "--registry", registry,
		"--program", programCommandAddress, "--sha256", pin.SHA256,
		"--instruction", "increment", "--fee-payer", programSimulationFeePayer,
		"--account", "state=" + programSimulationState,
		"--cluster", "alpenglow", "--genesis-hash", programCommandAddress,
		"--node-rpc", "http://127.0.0.1:8899", "--min-context-slot", "10",
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if fake.genesisCalls != 1 || fake.blockhashCalls != 1 || fake.simulateCalls != 1 || fake.deploymentCalls != 1 {
		t.Fatalf("alpenglow calls = %+v", fake)
	}
}

func pinProgramSimulationIDL(t *testing.T) (string, programinterface.PinResult) {
	t.Helper()
	registry, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	idl := []byte(`{"address":"11111111111111111111111111111111","metadata":{"name":"counter","version":"0.1.0","spec":"0.1.0"},"instructions":[{"name":"increment","discriminator":[1],"accounts":[{"name":"state","writable":true}],"args":[]}]}`)
	pin, err := programinterface.Pin(registry, programCommandAddress, idl)
	if err != nil {
		t.Fatal(err)
	}
	return registry, pin
}

func pinProgramTypedIDL(t *testing.T) (string, programinterface.PinResult) {
	t.Helper()
	registry, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	idl := []byte(`{"address":"11111111111111111111111111111111","metadata":{"name":"counter","version":"0.1.0","spec":"0.1.0"},"instructions":[{"name":"increment","discriminator":[1],"accounts":[{"name":"state","writable":true}],"args":[{"name":"amount","type":{"defined":{"name":"Amount"}}}]}],"types":[{"name":"Amount","type":{"kind":"type","alias":"u64"}}]}`)
	pin, err := programinterface.Pin(registry, programCommandAddress, idl)
	if err != nil {
		t.Fatal(err)
	}
	return registry, pin
}
