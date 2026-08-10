# Mithril Agent overview

Mithril Agent is a bounded automation layer for a Mithril Solana full node.
It lets an operator describe a small trading strategy once, checks the node and
the proposed action, signs through a restricted local process, submits through
Mithril, records what happened, and reports readable status through Telegram,
MCP, and Prometheus.

The current end-to-end pilot is deliberately **Devnet only**. It demonstrates
the complete operating and safety flow; it is not a claim that Mainnet trading
is ready.

## What currently works

- Guided setup saves one strategy instead of requiring separate configuration
  for every service.
- A dedicated, limited-balance Devnet account can sell SOL for devUSDC, buy SOL
  back, and sweep excess SOL to an operator-controlled wallet.
- Sell and buy legs can wait for configured price conditions.
- Time limits, action limits, daily spending caps, balance reserves, and a stop
  command bound unattended operation.
- Signing and submission run separately from strategy and messaging processes.
- Transactions are simulated and submitted through the local Mithril node,
  then checked against independent read providers.
- Telegram provides understandable alerts and read-only status commands.
- MCP exposes bounded node and agent status to compatible AI clients without
  giving an LLM signing authority.
- Hash-chained journals and Prometheus metrics preserve operational evidence
  and make failures visible.
- Keyless Mainnet shadow mode records hypothetical decisions for evaluation;
  it cannot sign or submit transactions.

## The flow in one line

```text
saved strategy -> price/health checks -> bounded authorization -> isolated signer
-> Mithril simulation and submission -> independent confirmation -> journal and alerts
```

The language model or agent framework may help explain status or suggest an
intent. It is not the security boundary: deterministic policy, signer limits,
and the expiring operator grant decide whether an action is allowed.

## Where the work lives

The project is split on purpose.

### 1. Mithril node integration

Repository: <https://github.com/NeerajGodiyal/mithril>

The review candidate is
[`koro/agent-node-integration-wip`](https://github.com/NeerajGodiyal/mithril/tree/koro/agent-node-integration-wip)
at commit `94718096a9d8ab02e38725a94a253ff105c0ed89`. It combines these focused
branches:

- [`feature/mcp`](https://github.com/NeerajGodiyal/mithril/tree/feature/mcp) —
  MCP diagnostics, operator controls, approvals, and audit support;
- [`koro/rpc`](https://github.com/NeerajGodiyal/mithril/tree/koro/rpc) — the
  transaction and verification RPC support used by the agent; and
- [`koro/node-monitoring`](https://github.com/NeerajGodiyal/mithril/tree/koro/node-monitoring) —
  node monitoring, alerts, and notifier support.

The integration branch exists so the complete pilot can be run and reviewed
together. The focused branches remain the easier units for code review.

### 2. Mithril Agent

Repository: <https://github.com/NeerajGodiyal/mithril-agent>

This repository contains guided setup, strategy evaluation, bounded authority,
wallet and signer isolation, submission and confirmation, journals, Telegram,
MCP-facing status, Prometheus integration, and demonstration tooling.

Agent frameworks are replaceable clients. Codex, Claude, Hermes, OpenClaw, or
another MCP-capable client can use the same read-only surfaces; none needs to
be built into the signer.

## What this does not claim yet

- No Mainnet signing or autonomous Mainnet execution is enabled.
- The demonstrated live route is one fixed Devnet SOL/devUSDC route, not an
  arbitrary-token trading system.
- Telegram and MCP cannot approve or initiate trades.
- An LLM cannot bypass policy or directly access the signing key.
- Devnet proves mechanics and safety behavior, not trading profitability.
- A production venue, token allowlist, strategy, custody model, and Mainnet
  canary limits still require explicit decisions and separate evidence.

## How to review or run it

Choose the path that matches what you are doing:

1. **Understand the project:** read this file, then
   [`DEMO.md`](DEMO.md).
2. **Review an already installed pilot:** follow the reviewer path in
   [`DEMO.md`](DEMO.md). It includes read-only checks and a bounded Devnet
   demonstration.
3. **Install on a fresh Linux host:** follow [`QUICKSTART.md`](QUICKSTART.md)
   from top to bottom. Do not combine it with older single-trade service
   examples.
4. **Operate or recover the system:** use [`README.md`](README.md) for the
   detailed security, service, journal, monitoring, and recovery reference.

A fresh installation requires a running Mithril full/RPC node near the Devnet
tip, the exact Mithril integration revision listed above, and the complete
Mithril Agent runtime. A non-technical reviewer should not install services or
edit protected configuration: an operator performs the one-time setup, then
the reviewer uses the read-only status, Telegram, MCP, and bounded demo paths.

Start with:

```sh
git clone https://github.com/NeerajGodiyal/mithril-agent.git
cd mithril-agent
make prereqs
make test
```

Then follow [`QUICKSTART.md`](QUICKSTART.md). Stop at the first failed check;
the setup is designed to refuse incomplete or stale deployments rather than
continue with weaker assumptions.
