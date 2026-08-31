package main

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestShadowChallengeQualifiesPairedForwardPaperEvidence(t *testing.T) {
	base := validShadowPolicy()
	champion := candidateForPrices(t, base, 200_000_000, 100_000_000)
	challenger := candidateForPrices(t, base, 220_000_000, 110_000_000)
	from := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	championReports := challengeReports(from, 7, 0)
	challengerReports := challengeReports(from, 7, 2_000)
	for index := range challengerReports {
		championReports[index].Counts.Fills = 2
		challengerReports[index].Counts.Fills = 2
		championReports[index].MaxDrawdownMicros = 5_000
		challengerReports[index].MaxDrawdownMicros = 4_000
	}

	result, err := qualifyShadowChallenger(
		champion, challenger, championReports, challengerReports,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "challenger_qualified_for_paper_selection" ||
		result.Authorized || result.Promotable || !result.PaperOnly ||
		!result.EligibleForPaperSelection || result.PointerUpdated ||
		result.ChallengerFullRoundTrips != 7 || result.ChallengerDailyWins != 7 ||
		result.AggregateAdvantageMicros != 14_000 ||
		result.RequiredAggregateAdvantageMicros != 7_000 || len(result.Reasons) != 0 {
		t.Fatalf("qualification crossed its boundary or scored incorrectly: %+v", result)
	}
}

func TestRetrospectiveChallengeCannotEmitAForwardQualification(t *testing.T) {
	qualified := shadowChallengeResult{
		Status: "challenger_qualified_for_paper_selection", PaperOnly: true,
		EligibleForPaperSelection: true,
	}
	retrospective := constrainRetrospectiveChallenge(qualified, time.Time{})
	if retrospective.Status != "retrospective_comparison_not_forward_qualified" ||
		!slices.Contains(retrospective.Reasons, "preselection_evidence_not_forward_qualified") {
		t.Fatalf("retrospective result = %+v", retrospective)
	}
	eligible := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	if forward := constrainRetrospectiveChallenge(qualified, eligible); forward.Status != qualified.Status {
		t.Fatalf("forward result was downgraded: %+v", forward)
	}
}

func TestShadowChallengeReturnsFixedReasonsWithoutSelecting(t *testing.T) {
	base := validShadowPolicy()
	champion := candidateForPrices(t, base, 200_000_000, 100_000_000)
	challenger := candidateForPrices(t, base, 220_000_000, 110_000_000)
	from := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	championReports := challengeReports(from, 7, 1_000)
	challengerReports := challengeReports(from, 7, 500)
	championReports[0].Counts.Missed = 1
	challengerReports[0].Counts.Missed = 1
	for index := range challengerReports {
		challengerReports[index].Counts.Fills = 1
		challengerReports[index].MaxDrawdownMicros = 4_000
	}

	result, err := qualifyShadowChallenger(
		champion, challenger, championReports, challengerReports,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"champion_has_missed_decisions",
		"challenger_has_missed_decisions",
		"insufficient_challenger_round_trips",
		"no_strict_daily_majority",
		"advantage_below_ten_bps",
		"drawdown_regressed",
	}
	if result.Status != "challenger_not_qualified" || result.PointerUpdated ||
		!slices.Equal(result.Reasons, want) {
		t.Fatalf("non-qualification = %+v, want reasons %v", result, want)
	}
}

func TestShadowChallengeRejectsUnpairedOrPreValidationEvidence(t *testing.T) {
	base := validShadowPolicy()
	champion := candidateForPrices(t, base, 200_000_000, 100_000_000)
	challenger := candidateForPrices(t, base, 220_000_000, 110_000_000)
	from := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	reports := challengeReports(from, 7, 1_000)
	if _, err := qualifyShadowChallenger(champion, challenger, reports, reports[:6]); err == nil {
		t.Fatal("an unpaired window was accepted")
	}
	mismatched := slices.Clone(reports)
	mismatched[0].From = mismatched[0].From.Add(time.Hour)
	if _, err := qualifyShadowChallenger(champion, challenger, reports, mismatched); err == nil {
		t.Fatal("mismatched paired dates were accepted")
	}
	preValidation := challengeReports(time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC), 7, 1_000)
	if _, err := qualifyShadowChallenger(champion, challenger, preValidation, preValidation); err == nil ||
		!strings.Contains(err.Error(), "after both validation") {
		t.Fatalf("pre-validation error = %v", err)
	}
}

func TestShadowChallengeChecksSignedDifferenceAndHelpBoundary(t *testing.T) {
	if _, err := checkedDifference(math.MaxInt64, -1); err == nil {
		t.Fatal("positive difference overflow was accepted")
	}
	if _, err := checkedDifference(math.MinInt64, 1); err == nil {
		t.Fatal("negative difference overflow was accepted")
	}
	if got, err := checkedDifference(7, -2); err != nil || got != 9 {
		t.Fatalf("checked difference = %d, %v", got, err)
	}
	var output bytes.Buffer
	if err := runShadowChallenge([]string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"never edits", "cannot authorize, sign, submit, or enable"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("challenge help omitted %q:\n%s", want, output.String())
		}
	}
}

func TestShadowChallengeDetectsChampionArtifactReplacement(t *testing.T) {
	root := privateTestDirectory(t)
	base := validShadowPolicy()
	base.TickSeconds = 3_600
	policyPath := writeShadowPolicy(t, base)
	champion := candidateForPrices(t, base, 200_000_000, 100_000_000)
	challenger := candidateForPrices(t, base, 220_000_000, 110_000_000)
	championPath := filepath.Join(root, "champion.json")
	challengerPath := filepath.Join(root, "challenger.json")
	if err := writeShadowPaperCandidate(championPath, champion); err != nil {
		t.Fatal(err)
	}
	if err := writeShadowPaperCandidate(challengerPath, challenger); err != nil {
		t.Fatal(err)
	}
	pointerPath := filepath.Join(root, "selected")
	if err := runShadowSelect([]string{
		"--policy", policyPath, "--candidate", championPath, "--pointer", pointerPath,
		"--lifecycle-lock", filepath.Join(root, "lifecycle.lock"),
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	pointerBefore, err := os.ReadFile(pointerPath)
	if err != nil {
		t.Fatal(err)
	}
	championRoot := filepath.Join(root, "champion-run")
	challengerRoot := filepath.Join(root, "challenger-run")
	championDir := filepath.Join(championRoot, champion.CandidatePolicySHA256)
	challengerDir := filepath.Join(challengerRoot, challenger.CandidatePolicySHA256)
	for _, directory := range []string{championDir, challengerDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for remaining := 7; remaining > 0; remaining-- {
		from := today.AddDate(0, 0, -remaining)
		writeCompleteShadowDay(t, championDir, champion.Policy, from)
		writeCompleteShadowDay(t, challengerDir, challenger.Policy, from)
	}

	originalHook := shadowChallengeAfterLoad
	defer func() { shadowChallengeAfterLoad = originalHook }()
	shadowChallengeAfterLoad = func() {
		raw, readErr := os.ReadFile(championPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		replaced := bytes.Replace(
			raw, []byte(strings.Repeat("a", 64)), []byte(strings.Repeat("c", 64)), 1,
		)
		if bytes.Equal(raw, replaced) {
			t.Fatal("test candidate had no replaceable provenance")
		}
		if writeErr := os.WriteFile(championPath, replaced, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	err = runShadowChallenge([]string{
		"--policy", policyPath,
		"--champion-pointer", pointerPath, "--challenger", challengerPath,
		"--champion-dir", championRoot, "--challenger-dir", challengerRoot,
		"--days", "7",
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "champion candidate changed") {
		t.Fatalf("champion replacement error = %v", err)
	}
	pointerAfter, err := os.ReadFile(pointerPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pointerBefore, pointerAfter) {
		t.Fatal("read-only challenge changed the champion pointer")
	}
}

func challengeReports(from time.Time, days int, versusHold int64) []shadow.Report {
	reports := make([]shadow.Report, days)
	for index := range reports {
		reports[index] = completeShadowReport(from.Add(time.Duration(index) * 24 * time.Hour))
		reports[index].VersusHoldMicros = versusHold
	}
	return reports
}
