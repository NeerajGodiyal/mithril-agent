package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/marketadmission"
	"github.com/Overclock-Validator/mithril-agent/paperstatus"
)

type unixAddr struct{}

func (unixAddr) Network() string { return "unix" }
func (unixAddr) String() string  { return "/run/test-dashboard.sock" }

type blockingListener struct {
	accepted chan struct{}
	closed   chan struct{}
}

func (l *blockingListener) Accept() (net.Conn, error) {
	select {
	case <-l.accepted:
	default:
		close(l.accepted)
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *blockingListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (*blockingListener) Addr() net.Addr { return unixAddr{} }

func TestRunServesOnlyFromActivatedUnixSocket(t *testing.T) {
	listener := &blockingListener{accepted: make(chan struct{}), closed: make(chan struct{})}
	previous := activatedListener
	activatedListener = func() (net.Listener, error) { return listener, nil }
	t.Cleanup(func() { activatedListener = previous })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{
			"--paper-status-socket", "SOL/USDC=/run/mithril-agent-paper-status.sock",
			"--paper-status-socket", "JUP/USDC=/run/mithril-agent-paper-jup-status.sock",
			"--research-packet-path", "/var/lib/mithril-agent-dashboard/research.json",
			"--mithril-evidence-status-path", "/var/lib/mithril-agent-dashboard/mithril-evidence.json",
		}, &bytes.Buffer{})
	}()
	select {
	case <-listener.accepted:
	case <-time.After(time.Second):
		t.Fatal("HTTP server did not accept on the activated listener")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP server did not stop")
	}
}

func TestRecordMithrilEvidenceModeDoesNotOpenAListener(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	previous := activatedListener
	activatedListener = func() (net.Listener, error) {
		t.Fatal("record mode opened a listener")
		return nil, errors.New("unexpected listener")
	}
	t.Cleanup(func() { activatedListener = previous })
	path := filepath.Join(root, "mithril.json")
	if err := run(t.Context(), []string{
		"--record-mithril-evidence", path, "--mithril-evidence", "unavailable",
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("recorded status info=%v err=%v", info, err)
	}
	if err := run(t.Context(), []string{
		"--record-mithril-evidence", path, "--mithril-evidence", "invented",
	}, &bytes.Buffer{}); err == nil {
		t.Fatal("unknown evidence status was accepted")
	}
}

func TestRecordMarketAdmissionModeIsExclusiveAndDoesNotOpenAListener(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	credentials := filepath.Join(root, "credentials")
	if err := os.Mkdir(credentials, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	through := now.Truncate(time.Minute)
	for name, market := range map[string]string{
		"market-wif-status":  marketadmission.MarketWIFUSDC,
		"market-jto-status":  marketadmission.MarketJTOUSDC,
		"market-pyth-status": marketadmission.MarketPYTHUSDC,
	} {
		status := marketadmission.DashboardStatus{
			Version: marketadmission.Version, Kind: marketadmission.DashboardStatusKind,
			Market: market, UpdatedAt: now, WindowHours: marketadmission.DashboardStatusWindowHours,
			Diagnostic: marketadmission.Diagnostic{
				Version: marketadmission.Version, Market: market,
				From: through.Add(-2 * time.Hour), Through: through,
				DiagnosticOnly: true, ExpectedBuckets: 120,
				FailureCounts: map[string]uint64{"missing_bucket": 120},
			},
		}
		raw, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(credentials, name), append(raw, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CREDENTIALS_DIRECTORY", credentials)
	previous := activatedListener
	activatedListener = func() (net.Listener, error) {
		t.Fatal("record mode opened a listener")
		return nil, errors.New("unexpected listener")
	}
	t.Cleanup(func() { activatedListener = previous })
	path := filepath.Join(root, "market-admission.json")
	if err := run(t.Context(), []string{"--record-market-admission", path}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("recorded market status info=%v err=%v", info, err)
	}
	if err := run(t.Context(), []string{
		"--record-market-admission", path,
		"--paper-status-socket", "SOL/USDC=/run/sol.sock",
	}, io.Discard); err == nil {
		t.Fatal("market record mode accepted server flags")
	}
}

func TestSocketPathsRejectUnsafeOrAmbiguousValues(t *testing.T) {
	var paths socketPaths
	if err := paths.Set("SOL/USDC=/run/sol.sock"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"/run/missing-label.sock", "bad label=/run/bad.sock", "JUP/USDC=relative.sock",
		"SOL/USDC=/run/other.sock", "JUP/USDC=/run/sol.sock",
	} {
		if err := paths.Set(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	if got := paths.String(); got != "SOL/USDC=/run/sol.sock" {
		t.Fatalf("paths = %q", got)
	}
}

func TestRunRequiresSourcesAndActivatedSocket(t *testing.T) {
	if err := run(t.Context(), nil, &bytes.Buffer{}); err == nil {
		t.Fatal("missing source accepted")
	}
	previous := activatedListener
	activatedListener = func() (net.Listener, error) { return nil, errors.New("not activated") }
	t.Cleanup(func() { activatedListener = previous })
	if err := run(t.Context(), []string{
		"--paper-status-socket", "SOL/USDC=/run/sol.sock",
	}, &bytes.Buffer{}); err == nil || err.Error() != "not activated" {
		t.Fatalf("activation error = %v", err)
	}
	if err := run(t.Context(), []string{
		"--paper-status-socket", "SOL/USDC=/run/sol.sock",
		"--optional-paper-status-socket", "SOL-PERP=/run/sol-perp.sock",
	}, &bytes.Buffer{}); err == nil || err.Error() != "not activated" {
		t.Fatalf("optional activation error = %v", err)
	}
	if err := run(t.Context(), []string{
		"--paper-status-socket", "SOL/USDC=/run/sol.sock",
		"--optional-paper-status-socket", "SOL/USDC=/run/other.sock",
	}, &bytes.Buffer{}); err == nil {
		t.Fatal("optional source duplicated a required label")
	}
}

func TestRenderInstructionModeDoesNotOpenAListener(t *testing.T) {
	previous := activatedListener
	activatedListener = func() (net.Listener, error) {
		t.Fatal("render mode opened a listener")
		return nil, errors.New("unexpected listener")
	}
	t.Cleanup(func() { activatedListener = previous })
	var output bytes.Buffer
	if err := run(t.Context(), []string{"--render-instruction", "/tmp/missing-paper-instruction.json"}, &output); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("missing instruction output = %q", output.String())
	}
	if err := run(t.Context(), []string{
		"--render-instruction", "/tmp/missing-paper-instruction.json",
		"--paper-status-socket", "SOL/USDC=/run/sol.sock",
	}, &output); err == nil {
		t.Fatal("mixed render and server flags accepted")
	}
	if err := run(t.Context(), []string{
		"--render-instruction", "/tmp/missing-paper-instruction.json",
		"--research-packet-path", "/tmp/research.json",
	}, &output); err == nil {
		t.Fatal("render mode accepted a research projection")
	}
}

func TestExportInstructionModeDoesNotOpenAListener(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "dashboard.json")
	content := []byte("{\"version\":3,\"updated_at\":\"2026-09-01T01:00:00Z\",\"market\":\"all\",\"preference\":\"balanced\",\"cadence_seconds\":60,\"max_drawdown_bps\":300}\n")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	previous := activatedListener
	activatedListener = func() (net.Listener, error) {
		t.Fatal("copy mode opened a listener")
		return nil, errors.New("unexpected listener")
	}
	t.Cleanup(func() { activatedListener = previous })
	var output bytes.Buffer
	if err := run(t.Context(), []string{"--export-instruction", source}, &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), content) {
		t.Fatalf("exported instruction = %q", output.Bytes())
	}
	if err := run(t.Context(), []string{
		"--export-instruction", source, "--research-packet-path", filepath.Join(root, "research.json"),
	}, io.Discard); err == nil {
		t.Fatal("export mode accepted a server flag")
	}
}

func TestRenderPerpsResearchModeIsExclusiveAndDoesNotOpenAListener(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	var args []string
	for index, market := range []string{"SOL-PERP", "BTC-PERP", "ETH-PERP"} {
		path := filepath.Join(root, strings.ToLower(strings.TrimSuffix(market, "-PERP"))+".json")
		snapshot := paperstatus.Snapshot{
			Version: paperstatus.Version, ObservedAt: now, Events: []paperstatus.Event{},
			Current: "PAPER · checkpoint complete",
			Summary: &paperstatus.CurrentSummary{
				Market: market, Instrument: "perpetual", RiskProfile: "balanced",
				PositionDirection: "flat", LeverageBPS: 20_000, FundingTracked: true,
				ValueUnit: "USD", Day: "2026-09-03", TickSeconds: 15,
				OpeningEquityMicros: 100_000_000, EquityMicros: 100_000_000,
				HoldBenchmarkMicros: 100_000_000, AccountingTracked: true,
				Checks: 421, State: "watching", Strategy: "fixed",
				QualificationTracked: true, QualificationOutcome: "no_training_candidate",
				QualificationSHA256: strings.Repeat(string(rune('a'+index)), 64),
				QualificationTapes:  4, QualificationFrames: 421,
				QualificationMinimumFrames: 96, QualificationTrainingFrames: 390,
				QualificationHoldoutFrames: 31,
			},
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--render-perps-research", market+"="+path)
	}
	previous := activatedListener
	activatedListener = func() (net.Listener, error) {
		t.Fatal("perps research render opened a listener")
		return nil, errors.New("unexpected listener")
	}
	t.Cleanup(func() { activatedListener = previous })
	var output bytes.Buffer
	if err := run(t.Context(), args, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"paper_only":true`) ||
		!strings.Contains(output.String(), `"advisory_only":true`) ||
		!strings.Contains(output.String(), `"authorized":false`) ||
		!strings.Contains(output.String(), `"promotable":false`) ||
		!strings.Contains(output.String(), `"content_sha256":"`) {
		t.Fatalf("perps research output = %q", output.String())
	}
	if err := run(t.Context(), append(append([]string(nil), args...),
		"--research-packet-path", filepath.Join(root, "research.json")), io.Discard); err == nil {
		t.Fatal("perps research render accepted a server flag")
	}
	if err := run(t.Context(), args[:2], io.Discard); err == nil {
		t.Fatal("perps research render accepted fewer than three markets")
	}
}
