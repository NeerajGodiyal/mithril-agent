package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/researchpacket"
)

const researchUsage = `Usage:
  mithril-agent research packet-record --in PATH --latest PATH [--archive-dir DIR]

Validates one strict, source-cited Hermes JSON packet. The optional archive is
immutable; latest is an atomic read-only projection for the dashboard. This
command cannot change a policy, start a runner, sign, submit, or spend.`

func runResearch(args []string, output io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, err := fmt.Fprintln(output, researchUsage)
		return err
	}
	if args[0] != "packet-record" {
		return errors.New("research expects packet-record")
	}
	return runResearchPacketRecord(args[1:], output, time.Now)
}

func runResearchPacketRecord(args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("research packet-record", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	input := flags.String("in", "", "private Hermes JSON output")
	latest := flags.String("latest", "", "private latest-packet projection")
	archiveDir := flags.String("archive-dir", "", "private immutable packet directory")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, researchUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || !cleanResearchPath(*input) || !cleanResearchPath(*latest) ||
		(*archiveDir != "" && !cleanResearchPath(*archiveDir)) {
		return errors.New("research packet-record requires clean absolute --in and --latest paths")
	}
	raw, err := securefile.ReadPrivate(*input, researchpacket.MaxBytes)
	if err != nil {
		return errors.New("could not read Hermes research output")
	}
	packet, err := researchpacket.Parse(raw, now().UTC())
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(packet)
	if err != nil {
		return errors.New("could not encode research packet")
	}
	encoded = append(encoded, '\n')
	if *archiveDir != "" {
		archive := filepath.Join(
			*archiveDir,
			packet.CreatedAt.Format("20060102T150405Z")+"-"+packet.ContentSHA256[:16]+".json",
		)
		if err := createSamePrivate(archive, encoded); err != nil {
			return errors.New("could not archive research packet")
		}
	}
	if err := securefile.ReplacePrivate(*latest, encoded, researchpacket.MaxBytes); err != nil {
		return errors.New("could not update latest research packet")
	}
	return json.NewEncoder(output).Encode(struct {
		HypothesisID  string    `json:"hypothesis_id"`
		Disposition   string    `json:"disposition"`
		ValidUntil    time.Time `json:"valid_until"`
		ContentSHA256 string    `json:"content_sha256"`
	}{packet.HypothesisID, packet.Disposition, packet.ValidUntil, packet.ContentSHA256})
}

func createSamePrivate(path string, data []byte) error {
	if err := securefile.CreatePrivate(path, data, researchpacket.MaxBytes); err == nil {
		return nil
	}
	stored, readErr := securefile.ReadPrivate(path, researchpacket.MaxBytes)
	if readErr == nil && bytes.Equal(stored, data) {
		return nil
	}
	return errors.New("immutable research packet already differs")
}

func cleanResearchPath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}
