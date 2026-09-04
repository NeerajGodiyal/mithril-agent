package marketadmission

import (
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
)

const (
	// DashboardStatusKind identifies the diagnostic-only projection schema.
	DashboardStatusKind = "market_collection_dashboard_status"
	// DashboardStatusWindowHours fixes the recent collection window.
	DashboardStatusWindowHours = uint16(2)
	// MaxDashboardStatusBytes bounds a serialized dashboard projection.
	MaxDashboardStatusBytes = 16 << 10

	// DashboardPaperOutcomeInsufficientEvidence means the replay lacked coverage.
	DashboardPaperOutcomeInsufficientEvidence = "insufficient_evidence"
	// DashboardPaperOutcomeNoTrainingCandidate means training produced no usable candidate.
	DashboardPaperOutcomeNoTrainingCandidate = "no_training_candidate"
	// DashboardPaperOutcomeCandidateRejected means holdout or stress gates rejected the candidate.
	DashboardPaperOutcomeCandidateRejected = "candidate_rejected"
	// DashboardPaperOutcomeCandidateReady permits only further paper testing.
	DashboardPaperOutcomeCandidateReady = "candidate_ready_for_more_paper_testing"
)

// DashboardPaperCheck is the sanitized result of one short paper replay.
// It cannot identify an artifact or authorize, select, or activate a policy.
type DashboardPaperCheck struct {
	Market                           string    `json:"market"`
	CheckedAt                        time.Time `json:"checked_at"`
	Through                          time.Time `json:"through"`
	Outcome                          string    `json:"outcome"`
	TrainingCoverageBPS              uint16    `json:"training_coverage_bps"`
	HoldoutCoverageBPS               uint16    `json:"holdout_coverage_bps"`
	HoldoutAfterCostNetReturnMicros  int64     `json:"holdout_after_cost_net_return_micros"`
	HoldoutAfterCostVersusHoldMicros int64     `json:"holdout_after_cost_versus_hold_micros"`
	StressAfterCostNetReturnMicros   int64     `json:"stress_after_cost_net_return_micros"`
	StressAfterCostVersusHoldMicros  int64     `json:"stress_after_cost_versus_hold_micros"`
	Reasons                          []string  `json:"reasons"`
}

// Current reports whether this check still describes the provisional startup
// window. Older results remain useful history but must not look current.
func (check DashboardPaperCheck) Current(now time.Time) bool {
	if check.Through.IsZero() || check.CheckedAt.IsZero() || now.IsZero() {
		return false
	}
	now = now.UTC()
	cadence := time.Duration(DefaultThresholds().CadenceSeconds) * time.Second
	return !check.CheckedAt.After(now.Add(5*time.Second)) &&
		!now.Before(check.Through) && now.Sub(check.Through) <= 2*cadence &&
		check.Through.Truncate(24*time.Hour).Equal(now.Truncate(24*time.Hour))
}

// DashboardStatus is a bounded, diagnostic-only view of recent collection.
// It contains no journal identity, provider detail, or activation authority.
type DashboardStatus struct {
	Version     uint32               `json:"version"`
	Kind        string               `json:"kind"`
	Market      string               `json:"market"`
	UpdatedAt   time.Time            `json:"updated_at"`
	WindowHours uint16               `json:"window_hours"`
	Diagnostic  Diagnostic           `json:"diagnostic"`
	PaperCheck  *DashboardPaperCheck `json:"paper_check,omitempty"`
}

// DiagnosticTracker retains only the recent observations needed for the
// dashboard's fixed two-hour window.
type DiagnosticTracker struct {
	opening      Opening
	observations []Observation
	lastBucket   time.Time
}

// NewDiagnosticTracker validates the collector journal once and bounds its
// retained observations to the dashboard window plus the current boundary.
func NewDiagnosticTracker(
	opening Opening,
	records []journal.Record,
	now time.Time,
) (*DiagnosticTracker, error) {
	stored, observations, err := decodeRecords(records)
	if err != nil {
		return nil, err
	}
	if stored != opening {
		return nil, errors.New("market dashboard status belongs to another opening")
	}
	tracker := &DiagnosticTracker{opening: opening, observations: observations}
	for _, observation := range observations {
		if observation.Bucket.After(tracker.lastBucket) {
			tracker.lastBucket = observation.Bucket.UTC()
		}
	}
	if err := tracker.prune(now); err != nil {
		return nil, err
	}
	return tracker, nil
}

// BuildDashboardStatus validates one collector's current records and derives
// an exact recent two-hour diagnostic.
func BuildDashboardStatus(
	opening Opening,
	records []journal.Record,
	now time.Time,
) (DashboardStatus, error) {
	tracker, err := NewDiagnosticTracker(opening, records, now)
	if err != nil {
		return DashboardStatus{}, err
	}
	return tracker.Status(now)
}

// Add validates and retains one newly appended collector observation.
func (tracker *DiagnosticTracker) Add(observation Observation) error {
	if tracker == nil {
		return errors.New("market dashboard tracker is required")
	}
	if err := observation.Validate(tracker.opening); err != nil {
		return err
	}
	if !tracker.lastBucket.IsZero() && !observation.Bucket.After(tracker.lastBucket) {
		return errors.New("market dashboard observation is not newer")
	}
	if err := tracker.prune(observation.ObservedAt); err != nil {
		return err
	}
	tracker.observations = append(tracker.observations, observation)
	tracker.lastBucket = observation.Bucket.UTC()
	return nil
}

// Status derives a diagnostic from the tracker's bounded recent observations.
func (tracker *DiagnosticTracker) Status(now time.Time) (DashboardStatus, error) {
	if tracker == nil {
		return DashboardStatus{}, errors.New("market dashboard tracker is required")
	}
	if err := tracker.prune(now); err != nil {
		return DashboardStatus{}, err
	}
	diagnostic, err := diagnose(
		tracker.opening, tracker.observations, now,
		time.Duration(DashboardStatusWindowHours)*time.Hour,
	)
	if err != nil {
		return DashboardStatus{}, err
	}
	status := DashboardStatus{
		Version: Version, Kind: DashboardStatusKind,
		Market: tracker.opening.Candidate.Market, UpdatedAt: now.UTC(),
		WindowHours: DashboardStatusWindowHours, Diagnostic: diagnostic,
	}
	if err := status.Validate(); err != nil {
		return DashboardStatus{}, err
	}
	return status, nil
}

func (tracker *DiagnosticTracker) prune(now time.Time) error {
	if now.IsZero() {
		return errors.New("market dashboard status time is required")
	}
	cadence := time.Duration(tracker.opening.Thresholds.CadenceSeconds) * time.Second
	through := now.UTC().Truncate(cadence)
	from := through.Add(-time.Duration(DashboardStatusWindowHours) * time.Hour)
	kept := tracker.observations[:0]
	for _, observation := range tracker.observations {
		if !observation.Bucket.Before(from) && !observation.Bucket.After(through) {
			kept = append(kept, observation)
		}
	}
	tracker.observations = kept
	maximum := int(time.Duration(DashboardStatusWindowHours)*time.Hour/cadence) + 1
	if len(tracker.observations) > maximum {
		return errors.New("market dashboard tracker exceeds its bounded window")
	}
	if cap(tracker.observations) > maximum {
		bounded := make([]Observation, len(tracker.observations), maximum)
		copy(bounded, tracker.observations)
		tracker.observations = bounded
	}
	return nil
}

// LoadDashboardStatus strictly decodes a bounded dashboard status document.
func LoadDashboardStatus(raw []byte) (DashboardStatus, error) {
	var status DashboardStatus
	if len(raw) == 0 || len(raw) > MaxDashboardStatusBytes {
		return status, errors.New("market dashboard status size is invalid")
	}
	if err := strictjson.Decode(raw, &status); err != nil {
		return DashboardStatus{}, err
	}
	if err := status.Validate(); err != nil {
		return DashboardStatus{}, err
	}
	return status, nil
}

// WithPaperCheck returns a validated copy carrying one sanitized short check.
func (status DashboardStatus) WithPaperCheck(check DashboardPaperCheck) (DashboardStatus, error) {
	copyCheck := check
	copyCheck.Reasons = append([]string{}, check.Reasons...)
	status.PaperCheck = &copyCheck
	if err := status.Validate(); err != nil {
		return DashboardStatus{}, err
	}
	return status, nil
}

// Validate checks that a dashboard status is a coherent, recent-window
// diagnostic and cannot claim market qualification.
func (status DashboardStatus) Validate() error {
	diagnostic := status.Diagnostic
	cadence := time.Duration(DefaultThresholds().CadenceSeconds) * time.Second
	window := time.Duration(DashboardStatusWindowHours) * time.Hour
	if status.Version != Version || status.Kind != DashboardStatusKind ||
		status.WindowHours != DashboardStatusWindowHours || status.UpdatedAt.IsZero() ||
		status.UpdatedAt != status.UpdatedAt.UTC() ||
		status.UpdatedAt.Before(diagnostic.Through) ||
		!status.UpdatedAt.Before(diagnostic.Through.Add(cadence)) ||
		diagnostic.Version != Version || diagnostic.Market != status.Market ||
		!diagnostic.DiagnosticOnly || diagnostic.OperationallyQualified ||
		diagnostic.From != diagnostic.From.UTC().Truncate(cadence) ||
		diagnostic.Through != diagnostic.Through.UTC().Truncate(cadence) ||
		diagnostic.Through.Sub(diagnostic.From) != window {
		return errors.New("market dashboard status envelope is invalid")
	}
	if _, ok := Lookup(status.Market); !ok {
		return errors.New("market dashboard status market is invalid")
	}
	expected := uint64(window / cadence)
	if diagnostic.ExpectedBuckets != expected ||
		diagnostic.ObservedBuckets > expected ||
		diagnostic.AvailableBuckets > diagnostic.ObservedBuckets ||
		diagnostic.AvailabilityBPS != availabilityBPS(diagnostic.AvailableBuckets, expected) ||
		diagnostic.FailureCounts == nil ||
		diagnostic.P95RouteCostBPS > 10_000 ||
		diagnostic.P95RouteCostBPS < diagnostic.MedianRouteCostBPS ||
		diagnostic.P95QuoteLatencyMillis > 2*DefaultThresholds().MaximumQuoteLatencyMillis ||
		diagnostic.P95QuoteLatencyMillis < diagnostic.MedianQuoteLatencyMillis {
		return errors.New("market dashboard status counters are invalid")
	}
	failures := uint64(0)
	missing := uint64(0)
	for reason, count := range diagnostic.FailureCounts {
		if !validDiagnosticFailure(reason) || count == 0 {
			return errors.New("market dashboard status failure counters are invalid")
		}
		if count > expected-failures {
			return errors.New("market dashboard status failure counters are invalid")
		}
		failures += count
		if reason == "missing_bucket" {
			missing = count
		}
	}
	if missing != expected-diagnostic.ObservedBuckets ||
		failures-missing != diagnostic.ObservedBuckets-diagnostic.AvailableBuckets ||
		failures != expected-diagnostic.AvailableBuckets {
		return errors.New("market dashboard status failure counters are invalid")
	}
	if diagnostic.AvailableBuckets == 0 &&
		(diagnostic.MedianRouteCostBPS != 0 || diagnostic.P95RouteCostBPS != 0 ||
			diagnostic.MedianQuoteLatencyMillis != 0 || diagnostic.P95QuoteLatencyMillis != 0) {
		return errors.New("market dashboard status measurements are invalid")
	}
	if status.PaperCheck != nil {
		if err := status.PaperCheck.validate(status); err != nil {
			return err
		}
	}
	return nil
}

func (check DashboardPaperCheck) validate(status DashboardStatus) error {
	cadence := time.Duration(DefaultThresholds().CadenceSeconds) * time.Second
	if check.Market != status.Market || check.CheckedAt.IsZero() || check.Through.IsZero() ||
		check.CheckedAt != check.CheckedAt.UTC() ||
		check.Through != check.Through.UTC().Truncate(cadence) ||
		check.CheckedAt.Before(check.Through) || check.Through.After(status.Diagnostic.Through) ||
		check.TrainingCoverageBPS > 10_000 || check.HoldoutCoverageBPS > 10_000 ||
		check.Reasons == nil {
		return errors.New("market dashboard paper check envelope is invalid")
	}
	seen := make(map[string]struct{}, len(check.Reasons))
	for _, reason := range check.Reasons {
		if !validDashboardPaperReason(reason) {
			return errors.New("market dashboard paper check reason is invalid")
		}
		if _, duplicate := seen[reason]; duplicate {
			return errors.New("market dashboard paper check reason is duplicated")
		}
		seen[reason] = struct{}{}
	}
	zeroScores := check.HoldoutAfterCostNetReturnMicros == 0 &&
		check.HoldoutAfterCostVersusHoldMicros == 0 &&
		check.StressAfterCostNetReturnMicros == 0 &&
		check.StressAfterCostVersusHoldMicros == 0
	switch check.Outcome {
	case DashboardPaperOutcomeInsufficientEvidence:
		coverageReasons := dashboardCoverageReasons(check)
		if !zeroScores || len(coverageReasons) == 0 ||
			!equalStrings(check.Reasons, coverageReasons) {
			return errors.New("market dashboard insufficient-evidence check is invalid")
		}
	case DashboardPaperOutcomeNoTrainingCandidate:
		if !zeroScores || check.TrainingCoverageBPS < ProvisionalMinimumAvailabilityBPS ||
			check.HoldoutCoverageBPS < ProvisionalMinimumAvailabilityBPS ||
			!equalStrings(check.Reasons, []string{"no_qualified_training_candidate"}) {
			return errors.New("market dashboard no-candidate check is invalid")
		}
	case DashboardPaperOutcomeCandidateRejected:
		if check.TrainingCoverageBPS < ProvisionalMinimumAvailabilityBPS ||
			check.HoldoutCoverageBPS < ProvisionalMinimumAvailabilityBPS ||
			len(check.Reasons) == 0 || hasDashboardPaperSetupReason(check.Reasons) {
			return errors.New("market dashboard rejected check is invalid")
		}
	case DashboardPaperOutcomeCandidateReady:
		if check.TrainingCoverageBPS < ProvisionalMinimumAvailabilityBPS ||
			check.HoldoutCoverageBPS < ProvisionalMinimumAvailabilityBPS ||
			len(check.Reasons) != 0 || check.HoldoutAfterCostNetReturnMicros <= 0 ||
			check.HoldoutAfterCostVersusHoldMicros <= 0 ||
			check.StressAfterCostNetReturnMicros <= 0 ||
			check.StressAfterCostVersusHoldMicros <= 0 {
			return errors.New("market dashboard ready check is invalid")
		}
	default:
		return errors.New("market dashboard paper check outcome is invalid")
	}
	return nil
}

func dashboardCoverageReasons(check DashboardPaperCheck) []string {
	var reasons []string
	if check.TrainingCoverageBPS < ProvisionalMinimumAvailabilityBPS {
		reasons = append(reasons, "training_coverage_below_95_percent")
	}
	if check.HoldoutCoverageBPS < ProvisionalMinimumAvailabilityBPS {
		reasons = append(reasons, "holdout_coverage_below_95_percent")
	}
	return reasons
}

func hasDashboardPaperSetupReason(reasons []string) bool {
	for _, reason := range reasons {
		switch reason {
		case "training_coverage_below_95_percent", "holdout_coverage_below_95_percent",
			"no_qualified_training_candidate":
			return true
		}
	}
	return false
}

func validDashboardPaperReason(value string) bool {
	switch value {
	case "training_coverage_below_95_percent", "holdout_coverage_below_95_percent",
		"no_qualified_training_candidate",
		"training_completed_fewer_than_1_round_trips",
		"training_has_unmatched_filled_leg", "training_has_pending_decision",
		"training_has_failed_execution", "training_net_return_not_positive",
		"training_did_not_beat_holding", "training_drawdown_above_policy_limit",
		"holdout_completed_fewer_than_2_round_trips",
		"holdout_has_unmatched_filled_leg", "holdout_has_pending_decision",
		"holdout_has_failed_execution", "holdout_net_return_not_positive",
		"holdout_did_not_beat_holding", "holdout_drawdown_above_policy_limit",
		"stress_completed_fewer_than_2_round_trips",
		"stress_has_unmatched_filled_leg", "stress_has_pending_decision",
		"stress_has_failed_execution", "stress_net_return_not_positive",
		"stress_did_not_beat_holding", "stress_drawdown_above_policy_limit":
		return true
	default:
		return false
	}
}

func validDiagnosticFailure(value string) bool {
	if validFailure(value) || value == "missing_bucket" {
		return true
	}
	switch value {
	case "observation_deadline_rejected", "mint_evidence_rejected",
		"market_primary_rejected", "market_sources_rejected",
		"market_source_time_alignment_rejected", "market_source_price_disagreement_rejected",
		"quote_primary_rejected", "quote_peg_rejected",
		"native_primary_rejected", "native_sources_rejected",
		"native_source_time_alignment_rejected", "native_source_price_disagreement_rejected",
		"buy_quote_rejected", "sell_quote_rejected",
		"round_trip_rejected", "quote_price_rejected":
		return true
	default:
		return false
	}
}
