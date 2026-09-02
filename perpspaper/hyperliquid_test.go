package perpspaper

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestHyperliquidClientUsesAllowlistedEndpointAndStrictSchema(t *testing.T) {
	var endpoint, requestBody string
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		endpoint = request.URL.String()
		body, _ := io.ReadAll(request.Body)
		requestBody = string(body)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"coin":"SOL","time":1,"levels":[[{"px":"100","sz":"1.5","n":1}],[{"px":"101","sz":"2","n":1}]]}`)), Header: make(http.Header)}, nil
	})}
	client, err := NewHyperliquidClient(Testnet, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	book, err := client.Book(context.Background(), SOL)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://api.hyperliquid-testnet.xyz/info" || requestBody != `{"coin":"SOL","type":"l2Book"}` || book.Levels[1][0].Price != "101" {
		t.Fatalf("request endpoint=%q body=%s book=%+v", endpoint, requestBody, book)
	}

	httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"coin":"SOL","time":1,"levels":[[],[]],"unexpected":true}`)), Header: make(http.Header)}, nil
	})
	strictClient, _ := NewHyperliquidClient(Testnet, httpClient)
	if _, err := strictClient.Book(context.Background(), SOL); err == nil {
		t.Fatal("unknown response field was accepted")
	}
	if _, err := NewHyperliquidClient("local", httpClient); err == nil {
		t.Fatal("arbitrary endpoint environment was accepted")
	}
	redirect, _ := http.NewRequest(http.MethodGet, "https://example.com/info", nil)
	if err := client.http.CheckRedirect(redirect, nil); err == nil {
		t.Fatal("redirect outside the allowlisted host was accepted")
	}
	exchangeRedirect, _ := http.NewRequest(http.MethodGet, "https://api.hyperliquid-testnet.xyz/exchange", nil)
	if err := client.http.CheckRedirect(exchangeRedirect, nil); err == nil {
		t.Fatal("same-host redirect outside the read-only info endpoint was accepted")
	}
}

func TestFundingHistorySurfacesFullPage(t *testing.T) {
	item := `{"coin":"SOL","fundingRate":"0.0001","premium":"0","time":1500}`
	payload := "[" + strings.Repeat(item+",", 499) + item + "]"
	client, _ := NewHyperliquidClient(Mainnet, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
	})})
	history, err := client.FundingHistory(context.Background(), SOL, 1000, 2000)
	if len(history) != 500 || err != ErrFundingHistoryPageFull {
		t.Fatalf("full funding page len=%d err=%v", len(history), err)
	}
}

func TestHyperliquidClientRejectsOversizedResponse(t *testing.T) {
	client, _ := NewHyperliquidClient(Mainnet, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat(" ", maxHyperliquidResponse+1))), Header: make(http.Header)}, nil
	})})
	if _, err := client.Book(context.Background(), SOL); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized response error = %v", err)
	}
}

func TestHyperliquidReadMethodsValidateRequestsAndResponses(t *testing.T) {
	client, _ := NewHyperliquidClient(Mainnet, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		response := ""
		switch {
		case strings.Contains(string(body), `"type":"metaAndAssetCtxs"`):
			response = `[{"universe":[{"name":"SOL","szDecimals":2,"maxLeverage":20,"marginTableId":54},{"name":"DOGE","szDecimals":0,"maxLeverage":10,"marginTableId":52}],"marginTables":[[50,{"description":"","marginTiers":[]}]],"collateralToken":0},[{"funding":"0.0001","openInterest":"1","prevDayPx":"99","dayNtlVlm":"1000","oraclePx":"100","markPx":"100","dayBaseVlm":"10"},{"funding":"0","openInterest":"1","prevDayPx":"1","dayNtlVlm":"1","oraclePx":"1","markPx":"1","dayBaseVlm":"1"}]]`
		case strings.Contains(string(body), `"type":"candleSnapshot"`):
			response = `[{"t":1000,"T":60999,"s":"SOL","i":"1m","o":"100","c":"101","h":"102","l":"99","v":"10","n":3}]`
		case strings.Contains(string(body), `"type":"fundingHistory"`):
			response = `[{"coin":"SOL","fundingRate":"-0.0001","premium":"0.0002","time":1500}]`
		default:
			t.Fatalf("unexpected request %s", body)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})})

	metadata, err := client.MetaAndAssetContexts(context.Background())
	if err != nil || len(metadata.Universe) != 1 || metadata.Universe[0].Name != SOL || len(metadata.Contexts) != 1 {
		t.Fatalf("filtered metadata = %+v, %v", metadata, err)
	}
	candles, err := client.Candles(context.Background(), SOL, "1m", 1000, 61000)
	if err != nil || len(candles) != 1 || candles[0].Close != "101" {
		t.Fatalf("candles = %+v, %v", candles, err)
	}
	funding, err := client.FundingHistory(context.Background(), SOL, 1000, 2000)
	if err != nil || len(funding) != 1 || funding[0].Rate != "-0.0001" {
		t.Fatalf("funding = %+v, %v", funding, err)
	}
	if _, err := client.Candles(context.Background(), SOL, "7m", 1000, 2000); err == nil {
		t.Fatal("unsupported candle interval was accepted")
	}
	if _, err := client.FundingHistory(context.Background(), "DOGE", 1000, 2000); err == nil {
		t.Fatal("unsupported funding market was accepted")
	}
}

func TestOneMinuteCandlesRequireExactDurationAndAdjacency(t *testing.T) {
	responses := []string{
		`[{"t":1000,"T":1999,"s":"SOL","i":"1m","o":"100","c":"101","h":"102","l":"99","v":"10","n":3}]`,
		`[{"t":1000,"T":60999,"s":"SOL","i":"1m","o":"100","c":"101","h":"102","l":"99","v":"10","n":3},{"t":121000,"T":180999,"s":"SOL","i":"1m","o":"101","c":"102","h":"103","l":"100","v":"10","n":3}]`,
	}
	for _, payload := range responses {
		client, _ := NewHyperliquidClient(Mainnet, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
		})})
		if _, err := client.Candles(context.Background(), SOL, "1m", 1000, 181000); err == nil {
			t.Fatalf("malformed one-minute candles accepted: %s", payload)
		}
	}
}

func TestHyperliquidMetadataRejectsUnsupportedSizePrecision(t *testing.T) {
	client, _ := NewHyperliquidClient(Mainnet, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		payload := `[{"universe":[{"name":"SOL","szDecimals":7,"maxLeverage":20,"marginTableId":54}],"marginTables":[[50,{"description":"","marginTiers":[]}]],"collateralToken":0},[{"funding":"0","openInterest":"1","prevDayPx":"99","dayNtlVlm":"1000","oraclePx":"100","markPx":"100","dayBaseVlm":"10"}]]`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
	})})
	if _, err := client.MetaAndAssetContexts(context.Background()); err == nil || !strings.Contains(err.Error(), "size precision") {
		t.Fatalf("invalid metadata precision error = %v", err)
	}
	frame := TapeFrame{
		Candles: []Candle{{OpenTime: 1, CloseTime: 1, Symbol: SOL, Close: "99"}, {OpenTime: 2, CloseTime: 2, Symbol: SOL, Close: "100"}},
		Context: sampledContext(SOL, "100"),
		Book:    L2Book{Symbol: SOL, Time: 3, Levels: [2][]Level{{{Price: "99", Size: "1", Count: 1}}, {{Price: "100", Size: "1", Count: 1}}}},
	}
	if _, err := ReplayTape(ReplayConfig{StartingCollateralMicros: 100_000_000, Symbol: SOL, RiskArm: Balanced, VenueMaxLeverage: 20, VenueSzDecimals: 10}, []TapeFrame{frame}); err == nil {
		t.Fatal("zero-quantity replay accepted unsafe venue precision")
	}
}

func TestAssetContextRejectsInvalidMarkOrOracle(t *testing.T) {
	valid := AssetContext{
		Funding: "0", OpenInterest: "1", PrevDayPx: "99", DayNtlVlm: "1",
		DayBaseVlm: "1", OraclePx: "100", MarkPx: "100",
	}
	for _, change := range []func(*AssetContext){
		func(context *AssetContext) { context.MarkPx = "0" },
		func(context *AssetContext) { context.MarkPx = "-1" },
		func(context *AssetContext) { context.OraclePx = "0" },
		func(context *AssetContext) { context.OraclePx = "100.0000001" },
	} {
		context := valid
		change(&context)
		if err := validateAssetContext(context); err == nil {
			t.Fatalf("invalid venue context accepted: %+v", context)
		}
	}
}

func TestDecisionAndVisibleBookReplayAreDeterministic(t *testing.T) {
	tape := []Candle{{OpenTime: 1, CloseTime: 1, Symbol: SOL, Interval: "1m", Close: "100"}, {OpenTime: 2, CloseTime: 2, Symbol: SOL, Interval: "1m", Close: "101"}}
	first, err := Decide(SOL, Balanced, tape)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Decide(SOL, Balanced, tape)
	if err != nil || first != second || first.Direction != Direction(Long) || first.ChangeBPS != 100 {
		t.Fatalf("decisions first=%+v second=%+v err=%v", first, second, err)
	}
	flat, err := Decide(SOL, Conservative, []Candle{{OpenTime: 1, CloseTime: 1, Symbol: SOL, Close: "100"}, {OpenTime: 2, CloseTime: 2, Symbol: SOL, Close: "100.50"}})
	if err != nil || flat.Direction != Flat {
		t.Fatalf("flat decision = %+v, %v", flat, err)
	}

	book := L2Book{Symbol: SOL, Time: 3, Levels: [2][]Level{
		{{Price: "99", Size: "1", Count: 1}},
		{{Price: "100", Size: "1", Count: 1}, {Price: "102", Size: "0.5", Count: 1}},
	}}
	fill, err := WalkBook(SOL, Long, 2_000_000_000, 2, book)
	if err != nil {
		t.Fatal(err)
	}
	if fill.FilledQuantity != 1_500_000_000 || fill.AveragePriceMicros != 100_666_666 || fill.Complete {
		t.Fatalf("partial fill = %+v", fill)
	}
	large, err := WalkBook(SOL, Long, 200_000_000_000, 2, L2Book{
		Symbol: SOL, Time: 1,
		Levels: [2][]Level{{}, {{Price: "100", Size: "200", Count: 1}}},
	})
	if err != nil || !large.Complete || large.AveragePriceMicros != 100_000_000 {
		t.Fatalf("large valid fill = %+v, %v", large, err)
	}
	short, err := WalkBook(SOL, Short, 500_000_000, 2, book)
	if err != nil || !short.Complete || short.AveragePriceMicros != 99_000_000 {
		t.Fatalf("short fill = %+v, %v", short, err)
	}

	frames := []TapeFrame{{Candles: tape, Context: sampledContext(SOL, "101"), Book: book}}
	config := ReplayConfig{StartingCollateralMicros: 1_000_000_000, Symbol: SOL, RiskArm: Balanced, Quantity: 2_000_000_000, VenueMaxLeverage: 20, VenueSzDecimals: 2}
	replayA, err := ReplayTape(config, frames)
	if err != nil {
		t.Fatal(err)
	}
	replayB, err := ReplayTape(config, frames)
	if err != nil || !reflect.DeepEqual(replayA, replayB) || replayA.Results[0].Fill == nil || replayA.Results[0].Fill.Complete || replayA.State.Position == nil || replayA.State.Position.Quantity != 1_500_000_000 {
		t.Fatalf("replays A=%+v B=%+v err=%v", replayA, replayB, err)
	}
	if _, err := Replay(replayA.Records); err != nil {
		t.Fatalf("accounting journal does not replay: %v", err)
	}
}

func TestDecimalParsingRejectsRoundingAndUnsupportedMarkets(t *testing.T) {
	if _, err := decimalMicros("1.0000001"); err == nil {
		t.Fatal("silent price rounding was accepted")
	}
	if _, err := decimalUnits(BTC, "0.000000001"); err == nil {
		t.Fatal("silent quantity rounding was accepted")
	}
	if _, err := Decide("DOGE", Balanced, []Candle{{OpenTime: 1, CloseTime: 1, Symbol: "DOGE", Close: "1"}, {OpenTime: 2, CloseTime: 2, Symbol: "DOGE", Close: "2"}}); err == nil {
		t.Fatal("unsupported market was accepted")
	}
}

func TestReplayTapeRejectsLookAheadAndDoesNotStack(t *testing.T) {
	config := ReplayConfig{StartingCollateralMicros: 1_000_000_000, Symbol: SOL, RiskArm: Experimental, Quantity: 1_000_000_000, VenueMaxLeverage: 2, VenueSzDecimals: 2}
	book := L2Book{Symbol: SOL, Time: 3, Levels: [2][]Level{{{Price: "100", Size: "2", Count: 1}}, {{Price: "101", Size: "2", Count: 1}}}}
	candles := []Candle{{OpenTime: 1, CloseTime: 1, Symbol: SOL, Close: "100"}, {OpenTime: 2, CloseTime: 2, Symbol: SOL, Close: "101"}}
	frames := []TapeFrame{{Candles: candles, Context: sampledContext(SOL, "101"), Book: book}, {
		Candles: []Candle{{OpenTime: 2, CloseTime: 2, Symbol: SOL, Close: "101"}, {OpenTime: 3, CloseTime: 3, Symbol: SOL, Close: "102"}},
		Context: sampledContext(SOL, "102"),
		Book:    L2Book{Symbol: SOL, Time: 4, Levels: book.Levels},
		Funding: []Funding{{Symbol: SOL, Rate: "0.0001", Premium: "0", Time: 4}},
	}}
	replay, err := ReplayTape(config, frames)
	if err != nil {
		t.Fatal(err)
	}
	if replay.State.Position == nil || replay.State.Position.LeverageBPS != 20_000 || replay.Results[1].Action != "marked" {
		t.Fatalf("stateful replay = %+v", replay)
	}
	if replay.State.FundingPnLMicros >= 0 {
		t.Fatalf("long positive-rate funding = %d, want debit", replay.State.FundingPnLMicros)
	}

	future := []TapeFrame{{Candles: candles, Context: sampledContext(SOL, "101"), Book: L2Book{Symbol: SOL, Time: 1, Levels: book.Levels}}}
	if _, err := ReplayTape(config, future); err == nil {
		t.Fatal("future candle relative to book was accepted")
	}
	stale := append([]TapeFrame(nil), frames...)
	stale[1].Candles = candles
	if _, err := ReplayTape(config, stale); err == nil {
		t.Fatal("stale repeated candle window was accepted")
	}
	reusedBook := append([]TapeFrame(nil), frames...)
	reusedBook[1].Book.Time = reusedBook[0].Book.Time
	if _, err := ReplayTape(config, reusedBook); err == nil {
		t.Fatal("a reused book snapshot was accepted")
	}
}

func TestReplayTapeUsesPolicySizeAndCausalLivePrecisionFunding(t *testing.T) {
	config := ReplayConfig{StartingCollateralMicros: 100_000_000, Symbol: SOL, RiskArm: Balanced, Quantity: 1_000_000_000, VenueMaxLeverage: 20, VenueSzDecimals: 2}
	levels := [2][]Level{{{Price: "99", Size: "2", Count: 1}}, {{Price: "100", Size: "2", Count: 1}}}
	first := TapeFrame{
		Candles: []Candle{{OpenTime: 1, CloseTime: 1, Symbol: SOL, Close: "99"}, {OpenTime: 2, CloseTime: 2, Symbol: SOL, Close: "100"}},
		Context: sampledContext(SOL, "100"),
		Book:    L2Book{Symbol: SOL, Time: 3, Levels: levels},
	}
	second := TapeFrame{
		Candles: []Candle{{OpenTime: 3, CloseTime: 3, Symbol: SOL, Close: "100"}, {OpenTime: 4, CloseTime: 4, Symbol: SOL, Close: "101"}},
		Context: sampledContext(SOL, "101"),
		Book:    L2Book{Symbol: SOL, Time: 6, Levels: levels},
		Funding: []Funding{{Symbol: SOL, Rate: "-0.0000187103", Premium: "0", Time: 5}},
	}
	replay, err := ReplayTape(config, []TapeFrame{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if replay.State.Position == nil || replay.State.Position.Quantity != 250_000_000 {
		t.Fatalf("balanced 25%% position = %+v", replay.State.Position)
	}
	if replay.State.FundingPnLMicros <= 0 {
		t.Fatalf("negative funding should credit a long, got %d", replay.State.FundingPnLMicros)
	}

	retroactive := second
	retroactive.Funding[0].Time = 1
	if _, err := ReplayTape(config, []TapeFrame{first, retroactive}); err == nil || !strings.Contains(err.Error(), "noncausal") {
		t.Fatalf("retroactive funding error = %v", err)
	}
}

func TestReplayTapeMarksWithVenueContextInsteadOfCandleClose(t *testing.T) {
	config := ReplayConfig{StartingCollateralMicros: 100_000_000, Symbol: SOL, RiskArm: Balanced, VenueMaxLeverage: 20, VenueSzDecimals: 2}
	first := TapeFrame{
		Candles: []Candle{{OpenTime: 1, CloseTime: 1, Symbol: SOL, Close: "101"}, {OpenTime: 2, CloseTime: 2, Symbol: SOL, Close: "100"}},
		Context: sampledContext(SOL, "100"),
		Book:    L2Book{Symbol: SOL, Time: 3, Levels: [2][]Level{{{Price: "99", Size: "2", Count: 1}}, {{Price: "100", Size: "2", Count: 1}}}},
	}
	adverseMark := TapeFrame{
		Candles: []Candle{{OpenTime: 3, CloseTime: 3, Symbol: SOL, Close: "100"}, {OpenTime: 4, CloseTime: 4, Symbol: SOL, Close: "99"}},
		Context: sampledContext(SOL, "1000"),
		Book:    L2Book{Symbol: SOL, Time: 5, Levels: first.Book.Levels},
	}
	replay, err := ReplayTape(config, []TapeFrame{first, adverseMark})
	if err != nil {
		t.Fatal(err)
	}
	if replay.Results[1].Action != "liquidated" || replay.State.Liquidations != 1 {
		t.Fatalf("adverse mark did not liquidate: %+v", replay)
	}

	extremeClose := adverseMark
	extremeClose.Candles[1].Close = "1000"
	extremeClose.Context = sampledContext(SOL, "100")
	replay, err = ReplayTape(config, []TapeFrame{first, extremeClose})
	if err != nil {
		t.Fatal(err)
	}
	if replay.Results[1].Action == "liquidated" || replay.State.Liquidations != 0 {
		t.Fatalf("candle close was incorrectly used as mark: %+v", replay)
	}
}

func TestReplayTapeFundingUsesThePreviousSampledOracle(t *testing.T) {
	config := ReplayConfig{StartingCollateralMicros: 100_000_000, Symbol: SOL, RiskArm: Balanced, VenueMaxLeverage: 20, VenueSzDecimals: 2}
	first := TapeFrame{
		Candles: []Candle{{OpenTime: 1, CloseTime: 1, Symbol: SOL, Close: "99"}, {OpenTime: 2, CloseTime: 2, Symbol: SOL, Close: "100"}},
		Context: PriceContext{Symbol: SOL, MarkPx: "100", OraclePx: "200", ReceivedAt: 2},
		Book:    L2Book{Symbol: SOL, Time: 3, Levels: [2][]Level{{{Price: "99", Size: "2", Count: 1}}, {{Price: "100", Size: "2", Count: 1}}}},
	}
	second := TapeFrame{
		Candles: []Candle{{OpenTime: 3, CloseTime: 3, Symbol: SOL, Close: "100"}, {OpenTime: 4, CloseTime: 4, Symbol: SOL, Close: "101"}},
		Context: PriceContext{Symbol: SOL, MarkPx: "100", OraclePx: "100", ReceivedAt: 5},
		Book:    L2Book{Symbol: SOL, Time: 6, Levels: first.Book.Levels},
		Funding: []Funding{{Symbol: SOL, Rate: "0.0001", Premium: "0", Time: 5}},
	}
	replay, err := ReplayTape(config, []TapeFrame{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if replay.State.FundingPnLMicros != -5_000 {
		t.Fatalf("funding from prior $200 oracle = %d, want -5000", replay.State.FundingPnLMicros)
	}
	second.Context.OraclePx = "1000"
	changedCurrent, err := ReplayTape(config, []TapeFrame{first, second})
	if err != nil || changedCurrent.State.FundingPnLMicros != replay.State.FundingPnLMicros {
		t.Fatalf("current oracle rewrote prior funding: %d, %v", changedCurrent.State.FundingPnLMicros, err)
	}
}

func TestReplayTapeRejectsMissingOrInvalidPriceContext(t *testing.T) {
	config := ReplayConfig{StartingCollateralMicros: 100_000_000, Symbol: SOL, RiskArm: Balanced, VenueMaxLeverage: 20, VenueSzDecimals: 2}
	frame := TapeFrame{
		Candles: []Candle{{OpenTime: 1, CloseTime: 1, Symbol: SOL, Close: "99"}, {OpenTime: 2, CloseTime: 2, Symbol: SOL, Close: "100"}},
		Book:    L2Book{Symbol: SOL, Time: 3, Levels: [2][]Level{{{Price: "99", Size: "2", Count: 1}}, {{Price: "100", Size: "2", Count: 1}}}},
	}
	for _, context := range []PriceContext{
		{},
		{Symbol: BTC, MarkPx: "100", OraclePx: "100", ReceivedAt: 2},
		{Symbol: SOL, MarkPx: "0", OraclePx: "100", ReceivedAt: 2},
		{Symbol: SOL, MarkPx: "100", OraclePx: "-1", ReceivedAt: 2},
		{Symbol: SOL, MarkPx: "100.0000001", OraclePx: "100", ReceivedAt: 2},
	} {
		frame.Context = context
		if _, err := ReplayTape(config, []TapeFrame{frame}); err == nil || !strings.Contains(err.Error(), "price context") {
			t.Fatalf("invalid price context accepted: %+v, %v", context, err)
		}
	}
}

func TestReplayTapeRejectsInvalidPriceContextTiming(t *testing.T) {
	config := ReplayConfig{StartingCollateralMicros: 100_000_000, Symbol: SOL, RiskArm: Balanced, VenueMaxLeverage: 20, VenueSzDecimals: 2}
	first := TapeFrame{
		Candles: []Candle{{OpenTime: 1, CloseTime: 1, Symbol: SOL, Close: "99"}, {OpenTime: 2, CloseTime: 2, Symbol: SOL, Close: "100"}},
		Context: PriceContext{Symbol: SOL, MarkPx: "100", OraclePx: "100", ReceivedAt: 3},
		Book:    L2Book{Symbol: SOL, Time: 3, Levels: [2][]Level{{{Price: "99", Size: "2", Count: 1}}, {{Price: "100", Size: "2", Count: 1}}}},
	}
	second := TapeFrame{
		Candles: []Candle{{OpenTime: 3, CloseTime: 3, Symbol: SOL, Close: "100"}, {OpenTime: 4, CloseTime: 4, Symbol: SOL, Close: "101"}},
		Context: PriceContext{Symbol: SOL, MarkPx: "101", OraclePx: "101", ReceivedAt: 4},
		Book:    L2Book{Symbol: SOL, Time: 5, Levels: first.Book.Levels},
	}
	for name, mutate := range map[string]func(*TapeFrame){
		"missing":  func(frame *TapeFrame) { frame.Context.ReceivedAt = 0 },
		"backward": func(frame *TapeFrame) { frame.Context.ReceivedAt = 2 },
		"separated": func(frame *TapeFrame) {
			frame.Context.ReceivedAt = frame.Book.Time + maxContextBookSeparationMillis + 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := second
			mutate(&candidate)
			if _, err := ReplayTape(config, []TapeFrame{first, candidate}); err == nil || !strings.Contains(err.Error(), "context time") {
				t.Fatalf("invalid timing error = %v", err)
			}
		})
	}
}

func TestReplayTapeRefusesFundingWithoutAnIndividualCausalMark(t *testing.T) {
	config := ReplayConfig{StartingCollateralMicros: 100_000_000, Symbol: SOL, RiskArm: Balanced, VenueMaxLeverage: 20, VenueSzDecimals: 2}
	frame := TapeFrame{
		Candles: []Candle{{OpenTime: 1, CloseTime: 1, Symbol: SOL, Close: "99"}, {OpenTime: 2, CloseTime: 2, Symbol: SOL, Close: "100"}},
		Context: sampledContext(SOL, "100"),
		Book:    L2Book{Symbol: SOL, Time: 5, Levels: [2][]Level{{{Price: "99", Size: "2", Count: 1}}, {{Price: "100", Size: "2", Count: 1}}}},
		Funding: []Funding{{Symbol: SOL, Rate: "0.01", Premium: "0", Time: 3}, {Symbol: SOL, Rate: "0.01", Premium: "0", Time: 4}},
	}
	if _, err := ReplayTape(config, []TapeFrame{frame}); err == nil || !strings.Contains(err.Error(), "individual marks") {
		t.Fatalf("multiple funding settlements error = %v", err)
	}
	frame.Funding = frame.Funding[:1]
	frame.Funding[0].Time = frame.Candles[len(frame.Candles)-1].CloseTime
	if _, err := ReplayTape(config, []TapeFrame{frame}); err == nil || !strings.Contains(err.Error(), "noncausal") {
		t.Fatalf("pre-mark funding error = %v", err)
	}
}

func TestReplayTapeCapsActualEntryNotionalAtArmAllocation(t *testing.T) {
	config := ReplayConfig{StartingCollateralMicros: 100_000_000, Symbol: SOL, RiskArm: Balanced, VenueMaxLeverage: 20, VenueSzDecimals: 2}
	frame := TapeFrame{
		Candles: []Candle{{OpenTime: 1, CloseTime: 1, Symbol: SOL, Close: "99"}, {OpenTime: 2, CloseTime: 2, Symbol: SOL, Close: "100"}},
		Context: sampledContext(SOL, "100"),
		Book: L2Book{Symbol: SOL, Time: 3, Levels: [2][]Level{
			{{Price: "299", Size: "2", Count: 1}},
			{{Price: "300", Size: "2", Count: 1}},
		}},
	}
	replay, err := ReplayTape(config, []TapeFrame{frame})
	if err != nil {
		t.Fatal(err)
	}
	if replay.State.Position == nil || replay.State.Position.Quantity != 80_000_000 || replay.State.Position.EntryNotionalMicros > 25_000_000 {
		t.Fatalf("balanced entry exceeded its 25%% allocation: %+v", replay.State.Position)
	}

	frame.Book.Levels[1] = []Level{{Price: "300", Size: "0.01", Count: 1}, {Price: "1000", Size: "2", Count: 1}}
	replay, err = ReplayTape(config, []TapeFrame{frame})
	if err != nil {
		t.Fatal(err)
	}
	if replay.State.Position != nil || replay.Results[0].Action != "slippage_limit" {
		t.Fatalf("multi-level slippage bypassed the allocation: %+v", replay)
	}
}

func TestReplayTapeLiquidatesBeforeAnOppositeDecision(t *testing.T) {
	config := ReplayConfig{StartingCollateralMicros: 100_000_000, Symbol: SOL, RiskArm: Balanced, VenueMaxLeverage: 20, VenueSzDecimals: 2}
	frames := []TapeFrame{
		{
			Candles: []Candle{{OpenTime: 1, CloseTime: 1, Symbol: SOL, Close: "101"}, {OpenTime: 2, CloseTime: 2, Symbol: SOL, Close: "100"}},
			Context: sampledContext(SOL, "100"),
			Book:    L2Book{Symbol: SOL, Time: 3, Levels: [2][]Level{{{Price: "100", Size: "2", Count: 1}}, {{Price: "101", Size: "2", Count: 1}}}},
		},
		{
			Candles: []Candle{{OpenTime: 3, CloseTime: 3, Symbol: SOL, Close: "100"}, {OpenTime: 4, CloseTime: 4, Symbol: SOL, Close: "500"}},
			Context: sampledContext(SOL, "500"),
			Book:    L2Book{Symbol: SOL, Time: 5, Levels: [2][]Level{{{Price: "499", Size: "2", Count: 1}}, {{Price: "500", Size: "2", Count: 1}}}},
		},
	}
	replay, err := ReplayTape(config, frames)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Results[1].Action != "liquidated" || replay.Results[1].Fill != nil || replay.State.Position != nil || replay.State.Liquidations != 1 || replay.State.LastCloseReason != "liquidation" {
		t.Fatalf("adverse reversal bypassed liquidation: %+v", replay)
	}
}

func TestReplayTapeDoesNotReportUnbookedPartialCloseAsFill(t *testing.T) {
	config := ReplayConfig{StartingCollateralMicros: 100_000_000, Symbol: SOL, RiskArm: Balanced, VenueMaxLeverage: 20, VenueSzDecimals: 2}
	frames := []TapeFrame{
		{
			Candles: []Candle{{OpenTime: 1, CloseTime: 1, Symbol: SOL, Close: "99"}, {OpenTime: 2, CloseTime: 2, Symbol: SOL, Close: "100"}},
			Context: sampledContext(SOL, "100"),
			Book:    L2Book{Symbol: SOL, Time: 3, Levels: [2][]Level{{{Price: "99", Size: "2", Count: 1}}, {{Price: "100", Size: "2", Count: 1}}}},
		},
		{
			Candles: []Candle{{OpenTime: 3, CloseTime: 3, Symbol: SOL, Close: "100"}, {OpenTime: 4, CloseTime: 4, Symbol: SOL, Close: "100"}},
			Context: sampledContext(SOL, "100"),
			Book:    L2Book{Symbol: SOL, Time: 5, Levels: [2][]Level{{{Price: "99", Size: "0.1", Count: 1}}, {{Price: "100", Size: "2", Count: 1}}}},
		},
	}
	replay, err := ReplayTape(config, frames)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Results[1].Action != "waiting_for_full_close" || replay.Results[1].Fill != nil || replay.Results[1].VisibleQuote == nil || replay.State.Position == nil || replay.State.Position.Quantity != 250_000_000 {
		t.Fatalf("partial close was reported as booked: %+v", replay)
	}
}

func TestPerpsMarketValuesRespectVenuePriceRules(t *testing.T) {
	if _, err := Decide(SOL, Balanced, []Candle{{OpenTime: 1, CloseTime: 1, Symbol: SOL, Close: "100"}, {OpenTime: 2, CloseTime: 2, Symbol: SOL, Close: "0"}}); err == nil {
		t.Fatal("zero final close was accepted")
	}
	book := L2Book{Symbol: SOL, Time: 3, Levels: [2][]Level{{{Price: "99", Size: "1", Count: 1}}, {{Price: "100.0001", Size: "1", Count: 1}}}}
	if _, err := WalkBook(SOL, Long, 1_000_000_000, 2, book); err == nil {
		t.Fatal("venue-impossible price precision was accepted")
	}
	if !validHyperliquidPrice("1234.5", 2) || !validHyperliquidPrice("123456", 2) || validHyperliquidPrice("1234.56", 2) {
		t.Fatal("Hyperliquid five-significant-figure rule is wrong")
	}
}

func TestReplayValidatesVenueBookEvenWithoutAnOrder(t *testing.T) {
	config := ReplayConfig{StartingCollateralMicros: 100_000_000, Symbol: SOL, RiskArm: Balanced, VenueMaxLeverage: 20, VenueSzDecimals: 2}
	frame := TapeFrame{
		Candles: []Candle{{OpenTime: 1, CloseTime: 1, Symbol: SOL, Close: "100"}, {OpenTime: 2, CloseTime: 2, Symbol: SOL, Close: "100"}},
		Context: sampledContext(SOL, "100"),
		Book:    L2Book{Symbol: SOL, Time: 3, Levels: [2][]Level{{{Price: "99", Size: "1", Count: 1}}, {{Price: "100.0001", Size: "1", Count: 1}}}},
	}
	if _, err := ReplayTape(config, []TapeFrame{frame}); err == nil || !strings.Contains(err.Error(), "tick precision") {
		t.Fatalf("flat invalid-tick frame error = %v", err)
	}
	frame.Book.Levels[1][0] = Level{Price: "100", Size: "0.001", Count: 1}
	if _, err := ReplayTape(config, []TapeFrame{frame}); err == nil || !strings.Contains(err.Error(), "lot precision") {
		t.Fatalf("flat invalid-lot frame error = %v", err)
	}
}

func sampledContext(symbol Symbol, price string) PriceContext {
	return PriceContext{Symbol: symbol, MarkPx: price, OraclePx: price, ReceivedAt: 1}
}
