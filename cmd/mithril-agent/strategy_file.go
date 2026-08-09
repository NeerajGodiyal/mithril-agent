package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
)

// One file the operator writes once, holding everything a strategy needs: what
// to trade, at which prices, where the profit goes, and which parts are on.
//
// It exists because the alternative was a fifteen-flag command line that had to
// be retyped correctly to change one number, with the real settings scattered
// across three generated files nobody should hand-edit. This is the one thing a
// person reads, edits and keeps.
//
// It is NOT a new source of authority. Every value here becomes an argument to
// the same setup path a person could run by hand, every generated profile is
// still fingerprinted and validated, and nothing in this file can arm anything:
// arming stays a separate, explicit, bounded command.
type strategyFile struct {
	// Comment carries the template's own explanation. Decoding is strict, so
	// the field has to exist or the file this tool writes could not be read
	// back by the tool that wrote it.
	Comment []string `json:"_comment,omitempty"`

	// SizeSOL is the SOL each trade spends. Required.
	SizeSOL string `json:"size_sol"`
	// SellAtUSD and BuyAtUSD are optional. Set BOTH to trade only at prices you
	// choose, or NEITHER to trade at whatever the pool gives. Setting one is
	// refused: half a pair is always a mistake.
	SellAtUSD string `json:"sell_at_usd,omitempty"`
	BuyAtUSD  string `json:"buy_at_usd,omitempty"`
	// ScheduleWindow bounds how often a leg may act: one action per window.
	ScheduleWindow string `json:"schedule_window,omitempty"`
	// TradesPerDay is how many trades a day the signer's caps must fund, per
	// leg. It is a spending bound, not a target: the agent trades when the rules
	// say so and never more often than this allows.
	TradesPerDay uint64 `json:"trades_per_day,omitempty"`

	Sweep    strategyFileSweep    `json:"sweep"`
	Telegram strategyFileTelegram `json:"telegram,omitempty"`
	// Alerts are notify-only thresholds. They belong here because "one file
	// holds every setting" was not true while the only way to set them was a
	// separate command after setup — an operator who wrote the whole strategy
	// down still had to remember one more step, and nothing said so.
	//
	// They remain live-editable afterwards with `strategy alerts set`: the file
	// is what the strategy STARTS as, not a lock on what it may become.
	Alerts strategyFileAlerts `json:"alerts,omitempty"`
}

// strategyFileAlerts are the notify-only thresholds, written the way an
// operator says them: dollars for prices, SOL for balances. They can never
// trade or move funds.
type strategyFileAlerts struct {
	PriceAboveUSD   string `json:"price_above_usd,omitempty"`
	PriceBelowUSD   string `json:"price_below_usd,omitempty"`
	BalanceAboveSOL string `json:"balance_above_sol,omitempty"`
	BalanceBelowSOL string `json:"balance_below_sol,omitempty"`
}

// empty reports whether the file asks for no alerts at all.
func (a strategyFileAlerts) empty() bool {
	return a == strategyFileAlerts{}
}

// resolve converts the operator's units into the stored ones, through the SAME
// parsers `strategy alerts set` uses. A second conversion here would be a
// second place for a rounding or unit bug to live.
func (a strategyFileAlerts) resolve() (alertsConfig, error) {
	var alerts alertsConfig
	var err error
	if a.PriceAboveUSD != "" {
		if alerts.PriceAboveMicroUSD, err = parseUSDThreshold(a.PriceAboveUSD, "price_above_usd"); err != nil {
			return alertsConfig{}, err
		}
	}
	if a.PriceBelowUSD != "" {
		if alerts.PriceBelowMicroUSD, err = parseUSDThreshold(a.PriceBelowUSD, "price_below_usd"); err != nil {
			return alertsConfig{}, err
		}
	}
	if a.BalanceAboveSOL != "" {
		if alerts.BalanceAboveLamports, err = parseDecimalUnits9(a.BalanceAboveSOL, "balance_above_sol"); err != nil {
			return alertsConfig{}, err
		}
	}
	if a.BalanceBelowSOL != "" {
		if alerts.BalanceBelowLamports, err = parseDecimalUnits9(a.BalanceBelowSOL, "balance_below_sol"); err != nil {
			return alertsConfig{}, err
		}
	}
	return alerts, nil
}

// strategyFileSweep is where the profit goes. The all-in-one strategy requires
// it; a standalone swap setup remains available when no sweep is wanted.
type strategyFileSweep struct {
	Enabled bool `json:"enabled"`
	// To is YOUR wallet. You prove you control it once, by signature.
	To string `json:"to,omitempty"`
	// The proof from that signature. Obtain it with the setup command or
	// deploy/sign-destination-proof.html, then paste all three here.
	ProofNonce     string `json:"proof_nonce,omitempty"`
	ProofIssued    string `json:"proof_issued,omitempty"`
	ProofSignature string `json:"proof_signature,omitempty"`
	// KeepSOL is how much stays in the agent wallet; everything above it is
	// swept to To. This is the "keep $20, send me the rest" number, and it was
	// the one thing a whole strategy could not say for itself: the floor was
	// derived from what the trading legs need and there was no way to ask for
	// more. Left empty it stays derived, which is the safe default.
	//
	// It can only ever RAISE the floor. Below what the legs need, a sweep would
	// drain the wallet under the trader and every later trade would be refused
	// for insufficient balance — so setup refuses rather than quietly clamping.
	KeepSOL string `json:"keep_sol,omitempty"`
	// ActivationDelay is the window in which you can still stop a sweep you did
	// not intend. Leave empty in production; "0s" is Devnet testing only.
	ActivationDelay string `json:"activation_delay,omitempty"`
}

// strategyFileTelegram records the intent only. The token never appears here —
// a secret in a config file ends up in backups, screenshots and git. The
// service reads it from its own protected environment.
type strategyFileTelegram struct {
	Enabled bool `json:"enabled"`
}

// validate refuses a file that cannot produce a working strategy, naming the
// field rather than failing later inside a generated profile.
func (f strategyFile) validate() error {
	if f.SizeSOL == "" {
		return errors.New(`"size_sol" is required: how much SOL each trade spends`)
	}
	if (f.SellAtUSD == "") != (f.BuyAtUSD == "") {
		return errors.New(
			`set BOTH "sell_at_usd" and "buy_at_usd", or NEITHER to trade at market`)
	}
	if f.ScheduleWindow != "" {
		if _, err := time.ParseDuration(f.ScheduleWindow); err != nil {
			return errors.New(`"schedule_window" must be a duration such as "1h" or "5m"`)
		}
	}
	if f.TradesPerDay > maxTradesPerDay {
		return fmt.Errorf(`"trades_per_day" may not exceed %d`, maxTradesPerDay)
	}
	if _, err := f.Alerts.resolve(); err != nil {
		return err
	}
	if (f.Alerts.PriceAboveUSD != "" || f.Alerts.PriceBelowUSD != "") &&
		(f.SellAtUSD == "" || f.BuyAtUSD == "") {
		return errors.New("price alerts require both sell_at_usd and buy_at_usd")
	}
	if f.Sweep.KeepSOL != "" {
		if _, err := parseDecimalUnitsLamports(f.Sweep.KeepSOL, "keep_sol"); err != nil {
			return err
		}
	}
	if !f.Sweep.Enabled {
		return errors.New(`"sweep.enabled" must be true for an all-in-one strategy`)
	}
	if f.Sweep.To == "" {
		return errors.New(`"sweep.to" is required when the sweep is enabled`)
	}
	// All three or none: a partial proof cannot be verified, and discovering
	// that after the setup has written a leg wastes a signing session.
	proof := 0
	for _, part := range []string{f.Sweep.ProofNonce, f.Sweep.ProofIssued, f.Sweep.ProofSignature} {
		if part != "" {
			proof++
		}
	}
	if proof != 0 && proof != 3 {
		return errors.New(
			`"sweep" needs all of proof_nonce, proof_issued and proof_signature, or none`)
	}
	if f.Sweep.ActivationDelay != "" {
		if _, err := time.ParseDuration(f.Sweep.ActivationDelay); err != nil {
			return errors.New(`"sweep.activation_delay" must be a duration such as "24h"`)
		}
	}
	return nil
}

// strategyFileTemplate is what `strategy init` writes. It is commented, because
// the point of one file is that a person can read it and know what to change.
// maxTradesPerDay matches the control state machine's own ceiling on actions
// per grant, so the two bounds cannot be set to contradict each other.
const maxTradesPerDay = uint64(100)

func firstNonZero(value, fallback uint64) uint64 {
	if value != 0 {
		return value
	}
	return fallback
}

// parseTradesPerDay refuses zero rather than reading it as "use the default":
// somebody who types 0 means "do not trade", and silently funding six trades
// instead is the opposite of what they asked for.
func parseTradesPerDay(text string) (uint64, error) {
	value, err := strconv.ParseUint(strings.TrimSpace(text), 10, 64)
	if err != nil || value == 0 || value > maxTradesPerDay {
		return 0, fmt.Errorf("trades per day must be a whole number from 1 to %d", maxTradesPerDay)
	}
	return value, nil
}

const strategyFileTemplate = `{
  "_comment": [
    "One strategy, one file. Edit it, then run:",
    "  mithril-agent setup strategy --from THIS_FILE  (plus the paths for your host)",
    "",
    "Nothing here arms anything. After setup you still run, explicitly:",
    "  mithril-agent service install --output $HOME/.mithril-agent/mithril-agent-run.service",
    "  mithril-agent start  (prints the safe command for the profiles setup wrote)",
    "",
    "Prices: set BOTH sell_at_usd and buy_at_usd to trade only at prices you",
    "choose, or delete BOTH lines to trade at whatever the pool gives. The price",
    "you set is a FLOOR ON THE FILL, not just a signal: the agent refuses to sell",
    "below it however high the oracle reads.",
    "",
    "alerts are NOTIFY-ONLY: they message you and can never trade or move",
    "funds. Leave a line empty for no alert. Editable later, live, with",
    "  mithril-agent strategy alerts set --price-above USD",
    "",
    "trades_per_day is a SPENDING BOUND: the most each leg may trade in a UTC",
    "day. It is not a target. --max-trades may not exceed it.",
    "",
    "keep_sol is what STAYS in the agent wallet; everything above it is sent to",
    "sweep.to. Leave it empty and the agent keeps exactly what the trades need."
  ],

  "size_sol": "0.05",
  "sell_at_usd": "",
  "buy_at_usd": "",
  "schedule_window": "1h",
  "trades_per_day": 6,

  "sweep": {
    "enabled": true,
    "to": "YOUR_WALLET_ADDRESS",
    "keep_sol": "",
    "proof_nonce": "",
    "proof_issued": "",
    "proof_signature": ""
  },

  "telegram": {
    "enabled": true
  },

  "alerts": {
    "price_above_usd": "",
    "price_below_usd": "",
    "balance_above_sol": "",
    "balance_below_sol": ""
  }
}
`

// runStrategyInit writes the template. It refuses to overwrite: a strategy file
// holds a destination proof somebody signed, and silently replacing it would
// throw that away.
func runStrategyInit(args []string, output io.Writer) error {
	path := ""
	if len(args) == 1 {
		path = args[0]
	}
	if len(args) > 1 {
		return errors.New("strategy init takes at most one path")
	}
	if path == "" {
		_, err := fmt.Fprint(output, strategyFileTemplate)
		return err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("the strategy file path must be absolute and clean")
	}
	if usableConfigPath(path) {
		return fmt.Errorf("%s already exists; edit it rather than replacing a signed proof", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.New("could not create the directory for the strategy file")
	}
	if err := securefile.ReplacePrivate(
		path, []byte(strategyFileTemplate), maxInputBytes); err != nil {
		// securefile refuses on principle and says so in four words. That is the
		// right answer for a library and a dead end for the FIRST command anybody
		// runs: "private file directory is not trusted" names no directory, no
		// reason, and nothing to do. The usual cause is a directory created with
		// a permissive umask — group-writable is enough — which nobody guesses.
		return fmt.Errorf("%w: %s", err, describeUntrustedDirectory(filepath.Dir(path)))
	}
	_, err := fmt.Fprintf(output,
		"Wrote %s\n\nEdit it, then:\n  mithril-agent setup strategy --from %s ...\n", path, path)
	return err
}

// runStrategyEdit opens the strategy file in the operator's own editor and
// validates it when they come back.
//
// Deliberately NOT a form: Mithril's `config edit` is a full-screen TUI, but
// this tool has no bubbletea and should not grow one for a file with nine
// fields. Everyone already has an editor they know, and the value this adds is
// not the typing — it is refusing to leave an unusable file behind and saying
// exactly which line is wrong.
func runStrategyEdit(args []string, output io.Writer) error {
	// The guided walkthrough is the DEFAULT. Raw JSON is the opt-in, because
	// the person who most needs this file is the least likely to edit JSON
	// safely — and a missing comma is a broken strategy.
	raw := false
	var rest []string
	for _, item := range args {
		if item == "--raw" {
			raw = true
			continue
		}
		rest = append(rest, item)
	}
	if len(rest) != 1 {
		return errors.New("strategy edit requires the path of a strategy file")
	}
	path := rest[0]
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("the strategy file path must be absolute and clean")
	}
	if !usableConfigPath(path) {
		return fmt.Errorf("%s does not exist; create it with: mithril-agent strategy init %s",
			path, path)
	}
	if !raw {
		return guidedStrategyEdit(path, output)
	}
	editor, err := operatorEditor()
	if err != nil {
		return err
	}
	// A syntactically broken file is recoverable — the operator edits again. An
	// unreadable one before editing means something else is wrong, and opening
	// an editor on it would hide that.
	if _, err := readStrategyFile(path); err != nil {
		if _, writeErr := fmt.Fprintf(output,
			"The file is not usable yet; opening it so you can fix it:\n  %v\n\n", err); writeErr != nil {
			return writeErr
		}
	}
	if err := runEditor(editor, path); err != nil {
		return err
	}
	file, err := readStrategyFile(path)
	if err != nil {
		// Named, not swallowed: the operator has just spent effort and needs to
		// know the file will be refused before they walk away expecting it to run.
		return fmt.Errorf("%w\n\nThe file was saved but will not be accepted. Run the "+
			"same command again to fix it", err)
	}
	summary := "trades at whatever the pool gives"
	if file.SellAtUSD != "" {
		summary = "sells at or above $" + file.SellAtUSD + ", buys at or below $" + file.BuyAtUSD
	}
	sweep := "sweep ON -> " + file.Sweep.To
	_, err = fmt.Fprintf(output,
		"Saved and valid.\n  %s SOL per trade, %s\n  %s\n\nNothing is armed. Apply it with:\n"+
			"  mithril-agent setup strategy --from %s ...\n",
		file.SizeSOL, summary, sweep, path)
	return err
}

// operatorEditor resolves the editor to run. It refuses a relative name so the
// program that opens a file holding a signed destination proof cannot be picked
// up from the working directory.
func operatorEditor() (string, error) {
	name := os.Getenv("VISUAL")
	if name == "" {
		name = os.Getenv("EDITOR")
	}
	if name == "" {
		return "", errors.New(
			"set EDITOR (or VISUAL) to the editor you want, for example: EDITOR=nano")
	}
	resolved := name
	if !filepath.IsAbs(resolved) {
		found, err := exec.LookPath(name)
		if err != nil {
			return "", fmt.Errorf("cannot find the editor %q on PATH", name)
		}
		resolved = found
	}
	if err := secureexec.ValidateExecutable(resolved); err != nil {
		return "", fmt.Errorf("editor %s: %w", resolved, err)
	}
	return resolved, nil
}

// runEditor is separated so the round trip can be tested without a terminal.
var runEditor = func(editor, path string) error {
	command := exec.Command(editor, path)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("editor exited without saving: %w", err)
	}
	return nil
}

// guideStrategyFile walks the operator through the file one question at a time,
// pre-filled with what is already there — the same shape as the setup wizard,
// using the prompter this tool already has.
//
// This is the default because the people who need a strategy file are not
// necessarily people who can safely edit JSON in vi: one missing comma and the
// file is broken, and nothing on screen explains what "proof_signature" is.
// Mithril solves the same problem with a full-screen TUI; this reaches the same
// place with the prompter that is already here and no new dependency.
func guideStrategyFile(p *prompter, current strategyFile) (strategyFile, error) {
	next := current
	p.sayf("\nEditing your strategy. Press Enter to keep what is shown.\n")

	size, err := p.ask("How much SOL should each trade spend?", firstNonEmpty(current.SizeSOL, "0.05"))
	if err != nil {
		return strategyFile{}, err
	}
	next.SizeSOL = size

	// Every optional part follows the same shape: ON or OFF first, details only
	// if on. A blank answer used to mean "trade at whatever the pool gives",
	// which is a different strategy entirely — and somebody pressing Enter to
	// keep the current value had no way to know they had just changed it.
	priceRules, err := p.confirm(
		"Only trade at prices you choose? (No = trade at whatever the pool gives)",
		current.SellAtUSD != "")
	if err != nil {
		return strategyFile{}, err
	}
	if priceRules {
		sell, err := p.ask(
			"  Sell only when SOL is at or ABOVE this price in dollars?", current.SellAtUSD)
		if err != nil {
			return strategyFile{}, err
		}
		next.SellAtUSD = strings.TrimSpace(sell)
		buy, err := p.ask(
			"  Buy back only when SOL is at or BELOW this price?", current.BuyAtUSD)
		if err != nil {
			return strategyFile{}, err
		}
		next.BuyAtUSD = strings.TrimSpace(buy)
	} else {
		// Half a pair is always a mistake, so turning the rules off clears both
		// rather than leaving a file the validator will reject.
		next.SellAtUSD, next.BuyAtUSD = "", ""
		next.Alerts.PriceAboveUSD, next.Alerts.PriceBelowUSD = "", ""
		p.sayf("  Trading at market. Arming this needs --allow-any-price.")
	}

	window, err := p.ask(
		"How often may each leg trade at most? (e.g. 1h, 15m)",
		firstNonEmpty(current.ScheduleWindow, "1h"))
	if err != nil {
		return strategyFile{}, err
	}
	next.ScheduleWindow = window

	// Asked in plain terms because it is a spending bound, and the number a
	// person answers here is the number that decides whether the strategy keeps
	// working after its first trade of the day.
	perDay, err := p.ask("At most how many trades a day may each leg make?",
		strconv.FormatUint(firstNonZero(current.TradesPerDay, defaultTradesPerDay), 10))
	if err != nil {
		return strategyFile{}, err
	}
	if next.TradesPerDay, err = parseTradesPerDay(perDay); err != nil {
		return strategyFile{}, err
	}

	// A complete strategy includes the safe return path. People who only want
	// a trading leg can use the standalone swap setup; offering "no" here wrote
	// a file this command later refused, which made the guided path self-contradictory.
	next.Sweep.Enabled = true
	to, err := p.ask("Which wallet should profit be sent to?", current.Sweep.To)
	if err != nil {
		return strategyFile{}, err
	}
	next.Sweep.To = strings.TrimSpace(to)
	if next.Sweep.To != current.Sweep.To {
		// The proof is bound to one destination. Keeping it after the address
		// changes would carry a signature for a wallet nobody approved.
		next.Sweep.ProofNonce, next.Sweep.ProofIssued, next.Sweep.ProofSignature = "", "", ""
		p.sayf("\n  That is a new destination, so the old proof no longer applies.")
		p.sayf("  You will be asked to prove you control it during setup.")
	}
	// The "keep this much, send me the rest" number. Blank keeps exactly
	// what the trades need, which is the safe answer and the default.
	keep, err := p.ask(
		"How much SOL should stay in the agent wallet? (blank = just what the trades need)",
		current.Sweep.KeepSOL)
	if err != nil {
		return strategyFile{}, err
	}
	next.Sweep.KeepSOL = strings.TrimSpace(keep)

	telegramOn, err := p.confirm(
		"Send trade alerts to Telegram? (the token stays in the service environment, never here)",
		current.Telegram.Enabled)
	if err != nil {
		return strategyFile{}, err
	}
	next.Telegram.Enabled = telegramOn

	if priceRules {
		if next.Alerts.PriceAboveUSD, err = p.ask(
			"Notify when SOL rises above this price? (blank = no alert)",
			current.Alerts.PriceAboveUSD); err != nil {
			return strategyFile{}, err
		}
		if next.Alerts.PriceBelowUSD, err = p.ask(
			"Notify when SOL falls below this price? (blank = no alert)",
			current.Alerts.PriceBelowUSD); err != nil {
			return strategyFile{}, err
		}
	}
	if next.Alerts.BalanceAboveSOL, err = p.ask(
		"Notify when the agent balance rises above this much SOL? (blank = no alert)",
		current.Alerts.BalanceAboveSOL); err != nil {
		return strategyFile{}, err
	}
	if next.Alerts.BalanceBelowSOL, err = p.ask(
		"Notify when the agent balance falls below this much SOL? (blank = no alert)",
		current.Alerts.BalanceBelowSOL); err != nil {
		return strategyFile{}, err
	}
	return next, nil
}

// writeStrategyFile saves the guided result, keeping the template's own
// explanation so the file stays readable to whoever opens it next.
func writeStrategyFile(path string, file strategyFile) error {
	if len(file.Comment) == 0 {
		var template strategyFile
		if err := json.Unmarshal([]byte(strategyFileTemplate), &template); err == nil {
			file.Comment = template.Comment
		}
	}
	encoded, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return errors.New("encode the strategy file")
	}
	return securefile.ReplacePrivate(path, append(encoded, '\n'), maxInputBytes)
}

// guidedStrategyEdit asks one question at a time and writes the result. It
// never asks the operator to type JSON, so a missing comma is not a way to
// break a strategy.
func guidedStrategyEdit(path string, output io.Writer) error {
	current, err := readStrategyFile(path)
	if err != nil {
		// A file too broken to read still has to be fixable, and the guided
		// path is exactly how somebody who cannot repair JSON does that.
		if _, writeErr := fmt.Fprintf(output,
			"The current file could not be read (%v).\nStarting from the defaults instead.\n",
			err); writeErr != nil {
			return writeErr
		}
		current = strategyFile{}
	}
	prompt := newPrompter(os.Stdin, output, stdinIsTerminal())
	if !prompt.interactive {
		return errors.New(
			"strategy edit asks questions, so it needs a terminal; " +
				"use --raw to edit the file directly instead")
	}
	next, err := guideStrategyFile(prompt, current)
	if err != nil {
		return err
	}
	if err := next.validate(); err != nil {
		return fmt.Errorf("%w\n\nNothing was saved. Run the command again", err)
	}
	if err := writeStrategyFile(path, next); err != nil {
		return err
	}
	summary := "trades at whatever the pool gives"
	if next.SellAtUSD != "" {
		summary = "sells at or above $" + next.SellAtUSD + ", buys at or below $" + next.BuyAtUSD
	}
	sweep := "profit goes to " + next.Sweep.To
	_, err = fmt.Fprintf(output,
		"\nSaved %s\n  %s SOL per trade, %s\n  %s\n\nNothing is armed.\n",
		path, next.SizeSOL, summary, sweep)
	return err
}

// strategyFileTargets are the setup values a strategy file may set. Naming them
// in one struct is what makes "one file configures everything" checkable: a
// field the file carries but nothing here consumes is a setting the operator
// wrote down and the agent silently ignored.
type strategyFileTargets struct {
	alerts          *alertsConfig
	sizeSOL         *string
	sellAtUSD       *string
	buyAtUSD        *string
	destination     *string
	proofNonce      *string
	proofIssued     *string
	proofSignature  *string
	keepSOL         *string
	scheduleWindow  *time.Duration
	activationDelay *time.Duration
	tradesPerDay    *uint64
}

// applyStrategyFile copies a strategy file onto the setup options.
//
// Explicit flags still win, so one value can be overridden without editing the
// file — but everything unset comes from the one place. `given` names the flags
// the operator actually typed.
func applyStrategyFile(file strategyFile, given map[string]bool, targets strategyFileTargets) error {
	apply := func(name string, target *string, value string) {
		if !given[name] && value != "" {
			*target = value
		}
	}
	apply("size-sol", targets.sizeSOL, file.SizeSOL)
	apply("sell-at-usd", targets.sellAtUSD, file.SellAtUSD)
	apply("buy-at-usd", targets.buyAtUSD, file.BuyAtUSD)
	apply("to", targets.destination, file.Sweep.To)
	apply("proof-nonce", targets.proofNonce, file.Sweep.ProofNonce)
	apply("proof-issued", targets.proofIssued, file.Sweep.ProofIssued)
	apply("proof-signature", targets.proofSignature, file.Sweep.ProofSignature)
	apply("keep-sol", targets.keepSOL, file.Sweep.KeepSOL)

	if !given["schedule-window"] && file.ScheduleWindow != "" {
		parsed, err := time.ParseDuration(file.ScheduleWindow)
		if err != nil {
			return err
		}
		*targets.scheduleWindow = parsed
	}
	if !given["trades-per-day"] && file.TradesPerDay != 0 {
		*targets.tradesPerDay = file.TradesPerDay
	}
	if !given["activation-delay"] && file.Sweep.ActivationDelay != "" {
		parsed, err := time.ParseDuration(file.Sweep.ActivationDelay)
		if err != nil {
			return err
		}
		*targets.activationDelay = parsed
		// 0s is meaningful here — "active immediately" for Devnet testing — so
		// the downstream cannot infer "was it given?" from the value alone.
		given["activation-delay"] = true
	}
	// Parsed here, applied to each leg after they are written, so an unusable
	// threshold fails BEFORE anything is created rather than leaving a
	// half-built strategy behind.
	if !file.Alerts.empty() && targets.alerts != nil {
		resolved, err := file.Alerts.resolve()
		if err != nil {
			return err
		}
		// A price alert is judged against the profile's price rule, which only
		// exists when the strategy trades at chosen prices. Caught here it is a
		// sentence; caught after the legs are written it is a half-built
		// strategy the operator has to clean up by hand.
		if resolved.PriceAboveMicroUSD != 0 || resolved.PriceBelowMicroUSD != 0 {
			if *targets.sellAtUSD == "" || *targets.buyAtUSD == "" {
				return errors.New(
					"price alerts need prices to watch: set sell_at_usd and buy_at_usd, " +
						"or use the balance alerts instead")
			}
		}
		*targets.alerts = resolved
	}
	if !file.Sweep.Enabled {
		return errors.New(
			"this strategy file has the sweep disabled, so there is nowhere for profit " +
				"to go; set \"sweep\": {\"enabled\": true, \"to\": \"YOUR_WALLET\"}")
	}
	return nil
}

// writeLegAlerts stores notify-only thresholds on a leg that setup has just
// written. It validates against that leg's own profile first, because a
// balance threshold below what the leg needs to trade would alert forever.
//
// Empty alerts are a no-op rather than a clear: setup naming no alerts must not
// erase ones an operator set live on an existing leg.
func writeLegAlerts(configPath string, alerts alertsConfig) error {
	if alerts == (alertsConfig{}) || configPath == "" {
		return nil
	}
	cfg, err := readConfig(configPath)
	if err != nil {
		return fmt.Errorf("read the leg to store its alerts: %w", err)
	}
	cfg.Alerts = alerts
	if err := cfg.Alerts.validate(cfg.Swap); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return errors.New("encode the leg with its alerts")
	}
	return securefile.ReplacePrivate(configPath, append(encoded, '\n'), maxSetupFileBytes)
}

// describeUntrustedDirectory says which directory failed the trust check, why,
// and the one command that repairs it. securefile deliberately reports only
// that it refused; repeating that verdict to an operator who has not read the
// source is a dead end on the first command they run.
//
// It re-checks the same conditions securefile does rather than being told the
// cause, so it cannot claim a reason the writer did not actually have. When
// nothing observable is wrong — a racing chmod, an ancestor rather than the
// parent — it says so instead of inventing one.
func describeUntrustedDirectory(directory string) string {
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Sprintf("%s could not be inspected: %v", directory, err)
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Sprintf("%s is a symlink, and a private file is never written through one", directory)
	case !info.IsDir():
		return directory + " is not a directory"
	case info.Mode().Perm()&0o022 != 0:
		return fmt.Sprintf(
			"%s is mode %04o, so the group or everyone can write in it and replace this "+
				"file after it is written; fix it with: chmod 700 %s",
			directory, info.Mode().Perm(), directory)
	case !fileowner.Trusted(info):
		return fmt.Sprintf(
			"%s is owned by uid %s, which is neither you nor root; write the strategy "+
				"file somewhere you own, or fix the ownership", directory, directoryOwner(info))
	}
	return fmt.Sprintf(
		"%s looked acceptable on re-inspection, so the cause is above it: every parent "+
			"directory must also be owned by you or root and not group- or world-writable",
		directory)
}

func directoryOwner(info os.FileInfo) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "unknown"
	}
	return fmt.Sprint(stat.Uid)
}
