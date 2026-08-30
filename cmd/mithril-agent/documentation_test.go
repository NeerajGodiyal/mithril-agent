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
		"mithril-agent-research",
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
		"rooted feed and private index decode and",
		"executable paths remain",
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
		"each v5 index",
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
		"Custom indexer", "Mainnet stays", "Rooted Solana v1 transaction ingestion",
	} {
		if !strings.Contains(roadmap, want) {
			t.Errorf("ROADMAP.md is missing product-direction fact %q", want)
		}
	}
	overview := readDocumentation(t, "../../OVERVIEW.md")
	for _, want := range []string{
		"Rooted Solana v1 transactions are decoded and identity-checked",
		"They are not signed or executed",
	} {
		if !strings.Contains(overview, want) {
			t.Errorf("OVERVIEW.md is missing Solana v1 boundary %q", want)
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

func TestHermesResearchProfileStaysBoundedAndPinned(t *testing.T) {
	compose := readDocumentation(t, "../../deploy/hermes-research/compose.yaml")
	for _, want := range []string{
		"nousresearch/hermes-agent:v2026.8.27@sha256:e0df6adebddf29b91112aefc999d4aaf6846c9eb544faca5672a16a13590ff79",
		"target: /opt/mithril/bin/mithril-agent",
		"source: \"${MITHRIL_STATUS_SOCKET:?set MITHRIL_STATUS_SOCKET}\"",
		"target: /run/mithril-agent/status.sock",
		"target: /var/lib/mithril-agent/index",
		"target: /var/lib/mithril-agent-research/challenger",
		"target: /var/lib/mithril-agent-research/runs/challenger",
		"cpus: 2.0", "mem_limit: 4g", "pids_limit: 512",
		"driver: local", "max-size: 10m", "max-file: \"5\"",
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("Hermes compose file is missing pinned read-only boundary %q", want)
		}
	}
	if got := strings.Count(compose, "read_only: true"); got != 8 {
		t.Errorf("Hermes compose has %d read-only Mithril mounts; want 8", got)
	}
	if got := strings.Count(compose, "create_host_path: false"); got != 9 {
		t.Errorf("Hermes compose protects %d Mithril host paths; want 9", got)
	}
	challengerMount := strings.Index(compose, "target: /var/lib/mithril-agent-research/challenger\n")
	championRunMount := strings.Index(compose, "target: /var/lib/mithril-agent-research/runs/champion\n")
	if challengerMount < 0 || championRunMount <= challengerMount ||
		strings.Contains(compose[challengerMount:championRunMount], "read_only: true") {
		t.Fatal("Hermes paper challenger mount must be the one writable Mithril mount")
	}
	for _, forbidden := range []string{
		"nousresearch/hermes-agent:latest", "network_mode:", "ports:",
		"docker.sock", "turnkey", "signer", "submitter", "helius", "secrets:", "build:",
	} {
		if strings.Contains(strings.ToLower(compose), forbidden) {
			t.Errorf("Hermes compose file contains forbidden capability %q", forbidden)
		}
	}

	config := readDocumentation(t, "../../deploy/hermes-research/config.yaml")
	for _, want := range []string{
		"_config_version: 39",
		"cron_mode: deny", "single_query_mode: deny", "allow_private_urls: false",
		"\nprovider_routing:\n  data_collection: deny",
		"search_backend: tavily", "extract_backend: tavily",
		"keyless_fallback: false", "keyless_rescue: false",
		"allow_lazy_installs: false", "tirith_fail_open: false",
		"memory_enabled: false", "user_profile_enabled: false",
		"allow_agent_scheduling: false", "unauthorized_dm_behavior: ignore",
		"plugins:\n  enabled: []",
		"guest_mode: false", "observe_unmentioned_group_messages: false",
		"mithril_status", "mithril_index", "mithril_paper", "solana_docs",
		"mithril_agent_status", "mithril_index_transactions",
		"mithril_paper_create_challenger", "mithril_paper_challenge_status",
		"/var/lib/mithril-agent-research/challenger/active.json",
	} {
		if !strings.Contains(config, want) {
			t.Errorf("Hermes config is missing read-only boundary %q", want)
		}
	}
	if got := strings.Count(config, "sampling:\n      enabled: false"); got != 4 {
		t.Errorf("Hermes config disables sampling for %d MCP servers; want 4", got)
	}
	if got := strings.Count(config, "elicitation:\n      enabled: false"); got != 4 {
		t.Errorf("Hermes config disables elicitation for %d MCP servers; want 4", got)
	}
	if got := strings.Count(config, "\n    trust: full\n"); got != 4 {
		t.Errorf("Hermes config grants reviewed full trust to %d MCP servers; want 4", got)
	}
	if strings.Contains(config, "trust: untrusted") {
		t.Fatal("Hermes config cannot use interactive MCP trust in unattended sessions")
	}
	toolsetsStart := strings.Index(config, "platform_toolsets:\n")
	if toolsetsStart < 0 {
		t.Fatal("Hermes config has no platform toolsets")
	}
	toolsets := config[toolsetsStart:]
	telegramStart := strings.Index(toolsets, "  telegram:\n")
	cronStart := strings.Index(toolsets, "  cron:\n")
	if telegramStart < 0 || cronStart <= telegramStart ||
		strings.Contains(toolsets[telegramStart:cronStart], "mithril_paper") {
		t.Fatal("Hermes Telegram toolset must not expose the paper mutation server")
	}
	for _, forbidden := range []string{
		"helius", "secret_sources:", "solana_documentation_search",
		"solana_expert__ask_for_help", "\n  provider_routing:",
	} {
		if strings.Contains(strings.ToLower(config), forbidden) {
			t.Errorf("Hermes config contains forbidden capability %q", forbidden)
		}
	}
	for _, disabled := range []string{
		"terminal", "file", "code_execution", "skills", "memory",
		"delegation", "cronjob", "computer_use", "desktop_ui",
	} {
		if !strings.Contains(config, "    - "+disabled+"\n") {
			t.Errorf("Hermes config does not disable toolset %q", disabled)
		}
	}
	if strings.Contains(toolsets, "    - browser\n") {
		t.Error("Hermes platform allowlists expose the browser toolset")
	}

	readme := readDocumentation(t, "../../README.md")
	if !strings.Contains(readme, "deploy/hermes-research") {
		t.Error("README.md does not route operators to the bounded Hermes profile")
	}
	deployReadme := readDocumentation(t, "../../deploy/hermes-research/README.md")
	for _, want := range []string{
		"state/.no-bundled-skills",
		"hermes profile create mithril-research \\\n  --no-skills",
		"only the official `hermes-agent/SKILL.md`",
		"MITHRIL_STATUS_SOCKET",
		"/run/mithril-agent-status-sell.sock",
		"do not\nmount all of `/run`",
		"mcp test mithril_paper",
		"Only `challenger/` is writable by Hermes",
		"later market days cannot reverse a completed decision",
		"The resolver assertion proves that Telegram omits the paper server",
		"_get_platform_tools",
		"get_tool_definitions",
		"web_search",
		"web_extract",
		"exactly one URL per invocation",
		"browser_exec",
		"discover_mcp_tools",
		"the model receives exactly the configured 11 MCP tools",
		"github.com/NousResearch/hermes-agent/issues/88858",
		"`full` removes the per-call approval gate",
		"Run the canary with stdin closed and no TTY",
		"Helius MCP is deliberately not installed",
		"without an opt-out",
		"not maintain a fork or proxy",
		"every 6h", "--provider openrouter", "--model anthropic/claude-opus-4.6",
		"different bot", "`getMe` bot IDs differ", "never join the Docker group",
		"rootless Docker", "last recorded time", "Hetzner server backups",
		"mithril-agent-paper-challenger.path", "--paper-alert-status",
		"/var/lib/mithril-agent-research/status/champion/alerts.json",
		"single-URL extraction canary: pass", "state/profiles/mithril-research/cache/web",
		"REVIEWED_SHA256", "'root:root 755'",
	} {
		if !strings.Contains(deployReadme, want) {
			t.Errorf("Hermes deployment README is missing isolation check %q", want)
		}
	}
	marketScout := readDocumentation(t, "../../deploy/hermes-research/prompts/market-scout.md")
	for _, want := range []string{
		"https://solana.com/changelog", "https://github.com/anza-xyz/agave/releases",
		"https://status.jup.ag/", "https://status.pyth.network/",
		"https://docs.kraken.com/api-reference/transparency/pre-trade-data",
		"https://status.kraken.com/", "Do not ingest Telegram channels",
	} {
		if !strings.Contains(marketScout, want) {
			t.Errorf("Hermes market scout omits official source rule %q", want)
		}
	}
	if strings.Contains(deployReadme, "/usr/local/bin/mithril-agent") {
		t.Error("Hermes runbook invokes a different agent binary than the supervised units")
	}
	for _, forbidden := range []string{
		"mcp test helius_read", "configured Mithril, Solana, and Helius evidence tools",
	} {
		if strings.Contains(deployReadme, forbidden) {
			t.Errorf("Hermes deployment README still enables removed Helius surface %q", forbidden)
		}
	}

	ignore := readDocumentation(t, "../../.gitignore")
	if !strings.Contains(ignore, "/deploy/hermes-research/state/") {
		t.Error(".gitignore does not protect Hermes state")
	}
	deployIgnore := readDocumentation(t, "../../deploy/hermes-research/.gitignore")
	if !strings.Contains(deployIgnore, "secrets/") {
		t.Error("Hermes deploy does not ignore private secret material")
	}
	envExample := readDocumentation(t, "../../deploy/hermes-research/env.example")
	if !strings.Contains(envExample,
		"MITHRIL_AGENT_BIN=/usr/local/libexec/mithril-agent/mithril-agent") {
		t.Error("Hermes environment example does not select the supervised agent binary")
	}
	if _, err := os.Stat("../../deploy/hermes-research/env.example"); err != nil {
		t.Fatal("Hermes environment example is missing")
	}
	for _, path := range []string{
		"../../deploy/hermes-research/.dockerignore",
		"../../deploy/hermes-research/Dockerfile",
		"../../deploy/hermes-research/helius-mcp",
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("removed Hermes dependency path still exists %q: %v", path, err)
		}
	}

	manifest := readDocumentation(t, "../../mithril-agent-source.sha256")
	makefile := readDocumentation(t, "../../Makefile")
	for _, privatePath := range []string{
		"./deploy/hermes-research/state/*", "./deploy/hermes-research/secrets/*",
	} {
		if strings.Count(makefile, "-not -path '"+privatePath+"'") != 2 {
			t.Errorf("source manifest generation does not exclude private path %q", privatePath)
		}
	}
	for _, path := range []string{
		"./deploy/hermes-research/compose.yaml",
		"./deploy/hermes-research/config.yaml",
		"./deploy/hermes-research/env.example",
		"./deploy/systemd/mithril-agent-paper-base.service",
		"./deploy/systemd/mithril-agent-paper-champion.service",
		"./deploy/systemd/mithril-agent-paper-challenger.service",
		"./deploy/systemd/mithril-agent-paper-challenger.path",
	} {
		if !strings.Contains(manifest, path) {
			t.Errorf("source manifest omits Hermes deployment input %q", path)
		}
	}

	for _, name := range []string{"base", "champion", "challenger"} {
		unit := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-"+name+".service")
		for _, want := range []string{
			"User=mithril-agent-research", "EnvironmentFile=/etc/mithril-agent/paper.env",
			"InaccessiblePaths=/proc", "NoNewPrivileges=yes", "ProtectSystem=strict",
			"Restart=always", "RestartSec=15s",
			"MITHRIL_AGENT_KRAKEN_RATE_STATE=/run/mithril-agent-research/kraken-rate",
			"RuntimeDirectory=mithril-agent-research", "RuntimeDirectoryPreserve=yes",
		} {
			if !strings.Contains(unit, want) {
				t.Errorf("paper %s unit is missing %q", name, want)
			}
		}
		if name != "challenger" && !strings.Contains(unit, "StartLimitIntervalSec=0") {
			t.Errorf("paper %s unit does not disable restart limiting", name)
		}
		if name == "challenger" && strings.Contains(unit, "StartLimitIntervalSec=0") {
			t.Error("challenger path-activated unit disables restart limiting")
		}
		if strings.Contains(unit, "/var/lib/mithril-agent/paper-research") {
			t.Errorf("paper %s unit is nested under the private live-agent home", name)
		}
	}
	challengerPath := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-challenger.path")
	if !strings.Contains(challengerPath, "PathChanged=/var/lib/mithril-agent-research/challenger/active.json") ||
		strings.Contains(challengerPath, "PathExists=") {
		t.Fatal("challenger path does not watch pointer changes safely")
	}
	championUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-champion.service")
	bridgeUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-status-bridge.service")
	legacyTelegramUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-telegram.service")
	if strings.Contains(legacyTelegramUnit, "paper-status") {
		t.Fatal("legacy Telegram unit makes opt-in paper status mandatory")
	}
	const alertPath = "/var/lib/mithril-agent-research/status/champion/alerts.json"
	if !strings.Contains(championUnit, "--alert-status "+alertPath) ||
		!strings.Contains(bridgeUnit, "LoadCredential=paper-status:"+alertPath) {
		t.Fatal("paper observer and Telegram bridge disagree on the alert snapshot path")
	}
	if !strings.Contains(bridgeUnit, "InaccessiblePaths=/var/lib/mithril-agent-research") {
		t.Fatal("paper status bridge can see the research tree outside its credential")
	}
	for _, want := range []string{
		"/var/lib/mithril-agent-research/runs \\",
		"/var/lib/mithril-agent-research/status \\",
		"getent group mithril-agent-status | cut -d: -f3",
		"/opt/mithril-hermes-research",
		"sudo -u mithril-agent-research env HOME=/var/lib/mithril-agent-research",
		"hermes -p mithril-research gateway stop",
		"hermes -p mithril-research gateway start",
	} {
		if !strings.Contains(deployReadme, want) {
			t.Errorf("Hermes deployment README is missing safe operation %q", want)
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
