# Mithril Agent Devnet demonstration

This guide is the short path for reviewing an already installed pilot. It does
not replace the full installation and operations reference in `README.md`.

## What the demonstration proves

The pilot can:

- read health and slot evidence from a Mithril Devnet node;
- validate one fixed Devnet swap route and its limits;
- permit at most one trade, sign it in a separate process, and submit it only
  through Mithril RPC;
- confirm the outcome using independent read providers;
- return to stopped mode and preserve a hash-chained audit record;
- expose bounded status through MCP, Telegram, and Prometheus.

It does not enable Mainnet trading, arbitrary tokens, recurring strategies, or
trade approval from Telegram or an LLM. Shadow mode does *read* Mainnet, but it
holds no key and cannot sign; see "Watching a real market" below.

## Handing this to someone non-technical

Split it in two: an administrator prepares the host once, then the reviewer runs
two commands. The reviewer needs no path, no flag, and no configuration file.

**One-time, by an administrator with root** (about five minutes):

```sh
# 1. Put the command on PATH, so nobody has to type /usr/local/libexec/...
sudo ln -sfn /usr/local/libexec/mithril-agent/mithril-agent /usr/local/bin/mithril-agent

# 2. Let the reviewer read the read-only status socket.
sudo usermod -aG mithril-agent-status REVIEWER
# The reviewer must log out and back in for the group to take effect.

# 3. Run the guided setup as the reviewer, once.
sudo -u REVIEWER mithril-agent setup
```

Step 3 is where an administrator is genuinely needed. Setup finds the node
runtime, the quote adapter and the Mithril executable by itself, but it asks for
one path it cannot discover: the Mithril node's own `config.toml`, which lives
in a directory a reviewer is not permitted to read. An administrator knows it;
a reviewer never will. Running setup as the reviewer records the result in
their home, so everything afterwards works without a path.

Without step 1 the reviewer types an absolute path; without step 2 even
read-only status is refused. Neither weakens anything: the socket stays
read-only and the group grants nothing but reading it.

**Then the reviewer runs two things** — the runner, left going, and the
authorisation:

```sh
mithril-agent swap run --config PATH    # leave this running
mithril-agent demo                      # arms ONE bounded trade
```

`demo` authorises; the runner executes. If the runner is not going, `demo` says
so and names the command that starts it.

`setup` recorded where it put things and `demo` finds it, which is why no path
is needed. If anything is wrong, `mithril-agent doctor` says what and prints the
command that fixes it. Note that `demo` uses only the configuration this user's
own setup recorded — it will not reach for the installed production one.

Before any of that, and needing nothing at all installed, they can run
`mithril-agent explain` and `mithril-agent walkthrough` on their own laptop.

## Review without trading

From the source directory:

```sh
umask 077
make verify-source   # every file matches the signed manifest
make test            # full suite, race detector, vet, format
make explain         # what it can and cannot do, in plain English
make walkthrough     # watch the real machinery run, on live prices
```

`make explain` and `make walkthrough` build what they need, so there is no
separate build step. All four need a Go toolchain and nothing else — no wallet,
no server, no account.

Neither of those needs a wallet, a host, or a configuration. If you are setting
one up, the guided path is two commands — answer or press Enter through the
first, and the second finds what the first wrote:

```sh
./bin/mithril-agent setup
./bin/mithril-agent doctor
```

`setup` authorises nothing. `doctor` is read-only and, for anything that is not
ready, prints the exact command that fixes it.

On an installed host, check the bounded operator view:

```sh
/usr/local/libexec/mithril-agent/mithril-agent status \
  --status-socket /run/mithril-agent-status.sock
```

If access is denied, ask an administrator to add the reviewer to the
`mithril-agent-status` group, then reconnect the login session. Do not loosen
the socket permissions or run the status command with broader privileges.

The status should report a recent runner, `no_new_actions`, no attention
required, and a Devnet profile. This command cannot read the wallet, private
configuration, RPC credentials, or raw journal. It receives only bounded
journal counters through the status snapshot.

For a no-trade review, run the live read-only gate as the service identity.
Systemd loads the protected environment without exposing it to the operator
shell. If you intend to start the demonstration next, skip this standalone
call: the demonstration repeats the same gate, and back-to-back calls can hit
a provider rate limit.

```sh
sudo systemd-run --quiet --wait --pipe --collect \
  --uid=mithril-agent --gid=mithril-agent \
  -p 'EnvironmentFile=/etc/mithril-agent/rpc.env' \
  -p 'EnvironmentFile=-/etc/mithril-agent/mcp.env' \
  -p 'EnvironmentFile=-/etc/mithril-agent/price.env' \
  /usr/local/libexec/mithril-agent/mithril-agent check \
  --config /var/lib/mithril-agent/agent/config.json
```

Continue only when it reports `status: ready`. Its `policy` object is the
reviewable, non-secret summary of direction, input and output assets, input in
raw base units, slippage, fee and reserve limits, direction-specific daily
caps, and schedule window.

## Run exactly one Devnet trade

The reviewer must have checked the policy values during setup or a prior
read-only gate. If a standalone gate was just run, wait for the provider's
documented rate window before starting the demonstration. Use a disposable
funded Devnet wallet. For an immediate
demonstration, use a profile without a price rule. The supplied unit gives a
price-triggered profile at most five minutes to reach its configured condition;
it is not a long-running market watcher.

In one terminal, follow the bounded unit's progress:

```sh
sudo journalctl --follow --unit=mithril-agent-demo.service
```

After the start command in the other terminal returns, press Ctrl-C to leave
the follow view.

In another terminal, start it and wait for completion:

```sh
sudo systemctl start --wait mithril-agent-demo.service
```

The command first repeats the read-only gate. It then permits one bounded
action, waits for final evidence, stops new actions, and prints a concise
result. A successful run ends with:

- `Devnet trade complete`;
- the input, output, and minimum output;
- a transaction signature and Devnet Explorer link;
- `Control: stopped`.

Any timeout or terminal failure also attempts to stop new actions. Do not
retry until the operator status has been checked and any attention state has
been resolved.

The command runs as `mithril-agent-demo.service`. Losing SSH or closing the
terminal does not prove that service stopped. Inspect it from a new session:

```sh
sudo systemctl status mithril-agent-demo.service --no-pager
```

To abort, stop the service:

```sh
sudo systemctl stop mithril-agent-demo.service
```

The unit's `ExecStopPost` attempts to revoke new actions even after a failure
or manual stop. Then read the public status and require control mode
`no_new_actions`; that status is authoritative. Do not rerun while an action is
pending or the status says attention is required.

## MCP and Telegram

Configure any MCP client to launch:

```text
command: /usr/local/libexec/mithril-agent/mithril-agent
args:    mcp --status-socket /run/mithril-agent-status.sock
```

The MCP process waiting for input is normal. It exposes only information,
status, and the operator guide. It cannot authorize, sign, or submit a trade.

Telegram is also read-only. The useful reviewer commands are:

```text
/help
/status
/price
/last_trade
```

An optional `/explain QUESTION` command may summarize the same bounded status
using the operator's own model account. Its output never participates in
policy, signing, submission, or confirmation.

## Verify the evidence

The live runner holds the journal lock, so journal verification must not be run
while that service is active. During a planned review window, stop the runner,
verify the sealed journal, then start the runner again. Its service starts with
new actions disabled:

```sh
sudo systemctl stop mithril-agent-swap.service
sudo -u mithril-agent \
  /usr/local/libexec/mithril-agent/mithril-agent journal verify \
  --path /var/lib/mithril-agent/agent/state/events.jsonl
sudo systemctl start mithril-agent-swap.service
```

The verification must pass. After restart, wait for the public status to become
recent and confirm that control remains `no_new_actions`.

## Handoff checklist

- The source revision and built binaries are identified.
- The node, quote, runner, status, Telegram, and monitoring services are
  healthy with no unexpected restarts before the demo and after any planned
  journal-verification restart.
- The live check reports ready at a fresh Devnet slot.
- The wallet and action limits are disposable and understood.
- One demonstration completes or fails closed; it is never repeated blindly.
- Final control mode is `no_new_actions`; any failed or halted result and its
  attention state remain visible. The journal verifies only in the planned
  window where its runner is intentionally stopped.
- No environment file, keypair, RPC URL, token, or raw provider response is
  copied into review notes.

For a fresh host installation, upgrades, recovery, alert routing, and the full
security boundary, follow `README.md`.

## Watching a real market

The Devnet demonstration proves the machinery is sound; it cannot tell you
whether the rule would make money. Shadow mode answers that separately: it
watches a live market — including Mainnet — and records the trade it would have
made, without being able to make one.

```sh
mithril-agent shadow run --policy PATH --dir PATH \
  --node-command PATH --quote-script PATH --pool ADDRESS --input-mint ADDRESS
```

It writes a hash-chained journal per UTC day and a report beside it. The report
scores every decision against a price observed *after* the decision was made,
charges the transaction fee on every fill, refuses any fill that would have
breached its own slippage floor, and compares the result against simply holding.
It also states how much of the period it could actually see, and leads with a
warning when that was poor.

You can recompute any day's result yourself from the record:

```sh
mithril-agent shadow report --policy PATH --dir PATH
```

It replays the hash-chained journal and tells you whether the stored report
matches it, field for field.

Shadow mode holds no key. The package it lives in declares no signer, no
submitter, and no field that could name a key, and two tests fail the build if
that ever stops being true.
