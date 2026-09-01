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
	if !strings.Contains(appJS, "card.style.display=card.hidden?'none':''") {
		t.Fatal("instruction controls can override the disabled hidden state")
	}
	if strings.Count(appJS, "feeBudgetUsed=m.fresh&&Boolean(m.fee_budget_tracked)") != 2 {
		t.Fatal("stale fee budgets can be presented as a current-day pause")
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	build := func(label string, checks, unavailable, opening, equity, hold uint64) *sourceStub {
		pnl := int64(equity) - int64(opening)
		return &sourceStub{label: label, snapshot: paperstatus.Snapshot{
			Version: paperstatus.Version, ObservedAt: now,
			Current: "PAPER · <script>alert(1)</script>",
			Summary: &paperstatus.CurrentSummary{
				Market: label, ValueUnit: "USD", Day: "2026-08-31", TickSeconds: 60,
				OpeningEquityMicros: opening, EquityMicros: equity,
				HoldBenchmarkMicros: hold, Checks: checks, Trades: 1, Signals: 1,
				AccountingTracked: true, RealizedMicros: pnl / 4,
				UnrealizedMicros: pnl - pnl/4,
				FeesMicros:       15_000, TurnoverMicros: opening * 3,
				Unobservable: unavailable, PriceMicros: 100_000_000,
				State: "range", Strategy: "adaptive", NextAction: "buy",
				DecisionReason:  "signal_below_cost_hurdle",
				InitialLotUnits: 250_000_000, InitialLotDecimals: 6, InitialLotAsset: "USDC",
				FeeReserveLamports: 32_000_000, FeeLamports: 100_000, FeeBudgetTracked: true,
				RemainingFeeReserveLamports: 29_000_000, EstimatedFillsRemaining: 290,
				SlippageBPS: 100, SettleSeconds: 60,
				FastWindow: 5, SlowWindow: 20, MinimumSignalBPS: 20,
				MaxVolatilityBPS: 500, MaxQuoteImpactBPS: 500, MaxDrawdownBPS: 300,
				CooldownSeconds: 300,
			},
			History: []paperstatus.PerformancePoint{
				{At: now.Add(-10 * time.Minute), PriceMicros: 99_000_000, EquityMicros: opening, HoldBenchmarkMicros: opening},
				{At: now, PriceMicros: 100_000_000, EquityMicros: equity, HoldBenchmarkMicros: hold},
			},
			Events: []paperstatus.Event{{
				ID: strings.Repeat("a", 64), At: now, Kind: paperstatus.KindOrderFilled,
				Message: "PAPER · </script><script>alert(1)</script>",
			}},
		}}
	}
	sol := build("SOL/USDC", 100, 0, 100_000_000, 101_000_000, 100_500_000)
	sol.snapshot.DroppedEvents = 3
	server, err := New([]Source{
		sol, build("JUP/USDC", 10, 5, 50_000_000, 49_000_000, 48_000_000),
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
		!view.Overview.AccountingTracked || view.Overview.RealizedMicros != 0 ||
		view.Overview.UnrealizedMicros != 0 || view.Overview.FeesMicros != 30_000 ||
		view.Overview.TurnoverMicros != 450_000_000 || view.Markets[0].History[0].PriceMicros != 99_000_000 ||
		len(view.Markets) != 2 || len(view.Markets[0].History) != 2 ||
		len(view.Activity) != 2 || view.ActivityOmitted != 3 {
		t.Fatalf("view = %+v", view)
	}
	if view.Markets[0].InitialLotUnits != 250_000_000 ||
		view.Markets[0].FeesMicros != 15_000 || view.Markets[0].TurnoverMicros != 300_000_000 ||
		view.Markets[0].InitialLotAsset != "USDC" || view.Markets[0].MaxDrawdownBPS != 300 ||
		!view.Markets[0].FeeBudgetTracked ||
		view.Markets[0].RemainingFeeReserveLamports != 29_000_000 ||
		view.Markets[0].EstimatedFillsRemaining != 290 ||
		view.Markets[0].DecisionReason != "signal_below_cost_hurdle" {
		t.Fatalf("paper limits = %+v", view.Markets[0])
	}
	if view.InstructionsEnabled {
		t.Fatal("instruction controls shown without an enabled instruction endpoint")
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
		"/": {"Paper order activity", "Live updates: On", "id=\"refresh-status\"", "role=\"tabpanel\"", "tabindex=\"0\"", "Automation setup", "Reviewed scope", "WIF, JTO, and PYTH", "View recent paper orders", "Plan the next paper experiment", "Largest order", "Paper loss stop", "Activation:"},
		"/app.css": {
			".help:focus", ".help[aria-expanded=\"true\"]", ".button.loading:before", "@keyframes spin",
			".market-overview", ".market-price{font-size:1.3rem}", "--line-strong:#51647a", "--subtle:#7f8b9a",
			".topbar:after,.metric:before", ".dot,.dot.ok,.dot.bad", "backdrop-filter:none",
			".badge.green{background:var(--green-bg)!important}", ".controls .button{padding-inline:6px",
			"@media(max-width:520px){.automation-grid", "@media(max-width:430px){.topbar",
			"@media(max-width:390px){.tabs", "@media(max-width:360px){.metrics",
		},
		"/app.js": {
			"Paper value now", "Started today with", "Today's result", "Compared with holding", "Filled paper orders",
			"role=\"tooltip\"", "aria-describedby", "event.key!=='Escape'", "Waiting for fresh prices",
			"More filled orders do not necessarily mean more profit.",
			"?fresh=1", "Refreshing…", "Updated ✓",
			"Checked ✓", "Data delayed", "requestSequence", "Last result",
			"Market-responsive paper plan", "not strategy quality", "deltaValue",
			"readableActivity", "Use Refresh to try again.", "liveUpdates&&!$('refresh').disabled",
			"This market value:", "This market's result today:", "readableActivityResult", "(profit?'profit':'loss')",
			"Plan tried to trade once", "The plan checked ",
			"market-overview", "Current plan", "age(current.observed_at)",
			"integer(micros)>0n&&integer(micros)<10000n?'<$0.01'",
			"amount>=1000000n?2:amount>=10000n?4:6",
			"chartDots", "marketPriceChart", "Largest drop", "Our strategy '+paperValue", "If held '+paperValue", "Closed-trade result", "Open-position result", "older events omitted", "Proposal ready", "Nous Hermes",
			"Rejected output", "No valid run yet", "No active plan was changed.", "open-order-history", "Starting trade lot", "Loss pause",
			"Minimum opportunity", "saveInstruction", "X-Mithril-Paper-Request",
			"Fee budget left", "Orders left today", "No more orders today",
			"Orders paused for today", "Orders paused until tomorrow",
			"Total traded today", "Modeled fees today", "renderActiveLimits",
			"High concentration", "Save experiment request", "validInstructionRequest",
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
