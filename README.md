# Mithril Agent

Mithril Agent is an application layer for a Mithril Solana node. It can inspect,
index, decode, build, and simulate program activity without a browser wallet or
block explorer. Optional execution is a separate Devnet-only profile with
deterministic policy, isolated signing, durable recovery, and operator controls.

The default path is read only. It gives an operator or MCP client useful Solana
program tools without granting transaction authority.

## What it does

| Profile | Capabilities | Current status |
| --- | --- | --- |
| Observe and index | Read current program state, ingest rooted history, query accounts and transactions, decode pinned data | Implemented for native Alpenglow rooted events and the separately labelled classic finalized feed |
| Build and simulate | Pin a reviewed Solana IDL or supported Codama interface, build deterministic unsigned calls, simulate through Mithril | Implemented without loading a signing key |
| Execute | Apply policy, sign through an isolated service, submit through Mithril, reconcile with independent evidence | Bounded Devnet pilot only |

The walletless path includes:

- a private workspace bound to one program, cluster, Mithril node, and set of
  local indexes;
- content-addressed program interfaces and reviewed evidence;
- current processed account reads and unsigned simulations through Mithril;
- restart-safe rooted account and transaction indexes;
- local decoding of accounts, instructions, and program events; and
- bounded stdio MCP tools for the same program and index operations.

The optional Devnet pilot adds guided strategy setup, spending and action caps,
separate policy, signer, and submitter processes, recovery journals, Telegram
status, and Prometheus metrics. It is not a general trading platform and does
not enable funded Mainnet execution.

## Choose the right guide

| Task | Guide |
| --- | --- |
| Understand the direction and remaining work | [ROADMAP.md](ROADMAP.md) |
| Set up the default walletless program workflow | [WALLETLESS_QUICKSTART.md](WALLETLESS_QUICKSTART.md) |
| Backfill, follow, query, or recover a rooted index | [INDEXING.md](INDEXING.md) |
| Install the optional Devnet trading pilot on Linux | [QUICKSTART.md](QUICKSTART.md) |
| Review an installed trading pilot | [DEMO.md](DEMO.md) |
| Operate, monitor, upgrade, or recover the trading pilot | [OPERATIONS.md](OPERATIONS.md) |
| Read the expanded capability inventory | [OVERVIEW.md](OVERVIEW.md) |

Use one path at a time. In particular, do not combine the generated strategy
services from `QUICKSTART.md` with the retained legacy single-trade examples in
`deploy/systemd`.

## Default walletless flow

Start with a verified checkout:

```sh
git clone https://github.com/NeerajGodiyal/mithril-agent.git
cd mithril-agent
make prereqs
make verify-source
make test-walletless
make test-rooted-contract MITHRIL_SOURCE=/absolute/path/to/Mithril
```

The cross-repository contract test requires the matching Mithril source. The
exact node prerequisites and current review artifact are documented in
[WALLETLESS_QUICKSTART.md](WALLETLESS_QUICKSTART.md) and
[ROADMAP.md](ROADMAP.md).

The working sequence is:

1. Create a private workspace for one program and cluster.
2. Fetch or pin one reviewed program interface.
3. Ingest Mithril's rooted account and transaction feeds into separate local
   indexes.
4. Run the index and workspace doctors.
5. Decode data, read current state, or build and simulate an unsigned call.
6. Optionally expose the same bounded tools to a local MCP client.

The complete commands, evidence labels, restart rules, and recovery procedure
are in the walletless and indexing guides. Stop at the first failed check. Do
not edit an index cursor, workspace identity, or evidence record to make a
doctor pass.

## MCP and AI clients

Mithril Agent exposes local stdio MCP servers for two purposes:

- `program mcp` provides interface summaries, unsigned construction,
  simulation, current account reads, and local decoding for one pinned
  workspace.
- `index mcp` provides bounded metadata queries over one verified rooted index.

Generate a paste-ready client entry instead of assembling it by hand:

```sh
mithril-agent program mcp-config \
  --workspace "$WORKSPACE" --sha256 "$IDL_SHA256" --name my-program

mithril-agent index mcp-config \
  --dir "$STATE_INDEX" --name my-program-state
```

Any MCP client that supports local stdio can launch these commands. Codex and
Claude Code registration examples, plus the authenticated SSH form for a client
on another machine, are in
[WALLETLESS_QUICKSTART.md](WALLETLESS_QUICKSTART.md#2-pin-the-exact-interface)
and [INDEXING.md](INDEXING.md#local-mcp-queries).

The MCP process uses the permissions of its OS account. It does not open a
network listener, fetch or replace an interface pin, write an index, sign, or
submit a transaction. An AI client may explain evidence or propose an intent;
it is not the policy or signing boundary.

## Optional Devnet execution

The execution pilot demonstrates one reviewed SOL/devUSDC route on Devnet. A
complete strategy may sell, buy back, and sweep excess SOL while an explicit
grant remains valid. Independent limits cover time, action count, daily debit,
fees, slippage, price conditions, and the balance that must remain in the
dedicated account.

Start with the offline review commands:

```sh
make explain
make walkthrough
make test-free-rehearsal
```

For a real installation, follow [QUICKSTART.md](QUICKSTART.md) from top to
bottom. It covers the complete runtime, restricted service identities,
protected environments, the dedicated Devnet account, optional Telegram
alerts, generated services, one-time buy-leg bootstrap, read-only acceptance,
and audit capture. Use [DEMO.md](DEMO.md) after an operator has prepared the
host. Use [OPERATIONS.md](OPERATIONS.md) for failures, upgrades, monitoring,
provider changes, or recovery.

The execution boundary is deliberately separate from the user interface:

- Telegram and MCP are read only.
- A language model cannot grant, sign, or submit an action.
- Deterministic policy checks the exact transaction and spending bounds.
- Short-lived signer and submitter services own their private state.
- Submission uses the local Mithril RPC only.
- Two independent evidence providers reconcile finality and exact effects.
- Every service restart returns execution to stopped mode.

Mainnet shadow and proposal commands are evaluation tools. Shadow mode has no
signing path. Proposal tooling can check and package a narrow transaction, but
the shipped runtime does not enable Mainnet signing or submission. Detailed
policy, custody, canary, and recovery boundaries belong in
[OPERATIONS.md](OPERATIONS.md), not this README.

## Architecture and repository split

```text
Mithril replay and RPC
        |
        +--> processed reads and simulation
        |
        +--> rooted event feed --> private indexes
                                  |
                    pinned program workflows --> local CLI or stdio MCP
                                  |
                          optional policy and execution
                                  |
                     journals, metrics, and operator status
```

The public [Mithril repository](https://github.com/NeerajGodiyal/mithril) owns
node correctness, replay, processed-bank RPC evidence, rooted publication, and
node monitoring primitives.

This repository owns program workspaces, interfaces, indexes, deterministic
builders, optional strategy and execution policy, recovery, Telegram UX,
agent-facing status, and deployment helpers. Trading, custody, messaging, and
agent orchestration stay here rather than moving into the public node.

The focused node prerequisites and their merge order are tracked in
[ROADMAP.md](ROADMAP.md#node-prerequisites). The old all-in-one node integration
branch is comparison material, not an installation target.

## Milestones

| Milestone | Status |
| --- | --- |
| Agent-side walletless workflows, private indexes, program tools, and local MCP | Published in Mithril Agent v0.1.0 |
| Bounded Devnet strategy, recovery, Telegram, and operator tooling | Implemented as the optional execution pilot |
| Focused Mithril node prerequisites | In review and landing in dependency order |
| Matched node and agent walletless acceptance | Required again for the final published node revisions |
| Alpenglow steady-state acceptance | Requires a longer soak of the exact published candidate |
| Funded Mainnet execution | Deferred until custody, immutable route deployment, canary, and recovery policies receive separate approval |

See [ROADMAP.md](ROADMAP.md) for the implementation order, evidence required to
close each milestone, and the current list of limits.

## Current limits

- Funded Mainnet execution is disabled.
- Solana version-1 transactions are not decoded or signed.
- The live execution pilot supports one reviewed Devnet route, not arbitrary
  assets or venues.
- The local rooted index is designed for private bounded queries, not public
  multi-user serving.
- MCP and Telegram cannot approve, sign, or submit an action.
- A passing Devnet run proves mechanics and fail-closed behavior, not
  profitability or Mainnet readiness.
- Native Alpenglow evidence and classic finalized evidence are kept separate.
  This release treats Solana Mainnet as the classic finalized path.
- Every new node or agent revision must repeat the cross-repository contract
  test and guarded live acceptance appropriate to that revision.

See [ROADMAP.md](ROADMAP.md#limits) for the current project-level list and
[OPERATIONS.md](OPERATIONS.md#production-owner-decisions) for the optional
execution decisions that remain outside this release.

## Verification

For the default walletless path:

```sh
make prereqs
make verify-source
make test-walletless
make test-rooted-contract MITHRIL_SOURCE=/absolute/path/to/Mithril
```

For the optional trading runtime:

```sh
make prereqs-trading
make verify-source
make test
make build
make adapter
```

`make test` runs formatting, vet, tests, the race detector, isolation checks,
and private-file checks. Live acceptance remains separate because it needs an
explicit node, cluster, and operator-approved environment.

## Releases and change history

Use [GitHub Releases](https://github.com/NeerajGodiyal/mithril-agent/releases)
for the human summary of each published version. Use the repository's commit
history or a GitHub comparison against the previous release for the exact file
and line changes. A feature branch that has already been fast-forwarded into
`main` has no remaining diff against `main`; compare the release tags instead.

## Using this repository with an AI assistant

Ask the assistant to read this README and [ROADMAP.md](ROADMAP.md), choose one
workflow from the guide table, and read only the matching detailed guide before
running commands. It should stop at the first failed check and must not change a
live node, service, strategy grant, signing boundary, or index history without
the operator's explicit approval.

Host paths, RPC endpoints, environment contents, credentials, private workspace
contents, transaction payloads, and Telegram messages should stay out of chat
and source control. Bounded status, doctor reports, public chain identities,
hashes, and sanitized acceptance records are the intended review surfaces.

## Documentation rule

Keep this README as the entry point: product scope, supported workflows,
architecture, limits, and links. Put installation in `QUICKSTART.md`, walletless
setup in `WALLETLESS_QUICKSTART.md`, index operation and recovery in
`INDEXING.md`, installed-pilot review in `DEMO.md`, and detailed execution,
security, monitoring, upgrade, and recovery material in `OPERATIONS.md`.
