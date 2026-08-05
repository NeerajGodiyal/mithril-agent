package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/swaprun"
)

const (
	testMithrilRPC     = "http://127.0.0.1:8899"
	testPrimaryRPC     = "https://rpc-one.invalid/v1?api-key=secret-one"
	testSecondaryRPC   = "https://rpc-two.invalid/v1?api-key=secret-two"
	testPrimaryRotated = "https://rpc-one.invalid/v2?api-key=rotated-one"
	testSecondRotated  = "https://rpc-two.invalid/v2?api-key=rotated-two"
)

func boundProviderConfig(t *testing.T) config {
	t.Helper()
	cfg := config{Swap: &swaprun.Profile{}}
	cfg.Evidence.PrimaryTrustDomain = "provider-one"
	cfg.Evidence.PrimaryOriginSHA256 = testRPCIdentity(t, testPrimaryRPC)
	cfg.Evidence.SecondaryTrustDomain = "provider-two"
	cfg.Evidence.SecondaryOriginSHA256 = testRPCIdentity(t, testSecondaryRPC)
	return cfg
}

func TestProviderBindingAllowsCredentialRotationAtSameOrigin(t *testing.T) {
	cfg := boundProviderConfig(t)
	providers, err := openBoundRPCProviders(
		cfg, testMithrilRPC, testPrimaryRotated, testSecondRotated,
	)
	if err != nil {
		t.Fatal(err)
	}
	if providers.primary.Identity() != cfg.Evidence.PrimaryOriginSHA256 ||
		providers.secondary.Identity() != cfg.Evidence.SecondaryOriginSHA256 {
		t.Fatal("credential rotation changed a canonical provider identity")
	}
}

func TestProviderBindingRejectsOriginChanges(t *testing.T) {
	cfg := boundProviderConfig(t)
	tests := []struct {
		name      string
		primary   string
		secondary string
	}{
		{
			name:      "primary host drift",
			primary:   "https://rpc-three.invalid/v1?api-key=secret-one",
			secondary: testSecondaryRPC,
		},
		{
			name:      "primary port drift",
			primary:   "https://rpc-one.invalid:8443/v1?api-key=secret-one",
			secondary: testSecondaryRPC,
		},
		{
			name:      "swapped origins",
			primary:   testSecondaryRPC,
			secondary: testPrimaryRPC,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := openBoundRPCProviders(
				cfg, testMithrilRPC, test.primary, test.secondary,
			)
			if err == nil || !strings.Contains(err.Error(), "do not match setup") {
				t.Fatalf("origin change error = %v", err)
			}
		})
	}
}

func TestProviderBindingRejectsMissingAndMalformedHashes(t *testing.T) {
	tests := []struct {
		name      string
		secondary bool
		value     string
	}{
		{name: "missing primary"},
		{name: "missing secondary", secondary: true},
		{name: "short", value: "0123"},
		{name: "not hex", value: strings.Repeat("z", 64)},
		{name: "uppercase", value: strings.ToUpper(testRPCIdentity(t, testPrimaryRPC))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := boundProviderConfig(t)
			if test.secondary {
				cfg.Evidence.SecondaryOriginSHA256 = test.value
			} else {
				cfg.Evidence.PrimaryOriginSHA256 = test.value
			}
			_, err := openBoundRPCProviders(
				cfg, testMithrilRPC, testPrimaryRPC, testSecondaryRPC,
			)
			if err == nil || !strings.Contains(err.Error(), "missing or invalid") {
				t.Fatalf("invalid binding error = %v", err)
			}
		})
	}
}

func TestProviderBindingIsEnforcedByPreflightAndSwapRuntime(t *testing.T) {
	cfg := boundProviderConfig(t)
	t.Setenv("MITHRIL_AGENT_MITHRIL_RPC_URL", testMithrilRPC)
	t.Setenv("MITHRIL_AGENT_PRIMARY_RPC_URL", "https://rpc-three.invalid/v1")
	t.Setenv("MITHRIL_AGENT_SECONDARY_RPC_URL", testSecondaryRPC)
	t.Setenv("MITHRIL_AGENT_QUOTE_RPC_URL", "https://quote.invalid/v1")

	if _, valid := validatePreflightProviders(cfg); valid {
		t.Fatal("preflight accepted an unbound primary provider origin")
	}
	if _, err := openSwapDependencies(cfg); err == nil ||
		!strings.Contains(err.Error(), "do not match setup") {
		t.Fatalf("swap runtime origin error = %v", err)
	}
}

func TestBindProvidersRequiresStoppedStateAndRecordsMigration(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	seed := sha256.Sum256([]byte(t.Name()))
	wallet := ed25519.NewKeyFromSeed(seed[:])
	profile := testSwapProfile(solana.Encode(wallet.Public().(ed25519.PublicKey)))
	cfg := config{Swap: &profile}
	cfg.Control.StatePath = filepath.Join(stateDir, "control.json")
	cfg.Journal.Path = filepath.Join(stateDir, "events.jsonl")
	cfg.Evidence.PrimaryTrustDomain = "provider-one"
	cfg.Evidence.SecondaryTrustDomain = "provider-two"
	configPath := filepath.Join(root, "config.json")
	writeJSON(t, configPath, cfg)

	t.Setenv("MITHRIL_AGENT_MITHRIL_RPC_URL", testMithrilRPC)
	t.Setenv("MITHRIL_AGENT_PRIMARY_RPC_URL", testPrimaryRPC)
	t.Setenv("MITHRIL_AGENT_SECONDARY_RPC_URL", testSecondaryRPC)
	var output bytes.Buffer
	if err := runSwapBindProviders([]string{
		"--config", configPath,
		"--reason", "bind upgraded provider configuration",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result providerBindingResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "providers_bound" || !result.ServiceRestartRequired ||
		result.PrimaryOriginSHA256 != testRPCIdentity(t, testPrimaryRPC) ||
		result.SecondaryOriginSHA256 != testRPCIdentity(t, testSecondaryRPC) {
		t.Fatalf("binding result = %+v", result)
	}
	var rebound config
	if err := readStrictJSON(configPath, &rebound); err != nil {
		t.Fatal(err)
	}
	if rebound.Evidence.PrimaryTrustDomain != "provider-one" ||
		rebound.Evidence.SecondaryTrustDomain != "provider-two" ||
		rebound.Evidence.PrimaryOriginSHA256 != result.PrimaryOriginSHA256 ||
		rebound.Evidence.SecondaryOriginSHA256 != result.SecondaryOriginSHA256 {
		t.Fatalf("rebound evidence configuration = %+v", rebound.Evidence)
	}
	store, err := journal.Open(cfg.Journal.Path)
	if err != nil {
		t.Fatal(err)
	}
	records := store.Records()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Type != "provider_binding_requested" ||
		records[1].Type != "providers_bound" {
		t.Fatalf("provider binding audit records = %+v", records)
	}

	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := control.WriteDevnetActivation(
		cfg.Control.StatePath, fingerprint, now, now.Add(time.Hour), 1, "test activation",
	); err != nil {
		t.Fatal(err)
	}
	if err := runSwapBindProviders([]string{
		"--config", configPath,
		"--primary-trust-domain", "provider-one",
		"--secondary-trust-domain", "provider-two",
		"--reason", "must remain stopped",
	}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "requires stopped") {
		t.Fatalf("enabled-state binding error = %v", err)
	}

	state, err := control.NewStateFile(cfg.Control.StatePath, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	actionID := strings.Repeat("a", 64)
	blocked, err := state.WithSendBarrier(actionID, func() error { return nil })
	if err != nil || blocked {
		t.Fatalf("consume activation = %v, %v", blocked, err)
	}
	status, err := state.Status()
	if err != nil || !status.RecoveryPending {
		t.Fatalf("recovery status = %+v, %v", status, err)
	}
	if err := runSwapBindProviders([]string{
		"--config", configPath,
		"--primary-trust-domain", "provider-one",
		"--secondary-trust-domain", "provider-two",
		"--reason", "must preserve recovery",
	}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "unresolved action") {
		t.Fatalf("recovery-state binding error = %v", err)
	}
	status, err = state.Status()
	if err != nil || !status.RecoveryPending {
		t.Fatalf("provider binding erased recovery state: %+v, %v", status, err)
	}

	if err := state.Stop("explicit test stop"); err != nil {
		t.Fatal(err)
	}
	store, err = journal.Open(cfg.Journal.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(time.Now().UTC(), swaprun.EventStarted, actionID, struct{}{}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	revision, err := state.Revision()
	if err != nil {
		t.Fatal(err)
	}
	if err := runSwapBindProviders([]string{
		"--config", configPath,
		"--primary-trust-domain", "provider-one",
		"--secondary-trust-domain", "provider-two",
		"--reason", "must preserve unfinished journal",
	}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "journal with no unresolved action") {
		t.Fatalf("unfinished-journal binding error = %v", err)
	}
	afterRevision, err := state.Revision()
	if err != nil || afterRevision != revision {
		t.Fatalf("provider binding changed control before journal validation: %q, %v", afterRevision, err)
	}
}

func TestBindProvidersPreservesNewerMetadataAfterStaleRead(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	seed := sha256.Sum256([]byte(t.Name()))
	wallet := ed25519.NewKeyFromSeed(seed[:])
	profile := testSwapProfile(solana.Encode(wallet.Public().(ed25519.PublicKey)))
	cfg := config{Swap: &profile}
	cfg.Control.StatePath = filepath.Join(stateDir, "control.json")
	cfg.Journal.Path = filepath.Join(stateDir, "events.jsonl")
	cfg.Evidence.PrimaryTrustDomain = "provider-one"
	cfg.Evidence.SecondaryTrustDomain = "provider-two"
	configPath := filepath.Join(root, "config.json")
	writeJSON(t, configPath, cfg)

	t.Setenv("MITHRIL_AGENT_MITHRIL_RPC_URL", testMithrilRPC)
	t.Setenv("MITHRIL_AGENT_PRIMARY_RPC_URL", testPrimaryRPC)
	t.Setenv("MITHRIL_AGENT_SECONDARY_RPC_URL", testSecondaryRPC)
	entered := make(chan struct{})
	release := make(chan struct{})
	previousHook := bindProvidersAfterInitialConfigRead
	var calls atomic.Int32
	bindProvidersAfterInitialConfigRead = func() {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
	}
	t.Cleanup(func() { bindProvidersAfterInitialConfigRead = previousHook })

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- runSwapBindProviders([]string{
			"--config", configPath,
			"--reason", "preserve a concurrent provider update",
		}, &bytes.Buffer{})
	}()
	<-entered
	if err := runSwapBindProviders([]string{
		"--config", configPath,
		"--primary-trust-domain", "provider-three",
		"--secondary-trust-domain", "provider-four",
		"--reason", "replace provider ownership metadata",
	}, &bytes.Buffer{}); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}

	var rebound config
	if err := readStrictJSON(configPath, &rebound); err != nil {
		t.Fatal(err)
	}
	if rebound.Evidence.PrimaryTrustDomain != "provider-three" ||
		rebound.Evidence.SecondaryTrustDomain != "provider-four" {
		t.Fatalf("stale binding restored old provider metadata: %+v", rebound.Evidence)
	}
}
