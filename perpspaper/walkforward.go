package perpspaper

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

const WalkForwardVersion uint32 = 1

// WalkForwardTape is one separate, non-overlapping paper episode. Capital is
// reset between episodes; the frames are never concatenated into one account.
type WalkForwardTape struct {
	ContentSHA256 string      `json:"content_sha256"`
	Frames        []TapeFrame `json:"-"`
}

type WalkForwardTapeEvidence struct {
	ContentSHA256     string `json:"content_sha256"`
	ReplayInputSHA256 string `json:"replay_input_sha256"`
	Frames            uint64 `json:"frames"`
	FirstTime         int64  `json:"first_time"`
	LastTime          int64  `json:"last_time"`
}

type WalkForwardFold struct {
	ContentSHA256    string           `json:"content_sha256"`
	Eligible         bool             `json:"eligible"`
	IneligibleReason string           `json:"ineligible_reason,omitempty"`
	Score            *TournamentScore `json:"score,omitempty"`
}

type WalkForwardTrial struct {
	QualificationKey
	Eligible         bool              `json:"eligible"`
	IneligibleReason string            `json:"ineligible_reason,omitempty"`
	Folds            []WalkForwardFold `json:"folds"`
	Aggregate        *TournamentScore  `json:"aggregate,omitempty"`
}

// ExecutionDelayAdvisory is a standalone paper-only stress result. It is never
// part of walk-forward qualification, candidate selection, or paper execution.
type ExecutionDelayAdvisory struct {
	Version                  uint32                `json:"version"`
	Status                   string                `json:"status"`
	PaperOnly                bool                  `json:"paper_only"`
	Authorized               bool                  `json:"authorized"`
	Promotable               bool                  `json:"promotable"`
	Rule                     string                `json:"rule"`
	InputSHA256              string                `json:"input_sha256"`
	ResultSHA256             string                `json:"result_sha256"`
	QualificationInputSHA256 string                `json:"qualification_input_sha256"`
	FinalTapeSHA256          string                `json:"final_tape_sha256"`
	Evidence                 QualificationEvidence `json:"evidence"`
}

// Validate proves the advisory is bound to this exact qualification context.
func (advisory ExecutionDelayAdvisory) Validate(qualificationInputSHA256, finalTapeSHA256 string, leader QualificationKey) error {
	digest, err := executionDelayInputSHA256(qualificationInputSHA256, finalTapeSHA256, leader)
	resultDigest, resultErr := executionDelayResultSHA256(advisory.Evidence)
	if err != nil || resultErr != nil || advisory.Version != 1 || advisory.Status != "advisory_only" ||
		!advisory.PaperOnly || advisory.Authorized || advisory.Promotable ||
		!validWalkForwardSHA256(qualificationInputSHA256) || !validWalkForwardSHA256(finalTapeSHA256) ||
		!validExecutionDelayLeader(leader) ||
		advisory.Rule != QualificationExecutionDelay || advisory.InputSHA256 != digest ||
		advisory.ResultSHA256 != resultDigest ||
		advisory.QualificationInputSHA256 != qualificationInputSHA256 ||
		advisory.FinalTapeSHA256 != finalTapeSHA256 || advisory.Evidence.QualificationKey != leader ||
		advisory.Evidence.StressRule != "" {
		return errors.New("execution-delay advisory is invalid")
	}
	return nil
}

func validExecutionDelayLeader(leader QualificationKey) bool {
	if _, _, _, err := armPolicy(leader.RiskArm); err != nil {
		return false
	}
	switch leader.Strategy {
	case StrategyMomentum, StrategyMeanReversion, StrategyBreakout, StrategyRegime:
		return true
	default:
		return false
	}
}

// EvaluateOneFrameExecutionDelay evaluates a frozen walk-forward leader with a
// one-frame execution delay without changing the authoritative qualification.
func EvaluateOneFrameExecutionDelay(
	config QualificationConfig,
	frames []TapeFrame,
	qualificationInputSHA256, finalTapeSHA256 string,
	leader QualificationKey,
) (ExecutionDelayAdvisory, error) {
	if !validWalkForwardSHA256(qualificationInputSHA256) || !validWalkForwardSHA256(finalTapeSHA256) {
		return ExecutionDelayAdvisory{}, errors.New("execution-delay advisory lineage is invalid")
	}
	result, err := evaluateTournamentStrategyOneFrameDelay(config.replayConfig(leader.RiskArm), frames, leader.Strategy)
	if err != nil {
		return ExecutionDelayAdvisory{}, err
	}
	advisory := ExecutionDelayAdvisory{
		Version: 1, Status: "advisory_only", PaperOnly: true,
		Rule: QualificationExecutionDelay, QualificationInputSHA256: qualificationInputSHA256,
		FinalTapeSHA256: finalTapeSHA256,
		Evidence:        *qualificationEvidence(leader, "", result),
	}
	advisory.InputSHA256, err = executionDelayInputSHA256(qualificationInputSHA256, finalTapeSHA256, leader)
	if err != nil {
		return ExecutionDelayAdvisory{}, err
	}
	advisory.ResultSHA256, err = executionDelayResultSHA256(advisory.Evidence)
	if err != nil {
		return ExecutionDelayAdvisory{}, err
	}
	return advisory, advisory.Validate(qualificationInputSHA256, finalTapeSHA256, leader)
}

// WalkForwardQualification selects only on earlier tapes and then evaluates
// the fixed leader on the final held-out tape. It is research evidence
// and can only make a candidate eligible for another bounded paper experiment.
type WalkForwardQualification struct {
	Version                    uint32                    `json:"version"`
	Status                     string                    `json:"status"`
	Outcome                    string                    `json:"outcome"`
	PaperOnly                  bool                      `json:"paper_only"`
	Authorized                 bool                      `json:"authorized"`
	Promotable                 bool                      `json:"promotable"`
	EligibleForPaperExperiment bool                      `json:"eligible_for_paper_experiment"`
	InputSHA256                string                    `json:"input_sha256"`
	Config                     QualificationConfig       `json:"config"`
	Tapes                      []WalkForwardTapeEvidence `json:"tapes"`
	TrainingTrials             uint64                    `json:"training_trials"`
	HoldoutPlansCompared       uint64                    `json:"holdout_plans_compared"`
	HoldoutCompletedTrades     uint64                    `json:"holdout_completed_trades"`
	StatisticalConfidence      string                    `json:"statistical_confidence"`
	Training                   []WalkForwardTrial        `json:"training"`
	TrainingLeader             *QualificationKey         `json:"training_leader,omitempty"`
	Forward                    *QualificationEvidence    `json:"forward,omitempty"`
	Stress                     *QualificationEvidence    `json:"stress,omitempty"`
	Candidate                  *QualificationKey         `json:"candidate,omitempty"`
	Reasons                    []string                  `json:"reasons"`
}

// QualifyWalkForward ranks fixed strategy/risk pairs without using the final
// tape, then evaluates the selected leader on that held-out tape.
func QualifyWalkForward(config QualificationConfig, tapes []WalkForwardTape) (WalkForwardQualification, error) {
	if len(tapes) < 2 {
		return WalkForwardQualification{}, errors.New("walk-forward qualification requires at least two tapes")
	}
	result := WalkForwardQualification{
		Version: WalkForwardVersion, Status: "research_only", Outcome: "insufficient_evidence",
		PaperOnly: true, Config: config, Tapes: []WalkForwardTapeEvidence{},
		StatisticalConfidence: QualificationConfidence,
		Training:              []WalkForwardTrial{}, Reasons: []string{},
	}
	seen := make(map[string]bool, len(tapes))
	var previousLast int64
	for index, tape := range tapes {
		if len(tape.Frames) == 0 {
			return WalkForwardQualification{}, fmt.Errorf("walk-forward tape %d is empty", index+1)
		}
		causal, err := tournamentCausalFrames(config.replayConfig(Balanced), tape.Frames)
		if err != nil {
			return WalkForwardQualification{}, fmt.Errorf("verify walk-forward tape %d: %w", index+1, err)
		}
		if _, err := replayTournamentStrategy(config.replayConfig(Balanced), causal, len(causal), StrategyMomentum); err != nil {
			return WalkForwardQualification{}, fmt.Errorf("verify walk-forward tape %d: %w", index+1, err)
		}
		if len(tape.ContentSHA256) != 64 {
			return WalkForwardQualification{}, fmt.Errorf("walk-forward tape %d content digest is invalid", index+1)
		}
		if _, err := hex.DecodeString(tape.ContentSHA256); err != nil || tape.ContentSHA256 != strings.ToLower(tape.ContentSHA256) {
			return WalkForwardQualification{}, fmt.Errorf("walk-forward tape %d content digest is invalid", index+1)
		}
		replayDigest, err := qualificationInputSHA256(config, tape.Frames)
		if err != nil {
			return WalkForwardQualification{}, err
		}
		if seen[tape.ContentSHA256] {
			return WalkForwardQualification{}, errors.New("walk-forward tapes must be distinct")
		}
		seen[tape.ContentSHA256] = true
		first, last := tape.Frames[0].Book.Time, tape.Frames[len(tape.Frames)-1].Book.Time
		if index > 0 && first <= previousLast {
			return WalkForwardQualification{}, errors.New("walk-forward tapes must be chronological and non-overlapping")
		}
		previousLast = last
		result.Tapes = append(result.Tapes, WalkForwardTapeEvidence{
			ContentSHA256: tape.ContentSHA256, ReplayInputSHA256: replayDigest,
			Frames: uint64(len(tape.Frames)), FirstTime: first, LastTime: last,
		})
		if len(tape.Frames) < QualificationMinimumFrames {
			result.Reasons = append(result.Reasons, fmt.Sprintf("tape_%d_collect_more_causal_frames", index+1))
		}
	}
	inputDigest, err := walkForwardInputSHA256(config, result.Tapes)
	if err != nil {
		return WalkForwardQualification{}, err
	}
	result.InputSHA256 = inputDigest
	if len(result.Reasons) != 0 {
		return result, nil
	}

	arms := [...]RiskArm{Conservative, Balanced, Experimental}
	strategies := [...]Strategy{StrategyMomentum, StrategyMeanReversion, StrategyBreakout, StrategyRegime}
	var leader QualificationTrial
	for _, arm := range arms {
		for _, strategy := range strategies {
			trial := WalkForwardTrial{QualificationKey: QualificationKey{RiskArm: arm, Strategy: strategy}, Eligible: true, Folds: []WalkForwardFold{}}
			var scores []TournamentScore
			for index, tape := range tapes[:len(tapes)-1] {
				foldResult, err := evaluateTournamentStrategy(config.replayConfig(arm), tape.Frames, strategy)
				if err != nil {
					return WalkForwardQualification{}, fmt.Errorf("training tape %d %s/%s: %w", index+1, arm, strategy, err)
				}
				fold := WalkForwardFold{ContentSHA256: result.Tapes[index].ContentSHA256, Eligible: foldResult.Eligible, IneligibleReason: foldResult.IneligibleReason, Score: foldResult.Score}
				trial.Folds = append(trial.Folds, fold)
				if !foldResult.Eligible || foldResult.Score == nil {
					trial.Eligible = false
					trial.IneligibleReason = foldResult.IneligibleReason
					if trial.IneligibleReason == "" {
						trial.IneligibleReason = "training_fold_ineligible"
					}
					continue
				}
				scores = append(scores, *foldResult.Score)
			}
			if trial.Eligible {
				aggregate, err := aggregateWalkForwardScores(config.StartingCollateralMicros, scores)
				if err != nil {
					return WalkForwardQualification{}, fmt.Errorf("aggregate %s/%s: %w", arm, strategy, err)
				}
				trial.Aggregate = &aggregate
			}
			result.Training = append(result.Training, trial)
			candidate := QualificationTrial{QualificationKey: trial.QualificationKey, Eligible: trial.Eligible, IneligibleReason: trial.IneligibleReason, Score: trial.Aggregate}
			if meaningfulQualificationScore(candidate.Eligible, candidate.Score) && (leader.Score == nil || betterQualificationTrial(candidate, leader)) {
				leader = candidate
			}
		}
	}
	result.TrainingTrials = uint64(len(result.Training))
	if leader.Score == nil {
		result.Outcome = "no_training_candidate"
		result.Reasons = append(result.Reasons, "no_profitable_completed_training_trade")
		return result, nil
	}
	leaderKey := leader.QualificationKey
	result.TrainingLeader = &leaderKey
	forwardTape := tapes[len(tapes)-1]
	forwardResult, err := evaluateTournamentStrategy(config.replayConfig(leader.RiskArm), forwardTape.Frames, leader.Strategy)
	if err != nil {
		return WalkForwardQualification{}, fmt.Errorf("forward %s/%s: %w", leader.RiskArm, leader.Strategy, err)
	}
	result.Forward = qualificationEvidence(leaderKey, "", forwardResult)
	result.HoldoutPlansCompared = 1
	if result.Forward.Score != nil {
		result.HoldoutCompletedTrades = result.Forward.Score.ClosedPositions
	}
	stressConfig := config.replayConfig(leader.RiskArm)
	entryFee, _, _ := armAccounting(leader.RiskArm)
	stressConfig.AdditionalFeeBPS = entryFee
	stressResult, err := evaluateTournamentStrategy(stressConfig, forwardTape.Frames, leader.Strategy)
	if err != nil {
		return WalkForwardQualification{}, fmt.Errorf("forward stress %s/%s: %w", leader.RiskArm, leader.Strategy, err)
	}
	result.Stress = qualificationEvidence(leaderKey, qualificationStressRule, stressResult)
	result.Reasons = append(result.Reasons, qualificationFailures("forward", *result.Forward, config.StartingCollateralMicros)...)
	result.Reasons = append(result.Reasons, qualificationFailures("stress", *result.Stress, config.StartingCollateralMicros)...)
	if len(result.Reasons) != 0 {
		result.Outcome = "candidate_rejected"
		return result, nil
	}
	result.Outcome = "candidate_ready_for_more_paper_testing"
	result.EligibleForPaperExperiment = true
	result.Candidate = &leaderKey
	return result, nil
}

func executionDelayInputSHA256(qualificationInputSHA256, finalTapeSHA256 string, leader QualificationKey) (string, error) {
	payload, err := json.Marshal(struct {
		Version                  uint32           `json:"version"`
		Rule                     string           `json:"rule"`
		QualificationInputSHA256 string           `json:"qualification_input_sha256"`
		FinalTapeSHA256          string           `json:"final_tape_sha256"`
		Leader                   QualificationKey `json:"leader"`
	}{1, QualificationExecutionDelay, qualificationInputSHA256, finalTapeSHA256, leader})
	if err != nil {
		return "", errors.New("encode execution-delay advisory input")
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func executionDelayResultSHA256(evidence QualificationEvidence) (string, error) {
	payload, err := json.Marshal(evidence)
	if err != nil {
		return "", errors.New("encode execution-delay advisory result")
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func validWalkForwardSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// BestCompletedTrainingAttempts returns the strongest completed aggregate
// training attempt for each risk arm. Unlike candidate qualification, it keeps
// losing attempts so operators can see why no candidate passed.
func BestCompletedTrainingAttempts(training []WalkForwardTrial) []QualificationTrial {
	arms := [...]RiskArm{Conservative, Balanced, Experimental}
	result := make([]QualificationTrial, 0, len(arms))
	for _, arm := range arms {
		var best QualificationTrial
		for _, trial := range training {
			if trial.RiskArm != arm || !trial.Eligible || trial.Aggregate == nil ||
				trial.Aggregate.FilledOrders == 0 || trial.Aggregate.ClosedPositions != trial.Aggregate.FilledOrders {
				continue
			}
			score := *trial.Aggregate
			candidate := QualificationTrial{QualificationKey: trial.QualificationKey, Eligible: true, Score: &score}
			if best.Score == nil || betterQualificationTrial(candidate, best) {
				best = candidate
			}
		}
		if best.Score != nil {
			result = append(result, best)
		}
	}
	return result
}

func aggregateWalkForwardScores(starting uint64, scores []TournamentScore) (TournamentScore, error) {
	if len(scores) == 0 || starting > math.MaxInt64 {
		return TournamentScore{}, errors.New("walk-forward scores are empty or out of range")
	}
	result := TournamentScore{EndingEquityMicros: int64(starting)}
	for _, score := range scores {
		if (score.NetPnLMicros > 0 && result.NetPnLMicros > math.MaxInt64-score.NetPnLMicros) ||
			(score.NetPnLMicros < 0 && result.NetPnLMicros < math.MinInt64-score.NetPnLMicros) ||
			(score.FundingPnLMicros > 0 && result.FundingPnLMicros > math.MaxInt64-score.FundingPnLMicros) ||
			(score.FundingPnLMicros < 0 && result.FundingPnLMicros < math.MinInt64-score.FundingPnLMicros) ||
			math.MaxUint64-result.FeesPaidMicros < score.FeesPaidMicros ||
			math.MaxUint64-result.Liquidations < score.Liquidations ||
			math.MaxUint64-result.FilledOrders < score.FilledOrders ||
			math.MaxUint64-result.ClosedPositions < score.ClosedPositions {
			return TournamentScore{}, errors.New("walk-forward score aggregate overflows")
		}
		result.NetPnLMicros += score.NetPnLMicros
		result.FundingPnLMicros += score.FundingPnLMicros
		result.FeesPaidMicros += score.FeesPaidMicros
		result.Liquidations += score.Liquidations
		result.FilledOrders += score.FilledOrders
		result.ClosedPositions += score.ClosedPositions
		result.MaxDrawdownMicros = max(result.MaxDrawdownMicros, score.MaxDrawdownMicros)
	}
	if (result.NetPnLMicros > 0 && result.EndingEquityMicros > math.MaxInt64-result.NetPnLMicros) ||
		(result.NetPnLMicros < 0 && result.EndingEquityMicros < math.MinInt64-result.NetPnLMicros) {
		return TournamentScore{}, errors.New("walk-forward ending equity overflows")
	}
	result.EndingEquityMicros += result.NetPnLMicros
	return result, nil
}

func walkForwardInputSHA256(config QualificationConfig, tapes []WalkForwardTapeEvidence) (string, error) {
	payload, err := json.Marshal(struct {
		Version    uint32                    `json:"version"`
		StressRule string                    `json:"stress_rule"`
		Config     QualificationConfig       `json:"config"`
		Tapes      []WalkForwardTapeEvidence `json:"tapes"`
	}{WalkForwardVersion, qualificationStressRule, config, tapes})
	if err != nil {
		return "", fmt.Errorf("encode walk-forward input: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}
