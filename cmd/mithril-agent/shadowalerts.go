package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func (s *shadowRun) alertStrategy(at time.Time, kind string) error {
	if s.alerts == nil {
		return nil
	}
	title := "Strategy on"
	label := "active"
	if kind == paperstatus.KindStrategyChanged {
		title = "Strategy updated"
		label = "changed"
	} else {
		label = fmt.Sprintf("active/%d", s.activationSequence)
	}
	message := fmt.Sprintf(
		"PAPER · 🧠 %s\n%s · %s · initial %s",
		title, shadowThresholds(s.policy), shadowMarketPair(s.policy), shadowSize(s.policy),
	)
	return s.appendAlert(at, kind, s.policySHA256+"/"+dayKey(at)+"/"+label, message)
}

func (s *shadowRun) alertTick(tick shadow.Tick, nextSell bool) error {
	if s.alerts == nil {
		return nil
	}
	if tick.PeriodClose {
		return nil
	}
	key := s.policySHA256 + "/" + tick.At.Format(time.RFC3339Nano) + "/" + tick.Event
	switch {
	case tick.Event == shadow.EventFilled && tick.Fill != nil && tick.Fill.Filled:
		input, output := shadowAssets(s.policy, tick.Fill.Sell)
		message := fmt.Sprintf(
			"PAPER · 🟢 %s\n%s %s → %s %s\n%s",
			shadowFilledSide(tick.Fill.Sell),
			formatShadowAmount(tick.Fill.SpentUnits, input.decimals), input.name,
			formatShadowAmount(tick.Fill.ReceivedUnits, output.decimals), output.name,
			shadowEquityLine(s.policy, tick.EquityMicros),
		)
		return s.appendAlert(tick.At, paperstatus.KindOrderFilled, key, message)
	case tick.Event == shadow.EventWaiting && tick.Decision != nil &&
		(tick.Decision.Reason == "drawdown_halt" || tick.Decision.Reason == "risk_halt"):
		message := "PAPER · 🔴 Trading paused\nDaily drawdown limit reached"
		riskKey := s.policySHA256 + "/" + dayKey(tick.At) + "/risk-halt"
		return s.appendAlert(tick.At, paperstatus.KindRiskHalted, riskKey, message)
	default:
		return nil
	}
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
			"Checks %d · signals %d · trades %d", counts.Ticks, counts.Signals, counts.Fills,
		))
	}
	if s.runner != nil && s.lastPrice != 0 {
		ledger := s.runner.Ledger()
		equity, equityErr := ledger.EquityMicros(s.lastPrice)
		hold, holdErr := ledger.HoldBenchmarkMicros(s.lastPrice)
		if equityErr == nil && holdErr == nil && ledger.OpeningEquityMicros != 0 {
			message = addPaperPerformance(message, paperPerformanceLine(
				ledger.OpeningEquityMicros, equity, hold,
			))
			summary = &paperstatus.CurrentSummary{
				Market: shadowMarketPair(s.policy), Day: dayKey(tick.At),
				TickSeconds:         s.policy.TickSeconds,
				OpeningEquityMicros: ledger.OpeningEquityMicros,
				EquityMicros:        equity, HoldBenchmarkMicros: hold,
				Checks: counts.Ticks, Signals: counts.Signals, Trades: counts.Fills,
				RiskHalted: s.runner.RiskHalted(),
			}
		}
	}
	return s.alerts.UpdateCurrentSummary(tick.At, message, summary)
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
		" · vs hold " + formatEquityChange(hold, current)
}

func paperCurrentMessage(policy shadow.Policy, tick shadow.Tick, nextSell bool) string {
	price := formatShadowAmount(tick.PriceMicros, 6)
	if tick.Decision != nil && tick.Decision.Regime == shadow.RegimeRisk {
		switch tick.Event {
		case shadow.EventSignal:
			if tick.Decision.Strategy == shadow.StrategyRiskExit && tick.DecisionQuote != nil {
				return "PAPER · ⏳ Risk exit pending\nDaily drawdown limit reached"
			}
			return "PAPER · ⏸ Trading paused\nRisk exit waiting for pending order"
		case shadow.EventFilled:
			label := "Order"
			if tick.Fill != nil {
				label = shadowSide(tick.Fill.Sell)
			}
			return "PAPER · ⏸ Trading paused\n" + label + " filled · " +
				shadowEquityLine(policy, tick.EquityMicros)
		case shadow.EventRefused, shadow.EventMissed:
			return "PAPER · ⏸ Trading paused\nLast order not filled"
		default:
			return "PAPER · ⏸ Trading paused\nDaily drawdown limit reached"
		}
	}
	switch tick.Event {
	case shadow.EventUnobservable:
		return "PAPER · ⚠️ Waiting for data\nVerified market data unavailable"
	case shadow.EventSignal:
		if tick.DecisionQuote == nil {
			return "PAPER · ⏳ Order pending\nWaiting for settlement"
		}
		input, _ := shadowAssets(policy, nextSell)
		return fmt.Sprintf("PAPER · ⏳ Order pending\n%s %s %s · ref $%s",
			shadowSide(nextSell), formatShadowAmount(tick.DecisionQuote.InputAmount, input.decimals),
			input.name, price)
	case shadow.EventFilled:
		if tick.Fill == nil {
			return "PAPER · 👀 Watching\nLast order filled"
		}
		return fmt.Sprintf("PAPER · 👀 Watching\n%s filled · %s",
			shadowSide(tick.Fill.Sell), shadowEquityLine(policy, tick.EquityMicros))
	case shadow.EventRefused:
		return "PAPER · 👀 Watching\nLast order refused · waiting for a new signal"
	case shadow.EventMissed:
		return "PAPER · 👀 Watching\nLast order missed · waiting for a new signal"
	}
	if tick.Decision != nil {
		if policy.Adaptive == nil {
			return fmt.Sprintf("PAPER · 👀 Watching\n%s · now $%s", shadowThresholds(policy), price)
		}
		base, _ := shadowAssets(policy, true)
		if tick.Decision.Regime == shadow.RegimeWarming {
			return fmt.Sprintf("PAPER · 👀 Watching\nWarming up · %s $%s", base.name, price)
		}
		if tick.Decision.Regime == shadow.RegimeVolatile {
			return fmt.Sprintf("PAPER · 👀 Watching\nVolatile · waiting for calmer market · %s $%s", base.name, price)
		}
		return fmt.Sprintf("PAPER · 👀 Watching\n%s · %s · %s $%s",
			adaptiveAlertLabel(tick.Decision.Regime), adaptiveWaitingLabel(tick.Decision.Reason),
			base.name, price)
	}
	return fmt.Sprintf("PAPER · 👀 Watching\n%s · now $%s", shadowThresholds(policy), price)
}

func adaptiveWaitingLabel(reason string) string {
	switch reason {
	case "signal_below_cost_hurdle":
		return "no edge after costs"
	case "cooldown":
		return "cooling down"
	case "sell_leg_waiting", "buy_leg_waiting":
		return "position held"
	default:
		return "no trade"
	}
}

// reconcileAlertTicks projects journaled decisions that may have survived a
// process crash before their alert snapshot was written. Append is idempotent,
// so the normal restart path only replays already-known IDs.
func (s *shadowRun) reconcileAlertTicks(ticks []shadow.Tick) error {
	previous := s.reconcilingAlerts
	s.reconcilingAlerts = true
	defer func() { s.reconcilingAlerts = previous }()
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
		return "Day closed"
	}
	return "Period stopped"
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

func shadowPerformanceLine(report shadow.Report) string {
	pnl := formatEquityChange(report.OpeningEquityMicros, report.ClosingEquityMicros)
	if report.Cluster == shadow.Mainnet {
		return "P&L " + pnl + " · vs hold " + formatSignedDollars(report.VersusHoldMicros)
	}
	return "Test P&L " + strings.Replace(pnl, "$", "", 1) +
		" · vs hold " + strings.Replace(formatSignedDollars(report.VersusHoldMicros), "$", "", 1) +
		" devUSDC"
}

func formatEquityChange(opening, closing uint64) string {
	if closing >= opening {
		return "+$" + formatShadowAmount(closing-opening, 6)
	}
	return "-$" + formatShadowAmount(opening-closing, 6)
}

func shadowCoverageLine(report shadow.Report) string {
	line := fmt.Sprintf("Coverage %d.%02d%%", report.ObservableBPS/100, report.ObservableBPS%100)
	if !report.Trustworthy() {
		line += " · incomplete"
	}
	return line + " · daily reset"
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
