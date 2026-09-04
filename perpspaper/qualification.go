package perpspaper

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	QualificationVersion        uint32 = 1
	QualificationMinimumFrames         = 24
	qualificationMaxDrawdownBPS        = 2_000
	qualificationStressRule            = "double_fees_v1"
)

// QualificationConfig deliberately omits a risk arm: qualification compares
// every fixed arm against the same tape.
type QualificationConfig struct {
	StartingCollateralMicros uint64 `json:"starting_collateral_micros"`
	Symbol                   Symbol `json:"symbol"`
	Quantity                 uint64 `json:"quantity"`
	VenueMaxLeverage         uint32 `json:"venue_max_leverage"`
	VenueSzDecimals          uint8  `json:"venue_sz_decimals"`
}

type QualificationKey struct {
	RiskArm  RiskArm  `json:"risk_arm"`
	Strategy Strategy `json:"strategy"`
}

// ReplaySelected applies one qualified strategy and risk arm to a causal paper tape.
func ReplaySelected(config ReplayConfig, frames []TapeFrame, key QualificationKey) (TapeReplay, error) {
	if config.RiskArm != key.RiskArm {
		return TapeReplay{}, errors.New("selected risk arm does not match replay configuration")
	}
	if _, _, _, err := armPolicy(key.RiskArm); err != nil {
		return TapeReplay{}, fmt.Errorf("selected risk arm: %w", err)
	}
	switch key.Strategy {
	case StrategyMomentum, StrategyMeanReversion, StrategyBreakout, StrategyRegime:
	default:
		return TapeReplay{}, fmt.Errorf("unsupported selected strategy %q", key.Strategy)
	}
	causal, err := tournamentCausalFrames(config, frames)
	if err != nil {
		return TapeReplay{}, err
	}
	return replayTournamentStrategy(config, causal, 0, key.Strategy)
}

type QualificationTrial struct {
	QualificationKey
	Eligible         bool             `json:"eligible"`
	IneligibleReason string           `json:"ineligible_reason,omitempty"`
	Score            *TournamentScore `json:"score,omitempty"`
}

type QualificationEvidence struct {
	QualificationKey
	StressRule       string           `json:"stress_rule,omitempty"`
	Eligible         bool             `json:"eligible"`
	IneligibleReason string           `json:"ineligible_reason,omitempty"`
	Score            *TournamentScore `json:"score,omitempty"`
}

// EvaluateFixedPlan scores one frozen paper decision plan on the whole tape,
// both normally and with the same double-fee stress used by qualification. An
// empty strategy means the legacy fixed decision rule.
func EvaluateFixedPlan(config QualificationConfig, key QualificationKey, frames []TapeFrame) (QualificationEvidence, QualificationEvidence, error) {
	normal, err := evaluateFixedPlan(config.replayConfig(key.RiskArm), frames, key)
	if err != nil {
		return QualificationEvidence{}, QualificationEvidence{}, err
	}
	stressConfig := config.replayConfig(key.RiskArm)
	entryFee, _, _ := armAccounting(key.RiskArm)
	stressConfig.AdditionalFeeBPS = entryFee
	stress, err := evaluateFixedPlan(stressConfig, frames, key)
	if err != nil {
		return QualificationEvidence{}, QualificationEvidence{}, err
	}
	return *qualificationEvidence(key, "", normal), *qualificationEvidence(key, qualificationStressRule, stress), nil
}

// Qualification is research evidence only. A passing result is eligible only
// for another bounded paper experiment and can never authorize execution.
type Qualification struct {
	Version                    uint32                 `json:"version"`
	Status                     string                 `json:"status"`
	Outcome                    string                 `json:"outcome"`
	PaperOnly                  bool                   `json:"paper_only"`
	Authorized                 bool                   `json:"authorized"`
	Promotable                 bool                   `json:"promotable"`
	EligibleForPaperExperiment bool                   `json:"eligible_for_paper_experiment"`
	InputSHA256                string                 `json:"input_sha256"`
	Config                     QualificationConfig    `json:"config"`
	Frames                     uint64                 `json:"frames"`
	MinimumFrames              uint64                 `json:"minimum_frames"`
	TrainingFrames             uint64                 `json:"training_frames"`
	HoldoutFrames              uint64                 `json:"holdout_frames"`
	Training                   []QualificationTrial   `json:"training"`
	TrainingLeader             *QualificationKey      `json:"training_leader,omitempty"`
	Holdout                    *QualificationEvidence `json:"holdout,omitempty"`
	Stress                     *QualificationEvidence `json:"stress,omitempty"`
	Candidate                  *QualificationKey      `json:"candidate,omitempty"`
	Reasons                    []string               `json:"reasons"`
}

// QualifyTournament selects on the first two thirds of a causal tape. It then
// evaluates only that fixed leader on the last third, first unchanged and then
// with doubled entry and exit fees. Training frames warm the holdout indicators
// while the holdout account remains flat.
func QualifyTournament(config QualificationConfig, frames []TapeFrame) (Qualification, error) {
	base := config.replayConfig(Balanced)
	if len(frames) == 0 {
		return Qualification{}, errors.New("qualification tape is empty")
	}
	causal, err := tournamentCausalFrames(base, frames)
	if err != nil {
		return Qualification{}, err
	}
	// Validate every recorded market timestamp and visible-book frame even when
	// the tape is still too short to score. Keeping the account flat makes this
	// a trust-boundary check, not another strategy trial.
	if _, err := replayTournamentStrategy(base, causal, len(causal), StrategyMomentum); err != nil {
		return Qualification{}, fmt.Errorf("verify qualification tape: %w", err)
	}
	digest, err := qualificationInputSHA256(config, frames)
	if err != nil {
		return Qualification{}, err
	}
	result := Qualification{
		Version: QualificationVersion, Status: "research_only", Outcome: "insufficient_evidence",
		PaperOnly: true, InputSHA256: digest, Config: config, Frames: uint64(len(frames)),
		MinimumFrames: QualificationMinimumFrames, Training: []QualificationTrial{}, Reasons: []string{},
	}
	if len(frames) < QualificationMinimumFrames {
		result.Reasons = append(result.Reasons, "collect_more_causal_frames")
		return result, nil
	}

	trainingCount := len(frames) * 2 / 3
	result.TrainingFrames = uint64(trainingCount)
	result.HoldoutFrames = uint64(len(frames) - trainingCount)
	arms := [...]RiskArm{Conservative, Balanced, Experimental}
	strategies := [...]Strategy{StrategyMomentum, StrategyMeanReversion, StrategyBreakout, StrategyRegime}
	var leader QualificationTrial
	for _, arm := range arms {
		replayConfig := config.replayConfig(arm)
		for _, strategy := range strategies {
			trialResult, err := evaluateTournamentStrategy(replayConfig, frames[:trainingCount], strategy)
			if err != nil {
				return Qualification{}, fmt.Errorf("training %s/%s: %w", arm, strategy, err)
			}
			trial := qualificationTrial(arm, trialResult)
			result.Training = append(result.Training, trial)
			if meaningfulQualificationScore(trial.Eligible, trial.Score) &&
				(leader.Score == nil || betterQualificationTrial(trial, leader)) {
				leader = trial
			}
		}
	}
	if leader.Score == nil {
		result.Outcome = "no_training_candidate"
		result.Reasons = append(result.Reasons, "no_profitable_completed_training_trade")
		return result, nil
	}
	leaderKey := leader.QualificationKey
	result.TrainingLeader = &leaderKey

	holdoutResult, err := evaluateQualificationHoldout(config.replayConfig(leader.RiskArm), causal, trainingCount, leader.Strategy)
	if err != nil {
		return Qualification{}, fmt.Errorf("holdout %s/%s: %w", leader.RiskArm, leader.Strategy, err)
	}
	result.Holdout = qualificationEvidence(leaderKey, "", holdoutResult)
	stressConfig := config.replayConfig(leader.RiskArm)
	entryFee, _, _ := armAccounting(leader.RiskArm)
	stressConfig.AdditionalFeeBPS = entryFee
	stressResult, err := evaluateQualificationHoldout(stressConfig, causal, trainingCount, leader.Strategy)
	if err != nil {
		return Qualification{}, fmt.Errorf("stress %s/%s: %w", leader.RiskArm, leader.Strategy, err)
	}
	result.Stress = qualificationEvidence(leaderKey, qualificationStressRule, stressResult)
	result.Reasons = append(result.Reasons, qualificationFailures("holdout", *result.Holdout, config.StartingCollateralMicros)...)
	result.Reasons = append(result.Reasons, qualificationFailures("stress", *result.Stress, config.StartingCollateralMicros)...)
	if len(result.Reasons) != 0 {
		result.Outcome = "candidate_rejected"
		return result, nil
	}
	result.Outcome = "candidate_ready_for_more_paper_testing"
	result.EligibleForPaperExperiment = true
	candidate := leaderKey
	result.Candidate = &candidate
	return result, nil
}

func (config QualificationConfig) replayConfig(arm RiskArm) ReplayConfig {
	return ReplayConfig{
		StartingCollateralMicros: config.StartingCollateralMicros, Symbol: config.Symbol,
		RiskArm: arm, Quantity: config.Quantity, VenueMaxLeverage: config.VenueMaxLeverage,
		VenueSzDecimals: config.VenueSzDecimals,
	}
}

func evaluateTournamentStrategy(config ReplayConfig, frames []TapeFrame, strategy Strategy) (TournamentResult, error) {
	causal, err := tournamentCausalFrames(config, frames)
	if err != nil {
		return TournamentResult{}, err
	}
	replay, err := replayTournamentStrategy(config, causal, 0, strategy)
	if err != nil {
		return TournamentResult{}, err
	}
	return scoreTournamentStrategy(config, frames[len(frames)-1].Book, strategy, replay)
}

func evaluateFixedPlan(config ReplayConfig, frames []TapeFrame, key QualificationKey) (TournamentResult, error) {
	if config.RiskArm != key.RiskArm {
		return TournamentResult{}, errors.New("fixed plan risk arm does not match replay configuration")
	}
	causal, err := tournamentCausalFrames(config, frames)
	if err != nil {
		return TournamentResult{}, err
	}
	var replay TapeReplay
	if key.Strategy == "" {
		replay, err = replayTape(config, causal, Decide)
	} else {
		replay, err = replayTournamentStrategy(config, causal, 0, key.Strategy)
	}
	if err != nil {
		return TournamentResult{}, err
	}
	return scoreTournamentStrategy(config, causal[len(causal)-1].Book, key.Strategy, replay)
}

func evaluateQualificationHoldout(config ReplayConfig, causal []TapeFrame, split int, strategy Strategy) (TournamentResult, error) {
	if split <= 0 || split >= len(causal) {
		return TournamentResult{}, errors.New("qualification holdout split is invalid")
	}
	replay, err := replayTournamentStrategy(config, causal, split, strategy)
	if err != nil {
		return TournamentResult{}, err
	}
	return scoreTournamentStrategy(config, causal[len(causal)-1].Book, strategy, replay)
}

func replayTournamentStrategy(config ReplayConfig, causal []TapeFrame, flatPrefix int, strategy Strategy) (TapeReplay, error) {
	frame := 0
	return replayTape(config, causal, func(symbol Symbol, arm RiskArm, candles []Candle) (Decision, error) {
		current := frame
		frame++
		decision, err := tournamentDecision(strategy, symbol, arm, candles)
		if err == nil && current < flatPrefix {
			decision.Direction, decision.ChangeBPS = Flat, 0
		}
		return decision, err
	})
}

func qualificationTrial(arm RiskArm, result TournamentResult) QualificationTrial {
	return QualificationTrial{
		QualificationKey: QualificationKey{RiskArm: arm, Strategy: result.Strategy},
		Eligible:         result.Eligible, IneligibleReason: result.IneligibleReason, Score: result.Score,
	}
}

func qualificationEvidence(key QualificationKey, stress string, result TournamentResult) *QualificationEvidence {
	return &QualificationEvidence{
		QualificationKey: key, StressRule: stress, Eligible: result.Eligible,
		IneligibleReason: result.IneligibleReason, Score: result.Score,
	}
}

func meaningfulQualificationScore(eligible bool, score *TournamentScore) bool {
	return eligible && score != nil && score.NetPnLMicros > 0 && score.FilledOrders > 0 &&
		score.ClosedPositions == score.FilledOrders && score.FeesPaidMicros > 0 && score.Liquidations == 0
}

func betterQualificationTrial(left, right QualificationTrial) bool {
	if left.Score.EndingEquityMicros != right.Score.EndingEquityMicros {
		return left.Score.EndingEquityMicros > right.Score.EndingEquityMicros
	}
	if left.Score.MaxDrawdownMicros != right.Score.MaxDrawdownMicros {
		return left.Score.MaxDrawdownMicros < right.Score.MaxDrawdownMicros
	}
	if left.Score.Liquidations != right.Score.Liquidations {
		return left.Score.Liquidations < right.Score.Liquidations
	}
	if qualificationRiskRank(left.RiskArm) != qualificationRiskRank(right.RiskArm) {
		return qualificationRiskRank(left.RiskArm) < qualificationRiskRank(right.RiskArm)
	}
	return left.Strategy < right.Strategy
}

func qualificationRiskRank(arm RiskArm) int {
	switch arm {
	case Conservative:
		return 0
	case Balanced:
		return 1
	default:
		return 2
	}
}

func qualificationFailures(prefix string, evidence QualificationEvidence, starting uint64) []string {
	if !evidence.Eligible || evidence.Score == nil {
		reason := evidence.IneligibleReason
		if reason == "" {
			reason = "ineligible"
		}
		return []string{prefix + "_" + reason}
	}
	score := evidence.Score
	var reasons []string
	if score.FilledOrders == 0 || score.ClosedPositions == 0 {
		reasons = append(reasons, prefix+"_has_no_completed_trade")
	} else if score.ClosedPositions != score.FilledOrders {
		reasons = append(reasons, prefix+"_has_unmatched_fills")
	}
	if score.FeesPaidMicros == 0 {
		reasons = append(reasons, prefix+"_has_no_cost_evidence")
	}
	if score.NetPnLMicros <= 0 {
		reasons = append(reasons, prefix+"_not_profitable_after_costs")
	}
	if score.Liquidations != 0 {
		reasons = append(reasons, prefix+"_liquidated")
	}
	maxDrawdown, err := mulDivFloor(starting, qualificationMaxDrawdownBPS, basisPoints)
	if err != nil || score.MaxDrawdownMicros > maxDrawdown {
		reasons = append(reasons, prefix+"_drawdown_above_20_percent")
	}
	return reasons
}

func qualificationInputSHA256(config QualificationConfig, frames []TapeFrame) (string, error) {
	payload, err := json.Marshal(struct {
		Version    uint32              `json:"version"`
		StressRule string              `json:"stress_rule"`
		Config     QualificationConfig `json:"config"`
		Frames     []TapeFrame         `json:"frames"`
	}{Version: QualificationVersion, StressRule: qualificationStressRule, Config: config, Frames: frames})
	if err != nil {
		return "", fmt.Errorf("encode qualification input: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}
