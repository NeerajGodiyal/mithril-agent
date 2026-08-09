package main

import (
	"os"
	"strings"
	"testing"
)

// These names are an operator-facing interface. Pinning the small set that
// must agree keeps the quick start, review guide, and generated services from
// drifting back into three different deployments.
func TestFullStrategyDocumentationMatchesGeneratedLayout(t *testing.T) {
	quick := readDocumentation(t, "../../QUICKSTART.md")
	for _, want := range []string{
		"mithril-agent-run.service",
		"mithril-agent-alerts.service",
		"/run/mithril-agent-status-sell.sock",
		"/run/mithril-agent-status-buy.sock",
		"/run/mithril-agent-status-sweep.sock",
		"127.0.0.1:9310",
		"127.0.0.1:9311",
		"127.0.0.1:9312",
		"sudo -u mithril-agent env HOME=/var/lib/mithril-agent",
		"EnvironmentFile=/etc/mithril-agent/telegram-operator.env",
		"--activation-delay 0s",
		"/usr/local/bin/mithril-agent check",
		"replacing `sell` with `buy`",
		`"status":"ready"`,
	} {
		if !strings.Contains(quick, want) {
			t.Errorf("QUICKSTART.md is missing generated-layout fact %q", want)
		}
	}
	for _, stale := range []string{
		"./bin/*",
		"docs/mcp-operator.md",
		"MITHRIL_BLOCK_SOURCE=turbine",
	} {
		if strings.Contains(quick, stale) {
			t.Errorf("QUICKSTART.md contains stale first-run instruction %q", stale)
		}
	}

	demo := readDocumentation(t, "../../DEMO.md")
	for _, want := range []string{
		"legacy single-leg only",
		"mithril-agent-run.service",
		"/run/mithril-agent-status-sell.sock",
		"/var/lib/mithril-agent/.mithril-agent/strategy-data/$leg/state/events.jsonl",
		"/usr/local/libexec/mithril-agent/mithril-agent check",
	} {
		if !strings.Contains(demo, want) {
			t.Errorf("DEMO.md is missing full-strategy review fact %q", want)
		}
	}

	readme := readDocumentation(t, "../../README.md")
	if !strings.Contains(readme, "[QUICKSTART.md](QUICKSTART.md)") {
		t.Error("README.md does not send a new operator to the supported quick start")
	}

	makefile := readDocumentation(t, "../../Makefile")
	for _, want := range []string{"MITHRIL_AGENT_QUOTE_RPC_URL", "RPC endpoints all four set"} {
		if !strings.Contains(makefile, want) {
			t.Errorf("Makefile prerequisite check is missing %q", want)
		}
	}
}

func readDocumentation(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
