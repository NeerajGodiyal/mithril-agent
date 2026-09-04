package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

const shadowPerpsQualificationUsage = `Usage: mithril-agent shadow perps-qualify --tape PATH

Compares all fixed strategy and paper-risk pairs on a verified private v3/v4 tape,
then checks one training leader on a held-out replay that was not used for
selection in that run, followed by a doubled-fee replay.
It only prints research JSON and cannot trade, sign, promote, or change tape.`

func runShadowPerpsQualification(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow perps-qualify", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tapePath := flags.String("tape", "", "private v3/v4 paper tape")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowPerpsQualificationUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *tapePath == "" || !filepath.IsAbs(*tapePath) || filepath.Clean(*tapePath) != *tapePath {
		return errors.New("shadow perps-qualify requires one clean absolute --tape path")
	}
	raw, err := securefile.ReadPrivate(*tapePath, shadowPerpsMaxFileBytes)
	if err != nil {
		return fmt.Errorf("read qualification paper tape: %w", err)
	}
	var header shadowPerpsTape
	if err := strictjson.Decode(raw, &header); err != nil {
		return errors.New("decode qualification paper tape")
	}
	tape, _, err := readShadowPerpsTape(*tapePath, header.Config)
	if err != nil {
		return err
	}
	qualification, err := perpspaper.QualifyTournament(tape.Config.qualificationConfig(), tape.Frames)
	if err != nil {
		return fmt.Errorf("qualify paper tournament: %w", err)
	}
	return json.NewEncoder(output).Encode(qualification)
}
