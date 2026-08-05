package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/swaprun"
)

type rpcProviders struct {
	mithril   *solanarpc.Client
	primary   *solanarpc.Client
	secondary *solanarpc.Client
}

const evidenceRequestInterval = 750 * time.Millisecond

var (
	newExternalRPC = func(endpoint string) (*solanarpc.Client, error) {
		return solanarpc.New(endpoint, nil, false)
	}
	newPacedExternalRPC = func(endpoint string, interval time.Duration) (*solanarpc.Client, error) {
		return solanarpc.NewPaced(endpoint, nil, interval)
	}
	bindProvidersAfterInitialConfigRead = func() {}
)

func openEvidenceProviders(primaryURL, secondaryURL string) (
	*solanarpc.Client,
	*solanarpc.Client,
	error,
) {
	if primaryURL == "" || secondaryURL == "" {
		return nil, nil, errors.New("two independent evidence RPC URLs are required")
	}
	primary, err := newPacedExternalRPC(primaryURL, evidenceRequestInterval)
	if err != nil {
		return nil, nil, fmt.Errorf("primary evidence RPC: %w", err)
	}
	secondary, err := newPacedExternalRPC(secondaryURL, evidenceRequestInterval)
	if err != nil {
		return nil, nil, fmt.Errorf("secondary evidence RPC: %w", err)
	}
	if primary.Identity() == secondary.Identity() {
		return nil, nil, errors.New("evidence RPC origins must be distinct")
	}
	return primary, secondary, nil
}

func openBoundRPCProviders(
	cfg config,
	mithrilURL,
	primaryURL,
	secondaryURL string,
) (rpcProviders, error) {
	providers, err := openUnboundRPCProviders(mithrilURL, primaryURL, secondaryURL)
	if err != nil {
		return rpcProviders{}, err
	}
	if cfg.Swap != nil {
		if err := cfg.validateEvidenceTrustDomains(); err != nil {
			return rpcProviders{}, err
		}
		if err := validateEvidenceOriginBindings(cfg, providers.primary, providers.secondary); err != nil {
			return rpcProviders{}, err
		}
	}
	return providers, nil
}

func openUnboundRPCProviders(mithrilURL, primaryURL, secondaryURL string) (rpcProviders, error) {
	if mithrilURL == "" {
		return rpcProviders{}, errors.New("Mithril RPC URL is required")
	}
	mithril, err := solanarpc.NewMithrilNode(mithrilURL, nil)
	if err != nil {
		return rpcProviders{}, fmt.Errorf("Mithril RPC: %w", err)
	}
	primary, secondary, err := openEvidenceProviders(primaryURL, secondaryURL)
	if err != nil {
		return rpcProviders{}, err
	}
	if primary.Identity() == mithril.Identity() || secondary.Identity() == mithril.Identity() {
		return rpcProviders{}, errors.New("Mithril and evidence RPC origins must be distinct")
	}
	return rpcProviders{mithril: mithril, primary: primary, secondary: secondary}, nil
}

type providerBindingResult struct {
	Status                 string `json:"status"`
	PrimaryTrustDomain     string `json:"primary_trust_domain"`
	PrimaryOriginSHA256    string `json:"primary_origin_sha256"`
	SecondaryTrustDomain   string `json:"secondary_trust_domain"`
	SecondaryOriginSHA256  string `json:"secondary_origin_sha256"`
	Reason                 string `json:"reason"`
	ServiceRestartRequired bool   `json:"service_restart_required"`
}

func runSwapBindProviders(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("swap bind-providers", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "private agent configuration")
	primaryTrust := flags.String("primary-trust-domain", "", "primary evidence trust domain")
	secondaryTrust := flags.String("secondary-trust-domain", "", "secondary evidence trust domain")
	reason := flags.String("reason", "", "operator reason")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(
				output,
				"Usage: mithril-agent swap bind-providers --config PATH --reason TEXT [--primary-trust-domain NAME] [--secondary-trust-domain NAME]",
			)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *reason == "" {
		return errors.New("bind-providers requires --config and --reason")
	}

	initialCfg, err := readConfig(*configPath)
	if err != nil {
		return errors.New("read agent configuration")
	}
	if initialCfg.Swap == nil || initialCfg.Swap.Cluster != "devnet" {
		return errors.New("bind-providers is limited to the Devnet swap profile")
	}
	initialFingerprint, err := initialCfg.Swap.Fingerprint()
	if err != nil {
		return errors.New("validate swap profile")
	}
	bindProvidersAfterInitialConfigRead()
	store, err := journal.OpenRotating(initialCfg.Journal.Path)
	if err != nil {
		return errors.New("open audit journal; stop the swap runner before binding providers")
	}
	closed := false
	defer func() {
		if !closed {
			_ = store.Close()
		}
	}()
	cfg, err := readConfig(*configPath)
	if err != nil {
		return errors.New("reread agent configuration after locking journal")
	}
	if cfg.Swap == nil || cfg.Swap.Cluster != "devnet" {
		return errors.New("bind-providers is limited to the Devnet swap profile")
	}
	fingerprint, err := cfg.Swap.Fingerprint()
	if err != nil {
		return errors.New("validate locked swap profile")
	}
	if cfg.Journal.Path != initialCfg.Journal.Path ||
		cfg.Control.StatePath != initialCfg.Control.StatePath ||
		fingerprint != initialFingerprint {
		return errors.New("agent configuration changed while binding providers")
	}
	state, err := control.NewStateFile(cfg.Control.StatePath, fingerprint, false)
	if err != nil {
		return errors.New("open control state")
	}
	status, err := state.Status()
	if err != nil || status.Mode != control.ModeNoNewActions || status.RecoveryPending ||
		status.TerminalActionID != "" {
		return errors.New("bind-providers requires stopped control state with no unresolved action")
	}
	if err := swaprun.ValidateNoUnresolvedActions(store); err != nil {
		return errors.New("bind-providers requires a journal with no unresolved action")
	}
	if err := state.Stop(*reason); err != nil {
		return errors.New("record stopped control state")
	}

	providers, err := openUnboundRPCProviders(
		os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL"),
		os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL"),
		os.Getenv("MITHRIL_AGENT_SECONDARY_RPC_URL"),
	)
	if err != nil {
		return errors.New("validate RPC providers")
	}
	if *primaryTrust != "" {
		cfg.Evidence.PrimaryTrustDomain = *primaryTrust
	}
	if *secondaryTrust != "" {
		cfg.Evidence.SecondaryTrustDomain = *secondaryTrust
	}
	if err := cfg.validateEvidenceTrustDomains(); err != nil {
		return err
	}
	cfg.Evidence.PrimaryOriginSHA256 = providers.primary.Identity()
	cfg.Evidence.SecondaryOriginSHA256 = providers.secondary.Identity()

	payload := providerBindingResult{
		Status:                 "provider_binding_requested",
		PrimaryTrustDomain:     cfg.Evidence.PrimaryTrustDomain,
		PrimaryOriginSHA256:    providers.primary.Identity(),
		SecondaryTrustDomain:   cfg.Evidence.SecondaryTrustDomain,
		SecondaryOriginSHA256:  providers.secondary.Identity(),
		Reason:                 *reason,
		ServiceRestartRequired: true,
	}
	if _, err := store.Append(time.Now().UTC(), "provider_binding_requested", "", payload); err != nil {
		return errors.New("record provider binding request")
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return errors.New("encode agent configuration")
	}
	encoded = append(encoded, '\n')
	if err := securefile.ReplacePrivate(*configPath, encoded, maxInputBytes); err != nil {
		return errors.New("replace agent configuration")
	}
	payload.Status = "providers_bound"
	if _, err := store.Append(time.Now().UTC(), "providers_bound", "", payload); err != nil {
		return errors.New("record completed provider binding")
	}
	if err := store.Close(); err != nil {
		return errors.New("close audit journal")
	}
	closed = true
	return json.NewEncoder(output).Encode(payload)
}

func validateEvidenceOriginBindings(
	cfg config,
	primary,
	secondary *solanarpc.Client,
) error {
	if !validSHA256(cfg.Evidence.PrimaryOriginSHA256) ||
		!validSHA256(cfg.Evidence.SecondaryOriginSHA256) {
		return errors.New("evidence RPC origin bindings are missing or invalid")
	}
	if cfg.Evidence.PrimaryOriginSHA256 != primary.Identity() ||
		cfg.Evidence.SecondaryOriginSHA256 != secondary.Identity() {
		return errors.New("evidence RPC origins do not match setup")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
