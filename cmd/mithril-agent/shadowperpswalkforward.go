package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

const shadowPerpsWalkForwardUsage = `Usage: mithril-agent shadow perps-walk-forward --tape PATH --tape PATH [--tape PATH ...]

Reads two or more write-once, content-addressed v3 paper tapes in chronological
order. It selects only on earlier non-overlapping tapes and evaluates that fixed
strategy/risk pair on the final held-out tape with normal and doubled
fees. It prints research JSON and cannot trade, sign, promote, or change tape.`

type repeatedPathFlag []string

func (paths *repeatedPathFlag) String() string { return fmt.Sprint([]string(*paths)) }

func (paths *repeatedPathFlag) Set(value string) error {
	*paths = append(*paths, value)
	return nil
}

func runShadowPerpsWalkForward(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow perps-walk-forward", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var paths repeatedPathFlag
	flags.Var(&paths, "tape", "private content-addressed v3 paper tape; repeat in chronological order")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowPerpsWalkForwardUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || len(paths) < 2 {
		return errors.New("shadow perps-walk-forward requires at least two --tape paths")
	}
	var identity shadowPerpsTapeConfig
	windows := make([]perpspaper.WalkForwardTape, 0, len(paths))
	for index, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("shadow perps-walk-forward requires clean absolute --tape paths")
		}
		tape, digest, err := readShadowPerpsCorpusTape(path)
		if err != nil {
			return fmt.Errorf("tape %d: %w", index+1, err)
		}
		if index == 0 {
			identity = tape.Config
		} else if !compatibleShadowPerpsTapes(identity, tape.Config) {
			return errors.New("walk-forward tapes use incompatible market configurations")
		}
		windows = append(windows, perpspaper.WalkForwardTape{ContentSHA256: digest, Frames: tape.Frames})
	}
	result, err := perpspaper.QualifyWalkForward(identity.qualificationConfig(), windows)
	if err != nil {
		return fmt.Errorf("walk-forward qualify paper tapes: %w", err)
	}
	return json.NewEncoder(output).Encode(result)
}
