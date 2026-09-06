package paperdashboard

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/paperstatus"
)

func hermesPerpsFixture(now time.Time) map[string]any {
	return map[string]any{
		"version": 1, "paper_only": true, "authorized": false, "promotable": false,
		"run_id": strings.Repeat("a", 32), "finished_at": now.Format(time.RFC3339Nano),
		"markets": []any{map[string]any{
			"symbol": "SOL", "status": "pending_advisory", "target_episode": "2",
			"strategy": "regime", "risk_arm": "conservative",
			"context_sha256": strings.Repeat("b", 64), "proposal_sha256": strings.Repeat("c", 64),
			"training_tapes": 8, "resolved_outcomes": 0,
		}},
	}
}

func writeHermesPerpsFixture(t *testing.T, path string, value map[string]any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func hermesLifecycleFixture(now time.Time) (map[string]any, map[string]any, map[string]any) {
	value := hermesPerpsFixture(now)
	proposal := map[string]any{"symbol": "SOL", "proposal_sha256": strings.Repeat("d", 64), "target_episode": "2", "frozen_at": now.Add(-time.Hour).Format(time.RFC3339Nano), "evaluation_status": "pending", "evaluation_observed_at": now.Format(time.RFC3339Nano), "selection_status": "paused"}
	lifecycle := map[string]any{"as_of": now.Format(time.RFC3339Nano), "selection_enabled": false, "markets": []any{
		map[string]any{"symbol": "SOL", "recorded_proposals": 1, "manual_reconciliation_required": false},
		map[string]any{"symbol": "BTC", "recorded_proposals": 0, "manual_reconciliation_required": true},
		map[string]any{"symbol": "ETH", "recorded_proposals": 0, "manual_reconciliation_required": false},
	}, "proposals": []any{proposal}}
	value["lifecycle"] = lifecycle
	return value, lifecycle, proposal
}

func TestHermesPerpsLifecycleStagesAndHistoricalSelection(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "perps-proposals.json")
	for _, stage := range []string{"paused", "not_attempted", "not_selected", "selected_previously", "retired", "needs_attention", "unevaluable", "unavailable"} {
		value, lifecycle, row := hermesLifecycleFixture(now)
		switch stage {
		case "not_attempted":
			lifecycle["selection_enabled"] = true
			row["selection_status"] = stage
		case "not_selected", "selected_previously", "retired":
			row["evaluation_status"], row["evaluation_sha256"], row["selection_status"] = "evaluated", strings.Repeat("e", 64), stage
			if stage != "not_selected" {
				row["plan_sha256"] = strings.Repeat("f", 64)
			}
		case "needs_attention":
			row["selection_status"] = stage
			lifecycle["markets"].([]any)[0].(map[string]any)["manual_reconciliation_required"] = true
		case "unevaluable":
			row["evaluation_status"], row["evaluation_sha256"], row["selection_status"] = stage, strings.Repeat("e", 64), "not_selected"
		case "unavailable":
			row["evaluation_status"] = stage
			delete(row, "evaluation_observed_at")
		}
		writeHermesPerpsFixture(t, path, value)
		view, err := readHermesPerps(path, now)
		if err != nil || view.Lifecycle == nil || len(view.Lifecycle.Proposals) != 1 || !*view.Lifecycle.Markets[1].ManualReconciliationRequired {
			t.Fatalf("stage %s rejected: %+v %v", stage, view, err)
		}
	}
	value := hermesPerpsFixture(now)
	value["lifecycle_error"] = true
	writeHermesPerpsFixture(t, path, value)
	view, err := readHermesPerps(path, now)
	if err != nil || !view.LifecycleError || view.Lifecycle != nil {
		t.Fatalf("independent lifecycle error rejected: %+v %v", view, err)
	}
}

func TestHermesPerpsLifecycleRejectsMalformedStageOrEvidence(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	mutations := map[string]func(map[string]any, map[string]any, map[string]any){
		"future_asof":      func(v, l, p map[string]any) { l["as_of"] = now.Add(time.Nanosecond).Format(time.RFC3339Nano) },
		"zero_asof":        func(v, l, p map[string]any) { l["as_of"] = "0001-01-01T00:00:00Z" },
		"missing_enabled":  func(v, l, p map[string]any) { delete(l, "selection_enabled") },
		"missing_markets":  func(v, l, p map[string]any) { l["markets"] = []any{} },
		"duplicate_market": func(v, l, p map[string]any) { m := l["markets"].([]any); m[1] = m[0] },
		"many_recorded":    func(v, l, p map[string]any) { l["markets"].([]any)[0].(map[string]any)["recorded_proposals"] = 257 },
		"missing_count":    func(v, l, p map[string]any) { delete(l["markets"].([]any)[0].(map[string]any), "recorded_proposals") },
		"missing_manual": func(v, l, p map[string]any) {
			delete(l["markets"].([]any)[0].(map[string]any), "manual_reconciliation_required")
		},
		"row_exceeds_count":  func(v, l, p map[string]any) { l["markets"].([]any)[0].(map[string]any)["recorded_proposals"] = 0 },
		"null_rows":          func(v, l, p map[string]any) { l["proposals"] = nil },
		"duplicate_proposal": func(v, l, p map[string]any) { l["proposals"] = []any{p, p} },
		"symbol":             func(v, l, p map[string]any) { p["symbol"] = "JUP" },
		"target":             func(v, l, p map[string]any) { p["target_episode"] = "02" },
		"hash":               func(v, l, p map[string]any) { p["proposal_sha256"] = "bad" },
		"future_frozen":      func(v, l, p map[string]any) { p["frozen_at"] = now.Add(time.Second).Format(time.RFC3339Nano) },
		"observation_before_freeze": func(v, l, p map[string]any) {
			p["evaluation_observed_at"] = now.Add(-2 * time.Hour).Format(time.RFC3339Nano)
		},
		"future_observation": func(v, l, p map[string]any) {
			p["evaluation_observed_at"] = now.Add(time.Nanosecond).Format(time.RFC3339Nano)
		},
		"pending_digest":          func(v, l, p map[string]any) { p["evaluation_sha256"] = strings.Repeat("e", 64) },
		"pending_unobserved":      func(v, l, p map[string]any) { delete(p, "evaluation_observed_at") },
		"terminal_no_digest":      func(v, l, p map[string]any) { p["evaluation_status"] = "evaluated" },
		"unavailable_observation": func(v, l, p map[string]any) { p["evaluation_status"] = "unavailable" },
		"evaluation_status":       func(v, l, p map[string]any) { p["evaluation_status"] = "profitable" },
		"selected_pending": func(v, l, p map[string]any) {
			p["selection_status"], p["plan_sha256"] = "selected_previously", strings.Repeat("f", 64)
		},
		"retired_unevaluable": func(v, l, p map[string]any) {
			p["evaluation_status"], p["evaluation_sha256"], p["selection_status"], p["plan_sha256"] = "unevaluable", strings.Repeat("e", 64), "retired", strings.Repeat("f", 64)
		},
		"selected_missing_plan": func(v, l, p map[string]any) {
			p["evaluation_status"], p["evaluation_sha256"], p["selection_status"] = "evaluated", strings.Repeat("e", 64), "selected_previously"
		},
		"unexpected_plan":           func(v, l, p map[string]any) { p["plan_sha256"] = strings.Repeat("f", 64) },
		"attention_without_warning": func(v, l, p map[string]any) { p["selection_status"] = "needs_attention" },
		"paused_enabled":            func(v, l, p map[string]any) { l["selection_enabled"] = true },
		"attempting_disabled":       func(v, l, p map[string]any) { p["selection_status"] = "not_attempted" },
		"prose":                     func(v, l, p map[string]any) { p["rationale"] = "model prose" },
		"error_and_history":         func(v, l, p map[string]any) { v["lifecycle_error"] = true },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			value, lifecycle, row := hermesLifecycleFixture(now)
			mutate(value, lifecycle, row)
			path := filepath.Join(t.TempDir(), "perps-proposals.json")
			writeHermesPerpsFixture(t, path, value)
			if view, err := readHermesPerps(path, now); err == nil || view != nil {
				t.Fatalf("invalid lifecycle accepted: %+v %v", view, err)
			}
		})
	}
}

func TestHermesPerpsLifecycleNineRowsStayBoundedAndOrdered(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	value, lifecycle, _ := hermesLifecycleFixture(now)
	var rows []any
	for i, symbol := range []string{"SOL", "BTC", "ETH"} {
		lifecycle["markets"].([]any)[i].(map[string]any)["recorded_proposals"] = 256
		for j := 0; j < 3; j++ {
			rows = append(rows, map[string]any{"symbol": symbol, "proposal_sha256": strings.Repeat(string(rune('1'+i*3+j)), 64), "target_episode": string(rune('1' + j)), "frozen_at": now.Add(time.Duration(j-3) * time.Hour).Format(time.RFC3339Nano), "evaluation_status": "pending", "evaluation_observed_at": now.Format(time.RFC3339Nano), "selection_status": "paused"})
		}
	}
	lifecycle["proposals"] = rows
	path := filepath.Join(t.TempDir(), "perps-proposals.json")
	writeHermesPerpsFixture(t, path, value)
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) >= 16<<10 {
		t.Fatalf("bounded projection size=%d: %v", len(raw), err)
	}
	if view, err := readHermesPerps(path, now); err != nil || len(view.Lifecycle.Proposals) != 9 {
		t.Fatalf("nine rows rejected: %+v %v", view, err)
	}
	rows[0], rows[1] = rows[1], rows[0]
	writeHermesPerpsFixture(t, path, value)
	if _, err := readHermesPerps(path, now); err == nil {
		t.Fatal("reordered original chronology accepted")
	}
	rows[0], rows[1] = rows[1], rows[0]
	rows[3].(map[string]any)["symbol"] = "SOL"
	rows[3].(map[string]any)["frozen_at"] = now.Format(time.RFC3339Nano)
	writeHermesPerpsFixture(t, path, value)
	if _, err := readHermesPerps(path, now); err == nil {
		t.Fatal("four rows for one symbol accepted")
	}
}

func TestHermesPerpsProjectsOnlyLastRecordedAttempt(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "perps-proposals.json")
	writeHermesPerpsFixture(t, path, hermesPerpsFixture(now))
	view, err := readHermesPerps(path, now.Add(24*time.Hour))
	if err != nil || !view.FinishedAt.Equal(now) || len(view.Markets) != 1 || view.Markets[0].TargetEpisode != "2" || view.Markets[0].ResolvedOutcomes == nil || *view.Markets[0].ResolvedOutcomes != 0 {
		t.Fatalf("projection=%+v err=%v", view, err)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"run_id", "authorized", "promotable", "current", "active", "rationale", "path", strings.Repeat("a", 32)} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("projection leaked %q", forbidden)
		}
	}
	if !bytes.Contains(raw, []byte(`"resolved_outcomes":0`)) {
		t.Fatal("zero resolved outcomes disappeared")
	}
	if bytes.Contains(raw, []byte("lifecycle")) {
		t.Fatal("legacy projection gained optional lifecycle fields")
	}
}

func TestHermesPerpsExistingProposalDoesNotClaimFreshResearch(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "perps-proposals.json")
	fixture := func() (map[string]any, map[string]any) {
		value := hermesPerpsFixture(now)
		market := value["markets"].([]any)[0].(map[string]any)
		market["status"] = "already_saved"
		market["frozen_at"] = now.Add(-time.Hour).Format(time.RFC3339Nano)
		delete(market, "training_tapes")
		delete(market, "resolved_outcomes")
		return value, market
	}
	for _, withContext := range []bool{true, false} {
		value, market := fixture()
		if !withContext {
			delete(market, "context_sha256")
		}
		writeHermesPerpsFixture(t, path, value)
		view, err := readHermesPerps(path, now)
		if err != nil || view.Markets[0].Status != "already_saved" || view.Markets[0].FrozenAt == nil {
			t.Fatalf("existing receipt rejected: %+v %v", view, err)
		}
	}
	for field, invalid := range map[string]any{
		"frozen_at":      now.Add(time.Second).Format(time.RFC3339Nano),
		"training_tapes": 1, "resolved_outcomes": 0, "context_sha256": "invalid",
		"phase": "model_proposal", "proposal_sha256": "", "risk_arm": "unlimited",
	} {
		value, market := fixture()
		market[field] = invalid
		writeHermesPerpsFixture(t, path, value)
		if _, err := readHermesPerps(path, now); err == nil {
			t.Fatalf("existing receipt accepted invalid %s", field)
		}
	}
	value, market := fixture()
	delete(market, "frozen_at")
	writeHermesPerpsFixture(t, path, value)
	if _, err := readHermesPerps(path, now); err == nil {
		t.Fatal("existing receipt lost original save time")
	}
}

func TestHermesPerpsRejectsMalformedProjection(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(map[string]any, map[string]any){
		"version":           func(v, m map[string]any) { v["version"] = 2 },
		"authority":         func(v, m map[string]any) { v["authorized"] = true },
		"missing_authority": func(v, m map[string]any) { delete(v, "authorized") },
		"promotion":         func(v, m map[string]any) { v["promotable"] = true },
		"not_paper":         func(v, m map[string]any) { v["paper_only"] = false },
		"run_id":            func(v, m map[string]any) { v["run_id"] = strings.Repeat("A", 32) },
		"zero_time":         func(v, m map[string]any) { v["finished_at"] = "0001-01-01T00:00:00Z" },
		"future":            func(v, m map[string]any) { v["finished_at"] = now.Add(time.Nanosecond).Format(time.RFC3339Nano) },
		"too_many":          func(v, m map[string]any) { v["markets"] = []any{m, m, m, m} },
		"empty":             func(v, m map[string]any) { v["markets"] = []any{} },
		"duplicate":         func(v, m map[string]any) { v["markets"] = []any{m, m} },
		"symbol":            func(v, m map[string]any) { m["symbol"] = "JUP" },
		"status":            func(v, m map[string]any) { m["status"] = "trading" },
		"phase":             func(v, m map[string]any) { m["phase"] = "model_proposal" },
		"target_zero":       func(v, m map[string]any) { m["target_episode"] = "0" },
		"target_alias":      func(v, m map[string]any) { m["target_episode"] = "02" },
		"target_number":     func(v, m map[string]any) { m["target_episode"] = 2 },
		"context_hash":      func(v, m map[string]any) { m["context_sha256"] = "not-a-hash" },
		"proposal_hash":     func(v, m map[string]any) { delete(m, "proposal_sha256") },
		"strategy":          func(v, m map[string]any) { m["strategy"] = "model text" },
		"risk":              func(v, m map[string]any) { m["risk_arm"] = "unlimited" },
		"zero_training":     func(v, m map[string]any) { m["training_tapes"] = 0 },
		"many_training":     func(v, m map[string]any) { m["training_tapes"] = 9 },
		"many_outcomes":     func(v, m map[string]any) { m["resolved_outcomes"] = 9 },
		"missing_outcomes":  func(v, m map[string]any) { delete(m, "resolved_outcomes") },
		"negative":          func(v, m map[string]any) { m["resolved_outcomes"] = -1 },
		"prose":             func(v, m map[string]any) { m["rationale"] = "untrusted model text" },
		"private_path":      func(v, m map[string]any) { v["path"] = "/private/state" },
	} {
		t.Run(name, func(t *testing.T) {
			value := hermesPerpsFixture(now)
			mutate(value, value["markets"].([]any)[0].(map[string]any))
			path := filepath.Join(t.TempDir(), "perps-proposals.json")
			writeHermesPerpsFixture(t, path, value)
			if view, err := readHermesPerps(path, now); err == nil || view != nil {
				t.Fatalf("invalid projection accepted: %+v %v", view, err)
			}
		})
	}
}

func TestHermesPerpsFailurePhasesCannotClaimProposal(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "perps-proposals.json")
	for _, status := range []string{"unavailable", "cleanup_required", "interrupted"} {
		for _, phase := range []string{"prepare_directories", "check_reservation", "prepare_context", "model_proposal", "export_session", "verify_model_output", "freeze_proposal", "record_invocation"} {
			value := hermesPerpsFixture(now)
			market := map[string]any{"symbol": "ETH", "status": status, "phase": phase}
			value["markets"] = []any{market}
			writeHermesPerpsFixture(t, path, value)
			if _, err := readHermesPerps(path, now); err != nil {
				t.Fatalf("valid %s/%s rejected: %v", status, phase, err)
			}
			market["proposal_sha256"] = strings.Repeat("c", 64)
			writeHermesPerpsFixture(t, path, value)
			if _, err := readHermesPerps(path, now); err == nil {
				t.Fatal("failure claimed a proposal")
			}
		}
	}
}

func TestHermesPerpsOptionalSiblingDoesNotDegradeHealth(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	server, err := New([]Source{&sourceStub{label: "SOL/USDC", snapshot: paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: now, Current: "PAPER · Watching",
		Summary: &paperstatus.CurrentSummary{Market: "SOL/USDC", ValueUnit: "USD", Day: "2026-09-05", TickSeconds: 60,
			OpeningEquityMicros: 1, EquityMicros: 1, HoldBenchmarkMicros: 1, Checks: 1, PriceMicros: 100_000_000, State: "watching"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return now }
	directory := t.TempDir()
	if err := server.EnableResearch(filepath.Join(directory, "research.json")); err != nil {
		t.Fatal(err)
	}
	view := server.snapshot()
	if !view.Complete || view.HermesPerps != nil || view.HermesPerpsError {
		t.Fatalf("missing projection changed health: %+v", view)
	}
	path := filepath.Join(directory, "perps-proposals.json")
	writeHermesPerpsFixture(t, path, hermesPerpsFixture(now))
	view = server.snapshotWithRefresh(true)
	if !view.Complete || view.HermesPerps == nil || view.HermesPerpsError {
		t.Fatalf("valid sibling unavailable: %+v", view)
	}
	for _, raw := range []string{`{"version":1,"version":1}`, strings.Repeat(" ", (16<<10)+1)} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		view = server.snapshotWithRefresh(true)
		if !view.Complete || view.HermesPerps != nil || !view.HermesPerpsError || view.ResearchError {
			t.Fatalf("invalid projection affected market health: %+v", view)
		}
	}
	writeHermesPerpsFixture(t, path, hermesPerpsFixture(now))
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readHermesPerps(path, now); err == nil {
		t.Fatal("publicly readable projection accepted")
	}
}
