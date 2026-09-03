package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/internal/clockcheck"
	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/internal/runmetrics"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/swapbuilder"
	"github.com/Overclock-Validator/mithril-agent/swaprun"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func TestShadowCommandEndToEnd(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	observationPath := filepath.Join(dir, "observation.json")
	journalPath := filepath.Join(dir, "state", "events.jsonl")
	now := time.Now().UTC()

	cfg := config{Profile: agent.Profile{
		Name:                         agent.ProfileTreasurySweepV1,
		Version:                      1,
		Cluster:                      "devnet",
		Source:                       "11111111111111111111111111111111",
		Destination:                  "SysvarC1ock11111111111111111111111111111111",
		ReserveLamports:              100,
		MinTransferLamports:          10,
		MaxTransferLamports:          50,
		DailyCapLamports:             80,
		MaxFeeLamports:               5,
		ScheduleWindowSeconds:        3_600,
		ScheduleAnchorUnix:           time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		MaxClockUncertaintyMillis:    100,
		MaxObservationAgeSeconds:     30,
		MinHealthyObservationSeconds: 5,
		MinHealthySlotAdvance:        1,
		MaxNodeLagSlots:              150,
		MaxReconciliationSeconds:     180,
	}}
	observation := agent.Observation{
		Cluster:         "devnet",
		Source:          cfg.Profile.Source,
		BalanceLamports: 1000,
		Slot:            7,
		ObservedAt:      now,
	}
	writeJSON(t, configPath, cfg)
	writeJSON(t, observationPath, observation)
	var fingerprintOutput bytes.Buffer
	if err := run([]string{
		"profile-fingerprint",
		"--config", configPath,
	}, &fingerprintOutput); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := cfg.Profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(
		fingerprintOutput.String(),
		`"profile_sha256":"`+fingerprint+`"`,
	) {
		t.Fatalf("profile fingerprint output = %s", fingerprintOutput.String())
	}

	var first bytes.Buffer
	args := []string{
		"shadow",
		"--config", configPath,
		"--observation", observationPath,
		"--journal", journalPath,
	}
	if err := run(args, &first); err != nil {
		t.Fatal(err)
	}
	var firstResult agent.ShadowResult
	if err := json.Unmarshal(first.Bytes(), &firstResult); err != nil {
		t.Fatal(err)
	}
	if firstResult.Decision != "shadowed" || firstResult.AmountLamports != 50 {
		t.Fatalf("first result = %+v", firstResult)
	}

	var second bytes.Buffer
	if err := run(args, &second); err != nil {
		t.Fatal(err)
	}
	var secondResult agent.ShadowResult
	if err := json.Unmarshal(second.Bytes(), &secondResult); err != nil {
		t.Fatal(err)
	}
	if !secondResult.Recovered || secondResult.ActionID != firstResult.ActionID {
		t.Fatalf("second result = %+v, first = %+v", secondResult, firstResult)
	}
}

func TestClockCheckRejectsArguments(t *testing.T) {
	if err := run([]string{"clock-check", "unexpected"}, &bytes.Buffer{}); err == nil {
		t.Fatal("clock-check accepted invalid arguments")
	}
}

func TestRootHelpPrioritizesSupportedCommands(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	help := output.String()
	if strings.ContainsRune(help, '\t') {
		t.Fatalf("root help contains tab indentation:\n%s", help)
	}
	documentation := strings.Index(help, "Read README.md, then ROADMAP.md")
	program := strings.Index(help, "mithril-agent program --help")
	index := strings.Index(help, "mithril-agent index --help")
	optional := strings.Index(help, "Optional bounded Devnet trading pilot")
	status := strings.Index(help, "mithril-agent status --status-socket /run/mithril-agent-status-sell.sock")
	legacy := strings.Index(help, "Legacy single-leg check, demo, preflight, and swap commands")
	if documentation < 0 || program < 0 || index < 0 || optional < 0 || status < 0 || legacy < 0 ||
		documentation > program || program > index || index > optional || optional > status || status > legacy {
		t.Fatalf("root help does not prioritize the walletless workflow:\n%s", help)
	}
	if strings.Contains(help, "mithril-agent devnet-check") ||
		strings.Contains(help, "mithril-agent devnet-enable") ||
		strings.Contains(help, "mithril-agent-demo.service") ||
		strings.Contains(help, "/run/mithril-agent-status.sock\n") {
		t.Fatalf("root help advertises unsupported legacy commands:\n%s", help)
	}
	for _, statement := range []string{
		"walletless Solana program and index workflows",
		"default workflows load no wallet or signing key",
		"strategy dca-plan ...   plan only; never arms, signs, or submits",
		"optional setup is one generated strategy: sell, buy, sweep",
		"Run these strategy commands as the mithril-agent service identity",
		"shadow review --policy PATH --dir PATH --days N",
		"shadow portfolio --out PATH",
		"shadow allocation --portfolio PATH",
		"shadow research-mcp --policy PATH --journal-dir PATH",
		"proposal check --taker ADDR --input-mint ADDR --output-mint ADDR --amount N",
		"proposal review --request ABSOLUTE_PATH --signer-policy PATH",
		"proposal approval-create --request ABSOLUTE_PATH --authority-policy PATH --out PATH",
		"program inspect --idl PATH --program ADDRESS",
		"program read-account --registry PATH",
		"MCP and Telegram are read-only.",
		"Neither can enable, sign, or submit a trade.",
		"Trading remains Devnet-only",
	} {
		if !strings.Contains(help, statement) {
			t.Fatalf("root help is missing %q:\n%s", statement, help)
		}
	}
}

func TestDemoJSONFailureBoundsProcessStdoutAndStderr(t *testing.T) {
	privatePath := filepath.Join(t.TempDir(), "private-config.json")
	var stdout, stderr bytes.Buffer
	code := runCLI(t.Context(), []string{
		"demo", "--json", "--config", privatePath,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	var failure demoFailure
	if err := json.Unmarshal(stdout.Bytes(), &failure); err != nil {
		t.Fatalf("stdout = %q: %v", stdout.String(), err)
	}
	if failure.ErrorCode != "configuration" ||
		stderr.String() != "mithril-agent: demo failed; see JSON output\n" ||
		strings.Contains(stdout.String(), privatePath) || strings.Contains(stderr.String(), privatePath) {
		t.Fatalf("failure = %+v, stdout = %q, stderr = %q", failure, stdout.String(), stderr.String())
	}
}

func TestDemoRepeatedJSONFlagsFollowLastValue(t *testing.T) {
	for _, test := range []struct {
		name       string
		args       []string
		wantStdout bool
		wantStderr string
	}{
		{
			name:       "JSON enabled last",
			args:       []string{"demo", "--json=false", "--json=true"},
			wantStdout: true,
			wantStderr: "mithril-agent: demo failed; see JSON output\n",
		},
		{
			name:       "JSON disabled last",
			args:       []string{"demo", "--json=true", "--json=false"},
			wantStderr: "mithril-agent: swap demo requires --config, or run: mithril-agent setup\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runCLI(t.Context(), test.args, &stdout, &stderr); code != 1 {
				t.Fatalf("exit code = %d", code)
			}
			if (stdout.Len() != 0) != test.wantStdout || stderr.String() != test.wantStderr {
				t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
			}
			if test.wantStdout {
				var failure demoFailure
				if err := json.Unmarshal(stdout.Bytes(), &failure); err != nil ||
					failure.ErrorCode != "arguments" {
					t.Fatalf("JSON failure = %+v, %v", failure, err)
				}
			}
		})
	}
}

func TestShadowHelpListsResearchAndPerpsPaper(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"shadow", "--help"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "shadow research-mcp --policy PATH --journal-dir PATH") {
		t.Fatalf("shadow help omits research-mcp: %q", output.String())
	}
	if !strings.Contains(output.String(), "shadow perps-paper-run --state-dir PATH") {
		t.Fatalf("shadow help omits perps paper runner: %q", output.String())
	}
	if !strings.Contains(output.String(), "shadow perps-tournament --tape PATH") {
		t.Fatalf("shadow help omits perps tournament: %q", output.String())
	}
	if !strings.Contains(output.String(), "shadow perps-qualify --tape PATH") {
		t.Fatalf("shadow help omits perps qualification: %q", output.String())
	}
}

func TestTopLevelCheckAndDemoAliasesKeepCanonicalHelp(t *testing.T) {
	for _, test := range []struct {
		command string
		want    string
	}{
		{command: "check", want: "Usage: mithril-agent check --config PATH"},
		{command: "demo", want: "Usage: mithril-agent demo --config PATH"},
	} {
		t.Run(test.command, func(t *testing.T) {
			var output bytes.Buffer
			if err := runContext(t.Context(), []string{test.command, "--help"}, &output); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("%s help output = %q", test.command, output.String())
			}
		})
	}
}

func TestProposalHelpListsEverySafeSubcommand(t *testing.T) {
	var output bytes.Buffer
	if err := runContext(t.Context(), []string{"proposal", "--help"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"check", "recheck", "prepare", "review", "key-create", "policy-create", "policy-check", "turnkey-policy"} {
		if !strings.Contains(output.String(), "proposal "+command) {
			t.Errorf("proposal help omitted %q: %q", command, output.String())
		}
	}
}

func TestConfigRequiresExactlyOneProfile(t *testing.T) {
	swap := testSwapProfile("3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh")
	buy := testBuySwapProfile(t)
	legacy := agent.Profile{Name: agent.ProfileTreasurySweepV1}

	for name, cfg := range map[string]config{
		"neither": {},
		"both":    {Profile: legacy, Swap: &swap},
	} {
		t.Run(name, func(t *testing.T) {
			if err := cfg.validateProfileSelection(); err == nil {
				t.Fatal("ambiguous profile selection was accepted")
			}
		})
	}

	for name, cfg := range map[string]config{
		"legacy": {Profile: legacy},
		"swap":   {Swap: &swap},
		"buy":    {Swap: &buy},
	} {
		t.Run(name, func(t *testing.T) {
			if err := cfg.validateProfileSelection(); err != nil {
				t.Fatalf("valid profile selection: %v", err)
			}
		})
	}
}

func TestBuyOperatorInfoUsesTheReadOnlyMCPBoundary(t *testing.T) {
	dir := t.TempDir()
	profile := testBuySwapProfile(t)
	cfg := config{Swap: &profile}
	cfg.Control.StatePath = filepath.Join(dir, "control.json")
	cfg.Journal.Path = filepath.Join(dir, "journal.jsonl")
	configPath := filepath.Join(dir, "config.json")
	writeJSON(t, configPath, cfg)
	provider, err := newOperatorProvider(configPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := provider.Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Profile != orcaswap.BuyProfileName || info.ProfileVersion != orcaswap.BuyProfileVersion ||
		!info.TradingImplemented || info.TelegramHasAuthority || info.MainnetEnabled ||
		info.Execution != "bounded_devnet_only" {
		t.Fatalf("buy MCP info = %+v", info)
	}
}

type boundedStatusReaderStub struct {
	snapshot operatorstatus.Snapshot
	err      error
}

func (s boundedStatusReaderStub) Read() (operatorstatus.Snapshot, error) {
	return s.snapshot, s.err
}

type mutableBoundedStatusReader struct {
	snapshot operatorstatus.Snapshot
}

func (s *mutableBoundedStatusReader) Read() (operatorstatus.Snapshot, error) {
	return s.snapshot, nil
}

func TestSocketOperatorProviderUsesOnlyBoundedStatus(t *testing.T) {
	now := time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC)
	reader := boundedStatusReaderStub{snapshot: operatorstatus.Snapshot{
		Version: operatorstatus.Version, ObservedAt: now,
		Profile: orcaswap.BuyProfileName, ProfileVersion: orcaswap.BuyProfileVersion,
		Cluster: "devnet", Result: execution.Result{Decision: "stopped"},
		Journal: journal.Stats{MaxRecords: 100, MaxBytes: 1024},
		Control: control.Status{Mode: control.ModeNoNewActions},
		Strategy: operatorstatus.StrategyProjection{
			Configured: true, Direction: "buy", InputAmount: 1_000_000,
			DailyCap: 3_000_000, MaxFeeLamports: 100_000, FundedTradesPerDay: 3,
			PriceDirection: "buy_at_or_below", PriceThresholdMicros: 150_000_000,
			SweepConfigured: true, SweepProofValid: true,
			SweepKeepLamports: 100_000_000, SweepMaxLamports: 50_000_000,
			SweepDailyLamports: 100_000_000, SweepActiveAfter: now.Add(time.Hour),
		},
	}}
	provider, err := newSocketOperatorProvider(reader, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	info, err := provider.Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Profile != orcaswap.BuyProfileName || !info.TradingImplemented ||
		info.MainnetEnabled || info.TelegramHasAuthority {
		t.Fatalf("socket MCP info = %+v", info)
	}
	view, err := provider.Status()
	if err != nil {
		t.Fatal(err)
	}
	if view.RunnerState != "recent" || view.Control.Mode != control.ModeNoNewActions ||
		view.Profile != orcaswap.BuyProfileName {
		t.Fatalf("socket MCP status = %+v", view)
	}
	strategy, err := provider.Strategy()
	if err != nil {
		t.Fatal(err)
	}
	if !strategy.Configured || strategy.Direction != "buy SOL with devUSDC" ||
		strategy.InputPerAction != "1.000000 devUSDC" ||
		strategy.DailyCap != "3.000000 devUSDC" ||
		strategy.FundedTradesPerDay != 3 ||
		strategy.PriceRule != "buy_at_or_below $150.000000" ||
		!strategy.SweepConfigured || !strategy.SweepProofValid ||
		strategy.ControlMode != control.ModeNoNewActions {
		t.Fatalf("socket MCP strategy = %+v", strategy)
	}
	encoded, err := json.Marshal(strategy)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"address", "keypair", "/var/", "http://", "https://"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Errorf("bounded strategy contains private locator %q: %s", forbidden, encoded)
		}
	}
	guide := provider.OperatorGuide()
	if guide.SafeLocalCommand != supervisedDemoCommand ||
		!strings.Contains(guide.SafeLocalCommand, "systemctl start --wait") ||
		!strings.Contains(guide.SafeLocalCommand, "mithril-agent-demo.service") ||
		strings.Contains(guide.SafeLocalCommand, "systemd-run") {
		t.Fatalf("socket MCP guide = %+v", guide)
	}
}

func TestStrategyProjectionAttachesOnlyAValidBoundSweep(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	profile := testSwapProfile(reserveOwner)
	sell := filepath.Join(dir, "sell.json")
	writeJSON(t, sell, config{Swap: &profile})
	sweep := filepath.Join(dir, "sweep.json")
	anchor := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC).Unix()
	if err := recordStrategy(strategyPaths{sell: sell, sweep: sweep}); err != nil {
		t.Fatal(err)
	}

	for name, sweepProfile := range map[string]agent.Profile{
		"valid": testSweepProfileForStrategy(reserveOwner, otherOwner, anchor),
		"zero reserve": func() agent.Profile {
			p := testSweepProfileForStrategy(reserveOwner, otherOwner, anchor)
			p.ReserveLamports = 0
			return p
		}(),
		"wrong wallet": testSweepProfileForStrategy(otherOwner, reserveOwner, anchor),
		"malformed": func() agent.Profile {
			p := testSweepProfileForStrategy(reserveOwner, otherOwner, anchor)
			p.ScheduleWindowSeconds = 0
			return p
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			writeJSON(t, sweep, config{Profile: sweepProfile})
			projection, err := strategyProjection(profile, sell)
			if err != nil {
				t.Fatalf("display-only sweep metadata stopped status: %v", err)
			}
			want := name == "valid" || name == "zero reserve"
			if projection.SweepConfigured != want {
				t.Fatalf("sweep configured = %v, want %v", projection.SweepConfigured, want)
			}
		})
	}
}

func TestSocketOperatorProviderPinsStatusIdentity(t *testing.T) {
	now := time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC)
	reader := &mutableBoundedStatusReader{snapshot: operatorstatus.Snapshot{
		Version: operatorstatus.Version, ObservedAt: now,
		Profile: orcaswap.ProfileName, ProfileVersion: orcaswap.ProfileVersion,
		Cluster: "devnet", Result: execution.Result{Decision: "stopped"},
		Journal: journal.Stats{MaxRecords: 100, MaxBytes: 1024},
		Control: control.Status{Mode: control.ModeNoNewActions},
	}}
	provider, err := newSocketOperatorProvider(reader, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	reader.snapshot.Profile = orcaswap.BuyProfileName
	reader.snapshot.ProfileVersion = orcaswap.BuyProfileVersion
	if _, err := provider.Info(); err == nil {
		t.Fatal("socket MCP accepted a changed status identity")
	}
}

func TestStatusAndMCPRequireExactlyOneStatusSource(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "none"},
		{name: "both", args: []string{"--config", "/private/config.json", "--status-socket", "/run/status.sock"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := runStatus(test.args, io.Discard); err == nil {
				t.Fatal("status accepted an ambiguous source")
			}
			if err := runMCP(t.Context(), test.args, io.NopCloser(strings.NewReader("")), io.Discard); err == nil {
				t.Fatal("MCP accepted an ambiguous source")
			}
		})
	}
}

func TestSwapConfigRejectsLegacyProfileAlongsideSwap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	swap := testSwapProfile("3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh")
	writeJSON(t, path, config{
		Profile: agent.Profile{Name: agent.ProfileTreasurySweepV1},
		Swap:    &swap,
	})

	if _, err := readSwapConfig(path); err == nil ||
		!strings.Contains(err.Error(), "exactly one profile") {
		t.Fatalf("mixed-profile config error = %v", err)
	}
}

func TestSwapCommandHelpIsCompleteAndConsistent(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{"setup", "Usage: mithril-agent swap setup"},
		{"bind-providers", "Usage: mithril-agent swap bind-providers --config PATH"},
		{"plan", "Usage: mithril-agent swap plan --config PATH"},
		{"check", "Usage: mithril-agent swap check --config PATH"},
		{"demo", "Usage: mithril-agent swap demo --config PATH"},
		{"run", "Usage: mithril-agent swap run --config PATH"},
		{"enable", "Usage: mithril-agent swap enable --config PATH"},
		{"stop", "Usage: mithril-agent swap stop --config PATH"},
		{"acknowledge", "Usage: mithril-agent swap acknowledge --config PATH"},
		{"drain", "Usage: mithril-agent swap drain --config PATH"},
		{"status", "Usage: mithril-agent swap status --config PATH"},
		{"fingerprint", "Usage: mithril-agent swap fingerprint --config PATH"},
		{"discover", "Usage: mithril-agent swap discover --direction sell|buy (--owner ADDRESS | --wallet-keypair PATH)"},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			var output bytes.Buffer
			if err := runContext(
				t.Context(), []string{"swap", test.command, "--help"}, &output,
			); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("help output = %q", output.String())
			}
		})
	}

	err := runContext(t.Context(), []string{"swap", "unknown"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "run mithril-agent swap --help") {
		t.Fatalf("unknown command error = %v", err)
	}
}

func TestSwapRunRefusesFailedPreflight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	err := runSwapLoop(t.Context(), []string{"--config", path}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "swap preflight failed") {
		t.Fatalf("preflight error = %v", err)
	}
}

func TestEvidenceTrustDomainsMustBeDistinctBoundedNames(t *testing.T) {
	for _, test := range []struct {
		primary   string
		secondary string
		valid     bool
	}{
		{primary: "provider-one", secondary: "provider-two", valid: true},
		{primary: "same-provider", secondary: "same-provider"},
		{primary: "Provider-One", secondary: "provider-two"},
		{primary: "one", secondary: "bad/name"},
		{primary: "", secondary: "provider-two"},
	} {
		cfg := config{}
		cfg.Evidence.PrimaryTrustDomain = test.primary
		cfg.Evidence.SecondaryTrustDomain = test.secondary
		err := cfg.validateEvidenceTrustDomains()
		if (err == nil) != test.valid {
			t.Fatalf("trust domains %q/%q: %v", test.primary, test.secondary, err)
		}
	}
}

func TestClockCheckReportsPolicyFailure(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	profile := testSwapProfile("3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh")
	profile.MaxClockUncertaintyMillis = 500
	writeJSON(t, configPath, config{Swap: &profile})

	previous := clockCheckSample
	clockCheckSample = func() (clockcheck.Sample, error) {
		return clockcheck.Sample{
			WallTime: time.Now().UTC(), BootID: "00000000-0000-0000-0000-000000000001",
			MonotonicNanos: 1, UncertaintyNanos: uint64(600 * time.Millisecond),
		}, nil
	}
	t.Cleanup(func() { clockCheckSample = previous })

	var output bytes.Buffer
	err := run([]string{"clock-check", "--config", configPath}, &output)
	if err == nil || !strings.Contains(err.Error(), "uncertainty exceeds") {
		t.Fatalf("clock-check error = %v", err)
	}
	if !strings.Contains(output.String(), `"status":"failed"`) ||
		!strings.Contains(output.String(), `"uncertainty_nanos":600000000`) ||
		!strings.Contains(output.String(), `"max_uncertainty_nanos":500000000`) {
		t.Fatalf("clock-check output = %s", output.String())
	}
}

func TestDevnetRunRejectsIntervalsBeyondAlertContract(t *testing.T) {
	err := runContext(t.Context(), []string{
		"devnet-run",
		"--config", "/not/read",
		"--interval", "31s",
	}, &bytes.Buffer{})
	if err == nil || err.Error() != "devnet-run interval must be between 1 and 30 seconds" {
		t.Fatalf("interval error = %v", err)
	}
}

func TestDevnetCommandsRejectJournalOverrides(t *testing.T) {
	for _, command := range []string{"devnet-once", "devnet-run", "preflight"} {
		t.Run(command, func(t *testing.T) {
			err := runContext(t.Context(), []string{
				command,
				"--config", "/not/read",
				"--journal", "/alternate/events.jsonl",
			}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
				t.Fatalf("journal override error = %v", err)
			}
		})
	}
}

type recoveringCycleRuntime struct {
	calls      int
	statsCalls int
	stopCalls  int
	cancel     context.CancelFunc
}

type unavailableDependencyRuntime struct {
	recoveringCycleRuntime
	checks int
}

func (r *unavailableDependencyRuntime) Step(
	context.Context,
) (execution.Result, journal.Stats, error) {
	r.calls++
	if r.calls == 2 {
		r.cancel()
	}
	return execution.Result{Decision: "stopped"}, journal.Stats{Records: 7}, nil
}

func (r *unavailableDependencyRuntime) CheckDependencies(context.Context) error {
	r.checks++
	if r.checks == 1 {
		return swapbuilder.ErrQuoteTemporarilyUnavailable
	}
	return nil
}

// stallingCycleRuntime lets the step deadline pass and then reports a failure
// in its OWN words, the way a provider client that gave up does. Nothing in the
// error says "deadline", so the loop's reading of the step context is the only
// thing that can classify it.
type stallingCycleRuntime struct {
	recoveringCycleRuntime
}

func (r *stallingCycleRuntime) Step(
	ctx context.Context,
) (execution.Result, journal.Stats, error) {
	r.calls++
	<-ctx.Done()
	if r.calls == 2 {
		r.cancel()
	}
	return execution.Result{}, journal.Stats{}, errors.New("quote provider gave up")
}

func (r *recoveringCycleRuntime) Step(
	context.Context,
) (execution.Result, journal.Stats, error) {
	r.calls++
	if r.calls <= 2 {
		return execution.Result{}, journal.Stats{}, errors.New("dependency unavailable")
	}
	r.cancel()
	return execution.Result{Decision: "stopped"}, journal.Stats{Records: 7}, nil
}

func (r *recoveringCycleRuntime) Stats() (journal.Stats, error) {
	r.statsCalls++
	return journal.Stats{Records: 6}, nil
}

func (r *recoveringCycleRuntime) ControlStatus() (control.Status, error) {
	return control.Status{Mode: control.ModeNoNewActions}, nil
}

func (r *recoveringCycleRuntime) StopNewActions(string, string) error {
	r.stopCalls++
	return nil
}

type terminalCycleRuntime struct {
	state      *control.StateFile
	cancel     context.CancelFunc
	actionID   string
	decision   string
	verdict    string
	stepErr    error
	statsErr   error
	stopErr    error
	stopCalls  int
	stopReason string
	calls      []string
	recorded   control.Status
}

func (r *terminalCycleRuntime) Step(
	context.Context,
) (execution.Result, journal.Stats, error) {
	r.calls = append(r.calls, "step")
	if r.decision == "complete" {
		r.cancel()
	}
	return execution.Result{
		ActionID: r.actionID,
		Decision: r.decision,
		Verdict:  r.verdict,
	}, journal.Stats{Records: 1}, r.stepErr
}

func (r *terminalCycleRuntime) Stats() (journal.Stats, error) {
	r.calls = append(r.calls, "stats")
	return journal.Stats{Records: 1}, r.statsErr
}

func (r *terminalCycleRuntime) ControlStatus() (control.Status, error) {
	r.calls = append(r.calls, "control")
	return r.state.Status()
}

func (r *terminalCycleRuntime) StopNewActions(actionID, outcome string) error {
	r.calls = append(r.calls, "stop")
	r.stopCalls++
	r.stopReason = outcome
	if r.stopErr != nil {
		return r.stopErr
	}
	if err := r.state.StopForTerminal(actionID, outcome); err != nil {
		return err
	}
	if r.stepErr == nil {
		r.cancel()
	}
	return nil
}

func (r *terminalCycleRuntime) RecordStatus(
	_ time.Time,
	_ execution.Result,
	_ journal.Stats,
	status control.Status,
) (operatorstatus.Action, error) {
	r.calls = append(r.calls, "record")
	r.recorded = status
	return operatorstatus.Action{}, nil
}

func TestTerminalActionDurablyStopsRemainingActivation(t *testing.T) {
	for _, test := range []struct {
		decision string
		verdict  string
	}{
		{decision: "failed", verdict: txflow.VerdictFailed},
		{decision: "halted", verdict: txflow.VerdictDiverged},
	} {
		t.Run(test.decision, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Chmod(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			statePath := filepath.Join(directory, "control.json")
			fingerprint := strings.Repeat("a", 64)
			now := time.Now().UTC()
			if err := control.WriteDevnetActivation(
				statePath, fingerprint, now.Add(-time.Minute), now.Add(time.Hour), 2, "test",
			); err != nil {
				t.Fatal(err)
			}
			state, err := control.NewStateFile(statePath, fingerprint, false)
			if err != nil {
				t.Fatal(err)
			}
			actionID := strings.Repeat("b", 64)
			blocked, err := state.WithSendBarrier(actionID, func() error { return nil })
			if err != nil || blocked {
				t.Fatalf("consume first action: blocked=%v err=%v", blocked, err)
			}
			status, err := state.Status()
			if err != nil || !status.RecoveryPending || status.Mode != control.ModeNoNewActions {
				t.Fatalf("remaining activation before terminal result = %+v, %v", status, err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			runtime := &terminalCycleRuntime{
				state: state, cancel: cancel, actionID: actionID,
				decision: test.decision, verdict: test.verdict,
			}
			var output bytes.Buffer
			if err := runDevnetCycles(
				ctx, runtime, runmetrics.New(now), make(chan error), &output,
				time.Millisecond, devnetStepTimeout,
			); err != nil {
				t.Fatal(err)
			}
			if runtime.stopCalls != 1 || runtime.stopReason != test.decision ||
				strings.Join(runtime.calls, ",") != "step,stop,control,record" {
				t.Fatalf("terminal calls = %v, stop calls = %d", runtime.calls, runtime.stopCalls)
			}
			if runtime.recorded.Mode != control.ModeNoNewActions {
				t.Fatalf("recorded control = %+v", runtime.recorded)
			}
			blocked, err = state.NoNewActions()
			if err != nil || !blocked {
				t.Fatalf("terminal state blocked=%v err=%v", blocked, err)
			}
			blocked, err = state.WithSendBarrier(strings.Repeat("c", 64), func() error { return nil })
			if err != nil || !blocked {
				t.Fatalf("second action blocked=%v err=%v", blocked, err)
			}
			var result execution.Result
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &result); err != nil {
				t.Fatal(err)
			}
			if result.Decision != test.decision || result.Verdict != test.verdict {
				t.Fatalf("terminal output = %+v", result)
			}
		})
	}
}

func TestCompleteActionDoesNotStopRemainingActivation(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(directory, "control.json")
	fingerprint := strings.Repeat("a", 64)
	now := time.Now().UTC()
	if err := control.WriteDevnetActivation(
		statePath, fingerprint, now.Add(-time.Minute), now.Add(time.Hour), 2, "test",
	); err != nil {
		t.Fatal(err)
	}
	state, err := control.NewStateFile(statePath, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	runtime := &terminalCycleRuntime{
		state: state, cancel: cancel, actionID: strings.Repeat("b", 64),
		decision: "complete", verdict: txflow.VerdictFinalized,
	}
	if err := runDevnetCycles(
		ctx, runtime, runmetrics.New(now), make(chan error), io.Discard,
		time.Millisecond, devnetStepTimeout,
	); err != nil {
		t.Fatal(err)
	}
	if runtime.stopCalls != 0 || strings.Join(runtime.calls, ",") != "step,control,record" {
		t.Fatalf("complete calls = %v, stop calls = %d", runtime.calls, runtime.stopCalls)
	}
	if runtime.recorded.Mode != control.ModeDevnetEnabled {
		t.Fatalf("recorded control = %+v", runtime.recorded)
	}
}

func TestTerminalStopFailureExitsBeforeStatusOrOutput(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	state, err := control.NewStateFile(
		filepath.Join(directory, "control.json"), strings.Repeat("a", 64), false,
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &terminalCycleRuntime{
		state: state, actionID: strings.Repeat("b", 64), decision: "failed",
		verdict: txflow.VerdictFailed, stopErr: errors.New("disk failure"),
	}
	var output bytes.Buffer
	err = runDevnetCycles(
		t.Context(), runtime, runmetrics.New(time.Now().UTC()), make(chan error),
		&output, time.Millisecond, devnetStepTimeout,
	)
	if err == nil || err.Error() != "stop new actions after terminal result" {
		t.Fatalf("terminal stop error = %v", err)
	}
	if strings.Join(runtime.calls, ",") != "step,stop" || output.Len() != 0 {
		t.Fatalf("calls=%v output=%q", runtime.calls, output.String())
	}
}

func TestTerminalResultStopsBeforeJournalStatsFailureExits(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	state, err := control.NewStateFile(
		filepath.Join(directory, "control.json"), strings.Repeat("a", 64), false,
	)
	if err != nil {
		t.Fatal(err)
	}
	statsErr := errors.New("journal stats failed")
	ctx, cancel := context.WithCancel(t.Context())
	runtime := &terminalCycleRuntime{
		state: state, cancel: cancel, actionID: strings.Repeat("b", 64),
		decision: "halted", verdict: txflow.VerdictDiverged,
		stepErr: statsErr, statsErr: statsErr,
	}
	var output bytes.Buffer
	err = runDevnetCycles(
		ctx, runtime, runmetrics.New(time.Now().UTC()), make(chan error),
		&output, time.Millisecond, devnetStepTimeout,
	)
	if err == nil || err.Error() != "inspect journal after failed cycle" {
		t.Fatalf("stats failure = %v", err)
	}
	status, statusErr := state.Status()
	if statusErr != nil || status.TerminalOutcome != "halted" {
		t.Fatalf("terminal control = %+v, %v", status, statusErr)
	}
	if strings.Join(runtime.calls, ",") != "step,stop,stats" || output.Len() != 0 {
		t.Fatalf("calls=%v output=%q", runtime.calls, output.String())
	}
}

func TestCanceledLoopStillStopsDurableTerminalResult(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	state, err := control.NewStateFile(
		filepath.Join(directory, "control.json"), strings.Repeat("a", 64), false,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	runtime := &terminalCycleRuntime{
		state: state, cancel: cancel, actionID: strings.Repeat("b", 64),
		decision: "failed", verdict: txflow.VerdictFailed,
		stepErr: context.Canceled,
	}
	if err := runDevnetCycles(
		ctx, runtime, runmetrics.New(time.Now().UTC()), make(chan error),
		io.Discard, time.Millisecond, devnetStepTimeout,
	); err != nil {
		t.Fatal(err)
	}
	status, err := state.Status()
	if err != nil || status.TerminalOutcome != "failed" {
		t.Fatalf("terminal control = %+v, %v", status, err)
	}
	if strings.Join(runtime.calls, ",") != "step,stop" {
		t.Fatalf("calls = %v", runtime.calls)
	}
}

type shutdownControlStub struct {
	called bool
	err    error
}

func (s *shutdownControlStub) StopPreservingRecovery(reason string) error {
	s.called = reason == "runner shutdown"
	return s.err
}

type shutdownRuntimeStub struct {
	called bool
	err    error
}

func (s *shutdownRuntimeStub) Close() error {
	s.called = true
	return s.err
}

func TestRunnerShutdownAlwaysRevokesAndCloses(t *testing.T) {
	for _, test := range []struct {
		name       string
		stopErr    error
		closeErr   error
		wantErrors []string
	}{
		{name: "success"},
		{
			name: "both fail", stopErr: errors.New("stop failed"),
			closeErr: errors.New("close failed"),
			wantErrors: []string{
				"original runner failure",
				"stop new actions during runner shutdown",
				"close agent journal",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			control := &shutdownControlStub{err: test.stopErr}
			runtime := &shutdownRuntimeStub{err: test.closeErr}
			var runErr error
			if len(test.wantErrors) > 0 {
				runErr = errors.New("original runner failure")
			}

			shutdownRunner(&runErr, control, runtime)

			if !control.called || !runtime.called {
				t.Fatalf("stop called=%t close called=%t", control.called, runtime.called)
			}
			for _, want := range test.wantErrors {
				if runErr == nil || !strings.Contains(runErr.Error(), want) {
					t.Fatalf("shutdown error %q does not contain %q", runErr, want)
				}
			}
			if len(test.wantErrors) == 0 && runErr != nil {
				t.Fatalf("successful shutdown error = %v", runErr)
			}
		})
	}
}

func TestRunnerShutdownPreservesPendingRecovery(t *testing.T) {
	directory := t.TempDir()
	fingerprint := strings.Repeat("a", 64)
	path := filepath.Join(directory, "control.json")
	now := time.Now().UTC()
	if err := control.WriteDevnetActivation(
		path, fingerprint, now.Add(-time.Second), now.Add(time.Hour), 2, "test",
	); err != nil {
		t.Fatal(err)
	}
	state, err := control.NewStateFile(path, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if blocked, err := state.WithSendBarrier(
		strings.Repeat("b", 64), func() error { return nil },
	); err != nil || blocked {
		t.Fatalf("send barrier = blocked %t, error %v", blocked, err)
	}
	runtime := &shutdownRuntimeStub{}
	var runErr error
	shutdownRunner(&runErr, state, runtime)
	if !runtime.called || runErr != nil {
		t.Fatalf("shutdown runtime called=%t error=%v", runtime.called, runErr)
	}
	status, err := state.Status()
	if err != nil || !status.RecoveryPending || status.Mode != control.ModeNoNewActions {
		t.Fatalf("shutdown erased recovery: status=%+v error=%v", status, err)
	}
}

func TestDevnetLoopStaysObservableAcrossCycleFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	runtime := &recoveringCycleRuntime{cancel: cancel}
	var output bytes.Buffer
	if err := runDevnetCycles(
		ctx,
		runtime,
		runmetrics.New(time.Now().UTC()),
		make(chan error),
		&output,
		time.Millisecond,
		devnetStepTimeout,
	); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 || runtime.statsCalls != 2 {
		t.Fatalf("cycles=%d stats=%d output=%q", len(lines), runtime.statsCalls, output.String())
	}
	if runtime.stopCalls != 0 {
		t.Fatalf("synthetic failure stop calls = %d", runtime.stopCalls)
	}
	for index, line := range lines {
		var result execution.Result
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			t.Fatal(err)
		}
		if index < 2 &&
			(result.Decision != "failed" || result.Reason != "operation_failed") {
			t.Fatalf("failure cycle %d = %+v", index, result)
		}
		if index == 2 && result.Decision != "stopped" {
			t.Fatalf("recovery cycle = %+v", result)
		}
	}
}

// A provider that gives up after the step deadline reports a failure in its own
// words, with nothing in the error saying "deadline". Only the step context can
// classify it, and without that read it lands in "operation_failed" — the one
// reason that says nothing about what to fix — so the timeout alert never
// fires and a hung dependency looks like a broken agent.
func TestAStalledStepIsReportedAsATimeoutNotAGenericFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	runtime := &stallingCycleRuntime{recoveringCycleRuntime{cancel: cancel}}
	var output bytes.Buffer
	if err := runDevnetCycles(
		ctx, runtime, runmetrics.New(time.Now().UTC()), make(chan error), &output,
		time.Millisecond, 20*time.Millisecond,
	); err != nil {
		t.Fatal(err)
	}
	var result execution.Result
	first := strings.SplitN(strings.TrimSpace(output.String()), "\n", 2)[0]
	if err := json.Unmarshal([]byte(first), &result); err != nil {
		t.Fatal(err)
	}
	if result.Reason != "operation_timeout" {
		t.Fatalf("stalled cycle reason = %q, want operation_timeout", result.Reason)
	}
}

func TestDevnetLoopReportsQuoteServiceLossWhileStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	runtime := &unavailableDependencyRuntime{
		recoveringCycleRuntime: recoveringCycleRuntime{cancel: cancel},
	}
	var output bytes.Buffer
	if err := runDevnetCycles(
		ctx, runtime, runmetrics.New(time.Now().UTC()), make(chan error), &output,
		time.Millisecond, devnetStepTimeout,
	); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 || runtime.checks != 2 || runtime.statsCalls != 1 {
		t.Fatalf(
			"cycles=%d checks=%d stats=%d output=%q",
			len(lines), runtime.checks, runtime.statsCalls, output.String(),
		)
	}
	var failed execution.Result
	if err := json.Unmarshal([]byte(lines[0]), &failed); err != nil {
		t.Fatal(err)
	}
	if failed.Decision != "failed" || failed.Reason != "quote_unavailable" {
		t.Fatalf("quote outage cycle = %+v", failed)
	}
}

func TestDependencyProbeDoesNotOverrideActionResult(t *testing.T) {
	for _, decision := range []string{
		"pending", "executing", "complete", "canceled", "failed", "halted",
	} {
		if dependencyProbeSafe(execution.Result{Decision: decision}) {
			t.Fatalf("dependency probe allowed during %q", decision)
		}
	}
	for _, decision := range []string{"stopped", "waiting", "degraded"} {
		if !dependencyProbeSafe(execution.Result{Decision: decision}) {
			t.Fatalf("dependency probe skipped during %q", decision)
		}
	}
}

func TestCycleFailureReasonIsBoundedAndTimeoutAware(t *testing.T) {
	if got := cycleFailureReason(errors.New("provider included a secret"), false); got != "operation_failed" {
		t.Fatalf("generic failure category = %q", got)
	}
	if got := cycleFailureReason(context.DeadlineExceeded, false); got != "operation_timeout" {
		t.Fatalf("deadline failure category = %q", got)
	}
	if got := cycleFailureReason(errors.New("wrapped timeout"), true); got != "operation_timeout" {
		t.Fatalf("observed timeout category = %q", got)
	}
	if got := cycleFailureReason(swapbuilder.ErrQuoteTemporarilyUnavailable, false); got != "quote_unavailable" {
		t.Fatalf("quote failure category = %q", got)
	}
}

func TestStrictInputRejectsUnknownFieldsAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.json")
	if err := os.WriteFile(path, []byte(`{"known":1,"unknown":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var value struct {
		Known int `json:"known"`
	}
	if err := readStrictJSON(path, &value); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown-field error = %v", err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := readStrictJSON(link, &value); err == nil || !strings.Contains(err.Error(), "not a symlink") {
		t.Fatalf("symlink error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"known":1,"Known":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := readStrictJSON(path, &value); err == nil {
		t.Fatal("duplicate input field was accepted")
	}
}

func TestDevnetRejectsOtherClustersBeforeRPCSetup(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	journalPath := filepath.Join(dir, "events.jsonl")
	cfg := config{Profile: agent.Profile{
		Name:                         agent.ProfileTreasurySweepV1,
		Version:                      1,
		Cluster:                      "mainnet-beta",
		Source:                       "11111111111111111111111111111111",
		Destination:                  "SysvarC1ock11111111111111111111111111111111",
		ReserveLamports:              100,
		MinTransferLamports:          10,
		MaxTransferLamports:          50,
		DailyCapLamports:             80,
		MaxFeeLamports:               5,
		ScheduleWindowSeconds:        3_600,
		ScheduleAnchorUnix:           time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		MaxClockUncertaintyMillis:    100,
		MaxObservationAgeSeconds:     30,
		MinHealthyObservationSeconds: 5,
		MinHealthySlotAdvance:        1,
		MaxNodeLagSlots:              150,
		MaxReconciliationSeconds:     180,
	}}
	cfg.Journal.Path = journalPath
	writeJSON(t, configPath, cfg)
	err := runContext(t.Context(), []string{
		"devnet-once",
		"--config", configPath,
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "restricted to devnet") {
		t.Fatalf("non-devnet error = %v", err)
	}
}

func TestMithrilMCPEnvironmentBindsLocalAndReferenceRPCs(t *testing.T) {
	got := mithrilMCPEnvironment("http://127.0.0.1:8899", "https://reference.invalid")
	want := []string{
		"MITHRIL_REFERENCE_RPC_URL=https://reference.invalid",
		"MITHRIL_RPC_URL=http://127.0.0.1:8899",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("MCP environment = %q, want %q", got, want)
	}
}

func TestDevnetControlCommandsDefaultStopAndBoundEnablement(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	statePath := filepath.Join(dir, "control.json")
	cfg := config{Profile: agent.Profile{
		Name:                         agent.ProfileTreasurySweepV1,
		Version:                      1,
		Cluster:                      "devnet",
		Source:                       "11111111111111111111111111111111",
		Destination:                  "SysvarC1ock11111111111111111111111111111111",
		ReserveLamports:              100,
		MinTransferLamports:          10,
		MaxTransferLamports:          50,
		DailyCapLamports:             80,
		MaxFeeLamports:               5,
		ScheduleWindowSeconds:        3_600,
		ScheduleAnchorUnix:           time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		MaxClockUncertaintyMillis:    100,
		MaxObservationAgeSeconds:     30,
		MinHealthyObservationSeconds: 5,
		MinHealthySlotAdvance:        1,
		MaxNodeLagSlots:              150,
		MaxReconciliationSeconds:     180,
	}}
	cfg.Control.StatePath = statePath
	writeJSON(t, configPath, cfg)
	withDirectOperatorControl(t, cfg)

	var stopped bytes.Buffer
	if err := run([]string{
		"devnet-stop",
		"--config", configPath,
		"--reason", "operator test",
	}, &stopped); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stopped.String(), `"mode":"no_new_actions"`) {
		t.Fatalf("stop output = %s", stopped.String())
	}
	var stoppedStatus bytes.Buffer
	if err := run([]string{
		"devnet-status",
		"--config", configPath,
	}, &stoppedStatus); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stoppedStatus.String(), `"mode":"no_new_actions"`) ||
		strings.Contains(stoppedStatus.String(), "operator test") ||
		strings.Contains(stoppedStatus.String(), cfg.Profile.Source) {
		t.Fatalf("stopped status output = %s", stoppedStatus.String())
	}
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("control state permissions = %o", info.Mode().Perm())
	}

	var enabled bytes.Buffer
	if err := run([]string{
		"devnet-enable",
		"--config", configPath,
		"--duration", "1h",
		"--reason", "bounded test",
	}, &enabled); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enabled.String(), `"mode":"devnet_enabled"`) {
		t.Fatalf("enable output = %s", enabled.String())
	}
	if !strings.Contains(enabled.String(), `"max_actions":1`) {
		t.Fatalf("enable output omitted safe action cap = %s", enabled.String())
	}
	var enabledStatus bytes.Buffer
	if err := run([]string{
		"devnet-status",
		"--config", configPath,
	}, &enabledStatus); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enabledStatus.String(), `"mode":"devnet_enabled"`) ||
		!strings.Contains(enabledStatus.String(), `"expires_at"`) ||
		!strings.Contains(enabledStatus.String(), `"max_actions":1`) ||
		!strings.Contains(enabledStatus.String(), `"remaining_actions":1`) ||
		strings.Contains(enabledStatus.String(), "bounded test") ||
		strings.Contains(enabledStatus.String(), cfg.Profile.Source) {
		t.Fatalf("enabled status output = %s", enabledStatus.String())
	}
	fingerprint, err := cfg.Profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	state, err := control.NewStateFile(statePath, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := state.NoNewActions()
	if err != nil || blocked {
		t.Fatalf("enabled control state = %v, %v", blocked, err)
	}
	if err := state.StopForTerminal(strings.Repeat("d", 64), "failed"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{
		"devnet-enable",
		"--config", configPath,
		"--duration", "1h",
		"--reason", "must not clear terminal state",
	}, io.Discard); err == nil {
		t.Fatal("legacy enable cleared an unacknowledged terminal action")
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("legacy enable changed terminal control state")
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSwapOperatorCommandsExposeReadOnlyBoundary(t *testing.T) {
	dir := t.TempDir()
	profile := testSwapProfile("3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh")
	cfg := config{Swap: &profile}
	cfg.Evidence.PrimaryTrustDomain = "provider-one"
	cfg.Evidence.SecondaryTrustDomain = "provider-two"
	cfg.Control.StatePath = filepath.Join(dir, "control.json")
	cfg.Journal.Path = filepath.Join(dir, "journal.jsonl")
	configPath := filepath.Join(dir, "config.json")
	writeJSON(t, configPath, cfg)
	withDirectOperatorControl(t, cfg)

	provider, err := newOperatorProvider(configPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := provider.Info()
	if err != nil {
		t.Fatal(err)
	}
	if !info.TradingImplemented || info.TelegramHasAuthority || info.MainnetEnabled ||
		info.Profile != orcaswap.ProfileName || info.Execution != "bounded_devnet_only" {
		t.Fatalf("swap MCP info = %+v", info)
	}
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := operatorstatus.Write(operatorstatus.Path(cfg.Journal.Path), operatorstatus.Snapshot{
		Version: operatorstatus.Version, ObservedAt: time.Now().UTC(),
		Profile: profile.Name, ProfileVersion: profile.Version, Cluster: profile.Cluster,
		Result:  execution.Result{Decision: "stopped"},
		Journal: journal.Stats{MaxRecords: 100, MaxBytes: 1 << 20},
		Control: control.Status{Mode: control.ModeNoNewActions},
	}); err != nil {
		t.Fatal(err)
	}
	activeJournal, err := journal.Open(cfg.Journal.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer activeJournal.Close()
	// An activation may grant several trades, which is what makes the agent
	// autonomous, but the grant itself stays bounded at both ends.
	for _, invalid := range []string{"0", "101"} {
		if err := run([]string{
			"swap", "enable", "--config", configPath,
			"--duration", "5m", "--max-actions", invalid, "--reason", "operator test",
		}, io.Discard); err == nil || !strings.Contains(err.Error(), "between 1 and 100") {
			t.Fatalf("--max-actions %s error = %v", invalid, err)
		}
	}
	var enabled bytes.Buffer
	if err := run([]string{
		"swap", "enable", "--config", configPath,
		"--duration", "3h", "--max-actions", "3", "--reason", "operator test",
	}, &enabled); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enabled.String(), `"mode":"devnet_enabled"`) ||
		!strings.Contains(enabled.String(), `"max_actions":3`) ||
		strings.Contains(enabled.String(), cfg.Swap.Route.Owner) {
		t.Fatalf("swap enable output = %s", enabled.String())
	}
	// A live activation may not be silently replaced: raising the grant has to
	// go through an explicit stop, so authority never widens by accident.
	if err := run([]string{
		"swap", "enable", "--config", configPath,
		"--duration", "3h", "--max-actions", "4", "--reason", "operator test",
	}, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "stop the current activation") {
		t.Fatalf("re-enabling over a live activation = %v", err)
	}
	err = run([]string{
		"swap", "acknowledge", "--config", configPath,
		"--action-id", strings.Repeat("a", 64), "--outcome", "halted",
		"--reason", "operator review",
	}, io.Discard)
	if err == nil || err.Error() != "stop the swap runner before acknowledging its terminal action" {
		t.Fatalf("online acknowledgement error = %v", err)
	}
	var status bytes.Buffer
	if err := run([]string{"swap", "status", "--config", configPath}, &status); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.String(), `"runner_state":"recent"`) ||
		!strings.Contains(status.String(), `"mode":"devnet_enabled"`) ||
		strings.Contains(status.String(), cfg.Swap.Route.Owner) {
		t.Fatalf("swap status output = %s", status.String())
	}
	state, err := control.NewStateFile(cfg.Control.StatePath, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if blocked, err := state.NoNewActions(); err != nil || blocked {
		t.Fatalf("swap activation = %v, %v", blocked, err)
	}
	type statusWriteResult struct {
		observedAt time.Time
		err        error
	}
	statusWritten := make(chan statusWriteResult, 1)
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			status, err := state.Status()
			if err != nil {
				statusWritten <- statusWriteResult{err: err}
				return
			}
			if status.Mode == control.ModeNoNewActions {
				observedAt := time.Now().UTC()
				err := operatorstatus.Write(
					operatorstatus.Path(cfg.Journal.Path),
					operatorstatus.Snapshot{
						Version: operatorstatus.Version, ObservedAt: observedAt,
						Profile: profile.Name, ProfileVersion: profile.Version,
						Cluster: profile.Cluster, Result: execution.Result{Decision: "stopped"},
						Journal: journal.Stats{MaxRecords: 100, MaxBytes: 1 << 20},
						Control: status,
					},
				)
				statusWritten <- statusWriteResult{observedAt: observedAt, err: err}
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		statusWritten <- statusWriteResult{
			err: errors.New("drain stop state was not observed"),
		}
	}()
	var drained bytes.Buffer
	if err := runContext(t.Context(), []string{
		"swap", "drain", "--config", configPath,
		"--timeout", "4m", "--reason", "operator test",
	}, &drained); err != nil {
		t.Fatal(err)
	}
	acknowledgment := <-statusWritten
	if acknowledgment.err != nil {
		t.Fatal(acknowledgment.err)
	}
	written, err := operatorstatus.Read(operatorstatus.Path(cfg.Journal.Path))
	if err != nil {
		t.Fatal(err)
	}
	if !written.ObservedAt.Equal(acknowledgment.observedAt) ||
		written.Control.Mode != control.ModeNoNewActions || written.Result.Decision != "stopped" {
		t.Fatalf("drain acknowledgement = %+v", written)
	}
	if !strings.Contains(drained.String(), `"mode":"no_new_actions"`) ||
		!strings.Contains(drained.String(), `"drained":true`) ||
		strings.Contains(drained.String(), "operator test") {
		t.Fatalf("swap drain output = %s", drained.String())
	}
	if blocked, err := state.NoNewActions(); err != nil || !blocked {
		t.Fatalf("drained control state = %v, %v", blocked, err)
	}
}

func TestSwapAcknowledgeBoundsInvalidJournalError(t *testing.T) {
	dir := t.TempDir()
	profile := testSwapProfile("3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh")
	cfg := config{Swap: &profile}
	cfg.Evidence.PrimaryTrustDomain = "provider-one"
	cfg.Evidence.SecondaryTrustDomain = "provider-two"
	cfg.Control.StatePath = filepath.Join(dir, "control.json")
	cfg.Journal.Path = filepath.Join(dir, "journal.jsonl")
	configPath := filepath.Join(dir, "config.json")
	writeJSON(t, configPath, cfg)
	if err := os.WriteFile(cfg.Journal.Path, []byte("invalid journal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{
		"swap", "acknowledge", "--config", configPath,
		"--action-id", strings.Repeat("a", 64), "--outcome", "halted",
		"--reason", "operator review",
	}, io.Discard)
	if err == nil || err.Error() != "terminal acknowledgement journal is unavailable or invalid" {
		t.Fatalf("invalid journal acknowledgement error = %v", err)
	}
}

func TestSwapAcknowledgeOfflineWorkflowAndStatus(t *testing.T) {
	dir := t.TempDir()
	profile := testSwapProfile("3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh")
	cfg := config{Swap: &profile}
	cfg.Evidence.PrimaryTrustDomain = "provider-one"
	cfg.Evidence.SecondaryTrustDomain = "provider-two"
	cfg.Control.StatePath = filepath.Join(dir, "control.json")
	cfg.Journal.Path = filepath.Join(dir, "journal.jsonl")
	configPath := filepath.Join(dir, "config.json")
	writeJSON(t, configPath, cfg)
	withDirectOperatorControl(t, cfg)

	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	windowStart, windowEnd, err := profile.Window(now)
	if err != nil {
		t.Fatal(err)
	}
	actionID, err := orcaswap.ComputeActionID(fingerprint, windowStart)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(cfg.Journal.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		typeName string
		payload  any
	}{
		{swaprun.EventStarted, map[string]any{
			"profile_sha256": fingerprint, "schedule_window_start_unix": windowStart,
			"schedule_window_end_unix": windowEnd, "observation_slot": 100,
		}},
		{swaprun.EventBuilt, map[string]any{}},
		{swaprun.EventSimulated, map[string]any{}},
		{swaprun.EventSigned, map[string]any{"response": map[string]any{
			"signature": "signature", "transaction_sha256": strings.Repeat("1", 64),
		}}},
		{swaprun.EventPreSendObserved, agent.NodeObservation{}},
		{swaprun.EventSendStarted, map[string]any{
			"signature": "signature", "transaction_sha256": strings.Repeat("1", 64),
		}},
		{swaprun.EventSubmitted, txflow.Submission{Signature: "signature", State: txflow.StateAccepted}},
		{swaprun.EventReconciled, txflow.Reconciliation{
			Signature: "signature", Verdict: txflow.VerdictDiverged,
		}},
	} {
		if _, err := store.Append(now, event.typeName, actionID, event.payload); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := control.NewStateFile(cfg.Control.StatePath, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.StopForTerminal(actionID, "halted"); err != nil {
		t.Fatal(err)
	}
	terminalControl, err := state.Status()
	if err != nil {
		t.Fatal(err)
	}
	if err := operatorstatus.Write(operatorstatus.Path(cfg.Journal.Path), operatorstatus.Snapshot{
		Version: operatorstatus.Version, ObservedAt: now,
		Profile: profile.Name, ProfileVersion: profile.Version, Cluster: profile.Cluster,
		Result: execution.Result{
			ActionID: actionID, Decision: "halted", Verdict: "diverged",
			AmountLamports: profile.InputLamports, InputAmount: profile.InputLamports,
			InputAsset: "SOL", OutputAsset: "devUSDC", MinimumOutput: 1,
			Signature: "signature", Submitted: true, PendingSinceUnix: now.Unix(),
			ReconciliationTimeoutSeconds: profile.MaxReconciliationSeconds,
		},
		Journal: stats, Control: terminalControl,
	}); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := run([]string{
		"swap", "acknowledge", "--config", configPath,
		"--action-id", actionID, "--outcome", "halted",
		"--reason", "reviewed terminal evidence",
	}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"runner_restart_required":false`) ||
		!strings.Contains(output.String(), `"execution_permanently_blocked":true`) ||
		strings.Contains(output.String(), "attention_required") ||
		strings.Contains(output.String(), "reviewed terminal evidence") {
		t.Fatalf("acknowledgement output = %s", output.String())
	}
	status, err := state.Status()
	if err != nil || status.TerminalActionID != actionID || status.TerminalOutcome != "halted" {
		t.Fatalf("acknowledged control = %+v, %v", status, err)
	}
	var beforeRestart bytes.Buffer
	if err := run([]string{"swap", "status", "--config", configPath}, &beforeRestart); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(beforeRestart.String(), `"attention_required":true`) ||
		strings.Contains(beforeRestart.String(), `"last_action_acknowledged":true`) {
		t.Fatalf("pre-restart status = %s", beforeRestart.String())
	}

	err = run([]string{
		"swap", "enable", "--config", configPath,
		"--duration", "5m", "--reason", "must remain blocked",
	}, io.Discard)
	if err == nil || err.Error() != "this swap setup is permanently blocked after a halted action" {
		t.Fatalf("halted setup enable error = %v", err)
	}
}

func TestSwapEnableRejectsCompletedCurrentWindowFromRunnerStatus(t *testing.T) {
	dir := t.TempDir()
	profile := testSwapProfile("3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh")
	cfg := config{Swap: &profile}
	cfg.Control.StatePath = filepath.Join(dir, "control.json")
	cfg.Journal.Path = filepath.Join(dir, "journal.jsonl")
	now := time.Now().UTC()
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	windowStart, _, err := profile.Window(now)
	if err != nil {
		t.Fatal(err)
	}
	actionID, err := orcaswap.ComputeActionID(fingerprint, windowStart)
	if err != nil {
		t.Fatal(err)
	}
	statusPath := operatorstatus.Path(cfg.Journal.Path)
	terminal := operatorstatus.Snapshot{
		Version: operatorstatus.Version, ObservedAt: now.Add(-time.Second),
		Profile: profile.Name, ProfileVersion: profile.Version, Cluster: profile.Cluster,
		Result: execution.Result{
			ActionID: actionID, Decision: "complete", Verdict: "finalized",
			AmountLamports: profile.InputLamports, InputAmount: profile.InputLamports,
			InputAsset: "SOL", OutputAsset: "devUSDC",
			MinimumOutput: 1, OutputAmount: 1, Signature: "signature", Submitted: true,
			PendingSinceUnix:             now.Add(-time.Second).Unix(),
			ReconciliationTimeoutSeconds: profile.MaxReconciliationSeconds,
		},
		Journal: journal.Stats{MaxRecords: 100, MaxBytes: 1 << 20},
		Control: control.Status{Mode: control.ModeNoNewActions},
	}
	if err := operatorstatus.Write(statusPath, terminal); err != nil {
		t.Fatal(err)
	}
	idle := terminal
	idle.ObservedAt = now
	idle.Result = execution.Result{Decision: "stopped"}
	if err := operatorstatus.Write(statusPath, idle); err != nil {
		t.Fatal(err)
	}
	if _, err := requireRecentSwapRunner(cfg, now); err == nil ||
		!strings.Contains(err.Error(), "current swap window is already complete") {
		t.Fatalf("current-window enable error = %v", err)
	}
}

func TestSwapDrainRequiresPostStopRunnerAcknowledgement(t *testing.T) {
	requestedAt := time.Unix(100, 0).UTC()
	tests := []struct {
		name string
		view operatorstatus.View
		want string
	}{
		{
			name: "old status",
			view: operatorstatus.View{
				RunnerState: "recent", ObservedAt: requestedAt.Add(-time.Second),
				Result: execution.Result{Decision: "stopped"},
			},
			want: "waiting",
		},
		{
			name: "post-stop acknowledgement",
			view: operatorstatus.View{
				RunnerState: "recent", ObservedAt: requestedAt,
				Result: execution.Result{Decision: "stopped"},
			},
			want: "drained",
		},
		{
			name: "terminal attention",
			view: operatorstatus.View{
				RunnerState: "recent", ObservedAt: requestedAt.Add(time.Second),
				Result: execution.Result{Decision: "halted"},
			},
			want: "attention",
		},
		{
			name: "durable terminal attention after idle cycle",
			view: operatorstatus.View{
				RunnerState: "recent", ObservedAt: requestedAt.Add(time.Second),
				Result: execution.Result{Decision: "stopped"},
				Control: control.Status{
					Mode:             control.ModeNoNewActions,
					TerminalActionID: strings.Repeat("a", 64), TerminalOutcome: "halted",
				},
			},
			want: "attention",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := swapDrainState(test.view, requestedAt); got != test.want {
				t.Fatalf("drain state = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSwapEnableRequiresRunningHealthyLoop(t *testing.T) {
	dir := t.TempDir()
	profile := testSwapProfile("3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh")
	cfg := config{Swap: &profile}
	cfg.Evidence.PrimaryTrustDomain = "provider-one"
	cfg.Evidence.SecondaryTrustDomain = "provider-two"
	cfg.Control.StatePath = filepath.Join(dir, "control.json")
	cfg.Journal.Path = filepath.Join(dir, "journal.jsonl")
	configPath := filepath.Join(dir, "config.json")
	writeJSON(t, configPath, cfg)
	withDirectOperatorControl(t, cfg)

	err := run([]string{
		"swap", "enable", "--config", configPath,
		"--duration", "5m", "--reason", "operator test",
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "start the swap runner") {
		t.Fatalf("enable without runner error = %v", err)
	}
	if _, err := os.Lstat(cfg.Control.StatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed enable wrote control state: %v", err)
	}
}

func TestSwapDrainReportsRevokedAuthorityWhenRunnerNeedsAttention(t *testing.T) {
	dir := t.TempDir()
	profile := testSwapProfile("3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh")
	cfg := config{Swap: &profile}
	cfg.Evidence.PrimaryTrustDomain = "provider-one"
	cfg.Evidence.SecondaryTrustDomain = "provider-two"
	cfg.Control.StatePath = filepath.Join(dir, "control.json")
	cfg.Journal.Path = filepath.Join(dir, "journal.jsonl")
	configPath := filepath.Join(dir, "config.json")
	writeJSON(t, configPath, cfg)
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	state, err := control.NewStateFile(cfg.Control.StatePath, fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := control.WriteDevnetActivation(
		cfg.Control.StatePath,
		fingerprint,
		now.Add(-time.Second),
		now.Add(time.Hour),
		1,
		"test activation",
	); err != nil {
		t.Fatal(err)
	}
	statusWritten := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			status, statusErr := state.Status()
			if statusErr != nil {
				statusWritten <- statusErr
				return
			}
			if status.Mode == control.ModeNoNewActions {
				statusWritten <- operatorstatus.Write(
					operatorstatus.Path(cfg.Journal.Path),
					operatorstatus.Snapshot{
						Version: operatorstatus.Version, ObservedAt: time.Now().UTC(),
						Profile: profile.Name, ProfileVersion: profile.Version,
						Cluster: profile.Cluster, Result: execution.Result{Decision: "halted"},
						Journal: journal.Stats{MaxRecords: 100, MaxBytes: 1 << 20},
						Control: status,
					},
				)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		statusWritten <- errors.New("drain stop state was not observed")
	}()
	err = runContext(t.Context(), []string{
		"swap", "drain", "--config", configPath,
		"--timeout", "4m", "--reason", "operator test",
	}, &bytes.Buffer{})
	if statusErr := <-statusWritten; statusErr != nil {
		t.Fatal(statusErr)
	}
	if err == nil || !strings.Contains(err.Error(), "new actions are stopped") ||
		!strings.Contains(err.Error(), "operator attention") {
		t.Fatalf("halted drain error = %v", err)
	}
	if blocked, statusErr := state.NoNewActions(); statusErr != nil || !blocked {
		t.Fatalf("halted drain control state = %v, %v", blocked, statusErr)
	}
}

func testSwapProfile(owner string) swaprun.Profile {
	inputTokenAccount, err := orcaswap.AssociatedTokenAddress(owner, orcaswap.WrappedSOLMint)
	if err != nil {
		panic(err)
	}
	outputTokenAccount, err := orcaswap.AssociatedTokenAddress(owner, orcaswap.DevnetUSDCMint)
	if err != nil {
		panic(err)
	}
	return swaprun.Profile{
		Name: orcaswap.ProfileName, Version: orcaswap.ProfileVersion, Cluster: "devnet",
		Route: orcaswap.Policy{
			Owner: owner, Pool: "3KBZiL2g8C7tiJ32hTv5v3KM7aK9htpqTw4cTXz1HvPt",
			InputMint:          orcaswap.WrappedSOLMint,
			OutputMint:         "BRjpCHtyQLNCo8gqRUr8jtdAj5AjPYQaoqbvcZiHok1k",
			InputTokenAccount:  inputTokenAccount,
			OutputTokenAccount: outputTokenAccount,
			TokenVaultA:        "C9zLV5zWF66j3rZj3uuhDqvfuA8esJyWnruGzDW9qEj2",
			TokenVaultB:        "7DM3RMz2yzUB8yPRQM3FMZgdFrwZGMsabsfsKopWktoX",
			Oracle:             "2KEWNc3b6EfqoWQpfKQMHh4mhRyKXYRdPbtGRTJX3Cip",
			ProgramData:        orcaswap.WhirlpoolProgramData,
			UpgradeAuthority:   orcaswap.WhirlpoolUpgradeAuth,
			DeploymentSlot:     orcaswap.WhirlpoolDeploySlot,
			MaxInputLamports:   1_000_000, MinOutputAmount: 1, MaxSlippageBPS: 100,
			MaxOutputAccountRentLamports: orcaswap.DefaultMaxOutputAccountRentLamports,
		},
		InputLamports: 1_000_000, SlippageBPS: 100,
		ReserveLamports: 50_000_000, MaxFeeLamports: 100_000,
		// Six trades' worth (6 x 4_100_000), matching what setup now writes. It
		// used to be exactly one trade, which stopped being representative once
		// the daily caps started funding several — and left the fixture unable
		// to exercise any test that arms more than one action.
		DailyDebitCapLamports:     24_600_000,
		ScheduleWindowSeconds:     3_600,
		ScheduleAnchorUnix:        time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		MaxClockUncertaintyMillis: 100, MaxObservationAgeSeconds: 30,
		MinHealthyObservationSeconds: 5, MinHealthySlotAdvance: 1,
		MaxBlockHeightWindow: 200, MaxReconciliationSeconds: 180,
	}
}

// A public tool that answers a typo with nothing but "unknown" sends somebody
// to read the whole help page hunting for a letter they missed.
func TestATypoSuggestsTheRealCommand(t *testing.T) {
	for typo, want := range map[string]string{
		"demoo":  "demo",
		"doctr":  "doctor",
		"setpu":  "setup",
		"shadwo": "shadow",
		"waller": "wallet",
	} {
		err := runContext(t.Context(), []string{typo}, &bytes.Buffer{})
		if err == nil {
			t.Fatalf("%q was accepted as a command", typo)
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q was not corrected to %q: %v", typo, want, err)
		}
		if !strings.Contains(err.Error(), "mithril-agent help") {
			t.Errorf("%q does not point at help: %v", typo, err)
		}
	}
	// Something unrelated must not be answered with a confusing guess.
	err := runContext(t.Context(), []string{"xyzzy"}, &bytes.Buffer{})
	if err == nil || strings.Contains(err.Error(), "did you mean") {
		t.Errorf("an unrelated word got a guess: %v", err)
	}
}

// Every command must fail in a way that names what to do, never a bare refusal.
func TestEveryCommandFailsWithAnActionableMessage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{
		{"doctor"}, {"preflight"}, {"demo"}, {"check"},
		{"swap", "run"}, {"swap", "status"}, {"swap", "plan"},
		{"funding", "check"}, {"shadow", "run"}, {"shadow", "report"},
		{"shadow", "policy"}, {"journal", "verify"},
		{"wallet", "check"}, {"wallet", "new"}, {"clock-check"},
	} {
		name := strings.Join(args, " ")
		var out bytes.Buffer
		err := runContext(t.Context(), args, &out)
		if err == nil {
			continue // some commands legitimately succeed with no arguments
		}
		message := err.Error()
		// It must name a flag to supply or a command to run — not just refuse.
		if !strings.Contains(message, "--") && !strings.Contains(message, "mithril-agent") {
			t.Errorf("%q fails without naming a fix: %q", name, message)
		}
		// And it must not name a different command than the one that was run.
		if strings.HasPrefix(name, "shadow report") && strings.Contains(message, "shadow run") {
			t.Errorf("%q reports the wrong command: %q", name, message)
		}
	}
}

// A hardcoded version makes the documented upgrade and rollback procedure
// unverifiable: every build on the host claims to be the same one.
func TestVersionIsNotAHardcodedLiteral(t *testing.T) {
	reported := agentVersion()
	if reported == "" {
		t.Fatal("version is empty")
	}
	if strings.Contains(reported, "0.1.0-dev") {
		t.Errorf("version is still the hardcoded placeholder: %q", reported)
	}
	// With nothing stamped and no VCS data it must say so, not invent a number.
	if version == "" && !strings.Contains(reported, "unknown") &&
		len(reported) < 7 {
		t.Errorf("an unstamped build reported an implausible version: %q", reported)
	}
}
