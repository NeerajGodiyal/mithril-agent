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
		Market: "JTO/USDC", Preference: "balanced",
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
	if response := post(`{"market":"BTC/USDC","preference":"more-opportunities"}`, true); response.Code != http.StatusBadRequest {
		t.Fatalf("unconfigured market status = %d", response.Code)
	}
	response := post(`{"market":"SOL/USDC","preference":"more-opportunities"}`, true)
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
		!saved.UpdatedAt.Equal(now) {
		t.Fatalf("saved instruction = %+v, err = %v", saved, err)
	}
	rendered, err := RenderInstruction(path)
	if err != nil || !strings.Contains(rendered, "research preference (not trade authority)") ||
		!strings.Contains(rendered, "without lowering any guardrail") ||
		strings.Contains(rendered, "place an order") {
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
