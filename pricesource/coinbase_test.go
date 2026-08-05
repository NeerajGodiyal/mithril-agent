package pricesource

import (
	"net/http"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

func TestCoinbaseUsesBidAskBoundsAndPublicationTime(t *testing.T) {
	published := time.Now().UTC().Truncate(time.Microsecond)
	client := fixtureClient(t, func(request *http.Request) string {
		if request.URL.Host != "api.exchange.coinbase.com" ||
			request.URL.Path != "/products/SOL-USD/ticker" {
			t.Fatalf("request URL = %s", request.URL)
		}
		return `{"ask":"73.1300019","bid":"73.1200019","price":"73.13","time":"` +
			published.Format(time.RFC3339Nano) + `"}`
	})
	sample, err := NewCoinbase(client).Latest(t.Context(), pricetrigger.FeedSOLUSD)
	if err != nil {
		t.Fatal(err)
	}
	if sample.SourceSHA256 != CoinbaseIdentitySHA256() || sample.PriceMicros != 73_125_001 ||
		sample.ConfidenceMicros != 5_001 || !sample.PublishedAt.Equal(published) {
		t.Fatalf("sample = %+v", sample)
	}
	if sample.PriceMicros-sample.ConfidenceMicros != 73_120_000 ||
		sample.PriceMicros+sample.ConfidenceMicros != 73_130_002 {
		t.Fatalf("sample does not contain the bid-ask interval: %+v", sample)
	}
	if sample.SourceSHA256 == PythIdentitySHA256() {
		t.Fatal("independent source identities collided")
	}
}

func TestCoinbaseRejectsMalformedMarketData(t *testing.T) {
	tests := []string{
		`{"ask":"73","bid":"74","price":"73","time":"2026-08-02T00:00:00Z"}`,
		`{"ask":"73","bid":"72","price":"bad","time":"2026-08-02T00:00:00Z"}`,
		`{"ask":"73","bid":"72","price":"73","time":"bad"}`,
	}
	for _, body := range tests {
		client := fixtureClient(t, func(*http.Request) string { return body })
		if _, err := NewCoinbase(client).Latest(t.Context(), pricetrigger.FeedSOLUSD); err == nil {
			t.Fatalf("accepted market data %s", body)
		}
	}
	if _, err := NewCoinbase(fixtureClient(t, func(*http.Request) string { return `{}` })).Latest(
		t.Context(), "BTC/USD",
	); err == nil {
		t.Fatal("unsupported feed succeeded")
	}
}

func TestDecimalMicrosIsExactAndBounded(t *testing.T) {
	for _, value := range []string{"", "-1", "+1", "1e2", ".5", "10000000"} {
		if _, _, err := decimalMicros(value); err == nil {
			t.Fatalf("accepted decimal %q", value)
		}
	}
	if value, rounded, err := decimalMicros("73.134928084"); err != nil ||
		value != 73_134_928 || !rounded {
		t.Fatalf("decimal = %d, rounded=%t, error=%v", value, rounded, err)
	}
}
