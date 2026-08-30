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
	message := fmt.Sprintf(
		"PAPER SIMULATION — reset-daily operational canary active for %s; policy %s; %s %s. A running observer applies changes at the next UTC boundary; a restarted observer resumes today's journal policy. %s",
		s.policy.Cluster, s.policySHA256[:12], shadowThresholds(s.policy), shadowSize(s.policy), paperDisclaimer,
	)
	label := "active"
	if kind == paperstatus.KindStrategyChanged {
		label = "changed"
		message = strings.Replace(message, "canary active", "canary changed", 1)
	}
	return s.appendAlert(at, kind, s.policySHA256+"/"+dayKey(at)+"/"+label, message)
}

func (s *shadowRun) alertTick(tick shadow.Tick, nextSell bool) error {
	if s.alerts == nil {
		return nil
	}
	key := s.policySHA256 + "/" + tick.At.Format(time.RFC3339Nano) + "/" + tick.Event
	switch {
	case tick.Event == shadow.EventSignal && tick.DecisionQuote != nil:
		input, output := shadowAssets(s.policy, nextSell)
		message := fmt.Sprintf(
			"PAPER SIMULATION — order opened: would %s %s %s for an estimated %s %s (minimum %s), reference $%s. %s",
			shadowVerb(nextSell), formatShadowAmount(tick.DecisionQuote.InputAmount, input.decimals), input.name,
			formatShadowAmount(tick.DecisionQuote.EstimatedOutput, output.decimals), output.name,
			formatShadowAmount(tick.DecisionQuote.MinimumOutput, output.decimals),
			formatUnits(tick.PriceMicros, 6), paperDisclaimer,
		)
		return s.appendAlert(tick.At, paperstatus.KindOrderOpened, key, message)
	case tick.Event == shadow.EventFilled && tick.Fill != nil && tick.Fill.Filled:
		input, output := shadowAssets(s.policy, tick.Fill.Sell)
		message := fmt.Sprintf(
			"PAPER SIMULATION — order filled: would %s %s %s for %s %s; fee %s SOL; impact %d bps; settlement slippage %d bps; daily-reset paper equity $%s. %s",
			shadowVerb(tick.Fill.Sell), formatShadowAmount(tick.Fill.SpentUnits, input.decimals), input.name,
			formatShadowAmount(tick.Fill.ReceivedUnits, output.decimals), output.name,
			formatShadowAmount(tick.Fill.FeeLamports, 9), tick.Fill.ImpactBPS, tick.Fill.SlippageBPS,
			formatUnits(tick.EquityMicros, 6), paperDisclaimer,
		)
		return s.appendAlert(tick.At, paperstatus.KindOrderFilled, key, message)
	case tick.Event == shadow.EventRefused && tick.Fill != nil:
		message := fmt.Sprintf(
			"PAPER SIMULATION — modeled submitted order refused by the paper slippage guard: %s; modeled fee %s SOL; impact %d bps; settlement slippage %d bps. %s",
			tick.Fill.Refusal, formatShadowAmount(tick.Fill.FeeLamports, 9),
			tick.Fill.ImpactBPS, tick.Fill.SlippageBPS, paperDisclaimer,
		)
		return s.appendAlert(tick.At, paperstatus.KindOrderRefused, key, message)
	case tick.Event == shadow.EventMissed || tick.Event == shadow.EventUnobservable && tick.DecisionMissed:
		reason := "the decision could not be priced or settled with valid current evidence"
		if tick.Reason != "" {
			reason = strings.ReplaceAll(string(tick.Reason), "_", " ")
		}
		message := fmt.Sprintf("PAPER SIMULATION — order missed: %s. %s", reason, paperDisclaimer)
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
	period := "observation period closed early at " + report.To.Format(time.RFC3339)
	if report.To.Equal(report.From.Add(24 * time.Hour)) {
		period = "UTC day closed"
	}
	message := fmt.Sprintf(
		"PAPER SIMULATION — reset-daily canary %s: fills %d, refused %d, missed %d, unobservable %d; same-period reset-book versus hold %s USD; observable %s; acted on %s of signals. This result cannot be compounded across days. No funds moved. %s",
		period,
		report.Counts.Fills, report.Counts.Refused, report.Counts.Missed, report.Counts.Unobservable,
		formatSignedMicros(report.VersusHoldMicros), formatBasisPoints(report.ObservableBPS),
		formatBasisPoints(report.ActedBPS), paperDisclaimer,
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

func shadowVerb(sell bool) string {
	if sell {
		return "sell"
	}
	return "buy with"
}

func shadowThresholds(policy shadow.Policy) string {
	if policy.ReturnTrigger == nil {
		return fmt.Sprintf("trigger %s $%s;", triggerComparator(policy.Trigger.Direction),
			formatUnits(policy.Trigger.ThresholdMicros, 6))
	}
	sell, buy := policy.Trigger, *policy.ReturnTrigger
	if !policy.IsSell() {
		sell, buy = buy, sell
	}
	return fmt.Sprintf("sell at or above $%s and buy below $%s;",
		formatUnits(sell.ThresholdMicros, 6), formatUnits(buy.ThresholdMicros, 6))
}

func triggerComparator(direction pricetrigger.Direction) string {
	if direction == pricetrigger.SellAtOrAbove {
		return "at or above"
	}
	return "at or below"
}

func shadowSize(policy shadow.Policy) string {
	input, _ := shadowAssets(policy, policy.IsSell())
	return fmt.Sprintf("paper size %s %s", formatShadowAmount(policy.InputAmount, input.decimals), input.name)
}

func formatShadowAmount(amount uint64, decimals uint) string {
	return strings.TrimRight(strings.TrimRight(formatUnits(amount, decimals), "0"), ".")
}

func formatBasisPoints(bps int32) string {
	sign := ""
	if bps < 0 {
		sign, bps = "-", -bps
	}
	return fmt.Sprintf("%s%d.%02d%%", sign, bps/100, bps%100)
}
