# Mithril Agent overview

Mithril Agent is a programmatic automation layer for a Mithril Solana full node.
The primary direction is to read, decode, index, and simulate on-chain programs
without a browser wallet or block explorer. The bounded trading system remains
an optional execution capability rather than the product's only workflow.

The current end-to-end **execution** pilot is deliberately Devnet only. It
demonstrates the complete bounded trading and safety flow. Walletless custom
indexing and pinned arbitrary-program read/build/simulation now work locally;
the private workspace setup removes repeated connection and directory flags.
Concurrent network serving and Mainnet funded execution are not ready.

The rooted feed supports two deliberately distinct evidence paths. Alpenglow
uses Mithril's native rooted-durable protocol; classic Devnet, Testnet, and
Mainnet Beta use finalized blocks verified by the local node and are labelled
`mithril_classic_finalized_feed`. Enabling classic rooted publication requires
a fresh or separate AccountsDB. Alpenglow workspaces pin the cluster's exact
genesis because community clusters may regenerate.

## Product profiles

1. **Observe and index:** no signing key, wallet application, or explorer.
2. **Build and simulate:** pinned program interface and deterministic builder,
   still with no signing key.
3. **Execute:** optional policy, signer, submitter, and recovery. Solana still
   requires a fee payer and any instruction authorities to sign.

See [`ROADMAP.md`](ROADMAP.md) for the current/target matrix and implementation
order.

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
- Canonical direct or external-account Program Metadata interfaces can be fetched
  through loopback Mithril, checked against their program address, pinned by
  exact SHA-256 with read provenance, and used to build and simulate
  instructions or decode live/indexed account data with Solana IDL 0.1.0 and
  bounded Codama 1.x codecs, without a key or submission method.
- A private walletless workspace validates one program, cluster, loopback node,
  and accounts path once, creates its interface and index directories, and is
  reused by canonical fetch, live account read, and unsigned simulation.
- A local stdio MCP is pinned to one private workspace and exact interface hash.
  It reuses the same validated interface summary, unsigned build, processed
  read/simulation, and rooted decode paths without accepting caller-selected
  files or endpoints, opening a listener, or gaining signing authority.
- Mithril rooted account events can be persisted in a restart-safe,
  hash-chained local index and queried by owner-matching history, exact account,
  or durable cursor. Owner history is explicitly not labelled current state.
- Machine-readable outputs distinguish live `processed` bank reads and
  simulation from manifest-selected `rooted` index evidence; a Mithril context
  slot is never silently promoted into a rooted claim.
- Rooted transactions, logs, CPI, return data, and slot boundaries can be
  persisted and queried by exact signature, mentioned address, or cursor.
- Pinned program events can be decoded from one exact rooted transaction
  without an explorer, external RPC, or signing key.
- Hash-chained journals and Prometheus metrics preserve operational evidence
  and make failures visible.
- Mainnet shadow mode records hypothetical decisions for evaluation; it holds
  no wallet signing key and cannot sign or submit transactions. Its journal
  policy binds the quote provider, pool, and token pair, preventing route drift.
- Mainnet shadow USD accounting is additionally gated by independent Pyth-on-node
  and Kraken USDC/USD evidence; a stale source, disagreement, or depeg stops the
  observation instead of silently valuing USDC at one dollar.
- Shadow reports hash-chain their exact period end, count observations missed
  during downtime, and recompute each fill from its exact decision-time quote.
- A read-only shadow review verifies consecutive complete Mainnet days at 95%
  or better coverage and summarizes them without approving or enabling a rule.
- A Mainnet proposal checker with no wallet-signing capability verifies one
  narrow Jupiter Exact-In candidate in either approved SOL-to-token or
  token-to-SOL direction. It independently witnesses the pre-created output
  account for SOL input or the funded input account for token input, plus the
  exact bytes, fees, lookup tables, lifetime, and Mithril
  simulation, and makes both evidence providers recover one protected old v0
  transaction before stopping ahead of authorization.
- An offline bundle check verifies that the candidate plus the protected
  authority, signer, and submitter files bind the same route, limits, schedule,
  providers, and identities while leaving signing and submission disabled.
- Identity-only authority and submitter checks prove their installed keys match
  the protected policy set without receiving an RPC or transaction.
- An offline proposal review independently decodes the exact signer request and
  shows its bounded direction, mint addresses, base-unit amounts, maximum debit,
  schedule, action, and message hash without loading a key or granting authority.
- A separate operator wallet can sign that exact human-readable review through
  a short-lived loopback page. The detached approval is required by the Mainnet
  risk authority and cannot sign or submit a transaction by itself.
- A read-only canary check binds that policy set to the keyless operator socket,
  verifies the exact detached approval, replays the matching multi-day shadow
  evidence, then checks the stopped control revision, prepared action, Mithril,
  and both witnesses; it cannot enable, sign, or submit the action.
- Internal Mainnet package boundaries can grant one exact checked request,
  validate a transaction-only custody response, reserve its durable cap,
  attest it with a separate identity, seal it, validate it independently, and
  preserve the exact v0 and lookup-table evidence needed for fail-closed
  restart reconciliation. The bounded submitter CLI/socket can prepare that
  recovery record offline but cannot submit it. The self-hosted file-key
  adapter is callable through the bounded signer CLI/socket only when separate
  wallet and attestation keys are supplied. The Turnkey transaction-only
  adapter is also callable there only with its complete explicit protected-file
  configuration and no local wallet key. Its opt-in retained-candidate harness
  checks policy-bound Jupiter and lookup-table mutations without an RPC or
  broadcast. No generated service or live Mainnet submit path uses either adapter.
- An unexported Mainnet canary sender uses a control mode distinct from the
  Devnet grant, limited to one action for at most one hour, and activated only
  for the exact reviewed action ID against an unchanged state revision. The
  sender repeats readiness under both locks, marks send-started durably, and
  refuses an expired operator-approved schedule before any RPC and again at the
  final locked boundary. It permits one initial exact-byte attempt. The
  protected submitter policy defaults crash recovery to `stop_only`; an exact
  signed-byte retry requires the explicit `exact_retry` mode and is limited to
  one resubmission of those same persisted bytes. The
  terminal two-provider reconciliation is stored with the exact recovery
  record and retained in an action-ID archive before another proposal can
  replace it. The
  root-only operator socket can activate the state, but no generated service,
  strategy runner, operator command, or live submit path uses the sender.
- Offline Mainnet preparation is explicitly pre-send. The keyless read-only
  `--check-mainnet` command repeats the complete retained-proposal check using
  fresh Mithril and two-witness evidence, then requires fresh independent
  blockhash validity. It returns only the action ID and cannot submit. The same
  keyless `--retire-mainnet` command can archive an expired proposal only while
  control is stopped and only before send-started; retired actions cannot be
  prepared again. The same keyless `--recover` command accepts Mainnet policy,
  but finality reconciliation refuses until a future send path durably marks
  send-started. Two-provider exact effects clear a finalized success or turn a
  finalized failure into an operator-acknowledgeable stop; uncertain evidence
  remains latched.

## The target flow in one line

```text
Mithril replay -> read/MCP + rooted event feed -> custom index and program workflow
-> Mithril simulation -> optional bounded signer/submission -> journal and alerts
```

The language model or agent framework may help explain status or suggest an
intent. It is not the security boundary: deterministic policy, signer limits,
and the expiring operator grant decide whether an action is allowed.

## Where the work lives

The project is split on purpose.

### 1. Mithril node integration

Repository: <https://github.com/NeerajGodiyal/mithril>

The node prerequisites are reviewed as focused branches. Their current complete
dependency order is maintained in [ROADMAP.md](ROADMAP.md#node-prerequisites).
The old all-in-one integration branch remains comparison material and should
not be merged or used for a new deployment.

Mithril Agent v0.1.0 published the agent-side walletless program and index
commands. The matching public-node producer and later Solana-v1 consumer work
remain separately reviewed focused revisions. Until a matched set is authorized
and published, verify the operator-supplied revisions and cross-repository
contract together; an older integration branch is not a substitute.

### 2. Mithril Agent

Repository: <https://github.com/NeerajGodiyal/mithril-agent>

This repository contains program-interface, immutable review-attested program
evidence, rooted-index, and workflow clients,
guided setup, strategy evaluation, bounded authority, optional wallet and signer
isolation, submission and confirmation, journals, Telegram, MCP-facing status,
Prometheus integration, and demonstration tooling. Pinned account and event
decoding plus rooted transaction/log indexing are available locally. Bounded
local stdio MCP surfaces expose the workspace-pinned program workflow and
metadata-only index queries under the operator's OS identity and
private-directory permissions; concurrent network serving is not implemented.

Agent frameworks are replaceable clients. The program, index, and status MCP
surfaces are read-only. The dedicated research MCP may write only an immutable
paper challenger and its pointer; none of these surfaces belongs in the signer.

## What this does not claim yet

- No Mainnet signing or autonomous Mainnet execution is enabled.
- Rooted Solana v1 transactions are decoded and identity-checked for the local
  index. They are not signed or executed; supported execution stays on the
  existing bounded legacy and v0 paths.
- The demonstrated live route is one fixed Devnet SOL/devUSDC route, not an
  arbitrary-token trading system.
- Telegram and MCP cannot approve, sign, submit, or initiate real trades.
- An LLM cannot bypass policy or directly access the signing key.
- A newly generated supervised service isolates the submitter key, policy, and
  writable activation state behind a narrow runtime socket. A root-owned `0600`
  socket handles enable/acknowledgement, while a separate keyless timer verifies
  exact finalized effects through the two bound providers before resolving
  recovery. A confirmed success clears the marker; a confirmed failure stays
  stopped until an operator acknowledges it. This boundary remains Devnet-only
  and is not production custody.
- Devnet proves mechanics and safety behavior, not trading profitability.
- A production token allowlist, strategy, custody model, Mainnet canary limits,
  and a separately reviewed immutable deployment of the included route guard
  still require explicit decisions and separate evidence. The v7 policy and
  transaction path fail closed unless that exact deployment identity is
  supplied and verified.

## How to review or run it

Choose the path that matches what you are doing:

1. **Understand the project:** read this file, then [`ROADMAP.md`](ROADMAP.md).
   Follow [`WALLETLESS_QUICKSTART.md`](WALLETLESS_QUICKSTART.md) for the default
   path or [`DEMO.md`](DEMO.md) for the optional trading pilot.
2. **Review an already installed pilot:** follow the reviewer path in
   [`DEMO.md`](DEMO.md). It includes read-only checks and a bounded Devnet
   demonstration.
3. **Install on a fresh Linux host:** follow [`QUICKSTART.md`](QUICKSTART.md)
   from top to bottom. Do not combine it with older single-trade service
   examples.
4. **Operate or recover the system:** use [`OPERATIONS.md`](OPERATIONS.md) for the
   detailed security, service, journal, monitoring, and recovery reference.

A fresh installation requires a running Mithril full/RPC node near the Devnet
tip, the exact Mithril integration revision listed above, and the complete
Mithril Agent runtime. A non-technical reviewer should not install services or
edit protected configuration: an operator performs the one-time setup, then
the reviewer uses the read-only status, Telegram, MCP, and bounded demo paths.

For the walletless path, start from the matched source bundle described in
[`WALLETLESS_QUICKSTART.md`](WALLETLESS_QUICKSTART.md):

```sh
cd /path/to/verified/mithril-agent-source
make prereqs
make test-walletless
```

Then follow that guide. The published repository remains suitable for the
separate trading-pilot review described in [`DEMO.md`](DEMO.md). Run
`make test` as well when reviewing the optional execution and trading modules.
Stop at the first failed check; the setup is designed to refuse incomplete or
stale deployments rather than continue with weaker assumptions.
