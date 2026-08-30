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
- Jupiter documentation and status: `https://developers.jup.ag/docs/swap/index`
  and `https://status.jup.ag/`;
- Pyth documentation and status: `https://docs.pyth.network/price-feeds/core/best-practices`
  and `https://status.pyth.network/`; and
- Kraken API documentation and status: `https://docs.kraken.com/api-reference/transparency/pre-trade-data`
  and `https://status.kraken.com/`.

General news and social posts are discovery hints only. Follow each material
claim to a direct protocol, repository, provider, regulator, or status source
before using it. Do not ingest or deliver through Telegram; the separate
deterministic Mithril service owns operator notifications.

If the paper challenge-status tool is available, read it first. It is withheld
until the operator has selected the first champion; before that gate, research
and report hypotheses but do not try to create a challenger. If the tool is
available, create at most one challenger only when no challenger is active, the
completed prior challenge is rejected, or its exact artifact was promoted by
the operator, and only when the two UTC days immediately preceding today
provide the training and validation journals. Do not fall back to older or
cherry-picked dates when either journal is absent. The hypothesis must cite the
primary sources used and must retain all paper-only, unauthorized, and
non-promotable markers. Never rotate a pending or qualified challenger.

Return a concise brief with:

1. Material sourced changes, with event time and direct source links.
2. Relevant local Mithril/index evidence when that verified tool is available,
   plus any source disagreement; otherwise state that it is unavailable.
3. Paper-only hypotheses worth testing, clearly separated from facts, and the
   exact challenger receipt or reason no challenger was created.
4. Risks, missing evidence, and changes that require code or operator review.

Do not edit policy/candidate JSON directly, select a champion, authorize an
action, or suggest live execution. If nothing materially changed and the paper
challenge status needs no operator attention, respond with exactly `[SILENT]`.
