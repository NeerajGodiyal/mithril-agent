package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/marketadmission"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func marketCostFixture(t *testing.T) (shadow.Policy, marketadmission.ProvisionalArtifact, []marketadmission.ProvisionalReplayPoint, []string) {
	t.Helper()
	artifactPath, journalPath, now := writePassingProvisionalEvidence(t)
	artifact, err := loadProvisionalMarketAdmission(artifactPath, journalPath, now)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := buildAdaptiveProvisionalPolicy(artifact, artifact.Candidate.QuoteNotionalUSDC,
		defaultTokenFeeReserveLamports, defaultTokenSetupRentLamports,
		artifact.Candidate.QuoteSlippageBPS, defaultPaperFeeLamports,
		artifact.Observe, uint64(artifact.Thresholds.CadenceSeconds))
	if err != nil {
		t.Fatal(err)
	}
	points, err := artifact.ReplayPoints(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	return policy, artifact, points, []string{"--policy", writeShadowPolicy(t, policy), "--provisional-artifact", artifactPath, "--journal", journalPath, "--cost-experiment", shadow.ObservedNativeCostVersion}
}

func TestMarketCostComparisonStdoutOnlyAndExactBaseline(t *testing.T) {
	policy, artifact, points, args := marketCostFixture(t)
	before, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	var ordinaryBefore bytes.Buffer
	ordinaryErr := runShadowMarketPaperCheck(args[:6], &ordinaryBefore)
	files := map[string][]byte{}
	for _, path := range []string{args[1], args[3], args[5]} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		files[path] = data
	}
	var output bytes.Buffer
	if err := runShadowMarketPaperCheck(args, &output); err != nil {
		t.Fatal(err)
	}
	var result marketPaperCostComparison
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	hash, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if result.PolicySHA256 != hash || result.ProvisionalEvidenceSHA256 != artifact.ContentSHA256 ||
		!reflect.DeepEqual(result.Journal, artifact.Journal) || result.AssumedFeeLamports != policy.FeeLamports ||
		result.AdmissionEvidence || result.TradingEnabled || result.Status != "historical_supplied_policy_comparison" || len(result.Lanes) != 4 {
		t.Fatalf("invalid comparison metadata: %+v", result)
	}
	trainingPoints, holdoutPoints := splitMarketPaperPoints(points, artifact.From.Add(80*time.Minute))
	training, primary, secondary, err := provisionalMarketTicksFrom(policy, trainingPoints, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	holdout, _, _, err := provisionalMarketTicksFrom(policy, holdoutPoints, primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	for i, lane := range result.Lanes {
		ticks, name := training, "training"
		if i >= 2 {
			ticks, name = holdout, "holdout"
		}
		spread := marketPaperCheckSpreadBPS * uint16(1+i%2)
		want, err := shadow.ReplayRoundTripTicksWithLiquidationMarks(policy, ticks, modelledPool(policy, uint64(spread), policy.SlippageBPS))
		if err != nil {
			t.Fatal(err)
		}
		var filtered uint64
		for _, count := range lane.Baseline.FilteredReasons {
			filtered += count
		}
		if filtered != lane.Baseline.Counts.Filtered {
			t.Fatal("filter reason count mismatch")
		}
		lane.Baseline.FilteredReasons = nil
		gotJSON, err := json.Marshal(lane.Baseline)
		if err != nil {
			t.Fatal(err)
		}
		wantJSON, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		if lane.Partition != name || lane.SpreadBPS != spread || !bytes.Equal(gotJSON, wantJSON) {
			t.Fatalf("lane %d changed ordinary baseline", i)
		}
	}
	after, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("base policy mutated")
	}
	var ordinaryAfter bytes.Buffer
	afterErr := runShadowMarketPaperCheck(args[:6], &ordinaryAfter)
	if (ordinaryErr == nil) != (afterErr == nil) || !bytes.Equal(ordinaryBefore.Bytes(), ordinaryAfter.Bytes()) {
		t.Fatal("ordinary paper-check changed after cost mode")
	}
	for path, want := range files {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("input file changed: %v", err)
		}
	}
	for _, flag := range []string{"--result-out", "--candidate-policy-out", "--dashboard-status"} {
		t.Run(flag, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "forbidden.json")
			var rejected bytes.Buffer
			if err := runShadowMarketPaperCheck(append(append([]string{}, args...), flag, path), &rejected); err == nil {
				t.Fatal("accepted output file")
			}
			if rejected.Len() != 0 {
				t.Fatal("emitted success output")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("output created: %v", err)
			}
		})
	}
	otherPolicy := policy
	otherPolicy.MarketEvidenceSHA256 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	otherPolicyPath := writeShadowPolicy(t, otherPolicy)
	_, otherJournalPath, _ := writeReadyProvisionalEvidence(t)
	for _, index := range []int{1, 5} {
		bad := append([]string{}, args...)
		bad[index] = otherPolicyPath
		if index == 5 {
			bad[index] = otherJournalPath
		}
		var rejected bytes.Buffer
		if err := runShadowMarketPaperCheck(bad, &rejected); err == nil || rejected.Len() != 0 {
			t.Fatalf("accepted unrelated input at %d", index)
		}
	}
}

func TestMarketCostComparisonRejectsUnboundInputs(t *testing.T) {
	for _, name := range []string{"evidence", "cadence", "adaptive", "missing_point", "window"} {
		t.Run(name, func(t *testing.T) {
			policy, artifact, points, _ := marketCostFixture(t)
			switch name {
			case "evidence":
				artifact.ContentSHA256 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			case "cadence":
				policy.TickSeconds /= 2
			case "adaptive":
				policy.Adaptive = nil
			case "missing_point":
				points = points[:len(points)-1]
			case "window":
				artifact.Through = artifact.Through.Add(time.Minute)
			}
			var output bytes.Buffer
			if err := writeMarketPaperCostComparison(&output, policy, artifact, points); err == nil {
				t.Fatal("accepted mismatched input")
			}
			if output.Len() != 0 {
				t.Fatal("emitted result for mismatched input")
			}
		})
	}
}

func TestMarketCostComparisonSplitCoverageAndWriterError(t *testing.T) {
	policy, artifact, points, _ := marketCostFixture(t)
	// A repeated provider sample across the split is not a fresh holdout tick.
	points[80].MarketPrimary.PublishedAt = points[79].MarketPrimary.PublishedAt
	points[80].MarketSecondary.PublishedAt = points[79].MarketSecondary.PublishedAt
	for i := 81; i <= 86; i++ {
		points[i].Available = false
	}
	var output bytes.Buffer
	if err := writeMarketPaperCostComparison(&output, policy, artifact, points); err != nil {
		t.Fatal(err)
	}
	var result marketPaperCostComparison
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "insufficient_evidence" || result.TrainingCoverageBPS != 10000 || result.HoldoutCoverageBPS != 8250 || len(result.Lanes) != 0 {
		t.Fatalf("incorrect split coverage: %+v", result)
	}
	want := errors.New("output unavailable")
	if err := writeMarketPaperCostComparison(writerFunc(func([]byte) (int, error) { return 0, want }), policy, artifact, points); !errors.Is(err, want) {
		t.Fatalf("writer error = %v", err)
	}
	policy, artifact, points, _ = marketCostFixture(t)
	if err := writeMarketPaperCostComparison(writerFunc(func([]byte) (int, error) { return 0, want }), policy, artifact, points); !errors.Is(err, want) {
		t.Fatalf("successful-comparison writer error = %v", err)
	}
}
