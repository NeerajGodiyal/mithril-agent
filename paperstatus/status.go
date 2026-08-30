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
	Version            = 1
	MaxEvents          = 64
	MaxMessageBytes    = 3000
	maxSnapshotBytes   = 256 << 10
	messagePrefix      = "PAPER SIMULATION —"
	requiredDisclaimer = "No transaction was signed or submitted."
)

const (
	KindStrategyActive  = "strategy_active"
	KindStrategyChanged = "strategy_changed"
	KindOrderOpened     = "order_opened"
	KindOrderFilled     = "order_filled"
	KindOrderRefused    = "order_refused"
	KindOrderMissed     = "order_missed"
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

func (w *Writer) append(at time.Time, kind, key, message string, reconcile bool) error {
	if w == nil || !cleanPath(w.path) || at.IsZero() || !at.Equal(at.UTC()) ||
		!validKind(kind) || key == "" || len(key) > 512 ||
		len(message) == 0 || len(message) > MaxMessageBytes ||
		!strings.HasPrefix(message, messagePrefix) ||
		!strings.Contains(message, requiredDisclaimer) {
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
	snapshot.ObservedAt = at
	snapshot.Events = append(snapshot.Events, Event{ID: id, At: at, Kind: kind, Message: message})
	if len(snapshot.Events) > MaxEvents {
		dropped := uint64(len(snapshot.Events) - MaxEvents)
		if snapshot.DroppedEvents > ^uint64(0)-dropped {
			return errors.New("paper alert dropped-event counter overflow")
		}
		snapshot.DroppedEvents += dropped
		snapshot.Events = snapshot.Events[len(snapshot.Events)-MaxEvents:]
	}
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
		Message: "PAPER SIMULATION — ⚠️ ALERT HISTORY TRUNCATED\nReview the hash-chained journal.\nNo transaction was signed or submitted.",
	}, true
}

func ValidateSnapshot(snapshot Snapshot) error {
	if snapshot.Version != Version || snapshot.ObservedAt.IsZero() ||
		!snapshot.ObservedAt.Equal(snapshot.ObservedAt.UTC()) ||
		len(snapshot.Events) > MaxEvents ||
		(snapshot.DroppedEvents != 0 && len(snapshot.Events) == 0) {
		return errors.New("paper alert snapshot is invalid")
	}
	seen := make(map[string]struct{}, len(snapshot.Events))
	var previous time.Time
	for _, event := range snapshot.Events {
		decoded, err := hex.DecodeString(event.ID)
		if err != nil || len(decoded) != sha256.Size || event.At.IsZero() ||
			!event.At.Equal(event.At.UTC()) || event.At.After(snapshot.ObservedAt) ||
			!validKind(event.Kind) || len(event.Message) == 0 ||
			len(event.Message) > MaxMessageBytes ||
			!strings.HasPrefix(event.Message, messagePrefix) ||
			!strings.Contains(event.Message, requiredDisclaimer) ||
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

func eventID(kind, key string) string {
	digest := sha256.Sum256([]byte("mithril-agent/paper-alert/v1\x00" + kind + "\x00" + key))
	return hex.EncodeToString(digest[:])
}

func validKind(kind string) bool {
	switch kind {
	case KindStrategyActive, KindStrategyChanged, KindOrderOpened, KindOrderFilled,
		KindOrderRefused, KindOrderMissed, KindPeriodClosed:
		return true
	default:
		return false
	}
}

func cleanPath(path string) bool {
	return path != "" && path != string(filepath.Separator) && filepath.IsAbs(path) &&
		filepath.Clean(path) == path
}
