package perpspaper

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestTournamentStrategiesAreIndependent(t *testing.T) {
	candles := tournamentTestCandles("100", "102", "98", "102", "98", "100.8")
	want := map[Strategy]Direction{
		StrategyMomentum:      Direction(Long),
		StrategyMeanReversion: Direction(Short),
		StrategyBreakout:      Flat,
		StrategyRegime:        Direction(Short),
	}
	for strategy, direction := range want {
		decision, err := tournamentDecision(strategy, SOL, Balanced, candles)
		if err != nil {
			t.Fatalf("%s decision: %v", strategy, err)
		}
		if decision.Direction != direction {
			t.Errorf("%s direction = %s, want %s", strategy, decision.Direction, direction)
		}
	}
}

func TestTournamentCausalPrefixIgnoresFutureSuffix(t *testing.T) {
	config := tournamentTestConfig()
	common := []int{100, 101, 99, 102, 98, 101, 100, 103}
	up := tournamentTestFrames(append(append([]int(nil), common...), 110, 120))
	down := tournamentTestFrames(append(append([]int(nil), common...), 90, 80))
	causalUp, err := tournamentCausalFrames(config, up)
	if err != nil {
		t.Fatal(err)
	}
	causalDown, err := tournamentCausalFrames(config, down)
	if err != nil {
		t.Fatal(err)
	}
	for i := range common[1:] {
		if got, want := len(causalUp[i].Candles), i+2; got != want {
			t.Fatalf("frame %d causal candle count = %d, want %d", i, got, want)
		}
		for _, strategy := range []Strategy{StrategyMomentum, StrategyMeanReversion, StrategyBreakout, StrategyRegime} {
			left, err := tournamentDecision(strategy, SOL, Balanced, causalUp[i].Candles)
			if err != nil {
				t.Fatal(err)
			}
			right, err := tournamentDecision(strategy, SOL, Balanced, causalDown[i].Candles)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(left, right) {
				t.Fatalf("%s frame %d changed before the divergent suffix: %+v != %+v", strategy, i, left, right)
			}
		}
	}
}

func TestTournamentIsDeterministicAndUsesStableTieBreak(t *testing.T) {
	config := tournamentTestConfig()
	frames := tournamentTestFrames([]int{100, 100, 100, 100, 100, 100, 100, 100})
	first, err := RunTournament(config, frames)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RunTournament(config, frames)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("identical tournament input produced different results")
	}
	if first.Status != "research_only" || !first.PaperOnly || first.Authorized || first.Promotable ||
		first.Winner != StrategyBreakout || len(first.Results) != 4 || len(first.InputSHA256) != 64 {
		t.Fatalf("tournament safety or tie result = %+v", first)
	}
	if first.NoTrade.EndingEquityMicros != int64(config.StartingCollateralMicros) || first.NoTrade.NetPnLMicros != 0 {
		t.Fatalf("no-trade benchmark = %+v", first.NoTrade)
	}
	for _, result := range first.Results {
		if !result.Eligible || result.Score == nil || result.Score.EndingEquityMicros != int64(config.StartingCollateralMicros) {
			t.Fatalf("flat strategy result = %+v", result)
		}
	}
	reversed := append([]TournamentResult(nil), first.Results...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	if winner := tournamentWinner(reversed); winner != first.Winner {
		t.Fatalf("reversed candidate order winner = %s, want %s", winner, first.Winner)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "StartingCollateralMicros") || !strings.Contains(string(encoded), `"starting_collateral_micros"`) {
		t.Fatalf("tournament config JSON is not stable snake case: %s", encoded)
	}
}

func TestTournamentScoresFundingFeesAndTerminalVisibleBookClose(t *testing.T) {
	config := tournamentTestConfig()
	frames := tournamentTestFrames([]int{100, 100, 100, 100, 100, 110, 112})
	last := &frames[len(frames)-1]
	last.Funding = []Funding{{Symbol: SOL, Rate: "0.001", Time: last.Candles[len(last.Candles)-1].CloseTime + 500}}
	tournament, err := RunTournament(config, frames)
	if err != nil {
		t.Fatal(err)
	}
	momentum := tournamentTestResult(t, tournament, StrategyMomentum)
	if !momentum.Eligible || momentum.Score == nil {
		t.Fatalf("momentum result = %+v", momentum)
	}
	if momentum.Score.FilledOrders == 0 || momentum.Score.ClosedPositions == 0 ||
		momentum.Score.FeesPaidMicros == 0 || momentum.Score.FundingPnLMicros >= 0 ||
		momentum.Score.EndingEquityMicros >= int64(config.StartingCollateralMicros) {
		t.Fatalf("terminal after-cost score = %+v", *momentum.Score)
	}
}

func TestTournamentMakesIncompleteTerminalCloseIneligible(t *testing.T) {
	config := tournamentTestConfig()
	frames := tournamentTestFrames([]int{100, 100, 100, 100, 100, 110, 112})
	frames[len(frames)-1].Book.Levels[0] = []Level{{Price: "111", Size: "0.01", Count: 1}}
	tournament, err := RunTournament(config, frames)
	if err != nil {
		t.Fatal(err)
	}
	momentum := tournamentTestResult(t, tournament, StrategyMomentum)
	if momentum.Eligible || momentum.Score != nil || momentum.IneligibleReason != "terminal_position_cannot_fill_from_visible_book" {
		t.Fatalf("incomplete terminal close result = %+v", momentum)
	}
}

func TestTournamentRejectsChangedDuplicateCandle(t *testing.T) {
	config := tournamentTestConfig()
	frames := tournamentTestFrames([]int{100, 101, 102})
	frames[1].Candles[0].Close = "99"
	if _, err := RunTournament(config, frames); err == nil || !strings.Contains(err.Error(), "changes an existing closed candle") {
		t.Fatalf("changed duplicate candle error = %v", err)
	}
}

func tournamentTestConfig() ReplayConfig {
	return ReplayConfig{
		StartingCollateralMicros: 100_000_000,
		Symbol:                   SOL, RiskArm: Balanced, VenueMaxLeverage: 20, VenueSzDecimals: 2,
	}
}

func tournamentTestCandles(prices ...string) []Candle {
	candles := make([]Candle, len(prices))
	for i, price := range prices {
		candles[i] = Candle{
			OpenTime: int64(i*2 + 1), CloseTime: int64(i*2 + 2), Symbol: SOL, Close: price,
		}
	}
	return candles
}

func tournamentTestFrames(prices []int) []TapeFrame {
	candles := make([]Candle, len(prices))
	for i, price := range prices {
		open := int64(i)*60_000 + 1
		candles[i] = Candle{
			OpenTime: open, CloseTime: open + 59_999, Symbol: SOL, Interval: "1m",
			Open: strconv.Itoa(price), Close: strconv.Itoa(price), High: strconv.Itoa(price),
			Low: strconv.Itoa(price), Volume: "1", Trades: 1,
		}
	}
	frames := make([]TapeFrame, len(prices)-1)
	for i := range frames {
		price := prices[i+1]
		bookTime := candles[i+1].CloseTime + 1_000
		frames[i] = TapeFrame{
			Candles: []Candle{candles[i], candles[i+1]},
			Context: PriceContext{Symbol: SOL, MarkPx: strconv.Itoa(price), OraclePx: strconv.Itoa(price), ReceivedAt: bookTime},
			Book: L2Book{Symbol: SOL, Time: bookTime, Levels: [2][]Level{
				{{Price: strconv.Itoa(price - 1), Size: "1000", Count: 1}},
				{{Price: strconv.Itoa(price + 1), Size: "1000", Count: 1}},
			}},
		}
	}
	return frames
}

func tournamentTestResult(t *testing.T, tournament Tournament, strategy Strategy) TournamentResult {
	t.Helper()
	for _, result := range tournament.Results {
		if result.Strategy == strategy {
			return result
		}
	}
	t.Fatalf("tournament has no %s result", strategy)
	return TournamentResult{}
}
