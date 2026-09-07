package shadow

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func accountedLedgerFixture(t *testing.T) Ledger {
	t.Helper()
	p := separateFeePolicy(t)
	p.StartingFeeReserveLamports = 0
	p.StartingOutputUnits = 100_000_000
	l, err := NewLedger(p, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestAccountedLedgerPreservesExactAccountingAndPaperParity(t *testing.T) {
	l := accountedLedgerFixture(t)
	original := l
	steps := []struct {
		outcome AccountedOutcome
		mark    uint64
	}{
		{AccountedOutcome{TokenMint: mainnetUSDCMint, Success: true, Sell: true, SpentUnits: 100_000_000, ReceivedUnits: 3_000_000, FeeLamports: 1_000,
			PreBaseUnits: 1_000_000_000, PreQuoteUnits: 100_000_000, PostBaseUnits: 899_999_000, PostQuoteUnits: 103_000_000}, 30_000_000},
		{AccountedOutcome{TokenMint: mainnetUSDCMint, Success: true, SpentUnits: 1_000_000, ReceivedUnits: 50_000_000, FeeLamports: 1_000,
			PreBaseUnits: 899_999_000, PreQuoteUnits: 103_000_000, PostBaseUnits: 949_998_000, PostQuoteUnits: 102_000_000}, 20_000_000},
		{AccountedOutcome{TokenMint: mainnetUSDCMint, FeeLamports: 1_000, PreBaseUnits: 949_998_000, PreQuoteUnits: 102_000_000,
			PostBaseUnits: 949_997_000, PostQuoteUnits: 102_000_000}, 10_000_000},
	}
	for index, step := range steps {
		before := l
		o := step.outcome
		got, err := l.ApplyAccounted(o, step.mark)
		if err != nil {
			t.Fatalf("step %d: %v", index, err)
		}
		want, err := l.Apply(Fill{Filled: o.Success, Sell: o.Sell, SpentUnits: o.SpentUnits, ReceivedUnits: o.ReceivedUnits, FeeLamports: o.FeeLamports}, step.mark)
		if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(l, before) {
			t.Fatalf("step %d parity/input: %v", index, err)
		}
		if got.OpeningEquityMicros != original.OpeningEquityMicros || got.openingBaseUnits != original.openingBaseUnits || got.openingQuoteUnits != original.openingQuoteUnits {
			t.Fatal("opening reset")
		}
		if index == 0 && (got.CostBasisMicros != 17_999_980 || got.RealizedMicros != 999_980 || got.FeesMicros != 30 || got.Fills != 1) {
			t.Fatalf("hand accounting: %+v", got)
		}
		if index == 2 && (got.Fills != before.Fills || got.TurnoverMicros != before.TurnoverMicros || got.PeakEquityMicros != before.PeakEquityMicros || got.MaxDrawdownMicros <= before.MaxDrawdownMicros) {
			t.Fatal("fee-only failure lost history")
		}
		l = got
	}
	// Replay from the original opening recreates private benchmark state too.
	replayed := original
	for _, step := range steps {
		var err error
		replayed, err = replayed.ApplyAccounted(step.outcome, step.mark)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(l, replayed) {
		t.Fatal("restart replay changed books")
	}
}

func TestAccountedLedgerRejectsUnboundOrUnsupportedEffects(t *testing.T) {
	for name, mutate := range map[string]func(*Ledger, *AccountedOutcome){
		"missing mint":         func(_ *Ledger, o *AccountedOutcome) { o.TokenMint = "" },
		"wrong mint":           func(_ *Ledger, o *AccountedOutcome) { o.TokenMint = mainnetJUPMint },
		"pre base":             func(_ *Ledger, o *AccountedOutcome) { o.PreBaseUnits++ },
		"pre quote":            func(_ *Ledger, o *AccountedOutcome) { o.PreQuoteUnits++ },
		"post base":            func(_ *Ledger, o *AccountedOutcome) { o.PostBaseUnits++ },
		"post quote":           func(_ *Ledger, o *AccountedOutcome) { o.PostQuoteUnits++ },
		"failure spent":        func(_ *Ledger, o *AccountedOutcome) { o.SpentUnits = 1 },
		"failure received":     func(_ *Ledger, o *AccountedOutcome) { o.ReceivedUnits = 1 },
		"empty success":        func(_ *Ledger, o *AccountedOutcome) { o.Success = true },
		"no fee":               func(_ *Ledger, o *AccountedOutcome) { o.FeeLamports = 0 },
		"reclaimed rent":       func(_ *Ledger, o *AccountedOutcome) { o.ReclaimedInputLamports = 1 },
		"locked rent":          func(_ *Ledger, o *AccountedOutcome) { o.OutputAccountRent = 1 },
		"modeled reserve":      func(l *Ledger, _ *AccountedOutcome) { l.Policy.StartingFeeReserveLamports = 1 },
		"hidden reserve basis": func(l *Ledger, _ *AccountedOutcome) { l.FeeReserveCostBasisMicros = 1 },
		"wrong decimals":       func(l *Ledger, _ *AccountedOutcome) { l.Policy.OutputDecimals = 9 },
		"excess fee":           func(_ *Ledger, o *AccountedOutcome) { o.FeeLamports = math.MaxUint64 },
		"overflow output": func(_ *Ledger, o *AccountedOutcome) {
			o.Success = true
			o.Sell = true
			o.SpentUnits = 1
			o.ReceivedUnits = math.MaxUint64
		},
	} {
		t.Run(name, func(t *testing.T) {
			l := accountedLedgerFixture(t)
			o := AccountedOutcome{TokenMint: mainnetUSDCMint, FeeLamports: 1, PreBaseUnits: l.BaseUnits, PreQuoteUnits: l.QuoteUnits, PostBaseUnits: l.BaseUnits - 1, PostQuoteUnits: l.QuoteUnits}
			mutate(&l, &o)
			before := l
			got, err := l.ApplyAccounted(o, 20_000_000)
			if err == nil || !reflect.DeepEqual(got, Ledger{}) || !reflect.DeepEqual(l, before) {
				t.Fatalf("accepted or mutated: %v", err)
			}
		})
	}
}

func TestAccountedOutcomeUsesExactJSONStrings(t *testing.T) {
	o := AccountedOutcome{TokenMint: mainnetUSDCMint, SpentUnits: math.MaxUint64, FeeLamports: 1}
	raw, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["spent_units"] != "18446744073709551615" || fields["fee_lamports"] != "1" {
		t.Fatalf("inexact fields: %s", raw)
	}
}
