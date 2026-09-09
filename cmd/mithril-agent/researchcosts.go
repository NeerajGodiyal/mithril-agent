package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const researchCostsUsage = `Usage: mithril-agent research cost-sensitivity --policy PATH --journal-dir PATH

Replays the previous complete UTC day's verified prices under the unchanged
adaptive paper policy at four hypothetical venue spreads: 1, 10, 25 and 100 bps
per side. All lanes retain policy fees, inventory, timing and risk limits.
This diagnoses model sensitivity, not historical execution costs or strategy
qualification. No lane is selected, and no file, policy or order is written.`

type researchCostLane struct {
	SpreadBPS       uint64                 `json:"assumed_spread_bps_per_side"`
	Counts          shadow.RoundTripCounts `json:"counts"`
	FilteredReasons map[string]uint64      `json:"filtered_reasons"`
	OpeningEquity   uint64                 `json:"opening_equity_micros,string"`
	ClosingEquity   uint64                 `json:"closing_equity_micros,string"`
	NetChange       int64                  `json:"net_change_micros,string"`
	VersusHold      int64                  `json:"versus_hold_micros,string"`
}

type researchCosts struct {
	Kind                  string                  `json:"kind"`
	PaperOnly             bool                    `json:"paper_only"`
	AdvisoryOnly          bool                    `json:"advisory_only"`
	Authorized            bool                    `json:"authorized"`
	Promotable            bool                    `json:"promotable"`
	RecordedBasisEligible bool                    `json:"recorded_basis_eligible"`
	PoolModelled          bool                    `json:"pool_modelled"`
	Market                string                  `json:"market"`
	PolicySHA256          string                  `json:"policy_sha256"`
	Journal               shadowJournalProvenance `json:"journal"`
	ObservableBPS         int32                   `json:"observable_bps"`
	CoverageSufficient    bool                    `json:"coverage_sufficient"`
	PeriodBasis           string                  `json:"period_basis"`
	Lanes                 []researchCostLane      `json:"lanes"`
	Limitations           string                  `json:"limitations"`
}

func runResearchCosts(args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("research cost-sensitivity", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "exact current adaptive paper policy")
	directory := flags.String("journal-dir", "", "private daily paper journals")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, researchCostsUsage)
		}
		return err
	}
	if flags.NArg() != 0 || now == nil || !cleanResearchPath(*policyPath) || !cleanResearchPath(*directory) {
		return errors.New("cost sensitivity requires clean absolute policy and journal paths")
	}
	policy, err := loadActiveShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	if policy.Adaptive == nil {
		return errors.New("cost sensitivity requires an adaptive paper policy")
	}
	day, err := readResearchDay(policy, *directory, now())
	if err != nil {
		return err
	}
	if len(observedPrices(day.ticks)) < 2 {
		return errors.New("cost sensitivity requires at least two observable prices")
	}
	pin, err := policy.Fingerprint()
	if err != nil {
		return err
	}
	report := researchCosts{
		Kind: "modelled_paper_cost_sensitivity", PaperOnly: true, AdvisoryOnly: true, PoolModelled: true,
		Market: shadowMarketPair(policy), PolicySHA256: pin, Journal: day.provenance,
		ObservableBPS: day.report.ObservableBPS, CoverageSufficient: day.report.ObservableBPS >= 9500,
		PeriodBasis: shadow.EvaluationResetDaily,
		Limitations: "Same previously observed BASE-policy prices, not fresh holdout evidence or an active-role result. Spreads are hypothetical per-side output haircuts, not measured fees or historical quotes; policy transaction fees remain included. Signal and filter counts describe each model, not actual venue refusals. Net change includes remaining inventory, not only closed trades. Each day resets inventory at its first observed price; sparse coverage is not whole-day performance. No cost lane is selected or authorized; do not infer execution costs from the best result.",
	}
	for _, spread := range []uint64{1, 10, 25, 100} {
		result, err := shadow.ReplayRoundTripTicksWithDiagnostics(policy, day.ticks, modelledPool(policy, spread, policy.SlippageBPS))
		if err != nil {
			return err
		}
		account, err := shadow.BuildReport(policy, result.Ledger, shadow.Counts{}, shadow.Stats{},
			result.ClosingPrice, day.report.From, day.report.To)
		if err != nil {
			return err
		}
		net, err := unsignedDifference(account.ClosingEquityMicros, account.OpeningEquityMicros)
		if err != nil {
			return err
		}
		report.Lanes = append(report.Lanes, researchCostLane{SpreadBPS: spread,
			Counts: result.Counts, FilteredReasons: result.FilteredReasons,
			OpeningEquity: account.OpeningEquityMicros, ClosingEquity: account.ClosingEquityMicros,
			NetChange: net, VersusHold: account.VersusHoldMicros})
	}
	return json.NewEncoder(output).Encode(report)
}
