// Package paperdashboard serves the bounded paper-status projection as a
// read-only local dashboard. It never reads journals, configuration, or keys.
package paperdashboard

import (
	"crypto/sha256"
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

type Source interface {
	SourceLabel() string
	Read() (paperstatus.Snapshot, error)
}

type Server struct {
	sources         []Source
	instructionPath string
	researchPath    string
	now             func() time.Time
	mu              sync.Mutex
	cached          View
	readAt          time.Time
}

type View struct {
	Mode                string       `json:"mode"`
	ObservedAt          *time.Time   `json:"observed_at,omitempty"`
	Complete            bool         `json:"complete"`
	Overview            Overview     `json:"overview"`
	Markets             []Market     `json:"markets"`
	Activity            []Activity   `json:"activity"`
	InstructionsEnabled bool         `json:"instructions_enabled"`
	Instruction         *Instruction `json:"instruction,omitempty"`
	InstructionError    bool         `json:"instruction_error,omitempty"`
	ResearchEnabled     bool         `json:"research_enabled"`
	Research            *Research    `json:"research,omitempty"`
	ResearchError       bool         `json:"research_error,omitempty"`
	ResearchMarkets     []string     `json:"research_markets"`
	// ActivityOmitted counts older bounded status events plus events removed by
	// the dashboard's own combined-list cap.
	ActivityOmitted uint64 `json:"activity_omitted"`
}

type Overview struct {
	ValueUnit           string `json:"value_unit,omitempty"`
	OpeningEquityMicros uint64 `json:"opening_equity_micros,omitempty,string"`
	EquityMicros        uint64 `json:"equity_micros,omitempty,string"`
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
	Name                        string             `json:"name"`
	ObservedAt                  *time.Time         `json:"observed_at,omitempty"`
	Available                   bool               `json:"available"`
	Ready                       bool               `json:"ready"`
	Fresh                       bool               `json:"fresh"`
	Current                     string             `json:"current,omitempty"`
	Day                         string             `json:"day,omitempty"`
	ValueUnit                   string             `json:"value_unit,omitempty"`
	TickSeconds                 uint64             `json:"tick_seconds,omitempty"`
	OpeningEquityMicros         uint64             `json:"opening_equity_micros,omitempty,string"`
	EquityMicros                uint64             `json:"equity_micros,omitempty,string"`
	HoldBenchmarkMicros         uint64             `json:"hold_benchmark_micros,omitempty,string"`
	AccountingTracked           bool               `json:"accounting_tracked,omitempty"`
	RealizedMicros              int64              `json:"realized_micros,omitempty,string"`
	UnrealizedMicros            int64              `json:"unrealized_micros,omitempty,string"`
	FeesMicros                  int64              `json:"fees_micros,omitempty,string"`
	TurnoverMicros              uint64             `json:"turnover_micros,omitempty,string"`
	DrawdownMicros              uint64             `json:"drawdown_micros,omitempty,string"`
	MaxDrawdownMicros           uint64             `json:"max_drawdown_micros,omitempty,string"`
	PriceMicros                 uint64             `json:"price_micros,omitempty,string"`
	Checks                      uint64             `json:"checks,omitempty"`
	Signals                     uint64             `json:"signals,omitempty"`
	Trades                      uint64             `json:"trades,omitempty"`
	CoverageBPS                 uint64             `json:"coverage_bps,omitempty"`
	CoverageReady               bool               `json:"coverage_ready"`
	State                       string             `json:"state,omitempty"`
	Strategy                    string             `json:"strategy,omitempty"`
	NextAction                  string             `json:"next_action,omitempty"`
	DecisionReason              string             `json:"decision_reason,omitempty"`
	RiskHalted                  bool               `json:"risk_halted,omitempty"`
	InitialLotUnits             uint64             `json:"initial_lot_units,omitempty,string"`
	InitialLotDecimals          uint8              `json:"initial_lot_decimals,omitempty"`
	InitialLotAsset             string             `json:"initial_lot_asset,omitempty"`
	FeeReserveLamports          uint64             `json:"fee_reserve_lamports,omitempty,string"`
	FeeLamports                 uint64             `json:"fee_lamports,omitempty,string"`
	FeeBudgetTracked            bool               `json:"fee_budget_tracked,omitempty"`
	RemainingFeeReserveLamports uint64             `json:"remaining_fee_reserve_lamports,omitempty,string"`
	EstimatedFillsRemaining     uint64             `json:"estimated_fills_remaining,omitempty"`
	SlippageBPS                 uint16             `json:"slippage_bps,omitempty"`
	SettleSeconds               uint64             `json:"settle_seconds,omitempty"`
	FastWindow                  uint16             `json:"fast_window,omitempty"`
	SlowWindow                  uint16             `json:"slow_window,omitempty"`
	MinimumSignalBPS            uint16             `json:"minimum_signal_bps,omitempty"`
	MaxVolatilityBPS            uint16             `json:"max_volatility_bps,omitempty"`
	MaxQuoteImpactBPS           uint16             `json:"max_quote_impact_bps,omitempty"`
	MaxDrawdownBPS              uint16             `json:"max_drawdown_bps,omitempty"`
	CooldownSeconds             uint64             `json:"cooldown_seconds,omitempty"`
	History                     []PerformancePoint `json:"history,omitempty"`
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
		serveAsset(writer, request, "text/css; charset=utf-8", appCSS+mobileCSS+refinedCSS+finishingCSS+narrowCSS+observabilityCSS+qaCSS+finalCSS+clarityCSS+controlCSS)
	case "/app.js":
		serveAsset(writer, request, "text/javascript; charset=utf-8", appJS)
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
		InstructionsEnabled: s.instructionPath != "",
		ResearchEnabled:     s.researchPath != "",
		ResearchMarkets:     marketadmission.Markets(),
		Markets:             make([]Market, 0, len(s.sources)), Activity: make([]Activity, 0),
	}
	if s.instructionPath != "" {
		instruction, err := readInstruction(s.instructionPath)
		if err == nil {
			view.Instruction = instruction
		} else if !errors.Is(err, os.ErrNotExist) {
			view.InstructionError = true
		}
	}
	if s.researchPath != "" {
		research, err := readResearch(s.researchPath, now)
		if err == nil {
			view.Research = research
		} else if !errors.Is(err, os.ErrNotExist) {
			view.ResearchError = true
		}
	}
	minimumCoverage := uint64(10_000)
	coverageReady := true
	for _, source := range s.sources {
		label := source.SourceLabel()
		snapshot, err := source.Read()
		if err != nil || paperstatus.ValidateSnapshot(snapshot) != nil {
			view.Complete = false
			view.Markets = append(view.Markets, Market{Name: label})
			coverageReady = false
			continue
		}
		if snapshot.Summary != nil && snapshot.Summary.Market != label {
			view.Complete = false
			view.Markets = append(view.Markets, Market{Name: label})
			coverageReady = false
			continue
		}
		market := marketView(label, snapshot, now)
		view.Markets = append(view.Markets, market)
		if !market.Fresh {
			view.Complete = false
		}
		if market.Ready && (view.ObservedAt == nil || snapshot.ObservedAt.Before(*view.ObservedAt)) {
			observedAt := snapshot.ObservedAt
			view.ObservedAt = &observedAt
		}
		if !market.Fresh || !addOverview(&view.Overview, *snapshot.Summary) {
			view.Complete = false
			coverageReady = false
		} else if market.CoverageReady && market.CoverageBPS < minimumCoverage {
			minimumCoverage = market.CoverageBPS
		} else if !market.CoverageReady {
			coverageReady = false
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
	sort.SliceStable(view.Activity, func(i, j int) bool {
		return view.Activity[i].At.After(view.Activity[j].At)
	})
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

func marketView(label string, snapshot paperstatus.Snapshot, now time.Time) Market {
	observedAt := snapshot.ObservedAt
	market := Market{
		Name: label, ObservedAt: &observedAt, Available: true,
		Current: snapshot.Current, History: make([]PerformancePoint, 0, len(snapshot.History)),
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
		market.Ready = summary.ValueUnit != ""
		market.Fresh = market.Ready && summary.State != "waiting for data" &&
			summary.Day == now.UTC().Format("2006-01-02") && fresh(snapshot, now)
		market.Day = summary.Day
		market.ValueUnit = summary.ValueUnit
		market.TickSeconds = summary.TickSeconds
		market.OpeningEquityMicros = summary.OpeningEquityMicros
		market.EquityMicros = summary.EquityMicros
		market.HoldBenchmarkMicros = summary.HoldBenchmarkMicros
		market.AccountingTracked = summary.AccountingTracked
		market.RealizedMicros = summary.RealizedMicros
		market.UnrealizedMicros = summary.UnrealizedMicros
		market.FeesMicros = summary.FeesMicros
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
		market.NextAction = summary.NextAction
		market.DecisionReason = summary.DecisionReason
		market.RiskHalted = summary.RiskHalted
		market.InitialLotUnits = summary.InitialLotUnits
		market.InitialLotDecimals = summary.InitialLotDecimals
		market.InitialLotAsset = summary.InitialLotAsset
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
	}
	return market
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
	if summary.ValueUnit == "" || overview.ValueUnit != "" && overview.ValueUnit != summary.ValueUnit {
		return false
	}
	if overview.OpeningEquityMicros > math.MaxUint64-summary.OpeningEquityMicros ||
		overview.EquityMicros > math.MaxUint64-summary.EquityMicros ||
		overview.HoldBenchmarkMicros > math.MaxUint64-summary.HoldBenchmarkMicros ||
		overview.TurnoverMicros > math.MaxUint64-summary.TurnoverMicros ||
		overview.Signals > math.MaxUint64-summary.Signals ||
		overview.Trades > math.MaxUint64-summary.Trades {
		return false
	}
	tracked := summary.AccountingTracked
	if overview.ValueUnit != "" {
		tracked = overview.AccountingTracked && tracked
	}
	if tracked && (!addSigned(&overview.RealizedMicros, summary.RealizedMicros) ||
		!addSigned(&overview.UnrealizedMicros, summary.UnrealizedMicros) ||
		!addSigned(&overview.FeesMicros, summary.FeesMicros)) {
		return false
	}
	if !tracked {
		overview.RealizedMicros, overview.UnrealizedMicros, overview.FeesMicros = 0, 0, 0
	}
	overview.AccountingTracked = tracked
	overview.OpeningEquityMicros += summary.OpeningEquityMicros
	overview.EquityMicros += summary.EquityMicros
	overview.HoldBenchmarkMicros += summary.HoldBenchmarkMicros
	overview.TurnoverMicros += summary.TurnoverMicros
	overview.Signals += summary.Signals
	overview.Trades += summary.Trades
	overview.ValueUnit = summary.ValueUnit
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
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", contentType)
	if request.Method == http.MethodGet {
		_, _ = writer.Write([]byte(body))
	}
}

func setSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", "default-src 'none'; connect-src 'self'; script-src 'self'; style-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
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
