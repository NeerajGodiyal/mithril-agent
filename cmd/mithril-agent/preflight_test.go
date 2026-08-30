package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/internal/clockcheck"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/swaprun"
)

type preflightFixture struct {
	cfg                 config
	policy              signer.Policy
	riskPolicy          policyauthority.Policy
	submitterPolicy     submitter.Policy
	configPath          string
	policyPath          string
	keypairPath         string
	riskPolicyPath      string
	riskKeypairPath     string
	submitterPolicyPath string
	submitterKeyPath    string
	journalPath         string
	mcpCommand          string
	riskCommand         string
	signerCommand       string
	submitterCommand    string
	commandMarker       string
}

func TestPreflightValidatesOfflineSetupWithoutSideEffects(t *testing.T) {
	fixture := newPreflightFixture(t)
	installPreflightEnvironment(t, fixture.cfg.Profile.ClockUncertaintyLimit())

	var output bytes.Buffer
	err := run([]string{
		"preflight",
		"--config", fixture.configPath,
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	summary := decodePreflightSummary(t, output.Bytes())
	if summary.Status != preflightOK || !allPreflightChecksOK(summary.Checks) {
		t.Fatalf("preflight summary = %+v", summary)
	}
	if output.Len() > 512 || bytes.Count(output.Bytes(), []byte{'\n'}) != 1 {
		t.Fatalf("preflight output is not a bounded JSON line: %q", output.String())
	}
	for _, forbidden := range []string{
		fixture.cfg.Profile.Source,
		fixture.cfg.Profile.Destination,
		filepath.Dir(fixture.configPath),
		"http",
		"rpc-one",
		"secret-one",
		"reason",
	} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("preflight output disclosed %q: %s", forbidden, output.String())
		}
	}
	for _, path := range []string{
		fixture.cfg.Control.StatePath,
		fixture.cfg.Control.StatePath + ".lock",
		fixture.journalPath,
		fixture.journalPath + ".reserve",
		fixture.policy.AuthorizationLedgerPath,
		fixture.commandMarker,
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("preflight created or executed %q: %v", path, err)
		}
	}
}

func TestPreflightRejectsPolicyAndKeypairDrift(t *testing.T) {
	t.Run("policy field", func(t *testing.T) {
		fixture := newPreflightFixture(t)
		installPreflightEnvironment(t, fixture.cfg.Profile.ClockUncertaintyLimit())
		fixture.policy.MaxLamports--
		writeJSON(t, fixture.policyPath, fixture.policy)

		summary := runFailedPreflight(t, fixture)
		if summary.Checks.PolicyBinding != preflightFailed ||
			summary.Checks.KeypairBinding != preflightSkipped {
			t.Fatalf("policy drift summary = %+v", summary)
		}
	})

	t.Run("profile fingerprint", func(t *testing.T) {
		fixture := newPreflightFixture(t)
		installPreflightEnvironment(t, fixture.cfg.Profile.ClockUncertaintyLimit())
		fixture.policy.ProfileFingerprint = strings.Repeat("0", sha256.Size*2)
		writeJSON(t, fixture.policyPath, fixture.policy)

		summary := runFailedPreflight(t, fixture)
		if summary.Checks.PolicyBinding != preflightFailed ||
			summary.Checks.KeypairBinding != preflightSkipped {
			t.Fatalf("fingerprint drift summary = %+v", summary)
		}
	})

	t.Run("keypair", func(t *testing.T) {
		fixture := newPreflightFixture(t)
		installPreflightEnvironment(t, fixture.cfg.Profile.ClockUncertaintyLimit())
		otherSeed := sha256.Sum256([]byte("different signer"))
		writeKeypair(t, fixture.keypairPath, ed25519.NewKeyFromSeed(otherSeed[:]))

		summary := runFailedPreflight(t, fixture)
		if summary.Checks.KeypairBinding != preflightFailed ||
			summary.Checks.PolicyBinding != preflightOK {
			t.Fatalf("keypair drift summary = %+v", summary)
		}
	})
}

func TestPreflightRejectsMissingMCPState(t *testing.T) {
	fixture := newPreflightFixture(t)
	installPreflightEnvironment(t, fixture.cfg.Profile.ClockUncertaintyLimit())
	t.Setenv("MITHRIL_STATE_PATH", filepath.Join(t.TempDir(), "missing-state.json"))

	summary := runFailedPreflight(t, fixture)
	if summary.Checks.MCPInputs != preflightFailed {
		t.Fatalf("MCP input summary = %+v", summary)
	}
}

func TestValidateMCPInputs(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "mithril_state.json")
	configPath := filepath.Join(root, "mithril.toml")
	for _, path := range []string{statePath, configPath} {
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("explicit state", func(t *testing.T) {
		t.Setenv("MITHRIL_STATE_PATH", statePath)
		if !validateMCPInputs(nil) {
			t.Fatal("readable state was rejected")
		}
	})
	t.Run("accounts inference", func(t *testing.T) {
		t.Setenv("MITHRIL_STATE_PATH", "")
		t.Setenv("MITHRIL_ACCOUNTS_PATH", root)
		if !validateMCPInputs(nil) {
			t.Fatal("state below the accounts path was rejected")
		}
	})
	t.Run("Mithril config", func(t *testing.T) {
		t.Setenv("MITHRIL_STATE_PATH", "")
		t.Setenv("MITHRIL_ACCOUNTS_PATH", "")
		if !validateMCPInputs([]string{"mcp", "--config", configPath}) {
			t.Fatal("readable Mithril config was rejected")
		}
	})
	t.Run("missing config value", func(t *testing.T) {
		if validateMCPInputs([]string{"mcp", "--config"}) {
			t.Fatal("missing Mithril config value was accepted")
		}
	})
	t.Run("public config", func(t *testing.T) {
		if err := os.Chmod(configPath, 0o644); err != nil {
			t.Fatal(err)
		}
		if validateMCPInputs([]string{"mcp", "--config", configPath}) {
			t.Fatal("public Mithril config was accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(root, "state-link.json")
		if err := os.Symlink(statePath, link); err != nil {
			t.Fatal(err)
		}
		t.Setenv("MITHRIL_STATE_PATH", link)
		if validateMCPInputs(nil) {
			t.Fatal("symlinked state was accepted")
		}
	})
}

func TestPreflightChecksPrivateKeysInChildProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the three identity commands")
	}
	fixture := newPreflightFixture(t)
	installPreflightEnvironment(t, fixture.cfg.Profile.ClockUncertaintyLimit())
	preflightSignerIdentity = verifySignerIdentity
	preflightRiskIdentity = verifyRiskIdentity
	preflightSubmitIdentity = verifySubmitterIdentity

	for output, source := range map[string]string{
		fixture.riskCommand:      "../mithril-agent-policy",
		fixture.signerCommand:    "../mithril-agent-signer",
		fixture.submitterCommand: "../mithril-agent-submitter",
	} {
		if err := os.Remove(output); err != nil {
			t.Fatal(err)
		}
		command := exec.Command("go", "build", "-o", output, source)
		if result, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build identity command: %v\n%s", err, result)
		}
		if err := os.Chmod(output, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	if summary := checkPreflight(fixture.configPath); summary.Status != preflightOK {
		t.Fatalf("valid child identities = %+v", summary)
	}

	walletData, err := os.ReadFile(fixture.keypairPath)
	if err != nil {
		t.Fatal(err)
	}
	riskData, err := os.ReadFile(fixture.riskKeypairPath)
	if err != nil {
		t.Fatal(err)
	}
	submitterData, err := os.ReadFile(fixture.submitterKeyPath)
	if err != nil {
		t.Fatal(err)
	}

	wrongSeed := sha256.Sum256([]byte("wrong child identity"))
	writeKeypair(t, fixture.keypairPath, ed25519.NewKeyFromSeed(wrongSeed[:]))
	if summary := runFailedPreflight(t, fixture); summary.Checks.KeypairBinding != preflightFailed {
		t.Fatalf("wrong signer identity = %+v", summary)
	}
	if err := os.WriteFile(fixture.keypairPath, walletData, 0o600); err != nil {
		t.Fatal(err)
	}

	writeKeypair(t, fixture.riskKeypairPath, ed25519.NewKeyFromSeed(wrongSeed[:]))
	if summary := runFailedPreflight(t, fixture); summary.Checks.RiskKeypair != preflightFailed {
		t.Fatalf("wrong risk identity = %+v", summary)
	}
	if err := os.WriteFile(fixture.riskKeypairPath, riskData, 0o600); err != nil {
		t.Fatal(err)
	}

	wrongSubmitter := sha256.Sum256([]byte("wrong submitter identity"))
	writeJSON(t, fixture.submitterKeyPath, submitter.KeyDocument{
		Version: 1, PrivateKey: hex.EncodeToString(wrongSubmitter[:]),
	})
	if summary := runFailedPreflight(t, fixture); summary.Checks.SubmitterKey != preflightFailed {
		t.Fatalf("wrong submitter identity = %+v", summary)
	}
	if err := os.WriteFile(fixture.submitterKeyPath, submitterData, 0o600); err != nil {
		t.Fatal(err)
	}

	if summary := checkPreflight(fixture.configPath); summary.Status != preflightOK {
		t.Fatalf("restored child identities = %+v", summary)
	}
}

func TestPreflightRejectsUnsafeFilesAndCommands(t *testing.T) {
	t.Run("private policy", func(t *testing.T) {
		fixture := newPreflightFixture(t)
		installPreflightEnvironment(t, fixture.cfg.Profile.ClockUncertaintyLimit())
		if err := os.Chmod(fixture.policyPath, 0o644); err != nil {
			t.Fatal(err)
		}

		summary := runFailedPreflight(t, fixture)
		if summary.Checks.PolicyBinding != preflightFailed {
			t.Fatalf("unsafe policy summary = %+v", summary)
		}
	})

	t.Run("writable command", func(t *testing.T) {
		fixture := newPreflightFixture(t)
		installPreflightEnvironment(t, fixture.cfg.Profile.ClockUncertaintyLimit())
		if err := os.Chmod(fixture.mcpCommand, 0o777); err != nil {
			t.Fatal(err)
		}

		summary := runFailedPreflight(t, fixture)
		if summary.Checks.Commands != preflightFailed {
			t.Fatalf("unsafe command summary = %+v", summary)
		}
	})
}

func TestPreflightRejectsUnsafeRuntimePaths(t *testing.T) {
	t.Run("authorization lock symlink", func(t *testing.T) {
		fixture := newPreflightFixture(t)
		installPreflightEnvironment(t, fixture.cfg.Profile.ClockUncertaintyLimit())
		if err := os.Symlink(
			fixture.configPath,
			fixture.policy.AuthorizationLedgerPath+".lock",
		); err != nil {
			t.Fatal(err)
		}

		summary := runFailedPreflight(t, fixture)
		if summary.Checks.PolicyBinding != preflightFailed {
			t.Fatalf("unsafe authorization lock summary = %+v", summary)
		}
	})

	t.Run("journal reserve symlink", func(t *testing.T) {
		fixture := newPreflightFixture(t)
		installPreflightEnvironment(t, fixture.cfg.Profile.ClockUncertaintyLimit())
		if err := os.Symlink(
			fixture.configPath,
			fixture.journalPath+".reserve",
		); err != nil {
			t.Fatal(err)
		}

		summary := runFailedPreflight(t, fixture)
		if summary.Checks.JournalPath != preflightFailed {
			t.Fatalf("unsafe journal summary = %+v", summary)
		}
	})

	t.Run("operator status symlink", func(t *testing.T) {
		fixture := newPreflightFixture(t)
		installPreflightEnvironment(t, fixture.cfg.Profile.ClockUncertaintyLimit())
		if err := os.Symlink(
			fixture.configPath,
			fixture.journalPath+".status.json",
		); err != nil {
			t.Fatal(err)
		}

		summary := runFailedPreflight(t, fixture)
		if summary.Checks.JournalPath != preflightFailed {
			t.Fatalf("unsafe status summary = %+v", summary)
		}
	})

	t.Run("control symlink", func(t *testing.T) {
		fixture := newPreflightFixture(t)
		installPreflightEnvironment(t, fixture.cfg.Profile.ClockUncertaintyLimit())
		if err := os.Symlink(
			fixture.configPath,
			fixture.cfg.Control.StatePath,
		); err != nil {
			t.Fatal(err)
		}

		summary := runFailedPreflight(t, fixture)
		if summary.Checks.ControlPath != preflightFailed {
			t.Fatalf("unsafe control summary = %+v", summary)
		}
	})

	t.Run("journal parent symlink", func(t *testing.T) {
		fixture := newPreflightFixture(t)
		installPreflightEnvironment(t, fixture.cfg.Profile.ClockUncertaintyLimit())
		actual := filepath.Join(filepath.Dir(fixture.configPath), "actual-state")
		if err := os.Mkdir(actual, 0o700); err != nil {
			t.Fatal(err)
		}
		linked := filepath.Join(filepath.Dir(fixture.configPath), "linked-state")
		if err := os.Symlink(actual, linked); err != nil {
			t.Fatal(err)
		}
		fixture.cfg.Journal.Path = filepath.Join(linked, "events.jsonl")
		writeJSON(t, fixture.configPath, fixture.cfg)

		summary := runFailedPreflight(t, fixture)
		if summary.Checks.JournalPath != preflightFailed {
			t.Fatalf("unsafe journal parent summary = %+v", summary)
		}
	})

	t.Run("journal writable grandparent", func(t *testing.T) {
		fixture := newPreflightFixture(t)
		installPreflightEnvironment(t, fixture.cfg.Profile.ClockUncertaintyLimit())
		unsafe := filepath.Join(filepath.Dir(fixture.configPath), "unsafe-parent")
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
		fixture.cfg.Journal.Path = filepath.Join(safeLeaf, "events.jsonl")
		writeJSON(t, fixture.configPath, fixture.cfg)

		summary := runFailedPreflight(t, fixture)
		if summary.Checks.JournalPath != preflightFailed {
			t.Fatalf("unsafe journal ancestry summary = %+v", summary)
		}
	})
}

func TestPreflightRejectsProviderAliasesAndClockFailure(t *testing.T) {
	t.Run("provider alias", func(t *testing.T) {
		fixture := newPreflightFixture(t)
		installPreflightEnvironment(t, fixture.cfg.Profile.ClockUncertaintyLimit())
		t.Setenv(
			"MITHRIL_AGENT_SECONDARY_RPC_URL",
			"https://rpc-one.invalid/other?api-key=secret-two",
		)

		summary := runFailedPreflight(t, fixture)
		if summary.Checks.Providers != preflightFailed ||
			summary.Checks.Commands != preflightSkipped {
			t.Fatalf("provider alias summary = %+v", summary)
		}
	})

	t.Run("clock", func(t *testing.T) {
		fixture := newPreflightFixture(t)
		limit := fixture.cfg.Profile.ClockUncertaintyLimit()
		installPreflightEnvironment(t, limit)
		preflightClockSample = func() (clockcheck.Sample, error) {
			return validPreflightClockSample(uint64(limit) + 1), nil
		}

		summary := runFailedPreflight(t, fixture)
		if summary.Checks.Clock != preflightFailed {
			t.Fatalf("clock failure summary = %+v", summary)
		}
	})

	t.Run("minimum clock offset", func(t *testing.T) {
		fixture := newPreflightFixture(t)
		limit := fixture.cfg.Profile.ClockUncertaintyLimit()
		installPreflightEnvironment(t, limit)
		preflightClockSample = func() (clockcheck.Sample, error) {
			sample := validPreflightClockSample(uint64(limit))
			sample.OffsetNanos = math.MinInt64
			return sample, nil
		}

		summary := runFailedPreflight(t, fixture)
		if summary.Checks.Clock != preflightFailed {
			t.Fatalf("clock offset summary = %+v", summary)
		}
	})
}

func TestPreflightRejectsMutablePathCollisions(t *testing.T) {
	fixture := newPreflightFixture(t)
	installPreflightEnvironment(t, fixture.cfg.Profile.ClockUncertaintyLimit())
	fixture.journalPath = fixture.configPath
	fixture.cfg.Journal.Path = fixture.journalPath
	writeJSON(t, fixture.configPath, fixture.cfg)
	before, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatal(err)
	}

	summary := runFailedPreflight(t, fixture)
	if summary.Checks.PathSeparation != preflightFailed ||
		summary.Checks.JournalPath != preflightOK ||
		summary.Checks.ControlPath != preflightOK {
		t.Fatalf("path collision summary = %+v", summary)
	}
	after, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("preflight changed the colliding config file")
	}
}

func TestPreflightRequiresExactArguments(t *testing.T) {
	var output bytes.Buffer
	err := run([]string{"preflight"}, &output)
	if err == nil || output.Len() != 0 {
		t.Fatalf("missing config result = %v, %q", err, output.String())
	}
}

func TestPreflightValidatesSwapPolicyBindings(t *testing.T) {
	fixture := newPreflightFixture(t)
	owner := fixture.policy.Source
	profile := testSwapProfile(owner)
	configureSwapPreflightFixture(t, &fixture, profile)
	installPreflightEnvironment(t, profile.ClockUncertaintyLimit())
	t.Setenv("MITHRIL_AGENT_QUOTE_RPC_URL", "https://quote.invalid/devnet")

	var output bytes.Buffer
	if err := run([]string{"preflight", "--config", fixture.configPath}, &output); err != nil {
		t.Fatalf("swap preflight: %v: %s", err, output.String())
	}
	summary := decodePreflightSummary(t, output.Bytes())
	if summary.Status != preflightOK || !allPreflightChecksOK(summary.Checks) {
		t.Fatalf("swap preflight summary = %+v", summary)
	}
}

func TestPreflightValidatesBuyPolicyBindings(t *testing.T) {
	fixture := newPreflightFixture(t)
	profile := testBuySwapProfile(t)
	profile.BuyRoute.Owner = fixture.policy.Source
	inputAccount, err := orcaswap.AssociatedTokenAddress(
		fixture.policy.Source, orcaswap.DevnetUSDCMint,
	)
	if err != nil {
		t.Fatal(err)
	}
	profile.BuyRoute.InputTokenAccount = inputAccount
	configureSwapPreflightFixture(t, &fixture, profile)
	installPreflightEnvironment(t, profile.ClockUncertaintyLimit())
	t.Setenv("MITHRIL_AGENT_QUOTE_RPC_URL", "https://quote.invalid/devnet")

	var output bytes.Buffer
	if err := run([]string{"preflight", "--config", fixture.configPath}, &output); err != nil {
		t.Fatalf("buy preflight: %v: %s", err, output.String())
	}
	summary := decodePreflightSummary(t, output.Bytes())
	if summary.Status != preflightOK || !allPreflightChecksOK(summary.Checks) {
		t.Fatalf("buy preflight summary = %+v", summary)
	}
}

func TestBuyPreflightRejectsPolicyDocumentDrift(t *testing.T) {
	for _, test := range []struct {
		name        string
		mutate      func(*preflightFixture)
		failedCheck func(preflightChecks) string
	}{
		{
			name: "risk policy",
			mutate: func(fixture *preflightFixture) {
				fixture.riskPolicy.TransactionPolicy.DailyInputTokenCap++
				writeJSON(t, fixture.riskPolicyPath, fixture.riskPolicy)
			},
			failedCheck: func(checks preflightChecks) string { return checks.RiskPolicy },
		},
		{
			name: "submitter route",
			mutate: func(fixture *preflightFixture) {
				route := *fixture.submitterPolicy.OrcaBuy
				route.MinOutputLamports++
				fixture.submitterPolicy.OrcaBuy = &route
				writeJSON(t, fixture.submitterPolicyPath, fixture.submitterPolicy)
			},
			failedCheck: func(checks preflightChecks) string { return checks.SubmitterPolicy },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPreflightFixture(t)
			profile := testBuySwapProfile(t)
			profile.BuyRoute.Owner = fixture.policy.Source
			inputAccount, err := orcaswap.AssociatedTokenAddress(
				fixture.policy.Source, orcaswap.DevnetUSDCMint,
			)
			if err != nil {
				t.Fatal(err)
			}
			profile.BuyRoute.InputTokenAccount = inputAccount
			configureSwapPreflightFixture(t, &fixture, profile)
			installPreflightEnvironment(t, profile.ClockUncertaintyLimit())
			t.Setenv("MITHRIL_AGENT_QUOTE_RPC_URL", "https://quote.invalid/devnet")
			test.mutate(&fixture)
			summary := runFailedPreflight(t, fixture)
			if test.failedCheck(summary.Checks) != preflightFailed {
				t.Fatalf("drifted buy policy summary = %+v", summary)
			}
		})
	}
}

func TestBuyPolicyMatchingBindsEveryLimitAndRouteField(t *testing.T) {
	profile := testBuySwapProfile(t)
	fingerprint := strings.Repeat("a", 64)
	policy := signer.Policy{
		Cluster: profile.Cluster, Profile: profile.Name, ProfileVersion: profile.Version,
		ProfileFingerprint: fingerprint, Source: profile.Owner(),
		MaxInputTokenAmount:       profile.InputTokenAmount,
		MaxFeeLamports:            profile.MaxFeeLamports,
		DailyInputTokenCap:        profile.DailyInputTokenCap,
		DailyNativeFeeCapLamports: profile.DailyNativeFeeCapLamports,
		ScheduleWindowSeconds:     profile.ScheduleWindowSeconds,
		ScheduleAnchorUnix:        profile.ScheduleAnchorUnix,
		MaxBlockHeightWindow:      profile.MaxBlockHeightWindow,
		OrcaBuy:                   profile.BuyRoute,
	}
	if !policyMatchesSwap(policy, profile, fingerprint) {
		t.Fatal("exact buy signer policy did not match")
	}
	mutations := map[string]func(*signer.Policy){
		"cluster":     func(value *signer.Policy) { value.Cluster = "mainnet" },
		"profile":     func(value *signer.Policy) { value.Profile = orcaswap.ProfileName },
		"version":     func(value *signer.Policy) { value.ProfileVersion++ },
		"fingerprint": func(value *signer.Policy) { value.ProfileFingerprint = strings.Repeat("b", 64) },
		"source":      func(value *signer.Policy) { value.Source = profile.BuyRoute.Pool },
		"input":       func(value *signer.Policy) { value.MaxInputTokenAmount-- },
		"fee":         func(value *signer.Policy) { value.MaxFeeLamports-- },
		"daily input": func(value *signer.Policy) { value.DailyInputTokenCap++ },
		"daily fee":   func(value *signer.Policy) { value.DailyNativeFeeCapLamports++ },
		"window":      func(value *signer.Policy) { value.ScheduleWindowSeconds *= 2 },
		"anchor":      func(value *signer.Policy) { value.ScheduleAnchorUnix += 86_400 },
		"height":      func(value *signer.Policy) { value.MaxBlockHeightWindow++ },
		"route": func(value *signer.Policy) {
			copy := *value.OrcaBuy
			copy.MinOutputLamports++
			value.OrcaBuy = &copy
		},
		"sell route": func(value *signer.Policy) { value.OrcaSwap = &orcaswap.Policy{} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := policy
			mutate(&changed)
			if policyMatchesSwap(changed, profile, fingerprint) {
				t.Fatal("mutated buy signer policy matched")
			}
		})
	}
}

func testBuySwapProfile(t *testing.T) swaprun.Profile {
	t.Helper()
	ownerKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	owner := solana.Encode(ownerKey.Public().(ed25519.PublicKey))
	inputAccount, err := orcaswap.AssociatedTokenAddress(owner, orcaswap.DevnetUSDCMint)
	if err != nil {
		t.Fatal(err)
	}
	return swaprun.Profile{
		Name: orcaswap.BuyProfileName, Version: orcaswap.BuyProfileVersion, Cluster: "devnet",
		BuyRoute: &orcaswap.BuyPolicyV2{
			Owner: owner, Pool: orcaswap.DevnetPool,
			TokenMintA: orcaswap.WrappedSOLMint, TokenMintB: orcaswap.DevnetUSDCMint,
			InputTokenAccount:   inputAccount,
			TokenVaultA:         "C9zLV5zWF66j3rZj3uuhDqvfuA8esJyWnruGzDW9qEj2",
			TokenVaultB:         "7DM3RMz2yzUB8yPRQM3FMZgdFrwZGMsabsfsKopWktoX",
			Oracle:              "2KEWNc3b6EfqoWQpfKQMHh4mhRyKXYRdPbtGRTJX3Cip",
			ProgramData:         orcaswap.WhirlpoolProgramData,
			UpgradeAuthority:    orcaswap.WhirlpoolUpgradeAuth,
			DeploymentSlot:      orcaswap.WhirlpoolDeploySlot,
			MaxInputTokenAmount: 100_000, MinOutputLamports: 400_000,
			MaxSlippageBPS: 100, MaxTemporaryRentLamports: 3_000_000,
		},
		InputTokenAmount: 100_000, SlippageBPS: 100,
		ReserveLamports: 50_000_000, MaxFeeLamports: 100_000,
		DailyInputTokenCap: 100_000, DailyNativeFeeCapLamports: 100_000,
		ScheduleWindowSeconds:     3_600,
		ScheduleAnchorUnix:        time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		MaxClockUncertaintyMillis: 500, MaxObservationAgeSeconds: 30,
		MinHealthyObservationSeconds: 5, MinHealthySlotAdvance: 1,
		MaxBlockHeightWindow: 200, MaxReconciliationSeconds: 180,
	}
}

func configureSwapPreflightFixture(
	t *testing.T,
	fixture *preflightFixture,
	profile swaprun.Profile,
) {
	t.Helper()
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	fixture.cfg.Profile = agent.Profile{}
	fixture.cfg.Swap = &profile
	fixture.cfg.Evidence.PrimaryTrustDomain = "provider-one"
	fixture.cfg.Evidence.PrimaryOriginSHA256 = testRPCIdentity(
		t, "https://rpc-one.invalid/path?api-key=secret-one",
	)
	fixture.cfg.Evidence.SecondaryTrustDomain = "provider-two"
	fixture.cfg.Evidence.SecondaryOriginSHA256 = testRPCIdentity(
		t, "https://rpc-two.invalid/path?api-key=secret-two",
	)
	quoteScript := filepath.Join(filepath.Dir(fixture.configPath), "quote.mjs")
	if err := os.WriteFile(quoteScript, []byte("// preflight fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.cfg.Quote.Command = fixture.mcpCommand
	fixture.cfg.Quote.ScriptPath = quoteScript
	fixture.policy.Cluster = profile.Cluster
	fixture.policy.Profile = profile.Name
	fixture.policy.ProfileVersion = profile.Version
	fixture.policy.ProfileFingerprint = fingerprint
	fixture.policy.Source = profile.Owner()
	fixture.policy.Destination = ""
	fixture.policy.MaxLamports = profile.InputLamports
	fixture.policy.MaxFeeLamports = profile.MaxFeeLamports
	fixture.policy.DailyDebitCapLamports = profile.DailyDebitCapLamports
	fixture.policy.MaxInputTokenAmount = profile.InputTokenAmount
	fixture.policy.DailyInputTokenCap = profile.DailyInputTokenCap
	fixture.policy.DailyNativeFeeCapLamports = profile.DailyNativeFeeCapLamports
	fixture.policy.ScheduleWindowSeconds = profile.ScheduleWindowSeconds
	fixture.policy.ScheduleAnchorUnix = profile.ScheduleAnchorUnix
	fixture.policy.MaxBlockHeightWindow = profile.MaxBlockHeightWindow
	fixture.policy.OrcaSwap = nil
	fixture.policy.OrcaBuy = nil
	if profile.IsBuy() {
		fixture.policy.OrcaBuy = profile.BuyRoute
	} else {
		fixture.policy.OrcaSwap = &profile.Route
	}
	fixture.riskPolicy.TransactionPolicy = fixture.policy
	fixture.submitterPolicy.Cluster = profile.Cluster
	fixture.submitterPolicy.Profile = profile.Name
	fixture.submitterPolicy.ProfileFingerprint = fingerprint
	fixture.submitterPolicy.ControlStatePath = fixture.cfg.Control.StatePath
	fixture.submitterPolicy.Source = profile.Owner()
	fixture.submitterPolicy.Destination = ""
	fixture.submitterPolicy.MaxLamports = profile.InputLamports
	fixture.submitterPolicy.MaxInputTokenAmount = profile.InputTokenAmount
	fixture.submitterPolicy.MaxFeeLamports = profile.MaxFeeLamports
	fixture.submitterPolicy.Evidence = proposalcheck.ProviderBindings{
		PrimaryTrustDomain:    fixture.cfg.Evidence.PrimaryTrustDomain,
		PrimaryOriginSHA256:   fixture.cfg.Evidence.PrimaryOriginSHA256,
		SecondaryTrustDomain:  fixture.cfg.Evidence.SecondaryTrustDomain,
		SecondaryOriginSHA256: fixture.cfg.Evidence.SecondaryOriginSHA256,
	}
	fixture.submitterPolicy.OrcaSwap = nil
	fixture.submitterPolicy.OrcaBuy = nil
	if profile.IsBuy() {
		fixture.submitterPolicy.OrcaBuy = profile.BuyRoute
	} else {
		fixture.submitterPolicy.OrcaSwap = &profile.Route
	}
	writeJSON(t, fixture.configPath, fixture.cfg)
	writeJSON(t, fixture.policyPath, fixture.policy)
	writeJSON(t, fixture.riskPolicyPath, fixture.riskPolicy)
	writeJSON(t, fixture.submitterPolicyPath, fixture.submitterPolicy)
}

func TestSwapSocketModeStartsWithoutQuoteCredentialOrService(t *testing.T) {
	fixture := newPreflightFixture(t)
	owner := fixture.policy.Source
	profile := testSwapProfile(owner)
	configureSwapPreflightFixture(t, &fixture, profile)
	installPreflightEnvironment(t, profile.ClockUncertaintyLimit())
	t.Setenv("MITHRIL_AGENT_QUOTE_RPC_URL", "")

	socketDirectory, err := os.MkdirTemp("/tmp", "mithril-agent-quote-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDirectory) })
	if err := os.Chmod(socketDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(socketDirectory, "quote.sock")
	fixture.cfg.Quote.Command = ""
	fixture.cfg.Quote.ScriptPath = ""
	fixture.cfg.Quote.SocketPath = socketPath
	writeJSON(t, fixture.configPath, fixture.cfg)

	if _, err := openSwapDependencies(fixture.cfg); err != nil {
		t.Fatalf("open socket-mode dependencies without quote credential: %v", err)
	}
	var output bytes.Buffer
	if err := run([]string{"preflight", "--config", fixture.configPath}, &output); err != nil {
		t.Fatalf("socket-mode preflight: %v: %s", err, output.String())
	}
	summary := decodePreflightSummary(t, output.Bytes())
	if summary.Status != preflightOK || !allPreflightChecksOK(summary.Checks) {
		t.Fatalf("socket-mode preflight summary = %+v", summary)
	}
}

func TestPreflightUsesRiskAuthoritySocketWithoutReadingItsKey(t *testing.T) {
	fixture := newPreflightFixture(t)
	profile := testSwapProfile(fixture.policy.Source)
	configureSwapPreflightFixture(t, &fixture, profile)
	installPreflightEnvironment(t, profile.ClockUncertaintyLimit())
	t.Setenv("MITHRIL_AGENT_QUOTE_RPC_URL", "https://quote.invalid/devnet")
	if err := os.Remove(fixture.riskKeypairPath); err != nil {
		t.Fatal(err)
	}

	oldIdentity := preflightRiskSocketIdentity
	t.Cleanup(func() { preflightRiskSocketIdentity = oldIdentity })
	called := false
	preflightRiskSocketIdentity = func(_ context.Context, socketPath, keyID, publicKey string) error {
		called = true
		if socketPath != "/run/mithril-agent-policy-sell.sock" ||
			keyID != fixture.cfg.Policy.KeyID || publicKey != fixture.cfg.Policy.PublicKey {
			return errors.New("risk socket identity mismatch")
		}
		return nil
	}

	summary := checkPreflightWithSockets(
		fixture.configPath, "", "/run/mithril-agent-policy-sell.sock", "",
	)
	if !called {
		t.Fatal("preflight did not verify the risk authority socket identity")
	}
	if summary.Status != preflightOK || !allPreflightChecksOK(summary.Checks) {
		t.Fatalf("risk socket preflight summary = %+v", summary)
	}
}

func TestPreflightRequiresPythKeyOnlyForConfiguredPriceRule(t *testing.T) {
	fixture := newPreflightFixture(t)
	owner := fixture.policy.Source
	profile := testSwapProfile(owner)
	profile.PriceTrigger = &pricetrigger.Policy{
		Version: pricetrigger.Version, Feed: pricetrigger.FeedSOLUSD,
		Direction: pricetrigger.SellAtOrAbove, ThresholdMicros: 75_000_000,
		MaxAgeSeconds: 30, MaxSourceSkewSeconds: 10,
		MaxDeviationBPS: 200, MaxConfidenceBPS: 200,
		PrimarySourceSHA256:   pricesource.PythIdentitySHA256(),
		SecondarySourceSHA256: pricesource.KrakenSOLIdentitySHA256(),
	}
	configureSwapPreflightFixture(t, &fixture, profile)
	installPreflightEnvironment(t, profile.ClockUncertaintyLimit())
	t.Setenv("MITHRIL_AGENT_QUOTE_RPC_URL", "https://quote.invalid/path")
	t.Setenv("MITHRIL_AGENT_PYTH_API_KEY", "")

	summary := runFailedPreflight(t, fixture)
	if summary.Checks.PriceSource != preflightFailed {
		t.Fatalf("price source check = %q", summary.Checks.PriceSource)
	}

	t.Setenv("MITHRIL_AGENT_PYTH_API_KEY", "test-pyth-key")
	var output bytes.Buffer
	if err := run([]string{"preflight", "--config", fixture.configPath}, &output); err != nil {
		t.Fatalf("preflight with Pyth key: %v: %s", err, output.String())
	}
}

func newPreflightFixture(t *testing.T) preflightFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "state")
	binDir := filepath.Join(root, "bin")
	for _, path := range []string{stateDir, binDir} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	sourceSeed := sha256.Sum256([]byte("preflight source " + t.Name()))
	sourceKey := ed25519.NewKeyFromSeed(sourceSeed[:])
	destinationSeed := sha256.Sum256([]byte("preflight destination " + t.Name()))
	destinationKey := ed25519.NewKeyFromSeed(destinationSeed[:])
	source := solana.Encode(sourceKey.Public().(ed25519.PublicKey))
	destination := solana.Encode(destinationKey.Public().(ed25519.PublicKey))

	fixture := preflightFixture{
		configPath:          filepath.Join(root, "config.json"),
		policyPath:          filepath.Join(root, "signer-policy.json"),
		keypairPath:         filepath.Join(root, "devnet-keypair.json"),
		riskPolicyPath:      filepath.Join(root, "risk-policy.json"),
		riskKeypairPath:     filepath.Join(root, "risk-authority-keypair.json"),
		submitterPolicyPath: filepath.Join(root, "submitter-policy.json"),
		submitterKeyPath:    filepath.Join(root, "submitter-key.json"),
		journalPath:         filepath.Join(stateDir, "events.jsonl"),
		mcpCommand:          filepath.Join(binDir, "mithril"),
		riskCommand:         filepath.Join(binDir, "mithril-agent-policy"),
		signerCommand:       filepath.Join(binDir, "mithril-agent-signer"),
		submitterCommand:    filepath.Join(binDir, "mithril-agent-submitter"),
		commandMarker:       filepath.Join(root, "command-ran"),
	}
	fixture.cfg = config{Profile: agent.Profile{
		Name:                         agent.ProfileTreasurySweepV1,
		Version:                      1,
		Cluster:                      "devnet",
		Source:                       source,
		Destination:                  destination,
		ReserveLamports:              100_000_000,
		MinTransferLamports:          1_000_000,
		MaxTransferLamports:          5_000_000,
		DailyCapLamports:             10_000_000,
		MaxFeeLamports:               10_000,
		ScheduleWindowSeconds:        3_600,
		ScheduleAnchorUnix:           time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		MaxClockUncertaintyMillis:    100,
		MaxObservationAgeSeconds:     30,
		MinHealthyObservationSeconds: 5,
		MinHealthySlotAdvance:        1,
		MaxNodeLagSlots:              150,
		MaxReconciliationSeconds:     180,
	}}
	fixture.cfg.MCP.Command = fixture.mcpCommand
	fixture.cfg.MCP.Args = []string{"mcp", "--profile", "monitor"}
	fixture.cfg.Signer.Command = fixture.signerCommand
	fixture.cfg.Signer.PolicyPath = fixture.policyPath
	fixture.cfg.Signer.KeypairPath = fixture.keypairPath
	fixture.cfg.Submitter.Command = fixture.submitterCommand
	fixture.cfg.Submitter.PolicyPath = fixture.submitterPolicyPath
	fixture.cfg.Submitter.PrivateKeyPath = fixture.submitterKeyPath
	authoritySeed := sha256.Sum256([]byte("preflight risk authority " + t.Name()))
	authorityKey := ed25519.NewKeyFromSeed(authoritySeed[:])
	authorityPublic, err := riskgrant.PublicKeyHex(authorityKey)
	if err != nil {
		t.Fatal(err)
	}
	fixture.cfg.Policy.Command = fixture.riskCommand
	fixture.cfg.Policy.PolicyPath = fixture.riskPolicyPath
	fixture.cfg.Policy.KeypairPath = fixture.riskKeypairPath
	fixture.cfg.Policy.KeyID = "preflight-risk-authority"
	fixture.cfg.Policy.PublicKey = authorityPublic
	submitterSeed := sha256.Sum256([]byte("preflight submitter " + t.Name()))
	submitterPrivateKey := hex.EncodeToString(submitterSeed[:])
	submitterPublicKey, err := sealedtx.PublicKey(submitterPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	fixture.cfg.Control.StatePath = filepath.Join(stateDir, "control.json")
	fixture.cfg.Journal.Path = fixture.journalPath
	fixture.cfg.Evidence.PrimaryTrustDomain = "provider-one"
	fixture.cfg.Evidence.PrimaryOriginSHA256 = testRPCIdentity(t, testPrimaryRPC)
	fixture.cfg.Evidence.SecondaryTrustDomain = "provider-two"
	fixture.cfg.Evidence.SecondaryOriginSHA256 = testRPCIdentity(t, testSecondaryRPC)
	fingerprint, err := fixture.cfg.Profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	fixture.policy = signer.Policy{
		Cluster:               fixture.cfg.Profile.Cluster,
		Profile:               fixture.cfg.Profile.Name,
		ProfileVersion:        fixture.cfg.Profile.Version,
		ProfileFingerprint:    fingerprint,
		Source:                fixture.cfg.Profile.Source,
		Destination:           fixture.cfg.Profile.Destination,
		MaxLamports:           fixture.cfg.Profile.MaxTransferLamports,
		MaxFeeLamports:        fixture.cfg.Profile.MaxFeeLamports,
		DailyDebitCapLamports: fixture.cfg.Profile.DailyCapLamports,
		AuthorizationLedgerPath: filepath.Join(
			stateDir,
			"signer-authorizations.jsonl",
		),
		ScheduleWindowSeconds:  fixture.cfg.Profile.ScheduleWindowSeconds,
		ScheduleAnchorUnix:     fixture.cfg.Profile.ScheduleAnchorUnix,
		MaxBlockHeightWindow:   200,
		RiskAuthorityKeyID:     fixture.cfg.Policy.KeyID,
		RiskAuthorityPublicKey: fixture.cfg.Policy.PublicKey,
		SubmitterPublicKey:     submitterPublicKey,
	}
	fixture.riskPolicy = policyauthority.Policy{
		TransactionPolicy: fixture.policy,
		GrantLifetimeSecs: 30,
	}
	fixture.submitterPolicy = submitter.Policy{
		Cluster:            fixture.policy.Cluster,
		ProfileFingerprint: fixture.policy.ProfileFingerprint,
		ControlStatePath:   fixture.cfg.Control.StatePath,
		Source:             fixture.policy.Source,
		Destination:        fixture.policy.Destination,
		MaxLamports:        fixture.policy.MaxLamports,
		MaxFeeLamports:     fixture.policy.MaxFeeLamports,
		SubmitterPublicKey: fixture.policy.SubmitterPublicKey,
		Evidence: proposalcheck.ProviderBindings{
			PrimaryTrustDomain:    fixture.cfg.Evidence.PrimaryTrustDomain,
			PrimaryOriginSHA256:   fixture.cfg.Evidence.PrimaryOriginSHA256,
			SecondaryTrustDomain:  fixture.cfg.Evidence.SecondaryTrustDomain,
			SecondaryOriginSHA256: fixture.cfg.Evidence.SecondaryOriginSHA256,
		},
	}

	script := fmt.Sprintf(
		"#!/bin/sh\nprintf ran > %q\nexit 99\n",
		fixture.commandMarker,
	)
	for _, path := range []string{
		fixture.mcpCommand,
		fixture.riskCommand,
		fixture.signerCommand,
		fixture.submitterCommand,
	} {
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(t, fixture.configPath, fixture.cfg)
	writeJSON(t, fixture.policyPath, fixture.policy)
	writeKeypair(t, fixture.keypairPath, sourceKey)
	writeJSON(t, fixture.riskPolicyPath, fixture.riskPolicy)
	writeKeypair(t, fixture.riskKeypairPath, authorityKey)
	writeJSON(t, fixture.submitterPolicyPath, fixture.submitterPolicy)
	writeJSON(t, fixture.submitterKeyPath, submitter.KeyDocument{
		Version:    1,
		PrivateKey: submitterPrivateKey,
	})
	return fixture
}

func writeKeypair(t *testing.T, path string, key ed25519.PrivateKey) {
	t.Helper()
	values := make([]uint16, len(key))
	for index, value := range key {
		values[index] = uint16(value)
	}
	writeJSON(t, path, values)
}

func installPreflightEnvironment(t *testing.T, uncertainty time.Duration) {
	t.Helper()
	oldClockSample := preflightClockSample
	oldOperatingSystem := preflightOperatingSystem
	oldSignerIdentity := preflightSignerIdentity
	oldRiskIdentity := preflightRiskIdentity
	oldSubmitIdentity := preflightSubmitIdentity
	t.Cleanup(func() {
		preflightClockSample = oldClockSample
		preflightOperatingSystem = oldOperatingSystem
		preflightSignerIdentity = oldSignerIdentity
		preflightRiskIdentity = oldRiskIdentity
		preflightSubmitIdentity = oldSubmitIdentity
	})
	preflightOperatingSystem = "linux"
	preflightClockSample = func() (clockcheck.Sample, error) {
		return validPreflightClockSample(uint64(uncertainty)), nil
	}
	preflightSignerIdentity = func(
		_ context.Context, _, _, keypairPath, expected string,
	) error {
		key, err := signer.LoadKeypair(keypairPath)
		if err != nil {
			return err
		}
		defer clear(key)
		publicKey, err := signer.PublicKey(key)
		if err != nil || publicKey != expected {
			return errors.New("signer identity mismatch")
		}
		return nil
	}
	preflightRiskIdentity = func(
		_ context.Context, _, _, keypairPath, _, expected string,
	) error {
		key, err := signer.LoadKeypair(keypairPath)
		if err != nil {
			return err
		}
		defer clear(key)
		publicKey, err := riskgrant.PublicKeyHex(key)
		if err != nil || publicKey != expected {
			return errors.New("risk identity mismatch")
		}
		return nil
	}
	preflightSubmitIdentity = func(
		_ context.Context, _, _, keyPath, expected string,
	) error {
		privateKey, err := submitter.LoadPrivateKey(keyPath)
		if err != nil {
			return err
		}
		publicKey, err := sealedtx.PublicKey(privateKey)
		if err != nil || publicKey != expected {
			return errors.New("submitter identity mismatch")
		}
		return nil
	}
	t.Setenv("MITHRIL_AGENT_MITHRIL_RPC_URL", "http://127.0.0.1:8899")
	t.Setenv(
		"MITHRIL_AGENT_PRIMARY_RPC_URL",
		"https://rpc-one.invalid/path?api-key=secret-one",
	)
	t.Setenv(
		"MITHRIL_AGENT_SECONDARY_RPC_URL",
		"https://rpc-two.invalid/path?api-key=secret-two",
	)
	statePath := filepath.Join(t.TempDir(), "mithril_state.json")
	if err := os.WriteFile(statePath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MITHRIL_STATE_PATH", statePath)
}

func validPreflightClockSample(uncertainty uint64) clockcheck.Sample {
	return clockcheck.Sample{
		WallTime:         time.Now().UTC(),
		BootID:           "00000000-0000-0000-0000-000000000001",
		MonotonicNanos:   1,
		UncertaintyNanos: uncertainty,
	}
}

func runFailedPreflight(
	t *testing.T,
	fixture preflightFixture,
) preflightSummary {
	t.Helper()
	var output bytes.Buffer
	err := run([]string{
		"preflight",
		"--config", fixture.configPath,
	}, &output)
	if !errors.Is(err, errPreflightFailed) {
		t.Fatalf("preflight error = %v", err)
	}
	if output.Len() > 512 || strings.Contains(output.String(), filepath.Dir(fixture.configPath)) {
		t.Fatalf("failed preflight output is unsafe: %s", output.String())
	}
	return decodePreflightSummary(t, output.Bytes())
}

func decodePreflightSummary(t *testing.T, encoded []byte) preflightSummary {
	t.Helper()
	var summary preflightSummary
	if err := json.Unmarshal(encoded, &summary); err != nil {
		t.Fatalf("decode preflight summary: %v", err)
	}
	return summary
}
