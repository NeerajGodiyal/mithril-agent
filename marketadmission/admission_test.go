package marketadmission

import (
	"encoding/json"
	"path/filepath"
	"reflect"
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

func TestPreviousEvidenceVersionCannotResume(t *testing.T) {
	candidate, _ := Lookup(MarketWIFUSDC)
	current, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	previous := current
	previous.Version--
	previous.Candidate.Version--
	previous.Thresholds.Version--
	previous.CandidateSHA256, err = digest(previous.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	previous.ContentSHA256, err = openingFingerprint(previous)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	if previous.Validate() == nil {
		t.Fatal("previous market evidence version was accepted")
	}
	if _, err := ValidateResume([]journal.Record{{
		Type: EventOpened, ActionID: previous.ContentSHA256, Payload: payload,
	}}, current); err == nil {
		t.Fatal("previous market evidence journal resumed")
	}
}

func TestEvaluateCountsMissingAndFailedBuckets(t *testing.T) {
	candidate, _ := Lookup(MarketWIFUSDC)
	thresholds := DefaultThresholds()
	opening := testOpening(t, candidate, thresholds)
	from := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	through := from.Add(30 * 24 * time.Hour)
	observations := observationsFor(t, opening, from, through, 10)
	observations = observations[:len(observations)-220]
	for index := 0; index < 220; index++ {
		observations[index].Failure = FailureBuyQuote
	}
	artifact, err := evaluate(opening, from, through, testPrefix(), observations)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.OperationallyQualified || artifact.ObservedBuckets != 42_980 ||
		artifact.AvailableBuckets != 42_760 || artifact.AvailabilityBPS != 9_898 {
		t.Fatalf("artifact = %+v", artifact)
	}
}

func TestEvaluateMeasuresBidirectionalRouteCost(t *testing.T) {
	candidate, _ := Lookup(MarketWIFUSDC)
	opening, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	through := from.Add(30 * 24 * time.Hour)
	artifact, err := evaluate(
		opening, from, through, testPrefix(), observationsFor(t, opening, from, through, 10),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !artifact.OperationallyQualified || artifact.AvailabilityBPS != 10_000 ||
		artifact.ExpectedBuckets != 43_200 || artifact.MedianRouteCostBPS != 10 ||
		artifact.P95RouteCostBPS != 10 {
		t.Fatalf("artifact = %+v", artifact)
	}
}

func TestDiagnoseMeasuresAPartialWindowWithoutQualifyingIt(t *testing.T) {
	candidate, _ := Lookup(MarketWIFUSDC)
	opening, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	through := time.Date(2026, time.September, 2, 12, 45, 0, 0, time.UTC)
	from := through.Truncate(time.Minute).Add(-6 * time.Hour)
	observations := observationsFor(t, opening, from, through.Truncate(time.Minute), 12)
	observations[0].Failure = FailureSellQuote
	observations = observations[:len(observations)-1]
	diagnostic, err := diagnose(opening, observations, through, 6*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !diagnostic.DiagnosticOnly || diagnostic.OperationallyQualified ||
		diagnostic.From != from || diagnostic.Through != through.Truncate(time.Minute) ||
		diagnostic.ExpectedBuckets != 360 || diagnostic.ObservedBuckets != 359 ||
		diagnostic.AvailableBuckets != 358 || diagnostic.AvailabilityBPS != 9_944 ||
		diagnostic.MedianRouteCostBPS != 12 || diagnostic.P95RouteCostBPS != 12 ||
		diagnostic.FailureCounts["missing_bucket"] != 1 ||
		diagnostic.FailureCounts[FailureSellQuote] != 1 {
		t.Fatalf("diagnostic = %+v", diagnostic)
	}
	if _, err := diagnose(opening, observations, through, 30*time.Minute); err == nil {
		t.Fatal("sub-hour diagnostic window was accepted")
	}
	if _, err := diagnose(opening, observations, through, 90*time.Minute); err == nil {
		t.Fatal("fractional-hour diagnostic window was accepted")
	}
}

func TestDiagnoseNamesRejectedEvidenceCause(t *testing.T) {
	candidate, _ := Lookup(MarketWIFUSDC)
	opening, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	through := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	from := through.Add(-time.Hour)
	observations := observationsFor(t, opening, from, through, 12)
	observations[0].MarketSecondary.PublishedAt =
		observations[0].MarketPrimary.Sample.PublishedAt.Add(-76 * time.Second)
	observations[1].MarketSecondary.PriceMicros =
		observations[1].MarketPrimary.Sample.PriceMicros * 2
	diagnostic, err := diagnose(opening, observations, through, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if diagnostic.AvailableBuckets != 58 ||
		diagnostic.FailureCounts["market_source_time_alignment_rejected"] != 1 ||
		diagnostic.FailureCounts["market_source_price_disagreement_rejected"] != 1 {
		t.Fatalf("diagnostic = %+v", diagnostic)
	}
}

func TestSourceAlignmentAcceptsOneHeartbeatAndSlack(t *testing.T) {
	candidate, _ := Lookup(MarketWIFUSDC)
	opening, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	through := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	from := through.Add(-time.Hour)
	observations := observationsFor(t, opening, from, through, 12)
	observations[0].MarketSecondary.PublishedAt =
		observations[0].MarketPrimary.Sample.PublishedAt.Add(-75 * time.Second)
	observations[1].MarketSecondary.PublishedAt =
		observations[1].MarketPrimary.Sample.PublishedAt.Add(-76 * time.Second)
	diagnostic, err := diagnose(opening, observations, through, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if diagnostic.AvailableBuckets != 59 ||
		diagnostic.FailureCounts["market_source_time_alignment_rejected"] != 1 {
		t.Fatalf("diagnostic = %+v", diagnostic)
	}
}

func TestProvisionalArtifactIsTwoHoursPaperOnlyAndExpiresQuickly(t *testing.T) {
	candidate, _ := Lookup(MarketWIFUSDC)
	opening, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	through := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	from := through.Add(-2 * time.Hour)
	observations := observationsFor(t, opening, from, through, 10)
	for index := 0; index < 6; index++ {
		observations[index].Failure = FailureBuyQuote
	}
	artifact, err := evaluateProvisional(opening, from, through, testPrefix(), observations)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Status != ProvisionalStatus || !artifact.PaperOnly || artifact.Authorized ||
		!artifact.ProvisionalPaperReady || artifact.ExpectedBuckets != 120 ||
		artifact.ObservedBuckets != 120 || artifact.AvailableBuckets != 114 ||
		artifact.AvailabilityBPS != ProvisionalMinimumAvailabilityBPS ||
		artifact.Validate() != nil {
		t.Fatalf("provisional artifact = %+v", artifact)
	}
	if !artifact.Current(through.Add(30*time.Second)) ||
		artifact.Current(through.Add(3*time.Minute)) || artifact.Current(through.Add(24*time.Hour)) {
		t.Fatal("provisional artifact freshness crossed its bounded startup window")
	}
	observations[6].Failure = FailureBuyQuote
	failed, err := evaluateProvisional(opening, from, through, testPrefix(), observations)
	if err != nil {
		t.Fatal(err)
	}
	if failed.ProvisionalPaperReady || failed.AvailabilityBPS >= ProvisionalMinimumAvailabilityBPS ||
		failed.Validate() != nil {
		t.Fatalf("under-covered provisional artifact = %+v", failed)
	}
	tampered := artifact
	tampered.Authorized = true
	if tampered.Validate() == nil {
		t.Fatal("an authorized provisional artifact was accepted")
	}
}

func TestDiagnosticProvisionalReadinessUsesTheSharedCoverageAndCostGates(t *testing.T) {
	diagnostic := Diagnostic{
		AvailableBuckets: 114, AvailabilityBPS: ProvisionalMinimumAvailabilityBPS,
		MedianRouteCostBPS: DefaultThresholds().MedianRouteCostBPS,
		P95RouteCostBPS:    DefaultThresholds().P95RouteCostBPS,
	}
	if !diagnostic.ReadyForProvisionalPaperCheck() {
		t.Fatal("the exact provisional thresholds were rejected")
	}
	if reasons := diagnostic.ProvisionalPaperCheckReasons(); len(reasons) != 0 {
		t.Fatalf("exact provisional thresholds have reasons %q", reasons)
	}
	diagnostic.P95RouteCostBPS++
	if reasons := diagnostic.ProvisionalPaperCheckReasons(); diagnostic.ReadyForProvisionalPaperCheck() ||
		len(reasons) != 1 || reasons[0] != "p95 round-trip route cost exceeds the limit" {
		t.Fatalf("over-cost diagnostic reasons = %q", reasons)
	}
	diagnostic.P95RouteCostBPS = DefaultThresholds().P95RouteCostBPS
	diagnostic.AvailabilityBPS--
	if reasons := diagnostic.ProvisionalPaperCheckReasons(); diagnostic.ReadyForProvisionalPaperCheck() ||
		len(reasons) != 1 || reasons[0] != "two-hour bidirectional availability is below the paper-testing minimum" {
		t.Fatalf("under-covered diagnostic reasons = %q", reasons)
	}
}

func TestProvisionalReplayPointsBindTheExactPrefixAndPreserveGaps(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "provisional-replay.jsonl")
	candidate, _ := Lookup(MarketWIFUSDC)
	opening, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	through := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	from := through.Add(-2 * time.Hour)
	observations := observationsFor(t, opening, from, through, 10)
	observations[1].Failure = FailureBuyQuote
	store, err := journal.OpenRotating(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(from, EventOpened, opening.ContentSHA256, opening); err != nil {
		t.Fatal(err)
	}
	for index, observation := range observations {
		if index == 2 {
			continue
		}
		if _, err := store.Append(
			observation.ObservedAt, EventObserved,
			observation.Bucket.Format(time.RFC3339), observation,
		); err != nil {
			t.Fatal(err)
		}
	}
	prefix, err := store.DurablePrefix()
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := EvaluateProvisionalJournal(path, prefix, through.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	points, err := artifact.ReplayPoints(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 120 || !points[0].Available || points[1].Available ||
		points[1].At != observations[1].ObservedAt.UTC() || points[2].Available ||
		points[2].At != points[2].Bucket.Add(time.Minute) ||
		points[1].MarketPrimaryPublishedAt != observations[1].MarketPrimary.Sample.PublishedAt.UTC() ||
		points[1].MarketSecondaryPublishedAt != observations[1].MarketSecondary.PublishedAt.UTC() ||
		!reflect.DeepEqual(points[1].MarketPrimary, pricetrigger.Sample{}) ||
		!reflect.DeepEqual(points[1].NativePrimary, pricetrigger.Sample{}) ||
		points[3].MarketPrimary.PriceMicros == 0 || points[3].NativePrimary.PriceMicros == 0 {
		t.Fatalf("replay points = %+v", points[:4])
	}
	if _, err := store.Append(
		through, "market_admission.unsupported", "tail", map[string]string{"invalid": "tail"},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := artifact.ReplayPoints(path)
	if err != nil || !reflect.DeepEqual(points, again) {
		t.Fatal("an appended journal tail changed the exact-prefix replay")
	}
	tampered := artifact
	tampered.Journal.ChainHeadSHA256 = strings.Repeat("f", 64)
	if _, err := tampered.ReplayPoints(path); err == nil {
		t.Fatal("a foreign replay prefix was accepted")
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
			if _, reason, ok := observationUsability(opening, tampered); ok || reason == "" {
				t.Fatalf("tampered observation usability = %t, reason = %q", ok, reason)
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
