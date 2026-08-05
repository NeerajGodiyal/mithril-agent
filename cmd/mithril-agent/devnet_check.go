package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/mcpobserve"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const devnetCheckTimeout = 30 * time.Second

var errDevnetCheckFailed = errors.New("devnet check failed")

type devnetCheckSummary struct {
	Status      string             `json:"status"`
	Checks      devnetCheckResults `json:"checks"`
	NextCommand string             `json:"next_command,omitempty"`
}

type devnetCheckResults struct {
	Preflight        string `json:"preflight"`
	MithrilMCP       string `json:"mithril_mcp"`
	MithrilHealth    string `json:"mithril_health"`
	ProviderGenesis  string `json:"provider_genesis"`
	ProviderAccounts string `json:"provider_accounts"`
}

type devnetCheckDependencies struct {
	observer  devnetCheckObserver
	lifecycle devnetCheckLifecycle
}

type devnetCheckObserver interface {
	Observe(context.Context, string) (agent.NodeObservation, error)
}

type devnetCheckLifecycle interface {
	VerifyGenesis(context.Context, string) error
	AccountsForTransfer(
		context.Context,
		string,
		string,
		uint64,
	) (txflow.TransferAccountEvidence, error)
}

var (
	openDevnetCheckDependencies = newDevnetCheckDependencies
	devnetPreflight             = checkPreflight
)

func runDevnetCheck(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("devnet-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := io.WriteString(
				output,
				"Usage: mithril-agent devnet-check --config PATH\n",
			)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" {
		return errors.New("devnet-check requires --config")
	}

	checkCtx, cancel := context.WithTimeout(ctx, devnetCheckTimeout)
	defer cancel()
	summary := checkDevnetReadiness(checkCtx, *configPath)
	if err := json.NewEncoder(output).Encode(summary); err != nil {
		return err
	}
	if summary.Status != preflightOK {
		return errDevnetCheckFailed
	}
	return nil
}

func checkDevnetReadiness(ctx context.Context, configPath string) devnetCheckSummary {
	summary := devnetCheckSummary{
		Status: preflightFailed,
		Checks: devnetCheckResults{
			Preflight:        preflightFailed,
			MithrilMCP:       preflightSkipped,
			MithrilHealth:    preflightSkipped,
			ProviderGenesis:  preflightSkipped,
			ProviderAccounts: preflightSkipped,
		},
	}
	cfg, err := readConfig(configPath)
	if err != nil {
		return summary
	}
	if cfg.Swap != nil {
		summary.Checks.Preflight = preflightSkipped
		summary.NextCommand = "mithril-agent swap check --config PATH"
		return summary
	}
	if devnetPreflight(configPath).Status != preflightOK {
		return summary
	}
	summary.Checks.Preflight = preflightOK
	dependencies, err := openDevnetCheckDependencies(cfg)
	if err != nil {
		return summary
	}

	summary.Checks.MithrilMCP = preflightFailed
	observation, err := dependencies.observer.Observe(ctx, cfg.Profile.Source)
	if err != nil {
		return summary
	}
	summary.Checks.MithrilMCP = preflightOK
	if healthyForDevnetCheck(observation.Health) {
		summary.Checks.MithrilHealth = preflightOK
	} else {
		summary.Checks.MithrilHealth = preflightFailed
	}

	summary.Checks.ProviderGenesis = preflightFailed
	if err := dependencies.lifecycle.VerifyGenesis(ctx, solana.DevnetGenesisHash); err != nil {
		return summary
	}
	summary.Checks.ProviderGenesis = preflightOK
	summary.Checks.ProviderAccounts = preflightFailed
	if _, err := dependencies.lifecycle.AccountsForTransfer(
		ctx,
		cfg.Profile.Source,
		cfg.Profile.Destination,
		observation.Account.Slot,
	); err != nil {
		return summary
	}
	summary.Checks.ProviderAccounts = preflightOK
	if allDevnetChecksOK(summary.Checks) {
		summary.Status = preflightOK
	}
	return summary
}

func newDevnetCheckDependencies(cfg config) (devnetCheckDependencies, error) {
	mithril, err := solanarpc.NewMithrilNode(
		os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL"),
		nil,
	)
	if err != nil {
		return devnetCheckDependencies{}, err
	}
	primary, err := newExternalRPC(os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL"))
	if err != nil {
		return devnetCheckDependencies{}, err
	}
	secondary, err := newExternalRPC(os.Getenv("MITHRIL_AGENT_SECONDARY_RPC_URL"))
	if err != nil {
		return devnetCheckDependencies{}, err
	}
	lifecycle, err := txflow.New(mithril, primary, secondary)
	if err != nil {
		return devnetCheckDependencies{}, err
	}
	observer, err := mcpobserve.New(mcpobserve.Config{
		Command: cfg.MCP.Command,
		Args:    cfg.MCP.Args,
		Env: mithrilMCPEnvironment(
			os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL"),
			os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL"),
		),
		Cluster:   cfg.Profile.Cluster,
		RPCOrigin: mithril.Origin(),
	}, nil)
	if err != nil {
		return devnetCheckDependencies{}, err
	}
	return devnetCheckDependencies{observer: observer, lifecycle: lifecycle}, nil
}

func healthyForDevnetCheck(health agent.NodeHealth) bool {
	return health.Status == "healthy" &&
		health.AssessmentScope == "point_in_time_snapshot" &&
		!health.SafeForAutomation &&
		health.EvidenceComplete &&
		health.DivergenceArtifacts == 0
}

func allDevnetChecksOK(checks devnetCheckResults) bool {
	return checks.Preflight == preflightOK &&
		checks.MithrilMCP == preflightOK &&
		checks.MithrilHealth == preflightOK &&
		checks.ProviderGenesis == preflightOK &&
		checks.ProviderAccounts == preflightOK
}
