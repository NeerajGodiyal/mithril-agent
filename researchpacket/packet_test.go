package researchpacket

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseBindsCurrentTwoSourceCandidate(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	input := candidatePacket(now)
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := Parse(encoded, now)
	if err != nil {
		t.Fatal(err)
	}
	status := packet.StatusAt(now)
	if packet.ContentSHA256 == "" || packet.Validate() != nil || !status.Current ||
		!status.Actionable || status.VerifiedFacts != 1 || status.Sources != 2 {
		t.Fatalf("packet = %+v; status = %+v", packet, status)
	}
	stored, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeStored(stored)
	if err != nil || decoded.ContentSHA256 != packet.ContentSHA256 {
		t.Fatalf("decoded = %+v, %v", decoded, err)
	}
	tampered := packet
	tampered.BullCase = "Changed after validation."
	if tampered.Validate() == nil {
		t.Fatal("tampered packet retained a valid digest")
	}
}

func TestCandidateNeedsCurrentIndependentSources(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*Packet){
		"one source": func(packet *Packet) {
			packet.VerifiedFacts[0].Sources = packet.VerifiedFacts[0].Sources[:1]
		},
		"same owner": func(packet *Packet) {
			packet.VerifiedFacts[0].Sources[1].URL = "https://status.solana.com/history"
		},
		"stale retrieval": func(packet *Packet) {
			packet.VerifiedFacts[0].Sources[1].RetrievedAt = now.Add(-13 * time.Hour)
		},
		"private URL": func(packet *Packet) {
			packet.VerifiedFacts[0].Sources[1].URL = "https://127.0.0.1/report"
		},
		"unreviewed domain": func(packet *Packet) {
			packet.VerifiedFacts[0].Sources[1].URL = "https://example.com/report"
		},
		"unreviewed GitHub repository": func(packet *Packet) {
			packet.VerifiedFacts[0].Sources[1].URL = "https://github.com/example/agave/releases"
		},
		"unreviewed status provider": func(packet *Packet) {
			packet.VerifiedFacts[0].Sources[1].URL = "https://example.statuspage.io/report"
		},
		"already expired": func(packet *Packet) {
			packet.ValidUntil = now
		},
	} {
		t.Run(name, func(t *testing.T) {
			packet := candidatePacket(now)
			mutate(&packet)
			encoded, err := json.Marshal(packet)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Parse(encoded, now); err == nil {
				t.Fatal("invalid source quorum was accepted")
			}
		})
	}
}

func TestRejectedPacketCannotCarryParameterChanges(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	packet := candidatePacket(now)
	packet.Disposition = DispositionBlocked
	packet.RiskVeto = RiskVeto{Decision: VetoReject, Reason: "Independent risk review rejected the idea."}
	packet.CandidateParameterDiff = nil
	encoded, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := Parse(encoded, now)
	if err != nil || stored.StatusAt(now).Actionable {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	packet.CandidateParameterDiff = []ParameterChange{{
		Name: "minimum_signal_bps", Current: 80, Proposed: 90,
	}}
	encoded, _ = json.Marshal(packet)
	if _, err := Parse(encoded, now); err == nil || !strings.Contains(err.Error(), "cannot change") {
		t.Fatalf("rejected change error = %v", err)
	}
}

func TestPacketRejectsAParameterTheDeterministicSearchCannotApply(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	packet := candidatePacket(now)
	packet.CandidateParameterDiff[0] = ParameterChange{
		Name: "max_drawdown_bps", Current: 300, Proposed: 400,
	}
	encoded, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(encoded, now); err == nil {
		t.Fatal("packet accepted a parameter the candidate search cannot apply")
	}
}

func TestPacketAcceptsSourcesRetrievedDuringABoundedResearchRun(t *testing.T) {
	started := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	packet := candidatePacket(started)
	packet.CreatedAt = started
	packet.ValidUntil = started.Add(6 * time.Hour)
	for factIndex := range packet.VerifiedFacts {
		for sourceIndex := range packet.VerifiedFacts[factIndex].Sources {
			packet.VerifiedFacts[factIndex].Sources[sourceIndex].RetrievedAt = started.Add(10 * time.Minute)
		}
	}
	encoded, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(encoded, started.Add(11*time.Minute)); err != nil {
		t.Fatalf("current source retrieved during the research run was rejected: %v", err)
	}
	if _, err := Parse(encoded, started.Add(7*time.Minute)); err == nil {
		t.Fatal("packet accepted a source timestamp in the future")
	}
}

func candidatePacket(now time.Time) Packet {
	created := now.Add(-time.Minute)
	return Packet{
		Version: Version, HypothesisID: "jto-range-20260901",
		CreatedAt: created, ValidUntil: created.Add(6 * time.Hour),
		Market: "JTO/USDC", Disposition: DispositionCandidate,
		VerifiedFacts: []Fact{{
			ID: "route_quality", Claim: "The reviewed route and independent market feed are current.",
			Status: FactVerified,
			Sources: []Source{
				{URL: "https://solana.com/docs/core", RetrievedAt: created},
				{URL: "https://github.com/anza-xyz/agave/releases", RetrievedAt: created},
			},
		}},
		BullCase:          "The bounded paper hypothesis may improve range entries.",
		BearCase:          "The observed move may be noise and disappear after costs.",
		NoTradeCase:       "Keep the current paper policy if the route or sources weaken.",
		ExecutionCostCase: "Replay all fees, slippage, impact, and delayed settlement.",
		RiskVeto:          RiskVeto{Decision: VetoPass, Reason: "Paper-only change stays inside existing limits."},
		CandidateParameterDiff: []ParameterChange{{
			Name: "minimum_signal_bps", Current: 80, Proposed: 90,
		}},
		RejectionConditions: []string{"Reject if forward evidence loses to the current champion."},
		OutOfSampleTest:     "Run chronological walk-forward and a separate paired forward challenge.",
	}
}
