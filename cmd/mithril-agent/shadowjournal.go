package main

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
)

// The journal has a hard record limit and does not rotate, so a run that never
// stops has to roll files itself. Rolling on the UTC day is not just a size
// decision: it is the same boundary the report uses, so one file is exactly one
// reporting period and a reader never has to reconcile the two.
type dailyJournal struct {
	directory string
	day       string
	store     *journal.Store
}

func newDailyJournal(directory string) (*dailyJournal, error) {
	if directory == "" || !filepath.IsAbs(directory) ||
		filepath.Clean(directory) != directory {
		return nil, errors.New("shadow journal directory must be an absolute clean path")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, errors.New("could not create the shadow journal directory")
	}
	return &dailyJournal{directory: directory}, nil
}

// Record appends one shadow event, opening the day's file on first use.
func (d *dailyJournal) Record(at time.Time, eventType string, payload any) error {
	if err := d.openFor(at); err != nil {
		return err
	}
	_, err := d.store.Append(at, eventType, "", payload)
	return err
}

// Day is the UTC day the journal is currently writing.
func (d *dailyJournal) Day() string { return d.day }

// Records returns everything written for the current day, including records
// written by an earlier process. The day report is derived from these rather
// than from one process's counters: a runner that restarts mid-day would
// otherwise report only its own share of the day and silently understate it.
func (d *dailyJournal) Records() []journal.Record {
	if d.store == nil {
		return nil
	}
	return d.store.Records()
}

// RolledOver reports whether the given time belongs to a later day than the one
// being written, which is the signal to finalise a report.
func (d *dailyJournal) RolledOver(at time.Time) bool {
	return d.day != "" && dayKey(at) != d.day
}

func (d *dailyJournal) openFor(at time.Time) error {
	key := dayKey(at)
	if d.store != nil && key == d.day {
		return nil
	}
	if err := d.Close(); err != nil {
		return err
	}
	path := filepath.Join(d.directory, "shadow-"+key+".jsonl")
	store, err := journal.Open(path)
	if err != nil {
		return err
	}
	if err := validateShadowJournalDay(path, store.Records()); err != nil {
		_ = store.Close()
		return err
	}
	d.store, d.day = store, key
	return nil
}

func (d *dailyJournal) Close() error {
	if d.store == nil {
		return nil
	}
	store := d.store
	d.store = nil
	return store.Close()
}

func dayKey(at time.Time) string { return at.UTC().Format("2006-01-02") }
