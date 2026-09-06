package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

const perpsSelectProposalUsage = `Usage: mithril-agent shadow perps-select-proposal --proposal PATH

Select one exact evaluated frozen proposal for the next paper invocation only.
Reverifies the original target and immutable result; never picks a later winner.
Requires at least three distinct training/target tapes, profitable completed
trades at normal and doubled fees, bounded drawdown, no liquidations, and an
unchanged baseline which the challenger improves without worsening either lane.
Uses evaluated_proposal_v1 evidence separately from tournament qualification.
Preserves the previous plan for perps-restore. Automatic invocation is opt-in
after compatible readers are deployed. It cannot authorize, sign, send or enable real trading.`

type shadowPerpsPlanProposal struct {
	Version          uint32    `json:"version"`
	ProposalSHA256   string    `json:"proposal_sha256"`
	EvaluationSHA256 string    `json:"evaluation_sha256"`
	ContextSHA256    string    `json:"context_sha256,omitempty"`
	TargetEpisode    string    `json:"target_episode"`
	StartSHA256      string    `json:"start_sha256"`
	TerminalSHA256   string    `json:"terminal_sha256"`
	FrozenAt         time.Time `json:"frozen_at"`
	ObservedAt       time.Time `json:"observed_at"`
}

func validShadowPerpsPlanProposal(proof *shadowPerpsPlanProposal) bool {
	if proof == nil || proof.Version != 1 || !validLowerSHA256(proof.ProposalSHA256) || !validLowerSHA256(proof.EvaluationSHA256) ||
		(proof.ContextSHA256 != "" && !validLowerSHA256(proof.ContextSHA256)) || !validLowerSHA256(proof.StartSHA256) || !validLowerSHA256(proof.TerminalSHA256) ||
		proof.FrozenAt.IsZero() || proof.ObservedAt.Before(proof.FrozenAt) {
		return false
	}
	id, err := strconv.ParseUint(proof.TargetEpisode, 10, 64)
	return err == nil && id > 0 && strconv.FormatUint(id, 10) == proof.TargetEpisode
}

func validShadowPerpsSelectionEconomics(config perpspaper.QualificationConfig, key perpspaper.QualificationKey, evidence ...perpspaper.QualificationEvidence) bool {
	for _, item := range evidence {
		if item.QualificationKey != key || !item.Eligible || item.Score == nil || item.Score.NetPnLMicros <= 0 || item.Score.FilledOrders == 0 ||
			item.Score.ClosedPositions != item.Score.FilledOrders || item.Score.FeesPaidMicros == 0 || item.Score.Liquidations != 0 || item.Score.MaxDrawdownMicros > config.StartingCollateralMicros/5 {
			return false
		}
	}
	return true
}

func runShadowPerpsSelectProposal(args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("shadow perps-select-proposal", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("proposal", "", "one exact host-owned frozen proposal")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, perpsSelectProposalUsage)
		}
		return err
	}
	if flags.NArg() != 0 || !cleanResearchPath(*path) {
		return errors.New("perps selection requires a clean absolute --proposal")
	}
	proposal, _, err := readPerpsProposal(*path)
	if err != nil {
		return err
	}
	expected := filepath.Join(filepath.Dir(proposal.StateDir), "proposals", strings.ToLower(string(proposal.Input.Symbol)), proposal.Input.HypothesisID+".json")
	if *path != expected {
		return errors.New("perps selection proposal is outside its host directory")
	}
	evaluationPath := filepath.Join(filepath.Dir(proposal.StateDir), "proposal-evaluations", strings.ToLower(string(proposal.Input.Symbol)), proposal.ContentSHA256+".json")
	outcome, err := perpsContextEvaluation(proposal.StateDir, proposal.Input.Symbol, evaluationPath, false, false)
	if err != nil {
		return err
	}
	if outcome.Status != "evaluated" || outcome.ProposalSHA256 != proposal.ContentSHA256 || outcome.TargetEpisode != proposal.TargetEpisode || outcome.Baseline == nil || outcome.BaselineStress == nil || outcome.Proposed == nil || outcome.ProposedStress == nil {
		return errors.New("perps proposal has no evaluated exact-target outcome")
	}
	records, err := journal.ReadDurablePrefix(proposal.EpisodeJournal, outcome.ObservedPrefix)
	if err != nil {
		return err
	}
	finalTape := ""
	for _, record := range records {
		if record.Hash != outcome.TerminalSHA256 {
			continue
		}
		var terminal shadowPerpsEpisodeEvent
		if err := strictjson.Decode(record.Payload, &terminal); err != nil {
			return err
		}
		for _, tape := range terminal.Tapes {
			if tape.Symbol == proposal.Input.Symbol {
				finalTape = tape.TapeSHA256
			}
		}
	}
	if !validLowerSHA256(finalTape) {
		return errors.New("perps proposal target has no bound final tape")
	}
	for _, training := range proposal.Training {
		if training.TapeSHA256 == finalTape {
			return errors.New("perps proposal target repeats training evidence")
		}
	}
	key := perpspaper.QualificationKey{RiskArm: proposal.Input.RiskArm, Strategy: proposal.Input.Strategy}
	proof := &shadowPerpsPlanProposal{Version: 1, ProposalSHA256: proposal.ContentSHA256, EvaluationSHA256: outcome.ContentSHA256,
		ContextSHA256: proposal.ContextSHA256, TargetEpisode: proposal.TargetEpisode, StartSHA256: outcome.StartSHA256,
		TerminalSHA256: outcome.TerminalSHA256, FrozenAt: proposal.FrozenAt, ObservedAt: outcome.ObservedAt}
	result := shadowPerpsPlanReceipt{Status: "evaluated_proposal_not_selected", PaperOnly: true, Symbol: proposal.Input.Symbol,
		RiskArm: key.RiskArm, Strategy: key.Strategy, TapesChecked: uint64(len(proposal.Training) + 1), HoldoutPlansCompared: 2,
		StatisticalConfidence: perpspaper.QualificationConfidence, EvaluatedProposal: proof}
	if result.TapesChecked < 3 {
		result.Reasons = []string{"collect_at_least_three_separate_tapes"}
	} else if !validShadowPerpsSelectionEconomics(proposal.Baseline.Config, key, *outcome.Proposed, *outcome.ProposedStress) {
		result.Reasons = []string{"evaluated_proposal_lacks_passing_forward_evidence"}
	} else {
		result.Reasons = shadowPerpsComparisonReasons(*outcome.Baseline, *outcome.BaselineStress, *outcome.Proposed, *outcome.ProposedStress)
	}
	if len(result.Reasons) != 0 {
		return json.NewEncoder(output).Encode(result)
	}
	result.HoldoutCompletedTrades = outcome.Proposed.Score.ClosedPositions
	if outcome.Baseline.Score != nil {
		result.HoldoutCompletedTrades += outcome.Baseline.Score.ClosedPositions
	}
	comparison := &shadowPerpsPlanComparison{Version: shadowPerpsComparisonVersion, Status: "challenger_outperformed_incumbent", PaperOnly: true,
		TapesChecked: result.TapesChecked, FinalTapeSHA256: finalTape, IncumbentPlanSHA256: proposal.BaselineSHA256,
		IncumbentDecisionMode: proposal.Baseline.DecisionMode, Incumbent: proposal.Baseline.Key, Challenger: key,
		IncumbentForward: *outcome.Baseline, IncumbentStress: *outcome.BaselineStress, ChallengerForward: *outcome.Proposed, ChallengerStress: *outcome.ProposedStress, Reasons: []string{}}
	plan := shadowPerpsPlan{Version: shadowPerpsPlanVersion, Status: "qualified_paper_plan", PaperOnly: true, DecisionMode: shadowPerpsDecisionProposal,
		Environment: proposal.Baseline.Environment, Config: proposal.Baseline.Config, Key: key, Comparison: comparison, EvaluatedProposal: proof}
	_, planDigest, err := canonicalShadowPerpsPlan(plan)
	if err != nil {
		return err
	}
	_, _, active, _, lock := shadowPerpsPlanPaths(proposal.StateDir, proposal.Input.Symbol)
	err = withShadowLifecycleLock(lock, func() error {
		_, currentDigest, pointer, err := loadBoundShadowPerpsPlanPointer(active, plan.Environment, plan.Config)
		if err != nil {
			return err
		}
		if currentDigest != proposal.BaselineSHA256 && currentDigest != planDigest {
			return errors.New("perps paper incumbent changed since proposal freeze")
		}
		selectedAt := now().UTC()
		if selectedAt.IsZero() || selectedAt.Before(outcome.ObservedAt) || selectedAt.Before(pointer.SelectedAt) {
			return errors.New("perps selection time precedes its evidence or current plan")
		}
		result.Comparison = comparison
		return installShadowPerpsPlan(proposal.StateDir, plan, pointer, selectedAt, &result)
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

// installShadowPerpsPlan is the shared write tail. Callers must hold the plan
// lifecycle lock and have reverified the expected incumbent and their evidence.
func installShadowPerpsPlan(state string, plan shadowPerpsPlan, current shadowPerpsPlanPointer, now time.Time, result *shadowPerpsPlanReceipt) error {
	encoded, digest, err := canonicalShadowPerpsPlan(plan)
	if err != nil {
		return err
	}
	result.RiskArm, result.Strategy, result.PlanSHA256 = plan.Key.RiskArm, plan.Key.Strategy, digest
	if digest == current.PlanSHA256 {
		result.Status, result.Effective = "qualified_paper_plan_already_selected", "current_or_next_bounded_invocation"
		return nil
	}
	if current.RestoredFromSHA256 == digest {
		result.Status = "qualified_paper_plan_retired"
		result.Reasons = []string{"same_plan_was_restored_from"}
		return nil
	}
	_, artifacts, active, previous, _ := shadowPerpsPlanPaths(state, plan.Config.Symbol)
	path := filepath.Join(artifacts, "plan-"+digest+".json")
	if err := ensureShadowPerpsPlanArtifact(path, encoded); err != nil {
		return err
	}
	rollback := shadowPerpsPlanRollbackRecord{Version: shadowPerpsPlanVersion, ReplacedByPlanSHA256: digest, PreviousPlan: current}
	raw, err := json.MarshalIndent(rollback, "", "  ")
	if err != nil {
		return err
	}
	if err := securefile.ReplacePrivate(previous, append(raw, '\n'), shadowPerpsPlanMaxBytes); err != nil {
		return errors.New("could not preserve the previous perps paper plan")
	}
	next := shadowPerpsPlanPointer{Version: shadowPerpsPlanVersion, PlanPath: path, PlanSHA256: digest,
		QualificationInputSHA256: plan.QualificationInputSHA256, SelectedAt: now.UTC()}
	if err := replaceShadowPerpsPlanPointer(active, next); err != nil {
		return errors.New("could not select the qualified perps paper plan")
	}
	result.Status = "qualified_paper_plan_selected"
	result.PointerUpdated, result.RollbackUpdated = true, true
	result.Effective = "next_bounded_invocation"
	return nil
}
