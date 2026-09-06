package main

import (
	"errors"
	"io"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/marketadmission"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

type marketPaperCostLane struct {
	Partition      string                 `json:"partition"`
	From           time.Time              `json:"from"`
	Through        time.Time              `json:"through"`
	SpreadBPS      uint16                 `json:"modelled_spread_bps_each_way"`
	Baseline       shadow.RoundTripResult `json:"baseline"`
	ObservedNative shadow.RoundTripResult `json:"observed_native_cost"`
}

type marketPaperCostComparison struct {
	Experiment                string                `json:"experiment"`
	Status                    string                `json:"status"`
	Market                    string                `json:"market"`
	PolicySHA256              string                `json:"policy_sha256"`
	ProvisionalEvidenceSHA256 string                `json:"provisional_evidence_sha256"`
	Journal                   journal.DurablePrefix `json:"journal"`
	AssumedFeeLamports        uint64                `json:"assumed_fee_lamports,string"`
	From                      time.Time             `json:"from"`
	TrainingThrough           time.Time             `json:"training_through"`
	Through                   time.Time             `json:"through"`
	TrainingCoverageBPS       uint16                `json:"training_coverage_bps"`
	HoldoutCoverageBPS        uint16                `json:"holdout_coverage_bps"`
	AdmissionEvidence         bool                  `json:"admission_evidence"`
	TradingEnabled            bool                  `json:"trading_enabled"`
	Limitation                string                `json:"limitation"`
	Lanes                     []marketPaperCostLane `json:"lanes"`
}

// writeMarketPaperCostComparison scores the supplied policy without parameter
// search. It cannot write a candidate or qualify a market.
func writeMarketPaperCostComparison(output io.Writer, policy shadow.Policy,
	artifact marketadmission.ProvisionalArtifact, points []marketadmission.ProvisionalReplayPoint,
) error {
	if !provisionalPolicyMatchesArtifact(policy, artifact) || policy.Adaptive == nil ||
		len(points) != int(artifact.ExpectedBuckets) || policy.TickSeconds != uint64(artifact.Thresholds.CadenceSeconds) {
		return errors.New("cost comparison inputs do not match provisional evidence")
	}
	trainingThrough := artifact.From.Add(marketPaperCheckTrainingMinutes * time.Minute)
	if !trainingThrough.Add(marketPaperCheckHoldoutMinutes * time.Minute).Equal(artifact.Through) {
		return errors.New("cost comparison window split is invalid")
	}
	policyHash, err := policy.Fingerprint()
	if err != nil {
		return err
	}
	trainingPoints, holdoutPoints := splitMarketPaperPoints(points, trainingThrough)
	training, primaryAt, secondaryAt, err := provisionalMarketTicksFrom(policy, trainingPoints, time.Time{}, time.Time{})
	if err != nil {
		return err
	}
	holdout, _, _, err := provisionalMarketTicksFrom(policy, holdoutPoints, primaryAt, secondaryAt)
	if err != nil {
		return err
	}
	result := marketPaperCostComparison{
		Experiment: "provisional-observed-native-cost-v1", Status: "historical_supplied_policy_comparison",
		Market: artifact.Candidate.Market, PolicySHA256: policyHash,
		ProvisionalEvidenceSHA256: artifact.ContentSHA256, Journal: artifact.Journal,
		AssumedFeeLamports: policy.FeeLamports, From: artifact.From,
		TrainingThrough: trainingThrough, Through: artifact.Through,
		TrainingCoverageBPS: marketPaperCoverageBPS(training), HoldoutCoverageBPS: marketPaperCoverageBPS(holdout),
		Limitation: "Supplied policy parameters, not the training-search winner. Historical 80/40-minute partitions, not new unseen validation. Recorded SOL/USD changes fee valuation; assumed lamports stay unchanged. Route spreads are modeled, not historical fills. Results are not portfolio income or admission evidence.",
	}
	if result.TrainingCoverageBPS < marketadmission.ProvisionalMinimumAvailabilityBPS ||
		result.HoldoutCoverageBPS < marketadmission.ProvisionalMinimumAvailabilityBPS {
		result.Status = "insufficient_evidence"
		return writeShadowMarketJSON(output, result)
	}
	for _, partition := range []struct {
		name          string
		from, through time.Time
		ticks         []shadow.Tick
	}{
		{"training", artifact.From, trainingThrough, training},
		{"holdout", trainingThrough, artifact.Through, holdout},
	} {
		for _, spread := range []uint16{marketPaperCheckSpreadBPS, marketPaperCheckSpreadBPS * 2} {
			baseline, observed, err := shadow.ReplayObservedNativeObservationComparison(
				policy, partition.ticks, modelledPool(policy, uint64(spread), policy.SlippageBPS),
			)
			if err != nil {
				return err
			}
			result.Lanes = append(result.Lanes, marketPaperCostLane{
				Partition: partition.name, From: partition.from, Through: partition.through,
				SpreadBPS: spread, Baseline: baseline, ObservedNative: observed,
			})
		}
	}
	return writeShadowMarketJSON(output, result)
}
