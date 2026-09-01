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
	server, err := New([]Source{&sourceStub{label: "SOL/USDC"}, &sourceStub{label: "JUP/USDC"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.EnableInstructions(path); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }
	if !server.validInstructionRequest(instructionRequest{
		Market: "JTO/USDC", Preference: "balanced", PaperCapitalMicros: 270_000_000,
		MinimumOrderMicros: 10_000_000, MaximumOrderMicros: 200_000_000,
		CadenceSeconds: 15, MaxDrawdownBPS: 300,
	}) {
		t.Fatal("reviewed observation candidate was not available as a research priority")
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
	valid := `"preference":"more-opportunities","paper_capital_micros":270000000,"minimum_order_micros":10000000,"maximum_order_micros":200000000,"cadence_seconds":15,"max_drawdown_bps":300`
	if response := post(`{"market":"BTC/USDC",`+valid+`}`, true); response.Code != http.StatusBadRequest {
		t.Fatalf("unconfigured market status = %d", response.Code)
	}
	response := post(`{"market":"SOL/USDC",`+valid+`}`, true)
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
		saved.Market != "SOL/USDC" || saved.Preference != "more-opportunities" ||
		saved.PaperCapitalMicros != 270_000_000 || saved.MinimumOrderMicros != 10_000_000 ||
		saved.MaximumOrderMicros != 200_000_000 || saved.CadenceSeconds != 15 ||
		saved.MaxDrawdownBPS != 300 ||
		!saved.UpdatedAt.Equal(now) {
		t.Fatalf("saved instruction = %+v, err = %v", saved, err)
	}
	rendered, err := RenderInstruction(path)
	if err != nil || !strings.Contains(rendered, "paper-experiment request (not trade authority)") ||
		!strings.Contains(rendered, "without lowering any guardrail") ||
		!strings.Contains(rendered, "$270.00 paper capital") ||
		!strings.Contains(rendered, "orders from $10.00 to $200.00") ||
		strings.Contains(rendered, "place an order now") {
		t.Fatalf("rendered instruction = %q, err = %v", rendered, err)
	}
	view := server.snapshotWithRefresh(true)
	if !view.InstructionsEnabled || view.Instruction == nil || *view.Instruction != saved ||
		view.InstructionError || len(view.ResearchMarkets) != 3 {
		t.Fatalf("dashboard instruction = %+v", view.Instruction)
	}
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
		`{"market":"all","preference":"balanced","paper_capital_micros":270000000,"minimum_order_micros":10000000,"maximum_order_micros":200000000,"cadence_seconds":7,"max_drawdown_bps":300}`,
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
