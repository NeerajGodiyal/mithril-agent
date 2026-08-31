package marketadmission

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

type priceStub struct {
	sample pricetrigger.Sample
	err    error
}

func (source priceStub) Latest(_ context.Context, feed string) (pricetrigger.Sample, error) {
	if source.err != nil {
		return pricetrigger.Sample{}, source.err
	}
	result := source.sample
	result.Feed = feed
	return result, nil
}

type pythStub struct {
	observation pricesource.PythObservation
	err         error
}

func (source pythStub) LatestObservation(
	_ context.Context,
	feed string,
) (pricesource.PythObservation, error) {
	if source.err != nil {
		return pricesource.PythObservation{}, source.err
	}
	result := source.observation
	result.Sample.Feed = feed
	return result, nil
}

type accountStub struct {
	account pricesource.AccountData
	err     error
}

func (source accountStub) AccountSlice(
	_ context.Context,
	_ string,
	_, _, _ uint64,
) (pricesource.AccountData, error) {
	return source.account, source.err
}

type quoteStub struct {
	results  []jupiterquote.Result
	requests []jupiterquote.Request
}

func (source *quoteStub) Quote(
	_ context.Context,
	request jupiterquote.Request,
) (jupiterquote.Result, error) {
	source.requests = append(source.requests, request)
	if len(source.results) == 0 {
		return jupiterquote.Result{}, errors.New("unavailable")
	}
	result := source.results[0]
	source.results = source.results[1:]
	return result, nil
}

func TestCollectorRecordsMintPricesAndBothQuoteDirections(t *testing.T) {
	candidate, _ := Lookup(MarketWIFUSDC)
	opening, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	bucket := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	marketPrimary, _ := candidate.Pyth.IdentitySHA256()
	marketSecondary, _ := candidate.Kraken.IdentitySHA256()
	quotes := &quoteStub{results: []jupiterquote.Result{
		{
			InputAmount: candidate.QuoteNotionalUSDC, EstimatedOutput: 125_000_000,
			MinimumOutput: 123_750_000, ReceivedAt: bucket.Add(2 * time.Second),
			ResponseSHA256: strings.Repeat("a", 64),
		},
		{
			InputAmount: 125_000_000, EstimatedOutput: 24_975_000,
			MinimumOutput: 24_725_250, ReceivedAt: bucket.Add(4 * time.Second),
			ResponseSHA256: strings.Repeat("b", 64),
		},
	}}
	times := []time.Time{
		bucket.Add(time.Second), bucket.Add(2 * time.Second),
		bucket.Add(3 * time.Second), bucket.Add(4 * time.Second),
	}
	now := func() time.Time {
		result := times[0]
		times = times[1:]
		return result
	}
	collector, err := NewCollector(opening, Sources{
		Mint: accountStub{account: validMintAccount(candidate)},
		MarketPrimary: pythStub{observation: pythObservation(
			candidate.Pyth, marketPrimary, 200_000, bucket.Add(time.Second),
		)},
		MarketSecondary: priceStub{sample: testCollectorSample(marketSecondary, bucket)},
		USDCPrimary: pythStub{observation: pythObservation(
			pricesource.PythPushUSDCSpec(), pricesource.PythPushUSDCIdentitySHA256(),
			1_000_000, bucket.Add(time.Second),
		)},
		USDCSecondary: priceStub{sample: testCollectorSample(
			pricesource.KrakenIdentitySHA256(), bucket,
		)},
		SOLPrimary: pythStub{observation: pythObservation(
			pricesource.PythPushSOLSpec(), pricesource.PythPushIdentitySHA256(),
			200_000_000, bucket.Add(time.Second),
		)},
		SOLSecondary: priceStub{sample: testCollectorSample(
			pricesource.KrakenSOLIdentitySHA256(), bucket,
		)},
		Quotes: quotes,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	observation := collector.RunTick(t.Context(), bucket)
	if observation.Failure != "" || !observation.ObservedAt.Equal(bucket.Add(4*time.Second)) ||
		observation.Mint.ContextSlot != 99 || observation.Mint.DataSHA256 == "" ||
		observation.Buy.LatencyMillis != 1_000 || observation.Sell.LatencyMillis != 1_000 ||
		observation.Buy.ResponseSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("observation = %+v", observation)
	}
	if len(quotes.requests) != 2 || quotes.requests[1].InputAmount != 125_000_000 ||
		quotes.requests[0].Taker != testObserve ||
		quotes.requests[0].InputMint != candidate.QuoteMint ||
		quotes.requests[1].InputMint != candidate.BaseMint {
		t.Fatalf("quote requests = %+v", quotes.requests)
	}
}

func TestCollectorTurnsProviderAndAuthorityFailuresIntoBoundedCodes(t *testing.T) {
	candidate, _ := Lookup(MarketWIFUSDC)
	opening, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	bucket := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	sources := Sources{
		Mint:            accountStub{account: validMintAccount(candidate)},
		MarketPrimary:   pythStub{err: errors.New("secret endpoint failed")},
		MarketSecondary: priceStub{}, USDCPrimary: pythStub{}, USDCSecondary: priceStub{},
		SOLPrimary: pythStub{}, SOLSecondary: priceStub{}, Quotes: &quoteStub{},
	}
	collector, err := NewCollector(opening, sources, func() time.Time { return bucket.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	observation := collector.RunTick(t.Context(), bucket)
	if observation.Failure != FailureMarketPrice || strings.Contains(observation.Failure, "secret") {
		t.Fatalf("observation = %+v", observation)
	}

	badMint := validMintAccount(candidate)
	badMint.Data[0] = 1
	badMint.Data[4] = 9
	sources.Mint = accountStub{account: badMint}
	collector, err = NewCollector(opening, sources, func() time.Time { return bucket.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	if got := collector.RunTick(t.Context(), bucket); got.Failure != FailureMintState ||
		got.Mint.MintAuthority == "" {
		t.Fatalf("mint failure = %+v", got)
	}
}

func validMintAccount(candidate Candidate) pricesource.AccountData {
	data := make([]byte, tokenMintBytes)
	data[44] = candidate.BaseDecimals
	data[45] = 1
	return pricesource.AccountData{
		ContextSlot: 99, Owner: candidate.TokenProgram,
		DataLength: tokenMintBytes, Data: data,
	}
}

func testCollectorSample(identity string, at time.Time) pricetrigger.Sample {
	return pricetrigger.Sample{
		SourceSHA256: identity, PriceMicros: 1_000_000,
		ConfidenceMicros: 1, PublishedAt: at,
	}
}
