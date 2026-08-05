package agent

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
)

func TestShadowRunIsIdempotentAndReservesCap(t *testing.T) {
	store, err := journal.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC)
	engine, err := NewEngine(store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	profile := testProfile()
	obs := Observation{
		Cluster:         profile.Cluster,
		Source:          profile.Source,
		BalanceLamports: 1000,
		Slot:            7,
		ObservedAt:      now,
	}
	first, err := engine.RunShadow(profile, obs)
	if err != nil {
		t.Fatal(err)
	}
	obs.Slot++
	obs.ObservedAt = now.Add(time.Second)
	second, err := engine.RunShadow(profile, obs)
	if err != nil {
		t.Fatal(err)
	}
	if first.Decision != "shadowed" || first.AmountLamports != 50 {
		t.Fatalf("first = %+v", first)
	}
	if !second.Recovered || second.ActionID != first.ActionID {
		t.Fatalf("second = %+v, first = %+v", second, first)
	}
	if got := len(store.Records()); got != 2 {
		t.Fatalf("record count = %d, want 2", got)
	}

	obs.Slot++
	now = now.Add(time.Hour)
	obs.ObservedAt = now
	third, err := engine.RunShadow(profile, obs)
	if err != nil {
		t.Fatal(err)
	}
	if third.AmountLamports != 20 {
		t.Fatalf("third = %+v, want remaining daily cap after fee 20", third)
	}
}

func TestShadowRunRecoversProposedAction(t *testing.T) {
	store, err := journal.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC)
	profile := testProfile()
	obs := Observation{
		Cluster:         profile.Cluster,
		Source:          profile.Source,
		BalanceLamports: 1000,
		Slot:            7,
		ObservedAt:      now,
	}
	proposal, _, err := profile.Propose(obs, now, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(now, EventActionShadowProposed, proposal.ActionID, proposal); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(store, func() time.Time { return now.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.RunShadow(profile, obs)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Recovered || result.Decision != "shadowed" || len(store.Records()) != 2 {
		t.Fatalf("result/records = %+v/%d", result, len(store.Records()))
	}
}

func TestShadowReservationsDoNotAuthorizeOrConsumeLiveActions(t *testing.T) {
	store, err := journal.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC)
	engine, err := NewEngine(store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	profile := testProfile()
	observation := Observation{
		Cluster: profile.Cluster, Source: profile.Source, BalanceLamports: 1000,
		Slot: 7, ObservedAt: now,
	}
	if _, err := engine.RunShadow(profile, observation); err != nil {
		t.Fatal(err)
	}
	live, err := engine.Propose(profile, observation)
	if err != nil {
		t.Fatal(err)
	}
	if live.Recovered || live.Proposal.AmountLamports != profile.MaxTransferLamports {
		t.Fatalf("shadow state affected live proposal: %+v", live)
	}
	if got := len(store.Records()); got != 3 {
		t.Fatalf("record count = %d, want 3", got)
	}
}
