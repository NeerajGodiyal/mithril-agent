package paperdashboard

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/researchpacket"
)

func TestResearchProjectionShowsOnlyValidatedPacketStatus(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	packet := researchpacket.Packet{
		Version: researchpacket.Version, HypothesisID: "jto-range-20260901",
		CreatedAt: now.Add(-time.Minute), ValidUntil: now.Add(5 * time.Hour),
		Market: "JTO/USDC", Disposition: researchpacket.DispositionCandidate,
		VerifiedFacts: []researchpacket.Fact{
			{ID: "route_quality", Claim: "The route evidence is current.", Status: researchpacket.FactVerified, Sources: []researchpacket.Source{
				{URL: "https://solana.com/docs/core", RetrievedAt: now.Add(-time.Minute)},
				{URL: "https://github.com/anza-xyz/agave/releases", RetrievedAt: now.Add(-time.Minute)},
			}},
			{ID: "route_status", Claim: "The same owners report current status.", Status: researchpacket.FactVerified, Sources: []researchpacket.Source{
				{URL: "https://solana.com/docs/core", RetrievedAt: now.Add(-time.Minute)},
				{URL: "https://github.com/anza-xyz/agave/releases", RetrievedAt: now.Add(-time.Minute)},
			}},
		},
		BullCase: "The paper hypothesis may improve range entries.",
		BearCase: "The move may be noise.", NoTradeCase: "Keep the current plan.",
		ExecutionCostCase: "Replay fees and route impact.",
		RiskVeto: researchpacket.RiskVeto{
			Decision: researchpacket.VetoPass, Reason: "Costs & uncertainty remain bounded.",
		},
		CandidateParameterDiff: []researchpacket.ParameterChange{{
			Name: "minimum_signal_bps", Current: 80, Proposed: 90,
		}},
		RejectionConditions: []string{"Reject if forward evidence loses."},
		OutOfSampleTest:     "Run a separate paired forward challenge.",
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	packet, err = researchpacket.Parse(raw, now)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "research.json")
	if err := os.WriteFile(path, stored, 0o600); err != nil {
		t.Fatal(err)
	}
	writeResearchEvidence(t, path, packet, []string{
		"https://solana.com/docs/core", "https://github.com/anza-xyz/agave/releases",
	})
	server, err := New([]Source{&sourceStub{label: "SOL/USDC"}})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	if err := server.EnableResearch(path); err != nil {
		t.Fatal(err)
	}
	view := server.snapshot()
	if !view.ResearchEnabled || view.ResearchError || view.Research == nil ||
		view.Research.EvidenceBasis != "web_sources" || view.Research.RetrospectiveScreening ||
		view.Research.ObservationDay != "" || len(view.Research.ObservationMetricIDs) != 0 ||
		!view.Research.Current || !view.Research.Actionable ||
		view.Research.TwoSourceClaims != 2 || view.Research.RetrievedCitations != 4 ||
		view.Research.OfficialPagesChecked != 2 ||
		view.Research.RetrievedPages != 2 || view.Research.ResearchSessions != 4 ||
		len(view.Research.ResearchToolCalls) != 3 || view.Research.SuccessfulWebSearches != 1 ||
		view.Research.SourcesChecked != 2 || view.Research.SingleSource != 0 ||
		view.Research.Contradicted != 0 || view.Research.Unverified != 0 ||
		len(view.Research.ProposedChanges) != 1 {
		t.Fatalf("research = %+v, error = %v", view.Research, view.ResearchError)
	}

	stored[len(stored)/2] ^= 1
	if err := os.WriteFile(path, stored, 0o600); err != nil {
		t.Fatal(err)
	}
	now = now.Add(refreshInterval)
	view = server.snapshot()
	if !view.ResearchError || view.Research != nil {
		t.Fatalf("tampered packet was exposed: %+v", view.Research)
	}
}

func TestResearchProjectionDistinguishesRecordedPaperEvidence(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	day := now.Truncate(24*time.Hour).AddDate(0, 0, -1)
	observation, err := (researchpacket.RecordedObservations{
		Version: 1, Kind: "recorded_paper_observations", PaperOnly: true, AdvisoryOnly: true,
		Market: "SOL/USDC", PolicySHA256: strings.Repeat("a", 64),
		ObservedFrom: day, ObservedThrough: day.Add(24*time.Hour - time.Nanosecond),
		Journal: researchpacket.RecordedJournal{Day: "2026-09-04", Records: 100, ChainHeadSHA256: strings.Repeat("b", 64)},
		Metrics: researchpacket.ObservationMetrics{ObservableBPS: 10_000, Signals: 3, Fills: 2},
	}).Seal()
	if err != nil {
		t.Fatal(err)
	}
	input := researchpacket.Packet{
		Version: researchpacket.RecordedVersion, HypothesisID: "recorded-sol-20260905",
		CreatedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
		Market: observation.Market, Disposition: researchpacket.DispositionCandidate,
		RecordedEvidence: &researchpacket.RecordedReference{ContentSHA256: observation.ContentSHA256,
			MetricIDs: []string{"signals", "fills"}},
		BullCase: "Test a bounded timing change.", BearCase: "Past patterns may not recur.",
		NoTradeCase: "Keep the current policy.", ExecutionCostCase: "Retain all modeled costs.",
		RiskVeto:               researchpacket.RiskVeto{Decision: researchpacket.VetoPass, Reason: "Paper research only."},
		CandidateParameterDiff: []researchpacket.ParameterChange{{Name: "minimum_signal_bps", Current: 80, Proposed: 90}},
		RejectionConditions:    []string{"Reject without independent forward evidence."},
		OutOfSampleTest:        "Freeze the candidate before a new forward window.",
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := researchpacket.ParseWithRecorded(raw, &observation, now)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "research.json")
	writeJSON := func(path string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(path, packet)
	// A recorded-only run has no successful web extraction to invent.
	evidencePath := filepath.Join(filepath.Dir(path), "research-evidence.json")
	evidence := researchEvidence{Version: researchEvidenceVersion, CreatedAt: packet.CreatedAt,
		PacketSHA256: packet.ContentSHA256, SessionExportSHA256: strings.Repeat("c", 64),
		SessionCount: 1, ToolCalls: []ResearchToolCount{{Name: "delegate_task", Count: 1}}}
	writeJSON(evidencePath, evidence)
	view, err := readResearch(path, now)
	if err != nil || view == nil {
		t.Fatalf("recorded projection: %v", err)
	}
	if view.EvidenceBasis != "recorded_paper_observations" || !view.RetrospectiveScreening ||
		view.ObservationDay != observation.Journal.Day || strings.Join(view.ObservationMetricIDs, ",") != "signals,fills" ||
		!view.Current || !view.Actionable || view.TwoSourceClaims != 0 || view.RetrievedCitations != 0 ||
		view.SourcesChecked != 0 || view.OfficialPagesChecked != 0 || view.RetrievedPages != 0 || view.SuccessfulWebSearches != 0 {
		t.Fatalf("recorded measurements were misrepresented: %+v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"evidence_basis":"recorded_paper_observations"`, `"observation_day":"2026-09-04"`,
		`"observation_metric_ids":["signals","fills"]`, `"retrospective_screening":true`} {
		if !bytes.Contains(encoded, []byte(want)) {
			t.Errorf("projection missing %s", want)
		}
	}
	for _, unwanted := range []string{"policy_sha256", "chain_head_sha256", "recorded_observations", "versus_hold_micros"} {
		if bytes.Contains(encoded, []byte(unwanted)) {
			t.Errorf("projection exposes unused artifact field %s", unwanted)
		}
	}
	view.ObservationMetricIDs[0] = "changed"
	fresh, err := readResearch(path, now)
	if err != nil || fresh.ObservationMetricIDs[0] != "signals" {
		t.Fatalf("projection mutation persisted: %v", err)
	}
	packet.RecordedObservations.Metrics.Fills++
	writeJSON(path, packet)
	if _, err := readResearch(path, now); err == nil {
		t.Fatal("tampered recorded packet was projected")
	}
}

func TestResearchProjectionExplainsNonVerifiedSources(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	packet := researchpacket.Packet{
		Version: researchpacket.Version, HypothesisID: "jup-no-change-20260901",
		CreatedAt: now.Add(-time.Minute), ValidUntil: now.Add(5 * time.Hour),
		Market: "JUP/USDC", Disposition: researchpacket.DispositionNoChange,
		VerifiedFacts: []researchpacket.Fact{
			{ID: "one_source", Claim: "Only one source supports this claim.", Status: researchpacket.FactSingleSource, Sources: []researchpacket.Source{{URL: "https://solana.com/docs/core", RetrievedAt: now.Add(-time.Minute)}}},
			{ID: "conflict", Claim: "The sources conflict.", Status: researchpacket.FactContradicted, Sources: []researchpacket.Source{{URL: "https://solana.com/docs/core", RetrievedAt: now.Add(-time.Minute)}, {URL: "https://github.com/anza-xyz/agave/releases", RetrievedAt: now.Add(-time.Minute)}}},
			{ID: "unknown", Claim: "No source verified this claim.", Status: researchpacket.FactUnverified},
		},
		BullCase: "The hypothesis may help.", BearCase: "It may be noise.",
		NoTradeCase: "Keep the current plan.", ExecutionCostCase: "Retain modeled costs.",
		RiskVeto:            researchpacket.RiskVeto{Decision: researchpacket.VetoReject, Reason: "Evidence is insufficient."},
		RejectionConditions: []string{"Reject without independent evidence."},
		OutOfSampleTest:     "Wait for independent evidence before replay.",
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	packet, err = researchpacket.Parse(raw, now)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "research.json")
	if err := os.WriteFile(path, stored, 0o600); err != nil {
		t.Fatal(err)
	}
	writeResearchEvidence(t, path, packet, []string{
		"https://solana.com/docs/core", "https://github.com/anza-xyz/agave/releases",
	})
	server, err := New([]Source{&sourceStub{label: "JUP/USDC"}})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	if err := server.EnableResearch(path); err != nil {
		t.Fatal(err)
	}
	research := server.snapshot().Research
	if research == nil || research.SourcesChecked != 2 || research.OfficialPagesChecked != 2 ||
		research.RetrievedPages != 2 || research.ResearchSessions != 4 || research.TwoSourceClaims != 0 ||
		research.SingleSource != 1 || research.Contradicted != 1 || research.Unverified != 1 {
		t.Fatalf("research = %+v", research)
	}
}

func TestResearchProjectionDoesNotAffectMarketCompleteness(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	server, err := New([]Source{&sourceStub{label: "SOL/USDC", snapshot: paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: now, Current: "PAPER · Watching",
		Summary: &paperstatus.CurrentSummary{
			Market: "SOL/USDC", ValueUnit: "USD", Day: "2026-09-01", TickSeconds: 60,
			OpeningEquityMicros: 1, EquityMicros: 1, HoldBenchmarkMicros: 1,
			Checks: 1, PriceMicros: 100_000_000, State: "watching",
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	if err := server.EnableResearch(filepath.Join(t.TempDir(), "missing.json")); err != nil {
		t.Fatal(err)
	}
	view := server.snapshot()
	if !view.Complete || !view.ResearchEnabled || view.ResearchError || view.Research != nil {
		t.Fatalf("missing research packet was misreported: %+v", view)
	}
}

func TestResearchProjectionFailsClosedWithoutMatchingSessionEvidence(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	packet := researchpacket.Packet{
		Version: researchpacket.Version, HypothesisID: "sol-evidence-20260901",
		CreatedAt: now.Add(-time.Minute), ValidUntil: now.Add(5 * time.Hour),
		Market: "SOL/USDC", Disposition: researchpacket.DispositionNoChange,
		BullCase: "The hypothesis may help.", BearCase: "It may be noise.",
		NoTradeCase: "Keep the current plan.", ExecutionCostCase: "Retain modeled costs.",
		RiskVeto:            researchpacket.RiskVeto{Decision: researchpacket.VetoReject, Reason: "No independent evidence."},
		RejectionConditions: []string{"Reject without independent evidence."},
		OutOfSampleTest:     "Wait for evidence before replay.",
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	packet, err = researchpacket.Parse(raw, now)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "research.json")
	if err := os.WriteFile(path, stored, 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := New([]Source{&sourceStub{label: "SOL/USDC"}})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	if err := server.EnableResearch(path); err != nil {
		t.Fatal(err)
	}
	view := server.snapshot()
	if !view.ResearchError || view.Research != nil {
		t.Fatalf("packet without session evidence was exposed: %+v", view.Research)
	}
}

func TestRenderPerpsResearchIsDeterministicContentBoundAndMinimal(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	paths := writePerpsResearchStatuses(t, now)
	first, err := RenderPerpsResearch(paths)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderPerpsResearch(paths)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("deterministic render = %q, %v", second, err)
	}
	for index, market := range []string{"SOL-PERP", "BTC-PERP", "ETH-PERP"} {
		rewritePerpsResearchStatus(t, paths[market], func(snapshot *paperstatus.Snapshot) {
			liveAt := now.Add(time.Duration(index+1) * time.Minute)
			snapshot.ObservedAt = liveAt
			snapshot.Current = "PAPER · Recording next run"
			snapshot.Events = nil
			snapshot.Summary = &paperstatus.CurrentSummary{
				Market: market, Instrument: "perpetual", RiskProfile: "balanced",
				PositionDirection: "flat", LeverageBPS: 20_000, FundingTracked: true,
				ValueUnit: "USD", Day: liveAt.Format("2006-01-02"), TickSeconds: 15,
				OpeningEquityMicros: 100_000_000, EquityMicros: 100_000_000,
				HoldBenchmarkMicros: 100_000_000, Checks: uint64(index + 1),
				State: "watching", Strategy: "fixed",
			}
		})
	}
	third, err := RenderPerpsResearch(paths)
	if err != nil || !bytes.Equal(first, third) {
		t.Fatalf("live run changed completed research = %q, %v", third, err)
	}
	for _, forbidden := range []string{
		"wallet-secret-marker", "policy-secret-marker", "human-event-marker",
		`"events"`, `"current"`, `"equity_micros"`, `"realized_micros"`, `"history"`, `"path"`,
	} {
		if bytes.Contains(first, []byte(forbidden)) {
			t.Errorf("perps research output contains %q", forbidden)
		}
	}
	var summary perpsResearchSummary
	if err := json.Unmarshal(first, &summary); err != nil {
		t.Fatal(err)
	}
	wantDigest, err := perpsResearchFingerprint(summary)
	if err != nil || summary.ContentSHA256 != wantDigest || summary.Version != 3 ||
		!summary.PaperOnly || !summary.AdvisoryOnly || summary.Authorized || summary.Promotable ||
		!summary.ObservedAt.Equal(now) || len(summary.Markets) != 3 {
		t.Fatalf("summary = %+v, digest error = %v", summary, err)
	}
	for index, market := range []string{"SOL-PERP", "BTC-PERP", "ETH-PERP"} {
		item := summary.Markets[index]
		raw, err := os.ReadFile(paths[market])
		if err != nil {
			t.Fatal(err)
		}
		var snapshot paperstatus.Snapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			t.Fatal(err)
		}
		completed, ok := paperstatus.LatestCompletedSnapshot(snapshot)
		completedDigest, digestErr := paperstatus.CompletedSnapshotSHA256(completed)
		if !ok || digestErr != nil || item.Market != market || item.CompletedSnapshotSHA256 != completedDigest ||
			item.DecisionSource == "" || item.ProposalSource == "" || item.RunPlanSHA256 == "" ||
			item.QualificationOutcome != "candidate_ready_for_more_paper_testing" ||
			item.QualificationInputSHA256 == "" || item.QualificationTapes != 4 ||
			item.QualificationFrames != 421 || item.QualificationTrainingFrames != 390 ||
			item.QualificationHoldoutFrames != 31 || !item.QualificationHoldoutScored ||
			!item.QualificationStressScored || len(item.QualificationAttempts) != 1 ||
			item.QualificationAttempts[0].ClosedPositions != 2 {
			t.Fatalf("market %d = %+v", index, item)
		}
		if index == 0 {
			if item.DecisionSource != "selected_paper_plan" || item.ProposalSource != "deterministic_search" ||
				item.RunStrategy != "momentum" || item.RunRiskProfile != "balanced" ||
				item.PerpsPlanOutcome == nil || item.PerpsPlanOutcome.Result != "gain" ||
				item.PerpsPlanOutcome.TapeSHA256 != strings.Repeat("f", 64) {
				t.Fatalf("selected plan outcome = %+v", item)
			}
		} else if item.DecisionSource != "legacy_fixed_policy" || item.ProposalSource != "built_in" ||
			item.RunStrategy != "fixed" || item.PerpsPlanOutcome != nil {
			t.Fatalf("built-in plan attribution = %+v", item)
		}
	}
	tampered := summary
	tampered.Markets = append([]perpsResearchMarket(nil), summary.Markets...)
	tampered.Markets[0].QualificationFrames++
	if digest, err := perpsResearchFingerprint(tampered); err != nil || digest == tampered.ContentSHA256 {
		t.Fatalf("tampered summary retained digest %q, %v", digest, err)
	}
}

func TestRenderPerpsResearchRejectsUnsafeOrMixedSnapshots(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	t.Run("mixed completion", func(t *testing.T) {
		paths := writePerpsResearchStatuses(t, now)
		rewritePerpsResearchStatus(t, paths["ETH-PERP"], func(snapshot *paperstatus.Snapshot) {
			snapshot.ObservedAt = snapshot.ObservedAt.Add(time.Second)
			snapshot.LatestCompleted.ObservedAt = snapshot.LatestCompleted.ObservedAt.Add(time.Second)
			snapshot.LatestCompleted.Summary.Day = snapshot.LatestCompleted.ObservedAt.Format("2006-01-02")
		})
		if _, err := RenderPerpsResearch(paths); err == nil {
			t.Fatal("mixed completion times were accepted")
		}
	})
	t.Run("tampered status", func(t *testing.T) {
		paths := writePerpsResearchStatuses(t, now)
		rewritePerpsResearchStatus(t, paths["SOL-PERP"], func(snapshot *paperstatus.Snapshot) {
			snapshot.LatestCompleted.Summary.QualificationSHA256 = "not-a-digest"
		})
		if _, err := RenderPerpsResearch(paths); err == nil {
			t.Fatal("tampered qualification was accepted")
		}
	})
	t.Run("unsafe mode", func(t *testing.T) {
		paths := writePerpsResearchStatuses(t, now)
		if err := os.Chmod(paths["BTC-PERP"], 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := RenderPerpsResearch(paths); err == nil {
			t.Fatal("public status file was accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		paths := writePerpsResearchStatuses(t, now)
		source := paths["SOL-PERP"]
		target := source + ".target"
		if err := os.Rename(source, target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, source); err != nil {
			t.Fatal(err)
		}
		if _, err := RenderPerpsResearch(paths); err == nil {
			t.Fatal("symlinked status file was accepted")
		}
	})
	t.Run("wrong market", func(t *testing.T) {
		paths := writePerpsResearchStatuses(t, now)
		rewritePerpsResearchStatus(t, paths["SOL-PERP"], func(snapshot *paperstatus.Snapshot) {
			snapshot.LatestCompleted.Summary.Market = "JUP/USDC"
		})
		if _, err := RenderPerpsResearch(paths); err == nil {
			t.Fatal("mislabeled spot status was accepted")
		}
	})
	t.Run("extra market", func(t *testing.T) {
		paths := writePerpsResearchStatuses(t, now)
		paths["JUP-PERP"] = paths["SOL-PERP"]
		if _, err := RenderPerpsResearch(paths); err == nil {
			t.Fatal("extra market was accepted")
		}
	})
}

func writePerpsResearchStatuses(t *testing.T, observedAt time.Time) map[string]string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := make(map[string]string, 3)
	for index, market := range []string{"SOL-PERP", "BTC-PERP", "ETH-PERP"} {
		path := filepath.Join(directory, strings.ToLower(strings.TrimSuffix(market, "-PERP"))+".json")
		snapshot := paperstatus.Snapshot{
			Version: paperstatus.Version, ObservedAt: observedAt,
			Current: "PAPER · wallet-secret-marker policy-secret-marker",
			Events: []paperstatus.Event{{
				ID: strings.Repeat("d", 64), At: observedAt, Kind: paperstatus.KindExperimentDone,
				Message: "PAPER · human-event-marker",
			}},
			Summary: &paperstatus.CurrentSummary{
				Market: market, Instrument: "perpetual", RiskProfile: "balanced",
				PositionDirection: "flat", LeverageBPS: 20_000, FundingTracked: true,
				ValueUnit: "USD", Day: observedAt.Format("2006-01-02"), TickSeconds: 15,
				OpeningEquityMicros: 100_000_000, EquityMicros: 100_100_000,
				HoldBenchmarkMicros: 100_000_000, AccountingTracked: true,
				UnrealizedMicros: 100_000, Checks: 421, Signals: 2, Trades: 2,
				State: "watching", Strategy: "fixed",
				QualificationTracked: true,
				QualificationOutcome: "candidate_ready_for_more_paper_testing",
				QualificationSHA256:  strings.Repeat(string(rune('a'+index)), 64),
				QualificationTapes:   4, QualificationFrames: 421,
				QualificationMinimumFrames: 96, QualificationTrainingFrames: 390,
				QualificationHoldoutFrames: 31, QualificationStrategy: "momentum",
				QualificationRiskProfile: "balanced", QualificationHoldoutEvaluated: true,
				QualificationStressEvaluated: true, QualificationHoldoutScored: true,
				QualificationStressScored: true, QualificationHoldoutMicros: 100_000,
				QualificationStressMicros: 50_000,
				QualificationAttempts: []paperstatus.QualificationAttempt{{
					RiskProfile: "conservative", Strategy: "momentum", NetPnLMicros: -50_000,
					FeesMicros: 20_000, FundingMicros: -1_000, MaxDrawdownMicros: 70_000,
					FilledOrders: 2, ClosedPositions: 2,
				}},
			},
		}
		snapshot.Summary.DecisionSource = "legacy_fixed_policy"
		snapshot.Summary.ProposalSource = "built_in"
		snapshot.Summary.RunPlanSHA256 = strings.Repeat(string(rune('1'+index)), 64)
		if index == 0 {
			snapshot.Summary.Strategy = "momentum"
			snapshot.Summary.DecisionSource = "selected_paper_plan"
			snapshot.Summary.ProposalSource = "deterministic_search"
			snapshot.Summary.RealizedMicros = 100_000
			snapshot.Summary.UnrealizedMicros = 0
			snapshot.Summary.PerpsPlanOutcome = &paperstatus.PerpsPlanOutcome{
				TapeSHA256: strings.Repeat("f", 64), Result: "gain",
			}
		}
		completedSummary := *snapshot.Summary
		snapshot.LatestCompleted = &paperstatus.CompletedSnapshot{
			ObservedAt: observedAt, EventID: snapshot.Events[0].ID, Summary: completedSummary,
		}
		if err := paperstatus.ValidateSnapshot(snapshot); err != nil {
			t.Fatalf("fixture %s is invalid: %v", market, err)
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		paths[market] = path
	}
	return paths
}

func rewritePerpsResearchStatus(t *testing.T, path string, mutate func(*paperstatus.Snapshot)) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	mutate(&snapshot)
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeResearchEvidence(t *testing.T, packetPath string, packet researchpacket.Packet, retrieved []string) {
	t.Helper()
	cited := make(map[string]struct{})
	for _, fact := range packet.VerifiedFacts {
		for _, source := range fact.Sources {
			cited[source.URL] = struct{}{}
		}
	}
	evidence := researchEvidence{
		Version: researchEvidenceVersion, CreatedAt: packet.CreatedAt,
		PacketSHA256: packet.ContentSHA256, SessionExportSHA256: "a" + strings.Repeat("0", 63),
		SessionCount: 4, ToolCalls: []ResearchToolCount{
			{Name: "delegate_task", Count: 1}, {Name: "web_extract", Count: uint64(len(retrieved))},
			{Name: "web_search", Count: 1},
		},
		SuccessfulWebSearches: 1, RetrievedURLs: retrieved,
		OfficialPagesChecked: uint64(len(cited)),
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(packetPath), "research-evidence.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}
