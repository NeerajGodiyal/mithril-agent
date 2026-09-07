package main

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestResearchAttributionIsReadOnlyAndReconciled(t *testing.T) {
	p := adaptiveShadowSearchPolicy()
	p.TickSeconds, p.Adaptive.MaxObservationGapSeconds = 3600, 3600
	dir := privateTestDirectory(t)
	writeResearchBehaviorDay(t, dir, p, []time.Duration{time.Hour, 2 * time.Hour, 3 * time.Hour, 4 * time.Hour, 5 * time.Hour})
	journal := filepath.Join(dir, "shadow-2026-09-04.jsonl")
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--policy", writeShadowPolicy(t, p), "--journal-dir", dir}
	clock := func() time.Time { return time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC) }
	var out, again bytes.Buffer
	if err := runResearchAttribution(args, &out, clock); err != nil {
		t.Fatal(err)
	}
	var report researchAttribution
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Kind != "origin_signal_realized_accounting" || !report.PaperOnly || report.Authorized || report.Promotable || report.RecordedBasisEligible || report.PolicySHA256 == "" {
		t.Fatal("unsafe attribution envelope")
	}
	if err := runResearchAttribution(args, &again, clock); err != nil || !bytes.Equal(out.Bytes(), again.Bytes()) {
		t.Fatal("attribution retry changed")
	}
	after, err := os.ReadFile(journal)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("attribution mutated journal")
	}
	if bytes.Contains(out.Bytes(), []byte(dir)) || bytes.Contains(out.Bytes(), []byte(p.Observe)) {
		t.Fatal("private data leaked")
	}
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runResearchAttribution(args, &out, clock); err == nil || out.Len() != 0 {
		t.Fatal("missing evidence returned report")
	}
}

func TestResearchAttributionProfitableSettlementCanLoseAccountEquity(t *testing.T) {
	p := validShadowPolicy()
	p.InputAmount = 100_000_000 // Sell only one tenth of the original SOL inventory.
	dir := privateTestDirectory(t)
	log, err := newDailyJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(); err != nil {
			t.Error(err)
		}
	})
	primary := &shadowSearchReader{identity: p.Trigger.PrimarySourceSHA256}
	secondary := &shadowSearchReader{identity: p.Trigger.SecondarySourceSHA256}
	peg1 := &shadowSearchReader{identity: p.QuotePeg.PrimarySourceSHA256, price: 1_000_000}
	peg2 := &shadowSearchReader{identity: p.QuotePeg.SecondarySourceSHA256, price: 1_000_000}
	quoter := shadowSearchQuoter(func(sell bool, amount uint64) shadow.Quote {
		if !sell {
			t.Fatal("unexpected buy")
		}
		out := amount * primary.price / 1_000_000_000
		return shadow.Quote{InputAmount: amount, EstimatedOutput: out, MinimumOutput: out * uint64(10000-p.SlippageBPS) / 10000}
	})
	runner, err := shadow.NewRunner(p, primary, secondary, quoter, log, peg1, peg2)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	for i, price := range []uint64{100_000_000, 210_000_000, 210_000_000, 80_000_000} {
		at := start.Add(time.Duration(i+1) * time.Minute)
		primary.price, secondary.price = price, price
		primary.at, secondary.at, peg1.at, peg2.at = at, at, at, at
		if _, err := runner.Step(t.Context(), at); err != nil {
			t.Fatal(err)
		}
	}
	end := start.Add(24*time.Hour - time.Nanosecond)
	if err := runner.ClosePeriod(end, 80_000_000); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "shadow-2026-09-04.jsonl")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	now := end.Add(time.Second)
	if err := runResearchAttribution([]string{"--policy", writeShadowPolicy(t, p), "--journal-dir", dir}, &out, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	var got researchAttribution
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	day, err := readResearchDay(p, dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != 1 || got.Groups[0].Filled != 1 || got.RealizedMicros <= 0 || got.NetChangeMicros >= 0 || got.UnrealizedMicros >= 0 || got.VersusHoldMicros <= 0 || got.FeesMicros <= 0 {
		t.Fatalf("fixture failed to distinguish realized from total-account performance: %+v", got)
	}
	if got.OpeningEquityMicros != day.report.OpeningEquityMicros || got.ClosingEquityMicros != day.report.ClosingEquityMicros || got.UnrealizedMicros != day.report.UnrealizedMicros || got.VersusHoldMicros != day.report.VersusHoldMicros || got.NetChangeMicros != got.RealizedMicros+got.UnrealizedMicros || got.Groups[0].RealizedMicros != got.RealizedMicros || got.Groups[0].FeesMicros != got.FeesMicros {
		t.Fatal("whole-account identity differs from existing report")
	}
	if !got.FeesAlreadyIncluded || got.CoverageSufficient || got.FirstPriceAt != start.Add(time.Minute) || got.PeriodFrom != start || got.PeriodThrough != end || got.PeriodBasis != shadow.EvaluationResetDaily {
		t.Fatal("coverage, fee or observation-time basis is misleading")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("account attribution mutated journal")
	}
}

func TestResearchAttributionAccountReconciliationFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		result researchAttribution
		report shadow.Report
	}{
		{name: "equity overflow", report: shadow.Report{ClosingEquityMicros: math.MaxUint64}},
		{name: "sum overflow", result: researchAttribution{RealizedMicros: math.MaxInt64}, report: shadow.Report{RealizedMicros: math.MaxInt64, UnrealizedMicros: 1}},
		{name: "unreconciled", report: shadow.Report{OpeningEquityMicros: 10, ClosingEquityMicros: 11}},
		{name: "fees differ", report: shadow.Report{FeesMicros: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := test.result
			if err := test.result.setAccountReport(test.report); err == nil || !reflect.DeepEqual(before, test.result) {
				t.Fatal("invalid account bridge accepted or partly published")
			}
		})
	}
}

func TestResearchAttributionGroupsActualSettlements(t *testing.T) {
	policy, log, runner, observed := researchPerformanceFixture(t, false)
	end := observed.UTC().Truncate(24 * time.Hour).Add(24*time.Hour - time.Nanosecond)
	if err := runner.ClosePeriod(end, 290_000_000); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(log.directory, "shadow-"+dayKey(observed)+".jsonl")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	now := end.Add(time.Second)
	day, err := readResearchDay(policy, log.directory, now)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	args := []string{"--policy", writeShadowPolicy(t, policy), "--journal-dir", log.directory}
	if err := runResearchAttribution(args, &output, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	var got researchAttribution
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) == 0 || got.FeesMicros <= 0 || got.RealizedMicros >= 0 {
		t.Fatalf("fixture did not attribute actual fee-inclusive losing settlements: %+v", got)
	}
	var fees, realized int64
	var settled, filled uint64
	for _, group := range got.Groups {
		fees += group.FeesMicros
		realized += group.RealizedMicros
		settled += group.Settlements
		filled += group.Filled
	}
	if fees != got.FeesMicros || realized != got.RealizedMicros || filled == 0 ||
		got.FeesMicros != day.report.FeesMicros || got.RealizedMicros != day.report.RealizedMicros ||
		got.UnknownOrigins != settled || got.UnmatchedSettlements != 0 {
		t.Fatalf("groups do not reconcile with original report, or fixed-policy origins were invented: %+v", got)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("attribution changed the settled journal")
	}
}
