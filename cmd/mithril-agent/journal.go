package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Overclock-Validator/mithril-agent/journal"
)

const journalUsage = `Usage:
  mithril-agent journal verify --path ABSOLUTE_PATH`

func runJournal(args []string, output io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, err := fmt.Fprintln(output, journalUsage)
		return err
	}
	if args[0] != "verify" {
		return fmt.Errorf("unknown journal command %q; run mithril-agent journal --help", args[0])
	}
	return runJournalVerify(args[1:], output)
}

func runJournalVerify(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("journal verify", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("path", "", "absolute journal path")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, journalUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *path == "" {
		return errors.New("journal verify requires --path")
	}
	verified, err := verifyJournal(*path)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Status          string `json:"status"`
		Format          string `json:"format"`
		Records         int    `json:"records"`
		Bytes           int64  `json:"bytes"`
		ChainHeadSHA256 string `json:"chain_head_sha256,omitempty"`
		FileSHA256      string `json:"file_sha256"`
		SendStarted     int    `json:"send_started_records"`
		Submitted       int    `json:"submitted_records"`
	}{
		Status:          "valid",
		Format:          journal.Format,
		Records:         verified.Records,
		Bytes:           verified.Bytes,
		ChainHeadSHA256: verified.ChainHeadSHA256,
		FileSHA256:      verified.FileSHA256,
		SendStarted:     verified.SendStartedRecords,
		Submitted:       verified.SubmittedRecords,
	})
}

func verifyJournal(path string) (journal.Verification, error) {
	verified, err := journal.Verify(path)
	if errors.Is(err, journal.ErrLocked) {
		return journal.Verification{}, errors.New("journal is active; stop the runner or verify a sealed copy")
	}
	if err != nil {
		return journal.Verification{}, fmt.Errorf("verify journal: %w", err)
	}
	return verified, nil
}
