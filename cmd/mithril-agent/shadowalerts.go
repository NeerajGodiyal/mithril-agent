package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const paperDisclaimer = "No transaction was signed or submitted."

func (s *shadowRun) alertStrategy(at time.Time, kind string) error {
	if s.alerts == nil {
		return nil
	}
	title := "STRATEGY ACTIVE"
	label := "active"
	if kind == paperstatus.KindStrategyChanged {
		title = "STRATEGY UPDATED"
		label = "changed"
	}
	message := fmt.Sprintf(
		"PAPER SIMULATION — 🧠 %s\n%s · %s\nSize %s · Policy %s\n%s",
		title, shadowMarket(s.policy), shadowThresholds(s.policy), shadowSize(s.policy),
		s.alertPolicyID(), paperDisclaimer,
	)
	return s.appendAlert(at, kind, s.policySHA256+"/"+dayKey(at)+"/"+label, message)
}

func (s *shadowRun) alertTick(tick shadow.Tick, nextSell bool) error {
	if s.alerts == nil {
		return nil
	}
	key := s.policySHA256 + "/" + tick.At.Format(time.RFC3339Nano) + "/" + tick.Event
	switch {
	case tick.Event == shadow.EventSignal && tick.DecisionQuote != nil:
		input, _ := shadowAssets(s.policy, nextSell)
		message := fmt.Sprintf(
			"PAPER SIMULATION — 🟡 %s ORDER\n%s %s · ref $%s%s\n%s",
			shadowSide(nextSell),
			formatShadowAmount(tick.DecisionQuote.InputAmount, input.decimals), input.name,
			formatShadowAmount(tick.PriceMicros, 6), adaptiveDecisionLine(tick.Decision), paperDisclaimer,
		)
		return s.appendAlert(tick.At, paperstatus.KindOrderOpened, key, message)
	case tick.Event == shadow.EventFilled && tick.Fill != nil && tick.Fill.Filled:
		input, output := shadowAssets(s.policy, tick.Fill.Sell)
		price := tick.Fill.SettlePriceMicros
		if price == 0 {
			price = tick.PriceMicros
		}
		message := fmt.Sprintf(
			"PAPER SIMULATION — 🟢 %s\n%s %s → %s %s · ref $%s\nEquity $%s\n%s",
			shadowFillVerb(tick.Fill.Sell),
			formatShadowAmount(tick.Fill.SpentUnits, input.decimals), input.name,
			formatShadowAmount(tick.Fill.ReceivedUnits, output.decimals), output.name,
			formatShadowAmount(price, 6), formatShadowAmount(tick.EquityMicros, 6), paperDisclaimer,
		)
		return s.appendAlert(tick.At, paperstatus.KindOrderFilled, key, message)
	case tick.Event == shadow.EventRefused && tick.Fill != nil:
		message := fmt.Sprintf(
			"PAPER SIMULATION — ⚪ %s REFUSED\n%s\n%s",
			shadowSide(tick.Fill.Sell), tick.Fill.Refusal, paperDisclaimer,
		)
		return s.appendAlert(tick.At, paperstatus.KindOrderRefused, key, message)
	case tick.Event == shadow.EventWaiting && tick.Decision != nil &&
		(tick.Decision.Reason == "drawdown_halt" || tick.Decision.Reason == "risk_halt"):
		message := fmt.Sprintf(
			"PAPER SIMULATION — 🔴 RISK PAUSED\nDrawdown limit reached. No new paper orders today.\n%s",
			paperDisclaimer,
		)
		riskKey := s.policySHA256 + "/" + dayKey(tick.At) + "/risk-halt"
		return s.appendAlert(tick.At, paperstatus.KindRiskHalted, riskKey, message)
	case tick.Event == shadow.EventMissed && tick.DecisionQuote == nil ||
		tick.Event == shadow.EventUnobservable && tick.DecisionMissed:
		reason := "the decision could not be priced or settled with valid current evidence"
		if tick.Reason != "" {
			reason = strings.ReplaceAll(string(tick.Reason), "_", " ")
		}
		message := fmt.Sprintf(
			"PAPER SIMULATION — ⚠️ ORDER MISSED\n%s\n%s", reason, paperDisclaimer,
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
	period := "PERIOD CLOSED EARLY"
	if report.To.Equal(report.From.Add(24 * time.Hour)) {
		period = "UTC DAY CLOSED"
	}
	message := fmt.Sprintf(
		"PAPER SIMULATION — 📊 %s\n%d filled · %d filtered · %d refused · %d missed · %d unavailable\nVs hold %s USD · Policy %s\n%s",
		period, report.Counts.Fills, report.Counts.Filtered, report.Counts.Refused,
		report.Counts.Missed, report.Counts.Unobservable,
		formatSignedMicros(report.VersusHoldMicros), s.alertPolicyID(), paperDisclaimer,
	)
	key := s.policySHA256 + "/" + report.From.Format("2006-01-02") + "/" + report.To.Format(time.RFC3339Nano)
	return s.appendAlert(report.To, paperstatus.KindPeriodClosed, key, message)
}

func (s *shadowRun) appendAlert(at time.Time, kind, key, message string) error {
	if s.reconcilingAlerts {
		return s.alerts.Reconcile(at, kind, key, message)
	}
	return s.alerts.Append(at, kind, key, message)
}

func (s *shadowRun) alertPolicyID() string {
	fingerprint := s.policySHA256
	if len(fingerprint) < 12 {
		fingerprint, _ = s.policy.Fingerprint()
	}
	if len(fingerprint) < 12 {
		return "unavailable"
	}
	return fingerprint[:12]
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

func shadowFillVerb(sell bool) string {
	if sell {
		return "SOLD"
	}
	return "BOUGHT"
}

func shadowMarket(policy shadow.Policy) string {
	quote := "devUSDC"
	if policy.Cluster == shadow.Mainnet {
		quote = "USDC"
	}
	return policy.Cluster + " · SOL/" + quote
}

func shadowThresholds(policy shadow.Policy) string {
	if policy.Adaptive != nil {
		return "adaptive trend + range reversion"
	}
	if policy.ReturnTrigger == nil {
		return fmt.Sprintf("trigger %s $%s", triggerComparator(policy.Trigger.Direction),
			formatShadowAmount(policy.Trigger.ThresholdMicros, 6))
	}
	sell, buy := policy.Trigger, *policy.ReturnTrigger
	if !policy.IsSell() {
		sell, buy = buy, sell
	}
	return fmt.Sprintf("sell at or above $%s and buy at or below $%s",
		formatShadowAmount(sell.ThresholdMicros, 6), formatShadowAmount(buy.ThresholdMicros, 6))
}

func adaptiveDecisionLine(decision *shadow.AdaptiveDecision) string {
	if decision == nil {
		return ""
	}
	line := strings.ReplaceAll(decision.Reason, "_", " ")
	if decision.SignalBPS == 0 {
		return "\n" + line
	}
	return fmt.Sprintf("\n%s · signal %d bps", line, decision.SignalBPS)
}

func triggerComparator(direction pricetrigger.Direction) string {
	if direction == pricetrigger.SellAtOrAbove {
		return "at or above"
	}
	return "at or below"
}

func shadowSize(policy shadow.Policy) string {
	input, _ := shadowAssets(policy, policy.IsSell())
	return fmt.Sprintf("%s %s", formatShadowAmount(policy.InputAmount, input.decimals), input.name)
}

func formatShadowAmount(amount uint64, decimals uint) string {
	return strings.TrimRight(strings.TrimRight(formatUnits(amount, decimals), "0"), ".")
}
