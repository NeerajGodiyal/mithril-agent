package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

func TestShadowPerpsWalkForwardReadsOnlySealedCompatibleTapes(t *testing.T) {
	base := t.TempDir()
	first := createAndSealShadowPerpsTestTape(t, base, "first", time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC))
	second := createAndSealShadowPerpsTestTape(t, base, "second", time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC))
	var output bytes.Buffer
	if err := runShadowPerpsWalkForward([]string{"--tape", first, "--tape", second}, &output); err != nil {
		t.Fatal(err)
	}
	var result perpspaper.WalkForwardQualification
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("walk-forward JSON = %q: %v", output.String(), err)
	}
	if result.Outcome != "insufficient_evidence" || !result.PaperOnly || result.Authorized || result.Promotable || len(result.Tapes) != 2 {
		t.Fatalf("walk-forward result = %+v", result)
	}
	if err := runShadowPerpsWalkForward([]string{"--tape", second, "--tape", first}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "chronological") {
		t.Fatalf("reversed tapes error = %v", err)
	}
}

func TestSettledCaptureKeepsVerifiedV3ReplayTapesCompatible(t *testing.T) {
	base := t.TempDir()
	config := shadowPerpsTapeConfig{
		Environment: perpspaper.Mainnet, Symbol: perpspaper.SOL, RiskArm: perpspaper.Balanced,
		StartingCollateralMicros: 100_000_000, VenueMaxLeverage: 20, VenueSzDecimals: 2,
	}
	// Sampling delay is recorded by frame timestamps; it does not change the v3
	// tape schema, accounting, or replay rules.
	legacy := shadowPerpsTape{
		Version: 3, PaperOnly: true, AccountingModel: "hyperliquid_causal_sampled_context_stress_v3",
		Config: config, Frames: flatShadowPerpsTestFrames(0, perpspaper.QualificationMinimumFrames),
	}
	if gap := legacy.Frames[0].Book.Time - legacy.Frames[0].Candles[1].CloseTime; gap != 1_000 {
		t.Fatalf("legacy fixture gap = %d", gap)
	}
	path, err := sealShadowPerpsTape(filepath.Join(base, "current"), legacy)
	if err != nil {
		t.Fatal(err)
	}
	read, _, err := readShadowPerpsCorpusTape(path)
	if err != nil {
		t.Fatal(err)
	}
	if read.Version != shadowPerpsTapeVersion || read.AccountingModel != shadowPerpsModel || !compatibleShadowPerpsTapes(config, read.Config) {
		t.Fatalf("verified v3 tape became incompatible: %+v", read)
	}
}

func TestShadowPerpsSealedTapeIsWriteOnceAndDigestBound(t *testing.T) {
	base := t.TempDir()
	path := createAndSealShadowPerpsTestTape(t, base, "first", time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC))
	tape, _, err := readShadowPerpsCorpusTape(path)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := sealShadowPerpsTape(filepath.Join(base, "another-current"), tape)
	if err != nil || duplicate != path {
		t.Fatalf("idempotent seal = %q, %v", duplicate, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], ' ', '\n')
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readShadowPerpsCorpusTape(path); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("mutated sealed tape error = %v", err)
	}
}

func TestShadowPerpsSealRejectsChangedClosedCandle(t *testing.T) {
	base := t.TempDir()
	frames := flatShadowPerpsTestFrames(0, perpspaper.QualificationMinimumFrames)
	frames[1].Candles[0].Close = "101"
	tape := shadowPerpsTape{
		Version: shadowPerpsTapeVersion, PaperOnly: true, AccountingModel: shadowPerpsModel,
		Config: shadowPerpsTapeConfig{
			Environment: perpspaper.Mainnet, Symbol: perpspaper.SOL, RiskArm: perpspaper.Balanced,
			StartingCollateralMicros: 100_000_000, VenueMaxLeverage: 20, VenueSzDecimals: 2,
		},
		Frames: frames,
	}
	if _, err := sealShadowPerpsTape(filepath.Join(base, "current"), tape); err == nil || !strings.Contains(err.Error(), "changes an existing closed candle") {
		t.Fatalf("changed closed candle seal error = %v", err)
	}
	if _, err := os.Stat(shadowPerpsCorpusDir(filepath.Join(base, "current"), perpspaper.SOL)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid tape created corpus: %v", err)
	}
}

func TestShadowPerpsStagingRecoveryRejectsChangedClosedCandle(t *testing.T) {
	base := t.TempDir()
	config := shadowPerpsTapeConfig{
		Environment: perpspaper.Mainnet, Symbol: perpspaper.SOL, RiskArm: perpspaper.Balanced,
		StartingCollateralMicros: 100_000_000, VenueMaxLeverage: 20, VenueSzDecimals: 2,
	}
	frames := flatShadowPerpsTestFrames(0, perpspaper.QualificationMinimumFrames)
	frames[1].Candles[0].Close = "101"
	raw, err := json.Marshal(shadowPerpsTape{
		Version: shadowPerpsTapeVersion, PaperOnly: true, AccountingModel: shadowPerpsModel,
		Config: config, Frames: frames,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	digest := sha256.Sum256(raw)
	name := "." + hex.EncodeToString(digest[:]) + ".staging"
	directory := shadowPerpsCorpusDir(filepath.Join(base, "current"), perpspaper.SOL)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recoverShadowPerpsStaging(directory, name); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(directory); err != nil || len(entries) != 0 {
		t.Fatalf("invalid staging survived recovery: %v, %v", entries, err)
	}
}

func TestShadowPerpsWalkForwardHelpAndArguments(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"shadow", "perps-walk-forward", "--help"}, &output); err != nil || !strings.Contains(output.String(), "final held-out tape") {
		t.Fatalf("help = %q, %v", output.String(), err)
	}
	for _, args := range [][]string{nil, {"--tape", "/tmp/one.json"}, {"--tape", "relative", "--tape", "/tmp/two.json"}} {
		if err := runShadowPerpsWalkForward(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("arguments accepted: %v", args)
		}
	}
}

func TestShadowPerpsWalkForwardMessageUsesPlainPaperLanguage(t *testing.T) {
	result := perpspaper.WalkForwardQualification{
		Outcome: "no_training_candidate",
		Tapes:   []perpspaper.WalkForwardTapeEvidence{{}, {}},
	}
	message := shadowPerpsWalkForwardMessage(result)
	for _, want := range []string{"PAPER · 🧪 STRATEGY CHECK", "Recordings checked: 2 separate", "Final untouched recording: kept closed", "No real order was sent."} {
		if !strings.Contains(message, want) {
			t.Fatalf("plain paper message = %q", message)
		}
	}
}

func TestWalkForwardSummaryShowsCompletedAttemptsWithoutSelectingThem(t *testing.T) {
	result := perpspaper.WalkForwardQualification{
		Outcome: "no_training_candidate", InputSHA256: strings.Repeat("a", 64),
		Tapes: []perpspaper.WalkForwardTapeEvidence{{Frames: 24}, {Frames: 24}},
		Training: []perpspaper.WalkForwardTrial{{
			QualificationKey: perpspaper.QualificationKey{RiskArm: perpspaper.Balanced, Strategy: perpspaper.StrategyMomentum},
			Eligible:         true, Aggregate: &perpspaper.TournamentScore{
				NetPnLMicros: -125_000, FeesPaidMicros: 25_000, FundingPnLMicros: 1_000,
				MaxDrawdownMicros: 400_000, FilledOrders: 2, ClosedPositions: 2,
			},
		}},
	}
	var summary paperstatus.CurrentSummary
	applyShadowPerpsWalkForward(&summary, result)
	if summary.QualificationStrategy != "" || summary.QualificationRiskProfile != "" ||
		len(summary.QualificationAttempts) != 1 {
		t.Fatalf("losing attempt was selected or omitted: %+v", summary)
	}
	attempt := summary.QualificationAttempts[0]
	if attempt.RiskProfile != "balanced" || attempt.Strategy != "momentum" ||
		attempt.NetPnLMicros != -125_000 || attempt.FeesMicros != 25_000 ||
		attempt.FundingMicros != 1_000 || attempt.MaxDrawdownMicros != 400_000 ||
		attempt.FilledOrders != 2 || attempt.ClosedPositions != 2 {
		t.Fatalf("attempt projection = %+v", attempt)
	}
}

func TestFinalizePreservesTapesAndPublishesMultiTapeSummary(t *testing.T) {
	base := t.TempDir()
	stateDir := filepath.Join(base, "current")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := shadowPerpsTapeConfig{
		Environment: perpspaper.Mainnet, Symbol: perpspaper.SOL, RiskArm: perpspaper.Balanced,
		StartingCollateralMicros: 100_000_000, VenueMaxLeverage: 20, VenueSzDecimals: 2,
	}
	write := func(offset int64) {
		t.Helper()
		tape := shadowPerpsTape{
			Version: shadowPerpsTapeVersion, PaperOnly: true, AccountingModel: shadowPerpsModel,
			Config: config, Frames: flatShadowPerpsTestFrames(offset, perpspaper.QualificationMinimumFrames),
		}
		if err := writeShadowPerpsJSON(filepath.Join(stateDir, "sol-tape.json"), tape); err != nil {
			t.Fatal(err)
		}
		if err := finalizeShadowPerpsRun(stateDir, []perpspaper.Symbol{perpspaper.SOL}, time.UnixMilli(offset+10_000_000)); err != nil {
			t.Fatal(err)
		}
	}
	write(0)
	entries, err := os.ReadDir(shadowPerpsCorpusDir(stateDir, perpspaper.SOL))
	if err != nil || len(entries) != 1 {
		t.Fatalf("first immutable tape = %v, %v", entries, err)
	}
	firstSealed := filepath.Join(shadowPerpsCorpusDir(stateDir, perpspaper.SOL), entries[0].Name())
	completeStaging := filepath.Join(shadowPerpsCorpusDir(stateDir, perpspaper.SOL), "."+strings.TrimSuffix(entries[0].Name(), ".json")+".staging")
	if err := os.Rename(firstSealed, completeStaging); err != nil {
		t.Fatal(err)
	}
	staleStaging := filepath.Join(shadowPerpsCorpusDir(stateDir, perpspaper.SOL), "."+strings.Repeat("f", 64)+".staging")
	if err := os.WriteFile(staleStaging, []byte("interrupted write"), 0o600); err != nil {
		t.Fatal(err)
	}
	write(10_000_000)
	if _, err := os.Stat(firstSealed); err != nil {
		t.Fatalf("complete staging tape was not promoted: %v", err)
	}
	if _, err := os.Lstat(completeStaging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("complete staging file remains after recovery: %v", err)
	}
	if _, err := os.Lstat(staleStaging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale staging file was not recovered: %v", err)
	}
	entries, err = os.ReadDir(shadowPerpsCorpusDir(stateDir, perpspaper.SOL))
	if err != nil || len(entries) != 2 {
		t.Fatalf("immutable tapes = %v, %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sol-walk-forward.json")); err != nil {
		t.Fatalf("walk-forward result: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, "sol-paper-status.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot paperstatus.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || snapshot.Summary == nil {
		t.Fatalf("paper status = %q, %v", raw, err)
	}
	summary := snapshot.Summary
	if summary.QualificationTapes != 2 || summary.QualificationOutcome != "no_training_candidate" ||
		summary.QualificationHoldoutEvaluated || summary.QualificationStressEvaluated ||
		summary.QualificationFrames != 2*perpspaper.QualificationMinimumFrames {
		t.Fatalf("multi-tape summary = %+v", summary)
	}
}

func TestPreparePreservesCompleteTapeAfterInterruptedRun(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir, archiveDir := filepath.Join(base, "current"), filepath.Join(base, "runs")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := shadowPerpsTapeConfig{
		Environment: perpspaper.Mainnet, Symbol: perpspaper.SOL, RiskArm: perpspaper.Balanced,
		StartingCollateralMicros: 100_000_000, VenueMaxLeverage: 20, VenueSzDecimals: 2,
	}
	tape := shadowPerpsTape{Version: shadowPerpsTapeVersion, PaperOnly: true, AccountingModel: shadowPerpsModel,
		Config: config, Frames: flatShadowPerpsTestFrames(0, perpspaper.QualificationMinimumFrames)}
	if err := writeShadowPerpsJSON(filepath.Join(stateDir, "sol-tape.json"), tape); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 9, 3, 14, 0, 0, 0, time.UTC)
	if err := prepareShadowPerpsRun(stateDir, archiveDir, startedAt); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(shadowPerpsCorpusDir(stateDir, perpspaper.SOL))
	if err != nil || len(entries) != 1 {
		t.Fatalf("recovered immutable tape = %v, %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(archiveDir, startedAt.Format("20060102T150405.000000000Z"), "sol-tape.json")); err != nil {
		t.Fatalf("archived interrupted run: %v", err)
	}
}

func TestPrepareDoesNotSealOrArchiveCausallyInvalidCompletedTape(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir, archiveDir := filepath.Join(base, "current"), filepath.Join(base, "runs")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := shadowPerpsTapeConfig{
		Environment: perpspaper.Mainnet, Symbol: perpspaper.SOL, RiskArm: perpspaper.Balanced,
		StartingCollateralMicros: 100_000_000, VenueMaxLeverage: 20, VenueSzDecimals: 2,
	}
	frames := flatShadowPerpsTestFrames(0, perpspaper.QualificationMinimumFrames)
	frames[1].Candles[0].Close = "101"
	tapePath := filepath.Join(stateDir, "sol-tape.json")
	if err := writeShadowPerpsJSON(tapePath, shadowPerpsTape{
		Version: shadowPerpsTapeVersion, PaperOnly: true, AccountingModel: shadowPerpsModel,
		Config: config, Frames: frames,
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(tapePath)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 9, 3, 15, 0, 0, 0, time.UTC)
	err = prepareShadowPerpsRun(stateDir, archiveDir, startedAt)
	if err == nil || !strings.Contains(err.Error(), "changes an existing closed candle") {
		t.Fatalf("invalid recovery error = %v", err)
	}
	after, readErr := os.ReadFile(tapePath)
	if readErr != nil || !bytes.Equal(after, before) {
		t.Fatalf("invalid current tape changed: %v", readErr)
	}
	if _, statErr := os.Stat(shadowPerpsCorpusDir(stateDir, perpspaper.SOL)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid tape created corpus: %v", statErr)
	}
	archivePath := filepath.Join(archiveDir, startedAt.Format("20060102T150405.000000000Z"))
	if _, statErr := os.Stat(archivePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid tape was archived: %v", statErr)
	}
}

func TestResearchFailureStillPublishesValidSingleTapeStatus(t *testing.T) {
	base := t.TempDir()
	stateDir, publishedDir := filepath.Join(base, "current"), filepath.Join(base, "published")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(publishedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := shadowPerpsTapeConfig{
		Environment: perpspaper.Mainnet, Symbol: perpspaper.SOL, RiskArm: perpspaper.Balanced,
		StartingCollateralMicros: 100_000_000, VenueMaxLeverage: 20, VenueSzDecimals: 2,
	}
	tape := shadowPerpsTape{Version: shadowPerpsTapeVersion, PaperOnly: true, AccountingModel: shadowPerpsModel,
		Config: config, Frames: flatShadowPerpsTestFrames(0, perpspaper.QualificationMinimumFrames)}
	if err := writeShadowPerpsJSON(filepath.Join(stateDir, "sol-tape.json"), tape); err != nil {
		t.Fatal(err)
	}
	corpus := shadowPerpsCorpusDir(stateDir, perpspaper.SOL)
	if err := ensureShadowPerpsPrivateDirectory(filepath.Dir(corpus)); err != nil {
		t.Fatal(err)
	}
	if err := ensureShadowPerpsPrivateDirectory(corpus); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corpus, "unexpected"), []byte("strict corpus"), 0o600); err != nil {
		t.Fatal(err)
	}
	endedAt := time.Date(2026, 9, 3, 15, 0, 0, 0, time.UTC)
	err := finalizeAndPublishShadowPerps(stateDir, publishedDir, []perpspaper.Symbol{perpspaper.SOL}, endedAt)
	var researchErr *shadowPerpsResearchError
	if !errors.As(err, &researchErr) {
		t.Fatalf("research failure = %v", err)
	}
	raw, readErr := os.ReadFile(filepath.Join(publishedDir, "sol-paper-status.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	var snapshot paperstatus.Snapshot
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Summary == nil ||
		snapshot.Summary.QualificationTapes != 1 || snapshot.Summary.QualificationFrames != perpspaper.QualificationMinimumFrames {
		t.Fatalf("published single-tape fallback = %s", raw)
	}
}

func createAndSealShadowPerpsTestTape(t *testing.T, base, name string, now time.Time) string {
	t.Helper()
	stateDir := filepath.Join(base, name)
	if err := runShadowPerpsPaperWith(t.Context(), []string{"--state-dir", stateDir, "--symbols", "SOL", "--once"}, &bytes.Buffer{}, func() time.Time { return now }, func(perpspaper.Environment) (shadowPerpsReader, error) {
		return validStubShadowPerpsReader(now), nil
	}); err != nil {
		t.Fatal(err)
	}
	tapePath := filepath.Join(stateDir, "sol-tape.json")
	raw, err := os.ReadFile(tapePath)
	if err != nil {
		t.Fatal(err)
	}
	var header shadowPerpsTape
	if err := json.Unmarshal(raw, &header); err != nil {
		t.Fatal(err)
	}
	tape, _, err := readShadowPerpsTape(tapePath, header.Config)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealShadowPerpsTape(stateDir, tape)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func flatShadowPerpsTestFrames(offset int64, count int) []perpspaper.TapeFrame {
	candles := make([]perpspaper.Candle, count+1)
	for index := range candles {
		open := offset + int64(index)*60_000 + 1
		candles[index] = perpspaper.Candle{
			OpenTime: open, CloseTime: open + 59_999, Symbol: perpspaper.SOL, Interval: "1m",
			Open: "100", Close: "100", High: "100", Low: "100", Volume: "1", Trades: 1,
		}
	}
	frames := make([]perpspaper.TapeFrame, count)
	for index := range frames {
		bookTime := candles[index+1].CloseTime + 1_000
		frames[index] = perpspaper.TapeFrame{
			Candles: []perpspaper.Candle{candles[index], candles[index+1]},
			Context: perpspaper.PriceContext{Symbol: perpspaper.SOL, MarkPx: "100", OraclePx: "100", ReceivedAt: bookTime},
			Book: perpspaper.L2Book{Symbol: perpspaper.SOL, Time: bookTime, Levels: [2][]perpspaper.Level{
				{{Price: "99.99", Size: strconv.Itoa(1_000), Count: 1}},
				{{Price: "100.01", Size: strconv.Itoa(1_000), Count: 1}},
			}},
		}
	}
	return frames
}
