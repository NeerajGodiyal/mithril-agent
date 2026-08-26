package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStrategySetupRejectsFloorToleranceBeforeNarrowing(t *testing.T) {
	err := runStrategySetup(context.Background(), []string{
		"--floor-tolerance-bps", "70000",
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "at most 2000") {
		t.Fatalf("oversized floor tolerance = %v, want a refusal before uint16 conversion", err)
	}
}

func TestOlderStrategyIsDecodedForMigrationWithoutLosingItsProof(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.json")
	legacy := map[string]any{
		"size_sol": "0.05", "sell_at_usd": "250", "buy_at_usd": "200",
		"sweep": map[string]any{
			"enabled": true, "to": "wallet", "proof_nonce": "nonce",
			"proof_issued": "issued", "proof_signature": "signature",
		},
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readStrategyFileForEdit(path)
	if err != nil {
		t.Fatalf("older strategy could not seed guided migration: %v", err)
	}
	if got.SizeSOL != "0.05" || got.Sweep.To != "wallet" ||
		got.Sweep.ProofNonce != "nonce" || got.Sweep.ProofIssued != "issued" ||
		got.Sweep.ProofSignature != "signature" {
		t.Fatalf("older strategy fields were lost: %+v", got)
	}
	if _, err := readStrategyFile(path); err == nil {
		t.Fatal("older strategy was accepted without the new provider names")
	}
}

// The plan is derived once, before any file is written, so a strategy that
// cannot work is refused rather than half-configured.
func TestStrategyPlanDerivesTheBuyFromTheSell(t *testing.T) {
	plan, err := planStrategy("0.05", "250", "200")
	if err != nil {
		t.Fatal(err)
	}
	if plan.sizeLamports != 50_000_000 {
		t.Errorf("size = %d lamports", plan.sizeLamports)
	}
	if plan.buyInput != 12_500_000 {
		t.Errorf("buy input = %d, want 12.50 devUSDC", plan.buyInput)
	}
	if plan.gainLamports != 12_500_000 {
		t.Errorf("gain = %d lamports, want 0.0125 SOL", plan.gainLamports)
	}
	// The operator has to be able to check the arithmetic, not trust it.
	description := describeStrategyPlan(plan)
	for _, expected := range []string{"0.050000000", "250.000000", "12.500000", "0.012500000"} {
		if !strings.Contains(description, expected) {
			t.Errorf("the description hid %q:\n%s", expected, description)
		}
	}
}

// A round trip whose gain is smaller than the fees to move it configures a
// profit that structurally cannot be swept. Refusing beats leaving an operator
// waiting on a sweep that can never fire.
func TestStrategyPlanRefusesAGainSmallerThanTheSweepCosts(t *testing.T) {
	_, err := planStrategy("0.05", "250", "249.999")
	if err == nil {
		t.Fatal("a strategy whose gain cannot be swept was accepted")
	}
	if !strings.Contains(err.Error(), "to sweep") {
		t.Errorf("the refusal did not explain the sweep minimum: %v", err)
	}
}

// HALF a price pair is always a mistake — one leg gated, the other not, with no
// way to tell which the operator meant. Neither price is a deliberate choice:
// trade at market, which is the only shape that completes a cycle on a pool
// whose price disagrees with the oracle.
func TestStrategyPlanRefusesHalfAPricePair(t *testing.T) {
	for name, prices := range map[string][2]string{
		"no sell": {"", "200"},
		"no buy":  {"250", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := planStrategy("0.05", prices[0], prices[1]); err == nil {
				t.Fatal("half a price pair was accepted")
			}
		})
	}
	plan, err := planStrategy("0.05", "", "")
	if err != nil {
		t.Fatalf("a market-order strategy was refused: %v", err)
	}
	if !plan.atMarket || plan.sellAtMicros != 0 || plan.buyAtMicros != 0 {
		t.Fatalf("market plan carries thresholds: %+v", plan)
	}
	// The sweep minimum must still be reachable, or the profit cannot leave.
	if plan.gainLamports != 2*defaultSweepFee {
		t.Fatalf("market sweep minimum = %d, want the fee floor", plan.gainLamports)
	}
}

func TestDefaultStrategyDirectoryDoesNotCollideWithPointer(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	directory := defaultStrategyDirectory(home)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := recordStrategy(strategyPaths{sell: writeLeg(t, directory, "sell")}); err != nil {
		t.Fatalf("record strategy beside the default generated directory: %v", err)
	}
	pointer, err := strategyPointerPath()
	if err != nil {
		t.Fatal(err)
	}
	if pointer == directory {
		t.Fatalf("strategy pointer and generated directory both use %s", pointer)
	}
}

func TestStrategySetupRecordsTheVerifiedDestinationProof(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	strategyPath := filepath.Join(dir, "strategy.json")
	if err := writeStrategyFile(strategyPath, strategyFile{
		SizeSOL: "0.05", PrimaryTrustDomain: "provider-one", SecondaryTrustDomain: "provider-two",
		Sweep: strategyFileSweep{Enabled: true, To: "destination"},
	}); err != nil {
		t.Fatal(err)
	}
	sweepDir := filepath.Join(dir, "sweep")
	if err := os.MkdirAll(sweepDir, 0o700); err != nil {
		t.Fatal(err)
	}
	proof := destinationProof{
		Version: 1, Destination: "destination", Nonce: "nonce",
		IssuedAt: "2026-08-09T00:00:00Z", SignatureBase58: "signature",
	}
	raw, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sweepDir, "destination-proof.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordStrategyProof(strategyPath, sweepDir, 0, true); err != nil {
		t.Fatal(err)
	}
	got, err := readStrategyFile(strategyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sweep.ProofNonce != proof.Nonce || got.Sweep.ProofIssued != proof.IssuedAt ||
		got.Sweep.ProofSignature != proof.SignatureBase58 {
		t.Fatal("the editable strategy did not retain the verified destination proof")
	}
	if got.Sweep.ActivationDelay != "0s" {
		t.Fatalf("activation delay = %q, want 0s", got.Sweep.ActivationDelay)
	}
}

// An inverted pair must be refused with the reason, because it is also the
// pair that would let one price reading trigger both legs.
func TestStrategyPlanRefusesAnInvertedPair(t *testing.T) {
	_, err := planStrategy("0.05", "200", "250")
	if err == nil {
		t.Fatal("buy above sell was accepted")
	}
	if !strings.Contains(err.Error(), "both legs") {
		t.Errorf("the refusal did not name the double-trigger hazard: %v", err)
	}
}

// The reserved buy requirement must match what the profile will actually demand
// once the leg exists, or the sweep floor under-reserves and starves it.
func TestPlannedBuyRequirementMatchesTheProfileFormula(t *testing.T) {
	profile := testBuySwapProfile(t)
	profile.ReserveLamports = defaultSwapReserve
	profile.MaxFeeLamports = defaultSwapMaxFee
	if want := profile.WalletRequirementLamports(); plannedBuyRequirement() != want {
		t.Fatalf("planned buy requirement = %d, but the profile demands %d",
			plannedBuyRequirement(), want)
	}
}

// The oracle decides WHEN a leg may fire; the pool decides what it returns, and
// the two diverge — on Devnet by a factor of three. Sizing the buy from
// size x sell-price left it wanting devUSDC the sell never produced, so it
// could never be funded. It must be sized from what the sell GUARANTEES.
func TestTheBuyIsSizedFromWhatTheSellGuaranteesNotTheOraclePrice(t *testing.T) {
	profile := testSwapProfile(reserveOwner)
	profile.InputLamports = 50_000_000
	// A pool paying far less than the oracle threshold implies.
	profile.Route.MinOutputAmount = 1_047_255

	oracleSized, err := buyInputForSell(profile.InputLamports, 73_000_000)
	if err != nil {
		t.Fatal(err)
	}
	guaranteed := profile.MinimumOutput()
	if guaranteed >= oracleSized {
		t.Fatalf("fixture does not exercise the divergence: guaranteed %d, oracle-sized %d",
			guaranteed, oracleSized)
	}
	// Sizing on the oracle would demand more devUSDC than one sell can yield.
	if oracleSized <= guaranteed {
		t.Fatal("the oracle-sized buy should exceed what the sell guarantees")
	}
	// A buy sized at the guarantee is always fundable by exactly one sell.
	if guaranteed > profile.MinimumOutput() {
		t.Fatal("a buy sized at the guarantee cannot be funded by one sell")
	}
}

// The sweep runs its own MCP health observer. Omitting the command wrote a
// sweep whose runner died at startup — a config that looked complete and could
// never run, found only by starting it live.
func TestTheSweepArgsCarryTheMithrilCommand(t *testing.T) {
	// The wizard builds the sweep invocation as flags; the sweep's own flagset
	// is what consumes them, so the contract is that the flag is present.
	if !strings.Contains(strategySetupUsage, "--mithril-command") {
		t.Error("setup strategy does not document the command the sweep also needs")
	}
	source, err := os.ReadFile("strategy_setup.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), `"--mithril-command", *mithrilCommand,`) {
		t.Fatal("the sweep invocation no longer passes --mithril-command; its runner will not start")
	}
}

// The threshold is a floor on the FILL: swaprun refuses to sell below it
// however high the oracle reads (engine.go:1219-1236). A threshold the pool
// cannot reach configures a leg that sits "waiting" forever with its trigger
// satisfied — which reads as a broken agent, and is what a live run did.
func TestExecutablePriceMatchesWhatThePoolPays(t *testing.T) {
	// The real numbers from the live Devnet pool: 0.05 SOL yielded a guaranteed
	// 1.047255 devUSDC, which the engine reported as $20.9451.
	price, err := executablePriceFromQuote(50_000_000, 1_047_255)
	if err != nil {
		t.Fatal(err)
	}
	if price != 20_945_100 {
		t.Fatalf("executable price = %d micros, want 20945100 as the engine computed", price)
	}
	if _, err := executablePriceFromQuote(0, 1); err == nil {
		t.Error("a zero size was accepted")
	}
	if _, err := executablePriceFromQuote(1, math.MaxUint64); err == nil {
		t.Error("an unrepresentable quote was accepted")
	}
}

// 0 is a MEANINGFUL activation delay — the sweep reads it as "active
// immediately" — so a zero-value sentinel silently dropped the one value an
// operator would deliberately pass. Live setup printed a window two days out
// while being told to open it now.
func TestActivationDelayIsForwardedWhenGivenIncludingZero(t *testing.T) {
	source, err := os.ReadFile("strategy_setup.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if strings.Contains(text, `if *activationDelay != 0 {`) {
		t.Fatal("a zero activation delay is being treated as unset; --activation-delay 0 would be dropped")
	}
	if !strings.Contains(text, `given["activation-delay"]`) {
		t.Fatal("the forwarding no longer asks whether the flag was actually given")
	}
}

// One action per schedule window is the rate limit. Hardcoding an hour meant a
// strategy could act only once an hour and testing the cycle meant waiting one
// out — a limit the tool imposed, not one the operator chose.
func TestTheScheduleWindowIsChosenNotHardcoded(t *testing.T) {
	source, err := os.ReadFile("strategy_setup.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if strings.Contains(text, "scheduleWindow: time.Hour,") {
		t.Fatal("the sell leg's window is hardcoded again; the operator cannot choose the cadence")
	}
	if !strings.Contains(text, `flags.Duration("schedule-window"`) {
		t.Fatal("--schedule-window is no longer offered")
	}
}

// A resumed buy leg must keep the cadence the operator chose for the sell.
// Reverting it to an hour would give one strategy two different rate limits.
func TestTheResumedBuyLegInheritsTheSellsWindow(t *testing.T) {
	profile := testSwapProfile(reserveOwner)
	profile.ScheduleWindowSeconds = 120
	if got := scheduleWindowFromSellLeg(config{Swap: &profile}); got != 2*time.Minute {
		t.Fatalf("resumed window = %s, want the sell leg's 2m", got)
	}
	// A sell leg that records nothing must not produce a zero window, which the
	// profile would reject.
	if got := scheduleWindowFromSellLeg(config{}); got != time.Hour {
		t.Fatalf("fallback window = %s, want 1h", got)
	}
}

// Somebody who ran `strategy init` and `strategy edit` has already said
// everything a strategy needs. Making them then supply seven absolute host
// paths they have no way to know is where a non-technical operator stops.
func TestSetupStrategyAsksOnlyForWhatItCannotFindItself(t *testing.T) {
	source, err := os.ReadFile("strategy_setup.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	// Each of these is discovered from the host, exactly as the wizard does.
	for _, discovered := range []string{
		`detectInstalled("mithril-node", "mithril-mcp")`,
		`detectInstalled("node")`,
		`detectSourceAdapter()`,
	} {
		if !strings.Contains(text, discovered) {
			t.Errorf("setup strategy no longer discovers %s; the operator must type it", discovered)
		}
	}
	// Provider ownership must never be invented: two keys from one company are
	// not independent just because setup assigned them different labels.
	for _, invented := range []string{`"primary-provider"`, `"secondary-provider"`} {
		if strings.Contains(text, invented) {
			t.Errorf("setup still invents an evidence provider identity: %s", invented)
		}
	}
	// The floor is confirmed on screen, not fetched from a second command and
	// pasted back — that round trip is where a stale number gets pasted.
	if !strings.Contains(text, "confirmQuotedFloor(") {
		t.Error("the quote floor is no longer confirmed interactively")
	}
}

// One command must ask. Requiring somebody to discover `strategy init`, then
// `strategy edit`, then come back here is three commands to learn before
// anything happens — and the questions are identical either way.
func TestSetupStrategyAsksWhenNothingWasSupplied(t *testing.T) {
	source, err := os.ReadFile("strategy_setup.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "askForStrategyInline(") {
		t.Fatal("setup strategy no longer asks; the operator must find another command first")
	}
	// It must only take over when the operator supplied nothing AND a person is
	// there — otherwise it would hang a script or override explicit flags.
	for _, guard := range []string{
		"!*resume", `*fromFile == ""`, `!given["size-sol"]`, "stdinIsTerminal()",
	} {
		if !strings.Contains(text, guard) {
			t.Errorf("the ask is no longer guarded by %s; it could hang a script", guard)
		}
	}
	for _, want := range []string{
		"Run it with no strategy options.",
		"On a fresh host, pass --wallet-keypair and --mithril-command",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the help no longer states the guided fresh-host contract: missing %q", want)
		}
	}
}

// Re-running must not silently discard a destination proof somebody signed.
func TestAskingAgainStartsFromTheSavedAnswers(t *testing.T) {
	source, err := os.ReadFile("strategy_setup.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "Using your existing answers from") {
		t.Fatal("re-running no longer reuses saved answers; a signed proof would be lost")
	}
}

// Without a floor tolerance a strategy trades EXACTLY ONCE: the floor is pinned
// to the quote confirmed at setup, the trade itself moves the pool below it,
// and every later cycle reports price_below_floor forever. Observed live: one
// sell landed, then four consecutive failures.
func TestAStrategyCanTradeMoreThanOnce(t *testing.T) {
	source, err := os.ReadFile("strategy_setup.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, `flags.Uint("floor-tolerance-bps", 100,`) {
		t.Fatal("the floor tolerance is gone or defaults to zero; the strategy would trade once")
	}
	if !strings.Contains(text, "floorToleranceBPS: uint16(*floorToleranceBPS)") {
		t.Error("the sell leg no longer receives the tolerance")
	}
	if !strings.Contains(text, "options.floorToleranceBPS = floorToleranceBPS") {
		t.Error("the resumed buy leg no longer receives the tolerance")
	}
}

// The floor written into the policy is the RELAXED one, so the number the
// operator agrees to must be that same number. Showing the raw quote would have
// them agree to a figure the policy never receives, then be refused for not
// matching it — which is exactly what a live run did.
func TestTheConfirmedFloorIsTheOneThatGetsWritten(t *testing.T) {
	source, err := os.ReadFile("strategy_setup.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "relaxRouteFloor(route.MinOutputAmount, toleranceBPS)") {
		t.Fatal("the confirmed floor no longer matches the written floor")
	}
	// And the arithmetic itself: a 5% tolerance on 418553 must be what setup writes.
	relaxed, err := relaxRouteFloor(418_553, 500)
	if err != nil {
		t.Fatal(err)
	}
	if relaxed >= 418_553 || relaxed < 397_000 {
		t.Fatalf("relaxed floor = %d, want just under 5%% below 418553", relaxed)
	}
}

// A market-mode sell carries no price trigger, and the buy is sized from the
// sell's guaranteed minimum output rather than from any price — so demanding a
// trigger on resume blocked the one configuration that can complete a cycle on
// a pool the oracle disagrees with.
func TestResumeWorksForAMarketModeSell(t *testing.T) {
	source, err := os.ReadFile("strategy_setup.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if strings.Contains(text, "there is no sell price to size") {
		t.Fatal("resume still demands a price trigger; a market-mode strategy cannot be completed")
	}
	// Asserted by BEHAVIOUR, not by the spelling of the expression: this used to
	// grep for `(sellPrice == "") != (buyAtUSD == "")`, which broke the moment
	// the check moved into a function without changing what it does.
	if err := priceModeMismatch("", ""); err != nil {
		t.Errorf("a market-mode pair was rejected: %v", err)
	}
	if priceModeMismatch("", "19.50") == nil {
		t.Error("a market sell paired with a priced buy was accepted")
	}
	if priceModeMismatch("20.70", "") == nil {
		t.Error("a priced sell paired with a market buy was accepted")
	}
	// And the sizing must still come from the guarantee, not a price.
	if !strings.Contains(text, "options.inputTokenAmount = sellCfg.Swap.MinimumOutput()") {
		t.Error("the buy is no longer sized from what the sell guarantees")
	}
}

func TestResumeAppliesTheOneFileAlertsToTheBuyLeg(t *testing.T) {
	source, err := os.ReadFile("strategy_setup.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "writeLegAlerts(paths.buy, fileAlerts)") {
		t.Fatal("resuming the buy leg silently drops alerts from the strategy file")
	}
}

// The buy price is the one setting --resume cannot derive: it is a choice, and
// the strategy pointer records paths only. An operator who wrote buy_at_usd in
// their one config file was still refused later for not passing --buy-at-usd,
// by a command the documentation says takes no arguments.
func TestTheBuyPriceSurvivesUntilResumeNeedsIt(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := recordPlannedBuyPrice(directory, "18.50"); err != nil {
		t.Fatal(err)
	}
	if got := plannedBuyPrice(directory); got != "18.50" {
		t.Errorf("resume would ask for a price it was already given: %q", got)
	}
}

// A market-mode strategy has no buy price, and an empty note would make "no
// price" and "not recorded" indistinguishable. Nothing is written, and the
// absence means market — the same thing an absent flag means.
func TestMarketModeRecordsNoBuyPrice(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := recordPlannedBuyPrice(directory, ""); err != nil {
		t.Fatal(err)
	}
	if got := plannedBuyPrice(directory); got != "" {
		t.Errorf("market mode recorded a price: %q", got)
	}
	if _, err := os.Stat(filepath.Join(directory, plannedBuyPriceName)); !os.IsNotExist(err) {
		t.Errorf("an empty note was written: %v", err)
	}
}

func TestFailedStrategySetupRemovesOnlyItsIncompleteDirectories(t *testing.T) {
	root := t.TempDir()
	sell := filepath.Join(root, "sell")
	sweep := filepath.Join(root, "sweep")
	kept := filepath.Join(root, "complete")
	for _, directory := range []string{sell, sweep, kept} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	cleanupIncompleteStrategy(errors.New("late setup failure"), false, []string{sell, sweep})
	for _, directory := range []string{sell, sweep} {
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Errorf("incomplete directory %s was not removed: %v", directory, err)
		}
	}
	cleanupIncompleteStrategy(errors.New("output closed after success"), true, []string{kept})
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("a complete setup was removed after a reporting error: %v", err)
	}
}

// An unreadable note must degrade to "not recorded" rather than propagating an
// error: --resume already handles the absence by naming --buy-at-usd.
func TestAnUnreadableBuyPriceNoteIsTreatedAsAbsent(t *testing.T) {
	if got := plannedBuyPrice(filepath.Join(t.TempDir(), "nowhere")); got != "" {
		t.Errorf("a missing note produced a price: %q", got)
	}
}

// Re-running setup writes new legs with new, empty daily ledgers, because each
// leg's ledger lives inside the state directory setup creates fresh each time.
// The day's spend does not follow. Nothing said so, and the direction that
// matters is the counter-intuitive one: LOWERING a cap hands the operator a
// fresh full day at the new figure on top of what the old leg already spent.
func TestSetupWarnsThatNewLegsRestartTheSpendingDay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig(sell)
	if err != nil {
		t.Fatal(err)
	}
	warning := warnAboutRestartingTheSpendingDay(cfg.Swap.Owner())
	if warning == "" {
		t.Fatal("an existing leg on this wallet produced no warning")
	}
	if !strings.Contains(warning, "does NOT carry over") {
		t.Errorf("the warning does not say the day's spend is lost: %q", warning)
	}
	// The counter-intuitive direction is the whole point of warning at all.
	if !strings.Contains(warning, "LOWERING") || !strings.Contains(warning, "00:00 UTC") {
		t.Errorf("the warning does not cover tightening or when it takes effect: %q", warning)
	}
	if !strings.Contains(warning, sell) {
		t.Errorf("the warning does not name the existing leg: %q", warning)
	}
}

// A first setup has nothing to warn about, and a warning nobody needs is a
// warning everybody learns to skip.
func TestSetupIsSilentOnAFreshWallet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	cfg, err := readConfig(sell)
	if err != nil {
		t.Fatal(err)
	}
	if warning := warnAboutRestartingTheSpendingDay(cfg.Swap.Owner()); warning != "" {
		t.Errorf("a fresh wallet was warned: %q", warning)
	}
}
