package runmetrics

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

var decisions = []string{
	"canceled",
	"complete",
	"degraded",
	"executing",
	"failed",
	"halted",
	"pending",
	"skipped",
	"stopped",
	"waiting",
}

var failureCategories = []string{
	"control_state_unavailable",
	"operation_failed",
	"operation_timeout",
	"quote_unavailable",
	"unknown",
}

var actionVerdicts = []string{
	"none",
	"pending",
	"finalized",
	"failed",
	"unresolved",
	"diverged",
	"unknown",
}

var terminalOutcomes = []string{"failed", "halted"}

// AlertSlot is one configured alert threshold and whether its condition held
// this cycle. Slots render as gauges; the deployed Prometheus stack owns
// persistence and delivery.
type AlertSlot struct {
	Configured bool
	Threshold  uint64
	Met        bool
}

// AlertGauges is the full alert surface for one cycle. EvidenceAvailable is
// false when any CONFIGURED slot lacked usable evidence — a stale or refused
// price print, a cycle with no balance observation — so a dead source is loud
// instead of silently disabling the operator's alerts.
type AlertGauges struct {
	PriceAbove        AlertSlot
	PriceBelow        AlertSlot
	BalanceAbove      AlertSlot
	BalanceBelow      AlertSlot
	EvidenceAvailable bool
}

type Metrics struct {
	mu                           sync.Mutex
	startedAt                    time.Time
	heartbeatAt                  time.Time
	observedAt                   time.Time
	decision                     string
	failureCategory              string
	lastActionAt                 time.Time
	lastActionDecision           string
	lastActionVerdict            string
	lastActionSubmitted          bool
	priceTriggerConfigured       bool
	priceTriggerValid            bool
	priceTrigger                 pricetrigger.Status
	pendingSinceUnix             int64
	reconciliationTimeoutSeconds uint64
	balanceLamports              uint64
	balanceObservedUnix          int64
	alerts                       AlertGauges
	sweepRegisteredUnix          int64
	sweepActiveAfterUnix         int64
	journal                      journal.Stats
	control                      control.Status
	now                          func() time.Time
}

// ObserveAlerts records one cycle's alert evaluation for the next scrape.
func (m *Metrics) ObserveAlerts(gauges AlertGauges) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alerts = gauges
}

// ObserveSweepRegistration publishes when the sweep destination was proven
// and when it becomes active. A destination change is a re-registration, so
// alerting on a recent registration timestamp notifies the operator on a
// channel an on-host attacker does not control.
func (m *Metrics) ObserveSweepRegistration(registeredUnix, activeAfterUnix int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepRegisteredUnix = registeredUnix
	m.sweepActiveAfterUnix = activeAfterUnix
}

func (m *Metrics) Heartbeat(at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.heartbeatAt = at.UTC()
}

func New(startedAt time.Time) *Metrics {
	return &Metrics{startedAt: startedAt.UTC()}
}

func (m *Metrics) Observe(
	at time.Time,
	result execution.Result,
	stats journal.Stats,
	status control.Status,
	lastAction operatorstatus.Action,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.heartbeatAt = at.UTC()
	m.observedAt = at.UTC()
	m.decision = result.Decision
	m.failureCategory = ""
	if result.Decision == "failed" {
		m.failureCategory = result.Reason
		if !knownFailureCategory(m.failureCategory) {
			m.failureCategory = "unknown"
		}
	}
	m.lastActionAt = lastAction.ObservedAt.UTC()
	m.lastActionDecision = boundedDecision(lastAction.Result.Decision)
	m.lastActionVerdict = boundedVerdict(lastAction.Result.Verdict)
	m.lastActionSubmitted = lastAction.Result.Submitted
	m.priceTriggerConfigured = result.PriceTrigger != nil
	m.priceTriggerValid = false
	m.priceTrigger = pricetrigger.Status{}
	if result.PriceTrigger != nil && pricetrigger.ValidateStatus(*result.PriceTrigger) == nil {
		m.priceTriggerValid = true
		m.priceTrigger = *result.PriceTrigger
	}
	m.pendingSinceUnix = result.PendingSinceUnix
	m.reconciliationTimeoutSeconds = result.ReconciliationTimeoutSeconds
	if result.BalanceObservedUnix > 0 {
		// A cycle that stopped before observing carries no balance; keeping
		// the previous reading (with its own timestamp) beats zeroing a
		// gauge that alerting compares against thresholds.
		m.balanceLamports = result.BalanceLamports
		m.balanceObservedUnix = result.BalanceObservedUnix
	}
	m.journal = stats
	m.control = status
}

func (m *Metrics) ServeHTTP(writer http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	startedAt := m.startedAt
	heartbeatAt := m.heartbeatAt
	observedAt := m.observedAt
	decision := m.decision
	failureCategory := m.failureCategory
	lastActionAt := m.lastActionAt
	lastActionDecision := m.lastActionDecision
	lastActionVerdict := m.lastActionVerdict
	lastActionSubmitted := m.lastActionSubmitted
	priceTriggerConfigured := m.priceTriggerConfigured
	priceTriggerValid := m.priceTriggerValid
	priceTrigger := m.priceTrigger
	pendingSinceUnix := m.pendingSinceUnix
	reconciliationTimeoutSeconds := m.reconciliationTimeoutSeconds
	balanceLamports := m.balanceLamports
	balanceObservedUnix := m.balanceObservedUnix
	alerts := m.alerts
	sweepRegisteredUnix := m.sweepRegisteredUnix
	sweepActiveAfterUnix := m.sweepActiveAfterUnix
	stats := m.journal
	controlStatus := m.control
	now := m.now
	m.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_process_start_timestamp_seconds gauge")
	var startedUnix int64
	if !startedAt.IsZero() {
		startedUnix = startedAt.Unix()
	}
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_process_start_timestamp_seconds %d\n",
		startedUnix,
	)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_last_heartbeat_timestamp_seconds gauge")
	var heartbeatUnix int64
	if !heartbeatAt.IsZero() {
		heartbeatUnix = heartbeatAt.Unix()
	}
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_last_heartbeat_timestamp_seconds %d\n",
		heartbeatUnix,
	)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_last_cycle_timestamp_seconds gauge")
	var observedUnix int64
	if !observedAt.IsZero() {
		observedUnix = observedAt.Unix()
	}
	_, _ = fmt.Fprintf(writer, "mithril_agent_last_cycle_timestamp_seconds %d\n", observedUnix)
	// The balance pair travels together: the value is only as good as its
	// observation time, and alert rules must gate on the timestamp being
	// fresh rather than trust a possibly-stale number.
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_balance_lamports gauge")
	_, _ = fmt.Fprintf(writer, "mithril_agent_balance_lamports %d\n", balanceLamports)
	_, _ = fmt.Fprintln(
		writer,
		"# TYPE mithril_agent_balance_observation_timestamp_seconds gauge",
	)
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_balance_observation_timestamp_seconds %d\n",
		balanceObservedUnix,
	)
	// Alert slots render every member on every scrape, so a stale series can
	// never read as configured or met.
	for _, slot := range []struct {
		name string
		slot AlertSlot
	}{
		{"price_above", alerts.PriceAbove},
		{"price_below", alerts.PriceBelow},
		{"balance_above", alerts.BalanceAbove},
		{"balance_below", alerts.BalanceBelow},
	} {
		configured, met := 0, 0
		if slot.slot.Configured {
			configured = 1
		}
		if slot.slot.Met {
			met = 1
		}
		_, _ = fmt.Fprintf(writer, "# TYPE mithril_agent_alert_%s_configured gauge\n", slot.name)
		_, _ = fmt.Fprintf(writer, "mithril_agent_alert_%s_configured %d\n", slot.name, configured)
		_, _ = fmt.Fprintf(writer, "# TYPE mithril_agent_alert_%s_threshold gauge\n", slot.name)
		_, _ = fmt.Fprintf(writer, "mithril_agent_alert_%s_threshold %d\n", slot.name, slot.slot.Threshold)
		_, _ = fmt.Fprintf(writer, "# TYPE mithril_agent_alert_%s_condition_met gauge\n", slot.name)
		_, _ = fmt.Fprintf(writer, "mithril_agent_alert_%s_condition_met %d\n", slot.name, met)
	}
	evidenceAvailable := 0
	if alerts.EvidenceAvailable {
		evidenceAvailable = 1
	}
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_alert_evidence_available gauge")
	_, _ = fmt.Fprintf(writer, "mithril_agent_alert_evidence_available %d\n", evidenceAvailable)
	_, _ = fmt.Fprintln(
		writer,
		"# TYPE mithril_agent_sweep_destination_registered_timestamp_seconds gauge",
	)
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_sweep_destination_registered_timestamp_seconds %d\n",
		sweepRegisteredUnix,
	)
	_, _ = fmt.Fprintln(
		writer,
		"# TYPE mithril_agent_sweep_active_after_timestamp_seconds gauge",
	)
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_sweep_active_after_timestamp_seconds %d\n",
		sweepActiveAfterUnix,
	)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_attention_required gauge")
	attention := 0
	if operatorstatus.RequiresAttention(
		execution.Result{
			Decision: decision, PendingSinceUnix: pendingSinceUnix,
			ReconciliationTimeoutSeconds: reconciliationTimeoutSeconds,
		},
		controlStatus,
		now().UTC(),
	) ||
		lastActionDecision == "unknown" || lastActionVerdict == "unknown" ||
		(priceTriggerConfigured && !priceTriggerValid) ||
		(decision != "" && !knownControlMode(controlStatus.Mode)) {
		attention = 1
	}
	pendingAge := int64(0)
	if pendingSinceUnix > 0 {
		pendingAge = now().UTC().Unix() - pendingSinceUnix
		if pendingAge < 0 {
			pendingAge = 0
		}
	}
	_, _ = fmt.Fprintf(writer, "mithril_agent_attention_required %d\n", attention)
	_, _ = fmt.Fprintln(
		writer,
		"# TYPE mithril_agent_oldest_pending_reconciliation_age_seconds gauge",
	)
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_oldest_pending_reconciliation_age_seconds %d\n",
		pendingAge,
	)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_reconciliation_timeout_seconds gauge")
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_reconciliation_timeout_seconds %d\n",
		reconciliationTimeoutSeconds,
	)
	_, _ = fmt.Fprintln(
		writer,
		"# TYPE mithril_agent_pending_reconciliation_since_timestamp_seconds gauge",
	)
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_pending_reconciliation_since_timestamp_seconds %d\n",
		pendingSinceUnix,
	)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_last_result gauge")
	for _, value := range decisions {
		active := 0
		if decision == value {
			active = 1
		}
		_, _ = fmt.Fprintf(
			writer,
			"mithril_agent_last_result{decision=%q} %d\n",
			value,
			active,
		)
	}
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_last_failure gauge")
	for _, category := range failureCategories {
		active := 0
		if failureCategory == category {
			active = 1
		}
		_, _ = fmt.Fprintf(
			writer,
			"mithril_agent_last_failure{category=%q} %d\n",
			category,
			active,
		)
	}
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_last_action_timestamp_seconds gauge")
	var lastActionUnix int64
	if !lastActionAt.IsZero() {
		lastActionUnix = lastActionAt.Unix()
	}
	_, _ = fmt.Fprintf(writer, "mithril_agent_last_action_timestamp_seconds %d\n", lastActionUnix)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_last_action_result gauge")
	for _, value := range append(decisions, "unknown") {
		active := 0
		if lastActionDecision == value {
			active = 1
		}
		_, _ = fmt.Fprintf(
			writer,
			"mithril_agent_last_action_result{decision=%q} %d\n",
			value,
			active,
		)
	}
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_last_action_verdict gauge")
	for _, value := range actionVerdicts {
		active := 0
		if lastActionVerdict == value {
			active = 1
		}
		_, _ = fmt.Fprintf(
			writer,
			"mithril_agent_last_action_verdict{verdict=%q} %d\n",
			value,
			active,
		)
	}
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_last_action_submitted gauge")
	submitted := 0
	if lastActionSubmitted {
		submitted = 1
	}
	_, _ = fmt.Fprintf(writer, "mithril_agent_last_action_submitted %d\n", submitted)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_price_trigger_configured gauge")
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_price_trigger_configured %d\n",
		boolMetric(priceTriggerConfigured),
	)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_price_trigger_valid gauge")
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_price_trigger_valid %d\n",
		boolMetric(priceTriggerValid),
	)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_price_trigger_available gauge")
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_price_trigger_available %d\n",
		boolMetric(priceTriggerValid && priceTrigger.Available),
	)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_price_trigger_condition_met gauge")
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_price_trigger_condition_met %d\n",
		boolMetric(priceTriggerValid && priceTrigger.ConditionMet),
	)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_price_trigger_executable_evaluated gauge")
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_price_trigger_executable_evaluated %d\n",
		boolMetric(priceTriggerValid && priceTrigger.ExecutableMinimum != 0),
	)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_price_trigger_executable_condition_met gauge")
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_price_trigger_executable_condition_met %d\n",
		boolMetric(priceTriggerValid && priceTrigger.ExecutableCondition),
	)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_price_trigger_target_microusd gauge")
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_price_trigger_target_microusd %d\n",
		priceTrigger.ThresholdMicros,
	)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_price_trigger_conservative_microusd gauge")
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_price_trigger_conservative_microusd %d\n",
		priceTrigger.ConservativePrice,
	)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_price_trigger_minimum_devusdc_per_sol_micro gauge")
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_price_trigger_minimum_devusdc_per_sol_micro %d\n",
		priceTrigger.ExecutableMinimum,
	)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_price_trigger_observed_timestamp_seconds gauge")
	var priceObservedUnix int64
	if !priceTrigger.ObservedAt.IsZero() {
		priceObservedUnix = priceTrigger.ObservedAt.Unix()
	}
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_price_trigger_observed_timestamp_seconds %d\n",
		priceObservedUnix,
	)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_journal_records gauge")
	_, _ = fmt.Fprintf(writer, "mithril_agent_journal_records %d\n", stats.Records)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_journal_record_limit gauge")
	_, _ = fmt.Fprintf(writer, "mithril_agent_journal_record_limit %d\n", stats.MaxRecords)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_journal_bytes gauge")
	_, _ = fmt.Fprintf(writer, "mithril_agent_journal_bytes %d\n", stats.Bytes)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_journal_reserved_bytes gauge")
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_journal_reserved_bytes %d\n",
		stats.ReservedBytes,
	)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_journal_byte_limit gauge")
	_, _ = fmt.Fprintf(writer, "mithril_agent_journal_byte_limit %d\n", stats.MaxBytes)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_send_started_total counter")
	_, _ = fmt.Fprintf(writer, "mithril_agent_send_started_total %d\n", stats.SendStartedRecords)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_submitted_total counter")
	_, _ = fmt.Fprintf(writer, "mithril_agent_submitted_total %d\n", stats.SubmittedRecords)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_control_mode gauge")
	for _, mode := range []string{control.ModeNoNewActions, control.ModeDevnetEnabled} {
		active := 0
		if controlStatus.Mode == mode {
			active = 1
		}
		_, _ = fmt.Fprintf(
			writer,
			"mithril_agent_control_mode{mode=%q} %d\n",
			mode,
			active,
		)
	}
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_terminal_stop gauge")
	for _, outcome := range terminalOutcomes {
		active := 0
		if controlStatus.TerminalOutcome == outcome {
			active = 1
		}
		_, _ = fmt.Fprintf(
			writer,
			"mithril_agent_terminal_stop{outcome=%q} %d\n",
			outcome,
			active,
		)
	}
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_activation_max_actions gauge")
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_activation_max_actions %d\n",
		controlStatus.MaxActions,
	)
	_, _ = fmt.Fprintln(writer, "# TYPE mithril_agent_activation_remaining_actions gauge")
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_activation_remaining_actions %d\n",
		controlStatus.RemainingActions,
	)
	_, _ = fmt.Fprintln(
		writer,
		"# TYPE mithril_agent_activation_expires_timestamp_seconds gauge",
	)
	var expiresUnix int64
	if !controlStatus.ExpiresAt.IsZero() {
		expiresUnix = controlStatus.ExpiresAt.Unix()
	}
	_, _ = fmt.Fprintf(
		writer,
		"mithril_agent_activation_expires_timestamp_seconds %d\n",
		expiresUnix,
	)
}

func boolMetric(value bool) int {
	if value {
		return 1
	}
	return 0
}

func knownDecision(value string) bool {
	for _, decision := range decisions {
		if strings.EqualFold(value, decision) {
			return true
		}
	}
	return false
}

func knownFailureCategory(value string) bool {
	for _, category := range failureCategories {
		if value == category && value != "unknown" {
			return true
		}
	}
	return false
}

func knownControlMode(value string) bool {
	return value == control.ModeNoNewActions || value == control.ModeDevnetEnabled
}

func boundedDecision(value string) string {
	if value == "" {
		return ""
	}
	if knownDecision(value) {
		return value
	}
	return "unknown"
}

func boundedVerdict(value string) string {
	if value == "" {
		return "none"
	}
	for _, verdict := range actionVerdicts {
		if value == verdict && value != "none" && value != "unknown" {
			return value
		}
	}
	return "unknown"
}
