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
	if lines := strings.Count(readme, "\n") + 1; lines < 250 || lines > 300 {
		t.Errorf("README.md has %d lines; keep detailed procedures in the linked guides", lines)
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
	for _, want := range []string{"# Mithril Agent operations and reference", "[README.md](README.md)", "[QUICKSTART.md](QUICKSTART.md)", "### Node-state filesystem access", "mithril-agent audit snapshot", "keyless systemd timer", "make test-account-free", "make test-free-rehearsal", "make test-free-custody", "make test-free-policy", "make test-free-market-data", "make test-free-jupiter", "make test-free-evidence", "proposal evidence-check", "proposal review", "proposal approval-create", "proposal key-create", "proposal policy-create", "--operator-approver", "proposal bundle-check", "proposal self-hosted-check", "proposal authority-check", "proposal submitter-check", "proposal canary-check", "proposal turnkey-check", "--recovery-status", "stores the exact two-provider reconciliation", "--retire-mainnet", "--recovery-mode stop_only", "exact_retry", "mithril-agent-paper-auto-select.timer", "mithril-agent-paper-champion.path", "market sockets publish fresh current-day"} {
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
		"name: mithril-hermes-research", "external: true",
		"source: /usr/local/libexec/mithril-agent/mithril-agent",
		"source: ${MITHRIL_HERMES_QUERY_FILE:-/opt/mithril-hermes-research/prompts/market-scout.md}",
		"source: /var/lib/mithril-agent-research/index",
		"source: /var/lib/mithril-agent-research/policy",
		"source: /var/lib/mithril-agent-research/journals",
		"source: /var/lib/mithril-agent-research/champion",
		"source: /var/lib/mithril-agent-research/challenger",
		"source: /var/lib/mithril-agent-research/runs/champion",
		"source: /var/lib/mithril-agent-research/runs/challenger",
		"target: /opt/mithril/bin/mithril-agent",
		"target: /opt/mithril/prompts/market-scout.md",
		"target: /var/lib/mithril-agent/index",
		"target: /var/lib/mithril-agent-research/challenger",
		"target: /var/lib/mithril-agent-research/runs/challenger",
		"cpus: 2.0", "mem_limit: 4g", "pids_limit: 512",
		"driver: local", "max-size: 10m", "max-file: \"5\"",
		"- hermes", "- chat", "- --query-file", "- /opt/mithril/prompts/market-scout.md",
		"- --provider", "- openai-codex", "- --model", "- gpt-5.6-terra",
		"- --reasoning", "- high", "- --toolsets",
		"${MITHRIL_HERMES_TOOLSETS:-web,solana_docs}", "- --run-budget", "- \"300\"", "- --quiet",
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("Hermes compose file is missing pinned read-only boundary %q", want)
		}
	}
	if got := strings.Count(compose, "read_only: true"); got != 8 {
		t.Errorf("Hermes compose has %d read-only mounts; want 8", got)
	}
	if got := strings.Count(compose, "create_host_path: false"); got != 9 {
		t.Errorf("Hermes compose protects %d host paths; want 9", got)
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
		"openrouter_api_key", "tavily_api_key", "telegram_bot_token",
		"restart:", "gateway\n", "gateway run",
	} {
		if strings.Contains(strings.ToLower(compose), forbidden) {
			t.Errorf("Hermes compose file contains forbidden capability %q", forbidden)
		}
	}

	config := readDocumentation(t, "../../deploy/hermes-research/config.yaml")
	for _, want := range []string{
		"_config_version: 39",
		"provider: openai-codex", "default: gpt-5.6-terra",
		"cron_mode: deny", "single_query_mode: deny", "allow_private_urls: false",
		"keyless_fallback: true", "keyless_rescue: true",
		"hard_stop_enabled: true", "exact_failure: 5", "idempotent_no_progress: 5",
		"allow_lazy_installs: false", "tirith_enabled: false", "tirith_fail_open: false",
		"memory_enabled: false", "user_profile_enabled: false",
		"allow_agent_scheduling: false", "unauthorized_dm_behavior: ignore",
		"plugins:\n  enabled: []", "telegram:\n    enabled: false",
		"mithril_index", "mithril_paper", "solana_docs",
		"mithril_index_transactions",
		"--max-record-age", "- 15m",
		"mithril_paper_create_challenger", "mithril_paper_challenge_status",
		"/var/lib/mithril-agent-research/challenger/active.json",
	} {
		if !strings.Contains(config, want) {
			t.Errorf("Hermes config is missing read-only boundary %q", want)
		}
	}
	if got := strings.Count(config, "sampling:\n      enabled: false"); got != 3 {
		t.Errorf("Hermes config disables sampling for %d MCP servers; want 3", got)
	}
	if got := strings.Count(config, "elicitation:\n      enabled: false"); got != 3 {
		t.Errorf("Hermes config disables elicitation for %d MCP servers; want 3", got)
	}
	if got := strings.Count(config, "\n    trust: full\n"); got != 3 {
		t.Errorf("Hermes config grants reviewed full trust to %d MCP servers; want 3", got)
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
		"solana_expert__ask_for_help", "provider_routing:",
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
	if strings.Contains(deployReadme, "\ndocker compose ") {
		t.Error("rootful Hermes deployment README runs Docker Compose without sudo")
	}
	if strings.Contains(deployReadme, "\ndocker ") {
		t.Error("rootful Hermes deployment README runs Docker without sudo")
	}
	for _, forbidden := range []string{
		"-p mithril-research", "/profiles/mithril-research",
		"HERMES_HOME=/opt/data/profiles", "--deliver telegram",
	} {
		for name, contents := range map[string]string{
			"README": deployReadme, "Compose": compose, "config": config,
		} {
			if strings.Contains(contents, forbidden) {
				t.Errorf("Hermes %s still contains removed profile form %q", name, forbidden)
			}
		}
	}
	for _, want := range []string{
		"state/.no-bundled-skills",
		"only the official `hermes-agent/SKILL.md`",
		"mcp test mithril_paper",
		"Only `challenger/` is writable by Hermes",
		"later market days cannot reverse a completed decision",
		"explicit pre-champion registry must contain exactly",
		"pre-champion tools:", "post-filter registry assertion after each gate opens",
		"_get_platform_tools",
		"get_tool_definitions",
		"web_search",
		"web_extract",
		"exactly one URL per invocation",
		"browser_exec",
		"discover_mcp_tools",
		"the model receives exactly the configured 7 MCP tools",
		"github.com/NousResearch/hermes-agent/issues/88858",
		"`full` removes the per-call approval gate",
		"Run the canary with stdin closed and no TTY",
		"Helius MCP is deliberately not installed",
		"without an opt-out",
		"not maintain a fork or proxy",
		"mithril-hermes-research.timer", "systemctl list-timers",
		"auth add openai-codex", "never join the Docker group",
		"rootless Docker", "last recorded time", "Hetzner server backups",
		"mithril-agent-paper-challenger.path", "--paper-alert-status",
		"mithril-agent-paper-champion.path",
		"mithril-agent-paper-bootstrap.timer", "shadow select --initial",
		"mithril-agent-paper-auto-select.timer", "shadow restore", "champion/previous.json",
		"/var/lib/mithril-agent-research/status/champion/alerts.json",
		"single-URL extraction canary: pass", "state/cache/web",
		"REVIEWED_SHA256", "'root:root 755'", "--fee-reserve-sol 0.080",
	} {
		if !strings.Contains(deployReadme, want) {
			t.Errorf("Hermes deployment README is missing isolation check %q", want)
		}
	}
	marketScout := readDocumentation(t, "../../deploy/hermes-research/prompts/market-scout.md")
	for _, want := range []string{
		"https://solana.com/changelog", "https://github.com/anza-xyz/agave/releases",
		"https://developers.jup.ag/docs/api-reference/swap/build",
		"https://developers.jup.ag/docs/price", "https://status.jup.ag/",
		"https://docs.pyth.network/price-feeds/core/fetch-price-updates",
		"https://status.pyth.network/",
		"https://docs.kraken.com/exchange/api-reference/spot-websocket-v2/ticker",
		"https://status.kraken.com/", "Do not ingest or deliver through Telegram",
		"https://www.helius.dev/docs/laserstream", "https://docs.jito.wtf/lowlatencytxnsend/",
		"previous 12 hours", "infrastructure", "not trading alpha",
		"cbBTC/USDC", "30 consecutive complete days of evidence",
		"Never resolve an asset by ticker alone",
		"hypothesis_id", "verified_facts", "no_trade_case", "risk_veto",
		"candidate_parameter_diff", "two independent timestamped",
		"trusted run-time anchor", "single_source` requires exactly one source",
		"`unverified`", "requires an empty sources array",
		"https://www.coinbase.com/cbbtc", "https://www.circle.com/transparency",
		"not all-in execution guarantees",
	} {
		if !strings.Contains(marketScout, want) {
			t.Errorf("Hermes market scout omits official source rule %q", want)
		}
	}
	if strings.Contains(marketScout, "since the previous run") {
		t.Error("stateless Hermes market scout uses an unknowable previous-run window")
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
	for _, forbidden := range []string{
		"OPENROUTER", "TAVILY", "TELEGRAM", "MITHRIL_AGENT_BIN", "MITHRIL_INDEX_DIR", "MITHRIL_PAPER_",
	} {
		if strings.Contains(envExample, forbidden) {
			t.Errorf("Hermes environment example still requests %s credentials", forbidden)
		}
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
		"./deploy/hermes-research/check-network.sh",
		"./deploy/hermes-research/bootstrap-first-champion.sh",
		"./deploy/hermes-research/run-market-scout.sh",
		"./deploy/hermes-research/compose.yaml",
		"./deploy/hermes-research/config.yaml",
		"./deploy/hermes-research/env.example",
		"./deploy/systemd/mithril-agent-paper-base.service",
		"./deploy/systemd/mithril-agent-paper-champion.service",
		"./deploy/systemd/mithril-agent-paper-champion.path",
		"./deploy/systemd/mithril-agent-paper-challenger.service",
		"./deploy/systemd/mithril-agent-paper-challenger.path",
		"./deploy/systemd/mithril-agent-paper-bootstrap.service",
		"./deploy/systemd/mithril-agent-paper-bootstrap.timer",
		"./deploy/systemd/mithril-agent-paper-auto-select.service",
		"./deploy/systemd/mithril-agent-paper-auto-select.timer",
		"./deploy/systemd/mithril-agent-paper-dashboard.service",
		"./deploy/systemd/mithril-agent-paper-dashboard.socket",
		"./deploy/systemd/mithril-agent-market-candidate@.service",
		"./deploy/systemd/mithril-hermes-research-egress.service",
		"./deploy/systemd/mithril-hermes-research.service",
		"./deploy/systemd/mithril-hermes-research.timer",
	} {
		if !strings.Contains(manifest, path) {
			t.Errorf("source manifest omits Hermes deployment input %q", path)
		}
	}
	for _, path := range []string{
		"../../deploy/hermes-research/check-network.sh",
		"../../deploy/hermes-research/bootstrap-first-champion.sh",
		"../../deploy/hermes-research/run-market-scout.sh",
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0111 == 0 {
			t.Errorf("Hermes deployment script is not executable: %q", path)
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
	championPath := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-champion.path")
	if !strings.Contains(championPath, "PathChanged=/var/lib/mithril-agent-research/champion/active.json") ||
		strings.Contains(championPath, "PathExists=") {
		t.Fatal("champion path does not watch pointer changes safely")
	}
	championUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-champion.service")
	if !strings.Contains(championUnit, "ConditionPathExists=/var/lib/mithril-agent-research/champion/active.json") {
		t.Fatal("champion service does not require its selected pointer")
	}
	marketScoutWrapper := readDocumentation(t, "../../deploy/hermes-research/run-market-scout.sh")
	for _, want := range []string{
		"systemctl is-active --quiet mithril-agent-paper-base.service",
		"systemctl is-active --quiet mithril-agent-paper-champion.service",
	} {
		if !strings.Contains(marketScoutWrapper, want) {
			t.Errorf("Hermes paper tools are not gated by a healthy observer: %q", want)
		}
	}
	candidateUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-market-candidate@.service")
	for _, want := range []string{
		"ConditionPathExists=/etc/mithril-agent/market-%i.env",
		"--market ${MITHRIL_AGENT_MARKET} --observe ${MITHRIL_AGENT_OBSERVE}",
		"--journal /var/lib/mithril-agent-research/market-admission-%i/evidence.jsonl",
		"ReadWritePaths=/var/lib/mithril-agent-research/market-admission-%i",
		"ProtectSystem=strict", "UMask=0077",
	} {
		if !strings.Contains(candidateUnit, want) {
			t.Errorf("candidate admission collector is missing %q", want)
		}
	}
	autoSelectUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-auto-select.service")
	for _, want := range []string{
		"shadow auto-select", "PrivateNetwork=yes", "RestrictAddressFamilies=AF_UNIX",
		"--rollback-pointer /var/lib/mithril-agent-research/champion/previous.json",
		"ReadWritePaths=/var/lib/mithril-agent-research/champion /var/lib/mithril-agent-research/challenger",
	} {
		if !strings.Contains(autoSelectUnit, want) {
			t.Errorf("paper auto-selector is missing %q", want)
		}
	}
	autoSelectTimer := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-auto-select.timer")
	for _, want := range []string{
		"OnCalendar=*-*-* *:20:00 UTC", "Persistent=true",
		"Unit=mithril-agent-paper-auto-select.service", "WantedBy=timers.target",
	} {
		if !strings.Contains(autoSelectTimer, want) {
			t.Errorf("paper auto-selector timer is missing %q", want)
		}
	}
	bootstrapUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-bootstrap.service")
	for _, want := range []string{
		"ConditionPathExists=/var/lib/mithril-agent-research/policy/policy.json",
		"ConditionPathExists=!/var/lib/mithril-agent-research/champion/active.json",
		"PrivateNetwork=yes", "RestrictAddressFamilies=AF_UNIX",
		"ReadOnlyPaths=/var/lib/mithril-agent-research/policy /var/lib/mithril-agent-research/journals",
		"ReadWritePaths=/var/lib/mithril-agent-research/champion /var/lib/mithril-agent-research/challenger",
	} {
		if !strings.Contains(bootstrapUnit, want) {
			t.Errorf("paper bootstrap unit is missing %q", want)
		}
	}
	bootstrapTimer := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-bootstrap.timer")
	for _, want := range []string{
		"OnCalendar=*-*-* *:30:00 UTC", "Persistent=true",
		"Unit=mithril-agent-paper-bootstrap.service", "WantedBy=timers.target",
	} {
		if !strings.Contains(bootstrapTimer, want) {
			t.Errorf("paper bootstrap timer is missing %q", want)
		}
	}
	networkCheck := readDocumentation(t, "../../deploy/hermes-research/check-network.sh")
	for _, want := range []string{
		"bridge false false 1", "docker network inspect",
	} {
		if !strings.Contains(networkCheck, want) {
			t.Errorf("Hermes network preflight is missing %q", want)
		}
	}
	egressUnit := readDocumentation(t, "../../deploy/systemd/mithril-hermes-research-egress.service")
	for _, want := range []string{
		"/opt/mithril-hermes-research/check-network.sh",
		"BindsTo=docker.service", "PartOf=docker.service", "WantedBy=docker.service",
		"-j MITHRIL_HERMES", "-j RETURN", "-D INPUT", "-I INPUT",
	} {
		if !strings.Contains(egressUnit, want) {
			t.Errorf("Hermes egress unit is missing %q", want)
		}
	}
	if got := strings.Count(egressUnit, "-A MITHRIL_HERMES -d"); got != 6 {
		t.Errorf("Hermes egress unit rejects %d private ranges; want 6", got)
	}
	hermesUnit := readDocumentation(t, "../../deploy/systemd/mithril-hermes-research.service")
	for _, want := range []string{
		"Requires=docker.service mithril-hermes-research-egress.service",
		"BindsTo=docker.service mithril-hermes-research-egress.service",
		"After=docker.service mithril-hermes-research-egress.service",
		"ConditionPathExists=/opt/mithril-hermes-research/state/auth.json",
		"ConditionPathExists=/var/lib/mithril-agent-research/policy/policy.json",
		"ConditionPathIsDirectory=/var/lib/mithril-agent-research/reports",
		"ConditionPathIsDirectory=/var/lib/mithril-agent-dashboard",
		"Type=oneshot", "UMask=0077",
		"RuntimeDirectory=mithril-hermes-research", "RuntimeDirectoryMode=0711",
		"iptables -C DOCKER-USER", "iptables -C INPUT",
		"ExecStart=/opt/mithril-hermes-research/run-market-scout.sh",
		"TimeoutStartSec=6min", "TimeoutStopSec=1min",
	} {
		if !strings.Contains(hermesUnit, want) {
			t.Errorf("Hermes systemd owner is missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"gateway", "docker compose up", "docker compose down", "Restart=", "[Install]",
	} {
		if strings.Contains(hermesUnit, forbidden) {
			t.Errorf("Hermes one-shot unit contains obsolete behavior %q", forbidden)
		}
	}
	researchRunner := readDocumentation(t, "../../deploy/hermes-research/run-market-scout.sh")
	for _, want := range []string{
		"toolsets='web,solana_docs'",
		"/var/lib/mithril-agent-research/champion/active.json",
		"toolsets=\"$toolsets,mithril_paper\"",
		"/var/lib/mithril-agent-research/index/events.jsonl",
		"index doctor", "--max-record-age 15m",
		"toolsets=\"$toolsets,mithril_index\"",
		"export MITHRIL_HERMES_TOOLSETS=\"$toolsets\"",
		"/var/lib/mithril-agent-dashboard/instruction.json",
		"--render-instruction \"$instruction\"",
		"query_file=/run/mithril-hermes-research/market-scout.md",
		"Trusted run-time anchor", "/usr/bin/date -u +%Y-%m-%dT%H:%M:%SZ",
		"/usr/bin/chmod 0644 \"$query_file\"",
		"export MITHRIL_HERMES_QUERY_FILE=\"$query_file\"",
		"ulimit -f 128",
		"/usr/bin/docker compose run --rm --no-TTY hermes-research >\"$packet\"",
		"mithril-agent research packet-record", "--archive-dir /var/lib/mithril-agent-research/reports",
		"--latest \"$latest\"", "/var/lib/mithril-agent-dashboard/research.json",
		"/usr/bin/install -o mithril-agent-dashboard -g mithril-agent-dashboard -m 0600",
		"runuser -u mithril-agent-dashboard", "--in \"$dashboard_packet\" --latest \"$projection\"",
	} {
		if !strings.Contains(researchRunner, want) {
			t.Errorf("Hermes market scout wrapper is missing %q", want)
		}
	}
	researchTimer := readDocumentation(t, "../../deploy/systemd/mithril-hermes-research.timer")
	for _, want := range []string{
		"OnCalendar=*-*-* 00,06,12,18:15:00 UTC", "Persistent=true",
		"RandomizedDelaySec=15min", "AccuracySec=1min",
		"Unit=mithril-hermes-research.service", "WantedBy=timers.target",
	} {
		if !strings.Contains(researchTimer, want) {
			t.Errorf("Hermes market scout timer is missing %q", want)
		}
	}
	championUnit = readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-champion.service")
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
	jupUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-jup.service")
	jupBridge := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-jup-status-bridge.service")
	jupSocket := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-jup-status.socket")
	telegramPaper := readDocumentation(t, "../../deploy/systemd/mithril-agent-telegram-paper.conf")
	const jupAlertPath = "/var/lib/mithril-agent-research/status/jup/alerts.json"
	for _, want := range []string{
		"ConditionPathExists=/var/lib/mithril-agent-research/policy/jup-policy.json",
		"--dir /var/lib/mithril-agent-research/journals-jup",
		"--alert-status " + jupAlertPath,
		"ReadWritePaths=/var/lib/mithril-agent-research/journals-jup /var/lib/mithril-agent-research/status/jup",
	} {
		if !strings.Contains(jupUnit, want) {
			t.Errorf("JUP observer is missing %q", want)
		}
	}
	if !strings.Contains(jupBridge, "LoadCredential=paper-status:"+jupAlertPath) ||
		!strings.Contains(jupBridge, "InaccessiblePaths=/var/lib/mithril-agent-research") ||
		!strings.Contains(jupSocket, "ListenStream=/run/mithril-agent-paper-jup-status.sock") {
		t.Fatal("JUP status bridge/socket does not preserve the bounded credential path")
	}
	for _, want := range []string{
		"SOL/USDC=/run/mithril-agent-paper-status.sock",
		"JUP/USDC=/run/mithril-agent-paper-jup-status.sock",
	} {
		if !strings.Contains(telegramPaper, want) {
			t.Errorf("paper Telegram opt-in is missing %q", want)
		}
	}
	wifUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-market-wif.service")
	for _, want := range []string{
		"ConditionPathExists=/etc/mithril-agent/paper-wif.env",
		"--market WIF/USDC --observe ${MITHRIL_AGENT_WIF_OBSERVE}",
		"--journal /var/lib/mithril-agent-research/market-admission-wif/evidence.jsonl",
		"ReadWritePaths=/var/lib/mithril-agent-research/market-admission-wif",
		"ProtectSystem=strict", "UMask=0077",
	} {
		if !strings.Contains(wifUnit, want) {
			t.Errorf("WIF admission collector is missing %q", want)
		}
	}
	dashboardUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-dashboard.service")
	dashboardSocket := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-dashboard.socket")
	dashboardSysusers := readDocumentation(t, "../../deploy/sysusers/mithril-agent-dashboard.conf")
	for _, want := range []string{
		"User=mithril-agent-dashboard", "SupplementaryGroups=mithril-agent-status",
		"PrivateNetwork=yes", "RestrictAddressFamilies=AF_UNIX",
		"InaccessiblePaths=/var/lib/mithril-agent",
		"SOL/USDC=/run/mithril-agent-paper-status.sock",
		"JUP/USDC=/run/mithril-agent-paper-jup-status.sock",
		"--research-packet-path /var/lib/mithril-agent-dashboard/research.json",
	} {
		if !strings.Contains(dashboardUnit, want) {
			t.Errorf("paper dashboard unit is missing %q", want)
		}
	}
	for _, forbidden := range []string{"EnvironmentFile=", "ReadWritePaths=", "AF_INET", "--listen"} {
		if strings.Contains(dashboardUnit, forbidden) {
			t.Errorf("paper dashboard unit contains unsafe capability %q", forbidden)
		}
	}
	for _, want := range []string{
		"ListenStream=/run/mithril-agent-paper-dashboard.sock",
		"SocketGroup=mithril-agent-dashboard", "SocketMode=0660",
		"FileDescriptorName=paper-dashboard", "Accept=no",
	} {
		if !strings.Contains(dashboardSocket, want) {
			t.Errorf("paper dashboard socket is missing %q", want)
		}
	}
	for _, want := range []string{
		"g mithril-agent-dashboard -", "u mithril-agent-dashboard -:mithril-agent-dashboard",
	} {
		if !strings.Contains(dashboardSysusers, want) {
			t.Errorf("paper dashboard identity is missing %q", want)
		}
	}
	if strings.Contains(dashboardSysusers, "m mithril-agent-dashboard mithril-agent-status") {
		t.Error("paper dashboard identity has status access outside its hardened service")
	}
	for _, want := range []string{
		"/var/lib/mithril-agent-research/index \\",
		"/var/lib/mithril-agent-research/runs \\",
		"/var/lib/mithril-agent-research/status \\",
		"/var/lib/mithril-agent-research/reports \\",
		"/var/lib/mithril-agent-dashboard",
		"MITHRIL_AGENT_MARKET=JTO/USDC", "MITHRIL_AGENT_MARKET=PYTH/USDC",
		"mithril-agent-market-candidate@jto.service",
		"mithril-agent-market-candidate@pyth.service",
		"PUMP remains excluded", "Token-2022",
		"id -g mithril-agent-research",
		"Do not copy or hand-edit `events.jsonl`",
		"/opt/mithril-hermes-research",
		"sudo -u mithril-agent-research env HOME=/var/lib/mithril-agent-research",
		"docker compose down --timeout 30",
		"test -z \"$(sudo docker compose ps -q)\"",
		"systemctl stop mithril-hermes-research.timer",
		"systemctl start mithril-hermes-research.service",
		"systemctl enable --now mithril-hermes-research.timer",
		"$(id -u mithril-agent-research):600",
		"sudo test ! -d state/cache/web",
		"sudo find state/cache/web",
		"sudo find state/skills -name SKILL.md -print",
		"This profile disables Tirith",
		"prevents Hermes from downloading an unpinned",
	} {
		if !strings.Contains(deployReadme, want) {
			t.Errorf("Hermes deployment README is missing safe operation %q", want)
		}
	}
	if strings.Contains(deployReadme, "enabling Telegram or cron") {
		t.Error("Hermes deployment README suggests enabling its disabled Telegram platform")
	}
	for _, forbidden := range []string{"hermes cron create", "supervised gateway", "restore the gateway"} {
		if strings.Contains(deployReadme, forbidden) {
			t.Errorf("Hermes deployment README contains obsolete schedule guidance %q", forbidden)
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
