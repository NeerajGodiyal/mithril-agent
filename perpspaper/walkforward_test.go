package perpspaper

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestWalkForwardPreservesVersionedNoCandidateReason(t *testing.T) {
	first := shiftedWalkForwardFrames(tournamentTestFrames(slices.Repeat([]int{10_000}, 33)), 0)
	second := shiftedWalkForwardFrames(tournamentTestFrames(slices.Repeat([]int{10_000}, 33)), 10_000_000)
	result, err := QualifyWalkForward(qualificationTestConfig(), []WalkForwardTape{
		{ContentSHA256: strings.Repeat("1", 64), Frames: first},
		{ContentSHA256: strings.Repeat("2", 64), Frames: second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "no_training_candidate" ||
		!slices.Equal(result.Reasons, []string{"no_profitable_completed_training_trade"}) {
		t.Fatalf("flat walk-forward result = %+v", result)
	}
}

func TestWalkForwardFreezesTrainingLeaderBeforeFinalTape(t *testing.T) {
	training := shiftedWalkForwardFrames(tournamentTestFrames(qualificationWavePrices(4)), 0)
	forwardLeft := shiftedWalkForwardFrames(tournamentTestFrames(qualificationWavePrices(3)), 10_000_000)
	forwardRightPrices := qualificationWavePrices(3)
	for left, right := 0, len(forwardRightPrices)-1; left < right; left, right = left+1, right-1 {
		forwardRightPrices[left], forwardRightPrices[right] = forwardRightPrices[right], forwardRightPrices[left]
	}
	forwardRight := shiftedWalkForwardFrames(tournamentTestFrames(forwardRightPrices), 10_000_000)

	left, err := QualifyWalkForward(qualificationTestConfig(), []WalkForwardTape{{ContentSHA256: strings.Repeat("1", 64), Frames: training}, {ContentSHA256: strings.Repeat("2", 64), Frames: forwardLeft}})
	if err != nil {
		t.Fatal(err)
	}
	right, err := QualifyWalkForward(qualificationTestConfig(), []WalkForwardTape{{ContentSHA256: strings.Repeat("1", 64), Frames: training}, {ContentSHA256: strings.Repeat("3", 64), Frames: forwardRight}})
	if err != nil {
		t.Fatal(err)
	}
	if left.TrainingLeader == nil || right.TrainingLeader == nil ||
		!reflect.DeepEqual(left.Training, right.Training) || !reflect.DeepEqual(left.TrainingLeader, right.TrainingLeader) {
		t.Fatalf("final tape changed selection: left=%+v right=%+v", left.TrainingLeader, right.TrainingLeader)
	}
	if left.Status != "research_only" || !left.PaperOnly || left.Authorized || left.Promotable ||
		len(left.Tapes) != 2 || len(left.Training) != 12 || left.TrainingTrials != 12 ||
		left.HoldoutPlansCompared != 1 || left.Forward == nil || left.Stress == nil ||
		left.StatisticalConfidence != QualificationConfidence {
		t.Fatalf("walk-forward boundary = %+v", left)
	}
}

func TestWalkForwardExecutionDelayUsesNextBookAndIgnoresFinalSignal(t *testing.T) {
	config := qualificationTestConfig().replayConfig(Balanced)
	frames := tournamentTestFrames([]int{10_000, 10_000, 10_000, 10_000, 10_000, 10_000, 10_000, 11_000, 9_000})
	causal, err := tournamentCausalFrames(config, frames)
	if err != nil {
		t.Fatal(err)
	}
	normal, err := replayTournamentStrategy(config, causal, 0, StrategyMomentum)
	if err != nil {
		t.Fatal(err)
	}
	delayed, err := replayTournamentStrategyOneFrameDelay(config, causal, 0, StrategyMomentum)
	if err != nil {
		t.Fatal(err)
	}
	decisionIndex := len(frames) - 2
	executionIndex := len(frames) - 1
	finalSignal, err := tournamentDecision(StrategyMomentum, SOL, Balanced, causal[executionIndex].Candles)
	if err != nil {
		t.Fatal(err)
	}
	if normal.Results[decisionIndex].Decision.Direction != Direction(Long) ||
		delayed.Results[decisionIndex].Decision.Direction != Flat ||
		delayed.Results[executionIndex].Decision.Direction != Direction(Long) ||
		finalSignal.Direction != Direction(Short) || delayed.Results[executionIndex].Fill == nil {
		t.Fatalf("normal=%+v delayed=%+v final=%+v", normal.Results, delayed.Results, finalSignal)
	}
	want, err := decimalMicros(frames[executionIndex].Book.Levels[1][0].Price)
	if err != nil || delayed.Results[executionIndex].Fill.AveragePriceMicros != want {
		t.Fatalf("delayed fill = %+v, next-book ask = %d, %v", delayed.Results[executionIndex].Fill, want, err)
	}
	fills, closes := 0, 0
	for _, result := range delayed.Results {
		if result.Fill != nil {
			fills++
		}
		if result.Action == "closed" {
			closes++
		}
	}
	if fills != 1 || closes != 0 {
		t.Fatalf("final queued short signal executed: fills=%d closes=%d results=%+v", fills, closes, delayed.Results)
	}
}

func TestWalkForwardExecutionDelayAppliesFundingAndLiquidationBeforeQueuedDecision(t *testing.T) {
	config := qualificationTestConfig().replayConfig(Experimental)
	frames := tournamentTestFrames([]int{10_000, 10_000, 10_000, 10_000, 10_000, 10_000, 10_000, 9_000, 9_000, 50_000})
	last := len(frames) - 1
	frames[last].Funding = []Funding{{
		Symbol: SOL, Rate: "-0.001", Time: frames[last].Book.Time - 1,
	}}
	causal, err := tournamentCausalFrames(config, frames)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := replayTournamentStrategyOneFrameDelay(config, causal, 0, StrategyMomentum)
	if err != nil {
		t.Fatal(err)
	}
	result := replay.Results[last]
	if result.Action != "liquidated" || result.Fill != nil || len(result.Records) < 2 ||
		result.Records[0].Command.Type != FundingApplied || result.Records[1].Command.Type != Marked ||
		replay.State.Position != nil || replay.State.Liquidations != 1 {
		t.Fatalf("funding/mark/liquidation order = %+v, state=%+v", result, replay.State)
	}
}

func TestWalkForwardAdvisoryInputIsSeparateAndEachTamperFails(t *testing.T) {
	training := shiftedWalkForwardFrames(tournamentTestFrames(qualificationWavePrices(4)), 0)
	forward := shiftedWalkForwardFrames(tournamentTestFrames(qualificationWavePrices(3)), 10_000_000)
	result, err := QualifyWalkForward(qualificationTestConfig(), []WalkForwardTape{
		{ContentSHA256: strings.Repeat("1", 64), Frames: training},
		{ContentSHA256: strings.Repeat("2", 64), Frames: forward},
	})
	if err != nil || result.TrainingLeader == nil {
		t.Fatalf("qualification = %+v, %v", result, err)
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "execution_delay") {
		t.Fatalf("walk-forward qualification contains advisory: %s, %v", encoded, err)
	}
	advisory, err := EvaluateOneFrameExecutionDelay(
		result.Config, forward, result.InputSHA256, strings.Repeat("2", 64), *result.TrainingLeader,
	)
	if err != nil {
		t.Fatal(err)
	}
	input, err := walkForwardInputSHA256(result.Config, result.Tapes)
	if err != nil || input != result.InputSHA256 || input == advisory.InputSHA256 {
		t.Fatalf("input digests = qualification %q advisory %q, %v", result.InputSHA256, advisory.InputSHA256, err)
	}
	for name, mutate := range map[string]func(*ExecutionDelayAdvisory){
		"authority": func(advisory *ExecutionDelayAdvisory) { advisory.Authorized = true },
		"evidence":  func(advisory *ExecutionDelayAdvisory) { advisory.Evidence.Eligible = !advisory.Evidence.Eligible },
		"digest":    func(advisory *ExecutionDelayAdvisory) { advisory.InputSHA256 = strings.Repeat("f", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := advisory
			mutate(&candidate)
			if candidate.Validate(result.InputSHA256, strings.Repeat("2", 64), *result.TrainingLeader) == nil {
				t.Fatal("tampered advisory still validated")
			}
		})
	}
	malformedHash := advisory
	malformedHash.QualificationInputSHA256 = strings.Repeat("g", 64)
	malformedHash.InputSHA256, _ = executionDelayInputSHA256(
		malformedHash.QualificationInputSHA256, malformedHash.FinalTapeSHA256, *result.TrainingLeader,
	)
	if malformedHash.Validate(
		malformedHash.QualificationInputSHA256, malformedHash.FinalTapeSHA256, *result.TrainingLeader,
	) == nil {
		t.Fatal("self-consistent malformed lineage hash validated")
	}
	malformedLeader := QualificationKey{RiskArm: RiskArm("reckless"), Strategy: StrategyMomentum}
	malformedKey := advisory
	malformedKey.Evidence.QualificationKey = malformedLeader
	malformedKey.InputSHA256, _ = executionDelayInputSHA256(
		malformedKey.QualificationInputSHA256, malformedKey.FinalTapeSHA256, malformedLeader,
	)
	malformedKey.ResultSHA256, _ = executionDelayResultSHA256(malformedKey.Evidence)
	if malformedKey.Validate(
		malformedKey.QualificationInputSHA256, malformedKey.FinalTapeSHA256, malformedLeader,
	) == nil {
		t.Fatal("self-consistent malformed leader validated")
	}
}

func TestWalkForwardRejectsDuplicateOverlapAndDigestMismatch(t *testing.T) {
	first := shiftedWalkForwardFrames(tournamentTestFrames(qualificationWavePrices(2)), 0)
	second := shiftedWalkForwardFrames(tournamentTestFrames(qualificationWavePrices(2)), 10_000_000)
	for name, tapes := range map[string][]WalkForwardTape{
		"duplicate": {{ContentSHA256: strings.Repeat("1", 64), Frames: first}, {ContentSHA256: strings.Repeat("1", 64), Frames: first}},
		"reversed":  {{ContentSHA256: strings.Repeat("1", 64), Frames: second}, {ContentSHA256: strings.Repeat("2", 64), Frames: first}},
		"overlap":   {{ContentSHA256: strings.Repeat("1", 64), Frames: first}, {ContentSHA256: strings.Repeat("2", 64), Frames: shiftedWalkForwardFrames(first, 1)}},
		"digest":    {{ContentSHA256: strings.Repeat("A", 64), Frames: first}, {ContentSHA256: strings.Repeat("2", 64), Frames: second}},
	} {
		if _, err := QualifyWalkForward(qualificationTestConfig(), tapes); err == nil {
			t.Fatalf("%s tapes accepted", name)
		}
	}
	if _, err := QualifyWalkForward(qualificationTestConfig(), []WalkForwardTape{{ContentSHA256: strings.Repeat("1", 64), Frames: first}, {ContentSHA256: strings.Repeat("2", 64), Frames: second}}); err != nil {
		t.Fatalf("valid content digests rejected: %v", err)
	}
}

func TestWalkForwardReportsShortTapeWithoutSelecting(t *testing.T) {
	first := shiftedWalkForwardFrames(tournamentTestFrames([]int{10_000, 10_010}), 0)
	second := shiftedWalkForwardFrames(tournamentTestFrames(qualificationWavePrices(2)), 10_000_000)
	result, err := QualifyWalkForward(qualificationTestConfig(), []WalkForwardTape{{ContentSHA256: strings.Repeat("1", 64), Frames: first}, {ContentSHA256: strings.Repeat("2", 64), Frames: second}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "insufficient_evidence" || len(result.Reasons) != 1 || result.TrainingLeader != nil ||
		result.EligibleForPaperExperiment || result.InputSHA256 == "" {
		t.Fatalf("short result = %+v", result)
	}
}

func TestBestCompletedTrainingAttemptsKeepsLosingActiveAttempts(t *testing.T) {
	score := func(pnl int64, drawdown, fills, closes uint64) *TournamentScore {
		return &TournamentScore{
			EndingEquityMicros: 100_000_000 + pnl, NetPnLMicros: pnl,
			MaxDrawdownMicros: drawdown, FilledOrders: fills, ClosedPositions: closes,
		}
	}
	training := []WalkForwardTrial{
		{QualificationKey: QualificationKey{RiskArm: Conservative, Strategy: StrategyMomentum}, Eligible: true, Aggregate: score(0, 0, 0, 0)},
		{QualificationKey: QualificationKey{RiskArm: Conservative, Strategy: StrategyMeanReversion}, Eligible: true, Aggregate: score(-10, 5, 1, 1)},
		{QualificationKey: QualificationKey{RiskArm: Conservative, Strategy: StrategyBreakout}, Eligible: true, Aggregate: score(-20, 1, 1, 1)},
		{QualificationKey: QualificationKey{RiskArm: Conservative, Strategy: StrategyRegime}, Eligible: true, Aggregate: score(100, 1, 2, 1)},
		{QualificationKey: QualificationKey{RiskArm: Balanced, Strategy: StrategyMomentum}, Eligible: false, Aggregate: score(50, 1, 1, 1)},
		{QualificationKey: QualificationKey{RiskArm: Balanced, Strategy: StrategyBreakout}, Eligible: true, Aggregate: score(-5, 9, 2, 2)},
		{QualificationKey: QualificationKey{RiskArm: Balanced, Strategy: StrategyMeanReversion}, Eligible: true, Aggregate: score(-5, 4, 2, 2)},
		{QualificationKey: QualificationKey{RiskArm: Experimental, Strategy: StrategyRegime}, Eligible: true, Aggregate: score(-1, 2, 3, 3)},
	}

	got := BestCompletedTrainingAttempts(training)
	want := []QualificationKey{
		{RiskArm: Conservative, Strategy: StrategyMeanReversion},
		{RiskArm: Balanced, Strategy: StrategyMeanReversion},
		{RiskArm: Experimental, Strategy: StrategyRegime},
	}
	if len(got) != len(want) {
		t.Fatalf("best completed attempts = %+v", got)
	}
	for index := range want {
		if got[index].QualificationKey != want[index] || got[index].Score == nil || got[index].Score.NetPnLMicros >= 0 {
			t.Fatalf("best completed attempt %d = %+v", index, got[index])
		}
	}
	got[0].Score.NetPnLMicros = 1
	if training[1].Aggregate.NetPnLMicros != -10 {
		t.Fatal("returned score aliases the training evidence")
	}
	if got := BestCompletedTrainingAttempts(training[:1]); len(got) != 0 {
		t.Fatalf("no-trade attempt was exposed as active: %+v", got)
	}
}

func shiftedWalkForwardFrames(frames []TapeFrame, offset int64) []TapeFrame {
	shifted := cloneTournamentFrames(frames)
	for index := range shifted {
		for candleIndex := range shifted[index].Candles {
			shifted[index].Candles[candleIndex].OpenTime += offset
			shifted[index].Candles[candleIndex].CloseTime += offset
		}
		shifted[index].Context.ReceivedAt += offset
		shifted[index].Book.Time += offset
		for fundingIndex := range shifted[index].Funding {
			shifted[index].Funding[fundingIndex].Time += offset
		}
	}
	return shifted
}
