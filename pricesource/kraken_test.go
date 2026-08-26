package pricesource

import (
	"net/http"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

func TestKrakenUsesFreshSnapshotWithPersistentBidAsk(t *testing.T) {
	snapshotAt := time.Now().UTC().Truncate(time.Second)
	bidAt := snapshotAt.Add(-10 * time.Minute)
	askAt := snapshotAt.Add(-5 * time.Minute)
	client := fixtureClient(t, func(request *http.Request) string {
		if request.URL.Host != "api.kraken.com" || request.URL.Path != "/0/public/PreTrade" ||
			request.URL.Query().Get("symbol") != "USDC/USD" {
			t.Fatalf("request URL = %s", request.URL)
		}
		return `{"result":{"symbol":"USDC/USD","bids":[{"side":"BUY","price":"0.99991","publication_ts":"` +
			bidAt.Format(time.RFC3339Nano) + `"}],"asks":[{"side":"SELL","price":"1.00009","publication_ts":"` +
			askAt.Format(time.RFC3339Nano) + `"}]},"error":[]}`
	}, http.Header{"Date": []string{snapshotAt.Format(http.TimeFormat)}})
	sample, err := NewKraken(client).Latest(t.Context(), pricetrigger.FeedUSDCUSD)
	if err != nil {
		t.Fatal(err)
	}
	if sample.SourceSHA256 != KrakenIdentitySHA256() || sample.Feed != pricetrigger.FeedUSDCUSD ||
		sample.PriceMicros != 1_000_000 || sample.ConfidenceMicros != 90 ||
		!sample.PublishedAt.Equal(snapshotAt) {
		t.Fatalf("sample = %+v", sample)
	}
}

func TestKrakenRejectsMalformedMarketData(t *testing.T) {
	for _, body := range []string{
		`{"result":{"symbol":"USDC/USD","bids":[],"asks":[]},"error":[]}`,
		`{"result":{"symbol":"USDT/USD","bids":[{"side":"BUY","price":"1","publication_ts":"2026-08-12T00:00:00Z"}],"asks":[{"side":"SELL","price":"1","publication_ts":"2026-08-12T00:00:00Z"}]},"error":[]}`,
		`{"result":{"symbol":"USDC/USD","bids":[{"side":"BUY","price":"1.01","publication_ts":"bad"}],"asks":[{"side":"SELL","price":"1.00","publication_ts":"bad"}]},"error":[]}`,
		`{"result":{"symbol":"USDC/USD","bids":[{"side":"BUY","price":"0.9999","publication_ts":"2026-08-12T00:00:00Z"}],"asks":[{"side":"SELL","price":"1.0001","publication_ts":"2026-08-12T00:00:00Z"}]},"error":[]}`,
		`{"result":{"symbol":"USDC/USD"},"error":["provider failure"]}`,
	} {
		client := fixtureClient(t, func(*http.Request) string { return body })
		if _, err := NewKraken(client).Latest(t.Context(), pricetrigger.FeedUSDCUSD); err == nil {
			t.Fatalf("accepted market data %s", body)
		}
	}
	if _, err := NewKraken(fixtureClient(t, func(*http.Request) string { return `{}` })).Latest(
		t.Context(), pricetrigger.FeedSOLUSD,
	); err == nil {
		t.Fatal("unsupported feed succeeded")
	}
}
