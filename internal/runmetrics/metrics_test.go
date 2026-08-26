package runmetrics

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

func TestMetricsExposeBoundedAttentionState(t *testing.T) {
	metrics := New(time.Unix(50, 0))
	metrics.Observe(
		time.Unix(100, 0),
		execution.Result{Decision: "degraded"},
		journal.Stats{
			Records: 12, Bytes: 34, ReservedBytes: 21, MaxRecords: 56, MaxBytes: 78,
			SendStartedRecords: 2, SubmittedRecords: 1,
		},
		control.Status{
			Mode:             control.ModeDevnetEnabled,
			ExpiresAt:        time.Unix(500, 0),
			MaxActions:       3,
			RemainingActions: 2,
		},
		operatorstatus.Action{},
	)
	recorder := httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	for _, expected := range []string{
		"mithril_agent_process_start_timestamp_seconds 50",
		"mithril_agent_last_heartbeat_timestamp_seconds 100",
		"mithril_agent_last_cycle_timestamp_seconds 100",
		"mithril_agent_attention_required 1",
		`mithril_agent_last_result{decision="degraded"} 1`,
		`mithril_agent_last_result{decision="complete"} 0`,
		"mithril_agent_journal_records 12",
		"mithril_agent_journal_record_limit 56",
		"mithril_agent_journal_bytes 34",
		"mithril_agent_journal_reserved_bytes 21",
		"mithril_agent_journal_byte_limit 78",
		"mithril_agent_send_started_total 2",
		"mithril_agent_submitted_total 1",
		`mithril_agent_control_mode{mode="devnet_enabled"} 1`,
		`mithril_agent_control_mode{mode="no_new_actions"} 0`,
		"mithril_agent_activation_max_actions 3",
		"mithril_agent_activation_remaining_actions 2",
		"mithril_agent_activation_expires_timestamp_seconds 500",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics omitted %q:\n%s", expected, body)
		}
	}
}

func TestMetricsExposePendingRecovery(t *testing.T) {
	metrics := New(time.Unix(50, 0))
	metrics.Observe(
		time.Unix(100, 0),
		execution.Result{Decision: "stopped"},
		journal.Stats{},
		control.Status{Mode: control.ModeNoNewActions, RecoveryPending: true},
		operatorstatus.Action{},
	)
	recorder := httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, "mithril_agent_recovery_pending 1") ||
		!strings.Contains(body, "mithril_agent_attention_required 1") {
		t.Fatalf("pending recovery is not observable:\n%s", body)
	}
}

func TestMetricsBeforeFirstCycleUseZeroTimestamp(t *testing.T) {
	recorder := httptest.NewRecorder()
	(&Metrics{}).ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(recorder.Body.String(), "mithril_agent_last_cycle_timestamp_seconds 0") ||
		!strings.Contains(recorder.Body.String(), "mithril_agent_last_heartbeat_timestamp_seconds 0") {
		t.Fatalf("initial metrics = %s", recorder.Body.String())
	}
}

func TestMetricsExposeBoundedPriceTriggerState(t *testing.T) {
	metrics := New(time.Unix(50, 0))
	metrics.Observe(
		time.Unix(100, 0),
		execution.Result{
			Decision: "stopped",
			PriceTrigger: &pricetrigger.Status{
				Feed: pricetrigger.FeedSOLUSD, Direction: pricetrigger.SellAtOrAbove,
				ThresholdMicros: 150_000_000, Available: true,
				ConservativePrice: 151_000_000, ConditionMet: true,
				ExecutableMinimum: 150_500_000, ExecutableCondition: true,
				ObservedAt: time.Unix(99, 0), PrimaryPublishedAt: time.Unix(98, 0),
				SecondaryPublishedAt: time.Unix(97, 0),
			},
		},
		journal.Stats{}, control.Status{Mode: control.ModeNoNewActions},
		operatorstatus.Action{},
	)
	recorder := httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	for _, expected := range []string{
		"mithril_agent_price_trigger_configured 1",
		"mithril_agent_price_trigger_valid 1",
		"mithril_agent_price_trigger_available 1",
		"mithril_agent_price_trigger_condition_met 1",
		"mithril_agent_price_trigger_executable_evaluated 1",
		"mithril_agent_price_trigger_executable_condition_met 1",
		"mithril_agent_price_trigger_target_microusd 150000000",
		"mithril_agent_price_trigger_conservative_microusd 151000000",
		"mithril_agent_price_trigger_minimum_devusdc_per_sol_micro 150500000",
		"mithril_agent_price_trigger_observed_timestamp_seconds 99",
		"mithril_agent_attention_required 0",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("price metrics omitted %q:\n%s", expected, body)
		}
	}
}

func TestMalformedPriceStatusIsBoundedAndNeedsAttention(t *testing.T) {
	metrics := New(time.Unix(50, 0))
	metrics.Observe(
		time.Unix(100, 0),
		execution.Result{
			Decision: "stopped",
			PriceTrigger: &pricetrigger.Status{
				Feed: "private-provider-secret", Direction: pricetrigger.SellAtOrAbove,
				ThresholdMicros: 1,
			},
		},
		journal.Stats{}, control.Status{Mode: control.ModeNoNewActions},
		operatorstatus.Action{},
	)
	recorder := httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	if strings.Contains(body, "private-provider-secret") ||
		!strings.Contains(body, "mithril_agent_price_trigger_configured 1") ||
		!strings.Contains(body, "mithril_agent_price_trigger_valid 0") ||
		!strings.Contains(body, "mithril_agent_attention_required 1") {
		t.Fatalf("malformed price status leaked or escaped attention: %s", body)
	}
}

func TestFailureMetricsExposeOnlyBoundedCategories(t *testing.T) {
	metrics := New(time.Unix(50, 0))
	metrics.Observe(
		time.Unix(100, 0),
		execution.Result{Decision: "failed", Reason: "operation_timeout"},
		journal.Stats{},
		control.Status{Mode: control.ModeNoNewActions},
		operatorstatus.Action{},
	)
	recorder := httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, `mithril_agent_last_failure{category="operation_timeout"} 1`) ||
		!strings.Contains(body, `mithril_agent_last_failure{category="operation_failed"} 0`) ||
		!strings.Contains(body, `mithril_agent_last_failure{category="quote_unavailable"} 0`) {
		t.Fatalf("failure metrics = %s", body)
	}
	metrics.Observe(
		time.Unix(101, 0),
		execution.Result{Decision: "failed", Reason: "provider-secret-text"},
		journal.Stats{},
		control.Status{Mode: control.ModeNoNewActions},
		operatorstatus.Action{},
	)
	recorder = httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body = recorder.Body.String()
	if strings.Contains(body, "provider-secret-text") ||
		!strings.Contains(body, `mithril_agent_last_failure{category="unknown"} 1`) {
		t.Fatalf("unbounded failure leaked into metrics: %s", body)
	}
}

func TestHeartbeatPrecedesFirstCompletedCycle(t *testing.T) {
	metrics := New(time.Unix(50, 0))
	metrics.Heartbeat(time.Unix(75, 0))
	recorder := httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, "mithril_agent_last_heartbeat_timestamp_seconds 75") ||
		!strings.Contains(body, "mithril_agent_last_cycle_timestamp_seconds 0") {
		t.Fatalf("in-progress metrics = %s", body)
	}
}

func TestExecutingIsKnownWithoutImmediateAttention(t *testing.T) {
	metrics := &Metrics{now: func() time.Time { return time.Unix(200, 0) }}
	metrics.Observe(
		time.Unix(200, 0),
		execution.Result{
			Decision:                     "executing",
			PendingSinceUnix:             170,
			ReconciliationTimeoutSeconds: 120,
		},
		journal.Stats{},
		control.Status{
			Mode: control.ModeDevnetEnabled, ExpiresAt: time.Unix(500, 0),
			MaxActions: 1, RemainingActions: 1,
		},
		operatorstatus.Action{},
	)
	recorder := httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, "mithril_agent_attention_required 0") ||
		!strings.Contains(body, `mithril_agent_last_result{decision="executing"} 1`) ||
		!strings.Contains(body, "mithril_agent_oldest_pending_reconciliation_age_seconds 30") ||
		!strings.Contains(body, "mithril_agent_reconciliation_timeout_seconds 120") ||
		!strings.Contains(body, "mithril_agent_pending_reconciliation_since_timestamp_seconds 170") {
		t.Fatalf("executing metrics = %s", body)
	}
}

func TestStaleOrMalformedPendingExecutionNeedsAttention(t *testing.T) {
	tests := []execution.Result{
		{
			Decision:                     "executing",
			PendingSinceUnix:             50,
			ReconciliationTimeoutSeconds: 100,
		},
		{Decision: "executing"},
	}
	for _, result := range tests {
		metrics := &Metrics{now: func() time.Time { return time.Unix(200, 0) }}
		metrics.Observe(
			time.Unix(200, 0),
			result,
			journal.Stats{},
			control.Status{Mode: control.ModeNoNewActions},
			operatorstatus.Action{},
		)
		recorder := httptest.NewRecorder()
		metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
		if !strings.Contains(recorder.Body.String(), "mithril_agent_attention_required 1") {
			t.Fatalf("metrics did not require attention: %s", recorder.Body.String())
		}
	}
}

func TestLastActionMetricsRemainVisibleAcrossIdleCycles(t *testing.T) {
	metrics := New(time.Unix(50, 0))
	lastAction := operatorstatus.Action{
		ObservedAt: time.Unix(90, 0),
		Result: execution.Result{
			ActionID:  "not-exported",
			Decision:  "complete",
			Verdict:   "finalized",
			Signature: "also-not-exported",
			Submitted: true,
		},
	}
	metrics.Observe(
		time.Unix(100, 0), execution.Result{Decision: "stopped"}, journal.Stats{},
		control.Status{Mode: control.ModeNoNewActions}, lastAction,
	)
	recorder := httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	for _, expected := range []string{
		"mithril_agent_last_action_timestamp_seconds 90",
		`mithril_agent_last_action_result{decision="complete"} 1`,
		`mithril_agent_last_action_verdict{verdict="finalized"} 1`,
		"mithril_agent_last_action_submitted 1",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("last action metrics omitted %q:\n%s", expected, body)
		}
	}
	if strings.Contains(body, "not-exported") || strings.Contains(body, "also-not-exported") {
		t.Fatalf("action identifiers leaked into metrics: %s", body)
	}
}

func TestLastActionSubmittedMetricDoesNotExposeSignature(t *testing.T) {
	metrics := New(time.Unix(50, 0))
	metrics.Observe(
		time.Unix(100, 0), execution.Result{Decision: "pending"}, journal.Stats{},
		control.Status{Mode: control.ModeNoNewActions},
		operatorstatus.Action{
			ObservedAt: time.Unix(90, 0),
			Result: execution.Result{
				ActionID: strings.Repeat("a", 64), Decision: "pending",
				Verdict: "pending", Signature: "private-signature", Submitted: true,
			},
		},
	)
	recorder := httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, "mithril_agent_last_action_submitted 1") ||
		strings.Contains(body, "private-signature") {
		t.Fatalf("submitted action metrics = %s", body)
	}
}

func TestSignedActionIsNotReportedAsSubmittedWithoutDurableSubmission(t *testing.T) {
	metrics := New(time.Unix(50, 0))
	metrics.Observe(
		time.Unix(100, 0), execution.Result{Decision: "pending"}, journal.Stats{},
		control.Status{Mode: control.ModeNoNewActions},
		operatorstatus.Action{
			ObservedAt: time.Unix(90, 0),
			Result: execution.Result{
				ActionID: strings.Repeat("a", 64), Decision: "pending",
				Verdict: "pending", Signature: "signed-but-not-submitted",
			},
		},
	)
	recorder := httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, "mithril_agent_last_action_submitted 0") ||
		strings.Contains(body, "signed-but-not-submitted") {
		t.Fatalf("signed-only action metrics = %s", body)
	}
}

func TestLastActionMetricsBoundUnknownValues(t *testing.T) {
	metrics := New(time.Unix(50, 0))
	metrics.Observe(
		time.Unix(100, 0), execution.Result{Decision: "stopped"}, journal.Stats{},
		control.Status{Mode: control.ModeNoNewActions},
		operatorstatus.Action{
			ObservedAt: time.Unix(90, 0),
			Result:     execution.Result{ActionID: "hidden", Decision: "secret-decision", Verdict: "secret-verdict"},
		},
	)
	recorder := httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	if strings.Contains(body, "secret-") ||
		!strings.Contains(body, `mithril_agent_last_action_result{decision="unknown"} 1`) ||
		!strings.Contains(body, `mithril_agent_last_action_verdict{verdict="unknown"} 1`) ||
		!strings.Contains(body, "mithril_agent_attention_required 1") {
		t.Fatalf("unbounded last action metrics: %s", body)
	}
}

func TestAcknowledgedTerminalLastActionIsHistorical(t *testing.T) {
	lastAction := operatorstatus.Action{
		ObservedAt: time.Unix(90, 0),
		Result: execution.Result{
			ActionID: strings.Repeat("a", 64),
			Decision: "halted",
			Verdict:  "diverged",
		},
	}
	metrics := New(time.Unix(50, 0))
	metrics.Observe(
		time.Unix(100, 0), execution.Result{Decision: "stopped"}, journal.Stats{},
		control.Status{Mode: control.ModeNoNewActions}, lastAction,
	)
	recorder := httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(recorder.Body.String(), "mithril_agent_attention_required 0") {
		t.Fatalf("acknowledged terminal action still required attention: %s", recorder.Body.String())
	}

	metrics = New(time.Unix(101, 0))
	metrics.Observe(
		time.Unix(102, 0), execution.Result{Decision: "waiting"}, journal.Stats{},
		control.Status{
			Mode: control.ModeDevnetEnabled, ExpiresAt: time.Unix(500, 0),
			MaxActions: 1, RemainingActions: 1,
		},
		lastAction,
	)
	recorder = httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(recorder.Body.String(), "mithril_agent_attention_required 0") {
		t.Fatalf("historical terminal action affected fresh enablement: %s", recorder.Body.String())
	}
}

func TestDurableTerminalStopRestoresAttentionWithoutOperatorStatus(t *testing.T) {
	metrics := New(time.Unix(50, 0))
	metrics.Observe(
		time.Unix(100, 0), execution.Result{Decision: "stopped"}, journal.Stats{},
		control.Status{
			Mode: control.ModeNoNewActions, TerminalActionID: strings.Repeat("a", 64),
			TerminalOutcome: "halted",
		},
		operatorstatus.Action{},
	)
	recorder := httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, "mithril_agent_attention_required 1") ||
		!strings.Contains(body, `mithril_agent_terminal_stop{outcome="halted"} 1`) ||
		!strings.Contains(body, `mithril_agent_terminal_stop{outcome="failed"} 0`) {
		t.Fatalf("durable terminal stop metrics = %s", body)
	}
}

func TestMalformedTerminalLatchRequiresAttention(t *testing.T) {
	metrics := New(time.Unix(50, 0))
	metrics.Observe(
		time.Unix(100, 0), execution.Result{Decision: "stopped"}, journal.Stats{},
		control.Status{Mode: control.ModeNoNewActions, TerminalOutcome: "halted"},
		operatorstatus.Action{},
	)
	recorder := httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(recorder.Body.String(), "mithril_agent_attention_required 1") {
		t.Fatalf("malformed terminal latch did not require attention: %s", recorder.Body.String())
	}
}
