package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

func TestPerpsProposalCollectorUsesSelectedPlanThenRestoredBaseline(t *testing.T) {
	path, state, _, ended := selectableProposalFixture(t, shadowPerpsPlanWavePrices(3))
	proposal, _, err := readPerpsProposal(path)
	if err != nil {
		t.Fatal(err)
	}
	selected := selectProposalForTest(t, path, ended.Add(time.Second))
	if selected.Status != "qualified_paper_plan_selected" || !selected.PointerUpdated {
		t.Fatalf("proposal was not selected: %+v", selected)
	}
	args := []string{
		"--state-dir", state, "--archive-dir", filepath.Join(filepath.Dir(state), "runs"),
		"--symbols", "SOL", "--arm", "balanced", "--paper-usd-per-market", "100",
		"--duration", "30m", "--once",
	}
	for index, want := range []struct {
		mode   string
		digest string
		key    perpspaper.QualificationKey
	}{
		{shadowPerpsDecisionProposal, selected.PlanSHA256, perpspaper.QualificationKey{
			RiskArm: proposal.Input.RiskArm, Strategy: proposal.Input.Strategy,
		}},
		{proposal.Baseline.DecisionMode, proposal.BaselineSHA256, proposal.Baseline.Key},
	} {
		at := ended.Add(time.Duration(index+1) * time.Minute)
		if index == 1 {
			restored, err := restoreShadowPerpsPlan(state, perpspaper.SOL, at.Add(-time.Second))
			if err != nil || !restored.PointerUpdated || restored.PlanSHA256 != want.digest {
				t.Fatalf("restore baseline: %+v, %v", restored, err)
			}
		}
		reader := validStubShadowPerpsReader(at)
		var output bytes.Buffer
		err := runShadowPerpsPaperWith(t.Context(), args, &output, func() time.Time { return at },
			func(environment perpspaper.Environment) (shadowPerpsReader, error) {
				if environment != proposal.Baseline.Environment {
					t.Fatalf("collector environment = %s", environment)
				}
				return reader, nil
			})
		if err != nil {
			t.Fatalf("collector %s: %v", want.mode, err)
		}
		var status shadowPerpsStatus
		if err := json.Unmarshal(output.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if !status.PaperOnly || status.ExecutionEnabled || !status.NewFrame || status.Frames != 1 ||
			status.DecisionMode != want.mode || status.PlanSHA256 != want.digest ||
			status.Strategy != want.key.Strategy || status.RiskArm != want.key.RiskArm || reader.bookCalls != 1 {
			t.Fatalf("collector did not use %s plan: %+v", want.mode, status)
		}
		var tape shadowPerpsTape
		if err := readStrictJSON(filepath.Join(state, "sol-tape.json"), &tape); err != nil {
			t.Fatal(err)
		}
		if len(tape.Frames) != 1 || !tape.PaperOnly || tape.ExecutionEnabled ||
			tape.Config.DecisionMode != want.mode || tape.Config.PlanSHA256 != want.digest ||
			tape.Config.Strategy != want.key.Strategy || tape.Config.RiskArm != want.key.RiskArm ||
			tape.Frames[0].Context.ReceivedAt != at.UnixMilli() {
			t.Fatalf("collector tape did not bind %s plan: %+v", want.mode, tape.Config)
		}
	}
}
