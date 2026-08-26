package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Overclock-Validator/mithril-agent/programinterface"
)

type programReadOutput struct {
	Status           string `json:"status"`
	Cluster          string `json:"cluster"`
	GenesisHash      string `json:"genesis_hash"`
	Provenance       string `json:"provenance"`
	Finality         string `json:"finality"`
	Account          string `json:"account"`
	ContextSlot      uint64 `json:"context_slot"`
	Bankhash         string `json:"bankhash"`
	DeploymentSHA256 string `json:"deployment_sha256"`
	programinterface.DecodedData
}

func runProgramReadAccount(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("program read-account", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspacePath := flags.String("workspace", "", "program workspace.json path")
	registry := flags.String("registry", "", "local program interface registry")
	program := flags.String("program", "", "expected Solana program address")
	sha256 := flags.String("sha256", "", "pinned interface SHA-256")
	accountType := flags.String("account-type", "", "exact pinned account type name")
	account := flags.String("account", "", "account address to read")
	cluster := flags.String("cluster", "", "devnet, testnet, mainnet-beta, or alpenglow")
	genesisHash := flags.String("genesis-hash", "", "required exact genesis hash for alpenglow")
	nodeRPC := flags.String("node-rpc", "", "literal loopback Mithril RPC URL")
	minContextSlot := flags.Uint64("min-context-slot", 0, "minimum retained Mithril slot")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, programUsage)
			return writeErr
		}
		return err
	}
	if err := applyProgramWorkspace(*workspacePath, registry, program, cluster, genesisHash, nodeRPC); err != nil {
		return fmt.Errorf("program read-account: %w", err)
	}
	if flags.NArg() != 0 || *registry == "" || *program == "" || *sha256 == "" ||
		*accountType == "" || *account == "" || *cluster == "" || *nodeRPC == "" ||
		*minContextSlot == 0 {
		return errors.New("program read-account requires the pinned interface, account, account type, cluster, loopback node RPC, and minimum context slot")
	}
	report, _, err := programinterface.Load(*registry, *program, *sha256)
	if err != nil {
		return err
	}
	node, err := openProgramPMPNode(*nodeRPC)
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
	accountData, err := node.AccountData(
		ctx, *account, *minContextSlot, programinterface.MaxAccountDataBytes,
	)
	if err != nil {
		return errors.New("read account through Mithril")
	}
	if accountData.ContextSlot < *minContextSlot || !validProgramBankhash(accountData.Bankhash) ||
		accountData.Owner != report.Program ||
		accountData.Executable || accountData.DataLength != uint64(len(accountData.Data)) {
		return errors.New("account owner, context, or data length does not match the pinned program")
	}
	deployment, err := readProgramDeployment(ctx, node, report.Program, accountData.ContextSlot)
	if err != nil || deployment.ContextSlot != accountData.ContextSlot || deployment.Bankhash != accountData.Bankhash {
		return errors.New("program deployment evidence is not from the decoded processed bank")
	}
	decoded, err := programinterface.DecodeAccount(report, *accountType, accountData.Data)
	if err != nil {
		return err
	}
	view := programReadOutput{
		Status: "Mithril program account decoded", Cluster: *cluster, GenesisHash: verifiedGenesis,
		Provenance: programProcessedProvenance, Finality: programProcessedFinality,
		Account: *account, ContextSlot: accountData.ContextSlot, Bankhash: accountData.Bankhash,
		DeploymentSHA256: deployment.SHA256,
		DecodedData:      decoded,
	}
	if *jsonOutput {
		return json.NewEncoder(output).Encode(view)
	}
	if _, err := fmt.Fprintf(output,
		"%s\nCluster: %s\nGenesis hash: %s\nRead provenance: Mithril processed bank (not rooted)\nProgram: %s\nAccount: %s\nRead slot: %d\nProcessed bank hash: %s\nDeployment SHA-256: %s\nAccount type: %s\nData SHA-256: %s\nValue:\n",
		view.Status, view.Cluster, view.GenesisHash, report.Program, view.Account, view.ContextSlot, view.Bankhash,
		view.DeploymentSHA256,
		view.Name, view.SHA256); err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(view.Value)
}
