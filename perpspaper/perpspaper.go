// Package perpspaper implements deterministic, signer-free perpetual-futures
// paper accounting. Money and prices use micro-USDC; quantities use 1e9 units
// per SOL and 1e8 per BTC or ETH. Its network adapter is public and read-only;
// the package contains no signer or live execution path.
package perpspaper

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/bits"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

const (
	Version uint32 = 1

	MaxStartingCollateralMicros uint64 = 1_000_000_000_000_000
	MaxPriceMicros              uint64 = 10_000_000_000_000
	MaxNotionalMicros           uint64 = 1_000_000_000_000_000
	MaxLeverageBPS              uint32 = 500_000
	MaxFeeBPS                   uint16 = 1_000
	MaxMaintenanceMarginBPS     uint16 = 5_000

	basisPoints uint64 = 10_000
	hashDomain         = "mithril-agent/perpspaper/v1/record\n"
)

type Symbol string

const (
	SOL Symbol = "SOL"
	BTC Symbol = "BTC"
	ETH Symbol = "ETH"
)

type Side string

const (
	Long  Side = "long"
	Short Side = "short"
)

type OrderKind string

const (
	Market OrderKind = "market"
	Limit  OrderKind = "limit"
)

type EventType string

const (
	Opened         EventType = "opened"
	OrderPlaced    EventType = "order_placed"
	OrderFilled    EventType = "order_filled"
	Marked         EventType = "marked"
	FundingApplied EventType = "funding_applied"
	PositionClosed EventType = "position_closed"
)

type Order struct {
	ID                   string    `json:"id"`
	Symbol               Symbol    `json:"symbol"`
	Side                 Side      `json:"side"`
	Kind                 OrderKind `json:"kind"`
	Quantity             uint64    `json:"quantity"`
	LimitPriceMicros     uint64    `json:"limit_price_micros"`
	LeverageBPS          uint32    `json:"leverage_bps"`
	EntryFeeBPS          uint16    `json:"entry_fee_bps"`
	ExitFeeBPS           uint16    `json:"exit_fee_bps"`
	MaintenanceMarginBPS uint16    `json:"maintenance_margin_bps"`
}

type Position struct {
	OrderID              string `json:"order_id"`
	Symbol               Symbol `json:"symbol"`
	Side                 Side   `json:"side"`
	Quantity             uint64 `json:"quantity"`
	EntryPriceMicros     uint64 `json:"entry_price_micros"`
	EntryNotionalMicros  uint64 `json:"entry_notional_micros"`
	LeverageBPS          uint32 `json:"leverage_bps"`
	ExitFeeBPS           uint16 `json:"exit_fee_bps"`
	MaintenanceMarginBPS uint16 `json:"maintenance_margin_bps"`
	InitialMarginMicros  uint64 `json:"initial_margin_micros"`
}

type Command struct {
	Type             EventType `json:"type"`
	CollateralMicros uint64    `json:"collateral_micros"`
	Order            *Order    `json:"order"`
	OrderID          string    `json:"order_id"`
	PriceMicros      uint64    `json:"price_micros"`
	// FundingPaymentMicros credits collateral when positive and debits it when negative.
	FundingPaymentMicros int64 `json:"funding_payment_micros"`
}

type Record struct {
	Version        uint32  `json:"version"`
	Sequence       uint64  `json:"sequence"`
	PreviousSHA256 string  `json:"previous_sha256"`
	Command        Command `json:"command"`
	SHA256         string  `json:"sha256"`
}

type State struct {
	Initialized              bool      `json:"initialized"`
	StartingCollateralMicros uint64    `json:"starting_collateral_micros"`
	BalanceMicros            int64     `json:"balance_micros"`
	PendingOrder             *Order    `json:"pending_order"`
	Position                 *Position `json:"position"`
	LastMarkPriceMicros      uint64    `json:"last_mark_price_micros"`
	UnrealizedPnLMicros      int64     `json:"unrealized_pnl_micros"`
	EquityMicros             int64     `json:"equity_micros"`
	MaintenanceMarginMicros  uint64    `json:"maintenance_margin_micros"`
	RealizedPnLMicros        int64     `json:"realized_pnl_micros"`
	FundingPnLMicros         int64     `json:"funding_pnl_micros"`
	FeesPaidMicros           uint64    `json:"fees_paid_micros"`
	Liquidations             uint64    `json:"liquidations"`
	LastCloseReason          string    `json:"last_close_reason"`
}

type Book struct {
	state   State
	records []Record
}

func New(startingCollateralMicros uint64) (*Book, error) {
	b := &Book{}
	if _, err := b.Append(Command{Type: Opened, CollateralMicros: startingCollateralMicros}); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *Book) Append(command Command) (Record, error) {
	if b == nil {
		return Record{}, errors.New("paper book is nil")
	}
	command = cloneCommand(command)
	next, err := apply(cloneState(b.state), command)
	if err != nil {
		return Record{}, err
	}
	sequence := uint64(len(b.records)) + 1
	previous := ""
	if len(b.records) != 0 {
		previous = b.records[len(b.records)-1].SHA256
	}
	record := Record{
		Version:        Version,
		Sequence:       sequence,
		PreviousSHA256: previous,
		Command:        command,
	}
	record.SHA256, err = recordHash(record)
	if err != nil {
		return Record{}, err
	}
	b.state = next
	b.records = append(b.records, cloneRecord(record))
	return cloneRecord(record), nil
}

func (b *Book) State() State {
	if b == nil {
		return State{}
	}
	return cloneState(b.state)
}

func (b *Book) Records() []Record {
	if b == nil {
		return nil
	}
	records := make([]Record, len(b.records))
	for i := range b.records {
		records[i] = cloneRecord(b.records[i])
	}
	return records
}

func Replay(records []Record) (*Book, error) {
	if len(records) == 0 {
		return nil, errors.New("paper journal is empty")
	}
	b := &Book{}
	for i := range records {
		record := cloneRecord(records[i])
		sequence := uint64(i) + 1
		if record.Version != Version {
			return nil, fmt.Errorf("record %d has unsupported version %d", sequence, record.Version)
		}
		if record.Sequence != sequence {
			return nil, fmt.Errorf("record %d has sequence %d", sequence, record.Sequence)
		}
		previous := ""
		if i != 0 {
			previous = records[i-1].SHA256
		}
		if record.PreviousSHA256 != previous {
			return nil, fmt.Errorf("record %d previous hash mismatch", sequence)
		}
		want, err := recordHash(record)
		if err != nil {
			return nil, fmt.Errorf("record %d hash: %w", sequence, err)
		}
		if record.SHA256 != want {
			return nil, fmt.Errorf("record %d hash mismatch", sequence)
		}
		if i == 0 && record.Command.Type != Opened {
			return nil, errors.New("first paper record must open the account")
		}
		next, err := apply(cloneState(b.state), record.Command)
		if err != nil {
			return nil, fmt.Errorf("record %d: %w", sequence, err)
		}
		b.state = next
		b.records = append(b.records, record)
	}
	return b, nil
}

func ReplayJSON(data []byte) (*Book, error) {
	var records []Record
	if err := strictjson.Decode(data, &records); err != nil {
		return nil, fmt.Errorf("decode paper journal: %w", err)
	}
	return Replay(records)
}

func apply(state State, command Command) (State, error) {
	if err := validateCommandShape(command); err != nil {
		return State{}, err
	}
	if !state.Initialized && command.Type != Opened {
		return State{}, errors.New("paper account is not open")
	}

	switch command.Type {
	case Opened:
		if state.Initialized {
			return State{}, errors.New("paper account is already open")
		}
		if command.CollateralMicros == 0 || command.CollateralMicros > MaxStartingCollateralMicros || command.CollateralMicros > math.MaxInt64 {
			return State{}, errors.New("starting collateral is outside the supported range")
		}
		state.Initialized = true
		state.StartingCollateralMicros = command.CollateralMicros
		state.BalanceMicros = int64(command.CollateralMicros)
		state.EquityMicros = int64(command.CollateralMicros)
	case OrderPlaced:
		if state.PendingOrder != nil || state.Position != nil {
			return State{}, errors.New("paper account already has an active order or position")
		}
		if err := validateOrder(*command.Order); err != nil {
			return State{}, err
		}
		order := *command.Order
		state.PendingOrder = &order
	case OrderFilled:
		if state.PendingOrder == nil {
			return State{}, errors.New("paper account has no pending order")
		}
		if command.OrderID != state.PendingOrder.ID {
			return State{}, errors.New("fill order ID does not match the pending order")
		}
		if err := validatePrice(command.PriceMicros); err != nil {
			return State{}, err
		}
		order := *state.PendingOrder
		if order.Kind == Limit {
			if order.Side == Long && command.PriceMicros > order.LimitPriceMicros {
				return State{}, errors.New("long limit fill is above the limit price")
			}
			if order.Side == Short && command.PriceMicros < order.LimitPriceMicros {
				return State{}, errors.New("short limit fill is below the limit price")
			}
		}
		notional, err := notionalMicros(order.Symbol, order.Quantity, command.PriceMicros)
		if err != nil {
			return State{}, err
		}
		margin, err := mulDivCeil(notional, basisPoints, uint64(order.LeverageBPS))
		if err != nil {
			return State{}, fmt.Errorf("initial margin: %w", err)
		}
		fee, err := feeMicros(notional, order.EntryFeeBPS)
		if err != nil {
			return State{}, fmt.Errorf("entry fee: %w", err)
		}
		required, err := addUnsigned(margin, fee)
		if err != nil || state.BalanceMicros < 0 || uint64(state.BalanceMicros) < required {
			return State{}, errors.New("insufficient paper collateral for initial margin and entry fee")
		}
		state.BalanceMicros -= int64(fee)
		state.FeesPaidMicros, err = addUnsigned(state.FeesPaidMicros, fee)
		if err != nil {
			return State{}, errors.New("fee total overflow")
		}
		state.PendingOrder = nil
		state.Position = &Position{
			OrderID:              order.ID,
			Symbol:               order.Symbol,
			Side:                 order.Side,
			Quantity:             order.Quantity,
			EntryPriceMicros:     command.PriceMicros,
			EntryNotionalMicros:  notional,
			LeverageBPS:          order.LeverageBPS,
			ExitFeeBPS:           order.ExitFeeBPS,
			MaintenanceMarginBPS: order.MaintenanceMarginBPS,
			InitialMarginMicros:  margin,
		}
		if err := mark(&state, command.PriceMicros); err != nil {
			return State{}, err
		}
	case Marked:
		if state.Position == nil {
			return State{}, errors.New("paper account has no open position")
		}
		if err := mark(&state, command.PriceMicros); err != nil {
			return State{}, err
		}
		if err := liquidateIfNeeded(&state); err != nil {
			return State{}, err
		}
	case FundingApplied:
		if state.Position == nil {
			return State{}, errors.New("paper account has no open position")
		}
		if command.FundingPaymentMicros == 0 {
			return State{}, errors.New("funding payment must be nonzero")
		}
		if err := mark(&state, command.PriceMicros); err != nil {
			return State{}, err
		}
		currentNotional, err := notionalMicros(state.Position.Symbol, state.Position.Quantity, command.PriceMicros)
		if err != nil {
			return State{}, err
		}
		if exceedsSignedMagnitude(command.FundingPaymentMicros, currentNotional) {
			return State{}, errors.New("funding payment exceeds current notional")
		}
		state.BalanceMicros, err = addSigned(state.BalanceMicros, command.FundingPaymentMicros)
		if err != nil {
			return State{}, errors.New("paper balance overflow")
		}
		state.FundingPnLMicros, err = addSigned(state.FundingPnLMicros, command.FundingPaymentMicros)
		if err != nil {
			return State{}, errors.New("funding total overflow")
		}
		if err := refreshRisk(&state, command.PriceMicros); err != nil {
			return State{}, err
		}
		if err := liquidateIfNeeded(&state); err != nil {
			return State{}, err
		}
	case PositionClosed:
		if state.Position == nil {
			return State{}, errors.New("paper account has no open position")
		}
		if err := closePosition(&state, command.PriceMicros, "operator"); err != nil {
			return State{}, err
		}
	default:
		return State{}, fmt.Errorf("unsupported paper event %q", command.Type)
	}
	return state, nil
}

func validateCommandShape(command Command) error {
	switch command.Type {
	case Opened:
		if command.Order != nil || command.OrderID != "" || command.PriceMicros != 0 || command.FundingPaymentMicros != 0 {
			return errors.New("open command contains unrelated fields")
		}
	case OrderPlaced:
		if command.CollateralMicros != 0 || command.Order == nil || command.OrderID != "" || command.PriceMicros != 0 || command.FundingPaymentMicros != 0 {
			return errors.New("place-order command fields are invalid")
		}
	case OrderFilled:
		if command.CollateralMicros != 0 || command.Order != nil || command.OrderID == "" || command.PriceMicros == 0 || command.FundingPaymentMicros != 0 {
			return errors.New("fill command fields are invalid")
		}
	case Marked, PositionClosed:
		if command.CollateralMicros != 0 || command.Order != nil || command.OrderID != "" || command.PriceMicros == 0 || command.FundingPaymentMicros != 0 {
			return errors.New("price command fields are invalid")
		}
	case FundingApplied:
		if command.CollateralMicros != 0 || command.Order != nil || command.OrderID != "" || command.PriceMicros == 0 {
			return errors.New("funding command fields are invalid")
		}
	default:
		return fmt.Errorf("unsupported paper event %q", command.Type)
	}
	return nil
}

func validateOrder(order Order) error {
	if !validOrderID(order.ID) {
		return errors.New("order ID must contain 1-64 ASCII letters, digits, dots, underscores, or hyphens")
	}
	if _, err := quantityScale(order.Symbol); err != nil {
		return err
	}
	if order.Side != Long && order.Side != Short {
		return fmt.Errorf("unsupported side %q", order.Side)
	}
	if order.Kind != Market && order.Kind != Limit {
		return fmt.Errorf("unsupported order kind %q", order.Kind)
	}
	if order.Quantity == 0 {
		return errors.New("order quantity must be nonzero")
	}
	if order.Kind == Market && order.LimitPriceMicros != 0 {
		return errors.New("market order must not contain a limit price")
	}
	if order.Kind == Limit {
		if err := validatePrice(order.LimitPriceMicros); err != nil {
			return fmt.Errorf("limit price: %w", err)
		}
		if _, err := notionalMicros(order.Symbol, order.Quantity, order.LimitPriceMicros); err != nil {
			return fmt.Errorf("limit order: %w", err)
		}
	}
	if order.LeverageBPS < uint32(basisPoints) || order.LeverageBPS > MaxLeverageBPS {
		return errors.New("leverage must be between 1x and 50x")
	}
	if order.EntryFeeBPS > MaxFeeBPS || order.ExitFeeBPS > MaxFeeBPS {
		return errors.New("fee rate exceeds the supported maximum")
	}
	if order.MaintenanceMarginBPS == 0 || order.MaintenanceMarginBPS > MaxMaintenanceMarginBPS {
		return errors.New("maintenance margin rate is outside the supported range")
	}
	if uint64(order.MaintenanceMarginBPS)*uint64(order.LeverageBPS) >= basisPoints*basisPoints {
		return errors.New("maintenance margin must be lower than initial margin")
	}
	return nil
}

func mark(state *State, price uint64) error {
	if err := validatePrice(price); err != nil {
		return err
	}
	return refreshRisk(state, price)
}

func refreshRisk(state *State, price uint64) error {
	position := state.Position
	if position == nil {
		return errors.New("paper account has no open position")
	}
	currentNotional, err := notionalMicros(position.Symbol, position.Quantity, price)
	if err != nil {
		return err
	}
	pnl, err := positionPnL(position, currentNotional)
	if err != nil {
		return err
	}
	equity, err := addSigned(state.BalanceMicros, pnl)
	if err != nil {
		return errors.New("paper equity overflow")
	}
	maintenance, err := mulDivCeil(currentNotional, uint64(position.MaintenanceMarginBPS), basisPoints)
	if err != nil {
		return fmt.Errorf("maintenance margin: %w", err)
	}
	state.LastMarkPriceMicros = price
	state.UnrealizedPnLMicros = pnl
	state.EquityMicros = equity
	state.MaintenanceMarginMicros = maintenance
	return nil
}

func liquidateIfNeeded(state *State) error {
	if state.Position == nil || (state.EquityMicros > 0 && uint64(state.EquityMicros) > state.MaintenanceMarginMicros) {
		return nil
	}
	return closePosition(state, state.LastMarkPriceMicros, "liquidation")
}

func closePosition(state *State, price uint64, reason string) error {
	if err := mark(state, price); err != nil {
		return err
	}
	position := state.Position
	currentNotional, err := notionalMicros(position.Symbol, position.Quantity, price)
	if err != nil {
		return err
	}
	fee, err := feeMicros(currentNotional, position.ExitFeeBPS)
	if err != nil {
		return fmt.Errorf("exit fee: %w", err)
	}
	balance, err := addSigned(state.BalanceMicros, state.UnrealizedPnLMicros)
	if err != nil {
		return errors.New("paper balance overflow")
	}
	balance, err = addSigned(balance, -int64(fee))
	if err != nil {
		return errors.New("paper balance overflow")
	}
	state.RealizedPnLMicros, err = addSigned(state.RealizedPnLMicros, state.UnrealizedPnLMicros)
	if err != nil {
		return errors.New("realized P&L overflow")
	}
	state.FeesPaidMicros, err = addUnsigned(state.FeesPaidMicros, fee)
	if err != nil {
		return errors.New("fee total overflow")
	}
	state.BalanceMicros = balance
	state.Position = nil
	state.LastMarkPriceMicros = price
	state.UnrealizedPnLMicros = 0
	state.EquityMicros = balance
	state.MaintenanceMarginMicros = 0
	state.LastCloseReason = reason
	if reason == "liquidation" {
		state.Liquidations++
	}
	return nil
}

func positionPnL(position *Position, currentNotional uint64) (int64, error) {
	if position.EntryNotionalMicros > math.MaxInt64 || currentNotional > math.MaxInt64 {
		return 0, errors.New("position P&L exceeds the supported range")
	}
	entry := int64(position.EntryNotionalMicros)
	current := int64(currentNotional)
	if position.Side == Long {
		return current - entry, nil
	}
	return entry - current, nil
}

func notionalMicros(symbol Symbol, quantity, price uint64) (uint64, error) {
	scale, err := quantityScale(symbol)
	if err != nil {
		return 0, err
	}
	if quantity == 0 {
		return 0, errors.New("quantity must be nonzero")
	}
	if err := validatePrice(price); err != nil {
		return 0, err
	}
	notional, err := mulDivFloor(quantity, price, scale)
	if err != nil || notional == 0 || notional > MaxNotionalMicros {
		return 0, errors.New("notional is outside the supported range")
	}
	return notional, nil
}

func quantityScale(symbol Symbol) (uint64, error) {
	switch symbol {
	case SOL:
		return 1_000_000_000, nil
	case BTC, ETH:
		return 100_000_000, nil
	default:
		return 0, fmt.Errorf("unsupported perps symbol %q", symbol)
	}
}

func validatePrice(price uint64) error {
	if price == 0 || price > MaxPriceMicros {
		return errors.New("price is outside the supported range")
	}
	return nil
}

func validOrderID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func feeMicros(notional uint64, bps uint16) (uint64, error) {
	return mulDivCeil(notional, uint64(bps), basisPoints)
}

func mulDivFloor(a, b, divisor uint64) (uint64, error) {
	if divisor == 0 {
		return 0, errors.New("division by zero")
	}
	hi, lo := bits.Mul64(a, b)
	if hi >= divisor {
		return 0, errors.New("fixed-point result overflow")
	}
	quotient, _ := bits.Div64(hi, lo, divisor)
	return quotient, nil
}

func mulDivCeil(a, b, divisor uint64) (uint64, error) {
	if divisor == 0 {
		return 0, errors.New("division by zero")
	}
	hi, lo := bits.Mul64(a, b)
	if hi >= divisor {
		return 0, errors.New("fixed-point result overflow")
	}
	quotient, remainder := bits.Div64(hi, lo, divisor)
	if remainder != 0 {
		if quotient == math.MaxUint64 {
			return 0, errors.New("fixed-point result overflow")
		}
		quotient++
	}
	return quotient, nil
}

func addSigned(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, errors.New("signed integer overflow")
	}
	return a + b, nil
}

func addUnsigned(a, b uint64) (uint64, error) {
	if a > math.MaxUint64-b {
		return 0, errors.New("unsigned integer overflow")
	}
	return a + b, nil
}

func exceedsSignedMagnitude(value int64, maximum uint64) bool {
	if value >= 0 {
		return uint64(value) > maximum
	}
	return uint64(-(value+1))+1 > maximum
}

func recordHash(record Record) (string, error) {
	record.SHA256 = ""
	canonical, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(hashDomain), canonical...))
	return hex.EncodeToString(sum[:]), nil
}

func cloneOrder(order *Order) *Order {
	if order == nil {
		return nil
	}
	copy := *order
	return &copy
}

func clonePosition(position *Position) *Position {
	if position == nil {
		return nil
	}
	copy := *position
	return &copy
}

func cloneCommand(command Command) Command {
	command.Order = cloneOrder(command.Order)
	return command
}

func cloneRecord(record Record) Record {
	record.Command = cloneCommand(record.Command)
	return record
}

func cloneState(state State) State {
	state.PendingOrder = cloneOrder(state.PendingOrder)
	state.Position = clonePosition(state.Position)
	return state
}
