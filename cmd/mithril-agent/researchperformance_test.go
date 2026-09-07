package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func researchPerformanceFixture(t *testing.T, futureMark bool) (shadow.Policy, *dailyJournal, *shadow.Runner, time.Time) {
	t.Helper()
	policy := validShadowPolicy()
	directory := privateTestDirectory(t)
	roll, err := newDailyJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := roll.Close(); err != nil {
			t.Error(err)
		}
	})
	primary := &shadowSearchReader{identity: policy.Trigger.PrimarySourceSHA256}
	secondary := &shadowSearchReader{identity: policy.Trigger.SecondarySourceSHA256}
	quotePrimary := &shadowSearchReader{identity: policy.QuotePeg.PrimarySourceSHA256, price: 1_000_000}
	quoteSecondary := &shadowSearchReader{identity: policy.QuotePeg.SecondarySourceSHA256, price: 1_000_000}
	quoter := shadowSearchQuoter(func(sell bool, amount uint64) shadow.Quote {
		if !sell {
			t.Fatal("fixture unexpectedly tried to buy")
		}
		output := amount * primary.price / 1_000_000_000
		return shadow.Quote{InputAmount: amount, EstimatedOutput: output, MinimumOutput: output * uint64(10_000-policy.SlippageBPS) / 10_000}
	})
	runner, err := shadow.NewRunner(policy, primary, secondary, quoter, roll, quotePrimary, quoteSecondary)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	var at time.Time
	for i, price := range []uint64{300_000_000, 300_000_000, 290_000_000} {
		at = start.Add(time.Duration(i+1) * time.Minute)
		primary.price, secondary.price = price, price
		primary.at, secondary.at, quotePrimary.at, quoteSecondary.at = at, at, at, at
		if futureMark {
			primary.at = at.Add(500 * time.Millisecond)
		}
		if _, err := runner.Step(t.Context(), at); err != nil {
			t.Fatal(err)
		}
	}
	if err := roll.publishResearchPrefix(); err != nil {
		t.Fatal(err)
	}
	return policy, roll, runner, at.Add(time.Second)
}

func TestResearchPerformanceReplaysLossesWhileWriterIsActive(t *testing.T) {
	policy, roll, _, now := researchPerformanceFixture(t, false)
	path := filepath.Join(roll.directory, "shadow-"+dayKey(now)+".jsonl")
	if _, err := journal.ReadRecords(path); !errors.Is(err, journal.ErrLocked) {
		t.Fatalf("ordinary reader did not encounter the active writer lock: %v", err)
	}
	before := roll.Records()
	got, err := buildResearchPerformance(policy, roll.directory, now, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got.Fills == 0 || got.RealizedMicros >= 0 || got.UnrealizedMicros >= 0 || got.FeesMicros <= 0 {
		t.Fatalf("fixture did not demonstrate filled losses and fees: %+v", got)
	}
	ticks, err := shadowTicksFrom(before, policy, false)
	if err != nil {
		t.Fatal(err)
	}
	report, err := buildShadowReport(policy, dayKey(now), ticks)
	if err != nil || got.RealizedMicros != report.RealizedMicros || got.UnrealizedMicros != report.UnrealizedMicros ||
		got.FeesMicros != report.FeesMicros || got.EquityMicros != report.ClosingEquityMicros || got.VersusHoldMicros != report.VersusHoldMicros {
		t.Fatalf("performance changed existing accounting: %+v, %v", got, err)
	}
	if !got.PaperOnly || !got.DiagnosticOnly || !got.RealizedIncludesFees || got.RecordedBasisEligible || got.ActiveRoleVerified ||
		got.PeriodBasis != "reset_daily_partial" || got.ObservableTimeBuckets != 3 || got.ExpectedTimeBuckets != 4 || got.ObservableBPS != 7500 ||
		got.Journal.ChainHeadSHA256 != before[len(before)-1].Hash || got.Journal.Records != len(before) || !got.ObservedThrough.Equal(now.Add(-time.Second)) {
		t.Fatalf("performance lost scope or prefix provenance: %+v", got)
	}
	var output bytes.Buffer
	if err := runResearchPerformance([]string{"--policy", writeShadowPolicy(t, policy), "--journal-dir", roll.directory}, &output, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	var decoded researchPerformance
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil || !reflect.DeepEqual(decoded, got) || bytes.Contains(output.Bytes(), []byte(roll.directory)) {
		t.Fatalf("CLI projection changed values or exposed private paths: %v", err)
	}
	if !reflect.DeepEqual(before, roll.Records()) {
		t.Fatal("read-only performance projection changed the active journal")
	}
}

func TestResearchPerformanceRejectsUnavailableOrMisboundEvidence(t *testing.T) {
	for _, mode := range []string{"missing prefix", "tampered prefix", "wrong policy", "future", "future mark", "stale prefix", "fresh outage stale mark", "other day"} {
		t.Run(mode, func(t *testing.T) {
			policy, roll, runner, now := researchPerformanceFixture(t, mode == "future mark")
			prefixPath := filepath.Join(roll.directory, "shadow-"+dayKey(now)+".jsonl.prefix.json")
			switch mode {
			case "missing prefix":
				if err := os.Remove(prefixPath); err != nil {
					t.Fatal(err)
				}
			case "tampered prefix":
				if err := os.WriteFile(prefixPath, []byte(`{"format":"unknown"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong policy":
				policy.InputAmount++
			case "future":
				now = now.Add(-2 * time.Minute)
			case "future mark":
				now = now.Add(-time.Second)
			case "stale prefix":
				now = now.Add(10 * time.Minute)
			case "fresh outage stale mark":
				now = now.Add(10 * time.Minute)
				if _, err := runner.Step(t.Context(), now); err != nil {
					t.Fatal(err)
				}
				if err := roll.publishResearchPrefix(); err != nil {
					t.Fatal(err)
				}
			case "other day":
				now = now.Add(24 * time.Hour)
			}
			var output bytes.Buffer
			if err := runResearchPerformance([]string{"--policy", writeShadowPolicy(t, policy), "--journal-dir", roll.directory}, &output, func() time.Time { return now }); err == nil || output.Len() != 0 {
				t.Fatalf("invalid performance emitted values: %v, %s", err, output.String())
			}
		})
	}
}

func TestResearchPerformanceIncludesNativeValuationFreshness(t *testing.T) {
	directory := privateTestDirectory(t)
	policyPath := filepath.Join(directory, "policy.json")
	var output bytes.Buffer
	if err := runShadowPolicy([]string{"--out", policyPath, "--observe", "So11111111111111111111111111111111111111112", "--adaptive", "--market", "JUP/USDC", "--budget-usdc", "250", "--drawdown-stop-bps", "300"}, &output); err != nil {
		t.Fatal(err)
	}
	policy, err := loadActiveShadowPolicy(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	roll, err := newDailyJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer roll.Close()
	at := time.Date(2026, 9, 7, 0, 1, 0, 0, time.UTC)
	primary := &shadowSearchReader{identity: policy.Trigger.PrimarySourceSHA256, price: 1_000_000, at: at}
	secondary := &shadowSearchReader{identity: policy.Trigger.SecondarySourceSHA256, price: 1_000_000, at: at}
	quotePrimary := &shadowSearchReader{identity: policy.QuotePeg.PrimarySourceSHA256, price: 1_000_000, at: at}
	quoteSecondary := &shadowSearchReader{identity: policy.QuotePeg.SecondarySourceSHA256, price: 1_000_000, at: at}
	nativePrimary := &shadowSearchReader{identity: policy.NativeFeePrice.PrimarySourceSHA256, price: 100_000_000, at: at.Add(-30 * time.Second)}
	nativeSecondary := &shadowSearchReader{identity: policy.NativeFeePrice.SecondarySourceSHA256, price: 100_000_000, at: at.Add(-30 * time.Second)}
	runner, err := shadow.NewRunner(policy, primary, secondary, shadowSearchUnavailableQuoter{}, roll, quotePrimary, quoteSecondary, nativePrimary, nativeSecondary)
	if err != nil {
		t.Fatal(err)
	}
	tick, err := runner.Step(t.Context(), at)
	if err != nil || tick.PriceMicros == 0 || tick.NativeFeePriceMicros == 0 {
		t.Fatalf("fixture lacks replayable native valuation: %+v, %v", tick, err)
	}
	if err := roll.publishResearchPrefix(); err != nil {
		t.Fatal(err)
	}
	if _, err := buildResearchPerformance(policy, directory, at, 2*time.Minute); err != nil {
		t.Fatalf("valid native valuation rejected: %v", err)
	}
	if _, err := buildResearchPerformance(policy, directory, at, 10*time.Second); err == nil {
		t.Fatal("fresh JUP price hid a stale SOL fee-reserve valuation")
	}
}
