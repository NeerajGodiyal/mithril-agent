package telegramoperator

import (
	"encoding/json"
	"errors"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
)

// The in-memory dedup map does not survive a restart, and the seeding
// heuristic that was meant to cover that cannot: seeding deliberately adopts
// only SETTLED actions, because claiming an in-flight one as history would
// silence the very trade the operator is waiting on. So a restart landing while
// an action is in flight seeds nothing, and when that action settles the new
// process has no memory that its predecessor already announced it, so it sends
// the message a second time.
//
// Keyed on the ACTION ID, not the leg. The in-memory map keys on the leg's
// INDEX, which silently shifts the moment a leg is added, removed, or
// reordered — two different actions can inherit each other's dedup slot. An
// action ID is a digest: unique on its own, so it needs no stable leg identity
// and is immune to reordering.
//
// Live-action entries WITHOUT an action ID are deliberately never stored.
// announceKey falls back to decision/reason for those, which is not unique —
// persisting one would permanently silence every later failure sharing that
// reason. Paper events use deterministic IDs in their own bounded store.

const (
	announcedVersion  = 1
	maxAnnouncedBytes = 64 << 10
	// Bounded so the file cannot grow without limit on a long-running host.
	// Oldest entries fall off first; re-announcing an action this old after a
	// restart is not a realistic outcome, since it would have to still be the
	// current action in a status file.
	maxAnnouncedEntries = 256
)

type announcedDocument struct {
	Version   int      `json:"version"`
	ActionIDs []string `json:"action_ids"`
}

// announcedStore remembers which actions have already been announced, across
// restarts. The service is the only writer.
type announcedStore struct {
	path       string
	order      []string
	seen       map[string]struct{}
	maxEntries int
	maxBytes   int64
}

// loadAnnouncedStore reads the record, tolerating absence. A CORRUPT file is
// also tolerated as empty: refusing to start the alert path because a dedup
// record failed to parse would trade duplicate messages for no messages at all,
// which is the worse failure for something whose job is to tell you what
// happened.
func loadAnnouncedStore(path string) *announcedStore {
	return loadBoundedAnnouncedStore(path, maxAnnouncedEntries, maxAnnouncedBytes)
}

func loadBoundedAnnouncedStore(path string, maxEntries int, maxBytes int64) *announcedStore {
	store := &announcedStore{
		path: path, seen: map[string]struct{}{}, maxEntries: maxEntries, maxBytes: maxBytes,
	}
	if maxEntries <= 0 || maxBytes <= 0 {
		return store
	}
	data, err := securefile.ReadPrivate(path, maxBytes)
	if err != nil {
		return store
	}
	var document announcedDocument
	if err := json.Unmarshal(data, &document); err != nil || document.Version != announcedVersion {
		return store
	}
	for _, id := range document.ActionIDs {
		if !validAnnouncedID(id) {
			continue
		}
		if _, exists := store.seen[id]; exists {
			continue
		}
		store.seen[id] = struct{}{}
		store.order = append(store.order, id)
	}
	for len(store.order) > store.maxEntries {
		delete(store.seen, store.order[0])
		store.order = store.order[1:]
	}
	return store
}

// announced reports whether this action has already been sent.
func (s *announcedStore) announced(actionID string) bool {
	if s == nil || !validAnnouncedID(actionID) {
		return false
	}
	_, exists := s.seen[actionID]
	return exists
}

// record marks an action as announced and persists it. A write failure is
// returned but never blocks the announcement itself: the message has already
// gone out, and the worst case is announcing it again after a restart.
func (s *announcedStore) record(actionID string) error {
	if s == nil || !validAnnouncedID(actionID) {
		return nil
	}
	if _, exists := s.seen[actionID]; exists {
		return nil
	}
	s.seen[actionID] = struct{}{}
	s.order = append(s.order, actionID)
	for len(s.order) > s.maxEntries {
		delete(s.seen, s.order[0])
		s.order = s.order[1:]
	}
	if s.path == "" {
		return nil
	}
	encoded, err := json.Marshal(announcedDocument{
		Version: announcedVersion, ActionIDs: s.order,
	})
	if err != nil {
		return errors.New("encode announced action record")
	}
	if err := securefile.ReplacePrivate(
		s.path, append(encoded, '\n'), s.maxBytes,
	); err != nil {
		return errors.New("write announced action record")
	}
	return nil
}

// validAnnouncedID accepts only the action-ID shape, so a decision/reason
// fallback key can never reach the file and permanently mute a failure class.
func validAnnouncedID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
