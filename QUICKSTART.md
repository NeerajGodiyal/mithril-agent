# Mithril Agent full-strategy quick start

This is the supported first-run path for the current Devnet pilot. It creates
one bounded strategy with sell, buy, sweep, Telegram alerts, and read-only MCP
status. Follow it in order. Do not mix these commands with the legacy
single-trade units in `deploy/systemd`.

The result is deliberately limited:

- Devnet only;
- one fixed SOL/devUSDC route;
- Telegram and MCP are read-only;
- the agent uses a dedicated limited-balance account, not your main wallet;
- every trading grant expires and has an action limit; and
- the first sell is a one-time bootstrap before the buy leg can be created.

This guide assumes you already know how to run a Mithril full node or RPC node.
Use a full-node/RPC host, not a voting validator host.

## 1. Use the matching Mithril build

The current end-to-end candidate is the public Mithril integration branch:

```text
repository: https://github.com/NeerajGodiyal/mithril
branch:     koro/agent-node-integration-wip
commit:     73497e69faed01e4952f61ae241a76958916b1bc
```

That candidate combines the MCP, RPC-evidence, and node-monitoring branches
used by this agent. Build and run that exact Mithril revision for this review.
Keep Mithril's RPC on a literal loopback address; the agent does not support an
external submission fallback.

Before continuing, require all four:

```sh
test -x /usr/local/bin/mithril
systemctl is-active YOUR_MITHRIL_SERVICE
curl --fail --silent --show-error \
  --header 'content-type: application/json' \
  --data '{"jsonrpc":"2.0","id":1,"method":"getEpochInfo","params":[]}' \
  http://127.0.0.1:8899 >/dev/null
curl --fail --silent --show-error http://127.0.0.1:9090/metrics >/dev/null
```

Replace the service name and loopback ports only when your Mithril deployment
uses different ones. If the Mithril binary is elsewhere, use that absolute path
in step 8. The node must be advancing near the Devnet tip; the agent correctly
refuses a node that is still catching up.

## 2. Build and verify Mithril Agent

Clone this repository on the Linux host, then run:

```sh
make prereqs
make verify-source
make test
make build
make adapter
```

Do not continue if any command fails. `make build` produces seven Go binaries;
all seven must be installed together. The quote adapter also needs the pinned
Node.js runtime and its installed `node_modules`. At this early step,
`make prereqs` may also say that RPC variables are not set. That is expected;
step 4 puts them in protected service files instead of your login shell.

## 3. Install the runtime and service identities

Install the service accounts first:

```sh
sudo install -m 0644 deploy/sysusers/mithril-agent-status.conf \
  /usr/lib/sysusers.d/mithril-agent-status.conf
sudo systemd-sysusers /usr/lib/sysusers.d/mithril-agent-status.conf
```

Install the verified runtime:

```sh
sudo install -d -o root -g root -m 0755 /usr/local/libexec/mithril-agent
sudo install -d -o root -g root -m 0755 /usr/local/share/doc/mithril-agent
sudo install -o root -g root -m 0644 README.md QUICKSTART.md DEMO.md \
  /usr/local/share/doc/mithril-agent/
sudo install -o root -g root -m 0755 \
  ./bin/mithril-agent \
  ./bin/mithril-agent-policy \
  ./bin/mithril-agent-signer \
  ./bin/mithril-agent-submitter \
  ./bin/mithril-agent-quote \
  ./bin/mithril-agent-telegram \
  ./bin/mithril-agent-status-bridge \
  /usr/local/libexec/mithril-agent/
sudo install -o root -g root -m 0755 "$(command -v node)" \
  /usr/local/libexec/mithril-agent/node
sudo install -o root -g root -m 0644 adapters/orca/quote.mjs \
  /usr/local/libexec/mithril-agent/quote.mjs
sudo install -d -o root -g root -m 0755 \
  /usr/local/libexec/mithril-agent/node_modules
sudo cp -a adapters/orca/node_modules/. \
  /usr/local/libexec/mithril-agent/node_modules/
sudo chown -R root:root /usr/local/libexec/mithril-agent
sudo chmod -R go-w /usr/local/libexec/mithril-agent
sudo ln -sfn /usr/local/libexec/mithril-agent/mithril-agent \
  /usr/local/bin/mithril-agent
```

Check that the installed command can find every sibling helper:

```sh
/usr/local/bin/mithril-agent version
for name in mithril-agent mithril-agent-policy mithril-agent-signer \
  mithril-agent-submitter mithril-agent-quote mithril-agent-telegram \
  mithril-agent-status-bridge; do
  test -x "/usr/local/libexec/mithril-agent/$name" || exit 1
done
```

## 4. Create the protected environment

This pilot needs four RPC URLs:

1. your loopback Mithril RPC;
2. one HTTPS endpoint used only by the quote sidecar;
3. a primary HTTPS evidence RPC; and
4. a secondary HTTPS evidence RPC operated by a different provider.

The two evidence providers must be independent. Two keys or hostnames from one
provider are not independent evidence.

Create the files, then edit them as root:

```sh
sudo install -d -o root -g root -m 0700 /etc/mithril-agent
sudo touch /etc/mithril-agent/rpc.env \
  /etc/mithril-agent/quote.env \
  /etc/mithril-agent/mcp.env \
  /etc/mithril-agent/price.env \
  /etc/mithril-agent/telegram-operator.env
sudo chown root:root /etc/mithril-agent/*.env
sudo chmod 0600 /etc/mithril-agent/*.env
```

`/etc/mithril-agent/rpc.env`:

```text
MITHRIL_AGENT_MITHRIL_RPC_URL=http://127.0.0.1:8899
MITHRIL_AGENT_PRIMARY_RPC_URL=https://FIRST-INDEPENDENT-PROVIDER/...
MITHRIL_AGENT_SECONDARY_RPC_URL=https://SECOND-INDEPENDENT-PROVIDER/...
```

`/etc/mithril-agent/quote.env`:

```text
MITHRIL_AGENT_QUOTE_RPC_URL=https://QUOTE-PROVIDER/...
```

`/etc/mithril-agent/mcp.env` contains the nonsecret local Mithril paths and
loopback addresses needed by `mithril mcp`, for example:

```text
MITHRIL_RPC_URL=http://127.0.0.1:8899
MITHRIL_METRICS_URL=http://127.0.0.1:9090/metrics
MITHRIL_STATE_PATH=/absolute/path/to/mithril_state.json
MITHRIL_REPLAY_PATH=/absolute/path/to/replay_timings.jsonl
MITHRIL_BLOCK_SOURCE=rpc
```

Add the actual accounts, snapshots, shredstore, log, and optional cgroup paths
reported by your running Mithril deployment. `mithril mcp --help` lists every
supported override. `rpc` is the default source for a classic Devnet node;
replace it only when the running node is explicitly configured for another
source. Do not copy paths or a source value from another host or an old run.

The restricted agent must be able to traverse the storage parent and read the
state and replay files without being able to list or rewrite AccountsDB. Follow
the narrow `mithril-node-state` group and ACL block in README's "Service
identities and filesystem layout" section, then verify as the service identity:

```sh
sudo -u mithril-agent test -r /absolute/path/to/mithril_state.json
sudo -u mithril-agent test -r /absolute/path/to/replay_timings.jsonl
```

Do not solve a failed check with broad recursive read permissions.

Leave `/etc/mithril-agent/price.env` empty when using the default on-chain Pyth
push feed. `MITHRIL_AGENT_PYTH_API_KEY` is required only when deliberately
selecting the optional Hermes HTTP source.

## 5. Install a dedicated Devnet account

Use a separate, limited-balance Devnet keypair for the agent. The agent never
imports your main wallet. Your Phantom, Solflare, or CLI wallet is used only as
the sweep destination and signs a proof of control during setup.

Create the dedicated Devnet-only account with the built-in command:

```sh
sudo install -d -o mithril-agent -g mithril-agent -m 0700 \
  /var/lib/mithril-agent /var/lib/mithril-agent/private
sudo -u mithril-agent env HOME=/var/lib/mithril-agent \
  /usr/local/bin/mithril-agent wallet new \
  --file /var/lib/mithril-agent/private/devnet-keypair.json
sudo -u mithril-agent /usr/local/bin/mithril-agent wallet check \
  --file /var/lib/mithril-agent/private/devnet-keypair.json
```

`wallet new` refuses to replace an existing file. If you already have a
dedicated Devnet keypair, install it at that path as
`mithril-agent:mithril-agent` with mode `0600` instead. Fund only this account
with enough Devnet SOL for the configured trade size, fees, rent, and reserve.
Devnet SOL has no value. `mithril-agent start` reports the public address and
any shortfall without printing the private key.

## 6. Connect Telegram before trading

Put only the bot token in `/etc/mithril-agent/telegram-operator.env`, send the
bot `hello` from Telegram, then discover the numeric chat ID through the same
protected environment the service will use:

```sh
sudo systemd-run --quiet --wait --pipe --collect \
  --uid=mithril-agent-telegram --gid=mithril-agent-telegram \
  -p 'EnvironmentFile=/etc/mithril-agent/telegram-operator.env' \
  /usr/local/libexec/mithril-agent/mithril-agent-telegram link
```

Edit the file so it contains:

```text
MITHRIL_AGENT_TELEGRAM_BOT_TOKEN=YOUR_BOT_TOKEN
MITHRIL_AGENT_TELEGRAM_CHAT_IDS=YOUR_NUMERIC_CHAT_ID
MITHRIL_AGENT_TELEGRAM_EXPLANATIONS=off
```

Do not put an OpenAI key in the file while explanations are off. Optional LLM
explanations can be added later; they are not part of trading decisions.

Prove delivery now:

```sh
sudo systemd-run --quiet --wait --pipe --collect \
  --uid=mithril-agent-telegram --gid=mithril-agent-telegram \
  -p 'EnvironmentFile=/etc/mithril-agent/telegram-operator.env' \
  /usr/local/libexec/mithril-agent/mithril-agent-telegram test
```

Continue only after every configured chat reports `delivered`.

## 7. Install the quote service and clock policy

```sh
sudo install -m 0644 deploy/systemd/mithril-agent-quote.service \
  /etc/systemd/system/
sudo install -d -m 0755 /etc/systemd/timesyncd.conf.d
sudo install -m 0644 deploy/timesyncd/90-mithril-agent.conf \
  /etc/systemd/timesyncd.conf.d/90-mithril-agent.conf
sudo systemd-analyze verify \
  /etc/systemd/system/mithril-agent-quote.service
sudo systemctl daemon-reload
sudo systemctl restart systemd-timesyncd
sudo systemctl enable --now mithril-agent-quote.service
test "$(timedatectl show --property=NTPSynchronized --value)" = yes
```

Do not continue if the quote service or time synchronization check fails. The
strategy's own preflight repeats the stricter, profile-specific clock check
after setup has created a configuration.

## 8. Create the all-in-one strategy

Run the guided setup as the restricted service identity. It asks plain
questions, stores one editable strategy file, confirms the live quote, and
guides the payout-wallet proof. Nothing is armed by setup.

```sh
sudo systemd-run --quiet --wait --pty --collect \
  --uid=mithril-agent --gid=mithril-agent \
  --setenv=HOME=/var/lib/mithril-agent \
  --property=UMask=0077 \
  --property=EnvironmentFile=/etc/mithril-agent/rpc.env \
  --property=EnvironmentFile=/etc/mithril-agent/quote.env \
  --property=EnvironmentFile=-/etc/mithril-agent/mcp.env \
  --property=EnvironmentFile=-/etc/mithril-agent/price.env \
  /usr/local/libexec/mithril-agent/mithril-agent setup strategy \
  --wallet-keypair /var/lib/mithril-agent/private/devnet-keypair.json \
  --mithril-command /usr/local/bin/mithril \
  --node-command /usr/local/libexec/mithril-agent/node \
  --quote-script /usr/local/libexec/mithril-agent/quote.mjs \
  --quote-socket /run/mithril-agent-quote/quote.sock
```

Choose **yes** when setup asks whether Telegram alerts are enabled. For a quick
mechanics demonstration, choose no price conditions. For a market
rule, set both a sell price and a lower buy price. Setting only one is refused.
Choose a small trade size and a small `trades_per_day` bound for the first run.

The setup writes sell and sweep first. A fresh wallet has no devUSDC token
account, so the buy leg is intentionally pending until the first sell creates
it.

## 9. Generate and install the strategy services

Generate the runner, one read-only status socket per configured leg, and the
single Telegram alert service from the recorded strategy:

```sh
sudo -u mithril-agent env HOME=/var/lib/mithril-agent \
  /usr/local/libexec/mithril-agent/mithril-agent service install \
  --output /var/lib/mithril-agent/.mithril-agent/mithril-agent-run.service
```

Review every generated file, then run the exact install, enable, and restart
commands printed by that command. Restart is intentional: every generated
runner start revokes old authority before doing any work.

Do **not** install or start these legacy single-leg units for a full strategy:

```text
mithril-agent-swap.service
mithril-agent-demo.service
mithril-agent-status.socket
mithril-agent-status-bridge.service
mithril-agent-telegram.service
```

The full strategy uses `mithril-agent-run.service`, per-leg status units, and
`mithril-agent-alerts.service`. Running both layouts creates competing runners
or Telegram consumers.

Verify the generated layout:

```sh
systemctl is-active mithril-agent-run.service
systemctl is-active mithril-agent-alerts.service
systemctl is-active mithril-agent-status-sell.socket
systemctl is-active mithril-agent-status-sweep.socket
sudo -u mithril-agent env HOME=/var/lib/mithril-agent \
  /usr/local/bin/mithril-agent strategy show
```

The first runner exposes sell metrics on `127.0.0.1:9310` and sweep metrics on
`127.0.0.1:9312`; `9311` stays reserved for the pending buy leg.

## 10. Bootstrap the buy leg once

Run the path-free readiness command as the service identity:

```sh
sudo systemd-run --quiet --wait --pipe --collect \
  --uid=mithril-agent --gid=mithril-agent \
  --setenv=HOME=/var/lib/mithril-agent \
  -p 'EnvironmentFile=/etc/mithril-agent/rpc.env' \
  -p 'EnvironmentFile=/etc/mithril-agent/quote.env' \
  -p 'EnvironmentFile=-/etc/mithril-agent/mcp.env' \
  -p 'EnvironmentFile=-/etc/mithril-agent/price.env' \
  /usr/local/bin/mithril-agent start
```

When it reports ready, use the same arguments it prints through the service
identity. Use one trade per leg for bootstrap and include `--allow-any-price`
only when `start` printed it:

```sh
sudo -u mithril-agent env HOME=/var/lib/mithril-agent \
  /usr/local/bin/mithril-agent strategy enable \
  --duration 8h --max-trades 1 --reason 'bootstrap sell'
```

If setup used no price conditions, add `--allow-any-price` immediately before
`--reason`. Do not add it to a price-triggered strategy.

Wait for the sell to complete and confirm its Telegram message. Then stop all
new actions:

```sh
sudo -u mithril-agent env HOME=/var/lib/mithril-agent \
  /usr/local/bin/mithril-agent strategy stop --reason 'bootstrap sell complete'
```

Create the buy leg from the saved settings:

```sh
sudo systemd-run --quiet --wait --pty --collect \
  --uid=mithril-agent --gid=mithril-agent \
  --setenv=HOME=/var/lib/mithril-agent \
  --property=UMask=0077 \
  --property=EnvironmentFile=/etc/mithril-agent/rpc.env \
  --property=EnvironmentFile=/etc/mithril-agent/quote.env \
  --property=EnvironmentFile=-/etc/mithril-agent/mcp.env \
  --property=EnvironmentFile=-/etc/mithril-agent/price.env \
  /usr/local/libexec/mithril-agent/mithril-agent setup strategy --resume
```

Run step 9 again. The printed restart commands are required so the existing
runner and Telegram service load the new buy leg. Then require:

```sh
systemctl is-active mithril-agent-status-buy.socket
sudo -u mithril-agent env HOME=/var/lib/mithril-agent \
  /usr/local/bin/mithril-agent strategy show
curl --fail --silent http://127.0.0.1:9310/metrics >/dev/null
curl --fail --silent http://127.0.0.1:9311/metrics >/dev/null
curl --fail --silent http://127.0.0.1:9312/metrics >/dev/null
```

`strategy show` must list sell, buy, and sweep. All three start stopped after
the service restart.

## 11. Run and operate the complete strategy

Run the readiness command from step 10 again. Review its bounded enable
arguments, then run `strategy enable` through the same `sudo -u mithril-agent
env HOME=...` wrapper shown in step 10. The supervised runner continues after
SSH closes and trades only while that grant remains valid.

Read-only operator commands:

```sh
sudo -u mithril-agent env HOME=/var/lib/mithril-agent \
  /usr/local/bin/mithril-agent strategy show
```

Telegram:

```text
/help
/status
/price
/last_trade
```

Stop every leg at any time:

```sh
sudo -u mithril-agent env HOME=/var/lib/mithril-agent \
  /usr/local/bin/mithril-agent strategy stop --reason 'operator stop'
```

After stopping, `strategy show` must report every leg stopped. A missing
Telegram message is never proof that no trade happened; use strategy status
and the journal as the record.

## 12. Optional read-only MCP

The generated strategy has one bounded socket per leg:

```text
/run/mithril-agent-status-sell.sock
/run/mithril-agent-status-buy.sock
/run/mithril-agent-status-sweep.sock
```

The current MCP command reads one socket per process. Configure one read-only
MCP entry for each leg you want the client to see. Example for the sell leg:

```text
command: /usr/local/libexec/mithril-agent/mithril-agent
args:    mcp --status-socket /run/mithril-agent-status-sell.sock
```

Use distinct client names such as `mithril-agent-sell`,
`mithril-agent-buy`, and `mithril-agent-sweep`. There is no combined
multi-leg MCP socket in this version. Telegram `/status` and `strategy show`
are the combined views.

## 13. Verify the three journals

The live runner owns all journal locks. Stop it before verification; restarting
it returns every leg to stopped mode:

```sh
sudo systemctl stop mithril-agent-run.service
for leg in sell buy sweep; do
  sudo -u mithril-agent \
    /usr/local/bin/mithril-agent journal verify \
    --path "/var/lib/mithril-agent/.mithril-agent/strategy/$leg/state/events.jsonl" \
    || exit 1
done
sudo systemctl start mithril-agent-run.service
```

If setup used a custom `--dir`, use that directory instead of the default path.
Do not hand-edit a journal, configuration, policy, control file, or generated
unit.

## When to stop

Stop and do not enable the strategy when any of these is true:

- Mithril is stale, diverged, rebuilding, or more than the configured slot gap
  behind;
- the quote service, either evidence provider, clock check, status socket,
  Telegram test, or monitoring target fails;
- `strategy show` reports an unreadable leg or `attention_required`;
- a transaction outcome is unresolved; or
- a service restarted unexpectedly.

For upgrades, recovery, provider rotation, monitoring, and incident handling,
use [README.md](README.md). For a short reviewer walkthrough after this setup is
complete, use [DEMO.md](DEMO.md).
