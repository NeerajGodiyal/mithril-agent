package main

import (
	"os"
	"os/exec"
	"path/filepath"
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
		"source: /etc/mithril-agent/paper-active",
		"source: /run/mithril-hermes-research",
		"source: /etc/mithril-agent/paper-active/selection/sol/challenger",
		"source: /etc/mithril-agent/paper-active/selection/jup/challenger",
		"target: /opt/mithril/bin/mithril-agent",
		"target: /opt/mithril/prompts/market-scout.md",
		"target: /var/lib/mithril-agent/index",
		"target: /etc/mithril-agent/paper-active",
		"cpus: 2.0", "mem_limit: 4g", "pids_limit: 512",
		"driver: local", "max-size: 10m", "max-file: \"5\"",
		"- hermes", "- chat", "- --query-file", "- /opt/mithril/prompts/market-scout.md",
		"- --provider", "- openai-codex", "- --model", "- gpt-5.6-terra",
		"- --reasoning", "- high", "- --toolsets",
		"hermes-research-parallel:", "source: /run/mithril-hermes-research/research-state",
		"source: ./state/auth.json", "source: ./config-delegated.yaml",
		"source: ./SOUL-research.md", "source: ./AGENTS-research.md", "HERMES_HOME: /opt/research-data",
		"- /bin/sh", "- -ec", "exec hermes -z",
		"${MITHRIL_HERMES_TOOLSETS:-web,solana_docs,delegation}",
		"${MITHRIL_HERMES_TOOLSETS:-web,solana_docs}", "- --run-budget", "- \"300\"", "- --quiet",
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("Hermes compose file is missing pinned read-only boundary %q", want)
		}
	}
	if got := strings.Count(compose, "read_only: true"); got != 12 {
		t.Errorf("Hermes compose has %d read-only mounts; want 12", got)
	}
	if got := strings.Count(compose, "create_host_path: false"); got != 15 {
		t.Errorf("Hermes compose protects %d host paths; want 15", got)
	}
	challengerMount := "source: /etc/mithril-agent/paper-active/selection/sol/challenger\n        target: /etc/mithril-agent/paper-active/selection/sol/challenger\n        bind:"
	if !strings.Contains(compose, challengerMount) {
		t.Fatal("Hermes SOL paper challenger mount must be writable")
	}
	jupChallengerMount := "source: /etc/mithril-agent/paper-active/selection/jup/challenger\n        target: /etc/mithril-agent/paper-active/selection/jup/challenger\n        bind:"
	if !strings.Contains(compose, jupChallengerMount) {
		t.Fatal("Hermes JUP paper challenger mount must be writable")
	}
	for _, forbidden := range []string{
		"nousresearch/hermes-agent:latest", "network_mode:", "ports:",
		"docker.sock", "turnkey", "signer", "submitter", "helius", "secrets:", "build:",
		"openrouter_api_key", "tavily_api_key", "telegram_bot_token",
		"restart:", "gateway\n", "gateway run",
		"MITHRIL_PAPER_GENERATION",
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
		"mithril_index", "mithril_paper", "mithril_paper_jup", "solana_docs",
		"mithril_index_transactions",
		"--max-record-age", "- 15m",
		"mithril_paper_create_challenger", "mithril_paper_challenge_status",
		"/etc/mithril-agent/paper-active/selection/sol/challenger/active.json",
		"/etc/mithril-agent/paper-active/selection/jup/challenger/active.json",
		"/etc/mithril-agent/paper-active/runs/jup/base",
		"--instruction", "/run/mithril-hermes-research/instruction.json",
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

	delegatedConfig := readDocumentation(t, "../../deploy/hermes-research/config-delegated.yaml")
	for _, want := range []string{
		"max_concurrent_children: 3", "max_spawn_depth: 1", "orchestrator_enabled: false",
		"run_budget_seconds: 300", "reasoning_effort: high",
		"    - delegation\n", "mithril_index_transactions", "solana_docs",
		"allow_private_urls: false", "memory_enabled: false", "plugins:\n  enabled: []",
	} {
		if !strings.Contains(delegatedConfig, want) {
			t.Errorf("delegated Hermes config is missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"mithril_paper", "/var/lib/mithril-agent-research/policy",
		"challenger",
	} {
		if strings.Contains(delegatedConfig, forbidden) {
			t.Errorf("delegated Hermes config contains forbidden authority %q", forbidden)
		}
	}
	delegatedDisabledStart := strings.Index(delegatedConfig, "  disabled_toolsets:\n")
	delegatedSecurityStart := strings.Index(delegatedConfig, "\nsecurity:\n")
	if delegatedDisabledStart < 0 || delegatedSecurityStart <= delegatedDisabledStart ||
		strings.Contains(delegatedConfig[delegatedDisabledStart:delegatedSecurityStart], "    - delegation\n") {
		t.Fatal("delegated Hermes config disables its required delegation toolset")
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
		"mcp test mithril_paper_jup",
		"/run/mithril-hermes-research/instruction.json",
		"archive/jup-status-before-reallocation/alerts.json",
		"Only the SOL and JUP `challenger/` trees are writable by",
		"later market days cannot reverse a completed decision",
		"delegated research registry contains",
		"pre-champion tools:", "post-filter registry assertion after each gate opens",
		"_get_platform_tools",
		"get_tool_definitions",
		"web_search",
		"web_extract",
		"exactly one URL per invocation",
		"browser_exec",
		"discover_mcp_tools",
		"the model receives exactly the configured 9 MCP tools",
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
		"mithril-agent-paper-jup-pre-champion.service",
		"mithril-agent-paper-jup-status-handoff.service",
		"mithril-agent-paper-jup-status.socket", "status/jup/champion-owned",
		"/etc/mithril-agent/paper-active/status/sol/alerts.json",
		"single-URL extraction canary: pass", "state/cache/web",
		"REVIEWED_SHA256", "'root:root 755'", "--fee-reserve-sol 0.080",
	} {
		if !strings.Contains(deployReadme, want) {
			t.Errorf("Hermes deployment README is missing isolation check %q", want)
		}
	}
	if !strings.Contains(deployReadme, "Both paper policies deployed by") ||
		!strings.Contains(deployReadme, "supports a separately invoked fixed policy when no adaptive research packet is") ||
		strings.Contains(deployReadme, "fixed policies ignore the") {
		t.Fatal("Hermes deployment does not document its adaptive instruction boundary")
	}
	if strings.Contains(deployReadme, "disable --now mithril-agent-paper-jup-status.socket") {
		t.Fatal("JUP status socket is disabled during first-champion evidence collection")
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
		"Do not discard a supported observation", "Use `unverified` only",
		"independently call `web_extract` for every URL it cites",
		"The SOL server is `mithril_paper`; the JUP server is `mithril_paper_jup`",
		"infer one market's state", "at most one challenger per market per run",
		"`unverified`", "requires an empty sources array",
		"omit `retrieved_at`", "host inserts the exact successful",
		"https://www.coinbase.com/cbbtc", "https://www.circle.com/transparency",
		"not all-in execution guarantees",
		"host-produced completed perps summary",
		"content hash and completed-snapshot hashes as integrity bindings",
		"not market sources",
		"cannot open a holdout, change a policy, authorize execution",
		"If the host marks it unavailable, do not infer any perps result.",
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
		"./deploy/hermes-research/build-research-evidence.py",
		"./deploy/hermes-research/compose.yaml",
		"./deploy/hermes-research/config.yaml",
		"./deploy/hermes-research/config-delegated.yaml",
		"./deploy/hermes-research/AGENTS.md",
		"./deploy/hermes-research/AGENTS-research.md",
		"./deploy/hermes-research/SOUL-research.md",
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
		"./deploy/systemd/mithril-agent-paper-jup.service",
		"./deploy/systemd/mithril-agent-paper-jup-pre-champion.service",
		"./deploy/systemd/mithril-agent-paper-jup-champion.service",
		"./deploy/systemd/mithril-agent-paper-jup-champion.path",
		"./deploy/systemd/mithril-agent-paper-jup-challenger.service",
		"./deploy/systemd/mithril-agent-paper-jup-challenger.path",
		"./deploy/systemd/mithril-agent-paper-jup-bootstrap.service",
		"./deploy/systemd/mithril-agent-paper-jup-bootstrap.timer",
		"./deploy/systemd/mithril-agent-paper-jup-auto-select.service",
		"./deploy/systemd/mithril-agent-paper-jup-auto-select.timer",
		"./deploy/systemd/mithril-agent-paper-jup-status-handoff.service",
		"./deploy/systemd/mithril-agent-paper-jup-status.socket",
		"./deploy/systemd/mithril-agent-paper-jup-status-bridge.service",
		"./deploy/systemd/mithril-agent-paper-dashboard.service",
		"./deploy/systemd/mithril-agent-paper-dashboard.socket",
		"./deploy/systemd/mithril-agent-market-candidate@.service",
		"./deploy/systemd/mithril-agent-market-status.service",
		"./deploy/systemd/mithril-agent-market-status.timer",
		"./deploy/systemd/mithril-hermes-research-egress.service",
		"./deploy/systemd/mithril-hermes-research.service",
		"./deploy/systemd/mithril-hermes-research.timer",
	} {
		if !strings.Contains(manifest, path) {
			t.Errorf("source manifest omits Hermes deployment input %q", path)
		}
	}
	for _, path := range []string{
		"../../deploy/hermes-research/apply-paper-instruction.sh",
		"../../deploy/hermes-research/check-network.sh",
		"../../deploy/hermes-research/bootstrap-first-champion.sh",
		"../../deploy/hermes-research/run-paper-generation.sh",
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
		role := name
		for _, want := range []string{
			"User=mithril-agent-research", "EnvironmentFile=/etc/mithril-agent/paper.env",
			"InaccessiblePaths=/proc", "NoNewPrivileges=yes", "ProtectSystem=strict",
			"Restart=always", "RestartSec=15s",
			"MITHRIL_AGENT_KRAKEN_RATE_STATE=/run/mithril-agent-research/kraken-rate",
			"RuntimeDirectory=mithril-agent-research", "RuntimeDirectoryPreserve=yes",
			"PartOf=mithril-agent-paper-generation.target",
			"ConditionPathExists=/etc/mithril-agent/paper-active/portfolio.json",
			"ExecStart=/opt/mithril-hermes-research/run-paper-generation.sh observe sol " + role,
			"ReadOnlyPaths=/var/lib/mithril-agent-research/allocations",
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
	if !strings.Contains(challengerPath, "PathChanged=/etc/mithril-agent/paper-active/selection/sol/challenger/active.json") ||
		strings.Contains(challengerPath, "PathExists=") {
		t.Fatal("challenger path does not watch pointer changes safely")
	}
	championPath := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-champion.path")
	if !strings.Contains(championPath, "PathChanged=/etc/mithril-agent/paper-active/selection/sol/champion/active.json") ||
		strings.Contains(championPath, "PathExists=") {
		t.Fatal("champion path does not watch pointer changes safely")
	}
	championUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-champion.service")
	if !strings.Contains(championUnit, "ConditionPathExists=/etc/mithril-agent/paper-active/selection/sol/champion/active.json") {
		t.Fatal("champion service does not require its selected pointer")
	}
	marketScoutWrapper := readDocumentation(t, "../../deploy/hermes-research/run-market-scout.sh")
	for _, want := range []string{
		"systemctl is-active --quiet mithril-agent-paper-base.service",
		"systemctl is-active --quiet mithril-agent-paper-champion.service",
		"systemctl is-active --quiet mithril-agent-paper-jup.service",
		"systemctl is-active --quiet mithril-agent-paper-jup-champion.service",
		"systemctl is-active --quiet mithril-agent-paper-jup-challenger.path",
		"systemctl is-active --quiet mithril-agent-paper-jup-auto-select.timer",
		"finalizer_toolsets=\"${finalizer_toolsets:+$finalizer_toolsets,}mithril_paper_jup\"",
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
		"--dashboard-status /var/lib/mithril-agent-research/market-admission-%i/dashboard-status.json",
		"ReadWritePaths=/var/lib/mithril-agent-research/market-admission-%i",
		"ProtectSystem=strict", "UMask=0077",
	} {
		if !strings.Contains(candidateUnit, want) {
			t.Errorf("candidate admission collector is missing %q", want)
		}
	}
	autoSelectUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-auto-select.service")
	for _, want := range []string{
		"run-paper-generation.sh auto-select sol", "PrivateNetwork=yes", "RestrictAddressFamilies=AF_UNIX",
		"ReadOnlyPaths=/var/lib/mithril-agent-research/allocations",
		"ConditionPathIsDirectory=/var/lib/mithril-agent-research/outcomes",
		"ReadWritePaths=/etc/mithril-agent/paper-active/selection/sol/champion /etc/mithril-agent/paper-active/selection/sol/challenger",
		"/var/lib/mithril-agent-research/outcomes",
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
		"ConditionPathExists=/etc/mithril-agent/paper-active/sol-policy.json",
		"ConditionPathExists=!/etc/mithril-agent/paper-active/selection/sol/champion/active.json",
		"PrivateNetwork=yes", "RestrictAddressFamilies=AF_UNIX",
		"ExecStart=/opt/mithril-hermes-research/run-paper-generation.sh bootstrap sol",
		"ReadOnlyPaths=/var/lib/mithril-agent-research/allocations",
		"ReadWritePaths=/etc/mithril-agent/paper-active/selection/sol/champion /etc/mithril-agent/paper-active/selection/sol/challenger",
	} {
		if !strings.Contains(bootstrapUnit, want) {
			t.Errorf("paper bootstrap unit is missing %q", want)
		}
	}
	bootstrapScript := readDocumentation(t, "../../deploy/hermes-research/bootstrap-first-champion.sh")
	for _, want := range []string{
		"case \"$#\" in", "policy=/var/lib/mithril-agent-research/policy/policy.json",
		"journals=/var/lib/mithril-agent-research/journals", "policy=$1", "journals=$2",
		"champion=$3", "challenger=$4", `--dir "$journals"`, `--evidence-dir "$journals"`,
	} {
		if !strings.Contains(bootstrapScript, want) {
			t.Errorf("paper bootstrap script is missing %q", want)
		}
	}
	generationRunner := readDocumentation(t, "../../deploy/hermes-research/run-paper-generation.sh")
	for _, want := range []string{
		"generation=$(/usr/bin/readlink -e -- \"$selector\")",
		`exec 9<"$allocations"`, `/usr/bin/flock -s 9`,
		`[ "$(/usr/bin/dirname -- "$generation")" = "$allocations" ]`,
		`--policy "$policy" --dir "$runs/$role"`,
		`--portfolio "$portfolio" --portfolio-book "$market"`,
		`--candidate-pointer "$selection/$role/active.json"`,
		`"$policy" "$runs/base" "$selection/champion" "$selection/challenger"`,
		`"$generation/instruction.json"`,
		`--rollback-pointer "$selection/champion/previous.json"`,
		`--outcome-journal "$outcomes/$market.jsonl"`,
		`/usr/bin/touch -- "$status/champion-owned"`,
	} {
		if !strings.Contains(generationRunner, want) {
			t.Errorf("paper generation runner is missing %q", want)
		}
	}
	applyInstruction := readDocumentation(t, "../../deploy/hermes-research/apply-paper-instruction.sh")
	for _, want := range []string{
		`[ "$(/usr/bin/id -u)" -eq 0 ]`,
		"/usr/sbin/runuser -u mithril-agent-dashboard",
		`/usr/bin/install -d -o root -g mithril-agent-research -m 0710 "$runtime"`,
		`/usr/bin/chown mithril-agent-research:mithril-agent-research "$instruction"`,
		`/usr/bin/chmod 0400 "$instruction"`,
		`/usr/bin/install -d -o mithril-agent-research -g mithril-agent-research -m 0700 "$next"`,
		`/usr/sbin/runuser -u mithril-agent-research -- "$agent" shadow allocation`,
		`--portfolio "$current" --instruction "$instruction" --out-dir "$next"`,
		`/usr/bin/ln -- "$next/sol-policy.json" "$next/policy.json"`,
		`/usr/bin/cmp -s "$instruction" "$old/instruction.json"`,
		`generation_is_active "$old"`,
		`generation_stable=$((generation_stable + 1))`,
		`[ "$generation_stable" -lt 9 ] || return 0`,
		`start_generation "$old"`,
		`wait_for_generation "$old"`,
		`/usr/bin/systemctl stop "$target"`,
		`exec 8<"$runtime"`, `/usr/bin/flock -n 8`,
		`exec 9<"$allocations"`, `/usr/bin/flock -x 9`,
		`/usr/bin/mv -Tf -- "$temporary" "$selector"`,
		`/usr/bin/systemctl is-active --quiet "$generation_base"`,
		`/usr/bin/systemctl is-active --quiet "$generation_champion"`,
		`/usr/bin/systemctl is-active --quiet "$generation_pre"`,
		`/usr/bin/systemctl start "$generation_base" || true`,
		`/usr/bin/systemctl start "$generation_champion" || true`,
		`/usr/bin/systemctl start "$generation_pre" || true`,
		`start_generation "$next"`,
		`wait_for_generation "$next"`,
		`[ "$stopped" = true ] && [ -n "$old" ]`,
		`/usr/bin/find "$next" -xdev -depth -delete`,
		"restore_selector",
	} {
		if !strings.Contains(applyInstruction, want) {
			t.Errorf("paper instruction activator is missing %q", want)
		}
	}
	for _, forbidden := range []string{"rm -rf", "systemctl restart mithril", "systemctl stop mithril.service"} {
		if strings.Contains(applyInstruction, forbidden) {
			t.Errorf("paper instruction activator contains unsafe operation %q", forbidden)
		}
	}
	generationTarget := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-generation.target")
	for _, want := range []string{
		"ConditionPathExists=/etc/mithril-agent/paper-active/portfolio.json",
		"Wants=mithril-agent-paper-base.service",
		"Wants=mithril-agent-paper-pre-champion.service",
		"Wants=mithril-agent-paper-jup.service",
		"Wants=mithril-agent-paper-jup-pre-champion.service",
	} {
		if !strings.Contains(generationTarget, want) {
			t.Errorf("paper generation target is missing %q", want)
		}
	}
	instructionPath := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-instruction.path")
	if !strings.Contains(instructionPath, "PathChanged=/var/lib/mithril-agent-dashboard/instruction.json") ||
		strings.Contains(instructionPath, "PathExists=") {
		t.Fatal("paper instruction watcher does not watch atomic updates safely")
	}
	instructionUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-instruction.service")
	for _, want := range []string{
		"Conflicts=mithril-hermes-research.service",
		"Group=mithril-agent-research",
		"RuntimeDirectoryMode=0710",
		"ExecStart=/opt/mithril-hermes-research/apply-paper-instruction.sh",
		"ReadWritePaths=/var/lib/mithril-agent-research/allocations /etc/mithril-agent /run/mithril-agent-paper-instruction",
		"PrivateNetwork=yes", "ProtectSystem=strict",
	} {
		if !strings.Contains(instructionUnit, want) {
			t.Errorf("paper instruction unit is missing %q", want)
		}
	}
	if strings.Contains(instructionUnit, "InaccessiblePaths=/proc") {
		t.Fatal("paper instruction unit hides /proc from systemctl")
	}
	if strings.Contains(instructionUnit, "User=") {
		t.Fatal("root-owned paper selector is delegated to an unprivileged unit user")
	}
	if switched, moved := strings.Index(applyInstruction, "switched=true"), strings.LastIndex(applyInstruction, `/usr/bin/mv -Tf -- "$temporary" "$selector"`); switched < 0 || moved <= switched {
		t.Fatal("paper instruction activator does not arm rollback before moving the selector")
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
		"ConditionPathExists=/etc/mithril-agent/paper-active/sol-policy.json",
		"ConditionPathIsDirectory=/var/lib/mithril-agent-research/reports",
		"ConditionPathIsDirectory=/var/lib/mithril-agent-dashboard",
		"Type=oneshot", "UMask=0077",
		"RuntimeDirectory=mithril-hermes-research", "RuntimeDirectoryMode=0711",
		"iptables -C DOCKER-USER", "iptables -C INPUT",
		"ExecStart=/opt/mithril-hermes-research/run-market-scout.sh",
		"TimeoutStartSec=18min", "TimeoutStopSec=1min",
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
	solGate := strings.Index(researchRunner, "if [ \"$has_instruction\" = true ] &&\n  [ -f \"$sol_champion\" ]")
	jupGate := strings.Index(researchRunner, "if [ \"$has_instruction\" = true ] &&\n  [ -f \"$jup_champion\" ]")
	if solGate < 0 || jupGate <= solGate {
		t.Fatal("Hermes wrapper does not fail both adaptive paper toolsets closed without an instruction")
	}
	if !strings.Contains(researchRunner, "trap cleanup EXIT") ||
		!strings.Contains(researchRunner, "trap 'exit 1' HUP INT TERM") ||
		!strings.Contains(researchRunner, "/usr/bin/rm -f \"$runtime_instruction\"\nhas_instruction=false") {
		t.Fatal("Hermes wrapper does not remove a stale adaptive instruction before probing its canonical source")
	}
	for _, want := range []string{
		"research_toolsets='web,solana_docs,delegation'",
		"finalizer_toolsets=''",
		"allocations=/var/lib/mithril-agent-research/allocations",
		"selector=/etc/mithril-agent/paper-active",
		`exec 9<"$allocations"`, `/usr/bin/flock -s 9`,
		"generation=$(/usr/bin/readlink -e -- \"$selector\")",
		`[ "$(/usr/bin/dirname -- "$generation")" = "$allocations" ]`,
		"source_instruction=$generation/instruction.json",
		"sol_policy=$generation/sol-policy.json",
		"sol_journals=$generation/runs/sol/base",
		"sol_champion=$generation/selection/sol/champion/active.json",
		"jup_policy=$generation/jup-policy.json",
		"jup_journals=$generation/runs/jup/base",
		"jup_champion=$generation/selection/jup/champion/active.json",
		"finalizer_toolsets='mithril_paper'",
		"/var/lib/mithril-agent-research/index/events.jsonl",
		"index doctor", "--max-record-age 15m",
		"mithril_evidence=recently_ingested",
		"do not call it current chain state",
		"research_toolsets=\"$research_toolsets,mithril_index\"",
		"*,mithril_paper,*|*,mithril_paper_jup,*) exit 1",
		"*,delegation,*) exit 1",
		"export MITHRIL_HERMES_TOOLSETS=\"$research_toolsets\"",
		"/usr/sbin/runuser -u mithril-agent-research",
		"--export-instruction \"$source_instruction\" >\"$runtime_instruction\"",
		"--render-instruction \"$runtime_instruction\"",
		"research_query=/run/mithril-hermes-research/market-research.md",
		"finalizer_query=/run/mithril-hermes-research/challenger-finalizer.md",
		"research_state=/run/mithril-hermes-research/research-state",
		"/dev/null \"$research_state/.no-bundled-skills\"",
		"Trusted run-time anchors", "/usr/bin/date -u +%Y-%m-%dT%H:%M:%SZ",
		"/usr/bin/date -u -d '6 hours' +%Y-%m-%dT%H:%M:%SZ",
		"Copy both exact values; do not invent, round, reuse an older value, or calculate either timestamp.",
		"sol_diagnostics='{\"status\":\"prior_complete_day_unavailable\"}'",
		"jup_diagnostics='{\"status\":\"prior_complete_day_unavailable\"}'",
		"mithril-agent shadow review", "--days 1 --json",
		"Trusted sanitized prior-complete-day paper diagnostics.",
		"SOL/USDC: %s\\nJUP/USDC: %s",
		"shadow research-context",
		"sol_policy_context=",
		"jup_policy_context=",
		"Trusted current paper-strategy settings.",
		"copy the matching market values exactly",
		"current_paper_policy_unavailable",
		"if reviewed=$(/usr/sbin/runuser -u mithril-agent-research --",
		"sol_perps_status=/var/lib/mithril-agent-perps-paper/published/sol-paper-status.json",
		"btc_perps_status=/var/lib/mithril-agent-perps-paper/published/btc-paper-status.json",
		"eth_perps_status=/var/lib/mithril-agent-perps-paper/published/eth-paper-status.json",
		"perps_research='{\"status\":\"completed_perps_research_unavailable\"}'",
		"--render-perps-research \"SOL-PERP=$sol_perps_status\"",
		"--render-perps-research \"BTC-PERP=$btc_perps_status\"",
		"--render-perps-research \"ETH-PERP=$eth_perps_status\"",
		"Trusted content-hashed completed perps paper research.",
		"cannot authorize, promote, or execute anything",
		"finalizer_raw=/run/mithril-hermes-research/hermes-finalizer.raw",
		"packet=/run/mithril-hermes-research/research-state/packet.raw",
		"/usr/bin/docker compose run --rm --no-TTY hermes-research-parallel >/dev/null",
		"--sessions \"$session_export\" --extract-output \"$packet\"",
		"/usr/bin/rm -f \"$finalizer_raw\" \"$packet\"",
		"collect_research_packet() (", "set +e\n  collect_research_packet\n  result=$?\n  set -e",
		"Hermes pre-publication validation failed; retrying once with fresh state",
		`/usr/bin/find "$research_state" -mindepth 1 -xdev -depth -delete`,
		`/dev/null "$research_state/.no-bundled-skills"`,
		`/usr/bin/printf '%s\n%s\n' "$run_started" "$run_finished" >"$run_bounds"`,
		"/usr/bin/chmod 0644 \"$research_query\"",
		"export MITHRIL_HERMES_QUERY_FILE=\"$research_query\"",
		"--latest \"$validated_research\" >/dev/null",
		"sessions export --format jsonl --redact --yes",
		"/opt/mithril-hermes-research/build-research-evidence.py",
		"--sessions \"$session_export\" --packet \"$validated_research\"",
		"/var/lib/mithril-agent-research/evidence",
		"/var/lib/mithril-agent-dashboard/research-evidence.json",
		"if [ \"$packet_disposition\" = candidate ] && [ -n \"$finalizer_toolsets\" ]",
		"export MITHRIL_HERMES_TOOLSETS=\"$finalizer_toolsets\"",
		"/usr/bin/docker compose run --rm --no-TTY hermes-research >\"$finalizer_raw\"",
		"ulimit -f 128",
		"mithril-agent research packet-record", "--archive-dir /var/lib/mithril-agent-research/reports",
		"--latest \"$latest\"", "/var/lib/mithril-agent-dashboard/research.json",
		"/usr/bin/install -o mithril-agent-dashboard -g mithril-agent-dashboard -m 0600",
		"runuser -u mithril-agent-dashboard", "--in \"$dashboard_packet\" --latest \"$projection\"",
		"--sessions \"$dashboard_sessions\" --packet \"$projection\"",
	} {
		if !strings.Contains(researchRunner, want) {
			t.Errorf("Hermes market scout wrapper is missing %q", want)
		}
	}
	if strings.Contains(researchRunner, "research_raw=") || strings.Contains(researchRunner, "/^[[:space:]]*{/,$p") {
		t.Fatal("Hermes wrapper heuristically extracts JSON instead of validating its complete reply")
	}
	validatedEvidence := strings.Index(researchRunner, `--output "$research_evidence"`)
	retryStop := strings.Index(researchRunner, "collect_research_packet\n  result=$?")
	persistentArchive := strings.Index(researchRunner, `"$session_export" "$evidence_archive/`)
	if validatedEvidence < 0 || retryStop <= validatedEvidence || persistentArchive <= retryStop {
		t.Fatal("Hermes retry boundary is not between validation and persistent publication")
	}
	if sol := strings.Index(researchRunner, `--render-perps-research "SOL-PERP=$sol_perps_status"`); sol < 0 {
		t.Fatal("Hermes wrapper is missing SOL perps research")
	} else if btc := strings.Index(researchRunner, `--render-perps-research "BTC-PERP=$btc_perps_status"`); btc <= sol {
		t.Fatal("Hermes wrapper does not render BTC after SOL")
	} else if eth := strings.Index(researchRunner, `--render-perps-research "ETH-PERP=$eth_perps_status"`); eth <= btc {
		t.Fatal("Hermes wrapper does not render ETH after BTC")
	}
	if got := strings.Count(researchRunner, "--render-perps-research "); got != 3 {
		t.Fatalf("Hermes wrapper has %d perps research inputs; want 3", got)
	}
	for _, assignment := range []string{"sol_perps_status=", "btc_perps_status=", "eth_perps_status="} {
		if got := strings.Count(researchRunner, assignment); got != 1 {
			t.Errorf("Hermes wrapper has %d %s assignments; want 1", got, assignment)
		}
	}
	fallback := strings.Index(researchRunner, `perps_research='{"status":"completed_perps_research_unavailable"}'`)
	if fallback < 0 {
		t.Fatal("Hermes wrapper is missing the completed perps fallback")
	}
	perpsBlock := researchRunner[fallback:]
	render := strings.Index(perpsBlock, "if reviewed=$(/usr/sbin/runuser -u mithril-agent-research --")
	accept := strings.Index(perpsBlock, "then\n  perps_research=$reviewed\nfi")
	appendSummary := strings.Index(perpsBlock, `"$perps_research" >>"$research_query"`)
	if render < 0 || accept <= render || appendSummary <= accept ||
		strings.Count(perpsBlock, "perps_research=$reviewed") != 1 {
		t.Fatal("Hermes wrapper does not fail the completed perps summary closed as one unit")
	}
	for _, want := range []string{
		"outcome_feedback=${MITHRIL_HERMES_OUTCOME_FEEDBACK:-0}",
		`case "$outcome_feedback" in`, "0|1) ;;",
		"MITHRIL_HERMES_OUTCOME_FEEDBACK must be 0 or 1",
		"sol_outcome_journal=/var/lib/mithril-agent-research/outcomes/sol.jsonl",
		"jup_outcome_journal=/var/lib/mithril-agent-research/outcomes/jup.jsonl",
		`[ "$outcome_feedback" -eq 1 ]`,
		`outcome_journal_exists() {`,
		`for artifact in "$1" "$1.next" "$1.lock" "$1".seg-*; do`,
		`[ -e "$artifact" ] || [ -L "$artifact" ] || continue`,
		`outcome_journal_exists "$sol_outcome_journal"`,
		`outcome_journal_exists "$jup_outcome_journal"`,
		`--journal "$sol_outcome_journal" --prompt-safe --limit 8`,
		`--journal "$jup_outcome_journal" --prompt-safe --limit 8`,
		`--policy "$sol_policy" --max-age 168h`,
		`--policy "$jup_policy" --max-age 168h`,
		`[ -f "$jup_policy" ] && outcome_journal_exists "$jup_outcome_journal"`,
		`[ -n "$sol_outcome_history$jup_outcome_history" ]`,
		"internal advisory evidence, not an external source",
		"cannot authorize, activate, select, promote, or execute anything",
	} {
		if !strings.Contains(researchRunner, want) {
			t.Errorf("Hermes outcome feedback is missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`if sol_outcome_history=$(`, `if jup_outcome_history=$(`,
		`/usr/bin/cat "$sol_outcome_journal"`, `/usr/bin/cat "$jup_outcome_journal"`,
		`"$sol_outcome_journal" >>"$research_query"`,
		`"$jup_outcome_journal" >>"$research_query"`,
	} {
		if strings.Contains(researchRunner, forbidden) {
			t.Errorf("Hermes outcome feedback exposes raw journal input %q", forbidden)
		}
	}
	if strings.Contains(hermesUnit, "MITHRIL_HERMES_OUTCOME_FEEDBACK") {
		t.Fatal("shipped Hermes service enables optional outcome feedback")
	}
	for _, want := range []string{
		"Outcome feedback to the next\nHermes scout is disabled by default",
		"After direct operator approval",
		"Environment=MITHRIL_HERMES_OUTCOME_FEEDBACK=1",
		"staged `.next`, `.lock`, or `.seg-*` artifact is omitted",
		"verifies\nand folds the complete journal",
		"only then applies the limit",
		"incomplete, invalid, or future-dated state stops",
		"JUP outcomes are\nignored when the current allocation has no JUP policy",
	} {
		if !strings.Contains(deployReadme, want) {
			t.Errorf("Hermes deployment README is missing outcome operation rule %q", want)
		}
	}
	marketPrompt := readDocumentation(t, "../../deploy/hermes-research/prompts/market-scout.md")
	for _, want := range []string{
		"sanitized current-policy paper outcome history", "never an external source",
		"Do not infer omitted measurements or identifiers",
		"absent outcome-history block means that evidence is unavailable",
		"only\nauthoritative values for the `current` side",
		"do not infer its values and do not propose a candidate",
	} {
		if !strings.Contains(marketPrompt, want) {
			t.Errorf("Hermes market prompt is missing outcome safety rule %q", want)
		}
	}
	dashboardRecord := strings.Index(researchRunner, `--in "$dashboard_packet" --latest "$projection"`)
	dashboardEvidence := strings.Index(researchRunner, `--sessions "$dashboard_sessions" --packet "$projection"`)
	if dashboardRecord < 0 || dashboardEvidence < 0 || dashboardRecord > dashboardEvidence {
		t.Fatal("dashboard research packet is not validated before its retrieval evidence")
	}
	sessionExport := strings.Index(researchRunner, "sessions export --format jsonl --redact --yes")
	bindPacket := strings.Index(researchRunner, `--bind-output "$bound_packet"`)
	validatePacket := strings.Index(researchRunner, `--in "$bound_packet" --latest "$validated_research"`)
	if sessionExport < 0 || bindPacket <= sessionExport || validatePacket <= bindPacket {
		t.Fatal("Hermes source times are not host-bound before packet validation")
	}
	researchTimer := readDocumentation(t, "../../deploy/systemd/mithril-hermes-research.timer")
	for _, want := range []string{
		"OnCalendar=*-*-* *:15:00 UTC", "Persistent=true",
		"RandomizedDelaySec=5min", "AccuracySec=1min",
		"Unit=mithril-hermes-research.service", "WantedBy=timers.target",
	} {
		if !strings.Contains(researchTimer, want) {
			t.Errorf("Hermes market scout timer is missing %q", want)
		}
	}
	championUnit = readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-champion.service")
	preChampionUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-pre-champion.service")
	statusHandoffUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-status-handoff.service")
	bridgeUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-status-bridge.service")
	legacyTelegramUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-telegram.service")
	if strings.Contains(legacyTelegramUnit, "paper-status") {
		t.Fatal("legacy Telegram unit makes opt-in paper status mandatory")
	}
	const alertPath = "/etc/mithril-agent/paper-active/status/sol/alerts.json"
	if !strings.Contains(championUnit, "run-paper-generation.sh observe sol champion") ||
		!strings.Contains(preChampionUnit, "run-paper-generation.sh observe sol pre-champion") ||
		!strings.Contains(bridgeUnit, "LoadCredential=paper-status:"+alertPath) {
		t.Fatal("paper observer and Telegram bridge disagree on the alert snapshot path")
	}
	for _, want := range []string{
		"ConditionPathExists=/etc/mithril-agent/paper-active/selection/sol/champion/active.json",
		"ConditionPathExists=!/etc/mithril-agent/paper-active/status/sol/champion-owned",
		"ExecStart=/opt/mithril-hermes-research/run-paper-generation.sh status-handoff sol",
		"ReadWritePaths=/etc/mithril-agent/paper-active/status/sol",
	} {
		if !strings.Contains(statusHandoffUnit, want) {
			t.Errorf("SOL status handoff is missing %q", want)
		}
	}
	if !strings.Contains(bridgeUnit, "InaccessiblePaths=/var/lib/mithril-agent-research") {
		t.Fatal("paper status bridge can see the research tree outside its credential")
	}
	jupBase := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-jup.service")
	jupPreChampion := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-jup-pre-champion.service")
	jupChampion := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-jup-champion.service")
	jupChallenger := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-jup-challenger.service")
	jupChampionPath := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-jup-champion.path")
	jupChallengerPath := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-jup-challenger.path")
	jupBootstrap := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-jup-bootstrap.service")
	jupBootstrapTimer := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-jup-bootstrap.timer")
	jupAutoSelect := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-jup-auto-select.service")
	jupAutoSelectTimer := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-jup-auto-select.timer")
	jupStatusHandoff := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-jup-status-handoff.service")
	jupBridge := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-jup-status-bridge.service")
	jupSocket := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-jup-status.socket")
	telegramPaper := readDocumentation(t, "../../deploy/systemd/mithril-agent-telegram-paper.conf")
	const jupAlertPath = "/etc/mithril-agent/paper-active/status/jup/alerts.json"
	for name, unit := range map[string]string{
		"base": jupBase, "pre-champion": jupPreChampion,
		"champion": jupChampion, "challenger": jupChallenger,
	} {
		for _, want := range []string{
			"PartOf=mithril-agent-paper-generation.target",
			"ConditionPathExists=/etc/mithril-agent/paper-active/portfolio.json",
			"ExecStart=/opt/mithril-hermes-research/run-paper-generation.sh observe jup " + name,
			"ReadOnlyPaths=/var/lib/mithril-agent-research/allocations",
		} {
			if !strings.Contains(unit, want) {
				t.Errorf("JUP %s observer is missing %q", name, want)
			}
		}
	}
	for _, want := range []string{
		"ConditionPathExists=/etc/mithril-agent/paper-active/jup-policy.json",
		"run-paper-generation.sh observe jup base",
		"ReadWritePaths=/etc/mithril-agent/paper-active/runs/jup/base",
	} {
		if !strings.Contains(jupBase, want) {
			t.Errorf("JUP base observer is missing %q", want)
		}
	}
	if strings.Contains(jupBase, "--alert-status") || strings.Contains(jupBase, "/status/jup") {
		t.Fatal("JUP base observer still publishes operator status")
	}
	for _, want := range []string{
		"ConditionPathExists=!/etc/mithril-agent/paper-active/selection/jup/champion/active.json",
		"Conflicts=mithril-agent-paper-jup-champion.service",
		"run-paper-generation.sh observe jup pre-champion",
		"ReadWritePaths=/etc/mithril-agent/paper-active/runs/jup/pre-champion /etc/mithril-agent/paper-active/status/jup",
	} {
		if !strings.Contains(jupPreChampion, want) {
			t.Errorf("JUP pre-champion status observer is missing %q", want)
		}
	}
	if strings.Contains(jupPreChampion, "--candidate-pointer") ||
		strings.Contains(jupPreChampion, "ReadWritePaths=/var/lib/mithril-agent-research/jup") {
		t.Fatal("JUP pre-champion status observer can select or rewrite lifecycle state")
	}
	for _, want := range []string{
		"ConditionPathExists=/etc/mithril-agent/paper-active/selection/jup/champion/active.json",
		"Requires=mithril-agent-paper-jup-status-handoff.service",
		"Conflicts=mithril-agent-paper-jup-pre-champion.service",
		"run-paper-generation.sh observe jup champion",
		"ReadWritePaths=/etc/mithril-agent/paper-active/runs/jup/champion /etc/mithril-agent/paper-active/status/jup",
	} {
		if !strings.Contains(jupChampion, want) {
			t.Errorf("JUP champion observer is missing %q", want)
		}
	}
	for _, want := range []string{
		"ConditionPathExists=/etc/mithril-agent/paper-active/selection/jup/challenger/active.json",
		"run-paper-generation.sh observe jup challenger",
		"ReadWritePaths=/etc/mithril-agent/paper-active/runs/jup/challenger",
	} {
		if !strings.Contains(jupChallenger, want) {
			t.Errorf("JUP challenger observer is missing %q", want)
		}
	}
	if strings.Contains(jupChallenger, "--alert-status") ||
		strings.Contains(jupChampion, "ReadWritePaths=/var/lib/mithril-agent-research/jup/champion") ||
		strings.Contains(jupChallenger, "ReadWritePaths=/var/lib/mithril-agent-research/jup/challenger") {
		t.Fatal("JUP paper observers can write status or lifecycle pointers they do not own")
	}
	if !strings.Contains(jupChampionPath, "PathChanged=/etc/mithril-agent/paper-active/selection/jup/champion/active.json") ||
		strings.Contains(jupChampionPath, "PathExists=") ||
		!strings.Contains(jupChallengerPath, "PathChanged=/etc/mithril-agent/paper-active/selection/jup/challenger/active.json") ||
		strings.Contains(jupChallengerPath, "PathExists=") {
		t.Fatal("JUP lifecycle paths do not watch pointer changes safely")
	}
	for _, want := range []string{
		"run-paper-generation.sh bootstrap jup",
		"ReadOnlyPaths=/var/lib/mithril-agent-research/allocations",
		"ReadWritePaths=/etc/mithril-agent/paper-active/selection/jup/champion /etc/mithril-agent/paper-active/selection/jup/challenger",
		"PrivateNetwork=yes", "RestrictAddressFamilies=AF_UNIX",
	} {
		if !strings.Contains(jupBootstrap, want) {
			t.Errorf("JUP bootstrap unit is missing %q", want)
		}
	}
	for _, want := range []string{
		"run-paper-generation.sh auto-select jup",
		"ReadOnlyPaths=/var/lib/mithril-agent-research/allocations",
		"ConditionPathIsDirectory=/var/lib/mithril-agent-research/outcomes",
		"ReadWritePaths=/etc/mithril-agent/paper-active/selection/jup/champion /etc/mithril-agent/paper-active/selection/jup/challenger",
		"/var/lib/mithril-agent-research/outcomes",
		"PrivateNetwork=yes", "RestrictAddressFamilies=AF_UNIX",
	} {
		if !strings.Contains(jupAutoSelect, want) {
			t.Errorf("JUP auto-selector is missing %q", want)
		}
	}
	if !strings.Contains(jupBootstrapTimer, "Unit=mithril-agent-paper-jup-bootstrap.service") ||
		!strings.Contains(jupAutoSelectTimer, "Unit=mithril-agent-paper-jup-auto-select.service") {
		t.Fatal("JUP lifecycle timers do not target their services")
	}
	for _, want := range []string{
		"ConditionPathExists=/etc/mithril-agent/paper-active/selection/jup/champion/active.json",
		"ConditionPathExists=!/etc/mithril-agent/paper-active/status/jup/champion-owned",
		"run-paper-generation.sh status-handoff jup",
		"ReadWritePaths=/etc/mithril-agent/paper-active/status/jup",
		"PrivateNetwork=yes", "RestrictAddressFamilies=AF_UNIX",
	} {
		if !strings.Contains(jupStatusHandoff, want) {
			t.Errorf("JUP status handoff is missing %q", want)
		}
	}
	if !strings.Contains(jupBridge, "LoadCredential=paper-status:"+jupAlertPath) ||
		!strings.Contains(jupBridge, "After=mithril-agent-paper-jup-status.socket mithril-agent-paper-jup-pre-champion.service mithril-agent-paper-jup-champion.service") ||
		!strings.Contains(jupBridge, "InaccessiblePaths=/var/lib/mithril-agent-research") ||
		!strings.Contains(jupSocket, "ListenStream=/run/mithril-agent-paper-jup-status.sock") {
		t.Fatal("JUP champion status bridge/socket does not preserve the bounded credential path")
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
	marketCandidateUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-market-candidate@.service")
	for _, want := range []string{
		"ConditionPathExists=/etc/mithril-agent/paper-wif.env",
		"--market WIF/USDC --observe ${MITHRIL_AGENT_WIF_OBSERVE}",
		"--journal /var/lib/mithril-agent-research/market-admission-wif/evidence.jsonl",
		"--dashboard-status /var/lib/mithril-agent-research/market-admission-wif/dashboard-status.json",
		"ReadWritePaths=/var/lib/mithril-agent-research/market-admission-wif",
		"ProtectSystem=strict", "UMask=0077",
	} {
		if !strings.Contains(wifUnit, want) {
			t.Errorf("WIF admission collector is missing %q", want)
		}
	}
	if !strings.Contains(marketCandidateUnit, "Conflicts=mithril-agent-market-%i.service") {
		t.Error("templated market collector does not conflict with the legacy per-market unit")
	}
	dashboardUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-dashboard.service")
	dashboardSocket := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-dashboard.socket")
	dashboardSysusers := readDocumentation(t, "../../deploy/sysusers/mithril-agent-dashboard.conf")
	marketStatusUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-market-status.service")
	marketStatusTimer := readDocumentation(t, "../../deploy/systemd/mithril-agent-market-status.timer")
	marketPaperUnit := readDocumentation(t, "../../deploy/systemd/mithril-agent-market-paper@.service")
	marketPaperSocket := readDocumentation(t, "../../deploy/systemd/mithril-agent-market-paper-status@.socket")
	marketPaperBridge := readDocumentation(t, "../../deploy/systemd/mithril-agent-market-paper-status-bridge@.service")
	for _, want := range []string{
		"User=mithril-agent-dashboard", "SupplementaryGroups=mithril-agent-status",
		"PrivateNetwork=yes", "RestrictAddressFamilies=AF_UNIX",
		"InaccessiblePaths=/var/lib/mithril-agent",
		"SOL/USDC=/run/mithril-agent-paper-status.sock",
		"JUP/USDC=/run/mithril-agent-paper-jup-status.sock",
		"--research-packet-path /var/lib/mithril-agent-dashboard/research.json",
		"--mithril-evidence-status-path /var/lib/mithril-agent-dashboard/mithril-evidence.json",
		"--market-admission-status-path /var/lib/mithril-agent-dashboard/market-admission.json",
	} {
		if !strings.Contains(dashboardUnit, want) {
			t.Errorf("paper dashboard unit is missing %q", want)
		}
	}
	for name, unit := range map[string]string{"paper dashboard": dashboardUnit, "paper Telegram": telegramPaper} {
		for _, forbidden := range []string{"WIF/USDC=/run/mithril-agent-market", "JTO/USDC=/run/mithril-agent-market", "PYTH/USDC=/run/mithril-agent-market"} {
			if strings.Contains(unit, forbidden) {
				t.Errorf("%s exposes an unadmitted market through %q", name, forbidden)
			}
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
		"ConditionPathExists=/etc/mithril-agent/market-paper-%i.env",
		"Type=notify", "NotifyAccess=main",
		"StateDirectory=mithril-agent-market-paper-%i", "Restart=no", "RuntimeMaxSec=24h",
		"shadow run --policy ${MITHRIL_AGENT_PAPER_POLICY}",
		"--portfolio ${MITHRIL_AGENT_PAPER_PORTFOLIO} --portfolio-book %i",
		"--provisional-artifact ${MITHRIL_AGENT_PAPER_ARTIFACT}",
		"--provisional-journal /var/lib/mithril-agent-research/market-admission-%i/evidence.jsonl",
		"--paper-check-artifact ${MITHRIL_AGENT_PAPER_CHECK}",
		"--alert-status /var/lib/mithril-agent-market-paper-%i/alerts.json",
		"ReadOnlyPaths=/var/lib/mithril-agent-research/market-admission-%i",
		"ReadWritePaths=/var/lib/mithril-agent-market-paper-%i",
		"NoNewPrivileges=yes", "CapabilityBoundingSet=", "ProtectSystem=strict",
	} {
		if !strings.Contains(marketPaperUnit, want) {
			t.Errorf("provisional market paper runner is missing %q", want)
		}
	}
	for _, forbidden := range []string{"candidate-pointer", "--signer", "--wallet", "--submit"} {
		if strings.Contains(marketPaperUnit, forbidden) {
			t.Errorf("provisional market paper runner contains unsafe capability %q", forbidden)
		}
	}
	for _, want := range []string{
		"ListenStream=/run/mithril-agent-market-%i-paper-status.sock",
		"SocketGroup=mithril-agent-status", "SocketMode=0660",
		"Service=mithril-agent-market-paper-status-bridge@%i.service", "Accept=no",
	} {
		if !strings.Contains(marketPaperSocket, want) {
			t.Errorf("provisional market paper socket is missing %q", want)
		}
	}
	for _, want := range []string{
		"LoadCredential=paper-status:/var/lib/mithril-agent-market-paper-%i/alerts.json",
		"mithril-agent-paper-status-bridge --credential paper-status",
		"PrivateNetwork=yes", "RestrictAddressFamilies=AF_UNIX",
		"InaccessiblePaths=/var/lib/mithril-agent-research",
	} {
		if !strings.Contains(marketPaperBridge, want) {
			t.Errorf("provisional market paper bridge is missing %q", want)
		}
	}
	for _, want := range []string{
		"User=mithril-agent-dashboard", "Group=mithril-agent-dashboard",
		"StateDirectory=mithril-agent-dashboard", "StateDirectoryMode=0700",
		"LoadCredential=market-wif-status:/var/lib/mithril-agent-research/market-admission-wif/dashboard-status.json",
		"LoadCredential=market-jto-status:/var/lib/mithril-agent-research/market-admission-jto/dashboard-status.json",
		"LoadCredential=market-pyth-status:/var/lib/mithril-agent-research/market-admission-pyth/dashboard-status.json",
		"ExecStart=/usr/local/libexec/mithril-agent/mithril-agent-paper-dashboard --record-market-admission /var/lib/mithril-agent-dashboard/market-admission.json",
		"InaccessiblePaths=-/var/lib/mithril-agent-research",
		"PrivateNetwork=yes", "RestrictAddressFamilies=AF_UNIX",
	} {
		if !strings.Contains(marketStatusUnit, want) {
			t.Errorf("market status publisher is missing %q", want)
		}
	}
	for _, forbidden := range []string{"EnvironmentFile=", "ReadWritePaths=", "AF_INET", "--listen"} {
		if strings.Contains(marketStatusUnit, forbidden) {
			t.Errorf("market status publisher contains unsafe capability %q", forbidden)
		}
	}
	for _, want := range []string{
		"OnCalendar=*-*-* *:*:00 UTC", "Persistent=true", "AccuracySec=1s",
		"Unit=mithril-agent-market-status.service", "WantedBy=timers.target",
	} {
		if !strings.Contains(marketStatusTimer, want) {
			t.Errorf("market status timer is missing %q", want)
		}
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
		"mithril-agent-market-status.timer",
		"systemctl restart mithril-agent-market-candidate@wif.service",
		"mithril-agent-market-wif.service` must remain disabled",
		"Market-admission v4 changes the source-alignment contract",
		"archive/market-admission-v3-$STAMP", "This is an evidence rotation, not deletion",
		"systemd credentials",
		"cannot traverse",
		"PUMP remains excluded", "Token-2022",
		"shadow market paper-check", "first 80 minutes", "final 40 minutes",
		"code-owned 25 bps", "50 bps stress", "--result-out", "--candidate-policy-out",
		"MITHRIL_AGENT_PAPER_CHECK=$MARKET_DIR/paper-check-$STAMP.json",
		"readiness notification",
		"--dashboard-status \"$MARKET_DIR/dashboard-status.json\"",
		"checked-policy-$STAMP.json", "writes no candidate policy",
		"trap 'sudo systemctl start mithril-agent-market-candidate@wif.service' EXIT",
		"deploy/systemd/mithril-agent-market-status.service",
		"deploy/systemd/mithril-agent-market-status.timer",
		"deploy/systemd/mithril-agent-telegram-paper.conf",
		"Result=success", "ExecMainStatus=0", "valid but stale snapshot",
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
	qualificationStart := strings.Index(deployReadme, "set -e\nSTAMP=")
	if qualificationStart < 0 {
		t.Fatal("Hermes deployment README omits the guarded short qualification workflow")
	}
	qualification := deployReadme[qualificationStart:]
	checkAt := strings.Index(qualification, "shadow market paper-check")
	restartAfterCheck := -1
	if checkAt >= 0 {
		restartAfterCheck = strings.Index(
			qualification[checkAt:], "\nsudo systemctl start mithril-agent-market-candidate@wif.service",
		)
	}
	earlyRestart := checkAt > 0 && strings.Contains(
		qualification[:checkAt], "\nsudo systemctl start mithril-agent-market-candidate@wif.service",
	)
	if checkAt < 0 || restartAfterCheck < 0 || earlyRestart {
		t.Fatal("short qualification restarts the collector before paper-check completes")
	}
	collectorProvision := strings.Index(deployReadme, "systemctl enable --now \\\n  mithril-agent-market-candidate@wif.service")
	firstDiagnostic := strings.Index(deployReadme, "shadow market diagnose")
	if collectorProvision < 0 || firstDiagnostic < 0 || collectorProvision > firstDiagnostic {
		t.Fatal("market collectors are not provisioned before diagnostic and qualification commands")
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

func TestHermesIndexGateRequiresMainnetIdentity(t *testing.T) {
	runner := readDocumentation(t, "../../deploy/hermes-research/run-market-scout.sh")
	start := strings.Index(runner, "if [ -f /var/lib/mithril-agent-research/index/events.jsonl ] &&")
	if start < 0 {
		t.Fatal("Hermes index gate is missing")
	}
	end := strings.Index(runner[start:], "\nfi")
	if end < 0 {
		t.Fatal("Hermes index gate is incomplete")
	}
	block := runner[start : start+end+3]
	block = strings.Replace(block, "[ -f /var/lib/mithril-agent-research/index/events.jsonl ]", "true", 1)
	doctorStart := strings.Index(block, "index_status=$(") + len("index_status=$(")
	doctorEnd := strings.Index(block, ") &&")
	if doctorStart < len("index_status=$(") || doctorEnd < doctorStart ||
		!strings.Contains(block[doctorStart:doctorEnd], "--max-record-age 15m --json") {
		t.Fatal("Hermes index gate does not capture a fresh JSON doctor result")
	}
	block = block[:doctorStart] + `/usr/bin/printf '%s' "$1"; exit "$2"` + block[doctorEnd:]
	valid := `{"ready":true,"index":{"source":{"cluster":"mainnet-beta","genesis_hash":"5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d"}}}`
	for _, test := range []struct {
		status, exitCode, want string
	}{
		{valid, "0", "recently_ingested"},
		{valid, "1", "unavailable"},
		{strings.Replace(valid, "mainnet-beta", "devnet", 1), "0", "unavailable"},
		{strings.Replace(valid, "mainnet-beta", "testnet", 1), "0", "unavailable"},
		{strings.Replace(valid, "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d", "wrong", 1), "0", "unavailable"},
		{strings.Replace(valid, "true", "false", 1), "0", "unavailable"},
		{`{"ready":true,"index":{}}`, "0", "unavailable"},
		{`not json`, "0", "unavailable"},
	} {
		script := "set -eu\nresearch_toolsets=web\nmithril_evidence=unavailable\n" + block +
			"\nprintf '%s' \"$mithril_evidence\"\n"
		output, err := exec.Command("/bin/sh", "-c", script, "test", test.status, test.exitCode).CombinedOutput()
		if err != nil || string(output) != test.want {
			t.Fatalf("doctor %q exit %s: got %q, %v; want %q", test.status, test.exitCode, output, err, test.want)
		}
	}
}

func TestHermesFinalizerSkipsNonCandidates(t *testing.T) {
	runner := readDocumentation(t, "../../deploy/hermes-research/run-market-scout.sh")
	line := func(prefix string) string {
		t.Helper()
		for _, value := range strings.Split(runner, "\n") {
			if strings.HasPrefix(value, prefix) {
				return value
			}
		}
		t.Fatalf("Hermes wrapper is missing %q", prefix)
		return ""
	}
	parser := line("packet_disposition=$(")
	gate := line(`if [ "$packet_disposition"`)
	script := "set -eu\npacket_receipt=$1\nfinalizer_toolsets=$2\n" + parser + "\n" + gate +
		"\nprintf finalize\nelse\nprintf skip\nfi\n"
	for _, test := range []struct {
		receipt, toolsets, want string
	}{
		{`{"disposition":"candidate"}`, "mithril_paper", "finalize"},
		{`{"disposition":"candidate"}`, "", "skip"},
		{`{"hypothesis_id":"candidate","disposition":"no_change"}`, "mithril_paper", "skip"},
		{`{"disposition": "blocked"}`, "mithril_paper_jup", "skip"},
		{`{"disposition":"invented"}`, "mithril_paper", ""},
		{`{}`, "mithril_paper", ""},
		{`not json`, "mithril_paper", ""},
	} {
		output, err := exec.Command("/bin/sh", "-c", script, "test", test.receipt, test.toolsets).Output()
		if test.want == "" {
			if err == nil || len(output) != 0 {
				t.Fatalf("invalid receipt reached dispatch: %q, %q, %v", test.receipt, output, err)
			}
		} else if err != nil || string(output) != test.want {
			t.Fatalf("receipt %q with tools %q: got %q, %v; want %q", test.receipt, test.toolsets, output, err, test.want)
		}
	}
}

func TestHermesOutcomeFeedbackRecognizesInterruptedRotation(t *testing.T) {
	runner := readDocumentation(t, "../../deploy/hermes-research/run-market-scout.sh")
	start := strings.Index(runner, "outcome_journal_exists() {")
	if start < 0 {
		t.Fatal("Hermes outcome journal existence gate is missing")
	}
	end := strings.Index(runner[start:], "\n}\n")
	if end < 0 {
		t.Fatal("Hermes outcome journal existence gate is missing")
	}
	helper := runner[start : start+end+2]
	directory := t.TempDir()
	journal := filepath.Join(directory, "sol.jsonl")
	run := func() error {
		return exec.Command("/bin/sh", "-c", helper+"\noutcome_journal_exists \"$1\"", "test", journal).Run()
	}
	if err := run(); err == nil {
		t.Fatal("a truly absent logical journal was reported present")
	}
	for _, suffix := range []string{".next", ".lock", ".seg-000001"} {
		if err := os.WriteFile(journal+suffix, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := run(); err != nil {
			t.Errorf("logical journal artifact %s was omitted: %v", suffix, err)
		}
		if err := os.Remove(journal + suffix); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHermesJUPResearchContextFailureKeepsTheMarketUnavailable(t *testing.T) {
	runner := readDocumentation(t, "../../deploy/hermes-research/run-market-scout.sh")
	start := strings.Index(runner, `jup_policy_context='{"status":"current_paper_policy_unavailable"`)
	end := strings.Index(runner, "perps_research=")
	if start < 0 || end <= start {
		t.Fatal("Hermes wrapper is missing the bounded JUP context block")
	}
	block := runner[start:end]
	for _, want := range []string{
		`if [ -f "$jup_policy" ]; then`,
		`if reviewed=$(/usr/sbin/runuser -u mithril-agent-research --`,
		`--policy "$jup_policy" 2>/dev/null); then`,
		`jup_policy_context=$reviewed`,
	} {
		if !strings.Contains(block, want) {
			t.Errorf("JUP context fallback is missing %q", want)
		}
	}
	if strings.Contains(block, `jup_policy_context=$(/usr/sbin/runuser`) {
		t.Fatal("JUP context extraction can still abort the whole research run")
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
