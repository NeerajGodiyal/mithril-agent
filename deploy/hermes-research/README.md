# Nous Hermes research profile

This deploys Nous Research Hermes Agent as a research process with one bounded
paper-only write: CLI and cron sessions may create an
immutable challenger and update its dedicated challenger pointer. It can also
read the local rooted index, current paper challenge state, official Solana
documentation, and public web sources. It cannot change the
champion or a live policy, build, sign, submit, authorize, or promote a
transaction or strategy. Hermes Telegram is disabled; the deterministic
Mithril Telegram service remains the only operator notification path.

The image is pinned to tag `v2026.8.27` and OCI index digest
`sha256:e0df6adebddf29b91112aefc999d4aaf6846c9eb544faca5672a16a13590ff79`.
Do not replace it with `latest` or a tag without this digest. Hermes
configuration allowlists are defense in depth; the whole container or VM is
the security boundary.

All three MCP entries deliberately use `trust: full`. Pinned Hermes has an
[open read-only annotation bug](https://github.com/NousResearch/hermes-agent/issues/88858):
it reads the Python MCP annotation by its wire-format name, so read-only tools
on an untrusted server enter the interactive approval path. That path can wait
up to 300 seconds in this noninteractive profile. The paper server also has one
intentional write, bounded challenger creation, which cannot run unattended
under `trust: untrusted` even after the annotation bug is fixed.

In this Hermes release, `full` removes the per-call approval gate for a server;
it does not add tools, mounts, credentials, network access, or signer authority.
The effective boundary remains the exact include filters, the post-filter
registry assertion proving exactly 7 MCP tools, the platform toolsets, and the
container mounts. Any newly included tool would inherit unattended approval,
so do not add or rename a server or tool without reviewing the resulting
authority. On every Hermes upgrade, recheck the upstream issue and repeat the
closed-stdin unattended canary. Even when the read-only bug is fixed, the paper
write still needs an explicit unattended authorization path; do not silently
change this profile back to an approval mode that makes cron hang.

## Host boundary

Use a dedicated host or container account and a dedicated `state/` directory.
Hermes writes its own profile, session, cron, and web-result cache beneath that
state directory; the container itself is not filesystem-read-only.
Mount only:

- the pinned Linux `mithril-agent` binary, read-only;
- the rooted index directory, read-only; and
- the paper policy, completed journals, champion, and paired run evidence,
  read-only;
- one dedicated challenger-control directory, read-write.

The Compose bind mounts deliberately set `create_host_path: false`. A typo or
missing source therefore stops startup instead of silently creating a directory
where the Mithril executable or live data was expected.

Never mount the source checkout, Docker socket, wallet/key files, Turnkey
credentials, live configuration root, runner/signer/risk/submitter sockets, or
a Telegram user-account session. Publish no ports and do not use host networking.
For rootful Docker, deny container access to the host gateway, private/ULA and
link-local networks, and cloud metadata addresses in the Docker forwarding
firewall, then canary only OpenAI Codex OAuth/inference, the keyless web ring,
and `mcp.solana.com`.
Hermes URL blocking does not sandbox MCP subprocess networking.
The tracked `mithril-hermes-research-egress.service` applies that deny policy to
the dedicated `172.30.77.0/28` Docker bridge on every boot and rejects every
connection from that bridge to the Docker host itself. Do not start a Hermes
container on another network.

Prompts and bounded Mithril/index tool results used in a conversation are sent
to OpenAI through Hermes' Codex subscription provider. Search and extraction
queries are also sent to whichever public keyless-ring vendor serves the call.
The profile remains unsuitable for secrets or private custody data, which must
never be mounted or pasted into it.

Do not configure a Bitwarden, 1Password, or plugin secret source in this
profile. Hermes v2026.8.27 forwards variables from external secret sources to
every stdio MCP subprocess. The ordinary model and Telegram variables in the
gateway environment are not inherited by these MCP children.

`MITHRIL_UID` and `MITHRIL_GID` must be the decimal UID and primary GID, from 1
through 65534, of the dedicated no-login `mithril-agent-research` identity.
The research identity remains the owner of its mode-`0700` data. Mithril's
protected-index checks validate ownership rather than ACL readability. If the
live index has another owner, export a read-only projection owned by this
research UID instead of broadening access.

That no-login identity must never join the Docker group or access a rootful
Docker socket; Docker documents that group as root-level authority. Have a
root/admin launch the reviewed rootful Compose profile, or use a genuinely
rootless Docker installation and verify `docker info` reports rootless mode.
The Compose CPU, memory, process, and rotating-log limits are part of the
reviewed boundary, not proof that model output is safe.

## Provision

First have the host administrator create the fixed paper tree with mode `0700`
and ownership set to the dedicated research UID/GID. Do not make it
group/world-readable. A root/admin must run the reviewed rootful Compose
commands; the no-login data identity only owns its bounded files. With verified
rootless Docker, its unprivileged runtime owner may run them instead:

```sh
sudo install -m 0644 deploy/sysusers/mithril-agent-status.conf \
  /usr/lib/sysusers.d/mithril-agent-status.conf
sudo systemd-sysusers /usr/lib/sysusers.d/mithril-agent-status.conf
sudo install -d -o mithril-agent-research -g mithril-agent-research -m 0700 \
  /var/lib/mithril-agent-research \
  /var/lib/mithril-agent-research/index \
  /var/lib/mithril-agent-research/runs \
  /var/lib/mithril-agent-research/status \
  /var/lib/mithril-agent-research/{policy,journals,champion,challenger} \
  /var/lib/mithril-agent-research/challenger/candidates \
  /var/lib/mithril-agent-research/runs/{champion,challenger} \
  /var/lib/mithril-agent-research/status/champion
```

Before validating or enabling Hermes, populate that fixed `index/` directory
with a completed rooted-event ingest using the producer and recovery procedure
in [`INDEXING.md`](../../INDEXING.md). Run the `mithril-agent index ingest`
side of the supervisor-owned pipe as `mithril-agent-research`, keep the Mithril
AccountsDB private to its existing node identity, and use the permanent cluster,
genesis hash, and account/owner/mention filter selected for this research
profile. Do not copy or hand-edit `events.jsonl`. The final command must pass
before Compose is started:

```sh
sudo -u mithril-agent-research /usr/local/libexec/mithril-agent/mithril-agent \
  index doctor --dir /var/lib/mithril-agent-research/index
```

The systemd unit deliberately remains ineligible until that verified index has
an `events.jsonl`; an empty provisioned directory is not research evidence.

For rootful Docker, copy the reviewed deployment inputs into a root-owned
directory before running Compose. Running root-equivalent Compose from a
developer-writable checkout would let a later checkout edit replace its image,
command, or mounts. Re-stage and review these exact files for every upgrade:

```sh
sudo install -d -o root -g root -m 0755 \
  /opt/mithril-hermes-research \
  /opt/mithril-hermes-research/prompts \
  /opt/mithril-hermes-research/systemd
sudo install -o root -g root -m 0644 \
  deploy/hermes-research/compose.yaml \
  deploy/hermes-research/config.yaml \
  deploy/hermes-research/SOUL.md \
  /opt/mithril-hermes-research/
sudo install -o root -g root -m 0755 \
  deploy/hermes-research/check-network.sh \
  /opt/mithril-hermes-research/check-network.sh
sudo install -o root -g root -m 0644 \
  deploy/hermes-research/prompts/market-scout.md \
  /opt/mithril-hermes-research/prompts/market-scout.md
sudo install -o root -g root -m 0644 \
  deploy/systemd/mithril-agent-paper-base.service \
  deploy/systemd/mithril-agent-paper-champion.service \
  deploy/systemd/mithril-agent-paper-challenger.service \
  deploy/systemd/mithril-agent-paper-challenger.path \
  deploy/systemd/mithril-hermes-research-egress.service \
  deploy/systemd/mithril-hermes-research.service \
  /opt/mithril-hermes-research/systemd/
sudo install -d -o mithril-agent-research -g mithril-agent-research -m 0700 \
  /opt/mithril-hermes-research/state
sudo install -o root -g root -m 0600 \
  deploy/hermes-research/env.example /opt/mithril-hermes-research/.env
sudo install -o mithril-agent-research -g mithril-agent-research -m 0600 \
  /dev/null /opt/mithril-hermes-research/state/.no-bundled-skills
sudo install -o mithril-agent-research -g mithril-agent-research -m 0600 \
  deploy/hermes-research/config.yaml \
  /opt/mithril-hermes-research/state/config.yaml
sudo install -o mithril-agent-research -g mithril-agent-research -m 0600 \
  deploy/hermes-research/SOUL.md /opt/mithril-hermes-research/state/SOUL.md
cd /opt/mithril-hermes-research
```

Create the fixed bridge once, install its firewall unit, and verify both the
subnet and active deny chain before any OAuth or web call:

```sh
sudo docker network create --driver bridge --subnet 172.30.77.0/28 \
  mithril-hermes-research
test "$(sudo docker network inspect mithril-hermes-research \
  --format '{{(index .IPAM.Config 0).Subnet}}')" = 172.30.77.0/28
sudo install -o root -g root -m 0644 \
  systemd/mithril-hermes-research-egress.service \
  systemd/mithril-hermes-research.service \
  /etc/systemd/system/
sudo systemd-analyze verify \
  /etc/systemd/system/mithril-hermes-research-egress.service \
  /etc/systemd/system/mithril-hermes-research.service
sudo systemctl daemon-reload
sudo systemctl enable mithril-hermes-research-egress.service
sudo systemctl restart mithril-hermes-research-egress.service
sudo iptables -C DOCKER-USER -s 172.30.77.0/28 -j MITHRIL_HERMES
sudo iptables -C INPUT -s 172.30.77.0/28 -j REJECT
```

The network has IPv4 only. Its dedicated forwarding chain rejects loopback,
carrier-grade NAT, RFC1918, and link-local/cloud-metadata destinations; its
INPUT rule rejects every route back into the Docker host, including the host's
public address. All other forwarded egress continues to the public providers.
The preflight checks the bridge driver, external/internal mode, IPv6 state,
IPAM count, and exact subnet on every start. A failed unit or absent rule is a
failed deployment.

The bind-mounted agent must also be a reviewed, immutable installation rather
than a binary from a developer-writable build tree. Verify its ownership and
the exact release digest before every rootful Compose start, replacing
`REVIEWED_SHA256` with the digest recorded for the accepted build:

```sh
test "$(stat -Lc '%U:%G %a' /usr/local/libexec/mithril-agent/mithril-agent)" = \
  'root:root 755'
printf '%s  %s\n' REVIEWED_SHA256 \
  /usr/local/libexec/mithril-agent/mithril-agent | sha256sum -c -
```

For genuine rootless Docker, use an equivalent private staging directory owned
by the rootless runtime owner; that owner must own/read `.env`. Do not run the
rootful commands from a writable checkout and do not make `.env` group or world
readable.

Fill `.env`: use `id -u mithril-agent-research` for `MITHRIL_UID` and
`id -g mithril-agent-research` for `MITHRIL_GID`.
Both values must contain decimal digits only. Do not add a Telegram token to
this container. Paper alerts continue through the separately supervised,
deterministic Mithril Telegram service.
The agent binary, index, policy, journals, champion, challenger, and run-tree
sources are literal paths in `compose.yaml`; `.env` cannot redirect them after
the systemd preflights validate those paths.

Install a reviewed Mainnet paper policy as
`/var/lib/mithril-agent-research/policy/policy.json`. Put its completed
training/validation journals in `journals/`, its operator-selected immutable
champion artifacts and `active.json` pointer in `champion/`, and the two
policy-fingerprinted observer run trees below `runs/champion/` and
`runs/challenger/`. Only `challenger/` is writable by Hermes. Its candidate
files are content-addressed and its `active.json` pointer records selection,
next-UTC-day eligibility, challenge duration, and evaluator version. Keep every
directory mode `0700` and regular private
file mode `0600`.

Keep observer-written delivery state outside the operator-owned champion tree:
pre-create `status/champion/` for
`/var/lib/mithril-agent-research/status/champion/alerts.json`. The
champion observer may write only its run tree and that status directory; it
must not be able to rewrite the champion pointer or artifacts.

The example paths are deliberately identical on the host and in the container
because candidate pointers store absolute paths. If a different host layout is
used, every producer, observer, and Hermes container must see the same absolute
targets. A path translation that only Compose knows will make the host runner
reject the pointer.

## Paper lifecycle bootstrap

The research MCP needs completed base-policy journals and one operator-selected
champion before it starts. First install the reviewed policy with the exact
observer owner; never copy a root-owned mode-`0600` policy into this tree:

```sh
sudo install -o mithril-agent-research -g mithril-agent-research -m 0600 \
  /absolute/reviewed/policy.json /var/lib/mithril-agent-research/policy/policy.json
```

The tracked `mithril-agent-paper-base.service`,
`mithril-agent-paper-champion.service`, and
`mithril-agent-paper-challenger.path` supervise those observers. Install them
only after creating `/etc/mithril-agent/paper.env` as root-owned mode `0600`
with `MITHRIL_AGENT_SHADOW_RPC_URL` and, when used,
`MITHRIL_AGENT_JUPITER_API_KEY`. Review the fixed paths, owners, sandbox, and
read-write directories, then install and enable them. The units share one
private `/run` rate state so all observer processes start Kraken public requests
at least one second apart; do not replace that with retries after a 429.

```sh
sudo install -o root -g root -m 0644 \
  systemd/mithril-agent-paper-base.service \
  systemd/mithril-agent-paper-champion.service \
  systemd/mithril-agent-paper-challenger.service \
  systemd/mithril-agent-paper-challenger.path \
  /etc/systemd/system/
sudo systemd-analyze verify \
  /etc/systemd/system/mithril-agent-paper-{base,champion,challenger}.service \
  /etc/systemd/system/mithril-agent-paper-challenger.path
sudo systemctl enable --now systemd-time-wait-sync.service
test "$(timedatectl show -p NTPSynchronized --value)" = yes
sudo systemctl daemon-reload
sudo systemctl enable --now mithril-agent-paper-base.service
```

After two complete UTC base journals exist, create and select the first
immutable champion as the same no-login identity. Operator authority is the
reviewed `sudo` action; matching file ownership keeps every later strict read
valid:

```sh
sudo -u mithril-agent-research env HOME=/var/lib/mithril-agent-research \
  /usr/local/libexec/mithril-agent/mithril-agent shadow search \
  --policy /var/lib/mithril-agent-research/policy/policy.json \
  --dir /var/lib/mithril-agent-research/journals \
  --train-day YYYY-MM-DD --validation-day YYYY-MM-DD \
  --candidate-out /var/lib/mithril-agent-research/champion/candidate.json
sudo -u mithril-agent-research env HOME=/var/lib/mithril-agent-research \
  /usr/local/libexec/mithril-agent/mithril-agent shadow select \
  --policy /var/lib/mithril-agent-research/policy/policy.json \
  --candidate /var/lib/mithril-agent-research/champion/candidate.json \
  --pointer /var/lib/mithril-agent-research/champion/active.json \
  --lifecycle-lock /var/lib/mithril-agent-research/challenger/lifecycle.lock
sudo systemctl enable --now mithril-agent-paper-champion.service \
  mithril-agent-paper-challenger.service \
  mithril-agent-paper-challenger.path
```

Start Hermes only after the champion pointer exists and both base and champion
services are healthy. The enabled challenger service starts at boot when its
pointer exists; the `.path` unit watches `challenger/active.json` for later
creation or replacement. Future rejected or
operator-promoted
challengers replace the same pointer and the running observer loads the new
policy only after closing its UTC day. This is hot configuration without mixing
two policies in one daily journal. Challenge status is fixed to the first
configured number of complete UTC days beginning at the pointer's
`eligible_from`; later market days cannot reverse a completed decision.

Promotion remains manual. Install the exact qualified immutable artifact with
owner `mithril-agent-research`, then run `shadow select` as that identity with
the same `challenger/lifecycle.lock`. The next status
call recognizes the identical artifact digest as operator-promoted, after which
Hermes may prepare a new challenger. Nothing in this lifecycle grants live
execution authority.

```sh
sudo install -o mithril-agent-research -g mithril-agent-research -m 0600 \
  /var/lib/mithril-agent-research/challenger/candidates/CANDIDATE.json \
  /var/lib/mithril-agent-research/champion/CANDIDATE.json
sudo -u mithril-agent-research env HOME=/var/lib/mithril-agent-research \
  /usr/local/libexec/mithril-agent/mithril-agent shadow select \
  --policy /var/lib/mithril-agent-research/policy/policy.json \
  --candidate /var/lib/mithril-agent-research/champion/CANDIDATE.json \
  --pointer /var/lib/mithril-agent-research/champion/active.json \
  --lifecycle-lock /var/lib/mithril-agent-research/challenger/lifecycle.lock
```

After the champion has produced its first bounded alert file, regenerate the
normal deterministic Telegram service with the explicit opt-in below. Review
the emitted paper socket/bridge and alert unit, then install and restart the
generated units using the normal service-install runbook:

```sh
sudo -u mithril-agent env HOME=/var/lib/mithril-agent \
  /usr/local/libexec/mithril-agent/mithril-agent service install \
  --paper-alert-status /var/lib/mithril-agent-research/status/champion/alerts.json \
  --output /var/lib/mithril-agent/.mithril-agent/mithril-agent-run.service
```

The first attachment may deliver retained bounded history. Verify every message
starts with `PAPER SIMULATION` and that the bridge never exposes the source
path or any live transaction authority.

The initial deployment uses Hermes' keyless web ring for both search and
single-URL extraction. It requires no Tavily key, but it is rate-limited and is
not a production freshness SLA. Evidence failure or exhaustion must fail closed
without creating a challenger. Add a reviewed keyed provider later only if
measured reliability requires it.

This profile disables Tirith because it exposes no terminal, file, or code
execution toolset. That also prevents Hermes from downloading an unpinned
scanner at runtime. Re-enable it only with a separately pinned and reviewed
binary.

Before pulling or starting it, verify the immutable multi-architecture index:

```sh
sudo docker buildx imagetools inspect \
  nousresearch/hermes-agent:v2026.8.27@sha256:e0df6adebddf29b91112aefc999d4aaf6846c9eb544faca5672a16a13590ff79
sudo docker compose pull
sudo docker compose config --images
```

The first command must report that same index digest and the reviewed linux
child manifests: `sha256:5f23552e16589d291099cd8041233e6200197d225e4b28b22a0463e732d4b843`
for amd64 and `sha256:e3f4f0679f15556d5e09369cc36bf1074351b2d37bdd672dae593dfd07495180`
for arm64. The final two commands must pull and print only that pinned upstream
image reference; this profile has no derived image or runtime package install.

Install the reviewed files into the ignored private state with the exact owner:

```sh
sudo install -o mithril-agent-research -g mithril-agent-research -m 0600 \
  config.yaml state/config.yaml
sudo install -o mithril-agent-research -g mithril-agent-research -m 0600 \
  SOUL.md state/SOUL.md
```

Keep the root `.no-bundled-skills` marker created above. Hermes retains its
essential `hermes-agent` operating-manual skill, but must not seed or install
any other skill in this deployment. The `skills` toolset remains disabled. Do
not share this state directory with another Hermes process.

Authenticate with the approved ChatGPT/Codex subscription using the pinned
container. `--no-browser` prints a URL and one-time device code for the operator;
do not paste the resulting token or `state/auth.json` into chat:

```sh
sudo docker compose run --rm hermes-research \
  auth add openai-codex --no-browser
test "$(sudo stat -Lc '%u:%a' state/auth.json)" = \
  "$(id -u mithril-agent-research):600"
```

Hermes stores the refreshable credential only in the ignored `state/auth.json`.
It can consume the same Codex allowance as the signed-in subscription, so keep
this file owner-only and back it up separately from public source.

## Validate before starting

Run these commands through pinned one-off containers before starting the
service:

```sh
sudo docker compose run --rm hermes-research id
sudo docker compose run --rm hermes-research config check
sudo docker compose run --rm hermes-research doctor
sudo docker compose run --rm hermes-research mcp list
sudo docker compose run --rm hermes-research mcp test mithril_index
sudo docker compose run --rm hermes-research mcp test mithril_paper
sudo docker compose run --rm hermes-research mcp test solana_docs
sudo docker compose run --rm hermes-research tools list --platform cli
sudo docker compose run --rm hermes-research tools list --platform telegram
sudo docker compose run --rm hermes-research tools list --platform cron
sudo docker compose run --rm hermes-research python -c \
  'from hermes_cli.config import load_config; from hermes_cli.tools_config import _get_platform_tools; c = load_config(); expected = {"cli": {"web", "mithril_index", "mithril_paper", "solana_docs"}, "telegram": {"web", "mithril_index", "solana_docs"}, "cron": {"web", "mithril_index", "mithril_paper", "solana_docs"}}; got = {p: set(_get_platform_tools(c, p)) for p in expected}; assert got == expected, got; print({p: sorted(v) for p, v in got.items()})'
sudo docker compose run --rm hermes-research python -c \
  'from hermes_cli.config import load_config; from hermes_cli.tools_config import _get_platform_tools; from model_tools import get_tool_definitions; c = load_config(); disabled = c.get("agent", {}).get("disabled_toolsets", []); names = {p: {d["function"]["name"] for d in get_tool_definitions(sorted(_get_platform_tools(c, p)), disabled, quiet_mode=True, skip_tool_search_assembly=True)} for p in ("cli", "telegram", "cron")}; assert all({"web_search", "web_extract"} <= value for value in names.values()), names; assert not any(name.startswith("browser_") or name == "browser_exec" for value in names.values() for name in value), names; print({p: sorted({"web_search", "web_extract"} & value) for p, value in names.items()})'
sudo docker compose run --rm hermes-research python -c \
  'from tools.mcp_tool import discover_mcp_tools, shutdown_mcp_servers; expected = {"mithril_index": {"mithril_index_status", "mithril_index_accounts", "mithril_index_transactions"}, "mithril_paper": {"mithril_paper_create_challenger", "mithril_paper_challenge_status"}, "solana_docs": {"list_sections", "get_documentation"}}; want = {f"mcp__{server}__{tool}" for server, tools in expected.items() for tool in tools}; got = set(discover_mcp_tools()); assert got == want, sorted(got ^ want); print("effective MCP tools:", len(got)); shutdown_mcp_servers()'
sudo test ! -d state/cache/web || \
  test -z "$(sudo find state/cache/web -type f -print -quit)"
sudo docker compose run --rm hermes-research python -c \
  'import asyncio, json
from urllib.parse import urlparse
from tools.web_tools import web_extract_tool
cases = (
    ("https://solana.com/upgrades/larger-transaction-sizes", "solana.com", ("4,096", "4096")),
    ("https://developers.jup.ag/docs/swap/index", "developers.jup.ag", ("/swap/v2/build",)),
)
for requested, domain, markers in cases:
    payload = json.loads(asyncio.run(web_extract_tool([requested])))
    results = payload.get("results", [])
    assert len(results) == 1, payload
    result = results[0]
    assert result.get("error") is None, result
    assert urlparse(result.get("url", "")).hostname == domain, result
    assert result.get("title"), result
    assert any(marker in result.get("content", "") for marker in markers), result
print("single-URL extraction canary: pass")'
sudo find state/skills -name SKILL.md -print
```

Pinned Hermes v2026.8.27 has an
[upstream batched-extraction cache-association bug](https://github.com/NousResearch/hermes-agent/issues/97378).
The canary deliberately calls `web_extract_tool` with exactly one URL per invocation
and checks the returned domain, title, and a page-specific marker. Do
not combine those URLs into one call. A mismatch is a failed deployment and its
cache must not inform research.

The first command's UID and GID must exactly match `MITHRIL_UID` and
`MITHRIL_GID`. `config check` and `doctor` must
prove that `state/` is
writable; the Mithril MCP tests must prove that the binary, rooted index, and
paper evidence are reachable through their intended mounts.
The final-schema assertion is required because Hermes v2026.8.27 lists
`web_search` in both the `web` and `browser` toolsets and applies global denies
after platform allowlists. The `browser` toolset therefore stays absent from
every platform allowlist rather than appearing in `disabled_toolsets`; the
assertion proves that both web tools remain available while no browser tool is
model-visible. The paper test's raw catalog must contain exactly the create-challenger and
read-only status tools. In Hermes v2026.8.27, `mcp test` shows the server's raw
catalog before `tools.include` filtering: Solana currently advertises five
tools. Review that upstream surface, but use the registry assertion to prove
that the model receives exactly the configured 7 MCP tools. An
authentication failure, a missing tool, or an extra effective tool is a failed
deployment.
The three explicit `trust: full` entries are authorization for only that
post-filtered registry. Run the canary with stdin closed and no TTY, require it
to finish before the configured approval timeout, and compare writable state
before and after. Only a new immutable challenger and
`challenger/active.json` may change; the champion, live policy, completed
journals, and paired evidence must remain unchanged.
The final command must list only the official `hermes-agent/SKILL.md`; any
other skill is a failed deployment. Before the extraction canary,
`state/cache/web` must be absent or empty. This deployment must start with
fresh web-cache state; if an older
deployment ever used batched extraction, preserve that state for audit and
create a fresh dedicated state directory instead of trusting its cache.

The CLI and cron resolved toolsets must contain web search/extraction, three
`mithril_index_*` tools, and only the two
read-annotated Solana documentation tools, `list_sections` and
`get_documentation`, plus `mithril_paper_create_challenger` and
`mithril_paper_challenge_status`.
The resolver assertion proves that Telegram omits the paper server while CLI
and cron include it. In Hermes v2026.8.27, `tools list --platform` prints every
globally configured MCP server and its include filter, even when that server is
not a member of the selected platform. Use those three listings to inspect the
built-in toolsets and global MCP filters, not as the per-platform MCP-membership
proof.
`mcp test` remains the raw discovery/schema check; neither it nor a toolset
summary substitutes for the post-filter registry assertion.
It must not contain terminal, process, file mutation, code execution, browser
automation, skills, memory mutation, delegation, cron mutation, messaging,
wallet, signer, submitter, program build, signing, sending, submission, or
service-control tools.

Only after that review should the systemd-owned service start. Do not run
`docker compose up` directly and do not add a Docker restart policy: the unit
orders every start after the egress boundary and refuses startup until the
OAuth file, selected champion, and rooted index exist.

```sh
sudo systemctl enable --now mithril-hermes-research.service
```

## First recurring brief

Use the exact prompt in `prompts/market-scout.md` for the initial manual canary:

```sh
sudo docker compose exec -T hermes-research /command/s6-setuidgid hermes \
  hermes chat --query-file - < prompts/market-scout.md
```

Inspect its sources and verify `[SILENT]` behavior before creating a schedule.
After the service passes its canary, the tagged CLI supports:

```sh
sudo docker compose exec hermes-research /command/s6-setuidgid hermes \
  hermes cron create \
  "every 6h" \
  "Research material Solana and market changes published or occurring in the previous 12 hours. Record both source publication time and event time, use current primary sources and configured Mithril and Solana evidence tools, and mark inference. Read paper challenge status first. Create at most one cited paper challenger only when none is active, the completed prior challenger was rejected, or its exact artifact was promoted by the operator. Never change the champion or perform live execution. If nothing materially changed and no operator attention is needed, respond with exactly [SILENT]." \
  --name "Mithril market scout" \
  --provider openai-codex \
  --model gpt-5.6-terra \
  --deliver local \
  --continuity
```

The model-facing cron tool is disabled. The platform `cron` allowlist repeats
the exact raw MCP server keys so Hermes does not fall back to enabling every
configured MCP server. A successful research call changes only the dedicated
paper challenger pointer; an operator still decides whether a qualified
challenger becomes the next champion. An identical digest in the champion
pointer is the durable promotion acknowledgement and permits the next research
cycle without deleting or resetting either observer.

Six-hour scans stay within the intended research cadence. The overlapping
12-hour window is intentional: continuity carries only recent output and is
not a durable news ledger. Monitor the pinned provider/model, latest successful
execution age, failed or unknown status, consecutive failure count, Codex
usage-limit errors, and keyless search/extraction failures. A `[SILENT]` result
is successful only when the execution record says so.

After the service starts, use `compose exec` with the explicit Hermes identity
as shown above. A one-off `compose run` against shared live state can reconcile
gateway state while the supervised gateway is still running, while a plain
root `exec` can leave state files with the wrong owner. Before upgrade
validation that must use one-off containers against this state, stop the
systemd owner:

```sh
sudo systemctl stop mithril-hermes-research.service
```

After validation, restore the gateway through the same ordered owner:

```sh
sudo systemctl start mithril-hermes-research.service
```

## Freshness and recovery

Do not equate `mithril_index_status.complete` with current data. It proves that
the stored prefix is internally complete, not that ingestion is live. Before a
brief can influence a challenger, require a healthy supervised rooted-event
ingester, expose/check the index's last recorded time, compare its last rooted
slot with the local node's current root, and fail closed on cursor gaps. Until
that public Mithril prerequisite lands, treat rooted-index findings as bounded
historical evidence and say so in every brief.

Monitor the base, champion, and challenger unit state and newest journal-record
age against the policy tick interval. The bounded alert snapshot is event-driven
and is not a heartbeat; an old valid snapshot must never be reported as a
healthy observer.

Keep encrypted off-host backups of the pinned Compose/config/SOUL and hashes,
Hermes `state/`, `.env` separately, paper policy/journals/pointers/run trees and
lifecycle state, deterministic Telegram cursor and both announced stores, and
the rooted index or retained feed needed to rebuild it. Hetzner server backups
and snapshots exclude attached Volumes, so export those volumes separately.
Restore binaries/config/owners/modes first, then prove index integrity and
freshness, restore paper trees, restore Telegram cursor/dedup state, canary the
runners, canary Hermes MCP/schema/search, and enable cron
last. Keep cron stopped until freshness and delivery checks pass.

## Helius

Helius MCP is deliberately not installed in this autonomous profile. Its tagged
2.1.0 source makes model-written feedback fields mandatory on every routed call
and [sends them with model, tool, client, and anonymous-session metadata](https://github.com/helius-labs/core-ai/blob/helius-mcp%402.1.0/helius-mcp/src/utils/feedback.ts)
without an opt-out. A prompt is not an egress boundary, and this project does
not maintain a fork or proxy solely to suppress upstream telemetry.

The rooted Mithril index, deterministic paper sources, official Solana MCP, and
web research cover the required workflow. Helius Wallet API may be reconsidered
later as a separate read-only adapter for a concrete missing capability. Wallet
Kit/WaaS, signing, account management, streaming mutation, and write routers
remain outside this walletless research profile. Reconsider Helius MCP only
after an exact reviewed release provides a documented telemetry opt-out.

## Upgrade and rollback

For each Hermes or MCP upgrade, review the tagged configuration parser and tool
resolver, update the exact tag and OCI index digest, rerun every validation command,
and compare the full tool list before enabling cron. Roll back by
stopping the container and restoring the previous image/config pair; preserve
the profile state for audit rather than copying it into a broader profile.
