package pricesource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

const (
	CoinbaseTrustDomain     = "coinbase"
	CoinbaseOrigin          = "https://api.exchange.coinbase.com"
	coinbaseProduct         = "SOL-USD"
	coinbaseIdentityDetails = "mithril-agent/price-source-v1|coinbase-exchange|api.exchange.coinbase.com|SOL-USD|product-ticker|bid-ask"
)

type Coinbase struct{ client *http.Client }

func CoinbaseIdentitySHA256() string {
	hash := sha256.Sum256([]byte(coinbaseIdentityDetails))
	return hex.EncodeToString(hash[:])
}

func NewCoinbase(client *http.Client) *Coinbase { return &Coinbase{client: boundedClient(client)} }
func (*Coinbase) IdentitySHA256() string        { return CoinbaseIdentitySHA256() }

func (source *Coinbase) Latest(ctx context.Context, feed string) (pricetrigger.Sample, error) {
	if feed != pricetrigger.FeedSOLUSD {
		return pricetrigger.Sample{}, errors.New("Coinbase price feed is unsupported")
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, CoinbaseOrigin+"/products/"+coinbaseProduct+"/ticker", nil,
	)
	if err != nil {
		return pricetrigger.Sample{}, errors.New("create Coinbase price request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "mithril-agent/0.1")
	var response struct {
		Ask   json.Number `json:"ask"`
		Bid   json.Number `json:"bid"`
		Price json.Number `json:"price"`
		Time  string      `json:"time"`
	}
	if err := readJSON(ctx, source.client, request, &response); err != nil {
		return pricetrigger.Sample{}, errors.New("Coinbase price is unavailable")
	}
	lower, _, lowerErr := decimalMicros(response.Bid.String())
	upper, upperRounded, upperErr := decimalMicros(response.Ask.String())
	_, _, tradeErr := decimalMicros(response.Price.String())
	publishedAt, timeErr := time.Parse(time.RFC3339Nano, response.Time)
	if lowerErr != nil || upperErr != nil || tradeErr != nil || timeErr != nil {
		return pricetrigger.Sample{}, errors.New("Coinbase price response is invalid")
	}
	if upperRounded {
		if upper == pricetrigger.MaxPriceMicros {
			return pricetrigger.Sample{}, errors.New("Coinbase price response is invalid")
		}
		upper++
	}
	if upper < lower {
		return pricetrigger.Sample{}, errors.New("Coinbase price response is invalid")
	}
	price := lower + (upper-lower)/2
	confidence := max(price-lower, upper-price)
	if confidence == 0 {
		confidence = 1
	}
	if confidence >= price {
		return pricetrigger.Sample{}, errors.New("Coinbase price response is invalid")
	}
	return pricetrigger.Sample{
		SourceSHA256: CoinbaseIdentitySHA256(), Feed: feed,
		PriceMicros: price, ConfidenceMicros: confidence,
		PublishedAt: publishedAt.UTC(),
	}, nil
}

func decimalMicros(raw string) (uint64, bool, error) {
	if raw == "" || strings.ContainsAny(raw, "eE+-") {
		return 0, false, errors.New("invalid decimal")
	}
	whole, fraction, found := strings.Cut(raw, ".")
	if !found {
		fraction = ""
	}
	if whole == "" || len(whole) > 7 || len(fraction) > 24 {
		return 0, false, errors.New("invalid decimal")
	}
	for _, part := range []string{whole, fraction} {
		for _, char := range part {
			if char < '0' || char > '9' {
				return 0, false, errors.New("invalid decimal")
			}
		}
	}
	wholeValue, err := strconv.ParseUint(whole, 10, 64)
	if err != nil || wholeValue > pricetrigger.MaxPriceMicros/1_000_000 {
		return 0, false, errors.New("invalid decimal")
	}
	padded := fraction + strings.Repeat("0", 6)
	fractionValue, err := strconv.ParseUint(padded[:6], 10, 64)
	if err != nil {
		return 0, false, errors.New("invalid decimal")
	}
	value := wholeValue*1_000_000 + fractionValue
	rounded := len(fraction) > 6 && strings.Trim(fraction[6:], "0") != ""
	if value == 0 || value > pricetrigger.MaxPriceMicros {
		return 0, false, errors.New("invalid decimal")
	}
	return value, rounded, nil
}
