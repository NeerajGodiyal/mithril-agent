package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestStrategyDCAPlanExactSOLBudget(t *testing.T) {
	var output bytes.Buffer
	if err := strategyDCAPlan([]string{
		"--total-sol", "1", "--days", "4", "--trades-per-day", "2",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var plan dcaPlan
	if err := json.Unmarshal(output.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Status != "planned_not_authorized" || plan.Authorized ||
		plan.BudgetLamports != 1_000_000_000 || plan.Actions != 8 ||
		plan.InputLamportsPerAction != 125_000_000 ||
		plan.PlannedInputLamports != plan.BudgetLamports ||
		plan.UnallocatedRemainderLamports != 0 ||
		plan.ScheduleWindowSeconds != 43_200 || !plan.RequiresDailyAuthorization {
		t.Fatalf("plan = %+v", plan)
	}
	if !slices.Contains(plan.SetupFlags, "125000000") ||
		!slices.Contains(plan.DailyEnableTemplate, "--allow-any-price") {
		t.Fatalf("plan did not compose the existing bounded commands: %+v", plan)
	}
}

func TestStrategyDCAPlanConvertsExplicitUSDReferenceOnce(t *testing.T) {
	var output bytes.Buffer
	if err := strategyDCAPlan([]string{
		"--budget-usd", "1000", "--reference-sol-usd", "200",
		"--days", "5",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var plan dcaPlan
	if err := json.Unmarshal(output.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.BudgetLamports != 5_000_000_000 || plan.InputLamportsPerAction != 1_000_000_000 ||
		plan.BudgetSOL != "5.000000000" || plan.BudgetUSD != "1000" ||
		plan.ReferenceSOLUSD != "200" || plan.BudgetScope != "swap_input_only" ||
		!plan.FeesAndRentAdditional || plan.ExecutionModel != "self_managed_bounded_runner" ||
		plan.MainnetExecution != "disabled" || plan.Authorized {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestStrategyDCAPlanNeverOverspendsNonDivisibleBudget(t *testing.T) {
	var output bytes.Buffer
	if err := strategyDCAPlan([]string{
		"--total-sol", "0.00000001", "--days", "3",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var plan dcaPlan
	if err := json.Unmarshal(output.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.BudgetLamports != 10 || plan.PlannedInputLamports != 9 ||
		plan.UnallocatedRemainderLamports != 1 {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestStrategyDCAPlanRefusesAmbiguousOrUnexecutablePlans(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"no budget", []string{"--days", "1"}, "exactly one"},
		{"two budgets", []string{"--total-sol", "1", "--budget-usd", "1", "--days", "1"}, "exactly one"},
		{"USD without reference", []string{"--budget-usd", "1000", "--days", "1"}, "never invents"},
		{"reference with SOL", []string{"--total-sol", "1", "--reference-sol-usd", "200", "--days", "1"}, "only valid"},
		{"non-dividing cadence", []string{"--total-sol", "1", "--days", "1", "--trades-per-day", "7"}, "divide one UTC day"},
		{"too many days", []string{"--total-sol", "1", "--days", "366"}, "1 to 365"},
		{"too little per action", []string{"--total-sol", "0.000000001", "--days", "2"}, "too small"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := strategyDCAPlan(test.args, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestStrategyDCAPlanIsRoutedByCLI(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{
		"strategy", "dca-plan", "--total-sol", "2", "--days", "2",
	}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"status":"planned_not_authorized"`) {
		t.Fatalf("output = %s", output.String())
	}
}
