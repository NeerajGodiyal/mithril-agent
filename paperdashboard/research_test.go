package paperdashboard

import (
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
