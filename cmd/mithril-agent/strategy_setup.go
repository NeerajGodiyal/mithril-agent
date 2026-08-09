package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
)

// setup strategy writes a whole round trip — a sell leg, a sweep, and (once a
// devUSDC account exists) a buy leg — from one set of answers.
//
// It arms nothing. Every profile it writes lands STOPPED, and the destination
// proof-of-control ceremony runs unchanged: the operator still retypes part of
// the address and still signs a challenge with the destination key. What this
// command removes is the bookkeeping between three separate setups, not a
// single check.
//
// The one thing it derives rather than asks: the buy leg spends exactly what
// one sell produces. That is what makes the profit land in SOL, which is the
// only asset the sweep can send.
const strategySetupUsage = `Usage: mithril-agent setup strategy

Run it with no strategy options. It asks what you want, saves your answers,
shows the exact trade it will configure, and takes you through proving you own
the wallet the profit goes to. It reuses host paths recorded by an existing
setup. On a fresh host, pass --wallet-keypair and --mithril-command; an installed
runtime finds its sibling Node.js executable and quote adapter itself.

  mithril-agent setup strategy

Then, when you are ready:

  mithril-agent service install --output "$HOME/.mithril-agent/mithril-agent-run.service"
    Run once per host; it prints the review and install commands.
  mithril-agent start

Everything below is optional. Use --from to re-run a saved strategy file
unattended, or any single flag to override one answer.

Nothing is armed. Nothing is signed but the destination proof you provide.

Options (each overrides the file):

  --from PATH            a strategy file holding every setting, written by
                         "mithril-agent strategy init". Explicit flags win.
  --dir PATH             where the private setup lives
  --wallet-keypair PATH  the agent account keypair
  --size-sol AMOUNT      SOL per trade (default 0.05)
  --sell-at-usd PRICE    sell only at or above this price
  --buy-at-usd PRICE     buy back only at or below this price
                         Omit BOTH to trade at market — the only shape that can
                         complete a cycle on a pool whose price disagrees with
                         the oracle. Arming then needs --allow-any-price.
  --to ADDRESS           YOUR wallet; the profit is swept here
  --node-command PATH    the pinned Node.js runtime
  --quote-script PATH    the Orca quote adapter
  --mithril-command PATH the Mithril node executable
  --primary-trust-domain NAME    first evidence provider
  --secondary-trust-domain NAME  second, independent evidence provider
  --confirm-min-output-amount N   REQUIRED: the minimum output you agreed to.
                         Read it first with:
                           mithril-agent swap discover --direction sell ...
  --quote-socket PATH    the runtime quote socket, when one is deployed
  --schedule-window D    one action per window, per leg (default 1h)
  --trades-per-day N     how many trades a day each leg's spending caps must
                         fund (default 6). This is a BOUND, not a target, and
                         --max-trades may not exceed it. It is written into the
                         signed profile, so changing it means running this again
  --keep-sol AMOUNT      SOL that STAYS in the agent wallet; everything above it
                         is swept to --to. Unset keeps exactly what the trades
                         need. It can only raise that floor, never lower it
  --floor-tolerance-bps N  how far below the agreed floor a fill may still be
                         accepted (default 100 = 1%). Zero means the strategy
                         trades once and then refuses forever.
  --activation-delay D   override the sweep's delay before it may first act.
                         Leave unset in production: the delay is the window in
                         which you can still stop a sweep you did not intend
  --proof-nonce / --proof-issued / --proof-signature
                         a destination proof signed earlier, so the ceremony
                         can be done in two steps instead of one sitting
  --resume               write the buy leg once the sell has created the
                         devUSDC account it must spend from. Needs only
                         --buy-at-usd and --confirm-min-output-amount: the size
                         and sell price come from the sell leg already on disk

Sweeps move native SOL only. The buy is sized so a completed round trip
returns devUSDC to where it started and leaves the gain in SOL.`

// scheduleWindowFromSellLeg keeps the buy leg on the same cadence the operator
// chose for the sell, rather than silently reverting it to an hour on resume.
func scheduleWindowFromSellLeg(sell config) time.Duration {
	if sell.Swap == nil || sell.Swap.ScheduleWindowSeconds == 0 {
		return time.Hour
	}
	return time.Duration(sell.Swap.ScheduleWindowSeconds) * time.Second
}

// executablePriceFromQuote is the USD-per-SOL the pool actually pays, derived
// from the minimum output the operator confirmed. It mirrors swaprun's own
// executable-price arithmetic rather than restating it in different units.
func executablePriceFromQuote(sizeLamports, minimumOutput uint64) (uint64, error) {
	if sizeLamports == 0 || minimumOutput == 0 {
		return 0, errors.New("a quote is needed to judge whether the price can be filled")
	}
	high, low := bits.Mul64(minimumOutput, lamportsPerSOL)
	if high >= sizeLamports {
		return 0, errors.New("that quote and size multiply out beyond what the agent can represent")
	}
	price, _ := bits.Div64(high, low, sizeLamports)
	return price, nil
}

// defaultStrategySweepMax mirrors the sweep's own --max default. The wizard
// does not override it, so a plan must fit inside it.
const defaultStrategySweepMax = uint64(1_000_000_000)

func runStrategySetup(ctx context.Context, args []string, output io.Writer) (failure error) {
	flags := flag.NewFlagSet("setup strategy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	directory := flags.String("dir", "", "new private setup directory")
	walletKeypair := flags.String("wallet-keypair", "", "private Devnet wallet keypair")
	// One file instead of fifteen flags. The flags stay: the file simply fills
	// them in, so there is one code path and one set of validations.
	fromFile := flags.String("from", "", "a strategy file written by strategy init")
	sizeSOL := flags.String("size-sol", "0.05", "SOL per trade")
	sellAtUSD := flags.String("sell-at-usd", "", "sell only at or above this price")
	buyAtUSD := flags.String("buy-at-usd", "", "buy back only at or below this price")
	destination := flags.String("to", "", "YOUR wallet address for the profit")
	mithrilCommand := flags.String("mithril-command", "", "absolute Mithril executable")
	nodeCommand := flags.String("node-command", "", "absolute Node.js executable")
	quoteScript := flags.String("quote-script", "", "absolute Orca quote adapter")
	quoteSocket := flags.String("quote-socket", "", "protected runtime quote socket")
	primaryTrust := flags.String("primary-trust-domain", "", "primary evidence provider")
	secondaryTrust := flags.String("secondary-trust-domain", "", "second evidence provider")
	acceptQuoted := flags.Bool("accept-quoted-floor", false,
		"take the floor from the live quote at write time; only valid with prices set")
	confirmedMinOut := flags.Uint64("confirm-min-output-amount", 0,
		"confirm the discovered minimum output for the leg being written")
	// A proof signed earlier, so the ceremony can be two steps: read the
	// challenge, sign it in whatever wallet holds the destination key, then
	// come back. Without these the sweep can only be set up from a terminal
	// that can both print the challenge and read the pasted signature.
	proofNonce := flags.String("proof-nonce", "", "nonce of a proof signed earlier")
	proofIssued := flags.String("proof-issued", "", "issue time of a proof signed earlier")
	proofSignature := flags.String("proof-signature", "", "base58 signature of a proof signed earlier")
	// Passed through, NOT defaulted away: the delay is the window in which an
	// operator can notice a destination they did not intend and stop the sweep
	// before it can move anything. Only Devnet testing legitimately sets 0, and
	// the sweep prints the anchor either way.
	// 0 is a MEANINGFUL value here — the sweep reads it as "active immediately"
	// for Devnet testing — so "was it given?" cannot be inferred from the value.
	// swap_setup.go already answers that with flags.Visit; same answer here.
	// One action per schedule window is the rate limit. Hardcoding an hour meant
	// a strategy could only ever act once an hour, and testing the cycle meant
	// waiting one out. swap setup already exposes this; so does this now.
	// Without this a strategy trades EXACTLY ONCE: the floor is pinned to the
	// quote confirmed at setup, our own trade moves the pool below it, and every
	// later cycle reports price_below_floor forever. An unattended strategy has
	// to survive the market moving — including the part we moved ourselves.
	floorToleranceBPS := flags.Uint("floor-tolerance-bps", 100,
		"how far below the agreed floor a fill may still be accepted, in basis points")
	scheduleWindow := flags.Duration("schedule-window", time.Hour,
		"one action per window, per leg (minimum 1 minute)")
	// The signer's daily caps are what actually funds a trade, and this sized
	// them at exactly one. A strategy armed for five trades therefore made one
	// and then spent the rest of the UTC day building transactions the signer
	// refused — reported as an expired blockhash, because the refusal was
	// discarded at the process boundary. The cap is still a hard bound written
	// into the fingerprinted profile; it is now a bound the operator chooses.
	tradesPerDay := flags.Uint64("trades-per-day", defaultTradesPerDay,
		"how many trades per day each leg's caps must fund")
	// The "keep this much, send me the rest" number. Derived when unset, which
	// is what a strategy needs at minimum; naming it can only ask to keep MORE.
	keepSOL := flags.String("keep-sol", "",
		"SOL to keep in the agent wallet; everything above it is swept")
	activationDelay := flags.Duration("activation-delay", 0,
		"override the sweep's activation delay; 0 means active immediately")
	resume := flags.Bool("resume", false, "write the buy leg now that devUSDC exists")
	assumeYes := flags.Bool("yes", false, "take defaults; still refuses anything needing a person")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, strategySetupUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("setup strategy takes no arguments")
	}
	given := map[string]bool{}
	flags.Visit(func(item *flag.Flag) { given[item.Name] = true })

	// With nothing supplied and a person present, ASK. Requiring somebody to
	// discover `strategy init`, then `strategy edit`, then come back here is
	// three commands to learn before anything happens; the questions are the
	// same either way, so the shortest path is to just ask them.
	// --resume supplies none of the values this guard inspects, so without the
	// !*resume term it walks the operator through every question again and then
	// feeds a price into the resume path, where a price against an at-market
	// sell leg is an error.
	if !*resume && *fromFile == "" && *destination == "" && !given["size-sol"] &&
		!given["sell-at-usd"] && !given["buy-at-usd"] && stdinIsTerminal() {
		saved, err := askForStrategyInline(*directory, output)
		if err != nil {
			return err
		}
		*fromFile = saved
	}

	// Alerts named in the file are applied to each leg once the legs exist.
	// Parsing happens with everything else, so a bad threshold stops setup
	// before anything is written.
	var fileAlerts alertsConfig
	telegramState := "enabled"
	if *fromFile != "" {
		file, err := readStrategyFile(*fromFile)
		if err != nil {
			return err
		}
		if err := applyStrategyFile(file, given, strategyFileTargets{
			alerts:  &fileAlerts,
			sizeSOL: sizeSOL, sellAtUSD: sellAtUSD, buyAtUSD: buyAtUSD,
			destination: destination, proofNonce: proofNonce,
			proofIssued: proofIssued, proofSignature: proofSignature,
			keepSOL: keepSOL, scheduleWindow: scheduleWindow,
			activationDelay: activationDelay, tradesPerDay: tradesPerDay,
		}); err != nil {
			return err
		}
		if !file.Telegram.Enabled {
			telegramState = "disabled"
		}
	}

	if *resume {
		// Resume builds a leg on this same host, so it needs the same paths. It
		// returned before discovery ran and failed with "wallet keypair: path
		// must be absolute and clean" — a path error for a path nobody was asked
		// for. There is no orphan concern here: resume ADDS the buy leg.
		if err := fillHostPaths(output, walletKeypair, mithrilCommand, nodeCommand, quoteScript); err != nil {
			return err
		}
		// The plan is NOT rebuilt from flags here: the sell leg on disk already
		// records the size and the sell price it was written with, and retyping
		// them is how a mis-sized buy leg gets attempted.
		confirmQuote := acceptedQuoteConfirmer(*acceptQuoted)
		if *confirmedMinOut == 0 && confirmQuote == nil && stdinIsTerminal() {
			prompt := newPrompter(os.Stdin, output, true)
			confirmQuote = func(quote quoteConfirmation) error {
				return confirmQuoteWithOperator(prompt, quote)
			}
		}
		err := resumeStrategyBuyLeg(ctx, swapSetupOptions{
			walletKeypair:  *walletKeypair,
			mithrilCommand: *mithrilCommand,
			nodeCommand:    *nodeCommand, quoteScript: *quoteScript,
			quoteSocket:  *quoteSocket,
			primaryTrust: *primaryTrust, secondaryTrust: *secondaryTrust,
			confirmedMinOut: *confirmedMinOut,
			// --accept-quoted-floor rides the existing confirmation seam rather
			// than adding a second way to skip it: a callback that accepts IS
			// the agreement, and it is only reachable when prices back it up.
			confirmQuote: confirmQuote,
		}, *buyAtUSD, uint16(*floorToleranceBPS), output)
		if err != nil {
			return err
		}
		paths, _ := discoverStrategy()
		return writeLegAlerts(paths.buy, fileAlerts)
	}
	// This path rewrites the pointer from sell + sweep alone, so a buy leg
	// recorded by an earlier --resume is dropped from it. The leg itself stays
	// on disk, keeps its control file, and its runner keeps the profile in
	// memory — so an ARMED buy leg would go on trading while `strategy stop` no
	// longer names it. That is the "brake that only half worked" case the
	// unreadable-leg list exists to prevent, reached a different way.
	//
	// Carrying the old buy forward instead would be worse: it is sized from the
	// PREVIOUS sell leg's minimum output, so pairing it with a freshly sized
	// sell is a mismatch that only shows up as a refusal much later.
	if recorded, _ := discoverStrategy(); recorded.buy != "" {
		var buyControl string
		if cfg, err := readConfig(recorded.buy); err == nil {
			buyControl = cfg.Control.StatePath
		}
		if _, _, live := controlGrantAt(buyControl); live {
			return fmt.Errorf(
				"the recorded buy leg at %s is still armed; stop it first with\n"+
					"  mithril-agent strategy stop --reason TEXT\n"+
					"then run setup again. Re-running setup re-sizes the sell leg, so the "+
					"buy leg must be rewritten with --resume afterwards",
				recorded.buy)
		}
		if _, err := fmt.Fprintf(output,
			"note: the buy leg at %s is no longer part of this strategy.\n"+
				"      It is re-created by: mithril-agent setup strategy --resume\n",
			recorded.buy); err != nil {
			return err
		}
	}
	// Bounded like the strategy file's own trades_per_day and like the control
	// state machine's actions-per-grant ceiling, so the three cannot disagree.
	if *tradesPerDay == 0 || *tradesPerDay > maxTradesPerDay {
		return fmt.Errorf("--trades-per-day must be between 1 and %d", maxTradesPerDay)
	}

	if err := fillHostPaths(output, walletKeypair, mithrilCommand, nodeCommand, quoteScript); err != nil {
		return err
	}
	plan, err := planStrategy(*sizeSOL, *sellAtUSD, *buyAtUSD)
	if err != nil {
		return err
	}
	plan.tradesPerDay = *tradesPerDay
	// Discover what the host already has rather than making somebody type seven
	// absolute paths they have no way to know. The wizard has done this since it
	// existed; there was no reason this command should not.
	home, _ := os.UserHomeDir()
	if *directory == "" {
		*directory = filepath.Join(firstNonEmpty(home, "."), ".mithril-agent", "strategy")
	}
	if *walletKeypair == "" {
		*walletKeypair = filepath.Join(filepath.Dir(*directory), "agent-account.json")
	}
	*mithrilCommand = firstNonEmpty(*mithrilCommand,
		detectInstalled("mithril-node", "mithril-mcp"), detectExecutable("mithril"))
	*nodeCommand = firstNonEmpty(*nodeCommand, detectInstalled("node"), detectExecutable("node"))
	*quoteScript = firstNonEmpty(*quoteScript, detectSourceAdapter(), detectInstalled("quote.mjs"))
	*primaryTrust = firstNonEmpty(*primaryTrust, "primary-provider")
	*secondaryTrust = firstNonEmpty(*secondaryTrust, "secondary-provider")

	var missing []string
	for _, item := range []struct{ name, value string }{
		{"--to (the wallet profit goes to)", *destination},
		{"--mithril-command", *mithrilCommand},
		{"--node-command", *nodeCommand},
		{"--quote-script", *quoteScript},
	} {
		if item.value == "" {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"could not find these on this host, so pass them: %s", strings.Join(missing, ", "))
	}
	if err := acceptQuotedFloorAllowed(*acceptQuoted, plan.atMarket); err != nil {
		return err
	}
	if *confirmedMinOut == 0 && !*acceptQuoted {
		// Ask, rather than sending somebody to a second command to fetch a
		// number and paste it back. The agreement is the same: a person sees the
		// exact floor that will be written and says yes.
		agreed, err := confirmQuotedFloor(ctx, *walletKeypair, *nodeCommand, *quoteScript,
			plan.sizeLamports, uint16(*floorToleranceBPS), output)
		if err != nil {
			return err
		}
		*confirmedMinOut = agreed
	}
	// The threshold is a FLOOR ON THE FILL, not just a signal: swaprun refuses
	// to sell below it however high the oracle reads. A threshold the pool
	// cannot reach therefore configures a leg that can never execute — it sits
	// "waiting" forever with the trigger satisfied, which reads as a broken
	// agent.
	//
	// The price MUST come from the pool, never from --confirm-min-output-amount.
	// That flag is what the OPERATOR SAID, and deriving the pool price from it
	// meant a mistyped value produced a fabricated price AND advice to adopt it:
	// passing 1 yielded "the pool pays about $0.000200 ... lower --sell-at-usd
	// to $0.000200 or below" while the pool was really paying $20.92. Following
	// that advice configures an agent that sells at any price — a wrong number
	// from the operator turned into a dangerous instruction back to them.
	livePrice, err := poolPriceForSize(ctx, *walletKeypair, *nodeCommand, *quoteScript,
		plan.sizeLamports, uint16(*floorToleranceBPS))
	if err != nil {
		return err
	}
	executable := livePrice
	if !plan.atMarket && plan.sellAtMicros > executable {
		return fmt.Errorf(
			"the pool pays about $%s for %s SOL right now, so a sell at $%s could never "+
				"execute — the price you set is a floor on the fill, not just a signal; "+
				"lower --sell-at-usd to $%s or below",
			formatUnits(executable, 6), formatUnits(plan.sizeLamports, 9),
			formatUnits(plan.sellAtMicros, 6), formatUnits(executable, 6))
	}
	if _, err := fmt.Fprint(output, describeStrategyPlan(plan)); err != nil {
		return err
	}
	// The plan above quotes a gain per round trip. Whether a round trip can
	// happen at all depends on the ORACLE, which nothing here had ever looked
	// at — so the gain was stated for pairs that could never complete.
	if _, err := fmt.Fprint(output, describeTriggerReach(ctx, plan)); err != nil {
		return err
	}
	// The wallet is validated properly further in, so a failure to read it here
	// only means no warning — never a wrong one.
	setupOwner, _ := walletAddress(*walletKeypair)
	if _, err := fmt.Fprint(output, warnAboutRestartingTheSpendingDay(setupOwner)); err != nil {
		return err
	}
	// Without this the setup dies at the sell leg complaining that the
	// operator's own --dir is a missing "parent" — after the plan is printed.
	// setup.go:376 fixed the same class the same way.
	if err := os.MkdirAll(*directory, 0o700); err != nil {
		return errors.New("could not create the strategy directory")
	}

	// The SELL leg first: it is the one that can be written on a fresh wallet,
	// and running it once is what creates the devUSDC account the buy leg must
	// pin before it can exist at all.
	sellDir := filepath.Join(*directory, "sell")
	sell, err := createSwapSetup(ctx, swapSetupOptions{
		directory: sellDir, direction: "sell",
		walletKeypair:  *walletKeypair,
		mithrilCommand: *mithrilCommand,
		nodeCommand:    *nodeCommand, quoteScript: *quoteScript,
		quoteSocket:   *quoteSocket,
		inputLamports: plan.sizeLamports, dailyDebitCap: 0,
		tradesPerDay:      plan.tradesPerDay,
		slippageBPS:       100,
		floorToleranceBPS: uint16(*floorToleranceBPS),
		reserveLamports:   defaultSwapReserve, maxFeeLamports: defaultSwapMaxFee,
		scheduleWindow: *scheduleWindow,
		primaryTrust:   *primaryTrust, secondaryTrust: *secondaryTrust,
		confirmedMinOut: *confirmedMinOut,
		// --accept-quoted-floor rides the existing confirmation seam rather than
		// adding a second way to skip it: a callback that accepts IS the
		// agreement, and it is only reachable when prices back the floor up.
		confirmQuote: acceptedQuoteConfirmer(*acceptQuoted),
		sellAtMicros: plan.sellAtMicros,
	})
	if err != nil {
		return fmt.Errorf("sell leg: %w", err)
	}
	// Every refusal AFTER this point used to leave the sell leg behind, and the
	// next attempt was then refused by setup's own guard — "already exists, and
	// setup will not write into a directory it did not create" — for a directory
	// setup had just created and abandoned. An operator correcting one number
	// hit a second, unrelated error and had to delete files by hand to retry.
	//
	// Only directories the successful setup calls below just made are removed,
	// and only before the whole strategy is recorded as complete. A failure to
	// print the final instructions must not erase a usable setup.
	sellDirectory := filepath.Dir(sell.ConfigPath)
	createdDirectories := []string{sellDirectory}
	setupComplete := false
	defer func() {
		cleanupIncompleteStrategy(failure, setupComplete, createdDirectories)
	}()
	// The sweep floor must reserve BOTH legs, including a buy leg that does not
	// exist yet. Reserving it now is what lets --resume write that leg later
	// without redoing the sweep, whose destination proof would otherwise have to
	// be signed a second time.
	sellCfg, err := readSwapConfig(sell.ConfigPath)
	if err != nil {
		return fmt.Errorf("re-read the sell leg: %w", err)
	}
	// The sweep counts every same-owner setup it can discover, not just the two
	// this wizard wrote. Computing the floor from our own legs alone meant an
	// operator who had run `mithril-agent setup` first was refused by the sweep
	// — after the destination retype and the signature — for being short by
	// exactly that older leg. Ask the same loader the sweep asks.
	discovered, err := loadSwapNeeds(nil, []string{sell.ConfigPath, discoverCurrentConfig()})
	if err != nil {
		return err
	}
	siblingReserve, _, err := siblingSwapReserve(sellCfg.Swap.Owner(), discovered)
	if err != nil {
		return err
	}
	floor, err := strategyFloorLamports(siblingReserve, plannedBuyRequirement())
	if err != nil {
		return err
	}
	// A named keep-behind can only RAISE the floor. Below what the legs need,
	// the sweep would drain the wallet under the trader and every later trade
	// would be refused for insufficient balance — the failure is far from the
	// cause, so it is refused here where the number is being chosen.
	if *keepSOL != "" {
		wanted, err := parseDecimalUnitsLamports(*keepSOL, "keep-sol")
		if err != nil {
			return err
		}
		if wanted < floor {
			return fmt.Errorf(
				"keeping %s SOL would starve the trades, which need %s SOL behind; "+
					"raise keep_sol or lower size_sol",
				formatUnits(wanted, 9), formatUnits(floor, 9))
		}
		floor = wanted
	}
	sweepDir := filepath.Join(*directory, "sweep")
	sweepArgs := []string{
		"--wallet", *walletKeypair, "--to", *destination,
		"--dir", sweepDir,
		"--floor", formatUnits(floor, 9),
		// The smallest sweep is the guaranteed gain: the default 0.1 SOL would
		// silently swallow every realistic profit at agent/policy.go's minimum
		// check, leaving an operator waiting on a sweep that never fires.
		"--min", formatUnits(plan.gainLamports, 9),
		"--swap-config", sell.ConfigPath,
		// The sweep runs its own MCP health observer, exactly as the swap legs
		// do. Omitting this wrote a sweep whose runner died on startup with
		// "MCP observer: command must be an absolute path" — a config that
		// looked complete and could never run.
		"--mithril-command", *mithrilCommand,
	}
	if given["activation-delay"] {
		sweepArgs = append(sweepArgs, "--activation-delay", activationDelay.String())
	}
	for flag, value := range map[string]string{
		"--proof-nonce": *proofNonce, "--proof-issued": *proofIssued,
		"--proof-signature": *proofSignature,
	} {
		if value != "" {
			sweepArgs = append(sweepArgs, flag, value)
		}
	}
	if *assumeYes {
		sweepArgs = append(sweepArgs, "--yes")
	}
	if err := runSweepSetup(ctx, sweepArgs, output); err != nil {
		return fmt.Errorf("sweep: %w", err)
	}
	createdDirectories = append(createdDirectories, sweepDir)

	paths := strategyPaths{
		sell: sell.ConfigPath, sweep: filepath.Join(sweepDir, "config.json"),
		telegram: telegramState,
	}
	if !usableConfigPath(paths.sweep) {
		return errors.New("sweep setup did not write a usable configuration")
	}
	if err := writeLegAlerts(paths.sell, fileAlerts); err != nil {
		return err
	}
	// The buy price is the one setting --resume cannot recover on its own: it is
	// a choice, not something derivable from the sell leg, and the strategy
	// pointer records paths only. An operator who put buy_at_usd in their one
	// config file was still refused later for not passing --buy-at-usd, by a
	// command the documentation says takes no arguments.
	//
	// Written only once the setup has succeeded, so a run that failed halfway
	// leaves nothing behind — the same reason the sell directory is removed.
	if err := recordPlannedBuyPrice(*directory, *buyAtUSD); err != nil {
		return err
	}
	if err := recordStrategy(paths); err != nil {
		return fmt.Errorf("record the strategy: %w", err)
	}
	setupComplete = true
	_, err = fmt.Fprintf(output,
		"\nStrategy written\n  sell   %s\n  buy    pending — run: mithril-agent setup strategy --resume\n"+
			"  sweep  %s\n\nNext\n"+
			"  1. mithril-agent service install --output \"$HOME/.mithril-agent/mithril-agent-run.service\"\n"+
			"                                         (once per host; prints the install commands)\n"+
			"  2. mithril-agent start                 (says what is left to do)\n",
		paths.sell, paths.sweep)
	return err
}

func cleanupIncompleteStrategy(failure error, complete bool, directories []string) {
	if failure == nil || complete {
		return
	}
	for i := len(directories) - 1; i >= 0; i-- {
		// A recursive delete is not a place to rely on a downstream path.
		if filepath.IsAbs(directories[i]) {
			_ = os.RemoveAll(directories[i])
		}
	}
}

// plannedBuyRequirement is the SOL a buy leg will demand once it exists. It
// mirrors Profile.WalletRequirementLamports for the buy direction rather than
// inventing arithmetic: reserve, fee, and the temporary account's rent, with no
// input term because a buy spends devUSDC.
func plannedBuyRequirement() uint64 {
	return defaultSwapReserve + defaultSwapMaxFee + orcaswap.DefaultMaxTemporaryRentLamports
}

// strategyPlan is the arithmetic every leg is built from. Deriving it once, up
// front, is what lets the sweep floor reserve a buy leg that does not exist yet.
type strategyPlan struct {
	sizeLamports uint64
	sellAtMicros uint64
	buyAtMicros  uint64
	// atMarket means the legs carry no price trigger and trade at whatever the
	// pool gives.
	atMarket bool
	// buyInput is the devUSDC the buy leg spends: exactly what one sell yields.
	buyInput uint64
	// gainLamports is what a completed round trip adds, in SOL.
	gainLamports uint64
	// tradesPerDay sizes the signer's daily caps. It is not derived from the
	// prices, so planStrategy does not set it; the caller does, from the flag or
	// the strategy file.
	tradesPerDay uint64
}

// planStrategy turns the operator's three numbers into every derived amount,
// refusing anything that cannot work before a single file is written.
func planStrategy(sizeSOL, sellAtUSD, buyAtUSD string) (strategyPlan, error) {
	// Both prices omitted means market orders: the legs trade at whatever the
	// pool gives. That is the only shape that can complete a cycle on a pool
	// whose price disagrees with the oracle, and arming it still needs an
	// explicit --allow-any-price.
	atMarket := sellAtUSD == "" && buyAtUSD == ""
	if !atMarket && (sellAtUSD == "" || buyAtUSD == "") {
		return strategyPlan{}, errors.New(
			"set BOTH --sell-at-usd and --buy-at-usd, or neither to trade at market")
	}
	sizeLamports, err := parseDecimalUnitsLamports(sizeSOL, "trade size")
	if err != nil {
		return strategyPlan{}, err
	}
	if atMarket {
		// No thresholds, so no derivable gain. The sweep minimum falls back to
		// the fee floor: enough that a sweep is worth its own cost, low enough
		// that it does not sit unreachable.
		return strategyPlan{sizeLamports: sizeLamports, atMarket: true,
			gainLamports: 2 * defaultSweepFee}, nil
	}
	sellAtMicros, err := parseUSDThreshold(sellAtUSD, "sell price")
	if err != nil {
		return strategyPlan{}, err
	}
	buyAtMicros, err := parseUSDThreshold(buyAtUSD, "buy price")
	if err != nil {
		return strategyPlan{}, err
	}
	if buyAtMicros >= sellAtMicros {
		return strategyPlan{}, fmt.Errorf(
			"buy at $%s must be below sell at $%s, or the round trip loses money "+
				"and one price reading could trigger both legs",
			formatUnits(buyAtMicros, 6), formatUnits(sellAtMicros, 6))
	}
	buyInput, err := buyInputForSell(sizeLamports, sellAtMicros)
	if err != nil {
		return strategyPlan{}, err
	}
	gain, err := roundTripGainLamports(sizeLamports, sellAtMicros, buyAtMicros)
	if err != nil {
		return strategyPlan{}, err
	}
	// The sweep will not send anything below its minimum, and the minimum is set
	// to this gain. A gain under the fees it costs to move means the profit can
	// never leave the agent account — better to refuse than to configure a
	// strategy whose proceeds are structurally unsweepable.
	if gain < 2*defaultSweepFee {
		return strategyPlan{}, fmt.Errorf(
			"a round trip at these prices gains %s SOL, less than the %s SOL it costs "+
				"to sweep; widen the gap between the prices or increase --size-sol",
			formatUnits(gain, 9), formatUnits(2*defaultSweepFee, 9))
	}
	// Bounded above as well. The sweep's largest single transfer defaults to
	// 1 SOL, and a gain over that is refused by the sweep profile itself —
	// after the destination ceremony, with the sell leg already written, by an
	// error that names neither the gain nor the size that caused it.
	if gain > defaultStrategySweepMax {
		return strategyPlan{}, fmt.Errorf(
			"a round trip at these prices gains %s SOL, more than the %s SOL the sweep "+
				"will move at once; reduce --size-sol or narrow the price gap",
			formatUnits(gain, 9), formatUnits(defaultStrategySweepMax, 9))
	}
	return strategyPlan{
		sizeLamports: sizeLamports, sellAtMicros: sellAtMicros,
		buyAtMicros: buyAtMicros, buyInput: buyInput, gainLamports: gain,
	}, nil
}

// describeStrategyPlan is what the operator reads before anything is written.
// The derivation is shown rather than asserted: a number nobody can check is a
// number nobody should agree to.
func describeStrategyPlan(plan strategyPlan) string {
	if plan.atMarket {
		return fmt.Sprintf(
			"No price conditions: each leg trades %s SOL at whatever the pool gives,\n"+
				"once per schedule window, only while you have armed it.\n"+
				"  Smallest sweep: %s SOL\n",
			formatUnits(plan.sizeLamports, 9), formatUnits(plan.gainLamports, 9))
	}
	return fmt.Sprintf(
		"The buy leg will spend exactly what one sell is guaranteed to produce:\n"+
			"  %s SOL x $%s = %s devUSDC\n"+
			"Sized that way, a completed round trip puts the devUSDC back where it\n"+
			"started and leaves the gain in SOL - the only asset the sweep can send.\n"+
			"  Estimated gain per round trip: %s SOL\n"+
			"  Smallest sweep set to that gain, so it is actually sweepable.\n"+
			"\nThis estimate assumes the pool trades near the oracle price. The\n"+
			"trigger decides WHEN a leg may fire; the pool decides what it returns.\n"+
			"The buy leg is sized from the sell's own guaranteed minimum output,\n"+
			"so it can always be funded by one sell whatever the pool does.\n",
		formatUnits(plan.sizeLamports, 9), formatUnits(plan.sellAtMicros, 6),
		formatUnits(plan.buyInput, 6), formatUnits(plan.gainLamports, 9))
}

// resumeStrategyBuyLeg writes the buy leg after the first sell has created the
// devUSDC account it must spend from. The sweep is deliberately NOT touched: its
// floor already reserved this leg, so resuming costs no second destination
// proof and no re-signing.
func resumeStrategyBuyLeg(
	ctx context.Context, options swapSetupOptions, buyAtUSD string,
	floorToleranceBPS uint16, output io.Writer,
) error {
	paths, _ := discoverStrategy()
	if paths.sell == "" {
		return errors.New("no strategy sell leg was found; run setup strategy first")
	}
	if paths.buy != "" {
		// Idempotent, matching `setup`: re-running must not die inside a
		// directory check that already exists.
		_, err := fmt.Fprintf(output, "Buy leg already configured, leaving it alone:\n  %s\n", paths.buy)
		return err
	}
	sellCfg, err := readSwapConfig(paths.sell)
	if err != nil {
		return fmt.Errorf("sell leg: %w", err)
	}
	if options.quoteSocket == "" {
		options.quoteSocket = sellCfg.Quote.SocketPath
	}
	// Everything the buy leg needs is already recorded in the sell leg, so the
	// two cannot disagree about what this strategy is.
	// The buy is sized from the sell's guaranteed MINIMUM OUTPUT, not from the
	// sell price, so a market-mode sell needs no trigger here. Demanding one was
	// left over from when the sizing used the price, and it blocked exactly the
	// configuration that can complete a cycle on a pool the oracle disagrees with.
	sellPrice := ""
	if trigger := sellCfg.Swap.PriceTrigger; trigger != nil {
		sellPrice = formatUnits(trigger.ThresholdMicros, 6)
	}
	if buyAtUSD == "" {
		buyAtUSD = plannedBuyPrice(filepath.Dir(filepath.Dir(paths.sell)))
	}
	if err := priceModeMismatch(sellPrice, buyAtUSD); err != nil {
		return err
	}
	plan, err := planStrategy(
		formatUnits(sellCfg.Swap.InputLamports, 9), sellPrice, buyAtUSD,
	)
	if err != nil {
		return err
	}
	// Taken from the sell leg rather than a flag: the two legs must fund the same
	// number of trades or the pair stalls at whichever runs out first, and nobody
	// resuming days later remembers what they passed at setup.
	plan.tradesPerDay = sellCfg.Swap.FundedTradesPerDay()
	if plan.tradesPerDay == 0 {
		return errors.New("the sell leg's daily caps fund no trades; run setup strategy again")
	}
	if _, err := fmt.Fprint(output, describeStrategyPlan(plan)); err != nil {
		return err
	}

	owner := sellCfg.Swap.Owner()
	_, exists, err := walletTokenBalance(ctx, owner, orcaswap.DevnetUSDCMint)
	if err != nil {
		return fmt.Errorf("read the devUSDC balance: %w", err)
	}
	if !exists {
		return errors.New(
			"this wallet has no devUSDC account yet; the buy leg pins the account it spends " +
				"from, so run the sell leg once first — it creates that account")
	}
	// Resume honours --accept-quoted-floor for the same reason the fresh setup
	// does: the buy leg carries a price trigger inherited from the sell, and
	// that trigger is the floor. Demanding the number here too left the round
	// trip half-configurable in one command and half in a retry loop.
	if options.confirmedMinOut == 0 && options.confirmQuote == nil {
		// Returning here is why this refusal could not name a number: it fires
		// before anything is quoted, so it could only send the operator away to
		// run `swap discover --direction buy` and work out --spend-usdc and
		// --floor-tolerance-bps themselves — then race the pool between reading
		// the number and pasting it.
		//
		// The sell leg has never done this. Letting the quote run and refusing
		// WITH its value gives the buy leg the same one-line answer, from the
		// same seam, with no second command and no race.
		options.confirmQuote = func(quote quoteConfirmation) error {
			return quoteDeclined(false, "--confirm-min-output-amount", quote.MinOutput)
		}
	}
	options.directory = filepath.Join(filepath.Dir(filepath.Dir(paths.sell)), "buy")
	options.direction = "buy"
	// Inherited, not re-typed. The two legs are judged against the SAME two
	// evidence providers or they are not one strategy, and a resume that left
	// these empty produced a buy leg whose preflight failed five bindings at
	// once — with nothing in the message connecting that to a flag nobody knew
	// to pass. The sell leg records them, exactly as it records size and price.
	if options.primaryTrust == "" {
		options.primaryTrust = sellCfg.Evidence.PrimaryTrustDomain
	}
	if options.secondaryTrust == "" {
		options.secondaryTrust = sellCfg.Evidence.SecondaryTrustDomain
	}
	// Sized from the sell leg's own guaranteed minimum output, NOT from
	// size x sell-price: the trigger's oracle decides WHEN the sell may fire,
	// while the pool decides what it actually returns, and the two diverge.
	// Sizing on the oracle left the buy wanting devUSDC the sell never produced.
	options.inputTokenAmount = sellCfg.Swap.MinimumOutput()
	if options.inputTokenAmount == 0 {
		return errors.New("the sell leg records no minimum output to size the buy from")
	}
	// Left at zero so createSwapSetup sizes both from tradesPerDay, the same way
	// the sell leg's debit cap is sized. Pinning them here to one trade's worth
	// is what made every buy after the day's first fail inside the signer.
	options.tradesPerDay = plan.tradesPerDay
	options.slippageBPS = 100
	// The tolerance is applied when the floor is written (relaxRouteFloor), not
	// stored on the profile, so the buy leg takes the same flag value.
	options.floorToleranceBPS = floorToleranceBPS
	options.reserveLamports = defaultSwapReserve
	options.maxFeeLamports = defaultSwapMaxFee
	options.scheduleWindow = scheduleWindowFromSellLeg(sellCfg)
	options.buyAtMicros = plan.buyAtMicros
	result, err := createSwapSetup(ctx, options)
	if err != nil {
		return err
	}
	paths.buy = result.ConfigPath
	if err := recordStrategy(paths); err != nil {
		// Recording is discoverability, never authority: the leg is written and
		// usable by path either way, so this reports rather than fails.
		_, _ = fmt.Fprintf(output, "note: the strategy pointer was not updated: %v\n", err)
	}
	_, err = fmt.Fprintf(output,
		"Buy leg written to %s\nThe sweep floor already reserved it. Then:\n"+
			"  sudo systemctl restart mithril-agent-run\n"+
			"  mithril-agent start\n", result.ConfigPath)
	return err
}

// readStrategyFile loads the one file a strategy is described by, through the
// same hardened, size-bounded, strictly-decoded path every other private file
// uses — so an unknown field is a refusal, not a silently ignored setting.
func readStrategyFile(path string) (strategyFile, error) {
	var file strategyFile
	if err := readStrictJSON(path, &file); err != nil {
		return strategyFile{}, fmt.Errorf("strategy file: %w", err)
	}
	if err := file.validate(); err != nil {
		return strategyFile{}, fmt.Errorf("strategy file: %w", err)
	}
	return file, nil
}

// confirmQuotedFloor reads the live quote, shows it, and takes the operator's
// yes. It replaces "run swap discover, copy the number, paste it into a flag",
// which is the same agreement with three more steps and a chance to paste a
// stale number from a market that has since moved.
func confirmQuotedFloor(
	ctx context.Context, walletKeypair, nodeCommand, quoteScript string,
	sizeLamports uint64, toleranceBPS uint16, output io.Writer,
) (uint64, error) {
	owner, err := walletAddress(walletKeypair)
	if err != nil {
		return 0, fmt.Errorf("agent account: %w", err)
	}
	route, err := swapSetupDiscover(ctx, owner, nodeCommand, quoteScript, sizeLamports, 100)
	if err != nil {
		return 0, fmt.Errorf("read a live quote: %w", err)
	}
	// Setup writes the RELAXED floor, so the number shown and agreed to must be
	// that one. Showing the raw quote would have the operator agree to a figure
	// the policy never receives — and then be refused for not matching it.
	minimum, err := relaxRouteFloor(route.MinOutputAmount, toleranceBPS)
	if err != nil {
		return 0, err
	}
	prompt := newPrompter(os.Stdin, output, stdinIsTerminal())
	prompt.sayf("\nThe trade this will configure")
	prompt.sayf("-----------------------------")
	prompt.sayf("  Spend at most:    %s SOL", formatUnits(sizeLamports, 9))
	prompt.sayf("  Receive at least: %s devUSDC", formatUnits(minimum, 6))
	prompt.sayf("")
	prompt.sayf("That floor is written into the policy. If the real fill would come in")
	prompt.sayf("below it, the trade is refused rather than filled at a worse price.")
	agreed, err := prompt.confirm("Write this as the configured trade?", false)
	if err != nil {
		return 0, err
	}
	if !agreed {
		return 0, quoteDeclined(stdinIsTerminal(), "--confirm-min-output-amount", minimum)
	}
	return minimum, nil
}

// askForStrategyInline runs the same guided questions `strategy edit` asks, then
// writes the answers so the operator has a file they can re-run or hand to
// someone. One command, and they still end up with the artefact.
func askForStrategyInline(directory string, output io.Writer) (string, error) {
	home, _ := os.UserHomeDir()
	base := directory
	if base == "" {
		base = filepath.Join(firstNonEmpty(home, "."), ".mithril-agent")
	}
	path := filepath.Join(base, "strategy.json")

	current := strategyFile{}
	if existing, err := readStrategyFile(path); err == nil {
		// Re-running must not silently discard a destination proof somebody
		// signed, so an existing file becomes the starting point.
		current = existing
		if _, err := fmt.Fprintf(output, "Using your existing answers from %s\n", path); err != nil {
			return "", err
		}
	}
	prompt := newPrompter(os.Stdin, output, true)
	next, err := guideStrategyFile(prompt, current)
	if err != nil {
		return "", err
	}
	if err := next.validate(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", errors.New("could not create the agent directory")
	}
	if err := writeStrategyFile(path, next); err != nil {
		return "", err
	}
	_, err = fmt.Fprintf(output,
		"\nSaved your answers to %s\n(edit them later with: mithril-agent strategy edit %s)\n",
		path, path)
	return path, err
}

// priceModeMismatch refuses a sell and buy leg that disagree about whether the
// strategy trades at chosen prices or at market, and names the fix for the
// mismatch that ACTUALLY happened.
//
// The check catches BOTH mismatches; the advice must match the one that
// actually happened. A single message described only the market-sell case,
// so a priced sell resuming without --buy-at-usd was told to REMOVE the
// flag it needed to ADD — advice that guarantees the next attempt fails the
// same way.
func priceModeMismatch(sellPrice, buyAtUSD string) error {
	switch {
	case sellPrice == "" && buyAtUSD != "":
		return errors.New(
			"the sell leg and the buy leg must agree: either both have prices, or neither. " +
				"This sell trades at market, so omit --buy-at-usd")
	case sellPrice != "" && buyAtUSD == "":
		return fmt.Errorf(
			"the sell leg and the buy leg must agree: either both have prices, or neither. "+
				"This sell is priced at $%s, so pass the price to buy back at:  --buy-at-usd N "+
				"(below $%s, by enough to cover the sweep)",
			sellPrice, sellPrice)
	}
	return nil
}

// fillHostPaths resolves whatever the operator did not type and reports what it
// found, so both the fresh-setup and the resume path get the same answers from
// the same place.
func fillHostPaths(output io.Writer, wallet, mithril, node, quote *string) error {
	typed := hostPaths{
		walletKeypair:  *wallet,
		mithrilCommand: *mithril,
		nodeCommand:    *node,
		quoteScript:    *quote,
	}
	found := resolveHostPaths(typed)
	if err := missingHostPaths(found); err != nil {
		return err
	}
	if lines := describeResolvedHostPaths(typed, found); len(lines) != 0 {
		if _, err := fmt.Fprintf(output, "Using paths found on this host:\n%s\n\n",
			strings.Join(lines, "\n")); err != nil {
			return err
		}
	}
	*wallet, *mithril, *node, *quote =
		found.walletKeypair, found.mithrilCommand, found.nodeCommand, found.quoteScript
	return nil
}

// poolPriceForSize reads what the pool pays for one trade, right now, from the
// pool itself. It exists so the executability check can never be computed from
// a number the operator supplied: advice about the market has to come from the
// market.
func poolPriceForSize(
	ctx context.Context, walletKeypair, nodeCommand, quoteScript string,
	sizeLamports uint64, toleranceBPS uint16,
) (uint64, error) {
	owner, err := walletAddress(walletKeypair)
	if err != nil {
		return 0, fmt.Errorf("agent account: %w", err)
	}
	route, err := swapSetupDiscover(ctx, owner, nodeCommand, quoteScript, sizeLamports, 100)
	if err != nil {
		return 0, fmt.Errorf("read a live quote: %w", err)
	}
	// The relaxed floor is what setup actually writes, so it is what the leg
	// will be judged against.
	minimum, err := relaxRouteFloor(route.MinOutputAmount, toleranceBPS)
	if err != nil {
		return 0, err
	}
	return executablePriceFromQuote(sizeLamports, minimum)
}

// acceptQuotedFloorAllowed decides whether the operator may skip pasting a
// minimum-output number back.
//
// Pasting it is friction with no safety when a PRICE TRIGGER exists: the
// trigger is itself a floor on the fill — the agent refuses to sell below it
// however the pool moves — so the quoted minimum is a second, faster-moving
// bound underneath a bound the operator already chose. The pool re-quotes every
// few seconds, so confirming it means discover, parse, paste, and race, often
// several times; nobody reviews a number they are chasing.
//
// At MARKET there is no trigger, and the quoted minimum is the ONLY thing
// standing between the agent and any price the pool offers. Skipping it there
// would remove the single protection, so it is refused.
func acceptQuotedFloorAllowed(accept, atMarket bool) error {
	if accept && atMarket {
		return errors.New(
			"--accept-quoted-floor needs prices to fall back on: this strategy trades at " +
				"market, where the quoted minimum is the only floor there is. Set " +
				"sell_at_usd and buy_at_usd, or confirm the number with " +
				"--confirm-min-output-amount")
	}
	return nil
}

// acceptedQuoteConfirmer returns a confirmation that accepts the live quote, or
// nil to leave the operator's own confirmation in place. Returning nil rather
// than a rejecting callback matters: createSwapSetup treats a nil confirmer as
// "the operator supplied a number", which is exactly the untouched path.
func acceptedQuoteConfirmer(accept bool) func(quoteConfirmation) error {
	if !accept {
		return nil
	}
	return func(quoteConfirmation) error { return nil }
}

// plannedBuyPriceName is the buy price setup was given, kept beside the legs so
// --resume does not have to ask for it back.
const plannedBuyPriceName = "planned-buy-usd"

// recordPlannedBuyPrice notes the buy price for a later --resume. A market-mode
// strategy has none, and writing an empty file would make "no price" and "not
// recorded" indistinguishable, so nothing is written and the absence means
// market — the same thing an absent flag means.
//
// Failing to record is not fatal: --buy-at-usd still works, so a lost note
// costs a retype, never the ability to resume.
func recordPlannedBuyPrice(directory, buyAtUSD string) error {
	if buyAtUSD == "" {
		return nil
	}
	path := filepath.Join(directory, plannedBuyPriceName)
	if err := securefile.ReplacePrivate(path, []byte(buyAtUSD+"\n"), 64); err != nil {
		return fmt.Errorf("record the buy price for --resume: %w", err)
	}
	return nil
}

// plannedBuyPrice reads that note back, and is silent about every failure: an
// unreadable note must degrade to "not recorded", which --resume already
// handles by refusing and naming --buy-at-usd.
//
// The value is re-validated by planStrategy like any typed one, so a tampered
// note cannot widen anything a flag could not. It lives in the same 0700
// directory as the profiles themselves, so writing it already requires the
// ability to write those.
func plannedBuyPrice(directory string) string {
	raw, err := securefile.ReadPrivate(filepath.Join(directory, plannedBuyPriceName), 64)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// warnAboutRestartingTheSpendingDay names the one consequence of re-running
// setup that nothing else announces.
//
// A leg's daily caps are enforced by a ledger that lives inside that leg's own
// state directory. Setup refuses to write into a directory it did not create,
// so re-running it produces a NEW directory and therefore a NEW, empty ledger:
// the day's accumulated spend does not follow. Today's total across the old leg
// and the new one can then exceed either leg's daily cap.
//
// The direction that matters is the counter-intuitive one. An operator who
// decides a cap is too generous and LOWERS it gets a fresh full day at the new
// figure on top of what the old leg already spent — so tightening, the safest
// thing they can do, widens the day it is applied to. Nothing said so.
//
// This warns rather than refuses: re-running setup is legitimate and often
// necessary, and a caps change cannot be applied any other way while the caps
// live inside a signed profile. Refusing would block the fix along with the
// footgun.
func warnAboutRestartingTheSpendingDay(owner string) string {
	if owner == "" {
		return ""
	}
	paths, _ := discoverStrategy()
	var existing []string
	for _, leg := range paths.configured() {
		cfg, err := readConfig(leg.path)
		if err != nil || cfg.Swap == nil || cfg.Swap.Owner() != owner {
			continue
		}
		existing = append(existing, leg.leg+" "+leg.path)
	}
	if len(existing) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"\nThis wallet already has %d configured leg(s):\n  %s\n\n"+
			"Their daily caps are counted in a ledger inside each leg's own state\n"+
			"directory. This setup writes new legs with new, empty ledgers, so what\n"+
			"they have already spent today does NOT carry over — today's total across\n"+
			"old and new can exceed either one's daily cap.\n"+
			"If you are LOWERING a cap, it governs a fresh full day: the tightening\n"+
			"does not really apply until 00:00 UTC. Stop the old legs first, or wait\n"+
			"for the reset, if that matters to you.\n",
		len(existing), strings.Join(existing, "\n  "))
}
