package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

const (
	shadowPerpsPlanVersion       uint32 = 1
	shadowPerpsComparisonVersion uint32 = 1
	shadowPerpsPlanMaxBytes             = 64 << 10
	shadowPerpsDecisionLegacy           = "legacy_fixed_v1"
	shadowPerpsDecisionSelected         = "qualified_tournament_v1"
)

type shadowPerpsPlan struct {
	Version                  uint32                         `json:"version"`
	Status                   string                         `json:"status"`
	PaperOnly                bool                           `json:"paper_only"`
	Authorized               bool                           `json:"authorized"`
	Promotable               bool                           `json:"promotable"`
	ExecutionEnabled         bool                           `json:"execution_enabled"`
	DecisionMode             string                         `json:"decision_mode"`
	Environment              perpspaper.Environment         `json:"environment"`
	Config                   perpspaper.QualificationConfig `json:"config"`
	Key                      perpspaper.QualificationKey    `json:"key"`
	QualificationInputSHA256 string                         `json:"qualification_input_sha256,omitempty"`
	Comparison               *shadowPerpsPlanComparison     `json:"comparison,omitempty"`
}

type shadowPerpsPlanComparison struct {
	Version                  uint32                           `json:"version"`
	Status                   string                           `json:"status"`
	PaperOnly                bool                             `json:"paper_only"`
	TapesChecked             uint64                           `json:"tapes_checked"`
	FinalTapeSHA256          string                           `json:"final_tape_sha256"`
	IncumbentPlanSHA256      string                           `json:"incumbent_plan_sha256"`
	QualificationInputSHA256 string                           `json:"qualification_input_sha256"`
	IncumbentDecisionMode    string                           `json:"incumbent_decision_mode"`
	Incumbent                perpspaper.QualificationKey      `json:"incumbent"`
	Challenger               perpspaper.QualificationKey      `json:"challenger"`
	IncumbentForward         perpspaper.QualificationEvidence `json:"incumbent_forward"`
	IncumbentStress          perpspaper.QualificationEvidence `json:"incumbent_stress"`
	ChallengerForward        perpspaper.QualificationEvidence `json:"challenger_forward"`
	ChallengerStress         perpspaper.QualificationEvidence `json:"challenger_stress"`
	Reasons                  []string                         `json:"reasons"`
}

type shadowPerpsPlanPointer struct {
	Version                  uint32    `json:"version"`
	PlanPath                 string    `json:"plan_path"`
	PlanSHA256               string    `json:"plan_sha256"`
	QualificationInputSHA256 string    `json:"qualification_input_sha256,omitempty"`
	RestoredFromSHA256       string    `json:"restored_from_sha256,omitempty"`
	SelectedAt               time.Time `json:"selected_at"`
}

type shadowPerpsPlanRollbackRecord struct {
	Version              uint32                 `json:"version"`
	ReplacedByPlanSHA256 string                 `json:"replaced_by_plan_sha256"`
	PreviousPlan         shadowPerpsPlanPointer `json:"previous_plan"`
}

type shadowPerpsPlanReceipt struct {
	Status                   string                     `json:"status"`
	PaperOnly                bool                       `json:"paper_only"`
	Authorized               bool                       `json:"authorized"`
	Promotable               bool                       `json:"promotable"`
	ExecutionEnabled         bool                       `json:"execution_enabled"`
	Symbol                   perpspaper.Symbol          `json:"symbol"`
	RiskArm                  perpspaper.RiskArm         `json:"risk_arm,omitempty"`
	Strategy                 perpspaper.Strategy        `json:"strategy,omitempty"`
	PlanSHA256               string                     `json:"plan_sha256,omitempty"`
	QualificationInputSHA256 string                     `json:"qualification_input_sha256,omitempty"`
	TapesChecked             uint64                     `json:"tapes_checked"`
	PointerUpdated           bool                       `json:"pointer_updated"`
	RollbackUpdated          bool                       `json:"rollback_updated"`
	Effective                string                     `json:"effective,omitempty"`
	Reasons                  []string                   `json:"reasons,omitempty"`
	Comparison               *shadowPerpsPlanComparison `json:"comparison,omitempty"`
}

const shadowPerpsRestoreUsage = `Usage: mithril-agent shadow perps-restore --state-dir PATH --symbol SOL|BTC|ETH

Restores the previous content-addressed perps paper plan for the next bounded
experiment. It cannot authorize, sign, submit, or enable a real order.`

func shadowPerpsPlanPaths(stateDir string, symbol perpspaper.Symbol) (root, artifacts, active, previous, lock string) {
	root = filepath.Join(filepath.Dir(stateDir), "plans", strings.ToLower(string(symbol)))
	artifacts = filepath.Join(root, "artifacts")
	return root, artifacts, filepath.Join(root, "active.json"), filepath.Join(root, "previous.json"), filepath.Join(root, "lifecycle.lock")
}

func loadOrCreateShadowPerpsPlan(
	stateDir string,
	environment perpspaper.Environment,
	config perpspaper.QualificationConfig,
	arm perpspaper.RiskArm,
	now time.Time,
) (shadowPerpsPlan, string, error) {
	root, artifacts, active, _, lock := shadowPerpsPlanPaths(stateDir, config.Symbol)
	for _, directory := range []string{filepath.Dir(root), root, artifacts} {
		if err := ensureShadowPerpsPrivateDirectory(directory); err != nil {
			return shadowPerpsPlan{}, "", err
		}
	}
	var plan shadowPerpsPlan
	var digest string
	err := withShadowLifecycleLock(lock, func() error {
		var err error
		plan, digest, _, err = loadBoundShadowPerpsPlanPointer(active, environment, config)
		if err == nil {
			if plan.DecisionMode == shadowPerpsDecisionLegacy && plan.Key.RiskArm != arm {
				return errors.New("configured perps paper baseline identity does not match --arm")
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		plan = shadowPerpsPlan{
			Version: shadowPerpsPlanVersion, Status: "configured_paper_baseline",
			PaperOnly: true, DecisionMode: shadowPerpsDecisionLegacy,
			Environment: environment, Config: config,
			Key: perpspaper.QualificationKey{RiskArm: arm},
		}
		encoded, planDigest, err := canonicalShadowPerpsPlan(plan)
		if err != nil {
			return err
		}
		path := filepath.Join(artifacts, "plan-"+planDigest+".json")
		if err := ensureShadowPerpsPlanArtifact(path, encoded); err != nil {
			return err
		}
		pointer := shadowPerpsPlanPointer{
			Version: shadowPerpsPlanVersion, PlanPath: path, PlanSHA256: planDigest,
			SelectedAt: now.UTC(),
		}
		if err := replaceShadowPerpsPlanPointer(active, pointer); err != nil {
			return err
		}
		digest = planDigest
		return nil
	})
	return plan, digest, err
}

func canonicalShadowPerpsPlan(plan shadowPerpsPlan) ([]byte, string, error) {
	if err := validateShadowPerpsPlan(plan); err != nil {
		return nil, "", err
	}
	encoded, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, "", err
	}
	encoded = append(encoded, '\n')
	sum := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(sum[:]), nil
}

func validateShadowPerpsPlan(plan shadowPerpsPlan) error {
	if plan.Version != shadowPerpsPlanVersion || !plan.PaperOnly || plan.Authorized ||
		plan.Promotable || plan.ExecutionEnabled ||
		(plan.Environment != perpspaper.Mainnet && plan.Environment != perpspaper.Testnet) ||
		plan.Config.Symbol != perpspaper.SOL && plan.Config.Symbol != perpspaper.BTC && plan.Config.Symbol != perpspaper.ETH ||
		plan.Config.StartingCollateralMicros == 0 ||
		plan.Config.StartingCollateralMicros > perpspaper.MaxStartingCollateralMicros ||
		plan.Config.VenueMaxLeverage == 0 ||
		!validShadowPerpsRiskArm(plan.Key.RiskArm) {
		return errors.New("perps paper plan is invalid")
	}
	switch plan.DecisionMode {
	case shadowPerpsDecisionLegacy:
		if plan.Status != "configured_paper_baseline" || plan.Key.Strategy != "" ||
			plan.QualificationInputSHA256 != "" || plan.Comparison != nil {
			return errors.New("perps paper baseline plan is invalid")
		}
	case shadowPerpsDecisionSelected:
		if plan.Status != "qualified_paper_plan" || !validShadowPerpsStrategy(plan.Key.Strategy) ||
			!validLowerSHA256(plan.QualificationInputSHA256) ||
			!validShadowPerpsPlanComparison(plan, plan.Comparison) {
			return errors.New("qualified perps paper plan is invalid")
		}
	default:
		return errors.New("perps paper decision mode is invalid")
	}
	return nil
}

func validShadowPerpsRiskArm(arm perpspaper.RiskArm) bool {
	return arm == perpspaper.Conservative || arm == perpspaper.Balanced || arm == perpspaper.Experimental
}

func validShadowPerpsStrategy(strategy perpspaper.Strategy) bool {
	return strategy == perpspaper.StrategyMomentum || strategy == perpspaper.StrategyMeanReversion ||
		strategy == perpspaper.StrategyBreakout || strategy == perpspaper.StrategyRegime
}

func validShadowPerpsPlanComparison(plan shadowPerpsPlan, comparison *shadowPerpsPlanComparison) bool {
	if comparison == nil || comparison.Version != shadowPerpsComparisonVersion ||
		comparison.Status != "challenger_outperformed_incumbent" || !comparison.PaperOnly ||
		comparison.TapesChecked < 3 || !validLowerSHA256(comparison.FinalTapeSHA256) ||
		!validLowerSHA256(comparison.IncumbentPlanSHA256) ||
		comparison.QualificationInputSHA256 != plan.QualificationInputSHA256 ||
		comparison.Challenger != plan.Key || len(comparison.Reasons) != 0 ||
		!validShadowPerpsComparisonKey(comparison.IncumbentDecisionMode, comparison.Incumbent) ||
		!validShadowPerpsComparisonEvidence(comparison.IncumbentForward, comparison.Incumbent, false) ||
		!validShadowPerpsComparisonEvidence(comparison.IncumbentStress, comparison.Incumbent, true) ||
		!validShadowPerpsComparisonEvidence(comparison.ChallengerForward, comparison.Challenger, false) ||
		!validShadowPerpsComparisonEvidence(comparison.ChallengerStress, comparison.Challenger, true) {
		return false
	}
	return len(shadowPerpsComparisonReasons(
		comparison.IncumbentForward, comparison.IncumbentStress,
		comparison.ChallengerForward, comparison.ChallengerStress,
	)) == 0
}

func validShadowPerpsComparisonKey(mode string, key perpspaper.QualificationKey) bool {
	switch mode {
	case shadowPerpsDecisionLegacy:
		return key.Strategy == "" && validShadowPerpsRiskArm(key.RiskArm)
	case shadowPerpsDecisionSelected:
		return validShadowPerpsRiskArm(key.RiskArm) && validShadowPerpsStrategy(key.Strategy)
	default:
		return false
	}
}

func validShadowPerpsComparisonEvidence(evidence perpspaper.QualificationEvidence, key perpspaper.QualificationKey, stress bool) bool {
	if evidence.QualificationKey != key || stress != (evidence.StressRule != "") {
		return false
	}
	return !evidence.Eligible && evidence.Score == nil || evidence.Eligible && evidence.Score != nil
}

func ensureShadowPerpsPlanArtifact(path string, encoded []byte) error {
	if existing, err := securefile.ReadPrivate(path, shadowPerpsPlanMaxBytes); err == nil {
		if !bytes.Equal(existing, encoded) {
			return errors.New("perps paper plan digest collision")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("could not inspect the perps paper plan artifact")
	}
	if err := securefile.CreatePrivate(path, encoded, shadowPerpsPlanMaxBytes); err == nil {
		return nil
	}
	existing, err := securefile.ReadPrivate(path, shadowPerpsPlanMaxBytes)
	if err != nil || !bytes.Equal(existing, encoded) {
		return errors.New("could not create the perps paper plan artifact")
	}
	return nil
}

func replaceShadowPerpsPlanPointer(path string, pointer shadowPerpsPlanPointer) error {
	encoded, err := json.MarshalIndent(pointer, "", "  ")
	if err != nil {
		return err
	}
	return securefile.ReplacePrivate(path, append(encoded, '\n'), shadowPerpsPlanMaxBytes)
}

func loadBoundShadowPerpsPlanPointer(
	path string,
	environment perpspaper.Environment,
	config perpspaper.QualificationConfig,
) (shadowPerpsPlan, string, shadowPerpsPlanPointer, error) {
	raw, err := securefile.ReadPrivate(path, shadowPerpsPlanMaxBytes)
	if err != nil {
		return shadowPerpsPlan{}, "", shadowPerpsPlanPointer{}, err
	}
	var pointer shadowPerpsPlanPointer
	if err := strictjson.Decode(raw, &pointer); err != nil || !validShadowPerpsPlanPointer(pointer, path) {
		return shadowPerpsPlan{}, "", shadowPerpsPlanPointer{}, errors.New("perps paper plan pointer is invalid")
	}
	planRaw, err := securefile.ReadPrivate(pointer.PlanPath, shadowPerpsPlanMaxBytes)
	if err != nil {
		return shadowPerpsPlan{}, "", shadowPerpsPlanPointer{}, errors.New("could not read the selected perps paper plan")
	}
	var plan shadowPerpsPlan
	if err := strictjson.Decode(planRaw, &plan); err != nil {
		return shadowPerpsPlan{}, "", shadowPerpsPlanPointer{}, errors.New("selected perps paper plan JSON is invalid")
	}
	canonical, digest, err := canonicalShadowPerpsPlan(plan)
	if err != nil || !bytes.Equal(canonical, planRaw) || digest != pointer.PlanSHA256 ||
		plan.QualificationInputSHA256 != pointer.QualificationInputSHA256 ||
		plan.Environment != environment || plan.Config != config {
		return shadowPerpsPlan{}, "", shadowPerpsPlanPointer{}, errors.New("perps paper plan pointer no longer matches its artifact or runtime")
	}
	return plan, digest, pointer, nil
}

func validShadowPerpsPlanPointer(pointer shadowPerpsPlanPointer, activePath string) bool {
	return pointer.Version == shadowPerpsPlanVersion && validLowerSHA256(pointer.PlanSHA256) &&
		(pointer.QualificationInputSHA256 == "" || validLowerSHA256(pointer.QualificationInputSHA256)) &&
		(pointer.RestoredFromSHA256 == "" || validLowerSHA256(pointer.RestoredFromSHA256)) &&
		!pointer.SelectedAt.IsZero() && pointer.SelectedAt.Location() == time.UTC &&
		filepath.IsAbs(pointer.PlanPath) && filepath.Clean(pointer.PlanPath) == pointer.PlanPath &&
		filepath.Dir(pointer.PlanPath) == filepath.Join(filepath.Dir(activePath), "artifacts") &&
		filepath.Base(pointer.PlanPath) == "plan-"+pointer.PlanSHA256+".json"
}

func selectQualifiedShadowPerpsPlan(
	stateDir string,
	environment perpspaper.Environment,
	currentPlanSHA256 string,
	qualification perpspaper.WalkForwardQualification,
	now time.Time,
) (shadowPerpsPlanReceipt, error) {
	result := shadowPerpsPlanReceipt{
		Status: "qualification_not_selected", PaperOnly: true,
		Symbol: qualification.Config.Symbol, QualificationInputSHA256: qualification.InputSHA256,
		TapesChecked: uint64(len(qualification.Tapes)),
	}
	if err := validateShadowPerpsWalkForwardCandidate(qualification); err != nil {
		result.Reasons = []string{err.Error()}
		return result, nil
	}
	key := *qualification.Candidate
	result.RiskArm, result.Strategy = key.RiskArm, key.Strategy
	if len(qualification.Tapes) < 3 {
		result.Status = "qualification_research_only"
		result.Reasons = []string{"collect_at_least_three_separate_tapes"}
		return result, nil
	}
	root, artifacts, active, previous, lock := shadowPerpsPlanPaths(stateDir, qualification.Config.Symbol)
	for _, directory := range []string{filepath.Dir(root), root, artifacts} {
		if err := ensureShadowPerpsPrivateDirectory(directory); err != nil {
			return result, err
		}
	}
	finalTape, finalTapeSHA256, err := loadShadowPerpsComparisonTape(stateDir, environment, qualification)
	if err != nil {
		return result, err
	}
	challengerForward, challengerStress, err := perpspaper.EvaluateFixedPlan(
		qualification.Config, key, finalTape.Frames,
	)
	if err != nil {
		return result, fmt.Errorf("replay challenger on final held-out tape: %w", err)
	}
	err = withShadowLifecycleLock(lock, func() error {
		current, currentDigest, currentPointer, err := loadBoundShadowPerpsPlanPointer(active, environment, qualification.Config)
		if err != nil || currentDigest != currentPlanSHA256 {
			return errors.New("perps paper plan changed during the bounded experiment")
		}
		if current.Key == key && current.QualificationInputSHA256 == qualification.InputSHA256 {
			result.Status = "qualified_paper_plan_already_selected"
			result.PlanSHA256 = currentDigest
			result.Effective = "current_or_next_bounded_invocation"
			return nil
		}
		incumbentForward, incumbentStress, err := perpspaper.EvaluateFixedPlan(
			qualification.Config, current.Key, finalTape.Frames,
		)
		if err != nil {
			return fmt.Errorf("replay incumbent on final held-out tape: %w", err)
		}
		comparison := shadowPerpsPlanComparison{
			Version: shadowPerpsComparisonVersion, Status: "challenger_not_selected", PaperOnly: true,
			TapesChecked: uint64(len(qualification.Tapes)), FinalTapeSHA256: finalTapeSHA256,
			IncumbentPlanSHA256: currentDigest, QualificationInputSHA256: qualification.InputSHA256,
			IncumbentDecisionMode: current.DecisionMode, Incumbent: current.Key, Challenger: key,
			IncumbentForward: incumbentForward, IncumbentStress: incumbentStress,
			ChallengerForward: challengerForward, ChallengerStress: challengerStress,
			Reasons: []string{},
		}
		if !equalShadowPerpsEvidence(challengerForward, *qualification.Forward) ||
			!equalShadowPerpsEvidence(challengerStress, *qualification.Stress) {
			comparison.Reasons = append(comparison.Reasons, "challenger_evidence_does_not_match_final_tape")
		}
		if len(comparison.Reasons) == 0 {
			comparison.Reasons = shadowPerpsComparisonReasons(
				incumbentForward, incumbentStress, challengerForward, challengerStress,
			)
		}
		result.Comparison = &comparison
		if len(comparison.Reasons) != 0 {
			result.Status = comparison.Status
			result.Reasons = append([]string(nil), comparison.Reasons...)
			return nil
		}
		comparison.Status = "challenger_outperformed_incumbent"
		plan := shadowPerpsPlan{
			Version: shadowPerpsPlanVersion, Status: "qualified_paper_plan",
			PaperOnly: true, DecisionMode: shadowPerpsDecisionSelected,
			Environment: current.Environment, Config: qualification.Config, Key: key,
			QualificationInputSHA256: qualification.InputSHA256, Comparison: &comparison,
		}
		encoded, digest, err := canonicalShadowPerpsPlan(plan)
		if err != nil {
			return err
		}
		result.RiskArm, result.Strategy, result.PlanSHA256 = key.RiskArm, key.Strategy, digest
		if digest == currentDigest {
			result.Status = "qualified_paper_plan_already_selected"
			result.Effective = "current_or_next_bounded_invocation"
			return nil
		}
		if currentPointer.RestoredFromSHA256 == digest {
			result.Status = "qualified_paper_plan_retired"
			result.Reasons = []string{"same_plan_was_restored_from"}
			return nil
		}
		path := filepath.Join(artifacts, "plan-"+digest+".json")
		if err := ensureShadowPerpsPlanArtifact(path, encoded); err != nil {
			return err
		}
		rollback := shadowPerpsPlanRollbackRecord{
			Version: shadowPerpsPlanVersion, ReplacedByPlanSHA256: digest, PreviousPlan: currentPointer,
		}
		rollbackRaw, err := json.MarshalIndent(rollback, "", "  ")
		if err != nil {
			return err
		}
		if err := securefile.ReplacePrivate(previous, append(rollbackRaw, '\n'), shadowPerpsPlanMaxBytes); err != nil {
			return errors.New("could not preserve the previous perps paper plan")
		}
		next := shadowPerpsPlanPointer{
			Version: shadowPerpsPlanVersion, PlanPath: path, PlanSHA256: digest,
			QualificationInputSHA256: qualification.InputSHA256, SelectedAt: now.UTC(),
		}
		if err := replaceShadowPerpsPlanPointer(active, next); err != nil {
			return errors.New("could not select the qualified perps paper plan")
		}
		result.Status = "qualified_paper_plan_selected"
		result.PointerUpdated, result.RollbackUpdated = true, true
		result.Effective = "next_bounded_invocation"
		return nil
	})
	return result, err
}

func validateShadowPerpsWalkForwardCandidate(result perpspaper.WalkForwardQualification) error {
	if result.Version != perpspaper.WalkForwardVersion || result.Status != "research_only" ||
		result.Outcome != "candidate_ready_for_more_paper_testing" || !result.PaperOnly ||
		result.Authorized || result.Promotable || !result.EligibleForPaperExperiment ||
		!validLowerSHA256(result.InputSHA256) || result.Candidate == nil ||
		result.TrainingLeader == nil || *result.Candidate != *result.TrainingLeader ||
		!validShadowPerpsRiskArm(result.Candidate.RiskArm) ||
		!validShadowPerpsStrategy(result.Candidate.Strategy) ||
		result.Forward == nil || result.Stress == nil || len(result.Tapes) < 2 || len(result.Reasons) != 0 {
		return errors.New("walk-forward result is not qualified for another paper experiment")
	}
	seen := make(map[string]bool, len(result.Tapes))
	var previousLast int64
	for index, tape := range result.Tapes {
		if !validLowerSHA256(tape.ContentSHA256) || !validLowerSHA256(tape.ReplayInputSHA256) ||
			tape.Frames == 0 || tape.FirstTime <= 0 || tape.LastTime <= tape.FirstTime ||
			seen[tape.ContentSHA256] || index > 0 && tape.FirstTime <= previousLast {
			return errors.New("walk-forward result has invalid tape evidence")
		}
		seen[tape.ContentSHA256] = true
		previousLast = tape.LastTime
	}
	for _, evidence := range []*perpspaper.QualificationEvidence{result.Forward, result.Stress} {
		if evidence.QualificationKey != *result.Candidate || !evidence.Eligible || evidence.Score == nil ||
			evidence.Score.NetPnLMicros <= 0 || evidence.Score.FilledOrders == 0 ||
			evidence.Score.ClosedPositions != evidence.Score.FilledOrders ||
			evidence.Score.FeesPaidMicros == 0 || evidence.Score.Liquidations != 0 ||
			evidence.Score.MaxDrawdownMicros > result.Config.StartingCollateralMicros/5 {
			return errors.New("walk-forward result lacks passing forward evidence")
		}
	}
	return nil
}

func loadShadowPerpsComparisonTape(
	stateDir string,
	environment perpspaper.Environment,
	qualification perpspaper.WalkForwardQualification,
) (shadowPerpsTape, string, error) {
	evidence := qualification.Tapes[len(qualification.Tapes)-1]
	path := filepath.Join(shadowPerpsCorpusDir(stateDir, qualification.Config.Symbol), evidence.ContentSHA256+".json")
	tape, digest, err := readShadowPerpsCorpusTape(path)
	if err != nil {
		return shadowPerpsTape{}, "", fmt.Errorf("read final held-out perps paper tape: %w", err)
	}
	if digest != evidence.ContentSHA256 || tape.Config.Environment != environment ||
		tape.Config.qualificationConfig() != qualification.Config || uint64(len(tape.Frames)) != evidence.Frames ||
		tape.Frames[0].Book.Time != evidence.FirstTime || tape.Frames[len(tape.Frames)-1].Book.Time != evidence.LastTime {
		return shadowPerpsTape{}, "", errors.New("final held-out tape does not match walk-forward evidence")
	}
	verified, err := perpspaper.QualifyTournament(qualification.Config, tape.Frames)
	if err != nil || verified.InputSHA256 != evidence.ReplayInputSHA256 {
		return shadowPerpsTape{}, "", errors.New("final held-out replay digest does not match walk-forward evidence")
	}
	return tape, digest, nil
}

func equalShadowPerpsEvidence(left, right perpspaper.QualificationEvidence) bool {
	if left.QualificationKey != right.QualificationKey || left.StressRule != right.StressRule ||
		left.Eligible != right.Eligible || left.IneligibleReason != right.IneligibleReason ||
		(left.Score == nil) != (right.Score == nil) {
		return false
	}
	return left.Score == nil || *left.Score == *right.Score
}

func shadowPerpsComparisonReasons(
	incumbentForward, incumbentStress, challengerForward, challengerStress perpspaper.QualificationEvidence,
) []string {
	forward := compareShadowPerpsEvidence(challengerForward, incumbentForward)
	stress := compareShadowPerpsEvidence(challengerStress, incumbentStress)
	reasons := []string{}
	if forward < 0 {
		reasons = append(reasons, "challenger_underperformed_incumbent_forward")
	}
	if stress < 0 {
		reasons = append(reasons, "challenger_underperformed_incumbent_fee_stress")
	}
	if forward == 0 && stress == 0 {
		reasons = append(reasons, "challenger_did_not_improve_incumbent")
	}
	return reasons
}

func compareShadowPerpsEvidence(left, right perpspaper.QualificationEvidence) int {
	if left.Eligible != right.Eligible {
		if left.Eligible {
			return 1
		}
		return -1
	}
	if left.Score == nil || right.Score == nil {
		if left.Score != nil {
			return 1
		}
		if right.Score != nil {
			return -1
		}
		return 0
	}
	if left.Score.NetPnLMicros != right.Score.NetPnLMicros {
		if left.Score.NetPnLMicros > right.Score.NetPnLMicros {
			return 1
		}
		return -1
	}
	if left.Score.MaxDrawdownMicros != right.Score.MaxDrawdownMicros {
		if left.Score.MaxDrawdownMicros < right.Score.MaxDrawdownMicros {
			return 1
		}
		return -1
	}
	if left.Score.Liquidations != right.Score.Liquidations {
		if left.Score.Liquidations < right.Score.Liquidations {
			return 1
		}
		return -1
	}
	return 0
}

func runShadowPerpsRestore(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow perps-restore", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", "", "private perps paper state directory")
	symbolText := flags.String("symbol", "", "SOL, BTC, or ETH")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowPerpsRestoreUsage)
			return writeErr
		}
		return err
	}
	symbol := perpspaper.Symbol(*symbolText)
	if flags.NArg() != 0 || !absoluteClean(*stateDir) ||
		symbol != perpspaper.SOL && symbol != perpspaper.BTC && symbol != perpspaper.ETH {
		return errors.New("shadow perps-restore requires a clean absolute --state-dir and one supported --symbol")
	}
	result, err := restoreShadowPerpsPlan(*stateDir, symbol, time.Now().UTC())
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

func restoreShadowPerpsPlan(stateDir string, symbol perpspaper.Symbol, now time.Time) (shadowPerpsPlanReceipt, error) {
	result := shadowPerpsPlanReceipt{Status: "perps_paper_plan_already_restored", PaperOnly: true, Symbol: symbol}
	_, _, active, previous, lock := shadowPerpsPlanPaths(stateDir, symbol)
	err := withShadowLifecycleLock(lock, func() error {
		raw, err := securefile.ReadPrivate(previous, shadowPerpsPlanMaxBytes)
		if err != nil {
			return errors.New("preserved perps paper plan is invalid")
		}
		var rollback shadowPerpsPlanRollbackRecord
		if strictjson.Decode(raw, &rollback) != nil || rollback.Version != shadowPerpsPlanVersion ||
			!validLowerSHA256(rollback.ReplacedByPlanSHA256) ||
			rollback.ReplacedByPlanSHA256 == rollback.PreviousPlan.PlanSHA256 ||
			!validShadowPerpsPlanPointer(rollback.PreviousPlan, active) {
			return errors.New("preserved perps paper plan is invalid")
		}
		planRaw, err := securefile.ReadPrivate(rollback.PreviousPlan.PlanPath, shadowPerpsPlanMaxBytes)
		if err != nil {
			return errors.New("preserved perps paper plan artifact is invalid")
		}
		var plan shadowPerpsPlan
		if strictjson.Decode(planRaw, &plan) != nil {
			return errors.New("preserved perps paper plan artifact is invalid")
		}
		canonical, previousDigest, err := canonicalShadowPerpsPlan(plan)
		if err != nil || !bytes.Equal(canonical, planRaw) ||
			previousDigest != rollback.PreviousPlan.PlanSHA256 || plan.Config.Symbol != symbol {
			return errors.New("preserved perps paper plan artifact is invalid")
		}
		_, currentDigest, current, err := loadBoundShadowPerpsPlanPointer(
			active, plan.Environment, plan.Config,
		)
		if err != nil {
			return errors.New("current perps paper plan is invalid")
		}
		result.PlanSHA256, result.RiskArm, result.Strategy = previousDigest, plan.Key.RiskArm, plan.Key.Strategy
		if currentDigest == previousDigest && current.RestoredFromSHA256 == rollback.ReplacedByPlanSHA256 {
			return nil
		}
		if currentDigest != rollback.ReplacedByPlanSHA256 {
			return errors.New("preserved perps paper plan does not apply to the current selection")
		}
		restored := rollback.PreviousPlan
		restored.RestoredFromSHA256 = rollback.ReplacedByPlanSHA256
		restored.SelectedAt = now.UTC()
		if err := replaceShadowPerpsPlanPointer(active, restored); err != nil {
			return errors.New("could not restore the previous perps paper plan")
		}
		result.Status = "perps_paper_plan_restored"
		result.PointerUpdated = true
		result.Effective = "next_bounded_invocation"
		return nil
	})
	return result, err
}
