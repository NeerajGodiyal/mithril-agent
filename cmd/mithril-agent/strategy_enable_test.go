package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/signer"
)

// triggeredLeg writes a swap config carrying a price trigger.
// The fixture legs fund different numbers of trades so a test can prove WHICH
// leg a refusal came from.
const (
	fixtureSellTradesPerDay = 6
	fixtureBuyTradesPerDay  = 4
)

func triggeredLeg(t *testing.T, dir string, buy bool, thresholdMicros uint64) string {
	t.Helper()
	// Both legs must share one wallet or the owner check fires first and hides
	// whatever the test is actually about — which it did, silently, until the
	// arming test failed and exposed it.
	profile := testBuySwapProfile(t)
	if !buy {
		profile = testSwapProfile(strategyTestOwner(t))
	}
	// The shared fixtures fund one trade a day, which is what shipped and what
	// made an unattended strategy stall after its first trade. A strategy leg
	// has to fund the trades it will be armed for, so size the caps here — and
	// the two legs fund DIFFERENT counts on purpose. With both at 4 and the sell
	// checked first, an arming test could never tell whether the buy leg was
	// examined at all — the sell refusal fired first and the assertion passed
	// either way.
	if buy {
		profile.DailyInputTokenCap = fixtureBuyTradesPerDay * profile.InputTokenAmount
		profile.DailyNativeFeeCapLamports = fixtureBuyTradesPerDay * profile.MaxFeeLamports
	} else {
		profile.DailyDebitCapLamports = fixtureSellTradesPerDay *
			(profile.InputLamports + profile.MaxFeeLamports +
				profile.Route.MaxOutputAccountRentLamports)
	}
	if thresholdMicros != 0 {
		direction := pricetrigger.SellAtOrAbove
		if buy {
			direction = pricetrigger.BuyAtOrBelow
		}
		profile.PriceTrigger = &pricetrigger.Policy{
			Version: pricetrigger.Version, Feed: pricetrigger.FeedSOLUSD,
			Direction: direction, ThresholdMicros: thresholdMicros,
			MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90,
			MaxDeviationBPS: 200, MaxConfidenceBPS: 200,
			PrimarySourceSHA256:   pricesource.PythPushIdentitySHA256(),
			SecondarySourceSHA256: pricesource.CoinbaseIdentitySHA256(),
		}
	}
	stateDir := filepath.Join(dir, stableStateDirName)
	if err := os.MkdirAll(filepath.Join(stateDir, signerStateDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, controlStateDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config{Swap: &profile}
	cfg.Quote.SocketPath = "/run/mithril-agent-quote/quote.sock"
	cfg.Evidence.PrimaryTrustDomain = "primary.test"
	cfg.Evidence.SecondaryTrustDomain = "secondary.test"
	cfg.Control.StatePath = filepath.Join(stateDir, controlStateDirName, "control.json")
	cfg.Signer.PolicyPath = filepath.Join(dir, "signer-policy.json")
	cfg.Signer.KeypairPath = filepath.Join(dir, "wallet-keypair.json")
	cfg.Policy.PolicyPath = filepath.Join(dir, "risk-policy.json")
	cfg.Policy.KeypairPath = filepath.Join(dir, "risk-keypair.json")
	cfg.Submitter.PolicyPath = filepath.Join(dir, "submitter-policy.json")
	cfg.Submitter.PrivateKeyPath = filepath.Join(dir, "submitter-key.json")
	writeJSON(t, cfg.Signer.PolicyPath, signer.Policy{
		AuthorizationLedgerPath: filepath.Join(stateDir, signerStateDirName, "authorizations.jsonl"),
	})
	if err := os.WriteFile(cfg.Signer.KeypairPath, []byte("[0]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Policy.PolicyPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Policy.KeypairPath, []byte("[0]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Submitter.PolicyPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Submitter.PrivateKeyPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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

// strategyTestOwner is the wallet the buy fixture pins, so a sell leg can be
// built on the same one.
func strategyTestOwner(t *testing.T) string {
	t.Helper()
	return testBuySwapProfile(t).Owner()
}

// captureEnables replaces the two real arming functions so the refusals can be
// tested without a live runner or a chain, and records what would have been armed.
func captureEnables(t *testing.T) *[]string {
	t.Helper()
	var armed []string
	previousSwap, previousSweep := enableSwapLeg, enableSweepLeg
	enableSwapLeg = func(args []string, _ io.Writer) error {
		armed = append(armed, "swap "+strings.Join(args, " "))
		return nil
	}
	enableSweepLeg = func(args []string, _ io.Writer) error {
		armed = append(armed, "sweep "+strings.Join(args, " "))
		return nil
	}
	t.Cleanup(func() { enableSwapLeg, enableSweepLeg = previousSwap, previousSweep })
	return &armed
}

// Overlapping thresholds are the one combination a single price reading could
// satisfy on both sides, putting two transactions in flight against one balance.
// It must be refused BEFORE anything is armed, not reported afterwards.
func TestStrategyEnableRefusesOverlappingThresholds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 180_000_000)
	buy := triggeredLeg(t, t.TempDir(), true, 220_000_000)
	if err := recordStrategy(strategyPaths{sell: sell, buy: buy}); err != nil {
		t.Fatal(err)
	}
	armed := captureEnables(t)
	err := strategyEnable([]string{"--duration", "1h", "--reason", "overlap"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("a buy threshold above the sell threshold was accepted")
	}
	if len(*armed) != 0 {
		t.Fatalf("legs were armed before the refusal: %v", *armed)
	}
}

// Non-overlapping thresholds are the ordinary round trip and must arm both legs.
func TestStrategyEnableArmsBothLegsWhenThresholdsCannotOverlap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 250_000_000)
	buy := triggeredLeg(t, t.TempDir(), true, 200_000_000)
	if err := recordStrategy(strategyPaths{sell: sell, buy: buy}); err != nil {
		t.Fatal(err)
	}
	armed := captureEnables(t)
	var output bytes.Buffer
	if err := strategyEnable(
		[]string{"--duration", "12h", "--max-trades", "4", "--reason", "round trip"}, &output,
	); err != nil {
		t.Fatal(err)
	}
	if len(*armed) != 2 {
		t.Fatalf("armed %v, want both legs", *armed)
	}
	// The printed line comes from the flag, not from what was passed onward, so
	// asserting on it alone left the bounds that limit spending authority
	// completely untested: raising --max-actions to 100 in the call kept every
	// assertion green.
	for _, entry := range *armed {
		for _, required := range []string{
			"--duration 12h0m0s", "--max-actions 4", "--reason round trip",
		} {
			if !strings.Contains(entry, required) {
				t.Errorf("the arming call lost %q: %s", required, entry)
			}
		}
	}
}

// A sweep is armed through a different function than a swap leg. Nothing
// exercised that routing: deleting it and sending everything to the swap path
// kept the suite green, while a real sweep could never be armed at all.
func TestStrategyEnableRoutesTheSweepToItsOwnArming(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	profile := testSweepProfileForStrategy(strategyTestOwner(t), otherOwner,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC).Unix())
	cfg := config{Profile: profile}
	cfg.Control.StatePath = filepath.Join(dir, "control.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sweep := filepath.Join(dir, "config.json")
	if err := os.WriteFile(sweep, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordStrategy(strategyPaths{
		sell: triggeredLeg(t, t.TempDir(), false, 250_000_000), sweep: sweep,
	}); err != nil {
		t.Fatal(err)
	}
	armed := captureEnables(t)
	if err := strategyEnable(
		[]string{"--duration", "1h", "--reason", "sweep routing"}, &bytes.Buffer{},
	); err != nil {
		t.Fatal(err)
	}
	var sweepArmed bool
	for _, entry := range *armed {
		if strings.HasPrefix(entry, "sweep ") {
			sweepArmed = true
		}
	}
	if !sweepArmed {
		t.Fatalf("the sweep was not armed through its own path: %v", *armed)
	}
}

// Every leg skipped is not success: scripts chaining on `strategy enable`
// saw exit 0 having armed nothing.
func TestStrategyEnableFailsWhenEveryLegIsSkipped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	profile := testSweepProfileForStrategy(reserveOwner, otherOwner,
		time.Now().UTC().Add(48*time.Hour).Unix())
	cfg := config{Profile: profile}
	cfg.Control.StatePath = filepath.Join(dir, "control.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sweep := filepath.Join(dir, "config.json")
	if err := os.WriteFile(sweep, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordStrategy(strategyPaths{sweep: sweep}); err != nil {
		t.Fatal(err)
	}
	captureEnables(t)
	if err := strategyEnable(
		[]string{"--duration", "12h", "--reason", "all skipped"}, &bytes.Buffer{},
	); err == nil {
		t.Fatal("arming nothing at all reported success")
	}
}

// An unreadable recorded leg is a preflight failure for the WHOLE strategy.
// Arming the readable legs and then returning an error leaves a partial live
// strategy behind even though the command itself failed.
func TestStrategyEnableRefusesBeforeArmingWhenALegIsUnreadable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 250_000_000)
	buy := triggeredLeg(t, t.TempDir(), true, 200_000_000)
	if err := recordStrategy(strategyPaths{sell: sell, buy: buy}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(buy); err != nil {
		t.Fatal(err)
	}
	armed := captureEnables(t)
	var output bytes.Buffer
	err := strategyEnable(
		[]string{"--duration", "1h", "--reason", "missing leg"}, &output,
	)
	if err == nil || !strings.Contains(err.Error(), "nothing was armed") {
		t.Fatalf("error = %v, want a whole-strategy refusal", err)
	}
	if len(*armed) != 0 {
		t.Fatalf("readable legs were armed before the refusal: %v", *armed)
	}
	if !strings.Contains(output.String(), "CANNOT BE READ") {
		t.Fatalf("the missing leg was not explained:\n%s", output.String())
	}
}

// A leg with no price condition fires at whatever the market is. That is a real
// choice for one hand-run trade, but not one to make for a whole strategy in a
// single command.
func TestStrategyEnableRefusesATriggerlessLeg(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	armed := captureEnables(t)
	err := strategyEnable([]string{"--duration", "1h", "--reason", "no trigger"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "no price trigger") {
		t.Fatalf("error = %v, want a complaint about the missing trigger", err)
	}
	if len(*armed) != 0 {
		t.Fatalf("a trigger-less leg was armed: %v", *armed)
	}
}

// Legs on different wallets are not one strategy, and arming them together
// would hide that from whoever typed one command.
func TestStrategyEnableRefusesLegsOnDifferentWallets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 250_000_000)
	other := testSwapProfile(otherOwner)
	other.PriceTrigger = &pricetrigger.Policy{
		Version: pricetrigger.Version, Feed: pricetrigger.FeedSOLUSD,
		Direction: pricetrigger.BuyAtOrBelow, ThresholdMicros: 200_000_000,
		MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90,
		MaxDeviationBPS: 200, MaxConfidenceBPS: 200,
		PrimarySourceSHA256:   pricesource.PythPushIdentitySHA256(),
		SecondarySourceSHA256: pricesource.CoinbaseIdentitySHA256(),
	}
	dir := t.TempDir()
	cfg := config{Swap: &other}
	cfg.Evidence.PrimaryTrustDomain = "primary.test"
	cfg.Evidence.SecondaryTrustDomain = "secondary.test"
	cfg.Control.StatePath = filepath.Join(dir, "control.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(dir, "config.json")
	if err := os.WriteFile(foreign, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordStrategy(strategyPaths{sell: sell, buy: foreign}); err != nil {
		t.Fatal(err)
	}
	armed := captureEnables(t)
	if err := strategyEnable(
		[]string{"--duration", "1h", "--reason", "two wallets"}, &bytes.Buffer{},
	); err == nil {
		t.Fatal("legs on two wallets were armed as one strategy")
	}
	if len(*armed) != 0 {
		t.Fatalf("legs were armed despite the mismatch: %v", *armed)
	}
}

// A sweep whose first window opens later would burn a <=24h grant on a window
// that has not arrived, leaving a dead grant and a runner reporting failure.
func TestStrategyEnableSkipsASweepBeforeItsFirstWindow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	profile := testSweepProfileForStrategy(reserveOwner, otherOwner,
		time.Now().UTC().Add(48*time.Hour).Unix())
	cfg := config{Profile: profile}
	cfg.Control.StatePath = filepath.Join(dir, "control.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sweep := filepath.Join(dir, "config.json")
	if err := os.WriteFile(sweep, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordStrategy(strategyPaths{sweep: sweep}); err != nil {
		t.Fatal(err)
	}
	armed := captureEnables(t)
	var output bytes.Buffer
	// Skipping every leg is now a failure — see the test below — so this one
	// asserts WHAT it said, not that it succeeded.
	_ = strategyEnable([]string{"--duration", "12h", "--reason", "early sweep"}, &output)
	if len(*armed) != 0 {
		t.Fatalf("a sweep was armed before its window opened: %v", *armed)
	}
	if !strings.Contains(output.String(), "skipped") {
		t.Errorf("the skip was not explained:\n%s", output.String())
	}
}

// On a pool whose price disagrees with the oracle — every Devnet pool — NO
// threshold pair can satisfy both legs: the sell needs sellAt <= what the pool
// pays (~$20.92) and the buy needs buyAt >= what the oracle reads (~$73.35),
// while enable requires buyAt < sellAt. Market-price legs are the only shape
// that can complete a cycle, so they must be armable — explicitly.
func TestMarketPriceLegsArmOnlyWhenExplicitlyAllowed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 0) // no trigger
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}

	armed := captureEnables(t)
	if err := strategyEnable(
		[]string{"--duration", "1h", "--reason", "no opt-in"}, &bytes.Buffer{},
	); err == nil {
		t.Fatal("a trigger-less leg was armed without --allow-any-price")
	}
	if len(*armed) != 0 {
		t.Fatalf("armed without the opt-in: %v", *armed)
	}

	var output bytes.Buffer
	if err := strategyEnable([]string{
		"--duration", "1h", "--allow-any-price", "--reason", "market orders",
	}, &output); err != nil {
		t.Fatalf("the explicit opt-in was still refused: %v", err)
	}
	if len(*armed) != 1 {
		t.Fatalf("armed %v, want the leg", *armed)
	}
	// The operator must be told, every time, what they just allowed.
	if !strings.Contains(output.String(), "NO PRICE CONDITION") {
		t.Errorf("arming at market was not called out:\n%s", output.String())
	}
}
