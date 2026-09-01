package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/researchpacket"
)

func TestResearchPacketRecordArchivesAndProjectsOnlyValidatedJSON(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	archive := filepath.Join(directory, "archive")
	if err := os.Mkdir(archive, 0o700); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(directory, "input.json")
	latest := filepath.Join(directory, "latest.json")
	packet := testResearchPacket(now)
	encoded, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--in", input, "--latest", latest, "--archive-dir", archive}
	var output bytes.Buffer
	if err := runResearchPacketRecord(args, &output, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(latest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := researchpacket.DecodeStored(stored)
	if err != nil || decoded.ContentSHA256 == "" {
		t.Fatalf("decoded = %+v, %v", decoded, err)
	}
	entries, err := os.ReadDir(archive)
	if err != nil || len(entries) != 1 {
		t.Fatalf("archive = %v, %v", entries, err)
	}
	if err := runResearchPacketRecord(args, &bytes.Buffer{}, func() time.Time { return now }); err != nil {
		t.Fatalf("idempotent record failed: %v", err)
	}
	entries, _ = os.ReadDir(archive)
	if len(entries) != 1 {
		t.Fatalf("duplicate archive entries = %d", len(entries))
	}

	packet.Disposition = researchpacket.DispositionBlocked
	packet.RiskVeto.Decision = researchpacket.VetoReject
	if err := os.WriteFile(input, mustJSON(t, packet), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runResearchPacketRecord(args, &bytes.Buffer{}, func() time.Time { return now }); err == nil {
		t.Fatal("a rejected packet carrying a parameter change was stored")
	}
}

func testResearchPacket(now time.Time) researchpacket.Packet {
	created := now.Add(-time.Minute)
	return researchpacket.Packet{
		Version: researchpacket.Version, HypothesisID: "sol-test-20260901",
		CreatedAt: created, ValidUntil: created.Add(6 * time.Hour),
		Market: "SOL/USDC", Disposition: researchpacket.DispositionCandidate,
		VerifiedFacts: []researchpacket.Fact{{
			ID: "fact_one", Claim: "Two current primary sources support the paper hypothesis.",
			Status: researchpacket.FactVerified,
			Sources: []researchpacket.Source{
				{URL: "https://solana.com/docs/core", RetrievedAt: created},
				{URL: "https://github.com/anza-xyz/agave/releases", RetrievedAt: created},
			},
		}},
		BullCase:          "The hypothesis may improve paper results.",
		BearCase:          "The observed edge may be noise.",
		NoTradeCase:       "Keep the current plan if evidence weakens.",
		ExecutionCostCase: "Include fees, impact, slippage, and settlement delay.",
		RiskVeto: researchpacket.RiskVeto{
			Decision: researchpacket.VetoPass, Reason: "The bounded paper test keeps all current limits.",
		},
		CandidateParameterDiff: []researchpacket.ParameterChange{{
			Name: "minimum_signal_bps", Current: 80, Proposed: 90,
		}},
		RejectionConditions: []string{"Reject if it loses to the champion out of sample."},
		OutOfSampleTest:     "Use chronological walk-forward and paired forward evidence.",
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
