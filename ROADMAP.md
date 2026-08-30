# Mithril Agent roadmap

## Goal

Mithril Agent lets an operator inspect and interact with Solana programs through
a Mithril node without depending on a browser wallet or block explorer.

The default path is read only:

1. ingest Mithril's rooted event feed;
2. build a local, restart-safe index;
3. pin a reviewed program interface;
4. decode accounts, transactions, and program events; and
5. build and simulate unsigned calls.

State-changing execution is optional. It keeps policy, signing, submission,
recovery, and operator messaging outside the language model.

## Capability profiles

| Profile | Wallet or key | Chain mutation | Current status |
| --- | --- | --- | --- |
| Observe and index | None | No | Implemented for native Alpenglow rooted events and the separately labelled classic finalized feed |
| Build and simulate | None | No | Implemented for pinned Solana IDL and supported Codama interfaces |
| Execute | Isolated signer | Yes | Bounded Devnet pilot only; funded Mainnet execution remains disabled |

"Walletless" means the read, index, build, and simulation paths do not load a
signing key. Solana still requires the fee payer and instruction authorities to
sign any transaction that changes state, including a fee payer signature.

## Repository ownership

Public Mithril owns replay correctness, processed-bank RPC evidence, the rooted
event feed, and node monitoring primitives.

Mithril Agent owns local indexes, program interfaces, deterministic builders,
simulation workflows, policy, optional execution, Telegram UX, and local stdio
MCP tools.

Trading, custody, Telegram, and agent orchestration stay in this repository.
They should not move into the public node merely because they consume Mithril
RPC or MCP.

## What is implemented

- Rooted event feed ingestion with source-bound transaction and account data;
- a Custom indexer with restart-safe, exact-cursor resume rules;
- local read-only MCP for index and program queries;
- canonical Program Metadata interface discovery and content-addressed pinning;
- reviewed program evidence bound to genesis, processed bank, and deployment;
- deterministic unsigned version-0 construction and Mithril simulation;
- decoded current accounts, rooted account history, and program events;
- bounded Devnet strategy, signer, submitter, recovery, Telegram, and metrics;
- keyless Mainnet shadow observation with policy-bound route and price evidence;
- offline Mainnet proposal checks that stop before authorization or submission.

The walletless isolation tests reject dependencies on signer, submission,
trading, Telegram, and policy packages. The route-guard suite separately checks
the on-chain safety program.

## Node prerequisites

The agent consumes public Mithril work in this order:

1. `feature/mcp` for local node inspection;
2. `koro/rpc` for transaction and verification evidence;
3. the focused Alpenglow replay and durable-rooting work; and
4. the rooted RPC and event-feed contract.

The old all-in-one node integration branch is a comparison and test source. It
should not be merged after the focused branches because it contains older copies
of the same work.

## Limits

- Funded Mainnet stays disabled.
- Rooted Solana v1 transaction ingestion and indexing are supported; signing
  and execution remain limited to the reviewed legacy and v0 paths.
- The live trading pilot covers one reviewed Devnet route, not arbitrary assets.
- MCP and Telegram are read only and cannot approve, sign, or submit actions.
- The local index is designed for bounded private queries, not public multi-user
  serving.
- A later producer or consumer revision must repeat the cross-repository
  contract and guarded live acceptance tests.
- Classic Mainnet acceptance still depends on adequate AccountsDB capacity.
- Native Alpenglow and classic finalized evidence remain separately labelled.

## Next milestones

1. Land the focused public Mithril prerequisites in dependency order.
2. Repeat walletless and rooted-contract acceptance against those published
   revisions.
3. Run a longer Alpenglow steady-state soak for the exact published candidate.
4. Keep funded Mainnet execution disabled until custody, canary, deployment, and
   reconciliation policies receive a separate review.

Use [WALLETLESS_QUICKSTART.md](WALLETLESS_QUICKSTART.md) for the default path,
[INDEXING.md](INDEXING.md) for rooted indexing, and [OPERATIONS.md](OPERATIONS.md)
for the optional trading and recovery procedures.
