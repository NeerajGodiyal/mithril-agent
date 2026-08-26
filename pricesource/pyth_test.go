package pricesource

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestPythParsesExactIntegerPrice(t *testing.T) {
	published := time.Now().UTC().Unix()
	client := fixtureClient(t, func(request *http.Request) string {
		if request.URL.Host != "pyth.dourolabs.app" ||
			request.URL.Path != "/hermes/v2/updates/price/latest" ||
			request.URL.Query().Get("ids[]") != SOLUSDFeedID ||
			request.Header.Get("Authorization") != "Bearer test-pyth-key" {
			t.Fatalf("request URL = %s", request.URL)
		}
		return `{"binary":{"encoding":"hex","data":[]},"parsed":[{"id":"` +
			SOLUSDFeedID + `","price":{"price":"7313007563","conf":"3993240","expo":-8,"publish_time":` +
			strconv.FormatInt(published, 10) + `},"ema_price":{},"metadata":{}}]}`
	})
	source, err := NewPyth(client, "test-pyth-key")
	if err != nil {
		t.Fatal(err)
	}
	sample, err := source.Latest(t.Context(), pricetrigger.FeedSOLUSD)
	if err != nil {
		t.Fatal(err)
	}
	if sample.SourceSHA256 != PythIdentitySHA256() || sample.PriceMicros != 73_130_075 ||
		sample.ConfidenceMicros != 39_933 || sample.PublishedAt.Unix() != published {
		t.Fatalf("sample = %+v", sample)
	}
}

func TestPythRejectsMalformedAndOversizedResponsesWithoutEcho(t *testing.T) {
	for name, body := range map[string]string{
		"malformed": `{private-provider-detail`,
		"oversized": strings.Repeat("x", maxResponseBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			client := fixtureClient(t, func(*http.Request) string { return body })
			source, err := NewPyth(client, "test-pyth-key")
			if err != nil {
				t.Fatal(err)
			}
			_, err = source.Latest(t.Context(), pricetrigger.FeedSOLUSD)
			if err == nil || strings.Contains(err.Error(), "private-provider-detail") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestPythCancellationAndDecimalOverflowFailClosed(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	source, err := NewPyth(client, "test-pyth-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Latest(ctx, pricetrigger.FeedSOLUSD); err == nil {
		t.Fatal("canceled request succeeded")
	}
	if _, err := source.Latest(t.Context(), "BTC/USD"); err == nil {
		t.Fatal("unsupported feed succeeded")
	}
	if _, err := scaleMicros("18446744073709551615", 12, false); err == nil {
		t.Fatal("scale overflow was accepted")
	}
}

func TestPythRejectsMissingOrUnsafeAccessToken(t *testing.T) {
	for _, token := range []string{"", " leading", "trailing ", "line\nbreak"} {
		if _, err := NewPyth(nil, token); err == nil {
			t.Fatalf("token %q was accepted", token)
		}
	}
}

func TestPriceSourcesDoNotUseAnAmbientProxyByDefault(t *testing.T) {
	for _, client := range []*http.Client{nil, {Timeout: time.Second}} {
		transport, ok := boundedClient(client).Transport.(*http.Transport)
		if !ok || transport.Proxy != nil {
			t.Fatal("default price-source client can use an ambient proxy")
		}
	}
}

func fixtureClient(
	t *testing.T,
	body func(*http.Request) string,
	headers ...http.Header,
) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		header := http.Header{"Content-Type": []string{"application/json"}}
		if len(headers) != 0 {
			header = headers[0].Clone()
			header.Set("Content-Type", "application/json")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(body(request))),
			Request:    request,
		}, nil
	})}
}
