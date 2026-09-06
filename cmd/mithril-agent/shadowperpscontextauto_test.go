package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

func autoContextArgs(state, out string) []string {
	return []string{"--state-dir", state, "--symbol", "SOL", "--auto", "--out", out}
}

func appendAutoContextTape(t *testing.T, state string, template shadowPerpsTape, at time.Time) string {
	t.Helper()
	prices := make([]int, 40)
	for i := range prices {
		prices[i] = 1000
	}
	template.Frames = shadowPerpsPlanTestFrames(at.UnixMilli()-int64(len(prices))*60_000-1000, prices)
	path, err := sealShadowPerpsTape(state, template)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.TrimSuffix(filepath.Base(path), ".json")
	qualification, err := perpspaper.QualifyTournament(template.Config.qualificationConfig(), template.Frames)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := replayShadowPerpsTape(template.Config, template.Frames)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := newShadowPerpsFinalizationReceipt(template, digest, replay, qualification, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := appendShadowPerpsFinalizationReceipt(state, receipt, at); err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestPerpsContextAutoSelectsLatestJournalTapesWithoutProfitFilter(t *testing.T) {
	freeze, state, _, at := perpsFreezeFixture(t)
	template, _, err := readShadowPerpsCorpusTape(freeze[5])
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for i := 1; i <= 9; i++ {
		want = append(want, appendAutoContextTape(t, state, template, at.Add(time.Duration(i)*2*time.Hour)))
	}
	args := autoContextArgs(state, filepath.Join(filepath.Dir(state), "auto.json"))
	context := createContextForTest(t, args, at.Add(20*time.Hour))
	var got []string
	for _, tape := range context.Training {
		got = append(got, tape.TapeSHA256)
		if tape.Holdout != nil && tape.Holdout.Score != nil && tape.Holdout.Score.FilledOrders != 0 {
			t.Fatal("flat fixture unexpectedly traded")
		}
	}
	if !reflect.DeepEqual(got, want[1:]) {
		t.Fatalf("selected %v, want latest eight %v", got, want[1:])
	}
	before, err := os.ReadFile(args[len(args)-1])
	if err != nil {
		t.Fatal(err)
	}
	appendAutoContextTape(t, state, template, at.Add(22*time.Hour))
	var retry bytes.Buffer
	if err := runShadowPerpsContext(args, &retry, func() time.Time { t.Fatal("auto retry renewed context"); return at }); err != nil || !bytes.Equal(before, retry.Bytes()) {
		t.Fatalf("auto retry changed: %v", err)
	}
}

func TestPerpsContextAutoDistinguishesShortFromLostCorpus(t *testing.T) {
	for _, short := range []bool{true, false} {
		t.Run(map[bool]string{true: "known_short", false: "lost_long"}[short], func(t *testing.T) {
			prices := shadowPerpsPlanWavePrices(2)
			if short {
				prices = []int{1000, 1001, 1002}
			}
			_, state, tape, at := completedProposalTarget(t, prices)
			if !short {
				_, digest, err := canonicalShadowPerpsTape(tape)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(shadowPerpsCorpusDir(state, perpspaper.SOL), digest+".json")); err != nil {
					t.Fatal(err)
				}
			}
			args := autoContextArgs(state, filepath.Join(filepath.Dir(state), "auto.json"))
			var output bytes.Buffer
			err := runShadowPerpsContext(args, &output, func() time.Time { return at })
			if short {
				if err != nil || !bytes.Contains(output.Bytes(), []byte("target_has_insufficient_frames")) {
					t.Fatalf("short result err=%v output=%s", err, output.String())
				}
			} else if err == nil || output.Len() != 0 {
				t.Fatalf("missing long corpus accepted: %v", err)
			}
		})
	}
}

func TestPerpsContextAutoComposesFrozenTargetAndIncompleteFeedback(t *testing.T) {
	freeze, state, first, at := perpsFreezeFixture(t)
	active := shadowPerpsActivePlanPath(state, perpspaper.SOL)
	baseline, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	contextPath := filepath.Join(filepath.Dir(state), "before.json")
	context := createContextForTest(t, autoContextArgs(state, contextPath), at)
	freeze = append(freeze, "--context", contextPath)
	var frozen bytes.Buffer
	if err := runShadowPerpsFreeze(freeze, &frozen, func() time.Time { return at.Add(time.Second) }); err != nil {
		t.Fatal(err)
	}
	proposalPath := filepath.Join(filepath.Dir(state), "proposals", "sol", "test-proposal.json")
	proposal, _, err := readPerpsProposal(proposalPath)
	if err != nil || proposal.ContextSHA256 != context.ContentSHA256 || proposal.TargetEpisode != "2" || proposal.BaselineSHA256 != context.BaselineSHA256 {
		t.Fatalf("freeze association=%+v err=%v", proposal, err)
	}
	pending := createContextForTest(t, autoContextArgs(state, filepath.Join(filepath.Dir(state), "pending.json")), at.Add(2*time.Second))
	if len(pending.Outcomes) != 0 {
		t.Fatal("pending became feedback")
	}
	resultPath := filepath.Join(filepath.Dir(state), "proposal-evaluations", "sol", proposal.ContentSHA256+".json")
	if _, err := os.Stat(resultPath); !os.IsNotExist(err) {
		t.Fatalf("pending persisted: %v", err)
	}
	if err := first.finish(state, at.Add(3*time.Second), false); err != nil {
		t.Fatal(err)
	}
	target, err := beginShadowPerpsEpisode(state, episodeTestConfig(), at.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := target.finish(state, at.Add(5*time.Second), false); err != nil {
		t.Fatal(err)
	}
	feedback := createContextForTest(t, autoContextArgs(state, filepath.Join(filepath.Dir(state), "feedback.json")), at.Add(6*time.Second))
	if len(feedback.Outcomes) != 1 || feedback.Outcomes[0].TargetEpisode != "2" || feedback.Outcomes[0].Reason != "target_incomplete" || feedback.Outcomes[0].ProposalSHA256 != proposal.ContentSHA256 {
		t.Fatalf("feedback=%+v", feedback.Outcomes)
	}
	original, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	later, err := beginShadowPerpsEpisode(state, episodeTestConfig(), at.Add(7*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := later.finish(state, at.Add(8*time.Second), false); err != nil {
		t.Fatal(err)
	}
	createContextForTest(t, autoContextArgs(state, filepath.Join(filepath.Dir(state), "later.json")), at.Add(9*time.Second))
	retried, err := os.ReadFile(resultPath)
	if err != nil || !bytes.Equal(original, retried) {
		t.Fatalf("resolution retargeted or renewed: %v", err)
	}
	after, err := os.ReadFile(active)
	if err != nil || !bytes.Equal(baseline, after) {
		t.Fatalf("auto context changed active plan: %v", err)
	}
}

func TestPerpsContextAutoRejectsExplicitSelections(t *testing.T) {
	freeze, state, _, at := perpsFreezeFixture(t)
	for _, flag := range []string{"--tape", "--evaluation"} {
		args := append(autoContextArgs(state, filepath.Join(filepath.Dir(state), "invalid.json")), flag, freeze[5])
		var output bytes.Buffer
		if err := runShadowPerpsContext(args, &output, func() time.Time { return at }); err == nil || output.Len() != 0 {
			t.Fatalf("auto accepted %s: %v", flag, err)
		}
	}
}

func TestPerpsContextAutoKeepsLatestOriginalOutcomeTimes(t *testing.T) {
	freeze, state, previous, at := perpsFreezeFixture(t)
	var expected []string
	for i := 0; i < 9; i++ {
		// Reverse filename order so directory sorting cannot masquerade as
		// selection by original observation time.
		input := shadowPerpsProposalInput{HypothesisID: fmt.Sprintf("proposal-%02d", 9-i), Symbol: perpspaper.SOL, RiskArm: perpspaper.Conservative, Strategy: perpspaper.StrategyRegime, Rationale: "Offline fixed-target composition test."}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(freeze[3], raw, 0600); err != nil {
			t.Fatal(err)
		}
		chosen := at.Add(time.Duration(i) * 10 * time.Second)
		if err := runShadowPerpsFreeze(freeze, &bytes.Buffer{}, func() time.Time { return chosen }); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := previous.finish(state, chosen.Add(time.Second), false); err != nil {
				t.Fatal(err)
			}
		}
		target, err := beginShadowPerpsEpisode(state, episodeTestConfig(), chosen.Add(2*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if err := target.finish(state, chosen.Add(3*time.Second), false); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(filepath.Dir(state), "proposals", "sol", input.HypothesisID+".json")
		result, _ := evaluateForTest(t, path, chosen.Add(4*time.Second))
		expected = append(expected, result.ProposalSHA256)
	}
	context := createContextForTest(t, autoContextArgs(state, filepath.Join(filepath.Dir(state), "latest.json")), at.Add(2*time.Minute))
	var got []string
	for _, outcome := range context.Outcomes {
		got = append(got, outcome.ProposalSHA256)
		if outcome.Reason != "target_incomplete" {
			t.Fatalf("failure omitted or relabeled: %+v", outcome)
		}
	}
	if !reflect.DeepEqual(got, expected[1:]) {
		t.Fatalf("outcome order=%v want=%v", got, expected[1:])
	}
}

func TestPerpsContextAutoRetryMissingEvidenceDoesNotResolve(t *testing.T) {
	freeze, state, _, at := perpsFreezeFixture(t)
	args := autoContextArgs(state, filepath.Join(filepath.Dir(state), "original.json"))
	createContextForTest(t, args, at)
	original, err := os.ReadFile(args[len(args)-1])
	if err != nil {
		t.Fatal(err)
	}
	template, _, err := readShadowPerpsCorpusTape(freeze[5])
	if err != nil {
		t.Fatal(err)
	}
	// Keep fresh selection viable even after the original backing tape is lost.
	// Otherwise an unrelated selection failure would also satisfy this test.
	for i := 1; i <= 8; i++ {
		appendAutoContextTape(t, state, template, at.Add(time.Duration(i)*2*time.Hour))
	}
	if err := os.Remove(freeze[5]); err != nil {
		t.Fatal(err)
	}
	createContextForTest(t, autoContextArgs(state, filepath.Join(filepath.Dir(state), "fresh.json")), at.Add(20*time.Hour))
	var output bytes.Buffer
	if err := runShadowPerpsContext(args, &output, func() time.Time { t.Fatal("retry tried new resolution"); return at }); err == nil || output.Len() != 0 {
		t.Fatalf("missing original evidence accepted: %v", err)
	}
	after, err := os.ReadFile(args[len(args)-1])
	if err != nil || !bytes.Equal(original, after) {
		t.Fatalf("retry changed original context: %v", err)
	}
}

func TestPerpsContextAutoRetainsCompletedOutcomeFeedback(t *testing.T) {
	for _, losing := range []bool{false, true} {
		t.Run(map[bool]string{false: "zero_trades", true: "loss"}[losing], func(t *testing.T) {
			prices := make([]int, 40)
			for i := range prices {
				prices[i] = 1000
				if losing {
					prices[i] += i * 10
				}
			}
			if losing {
				// A rising market followed by a final adverse move realizes the
				// held long's loss; do not replace the scorer with invented P&L.
				prices[len(prices)-1] = 500
			}
			proposalPath, state, tape, ended := completedProposalTarget(t, prices)
			proposal, _, err := readPerpsProposal(proposalPath)
			if err != nil {
				t.Fatal(err)
			}
			active := shadowPerpsActivePlanPath(state, perpspaper.SOL)
			before, err := os.ReadFile(active)
			if err != nil {
				t.Fatal(err)
			}
			observed := ended.Add(time.Second)
			context := createContextForTest(t, autoContextArgs(state, filepath.Join(filepath.Dir(state), "completed-feedback.json")), observed)
			if len(context.Outcomes) != 1 {
				t.Fatalf("completed outcome omitted: %+v", context.Outcomes)
			}
			outcome := context.Outcomes[0]
			if outcome.Status != "evaluated" || outcome.Reason != "fixed_target_modeled_comparison" || outcome.ProposalSHA256 != proposal.ContentSHA256 || outcome.TargetEpisode != proposal.TargetEpisode || outcome.StartSHA256 == "" || outcome.TerminalSHA256 == "" || outcome.ContentSHA256 == "" ||
				!outcome.ProposalFrozenAt.Equal(proposal.FrozenAt) || !outcome.ObservedAt.Equal(observed) || outcome.ObservedAt.Before(ended) || context.ContextKnownAt.Before(outcome.ObservedAt) || outcome.Authorized || outcome.Promotable {
				t.Fatalf("completed identity or chronology changed: %+v", outcome)
			}
			key := perpspaper.QualificationKey{RiskArm: proposal.Input.RiskArm, Strategy: proposal.Input.Strategy}
			forward, stress, err := perpspaper.EvaluateFixedPlan(proposal.Baseline.Config, key, tape.Frames)
			if err != nil || !reflect.DeepEqual(outcome.Proposed, &forward) || !reflect.DeepEqual(outcome.ProposedStress, &stress) || outcome.ProposedKey != key || outcome.BaselineKey != proposal.Baseline.Key {
				t.Fatalf("completed metrics differ from actual replay: %v", err)
			}
			if forward.Score == nil || (losing && (forward.Score.NetPnLMicros >= 0 || forward.Score.ClosedPositions == 0)) || (!losing && forward.Score.FilledOrders != 0) {
				t.Fatalf("fixture did not produce intended completed evidence: %+v", forward)
			}
			// The evaluator must return the exact outcome auto resolution saved,
			// even when a later observation time is supplied.
			stored, _ := evaluateForTest(t, proposalPath, observed.Add(time.Hour))
			if !reflect.DeepEqual(stored, outcome.shadowPerpsProposalEvaluation) {
				t.Fatal("feedback differs from immutable terminal outcome")
			}
			after, err := os.ReadFile(active)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("completed feedback changed active plan: %v", err)
			}
		})
	}
}
