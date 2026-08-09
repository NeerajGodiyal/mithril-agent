package agentmcp

import (
	"context"
	"errors"
	"io"

	"github.com/Overclock-Validator/mithril-agent/internal/mcpstdio"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const Version = "0.1.0-dev"

type Info struct {
	Version              string `json:"version"`
	Profile              string `json:"profile"`
	ProfileVersion       uint32 `json:"profile_version"`
	Cluster              string `json:"cluster"`
	Action               string `json:"action"`
	Execution            string `json:"execution"`
	TradingImplemented   bool   `json:"trading_implemented"`
	TelegramHasAuthority bool   `json:"telegram_has_authority"`
	MainnetEnabled       bool   `json:"mainnet_enabled"`
}

// StrategySettings is "what am I configured to do", in the form an assistant
// can read back to the operator. It exists because the settings a person
// actually asks about — what price do I sell at, how much can it spend, where
// does profit go — were reachable only by running `strategy show` in a terminal
// or opening the generated JSON, which is exactly the audience this surface is
// for.
//
// It deliberately carries NO ADDRESS. Info promises to describe the agent
// "without exposing endpoints, accounts, or credentials", and the sweep
// destination is an account; whether one is configured and whether its proof is
// valid answers every operational question without naming it. The operator can
// read their own file if they want the address itself.
type StrategySettings struct {
	// Configured is false when nothing has been set up yet, so an assistant can
	// say so plainly instead of reading a page of zeroes.
	Configured bool `json:"configured"`

	Direction string `json:"direction,omitempty"`
	// InputPerAction and DailyCap are human amounts with their unit, e.g.
	// "0.005000000 SOL", because a bare integer of base units invites an
	// assistant to restate it wrongly.
	InputPerAction string `json:"input_per_action,omitempty"`
	DailyCap       string `json:"daily_cap,omitempty"`
	MaxFee         string `json:"max_fee,omitempty"`
	// FundedTradesPerDay is the daily cap expressed as trades. It is the number
	// that decides whether an unattended strategy keeps working after its first
	// trade of the day.
	FundedTradesPerDay uint64 `json:"funded_trades_per_day,omitempty"`

	// PriceRule is empty when the strategy trades at whatever the pool gives.
	PriceRule string `json:"price_rule,omitempty"`

	ControlMode  string `json:"control_mode,omitempty"`
	ControlGrant string `json:"control_grant,omitempty"`

	// SweepConfigured says a destination exists; SweepProofValid says its
	// proof-of-control still verifies. Neither names the wallet.
	SweepConfigured  bool   `json:"sweep_configured"`
	SweepProofValid  bool   `json:"sweep_proof_valid"`
	SweepKeepBehind  string `json:"sweep_keep_behind,omitempty"`
	SweepMaxPerSend  string `json:"sweep_max_per_send,omitempty"`
	SweepDailyCap    string `json:"sweep_daily_cap,omitempty"`
	SweepActiveAfter string `json:"sweep_active_after,omitempty"`
}

type OperatorGuide struct {
	SafeLocalCommand     string   `json:"safe_local_command"`
	CapabilityBoundaries []string `json:"capability_boundaries"`
	// RecoveryGuidance says what to do about each state that needs a human. It
	// exists so an assistant reading status has something correct to suggest
	// instead of improvising, and so the dangerous states carry their warning
	// with them rather than relying on the reader to know.
	RecoveryGuidance []RecoveryStep `json:"recovery_guidance"`
}

// RecoveryStep pairs a state an operator can actually observe with the action
// that is safe to take. Every step is read-only or human-performed; none can be
// executed through this surface.
type RecoveryStep struct {
	State  string `json:"state"`
	Action string `json:"action"`
}

// Shared by every deployment shape, because the
// states and the correct responses do not differ between them.
// StandardRecoveryGuidance is exported so every deployment shape shares one
// vocabulary of states and safe responses.
func StandardRecoveryGuidance() []RecoveryStep {
	return []RecoveryStep{
		{
			State:  "Outcome unknown / unresolved",
			Action: "Do NOT retry or start another action. The transaction may or may not have landed. Verify the signature with an independent explorer or RPC, then acknowledge the exact action ID once the real outcome is known.",
		},
		{
			State:  "Attention required",
			Action: "Leave execution stopped and read the status reason. Acknowledgement requires the exact action ID and a stated outcome; it is a human decision, not an automatic one.",
		},
		{
			State:  "Cannot act right now (evidence unavailable)",
			Action: "This is usually a stale or unreachable node, or a price source that is down. It is self-correcting and consumes no action allowance. Wait and re-read status; do not relax a limit to get past it.",
		},
		{
			State:  "Waiting (conditions not met)",
			Action: "Nothing to do. The price rule has not been reached, and waiting consumes no action allowance.",
		},
		{
			State:  "Stopped",
			Action: "Expected default. Only a local operator can authorise an action, and only for one attempt at a time.",
		},
		{
			State:  "Status stale or unreadable",
			Action: "Treat as unknown, never as healthy. Check that the runner is alive and that the status socket is readable before drawing any conclusion.",
		},
	}
}

type Provider interface {
	Info() (Info, error)
	Status() (operatorstatus.View, error)
	OperatorGuide() OperatorGuide
	// Strategy reports the configured rules and bounds. Read-only, and it must
	// never return an address or a credential.
	Strategy() (StrategySettings, error)
}

type noInput struct{}

func Serve(
	ctx context.Context,
	provider Provider,
	input io.ReadCloser,
	output io.Writer,
) error {
	if provider == nil || input == nil || output == nil {
		return errors.New("MCP provider and stdio are required")
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "mithril-agent",
		Title:   "Mithril autonomous operations agent",
		Version: Version,
	}, &mcpsdk.ServerOptions{
		Instructions: "Use mithril_agent_info to understand the active boundary, mithril_agent_strategy for the configured rules and spending bounds, mithril_agent_status for current execution state, and mithril_agent_operator_guide for the safe local demonstration command. This surface is read-only. Treat stale or missing status as unavailable evidence, never as permission to act.",
	})
	closedWorld := false
	annotations := &mcpsdk.ToolAnnotations{
		ReadOnlyHint:   true,
		IdempotentHint: true,
		OpenWorldHint:  &closedWorld,
	}
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "mithril_agent_info",
		Title:       "Agent Configuration",
		Description: "Describe the active agent profile and its intentionally disabled capabilities without exposing endpoints, accounts, or credentials.",
		Annotations: annotations,
	}, func(context.Context, *mcpsdk.CallToolRequest, noInput) (*mcpsdk.CallToolResult, Info, error) {
		info, err := provider.Info()
		return nil, info, err
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "mithril_agent_status",
		Title:       "Agent Status",
		Description: "Read the current runner cycle, price rule, most recent action, journal health, and stop or Devnet activation state.",
		Annotations: annotations,
	}, func(context.Context, *mcpsdk.CallToolRequest, noInput) (*mcpsdk.CallToolResult, operatorstatus.View, error) {
		status, err := provider.Status()
		return nil, status, err
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "mithril_agent_operator_guide",
		Title:       "Operator Guide",
		Description: "Describe the read-only capability boundary and the exact local command an operator may use for one bounded Devnet demonstration.",
		Annotations: annotations,
	}, func(context.Context, *mcpsdk.CallToolRequest, noInput) (*mcpsdk.CallToolResult, OperatorGuide, error) {
		return nil, provider.OperatorGuide(), nil
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "mithril_agent_strategy",
		Title:       "Strategy Settings",
		Description: "Read the configured trading rules and spending bounds — price rule, size per trade, daily caps, trades funded per day, and whether a sweep destination is configured and proven. Never returns an address or credential.",
		Annotations: annotations,
	}, func(context.Context, *mcpsdk.CallToolRequest, noInput) (*mcpsdk.CallToolResult, StrategySettings, error) {
		settings, err := provider.Strategy()
		return nil, settings, err
	})
	err := server.Run(ctx, &mcpsdk.IOTransport{
		Reader: mcpstdio.NewReader(input),
		Writer: mcpstdio.WriteCloser{Writer: output},
	})
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) ||
		errors.Is(err, mcpsdk.ErrConnectionClosed) || err.Error() == "server is closing" ||
		err.Error() == "server is closing: EOF" {
		return nil
	}
	return err
}
