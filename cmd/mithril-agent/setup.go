package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/telegramoperator"
)

// setup is the guided path: read each line, press Enter to accept, and end up
// with a working configuration. It exists because the underlying `swap setup`
// needs a dozen exact flags and one number copied by hand from a separate
// discovery command — correct, but not something to hand a reviewer.
//
// It is a thin orchestrator. Every decision is still made by the same
// underlying functions, so the guided path and the scripted path cannot drift.
//
// It never authorises a trade. The last thing it does is tell the operator
// which separate, explicit command would.
const setupUsage = `Usage: mithril-agent setup [options]

Guided setup. Answer or press Enter to accept each default. Nothing is signed
or submitted, and no trade is authorised at any point.

  --dir PATH              where the private setup lives
  --direction sell|buy    which way the demonstration trades
  --mithril-command PATH  the Mithril node executable
  --mithril-config PATH   the Mithril node's config.toml
  --node-command PATH     the pinned Node.js runtime (24.18+)
  --quote-script PATH     the Orca quote adapter
  --yes                   do not ask; use the values given and the defaults

Any value given as a flag becomes that question's answer, so the same run can
be driven by hand or from a script. With --yes and no terminal, nothing is
asked at all.`

type setupChoices struct {
	directory      string
	direction      string
	accountKeypair string
	mithrilCommand string
	mithrilConfig  string
	nodeCommand    string
	quoteScript    string
}

// A supervised installation puts everything the agent needs in one directory.
// Looking there first is the difference between an operator pressing Enter and
// an operator being asked for absolute paths they have no way to know.
//
// Only the documented install location belongs here. Site-specific directory
// names from somebody else's deployment would be noise on every other machine,
// and would quietly imply this software knows about installations it does not.
var installedLibexecDirs = []string{
	"/usr/local/libexec/mithril-agent",
}

// detectInstalled returns the first of the named files that exists inside a
// known installation directory.
func detectInstalled(names ...string) string {
	for _, dir := range installedLibexecDirs {
		for _, name := range names {
			candidate := filepath.Join(dir, name)
			if info, err := os.Lstat(candidate); err == nil && info.Mode().IsRegular() {
				return candidate
			}
		}
	}
	return ""
}

// detectNodeConfig looks where somebody who already runs Mithril would actually
// have their node's configuration — `mithril config init` writes config.toml
// into the working directory, and ~/.mithril is the data directory — before
// falling back to the supervised-install locations.
//
// The audience for this is a validator operator running their own node, not a
// user of our packaging, so their layout is checked first.
func detectNodeConfig() string {
	candidates := []string{"config.toml", "mithril.toml"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".mithril", "config.toml"),
			filepath.Join(home, ".mithril", "mithril.toml"))
	}
	candidates = append(candidates,
		"/etc/mithril/config.toml",
		"/var/lib/mithril-agent/node.toml",
		"/var/lib/mithril-agent/mithril.toml")
	for _, candidate := range candidates {
		info, err := os.Lstat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if absolute, err := filepath.Abs(candidate); err == nil {
			return absolute
		}
	}
	return ""
}

// detectSourceAdapter finds the Orca quote adapter inside a source checkout,
// which is where somebody who cloned this and ran `make adapter` will have it.
func detectSourceAdapter() string {
	candidates := []string{filepath.Join("adapters", "orca", "quote.mjs")}
	if executable, err := os.Executable(); err == nil {
		// ./bin/mithril-agent -> the checkout root is one level up.
		root := filepath.Dir(filepath.Dir(executable))
		candidates = append(candidates,
			filepath.Join(root, "adapters", "orca", "quote.mjs"))
	}
	for _, candidate := range candidates {
		info, err := os.Lstat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if absolute, err := filepath.Abs(candidate); err == nil {
			return absolute
		}
	}
	return ""
}

// firstNonEmpty picks the first candidate that was actually found.
func firstNonEmpty(candidates ...string) string {
	for _, candidate := range candidates {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func runSetup(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("dir", "", "private setup directory")
	assumeYes := flags.Bool("yes", false, "accept every default without asking")
	// Pre-seeded answers. Without these, setup can only ever be run by hand:
	// a non-interactive run takes every default and there is no way to supply
	// a path it cannot guess, which makes deployment and testing impossible.
	given := setupChoices{}
	flags.StringVar(&given.direction, "direction", "", "sell or buy")
	flags.StringVar(&given.mithrilCommand, "mithril-command", "", "Mithril node executable")
	flags.StringVar(&given.mithrilConfig, "mithril-config", "", "Mithril node config.toml")
	flags.StringVar(&given.nodeCommand, "node-command", "", "Node.js runtime")
	flags.StringVar(&given.quoteScript, "quote-script", "", "Orca quote adapter")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, setupUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("setup takes no positional arguments")
	}

	interactive := !*assumeYes && stdinIsTerminal()
	p := newPrompter(os.Stdin, output, interactive)

	p.sayf("Mithril Agent — guided setup")
	p.sayf("============================")
	p.sayf("")
	p.sayf("Press Enter to accept each default. Nothing is signed or sent, and no")
	p.sayf("trade is authorised by this command. Stop any time with Ctrl-C.")
	if !interactive {
		p.sayf("")
		p.sayf("(Not a terminal, so every default is taken automatically.)")
	}

	given.directory = *dir
	choices, err := gatherSetupChoices(p, given)
	if err != nil {
		return err
	}
	if err := ensureAgentAccount(p, choices.accountKeypair, output); err != nil {
		return err
	}
	if err := guideTelegramLink(p); err != nil {
		return err
	}
	configPath, err := configureSwapProfile(ctx, p, choices, output)
	if err != nil {
		return err
	}
	return finishGuidedSetup(p, choices, configPath)
}

func gatherSetupChoices(p *prompter, given setupChoices) (setupChoices, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	defaultDir := given.directory
	if defaultDir == "" {
		defaultDir = filepath.Join(home, ".mithril-agent")
	}

	directory, err := p.ask("Where should the private setup live?", defaultDir)
	if err != nil {
		return setupChoices{}, err
	}
	direction, err := p.ask(
		"Which direction should the demonstration trade?\n  (sell = SOL for devUSDC, buy = devUSDC for SOL)",
		firstNonEmpty(given.direction, "sell"))
	if err != nil {
		return setupChoices{}, err
	}
	if direction != "sell" && direction != "buy" {
		return setupChoices{}, errors.New("direction must be sell or buy")
	}

	account, err := p.ask("Where is the agent's Devnet account keypair?",
		filepath.Join(directory, "agent-account.json"))
	if err != nil {
		return setupChoices{}, err
	}
	mithril, err := p.ask("Path to the Mithril node executable", firstNonEmpty(
		given.mithrilCommand,
		detectInstalled("mithril-node", "mithril-mcp"), detectExecutable("mithril")))
	if err != nil {
		return setupChoices{}, err
	}
	// The agent runs the node as an MCP monitor to prove the node is healthy
	// before it acts, and that invocation needs the node's own config. Omitting
	// this question produced a configuration that looked complete and could
	// never observe anything.
	mithrilConfig, err := p.ask(
		"Path to the Mithril node's config.toml"+
			"\n  (the node whose health the agent checks before acting)",
		firstNonEmpty(given.mithrilConfig, detectNodeConfig()))
	if err != nil {
		return setupChoices{}, err
	}
	node, err := p.ask("Path to the pinned Node.js runtime (24.18.x)", firstNonEmpty(
		given.nodeCommand, detectInstalled("node"), detectExecutable("node")))
	if err != nil {
		return setupChoices{}, err
	}
	quote, err := p.ask("Path to the Orca quote adapter (quote.mjs)", firstNonEmpty(
		given.quoteScript, detectSourceAdapter(), detectInstalled("quote.mjs"),
		filepath.Join(directory, "orca", "quote.mjs")))
	if err != nil {
		return setupChoices{}, err
	}

	return setupChoices{
		directory: directory, direction: direction, accountKeypair: account,
		mithrilCommand: mithril, mithrilConfig: mithrilConfig,
		nodeCommand: node, quoteScript: quote,
	}, nil
}

// ensureAgentAccount creates the dedicated Devnet account only when one is not
// already there. It never replaces an existing key.
func ensureAgentAccount(p *prompter, path string, output io.Writer) error {
	if _, err := os.Lstat(path); err == nil {
		p.sayf("\nAgent account: already present, leaving it alone.")
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect the agent account path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.New("could not create the setup directory")
	}
	p.sayf("\nNo agent account yet — creating a Devnet-only one.")
	return runWalletNew([]string{"--file", path}, output)
}

// guideTelegramLink reports whether the optional operator surface is wired.
//
// It deliberately never PROMPTS for the bot token. A secret typed at a prompt
// lands in scrollback, in the terminal's history, and in any session recording;
// an environment file read by the service is the only place it belongs. So the
// wizard checks, explains, and moves on.
func guideTelegramLink(p *prompter) error {
	tokenSet := os.Getenv(telegramoperator.BotTokenEnvironment) != ""
	chatsSet := os.Getenv(telegramoperator.AllowedIDsEnvironment) != ""

	if tokenSet && chatsSet {
		p.sayf("\nTelegram: configured. It is read-only — it can report status but")
		p.sayf("cannot enable, sign, or submit anything.")
		return nil
	}

	p.sayf("\nTelegram (optional)")
	p.sayf("-------------------")
	p.sayf("Telegram gives you read-only status on your phone. It is optional, and")
	p.sayf("the demonstration works without it.")
	p.sayf("")
	p.sayf("It is not configured here on purpose: a bot token typed at a prompt")
	p.sayf("would end up in your shell history and scrollback. Put it in the")
	p.sayf("service environment instead:")
	p.sayf("")
	p.sayf("  %s=<token from @BotFather>", telegramoperator.BotTokenEnvironment)
	p.sayf("  %s=<your numeric chat id>", telegramoperator.AllowedIDsEnvironment)
	p.sayf("")
	p.sayf("Only the chat IDs you list can talk to it, and it never accepts a")
	p.sayf("command that would authorise a trade.")
	return nil
}

// configureSwapProfile builds the swap profile in the same breath as the quote
// it is built from.
//
// The scripted path takes --confirm-min-output-amount: the operator reads a
// number from a discovery command and types it into the next one, and setup
// re-quotes and refuses unless the two match. That gate exists so a human
// agrees to the exact floor that will be written into the policy — but a
// number carried by hand between two commands goes stale the moment the market
// moves, and the operator's only recourse is to run both commands again.
//
// Here the same human agreement happens against the quote just read, inside
// one command. The gate is not relaxed: without a "yes" no profile is written.
//
// It returns the config path when one was written, and "" when the step was
// skipped for a reason already explained to the operator.
func configureSwapProfile(
	ctx context.Context,
	p *prompter,
	choices setupChoices,
	output io.Writer,
) (string, error) {
	profileDir := filepath.Join(choices.directory, "profile")
	configPath := filepath.Join(profileDir, "config.json")

	if len(missingSetupInputs(choices)) > 0 {
		return "", nil
	}
	// The setup directory is normally created as a side effect of writing the
	// agent account into it. An operator who points at an account they already
	// have — an entirely reasonable thing to do — would otherwise reach the
	// profile step with no directory at all, and be told its parent was
	// "unsafe" when the truth is it does not exist.
	if err := os.MkdirAll(choices.directory, 0o700); err != nil {
		return "", errors.New("could not create the setup directory")
	}
	if _, err := os.Lstat(profileDir); err == nil {
		p.sayf("\nTrading profile: already configured, leaving it alone.")
		return configPath, nil
	}
	if os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL") == "" ||
		os.Getenv("MITHRIL_AGENT_SECONDARY_RPC_URL") == "" {
		p.sayf("\nTrading profile: skipped. Two independent evidence RPC endpoints")
		p.sayf("must be set in the environment first (MITHRIL_AGENT_PRIMARY_RPC_URL")
		p.sayf("and MITHRIL_AGENT_SECONDARY_RPC_URL). They are read from the")
		p.sayf("environment, never typed here or written to disk.")
		return "", nil
	}
	if !p.interactive {
		p.sayf("\nTrading profile: skipped. The floor price has to be agreed to by a")
		p.sayf("person, and this is not a terminal. Run setup again interactively.")
		return "", nil
	}

	primary, err := p.ask("Name for your primary evidence provider", "primary-provider")
	if err != nil {
		return "", err
	}
	secondary, err := p.ask("Name for your second, independent evidence provider", "secondary-provider")
	if err != nil {
		return "", err
	}

	p.sayf("\nReading a live Devnet quote (read-only — nothing is signed)...")
	options := swapSetupOptions{
		directory: profileDir, direction: choices.direction,
		walletKeypair:  choices.accountKeypair,
		mithrilCommand: choices.mithrilCommand, mithrilConfig: choices.mithrilConfig,
		nodeCommand: choices.nodeCommand, quoteScript: choices.quoteScript,
		inputLamports: defaultSwapInputLamports, slippageBPS: 100,
		reserveLamports: defaultSwapReserve, maxFeeLamports: defaultSwapMaxFee,
		scheduleWindow: time.Hour,
		primaryTrust:   primary, secondaryTrust: secondary,
		confirmQuote: func(quote quoteConfirmation) error {
			return confirmQuoteWithOperator(p, quote)
		},
	}
	if choices.direction == "buy" {
		options.inputTokenAmount = 1_000_000
		options.dailyInputTokenCap = options.inputTokenAmount
		options.dailyNativeFeeCap = options.maxFeeLamports
		options.inputLamports = 0
	}
	if !validTrustDomain(primary) || !validTrustDomain(secondary) || primary == secondary {
		return "", errors.New("the two provider names must be distinct, lowercase, and short")
	}

	result, err := createSwapSetup(ctx, options)
	if err != nil {
		// A declined quote is a legitimate outcome, not a failure.
		if errors.Is(err, errQuoteDeclined) {
			p.sayf("\nNo profile written. Nothing was changed.")
			return "", nil
		}
		return "", err
	}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		return "", err
	}
	// Recording where this went is a convenience, not a requirement: if it
	// fails the operator can still pass --config, so say so and carry on.
	if err := recordCurrentConfig(result.ConfigPath); err != nil {
		p.sayf("\nCould not note this location, so later commands will need")
		p.sayf("--config %s", result.ConfigPath)
	}
	return result.ConfigPath, nil
}

var errQuoteDeclined = errors.New("the quote was not confirmed")

// confirmQuoteWithOperator shows the floor in the operator's units and asks.
// It defaults to no: holding Enter through the wizard must never write a
// policy the operator did not read.
func confirmQuoteWithOperator(p *prompter, quote quoteConfirmation) error {
	p.sayf("\nThe trade this will configure")
	p.sayf("-----------------------------")
	p.sayf("  Spend at most:  %s", quote.InputText)
	p.sayf("  Receive at least: %s", quote.OutputText)
	p.sayf("  Slippage limit: %d bps", quote.SlippageBPS)
	p.sayf("")
	p.sayf("That floor is written into the policy. If the real fill would come in")
	p.sayf("below it, the trade is refused rather than filled at a worse price.")
	p.sayf("This is Devnet: the tokens have no value.")

	agreed, err := p.confirm("Write this as the configured trade?", false)
	if err != nil {
		return err
	}
	if !agreed {
		return errQuoteDeclined
	}
	return nil
}

// finishGuidedSetup reports what is still required rather than pretending the
// wizard can complete a host deployment it cannot verify from here.
func finishGuidedSetup(p *prompter, choices setupChoices, configPath string) error {
	missing := missingSetupInputs(choices)

	p.sayf("\n\nWhat you chose")
	p.sayf("--------------")
	for _, answer := range p.answers {
		marker := " "
		if answer.Default {
			marker = "·"
		}
		p.sayf("  %s %s", marker, collapseQuestion(answer.Question)+": "+answer.Value)
	}

	if len(missing) > 0 {
		p.sayf("\nStill needed before a demonstration can run")
		p.sayf("------------------------------------------")
		for _, item := range missing {
			p.sayf("  - %s", item)
		}
		p.sayf("\nThese are host prerequisites, not settings. The agent cannot")
		p.sayf("create them, and it will refuse to act until they are real.")
	}

	p.sayf("\nNext")
	p.sayf("----")
	p.sayf("  1. Fund the agent account at https://faucet.solana.com")
	switch {
	case configPath != "":
		p.sayf("  2. mithril-agent doctor          (it knows where this went)")
	case len(missing) > 0:
		p.sayf("  2. Clear the items above, then run: mithril-agent setup")
	default:
		// Nothing is missing, yet no profile was written — the reason was
		// printed above, and telling somebody to "clear the items above" when
		// there are none is the kind of instruction that makes people give up.
		p.sayf("  2. Deal with the reason the trading profile was skipped,")
		p.sayf("     shown above, then run: mithril-agent setup")
	}
	p.sayf("  3. When doctor reports ready, a demonstration is a separate,")
	p.sayf("     explicit command. This setup did not authorise one.")
	return nil
}

// missingSetupInputs names the host prerequisites that are absent. It checks
// rather than assumes, because a path that does not exist is the single most
// common reason a guided setup produces a configuration that cannot run.
func missingSetupInputs(choices setupChoices) []string {
	var missing []string
	for _, item := range []struct{ label, path string }{
		{"the Mithril node executable", choices.mithrilCommand},
		{"the Mithril node's config.toml", choices.mithrilConfig},
		{"the pinned Node.js runtime", choices.nodeCommand},
		{"the Orca quote adapter", choices.quoteScript},
	} {
		if item.path == "" {
			missing = append(missing, item.label+" (no path given)")
			continue
		}
		if _, err := os.Lstat(item.path); err != nil {
			missing = append(missing, item.label+" is not at "+item.path)
		}
	}
	// A present but wrong-version Node is worse than an absent one: it looks
	// settled here and refuses at run time, after the operator believes setup
	// succeeded.
	if reason := unsupportedNodeReason(choices.nodeCommand); reason != "" {
		missing = append(missing, reason)
	}
	return missing
}

// unsupportedNodeReason reports why a Node runtime cannot be used, or "" when
// it is fine. The adapter refuses anything outside 24.18+ in the 24.x line
// (adapters/orca/quote.mjs), so checking here turns a confusing runtime refusal
// into a line in the same list as everything else that is missing.
func unsupportedNodeReason(nodeCommand string) string {
	if nodeCommand == "" {
		return ""
	}
	if _, err := os.Lstat(nodeCommand); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, nodeCommand, "--version").Output()
	if err != nil {
		return "the Node.js runtime at " + nodeCommand + " could not be run"
	}
	version := strings.TrimSpace(strings.TrimPrefix(string(output), "v"))
	major, minor, ok := strings.Cut(version, ".")
	if !ok {
		return "the Node.js runtime at " + nodeCommand + " reported no usable version"
	}
	minor, _, _ = strings.Cut(minor, ".")
	if major != "24" || len(minor) == 0 {
		return "Node " + version + " is not supported; the quote adapter needs 24.18+ in the 24.x line"
	}
	if len(minor) < 2 && minor < "9" || len(minor) == 2 && minor < "18" {
		return "Node " + version + " is not supported; the quote adapter needs 24.18+"
	}
	return ""
}

// detectExecutable offers a real path when one is on PATH, so the common case
// is a single Enter rather than a filesystem hunt.
func detectExecutable(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	return ""
}

// collapseQuestion turns a multi-line prompt into one summary line.
func collapseQuestion(question string) string {
	if index := strings.IndexByte(question, '\n'); index >= 0 {
		question = question[:index]
	}
	return strings.TrimSuffix(strings.TrimSpace(question), "?")
}
