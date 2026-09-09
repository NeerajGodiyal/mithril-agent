package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const researchPerformanceUsage = `Usage: mithril-agent research performance --policy PATH --journal-dir PATH [--max-age 2m]

Replays today's writer-published durable paper journal prefix, including losses
and costs. Requires --publish-research-prefix on the paper runner. Read-only,
partial-day and diagnostic-only; not completed evaluation or live-wallet results.
The supplied policy/journal identity is verified, not its active deployment role.
An operator must separately bind generation, role and allocation before treating
this as active-strategy feedback. Missing, stale or invalid evidence emits no
performance values. No journal lock is bypassed and no files are repaired.`

type researchPerformance struct {
	Kind                  string                `json:"kind"`
	PaperOnly             bool                  `json:"paper_only"`
	DiagnosticOnly        bool                  `json:"diagnostic_only"`
	RecordedBasisEligible bool                  `json:"recorded_basis_eligible"`
	ActiveRoleVerified    bool                  `json:"active_role_verified"`
	PeriodBasis           string                `json:"period_basis"`
	Market                string                `json:"market"`
	PolicySHA256          string                `json:"policy_sha256"`
	ObservedFrom          time.Time             `json:"observed_from"`
	ObservedThrough       time.Time             `json:"observed_through"`
	MarkPublishedAt       time.Time             `json:"mark_published_at"`
	CheckedAt             time.Time             `json:"checked_at"`
	Journal               journal.DurablePrefix `json:"journal"`
	ExpectedTimeBuckets   uint64                `json:"expected_time_buckets"`
	ObservableTimeBuckets uint64                `json:"observable_time_buckets"`
	ObservableBPS         uint64                `json:"observable_bps"`
	OpeningEquityMicros   uint64                `json:"opening_equity_micros"`
	EquityMicros          uint64                `json:"equity_micros"`
	RealizedMicros        int64                 `json:"realized_micros"`
	UnrealizedMicros      int64                 `json:"unrealized_micros"`
	FeesMicros            int64                 `json:"fees_micros"`
	RealizedIncludesFees  bool                  `json:"realized_includes_fees"`
	VersusHoldMicros      int64                 `json:"versus_hold_micros"`
	MaxDrawdownMicros     uint64                `json:"max_drawdown_micros"`
	Fills                 uint64                `json:"fills"`
	BaseUnits             uint64                `json:"base_units,string"`
	QuoteUnits            uint64                `json:"quote_units,string"`
	BaseDecimals          uint8                 `json:"base_decimals"`
	QuoteDecimals         uint8                 `json:"quote_decimals"`
}

func runResearchPerformance(args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("research performance", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "exact paper policy")
	directory := flags.String("journal-dir", "", "exact private paper journal directory")
	maxAge := flags.Duration("max-age", 2*time.Minute, "maximum age of the prefix and valuation mark")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, researchPerformanceUsage)
		}
		return err
	}
	if flags.NArg() != 0 || now == nil || !cleanResearchPath(*policyPath) || !cleanResearchPath(*directory) || *maxAge <= 0 || *maxAge > 24*time.Hour {
		return errors.New("research performance requires exact private paths, a clock and a positive age no greater than one day")
	}
	policy, err := loadActiveShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	result, err := buildResearchPerformance(policy, *directory, now().UTC(), *maxAge)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

func buildResearchPerformance(policy shadow.Policy, directory string, now time.Time, maxAge time.Duration) (researchPerformance, error) {
	if now.IsZero() || maxAge <= 0 || maxAge > 24*time.Hour || validateActiveShadowPolicy(policy) != nil ||
		policy.Cluster != shadow.Mainnet || (shadowMarketPair(policy) != "SOL/USDC" && shadowMarketPair(policy) != "JUP/USDC") ||
		!cleanResearchPath(directory) || validatePrivateDirectory(directory) != nil {
		return researchPerformance{}, errors.New("paper performance requires a supported policy and private journal")
	}
	day := dayKey(now)
	path := filepath.Join(directory, "shadow-"+day+".jsonl")
	raw, err := securefile.ReadPrivate(path+".prefix.json", 4096)
	if err != nil {
		return researchPerformance{}, errors.New("paper performance durable prefix is unavailable")
	}
	var prefix journal.DurablePrefix
	if err := strictjson.Decode(raw, &prefix); err != nil {
		return researchPerformance{}, errors.New("paper performance durable prefix is invalid")
	}
	records, err := journal.ReadDurablePrefix(path, prefix)
	if err != nil {
		return researchPerformance{}, err
	}
	if err := validateShadowJournalDay(path, records); err != nil {
		return researchPerformance{}, err
	}
	for _, record := range records {
		if record.At.After(now) {
			return researchPerformance{}, errors.New("paper performance contains future records")
		}
	}
	ticks, err := shadowTicksFrom(records, policy, false)
	if err != nil {
		return researchPerformance{}, err
	}
	report, err := buildShadowReport(policy, day, ticks)
	if err != nil {
		return researchPerformance{}, err
	}
	markAt := time.Time{}
	buckets := make(map[time.Duration]struct{})
	for _, tick := range ticks {
		if tick.PeriodClose || tick.Event == shadow.EventUnobservable || tick.PriceMicros == 0 {
			continue
		}
		if tick.PrimaryPrice == nil || tick.SecondaryPrice == nil {
			return researchPerformance{}, errors.New("paper performance lacks paired price evidence")
		}
		samples := []*pricetrigger.Sample{tick.PrimaryPrice, tick.SecondaryPrice}
		if policy.NativeFeePrice != nil {
			samples = append(samples, tick.NativeFeePrimary, tick.NativeFeeSecondary)
		}
		markAt = time.Time{}
		for _, sample := range samples {
			if sample == nil || sample.PublishedAt.IsZero() || sample.PublishedAt.After(now) {
				return researchPerformance{}, errors.New("paper performance valuation source time is unavailable or future")
			}
			if markAt.IsZero() || sample.PublishedAt.Before(markAt) {
				markAt = sample.PublishedAt
			}
		}
		buckets[tick.At.Sub(report.From)/policy.Tick()] = struct{}{}
	}
	if markAt.IsZero() || markAt.After(now) || now.Sub(markAt) > maxAge || report.To.After(now) || now.Sub(report.To) > maxAge {
		return researchPerformance{}, errors.New("paper performance prefix or valuation mark is stale")
	}
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		return researchPerformance{}, err
	}
	expected := uint64(report.To.Sub(report.From)/policy.Tick()) + 1
	baseDecimals, quoteDecimals := policy.InputDecimals, policy.OutputDecimals
	if !policy.IsSell() {
		baseDecimals, quoteDecimals = quoteDecimals, baseDecimals
	}
	return researchPerformance{
		Kind: "journal_bound_paper_performance", PaperOnly: true, DiagnosticOnly: true,
		PeriodBasis: "reset_daily_partial", Market: shadowMarketPair(policy), PolicySHA256: fingerprint,
		ObservedFrom: report.From, ObservedThrough: report.To, MarkPublishedAt: markAt, CheckedAt: now, Journal: prefix,
		ExpectedTimeBuckets: expected, ObservableTimeBuckets: uint64(len(buckets)), ObservableBPS: uint64(len(buckets)) * 10_000 / expected,
		OpeningEquityMicros: report.OpeningEquityMicros, EquityMicros: report.ClosingEquityMicros,
		RealizedMicros: report.RealizedMicros, UnrealizedMicros: report.UnrealizedMicros, FeesMicros: report.FeesMicros,
		RealizedIncludesFees: true, VersusHoldMicros: report.VersusHoldMicros, MaxDrawdownMicros: report.MaxDrawdownMicros, Fills: report.Counts.Fills,
		BaseUnits: report.BaseUnits, QuoteUnits: report.QuoteUnits, BaseDecimals: baseDecimals, QuoteDecimals: quoteDecimals,
	}, nil
}
