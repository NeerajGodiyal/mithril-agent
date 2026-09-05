package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

type cadencePriceSource struct {
	candidatePriceSource
	before func()
}

func (source cadencePriceSource) Latest(ctx context.Context, feed string) (pricetrigger.Sample, error) {
	if source.before != nil {
		source.before()
	}
	current := source.candidatePriceSource
	current.at = time.Now().UTC()
	return current.Latest(ctx, feed)
}

type cadenceTickWriter struct {
	ticks []shadow.Tick
	delay time.Duration
}

func (writer *cadenceTickWriter) Write(data []byte) (int, error) {
	var tick shadow.Tick
	if json.Unmarshal(data, &tick) == nil && tick.Event != "" {
		writer.ticks = append(writer.ticks, tick)
		// This work occurs after the event timestamp, like quotes/status writes.
		time.Sleep(writer.delay)
		writer.delay = 0
	}
	return len(data), nil
}

func cadenceRun(t *testing.T, before func()) *shadowRun {
	t.Helper()
	root := privateTestDirectory(t)
	policy := candidateTestPolicy()
	policy.TickSeconds = 15
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	roll, err := newDailyJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := roll.openFor(time.Now().UTC()); err != nil {
		roll.Close()
		t.Fatal(err)
	}
	alerts, err := paperstatus.OpenWriter(filepath.Join(root, "alerts.json"))
	if err != nil {
		roll.Close()
		t.Fatal(err)
	}
	run := &shadowRun{policy: policy, policySHA256: fingerprint, roll: roll, alerts: alerts,
		primary:   cadencePriceSource{candidatePriceSource: candidatePriceSource{identity: policy.Trigger.PrimarySourceSHA256}, before: before},
		secondary: cadencePriceSource{candidatePriceSource: candidatePriceSource{identity: policy.Trigger.SecondarySourceSHA256}},
		quoter:    liveStubQuoter{estimated: 21_525},
	}
	if run.runner, err = run.newRunner(); err != nil {
		roll.Close()
		t.Fatal(err)
	}
	return run
}

func TestShadowDriveUTCCadenceAfterCompletedWork(t *testing.T) {
	for _, test := range []struct {
		name                   string
		delays, starts, events []time.Duration
		work                   time.Duration
	}{
		// A recurring ticker started at 14s instead emits at 16s, 29.5s,
		// and 46s: two observations in bucket 1 and none in bucket 2.
		{"jitter", []time.Duration{2 * time.Second, time.Second / 2, 2 * time.Second},
			[]time.Duration{14 * time.Second, 30 * time.Second, 45 * time.Second},
			[]time.Duration{16 * time.Second, 30*time.Second + time.Second/2, 47 * time.Second}, 0},
		{"slow acquisition", []time.Duration{20 * time.Second, time.Second, time.Second},
			[]time.Duration{14 * time.Second, 45 * time.Second, 60 * time.Second},
			[]time.Duration{34 * time.Second, 46 * time.Second, 61 * time.Second}, 0},
		{"post timestamp work", []time.Duration{2 * time.Second, time.Second / 2, 2 * time.Second},
			[]time.Duration{14 * time.Second, 45 * time.Second, 60 * time.Second},
			[]time.Duration{16 * time.Second, 45*time.Second + time.Second/2, 62 * time.Second}, 21 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				day := time.Now().UTC()
				time.Sleep(14 * time.Second)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var starts []time.Duration
				run := cadenceRun(t, func() {
					index := len(starts)
					if index >= len(test.delays) {
						t.Fatal("unexpected catch-up acquisition")
					}
					starts = append(starts, time.Now().Sub(day))
					time.Sleep(test.delays[index])
					if index == len(test.delays)-1 {
						cancel()
					}
				})
				defer run.roll.Close()
				output := &cadenceTickWriter{delay: test.work}
				if err := run.drive(ctx, false, output); err != nil {
					t.Fatal(err)
				}
				if len(starts) != len(test.starts) || len(output.ticks) != len(test.events) {
					t.Fatalf("polls=%v events=%d", starts, len(output.ticks))
				}
				for i := range starts {
					if starts[i] != test.starts[i] || output.ticks[i].At.Sub(day) != test.events[i] {
						t.Fatalf("poll %d start=%v event=%v, want %v/%v", i, starts[i], output.ticks[i].At.Sub(day), test.starts[i], test.events[i])
					}
				}
			})
		})
	}
}

func TestShadowDriveCadenceOnceAndCancellation(t *testing.T) {
	for _, once := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			time.Sleep(14 * time.Second)
			start := time.Now()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			run := cadenceRun(t, func() { calls++; time.Sleep(2 * time.Second) })
			defer run.roll.Close()
			if !once {
				go func() { time.Sleep(7 * time.Second); cancel() }()
			}
			if err := run.drive(ctx, once, io.Discard); err != nil {
				t.Fatal(err)
			}
			want := 7 * time.Second
			if once {
				want = 2 * time.Second
			}
			if calls != 1 || time.Since(start) != want {
				t.Fatalf("once=%v calls=%d elapsed=%v, want 1/%v", once, calls, time.Since(start), want)
			}
		})
	}
}

func TestShadowDriveCadenceDiscardsMidnightObservation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		day := time.Now().UTC()
		time.Sleep(24*time.Hour - time.Second)
		calls := 0
		run := cadenceRun(t, func() {
			calls++
			if calls == 1 {
				time.Sleep(2 * time.Second)
			} else {
				time.Sleep(time.Second / 2)
			}
		})
		defer run.roll.Close()
		output := &cadenceTickWriter{}
		if err := run.drive(context.Background(), true, output); err != nil {
			t.Fatal(err)
		}
		if calls != 2 || len(output.ticks) != 1 || !output.ticks[0].At.Equal(day.Add(24*time.Hour+time.Second+time.Second/2)) {
			t.Fatalf("midnight calls=%d ticks=%+v", calls, output.ticks)
		}
		if run.runner.Counts().Ticks != 1 {
			t.Fatal("discarded old-day read mutated new runner")
		}
	})
}

func TestNextShadowPollAtUsesDayAnchoredStrictBoundaries(t *testing.T) {
	day := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct{ at, interval, want time.Duration }{
		{14 * time.Second, 15 * time.Second, 15 * time.Second},
		{15 * time.Second, 15 * time.Second, 30 * time.Second},
		{31 * time.Second, 15 * time.Second, 45 * time.Second},
		{time.Second, 7 * time.Second, 7 * time.Second},
		{24*time.Hour - time.Second, 7 * time.Second, 24 * time.Hour},
	} {
		if got := nextShadowPollAt(day.Add(test.at), test.interval); !got.Equal(day.Add(test.want)) {
			t.Fatalf("at=%v interval=%v got=%v want=%v", test.at, test.interval, got, day.Add(test.want))
		}
	}
}

func TestShadowRunSystemdReadinessNotification(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "mithril-shadow-notify-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "notify.sock")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("NOTIFY_SOCKET", path)
	if err := notifyShadowRunReady(); err != nil {
		t.Fatal(err)
	}
	if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	message := make([]byte, 32)
	count, _, err := listener.ReadFromUnix(message)
	if err != nil || string(message[:count]) != "READY=1" {
		t.Fatalf("notification = %q, %v", message[:count], err)
	}
}

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
		Version: shadow.Version, Cluster: shadow.Mainnet, Market: shadow.MarketSOLUSDC,
		QuoteRoute: shadow.QuoteRoute{
			Provider:   shadow.QuoteJupiter,
			InputMint:  "So11111111111111111111111111111111111111112",
			OutputMint: "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
		},
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
		QuotePeg: &pricetrigger.BandPolicy{
			Version: pricetrigger.Version, Feed: pricetrigger.FeedUSDCUSD,
			MinimumMicros: pricetrigger.USDCBandMinimumMicros,
			MaximumMicros: pricetrigger.USDCBandMaximumMicros,
			MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90,
			MaxDeviationBPS: 100, MaxConfidenceBPS: 100,
			PrimarySourceSHA256:   pricesource.PythPushUSDCIdentitySHA256(),
			SecondarySourceSHA256: pricesource.KrakenIdentitySHA256(),
		},
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
	legacy := validShadowPolicy()
	legacy.Trigger.SecondarySourceSHA256 = pricesource.CoinbaseIdentitySHA256()
	if legacy.ReturnTrigger != nil {
		legacy.ReturnTrigger.SecondarySourceSHA256 = pricesource.CoinbaseIdentitySHA256()
	}
	legacyPath := writeShadowPolicy(t, legacy)
	if _, err := loadShadowPolicy(legacyPath); err != nil {
		t.Fatalf("legacy policy could not be loaded for historical audit: %v", err)
	}
	if _, err := loadActiveShadowPolicy(legacyPath); err == nil ||
		!strings.Contains(err.Error(), "Coinbase") || !strings.Contains(err.Error(), "regenerate") {
		t.Fatalf("legacy Coinbase active-policy refusal = %v", err)
	}
}

// The endpoint must come from the environment, never a flag, and must never
// appear in an error a user could paste into a bug report.
func TestShadowEndpointIsNeverEchoed(t *testing.T) {
	const secret = "https://rpc.invalid/path?api-key=super-secret"
	t.Setenv(shadowEndpointEnvironment, "http://insecure.invalid")
	_, err := openShadowRun(t.Context(), validShadowPolicy(), shadowRunOptions{directory: "/tmp/x"})
	if err == nil {
		t.Fatal("a plain http endpoint was accepted")
	}
	if strings.Contains(err.Error(), "insecure.invalid") {
		t.Errorf("the error echoed the endpoint: %v", err)
	}

	t.Setenv(shadowEndpointEnvironment, secret)
	_, err = openShadowRun(t.Context(), validShadowPolicy(), shadowRunOptions{directory: "/tmp/x"})
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
	policy := validShadowPolicy()
	policy.Cluster = shadow.Devnet
	policy.QuotePeg = nil
	policy.QuoteRoute = shadow.QuoteRoute{
		Provider: shadow.QuoteOrca, Pool: "11111111111111111111111111111111",
		InputMint:  "So11111111111111111111111111111111111111112",
		OutputMint: "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
	}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := newShadowQuoter(policy, shadowRunOptions{}); err == nil {
		t.Fatal("an Orca quoter was built with no executable adapter")
	}
	if _, err := newShadowQuoter(policy, shadowRunOptions{
		nodeCommand: "/usr/bin/node", quoteScript: "/tmp/quote.mjs",
		pool: "So11111111111111111111111111111111111111112",
	}); err == nil || !strings.Contains(err.Error(), "does not match the policy") {
		t.Fatal("an Orca route override could replace the policy-bound pool")
	}
}

func TestShadowRunKeepsJupiterOnTheReadOnlyPath(t *testing.T) {
	const (
		inputMint  = "So11111111111111111111111111111111111111112"
		outputMint = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	)
	policy := validShadowPolicy()
	t.Setenv(jupiterAPIKeyEnvironment, "")
	quoter, err := newShadowQuoter(policy, shadowRunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := quoter.(*jupiterShadowQuoter); !ok {
		t.Fatalf("keyless Jupiter provider created %T", quoter)
	}
	t.Setenv(jupiterAPIKeyEnvironment, "test-key")
	quoter, err = newShadowQuoter(policy, shadowRunOptions{
		quoteSource: "jupiter", inputMint: inputMint, outputMint: outputMint,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := quoter.(*jupiterShadowQuoter); !ok {
		t.Fatalf("Jupiter provider created %T", quoter)
	}
	if _, err := newShadowQuoter(policy, shadowRunOptions{
		quoteSource: "jupiter", inputMint: inputMint, outputMint: outputMint,
		quoteScript: "/tmp/quote.mjs",
	}); err == nil {
		t.Fatal("Jupiter shadow mode accepted an executable quote adapter")
	}
	if _, err := newShadowQuoter(policy, shadowRunOptions{
		quoteSource: "jupiter", inputMint: outputMint, outputMint: inputMint,
	}); err == nil || !strings.Contains(err.Error(), "does not match the policy") {
		t.Fatal("Jupiter shadow mode accepted a route different from the policy")
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
	if !strings.Contains(text, "Nothing is") || !strings.Contains(text, "no wallet signing key is loaded") {
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

func TestDailyJournalRefusesRecordsFromAnotherUTCDay(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	current := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(root, "shadow-"+dayKey(current)+".jsonl")
	store, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(
		current.Add(-24*time.Hour), shadow.EventOpened, "", shadow.Opening{},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	roll, err := newDailyJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer roll.Close()
	if err := roll.openFor(current); err == nil ||
		!strings.Contains(err.Error(), "different UTC day") {
		t.Fatalf("active journal accepted another day's records: %v", err)
	}
}

func TestRolloverDiscardsPreparedObservationBeforeRunnerMutation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	oldAt := time.Date(2026, 8, 30, 23, 59, 59, 0, time.UTC)
	newAt := oldAt.Add(2 * time.Second)
	policy := candidateTestPolicy()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	primary := candidatePriceSource{identity: policy.Trigger.PrimarySourceSHA256, at: newAt}
	secondary := candidatePriceSource{identity: policy.Trigger.SecondarySourceSHA256, at: newAt}
	roll, err := newDailyJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer roll.Close()
	if err := roll.openFor(oldAt); err != nil {
		t.Fatal(err)
	}
	alerts, err := paperstatus.OpenWriter(filepath.Join(root, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	run := &shadowRun{
		policy: policy, policySHA256: fingerprint, primary: primary, secondary: secondary,
		quoter: liveStubQuoter{estimated: 21_525}, roll: roll, alerts: alerts,
	}
	if run.runner, err = run.newRunner(); err != nil {
		t.Fatal(err)
	}
	oldRunner := run.runner
	_ = oldRunner.Observe(t.Context())
	if oldRunner.Counts() != (shadow.Counts{}) {
		t.Fatalf("pre-roll observation mutated counts: %+v", oldRunner.Counts())
	}
	rolled, err := run.rollDay(newAt, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !rolled || run.runner == oldRunner || oldRunner.Counts() != (shadow.Counts{}) {
		t.Fatalf("rollover reused or mutated old runner: rolled=%t counts=%+v", rolled, oldRunner.Counts())
	}
	if run.roll.Day() != dayKey(newAt) {
		t.Fatalf("rollover kept the closed journal day %q", run.roll.Day())
	}
	if _, err := os.Stat(filepath.Join(root, "shadow-2026-08-31.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("old observation opened the new-day journal: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || len(snapshot.Events) != 2 ||
		snapshot.Events[0].Kind != paperstatus.KindPeriodClosed ||
		snapshot.Events[1].Kind != paperstatus.KindStrategyActive ||
		!strings.Contains(snapshot.Events[0].Message, "No usable market price") {
		t.Fatalf("rollover alert lifecycle=%+v err=%v", snapshot, err)
	}
}

func TestDriveTreatsProvisionalRolloverAsSuccessfulCompletion(t *testing.T) {
	artifactPath, journalPath, evidenceNow := writeReadyProvisionalEvidence(t)
	artifact, err := loadProvisionalMarketAdmission(artifactPath, journalPath, evidenceNow)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := buildAdaptiveProvisionalPolicy(
		artifact, artifact.Candidate.QuoteNotionalUSDC, 80_000_000, 3_000_000,
		artifact.Candidate.QuoteSlippageBPS, 100_000, artifact.Observe, 60,
	)
	if err != nil {
		t.Fatal(err)
	}
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
	previousDay := time.Now().UTC().Truncate(24 * time.Hour).Add(-time.Hour)
	if err := roll.openFor(previousDay); err != nil {
		t.Fatal(err)
	}
	alerts, err := paperstatus.OpenWriter(filepath.Join(root, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	reader := func(identity string) candidatePriceSource {
		return candidatePriceSource{identity: identity, at: previousDay}
	}
	run := &shadowRun{
		policy: policy, roll: roll, alerts: alerts,
		primary:         reader(policy.Trigger.PrimarySourceSHA256),
		secondary:       reader(policy.Trigger.SecondarySourceSHA256),
		quotePrimary:    reader(policy.QuotePeg.PrimarySourceSHA256),
		quoteSecondary:  reader(policy.QuotePeg.SecondarySourceSHA256),
		nativePrimary:   reader(policy.NativeFeePrice.PrimarySourceSHA256),
		nativeSecondary: reader(policy.NativeFeePrice.SecondarySourceSHA256),
		quoter:          liveStubQuoter{estimated: 21_525},
	}
	if run.runner, err = run.newRunner(); err != nil {
		t.Fatal(err)
	}
	if err := run.drive(t.Context(), false, io.Discard); err != nil {
		t.Fatalf("completed provisional drive returned an error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || len(snapshot.Events) != 1 ||
		snapshot.Events[0].Kind != paperstatus.KindExperimentDone {
		t.Fatalf("provisional completion lifecycle = %+v, %v", snapshot, err)
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

func TestShadowJournalHeaderBindsThePolicyAndRefusesTheOldFormat(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := validShadowPolicy()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	roll, err := newDailyJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer roll.Close()
	now := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	if err := roll.Record(now, shadow.EventOpened, shadow.Opening{
		Version: shadow.JournalVersion, PolicySHA256: fingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := shadowTicksFrom(roll.Records(), policy, true); err != nil {
		t.Fatalf("matching policy was rejected: %v", err)
	}
	changed := policy
	changed.Trigger.ThresholdMicros++
	if _, err := shadowTicksFrom(roll.Records(), changed, true); err == nil ||
		!strings.Contains(err.Error(), "different policy") {
		t.Fatalf("changed policy was not refused clearly: %v", err)
	}

	oldRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(oldRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	old, err := newDailyJournal(oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err := old.Record(now, shadow.EventOpened, shadow.Ledger{}); err != nil {
		t.Fatal(err)
	}
	if _, err := shadowTicksFrom(old.Records(), policy, true); err == nil ||
		!strings.Contains(err.Error(), "unsupported opening format") {
		t.Fatalf("old opening format was not refused clearly: %v", err)
	}
}

func TestShadowJournalRecordTimeBindsTheTick(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := validShadowPolicy()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	roll, err := newDailyJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer roll.Close()
	at := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	if err := roll.Record(at, shadow.EventOpened, shadow.Opening{
		Version: shadow.JournalVersion, PolicySHA256: fingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	if err := roll.Record(at.Add(time.Second), shadow.EventWaiting, shadow.Tick{
		At: at, Event: shadow.EventWaiting,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := shadowTicksFrom(roll.Records(), policy, false); err == nil ||
		!strings.Contains(err.Error(), "timestamp does not match") {
		t.Fatalf("journal/tick time mismatch was accepted: %v", err)
	}
}

func TestShadowReportIsAtomicallyReplacedAndUsesTheActualPartialPeriod(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := validShadowPolicy()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	roll, err := newDailyJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer roll.Close()
	now := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	if err := roll.Record(now, shadow.EventOpened, shadow.Opening{
		Version: shadow.JournalVersion, PolicySHA256: fingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	const price = uint64(190_000_000)
	ledger, err := shadow.NewLedger(policy, price)
	if err != nil {
		t.Fatal(err)
	}
	equity, err := ledger.EquityMicros(price)
	if err != nil {
		t.Fatal(err)
	}
	if err := roll.Record(now, shadow.EventWaiting, shadow.Tick{
		At: now, Event: shadow.EventWaiting, PriceMicros: price,
		QuoteLowerMicros: policy.QuotePeg.MinimumMicros,
		QuoteUpperMicros: policy.QuotePeg.MaximumMicros,
		EquityMicros:     equity,
	}); err != nil {
		t.Fatal(err)
	}
	run := shadowRun{policy: policy, roll: roll, lastPrice: price}
	firstEnd := now.Add(time.Hour)
	if err := roll.Record(firstEnd, shadow.EventClosed, shadow.Tick{
		At: firstEnd, Event: shadow.EventClosed, PeriodClose: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := run.finishDayAt(io.Discard, firstEnd); err != nil {
		t.Fatal(err)
	}
	secondEnd := firstEnd.Add(time.Hour)
	if err := roll.Record(secondEnd, shadow.EventClosed, shadow.Tick{
		At: secondEnd, Event: shadow.EventClosed, PeriodClose: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := run.finishDayAt(io.Discard, secondEnd); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "report-2026-03-02.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report shadow.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if !report.To.Equal(secondEnd) || report.Counts.Ticks != 1 {
		t.Fatalf("replaced report has to=%s ticks=%d", report.To, report.Counts.Ticks)
	}
	if err := roll.Close(); err != nil {
		t.Fatal(err)
	}
	policyPath := writeShadowPolicy(t, policy)
	var output bytes.Buffer
	if err := runShadowReport([]string{
		"--policy", policyPath, "--dir", root,
	}, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "DISAGREES") ||
		!strings.Contains(output.String(), "matches the journal exactly") {
		t.Fatalf("partial report did not recompute exactly:\n%s", output.String())
	}
	if err := os.Remove(filepath.Join(root, "report-2026-03-02.json")); err != nil {
		t.Fatal(err)
	}
	alertPath := filepath.Join(root, "alerts.json")
	run.alerts, err = paperstatus.OpenWriter(alertPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.reconcileMissingShadowReports(); err != nil {
		t.Fatal(err)
	}
	recoveredRaw, err := os.ReadFile(filepath.Join(root, "report-2026-03-02.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recovered shadow.Report
	if err := json.Unmarshal(recoveredRaw, &recovered); err != nil || !recovered.To.Equal(secondEnd) {
		t.Fatalf("recovered report = %+v, %v", recovered, err)
	}
	current, err := newDailyJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if err := current.openFor(time.Date(2026, 3, 2, 13, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	run.roll = current
	if err := run.reconcileStoredShadowReports(); err != nil {
		t.Fatal(err)
	}
	if err := current.openFor(time.Date(2026, 3, 3, 1, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	newer := time.Date(2026, 3, 3, 0, 30, 0, 0, time.UTC)
	if err := run.alerts.UpdateCurrent(newer, "PAPER · Watching current market"); err != nil {
		t.Fatal(err)
	}
	if err := run.reconcileStoredShadowReports(); err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	alertRaw, err := os.ReadFile(alertPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(alertRaw, &snapshot); err != nil || len(snapshot.Events) != 1 ||
		snapshot.Events[0].Kind != paperstatus.KindExperimentDone ||
		!snapshot.ObservedAt.Equal(newer) || snapshot.Current != "PAPER · Watching current market" {
		t.Fatalf("reconciled report alert = %+v, %v", snapshot, err)
	}
}

func TestClosedUnobservablePeriodReconcilesAfterRestart(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := validShadowPolicy()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	dayEnd := start.Add(24 * time.Hour)
	closed := dayEnd.Add(-time.Nanosecond)
	roll, err := newDailyJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := roll.Record(start, shadow.EventOpened, shadow.Opening{
		Version: shadow.JournalVersion, PolicySHA256: fingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	if err := roll.Record(start, shadow.EventUnobservable, shadow.Tick{
		At: start, Event: shadow.EventUnobservable,
	}); err != nil {
		t.Fatal(err)
	}
	if err := roll.Record(closed, shadow.EventClosed, shadow.Tick{
		At: closed, Event: shadow.EventClosed, PeriodClose: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := roll.Close(); err != nil {
		t.Fatal(err)
	}

	alerts, err := paperstatus.OpenWriter(filepath.Join(root, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := newDailyJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	run := shadowRun{
		policy: policy, policySHA256: fingerprint, roll: reopened, alerts: alerts,
	}
	if err := run.reconcileMissingShadowReports(); err != nil {
		t.Fatal(err)
	}
	if err := run.reconcileMissingShadowReports(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "report-2026-08-31.json")); !os.IsNotExist(err) {
		t.Fatalf("unobservable period report exists or could not be checked: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 1 ||
		snapshot.Events[0].Kind != paperstatus.KindPeriodClosed ||
		!snapshot.Events[0].At.Equal(dayEnd) ||
		!strings.Contains(snapshot.Events[0].Message, "DAY FINISHED") ||
		!strings.Contains(snapshot.Events[0].Message, "No usable market price") {
		t.Fatalf("reconciled unavailable period alert = %+v", snapshot.Events)
	}
}

func TestStoredShadowReportComparisonIsStrictAndFailsClosed(t *testing.T) {
	root := t.TempDir()
	day := "2026-08-12"
	report := shadow.Report{Version: shadow.Version, Cluster: shadow.Devnet}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "report-"+day+".json")
	withUnknown := append(raw[:len(raw)-1], []byte(`,"unexpected":true}`)...)
	if err := os.WriteFile(path, withUnknown, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := compareStoredShadowReport(root, day, report, io.Discard); err == nil {
		t.Fatal("stored report with an unknown field was accepted as an exact match")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := compareStoredShadowReport(root, day, report, io.Discard); err != nil {
		t.Fatalf("an absent optional stored report was rejected: %v", err)
	}
}

// liveStubQuoter stands in for the Orca adapter so the live test exercises the
// real price path without needing a Node runtime. It quotes off the observed
// price, which is enough to prove the pipeline runs; it is not used to produce
// any reported result.
type liveStubQuoter struct{ estimated uint64 }

func (q liveStubQuoter) Quote(
	_ context.Context, _ string, _ bool, amount uint64, _ uint16,
) (shadow.Quote, error) {
	return shadow.Quote{
		InputAmount: amount, EstimatedOutput: q.estimated,
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
	secondary := pricesource.NewKrakenSOL(nil)
	quotePrimary, err := pricesource.NewPythPushUSDC(publicAccountReader(endpoint), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	quoteSecondary := pricesource.NewKraken(nil)

	policy := validShadowPolicy()
	policy.Trigger.PrimarySourceSHA256 = primary.IdentitySHA256()
	policy.Trigger.SecondarySourceSHA256 = secondary.IdentitySHA256()
	policy.QuotePeg.PrimarySourceSHA256 = quotePrimary.IdentitySHA256()
	policy.QuotePeg.SecondarySourceSHA256 = quoteSecondary.IdentitySHA256()
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
		quotePrimary: quotePrimary, quoteSecondary: quoteSecondary,
		quoter: liveStubQuoter{estimated: 21_525}, roll: roll,
	}
	if run.runner, err = run.newRunner(); err != nil {
		t.Fatal(err)
	}
	observations := 0
	filled := false
	for attempt := range 12 {
		observed, err := run.runner.Step(t.Context(), time.Now().UTC().Add(
			time.Duration(attempt)*2*time.Second))
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if observed.Event == shadow.EventUnobservable {
			t.Logf("attempt %d was safely unobservable", attempt)
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if observed.PriceMicros == 0 {
			t.Fatalf("attempt %d observed no price", attempt)
		}
		observations++
		filled = filled || observed.Fill != nil
		run.lastPrice = observed.PriceMicros
		t.Logf("attempt %d: %s at $%d.%06d", attempt, observed.Event,
			observed.PriceMicros/1_000_000, observed.PriceMicros%1_000_000)
		if observations >= 3 && filled {
			break
		}
	}
	if observations < 3 || !filled {
		t.Fatalf("live shadow path produced %d observations, filled=%t", observations, filled)
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
		"http://[::1]:8898",
	} {
		if err := validateShadowEndpoint(allowed); err != nil {
			t.Errorf("%s was refused: %v", allowed, err)
		}
	}
	for _, refused := range []string{
		"", "http://example.invalid", "http://192.0.2.5:8899",
		"ftp://127.0.0.1", "https://user:pass@rpc.invalid", "not a url",
		"http://localhost:8898", "http://127.0.0.1.evil.invalid",
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
	secondary := pricesource.NewKrakenSOL(nil)
	quotePrimary, err := pricesource.NewPythPushUSDC(publicAccountReader(endpoint), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	quoteSecondary := pricesource.NewKraken(nil)
	policy := validShadowPolicy()
	policy.Trigger.PrimarySourceSHA256 = primary.IdentitySHA256()
	policy.Trigger.SecondarySourceSHA256 = secondary.IdentitySHA256()
	policy.QuotePeg.PrimarySourceSHA256 = quotePrimary.IdentitySHA256()
	policy.QuotePeg.SecondarySourceSHA256 = quoteSecondary.IdentitySHA256()
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
			quotePrimary: quotePrimary, quoteSecondary: quoteSecondary,
			quoter: liveStubQuoter{estimated: 21_525}, roll: roll,
		}
		if run == 0 {
			shadowRunner.runner, err = shadowRunner.newRunner()
		} else {
			var ticks []shadow.Tick
			ticks, err = shadowTicksFrom(roll.Records(), policy, true)
			if err == nil {
				shadowRunner.runner, err = shadow.ResumeRunner(
					policy, primary, secondary, shadowRunner.quoter, roll, ticks,
					quotePrimary, quoteSecondary,
				)
			}
		}
		if err != nil {
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
