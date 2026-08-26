package pricesource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

const (
	KrakenTrustDomain         = "kraken"
	KrakenOrigin              = "https://api.kraken.com"
	krakenProduct             = "USDC/USD"
	krakenIdentityDescription = "mithril-agent/price-source-v1|kraken-spot|api.kraken.com|USDC/USD|pre-trade|best-bid-ask|http-date"
)

type Kraken struct{ client *http.Client }

func KrakenIdentitySHA256() string {
	hash := sha256.Sum256([]byte(krakenIdentityDescription))
	return hex.EncodeToString(hash[:])
}

func NewKraken(client *http.Client) *Kraken { return &Kraken{client: boundedClient(client)} }
func (*Kraken) IdentitySHA256() string      { return KrakenIdentitySHA256() }

func (source *Kraken) Latest(ctx context.Context, feed string) (pricetrigger.Sample, error) {
	if feed != pricetrigger.FeedUSDCUSD {
		return pricetrigger.Sample{}, errors.New("Kraken price feed is unsupported")
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, KrakenOrigin+"/0/public/PreTrade?symbol=USDC%2FUSD", nil,
	)
	if err != nil {
		return pricetrigger.Sample{}, errors.New("create Kraken price request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "mithril-agent/0.1")
	type level struct {
		Side          string      `json:"side"`
		Price         json.Number `json:"price"`
		PublicationTS string      `json:"publication_ts"`
	}
	var response struct {
		Result struct {
			Symbol string  `json:"symbol"`
			Bids   []level `json:"bids"`
			Asks   []level `json:"asks"`
		} `json:"result"`
		Error []string `json:"error"`
	}
	headers, err := readJSON(ctx, source.client, request, &response)
	if err != nil {
		return pricetrigger.Sample{}, errors.New("Kraken price is unavailable")
	}
	if len(response.Error) != 0 || response.Result.Symbol != krakenProduct ||
		len(response.Result.Bids) == 0 || len(response.Result.Asks) == 0 {
		return pricetrigger.Sample{}, errors.New("Kraken price response is invalid")
	}
	bid, ask := response.Result.Bids[0], response.Result.Asks[0]
	if bid.Side != "BUY" || ask.Side != "SELL" {
		return pricetrigger.Sample{}, errors.New("Kraken price response is invalid")
	}
	lower, _, lowerErr := decimalMicros(bid.Price.String())
	upper, upperRounded, upperErr := decimalMicros(ask.Price.String())
	bidAt, bidTimeErr := time.Parse(time.RFC3339Nano, bid.PublicationTS)
	askAt, askTimeErr := time.Parse(time.RFC3339Nano, ask.PublicationTS)
	publishedAt, snapshotTimeErr := http.ParseTime(headers.Get("Date"))
	if lowerErr != nil || upperErr != nil || bidTimeErr != nil || askTimeErr != nil ||
		snapshotTimeErr != nil || bidAt.After(publishedAt.Add(time.Second)) ||
		askAt.After(publishedAt.Add(time.Second)) {
		return pricetrigger.Sample{}, errors.New("Kraken price response is invalid")
	}
	if upperRounded {
		if upper == pricetrigger.MaxPriceMicros {
			return pricetrigger.Sample{}, errors.New("Kraken price response is invalid")
		}
		upper++
	}
	if upper < lower {
		return pricetrigger.Sample{}, errors.New("Kraken price response is invalid")
	}
	price := lower + (upper-lower)/2
	confidence := max(price-lower, upper-price)
	if confidence == 0 {
		confidence = 1
	}
	if confidence >= price {
		return pricetrigger.Sample{}, errors.New("Kraken price response is invalid")
	}
	return pricetrigger.Sample{
		SourceSHA256: KrakenIdentitySHA256(), Feed: feed,
		PriceMicros: price, ConfidenceMicros: confidence,
		PublishedAt: publishedAt.UTC(),
	}, nil
}
