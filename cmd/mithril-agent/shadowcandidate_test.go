package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestShadowSelectWritesPrivatePointerAndLoadsExactCandidate(t *testing.T) {
	root := privateTestDirectory(t)
	base := candidateTestPolicy()
	policyPath := writeShadowPolicy(t, base)
	candidate := candidateForPrices(t, base, 220_000_000, 110_000_000)
	candidatePath := filepath.Join(root, "candidate.json")
	if err := writeShadowPaperCandidate(candidatePath, candidate); err != nil {
		t.Fatal(err)
	}
	pointerPath := filepath.Join(root, "selected")

	var output bytes.Buffer
	if err := run([]string{
		"shadow", "select", "--policy", policyPath,
		"--candidate", candidatePath, "--pointer", pointerPath,
		"--lifecycle-lock", filepath.Join(root, "lifecycle.lock"),
	}, &output); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(pointerPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("pointer mode = %o", info.Mode().Perm())
	}
	loaded, selected, err := loadSelectedShadowCandidate(pointerPath, base)
	if err != nil {
		t.Fatal(err)
	}
	if selected != candidatePath || loaded.CandidatePolicySHA256 != candidate.CandidatePolicySHA256 {
		t.Fatalf("selected %q candidate %+v", selected, loaded)
	}
	var result map[string]any
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["status"] != "paper_candidate_selected" || result["paper_only"] != true ||
		result["authorized"] != false {
		t.Fatalf("selection result = %#v", result)
	}
	replacement := candidateForPrices(t, base, 200_000_000, 100_000_000)
	replacementRaw, err := json.MarshalIndent(replacement, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidatePath, append(replacementRaw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadSelectedShadowCandidate(pointerPath, base); err == nil ||
		!strings.Contains(err.Error(), "no longer matches") {
		t.Fatalf("replaced candidate error = %v", err)
	}

	if err := os.WriteFile(pointerPath, []byte("relative.json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadSelectedShadowCandidate(pointerPath, base); err == nil {
		t.Fatal("relative candidate pointer was accepted")
	}
}

func TestShadowSelectRefusesAPointerThatAliasesTheBasePolicy(t *testing.T) {
	root := privateTestDirectory(t)
	base := candidateTestPolicy()
	originalPolicy := writeShadowPolicy(t, base)
	policyRaw, err := os.ReadFile(originalPolicy)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(root, "policy.json")
	if err := os.WriteFile(policyPath, policyRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := candidateForPrices(t, base, 220_000_000, 110_000_000)
	candidatePath := filepath.Join(root, "candidate.json")
	if err := writeShadowPaperCandidate(candidatePath, candidate); err != nil {
		t.Fatal(err)
	}
	if err := runShadowSelect([]string{
		"--policy", policyPath,
		"--candidate", candidatePath,
		"--pointer", policyPath,
		"--lifecycle-lock", filepath.Join(root, "lifecycle.lock"),
	}, &bytes.Buffer{}); err == nil {
		t.Fatal("shadow select accepted the base policy as its pointer output")
	}
	after, err := os.ReadFile(policyPath)
	if err != nil || !bytes.Equal(policyRaw, after) {
		t.Fatalf("pointer alias changed the base policy: %v", err)
	}
}

func TestShadowSelectInitialCannotReplaceAnExistingChampion(t *testing.T) {
	root := privateTestDirectory(t)
	_, policyPath, evidenceDir, first := initialShadowCandidateFixture(
		t, initialWinningPrices(200_000_000, 100_000_000),
		initialWinningPrices(200_000_000, 100_000_000),
	)
	second := first
	firstPath := filepath.Join(root, "first.json")
	secondPath := filepath.Join(root, "second.json")
	for path, candidate := range map[string]shadowPaperCandidate{
		firstPath: first, secondPath: second,
	} {
		if err := writeShadowPaperCandidate(path, candidate); err != nil {
			t.Fatal(err)
		}
	}
	pointerPath := filepath.Join(root, "active.json")
	lockPath := filepath.Join(root, "lifecycle.lock")
	selectInitial := func(candidatePath string) error {
		return runShadowSelect([]string{
			"--policy", policyPath, "--candidate", candidatePath,
			"--pointer", pointerPath, "--lifecycle-lock", lockPath,
			"--initial", "--evidence-dir", evidenceDir,
		}, &bytes.Buffer{})
	}
	if err := selectInitial(firstPath); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(pointerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := selectInitial(secondPath); err == nil {
		t.Fatalf("second initial selection error = %v", err)
	}
	after, err := os.ReadFile(pointerPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("initial selection replaced the champion: %v", err)
	}
}

func TestShadowSelectInitialRejectsUnsafeEvidence(t *testing.T) {
	for name, test := range map[string]struct {
		training   []uint64
		validation []uint64
		want       string
	}{
		"low coverage": {
			training:   initialWinningPrices(200_000_000, 100_000_000)[:8],
			validation: initialWinningPrices(200_000_000, 100_000_000)[:8],
			want:       "observable coverage",
		},
		"losing validation": {
			training:   initialWinningPrices(200_000_000, 100_000_000),
			validation: initialLosingPrices(200_000_000, 100_000_000),
			want:       "net return is not positive",
		},
		"doubled spread": {
			training:   initialWinningPrices(104_000_000, 100_000_000),
			validation: initialWinningPrices(104_000_000, 100_000_000),
			want:       "at 200 bps",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, policyPath, evidenceDir, candidate := initialShadowCandidateFixture(
				t, test.training, test.validation,
			)
			root := privateTestDirectory(t)
			candidatePath := filepath.Join(root, "candidate.json")
			if err := writeShadowPaperCandidate(candidatePath, candidate); err != nil {
				t.Fatal(err)
			}
			err := runShadowSelect([]string{
				"--policy", policyPath, "--candidate", candidatePath,
				"--pointer", filepath.Join(root, "active.json"),
				"--lifecycle-lock", filepath.Join(root, "lifecycle.lock"),
				"--initial", "--evidence-dir", evidenceDir,
			}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("initial selection error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestShadowSelectInitialRequiresItsEvidenceDirectory(t *testing.T) {
	root := privateTestDirectory(t)
	base := candidateTestPolicy()
	policyPath := writeShadowPolicy(t, base)
	candidate := candidateForPrices(t, base, 200_000_000, 100_000_000)
	candidatePath := filepath.Join(root, "candidate.json")
	if err := writeShadowPaperCandidate(candidatePath, candidate); err != nil {
		t.Fatal(err)
	}
	err := runShadowSelect([]string{
		"--policy", policyPath, "--candidate", candidatePath,
		"--pointer", filepath.Join(root, "active.json"),
		"--lifecycle-lock", filepath.Join(root, "lifecycle.lock"), "--initial",
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--evidence-dir") {
		t.Fatalf("initial selection error = %v", err)
	}
}

func TestInitialShadowDrawdownUsesTheAdaptiveOpeningLossBudget(t *testing.T) {
	policy := adaptiveShadowSearchPolicy()
	adaptive := *policy.Adaptive
	adaptive.MaxDrawdownBPS = 300
	policy.Adaptive = &adaptive
	ledger, err := shadow.NewLedger(policy, 100_000_000)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err = ledger.Mark(90_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if initialShadowDrawdownCompliant(policy, ledger) {
		t.Fatal("accepted a ten-percent drawdown under a three-percent limit")
	}
	adaptive.MaxDrawdownBPS = 5_000
	policy.Adaptive = &adaptive
	if !initialShadowDrawdownCompliant(policy, ledger) {
		t.Fatal("rejected a ten-percent drawdown under a fifty-percent limit")
	}
}

func TestInitialShadowDrawdownUsesTheActualHighWaterMark(t *testing.T) {
	policy := adaptiveShadowSearchPolicy()
	adaptive := *policy.Adaptive
	adaptive.MaxDrawdownBPS = 1_500
	policy.Adaptive = &adaptive
	ledger, err := shadow.NewLedger(policy, 100_000_000)
	if err != nil {
		t.Fatal(err)
	}
	for _, price := range []uint64{200_000_000, 180_000_000} {
		ledger, err = ledger.Mark(price)
		if err != nil {
			t.Fatal(err)
		}
	}
	if ledger.MaxDrawdownBPS != 1_000 || !initialShadowDrawdownCompliant(policy, ledger) {
		t.Fatalf("peak-relative drawdown = %d bps, want a compliant 1000 bps", ledger.MaxDrawdownBPS)
	}
}

func TestShadowRunChangesPaperPolicyOnlyAtBoundary(t *testing.T) {
	root := privateTestDirectory(t)
	base := candidateTestPolicy()
	policyPath := writeShadowPolicy(t, base)
	first := candidateForPrices(t, base, 200_000_000, 100_000_000)
	second := candidateForPrices(t, base, 220_000_000, 110_000_000)
	firstPath := filepath.Join(root, "first.json")
	secondPath := filepath.Join(root, "second.json")
	if err := writeShadowPaperCandidate(firstPath, first); err != nil {
		t.Fatal(err)
	}
	if err := writeShadowPaperCandidate(secondPath, second); err != nil {
		t.Fatal(err)
	}
	pointerPath := filepath.Join(root, "selected")
	selectCandidate := func(path string) {
		t.Helper()
		if err := runShadowSelect([]string{
			"--policy", policyPath, "--candidate", path, "--pointer", pointerPath,
			"--lifecycle-lock", filepath.Join(root, "lifecycle.lock"),
		}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	selectCandidate(firstPath)

	roll, err := newDailyJournal(filepath.Join(root, first.CandidatePolicySHA256))
	if err != nil {
		t.Fatal(err)
	}
	oldDay := time.Date(2026, 8, 27, 23, 59, 0, 0, time.UTC)
	if err := roll.openFor(oldDay); err != nil {
		t.Fatal(err)
	}
	newDay := oldDay.Add(2 * time.Minute)
	run := &shadowRun{
		basePolicy: base, policy: first.Policy, journalRoot: root,
		candidatePointer: pointerPath,
		policySHA256:     first.CandidatePolicySHA256,
		primary:          candidatePriceSource{pricesource.KrakenSOLIdentitySHA256(), newDay},
		secondary:        candidatePriceSource{pricesource.KrakenIdentitySHA256(), newDay},
		quoter:           liveStubQuoter{estimated: 21_525}, roll: roll,
	}
	run.runner, err = run.newRunner()
	if err != nil {
		t.Fatal(err)
	}
	selectCandidate(secondPath)
	if run.policySHA256 != first.CandidatePolicySHA256 {
		t.Fatal("writing the pointer changed the running policy before a boundary")
	}
	run.consecutiveUnavailable, run.dataUnavailable = 3, true
	if err := run.refreshSelectedCandidate(newDay); err != nil {
		t.Fatal(err)
	}
	defer run.roll.Close()
	if run.policySHA256 != second.CandidatePolicySHA256 ||
		run.roll.directory != filepath.Join(root, second.CandidatePolicySHA256) ||
		run.roll.Day() != dayKey(newDay) {
		t.Fatalf("candidate was not isolated at the boundary: %+v", run)
	}
	if run.consecutiveUnavailable != 0 || run.dataUnavailable {
		t.Fatalf("candidate inherited stale outage state: %+v", run)
	}
	if run.policy.Trigger.ThresholdMicros != second.Policy.Trigger.ThresholdMicros ||
		run.policy.ReturnTrigger == nil ||
		run.policy.ReturnTrigger.ThresholdMicros != second.Policy.ReturnTrigger.ThresholdMicros {
		t.Fatalf("active policy = %+v", run.policy)
	}
	stored, err := loadShadowPolicy(filepath.Join(run.roll.directory, "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	storedFingerprint, err := stored.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if storedFingerprint != second.CandidatePolicySHA256 {
		t.Fatalf("stored policy fingerprint = %s", storedFingerprint)
	}
	if _, err := run.runner.Step(t.Context(), newDay); err != nil {
		t.Fatal(err)
	}
	if records := run.roll.Records(); len(records) < 2 || records[0].Type != shadow.EventOpened {
		t.Fatalf("new candidate journal has no opening record: %+v", records)
	}

	selectCandidate(firstPath)
	run.portfolioBound = true
	run.portfolioInstructionSHA256 = strings.Repeat("c", 64)
	run.portfolioPaperCapitalMicros = 270_000_000
	activeFingerprint := run.policySHA256
	if err := run.refreshSelectedCandidate(newDay.Add(24 * time.Hour)); err == nil ||
		!strings.Contains(err.Error(), "instruction does not match") {
		t.Fatalf("unbound boundary candidate error = %v", err)
	}
	if run.policySHA256 != activeFingerprint {
		t.Fatal("unbound boundary candidate changed the active paper policy")
	}

	if err := os.WriteFile(pointerPath, []byte("not-an-absolute-path\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run.refreshSelectedCandidate(newDay.Add(24 * time.Hour)); err == nil ||
		!strings.Contains(err.Error(), "pointer") {
		t.Fatalf("invalid boundary pointer error = %v", err)
	}
}

func TestShadowStartupResumesTodaysPinnedPolicy(t *testing.T) {
	base := candidateTestPolicy()
	first := candidateForPrices(t, base, 200_000_000, 100_000_000)
	selected := candidateForPrices(t, base, 220_000_000, 110_000_000)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	startDay := func(root, fingerprint string, policy shadow.Policy, at time.Time) {
		t.Helper()
		directory := filepath.Join(root, fingerprint)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := ensureShadowPolicySnapshot(directory, policy); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(directory, "shadow-"+dayKey(at)+".jsonl"), nil, 0o600,
		); err != nil {
			t.Fatal(err)
		}
	}

	root := privateTestDirectory(t)
	startDay(root, first.CandidatePolicySHA256, first.Policy, now)
	resolved, fingerprint, err := resolveStartupShadowPolicy(
		base, selected.Policy, selected.CandidatePolicySHA256, root, now,
	)
	if err != nil || fingerprint != first.CandidatePolicySHA256 ||
		resolved.Trigger.ThresholdMicros != first.Policy.Trigger.ThresholdMicros {
		t.Fatalf("mid-day restart resolved %s/%+v, %v", fingerprint, resolved, err)
	}
	if fresh, freshFingerprint, err := resolveStartupShadowPolicy(
		base, selected.Policy, selected.CandidatePolicySHA256,
		privateTestDirectory(t), now,
	); err != nil || freshFingerprint != selected.CandidatePolicySHA256 ||
		fresh.Trigger.ThresholdMicros != selected.Policy.Trigger.ThresholdMicros {
		t.Fatalf("fresh startup resolved %s/%+v, %v", freshFingerprint, fresh, err)
	}
	if next, nextFingerprint, err := resolveStartupShadowPolicy(
		base, selected.Policy, selected.CandidatePolicySHA256, root, now.Add(24*time.Hour),
	); err != nil || nextFingerprint != selected.CandidatePolicySHA256 ||
		next.Trigger.ThresholdMicros != selected.Policy.Trigger.ThresholdMicros {
		t.Fatalf("next-day startup resolved %s/%+v, %v", nextFingerprint, next, err)
	}

	startDay(root, selected.CandidatePolicySHA256, selected.Policy, now)
	if _, _, err := resolveStartupShadowPolicy(
		base, selected.Policy, selected.CandidatePolicySHA256, root, now,
	); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("ambiguous current day was accepted: %v", err)
	}

	tamperedRoot := privateTestDirectory(t)
	startDay(tamperedRoot, first.CandidatePolicySHA256, selected.Policy, now)
	if _, _, err := resolveStartupShadowPolicy(
		base, selected.Policy, selected.CandidatePolicySHA256, tamperedRoot, now,
	); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched policy snapshot was accepted: %v", err)
	}
}

type candidatePriceSource struct {
	identity string
	at       time.Time
}

func (source candidatePriceSource) IdentitySHA256() string { return source.identity }

func (source candidatePriceSource) Latest(
	_ context.Context, feed string,
) (pricetrigger.Sample, error) {
	return pricetrigger.Sample{
		SourceSHA256: source.identity, Feed: feed, PriceMicros: 150_000_000,
		ConfidenceMicros: 1, PublishedAt: source.at,
	}, nil
}

func candidateTestPolicy() shadow.Policy {
	policy := validShadowPolicy()
	policy.Cluster, policy.QuotePeg = shadow.Devnet, nil
	policy.QuoteRoute.Provider = shadow.QuoteOrca
	policy.QuoteRoute.Pool = "11111111111111111111111111111111"
	policy.Trigger.PrimarySourceSHA256 = pricesource.KrakenSOLIdentitySHA256()
	policy.Trigger.SecondarySourceSHA256 = pricesource.KrakenIdentitySHA256()
	return policy
}

func candidateForPrices(
	t *testing.T, base shadow.Policy, sellAt, buyAt uint64,
) shadowPaperCandidate {
	t.Helper()
	prices := []uint64{sellAt, sellAt, buyAt, buyAt, sellAt, sellAt, buyAt, buyAt}
	result, err := searchShadowCandidate(base, prices, prices, 100)
	if err != nil {
		t.Fatal(err)
	}
	result.TrainDay, result.ValidationDay = "2026-08-17", "2026-08-18"
	candidate, err := newShadowPaperCandidate(
		base, result,
		shadowJournalProvenance{Day: result.TrainDay, Records: 9, ChainHeadSHA256: strings.Repeat("a", 64)},
		shadowJournalProvenance{Day: result.ValidationDay, Records: 9, ChainHeadSHA256: strings.Repeat("b", 64)},
	)
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func initialShadowCandidateFixture(
	t *testing.T, trainingPrices, validationPrices []uint64,
) (shadow.Policy, string, string, shadowPaperCandidate) {
	t.Helper()
	root := privateTestDirectory(t)
	base := candidateTestPolicy()
	base.TickSeconds = 3_600
	policyPath := writeShadowPolicy(t, base)
	trainDay, validationDay := "2026-08-17", "2026-08-18"
	writeShadowSearchDay(t, root, base, trainDay, trainingPrices)
	writeShadowSearchDay(t, root, base, validationDay, validationPrices)
	training, trainingProvenance, err := readShadowSearchJournal(
		filepath.Join(root, "shadow-"+trainDay+".jsonl"), trainDay, base,
	)
	if err != nil {
		t.Fatal(err)
	}
	validation, validationProvenance, err := readShadowSearchJournal(
		filepath.Join(root, "shadow-"+validationDay+".jsonl"), validationDay, base,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := searchShadowCandidateTicks(base, training, validation, 100)
	if err != nil {
		t.Fatal(err)
	}
	result.TrainDay, result.ValidationDay = trainDay, validationDay
	candidate, err := newShadowPaperCandidate(
		base, result, trainingProvenance, validationProvenance,
	)
	if err != nil {
		t.Fatal(err)
	}
	return base, policyPath, root, candidate
}

func initialWinningPrices(high, low uint64) []uint64 {
	prices := make([]uint64, 0, 23)
	for len(prices) < 20 {
		prices = append(prices, high, high, low, low)
	}
	return append(prices, high, high, high)
}

func initialLosingPrices(high, low uint64) []uint64 {
	prices := make([]uint64, 0, 23)
	for len(prices) < 23 {
		prices = append(prices, high, low, low, high)
	}
	return prices[:23]
}

func privateTestDirectory(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}
