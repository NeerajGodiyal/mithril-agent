package perpspaper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxHyperliquidResponse = 4 << 20

type Environment string

const (
	Mainnet Environment = "mainnet"
	Testnet Environment = "testnet"
)

type HyperliquidClient struct {
	endpoint string
	http     *http.Client
}

func NewHyperliquidClient(environment Environment, client *http.Client) (*HyperliquidClient, error) {
	endpoint := ""
	switch environment {
	case Mainnet:
		endpoint = "https://api.hyperliquid.xyz/info"
	case Testnet:
		endpoint = "https://api.hyperliquid-testnet.xyz/info"
	default:
		return nil, fmt.Errorf("unsupported Hyperliquid environment %q", environment)
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	copy := *client
	previousRedirect := copy.CheckRedirect
	copy.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if request.URL.Scheme != "https" || request.URL.Host != strings.TrimPrefix(strings.TrimSuffix(endpoint, "/info"), "https://") ||
			request.URL.Path != "/info" || request.URL.RawQuery != "" || request.URL.User != nil {
			return errors.New("Hyperliquid redirect left the allowlisted host")
		}
		if previousRedirect != nil {
			return previousRedirect(request, via)
		}
		if len(via) >= 10 {
			return errors.New("too many Hyperliquid redirects")
		}
		return nil
	}
	return &HyperliquidClient{endpoint: endpoint, http: &copy}, nil
}

type AssetMeta struct {
	Name          Symbol `json:"name"`
	SzDecimals    uint8  `json:"szDecimals"`
	MaxLeverage   uint32 `json:"maxLeverage"`
	MarginTableID uint32 `json:"marginTableId"`
	IsDelisted    bool   `json:"isDelisted,omitempty"`
	OnlyIsolated  bool   `json:"onlyIsolated,omitempty"`
	MarginMode    string `json:"marginMode,omitempty"`
}

type AssetContext struct {
	Funding      string   `json:"funding"`
	OpenInterest string   `json:"openInterest"`
	PrevDayPx    string   `json:"prevDayPx"`
	DayNtlVlm    string   `json:"dayNtlVlm"`
	DayBaseVlm   string   `json:"dayBaseVlm"`
	Premium      *string  `json:"premium,omitempty"`
	OraclePx     string   `json:"oraclePx"`
	MarkPx       string   `json:"markPx"`
	MidPx        *string  `json:"midPx,omitempty"`
	ImpactPxs    []string `json:"impactPxs,omitempty"`
}

type MetaAndAssetContexts struct {
	Universe []AssetMeta
	Contexts []AssetContext
}

func (c *HyperliquidClient) MetaAndAssetContexts(ctx context.Context) (MetaAndAssetContexts, error) {
	var raw []json.RawMessage
	if err := c.post(ctx, map[string]string{"type": "metaAndAssetCtxs"}, &raw); err != nil {
		return MetaAndAssetContexts{}, err
	}
	if len(raw) != 2 {
		return MetaAndAssetContexts{}, errors.New("metaAndAssetCtxs response must contain metadata and contexts")
	}
	var meta struct {
		Universe        []AssetMeta     `json:"universe"`
		MarginTables    json.RawMessage `json:"marginTables"`
		CollateralToken *uint32         `json:"collateralToken"`
	}
	if err := decodeExact(raw[0], &meta); err != nil {
		return MetaAndAssetContexts{}, fmt.Errorf("decode metadata: %w", err)
	}
	var contexts []AssetContext
	if err := decodeExact(raw[1], &contexts); err != nil {
		return MetaAndAssetContexts{}, fmt.Errorf("decode asset contexts: %w", err)
	}
	if len(meta.Universe) == 0 || len(meta.Universe) != len(contexts) ||
		len(meta.MarginTables) == 0 || meta.CollateralToken == nil {
		return MetaAndAssetContexts{}, errors.New("metadata and asset contexts do not align")
	}
	result := MetaAndAssetContexts{}
	for i := range meta.Universe {
		if !supportedSymbol(meta.Universe[i].Name) {
			continue
		}
		if meta.Universe[i].MaxLeverage == 0 || meta.Universe[i].MarginTableID == 0 || meta.Universe[i].IsDelisted {
			return MetaAndAssetContexts{}, fmt.Errorf("asset %s has no leverage limit", meta.Universe[i].Name)
		}
		if _, err := lotSize(meta.Universe[i].Name, meta.Universe[i].SzDecimals); err != nil {
			return MetaAndAssetContexts{}, fmt.Errorf("asset %s has invalid size precision", meta.Universe[i].Name)
		}
		if err := validateAssetContext(contexts[i]); err != nil {
			return MetaAndAssetContexts{}, fmt.Errorf("asset %s: %w", meta.Universe[i].Name, err)
		}
		result.Universe = append(result.Universe, meta.Universe[i])
		result.Contexts = append(result.Contexts, contexts[i])
	}
	if len(result.Universe) == 0 {
		return MetaAndAssetContexts{}, errors.New("response contains no supported assets")
	}
	return result, nil
}

type Candle struct {
	OpenTime  int64  `json:"open_time"`
	CloseTime int64  `json:"close_time"`
	Symbol    Symbol `json:"symbol"`
	Interval  string `json:"interval"`
	Open      string `json:"open"`
	Close     string `json:"close"`
	High      string `json:"high"`
	Low       string `json:"low"`
	Volume    string `json:"volume"`
	Trades    uint64 `json:"trades"`
}

type hyperliquidCandle struct {
	OpenTime  int64  `json:"t"`
	CloseTime int64  `json:"T"`
	Symbol    Symbol `json:"s"`
	Interval  string `json:"i"`
	Open      string `json:"o"`
	Close     string `json:"c"`
	High      string `json:"h"`
	Low       string `json:"l"`
	Volume    string `json:"v"`
	Trades    uint64 `json:"n"`
}

func (c *HyperliquidClient) Candles(ctx context.Context, symbol Symbol, interval string, start, end int64) ([]Candle, error) {
	if !supportedSymbol(symbol) || !validInterval(interval) || start < 0 || end <= start {
		return nil, errors.New("invalid candle request")
	}
	request := struct {
		Type string `json:"type"`
		Req  struct {
			Coin      Symbol `json:"coin"`
			Interval  string `json:"interval"`
			StartTime int64  `json:"startTime"`
			EndTime   int64  `json:"endTime"`
		} `json:"req"`
	}{Type: "candleSnapshot"}
	request.Req.Coin, request.Req.Interval, request.Req.StartTime, request.Req.EndTime = symbol, interval, start, end
	var wire []hyperliquidCandle
	if err := c.post(ctx, request, &wire); err != nil {
		return nil, err
	}
	candles := make([]Candle, len(wire))
	for i, item := range wire {
		candles[i] = Candle{
			OpenTime: item.OpenTime, CloseTime: item.CloseTime, Symbol: item.Symbol,
			Interval: item.Interval, Open: item.Open, Close: item.Close, High: item.High,
			Low: item.Low, Volume: item.Volume, Trades: item.Trades,
		}
	}
	if len(candles) > 5000 {
		return nil, errors.New("candle response exceeds documented maximum")
	}
	for i := range candles {
		v := candles[i]
		if v.Symbol != symbol || v.Interval != interval || v.OpenTime < start || v.OpenTime > end || v.CloseTime <= 0 || v.CloseTime > end || v.CloseTime < v.OpenTime ||
			(interval == "1m" && v.CloseTime-v.OpenTime != int64(time.Minute/time.Millisecond)-1) ||
			validateDecimalFields(v.Open, v.Close, v.High, v.Low, v.Volume) != nil {
			return nil, fmt.Errorf("invalid candle at index %d", i)
		}
		open, openErr := decimalMicros(v.Open)
		close, closeErr := decimalMicros(v.Close)
		high, highErr := decimalMicros(v.High)
		low, lowErr := decimalMicros(v.Low)
		volume, volumeErr := strconv.ParseFloat(v.Volume, 64)
		if openErr != nil || closeErr != nil || highErr != nil || lowErr != nil || volumeErr != nil || volume < 0 ||
			validatePrice(open) != nil || validatePrice(close) != nil || validatePrice(high) != nil || validatePrice(low) != nil ||
			high < open || high < close || low > open || low > close || low > high {
			return nil, fmt.Errorf("invalid candle values at index %d", i)
		}
		if i > 0 && (v.OpenTime <= candles[i-1].OpenTime || v.OpenTime <= candles[i-1].CloseTime ||
			interval == "1m" && v.OpenTime != candles[i-1].CloseTime+1) {
			return nil, fmt.Errorf("candle %d overlaps or reverses the preceding candle", i)
		}
	}
	return candles, nil
}

type Level struct {
	Price string `json:"px"`
	Size  string `json:"sz"`
	Count uint32 `json:"n"`
}

type L2Book struct {
	Symbol Symbol     `json:"coin"`
	Time   int64      `json:"time"`
	Levels [2][]Level `json:"levels"`
}

func (c *HyperliquidClient) Book(ctx context.Context, symbol Symbol) (L2Book, error) {
	if !supportedSymbol(symbol) {
		return L2Book{}, fmt.Errorf("unsupported perps symbol %q", symbol)
	}
	var book L2Book
	if err := c.post(ctx, map[string]any{"type": "l2Book", "coin": symbol}, &book); err != nil {
		return L2Book{}, err
	}
	if err := validateBook(book, symbol); err != nil {
		return L2Book{}, err
	}
	return book, nil
}

func validateBook(book L2Book, symbol Symbol) error {
	if book.Symbol != symbol || book.Time <= 0 {
		return errors.New("invalid L2 book identity")
	}
	for side := range book.Levels {
		if len(book.Levels[side]) > 20 {
			return errors.New("L2 book exceeds documented depth")
		}
		previous := uint64(0)
		for i, level := range book.Levels[side] {
			price, priceErr := decimalMicros(level.Price)
			size, sizeErr := decimalUnits(symbol, level.Size)
			if level.Count == 0 || priceErr != nil || sizeErr != nil || size == 0 ||
				(i > 0 && ((side == 0 && price >= previous) || (side == 1 && price <= previous))) {
				return errors.New("invalid L2 book level")
			}
			previous = price
		}
	}
	if len(book.Levels[0]) > 0 && len(book.Levels[1]) > 0 {
		bid, _ := decimalMicros(book.Levels[0][0].Price)
		ask, _ := decimalMicros(book.Levels[1][0].Price)
		if bid >= ask {
			return errors.New("L2 book is crossed")
		}
	}
	return nil
}

type Funding struct {
	Symbol  Symbol `json:"coin"`
	Rate    string `json:"fundingRate"`
	Premium string `json:"premium"`
	Time    int64  `json:"time"`
}

var ErrFundingHistoryPageFull = errors.New("funding history page is full; paginate from the last timestamp")

func (c *HyperliquidClient) FundingHistory(ctx context.Context, symbol Symbol, start, end int64) ([]Funding, error) {
	if !supportedSymbol(symbol) || start < 0 || end <= start {
		return nil, errors.New("invalid funding-history request")
	}
	var history []Funding
	if err := c.post(ctx, map[string]any{"type": "fundingHistory", "coin": symbol, "startTime": start, "endTime": end}, &history); err != nil {
		return nil, err
	}
	if len(history) > 500 {
		return nil, errors.New("funding response exceeds one-page maximum")
	}
	for i, item := range history {
		if item.Symbol != symbol || item.Time < start || item.Time > end || validateDecimalFields(item.Rate, item.Premium) != nil {
			return nil, fmt.Errorf("invalid funding record at index %d", i)
		}
	}
	if len(history) == 500 {
		return history, ErrFundingHistoryPageFull
	}
	return history, nil
}

func (c *HyperliquidClient) post(ctx context.Context, body any, target any) error {
	if c == nil || c.http == nil {
		return errors.New("Hyperliquid client is nil")
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Hyperliquid info request: %w", err)
	}
	defer response.Body.Close()
	reader := io.LimitReader(response.Body, maxHyperliquidResponse+1)
	payload, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read Hyperliquid response: %w", err)
	}
	if len(payload) > maxHyperliquidResponse {
		return errors.New("Hyperliquid response is too large")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Hyperliquid info returned HTTP %d", response.StatusCode)
	}
	if err := decodeExact(payload, target); err != nil {
		return fmt.Errorf("decode Hyperliquid response: %w", err)
	}
	return nil
}

func decodeExact(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("JSON response contains trailing data")
	}
	return nil
}

func supportedSymbol(symbol Symbol) bool { return symbol == SOL || symbol == BTC || symbol == ETH }

func validInterval(interval string) bool {
	switch interval {
	case "1m", "3m", "5m", "15m", "30m", "1h", "2h", "4h", "8h", "12h", "1d", "3d", "1w", "1M":
		return true
	}
	return false
}

func validateDecimalFields(values ...string) error {
	for _, value := range values {
		number, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || strings.TrimSpace(value) != value || value == "" {
			return errors.New("invalid decimal string")
		}
	}
	return nil
}

func validateAssetContext(context AssetContext) error {
	if err := validateDecimalFields(context.Funding, context.OpenInterest, context.PrevDayPx, context.DayNtlVlm, context.DayBaseVlm, context.OraclePx, context.MarkPx); err != nil {
		return err
	}
	mark, markErr := decimalMicros(context.MarkPx)
	oracle, oracleErr := decimalMicros(context.OraclePx)
	if markErr != nil || oracleErr != nil || validatePrice(mark) != nil || validatePrice(oracle) != nil {
		return errors.New("mark and oracle prices must be positive supported decimals")
	}
	if context.Premium != nil {
		if err := validateDecimalFields(*context.Premium); err != nil {
			return err
		}
	}
	if context.MidPx != nil {
		if err := validateDecimalFields(*context.MidPx); err != nil {
			return err
		}
	}
	if len(context.ImpactPxs) != 0 && len(context.ImpactPxs) != 2 {
		return errors.New("impact prices must contain bid and ask")
	}
	return validateDecimalFields(context.ImpactPxs...)
}
