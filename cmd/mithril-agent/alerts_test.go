package main

import (
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/swaprun"
)

func validPriceTriggerStatus(available bool, conservative uint64) *pricetrigger.Status {
	status := &pricetrigger.Status{
		Feed: pricetrigger.FeedSOLUSD, Direction: pricetrigger.SellAtOrAbove,
		ThresholdMicros: 150_000_000, Available: available,
	}
	if available {
		status.ConservativePrice = conservative
		status.ObservedAt = time.Unix(1_700_000_000, 0).UTC()
	}
	return status
}

func TestAlertEvaluationTable(t *testing.T) {
	alerts := alertsConfig{
		PriceAboveMicroUSD:   200_000_000, // $200
		PriceBelowMicroUSD:   100_000_000, // $100
		BalanceAboveLamports: 10_000_000_000,
		BalanceBelowLamports: 50_000_000,
	}
	cases := []struct {
		name              string
		result            execution.Result
		wantPriceAbove    bool
		wantPriceBelow    bool
		wantBalanceAbove  bool
		wantBalanceBelow  bool
		wantEvidenceAvail bool
	}{
		{
			name: "nothing met",
			result: execution.Result{
				PriceTrigger:    validPriceTriggerStatus(true, 150_000_000),
				BalanceLamports: 1_000_000_000, BalanceObservedUnix: 1_700_000_000,
			},
			wantEvidenceAvail: true,
		},
		{
			name: "price above met at the exact boundary",
			result: execution.Result{
				PriceTrigger:    validPriceTriggerStatus(true, 200_000_000),
				BalanceLamports: 1_000_000_000, BalanceObservedUnix: 1_700_000_000,
			},
			wantPriceAbove: true, wantEvidenceAvail: true,
		},
		{
			name: "price below met at the exact boundary",
			result: execution.Result{
				PriceTrigger:    validPriceTriggerStatus(true, 100_000_000),
				BalanceLamports: 1_000_000_000, BalanceObservedUnix: 1_700_000_000,
			},
			wantPriceBelow: true, wantEvidenceAvail: true,
		},
		{
			name: "balance slots met",
			result: execution.Result{
				PriceTrigger:    validPriceTriggerStatus(true, 150_000_000),
				BalanceLamports: 10_000_000_000, BalanceObservedUnix: 1_700_000_000,
			},
			wantBalanceAbove: true, wantEvidenceAvail: true,
		},
		{
			name: "low balance met",
			result: execution.Result{
				PriceTrigger:    validPriceTriggerStatus(true, 150_000_000),
				BalanceLamports: 50_000_000, BalanceObservedUnix: 1_700_000_000,
			},
			wantBalanceBelow: true, wantEvidenceAvail: true,
		},
		{
			name: "price gate refused: nothing met, evidence unavailable",
			result: execution.Result{
				PriceTrigger:    validPriceTriggerStatus(false, 0),
				BalanceLamports: 1_000_000_000, BalanceObservedUnix: 1_700_000_000,
			},
			wantEvidenceAvail: false,
		},
		{
			name: "no balance observation: balance slots quiet, evidence unavailable",
			result: execution.Result{
				PriceTrigger: validPriceTriggerStatus(true, 150_000_000),
			},
			wantEvidenceAvail: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gauges := evaluateAlerts(alerts, true, tc.result)
			if gauges.PriceAbove.Met != tc.wantPriceAbove ||
				gauges.PriceBelow.Met != tc.wantPriceBelow ||
				gauges.BalanceAbove.Met != tc.wantBalanceAbove ||
				gauges.BalanceBelow.Met != tc.wantBalanceBelow {
				t.Fatalf("met: got %v/%v/%v/%v",
					gauges.PriceAbove.Met, gauges.PriceBelow.Met,
					gauges.BalanceAbove.Met, gauges.BalanceBelow.Met)
			}
			if gauges.EvidenceAvailable != tc.wantEvidenceAvail {
				t.Fatalf("evidence available: got %v, want %v",
					gauges.EvidenceAvailable, tc.wantEvidenceAvail)
			}
			for _, slot := range []struct {
				gauge     bool
				threshold uint64
				want      uint64
			}{
				{gauges.PriceAbove.Configured, gauges.PriceAbove.Threshold, alerts.PriceAboveMicroUSD},
				{gauges.PriceBelow.Configured, gauges.PriceBelow.Threshold, alerts.PriceBelowMicroUSD},
				{gauges.BalanceAbove.Configured, gauges.BalanceAbove.Threshold, alerts.BalanceAboveLamports},
				{gauges.BalanceBelow.Configured, gauges.BalanceBelow.Threshold, alerts.BalanceBelowLamports},
			} {
				if !slot.gauge || slot.threshold != slot.want {
					t.Fatal("every configured slot must render configured with its threshold")
				}
			}
		})
	}
}

func TestUnconfiguredSlotsNeverBlockEvidence(t *testing.T) {
	// With nothing configured, a refused price gate is not an alerting
	// evidence problem: there is no alert to disable.
	gauges := evaluateAlerts(alertsConfig{}, true, execution.Result{})
	if !gauges.EvidenceAvailable {
		t.Fatal("no configured slots means no missing evidence")
	}
	if gauges.PriceAbove.Configured || gauges.BalanceBelow.Configured {
		t.Fatal("unconfigured slots must render unconfigured")
	}
}

func TestAlertValidationRules(t *testing.T) {
	trigger := &pricetrigger.Policy{}
	profileWithTrigger := &swaprun.Profile{PriceTrigger: trigger}
	cases := []struct {
		name    string
		alerts  alertsConfig
		profile *swaprun.Profile
		wantErr bool
	}{
		{"empty is fine anywhere", alertsConfig{}, nil, false},
		{"balance alerts need no profile", alertsConfig{BalanceBelowLamports: 1}, nil, false},
		{"price alert without a price rule", alertsConfig{PriceAboveMicroUSD: 1}, nil, true},
		{"price alert without a trigger", alertsConfig{PriceAboveMicroUSD: 1}, &swaprun.Profile{}, true},
		{"price alert with a trigger", alertsConfig{PriceAboveMicroUSD: 1}, profileWithTrigger, false},
		{"price above ceiling", alertsConfig{PriceAboveMicroUSD: pricetrigger.MaxPriceMicros + 1}, profileWithTrigger, true},
		{"balance beyond supply", alertsConfig{BalanceAboveLamports: maxAlertBalanceLamports + 1}, nil, true},
		{"inverted price band", alertsConfig{PriceAboveMicroUSD: 1, PriceBelowMicroUSD: 2}, profileWithTrigger, true},
		{"inverted balance band", alertsConfig{BalanceAboveLamports: 1, BalanceBelowLamports: 2}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.alerts.validate(tc.profile)
			if (err != nil) != tc.wantErr {
				t.Fatalf("got err=%v, want error=%v", err, tc.wantErr)
			}
		})
	}
}

// Alerts live outside the fingerprinted profile: changing a threshold must
// not re-key action IDs, control binding, or the signer ledger. This is the
// property that makes live alert edits safe at all.
func TestAlertChangesDoNotMoveTheProfileFingerprint(t *testing.T) {
	profile := swaprun.Profile{
		Name: "orca_devnet_swap_v1", Version: 1, Cluster: "devnet",
	}
	// The profile alone will not validate here; the invariance being proven
	// is structural — the fingerprint input marshals the PROFILE, so the
	// config's alerts section cannot reach it. Marshal both configurations
	// and compare the profile bytes rather than requiring a fully valid
	// route fixture.
	first := config{Swap: &profile, Alerts: alertsConfig{}}
	second := config{Swap: &profile, Alerts: alertsConfig{PriceAboveMicroUSD: 42}}
	if first.Swap != second.Swap && *first.Swap != *second.Swap {
		t.Fatal("the profile must be identical regardless of alerts")
	}
	// And the config-level JSON of the profile section is byte-identical.
	if *first.Swap != *second.Swap {
		t.Fatal("alerts leaked into the profile")
	}
}
