package perpspaper

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestQualificationRejectsNoTradeTieAndComparesAllPairs(t *testing.T) {
	frames := tournamentTestFrames(slices.Repeat([]int{10_000}, 33))
	before := cloneTournamentFrames(frames)
	first, err := QualifyTournament(qualificationTestConfig(), frames)
	if err != nil {
		t.Fatal(err)
	}
	second, err := QualifyTournament(qualificationTestConfig(), frames)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(frames, before) {
		t.Fatal("qualification is not deterministic or mutated its input")
	}
	if first.Status != "research_only" || first.Outcome != "no_training_candidate" ||
		!first.PaperOnly || first.Authorized || first.Promotable || first.EligibleForPaperExperiment ||
		first.TrainingLeader != nil || first.Candidate != nil || len(first.Training) != 12 {
		t.Fatalf("flat qualification = %+v", first)
	}
	if !slices.Equal(first.Reasons, []string{"no_profitable_completed_training_trade"}) {
		t.Fatalf("flat qualification reasons = %v", first.Reasons)
	}
	want := []QualificationKey{
		{Conservative, StrategyMomentum}, {Conservative, StrategyMeanReversion},
		{Conservative, StrategyBreakout}, {Conservative, StrategyRegime},
		{Balanced, StrategyMomentum}, {Balanced, StrategyMeanReversion},
		{Balanced, StrategyBreakout}, {Balanced, StrategyRegime},
		{Experimental, StrategyMomentum}, {Experimental, StrategyMeanReversion},
		{Experimental, StrategyBreakout}, {Experimental, StrategyRegime},
	}
	for index, trial := range first.Training {
		if trial.QualificationKey != want[index] {
			t.Fatalf("training pair %d = %+v, want %+v", index, trial.QualificationKey, want[index])
		}
	}
}

func TestQualificationUsesTrainingOnlyForSelectionAndChecksDoubleFees(t *testing.T) {
	common := qualificationWavePrices(4)
	leftPrices := append(append([]int(nil), common...), qualificationWavePrices(2)...)
	rightSuffix := qualificationWavePrices(2)
	slices.Reverse(rightSuffix)
	rightPrices := append(append([]int(nil), common...), rightSuffix...)
	left, err := QualifyTournament(qualificationTestConfig(), tournamentTestFrames(leftPrices))
	if err != nil {
		t.Fatal(err)
	}
	right, err := QualifyTournament(qualificationTestConfig(), tournamentTestFrames(rightPrices))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left.Training, right.Training) || !reflect.DeepEqual(left.TrainingLeader, right.TrainingLeader) {
		t.Fatal("holdout suffix changed training selection")
	}
	if left.TrainingLeader == nil || left.Holdout == nil || left.Stress == nil {
		for _, trial := range left.Training {
			t.Logf("%s/%s: %+v", trial.RiskArm, trial.Strategy, trial.Score)
		}
		t.Fatalf("qualification did not reach holdout: %+v", left)
	}
	if left.Stress.StressRule != qualificationStressRule || left.Holdout.StressRule != "" ||
		left.Stress.Score == nil || left.Holdout.Score == nil ||
		left.Stress.Score.FeesPaidMicros <= left.Holdout.Score.FeesPaidMicros ||
		left.Stress.Score.EndingEquityMicros > left.Holdout.Score.EndingEquityMicros {
		t.Fatalf("double-fee evidence = holdout %+v, stress %+v", left.Holdout, left.Stress)
	}
	if left.Candidate == nil || !left.EligibleForPaperExperiment || left.Outcome != "candidate_ready_for_more_paper_testing" ||
		left.Authorized || left.Promotable {
		t.Fatalf("unsafe candidate flags = %+v", left)
	}
}

func TestQualificationHoldoutKeepsTrainingAccountFlat(t *testing.T) {
	frames := tournamentTestFrames(qualificationWavePrices(4))
	causal, err := tournamentCausalFrames(qualificationTestConfig().replayConfig(Balanced), frames)
	if err != nil {
		t.Fatal(err)
	}
	split := len(frames) * 2 / 3
	replay, err := replayTournamentStrategy(qualificationTestConfig().replayConfig(Balanced), causal, split, StrategyMomentum)
	if err != nil {
		t.Fatal(err)
	}
	for index, result := range replay.Results[:split] {
		if result.Action != "flat" || result.Fill != nil || len(result.Records) != 0 {
			t.Fatalf("training frame %d contaminated holdout account: %+v", index, result)
		}
	}
}

func TestQualificationRejectsChangedCandleAcrossSplit(t *testing.T) {
	frames := tournamentTestFrames(qualificationWavePrices(4))
	split := len(frames) * 2 / 3
	frames[split].Candles[0].Close = "9999"
	if _, err := QualifyTournament(qualificationTestConfig(), frames); err == nil ||
		!strings.Contains(err.Error(), "changes an existing closed candle") {
		t.Fatalf("changed boundary candle error = %v", err)
	}
}

func TestReplayRejectsPreCloseAndPreviousBookContext(t *testing.T) {
	config := qualificationTestConfig().replayConfig(Balanced)
	preClose := tournamentTestFrames([]int{10_000, 10_010})
	preClose[0].Context.ReceivedAt = preClose[0].Candles[1].CloseTime - 1
	if _, err := ReplayTape(config, preClose); err == nil || !strings.Contains(err.Error(), "context time") {
		t.Fatalf("pre-close context error = %v", err)
	}

	previousBook := tournamentTestFrames([]int{10_000, 10_010, 10_020})
	firstClose := previousBook[0].Candles[1].CloseTime
	previousBook[0].Book.Time = firstClose + 65_000
	previousBook[0].Context.ReceivedAt = previousBook[0].Book.Time
	previousBook[1].Book.Time = previousBook[0].Book.Time + 1_000
	previousBook[1].Context.ReceivedAt = previousBook[0].Book.Time
	if _, err := ReplayTape(config, previousBook); err == nil || !strings.Contains(err.Error(), "context time") {
		t.Fatalf("previous-book context error = %v", err)
	}
}

func TestQualificationValidatesShortTapeBeforeCallingItInsufficient(t *testing.T) {
	frames := tournamentTestFrames([]int{10_000, 10_010})
	frames[0].Context.ReceivedAt = frames[0].Candles[1].CloseTime - 1
	if _, err := QualifyTournament(qualificationTestConfig(), frames); err == nil ||
		!strings.Contains(err.Error(), "verify qualification tape") {
		t.Fatalf("short invalid qualification error = %v", err)
	}
}

func qualificationTestConfig() QualificationConfig {
	return QualificationConfig{
		StartingCollateralMicros: 100_000_000_000, Symbol: SOL,
		VenueMaxLeverage: 20, VenueSzDecimals: 2,
	}
}

func qualificationWavePrices(cycles int) []int {
	prices := make([]int, 0, cycles*20)
	for range cycles {
		for step := 0; step < 10; step++ {
			prices = append(prices, 10_000+step*25)
		}
		for step := 10; step > 0; step-- {
			prices = append(prices, 10_000+step*25)
		}
	}
	return prices
}

func cloneTournamentFrames(frames []TapeFrame) []TapeFrame {
	copy := make([]TapeFrame, len(frames))
	for index, frame := range frames {
		copy[index] = frame
		copy[index].Candles = append([]Candle(nil), frame.Candles...)
		copy[index].Funding = append([]Funding(nil), frame.Funding...)
		copy[index].Book.Levels[0] = append([]Level(nil), frame.Book.Levels[0]...)
		copy[index].Book.Levels[1] = append([]Level(nil), frame.Book.Levels[1]...)
	}
	return copy
}
