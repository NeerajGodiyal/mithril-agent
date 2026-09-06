package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

const (
	perpsEpisodeStart = "perps.episode_started"
	perpsEpisodeEnd   = "perps.episode_finished"
)

// These records describe host attempts, not qualified experiments or fills.
// A start without a terminal record is unresolved, never a successful episode.
type shadowPerpsEpisodeConfig struct {
	Environment       perpspaper.Environment       `json:"environment"`
	Symbols           []perpspaper.Symbol          `json:"symbols"`
	RiskArm           perpspaper.RiskArm           `json:"risk_arm"`
	Collateral        uint64                       `json:"collateral_micros"`
	Cadence           time.Duration                `json:"cadence_ns"`
	Duration          time.Duration                `json:"duration_ns"`
	Archived          bool                         `json:"archived"`
	Once              bool                         `json:"once"`
	FinalizationHeads map[perpspaper.Symbol]string `json:"finalization_heads"`
}

type shadowPerpsEpisodeTape struct {
	Symbol             perpspaper.Symbol `json:"symbol"`
	TapeSHA256         string            `json:"tape_sha256,omitempty"`
	FinalizationSHA256 string            `json:"finalization_sha256,omitempty"`
	Frames             int               `json:"frames"`
}

type shadowPerpsEpisodeEvent struct {
	Version     uint32                    `json:"version"`
	PaperOnly   bool                      `json:"paper_only"`
	Authorized  bool                      `json:"authorized"`
	Config      *shadowPerpsEpisodeConfig `json:"config,omitempty"`
	StartSHA256 string                    `json:"start_sha256,omitempty"`
	Outcome     string                    `json:"outcome,omitempty"`
	Tapes       []shadowPerpsEpisodeTape  `json:"tapes,omitempty"`
}

type shadowPerpsEpisode struct {
	store  *journal.Store
	path   string
	start  journal.Record
	config shadowPerpsEpisodeConfig
}

func validateShadowPerpsEpisodeConfig(c shadowPerpsEpisodeConfig) bool {
	if (c.Environment != perpspaper.Mainnet && c.Environment != perpspaper.Testnet) ||
		!validShadowPerpsRiskArm(c.RiskArm) || c.Collateral == 0 || c.Collateral > perpspaper.MaxStartingCollateralMicros ||
		c.Cadence < time.Second || c.Cadence > 30*time.Second || c.Duration < time.Minute || c.Duration > 24*time.Hour ||
		len(c.Symbols) == 0 || len(c.Symbols) > 3 {
		return false
	}
	seen := make(map[perpspaper.Symbol]bool)
	for _, symbol := range c.Symbols {
		if (symbol != perpspaper.SOL && symbol != perpspaper.BTC && symbol != perpspaper.ETH) || seen[symbol] {
			return false
		}
		seen[symbol] = true
		head, ok := c.FinalizationHeads[symbol]
		if !ok || (head != "" && !validLowerSHA256(head)) {
			return false
		}
	}
	return len(c.FinalizationHeads) == len(c.Symbols)
}

// foldShadowPerpsEpisodes also accepts a final unresolved start. Only its next
// exclusive owner may append an interrupted outcome; readers cannot repair it.
func foldShadowPerpsEpisodes(records []journal.Record) (pending *journal.Record, count uint64, err error) {
	var last time.Time
	var config shadowPerpsEpisodeConfig
	for _, record := range records {
		if record.At.IsZero() || record.At.Before(last) {
			return nil, 0, errors.New("perps episode time regressed")
		}
		last = record.At
		if record.Type == journal.EventRotated {
			continue
		}
		var event shadowPerpsEpisodeEvent
		if strictjson.Decode(record.Payload, &event) != nil || event.Version != 1 || !event.PaperOnly || event.Authorized {
			return nil, 0, errors.New("perps episode record is invalid")
		}
		switch record.Type {
		case perpsEpisodeStart:
			count++
			if pending != nil || record.ActionID != strconv.FormatUint(count, 10) || event.Config == nil ||
				!validateShadowPerpsEpisodeConfig(*event.Config) || event.StartSHA256 != "" || event.Outcome != "" || len(event.Tapes) != 0 {
				return nil, 0, errors.New("perps episode start is invalid")
			}
			copy := record
			pending, config = &copy, *event.Config
		case perpsEpisodeEnd:
			if pending == nil || record.ActionID != pending.ActionID || event.StartSHA256 != pending.Hash || event.Config != nil ||
				(event.Outcome != "finished" && event.Outcome != "incomplete" && event.Outcome != "process_interrupted") {
				return nil, 0, errors.New("perps episode terminal is invalid")
			}
			if event.Outcome != "finished" || !config.Archived {
				if len(event.Tapes) != 0 {
					return nil, 0, errors.New("incomplete episode has tape evidence")
				}
			} else {
				if len(event.Tapes) != len(config.Symbols) {
					return nil, 0, errors.New("perps episode tape set is incomplete")
				}
				for i, tape := range event.Tapes {
					if tape.Symbol != config.Symbols[i] || tape.Frames < 0 || tape.Frames > shadowPerpsMaxFrames ||
						(tape.TapeSHA256 == "" && (tape.FinalizationSHA256 != "" || tape.Frames != 0)) ||
						(tape.TapeSHA256 != "" && (!validLowerSHA256(tape.TapeSHA256) || !validLowerSHA256(tape.FinalizationSHA256))) {
						return nil, 0, errors.New("perps episode tape binding is invalid")
					}
				}
			}
			pending = nil
		default:
			return nil, 0, errors.New("unexpected perps episode event")
		}
	}
	return pending, count, nil
}

func beginShadowPerpsEpisode(stateDir string, config shadowPerpsEpisodeConfig, at time.Time) (*shadowPerpsEpisode, error) {
	if at.IsZero() {
		return nil, errors.New("perps episode configuration is invalid")
	}
	path := filepath.Join(filepath.Dir(stateDir), filepath.Base(stateDir)+"-episodes.jsonl")
	store, err := journal.OpenRotating(path)
	if err != nil {
		return nil, err
	}
	config.FinalizationHeads = make(map[perpspaper.Symbol]string)
	for _, symbol := range config.Symbols {
		records, readErr := journal.ReadRecords(shadowPerpsFinalizationJournalPath(stateDir, symbol))
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return nil, errors.Join(readErr, store.Close())
		}
		if _, foldErr := foldShadowPerpsFinalizationReceipts(records); foldErr != nil {
			return nil, errors.Join(foldErr, store.Close())
		}
		config.FinalizationHeads[symbol] = ""
		if len(records) > 0 {
			config.FinalizationHeads[symbol] = records[len(records)-1].Hash
		}
	}
	if !validateShadowPerpsEpisodeConfig(config) {
		return nil, errors.Join(errors.New("perps episode configuration is invalid"), store.Close())
	}
	episode := &shadowPerpsEpisode{store: store, path: path, config: config}
	pending, count, err := foldShadowPerpsEpisodes(store.Records())
	if err == nil && pending != nil {
		episode.start = *pending
		err = episode.appendTerminal(at, "process_interrupted", nil)
	}
	if err == nil {
		episode.start, err = store.Append(at, perpsEpisodeStart, strconv.FormatUint(count+1, 10), shadowPerpsEpisodeEvent{
			Version: 1, PaperOnly: true, Config: &config,
		})
	}
	if err == nil {
		err = episode.publishPrefix()
	}
	if err != nil {
		return nil, errors.Join(err, store.Close())
	}
	return episode, nil
}

func (episode *shadowPerpsEpisode) publishPrefix() error {
	prefix, err := episode.store.DurablePrefix()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(prefix)
	if err != nil {
		return err
	}
	return securefile.ReplacePrivate(episode.path+".prefix.json", append(raw, '\n'), 4096)
}

func (episode *shadowPerpsEpisode) appendTerminal(at time.Time, outcome string, tapes []shadowPerpsEpisodeTape) error {
	_, err := episode.store.Append(at, perpsEpisodeEnd, episode.start.ActionID, shadowPerpsEpisodeEvent{
		Version: 1, PaperOnly: true, StartSHA256: episode.start.Hash, Outcome: outcome, Tapes: tapes,
	})
	return err
}

func (episode *shadowPerpsEpisode) finish(stateDir string, at time.Time, finished bool) error {
	outcome := "incomplete"
	var tapes []shadowPerpsEpisodeTape
	var evidenceErr error
	if finished {
		outcome = "finished"
		if episode.config.Archived {
			tapes, evidenceErr = episode.readTapes(stateDir, at)
			if evidenceErr != nil {
				outcome, tapes = "incomplete", nil
			}
		}
	}
	err := episode.appendTerminal(at, outcome, tapes)
	if err == nil {
		err = episode.publishPrefix()
	}
	return errors.Join(evidenceErr, err, episode.store.Close())
}

func (episode *shadowPerpsEpisode) readTapes(stateDir string, endedAt time.Time) ([]shadowPerpsEpisodeTape, error) {
	var tapes []shadowPerpsEpisodeTape
	for _, symbol := range episode.config.Symbols {
		binding := shadowPerpsEpisodeTape{Symbol: symbol}
		path := filepath.Join(stateDir, strings.ToLower(string(symbol))+"-tape.json")
		raw, err := securefile.ReadPrivate(path, shadowPerpsMaxFileBytes)
		if errors.Is(err, os.ErrNotExist) {
			tapes = append(tapes, binding)
			continue
		}
		if err != nil {
			return nil, err
		}
		var header shadowPerpsTape
		if strictjson.Decode(raw, &header) != nil || header.Config.Symbol != symbol || header.Config.Environment != episode.config.Environment ||
			header.Config.StartingCollateralMicros != episode.config.Collateral {
			return nil, errors.New("episode tape identity mismatch")
		}
		tape, _, err := readShadowPerpsTape(path, header.Config)
		if err != nil {
			return nil, err
		}
		_, digest, err := canonicalShadowPerpsTape(tape)
		if err != nil {
			return nil, err
		}
		records, err := journal.ReadRecords(shadowPerpsFinalizationJournalPath(stateDir, symbol))
		if err != nil {
			return nil, err
		}
		if _, err := foldShadowPerpsFinalizationReceipts(records); err != nil {
			return nil, err
		}
		afterBoundary := episode.config.FinalizationHeads[symbol] == ""
		for _, record := range records {
			if record.Hash == episode.config.FinalizationHeads[symbol] {
				afterBoundary = true
				continue
			}
			if afterBoundary && record.Type == shadowPerpsFinalizationEvent && record.ActionID == digest &&
				!record.At.Before(episode.start.At) && !record.At.After(endedAt) {
				binding.TapeSHA256, binding.FinalizationSHA256, binding.Frames = digest, record.Hash, len(tape.Frames)
			}
		}
		if binding.TapeSHA256 == "" {
			return nil, fmt.Errorf("%s episode has no new finalization receipt", symbol)
		}
		tapes = append(tapes, binding)
	}
	return tapes, nil
}
