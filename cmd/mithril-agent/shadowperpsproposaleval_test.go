package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

func TestPerpsEvaluateHelpIsRegisteredAndNonAuthorizing(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"shadow", "perps-evaluate", "--help"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"--proposal PATH", "assigned episode", "not the latest", "not persisted", "Missing or", "historical advisory", "never qualification"} {
		if !strings.Contains(output.String(), text) {
			t.Fatalf("evaluator help missing %q: %s", text, output.String())
		}
	}
}

func evalFreezeFixture(t *testing.T) (string, string, *shadowPerpsEpisode, time.Time) {
	t.Helper()
	args, state, episode, at := perpsFreezeFixture(t)
	if err := runShadowPerpsFreeze(args, &bytes.Buffer{}, func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(filepath.Dir(state), "proposals", "sol", "test-proposal.json"), state, episode, at
}

func evaluateForTest(t *testing.T, path string, at time.Time) (shadowPerpsProposalEvaluation, []byte) {
	t.Helper()
	var output bytes.Buffer
	if err := runShadowPerpsEvaluate([]string{"--proposal", path}, &output, func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
	var result shadowPerpsProposalEvaluation
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result, output.Bytes()
}

func completedProposalTarget(t *testing.T, prices []int, changes ...func(*shadowPerpsTape)) (string, string, shadowPerpsTape, time.Time) {
	t.Helper()
	path, state, first, at := evalFreezeFixture(t)
	if err := first.finish(state, at.Add(time.Second), false); err != nil {
		t.Fatal(err)
	}
	start := at.Add(2 * time.Second)
	next, err := beginShadowPerpsEpisode(state, episodeTestConfig(), start)
	if err != nil {
		t.Fatal(err)
	}
	proposal, _, err := readPerpsProposal(path)
	if err != nil {
		t.Fatal(err)
	}
	baseline := proposal.Baseline
	tape := shadowPerpsTape{Version: shadowPerpsTapeVersion, PaperOnly: true, AccountingModel: shadowPerpsModel,
		Config: shadowPerpsTapeConfig{Environment: baseline.Environment, Symbol: baseline.Config.Symbol, RiskArm: baseline.Key.RiskArm, StartingCollateralMicros: baseline.Config.StartingCollateralMicros, VenueMaxLeverage: baseline.Config.VenueMaxLeverage, VenueSzDecimals: baseline.Config.VenueSzDecimals, DecisionMode: baseline.DecisionMode, Strategy: baseline.Key.Strategy, PlanSHA256: proposal.BaselineSHA256, QualificationInputSHA256: baseline.QualificationInputSHA256}, Frames: shadowPerpsPlanTestFrames(start.UnixMilli(), prices)}
	for _, change := range changes {
		change(&tape)
	}
	raw, digest, err := canonicalShadowPerpsTape(tape)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "sol-tape.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if len(tape.Frames) >= perpspaper.QualificationMinimumFrames {
		if _, err := sealShadowPerpsTape(state, tape); err != nil {
			t.Fatal(err)
		}
	}
	qualification, err := perpspaper.QualifyTournament(tape.Config.qualificationConfig(), tape.Frames)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := replayShadowPerpsTape(tape.Config, tape.Frames)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := newShadowPerpsFinalizationReceipt(tape, digest, replay, qualification, nil)
	if err != nil {
		t.Fatal(err)
	}
	ended := time.UnixMilli(tape.Frames[len(tape.Frames)-1].Book.Time).UTC().Add(time.Second)
	if _, _, err := appendShadowPerpsFinalizationReceipt(state, receipt, ended); err != nil {
		t.Fatal(err)
	}
	if err := next.finish(state, ended.Add(time.Second), true); err != nil {
		t.Fatal(err)
	}
	return path, state, tape, ended.Add(2 * time.Second)
}

func TestPerpsEvaluatePendingThenIncompleteIsNeverSkipped(t *testing.T) {
	path, state, first, at := evalFreezeFixture(t)
	result, _ := evaluateForTest(t, path, at)
	if result.Status != "pending" || result.Reason != "target_not_in_observed_prefix" {
		t.Fatalf("result=%+v", result)
	}
	dir := filepath.Join(filepath.Dir(state), "proposal-evaluations", "sol")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "evaluation.lock" {
		t.Fatalf("pending persisted: %v", entries)
	}
	if err := first.finish(state, at.Add(time.Second), false); err != nil {
		t.Fatal(err)
	}
	next, err := beginShadowPerpsEpisode(state, episodeTestConfig(), at.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	result, _ = evaluateForTest(t, path, at.Add(3*time.Second))
	if result.Status != "pending" || result.Reason != "target_unresolved" {
		t.Fatalf("result=%+v", result)
	}
	if err := next.finish(state, at.Add(4*time.Second), false); err != nil {
		t.Fatal(err)
	}
	result, original := evaluateForTest(t, path, at.Add(5*time.Second))
	if result.Status != "unevaluable" || result.Reason != "target_incomplete" {
		t.Fatalf("result=%+v", result)
	}
	later, err := beginShadowPerpsEpisode(state, episodeTestConfig(), at.Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := later.finish(state, at.Add(7*time.Second), false); err != nil {
		t.Fatal(err)
	}
	var retried bytes.Buffer
	if err := runShadowPerpsEvaluate([]string{"--proposal", path}, &retried, func() time.Time { t.Fatal("retry renewed time"); return at }); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, retried.Bytes()) {
		t.Fatal("terminal outcome advanced to another target")
	}
}

func TestPerpsEvaluateMatchesExistingModeledScorer(t *testing.T) {
	path, state, tape, at := completedProposalTarget(t, shadowPerpsPlanWavePrices(3))
	proposal, _, err := readPerpsProposal(path)
	if err != nil {
		t.Fatal(err)
	}
	active := shadowPerpsActivePlanPath(state, perpspaper.SOL)
	before, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	result, original := evaluateForTest(t, path, at)
	baseline, stress, err := perpspaper.EvaluateFixedPlan(proposal.Baseline.Config, proposal.Baseline.Key, tape.Frames)
	if err != nil {
		t.Fatal(err)
	}
	proposed, proposedStress, err := perpspaper.EvaluateFixedPlan(proposal.Baseline.Config, perpspaper.QualificationKey{RiskArm: proposal.Input.RiskArm, Strategy: proposal.Input.Strategy}, tape.Frames)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Baseline, &baseline) || !reflect.DeepEqual(result.BaselineStress, &stress) || !reflect.DeepEqual(result.Proposed, &proposed) || !reflect.DeepEqual(result.ProposedStress, &proposedStress) || result.Authorized || result.Promotable {
		t.Fatalf("scorer mismatch: %+v", result)
	}
	_, retried := evaluateForTest(t, path, at.Add(time.Hour))
	if !bytes.Equal(original, retried) {
		t.Fatal("retry changed result time")
	}
	after, err := os.ReadFile(active)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("active plan changed: %v", err)
	}
}

func TestPerpsEvaluateShortTapeDistinctFromMissingBoundTape(t *testing.T) {
	for _, short := range []bool{true, false} {
		t.Run(map[bool]string{true: "short", false: "missing"}[short], func(t *testing.T) {
			prices := shadowPerpsPlanWavePrices(3)
			if short {
				prices = prices[:2]
			}
			path, state, tape, at := completedProposalTarget(t, prices)
			if short {
				result, _ := evaluateForTest(t, path, at)
				if result.Status != "unevaluable" || result.Reason != "target_has_insufficient_frames" {
					t.Fatalf("result=%+v", result)
				}
				return
			}
			_, digest, err := canonicalShadowPerpsTape(tape)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(shadowPerpsCorpusDir(state, perpspaper.SOL), digest+".json")); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := runShadowPerpsEvaluate([]string{"--proposal", path}, &output, func() time.Time { return at }); err == nil || output.Len() != 0 {
				t.Fatalf("err=%v output=%s", err, output.String())
			}
			entries, err := os.ReadDir(filepath.Join(filepath.Dir(state), "proposal-evaluations", "sol"))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("verification failure persisted: %v", entries)
			}
		})
	}
}

func TestPerpsEvaluateRejectsBackdatedOutcomeAndResealedTamper(t *testing.T) {
	path, state, _, at := completedProposalTarget(t, shadowPerpsPlanWavePrices(3))
	var output bytes.Buffer
	if err := runShadowPerpsEvaluate([]string{"--proposal", path}, &output, func() time.Time { return at.Add(-time.Hour) }); err == nil || output.Len() != 0 {
		t.Fatalf("backdated err=%v output=%s", err, output.String())
	}
	result, _ := evaluateForTest(t, path, at)
	result.Reason = "fabricated"
	encoded, err := canonicalPerpsEvaluation(result)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(filepath.Dir(state), "proposal-evaluations", "sol", result.ProposalSHA256+".json")
	if err := os.WriteFile(resultPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := runShadowPerpsEvaluate([]string{"--proposal", path}, &output, func() time.Time { return at }); err == nil || output.Len() != 0 {
		t.Fatalf("tamper err=%v output=%s", err, output.String())
	}
}

func TestPerpsEvaluateZeroTradesAreNotMissingEvidence(t *testing.T) {
	prices := make([]int, 40)
	for i := range prices {
		prices[i] = 1000
	}
	path, _, _, at := completedProposalTarget(t, prices)
	result, _ := evaluateForTest(t, path, at)
	if result.Status != "evaluated" || result.Proposed == nil || !result.Proposed.Eligible || result.Proposed.Score == nil || result.Proposed.Score.FilledOrders != 0 {
		t.Fatalf("zero-trade result=%+v", result)
	}
}

func TestPerpsEvaluateVisibleBookIneligibilityIsNotZeroScore(t *testing.T) {
	prices := make([]int, 40)
	for i := range prices {
		prices[i] = 1000 + i*10
	}
	path, _, _, at := completedProposalTarget(t, prices, func(tape *shadowPerpsTape) {
		last := &tape.Frames[len(tape.Frames)-1]
		last.Book.Levels[0][0].Size = "0.01"
		last.Book.Levels[1][0].Size = "0.01"
	})
	result, _ := evaluateForTest(t, path, at)
	if result.Status != "unevaluable" || result.Reason != "modeled_plan_ineligible" {
		t.Fatalf("result=%+v", result)
	}
	found := false
	for _, evidence := range []*perpspaper.QualificationEvidence{result.Baseline, result.BaselineStress, result.Proposed, result.ProposedStress} {
		if evidence != nil && !evidence.Eligible && evidence.Score == nil && evidence.IneligibleReason == "terminal_position_cannot_fill_from_visible_book" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing typed liquidity evidence: %+v", result)
	}
}

func TestPerpsEvaluateSettingsChangeConsumesAssignedTarget(t *testing.T) {
	for _, field := range []string{"arm", "cadence", "duration", "once", "archive", "collateral", "environment", "symbols"} {
		t.Run(field, func(t *testing.T) {
			path, state, first, at := evalFreezeFixture(t)
			if err := first.finish(state, at.Add(time.Second), false); err != nil {
				t.Fatal(err)
			}
			config := episodeTestConfig()
			switch field {
			case "arm":
				config.RiskArm = perpspaper.Conservative
			case "cadence":
				config.Cadence = time.Second
			case "duration":
				config.Duration = time.Hour
			case "once":
				config.Once = true
			case "archive":
				config.Archived = false
			case "collateral":
				config.Collateral++
			case "environment":
				config.Environment = perpspaper.Testnet
			case "symbols":
				config.Symbols = append(config.Symbols, perpspaper.BTC)
			}
			next, err := beginShadowPerpsEpisode(state, config, at.Add(2*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if err := next.finish(state, at.Add(3*time.Second), true); err != nil {
				t.Fatal(err)
			}
			result, _ := evaluateForTest(t, path, at.Add(4*time.Second))
			if result.Status != "unevaluable" || result.Reason != "target_configuration_changed" || result.TargetEpisode != "2" {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestPerpsEvaluateRejectsPublishedPrefixRollback(t *testing.T) {
	args, state, episode, at := perpsFreezeFixture(t)
	old, err := os.ReadFile(episode.path + ".prefix.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := episode.finish(state, at, false); err != nil {
		t.Fatal(err)
	}
	if err := runShadowPerpsFreeze(args, &bytes.Buffer{}, func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(episode.path+".prefix.json", old, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(state), "proposals", "sol", "test-proposal.json")
	var output bytes.Buffer
	if err := runShadowPerpsEvaluate([]string{"--proposal", path}, &output, func() time.Time { return at }); err == nil || output.Len() != 0 {
		t.Fatalf("rollback err=%v output=%s", err, output.String())
	}
}

func TestPerpsEvaluateChecksFinalizationMetadataAgainstTape(t *testing.T) {
	_, state, tape, _ := completedProposalTarget(t, shadowPerpsPlanWavePrices(3))
	_, digest, err := canonicalShadowPerpsTape(tape)
	if err != nil {
		t.Fatal(err)
	}
	records, err := journal.ReadRecords(shadowPerpsFinalizationJournalPath(state, perpspaper.SOL))
	if err != nil {
		t.Fatal(err)
	}
	var record journal.Record
	for _, candidate := range records {
		if candidate.ActionID == digest {
			record = candidate
		}
	}
	if err := verifyProposalTapeRecord(tape, digest, record); err != nil {
		t.Fatal(err)
	}
	var receipt shadowPerpsFinalizationReceipt
	if err := json.Unmarshal(record.Payload, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt.SingleResultSHA256 = strings.Repeat("f", 64)
	if err := receipt.validate(); err != nil {
		t.Fatalf("fixture must remain structurally valid: %v", err)
	}
	record.Payload, err = json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyProposalTapeRecord(tape, digest, record); err == nil {
		t.Fatal("accepted structurally valid false finalization metrics")
	}
}

func TestPerpsEvaluateRejectsContextReceivedAfterFinalization(t *testing.T) {
	path, state, _, at := completedProposalTarget(t, shadowPerpsPlanWavePrices(3), func(tape *shadowPerpsTape) {
		last := &tape.Frames[len(tape.Frames)-1]
		// Replay permits this small context/book separation, but the fixture's
		// finalization and terminal precede receipt of this context.
		last.Context.ReceivedAt = last.Book.Time + 3000
	})
	var output bytes.Buffer
	if err := runShadowPerpsEvaluate([]string{"--proposal", path}, &output, func() time.Time { return at }); err == nil || output.Len() != 0 {
		t.Fatalf("accepted context received after finalization: err=%v output=%s", err, output.String())
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(state), "proposal-evaluations", "sol"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "evaluation.lock" {
		t.Fatalf("temporal verification failure persisted: entries=%v err=%v", entries, err)
	}
}

func TestPerpsEvaluateShortTapeRequiresBoundFinalization(t *testing.T) {
	path, state, tape, at := completedProposalTarget(t, shadowPerpsPlanWavePrices(3)[:2])
	journalPath := shadowPerpsFinalizationJournalPath(state, perpspaper.SOL)
	records, err := journal.ReadRecords(journalPath)
	if err != nil || len(records) != 2 {
		t.Fatalf("fixture finalizations=%+v err=%v", records, err)
	}
	_, digest, err := canonicalShadowPerpsTape(tape)
	if err != nil || records[1].ActionID != digest {
		t.Fatalf("target finalization mismatch: digest=%s err=%v", digest, err)
	}
	raw, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	// Remove only the target suffix, leaving the exact training record intact.
	cut := bytes.LastIndexByte(bytes.TrimSuffix(raw, []byte{'\n'}), '\n')
	if cut < 0 {
		t.Fatal("fixture has no training prefix")
	}
	if err := os.WriteFile(journalPath, raw[:cut+1], 0600); err != nil {
		t.Fatal(err)
	}
	remaining, err := journal.ReadRecords(journalPath)
	if err != nil || len(remaining) != 1 || remaining[0].Hash != records[0].Hash {
		t.Fatalf("training prefix changed: records=%+v err=%v", remaining, err)
	}
	var output bytes.Buffer
	if err := runShadowPerpsEvaluate([]string{"--proposal", path}, &output, func() time.Time { return at }); err == nil || output.Len() != 0 {
		t.Fatalf("short tape accepted missing bound finalization: err=%v output=%s", err, output.String())
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(state), "proposal-evaluations", "sol"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "evaluation.lock" {
		t.Fatalf("missing provenance persisted: entries=%v err=%v", entries, err)
	}
}

func TestPerpsProposalFrameTimesUseHostObservationBounds(t *testing.T) {
	at := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"inclusive_bounds", "context_before_start", "book_before_start", "training_context_after_known", "training_book_after_known", "submillisecond_start"} {
		t.Run(name, func(t *testing.T) {
			start, known := at, at.Add(time.Second)
			// This directly exercises the time guard, not tape replay validity.
			frames := []perpspaper.TapeFrame{
				{Book: perpspaper.L2Book{Time: start.UnixMilli()}, Context: perpspaper.PriceContext{ReceivedAt: start.UnixMilli()}},
				{Book: perpspaper.L2Book{Time: known.UnixMilli()}, Context: perpspaper.PriceContext{ReceivedAt: known.UnixMilli()}},
			}
			switch name {
			case "context_before_start":
				frames[0].Context.ReceivedAt--
			case "book_before_start":
				frames[0].Book.Time--
			case "training_context_after_known":
				start = time.Time{}
				frames[1].Context.ReceivedAt++
			case "training_book_after_known":
				start = time.Time{}
				frames[1].Book.Time++
			case "submillisecond_start":
				start = start.Add(time.Nanosecond)
			}
			err := validatePerpsProposalFrameTimes(frames, start, known)
			if (err == nil) != (name == "inclusive_bounds") {
				t.Fatalf("time guard error=%v", err)
			}
		})
	}
}
