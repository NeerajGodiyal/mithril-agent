package paperdashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/paperstatus"
)

type sourceStub struct {
	mu       sync.Mutex
	label    string
	snapshot paperstatus.Snapshot
	err      error
	reads    int
}

func (s *sourceStub) SourceLabel() string { return s.label }

func (s *sourceStub) Read() (paperstatus.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	return s.snapshot, s.err
}

func (s *sourceStub) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func TestStatusCombinesMarketsWithoutExposingIDsOrHTML(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	build := func(label string, checks, unavailable, opening, equity, hold uint64) *sourceStub {
		return &sourceStub{label: label, snapshot: paperstatus.Snapshot{
			Version: paperstatus.Version, ObservedAt: now,
			Current: "PAPER · <script>alert(1)</script>",
			Summary: &paperstatus.CurrentSummary{
				Market: label, ValueUnit: "USD", Day: "2026-08-31", TickSeconds: 60,
				OpeningEquityMicros: opening, EquityMicros: equity,
				HoldBenchmarkMicros: hold, Checks: checks, Trades: 1, Signals: 1,
				Unobservable: unavailable, PriceMicros: 100_000_000,
				State: "range", Strategy: "adaptive", NextAction: "buy",
			},
			History: []paperstatus.PerformancePoint{
				{At: now.Add(-10 * time.Minute), EquityMicros: opening, HoldBenchmarkMicros: opening},
				{At: now, EquityMicros: equity, HoldBenchmarkMicros: hold},
			},
			Events: []paperstatus.Event{{
				ID: strings.Repeat("a", 64), At: now, Kind: paperstatus.KindOrderFilled,
				Message: "PAPER · </script><script>alert(1)</script>",
			}},
		}}
	}
	server, err := New([]Source{
		build("SOL/USDC", 100, 0, 100_000_000, 101_000_000, 100_500_000),
		build("JUP/USDC", 10, 5, 50_000_000, 49_000_000, 48_000_000),
	})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	request := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/status", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "<script>") ||
		strings.Contains(response.Body.String(), strings.Repeat("a", 64)) {
		t.Fatalf("unsafe response %d: %s", response.Code, response.Body.String())
	}
	var view View
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Complete || view.Overview.EquityMicros != 150_000_000 ||
		view.Overview.Signals != 2 || view.Overview.CoverageBPS != 5_000 ||
		len(view.Markets) != 2 || len(view.Markets[0].History) != 2 ||
		len(view.Activity) != 2 {
		t.Fatalf("view = %+v", view)
	}
	for _, header := range []string{
		"Content-Security-Policy", "Cross-Origin-Resource-Policy",
		"Referrer-Policy", "X-Content-Type-Options", "X-Frame-Options",
	} {
		if response.Header().Get(header) == "" {
			t.Errorf("missing %s", header)
		}
	}
}

func TestStatusReadsEachSourceOnlyOncePerRefreshInterval(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	source := &sourceStub{label: "SOL/USDC", snapshot: paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: now, Current: "PAPER · Watching",
		Summary: &paperstatus.CurrentSummary{
			Market: "SOL/USDC", ValueUnit: "USD", Day: "2026-08-31", TickSeconds: 60,
			OpeningEquityMicros: 1, EquityMicros: 1, HoldBenchmarkMicros: 1,
		},
	}}
	server, err := New([]Source{source})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	var wait sync.WaitGroup
	for range 100 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			request := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/status", nil)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Errorf("status = %d", response.Code)
			}
		}()
	}
	wait.Wait()
	if got := source.readCount(); got != 1 {
		t.Fatalf("source reads = %d, want 1", got)
	}
	source.mu.Lock()
	source.snapshot.Summary.EquityMicros = 2
	source.mu.Unlock()
	request := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/status?fresh=1", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	var forced View
	if err := json.Unmarshal(response.Body.Bytes(), &forced); err != nil {
		t.Fatal(err)
	}
	if got := source.readCount(); got != 2 || forced.Overview.EquityMicros != 2 {
		t.Fatalf("forced refresh = %d reads, %d equity; want 2 reads, 2 equity", got, forced.Overview.EquityMicros)
	}
	now = now.Add(refreshInterval)
	request = httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/status", nil)
	server.ServeHTTP(httptest.NewRecorder(), request)
	if got := source.readCount(); got != 3 {
		t.Fatalf("source reads after refresh = %d, want 3", got)
	}
}

func TestStatusRejectsIncompleteStaleOrMismatchedSummaries(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	for name, source := range map[string]*sourceStub{
		"missing summary": {label: "SOL/USDC", snapshot: paperstatus.Snapshot{
			Version: paperstatus.Version, ObservedAt: now, Current: "PAPER · Watching",
		}},
		"stale summary": {label: "SOL/USDC", snapshot: paperstatus.Snapshot{
			Version: paperstatus.Version, ObservedAt: now.Add(-10 * time.Minute),
			Current: "PAPER · Watching", Summary: &paperstatus.CurrentSummary{
				Market: "SOL/USDC", ValueUnit: "USD", Day: "2026-08-31", TickSeconds: 60,
				OpeningEquityMicros: 1, EquityMicros: 2, HoldBenchmarkMicros: 1,
			},
		}},
		"waiting for price data": {label: "SOL/USDC", snapshot: paperstatus.Snapshot{
			Version: paperstatus.Version, ObservedAt: now,
			Current: "PAPER · Waiting for prices", Summary: &paperstatus.CurrentSummary{
				Market: "SOL/USDC", ValueUnit: "USD", Day: "2026-08-31", TickSeconds: 60,
				OpeningEquityMicros: 1, EquityMicros: 2, HoldBenchmarkMicros: 1,
				Checks: 2, Unobservable: 1, PriceMicros: 100_000_000, State: "waiting for data",
			},
		}},
		"mismatched market": {label: "SOL/USDC", snapshot: paperstatus.Snapshot{
			Version: paperstatus.Version, ObservedAt: now, Current: "PAPER · Watching",
			Summary: &paperstatus.CurrentSummary{
				Market: "JUP/USDC", ValueUnit: "USD", Day: "2026-08-31", TickSeconds: 60,
				OpeningEquityMicros: 1, EquityMicros: 1, HoldBenchmarkMicros: 1,
			},
		}},
		"missing value unit": {label: "SOL/USDC", snapshot: paperstatus.Snapshot{
			Version: paperstatus.Version, ObservedAt: now, Current: "PAPER · Watching",
			Summary: &paperstatus.CurrentSummary{
				Market: "SOL/USDC", Day: "2026-08-31", TickSeconds: 60,
				OpeningEquityMicros: 1, EquityMicros: 1, HoldBenchmarkMicros: 1,
			},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			server, err := New([]Source{source})
			if err != nil {
				t.Fatal(err)
			}
			server.now = func() time.Time { return now }
			view := server.snapshot()
			if view.Complete || view.Overview.EquityMicros != 0 {
				t.Fatalf("unsafe aggregate = %+v", view)
			}
			request := httptest.NewRequest(http.MethodGet, "http://localhost/healthz", nil)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("health status = %d", response.Code)
			}
		})
	}
}

func TestStatusOmitsObservedAtWhenNoMarketIsReady(t *testing.T) {
	server, err := New([]Source{&sourceStub{label: "SOL/USDC", err: io.EOF}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/status", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if strings.Contains(response.Body.String(), `"observed_at"`) ||
		strings.Contains(response.Body.String(), "0001-01-01") {
		t.Fatalf("response includes a fake observation time: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"activity":[]`) {
		t.Fatalf("response does not include an empty activity list: %s", response.Body.String())
	}
}

func TestStatusDoesNotAggregateDifferentValueUnits(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	build := func(label, unit string) *sourceStub {
		return &sourceStub{label: label, snapshot: paperstatus.Snapshot{
			Version: paperstatus.Version, ObservedAt: now, Current: "PAPER · Watching",
			Summary: &paperstatus.CurrentSummary{
				Market: label, ValueUnit: unit, Day: "2026-08-31", TickSeconds: 60,
				OpeningEquityMicros: 1, EquityMicros: 1, HoldBenchmarkMicros: 1,
			},
		}}
	}
	server, err := New([]Source{build("SOL/USDC", "USD"), build("SOL/devUSDC", "devUSDC")})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	view := server.snapshot()
	if view.Complete || view.Overview.EquityMicros != 0 || view.Overview.ValueUnit != "" {
		t.Fatalf("mixed-unit aggregate = %+v", view.Overview)
	}
	request := httptest.NewRequest(http.MethodGet, "http://localhost/healthz", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("mixed-unit health status = %d", response.Code)
	}
}

func TestDashboardRejectsRemoteHostsAndMutatingMethods(t *testing.T) {
	server, err := New([]Source{&sourceStub{label: "SOL/USDC"}})
	if err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	remoteResponse := httptest.NewRecorder()
	server.ServeHTTP(remoteResponse, remote)
	if remoteResponse.Code != http.StatusBadRequest {
		t.Fatalf("remote Host status = %d", remoteResponse.Code)
	}
	mutation := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/status", strings.NewReader("x"))
	mutationResponse := httptest.NewRecorder()
	server.ServeHTTP(mutationResponse, mutation)
	if mutationResponse.Code != http.StatusMethodNotAllowed ||
		mutationResponse.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("mutation response = %d %+v", mutationResponse.Code, mutationResponse.Header())
	}
	if body, _ := io.ReadAll(mutationResponse.Result().Body); len(body) > 128 {
		t.Fatalf("oversized error body: %q", body)
	}
}

func TestDashboardUsesBeginnerLanguageAndAccessibleExplanations(t *testing.T) {
	server, err := New([]Source{&sourceStub{label: "SOL/USDC"}})
	if err != nil {
		t.Fatal(err)
	}
	for path, wants := range map[string][]string{
		"/":        {"Paper order activity", "aria-live=\"polite\">Refresh"},
		"/app.css": {".help:focus", ".help[aria-expanded=\"true\"]", ".button.loading:before", "@keyframes spin"},
		"/app.js": {
			"Practice account", "Today's result", "Versus no trading", "Completed trades",
			"role=\"tooltip\"", "aria-describedby", "event.key!=='Escape'", "Waiting for fresh prices",
			"More trades do not necessarily mean more profit.", "repeats can belong to the same trade",
			"?fresh=1", "Refreshing…", "Updated ✓",
			"Checked ✓", "Data delayed", "requestSequence", "Last recorded result",
			"Market-responsive paper plan", "does not retrain itself live", "deltaValue",
		},
	} {
		request := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.Code)
		}
		for _, want := range wants {
			if !strings.Contains(response.Body.String(), want) {
				t.Errorf("%s omits %q", path, want)
			}
		}
	}
}

func TestNewRejectsDuplicateOrInvalidSources(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("missing sources accepted")
	}
	if _, err := New([]Source{&sourceStub{label: "bad label"}}); err == nil {
		t.Fatal("invalid label accepted")
	}
	if _, err := New([]Source{
		&sourceStub{label: "SOL/USDC"}, &sourceStub{label: "SOL/USDC"},
	}); err == nil {
		t.Fatal("duplicate label accepted")
	}
}
