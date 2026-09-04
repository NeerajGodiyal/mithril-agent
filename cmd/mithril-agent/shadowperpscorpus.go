package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

type shadowPerpsResearchError struct {
	err error
}

const (
	shadowPerpsFinalizationReceiptVersion = uint32(1)
	shadowPerpsFinalizationEvent          = "perps.finalized_evaluated"
	shadowPerpsFinalizationEvaluator      = "single_tape_qualification"
)

// shadowPerpsFinalizationReceipt commits every current-format final tape and
// its authoritative qualification. Optional walk-forward evidence is bound by
// digest without storing paths, scores, or human-facing reasons.
type shadowPerpsFinalizationReceipt struct {
	Version                      uint32                       `json:"version"`
	Status                       string                       `json:"status"`
	PaperOnly                    bool                         `json:"paper_only"`
	Authorized                   bool                         `json:"authorized"`
	Promotable                   bool                         `json:"promotable"`
	Evaluator                    string                       `json:"evaluator"`
	EvaluatorVersion             uint32                       `json:"evaluator_version"`
	Environment                  perpspaper.Environment       `json:"environment"`
	Symbol                       perpspaper.Symbol            `json:"symbol"`
	FinalTapeSHA256              string                       `json:"final_tape_sha256"`
	SingleQualificationSHA256    string                       `json:"single_qualification_sha256"`
	SingleResultSHA256           string                       `json:"single_result_sha256"`
	IncumbentPlanSHA256          string                       `json:"incumbent_plan_sha256"`
	IncumbentDecisionMode        string                       `json:"incumbent_decision_mode"`
	Incumbent                    perpspaper.QualificationKey  `json:"incumbent"`
	IncumbentQualificationSHA256 string                       `json:"incumbent_qualification_sha256,omitempty"`
	IncumbentReplayResultSHA256  string                       `json:"incumbent_replay_result_sha256"`
	WalkForwardInputSHA256       string                       `json:"walk_forward_input_sha256,omitempty"`
	WalkForwardResultSHA256      string                       `json:"walk_forward_result_sha256,omitempty"`
	WalkForwardOutcome           string                       `json:"walk_forward_outcome,omitempty"`
	TrainingLeader               *perpspaper.QualificationKey `json:"training_leader,omitempty"`
	TrainingTrials               uint64                       `json:"training_trials,omitempty"`
	HoldoutPlansCompared         uint64                       `json:"holdout_plans_compared,omitempty"`
	HoldoutEvaluated             bool                         `json:"holdout_evaluated,omitempty"`
	HoldoutCompletedTrades       uint64                       `json:"holdout_completed_trades,omitempty"`
	StatisticalConfidence        string                       `json:"statistical_confidence,omitempty"`
}

func (err *shadowPerpsResearchError) Error() string { return err.err.Error() }
func (err *shadowPerpsResearchError) Unwrap() error { return err.err }

func shadowPerpsFinalizationJournalPath(stateDir string, symbol perpspaper.Symbol) string {
	return filepath.Join(filepath.Dir(stateDir), strings.ToLower(string(symbol))+"-finalizations.jsonl")
}

func evaluateAndRecordShadowPerpsFinalization(
	stateDir string,
	tape shadowPerpsTape,
	tapeSHA256 string,
	replay perpspaper.TapeReplay,
	qualification perpspaper.Qualification,
	at time.Time,
) (walkForward *perpspaper.WalkForwardQualification, count uint64, appended bool, err error) {
	if qualification.Frames >= qualification.MinimumFrames {
		walkForward, err = qualifyShadowPerpsCorpus(stateDir, tape.Config)
		if err != nil {
			return nil, 0, false, err
		}
	}
	receipt, err := newShadowPerpsFinalizationReceipt(tape, tapeSHA256, replay, qualification, walkForward)
	if err != nil {
		return nil, 0, false, err
	}
	count, appended, err = appendShadowPerpsFinalizationReceipt(stateDir, receipt, at)
	return walkForward, count, appended, err
}

func appendShadowPerpsFinalizationReceipt(
	stateDir string,
	receipt shadowPerpsFinalizationReceipt,
	at time.Time,
) (count uint64, appended bool, err error) {
	store, err := journal.OpenRotating(shadowPerpsFinalizationJournalPath(stateDir, receipt.Symbol))
	if err != nil {
		return 0, false, err
	}
	defer func() {
		if closeErr := store.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	receipts, err := foldShadowPerpsFinalizationReceipts(store.Records())
	if err != nil {
		return 0, false, err
	}
	canonical, err := json.Marshal(receipt)
	if err != nil {
		return 0, false, errors.New("could not encode perps finalization receipt")
	}
	for _, existing := range receipts {
		if existing.FinalTapeSHA256 != receipt.FinalTapeSHA256 {
			continue
		}
		encoded, marshalErr := json.Marshal(existing)
		if marshalErr == nil && bytes.Equal(encoded, canonical) {
			return uint64(len(receipts)), false, nil
		}
		return 0, false, errors.New("perps finalization receipt collision")
	}
	if at.IsZero() || at.Location() != time.UTC {
		return 0, false, errors.New("perps finalization receipt time must be UTC")
	}
	if _, err := store.Append(at, shadowPerpsFinalizationEvent, receipt.FinalTapeSHA256, receipt); err != nil {
		return 0, false, err
	}
	return uint64(len(receipts) + 1), true, nil
}

func newShadowPerpsFinalizationReceipt(
	tape shadowPerpsTape,
	tapeSHA256 string,
	replay perpspaper.TapeReplay,
	qualification perpspaper.Qualification,
	walkForward *perpspaper.WalkForwardQualification,
) (shadowPerpsFinalizationReceipt, error) {
	config := tape.Config.qualificationConfig()
	_, canonicalTapeSHA256, err := canonicalShadowPerpsTape(tape)
	if err != nil || tape.Version != shadowPerpsTapeVersion || tape.Config.Symbol != qualification.Config.Symbol ||
		config != qualification.Config || tapeSHA256 != canonicalTapeSHA256 || !validLowerSHA256(tape.Config.PlanSHA256) ||
		!validShadowPerpsComparisonKey(tape.Config.DecisionMode, perpspaper.QualificationKey{
			RiskArm: tape.Config.RiskArm, Strategy: tape.Config.Strategy,
		}) {
		return shadowPerpsFinalizationReceipt{}, errors.New("perps finalization lineage is invalid")
	}
	verifiedQualification, err := perpspaper.QualifyTournament(config, tape.Frames)
	qualificationDigest, digestErr := shadowPerpsJSONSHA256(qualification)
	verifiedQualificationDigest, verifiedDigestErr := shadowPerpsJSONSHA256(verifiedQualification)
	if err != nil || digestErr != nil || verifiedDigestErr != nil ||
		qualificationDigest != verifiedQualificationDigest || qualification.InputSHA256 != verifiedQualification.InputSHA256 {
		return shadowPerpsFinalizationReceipt{}, errors.New("perps finalization qualification is invalid")
	}
	verifiedReplay, err := replayShadowPerpsTape(tape.Config, tape.Frames)
	if err != nil {
		return shadowPerpsFinalizationReceipt{}, errors.New("perps finalization incumbent replay is invalid")
	}
	replayDigest, err := shadowPerpsJSONSHA256(replay)
	if err != nil {
		return shadowPerpsFinalizationReceipt{}, err
	}
	verifiedReplayDigest, err := shadowPerpsJSONSHA256(verifiedReplay)
	if err != nil || replayDigest != verifiedReplayDigest {
		return shadowPerpsFinalizationReceipt{}, errors.New("perps finalization incumbent replay is invalid")
	}
	receipt := shadowPerpsFinalizationReceipt{
		Version: shadowPerpsFinalizationReceiptVersion, Status: "finalized_evaluated",
		PaperOnly: true, Evaluator: shadowPerpsFinalizationEvaluator,
		EvaluatorVersion: perpspaper.QualificationVersion,
		Environment:      tape.Config.Environment, Symbol: tape.Config.Symbol,
		FinalTapeSHA256:              tapeSHA256,
		SingleQualificationSHA256:    qualification.InputSHA256,
		SingleResultSHA256:           qualificationDigest,
		IncumbentPlanSHA256:          tape.Config.PlanSHA256,
		IncumbentDecisionMode:        tape.Config.DecisionMode,
		Incumbent:                    perpspaper.QualificationKey{RiskArm: tape.Config.RiskArm, Strategy: tape.Config.Strategy},
		IncumbentQualificationSHA256: tape.Config.QualificationInputSHA256,
		IncumbentReplayResultSHA256:  replayDigest,
	}
	if walkForward != nil {
		if err := validateShadowPerpsWalkForwardReceipt(*walkForward, tape, tapeSHA256, qualification.InputSHA256); err != nil {
			return shadowPerpsFinalizationReceipt{}, err
		}
		resultDigest, err := shadowPerpsJSONSHA256(*walkForward)
		if err != nil {
			return shadowPerpsFinalizationReceipt{}, err
		}
		receipt.WalkForwardInputSHA256 = walkForward.InputSHA256
		receipt.WalkForwardResultSHA256 = resultDigest
		receipt.WalkForwardOutcome = walkForward.Outcome
		receipt.TrainingLeader = walkForward.TrainingLeader
		receipt.TrainingTrials = walkForward.TrainingTrials
		receipt.HoldoutPlansCompared = walkForward.HoldoutPlansCompared
		receipt.HoldoutEvaluated = walkForward.Forward != nil
		receipt.HoldoutCompletedTrades = walkForward.HoldoutCompletedTrades
		receipt.StatisticalConfidence = walkForward.StatisticalConfidence
	}
	if err := receipt.validate(); err != nil {
		return shadowPerpsFinalizationReceipt{}, err
	}
	return receipt, nil
}

func validateShadowPerpsWalkForwardReceipt(
	result perpspaper.WalkForwardQualification,
	tape shadowPerpsTape,
	tapeSHA256, singleQualificationSHA256 string,
) error {
	if tape.Config.qualificationConfig() != result.Config || len(result.Tapes) < 2 ||
		result.Version != perpspaper.WalkForwardVersion || result.Status != "research_only" ||
		!result.PaperOnly || result.Authorized || result.Promotable || !validLowerSHA256(result.InputSHA256) ||
		result.TrainingTrials != uint64(len(result.Training)) || !validShadowPerpsTrainingTrials(result.Training) ||
		result.StatisticalConfidence != perpspaper.QualificationConfidence {
		return errors.New("perps finalization walk-forward result is invalid")
	}
	final := result.Tapes[len(result.Tapes)-1]
	if final.ContentSHA256 != tapeSHA256 || final.ReplayInputSHA256 != singleQualificationSHA256 ||
		final.Frames != uint64(len(tape.Frames)) || final.FirstTime != tape.Frames[0].Book.Time ||
		final.LastTime != tape.Frames[len(tape.Frames)-1].Book.Time {
		return errors.New("perps finalization walk-forward tape is invalid")
	}
	switch result.Outcome {
	case "insufficient_evidence":
		if result.TrainingTrials != 0 || result.TrainingLeader != nil || result.Forward != nil ||
			result.Stress != nil || result.Candidate != nil || result.HoldoutPlansCompared != 0 ||
			result.HoldoutCompletedTrades != 0 || result.EligibleForPaperExperiment {
			return errors.New("perps finalization insufficient walk-forward result is inconsistent")
		}
	case "no_training_candidate":
		if result.TrainingTrials != 12 || result.TrainingLeader != nil || result.Forward != nil ||
			result.Stress != nil || result.Candidate != nil || result.HoldoutPlansCompared != 0 ||
			result.HoldoutCompletedTrades != 0 || result.EligibleForPaperExperiment {
			return errors.New("perps finalization no-candidate walk-forward result is inconsistent")
		}
	case "candidate_rejected", "candidate_ready_for_more_paper_testing":
		if result.TrainingTrials != 12 || result.TrainingLeader == nil || result.Forward == nil ||
			result.Stress == nil || result.HoldoutPlansCompared != 1 {
			return errors.New("perps finalization evaluated walk-forward result is inconsistent")
		}
		leader := *result.TrainingLeader
		if result.Forward.QualificationKey != leader || result.Forward.StressRule != "" ||
			result.Stress.QualificationKey != leader || result.Stress.StressRule != "double_fees_v1" {
			return errors.New("perps finalization walk-forward evidence is invalid")
		}
		completed := uint64(0)
		if result.Forward.Score != nil {
			completed = result.Forward.Score.ClosedPositions
		}
		if completed != result.HoldoutCompletedTrades {
			return errors.New("perps finalization completed trade count is invalid")
		}
		if result.Outcome == "candidate_ready_for_more_paper_testing" {
			if result.Candidate == nil || *result.Candidate != leader || !result.EligibleForPaperExperiment {
				return errors.New("perps finalization candidate result is inconsistent")
			}
		} else if result.Candidate != nil || result.EligibleForPaperExperiment {
			return errors.New("perps finalization rejected result is inconsistent")
		}
	default:
		return errors.New("perps finalization walk-forward outcome is invalid")
	}
	return nil
}

func validShadowPerpsTrainingTrials(trials []perpspaper.WalkForwardTrial) bool {
	if len(trials) != 0 && len(trials) != 12 {
		return false
	}
	seen := make(map[perpspaper.QualificationKey]struct{}, len(trials))
	for _, trial := range trials {
		if !validShadowPerpsRiskArm(trial.RiskArm) || !validShadowPerpsStrategy(trial.Strategy) {
			return false
		}
		if _, duplicate := seen[trial.QualificationKey]; duplicate {
			return false
		}
		seen[trial.QualificationKey] = struct{}{}
	}
	return true
}

func (receipt shadowPerpsFinalizationReceipt) validate() error {
	incumbentQualificationValid := receipt.IncumbentQualificationSHA256 == "" ||
		validLowerSHA256(receipt.IncumbentQualificationSHA256)
	if receipt.Version != shadowPerpsFinalizationReceiptVersion ||
		receipt.Status != "finalized_evaluated" || !receipt.PaperOnly ||
		receipt.Authorized || receipt.Promotable ||
		receipt.Evaluator != shadowPerpsFinalizationEvaluator ||
		receipt.EvaluatorVersion != perpspaper.QualificationVersion ||
		(receipt.Environment != perpspaper.Mainnet && receipt.Environment != perpspaper.Testnet) ||
		!validLowerSHA256(receipt.FinalTapeSHA256) ||
		!validLowerSHA256(receipt.SingleQualificationSHA256) ||
		!validLowerSHA256(receipt.SingleResultSHA256) ||
		!validLowerSHA256(receipt.IncumbentPlanSHA256) ||
		!validShadowPerpsComparisonKey(receipt.IncumbentDecisionMode, receipt.Incumbent) ||
		!incumbentQualificationValid || !validLowerSHA256(receipt.IncumbentReplayResultSHA256) {
		return errors.New("perps finalization receipt is invalid")
	}
	hasWalkForward := receipt.WalkForwardInputSHA256 != "" || receipt.WalkForwardResultSHA256 != ""
	if !hasWalkForward {
		if receipt.WalkForwardInputSHA256 != "" || receipt.WalkForwardResultSHA256 != "" ||
			receipt.WalkForwardOutcome != "" || receipt.TrainingLeader != nil || receipt.TrainingTrials != 0 ||
			receipt.HoldoutPlansCompared != 0 || receipt.HoldoutEvaluated || receipt.HoldoutCompletedTrades != 0 ||
			receipt.StatisticalConfidence != "" {
			return errors.New("perps finalization receipt has partial walk-forward evidence")
		}
		return nil
	}
	if !validLowerSHA256(receipt.WalkForwardInputSHA256) || !validLowerSHA256(receipt.WalkForwardResultSHA256) ||
		receipt.TrainingTrials > 12 || receipt.HoldoutPlansCompared > 1 ||
		receipt.StatisticalConfidence != perpspaper.QualificationConfidence {
		return errors.New("perps finalization receipt walk-forward evidence is invalid")
	}
	hasLeader := receipt.TrainingLeader != nil
	if hasLeader && (!validShadowPerpsStrategy(receipt.TrainingLeader.Strategy) ||
		!validShadowPerpsRiskArm(receipt.TrainingLeader.RiskArm)) {
		return errors.New("perps finalization receipt leader is invalid")
	}
	switch receipt.WalkForwardOutcome {
	case "insufficient_evidence":
		if receipt.TrainingTrials != 0 || hasLeader || receipt.HoldoutEvaluated ||
			receipt.HoldoutPlansCompared != 0 || receipt.HoldoutCompletedTrades != 0 {
			return errors.New("perps finalization receipt insufficient outcome is inconsistent")
		}
	case "no_training_candidate":
		if receipt.TrainingTrials != 12 || hasLeader || receipt.HoldoutEvaluated ||
			receipt.HoldoutPlansCompared != 0 || receipt.HoldoutCompletedTrades != 0 {
			return errors.New("perps finalization receipt no-candidate outcome is inconsistent")
		}
	case "candidate_rejected", "candidate_ready_for_more_paper_testing":
		if receipt.TrainingTrials != 12 || !hasLeader || !receipt.HoldoutEvaluated ||
			receipt.HoldoutPlansCompared != 1 {
			return errors.New("perps finalization receipt evaluated outcome is inconsistent")
		}
	default:
		return errors.New("perps finalization receipt outcome is invalid")
	}
	return nil
}

func shadowPerpsJSONSHA256(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", errors.New("could not encode perps evidence")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func foldShadowPerpsFinalizationReceipts(records []journal.Record) ([]shadowPerpsFinalizationReceipt, error) {
	receipts := make([]shadowPerpsFinalizationReceipt, 0, len(records))
	seen := make(map[string]struct{})
	for _, record := range records {
		if record.Type == journal.EventRotated {
			continue
		}
		if record.Type != shadowPerpsFinalizationEvent || !validLowerSHA256(record.ActionID) {
			return nil, errors.New("perps finalization journal contains an unexpected event")
		}
		var receipt shadowPerpsFinalizationReceipt
		if strictjson.Decode(record.Payload, &receipt) != nil || receipt.validate() != nil ||
			receipt.FinalTapeSHA256 != record.ActionID {
			return nil, errors.New("perps finalization journal receipt is invalid")
		}
		if _, duplicate := seen[record.ActionID]; duplicate {
			return nil, errors.New("perps finalization journal repeats a final tape")
		}
		seen[record.ActionID] = struct{}{}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}

func requireShadowPerpsFinalizationReceipt(
	stateDir string,
	tape shadowPerpsTape,
	tapeSHA256, incumbentPlanSHA256 string,
	walkForward perpspaper.WalkForwardQualification,
) (uint64, error) {
	if tape.Config.PlanSHA256 != incumbentPlanSHA256 {
		return 0, errors.New("perps finalization receipt does not match the incumbent plan")
	}
	qualification, err := perpspaper.QualifyTournament(tape.Config.qualificationConfig(), tape.Frames)
	if err != nil {
		return 0, err
	}
	replay, err := replayShadowPerpsTape(tape.Config, tape.Frames)
	if err != nil {
		return 0, err
	}
	expected, err := newShadowPerpsFinalizationReceipt(tape, tapeSHA256, replay, qualification, &walkForward)
	if err != nil {
		return 0, err
	}
	canonical, err := json.Marshal(expected)
	if err != nil {
		return 0, errors.New("could not encode expected perps finalization receipt")
	}
	store, err := journal.OpenRotating(shadowPerpsFinalizationJournalPath(stateDir, tape.Config.Symbol))
	if err != nil {
		return 0, err
	}
	receipts, foldErr := foldShadowPerpsFinalizationReceipts(store.Records())
	closeErr := store.Close()
	if foldErr != nil {
		return 0, foldErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	for _, receipt := range receipts {
		if receipt.FinalTapeSHA256 != tapeSHA256 {
			continue
		}
		encoded, marshalErr := json.Marshal(receipt)
		if marshalErr != nil || !bytes.Equal(encoded, canonical) {
			return 0, errors.New("perps finalization receipt does not match the selected result")
		}
		return uint64(len(receipts)), nil
	}
	return 0, errors.New("perps finalization receipt is missing")
}

func writeShadowPerpsExecutionDelayAdvisory(
	stateDir string,
	symbol perpspaper.Symbol,
	leader perpspaper.QualificationKey,
	advisory perpspaper.ExecutionDelayAdvisory,
) (string, error) {
	if err := advisory.Validate(advisory.QualificationInputSHA256, advisory.FinalTapeSHA256, leader); err != nil {
		return "", err
	}
	encoded, err := json.MarshalIndent(advisory, "", "  ")
	if err != nil {
		return "", errors.New("could not encode execution-delay advisory")
	}
	encoded = append(encoded, '\n')
	digest := sha256.Sum256(encoded)
	name := hex.EncodeToString(digest[:]) + ".json"
	directory := filepath.Join(filepath.Dir(stateDir), "advisories", strings.ToLower(string(symbol)))
	for _, path := range []string{filepath.Dir(directory), directory} {
		if err := ensureShadowPerpsPrivateDirectory(path); err != nil {
			return "", err
		}
	}
	path := filepath.Join(directory, name)
	if existing, err := securefile.ReadPrivate(path, shadowPerpsMaxFileBytes); err == nil {
		if bytes.Equal(existing, encoded) {
			return path, nil
		}
		return "", errors.New("execution-delay advisory digest collision")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("could not inspect execution-delay advisory")
	}
	if err := securefile.CreatePrivate(path, encoded, shadowPerpsMaxFileBytes); err == nil {
		return path, nil
	}
	existing, err := securefile.ReadPrivate(path, shadowPerpsMaxFileBytes)
	if err != nil || !bytes.Equal(existing, encoded) {
		return "", errors.New("could not create execution-delay advisory")
	}
	return path, nil
}

func sealShadowPerpsTape(stateDir string, tape shadowPerpsTape) (string, error) {
	raw, digest, err := canonicalShadowPerpsTape(tape)
	if err != nil {
		return "", err
	}
	directory := shadowPerpsCorpusDir(stateDir, tape.Config.Symbol)
	if err := ensureShadowPerpsPrivateDirectory(filepath.Dir(directory)); err != nil {
		return "", err
	}
	if err := ensureShadowPerpsPrivateDirectory(directory); err != nil {
		return "", err
	}
	path := filepath.Join(directory, digest+".json")
	staging := filepath.Join(directory, "."+digest+".staging")
	if err := os.Remove(staging); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("remove stale perps paper tape staging file: %w", err)
	}
	if existing, err := securefile.ReadPrivate(path, shadowPerpsMaxFileBytes); err == nil {
		if bytes.Equal(existing, raw) {
			return path, nil
		}
		return "", errors.New("sealed perps paper tape content does not match its digest")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect sealed perps paper tape: %w", err)
	}
	defer os.Remove(staging)
	if err := securefile.CreatePrivate(staging, raw, shadowPerpsMaxFileBytes); err != nil {
		return "", fmt.Errorf("stage perps paper tape: %w", err)
	}
	staged, err := securefile.ReadPrivate(staging, shadowPerpsMaxFileBytes)
	if err != nil || !bytes.Equal(staged, raw) {
		return "", errors.New("staged perps paper tape did not verify")
	}
	if err := securefile.RenameNoReplace(staging, path); err != nil {
		existing, readErr := securefile.ReadPrivate(path, shadowPerpsMaxFileBytes)
		if readErr == nil && bytes.Equal(existing, raw) {
			return path, nil
		}
		return "", fmt.Errorf("publish sealed perps paper tape: %w", err)
	}
	if err := syncShadowPerpsCorpus(directory); err != nil {
		return "", err
	}
	return path, nil
}

func syncShadowPerpsCorpus(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open perps paper tape corpus: %w", err)
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return fmt.Errorf("sync perps paper tape corpus: %w", err)
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("close perps paper tape corpus: %w", err)
	}
	return nil
}

func shadowPerpsCorpusDir(stateDir string, symbol perpspaper.Symbol) string {
	return filepath.Join(filepath.Dir(stateDir), "tapes", strings.ToLower(string(symbol)))
}

func ensureShadowPerpsPrivateDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create perps paper tape corpus: %w", err)
	}
	if err := secureexec.ValidateProtectedDirectory(path); err != nil {
		return errors.New("perps paper tape corpus is not trusted")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("perps paper tape corpus must be private mode 0700")
	}
	return nil
}

func canonicalShadowPerpsTape(tape shadowPerpsTape) ([]byte, string, error) {
	if _, err := perpspaper.QualifyTournament(tape.Config.qualificationConfig(), tape.Frames); err != nil {
		return nil, "", fmt.Errorf("verify perps paper tape: %w", err)
	}
	raw, err := json.Marshal(tape)
	if err != nil {
		return nil, "", fmt.Errorf("encode perps paper tape: %w", err)
	}
	raw = append(raw, '\n')
	digest := sha256.Sum256(raw)
	return raw, hex.EncodeToString(digest[:]), nil
}

func preserveCompletedShadowPerpsTapes(stateDir string) error {
	for _, symbol := range [...]perpspaper.Symbol{perpspaper.SOL, perpspaper.BTC, perpspaper.ETH} {
		path := filepath.Join(stateDir, strings.ToLower(string(symbol))+"-tape.json")
		raw, err := securefile.ReadPrivate(path, shadowPerpsMaxFileBytes)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read previous %s paper tape: %w", symbol, err)
		}
		var header shadowPerpsTape
		if err := strictjson.Decode(raw, &header); err != nil || header.Config.Symbol != symbol {
			return fmt.Errorf("decode previous %s paper tape", symbol)
		}
		tape, _, err := readShadowPerpsTape(path, header.Config)
		if err != nil {
			return err
		}
		if len(tape.Frames) >= perpspaper.QualificationMinimumFrames {
			if _, err := sealShadowPerpsTape(stateDir, tape); err != nil {
				return fmt.Errorf("preserve previous %s paper tape: %w", symbol, err)
			}
		}
	}
	return nil
}

func readShadowPerpsCorpusTape(path string) (shadowPerpsTape, string, error) {
	raw, err := securefile.ReadPrivate(path, shadowPerpsMaxFileBytes)
	if err != nil {
		return shadowPerpsTape{}, "", fmt.Errorf("read immutable paper tape: %w", err)
	}
	var header shadowPerpsTape
	if err := strictjson.Decode(raw, &header); err != nil {
		return shadowPerpsTape{}, "", errors.New("decode immutable paper tape")
	}
	tape, _, err := readShadowPerpsTape(path, header.Config)
	if err != nil {
		return shadowPerpsTape{}, "", err
	}
	canonical, digest, err := canonicalShadowPerpsTape(tape)
	if err != nil {
		return shadowPerpsTape{}, "", err
	}
	if !bytes.Equal(raw, canonical) || filepath.Base(path) != digest+".json" {
		return shadowPerpsTape{}, "", errors.New("immutable paper tape name or content digest does not match")
	}
	return tape, digest, nil
}

func qualifyShadowPerpsCorpus(stateDir string, config shadowPerpsTapeConfig) (*perpspaper.WalkForwardQualification, error) {
	directory := shadowPerpsCorpusDir(stateDir, config.Symbol)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read perps paper tape corpus: %w", err)
	}
	removedStaging := false
	for _, entry := range entries {
		name := entry.Name()
		if !shadowPerpsStagingName(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !fileowner.Trusted(info) {
			return nil, errors.New("perps paper tape corpus contains an invalid staging entry")
		}
		if err := recoverShadowPerpsStaging(directory, name); err != nil {
			return nil, err
		}
		removedStaging = true
	}
	if removedStaging {
		if err := syncShadowPerpsCorpus(directory); err != nil {
			return nil, err
		}
		entries, err = os.ReadDir(directory)
		if err != nil {
			return nil, fmt.Errorf("reread perps paper tape corpus: %w", err)
		}
	}
	type corpusTape struct {
		tape   shadowPerpsTape
		digest string
	}
	tapes := make([]corpusTape, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) != 69 || !strings.HasSuffix(name, ".json") {
			return nil, errors.New("perps paper tape corpus contains an unexpected entry")
		}
		if _, err := hex.DecodeString(strings.TrimSuffix(name, ".json")); err != nil {
			return nil, errors.New("perps paper tape corpus contains an unexpected entry")
		}
		tape, digest, err := readShadowPerpsCorpusTape(filepath.Join(directory, name))
		if err != nil {
			return nil, err
		}
		if !compatibleShadowPerpsTapes(config, tape.Config) {
			return nil, errors.New("perps paper tape corpus mixes incompatible market configurations")
		}
		tapes = append(tapes, corpusTape{tape: tape, digest: digest})
	}
	if len(tapes) < 2 {
		return nil, nil
	}
	sort.Slice(tapes, func(left, right int) bool {
		return tapes[left].tape.Frames[0].Book.Time < tapes[right].tape.Frames[0].Book.Time
	})
	windows := make([]perpspaper.WalkForwardTape, len(tapes))
	for index, item := range tapes {
		windows[index] = perpspaper.WalkForwardTape{ContentSHA256: item.digest, Frames: item.tape.Frames}
	}
	result, err := perpspaper.QualifyWalkForward(config.qualificationConfig(), windows)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func recoverShadowPerpsStaging(directory, name string) error {
	staging := filepath.Join(directory, name)
	raw, readErr := securefile.ReadPrivate(staging, shadowPerpsMaxFileBytes)
	digest := name[1:65]
	complete := false
	if readErr == nil {
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) == digest {
			var header shadowPerpsTape
			if strictjson.Decode(raw, &header) == nil {
				tape, _, err := readShadowPerpsTape(staging, header.Config)
				if err == nil {
					canonical, canonicalDigest, err := canonicalShadowPerpsTape(tape)
					complete = err == nil && canonicalDigest == digest && bytes.Equal(canonical, raw)
				}
			}
		}
	}
	if !complete {
		if err := os.Remove(staging); err != nil {
			return fmt.Errorf("remove incomplete perps paper tape staging file: %w", err)
		}
		return nil
	}
	path := filepath.Join(directory, digest+".json")
	if err := securefile.RenameNoReplace(staging, path); err != nil {
		existing, readErr := securefile.ReadPrivate(path, shadowPerpsMaxFileBytes)
		if readErr != nil || !bytes.Equal(existing, raw) {
			return fmt.Errorf("recover staged perps paper tape: %w", err)
		}
		if err := os.Remove(staging); err != nil {
			return fmt.Errorf("remove duplicate perps paper tape staging file: %w", err)
		}
	}
	return nil
}

func shadowPerpsStagingName(name string) bool {
	if len(name) != 73 || name[0] != '.' || !strings.HasSuffix(name, ".staging") {
		return false
	}
	digest := name[1:65]
	_, err := hex.DecodeString(digest)
	return err == nil && digest == strings.ToLower(digest)
}

func compatibleShadowPerpsTapes(left, right shadowPerpsTapeConfig) bool {
	return left.Environment == right.Environment && left.Symbol == right.Symbol &&
		left.StartingCollateralMicros == right.StartingCollateralMicros &&
		left.VenueMaxLeverage == right.VenueMaxLeverage && left.VenueSzDecimals == right.VenueSzDecimals
}

func applyShadowPerpsWalkForward(summary *paperstatus.CurrentSummary, result perpspaper.WalkForwardQualification) {
	summary.QualificationTracked = true
	summary.QualificationOutcome = result.Outcome
	summary.QualificationSHA256 = result.InputSHA256
	summary.QualificationTapes = uint64(len(result.Tapes))
	summary.QualificationFrames = 0
	summary.QualificationMinimumFrames = 0
	summary.QualificationTrainingFrames = 0
	summary.QualificationHoldoutFrames = 0
	summary.QualificationStrategy = ""
	summary.QualificationRiskProfile = ""
	summary.QualificationHoldoutEvaluated = false
	summary.QualificationStressEvaluated = false
	summary.QualificationHoldoutScored = false
	summary.QualificationStressScored = false
	summary.QualificationHoldoutMicros = 0
	summary.QualificationStressMicros = 0
	summary.QualificationAttempts = nil
	for index, tape := range result.Tapes {
		summary.QualificationFrames += tape.Frames
		if index < len(result.Tapes)-1 {
			summary.QualificationTrainingFrames += tape.Frames
		} else {
			summary.QualificationHoldoutFrames = tape.Frames
		}
	}
	summary.QualificationMinimumFrames = uint64(len(result.Tapes)) * perpspaper.QualificationMinimumFrames
	for _, attempt := range perpspaper.BestCompletedTrainingAttempts(result.Training) {
		score := attempt.Score
		summary.QualificationAttempts = append(summary.QualificationAttempts, paperstatus.QualificationAttempt{
			RiskProfile: string(attempt.RiskArm), Strategy: string(attempt.Strategy),
			NetPnLMicros: score.NetPnLMicros, FeesMicros: score.FeesPaidMicros,
			FundingMicros: score.FundingPnLMicros, MaxDrawdownMicros: score.MaxDrawdownMicros,
			Liquidations: score.Liquidations, FilledOrders: score.FilledOrders,
			ClosedPositions: score.ClosedPositions,
		})
	}
	if result.TrainingLeader != nil {
		summary.QualificationStrategy = string(result.TrainingLeader.Strategy)
		summary.QualificationRiskProfile = string(result.TrainingLeader.RiskArm)
	}
	if result.Forward != nil {
		summary.QualificationHoldoutEvaluated = true
		if result.Forward.Score != nil {
			summary.QualificationHoldoutScored = true
			summary.QualificationHoldoutMicros = result.Forward.Score.NetPnLMicros
		}
	}
	if result.Stress != nil {
		summary.QualificationStressEvaluated = true
		if result.Stress.Score != nil {
			summary.QualificationStressScored = true
			summary.QualificationStressMicros = result.Stress.Score.NetPnLMicros
		}
	}
}

func shadowPerpsWalkForwardLabel(result perpspaper.WalkForwardQualification) string {
	switch result.Outcome {
	case "insufficient_evidence":
		return "more complete market recordings are needed"
	case "no_training_candidate":
		return "no paper plan passed every training gate across the earlier recordings"
	case "candidate_rejected":
		return "the selected paper plan did not pass the final held-out recording"
	default:
		return "one paper plan passed and can be checked again"
	}
}

func shadowPerpsWalkForwardMessage(result perpspaper.WalkForwardQualification) string {
	message := fmt.Sprintf("PAPER · 🧪 STRATEGY CHECK\nRecordings checked: %d separate\nResult: %s", len(result.Tapes), shadowPerpsWalkForwardLabel(result))
	if result.Forward != nil {
		if result.Forward.Score != nil {
			message += "\nFinal held-out recording: " + formatPerpsResult(result.Forward.Score.NetPnLMicros)
		} else {
			message += "\nFinal held-out recording: no complete result"
		}
	} else if result.Outcome == "no_training_candidate" {
		message += "\nFinal held-out recording: kept closed"
	}
	return message + "\nNo real order was sent."
}
