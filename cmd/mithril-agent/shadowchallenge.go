package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const shadowChallengeUsage = `Usage: mithril-agent shadow challenge --policy BASE
       --champion-pointer PATH --challenger PATH
       --champion-dir PATH --challenger-dir PATH --days N

Replays the same immediately preceding complete Mainnet days for the selected
paper champion and one immutable challenger. Because this manual command does
not carry a pre-selection eligibility pointer, its result is retrospective and
cannot qualify a challenger. It never edits the pointer; it cannot authorize, sign, submit, or enable a live strategy.`

const shadowChallengeGateVersion = uint32(1)

type shadowChallengeResult struct {
	Version                          uint32    `json:"version"`
	Status                           string    `json:"status"`
	EvaluationMode                   string    `json:"evaluation_mode"`
	Authorized                       bool      `json:"authorized"`
	Promotable                       bool      `json:"promotable"`
	PaperOnly                        bool      `json:"paper_only"`
	RequiresOperatorDecision         bool      `json:"requires_operator_decision"`
	PointerUpdated                   bool      `json:"pointer_updated"`
	From                             time.Time `json:"from"`
	To                               time.Time `json:"to"`
	CompleteDays                     uint32    `json:"complete_days"`
	ChampionCandidatePath            string    `json:"champion_candidate_path"`
	ChampionCandidateSHA256          string    `json:"champion_candidate_sha256"`
	ChampionPolicySHA256             string    `json:"champion_policy_sha256"`
	ChallengerCandidatePath          string    `json:"challenger_candidate_path"`
	ChallengerCandidateSHA256        string    `json:"challenger_candidate_sha256"`
	ChallengerPolicySHA256           string    `json:"challenger_policy_sha256"`
	ChallengerFullRoundTrips         uint64    `json:"challenger_full_round_trips"`
	RequiredFullRoundTrips           uint64    `json:"required_full_round_trips"`
	ChallengerDailyWins              uint32    `json:"challenger_daily_wins"`
	RequiredDailyWins                uint32    `json:"required_daily_wins"`
	AggregateAdvantageMicros         int64     `json:"aggregate_advantage_micros"`
	RequiredAggregateAdvantageMicros uint64    `json:"required_aggregate_advantage_micros"`
	Reasons                          []string  `json:"reasons,omitempty"`
}

var shadowChallengeAfterLoad = func() {}

var errShadowChallengeEvidencePending = errors.New("shadow challenge evidence is incomplete")

func runShadowChallenge(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow challenge", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "immutable base shadow policy")
	championPointer := flags.String("champion-pointer", "", "selected champion pointer")
	challengerPath := flags.String("challenger", "", "immutable challenger candidate")
	championRoot := flags.String("champion-dir", "", "champion run root")
	challengerRoot := flags.String("challenger-dir", "", "challenger run root")
	days := flags.Uint("days", 0, "paired complete UTC days, 7..3650")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowChallengeUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *days < 7 || *days > 3650 ||
		!absoluteClean(*championPointer) || !absoluteClean(*challengerPath) ||
		!absoluteClean(*championRoot) || !absoluteClean(*challengerRoot) ||
		*championRoot == *challengerRoot {
		return errors.New("shadow challenge requires distinct absolute run roots, absolute candidate paths, and --days from 7 to 3650")
	}
	base, err := loadShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	result, err := evaluateShadowChallenge(
		base, *championPointer, *challengerPath, *championRoot, *challengerRoot,
		uint32(*days), time.Now().UTC(), time.Time{},
	)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}

func evaluateShadowChallenge(
	base shadow.Policy,
	championPointer, challengerPath, championRoot, challengerRoot string,
	days uint32, now, eligibleFrom time.Time,
) (shadowChallengeResult, error) {
	pointerBefore, err := securefile.ReadPrivate(championPointer, shadowCandidatePointerBytes)
	if err != nil {
		return shadowChallengeResult{}, errors.New("could not read the champion pointer")
	}
	champion, championPath, championSHA256, err := loadBoundSelectedShadowCandidate(championPointer, base)
	if err != nil {
		return shadowChallengeResult{}, err
	}
	challenger, challengerSHA256, err := loadBoundShadowPaperCandidate(challengerPath, base)
	if err != nil {
		return shadowChallengeResult{}, err
	}
	if champion.CandidatePolicySHA256 == challenger.CandidatePolicySHA256 {
		return shadowChallengeResult{}, errors.New("shadow champion and challenger policies must be distinct")
	}
	shadowChallengeAfterLoad()
	evidenceNow := now
	if !eligibleFrom.IsZero() {
		cutoff := eligibleFrom.UTC().AddDate(0, 0, int(days))
		if now.Before(cutoff) {
			return shadowChallengeResult{}, fmt.Errorf(
				"%w: fixed paired evidence window is incomplete", errShadowChallengeEvidencePending,
			)
		}
		// Automated challengers get one immutable forward window. Without this
		// anchor a later bad day could turn a qualified challenger into a rejected
		// one and allow the research loop to replace evidence awaiting an operator.
		evidenceNow = cutoff
	}

	championReports, err := loadShadowReviewReports(
		champion.Policy, filepath.Join(championRoot, champion.CandidatePolicySHA256),
		days, evidenceNow,
	)
	if err != nil {
		return shadowChallengeResult{}, fmt.Errorf("%w: champion evidence: %v", errShadowChallengeEvidencePending, err)
	}
	challengerReports, err := loadShadowReviewReports(
		challenger.Policy, filepath.Join(challengerRoot, challenger.CandidatePolicySHA256),
		days, evidenceNow,
	)
	if err != nil {
		return shadowChallengeResult{}, fmt.Errorf("%w: challenger evidence: %v", errShadowChallengeEvidencePending, err)
	}
	if !eligibleFrom.IsZero() && championReports[0].From.Before(eligibleFrom) {
		return shadowChallengeResult{}, fmt.Errorf(
			"%w: paired evidence predates challenger eligibility", errShadowChallengeEvidencePending,
		)
	}
	result, err := qualifyShadowChallenger(
		champion, challenger, championReports, challengerReports,
	)
	if err != nil {
		return shadowChallengeResult{}, err
	}
	result = constrainRetrospectiveChallenge(result, eligibleFrom)
	result.ChampionCandidatePath = championPath
	result.ChampionCandidateSHA256 = championSHA256
	result.ChampionPolicySHA256 = champion.CandidatePolicySHA256
	result.ChallengerCandidatePath = challengerPath
	result.ChallengerCandidateSHA256 = challengerSHA256
	result.ChallengerPolicySHA256 = challenger.CandidatePolicySHA256

	pointerAfter, err := securefile.ReadPrivate(championPointer, shadowCandidatePointerBytes)
	if err != nil || !bytes.Equal(pointerBefore, pointerAfter) {
		return shadowChallengeResult{}, errors.New("champion pointer changed while the challenge was running")
	}
	_, finalChampionPath, finalChampionSHA256, err := loadBoundSelectedShadowCandidate(championPointer, base)
	if err != nil || finalChampionPath != championPath || finalChampionSHA256 != championSHA256 {
		return shadowChallengeResult{}, errors.New("champion candidate changed while the challenge was running")
	}
	_, finalChallengerSHA256, err := loadBoundShadowPaperCandidate(challengerPath, base)
	if err != nil || finalChallengerSHA256 != challengerSHA256 {
		return shadowChallengeResult{}, errors.New("challenger candidate changed while the challenge was running")
	}
	return result, nil
}

func constrainRetrospectiveChallenge(
	result shadowChallengeResult, eligibleFrom time.Time,
) shadowChallengeResult {
	if eligibleFrom.IsZero() && result.Status == "challenger_qualified_for_operator_paper_selection" {
		result.Status = "retrospective_comparison_not_forward_qualified"
		result.Reasons = append(result.Reasons, "preselection_evidence_not_forward_qualified")
	}
	return result
}

func absoluteClean(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func qualifyShadowChallenger(
	champion, challenger shadowPaperCandidate,
	championReports, challengerReports []shadow.Report,
) (shadowChallengeResult, error) {
	if len(championReports) < 7 || len(championReports) != len(challengerReports) {
		return shadowChallengeResult{}, errors.New("shadow challenge needs at least seven paired reports")
	}
	championValidation, err := time.Parse("2006-01-02", champion.ValidationJournal.Day)
	if err != nil {
		return shadowChallengeResult{}, errors.New("champion validation day is invalid")
	}
	challengerValidation, err := time.Parse("2006-01-02", challenger.ValidationJournal.Day)
	if err != nil {
		return shadowChallengeResult{}, errors.New("challenger validation day is invalid")
	}
	championSummary, err := summarizeShadowReview(champion.Policy, championReports)
	if err != nil {
		return shadowChallengeResult{}, fmt.Errorf("champion evidence: %w", err)
	}
	challengerSummary, err := summarizeShadowReview(challenger.Policy, challengerReports)
	if err != nil {
		return shadowChallengeResult{}, fmt.Errorf("challenger evidence: %w", err)
	}
	if !championReports[0].From.After(championValidation) ||
		!championReports[0].From.After(challengerValidation) {
		return shadowChallengeResult{}, errors.New("paired challenge evidence must be after both validation days")
	}

	result := shadowChallengeResult{
		Version: 1, Status: "challenger_not_qualified", PaperOnly: true,
		EvaluationMode:           shadow.EvaluationResetDaily,
		RequiresOperatorDecision: true,
		From:                     championSummary.From, To: championSummary.To,
		CompleteDays:           championSummary.CompleteDays,
		RequiredFullRoundTrips: max(uint64(3), uint64(len(challengerReports)+1)/2),
		RequiredDailyWins:      uint32(len(challengerReports)/2 + 1),
	}
	var championCapital, challengerCapital uint64
	for index, challengerReport := range challengerReports {
		championReport := championReports[index]
		if !championReport.From.Equal(challengerReport.From) ||
			!championReport.To.Equal(challengerReport.To) {
			return shadowChallengeResult{}, errors.New("champion and challenger evidence windows do not match")
		}
		if !addShadowReviewCounter(&championCapital, championReport.OpeningEquityMicros) ||
			!addShadowReviewCounter(&challengerCapital, challengerReport.OpeningEquityMicros) ||
			!addShadowReviewCounter(&result.ChallengerFullRoundTrips, challengerReport.Counts.Fills/2) {
			return shadowChallengeResult{}, errors.New("shadow challenge counters overflow")
		}
		if challengerReport.VersusHoldMicros > championReport.VersusHoldMicros {
			result.ChallengerDailyWins++
		}
	}
	if max(championCapital, challengerCapital) == 0 {
		return shadowChallengeResult{}, errors.New("shadow challenge opening equity is zero")
	}
	capital := max(championCapital, challengerCapital)
	result.RequiredAggregateAdvantageMicros = capital / 1000
	if capital%1000 != 0 {
		result.RequiredAggregateAdvantageMicros++
	}
	result.AggregateAdvantageMicros, err = checkedDifference(
		challengerSummary.VersusHoldMicros, championSummary.VersusHoldMicros,
	)
	if err != nil {
		return shadowChallengeResult{}, err
	}
	if championSummary.Missed != 0 {
		result.Reasons = append(result.Reasons, "champion_has_missed_decisions")
	}
	if challengerSummary.Missed != 0 {
		result.Reasons = append(result.Reasons, "challenger_has_missed_decisions")
	}
	if result.ChallengerFullRoundTrips < result.RequiredFullRoundTrips {
		result.Reasons = append(result.Reasons, "insufficient_challenger_round_trips")
	}
	if challengerSummary.VersusHoldMicros <= 0 {
		result.Reasons = append(result.Reasons, "challenger_not_positive_vs_hold")
	}
	if result.ChallengerDailyWins < result.RequiredDailyWins {
		result.Reasons = append(result.Reasons, "no_strict_daily_majority")
	}
	if result.AggregateAdvantageMicros < 0 ||
		uint64(result.AggregateAdvantageMicros) < result.RequiredAggregateAdvantageMicros {
		result.Reasons = append(result.Reasons, "advantage_below_ten_bps")
	}
	if challengerSummary.MaximumDrawdownMicros > championSummary.MaximumDrawdownMicros {
		result.Reasons = append(result.Reasons, "drawdown_regressed")
	}
	if len(result.Reasons) == 0 {
		result.Status = "challenger_qualified_for_operator_paper_selection"
	}
	return result, nil
}

func checkedDifference(left, right int64) (int64, error) {
	if right > 0 && left < math.MinInt64+right ||
		right < 0 && left > math.MaxInt64+right {
		return 0, errors.New("shadow challenge result overflows")
	}
	return left - right, nil
}
