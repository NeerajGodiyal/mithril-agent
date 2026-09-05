package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

const perpsContextMaxBytes = 256 << 10
const perpsContextUsage = `Usage: mithril-agent shadow perps-context --state-dir PATH --symbol SOL --tape PATH [--tape PATH ...] [--evaluation PATH ...] --out PATH

Create a private write-once host context from 1–8 selected chronological tapes
and at most 8 explicit resolved evaluations. It contains modeled metrics, not
raw books/candles or model prose. All tape splits are now historical screening,
not unseen evidence for a new proposal. Pending evaluations are rejected.
No sources are chosen by profitability. This command cannot select a plan,
promote, authorize or trade. A future model integration must pass this exact
host-controlled context to perps-freeze --context PATH.`

type shadowPerpsContextTape struct {
	shadowPerpsProposalTraining
	TrainingFrames uint64                            `json:"training_frames"`
	HoldoutFrames  uint64                            `json:"holdout_frames"`
	Trials         []perpspaper.QualificationTrial   `json:"historical_training_trials"`
	Holdout        *perpspaper.QualificationEvidence `json:"historical_holdout,omitempty"`
	Stress         *perpspaper.QualificationEvidence `json:"historical_holdout_stress,omitempty"`
	Outcome        string                            `json:"historical_outcome"`
	Reasons        []string                          `json:"historical_reasons"`
}

type shadowPerpsContextOutcome struct {
	shadowPerpsProposalEvaluation
	ProposedKey      perpspaper.QualificationKey `json:"original_proposed_key"`
	BaselineKey      perpspaper.QualificationKey `json:"original_baseline_key"`
	ProposalFrozenAt time.Time                   `json:"proposal_frozen_at"`
}

type shadowPerpsContext struct {
	Version             uint32                      `json:"version"`
	Status              string                      `json:"status"`
	PaperOnly           bool                        `json:"paper_only"`
	Authorized          bool                        `json:"authorized"`
	Promotable          bool                        `json:"promotable"`
	HistoricalScreening bool                        `json:"historical_screening"`
	StateSHA256         string                      `json:"state_sha256"`
	Symbol              perpspaper.Symbol           `json:"symbol"`
	ContextKnownAt      time.Time                   `json:"context_known_at"`
	Baseline            shadowPerpsPlan             `json:"baseline"`
	BaselineSHA256      string                      `json:"baseline_sha256"`
	Training            []shadowPerpsContextTape    `json:"training"`
	Outcomes            []shadowPerpsContextOutcome `json:"resolved_outcomes"`
	ContentSHA256       string                      `json:"content_sha256"`
}

func canonicalPerpsContext(context shadowPerpsContext) ([]byte, error) {
	context.ContentSHA256 = ""
	digest, err := shadowPerpsJSONSHA256(context)
	if err != nil {
		return nil, err
	}
	context.ContentSHA256 = digest
	raw, err := json.Marshal(context)
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if len(raw) > perpsContextMaxBytes {
		return nil, errors.New("perps context exceeds size limit")
	}
	return raw, nil
}

func perpsContextEvaluation(state string, symbol perpspaper.Symbol, path string) (shadowPerpsContextOutcome, error) {
	var original shadowPerpsContextOutcome
	digest := strings.TrimSuffix(filepath.Base(path), ".json")
	if !cleanResearchPath(path) || !validLowerSHA256(digest) || filepath.Base(path) != digest+".json" || filepath.Dir(path) != filepath.Join(filepath.Dir(state), "proposal-evaluations", strings.ToLower(string(symbol))) {
		return original, errors.New("perps context evaluation is outside host directory")
	}
	raw, err := securefile.ReadPrivate(path, perpsProposalMaxBytes)
	if err != nil {
		return original, err
	}
	if strictjson.Decode(raw, &original) != nil || original.Status == "pending" || original.ProposalSHA256 != digest {
		return original, errors.New("perps context requires a resolved evaluation")
	}
	directory := filepath.Join(filepath.Dir(state), "proposals", strings.ToLower(string(symbol)))
	entries, err := os.ReadDir(directory)
	if err != nil {
		return original, err
	}
	// ponytail: bounded existing receipt scan; share a durable digest index only
	// if the existing 256-proposal research ceiling is deliberately expanded.
	if len(entries) > 257 {
		return original, errors.New("perps proposal directory exceeds bound")
	}
	found := false
	for _, entry := range entries {
		if entry.Name() == "freeze.lock" {
			continue
		}
		proposalPath := filepath.Join(directory, entry.Name())
		proposal, _, err := readPerpsProposal(proposalPath)
		if err != nil {
			return original, err
		}
		if proposal.StateDir != state || proposal.Input.Symbol != symbol || entry.Name() != proposal.Input.HypothesisID+".json" {
			return original, errors.New("perps evaluation proposal identity mismatch")
		}
		if proposal.ContentSHA256 != digest {
			continue
		}
		if found {
			return original, errors.New("perps evaluation has duplicate proposal matches")
		}
		found = true
		result, lastAt, err := evaluateFrozenPerpsProposal(proposal, original.ObservedPrefix)
		if err != nil {
			return original, err
		}
		result.ObservedAt = original.ObservedAt
		if result.Status == "pending" || result.ObservedAt.IsZero() || result.ObservedAt.Before(lastAt) || result.ObservedAt.Before(proposal.FrozenAt) {
			return original, errors.New("perps resolved outcome timing is invalid")
		}
		verified, err := canonicalPerpsEvaluation(result)
		if err != nil || !bytes.Equal(raw, verified) {
			return original, errors.New("perps resolved outcome does not match exact evidence")
		}
		original.ProposedKey = perpspaper.QualificationKey{RiskArm: proposal.Input.RiskArm, Strategy: proposal.Input.Strategy}
		original.BaselineKey = proposal.Baseline.Key
		original.ProposalFrozenAt = proposal.FrozenAt
	}
	if found {
		return original, nil
	}
	return original, errors.New("perps resolved outcome has no matching frozen proposal")
}

func perpsContextTraining(state string, symbol perpspaper.Symbol, paths []string) ([]shadowPerpsContextTape, shadowPerpsTapeConfig, error) {
	var training []shadowPerpsContextTape
	var config shadowPerpsTapeConfig
	if len(paths) < 1 || len(paths) > 8 {
		return nil, config, errors.New("perps context requires 1–8 tapes")
	}
	if _, err := proposalTapeIDs(paths, state, symbol); err != nil {
		return nil, config, err
	}
	records, err := journal.ReadRecords(shadowPerpsFinalizationJournalPath(state, symbol))
	if err != nil {
		return nil, config, err
	}
	if _, err := foldShadowPerpsFinalizationReceipts(records); err != nil {
		return nil, config, err
	}
	byDigest := make(map[string]journal.Record)
	for _, record := range records {
		if record.Type == shadowPerpsFinalizationEvent {
			byDigest[record.ActionID] = record
		}
	}
	var last int64
	for i, path := range paths {
		tape, digest, err := readShadowPerpsCorpusTape(path)
		if err != nil {
			return nil, config, err
		}
		if tape.Version != shadowPerpsTapeVersion || tape.Config.Symbol != symbol || len(tape.Frames) < perpspaper.QualificationMinimumFrames || len(tape.Frames) > shadowPerpsMaxFrames {
			return nil, config, errors.New("perps context tape is outside current bounded corpus")
		}
		if i == 0 {
			config = tape.Config
		} else if !compatibleShadowPerpsTapes(config, tape.Config) {
			return nil, config, errors.New("perps context tapes have different configurations")
		}
		first, end := tape.Frames[0].Book.Time, tape.Frames[len(tape.Frames)-1].Book.Time
		if i > 0 && first <= last {
			return nil, config, errors.New("perps context tapes overlap or are unordered")
		}
		last = end
		record, ok := byDigest[digest]
		if !ok {
			return nil, config, errors.New("perps context tape has no finalization")
		}
		if err := verifyProposalTapeRecord(tape, digest, record); err != nil {
			return nil, config, err
		}
		if err := validatePerpsProposalFrameTimes(tape.Frames, time.Time{}, record.At); err != nil {
			return nil, config, err
		}
		qualification, err := perpspaper.QualifyTournament(tape.Config.qualificationConfig(), tape.Frames)
		if err != nil {
			return nil, config, err
		}
		training = append(training, shadowPerpsContextTape{shadowPerpsProposalTraining: shadowPerpsProposalTraining{TapeSHA256: digest, FinalizationSHA256: record.Hash, KnownAt: record.At, FirstTime: first, LastTime: end, Frames: len(tape.Frames)}, TrainingFrames: qualification.TrainingFrames, HoldoutFrames: qualification.HoldoutFrames, Trials: qualification.Training, Holdout: qualification.Holdout, Stress: qualification.Stress, Outcome: qualification.Outcome, Reasons: qualification.Reasons})
	}
	return training, config, nil
}

func verifyPerpsContext(context shadowPerpsContext, state string) error {
	stateDigest, err := shadowPerpsJSONSHA256(state)
	if err != nil || !cleanResearchPath(state) || context.StateSHA256 != stateDigest || context.Version != 1 || context.Status != "advisory_context" || !context.PaperOnly || context.Authorized || context.Promotable || !context.HistoricalScreening || context.ContextKnownAt.IsZero() || len(context.Training) < 1 || len(context.Training) > 8 || len(context.Outcomes) > 8 {
		return errors.New("perps context envelope is invalid")
	}
	_, digest, err := canonicalShadowPerpsPlan(context.Baseline)
	if err != nil || digest != context.BaselineSHA256 || context.Baseline.Config.Symbol != context.Symbol {
		return errors.New("perps context baseline is invalid")
	}
	paths := make([]string, len(context.Training))
	for i, tape := range context.Training {
		paths[i] = filepath.Join(shadowPerpsCorpusDir(state, context.Symbol), tape.TapeSHA256+".json")
	}
	training, config, err := perpsContextTraining(state, context.Symbol, paths)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(training, context.Training) || config.Environment != context.Baseline.Environment || config.qualificationConfig() != context.Baseline.Config {
		return errors.New("perps context training metrics or configuration differ")
	}
	for _, tape := range training {
		if tape.KnownAt.After(context.ContextKnownAt) {
			return errors.New("perps context includes future training evidence")
		}
	}
	seen := make(map[string]bool)
	for _, outcome := range context.Outcomes {
		if !validLowerSHA256(outcome.ProposalSHA256) || seen[outcome.ProposalSHA256] {
			return errors.New("perps context outcome identity is invalid")
		}
		seen[outcome.ProposalSHA256] = true
		path := filepath.Join(filepath.Dir(state), "proposal-evaluations", strings.ToLower(string(context.Symbol)), outcome.ProposalSHA256+".json")
		verified, err := perpsContextEvaluation(state, context.Symbol, path)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(verified, outcome) || outcome.ObservedAt.After(context.ContextKnownAt) {
			return errors.New("perps context outcome differs or became known later")
		}
	}
	return nil
}

func readPerpsContext(path, state string) (shadowPerpsContext, []byte, error) {
	var context shadowPerpsContext
	raw, err := securefile.ReadPrivate(path, perpsContextMaxBytes)
	if err != nil {
		return context, nil, err
	}
	if strictjson.Decode(raw, &context) != nil {
		return context, nil, errors.New("perps context JSON is invalid")
	}
	canonical, err := canonicalPerpsContext(context)
	if err != nil || !bytes.Equal(raw, canonical) {
		return context, nil, errors.New("perps context digest or encoding is invalid")
	}
	if err := verifyPerpsContext(context, state); err != nil {
		return context, nil, err
	}
	return context, raw, nil
}

func runShadowPerpsContext(args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("shadow perps-context", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	state := flags.String("state-dir", "", "host state directory")
	symbolText := flags.String("symbol", "", "SOL, BTC or ETH")
	out := flags.String("out", "", "private write-once host context")
	var paths, evaluations repeatedPathFlag
	flags.Var(&paths, "tape", "selected chronological immutable tape")
	flags.Var(&evaluations, "evaluation", "selected resolved outcome")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, perpsContextUsage)
		}
		return err
	}
	symbol := perpspaper.Symbol(*symbolText)
	if flags.NArg() != 0 || !cleanResearchPath(*state) || !cleanResearchPath(*out) || (symbol != perpspaper.SOL && symbol != perpspaper.BTC && symbol != perpspaper.ETH) || len(paths) < 1 || len(paths) > 8 || len(evaluations) > 8 {
		return errors.New("perps context arguments are invalid")
	}
	ids, err := proposalTapeIDs(paths, *state, symbol)
	if err != nil {
		return err
	}
	if existing, raw, err := readPerpsContext(*out, *state); err == nil {
		if existing.Symbol != symbol || len(existing.Training) != len(ids) || len(existing.Outcomes) != len(evaluations) {
			return errors.New("context output already has different selections")
		}
		for i, id := range ids {
			if existing.Training[i].TapeSHA256 != id {
				return errors.New("context training selection already differs")
			}
		}
		for i, path := range evaluations {
			expected := filepath.Join(filepath.Dir(*state), "proposal-evaluations", strings.ToLower(string(symbol)), existing.Outcomes[i].ProposalSHA256+".json")
			if path != expected {
				return errors.New("context outcome selection already differs")
			}
		}
		_, err = output.Write(raw)
		return err
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stateDigest, err := shadowPerpsJSONSHA256(*state)
	if err != nil {
		return err
	}
	context := shadowPerpsContext{Version: 1, Status: "advisory_context", PaperOnly: true, HistoricalScreening: true, StateSHA256: stateDigest, Symbol: symbol}
	training, config, err := perpsContextTraining(*state, symbol, paths)
	if err != nil {
		return err
	}
	context.Training = training
	seen := make(map[string]bool)
	for _, path := range evaluations {
		outcome, err := perpsContextEvaluation(*state, symbol, path)
		if err != nil {
			return err
		}
		if seen[outcome.ProposalSHA256] {
			return errors.New("perps context repeats an outcome")
		}
		seen[outcome.ProposalSHA256] = true
		context.Outcomes = append(context.Outcomes, outcome)
	}
	_, _, active, _, lock := shadowPerpsPlanPaths(*state, symbol)
	var selectedAt time.Time
	err = withShadowLifecycleLock(lock, func() error {
		var pointer shadowPerpsPlanPointer
		var err error
		context.Baseline, context.BaselineSHA256, pointer, err = loadBoundShadowPerpsPlanPointer(active, config.Environment, config.qualificationConfig())
		selectedAt = pointer.SelectedAt
		return err
	})
	if err != nil {
		return err
	}
	context.ContextKnownAt = now().UTC()
	if context.ContextKnownAt.IsZero() || selectedAt.After(context.ContextKnownAt) {
		return errors.New("perps context time precedes baseline evidence")
	}
	for _, tape := range context.Training {
		if tape.KnownAt.After(context.ContextKnownAt) {
			return errors.New("perps context includes future training evidence")
		}
	}
	for _, outcome := range context.Outcomes {
		if outcome.ObservedAt.After(context.ContextKnownAt) {
			return errors.New("perps context includes future resolved outcome")
		}
	}
	raw, err := canonicalPerpsContext(context)
	if err != nil {
		return err
	}
	if err := securefile.CreatePrivate(*out, raw, perpsContextMaxBytes); err != nil {
		return err
	}
	_, err = output.Write(raw)
	return err
}
