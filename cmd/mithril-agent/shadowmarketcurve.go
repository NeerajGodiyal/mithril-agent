package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/marketadmission"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	shadowMarketCurveVersion = uint32(1)
	shadowMarketCurveStatus  = "diagnostic_only"
)

type shadowMarketCurveQuoteSource interface {
	Quote(context.Context, jupiterquote.Request) (jupiterquote.Result, error)
}

type shadowMarketCurveQuote struct {
	InputMint       string    `json:"input_mint"`
	OutputMint      string    `json:"output_mint"`
	InputAmount     uint64    `json:"input_amount"`
	EstimatedOutput uint64    `json:"estimated_output"`
	MinimumOutput   uint64    `json:"minimum_output"`
	PriceImpactPct  string    `json:"price_impact_pct"`
	LatencyMillis   uint32    `json:"latency_millis"`
	ReceivedAt      time.Time `json:"received_at"`
	ResponseSHA256  string    `json:"response_sha256"`
}

type shadowMarketCurvePoint struct {
	NotionalUSDC          uint64                 `json:"notional_usdc"`
	Buy                   shadowMarketCurveQuote `json:"buy"`
	Sell                  shadowMarketCurveQuote `json:"sell"`
	RoundTripRouteCostBPS uint16                 `json:"round_trip_route_cost_bps"`
}

type shadowMarketCurveArtifact struct {
	Version                uint32                   `json:"version"`
	Status                 string                   `json:"status"`
	OperationallyQualified bool                     `json:"operationally_qualified"`
	Market                 string                   `json:"market"`
	Observe                string                   `json:"observe"`
	SlippageBPS            uint16                   `json:"slippage_bps"`
	CreatedAt              time.Time                `json:"created_at"`
	Points                 []shadowMarketCurvePoint `json:"points"`
	ContentSHA256          string                   `json:"content_sha256"`
}

func runShadowMarketCurve(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow market curve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	market := flags.String("market", "", "allowlisted market")
	observe := flags.String("observe", "", "watch-only quote address")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := io.WriteString(output, shadowMarketUsage+"\n")
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *market == "" || *observe == "" {
		return errors.New("shadow market curve requires --market and --observe")
	}
	candidate, ok := marketadmission.Lookup(*market)
	if !ok {
		return errors.New("--market must be WIF/USDC, JTO/USDC, or PYTH/USDC")
	}
	quotes, err := jupiterquote.New(os.Getenv(jupiterAPIKeyEnvironment))
	if err != nil {
		return err
	}
	artifact, err := collectShadowMarketCurve(ctx, candidate, *observe, quotes, time.Now)
	if err != nil {
		return err
	}
	return writeShadowMarketJSON(output, artifact)
}

func collectShadowMarketCurve(
	ctx context.Context,
	candidate marketadmission.Candidate,
	observe string,
	quotes shadowMarketCurveQuoteSource,
	now func() time.Time,
) (shadowMarketCurveArtifact, error) {
	if candidate.Validate() != nil {
		return shadowMarketCurveArtifact{}, errors.New("market curve candidate is invalid")
	}
	if _, err := solana.Decode32(observe); err != nil {
		return shadowMarketCurveArtifact{}, errors.New("market curve observe address is invalid")
	}
	if quotes == nil || now == nil {
		return shadowMarketCurveArtifact{}, errors.New("market curve source is unavailable")
	}
	artifact := shadowMarketCurveArtifact{
		Version: shadowMarketCurveVersion, Status: shadowMarketCurveStatus,
		Market: candidate.Market, Observe: observe, SlippageBPS: candidate.QuoteSlippageBPS,
		Points: make([]shadowMarketCurvePoint, 0, len(shadowMarketCurveNotionals())),
	}
	for _, notional := range shadowMarketCurveNotionals() {
		buyRequest := jupiterquote.Request{
			Taker: observe, InputMint: candidate.QuoteMint, OutputMint: candidate.BaseMint,
			InputAmount: notional, SlippageBPS: candidate.QuoteSlippageBPS,
		}
		buy, buyLatency, err := collectShadowMarketCurveQuote(ctx, quotes, buyRequest, now)
		if err != nil {
			return shadowMarketCurveArtifact{}, err
		}
		sellRequest := jupiterquote.Request{
			Taker: observe, InputMint: candidate.BaseMint, OutputMint: candidate.QuoteMint,
			InputAmount: buy.EstimatedOutput, SlippageBPS: candidate.QuoteSlippageBPS,
		}
		sell, sellLatency, err := collectShadowMarketCurveQuote(ctx, quotes, sellRequest, now)
		if err != nil {
			return shadowMarketCurveArtifact{}, err
		}
		artifact.Points = append(artifact.Points, shadowMarketCurvePoint{
			NotionalUSDC: notional,
			Buy:          newShadowMarketCurveQuote(buyRequest, buy, buyLatency),
			Sell:         newShadowMarketCurveQuote(sellRequest, sell, sellLatency),
			RoundTripRouteCostBPS: shadowMarketCurveRouteCostBPS(
				notional, sell.EstimatedOutput,
			),
		})
	}
	artifact.CreatedAt = now().UTC()
	digest, err := artifact.fingerprint()
	if err != nil {
		return shadowMarketCurveArtifact{}, err
	}
	artifact.ContentSHA256 = digest
	if err := artifact.Validate(); err != nil {
		return shadowMarketCurveArtifact{}, err
	}
	return artifact, nil
}

func collectShadowMarketCurveQuote(
	ctx context.Context,
	quotes shadowMarketCurveQuoteSource,
	request jupiterquote.Request,
	now func() time.Time,
) (jupiterquote.Result, uint32, error) {
	started := now()
	result, err := quotes.Quote(ctx, request)
	finished := now()
	if err != nil {
		return jupiterquote.Result{}, 0, err
	}
	if err := result.Validate(request); err != nil {
		return jupiterquote.Result{}, 0, errors.New("Jupiter market curve quote is invalid")
	}
	return result, shadowMarketCurveElapsedMillis(started, finished), nil
}

func newShadowMarketCurveQuote(
	request jupiterquote.Request,
	result jupiterquote.Result,
	latency uint32,
) shadowMarketCurveQuote {
	return shadowMarketCurveQuote{
		InputMint: request.InputMint, OutputMint: request.OutputMint,
		InputAmount: result.InputAmount, EstimatedOutput: result.EstimatedOutput,
		MinimumOutput: result.MinimumOutput, PriceImpactPct: result.PriceImpactPct,
		LatencyMillis: latency, ReceivedAt: result.ReceivedAt,
		ResponseSHA256: result.ResponseSHA256,
	}
}

func (artifact shadowMarketCurveArtifact) Validate() error {
	candidate, ok := marketadmission.Lookup(artifact.Market)
	if artifact.Version != shadowMarketCurveVersion || artifact.Status != shadowMarketCurveStatus ||
		artifact.OperationallyQualified || !ok || artifact.SlippageBPS != candidate.QuoteSlippageBPS ||
		artifact.CreatedAt.IsZero() || !artifact.CreatedAt.Equal(artifact.CreatedAt.UTC()) ||
		len(artifact.Points) != len(shadowMarketCurveNotionals()) {
		return errors.New("market curve artifact envelope is invalid")
	}
	if _, err := solana.Decode32(artifact.Observe); err != nil {
		return errors.New("market curve observe address is invalid")
	}
	for index, notional := range shadowMarketCurveNotionals() {
		point := artifact.Points[index]
		buyRequest := jupiterquote.Request{
			Taker: artifact.Observe, InputMint: candidate.QuoteMint, OutputMint: candidate.BaseMint,
			InputAmount: notional, SlippageBPS: artifact.SlippageBPS,
		}
		if point.NotionalUSDC != notional ||
			shadowMarketCurveQuoteValid(point.Buy, buyRequest, artifact.CreatedAt) != nil {
			return errors.New("market curve buy quote is invalid")
		}
		sellRequest := jupiterquote.Request{
			Taker: artifact.Observe, InputMint: candidate.BaseMint, OutputMint: candidate.QuoteMint,
			InputAmount: point.Buy.EstimatedOutput, SlippageBPS: artifact.SlippageBPS,
		}
		if shadowMarketCurveQuoteValid(point.Sell, sellRequest, artifact.CreatedAt) != nil ||
			point.RoundTripRouteCostBPS != shadowMarketCurveRouteCostBPS(
				notional, point.Sell.EstimatedOutput,
			) {
			return errors.New("market curve sell quote is invalid")
		}
	}
	want, err := artifact.fingerprint()
	if err != nil || !validLowerSHA256(artifact.ContentSHA256) || want != artifact.ContentSHA256 {
		return errors.New("market curve artifact digest does not match")
	}
	return nil
}

func shadowMarketCurveQuoteValid(
	quote shadowMarketCurveQuote,
	request jupiterquote.Request,
	createdAt time.Time,
) error {
	result := jupiterquote.Result{
		InputAmount: quote.InputAmount, EstimatedOutput: quote.EstimatedOutput,
		MinimumOutput: quote.MinimumOutput,
	}
	if quote.InputMint != request.InputMint || quote.OutputMint != request.OutputMint ||
		result.Validate(request) != nil || !shadowMarketCurvePriceImpactValid(quote.PriceImpactPct) ||
		quote.ReceivedAt.IsZero() || !quote.ReceivedAt.Equal(quote.ReceivedAt.UTC()) ||
		quote.ReceivedAt.After(createdAt) || !validLowerSHA256(quote.ResponseSHA256) {
		return errors.New("market curve quote is invalid")
	}
	return nil
}

func (artifact shadowMarketCurveArtifact) fingerprint() (string, error) {
	copy := artifact
	copy.ContentSHA256 = ""
	encoded, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func shadowMarketCurveNotionals() [4]uint64 {
	return [4]uint64{10_000_000, 25_000_000, 50_000_000, 100_000_000}
}

func shadowMarketCurveRouteCostBPS(input, output uint64) uint16 {
	if output >= input {
		return 0
	}
	loss := input - output
	return uint16(min(uint64(10_000), (loss*10_000+input-1)/input))
}

func shadowMarketCurveElapsedMillis(from, through time.Time) uint32 {
	if through.Before(from) {
		return ^uint32(0)
	}
	millis := through.Sub(from) / time.Millisecond
	if millis > time.Duration(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(millis)
}

func shadowMarketCurvePriceImpactValid(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	digits, integerDigits, dots := 0, 0, 0
	for index, character := range value {
		if character == '-' && index == 0 {
			continue
		}
		if character == '.' {
			dots++
			if dots > 1 || integerDigits == 0 || index == len(value)-1 {
				return false
			}
			continue
		}
		if character < '0' || character > '9' {
			return false
		}
		digits++
		if dots == 0 {
			integerDigits++
		}
	}
	return digits != 0
}
