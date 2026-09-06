package perpspaper

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestSummarizeFixedPlanDistinguishesFlatFromMinimumLot(t *testing.T) {
	config := qualificationTestConfig()
	config.StartingCollateralMicros = 100_000_000 // $100; balanced allocates $25 notional.
	config.VenueSzDecimals = 0                    // One-token lots; prices are at least $1,000.
	config.Quantity = 0                           // Use actual arm sizing, not an explicit quantity cap.
	key := QualificationKey{RiskArm: Balanced, Strategy: StrategyMomentum}
	var firstNormal, firstStress QualificationEvidence
	for _, rising := range []bool{false, true} {
		prices := make([]int, 41)
		for i := range prices {
			prices[i] = 1000
			if rising {
				prices[i] += i * 10
			}
		}
		frames := tournamentTestFrames(prices)
		normal, stress, err := EvaluateFixedPlan(config, key, frames)
		if err != nil {
			t.Fatal(err)
		}
		for _, score := range []QualificationEvidence{normal, stress} {
			if !score.Eligible || score.Score == nil || score.Score.FilledOrders != 0 ||
				score.Score.ClosedPositions != 0 || score.Score.NetPnLMicros != 0 || score.Score.FeesPaidMicros != 0 {
				t.Fatalf("rising=%t expected eligible zero score, got %+v", rising, score)
			}
		}
		replay, err := ReplaySelected(config.replayConfig(key.RiskArm), frames, key)
		if err != nil {
			t.Fatal(err)
		}
		actions, kinds := make(map[string]uint64), make(map[string]uint64)
		directional := 0
		for _, result := range replay.Results {
			actions[result.Action]++
			kinds[result.Decision.SignalKind]++
			if result.Decision.Direction != Flat {
				directional++
			}
			if result.Fill != nil {
				t.Fatalf("rising=%t unexpectedly filled: %+v", rising, result)
			}
		}
		if len(frames) != 40 || len(replay.Results) != 40 ||
			!reflect.DeepEqual(kinds, map[string]uint64{SignalHistoryWarmup: 4, SignalMomentum: 36}) {
			t.Fatalf("unexpected frame/decision denominator: frames=%d results=%d kinds=%v", len(frames), len(replay.Results), kinds)
		}
		if rising {
			if !reflect.DeepEqual(normal, firstNormal) || !reflect.DeepEqual(stress, firstStress) {
				t.Fatal("the existing reduced evidence unexpectedly distinguishes these zero-fill cases")
			}
			if directional != 36 || !reflect.DeepEqual(actions, map[string]uint64{"flat": 4, "below_minimum_lot": 36}) {
				t.Fatalf("expected directional signals rejected by lot sizing: directional=%d actions=%v", directional, actions)
			}
		} else {
			firstNormal, firstStress = normal, stress
			if directional != 0 || !reflect.DeepEqual(actions, map[string]uint64{"flat": 40}) {
				t.Fatalf("expected flat decisions: directional=%d actions=%v", directional, actions)
			}
		}
		behavior, err := SummarizeFixedPlan(config, key, frames)
		if err != nil || behavior.Frames != uint64(len(frames)) || !reflect.DeepEqual(behavior.ActionCounts, actions) || !reflect.DeepEqual(behavior.SignalKindCounts, kinds) {
			t.Fatalf("summary differs from actual replay: %+v %v", behavior, err)
		}
		t.Logf("rising=%t: zero normal/stress scores; frames=%d directional=%d actions=%v", rising, len(frames), directional, actions)
	}
}

func TestSummarizeFixedPlanPreservesLegacyAndScoredEvidence(t *testing.T) {
	config := qualificationTestConfig()
	frames := tournamentTestFrames(qualificationWavePrices(3))
	before := cloneTournamentFrames(frames)
	for _, arm := range []RiskArm{Conservative, Balanced, Experimental} {
		for _, strategy := range []Strategy{"", StrategyMomentum, StrategyMeanReversion, StrategyBreakout, StrategyRegime} {
			key := QualificationKey{RiskArm: arm, Strategy: strategy}
			normal, stress, err := EvaluateFixedPlan(config, key, frames)
			if err != nil {
				t.Fatal(err)
			}
			for index, got := range []QualificationEvidence{normal, stress} {
				replayConfig := config.replayConfig(arm)
				rule := ""
				if index == 1 {
					entryFee, _, _ := armAccounting(arm)
					replayConfig.AdditionalFeeBPS = entryFee
					rule = qualificationStressRule
				}
				// This is the pre-refactor evaluation route, without replayFixedPlan.
				causal, err := tournamentCausalFrames(replayConfig, frames)
				if err != nil {
					t.Fatal(err)
				}
				var replay TapeReplay
				if strategy == "" {
					replay, err = replayTape(replayConfig, causal, Decide)
				} else {
					replay, err = replayTournamentStrategy(replayConfig, causal, 0, strategy)
				}
				if err != nil {
					t.Fatal(err)
				}
				scored, err := scoreTournamentStrategy(replayConfig, causal[len(causal)-1].Book, strategy, replay)
				if err != nil {
					t.Fatal(err)
				}
				want := qualificationEvidence(key, rule, scored)
				gotJSON, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				wantJSON, err := json.Marshal(want)
				if err != nil {
					t.Fatal(err)
				}
				if string(gotJSON) != string(wantJSON) {
					t.Fatalf("evidence changed for %+v stress=%d", key, index)
				}
				if index != 0 {
					continue
				}
				behavior, err := SummarizeFixedPlan(config, key, frames)
				if err != nil {
					t.Fatal(err)
				}
				wantBehavior := ReplayBehavior{Frames: uint64(len(replay.Results)), ActionCounts: make(map[string]uint64), SignalKindCounts: make(map[string]uint64)}
				for _, result := range replay.Results {
					wantBehavior.ActionCounts[result.Action]++
					wantBehavior.SignalKindCounts[result.Decision.SignalKind]++
				}
				if !reflect.DeepEqual(behavior, wantBehavior) {
					t.Fatalf("normal behavior changed for %+v: %+v", key, behavior)
				}
				var actions, signals uint64
				for _, count := range behavior.ActionCounts {
					actions += count
				}
				for _, count := range behavior.SignalKindCounts {
					signals += count
				}
				if actions != uint64(len(frames)) || signals != actions {
					t.Fatalf("behavior denominator mismatch: %+v", behavior)
				}
			}
		}
	}
	if !reflect.DeepEqual(frames, before) {
		t.Fatal("summarizing mutated the original tape")
	}
}

func TestSummarizeFixedPlanRejectsInvalidReplay(t *testing.T) {
	config := qualificationTestConfig()
	frames := tournamentTestFrames(qualificationWavePrices(1))
	for _, test := range []struct {
		key    QualificationKey
		frames []TapeFrame
	}{
		{QualificationKey{RiskArm: Balanced, Strategy: StrategyMomentum}, nil},
		{QualificationKey{RiskArm: RiskArm("unknown"), Strategy: StrategyMomentum}, frames},
		{QualificationKey{RiskArm: Balanced, Strategy: Strategy("model_text")}, frames},
	} {
		behavior, err := SummarizeFixedPlan(config, test.key, test.frames)
		if err == nil || behavior.Frames != 0 || behavior.ActionCounts != nil || behavior.SignalKindCounts != nil {
			t.Fatalf("invalid replay returned behavior: %+v %v", behavior, err)
		}
	}
}

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

func TestReplaySelectedIsCausalDeterministicAndDoesNotMutateInput(t *testing.T) {
	config := tournamentTestConfig()
	frames := tournamentTestFrames([]int{100, 102, 98, 102, 98, 101})
	before := cloneTournamentFrames(frames)
	key := QualificationKey{RiskArm: Balanced, Strategy: StrategyMomentum}

	first, err := ReplaySelected(config, frames, key)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ReplaySelected(config, frames, key)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(frames, before) {
		t.Fatal("selected replay is not deterministic or mutated its input")
	}
	if got := first.Results[len(first.Results)-1].Decision.Direction; got != Direction(Long) {
		t.Fatalf("final momentum direction = %s, want %s; causal prefixes were not accumulated", got, Direction(Long))
	}
}

func TestReplaySelectedUsesRequestedStrategy(t *testing.T) {
	config := tournamentTestConfig()
	frames := tournamentTestFrames([]int{10_000, 10_200, 9_800, 10_200, 9_800, 10_080})

	momentum, err := ReplaySelected(config, frames, QualificationKey{RiskArm: Balanced, Strategy: StrategyMomentum})
	if err != nil {
		t.Fatal(err)
	}
	meanReversion, err := ReplaySelected(config, frames, QualificationKey{RiskArm: Balanced, Strategy: StrategyMeanReversion})
	if err != nil {
		t.Fatal(err)
	}
	last := len(frames) - 1
	if got := momentum.Results[last].Decision.Direction; got != Direction(Long) {
		t.Fatalf("momentum direction = %s, want %s", got, Direction(Long))
	}
	if got := meanReversion.Results[last].Decision.Direction; got != Direction(Short) {
		t.Fatalf("mean-reversion direction = %s, want %s", got, Direction(Short))
	}
}

func TestReplaySelectedRejectsInvalidKey(t *testing.T) {
	frames := tournamentTestFrames([]int{100, 101})
	for name, test := range map[string]struct {
		config ReplayConfig
		key    QualificationKey
	}{
		"risk mismatch": {
			config: tournamentTestConfig(),
			key:    QualificationKey{RiskArm: Conservative, Strategy: StrategyMomentum},
		},
		"unsupported risk": {
			config: func() ReplayConfig {
				config := tournamentTestConfig()
				config.RiskArm = RiskArm("reckless")
				return config
			}(),
			key: QualificationKey{RiskArm: RiskArm("reckless"), Strategy: StrategyMomentum},
		},
		"unsupported strategy": {
			config: tournamentTestConfig(),
			key:    QualificationKey{RiskArm: Balanced, Strategy: Strategy("oracle")},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReplaySelected(test.config, frames, test.key); err == nil {
				t.Fatal("invalid selected replay key was accepted")
			}
		})
	}
}

func TestEvaluateFixedPlanScoresLegacyAndSelectedPlansWithFeeStress(t *testing.T) {
	config := qualificationTestConfig()
	frames := tournamentTestFrames(qualificationWavePrices(3))
	for name, key := range map[string]QualificationKey{
		"legacy":   {RiskArm: Balanced},
		"selected": {RiskArm: Experimental, Strategy: StrategyRegime},
	} {
		t.Run(name, func(t *testing.T) {
			forward, stress, err := EvaluateFixedPlan(config, key, frames)
			if err != nil {
				t.Fatal(err)
			}
			if forward.QualificationKey != key || stress.QualificationKey != key ||
				forward.StressRule != "" || stress.StressRule != qualificationStressRule ||
				forward.Score == nil || stress.Score == nil ||
				stress.Score.FeesPaidMicros <= forward.Score.FeesPaidMicros ||
				stress.Score.EndingEquityMicros > forward.Score.EndingEquityMicros {
				t.Fatalf("fixed plan evidence = forward %+v, stress %+v", forward, stress)
			}
		})
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
