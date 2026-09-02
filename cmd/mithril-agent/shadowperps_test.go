package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

type stubShadowPerpsReader struct {
	asset       perpspaper.AssetMeta
	context     perpspaper.AssetContext
	candles     []perpspaper.Candle
	book        perpspaper.L2Book
	funding     []perpspaper.Funding
	fundingFrom int64
	fundingTo   int64
	candleCalls int
	bookCalls   int
	metaCalls   int
	calls       []string
}

func (reader *stubShadowPerpsReader) MetaAndAssetContexts(context.Context) (perpspaper.MetaAndAssetContexts, error) {
	reader.metaCalls++
	reader.calls = append(reader.calls, "context")
	return perpspaper.MetaAndAssetContexts{
		Universe: []perpspaper.AssetMeta{reader.asset}, Contexts: []perpspaper.AssetContext{reader.context},
	}, nil
}

func (reader *stubShadowPerpsReader) Candles(context.Context, perpspaper.Symbol, string, int64, int64) ([]perpspaper.Candle, error) {
	reader.candleCalls++
	reader.calls = append(reader.calls, "candles")
	return append([]perpspaper.Candle(nil), reader.candles...), nil
}

func (reader *stubShadowPerpsReader) Book(context.Context, perpspaper.Symbol) (perpspaper.L2Book, error) {
	reader.bookCalls++
	reader.calls = append(reader.calls, "book")
	return reader.book, nil
}

func (reader *stubShadowPerpsReader) FundingHistory(_ context.Context, _ perpspaper.Symbol, from, to int64) ([]perpspaper.Funding, error) {
	reader.fundingFrom, reader.fundingTo = from, to
	reader.calls = append(reader.calls, "funding")
	return append([]perpspaper.Funding(nil), reader.funding...), nil
}

func TestShadowPerpsPaperRunPersistsSignerFreePaperState(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 5, 30, 0, time.UTC)
	reader := validStubShadowPerpsReader(now)
	directory := filepath.Join(t.TempDir(), "perps")
	var output bytes.Buffer
	err := runShadowPerpsPaperWith(t.Context(), []string{
		"--state-dir", directory, "--symbols", "SOL", "--arm", "balanced",
		"--paper-usd-per-market", "100", "--once",
	}, &output, func() time.Time { return now }, func(environment perpspaper.Environment) (shadowPerpsReader, error) {
		if environment != perpspaper.Mainnet {
			t.Fatalf("environment = %q", environment)
		}
		return reader, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var status shadowPerpsStatus
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatalf("status output = %q: %v", output.String(), err)
	}
	if !status.PaperOnly || status.ExecutionEnabled || status.AccountingModel != shadowPerpsModel || !status.NewFrame || status.Frames != 1 || status.Symbol != perpspaper.SOL {
		t.Fatalf("status = %+v", status)
	}
	for _, name := range []string{"sol-tape.json", "sol-status.json"} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, %v", name, info.Mode().Perm(), err)
		}
	}
	var tape shadowPerpsTape
	if err := readStrictJSON(filepath.Join(directory, "sol-tape.json"), &tape); err != nil {
		t.Fatal(err)
	}
	if len(tape.Frames) != 1 || !tape.PaperOnly || tape.ExecutionEnabled || tape.AccountingModel != shadowPerpsModel || tape.Config.RiskArm != perpspaper.Balanced {
		t.Fatalf("tape = %+v", tape)
	}
	if tape.Frames[0].Context.Symbol != perpspaper.SOL || tape.Frames[0].Context.MarkPx != "100" || tape.Frames[0].Context.OraclePx != "100" || tape.Frames[0].Context.ReceivedAt != now.UnixMilli() {
		t.Fatalf("stored price context = %+v", tape.Frames[0].Context)
	}
	if got := strings.Join(reader.calls, ","); got != "context,candles,context,book" {
		t.Fatalf("market data call order = %s", got)
	}
	if strings.Contains(output.String(), "wallet") || strings.Contains(output.String(), "signer") || strings.Contains(output.String(), "exchange") {
		t.Fatalf("runtime output implies an execution path: %q", output.String())
	}
}

func TestShadowPerpsPaperRunResumesOnlyNewCausalFrames(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 5, 30, 0, time.UTC)
	reader := validStubShadowPerpsReader(now)
	directory := filepath.Join(t.TempDir(), "perps")
	factory := func(perpspaper.Environment) (shadowPerpsReader, error) { return reader, nil }
	args := []string{"--state-dir", directory, "--symbols", "SOL", "--once"}
	if err := runShadowPerpsPaperWith(t.Context(), args, &bytes.Buffer{}, func() time.Time { return now }, factory); err != nil {
		t.Fatal(err)
	}
	firstCandleCalls, firstBookCalls := reader.candleCalls, reader.bookCalls
	var unchanged bytes.Buffer
	if err := runShadowPerpsPaperWith(t.Context(), args, &unchanged, func() time.Time { return now }, factory); err != nil {
		t.Fatal(err)
	}
	var unchangedStatus shadowPerpsStatus
	if err := json.Unmarshal(unchanged.Bytes(), &unchangedStatus); err != nil || unchangedStatus.NewFrame || unchangedStatus.Frames != 1 {
		t.Fatalf("unchanged status = %+v, %v", unchangedStatus, err)
	}
	if reader.candleCalls != firstCandleCalls || reader.bookCalls != firstBookCalls {
		t.Fatalf("unchanged minute made remote market calls: candles %d->%d book %d->%d", firstCandleCalls, reader.candleCalls, firstBookCalls, reader.bookCalls)
	}

	previousBook := reader.book.Time
	now = now.Add(time.Minute)
	reader.candles = paperCandles(now, "100", "101")
	reader.book.Time = now.UnixMilli()
	reader.funding = []perpspaper.Funding{{Symbol: perpspaper.SOL, Rate: "-0.00001", Premium: "0", Time: reader.candles[len(reader.candles)-1].CloseTime + 1}}
	var advanced bytes.Buffer
	if err := runShadowPerpsPaperWith(t.Context(), args, &advanced, func() time.Time { return now }, factory); err != nil {
		t.Fatal(err)
	}
	var advancedStatus shadowPerpsStatus
	if err := json.Unmarshal(advanced.Bytes(), &advancedStatus); err != nil || !advancedStatus.NewFrame || advancedStatus.Frames != 2 {
		t.Fatalf("advanced status = %+v, %v", advancedStatus, err)
	}
	if reader.fundingFrom != previousBook+1 || reader.fundingTo != reader.book.Time {
		t.Fatalf("funding range = %d..%d", reader.fundingFrom, reader.fundingTo)
	}
}

func TestShadowPerpsPaperRunRefusesMissedMinutes(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 5, 30, 0, time.UTC)
	reader := validStubShadowPerpsReader(now)
	directory := filepath.Join(t.TempDir(), "perps")
	factory := func(perpspaper.Environment) (shadowPerpsReader, error) { return reader, nil }
	args := []string{"--state-dir", directory, "--symbols", "SOL", "--once"}
	if err := runShadowPerpsPaperWith(t.Context(), args, &bytes.Buffer{}, func() time.Time { return now }, factory); err != nil {
		t.Fatal(err)
	}
	calls := reader.candleCalls
	now = now.Add(3 * time.Minute)
	err := runShadowPerpsPaperWith(t.Context(), args, &bytes.Buffer{}, func() time.Time { return now }, factory)
	if err == nil || !strings.Contains(err.Error(), "missed a closed minute") {
		t.Fatalf("missed-minute error = %v", err)
	}
	if reader.candleCalls != calls {
		t.Fatal("missed-minute refusal queried and collapsed newer data")
	}
}

func TestShadowPerpsPaperRunRefusesAStaleFirstSnapshot(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 5, 30, 0, time.UTC)
	reader := validStubShadowPerpsReader(now.Add(-time.Minute))
	directory := filepath.Join(t.TempDir(), "perps")
	err := runShadowPerpsPaperWith(t.Context(), []string{"--state-dir", directory, "--symbols", "SOL", "--once"}, &bytes.Buffer{}, func() time.Time { return now }, func(perpspaper.Environment) (shadowPerpsReader, error) {
		return reader, nil
	})
	if err == nil || !strings.Contains(err.Error(), "latest completed") {
		t.Fatalf("stale first-snapshot error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(directory, "sol-tape.json")); !os.IsNotExist(statErr) {
		t.Fatalf("stale first snapshot wrote a tape: %v", statErr)
	}
}

func TestShadowPerpsPaperRunRejectsAmbiguousOrChangedIdentity(t *testing.T) {
	for _, args := range [][]string{
		{"--state-dir", "relative", "--once"},
		{"--state-dir", filepath.Join(t.TempDir(), "state"), "--symbols", "SOL,SOL", "--once"},
		{"--state-dir", filepath.Join(t.TempDir(), "state"), "--arm", "reckless", "--once"},
	} {
		if err := runShadowPerpsPaperWith(t.Context(), args, &bytes.Buffer{}, time.Now, func(perpspaper.Environment) (shadowPerpsReader, error) {
			t.Fatal("reader was created for invalid arguments")
			return nil, nil
		}); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}

	now := time.Date(2026, 9, 2, 12, 5, 30, 0, time.UTC)
	reader := validStubShadowPerpsReader(now)
	directory := filepath.Join(t.TempDir(), "state")
	factory := func(perpspaper.Environment) (shadowPerpsReader, error) { return reader, nil }
	if err := runShadowPerpsPaperWith(t.Context(), []string{"--state-dir", directory, "--symbols", "SOL", "--arm", "balanced", "--once"}, &bytes.Buffer{}, func() time.Time { return now }, factory); err != nil {
		t.Fatal(err)
	}
	if err := runShadowPerpsPaperWith(t.Context(), []string{"--state-dir", directory, "--symbols", "SOL", "--arm", "experimental", "--once"}, &bytes.Buffer{}, func() time.Time { return now }, factory); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("changed arm error = %v", err)
	}
}

func TestShadowPerpsPaperRunRefusesLegacyCandleValuationTape(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := shadowPerpsTapeConfig{
		Environment: perpspaper.Mainnet, Symbol: perpspaper.SOL, RiskArm: perpspaper.Balanced,
		StartingCollateralMicros: 100_000_000, VenueMaxLeverage: 20, VenueSzDecimals: 2,
	}
	for _, version := range []uint32{1, 2} {
		legacy := shadowPerpsTape{Version: version, PaperOnly: true, AccountingModel: "stress_scenario", Config: config}
		raw, err := json.Marshal(legacy)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "sol-tape.json")
		if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := readShadowPerpsTape(path, config); err == nil || !strings.Contains(err.Error(), "cannot be migrated") {
			t.Fatalf("legacy tape v%d error = %v", version, err)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("legacy tape v%d changed: %v", version, err)
		}
	}
}

func TestShadowPerpsPaperRunRejectsFutureBookBeforePersistingIt(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 5, 30, 0, time.UTC)
	reader := validStubShadowPerpsReader(now)
	reader.book.Time = now.Add(24 * time.Hour).UnixMilli()
	directory := filepath.Join(t.TempDir(), "state")
	err := runShadowPerpsPaperWith(
		t.Context(),
		[]string{"--state-dir", directory, "--symbols", "SOL", "--once"},
		&bytes.Buffer{}, func() time.Time { return now },
		func(perpspaper.Environment) (shadowPerpsReader, error) { return reader, nil },
	)
	if err == nil || !strings.Contains(err.Error(), "ahead of the local clock") {
		t.Fatalf("future book error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(directory, "sol-tape.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("future book was persisted: %v", statErr)
	}
}

func TestShadowPerpsPaperRunRejectsStaleBookBeforePersistingIt(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 5, 30, 0, time.UTC)
	reader := validStubShadowPerpsReader(now)
	reader.book.Time = now.Add(-shadowPerpsMaxBookAge - time.Millisecond).UnixMilli()
	directory := filepath.Join(t.TempDir(), "state")
	err := runShadowPerpsPaperWith(
		t.Context(),
		[]string{"--state-dir", directory, "--symbols", "SOL", "--once"},
		&bytes.Buffer{}, func() time.Time { return now },
		func(perpspaper.Environment) (shadowPerpsReader, error) { return reader, nil },
	)
	if err == nil || !strings.Contains(err.Error(), "book is stale") {
		t.Fatalf("stale book error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(directory, "sol-tape.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stale book was persisted: %v", statErr)
	}
}

func validStubShadowPerpsReader(now time.Time) *stubShadowPerpsReader {
	return &stubShadowPerpsReader{
		asset:   perpspaper.AssetMeta{Name: perpspaper.SOL, SzDecimals: 2, MaxLeverage: 20, MarginTableID: 1},
		context: perpspaper.AssetContext{Funding: "0", OpenInterest: "1", PrevDayPx: "99", DayNtlVlm: "1", DayBaseVlm: "1", OraclePx: "100", MarkPx: "100"},
		candles: paperCandles(now, "99", "100"),
		book: perpspaper.L2Book{Symbol: perpspaper.SOL, Time: now.UnixMilli(), Levels: [2][]perpspaper.Level{
			{{Price: "99", Size: "10", Count: 1}},
			{{Price: "100", Size: "10", Count: 1}},
		}},
	}
}

func paperCandles(now time.Time, first, second string) []perpspaper.Candle {
	end := now.Truncate(time.Minute).Add(-time.Millisecond)
	return []perpspaper.Candle{
		{OpenTime: end.Add(-2 * time.Minute).Add(time.Millisecond).UnixMilli(), CloseTime: end.Add(-time.Minute).UnixMilli(), Symbol: perpspaper.SOL, Interval: "1m", Open: first, Close: first, High: first, Low: first, Volume: "1"},
		{OpenTime: end.Add(-time.Minute).Add(time.Millisecond).UnixMilli(), CloseTime: end.UnixMilli(), Symbol: perpspaper.SOL, Interval: "1m", Open: second, Close: second, High: second, Low: second, Volume: "1"},
	}
}
