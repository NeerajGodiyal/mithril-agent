package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/paperdashboard"
	"github.com/Overclock-Validator/mithril-agent/researchpacket"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestShadowResearchOutcomeJournalIsIdempotentAndFoldsSelection(t *testing.T) {
	base, _, candidate, candidateSHA256, challenge := shadowResearchOutcomeFixture(t)
	root := privateTestDirectory(t)
	path := filepath.Join(root, "research-outcomes.jsonl")
	evaluatedAt := time.Date(2026, 9, 4, 9, 0, 0, 0, time.UTC)

	receipt, appended, err := recordShadowResearchForwardOutcome(
		path, evaluatedAt, base, candidate, candidateSHA256, challenge,
	)
	if err != nil || !appended {
		t.Fatalf("record forward outcome = %+v, %t, %v", receipt, appended, err)
	}
	if _, appended, err := recordShadowResearchForwardOutcome(
		path, evaluatedAt, base, candidate, candidateSHA256, challenge,
	); err != nil || appended {
		t.Fatalf("duplicate forward outcome appended = %t, %v", appended, err)
	}
	selectedAt := evaluatedAt.Add(time.Second)
	if _, appended, err := recordShadowResearchSelectionConfirmation(
		path, selectedAt, base, candidate, candidateSHA256, challenge,
	); err != nil || !appended {
		t.Fatalf("record selection confirmation = %t, %v", appended, err)
	}
	if _, appended, err := recordShadowResearchSelectionConfirmation(
		path, selectedAt, base, candidate, candidateSHA256, challenge,
	); err != nil || appended {
		t.Fatalf("duplicate selection confirmation appended = %t, %v", appended, err)
	}
	if reconciled, appended, err := recordShadowResearchSelectionFromForward(
		path, selectedAt, candidateSHA256,
	); err != nil || appended || reconciled.CandidateSHA256 != candidateSHA256 {
		t.Fatalf("selection reconciliation = %+v, %t, %v", reconciled, appended, err)
	}

	summary, err := readShadowResearchOutcomeSummary(path)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Version != shadowResearchOutcomeVersion ||
		summary.Status != shadowResearchOutcomeSummaryStatus || !summary.PaperOnly ||
		!summary.AdvisoryOnly || summary.Authorized || summary.Records != 2 ||
		!validLowerSHA256(summary.ChainHeadSHA256) ||
		summary.CandidatesEvaluated != 1 || summary.SelectionsConfirmed != 1 ||
		len(summary.Outcomes) != 1 || !summary.Outcomes[0].SelectionConfirmed ||
		summary.Outcomes[0].SelectedAt == nil ||
		!summary.Outcomes[0].SelectedAt.Equal(selectedAt) {
		t.Fatalf("outcome summary = %+v", summary)
	}
	if summary.Outcomes[0].Receipt.CandidateSHA256 != candidateSHA256 ||
		summary.Outcomes[0].Receipt.ResearchPacketSHA256 != candidate.ResearchPacket.ContentSHA256 ||
		summary.Outcomes[0].Receipt.HypothesisID != candidate.ResearchPacket.HypothesisID ||
		len(summary.Outcomes[0].Receipt.ParameterChanges) == 0 {
		t.Fatalf("outcome lineage = %+v", summary.Outcomes[0].Receipt)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{root, "https://", "verified_facts", "\"policy\":", "\"thesis\":"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("outcome summary leaked %q: %s", forbidden, encoded)
		}
	}
	var output bytes.Buffer
	if err := run([]string{"shadow", "research-outcomes", "--journal", path}, &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte("\"advisory_only\":true")) ||
		!bytes.Contains(output.Bytes(), []byte("\"authorized\":false")) {
		t.Fatalf("summary output = %s", output.Bytes())
	}
	output.Reset()
	promptPolicyPath := writeShadowPolicy(t, base)
	if err := runShadowResearchOutcomeSummaryWith([]string{
		"--journal", path, "--prompt-safe", "--policy", promptPolicyPath,
		"--max-age", "168h",
	}, &output, func() time.Time { return evaluatedAt.Add(time.Hour) }); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"status":"research_outcome_learning_hints"`, `"paper_only":true`,
		`"advisory_only":true`, `"authorized":false`, `"market":"SOL/USDC"`,
		`"state":"selected"`, `"parameter_changes"`,
	} {
		if !bytes.Contains(output.Bytes(), []byte(want)) {
			t.Errorf("prompt-safe summary omitted %q: %s", want, output.Bytes())
		}
	}
	for _, forbidden := range []string{
		"sha256", "hypothesis_id", "evaluated_at", "selected_at", "complete_days",
		"round_trips", "daily_wins", "advantage", "records", "candidates_evaluated",
		"selections_confirmed", "hint_count", "current_context", promptPolicyPath,
		candidateSHA256, candidate.ResearchPacket.ContentSHA256, receipt.BasePolicySHA256,
	} {
		if bytes.Contains(output.Bytes(), []byte(forbidden)) {
			t.Errorf("prompt-safe summary leaked %q: %s", forbidden, output.Bytes())
		}
	}
}

func TestPromptSafeResearchOutcomesUseOnlyFreshCurrentPolicyHints(t *testing.T) {
	policy := adaptiveShadowSearchPolicy()
	policyPath := writeShadowPolicy(t, policy)
	policySHA256, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 4, 18, 0, 0, 0, time.UTC)
	path := filepath.Join(privateTestDirectory(t), "context-filtered.jsonl")
	receipts := []shadowResearchOutcomeReceipt{
		shadowResearchOutcomeReceiptFixture(),
		shadowResearchOutcomeReceiptFixture(),
		shadowResearchOutcomeReceiptFixture(),
		shadowResearchOutcomeReceiptFixture(),
	}
	candidateDigits := []string{"1", "2", "3", "4"}
	policyDigits := []string{"5", "6", "7", "8"}
	for index := range receipts {
		receipts[index].CandidateSHA256 = strings.Repeat(candidateDigits[index], 64)
		receipts[index].CandidatePolicySHA256 = strings.Repeat(policyDigits[index], 64)
		receipts[index].BasePolicySHA256 = policySHA256
		receipts[index].ParameterChanges[0].Proposed = uint64(300 + index*100)
	}
	receipts[2].BasePolicySHA256 = strings.Repeat("e", 64)
	receipts[3].Market = "JUP/USDC"
	for index, at := range []time.Time{
		now.Add(-8 * 24 * time.Hour), now.Add(-3 * time.Hour),
		now.Add(-2 * time.Hour), now.Add(-time.Hour),
	} {
		if appended, appendErr := appendShadowResearchOutcome(
			path, at, shadowResearchForwardEvaluated, receipts[index],
		); appendErr != nil || !appended {
			t.Fatalf("append context outcome %d = %t, %v", index, appended, appendErr)
		}
	}
	var output bytes.Buffer
	args := []string{
		"--journal", path, "--prompt-safe", "--policy", policyPath,
		"--max-age", "168h", "--limit", "1",
	}
	if err := runShadowResearchOutcomeSummaryWith(
		args, &output, func() time.Time { return now },
	); err != nil {
		t.Fatal(err)
	}
	var summary shadowResearchOutcomePromptSummary
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Hints) != 1 || summary.Hints[0].Market != "SOL/USDC" ||
		len(summary.Hints[0].ParameterChanges) != 1 ||
		summary.Hints[0].ParameterChanges[0].Proposed != 400 {
		t.Fatalf("context-filtered prompt summary = %+v", summary)
	}
	for _, invalid := range [][]string{
		{"--journal", path, "--prompt-safe", "--max-age", "168h"},
		{"--journal", path, "--prompt-safe", "--policy", policyPath, "--max-age", "0s"},
		{"--journal", path, "--prompt-safe", "--policy", policyPath, "--max-age", "169h"},
	} {
		if err := runShadowResearchOutcomeSummaryWith(
			invalid, io.Discard, func() time.Time { return now },
		); err == nil {
			t.Fatalf("unsafe prompt context was accepted: %v", invalid)
		}
	}
	future := shadowResearchOutcomeReceiptFixture()
	future.BasePolicySHA256 = policySHA256
	future.CandidateSHA256 = strings.Repeat("a", 64)
	future.CandidatePolicySHA256 = strings.Repeat("b", 64)
	if appended, appendErr := appendShadowResearchOutcome(
		path, now.Add(time.Minute), shadowResearchForwardEvaluated, future,
	); appendErr != nil || !appended {
		t.Fatalf("append future outcome = %t, %v", appended, appendErr)
	}
	if err := runShadowResearchOutcomeSummaryWith(
		args, io.Discard, func() time.Time { return now },
	); err == nil || !strings.Contains(err.Error(), "future event") {
		t.Fatalf("future outcome error = %v", err)
	}
	selectedFuturePath := filepath.Join(privateTestDirectory(t), "future-selection.jsonl")
	if appended, appendErr := appendShadowResearchOutcome(
		selectedFuturePath, now.Add(-time.Hour), shadowResearchForwardEvaluated, future,
	); appendErr != nil || !appended {
		t.Fatalf("append selected outcome = %t, %v", appended, appendErr)
	}
	if appended, appendErr := appendShadowResearchOutcome(
		selectedFuturePath, now.Add(time.Minute), shadowResearchSelectionConfirmed, future,
	); appendErr != nil || !appended {
		t.Fatalf("append future selection = %t, %v", appended, appendErr)
	}
	selectedFutureArgs := append([]string(nil), args...)
	selectedFutureArgs[1] = selectedFuturePath
	if err := runShadowResearchOutcomeSummaryWith(
		selectedFutureArgs, io.Discard, func() time.Time { return now },
	); err == nil || !strings.Contains(err.Error(), "future event") {
		t.Fatalf("future selection error = %v", err)
	}
	if err := os.WriteFile(path+".next", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runShadowResearchOutcomeSummaryWith(
		args, io.Discard, func() time.Time { return now },
	); err == nil || !strings.Contains(err.Error(), "incomplete rotation") {
		t.Fatalf("incomplete outcome rotation error = %v", err)
	}
}

func TestShadowResearchOutcomeSummaryLimitReturnsTheLatestCandidates(t *testing.T) {
	path := filepath.Join(privateTestDirectory(t), "limited.jsonl")
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	for index, character := range []string{"1", "2", "3"} {
		receipt := shadowResearchOutcomeReceiptFixture()
		receipt.CandidateSHA256 = strings.Repeat(character, 64)
		if appended, err := appendShadowResearchOutcome(
			path, now.Add(time.Duration(index)*time.Second), shadowResearchForwardEvaluated, receipt,
		); err != nil || !appended {
			t.Fatalf("append outcome %d = %t, %v", index, appended, err)
		}
	}
	summary, err := readShadowResearchOutcomeSummaryLimit(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Records != 3 || summary.CandidatesEvaluated != 3 ||
		len(summary.Outcomes) != 2 ||
		summary.Outcomes[0].Receipt.CandidateSHA256 != strings.Repeat("2", 64) ||
		summary.Outcomes[1].Receipt.CandidateSHA256 != strings.Repeat("3", 64) ||
		!validLowerSHA256(summary.ChainHeadSHA256) {
		t.Fatalf("limited summary = %+v", summary)
	}
	if err := runShadowResearchOutcomeSummary([]string{
		"--journal", path, "--limit", "0",
	}, io.Discard); err == nil {
		t.Fatal("zero outcome limit was accepted")
	}
}

func TestShadowResearchOutcomeJournalRejectsCollisionsAndOutOfOrderConfirmation(t *testing.T) {
	receipt := shadowResearchOutcomeReceiptFixture()
	root := privateTestDirectory(t)
	path := filepath.Join(root, "research-outcomes.jsonl")
	now := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	appended, err := appendShadowResearchOutcome(
		path, now, shadowResearchForwardEvaluated, receipt,
	)
	if err != nil || !appended {
		t.Fatalf("record forward = %t, %v", appended, err)
	}
	collision := receipt
	collision.CompleteDays++
	if appended, err := appendShadowResearchOutcome(
		path, now.Add(time.Second), shadowResearchForwardEvaluated, collision,
	); err == nil || appended || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("collision append = %t, %v", appended, err)
	}

	otherPath := filepath.Join(root, "missing-forward.jsonl")
	if appended, err := appendShadowResearchOutcome(
		otherPath, now, shadowResearchSelectionConfirmed, receipt,
	); err == nil || appended || !strings.Contains(err.Error(), "lacks") {
		t.Fatalf("out-of-order confirmation = %t, %v", appended, err)
	}

	rejected := receipt
	rejected.ForwardStatus = "challenger_not_qualified"
	rejected.Reasons = []string{"no_strict_daily_majority"}
	if appended, err := appendShadowResearchOutcome(
		filepath.Join(root, "rejected.jsonl"), now,
		shadowResearchSelectionConfirmed, rejected,
	); err == nil || appended || !strings.Contains(err.Error(), "requires a qualified") {
		t.Fatalf("rejected selection confirmation error = %v", err)
	}
}

func TestShadowResearchOutcomeRequiresPacketAndStrictJournalEvents(t *testing.T) {
	base := candidateTestPolicy()
	withoutPacket := candidateForPrices(t, base, 220_000_000, 110_000_000)
	candidateSHA256 := strings.Repeat("a", 64)
	challenge := shadowChallengeResult{}
	if _, _, err := recordShadowResearchForwardOutcome(
		filepath.Join(privateTestDirectory(t), "missing-packet.jsonl"),
		time.Date(2026, 9, 4, 11, 0, 0, 0, time.UTC),
		base, withoutPacket, candidateSHA256, challenge,
	); err == nil {
		t.Fatal("candidate without its research packet was accepted")
	}

	path := filepath.Join(privateTestDirectory(t), "unexpected.jsonl")
	store, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	valid := shadowResearchOutcomeReceiptFixture()
	if _, err := store.Append(
		time.Date(2026, 9, 4, 11, 0, 0, 0, time.UTC),
		shadowResearchForwardEvaluated, valid.CandidateSHA256, valid,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(
		time.Date(2026, 9, 4, 11, 0, 1, 0, time.UTC),
		"research.unexpected", candidateSHA256, map[string]bool{"paper_only": true},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readShadowResearchOutcomeSummary(path); err == nil ||
		!strings.Contains(err.Error(), "unexpected event") {
		t.Fatalf("unexpected event error = %v", err)
	}
	if err := runShadowResearchOutcomeSummaryWith([]string{
		"--journal", path, "--prompt-safe",
		"--policy", writeShadowPolicy(t, adaptiveShadowSearchPolicy()),
		"--max-age", "168h",
	}, io.Discard, func() time.Time {
		return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	}); err == nil || !strings.Contains(err.Error(), "unexpected event") {
		t.Fatalf("prompt-safe malformed event error = %v", err)
	}
}

func shadowResearchOutcomeReceiptFixture() shadowResearchOutcomeReceipt {
	return shadowResearchOutcomeReceipt{
		Version: shadowResearchOutcomeVersion, PaperOnly: true,
		Market: "SOL/USDC", HypothesisID: "bounded-signal-20260902",
		BasePolicySHA256:      strings.Repeat("a", 64),
		ResearchPacketSHA256:  strings.Repeat("b", 64),
		CandidateSHA256:       strings.Repeat("c", 64),
		CandidatePolicySHA256: strings.Repeat("d", 64),
		ParameterChanges: []researchpacket.ParameterChange{{
			Name: "minimum_signal_bps", Current: 150, Proposed: 250,
		}},
		ForwardStatus: "challenger_qualified_for_paper_selection",
		CompleteDays:  7, ChallengerFullRoundTrips: 6, RequiredFullRoundTrips: 4,
		ChallengerDailyWins: 5, RequiredDailyWins: 4,
		AggregateAdvantageMicros: 2_000_000, RequiredAdvantageMicros: 100_000,
	}
}

func shadowResearchOutcomeFixture(
	t *testing.T,
) (shadow.Policy, shadowPaperCandidate, shadowPaperCandidate, string, shadowChallengeResult) {
	t.Helper()
	policy := adaptiveShadowSearchPolicy()
	policy.TickSeconds = 300
	adaptive := *policy.Adaptive
	adaptive.MaxObservationGapSeconds = 600
	adaptive.MaxVolatilityBPS = 5_000
	policy.Adaptive = &adaptive
	policy.InputAmount = 20_000_000
	policy.MinimumOrderValueMicros = 1_000_000
	policy.MaximumOrderValueMicros = 100_000_000
	policyPath := writeShadowPolicy(t, policy)
	root := privateTestDirectory(t)
	journalDir := filepath.Join(root, "journals")
	candidateDir := filepath.Join(root, "challengers")
	for _, directory := range []string{journalDir, candidateDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	prices := []uint64{
		100_000_000, 98_000_000, 96_000_000, 94_000_000, 94_000_000,
		96_000_000, 98_000_000, 100_000_000, 102_000_000, 102_000_000,
	}
	writeShadowResearchWindow(t, journalDir, policy, "2026-08-29", prices)
	challengerPointer := filepath.Join(root, "challenger-pointer")
	championPointer, championRoot, challengerRoot := shadowResearchLifecycle(
		t, root, policyPath, policy,
	)
	champion, _, err := loadSelectedShadowCandidate(championPointer, policy)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := newShadowResearchController(
		policyPath, "", journalDir, candidateDir, challengerPointer,
		championPointer, championRoot, challengerRoot, 100, 64, 7,
	)
	if err != nil {
		t.Fatal(err)
	}
	instructionPath := writeShadowExperimentInstruction(t, root, paperdashboard.Instruction{
		Version:   paperdashboard.InstructionVersion,
		UpdatedAt: time.Date(2026, 8, 29, 23, 0, 0, 0, time.UTC),
		Market:    "all", Preference: "balanced", CadenceSeconds: policy.TickSeconds,
		PaperCapitalMicros: 150_000_000, MinimumOrderMicros: 1_000_000,
		MaximumOrderMicros: 100_000_000, MaxDrawdownBPS: policy.Adaptive.MaxDrawdownBPS,
	})
	controller.experiment, err = loadShadowPaperExperiment(instructionPath, policy)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	days, err := readShadowWalkForwardDays(journalDir, "2026-08-29", policy)
	if err != nil {
		t.Fatal(err)
	}
	var proposed shadow.Policy
	for _, policyCandidate := range adaptiveSearchPolicies(policy) {
		if len(shadowAdaptiveParameterDiff(*policy.Adaptive, *policyCandidate.Adaptive)) == 0 {
			continue
		}
		if _, candidateErr := searchShadowWalkForwardCandidates(
			policy, days, 100, []shadow.Policy{policyCandidate},
		); candidateErr == nil {
			proposed = policyCandidate
			break
		}
	}
	if proposed.Adaptive == nil {
		t.Fatal("fixture has no changed walk-forward candidate")
	}
	packet := boundShadowResearchPacket(t, policy, now, shadowMarketPair(policy))
	packet.CandidateParameterDiff = shadowAdaptiveParameterDiff(*policy.Adaptive, *proposed.Adaptive)
	packet = rehashShadowResearchPacket(t, packet, now)
	controller.researchPacket = &packet
	input := validShadowResearchInput()
	input.ResearchPacketSHA256 = packet.ContentSHA256
	created, err := controller.createCandidate(input, now)
	if err != nil {
		t.Fatal(err)
	}
	candidate, candidateSHA256, err := loadBoundShadowPaperCandidate(
		filepath.Join(candidateDir, created.Artifact), policy,
	)
	if err != nil {
		t.Fatal(err)
	}
	challenge := shadowChallengeResult{
		Version: 1, Status: "challenger_qualified_for_paper_selection",
		PaperOnly: true, EligibleForPaperSelection: true, EvaluationMode: shadow.EvaluationResetDaily,
		CompleteDays: 7, ChallengerFullRoundTrips: 6, RequiredFullRoundTrips: 4,
		ChallengerDailyWins: 5, RequiredDailyWins: 4,
		AggregateAdvantageMicros: 2_000_000, RequiredAggregateAdvantageMicros: 100_000,
		ChallengerCandidateSHA256: candidateSHA256,
		ChallengerPolicySHA256:    candidate.CandidatePolicySHA256,
	}
	return policy, champion, candidate, candidateSHA256, challenge
}
