package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const (
	defaultDemoTimeout = 5 * time.Minute
	minDemoTimeout     = swapStepTimeout + time.Minute
	maxDemoTimeout     = 20 * time.Minute
)

var errDemoJSONFailure = errors.New("demo failed; see JSON output")

var (
	demoCheck = func(ctx context.Context, path string) error {
		return runSwapCheck(ctx, []string{"--config", path}, io.Discard)
	}
	demoEnable = func(path, operatorSocket string, duration time.Duration) error {
		var output bytes.Buffer
		return runSwapEnable([]string{
			"--config", path,
			"--operator-socket", operatorSocket,
			"--duration", duration.String(),
			"--max-actions", "1",
			"--reason", "operator Devnet demo",
		}, &output)
	}
	demoStatus = func(path string) (operatorstatus.View, error) {
		provider, err := newOperatorProvider(path)
		if err != nil {
			return operatorstatus.View{}, err
		}
		return provider.Status()
	}
	demoStop = func(path string) error {
		cfg, err := readSwapConfig(path)
		if err != nil {
			return err
		}
		_, err = stopSwap(cfg, "Devnet demo finished")
		return err
	}
	demoWait = waitForDemoPoll
)

type demoResult struct {
	Status        string    `json:"status"`
	Network       string    `json:"network"`
	InputLamports uint64    `json:"input_lamports"`
	InputAmount   uint64    `json:"input_amount"`
	InputAsset    string    `json:"input_asset"`
	OutputAsset   string    `json:"output_asset"`
	MinimumOutput uint64    `json:"minimum_output"`
	OutputAmount  uint64    `json:"output_amount"`
	Signature     string    `json:"signature"`
	Verdict       string    `json:"verdict"`
	ObservedAt    time.Time `json:"observed_at"`
	Control       string    `json:"control"`
}

type demoFailure struct {
	Status    string `json:"status"`
	Network   string `json:"network"`
	ErrorCode string `json:"error_code"`
	Control   string `json:"control"`
}

func runSwapDemo(ctx context.Context, args []string, output io.Writer) (resultErr error) {
	jsonRequested := demoJSONRequested(args)
	jsonResultStarted := false
	failureCode := "arguments"
	if jsonRequested {
		defer func() {
			if resultErr == nil || jsonResultStarted {
				return
			}
			if err := writeDemoFailure(output, resultErr, failureCode); err != nil {
				resultErr = errors.New("write demo JSON failure result")
				return
			}
			resultErr = errDemoJSONFailure
		}()
	}
	flags := flag.NewFlagSet("swap demo", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	operatorSocket := flags.String("operator-socket", defaultOperatorSocket,
		"root-only submitter operator socket")
	timeout := flags.Duration("timeout", defaultDemoTimeout, "maximum time to wait")
	jsonOutput := flags.Bool("json", false, "print JSON instead of operator text")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent swap demo --config PATH --operator-socket PATH [--timeout DURATION] [--json]")
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("swap demo takes no positional arguments")
	}
	if *configPath == "" {
		// Only what this user's own setup recorded, never the installed
		// configuration: the demonstration authorises a trade, and acting on
		// somebody else's deployment because it happened to be on the same
		// machine is not something to infer from an omitted flag.
		if *configPath = recordedConfig(); *configPath == "" {
			return errors.New("swap demo requires --config, or run: mithril-agent setup")
		}
	}
	if *timeout < minDemoTimeout || *timeout > maxDemoTimeout {
		return fmt.Errorf(
			"swap demo timeout must be between %s and %s",
			minDemoTimeout,
			maxDemoTimeout,
		)
	}
	failureCode = "configuration"
	cfg, err := readSwapConfig(*configPath)
	if err != nil {
		return err
	}
	if cfg.Swap.Cluster != "devnet" {
		return errors.New("swap demo is restricted to Devnet")
	}
	failureCode = ""
	if err := writeDemoProgress(output, *jsonOutput, "Checking the Mithril node, quote service, and confirmation providers…"); err != nil {
		return err
	}
	if err := demoCheck(ctx, *configPath); err != nil {
		return fmt.Errorf("Devnet check failed: %w", explainDemoCheckError(err))
	}
	before, err := demoStatus(*configPath)
	if err != nil {
		return fmt.Errorf("read runner status: %w", err)
	}
	if !demoReady(before) {
		return errors.New(demoNotReadyReason(before))
	}
	if err := writeDemoProgress(output, *jsonOutput, "Checks passed. Allowing exactly one bounded Devnet trade…"); err != nil {
		return err
	}
	previousAction := before.LastAction.Result.ActionID
	activation := *timeout + 30*time.Second
	if activation > maxDemoTimeout {
		activation = maxDemoTimeout
	}
	if err := demoEnable(*configPath, *operatorSocket, activation); err != nil {
		return fmt.Errorf("enable one Devnet action: %w", err)
	}
	stopNeeded := true
	defer func() {
		if !stopNeeded {
			return
		}
		if err := demoStop(*configPath); err != nil {
			if resultErr == nil {
				resultErr = errors.New("trade finished, but stopping new actions failed")
			} else {
				resultErr = fmt.Errorf("%w; stopping new actions also failed", resultErr)
			}
		} else if resultErr != nil {
			resultErr = fmt.Errorf("%w; new actions were stopped", resultErr)
		}
	}()
	if err := writeDemoProgress(output, *jsonOutput, "Waiting for the configured condition and one bounded Devnet trade…"); err != nil {
		return err
	}

	waitCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	lastPhase := ""
	for {
		view, err := demoStatus(*configPath)
		if err != nil {
			return fmt.Errorf("read trade status: %w", err)
		}
		phase, message := demoPhase(view, previousAction)
		if phase != "" && phase != lastPhase {
			if err := writeDemoProgress(output, *jsonOutput, message); err != nil {
				return err
			}
			lastPhase = phase
		}
		action := view.LastAction
		if action.Result.ActionID != "" && action.Result.ActionID != previousAction {
			switch action.Result.Decision {
			case "complete":
				if !validDemoCompletion(cfg.Swap.Name, action.Result) {
					return errors.New("trade completed without finalized transaction evidence")
				}
				if err := demoStop(*configPath); err != nil {
					return errors.New("trade finalized, but stopping new actions failed")
				}
				stopNeeded = false
				jsonResultStarted = *jsonOutput
				return writeDemoResult(output, *jsonOutput, demoResult{
					Status: "complete", Network: "devnet",
					InputLamports: action.Result.AmountLamports,
					InputAmount:   action.Result.InputAmount,
					InputAsset:    action.Result.InputAsset,
					OutputAsset:   action.Result.OutputAsset,
					MinimumOutput: action.Result.MinimumOutput,
					OutputAmount:  action.Result.OutputAmount,
					Signature:     action.Result.Signature,
					Verdict:       action.Result.Verdict,
					ObservedAt:    action.ObservedAt.UTC(), Control: "stopped",
				})
			case "failed", "halted", "canceled":
				return fmt.Errorf("Devnet trade ended with status %s", action.Result.Decision)
			}
		}
		if err := demoWait(waitCtx, time.Second); err != nil {
			return errors.New("timed out waiting for the Devnet trade")
		}
	}
}

func demoPhase(view operatorstatus.View, previousAction string) (string, string) {
	action := view.LastAction.Result
	if action.ActionID != "" && action.ActionID != previousAction {
		switch action.Decision {
		case "pending":
			return "confirming", "Trade submitted. Waiting for final confirmation…"
		case "executing":
			return "executing", "The trade is being prepared and checked…"
		}
	}
	result := view.Result
	switch result.Decision {
	case "pending":
		return "confirming", "Trade submitted. Waiting for final confirmation…"
	case "executing":
		return "executing", "The trade is being prepared and checked…"
	case "waiting":
		if result.PriceTrigger != nil &&
			(!result.PriceTrigger.Available || !result.PriceTrigger.ConditionMet) {
			return "price", "Waiting for the configured SOL price condition…"
		}
		return "ready", "The condition is ready. Waiting for the trade cycle…"
	default:
		return "", ""
	}
}

// demoNotReadyReason names the specific condition that is unmet, and the
// command that fixes it. The demonstration arms an action and waits for the
// runner to execute it, so "not ready" most often means the runner simply is
// not running — and saying only "check status" leaves an operator with no next
// step at the exact moment they need one.
func demoNotReadyReason(view operatorstatus.View) string {
	switch {
	case view.RunnerState == "not_started":
		return "the swap runner is not running. Start it first, in another " +
			"terminal or as a service:\n  mithril-agent swap run --config PATH\n" +
			"then run the demonstration again"
	case view.RunnerState != "recent" || view.Stale:
		return "the swap runner has not reported recently; check it is still " +
			"running, then retry"
	case view.AttentionRequired:
		return "the agent is waiting for a human: resolve the outstanding " +
			"action with mithril-agent swap acknowledge, then retry"
	case view.Control.Mode != control.ModeNoNewActions:
		return "the agent is already armed; wait for it to finish and return " +
			"to stopped, or stop it with mithril-agent swap stop"
	case view.Journal.SendStartedRecords != 0 || view.Journal.SubmittedRecords != 0 ||
		view.Result.Submitted || view.LastAction.Result.Submitted:
		return "this configuration has already performed an action. A " +
			"demonstration runs once against a fresh setup; create a new one " +
			"with mithril-agent setup"
	default:
		return "the swap runner is not ready; run mithril-agent swap status " +
			"--config PATH to see why"
	}
}

func demoReady(view operatorstatus.View) bool {
	if view.RunnerState != "recent" || view.Stale || view.AttentionRequired ||
		view.Control.Mode != control.ModeNoNewActions ||
		view.Control.MaxActions != 0 || view.Control.RemainingActions != 0 ||
		view.Control.TerminalActionID != "" || view.Control.TerminalOutcome != "" ||
		view.Journal.SendStartedRecords != 0 || view.Journal.SubmittedRecords != 0 ||
		view.Result.Submitted || view.LastAction.Result.Submitted {
		return false
	}
	switch view.Result.Decision {
	case "", "stopped", "waiting", "skipped":
		return true
	default:
		return false
	}
}

func validDemoCompletion(profile string, result operatorstatus.Result) bool {
	if result.Verdict != "finalized" || !result.Submitted || result.Signature == "" ||
		result.InputAmount == 0 || result.MinimumOutput == 0 ||
		result.OutputAmount < result.MinimumOutput {
		return false
	}
	switch profile {
	case orcaswap.ProfileName:
		return result.InputAsset == "SOL" && result.OutputAsset == "devUSDC" &&
			result.AmountLamports == result.InputAmount
	case orcaswap.BuyProfileName:
		return result.InputAsset == "devUSDC" && result.OutputAsset == "SOL" &&
			result.AmountLamports == 0
	default:
		return false
	}
}

func writeDemoProgress(output io.Writer, jsonOutput bool, message string) error {
	if jsonOutput {
		return nil
	}
	_, err := fmt.Fprintln(output, message)
	return err
}

func explainDemoCheckError(err error) error {
	message := err.Error()
	switch {
	// Matched on the sentinel, not the sentence: this used to compare strings,
	// so rewording the error would have quietly dropped the one hint that names
	// the fix.
	case errors.Is(err, txflow.ErrNodeUnavailable):
		return fmt.Errorf("%w; verify MITHRIL_AGENT_MITHRIL_RPC_URL points to the live loopback RPC", err)
	case strings.Contains(message, "quote"):
		return fmt.Errorf("%w; verify the quote service is running and its socket is available", err)
	case strings.Contains(message, "evidence") || strings.Contains(message, "provider"):
		return fmt.Errorf("%w; verify both independent evidence providers are configured and reachable", err)
	default:
		return err
	}
}

func writeDemoResult(output io.Writer, jsonOutput bool, result demoResult) error {
	if jsonOutput {
		return json.NewEncoder(output).Encode(result)
	}
	input := operatorstatus.FormatAmount(result.InputLamports, "SOL")
	if result.InputAmount != 0 && result.InputAsset != "" {
		input = operatorstatus.FormatAmount(result.InputAmount, result.InputAsset)
	}
	_, err := fmt.Fprintf(output,
		"✅ Devnet trade complete\nInput: %s\nOutput: %s (minimum %s)\nSignature: %s\nExplorer: https://explorer.solana.com/tx/%s?cluster=devnet\nTime: %s\nControl: stopped\n",
		input,
		operatorstatus.FormatAmount(result.OutputAmount, result.OutputAsset),
		operatorstatus.FormatAmount(result.MinimumOutput, result.OutputAsset),
		result.Signature,
		result.Signature,
		result.ObservedAt.Format("2006-01-02 15:04:05 UTC"),
	)
	return err
}

func writeDemoFailure(output io.Writer, err error, errorCode string) error {
	controlState := "unknown"
	message := err.Error()
	switch {
	case strings.Contains(message, "stopping new actions"):
		controlState = "attention_required"
	case strings.Contains(message, "new actions were stopped"):
		controlState = "stopped"
	}
	if errorCode == "" {
		errorCode = demoFailureCode(message)
	}
	return json.NewEncoder(output).Encode(demoFailure{
		Status: "failed", Network: "devnet",
		ErrorCode: errorCode, Control: controlState,
	})
}

func demoJSONRequested(args []string) bool {
	enabled := false
	for _, arg := range args {
		if arg == "--json" {
			enabled = true
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--json="); ok {
			parsed, err := strconv.ParseBool(value)
			if err == nil {
				enabled = parsed
			}
		}
	}
	return enabled
}

func demoFailureCode(message string) string {
	switch {
	case strings.Contains(message, "stopping new actions"):
		return "stop_failed"
	case strings.Contains(message, "timed out"):
		return "timeout"
	case strings.Contains(message, "check failed") || strings.Contains(message, "Devnet check"):
		return "check_failed"
	case strings.Contains(message, "runner is not ready"):
		return "runner_not_ready"
	case strings.Contains(message, "enable one Devnet action"):
		return "enable_failed"
	case strings.Contains(message, "status"):
		return "status_failed"
	default:
		return "demo_failed"
	}
}

func waitForDemoPoll(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
