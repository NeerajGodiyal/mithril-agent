package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/bits"
	"time"

	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const researchEventWakeUsage = `Usage: mithril-agent research event-wake-experiment --policy PATH --journal-dir DIR
  [--window 5m --move-bps 100 --cooldown 30m --max-gap 2m --simulated-busy 3m --max-calls 48]
Offline previous-day authenticated-price experiment only. A fixed hourly baseline
and event requests share one simulated worker and total call budget. Busy requests
are coalesced without queueing stale evidence. No service or model is invoked.
Simulated duration does not establish production latency, research quality or profit.`

type researchEventWakeConfig struct {
	Window, Cooldown, MaxGap, Busy time.Duration
	MoveBPS, MaxCalls              uint64
}

type researchEventWakeResult struct {
	Kind                 string                  `json:"kind"`
	Experimental         bool                    `json:"experimental"`
	RuntimeEnabled       bool                    `json:"runtime_enabled"`
	PaperOnly            bool                    `json:"paper_only"`
	AdvisoryOnly         bool                    `json:"advisory_only"`
	Authorized           bool                    `json:"authorized"`
	ScheduleBasis        string                  `json:"schedule_basis"`
	BaselineOnly         researchEventBaseline   `json:"baseline_only"`
	Market               string                  `json:"market"`
	PolicySHA256         string                  `json:"policy_sha256"`
	Journal              shadowJournalProvenance `json:"journal"`
	CoverageBPS          int32                   `json:"coverage_bps"`
	CoverageSufficient   bool                    `json:"coverage_sufficient"`
	WindowSeconds        int64                   `json:"window_seconds"`
	CooldownSeconds      int64                   `json:"cooldown_seconds"`
	MaxGapSeconds        int64                   `json:"max_gap_seconds"`
	SimulatedBusySeconds int64                   `json:"simulated_busy_seconds"`
	MoveBPS              uint64                  `json:"move_bps"`
	MaxCalls             uint64                  `json:"max_calls"`
	BaselineRequests     uint64                  `json:"combined_baseline_requests"`
	EventTriggers        uint64                  `json:"event_triggers"`
	Calls                uint64                  `json:"combined_calls"`
	BaselineCalls        uint64                  `json:"combined_baseline_calls"`
	EventCalls           uint64                  `json:"combined_event_calls"`
	Coalesced            uint64                  `json:"coalesced"`
	CooldownSuppressed   uint64                  `json:"cooldown_suppressed"`
	BudgetSuppressed     uint64                  `json:"budget_suppressed"`
	Gaps                 uint64                  `json:"gaps"`
	Duplicates           uint64                  `json:"duplicates"`
}

type researchEventBaseline struct {
	Requests         uint64 `json:"requests"`
	Calls            uint64 `json:"calls"`
	BudgetSuppressed uint64 `json:"budget_suppressed"`
}

func runResearchEventWake(args []string, output io.Writer, now func() time.Time) error {
	f := flag.NewFlagSet("research event-wake-experiment", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	policyPath := f.String("policy", "", "exact private policy")
	directory := f.String("journal-dir", "", "private authenticated journals")
	c := researchEventWakeConfig{}
	f.DurationVar(&c.Window, "window", 5*time.Minute, "comparison window")
	f.DurationVar(&c.Cooldown, "cooldown", 30*time.Minute, "event request cooldown")
	f.DurationVar(&c.MaxGap, "max-gap", 2*time.Minute, "maximum observation gap")
	f.DurationVar(&c.Busy, "simulated-busy", 3*time.Minute, "assumed worker duration")
	f.Uint64Var(&c.MoveBPS, "move-bps", 100, "absolute endpoint move")
	f.Uint64Var(&c.MaxCalls, "max-calls", 48, "combined baseline and event budget")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, researchEventWakeUsage)
		}
		return err
	}
	if f.NArg() != 0 || !cleanResearchPath(*policyPath) || !cleanResearchPath(*directory) || now == nil {
		return errors.New("event experiment requires private policy and journal paths")
	}
	policy, err := loadActiveShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	day, err := readResearchDay(policy, *directory, now().UTC())
	if err != nil {
		return err
	}
	result, err := simulateResearchEventWake(day.ticks, day.report.From, c)
	if err != nil {
		return err
	}
	result.Market = shadowMarketPair(policy)
	result.PolicySHA256, err = policy.Fingerprint()
	if err != nil {
		return err
	}
	result.Journal = day.provenance
	result.CoverageBPS = day.report.ObservableBPS
	result.CoverageSufficient = result.CoverageBPS >= 9500
	return json.NewEncoder(output).Encode(result)
}

func simulateResearchEventWake(ticks []shadow.Tick, start time.Time, c researchEventWakeConfig) (researchEventWakeResult, error) {
	r := researchEventWakeResult{Kind: "offline_research_event_wake", Experimental: true, PaperOnly: true, AdvisoryOnly: true,
		ScheduleBasis: "synthetic_UTC_hour_boundaries_not_deployed_timer_jitter",
		WindowSeconds: int64(c.Window / time.Second), CooldownSeconds: int64(c.Cooldown / time.Second), MaxGapSeconds: int64(c.MaxGap / time.Second), SimulatedBusySeconds: int64(c.Busy / time.Second), MoveBPS: c.MoveBPS, MaxCalls: c.MaxCalls}
	if start.IsZero() || start.Year() < 2000 || start.Year() > 2100 || !start.Equal(start.UTC().Truncate(24*time.Hour)) || c.Window < time.Second || c.Window > time.Hour || c.MaxGap < time.Second || c.MaxGap > c.Window || c.Cooldown < time.Second || c.Cooldown > 24*time.Hour || c.Busy < time.Second || c.Busy > time.Hour || c.MoveBPS == 0 || c.MoveBPS > 10000 || c.MaxCalls == 0 || c.MaxCalls > 1000 {
		return r, errors.New("invalid bounded event experiment configuration")
	}
	for _, duration := range []time.Duration{c.Window, c.Cooldown, c.MaxGap, c.Busy} {
		if duration%time.Second != 0 {
			return r, errors.New("event experiment durations must be whole seconds")
		}
	}
	end := start.Add(24 * time.Hour)
	// Busy is at most one hour, so the isolated hourly lane never overlaps itself.
	r.BaselineOnly = researchEventBaseline{Requests: 24, Calls: min(uint64(24), c.MaxCalls)}
	r.BaselineOnly.BudgetSuppressed = 24 - r.BaselineOnly.Calls
	nextBaseline := start
	var busyUntil, lastEvent, lastAt time.Time
	var history []shadow.Tick
	request := func(at time.Time, baseline bool) {
		if baseline {
			r.BaselineRequests++
		} else {
			r.EventTriggers++
		}
		if !baseline && !lastEvent.IsZero() && at.Sub(lastEvent) < c.Cooldown {
			r.CooldownSuppressed++
			return
		}
		if !baseline {
			lastEvent = at
		}
		if at.Before(busyUntil) {
			r.Coalesced++
			return
		}
		if r.Calls >= c.MaxCalls {
			r.BudgetSuppressed++
			return
		}
		r.Calls++
		if baseline {
			r.BaselineCalls++
		} else {
			r.EventCalls++
		}
		busyUntil = at.Add(c.Busy)
	}
	for _, tick := range ticks {
		if tick.At.Before(start) || tick.At.After(end) || (tick.At.Equal(end) && !tick.PeriodClose) || (!lastAt.IsZero() && tick.At.Before(lastAt)) {
			return r, errors.New("event experiment observation chronology invalid")
		}
		if tick.At.Equal(lastAt) {
			r.Duplicates++
			continue
		}
		for !nextBaseline.After(tick.At) && nextBaseline.Before(end) {
			request(nextBaseline, true)
			nextBaseline = nextBaseline.Add(time.Hour)
		}
		if tick.PeriodClose {
			history = nil
			lastAt = tick.At
			continue
		}
		if tick.Event == shadow.EventUnobservable || tick.PriceMicros == 0 {
			history = nil
			lastAt = tick.At
			r.Gaps++
			continue
		}
		if !lastAt.IsZero() && tick.At.Sub(lastAt) > c.MaxGap {
			history = nil
			r.Gaps++
		}
		lastAt = tick.At
		cutoff := tick.At.Add(-c.Window)
		for len(history) > 1 && !history[1].At.After(cutoff) {
			history = history[1:]
		}
		if len(history) > 0 && !history[0].At.After(cutoff) {
			old := history[0].PriceMicros
			diff := tick.PriceMicros - old
			if tick.PriceMicros < old {
				diff = old - tick.PriceMicros
			}
			hi, lo := bits.Mul64(diff, 10000)
			thresholdHi, thresholdLo := bits.Mul64(old, c.MoveBPS)
			if hi > thresholdHi || (hi == thresholdHi && lo >= thresholdLo) {
				request(tick.At, false)
			}
		}
		history = append(history, tick)
	}
	for nextBaseline.Before(end) {
		request(nextBaseline, true)
		nextBaseline = nextBaseline.Add(time.Hour)
	}
	return r, nil
}
