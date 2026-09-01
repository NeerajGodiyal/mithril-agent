package paperstatus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

func TestWriterIsPrivateBoundedAndDeduplicated(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "alerts.json")
	writer, err := OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	for index := 0; index < MaxEvents+2; index++ {
		if err := writer.Append(start.Add(time.Duration(index)*time.Second), KindOrderFilled,
			string(rune('a'+index)), "PAPER · Filled"); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Append(start.Add((MaxEvents+1)*time.Second), KindOrderFilled,
		string(rune('a'+MaxEvents+1)), "PAPER · Duplicate"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
	data, err := securefile.ReadPrivate(path, maxSnapshotBytes)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot Snapshot
	if err := strictjson.Decode(data, &snapshot); err != nil || ValidateSnapshot(snapshot) != nil {
		t.Fatalf("invalid snapshot: %v", err)
	}
	if len(snapshot.Events) != MaxEvents || snapshot.DroppedEvents != 2 ||
		snapshot.Events[0].At != start.Add(2*time.Second) {
		t.Fatalf("events=%d dropped=%d first=%s", len(snapshot.Events), snapshot.DroppedEvents, snapshot.Events[0].At)
	}
	gap, ok := TruncationEvent(snapshot)
	if !ok || gap.Kind != "history_truncated" || !strings.Contains(gap.Message, "journal") {
		t.Fatalf("truncation event = %+v, %v", gap, ok)
	}
}

func TestWriterRejectsUnsafeOrMalformedEvents(t *testing.T) {
	if _, err := OpenWriter("relative.json"); err == nil {
		t.Fatal("relative path accepted")
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(filepath.Join(directory, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		kind, key, message string
	}{
		{"unknown", "key", "PAPER SIMULATION — test"},
		{KindOrderFilled, "", "PAPER SIMULATION — test"},
		{KindOrderFilled, "key", "looks live"},
		{KindOrderFilled, "key", "PAPER SIMULATION — disclaimer omitted"},
	} {
		if err := writer.Append(time.Now().UTC(), test.kind, test.key, test.message); err == nil {
			t.Fatalf("accepted %+v", test)
		}
	}
}

func TestWriterUpdatesCurrentWithoutCreatingAnAlert(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "alerts.json")
	writer, err := OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	if err := writer.Append(start, KindStrategyActive, "active", "PAPER · Strategy on"); err != nil {
		t.Fatal(err)
	}
	current := "PAPER · 👀 Watching\nRange · signal -2 bps · need 14 · SOL $106.55"
	summary := &CurrentSummary{
		Market: "SOL/USDC", ValueUnit: "USD", Day: "2026-08-30", TickSeconds: 60,
		OpeningEquityMicros: 100_000_000, EquityMicros: 101_000_000,
		HoldBenchmarkMicros: 100_500_000, Checks: 10, Signals: 2, Trades: 1,
		AccountingTracked: true, RealizedMicros: 400_000, UnrealizedMicros: 600_000,
		Unobservable: 1, Missed: 1, PriceMicros: 106_550_000, State: "range",
		DrawdownMicros: 250_000, MaxDrawdownMicros: 500_000,
		Strategy: "adaptive", NextAction: "sell", DecisionReason: "signal_below_cost_hurdle",
		InitialLotUnits: 246_000_000, InitialLotDecimals: 9, InitialLotAsset: "SOL",
		FeeReserveLamports: 32_000_000, FeeLamports: 100_000, FeeBudgetTracked: true,
		RemainingFeeReserveLamports: 29_000_000, EstimatedFillsRemaining: 290,
		SlippageBPS: 100, SettleSeconds: 60,
		FastWindow: 5, SlowWindow: 20, MinimumSignalBPS: 20,
		MaxVolatilityBPS: 500, MaxQuoteImpactBPS: 500, MaxDrawdownBPS: 300,
		CooldownSeconds: 300,
	}
	if err := writer.UpdateCurrentSummary(start.Add(time.Second), current, summary); err != nil {
		t.Fatal(err)
	}
	data, err := securefile.ReadPrivate(path, maxSnapshotBytes)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot Snapshot
	if err := strictjson.Decode(data, &snapshot); err != nil || ValidateSnapshot(snapshot) != nil {
		t.Fatalf("invalid snapshot: %v", err)
	}
	if snapshot.Current != current || snapshot.Summary == nil ||
		snapshot.Summary.Market != "SOL/USDC" || len(snapshot.Events) != 1 ||
		len(snapshot.History) != 1 || snapshot.History[0].EquityMicros != 101_000_000 ||
		snapshot.History[0].PriceMicros != 106_550_000 ||
		!snapshot.ObservedAt.Equal(start.Add(time.Second)) {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if err := writer.Append(start.Add(2*time.Second), KindOrderFilled, "fill", "PAPER · Filled"); err != nil {
		t.Fatal(err)
	}
	data, err = securefile.ReadPrivate(path, maxSnapshotBytes)
	if err != nil {
		t.Fatal(err)
	}
	snapshot = Snapshot{}
	if err := strictjson.Decode(data, &snapshot); err != nil || snapshot.Current != "" ||
		snapshot.Summary != nil ||
		len(snapshot.History) != 1 ||
		!snapshot.ObservedAt.Equal(start.Add(2*time.Second)) {
		t.Fatalf("new alert did not replace stale current status: %+v err=%v", snapshot, err)
	}
	if err := writer.UpdateCurrent(start.Add(3*time.Second), current); err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(start.Add(3*time.Second), KindPeriodClosed, "close", "PAPER · Day closed"); err != nil {
		t.Fatal(err)
	}
	data, err = securefile.ReadPrivate(path, maxSnapshotBytes)
	if err != nil {
		t.Fatal(err)
	}
	snapshot = Snapshot{}
	if err := strictjson.Decode(data, &snapshot); err != nil || snapshot.Current != "" {
		t.Fatalf("same-time alert did not replace current status: %+v err=%v", snapshot, err)
	}
	if err := writer.UpdateCurrent(start, current); err == nil {
		t.Fatal("accepted a current status timestamp regression")
	}
	if err := writer.UpdateCurrent(start.Add(2*time.Second), "looks live"); err == nil {
		t.Fatal("accepted ambiguous current status")
	}
	bad := *summary
	bad.Trades = bad.Signals + 1
	if err := writer.UpdateCurrentSummary(start.Add(4*time.Second), current, &bad); err == nil {
		t.Fatal("accepted an inconsistent numeric current summary")
	}
	bad = *summary
	bad.DrawdownMicros = bad.MaxDrawdownMicros + 1
	if err := writer.UpdateCurrentSummary(start.Add(4*time.Second), current, &bad); err == nil {
		t.Fatal("accepted a current drawdown above the period maximum")
	}
	bad = *summary
	bad.UnrealizedMicros++
	if err := writer.UpdateCurrentSummary(start.Add(4*time.Second), current, &bad); err == nil {
		t.Fatal("accepted accounting that does not reconcile to the paper account")
	}
	bad = *summary
	bad.TickSeconds = 0
	if err := writer.UpdateCurrentSummary(start.Add(4*time.Second), current, &bad); err == nil {
		t.Fatal("accepted a current summary with no observation cadence")
	}
	bad = *summary
	bad.State = "buy everything"
	if err := writer.UpdateCurrentSummary(start.Add(4*time.Second), current, &bad); err == nil {
		t.Fatal("accepted an unsupported current state")
	}
	bad = *summary
	bad.Strategy = "magic"
	if err := writer.UpdateCurrentSummary(start.Add(4*time.Second), current, &bad); err == nil {
		t.Fatal("accepted an unsupported current strategy")
	}
	bad = *summary
	bad.NextAction = "leverage"
	if err := writer.UpdateCurrentSummary(start.Add(4*time.Second), current, &bad); err == nil {
		t.Fatal("accepted an unsupported next action")
	}
	bad = *summary
	bad.DecisionReason = "llm_said_buy"
	if err := writer.UpdateCurrentSummary(start.Add(4*time.Second), current, &bad); err == nil {
		t.Fatal("accepted an unsupported paper decision reason")
	}
	bad = *summary
	bad.ValueUnit = "BTC"
	if err := writer.UpdateCurrentSummary(start.Add(4*time.Second), current, &bad); err == nil {
		t.Fatal("accepted an unsupported paper value unit")
	}
	bad = *summary
	bad.Unobservable = bad.Checks + 1
	if err := writer.UpdateCurrentSummary(start.Add(4*time.Second), current, &bad); err == nil {
		t.Fatal("accepted impossible market-data counts")
	}
	bad = *summary
	bad.MaxDrawdownBPS = 5_001
	if err := writer.UpdateCurrentSummary(start.Add(4*time.Second), current, &bad); err == nil {
		t.Fatal("accepted paper settings outside the policy bounds")
	}
	bad = *summary
	bad.EstimatedFillsRemaining++
	if err := writer.UpdateCurrentSummary(start.Add(4*time.Second), current, &bad); err == nil {
		t.Fatal("accepted a paper fill estimate above the remaining fee budget")
	}
	bad = *summary
	bad.EstimatedFillsRemaining = 0
	if err := writer.UpdateCurrentSummary(start.Add(4*time.Second), current, &bad); err == nil {
		t.Fatal("accepted a zero paper fill estimate with a positive remaining fee budget")
	}
	bad = *summary
	bad.FeeBudgetTracked = false
	bad.RemainingFeeReserveLamports = 0
	bad.EstimatedFillsRemaining = 0
	if err := writer.UpdateCurrentSummary(start.Add(4*time.Second), current, &bad); err == nil {
		t.Fatal("accepted an untracked nonzero fee reserve")
	}
	bad = *summary
	bad.FastWindow, bad.SlowWindow = 0, 0
	bad.MinimumSignalBPS, bad.MaxVolatilityBPS = 0, 0
	bad.MaxQuoteImpactBPS, bad.MaxDrawdownBPS, bad.CooldownSeconds = 0, 0, 0
	if err := writer.UpdateCurrentSummary(start.Add(4*time.Second), current, &bad); err == nil {
		t.Fatal("accepted an adaptive strategy without adaptive limits")
	}
	bad = *summary
	bad.Strategy = "fixed"
	if err := writer.UpdateCurrentSummary(start.Add(4*time.Second), current, &bad); err == nil {
		t.Fatal("accepted fixed strategy with contradictory adaptive limits")
	}
}

func TestWriterUpgradesLegacyStatusProjection(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "alerts.json")
	start := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	legacy := Snapshot{
		Version: legacyVersion, ObservedAt: start, Current: "PAPER · Watching",
		Summary: &CurrentSummary{
			Market: "SOL/USDC", ValueUnit: "USD", Day: "2026-08-30", TickSeconds: 60,
			OpeningEquityMicros: 1, EquityMicros: 1, HoldBenchmarkMicros: 1,
		},
		Events: []Event{},
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := securefile.ReplacePrivate(path, append(encoded, '\n'), maxSnapshotBytes); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.UpdateCurrentSummary(start.Add(time.Second), "PAPER · Watching", legacy.Summary); err != nil {
		t.Fatal(err)
	}
	data, err := securefile.ReadPrivate(path, maxSnapshotBytes)
	if err != nil {
		t.Fatal(err)
	}
	var upgraded Snapshot
	if err := strictjson.Decode(data, &upgraded); err != nil || upgraded.Version != Version ||
		ValidateSnapshot(upgraded) != nil {
		t.Fatalf("upgraded snapshot = %+v, err = %v", upgraded, err)
	}
}

func TestWriterKeepsBoundedCurrentDayPerformanceHistory(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "alerts.json")
	writer, err := OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 30, 0, 1, 0, 0, time.UTC)
	summary := CurrentSummary{
		Market: "SOL/USDC", ValueUnit: "USD", Day: "2026-08-30", TickSeconds: 60,
		OpeningEquityMicros: 100_000_000, EquityMicros: 100_000_000,
		HoldBenchmarkMicros: 100_000_000, State: "watching",
	}
	for _, update := range []struct {
		at          time.Time
		equity      uint64
		state       string
		drawdown    uint64
		maxDrawdown uint64
	}{
		{start, 100_000_000, "watching", 0, 0},
		{start.Add(4 * time.Minute), 101_000_000, "watching", 0, 0},
		{start.Add(10 * time.Minute), 99_000_000, "waiting for data", 2_000_000, 2_000_000},
		{start.Add(14 * time.Minute), 99_500_000, "watching", 1_500_000, 2_000_000},
	} {
		summary.EquityMicros = update.equity
		summary.DrawdownMicros = update.drawdown
		summary.MaxDrawdownMicros = update.maxDrawdown
		summary.State = update.state
		if err := writer.UpdateCurrentSummary(update.at, "PAPER · Watching", &summary); err != nil {
			t.Fatal(err)
		}
	}
	data, err := securefile.ReadPrivate(path, maxSnapshotBytes)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot Snapshot
	if err := strictjson.Decode(data, &snapshot); err != nil || ValidateSnapshot(snapshot) != nil {
		t.Fatalf("invalid snapshot: %v", err)
	}
	if len(snapshot.History) != 2 || snapshot.History[0].EquityMicros != 101_000_000 ||
		!snapshot.History[1].Unavailable || snapshot.History[1].EquityMicros != 99_500_000 ||
		snapshot.History[1].MaxDrawdownMicros != 2_000_000 {
		t.Fatalf("history = %+v", snapshot.History)
	}

	nextDay := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	summary.Day, summary.EquityMicros, summary.State = "2026-08-31", 102_000_000, "watching"
	summary.DrawdownMicros, summary.MaxDrawdownMicros = 0, 0
	if err := writer.UpdateCurrentSummary(nextDay, "PAPER · Watching", &summary); err != nil {
		t.Fatal(err)
	}
	data, err = securefile.ReadPrivate(path, maxSnapshotBytes)
	if err != nil {
		t.Fatal(err)
	}
	snapshot = Snapshot{}
	if err := strictjson.Decode(data, &snapshot); err != nil || len(snapshot.History) != 1 ||
		!snapshot.History[0].At.Equal(nextDay) {
		t.Fatalf("new-day history = %+v err=%v", snapshot.History, err)
	}
}

func TestMaximumProjectionFitsThePrivateStatusLimit(t *testing.T) {
	day := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	snapshot := Snapshot{
		Version: Version, ObservedAt: day.Add(24*time.Hour - time.Second),
		Current: "PAPER · Watching",
		Summary: &CurrentSummary{
			Market: "SOL/USDC", ValueUnit: "USD", Day: "2026-08-31", TickSeconds: 60,
			OpeningEquityMicros: 100_000_000, EquityMicros: 100_000_000,
			HoldBenchmarkMicros: 100_000_000,
		},
	}
	message := "PAPER · " + strings.Repeat("x", MaxMessageBytes-len("PAPER · "))
	for index := 0; index < MaxEvents; index++ {
		snapshot.Events = append(snapshot.Events, Event{
			ID: eventID(KindOrderFilled, fmt.Sprintf("fill/%d", index)),
			At: day.Add(time.Duration(index) * time.Second), Kind: KindOrderFilled,
			Message: message,
		})
	}
	for index := 0; index < MaxHistoryPoints; index++ {
		snapshot.History = append(snapshot.History, PerformancePoint{
			At:           day.Add(time.Duration(index) * historyInterval),
			EquityMicros: 100_000_000, HoldBenchmarkMicros: 100_000_000,
		})
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded)+1 > maxSnapshotBytes {
		t.Fatalf("maximum status projection is %d bytes; limit is %d", len(encoded)+1, maxSnapshotBytes)
	}
}

func TestWriterAcceptsAnOlderDuplicateDuringReconciliation(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(filepath.Join(directory, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	message := "PAPER SIMULATION — filled. No transaction was signed or submitted."
	if err := writer.Append(start, KindOrderFilled, "first", message); err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(start.Add(time.Second), KindOrderFilled, "second", message); err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(start, KindOrderFilled, "first", message); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
}

func TestReconciliationSkipsEventsOlderThanTheBoundedProjection(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "alerts.json")
	writer, err := OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	message := "PAPER SIMULATION — filled. No transaction was signed or submitted."
	for index := 0; index < MaxEvents+2; index++ {
		if err := writer.Append(start.Add(time.Duration(index)*time.Second),
			KindOrderFilled, fmt.Sprintf("event/%d", index), message); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < MaxEvents+2; index++ {
		if err := writer.Reconcile(start.Add(time.Duration(index)*time.Second),
			KindOrderFilled, fmt.Sprintf("event/%d", index), message); err != nil {
			t.Fatal(err)
		}
	}
	data, err := securefile.ReadPrivate(path, maxSnapshotBytes)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot Snapshot
	if err := strictjson.Decode(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != MaxEvents || snapshot.DroppedEvents != 2 {
		t.Fatalf("events=%d dropped=%d", len(snapshot.Events), snapshot.DroppedEvents)
	}
}
