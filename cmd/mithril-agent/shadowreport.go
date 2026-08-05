package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

// shadow report recomputes a day's result from the journal rather than reading
// the summary that was written beside it.
//
// A report you cannot recompute is a report you have to take on trust. The
// journal is hash-chained, so replaying it is the strongest claim this system
// can make about a number: not "we recorded this", but "here is the record, and
// the number follows from it".
//
// When a stored report is present it is compared field by field, and any
// disagreement is shown rather than resolved. That is the point — a mismatch is
// the finding.
const shadowReportUsage = `Usage: mithril-agent shadow report --policy PATH --dir PATH [options]

Recomputes a day's shadow result from its journal and compares it against the
stored report. Read-only.

  --policy PATH   the shadow policy the run used
  --dir PATH      the journal directory
  --day DATE      UTC day to report (default: the most recent journal)
  --json          emit the recomputed report as JSON`

func runShadowReport(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow report", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "shadow policy JSON")
	directory := flags.String("dir", "", "journal directory")
	day := flags.String("day", "", "UTC day, YYYY-MM-DD")
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowReportUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("shadow report takes no positional arguments")
	}
	policy, err := loadShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	if *directory == "" || !filepath.IsAbs(*directory) ||
		filepath.Clean(*directory) != *directory {
		return errors.New("shadow report requires an absolute clean --dir")
	}
	chosen, err := chooseShadowDay(*directory, *day)
	if err != nil {
		return err
	}

	ticks, err := readShadowTicks(filepath.Join(*directory, "shadow-"+chosen+".jsonl"))
	if err != nil {
		return err
	}
	replayed, err := shadow.Replay(policy, ticks)
	if err != nil {
		return err
	}
	from, err := time.Parse("2006-01-02", chosen)
	if err != nil {
		return err
	}
	report, err := shadow.BuildReport(policy, replayed.Ledger, replayed.Counts,
		replayed.Stats, replayed.ClosingPrice, from, from.Add(24*time.Hour))
	if err != nil {
		return err
	}
	if *asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	if _, err := fmt.Fprintf(output,
		"Recomputed from the journal for %s\n\n", chosen); err != nil {
		return err
	}
	if err := report.Render(output); err != nil {
		return err
	}
	return compareStoredShadowReport(*directory, chosen, report, output)
}

// compareStoredShadowReport checks the recomputed result against the one that
// shipped. A missing stored report is not a problem; a differing one is.
func compareStoredShadowReport(
	directory, day string,
	recomputed shadow.Report,
	output io.Writer,
) error {
	path := filepath.Join(directory, "report-"+day+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		_, writeErr := fmt.Fprintf(output,
			"\nNo stored report for %s, so there is nothing to compare against.\n", day)
		return writeErr
	}
	var stored shadow.Report
	if err := json.Unmarshal(raw, &stored); err != nil {
		_, writeErr := fmt.Fprintf(output,
			"\nThe stored report for %s could not be read, so it was not compared.\n", day)
		return writeErr
	}
	found := shadow.Compare(stored, recomputed)
	if len(found) == 0 {
		_, writeErr := fmt.Fprintf(output,
			"\nThe stored report matches the journal exactly, field for field.\n")
		return writeErr
	}
	if _, err := fmt.Fprintf(output,
		"\nThe stored report DISAGREES with the journal in %d place(s):\n", len(found)); err != nil {
		return err
	}
	for _, disagreement := range found {
		if _, err := fmt.Fprintf(output, "  %-24s stored %d, journal says %d\n",
			disagreement.Field, disagreement.Stored, disagreement.Replayed); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(output,
		"\nTrust the journal: it is hash-chained and the report file is not.\n")
	return err
}

// chooseShadowDay picks the requested day, or the most recent one present.
func chooseShadowDay(directory, requested string) (string, error) {
	if requested != "" {
		if _, err := time.Parse("2006-01-02", requested); err != nil {
			return "", errors.New("--day must be YYYY-MM-DD")
		}
		return requested, nil
	}
	entries, err := filepath.Glob(filepath.Join(directory, "shadow-*.jsonl"))
	if err != nil || len(entries) == 0 {
		return "", errors.New("no shadow journal was found in that directory")
	}
	latest := ""
	for _, entry := range entries {
		name := filepath.Base(entry)
		day := name[len("shadow-") : len(name)-len(".jsonl")]
		if day > latest {
			latest = day
		}
	}
	return latest, nil
}

// readShadowTicks opens the journal, which verifies its own hash chain, and
// decodes the recorded ticks.
func readShadowTicks(path string) ([]shadow.Tick, error) {
	store, err := journal.Open(path)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return shadowTicksFrom(store.Records())
}

// shadowTicksFrom decodes recorded ticks. The opening record carries a ledger
// rather than a tick and is skipped: the replay re-derives the opening position
// itself.
func shadowTicksFrom(records []journal.Record) ([]shadow.Tick, error) {
	ticks := make([]shadow.Tick, 0, len(records))
	for _, record := range records {
		if record.Type == shadow.EventOpened {
			continue
		}
		var tick shadow.Tick
		if err := json.Unmarshal(record.Payload, &tick); err != nil {
			return nil, errors.New("a journal record could not be read as a tick")
		}
		if tick.Event == "" {
			tick.Event = record.Type
		}
		ticks = append(ticks, tick)
	}
	if len(ticks) == 0 {
		return nil, errors.New("the journal contains no ticks")
	}
	return ticks, nil
}
