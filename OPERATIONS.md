# Mithril Agent operations and reference

> **New to the project?** Read [README.md](README.md) first. It explains the
> system, supported path, current limits, and which guide matches your task.
>
> **Start here:** [QUICKSTART.md](QUICKSTART.md) is the single supported
> first-run path for the complete Devnet strategy. This file is the detailed
> security, recovery, monitoring, and legacy single-leg reference. Do not mix
> the quick-start's generated strategy services with the legacy units below.

## Quick source check — you already run Mithril

This is a component that sits beside your Mithril node. If you have a node
running, first verify the checkout without installing or authorising anything:

```sh
make prereqs-trading # walletless plus the optional trading runtime
make build       # nine binaries into ./bin
make adapter     # the Orca quote adapter (checks your Node version first)
make test        # format, vet, tests, race, isolation and private-file checks
make walkthrough # read-only prices plus a local audit-integrity walkthrough
```

Keep all nine files produced by `make build` together. `mithril-agent` starts
the policy, signer, submitter, quote, status and Telegram helpers from beside
its own executable; copying only the main binary creates an incomplete runtime
that setup correctly refuses.

For a live unattended agent, complete [Fresh supervised Linux installation](#fresh-supervised-linux-installation)
before the strategy flow below. That installs the restricted OS identities,
protected quote sidecar, environment files and runtime paths the generated
runner expects. Skipping it is refused rather than producing a half-working
service.

## One strategy: bootstrap once, then run unattended

These commands run after the supervised host installation above. The quote
socket keeps Node.js out of the process that can sign and submit.
`service install` also supplies that socket explicitly. It additionally creates
separate socket-activated risk-authority and signer boundaries per leg. A
legacy setup whose signer ledger is not already below its stable `state/signer`
directory must be recreated; the installer will not silently move a daily-cap
ledger or weaken its permissions.
The short commands show the workflow and assume the `mithril-agent` service
identity with `HOME=/var/lib/mithril-agent`. On a live host, use the supervised
command shapes in the installation section or the complete QUICKSTART. Do not
source the protected environment files into a login shell.

A strategy is a sell leg, a buy leg, and a sweep on one wallet. You enter the
settings once. On a fresh wallet, the first setup writes the sell and sweep;
after that sell creates the devUSDC account, one resume command writes the buy.

This is the recommended path to a full unattended cycle (sell, optional buy
back, then sweep). A brand-new wallet cannot complete that cycle in one launch:
its first sell creates the devUSDC account that the buy leg must pin. Treat that
first sell as a one-time bootstrap, run `--resume`, and only then leave the
complete strategy unattended.

```bash
mithril-agent setup strategy \
  --quote-socket /run/mithril-agent-quote/quote.sock
                                  # guided; use the supervised service environment
mithril-agent service install --output "$HOME/.mithril-agent/mithril-agent-run.service"
                                    # once per host; review generated unit + install commands
mithril-agent strategy enable --duration 12h --max-trades 1 --reason 'create buy funds'
```

Bootstrap phase one is complete only when `mithril-agent strategy show` lists
the sell and sweep legs and the read-only check passes. A zero process exit by
itself is not acceptance evidence. Do not combine a strategy file, destination
proof or wallet from different setup attempts: the proof deliberately binds one
exact agent account to one exact payout address.

After the first sell completes, finish the same saved setup and reinstall the
generated service so it includes the buy leg. Run every install and restart
command printed by `service install`; `daemon-reload` alone does not update an
already-running runner, authority socket, recovery timer, status bridge, or
Telegram process:

```bash
mithril-agent setup strategy --resume
mithril-agent service install --output "$HOME/.mithril-agent/mithril-agent-run.service"
mithril-agent strategy enable --duration 12h --max-trades 1 --reason 'one round trip'
```

The unattended setup is complete only when `strategy show` lists sell, buy and
sweep, both swap legs pass the read-only gate, and the generated runner exposes
sell, buy and sweep on `BASE`, `BASE+1` and `BASE+2`. Those offsets never change;
an absent buy leg leaves `BASE+1` unused rather than moving the sweep.

`setup strategy` with no strategy options asks plain questions, saves the
answers to `~/.mithril-agent/strategy.json`, shows the exact trade it will
configure, and walks you through proving you own the wallet that profit goes
to. A prior setup supplies its recorded host paths; a fresh host supplies the
wallet and Mithril paths in the supervised command below. Change your mind
later with `strategy edit PATH` (the same questions, pre-filled) or `strategy
edit PATH --raw` to open the file in `$EDITOR`.

For a non-technical setup, run once and never touch per-leg flags again:

```bash
mithril-agent setup strategy \
  --sell-at-usd 75.00 \
  --buy-at-usd 73.00 \
  --trades-per-day 4 \
  --keep-sol 0.25 \
  --to YOUR_WALLET_ADDRESS \
  --quote-socket /run/mithril-agent-quote/quote.sock
```

Then use the bootstrap enable, resume, reinstall, and final enable commands
above. The saved buy settings carry through; you do not enter the per-leg flags
again.

After the resume step, one enable covers all three bounded legs:
- Sell when the sell trigger hits.
- Buy when the buy trigger hits (after the sell creates devUSDC).
- Sweep when the balance floor is above the keep amount at the configured time.

`--max-trades` is per leg. Use `1` for one sell plus one buy. Raise it only if
you want each leg to trade more than once during the same grant.

### Full AFK cycle check

After resume + enable, verify your strategy state before you go offline:

```bash
mithril-agent strategy show
```

You should see both `sell` and `buy` legs armed for a full round trip. If only
sell is armed, you will only get a sell alert and no buy event.

Prices are optional. Set BOTH a sell and a buy price to trade only at prices you
choose, or NEITHER to trade at whatever the pool gives — arming market-price
legs then requires `strategy enable --allow-any-price`, which prints
`NO PRICE CONDITION` per leg so it is never silent.

**The price you set is a floor on the fill, not just a signal.** The agent
refuses to sell below it however high the oracle reads, so a threshold the pool
cannot reach is refused at setup rather than waiting forever.

### How much it may spend, and what it keeps

Two numbers bound an unattended strategy, and both live in the same file.

`trades_per_day` (default 6) sizes the signer's daily caps. It is a **spending
bound, not a target**: the agent trades when the rules say so and never more
often than this allows. It matters more than it looks, because the caps and the
control grant are independent bounds — `strategy enable --max-trades N` is now
refused when `N` exceeds what the caps actually fund, rather than granting
authority the signer will spend the rest of the day declining.

`keep_sol` is what stays in the agent wallet; everything above it is swept to
`sweep.to`. Leave it empty and the agent keeps exactly what the trades need. It
can only ever **raise** that floor — asking to keep less than the legs require
is refused at setup, because a sweep that drains the wallet under the trader
turns every later trade into an insufficient-balance failure a long way from
its cause.

```bash
mithril-agent setup strategy --trades-per-day 6 --keep-sol 0.25
```

See what a configured strategy actually funds:

```bash
mithril-agent strategy show     # ... funds 6 trade(s) per day; cap resets 00:00 UTC
```

The buy leg is written after the first sell, because it pins the devUSDC account
that sell creates:

```bash
mithril-agent setup strategy --resume
```

The sweep floor already reserves the buy leg, so resuming costs no second
destination proof.

### Verify a browser wallet from a remote host

Setup never asks for your wallet key. When it needs proof that you control the
payout address, it waits on the server and prints one command for your Mac or
Linux desktop:

```sh
mithril-agent wallet verify --session SHORT_LIVED_SESSION
```

Run that command where Phantom or Solflare is installed. It asks for your
normal SSH host or alias, opens a temporary `http://127.0.0.1/...` page in that
browser, and closes the SSH tunnel after the server verifies the signature.
It never prints a server-local file link and never receives the wallet key.
The Solana CLI signature path remains available when no browser wallet is used.

Stop everything at once, at any time:

```bash
mithril-agent strategy stop --reason TEXT
```

`strategy show` prints every leg, its trigger, its grant and where the profit
goes. Nothing above arms anything except `strategy enable`, which is bounded:
at most 24 hours, 1..100 trades, one action per schedule window.

`setup` asks for the price to trade at. Leave it blank for an unconditional
demonstration, or give a number — a sell fires at or above it, a buy at or
below it. `setup sweep` configures sending profit to your own wallet, and
`strategy show` prints everything currently configured.

`setup` looks for your node's `config.toml` where you would actually have it —
the working directory, where `mithril config init` writes it, then `~/.mithril`
— and finds the quote adapter in this checkout. It records where it put things,
which is why `doctor`, `start`, and `strategy show` need no paths. `demo` is a
single-leg command and needs an explicit `--config` after strategy-only setup.

**What you need:** Go 1.26.6+, Node 24.18+ and npm 11.16+ in their respective
24.x and 11.x lines (for live quotes only), and a Mithril node you can point
at. `make prereqs-trading` tells you which of
those you are missing rather than letting you find out one failure at a time.

To configure a trade you need **four RPC URLs** in the protected environment:

- `MITHRIL_AGENT_MITHRIL_RPC_URL` — your own Mithril node. Plain http is
  accepted here when the host is loopback, because it is your machine.
- `MITHRIL_AGENT_PRIMARY_RPC_URL` and `MITHRIL_AGENT_SECONDARY_RPC_URL` — two
  https endpoints from **different providers**, so no single provider is the
  only witness to what happened.
- `MITHRIL_AGENT_QUOTE_RPC_URL` — the HTTPS read endpoint used only by the
  isolated quote sidecar. It is not a third source of confirmation evidence.

Setup reads them from the environment and never copies them into the generated
strategy or trading profile. A supervised installation keeps them separately
in the root-owned, mode-`0600` `/etc/mithril-agent/rpc.env` file described
below.

Your node also has to have **replayed far enough to see the agent's account**.
It reports what it has actually verified, so a node still catching up will
refuse with "source account was not found by Mithril" — which is the gate
working, not a fault. Compare `mithril_replay_slot` against the cluster head.

And you need Devnet SOL in the agent account. Start with the account-free
helper, which sends only the public address and tops the account up to 1 test
SOL through Solana's official public Devnet RPC:

```sh
mithril-agent wallet fund --file /absolute/path/to/devnet-keypair.json
```

The public RPC airdrop is rate-limited and may return 429. If it does, use
`https://faucet.solana.com` with the address shown by `wallet check`. Devnet SOL
has no value.

**Before installing anything**, these two run on their own and need no
configured wallet, server, or provider account:

```sh
make explain       # what it can and cannot do, in plain English
make walkthrough   # watch the real machinery run, on live prices
```

For a short reviewer walkthrough of the existing Devnet pilot, see
[DEMO.md](DEMO.md). This file remains the complete setup and operations
reference.

Mithril Agent is a self-hosted application layer beside a Mithril full node.
The current pilot can run bounded sell, buy, and sweep legs on Solana Devnet;
its separate demo command still permits only one swap.
It is intentionally separate from the public Mithril node repository: node
verification and RPC stay in Mithril, while wallet policy, signing, alerts, and
application releases stay here.

The installed implementation is not a general trading strategy and cannot
execute on Mainnet. Shadow mode does read Mainnet — it watches a live market and
records what the rule would have done — but it holds no wallet signing key and
has no code path to a signature. The supplied Telegram commands and MCP tools
expose read-only status and no authority to approve, construct, sign, or submit
a transaction.

### Plan-only DCA and agent research

`strategy dca-plan` accepts either an exact SOL cap or a USD notional plus an
operator-confirmed SOL/USD reference. It calculates fixed SOL inputs, reports
any lamport remainder, loads no wallet, and returns
`planned_not_authorized`. Multi-day plans require a fresh bounded grant each
day; fees and rent are outside the named swap-input cap.

The planner uses the self-managed bounded runner. Jupiter's current Trigger DCA
path instead requires an API key, wallet authentication, a signed upfront
deposit, and a provider-managed vault, so it remains a separate unqualified
Mainnet custody boundary. An agent may propose parameters and use `shadow
backtest`, `shadow search`, `shadow run`, and `shadow review`, but it cannot turn
a result into authority. `shadow search` chooses bounded parameters on one
hash-chained day—fixed thresholds or adaptive windows and a signal hurdle—and
reports the exact result on a later untouched day; pool fills remain
explicitly modelled. Unlimited “trade the wallet” behavior remains unsupported: future
execution still needs a dedicated limited-balance account, venue and asset
allowlists, hard action, daily, total and loss caps, an expiring operator grant,
independent evidence, and the separate signer and submitter.

Internal packages cover a narrow future Mainnet boundary: exact Jupiter v0
candidate validation, one bounded authorization, transaction-only custody,
separate attestation, sealed submitter validation, and durable recovery with
two-provider effect reconciliation. A disabled Turnkey adapter covers the
transaction-signing API contract and can be selected explicitly by the bounded
signer CLI/socket, but no generated service selects it and there is no Mainnet
submit command that invokes those pieces.
Do not grant an assistant shell or service-control access to the deployment.

The current demonstration is designed to exercise the autonomous mechanics
for one fixed SOL-to-devUSDC sell or devUSDC-to-SOL buy after an explicit
bounded grant.
It is not a Mainnet flow or a Telegram-controlled bot.

## Current flow

```text
Mithril MCP: node identity, health, wallet balance
                    |
          two healthy observations
                    |
 Pyth + Kraken -> optional one-shot SOL/USD trigger
                    |
external quote -> independent instruction and deployment validator
                    |
 two providers agree on fee and optional account rent
                    |
        exact Mithril RPC simulation
                    |
 risk policy process -> signer process -> sealed transaction
                    |
           durable send boundary
                    |
         Mithril RPC submission only
                    |
 two independent providers confirm status + exact effects
                    |
      status / MCP / Prometheus / Telegram alert
```

External providers are temporary, replaceable read interfaces:

- the Orca adapter reads pool state and builds a quote;
- two providers must agree on the transaction fee, current Orca deployment,
  and any route account rent;
- the same two providers independently confirm finality and effects.

Mithril is the only simulation and submission path. There is no external send
fallback. When Mithril later exposes the remaining standard read RPCs, their
implementations can replace the quote and fee readers without changing the
engine. Independent post-send evidence should remain external so the node is
not asked to verify itself.

### Optional price rule

The Devnet pilot can monitor SOL/USD and start the sells or buys its grant allows only
after an operator-set target is reached. It requires fresh, agreeing observations
from Pyth and Kraken, applies each source's uncertainty conservatively, and
uses integer arithmetic. An ordinary price miss is a waiting state, creates no
journal growth, and consumes no action allowance.

Legacy Coinbase-bound price policies are refused for active runs rather than
silently mapped to Kraken. Historical report, review, and backtest commands can
still decode their journals for audit. Regenerate an active policy so its new
source identity, journal, and evidence lineage are explicit. The existing
Coinbase adapter remains available for API compatibility, but new paper policies
do not select it. Coinbase's
[Market Data Terms](https://www.coinbase.com/legal/market_data), updated August
7, 2026, restrict using its market data for an automated system in section 3.5
without written consent; deployments must evaluate and comply with those terms
independently.

The market rule is checked before signing and again immediately before send.
The sell quote's minimum devUSDC-per-SOL rate must meet its floor; for a buy,
the maximum executable price derived from minimum SOL output must stay at or
below the ceiling. A market observation therefore cannot authorize a worse
pool rate. This is a Devnet test proxy, not proof that devUSDC is worth one US
dollar; a production stablecoin route also needs independent stablecoin/USD
evidence. Only the evidence used for an attempted action is durable. The rule
never rearms itself; once a grant is spent another requires a new explicit one.

The runner polls on its configured one-to-thirty-second interval and defaults
to ten seconds. It is a bounded conditional action, not an exchange-native
tick trigger or high-frequency strategy; network, source, quote, and block
latency still apply.

Telegram and optional LLM explanations distinguish a waiting rule, a reached
market target, and a quote that is actually ready to execute. They may report
source freshness and the last action, but they do not provide
signing authority or enter the decision calculation. The supplied public price
adapters are appropriate for a bounded Devnet demonstration, not low-latency
production trading. Mainnet, arbitrary pairs, production market-data SLAs,
recurring strategies, and Telegram write controls require additional policy
and route work.

The DEFAULT price source is the on-chain Pyth push feed, read through your own
node. It needs no credential, so an ordinary price rule costs nothing extra.

Only the optional Hermes adapter requires `MITHRIL_AGENT_PYTH_API_KEY`. Pyth's
[migration guide](https://docs.pyth.network/price-feeds/core/upgrade/preparing)
says the Pyth Core upgrade completed on August 26, 2026 and every Hermes user
now needs an API key; an integration that only reads on-chain push feeds does
not. The default adapter pins and probes both Pyth SOL/USD account generations
through your Mithril node, verifies each available account's exact owner, and
requires agreement while both are live. The completed upgrade required no
operator-side address switch.

`NewPyth` already refuses an empty key, so a Hermes-configured agent fails
closed rather than trading on a feed it cannot read. If you select that source,
keep the bearer credential in the protected service environment, never in the
config, journal, metrics, MCP output, or command line.

## Implemented safety boundary

- Devnet and both directions of one fixed SOL/devUSDC Orca pool only.
- The profile pins the Orca program-data account, deployment slot, and upgrade
  authority. Discovery validates quote instructions against those pinned values
  but does not read live deployment state. `swap check` and the runner verify
  the pinned deployment through both evidence providers; a mismatch blocks
  signing and send.
- Exact input amount, maximum fee, daily debit cap, reserve, slippage, schedule,
  and reconciliation timeout are bound into the profile fingerprint.
- Two fresh healthy Mithril observations must advance in time and slot before a
  new action starts.
- The pinned Orca SDK runs out of process. Go code independently validates every
  program, account role, signer, amount, threshold, and cleanup instruction.
- A read-only live contract test checks the pinned SDK output against public
  Devnet and the independent validator.
- The risk authority, signer, and submitter are separate processes with
  separate policies. In a newly generated supervised setup all three are
  short-lived, socket-activated services under separate OS identities; systemd
  passes each service its policy and key as private credentials. The runner
  cannot open those keys, the signer's durable ledger, or the submitter's
  writable control directory. Manual foreground Devnet runs retain the older
  child-process compatibility path. The Devnet runner receives the exact
  signature as response metadata and already has the unsigned message, so it
  could reconstruct that one signer-approved transaction. The Devnet split is
  operational isolation, not an adversarial boundary against a compromised
  runner.
- The signer re-decodes the exact message, applies its own daily cap, and
  encrypts the signed transaction for the submitter. The disconnected Mainnet
  response also withholds the signature inside that ciphertext, so the runner
  cannot reconstruct the signed transaction; the current Devnet response does
  not provide that stronger property. The response
  attestation also binds the complete immutable signing-request hash, and both
  fresh and recovered flows recompute that hash before accepting the response.
- A hash-chained journal records every boundary. Capacity for terminal records
  is reserved before execution starts.
- Stop state is checked under a send barrier. Before contacting the node, the
  submitter persists the exact signed transaction, signer attestation, action,
  fee, and expiry beside the protected control state. A keyless systemd timer,
  running as the submitter identity but unable to read its key or another
  submitter process through `/proc`, asks the two
  pinned evidence providers for finality and exact effects. Only that timer may
  resolve a matching finalized recovery marker. Matching success clears it;
  matching finalized failure becomes a stopped `failed` action that requires
  explicit operator acknowledgement. Pending, divergent, or malformed evidence
  keeps recovery pending and every new action blocked. The runner cannot call
  the timer or name an action for it to resolve.
- Finality, transaction bytes, native balances, token balances, fee, and output
  amount must agree across both evidence providers.
- Missing, stale, divergent, or malformed evidence fails closed.

For a sell, the supported SDK shape idempotently creates the wallet's canonical
WSOL account, funds it, and closes it after the swap. It may also idempotently
create the canonical devUSDC output account. For a buy, the canonical devUSDC
input account must already exist; the transaction creates one deterministic
temporary WSOL account and closes it back to the wallet. The agent verifies the
full 165-byte initialized token-account shape, mint, owner, balance, and account
size through both evidence providers. Any other instruction sequence is
refused. Rent and pre-transaction balances are included in exact effect
reconciliation and the applicable debit cap.

The last deployment and rent checks happen immediately before send, but they
cannot make an upgradeable external program or changing rent sysvar atomic with
the transaction. This is an accepted Devnet-pilot limitation. A future
real-capital design must pre-create the output account and remove the upgrade
race with an immutable deployment or an on-chain guard.

## Build

Go 1.26.6+, Node.js 24.18+ and npm 11.16+ in their respective 24.x LTS and
11.x lines, and Linux are required for live execution. The minimum Go patch
release includes security fixes used by the
agent's network paths. macOS can build and run most tests, but the execution
gate requires Linux kernel clock evidence.

Build the deployment bundle on the target Linux host. If you deliberately
cross-compile, set the target explicitly and verify every output before it is
transported; a macOS executable copied to Linux is not a candidate:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 make build
file ./bin/*                         # every file must be Linux x86-64/ELF
```

Record the exact source before building. Keep the manifest outside the source
tree and preserve its hash with the validation evidence; the development
version string is a release label, not source provenance.

```sh
mkdir -p /absolute/private/validation
find . -type f ! -path './.git/*' ! -path './bin/*' \
  ! -path './adapters/orca/node_modules/*' -print0 \
  | sort -z | xargs -0 sha256sum \
  > /absolute/private/validation/source.sha256
sha256sum /absolute/private/validation/source.sha256
```

```sh
umask 077
mkdir -p ./bin

go build -o ./bin/mithril-agent ./cmd/mithril-agent
go build -o ./bin/mithril-agent-policy ./cmd/mithril-agent-policy
go build -o ./bin/mithril-agent-signer ./cmd/mithril-agent-signer
go build -o ./bin/mithril-agent-submitter ./cmd/mithril-agent-submitter
go build -o ./bin/mithril-agent-quote ./cmd/mithril-agent-quote
go build -o ./bin/mithril-agent-telegram ./cmd/mithril-agent-telegram
go build -o ./bin/mithril-agent-status-bridge ./cmd/mithril-agent-status-bridge
go build -o ./bin/mithril-agent-paper-status-bridge ./cmd/mithril-agent-paper-status-bridge
go build -o ./bin/mithril-agent-paper-dashboard ./cmd/mithril-agent-paper-dashboard

npm --prefix ./adapters/orca ci --ignore-scripts
sha256sum ./bin/* > /absolute/private/validation/binaries.sha256
```

After assembling the complete candidate runtime (the nine binaries, pinned
Node.js, `quote.mjs`, and `node_modules`), create one manifest for every file
and symlink target. The Mithril executable is a separate prerequisite and must
also be installed at the path passed to setup. Keep the manifest outside the
runtime tree, verify it before transport, and run the same check from the
transported staging directory:

```sh
cd /absolute/runtime-staging
find . \( -type f -o -type l \) -print0 \
  | sort -z | xargs -0 sha256sum \
  > /absolute/private/validation/runtime.sha256
find . -type l -printf '%P\t%l\n' | LC_ALL=C sort \
  > /absolute/private/validation/runtime-symlinks.txt
sha256sum -c /absolute/private/validation/runtime.sha256
```

Regenerate and compare `runtime-symlinks.txt` after transport as well; hashing
only each symlink's current target content does not prove that the link itself
was preserved.

Staging is not an executable runtime. Do not run setup from `/tmp`, `/mnt`, a
home directory, or any tree owned by the login user. After verification,
install the complete bundle into the root-owned `/usr/local/libexec` tree shown
below; executable validation intentionally refuses replaceable directory
ancestry.

Hash the supplied sysusers, systemd, timesync, and Prometheus files into a
separate deployment-assets manifest. Verify that manifest before installation,
then compare every installed file byte-for-byte with its staged source. A
candidate is not ready for cutover if any manifest check or comparison fails.

```sh
sha256sum \
  deploy/sysusers/mithril-agent-status.conf \
  deploy/sysusers/mithril-agent-dashboard.conf \
  deploy/systemd/mithril-agent-paper-dashboard.service \
  deploy/systemd/mithril-agent-paper-dashboard.socket \
  deploy/systemd/mithril-agent-quote.service \
  deploy/timesyncd/90-mithril-agent.conf \
  deploy/prometheus/mithril-agent.rules.yml \
  > /absolute/private/validation/deployment-assets.sha256
sha256sum -c /absolute/private/validation/deployment-assets.sha256
```

The Orca dependencies are pinned. Run the adapter with `--conditions=node`, as
the agent does; Node's default conditional export can select a different SDK
build.

### Upgrade safety

Sealed transaction v2 binds the blockhash context slot into the encrypted
envelope. Signer responses now also authenticate the intended submitter and all
sealed metadata, and risk grant v2 authenticates the complete signing request.
These changes are intentionally incompatible with earlier pilot state. The
risk authority, signer, and supervised submitter are short-lived
socket-activated service instances. Manual foreground mode retains a transient
submitter child. Never roll binaries while an agent service, authority instance,
or transient child is running, and never open old pilot state with new binaries.

Use this upgrade sequence:

1. Stage the complete candidate runtime as the root-owned, same-filesystem
   sibling `/usr/local/libexec/mithril-agent.next`. Validate its contents and
   leave the fixed runtime path unchanged.
2. With the old binary and config, stop new actions and drain the runner. Accept
   only a fresh post-request status whose control mode is `no_new_actions` and
   whose action is stopped, independently finalized, canceled before send, or
   an independently confirmed `failed` action whose exact journal record was
   acknowledged. An unreviewed failure or any halted action is not safe for an
   ordinary upgrade.

   If the first drain returns attention for `failed`, stop the old runner,
   independently confirm the failure, acknowledge that exact action with the
   old binary, restart the old runner in stopped mode, wait for a fresh status,
   and drain again. Do not use this path for `halted`.
3. Stop the runner, risk-authority sockets, signer sockets, quote, Telegram,
   status-socket, bridge, and paper-dashboard service and socket units. Verify
   each unit has `MainPID=0` and that no process remains in any of their control
   groups. The short-lived authority instances and submitter child must also be
   absent.

   Stop paper activators before paper processes so they cannot recreate an old
   observer during the cutover. This includes
   `mithril-hermes-research.timer`, `mithril-agent-paper-auto-select.timer`,
   `mithril-agent-paper-bootstrap.timer`,
   `mithril-agent-paper-champion.path`, and
   `mithril-agent-paper-challenger.path`. Then stop every installed paper
   observer (`paper-base`, `paper-jup`, `paper-champion`, and
   `paper-challenger`), its status bridge and socket, Hermes research, Telegram,
   and the dashboard service and socket. Record which conditional paper units
   were active; do not substitute a generic runner name or leave an old paper
   process publishing the prior status schema.
4. Preserve a private rollback bundle containing the old runtime, units,
   configs, environment files, policies, keys, journal, control state, signer
   ledger, Telegram cursor, and recorded ownership and mode metadata. Keep it
   separate from the sanitized audit export. Copy each complete `state/signer`
   directory, not only its active `authorizations.jsonl` file: its lock and
   numbered sealed segments are part of the ledger history, and omitting one
   makes the restored signer refuse to start rather than resetting a cap.
5. Archive the complete old setup directory. While every service remains
   stopped, perform a same-filesystem two-rename cutover: rename
   `/usr/local/libexec/mithril-agent` to a required-nonexistent rollback name,
   then rename `mithril-agent.next` to `mithril-agent`. Each rename is atomic,
   but the pair is not. If the second rename fails, restore the first name and
   stop; do not start a partially switched runtime. Validate the new fixed tree
   before continuing.
6. Install the matching candidate units, sysusers definition, timesync policy,
   and Prometheus rule while services remain stopped. Run `systemd-analyze
   verify`, `promtool check rules`, and `promtool check config`, then run
   `systemd-sysusers`, `systemctl daemon-reload`, and restart
   `systemd-timesyncd`. If the private paper dashboard was enabled before the
   cutover, restore the previously active paper status sockets and paper
   observers first, then their bridges, Telegram, paths, and timers. Restart
   the dashboard socket last and require `curl --fail --unix-socket
   /run/mithril-agent-paper-dashboard.sock http://localhost/healthz` to succeed
   before accepting it. Dashboard health is successful only after both
   configured market sockets publish fresh current-day status from the
   candidate binary; an `Updating`, stale, or mixed-version market is a failed
   upgrade. Require `mithril-agent clock-check` to pass, then
   activate Prometheus and Alertmanager through the pinned monitoring
   deployment's documented reload or restart procedure. Keep the old matching
   assets in the rollback bundle and restore them together with the old
   runtime. Do not rerun the fresh-install binary-copy block below over the
   switched runtime.
7. Create a new required-empty setup directory with fresh journal, control,
   signer ledger, and transaction-envelope state. Do not copy state across
   versions. Setup, preflight, and read-only checks may run immediately. Do not enable the
   new setup until the first UTC midnight after the old runtime's last possible
   signature. This gate applies even when the old ledger appears empty.

Rollback is asymmetric. Before the new setup signs anything, the stopped old
runtime and its exact state may be restored. A signed but unsent action must
first reach a durable pre-send cancellation under the new runtime. After the
new setup signs during a UTC day, the restored old ledger must remain disabled
until the next UTC day. After send-start or submission, keep reconciliation on
the new setup; do not switch transaction ownership between versions. A
rollback must restore the matching runtime, environment, state, ownership, and
file modes as one bundle.

## Configuration

RPC URLs belong only in the process environment:

```sh
export MITHRIL_AGENT_MITHRIL_RPC_URL='http://127.0.0.1:8899'
export MITHRIL_AGENT_QUOTE_RPC_URL='https://quote-provider.example/...'
export MITHRIL_AGENT_PRIMARY_RPC_URL='https://evidence-a.example/...'
export MITHRIL_AGENT_SECONDARY_RPC_URL='https://evidence-b.example/...'
export MITHRIL_AGENT_PYTH_API_KEY='required-only-for-the-optional-hermes-source'
```

The Mithril endpoint must be a literal loopback address. The two evidence URLs
must use HTTPS and have distinct canonical hosts. Credentials never belong in
the JSON config, journal, command line, or MCP output. Two URLs, hostnames, or
API keys from one provider are still one trust domain; the evidence providers
must be operated independently.

First derive the wallet address locally and discover the current public route.
Use the same direction, amount, and slippage for discovery and setup. For a
sell:

```sh
./bin/mithril-agent swap discover \
  --wallet-keypair /absolute/private/devnet-keypair.json \
  --node-command /absolute/path/to/node \
  --quote-script "$PWD/adapters/orca/quote.mjs" \
  --input-lamports 1000000 \
  --slippage-bps 100
```

The command reads the keypair only to derive its public address. It performs a
read-only Devnet quote, independently validates the complete instruction list,
and prints a `route` object. Record its `min_output_amount`, then create the
private pilot setup:

```sh
./bin/mithril-agent swap setup \
  --dir /absolute/private/mithril-agent-swap \
  --wallet-keypair /absolute/private/devnet-keypair.json \
  --mithril-command /absolute/path/to/mithril \
  --node-command /absolute/path/to/node \
  --quote-script "$PWD/adapters/orca/quote.mjs" \
  --quote-socket /run/mithril-agent-quote/quote.sock \
  --input-lamports 1000000 \
  --slippage-bps 100 \
  --confirm-min-output-amount CURRENT_DISCOVERED_MINIMUM \
  --primary-trust-domain REPLACE_WITH_PRIMARY_PROVIDER_COMPANY \
  --secondary-trust-domain REPLACE_WITH_SECONDARY_PROVIDER_COMPANY \
  --sell-at-usd 75.00
```

By default the MCP child uses the restricted `MITHRIL_*` environment inherited
from the runner, so the operations service does not need access to the full node
configuration. Pass `--mithril-config /absolute/private/mithril-config.toml`
only when those local MCP paths and endpoints are not supplied by the service
environment.

`--quote-socket` selects the supervised layout. Setup still performs its
confirmation quote directly. The runner may start while the quote service is
unavailable so it can preserve status and reconcile a prior submission, but it
will report `quote_unavailable` and cannot start a new action. Omit the flag for
an interactive local setup that starts the pinned Node adapter for each quote.

Omit `--sell-at-usd` for the existing immediate one-action demonstration. When
it is set, the runner keeps observing the rule while stopped and exposes it via
`swap status`, MCP, Prometheus, and Telegram `/price`. Reaching the target while
stopped sends an informational alert; it does not grant execution authority.
After the operator enables a bounded number of actions, a satisfied market rule and
satisfied minimum swap rate allow the normal bounded swap flow to
continue. A finalized action consumes one slot; the independent recovery timer
then clears only its provisional marker, so a multi-action grant can continue.
A failed action is latched before its terminal journal record is written. If a
crash leaves only the recovery marker, matching finalized failure evidence
recreates that same operator-acknowledgeable stop. An ambiguous action keeps
the recovery marker and blocks every remaining slot until review.

If you only see a sell notification, this is usually expected for this
one-action path. For a full AFK round trip setup (sell -> buy -> sweep), both
sell and buy legs must be enabled. `--max-trades 1` allows one action on each
leg: one sell and one buy. If a leg is not armed, failed health checks, or funds are not
available, that leg will stay silent and `strategy show` will show why.

For a full run, `send_started_records` and `submitted_records` and Telegram
messages should move in this order:
- Sell executed (if sell leg fires)
- Buy executed (if buy leg fires)
- Sweep executed (if balance is above floor and route is healthy)

If one leg is healthy but waiting on market conditions, status stays active and
Telegram remains quiet until the trigger condition is actually met.

For a buy, the wallet must already have its canonical devUSDC token account —
and the devUSDC to spend. Run the sell leg once first: it creates that account
and produces the devUSDC the buy spends. A buy set up on a fresh wallet is
refused, because the instruction sequence has no account to spend from.
Amounts use ordinary decimal devUSDC at the command line and exact six-decimal
base units in the generated policy:

```sh
./bin/mithril-agent swap discover \
  --direction buy \
  --wallet-keypair /absolute/private/devnet-keypair.json \
  --node-command /absolute/path/to/node \
  --quote-script "$PWD/adapters/orca/quote.mjs" \
  --spend-usdc 0.100000 \
  --slippage-bps 100

./bin/mithril-agent swap setup \
  --direction buy \
  --dir /absolute/private/mithril-agent-buy \
  --wallet-keypair /absolute/private/devnet-keypair.json \
  --mithril-command /absolute/path/to/mithril \
  --node-command /absolute/path/to/node \
  --quote-script "$PWD/adapters/orca/quote.mjs" \
  --quote-socket /run/mithril-agent-quote/quote.sock \
  --spend-usdc 0.100000 \
  --daily-spend-usdc 0.100000 \
  --daily-native-fee-cap-lamports 100000 \
  --slippage-bps 100 \
  --confirm-min-output-amount CURRENT_DISCOVERED_MINIMUM \
  --primary-trust-domain REPLACE_WITH_PRIMARY_PROVIDER_COMPANY \
  --secondary-trust-domain REPLACE_WITH_SECONDARY_PROVIDER_COMPANY \
  --buy-at-usd 75.00
```

Omit `--buy-at-usd` for an immediate one-action demonstration. A configured
buy target means “buy SOL at or below this price”; it has the same two-source
freshness, executable-rate, one-action, and stopped-by-default controls as the
sell target.

Use `strategy enable --max-trades` and `strategy show` to verify both legs are
actually armed before expecting a buy and sweep notification.

The target directory must not exist. Setup repeats the live read-only quote and
fails if its current minimum no longer equals the confirmed value. It validates
the fixed Devnet route shape, creates the local risk and submitter keys, and
writes all mutually bound config and policy files with private permissions. It
never stores RPC URLs. The result includes the config path and argument arrays
for `swap plan`, `preflight`, `swap check`, and `swap demo`; the first three are
read-only, while `swap demo` grants and waits for one bounded action. Argument
arrays avoid emitting paths as unsafe shell text.

The trust-domain names are bounded, nonsecret operator assertions about who
controls each evidence endpoint. Use different provider organizations or
control planes, not merely two hostnames from the same provider.

Changing credentials at the same provider origin needs no config update. If an
evidence provider's host or port changes, stop the runner and bind the new
origins explicitly. The command refuses enabled or unresolved state, verifies
all three origins are distinct, records the change in the journal, and never
stores a URL:

```sh
sudo systemd-run --quiet --wait --pipe --collect \
  --uid=mithril-agent --gid=mithril-agent \
  -p 'EnvironmentFile=/etc/mithril-agent/rpc.env' \
  -p 'EnvironmentFile=-/etc/mithril-agent/mcp.env' \
  /usr/local/libexec/mithril-agent/mithril-agent swap bind-providers \
  --config /var/lib/mithril-agent/agent/config.json \
  --reason 'rotate evidence provider'
```

The existing trust-domain names are preserved unless replacement names are
passed explicitly. The command updates both the agent configuration and the
submitter's protected binding. If power is lost between those two atomic
writes, preflight rejects the mismatch and rerunning the command completes the
migration. Restart the swap service and rerun preflight afterward; the service
still starts with new actions disabled.

If Orca is deliberately upgraded, do not edit an existing setup in place.
Verify the new program-data account, deployment slot, and authority through two
independent providers, update the pinned constants and tests, run discovery
again, and create a new setup. The changed profile fingerprint requires a new
operator approval.

If only the public address is available, discovery can avoid loading the key:

```sh
./bin/mithril-agent swap discover \
  --owner WALLET_PUBLIC_KEY \
  --node-command /absolute/path/to/node \
  --quote-script "$PWD/adapters/orca/quote.mjs"
```

The config contains these sections:

```text
swap       fixed route and amount, fee/reserve/daily limits, health and time gates
mcp        Mithril binary plus: mcp --profile monitor
quote      direct Node.js adapter paths, or one protected Unix socket
evidence   distinct operator-declared trust domains for both evidence providers
policy     risk-authority binary, policy, key, key ID, public key
signer     signer binary, signer policy, Devnet wallet keypair
submitter  submitter binary, policy, X25519 private-key document
control    private stop/enable state path
journal    private append-only journal path
```

The same values must be bound across the files:

```text
swap profile fingerprint
  -> signer policy profile_sha256
  -> risk policy transaction_policy

swap owner + exact input + fee + daily cap + schedule + route
  -> signer policy
  -> submitter policy

risk public key -> signer policy
submitter public key -> signer policy
wallet public key -> swap owner
```

All config, policy, key, control, and journal paths must be absolute. Private
files must be mode `0600`; their directories must not be group- or
world-writable. Preflight rejects symlinks, writable executables, path
collisions, provider aliases, key drift, policy drift, and unsafe Linux clock
state. Each key is opened only by its child process; the runner receives and
checks the corresponding public identity.

## Fresh supervised Linux installation

Use a dedicated full-node or RPC host, not a voting validator. Build the nine
Go binaries and install the pinned Orca dependencies as described above. The
commands below are for a new host only; they are not an upgrade or rolling
replacement procedure. An upgrade may reuse the later setup and validation
requirements, but must use the staged cutover above for runtime files.

```sh
sudo install -m 0644 deploy/sysusers/mithril-agent-status.conf \
  /usr/lib/sysusers.d/mithril-agent-status.conf
sudo install -m 0644 deploy/sysusers/mithril-agent-dashboard.conf \
  /usr/lib/sysusers.d/mithril-agent-dashboard.conf
sudo systemd-sysusers /usr/lib/sysusers.d/mithril-agent-status.conf
sudo systemd-sysusers /usr/lib/sysusers.d/mithril-agent-dashboard.conf

sudo install -d -o root -g root -m 0755 /usr/local/libexec/mithril-agent
sudo install -d -o root -g root -m 0755 /usr/local/share/doc/mithril-agent
sudo install -o root -g root -m 0644 \
  README.md OVERVIEW.md ROADMAP.md QUICKSTART.md DEMO.md OPERATIONS.md \
  /usr/local/share/doc/mithril-agent/
sudo install -o root -g root -m 0755 \
  ./bin/mithril-agent \
  ./bin/mithril-agent-policy \
  ./bin/mithril-agent-signer \
  ./bin/mithril-agent-submitter \
  ./bin/mithril-agent-quote \
  ./bin/mithril-agent-telegram \
  ./bin/mithril-agent-status-bridge \
  ./bin/mithril-agent-paper-status-bridge \
  ./bin/mithril-agent-paper-dashboard \
  /usr/local/libexec/mithril-agent/
sudo install -o root -g root -m 0755 /absolute/pinned/node \
  /usr/local/libexec/mithril-agent/node
sudo install -o root -g root -m 0644 adapters/orca/quote.mjs \
  /usr/local/libexec/mithril-agent/quote.mjs
# The payout-wallet verification page is embedded in the mithril-agent binary.
sudo install -d -o root -g root -m 0755 \
  /usr/local/libexec/mithril-agent/node_modules
sudo cp -a adapters/orca/node_modules/. \
  /usr/local/libexec/mithril-agent/node_modules/
sudo chown -R root:root /usr/local/libexec/mithril-agent
sudo chmod -R go-w /usr/local/libexec/mithril-agent

sudo install -d -o root -g root -m 0711 /etc/mithril-agent
sudo install -d -o mithril-agent -g mithril-agent -m 0700 \
  /var/lib/mithril-agent /var/lib/mithril-agent/private

# Give the restricted agent its own private copy. Do not use a symlink.
# Repeat this after an intentional Mithril config change.
sudo install -o mithril-agent -g mithril-agent -m 0600 \
  /absolute/path/to/mithril/config.toml /var/lib/mithril-agent/node.toml
```

Create the environment files without replacing an existing file, then edit
them as root:

```sh
sudo touch \
  /etc/mithril-agent/rpc.env \
  /etc/mithril-agent/quote.env \
  /etc/mithril-agent/mcp.env \
  /etc/mithril-agent/price.env \
  /etc/mithril-agent/telegram-operator.env
sudo chown root:root \
  /etc/mithril-agent/rpc.env \
  /etc/mithril-agent/quote.env \
  /etc/mithril-agent/mcp.env \
  /etc/mithril-agent/price.env \
  /etc/mithril-agent/telegram-operator.env
sudo chmod 0600 \
  /etc/mithril-agent/rpc.env \
  /etc/mithril-agent/quote.env \
  /etc/mithril-agent/mcp.env \
  /etc/mithril-agent/price.env \
  /etc/mithril-agent/telegram-operator.env
sudoedit /etc/mithril-agent/rpc.env
sudoedit /etc/mithril-agent/quote.env
sudoedit /etc/mithril-agent/mcp.env
sudoedit /etc/mithril-agent/price.env
sudoedit /etc/mithril-agent/telegram-operator.env
```

`rpc.env` contains only the Mithril, primary-evidence, and
secondary-evidence RPC variables. `quote.env` contains only the quote RPC
variable. `price.env` may stay empty for the default on-chain Pyth push source;
it contains a Pyth API key only when the optional Hermes HTTP source is chosen.
`mcp.env` contains only nonsecret Mithril observation-path overrides. Put the
Telegram variables documented below only in `telegram-operator.env`.

Install the Devnet keypair at
`/var/lib/mithril-agent/private/devnet-keypair.json`, owned by
`mithril-agent:mithril-agent` with mode `0600`. Run the discovery and setup
commands from the Configuration section as the `mithril-agent` identity,
loading the protected environments through the service manager. A legacy
single-leg setup uses `/var/lib/mithril-agent/agent`; the guided strategy uses
its private root below the service account's `HOME`. Both use
`/usr/local/libexec/mithril-agent/node`, the installed `quote.mjs`, and
`--quote-socket /run/mithril-agent-quote/quote.sock`.

For the guided strategy path, use this exact supervised shape. Replace only the
Mithril executable path if this host installs it elsewhere:

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

After the bootstrap sell, repeat that command with `--resume` appended. Keep
all four environment-file entries: `quote.env` is required while setup obtains
the buy route, and the leading `-` makes the optional files non-fatal when they
are absent. `HOME` is required because it holds the strategy pointer. Do not
replace this with an ad-hoc `systemd-run` command assembled from memory.

After initial setup and again after resume, regenerate the runner and alert
units from that same strategy pointer:

```sh
sudo -u mithril-agent env HOME=/var/lib/mithril-agent \
  /usr/local/libexec/mithril-agent/mithril-agent service install \
  --output /var/lib/mithril-agent/.mithril-agent/mithril-agent-run.service
```

Review and run the privileged install commands it prints. Do not hand-edit the
unit or omit an environment file: the generated service is the source of truth
for leg paths, writable state, fixed metrics ports, status sockets and Telegram
bridges.

For the supervised immediate-sell pilot, these are the complete command shapes.
Replace the confirmed minimum, both provider-company placeholders with the
actual independent operator names, and any intentionally reviewed limits:

```sh
sudo systemd-run --quiet --wait --pipe --collect \
  --uid=mithril-agent --gid=mithril-agent \
  --property=UMask=0077 \
  --property=EnvironmentFile=/etc/mithril-agent/rpc.env \
  --property=EnvironmentFile=/etc/mithril-agent/quote.env \
  --property=EnvironmentFile=-/etc/mithril-agent/mcp.env \
  --property=EnvironmentFile=-/etc/mithril-agent/price.env \
  /usr/local/libexec/mithril-agent/mithril-agent swap discover \
  --wallet-keypair /var/lib/mithril-agent/private/devnet-keypair.json \
  --node-command /usr/local/libexec/mithril-agent/node \
  --quote-script /usr/local/libexec/mithril-agent/quote.mjs \
  --input-lamports 1000000 \
  --slippage-bps 100

sudo systemd-run --quiet --wait --pipe --collect \
  --uid=mithril-agent --gid=mithril-agent \
  --property=UMask=0077 \
  --property=EnvironmentFile=/etc/mithril-agent/rpc.env \
  --property=EnvironmentFile=/etc/mithril-agent/quote.env \
  --property=EnvironmentFile=-/etc/mithril-agent/mcp.env \
  --property=EnvironmentFile=-/etc/mithril-agent/price.env \
  /usr/local/libexec/mithril-agent/mithril-agent swap setup \
  --dir /var/lib/mithril-agent/agent \
  --wallet-keypair /var/lib/mithril-agent/private/devnet-keypair.json \
  --mithril-command /usr/local/bin/mithril \
  --node-command /usr/local/libexec/mithril-agent/node \
  --quote-script /usr/local/libexec/mithril-agent/quote.mjs \
  --quote-socket /run/mithril-agent-quote/quote.sock \
  --input-lamports 1000000 \
  --slippage-bps 100 \
  --confirm-min-output-amount CURRENT_DISCOVERED_MINIMUM \
  --primary-trust-domain REPLACE_WITH_PRIMARY_PROVIDER_COMPANY \
  --secondary-trust-domain REPLACE_WITH_SECONDARY_PROVIDER_COMPANY
```

One setup pins one direction. Create separate isolated setup and state paths
for buy and sell; do not edit one setup back and forth or run two runners over
the same state.

Do not hand-install the older runner, demo, status, or Telegram examples from
`deploy/systemd`. They predate the generated per-leg signer, risk-authority,
submitter, operator, and recovery boundaries and are retained only for
historical review. For both a strategy and a single-leg setup, `mithril-agent
service install` generates the runner plus signer, risk-authority, submitter,
operator, recovery, status, and optional alert units from the recorded paths
and prints the privileged installation and verification commands.

The generated runner always starts with new actions disabled. Accept the
installation only after `clock-check`, preflight, the read-only live check,
the quote and runner services, status sockets, Prometheus target, notifier,
and, when configured, the Telegram canary all pass. Do not grant an action
merely because the services are active.

## One bounded Devnet action

First run the offline gate. It validates the protected Mithril MCP executable
and argument encoding, and starts the risk-authority, signer, and submitter
processes briefly in identity-only mode. It does not start Mithril MCP or the
quote adapter, make a network request, create runtime state, authorize an
action, sign, or submit anything:

```sh
./bin/mithril-agent preflight --config /absolute/private/config.json
```

Then run the live read-only gate. It connects to Mithril MCP, validates its
required read-only tools and node identity, checks the live Orca deployment and,
where applicable, the buy input account and token-account rent through the
evidence providers, validates a fresh quote, fee, and simulation, and evaluates
any configured price rule. It does not open
the journal or control file, start the signer, authorize an action, or submit a
transaction:

```sh
./bin/mithril-agent swap check --config /absolute/private/config.json
```

The demonstration repeats this gate. With rate-limited evidence providers,
use the standalone check for setup or troubleshooting rather than running it
immediately before `swap demo`.

On failure, the check emits a bounded JSON result with `status: failed` and the
policy stage that stopped it. A slot cross-check failure also includes the
validated slot difference and configured threshold. It never includes an RPC
URL, credential, provider payload, or raw MCP error.

On success, the check also emits a bounded `policy` summary with the direction,
assets, input amount in raw base units, slippage, fee and reserve limits,
direction-specific daily caps, and schedule window. It intentionally omits
wallet, pool, endpoint, and credential data.

After the supervised runner is healthy, the simplest complete demonstration is:

```sh
./bin/mithril-agent swap demo --config /absolute/private/config.json
```

For the supervised layout, keep the private config and RPC environments away
from the operator shell. Follow progress in one terminal:

```sh
sudo journalctl --follow --unit=mithril-agent-demo.service
```

Start the hardened demonstration unit from another terminal and wait for it:

```sh
sudo systemctl start --wait mithril-agent-demo.service
```

The unit records progress, repeats the live gate, grants a bounded
Devnet action, waits for final confirmation, and stops new actions before it
reports success. Its sandbox matches the runner's filesystem, process, network,
and executable-memory restrictions. `ExecStopPost` attempts to revoke new
actions after success, failure, timeout, or a manual stop; the public status is
the authoritative confirmation that control returned to `no_new_actions`.
Losing the terminal or SSH session does not prove the service stopped. Inspect
the named unit; to abort it, stop `mithril-agent-demo.service` and inspect
status. Do not rerun while an action is pending or needs review. Direct CLI use
supports `--json`; success and failure both use stable JSON on stdout.

The manual commands below expose each step for review and troubleshooting.

For a supervised installation, run that check as the service identity while
letting systemd load the root-owned environment files. Never source or print
those files in an operator terminal:

```sh
sudo systemd-run --quiet --wait --pipe --collect \
  --uid=mithril-agent --gid=mithril-agent \
  -p 'EnvironmentFile=/etc/mithril-agent/rpc.env' \
  -p 'EnvironmentFile=/etc/mithril-agent/quote.env' \
  -p 'EnvironmentFile=-/etc/mithril-agent/mcp.env' \
  -p 'EnvironmentFile=-/etc/mithril-agent/price.env' \
  /usr/local/libexec/mithril-agent/mithril-agent swap check \
  --config /var/lib/mithril-agent/agent/config.json
```

Do not enable an action unless the check reports `status: ready` and
`swap status` reports a recent, non-stale runner, `control.mode: no_new_actions`,
`attention_required: false`, no submitted action, and zero cumulative
`send_started_records` and `submitted_records`. The one-action demonstration
uses a fresh zero-send journal; do not reuse a setup that has crossed a send
boundary. The node, quote, runner, monitoring, and notification services must
also be healthy.

Start the supervised runner. It starts with no authority:

```sh
./bin/mithril-agent swap run \
  --config /absolute/private/config.json \
  --interval 10s \
  --metrics-address 127.0.0.1:9191
```

From another terminal, grant one short-lived Devnet action:

```sh
./bin/mithril-agent swap enable \
  --config /absolute/private/config.json \
  --duration 15m \
  --max-actions 1 \
  --reason 'bounded Devnet swap'
```

Each action is consumed before the send boundary, so control
automatically returns to `no_new_actions`. Do not run `swap enable` a second
time. Poll `swap status` until it reaches a terminal result, then issue an
explicit `swap stop` even after a successful automatic stop.

Inspect or stop it without exposing endpoints, addresses, or operator reasons:

```sh
./bin/mithril-agent swap status --config /absolute/private/config.json

./bin/mithril-agent swap stop \
  --config /absolute/private/config.json \
  --reason 'operator stop'

./bin/mithril-agent swap drain \
  --config /absolute/private/config.json \
  --reason 'maintenance'
```

In the supervised layout, the operator reads status through the bounded socket
and runs control commands as the service identity. These commands do not need
RPC credentials:

```sh
/usr/local/libexec/mithril-agent/mithril-agent status \
  --status-socket /run/mithril-agent-status.sock

sudo systemd-run --quiet --wait --pipe --collect \
  --uid=mithril-agent --gid=mithril-agent --property=UMask=0077 \
  /usr/local/libexec/mithril-agent/mithril-agent swap stop \
  --config /var/lib/mithril-agent/agent/config.json \
  --reason 'operator stop'

sudo systemd-run --quiet --wait --pipe --collect \
  --uid=mithril-agent --gid=mithril-agent --property=UMask=0077 \
  /usr/local/libexec/mithril-agent/mithril-agent swap drain \
  --config /var/lib/mithril-agent/agent/config.json \
  --timeout 16m --reason 'maintenance'
```

A successful demonstration requires all of the following:

- `decision: complete`, `submitted: true`, and `verdict: finalized`;
- output at or above the configured minimum;
- `control.mode: no_new_actions` and `attention_required: false`;
- a nonempty transaction signature and nonempty journal;
- matching independent-provider effects and no second submission; and
- an understandable completion notification, plus a submitted notification
  when confirmation remains pending long enough to be observed.

On a failed, halted, divergent, stale, timed-out, or attention-required result,
run `swap stop`, do not re-enable, and preserve the terminal action ID and
outcome for review. Telegram proves notification delivery, not transaction
finality; status and the validated journal are authoritative.

A failed action latches `attention_required` until an operator reviews and
acknowledges that exact action. A halted action remains latched for the lifetime
of that setup even after its review is recorded. Review is deliberately offline
because the runner is the journal's only writer: stop the runner, copy
the `terminal_action_id` and `terminal_outcome` from `swap status`, then run:

```sh
./bin/mithril-agent swap acknowledge \
  --config /absolute/private/config.json \
  --action-id TERMINAL_ACTION_ID \
  --outcome failed \
  --reason 'reviewed terminal evidence'
```

For a supervised failed-action acknowledgement, stop the swap service first,
then use the service identity:

```sh
sudo systemctl stop mithril-agent-swap.service
sudo systemd-run --quiet --wait --pipe --collect \
  --uid=mithril-agent --gid=mithril-agent --property=UMask=0077 \
  /usr/local/libexec/mithril-agent/mithril-agent swap acknowledge \
  --config /var/lib/mithril-agent/agent/config.json \
  --action-id TERMINAL_ACTION_ID --outcome failed \
  --reason 'reviewed terminal evidence'
```

The acknowledgement is stored in the hash-chained journal with its bounded
reason and is idempotent only for the same action, outcome, and reason. It does
not enable execution. For a provider-confirmed `failed` action, restart the
runner, wait for the next schedule window, and explicitly enable a new bounded
action only after the failure has been independently reviewed.

A `halted` result means the providers left the transaction unresolved or
diverged. The same acknowledgement command may record the review with
`--outcome halted`, but it reports `execution_permanently_blocked: true` and
does not clear the control latch. Preserve the setup for external investigation
and audit; the runner does not reclassify an acknowledged halt. This is also a
recovery exception to the ordinary upgrade prohibition: a new isolated setup
may be considered only after independent evidence establishes and durably
records the final outcome. That record is not `swap acknowledge`: the
deployment owner must write an off-host append-only incident record containing
the action ID, signature, provider identities, final status and slot, evidence
digests, observation times, and reviewer identity. Preserve the halted setup,
and do not enable the new signer ledger until the first UTC midnight after the
halted action's signature.

The first healthy runner cycle normally waits for the sustained-health gate.
A later cycle must meet both the configured time interval and slot advance.
`swap stop` prevents new sends while leaving the runner available to reconcile
an already-submitted transaction. Before terminating the runner, `swap drain`
sets the same stop state and waits for a post-request `stopped`, finalized
`complete`, or pre-send `canceled` cycle. Failed or halted results require
operator attention and make drain return an error. Its default timeout follows
the configured reconciliation limit plus a 30-second margin.

## Connect any MCP client

The supported supervised topology runs the MCP client on the same Linux host
as the bounded status socket. A desktop client cannot open the remote Unix
socket directly. Do not give an assistant a general SSH key merely to bridge
MCP; use a separately reviewed forced-command SSH account or a future
authenticated remote transport if desktop access is required.

On the Linux host, give the human operator access only to the bounded status
socket. A legacy single-leg setup uses `/run/mithril-agent-status.sock`; a
generated strategy uses one socket per leg. Reconnect the login session after
adding the group:

```sh
sudo usermod -aG mithril-agent-status "$USER"
/usr/local/libexec/mithril-agent/mithril-agent status \
  --status-socket /run/mithril-agent-status-sell.sock
```

Configure an MCP client to start the same binary as a managed stdio subprocess.
For a full strategy, create a separate read-only MCP entry for each configured
leg (`sell`, `buy`, and `sweep`); this version has no combined multi-leg MCP
socket. Print the complete paste-ready client configuration with:

```sh
/usr/local/libexec/mithril-agent/mithril-agent mcp config
```

It discovers the active generated sockets and emits a distinct MCP server entry
for each leg. The equivalent entry for one leg is:

```text
command: /usr/local/libexec/mithril-agent/mithril-agent
args:    mcp --status-socket /run/mithril-agent-status-sell.sock
```

Do not launch the MCP command separately and wait for a prompt; waiting for
stdin is normal MCP behavior. The socket mode cannot read the private config,
journal, RPC environments, or wallet. `--config` remains available for a local
developer who already owns those private files; it is not the normal client
setup.

The server exposes four standard read-only tools:

- `mithril_agent_info` — active profile and disabled authority boundaries;
- `mithril_agent_strategy` — configured rules and bounded spending settings,
  without wallet addresses or credentials;
- `mithril_agent_status` — current runner cycle, most recent action, journal,
  and control state.
- `mithril_agent_operator_guide` — the safe local demonstration command and
  the boundary between an MCP client and transaction authority.

This is standard MCP over stdio and is tested with the official Go MCP client,
not with client-specific behavior.

### Optional conversational layer

Any MCP-capable assistant may sit above these read-only tools to explain status
and alerts. That layer is optional and uses the operator's own model account.
It must not receive wallet keys or gain a second submission path. Enabling an
action remains an explicit, bounded Go control operation; signing, submission,
reconciliation, and audit stay inside the deterministic agent services.

For a self-hosted Telegram command surface, run the separate
`mithril-agent-telegram` process. It long-polls one operator-owned bot, accepts
only configured numeric chat IDs, and exposes `/help`, `/status`, `/price`, and
`/last_trade`. `/price` reports the configured SOL/USD rule, conservative
two-source observation, and the direction-appropriate executable rate when a
quote has been evaluated. Its durable private cursor normally prevents command
replay after restart. Telegram delivery is at-least-once: a crash or lost HTTP
response after Telegram accepts a reply can produce a duplicate read-only
message.

Put these values in a root-owned mode-0600 environment file, never in Git or a
command line:

```text
MITHRIL_AGENT_TELEGRAM_BOT_TOKEN=BOT_TOKEN
MITHRIL_AGENT_TELEGRAM_CHAT_IDS=NUMERIC_CHAT_ID
MITHRIL_AGENT_TELEGRAM_EXPLANATIONS=off
```

Telegram never shows you your own chat ID. `mithril-agent-telegram link`
prints the IDs your bot has heard from — message the bot, then run it.

Alert expectation:

- Telegram alerts are sent per enabled leg, not per strategy name.
- If you are in one-action demo mode, one alert path is expected.
- For the all-in-one strategy (sell + buy + sweep), you should see each leg only
  when it is enabled, funded, and passes all gates.
- `/status`, `/price`, and `/last_trade` label every configured leg, so one
  healthy leg cannot hide a stopped or failed sibling.
- `strategy show` shows the live leg state if one leg is not producing output.

Setting these variables starts nothing: alerts come from a separate process.
Prove it can reach you before a trade depends on it:

```bash
# Development shell with the two Telegram variables already set:
mithril-agent-telegram test

# Supervised host; systemd loads the protected environment without printing it:
sudo systemd-run --quiet --wait --pipe --collect \
  --uid=mithril-agent-telegram --gid=mithril-agent-telegram \
  -p 'EnvironmentFile=/etc/mithril-agent/telegram-operator.env' \
  /usr/local/libexec/mithril-agent/mithril-agent-telegram test

mithril-agent-telegram --status-socket PATH --cursor PATH   # leave running
```

A chat that fails `test` fails silently for a real trade. The usual cause is
that nobody has pressed Start in that chat — Telegram refuses to deliver to a
user who has not started the bot.

The fresh supervised installation above already installs the bridge and
Telegram binaries, units, identities, and protected environment file. A
Telegram-only development host may install that same subset, but it must keep
the same service users, status socket, root-owned environment file, and unit
sandbox; do not invent a second layout.

The Telegram service prints one startup line and then waits for messages. This
is normal long-polling behavior. It runs as a dedicated user and can read only
the validated, bounded status snapshot delivered through the protected Unix
socket. A second dedicated bridge user receives a root-copied snapshot through
systemd credentials for one connection and then exits. Neither process can read
the agent's wallet, configuration, journal, or RPC environment.

The supplied unit blocks loopback, link-local, and private-network services;
only the systemd-resolved addresses and optional `127.0.0.2` model endpoint are
allowed back through. If a host uses a different resolver, allow only that
exact resolver address and repeat the local-RPC denial test before deployment.

Optional `/explain QUESTION` support can use the operator's own OpenAI API
account:

```text
MITHRIL_AGENT_TELEGRAM_EXPLANATIONS=openai
OPENAI_API_KEY=OPERATOR_API_KEY
MITHRIL_AGENT_OPENAI_MODEL=OPERATOR_CHOSEN_MODEL
MITHRIL_AGENT_TELEGRAM_DAILY_EXPLANATION_REQUESTS=20
```

API usage is billed separately from a ChatGPT or Codex subscription. A local
Responses-compatible model is also supported. The shipped service sandbox
reserves `127.0.0.2` for that endpoint:

```text
MITHRIL_AGENT_TELEGRAM_EXPLANATIONS=local
OPENAI_API_KEY=LOCAL_SERVER_KEY
MITHRIL_AGENT_OPENAI_MODEL=LOCAL_MODEL_NAME
MITHRIL_AGENT_OPENAI_BASE_URL=http://127.0.0.2:8080
MITHRIL_AGENT_TELEGRAM_DAILY_EXPLANATION_REQUESTS=20
```

The model receives only the bounded
question and status projection and has no tools. Requests set `store: false`;
provider retention and prompt-caching policies still apply. Provider failure
disables `/explain` for that request but does not affect deterministic commands,
alerts, the node, or execution. Explanation requests consume a private durable
daily request-count budget before the provider call; the default is 20 requests
per UTC day. This limits request volume, not the provider's monetary charge;
choose provider-side spending limits separately.

The surface exposes four read-only tools: `mithril_agent_info` (what the agent
is and what it deliberately cannot do), `mithril_agent_strategy` (the configured
rules and spending bounds), `mithril_agent_status` (the current cycle and last
action), and `mithril_agent_operator_guide` (the boundary plus the one safe
local command).

`mithril_agent_strategy` answers "what am I configured to do" — direction, size
per trade, daily cap, trades funded per day, the price rule, and whether a sweep
destination is configured and its proof still verifies. It carries **no
address**: a sweep destination is an account, and this surface promises not to
expose accounts. Whether one exists and whether its proof is valid answers every
operational question without naming the wallet.

## Alerts

`deploy/prometheus/mithril-agent.rules.yml` alerts on runner loss or quote
unavailability observed by the runner, stale cycles, attention-required
results, pending transaction recovery, invalid control state, enabled
execution, reached price targets while execution is stopped, submitted and
completed swaps, stale reconciliation, and journal capacity. For the
supervised demonstration, Alertmanager must deliver those alerts to Telegram
through Mithril's separate `mithril-notifier` process.
The deterministic notifier is outbound-only, and the interactive Telegram
operator exposes read-only status commands. Neither process can enable, stop,
sign, submit, or configure a transaction.

On a same-host Prometheus deployment, install the rule file as
`/etc/prometheus/rules/mithril-agent.yml` and create
`/etc/prometheus/targets/mithril-agent.json` with the same deployment ID used by
the node monitor. A complete generated strategy has fixed ports: sell `9310`,
buy `9311`, and sweep `9312`:

```json
[
  {
    "targets": ["127.0.0.1:9310", "127.0.0.1:9311", "127.0.0.1:9312"],
    "labels": {"deployment_id": "replace-at-deploy"}
  }
]
```

Before the buy leg exists, leave `9311` out of the target list; the sweep stays
on `9312`. Legacy single-leg runners keep their explicitly configured ports
such as `9191` and `9192` and must not run beside the generated strategy.

Pin the exact Mithril commit used by the node and follow
`https://github.com/Overclock-Validator/mithril/blob/COMMIT/prometheus/README.md`,
replacing `COMMIT` with that full commit ID. The pinned inputs are
`prometheus/prometheus.yml`, `prometheus/alertmanager.yml`,
`prometheus/rules/mithril.yml`, `prometheus/inventory.example.json`,
`prometheus/blackbox.yml`, and `cmd/mithril-notifier`. The notifier service
unit, protected config, client CA, server certificate and key, Alertmanager
client certificate and key, and external deadman are deployment-owned assets;
the upstream repository deliberately does not supply them. If the deployment
does not already provide and verify those assets, stop: the supervised
demonstration is not ready.

Create the target JSON shown above in a protected staging directory, replace
the example deployment ID, then install and validate the agent addition:

```sh
sudo install -m 0644 deploy/prometheus/mithril-agent.rules.yml \
  /etc/prometheus/rules/mithril-agent.yml
sudo install -o root -g prometheus -m 0640 \
  /absolute/protected/mithril-agent.json \
  /etc/prometheus/targets/mithril-agent.json
promtool check rules /etc/prometheus/rules/mithril-agent.yml
promtool check config /etc/prometheus/prometheus.yml
amtool check-config /etc/alertmanager/alertmanager.yml
```

Use the pinned deployment's documented reload or restart procedure; do not
assume its units implement `ExecReload`. Confirm `up{job="mithril-agent"} == 1`.
Starting the pinned notifier sends its silent Telegram canary immediately;
verify that canary arrives in the intended chat and that the notifier metrics
report a successful current probe. Do not enable a swap when any validation,
reload, target, mTLS, route, canary, or external-deadman check fails.

Informational completion
events use a neutral Telegram update and do not send a later recovery message.
The notifier gives known events short operator titles such as `Devnet trade
complete`, `Mithril node behind`, and `Mithril agent offline`. It omits
Prometheus state words and low-level labels, adds a next step only when useful,
and renders grouped incidents as bounded bullet summaries. Its silent route
canary is labelled `Telegram alerts working` and says that no action is needed.

The notifier is a deployment prerequisite, not an optional replacement for
the read-only Telegram command process. Use the notifier, Alertmanager mTLS,
target inventory, and deadman procedure from the matching Mithril release;
do not build a second alert engine in this repository. Before granting an
action, verify all of the following:

- Prometheus reports the `mithril-agent`, Alertmanager, and notifier targets up;
- `mithril_notification_route_configured{route="primary_telegram"}` is `1`;
- `mithril_notification_probe_success{route="primary_telegram"}` is `1` and
  its last-success timestamp is less than two hours old;
- the intended chat received the silent `Telegram alerts working` canary; and
- an independent deadman receiver outside the operations host is receiving
  the one-minute heartbeat and will alert on silence.

The canary proves the Telegram route accepted that message. It does not prove
the node, runner, or trade is healthy; their own metrics and rules provide that
evidence. A phone receiving alerts is not itself a deadman because it cannot
detect that the sending host went silent.

The deadman and alert receiver must run outside the agent process. Use the
`mithril-agent-run.service` generated by `mithril-agent service install`. It
forces stopped state before every ordinary start and after every ordinary stop,
uses the isolated per-leg signer and risk-authority sockets, and requires a
fresh operator enable after restart. When an action has crossed the durable
send boundary, the automatic hook instead fails visibly and preserves
`recovery_pending`; review independent evidence, then use an explicit operator
stop before starting the runner again.

The top-level `devnet-*` commands are the retained but unsupported transfer
pilot. They have no production service template. New Orca deployments must use
the `swap` commands and the generated runner.

For a generated single-leg unit, create the setup at
`/var/lib/mithril-agent/agent` as the
dedicated, non-login `mithril-agent` user. The unit mounts those private
service-owned files read-only and makes only the generated `state` directory
writable. This is integrity protection inside the service sandbox, not a
separate-key custody boundary. RPC URLs remain in the root-owned
`/etc/mithril-agent/rpc.env` file.

Put nonsecret local MCP discovery overrides such as `MITHRIL_LOG_DIR`,
`MITHRIL_STATE_PATH`, and `MITHRIL_REPLAY_PATH` in the optional root-owned
`/etc/mithril-agent/mcp.env` file. This keeps the full Mithril node
configuration outside the operations service while still binding MCP evidence
to the local node paths. The required `MITHRIL_RPC_URL` override is derived from
`MITHRIL_AGENT_MITHRIL_RPC_URL` by the runner.

The supervised swap observer disables the optional file-log scan because the
node service writes to journald. It still requires Mithril's structured RPC,
metrics, state, verification, divergence, provenance, and host checks.

### Node-state filesystem access

The service identity therefore needs only narrow filesystem evidence access.
Mithril replaces `mithril_state.json` atomically, so a one-time ACL on that file
does not survive. Use the dedicated state-reader group instead: the setgid
accounts directory gives replacement files that group, while mode `2710`
allows traversal without listing and Mithril writes only the state file as
group-readable (`0640`). Other AccountsDB files stay private under the node's
restrictive umask. Adapt these paths to the active deployment:

```sh
sudo groupadd -f --system mithril-node-state
sudo usermod -aG mithril-node-state mithril-agent
sudo setfacl -m u:mithril-agent:--x NODE_STORAGE_ANCESTOR
sudo chgrp mithril-node-state NODE_ACCOUNTS_DIRECTORY
sudo chmod 2710 NODE_ACCOUNTS_DIRECTORY
sudo chgrp mithril-node-state NODE_ACCOUNTS_DIRECTORY/mithril_state.json
sudo chmod 0640 NODE_ACCOUNTS_DIRECTORY/mithril_state.json
sudo setfacl -m u:mithril-agent:r-x NODE_LOG_DIRECTORY
sudo setfacl -m u:mithril-agent:r-- NODE_LOG_DIRECTORY/replay_timings.jsonl
```

Install `deploy/sysusers/mithril-agent-status.conf` before the units; it creates
the group and adds the standard agent account. The runner units request that
supplementary group explicitly. A Mithril build older than the `0640` state
writer can still replace the file as `0600`; upgrade it before relying on
unattended monitoring. Preflight reports `mcp_inputs=failed` when the state
input is absent or unreadable.

Verify the final permissions as `mithril-agent`: storage `statfs`, divergence
directory listing, and replay-timing reads must work, while opening the raw
node log must fail. Prefer a node-start deployment hook or another explicit
provisioning step for new run directories instead of broad recursive read
permission.

The swap service keeps `MemoryDenyWriteExecute=yes`. The pinned Orca adapter
uses V8 and WebAssembly, so a supervised setup must run
`mithril-agent-quote` as a separate unprivileged local service under its own
OS identity and configure the runner with `--quote-socket`. Give that service only
`MITHRIL_AGENT_QUOTE_RPC_URL`; the runner keeps the Mithril and evidence RPC
settings. The quote socket is writable only by the quote identity and readable
by the runner through a dedicated IPC group. Every response is still
independently decoded and validated by Go before signing.

The quote service is the only process that may need executable-memory support.
That exception is confined to its service unit, a separate UID, a dedicated
socket group, and explicit CPU, memory, swap, task, file-descriptor, and I/O
limits. Never disable the runner's `MemoryDenyWriteExecute` protection.

Install `deploy/systemd/mithril-agent-quote.service` beside the swap unit and
put only `MITHRIL_AGENT_QUOTE_RPC_URL` in the root-owned, mode `0600`
`/etc/mithril-agent/quote.env`. Keep the Mithril and evidence RPC variables in
`/etc/mithril-agent/rpc.env`; the two units hide the other unit's environment
file. The runner uses `Wants=` rather than `Requires=`, so quote-service loss
blocks new swaps without terminating reconciliation of a transaction that may
already have been sent.

Use this ownership layout for the service templates:

```text
/usr/local/libexec/mithril-agent/   root:root, not writable by the service
  nine Go binaries, pinned Node.js, quote.mjs, node_modules/
/etc/mithril-agent/                 root:root
  rpc.env, quote.env, mcp.env, price.env, telegram-operator.env  mode 0600
/var/lib/mithril-agent/             mithril-agent:mithril-agent, mode 0700
  private/devnet-keypair.json       wallet keypair loaded only by systemd for the signer
  agent/                            created by `swap setup`
```

Create `/var/lib/mithril-agent/private` first, put the mode-0600 Devnet wallet
keypair there, then run `swap setup` as the service user with `--dir
/var/lib/mithril-agent/agent`. Generate the runner and per-leg risk-authority,
signer, submitter, operator, recovery, and status units with `service install`,
then run the exact ownership, mode, installation, and verification commands it
prints. The quote unit cannot access any path below
`/var/lib/mithril-agent`; the runner can read its ordinary state but its unit
makes the wallet key, risk-authority key, signer ledger, submitter policy and
key, and submitter control directory inaccessible.
Install the generated units as root, validate them with
`systemd-analyze verify` and `systemd-analyze security`, then start the quote
service before the runner. The runner starts stopped and still requires a
separate, short-lived `swap enable` command.

The default swap profile accepts at most 500 ms of kernel clock uncertainty.
On hosts using `systemd-timesyncd`, install
`deploy/timesyncd/90-mithril-agent.conf` as
`/etc/systemd/timesyncd.conf.d/90-mithril-agent.conf`, restart
`systemd-timesyncd`, and require `mithril-agent clock-check` to pass before
starting the runner. The shorter poll ceiling keeps Linux's conservative
`adjtimex` error bound compatible with that profile; missed synchronization
still makes the runner fail closed.

The Devnet signer and risk authority now have separate service identities and
narrow local IPC, but that does not select a production custody backend. Before
any Mainnet-capable release, isolate the submitter and its durable send barrier
without weakening the stop/grant checks, then qualify the chosen KMS, HSM, MPC,
or bounded canary signer. Authority keys and durable limits must not be readable
or deletable by the runner identity.

The smallest acceptable authority topology is one submitter/control service
with two local sockets. Its service identity alone owns the submitter key,
policy, writable control state, and RPC credentials. The runner may reach only
the runtime socket, whose operations can inspect authority, request one exact
bounded submission, or narrow authority by stopping. A root-operated socket is
the only interface that may enable a bounded action budget or acknowledge a
terminal outcome. Socket ownership and mode, rather than a request field,
separate those capabilities. The runtime protocol must not contain a hidden
enable or acknowledgement operation.

For submission, that service must validate the sealed transaction and exact
action/request/transaction hashes, lock and recheck the control state, record
the recovery marker, and keep the same lock through the actual network send.
Returning the transaction to the runner or releasing the lock before the send
would recreate the race this boundary is intended to remove. An absent,
expired, malformed, mismatched, or pending-recovery state always refuses the
request. Automatic reconciliation may clear a marker only after the service
independently verifies the matching finalized transaction; otherwise clearing
is an operator action. It must never restore expired or exhausted authority.

Do not accept this boundary from unit tests alone. Its release gate includes:

- filesystem and socket tests proving the runner cannot read or replace the
  state, policy, credentials, or key and cannot connect to the operator socket;
- protocol tests proving the runtime socket has no authority-widening method;
- stop-versus-send race tests proving no send begins after a successful stop;
- crash tests before the recovery marker, after the marker but before send,
  during an ambiguous send, and after send but before the response;
- restart tests proving pending recovery and exhausted budgets survive; and
- migration and rollback tests proving deployment starts stopped and refuses
  to replace a state with unresolved recovery.

Newly generated supervised units implement this topology: the runner reaches a
narrow runtime socket, a root-owned `0600` socket handles enable and terminal
acknowledgement, and a keyless timer performs independent finality checks before
recovery clearing. Tests cover the runtime protocol, socket ownership, exact
pre-send evidence, ambiguous send, restart persistence, tampering, divergent
effects, and fail-closed provider migration. This is implemented for the
bounded Devnet profiles only; keep Mainnet signing and submission disabled.

## The audit journal and its segments

Every action writes to an append-only journal whose records are chained by
SHA-256: each record commits to the one before it, so removing or altering
anything downstream is detectable. `mithril-agent journal verify --path PATH`
re-walks the chain and reports the record count and chain head.

A single journal file is capped at 65,536 records, which a continuously
running agent reaches in about six weeks. Runner journals therefore rotate:
when the active file is half full it is sealed as `events.jsonl.seg-000001`
and a fresh active file takes over. Sequence numbers and the hash chain stay
global across segments, so concatenating the segments in order and then the
active file reproduces exactly the stream a single file would have held — and
`journal verify` walks all of them as one chain.

Rotation is deliberately crash-safe and refuses to guess. The successor file
is created and given its rotation marker, chained to the sealed segment's
head, *before* either rename happens; a crash mid-rotation leaves exactly one
recoverable state, which the next open completes. If the active file or a
sealed segment is missing, the journal refuses to open rather than rebuilding
what was lost: silently recreating either would roll history back and unlatch
whatever it recorded, including a halt.

The store checks for rotation after a completed action and before an idle
record such as the once-per-minute clock anchor. It never rotates while an
action capacity reservation is open, so a quiet strategy can run for years
without cutting a segment across an in-flight transaction.

The signer's authorization ledger uses the same segmented chain. Its header
remains the first record in the global history, rotation markers are structural
records rather than authorizations, and every reservation across every segment
is reloaded before a new signature is allowed. Rotation therefore cannot reset
an action ID or a daily cap. Never delete an active ledger or one of its sealed
segments: missing history makes the signer refuse to start.

## Strategy: caps, triggers, alerts, and sweep

One command shows everything the agent is allowed to do and where each
setting can be changed:

    mithril-agent strategy

Alert thresholds are notify-only and editable live — the runner picks up a
change on its next cycle:

    mithril-agent strategy alerts set --price-above 200 --balance-below 0.05
    mithril-agent strategy alerts clear

Alerts message the operator through the deployed Prometheus, Alertmanager and
notifier stack; the interactive Telegram bot stays read-only and rule
configuration only ever happens on the host. A configured alert whose price
or balance evidence stops being usable raises its own warning, so a dead
source cannot silently disable the operator's alerts.

Caps and price triggers live inside the signed profile; change them by
running `setup` again. The sweep destination requires more than that:

    mithril-agent setup sweep --wallet WALLET.json --to YOUR_ADDRESS \
      --primary-trust-domain PRIMARY_PROVIDER_OWNER \
      --secondary-trust-domain SECONDARY_PROVIDER_OWNER

Sweeping sends the agent account's excess balance — above a floor that
protects rent, fees, and any armed trade — to the operator's own wallet on a
schedule, through the same signer, caps, and durable daily ledger as every
other action. The destination must prove itself: setup issues a challenge and
only accepts the destination after its own key has signed it (Solana CLI
`sign-offchain-message` or a wallet's signMessage), which simultaneously
proves the address is real, on-curve, uncorrupted, and the operator's. A
newly proven destination cannot receive funds before the first UTC midnight
at least 24 hours later, and changing it is a full re-setup that lands
stopped until deliberately re-enabled. The signer refuses any other
destination at signing time.

## Production owner decisions

### Read-only Mainnet proposal check

`proposal check` exercises the current Mainnet transaction boundary without a
private key, signature, or submission. It permits one operator-approved
Exact-In route between native SOL and one classic SPL token, in either
direction, using one current Jupiter `route_v2` or `shared_accounts_route_v2`
and the canonical wrap/close instructions only. For SOL input, the checker
requires the taker's canonical output token account to exist before the trade
and rejects runtime creation. For token input, both evidence providers verify
the canonical input account, its mint and owner, and enough balance for the
bounded trade; Jupiter's temporary wrapped-SOL output account must close back
to the protected wallet. Shared program token accounts must be the canonical
accounts of one of Jupiter's 16 derived authorities.
It accepts only Solana v0 messages. The announced larger v1 transaction format
does not change existing v0 behavior, but this agent will keep refusing v1
until its decoder, route validator, signer, submitter, and recovery bindings are
implemented and independently tested. See Solana's
[larger-transaction-size announcement](https://solana.com/upgrades/larger-transaction-sizes).
The separately announced
[200 ms slot upgrade](https://solana.com/upgrades/reduced-slot-times) does not
weaken the existing safety gates: node lag is bounded in slots, while evidence
age and sustained health are independently bounded in seconds. Faster slots
therefore make the slot-gap check stricter in elapsed time; do not loosen it
without new failure-injection evidence.

This boundary deliberately uses Jupiter's advanced
[`/swap/v2/build`](https://developers.jup.ag/docs/swap/build)
route rather than `/order`: `/build` returns the raw instructions and lookup
table contents that the agent must independently constrain, compile, and
simulate. Jupiter currently documents `/build` as Metis-only and explicitly
incompatible with `/execute`; the agent signs through its isolated custody
boundary and sends through Mithril instead. Do not replace it with
Jupiter-managed execution unless the complete policy and evidence model is
redesigned and reviewed.

Run it with a local Mainnet Mithril RPC and two independent Mainnet evidence
RPCs. Jupiter's current
[plan documentation](https://developers.jup.ag/docs/portal/plans) permits
keyless `/swap/v2/build` access at 0.5 requests per second and explicitly lists
AI-agent and test use. That needs no account and is enough for local rehearsal.
For continuous production operation, Jupiter recommends using an API key; its
free plan currently permits one request per second and adds usage monitoring.
Keep `MITHRIL_AGENT_JUPITER_API_KEY` scoped to the read-only quote service. It
is an operational requirement, not a signing gate: a missing, rate-limited, or
unavailable quote still fails closed instead of triggering a trade. The agent
never accepts a configurable Jupiter endpoint and never
forwards a key through a redirect. A Devnet Mithril node will fail the cluster
check by design:

The evidence endpoints must support the standard Solana methods the checker
and recovery path actually use: `getGenesisHash`, finalized `getSlot`,
`getAccountInfo` with `minContextSlot`, `getMinimumBalanceForRentExemption`,
`getFeeForMessage`, `getBlockHeight`, `getSignatureStatuses` with
`searchTransactionHistory`, and finalized base64 `getTransaction` with
`maxSupportedTransactionVersion: 0`. Before contacting Jupiter or creating a
candidate, the checker loads one protected `archive_probe_signature` and
requires both providers to agree on its finality, exact version-0 transaction
bytes, fee, native balances, token balances, and failure result. It also proves
that the signature embedded in those bytes is the configured probe. A
recent-only endpoint therefore cannot pass by returning a convenient new
transaction. This proves the configured endpoints can serve that known-old
record; it does not make either provider authoritative.

The proposal checker also binds Mithril's `getBlockHeight` read to the
retained proposal context with `minContextSlot`. A node that has restarted or
fallen behind that context fails before signing or consuming an action grant.

Choose the probe during provider qualification, not during a trade. Use a
successful or failed finalized Mainnet v0 transaction older than the longest
retention window the recovery design relies on (and older than 48 hours when
screening out short-retention dedicated nodes). Verify it through separate
sources, then store its public signature with the protected provider bindings.
Changing the probe is a protected policy change. The probe contains no wallet
secret, but an untrusted candidate or quote must never be allowed to choose it.

The initial provider pair to qualify is one Helius shared standard RPC and one
QuickNode Mainnet archive RPC. They are separate operators,
[Helius documents](https://www.helius.dev/docs/faqs/rpc) complete history on
its shared standard plans, and
[QuickNode documents](https://www.quicknode.com/docs/solana/api-overview)
unpruned Mainnet archive access. Do not use two accounts or endpoints from one
company as the pair. Do not substitute a Helius dedicated node for its shared
endpoint: Helius documents only about 48 hours of history there. This is an
operator recommendation, not a hard-coded vendor dependency; keep any pair
that passes the live checker, retention drill, independent-ownership review,
rate-limit test, and outage test.

For an account-free preliminary drill, the following public pair and old v0
transaction exercise the checker. This is a rehearsal fixture, not a production
provider recommendation: the Solana Foundation endpoint is rate-limited, while
Lava's public endpoint is shared and routed across providers that may not retain
identical history. The check must fail closed when either origin cannot recover
the fixture. Re-run it before every rehearsal and never copy these public
endpoints into a production policy without the full qualification above.

```sh
export MITHRIL_AGENT_PRIMARY_RPC_URL='https://api.mainnet-beta.solana.com'
export MITHRIL_AGENT_SECONDARY_RPC_URL='https://solana.lava.build'

mithril-agent proposal evidence-check \
  --primary-trust-domain solana-foundation-public \
  --secondary-trust-domain lava-network-public \
  --archive-probe-signature 2eLMRUZzCAhF2KjUeD6JJXpWVeMtPYbqNShFbLeKYSdKLNmAKXs2oUN3u5odBJFeZoTEve4huLHAMw8LUJCXzyD
```

Passing proves only that these two origins agreed on that retained record at
the time of the check. It does not prove sustained independence, availability,
retention, or production capacity.

Qualify the evidence pair before choosing a wallet, asset, amount, or quote:

```sh
mithril-agent proposal evidence-check \
  --primary-trust-domain PRIMARY_PROVIDER_OWNER \
  --secondary-trust-domain SECONDARY_PROVIDER_OWNER \
  --archive-probe-signature KNOWN_OLD_FINALIZED_V0_SIGNATURE
```

This uses only the two protected RPC environment variables. It verifies
Mainnet genesis and exact finalized version-0 history, then prints the two
credential-free origin fingerprints needed by later policy bindings. It prints
no endpoint or probe signature, needs no wallet or provider account, and cannot
sign or send. Public or self-hosted endpoints may be used for preliminary
testing, but only independently operated endpoints that pass retention,
rate-limit, and outage drills qualify for production.

Run `make test-account-free` to exercise the self-hosted custody boundary,
offline matching-policy generator (`make test-free-policy`), and current public
market/Jupiter reads with temporary test identities. It creates no provider
account and submits nothing.

```sh
mithril-agent proposal check \
  --taker PUBLIC_WALLET_ADDRESS \
  --input-mint INPUT_MINT_OR_WRAPPED_SOL \
  --output-mint APPROVED_CLASSIC_SPL_MINT \
  --amount INPUT_BASE_UNITS \
  --minimum-output MINIMUM_OUTPUT_BASE_UNITS \
  --slippage-bps 50 \
  --max-compute-units MAX_COMPUTE_UNITS \
  --max-cu-price MAX_MICRO_LAMPORTS_PER_COMPUTE_UNIT \
  --max-fee-lamports MAX_TOTAL_FEE_LAMPORTS \
  --max-account-rent MAX_TOKEN_ACCOUNT_RENT_LAMPORTS \
  --route-guard-program IMMUTABLE_GUARD_PROGRAM \
  --route-guard-program-data IMMUTABLE_GUARD_PROGRAM_DATA \
  --route-guard-deployment-slot IMMUTABLE_GUARD_DEPLOYMENT_SLOT \
  --route-guard-code-length REVIEWED_GUARD_CODE_LENGTH \
  --route-guard-code-sha256 REVIEWED_GUARD_CODE_SHA256 \
  --primary-trust-domain PRIMARY_PROVIDER_OWNER \
  --secondary-trust-domain SECONDARY_PROVIDER_OWNER \
  --archive-probe-signature KNOWN_OLD_FINALIZED_V0_SIGNATURE \
  --candidate-output /ABSOLUTE/PRIVATE/PATH/jupiter-candidate.json \
  --policy-output /ABSOLUTE/PRIVATE/PATH/jupiter-policy.json \
  --result-output /ABSOLUTE/PRIVATE/PATH/jupiter-check-result.json
```

The checker rejects additional signers, instructions, routes, tips, token
programs, or non-canonical token accounts. It independently checks the current
Jupiter deployment, the protected historical v0 archive probe, and address tables;
sizes the compute limit from a Mithril simulation; obtains the fee for the
exact final message from both evidence providers; bounds the proposal lifetime
to 150 slots; and simulates that exact message again through Mithril.
The reviewed Jupiter deployment and the immutable guard's program,
ProgramData, deployment slot, code length, and code SHA-256 are part of the
policy fingerprint. Updating any deployment pin therefore invalidates existing
grants, control state, action identities, and authorization ledgers and requires
a fresh protected setup.
`--input-mint` defaults to wrapped SOL. Set it to an approved classic SPL mint
and set `--output-mint` to wrapped SOL for the token-to-SOL direction.

Mainnet Jupiter profile v7 makes Jupiter's pinned ProgramData and all ten fixed
`route_v2` accounts or all twelve fixed `shared_accounts_route_v2` accounts
static so a hosted signer can inspect them. For SOL input it binds the
pre-created canonical output account
into the retained request and makes both evidence providers verify its exact
classic-token mint, owner, initialized state, and absence of delegated or close
authority. Current `/build` responses may include a redundant idempotent setup
for that account. Only after the independent account proof passes, the checker
removes that exact canonical setup before compiling the retained message; a
missing account, duplicate setup, different account, or any other setup still
fails closed. For token input it instead verifies the canonical funded input
account and independently caps token spend and native fees. Profile-v1 through
profile-v6 candidates, grants, control
state, action identities, and authorization ledgers are intentionally
incompatible and must not be migrated or replayed.
The amount, output floor, slippage, compute-unit limit, priority-fee price,
final total fee, and token-account rent are operator limits supplied before
the proposal is accepted; Jupiter cannot raise them. The result reports both the
maximum net debit and the larger upfront balance that temporary wrapped-SOL
account creation may require.

The CLI requires distinct operator-declared trust domains, derives the
credential-free origin fingerprint for each provider, and includes both
bindings plus the archive probe in the result. The in-process recheck rejects any provider-origin
change while repeating the genesis, deployment, lookup-table, fee, simulation,
history, rent, and lifetime checks over the exact retained message without
asking Jupiter to rebuild it. Protected authority configuration must persist
those bindings before preparing a signer request; the CLI still grants no
authority.

The optional candidate file contains the exact checked message, policy, quote,
request, lifetime, and canonical lookup-table evidence. It is versioned,
strictly decoded, and suitable for transport to a separate policy authority,
which must recheck it using provider bindings from its own protected
configuration. The candidate never selects its own providers and contains no
signature or key. Its parent directory must already be private and trusted;
the command writes it atomically with private permissions.

The isolated policy-authority and signer protocols share a bounded 1 MiB
request limit. This is large enough for the portable candidate's complete
lookup-table evidence; their responses remain independently capped at 64 KiB.

The policy is deliberately a separate artifact derived from the operator's
explicit wallet, mint, amount, slippage, compute, fee, and rent limits. Transfer
it to the authority through the protected configuration path and verify the
`policy_sha256` from the check report. Never derive authority policy from the
candidate being checked.

The separate authority host or identity rechecks that artifact without calling
Jupiter again. Copy the two origin hashes from the successful check report into
protected operator configuration, then run:

```sh
mithril-agent proposal recheck \
  --candidate /ABSOLUTE/PRIVATE/PATH/jupiter-candidate.json \
  --policy /ABSOLUTE/PRIVATE/PATH/jupiter-policy.json \
  --primary-trust-domain PRIMARY_PROVIDER_OWNER \
  --primary-origin-sha256 PINNED_PRIMARY_ORIGIN_SHA256 \
  --secondary-trust-domain SECONDARY_PROVIDER_OWNER \
  --secondary-origin-sha256 PINNED_SECONDARY_ORIGIN_SHA256 \
  --archive-probe-signature KNOWN_OLD_FINALIZED_V0_SIGNATURE
```

Recheck reads no Jupiter credential and never rebuilds the transaction. It
rejects a changed provider origin and repeats every chain/evidence check over
the exact candidate. Its result remains `checked_not_authorized`; the recheck
command itself neither grants authority nor reaches signing or submission.

Optionally add `--retained-reserve-lamports N` to that recheck for an advisory
native SOL balance check. `N` is a positive decimal integer in lamports
(1 SOL = 1,000,000,000 lamports): the SOL to retain, not the trade amount.
The check requires matching independent balances for the protected wallet
owner, within the checked proposal's evidence-context bounds, sufficient for
both this reserve and the exact checked maximum upfront requirement. That
requirement includes the transaction's native spend, fees and applicable rent;
expected swap proceeds do not count toward it. Token-input funding remains
subject to the existing separate token-account check.

The additional `native_reserve` result is an observation, not a reservation:
other activity can spend that balance after the check. It grants no authority,
does not bypass exact approval or signer limits, and does not enable an
autonomous funded runtime. Omitting the option leaves the ordinary recheck
unchanged; passing it does not establish execution readiness.

Success returns `checked_not_authorized` with
`mainnet_signing_policy_not_configured`. That is the expected terminal state,
not an error and not permission to sign. Do not place a private key in the
command environment; this command does not read one.

Do not hand-write the three matching policy files. After reviewing the route,
provider bindings, schedule, and the four distinct public identities, generate
the protected files offline:

The three non-wallet identities need no vendor account. Run each command on
the separate host that will retain that private key, then carry only the JSON
`public_key` value into policy creation:

```sh
mithril-agent proposal key-create --kind risk-authority \
  --out /ABSOLUTE/PRIVATE/PATH/risk-authority-keypair.json
mithril-agent proposal key-create --kind attestation \
  --out /ABSOLUTE/PRIVATE/PATH/unfunded-attestor.json
mithril-agent proposal key-create --kind submitter \
  --out /ABSOLUTE/PRIVATE/PATH/unfunded-submitter-key.json
```

Each command is offline, refuses an existing path, writes mode `0600`, and
prints no private key. It deliberately does not create the trading wallet:
that source must be the separately reviewed custody identity already bound to
the checked route.

```sh
mithril-agent proposal policy-create \
  --route-policy /ABSOLUTE/PRIVATE/PATH/jupiter-policy.json \
  --check-result /ABSOLUTE/PRIVATE/PATH/jupiter-check-result.json \
  --out /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set \
  --risk-key-id MAINNET_RISK_KEY_LABEL \
  --risk-public-key RISK_AUTHORITY_PUBLIC_KEY_HEX \
  --attestation-public-key ZERO_FUNDS_ATTESTATION_ADDRESS \
  --submitter-public-key SUBMITTER_ENCRYPTION_PUBLIC_KEY_HEX \
  --operator-approver SEPARATE_OPERATOR_WALLET_ADDRESS \
  --recovery-mode stop_only
```

The command needs no provider account or network access. It copies only the
checked provider pins, computes the smallest daily SOL debit cap that can fund
one maximum action including fee and temporary token-account rent, defaults to
one action window per UTC day, and validates the complete cross-policy set
before installing it atomically. It creates no key, grant, control activation,
signature, or transaction. Generate and retain private keys only on their
separate authority, signer, and submitter hosts; pass only their public
identities here.

`stop_only` is the safe default: after an ambiguous crash or interrupted send
boundary, recovery checks evidence but cannot broadcast again. Choose
`exact_retry` only after the operator explicitly approves one resubmission of
the same signed bytes before blockhash expiry. The protected recovery record
durably counts each permitted attempt before its network call: one initial
attempt in `stop_only`, or that attempt plus one retry in `exact_retry`. The
mode is stored in the protected submitter policy, rejects unknown values, and
cannot widen the signed action.

Then independently check that the three generated files still describe one
identical boundary before using any key or RPC:

```sh
mithril-agent proposal policy-check \
  --authority-policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/authority-policy.json \
  --signer-policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/signer-policy.json \
  --submitter-policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/submitter-policy.json
```

This check is offline. It strictly reads private files, validates each policy,
and requires exact agreement on the Jupiter route, source, profile fingerprint,
limits, schedule, authority transaction envelope, provider bindings,
attestation identity, and submitter identity. Success is
`policies_consistent_not_authorized`; both `signing_enabled` and
`submission_enabled` remain false. Repeat it after any policy edit.

Before contacting providers again, verify that the retained candidate and the
whole generated policy directory still form one bundle:

```sh
mithril-agent proposal bundle-check \
  --candidate /ABSOLUTE/PRIVATE/PATH/jupiter-candidate.json \
  --policy-dir /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set
```

This is also offline and reads no key. It detects a candidate copied from a
different route or policy generation before the authority is involved. Success
is `bundle_consistent_not_authorized`; it is not permission to sign or submit.

On the separate authority host, prove that its installed key is the one bound
to the protected policy before preparing any grant:

```sh
mithril-agent proposal authority-check \
  --policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/authority-policy.json \
  --key /ABSOLUTE/PRIVATE/PATH/risk-authority-keypair.json
```

This starts `mithril-agent-policy` in identity-only mode with an empty input
and no RPC environment. It verifies the authority label and public key, prints
neither, and cannot authorize, sign, or submit. Repeat it after key rotation.

The same authority host can then exercise the final keyless proposal step:

```sh
umask 077
mithril-agent proposal prepare \
  --candidate /ABSOLUTE/PRIVATE/PATH/jupiter-candidate.json \
  --authority-policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/authority-policy.json \
  > /ABSOLUTE/PRIVATE/PATH/unsigned-signer-request.json
```

`proposal prepare` repeats the full recheck using provider bindings from that
protected file and prints the exact unsigned request. The request carries those
bindings in its immutable hash, and the submitter's protected policy must match
them exactly. It derives the current schedule window from the protected policy;
`--schedule-start` remains available for reproducible reviews. It reads no
wallet key, produces no risk grant, and cannot sign or submit.

Before granting it, independently decode the exact saved request against the
protected signer policy:

```sh
mithril-agent proposal review \
  --request /ABSOLUTE/PRIVATE/PATH/unsigned-signer-request.json \
  --signer-policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/signer-policy.json
```

The receipt states the direction, wallet, exact mint addresses, base-unit
amounts, maximum native debit, fee, rent allowance, schedule, action ID, and
message hash. It deliberately does not translate arbitrary tokens into symbols
or decimals. Confirm those separately from the reviewed route policy. This
command validates transaction structure and signer limits only; authorization,
signing, and submission remain false.

Approve only that saved request with the separate operator wallet. Without
`--signature`, the command prints one desktop helper command that opens the
existing loopback-only Phantom/Solflare page through SSH. The wallet signs the
displayed text, never a transaction, and the verified signature returns to the
authority host automatically:

```sh
mithril-agent proposal approval-create \
  --request /ABSOLUTE/PRIVATE/PATH/unsigned-signer-request.json \
  --authority-policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/authority-policy.json \
  --out /ABSOLUTE/PRIVATE/PATH/operator-approval.json
```

The output file is private, detached, and bound to the complete request hash,
message hash, provider evidence, amounts, fee, blockhash lifetime, and schedule.
The request is intentionally short-lived. Complete the remaining authority,
signer, and offline submitter steps promptly. If a freshness check refuses it,
prepare and approve a new request instead of weakening the check.
Changing any one of them invalidates it. The approver must be different from
the limited-balance trading wallet.

A separate policy authority can then validate that request against the same
protected bindings and emit the complete granted request without manual JSON
editing:

```sh
umask 077
mithril-agent-policy \
  --policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/authority-policy.json \
  --keypair /ABSOLUTE/PRIVATE/PATH/risk-authority-keypair.json \
  --operator-approval /ABSOLUTE/PRIVATE/PATH/operator-approval.json \
  --granted-request \
  < /ABSOLUTE/PRIVATE/PATH/unsigned-signer-request.json \
  > /ABSOLUTE/PRIVATE/PATH/granted-signer-request.json
```

Without `--granted-request`, the authority retains its existing machine
protocol and prints only the short-lived grant. The signer package can
then pass the canonical transaction through one transaction-only custody
callback, reject any returned message or signer drift, reserve the durable
daily cap, attest under a distinct zero-funds identity, and seal the result for
the submitter. The full debit reservation is made durable while holding the
ledger lock before custody is called. One caller context propagates cancellation
and deadlines through both the custody and attestation callbacks. A request
already canceled before reservation creates no ledger; once reservation begins,
a timeout or malformed return consumes that action's allowance conservatively
because a remote signature may exist. The first provider timestamp is stored in
that durable reservation, so retrying the exact request reuses the same request
hash, timestamp, and transaction while a changed request is refused. The
agent-side risk-authority and signer client boundaries impose a 30-second
operation deadline, while preserving any shorter caller deadline, so a stuck
socket, child process, or remote signer cannot wedge the autonomous loop. The
`turnkeycustody` package adapts that exact request to Turnkey and is available
only as an explicit backend of the bounded signer command described below. It
is mutually exclusive with the self-hosted file-key backend, requires a
protected CLI key file, and exposes no raw-signing operation. Generated
services do not select either Mainnet backend, and live submission remains
Devnet-only.

### Mainnet custody backend and cutover still required

No provider account is required for routine development or for qualifying the
repository's own custody boundary. From a clean checkout, run:

```sh
make test-account-free
make test-free-rehearsal

# Or run one boundary at a time while diagnosing a failure:
make test-free-custody
make test-free-market-data
make test-free-jupiter

# Separate strict public archive-availability drill:
make test-free-evidence
```

The rehearsal target runs the policy and custody/submitter composition checks
with temporary unfunded identities and no network. The aggregate target adds
the current keyless market-data and Jupiter compatibility checks.
The custody target runs
the self-hosted signer, hardened pinned-SSH transport,
exact Jupiter transaction checks, durable cap ledger, response sealing, and offline
submitter/recovery tests under the race detector. It creates only temporary
unfunded test identities and makes no RPC, hosted-custody, or broadcast call.
Passing it proves the local implementation; it cannot prove that an external
provider applies an equivalent policy. The second target reads the sponsored
Pyth SOL/USD and USDC/USD accounts plus
Kraken SOL/USD and USDC/USD public market data. It needs no API key or wallet and
cannot sign or submit; passing proves current reachability and agreement, not
production capacity or an SLA.
The third target forces keyless Jupiter access, uses only a fixed watch address,
builds both directions, and verifies the pinned Mainnet program and IDL. The
repository holds no private key for that address, so the check cannot sign or
submit even if an RPC or quote service misbehaves.
The separately invoked evidence target verifies that two no-signup public origins
currently agree on one retained finalized v0 transaction. It remains strict and
may stop when a shared public origin routes to a node without that history. That
is a successful fail-closed safety result, not a reason to weaken the checker.
It is a preliminary availability and retention drill only; it does not make
those shared endpoints production-ready.
An optional hosted-provider qualification may wait for a free allowance or
for a different provider to survive the retained-transaction mutation suite.
Do not weaken the local policy or enable Mainnet submission merely to avoid a
provider fee.

The smallest self-hosted canary design is a new, dedicated wallet funded only
with the explicitly accepted canary amount. Its signer must run on a separately
administered machine or hardware device, not merely as another process or user
on the Mithril/agent host, and re-decode the exact checked v0 message under the
same route, mint, amount, fee, lifetime, and daily-loss limits before signing.
The runner, MCP, Telegram, and any conversational model remain unable to read
the key or grant authority. Never import an operator's primary wallet into this
setup. If no separate signing host or device is available, keep funded Mainnet
execution disabled; Devnet execution and Mainnet shadow mode still work.

That file-backed wallet is a bounded canary mechanism, not the production
custody default. Solana's production guidance recommends a KMS/HSM or managed
signer for backend automation. The official
[Solana Keychain](https://solana.com/docs/tools/keychain/getting-started/rust)
already supplies AWS KMS and GCP KMS Ed25519 backends behind one signer
interface. Prefer that maintained adapter over new cryptographic integration
code: AWS uses `ECC_NIST_EDWARDS25519` with `ED25519_SHA_512` and a raw message;
GCP uses `EC_SIGN_ED25519`, also over the raw message.

The vendor-account-free separate-host foundation does not require a new network
service. `signerclient.SSHTransport` carries the existing bounded signer
protocol over the installed OpenSSH client. It ignores user and system SSH
configuration, uses a protected dedicated transport key rather than the wallet
key, requires an exact protected known-hosts file, allows only non-interactive
public-key authentication, disables certificates, agents, proxies, forwarding,
and local commands, and pins the wallet, zero-funds attestor, and submitter
public identities before accepting a signed response. The server-side
authorized key must use `restrict` and one absolute forced signer command; a
stable source address may additionally be constrained with `from=`. The client
sends only the fixed command name `mithril-agent-signer-protocol-v1`, so a
server missing its forced command fails closed instead of opening a shell.
OpenSSH documents that `restrict` disables forwarding, PTY allocation, and user
startup files, while `command=` ignores any client-supplied command. Give the
dedicated signer user no password and no other authorized key. Its one
`authorized_keys` entry has this shape (replace every uppercase placeholder):

```text
restrict,command="/usr/local/bin/mithril-agent-signer --policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/signer-policy.json --keypair /ABSOLUTE/PRIVATE/PATH/wallet.json --attestation-keypair /ABSOLUTE/PRIVATE/PATH/attestor.json --socket" ssh-ed25519 TRANSPORT_PUBLIC_KEY
```

See the current [OpenSSH authorized_keys specification](https://man.openbsd.org/OpenBSD-current/man8/sshd.8).
Independently verify the server host-key fingerprint before writing the client
`known-hosts` file; never accept a key learned only from the first connection.

After the operator installs that forced command, verify the complete transport
and identity binding without signing anything:

```sh
mithril-agent proposal self-hosted-check \
  --host SIGNER_HOST --user SIGNER_USER \
  --identity-file /ABSOLUTE/PRIVATE/PATH/transport-key \
  --known-hosts /ABSOLUTE/PRIVATE/PATH/known-hosts \
  --policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/signer-policy.json
```

Success reports `vendor_account_required:false`, `signing_activity:false`, and
`can_submit:false`. The command prints none of the host, paths, identities, or
policy fingerprint. It validates the local protected policy and derives all
four expected pins from that one source, avoiding manual transcription. The
four explicit pin flags remain available for policy-management automation but
cannot be mixed with `--policy`. It proves the pinned OpenSSH identity and policy path; it does not qualify a funded wallet
or enable Mainnet submission.

This is transport infrastructure, not a Mainnet cutover. The generated signer
service and runner configuration remain Devnet-only. The signer executable
accepts a reviewed Jupiter policy only when a separate attestation key is
supplied explicitly. For an unfunded local or isolated-host identity check:

```sh
umask 077
mithril-agent-signer \
  --policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/signer-policy.json \
  --keypair /ABSOLUTE/PRIVATE/PATH/unfunded-wallet.json \
  --attestation-keypair /ABSOLUTE/PRIVATE/PATH/unfunded-attestor.json \
  --identity
```

After both the unfunded bootstrap qualification and the retained-Jupiter
mutation qualification below pass for the exact operational Jupiter policy,
select the transaction-only backend without placing any private value in the
environment or command line:

```sh
mithril-agent-signer \
  --policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/signer-policy.json \
  --turnkey-api-key /ABSOLUTE/PRIVATE/PATH/turnkey.private \
  --turnkey-api-public-key '<REGISTERED_API_PUBLIC_KEY>' \
  --turnkey-organization '<ORGANIZATION_ID>' \
  --turnkey-sign-with '<SOLANA_POLICY_SOURCE_ADDRESS_OR_PRIVATE_KEY_ID>' \
  --attestation-keypair /ABSOLUTE/PRIVATE/PATH/unfunded-attestor.json \
  --identity
```

When `--turnkey-sign-with` is a Solana address, it must exactly equal the
protected policy source. It may instead be a Turnkey private-key ID. Before
loading either form, the command authenticates the API identity and verifies
that the signing resource resolves to the protected source address. It also
derives the API public key from the protected private file and requires it to
equal `--turnkey-api-public-key`. It rejects a partial Turnkey configuration or
simultaneous `--keypair` and Turnkey inputs. The API private key stays in the
protected file.

The command validates the complete policy and prints only the wallet,
attestation, and submitter public identities. Without `--identity`, it first
size-bounds, strictly decodes, and independently validates the exact request
before loading a file key or contacting hosted custody. It then returns one
submitter-encrypted response; it still cannot submit:

```sh
umask 077
mithril-agent-signer \
  --policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/signer-policy.json \
  --keypair /ABSOLUTE/PRIVATE/PATH/unfunded-wallet.json \
  --attestation-keypair /ABSOLUTE/PRIVATE/PATH/unfunded-attestor.json \
  < /ABSOLUTE/PRIVATE/PATH/granted-signer-request.json \
  > /ABSOLUTE/PRIVATE/PATH/sealed-signer-response.json
```

On the separate submitter host, prove that its installed encryption key is the
one bound into the same protected policy set:

```sh
mithril-agent proposal submitter-check \
  --policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/submitter-policy.json \
  --key /ABSOLUTE/PRIVATE/PATH/unfunded-submitter-key.json
```

This starts `mithril-agent-submitter` in identity-only mode with an empty input
and no RPC environment. It requires the returned public key, profile, and
source wallet to match the protected policy, prints none of them, and cannot
sign or submit. Run it after installing or rotating the submitter key.

Run prepare, grant, sign, and submitter preparation as one prompt review: the
grant and blockhash are intentionally short-lived. If either expires before
submitter preparation, discard the three request/response files and begin again
at `proposal prepare`; do not edit or refresh any field by hand. If the
submitter has already persisted the proposal, use the retirement command below
before preparing its replacement. Do not fund a wallet,
install a Mainnet signer service, or add the forced SSH command until the
retained transaction mutation suite and the outage, timeout, retry, recovery,
and audit gates below have passed.

The independent submitter can complete the next offline boundary using the
exact granted request and sealed response saved as private files:

```sh
umask 077
mithril-agent-submitter \
  --policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/submitter-policy.json \
  --key /ABSOLUTE/PRIVATE/PATH/unfunded-submitter-key.json \
  --prepare-mainnet \
  --signer-request /ABSOLUTE/PRIVATE/PATH/granted-signer-request.json \
  --signer-response /ABSOLUTE/PRIVATE/PATH/sealed-signer-response.json
```

Run this qualification only with temporary unfunded identities and a dedicated
empty state directory. Success returns `ok` with the exact action ID and writes
`submission-recovery.json` beside the policy's `control_state_path`. The
operation re-decodes the complete v0 transaction, verifies its signature,
lookup tables, policy, evidence bindings, response attestation, and encrypted
recipient, and persists the exact recovery material under a private
cross-process lock. It does not read an RPC URL, call `sendTransaction`, enable
a service, or grant submission authority. The record explicitly says that
submission has not started; recovery reconciliation refuses it before any
finality-provider call while that marker remains false.
The socket protocol exposes the same `prepare_mainnet` operation for a bounded
client, but no generated Mainnet service or strategy runner uses it.

An expired or operator-rejected proposal that never reached `send_started` can
be retired only while the bound control state is fully stopped:

```sh
mithril-agent-submitter \
  --policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/submitter-policy.json \
  --retire-mainnet
```

This keyless command revalidates the complete recovery record under the control
and recovery locks, refuses any action whose submission started, and moves the
record to a private action-ID-named audit file in the same directory. It returns
only the retired public action ID. The same action cannot be prepared again;
start from a fresh `proposal prepare` result. Retirement never grants authority,
signs, calls an RPC, or submits a transaction.

The control package also contains the next disabled Mainnet admission boundary.
Its `mainnet_canary` state is not interchangeable with `devnet_enabled`: each
gate rejects the other mode. A canary grant is hard-limited to one action and
one hour, and its only writer requires the exact action ID and protected-state
revision that the operator reviewed. Admission consumes the action before entering the
operation callback and leaves the exact action ID as the recovery marker. An
exact recovery retry is possible only when the protected submitter policy says
`exact_retry`; it does not create another action. Missing, expired,
exhausted, changed-mode, or recovery-pending state fails closed.

The existing keyless root-only operator socket can identify its protected
policy, inspect the state revision,
and activate this action-bound one-action canary. It validates the Mainnet policy and uses
the same compare-and-swap protocol as Devnet, so stale operator state is
refused. No generated unit, strategy runner, or operator command invokes this
path, and activation cannot submit a transaction. Do not manually manufacture
a `mainnet_canary` state file. The read-only command below binds the action ID, revision,
complete policy set, independent evidence, and submitter recovery state into a
review receipt; activation remains intentionally absent until the separate
funded-canary decisions and qualifications are complete.

Run the complete read-only receipt on the submitter host:

```sh
mithril-agent proposal canary-check \
  --policy-dir /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set \
  --operator-socket /run/mithril-agent-submitter-operator-mainnet.sock \
  --request /ABSOLUTE/PRIVATE/PATH/unsigned-signer-request.json \
  --operator-approval /ABSOLUTE/PRIVATE/PATH/operator-approval.json \
  --shadow-policy /ABSOLUTE/PRIVATE/PATH/shadow-policy.json \
  --shadow-dir /ABSOLUTE/PRIVATE/PATH/shadow-journals \
  --shadow-days N
```

It requires the configured Mithril RPC and both policy-bound independent
evidence RPCs. It first verifies all three policies, proves that the keyless
operator socket loaded the matching submitter policy, replays the immediately
preceding complete shadow days, and requires their wallet, route, action size,
slippage, and fee assumptions to conservatively match the protected canary.
It also requires a stopped state with no recovery or terminal latch, then runs
the exact prepared-record readiness check below. That check re-verifies the
policy-bound immutable guard program, ProgramData link, deployment slot, code
length, and code SHA-256 through Mithril and both independent providers.
Success reports the public
action ID, approved request hash, control revision, shadow policy fingerprint, and complete-day count
as `mainnet_canary_evidence_ready_not_enabled`. It explicitly reports
`strategy_approved: false`, `production_ready: false`,
`route_upgrade_atomic: true`, and
`route_upgrade_protection: "immutable_guard_exact_code_pinned"`; reads no key;
cannot enable, sign, or submit; and does not approve or judge profitability. Those route
fields are emitted only after the guarded v7 policy, exact candidate, and live
three-source deployment/readiness checks all pass. They do not mean this
repository contains an approved deployment or that Mainnet is production-ready.

#### Guarded v7 deployment boundary

`programs/mithril-route-guard` contains that narrowly scoped guard as an
isolated Rust program. It holds no state, signs for no account, accepts only the
two Jupiter route discriminators already supported by the Go policy, requires
the pinned Jupiter ProgramData account read-only, verifies the exact reviewed
Jupiter deployment, and forwards at most 64 route accounts unchanged. Holding
ProgramData read-only while invoking Jupiter makes an upgrade and the guarded
route mutually exclusive in the same transaction.

The Go `jupiterswap` package wraps the reviewed route before compilation and
unwraps it only after canonical message validation
without mutating the source plan. The guard's own program and ProgramData
identify its code deployment; the account prepended to a trade is Jupiter's
pinned ProgramData, not the guard's ProgramData. `txflow` can require the local
Mithril node and both independent evidence providers to agree that the guard's
deployment is executable, linked to the expected ProgramData, at the pinned
slot, contains the exact reviewed code bytes, and has no remaining upgrade
authority.

No deployment identity is built into the repository. `proposal check` requires
the operator to provide `--route-guard-program`, `--route-guard-program-data`,
`--route-guard-deployment-slot`, `--route-guard-code-length`, and
`--route-guard-code-sha256`. Those values are embedded in the protected route
policy and its fingerprint. Candidate, authority, signer, submitter, recovery,
and canary validation reject a missing guard, a direct Jupiter route, or
identity drift. This prepares the guarded profile but does not deploy,
authorize, sign, enable, or submit anything. Test the source boundary with:

```sh
make test-route-guard
```

Build the SBF artifact only with Agave CLI 4.2 or newer; the target pins the
verified platform-tools version and does not deploy anything:

```sh
make build-route-guard ROUTE_GUARD_OUT=/absolute/private/guard-build
```

The output directory must be outside the checkout with mode `0700` because the
builder emits both the SBF program and its deployment keypair. Preserve that
identity as protected release material; never place it in Git or a shared build
directory. Record the reviewed SBF byte length and SHA-256 before deployment.
After deployment, record the complete on-chain code region that begins after
the upgradeable loader's fixed 45-byte ProgramData metadata area. The production
`code_length` and `code_sha256` describe that deployed region, including any
explicitly allocated trailing bytes; they match the SBF artifact for a current
exact-size deployment that was not extended. The guard verifier reads this
complete deployed region in bounded chunks and requires the same length and
digest from Mithril and both independent providers.

After the authority has been removed and the local Mithril node is ready, use
Solana's read-only native dump command against that node to derive those two
values. The command resolves the program to its ProgramData account and writes
the complete code region after the loader metadata; it does not need a wallet
or submit a transaction:

```sh
install -d -m 0700 /absolute/private/guard-verification
solana program dump \
  --url http://127.0.0.1:YOUR_MITHRIL_RPC_PORT \
  ROUTE_GUARD_PROGRAM_ADDRESS \
  /absolute/private/guard-verification/route-guard-onchain.so
wc -c < /absolute/private/guard-verification/route-guard-onchain.so
sha256sum /absolute/private/guard-verification/route-guard-onchain.so
```

Use the byte count as `--route-guard-code-length` and the digest as
`--route-guard-code-sha256`. Keep the dump outside the checkout, compare it to
the reviewed release artifact, and let `proposal check` independently require
the same complete bytes from Mithril and both external evidence providers.

Before any Mainnet canary, separately review the program, deploy it under a fixed
address, permanently remove its upgrade authority, record its ProgramData,
deployment slot, code length, and code SHA-256, verify that complete identity
through Mithril plus two independent providers, and generate fresh v7 policies
and candidates with those exact values. Mainnet signing and submission remain
disabled until the separate custody, strategy, operator approval, shadow
evidence, and one-action canary decisions are complete.

The independent submitter exposes the lower-level readiness check as a keyless
read-only command:

```sh
mithril-agent-submitter \
  --policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/submitter-policy.json \
  --check-mainnet
```

Run it with the configured Mithril RPC and the two policy-bound independent
evidence RPCs. It re-opens the exact prepared record under its cross-process
lock and repeats the same immutable-candidate check used before signing. With
The operator-signed schedule end is an approval expiry: readiness refuses it
before opening an RPC once that time has passed. With fresh finalized contexts
it checks Mainnet identity, the pinned Jupiter
deployment, the archive witness, current token-account state, lookup tables,
fee and rent bounds, exact-message Mithril simulation, and block-height
lifetime. It then requires both witnesses to return fresh
`isBlockhashValid=true` at or after that new evidence context. It returns only
`ok` and the public action ID. The command rejects a submitter key, and the
readiness package interface exposes no transaction-submission method.

Solana considers the blockhash expired only after that height is exceeded. The
equality rejection above is a deliberate submission-headroom policy: the
submitter will not begin a network round trip in the final valid block.

The keyless `mithril-agent-submitter --recover` command also accepts this
Mainnet policy. It refuses a record whose send has not durably started. After a
future canary attempt, it queries only the two pinned evidence providers and
resolves recovery only when both report finalized matching effects. Finalized
success clears the matching marker. Finalized failure is made durable and
becomes a stopped `failed` action that an operator must review and acknowledge.
Pending, unresolved, or divergent evidence leaves recovery pending. The
command has no submission method.

Before choosing any recovery action, inspect the submitter-owned record without
a key or RPC:

```sh
mithril-agent-submitter \
  --policy /ABSOLUTE/PRIVATE/PATH/mainnet-policy-set/submitter-policy.json \
  --recovery-status
```

This returns only a versioned format, the public action ID, recovery mode,
whether sending started, the durable attempt count and limit, the remaining
attempt budget, and the terminal verdict after reconciliation finalizes the
record. It validates
the protected record under its lock and never prints the transaction,
signature, signer request, attestation, policy, endpoint, or key.

Successful terminal reconciliation upgrades the protected record to the
current format, stores the exact two-provider reconciliation and effects, and
hard-links that complete record to an action-ID-named `finalized` audit file in
the same protected directory. A later proposal cannot reuse that action ID and
cannot replace a finalized active record until the archive is durable. Copy
these archives to the off-host append-only audit destination; never delete one
to make a repeated action eligible again.

This follows Solana's RPC contract: [`sendTransaction`](https://solana.com/docs/rpc/http/sendtransaction)
acknowledges RPC acceptance without guaranteeing processing or confirmation,
and a transaction can still miss before its recent blockhash expires;
[`isBlockhashValid`](https://solana.com/docs/rpc/http/isblockhashvalid) supports
the `minContextSlot` freshness floor used here, as does
[`getBlockHeight`](https://solana.com/docs/rpc/http/getblockheight). The unexported
internal sender boundary repeats every readiness check while holding
the canary and recovery barriers, opens the exact control path and profile
fingerprint from the protected submitter policy, durably sets `send_started`
and the attempt count, and then attempts one exact-byte v0 broadcast. It checks
the approval expiry again inside those barriers, so crossing the boundary
during the preceding network checks cannot reach broadcast. A
transport error remains ambiguous. The default `stop_only` policy cannot create
a second attempt; `exact_retry` permits exactly one resubmission of the same
persisted bytes and never creates a new signature.
No command, socket operation, generated service, or
strategy runner calls this boundary yet.

The installed Devnet sender follows the same binding rule: both senders derive
the control path and profile fingerprint from their protected submitter policy.
A caller cannot authorize either sender with another setup's control file.

The Mainnet signer response is confidential to the submitter: its public
metadata carries the transaction hash and attestation but not the Solana
signature. The signature exists only inside the authenticated encrypted
transaction. The pinned client can verify the request, wallet, hashes,
attestation, and recipient identity; only the submitter can open the envelope,
verify the Ed25519 signature and exact v0 transaction, persist recovery
evidence, and eventually cross the final stop gate. This closes the bypass that
would exist if a runner received both the unsigned message and its signature.

Those raw-message APIs prevent key extraction; they do not independently
understand a Jupiter route, mint, amount, fee, or address lookup. A compromised
workload that holds KMS signing permission can still ask KMS to sign different
bytes. Therefore a cloud KMS is a production boundary only when the exact
transaction decoder and the KMS-authorized identity run together in a
separately protected signer domain that the runner host cannot enter. Putting a
Keychain adapter behind another service on the same compromised host is not
that boundary. Until an enclave, dedicated signer host, or equivalent isolated
workload identity is qualified, use a raw KMS only for a bounded canary or keep
Mainnet execution disabled.

The signer host is currently outside AWS and GCP, so a cloud KMS choice also
requires a deliberately provisioned workload identity and provider audit log.
Do not replace the file key with Vault on the same host and call that a separate
failure domain: a host compromise would still reach both. Use an off-host KMS,
HSM, MPC/managed signer, or keep Mainnet execution disabled. Grant only the
specific signing and public-key/metadata permissions required for the chosen
key; never give the runner provider credentials.

A hosted policy signer is an alternative, not an automatic improvement. It is
acceptable only if its enclave-enforced policy covers arbitrary Solana program
transactions and the exact Jupiter v0 message constraints above. A transfer
allowlist or transfer amount limit alone does not constrain a swap instruction.
The funded wallet must expose a policy-evaluated Solana transaction-signing
operation. Do not enable unrestricted message or raw-payload signing on that
wallet: it could sign transaction message bytes without evaluating the
transaction policy. Instead, configure `attestation_public_key` as a distinct,
zero-funds Ed25519 service identity. That identity authenticates the request
hash, submitter binding, and sealed response metadata; it never holds assets or
signs Solana transactions. The signer and submitter policies reject a Mainnet
configuration that reuses the funded wallet, risk authority, zero-funds
attestor, or sealed-response submitter public identity for another role. The
submitter key is also validated as a usable X25519 recipient during policy
loading, so an invalid delivery key cannot survive until a signing attempt.

This repository contains a provider-neutral transaction-only custody callback,
exact returned-transaction validation, durable cap reservation, separate
response attestation, sealing, independent submitter validation, and a pinned
Turnkey v2 transaction-signing adapter. Both the Turnkey and self-hosted
file-key implementations are callable only through explicit bounded signer
CLI/socket configuration; no generated service chooses either one. The
submitter command can prepare recovery evidence offline, but no Mainnet submit
path is operational. Qualify a hosted adapter against a real
test organization and wallet using
version-0 transactions, address lookup tables, every Jupiter instruction the
policy permits, deterministic idempotency keyed by `request_sha256`, outage and
timeout behavior, audit export, credential rotation, and transaction-only
policy enforcement before connecting that adapter to an operational signer.

The default qualification path is vendor-account-free: run `make test-free-custody`,
then qualify the pinned-SSH signer on a separately administered host with
`proposal self-hosted-check`. This avoids a vendor account, but the operator is
responsible for that host, its backups, and its physical and administrative
separation. Keep Mainnet submission disabled if those conditions cannot be met.

The provider ranking below applies only when the owner explicitly chooses
managed custody instead:

1. Evaluate Turnkey before other hosted candidates. Its current
   [Solana policy examples](https://docs.turnkey.com/features/policies/examples/solana)
   and [IDL policy documentation](https://docs.turnkey.com/features/policies/smart-contract-interfaces)
   cover legacy and v0 transaction signing, instruction and account
   constraints, address-table handling, uploaded IDL arguments (including a
   Jupiter route example), delegated users, multi-party consensus, and deny
   circuit-breakers.
   This is the closest documented match to the existing validator. It is still
   only a candidate: replay retained Jupiter V2 build responses through a test
   wallet and prove that an in-policy message signs while the hosted policy
   rejects an unapproved program, wallet, mint, instruction type, lookup table,
   extra instruction, or value above the input, slippage, and compute-fee caps.
   Do not require the hosted policy to pin one blockhash or one exact valid route:
   that would prevent autonomous quoting. The isolated local signer remains
   responsible for exact route/evidence binding, blockhash lifetime, quote
   threshold, and the full transaction policy. Also qualify idempotency, audit
   export, credential rotation, timeout, and outage behavior.
   Turnkey documents
   [identical activity POST bodies as idempotent](https://docs.turnkey.com/api-reference/activities/overview).
   Once accepted, an activity does not expire, so an exact re-submission returns
   that activity and a known activity ID can be polled. The same API documents
   `timestampMs` as a liveness check: if the first request never reached Turnkey,
   a much later submission of its old body may be refused. That case must remain
   stopped for operator review; it is not permission to create a new timestamp
   or a second signing activity.
   Bind one deterministic activity body, including its timestamp, to the
   agent's `request_sha256`; an exact retry must resubmit that body or poll its
   activity ID, never create a new timestamp after the durable reservation
   begins.
   Use Turnkey's maintained
   [Go v2 SDK](https://github.com/tkhq/go-sdk) rather than a new HTTP stamper or
   Node sidecar. The local adapter pins v2.0.0, fixes the API origin, refuses
   redirects, bounds response size and time, discards SDK logging, and never
   propagates provider error bodies.

### Bootstrap the Turnkey policy with an unfunded wallet

This bootstrap qualifies the real API identity, transaction-signing endpoint,
version-0 decoding, exact retry behavior, and basic policy rejection. It does
not use an RPC, fund the wallet, or broadcast a transaction. It is the first
provider gate, not the later retained-Jupiter/lookup-table qualification.

1. Keep the original passkey user as an administrator only. Turnkey documents
   that root quorum bypasses policies, so an API key attached to a root user
   cannot prove policy enforcement. Create a dedicated **API-only, non-root**
   user named `mithril-agent-qualification`, then generate its key pair with
   Turnkey's maintained CLI:

   ```sh
   turnkey generate api-key --organization "$ORGANIZATION_ID" \
     --key-name mithril-agent-qualification
   ```

   Register the exact `publicKey` printed by that command with the dedicated
   user and record the user's ID. Keep the matching `privateKeyFile` at the path
   printed by the command; do not pair it with a public key from a different
   dashboard activity. Keep the private half only in that `.private` file;
   never paste it into chat, JSON, a shell argument, or this repository. See
   Turnkey's [CLI guide](https://docs.turnkey.com/sdks/cli) for installation
   and higher-assurance installation choices.

   A downloaded create-API-key activity JSON is only a registration receipt. It
   contains the public identity and activity metadata, not the private key
   needed to authenticate that identity.
2. Create a separate Turnkey wallet with an Ed25519 Solana account and leave it
   unfunded. Record the public Solana account address. A wallet UUID is not the
   signing address.
3. As the administrator, create this allow policy after replacing the two
   placeholders. Do not add any broader signing policy for the API-only user:

```json
{
  "policyName": "Mithril agent unfunded qualification",
  "effect": "EFFECT_ALLOW",
  "consensus": "approvers.any(user, user.id == '<API_ONLY_USER_ID>')",
  "condition": "solana.tx.instructions.count() == 1 && solana.tx.address_table_lookups.count() == 0 && solana.tx.program_keys.all(p, p == '11111111111111111111111111111111') && solana.tx.transfers.count() == 1 && solana.tx.transfers[0].from == '<SOLANA_ACCOUNT_ADDRESS>' && solana.tx.transfers[0].to == '6HfHQs4q4hH3tXRPmbyVGYpHq1Zbw3xJY6R1dSfeoyNX' && solana.tx.instructions[0].instruction_data_hex == '020000000100000000000000'",
  "notes": "Unfunded bootstrap only: one exact one-lamport v0 System transfer; never broadcast"
}
```

4. Confirm that the CLI-generated private-key file is a regular file owned by
   the current user and grants no group or other access. First run the
   first-class read-only identity check from the repository root. It
   authenticates the API key and verifies the Solana signing-resource mapping,
   but creates no signing activity and prints none of the supplied identifiers:

```sh
chmod 600 "/ABSOLUTE/PATH/mithril-agent-qualification.private"
./bin/mithril-agent proposal turnkey-check \
  --api-key-file "/ABSOLUTE/PATH/mithril-agent-qualification.private" \
  --api-public-key "<REGISTERED_API_PUBLIC_KEY>" \
  --organization "<ORGANIZATION_ID>" \
  --sign-with "<PRIVATE_KEY_ID_OR_SOLANA_ACCOUNT_ADDRESS>" \
  --expected-address "<SOLANA_ACCOUNT_ADDRESS>"
```

   After that passes and the exact unfunded policy above is installed, run the
   signing qualification. The private key itself never enters the environment:

```sh
MITHRIL_AGENT_TURNKEY_QUALIFY=1 \
MITHRIL_AGENT_TURNKEY_API_PRIVATE_KEY_FILE="/ABSOLUTE/PATH/mithril-agent-qualification.private" \
MITHRIL_AGENT_TURNKEY_API_PUBLIC_KEY="<REGISTERED_API_PUBLIC_KEY>" \
MITHRIL_AGENT_TURNKEY_ORGANIZATION_ID="<ORGANIZATION_ID>" \
MITHRIL_AGENT_TURNKEY_SIGN_WITH="<PRIVATE_KEY_ID_OR_SOLANA_ACCOUNT_ADDRESS>" \
MITHRIL_AGENT_TURNKEY_SOLANA_ADDRESS="<SOLANA_ACCOUNT_ADDRESS>" \
go test -count=1 -run '^TestLiveTurnkeyPolicyQualification$' ./turnkeycustody
```

If Turnkey reports that custody is rate or plan limited, no signed transaction
was returned. HTTP 429 can be a short request window or the account's signature
allowance; check the dashboard and current [Turnkey pricing](https://www.turnkey.com/pricing),
then rerun the same command only after the applicable window or allowance
resets. Do not rotate credentials, broaden the policy, or fund the
qualification wallet to work around the limit.

`MITHRIL_AGENT_TURNKEY_SIGN_WITH` is the Turnkey resource identifier passed to
the signing API. `MITHRIL_AGENT_TURNKEY_SOLANA_ADDRESS` is the public Solana
account used as the transaction fee payer. They may be the same address, but a
Turnkey private-key UUID and a Solana address are not interchangeable.

Before making a request, the test derives the API public key locally and
requires it to equal `MITHRIL_AGENT_TURNKEY_API_PUBLIC_KEY`. This catches a
private file downloaded or copied for a different API-key registration without
printing either key. It then authenticates that API identity against the
configured organization before looking up the Solana signing resource, so a
missing API-key registration is reported separately from a wrong private-key
ID. The test then accepts one exact transaction, verifies the returned Ed25519
signature and unchanged message, repeats the identical Turnkey activity, and
requires amount, recipient, and extra-instruction mutations to return a terminal
provider-side `REJECTED` activity. A generic `FAILED` activity is not accepted as
policy enforcement. A known-good identical activity must still succeed after
every denial, so a provider outage cannot pass as policy enforcement. A root-user
key or an overly broad policy fails the rejection tests. The test contains no
submitter or RPC client, and the wallet must remain unfunded after it passes.

### Qualify the retained Jupiter policy

Do this only after the bootstrap passes. Use the same non-root API identity and
dedicated test wallet, confirm that wallet is unfunded before signing, and
replace the bootstrap policy with a retained-candidate qualification policy.
This first Jupiter policy is deliberately not the funded operational policy: it
proves Turnkey's parser and rejection behavior against one checked transaction
shape while leaving only the recent blockhash refreshable.

Create a protected Jupiter policy file and a protected candidate produced by
`proposal check --candidate-output`. Then generate the exact Turnkey policy
document from those two protected artifacts and the dedicated non-root API
user's ID:

```sh
mithril-agent proposal turnkey-policy \
  --candidate /ABSOLUTE/PRIVATE/PATH/jupiter-candidate.json \
  --policy /ABSOLUTE/PRIVATE/PATH/jupiter-policy.json \
  --api-user '<NON_ROOT_API_USER_ID>' \
  --out /ABSOLUTE/PRIVATE/PATH/turnkey-jupiter-qualification.json
```

This command is offline: it does not contact Turnkey, read a credential,
install a policy, sign, or send. It validates the candidate again and writes a
mode-`0600` JSON policy that pins the fee payer, every instruction and account
flag, raw instruction data, and every lookup-table key and index. It does not
pin the recent blockhash. An administrator reviews and installs that exact JSON
in Turnkey; the non-root API user must not have policy-administration rights.
Do not fund the wallet or reuse this candidate-specific qualification policy as
the eventual operational policy.

The generator follows Turnkey's current
[Solana policy schema](https://docs.turnkey.com/features/policies/language):
transaction-level lookup entries contain `writable_indexes` and
`readonly_indexes`, while each per-instruction lookup entry contains one
`index` and `writable` flag. Lookup-loaded accounts are kept out of the
instruction's static `accounts` list. This distinction is enforced by a local
regression test because mixing the two shapes produces a policy that cannot
qualify the retained transaction.

Retain the candidate, remove all funds from the dedicated test wallet, and
independently confirm its balance is zero before starting this test. The local
Jupiter policy and candidate files must be absolute, regular,
owned by the current user, and mode `0600`; their parent directories must not be
writable by group or other users. Then run:

```sh
MITHRIL_AGENT_TURNKEY_JUPITER_QUALIFY=1 \
MITHRIL_AGENT_TURNKEY_API_PRIVATE_KEY_FILE="/ABSOLUTE/PATH/turnkey.private" \
MITHRIL_AGENT_TURNKEY_API_PUBLIC_KEY="<REGISTERED_API_PUBLIC_KEY>" \
MITHRIL_AGENT_TURNKEY_ORGANIZATION_ID="<ORGANIZATION_ID>" \
MITHRIL_AGENT_TURNKEY_SIGN_WITH="<PRIVATE_KEY_ID_OR_SOLANA_ACCOUNT_ADDRESS>" \
MITHRIL_AGENT_TURNKEY_JUPITER_POLICY_FILE="/ABSOLUTE/PATH/jupiter-policy.json" \
MITHRIL_AGENT_TURNKEY_JUPITER_CANDIDATE_FILE="/ABSOLUTE/PATH/jupiter-candidate.json" \
go test -count=1 -run '^TestLiveTurnkeyJupiterPolicyQualification$' ./turnkeycustody
```

The signing resource must resolve to the protected policy owner. The harness
authenticates that mapping, strictly decodes the candidate and policy, requires
their exact match, verifies the returned signature and unchanged message,
repeats the exact activity, and
requires out-of-policy program, account, output-mint, instruction-type, input,
output, slippage, platform-fee, compute-limit, compute-price, lookup-table,
and extra-instruction variants to return a terminal provider-side refusal. Every variant is a
structurally valid version-zero message, and the known-good identical activity
must still succeed after each refusal. The harness contains no RPC or
submitter and never broadcasts any signed bytes.

For the later generalized operational policy, fetch the IDL from the standard
Anchor IDL account derived from the pinned Jupiter program. Do not use an older
parser-package copy: at the time of this qualification the program-owned
on-chain IDL includes `route_v2` and `shared_accounts_route_v2`, while older
checked-in parser IDLs may not. Before uploading it to Turnkey, verify that the IDL account is
owned by the pinned Jupiter program, has the legacy Anchor
`internal:IdlAccount` discriminator, and that both supported routes have the
expected discriminators, named accounts, and arguments. Record the IDL JSON
hash with the administrative policy change. Turnkey documents accepting Anchor IDLs and
exposing the decoded instruction name, named accounts, and program arguments to
policy conditions. The candidate-specific policy above pins raw instruction
data and therefore does not require this interface.

Constrain the decoded route instruction name, transfer authority,
input/output mints, input and slippage caps, platform fee, allowed program set,
allowed lookup-table shape, and total instruction shape. The compiler keeps all
accounts in the static key list whenever the complete transaction fits Solana's
packet limit. Larger routes fall back to keeping the ten fixed `route_v2`
accounts or twelve fixed `shared_accounts_route_v2` accounts static and bind
each remaining instruction account to its exact table key and writable/read-only index. The live Jupiter test covers whichever shape
the current route requires. This matters because Turnkey documents
lookup-loaded account addresses as the literal `ADDRESS_TABLE_LOOKUP`; a policy
cannot allowlist an actual mint or token account hidden behind that placeholder.
The local signer and two-provider evidence still verify the resolved addresses
and exact route.

Do not claim a hosted compute-unit or compute-price cap merely from raw
`instruction_data_hex`: Turnkey documents equality and slicing for strings, not
numeric decoding of the Compute Budget program's little-endian values. Such a
cap counts as qualified only if a separately reviewed compatible interface is
uploaded and the retained-candidate harness rejects both compute mutations. If
either mutation signs, the hosted policy has failed and must not be connected
to an operational wallet; the local signer still enforces both caps regardless.

The exact allowlist depends on the retained route and the operator's approved
venue set; do not copy a condition from an unrelated route. A Jupiter
deployment-pin change requires fetching and reviewing the new on-chain IDL,
replacing the hosted interface and policy, and repeating this entire
qualification before any wallet is funded. Turnkey's policy must be defense in
depth around the stricter local signer, not a replacement for it.

After this candidate-specific suite passes, replace it with a generalized
operational policy only when the uploaded program interfaces can enforce the
same amount, mint, slippage, program, instruction-shape, and compute-fee limits
without pinning one route or one compute estimate. Repeat the complete mutation
suite against that generalized policy before funding a canary wallet.

Changing the recent blockhash is deliberately not a hosted-policy rejection:
an operational autonomous signer must accept fresh blockhashes. The local signer
binds the selected blockhash and lifetime to the independently checked candidate
and rejects drift before Turnkey is called.

For credential rotation, have an administrator add a replacement API key to the
same non-root policy user with a finite `expirationSeconds`, run both
qualification suites with the replacement, stop the signer, atomically replace
the protected credential file, and repeat the signer identity check before
starting it again. Only then delete the old key and confirm it is absent using
Turnkey's [Get API keys](https://docs.turnkey.com/api-reference/queries/get-api-keys)
and [Delete API keys](https://docs.turnkey.com/api-reference/activities/delete-api-keys)
operations. The signer credential must never be authorized to rotate or delete
credentials itself.

Export the provider-side signing audit independently with Turnkey's
[List activities](https://docs.turnkey.com/api-reference/queries/list-activities)
and reconcile completed `SIGN_TRANSACTION_V2` activities with the local durable
authorization ledger. Keep this administrative reader outside the signer
service; the transaction-only runtime neither lists history nor gains an
administrative API permission.

2. Evaluate Coinbase CDP second. Its current
   [Solana policies](https://docs.cdp.coinbase.com/wallets/security-and-policies/policy-engine/solana-policies)
   and [IDL policy](https://docs.cdp.coinbase.com/wallets/security-and-policies/policy-engine/solana-idl-policies)
   cover program, mint, recipient, SOL/SPL value, network, and IDL-decoded
   instruction constraints, with a fail-secure default. However, its
   documented v0 address criteria inspect static account keys and its IDL
   policy supports fewer data shapes than Turnkey. Do not assume those controls
   cover a real Jupiter route; prove the same policy-bound mutation categories
   first.
3. Use AWS or GCP KMS only behind the separately protected transaction-aware
   signer domain described above. KMS signature success by itself is not policy
   qualification.

Do not connect a hosted adapter to an operational signer or add a generic
raw-signing sidecar before the qualification suite passes. The smallest safe
integration is the one bounded transaction-signing operation that survives the
suite; generic
message signing would create a bypass.

Choose between the dedicated self-hosted canary wallet and a specifically
qualified hosted signer before deploying any Mainnet signer profile. Until
then, the read-only checker is the correct terminal boundary for normal
operator workflows.

The Devnet pilot does not choose these on behalf of an operator:

- two independently funded and rate-limited production evidence RPCs;
- production SLAs for the keyless Pyth-on-Mithril and Kraken market
  evidence paths, or explicitly approved authenticated replacements;
- an external deadman receiver and an off-host append-only audit destination;
- separate signer, submitter, risk-authority, and runner identities;
- whether production should override the generated `stop_only` recovery mode
  and permit `exact_retry` for the same signed bytes;
- the separately approved one-time mechanism that creates the required output
  account (the read-only checker already verifies it);
- the independently audited deployment of `programs/mithril-route-guard` that
  production will use. The repository supplies and tests the narrow guard but
  deliberately ships no default program identity. Build the reviewed SBF,
  deploy it under a protected release process, remove its upgrade authority,
  and record its program, ProgramData, deployment slot, code length, and code
  SHA-256. Guarded v7 binds that exact immutable identity and locks Jupiter's
  pinned ProgramData read-only in the same transaction as the route. An
  off-chain recheck, multisig authority, or source verification alone is not
  atomic execution protection;
- production custody limits and approval-device recovery procedures; and
- the optional conversational model/provider and its operator-paid budget.

Mainnet routes, assets, limits, and authorities remain out of scope until those
decisions have owners and their failure-injection gates pass.

## Verification

```sh
umask 077

go test ./...
go test -race ./...
go vet ./...
test -z "$(gofmt -l .)"

MITHRIL_AGENT_ORCA_LIVE_RPC_URL=https://api.devnet.solana.com \
MITHRIL_AGENT_ORCA_LIVE_OWNER=DEVNET_WALLET_PUBLIC_KEY \
MITHRIL_AGENT_ORCA_LIVE_REQUIRED=1 \
  go test -v ./swapbuilder -run TestLiveOrcaAdapterMatchesIndependentValidator -count=1

MITHRIL_AGENT_LIVE_JUPITER_TEST=1 \
MITHRIL_AGENT_LIVE_JUPITER_TAKER=WATCH_ONLY_MAINNET_WALLET \
MITHRIL_AGENT_LIVE_MAINNET_RPC_URL=https://MAINNET_RPC \
  go test -v ./jupiterquote ./jupiterswap \
    -run 'TestLive(JupiterBuild|PinnedJupiterDeployment|CurrentJupiterRouteShape|CurrentJupiterIDL)' -count=1

make test-prometheus
```

The live Orca and Jupiter tests are read-only. The Jupiter tests fetch one
proposal in each direction, verify the pinned on-chain deployment and its
program-owned Anchor IDL, and require one current proposal to retain the
supported `route_v2` contract; the wallet address is watch-only and no key is
read. No command, generated service, or strategy runner can invoke the
unexported Mainnet sender, and there is no external-RPC submission fallback.
Shadow mode and `proposal check` can read Mainnet, but neither can sign; see
"Shadow mode" below.

## Preserve demonstration evidence

After the terminal result and explicit stop, preserve only sanitized status,
the public transaction signature and configured independent evidence result,
monitoring and Telegram counter snapshots, and SHA-256 hashes of the journal
and status file. An explorer link is optional and is not evidence required by
the system.
Copy those artifacts to the operator-selected off-host audit location. Do not
copy environment, config, or key material.

With the runner stopped, capture one coherent status-and-journal proof:

```sh
sudo -u mithril-agent \
  /usr/local/libexec/mithril-agent/mithril-agent audit snapshot \
  --config /var/lib/mithril-agent/agent/config.json
```

The command is read-only. It refuses an active journal, validates every rotated
segment, requires the bounded status projection to match the configured profile
fingerprint, strategy, and journal counters, and hashes the exact status bytes
before emitting JSON. It preserves whether recovery is pending or a finalized
failure/halt needs acknowledgement, without exposing the action ID. It prints
no paths, addresses, transaction payloads,
configuration, endpoints, or credentials. A valid result proves internal
consistency, not who created the files. Store the complete JSON result in the
off-host append-only audit destination so a later rewrite is detectable.
`journal verify --path PATH` remains available as the lower-level journal-only
check.

## Funding boundary

The two-tier model puts a cap between a Squads reserve Vault and the agent's
dedicated, limited-risk account. The agent operates only within that account.
The cap itself is a Squads v4 spending limit enforced on-chain by the Squads
program, not by the agent.

```bash
mithril-agent funding check --spending-limit ADDRESS --multisig ADDRESS \
  --vault-index N \
  --destination ADDRESS --max-base-units N --period one-time,daily \
  --spender AGENT_ADDRESS --owner OPERATOR_ADDRESS
```

`--multisig` is the Multisig **configuration** account; do not fund it.
`--vault-index` selects the asset-holding Vault PDA, which the command derives
and prints. Native SOL is deposited at that Vault PDA. SPL assets live in a
token account controlled by that Vault PDA.
`--max-base-units` is lamports for native SOL and the selected mint's smallest
unit for an SPL token (for example, micro-USDC for six-decimal USDC).
`--spender` verifies that it is the only key authorized by this spending-limit
account. Adding `--owner` also reads the Multisig config and makes its control
and revocation findings part of the same fail-closed text or JSON verdict.

The command reports the allowance for this spending-limit account and every
way it differs from the operator's expectation. A one-time limit never refills.
Daily, weekly, and monthly limits are rolling 24-hour, 7-day, and 30-day
intervals anchored at the account's last reset; they refill indefinitely. The
displayed current allowance is explicitly a local-clock projection because the
program performs resets lazily. A limit with an empty destination list decodes
perfectly and caps the amount, but lets funds leave for any address, so it is
not an aimed boundary.

The result covers one spending-limit account, not the Vault's total exposure.
Multiple limits may coexist and their allowances add. The amount is also shared
by every spender listed on that spending-limit account; it is not a per-member
allowance. Removing a Multisig member does not remove a separate spending-limit
authorization.

This command only reads. `make check-funding-isolation` is a regression guard
that keeps the `squads` package free of signing and submission imports; it is not
a proof of process isolation. The reserve boundary still holds when an
authorized automation key replenishes the agent through the spending limit,
because Squads enforces the cap on-chain. Whether replenishment is manual or
automated is a separate custody decision.

A spending limit is a funding boundary, not a policy engine. Squads v4's
`spending_limit_use` can only perform `system_program::transfer` and
`transfer_checked` — it cannot call another program. It bounds how much is at
risk; it cannot express "only swap SOL for USDC on this pool". That is what the
signer's own policy is for.

Devnet only for now, deliberately: Squads v4 is the same program on both
clusters, so the boundary can be proved before any real money is involved.

## Shadow mode

Shadow mode answers the question the Devnet demonstration cannot: would this
rule have made money on a real market? It watches live prices and read-only
pool quotes, runs the same deterministic decision pipeline, and writes down the
trade it would have made — without ever being able to make one.

Create the policy with `mithril-agent shadow policy`; its output prints the
correct run command for that policy's cluster and direction. Mainnet supports
two pinned Jupiter paper pairs: SOL/USDC and JUP/USDC. A SOL sell spends wrapped
SOL and receives canonical classic-SPL USDC; its buy reverses those mints. JUP
uses the verified six-decimal classic-SPL JUP mint and starts from a USDC buy
leg.
For Devnet, provide `--pool`, `--input-mint`, and `--output-mint` while creating
the policy; it prints the Orca adapter form instead. The quote provider, pool,
and pair are part of the policy fingerprint. Optional route flags on `shadow
run` can only repeat those values and cannot replace them.

The endpoint comes from `MITHRIL_AGENT_SHADOW_RPC_URL` and is never printed,
logged, or journalled. The default price pair — the sponsored Pyth push
accounts read through an RPC, cross-checked against the matching Kraken
pre-trade best bid/ask — needs no credential on either side. JUP/USDC adds a
separate SOL/USD pair to value native lamport fees; it never values fees at the
JUP price. Kraken is used only for the
operator's own paper evidence; do not assume its terms grant redistribution or
a public derived-data service.

Mainnet accounting has a second, separate evidence pair. USDC proceeds are
labelled as USD only while Pyth's sponsored USDC/USD account read through that
same RPC and Kraken's public timestamped USDC/USD order-book snapshot both remain fresh,
agree within policy, and their complete confidence interval stays between
$0.99 and $1.01. A stale source, disagreement, or depeg makes the tick
`shadow.unobservable`; it cannot trigger a hypothetical trade and is counted in
the report's coverage. Scheduled observations missed while the runner is down
also reduce coverage; the report shows both attempted and expected observations
so an outage cannot look like a fully observed period. The generated policy
pins both source identities and the report repeats the allowed accounting
range. No provider credential is needed.
Devnet does not claim this: devUSDC is a test token, so its results remain a
mechanics proxy rather than dollar P&L evidence.

An unobservable tick includes only a bounded `reason` code such as
`market_price_unavailable`, `market_price_invalid`,
`quote_currency_price_unavailable`, `quote_currency_price_invalid`, or
`quote_currency_outside_policy`. These identify the stage an operator should
check without copying provider errors, endpoints, credentials, or response
payloads into terminal output or the shareable journal. A later `shadow report`
with no observable tick repeats the latest bounded reason.

Shadow policy version 6 additionally models one-time token setup rent as locked,
recoverable native capital and timestamps venue-quote receipt before starting
the settlement delay. Journal version 4 records that evidence. Version 5 and
journal version 3 remain readable for earlier non-SOL runs; version 4 SOL-only
policies and journal version 2 also remain reviewable. Start each new policy
version in a new evidence directory rather than mixing accounting contracts.

`adapters/orca/quote.mjs` serves the trading path and is pinned to Devnet.
Mainnet shadow mode uses the separate keyless Jupiter reader in Go; it does not
widen or reuse the trading adapter. The trading engine therefore cannot be
aimed at Mainnet by changing an adapter option.

`MITHRIL_AGENT_SHADOW_RPC_URL` supplies price reads and accepts plain HTTP only
on a literal loopback IP, so it can read the operator's own verifying node;
anything off-box must be HTTPS. `MITHRIL_AGENT_QUOTE_RPC_URL` supplies the quote
adapter and is HTTPS only.

### Scoring a round trip

`shadow report` scores ONE direction: "was selling at this price good". That is
half the question. The other half — "and could I buy back low enough for the
round trip to clear its own costs" — cannot be answered by running the legs
separately, because the second leg spends exactly what the first produced and
the spread plus two fees comes out of one book.

For live evidence, generate a round trip within each independent UTC trial by
giving both thresholds:

```bash
mithril-agent shadow policy --out POLICY --observe WATCH_ONLY_ADDRESS \
  --sell-at-usd 240 --buy-at-usd 200 --amount 1000000
```

Or create a bounded adaptive paper mandate with no absolute buy or sell price:

```bash
mithril-agent shadow policy --out POLICY --observe WATCH_ONLY_ADDRESS \
  --adaptive --market SOL/USDC --budget-sol 0.25 \
  --drawdown-stop-bps 300
```

Or add the isolated JUP/USDC observer with a USDC trading budget and native fee
reserve:

```bash
mithril-agent shadow policy --out JUP_POLICY --observe WATCH_ONLY_ADDRESS \
  --adaptive --market JUP/USDC --budget-usdc 250 \
  --fee-reserve-sol 0.004 --setup-rent-sol 0.003 \
  --drawdown-stop-bps 300 --fee-lamports 100000
```

The SOL mandate keeps a conservative 0.004 SOL native reserve by default; the
JUP budget is USDC and its SOL reserve is separate. Both reserve 0.003 SOL as
locked setup capital on the first successful Jupiter route, covering the case
where the observed owner still needs its output token account.
`--fee-lamports` is a conservative recurring all-in modeled attempt cap, not a
live fee quote. The first successful JUP buy moves `--setup-rent-sol` from the
liquid reserve into locked capital; it remains in equity and is not reported as
a fee. A refused attempt pays the recurring fee but locks no setup rent. Funded
execution still has to use `proposalcheck` to simulate the built transaction,
calculate its exact fee, inspect its actual account-rent requirements, and
verify blockheight expiry. The drawdown stop pauses new trading for that UTC day; it is not a
guaranteed maximum loss because a delayed quote can cross the boundary. The
paper book and its stop reset at 00:00 UTC. Low-level test scripts may still
use `--amount` for the first lot instead of the mandate aliases.

The adaptive runner uses the same independently validated price evidence,
Jupiter quotes, settlement delay, ledger, fees, and replay checks. Its fixed,
deterministic regime controller selects momentum in a trend, range reversion in
a range, a bounded drawdown exit, or no action during warm-up, cooldown,
excessive volatility, or when the raw signal does not clear the current cost
hurdle. New adaptive policies use schema version 3: the configured maximum
slippage stays a hard fill-refusal boundary instead of being counted as a
certain cost on both legs. The expected signal hurdle still covers both modeled
fees and a margin and expands with observed volatility and adverse quote impact.
It does not guarantee a profitable fill: a later settlement may still move
against the decision, and the paper ledger records that later executable quote.
Version 3 also values each quoted leg with that asset's decimals. Legacy non-SOL
policies whose base and quote decimals differ (currently JTO/USDC) cannot create
new runs, allocations or qualified candidates. Generate a new policy and a
separate evidence lineage; do not edit the old policy or journal. Existing
SOL/JUP/WIF/PYTH policies remain usable.
Version 1 and 2 policies retain their original cost math for exact historical replay
and must be regenerated explicitly to use version 3. The controller rewarms
after a data gap and remains risk-off after a filled drawdown exit. `shadow
backtest` uses the policy directly; do not pass `--buy-at-usd` for
an adaptive policy. Search and Hermes candidate generation may tune only the
fast/slow windows, raise the minimum signal hurdle, or choose a bounded
post-fill cooldown. The operator's research preference filters that safe
candidate universe without changing its fee-aware score or admission gates;
starting inventory and all risk, quote, source, timing, and fee boundaries stay unchanged. `InputAmount`
is the first lot. Each later leg spends only the previous simulated proceeds,
so gains and losses resize the paper lot within one reset-daily UTC run; no
outside funds, leverage, or shorts are introduced. A drawdown risk exit sells
the full simulated base-asset inventory to reduce exposure.

`shadow run` starts from the policy's configured inventory leg, switches sides
only after a fill, and sizes the return leg from what the prior leg actually
received. It never spends more than that leg returned or the shadow book holds.
It still cannot sign or submit anything.

The offline replay below is useful for testing different thresholds against an
already-recorded price series without waiting for another live period:

```bash
mithril-agent shadow backtest --policy PATH --dir PATH \
  --buy-at-usd 200 --spread-bps 250
```

It replays a sell-then-buy-back over the prices the observer actually recorded,
on one set of books, with the same ledger and report the live run uses — so a
round-trip result is directly comparable with a one-directional one and with the
hold benchmark.

**It models the pool, and says so.** Recorded quotes exist only for decisions
the original policy made, not every hypothetical threshold. Changed-threshold
fills are therefore derived from `--spread-bps`. The report states that in its own output and in its JSON
(`"pool_modelled": true`), because a modelled number presented as an observed
one is worse than no backtest — somebody will size a real position on it. Read
the pool's real spread with `swap discover` and set the flag from what you see.

The model is deliberately pessimistic: it always fills worse than the oracle, in
both directions, and a wider spread always fills worse than a narrow one.

Why it is safe to point at mainnet: the `shadow` package declares no signer, no
submitter, and no field that could name a key, so "shadow mode cannot trade" is
not a rule somebody has to keep enforcing. Two tests fail the build if that
stops being true: one parses every file's imports and rejects any signing
package, the other rejects key material appearing anywhere in the package.

How it avoids flattering itself:

- A decision is never scored on the price that produced it. It settles against
  a price observed strictly later, so the delay a real order suffers is
  measured rather than assumed.
- If the settled amount falls below the quote's own minimum, the trade is
  refused — not booked at the floor. A real transaction would have been refused
  too.
- Every fill pays the transaction fee.
- One decision is in flight at a time, matching the real engine.
- Each UTC day is a reset-daily operational canary with its own opening mark
  and same-day hold benchmark. It is forward evidence for that day, not a
  continuous portfolio or a statistically independent profitability trial;
  values from separate days must not be compounded.
- The report states how much of the period was actually observable and how many
  signals could not be acted on, counts scheduled observations missed while the
  runner was down, and leads with a caveat when coverage was poor.
- Mainnet USD figures are emitted only while two independent USDC/USD sources
  keep the quote token inside the policy's recorded peg band.
- Every settled record carries its exact read-only decision and settlement quotes. Replay rejects
  a fill without a mature prior decision or whose quotes, amounts, direction,
  timing, trigger state, or equity cannot be reproduced.

Every report ends by stating that nothing was traded, no wallet signing key was loaded, and
nothing was signed.

Telegram keeps the paper stream deliberately quiet. It pushes newly opened
paper orders, settled fills, risk pauses, strategy lifecycle changes, and one short period
result with P&L versus the opening book and versus holding. Raw signals,
refused or missed attempts, warm-up, and ordinary waiting remain in the
hash-chained journal and the on-demand `/paper` view; they do not interrupt the
operator.

The optional dashboard shows the same bounded paper projection without reading
the journal, strategy files, research output, Telegram configuration, wallet,
or signing services. Its status projection is read-only; the private dashboard
can also write a bounded paper-only configuration request for the separate
root activator to validate. It is served from a private Unix socket and does
not open a TCP port on the host. Install and enable the supplied dashboard
service and socket after the two paper status sockets are available. Add only
the intended SSH login user to `mithril-agent-dashboard`, then reconnect that
SSH session so the new group applies:

```bash
sudo install -m 0644 deploy/sysusers/mithril-agent-dashboard.conf \
  /usr/lib/sysusers.d/mithril-agent-dashboard.conf
sudo systemd-sysusers /usr/lib/sysusers.d/mithril-agent-dashboard.conf
sudo install -o root -g root -m 0644 \
  deploy/systemd/mithril-agent-paper-dashboard.service \
  deploy/systemd/mithril-agent-paper-dashboard.socket \
  /etc/systemd/system/
sudo systemd-analyze verify \
  /etc/systemd/system/mithril-agent-paper-dashboard.service \
  /etc/systemd/system/mithril-agent-paper-dashboard.socket
sudo systemctl daemon-reload
sudo usermod -aG mithril-agent-dashboard ubuntu
sudo systemctl enable --now mithril-agent-paper-dashboard.socket
ssh -N -L 127.0.0.1:8787:/run/mithril-agent-paper-dashboard.sock ubuntu@HOST
```

Open `http://127.0.0.1:8787/` locally. The page labels simulation, stale or
unavailable markets, paper value, current-run P&L, comparison with holding, fills,
signal counts, current and worst drawdown, a bounded gap-aware current-day
paper-versus-holding chart, current strategy, and retained activity. Its plan
dialog can change only paper capital, order bounds, cadence, drawdown limit,
and research preference. Candidate evaluation and selection remain in the
existing immutable paper lifecycle; the browser never signs or submits a trade.

The report is not something you take on trust. It is derived by replaying the
day's hash-chained journal, and it can be recomputed independently at any time:

```bash
mithril-agent shadow report --policy PATH --dir PATH [--day YYYY-MM-DD]
```

That recomputes the day from the record alone and compares the result against
the stored report field by field. A disagreement is shown rather than resolved,
because a disagreement is the finding — and the journal, being hash-chained, is
the side to trust. A clean stop also records the exact report boundary in that
chain, so recomputation never guesses that a partial current day ran until a
future midnight. The day's report covers the whole journal rather than one
process's counters, so a runner that restarts mid-day still reports the whole
recorded period. The first journal record binds the exact policy to that day.
On restart the runner restores its books, completed round-trip direction, next
amount, and any unsettled decision from the verified record. A restart before
the settlement deadline keeps that decision pending; a restart after the
deadline records it as missed on the first fresh observation. A different policy or the older
non-resumable journal format is refused instead of being mixed into the day.

To test researched strategy parameters without restarting the observer, write
and select an immutable paper candidate:

```bash
mithril-agent shadow search --policy PATH --dir PATH \
  --train-day YYYY-MM-DD --validation-day YYYY-MM-DD \
  --candidate-out /absolute/private/candidate.json
mithril-agent shadow select --policy PATH \
  --candidate /absolute/private/candidate.json \
  --pointer /absolute/private/selected-candidate \
  --lifecycle-lock /absolute/private/lifecycle.lock
mithril-agent shadow run --policy PATH --dir /absolute/private/runs \
  --alert-status /absolute/private/status/champion/alerts.json \
  --candidate-pointer /absolute/private/selected-candidate
```

For a fixed policy, selection changes only the searched sell and buy thresholds.
For an adaptive policy, it may change the fast/slow windows, raise the minimum
signal hurdle, or test a post-fill cooldown between one-half and twice the base
value. It never lowers the signal hurdle; starting inventory and every risk/evidence boundary stay fixed. A process with no
journal for the current UTC day loads it at startup; a mid-day restart resumes
the one policy already pinned by today's journal. A running process checks the
pointer after closing the UTC day and before its next observation. The lifecycle lock serializes automated
challenger publication and paper selection. The pointer binds both the candidate file
and policy SHA-256, so replacing the selected artifact is refused. Each policy writes beneath its own SHA-256
directory, so evidence from two candidates cannot be mixed. Missing, malformed,
permissive, or base-policy-mismatched files stop the observer. The candidate is
paper-only, not promotable, and carries no signer, submitter, key, or authority.
The runner stores a private `policy.json` beside that candidate's journals; use
that exact file and fingerprinted directory with `shadow report` or `shadow review`.
For the next generation, pass that saved file as `--policy` and the original
startup policy as `--base-policy`; this keeps every candidate bound to one
immutable route, size, sources, timing, and fee configuration across iterations.

Run the selected champion and challenger as separate paper observers with
separate run roots. The manual command below is a retrospective rolling
comparison only: without a challenger pointer that predates the evidence it
cannot emit a forward qualification.

```bash
mithril-agent shadow challenge --policy PATH \
  --champion-pointer /absolute/private/selected-candidate \
  --challenger /absolute/private/challenger.json \
  --champion-dir /absolute/private/champion-run \
  --challenger-dir /absolute/private/challenger-run \
  --days 7
```

The fixed forward paper canary requires zero missed decisions, sufficient complete round
trips, positive aggregate performance versus holding, a strict majority of
daily wins, at least ten basis points of capital-days advantage, and no worse
maximum daily drawdown. A qualified result still has `authorized: false`,
`promotable: false`, `paper_only: true`, and `pointer_updated: false`. An
operator may use `shadow select`, or the separately confined `shadow auto-select`
timer may preserve the old pointer and select the exact qualified artifact for
the next UTC day. Seven days is an
operational canary, not statistical proof of a profitable strategy; use a much
longer precommitted window before drawing a performance conclusion.

The pinned Hermes research profile can automate only challenger preparation:

```bash
mithril-agent shadow research-mcp --policy /var/lib/mithril-agent-research/policy/policy.json \
  --instruction /run/mithril-hermes-research/instruction.json \
  --research-packet /run/mithril-hermes-research/research-state/validated.json \
  --journal-dir /var/lib/mithril-agent-research/journals \
  --candidate-dir /var/lib/mithril-agent-research/challenger/candidates \
  --challenger-pointer /var/lib/mithril-agent-research/challenger/active.json \
  --champion-pointer /var/lib/mithril-agent-research/champion/active.json \
  --champion-dir /var/lib/mithril-agent-research/runs/champion \
  --challenger-dir /var/lib/mithril-agent-research/runs/challenger \
  --challenge-days 7
```

The `--instruction` and `--research-packet` requirements apply to adaptive
policies; fixed-policy MCP use remains available without either. Its tool input
contains only the exact packet digest and date anchors, never paths, policy
parameters, keys, or grants. A separate private,
canonical operator instruction is required at startup and its SHA-256 digest,
exact cadence, exact adaptive drawdown, and bounded candidate-universe
preference are bound into the candidate and receipt. The full validated packet
is retained so its digest, market, exact parameter diff, and every fold's policy
lineage can be rechecked later. Dollar capital and order-size requests fail
closed because the current replay cannot enforce them across future prices and
fills. The server derives and
requires the full eight-day completed window, runs seven chronological
train/out-of-sample folds, writes a content-addressed paper candidate only when
that admission gate passes, and updates only the
dedicated challenger pointer. The pointer binds `selected_at`, the next UTC day
as `eligible_from`, the fixed challenge duration, and the evaluator version, so
reports created before selection or a changed gate cannot qualify it.
Automated status always evaluates the first configured number of complete UTC
days beginning at `eligible_from`; later days cannot rewrite that decision. A
pending or qualified challenger is retained. After the fixed cutoff, missing or
invalid paired evidence becomes a terminal non-qualification rather than
remaining pending forever, so a later research cycle may rotate safely. If the
operator or paper-only auto-selector copies the exact
qualified artifact into the champion tree and selects that copy, the matching
digest is recognized as paper selection and the next challenger may be prepared without deleting a pointer.
The paper champion tree and both run trees remain read-only to
Hermes, and the Telegram platform does not receive this paper write tool.

Once the observer has run for the period the operator chose in advance, verify
the whole consecutive observation period rather than selecting favourable days:

```bash
mithril-agent shadow review --policy PATH --dir PATH --days N
```

`shadow review` accepts only the immediately preceding `N` complete,
consecutive Mainnet UTC days. It replays every hash-chained journal, requires
at least 95% observable coverage on each day, and summarizes the result against
holding. It deliberately does not decide whether the strategy is profitable
or authorize anything: its result is
`strategy_evidence_complete_not_approved`, requires an operator decision, and
leaves execution disabled.
