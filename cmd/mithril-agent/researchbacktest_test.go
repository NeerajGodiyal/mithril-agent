package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/researchpacket"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestResearchPacketBacktestBindsOneRetrospectiveDayWithoutWrites(t *testing.T) {
	p := adaptiveShadowSearchPolicy()
	p.TickSeconds, p.Adaptive.MaxObservationGapSeconds = 300, 600
	p.Adaptive.MaxVolatilityBPS = 5_000
	root := privateTestDirectory(t)
	day := "2026-08-29"
	writeAdaptiveShadowResearchDay(t, root, p, day, []uint64{
		100_000_000, 96_000_000, 92_000_000, 88_000_000, 84_000_000,
		88_000_000, 92_000_000, 96_000_000, 100_000_000, 104_000_000,
	})
	created := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	observation, err := buildResearchObservations(p, root, created)
	if err != nil {
		t.Fatal(err)
	}
	model := boundShadowResearchPacket(t, p, created, shadowMarketPair(p))
	model.Version, model.ContentSHA256, model.VerifiedFacts = researchpacket.RecordedVersion, "", nil
	model.RecordedEvidence = &researchpacket.RecordedReference{ContentSHA256: observation.ContentSHA256, MetricIDs: []string{"signals", "fills"}}
	seal := func(model researchpacket.Packet, basis researchpacket.RecordedObservations) researchpacket.Packet {
		t.Helper()
		model.ContentSHA256, model.RecordedObservations = "", nil
		raw, err := json.Marshal(model)
		if err != nil {
			t.Fatal(err)
		}
		packet, err := researchpacket.ParseWithRecorded(raw, &basis, created)
		if err != nil {
			t.Fatal(err)
		}
		return packet
	}
	packet := seal(model, observation)
	packetPath, policyPath := filepath.Join(root, "packet.json"), filepath.Join(root, "policy.json")
	writeJSON(t, packetPath, packet)
	writeJSON(t, policyPath, p)
	args := []string{"--in", packetPath, "--packet-sha256", packet.ContentSHA256, "--policy", policyPath,
		"--journal-dir", root, "--day", day, "--spread-bps", "1"}
	// Only the consumed basis day exists. Neither earlier folds nor a later
	// holdout can be read, and an expired packet remains historical evidence.
	clock := func() time.Time { return created.Add(48 * time.Hour) }
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	original := make(map[string][]byte)
	for _, entry := range before {
		raw, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		original[entry.Name()] = raw
	}
	var output bytes.Buffer
	if err := runResearchPacketBacktest(args, &output, clock); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	var result researchBacktestResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	controller := shadowResearchController{policy: p, basePolicy: p, journalDir: root, researchPacket: &packet}
	candidate, _, _, err := controller.bindResearchPacket(packet.ContentSHA256, packet.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := readResearchDay(p, root, packet.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != "retrospective_training" || result.PacketSHA256 != packet.ContentSHA256 ||
		result.RecordedBasisSHA256 != observation.ContentSHA256 || result.Journal != observation.Journal || result.SpreadBPS != 1 {
		t.Fatal("diagnostic lost its consumed-day provenance")
	}
	if len(result.ParameterChanges) == 0 || !reflect.DeepEqual(result.ParameterChanges, packet.CandidateParameterDiff) {
		t.Fatal("diagnostic lost the exact tested parameter changes")
	}
	for i, policy := range []shadow.Policy{p, candidate} {
		replayed, err := shadow.ReplayRoundTripTicksWithDiagnostics(policy, recorded.ticks, modelledPool(policy, 1, policy.SlippageBPS))
		if err != nil {
			t.Fatal(err)
		}
		score, err := scoreShadowRoundTripResult(replayed)
		if err != nil {
			t.Fatal(err)
		}
		equity, err := replayed.Ledger.EquityMicros(replayed.ClosingPrice)
		if err != nil {
			t.Fatal(err)
		}
		fingerprint, err := policy.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		lane := []researchBacktestLane{result.Base, result.Candidate}[i]
		want := researchBacktestLane{Counts: replayed.Counts, FilteredReasons: replayed.FilteredReasons,
			EquityMicros: equity, VersusHoldMicros: score.VersusHoldMicros, MaxDrawdownMicros: score.MaxDrawdownMicros}
		if !reflect.DeepEqual(lane, want) || []string{result.BasePolicySHA256, result.CandidatePolicySHA256}[i] != fingerprint {
			t.Fatalf("lane %d differs from the unchanged replay engine", i)
		}
		if lane.Counts.BuySignals+lane.Counts.SellSignals == 0 || lane.Counts.Buys+lane.Counts.Sells == 0 {
			t.Fatalf("lane %d fixture did not exercise signals and fills", i)
		}
	}
	var costly bytes.Buffer
	if err := runResearchPacketBacktest(append(append([]string(nil), args...), "--spread-bps", "500"), &costly, clock); err != nil {
		t.Fatal(err)
	}
	var filtered researchBacktestResult
	if err := json.Unmarshal(costly.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	for _, lane := range []researchBacktestLane{filtered.Base, filtered.Candidate} {
		if lane.Counts.Filtered == 0 || lane.FilteredReasons["quote_impact_limit"] != lane.Counts.Filtered {
			t.Fatal("diagnostic lost modeled quote-filter explanations")
		}
	}
	for _, key := range []string{"paper_only", "pool_modelled"} {
		if string(decoded[key]) != "true" {
			t.Fatalf("missing retrospective marker %s", key)
		}
	}
	for _, key := range []string{"authorized", "promotable"} {
		if string(decoded[key]) != "false" {
			t.Fatalf("unexpected authority marker %s", key)
		}
	}
	for _, private := range []string{root, p.Observe} {
		if private != "" && bytes.Contains(output.Bytes(), []byte(private)) {
			t.Fatal("diagnostic exposed a private path or account")
		}
	}
	var repeated bytes.Buffer
	if err := runResearchPacketBacktest(args, &repeated, clock); err != nil || !bytes.Equal(output.Bytes(), repeated.Bytes()) {
		t.Fatalf("retrospective result changed on identical input: %v", err)
	}
	after, err := os.ReadDir(root)
	if err != nil || len(before) != len(after) {
		t.Fatal("diagnostic created or removed a file")
	}
	for name, want := range original {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("diagnostic changed an input")
		}
	}
	for _, test := range []struct {
		name  string
		extra []string
		now   time.Time
	}{
		{"digest", []string{"--packet-sha256", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, clock()},
		{"other day", []string{"--day", "2026-08-28"}, clock()},
		{"zero cost", []string{"--spread-bps", "0"}, clock()},
		{"total cost", []string{"--spread-bps", "10000"}, clock()},
		{"future packet", nil, created.Add(-time.Hour)},
		{"candidate output", []string{"--candidate-out", filepath.Join(root, "candidate")}, clock()},
	} {
		t.Run(test.name, func(t *testing.T) {
			var rejected bytes.Buffer
			if err := runResearchPacketBacktest(append(append([]string(nil), args...), test.extra...), &rejected, func() time.Time { return test.now }); err == nil || rejected.Len() != 0 {
				t.Fatalf("invalid request emitted a result: %v", err)
			}
		})
	}
	var missingCost bytes.Buffer
	if err := runResearchPacketBacktest(args[:len(args)-2], &missingCost, clock); err == nil || missingCost.Len() != 0 {
		t.Fatal("diagnostic silently defaulted its cost assumption")
	}
	for _, kind := range []string{"current value", "resealed observation", "changed policy", "damaged journal"} {
		t.Run(kind, func(t *testing.T) {
			for name, raw := range original {
				if err := os.WriteFile(filepath.Join(root, name), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			probe, basis := model, observation
			probe.CandidateParameterDiff = append([]researchpacket.ParameterChange(nil), model.CandidateParameterDiff...)
			switch kind {
			case "current value":
				probe.CandidateParameterDiff[0].Current++
			case "resealed observation":
				basis.Metrics.VersusHoldMicros++
				basis, err = basis.Seal()
				if err != nil {
					t.Fatal(err)
				}
				probe.RecordedEvidence = &researchpacket.RecordedReference{ContentSHA256: basis.ContentSHA256, MetricIDs: []string{"versus_hold_micros"}}
			case "changed policy":
				changed := p
				changed.SlippageBPS++
				writeJSON(t, policyPath, changed)
			case "damaged journal":
				if err := os.WriteFile(filepath.Join(root, "shadow-"+day+".jsonl"), []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			modified := seal(probe, basis)
			writeJSON(t, packetPath, modified)
			var rejected bytes.Buffer
			request := append(append([]string(nil), args...), "--packet-sha256", modified.ContentSHA256)
			if err := runResearchPacketBacktest(request, &rejected, clock); err == nil || rejected.Len() != 0 {
				t.Fatalf("unverified input emitted a result: %v", err)
			}
		})
	}
}
