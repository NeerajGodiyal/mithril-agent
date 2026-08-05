package agent

import "time"

const (
	ProfileTreasurySweepV1 = "treasury_sweep_v1"

	EventActionProposed       = "action.proposed"
	EventActionShadowProposed = "action.shadow_proposed"
	EventActionShadowed       = "action.shadowed"
)

type Profile struct {
	Name                         string `json:"name"`
	Version                      uint32 `json:"version"`
	Cluster                      string `json:"cluster"`
	Source                       string `json:"source"`
	Destination                  string `json:"destination"`
	ReserveLamports              uint64 `json:"reserve_lamports"`
	MinTransferLamports          uint64 `json:"min_transfer_lamports"`
	MaxTransferLamports          uint64 `json:"max_transfer_lamports"`
	DailyCapLamports             uint64 `json:"daily_cap_lamports"`
	MaxFeeLamports               uint64 `json:"max_fee_lamports"`
	ScheduleWindowSeconds        uint64 `json:"schedule_window_seconds"`
	ScheduleAnchorUnix           int64  `json:"schedule_anchor_unix"`
	MaxClockUncertaintyMillis    uint64 `json:"max_clock_uncertainty_millis"`
	MaxObservationAgeSeconds     uint64 `json:"max_observation_age_seconds"`
	MinHealthyObservationSeconds uint64 `json:"min_healthy_observation_seconds"`
	MinHealthySlotAdvance        uint64 `json:"min_healthy_slot_advance"`
	MaxNodeLagSlots              uint64 `json:"max_node_lag_slots"`
	MaxReconciliationSeconds     uint64 `json:"max_reconciliation_seconds"`
}

type Observation struct {
	Cluster         string    `json:"cluster"`
	Source          string    `json:"source"`
	BalanceLamports uint64    `json:"balance_lamports"`
	Slot            uint64    `json:"slot"`
	ObservedAt      time.Time `json:"observed_at"`
	EvidenceSource  string    `json:"evidence_source,omitempty"`
	Finality        string    `json:"finality,omitempty"`
	Consistency     string    `json:"consistency,omitempty"`
}

type NodeHealth struct {
	Status              string          `json:"status"`
	AssessmentScope     string          `json:"assessment_scope"`
	ObservedAt          time.Time       `json:"observed_at"`
	SafeForAutomation   bool            `json:"safe_for_automation"`
	EvidenceComplete    bool            `json:"evidence_complete"`
	DivergenceArtifacts int             `json:"divergence_artifacts"`
	Issues              []HealthIssue   `json:"issues,omitempty"`
	CrossCheck          *SlotComparison `json:"cross_check,omitempty"`
}

type HealthIssue struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type SlotComparison struct {
	MithrilSlot         uint64 `json:"mithril_slot"`
	ReferenceSlot       uint64 `json:"reference_slot"`
	SlotsBehind         int64  `json:"slots_behind"`
	ReferenceCommitment string `json:"reference_commitment"`
	MithrilView         string `json:"mithril_view"`
	Threshold           uint64 `json:"threshold"`
	Status              string `json:"status"`
}

type NodeObservation struct {
	Account Observation `json:"account"`
	Health  NodeHealth  `json:"health"`
}

type Proposal struct {
	ActionID                 string `json:"action_id"`
	Profile                  string `json:"profile"`
	ProfileVersion           uint32 `json:"profile_version"`
	Cluster                  string `json:"cluster"`
	Source                   string `json:"source"`
	Destination              string `json:"destination"`
	AmountLamports           uint64 `json:"amount_lamports"`
	FeeBudgetLamports        uint64 `json:"fee_budget_lamports"`
	ReservedLamports         uint64 `json:"reserved_lamports"`
	ReserveLamports          uint64 `json:"reserve_lamports"`
	ObservedBalanceLamports  uint64 `json:"observed_balance_lamports"`
	ObservationSlot          uint64 `json:"observation_slot"`
	ObservationUnix          int64  `json:"observation_unix"`
	ReservationDayUTC        string `json:"reservation_day_utc"`
	ProfileFingerprint       string `json:"profile_sha256"`
	ScheduleWindowStartUnix  int64  `json:"schedule_window_start_unix"`
	ScheduleWindowEndUnix    int64  `json:"schedule_window_end_unix"`
	MaxObservationAgeSeconds uint64 `json:"max_observation_age_seconds"`
	MaxNodeLagSlots          uint64 `json:"max_node_lag_slots"`
	MaxReconciliationSeconds uint64 `json:"max_reconciliation_seconds"`
}

type ShadowResult struct {
	ActionID       string `json:"action_id,omitempty"`
	Decision       string `json:"decision"`
	Reason         string `json:"reason,omitempty"`
	AmountLamports uint64 `json:"amount_lamports,omitempty"`
	JournalSeq     uint64 `json:"journal_seq,omitempty"`
	Recovered      bool   `json:"recovered,omitempty"`
}
