package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Overclock-Validator/mithril-agent/programinterface"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

type programClusterNode interface {
	GenesisHash(context.Context) (string, error)
	VerificationStatus(context.Context) (solanarpc.VerificationStatus, error)
}

type programPMPNode interface {
	programinterface.PMPReader
	programClusterNode
}

var openProgramPMPNode = func(endpoint string) (programPMPNode, error) {
	return solanarpc.NewMithrilNode(endpoint, nil)
}

type programFetchOutput struct {
	Status     string `json:"status"`
	Cluster    string `json:"cluster"`
	Provenance string `json:"provenance"`
	Finality   string `json:"finality"`
	programinterface.PMPResult
}

func runProgramFetch(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("program fetch", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspacePath := flags.String("workspace", "", "program workspace.json path")
	registry := flags.String("registry", "", "local program interface registry")
	program := flags.String("program", "", "expected Solana program address")
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
		return fmt.Errorf("program fetch: %w", err)
	}
	if flags.NArg() != 0 || *registry == "" || *program == "" || *cluster == "" ||
		*nodeRPC == "" || *minContextSlot == 0 {
		return errors.New("program fetch requires the program, registry, cluster, loopback node RPC, and minimum context slot")
	}
	node, err := openProgramPMPNode(*nodeRPC)
	if err != nil {
		return err
	}
	if err := verifyProgramNodeCluster(ctx, node, *cluster, *genesisHash); err != nil {
		return err
	}
	result, err := programinterface.FetchAndPinPMP(
		ctx, node, *registry, *program, *minContextSlot,
	)
	if err != nil {
		return err
	}
	status := "Canonical program interface already pinned"
	if result.Created {
		status = "Canonical program interface pinned"
	}
	view := programFetchOutput{
		Status: status, Cluster: *cluster, Provenance: programProcessedProvenance,
		Finality: programProcessedFinality, PMPResult: result,
	}
	if *jsonOutput {
		return json.NewEncoder(output).Encode(view)
	}
	contentSource := ""
	if view.ContentAccount != "" {
		contentSource = fmt.Sprintf(
			"Content account: %s\nContent read slot: %d\n",
			view.ContentAccount, view.ContentContextSlot,
		)
	}
	_, err = fmt.Fprintf(output, `%s
Cluster: %s
Read provenance: Mithril processed bank (not rooted)
Program: %s
Metadata account: %s
Metadata read slot: %d
Processed bank hash: %s
%sSHA-256: %s
Pin: %q
Walletless: no key loaded and no transaction submitted.
`, view.Status, view.Cluster, view.Program, view.MetadataAccount, view.ContextSlot, view.Bankhash,
		contentSource, view.SHA256, view.Path)
	return err
}

func verifyProgramNodeCluster(ctx context.Context, node programClusterNode, cluster, genesisHash string) error {
	expectedGenesis, err := expectedProgramGenesis(cluster, genesisHash)
	if err != nil {
		return err
	}
	status, err := node.VerificationStatus(ctx)
	if err != nil {
		return errors.New("loopback endpoint is not a compatible Mithril node; confirm Mithril RPC is running and ready, then retry")
	}
	if !status.EvidenceServed {
		return errors.New("Mithril node is not serving verified evidence; keep actions disabled and inspect node verification health")
	}
	genesis, err := node.GenesisHash(ctx)
	if err != nil {
		return errors.New("Mithril node did not return its cluster identity; confirm the loopback node is running and ready, then retry")
	}
	if genesis != expectedGenesis {
		return errors.New("Mithril node cluster does not match the configured cluster")
	}
	return nil
}
