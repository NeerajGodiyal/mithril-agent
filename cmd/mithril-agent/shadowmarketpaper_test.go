package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/marketadmission"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
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
	rejectedCandidatePath := filepath.Join(filepath.Dir(journalPath), "rejected-policy.json")
	rejectedResultPath := filepath.Join(filepath.Dir(journalPath), "rejected-result.json")
	if err := runShadowMarketPaperCheck([]string{
		"--policy", policyPath, "--provisional-artifact", artifactPath,
		"--journal", journalPath, "--result-out", rejectedResultPath,
		"--candidate-policy-out", rejectedCandidatePath,
	}, &output); err == nil || !strings.Contains(err.Error(), "did not pass") {
		t.Fatalf("paper-check rejection error = %v", err)
	}
	if _, err := os.Lstat(rejectedCandidatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected paper-check wrote a candidate policy: %v", err)
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
	dashboardStatusPath := filepath.Join(filepath.Dir(journalPath), "dashboard-status.json")
	writeTestMarketDashboardStatus(t, dashboardStatusPath, result.Market, result.Through)
	var projected bytes.Buffer
	if err := runShadowMarketPaperCheck([]string{
		"--policy", policyPath, "--provisional-artifact", artifactPath,
		"--journal", journalPath, "--dashboard-status", dashboardStatusPath,
	}, &projected); err == nil || !strings.Contains(err.Error(), "did not pass") {
		t.Fatalf("projected paper-check rejection error = %v", err)
	}
	projectedRaw, err := os.ReadFile(dashboardStatusPath)
	if err != nil {
		t.Fatal(err)
	}
	projectedStatus, err := marketadmission.LoadDashboardStatus(projectedRaw)
	if err != nil || projectedStatus.PaperCheck == nil ||
		projectedStatus.PaperCheck.Outcome != marketadmission.DashboardPaperOutcomeInsufficientEvidence ||
		projectedStatus.PaperCheck.TrainingCoverageBPS != result.TrainingCoverageBPS ||
		projectedStatus.PaperCheck.HoldoutCoverageBPS != result.HoldoutCoverageBPS {
		t.Fatalf("projected paper check = %+v, %v", projectedStatus.PaperCheck, err)
	}
	after, err := os.ReadFile(journalPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("paper check changed its evidence journal")
	}
	var repeated bytes.Buffer
	if err := runShadowMarketPaperCheck([]string{
		"--policy", policyPath, "--provisional-artifact", artifactPath,
		"--journal", journalPath,
	}, &repeated); err == nil || !strings.Contains(err.Error(), "did not pass") ||
		!bytes.Equal(output.Bytes(), repeated.Bytes()) {
		t.Fatalf("paper check was not deterministic for one exact prefix: %v", err)
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

func TestShadowMarketPaperCheckWritesExactRunnablePolicy(t *testing.T) {
	artifactPath, journalPath, now := writePassingProvisionalEvidence(t)
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
	policyPath := writeShadowPolicy(t, policy)
	statusPath := filepath.Join(filepath.Dir(journalPath), "dashboard-status.json")
	writeTestMarketDashboardStatus(t, statusPath, artifact.Candidate.Market, artifact.Through)
	candidatePath := filepath.Join(filepath.Dir(journalPath), "checked-policy.json")
	resultPath := filepath.Join(filepath.Dir(journalPath), "paper-check.json")
	var output bytes.Buffer
	if err := runShadowMarketPaperCheck([]string{
		"--policy", policyPath, "--provisional-artifact", artifactPath,
		"--journal", journalPath, "--dashboard-status", statusPath,
		"--result-out", resultPath,
		"--candidate-policy-out", candidatePath,
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result marketPaperCheckResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	checked, err := loadActiveShadowPolicy(candidatePath)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := checked.Fingerprint()
	if err != nil || result.Outcome != marketadmission.DashboardPaperOutcomeCandidateReady ||
		fingerprint != result.CandidatePolicySHA256 ||
		result.ContentSHA256 == "" || !provisionalPolicyMatchesArtifact(checked, artifact) {
		t.Fatalf("checked CLI policy = %q, result=%+v, error=%v", fingerprint, result, err)
	}
	if _, err := loadMarketPaperCheckResult(
		resultPath, checked, artifact, journalPath, now,
	); err != nil {
		t.Fatalf("checked result did not reproduce: %v", err)
	}
	unchecked := checked
	adaptive := *unchecked.Adaptive
	adaptive.MaxDrawdownBPS++
	unchecked.Adaptive = &adaptive
	if err := unchecked.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMarketPaperCheckResult(
		resultPath, unchecked, artifact, journalPath, now,
	); err == nil {
		t.Fatal("paper-check result accepted a different provisional policy")
	}
	forged := result
	forgedHoldout := *forged.Holdout
	forgedHoldout.NetReturnMicros++
	forged.Holdout = &forgedHoldout
	forged.ContentSHA256, err = marketPaperCheckFingerprint(forged)
	if err != nil {
		t.Fatal(err)
	}
	forgedPath := filepath.Join(t.TempDir(), "forged-paper-check.json")
	forgedRaw, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(forgedPath, forgedRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMarketPaperCheckResult(
		forgedPath, checked, artifact, journalPath, now,
	); err == nil {
		t.Fatal("self-hashed fabricated paper-check result was accepted")
	}
	portfolioPath := filepath.Join(t.TempDir(), "portfolio.json")
	if err := runShadowPortfolio([]string{
		"--out", portfolioPath, "--limit-usd", "270", "--max-sol-usd", "300",
		"--book", "wif=" + candidatePath,
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := loadShadowPortfolioForBook(
		portfolioPath, "wif", candidatePath, checked,
	); err != nil {
		t.Fatalf("checked candidate portfolio binding = %v", err)
	}
	if _, err := loadShadowPortfolioForBook(
		portfolioPath, "wif", policyPath, policy,
	); err == nil {
		t.Fatal("candidate portfolio accepted the unchecked base policy")
	}
	t.Setenv(shadowEndpointEnvironment, "")
	options := shadowRunOptions{
		policyPath: candidatePath, directory: t.TempDir(),
		portfolioPath: portfolioPath, portfolioBook: "wif",
		provisionalArtifact: artifactPath, provisionalJournal: journalPath,
	}
	if _, err := openShadowRun(t.Context(), checked, options); err == nil ||
		!strings.Contains(err.Error(), "requires a passing paper-check") {
		t.Fatalf("missing runner paper-check error = %v", err)
	}
	options.paperCheckArtifact = forgedPath
	if _, err := openShadowRun(t.Context(), checked, options); err == nil ||
		!strings.Contains(err.Error(), "paper-check artifact") {
		t.Fatalf("forged runner paper-check error = %v", err)
	}
	options.paperCheckArtifact = resultPath
	if _, err := openShadowRun(t.Context(), checked, options); err == nil ||
		!strings.Contains(err.Error(), shadowEndpointEnvironment) {
		t.Fatalf("checked runner did not reach endpoint validation: %v", err)
	}
	if err := runShadowMarketPaperCheck([]string{
		"--policy", policyPath, "--provisional-artifact", artifactPath,
		"--journal", journalPath, "--result-out", resultPath,
		"--candidate-policy-out", candidatePath,
	}, &bytes.Buffer{}); err == nil {
		t.Fatal("paper-check replaced an existing checked policy")
	}
}

func TestMarketPaperCheckDashboardUpdateRefusesMissingOrMismatchedStatus(t *testing.T) {
	directory := t.TempDir()
	through := time.Now().UTC().Truncate(time.Minute)
	path := filepath.Join(directory, "dashboard-status.json")
	result := marketPaperCheckResult{
		Market: marketadmission.MarketWIFUSDC, Through: through,
		Outcome:             marketadmission.DashboardPaperOutcomeCandidateRejected,
		TrainingCoverageBPS: 10_000, HoldoutCoverageBPS: 10_000,
		Holdout: &marketPaperCheckScore{NetReturnMicros: -1, VersusHoldMicros: -2},
		Stress:  &marketPaperCheckScore{NetReturnMicros: -3, VersusHoldMicros: -4},
		Reasons: []string{"holdout_net_return_not_positive"},
	}
	if err := updateMarketDashboardPaperCheck(path, result, through.Add(time.Minute)); err == nil {
		t.Fatal("missing market dashboard status was accepted")
	}
	writeTestMarketDashboardStatus(t, path, result.Market, through)
	if err := updateMarketDashboardPaperCheck(path, result, through.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	status, err := marketadmission.LoadDashboardStatus(raw)
	if err != nil || status.PaperCheck == nil ||
		status.PaperCheck.HoldoutAfterCostNetReturnMicros != -1 ||
		status.PaperCheck.StressAfterCostVersusHoldMicros != -4 {
		t.Fatalf("updated market dashboard status = %+v, %v", status.PaperCheck, err)
	}
	before := append([]byte(nil), raw...)
	for _, lag := range []time.Duration{time.Minute, 2 * time.Minute} {
		writeTestMarketDashboardStatus(t, path, result.Market, through.Add(lag))
		if err := updateMarketDashboardPaperCheck(path, result, through.Add(lag)); err != nil {
			t.Fatalf("current dashboard status lag %s was rejected: %v", lag, err)
		}
	}
	writeTestMarketDashboardStatus(t, path, result.Market, through.Add(3*time.Minute))
	if err := updateMarketDashboardPaperCheck(path, result, through.Add(3*time.Minute)); err == nil ||
		!strings.Contains(err.Error(), "current paper-check window") {
		t.Fatalf("expired dashboard update error = %v", err)
	}
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	wrongMarket := result
	wrongMarket.Market = marketadmission.MarketJTOUSDC
	if err := updateMarketDashboardPaperCheck(path, wrongMarket, through.Add(time.Minute)); err == nil ||
		!strings.Contains(err.Error(), "another market") {
		t.Fatalf("wrong-market dashboard update error = %v", err)
	}
	wrongWindow := result
	wrongWindow.Through = through.Add(time.Minute)
	if err := updateMarketDashboardPaperCheck(path, wrongWindow, through.Add(time.Minute)); err == nil ||
		!strings.Contains(err.Error(), "paper-check window") {
		t.Fatalf("wrong-window dashboard update error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refused dashboard update changed the prior status")
	}
}

func TestMarketPaperCheckWiresTheStrictDashboardStatusFlag(t *testing.T) {
	directory := t.TempDir()
	err := runShadowMarketPaperCheck([]string{
		"--policy", filepath.Join(directory, "policy.json"),
		"--provisional-artifact", filepath.Join(directory, "provisional.json"),
		"--journal", filepath.Join(directory, "admission.jsonl"),
		"--dashboard-status", filepath.Join(directory, "status.json"),
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "journal sibling dashboard-status.json") {
		t.Fatalf("non-sibling paper-check dashboard status error = %v", err)
	}
}

func TestMarketPaperCheckAllowsLivePrintOnlyButStopsDashboardUpdate(t *testing.T) {
	artifactPath, journalPath, _ := writeReadyProvisionalEvidence(t)
	artifact, err := loadProvisionalMarketAdmission(artifactPath, journalPath, time.Now())
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
	store, err := journal.OpenRotating(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	args := []string{
		"--policy", policyPath, "--provisional-artifact", artifactPath,
		"--journal", journalPath,
	}
	var output bytes.Buffer
	if err := runShadowMarketPaperCheck(args, &output); err == nil ||
		!strings.Contains(err.Error(), "did not pass") || output.Len() == 0 {
		t.Fatalf("live print-only paper-check = %v, %q", err, output.String())
	}
	statusPath := filepath.Join(filepath.Dir(journalPath), "dashboard-status.json")
	writeTestMarketDashboardStatus(t, statusPath, artifact.Candidate.Market, artifact.Through)
	before, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runShadowMarketPaperCheck(
		append(args, "--dashboard-status", statusPath), &bytes.Buffer{},
	)
	if err == nil || !strings.Contains(err.Error(), "stop the market collector") {
		t.Fatalf("live dashboard paper-check error = %v", err)
	}
	after, err := os.ReadFile(statusPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refused live dashboard update changed its status")
	}
	candidatePath := filepath.Join(filepath.Dir(journalPath), "checked.json")
	resultPath := filepath.Join(filepath.Dir(journalPath), "paper-check.json")
	err = runShadowMarketPaperCheck(
		append(args, "--result-out", resultPath, "--candidate-policy-out", candidatePath), &bytes.Buffer{},
	)
	if err == nil || !strings.Contains(err.Error(), "stop the market collector") {
		t.Fatalf("live candidate paper-check error = %v", err)
	}
	if _, statErr := os.Lstat(candidatePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused live paper-check wrote a candidate policy: %v", statErr)
	}
}

func writeTestMarketDashboardStatus(
	t *testing.T,
	path, market string,
	through time.Time,
) {
	t.Helper()
	status := marketadmission.DashboardStatus{
		Version:     marketadmission.Version,
		Kind:        marketadmission.DashboardStatusKind,
		Market:      market,
		UpdatedAt:   through.Add(30 * time.Second),
		WindowHours: marketadmission.DashboardStatusWindowHours,
		Diagnostic: marketadmission.Diagnostic{
			Version: marketadmission.Version, Market: market,
			From: through.Add(-2 * time.Hour), Through: through,
			DiagnosticOnly: true, ExpectedBuckets: 120,
			FailureCounts: map[string]uint64{"missing_bucket": 120},
		},
	}
	if err := status.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
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
		if index >= marketPaperCheckTrainingMinutes {
			phase -= marketPaperCheckTrainingMinutes
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
	candidatePath := filepath.Join(t.TempDir(), "checked-policy.json")
	if err := writeMarketPaperCandidatePolicy(candidatePath, policy, result); err != nil {
		t.Fatal(err)
	}
	checked, err := loadActiveShadowPolicy(candidatePath)
	if err != nil {
		t.Fatal(err)
	}
	checkedSHA256, err := checked.Fingerprint()
	if err != nil || checkedSHA256 != result.CandidatePolicySHA256 ||
		!provisionalPolicyMatchesArtifact(checked, artifact) {
		t.Fatalf("checked policy fingerprint = %q, %v", checkedSHA256, err)
	}
	if err := writeMarketPaperCandidatePolicy(candidatePath, policy, result); err == nil {
		t.Fatal("paper-check replaced an existing checked policy")
	}
	tampered := result
	tampered.CandidatePolicySHA256 = strings.Repeat("0", 64)
	if err := writeMarketPaperCandidatePolicy(
		filepath.Join(t.TempDir(), "tampered.json"), policy, tampered,
	); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("tampered checked policy error = %v", err)
	}

	changedHoldout := append([]marketadmission.ProvisionalReplayPoint(nil), points...)
	for index := marketPaperCheckTrainingMinutes; index < len(changedHoldout); index++ {
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

func TestProvisionalMarketTicksConsumeLateFailurePublicationTimes(t *testing.T) {
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
	hidden := points[first]
	hidden.Available = false
	hidden.MarketPrimaryPublishedAt = hidden.MarketPrimary.PublishedAt
	hidden.MarketSecondaryPublishedAt = hidden.MarketSecondary.PublishedAt
	hidden.MarketPrimary = pricetrigger.Sample{}
	hidden.MarketSecondary = pricetrigger.Sample{}
	hidden.NativePrimary = pricetrigger.Sample{}
	hidden.NativeSecondary = pricetrigger.Sample{}
	reused := points[first]
	reused.Bucket = hidden.Bucket.Add(time.Minute)
	reused.At = hidden.At.Add(time.Minute)
	ticks, err := provisionalMarketTicks(policy, []marketadmission.ProvisionalReplayPoint{hidden, reused})
	if err != nil {
		t.Fatal(err)
	}
	if len(ticks) != 2 || ticks[0].Event != shadow.EventUnobservable ||
		ticks[1].Event != shadow.EventUnobservable {
		t.Fatalf("late failure reused as fresh evidence: %+v", ticks)
	}
}

func TestProvisionalMarketPaperCheckCarriesSourceChronologyIntoHoldout(t *testing.T) {
	artifactPath, journalPath, now := writePassingProvisionalEvidence(t)
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
	boundary := marketPaperCheckTrainingMinutes
	points[boundary].MarketPrimary.PublishedAt = points[boundary-1].MarketPrimary.PublishedAt
	points[boundary].MarketSecondary.PublishedAt = points[boundary-1].MarketSecondary.PublishedAt
	for index := boundary + 1; index <= boundary+6; index++ {
		points[index].Available = false
	}
	result, err := checkProvisionalMarketPaper(policy, artifact, points)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != marketadmission.DashboardPaperOutcomeInsufficientEvidence ||
		result.HoldoutCoverageBPS != 8_250 {
		t.Fatalf("boundary-repeat holdout coverage = %d, outcome %q",
			result.HoldoutCoverageBPS, result.Outcome)
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
