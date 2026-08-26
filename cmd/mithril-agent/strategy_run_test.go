package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureLegLoops replaces the real runners and records the arguments each leg
// was started with.
// The snapshot closure it returns reads under the same mutex the leg goroutines
// write under; reading the slice directly is a data race the detector will fail.
func captureLegLoops(t *testing.T, body func(name string, out io.Writer)) func() []string {
	t.Helper()
	var mu sync.Mutex
	var started []string
	previousSwap, previousSweep := runSwapLegLoop, runSweepLegLoop
	record := func(name string) func(context.Context, []string, io.Writer) error {
		return func(ctx context.Context, args []string, out io.Writer) error {
			mu.Lock()
			started = append(started, name+" "+strings.Join(args, " "))
			mu.Unlock()
			if body != nil {
				body(name, out)
			}
			<-ctx.Done()
			return nil
		}
	}
	runSwapLegLoop = record("swap")
	runSweepLegLoop = record("sweep")
	t.Cleanup(func() { runSwapLegLoop, runSweepLegLoop = previousSwap, previousSweep })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string{}, started...)
	}
}

// All three runners default to 127.0.0.1:9191, so a second one used to die with
// an error that never mentioned ports. One process starting every leg must hand
// them distinct addresses itself.
func TestStrategyRunGivesEveryLegItsOwnMetricsPort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := recordStrategy(strategyPaths{
		sell:  writeLeg(t, dir, "sell"),
		buy:   writeLeg(t, dir, "buy"),
		sweep: writeLeg(t, dir, "sweep"),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := captureLegLoops(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- strategyRun(ctx, []string{
			"--quote-socket", supervisedQuoteSocket,
			"--signer-socket-prefix", signerSocketPrefix,
			"--risk-socket-prefix", riskSocketPrefix,
		}, &output)
	}()

	deadline := time.After(3 * time.Second)
	for len(snapshot()) < 3 {
		select {
		case <-deadline:
			t.Fatalf("only %d legs started: %v", len(snapshot()), snapshot())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("cancelling the strategy reported a failure: %v", err)
	}
	started := snapshot()
	for _, port := range []string{"9191", "9192", "9193"} {
		var found bool
		for _, entry := range started {
			if strings.Contains(entry, "127.0.0.1:"+port) {
				found = true
			}
		}
		if !found {
			t.Errorf("no leg listened on %s: %v", port, started)
		}
	}
	// The sweep is a different loop; routing it to the swap loop would start a
	// runner that cannot read its profile.
	var sweepRouted bool
	for _, entry := range started {
		if strings.HasPrefix(entry, "sweep ") {
			sweepRouted = true
			if strings.Contains(entry, "--quote-socket") {
				t.Errorf("the sweep loop received a swap-only flag: %s", entry)
			}
		} else if !strings.Contains(entry, "--quote-socket "+supervisedQuoteSocket) {
			t.Errorf("a swap leg did not receive the protected quote socket: %s", entry)
		}
	}
	if !sweepRouted {
		t.Errorf("the sweep leg was not routed to the sweep loop: %v", started)
	}
	for _, leg := range []string{"sell", "buy", "sweep"} {
		signerSocket := "--signer-socket " + signerSocketPrefix + leg + ".sock"
		riskSocket := "--risk-socket " + riskSocketPrefix + leg + ".sock"
		var matches int
		for _, entry := range started {
			if strings.Contains(entry, signerSocket) && strings.Contains(entry, riskSocket) {
				matches++
			}
		}
		if matches != 1 {
			t.Errorf("%s authority sockets reached %d legs, want 1: %v", leg, matches, started)
		}
	}
}

// Three encoders on one writer interleave mid-line and produce output nothing
// can parse. Every line must stay valid JSON and say which leg produced it.
func TestStrategyRunLabelsEveryLineAndKeepsThemParseable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := recordStrategy(strategyPaths{
		sell: writeLeg(t, dir, "sell"), sweep: writeLeg(t, dir, "sweep"),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := captureLegLoops(t, func(_ string, out io.Writer) {
		encoder := json.NewEncoder(out)
		for i := 0; i < 20; i++ {
			_ = encoder.Encode(map[string]any{"decision": "stopped"})
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- strategyRun(ctx, nil, &output) }()
	deadline := time.After(3 * time.Second)
	for len(snapshot()) < 2 {
		select {
		case <-deadline:
			t.Fatal("legs did not start")
		case <-time.After(5 * time.Millisecond):
		}
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	legs := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if !strings.HasPrefix(line, "{") {
			continue // the human-readable header
		}
		var record struct {
			Leg   string          `json:"leg"`
			Cycle json.RawMessage `json:"cycle"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("interleaved output is not parseable: %q (%v)", line, err)
		}
		if record.Leg == "" || len(record.Cycle) == 0 {
			t.Fatalf("line lost its leg or its payload: %q", line)
		}
		legs[record.Leg]++
	}
	if legs["sell"] == 0 || legs["sweep"] == 0 {
		t.Fatalf("expected labelled lines from both legs, got %v", legs)
	}
}

// A leg that fails must surface, not be swallowed by a clean shutdown.
func TestStrategyRunReportsALegFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := recordStrategy(strategyPaths{sell: writeLeg(t, dir, "sell")}); err != nil {
		t.Fatal(err)
	}
	previous := runSwapLegLoop
	runSwapLegLoop = func(context.Context, []string, io.Writer) error {
		return errors.New("the sell leg could not start")
	}
	t.Cleanup(func() { runSwapLegLoop = previous })
	err := strategyRun(t.Context(), nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "could not start") {
		t.Fatalf("error = %v, want the leg's own failure", err)
	}
}

// Nothing configured must say so rather than starting a supervisor over no legs.
func TestStrategyRunSaysWhenThereIsNoStrategy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := strategyRun(t.Context(), nil, &bytes.Buffer{}); err == nil {
		t.Fatal("strategy run started with nothing configured")
	}
}

func TestSystemdRunnersRequireAllAuthoritySockets(t *testing.T) {
	t.Setenv("INVOCATION_ID", "")
	if err := requireSystemdAuthoritySockets("", "", ""); err != nil {
		t.Fatalf("manual foreground run was rejected: %v", err)
	}

	t.Setenv("INVOCATION_ID", "test-invocation")
	for _, sockets := range [][3]string{
		{}, {"/run/signer.sock", "", ""}, {"", "/run/risk.sock", ""},
		{"/run/signer.sock", "/run/risk.sock", ""},
	} {
		if err := requireSystemdAuthoritySockets(sockets[0], sockets[1], sockets[2]); err == nil {
			t.Fatalf("supervised run accepted sockets %q", sockets)
		}
	}
	if err := requireSystemdAuthoritySockets(
		"/run/signer.sock", "/run/risk.sock", "/run/submitter.sock",
	); err != nil {
		t.Fatalf("supervised run with all authority sockets was rejected: %v", err)
	}
}

// A whole-strategy runner must not quietly start only the legs it can still
// read. A missing leg can still have a live process holding its old profile in
// memory, so partial startup gives the operator a different strategy than the
// one they recorded.
func TestStrategyRunRefusesAnUnreadableRecordedLeg(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sell := writeLeg(t, dir, "sell")
	missing := writeLeg(t, t.TempDir(), "buy")
	if err := recordStrategy(strategyPaths{sell: sell, buy: missing}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(missing); err != nil {
		t.Fatal(err)
	}
	started := captureLegLoops(t, nil)
	err := strategyRun(t.Context(), nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("error = %v, want the missing leg named", err)
	}
	if len(started()) != 0 {
		t.Fatalf("a partial strategy was started: %v", started())
	}
}

// Ports must be pinned per leg, not numbered by position. A strategy without
// its buy leg — the ordinary state until the first sell creates the devUSDC
// account — would otherwise put the sweep on the second port, and
// `setup strategy --resume` would then silently move it to the third, leaving
// any pinned Prometheus scrape reading a different leg than it was aimed at.
func TestStrategyRunPinsPortsPerLegNotByPosition(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	// No buy leg: sell and sweep only.
	if err := recordStrategy(strategyPaths{
		sell: writeLeg(t, dir, "sell"), sweep: writeLeg(t, dir, "sweep"),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := captureLegLoops(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- strategyRun(ctx, nil, &output) }()
	deadline := time.After(3 * time.Second)
	for len(snapshot()) < 2 {
		select {
		case <-deadline:
			t.Fatal("legs did not start")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done

	for _, entry := range snapshot() {
		switch {
		case strings.HasPrefix(entry, "sweep "):
			// The sweep keeps 9193 whether or not a buy leg exists.
			if !strings.Contains(entry, "127.0.0.1:9193") {
				t.Errorf("the sweep moved off its own port: %s", entry)
			}
		case strings.HasPrefix(entry, "swap "):
			if !strings.Contains(entry, "127.0.0.1:9191") {
				t.Errorf("the sell leg moved off its own port: %s", entry)
			}
		}
	}
	// The printed table has to agree with what was actually started.
	if !strings.Contains(output.String(), "sweep  127.0.0.1:9193") {
		t.Errorf("the printed table disagrees with the runner:\n%s", output.String())
	}
}
