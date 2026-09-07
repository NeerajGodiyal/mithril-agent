package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/paperdashboard"
)

type researchAllocationQuoteFunc func(context.Context, jupiterquote.Request) (jupiterquote.Result, error)

func (f researchAllocationQuoteFunc) Quote(ctx context.Context, request jupiterquote.Request) (jupiterquote.Result, error) {
	return f(ctx, request)
}

func researchAllocationQuoteResult(request jupiterquote.Request, at time.Time) jupiterquote.Result {
	return jupiterquote.Result{InputAmount: request.InputAmount, EstimatedOutput: 1_000_000,
		MinimumOutput:  100 * uint64(10_000-request.SlippageBPS),
		PriceImpactPct: "0.001", ReceivedAt: at, ResponseSHA256: strings.Repeat("a", 64)}
}

func TestResearchAllocationQuotesExactPolicyLot(t *testing.T) {
	for _, market := range []string{"sol", "jup"} {
		t.Run(market, func(t *testing.T) { testResearchAllocationQuotesPolicyLot(t, market) })
	}
}

func testResearchAllocationQuotesPolicyLot(t *testing.T, market string) {
	t.Helper()
	generation, at := researchAllocationPerformanceFixture(t)
	if market == "jup" {
		_, policy, _, _ := shadowPortfolioTestPolicies(t)
		write := func(path string, value any) {
			t.Helper()
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
		}
		policyPath := filepath.Join(generation, "jup-policy.json")
		write(policyPath, policy)
		capital, err := shadowPortfolioCapital(policy, 300_000_000)
		if err != nil {
			t.Fatal(err)
		}
		instruction, _, err := paperdashboard.LoadInstruction(filepath.Join(generation, "instruction.json"))
		if err != nil {
			t.Fatal(err)
		}
		instruction.PaperCapitalMicros = capital
		instruction.MaximumOrderMicros = min(instruction.MaximumOrderMicros, capital)
		write(filepath.Join(generation, "instruction.json"), instruction)
		digest, err := paperdashboard.InstructionSHA256(*instruction)
		if err != nil {
			t.Fatal(err)
		}
		policySHA, err := policy.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(generation, "portfolio.json"), shadowPortfolioManifest{Version: shadowPortfolioVersion, Status: "paper_portfolio", PaperOnly: true,
			InstructionSHA256: digest, TotalCapitalLimitMicros: capital, MaxSOLUSDMicros: 300_000_000,
			Books: []shadowPortfolioBook{{ID: "jup", Market: policy.Market, PolicyPath: policyPath, PolicySHA256: policySHA}}})
		if err := os.MkdirAll(filepath.Join(generation, "runs", "jup", "pre-champion"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	source, err := resolveResearchAllocation(generation, market, "pre-champion", at)
	if err != nil {
		t.Fatal(err)
	}
	var requests []jupiterquote.Request
	quotes := researchAllocationQuoteFunc(func(_ context.Context, request jupiterquote.Request) (jupiterquote.Result, error) {
		requests = append(requests, request)
		return researchAllocationQuoteResult(request, at), nil
	})
	result, err := collectResearchAllocationQuotes(t.Context(), generation, market, "pre-champion", time.Minute, quotes, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	policy := source.policy
	if len(requests) != 2 || requests[0] != (jupiterquote.Request{Taker: policy.Observe, InputMint: policy.QuoteRoute.InputMint, OutputMint: policy.QuoteRoute.OutputMint, InputAmount: policy.InputAmount, SlippageBPS: policy.SlippageBPS}) ||
		requests[1] != (jupiterquote.Request{Taker: policy.Observe, InputMint: policy.QuoteRoute.OutputMint, OutputMint: policy.QuoteRoute.InputMint, InputAmount: result.Initial.EstimatedOutput, SlippageBPS: policy.SlippageBPS}) {
		t.Fatalf("quotes did not use exact directional policy lots: %+v", requests)
	}
	policySHA, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != 1 || result.Status != "diagnostic_only" || result.PolicySHA256 != policySHA || result.Binding != source.binding || !result.RoleBindingVerified || result.ProcessHealthVerified || result.RecordedBasisEligible ||
		result.SizeBasis != "initial_policy_lot" || result.QuoteSource != "jupiter_swap_v2_build_metis" || result.Verification != "single_provider" || result.PriceImpactUnit != "decimal_ratio" || result.ReceivedAtBasis != "host_response_receipt" || result.AllInCostsKnown || result.RoundTripGuaranteed ||
		result.InputDecimals != policy.InputDecimals || result.OutputDecimals != policy.OutputDecimals || result.SlippageBPS != policy.SlippageBPS {
		t.Fatalf("quote diagnostic overstated evidence: %+v", result)
	}
}

func TestResearchAllocationQuotesRejectInvalidEvidence(t *testing.T) {
	for _, name := range []string{"missing time", "future", "stale", "missing hash", "missing impact", "wrong amount", "clock regression", "old first receipt", "day rollover", "role drift", "missing policy", "cancelled", "cancelled during quote", "second provider error", "zero age", "large age"} {
		t.Run(name, func(t *testing.T) {
			generation, at := researchAllocationPerformanceFixture(t)
			if name == "day rollover" {
				at = time.Date(at.Year(), at.Month(), at.Day(), 23, 59, 59, 0, time.UTC)
			}
			current := at
			age := time.Minute
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "cancelled" {
				cancel()
			}
			if name == "zero age" {
				age = 0
			}
			if name == "large age" {
				age += time.Nanosecond
			}
			calls := 0
			quotes := researchAllocationQuoteFunc(func(_ context.Context, request jupiterquote.Request) (jupiterquote.Result, error) {
				calls++
				result := researchAllocationQuoteResult(request, current)
				switch name {
				case "missing time":
					result.ReceivedAt = time.Time{}
				case "future":
					result.ReceivedAt = current.Add(time.Nanosecond)
				case "stale":
					result.ReceivedAt = current.Add(-time.Nanosecond)
				case "missing hash":
					result.ResponseSHA256 = ""
				case "missing impact":
					result.PriceImpactPct = ""
				case "wrong amount":
					result.InputAmount++
				case "clock regression":
					current = current.Add(-time.Second)
				case "old first receipt":
					if calls == 1 {
						current = current.Add(30 * time.Second)
					} else {
						current = current.Add(31 * time.Second)
						result.ReceivedAt = current
					}
				case "cancelled during quote":
					cancel()
				case "second provider error":
					if calls == 2 {
						return jupiterquote.Result{}, errors.New("provider unavailable")
					}
				case "day rollover":
					current = at.Add(2 * time.Second)
					result.ReceivedAt = current
				case "role drift":
					if err := os.WriteFile(filepath.Join(generation, "status", "sol", "champion-owned"), nil, 0600); err != nil {
						t.Fatal(err)
					}
				case "missing policy":
					if calls == 1 {
						if err := os.Remove(filepath.Join(generation, "sol-policy.json")); err != nil {
							t.Fatal(err)
						}
					}
				}
				return result, nil
			})
			result, err := collectResearchAllocationQuotes(ctx, generation, "sol", "pre-champion", age, quotes, func() time.Time { return current })
			if err == nil || !reflect.DeepEqual(result, researchAllocationQuotes{}) {
				t.Fatal("invalid quote evidence returned a usable artifact")
			}
			if name == "second provider error" && calls != 2 || name == "cancelled during quote" && calls != 1 {
				t.Fatalf("unexpected quote calls after failure: %d", calls)
			}
		})
	}
}

func TestResearchAllocationQuotesApparentGainIsNotNegativeLoss(t *testing.T) {
	generation, at := researchAllocationPerformanceFixture(t)
	source, err := resolveResearchAllocation(generation, "sol", "pre-champion", at)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	quotes := researchAllocationQuoteFunc(func(_ context.Context, request jupiterquote.Request) (jupiterquote.Result, error) {
		calls++
		result := researchAllocationQuoteResult(request, at)
		if calls == 2 {
			result.EstimatedOutput = source.policy.InputAmount + 1
			var ok bool
			result.MinimumOutput, ok = boundedMulDivCeil(result.EstimatedOutput, uint64(10_000-request.SlippageBPS), 10_000)
			if !ok {
				t.Fatal("fixture output floor overflow")
			}
		}
		return result, nil
	})
	result, err := collectResearchAllocationQuotes(t.Context(), generation, "sol", "pre-champion", time.Minute, quotes, func() time.Time { return at })
	if err != nil || calls != 2 || result.Reverse.EstimatedOutput != source.policy.InputAmount+1 || result.RoundTripRouteLossBPS != 0 {
		t.Fatalf("apparent route gain was reported as loss: %+v, %v", result, err)
	}
}

func TestResearchAllocationQuotesCLIRejectsInvalidInputs(t *testing.T) {
	var output bytes.Buffer
	if err := runResearchAllocationQuotes([]string{"--help"}, &output); err != nil || !strings.Contains(output.String(), "allocation-quotes") {
		t.Fatalf("quote help unavailable: %v", err)
	}
	for _, args := range [][]string{nil, {"--unknown"}, {"--max-age", "0s"}, {"--max-age", "61s"}} {
		output.Reset()
		if err := runResearchAllocationQuotes(args, &output); err == nil || output.Len() != 0 {
			t.Fatalf("invalid quote arguments produced output: %v, %v", args, err)
		}
	}
	generation, _ := researchAllocationPerformanceFixture(t)
	t.Setenv(jupiterAPIKeyEnvironment, "")
	output.Reset()
	if err := runResearch([]string{"allocation-quotes", "--generation", generation, "--market", "sol", "--role", "pre-champion"}, &output); err == nil || !strings.Contains(err.Error(), "API key") || output.Len() != 0 {
		t.Fatalf("missing quote credential did not fail before collection: %v", err)
	}
	output.Reset()
	if err := runResearchAllocationQuotes([]string{"--generation", generation, "--market", "sol", "--role", "pre-champion", "--max-age", "1ns"}, &output); err == nil || !strings.Contains(err.Error(), "one millisecond") || output.Len() != 0 {
		t.Fatalf("unrepresentable receipt age was accepted: %v", err)
	}
}
