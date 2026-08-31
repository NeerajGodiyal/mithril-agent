package marketadmission

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const tokenMintBytes = uint64(82)

type PriceSource interface {
	Latest(context.Context, string) (pricetrigger.Sample, error)
}

type PythSource interface {
	LatestObservation(context.Context, string) (pricesource.PythObservation, error)
}

type QuoteSource interface {
	Quote(context.Context, jupiterquote.Request) (jupiterquote.Result, error)
}

type Sources struct {
	Mint            pricesource.AccountReader
	MarketPrimary   PythSource
	MarketSecondary PriceSource
	USDCPrimary     PythSource
	USDCSecondary   PriceSource
	SOLPrimary      PythSource
	SOLSecondary    PriceSource
	Quotes          QuoteSource
}

type Collector struct {
	opening Opening
	sources Sources
	now     func() time.Time
}

func NewCollector(opening Opening, sources Sources, now func() time.Time) (*Collector, error) {
	if err := opening.Validate(); err != nil {
		return nil, err
	}
	if sources.Mint == nil || sources.MarketPrimary == nil || sources.MarketSecondary == nil ||
		sources.USDCPrimary == nil || sources.USDCSecondary == nil ||
		sources.SOLPrimary == nil || sources.SOLSecondary == nil || sources.Quotes == nil {
		return nil, errors.New("market evidence sources are incomplete")
	}
	if now == nil {
		now = time.Now
	}
	return &Collector{opening: opening, sources: sources, now: now}, nil
}

// RunTick records one scheduled bucket. Provider errors become bounded failure
// codes instead of leaking endpoints, credentials, or provider error text.
func (collector *Collector) RunTick(ctx context.Context, bucket time.Time) Observation {
	bucket = bucket.UTC()
	observation := Observation{
		Version: Version, OpeningSHA256: collector.opening.ContentSHA256, Bucket: bucket,
	}
	deadline := bucket.Add(
		time.Duration(collector.opening.Thresholds.CadenceSeconds)*time.Second - 5*time.Second,
	)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	fail := func(code string) Observation {
		observation.Failure = code
		observation.ObservedAt = collector.now().UTC()
		return observation
	}

	var err error
	observation.Mint, err = observeMint(ctx, collector.sources.Mint, collector.opening.Candidate)
	if err != nil {
		return fail(FailureMintState)
	}
	observation.MarketPrimary, err = collector.sources.MarketPrimary.LatestObservation(
		ctx, collector.opening.Candidate.Pyth.Feed,
	)
	if err != nil {
		return fail(FailureMarketPrice)
	}
	observation.MarketSecondary, err = collector.sources.MarketSecondary.Latest(
		ctx, collector.opening.Candidate.Kraken.Feed,
	)
	if err != nil {
		return fail(FailureMarketPrice)
	}
	observation.USDCPrimary, err = collector.sources.USDCPrimary.LatestObservation(
		ctx, pricetrigger.FeedUSDCUSD,
	)
	if err != nil {
		return fail(FailureQuotePeg)
	}
	observation.USDCSecondary, err = collector.sources.USDCSecondary.Latest(
		ctx, pricetrigger.FeedUSDCUSD,
	)
	if err != nil {
		return fail(FailureQuotePeg)
	}
	observation.SOLPrimary, err = collector.sources.SOLPrimary.LatestObservation(
		ctx, pricetrigger.FeedSOLUSD,
	)
	if err != nil {
		return fail(FailureNativePrice)
	}
	observation.SOLSecondary, err = collector.sources.SOLSecondary.Latest(
		ctx, pricetrigger.FeedSOLUSD,
	)
	if err != nil {
		return fail(FailureNativePrice)
	}

	candidate := collector.opening.Candidate
	buyStarted := collector.now().UTC()
	buy, err := collector.sources.Quotes.Quote(ctx, jupiterquote.Request{
		Taker: collector.opening.Observe, InputMint: candidate.QuoteMint,
		OutputMint: candidate.BaseMint, InputAmount: candidate.QuoteNotionalUSDC,
		SlippageBPS: candidate.QuoteSlippageBPS,
	})
	buyFinished := collector.now().UTC()
	if err != nil {
		return fail(FailureBuyQuote)
	}
	observation.Buy = quoteObservation(
		candidate.QuoteMint, candidate.BaseMint, buy,
		elapsedMillis(buyStarted, buyFinished),
	)

	sellStarted := collector.now().UTC()
	sell, err := collector.sources.Quotes.Quote(ctx, jupiterquote.Request{
		Taker: collector.opening.Observe, InputMint: candidate.BaseMint,
		OutputMint: candidate.QuoteMint, InputAmount: buy.EstimatedOutput,
		SlippageBPS: candidate.QuoteSlippageBPS,
	})
	sellFinished := collector.now().UTC()
	if err != nil {
		return fail(FailureSellQuote)
	}
	observation.Sell = quoteObservation(
		candidate.BaseMint, candidate.QuoteMint, sell,
		elapsedMillis(sellStarted, sellFinished),
	)
	observation.ObservedAt = sellFinished
	return observation
}

func observeMint(
	ctx context.Context,
	reader pricesource.AccountReader,
	candidate Candidate,
) (MintEvidence, error) {
	account, err := reader.AccountSlice(ctx, candidate.BaseMint, 0, 0, tokenMintBytes)
	if err != nil {
		return MintEvidence{}, errors.New("read market mint")
	}
	evidence := MintEvidence{
		Address: candidate.BaseMint, Owner: account.Owner,
		ContextSlot: account.ContextSlot,
	}
	if len(account.Data) != int(tokenMintBytes) || account.DataLength != tokenMintBytes {
		return evidence, errors.New("market mint length is invalid")
	}
	hash := sha256.Sum256(account.Data)
	evidence.DataSHA256 = hex.EncodeToString(hash[:])
	if account.Data[45] != 1 {
		return evidence, errors.New("market mint is not initialized")
	}
	evidence.Decimals = account.Data[44]
	var ok bool
	evidence.MintAuthority, ok = decodeAuthority(account.Data[0:36])
	if !ok {
		return evidence, errors.New("market mint authority is invalid")
	}
	evidence.FreezeAuthority, ok = decodeAuthority(account.Data[46:82])
	if !ok {
		return evidence, errors.New("market freeze authority is invalid")
	}
	if err := evidence.Validate(candidate); err != nil {
		return evidence, err
	}
	return evidence, nil
}

func decodeAuthority(data []byte) (string, bool) {
	if len(data) != 36 {
		return "", false
	}
	switch binary.LittleEndian.Uint32(data[:4]) {
	case 0:
		return "", true
	case 1:
		return solana.Encode(data[4:]), true
	default:
		return "", false
	}
}

func quoteObservation(
	inputMint, outputMint string,
	result jupiterquote.Result,
	latency uint32,
) Quote {
	return Quote{
		InputMint: inputMint, OutputMint: outputMint,
		InputAmount: result.InputAmount, EstimatedOutput: result.EstimatedOutput,
		MinimumOutput: result.MinimumOutput, ReceivedAt: result.ReceivedAt,
		LatencyMillis: latency, ResponseSHA256: result.ResponseSHA256,
	}
}

func elapsedMillis(from, through time.Time) uint32 {
	if through.Before(from) {
		return ^uint32(0)
	}
	millis := through.Sub(from) / time.Millisecond
	if millis > time.Duration(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(millis)
}
