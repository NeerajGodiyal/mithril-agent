// Package pricesource provides fixed-origin market-price adapters.
package pricesource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/bits"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

const (
	PythTrustDomain         = "pyth-network"
	PythOrigin              = "https://pyth.dourolabs.app/hermes"
	SOLUSDFeedID            = "ef0d8b6fda2ceba41da15d4095d1da392a0d2f8ed0c6c7bc0f4cfac8c280b56d"
	maxResponseBytes        = 64 << 10
	maxHTTPTime             = 8 * time.Second
	maxAccessTokenBytes     = 4 << 10
	pythIdentityDescription = "mithril-agent/price-source-v2|pyth-core|pyth.dourolabs.app/hermes|stable:SOL/USD|aggregate-confidence"
)

type Pyth struct {
	client      *http.Client
	accessToken string
}

func PythIdentitySHA256() string {
	hash := sha256.Sum256([]byte(pythIdentityDescription))
	return hex.EncodeToString(hash[:])
}

func NewPyth(client *http.Client, accessToken string) (*Pyth, error) {
	if !validAccessToken(accessToken) {
		return nil, errors.New("Pyth API key is missing or invalid")
	}
	return &Pyth{client: boundedClient(client), accessToken: accessToken}, nil
}

func (*Pyth) IdentitySHA256() string { return PythIdentitySHA256() }

func (source *Pyth) Latest(ctx context.Context, feed string) (pricetrigger.Sample, error) {
	if feed != pricetrigger.FeedSOLUSD {
		return pricetrigger.Sample{}, errors.New("Pyth price feed is unsupported")
	}
	endpoint := PythOrigin + "/v2/updates/price/latest?ids%5B%5D=" + SOLUSDFeedID + "&parsed=true"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return pricetrigger.Sample{}, errors.New("create Pyth price request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+source.accessToken)
	request.Header.Set("User-Agent", "mithril-agent/0.1")
	var response struct {
		Parsed []struct {
			ID    string `json:"id"`
			Price struct {
				Price       string `json:"price"`
				Confidence  string `json:"conf"`
				Exponent    int32  `json:"expo"`
				PublishedAt int64  `json:"publish_time"`
			} `json:"price"`
		} `json:"parsed"`
	}
	if err := readJSON(ctx, source.client, request, &response); err != nil {
		return pricetrigger.Sample{}, errors.New("Pyth price is unavailable")
	}
	if len(response.Parsed) != 1 || response.Parsed[0].ID != SOLUSDFeedID ||
		response.Parsed[0].Price.PublishedAt <= 0 {
		return pricetrigger.Sample{}, errors.New("Pyth price response is invalid")
	}
	price, err := scaleMicros(response.Parsed[0].Price.Price, response.Parsed[0].Price.Exponent, false)
	if err != nil {
		return pricetrigger.Sample{}, errors.New("Pyth price response is invalid")
	}
	confidence, err := scaleMicros(
		response.Parsed[0].Price.Confidence, response.Parsed[0].Price.Exponent, true,
	)
	if err != nil || confidence == 0 {
		return pricetrigger.Sample{}, errors.New("Pyth price response is invalid")
	}
	return pricetrigger.Sample{
		SourceSHA256: PythIdentitySHA256(), Feed: feed,
		PriceMicros: price, ConfidenceMicros: confidence,
		PublishedAt: time.Unix(response.Parsed[0].Price.PublishedAt, 0).UTC(),
	}, nil
}

func validAccessToken(token string) bool {
	if token == "" || len(token) > maxAccessTokenBytes || strings.TrimSpace(token) != token {
		return false
	}
	for _, character := range token {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}

func boundedClient(client *http.Client) *http.Client {
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		client = &http.Client{Transport: transport}
	}
	copy := *client
	if copy.Timeout <= 0 || copy.Timeout > maxHTTPTime {
		copy.Timeout = maxHTTPTime
	}
	copy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &copy
}

func readJSON(ctx context.Context, client *http.Client, request *http.Request, target any) error {
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		return errors.New("price provider rejected the request")
	}
	if mediaType := response.Header.Get("Content-Type"); mediaType != "" &&
		!strings.HasPrefix(strings.ToLower(mediaType), "application/json") {
		return errors.New("price provider returned an invalid content type")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxResponseBytes {
		return errors.New("price provider response is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return errors.New("price provider response is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("price provider response is invalid")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func scaleMicros(raw string, exponent int32, roundUp bool) (uint64, error) {
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 || exponent < -18 || exponent > 12 {
		return 0, errors.New("invalid decimal")
	}
	scale := int(exponent) + 6
	if scale >= 0 {
		factor, ok := pow10(scale)
		if !ok {
			return 0, errors.New("decimal overflow")
		}
		high, low := bits.Mul64(value, factor)
		if high != 0 || low > pricetrigger.MaxPriceMicros {
			return 0, errors.New("decimal overflow")
		}
		return low, nil
	}
	divisor, ok := pow10(-scale)
	if !ok {
		return 0, errors.New("decimal overflow")
	}
	result := value / divisor
	if roundUp && value%divisor != 0 {
		result++
	}
	if result == 0 || result > pricetrigger.MaxPriceMicros {
		return 0, errors.New("decimal overflow")
	}
	return result, nil
}

func pow10(exponent int) (uint64, bool) {
	if exponent < 0 || exponent > 18 {
		return 0, false
	}
	value := uint64(1)
	for range exponent {
		value *= 10
	}
	return value, true
}
