package pricesource

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

type testKrakenGate struct{}

func (testKrakenGate) Wait(context.Context) error { return nil }

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
	source := NewKraken(client)
	source.gate = testKrakenGate{}
	sample, err := source.Latest(t.Context(), pricetrigger.FeedUSDCUSD)
	if err != nil {
		t.Fatal(err)
	}
	if sample.SourceSHA256 != KrakenIdentitySHA256() || sample.Feed != pricetrigger.FeedUSDCUSD ||
		sample.PriceMicros != 1_000_000 || sample.ConfidenceMicros != 90 ||
		!sample.PublishedAt.Equal(snapshotAt) {
		t.Fatalf("sample = %+v", sample)
	}
}

func TestKrakenRejectsTopLevelNewerThanSnapshot(t *testing.T) {
	snapshotAt := time.Now().UTC().Truncate(time.Second)
	levelAt := snapshotAt.Add(2 * time.Second)
	client := fixtureClient(t, func(*http.Request) string {
		return `{"result":{"symbol":"USDC/USD","bids":[{"side":"BUY","price":"0.9999","publication_ts":"` +
			levelAt.Format(time.RFC3339Nano) + `"}],"asks":[{"side":"SELL","price":"1.0001","publication_ts":"` +
			levelAt.Format(time.RFC3339Nano) + `"}]},"error":[]}`
	}, http.Header{"Date": []string{snapshotAt.Format(http.TimeFormat)}})
	source := NewKraken(client)
	source.gate = testKrakenGate{}
	if _, err := source.Latest(t.Context(), pricetrigger.FeedUSDCUSD); err == nil {
		t.Fatal("top level newer than the snapshot was accepted")
	}
}

func TestKrakenSOLUsesDistinctFeedIdentity(t *testing.T) {
	snapshotAt := time.Now().UTC().Truncate(time.Second)
	client := fixtureClient(t, func(request *http.Request) string {
		if request.URL.Host != "api.kraken.com" || request.URL.Path != "/0/public/PreTrade" ||
			request.URL.Query().Get("symbol") != "SOL/USD" {
			t.Fatalf("request URL = %s", request.URL)
		}
		return `{"result":{"symbol":"SOL/USD","bids":[{"side":"BUY","price":"199.99","publication_ts":"` +
			snapshotAt.Format(time.RFC3339Nano) + `"}],"asks":[{"side":"SELL","price":"200.01","publication_ts":"` +
			snapshotAt.Format(time.RFC3339Nano) + `"}]},"error":[]}`
	}, http.Header{"Date": []string{snapshotAt.Format(http.TimeFormat)}})
	source := NewKrakenSOL(client)
	source.gate = testKrakenGate{}
	sample, err := source.Latest(t.Context(), pricetrigger.FeedSOLUSD)
	if err != nil {
		t.Fatal(err)
	}
	if sample.SourceSHA256 != KrakenSOLIdentitySHA256() ||
		sample.SourceSHA256 == KrakenIdentitySHA256() || sample.Feed != pricetrigger.FeedSOLUSD ||
		sample.PriceMicros != 200_000_000 || sample.ConfidenceMicros != 10_000 {
		t.Fatalf("sample = %+v", sample)
	}
}

func TestKrakenJUPUsesItsPinnedProductAndIdentity(t *testing.T) {
	snapshotAt := time.Now().UTC().Truncate(time.Second)
	client := fixtureClient(t, func(request *http.Request) string {
		if request.URL.Host != "api.kraken.com" || request.URL.Path != "/0/public/PreTrade" ||
			request.URL.Query().Get("symbol") != "JUP/USD" {
			t.Fatalf("request URL = %s", request.URL)
		}
		return `{"result":{"symbol":"JUP/USD","bids":[{"side":"BUY","price":"0.4849","publication_ts":"` +
			snapshotAt.Format(time.RFC3339Nano) + `"}],"asks":[{"side":"SELL","price":"0.4851","publication_ts":"` +
			snapshotAt.Format(time.RFC3339Nano) + `"}]},"error":[]}`
	}, http.Header{"Date": []string{snapshotAt.Format(http.TimeFormat)}})
	source := NewKrakenJUP(client)
	source.gate = testKrakenGate{}
	sample, err := source.Latest(t.Context(), pricetrigger.FeedJUPUSD)
	if err != nil {
		t.Fatal(err)
	}
	if sample.SourceSHA256 != KrakenJUPIdentitySHA256() ||
		sample.SourceSHA256 == KrakenSOLIdentitySHA256() ||
		sample.Feed != pricetrigger.FeedJUPUSD || sample.PriceMicros != 485_000 ||
		sample.ConfidenceMicros != 100 {
		t.Fatalf("JUP sample = %+v", sample)
	}
}

func TestKrakenReadsAnAdmissionBoundProduct(t *testing.T) {
	now := time.Date(2026, 8, 31, 14, 25, 20, 0, time.UTC)
	client := fixtureClient(t, func(request *http.Request) string {
		if request.URL.Query().Get("symbol") != "WIF/USD" ||
			request.URL.RawQuery != "symbol=WIF%2FUSD" {
			t.Fatalf("request URL = %s", request.URL.String())
		}
		return `{"result":{"symbol":"WIF/USD","bids":[{"side":"BUY","price":"0.1940","publication_ts":"` +
			now.Format(time.RFC3339Nano) + `"}],"asks":[{"side":"SELL","price":"0.1941","publication_ts":"` +
			now.Format(time.RFC3339Nano) + `"}]},"error":[]}`
	}, http.Header{"Date": []string{now.Format(http.TimeFormat)}})
	source, err := NewKrakenFromSpec(client, KrakenSpec{Feed: "WIF/USD", Product: "WIF/USD"})
	if err != nil {
		t.Fatal(err)
	}
	source.gate = testKrakenGate{}
	sample, err := source.Latest(t.Context(), "WIF/USD")
	if err != nil {
		t.Fatal(err)
	}
	if sample.PriceMicros != 194_050 || sample.Feed != "WIF/USD" ||
		sample.SourceSHA256 != source.IdentitySHA256() ||
		sample.SourceSHA256 == KrakenJUPIdentitySHA256() {
		t.Fatalf("sample = %+v", sample)
	}
	for _, spec := range []KrakenSpec{
		{Feed: "wif/USD", Product: "WIF/USD"},
		{Feed: "WIF/USD", Product: "RAY/USD"},
	} {
		if _, err := NewKrakenFromSpec(client, spec); err == nil {
			t.Fatalf("invalid spec was accepted: %+v", spec)
		}
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
		source := NewKraken(client)
		source.gate = testKrakenGate{}
		if _, err := source.Latest(t.Context(), pricetrigger.FeedUSDCUSD); err == nil {
			t.Fatalf("accepted market data %s", body)
		}
	}
	source := NewKraken(fixtureClient(t, func(*http.Request) string { return `{}` }))
	source.gate = testKrakenGate{}
	if _, err := source.Latest(
		t.Context(), pricetrigger.FeedSOLUSD,
	); err == nil {
		t.Fatal("unsupported feed succeeded")
	}
}

func TestKrakenDefaultGateCoordinatesAdaptersAndHonorsCancellation(t *testing.T) {
	t.Setenv(KrakenRateStateEnvironment, "")
	oldGate := processKrakenRequestGate
	processKrakenRequestGate = &memoryKrakenRequestGate{spacing: 20 * time.Millisecond}
	t.Cleanup(func() { processKrakenRequestGate = oldGate })

	snapshotAt := time.Now().UTC().Truncate(time.Second)
	started := make(chan time.Time, 3)
	client := fixtureClient(t, func(request *http.Request) string {
		started <- time.Now()
		product := request.URL.Query().Get("symbol")
		return `{"result":{"symbol":"` + product +
			`","bids":[{"side":"BUY","price":"0.9999","publication_ts":"` +
			snapshotAt.Format(time.RFC3339Nano) +
			`"}],"asks":[{"side":"SELL","price":"1.0001","publication_ts":"` +
			snapshotAt.Format(time.RFC3339Nano) + `"}]},"error":[]}`
	}, http.Header{"Date": []string{snapshotAt.Format(http.TimeFormat)}})
	sol, usdc := NewKrakenSOL(client), NewKraken(client)
	if sol.gate != usdc.gate {
		t.Fatal("default Kraken adapters do not share a request gate")
	}
	if _, err := sol.Latest(t.Context(), pricetrigger.FeedSOLUSD); err != nil {
		t.Fatal(err)
	}
	first := <-started
	if _, err := usdc.Latest(t.Context(), pricetrigger.FeedUSDCUSD); err != nil {
		t.Fatal(err)
	}
	if delay := (<-started).Sub(first); delay < 15*time.Millisecond {
		t.Fatalf("default Kraken requests started %s apart", delay)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := sol.Latest(ctx, pricetrigger.FeedSOLUSD); err == nil {
		t.Fatal("a canceled default rate wait reached Kraken")
	}
	select {
	case at := <-started:
		t.Fatalf("canceled request reached Kraken at %s", at)
	default:
	}
}

func TestKrakenDefaultGateCancellationIsNotBlockedByAnotherWaiter(t *testing.T) {
	gate := &memoryKrakenRequestGate{spacing: 250 * time.Millisecond}
	if err := gate.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	waiting := make(chan error, 1)
	go func() { waiting <- gate.Wait(t.Context()) }()
	time.Sleep(25 * time.Millisecond)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	started := time.Now()
	if err := gate.Wait(ctx); err == nil {
		t.Fatal("a canceled contended rate wait succeeded")
	}
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("canceled contended rate wait took %s", elapsed)
	}
	if err := <-waiting; err != nil {
		t.Fatal(err)
	}
}

func TestKrakenRateStateCoordinatesAdaptersAndHonorsCancellation(t *testing.T) {
	t.Setenv(KrakenRateStateEnvironment, filepath.Join(t.TempDir(), "kraken-rate"))
	snapshotAt := time.Now().UTC().Truncate(time.Second)
	started := make(chan time.Time, 3)
	client := fixtureClient(t, func(request *http.Request) string {
		started <- time.Now()
		product := request.URL.Query().Get("symbol")
		if product == "SOL/USD" {
			return `{"result":{"symbol":"SOL/USD","bids":[{"side":"BUY","price":"199.99","publication_ts":"` +
				snapshotAt.Format(time.RFC3339Nano) + `"}],"asks":[{"side":"SELL","price":"200.01","publication_ts":"` +
				snapshotAt.Format(time.RFC3339Nano) + `"}]},"error":[]}`
		}
		return `{"result":{"symbol":"USDC/USD","bids":[{"side":"BUY","price":"0.9999","publication_ts":"` +
			snapshotAt.Format(time.RFC3339Nano) + `"}],"asks":[{"side":"SELL","price":"1.0001","publication_ts":"` +
			snapshotAt.Format(time.RFC3339Nano) + `"}]},"error":[]}`
	}, http.Header{"Date": []string{snapshotAt.Format(http.TimeFormat)}})
	if _, err := NewKrakenSOL(client).Latest(t.Context(), pricetrigger.FeedSOLUSD); err != nil {
		t.Fatal(err)
	}
	first := <-started
	if _, err := NewKraken(client).Latest(t.Context(), pricetrigger.FeedUSDCUSD); err != nil {
		t.Fatal(err)
	}
	second := <-started
	if second.Sub(first) < time.Second {
		t.Fatalf("Kraken requests started %s apart", second.Sub(first))
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	before := time.Now()
	if _, err := NewKrakenSOL(client).Latest(ctx, pricetrigger.FeedSOLUSD); err == nil {
		t.Fatal("a canceled local rate wait reached Kraken")
	}
	if time.Since(before) > 250*time.Millisecond {
		t.Fatal("Kraken local rate wait ignored context cancellation")
	}
	select {
	case at := <-started:
		t.Fatalf("canceled request reached Kraken at %s", at)
	default:
	}
}

func TestKrakenRateStateDoesNotAdvanceForAPrecanceledRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kraken-rate")
	t.Setenv(KrakenRateStateEnvironment, path)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := NewKraken(nil).Latest(ctx, pricetrigger.FeedUSDCUSD); err == nil {
		t.Fatal("a precanceled Kraken request passed the file rate gate")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("a precanceled request created Kraken rate state: %v", err)
	}
}
