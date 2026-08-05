# Mithril Agent

## Quick start — you already run Mithril

This is a component that sits beside your Mithril node. If you have a node
running, this is the whole path:

```sh
make prereqs     # everything this needs, checked at once
make build       # seven binaries into ./bin
make adapter     # the Orca quote adapter (checks your Node version first)

./bin/mithril-agent setup     # guided; press Enter through it
./bin/mithril-agent doctor    # what is missing, and the command that fixes it
./bin/mithril-agent demo      # one bounded Devnet trade, then back to stopped
```

`setup` looks for your node's `config.toml` where you would actually have it —
the working directory, where `mithril config init` writes it, then `~/.mithril`
— and finds the quote adapter in this checkout. It records where it put things,
which is why `doctor` and `demo` need no paths.

**What you need:** Go 1.25.12+, Node 24.18.x with npm 11.16.x (for live quotes
only), and a Mithril node you can point at. `make prereqs` tells you which of
those you are missing rather than letting you find out one failure at a time.

To configure a trade you also need **three RPC endpoints** in the environment:

- `MITHRIL_AGENT_MITHRIL_RPC_URL` — your own Mithril node. Plain http is
  accepted here when the host is loopback, because it is your machine.
- `MITHRIL_AGENT_PRIMARY_RPC_URL` and `MITHRIL_AGENT_SECONDARY_RPC_URL` — two
  https endpoints from **different providers**, so no single provider is the
  only witness to what happened.

They are read from the environment and never written to disk.

Your node also has to have **replayed far enough to see the agent's account**.
It reports what it has actually verified, so a node still catching up will
refuse with "source account was not found by Mithril" — which is the gate
working, not a fault. Compare `mithril_replay_slot` against the cluster head.

And you need Devnet SOL in the agent account. `https://faucet.solana.com` is
the usual source; note the public RPC airdrop is heavily rate-limited and often
returns 429, so the web faucet is the reliable route. Devnet SOL has no value.

**Before installing anything**, these two run on their own and need no wallet,
no server and no account:

```sh
make explain       # what it can and cannot do, in plain English
make walkthrough   # watch the real machinery run, on live prices
```

For a short reviewer walkthrough of the existing Devnet pilot, see
[DEMO.md](DEMO.md). This README remains the complete setup and operations
reference.

Mithril Agent is a self-hosted application layer beside a Mithril full node.
The current pilot can execute one tightly bounded Orca swap on Solana Devnet.
It is intentionally separate from the public Mithril node repository: node
verification and RPC stay in Mithril, while wallet policy, signing, alerts, and
application releases stay here.

The implementation is not a general trading strategy and cannot execute on
mainnet. Shadow mode does read mainnet — it watches a live market and records
what the rule would have done — but it holds no key and has no code path to a
signature, which is the only reason it is allowed to look. The supplied Telegram commands and MCP tools expose read-only status
access and no authority to approve, construct, sign, or submit a transaction.
Do not grant an assistant shell or service-control access to the deployment.

The current demonstration is designed to exercise the autonomous mechanics
for one fixed SOL-to-devUSDC sell or devUSDC-to-SOL buy after an explicit
one-action grant.
It is not a Mainnet flow or a Telegram-controlled bot.

## Current flow

```text
Mithril MCP: node identity, health, wallet balance
                    |
          two healthy observations
                    |
 Pyth + Coinbase -> optional one-shot SOL/USD trigger
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

The Devnet pilot can monitor SOL/USD and start its one allowed sell or buy only
after an operator-set target is reached. It requires fresh, agreeing observations
from Pyth and Coinbase, applies each source's uncertainty conservatively, and
uses integer arithmetic. An ordinary price miss is a waiting state, creates no
journal growth, and consumes no action allowance.

The market rule is checked before signing and again immediately before send.
The sell quote's minimum devUSDC-per-SOL rate must meet its floor; for a buy,
the maximum executable price derived from minimum SOL output must stay at or
below the ceiling. A market observation therefore cannot authorize a worse
pool rate. This is a Devnet test proxy, not proof that devUSDC is worth one US
dollar; a production stablecoin route also needs independent stablecoin/USD
evidence. Only the evidence used for an attempted action is durable. The rule
never rearms itself; another action requires a new explicit one-action grant.

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

Pyth requires authenticated Hermes access from August 18, 2026. The adapter
already uses the upgraded endpoint and requires `MITHRIL_AGENT_PYTH_API_KEY`
whenever a price rule is configured. Keep that bearer credential in the
protected service environment, never in the config, journal, metrics, MCP
output, or command line. An immediate demonstration with no price rule does
not require it.

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
- The risk authority, signer, and submitter are separate processes with separate
  policies. This gives the Devnet pilot fault isolation, not a custody boundary:
  the provided setup runs them under one OS identity. The runner never receives
  raw signed transaction bytes.
- The signer re-decodes the exact message, applies its own daily cap, and
  encrypts the signed transaction for the submitter. The runner relays that
  envelope but never receives the raw signed transaction.
- A hash-chained journal records every boundary. Capacity for terminal records
  is reserved before execution starts.
- Stop state is checked under a send barrier. The submitter independently
  confirms that the durable state still authorizes that exact action before it
  contacts the node. The engine can conservatively retry only the same signed
  bytes after a crash or ambiguous RPC response. The supplied service instead
  stops on startup and does not resubmit after a restart; exact-byte recovery
  requires an explicit deployment choice and separate crash-recovery testing.
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

Go 1.25.12+, Node.js 24.18.x LTS, npm 11.16.x, and Linux are required for live
execution. The minimum Go patch release includes security fixes used by the
agent's network paths. macOS can build and run most tests, but the execution
gate requires Linux kernel clock evidence.

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

npm --prefix ./adapters/orca ci --ignore-scripts
sha256sum ./bin/* > /absolute/private/validation/binaries.sha256
```

After assembling the complete candidate runtime (the seven binaries, pinned
Node.js, `quote.mjs`, and `node_modules`), create one manifest for every file
and symlink target. Keep the manifest outside the runtime tree, verify it before
transport, and run the same check from the transported staging directory:

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

Hash the supplied sysusers, systemd, timesync, and Prometheus files into a
separate deployment-assets manifest. Verify that manifest before installation,
then compare every installed file byte-for-byte with its staged source. A
candidate is not ready for cutover if any manifest check or comparison fails.

```sh
sha256sum \
  deploy/sysusers/mithril-agent-status.conf \
  deploy/systemd/mithril-agent-demo.service \
  deploy/systemd/mithril-agent-quote.service \
  deploy/systemd/mithril-agent-swap.service \
  deploy/systemd/mithril-agent-status.socket \
  deploy/systemd/mithril-agent-status-bridge.service \
  deploy/systemd/mithril-agent-telegram.service \
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
risk authority, signer, and submitter are short-lived children of the runner,
not persistent services. Never roll binaries while an agent service or
transient child is running, and never open old pilot state with new binaries.

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
3. Stop the swap, quote, Telegram, status-socket, and bridge units. Verify each
   unit has `MainPID=0` and that no process remains in any of their control
   groups. The short-lived signer, submitter, and risk-authority children must
   also be absent.
4. Preserve a private rollback bundle containing the old runtime, units,
   configs, environment files, policies, keys, journal, control state, signer
   ledger, Telegram cursor, and recorded ownership and mode metadata. Keep it
   separate from the sanitized audit export.
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
   `systemd-timesyncd`. Require `mithril-agent clock-check` to pass, then
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
export MITHRIL_AGENT_PYTH_API_KEY='required-only-for-a-price-rule'
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
  --primary-trust-domain provider-one \
  --secondary-trust-domain provider-two \
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
After the operator enables exactly one action, a satisfied market rule and
satisfied minimum swap rate allow the normal bounded swap flow to
continue.

For a buy, the wallet must already have its canonical devUSDC token account.
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
  --primary-trust-domain provider-one \
  --secondary-trust-domain provider-two \
  --buy-at-usd 75.00
```

Omit `--buy-at-usd` for an immediate one-action demonstration. A configured
buy target means “buy SOL at or below this price”; it has the same two-source
freshness, executable-rate, one-action, and stopped-by-default controls as the
sell target.

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
passed explicitly. Restart the swap service and rerun preflight afterward; the
service still starts with new actions disabled.

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

Use a dedicated full-node or RPC host, not a voting validator. Build the seven
Go binaries and install the pinned Orca dependencies as described above. The
commands below are for a new host only; they are not an upgrade or rolling
replacement procedure. An upgrade may reuse the later setup and validation
requirements, but must use the staged cutover above for runtime files.

```sh
sudo install -m 0644 deploy/sysusers/mithril-agent-status.conf \
  /usr/lib/sysusers.d/mithril-agent-status.conf
sudo systemd-sysusers /usr/lib/sysusers.d/mithril-agent-status.conf

sudo install -d -o root -g root -m 0755 /usr/local/libexec/mithril-agent
sudo install -d -o root -g root -m 0755 /usr/local/share/doc/mithril-agent
sudo install -o root -g root -m 0644 README.md DEMO.md \
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
sudo install -o root -g root -m 0755 /absolute/pinned/node \
  /usr/local/libexec/mithril-agent/node
sudo install -o root -g root -m 0644 adapters/orca/quote.mjs \
  /usr/local/libexec/mithril-agent/quote.mjs
sudo install -d -o root -g root -m 0755 \
  /usr/local/libexec/mithril-agent/node_modules
sudo cp -a adapters/orca/node_modules/. \
  /usr/local/libexec/mithril-agent/node_modules/
sudo chown -R root:root /usr/local/libexec/mithril-agent
sudo chmod -R go-w /usr/local/libexec/mithril-agent

sudo install -d -o root -g root -m 0700 /etc/mithril-agent
sudo install -d -o mithril-agent -g mithril-agent -m 0700 \
  /var/lib/mithril-agent /var/lib/mithril-agent/private
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
variable. `price.env` contains only the Pyth key and may be empty when no price
rule is configured. `mcp.env` contains only nonsecret Mithril observation-path
overrides. Put the Telegram variables documented below only in
`telegram-operator.env`.

Install the Devnet keypair at
`/var/lib/mithril-agent/private/devnet-keypair.json`, owned by
`mithril-agent:mithril-agent` with mode `0600`. Run the discovery and setup
commands from the Configuration section as the `mithril-agent` identity,
loading the protected environments through the service manager. The setup
directory must be `/var/lib/mithril-agent/agent`, and the supervised setup must
use `/usr/local/libexec/mithril-agent/node`, the installed `quote.mjs`, and
`--quote-socket /run/mithril-agent-quote/quote.sock`. For example, prefix the
documented `swap discover` or `swap setup` arguments with `systemd-run`.
For the supervised immediate-sell pilot, these are the complete command shapes;
replace only the confirmed minimum and any intentionally reviewed limits:

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
  --primary-trust-domain provider-one \
  --secondary-trust-domain provider-two
```

One setup pins one direction. Create separate isolated setup and state paths
for buy and sell; do not edit one setup back and forth or run two runners over
the same state.

Install and validate the supplied units and clock policy only after setup is
complete:

```sh
sudo install -m 0644 deploy/systemd/mithril-agent-quote.service /etc/systemd/system/
sudo install -m 0644 deploy/systemd/mithril-agent-swap.service /etc/systemd/system/
sudo install -m 0644 deploy/systemd/mithril-agent-demo.service /etc/systemd/system/
sudo install -m 0644 deploy/systemd/mithril-agent-status.socket /etc/systemd/system/
sudo install -m 0644 deploy/systemd/mithril-agent-status-bridge.service /etc/systemd/system/
sudo install -m 0644 deploy/systemd/mithril-agent-telegram.service /etc/systemd/system/
sudo install -d -m 0755 /etc/systemd/timesyncd.conf.d
sudo install -m 0644 deploy/timesyncd/90-mithril-agent.conf \
  /etc/systemd/timesyncd.conf.d/90-mithril-agent.conf
sudo systemd-analyze verify \
  /etc/systemd/system/mithril-agent-quote.service \
  /etc/systemd/system/mithril-agent-swap.service \
  /etc/systemd/system/mithril-agent-demo.service \
  /etc/systemd/system/mithril-agent-status.socket \
  /etc/systemd/system/mithril-agent-status-bridge.service \
  /etc/systemd/system/mithril-agent-telegram.service
sudo systemctl daemon-reload
sudo systemctl restart systemd-timesyncd
sudo systemctl enable --now mithril-agent-status.socket
sudo systemctl enable --now mithril-agent-quote.service
sudo systemctl enable --now mithril-agent-swap.service
sudo systemctl enable --now mithril-agent-telegram.service
```

The swap service always starts with new actions disabled. Accept the
installation only after `clock-check`, preflight, the read-only live check,
the quote and runner services, the status socket, Prometheus target, notifier,
and Telegram canary all pass. Do not grant an action merely because the
services are active.

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

The unit records progress, repeats the live gate, grants exactly one bounded
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

The single action is consumed before the send boundary, so control
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
socket. Reconnect the login session after adding the group:

```sh
sudo usermod -aG mithril-agent-status "$USER"
/usr/local/libexec/mithril-agent/mithril-agent status \
  --status-socket /run/mithril-agent-status.sock
```

Configure an MCP client to start the same binary as a managed stdio
subprocess:

```text
command: /usr/local/libexec/mithril-agent/mithril-agent
args:    mcp --status-socket /run/mithril-agent-status.sock
```

Do not launch the MCP command separately and wait for a prompt; waiting for
stdin is normal MCP behavior. The socket mode cannot read the private config,
journal, RPC environments, or wallet. `--config` remains available for a local
developer who already owns those private files; it is not the normal client
setup.

The server exposes three standard read-only tools:

- `mithril_agent_info` — active profile and disabled authority boundaries;
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

## Alerts

`deploy/prometheus/mithril-agent.rules.yml` alerts on runner loss or quote
unavailability observed by the runner, stale cycles, attention-required
results, invalid control state, enabled
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
the node monitor:

```json
[
  {
    "targets": ["127.0.0.1:9191"],
    "labels": {"deployment_id": "replace-at-deploy"}
  }
]
```

A sweep runner is a second process with its own metrics: run it with
`--metrics-address 127.0.0.1:9192` and add `127.0.0.1:9192` to the same
targets list, or the sweep's balance gauges, alert slots, and destination
registration are invisible to every rule above.

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

The deadman and alert receiver must run outside the agent process. Use
`deploy/systemd/mithril-agent-swap.service` for the bounded swap runner. It
forces stopped state before every start and after every stop, drains before a
normal shutdown, runs preflight, and requires a fresh operator enable after
restart. That conservative template also prevents exact-byte resubmission after
a process crash; changing that restart policy requires an explicit deployment
decision and a Linux crash-recovery test.

The top-level `devnet-*` commands are the retained but unsupported transfer
pilot. They have no production service template. New Orca deployments must use
the `swap` commands and `mithril-agent-swap.service`.

For that unit, create the setup at `/var/lib/mithril-agent/agent` as the
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

The service identity therefore needs only narrow filesystem evidence access.
Grant traverse permission on the configured accounts, snapshot, and shredstore
ancestry so MCP can inspect filesystem capacity; grant list and traverse on the
configured log directory so divergence artifact names can be checked; and
grant read access only to `mithril_state.json` and `replay_timings.jsonl`. Do
not grant the agent read access to the raw node log or the AccountsDB. For
example, adapt these paths to the active node deployment and apply the same
policy whenever a new run directory is created:

```sh
sudo setfacl -m u:mithril-agent:--x NODE_STORAGE_ANCESTOR
sudo setfacl -m u:mithril-agent:r-x NODE_ACCOUNTS_DIRECTORY
sudo setfacl -m u:mithril-agent:r-- NODE_ACCOUNTS_DIRECTORY/mithril_state.json
sudo setfacl -m u:mithril-agent:r-x NODE_LOG_DIRECTORY
sudo setfacl -m u:mithril-agent:r-- NODE_LOG_DIRECTORY/replay_timings.jsonl
```

Mithril replaces its state file atomically. If the node runs with a restrictive
umask, reapply the state-file ACL after a state save or publish that one file
through a dedicated monitoring group. Preflight reports `mcp_inputs=failed`
when the configured state input is absent or unreadable.

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
  seven Go binaries, pinned Node.js, quote.mjs, node_modules/
/etc/mithril-agent/                 root:root
  rpc.env, quote.env, mcp.env, price.env, telegram-operator.env  mode 0600
/var/lib/mithril-agent/             mithril-agent:mithril-agent, mode 0700
  private/devnet-keypair.json       wallet keypair used only by the signer
  agent/                            created by `swap setup`
```

Create `/var/lib/mithril-agent/private` first, put the mode-0600 Devnet wallet
keypair there, then run `swap setup` as the service user with `--dir
/var/lib/mithril-agent/agent`. The quote unit cannot access any path below
`/var/lib/mithril-agent`; only the runner and its protected child processes can
read that state. Install both unit files as root, validate them with
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

Before any mainnet-capable release, replace the same-user child-process pilot
with separate service identities and narrow authenticated IPC. The signing key,
signer authorization ledger, risk key, and submitter key must not be readable or
deletable by the runner identity.

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

    mithril-agent setup sweep --wallet WALLET.json --to YOUR_ADDRESS

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

The Devnet pilot does not choose these on behalf of an operator:

- two independently funded and rate-limited production evidence RPCs;
- authenticated Pyth access and independent stablecoin/USD evidence for a
  dollar-denominated trading rule;
- an external deadman receiver and an off-host append-only audit destination;
- separate signer, submitter, risk-authority, and runner identities;
- whether crash recovery may resubmit exact signed bytes or must stay stopped;
- pre-created output accounts and the production mechanism that removes the
  route-deployment upgrade race;
- approval-device transaction decoding and production custody limits; and
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

promtool check rules deploy/prometheus/mithril-agent.rules.yml
promtool test rules deploy/prometheus/mithril-agent.rules.test.yml
```

The live Orca test is read-only. The repository contains no mainnet submission
path and no external-RPC submission fallback. The one component that reads
mainnet is shadow mode, which cannot sign; see "Shadow mode" below.

## Preserve demonstration evidence

After the terminal result and explicit stop, preserve only sanitized status,
the public transaction signature and explorer confirmation, monitoring and
Telegram counter snapshots, and SHA-256 hashes of the journal and status file.
Copy those artifacts to the operator-selected off-host audit location. Do not
copy environment, config, or key material.

With the runner stopped, verify the complete journal before copying its hashes:

```sh
sudo -u mithril-agent \
  /usr/local/libexec/mithril-agent/mithril-agent journal verify \
  --path /var/lib/mithril-agent/agent/state/events.jsonl

sudo -u mithril-agent sha256sum \
  /var/lib/mithril-agent/agent/state/events.jsonl.status.json
```

The journal command is read-only and prints only the format, record and byte
counts, send-boundary counts, final chain hash, and exact-file SHA-256. The
second command hashes the exact bounded status projection. A valid journal
result proves internal chain consistency, not who created the journal. Anchor
the journal file hash, chain head, and status-file hash in the off-host audit
destination so a later rewrite is detectable.

## Funding boundary

The two-tier model puts a cap between the operator's own wallet and the agent's
account: the main wallet funds a dedicated, limited-risk account, and the agent
operates only within that account. The cap itself is a Squads v4 spending limit,
which is enforced on-chain by a program this software does not control.

```bash
mithril-agent funding check --spending-limit ADDRESS --multisig ADDRESS \
  --destination ADDRESS --max-lamports N --period one-time,daily
```

It reports the worst case — the most that can ever leave the vault — and every
way the on-chain limit differs from what the operator believes they configured.
The check most worth having is the aimed-ness one: a spending limit with an
empty destination list decodes perfectly and caps the amount, but lets funds
leave for any address at all, so it is not a boundary.

This command only reads, and `make check-funding-isolation` proves the package
cannot do anything else. That is deliberate. A funding boundary is worth having
precisely because it is enforced somewhere this software cannot reach; if this
software could move funds through it, the boundary would only be as trustworthy
as this software, which defeats the point. Moving funds through it is a human
action taken in Squads.

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

```bash
mithril-agent shadow run --policy PATH --dir PATH \
  --node-command PATH --quote-script PATH --pool ADDRESS --input-mint ADDRESS
```

The endpoint comes from `MITHRIL_AGENT_SHADOW_RPC_URL` and is never printed,
logged, or journalled. The default price pair — the sponsored Pyth push
accounts read through an RPC, cross-checked against Coinbase — needs no
credential on either side.

There are two quote adapters. `adapters/orca/quote.mjs` serves the real trading
path and is pinned to Devnet; `adapters/orca-mainnet/quote.mjs` is a read-only
fork used only by shadow mode. That is a deliberate duplication: the trading
adapter's inability to quote mainnet means the trading engine cannot be aimed
there even by misconfiguration, and widening it would quietly delete that
property to save a file.

`MITHRIL_AGENT_SHADOW_RPC_URL` supplies price reads and accepts plain HTTP on
loopback, so it can read the operator's own verifying node; anything off-box
must be HTTPS. `MITHRIL_AGENT_QUOTE_RPC_URL` supplies the quote adapter and is
HTTPS only.

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
- Each UTC day is an independent trial with its own opening mark and its own
  hold benchmark, so a run of daily reports is a walk-forward, not a backtest.
- The report states how much of the period was actually observable and how many
  signals could not be acted on, and leads with a caveat when coverage was poor.

Every report ends by stating that nothing was traded, no key was loaded, and
nothing was signed.

The report is not something you take on trust. It is derived by replaying the
day's hash-chained journal, and it can be recomputed independently at any time:

```bash
mithril-agent shadow report --policy PATH --dir PATH [--day YYYY-MM-DD]
```

That recomputes the day from the record alone and compares the result against
the stored report field by field. A disagreement is shown rather than resolved,
because a disagreement is the finding — and the journal, being hash-chained, is
the side to trust. The day's report covers the whole journal rather than one
process's counters, so a runner that restarts mid-day still reports the whole
day instead of silently understating it.
