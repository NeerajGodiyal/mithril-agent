package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

func generatedPolicyPath(t *testing.T, args ...string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "policy.json")
	full := append([]string{"--out", out,
		"--observe", "So11111111111111111111111111111111111111112"}, args...)
	if err := runShadowPolicy(full, &bytes.Buffer{}); err != nil {
		t.Fatalf("generating a policy failed: %v", err)
	}
	return out
}

// The whole point: a generated policy must load, with no hand-editing. The two
// source digests are the fields nobody can produce by hand, and a policy naming
// the wrong source measures something other than what its author believes.
func TestAGeneratedPolicyLoadsWithoutEditing(t *testing.T) {
	path := generatedPolicyPath(t, "--sell-at-usd", "80.00")

	policy, err := loadShadowPolicy(path)
	if err != nil {
		t.Fatalf("a generated policy did not load: %v", err)
	}
	if policy.Trigger.PrimarySourceSHA256 != pricesource.PythPushIdentitySHA256() ||
		policy.Trigger.SecondarySourceSHA256 != pricesource.CoinbaseIdentitySHA256() {
		t.Fatal("the generated policy does not name the sources the runner uses")
	}
	if policy.Trigger.ThresholdMicros != 80_000_000 {
		t.Errorf("threshold = %d micros, want 80000000", policy.Trigger.ThresholdMicros)
	}
	if policy.Trigger.Direction != pricetrigger.SellAtOrAbove {
		t.Errorf("direction = %q, want a sell", policy.Trigger.Direction)
	}
}

// Buying is the mirror image and the decimals have to swap with it, or every
// price the report computes is out by a factor of a thousand.
func TestABuyPolicySwapsTheDecimals(t *testing.T) {
	policy, err := loadShadowPolicy(generatedPolicyPath(t, "--buy-at-usd", "50.00"))
	if err != nil {
		t.Fatal(err)
	}
	if policy.Trigger.Direction != pricetrigger.BuyAtOrBelow {
		t.Fatalf("direction = %q, want a buy", policy.Trigger.Direction)
	}
	if policy.InputDecimals != 6 || policy.OutputDecimals != 9 {
		t.Errorf("decimals = %d/%d, want 6/9 for a buy",
			policy.InputDecimals, policy.OutputDecimals)
	}
}

// The generated file holds no secret, but it is the operator's configuration
// and is written through the same private-file path as everything else here.
func TestAGeneratedPolicyIsWrittenPrivately(t *testing.T) {
	info, err := os.Stat(generatedPolicyPath(t, "--sell-at-usd", "80.00"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("policy mode = %o, want no group or other access", perm)
	}
}

// Ambiguous or impossible instructions must be refused here, not discovered at
// the start of a run somebody expected to leave going for days.
func TestPolicyGeneratorRefusesAmbiguousInstructions(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "p.json")
	observe := "So11111111111111111111111111111111111111112"
	for name, args := range map[string][]string{
		"neither direction": {"--out", out, "--observe", observe},
		"both directions": {"--out", out, "--observe", observe,
			"--sell-at-usd", "80", "--buy-at-usd", "50"},
		"no observe address": {"--out", out, "--sell-at-usd", "80"},
		"relative out":       {"--out", "policy.json", "--observe", observe, "--sell-at-usd", "80"},
		"no output path":     {"--observe", observe, "--sell-at-usd", "80"},
		"unparseable price": {"--out", out, "--observe", observe,
			"--sell-at-usd", "not-a-price"},
	} {
		if err := runShadowPolicy(args, &bytes.Buffer{}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The printed next step must be the command that actually runs it, and must
// repeat that nothing can be signed.
func TestPolicyGeneratorPrintsTheNextCommand(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runShadowPolicy([]string{
		"--out", filepath.Join(root, "policy.json"),
		"--observe", "So11111111111111111111111111111111111111112",
		"--sell-at-usd", "80.00",
	}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "shadow run --policy") {
		t.Errorf("the output does not name the command that runs it:\n%s", text)
	}
	if !strings.Contains(text, "cannot sign") {
		t.Errorf("the output does not repeat that it cannot sign:\n%s", text)
	}
}
