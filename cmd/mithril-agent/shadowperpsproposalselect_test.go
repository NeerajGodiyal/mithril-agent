package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

func selectableProposalFixture(t *testing.T, prices []int) (string, string, shadowPerpsTape, time.Time) {
	t.Helper()
	args, state, first, at := perpsFreezeFixture(t)
	template, _, err := readShadowPerpsCorpusTape(args[5])
	if err != nil {
		t.Fatal(err)
	}
	// Choose the fixture key using training only, before the target exists.
	key, _, _ := bestShadowPerpsPlanCandidate(t, template.Config.qualificationConfig(), template.Frames)
	input := shadowPerpsProposalInput{HypothesisID: "select-test", Symbol: perpspaper.SOL, RiskArm: key.RiskArm, Strategy: key.Strategy, Rationale: "Freeze the training-selected key before its assigned attempt."}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(args[3], raw, 0600); err != nil {
		t.Fatal(err)
	}
	known := at.Add(2 * time.Hour)
	digest := appendAutoContextTape(t, state, template, known)
	args = append(args, "--tape", filepath.Join(shadowPerpsCorpusDir(state, perpspaper.SOL), digest+".json"))
	frozenAt := known.Add(time.Second)
	if err := runShadowPerpsFreeze(args, &bytes.Buffer{}, func() time.Time { return frozenAt }); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(state), "proposals", "sol", "select-test.json")
	path, state, tape, ended := finishFrozenProposalTarget(t, path, state, first, frozenAt, prices)
	evaluateForTest(t, path, ended)
	return path, state, tape, ended
}

func selectProposalForTest(t *testing.T, path string, at time.Time) shadowPerpsPlanReceipt {
	t.Helper()
	var output bytes.Buffer
	if err := runShadowPerpsSelectProposal([]string{"--proposal", path}, &output, func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
	var receipt shadowPerpsPlanReceipt
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestPerpsSelectProposalInstallsReplaysRestoresAndRetires(t *testing.T) {
	path, state, tape, ended := selectableProposalFixture(t, shadowPerpsPlanWavePrices(3))
	proposal, _, err := readPerpsProposal(path)
	if err != nil {
		t.Fatal(err)
	}
	selected := selectProposalForTest(t, path, ended.Add(time.Second))
	if selected.Status != "qualified_paper_plan_selected" || !selected.PointerUpdated || !selected.RollbackUpdated || selected.TrainingTrials != 0 || selected.TapesChecked != 3 || selected.EvaluatedProposal == nil || selected.EvaluatedProposal.ProposalSHA256 != proposal.ContentSHA256 || selected.QualificationInputSHA256 != "" || selected.Authorized || selected.Promotable {
		t.Fatalf("selection=%+v", selected)
	}
	plan, digest, err := loadOrCreateShadowPerpsPlan(state, proposal.Baseline.Environment, proposal.Baseline.Config, perpspaper.Balanced, ended.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if plan.DecisionMode != shadowPerpsDecisionProposal || digest != selected.PlanSHA256 || plan.Key != (perpspaper.QualificationKey{RiskArm: proposal.Input.RiskArm, Strategy: proposal.Input.Strategy}) {
		t.Fatalf("plan=%+v", plan)
	}
	active := shadowPerpsActivePlanPath(state, perpspaper.SOL)
	before, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	retry := selectProposalForTest(t, path, ended.Add(3*time.Second))
	after, err := os.ReadFile(active)
	if err != nil || retry.Status != "qualified_paper_plan_already_selected" || retry.PointerUpdated || !bytes.Equal(before, after) {
		t.Fatalf("retry=%+v err=%v", retry, err)
	}
	// Independently exercise the tape consumer with the selected mode. These
	// historical frames are not represented as a new forward observation.
	tape.Config.DecisionMode, tape.Config.Strategy, tape.Config.RiskArm = plan.DecisionMode, plan.Key.Strategy, plan.Key.RiskArm
	tape.Config.PlanSHA256, tape.Config.QualificationInputSHA256 = digest, ""
	replayed, err := replayShadowPerpsTape(tape.Config, tape.Frames)
	if err != nil {
		t.Fatal(err)
	}
	want, err := perpspaper.ReplaySelected(tape.Config.replayConfig(), tape.Frames, plan.Key)
	if err != nil || !reflect.DeepEqual(replayed, want) {
		t.Fatalf("selected replay differs: %v", err)
	}
	if shadowPerpsDecisionSource(tape.Config) != "selected_paper_plan" || shadowPerpsProposalSource(tape.Config) != "frozen_proposal" || shadowPerpsCurrentStrategy(tape.Config) != string(plan.Key.Strategy) {
		t.Fatal("selected mode lost its source or strategy")
	}
	sealed, err := sealShadowPerpsTape(state, tape)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := readShadowPerpsCorpusTape(sealed); err != nil {
		t.Fatal(err)
	}
	qualification, err := perpspaper.QualifyTournament(tape.Config.qualificationConfig(), tape.Frames)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newShadowPerpsFinalizationReceipt(tape, strings.TrimSuffix(filepath.Base(sealed), ".json"), replayed, qualification, nil); err != nil {
		t.Fatal(err)
	}
	restored, err := restoreShadowPerpsPlan(state, perpspaper.SOL, ended.Add(4*time.Second))
	if err != nil || restored.PlanSHA256 != proposal.BaselineSHA256 {
		t.Fatalf("restore=%+v err=%v", restored, err)
	}
	retired := selectProposalForTest(t, path, ended.Add(5*time.Second))
	if retired.Status != "qualified_paper_plan_retired" || retired.PointerUpdated {
		t.Fatalf("retired=%+v", retired)
	}
}

func TestPerpsSelectProposalRejectsZeroTradeAndInsufficientTapes(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(map[bool]string{false: "zero_trades", true: "two_tapes"}[short], func(t *testing.T) {
			var path, state string
			var ended time.Time
			if short {
				path, state, _, ended = completedProposalTarget(t, shadowPerpsPlanWavePrices(3))
				evaluateForTest(t, path, ended)
			} else {
				prices := make([]int, 40)
				for i := range prices {
					prices[i] = 1000
				}
				path, state, _, ended = selectableProposalFixture(t, prices)
			}
			active := shadowPerpsActivePlanPath(state, perpspaper.SOL)
			before, err := os.ReadFile(active)
			if err != nil {
				t.Fatal(err)
			}
			result := selectProposalForTest(t, path, ended.Add(time.Second))
			after, err := os.ReadFile(active)
			if err != nil || result.Status != "evaluated_proposal_not_selected" || len(result.Reasons) != 1 || result.PointerUpdated || !bytes.Equal(before, after) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestPerpsSelectProposalRejectsChangedIncumbentTimeTamperAndRollbackFailure(t *testing.T) {
	for _, mode := range []string{"incumbent", "time", "evaluation", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			path, state, _, ended := selectableProposalFixture(t, shadowPerpsPlanWavePrices(3))
			proposal, _, err := readPerpsProposal(path)
			if err != nil {
				t.Fatal(err)
			}
			_, artifacts, active, previous, _ := shadowPerpsPlanPaths(state, perpspaper.SOL)
			at := ended.Add(time.Second)
			switch mode {
			case "incumbent":
				other := proposal.Baseline
				other.Key.RiskArm = perpspaper.Experimental
				raw, digest, err := canonicalShadowPerpsPlan(other)
				if err != nil {
					t.Fatal(err)
				}
				artifact := filepath.Join(artifacts, "plan-"+digest+".json")
				if err := ensureShadowPerpsPlanArtifact(artifact, raw); err != nil {
					t.Fatal(err)
				}
				if err := replaceShadowPerpsPlanPointer(active, shadowPerpsPlanPointer{Version: shadowPerpsPlanVersion, PlanPath: artifact, PlanSHA256: digest, SelectedAt: ended}); err != nil {
					t.Fatal(err)
				}
			case "time":
				at = ended.Add(-time.Nanosecond)
			case "evaluation":
				result, _ := evaluateForTest(t, path, ended)
				result.TargetEpisode = "999"
				raw, err := canonicalPerpsEvaluation(result)
				if err != nil {
					t.Fatal(err)
				}
				evaluation := filepath.Join(filepath.Dir(state), "proposal-evaluations", "sol", proposal.ContentSHA256+".json")
				if err := os.WriteFile(evaluation, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "rollback":
				if err := os.Mkdir(previous, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(active)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := runShadowPerpsSelectProposal([]string{"--proposal", path}, &output, func() time.Time { return at }); err == nil || output.Len() != 0 {
				t.Fatalf("accepted %s: %v %s", mode, err, output.String())
			}
			after, err := os.ReadFile(active)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("failed selection changed incumbent: %v", err)
			}
		})
	}
}

func TestPerpsSelectProposalRejectsUnscoredIncumbent(t *testing.T) {
	prices := make([]int, 40)
	for i := range prices {
		prices[i] = 1000 + i*10
	}
	path, state, _, at := completedProposalTarget(t, prices, func(tape *shadowPerpsTape) {
		last := &tape.Frames[len(tape.Frames)-1]
		last.Book.Levels[0][0].Size = "0.01"
		last.Book.Levels[1][0].Size = "0.01"
	})
	outcome, _ := evaluateForTest(t, path, at)
	if outcome.Status != "unevaluable" || outcome.Baseline == nil || outcome.Baseline.Eligible || outcome.Baseline.Score != nil {
		t.Fatalf("fixture did not produce an unscored incumbent: %+v", outcome)
	}
	evaluationPath := filepath.Join(filepath.Dir(state), "proposal-evaluations", "sol", outcome.ProposalSHA256+".json")
	described, err := perpsContextEvaluation(state, perpspaper.SOL, evaluationPath, true, false)
	if err != nil || described.NormalFeeBehavior == nil || described.NormalFeeBehavior.Baseline.Frames == 0 || described.Baseline.Score != nil || described.Status != "unevaluable" {
		t.Fatalf("unscored target lost frame evidence or gained a score: %+v, %v", described, err)
	}
	active := shadowPerpsActivePlanPath(state, perpspaper.SOL)
	before, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runShadowPerpsSelectProposal([]string{"--proposal", path}, &output, func() time.Time { return at.Add(time.Second) }); err == nil || output.Len() != 0 {
		t.Fatalf("unscored incumbent accepted: %v %s", err, output.String())
	}
	after, err := os.ReadFile(active)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("ineligible evidence changed selected plan: %v", err)
	}
}

func TestPerpsSelectProposalSharesStrictEconomicGate(t *testing.T) {
	_, config, at := shadowPerpsPlanFixture(t)
	key, forward, stress := bestShadowPerpsPlanCandidate(t, config, shadowPerpsPlanTestFrames(at.UnixMilli(), shadowPerpsPlanWavePrices(3)))
	if !validShadowPerpsSelectionEconomics(config, key, forward, stress) {
		t.Fatal("fixture is not passing")
	}
	for _, mode := range []string{"loss", "zero_trades", "open_position", "zero_fees", "liquidation", "drawdown"} {
		t.Run(mode, func(t *testing.T) {
			bad := stress
			score := *stress.Score
			bad.Score = &score
			switch mode {
			case "loss":
				score.NetPnLMicros = -1
			case "zero_trades":
				score.FilledOrders = 0
			case "open_position":
				score.ClosedPositions++
			case "zero_fees":
				score.FeesPaidMicros = 0
			case "liquidation":
				score.Liquidations = 1
			case "drawdown":
				score.MaxDrawdownMicros = config.StartingCollateralMicros/5 + 1
			}
			if validShadowPerpsSelectionEconomics(config, key, forward, bad) {
				t.Fatalf("accepted %s", mode)
			}
		})
	}
}

func TestPerpsSelectProposalHelpAndLegacyEncoding(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"shadow", "perps-select-proposal", "--help"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"evaluated_proposal_v1", "at least three", "Automatic invocation is opt-in", "cannot authorize"} {
		if !strings.Contains(output.String(), text) {
			t.Fatalf("help missing %q", text)
		}
	}
	state, config, at := shadowPerpsPlanFixture(t)
	plan, digest, err := loadOrCreateShadowPerpsPlan(state, perpspaper.Mainnet, config, perpspaper.Balanced, at)
	if err != nil {
		t.Fatal(err)
	}
	raw, repeated, err := canonicalShadowPerpsPlan(plan)
	if err != nil || repeated != digest || bytes.Contains(raw, []byte("evaluated_proposal")) {
		t.Fatalf("legacy encoding changed: %v", err)
	}
}
