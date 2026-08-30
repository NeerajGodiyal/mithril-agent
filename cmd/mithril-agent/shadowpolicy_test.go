package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func generatedPolicyPath(t *testing.T, args ...string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "policy.json")
	full := append([]string{"--out", out,
		"--observe", "So11111111111111111111111111111111111111112"}, args...)
	if err := runShadowPolicy(full, &bytes.Buffer{}); err != nil {
		t.Fatalf("generating a policy failed: %v", err)
	}
	return out
}

// The whole point: a generated policy must load, with no hand-editing. The two
// source digests are the fields nobody can produce by hand, and a policy naming
// the wrong source measures something other than what its author believes.
func TestAGeneratedPolicyLoadsWithoutEditing(t *testing.T) {
	path := generatedPolicyPath(t, "--sell-at-usd", "80.00")

	policy, err := loadShadowPolicy(path)
	if err != nil {
		t.Fatalf("a generated policy did not load: %v", err)
	}
	if policy.Trigger.PrimarySourceSHA256 != pricesource.PythPushIdentitySHA256() ||
		policy.Trigger.SecondarySourceSHA256 != pricesource.KrakenSOLIdentitySHA256() {
		t.Fatal("the generated policy does not name the sources the runner uses")
	}
	if policy.QuotePeg == nil ||
		policy.QuotePeg.PrimarySourceSHA256 != pricesource.PythPushUSDCIdentitySHA256() ||
		policy.QuotePeg.SecondarySourceSHA256 != pricesource.KrakenIdentitySHA256() {
		t.Fatal("the generated Mainnet policy does not bind its independent USDC/USD sources")
	}
	if policy.Trigger.ThresholdMicros != 80_000_000 {
		t.Errorf("threshold = %d micros, want 80000000", policy.Trigger.ThresholdMicros)
	}
	if policy.Trigger.Direction != pricetrigger.SellAtOrAbove {
		t.Errorf("direction = %q, want a sell", policy.Trigger.Direction)
	}
	if policy.QuoteRoute.Provider != "jupiter" ||
		policy.QuoteRoute.InputMint != "So11111111111111111111111111111111111111112" ||
		policy.QuoteRoute.OutputMint != mainnetUSDCMint {
		t.Fatalf("generated policy does not bind its Jupiter SOL/USDC route: %+v", policy.QuoteRoute)
	}
}

func TestGeneratedAdaptivePolicyUsesRelativeMarketDecisions(t *testing.T) {
	path := generatedPolicyPath(t, "--adaptive", "--slippage-bps", "500")
	policy, err := loadShadowPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Adaptive == nil || !policy.RoundTrip() {
		t.Fatalf("generated adaptive policy has no adaptive round trip: %+v", policy)
	}
	if policy.Adaptive.MinimumSignalBPS != 1_110 ||
		policy.Adaptive.MaxVolatilityBPS <= policy.Adaptive.MinimumSignalBPS {
		t.Fatalf("adaptive defaults do not cover configured execution cost: %+v", policy.Adaptive)
	}
	if policy.Trigger.ThresholdMicros <= 100_000_000 ||
		policy.ReturnTrigger.ThresholdMicros >= 100_000_000 {
		t.Fatal("adaptive feed bindings could accidentally act as a normal SOL price threshold")
	}
}

func TestGeneratedAdaptiveMandateMapsToExistingPolicy(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "policy.json")
	var output bytes.Buffer
	if err := runShadowPolicy([]string{
		"--out", path,
		"--observe", "So11111111111111111111111111111111111111112",
		"--adaptive", "--market", "SOL/USDC", "--budget-sol", "0.25",
		"--drawdown-stop-bps", "250",
	}, &output); err != nil {
		t.Fatal(err)
	}
	policy, err := loadShadowPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Adaptive == nil || policy.Adaptive.MaxDrawdownBPS != 250 ||
		policy.StartingInputUnits != 250_000_000-defaultPaperMandateReserve ||
		policy.InputAmount != 250_000_000-defaultPaperMandateReserve ||
		policy.StartingFeeReserveLamports != defaultPaperMandateReserve ||
		policy.OneTimeSetupRentLamports != defaultJUPSetupRentLamports ||
		policy.QuoteRoute != shadow.MainnetQuoteRoute(true) {
		t.Fatalf("paper mandate policy = %+v", policy)
	}
	text := output.String()
	for _, want := range []string{
		"Paper mandate: SOL/USDC · budget 0.25 SOL · setup locks 0.003 SOL · daily drawdown stop 2.5%",
		"resets at 00:00 UTC", "not a guaranteed maximum loss", "cannot sign",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("mandate output omits %q:\n%s", want, text)
		}
	}
}

func TestGeneratedJUPMandateBindsMarketAndIndependentSOLFees(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "jup-policy.json")
	var output bytes.Buffer
	if err := runShadowPolicy([]string{
		"--out", path,
		"--observe", "So11111111111111111111111111111111111111112",
		"--adaptive", "--market", "JUP/USDC", "--budget-usdc", "250",
		"--fee-reserve-sol", "0.004", "--drawdown-stop-bps", "300",
	}, &output); err != nil {
		t.Fatal(err)
	}
	policy, err := loadShadowPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Market != shadow.MarketJUPUSDC || policy.Adaptive == nil ||
		policy.Adaptive.MaxDrawdownBPS != 300 || policy.InputAmount != 250_000_000 ||
		policy.StartingInputUnits != 250_000_000 || policy.StartingOutputUnits != 0 ||
		policy.StartingFeeReserveLamports != 4_000_000 ||
		policy.OneTimeSetupRentLamports != 3_000_000 ||
		policy.InputDecimals != 6 || policy.OutputDecimals != 6 ||
		policy.QuoteRoute != shadow.MainnetMarketQuoteRoute(shadow.MarketJUPUSDC, false) {
		t.Fatalf("JUP mandate policy = %+v", policy)
	}
	if policy.Trigger.Version != pricetrigger.MultiFeedVersion ||
		policy.Trigger.Feed != pricetrigger.FeedJUPUSD ||
		policy.Trigger.PrimarySourceSHA256 != pricesource.PythPushJUPIdentitySHA256() ||
		policy.Trigger.SecondarySourceSHA256 != pricesource.KrakenJUPIdentitySHA256() ||
		policy.NativeFeePrice == nil || policy.NativeFeePrice.Feed != pricetrigger.FeedSOLUSD ||
		policy.NativeFeePrice.PrimarySourceSHA256 != pricesource.PythPushIdentitySHA256() ||
		policy.NativeFeePrice.SecondarySourceSHA256 != pricesource.KrakenSOLIdentitySHA256() {
		t.Fatalf("JUP evidence bindings = %+v / %+v", policy.Trigger, policy.NativeFeePrice)
	}
	for _, want := range []string{
		"Paper mandate: JUP/USDC", "budget 250 USDC", "native reserve 0.004 SOL",
		"setup locks 0.003 SOL", "cannot sign",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("JUP mandate output omits %q:\n%s", want, output.String())
		}
	}
}

// Buying is the mirror image and the decimals have to swap with it, or every
// price the report computes is out by a factor of a thousand.
func TestABuyPolicySwapsTheDecimals(t *testing.T) {
	policy, err := loadShadowPolicy(generatedPolicyPath(t, "--buy-at-usd", "50.00"))
	if err != nil {
		t.Fatal(err)
	}
	if policy.Trigger.Direction != pricetrigger.BuyAtOrBelow {
		t.Fatalf("direction = %q, want a buy", policy.Trigger.Direction)
	}
	if policy.InputDecimals != 6 || policy.OutputDecimals != 9 {
		t.Errorf("decimals = %d/%d, want 6/9 for a buy",
			policy.InputDecimals, policy.OutputDecimals)
	}
	if policy.StartingInputUnits < policy.InputAmount ||
		policy.StartingFeeReserveLamports < policy.FeeLamports {
		t.Fatalf("buy policy cannot fund its notional trade and fee: %+v", policy)
	}
}

func TestGeneratedSellPolicyFundsTheConfiguredNotional(t *testing.T) {
	const amount = "2000000000"
	policy, err := loadShadowPolicy(generatedPolicyPath(t,
		"--sell-at-usd", "80.00", "--amount", amount))
	if err != nil {
		t.Fatal(err)
	}
	if policy.StartingInputUnits < policy.InputAmount ||
		policy.StartingFeeReserveLamports < policy.FeeLamports {
		t.Fatalf("generated sell policy cannot fund its trade and fee: %+v", policy)
	}
}

func TestGeneratedRoundTripPreservesBothNativeFees(t *testing.T) {
	const amount = "2000000000"
	policy, err := loadShadowPolicy(generatedPolicyPath(t,
		"--sell-at-usd", "80.00", "--buy-at-usd", "50.00", "--amount", amount))
	if err != nil {
		t.Fatal(err)
	}
	if reserve := policy.StartingFeeReserveLamports; reserve < 2*policy.FeeLamports {
		t.Fatalf("generated round trip reserved %d lamports, want at least %d",
			reserve, 2*policy.FeeLamports)
	}
	ledger, err := shadow.NewLedger(policy, 80_000_000)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ledger.Apply(shadow.Fill{
		Sell: true, Refusal: "slippage", FeeLamports: policy.FeeLamports,
	}, 80_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if after.FeeReserveLamports < 2*policy.FeeLamports {
		t.Fatalf("one refusal stranded the unchanged round trip: %+v", after)
	}
}

func TestGeneratedPolicyAcceptsConservativeExecutionCosts(t *testing.T) {
	policy, err := loadShadowPolicy(generatedPolicyPath(t,
		"--sell-at-usd", "80.00", "--slippage-bps", "125", "--fee-lamports", "20000"))
	if err != nil {
		t.Fatal(err)
	}
	if policy.SlippageBPS != 125 || policy.FeeLamports != 20_000 {
		t.Fatalf("generated execution costs = %d/%d", policy.SlippageBPS, policy.FeeLamports)
	}
}

// The generated file holds no secret, but it is the operator's configuration
// and is written through the same private-file path as everything else here.
func TestAGeneratedPolicyIsWrittenPrivately(t *testing.T) {
	info, err := os.Stat(generatedPolicyPath(t, "--sell-at-usd", "80.00"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("policy mode = %o, want no group or other access", perm)
	}
}

// Ambiguous or impossible instructions must be refused here, not discovered at
// the start of a run somebody expected to leave going for days.
func TestPolicyGeneratorRefusesAmbiguousInstructions(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "p.json")
	observe := "So11111111111111111111111111111111111111112"
	for name, args := range map[string][]string{
		"neither direction":  {"--out", out, "--observe", observe},
		"no observe address": {"--out", out, "--sell-at-usd", "80"},
		"relative out":       {"--out", "policy.json", "--observe", observe, "--sell-at-usd", "80"},
		"no output path":     {"--observe", observe, "--sell-at-usd", "80"},
		"unparseable price": {"--out", out, "--observe", observe,
			"--sell-at-usd", "not-a-price"},
		"unfundable amount": {"--out", out, "--observe", observe,
			"--sell-at-usd", "80", "--amount", "18446744073709551615"},
		"wrapped slippage": {"--out", out, "--observe", observe,
			"--sell-at-usd", "80", "--slippage-bps", "65537"},
		"zero fee": {"--out", out, "--observe", observe,
			"--sell-at-usd", "80", "--fee-lamports", "0"},
		"Devnet route omitted": {"--out", out, "--observe", observe,
			"--cluster", "devnet", "--sell-at-usd", "80"},
		"Mainnet route override": {"--out", out, "--observe", observe,
			"--sell-at-usd", "80", "--pool", observe,
			"--input-mint", observe, "--output-mint", mainnetUSDCMint},
		"adaptive fixed price": {"--out", out, "--observe", observe,
			"--adaptive", "--sell-at-usd", "80"},
		"unsupported mandate market": {"--out", out, "--observe", observe,
			"--adaptive", "--market", "BTC/USDC", "--budget-sol", "1",
			"--drawdown-stop-bps", "300"},
		"mandate plus raw amount": {"--out", out, "--observe", observe,
			"--adaptive", "--market", "SOL/USDC", "--budget-sol", "1",
			"--drawdown-stop-bps", "300", "--amount", "1"},
		"invalid mandate budget": {"--out", out, "--observe", observe,
			"--adaptive", "--market", "SOL/USDC", "--budget-sol", "nope",
			"--drawdown-stop-bps", "300"},
		"fixed mandate": {"--out", out, "--observe", observe,
			"--sell-at-usd", "80", "--market", "SOL/USDC", "--budget-sol", "1",
			"--drawdown-stop-bps", "300"},
		"unbounded mandate drawdown": {"--out", out, "--observe", observe,
			"--adaptive", "--market", "SOL/USDC", "--budget-sol", "1",
			"--drawdown-stop-bps", "5001"},
		"incomplete mandate": {"--out", out, "--observe", observe,
			"--adaptive", "--market", "SOL/USDC"},
		"JUP with SOL budget": {"--out", out, "--observe", observe,
			"--adaptive", "--market", "JUP/USDC", "--budget-sol", "1",
			"--drawdown-stop-bps", "300"},
		"JUP fee reserve too small": {"--out", out, "--observe", observe,
			"--adaptive", "--market", "JUP/USDC", "--budget-usdc", "100",
			"--fee-reserve-sol", "0.000001", "--drawdown-stop-bps", "300"},
	} {
		if err := runShadowPolicy(args, &bytes.Buffer{}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestPolicyGeneratorBuildsAContinuousRoundTrip(t *testing.T) {
	policy, err := loadShadowPolicy(generatedPolicyPath(t,
		"--sell-at-usd", "80.00", "--buy-at-usd", "60.00"))
	if err != nil {
		t.Fatal(err)
	}
	if !policy.RoundTrip() || policy.ReturnTrigger == nil {
		t.Fatal("giving both thresholds did not create a round-trip policy")
	}
	if policy.Trigger.Direction != pricetrigger.SellAtOrAbove ||
		policy.Trigger.ThresholdMicros != 80_000_000 ||
		policy.ReturnTrigger.Direction != pricetrigger.BuyAtOrBelow ||
		policy.ReturnTrigger.ThresholdMicros != 60_000_000 {
		t.Fatalf("generated round trip has the wrong legs: %+v / %+v",
			policy.Trigger, *policy.ReturnTrigger)
	}
}

// The printed next step must be the command that actually runs it, and must
// repeat that nothing can be signed.
func TestPolicyGeneratorPrintsTheNextCommand(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runShadowPolicy([]string{
		"--out", filepath.Join(root, "policy.json"),
		"--observe", "So11111111111111111111111111111111111111112",
		"--sell-at-usd", "80.00",
	}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, command := range []string{"shadow run --policy", "shadow review --policy"} {
		if !strings.Contains(text, command) {
			t.Errorf("the output does not name %q:\n%s", command, text)
		}
	}
	if !strings.Contains(text, "cannot sign") {
		t.Errorf("the output does not repeat that it cannot sign:\n%s", text)
	}
	if strings.Contains(text, "--input-mint") || !strings.Contains(text, "shadow run --policy") {
		t.Errorf("the next command repeats route settings instead of reading the policy:\n%s", text)
	}
}

func TestPolicyGeneratorPrintsDirectionAndClusterSpecificCommands(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	observe := "So11111111111111111111111111111111111111112"

	var buy bytes.Buffer
	if err := runShadowPolicy([]string{
		"--out", filepath.Join(root, "buy.json"), "--observe", observe,
		"--buy-at-usd", "50.00",
	}, &buy); err != nil {
		t.Fatal(err)
	}
	buyPolicy, err := loadShadowPolicy(filepath.Join(root, "buy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if buyPolicy.QuoteRoute.InputMint != mainnetUSDCMint ||
		buyPolicy.QuoteRoute.OutputMint != observe {
		t.Errorf("the buy policy does not reverse the supported pair: %+v", buyPolicy.QuoteRoute)
	}

	var devnet bytes.Buffer
	if err := runShadowPolicy([]string{
		"--out", filepath.Join(root, "devnet.json"), "--observe", observe,
		"--cluster", "devnet", "--sell-at-usd", "80.00",
		"--pool", "11111111111111111111111111111111",
		"--input-mint", observe, "--output-mint", mainnetUSDCMint,
	}, &devnet); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(devnet.String(), "jupiter") ||
		!strings.Contains(devnet.String(), "--node-command") {
		t.Errorf("the Devnet policy printed a Mainnet-only command:\n%s", devnet.String())
	}
	devnetPolicy, err := loadShadowPolicy(filepath.Join(root, "devnet.json"))
	if err != nil {
		t.Fatal(err)
	}
	if devnetPolicy.QuotePeg != nil {
		t.Fatal("Devnet policy claimed the test quote token had USDC/USD evidence")
	}
	if devnetPolicy.QuoteRoute.Provider != "orca" ||
		devnetPolicy.QuoteRoute.Pool != "11111111111111111111111111111111" {
		t.Fatalf("Devnet policy did not bind its Orca route: %+v", devnetPolicy.QuoteRoute)
	}
}
