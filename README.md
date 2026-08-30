# Mithril Agent

Mithril Agent is an application layer for a Mithril Solana node. Its default
path is walletless: inspect, index, decode, build, and simulate program activity
without a browser wallet, block explorer, or signing key. Optional execution is
a separate bounded Devnet profile. Mainnet strategy work remains paper-only.

The language model is not the security boundary. Deterministic policy, isolated
services, private filesystem permissions, and explicit operator approval decide
what may change. An agent can research or explain; it cannot bypass those gates.

## What it does

| Profile | Capabilities | Current status |
| --- | --- | --- |
| Observe and index | Read current state, ingest rooted history, query accounts and transactions, decode pinned data | Implemented for native Alpenglow and separately labelled classic finalized feeds |
| Build and simulate | Pin a reviewed Solana IDL or supported Codama interface, build unsigned calls, simulate through Mithril | Implemented without a signing key |
| Paper evaluate | Observe Mainnet prices, model or record hypothetical round trips, compare candidates, alert through Telegram | Implemented with no wallet, signing, or submission path |
| Execute | Apply policy, sign through an isolated service, submit through Mithril, reconcile independent evidence | Bounded Devnet pilot only |

The walletless path provides a private workspace bound to one program, cluster,
Mithril node, and local indexes. It supports content-addressed interfaces,
processed reads, unsigned simulation, restart-safe rooted account and transaction
indexes, local decoding, and bounded stdio MCP tools.

The Devnet pilot adds guided strategy setup, spending and action caps, separate
policy/signer/submitter processes, recovery journals, Telegram status, and
Prometheus metrics. It is not a general trading platform.

## Choose the right guide

| Task | Guide |
| --- | --- |
| Direction, branch prerequisites, and remaining work | [ROADMAP.md](ROADMAP.md) |
| Default walletless program workflow | [WALLETLESS_QUICKSTART.md](WALLETLESS_QUICKSTART.md) |
| Rooted index backfill, follow, query, and recovery | [INDEXING.md](INDEXING.md) |
| Optional Devnet pilot installation | [QUICKSTART.md](QUICKSTART.md) |
| Installed-pilot review or demonstration | [DEMO.md](DEMO.md) |
| Detailed operation, paper evaluation, monitoring, and recovery | [OPERATIONS.md](OPERATIONS.md) |
| Expanded capability inventory | [OVERVIEW.md](OVERVIEW.md) |

Use one path at a time. Do not combine the generated strategy services from
`QUICKSTART.md` with the retained legacy single-trade examples in `deploy/systemd`.

## Default walletless flow

Start from a verified checkout:

```sh
git clone https://github.com/NeerajGodiyal/mithril-agent.git
cd mithril-agent
make prereqs
make verify-source
make test-walletless
make test-rooted-contract MITHRIL_SOURCE=/absolute/path/to/Mithril
```

Then:

1. Create a private workspace for one program and cluster.
2. Fetch or pin one reviewed program interface.
3. Ingest Mithril rooted account and transaction feeds into separate indexes.
4. Run the index and workspace doctors.
5. Decode data, read current state, or build and simulate an unsigned call.
6. Optionally expose the same bounded tools to a local MCP client.

The rooted feed and private index decode and identity-check rooted Solana v1
transactions. The executable paths remain limited to the reviewed legacy and
v0 formats. Do not edit a cursor, identity, or evidence record to make a doctor
pass; stop and preserve the failed evidence.

## MCP and agent clients

The repository has distinct local stdio MCP surfaces:

- `program mcp` summarizes a pinned interface, builds unsigned instructions,
  simulates, reads current accounts, and decodes local data;
- `index mcp` performs bounded metadata queries over one verified rooted index;
- agent status MCP reports bounded runtime state; and
- `shadow research-mcp` exposes two paper-only tools. One reads challenge
  status; the other may create a content-addressed challenger and update only
  its dedicated challenger pointer.

The paper write tool cannot change the champion, live policy, signer, submitter,
or operator control state. It is not exposed to the Telegram toolset.

Generate client configuration instead of assembling paths by hand:

```sh
mithril-agent program mcp-config \
  --workspace "$WORKSPACE" --sha256 "$IDL_SHA256" --name my-program
mithril-agent index mcp-config \
  --dir "$STATE_INDEX" --name my-program-state
```

Any compatible client can launch those commands. The process has only its OS
account's permissions; it does not gain transaction authority by speaking MCP.

## Mainnet paper evaluation

Paper mode answers a narrower question before real execution is considered:
“What would this fixed or adaptive strategy have done on current market
evidence after spread, slippage, fees, stale data, downtime, and a
settlement-time re-quote?”

For example, a policy can hypothetically sell 1 SOL at or above a threshold,
wait, re-quote the same venue, and later buy back using only the simulated USDC
actually received. The ledger charges both fees and reports the result against
holding. Telegram interrupts the operator only for a settled fill, risk pause,
strategy lifecycle change, or daily result; signals, refused attempts, and
ordinary waiting remain available in the journal and compact `/paper` status.
No SOL or USDC is needed because nothing is signed, submitted, or placed on an
exchange.

Each UTC day is a reset-daily operational canary with its own opening mark.
Separate days are not a continuous portfolio or independent statistical trials,
and their values must not be compounded. Backtests validate the original
journal with strict replay before applying a hypothetical candidate.

An adaptive policy has no absolute entry prices. Its deterministic, regime-aware
controller maintains rolling fast and slow market baselines, measures return
volatility and drawdown, and chooses momentum, range-reversion, risk-exit, or
no-trade. It rewarms after a data gap, recomputes the fee hurdle as paper sizing
changes, and latches risk-off after a drawdown exit for the rest of the run.
Every decision and raw signal is journaled and recomputed during replay. This is
a paper heuristic, not a profitability claim. `InputAmount` is the initial lot;
later legs spend only the preceding simulated proceeds, so paper profits or
losses change the next lot within that UTC day. The learner cannot inject funds,
add leverage, short, or change fees, sources, quote limits, cooldown,
volatility, or drawdown boundaries. A risk exit may sell the full simulated
base-asset inventory because that reduces exposure rather than increasing it.

Candidate changes are deliberate and bounded. `shadow search` can write an
immutable candidate, `shadow select` can stage it, and a running observer adopts
it only at a UTC boundary. `shadow challenge` compares preselected champion and
challenger runs. For adaptive policies the chronological train/validation search
may change only the fast/slow windows or raise the minimum signal hurdle. A
separate deterministic timer may select an exact challenger only after its fixed
seven-day forward paper gate passes; it preserves the previous champion for
rollback and still cannot authorize real trading. This is controlled parameter
learning, not unrestricted model-weight self-training.

The pinned Nous Hermes profile in [`deploy/hermes-research`](deploy/hermes-research)
can search official sources every six hours, submit a cited typed hypothesis,
and prepare only the next paper challenger after its evidence gates open. Its
three explicit MCP servers expose 7 allowlisted tools; terminal, files, code execution, delegation, browser
control, wallets, signing, and submission are absent. Research prose is
untrusted provenance attached after the deterministic parameter search; it
cannot change the selected parameters or act as a trading command. Hermes' own memory/skill
self-improvement is procedural assistance; it is not treated as market-return
learning and never receives champion-selection authority.

## Optional Devnet execution

The execution pilot demonstrates one reviewed SOL/devUSDC route on Devnet. A
complete strategy may sell, buy back, and sweep excess SOL while an explicit
grant remains valid. Independent limits cover time, actions, daily debit, fees,
slippage, price conditions, and the balance that must remain.

Start with account-free checks:

```sh
make explain
make walkthrough
make test-free-rehearsal
make test-free-custody
make test-account-free
```

The rehearsal uses temporary, unfunded test identities and makes no broadcast.
No operator wallet or custody-provider or messaging account is required.

For installation, follow [QUICKSTART.md](QUICKSTART.md) completely. It installs
all eight binaries, restricted service identities, protected environments, the
dedicated Devnet account, optional Telegram alerts, and generated services. A
typical generated-runner path is explicit:

```sh
mithril-agent service install \
  --output /var/lib/mithril-agent/.mithril-agent/mithril-agent-run.service
```

Telegram and the execution-status MCP remain read-only. A language model cannot
grant, sign, submit, enable, or stop execution. Every service restart returns
execution to stopped mode.

## Mainnet proposal boundary

Mainnet proposal commands are evaluation and packaging tools, not a live send
path. The current flow can check a narrow Jupiter Exact-In v0 transaction,
create matching protected policies, review exact human-readable intent, and
verify detached operator approval while remaining unauthorized.

Relevant gates include `proposal approval-create`,
`mithril-agent proposal canary-check`, and
`mithril-agent proposal turnkey-check`. The canary repeats
Mithril plus two-provider evidence and still cannot enable, sign, or submit.
The Turnkey command validates an explicitly configured transaction-only mapping;
generated services do not select it. Funded Mainnet submission remains disabled.

## Architecture and repository split

```text
Mithril replay and RPC
        |
        +--> processed reads and simulation
        |
        +--> rooted event feed --> private indexes
                                  |
                    pinned program workflows --> CLI or stdio MCP
                                  |
              paper evaluation or optional bounded Devnet execution
                                  |
                     journals, metrics, and operator status
```

The public [Mithril repository](https://github.com/NeerajGodiyal/mithril) owns
node correctness, replay, RPC/evidence primitives, rooted publication, and node
monitoring. This repository owns workspaces, interfaces, indexes, builders,
paper evaluation, execution policy, custody adapters, recovery, Telegram UX,
agent status, and deployment helpers.

The focused public-node prerequisites and their merge order are in
[ROADMAP.md](ROADMAP.md#node-prerequisites). The old all-in-one integration
branch is comparison material, not an installation or merge target.

## Milestones

| Milestone | Status |
| --- | --- |
| Agent-side walletless workflows, private indexes, program tools, and local MCP | Published in Mithril Agent v0.1.0 |
| Rooted Solana v1 ingestion and identity validation | Implemented on the focused agent branch after the public producer |
| Reset-daily Mainnet paper observer, research challenger, and Telegram alerts | Implemented; still paper-only |
| Bounded Devnet strategy, recovery, Telegram, and operator tooling | Implemented as the optional execution pilot |
| Focused public Mithril prerequisites | Reviewed and stacked in dependency order; publication and matched acceptance remain |
| Funded Mainnet execution | Disabled pending custody, venue, immutable route, canary, and recovery approval |

## Current limits

- Funded Mainnet signing and submission are disabled.
- Solana v1 is indexed, not signed or executed.
- The live execution pilot supports one reviewed Devnet route, not arbitrary
  assets, venues, leverage, or perpetuals.
- Paper results are evidence about a strategy and data path, not proof of a
  profitable strategy.
- Dynamic paper candidate selection applies at UTC boundaries. Protected live
  execution configuration is not arbitrary hot-reload state.
- The local rooted index is for private bounded queries, not public multi-user
  serving.
- Native Alpenglow and classic finalized evidence remain separately labelled.
- A new node or agent revision must repeat the cross-repository contract and
  the live acceptance appropriate to that revision.

## Verification

Default walletless path:

```sh
make prereqs
make verify-source
make test-walletless
make test-rooted-contract MITHRIL_SOURCE=/absolute/path/to/Mithril
```

Complete optional runtime:

```sh
make prereqs-trading
make verify-source
make test
make build
make adapter
```

`make test` runs formatting, vet, unit/integration tests, the race detector,
isolation checks, and private-file checks. Live checks are separate because
they require explicit public inputs and an operator-approved environment.

## Give this project to another AI assistant

Tell the assistant to read this README and [ROADMAP.md](ROADMAP.md), choose one
workflow from the guide table, and then read only the matching detailed guide.
It should stop at the first failed check and preserve existing evidence.

It must not print or copy RPC URLs, environment contents, credentials, wallet or
key material, raw transactions, private workspace contents, or Telegram message
contents. It must not sign, submit, enable a strategy, restart a node, change a
live service, or mutate index history without the operator's explicit approval.

## Releases and documentation rule

Use [GitHub Releases](https://github.com/NeerajGodiyal/mithril-agent/releases)
for published summaries and Git history for exact changes. A merged feature
branch has no remaining diff against `main`; compare release tags instead.

Keep this README as the entry point. Put walletless setup in
`WALLETLESS_QUICKSTART.md`, index operation in `INDEXING.md`, installation in
`QUICKSTART.md`, installed-pilot review in `DEMO.md`, and detailed security,
paper, monitoring, upgrade, and recovery material in `OPERATIONS.md`.
