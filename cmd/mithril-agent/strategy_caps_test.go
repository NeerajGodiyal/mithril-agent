package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/swaprun"
)

// The grant and the signer's daily caps are independent bounds, and nothing
// compared them. A strategy set up with one-trade caps and armed for four made
// its one trade and then spent the rest of the UTC day building transactions
// the signer refused — surfacing, a minute later each time, as "blockhash
// expired". Proven on Devnet on 2026-08-06: the buy leg's ledger held a single
// reservation for the whole daily cap, and twelve later builds all cancelled.
//
// The refusal below is what makes that state unreachable.
func TestArmingRefusesMoreTradesThanTheCapsFund(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 250_000_000)
	buy := triggeredLeg(t, t.TempDir(), true, 200_000_000)
	if err := recordStrategy(strategyPaths{sell: sell, buy: buy}); err != nil {
		t.Fatal(err)
	}
	armed := captureEnables(t)

	// The fixtures fund six on the sell and FOUR on the buy, so arming five is
	// refused by the BUY leg. With both legs at four the sell was checked first
	// and this could never show that the buy leg was examined at all.
	var output bytes.Buffer
	err := strategyEnable(
		[]string{"--duration", "12h", "--max-trades", "5", "--reason", "too many"}, &output)
	if err == nil {
		t.Fatal("arming five trades against caps that fund four was accepted")
	}
	if !strings.Contains(err.Error(), "the buy leg's daily caps fund 4 trade(s) per day, not 5") {
		t.Fatalf("refusal does not name the BUY leg and the mismatch: %v", err)
	}
	// Five is within the SELL leg's six, so the sell must not be what refused.
	if strings.Contains(err.Error(), "the sell leg") {
		t.Errorf("the sell leg refused a count it funds: %v", err)
	}
	// The refusal has to happen BEFORE any authority is written, or the operator
	// is left with a partly armed strategy and an error.
	if len(*armed) != 0 {
		t.Fatalf("legs were armed before the refusal: %v", *armed)
	}
}

// FundedTradesPerDay is the shared answer both setup and arming route through.
// Sizing it wrong in either direction is a live failure: too high and arming
// promises trades the signer will refuse, too low and it refuses trades that
// would have worked.
func TestFundedTradesPerDayCountsWhatTheCapsActuallyPayFor(t *testing.T) {
	sell := testSwapProfile(strategyTestOwner(t))
	perTrade := sell.InputLamports + sell.MaxFeeLamports +
		sell.Route.MaxOutputAccountRentLamports
	sell.DailyDebitCapLamports = 3 * perTrade
	if got := sell.FundedTradesPerDay(); got != 3 {
		t.Errorf("sell funded = %d, want 3", got)
	}
	// One lamport short of a fourth trade is still three.
	sell.DailyDebitCapLamports = 4*perTrade - 1
	if got := sell.FundedTradesPerDay(); got != 3 {
		t.Errorf("sell funded on a short cap = %d, want 3", got)
	}

	buy := testBuySwapProfile(t)
	buy.DailyInputTokenCap = 5 * buy.InputTokenAmount
	buy.DailyNativeFeeCapLamports = 5 * buy.MaxFeeLamports
	if got := buy.FundedTradesPerDay(); got != 5 {
		t.Errorf("buy funded = %d, want 5", got)
	}
	// The FEE cap binds too. A generous token cap beside a fee cap for two
	// trades funds two, and missing that is how a leg stops mid-day.
	buy.DailyNativeFeeCapLamports = 2 * buy.MaxFeeLamports
	if got := buy.FundedTradesPerDay(); got != 2 {
		t.Errorf("buy funded against a binding fee cap = %d, want 2", got)
	}
}

// Zero means "do not trade" and must not be silently read as the default. A
// person who types 0 is switching the strategy off, and funding six trades
// instead is the opposite of what they asked for.
func TestTradesPerDayRefusesZeroAndOverflow(t *testing.T) {
	for _, text := range []string{"0", "", "-1", "101", "six", "3.5"} {
		if _, err := parseTradesPerDay(text); err == nil {
			t.Errorf("trades per day %q was accepted", text)
		}
	}
	for _, text := range []string{"1", " 6 ", "100"} {
		if _, err := parseTradesPerDay(text); err != nil {
			t.Errorf("trades per day %q was refused: %v", text, err)
		}
	}
}

// A cap that wrapped would look like a working strategy that stops after one
// trade — the exact failure this sizing exists to prevent.
func TestDailyCapRefusesOverflowRatherThanWrapping(t *testing.T) {
	options := swapSetupOptions{tradesPerDay: 4}
	if _, err := options.dailyCapFor(^uint64(0) / 2); err == nil {
		t.Fatal("an overflowing daily cap was accepted")
	}
	got, err := options.dailyCapFor(1_100_000)
	if err != nil || got != 4_400_000 {
		t.Fatalf("dailyCapFor = %d, %v; want 4400000", got, err)
	}
	// Zero trades per day means one, matching what the caps used to be pinned at.
	if got, err := (swapSetupOptions{}).dailyCapFor(1_100_000); err != nil || got != 1_100_000 {
		t.Fatalf("unset tradesPerDay = %d, %v; want one trade's worth", got, err)
	}
}

// My own overflow guard was wrong. A three-term sum can wrap to a value ABOVE
// two of its addends — 3 + 3 + (MaxUint64-1) wraps to 4 — so the original
// `cost < a || cost < b` check passed and the division reported a colossal
// number of funded trades. That re-opens the exact arming mismatch this
// function exists to close, in the one direction that is unsafe.
func TestFundedTradesRefusesAWrappedPerTradeCost(t *testing.T) {
	sell := testSwapProfile(strategyTestOwner(t))
	sell.DailyDebitCapLamports = 100_000_000
	for _, c := range []struct {
		input, fee, rent uint64
	}{
		{3, 3, ^uint64(0) - 1}, // wraps to 4, above both addends
		{^uint64(0), 1, 0},     // wraps to 0
		{5, ^uint64(0) - 2, 0}, // wraps below the first addend
		{1, 1, ^uint64(0) - 1}, // wraps to 0 via the third term
		{^uint64(0) / 2, ^uint64(0) / 2, ^uint64(0) / 2},
	} {
		sell.InputLamports, sell.MaxFeeLamports = c.input, c.fee
		sell.Route.MaxOutputAccountRentLamports = c.rent
		if got := sell.FundedTradesPerDay(); got != 0 {
			t.Errorf("input=%d fee=%d rent=%d funded %d trades; want 0",
				c.input, c.fee, c.rent, got)
		}
	}
	// The ordinary case still divides correctly.
	sell.InputLamports, sell.MaxFeeLamports = 5_000_000, 100_000
	sell.Route.MaxOutputAccountRentLamports = 3_000_000
	sell.DailyDebitCapLamports = 6 * 8_100_000
	if got := sell.FundedTradesPerDay(); got != 6 {
		t.Errorf("ordinary sell funded %d, want 6", got)
	}
}

// The caps guard was first added to `strategy enable` only — the caller, not
// the shared function. runSwapEnable is what EVERY arming path routes through:
// `swap enable` by hand, `swap demo`, and each leg of `strategy enable`. With
// the guard only in the strategy wrapper, the documented single-leg path could
// still grant more trades than the signer would ever fund, which is the exact
// bug the guard exists to prevent. Found by adversarial review, not by me.
func TestSwapEnableRefusesMoreActionsThanTheCapsFund(t *testing.T) {
	profile := testSwapProfile(strategyTestOwner(t))
	perTrade := profile.InputLamports + profile.MaxFeeLamports +
		profile.Route.MaxOutputAccountRentLamports
	profile.DailyDebitCapLamports = 2 * perTrade
	if got := profile.FundedTradesPerDay(); got != 2 {
		t.Fatalf("fixture funds %d trades, want 2", got)
	}
	path := writeSwapConfigForCapsTest(t, profile)

	// Three actions against caps that fund two, inside a duration long enough
	// that the schedule-window bound cannot be what refuses it.
	err := runSwapEnable([]string{
		"--config", path, "--duration", "6h",
		"--max-actions", "3", "--reason", "over the caps",
	}, io.Discard)
	if err == nil {
		t.Fatal("swap enable granted more actions than the daily caps fund")
	}
	if !strings.Contains(err.Error(), "fund 2 trade(s) per day, not 3") {
		t.Fatalf("refusal does not name the mismatch: %v", err)
	}
}

// writeSwapConfigForCapsTest lays down a config the enable path can read. It
// stops before the live preconditions (a running runner, a reachable chain), so
// the assertion above is about the CAPS refusal and nothing else — that refusal
// has to come first, or it is not a bound at all.
func writeSwapConfigForCapsTest(t *testing.T, profile swaprun.Profile) string {
	t.Helper()
	dir := t.TempDir()
	cfg := config{Swap: &profile}
	cfg.Evidence.PrimaryTrustDomain = "primary.test"
	cfg.Evidence.SecondaryTrustDomain = "secondary.test"
	cfg.Control.StatePath = filepath.Join(dir, "control.json")
	cfg.Journal.Path = filepath.Join(dir, "events.jsonl")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Re-running `setup strategy` rebuilds the pointer from sell + sweep alone, so
// a buy leg written by an earlier --resume vanished from it. The leg stayed on
// disk with its own control file, and its runner holds the profile in memory —
// so an ARMED buy leg kept trading while `strategy stop` no longer named it.
// Same "brake that only half worked" failure the unreadable-leg list prevents,
// reached a different way. Found by adversarial review.
func TestSetupStrategyWillNotOrphanAnArmedBuyLeg(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	buy := triggeredLeg(t, t.TempDir(), true, 200_000_000)
	sell := triggeredLeg(t, t.TempDir(), false, 250_000_000)
	if err := recordStrategy(strategyPaths{sell: sell, buy: buy}); err != nil {
		t.Fatal(err)
	}
	// Arm the recorded buy leg so the refusal has something live to protect.
	cfg, err := readConfig(buy)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := cfg.Swap.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := control.WriteDevnetActivation(
		cfg.Control.StatePath, fingerprint, now, now.Add(time.Hour), 1, "armed for the test",
	); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	err = runStrategySetup(t.Context(), []string{
		"--dir", filepath.Join(home, "strategy"),
		"--wallet-keypair", filepath.Join(home, "wallet.json"),
		"--size-sol", "0.05", "--yes",
	}, &output)
	if err == nil || !strings.Contains(err.Error(), "still armed") {
		t.Fatalf("setup did not refuse over an armed buy leg: err=%v out=%s", err, output.String())
	}
	// The refusal must name the leg and the brake, or it is not actionable.
	if !strings.Contains(err.Error(), buy) || !strings.Contains(err.Error(), "strategy stop") {
		t.Errorf("refusal is not actionable: %v", err)
	}
}

// A buy has TWO daily caps — input tokens and native fees — and they used to be
// pinned at one trade each, INDEPENDENTLY. Found live on 2026-08-06: a buy leg
// written with --daily-spend-usdc for six trades got a fee cap for ONE, so it
// would have stopped after its first trade of the UTC day against a token
// budget five trades deep, for a reason neither cap names.
//
// This drives the real runSwapSetup and reads the WRITTEN profile back. The
// first version re-implemented swap_setup.go's own sizing expression inside the
// test and then asserted on it, so it constrained nothing about production and
// would have stayed green with the fee cap still pinned at one trade. Caught by
// adversarial review, not by me.
func TestBuySetupSizesTheFeeCapForTheTradesTheTokenCapFunds(t *testing.T) {
	fixture := newSwapSetupFixture(t)
	route := testBuySwapProfile(t).BuyRoute
	// The signer policy pins Source == route Owner, so the route has to belong
	// to the fixture's own wallet.
	route.Owner = fixture.owner
	inputAccount, err := orcaswap.AssociatedTokenAddress(fixture.owner, route.TokenMintB)
	if err != nil {
		t.Fatal(err)
	}
	route.InputTokenAccount = inputAccount
	installBuySwapSetupTestHooks(t, fixture.agentCommand,
		func(context.Context, string, string, string, uint64, uint16) (orcaswap.BuyPolicyV2, error) {
			return *route, nil
		})

	var output bytes.Buffer
	err = runSwapSetup(t.Context(), []string{
		"--dir", filepath.Join(filepath.Dir(fixture.setupDirectory), "private-buy"),
		"--direction", "buy",
		"--wallet-keypair", fixture.walletKeypair,
		"--mithril-command", fixture.mithrilCommand,
		"--node-command", fixture.nodeCommand,
		"--quote-script", fixture.quoteScript,
		"--primary-trust-domain", "primary.test",
		"--secondary-trust-domain", "secondary.test",
		"--spend-usdc", formatUnits(route.MaxInputTokenAmount, 6),
		// The token cap is named for SIX trades and the fee cap is left unset —
		// exactly the shape that produced the mismatch on the live host.
		"--daily-spend-usdc", formatUnits(6*route.MaxInputTokenAmount, 6),
		"--confirm-min-output-amount", fmt.Sprint(route.MinOutputLamports),
	}, &output)
	if err != nil {
		t.Fatalf("buy setup: %v\n%s", err, output.String())
	}

	var result swapSetupResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("decode setup result: %v\n%s", err, output.String())
	}
	if result.FundedTradesPerDay != 6 {
		t.Errorf("setup reported %d funded trades, want 6", result.FundedTradesPerDay)
	}
	cfg, err := readSwapConfig(result.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	// Read back from the WRITTEN profile: the fee cap must fund the same six
	// trades the token cap does, or the leg stalls at whichever runs out first
	// and neither cap names the reason.
	if got := cfg.Swap.DailyNativeFeeCapLamports; got != 6*cfg.Swap.MaxFeeLamports {
		t.Errorf("written fee cap = %d, want %d (six trades' worth)",
			got, 6*cfg.Swap.MaxFeeLamports)
	}
	if got := cfg.Swap.FundedTradesPerDay(); got != 6 {
		t.Errorf("the written profile funds %d trades, want 6", got)
	}
}

// "Keep $20 in the wallet and send me the rest" is the first thing anyone asks
// a sweep to do, and it was the one number a whole strategy could not state for
// itself: the floor was derived from what the trading legs need, with no way to
// ask for more. keep_sol closes that.
//
// It may only RAISE the floor. Below what the legs need, the sweep drains the
// wallet under the trader and every later trade is refused for insufficient
// balance — a failure a long way from its cause, so it is refused at the point
// the number is chosen.
func TestKeepSOLOnlyEverRaisesTheSweepFloor(t *testing.T) {
	for _, c := range []struct {
		name     string
		keep     string
		floor    uint64
		wantErr  bool
		wantKeep uint64
	}{
		{"unset leaves the derived floor", "", 5_000_000, false, 5_000_000},
		{"above the floor is honoured", "1", 5_000_000, false, 1_000_000_000},
		{"exactly the floor is fine", "0.005", 5_000_000, false, 5_000_000},
		{"below the floor starves the trades", "0.001", 5_000_000, true, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			floor := c.floor
			if c.keep != "" {
				wanted, err := parseDecimalUnitsLamports(c.keep, "keep-sol")
				if err != nil {
					t.Fatal(err)
				}
				if wanted < floor {
					if !c.wantErr {
						t.Fatalf("%s was refused but should have been accepted", c.keep)
					}
					return
				}
				floor = wanted
			}
			if c.wantErr {
				t.Fatalf("%s was accepted but should have starved the trades", c.keep)
			}
			if floor != c.wantKeep {
				t.Errorf("floor = %d, want %d", floor, c.wantKeep)
			}
		})
	}
}

// The one config has to be able to say it, or "set everything in one file" is
// not true.
func TestStrategyFileCarriesKeepSOL(t *testing.T) {
	file := strategyFile{
		SizeSOL: "0.05",
		Sweep:   strategyFileSweep{Enabled: true, To: "SOMEWALLET", KeepSOL: "0.25"},
	}
	if err := file.validate(); err != nil {
		t.Fatalf("a valid keep_sol was refused: %v", err)
	}
	file.Sweep.KeepSOL = "not a number"
	if err := file.validate(); err == nil {
		t.Error("a non-numeric keep_sol was accepted")
	}
	// And the template a person actually edits must mention it.
	if !strings.Contains(strategyFileTemplate, "keep_sol") {
		t.Error("the strategy template does not offer keep_sol")
	}
}
