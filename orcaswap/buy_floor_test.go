package orcaswap

import (
	"errors"
	"testing"
)

// The sell leg reports a below-floor quote as ErrQuoteBelowFloor so the runner
// classifies it price_below_floor and stays quiet. The buy leg folded the same
// condition into a generic "outside policy" error, so an ordinary unfavourable
// price on a buy classified as operation_failed and raised the critical alert.
//
// Both legs must agree: a market that has not reached the operator's price is
// not a fault.
func TestBuyBelowFloorIsTheSameSentinelAsSell(t *testing.T) {
	policy := testBuyPolicyV2()
	policy.MaxInputTokenAmount = 1_000_000
	policy.MinOutputLamports = 900_000
	quote := Quote{
		InputAmount:     1_000_000,
		EstimatedOutput: 800_000,
		MinimumOutput:   800_000,
		SlippageBPS:     100,
	}
	_, err := ValidateBuyInstructionsV2(policy, quote, nil)
	if !errors.Is(err, ErrQuoteBelowFloor) {
		t.Fatalf("below-floor buy = %v, want ErrQuoteBelowFloor", err)
	}
}

// A structurally broken quote must still be reported as a fault, never as the
// market having moved — otherwise a malformed adapter response would be
// silently tolerated as an ordinary price decline.
func TestBuyStructuralFaultIsNotReportedAsPrice(t *testing.T) {
	policy := testBuyPolicyV2()
	policy.MaxInputTokenAmount = 1_000_000
	policy.MinOutputLamports = 900_000
	for name, quote := range map[string]Quote{
		"no input":               {InputAmount: 0, EstimatedOutput: 1_000_000, MinimumOutput: 950_000, SlippageBPS: 100},
		"input over cap":         {InputAmount: 2_000_000, EstimatedOutput: 1_000_000, MinimumOutput: 950_000, SlippageBPS: 100},
		"no output":              {InputAmount: 1_000_000, EstimatedOutput: 0, MinimumOutput: 0, SlippageBPS: 100},
		"minimum above estimate": {InputAmount: 1_000_000, EstimatedOutput: 950_000, MinimumOutput: 1_000_000, SlippageBPS: 100},
		"no slippage":            {InputAmount: 1_000_000, EstimatedOutput: 1_000_000, MinimumOutput: 950_000, SlippageBPS: 0},
		"slippage over cap":      {InputAmount: 1_000_000, EstimatedOutput: 1_000_000, MinimumOutput: 950_000, SlippageBPS: 500},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ValidateBuyInstructionsV2(policy, quote, nil)
			if err == nil {
				t.Fatal("structural fault accepted")
			}
			if errors.Is(err, ErrQuoteBelowFloor) {
				t.Fatalf("structural fault reported as a price decline: %v", err)
			}
		})
	}
}
