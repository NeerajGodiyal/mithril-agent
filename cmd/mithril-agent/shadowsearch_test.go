package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestShadowSearchUsesTrainingOnlyAndCannotAuthorize(t *testing.T) {
	policy := validShadowPolicy()
	train := []uint64{
		200_000_000, 200_000_000, 100_000_000, 100_000_000,
		200_000_000, 200_000_000, 100_000_000, 100_000_000,
	}
	validation := slices.Clone(train)
	result, err := searchShadowCandidate(policy, train, validation, 100)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "research_only" || result.Authorized || result.Promotable ||
		!result.PoolModelled || result.AssumedSpreadBPS != 100 ||
		result.CandidatesEvaluated != 1 || result.Candidate.SellAtMicros != 200_000_000 ||
		result.Candidate.BuyAtMicros != 100_000_000 ||
		result.Training.FullRoundTrips == 0 || result.Validation.FullRoundTrips == 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestAdaptiveShadowSearchKeepsRiskLimitsFixedAndBuildsAReplayableCandidate(t *testing.T) {
	policy := adaptiveShadowSearchPolicy()
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	prices := []uint64{
		100_000_000, 98_000_000, 96_000_000, 94_000_000, 94_000_000,
		96_000_000, 98_000_000, 100_000_000, 102_000_000, 102_000_000,
		100_000_000, 98_000_000, 96_000_000, 94_000_000, 94_000_000,
		96_000_000, 98_000_000, 100_000_000, 102_000_000, 102_000_000,
	}
	result, err := searchShadowCandidate(policy, prices, prices, 25)
	if err != nil {
		t.Fatal(err)
	}
	if result.Candidate.Adaptive == nil || result.CandidatesEvaluated < 2 ||
		result.Training.FullRoundTrips == 0 || result.Validation.FullRoundTrips == 0 {
		t.Fatalf("adaptive search did not produce a usable candidate: %+v", result)
	}
	wantRisk := *policy.Adaptive
	gotRisk := *result.Candidate.Adaptive
	wantRisk.FastWindow, wantRisk.SlowWindow, wantRisk.MinimumSignalBPS =
		gotRisk.FastWindow, gotRisk.SlowWindow, gotRisk.MinimumSignalBPS
	if gotRisk != wantRisk {
		t.Fatalf("adaptive search changed a risk boundary: got %+v want %+v", gotRisk, wantRisk)
	}
	result.TrainDay, result.ValidationDay = "2026-08-17", "2026-08-18"
	provenance := func(day string) shadowJournalProvenance {
		return shadowJournalProvenance{Day: day, Records: 2, ChainHeadSHA256: strings.Repeat("a", 64)}
	}
	candidate, err := newShadowPaperCandidate(
		policy, result, provenance(result.TrainDay), provenance(result.ValidationDay),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := candidate.validateAgainst(policy); err != nil {
		t.Fatalf("adaptive candidate does not replay against its base: %v", err)
	}
	tampered := result
	changed := *tampered.Candidate.Adaptive
	changed.MaxDrawdownBPS++
	tampered.Candidate.Adaptive = &changed
	if _, err := newShadowPaperCandidate(
		policy, tampered, provenance(result.TrainDay), provenance(result.ValidationDay),
	); err == nil {
		t.Fatal("adaptive search accepted a changed drawdown boundary")
	}
}

func TestAdaptiveShadowSearchConsumesARealReplayableRunnerJournal(t *testing.T) {
	root := privateTestDirectory(t)
	policy := adaptiveShadowSearchPolicy()
	day := "2026-08-17"
	start, _ := time.Parse("2006-01-02", day)
	primary := &shadowSearchReader{identity: policy.Trigger.PrimarySourceSHA256}
	secondary := &shadowSearchReader{identity: policy.Trigger.SecondarySourceSHA256}
	quotePrimary := &shadowSearchReader{
		identity: policy.QuotePeg.PrimarySourceSHA256, price: 1_000_000,
	}
	quoteSecondary := &shadowSearchReader{
		identity: policy.QuotePeg.SecondarySourceSHA256, price: 1_000_000,
	}
	roll, err := newDailyJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := shadow.NewRunner(
		policy, primary, secondary, shadowSearchUnavailableQuoter{}, roll,
		quotePrimary, quoteSecondary,
	)
	if err != nil {
		t.Fatal(err)
	}
	prices := []uint64{
		100_000_000, 98_000_000, 96_000_000, 94_000_000, 94_000_000,
		96_000_000, 98_000_000, 100_000_000, 102_000_000, 102_000_000,
		100_000_000, 98_000_000, 96_000_000, 94_000_000, 94_000_000,
		96_000_000, 98_000_000, 100_000_000, 102_000_000, 102_000_000,
	}
	for index, price := range prices {
		at := start.Add(time.Duration(index+1) * policy.Tick())
		primary.price, primary.at = price, at
		secondary.price, secondary.at = price, at
		quotePrimary.at, quoteSecondary.at = at, at
		if _, err := runner.Step(t.Context(), at); err != nil {
			t.Fatal(err)
		}
	}
	if err := runner.ClosePeriod(
		start.Add(24*time.Hour-time.Nanosecond), prices[len(prices)-1],
	); err != nil {
		t.Fatal(err)
	}
	if err := roll.Close(); err != nil {
		t.Fatal(err)
	}
	ticks, _, err := readShadowSearchJournal(
		filepath.Join(root, "shadow-"+day+".jsonl"), day, policy,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := searchShadowCandidateTicks(policy, ticks, ticks, 25)
	if err != nil {
		t.Fatal(err)
	}
	if result.Candidate.Adaptive == nil || result.Training.FullRoundTrips == 0 ||
		result.Validation.FullRoundTrips == 0 {
		t.Fatalf("real adaptive journal did not reach the learner: %+v", result)
	}
}

func TestAdaptiveSearchAcceptsAPendingDecisionPeriodClose(t *testing.T) {
	root := privateTestDirectory(t)
	policy := adaptiveShadowSearchPolicy()
	day := "2026-08-17"
	start, _ := time.Parse("2006-01-02", day)
	primary := &shadowSearchReader{identity: policy.Trigger.PrimarySourceSHA256}
	secondary := &shadowSearchReader{identity: policy.Trigger.SecondarySourceSHA256}
	quotePrimary := &shadowSearchReader{
		identity: policy.QuotePeg.PrimarySourceSHA256, price: 1_000_000,
	}
	quoteSecondary := &shadowSearchReader{
		identity: policy.QuotePeg.SecondarySourceSHA256, price: 1_000_000,
	}
	roll, err := newDailyJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	quoter := shadowSearchQuoter(func(sell bool, amount uint64) shadow.Quote {
		price := primary.price
		output := amount * price / 1_000_000_000
		if !sell {
			output = amount * 1_000_000_000 / price
		}
		minimum := (output*uint64(10_000-policy.SlippageBPS) + 9_999) / 10_000
		return shadow.Quote{InputAmount: amount, EstimatedOutput: output, MinimumOutput: minimum}
	})
	runner, err := shadow.NewRunner(
		policy, primary, secondary, quoter, roll, quotePrimary, quoteSecondary,
	)
	if err != nil {
		t.Fatal(err)
	}
	prices := []uint64{100_000_000, 98_000_000, 96_000_000, 94_000_000}
	for index, price := range prices {
		at := start.Add(time.Duration(index+1) * time.Minute)
		primary.price, primary.at = price, at
		secondary.price, secondary.at = price, at
		quotePrimary.at, quoteSecondary.at = at, at
		if _, err := runner.Step(t.Context(), at); err != nil {
			t.Fatal(err)
		}
	}
	if err := runner.ClosePeriod(
		start.Add(24*time.Hour-time.Nanosecond), prices[len(prices)-1],
	); err != nil {
		t.Fatal(err)
	}
	if err := roll.Close(); err != nil {
		t.Fatal(err)
	}
	ticks, _, err := readShadowSearchJournal(
		filepath.Join(root, "shadow-"+day+".jsonl"), day, policy,
	)
	if err != nil {
		t.Fatal(err)
	}
	last := ticks[len(ticks)-1]
	if last.Event != shadow.EventMissed || !last.PeriodClose ||
		last.PrimaryPrice != nil || last.SecondaryPrice != nil {
		t.Fatalf("adaptive terminal close = %+v", last)
	}
}

func adaptiveShadowSearchPolicy() shadow.Policy {
	policy := validShadowPolicy()
	policy.Trigger.ThresholdMicros = pricetrigger.MaxPriceMicros
	policy.SlippageBPS = 10
	policy.FeeLamports = 1
	buy := policy.Trigger
	buy.Direction = pricetrigger.BuyAtOrBelow
	buy.ThresholdMicros = 1
	policy.ReturnTrigger = &buy
	policy.Adaptive = &shadow.AdaptivePolicy{
		Version:    shadow.AdaptiveVersion,
		FastWindow: 2, SlowWindow: 4,
		MinimumSignalBPS:         150,
		MaxVolatilityBPS:         2_000,
		MaxQuoteImpactBPS:        300,
		MaxDrawdownBPS:           5_000,
		MaxObservationGapSeconds: 120,
	}
	return policy
}

type shadowSearchReader struct {
	identity string
	price    uint64
	at       time.Time
}

func (reader *shadowSearchReader) IdentitySHA256() string { return reader.identity }

func (reader *shadowSearchReader) Latest(
	_ context.Context, feed string,
) (pricetrigger.Sample, error) {
	return pricetrigger.Sample{
		SourceSHA256: reader.identity, Feed: feed,
		PriceMicros: reader.price, PublishedAt: reader.at,
	}, nil
}

type shadowSearchUnavailableQuoter struct{}

func (shadowSearchUnavailableQuoter) Quote(
	context.Context, string, bool, uint64, uint16,
) (shadow.Quote, error) {
	return shadow.Quote{}, errors.New("modelled venue unavailable")
}

type shadowSearchQuoter func(sell bool, amount uint64) shadow.Quote

func (quote shadowSearchQuoter) Quote(
	_ context.Context, _ string, sell bool, amount uint64, _ uint16,
) (shadow.Quote, error) {
	return quote(sell, amount), nil
}

func TestShadowSearchCLIReadsSeparateHashChainedDays(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := validShadowPolicy()
	policyPath := writeShadowPolicy(t, policy)
	prices := []uint64{
		200_000_000, 200_000_000, 100_000_000, 100_000_000,
		200_000_000, 200_000_000, 100_000_000, 100_000_000,
	}
	writeShadowSearchDay(t, root, policy, "2026-08-17", prices)
	writeShadowSearchDay(t, root, policy, "2026-08-18", prices)

	var output bytes.Buffer
	if err := run([]string{
		"shadow", "search", "--policy", policyPath, "--dir", root,
		"--train-day", "2026-08-17", "--validation-day", "2026-08-18",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result shadowSearchResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.TrainDay != "2026-08-17" || result.ValidationDay != "2026-08-18" ||
		result.Status != "research_only" || result.Authorized {
		t.Fatalf("result = %+v", result)
	}
}

func TestShadowSearchWritesImmutablePolicyBoundPaperCandidate(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := validShadowPolicy()
	policyPath := writeShadowPolicy(t, policy)
	prices := []uint64{
		200_000_000, 200_000_000, 100_000_000, 100_000_000,
		200_000_000, 200_000_000, 100_000_000, 100_000_000,
	}
	writeShadowSearchDay(t, root, policy, "2026-08-17", prices)
	writeShadowSearchDay(t, root, policy, "2026-08-18", prices)
	candidatePath := filepath.Join(root, "candidate.json")
	args := []string{
		"shadow", "search", "--policy", policyPath, "--dir", root,
		"--train-day", "2026-08-17", "--validation-day", "2026-08-18",
		"--candidate-out", candidatePath,
	}

	var output bytes.Buffer
	if err := run(args, &output); err != nil {
		t.Fatal(err)
	}
	var result shadowSearchResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.CandidatePolicySHA256 == "" {
		t.Fatal("search result omitted the staged candidate fingerprint")
	}
	info, err := os.Stat(candidatePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("candidate mode = %o", info.Mode().Perm())
	}
	var candidate shadowPaperCandidate
	if err := readStrictJSON(candidatePath, &candidate); err != nil {
		t.Fatal(err)
	}
	if err := candidate.validateAgainst(policy); err != nil {
		t.Fatal(err)
	}
	if candidate.CandidatePolicySHA256 != result.CandidatePolicySHA256 ||
		candidate.TrainingJournal.Day != "2026-08-17" ||
		candidate.ValidationJournal.Day != "2026-08-18" ||
		candidate.TrainingJournal.Records != len(prices)+2 ||
		candidate.ValidationJournal.Records != len(prices)+2 ||
		candidate.TrainingJournal.ChainHeadSHA256 == "" ||
		candidate.ValidationJournal.ChainHeadSHA256 == "" ||
		candidate.Policy.ReturnTrigger == nil ||
		candidate.Policy.Trigger.ThresholdMicros != 200_000_000 ||
		candidate.Policy.ReturnTrigger.ThresholdMicros != 100_000_000 {
		t.Fatalf("candidate = %+v", candidate)
	}

	if err := run(args, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "immutable") {
		t.Fatalf("candidate overwrite error = %v", err)
	}

	candidate.Policy.FeeLamports++
	candidate.CandidatePolicySHA256, err = candidate.Policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := candidate.validateAgainst(policy); err == nil ||
		!strings.Contains(err.Error(), "outside the searched thresholds") {
		t.Fatalf("non-threshold policy drift error = %v", err)
	}
}

func TestShadowSearchKeepsIterativeCandidateBoundToOriginalBase(t *testing.T) {
	root := privateTestDirectory(t)
	base := candidateTestPolicy()
	basePath := writeShadowPolicy(t, base)
	first := candidateForPrices(t, base, 200_000_000, 100_000_000)
	observedDirectory := filepath.Join(root, first.CandidatePolicySHA256)
	roll, err := newDailyJournal(observedDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if err := roll.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ensureShadowPolicySnapshot(observedDirectory, first.Policy); err != nil {
		t.Fatal(err)
	}
	prices := []uint64{
		220_000_000, 220_000_000, 110_000_000, 110_000_000,
		220_000_000, 220_000_000, 110_000_000, 110_000_000,
	}
	writeShadowSearchDay(t, observedDirectory, first.Policy, "2026-08-19", prices)
	writeShadowSearchDay(t, observedDirectory, first.Policy, "2026-08-20", prices)
	nextPath := filepath.Join(root, "next.json")
	if err := runShadowSearch([]string{
		"--policy", filepath.Join(observedDirectory, "policy.json"),
		"--base-policy", basePath, "--dir", observedDirectory,
		"--train-day", "2026-08-19", "--validation-day", "2026-08-20",
		"--candidate-out", nextPath,
	}, io.Discard); err != nil {
		t.Fatal(err)
	}
	next, err := loadShadowPaperCandidate(nextPath, base)
	if err != nil {
		t.Fatal(err)
	}
	baseFingerprint, err := base.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if next.BasePolicySHA256 != baseFingerprint ||
		next.CandidatePolicySHA256 == first.CandidatePolicySHA256 {
		t.Fatalf("iterative candidate = %+v", next)
	}
	pointerPath := filepath.Join(root, "selected")
	if err := runShadowSelect([]string{
		"--policy", basePath, "--candidate", nextPath, "--pointer", pointerPath,
		"--lifecycle-lock", filepath.Join(root, "lifecycle.lock"),
	}, io.Discard); err != nil {
		t.Fatal(err)
	}
	activeRoll, err := newDailyJournal(observedDirectory)
	if err != nil {
		t.Fatal(err)
	}
	boundary := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	if err := activeRoll.openFor(boundary.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	run := &shadowRun{
		basePolicy: base, policy: first.Policy, journalRoot: root,
		candidatePointer: pointerPath, policySHA256: first.CandidatePolicySHA256,
		primary:   candidatePriceSource{pricesource.KrakenSOLIdentitySHA256(), boundary},
		secondary: candidatePriceSource{pricesource.KrakenIdentitySHA256(), boundary},
		quoter:    liveStubQuoter{estimated: 21_525}, roll: activeRoll,
	}
	if err := run.refreshSelectedCandidate(boundary); err != nil {
		t.Fatal(err)
	}
	defer run.roll.Close()
	if run.policySHA256 != next.CandidatePolicySHA256 {
		t.Fatalf("running original-base observer rejected iterative candidate: %+v", run)
	}

	drifted := first.Policy
	drifted.FeeLamports++
	if err := validateShadowSearchLineage(base, drifted); err == nil {
		t.Fatal("an iterative journal policy changed a non-threshold field")
	}
}

func TestShadowSearchRejectsLeakyOrUnusableInputs(t *testing.T) {
	policy := validShadowPolicy()
	if _, err := searchShadowCandidate(policy, []uint64{1, 1}, []uint64{1, 2}, 100); err == nil ||
		!strings.Contains(err.Error(), "distinct") {
		t.Fatalf("single training level error = %v", err)
	}
	if _, err := searchShadowCandidate(policy, []uint64{1, 2}, []uint64{1, 2}, 10_000); err == nil ||
		!strings.Contains(err.Error(), "spread") {
		t.Fatalf("unsafe spread error = %v", err)
	}
	if err := runShadowSearch([]string{
		"--policy", "/tmp/policy", "--dir", "/tmp/journals",
		"--train-day", "2026-08-18", "--validation-day", "2026-08-18",
	}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "later") {
		t.Fatalf("same-day split error = %v", err)
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if err := runShadowSearch([]string{
		"--policy", "/tmp/policy", "--dir", "/tmp/journals",
		"--train-day", today.Add(-24 * time.Hour).Format("2006-01-02"),
		"--validation-day", today.Format("2006-01-02"),
	}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "complete UTC days") {
		t.Fatalf("open validation day error = %v", err)
	}
}

func TestShadowSearchPreservesReturnTriggerEvidenceLimits(t *testing.T) {
	policy := validShadowPolicy()
	buy := policy.Trigger
	buy.Direction = pricetrigger.BuyAtOrBelow
	buy.ThresholdMicros = 100_000_000
	buy.MaxAgeSeconds = 60
	buy.MaxSourceSkewSeconds = 30
	buy.MaxDeviationBPS = 100
	buy.MaxConfidenceBPS = 100
	policy.ReturnTrigger = &buy
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}

	got := shadowSearchPolicy(policy, 210_000_000, 90_000_000)
	expected := buy
	expected.ThresholdMicros = 90_000_000
	if got.Trigger.ThresholdMicros != 210_000_000 || got.ReturnTrigger == nil ||
		*got.ReturnTrigger != expected {
		t.Fatalf("candidate changed return-leg evidence limits: %+v", got.ReturnTrigger)
	}
}

func TestShadowSearchRejectsTicksTheRunnerCouldNotEmit(t *testing.T) {
	root := privateTestDirectory(t)
	policy := validShadowPolicy()
	day := "2026-08-17"
	start, _ := time.Parse("2006-01-02", day)
	path := filepath.Join(root, "shadow-"+day+".jsonl")
	store, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(start, shadow.EventOpened, "", shadow.Opening{
		Version: shadow.JournalVersion, PolicySHA256: fingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	book, err := shadow.NewLedger(policy, policy.Trigger.ThresholdMicros)
	if err != nil {
		t.Fatal(err)
	}
	equity, err := book.EquityMicros(policy.Trigger.ThresholdMicros)
	if err != nil {
		t.Fatal(err)
	}
	at := start.Add(time.Minute)
	primary, secondary := shadowSearchSamples(policy, policy.Trigger.ThresholdMicros, at)
	if _, err := store.Append(at, shadow.EventWaiting, "", shadow.Tick{
		At: at, Event: shadow.EventWaiting, PriceMicros: policy.Trigger.ThresholdMicros,
		EquityMicros: equity, QuoteLowerMicros: policy.QuotePeg.MinimumMicros,
		QuoteUpperMicros: policy.QuotePeg.MaximumMicros,
		PrimaryPrice:     &primary, SecondaryPrice: &secondary,
	}); err != nil {
		t.Fatal(err)
	}
	closeAt := start.Add(24*time.Hour - time.Nanosecond)
	if _, err := store.Append(closeAt, shadow.EventClosed, "", shadow.Tick{
		At: closeAt, Event: shadow.EventClosed, PeriodClose: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readShadowSearchJournal(path, day, policy); err == nil ||
		!strings.Contains(err.Error(), "active price rule") {
		t.Fatalf("impossible tick error = %v", err)
	}
}

func TestShadowSearchJournalBindsItsDayAndTickTimes(t *testing.T) {
	root := privateTestDirectory(t)
	policy := validShadowPolicy()
	prices := []uint64{200_000_000, 100_000_000}
	writeShadowSearchDay(t, root, policy, "2026-08-17", prices)
	original := filepath.Join(root, "shadow-2026-08-17.jsonl")
	raw, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(root, "shadow-2026-08-18.jsonl")
	if err := os.WriteFile(renamed, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readShadowSearchJournal(renamed, "2026-08-18", policy); err == nil ||
		!strings.Contains(err.Error(), "different UTC day") {
		t.Fatalf("renamed journal error = %v", err)
	}

	day := "2026-08-19"
	start, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(filepath.Join(root, "shadow-"+day+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(start, shadow.EventOpened, "", shadow.Opening{
		Version: shadow.JournalVersion, PolicySHA256: fingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(start.Add(time.Minute), shadow.EventWaiting, "", shadow.Tick{
		At: start.Add(2 * time.Minute), Event: shadow.EventWaiting,
		PriceMicros: 200_000_000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readShadowSearchJournal(
		filepath.Join(root, "shadow-"+day+".jsonl"), day, policy,
	); err == nil || !strings.Contains(err.Error(), "timestamp") {
		t.Fatalf("mismatched tick time error = %v", err)
	}
}

func TestShadowSearchRequiresACompletedUTCJournal(t *testing.T) {
	root := privateTestDirectory(t)
	policy := validShadowPolicy()
	day := "2026-08-17"
	prices := []uint64{200_000_000, 100_000_000}
	writeShadowSearchJournal(t, root, policy, day, prices, false)
	path := filepath.Join(root, "shadow-"+day+".jsonl")
	if _, _, err := readShadowSearchJournal(path, day, policy); err == nil ||
		!strings.Contains(err.Error(), "terminal close") {
		t.Fatalf("unclosed journal error = %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeShadowSearchJournal(t, root, policy, day, prices, false)
	store, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse("2006-01-02", day)
	earlyClose := start.Add(time.Hour)
	if _, err := store.Append(earlyClose, shadow.EventClosed, "", shadow.Tick{
		At: earlyClose, Event: shadow.EventClosed, PeriodClose: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readShadowSearchJournal(path, day, policy); err == nil ||
		!strings.Contains(err.Error(), "terminal close") {
		t.Fatalf("early close error = %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeShadowSearchJournal(t, root, policy, day, prices, false)
	store, err = journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	firstClose := start.Add(3 * time.Minute)
	if _, err := store.Append(firstClose, shadow.EventClosed, "", shadow.Tick{
		At: firstClose, Event: shadow.EventClosed, PeriodClose: true,
	}); err != nil {
		t.Fatal(err)
	}
	at := start.Add(4 * time.Minute)
	book, err := shadow.NewLedger(policy, prices[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, price := range prices[1:] {
		book, err = book.Mark(price)
		if err != nil {
			t.Fatal(err)
		}
	}
	book, err = book.Mark(150_000_000)
	if err != nil {
		t.Fatal(err)
	}
	equity, err := book.EquityMicros(150_000_000)
	if err != nil {
		t.Fatal(err)
	}
	primary, secondary := shadowSearchSamples(policy, 150_000_000, at)
	if _, err := store.Append(at, shadow.EventWaiting, "", shadow.Tick{
		At: at, Event: shadow.EventWaiting, PriceMicros: 150_000_000, EquityMicros: equity,
		QuoteLowerMicros: policy.QuotePeg.MinimumMicros,
		QuoteUpperMicros: policy.QuotePeg.MaximumMicros,
		PrimaryPrice:     &primary, SecondaryPrice: &secondary,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(start.Add(24*time.Hour-time.Nanosecond), shadow.EventClosed, "", shadow.Tick{
		At:    start.Add(24*time.Hour - time.Nanosecond),
		Event: shadow.EventClosed, PeriodClose: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readShadowSearchJournal(path, day, policy); err != nil {
		t.Fatalf("clean restart followed by a final close was rejected: %v", err)
	}
}

func writeShadowSearchDay(
	t *testing.T, directory string, policy shadow.Policy, day string, prices []uint64,
) {
	t.Helper()
	writeShadowSearchJournal(t, directory, policy, day, prices, true)
}

func writeShadowSearchJournal(
	t *testing.T, directory string, policy shadow.Policy, day string, prices []uint64,
	closed bool,
) {
	t.Helper()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(filepath.Join(directory, "shadow-"+day+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(start, shadow.EventOpened, "", shadow.Opening{
		Version: shadow.JournalVersion, PolicySHA256: fingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	var book shadow.Ledger
	opened := false
	for index, price := range prices {
		at := start.Add(time.Duration(index+1) * policy.Tick())
		if !opened {
			book, err = shadow.NewLedger(policy, price)
			opened = true
		} else {
			book, err = book.Mark(price)
		}
		if err != nil {
			t.Fatal(err)
		}
		equity, err := book.EquityMicros(price)
		if err != nil {
			t.Fatal(err)
		}
		triggered := price >= policy.Trigger.ThresholdMicros
		if policy.Trigger.Direction == pricetrigger.BuyAtOrBelow {
			triggered = price <= policy.Trigger.ThresholdMicros
		}
		event := shadow.EventWaiting
		if triggered {
			event = shadow.EventMissed
		}
		tick := shadow.Tick{
			At: at, Event: event, PriceMicros: price, Triggered: triggered, EquityMicros: equity,
		}
		primary, secondary := shadowSearchSamples(policy, price, at)
		tick.PrimaryPrice, tick.SecondaryPrice = &primary, &secondary
		if policy.QuotePeg != nil {
			tick.QuoteLowerMicros = policy.QuotePeg.MinimumMicros
			tick.QuoteUpperMicros = policy.QuotePeg.MaximumMicros
		}
		if _, err := store.Append(at, event, "", tick); err != nil {
			t.Fatal(err)
		}
	}
	if closed {
		at := start.Add(24*time.Hour - time.Nanosecond)
		if _, err := store.Append(at, shadow.EventClosed, "", shadow.Tick{
			At: at, Event: shadow.EventClosed, PeriodClose: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func shadowSearchSamples(
	policy shadow.Policy, price uint64, at time.Time,
) (pricetrigger.Sample, pricetrigger.Sample) {
	primary := pricetrigger.Sample{
		SourceSHA256: policy.Trigger.PrimarySourceSHA256,
		Feed:         policy.Trigger.Feed,
		PriceMicros:  price,
		PublishedAt:  at,
	}
	secondary := primary
	secondary.SourceSHA256 = policy.Trigger.SecondarySourceSHA256
	return primary, secondary
}
