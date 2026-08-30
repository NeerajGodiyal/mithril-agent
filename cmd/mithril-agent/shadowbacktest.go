package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/bits"
	"path/filepath"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

// A continuous shadow run scores the rule as it happens. This command reruns a
// sell-then-buy-back rule over prices the observer already recorded, which lets
// an operator compare thresholds without waiting for another live period. The
// second leg spends exactly what the first produced and the spread plus two
// fees comes out of one book.
//
// One thing it does NOT do is pretend to know the pool. The recorded quotes
// belong only to decisions the original policy actually made; hypothetical
// thresholds have no venue quote at every tick. Their fills therefore have to
// be modelled from a spread the operator supplies — and the report says so on
// its own face. A backtest that hides its assumptions is worse than no backtest.
const shadowBacktestUsage = `Usage: mithril-agent shadow backtest --policy PATH --dir PATH
                              --buy-at-usd PRICE [--spread-bps N] [--day YYYY-MM-DD] [--json]

Scores a sell-then-buy-back round trip against the prices a shadow run already
recorded, on ONE set of books, with the same ledger and report the live observer
uses.

  --policy PATH     the shadow policy whose sell rule and sizing to reuse
  --dir PATH        the directory holding recorded shadow journals
  --buy-at-usd P    buy back at or below this price; must be BELOW the policy's
                    sell price, or one reading could satisfy both legs
  --spread-bps N    how much worse than the oracle the pool is assumed to fill,
                    each way (default 100 = 1%). This is a MODEL, not a quote.
  --day DATE        which recorded UTC day to score (default: the latest)
  --json            emit the report as JSON

The result is only as honest as --spread-bps. Read the pool's real quote with
"mithril-agent swap discover" and set it from what you actually see.`

func runShadowBacktest(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow backtest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "shadow policy JSON")
	directory := flags.String("dir", "", "journal directory")
	buyAtUSD := flags.String("buy-at-usd", "", "buy back at or below this price")
	spreadBPS := flags.Uint("spread-bps", 100, "assumed pool cost each way, in basis points")
	day := flags.String("day", "", "UTC day, YYYY-MM-DD")
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowBacktestUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *policyPath == "" || *directory == "" || *buyAtUSD == "" {
		return errors.New("shadow backtest requires --policy, --dir and --buy-at-usd")
	}
	// A pool that costs nothing is the single easiest way to make a paper
	// result flatter itself, and 100% would consume every trade.
	if *spreadBPS == 0 || *spreadBPS >= 10_000 {
		return errors.New("--spread-bps must be between 1 and 9999")
	}
	buyAtMicros, err := parseUSDThreshold(*buyAtUSD, "buy price")
	if err != nil {
		return err
	}

	policy, err := loadShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	journalPolicy := policy
	// The return leg is the policy's own rule with the direction flipped, so a
	// round trip cannot silently read a different feed or a different source
	// pair than the sell it is paired with.
	returnLeg := policy.Trigger
	returnLeg.Direction = pricetrigger.BuyAtOrBelow
	returnLeg.ThresholdMicros = buyAtMicros
	if !policy.IsSell() {
		returnLeg.Direction = pricetrigger.SellAtOrAbove
	}
	policy.ReturnTrigger = &returnLeg

	chosen, err := chooseShadowDay(*directory, *day)
	if err != nil {
		return err
	}
	ticks, err := readShadowTicks(
		filepath.Join(*directory, "shadow-"+chosen+".jsonl"), journalPolicy,
	)
	if err != nil {
		return err
	}
	if _, err := shadow.Replay(journalPolicy, ticks); err != nil {
		return fmt.Errorf("replay source shadow journal: %w", err)
	}
	prices := observedPrices(ticks)
	if len(prices) < 2 {
		return errors.New("that day recorded fewer than two observable prices to score")
	}

	result, err := shadow.ReplayRoundTripTicks(
		policy, ticks, modelledPool(uint64(*spreadBPS), policy.SlippageBPS),
	)
	if err != nil {
		return err
	}
	report, err := shadow.BuildReport(
		policy, result.Ledger, shadow.Counts{}, shadow.Stats{},
		result.ClosingPrice, ticks[0].At, ticks[len(ticks)-1].At,
	)
	if err != nil {
		return err
	}
	return writeBacktest(output, *asJSON, chosen, uint64(*spreadBPS), result, report)
}

// observedPrices keeps only ticks where the market could actually be read. A
// gap is not a price, and carrying one forward would invent a decision the
// observer never had the evidence to make.
func observedPrices(ticks []shadow.Tick) []uint64 {
	prices := make([]uint64, 0, len(ticks))
	for _, tick := range ticks {
		if tick.PriceMicros != 0 && !tick.PeriodClose {
			prices = append(prices, tick.PriceMicros)
		}
	}
	return prices
}

// modelledPool turns an oracle price into the quote a pool is ASSUMED to give,
// worse than the oracle by spreadBPS in whichever direction the trade goes.
//
// SOL carries 9 decimals and devUSDC 6, and a price is USD micros per whole
// SOL, so one lot of lamports yields lot*price/1e9 devUSDC units, and spending
// u devUSDC units yields u*1e9/price lamports.
func modelledPool(
	spreadBPS uint64, slippageBPS uint16,
) func(uint64, bool, uint64) (shadow.Quote, error) {
	return func(price uint64, sell bool, input uint64) (shadow.Quote, error) {
		if price == 0 || input == 0 {
			return shadow.Quote{}, errors.New("cannot model a fill at a zero price")
		}
		if slippageBPS == 0 || slippageBPS >= 10_000 {
			return shadow.Quote{}, errors.New("cannot model a fill with invalid slippage")
		}
		multiplier, divisor := price, uint64(lamportsPerSOL)
		if sell {
			multiplier, divisor = price, lamportsPerSOL
		} else {
			multiplier, divisor = lamportsPerSOL, price
		}
		out, ok := boundedMulDiv(input, multiplier, divisor)
		if !ok {
			return shadow.Quote{}, errors.New("the modelled fill is out of range")
		}
		out, ok = boundedMulDiv(out, 10_000-spreadBPS, 10_000)
		if !ok || out == 0 {
			return shadow.Quote{}, errors.New("the modelled fill rounds to nothing at this price")
		}
		minimum, ok := boundedMulDivCeil(out, 10_000-uint64(slippageBPS), 10_000)
		if !ok || minimum == 0 {
			return shadow.Quote{}, errors.New("the modelled slippage floor is out of range")
		}
		return shadow.Quote{
			InputAmount: input, EstimatedOutput: out, MinimumOutput: minimum,
		}, nil
	}
}

func boundedMulDiv(value, multiplier, divisor uint64) (uint64, bool) {
	high, low := bits.Mul64(value, multiplier)
	if divisor == 0 || high >= divisor {
		return 0, false
	}
	result, _ := bits.Div64(high, low, divisor)
	return result, true
}

func boundedMulDivCeil(value, multiplier, divisor uint64) (uint64, bool) {
	high, low := bits.Mul64(value, multiplier)
	if divisor == 0 || high >= divisor {
		return 0, false
	}
	result, remainder := bits.Div64(high, low, divisor)
	if remainder != 0 {
		if result == ^uint64(0) {
			return 0, false
		}
		result++
	}
	return result, true
}

type backtestResult struct {
	Day string `json:"day"`
	// PoolModelled is stated in the payload, not just the prose, so a machine
	// reading this cannot present it as an observed result either.
	PoolModelled   bool                   `json:"pool_modelled"`
	SpreadBPS      uint64                 `json:"assumed_spread_bps"`
	Counts         shadow.RoundTripCounts `json:"counts"`
	RealizedMicros int64                  `json:"realized_micros"`
	VersusHold     int64                  `json:"versus_hold_micros"`
	ClosingEquity  uint64                 `json:"closing_equity_micros"`
	OpeningEquity  uint64                 `json:"opening_equity_micros"`
}

func writeBacktest(
	output io.Writer, asJSON bool, day string, spreadBPS uint64,
	result shadow.RoundTripResult, report shadow.Report,
) error {
	if asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(backtestResult{
			Day: day, PoolModelled: true, SpreadBPS: spreadBPS,
			Counts:         result.Counts,
			RealizedMicros: report.RealizedMicros,
			VersusHold:     report.VersusHoldMicros,
			ClosingEquity:  report.ClosingEquityMicros,
			OpeningEquity:  report.OpeningEquityMicros,
		})
	}
	w := func(format string, args ...any) { fmt.Fprintf(output, format, args...) }
	w("\nRound trip over recorded prices — %s\n", day)
	w("  legs       %d sell(s), %d buy(s), %d refused\n",
		result.Counts.Sells, result.Counts.Buys, result.Counts.Refused)
	w("  signals    %d sell, %d buy\n",
		result.Counts.SellSignals, result.Counts.BuySignals)
	w("  realized   $%s\n", formatSignedMicros(report.RealizedMicros))
	w("  vs holding $%s   <- the only number that answers \"was this worth doing\"\n",
		formatSignedMicros(report.VersusHoldMicros))
	// The assumption goes last, where a reader stops, rather than buried above
	// the numbers it produced.
	w("\n  The pool was MODELLED at %d bps each way, not quoted.\n", spreadBPS)
	w("  Read the real number with: mithril-agent swap discover --direction sell ...\n")
	return nil
}

// formatSignedMicros prints a signed USD-micros value with its sign, because a
// loss that renders as a bare number reads as a profit at a glance.
func formatSignedMicros(value int64) string {
	if value < 0 {
		// -(MinInt64) overflows an int64. Convert the magnitude without ever
		// constructing that unrepresentable positive value.
		magnitude := uint64(-(value + 1)) + 1
		return "-" + formatUnits(magnitude, 6)
	}
	return "+" + formatUnits(uint64(value), 6)
}
