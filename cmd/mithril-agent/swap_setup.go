package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/swaprun"
)

const (
	defaultSwapInputLamports = uint64(1_000_000)
	defaultSwapReserve       = uint64(50_000_000)
	defaultSwapMaxFee        = uint64(100_000)
	maxSetupFileBytes        = int64(64 << 10)
)

var (
	swapSetupDiscover    = discoverSwapPolicy
	swapSetupDiscoverBuy = discoverBuyPolicy
	swapSetupExecutable  = os.Executable
	swapSetupRandom      = rand.Reader
)

type swapSetupOptions struct {
	directory          string
	direction          string
	walletKeypair      string
	mithrilCommand     string
	mithrilConfig      string
	nodeCommand        string
	quoteScript        string
	quoteSocket        string
	inputLamports      uint64
	inputTokenAmount   uint64
	confirmedMinOut    uint64
	slippageBPS        uint16
	reserveLamports    uint64
	maxFeeLamports     uint64
	dailyDebitCap      uint64
	dailyInputTokenCap uint64
	dailyNativeFeeCap  uint64
	scheduleWindow     time.Duration
	primaryTrust       string
	secondaryTrust     string
	sellAtMicros       uint64
	buyAtMicros        uint64
	// confirmQuote, when set, replaces the numeric confirmation with a live one
	// against the route that was just discovered. Both paths enforce the same
	// property — a human agreed to this exact number — but the callback closes
	// the window in which the market can move between an operator reading a
	// quote in one command and setup re-reading it in the next.
	confirmQuote func(quoteConfirmation) error
}

// quoteConfirmation is what an operator is agreeing to: the floor that will be
// written into the policy, in the units they think in.
type quoteConfirmation struct {
	Direction   string
	InputText   string
	OutputText  string
	MinOutput   uint64
	SlippageBPS uint16
}

// confirmMinimumOutput is the single gate. It is never skipped: createSwapSetup
// refuses to build a profile unless one of the two forms of confirmation is
// present.
func (o swapSetupOptions) confirmMinimumOutput(quote quoteConfirmation) error {
	if o.confirmQuote != nil {
		return o.confirmQuote(quote)
	}
	if o.confirmedMinOut != quote.MinOutput {
		return errors.New("confirmed minimum output does not match the current read-only quote")
	}
	return nil
}

type swapSetupResult struct {
	Status        string   `json:"status"`
	ConfigPath    string   `json:"config_path"`
	ProfileSHA256 string   `json:"profile_sha256"`
	InputLamports uint64   `json:"input_lamports"`
	InputAmount   uint64   `json:"input_amount"`
	InputAsset    string   `json:"input_asset"`
	OutputAsset   string   `json:"output_asset"`
	MinimumOutput uint64   `json:"minimum_output"`
	PlanArgv      []string `json:"plan_argv"`
	PreflightArgv []string `json:"preflight_argv"`
	LiveCheckArgv []string `json:"live_check_argv"`
	DemoArgv      []string `json:"demo_argv"`
}

type swapSetupCommands struct {
	agent     string
	policy    string
	signer    string
	submitter string
}

func runSwapSetup(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("swap setup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	directory := flags.String("dir", "", "new private setup directory")
	direction := flags.String("direction", "sell", "trade direction: sell or buy")
	walletKeypair := flags.String("wallet-keypair", "", "private Devnet wallet keypair")
	mithrilCommand := flags.String("mithril-command", "", "absolute Mithril executable")
	mithrilConfig := flags.String("mithril-config", "", "absolute Mithril node config")
	nodeCommand := flags.String("node-command", "", "absolute Node.js executable")
	quoteScript := flags.String("quote-script", "", "absolute Orca quote adapter")
	quoteSocket := flags.String("quote-socket", "", "protected runtime quote socket")
	inputLamports := flags.Uint64("input-lamports", defaultSwapInputLamports, "exact Devnet input amount")
	spendUSDC := flags.String("spend-usdc", "", "exact Devnet devUSDC amount")
	confirmedMinOut := flags.Uint64(
		"confirm-min-output-amount", 0, "confirm the current discovered minimum output",
	)
	slippageBPS := flags.Uint("slippage-bps", 100, "maximum slippage in basis points")
	reserveLamports := flags.Uint64("reserve-lamports", defaultSwapReserve, "wallet reserve")
	maxFeeLamports := flags.Uint64("max-fee-lamports", defaultSwapMaxFee, "maximum transaction fee")
	dailyDebitCap := flags.Uint64("daily-debit-cap-lamports", 0, "daily input plus fee cap")
	dailySpendUSDC := flags.String("daily-spend-usdc", "", "daily devUSDC input cap")
	dailyNativeFeeCap := flags.Uint64(
		"daily-native-fee-cap-lamports", 0, "daily native transaction-fee cap",
	)
	scheduleWindow := flags.Duration("schedule-window", time.Hour, "one-action schedule window")
	primaryTrust := flags.String("primary-trust-domain", "", "primary evidence provider trust domain")
	secondaryTrust := flags.String("secondary-trust-domain", "", "secondary evidence provider trust domain")
	sellAtUSD := flags.String("sell-at-usd", "", "optional one-shot SOL sell threshold")
	buyAtUSD := flags.String("buy-at-usd", "", "optional one-shot SOL buy threshold")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, swapSetupUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *directory == "" || *walletKeypair == "" ||
		*mithrilCommand == "" || *nodeCommand == "" || *quoteScript == "" ||
		*confirmedMinOut == 0 || *primaryTrust == "" || *secondaryTrust == "" {
		return errors.New("swap setup requires all paths, quote confirmation, and two evidence trust domains")
	}
	if *direction != "sell" && *direction != "buy" {
		return errors.New("swap setup direction must be sell or buy")
	}
	if *slippageBPS == 0 || *slippageBPS > 500 {
		return errors.New("swap setup slippage must be between 1 and 500 basis points")
	}
	explicit := make(map[string]bool)
	flags.Visit(func(item *flag.Flag) { explicit[item.Name] = true })
	var sellAtMicros, buyAtMicros, inputTokenAmount, dailyInputTokenCap uint64
	if *direction == "buy" {
		for _, name := range []string{"input-lamports", "daily-debit-cap-lamports", "sell-at-usd"} {
			if explicit[name] {
				return fmt.Errorf("--%s is only valid for sell setup", name)
			}
		}
		var err error
		inputTokenAmount, err = parseDecimalUnits(*spendUSDC, "devUSDC spend", ^uint64(0))
		if err != nil {
			return err
		}
		if *dailySpendUSDC == "" {
			dailyInputTokenCap = inputTokenAmount
		} else if dailyInputTokenCap, err = parseDecimalUnits(*dailySpendUSDC, "daily devUSDC spend", ^uint64(0)); err != nil {
			return err
		}
		if dailyInputTokenCap < inputTokenAmount {
			return errors.New("daily devUSDC spend must cover one configured trade")
		}
		if *dailyNativeFeeCap == 0 {
			*dailyNativeFeeCap = *maxFeeLamports
		}
		if *buyAtUSD != "" {
			buyAtMicros, err = parseDecimalUnits(*buyAtUSD, "buy price", pricetrigger.MaxPriceMicros)
			if err != nil {
				return err
			}
		}
	} else {
		for _, name := range []string{"spend-usdc", "daily-spend-usdc", "daily-native-fee-cap-lamports", "buy-at-usd"} {
			if explicit[name] {
				return fmt.Errorf("--%s is only valid for buy setup", name)
			}
		}
	}
	if *sellAtUSD != "" {
		var err error
		sellAtMicros, err = parseUSDThreshold(*sellAtUSD)
		if err != nil {
			return err
		}
	}
	if *reserveLamports == 0 || *maxFeeLamports == 0 {
		return errors.New("swap setup reserve and maximum fee must be positive")
	}
	windowSeconds := uint64(*scheduleWindow / time.Second)
	if *scheduleWindow <= 0 || time.Duration(windowSeconds)*time.Second != *scheduleWindow {
		return errors.New("swap setup schedule window must use positive whole seconds")
	}
	if *direction == "buy" && *dailyNativeFeeCap < *maxFeeLamports {
		return errors.New("daily native fee cap must cover one maximum transaction fee")
	}
	if *direction == "sell" && *dailyDebitCap != 0 {
		if *inputLamports > ^uint64(0)-*maxFeeLamports ||
			*dailyDebitCap < *inputLamports+*maxFeeLamports {
			return errors.New("daily debit cap must cover one input and maximum transaction fee")
		}
	}
	options := swapSetupOptions{
		directory: *directory, direction: *direction, walletKeypair: *walletKeypair,
		mithrilCommand: *mithrilCommand, mithrilConfig: *mithrilConfig,
		nodeCommand: *nodeCommand,
		quoteScript: *quoteScript, quoteSocket: *quoteSocket,
		inputLamports: *inputLamports, inputTokenAmount: inputTokenAmount,
		confirmedMinOut: *confirmedMinOut,
		slippageBPS:     uint16(*slippageBPS), reserveLamports: *reserveLamports,
		maxFeeLamports: *maxFeeLamports, dailyDebitCap: *dailyDebitCap,
		dailyInputTokenCap: dailyInputTokenCap, dailyNativeFeeCap: *dailyNativeFeeCap,
		scheduleWindow: *scheduleWindow, primaryTrust: *primaryTrust,
		secondaryTrust: *secondaryTrust,
		sellAtMicros:   sellAtMicros, buyAtMicros: buyAtMicros,
	}
	if !validTrustDomain(options.primaryTrust) || !validTrustDomain(options.secondaryTrust) ||
		options.primaryTrust == options.secondaryTrust {
		return errors.New("swap setup requires two distinct evidence trust domains")
	}
	result, err := createSwapSetup(ctx, options)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

func createSwapSetup(ctx context.Context, options swapSetupOptions) (swapSetupResult, error) {
	if options.confirmQuote == nil && options.confirmedMinOut == 0 {
		return swapSetupResult{}, errors.New("swap setup requires a confirmed minimum output")
	}
	root, err := cleanNewSetupPath(options.directory)
	if err != nil {
		return swapSetupResult{}, err
	}
	walletPath, err := cleanExistingPath(options.walletKeypair)
	if err != nil {
		return swapSetupResult{}, fmt.Errorf("wallet keypair: %w", err)
	}
	walletKey, err := signer.LoadKeypair(walletPath)
	if err != nil {
		return swapSetupResult{}, fmt.Errorf("wallet keypair: %w", err)
	}
	defer clear(walletKey)
	walletPublic, ok := walletKey.Public().(ed25519.PublicKey)
	if !ok {
		return swapSetupResult{}, errors.New("wallet public key is invalid")
	}
	owner := solana.Encode(walletPublic)

	agentCommand, err := resolvedAgentExecutable()
	if err != nil {
		return swapSetupResult{}, err
	}
	commands := swapSetupCommands{
		agent:     agentCommand,
		policy:    filepath.Join(filepath.Dir(agentCommand), "mithril-agent-policy"),
		signer:    filepath.Join(filepath.Dir(agentCommand), "mithril-agent-signer"),
		submitter: filepath.Join(filepath.Dir(agentCommand), "mithril-agent-submitter"),
	}
	for name, path := range map[string]string{
		"Mithril": options.mithrilCommand, "Node.js": options.nodeCommand,
		"agent": commands.agent, "risk authority": commands.policy,
		"signer": commands.signer, "submitter": commands.submitter,
	} {
		if err := validateExecutable(path); err != nil {
			return swapSetupResult{}, fmt.Errorf("%s executable: %w", name, err)
		}
	}
	if err := validateProtectedFile(options.quoteScript); err != nil {
		return swapSetupResult{}, fmt.Errorf("Orca quote script: %w", err)
	}
	if options.quoteSocket != "" &&
		(!filepath.IsAbs(options.quoteSocket) || filepath.Clean(options.quoteSocket) != options.quoteSocket) {
		return swapSetupResult{}, errors.New("Orca quote socket must be an absolute clean path")
	}
	if options.mithrilConfig != "" {
		if err := validatePrivateFile(options.mithrilConfig); err != nil {
			return swapSetupResult{}, fmt.Errorf("Mithril node config: %w", err)
		}
	}
	primaryProvider, secondaryProvider, err := openEvidenceProviders(
		os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL"),
		os.Getenv("MITHRIL_AGENT_SECONDARY_RPC_URL"),
	)
	if err != nil {
		return swapSetupResult{}, err
	}

	windowSeconds := uint64(options.scheduleWindow / time.Second)
	if options.scheduleWindow <= 0 || time.Duration(windowSeconds)*time.Second != options.scheduleWindow {
		return swapSetupResult{}, errors.New("swap setup schedule window must use whole seconds")
	}
	profile := swaprun.Profile{
		Cluster:     "devnet",
		SlippageBPS: options.slippageBPS, ReserveLamports: options.reserveLamports,
		MaxFeeLamports:            options.maxFeeLamports,
		ScheduleWindowSeconds:     windowSeconds,
		ScheduleAnchorUnix:        time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		MaxClockUncertaintyMillis: 500, MaxObservationAgeSeconds: 30,
		MinHealthyObservationSeconds: 5, MinHealthySlotAdvance: 1,
		MaxBlockHeightWindow: 200, MaxReconciliationSeconds: 180,
	}
	if options.direction == "buy" {
		route, err := swapSetupDiscoverBuy(
			ctx, owner, options.nodeCommand, options.quoteScript,
			options.inputTokenAmount, options.slippageBPS,
		)
		if err != nil {
			return swapSetupResult{}, fmt.Errorf("discover Orca buy route: %w", err)
		}
		if err := options.confirmMinimumOutput(quoteConfirmation{
			Direction:  "buy",
			InputText:  formatUnits(options.inputTokenAmount, 6) + " devUSDC",
			OutputText: formatUnits(route.MinOutputLamports, 9) + " SOL",
			MinOutput:  route.MinOutputLamports, SlippageBPS: options.slippageBPS,
		}); err != nil {
			return swapSetupResult{}, err
		}
		profile.Name, profile.Version = orcaswap.BuyProfileName, orcaswap.BuyProfileVersion
		profile.BuyRoute = &route
		profile.InputTokenAmount = options.inputTokenAmount
		profile.DailyInputTokenCap = options.dailyInputTokenCap
		profile.DailyNativeFeeCapLamports = options.dailyNativeFeeCap
	} else {
		route, err := swapSetupDiscover(
			ctx, owner, options.nodeCommand, options.quoteScript,
			options.inputLamports, options.slippageBPS,
		)
		if err != nil {
			return swapSetupResult{}, fmt.Errorf("discover Orca route: %w", err)
		}
		if err := options.confirmMinimumOutput(quoteConfirmation{
			Direction:  "sell",
			InputText:  formatUnits(options.inputLamports, 9) + " SOL",
			OutputText: formatUnits(route.MinOutputAmount, 6) + " devUSDC",
			MinOutput:  route.MinOutputAmount, SlippageBPS: options.slippageBPS,
		}); err != nil {
			return swapSetupResult{}, err
		}
		if options.dailyDebitCap == 0 {
			if options.inputLamports > ^uint64(0)-options.maxFeeLamports ||
				options.inputLamports+options.maxFeeLamports > ^uint64(0)-route.MaxOutputAccountRentLamports {
				return swapSetupResult{}, errors.New("swap setup debit cap overflows")
			}
			options.dailyDebitCap = options.inputLamports + options.maxFeeLamports +
				route.MaxOutputAccountRentLamports
		}
		profile.Name, profile.Version = orcaswap.ProfileName, orcaswap.ProfileVersion
		profile.Route = route
		profile.InputLamports = options.inputLamports
		profile.DailyDebitCapLamports = options.dailyDebitCap
	}
	threshold, direction := options.sellAtMicros, pricetrigger.SellAtOrAbove
	if profile.IsBuy() {
		threshold, direction = options.buyAtMicros, pricetrigger.BuyAtOrBelow
	}
	if threshold != 0 {
		profile.PriceTrigger = &pricetrigger.Policy{
			Version: pricetrigger.Version, Feed: pricetrigger.FeedSOLUSD,
			Direction: direction, ThresholdMicros: threshold,
			MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90,
			MaxDeviationBPS: 200, MaxConfidenceBPS: 200,
			// The sponsored on-chain feed read through the operator's own node
			// is the default so no paid data subscription is required. An
			// operator with a Hermes key can substitute that adapter's identity.
			PrimarySourceSHA256:   pricesource.PythPushIdentitySHA256(),
			SecondarySourceSHA256: pricesource.CoinbaseIdentitySHA256(),
		}
	}
	return finishSwapSetup(
		root, walletPath, owner, profile, options, commands,
		primaryProvider.Identity(), secondaryProvider.Identity(),
	)
}

func finishSwapSetup(
	root,
	walletPath,
	owner string,
	profile swaprun.Profile,
	options swapSetupOptions,
	commands swapSetupCommands,
	primaryProviderIdentity,
	secondaryProviderIdentity string,
) (swapSetupResult, error) {
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		return swapSetupResult{}, fmt.Errorf("swap profile: %w", err)
	}
	_, riskPrivate, err := ed25519.GenerateKey(swapSetupRandom)
	if err != nil {
		return swapSetupResult{}, errors.New("generate risk authority key")
	}
	defer clear(riskPrivate)
	riskPublic, err := riskgrant.PublicKeyHex(riskPrivate)
	if err != nil {
		return swapSetupResult{}, err
	}
	riskIDHash := sha256.Sum256([]byte(riskPublic))
	riskID := "risk-" + hex.EncodeToString(riskIDHash[:8])
	submitterPrivate, submitterPublic, err := sealedtx.GenerateKey(swapSetupRandom)
	if err != nil {
		return swapSetupResult{}, err
	}

	paths := newSwapSetupPaths(root)
	signerPolicy := signer.Policy{
		Cluster: profile.Cluster, Profile: profile.Name, ProfileVersion: profile.Version,
		ProfileFingerprint: fingerprint, Source: owner,
		MaxLamports: profile.InputLamports, MaxInputTokenAmount: profile.InputTokenAmount,
		MaxFeeLamports:            profile.MaxFeeLamports,
		DailyDebitCapLamports:     profile.DailyDebitCapLamports,
		DailyInputTokenCap:        profile.DailyInputTokenCap,
		DailyNativeFeeCapLamports: profile.DailyNativeFeeCapLamports,
		AuthorizationLedgerPath:   paths.authorizationLedger,
		ScheduleWindowSeconds:     profile.ScheduleWindowSeconds,
		ScheduleAnchorUnix:        profile.ScheduleAnchorUnix,
		MaxBlockHeightWindow:      profile.MaxBlockHeightWindow,
		RiskAuthorityKeyID:        riskID, RiskAuthorityPublicKey: riskPublic,
		SubmitterPublicKey: submitterPublic,
	}
	submitterPolicy := submitter.Policy{
		Cluster: profile.Cluster, Profile: profile.Name,
		ProfileFingerprint: fingerprint, ControlStatePath: paths.control, Source: owner,
		MaxLamports: profile.InputLamports, MaxInputTokenAmount: profile.InputTokenAmount,
		MaxFeeLamports: profile.MaxFeeLamports, SubmitterPublicKey: submitterPublic,
	}
	minimumOutput := profile.Route.MinOutputAmount
	if profile.IsBuy() {
		signerPolicy.OrcaBuy = profile.BuyRoute
		submitterPolicy.OrcaBuy = profile.BuyRoute
		minimumOutput = profile.BuyRoute.MinOutputLamports
	} else {
		signerPolicy.OrcaSwap = &profile.Route
		submitterPolicy.OrcaSwap = &profile.Route
	}
	riskPolicy := policyauthority.Policy{TransactionPolicy: signerPolicy, GrantLifetimeSecs: 30}
	if err := signerPolicy.ValidateAuthorizationPolicy(); err != nil {
		return swapSetupResult{}, fmt.Errorf("signer policy: %w", err)
	}
	if err := riskPolicy.Validate(); err != nil {
		return swapSetupResult{}, fmt.Errorf("risk policy: %w", err)
	}
	if err := submitterPolicy.Validate(); err != nil {
		return swapSetupResult{}, fmt.Errorf("submitter policy: %w", err)
	}

	var cfg config
	cfg.Swap = &profile
	cfg.MCP.Command = options.mithrilCommand
	cfg.MCP.Args = []string{"mcp", "--profile", "monitor"}
	if options.mithrilConfig != "" {
		cfg.MCP.Args = []string{
			"mcp", "--config", options.mithrilConfig, "--profile", "monitor",
		}
	}
	cfg.Policy.Command = commands.policy
	cfg.Policy.PolicyPath = paths.riskPolicy
	cfg.Policy.KeypairPath = paths.riskKeypair
	cfg.Policy.KeyID = riskID
	cfg.Policy.PublicKey = riskPublic
	cfg.Signer.Command = commands.signer
	cfg.Signer.PolicyPath = paths.signerPolicy
	cfg.Signer.KeypairPath = walletPath
	cfg.Submitter.Command = commands.submitter
	cfg.Submitter.PolicyPath = paths.submitterPolicy
	cfg.Submitter.PrivateKeyPath = paths.submitterKey
	if options.quoteSocket == "" {
		cfg.Quote.Command = options.nodeCommand
		cfg.Quote.ScriptPath = options.quoteScript
	} else {
		cfg.Quote.SocketPath = options.quoteSocket
	}
	cfg.Evidence.PrimaryTrustDomain = options.primaryTrust
	cfg.Evidence.PrimaryOriginSHA256 = primaryProviderIdentity
	cfg.Evidence.SecondaryTrustDomain = options.secondaryTrust
	cfg.Evidence.SecondaryOriginSHA256 = secondaryProviderIdentity
	cfg.Control.StatePath = paths.control
	cfg.Journal.Path = paths.journal

	riskKeypairDocument := keypairDocument(riskPrivate)
	defer clear(riskKeypairDocument)
	documents := map[string]any{
		"config.json": cfg, "signer-policy.json": signerPolicy,
		"risk-policy.json": riskPolicy, "risk-authority-keypair.json": riskKeypairDocument,
		"submitter-policy.json": submitterPolicy,
		"submitter-key.json":    submitter.KeyDocument{Version: 1, PrivateKey: submitterPrivate},
	}
	if err := installSwapSetup(root, documents); err != nil {
		return swapSetupResult{}, err
	}
	inputAsset, outputAsset := "SOL", "devUSDC"
	if profile.IsBuy() {
		inputAsset, outputAsset = "devUSDC", "SOL"
	}
	return swapSetupResult{
		Status: "configured", ConfigPath: paths.config, ProfileSHA256: fingerprint,
		InputLamports: profile.InputLamports, InputAmount: profile.InputAmount(),
		InputAsset: inputAsset, OutputAsset: outputAsset, MinimumOutput: minimumOutput,
		PlanArgv:      []string{commands.agent, "swap", "plan", "--config", paths.config},
		PreflightArgv: []string{commands.agent, "preflight", "--config", paths.config},
		LiveCheckArgv: []string{commands.agent, "swap", "check", "--config", paths.config},
		DemoArgv:      []string{commands.agent, "swap", "demo", "--config", paths.config},
	}, nil
}

func parseUSDThreshold(value string) (uint64, error) {
	return parseDecimalUnits(value, "sell price", pricetrigger.MaxPriceMicros)
}

func parseDecimalUnits(value, description string, maximum uint64) (uint64, error) {
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "eE+-") {
		return 0, fmt.Errorf("%s must be a positive amount with at most six decimals", description)
	}
	whole, fraction, found := strings.Cut(value, ".")
	if !found {
		fraction = ""
	}
	if whole == "" || len(whole) > 20 || len(fraction) > 6 {
		return 0, fmt.Errorf("%s must be a positive amount with at most six decimals", description)
	}
	for _, part := range []string{whole, fraction} {
		for _, char := range part {
			if char < '0' || char > '9' {
				return 0, fmt.Errorf("%s must be a positive amount with at most six decimals", description)
			}
		}
	}
	wholeValue, err := strconv.ParseUint(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s is outside policy", description)
	}
	padded := fraction + strings.Repeat("0", 6)
	fractionValue, err := strconv.ParseUint(padded[:6], 10, 64)
	if err != nil || wholeValue > maximum/1_000_000 ||
		(wholeValue == maximum/1_000_000 && fractionValue > maximum%1_000_000) {
		return 0, fmt.Errorf("%s is outside policy", description)
	}
	result := wholeValue*1_000_000 + fractionValue
	if result == 0 || result > maximum {
		return 0, fmt.Errorf("%s is outside policy", description)
	}
	return result, nil
}

type swapSetupPaths struct {
	config, signerPolicy, riskPolicy, riskKeypair, submitterPolicy, submitterKey string
	control, journal, authorizationLedger                                        string
}

func newSwapSetupPaths(root string) swapSetupPaths {
	state := filepath.Join(root, "state")
	return swapSetupPaths{
		config:              filepath.Join(root, "config.json"),
		signerPolicy:        filepath.Join(root, "signer-policy.json"),
		riskPolicy:          filepath.Join(root, "risk-policy.json"),
		riskKeypair:         filepath.Join(root, "risk-authority-keypair.json"),
		submitterPolicy:     filepath.Join(root, "submitter-policy.json"),
		submitterKey:        filepath.Join(root, "submitter-key.json"),
		control:             filepath.Join(state, "control.json"),
		journal:             filepath.Join(state, "events.jsonl"),
		authorizationLedger: filepath.Join(state, "signer-authorizations.jsonl"),
	}
}

func cleanNewSetupPath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return "", errors.New("swap setup directory must be a clean absolute path")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return "", errors.New("swap setup directory already exists")
		}
		return "", errors.New("inspect swap setup directory")
	}
	parent := filepath.Dir(path)
	if _, err := os.Stat(parent); errors.Is(err, os.ErrNotExist) {
		return "", errors.New("swap setup parent directory does not exist: " + parent)
	}
	if err := validateSafeParent(parent, 0o022); err != nil {
		return "", errors.New("swap setup parent directory is unsafe " +
			"(it must not be writable by group or other): " + parent)
	}
	return path, nil
}

func cleanExistingPath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("path must be absolute and clean")
	}
	return path, nil
}

func resolvedAgentExecutable() (string, error) {
	path, err := swapSetupExecutable()
	if err != nil {
		return "", errors.New("locate agent executable")
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) {
		return "", errors.New("resolve agent executable")
	}
	return filepath.Clean(path), nil
}

func validateProtectedFile(path string) error {
	path, err := cleanExistingPath(path)
	if err != nil {
		return err
	}
	return secureexec.ValidateProtectedFile(path)
}

func validatePrivateFile(path string) error {
	if err := validateProtectedFile(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("file must not be readable or writable by group or others")
	}
	return nil
}

func keypairDocument(key ed25519.PrivateKey) []uint16 {
	values := make([]uint16, len(key))
	for index, value := range key {
		values[index] = uint16(value)
	}
	return values
}

func installSwapSetup(root string, documents map[string]any) error {
	parent := filepath.Dir(root)
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(root)+".setup-")
	if err != nil {
		return errors.New("create swap setup staging directory")
	}
	installed := false
	defer func() {
		if !installed {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := os.Chmod(stage, 0o700); err != nil {
		return errors.New("protect swap setup staging directory")
	}
	if err := os.Mkdir(filepath.Join(stage, "state"), 0o700); err != nil {
		return errors.New("create swap state directory")
	}
	for name, document := range documents {
		encoded, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return errors.New("encode swap setup document")
		}
		encoded = append(encoded, '\n')
		if err := securefile.ReplacePrivate(
			filepath.Join(stage, name), encoded, maxSetupFileBytes,
		); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	stageDirectory, err := os.Open(stage)
	if err != nil {
		return errors.New("open swap setup staging directory")
	}
	if err := stageDirectory.Sync(); err != nil {
		_ = stageDirectory.Close()
		return errors.New("sync swap setup staging directory")
	}
	if err := stageDirectory.Close(); err != nil {
		return errors.New("close swap setup staging directory")
	}
	if err := os.Rename(stage, root); err != nil {
		return errors.New("install swap setup directory")
	}
	installed = true
	parentDirectory, err := os.Open(parent)
	if err != nil {
		return errors.New("open swap setup parent directory")
	}
	defer parentDirectory.Close()
	if err := parentDirectory.Sync(); err != nil {
		return errors.New("sync swap setup parent directory")
	}
	return nil
}

// formatUnits renders a fixed-point integer amount the way a person reads it.
// Integer only: a float here would round the number the operator is confirming.
func formatUnits(amount uint64, decimals uint) string {
	scale := uint64(1)
	for range decimals {
		scale *= 10
	}
	return fmt.Sprintf("%d.%0*d", amount/scale, decimals, amount%scale)
}
