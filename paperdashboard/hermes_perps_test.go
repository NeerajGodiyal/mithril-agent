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
