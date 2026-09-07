package pricesource

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

const tickerFixture = `{"error":[],"result":{"JUP/USD":{"a":["2.0000001","1","1"],"b":["1.9999999","1","1"]},"SOL/USD":{"a":["200.01","1","1"],"b":["199.99","1","1"]},"USDC/USD":{"a":["1.00009","1","1"],"b":["0.99991","1","1"]}}}`

func TestKrakenTickerBatchObservationLifecycle(t *testing.T) {
	date := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	var calls atomic.Int64
	var fail atomic.Bool
	client := fixtureClient(t, func(r *http.Request) string {
		calls.Add(1)
		if r.URL.Host != "api.kraken.com" || r.URL.Path != "/0/public/Ticker" || r.URL.Query().Get("pair") != "JUPUSD,SOLUSD,USDCUSD" || r.URL.Query().Get("assetVersion") != "1" {
			t.Error("incorrect ticker endpoint or pairs")
		}
		if fail.Load() {
			return `{"error":[],"result":{}}`
		}
		return tickerFixture
	}, http.Header{"Date": {date.Format(http.TimeFormat)}})
	feeds := []string{pricetrigger.FeedSOLUSD, pricetrigger.FeedJUPUSD, pricetrigger.FeedUSDCUSD}
	b, err := NewKrakenTickerBatch(client, feeds)
	if err != nil {
		t.Fatal(err)
	}
	b.gate = testKrakenGate{}
	feeds[0] = "mutated"
	var wg sync.WaitGroup
	for _, feed := range []string{pricetrigger.FeedSOLUSD, pricetrigger.FeedJUPUSD, pricetrigger.FeedUSDCUSD} {
		r, err := b.Reader(feed)
		if err != nil {
			t.Fatal(err)
		}
		wg.Go(func() {
			for range 3 {
				s, err := r.Latest(t.Context(), feed)
				id, _ := KrakenTickerIdentitySHA256(feed)
				if err != nil || s.SourceSHA256 != id || !s.PublishedAt.Equal(date) || s.Feed != feed {
					t.Error("snapshot identity/date changed")
				}
				if feed == pricetrigger.FeedJUPUSD && (s.PriceMicros != 2_000_000 || s.ConfidenceMicros != 1) {
					t.Error("conservative rounding changed")
				}
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("batch fetched more than once")
	}
	r, _ := b.Reader(pricetrigger.FeedSOLUSD)
	fail.Store(true)
	b.BeginObservation()
	for range 2 {
		if s, err := r.Latest(t.Context(), pricetrigger.FeedSOLUSD); err == nil || s.PriceMicros != 0 {
			t.Fatal("failed batch reused old value")
		}
	}
	if calls.Load() != 2 {
		t.Fatal("failure not memoized")
	}
	fail.Store(false)
	b.BeginObservation()
	if _, err := r.Latest(t.Context(), pricetrigger.FeedSOLUSD); err != nil || calls.Load() != 3 {
		t.Fatal("explicit refresh failed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	b.BeginObservation()
	if _, err := r.Latest(ctx, pricetrigger.FeedSOLUSD); err == nil || calls.Load() != 3 {
		t.Fatal("canceled read issued a request")
	}
	if _, err := r.Latest(t.Context(), pricetrigger.FeedJUPUSD); err == nil {
		t.Fatal("wrong reader feed accepted")
	}
}

func TestKrakenTickerRejectsMalformedBatch(t *testing.T) {
	for _, tc := range []struct{ name, body, date string }{
		{"missing", `{"error":[],"result":{}}`, time.Now().UTC().Format(http.TimeFormat)},
		{"mapping", strings.Replace(tickerFixture, "JUP/USD", "JUPUSD", 1), time.Now().UTC().Format(http.TimeFormat)},
		{"crossed", strings.Replace(tickerFixture, "1.9999999", "3", 1), time.Now().UTC().Format(http.TimeFormat)},
		{"decimal", strings.Replace(tickerFixture, "2.0000001", "NaN", 1), time.Now().UTC().Format(http.TimeFormat)},
		{"date", tickerFixture, "invalid"},
		{"provider error", strings.Replace(tickerFixture, `"error":[]`, `"error":["untrusted"]`, 1), time.Now().UTC().Format(http.TimeFormat)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := NewKrakenTickerBatch(fixtureClient(t, func(*http.Request) string { return tc.body }, http.Header{"Date": {tc.date}}), []string{pricetrigger.FeedSOLUSD, pricetrigger.FeedJUPUSD, pricetrigger.FeedUSDCUSD})
			if err != nil {
				t.Fatal(err)
			}
			b.gate = testKrakenGate{}
			r, _ := b.Reader(pricetrigger.FeedSOLUSD)
			if s, err := r.Latest(t.Context(), pricetrigger.FeedSOLUSD); err == nil || s.PriceMicros != 0 {
				t.Fatal("malformed batch accepted")
			}
		})
	}
}

func TestKrakenTickerIdentityAndFeedBounds(t *testing.T) {
	var missing *KrakenTickerReader
	if missing.IdentitySHA256() != "" {
		t.Fatal("nil reader returned an identity")
	}
	if _, err := missing.Latest(t.Context(), pricetrigger.FeedSOLUSD); err == nil {
		t.Fatal("nil reader accepted a read")
	}
	for _, feeds := range [][]string{nil, {"unknown"}, {pricetrigger.FeedSOLUSD, pricetrigger.FeedSOLUSD}} {
		if _, err := NewKrakenTickerBatch(nil, feeds); err == nil {
			t.Fatal("invalid feed set accepted")
		}
	}
	seen := map[string]bool{}
	for _, feed := range []string{pricetrigger.FeedSOLUSD, pricetrigger.FeedJUPUSD, pricetrigger.FeedUSDCUSD} {
		id, err := KrakenTickerIdentitySHA256(feed)
		if err != nil || len(id) != 64 || seen[id] || id == KrakenIdentitySHA256() || id == KrakenSOLIdentitySHA256() || id == KrakenJUPIdentitySHA256() {
			t.Fatal("ticker identity is not method/feed bound")
		}
		seen[id] = true
	}
}
