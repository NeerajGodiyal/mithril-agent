package agent

import (
	"testing"
	"time"
)

const (
	testSource      = "11111111111111111111111111111111"
	testDestination = "SysvarC1ock11111111111111111111111111111111"
)

func testProfile() Profile {
	return Profile{
		Name:                         ProfileTreasurySweepV1,
		Version:                      1,
		Cluster:                      "devnet",
		Source:                       testSource,
		Destination:                  testDestination,
		ReserveLamports:              100,
		MinTransferLamports:          10,
		MaxTransferLamports:          50,
		DailyCapLamports:             80,
		MaxFeeLamports:               5,
		ScheduleWindowSeconds:        3_600,
		ScheduleAnchorUnix:           time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC).Unix(),
		MaxClockUncertaintyMillis:    100,
		MaxObservationAgeSeconds:     30,
		MinHealthyObservationSeconds: 5,
		MinHealthySlotAdvance:        1,
		MaxNodeLagSlots:              150,
		MaxReconciliationSeconds:     180,
	}
}

func TestProposeAppliesEveryBound(t *testing.T) {
	now := time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC)
	profile := testProfile()
	obs := Observation{
		Cluster:         "devnet",
		Source:          testSource,
		BalanceLamports: 1000,
		Slot:            7,
		ObservedAt:      now,
	}
	proposal, reason, err := profile.Propose(obs, now, 35)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" || proposal.AmountLamports != 40 ||
		proposal.FeeBudgetLamports != 5 || proposal.ReservedLamports != 45 {
		t.Fatalf("proposal = %+v, reason = %q", proposal, reason)
	}
	if proposal.ActionID == "" || proposal.ReservationDayUTC != "2026-07-30" {
		t.Fatalf("proposal identity/day missing: %+v", proposal)
	}
	if proposal.ScheduleWindowStartUnix !=
		time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC).Unix() ||
		proposal.ScheduleWindowEndUnix !=
			time.Date(2026, 7, 30, 2, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("proposal schedule window = %+v", proposal)
	}
}

func TestProposeFailsClosed(t *testing.T) {
	now := time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC)
	base := Observation{
		Cluster:         "devnet",
		Source:          testSource,
		BalanceLamports: 200,
		Slot:            7,
		ObservedAt:      now,
	}
	tests := []struct {
		name    string
		change  func(*Profile, *Observation)
		reserve uint64
	}{
		{"stale", func(_ *Profile, obs *Observation) { obs.ObservedAt = now.Add(-31 * time.Second) }, 0},
		{"future", func(_ *Profile, obs *Observation) { obs.ObservedAt = now.Add(6 * time.Second) }, 0},
		{"wrong cluster", func(_ *Profile, obs *Observation) { obs.Cluster = "mainnet-beta" }, 0},
		{"wrong source", func(_ *Profile, obs *Observation) { obs.Source = testDestination }, 0},
		{"zero slot", func(_ *Profile, obs *Observation) { obs.Slot = 0 }, 0},
		{"cap corruption", func(_ *Profile, _ *Observation) {}, 81},
		{"same addresses", func(profile *Profile, _ *Observation) { profile.Destination = profile.Source }, 0},
		{"unsupported version", func(profile *Profile, _ *Observation) { profile.Version = 2 }, 0},
		{"missing clock uncertainty", func(profile *Profile, _ *Observation) {
			profile.MaxClockUncertaintyMillis = 0
		}, 0},
		{"excessive clock uncertainty", func(profile *Profile, _ *Observation) {
			profile.MaxClockUncertaintyMillis = 2_001
		}, 0},
		{"wrapping clock uncertainty", func(profile *Profile, _ *Observation) {
			// This becomes exactly 100ms if multiplied before it is bounded.
			profile.MaxClockUncertaintyMillis = 100 + 1<<58
		}, 0},
		{"missing lag bound", func(profile *Profile, _ *Observation) { profile.MaxNodeLagSlots = 0 }, 0},
		{"missing reconciliation bound", func(profile *Profile, _ *Observation) {
			profile.MaxReconciliationSeconds = 0
		}, 0},
		{"missing fee bound", func(profile *Profile, _ *Observation) { profile.MaxFeeLamports = 0 }, 0},
		{"missing schedule", func(profile *Profile, _ *Observation) {
			profile.ScheduleWindowSeconds = 0
		}, 0},
		{"unaligned schedule anchor", func(profile *Profile, _ *Observation) {
			profile.ScheduleAnchorUnix++
		}, 0},
		{"fee and transfer exceed cap", func(profile *Profile, _ *Observation) {
			profile.MaxFeeLamports = 31
		}, 0},
		{"reserve and fee overflow", func(profile *Profile, _ *Observation) {
			profile.ReserveLamports = ^uint64(0)
		}, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := testProfile()
			obs := base
			test.change(&profile, &obs)
			if _, _, err := profile.Propose(obs, now, test.reserve); err == nil {
				t.Fatal("expected failure")
			}
		})
	}
}

func TestActionIdentityIsOneDecisionPerScheduleWindow(t *testing.T) {
	profile := testProfile()
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	firstStart, _, err := profile.scheduleWindow(
		time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ComputeActionID(fingerprint, firstStart)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ComputeActionID(fingerprint, firstStart)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("the same policy and schedule window produced two action identities")
	}
	nextStart, _, err := profile.scheduleWindow(
		time.Date(2026, 7, 30, 2, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	third, err := ComputeActionID(fingerprint, nextStart)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("different schedule windows produced the same action identity")
	}
}

func TestProfileFingerprintBindsEveryPolicyField(t *testing.T) {
	profile := testProfile()
	first, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	changed := profile
	changed.MaxTransferLamports--
	second, err := changed.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 64 || first == second {
		t.Fatalf("profile fingerprints = %q, %q", first, second)
	}
}

func TestProposalReservationUsesDecisionDay(t *testing.T) {
	profile := testProfile()
	observedAt := time.Date(2026, 7, 30, 23, 59, 59, 0, time.UTC)
	now := observedAt.Add(2 * time.Second)
	observation := Observation{
		Cluster: profile.Cluster, Source: profile.Source, BalanceLamports: 1000,
		Slot: 7, ObservedAt: observedAt,
	}
	proposal, _, err := profile.Propose(observation, now, 0)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.ReservationDayUTC != "2026-07-31" {
		t.Fatalf("reservation day = %q", proposal.ReservationDayUTC)
	}
}
