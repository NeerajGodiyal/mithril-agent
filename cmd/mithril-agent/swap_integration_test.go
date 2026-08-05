package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/internal/clockcheck"
	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/swaprun"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func TestSwapRunComposesSetupProcessesAndRPCs(t *testing.T) {
	if testing.Short() {
		t.Skip("builds command processes")
	}
	realOperatingSystem := preflightOperatingSystem
	preflightOperatingSystem = "linux"
	t.Cleanup(func() { preflightOperatingSystem = realOperatingSystem })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	agentCommand := filepath.Join(bin, "mithril-agent")
	mcpTestCommand := filepath.Join(bin, "mcpobserve.test")
	if prebuilt := os.Getenv("MITHRIL_AGENT_INTEGRATION_BIN_DIR"); prebuilt != "" {
		agentCommand = prebuiltIntegrationCommand(t, prebuilt, "mithril-agent")
		mcpTestCommand = prebuiltIntegrationCommand(t, prebuilt, "mcpobserve.test")
	} else {
		repository, err := filepath.Abs("../..")
		if err != nil {
			t.Fatal(err)
		}
		policyCommand := filepath.Join(bin, "mithril-agent-policy")
		signerCommand := filepath.Join(bin, "mithril-agent-signer")
		submitterCommand := filepath.Join(bin, "mithril-agent-submitter")
		buildCommand(t, repository, agentCommand, "go", "build", "-o", agentCommand, "./cmd/mithril-agent")
		buildCommand(t, repository, policyCommand, "go", "build", "-o", policyCommand, "./cmd/mithril-agent-policy")
		buildCommand(t, repository, signerCommand, "go", "build", "-o", signerCommand, "./cmd/mithril-agent-signer")
		buildCommand(t, repository, submitterCommand, "go", "build", "-o", submitterCommand, "./cmd/mithril-agent-submitter")
		buildCommand(t, repository, mcpTestCommand, "go", "test", "-c", "-o", mcpTestCommand, "./mcpobserve")
	}

	testCommand, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	mithrilCommand := filepath.Join(bin, "mithril-test")
	quoteCommand := filepath.Join(bin, "quote-test")
	writeExecWrapper(t, mithrilCommand, "", mcpTestCommand, "-test.run=^TestMCPHelperProcess$")
	writeExecWrapper(
		t,
		quoteCommand,
		"MITHRIL_AGENT_SWAP_QUOTE_HELPER=1",
		testCommand,
		"-test.run=^TestSwapQuoteHelperProcess$",
	)
	quoteScript := filepath.Join(root, "quote.mjs")
	if err := os.WriteFile(quoteScript, []byte("// integration adapter placeholder\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mithrilConfig := filepath.Join(root, "mithril.toml")
	if err := os.WriteFile(mithrilConfig, []byte("[network]\ncluster = 'devnet'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	walletSeed := [32]byte{1, 3, 3, 7}
	walletKey := ed25519.NewKeyFromSeed(walletSeed[:])
	walletPath := filepath.Join(root, "wallet.json")
	writeKeypair(t, walletPath, walletKey)

	rpc := &swapIntegrationRPC{t: t, blockhash: solana.Encode(bytes.Repeat([]byte{8}, 32))}
	mithrilNode := httptest.NewServer(rpc.handler("mithril"))
	defer mithrilNode.Close()
	primary := httptest.NewTLSServer(rpc.handler("primary"))
	defer primary.Close()
	secondary := httptest.NewTLSServer(rpc.handler("secondary"))
	defer secondary.Close()
	trustTestServers(t, primary, secondary)
	t.Setenv("MITHRIL_AGENT_MITHRIL_RPC_URL", mithrilNode.URL)
	t.Setenv("MITHRIL_AGENT_QUOTE_RPC_URL", primary.URL)
	t.Setenv("MITHRIL_AGENT_PRIMARY_RPC_URL", primary.URL)
	t.Setenv("MITHRIL_AGENT_SECONDARY_RPC_URL", secondary.URL)
	t.Setenv("MITHRIL_MCP_PROFILE", "integration-test-helper")
	t.Setenv("MITHRIL_RPC_URL", "")

	previousExecutable := swapSetupExecutable
	swapSetupExecutable = func() (string, error) { return agentCommand, nil }
	t.Cleanup(func() { swapSetupExecutable = previousExecutable })
	setupDirectory := filepath.Join(root, "agent")
	var setupOutput bytes.Buffer
	if err := runContext(t.Context(), []string{
		"swap", "setup",
		"--dir", setupDirectory,
		"--wallet-keypair", walletPath,
		"--mithril-command", mithrilCommand,
		"--mithril-config", mithrilConfig,
		"--node-command", quoteCommand,
		"--quote-script", quoteScript,
		"--input-lamports", "10",
		"--reserve-lamports", "100",
		"--max-fee-lamports", "5",
		"--confirm-min-output-amount", "99",
		"--primary-trust-domain", "provider-one",
		"--secondary-trust-domain", "provider-two",
	}, &setupOutput); err != nil {
		t.Fatal(err)
	}
	var setup swapSetupResult
	if err := json.Unmarshal(setupOutput.Bytes(), &setup); err != nil {
		t.Fatal(err)
	}
	if setup.Status != "configured" || setup.InputLamports != 10 || setup.MinimumOutput != 99 {
		t.Fatalf("setup result = %+v", setup)
	}
	cfg, err := readSwapConfig(setup.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	realClockSample := preflightClockSample
	realRuntimeClockSample := clockCheckSample
	testClockSample := func() (clockcheck.Sample, error) {
		sample := validPreflightClockSample(uint64(cfg.Swap.ClockUncertaintyLimit()))
		sample.MonotonicNanos = uint64(sample.WallTime.UnixNano())
		return sample, nil
	}
	preflightClockSample = testClockSample
	clockCheckSample = testClockSample
	t.Cleanup(func() {
		preflightClockSample = realClockSample
		clockCheckSample = realRuntimeClockSample
	})
	rpc.setPolicy(cfg.Swap.Route)
	if summary := checkPreflight(setup.ConfigPath); summary.Status != preflightOK {
		t.Fatalf("preflight summary = %+v", summary)
	}
	var checkOutput bytes.Buffer
	if err := runContext(t.Context(), []string{
		"check", "--config", setup.ConfigPath,
	}, &checkOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(checkOutput.String(), `"status":"ready"`) ||
		!strings.Contains(checkOutput.String(), `"direction":"sell"`) ||
		!strings.Contains(checkOutput.String(), `"input_asset":"SOL"`) ||
		!strings.Contains(checkOutput.String(), `"output_asset":"devUSDC"`) ||
		!strings.Contains(checkOutput.String(), `"input_amount_base_units":`) ||
		!strings.Contains(checkOutput.String(), `"slippage_bps":`) ||
		!strings.Contains(checkOutput.String(), `"daily_debit_cap_lamports":`) ||
		strings.Contains(checkOutput.String(), `"daily_input_token_cap":`) ||
		rpc.submitCount() != 0 {
		t.Fatalf("read-only check = %s, submissions=%d", checkOutput.String(), rpc.submitCount())
	}
	assertSwapCheckRPCRouting(t, rpc.methodCounts())
	rpc.resetMethodCounts()
	rpc.lastValidBlockHeight = 301
	var failedCheckOutput bytes.Buffer
	if err := runContext(t.Context(), []string{
		"check", "--config", setup.ConfigPath,
	}, &failedCheckOutput); err == nil || !strings.Contains(err.Error(), "validity window") {
		t.Fatalf("out-of-policy blockhash window error = %v", err)
	}
	var failedCheck struct {
		Status string `json:"status"`
		Stage  string `json:"stage"`
	}
	if err := json.Unmarshal(failedCheckOutput.Bytes(), &failedCheck); err != nil {
		t.Fatalf("decode failed check result: %v", err)
	}
	if failedCheck.Status != "failed" || failedCheck.Stage != "block_height" {
		t.Fatalf("failed check result = %+v", failedCheck)
	}
	if rpc.submitCount() != 0 {
		t.Fatal("read-only blockhash-window rejection submitted a transaction")
	}
	rpc.lastValidBlockHeight = 250
	rpc.resetMethodCounts()
	var signerPolicy signer.Policy
	if err := readStrictJSON(cfg.Signer.PolicyPath, &signerPolicy); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(signerPolicy.AuthorizationLedgerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only check invoked signer: %v", err)
	}

	seedSwapObservation(t, cfg)
	loopCtx, cancelLoop := context.WithCancel(t.Context())
	loopDone := make(chan error, 1)
	var loopOutput lockedBuffer
	go func() {
		loopDone <- runContext(loopCtx, []string{
			"swap", "run", "--config", setup.ConfigPath,
			"--interval", "1s", "--metrics-address", "127.0.0.1:0",
		}, &loopOutput)
	}()
	waitForSwapStatus(t, cfg, 10*time.Second, func(view operatorstatus.View) bool {
		return view.RunnerState == "recent" && view.Result.Decision == "stopped"
	})
	var enableOutput bytes.Buffer
	if err := runContext(t.Context(), []string{
		"swap", "enable", "--config", setup.ConfigPath,
		"--duration", "1m", "--max-actions", "1", "--reason", "integration test",
	}, &enableOutput); err != nil {
		cancelLoop()
		<-loopDone
		t.Fatal(err)
	}
	if !strings.Contains(enableOutput.String(), `"max_actions":1`) {
		t.Fatalf("enable output = %s", enableOutput.String())
	}
	view := waitForSwapStatus(t, cfg, 20*time.Second, func(view operatorstatus.View) bool {
		return view.LastAction.Result.Decision == "complete" &&
			view.LastAction.Result.Verdict == txflow.VerdictFinalized &&
			view.Control.Mode == control.ModeNoNewActions
	})
	if view.LastAction.Result.AmountLamports != 10 ||
		view.LastAction.Result.MinimumOutput != 99 ||
		view.LastAction.Result.OutputAmount < 99 ||
		view.LastAction.Result.Signature == "" || !view.LastAction.Result.Submitted ||
		view.LastAction.Result.Recovered {
		t.Fatalf("completed status = %+v", view)
	}
	cancelLoop()
	if err := <-loopDone; err != nil {
		t.Fatal(err)
	}
	if rpc.submitCount() != 1 {
		t.Fatalf("submissions = %d, want 1", rpc.submitCount())
	}
	assertSwapRPCRouting(t, rpc.methodCounts())
	assertSwapJournal(t, cfg.Journal.Path)
	authorizations, err := journal.Open(signerPolicy.AuthorizationLedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(authorizations.Records()); got != 2 {
		_ = authorizations.Close()
		t.Fatalf("authorization records = %d, want header plus one reservation", got)
	}
	var reservation struct {
		DebitLamports      uint64 `json:"debit_lamports"`
		ExtraDebitLamports uint64 `json:"extra_debit_lamports"`
	}
	if err := json.Unmarshal(authorizations.Records()[1].Payload, &reservation); err != nil {
		_ = authorizations.Close()
		t.Fatal(err)
	}
	if reservation.ExtraDebitLamports != orcaswap.DefaultMaxOutputAccountRentLamports ||
		reservation.DebitLamports != 10+5+orcaswap.DefaultMaxOutputAccountRentLamports {
		_ = authorizations.Close()
		t.Fatalf("authorization debit = %+v", reservation)
	}
	if err := authorizations.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(loopOutput.String(), "http://") ||
		strings.Contains(loopOutput.String(), "https://") {
		t.Fatal("runner output exposed an endpoint")
	}
}

func TestSwapCheckReportsArgumentAndConfigurationStages(t *testing.T) {
	for _, test := range []struct {
		name      string
		args      []string
		wantStage string
	}{
		{name: "arguments", args: []string{"check"}, wantStage: "arguments"},
		{name: "configuration", args: []string{
			"check", "--config", filepath.Join(t.TempDir(), "missing.json"),
		}, wantStage: "configuration"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			err := runContext(t.Context(), test.args, &output)
			if err == nil {
				t.Fatal("check accepted invalid input")
			}
			var failure struct {
				Status string `json:"status"`
				Stage  string `json:"stage"`
			}
			if decodeErr := json.Unmarshal(output.Bytes(), &failure); decodeErr != nil {
				t.Fatalf("JSON failure output = %q: %v", output.String(), decodeErr)
			}
			if failure.Status != "failed" || failure.Stage != test.wantStage ||
				strings.Contains(output.String(), "missing.json") {
				t.Fatalf("JSON failure = %+v, output = %q", failure, output.String())
			}
		})
	}
}

func TestCheckPolicyUsesDirectionSpecificBaseUnitsAndCaps(t *testing.T) {
	sell := checkPolicy(swaprun.Profile{
		InputLamports: 11, DailyDebitCapLamports: 22,
	})
	if sell.InputAmountBaseUnits != 11 || sell.DailyDebitCapLamports != 22 ||
		sell.DailyInputTokenCap != 0 || sell.DailyNativeFeeCapLamports != 0 {
		t.Fatalf("sell policy = %+v", sell)
	}

	buy := checkPolicy(swaprun.Profile{
		Name: orcaswap.BuyProfileName, Version: orcaswap.BuyProfileVersion,
		InputTokenAmount:   33,
		DailyInputTokenCap: 44, DailyNativeFeeCapLamports: 55,
	})
	if buy.InputAmountBaseUnits != 33 || buy.DailyDebitCapLamports != 0 ||
		buy.DailyInputTokenCap != 44 || buy.DailyNativeFeeCapLamports != 55 {
		t.Fatalf("buy policy = %+v", buy)
	}
}

func prebuiltIntegrationCommand(t *testing.T, directory, name string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("prebuilt integration command %q is not executable", name)
	}
	return path
}

func TestSwapQuoteHelperProcess(_ *testing.T) {
	if os.Getenv("MITHRIL_AGENT_SWAP_QUOTE_HELPER") != "1" {
		return
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 8<<10))
	if err != nil {
		os.Exit(2)
	}
	var request struct {
		Owner       string `json:"owner"`
		Pool        string `json:"pool"`
		InputMint   string `json:"input_mint"`
		InputAmount string `json:"input_amount"`
		SlippageBPS uint16 `json:"slippage_bps"`
	}
	if json.Unmarshal(input, &request) != nil || request.Pool != orcaswap.DevnetPool ||
		request.InputMint != orcaswap.WrappedSOLMint || request.SlippageBPS != 100 {
		os.Exit(2)
	}
	amount, err := strconv.ParseUint(request.InputAmount, 10, 64)
	if err != nil || amount != 10 {
		os.Exit(2)
	}
	instructions := swapIntegrationInstructions(testSwapProfile(request.Owner).Route, amount, 99)
	type wireInstruction struct {
		Program    string               `json:"program"`
		Accounts   []solana.AccountMeta `json:"accounts"`
		DataBase64 string               `json:"data_base64"`
	}
	wire := make([]wireInstruction, len(instructions))
	for index, instruction := range instructions {
		wire[index] = wireInstruction{
			Program: instruction.Program, Accounts: instruction.Accounts,
			DataBase64: base64.StdEncoding.EncodeToString(instruction.Data),
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"instructions": wire, "token_in": request.InputAmount,
		"token_est_out": "100", "token_min_out": "99",
		"trade_enable_timestamp": "0",
	})
	os.Exit(0)
}

func writeExecWrapper(t *testing.T, path, environment, target string, args ...string) {
	t.Helper()
	parts := []string{"exec", shellQuote(target)}
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	content := "#!/bin/sh\n"
	if environment != "" {
		content += "export " + environment + "\n"
	}
	content += strings.Join(parts, " ") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.Buffer.Write(data)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.Buffer.String()
}

func seedSwapObservation(t *testing.T, cfg config) {
	t.Helper()
	actionID, err := cfg.Swap.ActionID(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-6 * time.Second)
	store, err := journal.Open(cfg.Journal.Path)
	if err != nil {
		t.Fatal(err)
	}
	_, appendErr := store.Append(at, swaprun.EventObserved, actionID, agent.NodeObservation{
		Account: agent.Observation{
			Cluster: cfg.Swap.Cluster, Source: cfg.Swap.Route.Owner,
			BalanceLamports: 4_000_000, Slot: 455, ObservedAt: at,
			EvidenceSource: "mithril_mcp", Finality: "local_unfinalized",
			Consistency: "node_reported_non_atomic",
		},
		Health: agent.NodeHealth{
			Status: "healthy", AssessmentScope: "point_in_time_snapshot",
			ObservedAt: at, EvidenceComplete: true,
			CrossCheck: &agent.SlotComparison{
				MithrilSlot: 455, ReferenceSlot: 455, ReferenceCommitment: "confirmed",
				MithrilView: "local_unfinalized_head", Threshold: 150, Status: "in_sync",
			},
		},
	})
	closeErr := store.Close()
	if appendErr != nil {
		t.Fatal(appendErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func waitForSwapStatus(
	t *testing.T,
	cfg config,
	timeout time.Duration,
	ready func(operatorstatus.View) bool,
) operatorstatus.View {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastView operatorstatus.View
	var lastErr error
	for time.Now().Before(deadline) {
		fingerprint, err := cfg.Swap.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		state, err := control.NewStateFile(cfg.Control.StatePath, fingerprint, false)
		lastErr = err
		if err == nil {
			status, statusErr := state.Status()
			lastErr = statusErr
			if statusErr == nil {
				view, viewErr := operatorstatus.CurrentView(
					operatorstatus.Path(cfg.Journal.Path), cfg.Swap.Name,
					cfg.Swap.Cluster, cfg.Swap.Version, status, time.Now().UTC(),
				)
				lastView = view
				lastErr = viewErr
				if viewErr == nil && ready(view) {
					return view
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for swap runner status: last=%+v error=%v", lastView, lastErr)
	return operatorstatus.View{}
}

func assertSwapJournal(t *testing.T, path string) {
	t.Helper()
	store, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	counts := map[string]int{}
	for _, record := range store.Records() {
		counts[record.Type]++
	}
	for _, event := range []string{
		swaprun.EventStarted, swaprun.EventBuilt, swaprun.EventSimulated,
		swaprun.EventSigned, swaprun.EventPreSendObserved, swaprun.EventSendStarted,
		swaprun.EventSubmitted, swaprun.EventReconciled,
	} {
		if counts[event] != 1 {
			t.Fatalf("%s records = %d, want 1", event, counts[event])
		}
	}
	if counts[swaprun.EventObserved] != 2 || counts[swaprun.EventCanceled] != 0 ||
		counts[swaprun.EventObservationFailed] != 0 {
		t.Fatalf("observation/cancellation records = %+v", counts)
	}
}

type swapIntegrationRPC struct {
	t                    *testing.T
	blockhash            string
	blockHeight          uint64
	lastValidBlockHeight uint64
	mu                   sync.Mutex
	policy               orcaswap.Policy
	signature            string
	tx                   []byte
	submits              int
	methods              map[string]map[string]int
}

func (rpc *swapIntegrationRPC) setPolicy(policy orcaswap.Policy) {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	rpc.policy = policy
}

func (rpc *swapIntegrationRPC) handler(origin string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rpc.serve(origin, writer, request)
	})
}

func (rpc *swapIntegrationRPC) serve(
	origin string,
	writer http.ResponseWriter,
	request *http.Request,
) {
	var input struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	rpc.recordMethod(origin, input.Method)
	var result any
	switch input.Method {
	case "getGenesisHash":
		result = solana.DevnetGenesisHash
	case "getLatestBlockhash":
		lastValidBlockHeight := rpc.lastValidBlockHeight
		if lastValidBlockHeight == 0 {
			lastValidBlockHeight = 250
		}
		result = map[string]any{
			"context": map[string]any{"slot": uint64(460)},
			"value": map[string]any{
				"blockhash": rpc.blockhash, "lastValidBlockHeight": lastValidBlockHeight,
			},
		}
	case "getBlockHeight":
		blockHeight := rpc.blockHeight
		if blockHeight == 0 {
			blockHeight = 100
		}
		result = blockHeight
	case "getFeeForMessage":
		result = map[string]any{
			"context": map[string]any{"slot": uint64(460)}, "value": uint64(5),
		}
	case "getMinimumBalanceForRentExemption":
		result = uint64(2_039_280)
	case "getAccountInfo":
		result = rpc.deploymentAccount(input.Params)
	case "simulateTransaction":
		rpc.validateSimulation(input.Params)
		result = map[string]any{
			"context": map[string]any{"slot": uint64(460)},
			"value": map[string]any{
				"err": nil, "unitsConsumed": uint64(500), "logs": []string{"success"},
			},
		}
	case "sendTransaction":
		result = rpc.acceptTransaction(input.Params)
	case "getSignatureStatuses":
		result = map[string]any{"value": []any{rpc.finalizedStatus(input.Params)}}
	case "getTransaction":
		result = rpc.finalizedTransaction(input.Params)
	default:
		rpc.t.Errorf("unexpected swap RPC method %q", input.Method)
		http.Error(writer, "unexpected method", http.StatusBadRequest)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(map[string]any{
		"jsonrpc": "2.0", "id": input.ID, "result": result,
	}); err != nil {
		rpc.t.Errorf("encode RPC response: %v", err)
	}
}

func (rpc *swapIntegrationRPC) deploymentAccount(params json.RawMessage) any {
	address := firstRPCString(rpc.t, params)
	var data []byte
	executable := false
	switch address {
	case orcaswap.WhirlpoolProgram:
		data = make([]byte, 36)
		binary.LittleEndian.PutUint32(data[:4], 2)
		programData, _ := solana.Decode32(orcaswap.WhirlpoolProgramData)
		copy(data[4:], programData[:])
		executable = true
	case orcaswap.WhirlpoolProgramData:
		data = make([]byte, 45)
		binary.LittleEndian.PutUint32(data[:4], 3)
		binary.LittleEndian.PutUint64(data[4:12], orcaswap.WhirlpoolDeploySlot)
		data[12] = 1
		authority, _ := solana.Decode32(orcaswap.WhirlpoolUpgradeAuth)
		copy(data[13:], authority[:])
	default:
		rpc.t.Errorf("unexpected deployment account %q", address)
		return nil
	}
	return map[string]any{
		"context": map[string]any{"slot": uint64(460)},
		"value": map[string]any{
			"data":       []any{base64.StdEncoding.EncodeToString(data), "base64"},
			"executable": executable, "lamports": uint64(1),
			"owner": orcaswap.UpgradeableLoader,
		},
	}
}

func (rpc *swapIntegrationRPC) validateSimulation(params json.RawMessage) {
	encoded := firstRPCString(rpc.t, params)
	transaction, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(transaction) < 65 || transaction[0] != 1 ||
		!bytes.Equal(transaction[1:65], make([]byte, 64)) {
		rpc.t.Errorf("invalid legacy simulation transaction")
		return
	}
	rpc.mu.Lock()
	policy := rpc.policy
	rpc.mu.Unlock()
	if _, err := orcaswap.DecodeMessage(policy, transaction[65:]); err != nil {
		rpc.t.Errorf("invalid simulated Orca message: %v", err)
	}
}

func (rpc *swapIntegrationRPC) acceptTransaction(params json.RawMessage) string {
	encoded := firstRPCString(rpc.t, params)
	transaction, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		rpc.t.Errorf("invalid submitted transaction")
		return ""
	}
	decoded, err := solana.DecodeSignedLegacyTransaction(transaction)
	if err != nil {
		rpc.t.Errorf("invalid signed Orca transaction: %v", err)
		return ""
	}
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	intent, err := orcaswap.DecodeMessage(rpc.policy, decoded.Message.Raw)
	if err != nil || intent.InputAmount != 10 || intent.MinimumOutput != 99 {
		rpc.t.Errorf("submitted Orca intent = %+v, %v", intent, err)
		return ""
	}
	rpc.signature = solana.Encode(decoded.Signature[:])
	rpc.tx = bytes.Clone(transaction)
	rpc.submits++
	return rpc.signature
}

func (rpc *swapIntegrationRPC) finalizedStatus(params json.RawMessage) any {
	signature := firstSignature(rpc.t, params)
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	if signature == "" || signature != rpc.signature {
		return nil
	}
	return map[string]any{
		"slot": uint64(470), "err": nil, "confirmationStatus": "finalized",
	}
}

func (rpc *swapIntegrationRPC) finalizedTransaction(params json.RawMessage) any {
	var values []json.RawMessage
	if json.Unmarshal(params, &values) != nil || len(values) != 2 {
		return nil
	}
	var signature string
	if json.Unmarshal(values[0], &signature) != nil {
		return nil
	}
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	if signature != rpc.signature || len(rpc.tx) == 0 {
		return nil
	}
	decoded, err := solana.DecodeSignedLegacyTransaction(rpc.tx)
	if err != nil {
		return nil
	}
	inputIndex := -1
	outputIndex := -1
	for index, key := range decoded.Message.AccountKeys {
		switch solana.Encode(key[:]) {
		case rpc.policy.InputTokenAccount:
			inputIndex = index
		case rpc.policy.OutputTokenAccount:
			outputIndex = index
		}
		if inputIndex >= 0 && outputIndex >= 0 {
			break
		}
	}
	if inputIndex < 0 || outputIndex < 0 {
		return nil
	}
	pre := make([]uint64, len(decoded.Message.AccountKeys))
	post := make([]uint64, len(decoded.Message.AccountKeys))
	for index := range pre {
		pre[index] = uint64(10_000_000 + index)
		post[index] = pre[index]
	}
	pre[outputIndex] = 0
	post[outputIndex] = 2_039_280
	post[inputIndex] = 0
	post[0] += pre[inputIndex]
	post[0] -= 5 + 10 + 2_039_280
	token := func(amount string) map[string]any {
		return map[string]any{
			"accountIndex": uint16(outputIndex), "mint": rpc.policy.OutputMint,
			"owner": rpc.policy.Owner, "uiTokenAmount": map[string]any{"amount": amount},
		}
	}
	return map[string]any{
		"slot": uint64(470),
		"meta": map[string]any{
			"err": nil, "fee": uint64(5), "preBalances": pre, "postBalances": post,
			"preTokenBalances":  []any{},
			"postTokenBalances": []any{token("99")},
		},
		"transaction": []any{base64.StdEncoding.EncodeToString(rpc.tx), "base64"},
		"version":     "legacy",
	}
}

func (rpc *swapIntegrationRPC) submitCount() int {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	return rpc.submits
}

func (rpc *swapIntegrationRPC) recordMethod(origin, method string) {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	if rpc.methods == nil {
		rpc.methods = make(map[string]map[string]int)
	}
	if rpc.methods[origin] == nil {
		rpc.methods[origin] = make(map[string]int)
	}
	rpc.methods[origin][method]++
}

func (rpc *swapIntegrationRPC) methodCounts() map[string]map[string]int {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	counts := make(map[string]map[string]int, len(rpc.methods))
	for origin, methods := range rpc.methods {
		counts[origin] = make(map[string]int, len(methods))
		for method, count := range methods {
			counts[origin][method] = count
		}
	}
	return counts
}

func (rpc *swapIntegrationRPC) resetMethodCounts() {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	rpc.methods = nil
}

func assertSwapCheckRPCRouting(t *testing.T, counts map[string]map[string]int) {
	t.Helper()
	mithril := counts["mithril"]
	if mithril["getBlockHeight"] != 1 || mithril["simulateTransaction"] != 1 ||
		mithril["sendTransaction"] != 0 {
		t.Fatalf("read-only Mithril calls = %+v", mithril)
	}
	assertNoRPCMethods(t, "Mithril", mithril,
		"getFeeForMessage", "getSignatureStatuses", "getTransaction")
	for _, origin := range []string{"primary", "secondary"} {
		methods := counts[origin]
		for _, method := range []string{
			"getGenesisHash", "getAccountInfo", "getMinimumBalanceForRentExemption",
			"getFeeForMessage",
		} {
			if methods[method] == 0 {
				t.Fatalf("%s provider did not receive %s: %+v", origin, method, methods)
			}
		}
		assertNoRPCMethods(t, origin, methods,
			"getLatestBlockhash", "getBlockHeight", "simulateTransaction", "sendTransaction")
	}
}

func assertSwapRPCRouting(t *testing.T, counts map[string]map[string]int) {
	t.Helper()
	mithril := counts["mithril"]
	if mithril["simulateTransaction"] != 1 || mithril["sendTransaction"] != 1 {
		t.Fatalf("Mithril simulation/submission calls = %+v", mithril)
	}
	assertNoRPCMethods(t, "Mithril", mithril,
		"getFeeForMessage", "getSignatureStatuses", "getTransaction")
	for _, origin := range []string{"primary", "secondary"} {
		methods := counts[origin]
		for _, method := range []string{
			"getGenesisHash", "getAccountInfo", "getMinimumBalanceForRentExemption",
			"getFeeForMessage", "getSignatureStatuses", "getTransaction",
		} {
			if methods[method] == 0 {
				t.Fatalf("%s provider did not receive %s: %+v", origin, method, methods)
			}
		}
		assertNoRPCMethods(t, origin, methods,
			"getLatestBlockhash", "getBlockHeight", "simulateTransaction", "sendTransaction")
	}
}

func assertNoRPCMethods(
	t *testing.T,
	origin string,
	counts map[string]int,
	methods ...string,
) {
	t.Helper()
	for _, method := range methods {
		if counts[method] != 0 {
			t.Fatalf("%s received disallowed %s call: %+v", origin, method, counts)
		}
	}
}

func firstRPCString(t *testing.T, params json.RawMessage) string {
	t.Helper()
	var values []json.RawMessage
	if json.Unmarshal(params, &values) != nil || len(values) == 0 {
		t.Errorf("invalid RPC parameters")
		return ""
	}
	var value string
	if json.Unmarshal(values[0], &value) != nil {
		t.Errorf("invalid RPC string parameter")
	}
	return value
}

func firstSignature(t *testing.T, params json.RawMessage) string {
	t.Helper()
	var values []json.RawMessage
	if json.Unmarshal(params, &values) != nil || len(values) == 0 {
		return ""
	}
	var signatures []string
	if json.Unmarshal(values[0], &signatures) != nil || len(signatures) != 1 {
		return ""
	}
	return signatures[0]
}

func swapIntegrationInstructions(
	policy orcaswap.Policy,
	input,
	minimum uint64,
) []solana.Instruction {
	ata := func(account, mint string) solana.Instruction {
		return solana.Instruction{
			Program: orcaswap.AssociatedTokenProgram,
			Accounts: []solana.AccountMeta{
				{Address: policy.Owner, Signer: true, Writable: true},
				{Address: account, Writable: true}, {Address: policy.Owner},
				{Address: mint}, {Address: orcaswap.SystemProgram},
				{Address: orcaswap.TokenProgram},
			}, Data: []byte{1},
		}
	}
	transfer := make([]byte, 12)
	binary.LittleEndian.PutUint32(transfer[:4], 2)
	binary.LittleEndian.PutUint64(transfer[4:], input)
	swap := make([]byte, 49)
	copy(swap, []byte{43, 4, 237, 11, 26, 201, 30, 98})
	binary.LittleEndian.PutUint64(swap[8:16], input)
	binary.LittleEndian.PutUint64(swap[16:24], minimum)
	copy(swap[40:], []byte{1, 1, 1, 1, 0, 0, 0, 6, 2})
	return []solana.Instruction{
		ata(policy.InputTokenAccount, policy.InputMint),
		ata(policy.OutputTokenAccount, policy.OutputMint),
		{Program: orcaswap.SystemProgram, Accounts: []solana.AccountMeta{
			{Address: policy.Owner, Signer: true, Writable: true},
			{Address: policy.InputTokenAccount, Writable: true},
		}, Data: transfer},
		{Program: orcaswap.TokenProgram, Accounts: []solana.AccountMeta{
			{Address: policy.InputTokenAccount, Writable: true},
		}, Data: []byte{17}},
		{Program: orcaswap.WhirlpoolProgram, Accounts: []solana.AccountMeta{
			{Address: orcaswap.TokenProgram}, {Address: orcaswap.TokenProgram},
			{Address: orcaswap.MemoProgram}, {Address: policy.Owner, Signer: true},
			{Address: policy.Pool, Writable: true}, {Address: policy.InputMint},
			{Address: policy.OutputMint}, {Address: policy.InputTokenAccount, Writable: true},
			{Address: policy.TokenVaultA, Writable: true},
			{Address: policy.OutputTokenAccount, Writable: true},
			{Address: policy.TokenVaultB, Writable: true},
			{Address: "7knZZ461yySGbSEHeBUwEpg3VtAkQy8B9tp78RGgyUHE", Writable: true},
			{Address: "CpoSFo3ajrizueggtJr2ZjvYgdtkgugXtvhqcwkyCkKP", Writable: true},
			{Address: "9iGzy4mQtJadZDuH8seBFQGiqcb6wyp2KW4M6NKHvsAW", Writable: true},
			{Address: policy.Oracle, Writable: true},
			{Address: "3aBJJLAR3QxGcGsesNXeW3f64Rv3TckF7EQ6sXtAuvGM", Writable: true},
			{Address: "A1vrG379E5ttoaWmyQBiunsMdyrpoUp7mSQwu8DgLcip", Writable: true},
		}, Data: swap},
		{Program: orcaswap.TokenProgram, Accounts: []solana.AccountMeta{
			{Address: policy.InputTokenAccount, Writable: true},
			{Address: policy.Owner, Writable: true}, {Address: policy.Owner, Signer: true},
		}, Data: []byte{9}},
	}
}
