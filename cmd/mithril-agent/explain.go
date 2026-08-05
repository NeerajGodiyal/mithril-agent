package main

import (
	"fmt"
	"io"
)

// explainText answers "what is this and what can it do to my money" without
// needing config, a host, or a wallet, so a reviewer can read it before
// installing anything.
const explainText = `What this is
------------
A deterministic program that performs ONE token swap on Solana Devnet, only
after an operator explicitly allows it, and then returns itself to a stopped
state. Devnet is a test network: the tokens on it have no monetary value.

What it can do
--------------
  - Swap between SOL and devUSDC on one fixed, pre-reviewed Orca pool.
  - Perform at most ONE swap per explicit grant. The grant is short-lived and
    is consumed by the attempt, not by success.
  - Wait for an optional price condition before acting, then re-check the
    market and the route again immediately before sending.
  - Report what it did: status, last trade, current price rule, and a sealed
    audit record you can verify independently.
  - Watch a live market, including Mainnet, and record what the rule WOULD have
    done — see "The one thing that looks at Mainnet" below.

What it cannot do
-----------------
  - Its trading side cannot touch Mainnet. There is no Mainnet execution path,
    and a wrong network stops it before anything is signed.
  - It cannot trade a token or venue other than the one fixed pair above.
  - It cannot act twice on one grant, or resume acting after a restart. It
    always restarts stopped.
  - It has no strategy. It does not decide what or when to trade; a human sets
    the rule and allows each action.
  - No language model can authorize, price, approve, sign, submit, or confirm
    anything. Model output is optional explanation text only.
  - Telegram and MCP can only read and report. There is no command, button, or
    message that can start a trade.

The one thing that looks at Mainnet
-----------------------------------
Shadow mode watches a real market and writes down what the rule would have
done, so the strategy can be judged on live data before anyone risks anything.
It cannot touch Mainnet in the sense that matters: it holds no key and there is
no code path from it to a signature. That is enforced by the structure of the
program — the shadow code declares no signer, no submitter, and no field that
could name a key — and two tests fail the build if that ever stops being true.

Its report always states how much of the period it could actually see. A
profitable-looking day in which a third of the market was unreadable is not a
result, and the report says so before it shows you a number.

Where the money boundary is
---------------------------
The signing key is held by a separate signer process that re-decodes the exact
transaction bytes and applies its own independent limits before signing. The
main runner never receives the raw signed transaction.

For this Devnet pilot those processes share one operating-system identity. That
gives fault isolation, NOT custody separation. Do not treat this arrangement as
safe for real funds; real capital needs a separately designed custody boundary.

How to check the claims yourself
--------------------------------
  make verify-source                        every file matches the manifest
  make test                                 full suite, race detector, vet
  mithril-agent journal verify --path PATH   the audit chain is intact
  mithril-agent shadow run --help            what the market observer does
`

func runExplain(args []string, output io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("explain takes no arguments")
	}
	_, err := fmt.Fprint(output, explainText)
	return err
}
