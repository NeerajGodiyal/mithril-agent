package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestSwapPlanHasStableClosedOrder(t *testing.T) {
	profile := testSwapProfile("3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh")
	cfg := config{Swap: &profile}
	plan := newSwapPlan("/usr/local/bin/mithril-agent", "/private/agent/config.json", cfg)
	want := []string{
		"offline_preflight",
		"start_quote_service",
		"live_read_only_check",
		"start_runner_stopped",
		"verify_status",
		"verify_alert_delivery",
		"enable_one_action",
		"watch_confirmation",
		"stop_and_review",
	}
	got := make([]string, len(plan.Steps))
	for index, step := range plan.Steps {
		got[index] = step.ID
		if step.State != "command" && step.State != "external_check_required" {
			t.Fatalf("step %q state = %q", step.ID, step.State)
		}
		if step.State == "command" && len(step.Argv) == 0 {
			t.Fatalf("command step %q has no argv", step.ID)
		}
	}
	if plan.Version != swapPlanVersion || plan.Status != "configured" || !slices.Equal(got, want) {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestSwapPlanOutputContainsNoProviderOrAccountData(t *testing.T) {
	profile := testSwapProfile("3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh")
	cfg := config{Swap: &profile}
	var output bytes.Buffer
	if err := json.NewEncoder(&output).Encode(newSwapPlan(
		"/usr/local/bin/mithril-agent", "/private/agent/config.json", cfg,
	)); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"api-key", "rpc-one", "rpc-two", profile.Route.Owner,
		profile.Route.Pool, profile.Route.InputTokenAccount, profile.Route.OutputTokenAccount,
	} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("plan output exposed %q: %s", forbidden, output.String())
		}
	}
}
