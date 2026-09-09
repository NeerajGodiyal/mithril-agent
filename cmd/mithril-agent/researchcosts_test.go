package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/researchpacket"
)

func TestResearchCostsPreserveReplayAccountingAndEvidence(t *testing.T) {
	policy := adaptiveShadowSearchPolicy()
	policy.TickSeconds, policy.Adaptive.MaxObservationGapSeconds = 3600, 3600
	dir := privateTestDirectory(t)
	writeAdaptiveShadowResearchDay(t, dir, policy, "2026-09-04", []uint64{100_000_000, 103_000_000, 106_000_000, 109_000_000, 106_000_000, 103_000_000})
	path := filepath.Join(dir, "shadow-2026-09-04.jsonl")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := writeShadowPolicy(t, policy)
	args := []string{"--policy", policyPath, "--journal-dir", dir}
	now := func() time.Time { return time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC) }
	var output, again bytes.Buffer
	if err := runResearchCosts(args, &output, now); err != nil {
		t.Fatal(err)
	}
	var got researchCosts
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	pin, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "modelled_paper_cost_sensitivity" || !got.PaperOnly || !got.AdvisoryOnly || !got.PoolModelled ||
		got.Authorized || got.Promotable || got.RecordedBasisEligible || got.PolicySHA256 != pin ||
		got.Journal.Day != "2026-09-04" || got.Journal.ChainHeadSHA256 == "" || !got.CoverageSufficient || len(got.Lanes) != 4 {
		t.Fatalf("incorrect cost sensitivity envelope: %+v", got)
	}
	for i, spread := range []uint64{1, 10, 25, 100} {
		var ordinary bytes.Buffer
		if err := runShadowBacktest([]string{"--policy", policyPath, "--dir", dir, "--day", "2026-09-04", "--spread-bps", fmt.Sprint(spread), "--json", "--explain-filters"}, &ordinary); err != nil {
			t.Fatal(err)
		}
		var expected backtestResult
		if err := json.Unmarshal(ordinary.Bytes(), &expected); err != nil {
			t.Fatal(err)
		}
		lane := got.Lanes[i]
		if i == 0 && (lane.Counts.Sells == 0 || lane.Counts.Buys == 0) {
			t.Fatalf("fixture did not exercise both settlement sides: %+v", lane.Counts)
		}
		if lane.SpreadBPS != spread || lane.Counts != expected.Counts || !maps.Equal(lane.FilteredReasons, expected.FilteredReasons) ||
			lane.OpeningEquity != expected.OpeningEquity || lane.ClosingEquity != expected.ClosingEquity || lane.VersusHold != expected.VersusHold ||
			lane.NetChange != int64(expected.ClosingEquity)-int64(expected.OpeningEquity) {
			t.Fatalf("lane %d changed ordinary replay accounting: got %+v, want %+v", spread, lane, expected)
		}
		var filtered uint64
		for _, count := range lane.FilteredReasons {
			filtered += count
		}
		if filtered != lane.Counts.Filtered {
			t.Fatal("filter reason counts do not reconcile")
		}
	}
	if err := runResearchCosts(args, &again, now); err != nil || !bytes.Equal(output.Bytes(), again.Bytes()) {
		t.Fatal("identical evidence changed diagnostic output")
	}
	var artifact researchpacket.RecordedObservations
	if err := json.Unmarshal(output.Bytes(), &artifact); err != nil || artifact.Validate() == nil {
		t.Fatal("modeled diagnostic became qualifying recorded evidence")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || bytes.Contains(output.Bytes(), []byte(dir)) || bytes.Contains(output.Bytes(), []byte(policy.Observe)) {
		t.Fatal("diagnostic changed or exposed private evidence")
	}
}

func TestResearchCostsRejectInvalidEvidenceWithoutPartialOutput(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt", "wrong-policy", "unclosed", "current-day", "fixed"} {
		t.Run(mode, func(t *testing.T) {
			policy := adaptiveShadowSearchPolicy()
			policy.TickSeconds, policy.Adaptive.MaxObservationGapSeconds = 3600, 3600
			dir := privateTestDirectory(t)
			path := filepath.Join(dir, "shadow-2026-09-04.jsonl")
			if mode != "missing" {
				writeResearchBehaviorDay(t, dir, policy, []time.Duration{time.Hour, 2 * time.Hour, 3 * time.Hour})
			}
			switch mode {
			case "corrupt":
				if err := os.WriteFile(path, []byte("invalid journal\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong-policy":
				policy.FeeLamports++
			case "unclosed":
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				lines := bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n"))
				if err := os.WriteFile(path, append(bytes.Join(lines[:len(lines)-1], []byte("\n")), '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			case "fixed":
				policy = validShadowPolicy()
			}
			now := func() time.Time {
				if mode == "current-day" {
					return time.Date(2026, 9, 4, 23, 0, 0, 0, time.UTC)
				}
				return time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC)
			}
			var output bytes.Buffer
			if err := runResearchCosts([]string{"--policy", writeShadowPolicy(t, policy), "--journal-dir", dir}, &output, now); err == nil || output.Len() != 0 {
				t.Fatal("invalid history produced a report")
			}
		})
	}
}

func TestResearchCostsSupportJUPAndLabelSparseCoverage(t *testing.T) {
	policy, err := buildAdaptiveJUPPolicy(25_000_000, 20_000_000, 3_000_000, 100, 100_000, "So11111111111111111111111111111111111111112", 3600)
	if err != nil {
		t.Fatal(err)
	}
	dir := privateTestDirectory(t)
	writeResearchBehaviorDay(t, dir, policy, []time.Duration{time.Hour, 2 * time.Hour, 3 * time.Hour})
	var output bytes.Buffer
	if err := runResearchCosts([]string{"--policy", writeShadowPolicy(t, policy), "--journal-dir", dir}, &output,
		func() time.Time { return time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC) }); err != nil {
		t.Fatal(err)
	}
	var report researchCosts
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Market != "JUP/USDC" || report.CoverageSufficient || report.ObservableBPS != 1250 || len(report.Lanes) != 4 {
		t.Fatalf("sparse JUP history misrepresented: %+v", report)
	}
	output.Reset()
	if err := runResearch([]string{"cost-sensitivity", "--help"}, &output); err != nil || output.Len() == 0 {
		t.Fatal("command is not routed")
	}
}
