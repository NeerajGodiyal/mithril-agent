package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/telegramoperator"
)

// A non-terminal run must never block waiting for input, or setup hangs
// forever in CI and over a piped SSH command.
func TestSetupNeverBlocksWhenNotATerminal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup")
	var out bytes.Buffer
	// It reports that it wrote no profile — the floor needs a person — but the
	// point here is that it RETURNS rather than waiting on a prompt forever.
	if err := runSetup(t.Context(), []string{"--dir", dir, "--yes"}, &out); err == nil {
		t.Fatal("a non-interactive run wrote a trading profile with nobody to agree to the floor")
	}
	if !strings.Contains(out.String(), "default") {
		t.Error("non-interactive run does not show that defaults were taken")
	}
}

// Setup must create the dedicated account so the reviewer does not have to,
// and must leave an existing one alone.
func TestSetupCreatesAnAccountOnceAndNeverReplacesIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup")
	// The profile is skipped without a terminal; the account still must exist,
	// which is exactly what this test is about.
	_ = runSetup(t.Context(), []string{"--dir", dir, "--yes"}, &bytes.Buffer{})
	account := filepath.Join(dir, "agent-account.json")
	first, err := os.ReadFile(account)
	if err != nil {
		t.Fatalf("setup did not create the agent account: %v", err)
	}
	info, err := os.Stat(account)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("account mode = %o, want 0600", perm)
	}

	var out bytes.Buffer
	// A --yes run cannot agree to a floor, so no profile is written and setup
	// reports that. What it PRINTS is this test's subject.
	_ = runSetup(t.Context(), []string{"--dir", dir, "--yes"}, &out)
	second, err := os.ReadFile(account)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("a second setup replaced the existing account key")
	}
	if !strings.Contains(out.String(), "already present") {
		t.Error("setup did not say it was leaving the existing account alone")
	}
}

// Setup is preparation, never authorisation. It must say so and must not
// pretend the demonstration has been enabled.
func TestSetupNeverAuthorisesATrade(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup")
	var out bytes.Buffer
	// Skipping the profile is expected here; what it SAYS is the subject.
	_ = runSetup(t.Context(), []string{"--dir", dir, "--yes"}, &out)
	text := out.String()
	if !strings.Contains(text, "did not authorise") {
		t.Error("setup does not state that it authorised nothing")
	}
	for _, forbidden := range []string{"trade started", "now trading", "enabled trading"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Errorf("setup implies it started trading: %q", forbidden)
		}
	}
}

// Missing host prerequisites must be reported honestly rather than producing a
// configuration that looks complete and then fails at run time.
func TestSetupReportsMissingHostPrerequisites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup")
	var out bytes.Buffer
	if err := runSetup(t.Context(), []string{"--dir", dir, "--yes"}, &out); err == nil {
		t.Fatal("setup reported success with host prerequisites missing")
	}
	text := out.String()
	// The Orca adapter cannot exist in a fresh temp directory.
	if !strings.Contains(text, "Still needed") {
		t.Fatalf("setup did not report missing prerequisites:\n%s", text)
	}
	if !strings.Contains(text, "quote adapter") {
		t.Error("setup did not name the missing Orca adapter")
	}
	if !strings.Contains(text, "host prerequisites, not settings") {
		t.Error("setup does not explain that these cannot be configured away")
	}
}

// The unrecognised answer must never be read as consent.
func TestConfirmTreatsAnythingUnrecognisedAsNo(t *testing.T) {
	for _, answer := range []string{"maybe\n", "sure\n", "1\n", "yolo\n"} {
		p := newPrompter(strings.NewReader(answer), &bytes.Buffer{}, true)
		got, err := p.confirm("Proceed?", false)
		if err != nil {
			t.Fatal(err)
		}
		if got {
			t.Errorf("answer %q was treated as consent", strings.TrimSpace(answer))
		}
	}
	// Enter accepts the stated default, in both directions.
	p := newPrompter(strings.NewReader("\n"), &bytes.Buffer{}, true)
	if got, _ := p.confirm("Proceed?", true); !got {
		t.Error("Enter did not accept a yes-default")
	}
	p = newPrompter(strings.NewReader("\n"), &bytes.Buffer{}, true)
	if got, _ := p.confirm("Proceed?", false); got {
		t.Error("Enter did not accept a no-default")
	}
}

// Enter accepts the default; a typed value replaces it and is recorded as
// non-default so the summary is truthful.
func TestAskRecordsWhetherTheDefaultWasUsed(t *testing.T) {
	p := newPrompter(strings.NewReader("\n/custom/path\n"), &bytes.Buffer{}, true)
	first, err := p.ask("Where?", "/default")
	if err != nil || first != "/default" {
		t.Fatalf("Enter gave %q, %v", first, err)
	}
	second, err := p.ask("Where else?", "/other-default")
	if err != nil || second != "/custom/path" {
		t.Fatalf("typed value gave %q, %v", second, err)
	}
	if !p.answers[0].Default || p.answers[1].Default {
		t.Fatalf("default tracking is wrong: %+v", p.answers)
	}
}

// A bot token must never be requested at a prompt: it would land in shell
// history and scrollback. The wizard explains instead of asking.
func TestSetupNeverPromptsForTheTelegramToken(t *testing.T) {
	t.Setenv(telegramoperator.BotTokenEnvironment, "")
	t.Setenv(telegramoperator.AllowedIDsEnvironment, "")
	dir := filepath.Join(t.TempDir(), "setup")
	var out bytes.Buffer
	// A --yes run cannot agree to a floor, so no profile is written and setup
	// reports that. What it PRINTS is this test's subject.
	_ = runSetup(t.Context(), []string{"--dir", dir, "--yes"}, &out)
	text := out.String()
	if !strings.Contains(text, "shell history") {
		t.Error("setup does not explain why it will not take the token")
	}
	if !strings.Contains(text, telegramoperator.BotTokenEnvironment) {
		t.Error("setup does not name the environment variable to set")
	}
	// It must never ask for the token itself. A question is the line directly
	// above the "[default] ›" line, so check those rather than the whole
	// transcript — the temp directory path would otherwise match by accident.
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		if !strings.Contains(line, "›") || index == 0 {
			continue
		}
		question := strings.ToLower(lines[index-1])
		if strings.Contains(question, "token") || strings.Contains(question, "secret") {
			t.Fatalf("setup prompted for a secret: %q", lines[index-1])
		}
	}
}

// When Telegram IS configured, setup must say it is read-only rather than
// implying it can act.
func TestSetupSaysTelegramIsReadOnlyWhenConfigured(t *testing.T) {
	t.Setenv(telegramoperator.BotTokenEnvironment, "123:SECRET")
	t.Setenv(telegramoperator.AllowedIDsEnvironment, "111")
	dir := filepath.Join(t.TempDir(), "setup")
	var out bytes.Buffer
	// A --yes run cannot agree to a floor, so no profile is written and setup
	// reports that. What it PRINTS is this test's subject.
	_ = runSetup(t.Context(), []string{"--dir", dir, "--yes"}, &out)
	text := out.String()
	if !strings.Contains(text, "read-only") {
		t.Error("setup does not state Telegram is read-only")
	}
	if strings.Contains(text, "123:SECRET") {
		t.Fatal("setup echoed the Telegram bot token")
	}
}

// guidedProfileFixture wires the guided path onto the same fake discovery the
// scripted path uses, so the two cannot be tested into agreement while
// diverging in reality.
func guidedProfileFixture(t *testing.T, answers string, minOutput uint64) (
	setupChoices, *prompter, *bytes.Buffer, *int,
) {
	t.Helper()
	// Setup records where it put the configuration. Without an isolated home
	// the suite writes that pointer into the real one, so running the tests
	// changes the machine that ran them.
	t.Setenv("HOME", t.TempDir())
	fixture := newSwapSetupFixture(t)
	// Setup probes the Node runtime's version, so the stand-in has to report a
	// supported one rather than being an inert file.
	if err := os.WriteFile(fixture.nodeCommand,
		[]byte("#!/bin/sh\necho v24.18.0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	route := testSwapProfile(fixture.owner).Route
	route.MinOutputAmount = minOutput
	calls := 0
	installSwapSetupTestHooks(t, fixture.agentCommand, func(
		context.Context, string, string, string, uint64, uint16,
	) (orcaswap.Policy, error) {
		calls++
		return route, nil
	})
	choices := setupChoices{
		directory:      filepath.Dir(fixture.setupDirectory),
		direction:      "sell",
		accountKeypair: fixture.walletKeypair,
		mithrilCommand: fixture.mithrilCommand,
		mithrilConfig:  fixture.mithrilConfig,
		nodeCommand:    fixture.nodeCommand,
		quoteScript:    fixture.quoteScript,
	}
	out := &bytes.Buffer{}
	return choices, newPrompter(strings.NewReader(answers), out, true), out, &calls
}

// The whole point of the guided path: the operator sees the live floor and
// agrees to it inside one command, so there is no window for the market to
// move between reading a number and using it.
func TestGuidedSetupConfirmsTheLiveQuoteWithoutReQuoting(t *testing.T) {
	const floor = uint64(21_525)
	choices, p, out, calls := guidedProfileFixture(t, "\n\ny\n", floor)
	choices.quoteSocket = "/run/mithril-agent-quote/quote.sock"

	configPath, err := configureSwapProfile(t.Context(), p, choices, out)
	if err != nil {
		t.Fatal(err)
	}
	if configPath == "" {
		t.Fatal("guided setup wrote no configuration after the quote was confirmed")
	}
	if *calls != 1 {
		t.Fatalf("quoted %d times; the guided path must quote exactly once", *calls)
	}
	if _, err := os.Lstat(configPath); err != nil {
		t.Fatalf("configuration is not on disk: %v", err)
	}
	cfg, err := readConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Quote.SocketPath != choices.quoteSocket {
		t.Fatalf("quote socket = %q, want %q", cfg.Quote.SocketPath, choices.quoteSocket)
	}
	// The operator must have been shown the floor in readable units, not raw
	// integer units they cannot sanity-check.
	if !strings.Contains(out.String(), "0.021525 devUSDC") {
		t.Errorf("the floor was not shown in readable units:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "0.001000000 SOL") {
		t.Errorf("the amount spent was not shown in readable units:\n%s", out.String())
	}
	if strings.Contains(out.String(), `"status":"configured"`) ||
		strings.Contains(out.String(), `"plan_argv"`) {
		t.Errorf("guided setup printed internal JSON:\n%s", out.String())
	}
}

func TestGuidedSetupRediscoversAnExistingProfile(t *testing.T) {
	choices, p, out, _ := guidedProfileFixture(t, "\n\ny\n", 21_525)
	configPath, err := configureSwapProfile(t.Context(), p, choices, out)
	if err != nil {
		t.Fatal(err)
	}

	newHome := t.TempDir()
	t.Setenv("HOME", newHome)
	if _, err := configureSwapProfile(
		t.Context(), newPrompter(strings.NewReader(""), out, true), choices, out,
	); err != nil {
		t.Fatal(err)
	}
	if got := recordedConfig(); got != configPath {
		t.Fatalf("recorded config = %q, want %q", got, configPath)
	}
}

// Declining is a normal answer, not an error, and it must leave nothing behind.
func TestGuidedSetupWritesNothingWhenTheQuoteIsDeclined(t *testing.T) {
	choices, p, out, _ := guidedProfileFixture(t, "\n\nn\n", 21_525)

	configPath, err := configureSwapProfile(t.Context(), p, choices, out)
	if err != nil {
		t.Fatalf("declining a quote must not be an error: %v", err)
	}
	if configPath != "" {
		t.Fatal("a configuration was written despite the quote being declined")
	}
	if _, err := os.Lstat(filepath.Join(choices.directory, "profile")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the profile directory was created despite the quote being declined")
	}
}

// Holding Enter through the wizard must never write a policy. The confirmation
// defaults to no.
func TestGuidedSetupTreatsEnterAsDeclineOnTheQuote(t *testing.T) {
	choices, p, out, _ := guidedProfileFixture(t, "\n\n\n", 21_525)

	configPath, err := configureSwapProfile(t.Context(), p, choices, out)
	if err != nil {
		t.Fatal(err)
	}
	if configPath != "" {
		t.Fatal("pressing Enter through the wizard configured a trade")
	}
}

// The gate must not be bypassable by calling the builder directly with neither
// form of confirmation.
func TestSwapSetupRefusesWithoutAnyConfirmation(t *testing.T) {
	fixture := newSwapSetupFixture(t)
	_, err := createSwapSetup(t.Context(), swapSetupOptions{
		directory: fixture.setupDirectory, direction: "sell",
		walletKeypair:  fixture.walletKeypair,
		mithrilCommand: fixture.mithrilCommand, nodeCommand: fixture.nodeCommand,
		quoteScript: fixture.quoteScript,
	})
	if err == nil {
		t.Fatal("a profile was built with no confirmed minimum output at all")
	}
}

// A non-interactive run must never agree to a price on the operator's behalf.
func TestGuidedSetupNeverConfirmsAQuoteNonInteractively(t *testing.T) {
	choices, _, out, calls := guidedProfileFixture(t, "", 21_525)
	quiet := newPrompter(strings.NewReader(""), out, false)

	configPath, err := configureSwapProfile(t.Context(), quiet, choices, out)
	if err != nil {
		t.Fatal(err)
	}
	if configPath != "" {
		t.Fatal("a non-interactive run configured a trade with no human agreement")
	}
	if *calls != 0 {
		t.Error("a non-interactive run read a live quote it could never confirm")
	}
}

// Both prompts are bounded. An unbounded consent prompt is never a yes, but it
// should not buffer an arbitrarily long line to find that out.
func TestPromptsAreBounded(t *testing.T) {
	long := strings.Repeat("y", maxAnswerBytes+1) + "\n"

	p := newPrompter(strings.NewReader(long), &bytes.Buffer{}, true)
	agreed, err := p.confirm("Proceed?", false)
	if err == nil {
		t.Error("confirm accepted an unbounded line")
	}
	if agreed {
		t.Fatal("an over-long answer was treated as consent")
	}
	p = newPrompter(strings.NewReader(long), &bytes.Buffer{}, true)
	if _, err := p.ask("Where?", "/default"); err == nil {
		t.Error("ask accepted an unbounded line")
	}
}

// A supervised installation puts the node runtime and the quote adapter in one
// place. Setup must look there, or a reviewer on a prepared host is asked for
// absolute paths they have no way to know — which is where they give up.
func TestSetupDetectsAnInstalledLayout(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "libexec", "mithril-agent")
	if err := os.MkdirAll(installed, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"node", "quote.mjs", "mithril-node"} {
		if err := os.WriteFile(filepath.Join(installed, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	previous := installedLibexecDirs
	installedLibexecDirs = []string{installed}
	t.Cleanup(func() { installedLibexecDirs = previous })

	for name, want := range map[string]string{
		"node":         filepath.Join(installed, "node"),
		"quote.mjs":    filepath.Join(installed, "quote.mjs"),
		"mithril-node": filepath.Join(installed, "mithril-node"),
	} {
		if got := detectInstalled(name); got != want {
			t.Errorf("detectInstalled(%q) = %q, want %q", name, got, want)
		}
	}
	if got := detectInstalled("not-installed"); got != "" {
		t.Errorf("detectInstalled found something that is not there: %q", got)
	}
	// A directory is not an executable and must not be offered as one.
	if err := os.MkdirAll(filepath.Join(installed, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := detectInstalled("adir"); got != "" {
		t.Errorf("a directory was offered as a file: %q", got)
	}
}

// The agent runs the node as a health monitor before it acts, and that needs
// the node's own config. Omitting the question produced a configuration that
// looked complete and could never observe anything.
func TestSetupRequiresTheMithrilNodeConfig(t *testing.T) {
	missing := missingSetupInputs(setupChoices{
		mithrilCommand: "/usr/bin/true",
		nodeCommand:    "/usr/bin/true",
		quoteScript:    "/usr/bin/true",
	})
	found := false
	for _, item := range missing {
		if strings.Contains(item, "config.toml") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a setup with no node config reported it as complete: %v", missing)
	}
}

// The audience is somebody who already runs Mithril, so setup must find THEIR
// layout — `mithril config init` writes config.toml into the working directory
// and ~/.mithril is the data directory — not only our packaging.
func TestSetupFindsAnOperatorsOwnMithrilConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	t.Chdir(work)

	if got := detectNodeConfig(); got != "" {
		t.Fatalf("found a config where none exists: %q", got)
	}
	// The working directory, where `mithril config init` puts it.
	local := filepath.Join(work, "config.toml")
	if err := os.WriteFile(local, []byte("[network]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := detectNodeConfig()
	if !filepath.IsAbs(got) {
		t.Errorf("detection returned a relative path: %q", got)
	}
	if resolved, err := filepath.EvalSymlinks(got); err != nil ||
		resolved != mustEval(t, local) {
		t.Errorf("detected %q, want %q", got, local)
	}

	// With none in the working directory, the Mithril data directory is next.
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(home, ".mithril")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	inHome := filepath.Join(dataDir, "config.toml")
	if err := os.WriteFile(inHome, []byte("[network]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := detectNodeConfig(); mustEval(t, got) != mustEval(t, inHome) {
		t.Errorf("detected %q, want %q", got, inHome)
	}
}

// Somebody who cloned this and ran `make adapter` has the adapter in their
// checkout, not in an install directory.
func TestSetupFindsTheAdapterInASourceCheckout(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	if got := detectSourceAdapter(); got != "" {
		t.Fatalf("found an adapter where none exists: %q", got)
	}
	adapter := filepath.Join(work, "adapters", "orca", "quote.mjs")
	if err := os.MkdirAll(filepath.Dir(adapter), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adapter, []byte("// adapter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := detectSourceAdapter()
	if !filepath.IsAbs(got) {
		t.Errorf("detection returned a relative path: %q", got)
	}
	if mustEval(t, got) != mustEval(t, adapter) {
		t.Errorf("detected %q, want %q", got, adapter)
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

// A present but wrong-version Node is worse than an absent one: setup looks
// settled and the adapter refuses at run time, after the operator believes they
// are finished.
func TestSetupRejectsAnUnsupportedNodeVersion(t *testing.T) {
	root := t.TempDir()
	fake := filepath.Join(root, "node")
	// A stand-in that reports an old version, so no real Node is needed.
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho v23.11.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if reason := unsupportedNodeReason(fake); !strings.Contains(reason, "23.11.0") ||
		!strings.Contains(reason, "24.18") {
		t.Fatalf("an old Node was not reported clearly: %q", reason)
	}

	supported := filepath.Join(root, "node-ok")
	if err := os.WriteFile(supported, []byte("#!/bin/sh\necho v24.18.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if reason := unsupportedNodeReason(supported); reason != "" {
		t.Errorf("a supported Node was rejected: %q", reason)
	}
	newer := filepath.Join(root, "node-newer")
	if err := os.WriteFile(newer, []byte("#!/bin/sh\necho v24.20.1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if reason := unsupportedNodeReason(newer); reason != "" {
		t.Errorf("a newer 24.x Node was rejected: %q", reason)
	}
	// The next major is out of range too.
	next := filepath.Join(root, "node-next")
	if err := os.WriteFile(next, []byte("#!/bin/sh\necho v25.0.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if reason := unsupportedNodeReason(next); reason == "" {
		t.Error("Node 25 was accepted; the adapter pins the 24.x line")
	}
	// Absent or unset is somebody else's finding, not this one's.
	if reason := unsupportedNodeReason(""); reason != "" {
		t.Errorf("an unset path produced a version complaint: %q", reason)
	}
	if reason := unsupportedNodeReason(filepath.Join(root, "absent")); reason != "" {
		t.Errorf("an absent path produced a version complaint: %q", reason)
	}
}

// Without pre-seeded answers setup can only ever be run by hand: a
// non-interactive run takes every default, and there is no way to supply a path
// it cannot guess. That makes both unattended deployment and end-to-end testing
// impossible, which is how a broken flow stays undiscovered.
func TestSetupCanBeDrivenFromAScript(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	node := filepath.Join(root, "node")
	if err := os.WriteFile(node, []byte("#!/bin/sh\necho v24.18.0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "node-config.toml")
	if err := os.WriteFile(config, []byte("[network]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runSetup(t.Context(), []string{
		"--dir", filepath.Join(root, "setup"),
		"--direction", "buy",
		"--mithril-command", node,
		"--mithril-config", config,
		"--node-command", node,
		"--quote-script", config,
		"--yes",
	}, &out)
	// Scripted runs still skip the profile: the floor needs a person.
	_ = err
	text := out.String()
	// Every supplied value must appear as the chosen answer, not the default.
	for _, want := range []string{"buy", config, node} {
		if !strings.Contains(text, want) {
			t.Errorf("a value given as a flag was ignored: %q\n%s", want, text)
		}
	}
	// And with everything supplied, nothing should be reported missing.
	if strings.Contains(text, "Still needed") {
		t.Errorf("a fully specified setup still reported missing inputs:\n%s", text)
	}
}

// A flag must not be able to smuggle in a direction the profile cannot use.
func TestSetupRejectsAnInvalidDirectionFlag(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	err := runSetup(t.Context(), []string{
		"--dir", filepath.Join(root, "setup"), "--direction", "sideways", "--yes",
	}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("setup accepted a direction that is neither sell nor buy")
	}
}

// "Clear the items above" when there are no items above is the kind of
// instruction that makes somebody give up. Each ending has to match the state
// it actually describes.
func TestSetupNextStepMatchesTheActualState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	node := filepath.Join(root, "node")
	if err := os.WriteFile(node, []byte("#!/bin/sh\necho v24.18.0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "c.toml")
	if err := os.WriteFile(config, []byte("[network]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Everything present, but no profile written: must not say "clear the
	// items above", because there are none.
	var complete bytes.Buffer
	if err := runSetup(t.Context(), []string{
		"--dir", filepath.Join(root, "a"), "--mithril-command", node,
		"--mithril-config", config, "--node-command", node,
		"--quote-script", config, "--yes",
	}, &complete); err == nil {
		t.Fatal("a non-interactive run wrote a profile nobody agreed to")
	}
	if strings.Contains(complete.String(), "Still needed") {
		t.Fatalf("a fully specified setup reported missing inputs:\n%s", complete.String())
	}
	if strings.Contains(complete.String(), "Clear the items above") {
		t.Error("setup said to clear items when nothing was missing")
	}

	// Something genuinely missing: the instruction is then correct.
	var incomplete bytes.Buffer
	if err := runSetup(t.Context(), []string{
		"--dir", filepath.Join(root, "b"), "--yes",
	}, &incomplete); err == nil {
		t.Fatal("an incomplete setup reported success")
	}
	if !strings.Contains(incomplete.String(), "Still needed") {
		t.Fatalf("an incomplete setup reported nothing missing:\n%s", incomplete.String())
	}
	if !strings.Contains(incomplete.String(), "Clear the items above") {
		t.Error("an incomplete setup did not tell the operator to clear them")
	}
}

// Pointing setup at an account you already have is a normal thing to do. When
// you do, the setup directory is never created as a side effect of writing a
// keypair into it — and the profile step used to fail with "parent directory is
// unsafe" when the truth was that it did not exist.
func TestSetupCreatesItsDirectoryEvenWithAnExistingAccount(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	// An account that lives somewhere else entirely.
	elsewhere := filepath.Join(root, "existing", "account.json")
	if err := os.MkdirAll(filepath.Dir(elsewhere), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runWalletNew([]string{"--file", elsewhere}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(root, "node")
	if err := os.WriteFile(node, []byte("#!/bin/sh\necho v24.18.0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	setupDir := filepath.Join(root, "chosen")

	choices := setupChoices{
		directory: setupDir, direction: "sell", accountKeypair: elsewhere,
		mithrilCommand: node, mithrilConfig: node,
		nodeCommand: node, quoteScript: node,
	}
	// Non-interactive, so the profile step stops before quoting — but the
	// directory must exist by then regardless.
	p := newPrompter(strings.NewReader(""), &bytes.Buffer{}, false)
	if _, err := configureSwapProfile(t.Context(), p, choices, &bytes.Buffer{}); err != nil {
		t.Fatalf("configuring the profile failed: %v", err)
	}
	info, err := os.Stat(setupDir)
	if err != nil {
		t.Fatalf("the chosen setup directory was never created: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("setup directory mode = %o, want no group or other access", perm)
	}
}

// "unsafe" and "does not exist" are different problems with different fixes,
// and telling somebody their directory is unsafe when it is simply absent sends
// them looking at permissions for something that is not there.
func TestMissingParentIsNotReportedAsUnsafe(t *testing.T) {
	root := t.TempDir()
	_, err := cleanNewSetupPath(filepath.Join(root, "absent", "profile"))
	if err == nil {
		t.Fatal("a path under a missing parent was accepted")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("a missing parent was reported as %q", err.Error())
	}
}

// Detection must not carry directory names from any particular deployment.
// A site-specific path would be dead weight on every other machine and would
// imply this software knows about installations it does not.
func TestDetectionCarriesNoSiteSpecificPaths(t *testing.T) {
	for _, dir := range installedLibexecDirs {
		if dir != "/usr/local/libexec/mithril-agent" {
			t.Errorf("install detection carries a non-standard path: %q", dir)
		}
	}
	// And nothing in the shipped source should name a specific host or user.
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	// Test files are scanned too. Skipping them left the largest hole: fixtures
	// are where a real address, endpoint or host gets pasted in "just to make
	// the test run", and a public repository ships them exactly like source.
	// One did — a payout wallet sat in a strategy fixture for a whole day.
	for _, name := range entries {
		// Only this file, which has to contain the needles to search for them.
		if name == "setup_test.go" {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, leaked := range []string{"/home/ubuntu", "live3", "live4"} {
			if strings.Contains(string(source), leaked) {
				t.Errorf("%s contains a site-specific reference: %q", name, leaked)
			}
		}
		// A routable IP literal is somebody's real machine. Standard private
		// ranges are allowed only as systemd deny rules; every other literal must
		// be loopback or an RFC 5737 documentation address.
		for line := range strings.SplitSeq(string(source), "\n") {
			if strings.Contains(line, "IPAddressDeny=") {
				continue
			}
			for _, address := range ipv4Literal.FindAllString(line, -1) {
				if allowedExampleIP(address) {
					continue
				}
				t.Errorf("%s names a routable address %q; use a 192.0.2.x documentation address", name, address)
			}
		}
	}
}

var ipv4Literal = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

// allowedExampleIP permits the addresses that are safe to publish: loopback,
// "any", and the three ranges RFC 5737 reserves for documentation.
func allowedExampleIP(address string) bool {
	parsed := net.ParseIP(address)
	if parsed == nil || parsed.IsLoopback() || parsed.IsUnspecified() {
		return true
	}
	for _, block := range []string{"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24"} {
		_, network, err := net.ParseCIDR(block)
		if err == nil && network.Contains(parsed) {
			return true
		}
	}
	return false
}

// Setup that wrote no trading profile must not exit 0. It printed a cheerful
// "Next" list either way, so a zero status was the only thing distinguishing
// "your agent is configured" from "nothing was written", and it said the wrong
// one.
func TestSetupThatWroteNoProfileFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup")
	var out bytes.Buffer
	err := runSetup(t.Context(), []string{"--dir", dir, "--yes"}, &out)
	if err == nil {
		t.Fatal("setup exited successfully having written no trading profile")
	}
	if !strings.Contains(err.Error(), "no trading profile") {
		t.Errorf("the failure does not name what is missing: %v", err)
	}
	// And the reason must already be on screen — an error naming nothing
	// actionable is its own dead end.
	if !strings.Contains(out.String(), "Still needed") {
		t.Errorf("setup failed without explaining why:\n%s", out.String())
	}
}
