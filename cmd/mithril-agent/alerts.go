package main

import (
	"errors"

	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/internal/runmetrics"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/swaprun"
)

// Alert rules notify; they never trade. They are four fixed integer slots
// rather than a rule engine: the closed shape keeps every possible
// configuration statable, validatable, and printable on one line each.
//
// The slots live in config.json OUTSIDE the fingerprinted profile, because a
// notification threshold must be editable without re-keying action IDs,
// control binding, and the signer ledger. The cost of that placement is that
// alerts are not fingerprint-protected — acceptable because they authorize
// nothing, and the strategy listing labels them "notify only" so the
// distinction is visible rather than buried.
//
// The agent itself stays stateless about alerting: it exports the condition
// as gauges each cycle and the deployed Prometheus/Alertmanager/notifier
// stack owns persistence, dedup and delivery. Building a second alert engine
// here is explicitly refused by the README's alerting section.
type alertsConfig struct {
	// PriceAboveMicroUSD fires when SOL/USD reaches the value, in micro-USD.
	PriceAboveMicroUSD uint64 `json:"price_above_microusd,omitempty"`
	// PriceBelowMicroUSD fires when SOL/USD falls to the value.
	PriceBelowMicroUSD uint64 `json:"price_below_microusd,omitempty"`
	// BalanceAboveLamports fires when the agent balance reaches the value —
	// typically "enough has accumulated to be worth sweeping".
	BalanceAboveLamports uint64 `json:"balance_above_lamports,omitempty"`
	// BalanceBelowLamports fires when the agent balance falls to the value —
	// typically "fee headroom is running out".
	BalanceBelowLamports uint64 `json:"balance_below_lamports,omitempty"`
}

// maxAlertBalanceLamports bounds balance thresholds at 1 billion SOL, well
// above the total supply, so a fat-fingered extra digit is caught at
// configuration rather than becoming a rule that can never fire.
const maxAlertBalanceLamports = 1_000_000_000 * 1_000_000_000

func (a alertsConfig) empty() bool {
	return a == alertsConfig{}
}

// validate checks the slots against each other and against the profile. The
// price slots need the profile's price-trigger sources: the alert reuses the
// same dual-source, deviation-gated evidence the trading path evaluates, and
// refusing a price alert without those sources is what keeps a third,
// less-guarded price path from growing here.
func (a alertsConfig) validate(swap *swaprun.Profile) error {
	if a.PriceAboveMicroUSD > pricetrigger.MaxPriceMicros ||
		a.PriceBelowMicroUSD > pricetrigger.MaxPriceMicros {
		return errors.New("alert price thresholds must be at most the price ceiling")
	}
	if a.BalanceAboveLamports > maxAlertBalanceLamports ||
		a.BalanceBelowLamports > maxAlertBalanceLamports {
		return errors.New("alert balance thresholds are implausibly large")
	}
	if a.PriceAboveMicroUSD != 0 && a.PriceBelowMicroUSD != 0 &&
		a.PriceAboveMicroUSD <= a.PriceBelowMicroUSD {
		return errors.New("the price-above alert must be above the price-below alert")
	}
	if a.BalanceAboveLamports != 0 && a.BalanceBelowLamports != 0 &&
		a.BalanceAboveLamports <= a.BalanceBelowLamports {
		return errors.New("the balance-above alert must be above the balance-below alert")
	}
	priceConfigured := a.PriceAboveMicroUSD != 0 || a.PriceBelowMicroUSD != 0
	if priceConfigured && (swap == nil || swap.PriceTrigger == nil) {
		return errors.New(
			"price alerts need the profile's price rule: its two bound sources are " +
				"the evidence the alert is judged against")
	}
	return nil
}

// evaluateAlerts turns one cycle's result into the gauge values Prometheus
// consumes. Everything here is a pure comparison; freshness, deviation and
// confidence were already judged by the price trigger's evaluator, and the
// balance carries its own observation timestamp on the metrics surface.
func evaluateAlerts(alerts alertsConfig, configValid bool, result execution.Result) runmetrics.AlertGauges {
	gauges := runmetrics.AlertGauges{EvidenceAvailable: true, ConfigValid: configValid}

	priceAvailable := result.PriceTrigger != nil && result.PriceTrigger.Available
	var conservative uint64
	if priceAvailable {
		conservative = result.PriceTrigger.ConservativePrice
	}
	balanceAvailable := result.BalanceObservedUnix > 0

	if alerts.PriceAboveMicroUSD != 0 {
		gauges.PriceAbove = runmetrics.AlertSlot{
			Configured: true,
			Threshold:  alerts.PriceAboveMicroUSD,
			Met:        priceAvailable && conservative >= alerts.PriceAboveMicroUSD,
		}
		if !priceAvailable {
			gauges.EvidenceAvailable = false
		}
	}
	if alerts.PriceBelowMicroUSD != 0 {
		gauges.PriceBelow = runmetrics.AlertSlot{
			Configured: true,
			Threshold:  alerts.PriceBelowMicroUSD,
			Met:        priceAvailable && conservative <= alerts.PriceBelowMicroUSD,
		}
		if !priceAvailable {
			gauges.EvidenceAvailable = false
		}
	}
	if alerts.BalanceAboveLamports != 0 {
		gauges.BalanceAbove = runmetrics.AlertSlot{
			Configured: true,
			Threshold:  alerts.BalanceAboveLamports,
			Met:        balanceAvailable && result.BalanceLamports >= alerts.BalanceAboveLamports,
		}
		if !balanceAvailable {
			gauges.EvidenceAvailable = false
		}
	}
	if alerts.BalanceBelowLamports != 0 {
		gauges.BalanceBelow = runmetrics.AlertSlot{
			Configured: true,
			Threshold:  alerts.BalanceBelowLamports,
			Met:        balanceAvailable && result.BalanceLamports <= alerts.BalanceBelowLamports,
		}
		if !balanceAvailable {
			gauges.EvidenceAvailable = false
		}
	}
	return gauges
}
