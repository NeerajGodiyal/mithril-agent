package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestShadowAutoSelectPreservesAndRestoresThePreviousPaperChampion(t *testing.T) {
	fixture := newShadowAutoSelectFixture(t)
	originalEvaluate := shadowAutoSelectEvaluate
	defer func() { shadowAutoSelectEvaluate = originalEvaluate }()
	shadowAutoSelectEvaluate = func(
		shadow.Policy, string, string, string, string, uint32, time.Time, time.Time,
	) (shadowChallengeResult, error) {
		return shadowChallengeResult{
			Status: "challenger_qualified_for_paper_selection", PaperOnly: true,
			EligibleForPaperSelection: true,
		}, nil
	}

	result, err := fixture.autoSelect()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "qualified_paper_challenger_selected" ||
		!result.PaperOnly || result.Authorized || !result.ChampionPointerUpdated ||
		!result.RollbackPointerUpdated || result.Effective != "runner_start_or_next_utc_day" {
		t.Fatalf("auto-selection crossed its paper boundary: %+v", result)
	}
	selected, selectedPath, selectedSHA256, err := loadBoundSelectedShadowCandidate(
		fixture.championPointer, fixture.base,
	)
	if err != nil {
		t.Fatal(err)
	}
	if selected.CandidatePolicySHA256 != fixture.challenger.CandidatePolicySHA256 ||
		filepath.Dir(selectedPath) != filepath.Dir(fixture.championPointer) ||
		selectedSHA256 != fixture.challengerSHA256 {
		t.Fatalf("selected champion was not the protected challenger copy: path=%s candidate=%+v",
			selectedPath, selected)
	}
	rollbackRecord, rollback, _, _, err := loadBoundShadowRollbackRecord(
		fixture.rollbackPointer, fixture.base,
	)
	if err != nil {
		t.Fatal(err)
	}
	if rollback.CandidatePolicySHA256 != fixture.champion.CandidatePolicySHA256 ||
		rollbackRecord.ReplacedByCandidateSHA256 != fixture.challengerSHA256 {
		t.Fatal("rollback pointer did not preserve the previous champion")
	}
	rollbackBefore, err := os.ReadFile(fixture.rollbackPointer)
	if err != nil {
		t.Fatal(err)
	}
	again, err := fixture.autoSelect()
	if err != nil {
		t.Fatal(err)
	}
	rollbackAfter, err := os.ReadFile(fixture.rollbackPointer)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != "paper_challenger_already_selected" ||
		!bytes.Equal(rollbackBefore, rollbackAfter) {
		t.Fatalf("idempotent selection changed rollback state: %+v", again)
	}

	// Research is allowed to prepare the next challenger immediately after a
	// selection. That normal rotation must not invalidate the saved rollback.
	rotated := candidateForPrices(t, fixture.base, 230_000_000, 120_000_000)
	rotatedPath := filepath.Join(fixture.challengerCandidateDir, "rotated.json")
	if err := writeShadowPaperCandidate(rotatedPath, rotated); err != nil {
		t.Fatal(err)
	}
	_, rotatedSHA256, err := loadBoundShadowPaperCandidate(rotatedPath, fixture.base)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceShadowCandidatePointerSelected(
		fixture.challengerPointer, rotatedPath, rotatedSHA256,
		rotated.CandidatePolicySHA256, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC), 7,
	); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runShadowRestore([]string{
		"--policy", fixture.policyPath,
		"--champion-pointer", fixture.championPointer,
		"--rollback-pointer", fixture.rollbackPointer,
		"--challenger-pointer", fixture.challengerPointer,
		"--challenger-candidate-dir", fixture.challengerCandidateDir,
		"--lifecycle-lock", fixture.lifecycleLock,
	}, &output); err != nil {
		t.Fatal(err)
	}
	restored, _, err := loadSelectedShadowCandidate(fixture.championPointer, fixture.base)
	if err != nil {
		t.Fatal(err)
	}
	if restored.CandidatePolicySHA256 != fixture.champion.CandidatePolicySHA256 ||
		!bytes.Contains(output.Bytes(), []byte(`"status":"paper_champion_restored"`)) ||
		!bytes.Contains(output.Bytes(), []byte(`"challenger_pointer_updated":true`)) {
		t.Fatalf("paper rollback failed: candidate=%+v output=%s", restored, output.String())
	}
	baseline, baselinePath, baselineSHA256, _, err := loadBoundShadowCandidateSelection(
		fixture.challengerPointer, fixture.base,
	)
	if err != nil || baseline.CandidatePolicySHA256 != restored.CandidatePolicySHA256 ||
		filepath.Dir(baselinePath) != fixture.challengerCandidateDir ||
		baselineSHA256 == fixture.challengerSHA256 {
		t.Fatalf("rollback did not retire the selected challenger: candidate=%+v path=%s digest=%s err=%v",
			baseline, baselinePath, baselineSHA256, err)
	}
	afterRestore, err := fixture.autoSelect()
	if err != nil || afterRestore.Status != "paper_challenger_already_selected" ||
		afterRestore.ChampionPointerUpdated {
		t.Fatalf("hourly selector undid rollback: %+v, %v", afterRestore, err)
	}
	_, _, _, restoredSelection, err := loadBoundShadowCandidateSelection(
		fixture.championPointer, fixture.base,
	)
	if err != nil || restoredSelection.RestoredFromSHA256 != fixture.challengerSHA256 {
		t.Fatalf("champion pointer did not commit the rollback: %+v, %v", restoredSelection, err)
	}
	if err := replaceShadowCandidatePointerSelected(
		fixture.challengerPointer, rotatedPath, rotatedSHA256,
		rotated.CandidatePolicySHA256, time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC), 7,
	); err != nil {
		t.Fatal(err)
	}
	if err := runShadowSelect([]string{
		"--policy", fixture.policyPath,
		"--candidate", restoredSelection.CandidatePath,
		"--pointer", fixture.championPointer,
		"--lifecycle-lock", fixture.lifecycleLock,
	}, io.Discard); err != nil {
		t.Fatal(err)
	}
	_, _, _, reselected, err := loadBoundShadowCandidateSelection(
		fixture.championPointer, fixture.base,
	)
	if err != nil || reselected.RestoredFromSHA256 != fixture.challengerSHA256 {
		t.Fatalf("idempotent manual selection erased the rollback commit: %+v, %v", reselected, err)
	}
	challengerBeforeRetry, err := os.ReadFile(fixture.challengerPointer)
	if err != nil {
		t.Fatal(err)
	}
	var retryOutput bytes.Buffer
	if err := runShadowRestore([]string{
		"--policy", fixture.policyPath,
		"--champion-pointer", fixture.championPointer,
		"--rollback-pointer", fixture.rollbackPointer,
		"--challenger-pointer", fixture.challengerPointer,
		"--challenger-candidate-dir", fixture.challengerCandidateDir,
		"--lifecycle-lock", fixture.lifecycleLock,
	}, &retryOutput); err != nil {
		t.Fatal(err)
	}
	challengerAfterRetry, err := os.ReadFile(fixture.challengerPointer)
	if err != nil || !bytes.Equal(challengerBeforeRetry, challengerAfterRetry) ||
		!bytes.Contains(retryOutput.Bytes(), []byte(`"status":"paper_champion_already_restored"`)) {
		t.Fatalf("consumed rollback retired a later challenger: %s, %v", retryOutput.String(), err)
	}
}

func TestShadowAutoSelectRefusesRollbackPathsThatAliasCandidateArtifacts(t *testing.T) {
	for _, test := range []struct {
		name     string
		rollback func(shadowAutoSelectFixture) string
	}{
		{
			name: "current champion",
			rollback: func(fixture shadowAutoSelectFixture) string {
				_, path, _, err := loadBoundSelectedShadowCandidate(
					fixture.championPointer, fixture.base,
				)
				if err != nil {
					fixture.t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "selected champion copy",
			rollback: func(fixture shadowAutoSelectFixture) string {
				return filepath.Join(
					filepath.Dir(fixture.championPointer),
					"champion-"+fixture.challengerSHA256+".json",
				)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newShadowAutoSelectFixture(t)
			originalEvaluate := shadowAutoSelectEvaluate
			defer func() { shadowAutoSelectEvaluate = originalEvaluate }()
			shadowAutoSelectEvaluate = func(
				shadow.Policy, string, string, string, string, uint32, time.Time, time.Time,
			) (shadowChallengeResult, error) {
				return shadowChallengeResult{
					Status: "challenger_qualified_for_paper_selection", PaperOnly: true,
					EligibleForPaperSelection: true,
				}, nil
			}
			championBefore, err := os.ReadFile(fixture.championPointer)
			if err != nil {
				t.Fatal(err)
			}
			_, err = autoSelectShadowChallenger(
				fixture.base, fixture.championPointer, fixture.challengerPointer,
				fixture.championRoot, fixture.challengerRoot, test.rollback(fixture),
				fixture.lifecycleLock, 7, time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC),
			)
			championAfter, readErr := os.ReadFile(fixture.championPointer)
			if err == nil || !bytes.Equal(championBefore, championAfter) || readErr != nil {
				t.Fatalf("alias was not refused before mutation: err=%v read=%v", err, readErr)
			}
		})
	}
}

func TestShadowAutoSelectRefusesARollbackPathThatAliasesTheBasePolicy(t *testing.T) {
	fixture := newShadowAutoSelectFixture(t)
	policyRaw, err := os.ReadFile(fixture.policyPath)
	if err != nil {
		t.Fatal(err)
	}
	aliasedPolicy := filepath.Join(filepath.Dir(fixture.championPointer), "policy.json")
	if err := os.WriteFile(aliasedPolicy, policyRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runShadowAutoSelect([]string{
		"--policy", aliasedPolicy,
		"--champion-pointer", fixture.championPointer,
		"--challenger-pointer", fixture.challengerPointer,
		"--champion-dir", fixture.championRoot,
		"--challenger-dir", fixture.challengerRoot,
		"--days", "7",
		"--rollback-pointer", aliasedPolicy,
		"--lifecycle-lock", fixture.lifecycleLock,
	}, io.Discard); err == nil {
		t.Fatal("auto-select accepted the base policy as its rollback output")
	}
	after, err := os.ReadFile(aliasedPolicy)
	if err != nil || !bytes.Equal(policyRaw, after) {
		t.Fatalf("rollback alias changed the base policy: %v", err)
	}
}

func TestShadowAutoSelectLeavesPendingOrRejectedChallengersUntouched(t *testing.T) {
	for _, test := range []struct {
		name   string
		result shadowChallengeResult
		err    error
		status string
	}{
		{name: "pending", err: errShadowChallengeEvidencePending, status: "challenger_evidence_pending"},
		{name: "rejected", result: shadowChallengeResult{
			Status: "challenger_not_qualified", Reasons: []string{"drawdown_regressed"},
		}, status: "challenger_not_qualified"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newShadowAutoSelectFixture(t)
			before, readErr := os.ReadFile(fixture.championPointer)
			if readErr != nil {
				t.Fatal(readErr)
			}
			originalEvaluate := shadowAutoSelectEvaluate
			defer func() { shadowAutoSelectEvaluate = originalEvaluate }()
			shadowAutoSelectEvaluate = func(
				shadow.Policy, string, string, string, string, uint32, time.Time, time.Time,
			) (shadowChallengeResult, error) {
				return test.result, test.err
			}
			result, err := fixture.autoSelect()
			if err != nil {
				t.Fatal(err)
			}
			after, readErr := os.ReadFile(fixture.championPointer)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if result.Status != test.status || result.ChampionPointerUpdated ||
				!bytes.Equal(before, after) {
				t.Fatalf("%s challenger changed the champion: result=%+v", test.name, result)
			}
			if _, statErr := os.Stat(fixture.rollbackPointer); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("%s challenger wrote rollback state: %v", test.name, statErr)
			}
		})
	}
}

func TestShadowAutoSelectRejectsAPreWalkForwardChallenger(t *testing.T) {
	fixture := newShadowAutoSelectFixtureWithWalkForward(t, false)
	before, err := os.ReadFile(fixture.championPointer)
	if err != nil {
		t.Fatal(err)
	}
	originalEvaluate := shadowAutoSelectEvaluate
	defer func() { shadowAutoSelectEvaluate = originalEvaluate }()
	shadowAutoSelectEvaluate = func(
		shadow.Policy, string, string, string, string, uint32, time.Time, time.Time,
	) (shadowChallengeResult, error) {
		return shadowChallengeResult{
			Status: "challenger_qualified_for_paper_selection", PaperOnly: true,
			EligibleForPaperSelection: true,
		}, nil
	}
	result, err := fixture.autoSelect()
	after, readErr := os.ReadFile(fixture.championPointer)
	if err == nil || result.ChampionPointerUpdated || readErr != nil || !bytes.Equal(before, after) {
		t.Fatalf("legacy challenger crossed auto-selection: result=%+v err=%v read=%v", result, err, readErr)
	}
}

func TestFixedForwardEvidenceWindowEndsAsNotQualifiedWhenReportsAreMissing(t *testing.T) {
	fixture := newShadowAutoSelectFixture(t)
	_, challengerPath, _, selection, err := loadBoundShadowCandidateSelection(
		fixture.challengerPointer, fixture.base,
	)
	if err != nil || selection.EligibleFrom == nil {
		t.Fatalf("challenger selection = %+v, %v", selection, err)
	}
	cutoff := selection.EligibleFrom.AddDate(0, 0, int(selection.ChallengeDays))
	result, err := evaluateShadowChallenge(
		fixture.base, fixture.championPointer, challengerPath,
		fixture.championRoot, fixture.challengerRoot,
		selection.ChallengeDays, cutoff, selection.EligibleFrom.UTC(),
	)
	if err != nil || result.Status != "challenger_not_qualified" ||
		result.EligibleForPaperSelection || result.CompleteDays != 0 ||
		len(result.Reasons) != 1 || result.Reasons[0] != "champion_evidence_window_incomplete" ||
		!result.From.Equal(selection.EligibleFrom.UTC()) || !result.To.Equal(cutoff) {
		t.Fatalf("terminal incomplete evidence = %+v, %v", result, err)
	}
	if err := validateShadowResearchRotationStatus(result.Status); err != nil {
		t.Fatalf("terminal incomplete challenger could not rotate: %v", err)
	}
}

type shadowAutoSelectFixture struct {
	t                      *testing.T
	base                   shadow.Policy
	policyPath             string
	champion               shadowPaperCandidate
	challenger             shadowPaperCandidate
	championPointer        string
	challengerPointer      string
	challengerCandidateDir string
	championRoot           string
	challengerRoot         string
	rollbackPointer        string
	lifecycleLock          string
	challengerSHA256       string
}

func newShadowAutoSelectFixture(t *testing.T) shadowAutoSelectFixture {
	return newShadowAutoSelectFixtureWithWalkForward(t, true)
}

func newShadowAutoSelectFixtureWithWalkForward(
	t *testing.T, walkForward bool,
) shadowAutoSelectFixture {
	t.Helper()
	root := privateTestDirectory(t)
	championControl := filepath.Join(root, "champion")
	challengerControl := filepath.Join(root, "challenger")
	challengerCandidateDir := filepath.Join(challengerControl, "candidates")
	championRoot := filepath.Join(root, "champion-run")
	challengerRoot := filepath.Join(root, "challenger-run")
	for _, directory := range []string{
		championControl, challengerControl, challengerCandidateDir, championRoot, challengerRoot,
	} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	base := validShadowPolicy()
	policyPath := writeShadowPolicy(t, base)
	champion := candidateForPrices(t, base, 200_000_000, 100_000_000)
	challenger := candidateForPrices(t, base, 220_000_000, 110_000_000)
	if walkForward {
		challenger = admittedShadowAutoSelectCandidate(t, base, challenger)
	}
	championPath := filepath.Join(championControl, "champion.json")
	challengerPath := filepath.Join(challengerCandidateDir, "challenger.json")
	if err := writeShadowPaperCandidate(championPath, champion); err != nil {
		t.Fatal(err)
	}
	if err := writeShadowPaperCandidate(challengerPath, challenger); err != nil {
		t.Fatal(err)
	}
	_, championSHA256, err := loadBoundShadowPaperCandidate(championPath, base)
	if err != nil {
		t.Fatal(err)
	}
	_, challengerSHA256, err := loadBoundShadowPaperCandidate(challengerPath, base)
	if err != nil {
		t.Fatal(err)
	}
	championPointer := filepath.Join(championControl, "active.json")
	challengerPointer := filepath.Join(challengerControl, "active.json")
	if err := replaceShadowCandidatePointer(
		championPointer, championPath, championSHA256, champion.CandidatePolicySHA256,
	); err != nil {
		t.Fatal(err)
	}
	selectedAt := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	if err := replaceShadowCandidatePointerSelected(
		challengerPointer, challengerPath, challengerSHA256,
		challenger.CandidatePolicySHA256, selectedAt, 7,
	); err != nil {
		t.Fatal(err)
	}
	return shadowAutoSelectFixture{
		t: t, base: base, policyPath: policyPath,
		champion: champion, challenger: challenger,
		championPointer: championPointer, challengerPointer: challengerPointer,
		challengerCandidateDir: challengerCandidateDir,
		championRoot:           championRoot, challengerRoot: challengerRoot,
		rollbackPointer:  filepath.Join(championControl, "previous.json"),
		lifecycleLock:    filepath.Join(challengerControl, "lifecycle.lock"),
		challengerSHA256: challengerSHA256,
	}
}

func admittedShadowAutoSelectCandidate(
	t *testing.T, base shadow.Policy, candidate shadowPaperCandidate,
) shadowPaperCandidate {
	t.Helper()
	if candidate.Research.Validation.VersusHoldMicros <= 0 ||
		candidate.Research.Validation.FullRoundTrips == 0 {
		t.Fatalf("auto-select fixture candidate is not profitable: %+v", candidate.Research.Validation)
	}
	provenance := make([]shadowJournalProvenance, shadowWalkForwardWindows+1)
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	for index := range provenance {
		provenance[index] = shadowJournalProvenance{
			Day: start.AddDate(0, 0, index).Format("2006-01-02"), Records: 9,
			ChainHeadSHA256: strings.Repeat(string("12345678"[index]), 64),
		}
	}
	admission := shadowWalkForwardAdmission{
		Version: shadowWalkForwardVersion, Status: "admitted",
		Windows: shadowWalkForwardWindows, PositiveWindows: shadowWalkForwardWindows,
		RequiredPositiveWindows:   4,
		ValidationFullRoundTrips:  candidate.Research.Validation.FullRoundTrips * shadowWalkForwardWindows,
		RequiredFullRoundTrips:    4,
		AggregateVersusHoldMicros: candidate.Research.Validation.VersusHoldMicros * shadowWalkForwardWindows,
		TotalCandidatesEvaluated:  candidate.Research.CandidatesEvaluated * shadowWalkForwardWindows,
		Folds:                     make([]shadowWalkForwardFold, 0, shadowWalkForwardWindows),
	}
	for index := 0; index < shadowWalkForwardWindows; index++ {
		admission.Folds = append(admission.Folds, shadowWalkForwardFold{
			TrainingJournal: provenance[index], ValidationJournal: provenance[index+1],
			TrainingObservableBPS: 10_000, ValidationObservableBPS: 10_000,
			Candidate:             candidate.Research.Candidate,
			CandidatePolicySHA256: candidate.CandidatePolicySHA256,
			CandidatesEvaluated:   candidate.Research.CandidatesEvaluated,
			Training:              candidate.Research.Training, Validation: candidate.Research.Validation,
		})
	}
	candidate.Research.TrainDay = provenance[len(provenance)-2].Day
	candidate.Research.ValidationDay = provenance[len(provenance)-1].Day
	candidate.Research.WalkForward = &admission
	candidate.TrainingJournal = provenance[len(provenance)-2]
	candidate.ValidationJournal = provenance[len(provenance)-1]
	if err := candidate.validateAgainst(base); err != nil {
		t.Fatal(err)
	}
	return candidate
}

func (fixture shadowAutoSelectFixture) autoSelect() (shadowAutoSelectResult, error) {
	fixture.t.Helper()
	return autoSelectShadowChallenger(
		fixture.base, fixture.championPointer, fixture.challengerPointer,
		fixture.championRoot, fixture.challengerRoot, fixture.rollbackPointer,
		fixture.lifecycleLock, 7, time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC),
	)
}

func TestShadowAutoSelectHelpStatesThePaperOnlyBoundary(t *testing.T) {
	var output bytes.Buffer
	if err := runShadowAutoSelect([]string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"forward paper challenge", "preserves", "cannot authorize"} {
		if !bytes.Contains(output.Bytes(), []byte(want)) {
			t.Fatalf("auto-select help omitted %q:\n%s", want, output.String())
		}
	}
}
