package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const researchAttributionUsage = `Usage: mithril-agent research attribution --policy PATH --journal-dir PATH

Read-only previous-day realized accounting grouped by original signal strategy
and regime. Fees are already included through ledger cost basis; their current
valuation is reported separately, not subtracted twice. This is not complete
round-trip profit, causal attribution, validation or trade authority. Account
change includes remaining inventory: positive realized trades can still leave
the whole account down. Opening equity uses the first observed price, not an
invented midnight mark; low coverage cannot establish whole-day performance.`

type researchAttributionGroup struct {
	Strategy       string `json:"strategy"`
	Regime         string `json:"regime"`
	Settlements    uint64 `json:"settlements"`
	Filled         uint64 `json:"filled"`
	Refused        uint64 `json:"refused"`
	RealizedMicros int64  `json:"realized_micros,string"`
	FeesMicros     int64  `json:"fees_micros,string"`
}

type researchAttribution struct {
	Kind                  string                     `json:"kind"`
	PaperOnly             bool                       `json:"paper_only"`
	Authorized            bool                       `json:"authorized"`
	Promotable            bool                       `json:"promotable"`
	RecordedBasisEligible bool                       `json:"recorded_basis_eligible"`
	Market                string                     `json:"market"`
	PolicySHA256          string                     `json:"policy_sha256"`
	Journal               shadowJournalProvenance    `json:"journal"`
	ObservableBPS         int32                      `json:"observable_bps"`
	CoverageSufficient    bool                       `json:"coverage_sufficient"`
	UnknownOrigins        uint64                     `json:"unknown_origins"`
	UnmatchedSettlements  uint64                     `json:"unmatched_settlements"`
	MissedOrigins         uint64                     `json:"missed_origins"`
	PendingOrigins        uint64                     `json:"pending_origins"`
	RealizedMicros        int64                      `json:"realized_micros,string"`
	FeesMicros            int64                      `json:"fees_micros,string"`
	OpeningEquityMicros   uint64                     `json:"opening_equity_micros,string"`
	ClosingEquityMicros   uint64                     `json:"closing_equity_micros,string"`
	NetChangeMicros       int64                      `json:"net_change_micros,string"`
	UnrealizedMicros      int64                      `json:"unrealized_micros,string"`
	VersusHoldMicros      int64                      `json:"versus_hold_micros,string"`
	FeesAlreadyIncluded   bool                       `json:"fees_already_included"`
	PeriodBasis           string                     `json:"period_basis"`
	PeriodFrom            time.Time                  `json:"period_from"`
	PeriodThrough         time.Time                  `json:"period_through"`
	FirstPriceAt          time.Time                  `json:"first_price_at"`
	Groups                []researchAttributionGroup `json:"groups"`
	Limitations           string                     `json:"limitations"`
}

func runResearchAttribution(args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("research attribution", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "exact current paper policy")
	directory := flags.String("journal-dir", "", "private daily paper journals")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, researchAttributionUsage)
		}
		return err
	}
	if flags.NArg() != 0 || now == nil || !cleanResearchPath(*policyPath) || !cleanResearchPath(*directory) {
		return errors.New("research attribution requires clean absolute policy and journal paths")
	}
	policy, err := loadActiveShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	result, err := buildResearchAttribution(policy, *directory, now())
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

func buildResearchAttribution(policy shadow.Policy, directory string, now time.Time) (researchAttribution, error) {
	day, err := readResearchDay(policy, directory, now)
	if err != nil {
		return researchAttribution{}, err
	}
	if !day.report.Trustworthy() && day.report.ObservableBPS >= 9500 {
		return researchAttribution{}, errors.New("research attribution report failed non-coverage validation")
	}
	replayed, attribution, err := shadow.ReplayWithAttribution(policy, day.ticks)
	if err != nil {
		return researchAttribution{}, err
	}
	pin, err := policy.Fingerprint()
	if err != nil {
		return researchAttribution{}, err
	}
	result := researchAttribution{Kind: "origin_signal_realized_accounting", PaperOnly: true,
		Market: shadowMarketPair(policy), PolicySHA256: pin, Journal: day.provenance,
		ObservableBPS: day.report.ObservableBPS, CoverageSufficient: day.report.ObservableBPS >= 9500,
		UnknownOrigins: attribution.UnknownOrigins, UnmatchedSettlements: attribution.UnmatchedSettlements,
		MissedOrigins: attribution.MissedOrigins, PendingOrigins: attribution.PendingOrigins,
		Groups:      []researchAttributionGroup{},
		Limitations: "Per-settlement realized cost-basis accounting, not complete round-trip or causal profit. Account change also includes unrealized inventory changes. Each UTC period resets configured inventory at its first observed price, not an invented midnight mark; periods cannot be compounded. Low coverage cannot establish whole-day performance. Fees are included at cost basis; fees_micros separately values them at settlement, not an additional deduction. Unknown labels remain unknown; invalid unmatched settlements fail replay. Missing observations are not no-trades. No validation or trading authority."}
	for _, tick := range day.ticks {
		if !tick.PeriodClose && tick.PriceMicros != 0 {
			result.FirstPriceAt = tick.At
			break
		}
	}
	groups := make(map[[2]string]researchAttributionGroup)
	for _, row := range attribution.Settlements {
		key := [2]string{row.Strategy, row.Regime}
		group := groups[key]
		group.Strategy, group.Regime = key[0], key[1]
		group.Settlements++
		if row.Filled {
			group.Filled++
		} else {
			group.Refused++
		}
		group.RealizedMicros, err = addShadowSearchSigned(group.RealizedMicros, row.RealizedMicros)
		if err != nil {
			return researchAttribution{}, err
		}
		group.FeesMicros, err = addShadowSearchSigned(group.FeesMicros, row.FeesMicros)
		if err != nil {
			return researchAttribution{}, err
		}
		result.RealizedMicros, err = addShadowSearchSigned(result.RealizedMicros, row.RealizedMicros)
		if err != nil {
			return researchAttribution{}, err
		}
		result.FeesMicros, err = addShadowSearchSigned(result.FeesMicros, row.FeesMicros)
		if err != nil {
			return researchAttribution{}, err
		}
		groups[key] = group
	}
	if result.RealizedMicros != replayed.Ledger.RealizedMicros || result.FeesMicros != replayed.Ledger.FeesMicros ||
		uint64(len(attribution.Settlements)) != replayed.Counts.Fills+replayed.Counts.Refused {
		return researchAttribution{}, errors.New("origin accounting does not reconcile with replay")
	}
	if err := result.setAccountReport(day.report); err != nil {
		return researchAttribution{}, err
	}
	for _, group := range groups {
		result.Groups = append(result.Groups, group)
	}
	sort.Slice(result.Groups, func(i, j int) bool {
		a, b := result.Groups[i], result.Groups[j]
		if a.Strategy != b.Strategy {
			return a.Strategy < b.Strategy
		}
		return a.Regime < b.Regime
	})
	return result, nil
}

// setAccountReport reconciles the existing ledger report with the attributed
// settlement totals. It never derives a new cost basis or fee valuation.
func (result *researchAttribution) setAccountReport(report shadow.Report) error {
	net, err := unsignedDifference(report.ClosingEquityMicros, report.OpeningEquityMicros)
	if err != nil {
		return err
	}
	accounted, err := addShadowSearchSigned(result.RealizedMicros, report.UnrealizedMicros)
	if err != nil {
		return err
	}
	if accounted != net || result.RealizedMicros != report.RealizedMicros || result.FeesMicros != report.FeesMicros {
		return errors.New("attributed realized and unrealized accounting do not reconcile with account change")
	}
	result.OpeningEquityMicros, result.ClosingEquityMicros = report.OpeningEquityMicros, report.ClosingEquityMicros
	result.NetChangeMicros, result.UnrealizedMicros, result.VersusHoldMicros = net, report.UnrealizedMicros, report.VersusHoldMicros
	result.FeesAlreadyIncluded = true
	result.PeriodBasis, result.PeriodFrom, result.PeriodThrough = shadow.EvaluationResetDaily, report.From, report.To
	return nil
}
