package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/mcpobserve"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/policyclient"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/signerclient"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/submitterclient"
	"github.com/Overclock-Validator/mithril-agent/swapbuilder"
	"github.com/Overclock-Validator/mithril-agent/swaprun"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const swapUsage = `Usage:
  mithril-agent swap setup [options]
  mithril-agent swap bind-providers --config PATH --reason TEXT [--primary-trust-domain NAME] [--secondary-trust-domain NAME]
  mithril-agent swap plan --config PATH
  mithril-agent swap check --config PATH
  mithril-agent swap demo --config PATH [--timeout DURATION] [--json]
  mithril-agent swap run --config PATH [--interval DURATION] [--metrics-address ADDRESS]
                           [--quote-socket PATH]
  mithril-agent swap enable --config PATH --duration DURATION [--max-actions N] --reason TEXT
  mithril-agent swap stop --config PATH --reason TEXT
  mithril-agent swap acknowledge --config PATH --action-id SHA256 --outcome failed|halted --reason TEXT
  mithril-agent swap drain --config PATH [--timeout DURATION] --reason TEXT
  mithril-agent swap status --config PATH
  mithril-agent swap fingerprint --config PATH
  mithril-agent swap discover --direction sell|buy (--owner ADDRESS | --wallet-keypair PATH) --node-command PATH --quote-script PATH [amount options] [--slippage-bps N] [--floor-tolerance-bps N]
  mithril-agent swap challenge (--wallet PATH | --agent ADDRESS) --to ADDRESS [--json]`

const swapSetupUsage = `Usage: mithril-agent swap setup [options]

Required options:
  --dir PATH
  --wallet-keypair PATH
  --mithril-command PATH
  --node-command PATH
  --quote-script PATH
  --confirm-min-output-amount N
  --primary-trust-domain NAME
  --secondary-trust-domain NAME

Optional limits:
  --direction sell|buy
  --mithril-config PATH
  --quote-socket PATH
  --input-lamports N
  --spend-usdc AMOUNT
  --slippage-bps N
  --reserve-lamports N
  --max-fee-lamports N
  --daily-debit-cap-lamports N
  --daily-spend-usdc AMOUNT
  --daily-native-fee-cap-lamports N
  --schedule-window DURATION
  --sell-at-usd PRICE
  --buy-at-usd PRICE
  --floor-tolerance-bps N`

const swapStepTimeout = 2 * time.Minute

func runSwap(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, err := fmt.Fprintln(output, swapUsage)
		return err
	}
	switch args[0] {
	case "setup":
		return runSwapSetup(ctx, args[1:], output)
	case "bind-providers":
		return runSwapBindProviders(args[1:], output)
	case "plan":
		return runSwapPlan(args[1:], output)
	case "check":
		return runSwapCheck(ctx, args[1:], output)
	case "demo":
		return runSwapDemo(ctx, args[1:], output)
	case "run":
		return runSwapLoop(ctx, args[1:], output)
	case "enable":
		return runSwapEnable(args[1:], output)
	case "stop":
		return runSwapStop(args[1:], output)
	case "acknowledge":
		return runSwapAcknowledge(args[1:], output)
	case "drain":
		return runSwapDrain(ctx, args[1:], output)
	case "status":
		return runSwapStatus(args[1:], output)
	case "fingerprint":
		return runSwapFingerprint(args[1:], output)
	case "discover":
		return runSwapDiscover(ctx, args[1:], output)
	case "challenge":
		return runSweepChallenge(args[1:], output)
	default:
		return fmt.Errorf("unknown swap command %q; run mithril-agent swap --help", args[0])
	}
}

func runSwapDiscover(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("swap discover", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	owner := flags.String("owner", "", "wallet public key")
	walletKeypair := flags.String("wallet-keypair", "", "private Devnet wallet keypair")
	nodeCommand := flags.String("node-command", "", "absolute Node.js executable")
	quoteScript := flags.String("quote-script", "", "absolute Orca quote adapter")
	direction := flags.String("direction", "sell", "trade direction: sell or buy")
	inputLamports := flags.Uint64("input-lamports", defaultSwapInputLamports, "exact Devnet input amount")
	spendUSDC := flags.String("spend-usdc", "", "exact Devnet devUSDC amount")
	slippageBPS := flags.Uint("slippage-bps", 100, "maximum slippage in basis points")
	// Setup writes the floor this prints, so discovery has to apply the same
	// tolerance. Otherwise the operator confirms one number and a different,
	// looser one is signed into the policy.
	floorToleranceBPS := flags.Uint(
		"floor-tolerance-bps", 0,
		"how far below the quote the route floor may sit; must match swap setup",
	)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent swap discover --direction sell|buy (--owner ADDRESS | --wallet-keypair PATH) --node-command PATH --quote-script PATH [amount options] [--slippage-bps N] [--floor-tolerance-bps N]")
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || (*owner == "") == (*walletKeypair == "") ||
		*nodeCommand == "" || *quoteScript == "" {
		return errors.New("swap discover requires exactly one wallet source, --node-command, and --quote-script")
	}
	if *inputLamports == 0 {
		return errors.New("swap discover input amount must be positive")
	}
	if *direction != "sell" && *direction != "buy" {
		return errors.New("swap discover direction must be sell or buy")
	}
	explicit := make(map[string]bool)
	flags.Visit(func(item *flag.Flag) { explicit[item.Name] = true })
	var inputTokenAmount uint64
	if *direction == "buy" {
		if explicit["input-lamports"] {
			return errors.New("--input-lamports is only valid for sell discovery")
		}
		var err error
		inputTokenAmount, err = parseDecimalUnits(*spendUSDC, "devUSDC spend", ^uint64(0))
		if err != nil {
			return err
		}
	} else if explicit["spend-usdc"] {
		return errors.New("--spend-usdc is only valid for buy discovery")
	}
	if *slippageBPS == 0 || *slippageBPS > 500 {
		return errors.New("swap discover slippage must be between 1 and 500 basis points")
	}
	// Bound before the uint16 cast, so a typo cannot truncate into a real
	// concession. Must match the bound swap setup enforces.
	if *floorToleranceBPS > 2_000 {
		return errors.New("swap discover floor tolerance must be at most 2000 basis points")
	}
	discoveredOwner := *owner
	if *walletKeypair != "" {
		walletPath, err := cleanExistingPath(*walletKeypair)
		if err != nil || validatePrivateFile(walletPath) != nil {
			return errors.New("swap discover wallet keypair is not a protected private file")
		}
		walletKey, err := signer.LoadKeypair(walletPath)
		if err != nil {
			return errors.New("swap discover wallet keypair is invalid")
		}
		defer clear(walletKey)
		publicKey, ok := walletKey.Public().(ed25519.PublicKey)
		if !ok {
			return errors.New("swap discover wallet public key is invalid")
		}
		discoveredOwner = solana.Encode(publicKey)
	}
	if *direction == "buy" {
		policy, err := swapSetupDiscoverBuy(
			ctx, discoveredOwner, *nodeCommand, *quoteScript,
			inputTokenAmount, uint16(*slippageBPS),
		)
		if err != nil {
			return err
		}
		policy.MinOutputLamports, err = relaxRouteFloor(
			policy.MinOutputLamports, uint16(*floorToleranceBPS),
		)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(struct {
			Direction string               `json:"direction"`
			Spend     uint64               `json:"input_token_amount"`
			Route     orcaswap.BuyPolicyV2 `json:"route"`
		}{Direction: "buy", Spend: inputTokenAmount, Route: policy})
	}
	policy, err := swapSetupDiscover(
		ctx, discoveredOwner, *nodeCommand, *quoteScript,
		*inputLamports, uint16(*slippageBPS),
	)
	if err != nil {
		return err
	}
	policy.MinOutputAmount, err = relaxRouteFloor(
		policy.MinOutputAmount, uint16(*floorToleranceBPS),
	)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Direction string          `json:"direction"`
		Route     orcaswap.Policy `json:"route"`
	}{Direction: "sell", Route: policy})
}

func discoverSwapPolicy(
	ctx context.Context,
	owner,
	nodeCommand,
	quoteScript string,
	inputLamports uint64,
	slippageBPS uint16,
) (orcaswap.Policy, error) {
	quoteURL := os.Getenv("MITHRIL_AGENT_QUOTE_RPC_URL")
	client, err := swapbuilder.New(swapbuilder.Config{
		NodeCommand: nodeCommand, ScriptPath: quoteScript, RPCURL: quoteURL,
	})
	if err != nil {
		return orcaswap.Policy{}, err
	}
	request := swapbuilder.Request{
		Owner: owner, Pool: orcaswap.DevnetPool,
		InputMint:   orcaswap.WrappedSOLMint,
		InputAmount: inputLamports,
		SlippageBPS: slippageBPS,
	}
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := client.Quote(checkCtx, request)
	if err != nil {
		return orcaswap.Policy{}, err
	}
	policy, err := orcaswap.DiscoverPolicy(owner, orcaswap.Quote{
		InputAmount: result.TokenIn, EstimatedOutput: result.TokenEstOut,
		MinimumOutput: result.TokenMinOut, SlippageBPS: request.SlippageBPS,
	}, result.Instructions)
	if err != nil {
		return orcaswap.Policy{}, err
	}
	return policy, nil
}

func discoverBuyPolicy(
	ctx context.Context,
	owner,
	nodeCommand,
	quoteScript string,
	inputTokenAmount uint64,
	slippageBPS uint16,
) (orcaswap.BuyPolicyV2, error) {
	client, err := swapbuilder.New(swapbuilder.Config{
		NodeCommand: nodeCommand, ScriptPath: quoteScript,
		RPCURL: os.Getenv("MITHRIL_AGENT_QUOTE_RPC_URL"),
	})
	if err != nil {
		return orcaswap.BuyPolicyV2{}, err
	}
	request := swapbuilder.Request{
		Owner: owner, Pool: orcaswap.DevnetPool,
		InputMint: orcaswap.DevnetUSDCMint, InputAmount: inputTokenAmount,
		SlippageBPS: slippageBPS,
	}
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := client.Quote(checkCtx, request)
	if err != nil {
		return orcaswap.BuyPolicyV2{}, err
	}
	return orcaswap.DiscoverBuyPolicyV2(owner, orcaswap.Quote{
		InputAmount: result.TokenIn, EstimatedOutput: result.TokenEstOut,
		MinimumOutput: result.TokenMinOut, SlippageBPS: request.SlippageBPS,
	}, result.Instructions)
}

func swapConfigPath(command string, args []string, output io.Writer) (string, error) {
	flags := flag.NewFlagSet("swap "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent swap "+command+" --config PATH")
			return "", writeErr
		}
		return "", err
	}
	if flags.NArg() != 0 {
		return "", errors.New("swap " + command + " takes no positional arguments")
	}
	if *configPath == "" {
		// Read-only commands find what setup recorded, or the installed
		// configuration, so a reviewer never has to know a path.
		if found := discoverCurrentConfig(); found != "" {
			return found, nil
		}
		return "", errors.New("swap " + command +
			" found no configuration; run: mithril-agent setup")
	}
	return *configPath, nil
}

func readSwapConfig(path string) (config, error) {
	cfg, err := readConfig(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if cfg.Swap == nil {
		return cfg, errors.New("config has no swap profile")
	}
	if err := cfg.validateEvidenceTrustDomains(); err != nil {
		return cfg, err
	}
	if err := cfg.Swap.Validate(); err != nil {
		return cfg, fmt.Errorf("swap profile: %w", err)
	}
	return cfg, nil
}

func runSwapFingerprint(args []string, output io.Writer) error {
	path, err := swapConfigPath("fingerprint", args, output)
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	cfg, err := readSwapConfig(path)
	if err != nil {
		return err
	}
	fingerprint, err := cfg.Swap.Fingerprint()
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		ProfileSHA256 string `json:"profile_sha256"`
	}{ProfileSHA256: fingerprint})
}

// maxSwapActionsPerActivation matches the control state machine's own ceiling
// (internal/control.maxActivationActions). Keeping the two equal means an
// activation this command accepts is one the state machine will also accept:
// a larger number here would be refused later, after the operator believed it
// had been granted.
const maxSwapActionsPerActivation = 100

func runSwapEnable(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("swap enable", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	duration := flags.Duration("duration", 0, "bounded activation lifetime")
	maxActions := flags.Uint("max-actions", 1, "trades this activation permits, 1..100")
	reason := flags.String("reason", "", "operator reason")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent swap enable --config PATH --duration DURATION [--max-actions N] --reason TEXT")
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *reason == "" {
		return errors.New("swap enable requires --config, --duration, and --reason")
	}
	if *duration < time.Minute || *duration > 24*time.Hour {
		return errors.New("swap activation must last between 1 minute and 24 hours")
	}
	// An activation is the only thing standing between a configured agent and
	// an autonomous one, so its bound is the operator's real risk dial: at
	// most this many trades, for at most this long, and nothing after either
	// runs out without a fresh deliberate activation.
	//
	// Every other limit still applies underneath and none of them is relaxed
	// here — per-trade input and fee caps, the signer's durable daily debit
	// ledger, the price trigger, and the schedule window. This bound is a
	// ceiling on top of those, never a replacement for them.
	if *maxActions == 0 || *maxActions > maxSwapActionsPerActivation {
		return fmt.Errorf(
			"an activation must permit between 1 and %d trades",
			maxSwapActionsPerActivation)
	}
	cfg, err := readSwapConfig(*configPath)
	if err != nil {
		return err
	}
	// A trade's identity is derived from its schedule window, so one window
	// yields at most one trade and an activation can never spend more slots
	// than it contains windows. Granting more would read to the operator as
	// permission they have, and quietly never be usable — a dial that lies
	// about its own range is worse than a smaller dial.
	// An activation always overlaps the window already in progress, so the
	// windows it can touch is one more than the whole windows it spans.
	windows := uint(duration.Seconds())/uint(cfg.Swap.ScheduleWindowSeconds) + 1
	if *maxActions > windows {
		return fmt.Errorf(
			"a %s activation reaches at most %d schedule windows of %ds, and a window permits one "+
				"trade; ask for at most %d, lengthen --duration, or set a shorter --schedule-window at setup",
			*duration, windows, cfg.Swap.ScheduleWindowSeconds, windows)
	}
	// The same reasoning as the window bound above, against the other limit an
	// activation cannot exceed: the signer's daily caps. Granting more trades
	// than they fund is a dial that lies about its range, and it lies silently —
	// the signer refuses each extra trade, the refusal is a category rather than
	// a sentence, and the operator sees an expired blockhash a minute later.
	//
	// It lives HERE, not only in strategy enable, because this is the function
	// every arming path routes through: `swap enable` by hand, `swap demo`, and
	// each leg of `strategy enable`. Guarding only the strategy caller left the
	// documented single-leg path with no bound at all.
	if funded := cfg.Swap.FundedTradesPerDay(); uint64(*maxActions) > funded {
		return fmt.Errorf(
			"this profile's daily caps fund %d trade(s) per day, not %d; ask for at most %d, "+
				"or run setup again with a higher --trades-per-day",
			funded, *maxActions, funded)
	}
	fingerprint, err := cfg.Swap.Fingerprint()
	if err != nil {
		return err
	}
	issuedAt := time.Now().UTC()
	revision, err := requireRecentSwapRunner(cfg, issuedAt)
	if err != nil {
		return err
	}
	expiresAt := issuedAt.Add(*duration)
	written, err := control.WriteDevnetActivationIfRevision(
		cfg.Control.StatePath, fingerprint, revision, issuedAt, expiresAt,
		uint32(*maxActions), *reason,
	)
	if err != nil {
		return err
	}
	if !written {
		return errors.New("control state changed while enabling; inspect status and retry")
	}
	return json.NewEncoder(output).Encode(struct {
		Mode       string    `json:"mode"`
		ExpiresAt  time.Time `json:"expires_at"`
		MaxActions uint32    `json:"max_actions"`
	}{control.ModeDevnetEnabled, expiresAt, uint32(*maxActions)})
}

func requireRecentSwapRunner(cfg config, now time.Time) (string, error) {
	fingerprint, err := cfg.Swap.Fingerprint()
	if err != nil {
		return "", err
	}
	state, err := control.NewStateFile(cfg.Control.StatePath, fingerprint, false)
	if err != nil {
		return "", err
	}
	revision, err := state.Revision()
	if err != nil {
		return "", err
	}
	status, err := state.Status()
	if err != nil {
		return "", err
	}
	if status.TerminalOutcome != "" {
		if status.TerminalOutcome == "halted" {
			return "", errors.New("this swap setup is permanently blocked after a halted action")
		}
		return "", errors.New("acknowledge the terminal swap before enabling")
	}
	view, err := operatorstatus.CurrentView(
		operatorstatus.Path(cfg.Journal.Path),
		cfg.Swap.Name,
		cfg.Swap.Cluster,
		cfg.Swap.Version,
		status,
		now.UTC(),
	)
	if err != nil {
		return "", err
	}
	if view.RunnerState != "recent" {
		return "", errors.New("start the swap runner and wait for its first cycle before enabling")
	}
	currentActionID, err := cfg.Swap.ActionID(now.UTC())
	if err != nil {
		return "", err
	}
	if view.LastAction.Result.ActionID == currentActionID {
		switch view.LastAction.Result.Decision {
		case "canceled", "complete", "failed", "halted":
			return "", errors.New("the current swap window is already complete; wait for the next window")
		}
	}
	switch view.Result.Decision {
	case "stopped", "waiting", "skipped":
		return revision, nil
	default:
		return "", errors.New("the swap runner is not ready for a new activation")
	}
}

func runSwapStop(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("swap stop", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	reason := flags.String("reason", "", "operator reason")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent swap stop --config PATH --reason TEXT")
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *reason == "" {
		return errors.New("swap stop requires --config and --reason")
	}
	cfg, err := readSwapConfig(*configPath)
	if err != nil {
		return err
	}
	status, err := stopSwap(cfg, *reason)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Mode              string `json:"mode"`
		RecoveryPending   bool   `json:"recovery_pending,omitempty"`
		TerminalActionID  string `json:"terminal_action_id,omitempty"`
		TerminalOutcome   string `json:"terminal_outcome,omitempty"`
		AttentionRequired bool   `json:"attention_required"`
	}{
		Mode: status.Mode, RecoveryPending: status.RecoveryPending,
		TerminalActionID:  status.TerminalActionID,
		TerminalOutcome:   status.TerminalOutcome,
		AttentionRequired: status.RecoveryPending || status.TerminalOutcome != "",
	})
}

func stopSwap(cfg config, reason string) (control.Status, error) {
	fingerprint, err := cfg.Swap.Fingerprint()
	if err != nil {
		return control.Status{}, err
	}
	state, err := control.NewStateFile(cfg.Control.StatePath, fingerprint, false)
	if err != nil {
		return control.Status{}, err
	}
	if err := state.Stop(reason); err != nil {
		return control.Status{}, err
	}
	return state.Status()
}

func runSwapAcknowledge(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("swap acknowledge", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	actionID := flags.String("action-id", "", "exact terminal action ID")
	outcome := flags.String("outcome", "", "expected terminal outcome")
	reason := flags.String("reason", "", "operator reason")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent swap acknowledge --config PATH --action-id SHA256 --outcome failed|halted --reason TEXT")
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *actionID == "" || *reason == "" ||
		(*outcome != "failed" && *outcome != "halted") {
		return errors.New("swap acknowledge requires --config, --action-id, --outcome failed|halted, and --reason")
	}
	cfg, err := readSwapConfig(*configPath)
	if err != nil {
		return err
	}
	store, err := journal.OpenRotating(cfg.Journal.Path)
	if err != nil {
		if errors.Is(err, journal.ErrLocked) {
			return errors.New("stop the swap runner before acknowledging its terminal action")
		}
		return errors.New("terminal acknowledgement journal is unavailable or invalid")
	}
	fingerprint, err := cfg.Swap.Fingerprint()
	if err != nil {
		_ = store.Close()
		return err
	}
	state, err := control.NewStateFile(cfg.Control.StatePath, fingerprint, false)
	if err != nil {
		_ = store.Close()
		return err
	}
	if _, err := swaprun.ValidateTerminalAcknowledgement(
		store, *actionID, *outcome, *reason,
	); err != nil {
		_ = store.Close()
		return err
	}
	if err := state.StopForTerminal(*actionID, *outcome); err != nil {
		_ = store.Close()
		return err
	}
	appended, err := swaprun.AcknowledgeTerminal(
		store, *actionID, *outcome, *reason, time.Now().UTC(),
	)
	if err != nil {
		_ = store.Close()
		return err
	}
	if err := store.Close(); err != nil {
		return errors.New("close agent journal")
	}
	permanentlyBlocked := *outcome == "halted"
	var status control.Status
	if permanentlyBlocked {
		status, err = state.Status()
	} else {
		status, err = state.AcknowledgeTerminal(*actionID, *outcome, *reason)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Mode                        string `json:"mode"`
		ActionID                    string `json:"action_id"`
		Acknowledged                string `json:"acknowledged"`
		JournalAppended             bool   `json:"journal_appended"`
		RunnerRestartRequired       bool   `json:"runner_restart_required"`
		ExecutionPermanentlyBlocked bool   `json:"execution_permanently_blocked"`
	}{
		Mode: status.Mode, ActionID: *actionID, Acknowledged: *outcome,
		JournalAppended: appended, RunnerRestartRequired: !permanentlyBlocked,
		ExecutionPermanentlyBlocked: permanentlyBlocked,
	})
}

func runSwapDrain(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("swap drain", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	timeout := flags.Duration("timeout", 0, "maximum drain time")
	reason := flags.String("reason", "", "operator reason")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent swap drain --config PATH [--timeout DURATION] --reason TEXT")
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *reason == "" {
		return errors.New("swap drain requires --config and --reason")
	}
	cfg, err := readSwapConfig(*configPath)
	if err != nil {
		return err
	}
	minimumTimeout := time.Duration(cfg.Swap.MaxReconciliationSeconds)*time.Second + 30*time.Second
	if *timeout == 0 {
		*timeout = minimumTimeout
	}
	if *timeout < minimumTimeout || *timeout > 20*time.Minute {
		return fmt.Errorf("swap drain timeout must be between %s and 20m", minimumTimeout)
	}
	stopRequestedAt := time.Now().UTC()
	if _, err := stopSwap(cfg, *reason); err != nil {
		return err
	}
	fingerprint, err := cfg.Swap.Fingerprint()
	if err != nil {
		return err
	}
	stateFile, err := control.NewStateFile(cfg.Control.StatePath, fingerprint, false)
	if err != nil {
		return err
	}
	drainCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := stateFile.Status()
		if err != nil {
			// Naming the cause matters more here than anywhere: this is the brake,
			// and an operator who is told only that it failed has nothing to act on.
			return fmt.Errorf("new actions are stopped, but control status cannot be read: %w", err)
		}
		view, err := operatorstatus.CurrentView(
			operatorstatus.Path(cfg.Journal.Path),
			cfg.Swap.Name,
			cfg.Swap.Cluster,
			cfg.Swap.Version,
			status,
			time.Now().UTC(),
		)
		if err != nil {
			return fmt.Errorf("new actions are stopped, but runner status cannot be read: %w", err)
		}
		switch swapDrainState(view, stopRequestedAt) {
		case "drained":
			return json.NewEncoder(output).Encode(struct {
				Mode    string `json:"mode"`
				Drained bool   `json:"drained"`
			}{Mode: control.ModeNoNewActions, Drained: true})
		case "attention":
			return errors.New("new actions are stopped, but the swap runner requires operator attention before shutdown")
		}
		select {
		case <-drainCtx.Done():
			return errors.New("new actions are stopped, but the runner did not become idle before the drain timeout")
		case <-ticker.C:
		}
	}
}

func swapDrainState(view operatorstatus.View, stopRequestedAt time.Time) string {
	if view.RunnerState != "recent" || view.ObservedAt.Before(stopRequestedAt) {
		return "waiting"
	}
	if view.Control.TerminalOutcome == "failed" || view.Control.TerminalOutcome == "halted" {
		return "attention"
	}
	switch view.Result.Decision {
	case "stopped", "complete", "canceled":
		return "drained"
	case "failed", "halted":
		return "attention"
	default:
		return "waiting"
	}
}

func runSwapLoop(ctx context.Context, args []string, output io.Writer) (runErr error) {
	flags := flag.NewFlagSet("swap run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	interval := flags.Duration("interval", 10*time.Second, "delay between lifecycle steps")
	metricsAddress := flags.String("metrics-address", "127.0.0.1:9191", "loopback Prometheus listen address")
	quoteSocket := flags.String("quote-socket", "", "override quote transport with this local socket")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent swap run --config PATH [--interval DURATION] [--metrics-address ADDRESS] [--quote-socket PATH]")
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" {
		return errors.New("swap run requires --config")
	}
	if *interval < time.Second || *interval > 30*time.Second {
		return errors.New("swap interval must be between 1 and 30 seconds")
	}
	if *quoteSocket != "" &&
		(!filepath.IsAbs(*quoteSocket) || filepath.Clean(*quoteSocket) != *quoteSocket) {
		return errors.New("quote socket must be an absolute clean path")
	}
	if !swapPreflightAllowsStartup(checkPreflight(*configPath)) {
		return errors.New("swap preflight failed; run mithril-agent preflight for details")
	}
	runtime, err := openSwapRuntime(*configPath, true, *quoteSocket)
	if err != nil {
		return err
	}
	defer shutdownRunner(&runErr, runtime.control, runtime)
	metrics, serverErrors, closeMetrics, err := startMetrics(ctx, *metricsAddress)
	if err != nil {
		return err
	}
	defer closeMetrics()
	return runDevnetCycles(
		ctx, runtime, metrics, serverErrors, output, *interval, swapStepTimeout,
	)
}

// A node still building its first snapshot has no state file for MCP to read.
// The runtime can safely start in that state: every cycle remains fail-closed,
// publishes why it is waiting, and recovers when the node becomes ready. No
// other failed preflight is allowed through.
func swapPreflightAllowsStartup(summary preflightSummary) bool {
	if summary.Status == preflightOK {
		return true
	}
	mcpPending := summary.Checks.MCPInputs == preflightFailed
	summary.Checks.MCPInputs = preflightOK
	return mcpPending && allPreflightChecksOK(summary.Checks)
}

func runSwapStatus(args []string, output io.Writer) error {
	path, err := swapConfigPath("status", args, output)
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	provider, err := newOperatorProvider(path)
	if err != nil {
		return err
	}
	status, err := provider.Status()
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(status)
}

type swapRuntime struct {
	profile    swaprun.Profile
	engine     *swaprun.Engine
	store      *journal.Store
	control    *control.StateFile
	configPath string
	statusPath string
	observer   *mcpobserve.Client
	quotes     *swapbuilder.Client
	node       *solanarpc.Client
	lifecycle  *txflow.Lifecycle
}

type swapDependencies struct {
	observer  *mcpobserve.Client
	quotes    *swapbuilder.Client
	node      *solanarpc.Client
	lifecycle *txflow.Lifecycle
	prices    *pricetrigger.Evaluator
}

func openSwapDependencies(cfg config) (swapDependencies, error) {
	mithrilURL := os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL")
	primaryURL := os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL")
	secondaryURL := os.Getenv("MITHRIL_AGENT_SECONDARY_RPC_URL")
	if mithrilURL == "" || primaryURL == "" || secondaryURL == "" {
		return swapDependencies{}, errors.New("Mithril and two independent evidence RPC URLs are required")
	}
	if cfg.Quote.SocketPath == "" && os.Getenv("MITHRIL_AGENT_QUOTE_RPC_URL") == "" {
		return swapDependencies{}, errors.New("Orca quote RPC URL is required for direct quote mode")
	}
	providers, err := openBoundRPCProviders(cfg, mithrilURL, primaryURL, secondaryURL)
	if err != nil {
		return swapDependencies{}, err
	}
	lifecycle, err := txflow.New(providers.mithril, providers.primary, providers.secondary)
	if err != nil {
		return swapDependencies{}, err
	}
	observer, err := mcpobserve.New(mcpobserve.Config{
		Command: cfg.MCP.Command, Args: cfg.MCP.Args,
		Env:     mithrilMCPEnvironment(mithrilURL, secondaryURL),
		Cluster: cfg.Swap.Cluster, RPCOrigin: providers.mithril.Origin(),
	}, nil)
	if err != nil {
		return swapDependencies{}, fmt.Errorf("MCP observer: %w", err)
	}
	quotes, err := swapbuilder.New(quoteBuilderConfig(cfg))
	if err != nil {
		return swapDependencies{}, fmt.Errorf("Orca quote builder: %w", err)
	}
	var prices *pricetrigger.Evaluator
	if cfg.Swap.PriceTrigger != nil {
		if !priceSourcePolicyMatches(*cfg.Swap.PriceTrigger) {
			return swapDependencies{}, errors.New("price sources do not match the supported Devnet adapters")
		}
		// The sponsored on-chain feed read through our own node is the default,
		// so a paid data subscription is not a baseline requirement. Hermes
		// stays available for an operator who supplies a key.
		var primary pricetrigger.Source
		if cfg.Swap.PriceTrigger.PrimarySourceSHA256 == pricesource.PythPushIdentitySHA256() {
			node := providers.mithril
			push, pushErr := pricesource.NewPythPush(
				pricesource.NewMithrilAccountReader(
					func(ctx context.Context, address string, minContextSlot, offset, length uint64) (pricesource.AccountData, error) {
						slice, sliceErr := node.AccountSlice(ctx, address, minContextSlot, offset, length)
						if sliceErr != nil {
							return pricesource.AccountData{}, sliceErr
						}
						return pricesource.AccountData{
							ContextSlot: slice.ContextSlot,
							Owner:       slice.Owner,
							DataLength:  slice.DataLength,
							Data:        slice.Data,
						}, nil
					},
				),
				nil,
			)
			if pushErr != nil {
				return swapDependencies{}, pushErr
			}
			primary = push
		} else {
			pyth, pythErr := pricesource.NewPyth(
				nil,
				os.Getenv("MITHRIL_AGENT_PYTH_API_KEY"),
			)
			if pythErr != nil {
				return swapDependencies{}, pythErr
			}
			primary = pyth
		}
		prices, err = pricetrigger.NewEvaluator(
			primary, pricesource.NewCoinbase(nil), nil,
		)
		if err != nil {
			return swapDependencies{}, fmt.Errorf("price evaluator: %w", err)
		}
	}
	return swapDependencies{
		observer: observer, quotes: quotes, node: providers.mithril, lifecycle: lifecycle,
		prices: prices,
	}, nil
}

// priceSourcePolicyMatches accepts either reviewed primary adapter. Both are
// pinned by identity hash, so a policy still names exactly one of them and an
// unreviewed source remains impossible.
func priceSourcePolicyMatches(policy pricetrigger.Policy) bool {
	primaryReviewed := policy.PrimarySourceSHA256 == pricesource.PythPushIdentitySHA256() ||
		policy.PrimarySourceSHA256 == pricesource.PythIdentitySHA256()
	return primaryReviewed &&
		policy.SecondarySourceSHA256 == pricesource.CoinbaseIdentitySHA256()
}

func quoteBuilderConfig(cfg config) swapbuilder.Config {
	if cfg.Quote.SocketPath != "" {
		return swapbuilder.Config{SocketPath: cfg.Quote.SocketPath}
	}
	return swapbuilder.Config{
		NodeCommand: cfg.Quote.Command,
		ScriptPath:  cfg.Quote.ScriptPath,
		RPCURL:      os.Getenv("MITHRIL_AGENT_QUOTE_RPC_URL"),
	}
}

func openSwapRuntime(
	configPath string, requireFreshActivation bool, quoteSocket string,
) (*swapRuntime, error) {
	cfg, err := readSwapConfig(configPath)
	if err != nil {
		return nil, err
	}
	applyQuoteSocketOverride(&cfg, quoteSocket)
	fingerprint, err := cfg.Swap.Fingerprint()
	if err != nil {
		return nil, err
	}
	dependencies, err := openSwapDependencies(cfg)
	if err != nil {
		return nil, err
	}
	authority, err := policyclient.New(policyclient.Config{
		Command: cfg.Policy.Command, PolicyPath: cfg.Policy.PolicyPath,
		KeypairPath: cfg.Policy.KeypairPath, KeyID: cfg.Policy.KeyID,
		PublicKey: cfg.Policy.PublicKey,
	})
	if err != nil {
		return nil, fmt.Errorf("risk authority: %w", err)
	}
	signerProcess, err := signerclient.New(signerclient.Config{
		Command: cfg.Signer.Command, PolicyPath: cfg.Signer.PolicyPath,
		KeypairPath: cfg.Signer.KeypairPath,
	})
	if err != nil {
		return nil, fmt.Errorf("signer: %w", err)
	}
	submitter, err := submitterclient.New(submitterclient.Config{
		Command: cfg.Submitter.Command, PolicyPath: cfg.Submitter.PolicyPath,
		PrivateKeyPath: cfg.Submitter.PrivateKeyPath,
		Env: []string{
			"MITHRIL_AGENT_MITHRIL_RPC_URL=" + os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL"),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("submitter: %w", err)
	}
	stateFile, err := control.NewStateFile(cfg.Control.StatePath, fingerprint, requireFreshActivation)
	if err != nil {
		return nil, fmt.Errorf("control state: %w", err)
	}
	store, err := journal.OpenRotating(cfg.Journal.Path)
	if err != nil {
		return nil, err
	}
	options := []swaprun.Option{swaprun.WithClockSample(clockCheckSample)}
	if dependencies.prices != nil {
		options = append(options, swaprun.WithPriceTrigger(dependencies.prices))
	}
	engine, err := swaprun.New(
		store, dependencies.observer, dependencies.quotes, dependencies.node,
		authority, signerProcess, submitter, dependencies.lifecycle, stateFile, nil,
		options...,
	)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return &swapRuntime{
		profile: *cfg.Swap, engine: engine, store: store, control: stateFile,
		configPath: configPath,
		statusPath: operatorstatus.Path(cfg.Journal.Path), observer: dependencies.observer,
		quotes: dependencies.quotes, node: dependencies.node, lifecycle: dependencies.lifecycle,
	}, nil
}

func applyQuoteSocketOverride(cfg *config, quoteSocket string) {
	if quoteSocket == "" {
		return
	}
	cfg.Quote.Command = ""
	cfg.Quote.ScriptPath = ""
	cfg.Quote.SocketPath = quoteSocket
}

// Alerts re-reads the current alert slots so a threshold edit takes effect
// without restarting the runner. On any read or validation failure it reports
// no alerts configured: for a notify-only feature, silence plus the
// evidence-available gauge is the fail-closed direction.
func (runtime *swapRuntime) Alerts() (alertsConfig, bool) {
	cfg, err := readSwapConfig(runtime.configPath)
	if err != nil {
		return alertsConfig{}, false
	}
	return cfg.Alerts, true
}

func (runtime *swapRuntime) Step(ctx context.Context) (execution.Result, journal.Stats, error) {
	result, err := runtime.engine.RunOnce(ctx, runtime.profile)
	if err != nil {
		return execution.Result{}, journal.Stats{}, err
	}
	executionResult := swapExecutionResult(result)
	// The balance rides on every cycle result, stamped with its own
	// observation time, so status and metrics can carry it without a
	// separate read that could disagree with what the engine acted on.
	if lamports, observedUnix, ok := runtime.engine.LastBalance(); ok {
		executionResult.BalanceLamports = lamports
		executionResult.BalanceObservedUnix = observedUnix
	}
	stats, err := runtime.store.Stats()
	if err != nil {
		return executionResult, journal.Stats{}, err
	}
	return executionResult, stats, nil
}

func (runtime *swapRuntime) CheckDependencies(ctx context.Context) error {
	return runtime.quotes.Health(ctx)
}

func swapExecutionResult(result swaprun.Result) execution.Result {
	return execution.Result{
		ActionID: result.ActionID, Decision: result.Decision, Reason: result.Reason,
		AmountLamports: result.InputLamports, InputAmount: result.InputAmount,
		InputAsset: result.InputAsset, OutputAsset: result.OutputAsset,
		MinimumOutput: result.MinimumOutput,
		OutputAmount:  result.OutputAmount, Signature: result.Signature,
		Submitted: result.Submitted,
		Verdict:   result.Verdict, Recovered: result.Recovered,
		PendingSinceUnix:             result.PendingSinceUnix,
		ReconciliationTimeoutSeconds: result.ReconciliationTimeoutSeconds,
		PriceTrigger:                 result.PriceTrigger,
	}
}

func (runtime *swapRuntime) Close() error                           { return runtime.store.Close() }
func (runtime *swapRuntime) Stats() (journal.Stats, error)          { return runtime.store.Stats() }
func (runtime *swapRuntime) ControlStatus() (control.Status, error) { return runtime.control.Status() }
func (runtime *swapRuntime) StopNewActions(actionID, outcome string) error {
	return runtime.control.StopForTerminal(actionID, outcome)
}
func (runtime *swapRuntime) RecordStatus(
	at time.Time,
	result execution.Result,
	stats journal.Stats,
	status control.Status,
) (operatorstatus.Action, error) {
	var lastAction operatorstatus.Action
	durable, durableAt, found, err := swaprun.LatestDurableAction(
		runtime.store, runtime.profile, at.UTC(),
	)
	if err != nil {
		return operatorstatus.Action{}, err
	}
	if found {
		lastAction = operatorstatus.Action{
			ObservedAt: durableAt,
			Result:     swapExecutionResult(durable),
		}
	}
	snapshot := operatorstatus.Snapshot{
		Version: operatorstatus.Version, ObservedAt: at.UTC(),
		Profile: runtime.profile.Name, ProfileVersion: runtime.profile.Version,
		Cluster: runtime.profile.Cluster, Result: result, LastAction: lastAction,
		Journal: stats, Control: status,
	}
	if err := operatorstatus.Write(runtime.statusPath, snapshot); err != nil {
		return operatorstatus.Action{}, err
	}
	if result.ActionID != "" &&
		(result.Decision == "complete" || result.Decision == "canceled") {
		if _, err := swaprun.MarkStatusProjected(
			runtime.store, result.ActionID, result.Decision, result.Verdict, at.UTC(),
		); err != nil {
			return operatorstatus.Action{}, err
		}
		updatedStats, err := runtime.store.Stats()
		if err != nil {
			return operatorstatus.Action{}, err
		}
		snapshot.Journal = updatedStats
		if err := operatorstatus.Write(runtime.statusPath, snapshot); err != nil {
			return operatorstatus.Action{}, err
		}
	}
	written, err := operatorstatus.Read(runtime.statusPath)
	if err != nil {
		return operatorstatus.Action{}, err
	}
	return written.LastAction, nil
}

// swapCheckNode is the node evidence that made this check trustworthy. It
// carries no endpoint, identity, or credential.
type swapCheckNode struct {
	ObservedSlot     uint64  `json:"observed_slot"`
	ObservationAgeS  int64   `json:"observation_age_seconds"`
	SlotsBehind      *int64  `json:"slots_behind,omitempty"`
	SlotThreshold    *uint64 `json:"slot_threshold,omitempty"`
	Health           string  `json:"health"`
	EvidenceComplete bool    `json:"evidence_complete"`
}

func checkNode(
	observation agent.NodeObservation,
	lagSlots *int64,
	lagThreshold *uint64,
) swapCheckNode {
	return swapCheckNode{
		ObservedSlot:     observation.Account.Slot,
		ObservationAgeS:  int64(time.Since(observation.Account.ObservedAt).Seconds()),
		SlotsBehind:      lagSlots,
		SlotThreshold:    lagThreshold,
		Health:           observation.Health.Status,
		EvidenceComplete: observation.Health.EvidenceComplete,
	}
}

// describePriceSource names the reviewed adapter in use without exposing an
// endpoint or credential.
func describePriceSource(policy *pricetrigger.Policy) string {
	if policy == nil {
		return "none (no price rule configured)"
	}
	switch policy.PrimarySourceSHA256 {
	case pricesource.PythPushIdentitySHA256():
		return "Pyth on-chain push via Mithril + Coinbase"
	case pricesource.PythIdentitySHA256():
		return "Pyth Hermes (operator-supplied credential) + Coinbase"
	default:
		return "unrecognised"
	}
}

type swapCheckPolicy struct {
	Direction                 string `json:"direction"`
	InputAsset                string `json:"input_asset"`
	OutputAsset               string `json:"output_asset"`
	InputAmountBaseUnits      uint64 `json:"input_amount_base_units"`
	SlippageBPS               uint16 `json:"slippage_bps"`
	MaxFeeLamports            uint64 `json:"max_fee_lamports"`
	ReserveLamports           uint64 `json:"reserve_lamports"`
	DailyDebitCapLamports     uint64 `json:"daily_debit_cap_lamports,omitempty"`
	DailyInputTokenCap        uint64 `json:"daily_input_token_cap,omitempty"`
	DailyNativeFeeCapLamports uint64 `json:"daily_native_fee_cap_lamports,omitempty"`
	ScheduleWindowSeconds     uint64 `json:"schedule_window_seconds"`
}

func checkPolicy(profile swaprun.Profile) swapCheckPolicy {
	policy := swapCheckPolicy{
		Direction: "sell", InputAsset: "SOL", OutputAsset: "devUSDC",
		InputAmountBaseUnits: profile.InputAmount(), SlippageBPS: profile.SlippageBPS,
		MaxFeeLamports: profile.MaxFeeLamports, ReserveLamports: profile.ReserveLamports,
		DailyDebitCapLamports: profile.DailyDebitCapLamports,
		ScheduleWindowSeconds: profile.ScheduleWindowSeconds,
	}
	if profile.IsBuy() {
		policy.Direction = "buy"
		policy.InputAsset = "devUSDC"
		policy.OutputAsset = "SOL"
		policy.DailyDebitCapLamports = 0
		policy.DailyInputTokenCap = profile.DailyInputTokenCap
		policy.DailyNativeFeeCapLamports = profile.DailyNativeFeeCapLamports
	}
	return policy
}

func runSwapCheck(ctx context.Context, args []string, output io.Writer) (returnErr error) {
	stage := "arguments"
	reportFailure := true
	var lagSlots *int64
	var lagThreshold *uint64
	defer func() {
		if returnErr == nil || !reportFailure {
			return
		}
		// stage stays the stable machine token; meaning is added so an operator
		// can act without decoding it. Presentation only — the verdict is
		// already decided above.
		_ = json.NewEncoder(output).Encode(struct {
			Status        string  `json:"status"`
			Stage         string  `json:"stage"`
			Meaning       string  `json:"meaning,omitempty"`
			SlotsBehind   *int64  `json:"slots_behind,omitempty"`
			SlotThreshold *uint64 `json:"slot_threshold,omitempty"`
		}{
			Status: "failed", Stage: stage, Meaning: explainStage(stage),
			SlotsBehind: lagSlots, SlotThreshold: lagThreshold,
		})
	}()
	path, err := swapConfigPath("check", args, output)
	if err != nil {
		return err
	}
	if path == "" {
		reportFailure = false
		return nil
	}
	stage = "configuration"
	cfg, err := readSwapConfig(path)
	if err != nil {
		return err
	}
	stage = "dependencies"
	dependencies, err := openSwapDependencies(cfg)
	if err != nil {
		return err
	}
	profile := *cfg.Swap
	checkCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	now := time.Now().UTC()
	stage = "clock"
	clockSample, err := clockCheckSample()
	if err != nil {
		return err
	}
	if err := swaprun.ValidateClockSample(
		clockSample,
		now,
		profile.ClockUncertaintyLimit(),
	); err != nil {
		return err
	}
	stage = "genesis"
	if err := dependencies.lifecycle.VerifyGenesis(checkCtx, solana.DevnetGenesisHash); err != nil {
		return err
	}
	stage = "mithril_observation"
	observation, err := dependencies.observer.Observe(checkCtx, profile.Owner())
	if err != nil {
		if failure := mcpobserve.FailureStage(err); failure != "" {
			stage += "_" + failure
		}
		return err
	}
	if comparison := observation.Health.CrossCheck; comparison != nil {
		behind := comparison.SlotsBehind
		threshold := comparison.Threshold
		lagSlots = &behind
		lagThreshold = &threshold
	}
	now = time.Now().UTC()
	stage = "observation_policy"
	if err := swaprun.ValidateObservation(profile, observation, now); err != nil {
		if failure := swaprun.ObservationFailure(err); failure != "" {
			stage = failure
		}
		return err
	}
	stage = "quote"
	quote, err := dependencies.quotes.Quote(checkCtx, swapbuilder.Request{
		Owner: profile.Owner(), Pool: profile.Pool(),
		InputMint:   profile.InputMint(),
		InputAmount: profile.InputAmount(), SlippageBPS: profile.SlippageBPS,
	})
	if err != nil {
		return err
	}
	var priceStatus *pricetrigger.Status
	if dependencies.prices != nil {
		stage = "price_evidence"
		// The default feed is an on-chain account read through this operator's
		// own node, and such a source refuses a read that names no proved slot.
		// The observation above has already been validated, so bind to it: an
		// unbound read here would report the rule as unreadable on a node that
		// is in fact healthy.
		evidence, err := dependencies.prices.EvaluateAtSlot(
			checkCtx, *profile.PriceTrigger, observation.Account.Slot,
		)
		if err != nil {
			return errors.New("price evidence is unavailable")
		}
		status, err := pricetrigger.Project(*profile.PriceTrigger, evidence)
		if err != nil {
			return errors.New("price evidence is invalid")
		}
		priceStatus = &status
	}
	stage = "quote_policy"
	if quote.TradeEnableTimestamp.After(now) {
		return errors.New("Orca pool is not trading yet")
	}
	if quote.TokenIn != profile.InputAmount() {
		return errors.New("quote input does not match the active profile")
	}
	approvedQuote := orcaswap.Quote{
		InputAmount: quote.TokenIn, EstimatedOutput: quote.TokenEstOut,
		MinimumOutput: quote.TokenMinOut,
		SlippageBPS:   profile.SlippageBPS,
	}
	if profile.IsBuy() {
		if profile.BuyRoute == nil {
			return errors.New("buy route is missing")
		}
		if _, err := orcaswap.ValidateBuyInstructionsV2(
			*profile.BuyRoute, approvedQuote, quote.Instructions,
		); err != nil {
			return err
		}
	} else if _, err := orcaswap.ValidateInstructions(
		profile.Route, approvedQuote, quote.Instructions,
	); err != nil {
		return err
	}
	stage = "blockhash"
	latest, err := dependencies.node.LatestBlockhash(checkCtx, observation.Account.Slot)
	if err != nil {
		return err
	}
	if latest.ContextSlot < observation.Account.Slot {
		return errors.New("swap blockhash predates the node observation")
	}
	stage = "block_height"
	height, err := dependencies.node.BlockHeight(checkCtx)
	if err != nil {
		return err
	}
	if height == 0 || height >= latest.LastValidBlockHeight ||
		latest.LastValidBlockHeight-height > profile.MaxBlockHeightWindow {
		return errors.New("swap blockhash validity window is outside policy")
	}
	stage = "deployment_evidence"
	if profile.IsBuy() {
		if err := dependencies.lifecycle.VerifyWhirlpoolBuyDeployment(
			checkCtx, *profile.BuyRoute, latest.ContextSlot,
		); err != nil {
			return err
		}
		if _, err := dependencies.lifecycle.VerifyTokenInputAccount(
			checkCtx, profile.BuyRoute.InputTokenAccount, profile.BuyRoute.TokenMintB,
			profile.BuyRoute.Owner, profile.InputTokenAmount, latest.ContextSlot,
		); err != nil {
			return err
		}
	} else if err := dependencies.lifecycle.VerifyWhirlpoolDeployment(
		checkCtx, profile.Route, latest.ContextSlot,
	); err != nil {
		return err
	}
	stage = "message"
	message, err := solana.BuildLegacyMessage(
		profile.Owner(), latest.Blockhash, quote.Instructions,
	)
	if err != nil {
		return err
	}
	var routeRent uint64
	stage = "rent"
	if profile.IsBuy() {
		intent, err := orcaswap.DecodeBuyMessageV2(*profile.BuyRoute, message)
		if err != nil {
			return err
		}
		rent, err := dependencies.lifecycle.VerifyTokenAccountRent(
			checkCtx, profile.BuyRoute.MaxTemporaryRentLamports,
		)
		if err != nil {
			return err
		}
		if rent.Lamports != intent.TemporaryRentLamports {
			return errors.New("temporary account rent evidence changed")
		}
		routeRent = rent.Lamports
	} else {
		intent, err := orcaswap.DecodeMessage(profile.Route, message)
		if err != nil {
			return err
		}
		if intent.OutputAccountCreated {
			rent, err := dependencies.lifecycle.VerifyTokenAccountRent(
				checkCtx, profile.Route.MaxOutputAccountRentLamports,
			)
			if err != nil {
				return err
			}
			routeRent = rent.Lamports
		}
	}
	stage = "fee"
	fee, err := dependencies.lifecycle.FeeForMessage(checkCtx, message, latest.ContextSlot)
	if err != nil {
		return err
	}
	if fee.Lamports == 0 || fee.Lamports > profile.MaxFeeLamports {
		return errors.New("swap fee exceeds the active profile")
	}
	stage = "simulation"
	simulation, err := dependencies.lifecycle.SimulateLegacy(checkCtx, message, latest.ContextSlot)
	if err != nil {
		return err
	}
	readyAt := time.Now().UTC()
	stage = "final_observation"
	if err := swaprun.ValidateObservation(profile, observation, readyAt); err != nil {
		if failure := swaprun.ObservationFailure(err); failure != "" {
			stage = "final_" + failure
		}
		return err
	}
	stage = "final_clock"
	clockSample, err = clockCheckSample()
	if err != nil {
		return err
	}
	if err := swaprun.ValidateClockSample(
		clockSample,
		readyAt,
		profile.ClockUncertaintyLimit(),
	); err != nil {
		return err
	}
	reportFailure = false
	// A reviewer should be able to read cluster, node freshness, route, limits
	// and data-source readiness from one command, without cross-referencing
	// another tool to learn whether the node was actually fit to act.
	return json.NewEncoder(output).Encode(struct {
		Status        string               `json:"status"`
		Cluster       string               `json:"cluster"`
		Node          swapCheckNode        `json:"node"`
		Route         string               `json:"route"`
		PriceSource   string               `json:"price_source"`
		Policy        swapCheckPolicy      `json:"policy"`
		MinimumOutput uint64               `json:"minimum_output"`
		FeeLamports   uint64               `json:"fee_lamports"`
		ContextSlot   uint64               `json:"simulation_context_slot"`
		UnitsConsumed uint64               `json:"units_consumed,omitempty"`
		RouteRent     uint64               `json:"route_rent_lamports,omitempty"`
		PriceTrigger  *pricetrigger.Status `json:"price_trigger,omitempty"`
	}{
		"ready", profile.Cluster, checkNode(observation, lagSlots, lagThreshold),
		// Route.Pool is empty on a buy by construction — Validate requires the
		// sell route to be the zero value there — so reading the field directly
		// printed a blank pool on the one screen an operator checks before
		// arming. The accessor resolves the direction.
		profile.Pool(), describePriceSource(profile.PriceTrigger),
		checkPolicy(profile), quote.TokenMinOut, fee.Lamports, simulation.ContextSlot,
		simulation.UnitsConsumed, routeRent, priceStatus,
	})
}

var _ devnetCycleRuntime = (*swapRuntime)(nil)
var _ dependencyChecker = (*swapRuntime)(nil)
var _ statusRecorder = (*swapRuntime)(nil)
