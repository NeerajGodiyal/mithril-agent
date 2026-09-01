package marketadmission

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

const testObserve = "11111111111111111111111111111111"

func TestCatalogPinsCanonicalCandidateMetadata(t *testing.T) {
	markets := Markets()
	if strings.Join(markets, ",") != "WIF/USDC,JTO/USDC,PYTH/USDC" {
		t.Fatalf("markets = %v", markets)
	}
	for _, market := range markets {
		candidate, ok := Lookup(market)
		if !ok || candidate.Validate() != nil || candidate.BaseMint == "" ||
			candidate.Pyth.UpgradedAccount == "" || candidate.Kraken.Product == "" ||
			!candidate.MintAuthorityDisabled || !candidate.FreezeAuthorityDisabled {
			t.Fatalf("candidate %s = %+v", market, candidate)
		}
	}
	if _, ok := Lookup("BONK/USDC"); ok {
		t.Fatal("an unreviewed market appeared on the operator allowlist")
	}
	if _, ok := Lookup("RAY/USDC"); ok {
		t.Fatal("a market without a sponsored Solana push feed appeared on the allowlist")
	}
}

func TestEvaluateCountsMissingAndFailedBuckets(t *testing.T) {
	candidate, _ := Lookup(MarketWIFUSDC)
	thresholds := DefaultThresholds()
	thresholds.CadenceSeconds = 3600
	opening := testOpening(t, candidate, thresholds)
	from := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	through := from.Add(30 * 24 * time.Hour)
	observations := observationsFor(t, opening, from, through, 10)
	observations = observations[:len(observations)-4]
	for index := 0; index < 4; index++ {
		observations[index].Failure = FailureBuyQuote
	}
	artifact, err := evaluate(opening, from, through, testPrefix(), observations)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.OperationallyQualified || artifact.ObservedBuckets != 716 ||
		artifact.AvailableBuckets != 712 || artifact.AvailabilityBPS != 9_888 {
		t.Fatalf("artifact = %+v", artifact)
	}
}

func TestEvaluateMeasuresBidirectionalRouteCost(t *testing.T) {
	candidate, _ := Lookup(MarketWIFUSDC)
	thresholds := DefaultThresholds()
	thresholds.CadenceSeconds = 3600
	opening := testOpening(t, candidate, thresholds)
	from := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	through := from.Add(30 * 24 * time.Hour)
	artifact, err := evaluate(
		opening, from, through, testPrefix(), observationsFor(t, opening, from, through, 10),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !artifact.OperationallyQualified || artifact.AvailabilityBPS != 10_000 ||
		artifact.ExpectedBuckets != 720 || artifact.MedianRouteCostBPS != 10 ||
		artifact.P95RouteCostBPS != 10 {
		t.Fatalf("artifact = %+v", artifact)
	}
}

func TestArtifactIsDerivedFromItsExactJournalPrefix(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "admission.jsonl")
	candidate, _ := Lookup(MarketWIFUSDC)
	opening, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	store, err := journal.OpenRotating(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(from, EventOpened, opening.ContentSHA256, opening); err != nil {
		t.Fatal(err)
	}
	failed := Observation{
		Version: Version, OpeningSHA256: opening.ContentSHA256,
		Bucket: from, ObservedAt: from.Add(time.Second), Failure: FailureMintState,
	}
	if _, err := store.Append(
		failed.ObservedAt, EventObserved, failed.Bucket.Format(time.RFC3339), failed,
	); err != nil {
		t.Fatal(err)
	}
	prefix, err := store.DurablePrefix()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	artifact, err := EvaluateJournal(path, prefix, time.Date(2026, time.January, 31, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.OperationallyQualified || artifact.ObservedBuckets != 1 ||
		artifact.AvailableBuckets != 0 || artifact.Validate() != nil ||
		artifact.VerifyJournal(path) != nil {
		t.Fatalf("artifact = %+v", artifact)
	}
	tampered := prefix
	tampered.ChainHeadSHA256 = strings.Repeat("f", 64)
	if _, err := EvaluateJournal(path, tampered, time.Now()); err == nil {
		t.Fatal("foreign journal prefix was accepted")
	}
}

func TestJournalMetadataIsBoundToEvidencePayloads(t *testing.T) {
	candidate, _ := Lookup(MarketWIFUSDC)
	opening, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	bucket := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	observation := Observation{
		Version: Version, OpeningSHA256: opening.ContentSHA256,
		Bucket: bucket, ObservedAt: bucket.Add(time.Second), Failure: FailureMintState,
	}
	openingPayload, err := json.Marshal(opening)
	if err != nil {
		t.Fatal(err)
	}
	observationPayload, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	valid := []journal.Record{
		{Type: EventOpened, ActionID: opening.ContentSHA256, Payload: openingPayload},
		{
			At: observation.ObservedAt, Type: EventObserved,
			ActionID: observation.Bucket.Format(time.RFC3339), Payload: observationPayload,
		},
	}
	if _, _, err := decodeRecords(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func([]journal.Record){
		"opening action": func(records []journal.Record) { records[0].ActionID = "wrong" },
		"observation time": func(records []journal.Record) {
			records[1].At = observation.ObservedAt.Add(time.Second)
		},
		"observation action": func(records []journal.Record) { records[1].ActionID = "wrong" },
	} {
		t.Run(name, func(t *testing.T) {
			records := append([]journal.Record(nil), valid...)
			mutate(records)
			if _, _, err := decodeRecords(records); err == nil {
				t.Fatal("mismatched journal metadata was accepted")
			}
		})
	}
}

func TestPythAndMintProvenanceAreRequired(t *testing.T) {
	candidate, _ := Lookup(MarketWIFUSDC)
	opening, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	bucket := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	observation := observationsFor(t, opening, bucket, bucket.Add(time.Minute), 10)[0]
	if _, ok := usableObservation(opening, observation); !ok {
		t.Fatal("valid observation was not usable")
	}
	for name, mutate := range map[string]func(*Observation){
		"mint owner": func(value *Observation) {
			value.Mint.Owner = "11111111111111111111111111111111"
		},
		"Pyth account": func(value *Observation) { value.MarketPrimary.Account = candidate.BaseMint },
		"Pyth feed": func(value *Observation) {
			value.MarketPrimary.FeedID = strings.Repeat("0", 64)
		},
		"Pyth slot": func(value *Observation) { value.MarketPrimary.ContextSlot = 0 },
		"buy slippage floor": func(value *Observation) {
			value.Buy.MinimumOutput++
		},
		"sell slippage floor": func(value *Observation) {
			value.Sell.MinimumOutput--
		},
		"internally consistent but off-market route": func(value *Observation) {
			value.Buy.EstimatedOutput = 1_000_000
			value.Buy.MinimumOutput = slippageFloor(1_000_000, candidate.QuoteSlippageBPS)
			value.Sell.InputAmount = 1_000_000
		},
	} {
		t.Run(name, func(t *testing.T) {
			tampered := observation
			mutate(&tampered)
			if _, ok := usableObservation(opening, tampered); ok {
				t.Fatal("tampered observation was usable")
			}
		})
	}
}

func testOpening(t *testing.T, candidate Candidate, thresholds Thresholds) Opening {
	t.Helper()
	candidateHash, err := candidate.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	opening := Opening{
		Version: Version, Candidate: candidate, CandidateSHA256: candidateHash,
		Observe: testObserve, Thresholds: thresholds,
	}
	opening.ContentSHA256, err = openingFingerprint(opening)
	if err != nil {
		t.Fatal(err)
	}
	return opening
}

func observationsFor(
	t *testing.T,
	opening Opening,
	from, through time.Time,
	costBPS uint16,
) []Observation {
	t.Helper()
	candidate := opening.Candidate
	primary, err := candidate.Pyth.IdentitySHA256()
	if err != nil {
		t.Fatal(err)
	}
	secondary, err := candidate.Kraken.IdentitySHA256()
	if err != nil {
		t.Fatal(err)
	}
	cadence := time.Duration(opening.Thresholds.CadenceSeconds) * time.Second
	result := make([]Observation, 0, int(through.Sub(from)/cadence))
	for bucket := from; bucket.Before(through); bucket = bucket.Add(cadence) {
		observed := bucket.Add(time.Second)
		baseOutput := uint64(125_000_000)
		sellOutput := candidate.QuoteNotionalUSDC * uint64(10_000-costBPS) / 10_000
		result = append(result, Observation{
			Version: Version, OpeningSHA256: opening.ContentSHA256,
			Bucket: bucket, ObservedAt: observed,
			Mint: MintEvidence{
				Address: candidate.BaseMint, Owner: candidate.TokenProgram,
				Decimals: candidate.BaseDecimals, ContextSlot: 100,
				DataSHA256: strings.Repeat("d", 64),
			},
			MarketPrimary:   pythObservation(candidate.Pyth, primary, 200_000, observed),
			MarketSecondary: sample(secondary, candidate.Pyth.Feed, 200_000, observed),
			USDCPrimary: pythObservation(
				pricesource.PythPushUSDCSpec(), pricesource.PythPushUSDCIdentitySHA256(),
				1_000_000, observed,
			),
			USDCSecondary: sample(
				pricesource.KrakenIdentitySHA256(), pricetrigger.FeedUSDCUSD, 1_000_000, observed,
			),
			SOLPrimary: pythObservation(
				pricesource.PythPushSOLSpec(), pricesource.PythPushIdentitySHA256(),
				200_000_000, observed,
			),
			SOLSecondary: sample(
				pricesource.KrakenSOLIdentitySHA256(), pricetrigger.FeedSOLUSD, 200_000_000, observed,
			),
			Buy: Quote{
				InputMint: candidate.QuoteMint, OutputMint: candidate.BaseMint,
				InputAmount: candidate.QuoteNotionalUSDC, EstimatedOutput: baseOutput,
				MinimumOutput: slippageFloor(baseOutput, candidate.QuoteSlippageBPS),
				ReceivedAt:    observed.Add(-time.Millisecond),
				LatencyMillis: 20, ResponseSHA256: strings.Repeat("a", 64),
			},
			Sell: Quote{
				InputMint: candidate.BaseMint, OutputMint: candidate.QuoteMint,
				InputAmount: baseOutput, EstimatedOutput: sellOutput,
				MinimumOutput: slippageFloor(sellOutput, candidate.QuoteSlippageBPS),
				ReceivedAt:    observed.Add(-time.Millisecond),
				LatencyMillis: 20, ResponseSHA256: strings.Repeat("b", 64),
			},
		})
	}
	return result
}

func slippageFloor(estimated uint64, bps uint16) uint64 {
	return (estimated*uint64(10_000-bps) + 9_999) / 10_000
}

func pythObservation(
	spec pricesource.PythPushSpec,
	identity string,
	price uint64,
	observed time.Time,
) pricesource.PythObservation {
	return pricesource.PythObservation{
		Sample:      sample(identity, spec.Feed, price, observed),
		ContextSlot: 100, Account: spec.LegacyAccount, FeedID: spec.FeedID,
	}
}

func sample(identity, feed string, price uint64, observed time.Time) pricetrigger.Sample {
	return pricetrigger.Sample{
		SourceSHA256: identity, Feed: feed, PriceMicros: price,
		ConfidenceMicros: 1, PublishedAt: observed.Add(-time.Second),
	}
}

func testPrefix() journal.DurablePrefix {
	return journal.DurablePrefix{
		Format: journal.Format, Bytes: 100, Records: 1,
		ChainHeadSHA256: strings.Repeat("c", 64),
	}
}
