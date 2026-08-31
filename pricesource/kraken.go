package pricesource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

const (
	KrakenTrustDomain             = "kraken"
	KrakenOrigin                  = "https://api.kraken.com"
	krakenUSDCProduct             = "USDC/USD"
	krakenSOLProduct              = "SOL/USD"
	krakenJUPProduct              = "JUP/USD"
	krakenUSDCIdentityDescription = "mithril-agent/price-source-v1|kraken-spot|api.kraken.com|USDC/USD|pre-trade|best-bid-ask|http-date"
	krakenSOLIdentityDescription  = "mithril-agent/price-source-v1|kraken-spot|api.kraken.com|SOL/USD|pre-trade|best-bid-ask|http-date"
	krakenJUPIdentityDescription  = "mithril-agent/price-source-v1|kraken-spot|api.kraken.com|JUP/USD|pre-trade|best-bid-ask|http-date"
	KrakenRateStateEnvironment    = "MITHRIL_AGENT_KRAKEN_RATE_STATE"
)

type Kraken struct {
	client   *http.Client
	feed     string
	product  string
	identity string
	gate     krakenRequestGate
}

// KrakenSpec binds an allowlisted candidate feed to one exact public PreTrade
// product. Kraken's origin stays fixed in code.
type KrakenSpec struct {
	Feed    string `json:"feed"`
	Product string `json:"product"`
}

func (spec KrakenSpec) Validate() error {
	if !pricetrigger.ValidUSDFeed(spec.Feed) {
		return errors.New("Kraken feed name is invalid")
	}
	base, ok := strings.CutSuffix(spec.Feed, "/USD")
	if !ok || spec.Product != base+"/USD" {
		return errors.New("Kraken product does not match its feed")
	}
	return nil
}

func (spec KrakenSpec) IdentitySHA256() (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	description := "mithril-agent/price-source-v2|kraken-spot|api.kraken.com|" +
		spec.Product + "|pre-trade|best-bid-ask|http-date"
	return sourceIdentity(description), nil
}

func KrakenIdentitySHA256() string {
	hash := sha256.Sum256([]byte(krakenUSDCIdentityDescription))
	return hex.EncodeToString(hash[:])
}

func KrakenSOLIdentitySHA256() string {
	hash := sha256.Sum256([]byte(krakenSOLIdentityDescription))
	return hex.EncodeToString(hash[:])
}

func KrakenJUPIdentitySHA256() string {
	hash := sha256.Sum256([]byte(krakenJUPIdentityDescription))
	return hex.EncodeToString(hash[:])
}

func NewKraken(client *http.Client) *Kraken {
	return &Kraken{
		client: boundedClient(client), feed: pricetrigger.FeedUSDCUSD,
		product: krakenUSDCProduct, identity: KrakenIdentitySHA256(),
		gate: newKrakenRequestGate(os.Getenv(KrakenRateStateEnvironment)),
	}
}

func NewKrakenSOL(client *http.Client) *Kraken {
	return &Kraken{
		client: boundedClient(client), feed: pricetrigger.FeedSOLUSD,
		product: krakenSOLProduct, identity: KrakenSOLIdentitySHA256(),
		gate: newKrakenRequestGate(os.Getenv(KrakenRateStateEnvironment)),
	}
}

func NewKrakenJUP(client *http.Client) *Kraken {
	return &Kraken{
		client: boundedClient(client), feed: pricetrigger.FeedJUPUSD,
		product: krakenJUPProduct, identity: KrakenJUPIdentitySHA256(),
		gate: newKrakenRequestGate(os.Getenv(KrakenRateStateEnvironment)),
	}
}

func NewKrakenFromSpec(client *http.Client, spec KrakenSpec) (*Kraken, error) {
	identity, err := spec.IdentitySHA256()
	if err != nil {
		return nil, err
	}
	return &Kraken{
		client: boundedClient(client), feed: spec.Feed, product: spec.Product,
		identity: identity,
		gate:     newKrakenRequestGate(os.Getenv(KrakenRateStateEnvironment)),
	}, nil
}

func (source *Kraken) IdentitySHA256() string {
	if source == nil {
		return ""
	}
	return source.identity
}

func (source *Kraken) Latest(ctx context.Context, feed string) (pricetrigger.Sample, error) {
	if source == nil || source.gate == nil || feed != source.feed {
		return pricetrigger.Sample{}, errors.New("Kraken price feed is unsupported")
	}
	if err := source.gate.Wait(ctx); err != nil {
		return pricetrigger.Sample{}, errors.New("Kraken price request was rate-limited locally")
	}
	query := url.Values{"symbol": []string{source.product}}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, KrakenOrigin+"/0/public/PreTrade?"+query.Encode(), nil,
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
	if len(response.Error) != 0 || response.Result.Symbol != source.product ||
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
		SourceSHA256: source.IdentitySHA256(), Feed: feed,
		PriceMicros: price, ConfidenceMicros: confidence,
		PublishedAt: publishedAt.UTC(),
	}, nil
}
