package perpspaper

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestLongMarketPositionAccountsForMarginFeesFundingAndPnL(t *testing.T) {
	book := mustBook(t, 100_000_000)
	order := Order{
		ID:                   "sol-long-1",
		Symbol:               SOL,
		Side:                 Long,
		Kind:                 Market,
		Quantity:             1_000_000_000,
		LeverageBPS:          50_000,
		EntryFeeBPS:          10,
		ExitFeeBPS:           10,
		MaintenanceMarginBPS: 500,
	}
	mustAppend(t, book, Command{Type: OrderPlaced, Order: &order})
	mustAppend(t, book, Command{Type: OrderFilled, OrderID: order.ID, PriceMicros: 100_000_000})

	state := book.State()
	if state.Position == nil || state.Position.InitialMarginMicros != 20_000_000 {
		t.Fatalf("initial margin = %+v, want 20_000_000", state.Position)
	}
	if state.BalanceMicros != 99_900_000 {
		t.Fatalf("balance after entry = %d, want 99_900_000", state.BalanceMicros)
	}

	mustAppend(t, book, Command{Type: Marked, PriceMicros: 110_000_000})
	state = book.State()
	if state.UnrealizedPnLMicros != 10_000_000 || state.EquityMicros != 109_900_000 {
		t.Fatalf("marked state = %+v", state)
	}

	mustAppend(t, book, Command{Type: FundingApplied, PriceMicros: 110_000_000, FundingPaymentMicros: -500_000})
	mustAppend(t, book, Command{Type: FundingApplied, PriceMicros: 110_000_000, FundingPaymentMicros: 200_000})
	mustAppend(t, book, Command{Type: PositionClosed, PriceMicros: 110_000_000})
	state = book.State()
	if state.Position != nil || state.BalanceMicros != 109_490_000 {
		t.Fatalf("closed state = %+v", state)
	}
	if state.RealizedPnLMicros != 10_000_000 || state.FundingPnLMicros != -300_000 || state.FeesPaidMicros != 210_000 {
		t.Fatalf("lifecycle totals = %+v", state)
	}
	if state.LastCloseReason != "operator" {
		t.Fatalf("close reason = %q, want operator", state.LastCloseReason)
	}
}

func TestShortLimitFillConditionAndSymbolAllowlist(t *testing.T) {
	book := mustBook(t, 100_000_000)
	order := Order{
		ID:                   "btc-short-1",
		Symbol:               BTC,
		Side:                 Short,
		Kind:                 Limit,
		Quantity:             100_000,
		LimitPriceMicros:     50_000_000_000,
		LeverageBPS:          100_000,
		EntryFeeBPS:          10,
		ExitFeeBPS:           10,
		MaintenanceMarginBPS: 500,
	}
	mustAppend(t, book, Command{Type: OrderPlaced, Order: &order})
	before := len(book.Records())
	if _, err := book.Append(Command{Type: OrderFilled, OrderID: order.ID, PriceMicros: 49_999_000_000}); err == nil {
		t.Fatal("short limit fill below its limit was accepted")
	}
	if len(book.Records()) != before || book.State().PendingOrder == nil {
		t.Fatal("rejected fill mutated the journal or pending order")
	}
	mustAppend(t, book, Command{Type: OrderFilled, OrderID: order.ID, PriceMicros: 50_000_000_000})
	mustAppend(t, book, Command{Type: Marked, PriceMicros: 45_000_000_000})
	if got := book.State().UnrealizedPnLMicros; got != 5_000_000 {
		t.Fatalf("short unrealized P&L = %d, want 5_000_000", got)
	}

	for _, symbol := range []Symbol{SOL, BTC, ETH} {
		order := validOrder(symbol)
		order.ID = "allow-" + strings.ToLower(string(symbol))
		candidate := mustBook(t, 100_000_000)
		if _, err := candidate.Append(Command{Type: OrderPlaced, Order: &order}); err != nil {
			t.Fatalf("allowlisted symbol %s rejected: %v", symbol, err)
		}
	}
	order = validOrder("XRP")
	if _, err := mustBook(t, 100_000_000).Append(Command{Type: OrderPlaced, Order: &order}); err == nil {
		t.Fatal("unsupported symbol was accepted")
	}
}

func TestMaintenanceMarginLiquidatesAndChargesExitFee(t *testing.T) {
	book := mustBook(t, 500_000_000)
	order := Order{
		ID:                   "eth-long-1",
		Symbol:               ETH,
		Side:                 Long,
		Kind:                 Market,
		Quantity:             100_000_000,
		LeverageBPS:          50_000,
		EntryFeeBPS:          10,
		ExitFeeBPS:           10,
		MaintenanceMarginBPS: 500,
	}
	mustAppend(t, book, Command{Type: OrderPlaced, Order: &order})
	mustAppend(t, book, Command{Type: OrderFilled, OrderID: order.ID, PriceMicros: 2_000_000_000})
	mustAppend(t, book, Command{Type: Marked, PriceMicros: 1_550_000_000})

	state := book.State()
	if state.Position != nil || state.Liquidations != 1 || state.LastCloseReason != "liquidation" {
		t.Fatalf("position was not liquidated: %+v", state)
	}
	if state.BalanceMicros != 46_450_000 || state.RealizedPnLMicros != -450_000_000 || state.FeesPaidMicros != 3_550_000 {
		t.Fatalf("liquidation accounting = %+v", state)
	}
}

func TestReplayRejectsTamperingAndNonStrictJSON(t *testing.T) {
	book := mustBook(t, 100_000_000)
	order := validOrder(SOL)
	mustAppend(t, book, Command{Type: OrderPlaced, Order: &order})
	mustAppend(t, book, Command{Type: OrderFilled, OrderID: order.ID, PriceMicros: 10_000_000})
	mustAppend(t, book, Command{Type: Marked, PriceMicros: 11_000_000})

	replayed, err := Replay(book.Records())
	if err != nil {
		t.Fatalf("replay valid journal: %v", err)
	}
	if !reflect.DeepEqual(replayed.State(), book.State()) {
		t.Fatalf("replayed state = %+v, want %+v", replayed.State(), book.State())
	}

	tampered := book.Records()
	tampered[2].Command.PriceMicros++
	if _, err := Replay(tampered); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("tampered payload replay error = %v", err)
	}
	tampered = book.Records()
	tampered[2].PreviousSHA256 = strings.Repeat("0", 64)
	if _, err := Replay(tampered); err == nil || !strings.Contains(err.Error(), "previous hash mismatch") {
		t.Fatalf("tampered link replay error = %v", err)
	}

	encoded, err := json.Marshal(book.Records())
	if err != nil {
		t.Fatalf("marshal journal: %v", err)
	}
	if _, err := ReplayJSON(encoded); err != nil {
		t.Fatalf("replay valid JSON journal: %v", err)
	}
	unknown := bytes.Replace(encoded, []byte(`{"version"`), []byte(`{"unknown":true,"version"`), 1)
	if _, err := ReplayJSON(unknown); err == nil {
		t.Fatal("journal with unknown JSON field was accepted")
	}
	duplicate := bytes.Replace(encoded, []byte(`{"version":1`), []byte(`{"version":1,"version":1`), 1)
	if _, err := ReplayJSON(duplicate); err == nil {
		t.Fatal("journal with duplicate JSON field was accepted")
	}

	copy := book.Records()
	copy[1].Command.Order.ID = "mutated"
	if book.Records()[1].Command.Order.ID == "mutated" {
		t.Fatal("Records exposed mutable journal storage")
	}
}

func TestOrderAndCollateralBounds(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Order)
	}{
		{"leverage", func(order *Order) { order.LeverageBPS = MaxLeverageBPS + 1 }},
		{"market limit price", func(order *Order) { order.LimitPriceMicros = 1 }},
		{"maintenance versus initial", func(order *Order) {
			order.LeverageBPS = 20_000
			order.MaintenanceMarginBPS = 5_000
		}},
		{"order ID", func(order *Order) { order.ID = "not valid" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			order := validOrder(SOL)
			test.mutate(&order)
			if _, err := mustBook(t, 100_000_000).Append(Command{Type: OrderPlaced, Order: &order}); err == nil {
				t.Fatal("invalid order was accepted")
			}
		})
	}
	if _, err := New(0); err == nil {
		t.Fatal("zero starting collateral was accepted")
	}
	if _, err := New(MaxStartingCollateralMicros + 1); err == nil {
		t.Fatal("oversized starting collateral was accepted")
	}

	book := mustBook(t, 100_000_000)
	order := validOrder(SOL)
	order.LeverageBPS = 10_000
	mustAppend(t, book, Command{Type: OrderPlaced, Order: &order})
	if _, err := book.Append(Command{Type: OrderFilled, OrderID: order.ID, PriceMicros: 100_000_000}); err == nil {
		t.Fatal("fill without enough collateral for margin and fee was accepted")
	}
}

func validOrder(symbol Symbol) Order {
	return Order{
		ID:                   "order-1",
		Symbol:               symbol,
		Side:                 Long,
		Kind:                 Market,
		Quantity:             1_000_000_000,
		LeverageBPS:          50_000,
		EntryFeeBPS:          10,
		ExitFeeBPS:           10,
		MaintenanceMarginBPS: 500,
	}
}

func mustBook(t *testing.T, collateral uint64) *Book {
	t.Helper()
	book, err := New(collateral)
	if err != nil {
		t.Fatalf("open paper book: %v", err)
	}
	return book
}

func mustAppend(t *testing.T, book *Book, command Command) Record {
	t.Helper()
	record, err := book.Append(command)
	if err != nil {
		t.Fatalf("append %s: %v", command.Type, err)
	}
	return record
}
