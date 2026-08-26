package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// mcp config prints the exact client entry for connecting an MCP client to
// the agent's read-only status surface — the last hand-written JSON in the
// onboarding path.
//
// It prints ONLY the status-socket form. The --config form exposes the wider
// operator surface (journal, private paths, RPC environment), and emitting it
// as a fallback would silently hand every guided-setup user the wide surface
// because the socket only exists after the supervised install. A narrower
// default that sometimes says "not ready yet" beats a wider one that always
// works.
const mcpConfigUsage = `Usage: mithril-agent mcp config [--socket PATH]

Prints the MCP client configuration for the read-only status surface, ready
to paste into a client's mcpServers section. With no --socket it discovers the
generated sell, buy, and sweep status sockets. Requires the supervised status
socket units; see the README's install section if none exist yet.`

const defaultStatusSocket = "/run/mithril-agent-status.sock"

type mcpStatusSocket struct {
	Name string
	Path string
}

var supervisedMCPStatusSockets = []mcpStatusSocket{
	{Name: "mithril-agent-sell", Path: "/run/mithril-agent-status-sell.sock"},
	{Name: "mithril-agent-buy", Path: "/run/mithril-agent-status-buy.sock"},
	{Name: "mithril-agent-sweep", Path: "/run/mithril-agent-status-sweep.sock"},
	{Name: "mithril-agent", Path: defaultStatusSocket},
}

func runMCPConfig(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("mcp config", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socket := flags.String("socket", "", "one status socket path instead of auto-discovery")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, mcpConfigUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("mcp config takes no positional arguments")
	}
	sockets := supervisedMCPStatusSockets
	if *socket != "" {
		sockets = []mcpStatusSocket{{Name: "mithril-agent", Path: *socket}}
	}
	available := make([]mcpStatusSocket, 0, len(sockets))
	for _, candidate := range sockets {
		info, err := os.Stat(candidate.Path)
		if errors.Is(err, os.ErrNotExist) && *socket == "" {
			continue
		}
		if err != nil || info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("the status socket %s is not an active Unix socket", candidate.Path)
		}
		available = append(available, candidate)
	}
	if len(available) == 0 {
		return errors.New(
			"no supervised status sockets are active; start the generated status socket units first")
	}
	agentPath, err := resolvedAgentExecutable()
	if err != nil {
		return err
	}
	servers := make(map[string]any, len(available))
	for _, candidate := range available {
		servers[candidate.Name] = map[string]any{
			"command": agentPath,
			"args":    []string{"mcp", "--status-socket", candidate.Path},
		}
	}
	entry := map[string]any{"mcpServers": servers}
	encoded, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "%s\n\nRead-only: this surface cannot authorize, sign, or submit anything.\n", encoded)
	return err
}
