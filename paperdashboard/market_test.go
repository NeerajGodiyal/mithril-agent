package paperdashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/marketadmission"
	"github.com/Overclock-Validator/mithril-agent/paperstatus"
)

func TestMarketAdmissionProjectionIsFixedMinimalAndAtomic(t *testing.T) {
	root := protectedTestDirectory(t)
	credentials := filepath.Join(root, "credentials")
	output := filepath.Join(root, "dashboard", "market-admission.json")
	if err := os.MkdirAll(credentials, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 4, 8, 0, 30, 0, time.UTC)
	for _, item := range marketAdmissionCredentials {
		status := dashboardStatus(item.market, now)
		if item.market == marketadmission.MarketWIFUSDC {
			var err error
			status, err = status.WithPaperCheck(marketadmission.DashboardPaperCheck{
				Version: marketadmission.DashboardPaperCheckVersion,
				Market:  item.market, CheckedAt: now, Through: status.Diagnostic.Through,
				Outcome:             marketadmission.DashboardPaperOutcomeCandidateRejected,
				TrainingCoverageBPS: 9_800, HoldoutCoverageBPS: 9_700,
				HoldoutAfterCostNetReturnMicros:  -25_000,
				HoldoutAfterCostVersusHoldMicros: -10_000,
				StressAfterCostNetReturnMicros:   -40_000,
				StressAfterCostVersusHoldMicros:  -30_000,
				CandidatesEvaluated:              12,
				TrainingRejections: marketadmission.DashboardPaperTrainingRejections{
					RejectedCandidates: 11, NoRoundTrip: 7, DidNotBeatHolding: 5,
				},
				Reasons: []string{"holdout_net_return_not_positive"},
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		writeMarketCredential(t, credentials, item.credential, status)
	}
	if err := RecordMarketAdmission(output, credentials, now); err != nil {
		t.Fatal(err)
	}
	markets, err := readMarketAdmission(output, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(markets) != 3 || markets[0].Market != marketadmission.MarketWIFUSDC ||
		!markets[0].Fresh || markets[0].AvailableBuckets != 9 || markets[0].ObservedBuckets != 10 ||
		markets[0].PaperCheck == nil ||
		markets[0].PaperCheck.Outcome != marketadmission.DashboardPaperOutcomeCandidateRejected ||
		markets[0].PaperCheck.HoldoutAfterCostNetReturnMicros != -25_000 ||
		markets[0].PaperCheck.CandidatesEvaluated != 12 ||
		markets[0].PaperCheck.TrainingRejections.RejectedCandidates != 11 ||
		markets[0].PaperCheck.TrainingRejections.NoRoundTrip != 7 {
		t.Fatalf("market projection = %+v", markets)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		`"observe":`, `"base_mint"`, `"quote_mint"`, `"provider"`, `"journal"`, `"chain_head"`,
		`"policy"`, `"authorized":true`, `"promotable":true`,
	} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("projection exposes %q: %s", forbidden, raw)
		}
	}
	before := append([]byte(nil), raw...)
	if err := os.WriteFile(filepath.Join(credentials, "market-jto-status"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecordMarketAdmission(output, credentials, now.Add(time.Minute)); err == nil {
		t.Fatal("invalid credential replaced the projection")
	}
	after, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed record changed the last valid projection")
	}
	viewRaw, err := json.Marshal(markets)
	if err != nil || !strings.Contains(string(viewRaw), `"holdout_after_cost_net_return_micros":"-25000"`) {
		t.Fatalf("public paper-check amounts are not precision-safe: %s, %v", viewRaw, err)
	}
}

func TestMarketAdmissionFreshnessIsPerCollectorAndDoesNotAffectHealth(t *testing.T) {
	root := protectedTestDirectory(t)
	credentials := filepath.Join(root, "credentials")
	output := filepath.Join(root, "market-admission.json")
	if err := os.MkdirAll(credentials, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 4, 8, 0, 30, 0, time.UTC)
	for index, item := range marketAdmissionCredentials {
		updated := now
		status := dashboardStatus(item.market, updated)
		if index == 1 {
			updated = now.Add(-4 * time.Minute)
			status = dashboardStatus(item.market, updated)
			status.Diagnostic.ObservedBuckets = status.Diagnostic.ExpectedBuckets
			status.Diagnostic.AvailableBuckets = status.Diagnostic.ExpectedBuckets
			status.Diagnostic.AvailabilityBPS = 10_000
			status.Diagnostic.FailureCounts = map[string]uint64{}
		}
		writeMarketCredential(t, credentials, item.credential, status)
	}
	if err := RecordMarketAdmission(output, credentials, now); err != nil {
		t.Fatal(err)
	}
	server, err := New([]Source{&sourceStub{label: "SOL/USDC", snapshot: paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: now, Current: "PAPER · Watching",
		Summary: &paperstatus.CurrentSummary{
			Market: "SOL/USDC", ValueUnit: "USD", Day: "2026-09-04", TickSeconds: 60,
			OpeningEquityMicros: 1_000_000, EquityMicros: 1_000_000,
			HoldBenchmarkMicros: 1_000_000, Checks: 1,
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	if err := server.EnableMarketAdmission(output); err != nil {
		t.Fatal(err)
	}
	view := server.readSnapshot(now)
	if !view.Complete || view.MarketResearchError || len(view.MarketResearch) != 3 ||
		!view.MarketResearch[0].Fresh || view.MarketResearch[1].Fresh ||
		view.MarketResearch[0].WindowHours != marketadmission.DashboardStatusWindowHours ||
		view.MarketResearch[1].ReadyForPaperCheck || !view.MarketResearch[2].Fresh {
		t.Fatalf("view = %+v", view)
	}
	if err := os.WriteFile(output, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	view = server.readSnapshot(now)
	if !view.Complete || !view.MarketResearchError || view.Overview.EquityMicros != 1_000_000 {
		t.Fatalf("invalid research status damaged active paper health: %+v", view)
	}
}

func TestMarketAdmissionExplainsACompleteWindowThatDidNotPass(t *testing.T) {
	root := protectedTestDirectory(t)
	credentials := filepath.Join(root, "credentials")
	output := filepath.Join(root, "market-admission.json")
	if err := os.MkdirAll(credentials, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 4, 8, 0, 30, 0, time.UTC)
	for _, item := range marketAdmissionCredentials {
		status := dashboardStatus(item.market, now)
		if item.market == marketadmission.MarketJTOUSDC {
			status.Diagnostic.ObservedBuckets = status.Diagnostic.ExpectedBuckets
			status.Diagnostic.AvailableBuckets = status.Diagnostic.ExpectedBuckets
			status.Diagnostic.AvailabilityBPS = 10_000
			status.Diagnostic.MedianRouteCostBPS = marketadmission.DefaultThresholds().MedianRouteCostBPS + 7
			status.Diagnostic.P95RouteCostBPS = marketadmission.DefaultThresholds().P95RouteCostBPS
			status.Diagnostic.FailureCounts = map[string]uint64{}
		}
		writeMarketCredential(t, credentials, item.credential, status)
	}
	if err := RecordMarketAdmission(output, credentials, now); err != nil {
		t.Fatal(err)
	}
	markets, err := readMarketAdmission(output, now)
	if err != nil {
		t.Fatal(err)
	}
	jto := markets[1]
	if jto.ReadyForPaperCheck || len(jto.PaperCheckGateReasons) != 1 ||
		jto.PaperCheckGateReasons[0] != "median round-trip route cost exceeds the limit" ||
		jto.MedianRouteCostLimitBPS != marketadmission.DefaultThresholds().MedianRouteCostBPS ||
		jto.P95RouteCostLimitBPS != marketadmission.DefaultThresholds().P95RouteCostBPS {
		t.Fatalf("JTO research = %+v", jto)
	}
}

func protectedTestDirectory(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeMarketCredential(
	t *testing.T, directory, name string, status marketadmission.DashboardStatus,
) {
	t.Helper()
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func dashboardStatus(market string, updatedAt time.Time) marketadmission.DashboardStatus {
	through := updatedAt.UTC().Truncate(time.Minute)
	return marketadmission.DashboardStatus{
		Version: marketadmission.Version, Kind: marketadmission.DashboardStatusKind,
		Market: market, UpdatedAt: updatedAt.UTC(), WindowHours: marketadmission.DashboardStatusWindowHours,
		Diagnostic: marketadmission.Diagnostic{
			Version: marketadmission.Version, Market: market,
			From: through.Add(-2 * time.Hour), Through: through,
			DiagnosticOnly: true, ExpectedBuckets: 120, ObservedBuckets: 10,
			AvailableBuckets: 9, AvailabilityBPS: 750,
			MedianRouteCostBPS: 8, P95RouteCostBPS: 12,
			MedianQuoteLatencyMillis: 400, P95QuoteLatencyMillis: 700,
			FailureCounts: map[string]uint64{"missing_bucket": 110, "mint_state_unavailable": 1},
		},
	}
}
