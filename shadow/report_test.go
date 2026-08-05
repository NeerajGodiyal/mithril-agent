package shadow

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testReport(t *testing.T) Report {
	t.Helper()
	policy := sellPolicy()
	policy.StartingInputUnits = 1_000_000_000
	ledger, err := NewLedger(policy, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err = ledger.Apply(Fill{
		Filled: true, SpentUnits: 100_000_000, ReceivedUnits: 2_200_000, FeeLamports: 5_000,
	}, 22_000_000)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Unix(1_700_000_000, 0).UTC()
	report, err := BuildReport(policy, ledger,
		Counts{Ticks: 100, Signals: 4, Fills: 1, Refused: 1, Missed: 2},
		Stats{Settled: 2, SumImpactBPS: -30, SumSlippageBPS: -20, WorstSlippageBPS: -15},
		22_000_000, from, from.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// The benchmark comparison is the number that decides whether the strategy was
// worth running, so it must be present and correctly signed.
func TestReportComparesAgainstHolding(t *testing.T) {
	report := testReport(t)
	if report.HoldBenchmarkMicros == 0 {
		t.Fatal("the report has no hold benchmark")
	}
	want := int64(report.ClosingEquityMicros) - int64(report.HoldBenchmarkMicros)
	if report.VersusHoldMicros != want {
		t.Fatalf("versus hold = %d, want %d", report.VersusHoldMicros, want)
	}
	// Selling into a rally must lag holding, and the report must show that
	// rather than quietly reporting the realized gain on its own.
	if report.RealizedMicros <= 0 {
		t.Fatal("the test scenario did not realize a gain")
	}
	if report.VersusHoldMicros >= 0 {
		t.Errorf("selling before a rally beat holding: %d", report.VersusHoldMicros)
	}
}

// A period the agent could barely see must not read as a result.
func TestReportRefusesToLookTrustworthyWhenMostlyBlind(t *testing.T) {
	policy := sellPolicy()
	policy.StartingInputUnits = 1_000_000_000
	ledger, err := NewLedger(policy, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Unix(1_700_000_000, 0).UTC()
	blind, err := BuildReport(policy, ledger,
		Counts{Ticks: 100, Unobservable: 40}, Stats{}, 20_000_000, from, from.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if blind.Trustworthy() {
		t.Fatal("a period that was 40% unreadable reported itself as trustworthy")
	}
	var out bytes.Buffer
	if err := blind.Render(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Read this with care") {
		t.Errorf("the caveat is missing from the rendered report:\n%s", out.String())
	}
	if !strings.HasPrefix(strings.Split(out.String(), "Read this with care")[0], "Shadow report") {
		t.Error("the caveat is buried below the numbers instead of above them")
	}
}

// Coverage must be reported honestly in both directions.
func TestObservableShareIsHonest(t *testing.T) {
	if got := share(95, 100); got != 9_500 {
		t.Errorf("95 of 100 = %d bps, want 9500", got)
	}
	if got := share(0, 100); got != 0 {
		t.Errorf("0 of 100 = %d bps, want 0", got)
	}
	// Nothing to miss is complete coverage, not zero coverage.
	if got := share(0, 0); got != bpsScale {
		t.Errorf("0 of 0 = %d bps, want %d", got, bpsScale)
	}
	// A nonsensical over-count must clamp rather than exceed 100%.
	if got := share(200, 100); got != bpsScale {
		t.Errorf("200 of 100 = %d bps, want %d", got, bpsScale)
	}
}

// A profit and a loss must never be distinguishable only by context.
func TestMoneyIsAlwaysRenderedWithASign(t *testing.T) {
	if got := usd(1_500_000); got != "+$1.500000" {
		t.Errorf("profit rendered as %q", got)
	}
	if got := usd(-1_500_000); got != "-$1.500000" {
		t.Errorf("loss rendered as %q", got)
	}
	if got := usd(0); got != "+$0.000000" {
		t.Errorf("zero rendered as %q", got)
	}
}

// The rendered report must state that nothing was traded, because that is the
// single fact a reader most needs to be sure of.
func TestRenderedReportStatesNothingWasTraded(t *testing.T) {
	var out bytes.Buffer
	if err := testReport(t).Render(&out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "Nothing here was traded") ||
		!strings.Contains(text, "nothing was signed") {
		t.Fatalf("the report does not state that nothing happened:\n%s", text)
	}
	for _, required := range []string{"Unrealized", "Worst fall",
		"Turnover", "Holding would be worth", "could not be acted on"} {
		if !strings.Contains(text, required) {
			t.Errorf("the report omits %q", required)
		}
	}
	// Realized is net of fees. Presenting the fee as a sibling line rather than
	// a breakdown would invite a reader to subtract it a second time.
	if !strings.Contains(text, "Realized, after fees") ||
		!strings.Contains(text, "of which fees") {
		t.Errorf("the fee is not presented as a breakdown of realized profit:\n%s", text)
	}
}

// The JSON surface is what a dashboard reads; it must stay stable and complete.
func TestReportJSONCarriesEveryMetric(t *testing.T) {
	encoded, err := json.Marshal(testReport(t))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"realized_micros", "unrealized_micros", "fees_micros", "turnover_micros",
		"max_drawdown_micros", "hold_benchmark_micros", "versus_hold_micros",
		"observable_bps", "acted_bps", "base_units", "quote_units",
		"closing_price_micros", "counts", "stats",
	} {
		if _, ok := decoded[field]; !ok {
			t.Errorf("the report JSON is missing %q", field)
		}
	}
}

// A report with no closing price would render as a flat, uneventful day. It
// must be an error instead.
func TestBuildReportRefusesWithoutAClosingPrice(t *testing.T) {
	policy := sellPolicy()
	policy.StartingInputUnits = 1_000_000_000
	ledger, err := NewLedger(policy, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Unix(1_700_000_000, 0).UTC()
	if _, err := BuildReport(policy, ledger, Counts{}, Stats{}, 0, from, from.Add(time.Hour)); err == nil {
		t.Error("a report was built with no closing price")
	}
	if _, err := BuildReport(policy, ledger, Counts{}, Stats{}, 20_000_000, from, from); err == nil {
		t.Error("a report was built for a period with no duration")
	}
}

// The averages must divide by the number of decisions actually reached, and an
// empty period must not divide by zero.
func TestStatsAveragesAreSafeAndCorrect(t *testing.T) {
	empty := Stats{}
	if empty.MeanImpactBPS() != 0 || empty.MeanSlippageBPS() != 0 {
		t.Fatal("an empty period produced a non-zero average")
	}
	stats := Stats{Settled: 4, SumImpactBPS: -40, SumSlippageBPS: 20}
	if got := stats.MeanImpactBPS(); got != -10 {
		t.Errorf("mean impact = %d, want -10", got)
	}
	if got := stats.MeanSlippageBPS(); got != 5 {
		t.Errorf("mean slippage = %d, want 5", got)
	}
}

// A miscounted period must never clamp to "saw everything". Both coverage
// denominators are guarded, because an underflow here reads as perfect
// coverage — the most misleading possible answer.
func TestCoverageNeverClaimsEverythingOnBadCounts(t *testing.T) {
	if got := observed(Counts{Ticks: 10, Unobservable: 40}); got != 0 {
		t.Errorf("observed = %d with more blind ticks than ticks, want 0", got)
	}
	if got := actionable(Counts{Signals: 2, Deferred: 9}); got != 0 {
		t.Errorf("actionable = %d with more deferred than signals, want 0", got)
	}
	policy := sellPolicy()
	policy.StartingInputUnits = 1_000_000_000
	ledger, err := NewLedger(policy, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Unix(1_700_000_000, 0).UTC()
	report, err := BuildReport(policy, ledger,
		Counts{Ticks: 10, Unobservable: 40, Signals: 2, Deferred: 9},
		Stats{}, 20_000_000, from, from.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if report.ObservableBPS != 0 || report.Trustworthy() {
		t.Fatalf("an incoherent period reported %d bps of coverage and trustworthy=%v",
			report.ObservableBPS, report.Trustworthy())
	}
}
