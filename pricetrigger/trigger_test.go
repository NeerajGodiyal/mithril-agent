package pricetrigger

import (
	"strings"
	"testing"
	"time"
)

func TestEvaluateUsesConservativePrice(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	policy := testPolicy()
	policy.ThresholdMicros = 149_900_000
	primary := testSample(policy.PrimarySourceSHA256, 150_100_000, now)
	secondary := testSample(policy.SecondarySourceSHA256, 150_000_000, now.Add(-time.Second))

	evidence, err := Evaluate(policy, primary, secondary, now)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Triggered || evidence.ConservativePrice != 149_900_000 {
		t.Fatalf("sell evidence = %+v", evidence)
	}

	policy.Direction = BuyAtOrBelow
	policy.ThresholdMicros = 150_200_000
	evidence, err = Evaluate(policy, primary, secondary, now)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Triggered || evidence.ConservativePrice != 150_200_000 {
		t.Fatalf("buy evidence = %+v", evidence)
	}
}

func TestEvaluateRequiresConfidenceAdjustedThreshold(t *testing.T) {
	now := time.Now().UTC()
	policy := testPolicy()
	primary := testSample(policy.PrimarySourceSHA256, policy.ThresholdMicros, now)
	secondary := testSample(policy.SecondarySourceSHA256, policy.ThresholdMicros, now)

	evidence, err := Evaluate(policy, primary, secondary, now)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Triggered || evidence.ConservativePrice != policy.ThresholdMicros-100_000 {
		t.Fatalf("confidence-adjusted evidence = %+v", evidence)
	}
}

func TestEvaluateWaitsWhenOnlyOneSourceCrosses(t *testing.T) {
	now := time.Now().UTC()
	policy := testPolicy()
	primary := testSample(policy.PrimarySourceSHA256, 150_100_000, now)
	secondary := testSample(policy.SecondarySourceSHA256, 149_900_000, now)
	evidence, err := Evaluate(policy, primary, secondary, now)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Triggered {
		t.Fatal("one-source threshold crossing triggered a sale")
	}
}

func TestStatusProjectionIsBoundedAndValidated(t *testing.T) {
	now := time.Now().UTC()
	policy := testPolicy()
	evidence, err := Evaluate(
		policy,
		testSample(policy.PrimarySourceSHA256, 150_200_000, now),
		testSample(policy.SecondarySourceSHA256, 150_100_000, now),
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	status, err := Project(policy, evidence)
	if err != nil || ValidateStatus(status) != nil || !status.Available || !status.ConditionMet {
		t.Fatalf("status = %+v, project error = %v", status, err)
	}
	if status := Unavailable(policy); ValidateStatus(status) != nil || status.Available {
		t.Fatalf("unavailable status = %+v", status)
	}
}

func TestValidateStatusRejectsInconsistentDecision(t *testing.T) {
	now := time.Now().UTC()
	status := Status{
		Feed: FeedSOLUSD, Direction: SellAtOrAbove, ThresholdMicros: 150_000_000,
		Available: true, ConservativePrice: 149_000_000, ConditionMet: true,
		ObservedAt: now, PrimaryPublishedAt: now, SecondaryPublishedAt: now,
	}
	if err := ValidateStatus(status); err == nil {
		t.Fatal("inconsistent price status was accepted")
	}
}

func TestValidateStatusAcceptsConservativeBuyExecutablePrice(t *testing.T) {
	now := time.Now().UTC()
	status := Status{
		Feed: FeedSOLUSD, Direction: BuyAtOrBelow, ThresholdMicros: 150_000_000,
		Available: true, ConservativePrice: 149_900_000, ConditionMet: true,
		ExecutableMinimum: 149_950_000, ExecutableCondition: true,
		ObservedAt: now, PrimaryPublishedAt: now, SecondaryPublishedAt: now,
	}
	if err := ValidateStatus(status); err != nil {
		t.Fatal(err)
	}
	status.ExecutableMinimum = 150_000_001
	if err := ValidateStatus(status); err == nil {
		t.Fatal("buy executable price above the threshold was accepted")
	}
}

func TestEvaluateRejectsUntrustedOrWeakEvidence(t *testing.T) {
	now := time.Now().UTC()
	for name, mutate := range map[string]func(*Policy, *Sample, *Sample){
		"same source": func(policy *Policy, _ *Sample, _ *Sample) {
			policy.SecondarySourceSHA256 = policy.PrimarySourceSHA256
		},
		"wrong source": func(_ *Policy, _ *Sample, secondary *Sample) {
			secondary.SourceSHA256 = strings.Repeat("c", 64)
		},
		"stale": func(policy *Policy, primary *Sample, _ *Sample) {
			primary.PublishedAt = now.Add(-time.Duration(policy.MaxAgeSeconds+1) * time.Second)
		},
		"future": func(_ *Policy, primary *Sample, _ *Sample) {
			primary.PublishedAt = now.Add(2 * time.Second)
		},
		"skew": func(policy *Policy, _ *Sample, secondary *Sample) {
			secondary.PublishedAt = now.Add(-time.Duration(policy.MaxSourceSkewSeconds+1) * time.Second)
		},
		"deviation": func(_ *Policy, _ *Sample, secondary *Sample) {
			secondary.PriceMicros = 130_000_000
		},
		"confidence": func(_ *Policy, primary *Sample, _ *Sample) {
			primary.ConfidenceMicros = primary.PriceMicros
		},
	} {
		t.Run(name, func(t *testing.T) {
			policy := testPolicy()
			primary := testSample(policy.PrimarySourceSHA256, 150_000_000, now)
			secondary := testSample(policy.SecondarySourceSHA256, 150_000_000, now)
			mutate(&policy, &primary, &secondary)
			if _, err := Evaluate(policy, primary, secondary, now); err == nil {
				t.Fatal("invalid price evidence was accepted")
			}
		})
	}
}

func TestRatioWithinBPSDoesNotOverflow(t *testing.T) {
	if !ratioWithinBPS(^uint64(0)/10_000, ^uint64(0), 1) {
		t.Fatal("valid large ratio was rejected")
	}
	if ratioWithinBPS(^uint64(0), ^uint64(0), 9_999) {
		t.Fatal("invalid large ratio was accepted")
	}
}

func testPolicy() Policy {
	return Policy{
		Version: Version, Feed: FeedSOLUSD, Direction: SellAtOrAbove,
		ThresholdMicros: 150_000_000, MaxAgeSeconds: 15,
		MaxSourceSkewSeconds: 5, MaxDeviationBPS: 100, MaxConfidenceBPS: 100,
		PrimarySourceSHA256:   strings.Repeat("a", 64),
		SecondarySourceSHA256: strings.Repeat("b", 64),
	}
}

func testSample(source string, price uint64, at time.Time) Sample {
	return Sample{
		SourceSHA256: source, Feed: FeedSOLUSD, PriceMicros: price,
		ConfidenceMicros: 100_000, PublishedAt: at,
	}
}
