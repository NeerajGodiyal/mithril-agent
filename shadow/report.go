package shadow

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

// A shadow report has one job: let a reader decide whether the result means
// anything. That is why the coverage figures — how much of the period was
// observable, how many signals could not be acted on — sit alongside the
// profit, rather than in a footnote. A profitable-looking day in which a third
// of the market could not be read is not a profitable day, and the report has
// to say so itself rather than relying on someone to ask.
//
// Every period is scored strictly out of sample: a decision is settled against
// a price observed after it was made, and no result is ever refitted. A run of
// consecutive daily reports is therefore a walk-forward, not a backtest.

// Report is one period's result. The units are explicit in the field names
// because a number whose scale is ambiguous is a number that gets misread.
type Report struct {
	Version uint32    `json:"version"`
	Cluster string    `json:"cluster"`
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`

	Counts Counts `json:"counts"`
	Stats  Stats  `json:"stats"`

	ClosingPriceMicros    uint64 `json:"closing_price_micros"`
	BaseUnits             uint64 `json:"base_units"`
	QuoteUnits            uint64 `json:"quote_units"`
	QuotePegMinimumMicros uint64 `json:"quote_peg_minimum_micros,omitempty"`
	QuotePegMaximumMicros uint64 `json:"quote_peg_maximum_micros,omitempty"`

	OpeningEquityMicros uint64 `json:"opening_equity_micros"`
	ClosingEquityMicros uint64 `json:"closing_equity_micros"`
	RealizedMicros      int64  `json:"realized_micros"`
	UnrealizedMicros    int64  `json:"unrealized_micros"`
	FeesMicros          int64  `json:"fees_micros"`
	TurnoverMicros      uint64 `json:"turnover_micros"`
	MaxDrawdownMicros   uint64 `json:"max_drawdown_micros"`

	HoldBenchmarkMicros uint64 `json:"hold_benchmark_micros"`
	// VersusHoldMicros is the only number that answers "was this worth doing".
	VersusHoldMicros int64 `json:"versus_hold_micros"`

	// ObservableBPS is the share of expected ticks in which the market could be
	// read. ExpectedTicks includes scheduled observations missed while the
	// runner was down, so downtime cannot look like perfect market coverage.
	ExpectedTicks uint64 `json:"expected_ticks"`
	ObservableBPS int32  `json:"observable_bps"`
	// ActedBPS is the share of signals that reached a settled decision.
	ActedBPS int32 `json:"acted_bps"`
}

// BuildReport assembles the period result. It refuses to produce a report with
// no closing price rather than emitting one full of zeroes that would read as a
// flat, uneventful day.
func BuildReport(
	policy Policy,
	ledger Ledger,
	counts Counts,
	stats Stats,
	closingPriceMicros uint64,
	from, to time.Time,
) (Report, error) {
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		return Report{}, err
	}
	ledgerFingerprint, err := ledger.Policy.Fingerprint()
	if err != nil || ledgerFingerprint != policyFingerprint {
		return Report{}, errors.New("report policy does not match the shadow ledger")
	}
	if closingPriceMicros == 0 {
		return Report{}, errZeroReference
	}
	if !to.After(from) {
		return Report{}, errors.New("a report period must end after it begins")
	}
	closing, err := ledger.EquityMicros(closingPriceMicros)
	if err != nil {
		return Report{}, err
	}
	unrealized, err := ledger.UnrealizedMicros(closingPriceMicros)
	if err != nil {
		return Report{}, err
	}
	benchmark, err := ledger.HoldBenchmarkMicros(closingPriceMicros)
	if err != nil {
		return Report{}, err
	}
	signedClosing, err := signed(closing)
	if err != nil {
		return Report{}, err
	}
	signedBenchmark, err := signed(benchmark)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		Version: Version, Cluster: policy.Cluster, From: from, To: to,
		Counts: counts, Stats: stats,
		ClosingPriceMicros: closingPriceMicros,
		BaseUnits:          ledger.BaseUnits, QuoteUnits: ledger.QuoteUnits,
		OpeningEquityMicros: ledger.OpeningEquityMicros,
		ClosingEquityMicros: closing,
		RealizedMicros:      ledger.RealizedMicros,
		UnrealizedMicros:    unrealized,
		FeesMicros:          ledger.FeesMicros,
		TurnoverMicros:      ledger.TurnoverMicros,
		MaxDrawdownMicros:   ledger.MaxDrawdownMicros,
		HoldBenchmarkMicros: benchmark,
		VersusHoldMicros:    signedClosing - signedBenchmark,
		ExpectedTicks:       expectedTicks(policy, counts, from, to),
		ActedBPS:            share(stats.Settled, actionable(counts)),
	}
	report.ObservableBPS = share(observed(counts), report.ExpectedTicks)
	if policy.QuotePeg != nil {
		report.QuotePegMinimumMicros = policy.QuotePeg.MinimumMicros
		report.QuotePegMaximumMicros = policy.QuotePeg.MaximumMicros
	}
	return report, nil
}

func expectedTicks(policy Policy, counts Counts, from, to time.Time) uint64 {
	interval := policy.Tick()
	duration := to.Sub(from)
	expected := uint64(duration / interval)
	if duration%interval != 0 {
		expected++
	}
	// A scheduler can observe both period boundaries. Do not hide actual
	// attempts merely because that yields one more tick than the estimate.
	return max(expected, counts.Ticks)
}

// Trustworthy reports whether the period saw enough of the market to be worth
// reading at all. It is deliberately strict: the failure mode this guards
// against is a confident conclusion drawn from a run that was mostly blind.
func (r Report) Trustworthy() bool {
	const minimumObservable = int32(9_500) // 95%
	return r.Counts.Ticks > 0 && r.ExpectedTicks >= r.Counts.Ticks &&
		r.ObservableBPS >= minimumObservable &&
		(r.Cluster != Mainnet || r.validQuotePeg())
}

func (r Report) validQuotePeg() bool {
	return r.QuotePegMinimumMicros == pricetrigger.USDCBandMinimumMicros &&
		r.QuotePegMaximumMicros == pricetrigger.USDCBandMaximumMicros
}

// Render writes the report for a person, in whole sentences, leading with the
// caveat when there is one.
func (r Report) Render(out io.Writer) error {
	if r.ExpectedTicks < r.Counts.Ticks || r.ExpectedTicks == 0 {
		return errors.New("shadow report has an invalid expected observation count")
	}
	drawdown, err := signed(r.MaxDrawdownMicros)
	if err != nil {
		return errors.New("shadow report has an unrepresentable drawdown")
	}
	turnover, err := signed(r.TurnoverMicros)
	if err != nil {
		return errors.New("shadow report has unrepresentable turnover")
	}
	benchmark, err := signed(r.HoldBenchmarkMicros)
	if err != nil {
		return errors.New("shadow report has an unrepresentable hold benchmark")
	}
	closing, err := signed(r.ClosingEquityMicros)
	if err != nil {
		return errors.New("shadow report has unrepresentable closing equity")
	}
	write := func(format string, args ...any) error {
		_, err := fmt.Fprintf(out, format+"\n", args...)
		return err
	}
	if err := write("Shadow report — %s", r.Cluster); err != nil {
		return err
	}
	if err := write("%s to %s (UTC)\n",
		r.From.UTC().Format(time.RFC3339), r.To.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if !r.Trustworthy() {
		if err := write(
			"Read this with care: the market could only be read for %s of the scheduled observations.\n"+
				"A result from a period that was largely unobservable does not\n"+
				"support a conclusion.\n", percent(r.ObservableBPS)); err != nil {
			return err
		}
	}
	if r.Cluster == Mainnet {
		if !r.validQuotePeg() {
			return errors.New("mainnet shadow report has an invalid USDC/USD accounting bound")
		}
		if err := write(
			"USD figures use USDC at one dollar only after each observable tick passes\n"+
				"two independent USDC/USD confidence checks inside $%d.%06d-$%d.%06d.\n",
			r.QuotePegMinimumMicros/1_000_000, r.QuotePegMinimumMicros%1_000_000,
			r.QuotePegMaximumMicros/1_000_000, r.QuotePegMaximumMicros%1_000_000,
		); err != nil {
			return err
		}
	}
	if err := write("What it would have done"); err != nil {
		return err
	}
	if err := write("  %d observations out of %d expected, %d signals, %d trades, %d refused by the slippage floor",
		r.Counts.Ticks, r.ExpectedTicks, r.Counts.Signals, r.Counts.Fills, r.Counts.Refused); err != nil {
		return err
	}
	if err := write("  %d signals could not be acted on, %d ticks could not be read",
		r.Counts.Missed, r.Counts.Unobservable); err != nil {
		return err
	}
	if err := write("\nWhat it would have made"); err != nil {
		return err
	}
	// Realized is already net of fees, so the fee line is labelled as a
	// breakdown rather than another deduction. Listing them as siblings invites
	// a reader to subtract the fees twice.
	if err := write("  Realized, after fees   %s", usd(r.RealizedMicros)); err != nil {
		return err
	}
	if err := write("    of which fees        %s", usd(-r.FeesMicros)); err != nil {
		return err
	}
	if err := write("  Unrealized             %s", usd(r.UnrealizedMicros)); err != nil {
		return err
	}
	if err := write("  Worst fall             %s", usd(-drawdown)); err != nil {
		return err
	}
	if err := write("  Turnover               %s", usd(turnover)); err != nil {
		return err
	}
	if err := write("\nAgainst simply holding"); err != nil {
		return err
	}
	if err := write("  Holding would be worth  %s", usd(benchmark)); err != nil {
		return err
	}
	if err := write("  This strategy is worth  %s", usd(closing)); err != nil {
		return err
	}
	if err := write("  Difference              %s", usd(r.VersusHoldMicros)); err != nil {
		return err
	}
	if err := write("\nWhat trading cost"); err != nil {
		return err
	}
	if err := write("  Pool price against the reference  %d bps", r.Stats.MeanImpactBPS()); err != nil {
		return err
	}
	if err := write("  Movement while settling           %d bps (worst %d)",
		r.Stats.MeanSlippageBPS(), r.Stats.WorstSlippageBPS); err != nil {
		return err
	}
	return write("\nNothing here was traded. No key was loaded and nothing was signed.")
}

// observed is the number of ticks in which the market could be read. Guarded
// like actionable: an unsigned underflow would clamp to 100% coverage, turning
// a counting mistake into a report that claims it saw everything.
func observed(counts Counts) uint64 {
	if counts.Unobservable >= counts.Ticks {
		return 0
	}
	return counts.Ticks - counts.Unobservable
}

// actionable is the number of signals that were free to be acted on, guarding
// the subtraction: an unsigned underflow here would turn a counting mistake
// into a report that claims perfect execution.
func actionable(counts Counts) uint64 {
	if counts.Deferred >= counts.Signals {
		return 0
	}
	return counts.Signals - counts.Deferred
}

// share returns a proportion in basis points, and reports an empty denominator
// as complete rather than as zero — nothing to miss is not the same as missing
// everything.
func share(part, whole uint64) int32 {
	if whole == 0 {
		return bpsScale
	}
	if part > whole {
		part = whole
	}
	return int32(part * bpsScale / whole)
}

func percent(bps int32) string {
	return fmt.Sprintf("%d.%02d%%", bps/100, abs32(bps%100))
}

// usd renders USD micros with an explicit sign, because a profit and a loss
// must never be distinguishable only by context.
func usd(micros int64) string {
	sign := "+"
	if micros < 0 {
		sign = "-"
	}
	whole, fraction := micros/1_000_000, micros%1_000_000
	if whole < 0 {
		whole = -whole
	}
	if fraction < 0 {
		fraction = -fraction
	}
	return fmt.Sprintf("%s$%d.%06d", sign, whole, fraction)
}

func abs32(value int32) int32 {
	if value < 0 {
		return -value
	}
	return value
}
