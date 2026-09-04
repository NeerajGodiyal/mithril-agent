package marketadmission

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
)

func TestDashboardStatusIsAValidatedSanitizedSixHourDiagnostic(t *testing.T) {
	candidate, _ := Lookup(MarketWIFUSDC)
	opening, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 4, 12, 34, 45, 0, time.UTC)
	through := now.Truncate(time.Minute)
	observations := observationsFor(t, opening, through.Add(-2*time.Minute), through, 12)
	observations[1].Failure = FailureSellQuote
	status, err := BuildDashboardStatus(opening, dashboardRecords(t, opening, observations), now)
	if err != nil {
		t.Fatal(err)
	}
	if status.Validate() != nil || status.Kind != DashboardStatusKind ||
		status.Market != MarketWIFUSDC || status.WindowHours != DashboardStatusWindowHours ||
		status.Diagnostic.From != through.Add(-6*time.Hour) ||
		status.Diagnostic.Through != through || status.Diagnostic.ExpectedBuckets != 360 ||
		status.Diagnostic.ObservedBuckets != 2 || status.Diagnostic.AvailableBuckets != 1 ||
		status.Diagnostic.AvailabilityBPS != 27 ||
		status.Diagnostic.FailureCounts[FailureSellQuote] != 1 ||
		status.Diagnostic.FailureCounts["missing_bucket"] != 358 {
		t.Fatalf("dashboard status = %+v", status)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDashboardStatus(raw)
	if err != nil || loaded.Market != status.Market {
		t.Fatalf("loaded dashboard status = %+v, %v", loaded, err)
	}
	for _, forbidden := range []string{
		testObserve, candidate.BaseMint, `"provider`, `"journal`, `sha256"`,
		`"authorized`, `"paper_only`, `"activation`, `"observe"`,
	} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("dashboard status exposes %q: %s", forbidden, raw)
		}
	}
}

func TestDashboardStatusRejectsIncoherentOrOperationalClaims(t *testing.T) {
	candidate, _ := Lookup(MarketJTOUSDC)
	opening, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 4, 12, 0, 30, 0, time.UTC)
	status, err := BuildDashboardStatus(opening, dashboardRecords(t, opening, nil), now)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*DashboardStatus){
		"qualified": func(value *DashboardStatus) {
			value.Diagnostic.OperationallyQualified = true
		},
		"coverage": func(value *DashboardStatus) { value.Diagnostic.AvailabilityBPS++ },
		"missing failures": func(value *DashboardStatus) {
			value.Diagnostic.FailureCounts = map[string]uint64{}
		},
		"unknown failure": func(value *DashboardStatus) {
			value.Diagnostic.FailureCounts = map[string]uint64{"secret provider error": 360}
		},
		"misclassified missing buckets": func(value *DashboardStatus) {
			value.Diagnostic.FailureCounts = map[string]uint64{FailureBuyQuote: 360}
		},
		"measurement without data": func(value *DashboardStatus) {
			value.Diagnostic.P95RouteCostBPS = 1
		},
		"stale timestamp": func(value *DashboardStatus) {
			value.UpdatedAt = value.Diagnostic.Through.Add(time.Minute)
		},
	} {
		t.Run(name, func(t *testing.T) {
			tampered := status
			tampered.Diagnostic.FailureCounts = cloneFailureCounts(status.Diagnostic.FailureCounts)
			mutate(&tampered)
			if tampered.Validate() == nil {
				t.Fatalf("tampered dashboard status was accepted: %+v", tampered)
			}
		})
	}
}

func TestDashboardPaperCheckAcceptsEveryBoundedOutcome(t *testing.T) {
	status := dashboardStatusForPaperCheck(t)
	through := status.Diagnostic.Through
	checks := []DashboardPaperCheck{
		{
			Market: status.Market, CheckedAt: through.Add(time.Minute), Through: through,
			Outcome:             DashboardPaperOutcomeInsufficientEvidence,
			TrainingCoverageBPS: 9_000, HoldoutCoverageBPS: 10_000,
			Reasons: []string{"training_coverage_below_95_percent"},
		},
		{
			Market: status.Market, CheckedAt: through.Add(time.Minute), Through: through,
			Outcome:             DashboardPaperOutcomeNoTrainingCandidate,
			TrainingCoverageBPS: 10_000, HoldoutCoverageBPS: 10_000,
			Reasons: []string{"no_qualified_training_candidate"},
		},
		{
			Market: status.Market, CheckedAt: through.Add(time.Minute), Through: through,
			Outcome:             DashboardPaperOutcomeCandidateRejected,
			TrainingCoverageBPS: 10_000, HoldoutCoverageBPS: 10_000,
			HoldoutAfterCostNetReturnMicros: -1, HoldoutAfterCostVersusHoldMicros: -2,
			StressAfterCostNetReturnMicros: -3, StressAfterCostVersusHoldMicros: -4,
			Reasons: []string{"holdout_net_return_not_positive", "holdout_did_not_beat_holding"},
		},
		{
			Market: status.Market, CheckedAt: through.Add(time.Minute), Through: through,
			Outcome:             DashboardPaperOutcomeCandidateReady,
			TrainingCoverageBPS: 10_000, HoldoutCoverageBPS: 10_000,
			HoldoutAfterCostNetReturnMicros: 1, HoldoutAfterCostVersusHoldMicros: 2,
			StressAfterCostNetReturnMicros: 3, StressAfterCostVersusHoldMicros: 4,
			Reasons: []string{},
		},
	}
	for _, check := range checks {
		t.Run(check.Outcome, func(t *testing.T) {
			withCheck, err := status.WithPaperCheck(check)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(withCheck)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadDashboardStatus(raw)
			if err != nil || loaded.PaperCheck == nil || loaded.PaperCheck.Outcome != check.Outcome {
				t.Fatalf("loaded paper check = %+v, %v", loaded.PaperCheck, err)
			}
		})
	}
}

func TestDashboardPaperCheckCurrentMatchesTheProvisionalStartupWindow(t *testing.T) {
	through := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	check := DashboardPaperCheck{Through: through, CheckedAt: through}
	if !check.Current(through.Add(2*time.Minute)) ||
		check.Current(through.Add(2*time.Minute+time.Nanosecond)) ||
		check.Current(through.Add(-time.Nanosecond)) ||
		check.Current(through.Add(24*time.Hour)) {
		t.Fatal("paper-check currency diverged from the provisional startup window")
	}
}

func TestDashboardPaperCheckRejectsMismatchAndUnknownReasons(t *testing.T) {
	status := dashboardStatusForPaperCheck(t)
	valid := DashboardPaperCheck{
		Market: status.Market, CheckedAt: status.Diagnostic.Through.Add(time.Minute),
		Through: status.Diagnostic.Through, Outcome: DashboardPaperOutcomeCandidateRejected,
		TrainingCoverageBPS: 10_000, HoldoutCoverageBPS: 10_000,
		Reasons: []string{"holdout_has_failed_execution"},
	}
	for name, mutate := range map[string]func(*DashboardPaperCheck){
		"market": func(check *DashboardPaperCheck) { check.Market = MarketJTOUSDC },
		"future window": func(check *DashboardPaperCheck) {
			check.Through = status.Diagnostic.Through.Add(time.Minute)
		},
		"unknown reason": func(check *DashboardPaperCheck) {
			check.Reasons = []string{"provider said something secret"}
		},
		"duplicate reason": func(check *DashboardPaperCheck) {
			check.Reasons = []string{"holdout_has_failed_execution", "holdout_has_failed_execution"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			check := valid
			check.Reasons = append([]string{}, valid.Reasons...)
			mutate(&check)
			if _, err := status.WithPaperCheck(check); err == nil {
				t.Fatalf("invalid paper check was accepted: %+v", check)
			}
		})
	}
	invalidInsufficient := valid
	invalidInsufficient.Outcome = DashboardPaperOutcomeInsufficientEvidence
	invalidInsufficient.Reasons = []string{}
	if _, err := status.WithPaperCheck(invalidInsufficient); err == nil {
		t.Fatal("insufficient-evidence outcome with complete coverage was accepted")
	}
}

func TestDashboardStatusCannotDecodeAsAnActivationArtifact(t *testing.T) {
	candidate, _ := Lookup(MarketPYTHUSDC)
	opening, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	status, err := BuildDashboardStatus(
		opening, dashboardRecords(t, opening, nil),
		time.Date(2026, time.September, 4, 12, 0, 30, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	status, err = status.WithPaperCheck(DashboardPaperCheck{
		Market: status.Market, CheckedAt: status.Diagnostic.Through.Add(time.Minute),
		Through: status.Diagnostic.Through, Outcome: DashboardPaperOutcomeInsufficientEvidence,
		TrainingCoverageBPS: 9_000, HoldoutCoverageBPS: 10_000,
		Reasons: []string{"training_coverage_below_95_percent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var artifact Artifact
	if err := strictjson.Decode(raw, &artifact); err == nil {
		t.Fatal("dashboard status decoded as a market admission artifact")
	}
	var provisional ProvisionalArtifact
	if err := strictjson.Decode(raw, &provisional); err == nil {
		t.Fatal("dashboard status decoded as a provisional market artifact")
	}
	if _, err := LoadDashboardStatus(append(raw, []byte(`{"extra":true}`)...)); err == nil {
		t.Fatal("dashboard status accepted trailing JSON")
	}
}

func dashboardStatusForPaperCheck(t *testing.T) DashboardStatus {
	t.Helper()
	candidate, _ := Lookup(MarketWIFUSDC)
	opening, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	status, err := BuildDashboardStatus(
		opening, dashboardRecords(t, opening, nil),
		time.Date(2026, time.September, 4, 12, 0, 30, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestDashboardStatusRefusesRecordsForAnotherOpening(t *testing.T) {
	candidate, _ := Lookup(MarketWIFUSDC)
	opening, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewOpening(
		candidate, "So11111111111111111111111111111111111111112", DefaultThresholds(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildDashboardStatus(
		opening, dashboardRecords(t, other, nil), time.Now(),
	); err == nil || !strings.Contains(err.Error(), "another opening") {
		t.Fatalf("foreign opening records were accepted: %v", err)
	}
}

func TestDiagnosticTrackerKeepsOnlySixHoursPlusTheCurrentBoundary(t *testing.T) {
	candidate, _ := Lookup(MarketWIFUSDC)
	opening, err := NewOpening(candidate, testObserve, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	through := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	history := observationsFor(t, opening, through.Add(-8*time.Hour), through, 12)
	tracker, err := NewDiagnosticTracker(
		opening, dashboardRecords(t, opening, history), through.Add(30*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(tracker.observations); got != 360 {
		t.Fatalf("retained startup observations = %d, want 360", got)
	}
	if got := cap(tracker.observations); got > 361 {
		t.Fatalf("retained startup capacity = %d, want at most 361", got)
	}
	boundary := Observation{
		Version: Version, OpeningSHA256: opening.ContentSHA256,
		Bucket: through, ObservedAt: through.Add(time.Second), Failure: FailureBuyQuote,
	}
	if err := tracker.Add(boundary); err != nil {
		t.Fatal(err)
	}
	if got := len(tracker.observations); got != 361 {
		t.Fatalf("retained boundary observations = %d, want 361", got)
	}
	if got := cap(tracker.observations); got > 361 {
		t.Fatalf("retained boundary capacity = %d, want at most 361", got)
	}
	if err := tracker.Add(boundary); err == nil {
		t.Fatal("duplicate dashboard observation was accepted")
	}
	status, err := tracker.Status(through.Add(30 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if status.Diagnostic.ObservedBuckets != 360 || status.Diagnostic.AvailableBuckets != 360 {
		t.Fatalf("boundary leaked into incomplete window: %+v", status.Diagnostic)
	}
}

func dashboardRecords(
	t *testing.T,
	opening Opening,
	observations []Observation,
) []journal.Record {
	t.Helper()
	openingRaw, err := json.Marshal(opening)
	if err != nil {
		t.Fatal(err)
	}
	records := []journal.Record{{
		Type: EventOpened, ActionID: opening.ContentSHA256, Payload: openingRaw,
	}}
	for _, observation := range observations {
		raw, err := json.Marshal(observation)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, journal.Record{
			At: observation.ObservedAt, Type: EventObserved,
			ActionID: observation.Bucket.Format(time.RFC3339), Payload: raw,
		})
	}
	return records
}

func cloneFailureCounts(source map[string]uint64) map[string]uint64 {
	result := make(map[string]uint64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
