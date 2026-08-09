package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

// strategyEnable arms every leg of one strategy with one command.
//
// It writes NO control document itself. It calls the same runSwapEnable and
// runDevnetEnable a person would call by hand, so every bound they enforce
// still applies: <=24h, 1..100 actions, one action per schedule window, a CAS
// on the current revision for swap legs, refusal over a live or exhausted
// grant, refusal on an unacknowledged terminal, and a live-runner precondition.
// This command adds refusals; it removes none.
//
// It is deliberately not the inverse of strategyStop in one respect: stop
// continues past a failure because a half-applied brake must still be applied
// as far as it can go, while enable ALSO continues but reports non-zero,
// because each grant is independently bounded and independently revocable, and
// an in-process rollback would be a lie — a kill between two writes cannot be
// undone. What must not happen is arming something the refusals below forbid,
// so every check runs before any grant is written.
var (
	// Replaceable in tests: arming for real needs a live runner and a chain.
	enableSwapLeg  = runSwapEnable
	enableSweepLeg = runDevnetEnable
)

func suggestedStrategyEnableCommand(paths strategyPaths) (string, error) {
	var (
		fundedLimit uint64
		atMarket    bool
	)
	for _, leg := range paths.configured() {
		cfg, err := readConfig(leg.path)
		if err != nil {
			return "", fmt.Errorf("read the %s leg: %w", leg.leg, err)
		}
		if cfg.Swap == nil {
			continue
		}
		funded := cfg.Swap.FundedTradesPerDay()
		if funded == 0 {
			return "", fmt.Errorf("the %s leg's daily caps fund no trades", leg.leg)
		}
		if fundedLimit == 0 || funded < fundedLimit {
			fundedLimit = funded
		}
		atMarket = atMarket || cfg.Swap.PriceTrigger == nil
	}
	if fundedLimit == 0 {
		return "", errors.New("the strategy has no trading leg")
	}
	allowAnyPrice := ""
	if atMarket {
		allowAnyPrice = " --allow-any-price"
	}
	return fmt.Sprintf(
		"strategy enable --duration 8h --max-trades %d%s --reason TEXT",
		fundedLimit, allowAnyPrice), nil
}

func strategyEnable(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("strategy enable", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	duration := flags.Duration("duration", 0, "how long the whole strategy stays armed")
	maxTrades := flags.Uint("max-trades", 1, "trades each leg may make, 1..100")
	reason := flags.String("reason", "", "why the strategy is being armed (recorded)")
	// A leg with no price condition trades at whatever the market gives. That is
	// refused by default because it is not a choice to make for a whole strategy
	// in one command — but it is the ONLY way to run a cycle on a pool whose
	// price disagrees with the oracle, which is every Devnet pool, so the
	// operator can say so explicitly.
	anyPrice := flags.Bool("allow-any-price", false,
		"permit legs with no price trigger; they trade at whatever the pool gives")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, strategyUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *reason == "" || *duration == 0 {
		return errors.New("strategy enable requires --duration and --reason")
	}

	paths, unreadable := discoverStrategy()
	if paths.empty() && len(unreadable) == 0 {
		return errors.New("no configured strategy was found; run mithril-agent setup strategy first")
	}
	if len(unreadable) != 0 {
		for _, entry := range unreadable {
			if _, err := fmt.Fprintf(output,
				"  %-6s CANNOT BE READ — nothing was armed: %s\n", "?", entry); err != nil {
				return err
			}
		}
		return fmt.Errorf("%d recorded leg(s) cannot be read; nothing was armed", len(unreadable))
	}
	legs, err := loadStrategyLegs(paths)
	if err != nil {
		return err
	}
	if err := refuseUnsafeStrategy(legs, *anyPrice, uint64(*maxTrades)); err != nil {
		return err
	}
	if *anyPrice {
		for _, leg := range legs {
			if !leg.isSweep && !leg.hasTrigger {
				if _, err := fmt.Fprintf(output,
					"  %-6s NO PRICE CONDITION — it will trade at whatever the pool gives\n",
					leg.name); err != nil {
					return err
				}
			}
		}
	}

	failures := 0
	armed := 0
	for _, leg := range legs {
		// A sweep whose first window has not opened yet would burn a <=24h grant
		// on a window that opens later, leaving the operator with a dead grant
		// and a runner reporting failure. Skipping is the honest answer.
		if leg.anchorUnix > time.Now().UTC().Unix() {
			if _, err := fmt.Fprintf(output, "  %-6s skipped — its first window opens %s\n",
				leg.name, time.Unix(leg.anchorUnix, 0).UTC().Format(time.RFC3339)); err != nil {
				return err
			}
			continue
		}
		enable := enableSwapLeg
		if leg.isSweep {
			enable = enableSweepLeg
		}
		if err := enable([]string{
			"--config", leg.path,
			"--duration", duration.String(),
			"--max-actions", fmt.Sprint(*maxTrades),
			"--reason", *reason,
		}, io.Discard); err != nil {
			failures++
			if _, writeErr := fmt.Fprintf(output,
				"  %-6s NOT ARMED — %v\n", leg.name, err); writeErr != nil {
				return writeErr
			}
			continue
		}
		armed++
		if _, err := fmt.Fprintf(output, "  %-6s armed   %d trade(s), for %s\n",
			leg.name, *maxTrades, *duration); err != nil {
			return err
		}
	}
	if failures != 0 {
		return fmt.Errorf("%d leg(s) were not armed", failures)
	}
	if armed == 0 {
		// Every leg skipped is not success: `strategy enable && ...` scripts saw
		// exit 0 having armed nothing at all.
		return errors.New("no leg was armed; every one was skipped for the reason shown")
	}
	return nil
}

// strategyLeg is what the refusals below need to reason about a leg without
// re-reading its config three times.
type strategyLeg struct {
	name       string
	path       string
	owner      string
	isSweep    bool
	threshold  uint64
	direction  pricetrigger.Direction
	hasTrigger bool
	anchorUnix int64
	// funded is how many trades this leg's own daily caps can pay for. Zero for
	// a sweep, which is bounded by its policy rather than by a swap profile.
	funded uint64
}

func loadStrategyLegs(paths strategyPaths) ([]strategyLeg, error) {
	var legs []strategyLeg
	for _, item := range paths.configured() {
		cfg, err := readConfig(item.path)
		if err != nil {
			return nil, fmt.Errorf("%s leg: %w", item.leg, err)
		}
		if cfg.Swap != nil {
			// readConfig checks neither Swap.Validate nor the agreement between a
			// profile's direction and its trigger. Deciding whether to ARM on
			// unvalidated data is the wrong order: the refusals below would read
			// a direction the profile could never legally carry.
			if _, err := readSwapConfig(item.path); err != nil {
				return nil, fmt.Errorf("%s leg: %w", item.leg, err)
			}
		}
		leg := strategyLeg{name: item.leg, path: item.path}
		switch {
		case cfg.Swap != nil:
			leg.owner = cfg.Swap.Owner()
			leg.anchorUnix = cfg.Swap.ScheduleAnchorUnix
			leg.funded = cfg.Swap.FundedTradesPerDay()
			if trigger := cfg.Swap.PriceTrigger; trigger != nil {
				leg.hasTrigger = true
				leg.threshold = trigger.ThresholdMicros
				leg.direction = trigger.Direction
			}
		case cfg.hasLegacyProfile():
			leg.isSweep = true
			leg.owner = cfg.Profile.Source
			leg.anchorUnix = cfg.Profile.ScheduleAnchorUnix
		default:
			return nil, fmt.Errorf("%s leg has no profile", item.leg)
		}
		legs = append(legs, leg)
	}
	return legs, nil
}

// refuseUnsafeStrategy runs BEFORE any grant is written. Every check here is
// strictly more conservative than the status quo: two hand-run `swap enable`
// calls can already arm a trigger-less buy and a trigger-less sell against one
// wallet with nothing between them.
func refuseUnsafeStrategy(legs []strategyLeg, anyPrice bool, maxTrades uint64) error {
	// The grant and the signer's daily caps are independent bounds, and nothing
	// used to compare them. Granting five actions against caps that fund one
	// meant four guaranteed refusals — each one building, simulating, and then
	// sitting until its blockhash aged out, so the operator saw "blockhash
	// expired" every few minutes for the rest of the UTC day and no cause at
	// all. Refusing here is the whole fix: the grant can no longer promise what
	// the profile cannot pay for.
	for _, leg := range legs {
		if leg.isSweep {
			continue
		}
		if leg.funded < maxTrades {
			return fmt.Errorf(
				"the %s leg's daily caps fund %d trade(s) per day, not %d; "+
					"lower --max-trades or raise trades_per_day and run setup strategy again",
				leg.name, leg.funded, maxTrades)
		}
	}

	var owner string
	for index, leg := range legs {
		if index == 0 {
			owner = leg.owner
			continue
		}
		// Legs on different wallets are not one strategy, and arming them
		// together would hide that from whoever typed one command.
		if leg.owner != owner {
			return fmt.Errorf(
				"the %s leg belongs to %s, not %s; a strategy is one wallet",
				leg.name, leg.owner, owner)
		}
	}

	var sellAt, buyAt uint64
	for _, leg := range legs {
		if leg.isSweep {
			continue
		}
		// Arming a trade with no price condition means it fires at whatever the
		// market happens to be. That is a real choice for a single hand-run
		// trade; it is not one to make for a whole strategy in one command.
		if !leg.hasTrigger {
			if anyPrice {
				// Explicitly accepted. It still cannot exceed the profile's own
				// caps, its schedule window, or the bounded grant.
				continue
			}
			return fmt.Errorf(
				"the %s leg has no price trigger; pass --allow-any-price to trade at "+
					"whatever the pool gives, or set one with setup strategy", leg.name)
		}
		switch leg.direction {
		case pricetrigger.SellAtOrAbove:
			sellAt = leg.threshold
		case pricetrigger.BuyAtOrBelow:
			buyAt = leg.threshold
		}
	}

	// With buy-below strictly under sell-above, no single price reading can
	// satisfy both: a sell needs min(price-confidence) >= sellAt and a buy needs
	// max(price+confidence) <= buyAt, and the low bound never exceeds the high
	// one. Overlapping thresholds would let one reading arm both sides of a
	// round trip against one balance.
	if sellAt != 0 && buyAt != 0 && buyAt >= sellAt {
		return fmt.Errorf(
			"buy at $%s is not below sell at $%s; one price could trigger both legs "+
				"against the same balance",
			formatUnits(buyAt, 6), formatUnits(sellAt, 6))
	}
	return nil
}
