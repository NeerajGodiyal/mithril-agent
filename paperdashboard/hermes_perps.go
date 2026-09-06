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
	FinishedAt     time.Time             `json:"finished_at"`
	Markets        []HermesPerpsMarket   `json:"markets"`
	Lifecycle      *HermesPerpsLifecycle `json:"lifecycle,omitempty"`
	LifecycleError bool                  `json:"lifecycle_error,omitempty"`
}

// HermesPerpsLifecycle is a host-recorded history snapshot, never the active
// plan or permission to select one. Market warnings also cover older history.
type HermesPerpsLifecycle struct {
	AsOf             time.Time                      `json:"as_of"`
	SelectionEnabled *bool                          `json:"selection_enabled"`
	Markets          []HermesPerpsLifecycleMarket   `json:"markets"`
	Proposals        []HermesPerpsLifecycleProposal `json:"proposals"`
}

type HermesPerpsLifecycleMarket struct {
	Symbol                       string  `json:"symbol"`
	RecordedProposals            *uint64 `json:"recorded_proposals"`
	ManualReconciliationRequired *bool   `json:"manual_reconciliation_required"`
}

type HermesPerpsLifecycleProposal struct {
	Symbol               string                          `json:"symbol"`
	ProposalSHA256       string                          `json:"proposal_sha256"`
	TargetEpisode        string                          `json:"target_episode"`
	FrozenAt             time.Time                       `json:"frozen_at"`
	EvaluationStatus     string                          `json:"evaluation_status"`
	EvaluationObservedAt *time.Time                      `json:"evaluation_observed_at,omitempty"`
	EvaluationSHA256     string                          `json:"evaluation_sha256,omitempty"`
	SelectionStatus      string                          `json:"selection_status"`
	PlanSHA256           string                          `json:"plan_sha256,omitempty"`
	Comparison           *HermesPerpsLifecycleComparison `json:"comparison,omitempty"`
}

// HermesPerpsLifecycleComparison reports verified modeled outcomes, not
// qualification or fills on a real venue. Nil lanes remain explicitly unscored.
type HermesPerpsLifecycleComparison struct {
	Proposed       *HermesPerpsLifecycleScore `json:"proposed"`
	Baseline       *HermesPerpsLifecycleScore `json:"baseline"`
	ProposedStress *HermesPerpsLifecycleScore `json:"proposed_stress"`
	BaselineStress *HermesPerpsLifecycleScore `json:"baseline_stress"`
}

type HermesPerpsLifecycleScore struct {
	FilledOrders    string `json:"filled_orders"`
	ClosedPositions string `json:"closed_positions"`
	NetPnLMicros    string `json:"net_pnl_micros"`
	FeesPaidMicros  string `json:"fees_paid_micros"`
}

func validHermesPerpsLifecycleScore(score *HermesPerpsLifecycleScore) bool {
	if score == nil {
		return true
	}
	var amounts [3]uint64
	for i, text := range []string{score.FilledOrders, score.ClosedPositions, score.FeesPaidMicros} {
		value, err := strconv.ParseUint(text, 10, 64)
		if err != nil || strconv.FormatUint(value, 10) != text {
			return false
		}
		amounts[i] = value
	}
	pnl, err := strconv.ParseInt(score.NetPnLMicros, 10, 64)
	return err == nil && strconv.FormatInt(pnl, 10) == score.NetPnLMicros && amounts[1] <= amounts[0]
}

func validHermesPerpsLifecycle(value *HermesPerpsLifecycle, finished time.Time) bool {
	if value == nil {
		return true
	}
	if value.AsOf.IsZero() || value.AsOf.Location() != time.UTC || value.AsOf.After(finished) || value.SelectionEnabled == nil || len(value.Markets) != 3 || value.Proposals == nil || len(value.Proposals) > 9 {
		return false
	}
	markets := make(map[string]HermesPerpsLifecycleMarket, 3)
	for _, market := range value.Markets {
		if (market.Symbol != "SOL" && market.Symbol != "BTC" && market.Symbol != "ETH") || market.RecordedProposals == nil || *market.RecordedProposals > 256 || market.ManualReconciliationRequired == nil {
			return false
		}
		if _, exists := markets[market.Symbol]; exists {
			return false
		}
		markets[market.Symbol] = market
	}
	seen := make(map[string]bool, len(value.Proposals))
	counts := make(map[string]uint64, 3)
	last := make(map[string]time.Time, 3)
	for _, proposal := range value.Proposals {
		market, ok := markets[proposal.Symbol]
		id, err := strconv.ParseUint(proposal.TargetEpisode, 10, 64)
		if !ok || err != nil || id == 0 || strconv.FormatUint(id, 10) != proposal.TargetEpisode || !validSHA256(proposal.ProposalSHA256) || seen[proposal.ProposalSHA256] || proposal.FrozenAt.IsZero() || proposal.FrozenAt.Location() != time.UTC || proposal.FrozenAt.After(value.AsOf) || proposal.FrozenAt.Before(last[proposal.Symbol]) {
			return false
		}
		seen[proposal.ProposalSHA256] = true
		counts[proposal.Symbol]++
		if counts[proposal.Symbol] > 3 || counts[proposal.Symbol] > *market.RecordedProposals {
			return false
		}
		last[proposal.Symbol] = proposal.FrozenAt
		observed := proposal.EvaluationObservedAt
		if observed != nil && (observed.IsZero() || observed.Location() != time.UTC || observed.Before(proposal.FrozenAt) || observed.After(value.AsOf)) {
			return false
		}
		switch proposal.EvaluationStatus {
		case "pending":
			if observed == nil || proposal.EvaluationSHA256 != "" {
				return false
			}
		case "evaluated", "unevaluable":
			if observed == nil || !validSHA256(proposal.EvaluationSHA256) {
				return false
			}
		case "unavailable":
			if observed != nil || proposal.EvaluationSHA256 != "" {
				return false
			}
		default:
			return false
		}
		if comparison := proposal.Comparison; comparison != nil {
			if proposal.EvaluationStatus != "evaluated" || !validHermesPerpsLifecycleScore(comparison.Proposed) || !validHermesPerpsLifecycleScore(comparison.Baseline) || !validHermesPerpsLifecycleScore(comparison.ProposedStress) || !validHermesPerpsLifecycleScore(comparison.BaselineStress) {
				return false
			}
		}
		switch proposal.SelectionStatus {
		case "selected_previously", "retired":
			if proposal.EvaluationStatus != "evaluated" || !validSHA256(proposal.PlanSHA256) {
				return false
			}
		case "not_selected":
			if (proposal.EvaluationStatus != "evaluated" && proposal.EvaluationStatus != "unevaluable") || proposal.PlanSHA256 != "" {
				return false
			}
		case "paused":
			if *value.SelectionEnabled || proposal.PlanSHA256 != "" {
				return false
			}
		case "not_attempted":
			if !*value.SelectionEnabled || proposal.PlanSHA256 != "" {
				return false
			}
		case "needs_attention":
			if !*market.ManualReconciliationRequired || proposal.PlanSHA256 != "" {
				return false
			}
		default:
			return false
		}
	}
	return true
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
	if (stored.LifecycleError && stored.Lifecycle != nil) || !validHermesPerpsLifecycle(stored.Lifecycle, stored.FinishedAt) {
		return nil, errors.New("hermes perps lifecycle is invalid")
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
