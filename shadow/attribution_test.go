package shadow

import (
	"reflect"
	"testing"
	"time"
)

func attributionTicks(t *testing.T, mode string) (Policy, []Tick) {
	t.Helper()
	p := adaptiveTestPolicy()
	p.Adaptive.MaxDrawdownBPS = 5000
	if mode == "fixed" {
		p.Adaptive = nil
		p.Trigger.ThresholdMicros = 98_000_000
	}
	primary := &stubSource{identity: p.Trigger.PrimarySourceSHA256}
	secondary := &stubSource{identity: p.Trigger.SecondarySourceSHA256}
	quotes := 0
	quoter := &stubQuoter{quote: func(sell bool, amount uint64) Quote {
		quotes++
		out := amount / 10
		if !sell {
			out = amount * 10
		}
		if mode == "refused" && quotes > 1 {
			out = out * 99 / 100
		}
		return Quote{InputAmount: amount, EstimatedOutput: out, MinimumOutput: out * 996 / 1000}
	}}
	recorder := &stubRecorder{}
	runner, err := NewRunner(p, primary, secondary, quoter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1700000000, 0).UTC()
	for i, price := range []uint64{100_000_000, 99_000_000, 98_000_000, 97_000_000} {
		at := start.Add(time.Duration(i) * time.Minute)
		primary.price, secondary.price, primary.at, secondary.at = price, price, at, at
		if _, err := runner.Step(t.Context(), at); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "pending" {
		return p, recorder.ticks
	}
	if mode == "missed" {
		if err := runner.ClosePeriod(start.Add(3*time.Minute+time.Second), 97_000_000); err != nil {
			t.Fatal(err)
		}
		return p, recorder.ticks
	}
	if mode == "deferred" {
		at := start.Add(3*time.Minute + time.Second)
		primary.price, secondary.price, primary.at, secondary.at = 96_000_000, 96_000_000, at, at
		tick, err := runner.Step(t.Context(), at)
		if err != nil || !tick.Deferred || tick.DecisionQuote != nil {
			t.Fatalf("fixture did not defer a new signal behind the original: %+v, %v", tick, err)
		}
	}
	// A gap resets the current decision to warmup while the original pending
	// execution still settles. Its origin must not become the warmup decision.
	at := start.Add(3*time.Minute + 122*time.Second)
	primary.at, secondary.at = at, at
	if _, err := runner.Step(t.Context(), at); err != nil {
		t.Fatal(err)
	}
	return p, recorder.ticks
}

func TestReplayAttributionUsesOriginAndPreservesAccounting(t *testing.T) {
	for _, mode := range []string{"filled", "refused", "fixed", "pending", "missed", "deferred"} {
		t.Run(mode, func(t *testing.T) {
			p, ticks := attributionTicks(t, mode)
			ordinary, err := Replay(p, ticks)
			if err != nil {
				t.Fatal(err)
			}
			got, a, err := ReplayWithAttribution(p, ticks)
			if err != nil || !reflect.DeepEqual(got, ordinary) {
				t.Fatalf("replay parity: %v", err)
			}
			_, again, err := ReplayWithAttribution(p, ticks)
			if err != nil || !reflect.DeepEqual(a, again) {
				t.Fatal("restart changed attribution")
			}
			if mode == "pending" || mode == "missed" {
				if len(a.Settlements) != 0 || (mode == "pending" && a.PendingOrigins != 1) || (mode == "missed" && a.MissedOrigins != 1) {
					t.Fatalf("unsettled origins: %+v", a)
				}
				return
			}
			if len(a.Settlements) != 1 || a.UnmatchedSettlements != 0 {
				t.Fatalf("settlements: %+v", a)
			}
			row := a.Settlements[0]
			if row.RealizedMicros != got.Ledger.RealizedMicros || row.FeesMicros != got.Ledger.FeesMicros || row.FeesMicros <= 0 {
				t.Fatal("accounting failed reconciliation")
			}
			if mode == "fixed" {
				if a.UnknownOrigins != 1 || row.OriginKnown || row.Strategy != "unknown" {
					t.Fatal("fixed origin invented")
				}
			} else {
				origin := ticks[3]
				last := ticks[len(ticks)-1]
				if origin.Decision.Strategy == last.Decision.Strategy {
					t.Fatal("fixture did not change settlement strategy")
				}
				if row.OriginAt != origin.At || row.Strategy != origin.Decision.Strategy || row.Regime != origin.Decision.Regime || !row.OriginKnown {
					t.Fatal("settlement decision replaced origin")
				}
				if row.Filled != (mode != "refused") {
					t.Fatal("refusal lost")
				}
			}
			bad := append([]Tick(nil), ticks...)
			for i := range bad {
				if bad[i].Fill != nil {
					f := *bad[i].Fill
					f.ReceivedUnits++
					bad[i].Fill = &f
					break
				}
			}
			_, partial, err := ReplayWithAttribution(p, bad)
			if err == nil || !reflect.DeepEqual(partial, ReplayAttribution{}) {
				t.Fatal("invalid evidence returned attribution")
			}
		})
	}
}
