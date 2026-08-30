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
	case tick.Event == shadow.EventSignal && tick.DecisionQuote != nil:
		input, _ := shadowAssets(s.policy, nextSell)
		message := fmt.Sprintf(
			"PAPER · 🟡 %s signal\n%s %s · ref $%s%s",
			shadowSide(nextSell),
			formatShadowAmount(tick.DecisionQuote.InputAmount, input.decimals), input.name,
			formatShadowAmount(tick.PriceMicros, 6), adaptiveDecisionLine(tick.Decision),
		)
		return s.appendAlert(tick.At, paperstatus.KindOrderOpened, key, message)
	case tick.Event == shadow.EventFilled && tick.Fill != nil && tick.Fill.Filled:
		input, output := shadowAssets(s.policy, tick.Fill.Sell)
		price := tick.Fill.SettlePriceMicros
		if price == 0 {
			price = tick.PriceMicros
		}
		message := fmt.Sprintf(
			"PAPER · 🟢 %s filled\n%s %s → %s %s · ref $%s\n%s",
			shadowSide(tick.Fill.Sell),
			formatShadowAmount(tick.Fill.SpentUnits, input.decimals), input.name,
			formatShadowAmount(tick.Fill.ReceivedUnits, output.decimals), output.name,
			formatShadowAmount(price, 6), shadowEquityLine(s.policy, tick.EquityMicros),
		)
		return s.appendAlert(tick.At, paperstatus.KindOrderFilled, key, message)
	case tick.Event == shadow.EventRefused && tick.Fill != nil:
		message := fmt.Sprintf(
			"PAPER · ⚪ %s refused\n%s",
			shadowSide(tick.Fill.Sell), tick.Fill.Refusal,
		)
		return s.appendAlert(tick.At, paperstatus.KindOrderRefused, key, message)
	case tick.Event == shadow.EventWaiting && tick.Decision != nil &&
		(tick.Decision.Reason == "drawdown_halt" || tick.Decision.Reason == "risk_halt"):
		message := "PAPER · 🔴 Trading paused\nDaily drawdown limit reached"
		riskKey := s.policySHA256 + "/" + dayKey(tick.At) + "/risk-halt"
		return s.appendAlert(tick.At, paperstatus.KindRiskHalted, riskKey, message)
	case tick.Event == shadow.EventMissed && tick.DecisionQuote == nil ||
		tick.Event == shadow.EventUnobservable && tick.DecisionMissed:
		reason := "Signal was not settled"
		if tick.Reason != "" {
			reason = strings.ReplaceAll(string(tick.Reason), "_", " ")
		}
		message := fmt.Sprintf(
			"PAPER · ⚠️ Order missed\n%s", reason,
		)
		return s.appendAlert(tick.At, paperstatus.KindOrderMissed, key, message)
	default:
		return nil
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
	message := fmt.Sprintf(
		"PAPER · %s %s\n%s\n%s\n%s",
		shadowReportIcon(report), period, shadowReportCounts(report.Counts),
		shadowPerformanceLine(report), shadowCoverageLine(report),
	)
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
	base, counter := shadowAsset{"SOL", baseDecimals}, shadowAsset{quote, quoteDecimals}
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

func shadowMarketPair(policy shadow.Policy) string {
	if policy.Cluster == shadow.Mainnet {
		return "SOL/USDC"
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
	return fmt.Sprintf("%s %s", formatShadowAmount(policy.InputAmount, input.decimals), input.name)
}

func shadowReportCounts(counts shadow.Counts) string {
	parts := []string{fmt.Sprintf("%d fills", counts.Fills)}
	if counts.Filtered > 0 {
		parts = append(parts, fmt.Sprintf("%d filtered", counts.Filtered))
	}
	if counts.Refused > 0 {
		parts = append(parts, fmt.Sprintf("%d refused", counts.Refused))
	}
	if counts.Missed > 0 {
		parts = append(parts, fmt.Sprintf("%d missed", counts.Missed))
	}
	if counts.Unobservable > 0 {
		parts = append(parts, fmt.Sprintf("%d data gaps", counts.Unobservable))
	}
	return strings.Join(parts, " · ")
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
