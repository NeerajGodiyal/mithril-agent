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

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
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
stored report. Read-only. With --json it emits only the recomputed report.

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

	ticks, err := readShadowTicks(
		filepath.Join(*directory, "shadow-"+chosen+".jsonl"), policy,
	)
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
	to, err := shadowReportEnd(from, replayed.PeriodEnd)
	if err != nil {
		return err
	}
	report, err := shadow.BuildReport(policy, replayed.Ledger, replayed.Counts,
		replayed.Stats, replayed.ClosingPrice, from, to)
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
		if !errors.Is(err, os.ErrNotExist) {
			return errors.New("the stored shadow report could not be read")
		}
		_, writeErr := fmt.Fprintf(output,
			"\nNo stored report for %s, so there is nothing to compare against.\n", day)
		return writeErr
	}
	var stored shadow.Report
	if err := strictjson.Decode(raw, &stored); err != nil {
		return errors.New("the stored shadow report is invalid")
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
		if _, err := fmt.Fprintf(output, "  %-24s stored %s, journal says %s\n",
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
func readShadowTicks(path string, policy shadow.Policy) ([]shadow.Tick, error) {
	records, err := journal.ReadRecords(path)
	if err != nil {
		return nil, err
	}
	return shadowTicksFrom(records, policy, false)
}

// shadowTicksFrom verifies the journal's policy-bound header and decodes its
// ticks. allowEmpty is used only while opening today's newly-created journal.
func shadowTicksFrom(
	records []journal.Record, policy shadow.Policy, allowEmpty bool,
) ([]shadow.Tick, error) {
	if len(records) == 0 {
		if allowEmpty {
			return nil, nil
		}
		return nil, errors.New("the journal contains no records")
	}
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		return nil, err
	}
	ticks := make([]shadow.Tick, 0, len(records))
	header := false
	for index, record := range records {
		if record.Type == shadow.EventOpened {
			if header || index != 0 {
				return nil, errors.New("the shadow journal has more than one opening header")
			}
			var opening shadow.Opening
			if err := strictjson.Decode(record.Payload, &opening); err != nil ||
				opening.Version != shadow.JournalVersion {
				return nil, errors.New("the shadow journal uses an unsupported opening format")
			}
			if opening.PolicySHA256 != fingerprint {
				return nil, errors.New("the shadow journal was written with a different policy")
			}
			header = true
			continue
		}
		if !header {
			return nil, errors.New("the shadow journal is missing its policy-bound opening header")
		}
		var tick shadow.Tick
		if err := strictjson.Decode(record.Payload, &tick); err != nil {
			return nil, errors.New("a journal record could not be read as a tick")
		}
		if tick.Event == "" {
			tick.Event = record.Type
		}
		if tick.Event != record.Type {
			return nil, errors.New("a journal record type does not match its tick")
		}
		ticks = append(ticks, tick)
	}
	if !header {
		return nil, errors.New("the shadow journal is missing its policy-bound opening header")
	}
	if len(ticks) == 0 && !allowEmpty {
		return nil, errors.New("the journal contains no ticks")
	}
	return ticks, nil
}
