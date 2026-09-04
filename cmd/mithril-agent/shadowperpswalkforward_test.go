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

	"github.com/Overclock-Validator/mithril-agent/journal"
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

func TestShadowPerpsFinalizationReceiptIsContentBoundIdempotentAndPrivate(t *testing.T) {
	stateDir, tape, tapeSHA256, replay, qualification, result, now := shadowPerpsFinalizationFixture(t)
	receipt, err := newShadowPerpsFinalizationReceipt(tape, tapeSHA256, replay, qualification, &result)
	if err != nil {
		t.Fatal(err)
	}
	count, appended, err := appendShadowPerpsFinalizationReceipt(stateDir, receipt, now)
	if err != nil || !appended || count != 1 {
		t.Fatalf("first receipt = %d, %t, %v", count, appended, err)
	}
	count, appended, err = appendShadowPerpsFinalizationReceipt(stateDir, receipt, now.Add(time.Second))
	if err != nil || appended || count != 1 {
		t.Fatalf("idempotent receipt = %d, %t, %v", count, appended, err)
	}
	raw, err := os.ReadFile(shadowPerpsFinalizationJournalPath(stateDir, tape.Config.Symbol))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{stateDir, `"reasons"`, `"ineligible_reason"`, `"score"`, `"frames"`, "execution_delay"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("finalization receipt exposed %q", forbidden)
		}
	}

	collision := result
	forward := *result.Forward
	score := *forward.Score
	score.NetPnLMicros++
	forward.Score = &score
	collision.Forward = &forward
	collisionReceipt, err := newShadowPerpsFinalizationReceipt(tape, tapeSHA256, replay, qualification, &collision)
	if err != nil {
		t.Fatal(err)
	}
	if _, appended, err := appendShadowPerpsFinalizationReceipt(
		stateDir, collisionReceipt, now.Add(2*time.Second),
	); err == nil || appended || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("contradictory receipt = %t, %v", appended, err)
	}
}

func TestShadowPerpsFinalizationReceiptPreservesOptionalWalkForwardAndRejectsMalformedHistory(t *testing.T) {
	stateDir, tape, tapeSHA256, replay, qualification, result, now := shadowPerpsFinalizationFixture(t)
	receipt, err := newShadowPerpsFinalizationReceipt(tape, tapeSHA256, replay, qualification, nil)
	if err != nil || receipt.SingleQualificationSHA256 != qualification.InputSHA256 ||
		receipt.WalkForwardInputSHA256 != "" || receipt.HoldoutEvaluated {
		t.Fatalf("single-tape receipt = %+v, %v", receipt, err)
	}

	result.Outcome = "no_training_candidate"
	result.EligibleForPaperExperiment = false
	result.TrainingLeader, result.Candidate = nil, nil
	result.Forward, result.Stress = nil, nil
	result.HoldoutPlansCompared, result.HoldoutCompletedTrades = 0, 0
	result.Reasons = []string{"no_profitable_completed_training_trade"}
	receipt, err = newShadowPerpsFinalizationReceipt(tape, tapeSHA256, replay, qualification, &result)
	if err != nil {
		t.Fatal(err)
	}
	count, appended, err := appendShadowPerpsFinalizationReceipt(stateDir, receipt, now)
	if err != nil || !appended || count != 1 {
		t.Fatalf("no-leader receipt = %d, %t, %v", count, appended, err)
	}
	store, err := journal.OpenRotating(shadowPerpsFinalizationJournalPath(stateDir, tape.Config.Symbol))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(now.Add(time.Second), "perps.unexpected", tapeSHA256, map[string]bool{"paper_only": true}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := appendShadowPerpsFinalizationReceipt(
		stateDir, receipt, now.Add(2*time.Second),
	); err == nil || !strings.Contains(err.Error(), "unexpected event") {
		t.Fatalf("malformed receipt history error = %v", err)
	}
}

func TestShadowPerpsExecutionDelayAdvisoryIsPrivateAndContentAddressed(t *testing.T) {
	stateDir, tape, tapeSHA256, _, _, result, _ := shadowPerpsFinalizationFixture(t)
	if result.TrainingLeader == nil {
		t.Fatal("fixture has no training leader")
	}
	advisory, err := perpspaper.EvaluateOneFrameExecutionDelay(
		result.Config, tape.Frames, result.InputSHA256, tapeSHA256, *result.TrainingLeader,
	)
	if err != nil {
		t.Fatal(err)
	}
	path, err := writeShadowPerpsExecutionDelayAdvisory(stateDir, tape.Config.Symbol, *result.TrainingLeader, advisory)
	if err != nil {
		t.Fatal(err)
	}
	again, err := writeShadowPerpsExecutionDelayAdvisory(stateDir, tape.Config.Symbol, *result.TrainingLeader, advisory)
	if err != nil || again != path {
		t.Fatalf("idempotent advisory = %q, %v", again, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	wantName := hex.EncodeToString(digest[:]) + ".json"
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		filepath.Base(path) != wantName || filepath.Dir(path) != filepath.Join(filepath.Dir(stateDir), "advisories", "sol") {
		t.Fatalf("private content-addressed advisory = %q, %v, %v", path, info, err)
	}
	for _, forbidden := range []string{stateDir, `"reasons"`, `"path"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("advisory exposed %q", forbidden)
		}
	}
}

func TestShadowPerpsExecutionDelayAdvisoryFailureDoesNotBlockFinalization(t *testing.T) {
	stateDir, tape, _, _, _, expected, now := shadowPerpsFinalizationFixture(t)
	publishedDir := filepath.Join(filepath.Dir(stateDir), "published")
	if err := os.Mkdir(publishedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	first := shadowPerpsTape{
		Version: shadowPerpsTapeVersion, PaperOnly: true, AccountingModel: shadowPerpsModel,
		Config: tape.Config, Frames: shadowPerpsPlanTestFrames(0, shadowPerpsPlanWavePrices(4)),
	}
	if _, err := sealShadowPerpsTape(stateDir, first); err != nil {
		t.Fatal(err)
	}
	if err := writeShadowPerpsJSON(filepath.Join(stateDir, "sol-tape.json"), tape); err != nil {
		t.Fatal(err)
	}
	advisoryRoot := filepath.Join(filepath.Dir(stateDir), "advisories")
	if err := os.WriteFile(advisoryRoot, []byte("block optional advisory directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := finalizeAndPublishShadowPerps(
		stateDir, publishedDir, []perpspaper.Symbol{perpspaper.SOL}, now,
	); err != nil {
		t.Fatalf("best-effort advisory blocked finalization: %v", err)
	}
	var walkForward perpspaper.WalkForwardQualification
	if err := readStrictJSON(filepath.Join(stateDir, "sol-walk-forward.json"), &walkForward); err != nil ||
		walkForward.InputSHA256 != expected.InputSHA256 || walkForward.TrainingLeader == nil {
		t.Fatalf("authoritative walk-forward = %+v, %v", walkForward, err)
	}
	if _, err := os.Stat(filepath.Join(publishedDir, "sol-paper-status.json")); err != nil {
		t.Fatalf("authoritative status was not published: %v", err)
	}
	store, err := journal.OpenRotating(shadowPerpsFinalizationJournalPath(stateDir, perpspaper.SOL))
	if err != nil {
		t.Fatal(err)
	}
	receipts, foldErr := foldShadowPerpsFinalizationReceipts(store.Records())
	closeErr := store.Close()
	if foldErr != nil || closeErr != nil || len(receipts) != 1 ||
		receipts[0].WalkForwardInputSHA256 != expected.InputSHA256 {
		t.Fatalf("authoritative receipt = %+v, %v, %v", receipts, foldErr, closeErr)
	}
	info, err := os.Lstat(advisoryRoot)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("best-effort advisory failure was not isolated: %v, %v", info, err)
	}
}

func shadowPerpsFinalizationFixture(t *testing.T) (
	string, shadowPerpsTape, string, perpspaper.TapeReplay,
	perpspaper.Qualification, perpspaper.WalkForwardQualification, time.Time,
) {
	t.Helper()
	stateDir, config, now := shadowPerpsPlanFixture(t)
	_, planSHA256, err := loadOrCreateShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, config, perpspaper.Balanced, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	tapeConfig := shadowPerpsTapeConfig{
		Environment: perpspaper.Mainnet, Symbol: config.Symbol, RiskArm: perpspaper.Balanced,
		StartingCollateralMicros: config.StartingCollateralMicros,
		VenueMaxLeverage:         config.VenueMaxLeverage, VenueSzDecimals: config.VenueSzDecimals,
		DecisionMode: shadowPerpsDecisionLegacy, PlanSHA256: planSHA256,
	}
	first := shadowPerpsTape{
		Version: shadowPerpsTapeVersion, PaperOnly: true, AccountingModel: shadowPerpsModel,
		Config: tapeConfig, Frames: shadowPerpsPlanTestFrames(0, shadowPerpsPlanWavePrices(4)),
	}
	second := shadowPerpsTape{
		Version: shadowPerpsTapeVersion, PaperOnly: true, AccountingModel: shadowPerpsModel,
		Config: tapeConfig, Frames: shadowPerpsPlanTestFrames(10_000_000, shadowPerpsPlanWavePrices(3)),
	}
	_, firstSHA256, err := canonicalShadowPerpsTape(first)
	if err != nil {
		t.Fatal(err)
	}
	_, secondSHA256, err := canonicalShadowPerpsTape(second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := perpspaper.QualifyWalkForward(config, []perpspaper.WalkForwardTape{
		{ContentSHA256: firstSHA256, Frames: first.Frames},
		{ContentSHA256: secondSHA256, Frames: second.Frames},
	})
	if err != nil || result.TrainingLeader == nil || result.Forward == nil || result.Forward.Score == nil {
		t.Fatalf("walk-forward fixture = %+v, %v", result, err)
	}
	qualification, err := perpspaper.QualifyTournament(config, second.Frames)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := replayShadowPerpsTape(second.Config, second.Frames)
	if err != nil {
		t.Fatal(err)
	}
	return stateDir, second, secondSHA256, replay, qualification, result, now.Add(time.Hour)
}

func TestSettledCaptureKeepsVerifiedV3ReplayTapesReadable(t *testing.T) {
	base := t.TempDir()
	config := shadowPerpsTapeConfig{
		Environment: perpspaper.Mainnet, Symbol: perpspaper.SOL, RiskArm: perpspaper.Balanced,
		StartingCollateralMicros: 100_000_000, VenueMaxLeverage: 20, VenueSzDecimals: 2,
	}
	// Sampling delay is recorded by frame timestamps; a v3 tape remains immutable
	// and readable after the selected-plan v4 format is introduced.
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
	if read.Version != 3 || read.AccountingModel != shadowPerpsLegacyModel || !compatibleShadowPerpsTapes(config, read.Config) {
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
	for _, want := range []string{"PAPER · 🧪 STRATEGY CHECK", "Recordings checked: 2 separate", "Final held-out recording: kept closed", "No real order was sent."} {
		if !strings.Contains(message, want) {
			t.Fatalf("plain paper message = %q", message)
		}
	}
	if strings.Contains(message, "made money") || !strings.Contains(message, "passed every training gate") {
		t.Fatalf("paper message overstates its rejection reason: %q", message)
	}
}

func TestShadowPerpsQualificationMessageUsesAggressiveOperatorLabel(t *testing.T) {
	message := shadowPerpsQualificationMessage(perpspaper.Qualification{
		Outcome: "candidate_rejected",
		TrainingLeader: &perpspaper.QualificationKey{
			RiskArm: perpspaper.Experimental, Strategy: perpspaper.StrategyRegime,
		},
	})
	if !strings.Contains(message, "regime · aggressive") || strings.Contains(message, "experimental") {
		t.Fatalf("candidate message exposes the internal risk name: %q", message)
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
	stateDir, publishedDir := filepath.Join(base, "current"), filepath.Join(base, "published")
	for _, directory := range []string{stateDir, publishedDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	config := shadowPerpsTapeConfig{
		Environment: perpspaper.Mainnet, Symbol: perpspaper.SOL, RiskArm: perpspaper.Balanced,
		StartingCollateralMicros: 100_000_000, VenueMaxLeverage: 20, VenueSzDecimals: 2,
	}
	_, planSHA256, err := loadOrCreateShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, config.qualificationConfig(), perpspaper.Balanced,
		time.Date(2026, 9, 3, 7, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	config.DecisionMode, config.PlanSHA256 = shadowPerpsDecisionLegacy, planSHA256
	write := func(offset int64) {
		t.Helper()
		tape := shadowPerpsTape{
			Version: shadowPerpsTapeVersion, PaperOnly: true, AccountingModel: shadowPerpsModel,
			Config: config, Frames: flatShadowPerpsTestFrames(offset, perpspaper.QualificationMinimumFrames),
		}
		if err := writeShadowPerpsJSON(filepath.Join(stateDir, "sol-tape.json"), tape); err != nil {
			t.Fatal(err)
		}
		if err := finalizeAndPublishShadowPerps(
			stateDir, publishedDir, []perpspaper.Symbol{perpspaper.SOL}, time.UnixMilli(offset+10_000_000).UTC(),
		); err != nil {
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
	walkForwardPath := filepath.Join(stateDir, "sol-walk-forward.json")
	walkForwardRaw, err := os.ReadFile(walkForwardPath)
	if err != nil {
		t.Fatalf("walk-forward result: %v", err)
	}
	var walkForward perpspaper.WalkForwardQualification
	if err := json.Unmarshal(walkForwardRaw, &walkForward); err != nil {
		t.Fatalf("receipted walk-forward result = %+v, %v", walkForward, err)
	}
	store, err := journal.OpenRotating(shadowPerpsFinalizationJournalPath(stateDir, perpspaper.SOL))
	if err != nil {
		t.Fatal(err)
	}
	receipts, foldErr := foldShadowPerpsFinalizationReceipts(store.Records())
	closeErr := store.Close()
	if foldErr != nil || closeErr != nil || len(receipts) != 2 {
		t.Fatalf("finalization receipts = %+v, %v, %v", receipts, foldErr, closeErr)
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
	preservedPaths := []string{
		walkForwardPath,
		filepath.Join(stateDir, "sol-qualification.json"),
		filepath.Join(stateDir, "sol-status.json"),
		filepath.Join(stateDir, "sol-paper-status.json"),
		filepath.Join(publishedDir, "sol-paper-status.json"),
		filepath.Join(stateDir, "sol-plan-selection.json"),
		shadowPerpsActivePlanPath(stateDir, perpspaper.SOL),
	}
	preserved := make(map[string][]byte, len(preservedPaths))
	for _, path := range preservedPaths {
		before, err := os.ReadFile(path)
		if err == nil {
			preserved[path] = before
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		preserved[path] = nil
	}

	store, err = journal.OpenRotating(shadowPerpsFinalizationJournalPath(stateDir, perpspaper.SOL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(time.UnixMilli(20_000_000), "perps.unexpected", strings.Repeat("f", 64), map[string]bool{"paper_only": true}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	third := shadowPerpsTape{
		Version: shadowPerpsTapeVersion, PaperOnly: true, AccountingModel: shadowPerpsModel,
		Config: config, Frames: flatShadowPerpsTestFrames(20_000_000, perpspaper.QualificationMinimumFrames),
	}
	if err := writeShadowPerpsJSON(filepath.Join(stateDir, "sol-tape.json"), third); err != nil {
		t.Fatal(err)
	}
	if err := finalizeAndPublishShadowPerps(
		stateDir, publishedDir, []perpspaper.Symbol{perpspaper.SOL}, time.UnixMilli(30_000_000),
	); err == nil ||
		!strings.Contains(err.Error(), "unexpected event") {
		t.Fatalf("malformed receipt history error = %v", err)
	}
	for path, before := range preserved {
		after, err := os.ReadFile(path)
		if before == nil {
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed receipt created %s: %v", path, err)
			}
			continue
		}
		if err != nil || !bytes.Equal(after, before) {
			t.Fatalf("failed receipt changed %s: %v", path, err)
		}
	}
}

func TestCurrentFinalizationFailurePreservesPublishedStateAndCanRetry(t *testing.T) {
	base := t.TempDir()
	stateDir, publishedDir := filepath.Join(base, "current"), filepath.Join(base, "published")
	for _, directory := range []string{stateDir, publishedDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	config := shadowPerpsTapeConfig{
		Environment: perpspaper.Mainnet, Symbol: perpspaper.SOL, RiskArm: perpspaper.Balanced,
		StartingCollateralMicros: 100_000_000, VenueMaxLeverage: 20, VenueSzDecimals: 2,
	}
	_, planSHA256, err := loadOrCreateShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, config.qualificationConfig(), perpspaper.Balanced,
		time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	config.DecisionMode, config.PlanSHA256 = shadowPerpsDecisionLegacy, planSHA256
	first := shadowPerpsTape{
		Version: shadowPerpsTapeVersion, PaperOnly: true, AccountingModel: shadowPerpsModel,
		Config: config, Frames: flatShadowPerpsTestFrames(0, perpspaper.QualificationMinimumFrames),
	}
	if _, err := sealShadowPerpsTape(stateDir, first); err != nil {
		t.Fatal(err)
	}
	second := shadowPerpsTape{
		Version: shadowPerpsTapeVersion, PaperOnly: true, AccountingModel: shadowPerpsModel,
		Config: config, Frames: flatShadowPerpsTestFrames(10_000_000, perpspaper.QualificationMinimumFrames),
	}
	if err := writeShadowPerpsJSON(filepath.Join(stateDir, "sol-tape.json"), second); err != nil {
		t.Fatal(err)
	}
	walkForwardPath := filepath.Join(stateDir, "sol-walk-forward.json")
	publishedPath := filepath.Join(publishedDir, "sol-paper-status.json")
	previousWalkForward, previousPublished := []byte("previous walk-forward\n"), []byte("previous published\n")
	if err := os.WriteFile(walkForwardPath, previousWalkForward, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publishedPath, previousPublished, 0o600); err != nil {
		t.Fatal(err)
	}
	activePath := shadowPerpsActivePlanPath(stateDir, perpspaper.SOL)
	previousPointer, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	unexpected := filepath.Join(shadowPerpsCorpusDir(stateDir, perpspaper.SOL), "unexpected")
	if err := os.WriteFile(unexpected, []byte("invalid corpus entry"), 0o600); err != nil {
		t.Fatal(err)
	}
	endedAt := time.Date(2026, 9, 4, 9, 0, 0, 0, time.UTC)
	if err := finalizeAndPublishShadowPerps(stateDir, publishedDir, []perpspaper.Symbol{perpspaper.SOL}, endedAt); err == nil ||
		!strings.Contains(err.Error(), "unexpected entry") {
		t.Fatalf("invalid corpus finalization error = %v", err)
	}
	for path, want := range map[string][]byte{
		walkForwardPath: previousWalkForward,
		publishedPath:   previousPublished,
		activePath:      previousPointer,
	} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("failed finalization changed %s: %v", path, err)
		}
	}
	if _, err := os.Stat(shadowPerpsFinalizationJournalPath(stateDir, perpspaper.SOL)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed evaluation wrote a finalization receipt: %v", err)
	}
	if err := os.Remove(unexpected); err != nil {
		t.Fatal(err)
	}
	if err := finalizeAndPublishShadowPerps(stateDir, publishedDir, []perpspaper.Symbol{perpspaper.SOL}, endedAt); err != nil {
		t.Fatalf("repaired corpus retry: %v", err)
	}
	store, err := journal.OpenRotating(shadowPerpsFinalizationJournalPath(stateDir, perpspaper.SOL))
	if err != nil {
		t.Fatal(err)
	}
	receipts, foldErr := foldShadowPerpsFinalizationReceipts(store.Records())
	closeErr := store.Close()
	if foldErr != nil || closeErr != nil || len(receipts) != 1 || receipts[0].WalkForwardInputSHA256 == "" {
		t.Fatalf("repaired finalization receipt = %+v, %v, %v", receipts, foldErr, closeErr)
	}
	if raw, err := os.ReadFile(publishedPath); err != nil || bytes.Equal(raw, previousPublished) {
		t.Fatalf("repaired finalization did not publish: %v", err)
	}
}

func TestCurrentShortFinalizationIsReceiptedButDoesNotEnterWalkForwardCorpus(t *testing.T) {
	base := t.TempDir()
	stateDir, publishedDir := filepath.Join(base, "current"), filepath.Join(base, "published")
	for _, directory := range []string{stateDir, publishedDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	config := shadowPerpsTapeConfig{
		Environment: perpspaper.Mainnet, Symbol: perpspaper.SOL, RiskArm: perpspaper.Balanced,
		StartingCollateralMicros: 100_000_000, VenueMaxLeverage: 20, VenueSzDecimals: 2,
	}
	_, planSHA256, err := loadOrCreateShadowPerpsPlan(
		stateDir, perpspaper.Mainnet, config.qualificationConfig(), perpspaper.Balanced,
		time.Date(2026, 9, 4, 9, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	config.DecisionMode, config.PlanSHA256 = shadowPerpsDecisionLegacy, planSHA256
	write := func(offset int64, frames int, endedAt time.Time) {
		t.Helper()
		tape := shadowPerpsTape{
			Version: shadowPerpsTapeVersion, PaperOnly: true, AccountingModel: shadowPerpsModel,
			Config: config, Frames: flatShadowPerpsTestFrames(offset, frames),
		}
		if err := writeShadowPerpsJSON(filepath.Join(stateDir, "sol-tape.json"), tape); err != nil {
			t.Fatal(err)
		}
		if err := finalizeAndPublishShadowPerps(
			stateDir, publishedDir, []perpspaper.Symbol{perpspaper.SOL}, endedAt,
		); err != nil {
			t.Fatal(err)
		}
	}
	write(0, 1, time.Date(2026, 9, 4, 9, 1, 0, 0, time.UTC))
	if entries, err := os.ReadDir(shadowPerpsCorpusDir(stateDir, perpspaper.SOL)); !errors.Is(err, os.ErrNotExist) || len(entries) != 0 {
		t.Fatalf("short tape entered walk-forward corpus: %v, %v", entries, err)
	}
	write(10_000_000, perpspaper.QualificationMinimumFrames, time.Date(2026, 9, 4, 9, 2, 0, 0, time.UTC))
	write(20_000_000, perpspaper.QualificationMinimumFrames, time.Date(2026, 9, 4, 9, 3, 0, 0, time.UTC))
	entries, err := os.ReadDir(shadowPerpsCorpusDir(stateDir, perpspaper.SOL))
	if err != nil || len(entries) != 2 {
		t.Fatalf("complete walk-forward corpus = %v, %v", entries, err)
	}
	var walkForward perpspaper.WalkForwardQualification
	if err := readStrictJSON(filepath.Join(stateDir, "sol-walk-forward.json"), &walkForward); err != nil ||
		len(walkForward.Tapes) != 2 || walkForward.Outcome == "insufficient_evidence" {
		t.Fatalf("walk-forward after short run = %+v, %v", walkForward, err)
	}
	store, err := journal.OpenRotating(shadowPerpsFinalizationJournalPath(stateDir, perpspaper.SOL))
	if err != nil {
		t.Fatal(err)
	}
	receipts, foldErr := foldShadowPerpsFinalizationReceipts(store.Records())
	closeErr := store.Close()
	if foldErr != nil || closeErr != nil || len(receipts) != 3 || receipts[0].WalkForwardInputSHA256 != "" {
		t.Fatalf("short and complete finalization receipts = %+v, %v, %v", receipts, foldErr, closeErr)
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
	tape := shadowPerpsTape{Version: 3, PaperOnly: true, AccountingModel: shadowPerpsLegacyModel,
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
		Version: 3, PaperOnly: true, AccountingModel: shadowPerpsLegacyModel,
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
	tape := shadowPerpsTape{Version: 3, PaperOnly: true, AccountingModel: shadowPerpsLegacyModel,
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
