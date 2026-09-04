package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const shadowRollbackRecordVersion = uint32(1)

type shadowRollbackRecord struct {
	Version                   uint32                 `json:"version"`
	ReplacedByCandidateSHA256 string                 `json:"replaced_by_candidate_sha256"`
	PreviousChampion          shadowCandidatePointer `json:"previous_champion"`
}

const shadowAutoSelectUsage = `Usage: mithril-agent shadow auto-select --policy PATH
       --champion-pointer PATH --challenger-pointer PATH
       --champion-dir PATH --challenger-dir PATH
       --days N --rollback-pointer PATH --lifecycle-lock PATH
       [--outcome-journal PATH]

Selects a challenger only after its fixed forward paper challenge qualifies.
It first preserves the current champion pointer, copies the immutable candidate
outside Hermes' writable tree, and then atomically updates only the paper
champion pointer. Pending or rejected challengers are reported without error.
This command cannot authorize, sign, submit, or enable a live strategy.`

const shadowRestoreUsage = `Usage: mithril-agent shadow restore --policy PATH
       --champion-pointer PATH --rollback-pointer PATH
       --challenger-pointer PATH --challenger-candidate-dir PATH
       --lifecycle-lock PATH

Safely restores the last preserved paper champion pointer. The referenced
candidate and base policy are revalidated first. It also retires the selected
challenger by rebinding the challenger observer to the restored paper champion,
so the automatic selector cannot undo the rollback. This command cannot
authorize, sign, submit, or enable a live strategy.`

type shadowAutoSelectResult struct {
	Status                   string   `json:"status"`
	Authorized               bool     `json:"authorized"`
	PaperOnly                bool     `json:"paper_only"`
	ChampionPointerUpdated   bool     `json:"champion_pointer_updated"`
	ChallengerPointerUpdated bool     `json:"challenger_pointer_updated"`
	RollbackPointerUpdated   bool     `json:"rollback_pointer_updated"`
	CandidatePolicySHA256    string   `json:"candidate_policy_sha256,omitempty"`
	Effective                string   `json:"effective,omitempty"`
	Reasons                  []string `json:"reasons,omitempty"`
}

var shadowAutoSelectEvaluate = evaluateShadowChallenge

func runShadowAutoSelect(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow auto-select", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "immutable base shadow policy")
	championPointer := flags.String("champion-pointer", "", "paper champion pointer")
	challengerPointer := flags.String("challenger-pointer", "", "forward-selected paper challenger pointer")
	championRoot := flags.String("champion-dir", "", "champion run root")
	challengerRoot := flags.String("challenger-dir", "", "challenger run root")
	rollbackPointer := flags.String("rollback-pointer", "", "preserved previous champion pointer")
	lifecycleLock := flags.String("lifecycle-lock", "", "shared paper lifecycle lock")
	outcomeJournal := flags.String("outcome-journal", "", "optional Hermes-bound paper outcome journal")
	days := flags.Uint("days", 0, "required forward challenge days, 7..3650")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowAutoSelectUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *days < 7 || *days > 3650 || !validShadowAutoSelectPaths(
		*policyPath, *championPointer, *challengerPointer, *championRoot, *challengerRoot,
		*rollbackPointer, *lifecycleLock,
	) || !validShadowAutoSelectOutcomePath(*outcomeJournal, []string{
		*policyPath, *championPointer, *challengerPointer, *championRoot, *challengerRoot,
		*rollbackPointer, *lifecycleLock,
	}) {
		return errors.New("shadow auto-select requires distinct fixed absolute private paper paths")
	}
	base, err := loadActiveShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	result, err := autoSelectShadowChallengerWithOutcomes(
		base, *championPointer, *challengerPointer, *championRoot, *challengerRoot,
		*rollbackPointer, *lifecycleLock, uint32(*days), time.Now().UTC(), *outcomeJournal,
	)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

func validShadowAutoSelectOutcomePath(path string, protected []string) bool {
	if path == "" {
		return true
	}
	if !absoluteClean(path) {
		return false
	}
	for _, other := range protected {
		if path == other || filepath.Dir(path) == other ||
			filepath.Dir(path) == filepath.Dir(other) ||
			strings.HasPrefix(path, other+string(os.PathSeparator)) {
			return false
		}
	}
	return true
}

func validShadowAutoSelectPaths(
	policyPath, championPointer, challengerPointer, championRoot, challengerRoot,
	rollbackPointer, lifecycleLock string,
) bool {
	paths := []string{
		policyPath, championPointer, challengerPointer, championRoot, challengerRoot,
		rollbackPointer, lifecycleLock,
	}
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if !absoluteClean(path) {
			return false
		}
		if _, duplicate := seen[path]; duplicate {
			return false
		}
		seen[path] = struct{}{}
	}
	return championRoot != challengerRoot &&
		filepath.Dir(rollbackPointer) == filepath.Dir(championPointer) &&
		filepath.Dir(lifecycleLock) == filepath.Dir(challengerPointer) &&
		filepath.Dir(championPointer) != filepath.Dir(challengerPointer)
}

func autoSelectShadowChallenger(
	base shadow.Policy,
	championPointer, challengerPointer, championRoot, challengerRoot,
	rollbackPointer, lifecycleLock string,
	expectedDays uint32,
	now time.Time,
) (shadowAutoSelectResult, error) {
	return autoSelectShadowChallengerWithOutcomes(
		base, championPointer, challengerPointer, championRoot, challengerRoot,
		rollbackPointer, lifecycleLock, expectedDays, now, "",
	)
}

func autoSelectShadowChallengerWithOutcomes(
	base shadow.Policy,
	championPointer, challengerPointer, championRoot, challengerRoot,
	rollbackPointer, lifecycleLock string,
	expectedDays uint32,
	now time.Time,
	outcomeJournal string,
) (shadowAutoSelectResult, error) {
	result := shadowAutoSelectResult{PaperOnly: true}
	if !validShadowAutoSelectOutcomePath(outcomeJournal, []string{
		championPointer, challengerPointer, championRoot, challengerRoot,
		rollbackPointer, lifecycleLock,
	}) {
		return result, errors.New("paper outcome journal path is invalid")
	}
	err := withShadowLifecycleLock(lifecycleLock, func() error {
		championPointerBefore, err := securefile.ReadPrivate(
			championPointer, shadowCandidatePointerBytes,
		)
		if err != nil {
			return errors.New("paper champion pointer is invalid")
		}
		challengerPointerBefore, err := securefile.ReadPrivate(
			challengerPointer, shadowCandidatePointerBytes,
		)
		if err != nil {
			return errors.New("paper challenger pointer is invalid")
		}
		challenger, challengerPath, challengerSHA256, selection, err :=
			loadBoundShadowCandidateSelection(challengerPointer, base)
		if err != nil || selection.SelectedAt == nil || selection.EligibleFrom == nil ||
			selection.ChallengeDays != expectedDays ||
			selection.ChallengeGateVersion != shadowChallengeGateVersion {
			return errors.New("paper challenger pointer is not forward-qualified evidence")
		}
		if outcomeJournal != "" && challenger.ResearchPacket == nil {
			return errors.New("paper outcome journaling requires a Hermes-bound challenger")
		}
		_, championPath, championSHA256, err := loadBoundSelectedShadowCandidate(championPointer, base)
		if err != nil {
			return errors.New("paper champion pointer is invalid")
		}
		if championSHA256 == challengerSHA256 {
			if outcomeJournal != "" {
				if _, err := reconcileShadowResearchSelectionFromForward(
					outcomeJournal, now.UTC(), challengerSHA256,
				); err != nil {
					return errors.New("could not reconcile the paper research selection outcome")
				}
			}
			result.Status = "paper_challenger_already_selected"
			result.CandidatePolicySHA256 = challenger.CandidatePolicySHA256
			return nil
		}
		if challenger.Research.WalkForward == nil {
			return errors.New("paper challenger lacks walk-forward admission evidence")
		}

		challenge, err := shadowAutoSelectEvaluate(
			base, championPointer, challengerPath, championRoot, challengerRoot,
			selection.ChallengeDays, now, selection.EligibleFrom.UTC(),
		)
		if errors.Is(err, errShadowChallengeEvidencePending) {
			result.Status = "challenger_evidence_pending"
			result.Reasons = []string{"paired_complete_days_unavailable"}
			return nil
		}
		if err != nil {
			return err
		}
		if outcomeJournal != "" {
			if _, _, err := recordShadowResearchForwardOutcome(
				outcomeJournal, now.UTC(), base, challenger, challengerSHA256, challenge,
			); err != nil {
				return errors.New("could not record the paper research forward outcome")
			}
		}
		result.Status = challenge.Status
		result.Reasons = challenge.Reasons
		result.CandidatePolicySHA256 = challenger.CandidatePolicySHA256
		if challenge.Status != "challenger_qualified_for_paper_selection" ||
			!challenge.EligibleForPaperSelection {
			return nil
		}

		championPointerAfter, championReadErr := securefile.ReadPrivate(
			championPointer, shadowCandidatePointerBytes,
		)
		challengerPointerAfter, challengerReadErr := securefile.ReadPrivate(
			challengerPointer, shadowCandidatePointerBytes,
		)
		if championReadErr != nil || challengerReadErr != nil ||
			!bytes.Equal(championPointerBefore, championPointerAfter) ||
			!bytes.Equal(challengerPointerBefore, challengerPointerAfter) {
			return errors.New("paper candidate pointers changed during automatic selection")
		}
		candidateBytes, err := securefile.ReadPrivate(challengerPath, maxInputBytes)
		if err != nil {
			return errors.New("could not read the qualified paper challenger")
		}
		candidateDigest := sha256.Sum256(candidateBytes)
		if fmt.Sprintf("%x", candidateDigest) != challengerSHA256 {
			return errors.New("qualified paper challenger changed during automatic selection")
		}
		championCandidate := filepath.Join(
			filepath.Dir(championPointer), "champion-"+challengerSHA256+".json",
		)
		if rollbackPointer == championPath || rollbackPointer == challengerPath ||
			rollbackPointer == championCandidate {
			return errors.New("paper rollback path aliases a candidate artifact")
		}
		if err := ensureShadowCandidateArtifact(championCandidate, candidateBytes); err != nil {
			return err
		}
		rollback, err := encodeShadowRollbackRecord(championPointerBefore, challengerSHA256)
		if err != nil {
			return errors.New("could not encode the paper rollback record")
		}
		if err := securefile.ReplacePrivate(
			rollbackPointer, rollback, shadowCandidatePointerBytes,
		); err != nil {
			return errors.New("could not preserve the previous paper champion pointer")
		}
		if err := replaceShadowCandidatePointer(
			championPointer, championCandidate, challengerSHA256,
			challenger.CandidatePolicySHA256,
		); err != nil {
			return errors.New("could not update the paper champion pointer")
		}
		if outcomeJournal != "" {
			if _, _, err := recordShadowResearchSelectionConfirmation(
				outcomeJournal, now.UTC(), base, challenger, challengerSHA256, challenge,
			); err != nil {
				return errors.New("paper champion changed but its research outcome confirmation needs reconciliation")
			}
		}
		result.Status = "qualified_paper_challenger_selected"
		result.ChampionPointerUpdated = true
		result.RollbackPointerUpdated = true
		result.Effective = "runner_start_or_next_utc_day"
		return nil
	})
	return result, err
}

func encodeShadowRollbackRecord(
	previousPointer []byte, replacedByCandidateSHA256 string,
) ([]byte, error) {
	var previous shadowCandidatePointer
	if err := strictjson.Decode(previousPointer, &previous); err != nil ||
		!validLowerSHA256(replacedByCandidateSHA256) {
		return nil, errors.New("paper rollback record input is invalid")
	}
	return marshalShadowRollbackRecord(shadowRollbackRecord{
		Version:                   shadowRollbackRecordVersion,
		ReplacedByCandidateSHA256: replacedByCandidateSHA256,
		PreviousChampion:          previous,
	})
}

func marshalShadowRollbackRecord(record shadowRollbackRecord) ([]byte, error) {
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func loadBoundShadowRollbackRecord(
	path string, base shadow.Policy,
) (shadowRollbackRecord, shadowPaperCandidate, []byte, string, error) {
	raw, err := securefile.ReadPrivate(path, shadowCandidatePointerBytes)
	if err != nil {
		return shadowRollbackRecord{}, shadowPaperCandidate{}, nil, "", err
	}
	var record shadowRollbackRecord
	if err := strictjson.Decode(raw, &record); err != nil ||
		record.Version != shadowRollbackRecordVersion ||
		!validLowerSHA256(record.ReplacedByCandidateSHA256) ||
		!validShadowCandidatePointer(record.PreviousChampion, path) {
		return shadowRollbackRecord{}, shadowPaperCandidate{}, nil, "", errors.New("paper rollback record is invalid")
	}
	candidate, digest, err := loadBoundShadowPaperCandidate(
		record.PreviousChampion.CandidatePath, base,
	)
	if err != nil || digest != record.PreviousChampion.CandidateSHA256 ||
		candidate.CandidatePolicySHA256 != record.PreviousChampion.CandidatePolicySHA256 {
		return shadowRollbackRecord{}, shadowPaperCandidate{}, nil, "", errors.New("paper rollback record no longer matches its candidate")
	}
	restored := record.PreviousChampion
	restored.RestoredFromSHA256 = record.ReplacedByCandidateSHA256
	pointer, err := json.MarshalIndent(restored, "", "  ")
	if err != nil {
		return shadowRollbackRecord{}, shadowPaperCandidate{}, nil, "", err
	}
	return record, candidate, append(pointer, '\n'), digest, nil
}

func ensureShadowCandidateArtifact(path string, encoded []byte) error {
	if existing, err := securefile.ReadPrivate(path, maxInputBytes); err == nil {
		if !bytes.Equal(existing, encoded) {
			return errors.New("paper candidate artifact digest collision")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("could not inspect the paper candidate artifact")
	}
	if err := securefile.CreatePrivate(path, encoded, maxInputBytes); err == nil {
		return nil
	}
	existing, err := securefile.ReadPrivate(path, maxInputBytes)
	if err != nil || !bytes.Equal(existing, encoded) {
		return errors.New("could not create the paper candidate artifact")
	}
	return nil
}

func runShadowRestore(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow restore", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "immutable base shadow policy")
	championPointer := flags.String("champion-pointer", "", "paper champion pointer")
	rollbackPointer := flags.String("rollback-pointer", "", "preserved previous champion pointer")
	challengerPointer := flags.String("challenger-pointer", "", "paper challenger pointer")
	challengerCandidateDir := flags.String("challenger-candidate-dir", "", "private challenger candidate directory")
	lifecycleLock := flags.String("lifecycle-lock", "", "shared paper lifecycle lock")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowRestoreUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || !validShadowRestorePaths(
		*championPointer, *rollbackPointer, *challengerPointer,
		*challengerCandidateDir, *lifecycleLock,
	) {
		return errors.New("shadow restore requires distinct fixed absolute paper pointer paths")
	}
	base, err := loadActiveShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	result, err := restoreShadowChampion(
		base, *championPointer, *rollbackPointer, *challengerPointer,
		*challengerCandidateDir, *lifecycleLock, time.Now().UTC(),
	)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

func validShadowRestorePaths(
	championPointer, rollbackPointer, challengerPointer,
	challengerCandidateDir, lifecycleLock string,
) bool {
	paths := []string{
		championPointer, rollbackPointer, challengerPointer,
		challengerCandidateDir, lifecycleLock,
	}
	for _, path := range paths {
		if !absoluteClean(path) {
			return false
		}
	}
	return championPointer != rollbackPointer &&
		championPointer != challengerPointer && rollbackPointer != challengerPointer &&
		championPointer != lifecycleLock && rollbackPointer != lifecycleLock &&
		challengerPointer != lifecycleLock &&
		filepath.Dir(championPointer) == filepath.Dir(rollbackPointer) &&
		filepath.Dir(challengerPointer) == filepath.Dir(lifecycleLock) &&
		filepath.Dir(challengerCandidateDir) == filepath.Dir(challengerPointer) &&
		filepath.Dir(championPointer) != filepath.Dir(challengerPointer)
}

func restoreShadowChampion(
	base shadow.Policy,
	championPointer, rollbackPointer, challengerPointer,
	challengerCandidateDir, lifecycleLock string,
	now time.Time,
) (shadowAutoSelectResult, error) {
	result := shadowAutoSelectResult{Status: "paper_champion_already_restored", PaperOnly: true}
	err := withShadowLifecycleLock(lifecycleLock, func() error {
		rollback, candidate, rollbackRaw, candidateSHA256, err := loadBoundShadowRollbackRecord(
			rollbackPointer, base,
		)
		if err != nil {
			return errors.New("preserved paper champion rollback record is invalid")
		}
		candidatePath := rollback.PreviousChampion.CandidatePath
		candidateBytes, err := securefile.ReadPrivate(candidatePath, maxInputBytes)
		if err != nil {
			return errors.New("could not read the preserved paper champion")
		}
		result.CandidatePolicySHA256 = candidate.CandidatePolicySHA256
		current, err := securefile.ReadPrivate(championPointer, shadowCandidatePointerBytes)
		if err != nil {
			return errors.New("could not read the current paper champion pointer")
		}
		_, _, currentSHA256, currentSelection, err := loadBoundShadowCandidateSelection(
			championPointer, base,
		)
		if err != nil {
			return errors.New("current paper champion pointer is invalid")
		}
		if currentSHA256 == candidateSHA256 &&
			currentSelection.RestoredFromSHA256 == rollback.ReplacedByCandidateSHA256 {
			return nil
		}
		challengerCurrent, err := securefile.ReadPrivate(
			challengerPointer, shadowCandidatePointerBytes,
		)
		if err != nil {
			return errors.New("could not read the paper challenger pointer")
		}
		_, activePath, activeSHA256, selection, err := loadBoundShadowCandidateSelection(
			challengerPointer, base,
		)
		if err != nil || selection.ChallengeDays < 7 ||
			selection.ChallengeGateVersion != shadowChallengeGateVersion ||
			filepath.Dir(activePath) != challengerCandidateDir {
			return errors.New("paper challenger pointer is invalid")
		}
		if currentSHA256 != candidateSHA256 &&
			currentSHA256 != rollback.ReplacedByCandidateSHA256 {
			return errors.New("paper rollback record does not apply to the current champion")
		}
		baselinePath := filepath.Join(
			challengerCandidateDir, "challenger-"+candidateSHA256+".json",
		)
		if err := ensureShadowCandidateArtifact(baselinePath, candidateBytes); err != nil {
			return errors.New("could not preserve the restored champion in the challenger tree")
		}
		challengerChanged := activeSHA256 != candidateSHA256 || activePath != baselinePath
		if challengerChanged {
			if err := replaceShadowCandidatePointerSelected(
				challengerPointer, baselinePath, candidateSHA256,
				candidate.CandidatePolicySHA256, now, selection.ChallengeDays,
			); err != nil {
				return errors.New("could not retire the rolled-back paper challenger")
			}
			result.ChallengerPointerUpdated = true
		}
		championChanged := !bytes.Equal(current, rollbackRaw)
		if championChanged {
			if err := securefile.ReplacePrivate(
				championPointer, rollbackRaw, shadowCandidatePointerBytes,
			); err != nil {
				if challengerChanged {
					_ = securefile.ReplacePrivate(
						challengerPointer, challengerCurrent, shadowCandidatePointerBytes,
					)
					result.ChallengerPointerUpdated = false
				}
				return errors.New("could not restore the paper champion pointer")
			}
			result.ChampionPointerUpdated = true
		}
		if result.ChampionPointerUpdated || result.ChallengerPointerUpdated {
			result.Status = "paper_champion_restored"
			result.Effective = "runner_start_or_next_utc_day"
		}
		return nil
	})
	if err != nil {
		return shadowAutoSelectResult{}, err
	}
	return result, nil
}
