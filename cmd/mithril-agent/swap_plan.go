package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
)

const swapPlanVersion = 1

type swapPlan struct {
	Version int            `json:"version"`
	Status  string         `json:"status"`
	Profile string         `json:"profile"`
	Cluster string         `json:"cluster"`
	Steps   []swapPlanStep `json:"steps"`
}

type swapPlanStep struct {
	ID     string   `json:"id"`
	State  string   `json:"state"`
	Argv   []string `json:"argv,omitempty"`
	Action string   `json:"action,omitempty"`
}

func runSwapPlan(args []string, output io.Writer) error {
	configPath, err := swapConfigPath("plan", args, output)
	if err != nil || configPath == "" {
		return err
	}
	cfg, err := readSwapConfig(configPath)
	if err != nil {
		return err
	}
	if _, err := openBoundRPCProviders(
		cfg,
		os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL"),
		os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL"),
		os.Getenv("MITHRIL_AGENT_SECONDARY_RPC_URL"),
	); err != nil {
		return err
	}
	if cfg.Quote.SocketPath == "" && os.Getenv("MITHRIL_AGENT_QUOTE_RPC_URL") == "" {
		return errors.New("Orca quote RPC URL is required for direct quote mode")
	}
	agentCommand, err := resolvedAgentExecutable()
	if err != nil {
		return err
	}
	plan := newSwapPlan(agentCommand, configPath, cfg)
	return json.NewEncoder(output).Encode(plan)
}

func newSwapPlan(agentCommand, configPath string, cfg config) swapPlan {
	command := func(parts ...string) []string {
		return append([]string{agentCommand}, parts...)
	}
	return swapPlan{
		Version: swapPlanVersion,
		Status:  "configured",
		Profile: cfg.Swap.Name,
		Cluster: cfg.Swap.Cluster,
		Steps: []swapPlanStep{
			{ID: "offline_preflight", State: "command", Argv: command("preflight", "--config", configPath)},
			{ID: "start_quote_service", State: "external_check_required", Action: "Start the configured quote service without enabling execution."},
			{ID: "live_read_only_check", State: "command", Argv: command("swap", "check", "--config", configPath)},
			{ID: "start_runner_stopped", State: "external_check_required", Action: "Start the runner; it remains stopped by default."},
			{ID: "verify_status", State: "command", Argv: command("swap", "status", "--config", configPath)},
			{ID: "verify_alert_delivery", State: "external_check_required", Action: "Verify the alert receiver and independent deadman before enabling execution."},
			{ID: "enable_one_action", State: "external_check_required", Action: "Enable one bounded action for a short operator-approved window."},
			{ID: "watch_confirmation", State: "command", Argv: command("swap", "status", "--config", configPath)},
			{ID: "stop_and_review", State: "external_check_required", Action: "Stop execution and review the final status and journal."},
		},
	}
}
