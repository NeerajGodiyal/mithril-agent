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
	sysusers := readDocumentation(t, "../../deploy/sysusers/mithril-agent-status.conf")
	for _, account := range []string{
		"mithril-agent-policy", "mithril-agent-signer", "mithril-agent-submitter",
	} {
		if !strings.Contains(sysusers, "g "+account+" -") ||
			!strings.Contains(sysusers, "u "+account+" -:"+account+" ") {
			t.Errorf("sysusers configuration does not create isolated account %q", account)
		}
	}

	quick := readDocumentation(t, "../../QUICKSTART.md")
	for _, want := range []string{
		"[ROADMAP.md](ROADMAP.md)",
		"make prereqs-trading",
		"Do not use the old `koro/agent-node-integration-wip` branch",
		"mithril-agent-run.service",
		"mithril-agent-alerts.service",
		"mithril-agent-submitter-sell.socket",
		"mithril-agent-submitter-operator-sell.socket",
		"mithril-agent-recovery-sell.timer",
		"systemd-analyze verify",
		"If Telegram was enabled",
		"/run/mithril-agent-status-sell.sock",
		"/run/mithril-agent-status-buy.sock",
		"/run/mithril-agent-status-sweep.sock",
		"127.0.0.1:9310",
		"127.0.0.1:9311",
		"127.0.0.1:9312",
		"sudo -u mithril-agent env HOME=/var/lib/mithril-agent",
		"[node-state filesystem access](OPERATIONS.md#node-state-filesystem-access)",
		"EnvironmentFile=/etc/mithril-agent/telegram-operator.env",
		"--activation-delay 0s",
		"/usr/local/bin/mithril-agent check",
		"replacing `sell` with `buy`",
		`"status":"ready"`,
		"mithril-agent audit snapshot",
		"keyless recovery timer",
		"https://api.devnet.solana.com",
		"https://solana-devnet.api.onfinality.io/public",
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
		"mithril-agent-submitter-sell.socket",
		"mithril-agent-submitter-operator-sell.socket",
		"mithril-agent-recovery-sell.timer",
		"mithril-agent-status-sell.socket",
		"/run/mithril-agent-status-sell.sock",
		"/var/lib/mithril-agent/.mithril-agent/strategy-data/$leg/config.json",
		"/usr/local/libexec/mithril-agent/mithril-agent check",
		"--setenv=HOME=/var/lib/mithril-agent",
		"mithril-agent audit snapshot",
	} {
		if !strings.Contains(demo, want) {
			t.Errorf("DEMO.md is missing full-strategy review fact %q", want)
		}
	}
	if strings.Contains(strings.ToLower(demo), "disposable wallet") ||
		strings.Contains(strings.ToLower(demo), "limits are disposable") {
		t.Error("DEMO.md contradicts the dedicated limited-risk wallet model")
	}

	readme := readDocumentation(t, "../../README.md")
	for _, want := range []string{
		"[ROADMAP.md](ROADMAP.md)",
		"[WALLETLESS_QUICKSTART.md](WALLETLESS_QUICKSTART.md)",
		"[QUICKSTART.md](QUICKSTART.md)",
		"[DEMO.md](DEMO.md)",
		"[OPERATIONS.md](OPERATIONS.md)",
		"Give this project to another AI assistant",
		"--output /var/lib/mithril-agent/.mithril-agent/mithril-agent-run.service",
		"make test-account-free",
		"make test-free-rehearsal",
		"make test-free-custody",
		"mithril-agent proposal canary-check",
		"proposal approval-create",
		"mithril-agent proposal turnkey-check",
		"temporary, unfunded test identities",
		"No operator wallet or custody-provider or messaging account is required",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md is missing entry-point fact %q", want)
		}
	}
	if lines := strings.Count(readme, "\n") + 1; lines < 250 || lines > 500 {
		t.Errorf("README.md has %d lines; want a complete 250..500-line orientation", lines)
	}
	walletless := readDocumentation(t, "../../WALLETLESS_QUICKSTART.md")
	for _, want := range []string{
		"rooted_events = true",
		"set -o pipefail",
		"mithril-agent program fetch",
		"--review '/absolute/path/to/repository-review.json'",
		`"summary": "A concise reviewed conclusion`,
		"Only the bounded reviewer-written `summary` is exposed",
		"mithril --config \"$NODE_CONFIG\" events --framed",
		"--accounts \"$ACCOUNTS_ROOT\" --owner \"$PROGRAM\"",
		"--accounts \"$ACCOUNTS_ROOT\" --mention \"$PROGRAM\"",
		"mithril-agent program decode-account",
		"mithril-agent program decode-event",
		"mithril-agent program simulate",
		"source-bound to this workspace and AccountsDB lineage",
		"no browser wallet, signing key, or block explorer",
		"valid shreds and Alpenglow certificates",
		"sha256sum *.json >SHA256SUMS",
		"make test-walletless",
		"make test-rooted-contract MITHRIL_SOURCE=/absolute/path/to/Mithril",
		"install -d -m 0755 bin",
		"(umask 022 && go build",
		"executable is not trusted",
		"operator-supplied source bundle",
		"Do not clone the older",
	} {
		if !strings.Contains(walletless, want) {
			t.Errorf("WALLETLESS_QUICKSTART.md is missing first-run fact %q", want)
		}
	}
	if strings.Contains(walletless, "make prereqs-trading") {
		t.Error("walletless quick start requires optional trading prerequisites")
	}
	for name, contents := range map[string]string{"QUICKSTART.md": quick, "DEMO.md": demo} {
		if !strings.Contains(contents, "OPERATIONS.md") {
			t.Errorf("%s does not route detailed failures to OPERATIONS.md", name)
		}
	}
	for _, want := range []string{
		"Telegram is optional",
		"service install` will not generate or",
		"Choose **yes** for Telegram only if its delivery test passed",
	} {
		if !strings.Contains(quick, want) {
			t.Errorf("QUICKSTART.md is missing account-free Telegram guidance %q", want)
		}
	}
	operations := readDocumentation(t, "../../OPERATIONS.md")
	for _, want := range []string{"# Mithril Agent operations and reference", "[README.md](README.md)", "[QUICKSTART.md](QUICKSTART.md)", "### Node-state filesystem access", "mithril-agent audit snapshot", "keyless systemd timer", "make test-account-free", "make test-free-rehearsal", "make test-free-custody", "make test-free-policy", "make test-free-market-data", "make test-free-jupiter", "make test-free-evidence", "proposal evidence-check", "proposal review", "proposal approval-create", "proposal key-create", "proposal policy-create", "--operator-approver", "proposal bundle-check", "proposal self-hosted-check", "proposal authority-check", "proposal submitter-check", "proposal canary-check", "proposal turnkey-check", "--recovery-status", "stores the exact two-provider reconciliation", "--retire-mainnet", "--recovery-mode stop_only", "exact_retry"} {
		if !strings.Contains(operations, want) {
			t.Errorf("OPERATIONS.md is missing reference fact %q", want)
		}
	}

	roadmap := readDocumentation(t, "../../ROADMAP.md")
	for _, want := range []string{
		"Observe and index", "fee payer signature", "Rooted event feed",
		"Custom indexer", "Mainnet stays",
	} {
		if !strings.Contains(roadmap, want) {
			t.Errorf("ROADMAP.md is missing product-direction fact %q", want)
		}
	}
	for _, stale := range []string{
		"sudo systemctl enable --now mithril-agent-swap.service",
		"Use `deploy/systemd/mithril-agent-swap.service`",
		"jupiter-signer-policy.json",
		"jupiter-submitter-policy.json",
	} {
		if strings.Contains(operations, stale) {
			t.Errorf("OPERATIONS.md contains unsupported legacy install instruction %q", stale)
		}
	}

	makefile := readDocumentation(t, "../../Makefile")
	for _, want := range []string{"prereqs-trading: prereqs", "Walletless prerequisites are ready", "MITHRIL_AGENT_QUOTE_RPC_URL", "RPC endpoints all four set", "TestDecodeProgram", "TestFullStrategyDocumentationMatchesGeneratedLayout", "test-walletless", "test-account-free", "test-free-policy", "test-free-market-data", "test-free-jupiter", "test-free-evidence", "test-prometheus", "Offline test identities exercised signing"} {
		if !strings.Contains(makefile, want) {
			t.Errorf("Makefile prerequisite check is missing %q", want)
		}
	}
	missingRPC := strings.Index(makefile, `echo "  RPC endpoints NOT SET:$$miss"`)
	if missingRPC < 0 || !strings.Contains(makefile[max(0, missingRPC-64):missingRPC], "ok=0") {
		t.Fatal("optional trading prerequisites must fail when an RPC setting is missing")
	}
	if !strings.Contains(makefile, "The command output above names the exact failed stage.") ||
		strings.Contains(makefile, "does not currently retain the protected history") {
		t.Fatal("public archive drill guidance must preserve the actual failed stage")
	}
}

func TestPrivateRuntimeArtifactsStayOutsideCheckout(t *testing.T) {
	ignore := readDocumentation(t, "../../.gitignore")
	makefile := readDocumentation(t, "../../Makefile")
	for _, name := range []string{
		"agent-account.json", "wallet.json", "*-wallet.json", "attestor.json",
		"*-attestor.json", "*-key.json", "transport-key", "*-transport-key",
		"submission-recovery*.json", "operator-approval.json", "*-signer-request.json",
		"*-signer-response.json", "*-check-result.json", "control.json*", "*.jsonl*",
		"config.json", "observation.json", "update-cursor.json*",
	} {
		if !strings.Contains(ignore, name) {
			t.Errorf(".gitignore does not protect runtime artifact %q", name)
		}
		if !strings.Contains(makefile, "-name '"+name+"'") {
			t.Errorf("check-private-files does not reject runtime artifact %q", name)
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
