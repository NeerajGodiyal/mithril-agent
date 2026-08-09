# Mithril Agent Devnet demonstration

This guide is the short path for reviewing an already installed pilot. A new
host must first complete [QUICKSTART.md](QUICKSTART.md). `README.md` remains the
detailed operations and recovery reference.

## What the demonstration proves

The pilot can:

- read health and slot evidence from a Mithril Devnet node;
- validate one fixed Devnet swap route and its limits;
- permit at most one trade, sign it in a separate process, and submit it only
  through Mithril RPC;
- confirm the outcome using independent read providers;
- return to stopped mode and preserve a hash-chained audit record;
- expose bounded status through MCP, Telegram, and Prometheus.

It does not enable Mainnet trading, arbitrary tokens, or trade approval from
Telegram or an LLM. The complete strategy can repeat only within an explicit
time, action-count, schedule, and daily-spend grant. Shadow mode does *read*
Mainnet, but it holds no key and cannot sign; see "Watching a real market"
below.

## Handing this to someone non-technical

Split it in two: an operator prepares and validates the host once, then the
reviewer uses the bounded status and strategy view. A reviewer must never be
asked to find a private path, edit an environment file, or debug a service.

**One-time, by an administrator with root:**

```sh
# 1. Put the command on PATH, so nobody has to type /usr/local/libexec/...
sudo ln -sfn /usr/local/libexec/mithril-agent/mithril-agent /usr/local/bin/mithril-agent

# 2. Let the reviewer read the read-only status socket.
sudo usermod -aG mithril-agent-status REVIEWER
# The reviewer must log out and back in for the group to take effect.

# 3. Finish the supervised installation and guided strategy setup from QUICKSTART.
#    Run setup as the mithril-agent service identity with its protected
#    environment; do not run it from a normal login shell.
```

The operator must install the complete runtime: all seven Go binaries, the
pinned Node.js executable, `quote.mjs`, and its installed `node_modules`, plus
an accessible Mithril executable. Copying only the main `mithril-agent`
executable is incomplete and setup will refuse it. Keep one wallet, one saved
strategy and its destination proof together: the proof binds that exact agent
account to that exact payout wallet and cannot be borrowed from another setup
attempt.

Without step 1 the reviewer types an absolute path; without step 2 even
read-only status is refused. Neither weakens anything: the socket stays
read-only and the group grants nothing but reading it.

Before handoff, the operator must prove all of these:

```sh
sudo -u mithril-agent env HOME=/var/lib/mithril-agent \
  /usr/local/bin/mithril-agent strategy show  # sell and sweep are listed
systemctl is-active mithril-agent-run.service
```

Then run the supervised no-trade check shown below and require it to say that
everything is ready. Do not run a direct check from a login shell that lacks
the protected service environment.

After the first sell, the operator runs `mithril-agent setup strategy --resume`,
reinstalls the generated service, and repeats the same checks until `strategy
show` lists sell, buy and sweep. A fresh wallet therefore needs this one-time
bootstrap before it can be left unattended. Process exit alone is never proof
of a usable setup; the generated artifacts and read-only gate are the
acceptance evidence.

**Then the reviewer uses the fixed read-only surfaces:**

```sh
mithril-agent status --status-socket /run/mithril-agent-status-sell.sock
mithril-agent status --status-socket /run/mithril-agent-status-buy.sock
mithril-agent status --status-socket /run/mithril-agent-status-sweep.sock
```

Telegram `/status`, `/price`, and `/last_trade` provide the combined phone
view. The separate `demo` command is for a legacy single-leg setup; it does not
discover a strategy pointer and must not be presented as the full-strategy
review path.

Before any of that, and needing nothing at all installed, they can run
`mithril-agent explain` and `mithril-agent walkthrough` on their own laptop.

## Review without trading

From the source directory:

```sh
umask 077
make verify-source   # every source file matches the recorded manifest
make test            # full suite, race detector, vet, format
make explain         # what it can and cannot do, in plain English
make walkthrough     # watch the real machinery run, on live prices
```

`make explain` and `make walkthrough` build what they need, so there is no
separate build step. All four need a Go toolchain and nothing else — no wallet,
no server, no account.

Neither command needs a wallet, a host, or a configuration. Creating a strategy
does need the prepared Linux host and must follow the supervised installation
in `README.md`; do not improvise a laptop setup and copy its files to the host.

On an installed full-strategy host, check one bounded leg through its generated
socket. Use `buy` or `sweep` in place of `sell` for the other legs:

```sh
/usr/local/libexec/mithril-agent/mithril-agent status \
  --status-socket /run/mithril-agent-status-sell.sock
```

If access is denied, ask an administrator to add the reviewer to the
`mithril-agent-status` group, then reconnect the login session. Do not loosen
the socket permissions or run the status command with broader privileges.

The status should report a recent runner, `no_new_actions`, no attention
required, and a Devnet profile. This command cannot read the wallet, private
configuration, RPC credentials, or raw journal. It receives only bounded
journal counters through the status snapshot.

For a no-trade review, run the path-free readiness command as the service
identity. Systemd loads the protected environment without exposing it to the
operator shell:

```sh
sudo systemd-run --quiet --wait --pipe --collect \
  --uid=mithril-agent --gid=mithril-agent \
  -p 'EnvironmentFile=/etc/mithril-agent/rpc.env' \
  -p 'EnvironmentFile=/etc/mithril-agent/quote.env' \
  -p 'EnvironmentFile=-/etc/mithril-agent/mcp.env' \
  -p 'EnvironmentFile=-/etc/mithril-agent/price.env' \
  /usr/local/libexec/mithril-agent/mithril-agent start
```

Continue only when it reports that everything is ready. `strategy show` is the
reviewable, non-secret summary of both trade directions, amounts, triggers,
daily caps, sweep bounds, and current grants.

## Run exactly one Devnet trade

This section is **legacy single-leg only**. It requires an explicit single-leg
setup at `/var/lib/mithril-agent/agent` and the supplied
`mithril-agent-demo.service`. Skip it when reviewing the generated full
strategy; use the next section instead.

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

## Run the complete bounded strategy

This is the full reviewer path after the operator has completed the one-time
bootstrap and `strategy show` lists sell, buy, and sweep. The supervised runner
must already be active; the reviewer does not start a second copy.

```sh
sudo -u mithril-agent env HOME=/var/lib/mithril-agent \
  /usr/local/bin/mithril-agent strategy show
```

The operator runs the protected, no-trade `start` command from the earlier
section. When every check is ready, it prints bounded `strategy enable`
arguments, including `--allow-any-price` when the operator intentionally
configured market-price legs. Review the duration and maximum trades, replace
`TEXT` with the review reason, and run those arguments once through the
`sudo -u mithril-agent env HOME=/var/lib/mithril-agent` wrapper in QUICKSTART.

The runner then watches the configured rules without an open terminal. A sell,
buy, and sweep happen only when their own trigger, funding, schedule, evidence,
and spending gates pass. Check all three from either surface:

```sh
sudo -u mithril-agent env HOME=/var/lib/mithril-agent \
  /usr/local/bin/mithril-agent strategy show
# In Telegram: /status, /price, /last_trade
```

For an immediate demonstration the operator must have prepared market-price
legs; a price-triggered strategy can correctly remain waiting. Stop the whole
strategy at any time with:

```sh
sudo -u mithril-agent env HOME=/var/lib/mithril-agent \
  /usr/local/bin/mithril-agent strategy stop --reason 'review finished'
```

Afterward, `strategy show` must report every leg stopped. Do not infer success
or failure from SSH, a terminal, or a missing Telegram message.

## MCP and Telegram

Configure any MCP client to launch:

```text
command: /usr/local/libexec/mithril-agent/mithril-agent
args:    mcp --status-socket /run/mithril-agent-status-sell.sock
```

The MCP process waiting for input is normal. A generated strategy has separate
`sell`, `buy`, and `sweep` sockets; configure one MCP entry per leg. This
version has no combined multi-leg MCP socket. Each entry exposes information,
status, the configured leg, and the operator guide. It cannot authorize, sign,
or submit a trade.

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

The full-strategy runner holds all three journal locks, so journal verification
must not run while that service is active. During a planned review window, stop
the generated runner, verify every journal, then start the runner again. Its
service starts with new actions disabled:

```sh
sudo systemctl stop mithril-agent-run.service
for leg in sell buy sweep; do
  sudo -u mithril-agent \
    /usr/local/libexec/mithril-agent/mithril-agent journal verify \
    --path "/var/lib/mithril-agent/.mithril-agent/strategy/$leg/state/events.jsonl" \
    || exit 1
done
sudo systemctl start mithril-agent-run.service
```

The verification must pass. If setup used a custom strategy directory, use its
three `state/events.jsonl` paths instead. After restart, wait for all public
statuses to become recent and confirm that every leg remains stopped.

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
