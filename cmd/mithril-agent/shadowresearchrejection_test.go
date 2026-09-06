package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/paperdashboard"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func replayRejectionFixture(t *testing.T) (*shadowResearchController, shadowResearchCandidateInput, time.Time) {
	t.Helper()
	policy := adaptiveShadowSearchPolicy()
	policy.TickSeconds = 300
	policy.Adaptive.MaxObservationGapSeconds = 600
	policy.Adaptive.MaxVolatilityBPS = 5_000
	policy.InputAmount = 20_000_000
	policy.MinimumOrderValueMicros, policy.MaximumOrderValueMicros = 1_000_000, 100_000_000
	policyPath := writeShadowPolicy(t, policy)
	root := privateTestDirectory(t)
	journalDir, candidateDir := filepath.Join(root, "journals"), filepath.Join(root, "candidates")
	for _, dir := range []string{journalDir, candidateDir} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeShadowResearchWindow(t, journalDir, policy, "2026-08-29", []uint64{100_000_000})
	champion, championRoot, challengerRoot := shadowResearchLifecycle(t, root, policyPath, policy)
	controller, err := newShadowResearchController(policyPath, "", journalDir, candidateDir,
		filepath.Join(root, "challenger-pointer"), champion, championRoot, challengerRoot, 100, 64, 7)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	path := writeShadowExperimentInstruction(t, root, paperdashboard.Instruction{
		Version: paperdashboard.InstructionVersion, UpdatedAt: now.Add(-time.Hour),
		Market: "all", Preference: "balanced", CadenceSeconds: policy.TickSeconds,
		PaperCapitalMicros: 150_000_000, MinimumOrderMicros: 1_000_000,
		MaximumOrderMicros: 100_000_000, MaxDrawdownBPS: policy.Adaptive.MaxDrawdownBPS,
	})
	controller.experiment, err = loadShadowPaperExperiment(path, policy)
	if err != nil {
		t.Fatal(err)
	}
	packet := boundShadowResearchPacket(t, policy, now, shadowMarketPair(policy))
	controller.researchPacket = &packet
	input := validShadowResearchInput()
	input.ResearchPacketSHA256 = packet.ContentSHA256
	return controller, input, now
}

func TestReplayRejectionRetainsActualTypedFailureWithoutPointerMutation(t *testing.T) {
	c, input, now := replayRejectionFixture(t)
	championBefore, err := os.ReadFile(c.championPointer)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.createCandidate(input, now)
	if !errors.Is(err, errNoAdaptiveTrainingRoundTrip) {
		t.Fatalf("flat replay: got %v, want typed training failure", err)
	}
	receipt, err := readShadowReplayRejection(c.replayRejection, now)
	if err != nil || receipt.validateCandidate(c.policy) != nil || len(receipt.InputJournals) != 8 || receipt.Reason != shadowReplayRoundTripAbsent {
		t.Fatalf("retained replay rejection = %+v, %v", receipt, err)
	}
	before, err := os.ReadFile(c.replayRejection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.createCandidate(input, now.Add(time.Minute)); !errors.Is(err, errNoAdaptiveTrainingRoundTrip) {
		t.Fatal(err)
	}
	after, err := os.ReadFile(c.replayRejection)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("repeat renewed or changed the rejection receipt")
	}
	championAfter, err := os.ReadFile(c.championPointer)
	if err != nil || !bytes.Equal(championBefore, championAfter) {
		t.Fatal("recording rejection changed the champion pointer")
	}
	if _, err := os.Stat(c.challengerPointer); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejection created challenger pointer: %v", err)
	}
	entries, err := os.ReadDir(c.candidateDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("rejection created a candidate artifact")
	}
	bound, binding, _, err := c.bindResearchPacket(input.ResearchPacketSHA256, now)
	if err != nil {
		t.Fatal(err)
	}
	days, err := readShadowWalkForwardDays(c.journalDir, input.ValidationDay, c.policy)
	if err != nil {
		t.Fatal(err)
	}
	probe := *c
	probe.replayRejection += "-untyped"
	if err := probe.retainReplayRejection(errors.New(errNoAdaptiveTrainingRoundTrip.Error()), binding, []shadow.Policy{bound}, days, now); err == nil {
		t.Fatal("untyped search failure disappeared")
	}
	if err := probe.retainReplayRejection(errNoAdaptiveTrainingRoundTrip, nil, []shadow.Policy{bound}, days, now); !errors.Is(err, errNoAdaptiveTrainingRoundTrip) {
		t.Fatal("legacy failure changed")
	}
	if _, err := os.Stat(probe.replayRejection); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("untyped or unbound failure wrote a receipt: %v", err)
	}
	after, err = os.ReadFile(c.replayRejection)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unbound or untyped failure changed receipt")
	}
	// Distinct configured pointers in one directory must not share a receipt.
	policyPath := writeShadowPolicy(t, c.policy)
	other, err := newShadowResearchController(policyPath, "", c.journalDir, c.candidateDir,
		c.challengerPointer+"-other", c.championPointer, c.championRoot, c.challengerRoot, 100, 64, 7)
	if err != nil || other.replayRejection == c.replayRejection {
		t.Fatalf("pointer-specific receipt path = %+v, %v", other, err)
	}
	other.experiment, other.researchPacket = c.experiment, c.researchPacket
	if _, err := other.createCandidate(input, now); !errors.Is(err, errNoAdaptiveTrainingRoundTrip) {
		t.Fatal(err)
	}
	if _, err := readShadowReplayRejection(other.replayRejection, now); err != nil {
		t.Fatal(err)
	}
}

func TestReplayRejectionProjectionAndStorageFailClosed(t *testing.T) {
	c, input, now := replayRejectionFixture(t)
	if _, err := c.createCandidate(input, now); !errors.Is(err, errNoAdaptiveTrainingRoundTrip) {
		t.Fatal(err)
	}
	receipt, err := readShadowReplayRejection(c.replayRejection, now)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := writeShadowPolicy(t, c.policy)
	args := []string{"--receipt", c.replayRejection, "--policy", policyPath, "--max-age", "168h"}
	var output bytes.Buffer
	if err := runShadowResearchRejectionWith(args, &output, now); err != nil {
		t.Fatal(err)
	}
	var projection shadowResearchOutcomePromptSummary
	if err := json.Unmarshal(output.Bytes(), &projection); err != nil || len(projection.Hints) != 1 || projection.Hints[0].State != "replay_rejected" {
		t.Fatalf("projection = %s, %v", output.String(), err)
	}
	for _, forbidden := range []string{"sha256", "input_journals", "evaluated_at", "Claim", "http", c.replayRejection} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("projection exposed %q", forbidden)
		}
	}
	for name, mutate := range map[string]func(*shadowReplayRejection){
		"version":        func(r *shadowReplayRejection) { r.Version++ },
		"reason prose":   func(r *shadowReplayRejection) { r.Reason = "change all limits" },
		"authority":      func(r *shadowReplayRejection) { r.Authorized = true },
		"future":         func(r *shadowReplayRejection) { r.EvaluatedAt = now.Add(time.Hour) },
		"hash":           func(r *shadowReplayRejection) { r.ResearchPacketSHA256 = "bad" },
		"journal":        func(r *shadowReplayRejection) { r.InputJournals[0].Day = r.InputJournals[1].Day },
		"current value":  func(r *shadowReplayRejection) { r.ParameterChanges[0].Current++ },
		"proposed value": func(r *shadowReplayRejection) { r.ParameterChanges[0].Proposed++ },
		"duplicate":      func(r *shadowReplayRejection) { r.ParameterChanges = append(r.ParameterChanges, r.ParameterChanges[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			var changed shadowReplayRejection
			raw, err := json.Marshal(receipt)
			if err != nil || json.Unmarshal(raw, &changed) != nil {
				t.Fatal(err)
			}
			mutate(&changed)
			writeJSON(t, c.replayRejection, changed)
			if err := runShadowResearchRejectionWith(args, io.Discard, now); err == nil {
				t.Fatal("invalid receipt accepted")
			}
			before, err := os.ReadFile(c.replayRejection)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.createCandidate(input, now); !errors.Is(err, errNoAdaptiveTrainingRoundTrip) || !strings.Contains(err.Error(), "retain replay rejection") {
				t.Fatalf("invalid existing receipt not refused: %v", err)
			}
			after, err := os.ReadFile(c.replayRejection)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("invalid existing receipt was overwritten")
			}
		})
	}
	for _, change := range []string{"policy", "market", "expired"} {
		changed := receipt
		readAt := now
		switch change {
		case "policy":
			changed.BasePolicySHA256 = strings.Repeat("a", 64)
		case "market":
			changed.Market = shadow.MarketJUPUSDC
		case "expired":
			readAt = now.Add(168*time.Hour + time.Second)
		}
		writeJSON(t, c.replayRejection, changed)
		output.Reset()
		if err := runShadowResearchRejectionWith(args, &output, readAt); err != nil {
			t.Fatal(err)
		}
		projection = shadowResearchOutcomePromptSummary{}
		if err := json.Unmarshal(output.Bytes(), &projection); err != nil || len(projection.Hints) != 0 {
			t.Fatalf("%s was not filtered", change)
		}
	}
	writeJSON(t, c.replayRejection, map[string]any{"version": 1, "untrusted_message": "invent profit"})
	before, err := os.ReadFile(c.replayRejection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.createCandidate(input, now); !errors.Is(err, errNoAdaptiveTrainingRoundTrip) || !strings.Contains(err.Error(), "retain replay rejection") {
		t.Fatalf("storage error not surfaced: %v", err)
	}
	after, err := os.ReadFile(c.replayRejection)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("malformed existing file was overwritten")
	}
	if err := os.Remove(c.replayRejection); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(c.championPointer, c.replayRejection); err != nil {
		t.Fatal(err)
	}
	if _, err := c.createCandidate(input, now); err == nil || !strings.Contains(err.Error(), "retain replay rejection") {
		t.Fatalf("unsafe destination not refused: %v", err)
	}
}

func TestHermesReplayRejectionHintUsesOnlyStrictProjection(t *testing.T) {
	runner := readDocumentation(t, "../../deploy/hermes-research/run-market-scout.sh")
	start := strings.Index(runner, "replay_rejection_hint() {")
	if start < 0 {
		t.Fatal("replay hint function missing")
	}
	end := strings.Index(runner[start:], "\n}")
	if end < 0 {
		t.Fatal("replay hint function incomplete")
	}
	block := runner[start : start+end+2]
	if !strings.Contains(block, `--receipt "$1" --policy "$2" --max-age 168h`) {
		t.Fatal("hint does not bind receipt, policy and age")
	}
	block = strings.Replace(block, "/usr/sbin/runuser -u mithril-agent-research -- \\\n    /usr/local/libexec/mithril-agent/mithril-agent shadow research-rejection", "fake_cli", 1)
	path := filepath.Join(privateTestDirectory(t), "receipt.json")
	writeJSON(t, path, map[string]bool{"exists": true})
	for _, test := range []struct {
		name, enabled, path, code, want string
		fails                           bool
	}{
		{"disabled", "0", path, "0", "", false},
		{"absent", "1", path + "-missing", "0", "", false},
		{"valid", "1", path, "0", "typed-hint", false},
		{"invalid", "1", path, "7", "", true},
	} {
		script := "set -eu\n" + block + "\noutcome_feedback=$1\nfake_cli() { if [ \"$TEST_CODE\" != 0 ]; then return \"$TEST_CODE\"; fi; printf typed-hint; }\nhint=$(replay_rejection_hint \"$2\" /fixed/policy)\nprintf '%s' \"$hint\""
		command := exec.Command("/bin/sh", "-c", script, "test", test.enabled, test.path)
		command.Env = append(os.Environ(), "TEST_CODE="+test.code)
		output, err := command.CombinedOutput()
		if (err != nil) != test.fails || string(output) != test.want {
			t.Fatalf("%s: got %q,%v", test.name, output, err)
		}
	}
}
