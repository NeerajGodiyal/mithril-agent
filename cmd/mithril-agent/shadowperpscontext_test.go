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

func TestPerpsContextHelpIsRegisteredAndAdvisory(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"shadow", "perps-context", "--help"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"1–8", "at most 8", "not unseen evidence", "Pending evaluations are rejected", "cannot select a plan", "--context PATH"} {
		if !strings.Contains(output.String(), text) {
			t.Fatalf("context help missing %q", text)
		}
	}
}

func contextFixture(t *testing.T) ([]string, []string, string, time.Time) {
	t.Helper()
	freeze, state, _, at := perpsFreezeFixture(t)
	path := filepath.Join(filepath.Dir(state), "context.json")
	return []string{"--state-dir", state, "--symbol", "SOL", "--tape", freeze[5], "--out", path}, freeze, path, at
}

func createContextForTest(t *testing.T, args []string, at time.Time) shadowPerpsContext {
	t.Helper()
	var output bytes.Buffer
	if err := runShadowPerpsContext(args, &output, func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
	var context shadowPerpsContext
	if err := json.Unmarshal(output.Bytes(), &context); err != nil {
		t.Fatal(err)
	}
	return context
}

func TestPerpsContextMetricsAreVerifiedSanitizedAndImmutable(t *testing.T) {
	args, freeze, path, at := contextFixture(t)
	context := createContextForTest(t, args, at)
	tape, _, err := readShadowPerpsCorpusTape(freeze[5])
	if err != nil {
		t.Fatal(err)
	}
	qualification, err := perpspaper.QualifyTournament(tape.Config.qualificationConfig(), tape.Frames)
	if err != nil {
		t.Fatal(err)
	}
	if context.Status != "advisory_context" || !context.PaperOnly || context.Authorized || context.Promotable || !context.HistoricalScreening || len(context.Training) != 1 || len(context.Training[0].Trials) != 12 ||
		!reflect.DeepEqual(context.Training[0].Trials, qualification.Training) || context.Training[0].TrainingFrames != qualification.TrainingFrames || context.Training[0].HoldoutFrames != qualification.HoldoutFrames || !context.ContextKnownAt.Equal(at) {
		t.Fatalf("context=%+v", context)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{freeze[1], "\"candles\"", "\"levels\"", "\"rationale\"", "Test a frozen"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("context leaked %q", forbidden)
		}
	}
	var retry bytes.Buffer
	if err := runShadowPerpsContext(args, &retry, func() time.Time { t.Fatal("context retry renewed time"); return at }); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, retry.Bytes()) {
		t.Fatal("context retry changed bytes")
	}
	context.Training[0].TrainingFrames++
	changed, err := canonicalPerpsContext(context)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readPerpsContext(path, freeze[1]); err == nil {
		t.Fatal("accepted resealed false training metric")
	}
}

func TestPerpsContextRejectsBoundsUnknownAndFutureEvidence(t *testing.T) {
	for _, mode := range []string{"tape_limit", "outcome_limit", "future", "missing", "changed_selection", "wrong_state"} {
		t.Run(mode, func(t *testing.T) {
			args, freeze, path, at := contextFixture(t)
			switch mode {
			case "tape_limit":
				for i := 0; i < 8; i++ {
					args = append(args, "--tape", freeze[5])
				}
			case "outcome_limit":
				for i := 0; i < 9; i++ {
					args = append(args, "--evaluation", filepath.Join(filepath.Dir(path), "missing.json"))
				}
			case "future":
				at = at.Add(-3 * time.Second)
			case "missing":
				if err := os.Remove(freeze[5]); err != nil {
					t.Fatal(err)
				}
			case "changed_selection":
				createContextForTest(t, args, at)
				args = append(args, "--tape", freeze[5])
			case "wrong_state":
				createContextForTest(t, args, at)
				if _, _, err := readPerpsContext(path, filepath.Join(filepath.Dir(freeze[1]), "other")); err == nil {
					t.Fatal("accepted unrelated host state")
				}
				return
			}
			var output bytes.Buffer
			if err := runShadowPerpsContext(args, &output, func() time.Time { return at }); err == nil || output.Len() != 0 {
				t.Fatalf("err=%v output=%s", err, output.String())
			}
		})
	}
}

func TestPerpsContextReverifiesResolvedZeroTradeOutcomeAndKnownTime(t *testing.T) {
	prices := make([]int, 40)
	for i := range prices {
		prices[i] = 1000
	}
	proposalPath, state, _, at := completedProposalTarget(t, prices)
	result, _ := evaluateForTest(t, proposalPath, at)
	if result.Status != "evaluated" || result.Proposed.Score == nil || result.Proposed.Score.FilledOrders != 0 {
		t.Fatalf("expected zero-trade fixture: %+v", result)
	}
	proposal, _, err := readPerpsProposal(proposalPath)
	if err != nil {
		t.Fatal(err)
	}
	contextPath := filepath.Join(filepath.Dir(state), "resolved-context.json")
	evaluationPath := filepath.Join(filepath.Dir(state), "proposal-evaluations", "sol", proposal.ContentSHA256+".json")
	tapePath := filepath.Join(shadowPerpsCorpusDir(state, perpspaper.SOL), proposal.Training[0].TapeSHA256+".json")
	args := []string{"--state-dir", state, "--symbol", "SOL", "--tape", tapePath, "--evaluation", evaluationPath, "--out", contextPath}
	var output bytes.Buffer
	if err := runShadowPerpsContext(args, &output, func() time.Time { return at.Add(-time.Nanosecond) }); err == nil || !strings.Contains(err.Error(), "future resolved outcome") || output.Len() != 0 {
		t.Fatalf("future outcome err=%v output=%s", err, output.String())
	}
	context := createContextForTest(t, args, at)
	if len(context.Outcomes) != 1 || !reflect.DeepEqual(context.Outcomes[0].shadowPerpsProposalEvaluation, result) || !context.Outcomes[0].ObservedAt.Equal(at) {
		t.Fatalf("outcomes=%+v", context.Outcomes)
	}
	result.Reason = "fabricated"
	raw, err := canonicalPerpsEvaluation(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evaluationPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readPerpsContext(contextPath, state); err == nil {
		t.Fatal("accepted context backed by resealed false outcome")
	}
}

func TestPerpsContextRejectsPendingButRetainsIncompleteOutcome(t *testing.T) {
	proposalPath, state, first, at := evalFreezeFixture(t)
	proposal, _, err := readPerpsProposal(proposalPath)
	if err != nil {
		t.Fatal(err)
	}
	pending, raw := evaluateForTest(t, proposalPath, at)
	if pending.Status != "pending" {
		t.Fatal("fixture not pending")
	}
	evaluationPath := filepath.Join(filepath.Dir(state), "proposal-evaluations", "sol", proposal.ContentSHA256+".json")
	if err := os.WriteFile(evaluationPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := perpsContextEvaluation(state, perpspaper.SOL, evaluationPath); err == nil {
		t.Fatal("pending result accepted as resolved")
	}
	if err := os.Remove(evaluationPath); err != nil {
		t.Fatal(err)
	}
	if err := first.finish(state, at.Add(time.Second), false); err != nil {
		t.Fatal(err)
	}
	next, err := beginShadowPerpsEpisode(state, episodeTestConfig(), at.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := next.finish(state, at.Add(3*time.Second), false); err != nil {
		t.Fatal(err)
	}
	result, _ := evaluateForTest(t, proposalPath, at.Add(4*time.Second))
	args := []string{"--state-dir", state, "--symbol", "SOL", "--tape", filepath.Join(shadowPerpsCorpusDir(state, perpspaper.SOL), proposal.Training[0].TapeSHA256+".json"), "--evaluation", evaluationPath, "--out", filepath.Join(filepath.Dir(state), "incomplete-context.json")}
	context := createContextForTest(t, args, at.Add(5*time.Second))
	if len(context.Outcomes) != 1 || context.Outcomes[0].Status != "unevaluable" || context.Outcomes[0].Reason != "target_incomplete" || context.Outcomes[0].Proposed != nil || !reflect.DeepEqual(context.Outcomes[0].shadowPerpsProposalEvaluation, result) ||
		context.Outcomes[0].ProposedKey != (perpspaper.QualificationKey{RiskArm: proposal.Input.RiskArm, Strategy: proposal.Input.Strategy}) || context.Outcomes[0].BaselineKey != proposal.Baseline.Key || !context.Outcomes[0].ProposalFrozenAt.Equal(proposal.FrozenAt) {
		t.Fatalf("incomplete lost: %+v", context.Outcomes)
	}
}
