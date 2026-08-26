package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/internal/clockcheck"
	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/mcpobserve"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/policyclient"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/signerclient"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/submitterclient"
	"github.com/Overclock-Validator/mithril-agent/swapbuilder"
	"github.com/Overclock-Validator/mithril-agent/swaprun"
)

const (
	preflightOK      = "ok"
	preflightFailed  = "failed"
	preflightSkipped = "skipped"
)

var (
	errPreflightFailed          = errors.New("preflight failed")
	preflightClockSample        = clockcheck.SystemSample
	preflightOperatingSystem    = runtime.GOOS
	preflightSignerIdentity     = verifySignerIdentity
	preflightSocketIdentity     = verifySignerSocketIdentity
	preflightRiskIdentity       = verifyRiskIdentity
	preflightRiskSocketIdentity = verifyRiskSocketIdentity
	preflightSubmitIdentity     = verifySubmitterIdentity
)

func verifySignerIdentity(
	ctx context.Context,
	command,
	policyPath,
	keypairPath,
	expected string,
) error {
	client, err := signerclient.New(signerclient.Config{
		Command: command, PolicyPath: policyPath, KeypairPath: keypairPath,
	})
	if err != nil {
		return err
	}
	identity, err := client.Identity(ctx)
	if err != nil || identity.PublicKey != expected {
		return errors.New("signer identity does not match policy")
	}
	return nil
}

func verifySignerSocketIdentity(
	ctx context.Context,
	socketPath,
	expected string,
) error {
	client, err := signerclient.New(signerclient.Config{SocketPath: socketPath})
	if err != nil {
		return err
	}
	identity, err := client.Identity(ctx)
	if err != nil || identity.PublicKey != expected {
		return errors.New("signer identity does not match policy")
	}
	return nil
}

func verifyRiskIdentity(
	ctx context.Context,
	command,
	policyPath,
	keypairPath,
	keyID,
	publicKey string,
) error {
	client, err := policyclient.New(policyclient.Config{
		Command: command, PolicyPath: policyPath, KeypairPath: keypairPath,
		KeyID: keyID, PublicKey: publicKey,
	})
	if err != nil {
		return err
	}
	_, err = client.Identity(ctx)
	return err
}

func verifyRiskSocketIdentity(ctx context.Context, socketPath, keyID, publicKey string) error {
	client, err := policyclient.New(policyclient.Config{
		SocketPath: socketPath, KeyID: keyID, PublicKey: publicKey,
	})
	if err != nil {
		return err
	}
	_, err = client.Identity(ctx)
	return err
}

func verifySubmitterIdentity(
	ctx context.Context,
	command,
	policyPath,
	keyPath,
	expected string,
) error {
	client, err := submitterclient.New(submitterclient.Config{
		Command: command, PolicyPath: policyPath, PrivateKeyPath: keyPath,
	})
	if err != nil {
		return err
	}
	identity, err := client.Identity(ctx)
	if err != nil || identity.PublicKey != expected {
		return errors.New("submitter identity does not match policy")
	}
	return nil
}

type preflightSummary struct {
	Status string          `json:"status"`
	Checks preflightChecks `json:"checks"`
}

type preflightChecks struct {
	Config          string `json:"config"`
	Profile         string `json:"profile"`
	PolicyBinding   string `json:"policy_binding"`
	KeypairBinding  string `json:"keypair_binding"`
	RiskPolicy      string `json:"risk_policy_binding"`
	RiskKeypair     string `json:"risk_keypair_binding"`
	SubmitterPolicy string `json:"submitter_policy_binding"`
	SubmitterKey    string `json:"submitter_key_binding"`
	Commands        string `json:"commands"`
	ControlPath     string `json:"control_path"`
	JournalPath     string `json:"journal_path"`
	PathSeparation  string `json:"path_separation"`
	Providers       string `json:"providers"`
	MCPInputs       string `json:"mcp_inputs"`
	PriceSource     string `json:"price_source"`
	Clock           string `json:"clock"`
}

type preflightProviderSet struct {
	mithril *solanarpc.Client
}

func runPreflight(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("preflight", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "agent config JSON")
	explain := flags.Bool("explain", false, "also print a plain-language report of failing checks")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := io.WriteString(
				output,
				"Usage: mithril-agent preflight --config PATH [--explain]\n",
			)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("preflight takes no positional arguments")
	}
	if *configPath == "" {
		if *configPath = discoverCurrentConfig(); *configPath == "" {
			return errors.New("preflight found no configuration; run: mithril-agent setup")
		}
	}

	summary := checkPreflight(*configPath)
	if err := writeJSONSummary(output, summary); err != nil {
		return err
	}
	if summary.Status != preflightOK {
		// The JSON above is a deliberately bounded machine contract, so the
		// human report is opt-in rather than appended to it.
		if *explain {
			if err := explainPreflight(output, preflightResultMap(summary.Checks)); err != nil {
				return err
			}
		}
		return errPreflightFailed
	}
	return nil
}

func checkPreflight(configPath string) preflightSummary {
	return checkPreflightWithSigner(configPath, "")
}

func checkPreflightWithSigner(configPath, signerSocket string) preflightSummary {
	return checkPreflightWithSockets(configPath, signerSocket, "", "")
}

func checkPreflightWithSockets(configPath, signerSocket, riskSocket, submitterSocket string) preflightSummary {
	summary := preflightSummary{
		Status: preflightFailed,
		Checks: preflightChecks{
			Config:          preflightSkipped,
			Profile:         preflightSkipped,
			PolicyBinding:   preflightSkipped,
			KeypairBinding:  preflightSkipped,
			RiskPolicy:      preflightSkipped,
			RiskKeypair:     preflightSkipped,
			SubmitterPolicy: preflightSkipped,
			SubmitterKey:    preflightSkipped,
			Commands:        preflightSkipped,
			ControlPath:     preflightSkipped,
			JournalPath:     preflightSkipped,
			PathSeparation:  preflightSkipped,
			Providers:       preflightSkipped,
			MCPInputs:       preflightSkipped,
			PriceSource:     preflightSkipped,
			Clock:           preflightSkipped,
		},
	}

	var cfg config
	configValid := readStrictJSON(configPath, &cfg) == nil &&
		validatePrivateTarget(configPath) == nil &&
		cfg.validateProfileSelection() == nil
	summary.Checks.Config = checkStatus(configValid)

	var fingerprint string
	profileValid := false
	activeCluster := ""
	activeSource := ""
	maxClockUncertainty := clockcheck.InitialMaxUncertainty
	if configValid {
		var err error
		if cfg.Swap != nil {
			fingerprint, err = cfg.Swap.Fingerprint()
			profileValid = err == nil && cfg.Swap.Cluster == "devnet" &&
				(cfg.Swap.PriceTrigger == nil || priceSourcePolicyMatches(*cfg.Swap.PriceTrigger))
			activeCluster = cfg.Swap.Cluster
			activeSource = cfg.Swap.Owner()
			maxClockUncertainty = cfg.Swap.ClockUncertaintyLimit()
		} else {
			fingerprint, err = cfg.Profile.Fingerprint()
			profileValid = err == nil && cfg.Profile.Cluster == "devnet"
			activeCluster = cfg.Profile.Cluster
			activeSource = cfg.Profile.Source
			maxClockUncertainty = cfg.Profile.ClockUncertaintyLimit()
		}
		summary.Checks.Profile = checkStatus(profileValid)
	}

	if profileValid {
		priceSourceValid := true
		if cfg.Swap != nil && cfg.Swap.PriceTrigger != nil {
			// The on-chain push feed reads through the operator's own node, so
			// it needs no credential. Only the Hermes adapter requires a key.
			if cfg.Swap.PriceTrigger.PrimarySourceSHA256 == pricesource.PythPushIdentitySHA256() {
				priceSourceValid = true
			} else {
				_, priceSourceErr := pricesource.NewPyth(
					nil,
					os.Getenv("MITHRIL_AGENT_PYTH_API_KEY"),
				)
				priceSourceValid = priceSourceErr == nil
			}
		}
		summary.Checks.PriceSource = checkStatus(priceSourceValid)
	}

	var policy signer.Policy
	policyValid := false
	var riskPolicy policyauthority.Policy
	riskPolicyValid := false
	var submitterPolicy submitter.Policy
	submitterPolicyValid := false
	signerKeyPathValid := false
	riskKeyPathValid := false
	submitterKeyPathValid := false
	if profileValid {
		policyValid = readStrictJSON(cfg.Signer.PolicyPath, &policy) == nil &&
			validatePrivateTarget(cfg.Signer.PolicyPath) == nil &&
			policy.ValidateAuthorizationPolicy() == nil &&
			((cfg.Swap == nil && policyMatchesProfile(policy, cfg.Profile, fingerprint)) ||
				(cfg.Swap != nil && policyMatchesSwap(policy, *cfg.Swap, fingerprint))) &&
			validatePrivateTarget(
				policy.AuthorizationLedgerPath,
				policy.AuthorizationLedgerPath+".lock",
				policy.AuthorizationLedgerPath+".reserve",
			) == nil
		summary.Checks.PolicyBinding = checkStatus(policyValid)

		signerKeyPathValid = signerSocket != "" || validatePrivateTarget(cfg.Signer.KeypairPath) == nil
		if !signerKeyPathValid {
			summary.Checks.KeypairBinding = preflightFailed
		}

		riskPolicyValid = readStrictJSON(cfg.Policy.PolicyPath, &riskPolicy) == nil &&
			validatePrivateTarget(cfg.Policy.PolicyPath) == nil &&
			riskPolicy.Validate() == nil &&
			signerPoliciesEqual(riskPolicy.TransactionPolicy, policy) &&
			cfg.Policy.KeyID == policy.RiskAuthorityKeyID &&
			cfg.Policy.PublicKey == policy.RiskAuthorityPublicKey
		summary.Checks.RiskPolicy = checkStatus(riskPolicyValid)
		riskKeyPathValid = riskSocket != "" || validatePrivateTarget(cfg.Policy.KeypairPath) == nil
		if !riskKeyPathValid {
			summary.Checks.RiskKeypair = preflightFailed
		}

		if submitterSocket == "" {
			submitterPolicyValid = readStrictJSON(
				cfg.Submitter.PolicyPath,
				&submitterPolicy,
			) == nil &&
				validatePrivateTarget(cfg.Submitter.PolicyPath) == nil &&
				submitterPolicy.Validate() == nil &&
				submitterPolicyMatchesSigner(submitterPolicy, policy, cfg) &&
				submitterPolicy.ControlStatePath == cfg.Control.StatePath
		} else {
			submitterPolicyValid = true
			submitterPolicy.SubmitterPublicKey = policy.SubmitterPublicKey
		}
		summary.Checks.SubmitterPolicy = checkStatus(submitterPolicyValid)
		submitterKeyPathValid = submitterSocket != "" || validatePrivateTarget(cfg.Submitter.PrivateKeyPath) == nil
		if !submitterKeyPathValid {
			summary.Checks.SubmitterKey = preflightFailed
		}
	}

	var providers preflightProviderSet
	var providersValid bool
	providers, providersValid = validatePreflightProviders(cfg)
	summary.Checks.Providers = checkStatus(providersValid)
	if configValid {
		summary.Checks.MCPInputs = checkStatus(validateMCPInputs(cfg.MCP.Args))
	}

	if configValid && profileValid && providersValid {
		commandsValid := validateCommandArgs(cfg.MCP.Args) &&
			validateExecutable(cfg.MCP.Command) == nil &&
			(submitterSocket != "" || validateExecutable(cfg.Submitter.Command) == nil)
		if riskSocket == "" {
			commandsValid = commandsValid && validateExecutable(cfg.Policy.Command) == nil
		}
		if signerSocket == "" {
			commandsValid = commandsValid && validateExecutable(cfg.Signer.Command) == nil
		}
		var quoteErr error
		if commandsValid && cfg.Swap != nil {
			_, quoteErr = swapbuilder.New(quoteBuilderConfig(cfg))
		}
		if commandsValid {
			_, mcpErr := mcpobserve.New(mcpobserve.Config{
				Command: cfg.MCP.Command,
				Args:    cfg.MCP.Args,
				Env: mithrilMCPEnvironment(
					os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL"),
					os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL"),
				),
				Cluster:   activeCluster,
				RPCOrigin: providers.mithril.Origin(),
			}, nil)
			_, signerErr := signerclient.New(signerclient.Config{
				Command:     cfg.Signer.Command,
				PolicyPath:  cfg.Signer.PolicyPath,
				KeypairPath: cfg.Signer.KeypairPath,
				SocketPath:  signerSocket,
			})
			_, policyErr := policyclient.New(policyclient.Config{
				Command:     cfg.Policy.Command,
				PolicyPath:  cfg.Policy.PolicyPath,
				KeypairPath: cfg.Policy.KeypairPath,
				SocketPath:  riskSocket,
				KeyID:       cfg.Policy.KeyID,
				PublicKey:   cfg.Policy.PublicKey,
			})
			_, submitterErr := submitterclient.New(submitterclient.Config{
				Command:        cfg.Submitter.Command,
				PolicyPath:     cfg.Submitter.PolicyPath,
				PrivateKeyPath: cfg.Submitter.PrivateKeyPath,
				SocketPath:     submitterSocket,
				Env: []string{
					"MITHRIL_AGENT_MITHRIL_RPC_URL=" + os.Getenv(
						"MITHRIL_AGENT_MITHRIL_RPC_URL",
					),
				},
			})
			commandsValid = mcpErr == nil && signerErr == nil &&
				policyErr == nil && submitterErr == nil && quoteErr == nil
		}
		summary.Checks.Commands = checkStatus(commandsValid)
		if commandsValid && policyValid && riskPolicyValid && submitterPolicyValid &&
			signerKeyPathValid && riskKeyPathValid && submitterKeyPathValid {
			identityCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			var signerIdentityErr error
			if signerSocket == "" {
				signerIdentityErr = preflightSignerIdentity(
					identityCtx,
					cfg.Signer.Command,
					cfg.Signer.PolicyPath,
					cfg.Signer.KeypairPath,
					activeSource,
				)
			} else {
				signerIdentityErr = preflightSocketIdentity(identityCtx, signerSocket, activeSource)
			}
			var riskIdentityErr error
			if riskSocket == "" {
				riskIdentityErr = preflightRiskIdentity(
					identityCtx,
					cfg.Policy.Command,
					cfg.Policy.PolicyPath,
					cfg.Policy.KeypairPath,
					cfg.Policy.KeyID,
					cfg.Policy.PublicKey,
				)
			} else {
				riskIdentityErr = preflightRiskSocketIdentity(
					identityCtx, riskSocket, cfg.Policy.KeyID, cfg.Policy.PublicKey,
				)
			}
			var submitterIdentityErr error
			if submitterSocket == "" {
				submitterIdentityErr = preflightSubmitIdentity(
					identityCtx, cfg.Submitter.Command, cfg.Submitter.PolicyPath,
					cfg.Submitter.PrivateKeyPath, submitterPolicy.SubmitterPublicKey,
				)
			} else {
				client, clientErr := submitterclient.New(submitterclient.Config{SocketPath: submitterSocket})
				if clientErr != nil {
					submitterIdentityErr = clientErr
				} else if identity, identityErr := client.Identity(identityCtx); identityErr != nil ||
					identity.PublicKey != policy.SubmitterPublicKey ||
					identity.ProfileFingerprint != fingerprint || identity.Source != activeSource {
					submitterIdentityErr = errors.New("submitter identity does not match policy")
				}
			}
			cancel()
			summary.Checks.KeypairBinding = checkStatus(signerIdentityErr == nil)
			summary.Checks.RiskKeypair = checkStatus(riskIdentityErr == nil)
			summary.Checks.SubmitterKey = checkStatus(submitterIdentityErr == nil)
		}
	}

	if configValid {
		journalValid := validatePrivateTarget(
			cfg.Journal.Path,
			cfg.Journal.Path+".reserve",
			operatorstatus.Path(cfg.Journal.Path),
		) == nil
		summary.Checks.JournalPath = checkStatus(journalValid)
	}

	if profileValid {
		_, stateErr := control.NewStateFile(
			cfg.Control.StatePath,
			fingerprint,
			true,
		)
		controlValid := stateErr == nil &&
			validatePrivateTarget(
				cfg.Control.StatePath,
				cfg.Control.StatePath+".lock",
			) == nil
		summary.Checks.ControlPath = checkStatus(controlValid)

		if configValid && policyValid && riskPolicyValid && submitterPolicyValid && !preflightPathsDistinct(
			cfg,
			configPath,
			policy.AuthorizationLedgerPath,
		) {
			summary.Checks.PathSeparation = preflightFailed
		} else if configValid && policyValid && riskPolicyValid && submitterPolicyValid {
			summary.Checks.PathSeparation = preflightOK
		}

		sample, err := preflightClockSample()
		now := time.Now().UTC()
		clockValid := preflightOperatingSystem == "linux" && err == nil &&
			!sample.WallTime.IsZero() && len(sample.BootID) == 36 &&
			sample.MonotonicNanos != 0 &&
			sample.OffsetNanos >= -int64(clockcheck.MaxOffset) &&
			sample.OffsetNanos <= int64(clockcheck.MaxOffset) &&
			sample.UncertaintyNanos <= uint64(maxClockUncertainty) &&
			!sample.WallTime.After(now.Add(clockcheck.MaxOffset)) &&
			now.Sub(sample.WallTime) <= clockcheck.MaxSampleAge
		summary.Checks.Clock = checkStatus(clockValid)
	}

	if allPreflightChecksOK(summary.Checks) {
		summary.Status = preflightOK
	}
	return summary
}

func writeJSONSummary(output io.Writer, summary preflightSummary) error {
	return json.NewEncoder(output).Encode(summary)
}

func checkStatus(ok bool) string {
	if ok {
		return preflightOK
	}
	return preflightFailed
}

func allPreflightChecksOK(checks preflightChecks) bool {
	return checks.Config == preflightOK &&
		checks.Profile == preflightOK &&
		checks.PolicyBinding == preflightOK &&
		checks.KeypairBinding == preflightOK &&
		checks.RiskPolicy == preflightOK &&
		checks.RiskKeypair == preflightOK &&
		checks.SubmitterPolicy == preflightOK &&
		checks.SubmitterKey == preflightOK &&
		checks.Commands == preflightOK &&
		checks.ControlPath == preflightOK &&
		checks.JournalPath == preflightOK &&
		checks.PathSeparation == preflightOK &&
		checks.Providers == preflightOK &&
		checks.MCPInputs == preflightOK &&
		checks.PriceSource == preflightOK &&
		checks.Clock == preflightOK
}

func validateMCPInputs(args []string) bool {
	for index, arg := range args {
		if arg == "--config" {
			return index+1 < len(args) &&
				validatePrivateTarget(args[index+1]) == nil &&
				readableRegularFile(args[index+1])
		}
	}
	statePath := os.Getenv("MITHRIL_STATE_PATH")
	if statePath == "" {
		accountsPath := os.Getenv("MITHRIL_ACCOUNTS_PATH")
		if accountsPath == "" {
			return false
		}
		statePath = filepath.Join(accountsPath, "mithril_state.json")
	}
	return readableRegularFile(statePath)
}

func readableRegularFile(path string) bool {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	return file.Close() == nil
}

func signerPoliciesEqual(left, right signer.Policy) bool {
	if (left.OrcaSwap == nil) != (right.OrcaSwap == nil) ||
		(left.OrcaBuy == nil) != (right.OrcaBuy == nil) {
		return false
	}
	if left.OrcaSwap != nil && *left.OrcaSwap != *right.OrcaSwap {
		return false
	}
	if left.OrcaBuy != nil && *left.OrcaBuy != *right.OrcaBuy {
		return false
	}
	left.OrcaSwap = nil
	right.OrcaSwap = nil
	left.OrcaBuy = nil
	right.OrcaBuy = nil
	return left == right
}

func submitterPolicyMatchesSigner(
	policy submitter.Policy,
	signerPolicy signer.Policy,
	cfg config,
) bool {
	if policy.Evidence != providerBindings(cfg) {
		return false
	}
	if policy.Cluster != signerPolicy.Cluster ||
		policy.Source != signerPolicy.Source ||
		policy.ProfileFingerprint != signerPolicy.ProfileFingerprint ||
		policy.Destination != signerPolicy.Destination ||
		policy.MaxLamports != signerPolicy.MaxLamports ||
		policy.MaxInputTokenAmount != signerPolicy.MaxInputTokenAmount ||
		policy.MaxFeeLamports != signerPolicy.MaxFeeLamports ||
		policy.SubmitterPublicKey != signerPolicy.SubmitterPublicKey ||
		(policy.OrcaSwap == nil) != (signerPolicy.OrcaSwap == nil) ||
		(policy.OrcaBuy == nil) != (signerPolicy.OrcaBuy == nil) {
		return false
	}
	if policy.OrcaSwap != nil {
		return policy.Profile == signerPolicy.Profile &&
			*policy.OrcaSwap == *signerPolicy.OrcaSwap
	}
	if policy.OrcaBuy != nil {
		return policy.Profile == signerPolicy.Profile &&
			*policy.OrcaBuy == *signerPolicy.OrcaBuy
	}
	if signerPolicy.OrcaSwap == nil && signerPolicy.OrcaBuy == nil {
		return policy.Profile == "" || policy.Profile == signerPolicy.Profile
	}
	return false
}

func policyMatchesSwap(policy signer.Policy, profile swaprun.Profile, fingerprint string) bool {
	if profile.IsBuy() {
		return profile.BuyRoute != nil && policy.Cluster == profile.Cluster &&
			policy.Profile == profile.Name && policy.ProfileVersion == profile.Version &&
			policy.ProfileFingerprint == fingerprint && policy.Source == profile.Owner() &&
			policy.Destination == "" && policy.MaxLamports == 0 &&
			policy.MaxInputTokenAmount == profile.InputTokenAmount &&
			policy.MaxFeeLamports == profile.MaxFeeLamports &&
			policy.DailyDebitCapLamports == 0 &&
			policy.DailyInputTokenCap == profile.DailyInputTokenCap &&
			policy.DailyNativeFeeCapLamports == profile.DailyNativeFeeCapLamports &&
			policy.ScheduleWindowSeconds == profile.ScheduleWindowSeconds &&
			policy.ScheduleAnchorUnix == profile.ScheduleAnchorUnix &&
			policy.MaxBlockHeightWindow == profile.MaxBlockHeightWindow &&
			policy.OrcaSwap == nil && policy.OrcaBuy != nil &&
			*policy.OrcaBuy == *profile.BuyRoute
	}
	return policy.Cluster == profile.Cluster && policy.Profile == profile.Name &&
		policy.ProfileVersion == profile.Version && policy.ProfileFingerprint == fingerprint &&
		policy.Source == profile.Route.Owner && policy.Destination == "" &&
		policy.MaxLamports == profile.InputLamports &&
		policy.MaxFeeLamports == profile.MaxFeeLamports &&
		policy.DailyDebitCapLamports == profile.DailyDebitCapLamports &&
		policy.ScheduleWindowSeconds == profile.ScheduleWindowSeconds &&
		policy.ScheduleAnchorUnix == profile.ScheduleAnchorUnix &&
		policy.MaxBlockHeightWindow == profile.MaxBlockHeightWindow &&
		policy.MaxInputTokenAmount == 0 && policy.DailyInputTokenCap == 0 &&
		policy.DailyNativeFeeCapLamports == 0 && policy.OrcaBuy == nil &&
		policy.OrcaSwap != nil && *policy.OrcaSwap == profile.Route
}

func policyMatchesProfile(
	policy signer.Policy,
	profile agent.Profile,
	fingerprint string,
) bool {
	return policy.Cluster == profile.Cluster &&
		policy.Profile == profile.Name &&
		policy.ProfileVersion == profile.Version &&
		policy.ProfileFingerprint == fingerprint &&
		policy.Source == profile.Source &&
		policy.Destination == profile.Destination &&
		policy.MaxLamports == profile.MaxTransferLamports &&
		policy.MaxFeeLamports == profile.MaxFeeLamports &&
		policy.DailyDebitCapLamports == profile.DailyCapLamports &&
		policy.ScheduleWindowSeconds == profile.ScheduleWindowSeconds &&
		policy.ScheduleAnchorUnix == profile.ScheduleAnchorUnix
}

func validatePreflightProviders(cfg config) (preflightProviderSet, bool) {
	mithrilURL, mithrilSet := os.LookupEnv("MITHRIL_AGENT_MITHRIL_RPC_URL")
	primaryURL, primarySet := os.LookupEnv("MITHRIL_AGENT_PRIMARY_RPC_URL")
	secondaryURL, secondarySet := os.LookupEnv("MITHRIL_AGENT_SECONDARY_RPC_URL")
	if !mithrilSet || !primarySet || !secondarySet ||
		mithrilURL == "" || primaryURL == "" || secondaryURL == "" {
		return preflightProviderSet{}, false
	}
	providers, err := openBoundRPCProviders(cfg, mithrilURL, primaryURL, secondaryURL)
	if err != nil {
		return preflightProviderSet{}, false
	}
	return preflightProviderSet{
		mithril: providers.mithril,
	}, true
}

func validateExecutable(path string) error {
	return secureexec.ValidateExecutable(path)
}

func validateCommandArgs(args []string) bool {
	for _, arg := range args {
		if strings.ContainsRune(arg, '\x00') {
			return false
		}
	}
	return true
}

func validatePrivateTarget(path string, sidecars ...string) error {
	if path == "" || !filepath.IsAbs(path) || path != filepath.Clean(path) {
		return errors.New("path is not absolute and clean")
	}
	if err := validateSafeParent(filepath.Dir(path), 0o022); err != nil {
		return err
	}
	for _, target := range append([]string{path}, sidecars...) {
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 ||
			!info.Mode().IsRegular() ||
			info.Mode().Perm()&0o077 != 0 ||
			!fileowner.Trusted(info) {
			return errors.New("path target is unsafe")
		}
	}
	return nil
}

func validateSafeParent(path string, forbidden os.FileMode) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("path directory is unsafe")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 ||
		!info.IsDir() ||
		info.Mode().Perm()&forbidden != 0 {
		return errors.New("path directory is unsafe")
	}
	return secureexec.ValidateProtectedDirectory(path)
}

func preflightPathsDistinct(
	cfg config,
	configPath,
	authorizationLedgerPath string,
) bool {
	protected := map[string]bool{}
	for _, path := range []string{
		configPath,
		cfg.Signer.PolicyPath,
		cfg.Signer.KeypairPath,
		cfg.Policy.PolicyPath,
		cfg.Policy.KeypairPath,
		cfg.Submitter.PolicyPath,
		cfg.Submitter.PrivateKeyPath,
		cfg.MCP.Command,
		cfg.Policy.Command,
		cfg.Signer.Command,
		cfg.Submitter.Command,
		cfg.Quote.Command,
		cfg.Quote.ScriptPath,
		cfg.Quote.SocketPath,
	} {
		if path != "" {
			protected[filepath.Clean(path)] = true
		}
	}
	mutable := []string{
		cfg.Control.StatePath,
		cfg.Control.StatePath + ".lock",
		authorizationLedgerPath,
		authorizationLedgerPath + ".lock",
		authorizationLedgerPath + ".reserve",
		cfg.Journal.Path,
		cfg.Journal.Path + ".reserve",
		operatorstatus.Path(cfg.Journal.Path),
	}
	seen := map[string]bool{}
	for _, path := range mutable {
		if path == "" {
			return false
		}
		path = filepath.Clean(path)
		if protected[path] || seen[path] {
			return false
		}
		seen[path] = true
	}
	return true
}
