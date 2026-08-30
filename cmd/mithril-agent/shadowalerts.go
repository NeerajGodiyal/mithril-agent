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
		title = "STRATEGY CHANGE ACTIVE"
		label = "changed"
	}
	message := fmt.Sprintf(
		"PAPER SIMULATION — %s\nMarket: %s\nPolicy: %s\nRules: %s\nSize: %s\nEvidence: %s\nConfig: Changes apply at the next UTC boundary; a restart keeps today's journal policy.\nSafety: %s",
		title,
		shadowMarket(s.policy), s.alertPolicyID(), shadowThresholds(s.policy), shadowSize(s.policy),
		shadowEvidence(s.policy), paperDisclaimer,
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
		input, output := shadowAssets(s.policy, nextSell)
		message := fmt.Sprintf(
			"PAPER SIMULATION — ORDER OPENED\nMarket: %s\nPolicy: %s\nSide: %s\nRule: %s\nInput: %s %s\nEstimated output: %s %s\nMinimum output: %s %s\nReference price: $%s\nEvidence: %s\nSafety: %s",
			shadowMarket(s.policy), s.alertPolicyID(), shadowSide(nextSell), shadowRule(s.policy, nextSell),
			formatShadowAmount(tick.DecisionQuote.InputAmount, input.decimals), input.name,
			formatShadowAmount(tick.DecisionQuote.EstimatedOutput, output.decimals), output.name,
			formatShadowAmount(tick.DecisionQuote.MinimumOutput, output.decimals), output.name,
			formatUnits(tick.PriceMicros, 6), shadowEvidence(s.policy), paperDisclaimer,
		)
		return s.appendAlert(tick.At, paperstatus.KindOrderOpened, key, message)
	case tick.Event == shadow.EventFilled && tick.Fill != nil && tick.Fill.Filled:
		input, output := shadowAssets(s.policy, tick.Fill.Sell)
		message := fmt.Sprintf(
			"PAPER SIMULATION — ORDER FILLED\nMarket: %s\nPolicy: %s\nSide: %s\nRule: %s\nSpent: %s %s\nReceived: %s %s\nModeled fee: %s SOL\nPrice impact: %d bps\nSettlement slippage: %d bps\nDaily-reset paper equity: $%s\nEvidence: %s\nSafety: %s",
			shadowMarket(s.policy), s.alertPolicyID(), shadowSide(tick.Fill.Sell), shadowRule(s.policy, tick.Fill.Sell),
			formatShadowAmount(tick.Fill.SpentUnits, input.decimals), input.name,
			formatShadowAmount(tick.Fill.ReceivedUnits, output.decimals), output.name,
			formatShadowAmount(tick.Fill.FeeLamports, 9), tick.Fill.ImpactBPS, tick.Fill.SlippageBPS,
			formatUnits(tick.EquityMicros, 6), shadowEvidence(s.policy), paperDisclaimer,
		)
		return s.appendAlert(tick.At, paperstatus.KindOrderFilled, key, message)
	case tick.Event == shadow.EventRefused && tick.Fill != nil:
		message := fmt.Sprintf(
			"PAPER SIMULATION — ORDER REFUSED\nMarket: %s\nPolicy: %s\nSide: %s\nRule: %s\nReason: %s\nModeled fee: %s SOL\nPrice impact: %d bps\nSettlement slippage: %d bps\nEvidence: %s\nSafety: %s",
			shadowMarket(s.policy), s.alertPolicyID(), shadowSide(tick.Fill.Sell), shadowRule(s.policy, tick.Fill.Sell),
			tick.Fill.Refusal, formatShadowAmount(tick.Fill.FeeLamports, 9),
			tick.Fill.ImpactBPS, tick.Fill.SlippageBPS, shadowEvidence(s.policy), paperDisclaimer,
		)
		return s.appendAlert(tick.At, paperstatus.KindOrderRefused, key, message)
	case tick.Event == shadow.EventMissed || tick.Event == shadow.EventUnobservable && tick.DecisionMissed:
		reason := "the decision could not be priced or settled with valid current evidence"
		if tick.Reason != "" {
			reason = strings.ReplaceAll(string(tick.Reason), "_", " ")
		}
		message := fmt.Sprintf(
			"PAPER SIMULATION — ORDER MISSED\nMarket: %s\nPolicy: %s\nReason: %s\nEvidence: %s\nSafety: %s",
			shadowMarket(s.policy), s.alertPolicyID(), reason, shadowEvidence(s.policy), paperDisclaimer,
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
	period := "observation period closed early at " + report.To.Format(time.RFC3339)
	if report.To.Equal(report.From.Add(24 * time.Hour)) {
		period = "UTC day closed"
	}
	message := fmt.Sprintf(
		"PAPER SIMULATION — PERIOD CLOSED\nMarket: %s\nPolicy: %s\nPeriod: %s\nFills: %d\nRefused: %d\nMissed: %d\nUnobservable: %d\nReset book vs hold: %s USD\nObservable: %s\nActed on: %s of signals\nNote: Daily-reset results cannot be compounded across days.\nSafety: No funds moved. %s",
		shadowMarket(s.policy), s.alertPolicyID(), period,
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

func shadowRule(policy shadow.Policy, sell bool) string {
	trigger := policy.Trigger
	if policy.ReturnTrigger != nil && sell != policy.IsSell() {
		trigger = *policy.ReturnTrigger
	}
	return fmt.Sprintf("%s %s $%s", strings.ToLower(shadowSide(sell)),
		triggerComparator(trigger.Direction), formatUnits(trigger.ThresholdMicros, 6))
}

func shadowMarket(policy shadow.Policy) string {
	quote := "devUSDC"
	if policy.Cluster == shadow.Mainnet {
		quote = "USDC"
	}
	return policy.Cluster + " · SOL/" + quote
}

func shadowEvidence(policy shadow.Policy) string {
	provider := "Orca"
	if policy.QuoteRoute.Provider == shadow.QuoteJupiter {
		provider = "Jupiter"
	}
	return "Pyth + Kraken price · " + provider + " quote"
}

func shadowThresholds(policy shadow.Policy) string {
	if policy.ReturnTrigger == nil {
		return fmt.Sprintf("trigger %s $%s", triggerComparator(policy.Trigger.Direction),
			formatUnits(policy.Trigger.ThresholdMicros, 6))
	}
	sell, buy := policy.Trigger, *policy.ReturnTrigger
	if !policy.IsSell() {
		sell, buy = buy, sell
	}
	return fmt.Sprintf("sell at or above $%s and buy below $%s",
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
	return fmt.Sprintf("%s %s", formatShadowAmount(policy.InputAmount, input.decimals), input.name)
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
