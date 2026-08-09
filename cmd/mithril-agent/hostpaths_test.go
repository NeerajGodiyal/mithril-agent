package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func touch(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A path the operator typed is an answer to a question. Overriding it with
// something discovered would be worse than asking, because they would have no
// way to tell the tool used something else.
func TestATypedPathIsNeverOverridden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()

	// Discovery must have a COMPETING answer available, or this test passes for
	// the wrong reason: with nothing to find, "never override" is vacuous and a
	// broken precedence check survives it.
	recorded := t.TempDir()
	sell := triggeredLeg(t, recorded, false, 0)
	cfg, err := readConfig(sell)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MCP.Command = touch(t, filepath.Join(recorded, "other-mithril"))
	cfg.Quote.Command = touch(t, filepath.Join(recorded, "other-node"))
	cfg.Quote.ScriptPath = touch(t, filepath.Join(recorded, "other-quote.mjs"))
	cfg.Signer.KeypairPath = touch(t, filepath.Join(recorded, "other-key.json"))
	writeJSON(t, sell, cfg)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}
	if found := resolveHostPaths(hostPaths{}); found.mithrilCommand != cfg.MCP.Command {
		t.Fatalf("discovery has no competing answer, so this test proves nothing: %+v", found)
	}

	typed := hostPaths{
		walletKeypair:  touch(t, filepath.Join(dir, "mine.json")),
		mithrilCommand: touch(t, filepath.Join(dir, "my-mithril")),
		nodeCommand:    touch(t, filepath.Join(dir, "my-node")),
		quoteScript:    touch(t, filepath.Join(dir, "my-quote.mjs")),
	}
	got := resolveHostPaths(typed)
	if got != typed {
		t.Errorf("discovery overrode typed paths:\n got  %+v\n want %+v", got, typed)
	}
	// Nothing was filled in, so nothing should be reported as found.
	if lines := describeResolvedHostPaths(typed, got); len(lines) != 0 {
		t.Errorf("reported paths as discovered that the operator typed: %v", lines)
	}
}

// A working leg records the exact commands that already ran on this machine.
// Nothing beats a value that has succeeded here, so it wins over convention.
func TestPathsComeFromAConfigThatAlreadyWorks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	sell := triggeredLeg(t, dir, false, 0)

	recordedMithril := touch(t, filepath.Join(dir, "recorded-mithril"))
	recordedNode := touch(t, filepath.Join(dir, "recorded-node"))
	recordedQuote := touch(t, filepath.Join(dir, "recorded-quote.mjs"))
	recordedKey := touch(t, filepath.Join(dir, "recorded-key.json"))

	cfg, err := readConfig(sell)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MCP.Command = recordedMithril
	cfg.Quote.Command = recordedNode
	cfg.Quote.ScriptPath = recordedQuote
	cfg.Signer.KeypairPath = recordedKey
	writeJSON(t, sell, cfg)
	if err := recordStrategy(strategyPaths{sell: sell}); err != nil {
		t.Fatal(err)
	}

	got := resolveHostPaths(hostPaths{})
	for label, pair := range map[string][2]string{
		"mithril": {got.mithrilCommand, recordedMithril},
		"node":    {got.nodeCommand, recordedNode},
		"quote":   {got.quoteScript, recordedQuote},
		"wallet":  {got.walletKeypair, recordedKey},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want the recorded %q", label, pair[0], pair[1])
		}
	}
	// And it must say so, so the operator can check what it chose.
	lines := strings.Join(describeResolvedHostPaths(hostPaths{}, got), "\n")
	if !strings.Contains(lines, recordedMithril) {
		t.Errorf("did not report the discovered mithril command:\n%s", lines)
	}
}

// A private key is not something to discover by convention. Adopting one found
// lying beside a binary is precisely the wrong instinct for a program that
// signs transactions, so the sibling lookup must never offer one.
func TestAWalletKeyIsNeverAdoptedFromConvention(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if beside := hostPathsBesideExecutable(); beside.walletKeypair != "" {
		t.Fatalf("a wallet keypair was discovered by convention: %q", beside.walletKeypair)
	}
	// With nothing recorded, the wallet must remain unknown and be ASKED for.
	got := resolveHostPaths(hostPaths{})
	if got.walletKeypair != "" {
		t.Errorf("wallet keypair was invented: %q", got.walletKeypair)
	}
	err := missingHostPaths(got)
	if err == nil {
		t.Fatal("setup accepted an unknown wallet keypair")
	}
	if !strings.Contains(err.Error(), "--wallet-keypair") {
		t.Errorf("the error does not name the flag that supplies it: %v", err)
	}
}

// Guessing a path that is not there converts a clear "tell me where X is" into
// a confusing failure deeper in — the exact thing this discovery exists to
// remove. Only files that EXIST may be adopted.
func TestOnlyPathsThatExistAreAdopted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	for name, path := range map[string]string{
		"missing":   filepath.Join(dir, "not-here"),
		"directory": dir,
		"relative":  "relative/path",
		"empty":     "",
	} {
		if usableFile(path) {
			t.Errorf("%s path was treated as usable: %q", name, path)
		}
	}
	if !usableFile(touch(t, filepath.Join(dir, "real"))) {
		t.Error("a real regular file was rejected")
	}
}

// An error saying only "not found" leaves the operator to work out which of
// four things it meant. It must name every missing flag.
func TestTheRefusalNamesEveryMissingFlag(t *testing.T) {
	err := missingHostPaths(hostPaths{})
	if err == nil {
		t.Fatal("an entirely empty set of paths was accepted")
	}
	for _, flag := range []string{
		"--wallet-keypair", "--mithril-command", "--node-command", "--quote-script",
	} {
		if !strings.Contains(err.Error(), flag) {
			t.Errorf("the refusal does not mention %s: %v", flag, err)
		}
	}
	// And a complete set must be accepted without complaint.
	dir := t.TempDir()
	complete := hostPaths{
		walletKeypair:  touch(t, filepath.Join(dir, "k.json")),
		mithrilCommand: touch(t, filepath.Join(dir, "m")),
		nodeCommand:    touch(t, filepath.Join(dir, "n")),
		quoteScript:    touch(t, filepath.Join(dir, "q.mjs")),
	}
	if err := missingHostPaths(complete); err != nil {
		t.Errorf("a complete set was rejected: %v", err)
	}
}

// Both entry points build a leg on the same host, so both need the same paths.
// Resume returned before discovery ran and failed with "wallet keypair: path
// must be absolute and clean" — a path error for a path nobody was asked for.
func TestBothSetupAndResumeResolveHostPaths(t *testing.T) {
	source, err := os.ReadFile("strategy_setup.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	resume := strings.Index(text, "if *resume {")
	if resume < 0 {
		t.Fatal("the resume branch has moved; this guard needs updating")
	}
	// The resume branch must resolve paths BEFORE it returns, or it inherits
	// whatever the operator failed to type.
	branch := text[resume:]
	if end := strings.Index(branch, "\n\t}\n"); end > 0 {
		branch = branch[:end]
	}
	if !strings.Contains(branch, "fillHostPaths") {
		t.Error("resume returns without resolving host paths")
	}
	// And there must be exactly one implementation, so the two paths cannot
	// drift apart in what they accept.
	if strings.Count(text, "func fillHostPaths") != 1 {
		t.Error("host path resolution is duplicated; the two entry points can diverge")
	}
}

// The two legs are judged against the SAME two evidence providers or they are
// not one strategy. A resume that left them empty produced a buy leg whose
// preflight failed five bindings at once, with nothing in the message
// connecting that to a flag nobody knew to pass.
//
// The sell leg records them, exactly as it records size and price.
func TestResumeInheritsEvidenceProvidersFromTheSellLeg(t *testing.T) {
	source, err := os.ReadFile("strategy_setup.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	resume := strings.Index(text, "func resumeStrategyBuyLeg")
	if resume < 0 {
		t.Fatal("resume has been renamed; this guard needs updating")
	}
	body := text[resume:]
	for _, want := range []string{
		"options.primaryTrust = sellCfg.Evidence.PrimaryTrustDomain",
		"options.secondaryTrust = sellCfg.Evidence.SecondaryTrustDomain",
		"options.quoteSocket = sellCfg.Quote.SocketPath",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("resume does not inherit the evidence providers: missing %q", want)
		}
	}
	// Inheritance must not override something the operator typed.
	if !strings.Contains(body, `if options.primaryTrust == ""`) {
		t.Error("inheritance overrides an explicitly supplied provider")
	}
	if !strings.Contains(body, `if options.quoteSocket == ""`) {
		t.Error("inheritance overrides an explicitly supplied quote socket")
	}
}
