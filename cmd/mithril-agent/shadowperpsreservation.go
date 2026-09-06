package main

import (
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

	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

const perpsReservationUsage = `Usage: mithril-agent shadow perps-reservation --state-dir PATH --symbol SOL|BTC|ETH

Read the published episode prefix and existing canonical frozen proposals before
inference. Reports reserved or unreserved for exactly the observed count+1 target.
This snapshot may be stale: it creates no reservation, repairs nothing, and does
not replace the freeze command's collision check. A saved proposal need not come
from Hermes. No training eligibility, selection, or real-trading authorization.`

type shadowPerpsReservation struct {
	Version             uint32              `json:"version"`
	Status              string              `json:"status"`
	PaperOnly           bool                `json:"paper_only"`
	Authorized          bool                `json:"authorized"`
	Promotable          bool                `json:"promotable"`
	Symbol              perpspaper.Symbol   `json:"symbol"`
	TargetEpisode       string              `json:"target_episode"`
	ObservedAt          time.Time           `json:"observed_at"`
	EpisodePrefixSHA256 string              `json:"episode_prefix_sha256"`
	ProposalSHA256      string              `json:"proposal_sha256,omitempty"`
	ContextSHA256       string              `json:"context_sha256,omitempty"`
	HypothesisID        string              `json:"hypothesis_id,omitempty"`
	Strategy            perpspaper.Strategy `json:"strategy,omitempty"`
	RiskArm             perpspaper.RiskArm  `json:"risk_arm,omitempty"`
	FrozenAt            *time.Time          `json:"frozen_at,omitempty"`
}

func runShadowPerpsReservation(args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("shadow perps-reservation", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	state := flags.String("state-dir", "", "host-controlled current state directory")
	symbolText := flags.String("symbol", "", "SOL, BTC, or ETH")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, perpsReservationUsage)
		}
		return err
	}
	symbol := perpspaper.Symbol(*symbolText)
	if flags.NArg() != 0 || !cleanResearchPath(*state) || (symbol != perpspaper.SOL && symbol != perpspaper.BTC && symbol != perpspaper.ETH) {
		return errors.New("perps reservation requires clean --state-dir and SOL, BTC, or ETH --symbol")
	}
	path := filepath.Join(filepath.Dir(*state), filepath.Base(*state)+"-episodes.jsonl")
	raw, err := securefile.ReadPrivate(path+".prefix.json", 4096)
	if err != nil {
		return err
	}
	var prefix journal.DurablePrefix
	if strictjson.Decode(raw, &prefix) != nil {
		return errors.New("perps reservation prefix is invalid")
	}
	records, err := journal.ReadDurablePrefix(path, prefix)
	if err != nil {
		return err
	}
	_, count, err := foldShadowPerpsEpisodes(records)
	if err != nil || count == 0 || count == ^uint64(0) {
		return errors.New("perps reservation episode boundary is invalid")
	}
	var latest shadowPerpsEpisodeEvent
	for _, record := range records {
		if record.Type == perpsEpisodeStart {
			if err := strictjson.Decode(record.Payload, &latest); err != nil {
				return err
			}
		}
	}
	compatible := false
	if latest.Config != nil && latest.Config.Archived {
		for _, candidate := range latest.Config.Symbols {
			if candidate == symbol {
				compatible = true
			}
		}
	}
	if !compatible {
		return errors.New("perps reservation requires an archived stream containing the symbol")
	}
	result := shadowPerpsReservation{Version: 1, Status: "unreserved", PaperOnly: true, Symbol: symbol, TargetEpisode: strconv.FormatUint(count+1, 10)}
	result.EpisodePrefixSHA256, err = shadowPerpsJSONSHA256(prefix)
	if err != nil {
		return err
	}
	root := filepath.Join(filepath.Dir(*state), "proposals")
	directory := filepath.Join(root, strings.ToLower(string(symbol)))
	missing := false
	for _, dir := range []string{root, directory} {
		info, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			missing = true
			break
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
			return errors.New("perps proposal directory is not private")
		}
		if err := secureexec.ValidateProtectedDirectory(dir); err != nil {
			return err
		}
	}
	var latestFrozen time.Time
	if !missing {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return err
		}
		// ponytail: same 256-receipt ceiling as freeze; no separate reservation index.
		if len(entries) > 257 {
			return errors.New("perps proposal directory exceeds bound")
		}
		receipts := 0
		for _, entry := range entries {
			if entry.Name() == "freeze.lock" {
				continue
			}
			receipts++
			if receipts > 256 {
				return errors.New("perps proposal directory exceeds bound")
			}
			proposal, _, err := readPerpsProposal(filepath.Join(directory, entry.Name()))
			if err != nil {
				return err
			}
			if proposal.StateDir != *state || proposal.Input.Symbol != symbol || entry.Name() != proposal.Input.HypothesisID+".json" || proposal.EpisodeJournal != path {
				return errors.New("perps reservation proposal identity mismatch")
			}
			if proposal.FrozenAt.After(latestFrozen) {
				latestFrozen = proposal.FrozenAt
			}
			if proposal.TargetEpisode != result.TargetEpisode {
				continue
			}
			if result.Status == "reserved" {
				return errors.New("perps target has conflicting frozen proposals")
			}
			result.Status, result.ProposalSHA256, result.ContextSHA256 = "reserved", proposal.ContentSHA256, proposal.ContextSHA256
			result.HypothesisID, result.Strategy, result.RiskArm = proposal.Input.HypothesisID, proposal.Input.Strategy, proposal.Input.RiskArm
			frozen := proposal.FrozenAt
			result.FrozenAt = &frozen
		}
	}
	result.ObservedAt = now().UTC()
	if result.ObservedAt.IsZero() || result.ObservedAt.Before(records[len(records)-1].At) || result.ObservedAt.Before(latestFrozen) {
		return errors.New("perps reservation observation precedes its evidence")
	}
	return json.NewEncoder(output).Encode(result)
}
