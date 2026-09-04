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

	"github.com/Overclock-Validator/mithril-agent/paperstatus"
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
	candleFrom  int64
	candleTo    int64
	candleCalls int
	bookCalls   int
	metaCalls   int
	calls       []string
	metaErr     error
	bookErr     error
}

func (reader *stubShadowPerpsReader) MetaAndAssetContexts(context.Context) (perpspaper.MetaAndAssetContexts, error) {
	reader.metaCalls++
	reader.calls = append(reader.calls, "context")
	if reader.metaErr != nil {
		return perpspaper.MetaAndAssetContexts{}, reader.metaErr
	}
	return perpspaper.MetaAndAssetContexts{
		Universe: []perpspaper.AssetMeta{reader.asset}, Contexts: []perpspaper.AssetContext{reader.context},
	}, nil
}

func (reader *stubShadowPerpsReader) Candles(_ context.Context, _ perpspaper.Symbol, _ string, from, to int64) ([]perpspaper.Candle, error) {
	reader.candleFrom, reader.candleTo = from, to
	reader.candleCalls++
	reader.calls = append(reader.calls, "candles")
	return append([]perpspaper.Candle(nil), reader.candles...), nil
}

func (reader *stubShadowPerpsReader) Book(context.Context, perpspaper.Symbol) (perpspaper.L2Book, error) {
	reader.bookCalls++
	reader.calls = append(reader.calls, "book")
	if reader.bookErr != nil {
		return perpspaper.L2Book{}, reader.bookErr
	}
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
	for _, name := range []string{"sol-tape.json", "sol-status.json", "sol-paper-status.json"} {
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

func TestShadowPerpsPaperRunUsesQualifiedPlanOnlyOnNextBoundedRun(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 5, 30, 0, time.UTC)
	reader := validStubShadowPerpsReader(now)
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir, archiveDir := filepath.Join(base, "current"), filepath.Join(base, "runs")
	factory := func(perpspaper.Environment) (shadowPerpsReader, error) { return reader, nil }
	args := []string{
		"--state-dir", stateDir, "--archive-dir", archiveDir,
		"--symbols", "SOL", "--arm", "balanced", "--once",
	}
	var first bytes.Buffer
	if err := runShadowPerpsPaperWith(t.Context(), args, &first, func() time.Time { return now }, factory); err != nil {
		t.Fatal(err)
	}
	var firstStatus shadowPerpsStatus
	if json.Unmarshal(first.Bytes(), &firstStatus) != nil ||
		firstStatus.DecisionMode != shadowPerpsDecisionLegacy || firstStatus.Strategy != "" {
		t.Fatalf("first status = %+v", firstStatus)
	}
	var firstTape shadowPerpsTape
	if err := readStrictJSON(filepath.Join(stateDir, "sol-tape.json"), &firstTape); err != nil {
		t.Fatal(err)
	}
	qualification := qualifiedShadowPerpsWalkForward(t, stateDir, firstTape.Config.qualificationConfig(), 3)
	receipt, err := selectQualifiedShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, firstTape.Config.PlanSHA256, qualification, now.Add(10*time.Second),
	)
	if err != nil || !receipt.PointerUpdated {
		t.Fatalf("select next plan = %+v, %v", receipt, err)
	}
	// The stored tape remains bound to the plan that produced it.
	stored, _, err := readShadowPerpsTape(filepath.Join(stateDir, "sol-tape.json"), firstTape.Config)
	if err != nil || stored.Config.DecisionMode != shadowPerpsDecisionLegacy {
		t.Fatalf("in-flight tape was reinterpreted: %+v, %v", stored.Config, err)
	}

	now = now.Add(time.Minute)
	reader.candles = paperCandles(now, "100", "101")
	reader.book.Time = now.UnixMilli()
	var next bytes.Buffer
	if err := runShadowPerpsPaperWith(t.Context(), args, &next, func() time.Time { return now }, factory); err != nil {
		t.Fatal(err)
	}
	var nextStatus shadowPerpsStatus
	if json.Unmarshal(next.Bytes(), &nextStatus) != nil ||
		nextStatus.DecisionMode != shadowPerpsDecisionSelected ||
		nextStatus.Strategy != qualification.Candidate.Strategy ||
		nextStatus.RiskArm != qualification.Candidate.RiskArm ||
		nextStatus.PlanSHA256 != receipt.PlanSHA256 {
		t.Fatalf("next status = %+v", nextStatus)
	}
	var nextTape shadowPerpsTape
	if err := readStrictJSON(filepath.Join(stateDir, "sol-tape.json"), &nextTape); err != nil {
		t.Fatal(err)
	}
	if nextTape.Version != shadowPerpsTapeVersion ||
		nextTape.Config.QualificationInputSHA256 != qualification.InputSHA256 {
		t.Fatalf("next tape = %+v", nextTape)
	}
}

func TestShadowPerpsPaperRunWaitsForCandleSettlement(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 5, 30, 0, time.UTC)
	reader := validStubShadowPerpsReader(now)
	directory := filepath.Join(t.TempDir(), "perps")
	if err := runShadowPerpsPaperWith(t.Context(), []string{
		"--state-dir", directory, "--symbols", "SOL", "--once",
	}, &bytes.Buffer{}, func() time.Time { return now }, func(perpspaper.Environment) (shadowPerpsReader, error) {
		return reader, nil
	}); err != nil {
		t.Fatal(err)
	}
	var tape shadowPerpsTape
	if err := readStrictJSON(filepath.Join(directory, "sol-tape.json"), &tape); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 2, 12, 2, 59, 999_000_000, time.UTC).UnixMilli()
	if got := tape.Frames[0].Candles[len(tape.Frames[0].Candles)-1].CloseTime; got != want || now.UnixMilli()-got < int64(shadowPerpsCandleSettleLag/time.Millisecond) {
		t.Fatalf("recorded candle close = %d, want %d with settlement lag", got, want)
	}
	if reader.candleTo != want {
		t.Fatalf("candle request ended at %d, want settled boundary %d", reader.candleTo, want)
	}
}

func TestShadowPerpsPaperRunRejectsChangedSettledCandleImmediately(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 5, 30, 0, time.UTC)
	reader := validStubShadowPerpsReader(now)
	directory := filepath.Join(t.TempDir(), "perps")
	factory := func(perpspaper.Environment) (shadowPerpsReader, error) { return reader, nil }
	args := []string{"--state-dir", directory, "--symbols", "SOL", "--once"}
	if err := runShadowPerpsPaperWith(t.Context(), args, &bytes.Buffer{}, func() time.Time { return now }, factory); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	reader.candles = paperCandles(now, "100", "101")
	reader.candles[0].Close = "100.01"
	reader.book.Time = now.UnixMilli()
	if err := runShadowPerpsPaperWith(t.Context(), args, &bytes.Buffer{}, func() time.Time { return now }, factory); err == nil || !strings.Contains(err.Error(), "changed a previously observed settled candle") {
		t.Fatalf("changed settled candle error = %v", err)
	}
	var tape shadowPerpsTape
	if err := readStrictJSON(filepath.Join(directory, "sol-tape.json"), &tape); err != nil || len(tape.Frames) != 1 {
		t.Fatalf("changed candle persisted: %d frames, %v", len(tape.Frames), err)
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
	reader.funding = []perpspaper.Funding{{Symbol: perpspaper.SOL, Rate: "-0.00001", Premium: "0", Time: previousBook + 1}}
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

func TestShadowPerpsPaperRunPersistsQualificationAndRotatesPreviousRun(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 5, 30, 0, time.UTC)
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir, archiveDir := filepath.Join(base, "current"), filepath.Join(base, "runs")
	if err := runShadowPerpsPaperWith(
		t.Context(),
		[]string{"--state-dir", stateDir, "--archive-dir", archiveDir, "--symbols", "SOL", "--once"},
		&bytes.Buffer{}, func() time.Time { return now },
		func(perpspaper.Environment) (shadowPerpsReader, error) { return validStubShadowPerpsReader(now), nil },
	); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, "sol-qualification.json"))
	if err != nil {
		t.Fatal(err)
	}
	var qualification perpspaper.Qualification
	if err := json.Unmarshal(raw, &qualification); err != nil || qualification.Outcome != "insufficient_evidence" || qualification.Frames != 1 {
		t.Fatalf("qualification = %+v, %v", qualification, err)
	}
	raw, err = os.ReadFile(filepath.Join(stateDir, "sol-paper-status.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || paperstatus.ValidateSnapshot(snapshot) != nil ||
		snapshot.Summary == nil || !snapshot.Summary.QualificationTracked ||
		snapshot.Summary.QualificationOutcome != "insufficient_evidence" ||
		len(snapshot.Events) == 0 || snapshot.Events[len(snapshot.Events)-1].Kind != paperstatus.KindExperimentDone {
		t.Fatalf("final snapshot = %+v, %v", snapshot, err)
	}
	publishedPath := filepath.Join(shadowPerpsPublishedDir(stateDir), "sol-paper-status.json")
	published, err := os.ReadFile(publishedPath)
	if err != nil || !bytes.Equal(published, raw) {
		t.Fatalf("published checkpoint = %q, %v", published, err)
	}

	later := now.Add(time.Minute)
	if err := prepareShadowPerpsRun(stateDir, archiveDir, later); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("new current run = %v, %v", entries, err)
	}
	archived := filepath.Join(archiveDir, later.Format("20060102T150405.000000000Z"))
	if _, err := os.Stat(filepath.Join(archived, "sol-tape.json")); err != nil {
		t.Fatalf("archived tape: %v", err)
	}
}

func TestPublishShadowPerpsCarriesTheLastCompletedReceiptIntoALiveRun(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateDir, publishedDir := filepath.Join(base, "current"), filepath.Join(base, "published")
	for _, directory := range []string{stateDir, publishedDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	completedAt := time.Date(2026, 9, 4, 18, 0, 0, 0, time.UTC)
	completed := shadowPerpsTestSummary(completedAt, true)
	previous := paperstatus.Snapshot{
		Version: paperstatus.Version - 1, ObservedAt: completedAt,
		Current: "PAPER · Completed", Summary: &completed,
		Events: []paperstatus.Event{{
			ID: strings.Repeat("a", 64), At: completedAt,
			Kind: paperstatus.KindExperimentDone, Message: "PAPER · Completed",
		}},
	}
	writeShadowPerpsTestSnapshot(t, filepath.Join(publishedDir, "sol-paper-status.json"), previous)

	liveAt := completedAt.Add(time.Minute)
	live := shadowPerpsTestSummary(liveAt, false)
	live.Checks = 7
	current := paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: liveAt,
		Current: "PAPER · Recording", Summary: &live,
	}
	writeShadowPerpsTestSnapshot(t, filepath.Join(stateDir, "sol-paper-status.json"), current)
	if err := publishShadowPerpsStatuses(stateDir, publishedDir, []perpspaper.Symbol{perpspaper.SOL}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(publishedDir, "sol-paper-status.json"))
	if err != nil {
		t.Fatal(err)
	}
	var published paperstatus.Snapshot
	if err := json.Unmarshal(raw, &published); err != nil || paperstatus.ValidateSnapshot(published) != nil {
		t.Fatalf("published snapshot = %+v, %v", published, err)
	}
	receipt, ok := paperstatus.LatestCompletedSnapshot(published)
	if !ok || published.Summary == nil || published.Summary.Checks != 7 ||
		published.Summary.QualificationTracked || !receipt.ObservedAt.Equal(completedAt) ||
		receipt.Summary.QualificationFrames != completed.QualificationFrames {
		t.Fatalf("published live + completed state = %+v, %+v, %v", published, receipt, ok)
	}
	corrupt := []byte("corrupt previous status\n")
	if err := os.WriteFile(filepath.Join(publishedDir, "sol-paper-status.json"), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishShadowPerpsStatuses(stateDir, publishedDir, []perpspaper.Symbol{perpspaper.SOL}); err == nil {
		t.Fatal("live publication replaced an invalid prior completion source")
	}
	after, err := os.ReadFile(filepath.Join(publishedDir, "sol-paper-status.json"))
	if err != nil || !bytes.Equal(after, corrupt) {
		t.Fatalf("failed carry-forward changed prior status: %q, %v", after, err)
	}
}

func TestPrepareShadowPerpsRunPublishesACompletedReceiptBeforeArchiving(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir, archiveDir := filepath.Join(base, "current"), filepath.Join(base, "runs")
	publishedDir := shadowPerpsPublishedDir(stateDir)
	for _, directory := range []string{stateDir, archiveDir, publishedDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	completedSnapshot := func(at time.Time, eventID string) paperstatus.Snapshot {
		summary := shadowPerpsTestSummary(at, true)
		return paperstatus.Snapshot{
			Version: paperstatus.Version, ObservedAt: at,
			Current: "PAPER · Completed", Summary: &summary,
			Events: []paperstatus.Event{{
				ID: eventID, At: at, Kind: paperstatus.KindExperimentDone,
				Message: "PAPER · Completed",
			}},
			LatestCompleted: &paperstatus.CompletedSnapshot{
				ObservedAt: at, EventID: eventID, Summary: summary,
			},
		}
	}
	previousAt := time.Date(2026, 9, 4, 18, 0, 0, 0, time.UTC)
	completedAt := previousAt.Add(time.Hour)
	writeShadowPerpsTestSnapshot(t, filepath.Join(publishedDir, "sol-paper-status.json"), completedSnapshot(previousAt, strings.Repeat("a", 64)))
	writeShadowPerpsTestSnapshot(t, filepath.Join(stateDir, "sol-paper-status.json"), completedSnapshot(completedAt, strings.Repeat("c", 64)))

	restartedAt := completedAt.Add(time.Minute)
	if err := prepareShadowPerpsRun(stateDir, archiveDir, restartedAt); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(publishedDir, "sol-paper-status.json"))
	if err != nil {
		t.Fatal(err)
	}
	var published paperstatus.Snapshot
	if err := json.Unmarshal(raw, &published); err != nil || paperstatus.ValidateSnapshot(published) != nil {
		t.Fatalf("recovered published snapshot = %+v, %v", published, err)
	}
	receipt, ok := paperstatus.LatestCompletedSnapshot(published)
	if !ok || !receipt.ObservedAt.Equal(completedAt) || receipt.EventID != strings.Repeat("c", 64) {
		t.Fatalf("recovered receipt = %+v, %v", receipt, ok)
	}
	archived := filepath.Join(archiveDir, restartedAt.Format("20060102T150405.000000000Z"))
	if _, err := os.Stat(filepath.Join(archived, "sol-paper-status.json")); err != nil {
		t.Fatalf("completed local status was not archived: %v", err)
	}
}

func TestPrepareShadowPerpsRunRejectsAStatusStoredUnderTheWrongMarket(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir, archiveDir := filepath.Join(base, "current"), filepath.Join(base, "runs")
	publishedDir := shadowPerpsPublishedDir(stateDir)
	for _, directory := range []string{stateDir, archiveDir, publishedDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	completedSnapshot := func(at time.Time, eventID, market string) paperstatus.Snapshot {
		summary := shadowPerpsTestSummary(at, true)
		summary.Market = market
		return paperstatus.Snapshot{
			Version: paperstatus.Version, ObservedAt: at,
			Current: "PAPER · Completed", Summary: &summary,
			Events: []paperstatus.Event{{
				ID: eventID, At: at, Kind: paperstatus.KindExperimentDone,
				Message: "PAPER · Completed",
			}},
			LatestCompleted: &paperstatus.CompletedSnapshot{
				ObservedAt: at, EventID: eventID, Summary: summary,
			},
		}
	}
	previousAt := time.Date(2026, 9, 4, 18, 0, 0, 0, time.UTC)
	previousPath := filepath.Join(publishedDir, "sol-paper-status.json")
	writeShadowPerpsTestSnapshot(t, previousPath, completedSnapshot(previousAt, strings.Repeat("a", 64), "SOL-PERP"))
	previousRaw, err := os.ReadFile(previousPath)
	if err != nil {
		t.Fatal(err)
	}
	writeShadowPerpsTestSnapshot(t, filepath.Join(stateDir, "sol-paper-status.json"),
		completedSnapshot(previousAt.Add(-time.Hour), strings.Repeat("c", 64), "BTC-PERP"))

	if err := prepareShadowPerpsRun(stateDir, archiveDir, previousAt.Add(2*time.Hour)); err == nil {
		t.Fatal("wrong-market status was recovered under the SOL filename")
	}
	after, err := os.ReadFile(previousPath)
	if err != nil || !bytes.Equal(after, previousRaw) {
		t.Fatalf("wrong-market recovery changed the published SOL status: %q, %v", after, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sol-paper-status.json")); err != nil {
		t.Fatalf("wrong-market current status was archived: %v", err)
	}
}

func TestPrepareShadowPerpsRunReplacesAMislabeledPublishedStatus(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir, archiveDir := filepath.Join(base, "current"), filepath.Join(base, "runs")
	publishedDir := shadowPerpsPublishedDir(stateDir)
	for _, directory := range []string{stateDir, archiveDir, publishedDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	completedSnapshot := func(at time.Time, eventID, market string) paperstatus.Snapshot {
		summary := shadowPerpsTestSummary(at, true)
		summary.Market = market
		return paperstatus.Snapshot{
			Version: paperstatus.Version, ObservedAt: at,
			Current: "PAPER · Completed", Summary: &summary,
			Events: []paperstatus.Event{{
				ID: eventID, At: at, Kind: paperstatus.KindExperimentDone,
				Message: "PAPER · Completed",
			}},
			LatestCompleted: &paperstatus.CompletedSnapshot{
				ObservedAt: at, EventID: eventID, Summary: summary,
			},
		}
	}
	correctAt := time.Date(2026, 9, 4, 18, 0, 0, 0, time.UTC)
	publishedPath := filepath.Join(publishedDir, "sol-paper-status.json")
	writeShadowPerpsTestSnapshot(t, publishedPath,
		completedSnapshot(correctAt.Add(time.Hour), strings.Repeat("a", 64), "BTC-PERP"))
	writeShadowPerpsTestSnapshot(t, filepath.Join(stateDir, "sol-paper-status.json"),
		completedSnapshot(correctAt, strings.Repeat("c", 64), "SOL-PERP"))

	if err := prepareShadowPerpsRun(stateDir, archiveDir, correctAt.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(publishedPath)
	if err != nil {
		t.Fatal(err)
	}
	var published paperstatus.Snapshot
	if err := json.Unmarshal(raw, &published); err != nil || paperstatus.ValidateSnapshot(published) != nil {
		t.Fatalf("replacement snapshot = %+v, %v", published, err)
	}
	receipt, ok := paperstatus.LatestCompletedSnapshot(published)
	if !ok || receipt.Summary.Market != "SOL-PERP" || !receipt.ObservedAt.Equal(correctAt) {
		t.Fatalf("replacement receipt = %+v, %v", receipt, ok)
	}
}

func shadowPerpsTestSummary(at time.Time, completed bool) paperstatus.CurrentSummary {
	summary := paperstatus.CurrentSummary{
		Market: "SOL-PERP", Instrument: "perpetual", RiskProfile: "balanced",
		PositionDirection: "flat", LeverageBPS: 20_000, FundingTracked: true,
		ValueUnit: "USD", Day: at.Format("2006-01-02"), TickSeconds: 60,
		OpeningEquityMicros: 100_000_000, EquityMicros: 100_000_000,
		HoldBenchmarkMicros: 100_000_000, State: "watching", Strategy: "fixed",
	}
	if !completed {
		return summary
	}
	summary.Checks = 10
	summary.QualificationTracked = true
	summary.QualificationOutcome = "insufficient_evidence"
	summary.QualificationSHA256 = strings.Repeat("b", 64)
	summary.QualificationTapes = 1
	summary.QualificationFrames = 10
	summary.QualificationMinimumFrames = 24
	return summary
}

func writeShadowPerpsTestSnapshot(t *testing.T, path string, snapshot paperstatus.Snapshot) {
	t.Helper()
	if err := paperstatus.ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestShadowPerpsFailureDoesNotPublishCompletedQualification(t *testing.T) {
	now := time.Date(2026, 9, 3, 13, 5, 30, 0, time.UTC)
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir, archiveDir := filepath.Join(base, "current"), filepath.Join(base, "runs")
	if err := runShadowPerpsPaperWith(t.Context(), []string{
		"--state-dir", stateDir, "--archive-dir", archiveDir, "--symbols", "SOL", "--once",
	}, &bytes.Buffer{}, func() time.Time { return now }, func(perpspaper.Environment) (shadowPerpsReader, error) {
		return validStubShadowPerpsReader(now), nil
	}); err != nil {
		t.Fatal(err)
	}
	publishedPath := filepath.Join(shadowPerpsPublishedDir(stateDir), "sol-paper-status.json")
	before, err := os.ReadFile(publishedPath)
	if err != nil {
		t.Fatal(err)
	}

	failed := validStubShadowPerpsReader(now.Add(time.Minute))
	failed.bookErr = errors.New("book unavailable")
	err = runShadowPerpsPaperWith(t.Context(), []string{
		"--state-dir", stateDir, "--archive-dir", archiveDir, "--symbols", "SOL", "--once",
	}, &bytes.Buffer{}, func() time.Time { return now.Add(time.Minute) }, func(perpspaper.Environment) (shadowPerpsReader, error) {
		return failed, nil
	})
	if err == nil || !strings.Contains(err.Error(), "book unavailable") {
		t.Fatalf("failed run error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sol-qualification.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed run published a qualification: %v", err)
	}
	after, err := os.ReadFile(publishedPath)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("failed run replaced last good checkpoint: %v", err)
	}
	var partial paperstatus.Snapshot
	if err := json.Unmarshal(before, &partial); err != nil {
		t.Fatal(err)
	}
	partial.ObservedAt = now.Add(90 * time.Second)
	partial.Current = "PAPER · partial failed run"
	partialRaw, err := json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "sol-paper-status.json"), partialRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareShadowPerpsRun(stateDir, archiveDir, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	afterRestart, err := os.ReadFile(publishedPath)
	if err != nil || !bytes.Equal(afterRestart, before) {
		t.Fatalf("restart published a partial failed run: %v", err)
	}
}

func TestShadowPerpsMetadataFailureDoesNotRotateCurrentRun(t *testing.T) {
	now := time.Date(2026, 9, 3, 14, 5, 30, 0, time.UTC)
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir, archiveDir := filepath.Join(base, "current"), filepath.Join(base, "runs")
	if err := runShadowPerpsPaperWith(t.Context(), []string{
		"--state-dir", stateDir, "--archive-dir", archiveDir, "--symbols", "SOL", "--once",
	}, &bytes.Buffer{}, func() time.Time { return now }, func(perpspaper.Environment) (shadowPerpsReader, error) {
		return validStubShadowPerpsReader(now), nil
	}); err != nil {
		t.Fatal(err)
	}
	failed := validStubShadowPerpsReader(now.Add(time.Minute))
	failed.metaErr = errors.New("metadata unavailable")
	if err := runShadowPerpsPaperWith(t.Context(), []string{
		"--state-dir", stateDir, "--archive-dir", archiveDir, "--symbols", "SOL", "--once",
	}, &bytes.Buffer{}, func() time.Time { return now.Add(time.Minute) }, func(perpspaper.Environment) (shadowPerpsReader, error) {
		return failed, nil
	}); err == nil || !strings.Contains(err.Error(), "metadata unavailable") {
		t.Fatalf("metadata failure = %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sol-qualification.json")); err != nil {
		t.Fatalf("metadata failure rotated the completed current run: %v", err)
	}
}

func TestShadowPerpsArchivesKeepOnlyNewestRuns(t *testing.T) {
	archiveDir := filepath.Join(t.TempDir(), "runs")
	if err := os.Mkdir(archiveDir, 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for index := 0; index < shadowPerpsMaxArchivedRuns+2; index++ {
		name := start.Add(time.Duration(index) * time.Minute).Format("20060102T150405.000000000Z")
		if err := os.Mkdir(filepath.Join(archiveDir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneShadowPerpsArchives(archiveDir, shadowPerpsMaxArchivedRuns); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(archiveDir)
	if err != nil || len(entries) != shadowPerpsMaxArchivedRuns ||
		entries[0].Name() != start.Add(2*time.Minute).Format("20060102T150405.000000000Z") {
		t.Fatalf("retained archives = %v, %v", entries, err)
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
	end := shadowPerpsSettledCandleEnd(now)
	return []perpspaper.Candle{
		{OpenTime: end.Add(-2 * time.Minute).Add(time.Millisecond).UnixMilli(), CloseTime: end.Add(-time.Minute).UnixMilli(), Symbol: perpspaper.SOL, Interval: "1m", Open: first, Close: first, High: first, Low: first, Volume: "1"},
		{OpenTime: end.Add(-time.Minute).Add(time.Millisecond).UnixMilli(), CloseTime: end.UnixMilli(), Symbol: perpspaper.SOL, Interval: "1m", Open: second, Close: second, High: second, Low: second, Volume: "1"},
	}
}
