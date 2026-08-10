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
		"94718096a9d8ab02e38725a94a253ff105c0ed89",
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
	for _, want := range []string{
		"[QUICKSTART.md](QUICKSTART.md)",
		"[DEMO.md](DEMO.md)",
		"[OPERATIONS.md](OPERATIONS.md)",
		"Give this project to another AI assistant",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md is missing entry-point fact %q", want)
		}
	}
	if lines := strings.Count(readme, "\n") + 1; lines < 250 || lines > 500 {
		t.Errorf("README.md has %d lines; want a complete 250..500-line orientation", lines)
	}
	for name, contents := range map[string]string{"QUICKSTART.md": quick, "DEMO.md": demo} {
		if !strings.Contains(contents, "OPERATIONS.md") {
			t.Errorf("%s does not route detailed failures to OPERATIONS.md", name)
		}
	}
	operations := readDocumentation(t, "../../OPERATIONS.md")
	for _, want := range []string{"# Mithril Agent operations and reference", "[README.md](README.md)", "[QUICKSTART.md](QUICKSTART.md)"} {
		if !strings.Contains(operations, want) {
			t.Errorf("OPERATIONS.md is missing reference fact %q", want)
		}
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
