package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/agentmcp"
	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/internal/clockcheck"
	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/internal/runmetrics"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/mcpobserve"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/policyclient"
	"github.com/Overclock-Validator/mithril-agent/signerclient"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/statussocket"
	"github.com/Overclock-Validator/mithril-agent/submitterclient"
	"github.com/Overclock-Validator/mithril-agent/swapbuilder"
	"github.com/Overclock-Validator/mithril-agent/swaprun"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const rootUsage = `Mithril Agent — bounded Solana Devnet pilot

If you read one line of this, read this one. It says where you are and the
single next thing to do, from any state, and changes nothing:
  mithril-agent start

Installed supervised pilot:
  mithril-agent status --status-socket /run/mithril-agent-status.sock
  sudo systemctl start --wait mithril-agent-demo.service
  Read /usr/local/share/doc/mithril-agent/DEMO.md before starting the demo.

Nothing installed? These two need no wallet, no server, no account:
  mithril-agent explain          what it can and cannot do, in plain English
  mithril-agent walkthrough      watch the real machinery run, on live prices

On a prepared host, the demonstration is three steps:
  mithril-agent setup            guided; press Enter to accept each default
  mithril-agent service install --output "$HOME/.mithril-agent/mithril-agent-run.service"
                                 prints the review and install steps
  mithril-agent demo             arms ONE bounded trade for the runner to make

The runner executes; demo only authorises. Without a running runner, demo has
nothing to act for it and says so.

Trading at a price, and getting the profit out:
  mithril-agent setup            asks the price to trade at (blank = no condition)
  mithril-agent setup strategy --sell-at-usd P --buy-at-usd P --to ADDRESS
                                 one round trip: sell high, buy back, sweep profit
  mithril-agent setup sweep --wallet PATH --to ADDRESS   profit to YOUR wallet
  mithril-agent strategy show [--config PATH]   everything currently configured

Alerts on your phone are a separate process:
  mithril-agent-telegram link    find your chat ID (Telegram never shows it)
  mithril-agent-telegram test    prove a message reaches you, before a trade does

  mithril-agent doctor           is it ready? what to do if not  [--json]
  mithril-agent wallet check --file PATH   check a wallet you already have
  mithril-agent funding check    is the cap on the agent's account real?

Local setup and review:
  mithril-agent check [--config PATH]
  mithril-agent demo [--config PATH] [--timeout DURATION] [--json]
  mithril-agent status --config PATH
  mithril-agent preflight [--config PATH] [--explain]

The read-only commands and demo find the configuration that setup recorded, or
the installed one, so a reviewer never has to know a path. The commands that
arm or stop the agent still require an explicit --config: someone changing what
the agent may do should have to name what they are changing.
  mithril-agent swap --help
  mithril-agent mcp (--config PATH | --status-socket PATH)
  mithril-agent shadow policy --out PATH --observe ADDR --sell-at-usd N
  mithril-agent shadow run --policy PATH --dir PATH
  mithril-agent shadow report --policy PATH --dir PATH
  mithril-agent shadow backtest --policy PATH --dir PATH --buy-at-usd N
  mithril-agent journal verify --path ABSOLUTE_PATH
  mithril-agent clock-check --config PATH
  mithril-agent version

Shadow mode watches a live market, including Mainnet, and records what the rule
would have done. It holds no key and has no code path to a signature, which is
why it is the one surface allowed to look at Mainnet.

The check command is read-only. Direct check and demo commands require the
protected local environment; installed operators should use the supervised
paths above. The demo permits at most one bounded Devnet trade and returns
execution to stopped mode. MCP and Telegram are read-only and cannot authorize,
sign, or submit a transaction.

Legacy transfer commands remain available for compatibility but are unsupported
for new deployments.`

// version is stamped at build time:
//
//	go build -ldflags "-X main.version=$(git describe --always --dirty)"
//
// It is deliberately not a constant. A hardcoded version makes the documented
// upgrade and rollback procedure unverifiable, because every build on the host
// claims to be the same one.
var version = ""

// agentVersion reports the stamped version, falling back to the build's own
// VCS metadata, and says plainly when neither is available rather than
// inventing a number.
func agentVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		revision, modified := "", ""
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.modified":
				if setting.Value == "true" {
					modified = "-dirty"
				}
			}
		}
		if revision != "" {
			if len(revision) > 12 {
				revision = revision[:12]
			}
			return revision + modified
		}
	}
	return "unknown (built without version information)"
}

const (
	maxInputBytes         = 64 << 10
	devnetStepTimeout     = 45 * time.Second
	supervisedDemoCommand = "sudo systemctl start --wait mithril-agent-demo.service"
)

type config struct {
	Profile agent.Profile    `json:"profile,omitzero"`
	Swap    *swaprun.Profile `json:"swap,omitempty"`
	MCP     struct {
		Command string   `json:"command"`
		Args    []string `json:"args,omitempty"`
	} `json:"mcp,omitempty"`
	Policy struct {
		Command     string `json:"command"`
		PolicyPath  string `json:"policy_path"`
		KeypairPath string `json:"keypair_path"`
		KeyID       string `json:"key_id"`
		PublicKey   string `json:"public_key"`
	} `json:"policy,omitempty"`
	Signer struct {
		Command     string `json:"command"`
		PolicyPath  string `json:"policy_path"`
		KeypairPath string `json:"keypair_path"`
	} `json:"signer,omitempty"`
	Submitter struct {
		Command        string `json:"command"`
		PolicyPath     string `json:"policy_path"`
		PrivateKeyPath string `json:"private_key_path"`
	} `json:"submitter,omitempty"`
	Quote struct {
		Command    string `json:"command,omitempty"`
		ScriptPath string `json:"script_path,omitempty"`
		SocketPath string `json:"socket_path,omitempty"`
	} `json:"quote,omitempty"`
	Evidence struct {
		PrimaryTrustDomain    string `json:"primary_trust_domain"`
		PrimaryOriginSHA256   string `json:"primary_origin_sha256"`
		SecondaryTrustDomain  string `json:"secondary_trust_domain"`
		SecondaryOriginSHA256 string `json:"secondary_origin_sha256"`
	} `json:"evidence,omitempty"`
	Control struct {
		StatePath string `json:"state_path"`
	} `json:"control,omitempty"`
	Journal struct {
		Path string `json:"path"`
	} `json:"journal,omitempty"`
	// Alerts are notify-only thresholds. They live outside the fingerprinted
	// profile deliberately: editing one must not re-key action IDs, control
	// binding, or the signer ledger. See alerts.go for the shape and rules.
	Alerts alertsConfig `json:"alerts,omitzero"`
}

func (c config) hasLegacyProfile() bool {
	return c.Profile != (agent.Profile{})
}

func (c config) validateProfileSelection() error {
	if c.hasLegacyProfile() == (c.Swap != nil) {
		return errors.New("config must contain exactly one profile")
	}
	return nil
}

func (c config) validateEvidenceTrustDomains() error {
	if !validTrustDomain(c.Evidence.PrimaryTrustDomain) ||
		!validTrustDomain(c.Evidence.SecondaryTrustDomain) ||
		c.Evidence.PrimaryTrustDomain == c.Evidence.SecondaryTrustDomain {
		return errors.New("evidence providers must have two distinct trust domains")
	}
	return nil
}

func validTrustDomain(value string) bool {
	if len(value) < 2 || len(value) > 64 {
		return false
	}
	for index, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' ||
			index > 0 && (char == '-' || char == '_' || char == '.') {
			continue
		}
		return false
	}
	return true
}

var clockCheckSample = clockcheck.SystemSample

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := runCLI(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if code != 0 {
		os.Exit(code)
	}
}

func runCLI(ctx context.Context, args []string, output, errorOutput io.Writer) int {
	if err := runContext(ctx, args, output); err != nil {
		_, _ = fmt.Fprintln(errorOutput, "mithril-agent:", err)
		return 1
	}
	return 0
}

func run(args []string, output io.Writer) error {
	return runContext(context.Background(), args, output)
}

func runContext(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("run mithril-agent help for usage")
	}
	switch args[0] {
	case "help", "-h", "--help":
		_, err := fmt.Fprintln(output, rootUsage)
		return err
	case "explain":
		return runExplain(args[1:], output)
	case "walkthrough":
		return runWalkthrough(ctx, args[1:], output)
	case "service":
		return runService(args[1:], output)
	case "start":
		return runStart(ctx, args[1:], output)
	case "doctor":
		return runDoctor(ctx, args[1:], output)
	case "setup":
		if len(args) > 1 && args[1] == "sweep" {
			return runSweepSetup(ctx, args[2:], output)
		}
		if len(args) > 1 && args[1] == "strategy" {
			return runStrategySetup(ctx, args[2:], output)
		}
		return runSetup(ctx, args[1:], output)
	case "wallet":
		return runWallet(ctx, args[1:], output)
	case "funding":
		return runFunding(ctx, args[1:], output)
	case "preflight":
		return runPreflight(args[1:], output)
	case "check":
		return runTopLevelCheck(ctx, args[1:], output)
	case "demo":
		return runTopLevelDemo(ctx, args[1:], output)
	case "devnet-check":
		return runDevnetCheck(ctx, args[1:], output)
	case "shadow":
		// The subcommand form is the continuous keyless observer; the flag form
		// is the older single-shot shadow, kept working for existing scripts.
		if len(args) > 1 && args[1] == "run" {
			return runShadowRun(ctx, args[2:], output)
		}
		if len(args) > 1 && args[1] == "report" {
			return runShadowReport(args[2:], output)
		}
		if len(args) > 1 && args[1] == "policy" {
			return runShadowPolicy(args[2:], output)
		}
		if len(args) > 1 && args[1] == "backtest" {
			return runShadowBacktest(args[2:], output)
		}
		return runShadow(args[1:], output)
	case "devnet-once":
		return runDevnetOnce(ctx, args[1:], output)
	case "devnet-run":
		return runDevnetLoop(ctx, args[1:], output)
	case "devnet-enable":
		return runDevnetEnable(args[1:], output)
	case "devnet-stop":
		return runDevnetStop(args[1:], output)
	case "devnet-status":
		return runDevnetStatus(args[1:], output)
	case "swap":
		return runSwap(ctx, args[1:], output)
	case "strategy":
		return runStrategy(ctx, args[1:], output)
	case "journal":
		return runJournal(args[1:], output)
	case "status":
		return runStatus(args[1:], output)
	case "mcp":
		if len(args) > 1 && args[1] == "config" {
			return runMCPConfig(args[2:], output)
		}
		return runMCP(ctx, args[1:], os.Stdin, output)
	case "profile-fingerprint":
		return runProfileFingerprint(args[1:], output)
	case "clock-check":
		return runClockCheck(args[1:], output)
	case "version":
		_, err := fmt.Fprintln(output, "mithril-agent "+agentVersion())
		return err
	default:
		return unknownCommandError(args[0])
	}
}

// knownCommands is the list offered when somebody mistypes one. Keeping it here
// rather than deriving it from the switch is deliberate: a command that is not
// meant to be discovered should not appear in a suggestion.
var knownCommands = []string{
	"explain", "walkthrough", "setup", "doctor", "wallet", "funding",
	"preflight", "check", "demo", "status", "swap", "shadow", "journal",
	"mcp", "clock-check", "version", "help",
}

// unknownCommandError suggests the closest command rather than only refusing.
// A public tool that answers a typo with nothing but "unknown" makes the person
// go and read the whole help page to find a letter they missed.
func unknownCommandError(given string) error {
	if nearest, ok := nearestCommand(given); ok {
		return fmt.Errorf("unknown command %q — did you mean %q? "+
			"Run: mithril-agent help", given, nearest)
	}
	return fmt.Errorf("unknown command %q. Run: mithril-agent help", given)
}

// nearestCommand returns the closest known command within a small edit
// distance, so a genuine typo is corrected but an unrelated word is not
// answered with a confusing guess.
func nearestCommand(given string) (string, bool) {
	best, bestDistance := "", 3
	for _, candidate := range knownCommands {
		if distance := editDistance(given, candidate); distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	return best, best != ""
}

func editDistance(a, b string) int {
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(min(current[j-1]+1, previous[j]+1), previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}

func runTopLevelCheck(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		_, err := fmt.Fprintln(output, `Usage: mithril-agent check --config PATH

Runs the live read-only readiness gate. It does not open the journal, grant an
action, sign, or submit a transaction.`)
		return err
	}
	return runSwapCheck(ctx, args, output)
}

func runTopLevelDemo(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		_, err := fmt.Fprintln(output, `Usage: mithril-agent demo --config PATH [--timeout DURATION] [--json]

Repeats the read-only gate, permits at most one bounded Devnet trade, waits for
the result, and returns execution to stopped mode. Timeout: 3m to 20m.`)
		return err
	}
	return runSwapDemo(ctx, args, output)
}

func runClockCheck(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("clock-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(
				output,
				"Usage: mithril-agent clock-check --config PATH",
			)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" {
		return errors.New("clock-check requires --config")
	}
	cfg, err := readConfig(*configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	maxUncertainty := time.Duration(0)
	if cfg.Swap != nil {
		if err := cfg.Swap.Validate(); err != nil {
			return fmt.Errorf("swap profile: %w", err)
		}
		maxUncertainty = cfg.Swap.ClockUncertaintyLimit()
	} else {
		if err := cfg.Profile.Validate(); err != nil {
			return fmt.Errorf("profile: %w", err)
		}
		maxUncertainty = cfg.Profile.ClockUncertaintyLimit()
	}
	sample, err := clockCheckSample()
	if err != nil {
		return err
	}
	status := "ok"
	if sample.UncertaintyNanos > uint64(maxUncertainty) {
		status = "failed"
	}
	if err := json.NewEncoder(output).Encode(struct {
		Status              string    `json:"status"`
		ObservedAt          time.Time `json:"observed_at"`
		OffsetNanos         int64     `json:"offset_nanos"`
		UncertaintyNanos    uint64    `json:"uncertainty_nanos"`
		MaxOffsetNanos      int64     `json:"max_offset_nanos"`
		MaxUncertaintyNanos int64     `json:"max_uncertainty_nanos"`
	}{
		Status:              status,
		ObservedAt:          sample.WallTime,
		OffsetNanos:         sample.OffsetNanos,
		UncertaintyNanos:    sample.UncertaintyNanos,
		MaxOffsetNanos:      int64(clockcheck.MaxOffset),
		MaxUncertaintyNanos: int64(maxUncertainty),
	}); err != nil {
		return err
	}
	if status != "ok" {
		return errors.New("kernel clock uncertainty exceeds the active profile")
	}
	return nil
}

func runProfileFingerprint(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("profile-fingerprint", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(
				output,
				"Usage: mithril-agent profile-fingerprint --config PATH",
			)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" {
		return errors.New("profile-fingerprint requires --config")
	}
	cfg, err := readConfig(*configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	_, _, _, fingerprint, err := cfg.activeProfile()
	if err != nil {
		return fmt.Errorf("profile: %w", err)
	}
	return json.NewEncoder(output).Encode(struct {
		ProfileSHA256 string `json:"profile_sha256"`
	}{ProfileSHA256: fingerprint})
}

func runDevnetEnable(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("devnet-enable", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	duration := flags.Duration("duration", 0, "bounded devnet activation lifetime")
	maxActions := flags.Uint("max-actions", 1, "maximum sends allowed by this activation")
	reason := flags.String("reason", "", "operator reason")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(
				output,
				"Usage: mithril-agent devnet-enable --config PATH --duration DURATION [--max-actions N] --reason TEXT",
			)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *reason == "" {
		return errors.New("devnet-enable requires --config, --duration, and --reason")
	}
	if *duration < time.Minute || *duration > 24*time.Hour {
		return errors.New("devnet activation must last between 1 minute and 24 hours")
	}
	if *maxActions == 0 || *maxActions > 100 {
		return errors.New("devnet activation action limit must be between 1 and 100")
	}
	cfg, err := readConfig(*configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	if err := cfg.Profile.Validate(); err != nil {
		return fmt.Errorf("profile: %w", err)
	}
	if cfg.Profile.Cluster != "devnet" {
		return errors.New("transaction execution is restricted to devnet")
	}
	fingerprint, err := cfg.Profile.Fingerprint()
	if err != nil {
		return err
	}
	issuedAt := time.Now().UTC()
	expiresAt := issuedAt.Add(*duration)
	if err := control.WriteDevnetActivation(
		cfg.Control.StatePath,
		fingerprint,
		issuedAt,
		expiresAt,
		uint32(*maxActions),
		*reason,
	); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Mode       string    `json:"mode"`
		ExpiresAt  time.Time `json:"expires_at"`
		MaxActions uint32    `json:"max_actions"`
	}{
		Mode:       control.ModeDevnetEnabled,
		ExpiresAt:  expiresAt,
		MaxActions: uint32(*maxActions),
	})
}

func runDevnetStop(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("devnet-stop", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	reason := flags.String("reason", "", "operator reason")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(
				output,
				"Usage: mithril-agent devnet-stop --config PATH --reason TEXT",
			)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *reason == "" {
		return errors.New("devnet-stop requires --config and --reason")
	}
	cfg, err := readConfig(*configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	if _, _, _, _, err := cfg.activeProfile(); err != nil {
		return err
	}
	if err := control.WriteNoNewActions(cfg.Control.StatePath, *reason); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Mode string `json:"mode"`
	}{Mode: control.ModeNoNewActions})
}

func runDevnetStatus(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("devnet-status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(
				output,
				"Usage: mithril-agent devnet-status --config PATH",
			)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" {
		return errors.New("devnet-status requires --config")
	}
	cfg, err := readConfig(*configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	if err := cfg.Profile.Validate(); err != nil {
		return fmt.Errorf("profile: %w", err)
	}
	fingerprint, err := cfg.Profile.Fingerprint()
	if err != nil {
		return err
	}
	state, err := control.NewStateFile(
		cfg.Control.StatePath,
		fingerprint,
		false,
	)
	if err != nil {
		return err
	}
	status, err := state.Status()
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Profile string         `json:"profile"`
		Version uint32         `json:"version"`
		Cluster string         `json:"cluster"`
		Control control.Status `json:"control"`
	}{
		Profile: cfg.Profile.Name,
		Version: cfg.Profile.Version,
		Cluster: cfg.Profile.Cluster,
		Control: status,
	})
}

func runStatus(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	statusSocketPath := flags.String("status-socket", "", "bounded operator status socket")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent status (--config PATH | --status-socket PATH)")
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || (*configPath == "") == (*statusSocketPath == "") {
		return errors.New("status requires exactly one of --config or --status-socket")
	}
	provider, err := newStatusProvider(*configPath, *statusSocketPath)
	if err != nil {
		return err
	}
	status, err := provider.Status()
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(status)
}

func runMCP(
	ctx context.Context,
	args []string,
	input io.ReadCloser,
	output io.Writer,
) error {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	statusSocketPath := flags.String("status-socket", "", "bounded operator status socket")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent mcp (--config PATH | --status-socket PATH)")
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || (*configPath == "") == (*statusSocketPath == "") {
		return errors.New("mcp requires exactly one of --config or --status-socket")
	}
	provider, err := newStatusProvider(*configPath, *statusSocketPath)
	if err != nil {
		return err
	}
	return agentmcp.Serve(ctx, provider, input, output)
}

func newStatusProvider(configPath, statusSocketPath string) (agentmcp.Provider, error) {
	if statusSocketPath != "" {
		reader, err := statussocket.NewReader(statusSocketPath)
		if err != nil {
			return nil, err
		}
		return newSocketOperatorProvider(reader, time.Now)
	}
	return newOperatorProvider(configPath)
}

type operatorProvider struct {
	config     config
	control    *control.StateFile
	statusPath string
	now        func() time.Time
}

// Strategy answers "what am I configured to do" from the config this provider
// already holds. It reuses strategyShow's own view so the assistant surface and
// the terminal screen can never drift into describing different settings.
//
// It carries no address: the sweep destination is an account, and this surface
// promises not to expose accounts.
func (p *operatorProvider) Strategy() (agentmcp.StrategySettings, error) {
	settings := agentmcp.StrategySettings{}
	if p.config.Swap == nil {
		return settings, nil
	}
	swap := p.config.Swap
	settings.Configured = true
	settings.Direction = "sell SOL for devUSDC"
	settings.InputPerAction = formatUnits(swap.InputLamports, 9) + " SOL"
	settings.DailyCap = formatUnits(swap.DailyDebitCapLamports, 9) + " SOL"
	if swap.IsBuy() {
		settings.Direction = "buy SOL with devUSDC"
		settings.InputPerAction = formatUnits(swap.InputTokenAmount, 6) + " devUSDC"
		settings.DailyCap = formatUnits(swap.DailyInputTokenCap, 6) + " devUSDC"
	}
	settings.MaxFee = formatUnits(swap.MaxFeeLamports, 9) + " SOL"
	settings.FundedTradesPerDay = swap.FundedTradesPerDay()
	if trigger := swap.PriceTrigger; trigger != nil {
		settings.PriceRule = string(trigger.Direction) + " $" + formatUnits(trigger.ThresholdMicros, 6)
	}
	var live bool
	settings.ControlMode, settings.ControlGrant, live = controlGrantAt(p.config.Control.StatePath)
	if settings.ControlGrant != "" && !live {
		settings.ControlGrant += " (cannot act)"
	}
	if strategy, _ := discoverStrategy(); strategy.sweep != "" {
		if cfg, err := readConfig(strategy.sweep); err == nil && cfg.hasLegacyProfile() {
			settings.SweepConfigured = true
			settings.SweepKeepBehind = formatUnits(cfg.Profile.ReserveLamports, 9) + " SOL"
			settings.SweepMaxPerSend = formatUnits(cfg.Profile.MaxTransferLamports, 9) + " SOL"
			settings.SweepDailyCap = formatUnits(cfg.Profile.DailyCapLamports, 9) + " SOL"
			settings.SweepActiveAfter = time.Unix(cfg.Profile.ScheduleAnchorUnix, 0).
				UTC().Format(time.RFC3339)
			// Re-verified here, not trusted from the file: a registration that
			// no longer verifies is exactly what an operator needs told.
			if proof, err := readDestinationProof(filepath.Dir(strategy.sweep)); err == nil {
				settings.SweepProofValid = verifySweepDestinationProof(
					proof.AgentAccount, proof.Destination,
					proof.Nonce, proof.IssuedAt, proof.SignatureBase58,
				) == nil && proof.Destination == cfg.Profile.Destination
			}
		}
	}
	return settings, nil
}

// Strategy on the socket provider reports "not configured": that provider reads
// a bounded status socket and never sees a config file, and inventing settings
// it cannot observe would be worse than saying nothing.
func (p *socketOperatorProvider) Strategy() (agentmcp.StrategySettings, error) {
	return agentmcp.StrategySettings{}, nil
}

func (c config) activeProfile() (string, uint32, string, string, error) {
	if err := c.validateProfileSelection(); err != nil {
		return "", 0, "", "", err
	}
	if c.Swap != nil {
		if err := c.Swap.Validate(); err != nil {
			return "", 0, "", "", fmt.Errorf("swap profile: %w", err)
		}
		fingerprint, err := c.Swap.Fingerprint()
		return c.Swap.Name, c.Swap.Version, c.Swap.Cluster, fingerprint, err
	}
	if err := c.Profile.Validate(); err != nil {
		return "", 0, "", "", fmt.Errorf("profile: %w", err)
	}
	fingerprint, err := c.Profile.Fingerprint()
	return c.Profile.Name, c.Profile.Version, c.Profile.Cluster, fingerprint, err
}

func newOperatorProvider(configPath string) (*operatorProvider, error) {
	cfg, err := readConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	_, _, _, fingerprint, err := cfg.activeProfile()
	if err != nil {
		return nil, fmt.Errorf("profile fingerprint: %w", err)
	}
	state, err := control.NewStateFile(cfg.Control.StatePath, fingerprint, false)
	if err != nil {
		return nil, fmt.Errorf("control state: %w", err)
	}
	return &operatorProvider{
		config:     cfg,
		control:    state,
		statusPath: operatorstatus.Path(cfg.Journal.Path),
		now:        time.Now,
	}, nil
}

func (p *operatorProvider) Info() (agentmcp.Info, error) {
	name, version, cluster, _, err := p.config.activeProfile()
	if err != nil {
		return agentmcp.Info{}, err
	}
	action := agent.ProfileTreasurySweepV1
	trading := false
	if p.config.Swap != nil {
		action = p.config.Swap.Name
		trading = true
	}
	return agentmcp.Info{
		Version:              agentmcp.Version,
		Profile:              name,
		ProfileVersion:       version,
		Cluster:              cluster,
		Action:               action,
		Execution:            "bounded_devnet_only",
		TradingImplemented:   trading,
		TelegramHasAuthority: false,
		MainnetEnabled:       false,
	}, nil
}

func (p *operatorProvider) OperatorGuide() agentmcp.OperatorGuide {
	return localOperatorGuide()
}

func (p *operatorProvider) Status() (operatorstatus.View, error) {
	status, err := p.control.Status()
	if err != nil {
		return operatorstatus.View{}, err
	}
	name, version, cluster, _, err := p.config.activeProfile()
	if err != nil {
		return operatorstatus.View{}, err
	}
	return operatorstatus.CurrentView(
		p.statusPath,
		name,
		cluster,
		version,
		status,
		p.now().UTC(),
	)
}

type socketOperatorProvider struct {
	reader         boundedStatusReader
	now            func() time.Time
	profile        string
	profileVersion uint32
	cluster        string
}

type boundedStatusReader interface {
	Read() (operatorstatus.Snapshot, error)
}

func newSocketOperatorProvider(
	reader boundedStatusReader,
	now func() time.Time,
) (*socketOperatorProvider, error) {
	if reader == nil || now == nil {
		return nil, errors.New("bounded operator status reader and clock are required")
	}
	snapshot, err := reader.Read()
	if err != nil {
		return nil, err
	}
	if _, err := operatorstatus.ViewFromSnapshot(snapshot, now().UTC()); err != nil {
		return nil, err
	}
	return &socketOperatorProvider{
		reader: reader, now: now, profile: snapshot.Profile,
		profileVersion: snapshot.ProfileVersion, cluster: snapshot.Cluster,
	}, nil
}

func (p *socketOperatorProvider) snapshot() (operatorstatus.Snapshot, error) {
	if p == nil || p.reader == nil || p.now == nil || p.profile == "" || p.cluster == "" ||
		p.profileVersion == 0 {
		return operatorstatus.Snapshot{}, errors.New("bounded operator status provider is invalid")
	}
	snapshot, err := p.reader.Read()
	if err != nil {
		return operatorstatus.Snapshot{}, err
	}
	if snapshot.Profile != p.profile || snapshot.ProfileVersion != p.profileVersion ||
		snapshot.Cluster != p.cluster {
		return operatorstatus.Snapshot{}, errors.New("bounded operator status identity changed")
	}
	if _, err := operatorstatus.ViewFromSnapshot(snapshot, p.now().UTC()); err != nil {
		return operatorstatus.Snapshot{}, err
	}
	return snapshot, nil
}

func (p *socketOperatorProvider) Info() (agentmcp.Info, error) {
	snapshot, err := p.snapshot()
	if err != nil {
		return agentmcp.Info{}, err
	}
	return agentmcp.Info{
		Version: agentmcp.Version, Profile: snapshot.Profile,
		ProfileVersion: snapshot.ProfileVersion, Cluster: snapshot.Cluster,
		Action: snapshot.Profile, Execution: "bounded_devnet_only",
		TradingImplemented: true, TelegramHasAuthority: false, MainnetEnabled: false,
	}, nil
}

func (p *socketOperatorProvider) Status() (operatorstatus.View, error) {
	snapshot, err := p.snapshot()
	if err != nil {
		return operatorstatus.View{}, err
	}
	return operatorstatus.ViewFromSnapshot(snapshot, p.now().UTC())
}

func (p *socketOperatorProvider) OperatorGuide() agentmcp.OperatorGuide {
	return agentmcp.OperatorGuide{
		SafeLocalCommand: supervisedDemoCommand,
		CapabilityBoundaries: []string{
			"This MCP server reads the bounded operator-status socket and has no action or control tool.",
			"Only a local system administrator may start the one-action Devnet demonstration service.",
			"Losing the terminal does not stop mithril-agent-demo.service; inspect or stop that unit and verify status before retrying.",
			"These MCP tools and Telegram commands expose no authority to authorize, enable, sign, or submit a transaction.",
			"Do not grant an assistant shell access or permission to control the demonstration service.",
		},
		RecoveryGuidance: agentmcp.StandardRecoveryGuidance(),
	}
}

func localOperatorGuide() agentmcp.OperatorGuide {
	return agentmcp.OperatorGuide{
		SafeLocalCommand: "mithril-agent demo --config PATH",
		CapabilityBoundaries: []string{
			"This MCP server exposes status and guidance only; it has no action or control tool.",
			"The demonstration command must be run locally by an operator with a protected configuration path.",
			"These MCP tools and Telegram commands expose no authority to authorize, enable, sign, or submit a transaction.",
			"Do not grant an assistant shell access to the protected configuration or demonstration command.",
		},
		RecoveryGuidance: agentmcp.StandardRecoveryGuidance(),
	}
}

func runDevnetOnce(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("devnet-once", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent devnet-once --config PATH")
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" {
		return errors.New("devnet-once requires --config")
	}
	runtime, err := openDevnetRuntime(*configPath, false)
	if err != nil {
		return err
	}
	defer runtime.Close()
	stepCtx, cancel := context.WithTimeout(ctx, devnetStepTimeout)
	defer cancel()
	result, _, err := runtime.Step(stepCtx)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}

func runDevnetLoop(ctx context.Context, args []string, output io.Writer) (runErr error) {
	flags := flag.NewFlagSet("devnet-run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	interval := flags.Duration("interval", 10*time.Second, "delay between lifecycle steps")
	metricsAddress := flags.String("metrics-address", "127.0.0.1:9191", "loopback Prometheus listen address")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent devnet-run --config PATH --interval DURATION")
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" {
		return errors.New("devnet-run requires --config")
	}
	if *interval < time.Second || *interval > 30*time.Second {
		return errors.New("devnet-run interval must be between 1 and 30 seconds")
	}
	runtime, err := openDevnetRuntime(*configPath, true)
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
		ctx,
		runtime,
		metrics,
		serverErrors,
		output,
		*interval,
		devnetStepTimeout,
	)
}

type runnerShutdownControl interface {
	Stop(string) error
}

type runnerShutdownRuntime interface {
	Close() error
}

func shutdownRunner(runErr *error, state runnerShutdownControl, runtime runnerShutdownRuntime) {
	if err := state.Stop("runner shutdown"); err != nil {
		*runErr = errors.Join(*runErr, errors.New("stop new actions during runner shutdown"))
	}
	if err := runtime.Close(); err != nil {
		*runErr = errors.Join(*runErr, errors.New("close agent journal"))
	}
}

type devnetCycleRuntime interface {
	Step(context.Context) (execution.Result, journal.Stats, error)
	Stats() (journal.Stats, error)
	ControlStatus() (control.Status, error)
	StopNewActions(string, string) error
}

type dependencyChecker interface {
	CheckDependencies(context.Context) error
}

type statusRecorder interface {
	RecordStatus(
		time.Time,
		execution.Result,
		journal.Stats,
		control.Status,
	) (operatorstatus.Action, error)
}

func runDevnetCycles(
	ctx context.Context,
	runtime devnetCycleRuntime,
	metrics *runmetrics.Metrics,
	serverErrors <-chan error,
	output io.Writer,
	interval time.Duration,
	stepTimeout time.Duration,
) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	for {
		metrics.Heartbeat(time.Now().UTC())
		stepCtx, cancel := context.WithTimeout(ctx, stepTimeout)
		result, stats, stepErr := runtime.Step(stepCtx)
		if stepErr == nil && dependencyProbeSafe(result) {
			if checker, ok := runtime.(dependencyChecker); ok {
				stepErr = checker.CheckDependencies(stepCtx)
			}
		}
		// Read before cancelling reads naturally, though either order is correct:
		// a context latches its first cause, so a later cancel cannot rewrite an
		// expired deadline into Canceled.
		stepTimedOut := errors.Is(stepCtx.Err(), context.DeadlineExceeded)
		cancel()
		terminal := terminalForControl(result)
		if terminal {
			if err := runtime.StopNewActions(result.ActionID, result.Decision); err != nil {
				return errors.New("stop new actions after terminal result")
			}
		}
		if stepErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			var err error
			stats, err = runtime.Stats()
			if err != nil {
				return errors.New("inspect journal after failed cycle")
			}
			if !terminal {
				reason := cycleFailureReason(stepErr, stepTimedOut)
				// The CATEGORY is bounded on purpose — it becomes a metric label,
				// and an unbounded one collapses the dashboard to "unknown". The
				// LOG has no such constraint, and dropping the error there is what
				// made "operation_failed" mean nothing: the same eight characters
				// covered a stale clock, a dead node, a spent cap and a genuine
				// bug, and every diagnosis this week began by running preflight by
				// hand to recover the sentence that was already in memory here.
				log.Printf("cycle failed [%s]: %v", reason, stepErr)
				result = execution.Result{Decision: "failed", Reason: reason}
			}
		}
		controlStatus, statusErr := runtime.ControlStatus()
		if statusErr != nil {
			result = execution.Result{Decision: "failed", Reason: "control_state_unavailable"}
		}
		var lastAction operatorstatus.Action
		if recorder, ok := runtime.(statusRecorder); ok {
			var err error
			lastAction, err = recorder.RecordStatus(
				time.Now().UTC(), result, stats, controlStatus,
			)
			if err != nil {
				return fmt.Errorf("write operator status: %w", err)
			}
		}
		if err := encoder.Encode(result); err != nil {
			return err
		}
		metrics.Observe(time.Now().UTC(), result, stats, controlStatus, lastAction)
		if carrier, ok := runtime.(interface{ Alerts() (alertsConfig, bool) }); ok {
			alerts, valid := carrier.Alerts()
			metrics.ObserveAlerts(evaluateAlerts(alerts, valid, result))
		}
		if carrier, ok := runtime.(interface{ SweepRegistration() (int64, int64) }); ok {
			metrics.ObserveSweepRegistration(carrier.SweepRegistration())
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case err := <-serverErrors:
			if !timer.Stop() {
				<-timer.C
			}
			return err
		case <-timer.C:
		}
	}
}

func dependencyProbeSafe(result execution.Result) bool {
	switch result.Decision {
	case "pending", "executing", "complete", "canceled", "failed", "halted":
		return false
	default:
		return true
	}
}

// terminalForControl decides whether a cycle's outcome should latch the control
// state, which is a decision the operator cannot undo: AcknowledgeTerminal
// refuses "halted" outright, and the devnet command set has no acknowledge verb
// at all.
//
// A halt for a transaction that was never broadcast is not that. A quarantined
// transaction was signed and kept — the engine resolves it to "canceled" as
// soon as the blockhash expires — so there is no ambiguous on-chain outcome to
// protect against, because nothing reached the chain. Latching it meant that
// pressing stop between signing and submission permanently bricked the setup.
//
// Submitted is the discriminator precisely because it is the fact that makes
// the outcome uncertain in the first place.
func terminalForControl(result operatorstatus.Result) bool {
	if result.ActionID == "" {
		return false
	}
	switch result.Decision {
	case "failed":
		return true
	case "halted":
		// Either signal means the bytes left this agent. A reconciliation
		// verdict can only exist for something that was broadcast — it is set
		// only when state.reconciliation is non-nil — so it is evidence of
		// submission even where a caller has not set the flag. A quarantine has
		// neither, which is exactly what makes it recoverable.
		return result.Submitted || result.Verdict != ""
	default:
		return false
	}
}

func cycleFailureReason(err error, timedOut bool) string {
	if timedOut || errors.Is(err, context.DeadlineExceeded) {
		return "operation_timeout"
	}
	if errors.Is(err, swapbuilder.ErrQuoteTemporarilyUnavailable) {
		return "quote_unavailable"
	}
	// The market being below the operator's floor is the one routine reason a
	// healthy runner sits idle. Left unnamed it reads as "operation_failed"
	// every cycle, which is indistinguishable from a broken agent.
	if errors.Is(err, orcaswap.ErrQuoteBelowFloor) {
		return "price_below_floor"
	}
	// The same problem for a freshly configured sweep: its anchor is 24-48h out
	// by default, so a CORRECT setup spends its first day reporting this.
	if errors.Is(err, agent.ErrBeforeScheduleAnchor) {
		return "before_schedule_anchor"
	}
	// Built but not submitted in time. Retryable, and the next cycle rebuilds —
	// so it must not look like the agent failing.
	if errors.Is(err, swaprun.ErrBlockhashExpired) {
		return "blockhash_expired"
	}
	// The signer declining under its own policy — a daily cap spent, a window
	// closed. The bounds working, not the agent breaking. Unnamed, it read as
	// operation_failed every cycle for the rest of the UTC day, and the visible
	// symptom became the expired blockhash of the transaction nobody would sign.
	if errors.Is(err, signerclient.ErrSignerRefused) {
		return "signer_refused"
	}
	// The agent's own Mithril node not answering. Entirely actionable — start
	// the node — but reported as a generic failure it looked like broken
	// trading code. A node that died mid-run on 2026-08-06 produced nothing but
	// operation_failed on all three legs for as long as it stayed down.
	if errors.Is(err, txflow.ErrNodeUnavailable) {
		return "node_unavailable"
	}
	// The pre-trade observation not meeting policy: wallet below the reserve,
	// evidence gone stale, the node's cross-check behind, health degraded. The
	// engine reports all of these in full at engine.go:525 when they are seen at
	// the START of an action, and returned them raw from validateBeforeSend —
	// so the identical condition read as a named, readable "degraded" one moment
	// and as operation_failed the next.
	//
	// ONE bounded category, deliberately: the stage token is composed from live
	// status and issue names ("mithril_health_status_degraded_..."), so using it
	// as a metric label would collapse to "unknown" — worse than what it replaces.
	if swaprun.ObservationFailure(err) != "" {
		return "observation_not_ready"
	}
	// The HOST's clock, not the agent. An NTP poll interval that let the
	// kernel's uncertainty bound drift past policy produced nothing but
	// operation_failed on all three legs for five minutes, and the fix — one
	// timesyncd setting — was invisible until preflight was run by hand.
	if errors.Is(err, clockcheck.ErrClockUnusable) {
		return "clock_unusable"
	}
	return "operation_failed"
}

func startMetrics(
	ctx context.Context,
	address string,
) (*runmetrics.Metrics, <-chan error, func(), error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, nil, nil, errors.New("metrics address must be an IP host and port")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return nil, nil, nil, errors.New("metrics address must use a loopback IP")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, nil, nil, errors.New("listen for agent metrics on " + address +
			"; another runner may already use it — pass --metrics-address")
	}
	metrics := runmetrics.New(time.Now().UTC())
	server := &http.Server{
		Handler:           metrics,
		ReadHeaderTimeout: 2 * time.Second,
		WriteTimeout:      2 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	serverErrors := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- errors.New("agent metrics server stopped")
		}
	}()
	closeServer := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}
	go func() {
		<-ctx.Done()
		closeServer()
	}()
	return metrics, serverErrors, closeServer, nil
}

type devnetRuntime struct {
	profile agent.Profile
	engine  *execution.Engine
	store   *journal.Store
	control *control.StateFile
	// configPath lets each cycle re-read the alerts section, so an alert
	// threshold edit takes effect without restarting the runner. Alerts
	// authorize nothing, which is what makes a live re-read safe here and
	// wrong for anything inside the fingerprinted profile.
	configPath string
	statusPath string
}

// Alerts re-reads the current alert slots. On any read or validation failure
// it reports no alerts configured: for a notify-only feature, silence plus
// the evidence-available gauge is the fail-closed direction.
func (runtime *devnetRuntime) Alerts() (alertsConfig, bool) {
	cfg, err := readConfig(runtime.configPath)
	if err != nil || cfg.Alerts.validate(cfg.Swap) != nil {
		return alertsConfig{}, false
	}
	return cfg.Alerts, true
}

// SweepRegistration reports when this setup's destination was proven and when
// it activates, from the durable proof record beside the config. Zeroes mean
// no readable registration — which, for a sweep runner, is itself worth an
// operator's attention and renders as such on the metrics surface.
func (runtime *devnetRuntime) SweepRegistration() (registeredUnix, activeAfterUnix int64) {
	proof, err := readDestinationProof(filepath.Dir(runtime.configPath))
	if err != nil {
		return 0, 0
	}
	issued, err := time.Parse(time.RFC3339, proof.IssuedAt)
	if err != nil {
		return 0, 0
	}
	return issued.Unix(), proof.ActiveAfterUnix
}

func openDevnetRuntime(
	configPath string,
	requireFreshActivation bool,
) (*devnetRuntime, error) {
	cfg, err := readConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := cfg.Profile.Validate(); err != nil {
		return nil, fmt.Errorf("profile: %w", err)
	}
	if cfg.Profile.Cluster != "devnet" {
		return nil, errors.New("transaction execution is restricted to devnet")
	}
	profileFingerprint, err := cfg.Profile.Fingerprint()
	if err != nil {
		return nil, fmt.Errorf("profile fingerprint: %w", err)
	}
	mithrilURL := os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL")
	primaryURL := os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL")
	secondaryURL := os.Getenv("MITHRIL_AGENT_SECONDARY_RPC_URL")
	if mithrilURL == "" || primaryURL == "" || secondaryURL == "" {
		return nil, errors.New(
			"MITHRIL_AGENT_MITHRIL_RPC_URL and two independent evidence RPC URLs are required",
		)
	}
	mithrilNode, err := solanarpc.NewMithrilNode(mithrilURL, nil)
	if err != nil {
		return nil, fmt.Errorf("Mithril RPC: %w", err)
	}
	primary, err := newExternalRPC(primaryURL)
	if err != nil {
		return nil, fmt.Errorf("primary evidence RPC: %w", err)
	}
	secondary, err := newExternalRPC(secondaryURL)
	if err != nil {
		return nil, fmt.Errorf("secondary evidence RPC: %w", err)
	}
	lifecycle, err := txflow.New(mithrilNode, primary, secondary)
	if err != nil {
		return nil, err
	}
	observer, err := mcpobserve.New(mcpobserve.Config{
		Command:   cfg.MCP.Command,
		Args:      cfg.MCP.Args,
		Env:       mithrilMCPEnvironment(mithrilURL, primaryURL),
		Cluster:   cfg.Profile.Cluster,
		RPCOrigin: mithrilNode.Origin(),
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("MCP observer: %w", err)
	}
	policyProcess, err := policyclient.New(policyclient.Config{
		Command:     cfg.Policy.Command,
		PolicyPath:  cfg.Policy.PolicyPath,
		KeypairPath: cfg.Policy.KeypairPath,
		KeyID:       cfg.Policy.KeyID,
		PublicKey:   cfg.Policy.PublicKey,
	})
	if err != nil {
		return nil, fmt.Errorf("risk authority: %w", err)
	}
	signerProcess, err := signerclient.New(signerclient.Config{
		Command:     cfg.Signer.Command,
		PolicyPath:  cfg.Signer.PolicyPath,
		KeypairPath: cfg.Signer.KeypairPath,
	})
	if err != nil {
		return nil, fmt.Errorf("signer: %w", err)
	}
	submitterProcess, err := submitterclient.New(submitterclient.Config{
		Command:        cfg.Submitter.Command,
		PolicyPath:     cfg.Submitter.PolicyPath,
		PrivateKeyPath: cfg.Submitter.PrivateKeyPath,
		Env: []string{
			"MITHRIL_AGENT_MITHRIL_RPC_URL=" + mithrilURL,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("submitter: %w", err)
	}
	stateFile, err := control.NewStateFile(
		cfg.Control.StatePath,
		profileFingerprint,
		requireFreshActivation,
	)
	if err != nil {
		return nil, fmt.Errorf("control state: %w", err)
	}
	store, err := journal.OpenRotating(cfg.Journal.Path)
	if err != nil {
		return nil, err
	}
	engine, err := execution.New(
		store,
		observer,
		mithrilNode,
		policyProcess,
		signerProcess,
		submitterProcess,
		lifecycle,
		stateFile,
		nil,
	)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return &devnetRuntime{
		profile:    cfg.Profile,
		engine:     engine,
		store:      store,
		control:    stateFile,
		configPath: configPath,
		statusPath: operatorstatus.Path(cfg.Journal.Path),
	}, nil
}

func (runtime *devnetRuntime) Step(
	ctx context.Context,
) (execution.Result, journal.Stats, error) {
	result, err := runtime.engine.RunOnce(ctx, runtime.profile)
	if err != nil {
		return execution.Result{}, journal.Stats{}, err
	}
	// The balance rides on every cycle result, stamped with its own
	// observation time, so status and metrics carry the bounded value the
	// engine acted on rather than a separate read that could disagree.
	if lamports, observedUnix, ok := runtime.engine.LastBalance(); ok {
		result.BalanceLamports = lamports
		result.BalanceObservedUnix = observedUnix
	}
	stats, err := runtime.store.Stats()
	if err != nil {
		return result, journal.Stats{}, err
	}
	return result, stats, nil
}

func (runtime *devnetRuntime) Close() error {
	return runtime.store.Close()
}

func (runtime *devnetRuntime) Stats() (journal.Stats, error) {
	return runtime.store.Stats()
}

func (runtime *devnetRuntime) ControlStatus() (control.Status, error) {
	return runtime.control.Status()
}

func (runtime *devnetRuntime) StopNewActions(actionID, outcome string) error {
	return runtime.control.StopForTerminal(actionID, outcome)
}

func (runtime *devnetRuntime) RecordStatus(
	at time.Time,
	result execution.Result,
	stats journal.Stats,
	status control.Status,
) (operatorstatus.Action, error) {
	if err := operatorstatus.Write(runtime.statusPath, operatorstatus.Snapshot{
		Version:        operatorstatus.Version,
		ObservedAt:     at.UTC(),
		Profile:        runtime.profile.Name,
		ProfileVersion: runtime.profile.Version,
		Cluster:        runtime.profile.Cluster,
		Result:         result,
		Journal:        stats,
		Control:        status,
	}); err != nil {
		return operatorstatus.Action{}, err
	}
	snapshot, err := operatorstatus.Read(runtime.statusPath)
	if err != nil {
		return operatorstatus.Action{}, err
	}
	return snapshot.LastAction, nil
}

func runShadow(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "policy config JSON")
	observationPath := flags.String("observation", "", "observation JSON")
	journalPath := flags.String("journal", "", "append-only journal JSONL")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// The subcommands are the ones anyone reaches for; the flag form
			// below is the older single-shot shadow kept for existing scripts.
			// Listing only the flag form meant `shadow --help` never mentioned
			// the commands that do the work, so they could not be found at all.
			_, writeErr := fmt.Fprintln(output, `Usage:
  mithril-agent shadow policy --out PATH --observe ADDR --sell-at-usd N
                                       write a shadow policy to score against
  mithril-agent shadow run --policy PATH --dir PATH
                                       watch a live market, record what the rule
                                       would have done. Holds no key.
  mithril-agent shadow report --policy PATH --dir PATH [--day DATE] [--json]
                                       score one recorded day, one direction
  mithril-agent shadow backtest --policy PATH --dir PATH --buy-at-usd N
                                [--spread-bps N] [--day DATE] [--json]
                                       score a sell-then-buy-back ROUND TRIP over
                                       recorded prices, on one set of books

Each subcommand takes --help of its own.

Legacy single-shot form, kept for existing scripts:
  mithril-agent shadow --config PATH --observation PATH --journal PATH`)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *observationPath == "" || *journalPath == "" {
		return errors.New("shadow requires --config, --observation, and --journal")
	}

	cfg, err := readConfig(*configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var observation agent.Observation
	if err := readStrictJSON(*observationPath, &observation); err != nil {
		return fmt.Errorf("read observation: %w", err)
	}

	store, err := journal.OpenRotating(*journalPath)
	if err != nil {
		return err
	}
	defer store.Close()
	engine, err := agent.NewEngine(store, nil)
	if err != nil {
		return err
	}
	result, err := engine.RunShadow(cfg.Profile, observation)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}

func readStrictJSON(path string, out any) error {
	data, err := securefile.ReadPrivate(path, maxInputBytes)
	if err != nil {
		return err
	}
	return strictjson.Decode(data, out)
}

func readConfig(path string) (config, error) {
	var cfg config
	if err := readStrictJSON(path, &cfg); err != nil {
		return config{}, err
	}
	if err := cfg.validateProfileSelection(); err != nil {
		return config{}, err
	}
	if err := cfg.Alerts.validate(cfg.Swap); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func mithrilMCPEnvironment(rpcURL, referenceRPCURL string) []string {
	return []string{
		"MITHRIL_REFERENCE_RPC_URL=" + referenceRPCURL,
		"MITHRIL_RPC_URL=" + rpcURL,
	}
}
