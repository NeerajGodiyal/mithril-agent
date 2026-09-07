package main

import (
	"errors"

	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func paperUsesKrakenTicker(policy shadow.Policy) bool {
	if policy.Cluster != shadow.Mainnet || policy.Version == shadow.AdmittedVersion {
		return false
	}
	feed := pricetrigger.FeedSOLUSD
	switch policy.Market {
	case shadow.MarketSOLUSDC:
	case shadow.MarketJUPUSDC:
		feed = pricetrigger.FeedJUPUSD
	default:
		return false
	}
	identity, err := pricesource.KrakenTickerIdentitySHA256(feed)
	return err == nil && policy.Trigger.Feed == feed && policy.Trigger.SecondarySourceSHA256 == identity
}

// newPaperTickerReaders binds one batch to the market, peg and optional fee
// feeds. Construction performs no reads; the caller owns observation boundaries.
func newPaperTickerReaders(policy shadow.Policy) (*pricesource.KrakenTickerBatch, [3]shadow.PriceReader, error) {
	var readers [3]shadow.PriceReader
	if err := validatePaperPolicySources(policy); err != nil {
		return nil, readers, err
	}
	if !paperUsesKrakenTicker(policy) {
		return nil, readers, errors.New("policy does not select the ticker batch source")
	}
	feeds := []string{policy.Trigger.Feed, policy.QuotePeg.Feed}
	if policy.NativeFeePrice != nil {
		feeds = append(feeds, policy.NativeFeePrice.Feed)
	}
	batch, err := pricesource.NewKrakenTickerBatch(nil, feeds)
	if err != nil {
		return nil, readers, err
	}
	for index, feed := range feeds {
		reader, err := batch.Reader(feed)
		if err != nil {
			return nil, [3]shadow.PriceReader{}, err
		}
		readers[index] = reader
	}
	return batch, readers, nil
}
