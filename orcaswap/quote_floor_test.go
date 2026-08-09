package orcaswap

import (
	"errors"
	"testing"
)

// A sound quote priced below the operator's floor is a market condition, not a
// malformed quote. An unattended runner reports only bounded reasons, so unless
// this case is distinguishable the operator sees "operation_failed" forever with
// nothing to act on. Observed live: the floor read one base unit under after the
// agent's own trade moved the pool.
func TestQuoteBelowFloorIsDistinguishable(t *testing.T) {
	policy := testPolicy()
	quote := Quote{
		InputAmount:     policy.MaxInputLamports,
		EstimatedOutput: 21_528,
		MinimumOutput:   policy.MinOutputAmount - 1,
		SlippageBPS:     100,
	}
	_, err := ValidateInstructions(policy, quote, testInstructions())
	if err == nil {
		t.Fatal("a quote below the floor must still be refused")
	}
	if !errors.Is(err, ErrQuoteBelowFloor) {
		t.Errorf("err = %v, want ErrQuoteBelowFloor", err)
	}
}

// Structural problems must not be reported as a price condition, or a real
// defect would be dismissed as "the market moved".
func TestMalformedQuoteIsNotReportedAsPrice(t *testing.T) {
	for name, quote := range map[string]Quote{
		"zero input": {
			InputAmount: 0, EstimatedOutput: 21_528,
			MinimumOutput: 21_525, SlippageBPS: 100,
		},
		"input over policy": {
			InputAmount: testPolicy().MaxInputLamports + 1, EstimatedOutput: 21_528,
			MinimumOutput: 21_525, SlippageBPS: 100,
		},
		"minimum above estimate": {
			InputAmount: testPolicy().MaxInputLamports, EstimatedOutput: 21_528,
			MinimumOutput: 21_600, SlippageBPS: 100,
		},
		"slippage over policy": {
			InputAmount: testPolicy().MaxInputLamports, EstimatedOutput: 21_528,
			MinimumOutput: 21_525, SlippageBPS: 101,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ValidateInstructions(testPolicy(), quote, testInstructions())
			if err == nil {
				t.Fatal("must be refused")
			}
			if errors.Is(err, ErrQuoteBelowFloor) {
				t.Errorf("classified as a price condition: %v", err)
			}
		})
	}
}

// The floor still refuses. Splitting the reason must not widen what is accepted.
func TestQuoteAtFloorIsAccepted(t *testing.T) {
	policy := testPolicy()
	quote := Quote{
		InputAmount:     policy.MaxInputLamports,
		EstimatedOutput: 21_742,
		MinimumOutput:   policy.MinOutputAmount,
		SlippageBPS:     100,
	}
	if _, err := ValidateInstructions(policy, quote, testInstructions()); err != nil {
		t.Fatalf("a quote exactly at the floor must be accepted: %v", err)
	}
}
