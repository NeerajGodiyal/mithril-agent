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
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/signerclient"
	"github.com/Overclock-Validator/mithril-agent/statussocket"
	"github.com/Overclock-Validator/mithril-agent/submitterclient"
	"github.com/Overclock-Validator/mithril-agent/swapbuilder"
	"github.com/Overclock-Validator/mithril-agent/swaprun"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const rootUsage = `Mithril Agent — walletless Solana program and index workflows

New host:
  Read README.md, then ROADMAP.md and WALLETLESS_QUICKSTART.md. Use INDEXING.md
  for durable cursor and recovery details.
  These default workflows load no wallet or signing key.

Start with no host configuration:
  mithril-agent program --help          pin, read, decode, and simulate programs
  mithril-agent index --help            ingest and query rooted program activity
  mithril-agent explain                 plain-language capability summary
  mithril-agent walkthrough             read-only live-price walkthrough

Optional bounded Devnet trading pilot:
  Read QUICKSTART.md in this checkout, or the installed copy at:
  /usr/local/share/doc/mithril-agent/QUICKSTART.md
  mithril-agent strategy dca-plan ...   plan only; never arms, signs, or submits

The optional setup is one generated strategy: sell, buy, sweep, read-only
Telegram alerts, and one read-only MCP socket per leg. Do not mix it with the
legacy single-leg systemd units.

Read-only review of an installed strategy:
  mithril-agent status --status-socket /run/mithril-agent-status-sell.sock
  mithril-agent status --status-socket /run/mithril-agent-status-buy.sock
  mithril-agent status --status-socket /run/mithril-agent-status-sweep.sock

Run these strategy commands as the mithril-agent service identity, with
HOME=/var/lib/mithril-agent:
  mithril-agent start                   show the one next step; changes nothing
  mithril-agent strategy show           show every configured leg and grant
  mithril-agent strategy enable ...     grant bounded spending authority
  mithril-agent strategy stop --reason TEXT

Other supported tools:
  mithril-agent wallet check --file PATH
  mithril-agent wallet fund --file PATH
  mithril-agent doctor [--config PATH] [--json]
  mithril-agent mcp config [--socket PATH]
  mithril-agent mcp (--config PATH | --status-socket PATH)
  mithril-agent program inspect --idl PATH --program ADDRESS
  mithril-agent program pin --idl PATH --program ADDRESS --registry PATH
  mithril-agent program fetch --program ADDRESS --registry PATH --cluster CLUSTER --node-rpc LOOPBACK_URL --min-context-slot N
  mithril-agent program show --program ADDRESS --sha256 HEX --registry PATH
  mithril-agent program build --registry PATH --program ADDRESS --sha256 HEX --instruction NAME --fee-payer ADDRESS --recent-blockhash HASH ...
  mithril-agent program decode-account --registry PATH --program ADDRESS --sha256 HEX --account-type NAME (--data PATH | --index-dir PATH --account ADDRESS)
  mithril-agent program decode-event --registry PATH --program ADDRESS --sha256 HEX --event-type NAME --index-dir PATH --signature SIGNATURE
  mithril-agent program read-account --registry PATH --program ADDRESS --sha256 HEX --account-type NAME --account ADDRESS --cluster CLUSTER --node-rpc LOOPBACK_URL --min-context-slot N
  mithril-agent program simulate --registry PATH --program ADDRESS --sha256 HEX ...
  mithril-agent program mcp --workspace PATH --sha256 HEX
  mithril-agent program mcp-config --workspace PATH --sha256 HEX --name NAME
  mithril-agent index ingest --dir ABSOLUTE_PATH [--owner ADDRESS] [--account ADDRESS] [--mention ADDRESS]
  mithril-agent index doctor --dir ABSOLUTE_PATH
  mithril-agent index status --dir ABSOLUTE_PATH
  mithril-agent index query --dir ABSOLUTE_PATH [--owner ADDRESS] [--account ADDRESS]
  mithril-agent index transactions --dir ABSOLUTE_PATH [--signature SIGNATURE] [--mention ADDRESS]
  mithril-agent shadow policy --out PATH --observe ADDR (--adaptive | --sell-at-usd N [--buy-at-usd N])
  mithril-agent shadow portfolio --out PATH --limit-usd N --max-sol-usd N --book SPEC
  mithril-agent shadow allocation --portfolio PATH --instruction PATH --out-dir PATH
  mithril-agent shadow run --policy PATH --dir PATH
  mithril-agent shadow perps-paper-run --state-dir PATH
  mithril-agent shadow perps-tournament --tape PATH
  mithril-agent shadow perps-qualify --tape PATH
  mithril-agent shadow perps-walk-forward --tape PATH --tape PATH
  mithril-agent shadow perps-restore --state-dir PATH --symbol SOL
  mithril-agent shadow report --policy PATH --dir PATH
  mithril-agent shadow review --policy PATH --dir PATH --days N
  mithril-agent shadow search --policy PATH --dir PATH --train-day DATE --validation-day DATE
  mithril-agent shadow select --policy PATH --candidate PATH --pointer PATH --lifecycle-lock PATH
  mithril-agent shadow challenge --policy PATH --champion-pointer PATH --challenger PATH --champion-dir PATH --challenger-dir PATH --days N
  mithril-agent shadow auto-select --policy PATH --champion-pointer PATH --challenger-pointer PATH --champion-dir PATH --challenger-dir PATH --days N --rollback-pointer PATH --lifecycle-lock PATH [--outcome-journal PATH]
  mithril-agent shadow research-outcomes --journal PATH [--limit 16] [--prompt-safe --policy PATH --max-age DURATION]
  mithril-agent shadow research-context --policy PATH
  mithril-agent shadow restore --policy PATH --champion-pointer PATH --rollback-pointer PATH --challenger-pointer PATH --challenger-candidate-dir PATH --lifecycle-lock PATH
  mithril-agent shadow research-mcp --policy PATH --journal-dir PATH ...
  mithril-agent research packet-record --in PATH --latest PATH [--archive-dir DIR]
  mithril-agent proposal evidence-check --primary-trust-domain NAME --secondary-trust-domain NAME --archive-probe-signature SIGNATURE
  mithril-agent proposal check --taker ADDR --input-mint ADDR --output-mint ADDR --amount N
  mithril-agent proposal recheck --candidate ABSOLUTE_PATH [provider bindings]
  mithril-agent proposal prepare --candidate ABSOLUTE_PATH --authority-policy PATH
  mithril-agent proposal review --request ABSOLUTE_PATH --signer-policy PATH
  mithril-agent proposal approval-create --request ABSOLUTE_PATH --authority-policy PATH --out PATH
  mithril-agent proposal key-create --kind KIND --out ABSOLUTE_PATH
  mithril-agent proposal policy-create --route-policy PATH --check-result PATH --out DIR
  mithril-agent proposal policy-check --authority-policy PATH --signer-policy PATH --submitter-policy PATH
  mithril-agent proposal bundle-check --candidate PATH --policy-dir DIR
  mithril-agent proposal self-hosted-check --host HOST --user USER --policy PATH
  mithril-agent proposal authority-check --policy PATH --key PATH
  mithril-agent proposal submitter-check --policy PATH --key PATH
  mithril-agent proposal canary-check --policy-dir DIR --operator-socket PATH
  mithril-agent proposal turnkey-check --api-key-file PATH --api-public-key KEY --organization ID --sign-with RESOURCE --expected-address ADDRESS
  mithril-agent proposal turnkey-policy --candidate PATH --policy PATH --api-user ID --out PATH
  mithril-agent audit snapshot --config ABSOLUTE_PATH
  mithril-agent journal verify --path ABSOLUTE_PATH
  mithril-agent clock-check --config PATH
  mithril-agent version

MCP and Telegram are read-only. Neither can enable, sign, or submit a trade.
Shadow holds no wallet signing key. Proposal tools may use a provider API key
but never a wallet signing key; neither path can sign or submit.
Trading remains Devnet-only in this pilot.

Legacy single-leg check, demo, preflight, and swap commands remain available
for existing deployments; they are not the full-strategy first-run path.`

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
	Evidence proposalcheck.ProviderBindings `json:"evidence,omitempty"`
	Control  struct {
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
		return errors.New("evidence providers need distinct short organization names using lowercase letters, numbers, dot, underscore, or hyphen")
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
		if len(args) > 1 && args[1] == "market" {
			return runShadowMarket(ctx, args[2:], output)
		}
		if len(args) > 1 && args[1] == "portfolio" {
			return runShadowPortfolio(args[2:], output)
		}
		if len(args) > 1 && args[1] == "allocation" {
			return runShadowAllocation(args[2:], output)
		}
		if len(args) > 1 && args[1] == "perps-paper-run" {
			return runShadowPerpsPaper(ctx, args[2:], output)
		}
		if len(args) > 1 && args[1] == "perps-tournament" {
			return runShadowPerpsTournament(args[2:], output)
		}
		if len(args) > 1 && args[1] == "perps-qualify" {
			return runShadowPerpsQualification(args[2:], output)
		}
		if len(args) > 1 && args[1] == "perps-walk-forward" {
			return runShadowPerpsWalkForward(args[2:], output)
		}
		if len(args) > 1 && args[1] == "perps-restore" {
			return runShadowPerpsRestore(args[2:], output)
		}
		if len(args) > 1 && args[1] == "run" {
			return runShadowRun(ctx, args[2:], output)
		}
		if len(args) > 1 && args[1] == "report" {
			return runShadowReport(args[2:], output)
		}
		if len(args) > 1 && args[1] == "review" {
			return runShadowReview(args[2:], output)
		}
		if len(args) > 1 && args[1] == "policy" {
			return runShadowPolicy(args[2:], output)
		}
		if len(args) > 1 && args[1] == "backtest" {
			return runShadowBacktest(args[2:], output)
		}
		if len(args) > 1 && args[1] == "search" {
			return runShadowSearch(args[2:], output)
		}
		if len(args) > 1 && args[1] == "select" {
			return runShadowSelect(args[2:], output)
		}
		if len(args) > 1 && args[1] == "challenge" {
			return runShadowChallenge(args[2:], output)
		}
		if len(args) > 1 && args[1] == "auto-select" {
			return runShadowAutoSelect(args[2:], output)
		}
		if len(args) > 1 && args[1] == "research-outcomes" {
			return runShadowResearchOutcomeSummary(args[2:], output)
		}
		if len(args) > 1 && args[1] == "research-context" {
			return runShadowResearchContext(args[2:], output)
		}
		if len(args) > 1 && args[1] == "restore" {
			return runShadowRestore(args[2:], output)
		}
		if len(args) > 1 && args[1] == "research-mcp" {
			return runShadowResearchMCP(ctx, args[2:], os.Stdin, output)
		}
		return runShadow(args[1:], output)
	case "proposal":
		if len(args) == 1 || args[1] == "help" || args[1] == "-h" || args[1] == "--help" {
			_, err := fmt.Fprintln(output, proposalUsage)
			return err
		}
		if len(args) > 1 && args[1] == "check" {
			return runProposalCheck(ctx, args[2:], output)
		}
		if len(args) > 1 && args[1] == "evidence-check" {
			return runProposalEvidenceCheck(ctx, args[2:], output)
		}
		if len(args) > 1 && args[1] == "recheck" {
			return runProposalRecheck(ctx, args[2:], output)
		}
		if len(args) > 1 && args[1] == "prepare" {
			return runProposalPrepare(ctx, args[2:], output, time.Now)
		}
		if len(args) > 1 && args[1] == "review" {
			return runProposalReview(args[2:], output)
		}
		if len(args) > 1 && args[1] == "approval-create" {
			return runProposalApprovalCreate(ctx, args[2:], output)
		}
		if len(args) > 1 && args[1] == "key-create" {
			return runProposalKeyCreate(args[2:], output)
		}
		if len(args) > 1 && args[1] == "policy-create" {
			return runProposalPolicyCreate(args[2:], output, time.Now)
		}
		if len(args) > 1 && args[1] == "policy-check" {
			return runProposalPolicyCheck(args[2:], output)
		}
		if len(args) > 1 && args[1] == "bundle-check" {
			return runProposalBundleCheck(args[2:], output)
		}
		if len(args) > 1 && args[1] == "self-hosted-check" {
			return runProposalSelfHostedCheck(ctx, args[2:], output)
		}
		if len(args) > 1 && args[1] == "authority-check" {
			return runProposalAuthorityCheck(ctx, args[2:], output)
		}
		if len(args) > 1 && args[1] == "submitter-check" {
			return runProposalSubmitterCheck(ctx, args[2:], output)
		}
		if len(args) > 1 && args[1] == "canary-check" {
			return runProposalCanaryCheck(ctx, args[2:], output)
		}
		if len(args) > 1 && args[1] == "turnkey-check" {
			return runProposalTurnkeyCheck(ctx, args[2:], output)
		}
		if len(args) > 1 && args[1] == "turnkey-policy" {
			return runProposalTurnkeyPolicy(args[2:], output)
		}
		return errors.New("proposal requires the check, evidence-check, recheck, prepare, review, approval-create, key-create, policy-create, policy-check, bundle-check, self-hosted-check, authority-check, submitter-check, canary-check, turnkey-check, or turnkey-policy subcommand")
	case "research":
		return runResearch(args[1:], output)
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
	case "audit":
		return runAudit(args[1:], output)
	case "status":
		return runStatus(args[1:], output)
	case "mcp":
		if len(args) > 1 && args[1] == "config" {
			return runMCPConfig(args[2:], output)
		}
		return runMCP(ctx, args[1:], os.Stdin, output)
	case "program":
		return runProgram(ctx, args[1:], output)
	case "index":
		return runIndex(ctx, args[1:], os.Stdin, output)
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
	"preflight", "check", "demo", "status", "swap", "shadow", "proposal", "journal", "audit",
	"mcp", "program", "index", "research", "clock-check", "version", "help",
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
	operatorSocket := flags.String("operator-socket", defaultOperatorSocket,
		"root-only submitter operator socket")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(
				output,
				"Usage: mithril-agent devnet-enable --config PATH --operator-socket PATH --duration DURATION [--max-actions N] --reason TEXT",
			)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *reason == "" {
		return errors.New("devnet-enable requires --config, --duration, and --reason")
	}
	if !filepath.IsAbs(*operatorSocket) || filepath.Clean(*operatorSocket) != *operatorSocket {
		return errors.New("operator socket must be an absolute clean path")
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
	issuedAt := time.Now().UTC()
	expiresAt := issuedAt.Add(*duration)
	status, revision, err := operatorControlStatus(*operatorSocket)
	if err != nil {
		return err
	}
	if status.TerminalOutcome != "" {
		return errors.New("terminal action requires acknowledgement before enabling")
	}
	if err := enableOperatorControl(
		*operatorSocket, revision, issuedAt, expiresAt, uint32(*maxActions), *reason,
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
	_, _, _, fingerprint, err := cfg.activeProfile()
	if err != nil {
		return err
	}
	status, err := stopControlState(cfg.Control.StatePath, fingerprint, *reason)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Mode string `json:"mode"`
	}{Mode: status.Mode})
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
	configPath string
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
	if p.config.Swap == nil {
		return agentmcp.StrategySettings{}, nil
	}
	projection, err := strategyProjection(*p.config.Swap, p.configPath)
	if err != nil {
		return agentmcp.StrategySettings{}, err
	}
	status, err := p.control.Status()
	if err != nil {
		return agentmcp.StrategySettings{}, err
	}
	return strategySettings(projection, status, p.now().UTC()), nil
}

func strategyProjection(
	profile swaprun.Profile,
	configPath string,
) (operatorstatus.StrategyProjection, error) {
	if err := profile.Validate(); err != nil {
		return operatorstatus.StrategyProjection{}, err
	}
	projection := operatorstatus.StrategyProjection{
		Configured: true, Direction: "sell", InputAmount: profile.InputLamports,
		DailyCap: profile.DailyDebitCapLamports, MaxFeeLamports: profile.MaxFeeLamports,
		FundedTradesPerDay: profile.FundedTradesPerDay(),
	}
	if profile.IsBuy() {
		projection.Direction = "buy"
		projection.InputAmount = profile.InputTokenAmount
		projection.DailyCap = profile.DailyInputTokenCap
	}
	if trigger := profile.PriceTrigger; trigger != nil {
		projection.PriceDirection = string(trigger.Direction)
		projection.PriceThresholdMicros = trigger.ThresholdMicros
	}
	strategy, _ := discoverStrategy()
	belongsToRecordedStrategy := configPath != "" &&
		(configPath == strategy.sell || configPath == strategy.buy)
	if belongsToRecordedStrategy && strategy.sweep != "" {
		if cfg, err := readConfig(strategy.sweep); err == nil && cfg.hasLegacyProfile() &&
			cfg.Profile.Validate() == nil && cfg.Profile.Source == profile.Owner() &&
			cfg.Profile.Cluster == profile.Cluster {
			projection.SweepConfigured = true
			projection.SweepKeepLamports = cfg.Profile.ReserveLamports
			projection.SweepMaxLamports = cfg.Profile.MaxTransferLamports
			projection.SweepDailyLamports = cfg.Profile.DailyCapLamports
			projection.SweepActiveAfter = time.Unix(cfg.Profile.ScheduleAnchorUnix, 0).UTC()
			// Re-verified here, not trusted from the file: a registration that
			// no longer verifies is exactly what an operator needs told.
			if proof, err := readDestinationProof(filepath.Dir(strategy.sweep)); err == nil {
				projection.SweepProofValid = verifySweepDestinationProof(
					proof.AgentAccount, proof.Destination,
					proof.Nonce, proof.IssuedAt, proof.SignatureBase58,
				) == nil && proof.AgentAccount == cfg.Profile.Source &&
					proof.Destination == cfg.Profile.Destination
			}
		}
	}
	if err := operatorstatus.ValidateStrategyProjection(
		profile.Name, profile.Version, profile.Cluster, projection,
	); err != nil {
		return operatorstatus.StrategyProjection{}, err
	}
	return projection, nil
}

func strategySettings(
	projection operatorstatus.StrategyProjection,
	status control.Status,
	now time.Time,
) agentmcp.StrategySettings {
	settings := agentmcp.StrategySettings{Configured: projection.Configured}
	if !projection.Configured {
		return settings
	}
	settings.Direction = "sell SOL for devUSDC"
	settings.InputPerAction = formatUnits(projection.InputAmount, 9) + " SOL"
	settings.DailyCap = formatUnits(projection.DailyCap, 9) + " SOL"
	if projection.Direction == "buy" {
		settings.Direction = "buy SOL with devUSDC"
		settings.InputPerAction = formatUnits(projection.InputAmount, 6) + " devUSDC"
		settings.DailyCap = formatUnits(projection.DailyCap, 6) + " devUSDC"
	}
	settings.MaxFee = formatUnits(projection.MaxFeeLamports, 9) + " SOL"
	settings.FundedTradesPerDay = projection.FundedTradesPerDay
	if projection.PriceDirection != "" {
		settings.PriceRule = projection.PriceDirection + " $" +
			formatUnits(projection.PriceThresholdMicros, 6)
	}
	var live bool
	settings.ControlMode, settings.ControlGrant, live = describeControlGrant(
		status.Mode, status.ExpiresAt, status.RemainingActions, now)
	if settings.ControlGrant != "" && !live {
		settings.ControlGrant += " (cannot act)"
	}
	settings.SweepConfigured = projection.SweepConfigured
	settings.SweepProofValid = projection.SweepProofValid
	if projection.SweepConfigured {
		settings.SweepKeepBehind = formatUnits(projection.SweepKeepLamports, 9) + " SOL"
		settings.SweepMaxPerSend = formatUnits(projection.SweepMaxLamports, 9) + " SOL"
		settings.SweepDailyCap = formatUnits(projection.SweepDailyLamports, 9) + " SOL"
		settings.SweepActiveAfter = projection.SweepActiveAfter.UTC().Format(time.RFC3339)
	}
	return settings
}

// Strategy uses only the address-free projection carried by the bounded status
// socket. The MCP identity never receives the private config.
func (p *socketOperatorProvider) Strategy() (agentmcp.StrategySettings, error) {
	snapshot, err := p.snapshot()
	if err != nil {
		return agentmcp.StrategySettings{}, err
	}
	return strategySettings(snapshot.Strategy, snapshot.Control, p.now().UTC()), nil
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
		configPath: configPath,
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
	signerSocket := flags.String("signer-socket", "", "override signer transport with this local socket")
	riskSocket := flags.String("risk-socket", "", "override risk authority transport with this local socket")
	submitterSocket := flags.String("submitter-socket", "", "override submitter transport with this local socket")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent devnet-run --config PATH --interval DURATION [--metrics-address ADDRESS] [--signer-socket PATH] [--risk-socket PATH] [--submitter-socket PATH]")
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
	if *signerSocket != "" &&
		(!filepath.IsAbs(*signerSocket) || filepath.Clean(*signerSocket) != *signerSocket) {
		return errors.New("signer socket must be an absolute clean path")
	}
	if *riskSocket != "" &&
		(!filepath.IsAbs(*riskSocket) || filepath.Clean(*riskSocket) != *riskSocket) {
		return errors.New("risk authority socket must be an absolute clean path")
	}
	if *submitterSocket != "" &&
		(!filepath.IsAbs(*submitterSocket) || filepath.Clean(*submitterSocket) != *submitterSocket) {
		return errors.New("submitter socket must be an absolute clean path")
	}
	if err := requireSystemdAuthoritySockets(*signerSocket, *riskSocket, *submitterSocket); err != nil {
		return err
	}
	runtime, err := openDevnetRuntimeWithSockets(*configPath, true, *signerSocket, *riskSocket, *submitterSocket)
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
	StopPreservingRecovery(string) error
}

// runtimeControl is the control surface shared by foreground runs and the
// isolated submitter service. Supervised runners receive the socket-backed
// implementation and never open the writable activation file themselves.
type runtimeControl interface {
	NoNewActions() (bool, error)
	Status() (control.Status, error)
	StopPreservingRecovery(string) error
	StopForTerminal(string, string) error
	TerminalLatch() (string, string, error)
	WithSendBarrier(string, func() error) (bool, error)
	WithRecoverySendBarrier(string, func() error) (bool, error)
}

type runnerShutdownRuntime interface {
	Close() error
}

func shutdownRunner(runErr *error, state runnerShutdownControl, runtime runnerShutdownRuntime) {
	if err := state.StopPreservingRecovery("runner shutdown"); err != nil &&
		!errors.Is(err, control.ErrRecoveryPending) {
		*runErr = errors.Join(*runErr, fmt.Errorf("stop new actions during runner shutdown: %w", err))
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
	// The MCP observer failed to return a usable account/health snapshot. The
	// execution engine keeps the detailed cause for protected logs; the public
	// status and metric need one stable label that reveals no provider detail.
	if errors.Is(err, execution.ErrObservationUnavailable) {
		return "observation_not_ready"
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
	control runtimeControl
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
	return openDevnetRuntimeWithSigner(configPath, requireFreshActivation, "")
}

func openDevnetRuntimeWithSigner(
	configPath string,
	requireFreshActivation bool,
	signerSocket string,
) (*devnetRuntime, error) {
	return openDevnetRuntimeWithSockets(configPath, requireFreshActivation, signerSocket, "", "")
}

func openDevnetRuntimeWithSockets(
	configPath string,
	requireFreshActivation bool,
	signerSocket string,
	riskSocket string,
	submitterSocket string,
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
	providers, err := openBoundRPCProviders(cfg, mithrilURL, primaryURL, secondaryURL)
	if err != nil {
		return nil, err
	}
	lifecycle, err := txflow.New(providers.mithril, providers.primary, providers.secondary)
	if err != nil {
		return nil, err
	}
	observer, err := mcpobserve.New(mcpobserve.Config{
		Command:   cfg.MCP.Command,
		Args:      cfg.MCP.Args,
		Env:       mithrilMCPEnvironment(mithrilURL, primaryURL),
		Cluster:   cfg.Profile.Cluster,
		RPCOrigin: providers.mithril.Origin(),
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("MCP observer: %w", err)
	}
	policyProcess, err := policyclient.New(policyclient.Config{
		Command:     cfg.Policy.Command,
		PolicyPath:  cfg.Policy.PolicyPath,
		KeypairPath: cfg.Policy.KeypairPath,
		SocketPath:  riskSocket,
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
		SocketPath:  signerSocket,
	})
	if err != nil {
		return nil, fmt.Errorf("signer: %w", err)
	}
	submitterProcess, err := submitterclient.New(submitterclient.Config{
		Command:        cfg.Submitter.Command,
		PolicyPath:     cfg.Submitter.PolicyPath,
		PrivateKeyPath: cfg.Submitter.PrivateKeyPath,
		SocketPath:     submitterSocket,
		Env: []string{
			"MITHRIL_AGENT_MITHRIL_RPC_URL=" + mithrilURL,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("submitter: %w", err)
	}
	var state runtimeControl = submitterProcess
	if submitterSocket == "" {
		stateFile, stateErr := control.NewStateFile(
			cfg.Control.StatePath, profileFingerprint, requireFreshActivation,
		)
		if stateErr != nil {
			return nil, fmt.Errorf("control state: %w", stateErr)
		}
		state = stateFile
	}
	store, err := journal.OpenRotating(cfg.Journal.Path)
	if err != nil {
		return nil, err
	}
	engine, err := execution.New(
		store,
		observer,
		providers.mithril,
		policyProcess,
		signerProcess,
		submitterProcess,
		lifecycle,
		state,
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
		control:    state,
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
  mithril-agent shadow policy --out PATH --observe ADDR (--adaptive | --sell-at-usd N [--buy-at-usd N])
                                       write an adaptive, one-way, or round-trip shadow policy
  mithril-agent shadow run --policy PATH --dir PATH
                                       watch a live market, record what the rule
                                       would have done. Holds no key.
  mithril-agent shadow perps-paper-run --state-dir PATH
                                       run bounded, signer-free SOL/BTC/ETH perps
                                       paper scenarios from public market data
  mithril-agent shadow perps-tournament --tape PATH
                                       compare research strategies on one
                                       verified private v3 paper tape; JSON only
  mithril-agent shadow perps-qualify --tape PATH
                                       compare all risk and strategy pairs, then
                                       test one leader on untouched data; JSON only
  mithril-agent shadow perps-walk-forward --tape PATH --tape PATH
                                       choose on earlier sealed tapes, then test
                                       the fixed leader on the held-out tape; JSON only
  mithril-agent shadow perps-restore --state-dir PATH --symbol SOL
                                       restore the previous paper-only perps plan
  mithril-agent shadow market collect --market NAME --observe ADDR --journal PATH
                                       collect immutable market-admission evidence
  mithril-agent shadow market evaluate --journal PATH --out PATH
                                       evaluate the latest complete evidence
                                       window; never starts a strategy
  mithril-agent shadow report --policy PATH --dir PATH [--day DATE] [--json]
                                       verify and score one recorded day
  mithril-agent shadow review --policy PATH --dir PATH --days N [--json]
                                       verify consecutive complete Mainnet days
                                       for operator review; never approves
  mithril-agent shadow challenge --policy PATH --champion-pointer PATH
                                 --challenger PATH --champion-dir PATH
                                 --challenger-dir PATH --days N
                                       compare paired forward paper evidence;
                                       read-only and never selects
  mithril-agent shadow backtest --policy PATH --dir PATH [--buy-at-usd N]
                                [--spread-bps N] [--day DATE] [--json]
                                       score a sell-then-buy-back ROUND TRIP over
                                       recorded prices; fixed policies require
                                       the buy price
  mithril-agent shadow search --policy PATH --dir PATH
                              --train-day DATE --validation-day DATE
                              [--spread-bps N] [--candidate-out PATH]
                                       choose bounded parameters on one recorded
                                       day and score the exact candidate on a
                                       later untouched day;
                                       research only, never authorizes
  mithril-agent shadow select --policy PATH --candidate PATH --pointer PATH --lifecycle-lock PATH
                                       atomically select an immutable paper
                                       candidate for startup/next UTC day
  mithril-agent shadow auto-select --policy PATH --champion-pointer PATH
                                   --challenger-pointer PATH --champion-dir PATH
                                   --challenger-dir PATH --days N --rollback-pointer PATH
                                   --lifecycle-lock PATH
                                       select only a forward-qualified paper
                                       challenger and preserve rollback
  mithril-agent shadow research-outcomes --journal PATH [--limit 16] [--prompt-safe --policy PATH --max-age DURATION]
                                       read bounded advisory outcomes from
                                       Hermes-backed paper candidates
  mithril-agent shadow research-context --policy PATH
                                       print only the exact current adaptive
                                       values needed by paper research
  mithril-agent shadow restore --policy PATH --champion-pointer PATH
                               --rollback-pointer PATH --challenger-pointer PATH
                               --challenger-candidate-dir PATH --lifecycle-lock PATH
                                       restore the paper champion and retire
                                       the rolled-back challenger
  mithril-agent shadow research-mcp --policy PATH --journal-dir PATH ...
                                       serve bounded local paper-research tools;
                                       cannot authorize, sign, or submit

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
