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
	"github.com/Overclock-Validator/mithril-agent/researchpacket"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestResearchBehaviorCountsVerifiedDecisionsWithoutQualifying(t *testing.T) {
	now := time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC)
	for _, market := range []string{"SOL/USDC", "JUP/USDC"} {
		t.Run(market, func(t *testing.T) {
			policy := adaptiveShadowSearchPolicy()
			if market == "JUP/USDC" {
				path := filepath.Join(privateTestDirectory(t), "policy.json")
				var output bytes.Buffer
				if err := runShadowPolicy([]string{"--out", path, "--observe", "So11111111111111111111111111111111111111112", "--adaptive", "--market", market, "--budget-usdc", "250", "--drawdown-stop-bps", "300"}, &output); err != nil {
					t.Fatal(err)
				}
				var err error
				policy, err = loadShadowPolicy(path)
				if err != nil {
					t.Fatal(err)
				}
				policy.Adaptive.FastWindow, policy.Adaptive.SlowWindow = 2, 4
			}
			policy.TickSeconds, policy.Adaptive.MaxObservationGapSeconds = 3600, 3600
			directory := privateTestDirectory(t)
			var offsets []time.Duration
			for hour := 1; hour < 24; hour++ {
				offsets = append(offsets, time.Duration(hour)*time.Hour)
			}
			writeResearchBehaviorDay(t, directory, policy, offsets)
			path := filepath.Join(directory, "shadow-2026-09-04.jsonl")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"--policy", writeShadowPolicy(t, policy), "--journal-dir", directory}
			var observationsBefore bytes.Buffer
			if err := runResearchObservations(args, &observationsBefore, func() time.Time { return now }); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := runResearchBehavior(args, &output, func() time.Time { return now }); err != nil {
				t.Fatal(err)
			}
			var got researchBehavior
			if err := json.Unmarshal(output.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Kind != "recorded_paper_strategy_behavior" || !got.PaperOnly || !got.AdvisoryOnly || !got.DiagnosticOnly || got.RecordedBasisEligible ||
				got.Market != market || got.ExpectedTimeBuckets != 24 || got.ObservableTimeBuckets != 23 || got.ObservableBPS != 9583 || !got.CoverageSufficient ||
				got.ObservedDecisions != 23 || got.DecisionBasis != "replay_verified_adaptive_decision_records" ||
				!reflect.DeepEqual(got.RegimeCounts, map[string]uint64{"warming": 3, "range": 20}) ||
				!reflect.DeepEqual(got.StrategyCounts, map[string]uint64{"observe": 3, "range_reversion": 20}) ||
				!reflect.DeepEqual(got.ReasonCounts, map[string]uint64{"collecting_history": 3, "signal_below_cost_hurdle": 20}) {
				t.Fatalf("unexpected decision diagnostic: %+v", got)
			}
			var artifact researchpacket.RecordedObservations
			if err := json.Unmarshal(observationsBefore.Bytes(), &artifact); err != nil {
				t.Fatal(err)
			}
			if got.PolicySHA256 != artifact.PolicySHA256 || got.Journal.ChainHeadSHA256 != artifact.Journal.ChainHeadSHA256 ||
				got.Journal.Records != artifact.Journal.Records || got.Journal.Day != artifact.Journal.Day ||
				!got.ObservedFrom.Equal(artifact.ObservedFrom) || !got.ObservedThrough.Equal(artifact.ObservedThrough) {
				t.Fatal("diagnostic lost verified observation provenance")
			}
			var nonArtifact researchpacket.RecordedObservations
			if err := json.Unmarshal(output.Bytes(), &nonArtifact); err != nil || nonArtifact.Validate() == nil {
				t.Fatal("behavior diagnostic became a recorded-basis artifact")
			}
			var observationsAfter bytes.Buffer
			if err := runResearchObservations(args, &observationsAfter, func() time.Time { return now.Add(time.Hour) }); err != nil || !bytes.Equal(observationsBefore.Bytes(), observationsAfter.Bytes()) {
				t.Fatalf("behavior changed observation bytes: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("read-only diagnostic changed journal")
			}
		})
	}
}

func TestResearchBehaviorSparseRecordsAreNotTimeBuckets(t *testing.T) {
	policy := adaptiveShadowSearchPolicy()
	policy.TickSeconds, policy.Adaptive.MaxObservationGapSeconds = 3600, 3600
	directory := privateTestDirectory(t)
	writeResearchBehaviorDay(t, directory, policy, []time.Duration{time.Hour, time.Hour + time.Second, 2 * time.Hour, 3 * time.Hour, 4 * time.Hour, 5 * time.Hour})
	now := time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC)
	got, err := buildResearchBehavior(policy, directory, now)
	if err != nil || got.ObservedDecisions != 6 || got.ObservableTimeBuckets != 5 || got.ExpectedTimeBuckets != 24 || got.ObservableBPS != 2083 || got.CoverageSufficient || got.RecordedBasisEligible ||
		!reflect.DeepEqual(got.ReasonCounts, map[string]uint64{"collecting_history": 3, "signal_below_cost_hurdle": 3}) {
		t.Fatalf("sparse decision denominator: %+v, %v", got, err)
	}
	var coverage *researchCoverageError
	if _, err := buildResearchObservations(policy, directory, now); !errors.As(err, &coverage) {
		t.Fatalf("behavior relaxed recorded-basis coverage gate: %v", err)
	}
	fixed := validShadowResearchPolicy()
	fixedDir := privateTestDirectory(t)
	writeShadowResearchDay(t, fixedDir, fixed, "2026-09-04", []uint64{100_000_000})
	got, err = buildResearchBehavior(fixed, fixedDir, now)
	if err != nil || got.ObservedDecisions != 0 || len(got.ReasonCounts) != 0 || len(got.StrategyCounts) != 0 || len(got.RegimeCounts) != 0 || got.DecisionBasis != "adaptive_decisions_absent_by_fixed_policy" || got.ObservableTimeBuckets != 23 {
		t.Fatalf("fixed policy inferred adaptive behavior: %+v, %v", got, err)
	}
}

func TestResearchBehaviorSharedReaderPreservesObservationEncoding(t *testing.T) {
	policy := validShadowResearchPolicy()
	directory := privateTestDirectory(t)
	writeShadowResearchDay(t, directory, policy, "2026-09-04", []uint64{100_000_000})
	records, err := journal.ReadRecords(filepath.Join(directory, "shadow-2026-09-04.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	// Pin the pre-extraction artifact shape and independently known flat-price
	// metrics, not a second invocation of the refactored builder.
	expected, err := (researchpacket.RecordedObservations{
		Version: 1, Kind: "recorded_paper_observations", PaperOnly: true, AdvisoryOnly: true,
		Market: "SOL/USDC", PolicySHA256: fingerprint,
		ObservedFrom:    time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC),
		ObservedThrough: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC).Add(-time.Nanosecond),
		Journal:         researchpacket.RecordedJournal{Day: "2026-09-04", Records: len(records), ChainHeadSHA256: records[len(records)-1].Hash},
		Metrics:         researchpacket.ObservationMetrics{ObservableBPS: 9583},
	}).Seal()
	if err != nil {
		t.Fatal(err)
	}
	var want, got bytes.Buffer
	if err := json.NewEncoder(&want).Encode(expected); err != nil {
		t.Fatal(err)
	}
	if err := runResearchObservations([]string{"--policy", writeShadowPolicy(t, policy), "--journal-dir", directory}, &got,
		func() time.Time { return time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC) }); err != nil || !bytes.Equal(want.Bytes(), got.Bytes()) {
		t.Fatalf("legacy artifact encoding changed: %v\nwant %s\ngot %s", err, want.Bytes(), got.Bytes())
	}
}

func TestResearchBehaviorInvalidEvidenceEmitsNoCounts(t *testing.T) {
	policy := adaptiveShadowSearchPolicy()
	policy.TickSeconds, policy.Adaptive.MaxObservationGapSeconds = 3600, 3600
	source := privateTestDirectory(t)
	writeResearchBehaviorDay(t, source, policy, []time.Duration{time.Hour, 2 * time.Hour, 3 * time.Hour, 4 * time.Hour})
	records, err := journal.ReadRecords(filepath.Join(source, "shadow-2026-09-04.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "corrupt", "wrong policy", "unpaired", "invented decision", "incomplete", "older day", "relative path"} {
		t.Run(name, func(t *testing.T) {
			directory := privateTestDirectory(t)
			current := policy
			now := time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC)
			path := filepath.Join(directory, "shadow-2026-09-04.jsonl")
			switch name {
			case "missing":
			case "corrupt":
				if err := os.WriteFile(path, []byte("not a journal\n"), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				store, err := journal.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				for _, record := range records {
					if name == "incomplete" && record.Type == shadow.EventClosed {
						continue
					}
					payload := record.Payload
					if record.Type == shadow.EventWaiting && (name == "unpaired" || name == "invented decision") {
						var tick shadow.Tick
						if err := json.Unmarshal(payload, &tick); err != nil {
							t.Fatal(err)
						}
						if name == "unpaired" {
							tick.SecondaryPrice = nil
						} else {
							tick.Decision.Reason = "invented"
						}
						payload, err = json.Marshal(tick)
						if err != nil {
							t.Fatal(err)
						}
					}
					if _, err := store.Append(record.At, record.Type, record.ActionID, payload); err != nil {
						t.Fatal(err)
					}
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if name == "wrong policy" {
				current.FeeLamports++
			}
			if name == "older day" {
				now = now.Add(24 * time.Hour)
			}
			if name == "relative path" {
				directory = "relative"
			}
			var output bytes.Buffer
			err := runResearchBehavior([]string{"--policy", writeShadowPolicy(t, current), "--journal-dir", directory}, &output, func() time.Time { return now })
			if err == nil || output.Len() != 0 {
				t.Fatalf("unverified evidence emitted counts: %q, %v", output.String(), err)
			}
		})
	}
}

// The real runner generates every decision; constant prices independently imply
// three warming decisions followed by range/no-cost-hurdle decisions (window 4).
func writeResearchBehaviorDay(t *testing.T, directory string, policy shadow.Policy, offsets []time.Duration) {
	t.Helper()
	primary := &shadowSearchReader{identity: policy.Trigger.PrimarySourceSHA256, price: 100_000_000}
	secondary := &shadowSearchReader{identity: policy.Trigger.SecondarySourceSHA256, price: 100_000_000}
	readers := []*shadowSearchReader{
		{identity: policy.QuotePeg.PrimarySourceSHA256, price: 1_000_000},
		{identity: policy.QuotePeg.SecondarySourceSHA256, price: 1_000_000},
	}
	if policy.NativeFeePrice != nil {
		primary.price, secondary.price = 1_000_000, 1_000_000
		readers = append(readers,
			&shadowSearchReader{identity: policy.NativeFeePrice.PrimarySourceSHA256, price: 100_000_000},
			&shadowSearchReader{identity: policy.NativeFeePrice.SecondarySourceSHA256, price: 100_000_000})
	}
	var extra []shadow.PriceReader
	for _, reader := range readers {
		extra = append(extra, reader)
	}
	roll, err := newDailyJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := shadow.NewRunner(policy, primary, secondary, shadowSearchUnavailableQuoter{}, roll, extra...)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	for _, offset := range offsets {
		at := start.Add(offset)
		primary.at, secondary.at = at, at
		for _, reader := range readers {
			reader.at = at
		}
		if _, err := runner.Step(t.Context(), at); err != nil {
			t.Fatal(err)
		}
	}
	if err := runner.ClosePeriod(start.Add(24*time.Hour-time.Nanosecond), primary.price); err != nil {
		t.Fatal(err)
	}
	if err := roll.Close(); err != nil {
		t.Fatal(err)
	}
}
