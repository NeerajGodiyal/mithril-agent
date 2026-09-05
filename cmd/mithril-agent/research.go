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
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/researchpacket"
)

const researchUsage = `Usage:
  mithril-agent research packet-record --in PATH --latest PATH [--archive-dir DIR]
      [--sol-policy PATH --sol-journal-dir DIR --jup-policy PATH --jup-journal-dir DIR]
  mithril-agent research packet-project --in PATH --latest PATH
  mithril-agent research observations --policy PATH --journal-dir DIR
  mithril-agent research behavior --policy PATH --journal-dir DIR

Validates one strict Hermes packet with web or host-recorded evidence. The optional archive is
immutable; latest is an atomic read-only projection for the dashboard. This
command cannot change a policy, start a runner, sign, submit, or spend.`

func runResearch(args []string, output io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, err := fmt.Fprintln(output, researchUsage)
		return err
	}
	switch args[0] {
	case "behavior":
		return runResearchBehavior(args[1:], output, time.Now)
	case "observations":
		return runResearchObservations(args[1:], output, time.Now)
	case "packet-project":
		return runResearchPacketProject(args[1:], output, time.Now)
	case "packet-record":
		return runResearchPacketRecord(args[1:], output, time.Now)
	default:
		return errors.New("research expects packet-record, packet-project, observations, or behavior")
	}
}

func runResearchPacketRecord(args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("research packet-record", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	input := flags.String("in", "", "private Hermes JSON output")
	latest := flags.String("latest", "", "private latest-packet projection")
	archiveDir := flags.String("archive-dir", "", "private immutable packet directory")
	solPolicy := flags.String("sol-policy", "", "fixed SOL paper policy for recorded evidence")
	solJournals := flags.String("sol-journal-dir", "", "fixed SOL journal directory")
	jupPolicy := flags.String("jup-policy", "", "fixed JUP paper policy for recorded evidence")
	jupJournals := flags.String("jup-journal-dir", "", "fixed JUP journal directory")
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
	current := now().UTC()
	var requested researchpacket.Packet
	if err := strictjson.Decode(raw, &requested); err != nil {
		return errors.New("research packet JSON is invalid")
	}
	var recorded *researchpacket.RecordedObservations
	if requested.Version == researchpacket.RecordedVersion {
		policyPath, journalDir := "", ""
		switch requested.Market {
		case "SOL/USDC":
			policyPath, journalDir = *solPolicy, *solJournals
		case "JUP/USDC":
			policyPath, journalDir = *jupPolicy, *jupJournals
		}
		if !cleanResearchPath(policyPath) || !cleanResearchPath(journalDir) {
			return errors.New("recorded research needs fixed policy and journal paths for its market")
		}
		policy, err := loadActiveShadowPolicy(policyPath)
		if err != nil {
			return err
		}
		observations, err := buildResearchObservations(policy, journalDir, current)
		if err != nil {
			return err
		}
		recorded = &observations
	}
	packet, err := researchpacket.ParseWithRecorded(raw, recorded, current)
	if err != nil {
		return err
	}
	return storeResearchPacket(packet, *latest, *archiveDir, output)
}

// runResearchPacketProject copies a host-validated sealed packet into the
// dashboard user's private projection. It cannot bind raw model evidence or
// reverify journals; the operator must supply the protected host output.
func runResearchPacketProject(args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("research packet-project", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	input := flags.String("in", "", "host-validated private sealed packet")
	latest := flags.String("latest", "", "private dashboard projection")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, researchUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || !cleanResearchPath(*input) || !cleanResearchPath(*latest) {
		return errors.New("research packet-project requires clean absolute input and latest paths")
	}
	raw, err := securefile.ReadPrivate(*input, researchpacket.MaxBytes)
	if err != nil {
		return err
	}
	packet, err := researchpacket.DecodeStored(raw)
	if err != nil {
		return err
	}
	if !packet.StatusAt(now()).Current {
		return errors.New("research projection packet is not current")
	}
	return storeResearchPacket(packet, *latest, "", output)
}

func storeResearchPacket(packet researchpacket.Packet, latest, archiveDir string, output io.Writer) error {
	encoded, err := json.Marshal(packet)
	if err != nil {
		return errors.New("could not encode research packet")
	}
	encoded = append(encoded, '\n')
	if archiveDir != "" {
		archive := filepath.Join(
			archiveDir,
			packet.CreatedAt.Format("20060102T150405Z")+"-"+packet.ContentSHA256[:16]+".json",
		)
		if err := createSamePrivate(archive, encoded); err != nil {
			return errors.New("could not archive research packet")
		}
	}
	if err := securefile.ReplacePrivate(latest, encoded, researchpacket.MaxBytes); err != nil {
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
