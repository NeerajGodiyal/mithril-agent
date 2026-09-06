package paperdashboard

import (
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

// HermesPerps describes the last recorded proposal attempt, not current
// activity, a selected paper plan, or authority to trade.
type HermesPerps struct {
	FinishedAt time.Time           `json:"finished_at"`
	Markets    []HermesPerpsMarket `json:"markets"`
}

type HermesPerpsMarket struct {
	Symbol           string     `json:"symbol"`
	Status           string     `json:"status"`
	Phase            string     `json:"phase,omitempty"`
	TargetEpisode    string     `json:"target_episode,omitempty"`
	Strategy         string     `json:"strategy,omitempty"`
	RiskArm          string     `json:"risk_arm,omitempty"`
	ContextSHA256    string     `json:"context_sha256,omitempty"`
	ProposalSHA256   string     `json:"proposal_sha256,omitempty"`
	TrainingTapes    *uint64    `json:"training_tapes,omitempty"`
	ResolvedOutcomes *uint64    `json:"resolved_outcomes,omitempty"`
	FrozenAt         *time.Time `json:"frozen_at,omitempty"`
}

func readHermesPerps(path string, now time.Time) (*HermesPerps, error) {
	raw, err := securefile.ReadPrivate(path, 16<<10)
	if err != nil {
		return nil, err
	}
	var stored struct {
		Version    uint32 `json:"version"`
		PaperOnly  bool   `json:"paper_only"`
		Authorized *bool  `json:"authorized"`
		Promotable *bool  `json:"promotable"`
		RunID      string `json:"run_id"`
		HermesPerps
	}
	if strictjson.Decode(raw, &stored) != nil {
		return nil, errors.New("hermes perps projection JSON is invalid")
	}
	runID, err := hex.DecodeString(stored.RunID)
	if err != nil || len(runID) != 16 || stored.RunID != strings.ToLower(stored.RunID) || stored.Version != 1 || !stored.PaperOnly || stored.Authorized == nil || *stored.Authorized || stored.Promotable == nil || *stored.Promotable || stored.FinishedAt.IsZero() || stored.FinishedAt.After(now) || len(stored.Markets) < 1 || len(stored.Markets) > 3 {
		return nil, errors.New("hermes perps projection envelope is invalid")
	}
	seen := make(map[string]bool)
	for _, market := range stored.Markets {
		if (market.Symbol != "SOL" && market.Symbol != "BTC" && market.Symbol != "ETH") || seen[market.Symbol] {
			return nil, errors.New("hermes perps market is invalid")
		}
		seen[market.Symbol] = true
		if market.Status == "pending_advisory" || market.Status == "already_saved" {
			target, err := strconv.ParseUint(market.TargetEpisode, 10, 64)
			if err != nil || target == 0 || strconv.FormatUint(target, 10) != market.TargetEpisode || market.Phase != "" || !validSHA256(market.ProposalSHA256) {
				return nil, errors.New("hermes perps pending identity is invalid")
			}
			if market.Status == "pending_advisory" {
				if !validSHA256(market.ContextSHA256) || market.TrainingTapes == nil || *market.TrainingTapes < 1 || *market.TrainingTapes > 8 || market.ResolvedOutcomes == nil || *market.ResolvedOutcomes > 8 || market.FrozenAt != nil {
					return nil, errors.New("hermes perps new proposal evidence is invalid")
				}
			} else if (market.ContextSHA256 != "" && !validSHA256(market.ContextSHA256)) || market.TrainingTapes != nil || market.ResolvedOutcomes != nil || market.FrozenAt == nil || market.FrozenAt.IsZero() || market.FrozenAt.After(stored.FinishedAt) {
				return nil, errors.New("hermes perps existing proposal evidence is invalid")
			}
			switch market.Strategy {
			case "momentum", "mean_reversion", "breakout", "regime":
			default:
				return nil, errors.New("hermes perps strategy is invalid")
			}
			switch market.RiskArm {
			case "conservative", "balanced", "experimental":
			default:
				return nil, errors.New("hermes perps risk arm is invalid")
			}
			continue
		}
		switch market.Status {
		case "unavailable", "cleanup_required", "interrupted":
		default:
			return nil, errors.New("hermes perps attempt status is invalid")
		}
		switch market.Phase {
		case "prepare_directories", "check_reservation", "prepare_context", "model_proposal", "export_session", "verify_model_output", "freeze_proposal", "record_invocation":
		default:
			return nil, errors.New("hermes perps attempt phase is invalid")
		}
		if market.TargetEpisode != "" || market.Strategy != "" || market.RiskArm != "" || market.ContextSHA256 != "" || market.ProposalSHA256 != "" || market.TrainingTapes != nil || market.ResolvedOutcomes != nil || market.FrozenAt != nil {
			return nil, errors.New("hermes perps failed attempt claims a proposal")
		}
	}
	return &stored.HermesPerps, nil
}
