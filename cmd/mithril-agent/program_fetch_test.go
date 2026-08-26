package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/programinterface"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

type fakeProgramPMPNode struct {
	genesis     string
	account     solanarpc.AccountDataSlice
	deployment  *solanarpc.AccountDataSlice
	genesisRead int
	accountRead int
}

var fakeProgramBankhash = solana.Encode(bytes.Repeat([]byte{5}, 32))

func (*fakeProgramPMPNode) VerificationStatus(context.Context) (solanarpc.VerificationStatus, error) {
	return solanarpc.VerificationStatus{State: "complete", Required: true, Healthy: true, EvidenceServed: true}, nil
}

func (node *fakeProgramPMPNode) GenesisHash(context.Context) (string, error) {
	node.genesisRead++
	return node.genesis, nil
}

func (node *fakeProgramPMPNode) AccountData(
	_ context.Context, address string, _, _ uint64,
) (solanarpc.AccountDataSlice, error) {
	node.accountRead++
	result := node.account
	if address == programCommandAddress && node.deployment != nil {
		result = *node.deployment
	}
	if result.Bankhash == "" {
		result.Bankhash = fakeProgramBankhash
	}
	return result, nil
}

func (node *fakeProgramPMPNode) AccountDataRange(
	_ context.Context, _ string, _, _, _ uint64,
) (solanarpc.AccountDataSlice, error) {
	node.accountRead++
	result := node.account
	if result.Bankhash == "" {
		result.Bankhash = fakeProgramBankhash
	}
	return result, nil
}

func TestProgramFetchPinsCanonicalPMPWithoutWallet(t *testing.T) {
	registry := t.TempDir()
	idl := []byte(`{"address":"11111111111111111111111111111111","metadata":{"name":"system","version":"1","spec":"0.1.0"},"instructions":[]}`)
	account := directPMPAccount(t, programCommandAddress, idl)
	fake := &fakeProgramPMPNode{
		genesis: solana.DevnetGenesisHash,
		account: solanarpc.AccountDataSlice{
			ContextSlot: 91, Owner: programinterface.ProgramMetadataProgram,
			DataLength: uint64(len(account)), Data: account,
		},
	}
	original := openProgramPMPNode
	openProgramPMPNode = func(string) (programPMPNode, error) { return fake, nil }
	t.Cleanup(func() { openProgramPMPNode = original })

	var output bytes.Buffer
	if err := runContext(t.Context(), []string{
		"program", "fetch", "--json", "--registry", registry,
		"--program", programCommandAddress, "--cluster", "devnet",
		"--node-rpc", "http://127.0.0.1:8899", "--min-context-slot", "90",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result programFetchOutput
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "Canonical program interface pinned" || result.Cluster != "devnet" ||
		result.Provenance != programProcessedProvenance || result.Finality != programProcessedFinality ||
		result.Source != "canonical_pmp_direct" || result.ContextSlot != 91 || result.Bankhash != fakeProgramBankhash ||
		result.SHA256 == "" || fake.genesisRead != 1 || fake.accountRead != 1 {
		t.Fatalf("result = %+v, node = %+v", result, fake)
	}
}

func TestProgramFetchPinsCanonicalCodamaWithoutWallet(t *testing.T) {
	registry := t.TempDir()
	idl := []byte(`{
  "kind":"rootNode","standard":"codama","version":"1.0.0",
  "program":{"kind":"programNode","name":"system",
    "publicKey":"11111111111111111111111111111111","version":"1.0.0",
    "instructions":[],"definedTypes":[],"accounts":[],"events":[]}
}`)
	account := directPMPAccount(t, programCommandAddress, idl)
	fake := &fakeProgramPMPNode{
		genesis: solana.DevnetGenesisHash,
		account: solanarpc.AccountDataSlice{
			ContextSlot: 91, Owner: programinterface.ProgramMetadataProgram,
			DataLength: uint64(len(account)), Data: account,
		},
	}
	original := openProgramPMPNode
	openProgramPMPNode = func(string) (programPMPNode, error) { return fake, nil }
	t.Cleanup(func() { openProgramPMPNode = original })

	var output bytes.Buffer
	if err := runContext(t.Context(), []string{
		"program", "fetch", "--json", "--registry", registry,
		"--program", programCommandAddress, "--cluster", "devnet",
		"--node-rpc", "http://127.0.0.1:8899", "--min-context-slot", "90",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result programFetchOutput
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "Canonical program interface pinned" || result.Source != "canonical_pmp_direct" ||
		result.SHA256 == "" || fake.genesisRead != 1 || fake.accountRead != 1 {
		t.Fatalf("result = %+v, node = %+v", result, fake)
	}
	loaded, _, err := programinterface.Load(registry, programCommandAddress, result.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Spec != "codama/1.0.0" {
		t.Fatalf("loaded interface = %+v", loaded)
	}
}

func TestProgramFetchUsesPrivateWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	accounts := filepath.Join(root, "accounts")
	if err := os.Mkdir(accounts, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceDir := filepath.Join(root, "workspace")
	if err := runContext(t.Context(), []string{
		"program", "workspace-create", "--dir", workspaceDir,
		"--program", programCommandAddress, "--cluster", "devnet",
		"--node-rpc", "http://127.0.0.1:8899", "--accounts", accounts,
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	idl := []byte(`{"address":"11111111111111111111111111111111","metadata":{"name":"system","version":"1","spec":"0.1.0"},"instructions":[]}`)
	account := directPMPAccount(t, programCommandAddress, idl)
	fake := &fakeProgramPMPNode{
		genesis: solana.DevnetGenesisHash,
		account: solanarpc.AccountDataSlice{
			ContextSlot: 91, Owner: programinterface.ProgramMetadataProgram,
			DataLength: uint64(len(account)), Data: account,
		},
	}
	original := openProgramPMPNode
	openProgramPMPNode = func(string) (programPMPNode, error) { return fake, nil }
	t.Cleanup(func() { openProgramPMPNode = original })

	var output bytes.Buffer
	if err := runContext(t.Context(), []string{
		"program", "fetch", "--json", "--workspace", filepath.Join(workspaceDir, programWorkspaceFile),
		"--min-context-slot", "90",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result programFetchOutput
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "Canonical program interface pinned" || result.Cluster != "devnet" ||
		fake.genesisRead != 1 || fake.accountRead != 1 {
		t.Fatalf("result = %+v, node = %+v", result, fake)
	}
}

func TestProgramFetchRefusesWorkspaceWithExplicitConnectionFlags(t *testing.T) {
	err := runContext(t.Context(), []string{
		"program", "fetch", "--workspace", "/private/workspace.json",
		"--program", programCommandAddress, "--min-context-slot", "1",
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("err = %v", err)
	}
}

func TestProgramFetchRefusesClusterMismatchBeforeAccountRead(t *testing.T) {
	fake := &fakeProgramPMPNode{genesis: solana.MainnetBetaGenesisHash}
	original := openProgramPMPNode
	openProgramPMPNode = func(string) (programPMPNode, error) { return fake, nil }
	t.Cleanup(func() { openProgramPMPNode = original })

	err := runContext(t.Context(), []string{
		"program", "fetch", "--registry", t.TempDir(), "--program", programCommandAddress,
		"--cluster", "devnet", "--node-rpc", "http://127.0.0.1:8899",
		"--min-context-slot", "90",
	}, &bytes.Buffer{})
	if err == nil || fake.genesisRead != 1 || fake.accountRead != 0 {
		t.Fatalf("err = %v, node = %+v", err, fake)
	}
}

func TestProgramFetchUsesPinnedAlpenglowGenesis(t *testing.T) {
	idl := []byte(`{"address":"11111111111111111111111111111111","metadata":{"name":"system","version":"1","spec":"0.1.0"},"instructions":[]}`)
	account := directPMPAccount(t, programCommandAddress, idl)
	fake := &fakeProgramPMPNode{
		genesis: programCommandAddress,
		account: solanarpc.AccountDataSlice{
			ContextSlot: 91, Owner: programinterface.ProgramMetadataProgram,
			DataLength: uint64(len(account)), Data: account,
		},
	}
	original := openProgramPMPNode
	openProgramPMPNode = func(string) (programPMPNode, error) { return fake, nil }
	t.Cleanup(func() { openProgramPMPNode = original })

	if err := runContext(t.Context(), []string{
		"program", "fetch", "--registry", t.TempDir(), "--program", programCommandAddress,
		"--cluster", "alpenglow", "--genesis-hash", programCommandAddress,
		"--node-rpc", "http://127.0.0.1:8899", "--min-context-slot", "90",
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if fake.genesisRead != 1 || fake.accountRead != 1 {
		t.Fatalf("node = %+v", fake)
	}
}

func TestProgramReadAccountDecodesThroughMithrilWithoutWallet(t *testing.T) {
	registry := t.TempDir()
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
	deployment, _ := testLegacyProgramDeployment(t, 91, programSimulationState)
	fake := &fakeProgramPMPNode{
		genesis:    solana.DevnetGenesisHash,
		deployment: &deployment,
		account: solanarpc.AccountDataSlice{
			ContextSlot: 91, Bankhash: programSimulationState, Owner: programCommandAddress,
			DataLength: uint64(len(data)), Data: data,
		},
	}
	original := openProgramPMPNode
	openProgramPMPNode = func(string) (programPMPNode, error) { return fake, nil }
	t.Cleanup(func() { openProgramPMPNode = original })

	var output bytes.Buffer
	if err := runContext(t.Context(), []string{
		"program", "read-account", "--json", "--registry", registry,
		"--program", programCommandAddress, "--sha256", pin.SHA256,
		"--account-type", "Counter", "--account", "SysvarC1ock11111111111111111111111111111111",
		"--cluster", "devnet", "--node-rpc", "http://127.0.0.1:8899",
		"--min-context-slot", "90",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result programReadOutput
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	value, ok := result.Value.(map[string]any)
	if !ok || value["value"] != "42" || result.ContextSlot != 91 ||
		result.Bankhash != programSimulationState ||
		result.Provenance != programProcessedProvenance || result.Finality != programProcessedFinality ||
		result.GenesisHash != solana.DevnetGenesisHash || result.DeploymentSHA256 == "" ||
		result.Status != "Mithril program account decoded" || fake.genesisRead != 1 || fake.accountRead != 2 {
		t.Fatalf("result = %+v, node = %+v", result, fake)
	}
	fake.account.Bankhash = programCommandAddress
	if err := runContext(t.Context(), []string{
		"program", "read-account", "--json", "--registry", registry,
		"--program", programCommandAddress, "--sha256", pin.SHA256,
		"--account-type", "Counter", "--account", "SysvarC1ock11111111111111111111111111111111",
		"--cluster", "devnet", "--node-rpc", "http://127.0.0.1:8899",
		"--min-context-slot", "90",
	}, &bytes.Buffer{}); err == nil {
		t.Fatal("program read accepted an invalid processed bank identity")
	}
	fake.account.Bankhash = programSimulationState
	mismatchedDeployment := *fake.deployment
	mismatchedDeployment.Bankhash = fakeProgramBankhash
	fake.deployment = &mismatchedDeployment
	err = runContext(t.Context(), []string{
		"program", "read-account", "--json", "--registry", registry,
		"--program", programCommandAddress, "--sha256", pin.SHA256,
		"--account-type", "Counter", "--account", "SysvarC1ock11111111111111111111111111111111",
		"--cluster", "devnet", "--node-rpc", "http://127.0.0.1:8899",
		"--min-context-slot", "90",
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not from the decoded processed bank") {
		t.Fatalf("deployment bank mismatch = %v", err)
	}
}

func directPMPAccount(t *testing.T, program string, idl []byte) []byte {
	t.Helper()
	data := make([]byte, 96+len(idl))
	data[0] = 2
	programKey, err := solana.Decode32(program)
	if err != nil {
		t.Fatal(err)
	}
	copy(data[1:33], programKey[:])
	data[65], data[66] = 1, 1
	copy(data[67:83], []byte("idl"))
	data[83], data[85] = 1, 1
	binary.LittleEndian.PutUint32(data[87:91], uint32(len(idl)))
	copy(data[96:], idl)
	return data
}
