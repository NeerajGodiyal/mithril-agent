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
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

const perpsProposalMaxBytes = 64 << 10
const perpsProposalUsage = `Usage: mithril-agent shadow perps-freeze --state-dir PATH --in PATH --tape PATH [--tape PATH ...]

Freeze a host-verified advisory proposal, never a plan selection or evaluation.
Input: hypothesis_id, symbol (SOL/BTC/ETH), risk_arm, strategy, rationale.
The host supplies 1–64 chronological immutable training tapes (at most 1500
frames each). Receipts are private and write-once under the state parent's
proposals/SYMBOL directory (maximum 256 receipts per symbol).
This is an initial advisory ceiling, not a week-long automated research loop.
The model must not control host paths or write the receipt directory.
The verified published episode prefix is not claimed to be latest. Its next
attempt ID is permanent: an already-started, failed or incomplete target must
not be replaced with a later winner. This command only freezes the proposal;
use perps-evaluate to inspect its assigned attempt.
Optional host --context PATH requires its exact selected tapes and expected
baseline. Future model integrations must use it; no-context operator use remains.
Every output remains pending, advisory-only, unauthorized and nonpromotable.`

type shadowPerpsProposalInput struct {
	HypothesisID string              `json:"hypothesis_id"`
	Symbol       perpspaper.Symbol   `json:"symbol"`
	RiskArm      perpspaper.RiskArm  `json:"risk_arm"`
	Strategy     perpspaper.Strategy `json:"strategy"`
	Rationale    string              `json:"rationale"`
}

type shadowPerpsProposalTraining struct {
	TapeSHA256         string    `json:"tape_sha256"`
	FinalizationSHA256 string    `json:"finalization_sha256"`
	KnownAt            time.Time `json:"known_at"`
	FirstTime          int64     `json:"first_time"`
	LastTime           int64     `json:"last_time"`
	Frames             int       `json:"frames"`
}

type shadowPerpsProposal struct {
	Version        uint32                        `json:"version"`
	Status         string                        `json:"status"`
	PaperOnly      bool                          `json:"paper_only"`
	Authorized     bool                          `json:"authorized"`
	Promotable     bool                          `json:"promotable"`
	Input          shadowPerpsProposalInput      `json:"input"`
	FrozenAt       time.Time                     `json:"frozen_at"`
	StateDir       string                        `json:"state_dir"`
	EpisodeJournal string                        `json:"episode_journal"`
	EpisodePrefix  journal.DurablePrefix         `json:"episode_prefix"`
	TargetEpisode  string                        `json:"target_episode"`
	BaselineSHA256 string                        `json:"baseline_sha256"`
	Baseline       shadowPerpsPlan               `json:"baseline"`
	Training       []shadowPerpsProposalTraining `json:"training"`
	ContextSHA256  string                        `json:"context_sha256,omitempty"`
	ContentSHA256  string                        `json:"content_sha256"`
}

func validPerpsProposalInput(input shadowPerpsProposalInput) bool {
	if len(input.HypothesisID) < 3 || len(input.HypothesisID) > 64 ||
		(input.Symbol != perpspaper.SOL && input.Symbol != perpspaper.BTC && input.Symbol != perpspaper.ETH) ||
		!validShadowPerpsRiskArm(input.RiskArm) || !validShadowPerpsStrategy(input.Strategy) ||
		len(input.Rationale) == 0 || len(input.Rationale) > 2000 || !utf8.ValidString(input.Rationale) || strings.TrimSpace(input.Rationale) != input.Rationale {
		return false
	}
	for _, c := range input.HypothesisID {
		if c != '-' && c != '_' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	for _, c := range input.Rationale {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

func proposalTapeIDs(paths []string, stateDir string, symbol perpspaper.Symbol) ([]string, error) {
	if len(paths) < 1 || len(paths) > 64 {
		return nil, errors.New("perps freeze requires 1–64 training tapes")
	}
	ids := make([]string, len(paths))
	seen := make(map[string]bool)
	for i, path := range paths {
		id := strings.TrimSuffix(filepath.Base(path), ".json")
		if !cleanResearchPath(path) || filepath.Dir(path) != shadowPerpsCorpusDir(stateDir, symbol) || !validLowerSHA256(id) || filepath.Base(path) != id+".json" || seen[id] {
			return nil, errors.New("perps training tape must be unique and in the host corpus")
		}
		ids[i] = id
		seen[id] = true
	}
	return ids, nil
}

func canonicalPerpsProposal(proposal shadowPerpsProposal) ([]byte, error) {
	if proposal.Version != 1 || proposal.Status != "pending_advisory" || !proposal.PaperOnly || proposal.Authorized || proposal.Promotable ||
		!validPerpsProposalInput(proposal.Input) || proposal.FrozenAt.IsZero() || !cleanResearchPath(proposal.StateDir) ||
		proposal.EpisodeJournal != filepath.Join(filepath.Dir(proposal.StateDir), filepath.Base(proposal.StateDir)+"-episodes.jsonl") ||
		len(proposal.Training) < 1 || len(proposal.Training) > 64 {
		return nil, errors.New("perps proposal receipt is invalid")
	}
	if proposal.ContextSHA256 != "" && !validLowerSHA256(proposal.ContextSHA256) {
		return nil, errors.New("perps proposal context digest is invalid")
	}
	_, baselineSHA, err := canonicalShadowPerpsPlan(proposal.Baseline)
	if err != nil || baselineSHA != proposal.BaselineSHA256 || proposal.Baseline.Config.Symbol != proposal.Input.Symbol {
		return nil, errors.New("perps proposal baseline is invalid")
	}
	if id, err := strconv.ParseUint(proposal.TargetEpisode, 10, 64); err != nil || id == 0 || strconv.FormatUint(id, 10) != proposal.TargetEpisode {
		return nil, errors.New("perps proposal target is invalid")
	}
	seen := make(map[string]bool)
	var last int64
	for i, tape := range proposal.Training {
		if !validLowerSHA256(tape.TapeSHA256) || !validLowerSHA256(tape.FinalizationSHA256) || seen[tape.TapeSHA256] || tape.KnownAt.IsZero() || tape.KnownAt.After(proposal.FrozenAt) ||
			tape.Frames < perpspaper.QualificationMinimumFrames || tape.Frames > shadowPerpsMaxFrames || tape.FirstTime <= 0 || tape.LastTime < tape.FirstTime ||
			tape.LastTime > proposal.FrozenAt.Add(shadowPerpsMaxClockSkew).UnixMilli() || i > 0 && tape.FirstTime <= last {
			return nil, errors.New("perps proposal training is invalid")
		}
		seen[tape.TapeSHA256] = true
		last = tape.LastTime
	}
	proposal.ContentSHA256 = ""
	digest, err := shadowPerpsJSONSHA256(proposal)
	if err != nil {
		return nil, err
	}
	proposal.ContentSHA256 = digest
	raw, err := json.Marshal(proposal)
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if len(raw) > perpsProposalMaxBytes {
		return nil, errors.New("perps proposal receipt exceeds size limit")
	}
	return raw, nil
}

func readPerpsProposal(path string) (shadowPerpsProposal, []byte, error) {
	raw, err := securefile.ReadPrivate(path, perpsProposalMaxBytes)
	if err != nil {
		return shadowPerpsProposal{}, nil, err
	}
	var proposal shadowPerpsProposal
	if strictjson.Decode(raw, &proposal) != nil {
		return proposal, nil, errors.New("perps proposal JSON is invalid")
	}
	canonical, err := canonicalPerpsProposal(proposal)
	if err != nil || !bytes.Equal(raw, canonical) {
		return proposal, nil, errors.New("perps proposal receipt digest or encoding is invalid")
	}
	records, err := journal.ReadDurablePrefix(proposal.EpisodeJournal, proposal.EpisodePrefix)
	if err != nil {
		return proposal, nil, err
	}
	if err := verifyPerpsProposalBoundary(proposal, records); err != nil {
		return proposal, nil, err
	}
	return proposal, raw, nil
}

func verifyPerpsProposalBoundary(proposal shadowPerpsProposal, records []journal.Record) error {
	_, count, err := foldShadowPerpsEpisodes(records)
	if err != nil || count == 0 || proposal.TargetEpisode != strconv.FormatUint(count+1, 10) || records[len(records)-1].At.After(proposal.FrozenAt) {
		return errors.New("perps proposal episode boundary mismatch")
	}
	var start shadowPerpsEpisodeEvent
	for _, record := range records {
		if record.Type == perpsEpisodeStart {
			if strictjson.Decode(record.Payload, &start) != nil {
				return errors.New("perps proposal episode start is invalid")
			}
		}
	}
	if start.Config == nil || !start.Config.Archived || start.Config.Environment != proposal.Baseline.Environment || start.Config.Collateral != proposal.Baseline.Config.StartingCollateralMicros {
		return errors.New("perps proposal needs a compatible archived episode stream")
	}
	for _, symbol := range start.Config.Symbols {
		if symbol == proposal.Input.Symbol {
			return nil
		}
	}
	return errors.New("perps proposal symbol is outside episode stream")
}

func runShadowPerpsFreeze(args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("shadow perps-freeze", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	state := flags.String("state-dir", "", "host-controlled current state directory")
	inputPath := flags.String("in", "", "private model proposal JSON")
	contextPath := flags.String("context", "", "optional immutable host context")
	var paths repeatedPathFlag
	flags.Var(&paths, "tape", "host-selected immutable training tape, chronological")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, perpsProposalUsage)
		}
		return err
	}
	if flags.NArg() != 0 || !cleanResearchPath(*state) || !cleanResearchPath(*inputPath) || (*contextPath != "" && !cleanResearchPath(*contextPath)) {
		return errors.New("perps freeze requires clean absolute --state-dir and --in")
	}
	raw, err := securefile.ReadPrivate(*inputPath, 8192)
	if err != nil {
		return err
	}
	var input shadowPerpsProposalInput
	if strictjson.Decode(raw, &input) != nil || !validPerpsProposalInput(input) {
		return errors.New("perps proposal input is invalid")
	}
	ids, err := proposalTapeIDs(paths, *state, input.Symbol)
	if err != nil {
		return err
	}
	var context *shadowPerpsContext
	contextDigest := ""
	if *contextPath != "" {
		verified, _, err := readPerpsContext(*contextPath, *state)
		if err != nil {
			return err
		}
		if verified.Symbol != input.Symbol || len(verified.Training) != len(ids) {
			return errors.New("perps context selection differs from proposal")
		}
		for i, id := range ids {
			if verified.Training[i].TapeSHA256 != id {
				return errors.New("perps context training selection differs")
			}
		}
		context, contextDigest = &verified, verified.ContentSHA256
	}
	root := filepath.Join(filepath.Dir(*state), "proposals")
	directory := filepath.Join(root, strings.ToLower(string(input.Symbol)))
	for _, dir := range []string{root, directory} {
		if err := ensureShadowPerpsPrivateDirectory(dir); err != nil {
			return err
		}
	}
	path := filepath.Join(directory, input.HypothesisID+".json")
	return withShadowLifecycleLock(filepath.Join(directory, "freeze.lock"), func() error {
		stored, encoded, err := readPerpsProposal(path)
		if err == nil {
			if stored.Input != input || stored.StateDir != *state || len(stored.Training) != len(ids) || stored.ContextSHA256 != contextDigest {
				return errors.New("perps hypothesis already frozen differently")
			}
			for i, id := range ids {
				if stored.Training[i].TapeSHA256 != id {
					return errors.New("perps hypothesis training already frozen differently")
				}
			}
			_, err = output.Write(encoded)
			return err
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// ponytail: bounded receipt scan; use a rotating reservation journal
		// before enabling unattended research beyond 256 proposals per symbol.
		entries, err := os.ReadDir(directory)
		if err != nil {
			return err
		}
		if len(entries) > 256 {
			return errors.New("perps proposal directory has reached its 256 receipt limit")
		}
		var existing []shadowPerpsProposal
		for _, entry := range entries {
			if entry.Name() == "freeze.lock" {
				continue
			}
			other, _, err := readPerpsProposal(filepath.Join(directory, entry.Name()))
			if err != nil {
				return err
			}
			existing = append(existing, other)
		}
		if len(existing) >= 256 {
			return errors.New("perps proposal directory has reached its 256 receipt limit")
		}
		proposal, err := preparePerpsProposal(*state, input, paths, now, context)
		if err != nil {
			return err
		}
		for _, other := range existing {
			if other.EpisodeJournal == proposal.EpisodeJournal && other.TargetEpisode == proposal.TargetEpisode {
				return errors.New("perps target already has a frozen proposal")
			}
		}
		encoded, err = canonicalPerpsProposal(proposal)
		if err != nil {
			return err
		}
		if err := securefile.CreatePrivate(path, encoded, perpsProposalMaxBytes); err != nil {
			return err
		}
		_, err = output.Write(encoded)
		return err
	})
}

func preparePerpsProposal(state string, input shadowPerpsProposalInput, paths []string, now func() time.Time, context *shadowPerpsContext) (shadowPerpsProposal, error) {
	proposal := shadowPerpsProposal{Version: 1, Status: "pending_advisory", PaperOnly: true, Input: input, StateDir: state,
		EpisodeJournal: filepath.Join(filepath.Dir(state), filepath.Base(state)+"-episodes.jsonl")}
	records, err := journal.ReadRecords(shadowPerpsFinalizationJournalPath(state, input.Symbol))
	if err != nil {
		return proposal, err
	}
	if _, err := foldShadowPerpsFinalizationReceipts(records); err != nil {
		return proposal, err
	}
	var config shadowPerpsTapeConfig
	for i, path := range paths {
		tape, digest, err := readShadowPerpsCorpusTape(path)
		if err != nil {
			return proposal, err
		}
		if tape.Version != shadowPerpsTapeVersion || tape.Config.Symbol != input.Symbol || len(tape.Frames) < perpspaper.QualificationMinimumFrames {
			return proposal, errors.New("perps training needs current completed corpus tapes")
		}
		if i == 0 {
			config = tape.Config
		} else if !compatibleShadowPerpsTapes(config, tape.Config) {
			return proposal, errors.New("perps training configurations differ")
		}
		training := shadowPerpsProposalTraining{TapeSHA256: digest, Frames: len(tape.Frames), FirstTime: tape.Frames[0].Book.Time, LastTime: tape.Frames[len(tape.Frames)-1].Book.Time}
		for _, record := range records {
			if record.Type == shadowPerpsFinalizationEvent && record.ActionID == digest {
				training.FinalizationSHA256 = record.Hash
				training.KnownAt = record.At
			}
		}
		if err := validatePerpsProposalFrameTimes(tape.Frames, time.Time{}, training.KnownAt); err != nil {
			return proposal, err
		}
		proposal.Training = append(proposal.Training, training)
	}
	_, _, active, _, lock := shadowPerpsPlanPaths(state, input.Symbol)
	err = withShadowLifecycleLock(lock, func() error {
		var err error
		proposal.Baseline, proposal.BaselineSHA256, _, err = loadBoundShadowPerpsPlanPointer(active, config.Environment, config.qualificationConfig())
		if err == nil && context != nil && proposal.BaselineSHA256 != context.BaselineSHA256 {
			return errors.New("perps baseline changed since host context")
		}
		return err
	})
	if err != nil {
		return proposal, err
	}
	raw, err := securefile.ReadPrivate(proposal.EpisodeJournal+".prefix.json", 4096)
	if err != nil {
		return proposal, err
	}
	if strictjson.Decode(raw, &proposal.EpisodePrefix) != nil {
		return proposal, errors.New("perps episode prefix is invalid")
	}
	episodes, err := journal.ReadDurablePrefix(proposal.EpisodeJournal, proposal.EpisodePrefix)
	if err != nil {
		return proposal, err
	}
	_, count, err := foldShadowPerpsEpisodes(episodes)
	if err != nil || count == 0 {
		return proposal, errors.New("perps episode boundary is invalid")
	}
	proposal.TargetEpisode = strconv.FormatUint(count+1, 10)
	proposal.FrozenAt = now().UTC()
	if context != nil {
		if context.ContextKnownAt.After(proposal.FrozenAt) {
			return proposal, errors.New("perps context became known after freeze")
		}
		proposal.ContextSHA256 = context.ContentSHA256
	}
	if episodes[len(episodes)-1].At.After(proposal.FrozenAt) {
		return proposal, errors.New("perps episode boundary is future dated")
	}
	if err := verifyPerpsProposalBoundary(proposal, episodes); err != nil {
		return proposal, err
	}
	return proposal, nil
}
