package main

import (
	"fmt"
	"io"
)

// explainText answers "what is this, what can it do, and what can move money"
// without needing config, a host, or a wallet, so a reviewer can read it
// before installing anything.
const explainText = `What this is
------------
A walletless Solana program-intelligence and automation CLI for a Mithril node.
Its default path can inspect programs and accounts, pin one reviewed interface,
build and simulate unsigned interactions, decode program data, maintain rooted
custom indexes, and expose the same bounded checks to a local MCP client. It
needs no browser wallet, signing key, or block explorer and cannot submit a
transaction.

What the walletless path can do
-------------------------------
  - Bind a private workspace to one program, cluster identity, loopback Mithril
    node, and exact interface hash.
  - Fetch a canonical published interface through Mithril or pin a reviewed
    local Solana IDL spec 0.1.0 or Codama 1.x file. Repository reviews,
    decompiler artifacts, and simulation results remain separate, hash-pinned evidence.
  - Label live reads and simulations with their processed context slot, and
    build custom account and transaction indexes from Mithril's native
    Alpenglow rooted feed or separately labelled classic finalized feed.
  - Decode instructions, accounts, and events and build deterministic unsigned
    messages without loading a key.
  - Give a local MCP-capable agent five bounded tools for a simulation-only
    workspace, or seven when rooted indexes are configured. MCP uses stdio and
    the operator's OS identity. It opens no network listener and accepts no caller-selected path or endpoint.

What the walletless path cannot do
----------------------------------
  - It cannot sign, submit, deploy, upgrade, or claim to understand an
    unfamiliar program from decompiler output alone. Construction requires one
    reviewed and pinned current interface.
  - Classic Devnet, Testnet, and Mainnet use finalized-only evidence, never the
    native Alpenglow label. Enabling that opt-in feed requires a fresh or
    separate AccountsDB and the mandatory local finality verifier.
  - Program MCP cannot fetch or change an interface, write an index, load a
    wallet, or listen on the network. Pinning and ingest remain explicit
    operator actions.
  - No language model can approve, sign, or submit anything. Agent output is a
    request for deterministic tools, never transaction authority.

Optional bounded trading modules
--------------------------------
The separate Devnet profile can perform one bounded demo swap, or explicitly
configured sell, buy, and sweep legs on one fixed, pre-reviewed Orca pool.
Devnet tokens have no monetary value. A human sets the rule and grants a short
window with a maximum action count; an isolated signer applies independent
daily caps. The runner has no built-in market strategy and always restarts stopped.
Configured legs may act automatically only inside that bounded window, action
count, schedule, and the signer's daily caps.

The installed strategy runner cannot trade on Mainnet. A package-only Mainnet
canary boundary is tested offline, but no command or service can submit through
it; no operational Mainnet execution path is enabled. Telegram and the trading
MCP can only read and report. There is no message or model output that can
start, authorize, price, approve, sign, submit, or confirm a trade.

Read-only Mainnet tools
-----------------------
Shadow mode holds no wallet signing key. It watches a real market and writes
down what the rule would have done, so the strategy can be judged on live data
before anyone risks anything. It has no code path to a signature. That is
enforced by the structure of the program — the shadow code declares no signer,
no submitter, and no field that could name a wallet signing key — and two tests
fail the build if that ever stops being true.

Its report always states how much of the period it could actually see. A
profitable-looking day in which a third of the market was unreadable is not a
result, and the report says so before it shows you a number. The policy also
binds the quote venue and token pair. Shadow review verifies consecutive,
complete Mainnet days together, but deliberately does not approve a strategy.

Proposal check builds one narrow Jupiter swap candidate and independently
checks its exact bytes, fees, lookup tables, lifetime, and Mithril simulation.
Recheck and prepare repeat those checks and produce an unsigned, ungranted
request. These commands hold no wallet signing key, cannot authorize,
cannot sign, and cannot submit a transaction. Jupiter reads may use a provider
API key.

Where the optional money boundary is
------------------------------------
The signing key is held by a separate signer process that re-decodes the exact
transaction bytes and applies its own independent limits before signing. The
Devnet runner receives the approved signature and already has the unsigned
message, so it could reconstruct that one policy-approved transaction. It
still cannot read the signing key or ask the signer to bypass its limits.

For this Devnet pilot the runner, risk authority, signer, and submitter use
separate operating-system identities. Narrow local sockets connect them; the
runner cannot open their keys. These host-local permission boundaries provide
fault isolation, not an adversarial boundary against a compromised runner or
an independently qualified production custody boundary. Real capital still
needs separately designed and qualified custody.

How to check the claims yourself
--------------------------------
  make verify-source                        every file matches the manifest
  make test                                 full suite, race detector, vet
  WALLETLESS_QUICKSTART.md                   no-wallet program workflow
  INDEXING.md                                rooted index and recovery rules
  mithril-agent program --help               program inspection and simulation
  mithril-agent index --help                 rooted custom indexing
  mithril-agent audit snapshot --config PATH status and audit chain agree
  mithril-agent journal verify --path PATH   lower-level chain verification
  mithril-agent shadow run --help            what the market observer does
  mithril-agent shadow review --help         how multi-day evidence is checked
`

func runExplain(args []string, output io.Writer) error {
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		_, err := fmt.Fprint(output, explainText)
		return err
	}
	if len(args) > 0 {
		return fmt.Errorf("explain takes no arguments")
	}
	_, err := fmt.Fprint(output, explainText)
	return err
}
