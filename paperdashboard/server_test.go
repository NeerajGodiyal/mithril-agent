package paperdashboard

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestAutomationListsOnlyOptionalExperiments(t *testing.T) {
	if !strings.Contains(appJS, "completedPerps=perpsMarkets.filter") ||
		!strings.Contains(appJS, "additionalSpots=current.markets.filter(market=>market.optional&&!isPerps(market))") ||
		!strings.Contains(appJS, "perps markets have a saved result") {
		t.Fatal("paper engine status must separate optional spot observers from perps experiments")
	}
	if !strings.Contains(appJS, "retained in the final packet") || strings.Contains(appJS, "unique source'+(packet.sources_checked===1?'':'s')+' checked") {
		t.Fatal("Hermes status must distinguish retrieved pages from sources retained in its final packet")
	}
}

func TestOptionalStoppedSpotIsCompletedRatherThanLiveOrPerps(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	completed := paperstatus.Event{
		ID: strings.Repeat("a", 64), At: now,
		Kind: paperstatus.KindExperimentDone, Message: "PAPER · STOPPED",
	}
	wif := &sourceStub{label: "WIF/USDC", snapshot: paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: now, Events: []paperstatus.Event{completed},
		Current: "PAPER · STOPPED",
		Summary: &paperstatus.CurrentSummary{
			Market: "WIF/USDC", ValueUnit: "USD", Day: "2026-09-03", TickSeconds: 60,
			OpeningEquityMicros: 100_000_000, EquityMicros: 101_000_000,
			HoldBenchmarkMicros: 100_500_000, AccountingTracked: true,
			RealizedMicros: 1_000_000, Checks: 10, Signals: 1, Trades: 1,
			PriceMicros: 220_000, State: "completed", Strategy: "adaptive",
		},
	}}
	completed.ID = strings.Repeat("b", 64)
	jto := &sourceStub{label: "JTO/USDC", snapshot: paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: now, Events: []paperstatus.Event{completed},
	}}
	server, err := New([]Source{Optional(wif), Optional(jto)})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	view := server.readSnapshot(now)
	if len(view.Markets) != 2 || !view.Markets[0].Optional || !view.Markets[0].Completed ||
		!view.Markets[0].Ready || view.Markets[0].Fresh ||
		!view.Markets[1].Optional || !view.Markets[1].Completed ||
		view.Markets[1].Ready || view.Markets[1].Fresh {
		t.Fatalf("stopped optional spot views = %+v", view.Markets)
	}
}

func TestPerpsViewKeepsLiveStateSeparateFromLatestCompletedEvidence(t *testing.T) {
	now := time.Date(2026, 9, 4, 17, 0, 0, 0, time.UTC)
	completedAt := now.Add(-time.Hour)
	completed := paperstatus.CurrentSummary{
		Market: "SOL-PERP", Instrument: "perpetual", RiskProfile: "experimental",
		PositionDirection: "flat", LeverageBPS: 30_000, FundingTracked: true,
		ValueUnit: "USD", Day: completedAt.Format("2006-01-02"), TickSeconds: 60,
		OpeningEquityMicros: 100_000_000, EquityMicros: 101_000_000,
		HoldBenchmarkMicros: 100_000_000, AccountingTracked: true, RealizedMicros: 1_000_000,
		Checks: 60, Signals: 2, Trades: 2, State: "watching", Strategy: "fixed",
		PriceMicros: 99_000_000, DecisionReason: "action_level_not_met",
		DecisionSignalKind: "two_candle_move", DecisionSignalBPS: 10,
		DecisionThresholdBPS: 50, MinimumResearchFrames: 24,
		DecisionSource: "legacy_fixed_policy", ProposalSource: "built_in",
		RunPlanSHA256: strings.Repeat("b", 64), QualificationTracked: true,
		QualificationOutcome: "candidate_ready_for_more_paper_testing",
		QualificationSHA256:  strings.Repeat("c", 64), QualificationTapes: 1,
		QualificationFrames: 60, QualificationMinimumFrames: 24,
		QualificationTrainingFrames: 40, QualificationHoldoutFrames: 20,
		QualificationStrategy: "momentum", QualificationRiskProfile: "balanced",
		QualificationHoldoutEvaluated: true, QualificationStressEvaluated: true,
		QualificationHoldoutScored: true, QualificationStressScored: true,
		QualificationHoldoutMicros: 100_000, QualificationStressMicros: 50_000,
		QualificationAttempts: []paperstatus.QualificationAttempt{{
			RiskProfile: "balanced", Strategy: "momentum", NetPnLMicros: 90_000,
			FeesMicros: 10_000, MaxDrawdownMicros: 25_000, FilledOrders: 2, ClosedPositions: 2,
		}},
	}
	live := &paperstatus.CurrentSummary{
		Market: "SOL-PERP", Instrument: "perpetual", RiskProfile: "balanced",
		PositionDirection: "long", LeverageBPS: 20_000, FundingTracked: true,
		ValueUnit: "USD", Day: now.Format("2006-01-02"), TickSeconds: 60,
		OpeningEquityMicros: 200_000_000, EquityMicros: 205_000_000,
		HoldBenchmarkMicros: 201_000_000, Checks: 7, Signals: 1, Trades: 1,
		State: "watching", Strategy: "fixed", PriceMicros: 105_000_000,
		DecisionReason: "watching", DecisionSignalKind: "momentum",
		DecisionSignalBPS: 60, DecisionThresholdBPS: 40, MinimumResearchFrames: 24,
	}
	source := &sourceStub{label: "SOL-PERP", snapshot: paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: now, Current: "PAPER · Recording", Summary: live,
		Events: []paperstatus.Event{{
			ID: strings.Repeat("a", 64), At: completedAt,
			Kind: paperstatus.KindExperimentDone, Message: "PAPER · Completed",
		}},
		LatestCompleted: &paperstatus.CompletedSnapshot{
			ObservedAt: completedAt, EventID: strings.Repeat("a", 64), Summary: completed,
		},
	}}
	server, err := New([]Source{Optional(source)})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	view := server.snapshotWithRefresh(true)
	if len(view.Markets) != 1 {
		t.Fatalf("markets = %+v", view.Markets)
	}
	market := view.Markets[0]
	if !market.Available || !market.Ready || !market.Fresh || market.Completed ||
		market.EquityMicros != live.EquityMicros || market.Checks != live.Checks ||
		market.PriceMicros != live.PriceMicros ||
		market.DecisionSignalKind != live.DecisionSignalKind ||
		market.DecisionSignalBPS != live.DecisionSignalBPS ||
		market.DecisionThresholdBPS != live.DecisionThresholdBPS ||
		market.MinimumResearchFrames != live.MinimumResearchFrames ||
		market.QualificationTracked || market.LatestCompleted == nil {
		t.Fatalf("live market projection = %+v", market)
	}
	latest := market.LatestCompleted
	if latest.ObservedAt == nil || !latest.Completed || latest.Fresh || latest.State != "completed" ||
		!latest.ObservedAt.Equal(completedAt) || latest.EquityMicros != completed.EquityMicros ||
		latest.PriceMicros != completed.PriceMicros ||
		latest.DecisionSignalKind != completed.DecisionSignalKind ||
		latest.DecisionSignalBPS != completed.DecisionSignalBPS ||
		latest.DecisionThresholdBPS != completed.DecisionThresholdBPS ||
		latest.MinimumResearchFrames != completed.MinimumResearchFrames ||
		!latest.QualificationTracked || latest.QualificationFrames != completed.QualificationFrames ||
		latest.InstructionSHA256 != "" || latest.LatestCompleted != nil || len(latest.History) != 0 {
		t.Fatalf("latest completed projection = %+v", latest)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("dashboard exposed private receipt binding %q", forbidden)
		}
	}
	if view.Overview.EquityMicros != 0 {
		t.Fatalf("optional perps changed spot overview: %+v", view.Overview)
	}

	source.snapshot.ObservedAt = completedAt
	source.snapshot.Current = "PAPER · Completed"
	source.snapshot.Summary = &completed
	view = server.snapshotWithRefresh(true)
	if len(view.Markets) != 1 || !view.Markets[0].Completed {
		t.Fatalf("terminal current result is not completed = %+v", view.Markets)
	}
}

func TestPerpsViewRejectsMislabeledCompletedOnlyReceipt(t *testing.T) {
	now := time.Date(2026, 9, 4, 17, 0, 0, 0, time.UTC)
	completed := paperstatus.CurrentSummary{
		Market: "BTC-PERP", Instrument: "perpetual", RiskProfile: "balanced",
		PositionDirection: "flat", LeverageBPS: 20_000, FundingTracked: true,
		ValueUnit: "USD", Day: now.Format("2006-01-02"), TickSeconds: 60,
		OpeningEquityMicros: 100_000_000, EquityMicros: 100_000_000,
		HoldBenchmarkMicros: 100_000_000, Checks: 10, State: "watching", Strategy: "fixed",
		QualificationTracked: true, QualificationOutcome: "insufficient_evidence",
		QualificationSHA256: strings.Repeat("d", 64), QualificationTapes: 1,
		QualificationFrames: 10, QualificationMinimumFrames: 24,
	}
	source := &sourceStub{label: "SOL-PERP", snapshot: paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: now, Current: paperstatus.UnconfiguredCurrent,
		LatestCompleted: &paperstatus.CompletedSnapshot{
			ObservedAt: now, EventID: strings.Repeat("e", 64), Summary: completed,
		},
	}}
	server, err := New([]Source{Optional(source)})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	view := server.snapshotWithRefresh(true)
	if len(view.Markets) != 1 || view.Markets[0].Available || view.Markets[0].LatestCompleted != nil {
		t.Fatalf("mislabeled completed receipt was exposed: %+v", view.Markets)
	}

	completed.Market = "SOL-PERP"
	source.snapshot.LatestCompleted.Summary = completed
	view = server.snapshotWithRefresh(true)
	if !view.Markets[0].Available || view.Markets[0].Ready || view.Markets[0].LatestCompleted == nil {
		t.Fatalf("valid completed-only receipt disappeared: %+v", view.Markets)
	}
}

func TestStatusCombinesMarketsWithoutExposingIDsOrHTML(t *testing.T) {
	if strings.Contains(appJS, "safe(packet.risk_reason)") || strings.Contains(appJS, "safe(packet.market)") {
		t.Fatal("research text is escaped before the final HTML sink")
	}
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
				MinimumOrderValueMicros: 10_000_000, MaximumOrderValueMicros: 75_000_000,
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
	sol.snapshot.Summary.BalancesTracked = true
	sol.snapshot.Summary.BaseUnits = 750_000_000
	sol.snapshot.Summary.BaseDecimals = 9
	sol.snapshot.Summary.BaseAsset = "SOL"
	sol.snapshot.Summary.QuoteUnits = 20_000_000
	sol.snapshot.Summary.QuoteDecimals = 6
	sol.snapshot.Summary.QuoteAsset = "USDC"
	sol.snapshot.Summary.LiquidFeeReserveLamports = 29_000_000
	sol.snapshot.Summary.LockedSetupRentLamports = 3_000_000
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
		view.Markets[0].MinimumOrderValueMicros != 10_000_000 ||
		view.Markets[0].MaximumOrderValueMicros != 75_000_000 ||
		!view.Markets[0].FeeBudgetTracked ||
		view.Markets[0].RemainingFeeReserveLamports != 29_000_000 ||
		view.Markets[0].EstimatedFillsRemaining != 290 ||
		view.Markets[0].DecisionReason != "signal_below_cost_hurdle" ||
		!view.Markets[0].BalancesTracked || view.Markets[0].BaseUnits != 750_000_000 ||
		view.Markets[0].QuoteUnits != 20_000_000 || view.Markets[0].BaseAsset != "SOL" ||
		view.Markets[0].QuoteAsset != "USDC" ||
		view.Markets[0].LiquidFeeReserveLamports != 29_000_000 ||
		view.Markets[0].LockedSetupRentLamports != 3_000_000 {
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

func TestStatusCoalescesOnlyTheDuplicateTerminalProjection(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	message := "PAPER · 📊 STOPPED\nPaper gain/loss: down $1"
	activity := coalesceTerminalActivity([]Activity{
		{Market: "SOL/USDC", At: now, Kind: paperstatus.KindPeriodClosed, Message: message},
		{Market: "SOL/USDC", At: now, Kind: paperstatus.KindExperimentDone, Message: message},
		{Market: "SOL/USDC", At: now, Kind: paperstatus.KindExperimentDone, Message: message + " changed"},
	})
	if len(activity) != 2 || activity[0].Kind != paperstatus.KindPeriodClosed || activity[1].Message == message {
		t.Fatalf("coalesced activity = %+v", activity)
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

func TestStatusDoesNotFailForAnExplicitlyUnconfiguredMarket(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	sol := &sourceStub{label: "SOL/USDC", snapshot: paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: now, Current: "PAPER · Watching",
		Summary: &paperstatus.CurrentSummary{
			Market: "SOL/USDC", ValueUnit: "USD", Day: "2026-09-02", TickSeconds: 60,
			OpeningEquityMicros: 100_000_000, EquityMicros: 101_000_000,
			HoldBenchmarkMicros: 100_500_000,
		},
	}}
	jup := &sourceStub{label: "JUP/USDC", snapshot: paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: now,
		Current: paperstatus.UnconfiguredCurrent,
	}}
	server, err := New([]Source{sol, jup})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	view := server.snapshotWithRefresh(true)
	if !view.Complete || view.Overview.EquityMicros != 101_000_000 ||
		len(view.Markets) != 2 || view.Markets[1].Available {
		t.Fatalf("optional market view = %+v", view)
	}
}

func TestStatusBindsTheSavedInstructionToOneActiveGeneration(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	instruction := Instruction{
		Version: InstructionVersion, UpdatedAt: now, Market: "all", Preference: "balanced",
		PaperCapitalMicros: 150_000_000, MinimumOrderMicros: 5_000_000,
		MaximumOrderMicros: 75_000_000, CadenceSeconds: 15, MaxDrawdownBPS: 300,
	}
	instructionPath := filepath.Join(directory, "instruction.json")
	if err := writeInstruction(instructionPath, instruction); err != nil {
		t.Fatal(err)
	}
	digest, err := InstructionSHA256(instruction)
	if err != nil {
		t.Fatal(err)
	}
	sol := instructionSource("SOL/USDC", now, 15, 300)
	jup := instructionSource("JUP/USDC", now, 15, 300)
	sol.snapshot.Summary.InstructionSHA256 = digest
	jup.snapshot.Summary.InstructionSHA256 = digest
	perps := instructionSource("SOL-PERP", now, 15, 300)
	server, err := New([]Source{sol, jup, perps})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	if err := server.EnableInstructions(instructionPath); err != nil {
		t.Fatal(err)
	}
	view := server.snapshotWithRefresh(true)
	if !view.Complete || !view.InstructionActive || view.InstructionSHA256 != digest ||
		view.ActiveInstructionSHA256 != digest || view.Overview.EquityMicros != 300_000_000 {
		t.Fatalf("active instruction view = %+v", view)
	}
	jup.mu.Lock()
	jup.snapshot.Summary.InstructionSHA256 = strings.Repeat("b", 64)
	jup.mu.Unlock()
	view = server.snapshotWithRefresh(true)
	if view.Complete || view.InstructionActive || view.ActiveInstructionSHA256 != "" ||
		view.Overview.EquityMicros != 0 {
		t.Fatalf("mixed generation was aggregated: %+v", view)
	}
}

func TestOptionalExperimentCannotEraseAHealthyRequiredOverview(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	spot := instructionSource("SOL/USDC", now, 15, 300)
	stalePerps := instructionSource("SOL-PERP", now.Add(-2*time.Hour), 60, 300)
	stalePerps.snapshot.Summary.Instrument = "perpetual"
	stalePerps.snapshot.Summary.RiskProfile = "balanced"
	stalePerps.snapshot.Summary.PositionDirection = "flat"
	stalePerps.snapshot.Summary.LeverageBPS = 20_000
	stalePerps.snapshot.Summary.FundingTracked = true
	server, err := New([]Source{spot, Optional(stalePerps)})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	view := server.snapshotWithRefresh(true)
	if !view.Complete || view.Overview.EquityMicros != 100_000_000 ||
		len(view.Markets) != 2 || view.Markets[1].Fresh || !view.Markets[1].Optional {
		t.Fatalf("stale optional experiment damaged required overview: %+v", view)
	}

	missingPerps := &sourceStub{label: "BTC-PERP", err: io.EOF}
	server, err = New([]Source{spot, Optional(missingPerps)})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	view = server.snapshotWithRefresh(true)
	if !view.Complete || view.Overview.EquityMicros != 100_000_000 ||
		len(view.Markets) != 2 || view.Markets[1].Available || !view.Markets[1].Optional {
		t.Fatalf("missing optional experiment damaged required overview: %+v", view)
	}

	mislabeledPerps := instructionSource("BTC-PERP", now, 60, 300)
	mislabeledPerps.label = "ETH-PERP"
	server, err = New([]Source{spot, Optional(mislabeledPerps)})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	view = server.snapshotWithRefresh(true)
	if !view.Complete || view.Overview.EquityMicros != 100_000_000 ||
		len(view.Markets) != 2 || !view.Markets[1].Optional {
		t.Fatalf("mislabeled optional experiment damaged required overview: %+v", view)
	}

	freshDifferentGeneration := instructionSource("ETH-PERP", now, 15, 300)
	freshDifferentGeneration.snapshot.Summary.Instrument = "perpetual"
	freshDifferentGeneration.snapshot.Summary.RiskProfile = "balanced"
	freshDifferentGeneration.snapshot.Summary.PositionDirection = "flat"
	freshDifferentGeneration.snapshot.Summary.LeverageBPS = 20_000
	freshDifferentGeneration.snapshot.Summary.FundingTracked = true
	freshDifferentGeneration.snapshot.Summary.InstructionSHA256 = strings.Repeat("b", 64)
	server, err = New([]Source{spot, Optional(freshDifferentGeneration)})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	view = server.snapshotWithRefresh(true)
	if !view.Complete || view.Overview.EquityMicros != 100_000_000 ||
		len(view.Markets) != 2 || !view.Markets[1].Optional {
		t.Fatalf("fresh independent experiment changed the live spot overview: %+v", view)
	}

	freshDifferentUnit := instructionSource("BTC-PERP", now, 15, 300)
	freshDifferentUnit.snapshot.Summary.Instrument = "perpetual"
	freshDifferentUnit.snapshot.Summary.RiskProfile = "balanced"
	freshDifferentUnit.snapshot.Summary.PositionDirection = "flat"
	freshDifferentUnit.snapshot.Summary.LeverageBPS = 20_000
	freshDifferentUnit.snapshot.Summary.FundingTracked = true
	freshDifferentUnit.snapshot.Summary.ValueUnit = "devUSDC"
	server, err = New([]Source{spot, Optional(freshDifferentUnit)})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	view = server.snapshotWithRefresh(true)
	if !view.Complete || view.Overview.EquityMicros != 100_000_000 ||
		view.Overview.ValueUnit != "USD" || len(view.Markets) != 2 || !view.Markets[1].Optional {
		t.Fatalf("incompatible optional unit damaged required overview: %+v", view)
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

func TestDashboardProjectsQualificationWithoutPrivateTapeIdentity(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	digest := strings.Repeat("f", 64)
	source := &sourceStub{label: "SOL-PERP", snapshot: paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: now, Current: "PAPER · checkpoint complete",
		Summary: &paperstatus.CurrentSummary{
			Market: "SOL-PERP", Instrument: "perpetual", RiskProfile: "balanced",
			PositionDirection: "flat", LeverageBPS: 20_000, FundingTracked: true,
			ValueUnit: "USD", Day: "2026-09-03", TickSeconds: 60,
			OpeningEquityMicros: 100_000_000, EquityMicros: 100_100_000,
			HoldBenchmarkMicros: 100_000_000, AccountingTracked: true,
			RealizedMicros: 100_000,
			Checks:         60, Trades: 2, Signals: 2, State: "watching", Strategy: "fixed",
			DecisionSource: "selected_paper_plan", ProposalSource: "deterministic_search",
			RunPlanSHA256:        strings.Repeat("e", 64),
			PerpsPlanOutcome:     &paperstatus.PerpsPlanOutcome{TapeSHA256: strings.Repeat("d", 64), Result: "gain"},
			QualificationTracked: true,
			QualificationOutcome: "candidate_ready_for_more_paper_testing",
			QualificationSHA256:  digest, QualificationTapes: 1, QualificationFrames: 60, QualificationMinimumFrames: 24,
			QualificationTrainingFrames: 40, QualificationHoldoutFrames: 20,
			QualificationStrategy: "momentum", QualificationRiskProfile: "balanced",
			QualificationHoldoutEvaluated: true, QualificationStressEvaluated: true,
			QualificationHoldoutScored: true, QualificationStressScored: true,
			QualificationHoldoutMicros: 100_000, QualificationStressMicros: 50_000,
			QualificationAttempts: []paperstatus.QualificationAttempt{
				{RiskProfile: "conservative", Strategy: "momentum", NetPnLMicros: 75_000, FeesMicros: 5_000, MaxDrawdownMicros: 10_000, FilledOrders: 2, ClosedPositions: 2},
				{RiskProfile: "balanced", Strategy: "breakout", NetPnLMicros: 50_000, FeesMicros: 8_000, FundingMicros: -1_000, MaxDrawdownMicros: 15_000, FilledOrders: 3, ClosedPositions: 3},
				{RiskProfile: "experimental", Strategy: "regime", NetPnLMicros: -25_000, FeesMicros: 10_000, MaxDrawdownMicros: 20_000, Liquidations: 1, FilledOrders: 4, ClosedPositions: 4},
			},
		},
	}}
	source.snapshot.Summary.Strategy = "momentum"
	server, err := New([]Source{Optional(source)})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/status", nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), digest) ||
		strings.Contains(response.Body.String(), strings.Repeat("e", 64)) ||
		strings.Contains(response.Body.String(), strings.Repeat("d", 64)) {
		t.Fatalf("qualification response leaked private identity or failed: %d %s", response.Code, response.Body.String())
	}
	var view View
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Markets) != 1 || !view.Markets[0].QualificationTracked ||
		view.Markets[0].QualificationOutcome != "candidate_ready_for_more_paper_testing" ||
		view.Markets[0].QualificationMinimumFrames != 24 ||
		view.Markets[0].QualificationStrategy != "momentum" ||
		!view.Markets[0].QualificationHoldoutEvaluated || !view.Markets[0].QualificationStressEvaluated ||
		!view.Markets[0].QualificationHoldoutScored || !view.Markets[0].QualificationStressScored ||
		view.Markets[0].QualificationHoldoutMicros != 100_000 ||
		view.Markets[0].QualificationStressMicros != 50_000 ||
		view.Markets[0].DecisionSource != "selected_paper_plan" ||
		view.Markets[0].ProposalSource != "deterministic_search" ||
		view.Markets[0].PerpsPlanOutcome != "gain" ||
		len(view.Markets[0].QualificationAttempts) != 3 ||
		view.Markets[0].QualificationAttempts[0].Strategy != "momentum" ||
		view.Markets[0].QualificationAttempts[2].Liquidations != 1 {
		t.Fatalf("qualification projection = %+v", view.Markets)
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

func TestDashboardServesPinnedInteractiveChartAsset(t *testing.T) {
	server, err := New([]Source{&sourceStub{label: "SOL/USDC"}})
	if err != nil {
		t.Fatal(err)
	}
	const path = "/vendor/lightweight-charts-5.2.1.js"
	get := httptest.NewRecorder()
	server.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil))
	if get.Code != http.StatusOK || get.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("vendor GET = %d %q", get.Code, get.Header().Get("Content-Type"))
	}
	digest := sha256.Sum256(get.Body.Bytes())
	if got := fmt.Sprintf("%x", digest); got != "e21cc5caa0226ef30bd8549c50b9ef926615f2a4ee6b4e486353477a55f598cf" {
		t.Fatalf("vendor digest = %s", got)
	}
	head := httptest.NewRecorder()
	server.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "http://localhost"+path, nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("vendor HEAD = %d, %d bytes", head.Code, head.Body.Len())
	}
	post := httptest.NewRecorder()
	server.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "http://localhost"+path, nil))
	if post.Code != http.StatusMethodNotAllowed || post.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("vendor POST = %d, Allow %q", post.Code, post.Header().Get("Allow"))
	}
}

func TestDashboardServesPinnedLocalFont(t *testing.T) {
	server, err := New([]Source{&sourceStub{label: "SOL/USDC"}})
	if err != nil {
		t.Fatal(err)
	}
	const path = "/vendor/space-grotesk-latin.woff2"
	get := httptest.NewRecorder()
	server.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil))
	if get.Code != http.StatusOK || get.Header().Get("Content-Type") != "font/woff2" {
		t.Fatalf("font GET = %d %q", get.Code, get.Header().Get("Content-Type"))
	}
	digest := sha256.Sum256(get.Body.Bytes())
	if got := fmt.Sprintf("%x", digest); got != "a0d054c4af557de20afd6ca59f47ab353bcaec49c63ff04b6c9d39d0f8910557" {
		t.Fatalf("font digest = %s", got)
	}
	head := httptest.NewRecorder()
	server.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "http://localhost"+path, nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("font HEAD = %d, %d bytes", head.Code, head.Body.Len())
	}
}

func TestDashboardServesPinnedOverclockLogo(t *testing.T) {
	server, err := New([]Source{&sourceStub{label: "SOL/USDC"}})
	if err != nil {
		t.Fatal(err)
	}
	const path = "/vendor/overclock.svg"
	get := httptest.NewRecorder()
	server.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil))
	if get.Code != http.StatusOK || get.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("logo GET = %d %q", get.Code, get.Header().Get("Content-Type"))
	}
	digest := sha256.Sum256(get.Body.Bytes())
	if got := fmt.Sprintf("%x", digest); got != "b77564161f8a92e39dc807254c7dfb782c8d6fc3f47860cc62fbe5f98c1ce76b" {
		t.Fatalf("logo digest = %s", got)
	}
}

func TestDashboardUsesBeginnerLanguageAndAccessibleExplanations(t *testing.T) {
	server, err := New([]Source{&sourceStub{label: "SOL/USDC"}})
	if err != nil {
		t.Fatal(err)
	}
	for path, wants := range map[string][]string{
		"/": {"Paper order activity", "Live updates: On", "id=\"refresh-status\"", "role=\"tabpanel\"", "tabindex=\"0\"", "class=\"overview-workspace\"", "id=\"market-switcher\"", "aria-label=\"Live spot markets\"", "id=\"perps-research-title\"", "id=\"perps-research-list\"", "class=\"activity-table\"", "id=\"help-dialog\"", "Quick explanation", "strategy-brief", "/vendor/overclock.svg", "Automation setup", "Markets being checked", "id=\"market-research\"", "Reviewed scope", "WIF, JTO, and PYTH", "Recorded replay and doubled-fee checks run in minutes", "short live checkpoints", "None proves profitability", "Paper money · No real orders", "View recent paper orders", "Plan the next paper experiment", "Paper capital ceiling", "Smallest order", "Largest order", "Paper loss stop", "saving never restarts Mithril", "About this paper account", "bot's UTC day", "/vendor/lightweight-charts-5.2.1.js", "TradingView Lightweight Charts™"},
		"/app.css": {
			"@font-face", "/vendor/space-grotesk-latin.woff2", "--canvas: #000", "--green: #86efac", "--line-strong: #353535", "--text: #e7e7e7", "--subtle: #7f7f7f",
			".tabs {", "position: fixed", ".tab.active", ".brand-logo", ".panel:focus-visible",
			".metrics {", ".metric:first-child .metric-value", ".help-dialog::backdrop", ".activity-table .activity-list-head", "scrollbar-gutter: stable", ".button.loading::before", "@keyframes spin",
			"height: calc(100vh - 120px)", ".overview-workspace", "grid-template-columns: minmax(0, 3fr) minmax(330px, 2fr)", "grid-template-columns: minmax(110px, 1fr) 82px max-content", ".market-list-head", ".market-choice.active::before", ".market-chart-stage",
			".chart-toggle.active", ".chart-canvas { width: 100%; height: 390px", ".chart-data table", ".balance-strip {",
			".activity-list-head", ".strategy-market-row", ".automation-list-head", "@keyframes view-enter",
			".market-research-grid", ".market-research-card", ".research-progress::-webkit-progress-value",
			"@media (max-width: 1023px)", "@media (max-width: 767px)", "@media (max-width: 430px)", "prefers-reduced-motion",
		},
		"/app.js": {
			"Live spot account", "Spot account start", "Spot result", "Spot versus holding", "Spot executions",
			"Completed experiment", "Not selected", "Best completed training attempts", "Training candidate",
			"Final held-out recording", "separate recordings",
			"<button class=\"help\"", "data-help-copy=", "helpDialog.showModal()", "Waiting for fresh prices",
			"?fresh=1", "Refreshing…", "Updated ✓",
			"Checked ✓", "Data delayed", "requestSequence",
			"Market-responsive paper plan", "not strategy quality", "deltaValue",
			"activityStatus", "activityStatusMarkup", "Use Refresh to try again.", "liveUpdates&&!$('refresh').disabled",
			"Order status", "Placed", "Filled", "Data status", "Delayed", "Restored",
			"Plan tried to trade once",
			"Performance", "marketStatus(m,feeBudgetUsed)",
			"paperBalanceStrip", "Paper cash", "Trading holdings", "Separate SOL for fees", "SOL in setup deposits", "Balance breakdown unavailable", "No fills this run", "Last recorded balances. Waiting for fresh status.",
			"integer(micros)>0n&&integer(micros)<10000n?'<$0.01'",
			"amount>=1000000n?2:amount>=10000n?4:6",
			"marketPriceChart", "LightweightCharts.createChart", "chartSegments", "View exact chart values", "data-chart-action=\"zoom-in\"", "activeChart.remove()", "Bot strategy", "If simply held", "Ahead by ", "Behind by ", "older events omitted", "Proposal ready", "Nous Hermes",
			"chartPointAvailable", "key!=='price_micros'||integer(point[key])>0n", "m.state==='waiting for data'", "price-values", "performance-values", "pnl===0n?'→'", "Paper values')+' unavailable", "readout.innerHTML=original",
			"Rejected output", "No valid run yet", "Deterministic replay gates alone decide whether any paper plan may change.", "open-order-history", "Starting trade lot", "Loss pause",
			"Minimum opportunity", "saveInstruction", "X-Mithril-Paper-Request",
			"Fee budget left", "Orders left this session", "No more orders this run",
			"Orders paused until tomorrow",
			"Total traded this session", "Modeled fees this session", "renderActiveLimits",
			"Largest market", "of the starting account", "Active order range",
			"This paper setup is active in the configurable spot markets", "Independent paper markets keep their own test setup", "Saved. The configurable spot paper services are validating and applying this setup",
			"current.instruction_active", "instruction-cadence", "instruction-drawdown",
			"Save experiment request", "validInstructionRequest",
			"decisionReason", "marketStatus", "Fixed paper plan", "const watching=", "const deciding=",
			"Limited price data", "coverage_bps", "marketDataHealthy", "rememberChartRange",
			"Math.hypot", "openChartDetail", "openDetails", "helpReturn", "captureRenderFocus", "updateTabOrientation", "ArrowDown", "ArrowUp",
			"selectedMarketName", "market-choice", "aria-controls=\"markets\"", "spotMarkets.find(market=>market.name===selectedMarketName)",
			"if(changed)window.scrollTo(0,0)",
			"activity-more", "strategy-list-head", "automation-list-head",
			"renderMarketResearch", "Ready for short check", "Usable in window", "Buy-and-sell cost", "Round-trip time",
			"Not enough data", "Plan did not pass", "Ready for paper test", "Untouched replay", "Higher-cost replay",
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
	if strings.Contains(appJS, "chartPaths") || strings.Contains(appJS, "chartDots") ||
		strings.Contains(appJS, "<polyline") || strings.Contains(appJS, "<svg viewBox=\"0 0 100 56\"") {
		t.Fatal("custom chart SVG remains in /app.js")
	}
	if strings.Contains(appJS, "fraction.length>9") {
		t.Fatal("asset balances are truncated to zero below nine decimal places")
	}
	css := dashboardCSS
	for _, obsolete := range []string{".chart svg", ".chart-grid", ".chart-paper", ".chart-hold", ".chart-market", ".chart-hit", ".coverage-ring", ".has-visual"} {
		if strings.Contains(css, obsolete) {
			t.Errorf("obsolete custom chart style %q remains", obsolete)
		}
	}
	if strings.Contains(indexHTML, "unsafe-inline") || strings.Contains(indexHTML, "unsafe-eval") ||
		strings.Contains(appJS, "style=\"") {
		t.Fatal("interactive chart weakens CSP compatibility")
	}
	for _, legacy := range []string{".market-meta", ".market-stat-rail", ".market-bottom", ".market-overview", ".decision-flow"} {
		if strings.Contains(dashboardCSS, legacy) || strings.Contains(appJS, legacy) {
			t.Fatalf("legacy dashboard structure %q remains", legacy)
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
