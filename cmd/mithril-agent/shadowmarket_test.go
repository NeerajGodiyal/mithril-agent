package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/marketadmission"
	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
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

func TestShadowMarketProvisionalWritesAnImmutablePaperOnlyCheckpoint(t *testing.T) {
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "wif-provisional.jsonl")
	outPath := filepath.Join(directory, "wif-provisional.json")
	candidate, _ := marketadmission.Lookup(marketadmission.MarketWIFUSDC)
	opening, err := marketadmission.NewOpening(
		candidate, "11111111111111111111111111111111", marketadmission.DefaultThresholds(),
	)
	if err != nil {
		t.Fatal(err)
	}
	bucket := time.Now().UTC().Truncate(time.Minute).Add(-time.Minute)
	store, err := journal.OpenRotating(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(bucket, marketadmission.EventOpened, opening.ContentSHA256, opening); err != nil {
		t.Fatal(err)
	}
	observation := marketadmission.Observation{
		Version: marketadmission.Version, OpeningSHA256: opening.ContentSHA256,
		Bucket: bucket, ObservedAt: bucket.Add(time.Second),
		Failure: marketadmission.FailureMarketPrice,
	}
	if _, err := store.Append(
		observation.ObservedAt, marketadmission.EventObserved,
		bucket.Format(time.RFC3339), observation,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	args := []string{"--journal", journalPath, "--out", outPath}
	var output bytes.Buffer
	if err := runShadowMarketProvisional(args, &output); err != nil {
		t.Fatal(err)
	}
	var artifact marketadmission.ProvisionalArtifact
	if err := readStrictJSON(outPath, &artifact); err != nil || artifact.Validate() != nil ||
		artifact.VerifyJournal(journalPath) != nil {
		t.Fatalf("invalid provisional artifact: %+v, %v", artifact, err)
	}
	if artifact.ProvisionalPaperReady || !artifact.PaperOnly || artifact.Authorized ||
		artifact.ExpectedBuckets != 360 || artifact.ObservedBuckets != 1 {
		t.Fatalf("unexpected provisional artifact: %+v", artifact)
	}
	if !strings.Contains(output.String(), `"status":"development_provisional"`) {
		t.Fatalf("unexpected provisional output: %s", output.String())
	}
	if err := runShadowMarketProvisional(args, &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected immutable provisional output refusal, got %v", err)
	}
}

func TestReadyProvisionalEvidenceBuildsOnlyAProvisionalPolicy(t *testing.T) {
	artifactPath, journalPath, now := writeReadyProvisionalEvidence(t)
	artifact, err := loadProvisionalMarketAdmission(artifactPath, journalPath, now)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := buildAdaptiveProvisionalPolicy(
		artifact,
		artifact.Candidate.QuoteNotionalUSDC,
		80_000_000,
		3_000_000,
		artifact.Candidate.QuoteSlippageBPS,
		100_000,
		artifact.Observe,
		60,
	)
	if err != nil {
		t.Fatal(err)
	}
	if policy.MarketEvidenceClass != shadow.MarketEvidenceDevelopmentProvisional ||
		!provisionalPolicyMatchesArtifact(policy, artifact) ||
		admittedPolicyMatchesArtifact(policy, marketadmission.Artifact{
			Candidate: artifact.Candidate, Observe: artifact.Observe,
			Thresholds: artifact.Thresholds, ContentSHA256: artifact.ContentSHA256,
		}) {
		t.Fatalf("provisional policy crossed its evidence class: %+v", policy)
	}
}

// This opt-in integration check covers the operator's real provisional path:
// one immutable checkpoint writes a policy, the policy enters the shared paper
// portfolio, and two CLI invocations use the same evidence and journal. The
// second invocation must replay the first; neither path can sign or submit.
func TestLiveReadyProvisionalPolicyRunsAndResumes(t *testing.T) {
	endpoint := os.Getenv("MITHRIL_AGENT_LIVE_SOLANA_RPC")
	if os.Getenv("MITHRIL_AGENT_LIVE_PRICE_TEST") != "1" || endpoint == "" {
		t.Skip("set MITHRIL_AGENT_LIVE_PRICE_TEST=1 and MITHRIL_AGENT_LIVE_SOLANA_RPC")
	}
	t.Setenv(shadowEndpointEnvironment, endpoint)
	artifactPath, evidenceJournal, _ := writePassingProvisionalEvidence(t)
	candidate, _ := marketadmission.Lookup(marketadmission.MarketWIFUSDC)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(root, "wif-policy.json")
	if err := runShadowPolicy([]string{
		"--out", policyPath,
		"--adaptive", "--market", candidate.Market,
		"--budget-usdc", formatShadowAmount(candidate.QuoteNotionalUSDC, 6),
		"--drawdown-stop-bps", "500",
		"--observe", "11111111111111111111111111111111",
		"--slippage-bps", strconv.FormatUint(uint64(candidate.QuoteSlippageBPS), 10),
		"--provisional-artifact", artifactPath,
		"--provisional-journal", evidenceJournal,
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	basePolicy, err := loadActiveShadowPolicy(policyPath)
	if err != nil || basePolicy.MarketEvidenceClass != shadow.MarketEvidenceDevelopmentProvisional {
		t.Fatalf("generated provisional policy = %+v, %v", basePolicy, err)
	}
	checkedPolicyPath := filepath.Join(root, "wif-checked-policy.json")
	paperCheckPath := filepath.Join(root, "wif-paper-check.json")
	if err := runShadowMarketPaperCheck([]string{
		"--policy", policyPath,
		"--provisional-artifact", artifactPath,
		"--journal", evidenceJournal,
		"--result-out", paperCheckPath,
		"--candidate-policy-out", checkedPolicyPath,
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := loadActiveShadowPolicy(checkedPolicyPath); err != nil {
		t.Fatal(err)
	}
	portfolioPath := filepath.Join(root, "portfolio.json")
	if err := runShadowPortfolio([]string{
		"--out", portfolioPath, "--limit-usd", "1000", "--max-sol-usd", "1000",
		"--book", "wif=" + checkedPolicyPath,
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	runDir, statusPath := filepath.Join(root, "journal"), filepath.Join(root, "status.json")
	args := []string{
		"--policy", checkedPolicyPath, "--dir", runDir,
		"--portfolio", portfolioPath, "--portfolio-book", "wif",
		"--provisional-artifact", artifactPath,
		"--provisional-journal", evidenceJournal,
		"--paper-check-artifact", paperCheckPath,
		"--alert-status", statusPath, "--once",
	}
	for invocation := range 2 {
		var output bytes.Buffer
		if err := runShadowRun(t.Context(), args, &output); err != nil {
			t.Fatalf("invocation %d: %v", invocation+1, err)
		}
		if !strings.Contains(output.String(), `"event":"shadow.`) {
			t.Fatalf("invocation %d emitted no paper tick: %s", invocation+1, output.String())
		}
	}
	var snapshot paperstatus.Snapshot
	if err := readStrictJSON(statusPath, &snapshot); err != nil ||
		paperstatus.ValidateSnapshot(snapshot) != nil {
		t.Fatalf("paper status = %+v, %v", snapshot, err)
	}
	if snapshot.Summary != nil && snapshot.Summary.Market != candidate.Market ||
		snapshot.Summary == nil && !strings.Contains(snapshot.Current, "WAITING FOR PRICES") {
		t.Fatalf("paper status does not describe the provisional market or its honest unavailable state: %+v", snapshot)
	}
	roll, err := newDailyJournal(runDir)
	if err != nil {
		t.Fatal(err)
	}
	defer roll.Close()
	if err := roll.openFor(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if len(roll.Records()) < 2 {
		t.Fatalf("second CLI invocation did not resume the first journal: %d records", len(roll.Records()))
	}
}

func TestShadowPolicyRequiresOneCompleteEvidencePair(t *testing.T) {
	base := []string{
		"--out", filepath.Join(t.TempDir(), "policy.json"),
		"--adaptive", "--market", marketadmission.MarketWIFUSDC,
		"--budget-usdc", "25", "--drawdown-stop-bps", "500",
		"--observe", "11111111111111111111111111111111",
	}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing provisional journal",
			args: []string{"--provisional-artifact", "/tmp/provisional.json"},
			want: "requires its matching journal",
		},
		{
			name: "missing qualified journal",
			args: []string{"--admission-artifact", "/tmp/admission.json"},
			want: "requires its matching journal",
		},
		{
			name: "mixed evidence classes",
			args: []string{
				"--admission-artifact", "/tmp/admission.json",
				"--admission-journal", "/tmp/admission.jsonl",
				"--provisional-artifact", "/tmp/provisional.json",
				"--provisional-journal", "/tmp/provisional.jsonl",
			},
			want: "choose qualified or provisional",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := runShadowPolicy(append(append([]string{}, base...), test.args...), &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("evidence flags error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestMarketEvidenceClassIsValidatedAndFingerprinted(t *testing.T) {
	candidate, _ := marketadmission.Lookup(marketadmission.MarketWIFUSDC)
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
		candidate.QuoteNotionalUSDC, 80_000_000, 3_000_000,
		candidate.QuoteSlippageBPS, 100_000,
		"11111111111111111111111111111111", 60,
	)
	if err != nil {
		t.Fatal(err)
	}
	legacyFingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	longRun := policy
	longRun.MarketEvidenceClass = shadow.MarketEvidenceLongRun
	longRunFingerprint, err := longRun.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	provisional := policy
	provisional.MarketEvidenceClass = shadow.MarketEvidenceDevelopmentProvisional
	provisionalFingerprint, err := provisional.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if legacyFingerprint == longRunFingerprint || legacyFingerprint == provisionalFingerprint ||
		longRunFingerprint == provisionalFingerprint {
		t.Fatal("market evidence class was not bound into the policy fingerprint")
	}
	unknown := policy
	unknown.MarketEvidenceClass = "unknown"
	if unknown.Validate() == nil {
		t.Fatal("unknown market evidence class was accepted")
	}
	nonAdmitted := validShadowPolicy()
	nonAdmitted.MarketEvidenceClass = shadow.MarketEvidenceLongRun
	if nonAdmitted.Validate() == nil {
		t.Fatal("non-admitted policy accepted market evidence class")
	}
}

func writeReadyProvisionalEvidence(t *testing.T) (string, string, time.Time) {
	return writeProvisionalEvidence(t, 18, nil)
}

func writePassingProvisionalEvidence(t *testing.T) (string, string, time.Time) {
	prices := []uint64{200_000, 200_000, 196_000, 196_000, 204_000, 204_000, 200_000, 200_000}
	return writeProvisionalEvidence(t, 0, func(index int) uint64 {
		return prices[index%len(prices)]
	})
}

func writeProvisionalEvidence(
	t *testing.T,
	missingMinutes int,
	priceAt func(int) uint64,
) (string, string, time.Time) {
	t.Helper()
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "provisional.jsonl")
	artifactPath := filepath.Join(directory, "provisional.json")
	candidate, _ := marketadmission.Lookup(marketadmission.MarketWIFUSDC)
	opening, err := marketadmission.NewOpening(
		candidate, "11111111111111111111111111111111", marketadmission.DefaultThresholds(),
	)
	if err != nil {
		t.Fatal(err)
	}
	through := time.Now().UTC().Truncate(time.Minute)
	store, err := journal.OpenRotating(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(through.Add(-6*time.Hour), marketadmission.EventOpened, opening.ContentSHA256, opening); err != nil {
		t.Fatal(err)
	}
	marketPrimary, _ := candidate.Pyth.IdentitySHA256()
	marketSecondary, _ := candidate.Kraken.IdentitySHA256()
	index := 0
	for bucket := through.Add(-6*time.Hour + time.Duration(missingMinutes)*time.Minute); bucket.Before(through); bucket = bucket.Add(time.Minute) {
		observed := bucket.Add(time.Second)
		price := uint64(200_000)
		if priceAt != nil {
			price = priceAt(index)
		}
		index++
		observation := marketadmission.Observation{
			Version: marketadmission.Version, OpeningSHA256: opening.ContentSHA256,
			Bucket: bucket, ObservedAt: observed,
			Mint: marketadmission.MintEvidence{
				Address: candidate.BaseMint, Owner: candidate.TokenProgram,
				Decimals: candidate.BaseDecimals, ContextSlot: 100,
				DataSHA256: strings.Repeat("d", 64),
			},
			MarketPrimary: provisionalPythSample(candidate.Pyth, marketPrimary, price, observed),
			MarketSecondary: provisionalSample(
				marketSecondary, candidate.Pyth.Feed, price, observed,
			),
			USDCPrimary: provisionalPythSample(
				pricesource.PythPushUSDCSpec(), pricesource.PythPushUSDCIdentitySHA256(),
				1_000_000, observed,
			),
			USDCSecondary: provisionalSample(
				pricesource.KrakenIdentitySHA256(), pricetrigger.FeedUSDCUSD, 1_000_000, observed,
			),
			SOLPrimary: provisionalPythSample(
				pricesource.PythPushSOLSpec(), pricesource.PythPushIdentitySHA256(),
				200_000_000, observed,
			),
			SOLSecondary: provisionalSample(
				pricesource.KrakenSOLIdentitySHA256(), pricetrigger.FeedSOLUSD, 200_000_000, observed,
			),
			Buy: marketadmission.Quote{
				InputMint: candidate.QuoteMint, OutputMint: candidate.BaseMint,
				InputAmount: candidate.QuoteNotionalUSDC, EstimatedOutput: 125_000_000,
				MinimumOutput: 123_750_000, ReceivedAt: observed.Add(-time.Millisecond),
				LatencyMillis: 20, ResponseSHA256: strings.Repeat("a", 64),
			},
			Sell: marketadmission.Quote{
				InputMint: candidate.BaseMint, OutputMint: candidate.QuoteMint,
				InputAmount: 125_000_000, EstimatedOutput: 24_975_000,
				MinimumOutput: 24_725_250, ReceivedAt: observed.Add(-time.Millisecond),
				LatencyMillis: 20, ResponseSHA256: strings.Repeat("b", 64),
			},
		}
		if _, err := store.Append(
			observed, marketadmission.EventObserved, bucket.Format(time.RFC3339), observation,
		); err != nil {
			t.Fatal(err)
		}
	}
	prefix, err := store.DurablePrefix()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	now := through.Add(30 * time.Second)
	artifact, err := marketadmission.EvaluateProvisionalJournal(journalPath, prefix, now)
	if err != nil {
		t.Fatal(err)
	}
	if !artifact.ProvisionalPaperReady || artifact.AvailableBuckets != uint64(360-missingMinutes) {
		t.Fatalf("provisional artifact is not ready: %+v", artifact)
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return artifactPath, journalPath, now
}

func provisionalPythSample(
	spec pricesource.PythPushSpec,
	identity string,
	price uint64,
	observed time.Time,
) pricesource.PythObservation {
	return pricesource.PythObservation{
		Sample:      provisionalSample(identity, spec.Feed, price, observed),
		ContextSlot: 100, Account: spec.LegacyAccount, FeedID: spec.FeedID,
	}
}

func provisionalSample(
	identity, feed string,
	price uint64,
	observed time.Time,
) pricetrigger.Sample {
	return pricetrigger.Sample{
		SourceSHA256: identity, Feed: feed, PriceMicros: price,
		ConfidenceMicros: 1, PublishedAt: observed.Add(-time.Second),
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

func TestMarketDashboardStatusPathMustBeTheJournalSibling(t *testing.T) {
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "admission.jsonl")
	want := filepath.Join(directory, "dashboard-status.json")
	if err := validateMarketDashboardStatusPath("", journalPath); err != nil {
		t.Fatalf("optional dashboard status was rejected: %v", err)
	}
	if err := validateMarketDashboardStatusPath(want, journalPath); err != nil {
		t.Fatalf("dashboard status sibling was rejected: %v", err)
	}
	for _, path := range []string{
		"dashboard-status.json",
		filepath.Join(directory, "status.json"),
		filepath.Join(directory, "nested", "dashboard-status.json"),
		directory + "/nested/../dashboard-status.json",
	} {
		if err := validateMarketDashboardStatusPath(path, journalPath); err == nil {
			t.Fatalf("non-sibling dashboard status was accepted: %q", path)
		}
	}
	if err := validateMarketDashboardStatusPath(want, want); err == nil {
		t.Fatal("dashboard status was allowed to replace its journal")
	}
}

func TestMarketCollectWiresTheStrictDashboardStatusFlag(t *testing.T) {
	directory := t.TempDir()
	err := runShadowMarketCollect(t.Context(), []string{
		"--market", marketadmission.MarketWIFUSDC,
		"--observe", "11111111111111111111111111111111",
		"--journal", filepath.Join(directory, "admission.jsonl"),
		"--dashboard-status", filepath.Join(directory, "status.json"),
		"--once",
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "journal sibling dashboard-status.json") {
		t.Fatalf("non-sibling collector dashboard status error = %v", err)
	}
}

func TestMarketCollectorStatusStartsMissingAndUpdatesAfterAppend(t *testing.T) {
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "admission.jsonl")
	statusPath := filepath.Join(directory, "dashboard-status.json")
	candidate, _ := marketadmission.Lookup(marketadmission.MarketWIFUSDC)
	opening, err := marketadmission.NewOpening(
		candidate, "11111111111111111111111111111111", marketadmission.DefaultThresholds(),
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 4, 12, 34, 45, 0, time.UTC)
	store, err := journal.OpenRotating(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := prepareMarketAdmissionJournal(store, opening, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	tracker, err := marketadmission.NewDiagnosticTracker(opening, store.Records(), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMarketDashboardStatus(statusPath, tracker, now); err != nil {
		t.Fatal(err)
	}
	initialRaw, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := marketadmission.LoadDashboardStatus(initialRaw)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Diagnostic.ObservedBuckets != 0 ||
		initial.Diagnostic.FailureCounts["missing_bucket"] != 360 {
		t.Fatalf("initial dashboard status = %+v", initial)
	}

	bucket := now.Truncate(time.Minute).Add(-time.Minute)
	observation := marketadmission.Observation{
		Version: marketadmission.Version, OpeningSHA256: opening.ContentSHA256,
		Bucket: bucket, ObservedAt: bucket.Add(time.Second),
		Failure: marketadmission.FailureBuyQuote,
	}
	if err := observation.Validate(opening); err != nil {
		t.Fatal(err)
	}
	updatedAt := now.Add(5 * time.Second)
	if err := appendMarketAdmissionObservation(
		store, observation, tracker, statusPath, updatedAt,
	); err != nil {
		t.Fatal(err)
	}
	updatedRaw, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := marketadmission.LoadDashboardStatus(updatedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if updated.UpdatedAt != updatedAt || updated.Diagnostic.ObservedBuckets != 1 ||
		updated.Diagnostic.AvailableBuckets != 0 ||
		updated.Diagnostic.FailureCounts[marketadmission.FailureBuyQuote] != 1 ||
		updated.Diagnostic.FailureCounts["missing_bucket"] != 359 ||
		bytes.Equal(initialRaw, updatedRaw) {
		t.Fatalf("updated dashboard status = %+v", updated)
	}
	info, err := os.Stat(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("dashboard status mode = %o", info.Mode().Perm())
	}
	withCheck, err := updated.WithPaperCheck(marketadmission.DashboardPaperCheck{
		Market: updated.Market, CheckedAt: updated.Diagnostic.Through.Add(time.Minute),
		Through:             updated.Diagnostic.Through,
		Outcome:             marketadmission.DashboardPaperOutcomeInsufficientEvidence,
		TrainingCoverageBPS: 9_000, HoldoutCoverageBPS: 10_000,
		Reasons: []string{"training_coverage_below_95_percent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	withCheckRaw, err := json.Marshal(withCheck)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusPath, withCheckRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeMarketDashboardStatus(statusPath, tracker, updatedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	preservedRaw, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	preserved, err := marketadmission.LoadDashboardStatus(preservedRaw)
	if err != nil || preserved.PaperCheck == nil ||
		preserved.PaperCheck.Outcome != marketadmission.DashboardPaperOutcomeInsufficientEvidence {
		t.Fatalf("preserved paper check = %+v, %v", preserved.PaperCheck, err)
	}
	preserved.PaperCheck.Market = marketadmission.MarketJTOUSDC
	invalidRaw, err := json.Marshal(preserved)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusPath, invalidRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeMarketDashboardStatus(statusPath, tracker, updatedAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	clearedRaw, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := marketadmission.LoadDashboardStatus(clearedRaw)
	if err != nil || cleared.PaperCheck != nil {
		t.Fatalf("invalid paper check was preserved: %+v, %v", cleared.PaperCheck, err)
	}
}

func TestMarketCollectorWithoutDashboardStatusCreatesNothing(t *testing.T) {
	if err := writeMarketDashboardStatus("", nil, time.Time{}); err != nil {
		t.Fatalf("omitted dashboard status was not a no-op: %v", err)
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
		!strings.Contains(output.String(), "market diagnose") ||
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
