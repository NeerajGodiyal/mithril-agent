package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/researchpacket"
)

func recordedResearchFixture(t *testing.T) (*shadowResearchController, shadowResearchCandidateInput, researchpacket.Packet, time.Time) {
	t.Helper()
	c, input, now := replayRejectionFixture(t)
	observations, err := buildResearchObservations(c.policy, c.journalDir, now)
	if err != nil {
		t.Fatal(err)
	}
	raw := *c.researchPacket
	raw.Version, raw.ContentSHA256, raw.VerifiedFacts = researchpacket.RecordedVersion, "", nil
	// This is a new recorded-basis run, not the web fixture's pre-midnight run.
	raw.CreatedAt, raw.ValidUntil = now, now.Add(6*time.Hour)
	raw.RecordedEvidence = &researchpacket.RecordedReference{ContentSHA256: observations.ContentSHA256,
		MetricIDs: []string{"observable_bps", "signals", "fills"}}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := researchpacket.ParseWithRecorded(encoded, &observations, now)
	if err != nil {
		t.Fatal(err)
	}
	c.researchPacket = &packet
	input.ResearchPacketSHA256 = packet.ContentSHA256
	return c, input, raw, now
}

func TestRecordedResearchRejectsPreMidnightRunWithNextDayBasis(t *testing.T) {
	c, _, raw, now := recordedResearchFixture(t)
	raw.CreatedAt = now.Add(-time.Minute)
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := researchpacket.ParseWithRecorded(encoded, c.researchPacket.RecordedObservations, now); err == nil {
		t.Fatal("pre-midnight run accepted observations from its own incomplete UTC day")
	}
}

func TestRecordedResearchMCPBindsActualJournalAndRetainsTypedFailure(t *testing.T) {
	c, input, _, now := recordedResearchFixture(t)
	before, err := os.ReadFile(c.championPointer)
	if err != nil {
		t.Fatal(err)
	}
	_, _, hypothesis, err := c.bindResearchPacket(input.ResearchPacketSHA256, now)
	if err != nil || hypothesis.Version != 2 || hypothesis.validate() != nil || len(hypothesis.Sources) != 0 {
		t.Fatalf("recorded hypothesis = %+v, %v", hypothesis, err)
	}
	if _, err := c.createCandidate(input, now); !errors.Is(err, errNoAdaptiveTrainingRoundTrip) {
		t.Fatalf("recorded candidate did not reach replay: %v", err)
	}
	if _, err := readShadowReplayRejection(c.replayRejection, now); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(c.championPointer)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("recorded rejection changed champion")
	}
	if _, err := os.Stat(c.challengerPointer); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recorded rejection changed challenger: %v", err)
	}
	// A different, still valid completed stream must not reuse the sealed basis.
	other := privateTestDirectory(t)
	writeShadowResearchWindow(t, other, c.policy, "2026-08-29", []uint64{101_000_000})
	if _, err := buildResearchObservations(c.policy, other, now); err != nil {
		t.Fatal(err)
	}
	c.journalDir = other
	if _, err := c.createCandidate(input, now); err == nil || errors.Is(err, errNoAdaptiveTrainingRoundTrip) {
		t.Fatalf("changed journal reached candidate replay: %v", err)
	}
}

func TestRecordedResearchMCPRejectsResealedMetricsAndChangedPolicy(t *testing.T) {
	c, input, raw, now := recordedResearchFixture(t)
	original := c.researchPacket
	for _, name := range []string{"resealed metrics", "current policy"} {
		t.Run(name, func(t *testing.T) {
			probe := *c
			probe.researchPacket = original
			request := input
			if name == "resealed metrics" {
				forged := *original.RecordedObservations
				forged.Metrics.VersusHoldMicros++
				forged, err := forged.Seal()
				if err != nil {
					t.Fatal(err)
				}
				model := raw
				model.RecordedEvidence = &researchpacket.RecordedReference{ContentSHA256: forged.ContentSHA256, MetricIDs: []string{"versus_hold_micros"}}
				encoded, err := json.Marshal(model)
				if err != nil {
					t.Fatal(err)
				}
				packet, err := researchpacket.ParseWithRecorded(encoded, &forged, now)
				if err != nil || packet.Validate() != nil {
					t.Fatalf("self-consistent forged packet fixture: %v", err)
				}
				probe.researchPacket = &packet
				request.ResearchPacketSHA256 = packet.ContentSHA256
			} else {
				probe.policy.SlippageBPS++
				probe.basePolicy = probe.policy
				if err := probe.policy.Validate(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := probe.createCandidate(request, now); err == nil || errors.Is(err, errNoAdaptiveTrainingRoundTrip) {
				t.Fatalf("unverified recorded basis reached replay: %v", err)
			}
			if _, err := os.Stat(probe.replayRejection); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unverified recorded basis wrote receipt: %v", err)
			}
		})
	}
}

func TestRecordedResearchPacketRecordRequiresHostContext(t *testing.T) {
	c, _, raw, now := recordedResearchFixture(t)
	root := privateTestDirectory(t)
	input, latest := filepath.Join(root, "raw.json"), filepath.Join(root, "latest.json")
	policy := writeShadowPolicy(t, c.policy)
	writeJSON(t, input, raw)
	args := []string{"--in", input, "--latest", latest, "--sol-policy", policy, "--sol-journal-dir", c.journalDir}
	archive := privateTestDirectory(t)
	if err := runResearchPacketRecord(append(append([]string(nil), args...), "--archive-dir", archive), io.Discard, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(latest)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := researchpacket.DecodeStored(before)
	if err != nil || stored.RecordedObservations == nil || stored.ContentSHA256 != c.researchPacket.ContentSHA256 {
		t.Fatalf("host packet = %+v, %v", stored, err)
	}
	entries, err := os.ReadDir(archive)
	if err != nil || len(entries) != 1 {
		t.Fatalf("host packet archive entries = %v, %v", entries, err)
	}
	archived, err := os.ReadFile(filepath.Join(archive, entries[0].Name()))
	if err != nil || !bytes.Equal(before, archived) {
		t.Fatal("immutable archive did not embed the exact host-sealed observations")
	}
	for _, name := range []string{"missing context", "wrong policy", "wrong journal", "embedded artifact"} {
		t.Run(name, func(t *testing.T) {
			attempt := append([]string(nil), args...)
			requested := raw
			switch name {
			case "missing context":
				attempt = attempt[:4]
			case "wrong policy":
				changed := c.policy
				changed.SlippageBPS++
				if err := changed.Validate(); err != nil {
					t.Fatal(err)
				}
				attempt[5] = writeShadowPolicy(t, changed)
			case "wrong journal":
				other := privateTestDirectory(t)
				writeShadowResearchWindow(t, other, c.policy, "2026-08-29", []uint64{101_000_000})
				if _, err := buildResearchObservations(c.policy, other, now); err != nil {
					t.Fatal(err)
				}
				attempt[7] = other
			case "embedded artifact":
				requested.RecordedObservations = c.researchPacket.RecordedObservations
			}
			writeJSON(t, input, requested)
			if err := runResearchPacketRecord(attempt, io.Discard, func() time.Time { return now }); err == nil {
				t.Fatal("unbound model input accepted")
			}
			after, err := os.ReadFile(latest)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected input overwrote host packet")
			}
		})
	}
}

func TestRecordedResearchProjectionPreservesPriorOnInvalidInput(t *testing.T) {
	c, _, raw, now := recordedResearchFixture(t)
	root := privateTestDirectory(t)
	input, latest := filepath.Join(root, "sealed.json"), filepath.Join(root, "projection.json")
	args := []string{"--in", input, "--latest", latest}
	writeJSON(t, input, c.researchPacket)
	if err := runResearchPacketProject(args, io.Discard, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(latest)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"raw", "tampered", "expired"} {
		t.Run(name, func(t *testing.T) {
			packet := *c.researchPacket
			readAt := now
			switch name {
			case "raw":
				packet = raw
			case "tampered":
				packet.BullCase += " altered"
			case "expired":
				readAt = now.Add(24 * time.Hour)
			}
			writeJSON(t, input, packet)
			if err := runResearchPacketProject(args, io.Discard, func() time.Time { return readAt }); err == nil {
				t.Fatal("unsealed or stale projection accepted")
			}
			after, err := os.ReadFile(latest)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("invalid projection overwrote prior packet")
			}
		})
	}
}

func TestRecordedResearchHypothesisCannotStandAlone(t *testing.T) {
	base := candidateTestPolicy()
	candidate := candidateForPrices(t, base, 220_000_000, 110_000_000)
	if err := candidate.validateAgainst(base); err != nil {
		t.Fatal(err)
	}
	candidate.Hypothesis = &shadowPaperHypothesis{Version: 2, Status: "paper_hypothesis", PaperOnly: true,
		Thesis: "A recorded observation is not independent admission evidence.", RecordedEvidenceSHA256: strings.Repeat("a", 64)}
	if err := candidate.Hypothesis.validate(); err != nil {
		t.Fatal(err)
	}
	if err := candidate.validateAgainst(base); err == nil || !strings.Contains(err.Error(), "recorded hypothesis lacks") {
		t.Fatalf("standalone recorded hypothesis accepted: %v", err)
	}
	legacy := validShadowResearchInput()
	legacy.Hypothesis = *candidate.Hypothesis
	if err := legacy.validate(time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("legacy model input accepted an unbound recorded hypothesis")
	}
}
