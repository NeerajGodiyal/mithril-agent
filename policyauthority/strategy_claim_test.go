package policyauthority

import (
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestStrategyClaimRequiresOriginalDecisionAndQuoteRecency(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	seed := strategySeed{Policy: shadow.Policy{Adaptive: &shadow.AdaptivePolicy{MaxObservationGapSeconds: 60}}}
	decision := strategyDecision{ObservationAt: now.Add(-time.Minute), AcquiredAt: now.Add(-30 * time.Second)}
	committed := now.Add(-20 * time.Second)
	if err := checkStrategyClaimTime(seed, decision, committed, now, time.Minute); err != nil {
		t.Fatalf("exact original age boundary: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*strategyDecision, *time.Time, *time.Time, *time.Duration)
	}{
		{"expired observation", func(d *strategyDecision, _, _ *time.Time, _ *time.Duration) {
			d.ObservationAt = d.ObservationAt.Add(-time.Nanosecond)
		}},
		{"future quote", func(d *strategyDecision, _, n *time.Time, _ *time.Duration) { d.AcquiredAt = n.Add(time.Nanosecond) }},
		{"future commit", func(_ *strategyDecision, c, n *time.Time, _ *time.Duration) { *c = n.Add(time.Nanosecond) }},
		{"latency crosses expiry", func(_ *strategyDecision, _, n *time.Time, _ *time.Duration) { *n = n.Add(time.Nanosecond) }},
		{"renewed observation bound", func(_ *strategyDecision, _, _ *time.Time, age *time.Duration) { *age = time.Minute + time.Nanosecond }},
		{"zero bound", func(_ *strategyDecision, _, _ *time.Time, age *time.Duration) { *age = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, c, n, age := decision, committed, now, time.Minute
			test.change(&d, &c, &n, &age)
			if err := checkStrategyClaimTime(seed, d, c, n, age); err == nil {
				t.Fatal("invalid continuation chronology accepted")
			}
		})
	}
}

func TestStrategyClaimIntentBindsDecisionWalletReceiptAndAction(t *testing.T) {
	inputs := [4]string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)}
	original, err := strategyClaimIntent(inputs[0], inputs[1], inputs[2], inputs[3], time.Minute, 1_000_000)
	if err != nil || !validHexDigest(original.SHA256) {
		t.Fatalf("invalid continuation intent: %+v, %v", original, err)
	}
	for index := range inputs {
		changed := inputs
		changed[index] = strings.Repeat("e", 64)
		other, err := strategyClaimIntent(changed[0], changed[1], changed[2], changed[3], time.Minute, 1_000_000)
		if err != nil || other.SHA256 == original.SHA256 {
			t.Fatalf("intent did not bind input %d: %v", index, err)
		}
	}
	for _, bounds := range []struct {
		age     time.Duration
		reserve uint64
	}{{time.Second, 1_000_000}, {time.Minute, 999_999}} {
		other, err := strategyClaimIntent(inputs[0], inputs[1], inputs[2], inputs[3], bounds.age, bounds.reserve)
		if err != nil || other.SHA256 == original.SHA256 {
			t.Fatal("intent did not bind review limits", err)
		}
	}
}
