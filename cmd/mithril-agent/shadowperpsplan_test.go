package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

func TestShadowPerpsPlanSelectsNextRunAndRestoresPrevious(t *testing.T) {
	stateDir, config, now := shadowPerpsPlanFixture(t)
	baseline, baselineSHA, err := loadOrCreateShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, config, perpspaper.Balanced, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.DecisionMode != shadowPerpsDecisionLegacy || baseline.Key.RiskArm != perpspaper.Balanced ||
		baseline.Key.Strategy != "" || !validLowerSHA256(baselineSHA) {
		t.Fatalf("baseline = %+v, %q", baseline, baselineSHA)
	}
	qualification := qualifiedShadowPerpsWalkForward(t, stateDir, config, 3)
	receipt, err := selectQualifiedShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, baselineSHA, qualification, now.Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "qualified_paper_plan_selected" || !receipt.PointerUpdated ||
		!receipt.RollbackUpdated || receipt.Effective != "next_bounded_invocation" ||
		receipt.Authorized || receipt.ExecutionEnabled {
		t.Fatalf("selection receipt = %+v", receipt)
	}
	selected, selectedSHA, err := loadOrCreateShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, config, perpspaper.Balanced, now.Add(2*time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if selected.DecisionMode != shadowPerpsDecisionSelected ||
		selected.Key != *qualification.Candidate || selectedSHA != receipt.PlanSHA256 ||
		selected.QualificationInputSHA256 != qualification.InputSHA256 || selected.Comparison == nil ||
		selected.Comparison.IncumbentDecisionMode != shadowPerpsDecisionLegacy ||
		selected.Comparison.IncumbentPlanSHA256 != baselineSHA ||
		selected.Comparison.Status != "challenger_outperformed_incumbent" ||
		len(selected.Comparison.Reasons) != 0 {
		t.Fatalf("selected plan = %+v, %q", selected, selectedSHA)
	}
	again, err := selectQualifiedShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, selectedSHA, qualification, now.Add(3*time.Hour),
	)
	if err != nil || again.Status != "qualified_paper_plan_already_selected" ||
		again.PointerUpdated || again.RollbackUpdated {
		t.Fatalf("idempotent selection = %+v, %v", again, err)
	}
	restored, err := restoreShadowPerpsPlan(stateDir, perpspaper.SOL, now.Add(4*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != "perps_paper_plan_restored" || !restored.PointerUpdated ||
		restored.PlanSHA256 != baselineSHA {
		t.Fatalf("restore receipt = %+v", restored)
	}
	active, activeSHA, pointer, err := loadBoundShadowPerpsPlanPointer(
		shadowPerpsActivePlanPath(stateDir, perpspaper.SOL), perpspaper.Mainnet, config,
	)
	if err != nil {
		t.Fatal(err)
	}
	if active.DecisionMode != shadowPerpsDecisionLegacy || activeSHA != baselineSHA ||
		pointer.RestoredFromSHA256 != selectedSHA {
		t.Fatalf("restored selection = %+v, %q, %+v", active, activeSHA, pointer)
	}
	retired, err := selectQualifiedShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, baselineSHA, qualification, now.Add(5*time.Hour),
	)
	if err != nil || retired.Status != "qualified_paper_plan_retired" || retired.PointerUpdated {
		t.Fatalf("retired selection = %+v, %v", retired, err)
	}
}

func TestShadowPerpsPlanRejectsUnqualifiedOrMismatchedEvidenceWithoutChangingPointer(t *testing.T) {
	stateDir, config, now := shadowPerpsPlanFixture(t)
	_, baselineSHA, err := loadOrCreateShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, config, perpspaper.Balanced, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	active := shadowPerpsActivePlanPath(stateDir, perpspaper.SOL)
	before, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	rejected := qualifiedShadowPerpsWalkForward(t, stateDir, config, 3)
	rejected.Outcome = "candidate_rejected"
	rejected.EligibleForPaperExperiment = false
	rejected.Reasons = []string{"forward_net_pnl_not_positive"}
	receipt, err := selectQualifiedShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, baselineSHA, rejected, now.Add(time.Hour),
	)
	if err != nil || receipt.Status != "qualification_not_selected" || receipt.PointerUpdated {
		t.Fatalf("rejected receipt = %+v, %v", receipt, err)
	}
	after, err := os.ReadFile(active)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected evidence changed pointer: %v", err)
	}
	qualified := qualifiedShadowPerpsWalkForward(t, stateDir, config, 3)
	if _, err := selectQualifiedShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, strings.Repeat("f", 64), qualified, now.Add(2*time.Hour),
	); err == nil || !strings.Contains(err.Error(), "changed during") {
		t.Fatalf("mismatched pinned plan error = %v", err)
	}
	after, err = os.ReadFile(active)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("mismatched evidence changed pointer: %v", err)
	}
}

func TestShadowPerpsPlanKeepsTwoTapeResultResearchOnlyWithoutChangingPointer(t *testing.T) {
	stateDir, config, now := shadowPerpsPlanFixture(t)
	_, baselineSHA, err := loadOrCreateShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, config, perpspaper.Balanced, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	active := shadowPerpsActivePlanPath(stateDir, perpspaper.SOL)
	before, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := selectQualifiedShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, baselineSHA,
		qualifiedShadowPerpsWalkForward(t, stateDir, config, 2), now.Add(time.Hour),
	)
	if err != nil || receipt.Status != "qualification_research_only" || receipt.PointerUpdated ||
		len(receipt.Reasons) != 1 || receipt.Reasons[0] != "collect_at_least_three_separate_tapes" {
		t.Fatalf("two-tape receipt = %+v, %v", receipt, err)
	}
	after, err := os.ReadFile(active)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("two-tape result changed pointer: %v", err)
	}
}

func TestShadowPerpsPlanRejectsWeakerChallengerWithoutChangingPointer(t *testing.T) {
	stateDir, config, now := shadowPerpsPlanFixture(t)
	_, baselineSHA, err := loadOrCreateShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, config, perpspaper.Balanced, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	qualified := qualifiedShadowPerpsWalkForward(t, stateDir, config, 3)
	selected, err := selectQualifiedShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, baselineSHA, qualified, now.Add(time.Hour),
	)
	if err != nil || !selected.PointerUpdated {
		t.Fatalf("select incumbent = %+v, %v", selected, err)
	}
	tape, _, err := loadShadowPerpsComparisonTape(stateDir, perpspaper.Mainnet, qualified)
	if err != nil {
		t.Fatal(err)
	}
	weaker := qualified
	weaker.InputSHA256 = strings.Repeat("d", 64)
	weakerKey, weakerForward, weakerStress := weakerShadowPerpsPlanCandidate(
		t, config, tape.Frames, *qualified.Candidate, *qualified.Forward, *qualified.Stress,
	)
	weaker.Candidate, weaker.TrainingLeader = &weakerKey, &weakerKey
	weaker.Forward, weaker.Stress = &weakerForward, &weakerStress
	active := shadowPerpsActivePlanPath(stateDir, perpspaper.SOL)
	before, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := selectQualifiedShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, selected.PlanSHA256, weaker, now.Add(2*time.Hour),
	)
	if err != nil || receipt.Status != "challenger_not_selected" || receipt.PointerUpdated ||
		receipt.Comparison == nil || len(receipt.Reasons) == 0 ||
		!strings.Contains(strings.Join(receipt.Reasons, ","), "underperformed_incumbent") {
		t.Fatalf("weaker receipt = %+v, %v", receipt, err)
	}
	after, err := os.ReadFile(active)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("weaker challenger changed pointer: %v", err)
	}
}

func TestShadowPerpsPlanArtifactAndPointerArePrivateAndBound(t *testing.T) {
	stateDir, config, now := shadowPerpsPlanFixture(t)
	_, digest, err := loadOrCreateShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, config, perpspaper.Balanced, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	active := shadowPerpsActivePlanPath(stateDir, perpspaper.SOL)
	var pointer shadowPerpsPlanPointer
	if err := readStrictJSON(active, &pointer); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{active, pointer.PlanPath} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s is not a private regular file: %v, %v", path, info, err)
		}
	}
	raw, err := os.ReadFile(pointer.PlanPath)
	if err != nil {
		t.Fatal(err)
	}
	var plan map[string]any
	if json.Unmarshal(raw, &plan) != nil || pointer.PlanSHA256 != digest {
		t.Fatalf("artifact binding = %+v", pointer)
	}
	if err := os.WriteFile(pointer.PlanPath, append(raw, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := loadBoundShadowPerpsPlanPointer(active, perpspaper.Mainnet, config); err == nil {
		t.Fatal("tampered plan artifact was accepted")
	}
}

func shadowPerpsPlanFixture(t *testing.T) (string, perpspaper.QualificationConfig, time.Time) {
	t.Helper()
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(base, "current")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return stateDir, perpspaper.QualificationConfig{
		StartingCollateralMicros: 100_000_000, Symbol: perpspaper.SOL,
		VenueMaxLeverage: 20, VenueSzDecimals: 2,
	}, time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
}

func shadowPerpsActivePlanPath(stateDir string, symbol perpspaper.Symbol) string {
	_, _, active, _, _ := shadowPerpsPlanPaths(stateDir, symbol)
	return active
}

func qualifiedShadowPerpsWalkForward(t *testing.T, stateDir string, config perpspaper.QualificationConfig, tapeCount int) perpspaper.WalkForwardQualification {
	t.Helper()
	frames := shadowPerpsPlanTestFrames(1_000_000_000, shadowPerpsPlanWavePrices(3))
	tape := shadowPerpsTape{
		Version: 3, PaperOnly: true, AccountingModel: shadowPerpsLegacyModel,
		Config: shadowPerpsTapeConfig{
			Environment: perpspaper.Mainnet, Symbol: config.Symbol, RiskArm: perpspaper.Balanced,
			StartingCollateralMicros: config.StartingCollateralMicros,
			VenueMaxLeverage:         config.VenueMaxLeverage, VenueSzDecimals: config.VenueSzDecimals,
		},
		Frames: frames,
	}
	path, err := sealShadowPerpsTape(stateDir, tape)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := perpspaper.QualifyTournament(config, frames)
	if err != nil {
		t.Fatal(err)
	}
	key, forward, stress := bestShadowPerpsPlanCandidate(t, config, frames)
	tapes := make([]perpspaper.WalkForwardTapeEvidence, tapeCount)
	for index := range tapes {
		first := frames[0].Book.Time - int64(tapeCount-index)*10_000_000
		tapes[index] = perpspaper.WalkForwardTapeEvidence{
			ContentSHA256:     strings.Repeat(strconv.Itoa(index+1), 64),
			ReplayInputSHA256: strings.Repeat(strconv.Itoa(index+4), 64),
			Frames:            uint64(len(frames)), FirstTime: first, LastTime: first + int64(len(frames))*60_000,
		}
	}
	final := &tapes[len(tapes)-1]
	final.ContentSHA256 = strings.TrimSuffix(filepath.Base(path), ".json")
	final.ReplayInputSHA256 = replayed.InputSHA256
	final.Frames = uint64(len(frames))
	final.FirstTime, final.LastTime = frames[0].Book.Time, frames[len(frames)-1].Book.Time
	return perpspaper.WalkForwardQualification{
		Version: perpspaper.WalkForwardVersion, Status: "research_only",
		Outcome: "candidate_ready_for_more_paper_testing", PaperOnly: true,
		EligibleForPaperExperiment: true, InputSHA256: strings.Repeat("a", 64),
		Config: config, Tapes: tapes, TrainingLeader: &key, Candidate: &key,
		Forward: &forward, Stress: &stress,
		Reasons: []string{},
	}
}

func bestShadowPerpsPlanCandidate(t *testing.T, config perpspaper.QualificationConfig, frames []perpspaper.TapeFrame) (perpspaper.QualificationKey, perpspaper.QualificationEvidence, perpspaper.QualificationEvidence) {
	t.Helper()
	var best perpspaper.QualificationKey
	var bestForward, bestStress perpspaper.QualificationEvidence
	for _, arm := range []perpspaper.RiskArm{perpspaper.Conservative, perpspaper.Balanced, perpspaper.Experimental} {
		for _, strategy := range []perpspaper.Strategy{perpspaper.StrategyMomentum, perpspaper.StrategyMeanReversion, perpspaper.StrategyBreakout, perpspaper.StrategyRegime} {
			key := perpspaper.QualificationKey{RiskArm: arm, Strategy: strategy}
			forward, stress, err := perpspaper.EvaluateFixedPlan(config, key, frames)
			if err != nil {
				t.Fatal(err)
			}
			if !passingShadowPerpsPlanEvidence(config, forward, stress) {
				continue
			}
			if best == (perpspaper.QualificationKey{}) ||
				compareShadowPerpsEvidence(forward, bestForward) > 0 && compareShadowPerpsEvidence(stress, bestStress) >= 0 {
				best, bestForward, bestStress = key, forward, stress
			}
		}
	}
	if best == (perpspaper.QualificationKey{}) {
		t.Fatal("test tape has no passing challenger")
	}
	return best, bestForward, bestStress
}

func weakerShadowPerpsPlanCandidate(
	t *testing.T,
	config perpspaper.QualificationConfig,
	frames []perpspaper.TapeFrame,
	incumbent perpspaper.QualificationKey,
	incumbentForward, incumbentStress perpspaper.QualificationEvidence,
) (perpspaper.QualificationKey, perpspaper.QualificationEvidence, perpspaper.QualificationEvidence) {
	t.Helper()
	for _, arm := range []perpspaper.RiskArm{perpspaper.Conservative, perpspaper.Balanced, perpspaper.Experimental} {
		for _, strategy := range []perpspaper.Strategy{perpspaper.StrategyMomentum, perpspaper.StrategyMeanReversion, perpspaper.StrategyBreakout, perpspaper.StrategyRegime} {
			key := perpspaper.QualificationKey{RiskArm: arm, Strategy: strategy}
			if key == incumbent {
				continue
			}
			forward, stress, err := perpspaper.EvaluateFixedPlan(config, key, frames)
			if err != nil {
				t.Fatal(err)
			}
			if passingShadowPerpsPlanEvidence(config, forward, stress) &&
				len(shadowPerpsComparisonReasons(incumbentForward, incumbentStress, forward, stress)) != 0 {
				return key, forward, stress
			}
		}
	}
	t.Fatal("test tape has no weaker passing challenger")
	return perpspaper.QualificationKey{}, perpspaper.QualificationEvidence{}, perpspaper.QualificationEvidence{}
}

func passingShadowPerpsPlanEvidence(config perpspaper.QualificationConfig, evidence ...perpspaper.QualificationEvidence) bool {
	for _, item := range evidence {
		if !item.Eligible || item.Score == nil || item.Score.NetPnLMicros <= 0 ||
			item.Score.FeesPaidMicros == 0 || item.Score.FilledOrders == 0 ||
			item.Score.ClosedPositions != item.Score.FilledOrders || item.Score.Liquidations != 0 ||
			item.Score.MaxDrawdownMicros > config.StartingCollateralMicros/5 {
			return false
		}
	}
	return true
}

func shadowPerpsPlanWavePrices(cycles int) []int {
	prices := make([]int, 0, cycles*20)
	for range cycles {
		for step := 0; step < 10; step++ {
			prices = append(prices, 1_000+step*25)
		}
		for step := 10; step > 0; step-- {
			prices = append(prices, 1_000+step*25)
		}
	}
	return prices
}

func shadowPerpsPlanTestFrames(offset int64, prices []int) []perpspaper.TapeFrame {
	candles := make([]perpspaper.Candle, len(prices))
	for index, price := range prices {
		open := offset + int64(index)*60_000 + 1
		text := strconv.Itoa(price)
		candles[index] = perpspaper.Candle{
			OpenTime: open, CloseTime: open + 59_999, Symbol: perpspaper.SOL, Interval: "1m",
			Open: text, Close: text, High: text, Low: text, Volume: "1", Trades: 1,
		}
	}
	frames := make([]perpspaper.TapeFrame, len(prices)-1)
	for index := range frames {
		price := prices[index+1]
		bookTime := candles[index+1].CloseTime + 1_000
		frames[index] = perpspaper.TapeFrame{
			Candles: []perpspaper.Candle{candles[index], candles[index+1]},
			Context: perpspaper.PriceContext{
				Symbol: perpspaper.SOL, MarkPx: strconv.Itoa(price), OraclePx: strconv.Itoa(price), ReceivedAt: bookTime,
			},
			Book: perpspaper.L2Book{Symbol: perpspaper.SOL, Time: bookTime, Levels: [2][]perpspaper.Level{
				{{Price: strconv.Itoa(price - 1), Size: "1000", Count: 1}},
				{{Price: strconv.Itoa(price + 1), Size: "1000", Count: 1}},
			}},
		}
	}
	return frames
}
