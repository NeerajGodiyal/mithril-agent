package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const dataUnavailableAlertThreshold = 3

func (s *shadowRun) alertStrategy(at time.Time, kind string) error {
	if s.alerts == nil {
		return nil
	}
	title := "PLAN STARTED"
	label := "active"
	if kind == paperstatus.KindStrategyChanged {
		title = "PLAN UPDATED"
		label = "changed"
	} else {
		label = fmt.Sprintf("active/%d", s.activationSequence)
	}
	detail := "Starts with " + paperStartingSize(s.policy)
	if s.policy.Adaptive != nil {
		detail += " · safety limit " +
			formatShadowAmount(uint64(s.policy.Adaptive.MaxDrawdownBPS), 2) + "% below this run's high"
	}
	message := fmt.Sprintf("PAPER · 🧠 %s\n%s\n%s",
		title, paperStrategyLine(s.policy), detail)
	return s.appendAlert(at, kind, s.policySHA256+"/"+dayKey(at)+"/"+label, message)
}

func (s *shadowRun) alertTick(tick shadow.Tick, nextSell bool) error {
	return s.alertTickWithRoundTrip(tick, nextSell, tick.RoundTripResultMicros != nil)
}

func (s *shadowRun) alertTickWithRoundTrip(
	tick shadow.Tick, nextSell, roundTripComplete bool,
) error {
	if s.alerts == nil {
		return nil
	}
	if tick.PeriodClose {
		return nil
	}
	if err := s.alertMarketData(tick); err != nil {
		return err
	}
	key := s.policySHA256 + "/" + tick.At.Format(time.RFC3339Nano) + "/" + tick.Event
	if tick.Decision != nil && tick.Decision.Regime == shadow.RegimeRisk &&
		tick.Event != shadow.EventFilled {
		message := "PAPER · ⏸ NEW BUYS PAUSED\nThis run's paper safety limit was reached\nSells can still reduce risk"
		if tick.Decision.Strategy == shadow.StrategyRiskExit {
			message = "PAPER · 🛡 SAFETY SELL ACTIVE\nThis run's paper safety limit was reached\nSelling to reduce risk"
		}
		riskKey := s.policySHA256 + "/" + dayKey(tick.At) + "/risk-halt"
		if err := s.appendAlert(tick.At, paperstatus.KindRiskHalted, riskKey, message); err != nil {
			return err
		}
	}
	switch {
	case tick.Event == shadow.EventSignal && tick.DecisionQuote != nil:
		input, _ := shadowAssets(s.policy, nextSell)
		action := "Buying with up to"
		if nextSell {
			action = "Selling up to"
		}
		message := fmt.Sprintf("PAPER · %s %s ORDER OPEN\n%s %s %s\nWaiting to see if it fills",
			paperOpenIcon(nextSell), shadowSide(nextSell),
			action, formatShadowAmount(tick.DecisionQuote.InputAmount, input.decimals), input.name)
		if tick.PriceMicros != 0 {
			message += "\nReference price: $" + formatShadowAmount(tick.PriceMicros, 6)
		}
		return s.appendAlert(tick.At, paperstatus.KindOrderOpened, key, message)
	case tick.Event == shadow.EventFilled && tick.Fill != nil && tick.Fill.Filled:
		input, output := shadowAssets(s.policy, tick.Fill.Sell)
		opening := uint64(0)
		if s.runner != nil {
			opening = s.runner.Ledger().OpeningEquityMicros
		}
		fill := paperFillLine(*tick.Fill, input, output)
		if prices := paperFillPriceLines(s.policy, *tick.Fill, input, output); prices != "" {
			fill += "\n" + prices
		}
		account := paperAccountLine(s.policy, opening, tick.EquityMicros)
		if s.runner != nil && !s.reconcilingAlerts {
			account = paperBalanceLines(s.policy, s.runner.Ledger()) + "\n" + account
		}
		message := fmt.Sprintf(
			"PAPER · %s %s\n%s\n%s",
			paperFilledIcon(tick.Fill.Sell), shadowFilledSide(tick.Fill.Sell),
			fill,
			account,
		)
		if s.policy.RoundTrip() {
			message += "\n" + paperRoundTripLineForFill(
				tick.RoundTripResultMicros, paperValueUnit(s.policy), roundTripComplete,
			)
		}
		return s.appendAlert(tick.At, paperstatus.KindOrderFilled, key, message)
	case tick.Event == shadow.EventRefused:
		message := fmt.Sprintf("PAPER · ⚪ %s NOT FILLED\nPrice moved past the limit",
			shadowSide(nextSell))
		return s.appendAlert(tick.At, paperstatus.KindOrderRefused, key, message)
	case tick.Event == shadow.EventMissed:
		if s.paperFeeBudgetUsed() {
			message := "PAPER · ⏸ ORDERS PAUSED\nSimulated SOL for fees is used up for this run"
			budgetKey := s.policySHA256 + "/" + dayKey(tick.At) + "/fee-budget-used"
			return s.appendAlert(tick.At, paperstatus.KindOrderMissed, budgetKey, message)
		}
		message := fmt.Sprintf("PAPER · ⏭ %s SKIPPED\nTrade could not be completed",
			shadowSide(nextSell))
		return s.appendAlert(tick.At, paperstatus.KindOrderMissed, key, message)
	case tick.Event == shadow.EventUnobservable && tick.DecisionMissed:
		message := fmt.Sprintf("PAPER · ⏭ %s SKIPPED\nTrade could not be completed",
			shadowSide(nextSell))
		return s.appendAlert(tick.At, paperstatus.KindOrderMissed, key+"/order", message)
	default:
		return nil
	}
}

// alertMarketData reports a sustained blind period once and its recovery once.
// Short provider hiccups remain visible in /paper without interrupting the operator.
func (s *shadowRun) alertMarketData(tick shadow.Tick) error {
	if tick.Event == shadow.EventUnobservable {
		if s.consecutiveUnavailable < dataUnavailableAlertThreshold {
			s.consecutiveUnavailable++
		}
		if s.dataUnavailable || s.consecutiveUnavailable < dataUnavailableAlertThreshold {
			return nil
		}
		s.dataUnavailable = true
		message := "PAPER · ⚠️ PRICE DATA DELAYED\nNo new orders until prices return"
		key := s.policySHA256 + "/" + tick.At.Format(time.RFC3339Nano) + "/data-unavailable"
		return s.appendAlert(tick.At, paperstatus.KindDataUnavailable, key, message)
	}

	s.consecutiveUnavailable = 0
	if !s.dataUnavailable {
		return nil
	}
	s.dataUnavailable = false
	message := "PAPER · ✅ PRICE DATA BACK\nWatching again"
	key := s.policySHA256 + "/" + tick.At.Format(time.RFC3339Nano) + "/data-restored"
	return s.appendAlert(tick.At, paperstatus.KindDataRestored, key, message)
}

func (s *shadowRun) updatePaperCurrent(tick shadow.Tick, nextSell bool) error {
	if s.alerts == nil {
		return nil
	}
	message := paperCurrentMessage(s.policy, tick, nextSell)
	var summary *paperstatus.CurrentSummary
	var counts shadow.Counts
	if s.runner != nil {
		counts = s.runner.Counts()
		message = addPaperPerformance(message, fmt.Sprintf(
			"%d price checks · %s", counts.Ticks, shadowReportTrades(counts),
		))
	}
	if s.runner != nil && s.lastPrice != 0 {
		ledger := s.runner.Ledger()
		equity, equityErr := ledger.EquityMicros(s.lastPrice)
		hold, holdErr := ledger.HoldBenchmarkMicros(s.lastPrice)
		unrealized, unrealizedErr := ledger.UnrealizedMicros(s.lastPrice)
		if equityErr == nil && holdErr == nil && unrealizedErr == nil &&
			ledger.OpeningEquityMicros != 0 {
			drawdown := ledger.PeakEquityMicros - min(equity, ledger.PeakEquityMicros)
			message = addPaperPerformance(message, paperPerformanceLine(
				ledger.OpeningEquityMicros, equity, hold, paperValueUnit(s.policy),
			))
			summary = &paperstatus.CurrentSummary{
				Market: shadowMarketPair(s.policy), ValueUnit: paperValueUnit(s.policy),
				InstructionSHA256:   s.portfolioInstructionSHA256,
				Day:                 dayKey(tick.At),
				TickSeconds:         s.policy.TickSeconds,
				OpeningEquityMicros: ledger.OpeningEquityMicros,
				EquityMicros:        equity, HoldBenchmarkMicros: hold,
				AccountingTracked: true, RealizedMicros: ledger.RealizedMicros,
				UnrealizedMicros: unrealized, FeesMicros: ledger.FeesMicros,
				TurnoverMicros: ledger.TurnoverMicros,
				DrawdownMicros: drawdown, MaxDrawdownMicros: ledger.MaxDrawdownMicros,
				Checks:       counts.Ticks,
				Signals:      counts.Signals,
				Trades:       counts.Fills,
				Unobservable: counts.Unobservable,
				Missed:       counts.Missed,
				PriceMicros:  s.lastPrice,
				State:        paperSummaryState(tick),
				Strategy:     paperStrategyName(s.policy),
				NextAction:   strings.ToLower(shadowSide(nextSell)),
				RiskHalted:   s.runner.RiskHalted(),
			}
			addPaperSettings(summary, s.policy)
			addPaperFeeBudget(summary, s.policy, ledger)
			summary.DecisionReason = paperDecisionReason(tick,
				summary.FeeBudgetTracked && summary.EstimatedFillsRemaining == 0)
		}
	}
	return s.alerts.UpdateCurrentSummary(tick.At, message, summary)
}

func paperDecisionReason(tick shadow.Tick, feeBudgetUsed bool) string {
	if feeBudgetUsed {
		return "fee_budget_used"
	}
	switch tick.Event {
	case shadow.EventUnobservable:
		return "data_unavailable"
	case shadow.EventFiltered:
		return "route_cost_limit"
	case shadow.EventSignal:
		return "order_pending"
	case shadow.EventFilled:
		return "order_filled"
	case shadow.EventRefused:
		return "fill_limit"
	case shadow.EventMissed:
		return "trade_unavailable"
	}
	if tick.Decision != nil {
		return tick.Decision.Reason
	}
	return "watching"
}

func addPaperSettings(summary *paperstatus.CurrentSummary, policy shadow.Policy) {
	initial, _ := shadowAssets(policy, policy.IsSell())
	summary.InitialLotUnits = policy.InputAmount
	summary.InitialLotDecimals = uint8(initial.decimals)
	summary.InitialLotAsset = initial.name
	summary.MinimumOrderValueMicros = policy.MinimumOrderValueMicros
	summary.MaximumOrderValueMicros = policy.MaximumOrderValueMicros
	summary.FeeReserveLamports = policy.StartingFeeReserveLamports
	summary.FeeLamports = policy.FeeLamports
	summary.SlippageBPS = policy.SlippageBPS
	summary.SettleSeconds = policy.SettleSeconds
	if adaptive := policy.Adaptive; adaptive != nil {
		summary.FastWindow, summary.SlowWindow = adaptive.FastWindow, adaptive.SlowWindow
		summary.MinimumSignalBPS = adaptive.MinimumSignalBPS
		summary.MaxVolatilityBPS = adaptive.MaxVolatilityBPS
		summary.MaxQuoteImpactBPS = adaptive.MaxQuoteImpactBPS
		summary.MaxDrawdownBPS = adaptive.MaxDrawdownBPS
		summary.CooldownSeconds = adaptive.CooldownSeconds
	}
}

func addPaperFeeBudget(summary *paperstatus.CurrentSummary, policy shadow.Policy, ledger shadow.Ledger) {
	remaining, fills, ok := paperFeeBudget(policy, ledger)
	if !ok {
		return
	}
	summary.FeeBudgetTracked = true
	summary.RemainingFeeReserveLamports = remaining
	summary.EstimatedFillsRemaining = fills
}

func paperFeeBudget(policy shadow.Policy, ledger shadow.Ledger) (uint64, uint64, bool) {
	if policy.StartingFeeReserveLamports == 0 || policy.FeeLamports == 0 {
		return 0, 0, false
	}
	remaining := ledger.FeeReserveLamports
	if ledger.LockedRentLamports == 0 {
		remaining -= min(remaining, policy.OneTimeSetupRentLamports)
	}
	return remaining, remaining / policy.FeeLamports, true
}

func (s *shadowRun) paperFeeBudgetUsed() bool {
	if s.runner == nil {
		return false
	}
	_, fills, ok := paperFeeBudget(s.policy, s.runner.Ledger())
	return ok && fills == 0
}

func paperSummaryState(tick shadow.Tick) string {
	if tick.Event == shadow.EventUnobservable {
		return "waiting for data"
	}
	if tick.Decision != nil && tick.Decision.Regime == shadow.RegimeRisk {
		return "paused"
	}
	if tick.Event == shadow.EventSignal {
		return "order pending"
	}
	if tick.Decision == nil {
		return "watching"
	}
	switch tick.Decision.Regime {
	case shadow.RegimeWarming:
		return "warming"
	case shadow.RegimeUptrend:
		return "uptrend"
	case shadow.RegimeDowntrend:
		return "downtrend"
	case shadow.RegimeRange:
		return "range"
	case shadow.RegimeVolatile:
		return "volatile"
	default:
		return "watching"
	}
}

func addPaperPerformance(message, performance string) string {
	before, after, found := strings.Cut(message, "\n")
	if !found {
		return message + "\n" + performance
	}
	return before + "\n" + performance + "\n" + after
}

func paperPerformanceLine(opening, current, hold uint64, unit string) string {
	return "Paper result this run: " + formatPaperChange(opening, current, unit) +
		" · " + formatPaperComparison(hold, current, unit)
}

func paperCurrentMessage(policy shadow.Policy, tick shadow.Tick, nextSell bool) string {
	price := formatShadowAmount(tick.PriceMicros, 6)
	if tick.Decision != nil && tick.Decision.Regime == shadow.RegimeRisk {
		switch tick.Event {
		case shadow.EventSignal:
			if tick.Decision.Strategy == shadow.StrategyRiskExit && tick.DecisionQuote != nil {
				return "PAPER · 🟠 SELL ORDER OPEN\nSelling to reduce risk\nWaiting to see if it fills"
			}
			return "PAPER · ⏸ PAUSED\nWaiting to see if the open order fills"
		case shadow.EventFilled:
			lastAction := "order completed"
			if tick.Fill != nil {
				lastAction = strings.ToLower(shadowFilledSide(tick.Fill.Sell))
			}
			return "PAPER · ⏸ PAUSED\nLast action: " + lastAction + "\n" +
				paperValueLine(policy, tick.EquityMicros)
		case shadow.EventRefused, shadow.EventMissed:
			return "PAPER · ⏸ PAUSED\nLast order did not fill"
		default:
			return "PAPER · ⏸ NEW BUYS PAUSED\nSells can still reduce risk"
		}
	}
	switch tick.Event {
	case shadow.EventUnobservable:
		return "PAPER · ⚠️ WAITING FOR PRICES\nNo new orders until prices return"
	case shadow.EventSignal:
		if tick.DecisionQuote == nil {
			return fmt.Sprintf("PAPER · %s %s ORDER OPEN\nWaiting to see if it fills",
				paperOpenIcon(nextSell), shadowSide(nextSell))
		}
		input, _ := shadowAssets(policy, nextSell)
		action := "Buying with up to"
		if nextSell {
			action = "Selling up to"
		}
		return fmt.Sprintf("PAPER · %s %s ORDER OPEN\n%s %s %s\nReference price: $%s",
			paperOpenIcon(nextSell), shadowSide(nextSell),
			action, formatShadowAmount(tick.DecisionQuote.InputAmount, input.decimals), input.name, price)
	case shadow.EventFilled:
		if tick.Fill == nil {
			return "PAPER · ✅ ORDER FILLED\nLooking for the next trade"
		}
		return fmt.Sprintf("PAPER · %s %s\n%s",
			paperFilledIcon(tick.Fill.Sell), shadowFilledSide(tick.Fill.Sell),
			paperValueLine(policy, tick.EquityMicros))
	case shadow.EventRefused:
		return fmt.Sprintf("PAPER · ⚪ %s NOT FILLED\nPrice moved past the limit", shadowSide(nextSell))
	case shadow.EventMissed:
		return fmt.Sprintf("PAPER · ⏭ %s SKIPPED\nTrade could not be completed", shadowSide(nextSell))
	}
	if tick.Decision != nil {
		if policy.Adaptive == nil {
			return fmt.Sprintf("PAPER · 👀 LOOKING TO %s\n%s · price $%s",
				shadowSide(nextSell), paperStrategyLine(policy), price)
		}
		base, _ := shadowAssets(policy, true)
		if tick.Decision.Regime == shadow.RegimeWarming {
			return fmt.Sprintf("PAPER · 👀 LEARNING PRICES\n%s $%s · no trade yet", base.name, price)
		}
		if tick.Decision.Regime == shadow.RegimeVolatile {
			return fmt.Sprintf("PAPER · 👀 WAITING\n%s $%s · market moving too fast", base.name, price)
		}
		return fmt.Sprintf("PAPER · 👀 LOOKING TO %s\n%s $%s · %s",
			shadowSide(nextSell), base.name, price, adaptiveWaitingLabel(tick.Decision.Reason))
	}
	return fmt.Sprintf("PAPER · 👀 LOOKING TO %s\nPrice $%s", shadowSide(nextSell), price)
}

func adaptiveWaitingLabel(reason string) string {
	switch reason {
	case "signal_below_cost_hurdle":
		return "no good price yet"
	case "cooldown":
		return "taking a short break"
	case "sell_leg_waiting", "buy_leg_waiting":
		return "waiting for a better move"
	default:
		return "no good trade yet"
	}
}

// reconcileAlertTicks projects journaled decisions that may have survived a
// process crash before their alert snapshot was written. Append is idempotent,
// so the normal restart path only replays already-known IDs.
func (s *shadowRun) reconcileAlertTicks(ticks []shadow.Tick) error {
	previous := s.reconcilingAlerts
	s.reconcilingAlerts = true
	defer func() { s.reconcilingAlerts = previous }()
	s.consecutiveUnavailable = 0
	s.dataUnavailable = false
	nextSell := s.policy.IsSell()
	completedFills := uint64(0)
	for _, tick := range ticks {
		if tick.Event == shadow.EventFilled && tick.Fill != nil && tick.Fill.Filled && s.policy.RoundTrip() {
			completedFills++
		}
		if err := s.alertTickWithRoundTrip(
			tick, nextSell, completedFills != 0 && completedFills%2 == 0,
		); err != nil {
			return err
		}
		if tick.Event == shadow.EventFilled && tick.Fill != nil && tick.Fill.Filled && s.policy.RoundTrip() {
			nextSell = !tick.Fill.Sell
		}
	}
	return nil
}

func (s *shadowRun) alertReport(report shadow.Report) error {
	if s.alerts == nil {
		return nil
	}
	period := shadowPeriodTitle(report.From, report.To)
	message := fmt.Sprintf("PAPER · %s %s\n%s · %s\n%s",
		shadowReportIcon(report), period, shadowPerformanceLine(report),
		shadowReportTrades(report.Counts), shadowCoverageLine(report))
	key := s.policySHA256 + "/" + report.From.Format("2006-01-02") + "/" + report.To.Format(time.RFC3339Nano)
	kind := s.shadowReportEventKind(report.From, report.To)
	if err := s.appendAlert(report.To, kind, key, message); err != nil {
		return err
	}
	if kind == paperstatus.KindPeriodClosed {
		return nil
	}
	if report.OpeningEquityMicros == 0 ||
		report.HoldBenchmarkMicros == 0 || report.ClosingPriceMicros == 0 {
		return nil
	}
	summary := &paperstatus.CurrentSummary{
		Market: shadowMarketPair(s.policy), ValueUnit: paperValueUnit(s.policy),
		Day: report.From.Format("2006-01-02"), TickSeconds: s.policy.TickSeconds,
		InstructionSHA256:   s.portfolioInstructionSHA256,
		OpeningEquityMicros: report.OpeningEquityMicros,
		EquityMicros:        report.ClosingEquityMicros, HoldBenchmarkMicros: report.HoldBenchmarkMicros,
		AccountingTracked: true, RealizedMicros: report.RealizedMicros,
		UnrealizedMicros: report.UnrealizedMicros, FeesMicros: report.FeesMicros,
		TurnoverMicros:    report.TurnoverMicros,
		MaxDrawdownMicros: report.MaxDrawdownMicros,
		Checks:            report.Counts.Ticks, Signals: report.Counts.Signals,
		Trades: report.Counts.Fills, Unobservable: report.Counts.Unobservable,
		Missed: report.Counts.Missed, PriceMicros: report.ClosingPriceMicros,
		State: "completed", Strategy: paperStrategyName(s.policy), DecisionReason: "watching",
	}
	addPaperSettings(summary, s.policy)
	if s.runner != nil {
		addPaperFeeBudget(summary, s.policy, s.runner.Ledger())
	}
	if s.reconcilingAlerts {
		return s.alerts.ReconcileCurrentSummary(report.To, message, summary)
	}
	return s.alerts.UpdateCurrentSummary(report.To, message, summary)
}

func (s *shadowRun) alertUnavailableReport(from, to time.Time) error {
	if s.alerts == nil {
		return nil
	}
	message := "PAPER · ⚠️ " + shadowPeriodTitle(from, to) +
		"\nNo usable market price\nNo daily result"
	key := s.policySHA256 + "/" + from.Format("2006-01-02") + "/" + to.Format(time.RFC3339Nano)
	return s.appendAlert(to, s.shadowReportEventKind(from, to), key, message)
}

func (s *shadowRun) shadowReportEventKind(from, to time.Time) string {
	if s.policy.MarketEvidenceClass != shadow.MarketEvidenceDevelopmentProvisional &&
		to.Equal(from.Add(24*time.Hour)) {
		return paperstatus.KindPeriodClosed
	}
	return paperstatus.KindExperimentDone
}

func shadowPeriodTitle(from, to time.Time) string {
	if to.Equal(from.Add(24 * time.Hour)) {
		return "DAY FINISHED"
	}
	return "STOPPED"
}

func paperOpenIcon(sell bool) string {
	if sell {
		return "🟠"
	}
	return "🟡"
}

func paperFilledIcon(sell bool) string {
	if sell {
		return "🔵"
	}
	return "🟢"
}

func (s *shadowRun) appendAlert(at time.Time, kind, key, message string) error {
	if s.reconcilingAlerts {
		return s.alerts.Reconcile(at, kind, key, message)
	}
	return s.alerts.Append(at, kind, key, message)
}

type shadowAsset struct {
	name     string
	decimals uint
}

func shadowAssets(policy shadow.Policy, sell bool) (shadowAsset, shadowAsset) {
	baseDecimals, quoteDecimals := uint(policy.InputDecimals), uint(policy.OutputDecimals)
	if !policy.IsSell() {
		baseDecimals, quoteDecimals = quoteDecimals, baseDecimals
	}
	quote := "devUSDC"
	if policy.Cluster == shadow.Mainnet {
		quote = "USDC"
	}
	baseName := "SOL"
	if name, found := strings.CutSuffix(policy.Market, "/USDC"); found && name != "" {
		baseName = name
	}
	base, counter := shadowAsset{baseName, baseDecimals}, shadowAsset{quote, quoteDecimals}
	if sell {
		return base, counter
	}
	return counter, base
}

func shadowSide(sell bool) string {
	if sell {
		return "SELL"
	}
	return "BUY"
}

func shadowFilledSide(sell bool) string {
	if sell {
		return "SOLD"
	}
	return "BOUGHT"
}

func shadowMarketPair(policy shadow.Policy) string {
	if policy.Cluster == shadow.Mainnet {
		if policy.Market != "" {
			return policy.Market
		}
		return shadow.MarketSOLUSDC
	}
	return "SOL/devUSDC"
}

func paperStrategyName(policy shadow.Policy) string {
	if policy.Adaptive != nil {
		return "adaptive"
	}
	return "fixed"
}

func paperStrategyLine(policy shadow.Policy) string {
	if policy.Adaptive != nil {
		return "Follows strong moves and rebounds"
	}
	if policy.ReturnTrigger == nil {
		action := "Buys"
		if policy.IsSell() {
			action = "Sells"
		}
		return fmt.Sprintf("%s at $%s", action,
			formatShadowAmount(policy.Trigger.ThresholdMicros, 6))
	}
	sell, buy := policy.Trigger, *policy.ReturnTrigger
	if !policy.IsSell() {
		sell, buy = buy, sell
	}
	return fmt.Sprintf("Buys at $%s · sells at $%s",
		formatShadowAmount(buy.ThresholdMicros, 6),
		formatShadowAmount(sell.ThresholdMicros, 6))
}

func paperStartingSize(policy shadow.Policy) string {
	sell := policy.IsSell()
	input, _ := shadowAssets(policy, sell)
	return fmt.Sprintf("%s %s",
		formatShadowAmount(policy.StartingInputUnits, input.decimals), input.name)
}

func shadowThresholds(policy shadow.Policy) string {
	if policy.Adaptive != nil {
		return "Adaptive"
	}
	if policy.ReturnTrigger == nil {
		return fmt.Sprintf("Fixed · %s $%s", triggerComparator(policy.Trigger.Direction),
			formatShadowAmount(policy.Trigger.ThresholdMicros, 6))
	}
	sell, buy := policy.Trigger, *policy.ReturnTrigger
	if !policy.IsSell() {
		sell, buy = buy, sell
	}
	return fmt.Sprintf("Fixed · sell ≥ $%s · buy ≤ $%s",
		formatShadowAmount(sell.ThresholdMicros, 6), formatShadowAmount(buy.ThresholdMicros, 6))
}

func adaptiveDecisionLine(decision *shadow.AdaptiveDecision) string {
	if decision == nil {
		return ""
	}
	if decision.Regime == shadow.RegimeRisk && decision.Strategy == shadow.StrategyRiskExit {
		return "\nRisk limit · exit"
	}
	return "\n" + adaptiveAlertLabel(decision.Regime) + " · " +
		adaptiveAlertLabel(decision.Strategy)
}

func adaptiveAlertLabel(value string) string {
	switch value {
	case shadow.RegimeUptrend:
		return "Uptrend"
	case shadow.RegimeDowntrend:
		return "Downtrend"
	case shadow.RegimeRange:
		return "Range"
	case shadow.RegimeVolatile:
		return "Volatile"
	case shadow.RegimeRisk:
		return "Risk exit"
	case shadow.StrategyMomentum:
		return "momentum"
	case shadow.StrategyRangeReversion:
		return "range reversion"
	case shadow.StrategyRiskExit:
		return "risk exit"
	default:
		return strings.ReplaceAll(value, "_", " ")
	}
}

func triggerComparator(direction pricetrigger.Direction) string {
	if direction == pricetrigger.SellAtOrAbove {
		return "SELL ≥"
	}
	return "BUY ≤"
}

func shadowSize(policy shadow.Policy) string {
	input, _ := shadowAssets(policy, policy.IsSell())
	if policy.Adaptive != nil {
		budget := policy.StartingInputUnits
		if policy.IsSell() && policy.StartingFeeReserveLamports != 0 {
			budget += policy.StartingFeeReserveLamports
		}
		return fmt.Sprintf("budget %s %s · stop %s%% drawdown",
			formatShadowAmount(budget, input.decimals), input.name,
			formatShadowAmount(uint64(policy.Adaptive.MaxDrawdownBPS), 2))
	}
	return fmt.Sprintf("%s %s", formatShadowAmount(policy.InputAmount, input.decimals), input.name)
}

func shadowReportTrades(counts shadow.Counts) string {
	if counts.Fills == 1 {
		return "1 filled paper order"
	}
	return fmt.Sprintf("%d filled paper orders", counts.Fills)
}

func shadowEquityLine(policy shadow.Policy, equity uint64) string {
	if policy.Cluster == shadow.Mainnet {
		return "Equity $" + formatShadowAmount(equity, 6)
	}
	return "Test value " + formatShadowAmount(equity, 6) + " devUSDC"
}

func paperValueLine(policy shadow.Policy, equity uint64) string {
	if policy.Cluster == shadow.Mainnet {
		return "Total paper value now: $" + formatShadowAmount(equity, 6) +
			"\nPaper cash + current value of paper holdings"
	}
	return "Total paper value now: " + formatShadowAmount(equity, 6) + " devUSDC" +
		"\nPaper cash + current value of paper holdings"
}

func paperFillLine(fill shadow.Fill, input, output shadowAsset) string {
	if fill.Sell {
		return fmt.Sprintf("Sold %s %s\nReceived %s %s",
			formatShadowAmount(fill.SpentUnits, input.decimals), input.name,
			formatShadowAmount(fill.ReceivedUnits, output.decimals), output.name)
	}
	return fmt.Sprintf("Bought %s %s\nPaid %s %s",
		formatShadowAmount(fill.ReceivedUnits, output.decimals), output.name,
		formatShadowAmount(fill.SpentUnits, input.decimals), input.name)
}

func paperFillPriceLines(
	policy shadow.Policy,
	fill shadow.Fill,
	input, output shadowAsset,
) string {
	quoted, quotedErr := paperEffectivePrice(
		fill.Sell, input, output,
		fill.DecisionQuote.InputAmount, fill.DecisionQuote.EstimatedOutput,
	)
	filled, filledErr := paperEffectivePrice(
		fill.Sell, input, output, fill.SpentUnits, fill.ReceivedUnits,
	)
	if quotedErr != nil || filledErr != nil {
		return ""
	}
	return "Expected price: " + paperMarketPrice(policy, quoted) +
		"\nFilled price: " + paperMarketPrice(policy, filled)
}

func paperEffectivePrice(
	sell bool,
	input, output shadowAsset,
	inputAmount, outputAmount uint64,
) (uint64, error) {
	if sell {
		return shadow.PriceMicros(inputAmount, outputAmount, uint8(input.decimals), uint8(output.decimals))
	}
	return shadow.PriceMicros(outputAmount, inputAmount, uint8(output.decimals), uint8(input.decimals))
}

func paperMarketPrice(policy shadow.Policy, price uint64) string {
	formatted := formatShadowAmount(price, 6)
	if policy.Cluster == shadow.Mainnet {
		return "$" + formatted
	}
	return formatted + " devUSDC"
}

func paperAccountLine(policy shadow.Policy, opening, equity uint64) string {
	line := paperValueLine(policy, equity)
	if opening != 0 {
		line += "\nPaper result this run: " +
			formatPaperChange(opening, equity, paperValueUnit(policy))
	}
	return line
}

func paperBalanceLines(policy shadow.Policy, ledger shadow.Ledger) string {
	base, quote := shadowAssets(policy, true)
	lines := fmt.Sprintf("Paper cash left: %s %s\nTrading position: %s %s",
		formatShadowAmount(ledger.QuoteUnits, quote.decimals), quote.name,
		formatShadowAmount(ledger.BaseUnits, base.decimals), base.name)
	if reserve := ledger.FeeReserveLamports + ledger.LockedRentLamports; reserve != 0 {
		lines += "\nSOL set aside for paper fees/setup: " + formatShadowAmount(reserve, 9) + " SOL"
	}
	return lines
}

func paperRoundTripLine(result *int64, unit string) string {
	if result == nil {
		return "Trade result: still open\nProfit or loss appears after the matching order"
	}
	return "This completed buy + sell: " + formatPaperSignedChange(*result, unit)
}

func paperRoundTripLineForFill(result *int64, unit string, complete bool) string {
	if result == nil && complete {
		return "This completed buy + sell: result unavailable for this recovered older record"
	}
	return paperRoundTripLine(result, unit)
}

func formatPaperSignedChange(value int64, unit string) string {
	if value == 0 {
		return "unchanged"
	}
	if value > 0 {
		return "up " + formatPaperAmount(uint64(value), unit)
	}
	return "down " + formatPaperAmount(uint64(-(value+1))+1, unit)
}

func paperValueUnit(policy shadow.Policy) string {
	if policy.Cluster == shadow.Mainnet {
		return "USD"
	}
	return "devUSDC"
}

func shadowPerformanceLine(report shadow.Report) string {
	unit := "devUSDC"
	if report.Cluster == shadow.Mainnet {
		unit = "USD"
	}
	return "Paper gain/loss: " + formatPaperChange(
		report.OpeningEquityMicros, report.ClosingEquityMicros, unit,
	) + " · " + formatPaperDifference(report.VersusHoldMicros, unit)
}

func formatPaperChange(reference, current uint64, unit string) string {
	if current == reference {
		return "unchanged"
	}
	if current > reference {
		return "up " + formatPaperAmount(current-reference, unit)
	}
	return "down " + formatPaperAmount(reference-current, unit)
}

func formatPaperComparison(reference, current uint64, unit string) string {
	if current == reference {
		return "same as holding"
	}
	if current > reference {
		return formatPaperAmount(current-reference, unit) + " better than holding"
	}
	return formatPaperAmount(reference-current, unit) + " worse than holding"
}

func formatPaperDifference(value int64, unit string) string {
	if value == 0 {
		return "same as holding"
	}
	comparison, magnitude := " better than holding", uint64(value)
	if value < 0 {
		comparison = " worse than holding"
		magnitude = uint64(-(value + 1)) + 1
	}
	return formatPaperAmount(magnitude, unit) + comparison
}

func formatPaperAmount(value uint64, unit string) string {
	if unit == "USD" {
		return "$" + formatShadowAmount(value, 6)
	}
	return formatShadowAmount(value, 6) + " " + unit
}

func shadowCoverageLine(report shadow.Report) string {
	if !report.Trustworthy() {
		return fmt.Sprintf("Not enough price information · %d.%02d%% available", report.ObservableBPS/100, report.ObservableBPS%100)
	}
	return fmt.Sprintf("Price information available: %d.%02d%%", report.ObservableBPS/100, report.ObservableBPS%100)
}

func shadowReportIcon(report shadow.Report) string {
	if report.Trustworthy() {
		return "📊"
	}
	return "⚠️"
}

func formatShadowAmount(amount uint64, decimals uint) string {
	return strings.TrimRight(strings.TrimRight(formatUnits(amount, decimals), "0"), ".")
}
