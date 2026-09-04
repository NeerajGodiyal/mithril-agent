package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/marketadmission"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestShadowMarketPaperCheckIsPrintOnlyResearchAndChecksSplitCoverage(t *testing.T) {
	if marketPaperCheckSpreadBPS*2 != marketadmission.DefaultThresholds().P95RouteCostBPS {
		t.Fatal("paper-check baseline no longer matches the code-owned route-cost ceiling")
	}
	artifactPath, journalPath, now := writeReadyProvisionalEvidence(t)
	artifact, err := loadProvisionalMarketAdmission(artifactPath, journalPath, now)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := buildAdaptiveProvisionalPolicy(
		artifact, artifact.Candidate.QuoteNotionalUSDC,
		defaultTokenFeeReserveLamports, defaultTokenSetupRentLamports,
		artifact.Candidate.QuoteSlippageBPS, defaultPaperFeeLamports,
		artifact.Observe, uint64(artifact.Thresholds.CadenceSeconds),
	)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := writeShadowPolicy(t, policy)
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runShadowMarketPaperCheck([]string{
		"--policy", policyPath, "--provisional-artifact", artifactPath,
		"--journal", journalPath,
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result marketPaperCheckResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "research_only" || result.Outcome != "insufficient_evidence" ||
		!result.PaperOnly || result.Authorized || result.Promotable ||
		result.Market != marketadmission.MarketWIFUSDC || result.InputSHA256 == "" ||
		result.TrainingCoverageBPS != 9_250 || result.HoldoutCoverageBPS != 10_000 ||
		result.ModelledSpreadBPS != 25 || result.StressModelledSpreadBPS != 50 ||
		result.CostModelRule != marketPaperCheckCostModelRule ||
		!strings.Contains(strings.Join(result.Reasons, ","), "training_coverage_below_95_percent") ||
		result.Candidate != nil || result.CandidatePolicySHA256 != "" {
		t.Fatalf("paper check = %+v", result)
	}
	after, err := os.ReadFile(journalPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("paper check changed its evidence journal")
	}
	var repeated bytes.Buffer
	if err := runShadowMarketPaperCheck([]string{
		"--policy", policyPath, "--provisional-artifact", artifactPath,
		"--journal", journalPath,
	}, &repeated); err != nil || !bytes.Equal(output.Bytes(), repeated.Bytes()) {
		t.Fatal("paper check was not deterministic for one exact prefix")
	}
	if err := runShadowMarketPaperCheck([]string{
		"--policy", policyPath, "--provisional-artifact", artifactPath,
		"--journal", journalPath,
		"--out", filepath.Join(t.TempDir(), "candidate.json"),
	}, &bytes.Buffer{}); err == nil {
		t.Fatal("paper check accepted an output or activation path")
	}
	if err := runShadowMarketPaperCheck([]string{
		"--policy", policyPath, "--provisional-artifact", artifactPath,
		"--journal", journalPath, "--spread-bps", "1",
	}, &bytes.Buffer{}); err == nil {
		t.Fatal("paper check accepted an operator-selected spread")
	}
	resultPath := filepath.Join(t.TempDir(), "paper-check.json")
	if err := os.WriteFile(resultPath, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadQualifiedMarketAdmission(resultPath, journalPath, now); err == nil {
		t.Fatal("paper check output loaded as qualified market admission")
	}
	if _, err := loadProvisionalMarketAdmission(resultPath, journalPath, now); err == nil {
		t.Fatal("paper check output loaded as provisional market admission")
	}
	if _, err := loadShadowPolicy(resultPath); err == nil {
		t.Fatal("paper check output loaded as an active policy")
	}
	if _, err := loadShadowPaperCandidate(resultPath, policy); err == nil {
		t.Fatal("paper check output loaded as a selectable candidate")
	}
}

func TestProvisionalMarketTicksPreserveFailureTime(t *testing.T) {
	artifactPath, journalPath, now := writeReadyProvisionalEvidence(t)
	artifact, err := loadProvisionalMarketAdmission(artifactPath, journalPath, now)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := buildAdaptiveProvisionalPolicy(
		artifact, artifact.Candidate.QuoteNotionalUSDC,
		defaultTokenFeeReserveLamports, defaultTokenSetupRentLamports,
		artifact.Candidate.QuoteSlippageBPS, defaultPaperFeeLamports,
		artifact.Observe, uint64(artifact.Thresholds.CadenceSeconds),
	)
	if err != nil {
		t.Fatal(err)
	}
	points, err := artifact.ReplayPoints(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	first := 0
	for !points[first].Available {
		first++
	}
	failureAt := points[first].Bucket.Add(55 * time.Second)
	points[first].Available = false
	points[first].At = failureAt
	ticks, err := provisionalMarketTicks(policy, points[first:first+1])
	if err != nil {
		t.Fatal(err)
	}
	if len(ticks) != 1 || ticks[0].At != failureAt || ticks[0].Event != shadow.EventUnobservable {
		t.Fatalf("failure tick = %+v", ticks)
	}
}

func TestProvisionalMarketPaperCheckSelectsThenPassesUntouchedHoldoutAndStress(t *testing.T) {
	artifactPath, journalPath, now := writeReadyProvisionalEvidence(t)
	artifact, err := loadProvisionalMarketAdmission(artifactPath, journalPath, now)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := buildAdaptiveProvisionalPolicy(
		artifact, artifact.Candidate.QuoteNotionalUSDC,
		defaultTokenFeeReserveLamports, defaultTokenSetupRentLamports,
		artifact.Candidate.QuoteSlippageBPS, defaultPaperFeeLamports,
		artifact.Observe, uint64(artifact.Thresholds.CadenceSeconds),
	)
	if err != nil {
		t.Fatal(err)
	}
	policy.Adaptive.FastWindow = 2
	policy.Adaptive.SlowWindow = 3
	policy.Adaptive.CooldownSeconds = 0
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	points, err := artifact.ReplayPoints(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var sample marketadmission.ProvisionalReplayPoint
	for _, point := range points {
		if point.Available {
			sample = point
			break
		}
	}
	prices := []uint64{200_000, 200_000, 196_000, 196_000, 204_000, 204_000, 200_000, 200_000}
	for index := range points {
		phase := index
		if index >= marketPaperCheckTrainingHours*60 {
			phase -= marketPaperCheckTrainingHours * 60
		}
		at := points[index].Bucket.Add(time.Second)
		points[index].At, points[index].Available = at, true
		points[index].MarketPrimary = sample.MarketPrimary
		points[index].MarketSecondary = sample.MarketSecondary
		points[index].NativePrimary = sample.NativePrimary
		points[index].NativeSecondary = sample.NativeSecondary
		points[index].MarketPrimary.PriceMicros = prices[phase%len(prices)]
		points[index].MarketSecondary.PriceMicros = prices[phase%len(prices)]
		points[index].MarketPrimary.PublishedAt = at.Add(-time.Second)
		points[index].MarketSecondary.PublishedAt = at.Add(-time.Second)
		points[index].NativePrimary.PublishedAt = at.Add(-time.Second)
		points[index].NativeSecondary.PublishedAt = at.Add(-time.Second)
	}
	result, err := checkProvisionalMarketPaper(policy, artifact, points)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "candidate_ready_for_more_paper_testing" ||
		result.Candidate == nil || result.CandidatesEvaluated == 0 ||
		result.Training == nil || result.Holdout == nil || result.Stress == nil ||
		result.Training.NetReturnMicros <= 0 || result.Holdout.NetReturnMicros <= 0 ||
		result.Stress.NetReturnMicros <= 0 || result.Holdout.VersusHoldMicros <= 0 ||
		result.Stress.VersusHoldMicros <= 0 || result.Holdout.FullRoundTrips < 2 ||
		result.Stress.FullRoundTrips < 2 || result.Holdout.Sells != result.Holdout.Buys ||
		result.Stress.Sells != result.Stress.Buys || result.Holdout.Pending || result.Stress.Pending ||
		*result.Holdout == *result.Stress ||
		len(result.Reasons) != 0 {
		t.Fatalf("paper check = %+v training=%+v holdout=%+v stress=%+v", result, result.Training, result.Holdout, result.Stress)
	}

	changedHoldout := append([]marketadmission.ProvisionalReplayPoint(nil), points...)
	for index := marketPaperCheckTrainingHours * 60; index < len(changedHoldout); index++ {
		changedHoldout[index].MarketPrimary.PriceMicros = 210_000
		changedHoldout[index].MarketSecondary.PriceMicros = 210_000
	}
	changed, err := checkProvisionalMarketPaper(policy, artifact, changedHoldout)
	if err != nil {
		t.Fatal(err)
	}
	if changed.CandidatePolicySHA256 != result.CandidatePolicySHA256 ||
		changed.CandidatesEvaluated != result.CandidatesEvaluated ||
		changed.Training == nil || *changed.Training != *result.Training ||
		changed.Outcome != "candidate_rejected" ||
		!strings.Contains(strings.Join(changed.Reasons, ","), "holdout_") {
		t.Fatalf("held-out prices changed training selection: before=%+v after=%+v", result, changed)
	}
}

func TestProvisionalMarketTicksRejectRepeatedSamplesAndCadenceDrift(t *testing.T) {
	artifactPath, journalPath, now := writeReadyProvisionalEvidence(t)
	artifact, err := loadProvisionalMarketAdmission(artifactPath, journalPath, now)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := buildAdaptiveProvisionalPolicy(
		artifact, artifact.Candidate.QuoteNotionalUSDC,
		defaultTokenFeeReserveLamports, defaultTokenSetupRentLamports,
		artifact.Candidate.QuoteSlippageBPS, defaultPaperFeeLamports,
		artifact.Observe, uint64(artifact.Thresholds.CadenceSeconds),
	)
	if err != nil {
		t.Fatal(err)
	}
	points, err := artifact.ReplayPoints(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	first := 0
	for !points[first].Available {
		first++
	}
	points[first+1] = points[first]
	points[first+1].Bucket = points[first].Bucket.Add(time.Minute)
	points[first+1].At = points[first].At.Add(time.Minute)
	ticks, err := provisionalMarketTicks(policy, points[first:first+2])
	if err != nil {
		t.Fatal(err)
	}
	if ticks[0].Event == shadow.EventUnobservable || ticks[1].Event != shadow.EventUnobservable ||
		ticks[1].PriceMicros != 0 {
		t.Fatalf("repeated source ticks = %+v", ticks)
	}
	policy.TickSeconds /= 2
	if _, err := checkProvisionalMarketPaper(policy, artifact, points); err == nil ||
		!strings.Contains(err.Error(), "cadence") {
		t.Fatalf("cadence mismatch error = %v", err)
	}
}

func TestMarketPaperScoreReasonsRequireTradesProfitAndBoundedDrawdown(t *testing.T) {
	reasons := marketPaperScoreReasons("holdout", marketPaperCheckScore{
		Sells: 2, Buys: 1, Refused: 1, Missed: 1, Pending: true,
		NetReturnMicros: -1, VersusHoldMicros: -1, MaxDrawdownBPS: 301,
	}, shadowInitialRoundTrips, 300)
	joined := strings.Join(reasons, ",")
	for _, want := range []string{
		"holdout_completed_fewer_than_2_round_trips",
		"holdout_has_unmatched_filled_leg",
		"holdout_has_pending_decision",
		"holdout_has_failed_execution",
		"holdout_net_return_not_positive",
		"holdout_did_not_beat_holding",
		"holdout_drawdown_above_policy_limit",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("reasons %q omit %q", joined, want)
		}
	}
	if got := marketPaperScoreReasons("stress", marketPaperCheckScore{
		FullRoundTrips: 2, Sells: 2, Buys: 2,
		NetReturnMicros: 1, VersusHoldMicros: 1, MaxDrawdownBPS: 300,
	}, shadowInitialRoundTrips, 300); len(got) != 0 {
		t.Fatalf("passing score reasons = %v", got)
	}
}

func TestSplitMarketPaperPointsUsesBucketsNotObservationTimes(t *testing.T) {
	boundary := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	points := []marketadmission.ProvisionalReplayPoint{
		{Bucket: boundary.Add(-time.Minute), At: boundary.Add(time.Second)},
		{Bucket: boundary, At: boundary.Add(-time.Second)},
	}
	training, holdout := splitMarketPaperPoints(points, boundary)
	if len(training) != 1 || len(holdout) != 1 ||
		training[0].Bucket != points[0].Bucket || holdout[0].Bucket != points[1].Bucket {
		t.Fatalf("bucket split = training %+v holdout %+v", training, holdout)
	}
}
