package perpspaper

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// TournamentVersion identifies the current research result and strategy rules.
const (
	TournamentVersion  uint32 = 1
	tournamentLookback        = 5
)

// Strategy identifies a deterministic paper-only decision rule.
type Strategy string

// Supported tournament strategies are fixed so input comparisons remain reproducible.
const (
	StrategyMomentum      Strategy = "momentum"
	StrategyMeanReversion Strategy = "mean_reversion"
	StrategyBreakout      Strategy = "breakout"
	StrategyRegime        Strategy = "regime"
)

// TournamentScore contains comparable after-cost results for an eligible strategy.
type TournamentScore struct {
	EndingEquityMicros int64  `json:"ending_equity_micros"`
	NetPnLMicros       int64  `json:"net_pnl_micros"`
	MaxDrawdownMicros  uint64 `json:"max_drawdown_micros"`
	FeesPaidMicros     uint64 `json:"fees_paid_micros"`
	FundingPnLMicros   int64  `json:"funding_pnl_micros"`
	Liquidations       uint64 `json:"liquidations"`
	FilledOrders       uint64 `json:"filled_orders"`
	ClosedPositions    uint64 `json:"closed_positions"`
}

// TournamentResult describes one strategy evaluated on the tournament's exact input.
type TournamentResult struct {
	Strategy         Strategy         `json:"strategy"`
	Eligible         bool             `json:"eligible"`
	IneligibleReason string           `json:"ineligible_reason,omitempty"`
	Score            *TournamentScore `json:"score,omitempty"`
}

// Tournament is research evidence only and cannot authorize paper or live execution.
type Tournament struct {
	Version     uint32             `json:"version"`
	Status      string             `json:"status"`
	PaperOnly   bool               `json:"paper_only"`
	Authorized  bool               `json:"authorized"`
	Promotable  bool               `json:"promotable"`
	InputSHA256 string             `json:"input_sha256"`
	Config      ReplayConfig       `json:"config"`
	Frames      uint64             `json:"frames"`
	NoTrade     TournamentScore    `json:"no_trade_benchmark"`
	Winner      Strategy           `json:"winner,omitempty"`
	Results     []TournamentResult `json:"results"`
}

// RunTournament compares fixed strategies on one causal tape and risk configuration.
// It closes every surviving position through the final visible book before ranking.
func RunTournament(config ReplayConfig, frames []TapeFrame) (Tournament, error) {
	causal, err := tournamentCausalFrames(config, frames)
	if err != nil {
		return Tournament{}, err
	}
	digest, err := tournamentInputSHA256(config, frames)
	if err != nil {
		return Tournament{}, err
	}
	result := Tournament{
		Version: TournamentVersion, Status: "research_only", PaperOnly: true,
		InputSHA256: digest, Config: config, Frames: uint64(len(frames)),
		NoTrade: TournamentScore{EndingEquityMicros: int64(config.StartingCollateralMicros)},
	}
	strategies := [...]Strategy{
		StrategyMomentum, StrategyMeanReversion, StrategyBreakout, StrategyRegime,
	}
	for _, strategy := range strategies {
		replay, err := replayTape(config, causal, func(symbol Symbol, arm RiskArm, candles []Candle) (Decision, error) {
			return tournamentDecision(strategy, symbol, arm, candles)
		})
		if err != nil {
			return Tournament{}, fmt.Errorf("%s strategy: %w", strategy, err)
		}
		candidate, err := scoreTournamentStrategy(config, frames[len(frames)-1].Book, strategy, replay)
		if err != nil {
			return Tournament{}, fmt.Errorf("%s strategy: %w", strategy, err)
		}
		result.Results = append(result.Results, candidate)
	}
	result.Winner = tournamentWinner(result.Results)
	return result, nil
}

func tournamentCausalFrames(config ReplayConfig, frames []TapeFrame) ([]TapeFrame, error) {
	if len(frames) == 0 {
		return nil, errors.New("tournament tape is empty")
	}
	causal := make([]TapeFrame, len(frames))
	seen := make(map[int64]Candle)
	var prefix []Candle
	for i, frame := range frames {
		if _, err := Decide(config.Symbol, config.RiskArm, frame.Candles); err != nil {
			return nil, fmt.Errorf("frame %d candles: %w", i, err)
		}
		for _, candle := range frame.Candles {
			if previous, ok := seen[candle.CloseTime]; ok {
				if previous != candle {
					return nil, fmt.Errorf("frame %d changes an existing closed candle", i)
				}
				continue
			}
			if len(prefix) != 0 && candle.OpenTime <= prefix[len(prefix)-1].CloseTime {
				return nil, fmt.Errorf("frame %d backfills an unseen candle", i)
			}
			seen[candle.CloseTime] = candle
			prefix = append(prefix, candle)
		}
		causal[i] = frame
		causal[i].Candles = append([]Candle(nil), prefix...)
	}
	return causal, nil
}

func tournamentDecision(strategy Strategy, symbol Symbol, arm RiskArm, candles []Candle) (Decision, error) {
	decision, err := Decide(symbol, arm, candles)
	if err != nil {
		return Decision{}, err
	}
	decision.Direction, decision.ChangeBPS = Flat, 0
	if len(candles) <= tournamentLookback {
		decision.SignalKind = SignalHistoryWarmup
		decision.ThresholdBPS = 0
		return decision, nil
	}
	switch strategy {
	case StrategyMomentum:
		return tournamentMomentum(decision, candles)
	case StrategyMeanReversion:
		return tournamentMeanReversion(decision, candles)
	case StrategyBreakout:
		return tournamentBreakout(decision, candles)
	case StrategyRegime:
		return tournamentRegime(decision, candles)
	default:
		return Decision{}, fmt.Errorf("unsupported tournament strategy %q", strategy)
	}
}

func tournamentMomentum(decision Decision, candles []Candle) (Decision, error) {
	baseline, current, err := tournamentPrices(candles, len(candles)-1-tournamentLookback, len(candles)-1)
	if err != nil {
		return Decision{}, err
	}
	decision.SignalKind = SignalMomentum
	return tournamentDirectionalDecision(decision, baseline, current, false)
}

func tournamentMeanReversion(decision Decision, candles []Candle) (Decision, error) {
	start, end := len(candles)-1-tournamentLookback, len(candles)-1
	var sum uint64
	for i := start; i < end; i++ {
		price, err := tournamentPrice(candles[i])
		if err != nil {
			return Decision{}, err
		}
		sum += price
	}
	current, err := tournamentPrice(candles[end])
	if err != nil {
		return Decision{}, err
	}
	decision.SignalKind = SignalMeanReversion
	return tournamentDirectionalDecision(decision, sum/tournamentLookback, current, true)
}

func tournamentBreakout(decision Decision, candles []Candle) (Decision, error) {
	start, end := len(candles)-1-tournamentLookback, len(candles)-1
	low, err := tournamentPrice(candles[start])
	if err != nil {
		return Decision{}, err
	}
	high := low
	for i := start + 1; i < end; i++ {
		price, err := tournamentPrice(candles[i])
		if err != nil {
			return Decision{}, err
		}
		low, high = min(low, price), max(high, price)
	}
	current, err := tournamentPrice(candles[end])
	if err != nil {
		return Decision{}, err
	}
	threshold, _, _, err := armPolicy(decision.RiskArm)
	if err != nil {
		return Decision{}, err
	}
	decision.ThresholdBPS = threshold
	highChange, err := signedChangeBPS(high, current)
	if err != nil {
		return Decision{}, err
	}
	if highChange >= threshold {
		decision.SignalKind = SignalBreakoutHigh
		decision.Direction, decision.ChangeBPS = Direction(Long), highChange
		return decision, nil
	}
	lowChange, err := signedChangeBPS(low, current)
	if err != nil {
		return Decision{}, err
	}
	if lowChange <= -threshold {
		decision.SignalKind = SignalBreakoutLow
		decision.ChangeBPS = lowChange
		decision.Direction = Direction(Short)
	} else if highChange > 0 {
		decision.SignalKind = SignalBreakoutHigh
		decision.ChangeBPS = highChange
	} else if lowChange < 0 {
		decision.SignalKind = SignalBreakoutLow
		decision.ChangeBPS = lowChange
	} else {
		decision.SignalKind = SignalBreakoutRange
		decision.ChangeBPS = 0
	}
	return decision, nil
}

func tournamentRegime(decision Decision, candles []Candle) (Decision, error) {
	breakout, err := tournamentBreakout(decision, candles)
	if err != nil || breakout.Direction != Flat {
		if breakout.SignalKind == SignalBreakoutHigh {
			breakout.SignalKind = SignalRegimeBreakoutHigh
		} else if breakout.SignalKind == SignalBreakoutLow {
			breakout.SignalKind = SignalRegimeBreakoutLow
		}
		return breakout, err
	}
	momentum, err := tournamentMomentum(decision, candles)
	if err != nil {
		return Decision{}, err
	}
	threshold, _, _, err := armPolicy(decision.RiskArm)
	if err != nil {
		return Decision{}, err
	}
	if momentum.ChangeBPS >= threshold*2 || momentum.ChangeBPS <= -threshold*2 {
		momentum.SignalKind = SignalRegimeMomentum
		momentum.ThresholdBPS = threshold * 2
		return momentum, nil
	}
	meanReversion, err := tournamentMeanReversion(decision, candles)
	if err == nil {
		meanReversion.SignalKind = SignalRegimeMeanReversion
	}
	return meanReversion, err
}

func tournamentDirectionalDecision(decision Decision, baseline, current uint64, reverse bool) (Decision, error) {
	change, err := signedChangeBPS(baseline, current)
	if err != nil {
		return Decision{}, err
	}
	threshold, _, _, err := armPolicy(decision.RiskArm)
	if err != nil {
		return Decision{}, err
	}
	decision.ThresholdBPS = threshold
	decision.ChangeBPS = change
	if change >= threshold {
		decision.Direction = Direction(Long)
	} else if change <= -threshold {
		decision.Direction = Direction(Short)
	}
	if reverse {
		if decision.Direction == Direction(Long) {
			decision.Direction = Direction(Short)
		} else if decision.Direction == Direction(Short) {
			decision.Direction = Direction(Long)
		}
	}
	return decision, nil
}

func tournamentPrices(candles []Candle, left, right int) (uint64, uint64, error) {
	first, err := tournamentPrice(candles[left])
	if err != nil {
		return 0, 0, err
	}
	last, err := tournamentPrice(candles[right])
	return first, last, err
}

func tournamentPrice(candle Candle) (uint64, error) {
	price, err := decimalMicros(candle.Close)
	if err != nil || validatePrice(price) != nil {
		return 0, errors.New("tournament candle close is invalid")
	}
	return price, nil
}

func scoreTournamentStrategy(config ReplayConfig, finalBook L2Book, strategy Strategy, replay TapeReplay) (TournamentResult, error) {
	result := TournamentResult{Strategy: strategy, Eligible: true}
	book, err := Replay(replay.Records)
	if err != nil {
		return TournamentResult{}, err
	}
	if position := book.State().Position; position != nil {
		fill, err := WalkBook(config.Symbol, opposite(position.Side), position.Quantity, config.VenueSzDecimals, finalBook)
		if err != nil {
			return TournamentResult{}, fmt.Errorf("terminal close: %w", err)
		}
		if !fill.Complete {
			result.Eligible = false
			result.IneligibleReason = "terminal_position_cannot_fill_from_visible_book"
			return result, nil
		}
		if _, err := book.Append(Command{Type: PositionClosed, PriceMicros: fill.AveragePriceMicros}); err != nil {
			return TournamentResult{}, fmt.Errorf("terminal close: %w", err)
		}
	}
	score, err := tournamentScore(config, book.Records())
	if err != nil {
		return TournamentResult{}, err
	}
	result.Score = &score
	return result, nil
}

func tournamentScore(config ReplayConfig, records []Record) (TournamentScore, error) {
	var state State
	var peak int64
	var maxDrawdown, fills, closes uint64
	for i, record := range records {
		next, err := apply(state, record.Command)
		if err != nil {
			return TournamentScore{}, fmt.Errorf("score record %d: %w", i+1, err)
		}
		state = next
		peak = max(peak, state.EquityMicros)
		if state.EquityMicros < peak {
			drawdown := tournamentDifference(peak, state.EquityMicros)
			maxDrawdown = max(maxDrawdown, drawdown)
		}
		switch record.Command.Type {
		case OrderFilled:
			fills++
		case PositionClosed:
			closes++
		}
	}
	starting := int64(config.StartingCollateralMicros)
	if state.EquityMicros < math.MinInt64+starting {
		return TournamentScore{}, errors.New("tournament net P&L exceeds the supported range")
	}
	return TournamentScore{
		EndingEquityMicros: state.EquityMicros,
		NetPnLMicros:       state.EquityMicros - starting,
		MaxDrawdownMicros:  maxDrawdown,
		FeesPaidMicros:     state.FeesPaidMicros,
		FundingPnLMicros:   state.FundingPnLMicros,
		Liquidations:       state.Liquidations,
		FilledOrders:       fills,
		ClosedPositions:    closes,
	}, nil
}

func tournamentDifference(high, low int64) uint64 {
	if low >= 0 {
		return uint64(high - low)
	}
	return uint64(high) + uint64(-(low + 1)) + 1
}

func betterTournamentResult(left, right TournamentResult) bool {
	if right.Score == nil {
		return true
	}
	if left.Score.EndingEquityMicros != right.Score.EndingEquityMicros {
		return left.Score.EndingEquityMicros > right.Score.EndingEquityMicros
	}
	if left.Score.MaxDrawdownMicros != right.Score.MaxDrawdownMicros {
		return left.Score.MaxDrawdownMicros < right.Score.MaxDrawdownMicros
	}
	if left.Score.Liquidations != right.Score.Liquidations {
		return left.Score.Liquidations < right.Score.Liquidations
	}
	return left.Strategy < right.Strategy
}

func tournamentWinner(results []TournamentResult) Strategy {
	var best TournamentResult
	for _, result := range results {
		if result.Eligible && result.Score != nil && result.Score.FilledOrders > 0 &&
			result.Score.ClosedPositions > 0 && (best.Score == nil || betterTournamentResult(result, best)) {
			best = result
		}
	}
	return best.Strategy
}

func tournamentInputSHA256(config ReplayConfig, frames []TapeFrame) (string, error) {
	payload, err := json.Marshal(struct {
		Version uint32       `json:"version"`
		Config  ReplayConfig `json:"config"`
		Frames  []TapeFrame  `json:"frames"`
	}{Version: TournamentVersion, Config: config, Frames: frames})
	if err != nil {
		return "", fmt.Errorf("encode tournament input: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}
