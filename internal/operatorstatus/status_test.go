package operatorstatus

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/journal"
)

func TestWriteRetainsLastActionAcrossIdleCycles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator-status.json")
	completedAt := time.Date(2026, time.August, 1, 10, 0, 0, 0, time.UTC)
	completed := validSnapshot(completedAt, validTradeResult("action-1", "complete", "finalized"))
	if err := Write(path, completed); err != nil {
		t.Fatal(err)
	}

	idle := validSnapshot(completedAt.Add(10*time.Second), Result{
		Decision: "stopped", Reason: "Devnet actions are not enabled",
	})
	if err := Write(path, idle); err != nil {
		t.Fatal(err)
	}

	view, err := CurrentView(
		path, idle.Profile, idle.Cluster, idle.ProfileVersion,
		idle.Control, completedAt.Add(20*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if view.Result.Decision != "stopped" {
		t.Fatalf("current decision = %q", view.Result.Decision)
	}
	if view.LastAction.ObservedAt != completedAt ||
		view.LastAction.Result.ActionID != "action-1" ||
		view.LastAction.Result.Verdict != "finalized" {
		t.Fatalf("last action = %+v", view.LastAction)
	}
}

func TestWriteRebuildsLastActionWithoutPriorStatusFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator-status.json")
	completedAt := time.Date(2026, time.August, 1, 10, 0, 0, 0, time.UTC)
	snapshot := validSnapshot(completedAt.Add(time.Minute), Result{
		Decision: "stopped", Reason: "Devnet actions are not enabled",
	})
	snapshot.LastAction = Action{
		ObservedAt: completedAt,
		Result:     validTradeResult("action-from-journal", "complete", "finalized"),
	}
	if err := Write(path, snapshot); err != nil {
		t.Fatal(err)
	}
	written, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if written.Result.Decision != "stopped" ||
		written.LastAction.ObservedAt != completedAt ||
		written.LastAction.Result.ActionID != "action-from-journal" ||
		written.LastAction.Result.Verdict != "finalized" {
		t.Fatalf("rebuilt status = %+v", written)
	}
}

func TestJournalLastActionOverridesNewerDerivedStatusTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator-status.json")
	at := time.Date(2026, time.August, 1, 10, 0, 0, 0, time.UTC)
	old := validSnapshot(at.Add(2*time.Hour), validTradeResult("old-action", "complete", "finalized"))
	if err := Write(path, old); err != nil {
		t.Fatal(err)
	}

	current := validSnapshot(at.Add(3*time.Hour), Result{Decision: "stopped"})
	current.LastAction = Action{
		ObservedAt: at.Add(time.Hour),
		Result:     validTradeResult("journal-action", "complete", "finalized"),
	}
	if err := Write(path, current); err != nil {
		t.Fatal(err)
	}
	written, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if written.LastAction.Result.ActionID != "journal-action" ||
		written.LastAction.ObservedAt != at.Add(time.Hour) {
		t.Fatalf("authoritative last action = %+v", written.LastAction)
	}
}

func TestWriteDoesNotRetainActionFromAnotherProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator-status.json")
	at := time.Date(2026, time.August, 1, 10, 0, 0, 0, time.UTC)
	first := validSnapshot(at, validTradeResult("action-1", "complete", "finalized"))
	if err := Write(path, first); err != nil {
		t.Fatal(err)
	}

	second := validSnapshot(at.Add(time.Second), Result{Decision: "stopped"})
	second.Profile = "another-profile"
	if err := Write(path, second); err != nil {
		t.Fatal(err)
	}
	written, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if written.LastAction != (Action{}) {
		t.Fatalf("last action crossed profiles: %+v", written.LastAction)
	}
}

func TestViewDistinguishesAcknowledgedTerminalHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator-status.json")
	at := time.Date(2026, time.August, 1, 10, 0, 0, 0, time.UTC)
	actionID := "9e5bff7152eb7da45d239cc3751589f94eec9d495902491b8932857747179052"
	terminal := validSnapshot(at, validTradeResult(actionID, "halted", "diverged"))
	terminal.Control = control.Status{
		Mode: control.ModeNoNewActions, TerminalActionID: actionID,
		TerminalOutcome: "halted",
	}
	if err := Write(path, terminal); err != nil {
		t.Fatal(err)
	}
	view, err := CurrentView(
		path, terminal.Profile, terminal.Cluster, terminal.ProfileVersion,
		terminal.Control, at.Add(time.Second),
	)
	if err != nil || !view.AttentionRequired || view.LastActionAcknowledged {
		t.Fatalf("unacknowledged view = %+v, %v", view, err)
	}

	idle := validSnapshot(at.Add(2*time.Second), Result{Decision: "stopped"})
	if err := Write(path, idle); err != nil {
		t.Fatal(err)
	}
	view, err = CurrentView(
		path, idle.Profile, idle.Cluster, idle.ProfileVersion,
		idle.Control, at.Add(3*time.Second),
	)
	if err != nil || view.AttentionRequired || !view.LastActionAcknowledged ||
		view.LastAction.Result.ActionID != actionID {
		t.Fatalf("acknowledged view = %+v, %v", view, err)
	}
}

func TestViewRejectsMalformedCurrentTerminalLatch(t *testing.T) {
	_, err := CurrentView(
		filepath.Join(t.TempDir(), "missing.json"), "profile", "devnet", 1,
		control.Status{Mode: control.ModeNoNewActions, TerminalOutcome: "halted"},
		time.Now().UTC(),
	)
	if err == nil {
		t.Fatal("malformed current terminal latch was accepted")
	}
}

func TestViewFromSnapshotRevalidatesAndDerivesFreshness(t *testing.T) {
	now := time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC)
	snapshot := Snapshot{
		Version: Version, ObservedAt: now.Add(-time.Second),
		Profile: "orca_devnet_swap_v1", ProfileVersion: 1, Cluster: "devnet",
		Result:  Result{Decision: "stopped"},
		Journal: journal.Stats{MaxRecords: 100, MaxBytes: 1024},
		Control: control.Status{Mode: control.ModeNoNewActions},
	}
	view, err := ViewFromSnapshot(snapshot, now)
	if err != nil {
		t.Fatal(err)
	}
	if view.RunnerState != "recent" || view.AgeSeconds != 1 || view.Stale ||
		view.Profile != snapshot.Profile || view.Control.Mode != control.ModeNoNewActions {
		t.Fatalf("bounded snapshot view = %+v", view)
	}
	snapshot.ObservedAt = now.Add(6 * time.Second)
	if _, err := ViewFromSnapshot(snapshot, now); err == nil {
		t.Fatal("future bounded status snapshot was accepted")
	}
}

func TestRequiresAttentionCoversPendingAndTerminalStates(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	stopped := control.Status{Mode: control.ModeNoNewActions}
	tests := []struct {
		name   string
		result Result
		status control.Status
		want   bool
	}{
		{name: "idle", result: Result{Decision: "stopped"}, status: stopped},
		{name: "recovery pending", result: Result{Decision: "stopped"}, status: control.Status{
			Mode: control.ModeNoNewActions, RecoveryPending: true,
		}, want: true},
		{name: "canceled before submission", result: Result{Decision: "canceled"}, status: stopped},
		{name: "canceled after submission", result: Result{Decision: "canceled", Submitted: true}, status: stopped, want: true},
		{name: "failed", result: Result{Decision: "failed"}, status: stopped, want: true},
		{name: "unknown", result: Result{Decision: "surprise"}, status: stopped, want: true},
		{name: "malformed pending", result: Result{Decision: "pending"}, status: stopped, want: true},
		{name: "future pending", result: Result{
			Decision: "pending", PendingSinceUnix: 1_001, ReconciliationTimeoutSeconds: 60,
		}, status: stopped, want: true},
		{name: "recent pending", result: Result{
			Decision: "pending", PendingSinceUnix: 950, ReconciliationTimeoutSeconds: 60,
		}, status: stopped},
		{name: "overdue pending", result: Result{
			Decision: "pending", PendingSinceUnix: 900, ReconciliationTimeoutSeconds: 60,
		}, status: stopped, want: true},
		{name: "terminal latch", result: Result{Decision: "stopped"}, status: control.Status{
			Mode:             control.ModeNoNewActions,
			TerminalActionID: "9e5bff7152eb7da45d239cc3751589f94eec9d495902491b8932857747179052",
			TerminalOutcome:  "halted",
		}, want: true},
		{name: "malformed latch", result: Result{Decision: "stopped"}, status: control.Status{
			Mode: control.ModeNoNewActions, TerminalOutcome: "halted",
		}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RequiresAttention(test.result, test.status, now); got != test.want {
				t.Fatalf("attention = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSnapshotBindsTradeAssetsToProfile(t *testing.T) {
	at := time.Date(2026, time.August, 1, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name, profile, input, output string
		valid                        bool
	}{
		{name: "sell", profile: "orca_devnet_swap_v1", input: "SOL", output: "devUSDC", valid: true},
		{name: "buy", profile: "orca_devnet_buy_v2", input: "devUSDC", output: "SOL", valid: true},
		{name: "reversed sell", profile: "orca_devnet_swap_v1", input: "devUSDC", output: "SOL"},
		{name: "reversed buy", profile: "orca_devnet_buy_v2", input: "SOL", output: "devUSDC"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := validTradeResult("action", "complete", "finalized")
			result.InputAsset, result.OutputAsset = test.input, test.output
			if test.profile == "orca_devnet_buy_v2" {
				result.AmountLamports = 0
			}
			snapshot := validSnapshot(at, result)
			snapshot.Profile = test.profile
			if test.profile == "orca_devnet_buy_v2" {
				snapshot.ProfileVersion = 2
			}
			err := ValidateSnapshot(snapshot)
			if (err == nil) != test.valid {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
}

func TestSnapshotRejectsInvalidTradeLifecycleSemantics(t *testing.T) {
	at := time.Date(2026, time.August, 1, 10, 0, 0, 0, time.UTC)
	tests := map[string]func(*Result){
		"missing assets":    func(result *Result) { result.InputAsset, result.OutputAsset = "", "" },
		"sell amount":       func(result *Result) { result.AmountLamports++ },
		"not submitted":     func(result *Result) { result.Submitted = false },
		"missing signature": func(result *Result) { result.Signature = "" },
		"below minimum":     func(result *Result) { result.OutputAmount = result.MinimumOutput - 1 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			result := validTradeResult("action", "complete", "finalized")
			mutate(&result)
			if err := ValidateSnapshot(validSnapshot(at, result)); err == nil {
				t.Fatal("invalid completed trade was accepted")
			}
		})
	}

	pending := validTradeResult("action", "pending", "pending")
	pending.Submitted = false
	pending.OutputAmount = 0
	if err := ValidateSnapshot(validSnapshot(at, pending)); err != nil {
		t.Fatalf("pending pre-ack trade was rejected: %v", err)
	}
	canceled := validTradeResult("action", "canceled", "")
	canceled.Submitted = false
	canceled.Signature = ""
	canceled.MinimumOutput = 0
	canceled.OutputAmount = 0
	canceled.PendingSinceUnix = 0
	if err := ValidateSnapshot(validSnapshot(at, canceled)); err != nil {
		t.Fatalf("canceled pre-send trade was rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Result){
		"submitted":      func(result *Result) { result.Submitted = true },
		"send timestamp": func(result *Result) { result.PendingSinceUnix = 1 },
	} {
		t.Run("canceled "+name, func(t *testing.T) {
			changed := canceled
			mutate(&changed)
			if err := ValidateSnapshot(validSnapshot(at, changed)); err == nil {
				t.Fatal("invalid canceled trade was accepted")
			}
		})
	}
	early := Result{ActionID: "action", Decision: "waiting"}
	if err := ValidateSnapshot(validSnapshot(at, early)); err != nil {
		t.Fatalf("early waiting result was rejected: %v", err)
	}
}

func TestFormatAmountUsesPilotAssetDecimals(t *testing.T) {
	if got := FormatAmount(100_000, "devUSDC"); got != "0.100000 devUSDC" {
		t.Fatalf("devUSDC amount = %q", got)
	}
	if got := FormatAmount(1_000_000, "SOL"); got != "0.001000000 SOL" {
		t.Fatalf("SOL amount = %q", got)
	}
}

func validSnapshot(at time.Time, result Result) Snapshot {
	return Snapshot{
		Version: Version, ObservedAt: at, Profile: "orca_devnet_swap_v1",
		ProfileVersion: 1, Cluster: "devnet", Result: result,
		Journal: journal.Stats{MaxRecords: 100, MaxBytes: 1024},
		Control: control.Status{Mode: control.ModeNoNewActions},
	}
}

func validTradeResult(actionID, decision, verdict string) Result {
	return Result{
		ActionID: actionID, Decision: decision, Verdict: verdict,
		AmountLamports: 1, InputAmount: 1, InputAsset: "SOL", OutputAsset: "devUSDC",
		MinimumOutput: 1, OutputAmount: 1, Signature: "signature", Submitted: true,
		PendingSinceUnix: 1, ReconciliationTimeoutSeconds: 30,
	}
}
