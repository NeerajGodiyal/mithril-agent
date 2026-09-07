package pricesource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

// KrakenTickerIdentitySHA256 identifies a Ticker BBO HTTP response snapshot,
// not a price-event timestamp or the older PreTrade method.
func KrakenTickerIdentitySHA256(feed string) (string, error) {
	if !krakenTickerFeed(feed) {
		return "", errors.New("Kraken ticker feed is unsupported")
	}
	hash := sha256.Sum256([]byte("mithril-agent/price-source-v1|kraken-spot|api.kraken.com|" + feed + "|ticker|bbo|http-response-date"))
	return hex.EncodeToString(hash[:]), nil
}

func krakenTickerFeed(feed string) bool {
	return feed == pricetrigger.FeedSOLUSD || feed == pricetrigger.FeedJUPUSD || feed == pricetrigger.FeedUSDCUSD
}

// KrakenTickerBatch fetches one immutable snapshot per explicit observation.
// Call BeginObservation before a new observation, after all previous readers
// have joined. It has no TTL and never renews a retained response's date.
type KrakenTickerBatch struct {
	client *http.Client
	gate   krakenRequestGate
	feeds  []string
	state  atomic.Pointer[krakenTickerState]
}

type krakenTickerState struct {
	once    sync.Once
	samples map[string]pricetrigger.Sample
	err     error
}

// NewKrakenTickerBatch copies a nonempty set of at most three supported feeds.
// The initial observation also supports startup checks before the first loop.
func NewKrakenTickerBatch(client *http.Client, feeds []string) (*KrakenTickerBatch, error) {
	if len(feeds) == 0 || len(feeds) > 3 {
		return nil, errors.New("Kraken ticker feeds are invalid")
	}
	owned := append([]string(nil), feeds...)
	sort.Strings(owned)
	for i, feed := range owned {
		if !krakenTickerFeed(feed) || i > 0 && owned[i-1] == feed {
			return nil, errors.New("Kraken ticker feeds are invalid")
		}
	}
	b := &KrakenTickerBatch{client: boundedClient(client), gate: newKrakenRequestGate(os.Getenv(KrakenRateStateEnvironment)), feeds: owned}
	b.BeginObservation()
	return b, nil
}

// BeginObservation discards previous success or failure without making requests.
// Concurrent readers already in flight finish against their original snapshot.
func (b *KrakenTickerBatch) BeginObservation() {
	if b != nil {
		b.state.Store(&krakenTickerState{})
	}
}

// KrakenTickerReader exposes one exact feed of its batch's current observation.
type KrakenTickerReader struct {
	batch          *KrakenTickerBatch
	feed, identity string
}

// Reader returns a reader only for a feed included at construction.
func (b *KrakenTickerBatch) Reader(feed string) (*KrakenTickerReader, error) {
	if b == nil {
		return nil, errors.New("Kraken ticker batch is missing")
	}
	i := sort.SearchStrings(b.feeds, feed)
	if i == len(b.feeds) || b.feeds[i] != feed {
		return nil, errors.New("Kraken ticker feed was not requested")
	}
	identity, err := KrakenTickerIdentitySHA256(feed)
	if err != nil {
		return nil, err
	}
	return &KrakenTickerReader{batch: b, feed: feed, identity: identity}, nil
}

// IdentitySHA256 returns the method-bound identity for this reader's feed.
func (r *KrakenTickerReader) IdentitySHA256() string {
	if r == nil {
		return ""
	}
	return r.identity
}

// Latest retains the original HTTP Date, including when read repeatedly.
// A failure is memoized until BeginObservation; no older snapshot is reused.
// Concurrent callers join the initiating caller's fetch and context; a canceled
// waiter returns its cancellation after that fetch joins, not before.
func (r *KrakenTickerReader) Latest(ctx context.Context, feed string) (pricetrigger.Sample, error) {
	if r == nil || r.batch == nil || feed != r.feed {
		return pricetrigger.Sample{}, errors.New("Kraken ticker reader feed is invalid")
	}
	if err := ctx.Err(); err != nil {
		return pricetrigger.Sample{}, err
	}
	state := r.batch.state.Load()
	state.once.Do(func() { state.samples, state.err = r.batch.fetch(ctx) })
	if err := ctx.Err(); err != nil {
		return pricetrigger.Sample{}, err
	}
	if state.err != nil {
		return pricetrigger.Sample{}, state.err
	}
	return state.samples[feed], nil
}

func (b *KrakenTickerBatch) fetch(ctx context.Context) (map[string]pricetrigger.Sample, error) {
	if err := b.gate.Wait(ctx); err != nil {
		return nil, errors.New("Kraken ticker request was rate-limited locally")
	}
	pairs := make([]string, len(b.feeds))
	for i, feed := range b.feeds {
		pairs[i] = strings.ReplaceAll(feed, "/", "")
	}
	query := url.Values{"pair": {strings.Join(pairs, ",")}, "assetVersion": {"1"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, KrakenOrigin+"/0/public/Ticker?"+query.Encode(), nil)
	if err != nil {
		return nil, errors.New("create Kraken ticker request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "mithril-agent/0.1")
	var response struct {
		Error  []string `json:"error"`
		Result map[string]struct {
			Ask []string `json:"a"`
			Bid []string `json:"b"`
		} `json:"result"`
	}
	headers, err := readJSON(ctx, b.client, request, &response)
	if err != nil {
		return nil, errors.New("Kraken ticker is unavailable")
	}
	date, err := http.ParseTime(headers.Get("Date"))
	if err != nil || len(response.Error) != 0 || len(response.Result) != len(b.feeds) {
		return nil, errors.New("Kraken ticker response is invalid")
	}
	samples := make(map[string]pricetrigger.Sample, len(b.feeds))
	for _, feed := range b.feeds {
		level, ok := response.Result[feed]
		if !ok || len(level.Ask) != 3 || len(level.Bid) != 3 {
			return nil, errors.New("Kraken ticker response is invalid")
		}
		lower, _, lowerErr := decimalMicros(level.Bid[0])
		upper, rounded, upperErr := decimalMicros(level.Ask[0])
		if lowerErr != nil || upperErr != nil || lower == 0 || upper == 0 || lower > pricetrigger.MaxPriceMicros || upper > pricetrigger.MaxPriceMicros {
			return nil, errors.New("Kraken ticker response is invalid")
		}
		if rounded {
			if upper == pricetrigger.MaxPriceMicros {
				return nil, errors.New("Kraken ticker response is invalid")
			}
			upper++
		}
		if upper < lower {
			return nil, errors.New("Kraken ticker response is invalid")
		}
		price := lower + (upper-lower)/2
		confidence := max(price-lower, upper-price, uint64(1))
		if confidence >= price {
			return nil, errors.New("Kraken ticker response is invalid")
		}
		identity, err := KrakenTickerIdentitySHA256(feed)
		if err != nil {
			return nil, err
		}
		samples[feed] = pricetrigger.Sample{SourceSHA256: identity, Feed: feed, PriceMicros: price, ConfidenceMicros: confidence, PublishedAt: date.UTC()}
	}
	return samples, nil
}
