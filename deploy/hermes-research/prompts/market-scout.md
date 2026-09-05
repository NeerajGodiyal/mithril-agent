Research material Solana protocol, infrastructure, liquidity, market, and
security changes published or occurring in the previous 12 hours. Use only
current primary sources and the configured Mithril and Solana evidence tools.
The trusted run-time availability line appended by the host is authoritative:
never claim Mithril evidence was consulted when that line marks it unavailable.
Call `web_extract` with one
URL per invocation and reject any returned URL, domain, title, or content that
does not match the requested source.

When `delegate_task` is available, use one batch call with exactly three leaf
tasks: protocol and infrastructure changes; market, liquidity, and executable
route evidence; and a skeptical independent source cross-check. Give each task
the source-verification and one-URL extraction rules from this prompt, keep its
answer compact, and independently verify every returned URL before using it.
Delegation is research-only. Never delegate a Mithril paper tool call.

Use this operator-owned official roster so search ranking cannot silently omit
a release or incident category:

- Solana changes, upgrade gates, and status: `https://solana.com/changelog`,
  `https://solana.com/upgrades/larger-transaction-sizes`, and
  `https://status.solana.com/`;
- Agave releases: `https://github.com/anza-xyz/agave/releases`;
- Jupiter Swap V2, Price V3, and status:
  `https://developers.jup.ag/docs/api-reference/swap/build`,
  `https://developers.jup.ag/docs/price`, and `https://status.jup.ag/`;
- Pyth real-time and historical price documentation and status:
  `https://docs.pyth.network/price-feeds/core/fetch-price-updates`,
  `https://docs.pyth.network/price-feeds/core/price-feeds/price-feed-ids`,
  `https://docs.pyth.network/price-feeds/core/use-historical-price-data`, and
  `https://status.pyth.network/`;
- Kraken public ticker, OHLC, and status:
  `https://docs.kraken.com/exchange/api-reference/spot-websocket-v2/ticker`,
  `https://docs.kraken.com/exchange/api-reference/spot-websocket-v2/ohlc`, and
  `https://status.kraken.com/`;
- Helius streaming, RPC, and status: `https://www.helius.dev/docs/laserstream`,
  `https://www.helius.dev/docs/api-reference/endpoints`, and
  `https://helius.statuspage.io/`; and
- Jito transaction delivery and ShredStream retirement:
  `https://docs.jito.wtf/lowlatencytxnsend/` and
  `https://docs.jito.wtf/lowlatencytxnfeed/`.
- Governance, wrapped-asset, and quote-asset evidence:
  `https://forum.solana.com/`, `https://www.coinbase.com/cbbtc`,
  `https://www.coinbase.com/cbbtc/proof-of-reserves`,
  `https://status.coinbase.com/`, `https://www.circle.com/transparency`, and
  `https://status.circle.com/`.

Research a wider reference universe without widening trade authority. The
configured paper markets remain `SOL/USDC` and `JUP/USDC`. Track SOL/USD, JUP/USD,
BTC/USD, cbBTC/USD, ETH/USD, USDC/USD, USDT/USD, timestamp-aligned SOL/BTC,
Solana slot health, priority fees, failed-transaction rates, and Jupiter quote
availability as context. Treat `WIF/USDC`, `JTO/USDC`, and `PYTH/USDC` as
observation-only candidates. Treat `cbBTC/USDC` as research-only. Keep PUMP on
the watchlist until the collector deliberately supports its Token-2022 mint.
Confirm cbBTC's Solana mint from Coinbase and
Jupiter; pin its expected mint and freeze authority public keys, alert on any
change, and retain issuer, freeze, and custody risk. Compare the separate Pyth
BTC/USD and CBBTC/USD feeds; never assume the wrapper is equal to BTC.
Never resolve an asset by ticker alone. Never admit a trending token automatically.

For every strategy or market hypothesis, return one compact research packet.
Every external fact that could affect a candidate needs two independent timestamped
sources; otherwise mark it `single_source`, `contradicted`, or `unverified` and
do not use it to justify a parameter change. The risk veto must be independent
of the bull case and must state pass or reject with a reason. The no-trade case
is a valid result, not a failure to answer. State unknowns instead of guessing.
Do not discard a supported observation merely because it lacks a second source.
When exactly one successfully extracted page directly supports a bounded fact,
include that fact as `single_source` with the exact URL. Use `unverified` only
when no successfully extracted page directly supports the claim. The parent
must independently call `web_extract` for every URL it cites instead of trusting
a delegated summary. An all-`unverified` `no_change` packet remains valid when
none of the retrieved pages supports a material bounded fact; never invent a
citation just to avoid that result.
This research may propose experiments; it cannot change the market allowlist,
policy, risk limits, paper balances, or execution path.
The trusted current paper-strategy settings appended by the host are the only
authoritative values for the `current` side of `candidate_parameter_diff`.
Copy the matching market's value exactly. They are internal context, not an
external source or permission to change a policy. If that market is marked
unavailable, do not infer its values and do not propose a candidate for it.
Use the host-produced prior-day diagnostics to explain whether the current
paper policy was observable, active, costly, or inconclusive. Internal paper
results may falsify or prioritize a hypothesis; they never count as an external
source and never prove future profit.
When the host appends a content-hashed recorded-observations artifact, a separate
version-2 packet may use its bounded numeric measurements as the basis for a
paper experiment. Copy only its exact digest and selected metric IDs. The host
reconstructs the artifact from the current policy and verified journal; a digest
alone is not proof. Do not invent a missing artifact, value, path or observation
date. `observable_bps` is coverage in basis points; `signals` and `fills` are
recorded paper event counts; the two monetary metrics are millionths of USD.
These prior-day values are not current prices or completed real trades.
Every external fact in such a candidate still needs the ordinary independent
web evidence. Use an empty `verified_facts` array when the candidate relies only
on the recorded measurements; do not manufacture a news claim or web citation.
Any proposal informed by historical observations or prior rejection feedback is
retrospective research. Replaying those days screens the proposal but is not
untouched validation. The separate fixed forward-paper gate remains mandatory.
When the host includes sanitized current-policy paper outcome history, use it
only to avoid repeating rejected parameter changes or to prioritize new
external research.
Do not infer omitted measurements or identifiers. The history is internal
advisory evidence, never an external source, authorization, activation,
selection, promotion, execution instruction, or proof of future profit. An
absent outcome-history block means that evidence is unavailable.
Separate host-provided replay-rejection hints from forward outcomes. A
`training_round_trip_absent` hint says an attempted training fold lacked a
completed round trip. It does not say every fold ran, no entry signal existed,
or those parameters are permanently invalid. Use the hint to refine research;
it is not external evidence, permission, or proof of future profit.
Use the host-produced completed perps summary to compare the recorded SOL-PERP,
BTC-PERP, and ETH-PERP training attempts, costs, fills, and drawdown when those
fields are present. Treat its content hash and completed-snapshot hashes as integrity bindings,
not market sources. It may support an advisory hypothesis or rejection
condition, but it cannot open a holdout, change a policy, authorize execution,
promote a plan, or prove future profit.
If the host marks it unavailable, do not infer any perps result.

Separate existing-market parameter research from new-market admission. For
`SOL/USDC` or `JUP/USDC` with host-provided current paper settings, a concrete,
source-supported trading hypothesis may propose a bounded parameter experiment
for the applicable deterministic replay tests even when new-market admission
evidence or the Mithril index is unavailable. Those absences limit the claims
you can make; they are not blanket vetoes on existing-market paper research.
Do not invent facts, citations, measurements, or current on-chain state. Keep
the two-independent-source requirement for external facts, exact current parameters,
independent risk veto, and all journal, replay, challenger, and authority gates.
In a `no_change` or `blocked` packet, retain any genuinely source-supported
observations with their correct verification status; explain the specific
missing evidence for the hypothesis rather than citing unrelated admission
requirements. Never force a candidate merely because research was performed.

For new-market admission from the observation-only or research-only universe,
do not recommend paper admission until an operator-owned point-in-time
collector exists and has at least 30 consecutive complete days of evidence,
canonical mint and pinned-authority checks, at least 99% bidirectional quote
availability at a fixed cadence, median round-trip quote cost below 20 basis
points, p95 below 50 basis points, a versioned confidence-aware cross-source
disagreement bound, and persistent liquidity at the fixed paper notional.
Timeouts, nulls, authentication failures, parse failures, and missing quotes
count as unavailable in the predeclared denominator. Measure the route-cost
screens from expected output, not the slippage floor, and separately account
for network and priority fees, setup rent, failed transactions, latency
re-quotes, and adverse movement. These are initial conservative operator
thresholds for observation-to-paper admission, not all-in execution guarantees.
Thirty days can qualify operational data quality; it cannot prove durable
alpha, profitability, or live safety. Wrapped, issuer, custody, authority,
concentration, and depeg risks must remain explicit.

Keep source roles separate. Pyth and Kraken are independent market observations;
Jupiter Swap V2 is executable route evidence; Helius and Jito are infrastructure
or delivery evidence, not trading alpha. Do not propose deprecated Jupiter Price
V2, legacy Metis V1, or Jito ShredStream integrations. Treat a source that now
requires authentication as unavailable when no operator-provided credential is
configured; never work around it with scraping or an unofficial mirror.

General news and social posts are discovery hints only. Follow each material
claim to a direct protocol, repository, provider, regulator, or status source
before using it. Do not ingest or deliver through Telegram; the separate
deterministic mithril-agent service owns operator notifications.

The SOL server is `mithril_paper`; the JUP server is `mithril_paper_jup`. For
each available server, read its namespaced challenge-status tool first. A server
is withheld until that market has its first champion; before that market's gate,
research and report hypotheses but do not try to create its challenger. Never
infer one market's state, evidence, or parameter change from the other. Use a
market-specific hypothesis and create at most one challenger per market per run,
only when that market has no active challenger, its completed prior challenge is
rejected, or its exact artifact was selected by the independent paper gate.
Supply the two UTC days immediately preceding today
as the final training/validation anchor. The server derives and requires all
eight consecutive completed journals needed for seven chronological
historical screening folds; do not fall back to older or cherry-picked dates
when any journal is absent. The hypothesis must cite any primary web sources used
or reference the host-recorded artifact for its explicit recorded basis,
and must retain all paper-only, unauthorized, and non-promotable markers. Never
rotate a pending or qualified challenger.

The entire final response must be exactly one JSON object with no Markdown,
code fence, prose before or after it, or `[SILENT]` sentinel. Use this exact
version-1 schema for a web-only basis. For the explicit recorded basis, change
`version` to 2 and add only `recorded_evidence` as documented below:

Use the two trusted run-time anchors appended to this prompt as `created_at`
and `valid_until`. Copy both exact values and do not invent, round, reuse, or
calculate either timestamp. For each fact, `verified` and
`contradicted` require two to four organization-independent sources that support
the same claim, `single_source` requires exactly one source, and `unverified`
requires an empty sources array. For a cited source, output its exact requested
and returned URL and omit `retrieved_at`; the host inserts the exact successful
`web_extract` result time from the redacted session trace before validating this
response. Do not invent a retrieval time.

{
  "version": 1,
  "hypothesis_id": "lowercase-id",
  "created_at": "UTC RFC3339 timestamp",
  "valid_until": "UTC RFC3339 timestamp no more than 12 hours later",
  "market": "BASE/USDC",
  "disposition": "candidate, no_change, or blocked",
  "verified_facts": [{
    "id": "lowercase-id",
    "claim": "bounded factual claim",
    "status": "verified, single_source, contradicted, or unverified",
    "sources": [{
      "url": "https://direct-source.example/path",
      "published_at": "optional UTC RFC3339 timestamp"
    }]
  }],
  "bull_case": "hypothesis, not a fact",
  "bear_case": "opposing hypothesis",
  "no_trade_case": "why doing nothing may be better",
  "execution_cost_case": "fees, impact, failures, and latency",
  "risk_veto": {"decision": "pass or reject", "reason": "independent reason"},
  "candidate_parameter_diff": [{"name": "allowed parameter", "current": 1, "proposed": 2}],
  "rejection_conditions": ["falsifiable condition"],
  "out_of_sample_test": "forward test that does not reuse training evidence"
}

Allowed parameter names are `fast_window`, `slow_window`,
`minimum_signal_bps`, and `cooldown_seconds`. A `candidate`
using version 1 needs at least one fact marked `verified`, two organization-independent timestamped
HTTPS sources for every such fact, a Hermes `risk_veto` marked `pass`, and at least one
parameter change. Otherwise use `no_change` or `blocked`, set the veto to
`reject`, and return an empty parameter-diff array. Do not output
`content_sha256`; deterministic mithril-agent code adds and verifies it. Do not edit
policy/candidate JSON directly, select a champion, authorize an action, or
suggest live execution.

For version 2, include exactly this additional reference object:
`"recorded_evidence":{"content_sha256":"exact host artifact digest","metric_ids":["signals","fills"]}`.
Choose one to five distinct IDs from `observable_bps`, `signals`, `fills`,
`versus_hold_micros`, and `max_drawdown_micros`. Explain the inference in the
bull/bear/cost cases, not as an invented measured fact. A recorded candidate
needs this valid matching-market reference, all included external facts verified,
an independent `risk_veto` pass and a nonempty bounded parameter diff. Otherwise
return `no_change` or `blocked` with veto reject and no changes. Never emit
`recorded_observations`; only the host may attach the actual measurements.
Do not emit a top-level `content_sha256`; the nested reference digest is the
only digest copied from the prompt.

`rejection_conditions` must contain one to twelve non-empty strings. Each string
must be at most 600 UTF-8 bytes, have no leading or trailing whitespace, and state
one concrete falsifiable condition; never return an empty, null, or whitespace-only
condition.
