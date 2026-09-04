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

const shadowPerpsTournamentUsage = `Usage: mithril-agent shadow perps-tournament --tape PATH

Compares deterministic research strategies against one verified private v3/v4
paper tape. It only prints JSON and cannot trade, sign, promote, or change tape.`

func runShadowPerpsTournament(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow perps-tournament", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tapePath := flags.String("tape", "", "private v3/v4 paper tape")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowPerpsTournamentUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *tapePath == "" || !filepath.IsAbs(*tapePath) || filepath.Clean(*tapePath) != *tapePath {
		return errors.New("shadow perps-tournament requires one clean absolute --tape path")
	}
	raw, err := securefile.ReadPrivate(*tapePath, shadowPerpsMaxFileBytes)
	if err != nil {
		return fmt.Errorf("read tournament paper tape: %w", err)
	}
	var header shadowPerpsTape
	if err := strictjson.Decode(raw, &header); err != nil {
		return errors.New("decode tournament paper tape")
	}
	tape, _, err := readShadowPerpsTape(*tapePath, header.Config)
	if err != nil {
		return err
	}
	tournament, err := perpspaper.RunTournament(tape.Config.replayConfig(), tape.Frames)
	if err != nil {
		return fmt.Errorf("run paper tournament: %w", err)
	}
	return json.NewEncoder(output).Encode(tournament)
}
