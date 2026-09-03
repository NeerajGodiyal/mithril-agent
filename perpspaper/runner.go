package perpspaper

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

const fundingRateScale uint64 = 1_000_000_000_000

const maxContextBookSeparationMillis int64 = 30_000

type RiskArm string

const (
	Conservative RiskArm = "conservative"
	Balanced     RiskArm = "balanced"
	Experimental RiskArm = "experimental"
)

type Direction string

const Flat Direction = "flat"

type Decision struct {
	Symbol      Symbol    `json:"symbol"`
	Direction   Direction `json:"direction"`
	RiskArm     RiskArm   `json:"risk_arm"`
	ChangeBPS   int64     `json:"change_bps"`
	NotionalBPS uint16    `json:"notional_bps"`
	LeverageBPS uint32    `json:"leverage_bps"`
}

type PriceContext struct {
	Symbol     Symbol `json:"symbol"`
	MarkPx     string `json:"mark_px"`
	OraclePx   string `json:"oracle_px"`
	ReceivedAt int64  `json:"received_at"`
}

// Decide is deliberately deterministic: the same closed-candle tape always
// produces the same paper intent. It does not claim or predict profitability.
func Decide(symbol Symbol, arm RiskArm, candles []Candle) (Decision, error) {
	if !supportedSymbol(symbol) || len(candles) < 2 {
		return Decision{}, errors.New("decision needs a supported symbol and at least two candles")
	}
	for i := range candles {
		if candles[i].Symbol != symbol || candles[i].CloseTime <= 0 || candles[i].CloseTime < candles[i].OpenTime ||
			(i > 0 && (candles[i].OpenTime <= candles[i-1].OpenTime || candles[i].OpenTime <= candles[i-1].CloseTime)) {
			return Decision{}, errors.New("decision candle tape is not ordered for the requested symbol")
		}
	}
	threshold, allocation, leverage, err := armPolicy(arm)
	if err != nil {
		return Decision{}, err
	}
	first, err := decimalMicros(candles[0].Close)
	if err != nil || first == 0 {
		return Decision{}, errors.New("invalid first close")
	}
	last, err := decimalMicros(candles[len(candles)-1].Close)
	if err != nil || validatePrice(last) != nil {
		return Decision{}, errors.New("invalid last close")
	}
	change, err := signedChangeBPS(first, last)
	if err != nil {
		return Decision{}, err
	}
	direction := Flat
	if change >= threshold {
		direction = Direction(Long)
	} else if change <= -threshold {
		direction = Direction(Short)
	}
	return Decision{Symbol: symbol, Direction: direction, RiskArm: arm, ChangeBPS: change, NotionalBPS: allocation, LeverageBPS: leverage}, nil
}

type TapeFrame struct {
	Candles []Candle     `json:"candles"`
	Context PriceContext `json:"context"`
	Book    L2Book       `json:"book"`
	Funding []Funding    `json:"funding,omitempty"`
}

type TapeResult struct {
	Decision     Decision `json:"decision"`
	Fill         *Fill    `json:"fill,omitempty"`
	VisibleQuote *Fill    `json:"visible_quote,omitempty"`
	Action       string   `json:"action"`
	Records      []Record `json:"records,omitempty"`
}

type TapeReplay struct {
	Results []TapeResult `json:"results"`
	Records []Record     `json:"records"`
	State   State        `json:"state"`
}

type ReplayConfig struct {
	StartingCollateralMicros uint64  `json:"starting_collateral_micros"`
	Symbol                   Symbol  `json:"symbol"`
	RiskArm                  RiskArm `json:"risk_arm"`
	Quantity                 uint64  `json:"quantity"`           // optional maximum quantity; zero uses the arm's notional allocation
	VenueMaxLeverage         uint32  `json:"venue_max_leverage"` // multiplier reported by venue metadata
	VenueSzDecimals          uint8   `json:"venue_sz_decimals"`
}

// ReplayTape binds every modeled fill to the hash-chained paper book. It uses
// no clock or network input, so an identical tape has an identical result.
func ReplayTape(config ReplayConfig, frames []TapeFrame) (TapeReplay, error) {
	return replayTape(config, frames, Decide)
}

func replayTape(config ReplayConfig, frames []TapeFrame, decide func(Symbol, RiskArm, []Candle) (Decision, error)) (TapeReplay, error) {
	if _, err := lotSize(config.Symbol, config.VenueSzDecimals); len(frames) == 0 || err != nil || config.VenueMaxLeverage == 0 || config.VenueMaxLeverage > MaxLeverageBPS/uint32(basisPoints) || config.Quantity != 0 && !validLot(config.Symbol, config.Quantity, config.VenueSzDecimals) {
		return TapeReplay{}, errors.New("paper replay configuration is invalid")
	}
	_, _, policyLeverage, err := armPolicy(config.RiskArm)
	if err != nil {
		return TapeReplay{}, err
	}
	venueLeverage, err := mulDivFloor(uint64(config.VenueMaxLeverage), basisPoints, 1)
	if err != nil || venueLeverage > uint64(MaxLeverageBPS) {
		venueLeverage = uint64(MaxLeverageBPS)
	}
	leverage := min(policyLeverage, uint32(venueLeverage))
	entryFee, exitFee, maintenance := armAccounting(config.RiskArm)
	book, err := New(config.StartingCollateralMicros)
	if err != nil {
		return TapeReplay{}, err
	}
	results := make([]TapeResult, len(frames))
	lastBookTime, lastCandleClose, lastFundingTime, lastContextTime := int64(0), int64(0), int64(0), int64(0)
	for i := range frames {
		frame := frames[i]
		if len(frame.Candles) == 0 || frame.Book.Symbol != config.Symbol || (i > 0 && frame.Book.Time <= lastBookTime) {
			return TapeReplay{}, fmt.Errorf("frame %d market time reverses", i)
		}
		if err := validateVenueBook(frame.Book, config.Symbol, config.VenueSzDecimals); err != nil {
			return TapeReplay{}, fmt.Errorf("frame %d book: %w", i, err)
		}
		finalClose := frame.Candles[len(frame.Candles)-1].CloseTime
		if finalClose <= lastCandleClose || finalClose > frame.Book.Time {
			return TapeReplay{}, fmt.Errorf("frame %d uses stale or future candle data", i)
		}
		markPrice, _, err := priceContextMicros(frame.Context, config.Symbol)
		if err != nil {
			return TapeReplay{}, fmt.Errorf("frame %d price context: %w", i, err)
		}
		contextBookSeparation := frame.Book.Time - frame.Context.ReceivedAt
		if frame.Context.ReceivedAt > frame.Book.Time {
			contextBookSeparation = frame.Context.ReceivedAt - frame.Book.Time
		}
		if frame.Context.ReceivedAt <= 0 || frame.Context.ReceivedAt < lastContextTime ||
			contextBookSeparation > maxContextBookSeparationMillis {
			return TapeReplay{}, fmt.Errorf("frame %d price context time is invalid", i)
		}
		lastContextTime = frame.Context.ReceivedAt
		if len(frame.Funding) > 1 {
			return TapeReplay{}, fmt.Errorf("frame %d has multiple funding settlements without individual marks", i)
		}
		before := len(book.Records())
		hadPosition := book.State().Position != nil
		for j, funding := range frame.Funding {
			if funding.Symbol != config.Symbol || funding.Time <= finalClose || funding.Time > frame.Book.Time || (j > 0 && funding.Time < frame.Funding[j-1].Time) {
				return TapeReplay{}, fmt.Errorf("frame %d has noncausal funding", i)
			}
			if _, rateErr := parseSignedDecimal(funding.Rate, fundingRateScale); rateErr != nil {
				return TapeReplay{}, fmt.Errorf("frame %d has invalid funding: %w", i, rateErr)
			}
			if funding.Time <= lastFundingTime {
				continue
			}
			if i > 0 && funding.Time <= lastBookTime {
				return TapeReplay{}, fmt.Errorf("frame %d has retroactive funding", i)
			}
			if i == 0 {
				return TapeReplay{}, fmt.Errorf("frame %d funding has no prior causal price context", i)
			}
			lastFundingTime = funding.Time
			if book.State().Position == nil {
				continue
			}
			priorMark, priorOracle, contextErr := priceContextMicros(frames[i-1].Context, config.Symbol)
			if contextErr != nil {
				return TapeReplay{}, fmt.Errorf("frame %d prior funding context: %w", i, contextErr)
			}
			payment, err := fundingPayment(book.State().Position, priorOracle, funding.Rate)
			if err != nil {
				return TapeReplay{}, fmt.Errorf("frame %d funding: %w", i, err)
			}
			if payment != 0 {
				if _, err := book.Append(Command{Type: FundingApplied, PriceMicros: priorMark, FundingPaymentMicros: payment}); err != nil {
					return TapeReplay{}, fmt.Errorf("frame %d funding: %w", i, err)
				}
			}
		}
		if book.State().Position != nil {
			if _, err := book.Append(Command{Type: Marked, PriceMicros: markPrice}); err != nil {
				return TapeReplay{}, fmt.Errorf("frame %d mark: %w", i, err)
			}
		}
		decision, err := decide(config.Symbol, config.RiskArm, frame.Candles)
		if err != nil {
			return TapeReplay{}, fmt.Errorf("frame %d decision: %w", i, err)
		}
		results[i].Decision = decision
		position := book.State().Position
		if hadPosition && position == nil {
			results[i].Action = "liquidated"
		} else if position != nil && decision.Direction == Direction(position.Side) {
			results[i].Action = "marked"
		} else if position != nil {
			fill, fillErr := WalkBook(config.Symbol, opposite(position.Side), position.Quantity, config.VenueSzDecimals, frame.Book)
			if fillErr != nil {
				return TapeReplay{}, fmt.Errorf("frame %d close: %w", i, fillErr)
			}
			if fill.Complete {
				_, err = book.Append(Command{Type: PositionClosed, PriceMicros: fill.AveragePriceMicros})
				results[i].Fill = &fill
				results[i].Action = "closed"
			} else {
				results[i].VisibleQuote = &fill
				results[i].Action = "waiting_for_full_close"
			}
		} else if decision.Direction == Flat {
			results[i].Action = "flat"
		} else {
			executionPrice, priceErr := firstBookPrice(frame.Book, Side(decision.Direction))
			if priceErr != nil {
				return TapeReplay{}, fmt.Errorf("frame %d size: %w", i, priceErr)
			}
			quantity, quantityErr := decisionQuantity(book.State(), decision, executionPrice, config)
			if quantityErr != nil {
				return TapeReplay{}, fmt.Errorf("frame %d size: %w", i, quantityErr)
			}
			if quantity == 0 {
				results[i].Action = "below_minimum_lot"
				lastBookTime, lastCandleClose = frame.Book.Time, finalClose
				continue
			}
			fill, fillErr := WalkBook(config.Symbol, Side(decision.Direction), quantity, config.VenueSzDecimals, frame.Book)
			if fillErr != nil {
				return TapeReplay{}, fmt.Errorf("frame %d open: %w", i, fillErr)
			}
			if fill.FilledQuantity == 0 {
				results[i].VisibleQuote = &fill
				results[i].Action = "no_visible_fill"
			} else {
				target, targetErr := decisionNotional(book.State(), decision)
				actual, actualErr := notionalMicros(config.Symbol, fill.FilledQuantity, fill.AveragePriceMicros)
				if targetErr != nil || actualErr != nil {
					return TapeReplay{}, fmt.Errorf("frame %d fill notional is invalid", i)
				}
				if actual > target {
					results[i].VisibleQuote = &fill
					results[i].Action = "slippage_limit"
					lastBookTime, lastCandleClose = frame.Book.Time, finalClose
					continue
				}
				order := Order{ID: fmt.Sprintf("tape-%d", i+1), Symbol: config.Symbol, Side: Side(decision.Direction), Kind: Market, Quantity: fill.FilledQuantity, LeverageBPS: leverage, EntryFeeBPS: entryFee, ExitFeeBPS: exitFee, MaintenanceMarginBPS: maintenance}
				if _, err = book.Append(Command{Type: OrderPlaced, Order: &order}); err == nil {
					_, err = book.Append(Command{Type: OrderFilled, OrderID: order.ID, PriceMicros: fill.AveragePriceMicros})
				}
				results[i].Fill = &fill
				results[i].Action = "opened"
			}
		}
		if err != nil {
			return TapeReplay{}, fmt.Errorf("frame %d accounting: %w", i, err)
		}
		records := book.Records()
		results[i].Records = append([]Record(nil), records[before:]...)
		lastBookTime, lastCandleClose = frame.Book.Time, finalClose
	}
	return TapeReplay{Results: results, Records: book.Records(), State: book.State()}, nil
}

func priceContextMicros(context PriceContext, symbol Symbol) (uint64, uint64, error) {
	if context.Symbol != symbol {
		return 0, 0, errors.New("price context symbol does not match")
	}
	mark, markErr := decimalMicros(context.MarkPx)
	oracle, oracleErr := decimalMicros(context.OraclePx)
	if markErr != nil || oracleErr != nil || validatePrice(mark) != nil || validatePrice(oracle) != nil {
		return 0, 0, errors.New("mark and oracle prices must be positive supported decimals")
	}
	return mark, oracle, nil
}

func armAccounting(arm RiskArm) (entryFee, exitFee, maintenance uint16) {
	switch arm {
	case Conservative:
		return 5, 5, 500
	case Balanced:
		return 7, 7, 750
	default:
		return 10, 10, 1_000
	}
}

func opposite(side Side) Side {
	if side == Long {
		return Short
	}
	return Long
}

func fundingPayment(position *Position, oraclePrice uint64, rate string) (int64, error) {
	scaledRate, err := parseSignedDecimal(rate, fundingRateScale)
	if err != nil {
		return 0, err
	}
	notional, err := notionalMicros(position.Symbol, position.Quantity, oraclePrice)
	if err != nil {
		return 0, err
	}
	magnitude := uint64(scaledRate)
	if scaledRate < 0 {
		magnitude = uint64(-(scaledRate + 1)) + 1
	}
	payment, err := mulDivFloor(notional, magnitude, fundingRateScale)
	if err != nil || payment > uint64(^uint64(0)>>1) {
		return 0, errors.New("funding payment exceeds supported range")
	}
	signed := int64(payment)
	if (position.Side == Long) == (scaledRate > 0) {
		signed = -signed
	}
	return signed, nil
}

func decisionNotional(state State, decision Decision) (uint64, error) {
	if state.EquityMicros <= 0 {
		return 0, nil
	}
	return mulDivFloor(uint64(state.EquityMicros), uint64(decision.NotionalBPS), basisPoints)
}

func decisionQuantity(state State, decision Decision, executionPrice uint64, config ReplayConfig) (uint64, error) {
	notional, err := decisionNotional(state, decision)
	if err != nil || notional == 0 {
		return 0, err
	}
	scale, err := quantityScale(config.Symbol)
	if err != nil {
		return 0, err
	}
	quantity, err := mulDivFloor(notional, scale, executionPrice)
	if err != nil {
		return 0, err
	}
	lot, err := lotSize(config.Symbol, config.VenueSzDecimals)
	if err != nil {
		return 0, err
	}
	quantity -= quantity % lot
	if config.Quantity != 0 {
		quantity = min(quantity, config.Quantity)
	}
	return quantity, nil
}

func firstBookPrice(book L2Book, side Side) (uint64, error) {
	levels := book.Levels[1]
	if side == Short {
		levels = book.Levels[0]
	} else if side != Long {
		return 0, fmt.Errorf("unsupported side %q", side)
	}
	if len(levels) == 0 {
		return 0, errors.New("visible order book has no executable level")
	}
	price, err := decimalMicros(levels[0].Price)
	if err != nil || validatePrice(price) != nil {
		return 0, errors.New("visible order book has an invalid executable price")
	}
	return price, nil
}

func armPolicy(arm RiskArm) (threshold int64, allocation uint16, leverage uint32, err error) {
	switch arm {
	case Conservative:
		return 100, 1_000, 10_000, nil
	case Balanced:
		return 50, 2_500, 20_000, nil
	case Experimental:
		return 25, 5_000, 30_000, nil
	default:
		return 0, 0, 0, fmt.Errorf("unsupported risk arm %q", arm)
	}
}

type Fill struct {
	RequestedQuantity  uint64 `json:"requested_quantity"`
	FilledQuantity     uint64 `json:"filled_quantity"`
	AveragePriceMicros uint64 `json:"average_price_micros"`
	Complete           bool   `json:"complete"`
}

// FilledNotionalMicros returns the USD notional represented by a visible-book
// fill using the venue quantity scale for the selected market.
func FilledNotionalMicros(symbol Symbol, fill Fill) (uint64, error) {
	if fill.FilledQuantity == 0 || fill.AveragePriceMicros == 0 {
		return 0, errors.New("fill has no executed quantity or price")
	}
	return notionalMicros(symbol, fill.FilledQuantity, fill.AveragePriceMicros)
}

// WalkBook models a market entry using only visible liquidity. Levels[0] are
// bids and Levels[1] asks, matching Hyperliquid's documented response order.
func WalkBook(symbol Symbol, side Side, quantity uint64, szDecimals uint8, book L2Book) (Fill, error) {
	if !supportedSymbol(symbol) || book.Symbol != symbol || quantity == 0 || !validLot(symbol, quantity, szDecimals) {
		return Fill{}, errors.New("invalid visible-book fill request")
	}
	if err := validateVenueBook(book, symbol, szDecimals); err != nil {
		return Fill{}, err
	}
	levels := book.Levels[1]
	if side == Short {
		levels = book.Levels[0]
	} else if side != Long {
		return Fill{}, fmt.Errorf("unsupported side %q", side)
	}
	remaining, filled := quantity, uint64(0)
	total := new(big.Int)
	for _, level := range levels {
		if !validHyperliquidPrice(level.Price, szDecimals) {
			return Fill{}, errors.New("book price is not aligned to venue tick precision")
		}
		price, err := decimalMicros(level.Price)
		if err != nil {
			return Fill{}, fmt.Errorf("book price: %w", err)
		}
		if err := validatePrice(price); err != nil {
			return Fill{}, fmt.Errorf("book price: %w", err)
		}
		size, err := decimalUnits(symbol, level.Size)
		if err != nil {
			return Fill{}, fmt.Errorf("book size: %w", err)
		}
		if !validLot(symbol, size, szDecimals) {
			return Fill{}, errors.New("book size is not aligned to venue lot precision")
		}
		take := min(remaining, size)
		cost := new(big.Int).SetUint64(take)
		cost.Mul(cost, new(big.Int).SetUint64(price))
		total.Add(total, cost)
		filled += take
		remaining -= take
		if remaining == 0 {
			break
		}
	}
	if filled == 0 {
		return Fill{RequestedQuantity: quantity}, nil
	}
	average := total.Div(total, new(big.Int).SetUint64(filled))
	if !average.IsUint64() {
		return Fill{}, errors.New("visible-book fill overflow")
	}
	return Fill{RequestedQuantity: quantity, FilledQuantity: filled, AveragePriceMicros: average.Uint64(), Complete: filled == quantity}, nil
}

func validateVenueBook(book L2Book, symbol Symbol, szDecimals uint8) error {
	if err := validateBook(book, symbol); err != nil {
		return err
	}
	for _, levels := range book.Levels {
		for _, level := range levels {
			quantity, err := decimalUnits(symbol, level.Size)
			if err != nil || !validLot(symbol, quantity, szDecimals) {
				return errors.New("book size is not aligned to venue lot precision")
			}
			if !validHyperliquidPrice(level.Price, szDecimals) {
				return errors.New("book price is not aligned to venue tick precision")
			}
		}
	}
	return nil
}

func validLot(symbol Symbol, quantity uint64, szDecimals uint8) bool {
	lot, err := lotSize(symbol, szDecimals)
	if err != nil {
		return false
	}
	return quantity > 0 && quantity%lot == 0
}

func lotSize(symbol Symbol, szDecimals uint8) (uint64, error) {
	scale, err := quantityScale(symbol)
	if err != nil {
		return 0, err
	}
	digits := uint8(len(strconv.FormatUint(scale, 10)) - 1)
	if szDecimals > 6 || szDecimals > digits {
		return 0, errors.New("venue size precision is outside supported perps rules")
	}
	lot := scale
	for range szDecimals {
		lot /= 10
	}
	return lot, nil
}

func validHyperliquidPrice(value string, szDecimals uint8) bool {
	if szDecimals > 6 || strings.Count(value, ".") > 1 || value == "" {
		return false
	}
	price, err := decimalMicros(value)
	if err != nil || validatePrice(price) != nil {
		return false
	}
	parts := strings.SplitN(value, ".", 2)
	fraction := ""
	if len(parts) == 2 {
		fraction = strings.TrimRight(parts[1], "0")
	}
	if len(fraction) > 6-int(szDecimals) {
		return false
	}
	if fraction == "" {
		return true
	}
	significant := strings.TrimLeft(parts[0]+fraction, "0")
	return len(significant) > 0 && len(significant) <= 5
}

func signedChangeBPS(first, last uint64) (int64, error) {
	if first > uint64(^uint64(0)>>1) || last > uint64(^uint64(0)>>1) {
		return 0, errors.New("price change exceeds supported range")
	}
	delta := int64(last) - int64(first)
	negative := delta < 0
	magnitude := uint64(delta)
	if negative {
		magnitude = uint64(-(delta + 1)) + 1
	}
	change, err := mulDivFloor(magnitude, basisPoints, first)
	if err != nil || change > uint64(^uint64(0)>>1) {
		return 0, errors.New("price change exceeds supported range")
	}
	if negative {
		return -int64(change), nil
	}
	return int64(change), nil
}

func decimalMicros(value string) (uint64, error) { return parseDecimal(value, 1_000_000) }

func decimalUnits(symbol Symbol, value string) (uint64, error) {
	scale, err := quantityScale(symbol)
	if err != nil {
		return 0, err
	}
	return parseDecimal(value, scale)
}

func parseDecimal(value string, scale uint64) (uint64, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.HasPrefix(value, "-") || strings.Count(value, ".") > 1 {
		return 0, errors.New("invalid non-negative decimal")
	}
	parts := strings.SplitN(value, ".", 2)
	whole, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, errors.New("invalid decimal whole part")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	digits := len(strconv.FormatUint(scale, 10)) - 1
	if len(fraction) > digits {
		return 0, errors.New("decimal precision exceeds supported scale")
	}
	fraction += strings.Repeat("0", digits-len(fraction))
	fractional := uint64(0)
	if fraction != "" {
		fractional, err = strconv.ParseUint(fraction, 10, 64)
		if err != nil {
			return 0, errors.New("invalid decimal fraction")
		}
	}
	scaled, err := mulDivFloor(whole, scale, 1)
	if err != nil {
		return 0, errors.New("decimal exceeds supported range")
	}
	return addUnsigned(scaled, fractional)
}

func parseSignedDecimal(value string, scale uint64) (int64, error) {
	negative := strings.HasPrefix(value, "-")
	if negative || strings.HasPrefix(value, "+") {
		value = value[1:]
	}
	magnitude, err := parseDecimal(value, scale)
	if err != nil || magnitude > uint64(^uint64(0)>>1) {
		return 0, errors.New("signed decimal exceeds supported range")
	}
	if negative {
		return -int64(magnitude), nil
	}
	return int64(magnitude), nil
}
