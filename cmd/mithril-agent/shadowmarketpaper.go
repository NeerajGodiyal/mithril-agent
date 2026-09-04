package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/marketadmission"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const (
	marketPaperCheckVersion       = uint32(1)
	marketPaperCheckTrainingHours = 4
	marketPaperCheckHoldoutHours  = 2
	// Two baseline legs equal the code-owned 50 bps p95 round-trip admission
	// ceiling; the stress lane doubles this per-leg spread.
	marketPaperCheckSpreadBPS     = uint16(25)
	marketPaperCheckCostModelRule = "code_owned_symmetric_spread_v1"
	marketPaperCheckStressRule    = "double_modelled_spread_v1"
)

type marketPaperCheckScore struct {
	FullRoundTrips      uint64 `json:"full_round_trips"`
	Sells               uint64 `json:"sells"`
	Buys                uint64 `json:"buys"`
	Refused             uint64 `json:"refused"`
	Missed              uint64 `json:"missed"`
	Pending             bool   `json:"pending"`
	OpeningEquityMicros uint64 `json:"opening_equity_micros"`
	ClosingEquityMicros uint64 `json:"closing_equity_micros"`
	NetReturnMicros     int64  `json:"net_return_micros"`
	VersusHoldMicros    int64  `json:"versus_hold_micros"`
	MaxDrawdownMicros   uint64 `json:"max_drawdown_micros"`
	MaxDrawdownBPS      uint16 `json:"max_drawdown_bps"`
}

// marketPaperCheckResult is deliberately incompatible with admission and
// selectable-candidate artifacts. It can report only research evidence.
type marketPaperCheckResult struct {
	Version                   uint32                 `json:"version"`
	Status                    string                 `json:"status"`
	Outcome                   string                 `json:"outcome"`
	PaperOnly                 bool                   `json:"paper_only"`
	Authorized                bool                   `json:"authorized"`
	Promotable                bool                   `json:"promotable"`
	Market                    string                 `json:"market"`
	InputSHA256               string                 `json:"input_sha256"`
	ProvisionalEvidenceSHA256 string                 `json:"provisional_evidence_sha256"`
	PolicySHA256              string                 `json:"policy_sha256"`
	CandidatePolicySHA256     string                 `json:"candidate_policy_sha256,omitempty"`
	Journal                   journal.DurablePrefix  `json:"journal"`
	From                      time.Time              `json:"from"`
	Through                   time.Time              `json:"through"`
	TrainingThrough           time.Time              `json:"training_through"`
	TrainingCoverageBPS       uint16                 `json:"training_coverage_bps"`
	HoldoutCoverageBPS        uint16                 `json:"holdout_coverage_bps"`
	ModelledSpreadBPS         uint16                 `json:"modelled_spread_bps_each_way"`
	StressModelledSpreadBPS   uint16                 `json:"stress_modelled_spread_bps_each_way"`
	CostModelRule             string                 `json:"cost_model_rule"`
	StressRule                string                 `json:"stress_rule"`
	CandidatesEvaluated       uint64                 `json:"candidates_evaluated"`
	Candidate                 *shadowSearchCandidate `json:"candidate,omitempty"`
	Training                  *marketPaperCheckScore `json:"training,omitempty"`
	Holdout                   *marketPaperCheckScore `json:"holdout,omitempty"`
	Stress                    *marketPaperCheckScore `json:"stress,omitempty"`
	Reasons                   []string               `json:"reasons"`
}

func runShadowMarketPaperCheck(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow market paper-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "provisional candidate-market policy")
	artifactPath := flags.String("provisional-artifact", "", "six-hour paper checkpoint")
	journalPath := flags.String("journal", "", "checkpoint evidence journal")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowMarketUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *policyPath == "" || *artifactPath == "" || *journalPath == "" {
		return errors.New("shadow market paper-check requires --policy, --provisional-artifact, and --journal")
	}
	for _, item := range []struct{ name, path string }{
		{"--policy", *policyPath}, {"--provisional-artifact", *artifactPath}, {"--journal", *journalPath},
	} {
		if err := validateMarketAdmissionPath(item.path, item.name); err != nil {
			return err
		}
	}
	artifact, err := loadProvisionalMarketAdmission(*artifactPath, *journalPath, time.Now())
	if err != nil {
		return err
	}
	policy, err := loadActiveShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	if !provisionalPolicyMatchesArtifact(policy, artifact) {
		return errors.New("paper-check policy does not match the provisional market evidence")
	}
	points, err := artifact.ReplayPoints(*journalPath)
	if err != nil {
		return err
	}
	result, err := checkProvisionalMarketPaper(policy, artifact, points)
	if err != nil {
		return err
	}
	return writeShadowMarketJSON(output, result)
}

func checkProvisionalMarketPaper(
	policy shadow.Policy,
	artifact marketadmission.ProvisionalArtifact,
	points []marketadmission.ProvisionalReplayPoint,
) (marketPaperCheckResult, error) {
	policySHA256, err := policy.Fingerprint()
	if err != nil {
		return marketPaperCheckResult{}, err
	}
	if !provisionalPolicyMatchesArtifact(policy, artifact) || len(points) != int(artifact.ExpectedBuckets) {
		return marketPaperCheckResult{}, errors.New("paper-check inputs do not match the provisional evidence")
	}
	if policy.Adaptive == nil || policy.TickSeconds != uint64(artifact.Thresholds.CadenceSeconds) {
		return marketPaperCheckResult{}, errors.New("paper-check policy cadence must match the evidence cadence")
	}
	trainingThrough := artifact.From.Add(marketPaperCheckTrainingHours * time.Hour)
	if trainingThrough.Add(marketPaperCheckHoldoutHours*time.Hour) != artifact.Through {
		return marketPaperCheckResult{}, errors.New("paper-check window split is invalid")
	}
	spreadBPS := marketPaperCheckSpreadBPS
	stress := spreadBPS * 2
	result := marketPaperCheckResult{
		Version: marketPaperCheckVersion, Status: "research_only", Outcome: "candidate_rejected",
		PaperOnly: true, Market: artifact.Candidate.Market,
		ProvisionalEvidenceSHA256: artifact.ContentSHA256, PolicySHA256: policySHA256,
		Journal: artifact.Journal, From: artifact.From, Through: artifact.Through,
		TrainingThrough:   trainingThrough,
		ModelledSpreadBPS: spreadBPS, StressModelledSpreadBPS: stress,
		CostModelRule: marketPaperCheckCostModelRule,
		StressRule:    marketPaperCheckStressRule, Reasons: []string{},
	}
	result.InputSHA256, err = marketPaperCheckInputSHA256(result)
	if err != nil {
		return marketPaperCheckResult{}, err
	}

	trainingPoints, holdoutPoints := splitMarketPaperPoints(points, trainingThrough)
	training, err := provisionalMarketTicks(policy, trainingPoints)
	if err != nil {
		return marketPaperCheckResult{}, err
	}
	holdout, err := provisionalMarketTicks(policy, holdoutPoints)
	if err != nil {
		return marketPaperCheckResult{}, err
	}
	result.TrainingCoverageBPS = marketPaperCoverageBPS(training)
	result.HoldoutCoverageBPS = marketPaperCoverageBPS(holdout)
	if result.TrainingCoverageBPS < marketadmission.ProvisionalMinimumAvailabilityBPS ||
		result.HoldoutCoverageBPS < marketadmission.ProvisionalMinimumAvailabilityBPS {
		result.Outcome = "insufficient_evidence"
		if result.TrainingCoverageBPS < marketadmission.ProvisionalMinimumAvailabilityBPS {
			result.Reasons = append(result.Reasons, "training_coverage_below_95_percent")
		}
		if result.HoldoutCoverageBPS < marketadmission.ProvisionalMinimumAvailabilityBPS {
			result.Reasons = append(result.Reasons, "holdout_coverage_below_95_percent")
		}
		return result, nil
	}
	var searchedHoldout scoredMarketPaperCandidate
	search, err := searchShadowCandidateScored(
		policy, observedPrices(training), observedPrices(holdout), uint64(spreadBPS), nil,
		func(candidate shadow.Policy) (shadowSearchScore, error) {
			score, err := scoreMarketPaperCandidate(candidate, training, uint64(spreadBPS))
			if err == nil && len(marketPaperScoreReasons(
				"training", score.Paper, 1, policy.Adaptive.MaxDrawdownBPS,
			)) != 0 {
				score.Search.FullRoundTrips = 0
			}
			return score.Search, err
		},
		func(candidate shadow.Policy) (shadowSearchScore, error) {
			score, err := scoreMarketPaperCandidate(candidate, holdout, uint64(spreadBPS))
			searchedHoldout = score
			return score.Search, err
		},
	)
	if errors.Is(err, errNoAdaptiveTrainingRoundTrip) {
		result.Outcome = "no_training_candidate"
		result.Reasons = append(result.Reasons, "no_qualified_training_candidate")
		return result, nil
	}
	if err != nil {
		return marketPaperCheckResult{}, err
	}
	candidate, err := shadowSearchCandidatePolicy(policy, search.Candidate)
	if err != nil {
		return marketPaperCheckResult{}, err
	}
	trainingScore, err := scoreMarketPaperCandidate(candidate, training, uint64(spreadBPS))
	if err != nil {
		return marketPaperCheckResult{}, err
	}
	holdoutScore := searchedHoldout
	stressScore, err := scoreMarketPaperCandidate(candidate, holdout, uint64(stress))
	if err != nil {
		return marketPaperCheckResult{}, err
	}
	result.CandidatesEvaluated = search.CandidatesEvaluated
	result.CandidatePolicySHA256, err = candidate.Fingerprint()
	if err != nil {
		return marketPaperCheckResult{}, err
	}
	chosen := search.Candidate
	result.Candidate = &chosen
	result.Training = &trainingScore.Paper
	result.Holdout = &holdoutScore.Paper
	result.Stress = &stressScore.Paper
	for _, check := range []struct {
		label             string
		score             marketPaperCheckScore
		minimumRoundTrips uint64
	}{
		{"training", *result.Training, 1},
		{"holdout", *result.Holdout, shadowInitialRoundTrips},
		{"stress", *result.Stress, shadowInitialRoundTrips},
	} {
		result.Reasons = append(result.Reasons, marketPaperScoreReasons(
			check.label, check.score, check.minimumRoundTrips, policy.Adaptive.MaxDrawdownBPS,
		)...)
	}
	if len(result.Reasons) == 0 {
		result.Outcome = "candidate_ready_for_more_paper_testing"
	}
	return result, nil
}

type scoredMarketPaperCandidate struct {
	Search shadowSearchScore
	Paper  marketPaperCheckScore
}

func scoreMarketPaperCandidate(
	policy shadow.Policy, ticks []shadow.Tick, spreadBPS uint64,
) (scoredMarketPaperCandidate, error) {
	replay, err := shadow.ReplayRoundTripTicksWithLiquidationMarks(
		policy, ticks, modelledPool(policy, spreadBPS, policy.SlippageBPS),
	)
	if err != nil {
		return scoredMarketPaperCandidate{}, err
	}
	score, err := scoreShadowRoundTripResult(replay)
	if err != nil {
		return scoredMarketPaperCandidate{}, err
	}
	score.MaxDrawdownMicros = replay.LiquidationMaxDrawdownMicros
	closing, err := replay.Ledger.EquityMicros(replay.ClosingPrice)
	if err != nil {
		return scoredMarketPaperCandidate{}, err
	}
	opening := replay.Ledger.OpeningEquityMicros
	if opening > math.MaxInt64 || closing > math.MaxInt64 {
		return scoredMarketPaperCandidate{}, errors.New("paper-check equity is too large to compare")
	}
	return scoredMarketPaperCandidate{
		Search: score,
		Paper: marketPaperCheckScore{
			FullRoundTrips: score.FullRoundTrips,
			Sells:          replay.Counts.Sells, Buys: replay.Counts.Buys,
			Refused: replay.Counts.Refused, Missed: replay.Counts.Missed,
			Pending:             replay.Counts.Pending,
			OpeningEquityMicros: opening, ClosingEquityMicros: closing,
			NetReturnMicros:   int64(closing) - int64(opening),
			VersusHoldMicros:  score.VersusHoldMicros,
			MaxDrawdownMicros: score.MaxDrawdownMicros,
			MaxDrawdownBPS:    replay.LiquidationMaxDrawdownBPS,
		},
	}, nil
}

func provisionalMarketTicks(
	policy shadow.Policy, points []marketadmission.ProvisionalReplayPoint,
) ([]shadow.Tick, error) {
	ticks := make([]shadow.Tick, 0, len(points))
	var previousPrimary, previousSecondary time.Time
	for _, point := range points {
		if !point.Available || !shadow.AdaptiveSampleAdvances(
			previousPrimary, previousSecondary,
			point.MarketPrimary.PublishedAt.UTC(), point.MarketSecondary.PublishedAt.UTC(),
		) {
			ticks = append(ticks, shadow.Tick{At: point.At, Event: shadow.EventUnobservable})
			continue
		}
		nativeEvidence, err := pricetrigger.Evaluate(
			*policy.NativeFeePrice, point.NativePrimary, point.NativeSecondary, point.At,
		)
		if err != nil {
			return nil, errors.New("paper-check native fee-price evidence is invalid")
		}
		primary, secondary := point.MarketPrimary, point.MarketSecondary
		nativePrimary, nativeSecondary := point.NativePrimary, point.NativeSecondary
		ticks = append(ticks, shadow.Tick{
			At: point.At, Event: shadow.EventWaiting, PriceMicros: primary.PriceMicros,
			PrimaryPrice: &primary, SecondaryPrice: &secondary,
			NativeFeePriceMicros: nativeEvidence.ConservativePrice,
			NativeFeePrimary:     &nativePrimary, NativeFeeSecondary: &nativeSecondary,
		})
		previousPrimary = point.MarketPrimary.PublishedAt.UTC()
		previousSecondary = point.MarketSecondary.PublishedAt.UTC()
	}
	return ticks, nil
}

func splitMarketPaperPoints(
	points []marketadmission.ProvisionalReplayPoint, trainingThrough time.Time,
) (training, holdout []marketadmission.ProvisionalReplayPoint) {
	for _, point := range points {
		if point.Bucket.Before(trainingThrough) {
			training = append(training, point)
		} else {
			holdout = append(holdout, point)
		}
	}
	return training, holdout
}

func marketPaperCoverageBPS(ticks []shadow.Tick) uint16 {
	available := uint64(0)
	for _, tick := range ticks {
		if tick.PriceMicros != 0 {
			available++
		}
	}
	if len(ticks) == 0 {
		return 0
	}
	return uint16(available * 10_000 / uint64(len(ticks)))
}

func marketPaperScoreReasons(
	label string, score marketPaperCheckScore, minimumRoundTrips uint64, maxDrawdownBPS uint16,
) []string {
	var reasons []string
	if score.FullRoundTrips < minimumRoundTrips {
		reasons = append(reasons, fmt.Sprintf("%s_completed_fewer_than_%d_round_trips", label, minimumRoundTrips))
	}
	if score.Sells != score.Buys {
		reasons = append(reasons, label+"_has_unmatched_filled_leg")
	}
	if score.Pending {
		reasons = append(reasons, label+"_has_pending_decision")
	}
	if score.Refused != 0 || score.Missed != 0 {
		reasons = append(reasons, label+"_has_failed_execution")
	}
	if score.NetReturnMicros <= 0 {
		reasons = append(reasons, label+"_net_return_not_positive")
	}
	if score.VersusHoldMicros <= 0 {
		reasons = append(reasons, label+"_did_not_beat_holding")
	}
	if score.MaxDrawdownBPS > maxDrawdownBPS {
		reasons = append(reasons, label+"_drawdown_above_policy_limit")
	}
	return reasons
}

func marketPaperCheckInputSHA256(result marketPaperCheckResult) (string, error) {
	encoded, err := json.Marshal(struct {
		Version                   uint32                `json:"version"`
		CostModelRule             string                `json:"cost_model_rule"`
		StressRule                string                `json:"stress_rule"`
		ProvisionalEvidenceSHA256 string                `json:"provisional_evidence_sha256"`
		PolicySHA256              string                `json:"policy_sha256"`
		Journal                   journal.DurablePrefix `json:"journal"`
		From                      time.Time             `json:"from"`
		Through                   time.Time             `json:"through"`
		TrainingThrough           time.Time             `json:"training_through"`
		ModelledSpreadBPS         uint16                `json:"modelled_spread_bps_each_way"`
		StressModelledSpreadBPS   uint16                `json:"stress_modelled_spread_bps_each_way"`
	}{
		Version: result.Version, CostModelRule: result.CostModelRule,
		StressRule:                result.StressRule,
		ProvisionalEvidenceSHA256: result.ProvisionalEvidenceSHA256,
		PolicySHA256:              result.PolicySHA256, Journal: result.Journal,
		From: result.From, Through: result.Through, TrainingThrough: result.TrainingThrough,
		ModelledSpreadBPS:       result.ModelledSpreadBPS,
		StressModelledSpreadBPS: result.StressModelledSpreadBPS,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
