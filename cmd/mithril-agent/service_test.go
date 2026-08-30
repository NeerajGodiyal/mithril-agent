package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/signer"
)

// A unit that names one deployment's paths silently supervises the wrong thing
// on the next one. Everything must come from what setup recorded.
func TestServiceUnitIsDerivedFromTheRecordedStrategy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	sell := triggeredLeg(t, dir, false, 0)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	plan, err := buildServicePlan(defaultMetricsBasePort)
	if err != nil {
		t.Fatal(err)
	}
	unit := renderServiceUnit(plan)

	// Without HOME the runner finds no strategy pointer, so it starts, finds no
	// legs and does nothing — the exact silent-idle failure this prevents.
	if !strings.Contains(unit, "Environment=HOME="+home) {
		t.Errorf("the unit does not pin HOME, so the runner would find no legs:\n%s", unit)
	}
	if !strings.Contains(unit, "strategy run") {
		t.Errorf("a recorded strategy did not produce a strategy runner:\n%s", unit)
	}
	// The runner must never be able to rewrite the profile whose caps bound it.
	cfg, err := readConfig(sell)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Dir(cfg.Control.StatePath)
	if !strings.Contains(unit, "ReadWritePaths=") || !strings.Contains(unit, stateDir) {
		t.Errorf("the leg's state directory is not writable, so the runner cannot record:\n%s", unit)
	}
	if !strings.Contains(unit, "ProtectSystem=strict") ||
		!strings.Contains(unit, "NoNewPrivileges=yes") {
		t.Errorf("the unit dropped the confinement the hand-written units had:\n%s", unit)
	}
}

func TestStandardServiceUsesTheNarrowNodeStateGroup(t *testing.T) {
	unit := renderServiceUnit(servicePlan{User: "mithril-agent", Group: "mithril-agent"})
	if !strings.Contains(unit, "SupplementaryGroups="+nodeStateGroupName+"\n") {
		t.Fatalf("standard service has no node-state reader group:\n%s", unit)
	}
	other := renderServiceUnit(servicePlan{User: "operator", Group: "operator"})
	if strings.Contains(other, "SupplementaryGroups="+nodeStateGroupName+"\n") {
		t.Fatalf("custom service was given a group it may not have:\n%s", other)
	}
}

// A path cannot be both read-only and writable: systemd applies ReadOnlyPaths
// and the runner then fails to record what it did. Legs whose config and state
// share a directory are the ordinary case, so this is not a corner.
func TestServiceUnitNeverMarksAWritablePathReadOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	sell := triggeredLeg(t, dir, false, 0)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	plan, err := buildServicePlan(defaultMetricsBasePort)
	if err != nil {
		t.Fatal(err)
	}
	writable := make(map[string]bool, len(plan.ReadWrite))
	for _, path := range plan.ReadWrite {
		writable[path] = true
	}
	for _, path := range plan.ReadOnly {
		if writable[path] {
			t.Errorf("%s is listed both read-only and writable", path)
		}
	}
}

// Installing a supervisor must never arm a wallet. Keeping the runner alive and
// granting it authority to spend are separate decisions, and a command that
// quietly did both would hide the only one that matters.
func TestServiceInstallArmsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	sell := triggeredLeg(t, dir, false, 0)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runService([]string{"install"}, &out); err != nil {
		t.Fatal(err)
	}
	// The control state must still say stopped after generating a unit.
	check := doctorStrategyCheck(time.Now)
	if strings.Contains(check.Detail, "armed") && !strings.Contains(check.Detail, "none armed") {
		t.Fatalf("generating a unit changed the armed state: %+v", check)
	}
	unit := out.String()
	for _, forbidden := range []string{"strategy enable", "swap enable", "--max-trades"} {
		if strings.Contains(unit, forbidden) {
			t.Errorf("the generated unit contains an arming command (%q):\n%s", forbidden, unit)
		}
	}
	stop := " strategy stop --submitter-socket-prefix " + submitterSocketPrefix + " --reason "
	if !strings.Contains(unit, "ExecStartPre="+planBinaryForTest(t)+stop+"service_start") ||
		!strings.Contains(unit, "ExecStopPost="+planBinaryForTest(t)+stop+"service_stop") {
		t.Errorf("the service can restart without revoking old authority:\n%s", unit)
	}
}

func TestStrategyServiceUsesTheProfilesSafeArmingCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	cfg, err := readConfig(sell)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Swap.PriceTrigger = nil
	cfg.Swap.DailyDebitCapLamports = cfg.Swap.InputLamports +
		cfg.Swap.MaxFeeLamports + cfg.Swap.Route.MaxOutputAccountRentLamports
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sell, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	plan, err := buildServicePlan(defaultMetricsBasePort)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--max-trades 1", "--allow-any-price"} {
		if !strings.Contains(plan.ArmCommand, want) {
			t.Errorf("arming command %q does not contain %q", plan.ArmCommand, want)
		}
	}
}

func planBinaryForTest(t *testing.T) string {
	t.Helper()
	plan, err := buildServicePlan(defaultMetricsBasePort)
	if err != nil {
		t.Fatal(err)
	}
	return plan.Binary
}

// With nothing set up, the command must say so rather than emit a unit that
// supervises a runner with no configuration to run.
func TestServiceInstallRefusesWithNothingSetUp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	err := runService([]string{"install"}, &out)
	if err == nil {
		t.Fatalf("emitted a unit with nothing set up:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "setup") {
		t.Errorf("the refusal does not name the command that fixes it: %v", err)
	}
}

func TestSingleLegServicePointsAtTheOneActionDemo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	single := triggeredLeg(t, t.TempDir(), false, 0)
	if err := recordCurrentConfig(single); err != nil {
		t.Fatal(err)
	}
	plan, err := buildServicePlan(defaultMetricsBasePort)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plan.ArmCommand, "demo --config ") {
		t.Fatalf("single-leg next command = %q, want one-action demo", plan.ArmCommand)
	}
	if strings.Contains(plan.ArmCommand, "swap enable") || strings.Contains(plan.ArmCommand, "max-actions") {
		t.Fatalf("single-leg service advised direct multi-action authority: %q", plan.ArmCommand)
	}
	if !strings.Contains(plan.RunArgs, "--metrics-address 127.0.0.1:9310") {
		t.Fatalf("single-leg runner ignored its reserved metrics port: %q", plan.RunArgs)
	}
}

// --output writes the file; without it the operator reads the unit first. A
// command that installs system configuration without showing it is one nobody
// can review before restarting something that spends money.
func TestServiceInstallPrintsBeforeItWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	sell := triggeredLeg(t, dir, false, 0)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	var printed bytes.Buffer
	if err := runService([]string{"install"}, &printed); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(printed.String(), "[Unit]") {
		t.Errorf("the default did not print the unit for review:\n%s", printed.String())
	}

	target := filepath.Join(t.TempDir(), serviceUnitName)
	var wrote bytes.Buffer
	if err := runService([]string{"install", "--output", target}, &wrote); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != printed.String() {
		t.Error("the written unit differs from the one shown for review")
	}
	// Telling the operator the unit exists is useless without the two commands
	// that make it take effect.
	if !strings.Contains(wrote.String(), "daemon-reload") ||
		!strings.Contains(wrote.String(), "sudo systemctl enable "+serviceUnitName) ||
		!strings.Contains(wrote.String(), "sudo systemctl restart "+serviceUnitName) {
		t.Errorf("did not say how to make the unit take effect:\n%s", wrote.String())
	}
	if !strings.Contains(wrote.String(), "sudo install") ||
		!strings.Contains(wrote.String(), serviceUnitName+" ") ||
		!strings.Contains(wrote.String(), "/etc/systemd/system/") {
		t.Errorf("a staged unit was not given an install command:\n%s", wrote.String())
	}
	if !strings.Contains(wrote.String(), "sudo systemd-analyze verify "+
		"/etc/systemd/system/"+serviceUnitName) {
		t.Errorf("the generated units were not given a syntax check:\n%s", wrote.String())
	}
}

// The same deployment must always render the same unit, or nobody can diff it
// against the one currently installed.
func TestServiceUnitIsStableAcrossRuns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	sell := triggeredLeg(t, dir, false, 0)
	buy := triggeredLeg(t, t.TempDir(), true, 0)
	if err := recordStrategy(strategyPaths{sell: sell, buy: buy}); err != nil {
		t.Fatal(err)
	}
	first, err := buildServicePlan(defaultMetricsBasePort)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildServicePlan(defaultMetricsBasePort)
	if err != nil {
		t.Fatal(err)
	}
	if renderServiceUnit(first) != renderServiceUnit(second) {
		t.Error("two runs on the same deployment rendered different units")
	}
}

// The runner reads its RPC endpoints from the environment. A unit that omitted
// them would produce a runner that starts cleanly and can reach nothing — a new
// silent failure, and a worse one than the terminal it replaces.
func TestServiceUnitCarriesTheRunnerEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runService([]string{"install"}, &out); err != nil {
		t.Fatal(err)
	}
	// rpc.env carries MITHRIL_AGENT_MITHRIL_RPC_URL and must NOT be optional:
	// a missing one has to fail at start, not at the first trade.
	if !strings.Contains(out.String(), "EnvironmentFile=/etc/mithril-agent/rpc.env") {
		t.Errorf("the unit does not require the RPC environment:\n%s", out.String())
	}
	if strings.Contains(out.String(), "EnvironmentFile=-/etc/mithril-agent/rpc.env") {
		t.Error("the RPC environment was made optional, so a missing one fails at trade time")
	}

	// A deployment keeping its environment elsewhere must be able to say so.
	var custom bytes.Buffer
	if err := runService([]string{"install", "--env-file", "/opt/agent/env"}, &custom); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(custom.String(), "EnvironmentFile=/opt/agent/env") {
		t.Errorf("--env-file was ignored:\n%s", custom.String())
	}
	if strings.Contains(custom.String(), "/etc/mithril-agent/rpc.env") {
		t.Error("--env-file did not replace the defaults, so two environments would load")
	}
}

// systemd silently ignores keys placed in the wrong section: it logs "Unknown
// key name" and loads the unit anyway. So a unit can claim a property it does
// not have, which is precisely what happened to a hand-written unit that put
// StartLimitIntervalSec under [Service] with a comment describing the
// behaviour it therefore never had.
//
// Every key must be in the section systemd reads it from, and the one that
// stops systemd abandoning a runner holding spending authority most of all.
func TestServiceUnitPutsEveryKeyInTheSectionSystemdReadsIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	plan, err := buildServicePlan(defaultMetricsBasePort)
	if err != nil {
		t.Fatal(err)
	}

	section := map[string]string{}
	current := ""
	for _, line := range strings.Split(renderServiceUnit(plan), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			current = line
		case line == "" || strings.HasPrefix(line, "#"):
		default:
			key, _, found := strings.Cut(line, "=")
			if !found {
				t.Fatalf("unit line is not key=value: %q", line)
			}
			section[key] = current
		}
	}

	// Where systemd actually reads each key we emit.
	for key, want := range map[string]string{
		"StartLimitIntervalSec": "[Unit]",
		"Description":           "[Unit]",
		"After":                 "[Unit]",
		"Wants":                 "[Unit]",
		"ExecStart":             "[Service]",
		"ExecStartPre":          "[Service]",
		"ExecStopPost":          "[Service]",
		"Restart":               "[Service]",
		"RestartSec":            "[Service]",
		"TimeoutStopSec":        "[Service]",
		"Environment":           "[Service]",
		"EnvironmentFile":       "[Service]",
		"ProtectSystem":         "[Service]",
		"ReadWritePaths":        "[Service]",
		"WantedBy":              "[Install]",
	} {
		got, present := section[key]
		if !present {
			t.Errorf("the unit no longer emits %s", key)
			continue
		}
		if got != want {
			t.Errorf("%s is in %s; systemd reads it from %s and ignores it here", key, got, want)
		}
	}
}

// A systemd system unit with no User= runs as ROOT. The working deployment on a
// real host runs as an unprivileged account, so a command that silently dropped
// the identity would take a correctly-confined agent and escalate it.
func TestServiceUnitNeverRunsAsRootByOmission(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	plan, err := buildServicePlan(defaultMetricsBasePort)
	if err != nil {
		t.Fatal(err)
	}
	if plan.User == "" {
		t.Fatal("no user was derived, so the unit would run as root")
	}
	unit := renderServiceUnit(plan)
	if !strings.Contains(unit, "\nUser="+plan.User+"\n") {
		t.Errorf("the unit does not pin a user:\n%s", unit)
	}
	// It must be the account that owns the state, not whatever was convenient.
	owner, _, err := fileOwner(sell)
	if err != nil {
		t.Fatal(err)
	}
	if plan.User != owner {
		t.Errorf("unit runs as %q but the deployment is owned by %q", plan.User, owner)
	}
}

// Failing to read an owner is a reason to stop, not a reason to fall back to
// the most privileged identity available.
func TestServicePlanRefusesRatherThanDefaultingToRoot(t *testing.T) {
	if _, err := (servicePlan{Binary: "/bin/agent", Home: "/home/x"}).checked(); err == nil {
		t.Fatal("a plan with no user was accepted; the unit would have run as root")
	} else if !strings.Contains(err.Error(), "root") {
		t.Errorf("the refusal does not say why it matters: %v", err)
	}
}

func TestServicePlanRejectsUnitSyntaxInAConfigPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "leg\nUser=root")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sell := triggeredLeg(t, dir, false, 0)
	var plan servicePlan
	if err := plan.addLeg("sell", sell); err == nil {
		t.Fatal("a config path that can add a systemd directive was accepted")
	} else if !strings.Contains(err.Error(), "unsafe in a systemd unit") {
		t.Fatalf("error = %v, want a systemd path refusal", err)
	}
}

func TestServiceInstallRejectsSyntaxInPrintedPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"install", "--env-file", "/tmp/rpc.env\nUser=root"},
		{"install", "--output", filepath.Join(t.TempDir(), "unit\necho unsafe")},
	} {
		if err := runService(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("unsafe path was accepted: %q", args)
		}
	}
}

// A supervised unit restarts forever by design, so a port collision is not a
// visible failure — it is a permanent crash loop. The generated unit must never
// inherit the default a single-leg `swap run` already binds, which is exactly
// how the first real install of this command failed on a live host.
func TestServiceUnitDoesNotInheritTheCollidingMetricsPort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runService([]string{"install"}, &out); err != nil {
		t.Fatal(err)
	}
	unit := out.String()
	if !strings.Contains(unit, "--metrics-base-port") {
		t.Fatalf("the unit does not pin a metrics port, so it inherits the colliding default:\n%s", unit)
	}
	// 9191 is the single-leg runner's default and the port the collision hit.
	if strings.Contains(unit, "--metrics-base-port 9191") {
		t.Error("the unit pins the very port that collides with a single-leg runner")
	}

	// And an operator with their own layout must be able to choose.
	var custom bytes.Buffer
	if err := runService([]string{"install", "--metrics-base-port", "9500"}, &custom); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(custom.String(), "--metrics-base-port 9500") {
		t.Errorf("--metrics-base-port was ignored:\n%s", custom.String())
	}
}

func TestServiceUnitUsesStableStatePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	strategyRoot := t.TempDir()
	sellRoot := filepath.Join(strategyRoot, "sell")
	if err := os.MkdirAll(filepath.Join(sellRoot, stableStateDirName, controlStateDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	sell := triggeredLeg(t, sellRoot, false, 0)
	cfg, err := readConfig(sell)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Control.StatePath = filepath.Join(sellRoot, stableStateDirName, controlStateDirName, "control.json")
	cfg.Journal.Path = filepath.Join(sellRoot, stableStateDirName, "events.jsonl")
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sell, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	plan, err := buildServicePlan(defaultMetricsBasePort)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ReadWrite) == 0 {
		t.Fatal("the unit grants no writable paths, so the runner cannot journal")
	}
	want := filepath.Join(filepath.Dir(sell), stableStateDirName)
	futureBuy := "-" + filepath.Join(strategyRoot, "buy", stableStateDirName)
	foundState, foundBuy := false, false
	for _, path := range plan.ReadWrite {
		if path == want {
			foundState = true
		}
		if path == futureBuy {
			foundBuy = true
		}
	}
	if !foundState {
		t.Errorf("the current leg's state dir %q is not writable: %v", want, plan.ReadWrite)
	}
	if !foundBuy {
		t.Errorf("the future buy state dir %q is not reserved: %v", futureBuy, plan.ReadWrite)
	}
}

func TestServiceInstallMovesALegacyQuoteAdapterBehindTheSidecar(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	cfg, err := readConfig(sell)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Quote.SocketPath = ""
	cfg.Quote.Command = "/usr/bin/node"
	cfg.Quote.ScriptPath = "/usr/local/libexec/mithril-agent/quote.mjs"
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sell, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runService([]string{"install"}, &out); err != nil {
		t.Fatal(err)
	}
	unit := out.String()
	if !strings.Contains(unit, "--quote-socket "+supervisedQuoteSocket) {
		t.Fatal("the generated runner did not move the legacy adapter behind the quote sidecar")
	}
	if !strings.Contains(unit, "MemoryDenyWriteExecute=yes") ||
		strings.Contains(unit, "MemoryDenyWriteExecute=no") {
		t.Fatal("the migrated runner does not keep write-execute protection")
	}
	applyQuoteSocketOverride(&cfg, supervisedQuoteSocket)
	if cfg.Quote.SocketPath != supervisedQuoteSocket || cfg.Quote.Command != "" || cfg.Quote.ScriptPath != "" {
		t.Fatalf("runtime quote override did not replace the direct adapter: %+v", cfg.Quote)
	}
}

func TestServiceUnitKeepsWriteExecuteProtectionWithQuoteSocket(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	cfg, err := readConfig(sell)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Quote.Command, cfg.Quote.ScriptPath = "", ""
	cfg.Quote.SocketPath = "/run/mithril-agent-quote/quote.sock"
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sell, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	plan, err := buildServicePlan(defaultMetricsBasePort)
	if err != nil {
		t.Fatal(err)
	}
	if unit := renderServiceUnit(plan); !strings.Contains(unit, "MemoryDenyWriteExecute=yes") {
		t.Fatal("the sidecar-backed runner lost write-execute protection")
	}
}

func TestSweepLegDoesNotDisableWriteExecuteProtection(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, stableStateDirName)
	if err := os.MkdirAll(filepath.Join(stateDir, signerStateDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, controlStateDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	var cfg config
	cfg.Profile = testSweepProfileForStrategy("source", "destination", time.Now().Unix())
	cfg.Control.StatePath = filepath.Join(stateDir, controlStateDirName, "control.json")
	cfg.Journal.Path = filepath.Join(stateDir, "events.jsonl")
	cfg.Signer.PolicyPath = filepath.Join(dir, "signer-policy.json")
	cfg.Signer.KeypairPath = filepath.Join(dir, "wallet-keypair.json")
	cfg.Submitter.PolicyPath = filepath.Join(dir, "submitter-policy.json")
	cfg.Submitter.PrivateKeyPath = filepath.Join(dir, "submitter-key.json")
	writeJSON(t, cfg.Signer.PolicyPath, signer.Policy{
		AuthorizationLedgerPath: filepath.Join(stateDir, signerStateDirName, "authorizations.jsonl"),
	})
	if err := os.WriteFile(cfg.Signer.KeypairPath, []byte("[0]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Submitter.PolicyPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Submitter.PrivateKeyPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	plan := servicePlan{}
	if err := plan.addLeg("sweep", path); err != nil {
		t.Fatal(err)
	}
	if unit := renderServiceUnit(plan); !strings.Contains(unit, "MemoryDenyWriteExecute=yes") {
		t.Fatal("the sweep runner lost write-execute protection")
	}
}

func TestServiceInstallIsolatesAuthoritiesBehindSocketActivation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	privateRoot := filepath.Join(t.TempDir(), "strategy-data", "sell")
	sell := triggeredLeg(t, privateRoot, false, 0)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	var output bytes.Buffer
	if err := runService([]string{"install", "--output", filepath.Join(directory, serviceUnitName)},
		&output); err != nil {
		t.Fatal(err)
	}
	plan, err := buildServicePlan(defaultMetricsBasePort)
	if err != nil {
		t.Fatal(err)
	}
	leg := plan.Legs[0]
	runner, err := os.ReadFile(filepath.Join(directory, serviceUnitName))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--signer-socket-prefix " + signerSocketPrefix,
		"--risk-socket-prefix " + riskSocketPrefix,
		"--submitter-socket-prefix " + submitterSocketPrefix,
		"Requires=" + leg.signerUnit() + ".socket",
		leg.riskUnit() + ".socket", leg.submitterUnit() + ".socket",
		"InaccessiblePaths=", leg.SignerKeypair, leg.RiskKeypair, leg.SignerStateDir,
		leg.SubmitterPolicy, leg.SubmitterKey, leg.ControlStateDir,
	} {
		if !strings.Contains(string(runner), want) {
			t.Errorf("runner is missing %q:\n%s", want, runner)
		}
	}
	socket, err := os.ReadFile(filepath.Join(directory, leg.signerUnit()+".socket"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ListenStream=" + leg.signerSocket(), "SocketUser=" + plan.User,
		"SocketGroup=" + plan.Group, "SocketMode=0660", "Accept=yes",
		"MaxConnections=8", "MaxConnectionsPerSource=2",
	} {
		if !strings.Contains(string(socket), want) {
			t.Errorf("signer socket is missing %q:\n%s", want, socket)
		}
	}
	if strings.Contains(string(socket), "Service=") || strings.Contains(string(socket), "FlushPending=") {
		t.Errorf("Accept=yes socket contains an Accept=no-only directive:\n%s", socket)
	}
	service, err := os.ReadFile(filepath.Join(directory, leg.signerUnit()+"@.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"User=" + signerAccountName, "LoadCredential=signer-policy:" + leg.SignerPolicy,
		"LoadCredential=signer-key:" + leg.SignerKeypair,
		"--policy %d/signer-policy --keypair %d/signer-key --socket",
		"StandardInput=socket", "StandardOutput=socket", "PrivateNetwork=yes",
		"ReadWritePaths=" + leg.SignerStateDir, "MemoryDenyWriteExecute=yes",
		"CollectMode=inactive-or-failed",
	} {
		if !strings.Contains(string(service), want) {
			t.Errorf("signer service is missing %q:\n%s", want, service)
		}
	}
	for _, want := range []string{
		"useradd --system --no-create-home --user-group " + signerAccountName,
		"chmod g+x ", filepath.Dir(leg.SignerStateDir), filepath.Dir(privateRoot),
		"chown -R " + signerAccountName + ":" + signerAccountName + " " + leg.SignerStateDir,
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("install instructions are missing %q:\n%s", want, output.String())
		}
	}
	riskSocket, err := os.ReadFile(filepath.Join(directory, leg.riskUnit()+".socket"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ListenStream=" + leg.riskSocket(), "SocketUser=" + plan.User,
		"SocketGroup=" + plan.Group, "SocketMode=0660", "Accept=yes",
		"MaxConnections=8", "MaxConnectionsPerSource=2",
	} {
		if !strings.Contains(string(riskSocket), want) {
			t.Errorf("risk authority socket is missing %q:\n%s", want, riskSocket)
		}
	}
	riskService, err := os.ReadFile(filepath.Join(directory, leg.riskUnit()+"@.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"User=" + riskAccountName, "LoadCredential=risk-policy:" + leg.RiskPolicy,
		"LoadCredential=risk-key:" + leg.RiskKeypair,
		"--policy %d/risk-policy --keypair %d/risk-key --socket",
		"StandardInput=socket", "StandardOutput=socket", "PrivateNetwork=yes",
		"MemoryDenyWriteExecute=yes", "CollectMode=inactive-or-failed",
	} {
		if !strings.Contains(string(riskService), want) {
			t.Errorf("risk authority service is missing %q:\n%s", want, riskService)
		}
	}
	for _, want := range []string{
		"useradd --system --no-create-home --user-group " + riskAccountName,
		"chown " + riskAccountName + ":" + riskAccountName + " " + leg.RiskKeypair,
		"chmod 0600 " + leg.RiskKeypair,
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("install instructions are missing %q:\n%s", want, output.String())
		}
	}
	submitterSocket, err := os.ReadFile(filepath.Join(directory, leg.submitterUnit()+".socket"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ListenStream=" + leg.submitterSocket(), "SocketUser=" + plan.User,
		"SocketGroup=" + plan.Group, "SocketMode=0660", "Accept=yes",
		"MaxConnections=8", "MaxConnectionsPerSource=2",
	} {
		if !strings.Contains(string(submitterSocket), want) {
			t.Errorf("submitter socket is missing %q:\n%s", want, submitterSocket)
		}
	}
	submitterService, err := os.ReadFile(filepath.Join(directory, leg.submitterUnit()+"@.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"User=" + submitterAccountName,
		"LoadCredential=submitter-policy:" + leg.SubmitterPolicy,
		"LoadCredential=submitter-key:" + leg.SubmitterKey,
		"--policy %d/submitter-policy --key %d/submitter-key --socket",
		"StandardInput=socket", "StandardOutput=socket",
		"ReadWritePaths=" + leg.ControlStateDir,
		"IPAddressDeny=any", "IPAddressAllow=127.0.0.0/8",
		"MemoryDenyWriteExecute=yes", "CollectMode=inactive-or-failed",
	} {
		if !strings.Contains(string(submitterService), want) {
			t.Errorf("submitter service is missing %q:\n%s", want, submitterService)
		}
	}
	for _, want := range []string{
		"useradd --system --no-create-home --user-group " + submitterAccountName,
		"chown " + submitterAccountName + ":" + submitterAccountName,
		leg.SubmitterPolicy, leg.SubmitterKey,
		"chown -R " + submitterAccountName + ":" + submitterAccountName + " " + leg.ControlStateDir,
		"chmod 0700 " + leg.ControlStateDir,
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("install instructions are missing %q:\n%s", want, output.String())
		}
	}
	operatorSocket, err := os.ReadFile(filepath.Join(directory, leg.operatorUnit()+".socket"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ListenStream=" + leg.operatorSocket(), "SocketUser=root", "SocketGroup=root",
		"SocketMode=0600", "Accept=yes", "MaxConnections=4", "MaxConnectionsPerSource=1",
	} {
		if !strings.Contains(string(operatorSocket), want) {
			t.Errorf("operator socket is missing %q:\n%s", want, operatorSocket)
		}
	}
	operatorService, err := os.ReadFile(filepath.Join(directory, leg.operatorUnit()+"@.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"User=" + submitterAccountName,
		"LoadCredential=submitter-policy:" + leg.SubmitterPolicy,
		"--policy %d/submitter-policy --operator-socket",
		"PrivateNetwork=yes", "RestrictAddressFamilies=AF_UNIX",
		"ReadWritePaths=" + leg.ControlStateDir,
		"InaccessiblePaths=/proc " + leg.SubmitterKey,
	} {
		if !strings.Contains(string(operatorService), want) {
			t.Errorf("operator service is missing %q:\n%s", want, operatorService)
		}
	}
	for _, forbidden := range []string{"LoadCredential=submitter-key:", "EnvironmentFile=", "--key "} {
		if strings.Contains(string(operatorService), forbidden) {
			t.Errorf("operator service contains %q:\n%s", forbidden, operatorService)
		}
	}
	if strings.Contains(string(runner), leg.operatorUnit()+".socket") ||
		strings.Contains(string(runner), leg.operatorSocket()) {
		t.Errorf("runner was granted the root-only operator socket:\n%s", runner)
	}
	recoveryService, err := os.ReadFile(filepath.Join(directory, leg.recoveryUnit()+".service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"User=" + submitterAccountName,
		"LoadCredential=submitter-policy:" + leg.SubmitterPolicy,
		"--policy %d/submitter-policy --recover",
		"ReadWritePaths=" + leg.ControlStateDir,
		"InaccessiblePaths=/proc " + leg.SubmitterKey,
		"EnvironmentFile=/etc/mithril-agent/rpc.env",
		"IPAddressDeny=127.0.0.0/8", "IPAddressDeny=169.254.0.0/16",
		"MemoryDenyWriteExecute=yes", "ProtectClock=yes",
	} {
		if !strings.Contains(string(recoveryService), want) {
			t.Errorf("recovery service is missing %q:\n%s", want, recoveryService)
		}
	}
	for _, forbidden := range []string{
		"submitter-key:", "--key ", "MITHRIL_AGENT_MITHRIL_RPC_URL=",
		"ListenStream=", "StandardInput=socket",
	} {
		if strings.Contains(string(recoveryService), forbidden) {
			t.Errorf("recovery service contains %q:\n%s", forbidden, recoveryService)
		}
	}
	recoveryTimer, err := os.ReadFile(filepath.Join(directory, leg.recoveryUnit()+".timer"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"OnBootSec=10s", "OnUnitInactiveSec=10s",
		"Unit=" + leg.recoveryUnit() + ".service", "WantedBy=timers.target",
	} {
		if !strings.Contains(string(recoveryTimer), want) {
			t.Errorf("recovery timer is missing %q:\n%s", want, recoveryTimer)
		}
	}
	if strings.Contains(string(runner), leg.recoveryUnit()) ||
		strings.Contains(string(runner), "--recover") {
		t.Errorf("runner controls the independent recovery service:\n%s", runner)
	}
	if !strings.Contains(output.String(), "systemctl enable "+leg.recoveryUnit()+".timer") ||
		!strings.Contains(output.String(), "systemctl restart "+leg.recoveryUnit()+".timer") {
		t.Errorf("install instructions omit the recovery timer:\n%s", output.String())
	}
	runtimeSockets := append(signerSocketUnits(plan), riskSocketUnits(plan)...)
	runtimeSockets = append(runtimeSockets, submitterSocketUnits(plan)...)
	for _, instruction := range []string{
		"systemctl stop " + serviceUnitName,
		"systemctl restart " + strings.Join(runtimeSockets, " "),
		"systemctl restart " + serviceUnitName,
	} {
		if !strings.Contains(output.String(), instruction) {
			t.Errorf("install instructions omit the safe service reload step %q:\n%s",
				instruction, output.String())
		}
	}
}

func TestTelegramCanBeDisabledFromTheStrategyFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := armedLegWithStatus(t, t.TempDir(), false, time.Second)
	if err := recordStrategy(strategyPaths{sell: sell, telegram: "disabled"}); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	var output bytes.Buffer
	if err := runService([]string{"install", "--output", filepath.Join(directory, serviceUnitName)},
		&output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, alertsUnitName)); !os.IsNotExist(err) {
		t.Fatalf("Telegram was disabled but its unit was generated: %v", err)
	}
	for _, name := range []string{
		"mithril-agent-status-sell.socket",
		"mithril-agent-status-sell.service",
	} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("Telegram was disabled and removed read-only status unit %s: %v", name, err)
		}
	}
	text := output.String()
	if !strings.Contains(text, "systemctl enable mithril-agent-status-sell.socket") {
		t.Fatalf("Telegram was disabled and status socket activation was omitted:\n%s", text)
	}
	for _, forbidden := range []string{alertsAccountName, alertsUnitName, "mithril-agent-telegram test"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("Telegram was disabled but install instructions contain %q:\n%s", forbidden, text)
		}
	}
}

func TestServiceUnitLeavesKernelBootIdentityVisible(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := triggeredLeg(t, t.TempDir(), false, 0)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	plan, err := buildServicePlan(defaultMetricsBasePort)
	if err != nil {
		t.Fatal(err)
	}
	unit := renderServiceUnit(plan)
	if strings.Contains(unit, "ProcSubset=pid") {
		t.Fatal("ProcSubset=pid hides /proc/sys/kernel/random/boot_id from the clock gate")
	}
	if !strings.Contains(unit, "ProtectProc=invisible") {
		t.Fatal("process visibility protection was removed with ProcSubset")
	}
}

// A runner installed on its own executes trades and tells nobody: the path from
// a leg's status file to the bot is four more units, and they used to be
// hand-written per deployment. An operator who goes away expecting messages and
// gets none reads that as "nothing happened", which is worse than an error.
func TestServiceInstallAlsoWiresTheAlertPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := armedLegWithStatus(t, t.TempDir(), false, time.Second)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	var printed bytes.Buffer
	if err := runService([]string{"install", "--output", filepath.Join(directory, serviceUnitName)},
		&printed); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"mithril-agent-status-sell.socket",
		"mithril-agent-status-sell.service",
		alertsUnitName,
	} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("%s was not generated: %v", name, err)
		}
		if !strings.Contains(printed.String(), name) {
			t.Errorf("%s was written but never named to the operator:\n%s", name, printed.String())
		}
	}
	for _, instruction := range []string{
		"less " + filepath.Join(directory, serviceUnitName),
		"sudo systemctl enable mithril-agent-status-sell.socket",
		"sudo systemctl restart mithril-agent-status-sell.socket",
		"sudo systemctl enable " + alertsUnitName,
		"sudo systemctl restart " + alertsUnitName,
		"--uid=" + alertsAccountName + " --gid=" + alertsAccountName,
		"EnvironmentFile=" + alertsEnvFile,
		filepath.Join(filepath.Dir(planBinaryForTest(t)), "mithril-agent-telegram") + " test",
	} {
		if !strings.Contains(printed.String(), instruction) {
			t.Errorf("the generated install instructions are missing %q:\n%s", instruction, printed.String())
		}
	}

	// The bot must be pointed at THIS deployment's socket. A unit naming another
	// deployment's path connects, stays quiet, and looks healthy.
	bot, err := os.ReadFile(filepath.Join(directory, alertsUnitName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bot), "/run/mithril-agent-status-sell.sock") {
		t.Errorf("the bot unit does not name this leg's socket:\n%s", bot)
	}
	// The bot gets status and nothing else: no config, no key, no journal, and
	// its own account so the token is not readable by the agent.
	if !strings.Contains(string(bot), "User="+alertsAccountName) {
		t.Errorf("the bot unit does not run under its own account:\n%s", bot)
	}
	for _, boundary := range []string{
		"InaccessiblePaths=-/var/lib/mithril-agent -/etc/mithril-agent",
		"IPAddressDeny=169.254.0.0/16",
		"IPAddressDeny=127.0.0.0/8",
	} {
		if !strings.Contains(string(bot), boundary) {
			t.Errorf("the bot unit is missing %q:\n%s", boundary, bot)
		}
	}
	if strings.Contains(string(bot), sell) {
		t.Errorf("the bot unit was handed a leg configuration:\n%s", bot)
	}

	// The bridge reads one file, passed in by systemd, so it cannot open a
	// second leg — or anything else — on its own.
	bridge, err := os.ReadFile(filepath.Join(directory, "mithril-agent-status-sell.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bridge), "LoadCredential="+statusCredential+":") {
		t.Errorf("the bridge does not receive its status file as a credential:\n%s", bridge)
	}
	cfg, err := readConfig(sell)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bridge), "DynamicUser=yes") ||
		!strings.Contains(string(bridge), "InaccessiblePaths="+filepath.Dir(cfg.Journal.Path)) {
		t.Errorf("the bridge can inherit the trading identity or reopen its source directory:\n%s", bridge)
	}
	if strings.Contains(string(bridge), "\nUser=") || strings.Contains(string(bridge), "\nGroup=") {
		t.Errorf("the bridge runs as the trading account:\n%s", bridge)
	}

	// Enabling a socket-activated service directly starts a bridge with no
	// reader. Only the sockets belong in the enable command.
	if strings.Contains(printed.String(), "enable --now mithril-agent-status-sell.service") {
		t.Errorf("told the operator to enable a socket-activated service:\n%s", printed.String())
	}
}

func TestServiceInstallOptInWiresPaperAlertsIntoTheSingleTelegramConsumer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := armedLegWithStatus(t, t.TempDir(), false, time.Second)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	paperStatus := filepath.Join(t.TempDir(), "champion-alerts.json")
	args := []string{
		"install", "--output", filepath.Join(directory, serviceUnitName),
		"--paper-alert-status", paperStatus,
	}
	var printed bytes.Buffer
	if err := runService(args, &printed); err != nil {
		t.Fatal(err)
	}
	if err := runService(args, io.Discard); err != nil {
		t.Fatalf("generated paper units could not be updated: %v", err)
	}
	for _, name := range []string{
		paperStatusUnitName + ".socket", paperStatusUnitName + ".service", alertsUnitName,
	} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("%s was not generated: %v", name, err)
		}
		if !strings.Contains(printed.String(), name) {
			t.Fatalf("install instructions omit %s:\n%s", name, printed.String())
		}
	}
	bridge, err := os.ReadFile(filepath.Join(directory, paperStatusUnitName+".service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bridge), "LoadCredential="+paperStatusCredential+":"+paperStatus) ||
		!strings.Contains(string(bridge), "DynamicUser=yes") ||
		!strings.Contains(string(bridge), "StartLimitIntervalSec=30s") ||
		!strings.Contains(string(bridge), "StartLimitBurst=12") ||
		strings.Contains(string(bridge), "\nUser=") {
		t.Fatalf("paper bridge boundary is incomplete:\n%s", bridge)
	}
	alerts, err := os.ReadFile(filepath.Join(directory, alertsUnitName))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Wants=network-online.target " + paperStatusUnitName + ".socket",
		"--paper-status-socket " + paperStatusSocketPath,
	} {
		if !strings.Contains(string(alerts), want) {
			t.Errorf("Telegram unit is missing %q:\n%s", want, alerts)
		}
	}
	if strings.Contains(string(alerts), paperStatus) {
		t.Fatalf("Telegram process can read the private paper file:\n%s", alerts)
	}
}

func TestServiceInstallRefusesPaperAlertsWhenTelegramIsDisabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := armedLegWithStatus(t, t.TempDir(), false, time.Second)
	if err := recordStrategy(strategyPaths{sell: sell, telegram: "disabled"}); err != nil {
		t.Fatal(err)
	}
	err := runService([]string{
		"install", "--paper-alert-status", filepath.Join(t.TempDir(), "alerts.json"),
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "requires Telegram") {
		t.Fatalf("paper alerts with Telegram disabled = %v", err)
	}
}

func TestStatusBridgeRetriesUntilTheFirstSnapshotExists(t *testing.T) {
	unit := renderStatusService(servicePlan{Binary: "/bin/true"}, serviceLeg{Name: "sell"})
	for _, want := range []string{
		"StartLimitIntervalSec=0",
		"Restart=on-failure",
		"RestartSec=1s",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("status bridge does not recover from a missing first snapshot (%s):\n%s", want, unit)
		}
	}
}

func listenSupportedMetricsPort(t *testing.T) net.Listener {
	t.Helper()
	for port := 60_000; port <= 65_000; port++ {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			return listener
		}
	}
	t.Fatal("could not allocate a test port in the supported metrics range")
	return nil
}

// The runner binds one metrics port per leg and exits if any is taken. Because
// the unit restarts forever by design, that is a permanent crash loop nobody
// sees without reading the journal — which is exactly how it went twice: once
// against a single-leg `swap run`, once against a runner an earlier rehearsal
// had left behind.
func TestServiceInstallRefusesAMetricsPortAlreadyInUse(t *testing.T) {
	held := listenSupportedMetricsPort(t)
	defer held.Close()
	port := held.Addr().(*net.TCPAddr).Port

	t.Setenv("HOME", t.TempDir())
	sell := armedLegWithStatus(t, t.TempDir(), false, time.Second)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runService([]string{"install", "--metrics-base-port", strconv.Itoa(port)}, &out)
	if err == nil {
		t.Fatal("a unit was written naming a port that is already bound")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(port)) {
		t.Errorf("the refusal does not name the busy port: %v", err)
	}
	// A refusal that does not say how to get past it is a dead end.
	if !strings.Contains(err.Error(), "--metrics-base-port") {
		t.Errorf("the refusal does not name the flag that moves the range: %v", err)
	}
}

// A free port must not be reported as busy, or the check blocks every install.
func TestServiceInstallAcceptsAFreeMetricsRange(t *testing.T) {
	probe := listenSupportedMetricsPort(t)
	port := probe.Addr().(*net.TCPAddr).Port
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	sell := armedLegWithStatus(t, t.TempDir(), false, time.Second)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runService([]string{"install", "--metrics-base-port", strconv.Itoa(port)}, &out); err != nil {
		t.Fatalf("a free port was refused: %v", err)
	}
}

func TestMetricsPortSpanKeepsTheSweepPortBeforeBuyExists(t *testing.T) {
	legs := []serviceLeg{{Name: "sell"}, {Name: "sweep"}}
	if got := metricsPortSpan(legs); got != 3 {
		t.Fatalf("metrics span = %d, want 3 for sell at base and sweep at base+2", got)
	}
}

// Adding the buy leg requires regenerating the staged unit while the existing
// sell runner still owns its metrics port. Treating that expected listener as a
// new collision made the documented resume flow impossible without stopping a
// healthy agent first.
func TestServiceInstallCanUpdateItsOwnStagedUnitWhilePortsAreBusy(t *testing.T) {
	held := listenSupportedMetricsPort(t)
	defer held.Close()
	port := held.Addr().(*net.TCPAddr).Port

	t.Setenv("HOME", t.TempDir())
	sell := armedLegWithStatus(t, t.TempDir(), false, time.Second)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), serviceUnitName)
	if err := os.WriteFile(target, []byte("[Unit]\nDescription=Mithril agent strategy runner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runService([]string{
		"install", "--output", target, "--metrics-base-port", strconv.Itoa(port),
	}, &output); err != nil {
		t.Fatalf("updating the staged unit was blocked by its live predecessor: %v", err)
	}
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(written, []byte("--metrics-base-port "+strconv.Itoa(port))) {
		t.Fatalf("updated unit lost the selected port:\n%s", written)
	}
}

func TestServiceInstallRefusesInvalidMetricsPortRanges(t *testing.T) {
	for _, base := range []int{-1, 0, 1023, 65_001, 65_535} {
		if err := metricsPortsFree(base, 3); err == nil {
			t.Errorf("base port %d was accepted for three legs", base)
		}
	}
}

func TestServiceInstallDoesNotOverwriteAnUnrelatedFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := armedLegWithStatus(t, t.TempDir(), false, time.Second)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), serviceUnitName)
	if err := os.WriteFile(target, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runService([]string{"install", "--output", target}, &bytes.Buffer{}); err == nil {
		t.Fatal("an unrelated existing file was overwritten")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep me\n" {
		t.Fatalf("unrelated file changed to %q", got)
	}
}

func TestGeneratedUnitWriterDoesNotFollowAReplacedSymlink(t *testing.T) {
	directory := t.TempDir()
	referent := filepath.Join(directory, "referent")
	if err := os.WriteFile(referent, []byte("keep me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, serviceUnitName)
	if err := os.Symlink(referent, target); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if err := writeGeneratedUnit(target, []byte("[Unit]\nDescription=Mithril agent runner\n")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(referent)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep me\n" {
		t.Fatalf("generated unit writer followed the replaced symlink: %q", got)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
		t.Fatalf("generated unit mode = %v, want regular 0644", info.Mode())
	}
}

func TestServiceInstallRefusesReplaceableOutputDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := armedLegWithStatus(t, t.TempDir(), false, time.Second)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(directory, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o770); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, serviceUnitName)
	if err := runService([]string{"install", "--output", target}, &bytes.Buffer{}); err == nil {
		t.Fatal("a replaceable service output directory was accepted")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("service unit was written into a replaceable directory: %v", err)
	}
}

func TestServiceInstallChecksEveryGeneratedUnitBeforeWriting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sell := armedLegWithStatus(t, t.TempDir(), false, time.Second)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	target := filepath.Join(directory, serviceUnitName)
	unrelated := filepath.Join(directory, alertsUnitName)
	if err := os.WriteFile(unrelated, []byte("keep this unit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runService([]string{"install", "--output", target}, &bytes.Buffer{}); err == nil {
		t.Fatal("an unrelated sidecar unit was overwritten")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("the runner unit was written before all destinations were checked: %v", err)
	}
	got, err := os.ReadFile(unrelated)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep this unit\n" {
		t.Fatalf("unrelated sidecar changed to %q", got)
	}
}
