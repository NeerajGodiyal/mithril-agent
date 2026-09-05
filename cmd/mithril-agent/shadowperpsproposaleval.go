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

const perpsEvaluateUsage = `Usage: mithril-agent shadow perps-evaluate --proposal PATH

Verify a frozen proposal against only its assigned episode. Pending describes
the observed published prefix, not the latest possible journal state, and is
not persisted. A verified terminal result is private and immutable. Missing or
corrupt bound evidence is an error, not a learning result. Comparisons reuse
modeled paper fills and normal/doubled fees; they are historical advisory
evidence, never qualification, promotion, live execution or venue performance.`

type shadowPerpsProposalEvaluation struct {
	Version        uint32                            `json:"version"`
	Status         string                            `json:"status"`
	Reason         string                            `json:"reason"`
	PaperOnly      bool                              `json:"paper_only"`
	Authorized     bool                              `json:"authorized"`
	Promotable     bool                              `json:"promotable"`
	Scope          string                            `json:"scope"`
	ProposalSHA256 string                            `json:"proposal_sha256"`
	TargetEpisode  string                            `json:"target_episode"`
	ObservedPrefix journal.DurablePrefix             `json:"observed_prefix"`
	ObservedAt     time.Time                         `json:"observed_at"`
	StartSHA256    string                            `json:"start_sha256,omitempty"`
	TerminalSHA256 string                            `json:"terminal_sha256,omitempty"`
	Baseline       *perpspaper.QualificationEvidence `json:"baseline,omitempty"`
	BaselineStress *perpspaper.QualificationEvidence `json:"baseline_stress,omitempty"`
	Proposed       *perpspaper.QualificationEvidence `json:"proposed,omitempty"`
	ProposedStress *perpspaper.QualificationEvidence `json:"proposed_stress,omitempty"`
	ContentSHA256  string                            `json:"content_sha256"`
}

func canonicalPerpsEvaluation(result shadowPerpsProposalEvaluation) ([]byte, error) {
	result.ContentSHA256 = ""
	digest, err := shadowPerpsJSONSHA256(result)
	if err != nil {
		return nil, err
	}
	result.ContentSHA256 = digest
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if len(raw) > perpsProposalMaxBytes {
		return nil, errors.New("perps evaluation exceeds size limit")
	}
	return raw, nil
}

func runShadowPerpsEvaluate(args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("shadow perps-evaluate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("proposal", "", "host-owned immutable frozen receipt")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, perpsEvaluateUsage)
		}
		return err
	}
	if flags.NArg() != 0 || !cleanResearchPath(*path) {
		return errors.New("perps evaluate requires a clean absolute --proposal")
	}
	proposal, _, err := readPerpsProposal(*path)
	if err != nil {
		return err
	}
	expected := filepath.Join(filepath.Dir(proposal.StateDir), "proposals", strings.ToLower(string(proposal.Input.Symbol)), proposal.Input.HypothesisID+".json")
	if *path != expected {
		return errors.New("perps proposal is outside its host receipt directory")
	}
	root := filepath.Join(filepath.Dir(proposal.StateDir), "proposal-evaluations")
	directory := filepath.Join(root, strings.ToLower(string(proposal.Input.Symbol)))
	for _, dir := range []string{root, directory} {
		if err := ensureShadowPerpsPrivateDirectory(dir); err != nil {
			return err
		}
	}
	return withShadowLifecycleLock(filepath.Join(directory, "evaluation.lock"), func() error {
		resultPath := filepath.Join(directory, proposal.ContentSHA256+".json")
		stored, readErr := securefile.ReadPrivate(resultPath, perpsProposalMaxBytes)
		var original shadowPerpsProposalEvaluation
		var prefix journal.DurablePrefix
		if readErr == nil {
			if strictjson.Decode(stored, &original) != nil || original.Status == "pending" {
				return errors.New("stored perps evaluation is invalid")
			}
			prefix = original.ObservedPrefix
		} else {
			if !errors.Is(readErr, os.ErrNotExist) {
				return readErr
			}
			raw, err := securefile.ReadPrivate(proposal.EpisodeJournal+".prefix.json", 4096)
			if err != nil {
				return err
			}
			if strictjson.Decode(raw, &prefix) != nil {
				return errors.New("perps evaluation prefix is invalid")
			}
		}
		result, lastAt, err := evaluateFrozenPerpsProposal(proposal, prefix)
		if err != nil {
			return err
		}
		if readErr == nil {
			result.ObservedAt = original.ObservedAt
		} else {
			result.ObservedAt = now().UTC()
		}
		if result.ObservedAt.IsZero() || result.ObservedAt.Before(lastAt) || result.ObservedAt.Before(proposal.FrozenAt) {
			return errors.New("perps evaluation observation time precedes its evidence")
		}
		encoded, err := canonicalPerpsEvaluation(result)
		if err != nil {
			return err
		}
		if readErr == nil {
			if !bytes.Equal(stored, encoded) {
				return errors.New("stored perps evaluation no longer matches its exact evidence")
			}
		} else if result.Status != "pending" {
			if err := securefile.CreatePrivate(resultPath, encoded, perpsProposalMaxBytes); err != nil {
				return err
			}
		}
		_, err = output.Write(encoded)
		return err
	})
}

// verifyProposalTapeRecord checks only the exact tape's core finalization.
// Optional tournament selection evidence is not authority for this comparison.
func verifyProposalTapeRecord(tape shadowPerpsTape, digest string, record journal.Record) error {
	var receipt shadowPerpsFinalizationReceipt
	if record.Type != shadowPerpsFinalizationEvent || record.ActionID != digest || strictjson.Decode(record.Payload, &receipt) != nil || receipt.validate() != nil {
		return errors.New("perps tape finalization record is invalid")
	}
	qualification, err := perpspaper.QualifyTournament(tape.Config.qualificationConfig(), tape.Frames)
	if err != nil {
		return err
	}
	replay, err := replayShadowPerpsTape(tape.Config, tape.Frames)
	if err != nil {
		return err
	}
	expected, err := newShadowPerpsFinalizationReceipt(tape, digest, replay, qualification, nil)
	if err != nil {
		return err
	}
	if receipt.Environment != expected.Environment || receipt.Symbol != expected.Symbol || receipt.FinalTapeSHA256 != expected.FinalTapeSHA256 ||
		receipt.SingleQualificationSHA256 != expected.SingleQualificationSHA256 || receipt.SingleResultSHA256 != expected.SingleResultSHA256 ||
		receipt.IncumbentPlanSHA256 != expected.IncumbentPlanSHA256 || receipt.IncumbentDecisionMode != expected.IncumbentDecisionMode ||
		receipt.Incumbent != expected.Incumbent || receipt.IncumbentQualificationSHA256 != expected.IncumbentQualificationSHA256 || receipt.IncumbentReplayResultSHA256 != expected.IncumbentReplayResultSHA256 {
		return errors.New("perps tape finalization metadata does not match replay")
	}
	return nil
}

// validatePerpsProposalFrameTimes bounds observations by when the host could
// have known them. Historical candles may warm indicators and are not bounded
// by the episode start; the book and sampled context must belong to its window.
func validatePerpsProposalFrameTimes(frames []perpspaper.TapeFrame, startedAt, knownAt time.Time) error {
	if knownAt.IsZero() || !startedAt.IsZero() && knownAt.Before(startedAt) {
		return errors.New("perps proposal observation window is invalid")
	}
	for _, frame := range frames {
		for _, timestamp := range [...]int64{frame.Book.Time, frame.Context.ReceivedAt} {
			observed := time.UnixMilli(timestamp)
			if timestamp <= 0 || observed.After(knownAt) || !startedAt.IsZero() && observed.Before(startedAt) {
				return errors.New("perps proposal observation is outside host evidence window")
			}
		}
	}
	return nil
}

func evaluateFrozenPerpsProposal(proposal shadowPerpsProposal, prefix journal.DurablePrefix) (shadowPerpsProposalEvaluation, time.Time, error) {
	result := shadowPerpsProposalEvaluation{Version: 1, Status: "pending", Reason: "target_not_in_observed_prefix", PaperOnly: true, Scope: "historical_modeled_advisory", ProposalSHA256: proposal.ContentSHA256, TargetEpisode: proposal.TargetEpisode, ObservedPrefix: prefix}
	episodes, err := journal.ReadDurablePrefix(proposal.EpisodeJournal, prefix)
	if err != nil {
		return result, time.Time{}, err
	}
	if len(episodes) < proposal.EpisodePrefix.Records || episodes[proposal.EpisodePrefix.Records-1].Hash != proposal.EpisodePrefix.ChainHeadSHA256 {
		return result, time.Time{}, errors.New("perps observed prefix does not extend frozen prefix")
	}
	if _, _, err := foldShadowPerpsEpisodes(episodes); err != nil {
		return result, time.Time{}, err
	}
	lastAt := episodes[len(episodes)-1].At
	finalizations, err := journal.ReadRecords(shadowPerpsFinalizationJournalPath(proposal.StateDir, proposal.Input.Symbol))
	if err != nil {
		return result, lastAt, err
	}
	if _, err := foldShadowPerpsFinalizationReceipts(finalizations); err != nil {
		return result, lastAt, err
	}
	byHash := make(map[string]journal.Record, len(finalizations))
	for _, record := range finalizations {
		byHash[record.Hash] = record
	}
	for _, training := range proposal.Training {
		path := filepath.Join(shadowPerpsCorpusDir(proposal.StateDir, proposal.Input.Symbol), training.TapeSHA256+".json")
		tape, digest, err := readShadowPerpsCorpusTape(path)
		if err != nil {
			return result, lastAt, err
		}
		record, ok := byHash[training.FinalizationSHA256]
		if !ok || !record.At.Equal(training.KnownAt) || record.At.After(proposal.FrozenAt) || tape.Config.Environment != proposal.Baseline.Environment || tape.Config.qualificationConfig() != proposal.Baseline.Config ||
			len(tape.Frames) != training.Frames || tape.Frames[0].Book.Time != training.FirstTime || tape.Frames[len(tape.Frames)-1].Book.Time != training.LastTime || training.LastTime > record.At.UnixMilli() {
			return result, lastAt, errors.New("perps frozen training provenance mismatch")
		}
		if err := verifyProposalTapeRecord(tape, digest, record); err != nil {
			return result, lastAt, err
		}
		if err := validatePerpsProposalFrameTimes(tape.Frames, time.Time{}, record.At); err != nil {
			return result, lastAt, err
		}
	}
	var frozenConfig, targetConfig shadowPerpsEpisodeConfig
	var start, end *journal.Record
	var terminal shadowPerpsEpisodeEvent
	for i, record := range episodes {
		var event shadowPerpsEpisodeEvent
		if record.Type == journal.EventRotated {
			continue
		}
		if err := strictjson.Decode(record.Payload, &event); err != nil {
			return result, lastAt, err
		}
		if record.Type == perpsEpisodeStart && i < proposal.EpisodePrefix.Records {
			frozenConfig = *event.Config
		}
		if record.ActionID == proposal.TargetEpisode {
			copy := record
			if record.Type == perpsEpisodeStart {
				start = &copy
				targetConfig = *event.Config
			} else {
				end = &copy
				terminal = event
			}
		}
	}
	if start == nil {
		return result, lastAt, nil
	}
	result.StartSHA256 = start.Hash
	result.Reason = "target_unresolved"
	if end == nil {
		return result, lastAt, nil
	}
	result.TerminalSHA256 = end.Hash
	result.Status = "unevaluable"
	if !start.At.After(proposal.FrozenAt) {
		result.Reason = "target_started_before_freeze"
		return result, lastAt, nil
	}
	if terminal.Outcome != "finished" {
		result.Reason = "target_incomplete"
		return result, lastAt, nil
	}
	startHead := targetConfig.FinalizationHeads[proposal.Input.Symbol]
	frozenConfig.FinalizationHeads = nil
	targetConfig.FinalizationHeads = nil
	if !reflect.DeepEqual(frozenConfig, targetConfig) {
		result.Reason = "target_configuration_changed"
		return result, lastAt, nil
	}
	var binding shadowPerpsEpisodeTape
	for _, tape := range terminal.Tapes {
		if tape.Symbol == proposal.Input.Symbol {
			binding = tape
		}
	}
	if binding.TapeSHA256 == "" {
		result.Reason = "target_has_no_tape"
		return result, lastAt, nil
	}
	record, ok := byHash[binding.FinalizationSHA256]
	var receipt shadowPerpsFinalizationReceipt
	if !ok || record.Type != shadowPerpsFinalizationEvent || record.ActionID != binding.TapeSHA256 ||
		record.At.Before(start.At) || record.At.After(end.At) || strictjson.Decode(record.Payload, &receipt) != nil ||
		receipt.validate() != nil || receipt.FinalTapeSHA256 != binding.TapeSHA256 ||
		receipt.Symbol != proposal.Input.Symbol || receipt.Environment != proposal.Baseline.Environment {
		return result, lastAt, errors.New("perps target finalization or time binding is invalid")
	}
	if startHead != "" {
		head, ok := byHash[startHead]
		if !ok || head.Sequence >= record.Sequence {
			return result, lastAt, errors.New("perps target finalization does not extend starting boundary")
		}
	}
	if binding.Frames < perpspaper.QualificationMinimumFrames {
		result.Reason = "target_has_insufficient_frames"
		return result, lastAt, nil
	}
	path := filepath.Join(shadowPerpsCorpusDir(proposal.StateDir, proposal.Input.Symbol), binding.TapeSHA256+".json")
	tape, digest, err := readShadowPerpsCorpusTape(path)
	if err != nil {
		return result, lastAt, err
	}
	if len(tape.Frames) != binding.Frames {
		return result, lastAt, errors.New("perps target frame count binding is invalid")
	}
	if err := validatePerpsProposalFrameTimes(tape.Frames, start.At, record.At); err != nil {
		return result, lastAt, err
	}
	if err := verifyProposalTapeRecord(tape, digest, record); err != nil {
		return result, lastAt, err
	}
	if tape.Config.Environment != proposal.Baseline.Environment || tape.Config.qualificationConfig() != proposal.Baseline.Config {
		return result, lastAt, errors.New("perps target tape configuration mismatch")
	}
	if tape.Config.PlanSHA256 != proposal.BaselineSHA256 {
		result.Reason = "target_baseline_changed"
		return result, lastAt, nil
	}
	baseline, baselineStress, err := perpspaper.EvaluateFixedPlan(proposal.Baseline.Config, proposal.Baseline.Key, tape.Frames)
	if err != nil {
		return result, lastAt, err
	}
	key := perpspaper.QualificationKey{RiskArm: proposal.Input.RiskArm, Strategy: proposal.Input.Strategy}
	proposed, proposedStress, err := perpspaper.EvaluateFixedPlan(proposal.Baseline.Config, key, tape.Frames)
	if err != nil {
		return result, lastAt, err
	}
	result.Baseline, result.BaselineStress, result.Proposed, result.ProposedStress = &baseline, &baselineStress, &proposed, &proposedStress
	result.Reason = "modeled_plan_ineligible"
	if baseline.Eligible && baselineStress.Eligible && proposed.Eligible && proposedStress.Eligible {
		result.Status = "evaluated"
		result.Reason = "fixed_target_modeled_comparison"
	}
	return result, lastAt, nil
}
