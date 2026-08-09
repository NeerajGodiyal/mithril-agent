package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/submitter"
)

func TestSwapSetupCreatesBoundPrivateConfiguration(t *testing.T) {
	fixture := newSwapSetupFixture(t)
	wantRoute := testSwapProfile(fixture.owner).Route
	wantRoute.MinOutputAmount = 21_525
	installSwapSetupTestHooks(t, fixture.agentCommand, func(
		_ context.Context,
		owner, nodeCommand, quoteScript string,
		inputLamports uint64,
		slippageBPS uint16,
	) (orcaswap.Policy, error) {
		if owner != fixture.owner || nodeCommand != fixture.nodeCommand ||
			quoteScript != fixture.quoteScript || inputLamports != 1_000_000 ||
			slippageBPS != 100 {
			t.Fatalf("discovery input = %q %q %q %d %d", owner, nodeCommand, quoteScript, inputLamports, slippageBPS)
		}
		return wantRoute, nil
	})

	var output bytes.Buffer
	err := runContext(t.Context(), []string{
		"swap", "setup",
		"--dir", fixture.setupDirectory,
		"--wallet-keypair", fixture.walletKeypair,
		"--mithril-command", fixture.mithrilCommand,
		"--mithril-config", fixture.mithrilConfig,
		"--node-command", fixture.nodeCommand,
		"--quote-script", fixture.quoteScript,
		"--confirm-min-output-amount", "21525",
		"--primary-trust-domain", "provider-one",
		"--secondary-trust-domain", "provider-two",
		"--sell-at-usd", "20.50",
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	var result swapSetupResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	paths := newSwapSetupPaths(fixture.setupDirectory)
	if result.Status != "configured" || result.ConfigPath != paths.config ||
		result.InputLamports != 1_000_000 || result.MinimumOutput != wantRoute.MinOutputAmount ||
		!slices.Equal(result.PlanArgv, []string{
			fixture.agentCommand, "swap", "plan", "--config", paths.config,
		}) ||
		!slices.Equal(result.PreflightArgv, []string{
			fixture.agentCommand, "preflight", "--config", paths.config,
		}) || !slices.Equal(result.LiveCheckArgv, []string{
		fixture.agentCommand, "swap", "check", "--config", paths.config,
	}) || !slices.Equal(result.DemoArgv, []string{
		fixture.agentCommand, "swap", "demo", "--config", paths.config,
	}) {
		t.Fatalf("setup result = %+v", result)
	}

	assertMode(t, fixture.setupDirectory, 0o700)
	assertMode(t, filepath.Join(fixture.setupDirectory, "state"), 0o700)
	for _, path := range []string{
		paths.config, paths.signerPolicy, paths.riskPolicy, paths.riskKeypair,
		paths.submitterPolicy, paths.submitterKey,
	} {
		assertMode(t, path, 0o600)
	}

	var cfg config
	if err := readStrictJSON(paths.config, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Swap == nil || cfg.Swap.Route != wantRoute || cfg.Swap.PriceTrigger == nil ||
		cfg.Swap.PriceTrigger.Direction != pricetrigger.SellAtOrAbove ||
		cfg.Swap.PriceTrigger.ThresholdMicros != 20_500_000 ||
		cfg.Swap.PriceTrigger.PrimarySourceSHA256 != pricesource.PythPushIdentitySHA256() ||
		cfg.Swap.PriceTrigger.SecondarySourceSHA256 != pricesource.CoinbaseIdentitySHA256() ||
		cfg.Signer.KeypairPath != fixture.walletKeypair ||
		cfg.MCP.Command != fixture.mithrilCommand ||
		!slices.Equal(cfg.MCP.Args, []string{"mcp", "--config", fixture.mithrilConfig, "--profile", "monitor"}) ||
		cfg.Quote.Command != fixture.nodeCommand || cfg.Quote.ScriptPath != fixture.quoteScript ||
		cfg.Evidence.PrimaryTrustDomain != "provider-one" ||
		cfg.Evidence.SecondaryTrustDomain != "provider-two" ||
		cfg.Evidence.PrimaryOriginSHA256 != testRPCIdentity(
			t, "https://rpc-one.invalid/path?api-key=secret-one",
		) || cfg.Evidence.SecondaryOriginSHA256 != testRPCIdentity(
		t, "https://rpc-two.invalid/path?api-key=secret-two",
	) {
		t.Fatalf("setup config = %+v", cfg)
	}
	fingerprint, err := cfg.Swap.Fingerprint()
	if err != nil || fingerprint != result.ProfileSHA256 {
		t.Fatalf("fingerprint = %q, %v", fingerprint, err)
	}

	t.Setenv("MITHRIL_AGENT_MITHRIL_RPC_URL", "http://127.0.0.1:8899")
	t.Setenv("MITHRIL_AGENT_QUOTE_RPC_URL", "https://quotes.invalid/path?api-key=secret")
	beforePlan := snapshotSetupTree(t, fixture.setupDirectory)
	var planOutput bytes.Buffer
	if err := runContext(t.Context(), []string{
		"swap", "plan", "--config", paths.config,
	}, &planOutput); err != nil {
		t.Fatal(err)
	}
	afterPlan := snapshotSetupTree(t, fixture.setupDirectory)
	if !reflect.DeepEqual(beforePlan, afterPlan) {
		t.Fatal("swap plan changed the setup tree")
	}
	var plan swapPlan
	if err := json.Unmarshal(planOutput.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Version != swapPlanVersion || plan.Status != "configured" || len(plan.Steps) != 9 {
		t.Fatalf("swap plan = %+v", plan)
	}
	for _, secret := range []string{"secret-one", "secret-two", "quotes.invalid", fixture.owner} {
		if strings.Contains(planOutput.String(), secret) {
			t.Fatalf("swap plan exposed %q", secret)
		}
	}

	var signerPolicy signer.Policy
	var riskPolicy policyauthority.Policy
	var submitterPolicy submitter.Policy
	if err := readStrictJSON(paths.signerPolicy, &signerPolicy); err != nil {
		t.Fatal(err)
	}
	if err := readStrictJSON(paths.riskPolicy, &riskPolicy); err != nil {
		t.Fatal(err)
	}
	if err := readStrictJSON(paths.submitterPolicy, &submitterPolicy); err != nil {
		t.Fatal(err)
	}
	if signerPolicy.ProfileFingerprint != fingerprint ||
		signerPolicy.Source != fixture.owner || signerPolicy.OrcaSwap == nil ||
		*signerPolicy.OrcaSwap != wantRoute ||
		!signerPoliciesEqual(riskPolicy.TransactionPolicy, signerPolicy) ||
		!submitterPolicyMatchesSigner(submitterPolicy, signerPolicy) {
		t.Fatalf("setup policies are not mutually bound")
	}
	riskPrivate, err := signer.LoadKeypair(paths.riskKeypair)
	if err != nil {
		t.Fatal(err)
	}
	riskPublic, err := riskgrant.PublicKeyHex(riskPrivate)
	if err != nil || riskPublic != signerPolicy.RiskAuthorityPublicKey {
		t.Fatalf("risk key binding = %q, %v", riskPublic, err)
	}
	submitterPrivate, err := submitter.LoadPrivateKey(paths.submitterKey)
	if err != nil {
		t.Fatal(err)
	}
	submitterPublic, err := sealedtx.PublicKey(submitterPrivate)
	if err != nil || submitterPublic != signerPolicy.SubmitterPublicKey {
		t.Fatalf("submitter key binding = %q, %v", submitterPublic, err)
	}
	configData, err := os.ReadFile(paths.config)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(configData, []byte("https://")) ||
		bytes.Contains(configData, []byte("api-key")) ||
		bytes.Contains(configData, []byte("rpc-one.invalid")) ||
		bytes.Contains(configData, []byte("secret-one")) {
		t.Fatal("setup config contains an RPC endpoint or credential")
	}

	err = runContext(t.Context(), []string{
		"swap", "setup",
		"--dir", fixture.setupDirectory,
		"--wallet-keypair", fixture.walletKeypair,
		"--mithril-command", fixture.mithrilCommand,
		"--mithril-config", fixture.mithrilConfig,
		"--node-command", fixture.nodeCommand,
		"--quote-script", fixture.quoteScript,
		"--confirm-min-output-amount", "21525",
		"--primary-trust-domain", "provider-one",
		"--secondary-trust-domain", "provider-two",
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("repeat setup error = %v", err)
	}
}

func TestSwapSetupCreatesBuyConfiguration(t *testing.T) {
	fixture := newSwapSetupFixture(t)
	route := *testBuySwapProfile(t).BuyRoute
	route.Owner = fixture.owner
	inputAccount, err := orcaswap.AssociatedTokenAddress(fixture.owner, orcaswap.DevnetUSDCMint)
	if err != nil {
		t.Fatal(err)
	}
	route.InputTokenAccount = inputAccount
	installBuySwapSetupTestHooks(t, fixture.agentCommand, func(
		_ context.Context, owner, nodeCommand, quoteScript string,
		inputAmount uint64, slippageBPS uint16,
	) (orcaswap.BuyPolicyV2, error) {
		if owner != fixture.owner || nodeCommand != fixture.nodeCommand ||
			quoteScript != fixture.quoteScript || inputAmount != 100_000 || slippageBPS != 100 {
			t.Fatalf("buy discovery input = %q %q %q %d %d", owner, nodeCommand, quoteScript, inputAmount, slippageBPS)
		}
		return route, nil
	})
	var output bytes.Buffer
	err = runContext(t.Context(), []string{
		"swap", "setup", "--direction", "buy",
		"--dir", fixture.setupDirectory,
		"--wallet-keypair", fixture.walletKeypair,
		"--mithril-command", fixture.mithrilCommand,
		"--mithril-config", fixture.mithrilConfig,
		"--node-command", fixture.nodeCommand,
		"--quote-script", fixture.quoteScript,
		"--spend-usdc", "0.1", "--daily-spend-usdc", "0.2",
		"--daily-native-fee-cap-lamports", "200000",
		"--confirm-min-output-amount", "400000",
		"--primary-trust-domain", "provider-one",
		"--secondary-trust-domain", "provider-two",
		"--buy-at-usd", "20.5",
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	var result swapSetupResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.InputLamports != 0 || result.InputAmount != 100_000 ||
		result.InputAsset != "devUSDC" || result.OutputAsset != "SOL" ||
		result.MinimumOutput != route.MinOutputLamports {
		t.Fatalf("buy setup result = %+v", result)
	}
	paths := newSwapSetupPaths(fixture.setupDirectory)
	var cfg config
	var signerPolicy signer.Policy
	var submitterPolicy submitter.Policy
	if err := readStrictJSON(paths.config, &cfg); err != nil {
		t.Fatal(err)
	}
	if err := readStrictJSON(paths.signerPolicy, &signerPolicy); err != nil {
		t.Fatal(err)
	}
	if err := readStrictJSON(paths.submitterPolicy, &submitterPolicy); err != nil {
		t.Fatal(err)
	}
	if cfg.Swap == nil || !cfg.Swap.IsBuy() || cfg.Swap.BuyRoute == nil ||
		*cfg.Swap.BuyRoute != route || cfg.Swap.InputTokenAmount != 100_000 ||
		cfg.Swap.DailyInputTokenCap != 200_000 ||
		cfg.Swap.DailyNativeFeeCapLamports != 200_000 ||
		cfg.Swap.PriceTrigger == nil || cfg.Swap.PriceTrigger.Direction != pricetrigger.BuyAtOrBelow ||
		cfg.Swap.PriceTrigger.ThresholdMicros != 20_500_000 ||
		signerPolicy.OrcaBuy == nil || submitterPolicy.OrcaBuy == nil ||
		signerPolicy.OrcaSwap != nil || submitterPolicy.OrcaSwap != nil {
		t.Fatalf("buy setup is not mutually bound: cfg=%+v signer=%+v submitter=%+v", cfg.Swap, signerPolicy, submitterPolicy)
	}
}

func TestSwapSetupRejectsDirectionSpecificFlags(t *testing.T) {
	for name, directionArgs := range map[string][]string{
		"sell with buy amount":  {"--spend-usdc", "0.1"},
		"buy with sell amount":  {"--direction", "buy", "--spend-usdc", "0.1", "--input-lamports", "1"},
		"buy with sell trigger": {"--direction", "buy", "--spend-usdc", "0.1", "--sell-at-usd", "20"},
		"sell with buy trigger": {"--buy-at-usd", "20"},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newSwapSetupFixture(t)
			args := []string{
				"swap", "setup", "--dir", fixture.setupDirectory,
				"--wallet-keypair", fixture.walletKeypair,
				"--mithril-command", fixture.mithrilCommand,
				"--node-command", fixture.nodeCommand,
				"--quote-script", fixture.quoteScript,
				"--confirm-min-output-amount", "1",
				"--primary-trust-domain", "provider-one",
				"--secondary-trust-domain", "provider-two",
			}
			args = append(args, directionArgs...)
			if err := runContext(t.Context(), args, io.Discard); err == nil {
				t.Fatal("incompatible direction flags were accepted")
			}
		})
	}
}

func TestParseUSDThreshold(t *testing.T) {
	for input, want := range map[string]uint64{
		"1": 1_000_000, "20.5": 20_500_000, "73.134928": 73_134_928,
	} {
		got, err := parseUSDThreshold(input, "sell price")
		if err != nil || got != want {
			t.Fatalf("parse %q = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "0", " 1", "1 ", "-1", "+1", "1e2", ".5", "1.0000001"} {
		if _, err := parseUSDThreshold(input, "sell price"); err == nil {
			t.Fatalf("accepted invalid threshold %q", input)
		}
	}
}

func TestDevUSDCAmountParsingIsIndependentOfPriceCeiling(t *testing.T) {
	got, err := parseDecimalUnits("1000001", "devUSDC spend", ^uint64(0))
	if err != nil || got != 1_000_001_000_000 {
		t.Fatalf("large token amount = %d, %v", got, err)
	}
	if _, err := parseDecimalUnits("18446744073709.551616", "devUSDC spend", ^uint64(0)); err == nil {
		t.Fatal("overflowing token amount was accepted")
	}
}

func TestSwapSetupRejectsLocalLimitErrorsBeforeDiscovery(t *testing.T) {
	fixture := newSwapSetupFixture(t)
	discoveryCalls := 0
	installBuySwapSetupTestHooks(t, fixture.agentCommand, func(
		context.Context, string, string, string, uint64, uint16,
	) (orcaswap.BuyPolicyV2, error) {
		discoveryCalls++
		return orcaswap.BuyPolicyV2{}, errors.New("unexpected discovery")
	})
	base := []string{
		"--direction", "buy", "--dir", fixture.setupDirectory,
		"--wallet-keypair", fixture.walletKeypair,
		"--mithril-command", fixture.mithrilCommand,
		"--node-command", fixture.nodeCommand, "--quote-script", fixture.quoteScript,
		"--confirm-min-output-amount", "1", "--primary-trust-domain", "provider-one",
		"--secondary-trust-domain", "provider-two", "--spend-usdc", "0.1",
	}
	for name, extra := range map[string][]string{
		"zero fee":          {"--max-fee-lamports", "0"},
		"fractional window": {"--schedule-window", "1500ms"},
		"small daily fee":   {"--max-fee-lamports", "100000", "--daily-native-fee-cap-lamports", "99999"},
	} {
		t.Run(name, func(t *testing.T) {
			args := append(append([]string{}, base...), extra...)
			if err := runSwapSetup(t.Context(), args, io.Discard); err == nil {
				t.Fatal("invalid local limits were accepted")
			}
		})
	}
	if discoveryCalls != 0 {
		t.Fatalf("invalid local limits performed %d discoveries", discoveryCalls)
	}
}

func TestSwapSetupCanSelectRuntimeQuoteSocket(t *testing.T) {
	fixture := newSwapSetupFixture(t)
	wantRoute := testSwapProfile(fixture.owner).Route
	wantRoute.MinOutputAmount = 21_525
	installSwapSetupTestHooks(t, fixture.agentCommand, func(
		context.Context, string, string, string, uint64, uint16,
	) (orcaswap.Policy, error) {
		return wantRoute, nil
	})
	socketPath := filepath.Join(filepath.Dir(fixture.setupDirectory), "quote.sock")
	if err := runContext(t.Context(), []string{
		"swap", "setup",
		"--dir", fixture.setupDirectory,
		"--wallet-keypair", fixture.walletKeypair,
		"--mithril-command", fixture.mithrilCommand,
		"--mithril-config", fixture.mithrilConfig,
		"--node-command", fixture.nodeCommand,
		"--quote-script", fixture.quoteScript,
		"--quote-socket", socketPath,
		"--confirm-min-output-amount", "21525",
		"--primary-trust-domain", "provider-one",
		"--secondary-trust-domain", "provider-two",
	}, io.Discard); err != nil {
		t.Fatal(err)
	}
	var cfg config
	if err := readStrictJSON(newSwapSetupPaths(fixture.setupDirectory).config, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Quote.SocketPath != socketPath || cfg.Quote.Command != "" || cfg.Quote.ScriptPath != "" {
		t.Fatalf("runtime quote transport = %+v", cfg.Quote)
	}
}

func TestSwapSetupCanUseEnvironmentOnlyMithrilConfiguration(t *testing.T) {
	fixture := newSwapSetupFixture(t)
	wantRoute := testSwapProfile(fixture.owner).Route
	wantRoute.MinOutputAmount = 21_525
	installSwapSetupTestHooks(t, fixture.agentCommand, func(
		context.Context, string, string, string, uint64, uint16,
	) (orcaswap.Policy, error) {
		return wantRoute, nil
	})
	if err := runContext(t.Context(), []string{
		"swap", "setup",
		"--dir", fixture.setupDirectory,
		"--wallet-keypair", fixture.walletKeypair,
		"--mithril-command", fixture.mithrilCommand,
		"--node-command", fixture.nodeCommand,
		"--quote-script", fixture.quoteScript,
		"--confirm-min-output-amount", "21525",
		"--primary-trust-domain", "provider-one",
		"--secondary-trust-domain", "provider-two",
	}, io.Discard); err != nil {
		t.Fatal(err)
	}
	var cfg config
	if err := readStrictJSON(newSwapSetupPaths(fixture.setupDirectory).config, &cfg); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.MCP.Args, []string{"mcp", "--profile", "monitor"}) {
		t.Fatalf("MCP arguments = %q", cfg.MCP.Args)
	}
}

func TestSwapDiscoverDerivesOwnerAndUsesSelectedLimits(t *testing.T) {
	fixture := newSwapSetupFixture(t)
	wantRoute := testSwapProfile(fixture.owner).Route
	wantRoute.MaxInputLamports = 2_000_000
	installSwapSetupTestHooks(t, fixture.agentCommand, func(
		_ context.Context,
		owner, nodeCommand, quoteScript string,
		inputLamports uint64,
		slippageBPS uint16,
	) (orcaswap.Policy, error) {
		if owner != fixture.owner || nodeCommand != fixture.nodeCommand ||
			quoteScript != fixture.quoteScript || inputLamports != 2_000_000 ||
			slippageBPS != 125 {
			t.Fatalf(
				"discovery input = %q %q %q %d %d",
				owner, nodeCommand, quoteScript, inputLamports, slippageBPS,
			)
		}
		return wantRoute, nil
	})

	var output bytes.Buffer
	if err := runSwapDiscover(t.Context(), []string{
		"--wallet-keypair", fixture.walletKeypair,
		"--node-command", fixture.nodeCommand,
		"--quote-script", fixture.quoteScript,
		"--input-lamports", "2000000",
		"--slippage-bps", "125",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Route orcaswap.Policy `json:"route"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Route != wantRoute {
		t.Fatalf("route = %+v", result.Route)
	}
}

func TestSwapDiscoverBuildsBuyRoute(t *testing.T) {
	fixture := newSwapSetupFixture(t)
	wantRoute := *testBuySwapProfile(t).BuyRoute
	wantRoute.Owner = fixture.owner
	inputAccount, err := orcaswap.AssociatedTokenAddress(fixture.owner, orcaswap.DevnetUSDCMint)
	if err != nil {
		t.Fatal(err)
	}
	wantRoute.InputTokenAccount = inputAccount
	installBuySwapSetupTestHooks(t, fixture.agentCommand, func(
		_ context.Context, owner, nodeCommand, quoteScript string,
		inputAmount uint64, slippageBPS uint16,
	) (orcaswap.BuyPolicyV2, error) {
		if owner != fixture.owner || nodeCommand != fixture.nodeCommand ||
			quoteScript != fixture.quoteScript || inputAmount != 100_000 || slippageBPS != 125 {
			t.Fatalf("buy discovery input = %q %q %q %d %d", owner, nodeCommand, quoteScript, inputAmount, slippageBPS)
		}
		return wantRoute, nil
	})

	var output bytes.Buffer
	if err := runSwapDiscover(t.Context(), []string{
		"--direction", "buy", "--wallet-keypair", fixture.walletKeypair,
		"--node-command", fixture.nodeCommand, "--quote-script", fixture.quoteScript,
		"--spend-usdc", "0.1", "--slippage-bps", "125",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Direction string               `json:"direction"`
		Spend     uint64               `json:"input_token_amount"`
		Route     orcaswap.BuyPolicyV2 `json:"route"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Direction != "buy" || result.Spend != 100_000 || result.Route != wantRoute {
		t.Fatalf("buy discovery = %+v", result)
	}
}

func TestSwapDiscoverRequiresOneWalletSourceAndSafeSlippage(t *testing.T) {
	fixture := newSwapSetupFixture(t)
	base := []string{
		"--node-command", fixture.nodeCommand,
		"--quote-script", fixture.quoteScript,
	}
	for _, args := range [][]string{
		base,
		append(append([]string{}, base...),
			"--owner", fixture.owner, "--wallet-keypair", fixture.walletKeypair),
		append(append([]string{}, base...), "--owner", fixture.owner, "--slippage-bps", "0"),
		append(append([]string{}, base...), "--owner", fixture.owner, "--slippage-bps", "501"),
		append(append([]string{}, base...), "--owner", fixture.owner, "--spend-usdc", ""),
	} {
		if err := runSwapDiscover(t.Context(), args, io.Discard); err == nil {
			t.Fatalf("unsafe discovery arguments were accepted: %v", args)
		}
	}
}

func TestSwapSetupRemovesIncompleteStagingDirectory(t *testing.T) {
	fixture := newSwapSetupFixture(t)
	installSwapSetupTestHooks(t, fixture.agentCommand, func(
		context.Context, string, string, string, uint64, uint16,
	) (orcaswap.Policy, error) {
		return orcaswap.Policy{}, errors.New("quote unavailable")
	})
	err := runContext(t.Context(), []string{
		"swap", "setup",
		"--dir", fixture.setupDirectory,
		"--wallet-keypair", fixture.walletKeypair,
		"--mithril-command", fixture.mithrilCommand,
		"--mithril-config", fixture.mithrilConfig,
		"--node-command", fixture.nodeCommand,
		"--quote-script", fixture.quoteScript,
		"--confirm-min-output-amount", "1",
		"--primary-trust-domain", "provider-one",
		"--secondary-trust-domain", "provider-two",
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "quote unavailable") {
		t.Fatalf("setup error = %v", err)
	}
	if _, err := os.Lstat(fixture.setupDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete setup directory remains: %v", err)
	}
}

func TestSwapSetupRequiresExactQuoteConfirmation(t *testing.T) {
	fixture := newSwapSetupFixture(t)
	wantRoute := testSwapProfile(fixture.owner).Route
	wantRoute.MinOutputAmount = 21_525
	installSwapSetupTestHooks(t, fixture.agentCommand, func(
		context.Context, string, string, string, uint64, uint16,
	) (orcaswap.Policy, error) {
		return wantRoute, nil
	})
	err := runContext(t.Context(), []string{
		"swap", "setup",
		"--dir", fixture.setupDirectory,
		"--wallet-keypair", fixture.walletKeypair,
		"--mithril-command", fixture.mithrilCommand,
		"--mithril-config", fixture.mithrilConfig,
		"--node-command", fixture.nodeCommand,
		"--quote-script", fixture.quoteScript,
		"--confirm-min-output-amount", "1",
		"--primary-trust-domain", "provider-one",
		"--secondary-trust-domain", "provider-two",
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("minimum-output confirmation error = %v", err)
	}
	if _, err := os.Lstat(fixture.setupDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatched setup directory remains: %v", err)
	}
}

func TestSwapSetupRequiresPrivateMithrilConfig(t *testing.T) {
	fixture := newSwapSetupFixture(t)
	if err := os.Chmod(fixture.mithrilConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	installSwapSetupTestHooks(t, fixture.agentCommand, func(
		context.Context, string, string, string, uint64, uint16,
	) (orcaswap.Policy, error) {
		t.Fatal("route discovery ran with a public Mithril config")
		return orcaswap.Policy{}, nil
	})
	err := runContext(t.Context(), []string{
		"swap", "setup", "--dir", fixture.setupDirectory,
		"--wallet-keypair", fixture.walletKeypair,
		"--mithril-command", fixture.mithrilCommand,
		"--mithril-config", fixture.mithrilConfig,
		"--node-command", fixture.nodeCommand,
		"--quote-script", fixture.quoteScript,
		"--confirm-min-output-amount", "1",
		"--primary-trust-domain", "provider-one",
		"--secondary-trust-domain", "provider-two",
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "must not be readable") {
		t.Fatalf("public Mithril config error = %v", err)
	}
}

func TestSwapSetupRejectsWritableDirectoryAncestry(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	unsafe := filepath.Join(root, "unsafe")
	if err := os.Mkdir(unsafe, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0o777); err != nil {
		t.Fatal(err)
	}
	safeLeaf := filepath.Join(unsafe, "safe-leaf")
	if err := os.Mkdir(safeLeaf, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := cleanNewSetupPath(filepath.Join(safeLeaf, "agent")); err == nil {
		t.Fatal("setup accepted a writable ancestor")
	}
}

type swapSetupFixture struct {
	setupDirectory string
	walletKeypair  string
	owner          string
	agentCommand   string
	mithrilCommand string
	mithrilConfig  string
	nodeCommand    string
	quoteScript    string
}

func newSwapSetupFixture(t *testing.T) swapSetupFixture {
	t.Helper()
	t.Setenv(
		"MITHRIL_AGENT_PRIMARY_RPC_URL",
		"https://rpc-one.invalid/path?api-key=secret-one",
	)
	t.Setenv(
		"MITHRIL_AGENT_SECONDARY_RPC_URL",
		"https://rpc-two.invalid/path?api-key=secret-two",
	)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	commands := []string{
		"mithril-agent", "mithril-agent-policy", "mithril-agent-signer",
		"mithril-agent-submitter", "mithril", "node",
	}
	for _, name := range commands {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("test executable\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	quoteScript := filepath.Join(root, "quote.mjs")
	if err := os.WriteFile(quoteScript, []byte("// test adapter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mithrilConfig := filepath.Join(root, "mithril.toml")
	if err := os.WriteFile(mithrilConfig, []byte("[network]\ncluster = 'devnet'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seed := sha256.Sum256([]byte(t.Name()))
	wallet := ed25519.NewKeyFromSeed(seed[:])
	walletPath := filepath.Join(root, "wallet.json")
	writeKeypair(t, walletPath, wallet)
	return swapSetupFixture{
		setupDirectory: filepath.Join(root, "private-swap"),
		walletKeypair:  walletPath,
		owner:          solana.Encode(wallet.Public().(ed25519.PublicKey)),
		agentCommand:   filepath.Join(bin, "mithril-agent"),
		mithrilCommand: filepath.Join(bin, "mithril"),
		mithrilConfig:  mithrilConfig,
		nodeCommand:    filepath.Join(bin, "node"),
		quoteScript:    quoteScript,
	}
}

func testRPCIdentity(t *testing.T, endpoint string) string {
	t.Helper()
	client, err := solanarpc.New(endpoint, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return client.Identity()
}

func installSwapSetupTestHooks(
	t *testing.T,
	agentCommand string,
	discover func(context.Context, string, string, string, uint64, uint16) (orcaswap.Policy, error),
) {
	t.Helper()
	previousExecutable := swapSetupExecutable
	previousDiscover := swapSetupDiscover
	swapSetupExecutable = func() (string, error) { return agentCommand, nil }
	swapSetupDiscover = discover
	t.Cleanup(func() {
		swapSetupExecutable = previousExecutable
		swapSetupDiscover = previousDiscover
	})
}

func installBuySwapSetupTestHooks(
	t *testing.T,
	agentCommand string,
	discover func(context.Context, string, string, string, uint64, uint16) (orcaswap.BuyPolicyV2, error),
) {
	t.Helper()
	previousExecutable := swapSetupExecutable
	previousDiscover := swapSetupDiscoverBuy
	swapSetupExecutable = func() (string, error) { return agentCommand, nil }
	swapSetupDiscoverBuy = discover
	t.Cleanup(func() {
		swapSetupExecutable = previousExecutable
		swapSetupDiscoverBuy = previousDiscover
	})
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
	}
}

type setupTreeEntry struct {
	Mode    os.FileMode
	Size    int64
	ModTime int64
	Digest  [32]byte
}

func snapshotSetupTree(t *testing.T, root string) map[string]setupTreeEntry {
	t.Helper()
	entries := make(map[string]setupTreeEntry)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		entry := setupTreeEntry{Mode: info.Mode(), Size: info.Size(), ModTime: info.ModTime().UnixNano()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry.Digest = sha256.Sum256(data)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries[relative] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
