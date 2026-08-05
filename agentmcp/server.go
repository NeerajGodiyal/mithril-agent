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
		Instructions: "Use mithril_agent_info to understand the active boundary, mithril_agent_status for current execution state, and mithril_agent_operator_guide for the safe local demonstration command. This surface is read-only. Treat stale or missing status as unavailable evidence, never as permission to act.",
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
