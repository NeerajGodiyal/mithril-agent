# Nous Hermes research profile

This deploys Nous Research Hermes Agent as a scheduled two-phase paper research
process. The first container may delegate up to three leaf source reviews but
has no policy, journal, challenger, wallet, or writable trading mount. Mithril
validates that phase's strict source-cited JSON before a separate
non-delegating session may create one immutable paper challenger through the
existing bounded MCP gate. The validated packet is hashed, archived, and
projected to the dashboard. Neither phase can change the champion or live
policy, build, sign, submit, authorize, or promote a transaction or strategy.
Hermes Telegram is disabled; deterministic Mithril remains the notification
path.

The image is pinned to tag `v2026.8.27` and OCI index digest
`sha256:e0df6adebddf29b91112aefc999d4aaf6846c9eb544faca5672a16a13590ff79`.
Do not replace it with `latest` or a tag without this digest. Hermes
configuration allowlists are defense in depth; the whole container or VM is
the security boundary.

All four MCP entries deliberately use `trust: full`. Pinned Hermes has an
[open read-only annotation bug](https://github.com/NousResearch/hermes-agent/issues/88858):
it reads the Python MCP annotation by its wire-format name, so read-only tools
on an untrusted server enter the interactive approval path. That path can wait
up to 300 seconds in this noninteractive profile. The paper server also has one
intentional write, bounded challenger creation, which cannot run unattended
under `trust: untrusted` even after the annotation bug is fixed.

In this Hermes release, `full` removes the per-call approval gate for a server;
it does not add tools, mounts, credentials, network access, or signer authority.
The finalizer boundary remains the exact include filters, the post-filter
registry assertion proving exactly 9 MCP tools after both champions exist, the
platform toolsets, and the container mounts. The delegated phase uses
`config-delegated.yaml`, two read-only MCP servers, and a separate ephemeral
state directory. Any newly included tool would inherit unattended approval,
so do not add or rename a server or tool without reviewing the resulting
authority. On every Hermes upgrade, recheck the upstream issue and repeat the
closed-stdin unattended canary. Even when the read-only bug is fixed, the paper
write still needs an explicit unattended authorization path; do not silently
change this profile back to an approval mode that makes unattended runs hang.

## Host boundary

Use a dedicated host or container account and a dedicated `state/` directory.
Hermes writes its own profile, sessions, and web-result cache beneath that state
directory; the container itself is not filesystem-read-only.
The non-delegating finalizer may mount only:

- the pinned Linux `mithril-agent` binary, read-only;
- the rooted index directory, read-only; and
- the paper policy, completed journals, champion, and paired run evidence,
  read-only;
- one dedicated challenger-control directory, read-write.

The delegated research container may mount only its ephemeral state, the shared
OAuth file read-only, its research-only config and SOUL read-only, the Mithril
binary read-only, the rooted index read-only, and the generated query read-only.
It must never mount a policy, journal, champion, or challenger path.

The research phase uses Hermes stateless one-shot mode (`hermes -z`). Pinned
Hermes runs delegation inline on that channel, so the parent waits for all
three leaf results before emitting one JSON packet. The wrapper extracts that
complete final response from the single completed root CLI session in Hermes'
redacted JSONL export, following an unambiguous compression-continuation chain
when present. Mixed container console output is discarded, never parsed. Do
not replace it with `hermes chat --query-file`: that mode exits while its
children are still running.

After the one-shot completes, the wrapper uses the pinned image's supported
`hermes sessions export --format jsonl --redact` command. It archives that full
redacted trace privately with mode `0600` and records its SHA-256 in a small
sidecar. The sidecar counts tool calls and successful page retrievals. A packet
citation is rejected unless its exact URL appears in a successful
`web_extract` result. The dashboard deliberately separates cited official pages
that were actually retrieved from two-source claims labelled by Hermes. Console
prose is never treated as retrieval evidence.

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
container environment are not inherited by these MCP children.

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
sudo install -m 0644 deploy/sysusers/mithril-agent-dashboard.conf \
  /usr/lib/sysusers.d/mithril-agent-dashboard.conf
sudo systemd-sysusers \
  /usr/lib/sysusers.d/mithril-agent-status.conf \
  /usr/lib/sysusers.d/mithril-agent-dashboard.conf
sudo install -d -o mithril-agent-research -g mithril-agent-research -m 0700 \
  /var/lib/mithril-agent-research \
  /var/lib/mithril-agent-research/index \
  /var/lib/mithril-agent-research/evidence \
  /var/lib/mithril-agent-research/reports \
  /var/lib/mithril-agent-research/runs \
  /var/lib/mithril-agent-research/status \
  /var/lib/mithril-agent-research/{policy,journals,champion,challenger} \
  /var/lib/mithril-agent-research/journals-jup \
  /var/lib/mithril-agent-research/challenger/candidates \
  /var/lib/mithril-agent-research/runs/{champion,challenger} \
  /var/lib/mithril-agent-research/jup/{champion,challenger} \
  /var/lib/mithril-agent-research/jup/challenger/candidates \
  /var/lib/mithril-agent-research/runs/jup/{pre-champion,champion,challenger} \
  /var/lib/mithril-agent-research/status/{champion,jup}
sudo install -d -o mithril-agent-dashboard -g mithril-agent-dashboard -m 0700 \
  /var/lib/mithril-agent-dashboard
```

Before the rooted index may inform research, populate that fixed `index/`
directory with a completed rooted-event ingest using the producer and recovery
procedure in [`INDEXING.md`](../../INDEXING.md). Run the `mithril-agent index
ingest` side of the supervisor-owned pipe as `mithril-agent-research`, keep the
Mithril AccountsDB private to its existing node identity, and use the permanent
cluster, genesis hash, and account/owner/mention filter selected for this
research profile. Do not copy or hand-edit `events.jsonl`. The final command
must pass before the wrapper exposes the index toolset:

```sh
sudo -u mithril-agent-research /usr/local/libexec/mithril-agent/mithril-agent \
  index doctor --dir /var/lib/mithril-agent-research/index \
  --max-record-age 15m
```

An empty provisioned directory is not research evidence. The wrapper runs
official-source research without the index until both `events.jsonl` exists and
`index doctor --max-record-age 15m` passes.
The wrapper also checks the doctor's JSON source for `mainnet-beta` and its
exact Mainnet genesis hash. Missing source identity, another cluster, or a
different genesis withholds the index even when local ingestion is recent.
The doctor's last recorded time proves recent local ingestion, not that the recorded
cursor has caught up with the chain. Replaying old records can pass this check.
The wrapper exposes valid recently ingested records as rooted history and
explicitly tells Hermes that current chain state has not been verified. The
dashboard reports the same limit. Comparing an independently observed producer
root with the ingestion cursor is still required before claiming current data.

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
  deploy/hermes-research/config-delegated.yaml \
  deploy/hermes-research/build-research-evidence.py \
  deploy/hermes-research/AGENTS.md \
  deploy/hermes-research/AGENTS-research.md \
  deploy/hermes-research/SOUL.md \
  deploy/hermes-research/SOUL-research.md \
  /opt/mithril-hermes-research/
sudo install -o root -g root -m 0755 \
  deploy/hermes-research/apply-paper-instruction.sh \
  deploy/hermes-research/check-network.sh \
  deploy/hermes-research/bootstrap-first-champion.sh \
  deploy/hermes-research/run-paper-generation.sh \
  deploy/hermes-research/run-market-scout.sh \
  /opt/mithril-hermes-research/
sudo install -o root -g root -m 0644 \
  deploy/hermes-research/prompts/market-scout.md \
  /opt/mithril-hermes-research/prompts/market-scout.md
sudo install -o root -g root -m 0644 \
  deploy/systemd/mithril-agent-paper-base.service \
  deploy/systemd/mithril-agent-paper-champion.service \
  deploy/systemd/mithril-agent-paper-champion.path \
  deploy/systemd/mithril-agent-paper-challenger.service \
  deploy/systemd/mithril-agent-paper-challenger.path \
  deploy/systemd/mithril-agent-paper-bootstrap.service \
  deploy/systemd/mithril-agent-paper-bootstrap.timer \
  deploy/systemd/mithril-agent-paper-auto-select.service \
  deploy/systemd/mithril-agent-paper-auto-select.timer \
  deploy/systemd/mithril-agent-paper-generation.target \
  deploy/systemd/mithril-agent-paper-instruction.path \
  deploy/systemd/mithril-agent-paper-instruction.service \
  deploy/systemd/mithril-agent-paper-pre-champion.service \
  deploy/systemd/mithril-agent-paper-status-handoff.service \
  deploy/systemd/mithril-agent-paper-jup.service \
  deploy/systemd/mithril-agent-paper-jup-pre-champion.service \
  deploy/systemd/mithril-agent-paper-jup-champion.service \
  deploy/systemd/mithril-agent-paper-jup-champion.path \
  deploy/systemd/mithril-agent-paper-jup-challenger.service \
  deploy/systemd/mithril-agent-paper-jup-challenger.path \
  deploy/systemd/mithril-agent-paper-jup-bootstrap.service \
  deploy/systemd/mithril-agent-paper-jup-bootstrap.timer \
  deploy/systemd/mithril-agent-paper-jup-auto-select.service \
  deploy/systemd/mithril-agent-paper-jup-auto-select.timer \
  deploy/systemd/mithril-agent-paper-jup-status-handoff.service \
  deploy/systemd/mithril-agent-paper-jup-status.socket \
  deploy/systemd/mithril-agent-paper-jup-status-bridge.service \
  deploy/systemd/mithril-agent-market-candidate@.service \
  deploy/systemd/mithril-agent-market-paper@.service \
  deploy/systemd/mithril-agent-market-paper-status@.socket \
  deploy/systemd/mithril-agent-market-paper-status-bridge@.service \
  deploy/systemd/mithril-agent-market-status.service \
  deploy/systemd/mithril-agent-market-status.timer \
  deploy/systemd/mithril-agent-perps-paper.service \
  deploy/systemd/mithril-agent-perps-paper.timer \
  deploy/systemd/mithril-agent-perps-paper-status@.socket \
  deploy/systemd/mithril-agent-perps-paper-status-bridge@.service \
  deploy/systemd/mithril-agent-paper-dashboard.service \
  deploy/systemd/mithril-agent-telegram-paper.conf \
  deploy/systemd/mithril-hermes-research-egress.service \
  deploy/systemd/mithril-hermes-research.service \
  deploy/systemd/mithril-hermes-research.timer \
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
sudo install -o mithril-agent-research -g mithril-agent-research -m 0600 \
  deploy/hermes-research/AGENTS.md /opt/mithril-hermes-research/state/AGENTS.md
cd /opt/mithril-hermes-research
```

Create the fixed bridge once, install its firewall unit, and verify both the
subnet and active deny chain before any OAuth or web call:

```sh
sudo docker network create --driver bridge --subnet 172.30.77.0/28 \
  mithril-hermes-research
test "$(sudo docker network inspect mithril-hermes-research \
  --format '{{(index .IPAM.Config 0).Subnet}}')" = 172.30.77.0/28
```

When upgrading from the former persistent gateway unit, stop it while its old
unit definition is still loaded and remove its Compose container before
installing the one-shot unit. On a fresh installation only the final empty
Compose check is required:

```sh
sudo systemctl stop mithril-hermes-research.service
sudo docker compose down --timeout 30
test -z "$(sudo docker compose ps -q)"
```

Then install and verify the one-shot service and timer:

```sh
sudo install -o root -g root -m 0644 \
  systemd/mithril-hermes-research-egress.service \
  systemd/mithril-hermes-research.service \
  systemd/mithril-hermes-research.timer \
  /etc/systemd/system/
sudo systemd-analyze verify \
  /etc/systemd/system/mithril-hermes-research-egress.service \
  /etc/systemd/system/mithril-hermes-research.service \
  /etc/systemd/system/mithril-hermes-research.timer
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
deterministic mithril-agent Telegram service.
The agent binary, index, policy, journals, champion, challenger, and run-tree
sources are literal paths in `compose.yaml`; `.env` cannot redirect them after
the systemd preflights validate those paths.

Install a reviewed Mainnet paper policy as
`/etc/mithril-agent/paper-active/sol-policy.json`. Put its completed daily
journals in `journals/`, its automatically bootstrapped or later paper-selected
immutable champion artifacts and `active.json` pointer in
`champion/`, and the two
policy-fingerprinted observer run trees below `runs/champion/` and
`runs/challenger/`. Only the SOL and JUP `challenger/` trees are writable by
Hermes. Their candidate
files are content-addressed and its `active.json` pointer records selection,
next-UTC-day eligibility, challenge duration, and evaluator version. Keep every
directory mode `0700` and regular private
file mode `0600`.

Keep observer-written delivery state outside the operator-owned champion tree:
pre-create `status/champion/` for
`/etc/mithril-agent/paper-active/status/sol/alerts.json`. The
champion observer may write only its run tree and that status directory; it
must not be able to rewrite the champion pointer or artifacts.

The example paths are deliberately identical on the host and in the container
because candidate pointers store absolute paths. If a different host layout is
used, every producer, observer, and Hermes container must see the same absolute
targets. A path translation that only Compose knows will make the host runner
reject the pointer.

## Paper lifecycle bootstrap

The research MCP can produce cited market briefs before a champion exists, but
its paper candidate tools remain absent until a first champion is selected.
First install the reviewed policy with the exact observer owner; never copy a
root-owned mode-`0600` policy into this tree:

```sh
sudo install -o mithril-agent-research -g mithril-agent-research -m 0600 \
  /absolute/reviewed/policy.json /var/lib/mithril-agent-research/policy/policy.json
sudo -u mithril-agent-research env HOME=/var/lib/mithril-agent-research \
  /usr/local/libexec/mithril-agent/mithril-agent shadow portfolio \
  --out /var/lib/mithril-agent-research/policy/portfolio.json \
  --limit-usd 150 --max-sol-usd 300 \
  --book sol=/var/lib/mithril-agent-research/policy/policy.json
```

The portfolio manifest counts one economic book once even when base, champion,
and challenger observers run counterfactual copies. The conservative $300/SOL
planning ceiling makes the initial 0.25 SOL mandate count as $75. If validated
SOL/USD evidence exceeds that ceiling, the observers fail closed before opening
a journal or announcing a strategy.

The tracked `mithril-agent-paper-base.service`,
`mithril-agent-paper-champion.service`, and
the champion and challenger path units supervise those observers. The separate
paper bootstrap selects the first champion from the immediately preceding two
complete UTC base journals, and the auto-selector checks later fixed forward
gates. Both run without network access. Install them
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
  systemd/mithril-agent-paper-champion.path \
  systemd/mithril-agent-paper-challenger.service \
  systemd/mithril-agent-paper-challenger.path \
  systemd/mithril-agent-paper-bootstrap.service \
  systemd/mithril-agent-paper-bootstrap.timer \
  systemd/mithril-agent-paper-auto-select.service \
  systemd/mithril-agent-paper-auto-select.timer \
  systemd/mithril-agent-paper-generation.target \
  systemd/mithril-agent-paper-instruction.path \
  systemd/mithril-agent-paper-instruction.service \
  systemd/mithril-agent-paper-pre-champion.service \
  systemd/mithril-agent-paper-status-handoff.service \
  systemd/mithril-agent-perps-paper.service \
  systemd/mithril-agent-perps-paper.timer \
  systemd/mithril-agent-perps-paper-status@.socket \
  systemd/mithril-agent-perps-paper-status-bridge@.service \
  /etc/systemd/system/
sudo install -d -o root -g root -m 0755 \
  /var/lib/mithril-agent-research/allocations
sudo install -d -o mithril-agent-research -g mithril-agent-research -m 0700 \
  /var/lib/mithril-agent-research/outcomes
sudo systemd-analyze verify \
  /etc/systemd/system/mithril-agent-paper-{base,champion,challenger}.service \
  /etc/systemd/system/mithril-agent-paper-{pre-champion,status-handoff,instruction}.service \
  /etc/systemd/system/mithril-agent-paper-{bootstrap,auto-select}.service \
  /etc/systemd/system/mithril-agent-paper-{champion.path,challenger.path,instruction.path,bootstrap.timer,auto-select.timer} \
  /etc/systemd/system/mithril-agent-perps-paper.service \
  /etc/systemd/system/mithril-agent-perps-paper.timer \
  /etc/systemd/system/mithril-agent-perps-paper-status@.socket \
  /etc/systemd/system/mithril-agent-perps-paper-status-bridge@.service \
  /etc/systemd/system/mithril-agent-paper-generation.target
sudo systemctl enable --now systemd-time-wait-sync.service
test "$(timedatectl show -p NTPSynchronized --value)" = yes
sudo systemctl daemon-reload
sudo systemctl enable mithril-agent-paper-generation.target \
  mithril-agent-paper-instruction.service
sudo systemctl enable --now mithril-agent-paper-instruction.path
sudo systemctl enable --now mithril-agent-perps-paper.timer \
  mithril-agent-perps-paper-status@sol.socket \
  mithril-agent-perps-paper-status@btc.socket \
  mithril-agent-perps-paper-status@eth.socket
sudo systemctl list-timers mithril-agent-perps-paper.timer
sudo systemctl status \
  mithril-agent-perps-paper-status@sol.socket \
  mithril-agent-perps-paper-status@btc.socket \
  mithril-agent-perps-paper-status@eth.socket
```

These commands install the bounded SOL, BTC, and ETH perps paper timer and its
read-only status sockets; no signer or exchange account is configured. They do
not start a legacy spot journal. Complete
the optional JUP portfolio setup below, save the dashboard instruction, and use
the atomic activation procedure before expecting paper observers to run.

Each current-format perps final tape and its evaluation are recorded first in
the symbol's hash-chained, segmented finalization journal. The journal keeps
every prior receipt, treats an exact repeat as idempotent, and rejects
conflicting lineage before replacing the displayed result or selecting a paper
plan. Receipts bind the evaluator, final tape, qualification result, optional
walk-forward input and result, leader, incumbent, and incumbent replay by
digest without storing file paths or prose. A walk-forward result reports the
actual 12 training trials when that search ran, one held-out plan, completed
trades, and confidence as
`not_estimated_insufficient_independent_episodes`; it does not claim PBO or DSR.
When selection is attempted, its receipt reports two compared held-out plans
after the selector replays the incumbent and the verified finalization-receipt
count. The separate `one_frame_execution_delay_v1` artifact is a standalone,
best-effort paper research result, not qualification evidence or network
latency. It applies the frozen prior-frame decision after the next frame's
funding and mark, ignores the final queued signal, and cannot change
qualification, selection, promotion, or live/paper decisions.
Verified legacy v3 tapes remain readable for offline research, but only a
current-format final tape can receive the exact finalization receipt required
to select a new paper plan.

The hourly bootstrap remains a no-op until the prior two UTC journals are both
complete and replayable. It then searches those exact chronological days and
writes one immutable initial candidate. `shadow select --initial` rereads the
bound journals, requires at least 95% observable coverage on both days, at least
two validation round trips, positive validation return, an advantage over
holding, and compliance with the adaptive drawdown limit. It repeats the same
validation at twice the candidate's modelled spread, then selects only if no
champion pointer already exists. The pointer check remains under the shared
lifecycle lock, so this automation cannot replace an existing champion. After
the pointer appears, verify the already-enabled paper observers and later
challenger selector:

```sh
sudo systemctl status mithril-agent-paper-bootstrap.timer
sudo journalctl -u mithril-agent-paper-bootstrap.service
test -f /etc/mithril-agent/paper-active/selection/sol/champion/active.json
sudo systemctl is-active mithril-agent-paper-champion.service
```

Hermes research may run before this point with only its public research tools;
it gains the bounded paper candidate tools only after the champion pointer
exists. The initial champion deliberately needs only the prior two completed
days so startup is not blocked for a week. Every later automatic challenger
requires eight consecutive completed, replayable days ending yesterday. The
server selects a fresh candidate on each earlier day and scores it only on the
following untouched day; at least four of seven out-of-sample folds, the
aggregate advantage, and the aggregate round-trip count must pass before an
artifact or pointer can be written. The separate fixed forward challenge still
runs afterward. The enabled challenger service starts at boot when its
pointer exists; the `.path` unit watches `challenger/active.json` for later
creation or replacement. Future rejected or paper-selected
challengers replace the same pointer and the running observer loads the new
policy only after closing its UTC day. This is hot configuration without mixing
two policies in one daily journal. Challenge status is fixed to the first
configured number of complete UTC days beginning at the pointer's
`eligible_from`; later market days cannot reverse a completed decision.

The hourly auto-selector does nothing while evidence is pending or a challenger
fails any gate. When the exact forward challenger qualifies, it copies the
content-addressed artifact outside Hermes' writable tree, preserves the old
champion in `champion/previous.json`, and selects the copy for the next UTC day.
For Hermes-bound challengers it first appends the fixed forward result to the
per-market hash-chained journal in `/var/lib/mithril-agent-research/outcomes`,
then appends a matching confirmation only after the champion pointer changes.
An exact retry reconciles a missing confirmation after revalidating the current
champion digest. The journal contains bounded identifiers, parameter changes,
measurements, and reason codes only—never source prose, paths, policy JSON, keys,
or authority.
The next status call recognizes the identical digest, after which Hermes may
prepare a new challenger. Nothing in this lifecycle grants live execution authority.
Manual `shadow select` remains available when the auto-selector timer is disabled.

```sh
sudo -u mithril-agent-research env HOME=/var/lib/mithril-agent-research \
  /usr/local/libexec/mithril-agent/mithril-agent shadow restore \
  --policy /etc/mithril-agent/paper-active/sol-policy.json \
  --champion-pointer /etc/mithril-agent/paper-active/selection/sol/champion/active.json \
  --rollback-pointer /etc/mithril-agent/paper-active/selection/sol/champion/previous.json \
  --challenger-pointer /etc/mithril-agent/paper-active/selection/sol/challenger/active.json \
  --challenger-candidate-dir /etc/mithril-agent/paper-active/selection/sol/challenger/candidates \
  --lifecycle-lock /etc/mithril-agent/paper-active/selection/sol/challenger/lifecycle.lock
```

Restore also rebinds the challenger observer to the restored champion under the
same lifecycle lock. The hourly selector therefore sees matching paper digests
and cannot immediately select the rolled-back challenger again; the next
research cycle may prepare a different challenger.

After the champion has produced its first bounded alert file, regenerate the
normal deterministic Telegram service with the explicit opt-in below. Review
the emitted paper socket/bridge and alert unit, then install and restart the
generated units using the normal service-install runbook:

```sh
sudo -u mithril-agent env HOME=/var/lib/mithril-agent \
  /usr/local/libexec/mithril-agent/mithril-agent service install \
  --paper-alert-status /etc/mithril-agent/paper-active/status/sol/alerts.json \
  --output /var/lib/mithril-agent/.mithril-agent/mithril-agent-run.service
```

The first attachment may deliver retained bounded history. Verify every new message
starts with `PAPER ·` and that the bridge never exposes the source
path or any live transaction authority.

### Optional JUP/USDC lifecycle

JUP/USDC is a second isolated paper mandate, not another full-budget observer.
Set its USDC lot as an explicit minority of the combined paper capital before
starting it; the example below assigns 50 USDC, not the former 250 USDC
full-size lot. Its native SOL fee reserve is separate. Candidate search,
selection, observation, and automatic promotion all fingerprint that base
policy, so no JUP challenger may enlarge the lot, opening inventory, fee
reserve, or setup-rent reserve.

Generate or replace the JUP policy only at a UTC journal boundary. An existing
deployment must stop the old observer before replacing its policy. Keep the old
journals as audit evidence, but collect two new complete UTC days under the
reduced policy before expecting the bootstrap to select a champion. The
separate pre-champion observer owns bounded status during that evidence window;
the always-on base observer remains journal-only.

Before replacing an existing deployment, archive its old full-size status so
the reduced pre-champion observer cannot inherit old fills, P/L, or chart
history. The guarded moves are no-ops for a fresh install; numbered backups
prevent a repeated migration from overwriting earlier audit evidence.

```sh
sudo systemctl stop mithril-agent-paper-jup.service
sudo install -d -o mithril-agent-research -g mithril-agent-research -m 0700 \
  /var/lib/mithril-agent-research/archive/jup-status-before-reallocation
sudo test ! -e /var/lib/mithril-agent-research/status/jup/alerts.json || \
  sudo mv --backup=numbered /var/lib/mithril-agent-research/status/jup/alerts.json \
    /var/lib/mithril-agent-research/archive/jup-status-before-reallocation/alerts.json
sudo test ! -e /var/lib/mithril-agent-research/status/jup/champion-owned || \
  sudo mv --backup=numbered /var/lib/mithril-agent-research/status/jup/champion-owned \
    /var/lib/mithril-agent-research/archive/jup-status-before-reallocation/champion-owned
sudo -u mithril-agent-research env HOME=/var/lib/mithril-agent-research \
  /usr/local/libexec/mithril-agent/mithril-agent shadow policy \
  --out /var/lib/mithril-agent-research/policy/jup-policy.json \
  --observe WATCH_ONLY_ADDRESS --adaptive --market JUP/USDC \
  --budget-usdc 50 --fee-reserve-sol 0.080 --setup-rent-sol 0.003 \
  --drawdown-stop-bps 300 --fee-lamports 100000
sudo -u mithril-agent-research env HOME=/var/lib/mithril-agent-research \
  /usr/local/libexec/mithril-agent/mithril-agent shadow portfolio \
  --out /var/lib/mithril-agent-research/policy/portfolio.json \
  --limit-usd 150 --max-sol-usd 300 \
  --book sol=/var/lib/mithril-agent-research/policy/policy.json \
  --book jup=/var/lib/mithril-agent-research/policy/jup-policy.json
sudo install -o root -g root -m 0644 \
  systemd/mithril-agent-paper-jup.service \
  systemd/mithril-agent-paper-jup-pre-champion.service \
  systemd/mithril-agent-paper-jup-champion.service \
  systemd/mithril-agent-paper-jup-champion.path \
  systemd/mithril-agent-paper-jup-challenger.service \
  systemd/mithril-agent-paper-jup-challenger.path \
  systemd/mithril-agent-paper-jup-bootstrap.service \
  systemd/mithril-agent-paper-jup-bootstrap.timer \
  systemd/mithril-agent-paper-jup-auto-select.service \
  systemd/mithril-agent-paper-jup-auto-select.timer \
  systemd/mithril-agent-paper-jup-status-handoff.service \
  systemd/mithril-agent-paper-jup-status.socket \
  systemd/mithril-agent-paper-jup-status-bridge.service \
  /etc/systemd/system/
sudo install -d -o root -g root -m 0755 \
  /etc/systemd/system/mithril-agent-telegram.service.d
sudo install -o root -g root -m 0644 \
  systemd/mithril-agent-telegram-paper.conf \
  /etc/systemd/system/mithril-agent-telegram.service.d/paper.conf
sudo systemd-analyze verify \
  /etc/systemd/system/mithril-agent-paper-jup.service \
  /etc/systemd/system/mithril-agent-paper-jup-{pre-champion,champion,challenger,bootstrap,auto-select,status-handoff}.service \
  /etc/systemd/system/mithril-agent-paper-jup-{champion.path,challenger.path,bootstrap.timer,auto-select.timer} \
  /etc/systemd/system/mithril-agent-paper-jup-status.socket \
  /etc/systemd/system/mithril-agent-paper-jup-status-bridge.service
sudo systemctl daemon-reload
sudo systemctl enable --now mithril-agent-paper-jup-status.socket
sudo systemctl restart mithril-agent-telegram.service
```

The base observer writes only `journals-jup/`; the pre-champion observer writes
its independent run tree and the bounded status snapshot, so `/paper` remains
available while bootstrap evidence accumulates. The bootstrap uses the same
immutable candidate format and lifecycle lock as SOL but separate JUP control
and run trees. It remains a no-op until the immediately preceding two journals
are complete and replayable, then applies the same coverage, validation return,
round-trip, versus-holding, drawdown, and doubled-spread initial gate.

When the first pointer appears, the champion service conflicts with and stops
the pre-champion owner. Its required offline handoff archives the bounded old
snapshot as `pre-champion-alerts.json` and creates the persistent
`champion-owned` marker before the champion starts. The new champion therefore
writes a fresh `alerts.json` without inheriting pre-champion events or chart
history. Verify the handoff:

```sh
test -f /etc/mithril-agent/paper-active/selection/jup/champion/active.json
test -f /etc/mithril-agent/paper-active/status/jup/alerts.json
test -f /etc/mithril-agent/paper-active/status/jup/champion-owned
sudo systemctl is-active mithril-agent-paper-jup-champion.service
test "$(systemctl is-active mithril-agent-paper-jup-pre-champion.service)" = inactive
```

`/paper` then reads JUP status from the immutable champion, never the base or
challenger observer. Alerts remain limited to strategy changes, newly opened orders, fills, risk
pauses, period closes, and sustained market-data loss or recovery. Once the JUP
base and champion observers, challenger path, and auto-selector timer are all
healthy, the wrapper exposes the separate `mithril_paper_jup` server. Hermes may
then trigger the same deterministic bounded search as SOL, but only the JUP
challenger tree is writable. The dedicated selector applies the same seven-day
fixed forward gate and rollback-pointer rules without sharing either market's
capital or control files.

### Apply dashboard paper settings without restarting Mithril

Paper budget, minimum and maximum order size, price-check speed, and the paper
loss stop are one atomic configuration. A dashboard save is only a request.
The root-owned apply service exports its canonical bytes, builds a new immutable
allocation generation, prepares empty generation-local journals and
champion/challenger state, and then replaces the single
`/etc/mithril-agent/paper-active` symlink. It stops and restarts only
`mithril-agent-paper-generation.target`; it never restarts Mithril, carries a
paper journal into a new configuration, signs, or submits a transaction. If a
new base or pre-champion observer does not start, the service restores the old
selector and old paper target. Old generations remain available for audit.
On the first apply there is no old selector to restore, so failure leaves paper
services stopped and the selector absent instead of presenting legacy files as
an active generation.

Install the two root-owned helpers and the generation units, then create the
root-owned allocation parent before enabling activation:

```sh
sudo install -o root -g root -m 0755 \
  deploy/hermes-research/apply-paper-instruction.sh \
  deploy/hermes-research/run-paper-generation.sh \
  /opt/mithril-hermes-research/
sudo install -d -o root -g root -m 0755 \
  /var/lib/mithril-agent-research/allocations
sudo install -o root -g root -m 0644 \
  deploy/systemd/mithril-agent-paper-generation.target \
  deploy/systemd/mithril-agent-paper-instruction.path \
  deploy/systemd/mithril-agent-paper-instruction.service \
  deploy/systemd/mithril-agent-paper-pre-champion.service \
  deploy/systemd/mithril-agent-paper-status-handoff.service \
  /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable mithril-agent-paper-generation.target \
  mithril-agent-paper-instruction.service
sudo systemctl enable --now mithril-agent-paper-instruction.path
```

The first apply uses the reviewed legacy portfolio only as its immutable input.
After saving a current request in the dashboard, start the apply once; later
saves trigger it through the path unit:

```sh
sudo systemctl start mithril-agent-paper-instruction.service
sudo systemctl status mithril-agent-paper-instruction.service \
  mithril-agent-paper-generation.target
readlink -e /etc/mithril-agent/paper-active
```

The dashboard reports a request as active only when both current market status
snapshots carry the exact saved instruction SHA-256. A matching dollar amount
or cadence is not treated as proof. Every new generation begins with fresh base
and pre-champion journals; its selectors can promote only candidates created
for that generation.

### Solana market-admission collectors

WIF is not enabled as a paper market immediately. Its collector first records
30 complete UTC days of minute-by-minute Pyth, Kraken, Jupiter route, mint,
USDC-peg, and native-fee evidence. Missing buckets and provider failures count
as unavailable. This prevents a newly discovered token from becoming a paper
mandate on the strength of a ticker or a short favorable sample. JTO/USDC and
PYTH/USDC use the same gate with their own pinned mint, decimals, Pyth feed,
Kraken pair, and Jupiter routes. They do not share evidence or become active
paper markets merely because their collectors are running.

Provision the collectors before running any diagnostic or qualification command.
The environment files contain only the public watch-only quote address; it cannot
sign or spend. The templated unit is the single WIF owner and conflicts with the
legacy dedicated unit at the systemd layer.

```sh
printf '%s\n' 'MITHRIL_AGENT_MARKET=WIF/USDC' \
  'MITHRIL_AGENT_OBSERVE=WATCH_ONLY_ADDRESS' | \
  sudo install -o root -g root -m 0600 /dev/stdin /etc/mithril-agent/market-wif.env
printf '%s\n' 'MITHRIL_AGENT_MARKET=JTO/USDC' \
  'MITHRIL_AGENT_OBSERVE=WATCH_ONLY_ADDRESS' | \
  sudo install -o root -g root -m 0600 /dev/stdin /etc/mithril-agent/market-jto.env
printf '%s\n' 'MITHRIL_AGENT_MARKET=PYTH/USDC' \
  'MITHRIL_AGENT_OBSERVE=WATCH_ONLY_ADDRESS' | \
  sudo install -o root -g root -m 0600 /dev/stdin /etc/mithril-agent/market-pyth.env
sudo install -d -o mithril-agent-research -g mithril-agent-research -m 0700 \
  /var/lib/mithril-agent-research/market-admission-wif \
  /var/lib/mithril-agent-research/market-admission-jto \
  /var/lib/mithril-agent-research/market-admission-pyth
sudo install -o root -g root -m 0644 \
  systemd/mithril-agent-market-candidate@.service /etc/systemd/system/
sudo systemd-analyze verify /etc/systemd/system/mithril-agent-market-candidate@.service
sudo systemctl daemon-reload
sudo systemctl disable --now mithril-agent-market-wif.service
sudo systemctl enable --now \
  mithril-agent-market-candidate@wif.service \
  mithril-agent-market-candidate@jto.service \
  mithril-agent-market-candidate@pyth.service
```

The older `mithril-agent-market-wif.service` must remain disabled. The systemd
conflict prevents both WIF units from owning the same evidence journal if it is
accidentally enabled again.

Market-admission v4 changes the source-alignment contract from 30 to 75
seconds, so v3 journals must not be resumed or mixed with new observations.
Before the first v4 collector restart, preserve each complete v3 directory and
the derived dashboard projection, then recreate empty private collector
directories. This is an evidence rotation, not deletion:

```sh
set -eu
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
sudo systemctl stop mithril-agent-market-status.timer \
  mithril-agent-market-candidate@wif.service \
  mithril-agent-market-candidate@jto.service \
  mithril-agent-market-candidate@pyth.service
sudo install -d -o root -g root -m 0700 \
  "/var/lib/mithril-agent-research/archive/market-admission-v3-$STAMP"
for market in wif jto pyth; do
  sudo mv --no-target-directory \
    "/var/lib/mithril-agent-research/market-admission-$market" \
    "/var/lib/mithril-agent-research/archive/market-admission-v3-$STAMP/$market"
  sudo install -d -o mithril-agent-research -g mithril-agent-research -m 0700 \
    "/var/lib/mithril-agent-research/market-admission-$market"
done
if sudo test -f /var/lib/mithril-agent-dashboard/market-admission.json; then
  sudo mv --no-target-directory /var/lib/mithril-agent-dashboard/market-admission.json \
    "/var/lib/mithril-agent-research/archive/market-admission-v3-$STAMP/dashboard-projection.json"
fi
sudo systemctl start \
  mithril-agent-market-candidate@wif.service \
  mithril-agent-market-candidate@jto.service \
  mithril-agent-market-candidate@pyth.service \
  mithril-agent-market-status.timer
```

For fast operational feedback, stop one collector briefly and run a diagnostic
over 6 hours, 24 hours, or up to 168 hours. It reports missing and rejected
buckets, bidirectional availability, route cost, and quote latency to stdout.
It is explicitly `diagnostic_only`, never writes an artifact, and cannot qualify
or start a market. This checks plumbing quickly while stronger admission
evidence continues collecting in the background.

```sh
sudo systemctl stop mithril-agent-market-candidate@wif.service
sudo -u mithril-agent-research /usr/local/libexec/mithril-agent/mithril-agent \
  shadow market diagnose \
  --journal /var/lib/mithril-agent-research/market-admission-wif/evidence.jsonl \
  --hours 6
sudo systemctl start mithril-agent-market-candidate@wif.service
```

After at least two complete hours, development can create a short-lived,
paper-only checkpoint instead of waiting 30 days before exercising the runner.
The checkpoint command refuses a live collector and refuses to replace an existing file.
The resulting artifact is only the evidence checkpoint; it is not a strategy.
Use it to generate a `development_provisional` policy and pass the same artifact
and journal to the runner. The check below replays only validated market samples
with modelled fills; it does not treat a runner journal as execution evidence.
Neither the artifact nor that policy can authorize a proposal or real-money
execution.

```sh
# Install the bounded runner and status bridge before stopping collection.
sudo install -o root -g root -m 0644 \
  systemd/mithril-agent-market-paper@.service \
  systemd/mithril-agent-market-paper-status@.socket \
  systemd/mithril-agent-market-paper-status-bridge@.service \
  /etc/systemd/system/
sudo systemd-analyze verify \
  /etc/systemd/system/mithril-agent-market-paper@.service \
  /etc/systemd/system/mithril-agent-market-paper-status@.socket \
  /etc/systemd/system/mithril-agent-market-paper-status-bridge@.service
sudo systemctl daemon-reload
sudo systemctl enable --now \
  mithril-agent-market-paper-status@wif.socket \
  mithril-agent-market-paper-status@jto.socket \
  mithril-agent-market-paper-status@pyth.socket

# Run the rest of this fenced block as one script so set -e and the EXIT trap
# always restart collection when a qualification command refuses its input.
set -e
STAMP="$(date -u +%Y%m%dT%H%MZ)"
MARKET_DIR=/var/lib/mithril-agent-research/market-admission-wif
sudo systemctl stop mithril-agent-market-candidate@wif.service
trap 'sudo systemctl start mithril-agent-market-candidate@wif.service' EXIT
sudo -u mithril-agent-research /usr/local/libexec/mithril-agent/mithril-agent \
  shadow market provisional \
  --journal "$MARKET_DIR/evidence.jsonl" \
  --out "$MARKET_DIR/provisional-$STAMP.json"

# Use the exact watch-only address, quote notional, and slippage recorded in
# the new checkpoint. The command refuses values that do not match the artifact.
sudo -u mithril-agent-research /usr/local/libexec/mithril-agent/mithril-agent \
  shadow policy --adaptive --market WIF/USDC \
  --observe WATCH_ONLY_ADDRESS --budget-usdc QUOTE_NOTIONAL \
  --slippage-bps RECORDED_SLIPPAGE_BPS --drawdown-stop-bps 500 \
  --provisional-artifact "$MARKET_DIR/provisional-$STAMP.json" \
  --provisional-journal "$MARKET_DIR/evidence.jsonl" \
  --out "$MARKET_DIR/base-policy-$STAMP.json"

# This reads only the checkpoint's exact journal prefix. It selects on the
# first 80 minutes, checks the fixed candidate on the final 40 minutes at a
# fixed, code-owned 25 bps symmetric spread and again at the 50 bps stress
# spread, then prints JSON. Observed route-cost percentiles remain operational
# evidence; held-out observations cannot lower or choose the model.
# A passing result writes the exact checked policy. A rejected result exits
# nonzero, writes no candidate policy, and the EXIT trap restarts the collector.
sudo -u mithril-agent-research /usr/local/libexec/mithril-agent/mithril-agent \
  shadow market paper-check \
  --policy "$MARKET_DIR/base-policy-$STAMP.json" \
  --provisional-artifact "$MARKET_DIR/provisional-$STAMP.json" \
  --journal "$MARKET_DIR/evidence.jsonl" \
  --dashboard-status "$MARKET_DIR/dashboard-status.json" \
  --result-out "$MARKET_DIR/paper-check-$STAMP.json" \
  --candidate-policy-out "$MARKET_DIR/checked-policy-$STAMP.json"

# These commands are reached only after the check passes. They bind the paper
# book and runner to checked-policy-$STAMP.json, never to the base policy that
# the training search changed.
sudo -u mithril-agent-research /usr/local/libexec/mithril-agent/mithril-agent \
  shadow portfolio \
  --out "$MARKET_DIR/paper-portfolio-$STAMP.json" \
  --limit-usd 270 --max-sol-usd 300 \
  --book "wif=$MARKET_DIR/checked-policy-$STAMP.json"

printf '%s\n' \
  "MITHRIL_AGENT_PAPER_POLICY=$MARKET_DIR/checked-policy-$STAMP.json" \
  "MITHRIL_AGENT_PAPER_ARTIFACT=$MARKET_DIR/provisional-$STAMP.json" \
  "MITHRIL_AGENT_PAPER_CHECK=$MARKET_DIR/paper-check-$STAMP.json" \
  "MITHRIL_AGENT_PAPER_PORTFOLIO=$MARKET_DIR/paper-portfolio-$STAMP.json" \
  "MITHRIL_AGENT_PAPER_RUN_DIR=/var/lib/mithril-agent-market-paper-wif/run-$STAMP" | \
  sudo install -o root -g root -m 0600 /dev/stdin \
  /etc/mithril-agent/market-paper-wif.env
sudo systemctl start mithril-agent-market-paper@wif.service
sudo systemctl is-active mithril-agent-market-paper@wif.service
sudo systemctl start mithril-agent-market-candidate@wif.service
trap - EXIT
```

`systemctl start` waits for the runner's readiness notification, which is sent
only after the policy, passing paper check, evidence journal, portfolio, sources,
paper journal, and alert output have opened successfully. Repeat the same guarded
script for `jto` or `pyth`, substituting the allowlisted market name and instance.
Each timestamped experiment keeps its own policy, paper-check result, portfolio,
and run journal; alert status and its read-only socket are shared only by that
market instance. A failed check leaves its runner stopped.

This is a faster development gate, not evidence that a strategy is profitable.
The 30-day artifact remains the stronger market-admission evidence and continues
to be required before any later real-money review.

Each collector also replaces a private, bounded `dashboard-status.json` in its
own market directory. The dashboard never reads those research directories.
Instead, a one-shot receives the exact WIF, JTO, and PYTH snapshots through
systemd credentials, validates and combines them, and atomically replaces the
dashboard-owned projection. Install its service and one-minute timer together
with the updated dashboard service:

```sh
sudo install -o root -g root -m 0644 \
  systemd/mithril-agent-market-status.service \
  systemd/mithril-agent-market-status.timer \
  systemd/mithril-agent-paper-dashboard.service \
  /etc/systemd/system/
sudo systemd-analyze verify \
  /etc/systemd/system/mithril-agent-market-status.service \
  /etc/systemd/system/mithril-agent-market-status.timer \
  /etc/systemd/system/mithril-agent-paper-dashboard.service
sudo systemctl daemon-reload
sudo systemctl restart mithril-agent-market-candidate@wif.service \
  mithril-agent-market-candidate@jto.service \
  mithril-agent-market-candidate@pyth.service
sudo systemctl start mithril-agent-market-status.service
sudo systemctl show mithril-agent-market-status.service \
  -p Result -p ExecMainStatus -p ExecMainStartTimestamp
sudo stat -Lc '%U:%G %a %s' \
  /var/lib/mithril-agent-dashboard/market-admission.json
sudo systemctl enable --now mithril-agent-market-status.timer
sudo systemctl restart mithril-agent-paper-dashboard.service
```

Before enabling the timer, the one-shot must report `Result=success` and
`ExecMainStatus=0`. Its output must be a non-empty mode-`600` file owned by
`mithril-agent-dashboard:mithril-agent-dashboard`.

The publisher has no network access and cannot traverse
`/var/lib/mithril-agent-research`; systemd copies only the three named status
files into its private credential directory. A missing, malformed, or
wrong-market snapshot makes that refresh fail without replacing the last valid
projection. A valid but stale snapshot is published with `fresh=false`, so the
dashboard clearly labels its data delayed. The projection reports paper-only
collection readiness for a separate qualification check. It does not make a
market tradable, select a policy, or authorize a transaction. Check both the
last successful refresh and collector health:

```sh
sudo systemctl status mithril-agent-market-status.timer
sudo journalctl -u mithril-agent-market-status.service
sudo systemctl is-active mithril-agent-market-candidate@wif.service \
  mithril-agent-market-candidate@jto.service \
  mithril-agent-market-candidate@pyth.service
```

PUMP remains excluded. Its canonical mint uses Token-2022 while the current
collector deliberately validates the legacy fixed-size mint layout; treating
those layouts as interchangeable would weaken the mint and authority checks.

After at least 30 complete days, stop the collector briefly and evaluate a new,
date-named immutable artifact. Qualification proves current source and route
quality only; it neither starts trading nor proves profit. A candidate paper
policy must use that artifact's exact `$25` notional, `100` bps slippage,
observer, source limits, mint, decimals, and journal prefix. The admitted
runner closes at the next UTC
boundary and refuses another day until a newly reviewed rolling-window
artifact and policy are supplied. Keep that handoff operator-reviewed; do not
silently auto-promote a token because a timer ran.

```sh
sudo systemctl stop mithril-agent-market-candidate@wif.service
sudo -u mithril-agent-research /usr/local/libexec/mithril-agent/mithril-agent \
  shadow market evaluate \
  --journal /var/lib/mithril-agent-research/market-admission-wif/evidence.jsonl \
  --out /var/lib/mithril-agent-research/market-admission-wif/wif-YYYY-MM-DD.json
sudo systemctl start mithril-agent-market-candidate@wif.service
```

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
sudo install -o mithril-agent-research -g mithril-agent-research -m 0600 \
  AGENTS.md state/AGENTS.md
```

Keep the root `.no-bundled-skills` marker created above. Hermes retains its
essential `hermes-agent` operating-manual skill, but must not seed or install
any other skill in this deployment. The `skills` toolset remains disabled. Do
not share this writable state directory with another Hermes process; the
delegated phase receives only `auth.json` read-only and uses ephemeral runtime
state.

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

Run the base checks through pinned one-off containers before starting the
timer. The index and paper MCP checks require a healthy rooted index and an
automatically bootstrapped paper champion respectively; run the gated checks only after those
inputs exist. The static Compose bind intentionally cannot create its host
runtime directory. Create that directory for this manual preflight; the normal
systemd service creates it automatically and removes its ephemeral files.

```sh
sudo install -d -o root -g root -m 0711 /run/mithril-hermes-research
sudo install -d -o mithril-agent-research -g mithril-agent-research -m 0700 \
  /run/mithril-hermes-research/research-state
sudo install -o mithril-agent-research -g mithril-agent-research -m 0600 \
  /dev/null /run/mithril-hermes-research/research-state/.no-bundled-skills
sudo docker compose run --rm hermes-research-parallel config check
sudo docker compose run --rm hermes-research-parallel doctor
sudo docker compose run --rm hermes-research id
sudo docker compose run --rm hermes-research config check
sudo docker compose run --rm hermes-research doctor
sudo docker compose run --rm hermes-research mcp list
sudo docker compose run --rm hermes-research mcp test solana_docs
sudo docker compose run --rm hermes-research tools list --platform cli
sudo docker compose run --rm hermes-research tools list --platform telegram
sudo docker compose run --rm hermes-research tools list --platform cron
sudo docker compose run --rm hermes-research python -c \
  'from hermes_cli.config import load_config; from hermes_cli.tools_config import _get_platform_tools; c = load_config(); expected = {"cli": {"web", "mithril_index", "mithril_paper", "mithril_paper_jup", "solana_docs"}, "telegram": {"web", "mithril_index", "solana_docs"}, "cron": {"web", "mithril_index", "mithril_paper", "mithril_paper_jup", "solana_docs"}}; got = {p: set(_get_platform_tools(c, p)) for p in expected}; assert got == expected, got; print({p: sorted(v) for p, v in got.items()})'
sudo docker compose run --rm hermes-research python -c \
  'from hermes_cli.config import load_config; from hermes_cli.tools_config import _get_platform_tools; from model_tools import get_tool_definitions; c = load_config(); disabled = c.get("agent", {}).get("disabled_toolsets", []); names = {p: {d["function"]["name"] for d in get_tool_definitions(sorted(_get_platform_tools(c, p)), disabled, quiet_mode=True, skip_tool_search_assembly=True)} for p in ("cli", "telegram", "cron")}; assert all({"web_search", "web_extract"} <= value for value in names.values()), names; assert not any(name.startswith("browser_") or name == "browser_exec" for value in names.values() for name in value), names; print({p: sorted({"web_search", "web_extract"} & value) for p, value in names.items()})'
sudo docker compose run --rm hermes-research python -c \
  'import logging; from hermes_cli.mcp_startup import ensure_mcp_discovery_before_agent_build; ensure_mcp_discovery_before_agent_build(logger=logging.getLogger(__name__), single_query=True); from hermes_cli.config import load_config; from model_tools import get_tool_definitions; c = load_config(); disabled = c.get("agent", {}).get("disabled_toolsets", []); want = {"web_search", "web_extract", "mcp__solana_docs__list_sections", "mcp__solana_docs__get_documentation"}; got = {d["function"]["name"] for d in get_tool_definitions(enabled_toolsets=["web", "solana_docs"], disabled_toolsets=disabled, quiet_mode=True, skip_tool_search_assembly=True)}; assert got == want, sorted(got ^ want); print("pre-champion tools:", len(got))'
sudo docker compose run --rm hermes-research-parallel python -c \
  'import logging; from hermes_cli.mcp_startup import ensure_mcp_discovery_before_agent_build; ensure_mcp_discovery_before_agent_build(logger=logging.getLogger(__name__), single_query=True); from hermes_cli.config import load_config; from model_tools import get_tool_definitions; c = load_config(); disabled = c.get("agent", {}).get("disabled_toolsets", []); got = {d["function"]["name"] for d in get_tool_definitions(enabled_toolsets=["web", "solana_docs", "delegation"], disabled_toolsets=disabled, quiet_mode=True, skip_tool_search_assembly=True)}; assert "delegate_task" in got and not any("mithril_paper" in name for name in got), sorted(got); print("delegated research tools:", len(got))'
test "$(sudo find /run/mithril-hermes-research/research-state/skills \
  -name SKILL.md -printf '%P\n')" = \
  'autonomous-ai-agents/hermes-agent/SKILL.md'
# After the rooted index and SOL champion exist:
sudo docker compose run --rm hermes-research mcp test mithril_index
# Adaptive policies only; fixed policies need no instruction snapshot.
if sudo test -e /var/lib/mithril-agent-dashboard/instruction.json; then
  sudo sh -c '/usr/sbin/runuser -u mithril-agent-dashboard -- \
    /usr/local/libexec/mithril-agent/mithril-agent-paper-dashboard \
    --export-instruction /var/lib/mithril-agent-dashboard/instruction.json \
    > /run/mithril-hermes-research/instruction.json'
  sudo chown mithril-agent-research:mithril-agent-research \
    /run/mithril-hermes-research/instruction.json
  sudo chmod 0600 /run/mithril-hermes-research/instruction.json
fi
sudo docker compose run --rm hermes-research mcp test mithril_paper
# After the separate JUP champion also exists:
sudo docker compose run --rm hermes-research mcp test mithril_paper_jup
sudo docker compose run --rm hermes-research python -c \
  'from tools.mcp_tool import discover_mcp_tools, shutdown_mcp_servers; expected = {"mithril_index": {"mithril_index_status", "mithril_index_accounts", "mithril_index_transactions"}, "mithril_paper": {"mithril_paper_create_challenger", "mithril_paper_challenge_status"}, "mithril_paper_jup": {"mithril_paper_create_challenger", "mithril_paper_challenge_status"}, "solana_docs": {"list_sections", "get_documentation"}}; want = {f"mcp__{server}__{tool}" for server, tools in expected.items() for tool in tools}; got = set(discover_mcp_tools()); assert got == want, sorted(got ^ want); print("effective MCP tools:", len(got)); shutdown_mcp_servers()'
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
sudo rm -f /run/mithril-hermes-research/instruction.json
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
that the model receives exactly the configured 9 MCP tools after both paper
champions exist. An
authentication failure, a missing tool, or an extra effective tool is a failed
deployment.
The four explicit `trust: full` entries are authorization for only that
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

The scheduled one-shot snapshots the dashboard-owned canonical experiment
requirement into its private runtime directory. Both paper policies deployed by
this profile are adaptive, so their paper toolsets fail closed unless that
snapshot and the matching champion exist. The `research-mcp` command still
supports a separately invoked fixed policy when no adaptive research packet is
configured; that is outside this profile. Every adaptive candidate binds the
snapshot digest and rejects unsupported dollar sizing or mismatched cadence and
drawdown. The delegated research registry contains web
search/extraction, the two read-only Solana documentation tools, and
`delegate_task`; it may add the three index tools only after
`index doctor --max-record-age 15m` passes. It never contains a paper tool. The
separate finalizer receives each market's paper tools only after its champion
and supervised lifecycle are healthy, and never receives `delegate_task`.
The index MCP server repeats that same check at startup and before every tool
call. Repeat the exact
post-filter registry assertion after each gate opens. The static CLI, Telegram,
and cron resolver assertions remain upgrade checks for the underlying profile;
they are not proof of the one-shot's dynamic runtime registry. In Hermes
v2026.8.27, `tools list --platform` prints every globally configured MCP server
and its include filter, even when that server is not a member of the selected
platform. Use those listings to inspect the built-in toolsets and global MCP
filters only.
`mcp test` remains the raw discovery/schema check; neither it nor a toolset
summary substitutes for the post-filter registry assertion.
Neither phase may contain terminal, process, file mutation, code execution,
browser automation, skills, memory mutation, cron mutation, messaging, wallet,
signer, submitter, program build, signing, sending, submission, or
service-control tools. Delegation exists only in the mount-isolated research
phase.

Only after that review should the systemd-owned one-shot start. Do not run
`docker compose up` directly and do not add a Docker restart policy: the unit
orders every run after the egress boundary and requires the OAuth file and
paper policy. The wrapper starts the isolated research phase with
`web,solana_docs,delegation`, adds `mithril_index` only after the bounded-age
doctor passes, validates its packet, and then starts the non-delegating
finalizer only when a healthy paper champion makes a paper toolset available.

```sh
sudo systemctl start mithril-hermes-research.service
sudo systemctl status mithril-hermes-research.service
sudo journalctl -u mithril-hermes-research.service
```

## First recurring brief

The manual one-shot above uses the exact reviewed prompt in
`prompts/market-scout.md`. Inspect its cited sources and confirm it created no
challenger before a champion exists. Then enable the fixed native schedule;
there is no reason to wait for the first champion to keep collecting briefs:

```sh
sudo systemctl enable --now mithril-hermes-research.timer
sudo systemctl list-timers mithril-hermes-research.timer
```

The paper-testing timer starts hourly at minute 15 with up to five minutes of
jitter, and catches up after downtime. Research output is retained in the
systemd journal rather than sent to Telegram. Once the paper tool becomes
available, a successful research call may change only the dedicated paper
challenger pointer. The separately confined auto-selector decides only the
paper champion after the fixed forward gate; Hermes cannot call it. An identical
digest in the champion pointer is the durable paper-selection acknowledgement and permits the next research cycle without
deleting or resetting either observer.

The auto-selector keeps a local, hash-chained SOL or JUP outcome journal. An
operator can inspect its bounded read-only summary with `shadow
research-outcomes --journal PATH --limit 16`. Outcome feedback to the next
Hermes scout is disabled by default. After direct operator approval, add a
systemd service override containing
`Environment=MITHRIL_HERMES_OUTCOME_FEEDBACK=1`; the wrapper then adds only each
journal's `--prompt-safe --limit 8 --policy CURRENT --max-age 168h` projection.
The command strictly loads and fingerprints the current market policy, verifies
and folds the complete journal, filters out other policy fingerprints, markets,
and older outcomes, and only then applies the limit. Only a journal with no
active, staged `.next`, `.lock`, or `.seg-*` artifact is omitted; any artifact
invokes the strict verifier, so incomplete, invalid, or future-dated state stops
the run. These hints are internal advisory evidence: they do not count as
external sources and cannot authorize, activate, select, promote, or execute
anything. The shipped unit does not enable this option, and JUP outcomes are
ignored when the current allocation has no JUP policy.

Malformed replies and pre-publication validation failures keep the last
validated research packet and dashboard research projection unchanged. The
runner permits one isolated retry before publication; malformed model JSON is never repaired or
partially accepted. The retry gets fresh state, timestamps, and a Hermes session
trace. Once validated evidence may be persisted or the bounded paper finalizer
may run, the retry phase is over and later failures do not repeat the workflow.
The complete unit retains an 18-minute ceiling for two five-minute research
attempts, one five-minute finalizer, and bounded validation overhead.

Hourly scans accelerate paper experiments without granting control over a
wallet, policy, or selector. Monitor the pinned
provider/model, timer age, service result, Codex usage-limit errors, and keyless
search/extraction failures. Before upgrade validation against this state, stop
the timer and wait for the one-shot service to become inactive:

```sh
sudo systemctl stop mithril-hermes-research.timer
sudo systemctl is-active mithril-hermes-research.service
```

After validation, run one manual canary and restore the schedule:

```sh
sudo systemctl start mithril-hermes-research.service
sudo systemctl enable --now mithril-hermes-research.timer
```

## Freshness and recovery

Do not equate `mithril_index_status.complete` with current data. It proves that
the stored prefix is internally complete, not that ingestion is live. The
profile now admits the index only when its complete, hash-verified journal has
a record no more than 15 minutes old, and the MCP server repeats that check on
every call. This proves recent local ingestion only: the timestamp comes from
the index host and does not prove parity with the node's current root. Until a
healthy supervised ingester and an independently trusted current-root check
are both enforced, treat rooted-index findings as bounded historical evidence
and say so in every brief.

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
runners, canary Hermes MCP/schema/search, and enable the timer last. Keep the
timer stopped until freshness and delivery checks pass.

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
and compare the full tool list before enabling the timer. Roll back by stopping
the timer and restoring the complete previous Compose, prompt, service, image,
and config set. If that set used the persistent gateway, restore its matching
`ExecStop` ownership semantics before starting it. Preserve the profile state
for audit rather than copying it into a broader profile.
