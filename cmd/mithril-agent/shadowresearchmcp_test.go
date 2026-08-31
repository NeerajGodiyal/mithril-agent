package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestShadowResearchMCPWritesOnlyAnImmutablePaperChallenger(t *testing.T) {
	policy := validShadowResearchPolicy()
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
		220_000_000, 220_000_000, 110_000_000, 110_000_000,
		220_000_000, 220_000_000, 110_000_000, 110_000_000,
	}
	writeShadowResearchWindow(t, journalDir, policy, "2026-08-29", prices)
	challengerPointer := filepath.Join(root, "challenger-pointer")
	championPointer, championRoot, challengerRoot := shadowResearchLifecycle(t, root, policyPath, policy)
	controller, err := newShadowResearchController(
		policyPath, "", journalDir, candidateDir, challengerPointer,
		championPointer, championRoot, challengerRoot, 100, 64, 7,
	)
	if err != nil {
		t.Fatal(err)
	}
	controller.now = func() time.Time {
		return time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	}

	pointerPath := championPointer
	pointerBefore, err := os.ReadFile(pointerPath)
	if err != nil {
		t.Fatal(err)
	}

	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- serveShadowResearchMCP(ctx, controller, serverReader, serverWriter)
	}()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.IOTransport{
		Reader: clientReader, Writer: clientWriter,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 2 {
		t.Fatalf("paper tools = %+v, %v", tools, err)
	}
	var tool, statusTool *mcpsdk.Tool
	for _, listed := range tools.Tools {
		switch listed.Name {
		case "mithril_paper_create_challenger":
			tool = listed
		case "mithril_paper_challenge_status":
			statusTool = listed
		}
	}
	if tool == nil || statusTool == nil {
		t.Fatalf("paper tools = %+v", tools.Tools)
	}
	if tool.Annotations == nil || tool.Annotations.ReadOnlyHint ||
		tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint ||
		!tool.Annotations.IdempotentHint || tool.Annotations.OpenWorldHint == nil ||
		*tool.Annotations.OpenWorldHint {
		t.Fatalf("paper tool annotations = %+v", tool.Annotations)
	}
	if statusTool.Annotations == nil || !statusTool.Annotations.ReadOnlyHint ||
		statusTool.Annotations.DestructiveHint == nil || *statusTool.Annotations.DestructiveHint ||
		!statusTool.Annotations.IdempotentHint || statusTool.Annotations.OpenWorldHint == nil ||
		*statusTool.Annotations.OpenWorldHint {
		t.Fatalf("paper status annotations = %+v", statusTool.Annotations)
	}
	schema, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"path", "pointer", "policy", "wallet", "signer", "submitter", "threshold",
	} {
		if bytes.Contains(bytes.ToLower(schema), []byte(forbidden)) {
			t.Fatalf("paper tool input schema exposes forbidden %q: %s", forbidden, schema)
		}
	}
	statusResult, callErr := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "mithril_paper_challenge_status", Arguments: map[string]any{},
	})
	if callErr != nil || statusResult.IsError {
		t.Fatalf("empty paper challenge status = %+v, %v", statusResult, callErr)
	}
	statusEncoded, err := json.Marshal(statusResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var challengeStatus shadowResearchChallengeStatus
	if err := json.Unmarshal(statusEncoded, &challengeStatus); err != nil ||
		challengeStatus.Status != "no_active_challenger" || !challengeStatus.PaperOnly ||
		challengeStatus.ChallengerPointerUpdated || challengeStatus.ChampionPointerUpdated {
		t.Fatalf("empty paper challenge status = %+v, %v", challengeStatus, err)
	}

	arguments := shadowResearchMCPArguments(t, validShadowResearchInput())
	arguments["champion_pointer"] = pointerPath
	result, callErr := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "mithril_paper_create_challenger", Arguments: arguments,
	})
	if callErr == nil && !result.IsError {
		t.Fatalf("unknown champion pointer input was accepted: %+v", result)
	}
	if entries, err := os.ReadDir(candidateDir); err != nil || len(entries) != 0 {
		t.Fatalf("rejected input wrote candidates: %v, %v", entries, err)
	}

	result, callErr = session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "mithril_paper_create_challenger",
		Arguments: shadowResearchMCPArguments(t, validShadowResearchInput()),
	})
	if callErr != nil || result.IsError {
		t.Fatalf("create paper challenger = %+v, %v", result, callErr)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var receipt shadowResearchCandidateReceipt
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "paper_challenger_ready" || receipt.Authorized ||
		receipt.Promotable || !receipt.PaperOnly || !receipt.ForwardEvidenceRequired ||
		!receipt.ChallengerPointerUpdated || receipt.ChampionPointerUpdated ||
		receipt.ArtifactSHA256 == "" || receipt.Research.WalkForward == nil ||
		len(receipt.Research.WalkForward.Folds) != shadowWalkForwardWindows ||
		filepath.Base(receipt.Artifact) != receipt.Artifact || strings.Contains(receipt.Artifact, "..") {
		t.Fatalf("paper receipt = %+v", receipt)
	}
	candidatePath := filepath.Join(candidateDir, receipt.Artifact)
	candidate, digest, err := loadBoundShadowPaperCandidate(candidatePath, policy)
	if err != nil {
		t.Fatal(err)
	}
	if digest != receipt.ArtifactSHA256 || candidate.Authorized || candidate.Promotable ||
		!candidate.PaperOnly || candidate.Hypothesis == nil ||
		candidate.Hypothesis.Thesis != validShadowResearchInput().Hypothesis.Thesis {
		t.Fatalf("paper candidate = %+v digest=%s", candidate, digest)
	}
	boundDays := make(map[string]struct{})
	for _, fold := range candidate.Research.WalkForward.Folds {
		boundDays[fold.TrainingJournal.ChainHeadSHA256] = struct{}{}
		boundDays[fold.ValidationJournal.ChainHeadSHA256] = struct{}{}
	}
	if len(boundDays) != shadowWalkForwardWindows+1 {
		t.Fatalf("walk-forward candidate bound %d distinct daily chain heads", len(boundDays))
	}
	tampered := candidate
	admission := *candidate.Research.WalkForward
	admission.Folds = append([]shadowWalkForwardFold(nil), admission.Folds...)
	admission.Folds[0].CandidatePolicySHA256 = strings.Repeat("0", 64)
	tampered.Research.WalkForward = &admission
	if err := tampered.validateAgainst(policy); err == nil {
		t.Fatal("walk-forward candidate accepted a tampered fold fingerprint")
	}
	selected, selectedPath, err := loadSelectedShadowCandidate(challengerPointer, policy)
	if err != nil || selectedPath != candidatePath ||
		selected.CandidatePolicySHA256 != candidate.CandidatePolicySHA256 {
		t.Fatalf("paper challenger pointer = %+v %q, %v", selected, selectedPath, err)
	}
	pointerAfter, err := os.ReadFile(pointerPath)
	if err != nil || !bytes.Equal(pointerBefore, pointerAfter) {
		t.Fatalf("paper MCP changed champion pointer: %q, %v", pointerAfter, err)
	}
	entries, err := os.ReadDir(candidateDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != receipt.Artifact {
		t.Fatalf("candidate directory = %v, %v", entries, err)
	}
	statusResult, callErr = session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "mithril_paper_challenge_status", Arguments: map[string]any{},
	})
	if callErr != nil || statusResult.IsError {
		t.Fatalf("pending paper challenge status = %+v, %v", statusResult, callErr)
	}
	statusEncoded, err = json.Marshal(statusResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(statusEncoded, &challengeStatus); err != nil ||
		challengeStatus.Status != "challenger_evidence_pending" ||
		challengeStatus.ActiveArtifact != receipt.Artifact ||
		challengeStatus.ChallengerPointerUpdated || challengeStatus.ChampionPointerUpdated ||
		bytes.Contains(statusEncoded, []byte(root)) {
		t.Fatalf("pending paper challenge status = %+v, %v", challengeStatus, err)
	}

	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = clientWriter.Close()
	_ = clientReader.Close()
	_ = serverReader.Close()
	_ = serverWriter.Close()
	select {
	case err := <-serverErrors:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shadow research MCP server did not stop")
	}
}

func TestShadowResearchControllerRejectsTraversalSymlinksAndUntrustedResearch(t *testing.T) {
	policy := validShadowResearchPolicy()
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
		220_000_000, 220_000_000, 110_000_000, 110_000_000,
		220_000_000, 220_000_000, 110_000_000, 110_000_000,
	}
	writeShadowResearchDay(t, journalDir, policy, "2026-08-29", prices)
	challengerPointer := filepath.Join(root, "challenger-pointer")
	championPointer, championRoot, challengerRoot := shadowResearchLifecycle(t, root, policyPath, policy)
	controller, err := newShadowResearchController(
		policyPath, "", journalDir, candidateDir, challengerPointer,
		championPointer, championRoot, challengerRoot, 100, 64, 7,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.createCandidate(
		validShadowResearchInput(), time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC),
	); err == nil || !strings.Contains(err.Error(), "2026-08-22") {
		t.Fatalf("missing first walk-forward day error = %v", err)
	}

	input := validShadowResearchInput()
	input.TrainDay = "../../2026-08-28"
	if _, err := controller.createCandidate(input, time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("traversal-shaped training day was accepted")
	}
	input = validShadowResearchInput()
	input.Hypothesis.Authorized = true
	if _, err := controller.createCandidate(input, time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("authorized research hypothesis was accepted")
	}
	input = validShadowResearchInput()
	input.Hypothesis.Sources[0].URL = "file:///private/key"
	if _, err := controller.createCandidate(input, time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("non-HTTPS research source was accepted")
	}
	input = validShadowResearchInput()
	input.Hypothesis.Sources[0].URL = "https://example.com/looks-official"
	if _, err := controller.createCandidate(input, time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("unapproved research source was accepted")
	}
	input = validShadowResearchInput()
	input.Hypothesis.Sources[0].URL = "https://github.com/random-owner/market-claim"
	if _, err := controller.createCandidate(input, time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("unapproved GitHub owner was accepted as a primary source")
	}
	input = validShadowResearchInput()
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	input.Hypothesis.Sources[0].ObservedAt = now.Add(time.Nanosecond)
	if _, err := controller.createCandidate(input, now); err == nil ||
		!strings.Contains(err.Error(), "future") {
		t.Fatalf("future-dated research source error = %v", err)
	}

	realCandidateDir := filepath.Join(root, "real-candidates")
	if err := os.Mkdir(realCandidateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedCandidateDir := filepath.Join(root, "linked-candidates")
	if err := os.Symlink(realCandidateDir, linkedCandidateDir); err != nil {
		t.Fatal(err)
	}
	if _, err := newShadowResearchController(
		policyPath, "", journalDir, linkedCandidateDir, challengerPointer,
		championPointer, championRoot, challengerRoot, 100, 64, 7,
	); err == nil {
		t.Fatal("symlinked candidate root was accepted")
	}
	linkedPointer := filepath.Join(root, "linked-pointer")
	if err := os.Symlink(filepath.Join(root, "pointer-target"), linkedPointer); err != nil {
		t.Fatal(err)
	}
	if _, err := newShadowResearchController(
		policyPath, "", journalDir, candidateDir, linkedPointer,
		championPointer, championRoot, challengerRoot, 100, 64, 7,
	); err == nil {
		t.Fatal("symlinked challenger pointer was accepted")
	}
	if _, err := newShadowResearchController(
		policyPath, "", journalDir, candidateDir,
		filepath.Join(candidateDir, "pointer"),
		championPointer, championRoot, challengerRoot, 100, 64, 7,
	); err == nil {
		t.Fatal("challenger pointer inside candidate root was accepted")
	}
	if _, err := newShadowResearchController(
		policyPath, "", journalDir, candidateDir, policyPath,
		championPointer, championRoot, challengerRoot, 100, 64, 7,
	); err == nil {
		t.Fatal("policy path was accepted as the challenger pointer")
	}
	if _, err := newShadowResearchController(
		policyPath, "", journalDir, candidateDir, challengerPointer,
		championPointer, championRoot, challengerRoot,
		100, shadowResearchMaxCandidates+1, 7,
	); err == nil {
		t.Fatal("unbounded candidate quota was accepted")
	}

	sourceDir := filepath.Join(root, "source-journals")
	if err := os.Mkdir(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for day := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC); day.Before(time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)); day = day.AddDate(0, 0, 1) {
		writeShadowResearchDay(t, journalDir, policy, day.Format("2006-01-02"), prices)
	}
	writeShadowResearchDay(t, sourceDir, policy, "2026-08-28", prices)
	if err := os.Symlink(
		filepath.Join(sourceDir, "shadow-2026-08-28.jsonl"),
		filepath.Join(journalDir, "shadow-2026-08-28.jsonl"),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.createCandidate(
		validShadowResearchInput(), time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC),
	); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked journal error = %v", err)
	}
	if entries, err := os.ReadDir(candidateDir); err != nil || len(entries) != 0 {
		t.Fatalf("rejected research wrote candidates: %v, %v", entries, err)
	}
}

func TestShadowResearchControllerIsIdempotentAndEnforcesCandidateQuota(t *testing.T) {
	policy := validShadowResearchPolicy()
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
		220_000_000, 220_000_000, 110_000_000, 110_000_000,
		220_000_000, 220_000_000, 110_000_000, 110_000_000,
	}
	writeShadowResearchWindow(t, journalDir, policy, "2026-08-29", prices)
	challengerPointer := filepath.Join(root, "challenger-pointer")
	championPointer, championRoot, challengerRoot := shadowResearchLifecycle(t, root, policyPath, policy)
	controller, err := newShadowResearchController(
		policyPath, "", journalDir, candidateDir, challengerPointer,
		championPointer, championRoot, challengerRoot, 100, 1, 7,
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	first, err := controller.createCandidate(validShadowResearchInput(), now)
	if err != nil {
		t.Fatal(err)
	}
	pointerBefore, err := os.ReadFile(challengerPointer)
	if err != nil {
		t.Fatal(err)
	}
	pointerInfoBefore, err := os.Stat(challengerPointer)
	if err != nil {
		t.Fatal(err)
	}
	second, err := controller.createCandidate(validShadowResearchInput(), now)
	if err != nil || first.Artifact != second.Artifact ||
		first.ArtifactSHA256 != second.ArtifactSHA256 ||
		!first.ChallengerPointerUpdated || second.ChallengerPointerUpdated {
		t.Fatalf("idempotent paper challenger = %+v, %+v, %v", first, second, err)
	}
	pointerInfoAfter, err := os.Stat(challengerPointer)
	if err != nil || !os.SameFile(pointerInfoBefore, pointerInfoAfter) {
		t.Fatalf("idempotent request replaced its pointer: %v", err)
	}

	unique := validShadowResearchInput()
	unique.Hypothesis.Thesis = "A different bounded paper hypothesis."
	if _, err := controller.createCandidate(unique, now); err == nil ||
		!strings.Contains(err.Error(), "evidence_pending") {
		t.Fatalf("pending challenger rotation error = %v", err)
	}
	entries, err := os.ReadDir(candidateDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != first.Artifact {
		t.Fatalf("pending challenger contents = %v, %v", entries, err)
	}
	pointerAfter, err := os.ReadFile(challengerPointer)
	if err != nil || !bytes.Equal(pointerBefore, pointerAfter) {
		t.Fatalf("pending challenge changed challenger pointer: %q, %v", pointerAfter, err)
	}
	if err := os.Remove(challengerPointer); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.createCandidate(unique, now); err == nil {
		t.Fatal("candidate quota allowed a second unique artifact")
	}
	if entries, err := os.ReadDir(candidateDir); err != nil || len(entries) != 1 {
		t.Fatalf("candidate quota contents = %v, %v", entries, err)
	}
	if _, err := os.Lstat(challengerPointer); !os.IsNotExist(err) {
		t.Fatalf("quota failure created a challenger pointer: %v", err)
	}
}

func TestShadowResearchRejectsWrappedDaysAndDuplicatePointerRace(t *testing.T) {
	tooManyDays := strconv.FormatUint(uint64(^uint32(0))+7, 10)
	err := runShadowResearchMCP(t.Context(), []string{
		"--policy", "/policy", "--journal-dir", "/journals",
		"--candidate-dir", "/candidates", "--challenger-pointer", "/challenger",
		"--champion-pointer", "/champion", "--champion-dir", "/champion-run",
		"--challenger-dir", "/challenger-run", "--challenge-days", tooManyDays,
	}, io.NopCloser(strings.NewReader("")), io.Discard)
	if err == nil {
		t.Fatal("wrapped challenge day count was accepted")
	}

	policy := validShadowResearchPolicy()
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
		220_000_000, 220_000_000, 110_000_000, 110_000_000,
		220_000_000, 220_000_000, 110_000_000, 110_000_000,
	}
	writeShadowResearchWindow(t, journalDir, policy, "2026-08-29", prices)
	challengerPointer := filepath.Join(root, "challenger-pointer")
	championPointer, championRoot, challengerRoot := shadowResearchLifecycle(t, root, policyPath, policy)
	controller, err := newShadowResearchController(
		policyPath, "", journalDir, candidateDir, challengerPointer,
		championPointer, championRoot, challengerRoot, 100, 1, 7,
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	if _, err := controller.createCandidate(validShadowResearchInput(), now); err != nil {
		t.Fatal(err)
	}
	previousHook := shadowResearchAfterCandidateArtifact
	shadowResearchAfterCandidateArtifact = func() {
		if err := os.WriteFile(challengerPointer, []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { shadowResearchAfterCandidateArtifact = previousHook })
	if _, err := controller.createCandidate(validShadowResearchInput(), now); err == nil ||
		!strings.Contains(err.Error(), "pointer changed") {
		t.Fatalf("duplicate pointer race error = %v", err)
	}
}

func TestShadowResearchPointerPublicationFailsClosedAndCanRetry(t *testing.T) {
	policy := validShadowResearchPolicy()
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
		220_000_000, 220_000_000, 110_000_000, 110_000_000,
		220_000_000, 220_000_000, 110_000_000, 110_000_000,
	}
	writeShadowResearchWindow(t, journalDir, policy, "2026-08-29", prices)
	challengerPointer := filepath.Join(root, "challenger-pointer")
	championPointer, championRoot, challengerRoot := shadowResearchLifecycle(t, root, policyPath, policy)
	controller, err := newShadowResearchController(
		policyPath, "", journalDir, candidateDir, challengerPointer,
		championPointer, championRoot, challengerRoot, 100, 1, 7,
	)
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "victim")
	victimBefore := []byte("must remain unchanged\n")
	if err := os.WriteFile(victim, victimBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, challengerPointer); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	if _, err := controller.createCandidate(validShadowResearchInput(), now); err == nil ||
		!strings.Contains(err.Error(), "pointer") {
		t.Fatalf("symlinked pointer publication error = %v", err)
	}
	victimAfter, err := os.ReadFile(victim)
	if err != nil || !bytes.Equal(victimBefore, victimAfter) {
		t.Fatalf("pointer publication wrote through a symlink: %q, %v", victimAfter, err)
	}
	if err := os.Remove(challengerPointer); err != nil {
		t.Fatal(err)
	}
	receipt, err := controller.createCandidate(validShadowResearchInput(), now)
	if err != nil || !receipt.ChallengerPointerUpdated || receipt.ChampionPointerUpdated {
		t.Fatalf("retry exact orphaned challenger = %+v, %v", receipt, err)
	}
	entries, err := os.ReadDir(candidateDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("publication retry candidates = %v, %v", entries, err)
	}
	if _, selectedPath, err := loadSelectedShadowCandidate(challengerPointer, policy); err != nil ||
		selectedPath != filepath.Join(candidateDir, receipt.Artifact) {
		t.Fatalf("published challenger pointer = %q, %v", selectedPath, err)
	}
}

func TestShadowResearchRotatesOnlyAfterACompleteRejectedChallenge(t *testing.T) {
	policy := validShadowResearchPolicy()
	policy.TickSeconds = 3_600
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
		220_000_000, 220_000_000, 110_000_000, 110_000_000,
		220_000_000, 220_000_000, 110_000_000, 110_000_000,
	}
	writeShadowResearchWindow(t, journalDir, policy, "2026-08-29", prices)
	challengerPointer := filepath.Join(root, "challenger-pointer")
	championPointer, championRoot, challengerRoot := shadowResearchLifecycle(t, root, policyPath, policy)
	controller, err := newShadowResearchController(
		policyPath, "", journalDir, candidateDir, challengerPointer,
		championPointer, championRoot, challengerRoot, 100, 64, 7,
	)
	if err != nil {
		t.Fatal(err)
	}
	selectedAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	first, err := controller.createCandidate(validShadowResearchInput(), selectedAt)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, selection, err := loadBoundShadowCandidateSelection(challengerPointer, policy)
	if err != nil || selection.ChallengeDays != 7 ||
		selection.ChallengeGateVersion != shadowChallengeGateVersion {
		t.Fatalf("frozen challenge selection = %+v, %v", selection, err)
	}
	if _, err := newShadowResearchController(
		policyPath, "", journalDir, candidateDir, challengerPointer,
		championPointer, championRoot, challengerRoot, 100, 64, 8,
	); err == nil || !strings.Contains(err.Error(), "bound research selection") {
		t.Fatalf("changed challenge duration accepted: %v", err)
	}
	champion, _, err := loadSelectedShadowCandidate(championPointer, policy)
	if err != nil {
		t.Fatal(err)
	}
	challenger, _, err := loadSelectedShadowCandidate(challengerPointer, policy)
	if err != nil {
		t.Fatal(err)
	}
	championEvidence := filepath.Join(championRoot, champion.CandidatePolicySHA256)
	challengerEvidence := filepath.Join(challengerRoot, challenger.CandidatePolicySHA256)
	for _, directory := range []string{championEvidence, challengerEvidence} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for remaining := 7; remaining > 0; remaining-- {
		from := selectedAt.AddDate(0, 0, -remaining).Truncate(24 * time.Hour)
		writeCompleteShadowDay(t, championEvidence, champion.Policy, from)
		writeCompleteShadowDay(t, challengerEvidence, challenger.Policy, from)
	}
	if status, err := controller.challengeStatus(selectedAt); err != nil ||
		status.Status != "challenger_evidence_pending" || status.CompleteDays != 0 {
		t.Fatalf("preselection evidence status = %+v, %v", status, err)
	}
	reviewAt := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	for remaining := 7; remaining > 0; remaining-- {
		from := reviewAt.AddDate(0, 0, -remaining).Truncate(24 * time.Hour)
		writeCompleteShadowDay(t, championEvidence, champion.Policy, from)
		writeCompleteShadowDay(t, challengerEvidence, challenger.Policy, from)
	}
	if result, err := evaluateShadowChallenge(
		policy, championPointer, filepath.Join(candidateDir, first.Artifact),
		championRoot, challengerRoot, 7, reviewAt,
		time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
	); err != nil || result.Status != "challenger_not_qualified" {
		t.Fatalf("direct rejected challenge = %+v, %v", result, err)
	}
	// Day eight cannot change the completed first evidence window. This also
	// keeps a qualified result retained for the operator instead of allowing a
	// later bad day to make it look rejected and replaceable.
	writeCompleteShadowDay(t, championEvidence, champion.Policy, reviewAt.Truncate(24*time.Hour))
	writeCompleteShadowDay(t, challengerEvidence, challenger.Policy, reviewAt.Truncate(24*time.Hour))
	laterResult, err := evaluateShadowChallenge(
		policy, championPointer, filepath.Join(candidateDir, first.Artifact),
		championRoot, challengerRoot, 7, reviewAt.Add(24*time.Hour),
		time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
	)
	if err != nil || laterResult.Status != "challenger_not_qualified" ||
		!laterResult.From.Equal(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)) ||
		!laterResult.To.Equal(reviewAt.Truncate(24*time.Hour)) {
		t.Fatalf("fixed challenge window drifted on day eight: %+v, %v", laterResult, err)
	}
	status, err := controller.challengeStatus(reviewAt)
	if err != nil || status.Status != "challenger_not_qualified" ||
		status.CompleteDays != 7 || status.ChallengerPointerUpdated ||
		status.ChampionPointerUpdated {
		t.Fatalf("complete rejected challenge = %+v, %v", status, err)
	}
	championBefore, err := os.ReadFile(championPointer)
	if err != nil {
		t.Fatal(err)
	}
	next := validShadowResearchInput()
	next.Hypothesis.Thesis = "Rotate only after the preceding challenger completed and failed its fixed gate."
	writeShadowResearchWindow(t, journalDir, policy, "2026-09-06", prices)
	next.TrainDay, next.ValidationDay = "2026-09-05", "2026-09-06"
	second, err := controller.createCandidate(next, reviewAt)
	if err != nil || second.Artifact == first.Artifact {
		t.Fatalf("rejected challenger rotation = %+v, %v", second, err)
	}
	if _, selectedPath, err := loadSelectedShadowCandidate(challengerPointer, policy); err != nil ||
		selectedPath != filepath.Join(candidateDir, second.Artifact) {
		t.Fatalf("rotated paper challenger = %q, %v", selectedPath, err)
	}
	championAfter, err := os.ReadFile(championPointer)
	if err != nil || !bytes.Equal(championBefore, championAfter) {
		t.Fatalf("challenger rotation changed champion: %q, %v", championAfter, err)
	}
}

func TestShadowResearchRetainsQualifiedChallengerUntilPaperSelection(t *testing.T) {
	if err := validateShadowResearchRotationStatus(
		"challenger_qualified_for_paper_selection",
	); err == nil || !strings.Contains(err.Error(), "retained") {
		t.Fatalf("qualified challenger retention error = %v", err)
	}
	if err := validateShadowResearchRotationStatus("challenger_evidence_pending"); err == nil {
		t.Fatal("pending challenger was allowed to rotate")
	}
	if err := validateShadowResearchRotationStatus("challenger_not_qualified"); err != nil {
		t.Fatalf("rejected challenger could not rotate: %v", err)
	}
	if err := validateShadowResearchRotationStatus("challenger_selected_as_paper_champion"); err != nil {
		t.Fatalf("paper-selected challenger could not rotate: %v", err)
	}
}

func TestShadowResearchRecognizesPaperSelectionBeforeRotation(t *testing.T) {
	policy := validShadowResearchPolicy()
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
		220_000_000, 220_000_000, 110_000_000, 110_000_000,
		220_000_000, 220_000_000, 110_000_000, 110_000_000,
	}
	writeShadowResearchWindow(t, journalDir, policy, "2026-08-29", prices)
	challengerPointer := filepath.Join(root, "challenger-pointer")
	championPointer, championRoot, challengerRoot := shadowResearchLifecycle(t, root, policyPath, policy)
	controller, err := newShadowResearchController(
		policyPath, "", journalDir, candidateDir, challengerPointer,
		championPointer, championRoot, challengerRoot, 100, 64, 7,
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	first, err := controller.createCandidate(validShadowResearchInput(), now)
	if err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(candidateDir, first.Artifact)
	activeCandidate, _, err := loadBoundShadowPaperCandidate(firstPath, policy)
	if err != nil {
		t.Fatal(err)
	}
	differentPath := filepath.Join(root, "same-policy-different-artifact.json")
	activeCandidate.Hypothesis.Thesis = "Same thresholds do not make this a byte-identical selected research artifact."
	if err := writeShadowPaperCandidate(differentPath, activeCandidate); err != nil {
		t.Fatal(err)
	}
	if err := runShadowSelect([]string{
		"--policy", policyPath, "--candidate", differentPath, "--pointer", championPointer,
		"--lifecycle-lock", filepath.Join(root, "lifecycle.lock"),
	}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if status, err := controller.challengeStatus(now); err == nil ||
		status.Status == "challenger_selected_as_paper_champion" ||
		!strings.Contains(err.Error(), "artifact digest differs") {
		t.Fatalf("different artifact was mistaken for paper selection: %+v, %v", status, err)
	}

	selectedPath := filepath.Join(root, "selected.json")
	contents, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(selectedPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runShadowSelect([]string{
		"--policy", policyPath, "--candidate", selectedPath, "--pointer", championPointer,
		"--lifecycle-lock", filepath.Join(root, "lifecycle.lock"),
	}, io.Discard); err != nil {
		t.Fatal(err)
	}
	status, err := controller.challengeStatus(now)
	if err != nil || status.Status != "challenger_selected_as_paper_champion" ||
		status.ActiveArtifact != first.Artifact || status.ChampionPointerUpdated {
		t.Fatalf("paper selection status = %+v, %v", status, err)
	}
	retry, err := controller.createCandidate(validShadowResearchInput(), now)
	if err != nil || retry.Artifact != first.Artifact || retry.ChallengerPointerUpdated {
		t.Fatalf("post-selection exact retry = %+v, %v", retry, err)
	}
	next := validShadowResearchInput()
	next.Hypothesis.Thesis = "Prepare a new challenger only after the prior artifact became paper champion."
	pointerBefore, err := os.ReadFile(challengerPointer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.createCandidate(next, now); err == nil ||
		!strings.Contains(err.Error(), "identical to the current champion") {
		t.Fatalf("same-policy challenger was not rejected: %v", err)
	}
	pointerAfter, err := os.ReadFile(challengerPointer)
	if err != nil || !bytes.Equal(pointerBefore, pointerAfter) {
		t.Fatalf("same-policy request changed the challenger pointer: %v", err)
	}

	newPrices := []uint64{
		230_000_000, 230_000_000, 105_000_000, 105_000_000,
		230_000_000, 230_000_000, 105_000_000, 105_000_000,
	}
	writeShadowResearchDay(t, journalDir, policy, "2026-08-30", newPrices)
	writeShadowResearchDay(t, journalDir, policy, "2026-08-31", newPrices)
	next.TrainDay, next.ValidationDay = "2026-08-30", "2026-08-31"
	second, err := controller.createCandidate(
		next, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	)
	if err != nil || second.Artifact == first.Artifact || !second.ChallengerPointerUpdated {
		t.Fatalf("post-selection rotation = %+v, %v", second, err)
	}
}

func validShadowResearchPolicy() shadow.Policy {
	policy := validShadowPolicy()
	policy.TickSeconds = 3_600
	return policy
}

func writeShadowResearchWindow(
	t *testing.T, directory string, policy shadow.Policy, finalDay string, prices []uint64,
) {
	t.Helper()
	end, err := time.Parse("2006-01-02", finalDay)
	if err != nil {
		t.Fatal(err)
	}
	for offset := -shadowWalkForwardWindows; offset <= 0; offset++ {
		writeShadowResearchDay(
			t, directory, policy, end.AddDate(0, 0, offset).Format("2006-01-02"), prices,
		)
	}
}

func TestShadowResearchRequiresCoverageAcrossTheWholeUTCDay(t *testing.T) {
	for _, test := range []struct {
		name    string
		first   time.Duration
		spacing time.Duration
		count   int
	}{
		{name: "late start", first: 12 * time.Hour, spacing: time.Hour, count: 12},
		{name: "clustered start", first: time.Minute, spacing: time.Minute, count: 23},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := validShadowResearchPolicy()
			root := privateTestDirectory(t)
			end, err := time.Parse("2006-01-02", "2026-08-29")
			if err != nil {
				t.Fatal(err)
			}
			prices := []uint64{220_000_000, 110_000_000}
			for offset := -shadowWalkForwardWindows; offset <= 0; offset++ {
				day := end.AddDate(0, 0, offset).Format("2006-01-02")
				if offset == -shadowWalkForwardWindows {
					writeShadowResearchSparseDay(
						t, root, policy, day, test.first, test.spacing, test.count,
					)
				} else {
					writeShadowResearchDay(t, root, policy, day, prices)
				}
			}
			if _, err := readShadowWalkForwardDays(root, "2026-08-29", policy); err == nil ||
				!strings.Contains(err.Error(), "2026-08-22 lacks 95% observable coverage") {
				t.Fatalf("sparse closed day error = %v", err)
			}
		})
	}
}

func writeShadowResearchSparseDay(
	t *testing.T, directory string, policy shadow.Policy, day string,
	first, spacing time.Duration, count int,
) {
	t.Helper()
	start, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(filepath.Join(directory, "shadow-"+day+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	firstAt := start.Add(first)
	if _, err := store.Append(firstAt, shadow.EventOpened, "", shadow.Opening{
		Version: shadow.JournalVersionFor(policy), PolicySHA256: fingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	var book shadow.Ledger
	for index := 0; index < count; index++ {
		at := firstAt.Add(time.Duration(index) * spacing)
		price := uint64(110_000_000)
		if index == 0 {
			book, err = shadow.NewLedger(policy, price)
		} else {
			book, err = book.Mark(price)
		}
		if err != nil {
			t.Fatal(err)
		}
		equity, err := book.EquityMicros(price)
		if err != nil {
			t.Fatal(err)
		}
		primary, secondary := shadowSearchSamples(policy, price, at)
		tick := shadow.Tick{
			At: at, Event: shadow.EventWaiting, PriceMicros: price, EquityMicros: equity,
			QuoteLowerMicros: policy.QuotePeg.MinimumMicros,
			QuoteUpperMicros: policy.QuotePeg.MaximumMicros,
			PrimaryPrice:     &primary, SecondaryPrice: &secondary,
		}
		if _, err := store.Append(at, tick.Event, "", tick); err != nil {
			t.Fatal(err)
		}
	}
	closeAt := start.Add(24*time.Hour - time.Nanosecond)
	if _, err := store.Append(closeAt, shadow.EventClosed, "", shadow.Tick{
		At: closeAt, Event: shadow.EventClosed, PeriodClose: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func writeShadowResearchDay(
	t *testing.T, directory string, policy shadow.Policy, day string, prices []uint64,
) {
	t.Helper()
	if len(prices) == 0 {
		t.Fatal("research day needs prices")
	}
	count := int(uint64(24*time.Hour/time.Second)/policy.TickSeconds) - 1
	fullDay := make([]uint64, count)
	for index := range fullDay {
		fullDay[index] = prices[index%len(prices)]
	}
	writeShadowSearchDay(t, directory, policy, day, fullDay)
}

func validShadowResearchInput() shadowResearchCandidateInput {
	return shadowResearchCandidateInput{
		Hypothesis: shadowPaperHypothesis{
			Version: shadowPaperHypothesisVersion, Status: "paper_hypothesis", PaperOnly: true,
			Thesis: "Test whether a completed high-low range supports a bounded round trip.",
			Sources: []shadowHypothesisEvidence{{
				URL: "https://solana.com/news/example", ObservedAt: time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
				Summary: "The cited market change is advisory and needs paper validation.",
			}},
		},
		TrainDay: "2026-08-28", ValidationDay: "2026-08-29",
	}
}

func TestShadowPaperHypothesisAcceptsOfficialRosterSources(t *testing.T) {
	for _, sourceURL := range []string{
		"https://developers.jup.ag/docs/api-reference/swap/build",
		"https://docs.pyth.network/price-feeds/core/fetch-price-updates",
		"https://docs.kraken.com/exchange/api-reference/spot-websocket-v2/ticker",
		"https://www.helius.dev/docs/laserstream",
		"https://helius.statuspage.io/",
		"https://docs.jito.wtf/lowlatencytxnsend/",
	} {
		input := validShadowResearchInput()
		input.Hypothesis.Sources[0].URL = sourceURL
		if err := input.Hypothesis.validate(); err != nil {
			t.Errorf("official source %q was refused: %v", sourceURL, err)
		}
	}
}

func TestShadowResearchRequiresTheLatestTwoCompletedUTCDays(t *testing.T) {
	now := time.Date(2026, 8, 30, 23, 59, 59, 0, time.UTC)
	valid := validShadowResearchInput()
	if err := valid.validate(now); err != nil {
		t.Fatalf("latest completed pair was refused: %v", err)
	}
	for name, days := range map[string][2]string{
		"stale pair":        {"2026-08-27", "2026-08-28"},
		"cherry picked":     {"2026-08-27", "2026-08-29"},
		"includes today":    {"2026-08-29", "2026-08-30"},
		"includes future":   {"2026-08-30", "2026-08-31"},
		"non calendar date": {"2026-08-28", "2026-08-32"},
	} {
		candidate := valid
		candidate.TrainDay, candidate.ValidationDay = days[0], days[1]
		if err := candidate.validate(now); err == nil {
			t.Errorf("%s was accepted: %v", name, days)
		}
	}
	afterMidnight := valid
	afterMidnight.TrainDay, afterMidnight.ValidationDay = "2026-08-29", "2026-08-30"
	if err := afterMidnight.validate(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("UTC midnight did not advance the completed pair: %v", err)
	}
}

func shadowResearchLifecycle(
	t *testing.T, root, policyPath string, base shadow.Policy,
) (string, string, string) {
	t.Helper()
	champion := candidateForPrices(t, base, 200_000_000, 100_000_000)
	championPath := filepath.Join(root, "champion.json")
	if err := writeShadowPaperCandidate(championPath, champion); err != nil {
		t.Fatal(err)
	}
	championPointer := filepath.Join(root, "champion-pointer")
	if err := runShadowSelect([]string{
		"--policy", policyPath, "--candidate", championPath, "--pointer", championPointer,
		"--lifecycle-lock", filepath.Join(root, "lifecycle.lock"),
	}, io.Discard); err != nil {
		t.Fatal(err)
	}
	championRoot := filepath.Join(root, "champion-run")
	challengerRoot := filepath.Join(root, "challenger-run")
	for _, directory := range []string{championRoot, challengerRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return championPointer, championRoot, challengerRoot
}

func shadowResearchMCPArguments(t *testing.T, input shadowResearchCandidateInput) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var arguments map[string]any
	if err := json.Unmarshal(encoded, &arguments); err != nil {
		t.Fatal(err)
	}
	return arguments
}
