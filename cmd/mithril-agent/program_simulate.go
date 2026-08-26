package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Overclock-Validator/mithril-agent/programinterface"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

type programSimulationNode interface {
	programClusterNode
	programDeploymentReader
	RootedFeedStatus(context.Context) (solanarpc.RootedFeedStatus, error)
	LatestBlockhash(context.Context, uint64) (solanarpc.LatestBlockhash, error)
	SimulateV0(context.Context, []byte, map[[32]byte][][32]byte, uint64) (solanarpc.LegacySimulation, error)
}

var openProgramSimulationNode = func(endpoint string) (programSimulationNode, error) {
	return solanarpc.NewMithrilNode(endpoint, nil)
}

type programAccountFlags []programinterface.Binding

func (values *programAccountFlags) String() string { return "" }

func (values *programAccountFlags) Set(value string) error {
	name, address, ok := strings.Cut(value, "=")
	if !ok || name == "" || address == "" {
		return errors.New("account must use NAME=ADDRESS")
	}
	*values = append(*values, programinterface.Binding{Name: name, Address: address})
	return nil
}

type programArgumentFlags []programinterface.ArgumentBinding

func (values *programArgumentFlags) String() string { return "" }

func (values *programArgumentFlags) Set(value string) error {
	name, encoded, ok := strings.Cut(value, "=")
	if !ok || name == "" || encoded == "" {
		return errors.New("argument must use NAME=JSON")
	}
	*values = append(*values, programinterface.ArgumentBinding{
		Name: name, Value: json.RawMessage(encoded),
	})
	return nil
}

type programSimulationOutput struct {
	Status                    string            `json:"status"`
	Cluster                   string            `json:"cluster"`
	GenesisHash               string            `json:"genesis_hash"`
	Provenance                string            `json:"provenance"`
	Finality                  string            `json:"finality"`
	Program                   string            `json:"program"`
	InterfaceSHA256           string            `json:"interface_sha256"`
	Instruction               string            `json:"instruction"`
	MessageSHA256             string            `json:"message_sha256"`
	UnsignedTransactionSHA256 string            `json:"unsigned_transaction_sha256"`
	ContextSlot               uint64            `json:"context_slot"`
	Bankhash                  string            `json:"bankhash"`
	DeploymentSHA256          string            `json:"deployment_sha256"`
	UnitsConsumed             uint64            `json:"units_consumed"`
	LogsSHA256                string            `json:"logs_sha256"`
	Review                    programCallReview `json:"review"`
	Walletless                bool              `json:"walletless"`
}

func runProgramSimulate(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("program simulate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspacePath := flags.String("workspace", "", "program workspace.json path")
	registry := flags.String("registry", "", "local program interface registry")
	program := flags.String("program", "", "expected Solana program address")
	interfaceHash := flags.String("sha256", "", "pinned interface SHA-256")
	instruction := flags.String("instruction", "", "exact instruction name")
	feePayer := flags.String("fee-payer", "", "public fee-payer address; no key is loaded")
	cluster := flags.String("cluster", "", "devnet, testnet, mainnet-beta, or alpenglow")
	genesisHash := flags.String("genesis-hash", "", "required exact genesis hash for alpenglow")
	nodeRPC := flags.String("node-rpc", "", "literal loopback Mithril RPC URL")
	minContextSlot := flags.Uint64("min-context-slot", 0, "minimum retained Mithril slot")
	jsonOutput := flags.Bool("json", false, "print JSON")
	var accounts programAccountFlags
	var arguments programArgumentFlags
	flags.Var(&accounts, "account", "pinned account binding NAME=ADDRESS; repeat in any order")
	flags.Var(&arguments, "arg", "pinned argument binding NAME=JSON; repeat as needed")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, programUsage)
			return writeErr
		}
		return err
	}
	if err := applyProgramWorkspace(*workspacePath, registry, program, cluster, genesisHash, nodeRPC); err != nil {
		return fmt.Errorf("program simulate: %w", err)
	}
	if flags.NArg() != 0 || *registry == "" || *program == "" ||
		*interfaceHash == "" || *instruction == "" || *feePayer == "" ||
		*cluster == "" || *nodeRPC == "" || *minContextSlot == 0 {
		return errors.New("program simulate requires the pinned interface, instruction, fee payer, cluster, loopback node RPC, and minimum context slot")
	}
	report, _, err := programinterface.Load(*registry, *program, *interfaceHash)
	if err != nil {
		return err
	}
	node, err := openProgramSimulationNode(*nodeRPC)
	if err != nil {
		return err
	}
	if err := verifyProgramNodeCluster(ctx, node, *cluster, *genesisHash); err != nil {
		return err
	}
	verifiedGenesis, err := expectedProgramGenesis(*cluster, *genesisHash)
	if err != nil {
		return err
	}
	blockhash, err := node.LatestBlockhash(ctx, *minContextSlot)
	if err != nil {
		return errors.New("read a fresh Mithril blockhash")
	}
	if blockhash.ContextSlot < *minContextSlot {
		return errors.New("Mithril blockhash context is older than the requested minimum")
	}
	call, err := programinterface.Build(
		report, *instruction, *feePayer, blockhash.Blockhash, accounts, arguments,
	)
	if err != nil {
		return err
	}
	review, err := reviewProgramCall(report, *instruction, *feePayer, accounts, arguments)
	if err != nil {
		return err
	}
	simulation, err := node.SimulateV0(ctx, call.Message, nil, *minContextSlot)
	if err != nil {
		return errors.New("Mithril program simulation failed")
	}
	if simulation.ContextSlot < *minContextSlot || !validProgramBankhash(simulation.Bankhash) ||
		!validProgramSHA256(simulation.LogsSHA256) {
		return errors.New("Mithril program simulation evidence is incomplete")
	}
	deployment, err := readProgramDeployment(ctx, node, report.Program, simulation.ContextSlot)
	if err != nil || deployment.ContextSlot != simulation.ContextSlot || deployment.Bankhash != simulation.Bankhash {
		return errors.New("Mithril program deployment evidence is not from the simulated processed bank")
	}
	messageHash := sha256.Sum256(call.Message)
	transactionHash := sha256.Sum256(call.Transaction)
	result := programSimulationOutput{
		Status: "simulated", Cluster: *cluster, GenesisHash: verifiedGenesis,
		Provenance: programProcessedProvenance,
		Finality:   programProcessedFinality, Program: report.Program,
		InterfaceSHA256: report.SHA256, Instruction: *instruction,
		MessageSHA256:             hex.EncodeToString(messageHash[:]),
		UnsignedTransactionSHA256: hex.EncodeToString(transactionHash[:]),
		ContextSlot:               simulation.ContextSlot, UnitsConsumed: simulation.UnitsConsumed,
		Bankhash: simulation.Bankhash, DeploymentSHA256: deployment.SHA256,
		LogsSHA256: simulation.LogsSHA256, Review: review, Walletless: true,
	}
	if *jsonOutput {
		return json.NewEncoder(output).Encode(result)
	}
	if _, err = fmt.Fprintf(output, `Program simulation succeeded
Cluster: %s
Genesis hash: %s
Simulation provenance: Mithril processed bank (not rooted)
Program: %s
Instruction: %s
Interface SHA-256: %s
Message SHA-256: %s
Unsigned transaction SHA-256: %s
Simulation slot: %d
Processed bank hash: %s
Deployment SHA-256: %s
Compute units: %d
Logs SHA-256: %s
		`, result.Cluster, result.GenesisHash, result.Program, result.Instruction, result.InterfaceSHA256,
		result.MessageSHA256, result.UnsignedTransactionSHA256, result.ContextSlot,
		result.Bankhash, result.DeploymentSHA256, result.UnitsConsumed, result.LogsSHA256); err != nil {
		return err
	}
	if err := writeProgramCallReview(output, result.Review); err != nil {
		return err
	}
	_, err = fmt.Fprint(output, "Walletless: no key loaded and no transaction submitted.\n")
	return err
}

func expectedProgramGenesis(cluster, pinned string) (string, error) {
	switch cluster {
	case "devnet":
		if pinned != "" {
			return "", errors.New("--genesis-hash is only valid for alpenglow")
		}
		return solana.DevnetGenesisHash, nil
	case "testnet":
		if pinned != "" {
			return "", errors.New("--genesis-hash is only valid for alpenglow")
		}
		return solana.TestnetGenesisHash, nil
	case "mainnet-beta":
		if pinned != "" {
			return "", errors.New("--genesis-hash is only valid for alpenglow")
		}
		return solana.MainnetBetaGenesisHash, nil
	case "alpenglow":
		if _, err := solana.Decode32(pinned); err != nil {
			return "", errors.New("alpenglow requires a canonical --genesis-hash")
		}
		return pinned, nil
	default:
		return "", errors.New("program cluster must be devnet, testnet, mainnet-beta, or alpenglow")
	}
}

func validProgramSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validProgramBankhash(value string) bool {
	decoded, err := solana.Decode32(value)
	return err == nil && decoded != ([32]byte{})
}
