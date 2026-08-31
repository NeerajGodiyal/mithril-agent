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
	title := "PLAN ON"
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
			formatShadowAmount(uint64(s.policy.Adaptive.MaxDrawdownBPS), 2) + "% below today's high"
	}
	message := fmt.Sprintf("PAPER · 🧠 %s\n%s\n%s",
		title, paperStrategyLine(s.policy), detail)
	return s.appendAlert(at, kind, s.policySHA256+"/"+dayKey(at)+"/"+label, message)
}

func (s *shadowRun) alertTick(tick shadow.Tick, nextSell bool) error {
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
		message := "PAPER · ⏸ PAUSED\nPaper value hit today's safety limit"
		if tick.Decision.Strategy == shadow.StrategyRiskExit {
			message = "PAPER · 🛡 SAFETY EXIT ACTIVE\nPaper value hit today's safety limit"
		}
		riskKey := s.policySHA256 + "/" + dayKey(tick.At) + "/risk-halt"
		if err := s.appendAlert(tick.At, paperstatus.KindRiskHalted, riskKey, message); err != nil {
			return err
		}
	}
	switch {
	case tick.Event == shadow.EventSignal && tick.DecisionQuote != nil:
		input, _ := shadowAssets(s.policy, nextSell)
		message := fmt.Sprintf("PAPER · %s %s ORDER OPEN\n%s %s · waiting for result",
			paperOpenIcon(nextSell), shadowSide(nextSell),
			formatShadowAmount(tick.DecisionQuote.InputAmount, input.decimals), input.name)
		return s.appendAlert(tick.At, paperstatus.KindOrderOpened, key, message)
	case tick.Event == shadow.EventFilled && tick.Fill != nil && tick.Fill.Filled:
		input, output := shadowAssets(s.policy, tick.Fill.Sell)
		message := fmt.Sprintf(
			"PAPER · %s %s\n%s %s → %s %s · %s",
			paperFilledIcon(tick.Fill.Sell), shadowFilledSide(tick.Fill.Sell),
			formatShadowAmount(tick.Fill.SpentUnits, input.decimals), input.name,
			formatShadowAmount(tick.Fill.ReceivedUnits, output.decimals), output.name,
			paperValueLine(s.policy, tick.EquityMicros),
		)
		return s.appendAlert(tick.At, paperstatus.KindOrderFilled, key, message)
	case tick.Event == shadow.EventRefused:
		message := fmt.Sprintf("PAPER · ⚪ %s NOT FILLED\nPrice moved past the limit",
			shadowSide(nextSell))
		return s.appendAlert(tick.At, paperstatus.KindOrderRefused, key, message)
	case tick.Event == shadow.EventMissed:
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
		if equityErr == nil && holdErr == nil && ledger.OpeningEquityMicros != 0 {
			drawdown := ledger.PeakEquityMicros - min(equity, ledger.PeakEquityMicros)
			message = addPaperPerformance(message, paperPerformanceLine(
				ledger.OpeningEquityMicros, equity, hold,
			))
			summary = &paperstatus.CurrentSummary{
				Market: shadowMarketPair(s.policy), ValueUnit: paperValueUnit(s.policy),
				Day:                 dayKey(tick.At),
				TickSeconds:         s.policy.TickSeconds,
				OpeningEquityMicros: ledger.OpeningEquityMicros,
				EquityMicros:        equity, HoldBenchmarkMicros: hold,
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
		}
	}
	return s.alerts.UpdateCurrentSummary(tick.At, message, summary)
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

func paperPerformanceLine(opening, current, hold uint64) string {
	return "Today " + formatEquityChange(opening, current) +
		" · vs holding " + formatEquityChange(hold, current)
}

func paperCurrentMessage(policy shadow.Policy, tick shadow.Tick, nextSell bool) string {
	price := formatShadowAmount(tick.PriceMicros, 6)
	if tick.Decision != nil && tick.Decision.Regime == shadow.RegimeRisk {
		switch tick.Event {
		case shadow.EventSignal:
			if tick.Decision.Strategy == shadow.StrategyRiskExit && tick.DecisionQuote != nil {
				return "PAPER · 🟠 SELL ORDER OPEN\nReducing risk · waiting for result"
			}
			return "PAPER · ⏸ PAUSED\nWaiting for the open order"
		case shadow.EventFilled:
			label := "Order"
			if tick.Fill != nil {
				label = shadowSide(tick.Fill.Sell)
			}
			return "PAPER · ⏸ PAUSED\n" + label + " filled · " +
				paperValueLine(policy, tick.EquityMicros)
		case shadow.EventRefused, shadow.EventMissed:
			return "PAPER · ⏸ PAUSED\nLast order did not fill"
		default:
			return "PAPER · ⏸ PAUSED\nPaper value hit today's safety limit"
		}
	}
	switch tick.Event {
	case shadow.EventUnobservable:
		return "PAPER · ⚠️ WAITING FOR PRICES\nNo new orders until prices return"
	case shadow.EventSignal:
		if tick.DecisionQuote == nil {
			return fmt.Sprintf("PAPER · %s %s ORDER OPEN\nWaiting for result",
				paperOpenIcon(nextSell), shadowSide(nextSell))
		}
		input, _ := shadowAssets(policy, nextSell)
		return fmt.Sprintf("PAPER · %s %s ORDER OPEN\n%s %s · price $%s",
			paperOpenIcon(nextSell), shadowSide(nextSell),
			formatShadowAmount(tick.DecisionQuote.InputAmount, input.decimals), input.name, price)
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
	for _, tick := range ticks {
		if err := s.alertTick(tick, nextSell); err != nil {
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
	return s.appendAlert(report.To, paperstatus.KindPeriodClosed, key, message)
}

func (s *shadowRun) alertUnavailableReport(from, to time.Time) error {
	if s.alerts == nil {
		return nil
	}
	message := "PAPER · ⚠️ " + shadowPeriodTitle(from, to) +
		"\nNo usable market price\nNo P&L report"
	key := s.policySHA256 + "/" + from.Format("2006-01-02") + "/" + to.Format(time.RFC3339Nano)
	return s.appendAlert(to, paperstatus.KindPeriodClosed, key, message)
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
	if policy.Market == shadow.MarketJUPUSDC {
		baseName = "JUP"
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
		return "1 trade"
	}
	return fmt.Sprintf("%d trades", counts.Fills)
}

func formatSignedDollars(value int64) string {
	sign := "+"
	magnitude := uint64(value)
	if value < 0 {
		sign = "-"
		magnitude = uint64(-(value + 1)) + 1
	}
	return sign + "$" + formatShadowAmount(magnitude, 6)
}

func shadowEquityLine(policy shadow.Policy, equity uint64) string {
	if policy.Cluster == shadow.Mainnet {
		return "Equity $" + formatShadowAmount(equity, 6)
	}
	return "Test value " + formatShadowAmount(equity, 6) + " devUSDC"
}

func paperValueLine(policy shadow.Policy, equity uint64) string {
	if policy.Cluster == shadow.Mainnet {
		return "Paper value $" + formatShadowAmount(equity, 6)
	}
	return "Paper value " + formatShadowAmount(equity, 6) + " devUSDC"
}

func paperValueUnit(policy shadow.Policy) string {
	if policy.Cluster == shadow.Mainnet {
		return "USD"
	}
	return "devUSDC"
}

func shadowPerformanceLine(report shadow.Report) string {
	pnl := formatEquityChange(report.OpeningEquityMicros, report.ClosingEquityMicros)
	if report.Cluster == shadow.Mainnet {
		return "P&L " + pnl + " · vs holding " + formatSignedDollars(report.VersusHoldMicros)
	}
	return "Test P&L " + strings.Replace(pnl, "$", "", 1) +
		" · vs holding " + strings.Replace(formatSignedDollars(report.VersusHoldMicros), "$", "", 1) +
		" devUSDC"
}

func formatEquityChange(opening, closing uint64) string {
	if closing >= opening {
		return "+$" + formatShadowAmount(closing-opening, 6)
	}
	return "-$" + formatShadowAmount(opening-closing, 6)
}

func shadowCoverageLine(report shadow.Report) string {
	line := fmt.Sprintf("Price data %d.%02d%%", report.ObservableBPS/100, report.ObservableBPS%100)
	if !report.Trustworthy() {
		line += " · some data missing"
	}
	return line
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
