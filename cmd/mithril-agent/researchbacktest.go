package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/researchpacket"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const researchBacktestUsage = `Usage:
  mithril-agent research packet-backtest --in PATH --packet-sha256 HASH
      --policy PATH --journal-dir DIR --day YYYY-MM-DD --spread-bps N

Read-only retrospective training comparison of one recorded-basis candidate
against its exact base policy on the already-consumed observation day.
Spread is an explicit modeled cost each way, not recorded executable fees.
No validation day, parameter search, candidate output or pointer writes.`

type researchBacktestLane struct {
	Counts            shadow.RoundTripCounts `json:"counts"`
	FilteredReasons   map[string]uint64      `json:"filtered_reasons"`
	EquityMicros      uint64                 `json:"equity_micros,string"`
	VersusHoldMicros  int64                  `json:"versus_hold_micros,string"`
	MaxDrawdownMicros uint64                 `json:"max_drawdown_micros,string"`
}

type researchBacktestResult struct {
	Version               uint32                           `json:"version"`
	Kind                  string                           `json:"kind"`
	PaperOnly             bool                             `json:"paper_only"`
	Authorized            bool                             `json:"authorized"`
	Promotable            bool                             `json:"promotable"`
	PoolModelled          bool                             `json:"pool_modelled"`
	SpreadBPS             uint64                           `json:"spread_bps"`
	Market                string                           `json:"market"`
	HypothesisCreatedAt   time.Time                        `json:"hypothesis_created_at"`
	PacketSHA256          string                           `json:"packet_sha256"`
	BasePolicySHA256      string                           `json:"base_policy_sha256"`
	CandidatePolicySHA256 string                           `json:"candidate_policy_sha256"`
	ParameterChanges      []researchpacket.ParameterChange `json:"parameter_changes"`
	RecordedBasisSHA256   string                           `json:"recorded_basis_sha256"`
	Journal               researchpacket.RecordedJournal   `json:"journal"`
	Base                  researchBacktestLane             `json:"base"`
	Candidate             researchBacktestLane             `json:"candidate"`
}

func runResearchPacketBacktest(args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("research packet-backtest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	input := flags.String("in", "", "private stored research packet")
	digest := flags.String("packet-sha256", "", "exact stored packet content digest")
	policyPath := flags.String("policy", "", "exact base policy")
	directory := flags.String("journal-dir", "", "private journal directory")
	day := flags.String("day", "", "recorded basis UTC day")
	spread := flags.Uint64("spread-bps", 0, "explicit modeled pool cost each way")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, researchBacktestUsage)
		}
		return err
	}
	if flags.NArg() != 0 || !cleanResearchPath(*input) || !cleanResearchPath(*policyPath) ||
		!cleanResearchPath(*directory) || *digest == "" || *spread == 0 || *spread >= 10000 || now == nil {
		return errors.New("packet-backtest requires clean private paths, exact packet digest, basis day and explicit spread between 1 and 9999 bps")
	}
	raw, err := securefile.ReadPrivate(*input, researchpacket.MaxBytes)
	if err != nil {
		return err
	}
	packet, err := researchpacket.DecodeStored(raw)
	if err != nil {
		return err
	}
	current := now().UTC()
	if current.IsZero() || packet.CreatedAt.After(current) || packet.ContentSHA256 != *digest ||
		packet.Version != researchpacket.RecordedVersion || packet.Disposition != researchpacket.DispositionCandidate ||
		packet.RecordedObservations == nil || packet.RecordedEvidence == nil ||
		packet.RecordedObservations.Journal.Day != *day {
		return errors.New("packet-backtest requires the exact nonfuture recorded candidate and its observation day")
	}
	base, err := loadShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	controller := shadowResearchController{policy: base, basePolicy: base, journalDir: *directory, researchPacket: &packet}
	// Replay the original opportunity, not a renewed current actionable packet.
	candidate, _, _, err := controller.bindResearchPacket(*digest, packet.CreatedAt)
	if err != nil {
		return err
	}
	recorded, err := readResearchDay(base, *directory, packet.CreatedAt)
	if err != nil {
		return err
	}
	basis := packet.RecordedObservations.Journal
	if recorded.provenance.Day != basis.Day || recorded.provenance.Records != basis.Records ||
		recorded.provenance.ChainHeadSHA256 != basis.ChainHeadSHA256 {
		return errors.New("packet-backtest journal changed from the bound recorded basis")
	}
	result := researchBacktestResult{Version: 1, Kind: "retrospective_training", PaperOnly: true,
		PoolModelled: true, SpreadBPS: *spread, Market: packet.Market, HypothesisCreatedAt: packet.CreatedAt,
		ParameterChanges: append([]researchpacket.ParameterChange(nil), packet.CandidateParameterDiff...),
		PacketSHA256:     *digest, RecordedBasisSHA256: packet.RecordedObservations.ContentSHA256, Journal: basis}
	result.BasePolicySHA256, err = base.Fingerprint()
	if err != nil {
		return err
	}
	result.CandidatePolicySHA256, err = candidate.Fingerprint()
	if err != nil {
		return err
	}
	result.Base, err = researchBacktestScore(base, recorded.ticks, *spread)
	if err != nil {
		return err
	}
	result.Candidate, err = researchBacktestScore(candidate, recorded.ticks, *spread)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

func researchBacktestScore(policy shadow.Policy, ticks []shadow.Tick, spread uint64) (researchBacktestLane, error) {
	replayed, err := shadow.ReplayRoundTripTicksWithDiagnostics(policy, ticks, modelledPool(policy, spread, policy.SlippageBPS))
	if err != nil {
		return researchBacktestLane{}, err
	}
	score, err := scoreShadowRoundTripResult(replayed)
	if err != nil {
		return researchBacktestLane{}, err
	}
	equity, err := replayed.Ledger.EquityMicros(replayed.ClosingPrice)
	if err != nil {
		return researchBacktestLane{}, err
	}
	return researchBacktestLane{Counts: replayed.Counts, FilteredReasons: replayed.FilteredReasons,
		EquityMicros: equity, VersusHoldMicros: score.VersusHoldMicros, MaxDrawdownMicros: score.MaxDrawdownMicros}, nil
}
