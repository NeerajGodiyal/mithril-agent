package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func writeShadowPolicy(t *testing.T, policy shadow.Policy) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "shadow-policy.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validShadowPolicy() shadow.Policy {
	return shadow.Policy{
		Version: shadow.Version, Cluster: shadow.Mainnet,
		Trigger: pricetrigger.Policy{
			Version: pricetrigger.Version, Feed: pricetrigger.FeedSOLUSD,
			Direction: pricetrigger.SellAtOrAbove, ThresholdMicros: 200_000_000,
			MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90,
			MaxDeviationBPS: 200, MaxConfidenceBPS: 200,
			PrimarySourceSHA256:   strings.Repeat("a", 64),
			SecondarySourceSHA256: strings.Repeat("b", 64),
		},
		Observe:     "So11111111111111111111111111111111111111112",
		InputAmount: 1_000_000, InputDecimals: 9, OutputDecimals: 6,
		SlippageBPS: 100, FeeLamports: 5_000,
		TickSeconds: 60, SettleSeconds: 30,
		StartingInputUnits: 1_000_000_000,
	}
}

// A shadow policy has no field that could name a key, so a configuration that
// tries to supply one must fail to load rather than be quietly ignored.
func TestShadowPolicyCannotCarryAKey(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "policy.json")
	if err := os.WriteFile(path,
		[]byte(`{"version":1,"cluster":"mainnet-beta","keypair_path":"/tmp/key.json"}`),
		0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadShadowPolicy(path); err == nil {
		t.Fatal("a policy naming a keypair was accepted")
	}
}

// An invalid policy must be refused at load, not discovered mid-run.
func TestShadowPolicyIsValidatedAtLoad(t *testing.T) {
	policy := validShadowPolicy()
	policy.FeeLamports = 0
	if _, err := loadShadowPolicy(writeShadowPolicy(t, policy)); err == nil {
		t.Error("a policy that charges no fee was accepted")
	}
	if _, err := loadShadowPolicy(""); err == nil {
		t.Error("shadow run accepted no policy at all")
	}
	if _, err := loadShadowPolicy(writeShadowPolicy(t, validShadowPolicy())); err != nil {
		t.Errorf("a valid policy was rejected: %v", err)
	}
}

// The endpoint must come from the environment, never a flag, and must never
// appear in an error a user could paste into a bug report.
func TestShadowEndpointIsNeverEchoed(t *testing.T) {
	const secret = "https://rpc.invalid/path?api-key=super-secret"
	t.Setenv(shadowEndpointEnvironment, "http://insecure.invalid")
	_, err := openShadowRun(validShadowPolicy(), shadowRunOptions{directory: "/tmp/x"})
	if err == nil {
		t.Fatal("a plain http endpoint was accepted")
	}
	if strings.Contains(err.Error(), "insecure.invalid") {
		t.Errorf("the error echoed the endpoint: %v", err)
	}

	t.Setenv(shadowEndpointEnvironment, secret)
	_, err = openShadowRun(validShadowPolicy(), shadowRunOptions{directory: "/tmp/x"})
	if err == nil {
		t.Fatal("a run opened with no quote adapter configured")
	}
	if strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), "rpc.invalid") {
		t.Errorf("the error echoed the endpoint: %v", err)
	}
}

// Shadow mode must refuse to run without a real quote source rather than
// inventing one. A modelled fill is not evidence.
func TestShadowRunRequiresARealQuoteAdapter(t *testing.T) {
	if _, err := newShadowQuoter(shadowRunOptions{}); err == nil {
		t.Fatal("a quoter was built with no adapter at all")
	}
	if _, err := newShadowQuoter(shadowRunOptions{
		nodeCommand: "/usr/bin/node", quoteScript: "/tmp/quote.mjs",
	}); err == nil {
		t.Error("a quoter was built with no pool or mint")
	}
}

// The command must reject stray arguments rather than silently ignoring them.
func TestShadowRunRejectsPositionalArguments(t *testing.T) {
	if err := runShadowRun(t.Context(), []string{"enable"}, &bytes.Buffer{}); err == nil {
		t.Fatal("shadow run accepted a positional argument")
	}
}

// Help must state plainly that nothing is signed, because that is the fact a
// reviewer most needs before pointing this at Mainnet.
func TestShadowRunHelpStatesItSignsNothing(t *testing.T) {
	var out bytes.Buffer
	if err := runShadowRun(t.Context(), []string{"-h"}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "Nothing is") || !strings.Contains(text, "no key is loaded") {
		t.Errorf("help does not state that nothing is signed:\n%s", text)
	}
}

// The journal rolls on the UTC day so one file is exactly one reporting period.
func TestDailyJournalRollsOnTheUTCDay(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	roll, err := newDailyJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer roll.Close()

	first := time.Date(2026, 3, 1, 23, 59, 0, 0, time.UTC)
	if err := roll.Record(first, shadow.EventWaiting, map[string]uint64{"price": 1}); err != nil {
		t.Fatal(err)
	}
	if roll.Day() != "2026-03-01" {
		t.Fatalf("day = %q, want 2026-03-01", roll.Day())
	}
	next := first.Add(2 * time.Minute)
	if !roll.RolledOver(next) {
		t.Fatal("crossing midnight was not reported as a rollover")
	}
	if err := roll.Record(next, shadow.EventWaiting, map[string]uint64{"price": 2}); err != nil {
		t.Fatal(err)
	}
	if roll.Day() != "2026-03-02" {
		t.Fatalf("day = %q, want 2026-03-02", roll.Day())
	}
	for _, name := range []string{"shadow-2026-03-01.jsonl", "shadow-2026-03-02.jsonl"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("%s was not written: %v", name, err)
		}
	}
}

// A relative or unclean directory must be refused: the journal is the evidence,
// and it has to land where the operator thinks it does.
func TestDailyJournalRefusesAnUnsafeDirectory(t *testing.T) {
	for _, directory := range []string{"", "relative/dir", "/tmp/../tmp/dir"} {
		if _, err := newDailyJournal(directory); err == nil {
			t.Errorf("accepted %q", directory)
		}
	}
}

// liveStubQuoter stands in for the Orca adapter so the live test exercises the
// real price path without needing a Node runtime. It quotes off the observed
// price, which is enough to prove the pipeline runs; it is not used to produce
// any reported result.
type liveStubQuoter struct{ estimated uint64 }

func (q liveStubQuoter) Quote(
	context.Context, string, uint64, uint16,
) (shadow.Quote, error) {
	return shadow.Quote{
		InputAmount: 1_000_000, EstimatedOutput: q.estimated,
		MinimumOutput: q.estimated - q.estimated/100,
	}, nil
}

// TestLiveShadowReadsMainnet proves the keyless path end to end against the
// real network: two independent sources, no credential, no key, a real journal
// on disk, and a report at the end. It is skipped unless explicitly asked for,
// like the other live smoke tests in this repo.
func TestLiveShadowReadsMainnet(t *testing.T) {
	endpoint := os.Getenv("MITHRIL_AGENT_LIVE_SOLANA_RPC")
	if os.Getenv("MITHRIL_AGENT_LIVE_PRICE_TEST") != "1" || endpoint == "" {
		t.Skip("set MITHRIL_AGENT_LIVE_PRICE_TEST=1 and MITHRIL_AGENT_LIVE_SOLANA_RPC")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	primary, err := pricesource.NewPythPush(publicAccountReader(endpoint), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	secondary := pricesource.NewCoinbase(nil)

	policy := validShadowPolicy()
	policy.Trigger.PrimarySourceSHA256 = primary.IdentitySHA256()
	policy.Trigger.SecondarySourceSHA256 = secondary.IdentitySHA256()
	// A threshold of one micro-dollar always triggers, so the quote and
	// settlement path is exercised rather than skipped.
	policy.Trigger.ThresholdMicros = 1
	policy.SettleSeconds = 1

	roll, err := newDailyJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer roll.Close()

	run := &shadowRun{
		policy: policy, primary: primary, secondary: secondary,
		quoter: liveStubQuoter{estimated: 21_525}, roll: roll,
	}
	if run.runner, err = run.newRunner(); err != nil {
		t.Fatal(err)
	}
	for tick := range 3 {
		observed, err := run.runner.Step(t.Context(), time.Now().UTC().Add(
			time.Duration(tick)*2*time.Second))
		if err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		if observed.Event == shadow.EventUnobservable {
			t.Fatalf("tick %d could not read Mainnet", tick)
		}
		if observed.PriceMicros == 0 {
			t.Fatalf("tick %d observed no price", tick)
		}
		run.lastPrice = observed.PriceMicros
		t.Logf("tick %d: %s at $%d.%06d", tick, observed.Event,
			observed.PriceMicros/1_000_000, observed.PriceMicros%1_000_000)
	}
	var out bytes.Buffer
	if err := run.finishDay(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Nothing here was traded") {
		t.Fatalf("the live report does not state that nothing was traded:\n%s", out.String())
	}
	t.Log("\n" + out.String())
}

// TLS is required for anything off-box. Plain HTTP is permitted only to
// loopback, where there is nothing between us and the node to intercept it —
// and where refusing it would push an operator toward a public endpoint, which
// is worse evidence than their own verifying node.
func TestShadowEndpointAllowsLoopbackHTTPOnly(t *testing.T) {
	for _, allowed := range []string{
		"https://api.devnet.solana.com",
		"http://127.0.0.1:8898",
		"http://localhost:8898",
		"http://[::1]:8898",
	} {
		if err := validateShadowEndpoint(allowed); err != nil {
			t.Errorf("%s was refused: %v", allowed, err)
		}
	}
	for _, refused := range []string{
		"", "http://example.invalid", "http://10.0.0.5:8899",
		"ftp://127.0.0.1", "https://user:pass@rpc.invalid", "not a url",
		"http://127.0.0.1.evil.invalid",
	} {
		if err := validateShadowEndpoint(refused); err == nil {
			t.Errorf("%q was accepted", refused)
		}
	}
	// The endpoint must never appear in the refusal: it can carry a key.
	err := validateShadowEndpoint("http://attacker.invalid/x?api-key=SECRET123")
	if err == nil {
		t.Fatal("a plain-http remote endpoint was accepted")
	}
	if strings.Contains(err.Error(), "SECRET123") ||
		strings.Contains(err.Error(), "attacker.invalid") {
		t.Errorf("the refusal echoed the endpoint: %v", err)
	}
}

// The day's report must cover the whole day's journal, not one process's view
// of it. The runner restarts on failure, so a mid-day restart would otherwise
// report only its own share and silently understate the day.
func TestTheDayReportCoversTheWholeJournalNotOneProcess(t *testing.T) {
	endpoint := os.Getenv("MITHRIL_AGENT_LIVE_SOLANA_RPC")
	if os.Getenv("MITHRIL_AGENT_LIVE_PRICE_TEST") != "1" || endpoint == "" {
		t.Skip("set MITHRIL_AGENT_LIVE_PRICE_TEST=1 and MITHRIL_AGENT_LIVE_SOLANA_RPC")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	primary, err := pricesource.NewPythPush(publicAccountReader(endpoint), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	secondary := pricesource.NewCoinbase(nil)
	policy := validShadowPolicy()
	policy.Trigger.PrimarySourceSHA256 = primary.IdentitySHA256()
	policy.Trigger.SecondarySourceSHA256 = secondary.IdentitySHA256()
	policy.Trigger.ThresholdMicros = 1
	policy.SettleSeconds = 1

	// Two separate runners over one day's journal, as a restart produces.
	at := time.Now().UTC()
	total := 0
	for run := range 2 {
		roll, err := newDailyJournal(root)
		if err != nil {
			t.Fatal(err)
		}
		shadowRunner := &shadowRun{
			policy: policy, primary: primary, secondary: secondary,
			quoter: liveStubQuoter{estimated: 21_525}, roll: roll,
		}
		if shadowRunner.runner, err = shadowRunner.newRunner(); err != nil {
			t.Fatal(err)
		}
		for tick := range 2 {
			observed, err := shadowRunner.runner.Step(t.Context(),
				at.Add(time.Duration(run*2+tick)*2*time.Second))
			if err != nil {
				t.Fatalf("run %d tick %d: %v", run, tick, err)
			}
			shadowRunner.lastPrice = observed.PriceMicros
			total++
		}
		var out bytes.Buffer
		if err := shadowRunner.finishDay(&out); err != nil {
			t.Fatal(err)
		}
		if err := roll.Close(); err != nil {
			t.Fatal(err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(root, "report-"+dayKey(at)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var report shadow.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Counts.Ticks != uint64(total) {
		t.Fatalf("the day report counted %d ticks; the journal holds %d from two runs",
			report.Counts.Ticks, total)
	}
}
