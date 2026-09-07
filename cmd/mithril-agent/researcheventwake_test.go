package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func eventWakeConfig() researchEventWakeConfig {
	return researchEventWakeConfig{Window: time.Minute, Cooldown: 10 * time.Minute, MaxGap: time.Minute, Busy: time.Second, MoveBPS: 100, MaxCalls: 48}
}

func TestResearchEventWakeCombinedBudgetAndDeterminism(t *testing.T) {
	start := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	ticks := []shadow.Tick{{At: start, PriceMicros: 10000}, {At: start.Add(time.Minute), PriceMicros: 10200}, {At: start.Add(2 * time.Minute), PriceMicros: 10400}}
	before := append([]shadow.Tick(nil), ticks...)
	c := eventWakeConfig()
	got, err := simulateResearchEventWake(ticks, start, c)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaselineRequests != 24 || got.BaselineCalls != 24 || got.EventTriggers != 2 || got.EventCalls != 1 || got.Calls != 25 || got.CooldownSuppressed != 1 || got.RuntimeEnabled {
		t.Fatalf("unexpected scheduling: %+v", got)
	}
	again, err := simulateResearchEventWake(ticks, start, c)
	if err != nil || !reflect.DeepEqual(got, again) || !reflect.DeepEqual(before, ticks) {
		t.Fatal("simulation changed inputs or was nondeterministic")
	}
	c.MaxCalls = 2
	limited, err := simulateResearchEventWake(ticks, start, c)
	if err != nil {
		t.Fatal(err)
	}
	if limited.Calls != 2 || limited.BaselineCalls != 1 || limited.EventCalls != 1 || limited.BudgetSuppressed != 23 {
		t.Fatalf("baseline not included in budget: %+v", limited)
	}
	if limited.BaselineOnly.Calls != 2 || limited.BaselineOnly.BudgetSuppressed != 22 {
		t.Fatal("standalone baseline did not have its own equal budget")
	}
}

func TestResearchEventWakeEventCanCoalesceLaterBaseline(t *testing.T) {
	start := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	c := eventWakeConfig()
	c.Busy = 3 * time.Minute
	ticks := []shadow.Tick{{At: start.Add(58 * time.Minute), PriceMicros: 10000}, {At: start.Add(59 * time.Minute), PriceMicros: 10200}, {At: start.Add(24 * time.Hour), PeriodClose: true}}
	got, err := simulateResearchEventWake(ticks, start, c)
	if err != nil || got.BaselineOnly.Calls != 24 || got.BaselineCalls != 23 || got.EventCalls != 1 || got.Coalesced != 1 {
		t.Fatalf("unequal schedule comparison: %+v %v", got, err)
	}
}

func TestResearchEventWakeBusyDuplicateGapAndFlat(t *testing.T) {
	start := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	c := eventWakeConfig()
	c.Busy = 3 * time.Minute
	ticks := []shadow.Tick{{At: start, PriceMicros: 10000}, {At: start.Add(time.Minute), PriceMicros: 10200}}
	got, err := simulateResearchEventWake(ticks, start, c)
	if err != nil {
		t.Fatal(err)
	}
	if got.Coalesced != 1 || got.EventCalls != 0 || got.Calls != 24 {
		t.Fatalf("busy event was queued: %+v", got)
	}
	ticks = append(ticks, ticks[1])
	duplicate, err := simulateResearchEventWake(ticks, start, c)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Duplicates != 1 || duplicate.EventTriggers != got.EventTriggers {
		t.Fatal("duplicate became new evidence")
	}
	for _, unobservable := range []bool{false, true} {
		ticks = []shadow.Tick{{At: start, PriceMicros: 10000}, {At: start.Add(3 * time.Minute), PriceMicros: 20000}}
		if unobservable {
			ticks = []shadow.Tick{{At: start, PriceMicros: 10000}, {At: start.Add(30 * time.Second), Event: shadow.EventUnobservable}, {At: start.Add(time.Minute), PriceMicros: 20000}}
		}
		gap, err := simulateResearchEventWake(ticks, start, c)
		if err != nil || gap.EventTriggers != 0 || gap.Gaps == 0 {
			t.Fatalf("gap did not reset warmup: %+v %v", gap, err)
		}
	}
	ticks = []shadow.Tick{{At: start, PriceMicros: 10000}, {At: start.Add(time.Minute), PriceMicros: 10000}}
	flat, err := simulateResearchEventWake(ticks, start, c)
	if err != nil || flat.EventTriggers != 0 || flat.Calls != 24 {
		t.Fatalf("flat tape triggered: %+v %v", flat, err)
	}
}

func TestResearchEventWakeRejectsClockAndBoundsAndAvoidsOverflow(t *testing.T) {
	start := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	c := eventWakeConfig()
	for _, ticks := range [][]shadow.Tick{
		{{At: start.Add(time.Minute), PriceMicros: 1}, {At: start, PriceMicros: 1}},
		{{At: start.Add(-time.Second), PriceMicros: 1}},
		{{At: start.Add(24 * time.Hour), PriceMicros: 1}},
	} {
		if _, err := simulateResearchEventWake(ticks, start, c); err == nil {
			t.Fatal("invalid chronology accepted")
		}
	}
	bad := c
	bad.Busy = -time.Second
	if _, err := simulateResearchEventWake(nil, start, bad); err == nil {
		t.Fatal("negative busy accepted")
	}
	bad = c
	bad.MaxCalls = 0
	if _, err := simulateResearchEventWake(nil, start, bad); err == nil {
		t.Fatal("zero budget accepted")
	}
	for _, field := range []string{"window", "cooldown", "gap", "busy"} {
		bad = c
		switch field {
		case "window":
			bad.Window += time.Millisecond
		case "cooldown":
			bad.Cooldown += time.Millisecond
		case "gap":
			bad.MaxGap -= time.Millisecond
		case "busy":
			bad.Busy += time.Millisecond
		}
		if _, err := simulateResearchEventWake(nil, start, bad); err == nil {
			t.Fatalf("fractional %s accepted", field)
		}
	}
	ticks := []shadow.Tick{{At: start, PriceMicros: ^uint64(0)}, {At: start.Add(time.Minute), PriceMicros: 1}}
	got, err := simulateResearchEventWake(ticks, start, c)
	if err != nil || got.EventTriggers != 1 {
		t.Fatalf("overflowed move comparison: %+v %v", got, err)
	}
}
