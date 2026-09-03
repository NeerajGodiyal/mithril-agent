package perpspaper

import (
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
		len(left.Tapes) != 2 || len(left.Training) != 12 || left.Forward == nil || left.Stress == nil {
		t.Fatalf("walk-forward boundary = %+v", left)
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
