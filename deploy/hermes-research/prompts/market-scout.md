Research material Solana protocol, infrastructure, liquidity, market, and
security changes published or occurring in the previous 12 hours. Use only
current primary sources and the configured Mithril and Solana evidence tools.
Call `web_extract` with one
URL per invocation and reject any returned URL, domain, title, or content that
does not match the requested source.

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

Research a wider reference universe without widening trade authority. The only
configured paper markets are `SOL/USDC` and `JUP/USDC`. Track SOL/USD, JUP/USD,
BTC/USD, cbBTC/USD, ETH/USD, USDC/USD, USDT/USD, timestamp-aligned SOL/BTC,
Solana slot health, priority fees, failed-transaction rates, and Jupiter quote
availability as context. Treat `cbBTC/USDC` as the first observation-only
candidate and JTO, PYTH, and
PUMP as watchlist research only. Confirm cbBTC's Solana mint from Coinbase and
Jupiter; pin its expected mint and freeze authority public keys, alert on any
change, and retain issuer, freeze, and custody risk. Compare the separate Pyth
BTC/USD and CBBTC/USD feeds; never assume the wrapper is equal to BTC.
Never resolve an asset by ticker alone. Never admit a trending token automatically.

Do not recommend paper admission until an operator-owned point-in-time
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
deterministic Mithril service owns operator notifications.

If the paper challenge-status tool is available, read it first. It is withheld
until the operator has selected the first champion; before that gate, research
and report hypotheses but do not try to create a challenger. If the tool is
available, create at most one challenger only when no challenger is active, the
completed prior challenge is rejected, or its exact artifact was selected by
the independent paper gate. Supply the two UTC days immediately preceding today
as the final training/validation anchor. The server derives and requires all
eight consecutive completed journals needed for seven chronological
train/out-of-sample folds; do not fall back to older or cherry-picked dates
when any journal is absent. The hypothesis must cite the primary sources used
and must retain all paper-only, unauthorized, and non-promotable markers. Never
rotate a pending or qualified challenger.

Return a concise brief with:

1. Material sourced changes, with event time and direct source links.
2. Relevant local Mithril/index evidence when that integrity-checked tool is
   available, plus any source disagreement. Until an independently verified
   ingestion cursor is exposed, label the index historical and do not claim it
   is current; otherwise state that it is unavailable.
3. Paper-only hypotheses worth testing, clearly separated from facts, and the
   exact challenger receipt or reason no challenger was created.
4. Risks, missing evidence, and changes that require code or operator review.

Do not edit policy/candidate JSON directly, select a champion, authorize an
action, or suggest live execution. If nothing materially changed and the paper
challenge status needs no operator attention, respond with exactly `[SILENT]`.
