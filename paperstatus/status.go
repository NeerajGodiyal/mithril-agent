// Package paperstatus stores a bounded, secret-free projection of meaningful
// paper-trading events. The hash-chained shadow journal remains authoritative.
package paperstatus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

const (
	legacyVersion           = 1
	settingsVersion         = 2
	accountingVersion       = 3
	qualificationVersion    = 4
	multiTapeVersion        = 5
	perpsPlanVersion        = 6
	decisionSourceVersion   = 7
	latestCompletedVersion  = 8
	decisionEvidenceVersion = 9
	Version                 = decisionEvidenceVersion
	MaxEvents               = 64
	MaxHistoryPoints        = 144
	MaxMessageBytes         = 3000
	maxCurrentBytes         = 512
	maxSnapshotBytes        = 256 << 10
	historyInterval         = 10 * time.Minute
	messagePrefix           = "PAPER ·"
	legacyMessagePrefix     = "PAPER SIMULATION —"
	legacyDisclaimer        = "No transaction was signed or submitted."
	UnconfiguredCurrent     = "PAPER · NOT ENABLED"
)

const (
	KindStrategyActive  = "strategy_active"
	KindStrategyChanged = "strategy_changed"
	KindOrderOpened     = "order_opened"
	KindOrderFilled     = "order_filled"
	KindOrderRefused    = "order_refused"
	KindOrderMissed     = "order_missed"
	KindRiskHalted      = "risk_halted"
	KindDataUnavailable = "data_unavailable"
	KindDataRestored    = "data_restored"
	KindPeriodClosed    = "period_closed"
	KindExperimentDone  = "experiment_completed"
)

// Event is safe to present to an operator. ID is deterministic so a Telegram
// process can deduplicate delivery across its own restarts.
type Event struct {
	ID      string    `json:"id"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
}

type Snapshot struct {
	Version       uint64    `json:"version"`
	ObservedAt    time.Time `json:"observed_at"`
	DroppedEvents uint64    `json:"dropped_events"`
	Events        []Event   `json:"events"`
	// Current is a compact, non-alerting view of what the paper engine is doing
	// now. It lets an operator distinguish a quiet market from a stopped loop
	// without receiving a Telegram message for every observation.
	Current string `json:"current,omitempty"`
	// Summary is the small numeric projection needed to combine several paper
	// markets without parsing human-facing Telegram text. The journal remains
	// authoritative; this is only a read-only current view.
	Summary *CurrentSummary `json:"summary,omitempty"`
	// History is a bounded, current-day performance projection for operator
	// charts. It contains no journal records, provider details, or authority.
	History []PerformancePoint `json:"history,omitempty"`
	// LatestCompleted keeps the most recent terminal perps result visible while
	// a new bounded recording is collecting. It is a compact receipt, not a
	// recursive copy of the live snapshot or an execution authority.
	LatestCompleted *CompletedSnapshot `json:"latest_completed,omitempty"`
}

// CompletedSnapshot binds one terminal event to the numeric perps summary it
// finalized. It deliberately excludes current prose, history, and nested state.
type CompletedSnapshot struct {
	ObservedAt time.Time      `json:"observed_at"`
	EventID    string         `json:"event_id"`
	Summary    CurrentSummary `json:"summary"`
}

type CurrentSummary struct {
	Market              string `json:"market"`
	Instrument          string `json:"instrument,omitempty"`
	RiskProfile         string `json:"risk_profile,omitempty"`
	PositionDirection   string `json:"position_direction,omitempty"`
	LeverageBPS         uint32 `json:"leverage_bps,omitempty"`
	ValueUnit           string `json:"value_unit,omitempty"`
	Day                 string `json:"day"`
	InstructionSHA256   string `json:"instruction_sha256,omitempty"`
	TickSeconds         uint64 `json:"tick_seconds"`
	OpeningEquityMicros uint64 `json:"opening_equity_micros"`
	EquityMicros        uint64 `json:"equity_micros"`
	DeficitMicros       uint64 `json:"deficit_micros,omitempty"`
	HoldBenchmarkMicros uint64 `json:"hold_benchmark_micros"`
	// Realized is the result from inventory already sold, after modeled fees.
	// Unrealized is the mark-to-market result still held in open inventory.
	AccountingTracked     bool              `json:"accounting_tracked,omitempty"`
	RealizedMicros        int64             `json:"realized_micros,omitempty"`
	UnrealizedMicros      int64             `json:"unrealized_micros,omitempty"`
	FeesMicros            int64             `json:"fees_micros,omitempty"`
	FundingTracked        bool              `json:"funding_tracked,omitempty"`
	FundingMicros         int64             `json:"funding_micros,omitempty"`
	TurnoverMicros        uint64            `json:"turnover_micros,omitempty"`
	DrawdownMicros        uint64            `json:"drawdown_micros,omitempty"`
	MaxDrawdownMicros     uint64            `json:"max_drawdown_micros,omitempty"`
	Checks                uint64            `json:"checks"`
	Signals               uint64            `json:"signals"`
	Trades                uint64            `json:"trades"`
	Unobservable          uint64            `json:"unobservable,omitempty"`
	Missed                uint64            `json:"missed,omitempty"`
	PriceMicros           uint64            `json:"price_micros,omitempty"`
	State                 string            `json:"state,omitempty"`
	Strategy              string            `json:"strategy,omitempty"`
	DecisionSource        string            `json:"decision_source,omitempty"`
	ProposalSource        string            `json:"proposal_source,omitempty"`
	RunPlanSHA256         string            `json:"run_plan_sha256,omitempty"`
	PerpsPlanOutcome      *PerpsPlanOutcome `json:"perps_plan_outcome,omitempty"`
	NextAction            string            `json:"next_action,omitempty"`
	DecisionReason        string            `json:"decision_reason,omitempty"`
	DecisionSignalKind    string            `json:"decision_signal_kind,omitempty"`
	DecisionSignalBPS     int64             `json:"decision_signal_bps,omitempty"`
	DecisionThresholdBPS  int64             `json:"decision_threshold_bps,omitempty"`
	MinimumResearchFrames uint64            `json:"minimum_research_frames,omitempty"`
	RiskHalted            bool              `json:"risk_halted,omitempty"`
	// InitialLot is the configured first paper leg. Later legs use the
	// simulated proceeds, so it is deliberately not described as a fixed order
	// size. These fields expose no address, provider, policy path, or key.
	InitialLotUnits         uint64 `json:"initial_lot_units,omitempty"`
	InitialLotDecimals      uint8  `json:"initial_lot_decimals,omitempty"`
	InitialLotAsset         string `json:"initial_lot_asset,omitempty"`
	MinimumOrderValueMicros uint64 `json:"minimum_order_value_micros,omitempty"`
	MaximumOrderValueMicros uint64 `json:"maximum_order_value_micros,omitempty"`
	FeeReserveLamports      uint64 `json:"fee_reserve_lamports,omitempty"`
	FeeLamports             uint64 `json:"fee_lamports,omitempty"`
	// RemainingFeeReserveLamports excludes setup rent that has not yet been
	// locked, so it is the native amount still available for paper attempts.
	FeeBudgetTracked              bool                   `json:"fee_budget_tracked,omitempty"`
	RemainingFeeReserveLamports   uint64                 `json:"remaining_fee_reserve_lamports,omitempty"`
	EstimatedFillsRemaining       uint64                 `json:"estimated_fills_remaining,omitempty"`
	SlippageBPS                   uint16                 `json:"slippage_bps,omitempty"`
	SettleSeconds                 uint64                 `json:"settle_seconds,omitempty"`
	FastWindow                    uint16                 `json:"fast_window,omitempty"`
	SlowWindow                    uint16                 `json:"slow_window,omitempty"`
	MinimumSignalBPS              uint16                 `json:"minimum_signal_bps,omitempty"`
	MaxVolatilityBPS              uint16                 `json:"max_volatility_bps,omitempty"`
	MaxQuoteImpactBPS             uint16                 `json:"max_quote_impact_bps,omitempty"`
	MaxDrawdownBPS                uint16                 `json:"max_drawdown_bps,omitempty"`
	CooldownSeconds               uint64                 `json:"cooldown_seconds,omitempty"`
	QualificationTracked          bool                   `json:"qualification_tracked,omitempty"`
	QualificationOutcome          string                 `json:"qualification_outcome,omitempty"`
	QualificationSHA256           string                 `json:"qualification_sha256,omitempty"`
	QualificationTapes            uint64                 `json:"qualification_tapes,omitempty"`
	QualificationFrames           uint64                 `json:"qualification_frames,omitempty"`
	QualificationMinimumFrames    uint64                 `json:"qualification_minimum_frames,omitempty"`
	QualificationTrainingFrames   uint64                 `json:"qualification_training_frames,omitempty"`
	QualificationHoldoutFrames    uint64                 `json:"qualification_holdout_frames,omitempty"`
	QualificationStrategy         string                 `json:"qualification_strategy,omitempty"`
	QualificationRiskProfile      string                 `json:"qualification_risk_profile,omitempty"`
	QualificationHoldoutEvaluated bool                   `json:"qualification_holdout_evaluated,omitempty"`
	QualificationStressEvaluated  bool                   `json:"qualification_stress_evaluated,omitempty"`
	QualificationHoldoutScored    bool                   `json:"qualification_holdout_scored,omitempty"`
	QualificationStressScored     bool                   `json:"qualification_stress_scored,omitempty"`
	QualificationHoldoutMicros    int64                  `json:"qualification_holdout_micros,omitempty"`
	QualificationStressMicros     int64                  `json:"qualification_stress_micros,omitempty"`
	QualificationAttempts         []QualificationAttempt `json:"qualification_attempts,omitempty"`
}

// PerpsPlanOutcome binds a selected plan's total paper-run result to the later,
// immutable tape that produced it without exposing the exact paper balance.
type PerpsPlanOutcome struct {
	TapeSHA256 string `json:"tape_sha256"`
	Result     string `json:"result"`
}

// QualificationAttempt is a bounded, read-only projection of one completed
// training result. It is evidence for an operator, never a selected strategy
// or execution authority.
type QualificationAttempt struct {
	RiskProfile       string `json:"risk_profile"`
	Strategy          string `json:"strategy"`
	NetPnLMicros      int64  `json:"net_pnl_micros"`
	FeesMicros        uint64 `json:"fees_micros"`
	FundingMicros     int64  `json:"funding_micros"`
	MaxDrawdownMicros uint64 `json:"max_drawdown_micros"`
	Liquidations      uint64 `json:"liquidations"`
	FilledOrders      uint64 `json:"filled_orders"`
	ClosedPositions   uint64 `json:"closed_positions"`
}

type PerformancePoint struct {
	At                  time.Time `json:"at"`
	PriceMicros         uint64    `json:"price_micros,omitempty"`
	EquityMicros        uint64    `json:"equity_micros"`
	HoldBenchmarkMicros uint64    `json:"hold_benchmark_micros"`
	DrawdownMicros      uint64    `json:"drawdown_micros,omitempty"`
	MaxDrawdownMicros   uint64    `json:"max_drawdown_micros,omitempty"`
	Unavailable         bool      `json:"unavailable,omitempty"`
}

type Writer struct {
	path string
}

func OpenWriter(path string) (*Writer, error) {
	if !cleanPath(path) {
		return nil, errors.New("paper alert status path must be a clean absolute path")
	}
	return &Writer{path: path}, nil
}

// Append atomically replaces the bounded projection. key must identify the
// underlying journal event, not a delivery attempt.
func (w *Writer) Append(at time.Time, kind, key, message string) error {
	return w.append(at, kind, key, message, false)
}

// Reconcile restores an event from the authoritative journal or report. An
// event older than the retained projection is already represented by the
// dropped-event counter and must not make every later restart fail.
func (w *Writer) Reconcile(at time.Time, kind, key, message string) error {
	return w.append(at, kind, key, message, true)
}

// UpdateCurrent refreshes the operator-facing state without creating an alert
// event. Alerts remain reserved for operator-significant transitions.
func (w *Writer) UpdateCurrent(at time.Time, current string) error {
	return w.UpdateCurrentSummary(at, current, nil)
}

func (w *Writer) UpdateCurrentSummary(
	at time.Time, current string, summary *CurrentSummary,
) error {
	return w.updateCurrentSummary(at, current, summary, false)
}

// ReconcileCurrentSummary restores a terminal current view when no newer
// observation exists. A newer live view wins, just as it does for reconciled
// alert events.
func (w *Writer) ReconcileCurrentSummary(
	at time.Time, current string, summary *CurrentSummary,
) error {
	return w.updateCurrentSummary(at, current, summary, true)
}

func (w *Writer) updateCurrentSummary(
	at time.Time, current string, summary *CurrentSummary, reconcile bool,
) error {
	if w == nil || !cleanPath(w.path) || at.IsZero() || !at.Equal(at.UTC()) ||
		len(current) == 0 || len(current) > maxCurrentBytes || !validMessage(current) ||
		validateCurrentSummary(summary) != nil ||
		summary != nil && !summaryDayMatchesObservation(*summary, at) {
		return errors.New("paper current status is invalid")
	}
	snapshot := Snapshot{Version: Version, ObservedAt: at, Events: []Event{}}
	data, err := securefile.ReadPrivate(w.path, maxSnapshotBytes)
	if err == nil {
		if err := strictjson.Decode(data, &snapshot); err != nil {
			return errors.New("existing paper alert status is invalid")
		}
		normalizeLegacySnapshot(&snapshot)
		if ValidateSnapshot(snapshot) != nil {
			return errors.New("existing paper alert status is invalid")
		}
		if at.Before(snapshot.ObservedAt) {
			if reconcile {
				return nil
			}
			return errors.New("paper current status is not chronological")
		}
		upgradeSnapshot(&snapshot)
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("read paper alert status")
	}
	snapshot.Version = Version
	snapshot.ObservedAt, snapshot.Current, snapshot.Summary = at, current, summary
	if summary != nil {
		snapshot.History = updateHistory(snapshot.History, at, *summary)
	}
	if completed, ok := inferredCompleted(snapshot); ok {
		if err := setLatestCompleted(&snapshot, completed); err != nil {
			return err
		}
	}
	return w.write(snapshot)
}

func (w *Writer) append(at time.Time, kind, key, message string, reconcile bool) error {
	if w == nil || !cleanPath(w.path) || at.IsZero() || !at.Equal(at.UTC()) ||
		!validKind(kind) || key == "" || len(key) > 512 ||
		len(message) == 0 || len(message) > MaxMessageBytes ||
		!validMessage(message) {
		return errors.New("paper alert event is invalid")
	}
	snapshot := Snapshot{Version: Version, ObservedAt: at, Events: []Event{}}
	data, err := securefile.ReadPrivate(w.path, maxSnapshotBytes)
	if err == nil {
		if err := strictjson.Decode(data, &snapshot); err != nil {
			return errors.New("existing paper alert status is invalid")
		}
		normalizeLegacySnapshot(&snapshot)
		if ValidateSnapshot(snapshot) != nil {
			return errors.New("existing paper alert status is invalid")
		}
		upgradeSnapshot(&snapshot)
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("read paper alert status")
	}
	snapshot.Version = Version
	id := eventID(kind, key)
	for _, event := range snapshot.Events {
		if event.ID == id {
			return nil
		}
	}
	if len(snapshot.Events) > 0 && at.Before(snapshot.Events[len(snapshot.Events)-1].At) {
		if reconcile {
			return nil
		}
		return errors.New("paper alert event is not chronological")
	}
	if !at.Before(snapshot.ObservedAt) {
		if at.After(snapshot.ObservedAt) {
			snapshot.ObservedAt = at
		}
		// A current or newer alert is the most current state until the runner
		// publishes the same tick's non-alerting status. This also keeps a crash between
		// those writes, or a same-time period close, from hiding the alert
		// behind stale text.
		snapshot.Current, snapshot.Summary = "", nil
	}
	snapshot.Events = append(snapshot.Events, Event{ID: id, At: at, Kind: kind, Message: message})
	if len(snapshot.Events) > MaxEvents {
		dropped := uint64(len(snapshot.Events) - MaxEvents)
		if snapshot.DroppedEvents > ^uint64(0)-dropped {
			return errors.New("paper alert dropped-event counter overflow")
		}
		snapshot.DroppedEvents += dropped
		snapshot.Events = snapshot.Events[len(snapshot.Events)-MaxEvents:]
	}
	return w.write(snapshot)
}

func (w *Writer) write(snapshot Snapshot) error {
	encoded, err := EncodeSnapshot(snapshot)
	if err != nil {
		return err
	}
	if err := securefile.ReplacePrivate(w.path, encoded, maxSnapshotBytes); err != nil {
		return errors.New("write paper alert status")
	}
	return nil
}

// EncodeSnapshot validates and encodes one bounded private status projection.
func EncodeSnapshot(snapshot Snapshot) ([]byte, error) {
	if ValidateSnapshot(snapshot) != nil {
		return nil, errors.New("encode paper alert status")
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded)+1 > maxSnapshotBytes {
		return nil, errors.New("encode paper alert status")
	}
	return append(encoded, '\n'), nil
}

// TruncationEvent warns a consumer that the bounded projection no longer
// contains the complete delivery history. One warning covers each block of 64
// omitted events, avoiding an extra message for every later paper event.
func TruncationEvent(snapshot Snapshot) (Event, bool) {
	if snapshot.DroppedEvents == 0 || ValidateSnapshot(snapshot) != nil {
		return Event{}, false
	}
	bucket := (snapshot.DroppedEvents - 1) / MaxEvents
	return Event{
		ID: eventID("history_truncated", fmt.Sprintf("truncated/%d", bucket)),
		At: snapshot.ObservedAt, Kind: "history_truncated",
		Message: "PAPER · ⚠️ Alert history trimmed\nFull history is in the journal.",
	}, true
}

func ValidateSnapshot(snapshot Snapshot) error {
	normalizeLegacySnapshot(&snapshot)
	if snapshot.Version != legacyVersion && snapshot.Version != settingsVersion &&
		snapshot.Version != accountingVersion &&
		snapshot.Version != qualificationVersion &&
		snapshot.Version != multiTapeVersion &&
		snapshot.Version != perpsPlanVersion &&
		snapshot.Version != decisionSourceVersion &&
		snapshot.Version != latestCompletedVersion &&
		snapshot.Version != Version ||
		snapshot.ObservedAt.IsZero() ||
		!snapshot.ObservedAt.Equal(snapshot.ObservedAt.UTC()) ||
		len(snapshot.Events) > MaxEvents ||
		(snapshot.DroppedEvents != 0 && len(snapshot.Events) == 0) ||
		(snapshot.Current != "" && (len(snapshot.Current) > maxCurrentBytes ||
			!validMessage(snapshot.Current))) ||
		(snapshot.Current == "" && snapshot.Summary != nil) ||
		(snapshot.Version < perpsPlanVersion && snapshot.Summary != nil &&
			len(snapshot.Summary.QualificationAttempts) != 0) ||
		(snapshot.Version < decisionSourceVersion && snapshot.Summary != nil &&
			(validQualificationStrategy(snapshot.Summary.Strategy) ||
				snapshot.Summary.DecisionSource != "" || snapshot.Summary.ProposalSource != "" ||
				snapshot.Summary.RunPlanSHA256 != "" ||
				snapshot.Summary.PerpsPlanOutcome != nil)) ||
		(snapshot.Version < latestCompletedVersion && snapshot.LatestCompleted != nil) ||
		(snapshot.Version < decisionEvidenceVersion &&
			(hasDecisionEvidence(snapshot.Summary) ||
				snapshot.LatestCompleted != nil && hasDecisionEvidence(&snapshot.LatestCompleted.Summary))) ||
		len(snapshot.History) > MaxHistoryPoints ||
		validateCurrentSummary(snapshot.Summary) != nil ||
		(snapshot.Summary != nil && snapshot.LatestCompleted != nil &&
			snapshot.Summary.Market != snapshot.LatestCompleted.Summary.Market) ||
		validateCompletedSnapshot(snapshot.LatestCompleted, snapshot.ObservedAt) != nil {
		return errors.New("paper alert snapshot is invalid")
	}
	if snapshot.Summary != nil && !summaryDayMatchesObservation(*snapshot.Summary, snapshot.ObservedAt) {
		return errors.New("paper alert snapshot summary is not current")
	}
	var previousPoint time.Time
	for _, point := range snapshot.History {
		if point.At.IsZero() || !point.At.Equal(point.At.UTC()) ||
			point.At.After(snapshot.ObservedAt) || point.HoldBenchmarkMicros == 0 ||
			point.DrawdownMicros > point.MaxDrawdownMicros ||
			!previousPoint.IsZero() && !point.At.After(previousPoint) {
			return errors.New("paper alert snapshot history is invalid")
		}
		if snapshot.Summary != nil && point.At.Format("2006-01-02") != snapshot.Summary.Day {
			return errors.New("paper alert snapshot history is not current")
		}
		previousPoint = point.At
	}
	seen := make(map[string]struct{}, len(snapshot.Events))
	var previous time.Time
	for _, event := range snapshot.Events {
		decoded, err := hex.DecodeString(event.ID)
		if err != nil || len(decoded) != sha256.Size || event.At.IsZero() ||
			!event.At.Equal(event.At.UTC()) || event.At.After(snapshot.ObservedAt) ||
			!validKind(event.Kind) || len(event.Message) == 0 ||
			len(event.Message) > MaxMessageBytes ||
			!validMessage(event.Message) ||
			!previous.IsZero() && event.At.Before(previous) {
			return errors.New("paper alert snapshot event is invalid")
		}
		if _, duplicate := seen[event.ID]; duplicate {
			return errors.New("paper alert snapshot has duplicate events")
		}
		seen[event.ID] = struct{}{}
		previous = event.At
	}
	if snapshot.Version == Version && snapshot.LatestCompleted == nil {
		if _, terminal := inferredCompleted(snapshot); terminal {
			return errors.New("paper alert snapshot is invalid")
		}
	}
	return nil
}

func normalizeLegacySnapshot(snapshot *Snapshot) {
	if snapshot == nil || snapshot.Version >= decisionSourceVersion || snapshot.Summary == nil {
		return
	}
	summary := snapshot.Summary
	if summary.QualificationTracked && summary.QualificationTapes == 0 {
		summary.QualificationTapes = 1
	}
	if snapshot.Version >= multiTapeVersion {
		return
	}
	if summary.QualificationOutcome != "candidate_rejected" && summary.QualificationOutcome != "candidate_ready_for_more_paper_testing" {
		return
	}
	summary.QualificationHoldoutEvaluated = true
	summary.QualificationStressEvaluated = true
	if summary.QualificationOutcome == "candidate_ready_for_more_paper_testing" {
		summary.QualificationHoldoutScored = true
		summary.QualificationStressScored = true
		return
	}
	summary.QualificationHoldoutScored = summary.QualificationHoldoutMicros != 0
	summary.QualificationStressScored = summary.QualificationStressMicros != 0
}

func upgradeSnapshot(snapshot *Snapshot) {
	if snapshot == nil || snapshot.Version >= Version {
		return
	}
	if snapshot.Version == decisionSourceVersion && snapshot.LatestCompleted == nil {
		if completed, ok := inferredCompleted(*snapshot); ok {
			snapshot.LatestCompleted = &completed
		}
	}
	snapshot.Version = Version
}

func inferredCompleted(snapshot Snapshot) (CompletedSnapshot, bool) {
	if snapshot.Summary == nil || !snapshot.Summary.QualificationTracked ||
		snapshot.Summary.Instrument != "perpetual" || len(snapshot.Events) == 0 {
		return CompletedSnapshot{}, false
	}
	event := snapshot.Events[len(snapshot.Events)-1]
	if event.Kind != KindExperimentDone || event.At.After(snapshot.ObservedAt) {
		return CompletedSnapshot{}, false
	}
	return CompletedSnapshot{
		ObservedAt: snapshot.ObservedAt,
		EventID:    event.ID,
		Summary:    cloneCurrentSummary(*snapshot.Summary),
	}, true
}

func cloneCurrentSummary(summary CurrentSummary) CurrentSummary {
	cloned := summary
	cloned.QualificationAttempts = append([]QualificationAttempt(nil), summary.QualificationAttempts...)
	if summary.PerpsPlanOutcome != nil {
		outcome := *summary.PerpsPlanOutcome
		cloned.PerpsPlanOutcome = &outcome
	}
	return cloned
}

func validateCompletedSnapshot(completed *CompletedSnapshot, outerObservedAt time.Time) error {
	if completed == nil {
		return nil
	}
	if completed.ObservedAt.IsZero() || completed.ObservedAt.Location() != time.UTC ||
		completed.ObservedAt.After(outerObservedAt) || completed.EventID == "" ||
		validateCurrentSummary(&completed.Summary) != nil ||
		!completed.Summary.QualificationTracked || completed.Summary.Instrument != "perpetual" ||
		!summaryDayMatchesObservation(completed.Summary, completed.ObservedAt) {
		return errors.New("paper completed snapshot is invalid")
	}
	decoded, err := hex.DecodeString(completed.EventID)
	if err != nil || len(decoded) != sha256.Size || completed.EventID != strings.ToLower(completed.EventID) {
		return errors.New("paper completed snapshot is invalid")
	}
	return nil
}

// LatestCompletedSnapshot returns a detached, validated terminal perps receipt.
// Legacy version-seven terminal snapshots are projected during the transition.
func LatestCompletedSnapshot(snapshot Snapshot) (CompletedSnapshot, bool) {
	if ValidateSnapshot(snapshot) != nil {
		return CompletedSnapshot{}, false
	}
	if snapshot.LatestCompleted != nil {
		completed := *snapshot.LatestCompleted
		completed.Summary = cloneCurrentSummary(completed.Summary)
		return completed, true
	}
	if snapshot.Version != decisionSourceVersion {
		return CompletedSnapshot{}, false
	}
	completed, ok := inferredCompleted(snapshot)
	if !ok || validateCompletedSnapshot(&completed, snapshot.ObservedAt) != nil {
		return CompletedSnapshot{}, false
	}
	return completed, true
}

func setLatestCompleted(snapshot *Snapshot, completed CompletedSnapshot) error {
	if snapshot == nil || validateCompletedSnapshot(&completed, snapshot.ObservedAt) != nil {
		return errors.New("paper completed snapshot is invalid")
	}
	if snapshot.LatestCompleted != nil {
		active := *snapshot.LatestCompleted
		if active.ObservedAt.After(completed.ObservedAt) {
			return nil
		}
		if active.ObservedAt.Equal(completed.ObservedAt) {
			activeDigest, activeErr := CompletedSnapshotSHA256(active)
			completedDigest, completedErr := CompletedSnapshotSHA256(completed)
			if activeErr != nil || completedErr != nil || activeDigest != completedDigest {
				return errors.New("paper completed snapshot collision")
			}
			return nil
		}
	}
	completed.Summary = cloneCurrentSummary(completed.Summary)
	snapshot.LatestCompleted = &completed
	return nil
}

// PreserveLatestCompleted carries the newest validated terminal receipt into a
// current live projection without changing its live summary, events, or clock.
func PreserveLatestCompleted(current *Snapshot, previous Snapshot) error {
	if current == nil || current.Version != Version || ValidateSnapshot(*current) != nil ||
		ValidateSnapshot(previous) != nil {
		return errors.New("paper snapshot carry-forward input is invalid")
	}
	prior, ok := LatestCompletedSnapshot(previous)
	if !ok {
		return nil
	}
	next := *current
	if err := setLatestCompleted(&next, prior); err != nil {
		return err
	}
	if ValidateSnapshot(next) != nil {
		return errors.New("paper completed snapshot does not match the live projection")
	}
	*current = next
	return nil
}

// CompletedSnapshotSHA256 returns the canonical digest of one validated receipt.
func CompletedSnapshotSHA256(completed CompletedSnapshot) (string, error) {
	if validateCompletedSnapshot(&completed, completed.ObservedAt) != nil {
		return "", errors.New("paper completed snapshot is invalid")
	}
	encoded, err := json.Marshal(completed)
	if err != nil {
		return "", errors.New("encode paper completed snapshot")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateCurrentSummary(summary *CurrentSummary) error {
	if summary == nil {
		return nil
	}
	if len(summary.Market) == 0 || len(summary.Market) > 32 ||
		!validOptionalSHA256(summary.InstructionSHA256) ||
		summary.TickSeconds < 5 || summary.TickSeconds > 3600 ||
		summary.OpeningEquityMicros == 0 || summary.HoldBenchmarkMicros == 0 ||
		summary.DrawdownMicros > summary.MaxDrawdownMicros ||
		summary.Signals > summary.Checks || summary.Trades > summary.Signals ||
		summary.Unobservable > summary.Checks || summary.Missed > summary.Signals ||
		summary.FeesMicros < 0 ||
		!validValueUnit(summary.ValueUnit) ||
		!validInstrument(*summary) ||
		!validAccounting(*summary) ||
		!validCurrentState(summary.State) || !validCurrentStrategy(*summary) ||
		!validPerpsPlanOutcome(*summary) ||
		!validNextAction(summary.NextAction) || !validDecisionReason(summary.DecisionReason) ||
		!validDecisionEvidence(*summary) ||
		!validQualification(*summary) ||
		!validPaperSettings(*summary) {
		return errors.New("paper current summary is invalid")
	}
	for _, character := range summary.Market {
		if character != '/' && character != '-' && character != '_' &&
			(character < '0' || character > '9') &&
			(character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') {
			return errors.New("paper current summary is invalid")
		}
	}
	day, err := time.Parse("2006-01-02", summary.Day)
	if err != nil || day.Format("2006-01-02") != summary.Day {
		return errors.New("paper current summary is invalid")
	}
	return nil
}

func validPerpsPlanOutcome(summary CurrentSummary) bool {
	present := validQualificationStrategy(summary.Strategy) ||
		summary.DecisionSource != "" || summary.ProposalSource != "" ||
		summary.RunPlanSHA256 != "" || summary.PerpsPlanOutcome != nil
	if !present {
		return true
	}
	if summary.Instrument != "perpetual" || !validOptionalSHA256(summary.RunPlanSHA256) ||
		summary.RunPlanSHA256 == "" {
		return false
	}
	switch summary.DecisionSource {
	case "legacy_fixed_policy":
		return summary.ProposalSource == "built_in" && summary.Strategy == "fixed" &&
			summary.PerpsPlanOutcome == nil
	case "selected_paper_plan":
		if summary.ProposalSource != "deterministic_search" || !validQualificationStrategy(summary.Strategy) {
			return false
		}
	default:
		return false
	}
	if summary.PerpsPlanOutcome == nil {
		return true
	}
	outcome := summary.PerpsPlanOutcome
	if !validOptionalSHA256(outcome.TapeSHA256) || outcome.TapeSHA256 == "" {
		return false
	}
	result, ok := currentResultMicros(summary)
	if !ok {
		return false
	}
	switch {
	case result > 0:
		return outcome.Result == "gain"
	case result < 0:
		return outcome.Result == "loss"
	default:
		return outcome.Result == "flat"
	}
}

func validQualification(summary CurrentSummary) bool {
	present := summary.QualificationOutcome != "" || summary.QualificationSHA256 != "" ||
		summary.QualificationTapes != 0 ||
		summary.QualificationFrames != 0 || summary.QualificationMinimumFrames != 0 ||
		summary.QualificationTrainingFrames != 0 ||
		summary.QualificationHoldoutFrames != 0 || summary.QualificationStrategy != "" ||
		summary.QualificationRiskProfile != "" || summary.QualificationHoldoutEvaluated ||
		summary.QualificationStressEvaluated || summary.QualificationHoldoutScored ||
		summary.QualificationStressScored || summary.QualificationHoldoutMicros != 0 ||
		summary.QualificationStressMicros != 0 || len(summary.QualificationAttempts) != 0
	if !summary.QualificationTracked {
		return !present
	}
	if summary.Instrument != "perpetual" || summary.QualificationTapes == 0 ||
		summary.QualificationTapes > summary.QualificationFrames || !validOptionalSHA256(summary.QualificationSHA256) ||
		summary.QualificationSHA256 == "" || summary.QualificationFrames == 0 || summary.QualificationMinimumFrames == 0 ||
		summary.QualificationTrainingFrames > summary.QualificationFrames ||
		summary.QualificationHoldoutFrames > summary.QualificationFrames-summary.QualificationTrainingFrames ||
		!validQualificationAttempts(summary.QualificationAttempts) {
		return false
	}
	switch summary.QualificationOutcome {
	case "insufficient_evidence":
		return summary.QualificationFrames < summary.QualificationMinimumFrames &&
			summary.QualificationTrainingFrames == 0 && summary.QualificationHoldoutFrames == 0 &&
			len(summary.QualificationAttempts) == 0 &&
			summary.QualificationStrategy == "" && summary.QualificationRiskProfile == "" &&
			!summary.QualificationHoldoutEvaluated && !summary.QualificationStressEvaluated &&
			!summary.QualificationHoldoutScored && !summary.QualificationStressScored &&
			summary.QualificationHoldoutMicros == 0 && summary.QualificationStressMicros == 0
	case "no_training_candidate":
		return summary.QualificationFrames >= summary.QualificationMinimumFrames &&
			summary.QualificationTrainingFrames != 0 && summary.QualificationHoldoutFrames != 0 &&
			summary.QualificationTrainingFrames == summary.QualificationFrames-summary.QualificationHoldoutFrames &&
			summary.QualificationStrategy == "" && summary.QualificationRiskProfile == "" &&
			!summary.QualificationHoldoutEvaluated && !summary.QualificationStressEvaluated &&
			!summary.QualificationHoldoutScored && !summary.QualificationStressScored &&
			summary.QualificationHoldoutMicros == 0 && summary.QualificationStressMicros == 0
	case "candidate_rejected", "candidate_ready_for_more_paper_testing":
		if summary.QualificationFrames < summary.QualificationMinimumFrames ||
			summary.QualificationTrainingFrames == 0 || summary.QualificationHoldoutFrames == 0 ||
			summary.QualificationTrainingFrames != summary.QualificationFrames-summary.QualificationHoldoutFrames ||
			!validQualificationStrategy(summary.QualificationStrategy) ||
			!validQualificationRisk(summary.QualificationRiskProfile) ||
			!summary.QualificationHoldoutEvaluated || !summary.QualificationStressEvaluated {
			return false
		}
		if (!summary.QualificationHoldoutScored && summary.QualificationHoldoutMicros != 0) ||
			(!summary.QualificationStressScored && summary.QualificationStressMicros != 0) {
			return false
		}
		return summary.QualificationOutcome != "candidate_ready_for_more_paper_testing" ||
			summary.QualificationHoldoutScored && summary.QualificationStressScored &&
				summary.QualificationHoldoutMicros > 0 && summary.QualificationStressMicros > 0
	default:
		return false
	}
}

func validQualificationAttempts(attempts []QualificationAttempt) bool {
	if len(attempts) > 3 {
		return false
	}
	order := map[string]int{"conservative": 0, "balanced": 1, "experimental": 2}
	previous := -1
	for _, attempt := range attempts {
		current, ok := order[attempt.RiskProfile]
		if !ok || current <= previous || !validQualificationStrategy(attempt.Strategy) ||
			attempt.FilledOrders == 0 || attempt.ClosedPositions != attempt.FilledOrders ||
			attempt.Liquidations > attempt.ClosedPositions {
			return false
		}
		previous = current
	}
	return true
}

func validQualificationStrategy(strategy string) bool {
	return strategy == "momentum" || strategy == "mean_reversion" || strategy == "breakout" || strategy == "regime"
}

func validQualificationRisk(risk string) bool {
	return risk == "conservative" || risk == "balanced" || risk == "experimental"
}

func validInstrument(summary CurrentSummary) bool {
	switch summary.Instrument {
	case "", "spot":
		return summary.RiskProfile == "" && summary.PositionDirection == "" &&
			summary.LeverageBPS == 0 && !summary.FundingTracked && summary.FundingMicros == 0 &&
			summary.DeficitMicros == 0
	case "perpetual":
		if summary.RiskProfile != "conservative" && summary.RiskProfile != "balanced" &&
			summary.RiskProfile != "experimental" ||
			summary.PositionDirection != "flat" && summary.PositionDirection != "long" &&
				summary.PositionDirection != "short" ||
			summary.LeverageBPS < 10_000 || summary.LeverageBPS > 500_000 ||
			!summary.FundingTracked {
			return false
		}
		return summary.DeficitMicros == 0 || summary.EquityMicros == 0 &&
			summary.PositionDirection == "flat" && summary.RiskHalted
	default:
		return false
	}
}

func validOptionalSHA256(value string) bool {
	if value == "" {
		return true
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func validAccounting(summary CurrentSummary) bool {
	if !summary.AccountingTracked {
		return summary.RealizedMicros == 0 && summary.UnrealizedMicros == 0 &&
			summary.FeesMicros == 0
	}
	if summary.OpeningEquityMicros > math.MaxInt64 || summary.EquityMicros > math.MaxInt64 ||
		summary.DeficitMicros > math.MaxInt64 ||
		summary.UnrealizedMicros > 0 && summary.RealizedMicros > math.MaxInt64-summary.UnrealizedMicros ||
		summary.UnrealizedMicros < 0 && summary.RealizedMicros < math.MinInt64-summary.UnrealizedMicros {
		return false
	}
	result, ok := currentResultMicros(summary)
	if !ok {
		return false
	}
	return summary.RealizedMicros+summary.UnrealizedMicros == result
}

func currentResultMicros(summary CurrentSummary) (int64, bool) {
	if summary.OpeningEquityMicros > math.MaxInt64 || summary.EquityMicros > math.MaxInt64 ||
		summary.DeficitMicros > math.MaxInt64 {
		return 0, false
	}
	result := int64(summary.EquityMicros) - int64(summary.OpeningEquityMicros)
	if result < math.MinInt64+int64(summary.DeficitMicros) {
		return 0, false
	}
	return result - int64(summary.DeficitMicros), true
}

func validDecisionReason(reason string) bool {
	switch reason {
	case "", "watching", "collecting_history", "drawdown_limit", "risk_halt",
		"drawdown_halt", "volatility_limit", "cooldown", "trend_aligned_buy",
		"sell_leg_waiting", "trend_aligned_sell", "buy_leg_waiting",
		"range_high_sell", "range_low_buy", "signal_below_cost_hurdle",
		"data_unavailable", "fee_budget_used", "route_cost_limit",
		"order_pending", "order_filled", "fill_limit", "trade_unavailable",
		"action_level_not_met", "inside_breakout_range", "minimum_order_size",
		"visible_liquidity_limit", "slippage_limit", "liquidation":
		return true
	default:
		return false
	}
}

func validDecisionEvidence(summary CurrentSummary) bool {
	decisionPresent := hasDecisionEvidence(&summary)
	if summary.MinimumResearchFrames != 0 &&
		(summary.Instrument != "perpetual" || summary.MinimumResearchFrames < 2 ||
			summary.MinimumResearchFrames > 1_500) {
		return false
	}
	if !decisionPresent {
		return true
	}
	if summary.Instrument != "perpetual" || summary.PriceMicros == 0 ||
		summary.MinimumResearchFrames == 0 || !validDecisionSignalKind(summary.DecisionSignalKind) ||
		summary.DecisionThresholdBPS < 0 || summary.DecisionThresholdBPS > 10_000 {
		return false
	}
	switch summary.DecisionSignalKind {
	case "history_warmup":
		return summary.DecisionSignalBPS == 0 && summary.DecisionThresholdBPS == 0 &&
			summary.DecisionReason == "collecting_history"
	case "breakout_range":
		return summary.DecisionSignalBPS == 0 && summary.DecisionThresholdBPS > 0 &&
			summary.DecisionReason == "inside_breakout_range"
	default:
		if summary.DecisionThresholdBPS == 0 {
			return false
		}
		return summary.DecisionReason != "action_level_not_met" ||
			summary.DecisionSignalBPS > -summary.DecisionThresholdBPS &&
				summary.DecisionSignalBPS < summary.DecisionThresholdBPS
	}
}

func hasDecisionEvidence(summary *CurrentSummary) bool {
	if summary == nil {
		return false
	}
	return summary.DecisionSignalKind != "" || summary.DecisionSignalBPS != 0 ||
		summary.DecisionThresholdBPS != 0 || summary.MinimumResearchFrames != 0 ||
		perpsDecisionEvidenceReason(summary.DecisionReason)
}

func perpsDecisionEvidenceReason(reason string) bool {
	switch reason {
	case "action_level_not_met", "inside_breakout_range", "minimum_order_size",
		"visible_liquidity_limit", "slippage_limit", "liquidation":
		return true
	default:
		return false
	}
}

func validDecisionSignalKind(kind string) bool {
	switch kind {
	case "two_candle_move", "history_warmup", "momentum", "mean_reversion",
		"breakout_high", "breakout_low", "breakout_range", "regime_momentum",
		"regime_mean_reversion", "regime_breakout_high", "regime_breakout_low":
		return true
	default:
		return false
	}
}

func validPaperSettings(summary CurrentSummary) bool {
	commonPresent := summary.InitialLotUnits != 0 || summary.InitialLotDecimals != 0 ||
		summary.InitialLotAsset != "" || summary.FeeReserveLamports != 0 ||
		summary.MinimumOrderValueMicros != 0 || summary.MaximumOrderValueMicros != 0 ||
		summary.FeeLamports != 0 || summary.FeeBudgetTracked ||
		summary.RemainingFeeReserveLamports != 0 ||
		summary.EstimatedFillsRemaining != 0 ||
		summary.SlippageBPS != 0 || summary.SettleSeconds != 0
	adaptivePresent := summary.FastWindow != 0 || summary.SlowWindow != 0 ||
		summary.MinimumSignalBPS != 0 || summary.MaxVolatilityBPS != 0 ||
		summary.MaxQuoteImpactBPS != 0 || summary.MaxDrawdownBPS != 0 ||
		summary.CooldownSeconds != 0
	if !commonPresent && !adaptivePresent {
		return true
	}
	if !commonPresent || summary.InitialLotUnits == 0 || summary.InitialLotDecimals > 18 ||
		!validAsset(summary.InitialLotAsset) || summary.SlippageBPS == 0 ||
		summary.SlippageBPS > 500 || summary.SettleSeconds == 0 ||
		summary.SettleSeconds > 600 ||
		(summary.MinimumOrderValueMicros != 0 || summary.MaximumOrderValueMicros != 0) &&
			(summary.MinimumOrderValueMicros < 1_000_000 ||
				summary.MinimumOrderValueMicros > summary.MaximumOrderValueMicros ||
				summary.MaximumOrderValueMicros > 1_000_000_000_000) ||
		adaptivePresent != (summary.Strategy == "adaptive") ||
		summary.FeeBudgetTracked != (summary.FeeReserveLamports != 0) ||
		(!summary.FeeBudgetTracked && (summary.RemainingFeeReserveLamports != 0 ||
			summary.EstimatedFillsRemaining != 0)) ||
		(summary.FeeBudgetTracked && (summary.FeeLamports == 0 ||
			summary.FeeReserveLamports == 0 ||
			summary.RemainingFeeReserveLamports > summary.FeeReserveLamports ||
			summary.EstimatedFillsRemaining != summary.RemainingFeeReserveLamports/summary.FeeLamports)) {
		return false
	}
	if !adaptivePresent {
		return true
	}
	if summary.FastWindow < 2 ||
		summary.SlowWindow <= summary.FastWindow || summary.SlowWindow > 1_440 ||
		summary.MinimumSignalBPS == 0 || summary.MinimumSignalBPS > 2_000 ||
		summary.MaxVolatilityBPS <= summary.MinimumSignalBPS ||
		summary.MaxVolatilityBPS > 5_000 || summary.MaxQuoteImpactBPS == 0 ||
		summary.MaxQuoteImpactBPS > 5_000 || summary.MaxDrawdownBPS == 0 ||
		summary.MaxDrawdownBPS > 5_000 || summary.CooldownSeconds > 86_400 {
		return false
	}
	return true
}

func validAsset(asset string) bool {
	if asset == "" || len(asset) > 16 {
		return false
	}
	for _, character := range asset {
		if character != '-' && character != '_' &&
			(character < '0' || character > '9') &&
			(character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') {
			return false
		}
	}
	return true
}

func updateHistory(history []PerformancePoint, at time.Time, summary CurrentSummary) []PerformancePoint {
	pointAt := at
	if summary.State == "completed" && summary.Day != at.Format("2006-01-02") {
		pointAt = at.Add(-time.Nanosecond)
	}
	point := PerformancePoint{
		At: pointAt, PriceMicros: summary.PriceMicros, EquityMicros: summary.EquityMicros,
		HoldBenchmarkMicros: summary.HoldBenchmarkMicros,
		DrawdownMicros:      summary.DrawdownMicros, MaxDrawdownMicros: summary.MaxDrawdownMicros,
		Unavailable: summary.State == "waiting for data",
	}
	if len(history) > 0 && history[len(history)-1].At.Format("2006-01-02") != summary.Day {
		history = nil
	}
	if len(history) > 0 && history[len(history)-1].At.Truncate(historyInterval) == pointAt.Truncate(historyInterval) {
		point.Unavailable = point.Unavailable || history[len(history)-1].Unavailable
		history[len(history)-1] = point
		return history
	}
	history = append(history, point)
	if len(history) > MaxHistoryPoints {
		history = history[len(history)-MaxHistoryPoints:]
	}
	return history
}

func summaryDayMatchesObservation(summary CurrentSummary, at time.Time) bool {
	if summary.Day == at.Format("2006-01-02") {
		return true
	}
	return summary.State == "completed" && at.Equal(at.Truncate(24*time.Hour)) &&
		summary.Day == at.Add(-time.Nanosecond).Format("2006-01-02")
}

func validValueUnit(unit string) bool {
	return unit == "" || unit == "USD" || unit == "devUSDC"
}

func validCurrentStrategy(summary CurrentSummary) bool {
	return summary.Strategy == "" || summary.Strategy == "fixed" || summary.Strategy == "adaptive" ||
		summary.Instrument == "perpetual" && validQualificationStrategy(summary.Strategy)
}

func validNextAction(action string) bool {
	return action == "" || action == "buy" || action == "sell"
}

func validCurrentState(state string) bool {
	switch state {
	case "", "watching", "warming", "uptrend", "downtrend", "range", "volatile",
		"order pending", "waiting for data", "paused", "completed":
		return true
	default:
		return false
	}
}

func validMessage(message string) bool {
	return strings.HasPrefix(message, messagePrefix) ||
		strings.HasPrefix(message, legacyMessagePrefix) &&
			strings.Contains(message, legacyDisclaimer)
}

func eventID(kind, key string) string {
	digest := sha256.Sum256([]byte("mithril-agent/paper-alert/v1\x00" + kind + "\x00" + key))
	return hex.EncodeToString(digest[:])
}

func validKind(kind string) bool {
	switch kind {
	case KindStrategyActive, KindStrategyChanged, KindOrderOpened, KindOrderFilled,
		KindOrderRefused, KindOrderMissed, KindRiskHalted, KindDataUnavailable,
		KindDataRestored, KindPeriodClosed, KindExperimentDone:
		return true
	default:
		return false
	}
}

func cleanPath(path string) bool {
	return path != "" && path != string(filepath.Separator) && filepath.IsAbs(path) &&
		filepath.Clean(path) == path
}
