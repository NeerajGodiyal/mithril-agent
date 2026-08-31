// Package paperstatus stores a bounded, secret-free projection of meaningful
// paper-trading events. The hash-chained shadow journal remains authoritative.
package paperstatus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

const (
	Version             = 1
	MaxEvents           = 64
	MaxHistoryPoints    = 144
	MaxMessageBytes     = 3000
	maxCurrentBytes     = 512
	maxSnapshotBytes    = 256 << 10
	historyInterval     = 10 * time.Minute
	messagePrefix       = "PAPER ·"
	legacyMessagePrefix = "PAPER SIMULATION —"
	legacyDisclaimer    = "No transaction was signed or submitted."
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
}

type CurrentSummary struct {
	Market              string `json:"market"`
	ValueUnit           string `json:"value_unit,omitempty"`
	Day                 string `json:"day"`
	TickSeconds         uint64 `json:"tick_seconds"`
	OpeningEquityMicros uint64 `json:"opening_equity_micros"`
	EquityMicros        uint64 `json:"equity_micros"`
	HoldBenchmarkMicros uint64 `json:"hold_benchmark_micros"`
	DrawdownMicros      uint64 `json:"drawdown_micros,omitempty"`
	MaxDrawdownMicros   uint64 `json:"max_drawdown_micros,omitempty"`
	Checks              uint64 `json:"checks"`
	Signals             uint64 `json:"signals"`
	Trades              uint64 `json:"trades"`
	Unobservable        uint64 `json:"unobservable,omitempty"`
	Missed              uint64 `json:"missed,omitempty"`
	PriceMicros         uint64 `json:"price_micros,omitempty"`
	State               string `json:"state,omitempty"`
	Strategy            string `json:"strategy,omitempty"`
	NextAction          string `json:"next_action,omitempty"`
	RiskHalted          bool   `json:"risk_halted,omitempty"`
}

type PerformancePoint struct {
	At                  time.Time `json:"at"`
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
	if w == nil || !cleanPath(w.path) || at.IsZero() || !at.Equal(at.UTC()) ||
		len(current) == 0 || len(current) > maxCurrentBytes || !validMessage(current) ||
		validateCurrentSummary(summary) != nil ||
		summary != nil && summary.Day != at.Format("2006-01-02") {
		return errors.New("paper current status is invalid")
	}
	snapshot := Snapshot{Version: Version, ObservedAt: at, Events: []Event{}}
	data, err := securefile.ReadPrivate(w.path, maxSnapshotBytes)
	if err == nil {
		if err := strictjson.Decode(data, &snapshot); err != nil || ValidateSnapshot(snapshot) != nil {
			return errors.New("existing paper alert status is invalid")
		}
		if at.Before(snapshot.ObservedAt) {
			return errors.New("paper current status is not chronological")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("read paper alert status")
	}
	snapshot.ObservedAt, snapshot.Current, snapshot.Summary = at, current, summary
	if summary != nil {
		snapshot.History = updateHistory(snapshot.History, at, *summary)
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
		if err := strictjson.Decode(data, &snapshot); err != nil || ValidateSnapshot(snapshot) != nil {
			return errors.New("existing paper alert status is invalid")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("read paper alert status")
	}
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
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) > maxSnapshotBytes {
		return errors.New("encode paper alert status")
	}
	if err := securefile.ReplacePrivate(w.path, append(encoded, '\n'), maxSnapshotBytes); err != nil {
		return errors.New("write paper alert status")
	}
	return nil
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
	if snapshot.Version != Version || snapshot.ObservedAt.IsZero() ||
		!snapshot.ObservedAt.Equal(snapshot.ObservedAt.UTC()) ||
		len(snapshot.Events) > MaxEvents ||
		(snapshot.DroppedEvents != 0 && len(snapshot.Events) == 0) ||
		(snapshot.Current != "" && (len(snapshot.Current) > maxCurrentBytes ||
			!validMessage(snapshot.Current))) ||
		(snapshot.Current == "" && snapshot.Summary != nil) ||
		len(snapshot.History) > MaxHistoryPoints ||
		validateCurrentSummary(snapshot.Summary) != nil {
		return errors.New("paper alert snapshot is invalid")
	}
	if snapshot.Summary != nil && snapshot.Summary.Day != snapshot.ObservedAt.Format("2006-01-02") {
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
	return nil
}

func validateCurrentSummary(summary *CurrentSummary) error {
	if summary == nil {
		return nil
	}
	if len(summary.Market) == 0 || len(summary.Market) > 32 ||
		summary.TickSeconds < 5 || summary.TickSeconds > 3600 ||
		summary.OpeningEquityMicros == 0 || summary.HoldBenchmarkMicros == 0 ||
		summary.DrawdownMicros > summary.MaxDrawdownMicros ||
		summary.Signals > summary.Checks || summary.Trades > summary.Signals ||
		summary.Unobservable > summary.Checks || summary.Missed > summary.Signals ||
		!validValueUnit(summary.ValueUnit) ||
		!validCurrentState(summary.State) || !validCurrentStrategy(summary.Strategy) ||
		!validNextAction(summary.NextAction) {
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

func updateHistory(history []PerformancePoint, at time.Time, summary CurrentSummary) []PerformancePoint {
	point := PerformancePoint{
		At: at, EquityMicros: summary.EquityMicros,
		HoldBenchmarkMicros: summary.HoldBenchmarkMicros,
		DrawdownMicros:      summary.DrawdownMicros, MaxDrawdownMicros: summary.MaxDrawdownMicros,
		Unavailable: summary.State == "waiting for data",
	}
	if len(history) > 0 && history[len(history)-1].At.Format("2006-01-02") != summary.Day {
		history = nil
	}
	if len(history) > 0 && history[len(history)-1].At.Truncate(historyInterval) == at.Truncate(historyInterval) {
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

func validValueUnit(unit string) bool {
	return unit == "" || unit == "USD" || unit == "devUSDC"
}

func validCurrentStrategy(strategy string) bool {
	return strategy == "" || strategy == "fixed" || strategy == "adaptive"
}

func validNextAction(action string) bool {
	return action == "" || action == "buy" || action == "sell"
}

func validCurrentState(state string) bool {
	switch state {
	case "", "watching", "warming", "uptrend", "downtrend", "range", "volatile",
		"order pending", "waiting for data", "paused":
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
		KindDataRestored, KindPeriodClosed:
		return true
	default:
		return false
	}
}

func cleanPath(path string) bool {
	return path != "" && path != string(filepath.Separator) && filepath.IsAbs(path) &&
		filepath.Clean(path) == path
}
