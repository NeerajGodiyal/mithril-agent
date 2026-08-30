# Walletless program quick start

Use this path to inspect, index, decode, build, and simulate a Solana program
through a Mithril node. It needs no browser wallet, signing key, or block explorer
and does not submit transactions.

In plain language, the workflow is:

1. Create one private folder that remembers the program and Mithril node.
2. Fetch or pin one reviewed description of the program.
3. Let Mithril export verified history into two local indexes, then run the
   doctors until both report ready.
4. Decode state and events, or build and simulate an unsigned call.
5. Optionally connect the same read-only tools to a local AI client through
   stdio MCP. The client receives no wallet, signing, or submission capability.

Stop at the first failed command. The recovery text printed by the command is
safer than deleting a workspace, changing its identity, or guessing a cursor.

Provenance is explicit rather than implied. Before accepting program evidence,
`program fetch`, `read-account`, `workspace-doctor`, and `simulate` require the
loopback endpoint to answer Mithril's node-specific verification-status method
with its evidence gate open and to match the configured genesis. They then use
Mithril's coherent published `processed` bank and label it as not rooted.
`mithril events` and every index built from it
use a manifest-selected rooted feed. Native Alpenglow evidence is labelled
`mithril_alpenglow_rooted_feed`; classic Devnet, Testnet, and Mainnet Beta use
Mithril's locally verified finalized-block feed and are labelled
`mithril_classic_finalized_feed`.

## Before you start

You need:

- a healthy Mithril RPC node built with the rooted-event-feed changes;
- for rooted indexing, the node's private AccountsDB storage root
  (the directory that contains `accounts/` and `rooted-events/`);
- a Solana program address; and
- a reviewed Solana IDL spec 0.1.0 or Codama 1.x file, or a program whose
  canonical Program Metadata account publishes one of those interface formats.

The rooted feed has two deliberately separate evidence paths. Alpenglow uses
Mithril's native rooted replay and certificate path. Classic `devnet`,
`testnet`, and `mainnet-beta` use finalized blocks fetched and verified by the
local Mithril node before durable rooted-event batches are published. Classic
output must never be relabelled as native Alpenglow evidence. Alpenglow is not
active on Solana Mainnet today; Mainnet uses the classic finalized path.

The node configuration must contain:

```toml
[storage]
rooted_events = true
```

For classic Devnet, Testnet, or Mainnet Beta, use a fresh or separate
AccountsDB when enabling rooted publication. Do not reuse or relabel an
existing legacy per-slot AccountsDB as finalized rooted evidence. Classic
rooted publication also requires finalized RPC selection and the trailing
verifier to fail closed:

```toml
[block]
source = "rpc"

[verifier]
enabled = true
required = true
```

Enabling the feed and restarting Mithril are operator actions. Confirm the node
is healthy and advancing first.

### Capacity gate for classic acceptance

Classic rooted acceptance must use separate config, AccountsDB, snapshot, log,
and workspace roots. Before starting it, reserve the operator's required free
space on every backing filesystem and budget the new AccountsDB in addition to
that reserve. Do not count a preserved snapshot as free space or point another
cluster at its snapshot directory. Run an independent watcher that samples the
same filesystems and sends `SIGINT` to the exact acceptance-node PID at the
first reserve breach; a clean node shutdown is part of the recovery evidence.

If Mainnet cannot meet that preflight budget, do not start it merely to let the
watcher stop it later. Bootstrap a fresh Devnet classic lineage in separate
roots and run the same finalized-RPC, required-verifier, rooted-feed, backfill,
restart, simulation, and stdio-MCP checks there. A passing Devnet run proves the
classic implementation path only. It does not prove Mainnet storage capacity,
Mainnet soak readiness, or permission to enable Mainnet signing or submission.

A successful gossip join does not by itself prove that the cluster is sending
valid shreds and Alpenglow certificates to this node. Complete that passive
ingress check before relying on the rooted feed. If the cluster does not route
live Turbine traffic to a fresh unstaked identity, use a separately generated
test identity whose public keys receive a small operator-approved delegation.
An operator-supported Lightbringer/relay source can separately test block
streaming, but its current public response does not carry the raw certificate
evidence required to prove Mithril's native Votor path. Never copy a production
validator, voter, or withdrawer private key into this setup.

## Install the walletless CLI

This path needs Go 1.26.6 or newer. It does not need Node.js, a quote adapter,
Telegram, a custody account, or any of the optional trading services:

This walletless work is newer than the published trading pilot. Use the exact
operator-supplied source bundle or focused branch revisions under review, and
only after their source and cross-repository contract checks pass. Do not rely
on a stale bundle name or checksum copied from an earlier review.
The Mithril source must contain the `events` command and
`storage.rooted_events`; the agent source must contain this guide plus the
`program` and `index` commands. Do not clone the older trading-pilot revision
and assume it contains these features. An archive is review evidence, not a
published revision. The old all-in-one node integration branch is not a
substitute for the focused node prerequisites.

```sh
cd /path/to/verified/mithril-agent-source
make verify-source
make test-walletless
make test-rooted-contract MITHRIL_SOURCE=/absolute/path/to/Mithril
install -d -m 0755 bin
(umask 022 && go build -o bin/mithril-agent ./cmd/mithril-agent)
chmod 0755 bin/mithril-agent
sudo install -m 0755 bin/mithril-agent /usr/local/bin/mithril-agent
mithril-agent program --help
```

Stop if source verification, tests, or the build fail. Installing only this
binary is sufficient for the walletless commands in this guide. The other Go
binaries and Node.js adapter are required only by the separate trading setup.
The explicit modes matter on shared Linux hosts: `mcp-config` refuses an
executable whose file or directory ancestry is group- or world-writable. If it
reports that the executable is not trusted, repeat the mode-preserving build
and install commands above; do not weaken that check.

## 1. Create one private workspace

Choose the program once. The workspace creates and remembers the interface and
index paths, cluster, loopback node address, and, for Alpenglow, the Mithril
AccountsDB storage root:

```sh
PROGRAM='<SOLANA_PROGRAM_ADDRESS_ON_THIS_CLUSTER>'
CLUSTER='alpenglow'
GENESIS_HASH='<EXACT_CURRENT_ALPENGLOW_GENESIS_HASH>'
NODE_RPC='http://127.0.0.1:8899'
MIN_SLOT='<CURRENT_VERIFIED_MITHRIL_SLOT>'
ACCOUNTS_ROOT='/absolute/path/to/mithril-accounts-storage-root'
NODE_CONFIG='/absolute/path/to/mithril-config.toml'
WORKSPACE_DIR='/var/lib/mithril-agent/programs/my-program'
WORKSPACE="$WORKSPACE_DIR/workspace.json"

mithril-agent program workspace-create \
  --dir "$WORKSPACE_DIR" --program "$PROGRAM" --cluster "$CLUSTER" \
  --genesis-hash "$GENESIS_HASH" \
  --node-rpc "$NODE_RPC" --accounts "$ACCOUNTS_ROOT"
mithril-agent program workspace-check --workspace "$WORKSPACE"

REGISTRY="$WORKSPACE_DIR/interfaces"
STATE_INDEX="$WORKSPACE_DIR/state-index"
ACTIVITY_INDEX="$WORKSPACE_DIR/activity-index"
```

`ACCOUNTS_ROOT` is the value of Mithril's `storage.accounts` setting, not its
nested `accounts/` directory. For example, if fold manifests live under
`/var/lib/mithril/accounts/`, use `/var/lib/mithril`.

The create command makes private mode-0700 directories and a mode-0600 configuration
file. It validates paths and settings without contacting the network. It loads
no wallet and stores no key. `NODE_RPC` must be a literal loopback URL.

`workspace-doctor` is the final read-only readiness gate. It first proves that
the literal-loopback endpoint is a compatible Mithril node whose evidence gate
is open and whose genesis matches the workspace; a generic Solana RPC hidden
behind a local proxy is rejected. When the workspace has an AccountsDB root,
run it after both rooted-index backfills in step 3: it verifies that the node
answers at the requested minimum slot and that both indexes are
complete, source-bound to this workspace and AccountsDB lineage, and indexed
through that slot. It also requires the node's `getRootedFeedStatus` response
to prove rooted publication is enabled and to match both indexes' AccountsDB
root-run identity, so an old index cannot be accepted after a rebuild. An active ingest is allowed: the doctor reads only its last
completely published manifest batch. It does not simulate or submit anything.

Alpenglow community clusters may regenerate, so obtain the exact current
genesis hash from the node operator and pin it in the workspace. The doctor
refuses a different node identity. For a classic Devnet, Testnet, or Mainnet
Beta rooted workspace, select the fixed cluster, omit `--genesis-hash`, and
keep `--accounts`; the fixed genesis identity is built in. Omit `--accounts`
only for a deliberately simulation-only workspace. In that profile,
`workspace-doctor` checks the Mithril verification gate, node identity, and
processed context slot and may be run immediately after `workspace-check`.

Re-running `workspace-create` with the same values is safe. A different value
is refused instead of silently changing an existing workspace. Use a separate
workspace for each program or cluster.

Both `workspace-create` and `workspace-check` print the next safe actions. JSON
output includes the same bounded `next_steps` list. A simulation-only workspace
is directed to interface pinning and `workspace-doctor`; a workspace with an
AccountsDB root is first directed through both rooted-index ingests and their
doctors. These hints never include the configured RPC endpoint or AccountsDB
path.

## 2. Pin the exact interface

Fetch a canonical interface through Mithril. Direct and external-account Program
Metadata sources are resolved automatically:

```sh
mithril-agent program fetch --json \
  --workspace "$WORKSPACE" \
  --min-context-slot "$MIN_SLOT"
```

If the program does not publish one, pin a reviewed local modern IDL instead:

```sh
mithril-agent program pin --json \
  --idl '/absolute/path/to/program.json' \
  --workspace "$WORKSPACE"
```

Save the returned `sha256` as `IDL_SHA256`. Every later decode or build checks
that exact immutable interface.

Program Metadata can carry several interface formats. This path pins unchanged
and builds or decodes the current Solana IDL spec 0.1.0 format used by Anchor and
Quasar, plus bounded Codama 1.x type nodes. Unsupported Codama codecs and custom
serialization fail closed. An instruction with dynamic remaining accounts can
still be inspected and decoded, but generic construction is refused until a
reviewed dedicated adapter defines those accounts; the richer schema is never
silently translated into transaction bytes. Codama size discriminators are
enforced as exact account-data lengths. Conditional `isSigner: "either"`
accounts are retained in interface summaries, but their construction is also
refused until a dedicated adapter defines the single-authority and multisig
forms. This permits current official SPL Token account inspection and decoding
without guessing signer behavior.

To let an MCP-capable local agent use this exact workspace, generate its client
entry after pinning:

```sh
mithril-agent program mcp-config \
  --workspace "$WORKSPACE" --sha256 "$IDL_SHA256" --name my-program
```

Paste the emitted `mcpServers` entry into the agent's local MCP configuration.
The command output is one valid JSON object, so the complete output can be
copied without removing commentary.
The client launches one stdio process pinned to this workspace and interface
hash. The operator's OS account plus the mode-0700 workspace are the
authorization boundary; no port, token, wallet, or signing key is created.

Codex and Claude Code can also register the same local stdio command directly.
Use the installed binary and the exact private workspace and interface hash;
neither command copies workspace contents into the client configuration:

```sh
codex mcp add my-program -- \
  /usr/local/bin/mithril-agent program mcp \
  --workspace "$WORKSPACE" --sha256 "$IDL_SHA256"

claude mcp add --transport stdio my-program -- \
  /usr/local/bin/mithril-agent program mcp \
  --workspace "$WORKSPACE" --sha256 "$IDL_SHA256"
```

Verify with `codex mcp list` or `claude mcp list`; Claude Code also shows the
connected tools in `/mcp`. Keep this as a local or user-scoped private server,
not a project configuration that publishes host-specific private paths.

When the client runs on another machine, launch the same stdio command as an
authenticated SSH remote command and keep the workspace on the node host. Do
not publish the process as an HTTP service or open a new MCP port. OAuth is an
HTTP-transport requirement; SSH identity is the supported remote authorization
boundary for this stdio workflow.

For example, first configure `MITHRIL_HOST` as a reviewed host alias in the
operator's SSH configuration, then register the remote command with Codex:

```sh
codex mcp add my-program-remote -- \
  ssh -T MITHRIL_HOST \
  /usr/local/bin/mithril-agent program mcp \
  --workspace /absolute/private/workspace.json --sha256 PINNED_INTERFACE_HASH
```

The SSH account must already be allowed to execute the binary and read that
workspace. Do not put a password, private key, RPC URL, or bearer token in the
MCP entry. Test the SSH host key and command interactively before handing the
entry to an MCP client.

For an external-account source, the result records the metadata and content
accounts plus their exact processed slot and bank hash. Pinning fails unless
both reads came from the same published Mithril bank. The exact resolved bytes
are what get pinned. URL sources remain disabled; download and review them
separately, then use `program pin`.

If a program is unfamiliar, keep repository analysis, decompiler output, and
simulation results as separate reviewed evidence. First write a strict review
file. This records local audit attribution; it is not a cryptographic signature:

```json
{
  "version": 3,
  "reviewer": "operator",
  "decision": "approved",
  "summary": "A concise reviewed conclusion that an agent may rely on; do not paste raw tool output here.",
  "source_revision": "the exact repository commit or deployed program-data hash",
  "tool": "the reviewed analysis tool",
  "tool_version": "its exact version",
  "interface_sha256": "the exact pinned interface SHA-256 this review evaluated",
  "genesis_hash": "the genesis_hash emitted by program simulate or read-account",
  "context_slot": 123,
  "bankhash": "the exact processed bankhash emitted with that context slot",
  "deployment_sha256": "the immutable deployment SHA-256 emitted at the same bank"
}
```

Pin the local artifact, its review, the program, and the exact resulting content
hash together:

```sh
mithril-agent program evidence-pin --json \
  --workspace "$WORKSPACE" \
  --kind repository --file '/absolute/path/to/repository-review.md' \
  --review '/absolute/path/to/repository-review.json'
```

Use `--kind decompiler` for a decompiler artifact and `--kind simulation` for
a saved simulation result. Every version-3 review must bind the exact pinned
`interface_sha256`, `genesis_hash`, `context_slot`, `bankhash`, and
`deployment_sha256` emitted by the same-bank Mithril read. A simulation review
must additionally include the exact `message_sha256` shown by the simulation
command. Mithril Agent stores the artifact and immutable review
privately, revalidates both before listing them, and never executes or
interprets the artifact. Only the bounded reviewer-written `summary` is exposed
to the workspace MCP; raw repository, decompiler, and simulation artifacts
remain private. Evidence without a matching approved review fails closed. It
does not become an interface and cannot enable construction by itself; a
reviewed, pinned Solana IDL 0.1.0 or Codama 1.x interface remains required.
Existing version-1 and version-2 attestations remain readable as historical
local audit records but are never exposed by the workspace MCP. Version-3
reviewer attribution and summaries are exposed only while the workspace
genesis and the live program deployment SHA-256 still match the attestation.
For a legacy pre-0.30 Anchor IDL, use the official `anchor idl convert` command,
review the result against the program source, then pin the converted file.

## 3. Prove rooted indexing

Run the following pipelines from Bash or Zsh after enabling pipeline failure
propagation. This makes a Mithril export error fail the whole pipeline instead
of leaving a partial backfill looking successful:

```sh
set -o pipefail
```

Backfill owner-matching account history:

```sh
mithril --config "$NODE_CONFIG" events --framed \
  --accounts "$ACCOUNTS_ROOT" --owner "$PROGRAM" \
  | mithril-agent index ingest --workspace "$WORKSPACE" --kind state
```

Backfill transactions that mention the program:

```sh
mithril --config "$NODE_CONFIG" events --framed \
  --accounts "$ACCOUNTS_ROOT" --mention "$PROGRAM" \
  | mithril-agent index ingest --workspace "$WORKSPACE" --kind activity
```

The workspace form derives the exact directory, cluster, genesis hash, and
permanent owner/mention filter so they cannot drift between commands. Run the
Mithril exporter as the unprivileged node service identity that can read its
private AccountsDB; if the index has a different owner, use a supervised stdout
pipe instead of broadening AccountsDB permissions. The framed source identity,
initial stream boundary, and selected sidecar hashes are permanently bound to
each v5 index. The explicit node config preserves its configured retention
horizon while `--accounts` overrides only the storage path. The commands stop
at the first malformed event, source mismatch, batch sequence gap, or history conflict.

The state index's owner filter matches each record's post-state owner. It is
rooted owner history, not proof that the newest matching account remains owned
or live after a later ownership exit or closure. Its decoded results therefore
set `scope: "owner_matching_history"` and `current: false`. Use
`program read-account` for current processed state; an operator who needs
current rooted state for one known address can maintain a separate
exact-account-only index without an owner filter.
After each bounded backfill exits, verify and query the stopped indexes:

```sh
mithril-agent index doctor --dir "$STATE_INDEX"
mithril-agent index doctor --dir "$ACTIVITY_INDEX"
mithril-agent index status --dir "$STATE_INDEX"
mithril-agent index query --dir "$STATE_INDEX" --limit 20
mithril-agent index transactions --dir "$ACTIVITY_INDEX" --limit 20
mithril-agent program workspace-doctor \
  --workspace "$WORKSPACE" --min-context-slot "$MIN_SLOT"
```

Preserve a private, machine-readable acceptance record from the stopped
indexes. These JSON reports contain hashes, counters, slots, and public chain
identities, but no wallet, key, RPC URL, raw account data, transaction payload,
or artifact contents:

```sh
umask 077
REPORT_DIR="$WORKSPACE_DIR/acceptance-$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0700 "$REPORT_DIR"
mithril-agent index doctor --json --dir "$STATE_INDEX" >"$REPORT_DIR/state-doctor.json"
mithril-agent index doctor --json --dir "$ACTIVITY_INDEX" >"$REPORT_DIR/activity-doctor.json"
mithril-agent index status --json --dir "$STATE_INDEX" >"$REPORT_DIR/state-status.json"
mithril-agent index status --json --dir "$ACTIVITY_INDEX" >"$REPORT_DIR/activity-status.json"
mithril-agent program workspace-doctor --json \
  --workspace "$WORKSPACE" --min-context-slot "$MIN_SLOT" \
  >"$REPORT_DIR/workspace-doctor.json"
(cd "$REPORT_DIR" && sha256sum *.json >SHA256SUMS)
```

Keep the complete directory with the build revision used for the run. A prose
summary or screenshot is not a substitute for these verified outputs.

For continuous foreground ingest, add `--latest --follow` to `mithril events`
when creating a new index. For restart rules and durable cursor resumption, read
[`INDEXING.md`](INDEXING.md); do not guess a resume cursor.

Local CLI and MCP queries can remain online beside that ingest. They verify an
atomically published hash-chained prefix and intentionally lag at the prior
complete manifest batch while the next batch is still being written. Stop the
writer before choosing a recovery cursor or creating the immutable acceptance
bundle above.

Both doctors are read-only. If either asks for attention, preserve the existing
index unchanged and follow the recovery section in `INDEXING.md`; neither
repairs, deletes, or resets evidence in place.

`workspace-doctor --json` still emits a bounded report when it exits nonzero.
Check `ready`, then follow its `reason` and `next_steps`. The report deliberately
omits RPC endpoints, workspace and AccountsDB paths, raw provider errors,
wallets, and keys. Run `program workspace-check` locally when a recovery step
needs the workspace's fixed index paths; do not copy those paths into a remote
support message.

## 4. Decode without an explorer

Decode raw instruction data from a reviewed local file:

```sh
mithril-agent program decode-instruction \
  --workspace "$WORKSPACE" --sha256 "$IDL_SHA256" \
  --instruction '<IDL_INSTRUCTION>' --data '/absolute/path/to/instruction.bin'
```

Decode one indexed owner-history account:

```sh
mithril-agent program decode-account \
  --workspace "$WORKSPACE" --sha256 "$IDL_SHA256" \
  --account-type '<IDL_ACCOUNT_TYPE>' \
  --index-dir "$STATE_INDEX" --account '<ACCOUNT_ADDRESS>'
```

Decode matching events from one exact rooted transaction:

```sh
mithril-agent program decode-event \
  --workspace "$WORKSPACE" --sha256 "$IDL_SHA256" \
  --event-type '<IDL_EVENT_TYPE>' \
  --index-dir "$ACTIVITY_INDEX" --signature '<TRANSACTION_SIGNATURE>'
```

Decoded account and event output carries provenance and finality explicitly.
Index-backed values are rooted. Owner-history account output is historical and
does not claim current state; raw local data files are labelled unverified.

## 5. Build or simulate without a key

`program build` produces deterministic unsigned bytes from the pinned
instruction, public account bindings, public fee payer, and recent blockhash.
Its normal output reviews the fee payer, each bound account's writable/signer
role, and each argument before showing the exact message and transaction hashes.
Run `mithril-agent program build --help` for its exact flags.

`program simulate` fetches a fresh blockhash from the loopback Mithril node,
builds the same unsigned call, verifies the node's cluster and context, and
simulates without signing or submission:

```sh
mithril-agent program simulate \
  --workspace "$WORKSPACE" --sha256 "$IDL_SHA256" \
  --instruction '<IDL_INSTRUCTION>' --fee-payer '<PUBLIC_ADDRESS>' \
  --account '<IDL_ACCOUNT_NAME>=<ACCOUNT_ADDRESS>' \
  --arg '<IDL_ARGUMENT_NAME>=<JSON_VALUE>' \
  --min-context-slot "$MIN_SLOT"
```

Omit `--arg` for a no-argument instruction and repeat `--account` or `--arg`
when the pinned IDL requires more than one. The command refuses missing,
unknown, duplicated, wrongly typed, or additional-signer bindings.

The official Memo program is the one reviewed exception for a Codama
instruction with dynamic remaining accounts. Pin its exact current Codama 1.x
IDL, use instruction `addMemo`, pass no `--account` values, and bind one JSON
string with `--arg 'memo="your text"'`. The adapter accepts at most 566 UTF-8
bytes and produces the same unsigned build/simulation evidence. Optional Memo
signer accounts, every other dynamic-account instruction, signing, and
submission remain disabled.

## 6. Use the same checks through local MCP

A workspace with both verified rooted indexes exposes seven bounded tools:
interface summary, unsigned build, walletless simulation, live account read,
local instruction decode, rooted owner-history account decode, and rooted event decode. A
workspace created without an AccountsDB root is deliberately simulation-only:
it exposes the first five and does not advertise rooted tools. Neither profile
accepts a caller-supplied file or RPC path. Live read and simulation are
restricted to the workspace's literal-loopback Mithril node and label results
`processed`; index decoders use only the workspace's verified rooted query
snapshots and preserve the feed's exact Alpenglow-native or classic-finalized
provenance. The rooted owner-history result explicitly returns `current: false`;
the live account tool is the workspace's current-state path. The interface summary includes only verified metadata
and the bounded reviewer-written summary for pinned repository, decompiler, and
simulation evidence: hashes and byte size plus the approved decision, reviewer,
source revision, tool version, and applicable interface/message/context
binding. It never returns artifact contents or filesystem paths. Responses are
capped at 256 KiB;
use the local CLI for a larger reviewed result.

The MCP process cannot fetch or change an interface pin, write an index, load a
wallet, sign, submit, or listen on the network. Pinning and ingest remain
explicit operator actions. It runs at most four tool calls concurrently and
immediately refuses additional calls; clients may retry after an active call
finishes.

## What success proves

- Mithril supplied processed live reads and simulation with their exact context
  slots, while the custom index supplied separately labelled rooted state and
  transaction evidence.
- The local index preserved its cursor, lineage, and content hashes.
- Decoding and construction used one exact pinned interface.
- A local MCP client could reuse those same checks without gaining a new path
  to files, RPC endpoints, signing, or submission.
- No explorer, wallet application, private key, signature, or submission was
  involved.

A real state-changing Solana transaction still needs the fee payer and every
required authority to sign. That is a Solana protocol rule; configure the
optional isolated execution profile only after its program and policy are
separately reviewed.

## Mainnet rehearsal without funds

Use `CLUSTER='mainnet-beta'`, omit `--genesis-hash`, and use a healthy Mainnet
Mithril node to repeat the workspace, pin, and `program simulate` flow. Include
`--accounts` and backfill both indexes to test the classic finalized rooted
path; omit `--accounts` for a deliberately simulation-only rehearsal. The fee
payer flag is only a public address in either profile: simulation disables signature verification,
loads no key, submits nothing, and cannot spend funds. Choose accounts that make
the reviewed instruction succeed in simulation. A passing isolated test exists
in the repository, but production acceptance still requires the same command
against the intended fully soaked node.
