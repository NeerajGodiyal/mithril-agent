package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/marketadmission"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestShadowMarketEvaluateWritesOneImmutableArtifact(t *testing.T) {
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "wif-admission.jsonl")
	outPath := filepath.Join(directory, "wif-admission.json")
	candidate, ok := marketadmission.Lookup(marketadmission.MarketWIFUSDC)
	if !ok {
		t.Fatal("WIF admission candidate missing")
	}
	thresholds := marketadmission.DefaultThresholds()
	opening, err := marketadmission.NewOpening(
		candidate, "11111111111111111111111111111111", thresholds,
	)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	store, err := journal.OpenRotating(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(from, marketadmission.EventOpened, opening.ContentSHA256, opening); err != nil {
		t.Fatal(err)
	}
	observation := marketadmission.Observation{
		Version: marketadmission.Version, OpeningSHA256: opening.ContentSHA256,
		Bucket: from, ObservedAt: from.Add(time.Second),
		Failure: marketadmission.FailureMarketPrice,
	}
	if _, err := store.Append(
		observation.ObservedAt,
		marketadmission.EventObserved,
		from.Format(time.RFC3339),
		observation,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	args := []string{
		"--journal", journalPath,
		"--out", outPath,
	}
	if err := runShadowMarketEvaluate(args, &output); err != nil {
		t.Fatal(err)
	}
	var artifact marketadmission.Artifact
	if err := readStrictJSON(outPath, &artifact); err != nil {
		t.Fatal(err)
	}
	if err := artifact.Validate(); err != nil {
		t.Fatal(err)
	}
	if artifact.OperationallyQualified || artifact.ExpectedBuckets != 30*24*60 ||
		artifact.ObservedBuckets != 1 || artifact.AvailableBuckets != 0 {
		t.Fatalf("unexpected artifact: %+v", artifact)
	}
	if !strings.Contains(output.String(), `"operationally_qualified":false`) {
		t.Fatalf("unexpected output: %s", output.String())
	}
	if err := runShadowMarketEvaluate(args, &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected immutable output refusal, got %v", err)
	}
}

func TestMarketAdmissionJournalCannotChangeItsOpening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admission.jsonl")
	wif, _ := marketadmission.Lookup(marketadmission.MarketWIFUSDC)
	thresholds := marketadmission.DefaultThresholds()
	wifOpening, err := marketadmission.NewOpening(
		wif, "11111111111111111111111111111111", thresholds,
	)
	if err != nil {
		t.Fatal(err)
	}
	otherOpening, err := marketadmission.NewOpening(
		wif, "So11111111111111111111111111111111111111112", thresholds,
	)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.OpenRotating(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareMarketAdmissionJournal(store, wifOpening, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = journal.OpenRotating(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := prepareMarketAdmissionJournal(store, otherOpening, time.Now()); err == nil ||
		!strings.Contains(err.Error(), "another opening") {
		t.Fatalf("expected market mismatch, got %v", err)
	}
}

func TestShadowMarketEvaluateNeverCreatesOrRepairsEvidence(t *testing.T) {
	directory := t.TempDir()
	missing := filepath.Join(directory, "missing.jsonl")
	if err := runShadowMarketEvaluate([]string{
		"--journal", missing, "--out", filepath.Join(directory, "missing-artifact.json"),
	}, &bytes.Buffer{}); err == nil {
		t.Fatal("missing journal was accepted")
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatalf("evaluate created a missing journal: %v", err)
	}

	path := filepath.Join(directory, "torn.jsonl")
	candidate, _ := marketadmission.Lookup(marketadmission.MarketWIFUSDC)
	opening, err := marketadmission.NewOpening(
		candidate, "11111111111111111111111111111111", marketadmission.DefaultThresholds(),
	)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.OpenRotating(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(time.Now().UTC(), marketadmission.EventOpened, opening.ContentSHA256, opening); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{\"torn\""); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := runShadowMarketEvaluate([]string{
		"--journal", path, "--out", filepath.Join(directory, "torn-artifact.json"),
	}, &bytes.Buffer{}); err == nil {
		t.Fatal("torn journal was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("evaluate repaired or otherwise changed torn evidence")
	}
}

func TestShadowMarketHelpAndBucketAlignment(t *testing.T) {
	var output bytes.Buffer
	if err := runShadowMarket(context.Background(), nil, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "market collect") ||
		!strings.Contains(output.String(), "keyless") {
		t.Fatalf("unexpected help: %s", output.String())
	}
	now := time.Date(2026, time.January, 1, 0, 0, 59, 999, time.UTC)
	want := time.Date(2026, time.January, 1, 0, 1, 0, 0, time.UTC)
	if got := nextMarketAdmissionBucket(now, time.Minute); !got.Equal(want) {
		t.Fatalf("next bucket = %s, want %s", got, want)
	}
	if marketAdmissionBucketExpired(want.Add(54*time.Second), want, time.Minute) ||
		!marketAdmissionBucketExpired(want.Add(55*time.Second), want, time.Minute) {
		t.Fatal("late market-admission bucket deadline is not exact")
	}
}

func TestAdmittedPolicyBindsTheQualifiedMarketContract(t *testing.T) {
	candidate, ok := marketadmission.Lookup(marketadmission.MarketWIFUSDC)
	if !ok {
		t.Fatal("WIF admission candidate missing")
	}
	primary, err := candidate.Pyth.IdentitySHA256()
	if err != nil {
		t.Fatal(err)
	}
	secondary, err := candidate.Kraken.IdentitySHA256()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := buildAdaptiveQuoteMarketPolicy(
		shadow.AdmittedVersion,
		candidate.Market,
		candidate.Pyth.Feed,
		primary,
		secondary,
		strings.Repeat("a", 64),
		25_000_000,
		4_000_000,
		3_000_000,
		100,
		100_000,
		"11111111111111111111111111111111",
		60,
	)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Version != shadow.AdmittedVersion ||
		policy.Market != shadow.MarketWIFUSDC ||
		policy.MarketEvidenceSHA256 != strings.Repeat("a", 64) ||
		policy.QuoteRoute != shadow.MainnetMarketQuoteRoute(shadow.MarketWIFUSDC, false) ||
		policy.Trigger.MaxSourceSkewSeconds != 30 ||
		policy.NativeFeePrice.MaxSourceSkewSeconds != 30 ||
		policy.QuotePeg.MaxSourceSkewSeconds != 30 {
		t.Fatalf("admitted policy = %+v", policy)
	}
	artifact := marketadmission.Artifact{
		Candidate: candidate, Observe: policy.Observe,
		Thresholds:    marketadmission.DefaultThresholds(),
		ContentSHA256: policy.MarketEvidenceSHA256,
	}
	if !admittedPolicyMatchesArtifact(policy, artifact) {
		t.Fatal("generated policy did not match its admission contract")
	}
	for name, mutate := range map[string]func(*shadow.Policy){
		"evidence": func(value *shadow.Policy) { value.MarketEvidenceSHA256 = "" },
		"feed":     func(value *shadow.Policy) { value.Trigger.Feed = "RAY/USD" },
		"route": func(value *shadow.Policy) {
			value.QuoteRoute = shadow.MainnetMarketQuoteRoute(shadow.MarketJUPUSDC, false)
		},
	} {
		t.Run(name, func(t *testing.T) {
			tampered := policy
			if policy.ReturnTrigger != nil {
				returnTrigger := *policy.ReturnTrigger
				tampered.ReturnTrigger = &returnTrigger
			}
			mutate(&tampered)
			if tampered.Validate() == nil {
				t.Fatal("tampered admitted policy was accepted")
			}
		})
	}
	for name, mutate := range map[string]func(*shadow.Policy){
		"trigger skew":      func(value *shadow.Policy) { value.Trigger.MaxSourceSkewSeconds = 31 },
		"return deviation":  func(value *shadow.Policy) { value.ReturnTrigger.MaxDeviationBPS = 201 },
		"native confidence": func(value *shadow.Policy) { value.NativeFeePrice.MaxConfidenceBPS = 201 },
		"peg skew":          func(value *shadow.Policy) { value.QuotePeg.MaxSourceSkewSeconds = 31 },
		"quote impact":      func(value *shadow.Policy) { value.Adaptive.MaxQuoteImpactBPS = 501 },
		"notional":          func(value *shadow.Policy) { value.InputAmount++ },
		"slippage":          func(value *shadow.Policy) { value.SlippageBPS++ },
	} {
		t.Run("artifact binding "+name, func(t *testing.T) {
			tampered := policy
			adaptive := *policy.Adaptive
			returnTrigger := *policy.ReturnTrigger
			native := *policy.NativeFeePrice
			peg := *policy.QuotePeg
			tampered.Adaptive = &adaptive
			tampered.ReturnTrigger = &returnTrigger
			tampered.NativeFeePrice = &native
			tampered.QuotePeg = &peg
			mutate(&tampered)
			if admittedPolicyMatchesArtifact(tampered, artifact) {
				t.Fatal("loosened admitted policy matched its evidence")
			}
		})
	}
}

func TestAdmittedCandidatePoliciesKeepTheirPinnedMintDecimals(t *testing.T) {
	for _, market := range []string{
		marketadmission.MarketWIFUSDC,
		marketadmission.MarketJTOUSDC,
		marketadmission.MarketPYTHUSDC,
	} {
		t.Run(market, func(t *testing.T) {
			candidate, ok := marketadmission.Lookup(market)
			if !ok {
				t.Fatal("candidate missing")
			}
			primary, err := candidate.Pyth.IdentitySHA256()
			if err != nil {
				t.Fatal(err)
			}
			secondary, err := candidate.Kraken.IdentitySHA256()
			if err != nil {
				t.Fatal(err)
			}
			policy, err := buildAdaptiveQuoteMarketPolicy(
				shadow.AdmittedVersion, candidate.Market, candidate.Pyth.Feed,
				primary, secondary, strings.Repeat("a", 64),
				candidate.QuoteNotionalUSDC, 4_000_000, 3_000_000,
				candidate.QuoteSlippageBPS, 100_000,
				"11111111111111111111111111111111", 60,
			)
			if err != nil {
				t.Fatal(err)
			}
			if policy.OutputDecimals != candidate.BaseDecimals ||
				policy.QuoteRoute.OutputMint != candidate.BaseMint ||
				policy.Validate() != nil {
				t.Fatalf("policy = %+v", policy)
			}
		})
	}
}

func TestMarketAdmissionMustCoverTheCurrentCompletedWindow(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	artifact := marketadmission.Artifact{Through: now.Truncate(24 * time.Hour)}
	if !currentMarketAdmission(artifact, now) {
		t.Fatal("current completed window was rejected")
	}
	artifact.Through = artifact.Through.Add(-24 * time.Hour)
	if currentMarketAdmission(artifact, now) {
		t.Fatal("stale market admission window was accepted")
	}
	artifact.Through = now.Truncate(24 * time.Hour)
	if currentMarketAdmission(artifact, time.Time{}) {
		t.Fatal("zero verification time was accepted")
	}
}
