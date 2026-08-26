package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func TestSwapDemoRunsOneActionAndStops(t *testing.T) {
	configPath := writeSwapDemoConfig(t)
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	statuses := []operatorstatus.View{
		{
			RunnerState: "recent",
			Control:     control.Status{Mode: control.ModeNoNewActions},
			Result:      execution.Result{Decision: "stopped"},
		},
		{
			RunnerState: "recent",
			Control:     control.Status{Mode: control.ModeNoNewActions},
			LastAction: operatorstatus.Action{
				ObservedAt: now,
				Result: execution.Result{
					ActionID: "new", Decision: "complete", Verdict: "finalized",
					Signature: "new-signature", Submitted: true, AmountLamports: 1_000_000,
					InputAmount: 1_000_000, InputAsset: "SOL", OutputAsset: "devUSDC",
					MinimumOutput: 21_000, OutputAmount: 21_743,
				},
			},
		},
	}
	var checked, enabled, stopped int
	withSwapDemoStubs(t,
		func(context.Context, string) error { checked++; return nil },
		func(string, time.Duration) error { enabled++; return nil },
		func(string) (operatorstatus.View, error) {
			view := statuses[0]
			statuses = statuses[1:]
			return view, nil
		},
		func(string) error { stopped++; return nil },
		func(context.Context, time.Duration) error { return nil },
	)
	var output bytes.Buffer
	if err := runContext(t.Context(), []string{
		"demo",
		"--config", configPath, "--timeout", minDemoTimeout.String(),
	}, &output); err != nil {
		t.Fatal(err)
	}
	if checked != 1 || enabled != 1 || stopped != 1 {
		t.Fatalf("check=%d enable=%d stop=%d", checked, enabled, stopped)
	}
	for _, expected := range []string{
		"Checking the Mithril node", "Allowing exactly one bounded Devnet trade",
		"Waiting for the configured condition",
		"✅ Devnet trade complete", "Output: 0.021743 devUSDC (minimum 0.021000 devUSDC)",
		"new-signature", "cluster=devnet", "Control: stopped",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output %q does not contain %q", output.String(), expected)
		}
	}
}

func TestSwapDemoRequiresFreshStoppedAuthority(t *testing.T) {
	configPath := writeSwapDemoConfig(t)
	tests := map[string]operatorstatus.View{
		"enabled": {
			RunnerState: "recent",
			Control: control.Status{
				Mode: control.ModeDevnetEnabled, MaxActions: 1, RemainingActions: 1,
			},
			Result: execution.Result{Decision: "waiting"},
		},
		"capacity remains": {
			RunnerState: "recent",
			Control: control.Status{
				Mode: control.ModeNoNewActions, MaxActions: 1, RemainingActions: 1,
			},
			Result: execution.Result{Decision: "stopped"},
		},
		"terminal latch": {
			RunnerState: "recent",
			Control: control.Status{
				Mode: control.ModeNoNewActions, TerminalActionID: strings.Repeat("a", 64),
				TerminalOutcome: "halted",
			},
			Result: execution.Result{Decision: "stopped"},
		},
		"action executing": {
			RunnerState: "recent", Control: control.Status{Mode: control.ModeNoNewActions},
			Result: execution.Result{Decision: "executing"},
		},
		"previous submission": {
			RunnerState: "recent", Control: control.Status{Mode: control.ModeNoNewActions},
			Result: execution.Result{Decision: "stopped"},
			LastAction: operatorstatus.Action{Result: execution.Result{
				ActionID: "old", Decision: "complete", Submitted: true,
			}},
		},
		"send boundary in journal": {
			RunnerState: "recent", Control: control.Status{Mode: control.ModeNoNewActions},
			Result:  execution.Result{Decision: "stopped"},
			Journal: journal.Stats{SendStartedRecords: 1},
		},
		"submission in journal": {
			RunnerState: "recent", Control: control.Status{Mode: control.ModeNoNewActions},
			Result:  execution.Result{Decision: "stopped"},
			Journal: journal.Stats{SubmittedRecords: 1},
		},
	}
	for name, view := range tests {
		t.Run(name, func(t *testing.T) {
			enabled := false
			withSwapDemoStubs(t,
				func(context.Context, string) error { return nil },
				func(string, time.Duration) error { enabled = true; return nil },
				func(string) (operatorstatus.View, error) { return view, nil },
				func(string) error { return nil },
				func(context.Context, time.Duration) error { return nil },
			)
			err := runSwapDemo(t.Context(), []string{
				"--config", configPath, "--timeout", minDemoTimeout.String(),
			}, &bytes.Buffer{})
			// What matters is that it refuses and never arms an action. The
			// wording is now specific to each cause, so assert the property
			// and that the operator is given a command, not a fixed phrase.
			if err == nil || enabled {
				t.Fatalf("error=%v enabled=%v", err, enabled)
			}
			if !strings.Contains(err.Error(), "mithril-agent") {
				t.Errorf("refusal names no command to run: %v", err)
			}
		})
	}
}

func TestSwapDemoCheckFailureNeverEnables(t *testing.T) {
	configPath := writeSwapDemoConfig(t)
	enabled := false
	withSwapDemoStubs(t,
		func(context.Context, string) error { return errors.New("RPC unavailable") },
		func(string, time.Duration) error { enabled = true; return nil },
		func(string) (operatorstatus.View, error) { return operatorstatus.View{}, nil },
		func(string) error { return nil },
		func(context.Context, time.Duration) error { return nil },
	)
	err := runSwapDemo(t.Context(), []string{
		"--config", configPath, "--timeout", minDemoTimeout.String(),
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "Devnet check failed") || enabled {
		t.Fatalf("error=%v enabled=%v", err, enabled)
	}
}

func TestValidDemoCompletionBindsDirectionAndEvidence(t *testing.T) {
	sell := operatorstatus.Result{
		Decision: "complete", Verdict: "finalized", Submitted: true, Signature: "signature",
		AmountLamports: 1, InputAmount: 1, InputAsset: "SOL", OutputAsset: "devUSDC",
		MinimumOutput: 1, OutputAmount: 1,
	}
	if !validDemoCompletion(orcaswap.ProfileName, sell) {
		t.Fatal("valid sell completion was rejected")
	}
	buy := sell
	buy.AmountLamports = 0
	buy.InputAsset, buy.OutputAsset = "devUSDC", "SOL"
	if !validDemoCompletion(orcaswap.BuyProfileName, buy) {
		t.Fatal("valid buy completion was rejected")
	}
	tests := map[string]func(*operatorstatus.Result){
		"not submitted":     func(result *operatorstatus.Result) { result.Submitted = false },
		"missing signature": func(result *operatorstatus.Result) { result.Signature = "" },
		"zero input":        func(result *operatorstatus.Result) { result.InputAmount = 0 },
		"wrong assets":      func(result *operatorstatus.Result) { result.OutputAsset = "SOL" },
		"wrong amount":      func(result *operatorstatus.Result) { result.AmountLamports++ },
		"below minimum":     func(result *operatorstatus.Result) { result.OutputAmount = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := sell
			mutate(&changed)
			if validDemoCompletion(orcaswap.ProfileName, changed) {
				t.Fatal("invalid completion was accepted")
			}
		})
	}
}

func TestSwapDemoRejectsTimeoutShorterThanOneRunnerStep(t *testing.T) {
	configPath := writeSwapDemoConfig(t)
	enabled := false
	withSwapDemoStubs(t,
		func(context.Context, string) error { return nil },
		func(string, time.Duration) error { enabled = true; return nil },
		func(string) (operatorstatus.View, error) { return operatorstatus.View{}, nil },
		func(string) error { return nil },
		func(context.Context, time.Duration) error { return nil },
	)
	err := runSwapDemo(t.Context(), []string{
		"--config", configPath, "--timeout", swapStepTimeout.String(),
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), minDemoTimeout.String()) || enabled {
		t.Fatalf("error=%v enabled=%v", err, enabled)
	}
}

func TestSwapDemoExplainsStaleMithrilRPCBinding(t *testing.T) {
	configPath := writeSwapDemoConfig(t)
	withSwapDemoStubs(t,
		func(context.Context, string) error {
			// The real sentinel, not a same-worded errors.New: this test used to
			// build a lookalike and pass because the classifier compared strings.
			return txflow.ErrNodeUnavailable
		},
		func(string, time.Duration) error { return nil },
		func(string) (operatorstatus.View, error) { return operatorstatus.View{}, nil },
		func(string) error { return nil },
		func(context.Context, time.Duration) error { return nil },
	)
	err := runSwapDemo(t.Context(), []string{
		"--config", configPath, "--timeout", minDemoTimeout.String(),
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "MITHRIL_AGENT_MITHRIL_RPC_URL") {
		t.Fatalf("error=%v", err)
	}
}

func TestSwapDemoTimeoutStops(t *testing.T) {
	configPath := writeSwapDemoConfig(t)
	stopped := 0
	view := operatorstatus.View{
		RunnerState: "recent",
		Control:     control.Status{Mode: control.ModeNoNewActions},
	}
	withSwapDemoStubs(t,
		func(context.Context, string) error { return nil },
		func(string, time.Duration) error { return nil },
		func(string) (operatorstatus.View, error) { return view, nil },
		func(string) error { stopped++; return nil },
		func(context.Context, time.Duration) error { return context.DeadlineExceeded },
	)
	err := runSwapDemo(t.Context(), []string{
		"--config", configPath, "--timeout", minDemoTimeout.String(),
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "timed out") ||
		!strings.Contains(err.Error(), "new actions were stopped") || stopped != 1 {
		t.Fatalf("error=%v stopped=%d", err, stopped)
	}
}

func TestSwapDemoReportsTimeoutAndStopFailure(t *testing.T) {
	configPath := writeSwapDemoConfig(t)
	view := operatorstatus.View{
		RunnerState: "recent",
		Control:     control.Status{Mode: control.ModeNoNewActions},
	}
	withSwapDemoStubs(t,
		func(context.Context, string) error { return nil },
		func(string, time.Duration) error { return nil },
		func(string) (operatorstatus.View, error) { return view, nil },
		func(string) error { return errors.New("stop unavailable") },
		func(context.Context, time.Duration) error { return context.DeadlineExceeded },
	)
	err := runSwapDemo(t.Context(), []string{
		"--config", configPath, "--timeout", minDemoTimeout.String(),
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "timed out") ||
		!strings.Contains(err.Error(), "stopping new actions also failed") ||
		strings.Contains(err.Error(), "new actions were stopped") {
		t.Fatalf("combined failure = %v", err)
	}
}

func TestSwapDemoJSONFailureIsStableAndBounded(t *testing.T) {
	configPath := writeSwapDemoConfig(t)
	withSwapDemoStubs(t,
		func(context.Context, string) error { return errors.New("provider payload omitted") },
		func(string, time.Duration) error { return nil },
		func(string) (operatorstatus.View, error) { return operatorstatus.View{}, nil },
		func(string) error { return nil },
		func(context.Context, time.Duration) error { return nil },
	)
	var output bytes.Buffer
	err := runContext(t.Context(), []string{
		"demo", "--config", configPath, "--json",
	}, &output)
	if err == nil {
		t.Fatal("JSON demo accepted a failed readiness check")
	}
	var failure demoFailure
	if decodeErr := json.Unmarshal(output.Bytes(), &failure); decodeErr != nil {
		t.Fatalf("JSON failure output = %q: %v", output.String(), decodeErr)
	}
	if failure.Status != "failed" || failure.Network != "devnet" ||
		failure.ErrorCode != "check_failed" || failure.Control != "unknown" ||
		strings.Contains(output.String(), "provider payload") {
		t.Fatalf("JSON failure = %+v, output = %q", failure, output.String())
	}
}

func TestSwapDemoJSONCoversArgumentAndConfigurationFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		args     []string
		wantCode string
	}{
		{name: "arguments", args: []string{"demo", "--json"}, wantCode: "arguments"},
		{name: "configuration", args: []string{
			"demo", "--json", "--config", filepath.Join(t.TempDir(), "missing.json"),
		}, wantCode: "configuration"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			err := runContext(t.Context(), test.args, &output)
			if err == nil {
				t.Fatal("demo accepted invalid input")
			}
			var failure demoFailure
			if decodeErr := json.Unmarshal(output.Bytes(), &failure); decodeErr != nil {
				t.Fatalf("JSON failure output = %q: %v", output.String(), decodeErr)
			}
			if failure.Status != "failed" || failure.Network != "devnet" ||
				failure.ErrorCode != test.wantCode || strings.Contains(output.String(), "missing.json") {
				t.Fatalf("JSON failure = %+v, output = %q", failure, output.String())
			}
		})
	}
}

func TestSwapDemoExplicitFalseKeepsHumanOutputMode(t *testing.T) {
	var output bytes.Buffer
	err := runContext(t.Context(), []string{"demo", "--json=false"}, &output)
	if err == nil {
		t.Fatal("demo accepted missing configuration")
	}
	if output.Len() != 0 || demoJSONRequested([]string{"--json=false"}) ||
		!demoJSONRequested([]string{"--json=true"}) ||
		!demoJSONRequested([]string{"--json=false", "--json=true"}) ||
		demoJSONRequested([]string{"--json=true", "--json=false"}) {
		t.Fatalf("output = %q", output.String())
	}
}

func TestDemoPhaseDistinguishesPriceExecutionAndConfirmation(t *testing.T) {
	price := &pricetrigger.Status{Available: true}
	for _, test := range []struct {
		name     string
		view     operatorstatus.View
		wantKey  string
		wantText string
	}{
		{
			name: "price", view: operatorstatus.View{Result: operatorstatus.Result{
				Decision: "waiting", PriceTrigger: price,
			}}, wantKey: "price", wantText: "price condition",
		},
		{
			name: "execution", view: operatorstatus.View{Result: operatorstatus.Result{
				Decision: "executing",
			}}, wantKey: "executing", wantText: "prepared and checked",
		},
		{
			name: "confirmation", view: operatorstatus.View{Result: operatorstatus.Result{
				Decision: "pending",
			}}, wantKey: "confirming", wantText: "final confirmation",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			key, text := demoPhase(test.view, "")
			if key != test.wantKey || !strings.Contains(text, test.wantText) {
				t.Fatalf("phase = %q, %q", key, text)
			}
		})
	}
}

func TestSwapDemoStopsWhenProgressOutputFailsAfterEnable(t *testing.T) {
	configPath := writeSwapDemoConfig(t)
	stopped := 0
	view := operatorstatus.View{
		RunnerState: "recent",
		Control:     control.Status{Mode: control.ModeNoNewActions},
	}
	withSwapDemoStubs(t,
		func(context.Context, string) error { return nil },
		func(string, time.Duration) error { return nil },
		func(string) (operatorstatus.View, error) { return view, nil },
		func(string) error { stopped++; return nil },
		func(context.Context, time.Duration) error { return nil },
	)
	writer := &failAfterWrites{remaining: 2}
	err := runSwapDemo(t.Context(), []string{
		"--config", configPath, "--timeout", minDemoTimeout.String(),
	}, writer)
	if err == nil || stopped != 1 {
		t.Fatalf("error=%v stopped=%d", err, stopped)
	}
}

type failAfterWrites struct {
	remaining int
}

func (w *failAfterWrites) Write(data []byte) (int, error) {
	if w.remaining == 0 {
		return 0, errors.New("output unavailable")
	}
	w.remaining--
	return len(data), nil
}

func TestSwapDemoDoesNotReportSuccessWhenStopFails(t *testing.T) {
	configPath := writeSwapDemoConfig(t)
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	statuses := []operatorstatus.View{
		{
			RunnerState: "recent",
			Control:     control.Status{Mode: control.ModeNoNewActions},
			Result:      execution.Result{Decision: "stopped"},
		},
		{
			RunnerState: "recent",
			LastAction: operatorstatus.Action{
				ObservedAt: now,
				Result: execution.Result{
					ActionID: "new", Decision: "complete", Verdict: "finalized",
					Signature: "new-signature", Submitted: true,
					AmountLamports: 1, InputAmount: 1, InputAsset: "SOL", OutputAsset: "devUSDC",
					MinimumOutput: 1, OutputAmount: 1,
				},
			},
		},
	}
	withSwapDemoStubs(t,
		func(context.Context, string) error { return nil },
		func(string, time.Duration) error { return nil },
		func(string) (operatorstatus.View, error) {
			view := statuses[0]
			statuses = statuses[1:]
			return view, nil
		},
		func(string) error { return errors.New("write failed") },
		func(context.Context, time.Duration) error { return nil },
	)
	var output bytes.Buffer
	err := runSwapDemo(t.Context(), []string{
		"--config", configPath, "--timeout", minDemoTimeout.String(),
	}, &output)
	if err == nil || !strings.Contains(err.Error(), "stopping new actions failed") {
		t.Fatalf("error=%v", err)
	}
	if strings.Contains(output.String(), "✅ Devnet trade complete") {
		t.Fatalf("unexpected success output %q", output.String())
	}
}

func writeSwapDemoConfig(t *testing.T) string {
	t.Helper()
	fixture := newPreflightFixture(t)
	profile := testSwapProfile(fixture.policy.Source)
	configureSwapPreflightFixture(t, &fixture, profile)
	return fixture.configPath
}

func withSwapDemoStubs(
	t *testing.T,
	check func(context.Context, string) error,
	enable func(string, time.Duration) error,
	status func(string) (operatorstatus.View, error),
	stop func(string) error,
	wait func(context.Context, time.Duration) error,
) {
	t.Helper()
	oldCheck, oldEnable, oldStatus := demoCheck, demoEnable, demoStatus
	oldStop, oldWait := demoStop, demoWait
	demoCheck, demoEnable, demoStatus = check,
		func(path, _ string, duration time.Duration) error { return enable(path, duration) }, status
	demoStop, demoWait = stop, wait
	t.Cleanup(func() {
		demoCheck, demoEnable, demoStatus = oldCheck, oldEnable, oldStatus
		demoStop, demoWait = oldStop, oldWait
	})
}

// The demonstration arms an action and waits for the runner to execute it, so
// "not ready" most often just means the runner is not running. Saying only
// "check status" leaves an operator with no next step at the exact moment they
// need one — which is what happened during the first live end-to-end run.
func TestDemoNotReadyNamesTheFix(t *testing.T) {
	notStarted := operatorstatus.View{RunnerState: "not_started"}
	reason := demoNotReadyReason(notStarted)
	if !strings.Contains(reason, "swap run") {
		t.Errorf("a stopped runner does not name the command that starts it: %q", reason)
	}
	if demoReady(notStarted) {
		t.Error("a stopped runner was treated as ready")
	}

	// Each distinct cause must give its own actionable sentence, never a
	// generic one.
	for name, view := range map[string]operatorstatus.View{
		"attention required": {RunnerState: "recent", AttentionRequired: true},
		"already acted": {
			RunnerState: "recent",
			Journal:     journal.Stats{SubmittedRecords: 1},
		},
	} {
		got := demoNotReadyReason(view)
		if got == "" || strings.Contains(got, "run mithril-agent swap status") {
			t.Errorf("%s fell through to the generic message: %q", name, got)
		}
		if !strings.Contains(got, "mithril-agent") {
			t.Errorf("%s names no command: %q", name, got)
		}
	}
}
