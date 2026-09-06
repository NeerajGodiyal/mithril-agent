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

const researchBehaviorUsage = `Usage: mithril-agent research behavior --policy PATH --journal-dir PATH

Summarizes replay-verified adaptive decision records from the immediately preceding
complete UTC day under the exact current Mainnet SOL/USDC or JUP/USDC paper policy.
Always advisory and nonqualifying, including when coverage reaches 95%. Verified
low coverage remains explanatory; missing or invalid evidence emits no counts.
Decision records are not time buckets, signals are not fills, and missing records
do not imply no signal. No performance claim, recorded-basis artifact, unseen
forward evidence, policy change, or trade authorization is produced.`

type researchBehavior struct {
	Kind                  string                  `json:"kind"`
	PaperOnly             bool                    `json:"paper_only"`
	AdvisoryOnly          bool                    `json:"advisory_only"`
	DiagnosticOnly        bool                    `json:"diagnostic_only"`
	RecordedBasisEligible bool                    `json:"recorded_basis_eligible"`
	Market                string                  `json:"market"`
	PolicySHA256          string                  `json:"policy_sha256"`
	ObservedFrom          time.Time               `json:"observed_from"`
	ObservedThrough       time.Time               `json:"observed_through"`
	Journal               shadowJournalProvenance `json:"journal"`
	ExpectedTimeBuckets   uint64                  `json:"expected_time_buckets"`
	ObservableTimeBuckets int                     `json:"observable_time_buckets"`
	ObservableBPS         int32                   `json:"observable_bps"`
	RequiredObservableBPS int32                   `json:"required_observable_bps"`
	CoverageSufficient    bool                    `json:"coverage_sufficient"`
	DecisionBasis         string                  `json:"decision_basis"`
	ObservedDecisions     uint64                  `json:"observed_decisions"`
	RegimeCounts          map[string]uint64       `json:"regime_counts"`
	StrategyCounts        map[string]uint64       `json:"strategy_counts"`
	ReasonCounts          map[string]uint64       `json:"reason_counts"`
}

func runResearchBehavior(args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("research behavior", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "exact current paper policy")
	journalDir := flags.String("journal-dir", "", "private daily paper journal directory")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, researchBehaviorUsage)
		}
		return err
	}
	if flags.NArg() != 0 || !cleanResearchPath(*policyPath) || !cleanResearchPath(*journalDir) {
		return errors.New("research behavior requires clean absolute --policy and --journal-dir paths")
	}
	policy, err := loadActiveShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	result, err := buildResearchBehavior(policy, *journalDir, now())
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

func buildResearchBehavior(policy shadow.Policy, journalDir string, now time.Time) (researchBehavior, error) {
	day, err := readResearchDay(policy, journalDir, now)
	if err != nil {
		return researchBehavior{}, err
	}
	if !day.report.Trustworthy() && day.report.ObservableBPS >= 9500 {
		return researchBehavior{}, errors.New("research behavior report failed non-coverage validation")
	}
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		return researchBehavior{}, err
	}
	result := researchBehavior{
		Kind: "recorded_paper_strategy_behavior", PaperOnly: true, AdvisoryOnly: true, DiagnosticOnly: true,
		Market: shadowMarketPair(policy), PolicySHA256: fingerprint,
		ObservedFrom: day.report.From, ObservedThrough: day.report.To, Journal: day.provenance,
		ExpectedTimeBuckets: uint64((24*time.Hour + policy.Tick() - 1) / policy.Tick()),
		ObservableBPS:       day.report.ObservableBPS, RequiredObservableBPS: 9500,
		CoverageSufficient: day.report.ObservableBPS >= 9500,
		DecisionBasis:      "replay_verified_adaptive_decision_records",
		RegimeCounts:       make(map[string]uint64), StrategyCounts: make(map[string]uint64), ReasonCounts: make(map[string]uint64),
	}
	if policy.Adaptive == nil {
		result.DecisionBasis = "adaptive_decisions_absent_by_fixed_policy"
	}
	buckets := make(map[time.Duration]struct{})
	for _, tick := range day.ticks {
		if tick.PeriodClose || tick.Event == shadow.EventUnobservable || tick.PriceMicros == 0 {
			continue
		}
		buckets[tick.At.Sub(day.report.From)/policy.Tick()] = struct{}{}
		if tick.Decision != nil {
			result.ObservedDecisions++
			result.RegimeCounts[tick.Decision.Regime]++
			result.StrategyCounts[tick.Decision.Strategy]++
			result.ReasonCounts[tick.Decision.Reason]++
		}
	}
	result.ObservableTimeBuckets = len(buckets)
	return result, nil
}
