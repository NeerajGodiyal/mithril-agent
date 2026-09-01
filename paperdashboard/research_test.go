package paperdashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
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
		VerifiedFacts: []researchpacket.Fact{{
			ID: "route_quality", Claim: "The route evidence is current.",
			Status: researchpacket.FactVerified,
			Sources: []researchpacket.Source{
				{URL: "https://solana.com/docs/core", RetrievedAt: now.Add(-time.Minute)},
				{URL: "https://github.com/anza-xyz/agave/releases", RetrievedAt: now.Add(-time.Minute)},
			},
		}},
		BullCase: "The paper hypothesis may improve range entries.",
		BearCase: "The move may be noise.", NoTradeCase: "Keep the current plan.",
		ExecutionCostCase: "Replay fees and route impact.",
		RiskVeto: researchpacket.RiskVeto{
			Decision: researchpacket.VetoPass, Reason: "The paper-only change remains bounded.",
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
		view.Research.VerifiedFacts != 1 || view.Research.Sources != 2 ||
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
