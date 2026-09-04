// Package paperdashboard serves bounded paper status and accepts validated,
// paper-only instructions. It never reads journals or keys.
package paperdashboard

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/bits"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Overclock-Validator/mithril-agent/marketadmission"
	"github.com/Overclock-Validator/mithril-agent/paperstatus"
)

const maxActivity = 100

const refreshInterval = 10 * time.Second

//go:embed vendor/lightweight-charts-5.2.1.min.js
var lightweightChartsJS string

//go:embed vendor/space-grotesk-latin.woff2
var spaceGroteskFont []byte

//go:embed vendor/overclock.svg
var overclockLogo []byte

type Source interface {
	SourceLabel() string
	Read() (paperstatus.Snapshot, error)
}

type optionalSource struct{ Source }

func (optionalSource) Optional() bool { return true }

// Optional keeps a bounded experiment visible without letting its expected
// absence or expiry erase the aggregate for required paper markets.
func Optional(source Source) Source { return optionalSource{Source: source} }

func sourceOptional(source Source) bool {
	optional, ok := source.(interface{ Optional() bool })
	return ok && optional.Optional()
}

type Server struct {
	sources             []Source
	instructionPath     string
	researchPath        string
	mithrilEvidencePath string
	marketAdmissionPath string
	now                 func() time.Time
	mu                  sync.Mutex
	cached              View
	readAt              time.Time
}

type View struct {
	Mode                    string           `json:"mode"`
	ObservedAt              *time.Time       `json:"observed_at,omitempty"`
	Complete                bool             `json:"complete"`
	Overview                Overview         `json:"overview"`
	Markets                 []Market         `json:"markets"`
	Activity                []Activity       `json:"activity"`
	InstructionsEnabled     bool             `json:"instructions_enabled"`
	Instruction             *Instruction     `json:"instruction,omitempty"`
	InstructionSHA256       string           `json:"instruction_sha256,omitempty"`
	ActiveInstructionSHA256 string           `json:"active_instruction_sha256,omitempty"`
	InstructionActive       bool             `json:"instruction_active"`
	InstructionError        bool             `json:"instruction_error,omitempty"`
	ResearchEnabled         bool             `json:"research_enabled"`
	Research                *Research        `json:"research,omitempty"`
	ResearchError           bool             `json:"research_error,omitempty"`
	MithrilEvidenceEnabled  bool             `json:"mithril_evidence_enabled"`
	MithrilEvidence         *MithrilEvidence `json:"mithril_evidence,omitempty"`
	MithrilEvidenceError    bool             `json:"mithril_evidence_error,omitempty"`
	MarketResearchEnabled   bool             `json:"market_research_enabled"`
	MarketResearch          []MarketResearch `json:"market_research,omitempty"`
	MarketResearchError     bool             `json:"market_research_error,omitempty"`
	ResearchMarkets         []string         `json:"research_markets"`
	// ActivityOmitted counts older bounded status events plus events removed by
	// the dashboard's own combined-list cap.
	ActivityOmitted uint64 `json:"activity_omitted"`
}

type Overview struct {
	ValueUnit           string `json:"value_unit,omitempty"`
	OpeningEquityMicros uint64 `json:"opening_equity_micros,omitempty,string"`
	EquityMicros        uint64 `json:"equity_micros,omitempty,string"`
	DeficitMicros       uint64 `json:"deficit_micros,omitempty,string"`
	HoldBenchmarkMicros uint64 `json:"hold_benchmark_micros,omitempty,string"`
	AccountingTracked   bool   `json:"accounting_tracked,omitempty"`
	RealizedMicros      int64  `json:"realized_micros,omitempty,string"`
	UnrealizedMicros    int64  `json:"unrealized_micros,omitempty,string"`
	FeesMicros          int64  `json:"fees_micros,omitempty,string"`
	TurnoverMicros      uint64 `json:"turnover_micros,omitempty,string"`
	Signals             uint64 `json:"signals"`
	Trades              uint64 `json:"trades"`
	CoverageBPS         uint64 `json:"coverage_bps,omitempty"`
	CoverageReady       bool   `json:"coverage_ready"`
}

type Market struct {
	Name                          string             `json:"name"`
	Optional                      bool               `json:"optional,omitempty"`
	Completed                     bool               `json:"completed,omitempty"`
	LatestCompleted               *Market            `json:"latest_completed,omitempty"`
	Instrument                    string             `json:"instrument,omitempty"`
	RiskProfile                   string             `json:"risk_profile,omitempty"`
	PositionDirection             string             `json:"position_direction,omitempty"`
	LeverageBPS                   uint32             `json:"leverage_bps,omitempty"`
	ObservedAt                    *time.Time         `json:"observed_at,omitempty"`
	Available                     bool               `json:"available"`
	Ready                         bool               `json:"ready"`
	Fresh                         bool               `json:"fresh"`
	Current                       string             `json:"current,omitempty"`
	Day                           string             `json:"day,omitempty"`
	ValueUnit                     string             `json:"value_unit,omitempty"`
	InstructionSHA256             string             `json:"instruction_sha256,omitempty"`
	TickSeconds                   uint64             `json:"tick_seconds,omitempty"`
	OpeningEquityMicros           uint64             `json:"opening_equity_micros,omitempty,string"`
	EquityMicros                  uint64             `json:"equity_micros,omitempty,string"`
	DeficitMicros                 uint64             `json:"deficit_micros,omitempty,string"`
	HoldBenchmarkMicros           uint64             `json:"hold_benchmark_micros,omitempty,string"`
	AccountingTracked             bool               `json:"accounting_tracked,omitempty"`
	RealizedMicros                int64              `json:"realized_micros,omitempty,string"`
	UnrealizedMicros              int64              `json:"unrealized_micros,omitempty,string"`
	FeesMicros                    int64              `json:"fees_micros,omitempty,string"`
	FundingTracked                bool               `json:"funding_tracked,omitempty"`
	FundingMicros                 int64              `json:"funding_micros,omitempty,string"`
	TurnoverMicros                uint64             `json:"turnover_micros,omitempty,string"`
	DrawdownMicros                uint64             `json:"drawdown_micros,omitempty,string"`
	MaxDrawdownMicros             uint64             `json:"max_drawdown_micros,omitempty,string"`
	PriceMicros                   uint64             `json:"price_micros,omitempty,string"`
	Checks                        uint64             `json:"checks,omitempty"`
	Signals                       uint64             `json:"signals,omitempty"`
	Trades                        uint64             `json:"trades,omitempty"`
	CoverageBPS                   uint64             `json:"coverage_bps,omitempty"`
	CoverageReady                 bool               `json:"coverage_ready"`
	State                         string             `json:"state,omitempty"`
	Strategy                      string             `json:"strategy,omitempty"`
	DecisionSource                string             `json:"decision_source,omitempty"`
	ProposalSource                string             `json:"proposal_source,omitempty"`
	PerpsPlanOutcome              string             `json:"perps_plan_outcome,omitempty"`
	NextAction                    string             `json:"next_action,omitempty"`
	DecisionReason                string             `json:"decision_reason,omitempty"`
	DecisionSignalKind            string             `json:"decision_signal_kind,omitempty"`
	DecisionSignalBPS             int64              `json:"decision_signal_bps,omitempty,string"`
	DecisionThresholdBPS          int64              `json:"decision_threshold_bps,omitempty,string"`
	MinimumResearchFrames         uint64             `json:"minimum_research_frames,omitempty"`
	RiskHalted                    bool               `json:"risk_halted,omitempty"`
	InitialLotUnits               uint64             `json:"initial_lot_units,omitempty,string"`
	InitialLotDecimals            uint8              `json:"initial_lot_decimals,omitempty"`
	InitialLotAsset               string             `json:"initial_lot_asset,omitempty"`
	MinimumOrderValueMicros       uint64             `json:"minimum_order_value_micros,omitempty,string"`
	MaximumOrderValueMicros       uint64             `json:"maximum_order_value_micros,omitempty,string"`
	FeeReserveLamports            uint64             `json:"fee_reserve_lamports,omitempty,string"`
	FeeLamports                   uint64             `json:"fee_lamports,omitempty,string"`
	FeeBudgetTracked              bool               `json:"fee_budget_tracked,omitempty"`
	RemainingFeeReserveLamports   uint64             `json:"remaining_fee_reserve_lamports,omitempty,string"`
	EstimatedFillsRemaining       uint64             `json:"estimated_fills_remaining,omitempty"`
	SlippageBPS                   uint16             `json:"slippage_bps,omitempty"`
	SettleSeconds                 uint64             `json:"settle_seconds,omitempty"`
	FastWindow                    uint16             `json:"fast_window,omitempty"`
	SlowWindow                    uint16             `json:"slow_window,omitempty"`
	MinimumSignalBPS              uint16             `json:"minimum_signal_bps,omitempty"`
	MaxVolatilityBPS              uint16             `json:"max_volatility_bps,omitempty"`
	MaxQuoteImpactBPS             uint16             `json:"max_quote_impact_bps,omitempty"`
	MaxDrawdownBPS                uint16             `json:"max_drawdown_bps,omitempty"`
	CooldownSeconds               uint64             `json:"cooldown_seconds,omitempty"`
	QualificationTracked          bool               `json:"qualification_tracked,omitempty"`
	QualificationOutcome          string             `json:"qualification_outcome,omitempty"`
	QualificationTapes            uint64             `json:"qualification_tapes,omitempty"`
	QualificationFrames           uint64             `json:"qualification_frames,omitempty"`
	QualificationMinimumFrames    uint64             `json:"qualification_minimum_frames,omitempty"`
	QualificationTrainingFrames   uint64             `json:"qualification_training_frames,omitempty"`
	QualificationHoldoutFrames    uint64             `json:"qualification_holdout_frames,omitempty"`
	QualificationStrategy         string             `json:"qualification_strategy,omitempty"`
	QualificationRiskProfile      string             `json:"qualification_risk_profile,omitempty"`
	QualificationHoldoutEvaluated bool               `json:"qualification_holdout_evaluated,omitempty"`
	QualificationStressEvaluated  bool               `json:"qualification_stress_evaluated,omitempty"`
	QualificationHoldoutScored    bool               `json:"qualification_holdout_scored,omitempty"`
	QualificationStressScored     bool               `json:"qualification_stress_scored,omitempty"`
	QualificationHoldoutMicros    int64              `json:"qualification_holdout_micros,omitempty,string"`
	QualificationStressMicros     int64              `json:"qualification_stress_micros,omitempty,string"`
	QualificationAttempts         []TrainingAttempt  `json:"qualification_attempts,omitempty"`
	History                       []PerformancePoint `json:"history,omitempty"`
}

// TrainingAttempt is read-only evidence from a completed paper replay.
// It must never be presented as an approved or selected trading plan.
type TrainingAttempt struct {
	RiskProfile       string `json:"risk_profile"`
	Strategy          string `json:"strategy"`
	NetPnLMicros      int64  `json:"net_pnl_micros,string"`
	FeesMicros        uint64 `json:"fees_micros,string"`
	FundingMicros     int64  `json:"funding_micros,string"`
	MaxDrawdownMicros uint64 `json:"max_drawdown_micros,string"`
	Liquidations      uint64 `json:"liquidations"`
	FilledOrders      uint64 `json:"filled_orders"`
	ClosedPositions   uint64 `json:"closed_positions"`
}

type PerformancePoint struct {
	At                  time.Time `json:"at"`
	PriceMicros         uint64    `json:"price_micros,omitempty,string"`
	EquityMicros        uint64    `json:"equity_micros,string"`
	HoldBenchmarkMicros uint64    `json:"hold_benchmark_micros,string"`
	DrawdownMicros      uint64    `json:"drawdown_micros,string"`
	MaxDrawdownMicros   uint64    `json:"max_drawdown_micros,string"`
	Unavailable         bool      `json:"unavailable,omitempty"`
}

type Activity struct {
	Market  string    `json:"market"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
}

func New(sources []Source) (*Server, error) {
	if len(sources) == 0 {
		return nil, errors.New("at least one paper status source is required")
	}
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if source == nil || !validLabel(source.SourceLabel()) {
			return nil, errors.New("paper status source is invalid")
		}
		label := source.SourceLabel()
		if _, duplicate := seen[label]; duplicate {
			return nil, errors.New("paper status source label is duplicated")
		}
		seen[label] = struct{}{}
	}
	return &Server{sources: append([]Source(nil), sources...), now: time.Now}, nil
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setSecurityHeaders(writer.Header())
	if !validHost(request.Host) {
		http.Error(writer, "invalid host", http.StatusBadRequest)
		return
	}
	switch request.URL.Path {
	case "/":
		serveAsset(writer, request, "text/html; charset=utf-8", indexHTML)
	case "/app.css":
		serveAsset(writer, request, "text/css; charset=utf-8", dashboardCSS)
	case "/app.js":
		serveAsset(writer, request, "text/javascript; charset=utf-8", appJS)
	case "/vendor/lightweight-charts-5.2.1.js":
		serveAsset(writer, request, "text/javascript; charset=utf-8", lightweightChartsJS)
	case "/vendor/space-grotesk-latin.woff2":
		serveBytesAsset(writer, request, "font/woff2", spaceGroteskFont)
	case "/vendor/overclock.svg":
		serveBytesAsset(writer, request, "image/svg+xml", overclockLogo)
	case "/api/v1/status":
		s.serveStatus(writer, request)
	case "/api/v1/instruction":
		s.serveInstruction(writer, request)
	case "/healthz":
		s.serveHealth(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (s *Server) serveStatus(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	view := s.snapshotWithRefresh(request.URL.Query().Get("fresh") == "1")
	encoded, err := json.Marshal(view)
	if err != nil {
		http.Error(writer, "status unavailable", http.StatusServiceUnavailable)
		return
	}
	digest := sha256.Sum256(encoded)
	etag := `"` + hex.EncodeToString(digest[:]) + `"`
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("ETag", etag)
	if request.Header.Get("If-None-Match") == etag {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	if request.Method == http.MethodHead {
		return
	}
	_, _ = writer.Write(append(encoded, '\n'))
}

func (s *Server) serveHealth(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	view := s.snapshot()
	status := http.StatusOK
	if len(view.Markets) == 0 || !view.Complete {
		status = http.StatusServiceUnavailable
	}
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(status)
	if request.Method == http.MethodGet {
		_, _ = writer.Write([]byte(http.StatusText(status) + "\n"))
	}
}

func (s *Server) snapshot() View {
	return s.snapshotWithRefresh(false)
}

func (s *Server) snapshotWithRefresh(force bool) View {
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !force && !s.readAt.IsZero() && now.Sub(s.readAt) >= 0 && now.Sub(s.readAt) < refreshInterval {
		return s.cached
	}
	s.cached, s.readAt = s.readSnapshot(now), now
	return s.cached
}

func (s *Server) readSnapshot(now time.Time) View {
	view := View{
		Mode: "paper", Complete: true,
		InstructionsEnabled:    s.instructionPath != "",
		ResearchEnabled:        s.researchPath != "",
		MithrilEvidenceEnabled: s.mithrilEvidencePath != "",
		MarketResearchEnabled:  s.marketAdmissionPath != "",
		ResearchMarkets:        marketadmission.Markets(),
		Markets:                make([]Market, 0, len(s.sources)), Activity: make([]Activity, 0),
	}
	if s.instructionPath != "" {
		instruction, digest, err := LoadInstruction(s.instructionPath)
		if err == nil {
			view.Instruction = instruction
			view.InstructionSHA256 = digest
		} else if !errors.Is(err, os.ErrNotExist) {
			view.InstructionError = true
		}
	}
	if s.researchPath != "" {
		research, err := readResearch(s.researchPath, now)
		if err == nil {
			view.Research = research
		} else if !errors.Is(err, os.ErrNotExist) || errors.Is(err, errResearchEvidenceUnavailable) {
			view.ResearchError = true
		}
	}
	if s.mithrilEvidencePath != "" {
		evidence, err := readMithrilEvidence(s.mithrilEvidencePath, now)
		if err == nil {
			view.MithrilEvidence = evidence
		} else if !errors.Is(err, os.ErrNotExist) {
			view.MithrilEvidenceError = true
		}
	}
	if s.marketAdmissionPath != "" {
		markets, err := readMarketAdmission(s.marketAdmissionPath, now)
		if err == nil {
			view.MarketResearch = markets
		} else if !errors.Is(err, os.ErrNotExist) {
			view.MarketResearchError = true
		}
	}
	minimumCoverage := uint64(10_000)
	coverageReady := true
	activeInstructions := make(map[string]struct{})
	for _, source := range s.sources {
		label := source.SourceLabel()
		optional := sourceOptional(source)
		snapshot, err := source.Read()
		if err != nil || paperstatus.ValidateSnapshot(snapshot) != nil {
			if !optional {
				view.Complete = false
				coverageReady = false
			}
			view.Markets = append(view.Markets, Market{Name: label, Optional: optional})
			continue
		}
		completed, hasCompleted := paperstatus.LatestCompletedSnapshot(snapshot)
		if snapshot.Summary != nil && snapshot.Summary.Market != label ||
			hasCompleted && completed.Summary.Market != label {
			if !optional {
				view.Complete = false
				coverageReady = false
			}
			view.Markets = append(view.Markets, Market{Name: label, Optional: optional})
			continue
		}
		if snapshot.Summary == nil && snapshot.Current == paperstatus.UnconfiguredCurrent && !hasCompleted {
			view.Markets = append(view.Markets, Market{Name: label, Optional: optional})
			continue
		}
		if snapshot.Summary == nil {
			if !optional {
				view.Complete = false
				coverageReady = false
			}
			market := marketView(label, snapshot, now)
			market.Optional = optional
			view.Markets = append(view.Markets, market)
			continue
		}
		market := marketView(label, snapshot, now)
		market.Optional = optional
		view.Markets = append(view.Markets, market)
		if snapshot.Summary != nil && !optional {
			if digest := snapshot.Summary.InstructionSHA256; digest != "" {
				activeInstructions[digest] = struct{}{}
			}
		}
		if !market.Fresh && !optional {
			view.Complete = false
		}
		if market.Fresh && !optional && (view.ObservedAt == nil || snapshot.ObservedAt.Before(*view.ObservedAt)) {
			observedAt := snapshot.ObservedAt
			view.ObservedAt = &observedAt
		}
		if !market.Fresh {
			if !optional {
				view.Complete = false
				coverageReady = false
			}
		} else if !optional {
			if !addOverview(&view.Overview, *snapshot.Summary) {
				view.Complete = false
				coverageReady = false
			} else if market.CoverageReady && market.CoverageBPS < minimumCoverage {
				minimumCoverage = market.CoverageBPS
			} else if !market.CoverageReady {
				coverageReady = false
			}
		}
		for _, event := range snapshot.Events {
			view.Activity = append(view.Activity, Activity{
				Market: label, At: event.At, Kind: event.Kind, Message: event.Message,
			})
		}
		if view.ActivityOmitted > math.MaxUint64-snapshot.DroppedEvents {
			view.ActivityOmitted = math.MaxUint64
		} else {
			view.ActivityOmitted += snapshot.DroppedEvents
		}
	}
	view.Overview.CoverageReady = coverageReady
	if coverageReady {
		view.Overview.CoverageBPS = minimumCoverage
	}
	if len(activeInstructions) == 1 {
		for digest := range activeInstructions {
			view.ActiveInstructionSHA256 = digest
		}
	} else if len(activeInstructions) > 1 {
		view.Complete = false
	}
	view.InstructionActive = view.Complete && view.InstructionSHA256 != "" &&
		view.InstructionSHA256 == view.ActiveInstructionSHA256
	sort.SliceStable(view.Activity, func(i, j int) bool {
		return view.Activity[i].At.After(view.Activity[j].At)
	})
	view.Activity = coalesceTerminalActivity(view.Activity)
	if len(view.Activity) > maxActivity {
		omitted := uint64(len(view.Activity) - maxActivity)
		if view.ActivityOmitted > math.MaxUint64-omitted {
			view.ActivityOmitted = math.MaxUint64
		} else {
			view.ActivityOmitted += omitted
		}
		view.Activity = view.Activity[:maxActivity]
	}
	if !view.Complete {
		view.Overview = Overview{}
	}
	return view
}

func coalesceTerminalActivity(activity []Activity) []Activity {
	type terminalKey struct {
		market  string
		at      time.Time
		message string
	}
	seen := make(map[terminalKey]string)
	result := activity[:0]
	for _, item := range activity {
		if item.Kind == paperstatus.KindPeriodClosed || item.Kind == paperstatus.KindExperimentDone {
			key := terminalKey{market: item.Market, at: item.At, message: item.Message}
			if kind, ok := seen[key]; ok && kind != item.Kind {
				continue
			}
			seen[key] = item.Kind
		}
		result = append(result, item)
	}
	return result
}

func marketView(label string, snapshot paperstatus.Snapshot, now time.Time) Market {
	observedAt := snapshot.ObservedAt
	completed, hasCompleted := paperstatus.LatestCompletedSnapshot(snapshot)
	terminalEvent := len(snapshot.Events) != 0 &&
		snapshot.Events[len(snapshot.Events)-1].Kind == paperstatus.KindExperimentDone
	market := Market{
		Name: label, ObservedAt: &observedAt, Available: true,
		Current: snapshot.Current, History: make([]PerformancePoint, 0, len(snapshot.History)),
	}
	if snapshot.Summary == nil && terminalEvent {
		market.Completed = true
	}
	for _, point := range snapshot.History {
		market.History = append(market.History, PerformancePoint{
			At: point.At, PriceMicros: point.PriceMicros, EquityMicros: point.EquityMicros,
			HoldBenchmarkMicros: point.HoldBenchmarkMicros,
			DrawdownMicros:      point.DrawdownMicros, MaxDrawdownMicros: point.MaxDrawdownMicros,
			Unavailable: point.Unavailable,
		})
	}
	if summary := snapshot.Summary; summary != nil {
		market.Completed = summary.State == "completed" || terminalEvent && hasCompleted &&
			completed.ObservedAt.Equal(snapshot.ObservedAt) && summary.QualificationTracked &&
			summary.QualificationSHA256 == completed.Summary.QualificationSHA256
		market.Ready = summary.ValueUnit != ""
		market.Fresh = market.Ready && summary.State != "waiting for data" && summary.State != "completed" &&
			summary.Day == now.UTC().Format("2006-01-02") && fresh(snapshot, now)
		applyMarketSummary(&market, *summary)
	}
	if hasCompleted {
		completedAt := completed.ObservedAt
		latest := Market{
			Name: label, ObservedAt: &completedAt, Available: true,
			Ready: completed.Summary.ValueUnit != "", Completed: true,
		}
		applyMarketSummary(&latest, completed.Summary)
		latest.InstructionSHA256 = ""
		latest.State = "completed"
		market.LatestCompleted = &latest
	}
	return market
}

func applyMarketSummary(market *Market, summary paperstatus.CurrentSummary) {
	market.Instrument = summary.Instrument
	market.RiskProfile = summary.RiskProfile
	market.PositionDirection = summary.PositionDirection
	market.LeverageBPS = summary.LeverageBPS
	market.Day = summary.Day
	market.ValueUnit = summary.ValueUnit
	market.InstructionSHA256 = summary.InstructionSHA256
	market.TickSeconds = summary.TickSeconds
	market.OpeningEquityMicros = summary.OpeningEquityMicros
	market.EquityMicros = summary.EquityMicros
	market.DeficitMicros = summary.DeficitMicros
	market.HoldBenchmarkMicros = summary.HoldBenchmarkMicros
	market.AccountingTracked = summary.AccountingTracked
	market.RealizedMicros = summary.RealizedMicros
	market.UnrealizedMicros = summary.UnrealizedMicros
	market.FeesMicros = summary.FeesMicros
	market.FundingTracked = summary.FundingTracked
	market.FundingMicros = summary.FundingMicros
	market.TurnoverMicros = summary.TurnoverMicros
	market.DrawdownMicros = summary.DrawdownMicros
	market.MaxDrawdownMicros = summary.MaxDrawdownMicros
	market.PriceMicros = summary.PriceMicros
	market.Checks = summary.Checks
	market.Signals = summary.Signals
	market.Trades = summary.Trades
	market.CoverageBPS, market.CoverageReady = coverage(summary.Checks, summary.Unobservable)
	market.State = summary.State
	market.Strategy = summary.Strategy
	market.DecisionSource = summary.DecisionSource
	market.ProposalSource = summary.ProposalSource
	if summary.PerpsPlanOutcome != nil {
		market.PerpsPlanOutcome = summary.PerpsPlanOutcome.Result
	}
	market.NextAction = summary.NextAction
	market.DecisionReason = summary.DecisionReason
	market.DecisionSignalKind = summary.DecisionSignalKind
	market.DecisionSignalBPS = summary.DecisionSignalBPS
	market.DecisionThresholdBPS = summary.DecisionThresholdBPS
	market.MinimumResearchFrames = summary.MinimumResearchFrames
	market.RiskHalted = summary.RiskHalted
	market.InitialLotUnits = summary.InitialLotUnits
	market.InitialLotDecimals = summary.InitialLotDecimals
	market.InitialLotAsset = summary.InitialLotAsset
	market.MinimumOrderValueMicros = summary.MinimumOrderValueMicros
	market.MaximumOrderValueMicros = summary.MaximumOrderValueMicros
	market.FeeReserveLamports = summary.FeeReserveLamports
	market.FeeLamports = summary.FeeLamports
	market.FeeBudgetTracked = summary.FeeBudgetTracked
	market.RemainingFeeReserveLamports = summary.RemainingFeeReserveLamports
	market.EstimatedFillsRemaining = summary.EstimatedFillsRemaining
	market.SlippageBPS = summary.SlippageBPS
	market.SettleSeconds = summary.SettleSeconds
	market.FastWindow = summary.FastWindow
	market.SlowWindow = summary.SlowWindow
	market.MinimumSignalBPS = summary.MinimumSignalBPS
	market.MaxVolatilityBPS = summary.MaxVolatilityBPS
	market.MaxQuoteImpactBPS = summary.MaxQuoteImpactBPS
	market.MaxDrawdownBPS = summary.MaxDrawdownBPS
	market.CooldownSeconds = summary.CooldownSeconds
	market.QualificationTracked = summary.QualificationTracked
	market.QualificationOutcome = summary.QualificationOutcome
	market.QualificationTapes = summary.QualificationTapes
	market.QualificationFrames = summary.QualificationFrames
	market.QualificationMinimumFrames = summary.QualificationMinimumFrames
	market.QualificationTrainingFrames = summary.QualificationTrainingFrames
	market.QualificationHoldoutFrames = summary.QualificationHoldoutFrames
	market.QualificationStrategy = summary.QualificationStrategy
	market.QualificationRiskProfile = summary.QualificationRiskProfile
	market.QualificationHoldoutEvaluated = summary.QualificationHoldoutEvaluated
	market.QualificationStressEvaluated = summary.QualificationStressEvaluated
	market.QualificationHoldoutScored = summary.QualificationHoldoutScored
	market.QualificationStressScored = summary.QualificationStressScored
	market.QualificationHoldoutMicros = summary.QualificationHoldoutMicros
	market.QualificationStressMicros = summary.QualificationStressMicros
	for _, attempt := range summary.QualificationAttempts[:min(3, len(summary.QualificationAttempts))] {
		market.QualificationAttempts = append(market.QualificationAttempts, TrainingAttempt{
			RiskProfile: attempt.RiskProfile, Strategy: attempt.Strategy,
			NetPnLMicros: attempt.NetPnLMicros, FeesMicros: attempt.FeesMicros,
			FundingMicros: attempt.FundingMicros, MaxDrawdownMicros: attempt.MaxDrawdownMicros,
			Liquidations: attempt.Liquidations, FilledOrders: attempt.FilledOrders,
			ClosedPositions: attempt.ClosedPositions,
		})
	}
}

func fresh(snapshot paperstatus.Snapshot, now time.Time) bool {
	if snapshot.ObservedAt.After(now.Add(5*time.Second)) ||
		snapshot.ObservedAt.UTC().Format("2006-01-02") != now.UTC().Format("2006-01-02") {
		return false
	}
	limit := 5 * time.Minute
	if snapshot.Summary != nil {
		limit = max(2*time.Minute,
			3*time.Duration(snapshot.Summary.TickSeconds)*time.Second)
	}
	return !snapshot.ObservedAt.Before(now.Add(-limit))
}

func addOverview(overview *Overview, summary paperstatus.CurrentSummary) bool {
	next := *overview
	if summary.ValueUnit == "" || next.ValueUnit != "" && next.ValueUnit != summary.ValueUnit {
		return false
	}
	if next.OpeningEquityMicros > math.MaxUint64-summary.OpeningEquityMicros ||
		next.EquityMicros > math.MaxUint64-summary.EquityMicros ||
		next.DeficitMicros > math.MaxUint64-summary.DeficitMicros ||
		next.HoldBenchmarkMicros > math.MaxUint64-summary.HoldBenchmarkMicros ||
		next.TurnoverMicros > math.MaxUint64-summary.TurnoverMicros ||
		next.Signals > math.MaxUint64-summary.Signals ||
		next.Trades > math.MaxUint64-summary.Trades {
		return false
	}
	tracked := summary.AccountingTracked
	if next.ValueUnit != "" {
		tracked = next.AccountingTracked && tracked
	}
	if tracked && (!addSigned(&next.RealizedMicros, summary.RealizedMicros) ||
		!addSigned(&next.UnrealizedMicros, summary.UnrealizedMicros) ||
		!addSigned(&next.FeesMicros, summary.FeesMicros)) {
		return false
	}
	if !tracked {
		next.RealizedMicros, next.UnrealizedMicros, next.FeesMicros = 0, 0, 0
	}
	next.AccountingTracked = tracked
	next.OpeningEquityMicros += summary.OpeningEquityMicros
	next.EquityMicros += summary.EquityMicros
	next.DeficitMicros += summary.DeficitMicros
	next.HoldBenchmarkMicros += summary.HoldBenchmarkMicros
	next.TurnoverMicros += summary.TurnoverMicros
	next.Signals += summary.Signals
	next.Trades += summary.Trades
	next.ValueUnit = summary.ValueUnit
	*overview = next
	return true
}

func addSigned(total *int64, value int64) bool {
	if value > 0 && *total > math.MaxInt64-value ||
		value < 0 && *total < math.MinInt64-value {
		return false
	}
	*total += value
	return true
}

func coverage(checks, unavailable uint64) (uint64, bool) {
	if checks == 0 || unavailable > checks {
		return 0, false
	}
	high, low := bits.Mul64(checks-unavailable, 10_000)
	bps, _ := bits.Div64(high, low, checks)
	return bps, true
}

func serveAsset(writer http.ResponseWriter, request *http.Request, contentType, body string) {
	serveBytesAsset(writer, request, contentType, []byte(body))
}

func serveBytesAsset(writer http.ResponseWriter, request *http.Request, contentType string, body []byte) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", contentType)
	if request.Method == http.MethodGet {
		_, _ = writer.Write(body)
	}
}

func setSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", "default-src 'none'; connect-src 'self'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
}

func validLabel(label string) bool {
	if label == "" || len(label) > 32 || strings.TrimSpace(label) != label {
		return false
	}
	for _, character := range label {
		if character != '/' && character != '-' && character != '_' &&
			(character < '0' || character > '9') &&
			(character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') {
			return false
		}
	}
	return true
}

func validHost(value string) bool {
	host := value
	if parsed, _, err := net.SplitHostPort(value); err == nil {
		host = parsed
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
