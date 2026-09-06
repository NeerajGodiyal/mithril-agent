package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

// autoPerpsContextPaths selects by verified journal order, never performance.
// Finalization reads use the existing journal read lock; a concurrent append
// may fail the command, but cannot become a partial or fabricated observation.
func autoPerpsContextPaths(state string, symbol perpspaper.Symbol, now func() time.Time) ([]string, []string, error) {
	_, _, active, _, lock := shadowPerpsPlanPaths(state, symbol)
	var baseline shadowPerpsPlan
	err := withShadowLifecycleLock(lock, func() error {
		raw, err := securefile.ReadPrivate(active, shadowPerpsPlanMaxBytes)
		if err != nil {
			return err
		}
		var pointer shadowPerpsPlanPointer
		if strictjson.Decode(raw, &pointer) != nil || !validShadowPerpsPlanPointer(pointer, active) {
			return errors.New("perps auto context baseline pointer is invalid")
		}
		raw, err = securefile.ReadPrivate(pointer.PlanPath, shadowPerpsPlanMaxBytes)
		if err != nil {
			return err
		}
		if strictjson.Decode(raw, &baseline) != nil {
			return errors.New("perps auto context baseline is invalid")
		}
		baseline, _, _, err = loadBoundShadowPerpsPlanPointer(active, baseline.Environment, baseline.Config)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	records, err := journal.ReadRecords(shadowPerpsFinalizationJournalPath(state, symbol))
	if err != nil {
		return nil, nil, err
	}
	if _, err := foldShadowPerpsFinalizationReceipts(records); err != nil {
		return nil, nil, err
	}
	var paths []string
	for i := len(records) - 1; i >= 0 && len(paths) < 8; i-- {
		record := records[i]
		if record.Type != shadowPerpsFinalizationEvent {
			continue
		}
		path := filepath.Join(shadowPerpsCorpusDir(state, symbol), record.ActionID+".json")
		tape, _, err := readShadowPerpsCorpusTape(path)
		if errors.Is(err, os.ErrNotExist) {
			short, proofErr := perpsContextKnownShortTape(state, symbol, record)
			if proofErr != nil {
				return nil, nil, proofErr
			}
			if short {
				continue
			}
		}
		if err != nil {
			return nil, nil, err
		}
		if tape.Config.Environment != baseline.Environment || tape.Config.qualificationConfig() != baseline.Config || tape.Version != shadowPerpsTapeVersion || len(tape.Frames) < perpspaper.QualificationMinimumFrames {
			continue
		}
		paths = append(paths, path)
	}
	for i, j := 0, len(paths)-1; i < j; i, j = i+1, j-1 {
		paths[i], paths[j] = paths[j], paths[i]
	}
	if len(paths) == 0 {
		return nil, nil, errors.New("perps auto context has no compatible finalized training tapes")
	}
	// Verify selected tapes before resolving proposals or writing any outcomes.
	if _, _, err := perpsContextTraining(state, symbol, paths); err != nil {
		return nil, nil, err
	}
	evaluations, err := resolvePerpsContextProposals(state, symbol, now)
	return paths, evaluations, err
}

// A missing corpus file is skippable only with positive, hash-bound evidence
// that the completed attempt was too short to seal. Unknown loss is an error.
func perpsContextKnownShortTape(state string, symbol perpspaper.Symbol, finalization journal.Record) (bool, error) {
	path := filepath.Join(filepath.Dir(state), filepath.Base(state)+"-episodes.jsonl")
	raw, err := securefile.ReadPrivate(path+".prefix.json", 4096)
	if err != nil {
		return false, err
	}
	var prefix journal.DurablePrefix
	if strictjson.Decode(raw, &prefix) != nil {
		return false, errors.New("perps auto context episode prefix is invalid")
	}
	records, err := journal.ReadDurablePrefix(path, prefix)
	if err != nil {
		return false, err
	}
	if _, _, err := foldShadowPerpsEpisodes(records); err != nil {
		return false, err
	}
	var startAt time.Time
	for _, record := range records {
		if record.Type == perpsEpisodeStart {
			startAt = record.At
			continue
		}
		if record.Type != perpsEpisodeEnd {
			continue
		}
		var event shadowPerpsEpisodeEvent
		if err := strictjson.Decode(record.Payload, &event); err != nil {
			return false, err
		}
		for _, tape := range event.Tapes {
			if tape.Symbol == symbol && tape.TapeSHA256 == finalization.ActionID && tape.FinalizationSHA256 == finalization.Hash && tape.Frames > 0 && tape.Frames < perpspaper.QualificationMinimumFrames && !finalization.At.Before(startAt) && !finalization.At.After(record.At) {
				return true, nil
			}
		}
	}
	return false, nil
}

func resolvePerpsContextProposals(state string, symbol perpspaper.Symbol, now func() time.Time) ([]string, error) {
	directory := filepath.Join(filepath.Dir(state), "proposals", strings.ToLower(string(symbol)))
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// ponytail: reuse the existing 256-receipt ceiling; no scheduler or index.
	if len(entries) > 257 {
		return nil, errors.New("perps proposal directory exceeds bound")
	}
	var results []shadowPerpsProposalEvaluation
	count := 0
	for _, entry := range entries {
		if entry.Name() == "freeze.lock" {
			continue
		}
		count++
		if count > 256 {
			return nil, errors.New("perps proposal directory exceeds bound")
		}
		path := filepath.Join(directory, entry.Name())
		proposal, _, err := readPerpsProposal(path)
		if err != nil {
			return nil, err
		}
		if proposal.StateDir != state || proposal.Input.Symbol != symbol || entry.Name() != proposal.Input.HypothesisID+".json" {
			return nil, errors.New("perps auto context proposal identity mismatch")
		}
		var output bytes.Buffer
		if err := runShadowPerpsEvaluate([]string{"--proposal", path}, &output, now); err != nil {
			return nil, err
		}
		var result shadowPerpsProposalEvaluation
		if err := strictjson.Decode(output.Bytes(), &result); err != nil {
			return nil, err
		}
		if result.Status != "pending" {
			results = append(results, result)
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].ObservedAt.Equal(results[j].ObservedAt) {
			return results[i].ProposalSHA256 < results[j].ProposalSHA256
		}
		return results[i].ObservedAt.Before(results[j].ObservedAt)
	})
	if len(results) > 8 {
		results = results[len(results)-8:]
	}
	var paths []string
	for _, result := range results {
		paths = append(paths, filepath.Join(filepath.Dir(state), "proposal-evaluations", strings.ToLower(string(symbol)), result.ProposalSHA256+".json"))
	}
	return paths, nil
}
