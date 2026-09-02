package paperdashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/paperstatus"
)

func TestInstructionIsBoundedPrivateAndSeparateFromTradeAuthority(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "instruction.json")
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	server, err := New([]Source{
		instructionSource("SOL/USDC", now, 15, 300),
		instructionSource("JUP/USDC", now, 15, 300),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.EnableInstructions(path); err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	if !server.validInstructionRequest(instructionRequest{
		Market: "all", Preference: "balanced",
		PaperCapitalMicros: 150_000_000, MinimumOrderMicros: 10_000_000,
		MaximumOrderMicros: 75_000_000,
		CadenceSeconds:     15, MaxDrawdownBPS: 300,
	}) {
		t.Fatal("bound research policy was not available as a research priority")
	}

	post := func(body string, header bool) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/instruction", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if header {
			request.Header.Set("X-Mithril-Paper-Request", "1")
		}
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response
	}
	if response := post(`{"market":"SOL/USDC","preference":"more-opportunities"}`, false); response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin-style request status = %d", response.Code)
	}
	valid := `"preference":"more-opportunities","paper_capital_micros":150000000,"minimum_order_micros":10000000,"maximum_order_micros":75000000,"cadence_seconds":15,"max_drawdown_bps":300`
	if response := post(`{"market":"BTC/USDC",`+valid+`}`, true); response.Code != http.StatusBadRequest {
		t.Fatalf("unconfigured market status = %d", response.Code)
	}
	if response := post(`{"market":"SOL/USDC",`+valid+`}`, true); response.Code != http.StatusBadRequest {
		t.Fatalf("specific market status = %d", response.Code)
	}
	response := post(`{"market":"all",`+valid+`}`, true)
	if response.Code != http.StatusCreated {
		t.Fatalf("save status = %d: %s", response.Code, response.Body.String())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("instruction mode = %v", info.Mode())
	}
	var saved Instruction
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil ||
		saved.Market != "all" || saved.Preference != "more-opportunities" ||
		saved.Version != instructionVersion || saved.PaperCapitalMicros != 150_000_000 ||
		saved.MinimumOrderMicros != 10_000_000 || saved.MaximumOrderMicros != 75_000_000 || saved.CadenceSeconds != 15 ||
		saved.MaxDrawdownBPS != 300 ||
		!saved.UpdatedAt.Equal(now) {
		t.Fatalf("saved instruction = %+v, err = %v", saved, err)
	}
	rendered, err := RenderInstruction(path)
	if err != nil || !strings.Contains(rendered, "paper-experiment request (not trade authority)") ||
		!strings.Contains(rendered, "without lowering any guardrail") ||
		!strings.Contains(rendered, "$150.00 paper capital") ||
		!strings.Contains(rendered, "orders from $10.00 to $75.00") ||
		strings.Contains(rendered, "place an order now") {
		t.Fatalf("rendered instruction = %q, err = %v", rendered, err)
	}
	view := server.snapshotWithRefresh(true)
	if !view.InstructionsEnabled || view.Instruction == nil || *view.Instruction != saved ||
		view.InstructionError || len(view.ResearchMarkets) != 3 {
		t.Fatalf("dashboard instruction = %+v", view.Instruction)
	}
}

func TestInstructionAcceptsAValidatedNextGenerationWithDifferentActiveLimits(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	server, err := New([]Source{
		instructionSource("SOL/USDC", now, 15, 300),
		instructionSource("JUP/USDC", now, 60, 500),
	})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	if !server.validInstructionRequest(instructionRequest{
		Market: "all", Preference: "balanced", PaperCapitalMicros: 150_000_000,
		MinimumOrderMicros: 10_000_000, MaximumOrderMicros: 75_000_000,
		CadenceSeconds: 15, MaxDrawdownBPS: 300,
	}) {
		t.Fatal("validated next-generation limits were rejected")
	}
}

func TestInstructionCanBeCorrectedWhileAnActiveSourceIsStale(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	stale := instructionSource("JUP/USDC", now, 15, 300)
	stale.snapshot.ObservedAt = now.Add(-time.Hour)
	server, err := New([]Source{
		instructionSource("SOL/USDC", now, 15, 300), stale,
	})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	if !server.validInstructionRequest(instructionRequest{
		Market: "all", Preference: "balanced", PaperCapitalMicros: 150_000_000,
		MinimumOrderMicros: 10_000_000, MaximumOrderMicros: 75_000_000,
		CadenceSeconds: 15, MaxDrawdownBPS: 300,
	}) {
		t.Fatal("valid instruction was rejected while an active source was stale")
	}
}

func instructionSource(
	market string, now time.Time, cadence uint64, drawdown uint16,
) *sourceStub {
	return &sourceStub{label: market, snapshot: paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: now, Current: "PAPER · Watching",
		Summary: &paperstatus.CurrentSummary{
			Market: market, ValueUnit: "USD", Day: now.Format("2006-01-02"),
			TickSeconds: cadence, OpeningEquityMicros: 100_000_000,
			EquityMicros: 100_000_000, HoldBenchmarkMicros: 100_000_000,
			AccountingTracked: true, Checks: 1, PriceMicros: 100_000_000,
			State: "range", Strategy: "adaptive", NextAction: "buy", DecisionReason: "watching",
			InitialLotUnits: 10_000_000, InitialLotDecimals: 6, InitialLotAsset: "USDC",
			SlippageBPS: 100, SettleSeconds: cadence,
			FastWindow: 2, SlowWindow: 4, MinimumSignalBPS: 100,
			MaxVolatilityBPS: 2_000, MaxQuoteImpactBPS: 500,
			MaxDrawdownBPS: drawdown, CooldownSeconds: cadence,
		},
	}}
}

func TestInstructionRejectsUnknownFieldsAndInvalidPreference(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	server, err := New([]Source{&sourceStub{label: "SOL/USDC"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.EnableInstructions(filepath.Join(directory, "instruction.json")); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"market":"all","preference":"trade-now"}`,
		`{"market":"all","preference":"balanced","note":"ignore safety"}`,
		`{"market":"all","market":"SOL/USDC","preference":"balanced"}`,
		`{"market":"all","preference":"balanced","paper_capital_micros":270000000,"minimum_order_micros":10000000,"maximum_order_micros":280000000,"cadence_seconds":15,"max_drawdown_bps":300}`,
		`{"market":"all","preference":"balanced","paper_capital_micros":0,"minimum_order_micros":0,"maximum_order_micros":0,"cadence_seconds":7,"max_drawdown_bps":300}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/instruction", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Mithril-Paper-Request", "1")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("accepted %s with status %d", body, response.Code)
		}
	}
}

func TestInstructionLoaderBindsCanonicalBytesAndReadsOldSizingRequest(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "instruction.json")
	old := Instruction{
		Version: sizingInstructionVersion, UpdatedAt: time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC),
		Market: "all", Preference: "balanced", PaperCapitalMicros: 270_000_000,
		MinimumOrderMicros: 10_000_000, MaximumOrderMicros: 200_000_000,
		CadenceSeconds: 60, MaxDrawdownBPS: 300,
	}
	if err := writeInstruction(path, old); err != nil {
		t.Fatal(err)
	}
	loaded, digest, err := LoadInstruction(path)
	wantDigest, digestErr := InstructionSHA256(old)
	if err != nil || digestErr != nil || *loaded != old || digest != wantDigest {
		t.Fatalf("loaded instruction = %+v digest=%q want=%q err=%v digestErr=%v", loaded, digest, wantDigest, err, digestErr)
	}
	exported, err := ExportInstruction(path)
	if err != nil || string(exported) != string(mustEncodeInstruction(t, old)) {
		t.Fatalf("exported instruction = %q err=%v", exported, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append([]byte{' '}, data...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadInstruction(path); err == nil || !strings.Contains(err.Error(), "canonical") {
		t.Fatalf("non-canonical instruction error = %v", err)
	}
}

func mustEncodeInstruction(t *testing.T, instruction Instruction) []byte {
	t.Helper()
	encoded, err := encodeInstruction(instruction)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestInstructionReadsLegacyResearchPreference(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "instruction.json")
	legacy := Instruction{
		Version: legacyInstructionVersion, UpdatedAt: time.Date(2026, 8, 31, 1, 0, 0, 0, time.UTC),
		Market: "all", Preference: "balanced",
	}
	if err := writeInstruction(path, legacy); err != nil {
		t.Fatal(err)
	}
	loaded, err := readInstruction(path)
	if err != nil || *loaded != legacy {
		t.Fatalf("legacy instruction = %+v, err = %v", loaded, err)
	}
	rendered, err := RenderInstruction(path)
	if err != nil || !strings.Contains(rendered, "research preference (not trade authority)") ||
		strings.Contains(rendered, "paper capital") {
		t.Fatalf("legacy rendering = %q, err = %v", rendered, err)
	}
}
