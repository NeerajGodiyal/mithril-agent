package main

import (
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/Overclock-Validator/mithril-agent/researchpacket"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const (
	shadowPaperCandidateVersion = uint32(1)
	shadowCandidatePointerBytes = int64(4096)
	shadowInitialCoverageBPS    = int32(9_500)
	shadowInitialRoundTrips     = uint64(2)
)

const shadowSelectUsage = `Usage: mithril-agent shadow select --policy PATH
                                           --candidate PATH --pointer PATH
                                           --lifecycle-lock PATH
                                           [--initial --evidence-dir PATH]

Validates one immutable paper candidate against its base policy, then atomically
records it for a shadow runner. A running observer applies the selection only at
the next UTC-day boundary. A restarted observer resumes the policy already
pinned by today's journal; a fresh UTC day applies the latest selection.
Initial selection replays both bound evidence days and a doubled-spread stress
case before it can create the first paper champion.
This command cannot authorize, sign, submit, or modify a live strategy.`

// shadowJournalProvenance identifies the exact verified journal stream used by
// research. A day alone is not evidence: the chain head and record count keep
// an artifact from silently referring to a later or truncated copy.
type shadowJournalProvenance struct {
	Day             string `json:"day"`
	Records         int    `json:"records"`
	ChainHeadSHA256 string `json:"chain_head_sha256"`
}

// shadowPaperCandidate is a complete, immutable policy that is safe to stage
// for another paper run. It is deliberately not an execution policy and has no
// field for a key, signer, submitter, grant, or live configuration pointer.
type shadowPaperCandidate struct {
	Version               uint32                  `json:"version"`
	Status                string                  `json:"status"`
	Authorized            bool                    `json:"authorized"`
	Promotable            bool                    `json:"promotable"`
	PaperOnly             bool                    `json:"paper_only"`
	BasePolicySHA256      string                  `json:"base_policy_sha256"`
	CandidatePolicySHA256 string                  `json:"candidate_policy_sha256"`
	TrainingJournal       shadowJournalProvenance `json:"training_journal"`
	ValidationJournal     shadowJournalProvenance `json:"validation_journal"`
	Hypothesis            *shadowPaperHypothesis  `json:"hypothesis,omitempty"`
	ResearchPacket        *researchpacket.Packet  `json:"research_packet,omitempty"`
	Experiment            *shadowPaperExperiment  `json:"experiment,omitempty"`
	Research              shadowSearchResult      `json:"research"`
	Policy                shadow.Policy           `json:"policy"`
}

type shadowCandidatePointer struct {
	Version               uint32     `json:"version"`
	CandidatePath         string     `json:"candidate_path"`
	CandidateSHA256       string     `json:"candidate_sha256"`
	CandidatePolicySHA256 string     `json:"candidate_policy_sha256"`
	RestoredFromSHA256    string     `json:"restored_from_candidate_sha256,omitempty"`
	SelectedAt            *time.Time `json:"selected_at,omitempty"`
	EligibleFrom          *time.Time `json:"eligible_from,omitempty"`
	ChallengeDays         uint32     `json:"challenge_days,omitempty"`
	ChallengeGateVersion  uint32     `json:"challenge_gate_version,omitempty"`
}

func newShadowPaperCandidate(
	base shadow.Policy,
	result shadowSearchResult,
	training, validation shadowJournalProvenance,
) (shadowPaperCandidate, error) {
	baseFingerprint, err := base.Fingerprint()
	if err != nil {
		return shadowPaperCandidate{}, err
	}
	policy, err := shadowSearchCandidatePolicy(base, result.Candidate)
	if err != nil {
		return shadowPaperCandidate{}, err
	}
	candidateFingerprint, err := policy.Fingerprint()
	if err != nil {
		return shadowPaperCandidate{}, err
	}
	result.CandidatePolicySHA256 = candidateFingerprint
	candidate := shadowPaperCandidate{
		Version: shadowPaperCandidateVersion, Status: "paper_candidate",
		PaperOnly: true, BasePolicySHA256: baseFingerprint,
		CandidatePolicySHA256: candidateFingerprint,
		TrainingJournal:       training, ValidationJournal: validation,
		Research: result, Policy: policy,
	}
	if err := candidate.validateAgainst(base); err != nil {
		return shadowPaperCandidate{}, err
	}
	return candidate, nil
}

func (candidate shadowPaperCandidate) validateAgainst(base shadow.Policy) error {
	if candidate.Version != shadowPaperCandidateVersion ||
		candidate.Status != "paper_candidate" || candidate.Authorized ||
		candidate.Promotable || !candidate.PaperOnly {
		return errors.New("shadow paper candidate safety markers are invalid")
	}
	baseFingerprint, err := base.Fingerprint()
	if err != nil {
		return err
	}
	if candidate.BasePolicySHA256 != baseFingerprint {
		return errors.New("shadow paper candidate was derived from a different base policy")
	}
	candidateFingerprint, err := candidate.Policy.Fingerprint()
	if err != nil {
		return err
	}
	if candidate.CandidatePolicySHA256 != candidateFingerprint {
		return errors.New("shadow paper candidate policy fingerprint does not match")
	}
	if candidate.Policy.ReturnTrigger == nil || base.Adaptive == nil && !base.IsSell() {
		return errors.New("shadow paper candidate is not a supported round trip")
	}
	expected, err := shadowSearchCandidatePolicy(base, candidate.Research.Candidate)
	if err != nil {
		return err
	}
	expectedFingerprint, err := expected.Fingerprint()
	if err != nil {
		return err
	}
	if expectedFingerprint != candidateFingerprint {
		return errors.New("shadow paper candidate changed fields outside the searched thresholds")
	}
	if err := candidate.TrainingJournal.validate(); err != nil {
		return errors.New("shadow paper candidate training provenance is invalid")
	}
	if err := candidate.ValidationJournal.validate(); err != nil {
		return errors.New("shadow paper candidate validation provenance is invalid")
	}
	if candidate.Hypothesis != nil {
		if err := candidate.Hypothesis.validate(); err != nil {
			return errors.New("shadow paper candidate hypothesis is invalid")
		}
		if candidate.Hypothesis.Version == 2 && (candidate.ResearchPacket == nil ||
			candidate.ResearchPacket.RecordedObservations == nil ||
			candidate.Hypothesis.RecordedEvidenceSHA256 != candidate.ResearchPacket.RecordedObservations.ContentSHA256) {
			return errors.New("recorded hypothesis lacks its bound packet")
		}
	}
	if candidate.ResearchPacket != nil {
		if candidate.ResearchPacket.Version == researchpacket.RecordedVersion &&
			(candidate.Hypothesis == nil || candidate.Hypothesis.Version != 2) {
			return errors.New("recorded research packet lacks its recorded hypothesis")
		}
		if candidate.Hypothesis == nil || validateShadowResearchPacketBinding(
			*candidate.ResearchPacket, base, candidate.Policy, candidate.Research,
		) != nil {
			return errors.New("shadow paper candidate research packet binding is invalid")
		}
	}
	if candidate.Experiment != nil {
		if err := candidate.Experiment.validate(candidate.Policy); err != nil {
			return err
		}
		if err := candidate.Experiment.validatePreference(base, candidate.Policy); err != nil {
			return err
		}
		if candidate.Research.WalkForward != nil {
			for _, fold := range candidate.Research.WalkForward.Folds {
				policy, err := shadowSearchCandidatePolicy(base, fold.Candidate)
				if err != nil || candidate.Experiment.validatePreference(base, policy) != nil {
					return errors.New("shadow paper experiment preference does not bind every walk-forward fold")
				}
			}
		}
	}
	trainAt, _ := time.Parse("2006-01-02", candidate.TrainingJournal.Day)
	validationAt, _ := time.Parse("2006-01-02", candidate.ValidationJournal.Day)
	if !validationAt.After(trainAt) ||
		candidate.Research.TrainDay != candidate.TrainingJournal.Day ||
		candidate.Research.ValidationDay != candidate.ValidationJournal.Day {
		return errors.New("shadow paper candidate journal days do not match its research split")
	}
	result := candidate.Research
	if result.Status != "research_only" || result.EvaluationMode != shadow.EvaluationResetDaily ||
		result.Authorized || result.Promotable ||
		!result.PoolModelled || result.AssumedSpreadBPS == 0 ||
		result.AssumedSpreadBPS >= 10_000 || result.CandidatesEvaluated == 0 ||
		result.Training.FullRoundTrips == 0 ||
		result.CandidatePolicySHA256 != candidate.CandidatePolicySHA256 {
		return errors.New("shadow paper candidate research result is invalid")
	}
	if result.WalkForward != nil {
		if err := validateShadowWalkForwardAdmission(base, result); err != nil {
			return err
		}
		last := result.WalkForward.Folds[len(result.WalkForward.Folds)-1]
		if candidate.TrainingJournal != last.TrainingJournal ||
			candidate.ValidationJournal != last.ValidationJournal {
			return errors.New("shadow paper candidate does not bind the final walk-forward journals")
		}
	}
	return nil
}

func (provenance shadowJournalProvenance) validate() error {
	if _, err := time.Parse("2006-01-02", provenance.Day); err != nil ||
		provenance.Records < 2 || len(provenance.ChainHeadSHA256) != 64 ||
		strings.ToLower(provenance.ChainHeadSHA256) != provenance.ChainHeadSHA256 {
		return errors.New("invalid shadow journal provenance")
	}
	if _, err := hex.DecodeString(provenance.ChainHeadSHA256); err != nil {
		return errors.New("invalid shadow journal provenance")
	}
	return nil
}

func loadShadowPaperCandidate(
	path string, base shadow.Policy,
) (shadowPaperCandidate, error) {
	candidate, _, err := loadBoundShadowPaperCandidate(path, base)
	return candidate, err
}

func loadBoundShadowPaperCandidate(
	path string, base shadow.Policy,
) (shadowPaperCandidate, string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return shadowPaperCandidate{}, "", errors.New("shadow paper candidate path must be absolute and clean")
	}
	raw, err := securefile.ReadPrivate(path, maxInputBytes)
	if err != nil {
		return shadowPaperCandidate{}, "", errors.New("could not read the shadow paper candidate")
	}
	var candidate shadowPaperCandidate
	if err := strictjson.Decode(raw, &candidate); err != nil {
		return shadowPaperCandidate{}, "", errors.New("shadow paper candidate JSON is invalid")
	}
	if err := candidate.validateAgainst(base); err != nil {
		return shadowPaperCandidate{}, "", err
	}
	digest := sha256.Sum256(raw)
	return candidate, hex.EncodeToString(digest[:]), nil
}

func loadSelectedShadowCandidate(
	pointer string, base shadow.Policy,
) (shadowPaperCandidate, string, error) {
	candidate, path, _, err := loadBoundSelectedShadowCandidate(pointer, base)
	return candidate, path, err
}

func loadBoundSelectedShadowCandidate(
	pointer string, base shadow.Policy,
) (shadowPaperCandidate, string, string, error) {
	candidate, path, digest, _, err := loadBoundShadowCandidateSelection(pointer, base)
	return candidate, path, digest, err
}

func loadBoundShadowCandidateSelection(
	pointer string, base shadow.Policy,
) (shadowPaperCandidate, string, string, shadowCandidatePointer, error) {
	if pointer == "" || !filepath.IsAbs(pointer) || filepath.Clean(pointer) != pointer {
		return shadowPaperCandidate{}, "", "", shadowCandidatePointer{}, errors.New("shadow candidate pointer must be absolute and clean")
	}
	raw, err := securefile.ReadPrivate(pointer, shadowCandidatePointerBytes)
	if err != nil {
		return shadowPaperCandidate{}, "", "", shadowCandidatePointer{}, errors.New("could not read the shadow candidate pointer")
	}
	var selected shadowCandidatePointer
	if err := strictjson.Decode(raw, &selected); err != nil ||
		!validShadowCandidatePointer(selected, pointer) {
		return shadowPaperCandidate{}, "", "", shadowCandidatePointer{}, errors.New("shadow candidate pointer is invalid")
	}
	candidate, digest, err := loadBoundShadowPaperCandidate(selected.CandidatePath, base)
	if err != nil {
		return shadowPaperCandidate{}, "", "", shadowCandidatePointer{}, err
	}
	if digest != selected.CandidateSHA256 ||
		candidate.CandidatePolicySHA256 != selected.CandidatePolicySHA256 {
		return shadowPaperCandidate{}, "", "", shadowCandidatePointer{}, errors.New("shadow candidate pointer no longer matches its selected artifact")
	}
	return candidate, selected.CandidatePath, digest, selected, nil
}

func validShadowCandidatePointer(selected shadowCandidatePointer, pointer string) bool {
	return selected.Version == shadowPaperCandidateVersion &&
		selected.CandidatePath != "" && filepath.IsAbs(selected.CandidatePath) &&
		filepath.Clean(selected.CandidatePath) == selected.CandidatePath &&
		selected.CandidatePath != pointer && validLowerSHA256(selected.CandidateSHA256) &&
		validLowerSHA256(selected.CandidatePolicySHA256) &&
		(selected.RestoredFromSHA256 == "" || validLowerSHA256(selected.RestoredFromSHA256)) &&
		validShadowCandidateSelection(
			selected.SelectedAt, selected.EligibleFrom,
			selected.ChallengeDays, selected.ChallengeGateVersion,
		)
}

func validShadowCandidateSelection(
	selectedAt, eligibleFrom *time.Time, challengeDays, gateVersion uint32,
) bool {
	if selectedAt == nil || eligibleFrom == nil {
		return selectedAt == nil && eligibleFrom == nil && challengeDays == 0 && gateVersion == 0
	}
	selected := selectedAt.UTC()
	eligible := eligibleFrom.UTC()
	want := time.Date(selected.Year(), selected.Month(), selected.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	return !selected.IsZero() && eligible.Equal(want) && eligible.After(selected) &&
		challengeDays >= 7 && challengeDays <= 3650 && gateVersion == shadowChallengeGateVersion
}

func validLowerSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func runShadowSelect(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow select", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "base shadow policy JSON")
	candidatePath := flags.String("candidate", "", "immutable paper candidate JSON")
	pointerPath := flags.String("pointer", "", "paper candidate pointer")
	lifecycleLock := flags.String("lifecycle-lock", "", "shared paper lifecycle lock")
	initial := flags.Bool("initial", false, "refuse to replace an existing paper selection")
	evidenceDir := flags.String("evidence-dir", "", "completed journals for initial admission")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowSelectUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || !absoluteClean(*policyPath) ||
		*candidatePath == "" || *pointerPath == "" || *lifecycleLock == "" ||
		!filepath.IsAbs(*candidatePath) || filepath.Clean(*candidatePath) != *candidatePath ||
		!filepath.IsAbs(*pointerPath) || filepath.Clean(*pointerPath) != *pointerPath ||
		!absoluteClean(*lifecycleLock) || *candidatePath == *pointerPath ||
		*policyPath == *candidatePath || *policyPath == *pointerPath ||
		*policyPath == *lifecycleLock || *lifecycleLock == *candidatePath ||
		*lifecycleLock == *pointerPath {
		return errors.New("shadow select requires distinct absolute policy, candidate, pointer, and lifecycle lock paths")
	}
	if *initial && !absoluteClean(*evidenceDir) {
		return errors.New("shadow select --initial requires an absolute clean --evidence-dir")
	}
	if !*initial && *evidenceDir != "" {
		return errors.New("shadow select --evidence-dir requires --initial")
	}
	base, err := loadActiveShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	candidate, candidateSHA256, err := loadBoundShadowPaperCandidate(*candidatePath, base)
	if err != nil {
		return err
	}
	if err := validateActiveShadowPolicy(candidate.Policy); err != nil {
		return err
	}
	if *initial {
		if err := validateInitialShadowCandidate(base, candidate, *evidenceDir); err != nil {
			return err
		}
	}
	if err := withShadowLifecycleLock(*lifecycleLock, func() error {
		if *initial {
			if _, err := os.Lstat(*pointerPath); err == nil {
				return errors.New("an initial shadow candidate is already selected")
			} else if !errors.Is(err, os.ErrNotExist) {
				return errors.New("could not inspect the initial shadow candidate pointer")
			}
		}
		return replaceShadowCandidatePointer(
			*pointerPath, *candidatePath, candidateSHA256, candidate.CandidatePolicySHA256,
		)
	}); err != nil {
		return errors.New("could not update the shadow candidate pointer")
	}
	return json.NewEncoder(output).Encode(struct {
		Status                string `json:"status"`
		Authorized            bool   `json:"authorized"`
		PaperOnly             bool   `json:"paper_only"`
		CandidatePolicySHA256 string `json:"candidate_policy_sha256"`
		Effective             string `json:"effective"`
	}{
		Status: "paper_candidate_selected", PaperOnly: true,
		CandidatePolicySHA256: candidate.CandidatePolicySHA256,
		Effective:             "runner_start_or_next_utc_day",
	})
}

func validateInitialShadowCandidate(
	base shadow.Policy, candidate shadowPaperCandidate, evidenceDir string,
) error {
	ticks := make([][]shadow.Tick, 2)
	for index, provenance := range []shadowJournalProvenance{
		candidate.TrainingJournal, candidate.ValidationJournal,
	} {
		dayStart, err := time.Parse("2006-01-02", provenance.Day)
		if err != nil {
			return errors.New("initial shadow candidate evidence day is invalid")
		}
		observed, current, err := readShadowSearchJournal(
			filepath.Join(evidenceDir, "shadow-"+provenance.Day+".jsonl"), provenance.Day, base,
		)
		if err != nil {
			return fmt.Errorf("replay initial shadow candidate evidence: %w", err)
		}
		if current != provenance {
			return errors.New("initial shadow candidate evidence no longer matches its bound journal")
		}
		if coverage := shadowWalkForwardObservableBPS(observed, dayStart, base.Tick()); coverage < shadowInitialCoverageBPS {
			return fmt.Errorf(
				"initial shadow candidate evidence has only %d.%02d%% observable coverage",
				coverage/100, coverage%100,
			)
		}
		ticks[index] = observed
	}

	training, err := shadow.ReplayRoundTripTicks(
		candidate.Policy, ticks[0],
		modelledPool(candidate.Policy, candidate.Research.AssumedSpreadBPS, candidate.Policy.SlippageBPS),
	)
	if err != nil {
		return fmt.Errorf("replay initial shadow candidate training evidence: %w", err)
	}
	trainingScore, err := scoreShadowRoundTripResult(training)
	if err != nil || trainingScore != candidate.Research.Training {
		return errors.New("initial shadow candidate training result does not match its evidence")
	}

	spread := candidate.Research.AssumedSpreadBPS
	if spread > 4_999 {
		return errors.New("initial shadow candidate spread is too large for the required doubled-spread stress")
	}
	for index, testedSpread := range []uint64{spread, spread * 2} {
		validation, err := shadow.ReplayRoundTripTicks(
			candidate.Policy, ticks[1],
			modelledPool(candidate.Policy, testedSpread, candidate.Policy.SlippageBPS),
		)
		if err != nil {
			return fmt.Errorf("replay initial shadow candidate validation evidence at %d bps: %w", testedSpread, err)
		}
		score, err := scoreShadowRoundTripResult(validation)
		if err != nil {
			return err
		}
		if index == 0 && score != candidate.Research.Validation {
			return errors.New("initial shadow candidate validation result does not match its evidence")
		}
		closing, err := validation.Ledger.EquityMicros(validation.ClosingPrice)
		if err != nil {
			return err
		}
		hold, err := validation.Ledger.HoldBenchmarkMicros(validation.ClosingPrice)
		if err != nil {
			return err
		}
		if score.FullRoundTrips < shadowInitialRoundTrips {
			return fmt.Errorf("initial shadow candidate completed fewer than two validation round trips at %d bps", testedSpread)
		}
		if closing <= validation.Ledger.OpeningEquityMicros {
			return fmt.Errorf("initial shadow candidate validation net return is not positive at %d bps", testedSpread)
		}
		if closing <= hold {
			return fmt.Errorf("initial shadow candidate validation did not beat holding at %d bps", testedSpread)
		}
		if !initialShadowDrawdownCompliant(candidate.Policy, validation.Ledger) {
			return fmt.Errorf("initial shadow candidate exceeded its adaptive drawdown limit at %d bps", testedSpread)
		}
	}
	return nil
}

func initialShadowDrawdownCompliant(policy shadow.Policy, ledger shadow.Ledger) bool {
	if policy.Adaptive == nil {
		return true
	}
	return ledger.MaxDrawdownBPS <= policy.Adaptive.MaxDrawdownBPS
}

func replaceShadowCandidatePointer(
	pointerPath, candidatePath, candidateSHA256, candidatePolicySHA256 string,
) error {
	return replaceShadowCandidatePointerSelected(
		pointerPath, candidatePath, candidateSHA256, candidatePolicySHA256, time.Time{}, 0,
	)
}

func replaceShadowCandidatePointerSelected(
	pointerPath, candidatePath, candidateSHA256, candidatePolicySHA256 string,
	selectedAt time.Time, challengeDays uint32,
) error {
	var selectedAtField, eligibleFromField *time.Time
	var gateVersion uint32
	var restoredFromSHA256 string
	current, readErr := securefile.ReadPrivate(pointerPath, shadowCandidatePointerBytes)
	if readErr == nil {
		var selected shadowCandidatePointer
		if strictjson.Decode(current, &selected) == nil &&
			validShadowCandidatePointer(selected, pointerPath) &&
			selected.CandidateSHA256 == candidateSHA256 &&
			selected.CandidatePolicySHA256 == candidatePolicySHA256 {
			restoredFromSHA256 = selected.RestoredFromSHA256
		}
	}
	if !selectedAt.IsZero() {
		selected := selectedAt.UTC()
		eligible := time.Date(
			selected.Year(), selected.Month(), selected.Day(), 0, 0, 0, 0, time.UTC,
		).AddDate(0, 0, 1)
		selectedAtField, eligibleFromField = &selected, &eligible
		gateVersion = shadowChallengeGateVersion
	} else {
		challengeDays = 0
	}
	pointer, err := json.MarshalIndent(shadowCandidatePointer{
		Version: shadowPaperCandidateVersion, CandidatePath: candidatePath,
		CandidateSHA256:       candidateSHA256,
		CandidatePolicySHA256: candidatePolicySHA256,
		RestoredFromSHA256:    restoredFromSHA256,
		SelectedAt:            selectedAtField,
		EligibleFrom:          eligibleFromField,
		ChallengeDays:         challengeDays,
		ChallengeGateVersion:  gateVersion,
	}, "", "  ")
	if err != nil {
		return err
	}
	encoded := append(pointer, '\n')
	if readErr == nil && string(current) == string(encoded) {
		return nil
	}
	return securefile.ReplacePrivate(pointerPath, encoded, shadowCandidatePointerBytes)
}

func writeShadowPaperCandidate(path string, candidate shadowPaperCandidate) error {
	encoded, err := encodeShadowPaperCandidate(candidate)
	if err != nil {
		return err
	}
	if err := securefile.CreatePrivate(path, encoded, maxInputBytes); err != nil {
		return errors.New("could not write the immutable shadow paper candidate")
	}
	return nil
}

func encodeShadowPaperCandidate(candidate shadowPaperCandidate) ([]byte, error) {
	encoded, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}
