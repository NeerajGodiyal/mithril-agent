package paperdashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/statuswire"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/marketadmission"
)

const (
	marketAdmissionProjectionVersion  = uint32(1)
	maxMarketAdmissionProjectionBytes = 64 << 10
	marketAdmissionFreshness          = 3 * time.Minute
)

var marketAdmissionCredentials = [...]struct {
	market     string
	credential string
}{
	{marketadmission.MarketWIFUSDC, "market-wif-status"},
	{marketadmission.MarketJTOUSDC, "market-jto-status"},
	{marketadmission.MarketPYTHUSDC, "market-pyth-status"},
}

// MarketResearch is a read-only collection status. It is deliberately kept
// separate from active paper markets and cannot affect account totals.
type MarketResearch struct {
	Market                   string            `json:"market"`
	UpdatedAt                time.Time         `json:"updated_at"`
	Fresh                    bool              `json:"fresh"`
	WindowHours              uint16            `json:"window_hours"`
	ExpectedBuckets          uint64            `json:"expected_buckets"`
	ObservedBuckets          uint64            `json:"observed_buckets"`
	AvailableBuckets         uint64            `json:"available_buckets"`
	AvailabilityBPS          uint16            `json:"availability_bps"`
	MedianRouteCostBPS       uint16            `json:"median_route_cost_bps,omitempty"`
	P95RouteCostBPS          uint16            `json:"p95_route_cost_bps,omitempty"`
	MedianQuoteLatencyMillis uint32            `json:"median_quote_latency_millis,omitempty"`
	P95QuoteLatencyMillis    uint32            `json:"p95_quote_latency_millis,omitempty"`
	FailureCounts            map[string]uint64 `json:"failure_counts"`
	ReadyForPaperCheck       bool              `json:"ready_for_paper_check"`
	PaperCheck               *MarketPaperCheck `json:"paper_check,omitempty"`
	PaperCheckCurrent        bool              `json:"paper_check_current,omitempty"`
}

// MarketPaperCheck is the public, precision-safe view of one short replay.
type MarketPaperCheck struct {
	Version                          uint32                                           `json:"version,omitempty"`
	CheckedAt                        time.Time                                        `json:"checked_at"`
	Through                          time.Time                                        `json:"through"`
	Outcome                          string                                           `json:"outcome"`
	TrainingCoverageBPS              uint16                                           `json:"training_coverage_bps"`
	HoldoutCoverageBPS               uint16                                           `json:"holdout_coverage_bps"`
	HoldoutAfterCostNetReturnMicros  int64                                            `json:"holdout_after_cost_net_return_micros,string"`
	HoldoutAfterCostVersusHoldMicros int64                                            `json:"holdout_after_cost_versus_hold_micros,string"`
	StressAfterCostNetReturnMicros   int64                                            `json:"stress_after_cost_net_return_micros,string"`
	StressAfterCostVersusHoldMicros  int64                                            `json:"stress_after_cost_versus_hold_micros,string"`
	CandidatesEvaluated              uint64                                           `json:"candidates_evaluated,omitempty"`
	TrainingRejections               marketadmission.DashboardPaperTrainingRejections `json:"training_rejections,omitzero"`
	Reasons                          []string                                         `json:"reasons"`
}

type marketAdmissionProjection struct {
	Version       uint32                            `json:"version"`
	PaperOnly     bool                              `json:"paper_only"`
	AdvisoryOnly  bool                              `json:"advisory_only"`
	Authorized    bool                              `json:"authorized"`
	Promotable    bool                              `json:"promotable"`
	RecordedAt    time.Time                         `json:"recorded_at"`
	Markets       []marketadmission.DashboardStatus `json:"markets"`
	ContentSHA256 string                            `json:"content_sha256"`
}

// RecordMarketAdmission copies exactly the three fixed, validated collector
// credentials into one bounded dashboard-owned projection.
func RecordMarketAdmission(path, credentialDirectory string, now time.Time) error {
	if !cleanAbsolutePath(path) || now.IsZero() {
		return errors.New("market admission projection path and time are required")
	}
	projection := marketAdmissionProjection{
		Version: marketAdmissionProjectionVersion, PaperOnly: true, AdvisoryOnly: true,
		RecordedAt: now.UTC(), Markets: make([]marketadmission.DashboardStatus, 0, len(marketAdmissionCredentials)),
	}
	for _, expected := range marketAdmissionCredentials {
		reader, err := statuswire.NewCredentialReader(
			credentialDirectory, expected.credential, marketadmission.MaxDashboardStatusBytes,
			func(raw []byte) error {
				_, err := marketadmission.LoadDashboardStatus(raw)
				return err
			},
		)
		if err != nil {
			return err
		}
		raw, err := reader.ReadJSON()
		if err != nil {
			return err
		}
		status, err := marketadmission.LoadDashboardStatus(raw)
		if err != nil || status.Market != expected.market || status.UpdatedAt.After(now.UTC().Add(2*time.Minute)) {
			return errors.New("market admission credential does not match its fixed market")
		}
		projection.Markets = append(projection.Markets, status)
	}
	digest, err := marketAdmissionProjectionFingerprint(projection)
	if err != nil {
		return err
	}
	projection.ContentSHA256 = digest
	encoded, err := json.Marshal(projection)
	if err != nil {
		return err
	}
	return securefile.ReplacePrivate(path, append(encoded, '\n'), maxMarketAdmissionProjectionBytes)
}

func (s *Server) EnableMarketAdmission(path string) error {
	if !cleanAbsolutePath(path) {
		return errors.New("market admission status path must be a clean absolute path")
	}
	s.marketAdmissionPath = path
	return nil
}

func readMarketAdmission(path string, now time.Time) ([]MarketResearch, error) {
	if !cleanAbsolutePath(path) || now.IsZero() {
		return nil, errors.New("market admission status path and time are required")
	}
	raw, err := securefile.ReadPrivate(path, maxMarketAdmissionProjectionBytes)
	if err != nil {
		return nil, err
	}
	var projection marketAdmissionProjection
	if strictjson.Decode(raw, &projection) != nil || validateMarketAdmissionProjection(projection) != nil ||
		projection.RecordedAt.After(now.UTC().Add(2*time.Minute)) {
		return nil, errors.New("market admission projection is invalid")
	}
	result := make([]MarketResearch, 0, len(projection.Markets))
	for _, status := range projection.Markets {
		diagnostic := status.Diagnostic
		fresh := !status.UpdatedAt.After(now.UTC().Add(2*time.Minute)) &&
			!status.UpdatedAt.Before(now.UTC().Add(-marketAdmissionFreshness))
		failures := make(map[string]uint64, len(diagnostic.FailureCounts))
		for reason, count := range diagnostic.FailureCounts {
			failures[reason] = count
		}
		var check *MarketPaperCheck
		if status.PaperCheck != nil {
			check = &MarketPaperCheck{
				Version:   status.PaperCheck.Version,
				CheckedAt: status.PaperCheck.CheckedAt, Through: status.PaperCheck.Through,
				Outcome:                          status.PaperCheck.Outcome,
				TrainingCoverageBPS:              status.PaperCheck.TrainingCoverageBPS,
				HoldoutCoverageBPS:               status.PaperCheck.HoldoutCoverageBPS,
				HoldoutAfterCostNetReturnMicros:  status.PaperCheck.HoldoutAfterCostNetReturnMicros,
				HoldoutAfterCostVersusHoldMicros: status.PaperCheck.HoldoutAfterCostVersusHoldMicros,
				StressAfterCostNetReturnMicros:   status.PaperCheck.StressAfterCostNetReturnMicros,
				StressAfterCostVersusHoldMicros:  status.PaperCheck.StressAfterCostVersusHoldMicros,
				CandidatesEvaluated:              status.PaperCheck.CandidatesEvaluated,
				TrainingRejections:               status.PaperCheck.TrainingRejections,
				Reasons:                          append([]string(nil), status.PaperCheck.Reasons...),
			}
		}
		result = append(result, MarketResearch{
			Market: status.Market, UpdatedAt: status.UpdatedAt, WindowHours: status.WindowHours,
			Fresh:           fresh,
			ExpectedBuckets: diagnostic.ExpectedBuckets, ObservedBuckets: diagnostic.ObservedBuckets,
			AvailableBuckets: diagnostic.AvailableBuckets, AvailabilityBPS: diagnostic.AvailabilityBPS,
			MedianRouteCostBPS: diagnostic.MedianRouteCostBPS, P95RouteCostBPS: diagnostic.P95RouteCostBPS,
			MedianQuoteLatencyMillis: diagnostic.MedianQuoteLatencyMillis,
			P95QuoteLatencyMillis:    diagnostic.P95QuoteLatencyMillis,
			FailureCounts:            failures,
			ReadyForPaperCheck:       fresh && diagnostic.ReadyForProvisionalPaperCheck(),
			PaperCheck:               check,
			PaperCheckCurrent:        status.PaperCheck != nil && status.PaperCheck.Current(now),
		})
	}
	return result, nil
}

func validateMarketAdmissionProjection(projection marketAdmissionProjection) error {
	if projection.Version != marketAdmissionProjectionVersion || !projection.PaperOnly ||
		!projection.AdvisoryOnly || projection.Authorized || projection.Promotable ||
		projection.RecordedAt.IsZero() || projection.RecordedAt.Location() != time.UTC ||
		len(projection.Markets) != len(marketAdmissionCredentials) || projection.ContentSHA256 == "" {
		return errors.New("market admission projection envelope is invalid")
	}
	for index, expected := range marketAdmissionCredentials {
		status := projection.Markets[index]
		if status.Market != expected.market || status.Validate() != nil {
			return errors.New("market admission projection markets are invalid")
		}
	}
	want, err := marketAdmissionProjectionFingerprint(projection)
	if err != nil || want != projection.ContentSHA256 {
		return errors.New("market admission projection digest does not match")
	}
	return nil
}

func marketAdmissionProjectionFingerprint(projection marketAdmissionProjection) (string, error) {
	projection.ContentSHA256 = ""
	encoded, err := json.Marshal(projection)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
