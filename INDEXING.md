# Rooted program indexing

This is the walletless path for building a small application index directly
from a Mithril node. It needs no wallet, signing key, block explorer, Telegram,
or external RPC. The node emits deterministic rooted transactions, logs, CPI,
return data, account updates, and slot boundaries; Mithril Agent verifies and
stores them in a private hash-chained index.

The source label is part of the evidence. Native Alpenglow replay is labelled
`mithril_alpenglow_rooted_feed`. Classic Devnet, Testnet, and Mainnet Beta use
Mithril's locally verified finalized-block feed and are labelled
`mithril_classic_finalized_feed`; they are never presented as native Alpenglow
certificate evidence.

## What is supported

- historical backfill from Mithril's retained rooted-event batches;
- continuous follow from the newest retained cursor;
- filtering owner-matching account history and/or one exact account address;
- filtering transaction activity by an exact mentioned address;
- restart from the last durable `SLOT:ORDINAL` cursor;
- idempotent replay of the exact same event;
- content-addressed account data up to Solana's 10 MiB account limit;
- bounded newest-first snapshots and lossless oldest-unseen-first continuation;
- local decoding of pinned Borsh account and program-event data; and
- fail-closed detection of malformed input, cursor conflicts, broken root
  lineage, changed blobs, or an index reopened with different source filters.

Transaction logs preserve Mithril's bounded runtime recorder output. A
`logs_truncated` result is explicit; event decoding rejects the whole record
rather than returning a possibly incomplete prefix.

Review both repositories together with:

```sh
make test-rooted-contract MITHRIL_SOURCE=/absolute/path/to/Mithril
```

That test feeds the public producer-owned stable and transaction-v1 golden
source/start/batch/event fixtures through this consumer so independent unit
suites cannot hide wire, message-hash, signed-payload, or inline-priority-fee
contract drift.

The first query implementation intentionally scans verified retained metadata.
It supports local queries and MCP while ingest continues, but it is not a
high-volume public query service. At each manifest batch's terminal root the
writer atomically publishes a tiny durable-prefix descriptor. Concurrent
readers verify that exact hash-chained prefix and never expose the next
in-progress batch. Stop ingest only when an operator needs the exact current
cursor for recovery or an immutable archival report.

## One-time node setting

Enable the opt-in rooted feed in the Mithril node configuration and keep its
accounts directory private:

```toml
[storage]
rooted_events = true
```

On classic Devnet, Testnet, or Mainnet Beta, enable this only with a fresh or
separate AccountsDB. A legacy per-slot AccountsDB cannot be relabelled as the
classic finalized rooted feed; keep it unchanged and bootstrap a new lineage.
Classic rooted publication also requires finalized RPC selection and the
trailing verifier to fail closed:

```toml
[block]
source = "rpc"

[verifier]
enabled = true
required = true
```

Restarting a node is an operator action. Confirm the node is healthy and
advancing before starting an index. The index never starts or changes the node.

## Backfill one program's owner-matching account history

Choose a private absolute directory and use the same owner filter on both
commands. In Bash or Zsh, enable pipeline failure propagation first so an
upstream Mithril export failure cannot be hidden by a clean indexer exit:

```sh
set -o pipefail
PROGRAM='<SOLANA_PROGRAM_ADDRESS>'
ACCOUNTS_ROOT='/absolute/path/to/mithril-accounts-storage-root'
NODE_CONFIG='/absolute/path/to/mithril-config.toml'
INDEX='/var/lib/mithril-agent/indexes/my-program'
CLUSTER='alpenglow' # or devnet, testnet, mainnet-beta
GENESIS_HASH='<EXACT_GENESIS_HASH_FOR_THAT_CLUSTER>'

mkdir -p "$INDEX"
chmod 700 "$INDEX"

mithril --config "$NODE_CONFIG" events --framed --accounts "$ACCOUNTS_ROOT" --owner "$PROGRAM" \
  | mithril-agent index ingest --dir "$INDEX" --cluster "$CLUSTER" \
      --genesis-hash "$GENESIS_HASH" --owner "$PROGRAM"
```

`ACCOUNTS_ROOT` is Mithril's `storage.accounts` value: the private storage root
that contains the nested `accounts/` and `rooted-events/` directories. Do not
append `/accounts` to it.

Run the left side as the unprivileged service identity that already owns and
can read Mithril's private AccountsDB. Run the right side as the private index
workspace owner. If those are different identities, connect them with a
supervisor-managed stdout pipe; do not make AccountsDB group- or world-readable.
Passing both `--config` and `--accounts` loads the configured retention horizon
while overriding only the storage path.

The owner filter is evaluated against each update's post-state owner. This
index is therefore rooted history of records that matched the program, not
proof that the newest matching account is still owned by the program or still
exists. Account decode output labels this scope `owner_matching_history` and
sets `current: false`. Use `program read-account` for current processed state,
or build a separate index with only `--account ADDRESS` when current rooted
state for one exact address is required. Do not combine that exact-account
filter with `--owner`, because an ownership exit would again be omitted.

The command exits only after the bounded backfill input ends. Resume an
existing index from its exact durable cursor; a new full-history stream is
rejected instead of being mistaken for a resume.

Backfill writes are synced at every rooted slot boundary and at least every
1,024 newly stored records. A clean exit or reported cursor is fully synced.
If the process is interrupted, reopen the same private directory and replay
from its last reported durable cursor (or replay the retained feed exactly);
the hash chain rejects conflicting content. This batching applies only to the
replayable rooted index. Action, signer, and authorization journals keep their
per-record durability.

An interrupted ingest may preserve a valid prefix of its current slot. `status`
reports that cursor with `complete: false` so the same stream can resume, while
doctor, queries, decoding, and MCP refuse the index until that slot's root
marker is stored. This keeps recovery information without presenting a partial
slot as a complete rooted snapshot. A bounded ingest that reaches end-of-input
before the root marker also exits nonzero even though it safely retains that
prefix for the documented resume flow.

Transaction activity uses a separate index because account-owner filters and
transaction-mention filters describe different data. Keeping their durable
cursors separate also makes restart behavior unambiguous:

```sh
ACTIVITY_INDEX='/var/lib/mithril-agent/indexes/my-program-activity'

mkdir -p "$ACTIVITY_INDEX"
chmod 700 "$ACTIVITY_INDEX"

mithril --config "$NODE_CONFIG" events --framed --accounts "$ACCOUNTS_ROOT" --mention "$PROGRAM" \
  | mithril-agent index ingest --dir "$ACTIVITY_INDEX" --cluster "$CLUSTER" \
      --genesis-hash "$GENESIS_HASH" --mention "$PROGRAM"
```

## Follow new roots

For a new index that should begin with future events rather than retained
history:

```sh
mithril --config "$NODE_CONFIG" events --framed --accounts "$ACCOUNTS_ROOT" --latest --follow --owner "$PROGRAM" \
  | mithril-agent index ingest --dir "$INDEX" --cluster "$CLUSTER" \
      --genesis-hash "$GENESIS_HASH" --owner "$PROGRAM"
```

For a previously populated index, first stop its ingest process and read the
last durable cursor:

```sh
mithril-agent index status --dir "$INDEX"
```

Then resume after the printed cursor:

```sh
LAST='<SLOT:ORDINAL_FROM_STATUS>'
mithril --config "$NODE_CONFIG" events --framed --accounts "$ACCOUNTS_ROOT" --after "$LAST" --follow --owner "$PROGRAM" \
  | mithril-agent index ingest --dir "$INDEX" --cluster "$CLUSTER" \
      --genesis-hash "$GENESIS_HASH" --owner "$PROGRAM"
```

Never use `--latest` to resume an existing index: doing so can skip retained
events between its last durable cursor and the current tip.

`--framed` is mandatory. Its source and stream-start records bind the index to
Mithril's ready AccountsDB lineage and to either full retained history or one
exact resume cursor. Every selected immutable sidecar contributes its manifest
sequence, slot range, version, and SHA-256. Private index schemas before v5 do
not bind the retained event schema or persist the current transaction identity
and full root lineage. Preserve them for audit and backfill a new private v5
directory; do not relabel or edit them in place. The public rooted-event wire
remains schema 3, so retained schema-3 batches stay resumable.

For a program workspace, prefer the shorter form below. It derives the fixed
index directory, cluster, genesis hash, and permanent filter from the private
workspace instead of asking the operator to repeat them:

```sh
mithril --config "$NODE_CONFIG" events --framed --accounts "$ACCOUNTS_ROOT" --owner "$PROGRAM" \
  | mithril-agent index ingest --workspace "$WORKSPACE" --kind state

mithril --config "$NODE_CONFIG" events --framed --accounts "$ACCOUNTS_ROOT" --mention "$PROGRAM" \
  | mithril-agent index ingest --workspace "$WORKSPACE" --kind activity
```

## Query safely

Verify and query the latest complete snapshot. These commands may run while
ingest continues; during the next manifest batch they intentionally remain at
the previous completely verified batch:

```sh
mithril-agent index doctor --dir "$INDEX"
mithril-agent index status --dir "$INDEX"
mithril-agent index query --dir "$INDEX" --limit 20
mithril-agent index query --dir "$INDEX" --account '<ACCOUNT_ADDRESS>' --limit 20
mithril-agent index query --dir "$INDEX" --after '<SLOT:ORDINAL>' --limit 100 --json
mithril-agent index transactions --dir "$ACTIVITY_INDEX" --limit 20
mithril-agent index transactions --dir "$ACTIVITY_INDEX" \
  --signature '<TRANSACTION_SIGNATURE>' --include-payload --json
```

Account bytes and signed transaction payloads are omitted by default. Include
them only when needed; JSON encodes bytes as base64. Transaction version and
message hash remain available as bounded metadata. JSON account and transaction
queries return a single envelope that labels `provenance` and `finality` beside
the results, so an agent never has to infer rootedness from a command name:

```sh
mithril-agent index query --dir "$INDEX" --account '<ACCOUNT_ADDRESS>' \
  --limit 1 --include-data --json
```

If the program has a pinned modern IDL, decode the newest completely published
owner-matching account record directly from the index. This verifies the recorded owner, pinned
discriminator, Borsh shape, exact byte consumption, and data hash:

```sh
mithril-agent program decode-account \
  --registry '<PROGRAM_INTERFACE_REGISTRY>' \
  --program "$PROGRAM" --sha256 '<PINNED_IDL_SHA256>' \
  --account-type '<IDL_ACCOUNT_TYPE>' \
  --index-dir "$INDEX" --account '<ACCOUNT_ADDRESS>'
```

The same command accepts `--data '<RAW_ACCOUNT_DATA_FILE>'` instead of
`--index-dir` and `--account` for a stable local file. Both paths are local and
load no wallet or signing key. JSON and human output label an index-backed
decode as `mithril_alpenglow_rooted_feed` or
`mithril_classic_finalized_feed`, plus `rooted`, its cursor, scope, and whether
the filter can prove current state. An owner-filtered result is historical and
sets `current: false`; a filter-free or exact-account-only index sets
`current: true`. A raw file
decode is `local_file` / `unverified`; local bytes are never silently presented
as chain-final evidence.

Decode a pinned Borsh event emitted by one exact rooted transaction. The
decoder follows the runtime invocation stack, so a CPI program's `Program data`
line cannot be mistaken for the requested program's event:

```sh
mithril-agent program decode-event \
  --registry '<PROGRAM_INTERFACE_REGISTRY>' \
  --program "$PROGRAM" --sha256 '<PINNED_IDL_SHA256>' \
  --event-type '<IDL_EVENT_TYPE>' \
  --index-dir "$ACTIVITY_INDEX" \
  --signature '<TRANSACTION_SIGNATURE>'
```

The transaction signature is only a stable local lookup key here. This command
does not contact an explorer or RPC service. Each decoded event includes the
rooted-feed provenance, finality, and cursor in machine-readable output.

## Build or simulate a pinned instruction

Build exact unsigned bytes locally when another component needs an auditable
artifact. The recent blockhash and fee payer are public values; this command
loads no private key. JSON output includes the message and unsigned transaction
as base64:

```sh
mithril-agent program build --json \
  --registry '<PROGRAM_INTERFACE_REGISTRY>' \
  --program "$PROGRAM" --sha256 '<PINNED_IDL_SHA256>' \
  --instruction '<IDL_INSTRUCTION>' \
  --fee-payer '<PUBLIC_FEE_PAYER_ADDRESS>' \
  --recent-blockhash '<RECENT_BLOCKHASH>' \
  --account '<IDL_ACCOUNT_NAME>=<ACCOUNT_ADDRESS>' \
  --arg '<IDL_ARGUMENT_NAME>=<JSON_VALUE>'
```

Use `mithril-agent program simulate` to fetch a fresh blockhash from the
loopback Mithril node and run the same builder through walletless simulation.
Simulation validates the node's cluster identity and minimum context slot and
returns content hashes instead of raw logs. A real state-changing transaction
still requires a Solana-authorized signer; no software can remove that protocol
rule.

## Operational rules

- Keep one index directory per permanent owner/account or mention filter.
- Keep the directory on durable storage with mode `0700`; files are private.
- Preserve `events.jsonl`, its numbered segments and lock file,
  `query-snapshot.json`, and `blobs/` together. They are one integrity
  boundary. The snapshot is a replaceable descriptor, never a second source
  of truth; every read re-verifies its record count and journal chain head.
- Stop at the first error. Do not delete a conflicting cursor or changed blob
  to make ingestion continue.
- A new Mithril node whose retained history no longer contains the last cursor
  needs a deliberate fresh backfill into a new directory.
- The proof index is deliberately a bounded local journal with concurrent local
  reads, not a high-volume database or public network server. A measured need for those
  workloads should select a storage backend without changing the rooted feed or
  pinned decoding rules.
- Network MCP query exposure remains disabled. The supported local stdio MCP
  boundary is described below.

## Recover without changing evidence

Run the read-only doctor at any time. While ingest is active it checks the last
completely published batch. Stop ingest first only when diagnosing an error or
capturing an exact archival endpoint:

```sh
mithril-agent index doctor --dir "$INDEX"
```

If it reports `ready`, the journal, retained records, account blobs, rooted
lineage, and content hashes form one valid snapshot. If it reports that
attention is required, follow the printed plan. Never edit, truncate, or delete
the old directory to make validation pass. First stop ingest and retry. If the
check still fails, keep that directory unchanged for audit, create a different
mode-`0700` directory, and backfill the replacement from Mithril's retained
rooted events. Use `--latest` only when deliberately accepting a future-only
index; it is not recovery for a missed cursor.

Automation may use `index doctor --json`. A result with `ready: false` also
exits nonzero, so a script cannot accidentally treat recovery instructions as
a healthy index.

## Local MCP queries

The supported MCP boundary is local stdio, not a network service. Keep the
index directory private with mode `0700`, and generate
one explicit client entry for each index:

```sh
mithril-agent index mcp-config \
  --dir "$STATE_INDEX" --name my-program-state
```

The command prints one valid JSON object that can be pasted directly into an
MCP client's configuration.

Codex and Claude Code can instead register the exact local stdio command:

```sh
codex mcp add my-program-state -- \
  /usr/local/bin/mithril-agent index mcp --dir "$STATE_INDEX"

claude mcp add --transport stdio my-program-state -- \
  /usr/local/bin/mithril-agent index mcp --dir "$STATE_INDEX"
```

Verify with `codex mcp list` or `claude mcp list`. For an MCP client on a
different machine, make `ssh -T MITHRIL_HOST` the registered command prefix and
run the same absolute `mithril-agent index mcp --dir ...` command on the index
host. SSH identity and the private index directory remain the authorization
boundary; do not open a network MCP listener or copy credentials into the
client entry.

The configured MCP client launches `mithril-agent index mcp` under the same OS
account. It may remain connected while the single ingest writer advances. Each
call sees one completely published and reverified journal prefix, never a
partially written slot or batch. That account and the exact private directory in the generated command
authorize access; no port is opened and no bearer token is stored. The three
tools expose verified status, bounded account metadata, and bounded transaction
metadata. They cap queries at 1,000 results and never return raw account bytes,
signed transactions, logs, CPI, or return data. The local server runs at most
four tool calls concurrently and immediately refuses additional calls; clients
may retry after an active call finishes. Use the local CLI when a reviewed
workflow explicitly needs those payloads.

This path is read-only from Solana's perspective. It cannot sign or submit a
transaction and has no dependency on the optional trading system.

For decoded program values and unsigned simulation, generate the separate
workspace-pinned program MCP entry after pinning an interface:

```sh
mithril-agent program mcp-config \
  --workspace "$WORKSPACE" --sha256 "$IDL_SHA256" --name my-program
```

This command also prints one valid JSON object with no explanatory text mixed
into it.

That server can decode only from the workspace's fixed state/activity indexes
and can contact only its configured literal-loopback Mithril node. It does not
turn the index MCP into a payload API or a network service.
