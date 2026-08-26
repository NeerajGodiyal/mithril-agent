package main

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fullStrategyFile sets EVERY setting a strategy file can carry. Anything left
// at its zero value here would make the coverage test below pass for the wrong
// reason, so this fixture is deliberately exhaustive.
func fullStrategyFile() strategyFile {
	return strategyFile{
		SizeSOL:              "0.02",
		PrimaryTrustDomain:   "provider-one",
		SecondaryTrustDomain: "provider-two",
		SellAtUSD:            "25.50",
		BuyAtUSD:             "19.75",
		ScheduleWindow:       "3m",
		TradesPerDay:         9,
		Sweep: strategyFileSweep{
			Enabled:         true,
			To:              "3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh",
			ProofNonce:      "nonce-value",
			ProofIssued:     "2026-08-06T14:30:15Z",
			ProofSignature:  "signature-value",
			KeepSOL:         "0.4",
			ActivationDelay: "0s",
		},
		Telegram: strategyFileTelegram{Enabled: true},
		Alerts: strategyFileAlerts{
			PriceAboveUSD:   "300",
			PriceBelowUSD:   "150",
			BalanceAboveSOL: "5",
			BalanceBelowSOL: "0.25",
		},
	}
}

// targets returns freshly-zeroed destinations plus a reader for each.
func newTargets() (strategyFileTargets, func() map[string]string) {
	var (
		sizeSOL, sellAtUSD, buyAtUSD, destination string
		primaryTrust, secondaryTrust              string
		proofNonce, proofIssued, proofSignature   string
		keepSOL                                   string
		alerts                                    alertsConfig
		scheduleWindow, activationDelay           time.Duration = time.Hour, time.Hour
		tradesPerDay                              uint64
	)
	targets := strategyFileTargets{
		sizeSOL: &sizeSOL, sellAtUSD: &sellAtUSD, buyAtUSD: &buyAtUSD,
		destination: &destination, proofNonce: &proofNonce,
		proofIssued: &proofIssued, proofSignature: &proofSignature,
		alerts:  &alerts,
		keepSOL: &keepSOL, scheduleWindow: &scheduleWindow,
		activationDelay: &activationDelay, tradesPerDay: &tradesPerDay,
		primaryTrust: &primaryTrust, secondaryTrust: &secondaryTrust,
	}
	read := func() map[string]string {
		return map[string]string{
			"SizeSOL": sizeSOL, "SellAtUSD": sellAtUSD, "BuyAtUSD": buyAtUSD,
			"PrimaryTrustDomain": primaryTrust, "SecondaryTrustDomain": secondaryTrust,
			"Sweep.To": destination, "Sweep.ProofNonce": proofNonce,
			"Sweep.ProofIssued": proofIssued, "Sweep.ProofSignature": proofSignature,
			"Sweep.KeepSOL": keepSOL, "ScheduleWindow": scheduleWindow.String(),
			"Sweep.ActivationDelay": activationDelay.String(),
			"TradesPerDay":          formatUint(tradesPerDay),
			// Alerts are stored in micro-USD and lamports; the file says
			// dollars and SOL. Reading them back in stored units is what
			// proves the conversion happened rather than the string surviving.
			"Alerts.PriceAboveUSD":   formatUint(alerts.PriceAboveMicroUSD),
			"Alerts.PriceBelowUSD":   formatUint(alerts.PriceBelowMicroUSD),
			"Alerts.BalanceAboveSOL": formatUint(alerts.BalanceAboveLamports),
			"Alerts.BalanceBelowSOL": formatUint(alerts.BalanceBelowLamports),
		}
	}
	return targets, read
}

func formatUint(value uint64) string {
	if value == 0 {
		return ""
	}
	return strconv.FormatUint(value, 10)
}

// The whole promise of the one-file setup is that writing a value down makes it
// happen. Every setting in the file must reach the option it configures — the
// prices most of all, because a strategy that loses them trades at ANY price,
// which on a pool that pays less than it charges is a guaranteed loss.
func TestOneFileConfiguresEverySetting(t *testing.T) {
	targets, read := newTargets()
	if err := applyStrategyFile(fullStrategyFile(), map[string]bool{}, targets); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{
		"SizeSOL":               "0.02",
		"PrimaryTrustDomain":    "provider-one",
		"SecondaryTrustDomain":  "provider-two",
		"SellAtUSD":             "25.50",
		"BuyAtUSD":              "19.75",
		"ScheduleWindow":        "3m0s",
		"TradesPerDay":          "9",
		"Sweep.To":              "3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh",
		"Sweep.ProofNonce":      "nonce-value",
		"Sweep.ProofIssued":     "2026-08-06T14:30:15Z",
		"Sweep.ProofSignature":  "signature-value",
		"Sweep.KeepSOL":         "0.4",
		"Sweep.ActivationDelay": "0s",
		// $300 and $150 in micro-USD; 5 SOL and 0.25 SOL in lamports.
		"Alerts.PriceAboveUSD":   "300000000",
		"Alerts.PriceBelowUSD":   "150000000",
		"Alerts.BalanceAboveSOL": "5000000000",
		"Alerts.BalanceBelowSOL": "250000000",
	} {
		if got := read()[field]; got != want {
			t.Errorf("%s did not reach setup: got %q, want %q", field, got, want)
		}
	}
}

// A field can be added to the file, documented in the template, and never wired
// into setup. The operator then writes a setting down, sees it accepted, and
// the agent ignores it — the worst outcome for a config-driven tool.
//
// This enumerates the file's fields by reflection and fails on any that no
// applier consumes, so the promise cannot silently rot as the schema grows.
func TestEveryFieldInTheFileIsActuallyConsumed(t *testing.T) {
	// Fields that legitimately configure nothing in setup, each with a reason.
	exempt := map[string]string{
		"Comment":         "the template's own explanation, not a setting",
		"Sweep.Enabled":   "a gate: setup refuses outright when it is false",
		"Telegram":        "records intent only; the token lives in the service environment",
		"Telegram.ChatID": "records intent only; the token lives in the service environment",
	}

	targets, read := newTargets()
	if err := applyStrategyFile(fullStrategyFile(), map[string]bool{}, targets); err != nil {
		t.Fatal(err)
	}
	applied := read()

	var walk func(value reflect.Value, prefix string)
	walk = func(value reflect.Value, prefix string) {
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			name := prefix + field.Name
			if _, ok := exempt[name]; ok {
				continue
			}
			if field.Type.Kind() == reflect.Struct {
				walk(value.Field(i), name+".")
				continue
			}
			got, present := applied[name]
			if !present {
				t.Errorf("%s is in the strategy file but no applier consumes it; "+
					"an operator setting it would be silently ignored", name)
				continue
			}
			if got == "" {
				t.Errorf("%s is wired but did not arrive; the file set it and setup did not receive it", name)
			}
		}
	}
	walk(reflect.ValueOf(fullStrategyFile()), "")
}

// An explicitly typed flag must beat the file, or "--sell-at-usd 30" would be
// silently overwritten by whatever the file happened to say.
func TestATypedFlagBeatsTheFile(t *testing.T) {
	targets, read := newTargets()
	*targets.sellAtUSD = "30.00"
	*targets.tradesPerDay = 2
	given := map[string]bool{"sell-at-usd": true, "trades-per-day": true}
	if err := applyStrategyFile(fullStrategyFile(), given, targets); err != nil {
		t.Fatal(err)
	}
	if got := read()["SellAtUSD"]; got != "30.00" {
		t.Errorf("the file overwrote a typed flag: got %q, want 30.00", got)
	}
	if got := read()["TradesPerDay"]; got != "2" {
		t.Errorf("the file overwrote a typed trades-per-day: got %q, want 2", got)
	}
	// Everything NOT typed still comes from the file.
	if got := read()["BuyAtUSD"]; got != "19.75" {
		t.Errorf("an untyped setting did not come from the file: %q", got)
	}
}

// A sweep-disabled file has nowhere for profit to go. Setup refuses rather than
// quietly building a strategy that accumulates in the agent wallet.
func TestAFileWithNoSweepIsRefused(t *testing.T) {
	file := fullStrategyFile()
	file.Sweep.Enabled = false
	targets, _ := newTargets()
	err := applyStrategyFile(file, map[string]bool{}, targets)
	if err == nil {
		t.Fatal("a strategy with no sweep destination was accepted")
	}
	if !strings.Contains(err.Error(), "sweep") {
		t.Errorf("the refusal does not name the problem: %v", err)
	}
}

// A duration the file cannot express must fail loudly. Falling back to a
// default would run the strategy on a schedule the operator never chose.
func TestAnUnparsableDurationIsReportedNotIgnored(t *testing.T) {
	for _, broken := range []func(*strategyFile){
		func(f *strategyFile) { f.ScheduleWindow = "half an hour" },
		func(f *strategyFile) { f.Sweep.ActivationDelay = "soon" },
	} {
		file := fullStrategyFile()
		broken(&file)
		targets, _ := newTargets()
		if err := applyStrategyFile(file, map[string]bool{}, targets); err == nil {
			t.Error("an unparsable duration was silently ignored")
		}
	}
}

// Alerts are notify-only and must never widen what the agent may do. A file
// that sets them changes thresholds and nothing else.
func TestAlertsFromTheFileCannotWidenSpending(t *testing.T) {
	file := fullStrategyFile()
	before, _ := newTargets()
	sizeBefore, tradesBefore := *before.sizeSOL, *before.tradesPerDay

	// Prices are required for a PRICE alert to be judgeable, so they are
	// present; the point of the test is that nothing SPENDING moves.
	onlyAlerts := strategyFile{
		Sweep: file.Sweep, Alerts: file.Alerts,
		SellAtUSD: file.SellAtUSD, BuyAtUSD: file.BuyAtUSD,
	}
	targets, read := newTargets()
	if err := applyStrategyFile(onlyAlerts, map[string]bool{}, targets); err != nil {
		t.Fatal(err)
	}
	if *targets.sizeSOL != sizeBefore || *targets.tradesPerDay != tradesBefore {
		t.Errorf("alerts changed a spending bound: size %q, trades %d",
			*targets.sizeSOL, *targets.tradesPerDay)
	}
	if read()["Alerts.PriceAboveUSD"] == "" {
		t.Error("the alert threshold did not arrive")
	}
}

// A threshold the operator mistyped must stop setup BEFORE any leg is written,
// not leave half a strategy on disk with no alerts.
func TestABadAlertThresholdStopsSetupEarly(t *testing.T) {
	for _, broken := range []strategyFileAlerts{
		{PriceAboveUSD: "not a price"},
		{BalanceBelowSOL: "-1"},
	} {
		file := fullStrategyFile()
		file.Alerts = broken
		targets, _ := newTargets()
		if err := applyStrategyFile(file, map[string]bool{}, targets); err == nil {
			t.Errorf("an unusable alert threshold was accepted: %+v", broken)
		}
	}
}

// Setup naming no alerts must not erase ones set live on an existing leg.
func TestSetupWithNoAlertsLeavesExistingOnesAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	leg := triggeredLeg(t, t.TempDir(), false, 0)
	live := alertsConfig{BalanceBelowLamports: 42_000_000}
	if err := writeLegAlerts(leg, live); err != nil {
		t.Fatal(err)
	}
	// An empty set is a no-op, not a clear.
	if err := writeLegAlerts(leg, alertsConfig{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig(leg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Alerts != live {
		t.Errorf("a setup that named no alerts erased live ones: %+v", cfg.Alerts)
	}
}

// A price alert with no prices to watch cannot be judged. Refusing while
// parsing keeps a half-built strategy off the disk.
func TestAPriceAlertWithoutPricesIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	file := fullStrategyFile()
	file.SellAtUSD, file.BuyAtUSD = "", ""
	file.Alerts = strategyFileAlerts{PriceAboveUSD: "300"}
	targets, _ := newTargets()
	err := applyStrategyFile(file, map[string]bool{}, targets)
	if err == nil {
		t.Fatal("a price alert with no price rule was accepted")
	}
	if !strings.Contains(err.Error(), "sell_at_usd") {
		t.Errorf("the refusal does not say how to fix it: %v", err)
	}

	// A balance alert needs no price rule and must still work.
	file.Alerts = strategyFileAlerts{BalanceBelowSOL: "0.25"}
	targets, read := newTargets()
	if err := applyStrategyFile(file, map[string]bool{}, targets); err != nil {
		t.Fatalf("a balance alert was refused without a price rule: %v", err)
	}
	if read()["Alerts.BalanceBelowSOL"] != "250000000" {
		t.Errorf("the balance alert did not arrive: %q", read()["Alerts.BalanceBelowSOL"])
	}
}
