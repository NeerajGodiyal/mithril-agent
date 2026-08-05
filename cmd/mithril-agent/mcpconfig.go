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
to paste into a client's mcpServers section. Requires the supervised status
socket; see the README's install section if it does not exist yet.`

const defaultStatusSocket = "/run/mithril-agent-status.sock"

func runMCPConfig(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("mcp config", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socket := flags.String("socket", defaultStatusSocket, "status socket path")
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
	if _, err := os.Stat(*socket); err != nil {
		return fmt.Errorf(
			"the status socket %s does not exist; start the supervised service first "+
				"(sudo systemctl start mithril-agent-status.socket) — this command "+
				"deliberately prints only the read-only surface", *socket)
	}
	agentPath, err := resolvedAgentExecutable()
	if err != nil {
		return err
	}
	entry := map[string]any{
		"mcpServers": map[string]any{
			"mithril-agent": map[string]any{
				"command": agentPath,
				"args":    []string{"mcp", "--status-socket", *socket},
			},
		},
	}
	encoded, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "%s\n\nRead-only: this surface cannot authorize, sign, or submit anything.\n", encoded)
	return err
}
