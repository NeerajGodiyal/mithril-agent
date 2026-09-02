package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"slices"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const (
	shadowWalkForwardVersion = uint32(1)
	shadowWalkForwardWindows = 7
)

const shadowSearchUsage = `Usage: mithril-agent shadow search --policy PATH --dir PATH
                                   --train-day YYYY-MM-DD
                                   --validation-day YYYY-MM-DD
                                   [--base-policy PATH]
                                   [--instruction PATH]
                                   [--spread-bps N] [--candidate-out PATH]

Chooses bounded strategy parameters from one completed UTC day's recorded
prices, then scores that exact candidate on a later completed, untouched UTC
day using a fresh reset book on each day. Fixed policies search sell/buy
thresholds; adaptive policies search windows, tighter signal hurdles, and a
post-fill cooldown from one-half to twice the base value. The signal hurdle is
never lowered and risk limits remain fixed. The pool is modelled at the
named spread. The result is research_only and can never authorize a trade.
When --candidate-out is set, it also writes one immutable, paper-only policy
bound to the base policy and both journals' verified chain heads.`

type shadowSearchScore struct {
	FullRoundTrips    uint64 `json:"full_round_trips"`
	VersusHoldMicros  int64  `json:"versus_hold_micros"`
	MaxDrawdownMicros uint64 `json:"max_drawdown_micros"`
}

type shadowSearchCandidate struct {
	SellAtMicros uint64                 `json:"sell_at_micros,omitempty"`
	SellAtUSD    string                 `json:"sell_at_usd,omitempty"`
	BuyAtMicros  uint64                 `json:"buy_at_micros,omitempty"`
	BuyAtUSD     string                 `json:"buy_at_usd,omitempty"`
	Adaptive     *shadow.AdaptivePolicy `json:"adaptive,omitempty"`
}

type shadowSearchResult struct {
	Status                string                      `json:"status"`
	EvaluationMode        string                      `json:"evaluation_mode"`
	Authorized            bool                        `json:"authorized"`
	Promotable            bool                        `json:"promotable"`
	PoolModelled          bool                        `json:"pool_modelled"`
	AssumedSpreadBPS      uint64                      `json:"assumed_spread_bps"`
	TrainDay              string                      `json:"train_day"`
	ValidationDay         string                      `json:"validation_day"`
	CandidatesEvaluated   uint64                      `json:"candidates_evaluated"`
	CandidatePolicySHA256 string                      `json:"candidate_policy_sha256,omitempty"`
	Candidate             shadowSearchCandidate       `json:"candidate"`
	Training              shadowSearchScore           `json:"training"`
	Validation            shadowSearchScore           `json:"validation"`
	WalkForward           *shadowWalkForwardAdmission `json:"walk_forward,omitempty"`
	NextStep              string                      `json:"next_step"`
}

type shadowWalkForwardDay struct {
	Ticks         []shadow.Tick
	Provenance    shadowJournalProvenance
	ObservableBPS int32
}

type shadowWalkForwardFold struct {
	TrainingJournal         shadowJournalProvenance `json:"training_journal"`
	ValidationJournal       shadowJournalProvenance `json:"validation_journal"`
	TrainingObservableBPS   int32                   `json:"training_observable_bps"`
	ValidationObservableBPS int32                   `json:"validation_observable_bps"`
	Candidate               shadowSearchCandidate   `json:"candidate"`
	CandidatePolicySHA256   string                  `json:"candidate_policy_sha256"`
	CandidatesEvaluated     uint64                  `json:"candidates_evaluated"`
	Training                shadowSearchScore       `json:"training"`
	Validation              shadowSearchScore       `json:"validation"`
}

type shadowWalkForwardAdmission struct {
	Version                   uint32                  `json:"version"`
	Status                    string                  `json:"status"`
	Windows                   uint32                  `json:"windows"`
	PositiveWindows           uint32                  `json:"positive_windows"`
	RequiredPositiveWindows   uint32                  `json:"required_positive_windows"`
	ValidationFullRoundTrips  uint64                  `json:"validation_full_round_trips"`
	RequiredFullRoundTrips    uint64                  `json:"required_full_round_trips"`
	AggregateVersusHoldMicros int64                   `json:"aggregate_versus_hold_micros"`
	TotalCandidatesEvaluated  uint64                  `json:"total_candidates_evaluated"`
	Folds                     []shadowWalkForwardFold `json:"folds"`
}

func runShadowSearch(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow search", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "shadow policy JSON")
	basePolicyPath := flags.String("base-policy", "", "immutable base policy for an iterative candidate")
	instructionPath := flags.String("instruction", "", "operator paper experiment instruction")
	directory := flags.String("dir", "", "directory holding recorded shadow journals")
	trainDay := flags.String("train-day", "", "training UTC day, YYYY-MM-DD")
	validationDay := flags.String("validation-day", "", "later validation UTC day, YYYY-MM-DD")
	spreadBPS := flags.Uint64("spread-bps", 100, "modelled pool cost each way, in basis points")
	candidateOut := flags.String("candidate-out", "", "immutable paper-only candidate output")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowSearchUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *policyPath == "" || *directory == "" ||
		*trainDay == "" || *validationDay == "" {
		return errors.New("shadow search requires --policy, --dir, --train-day and --validation-day")
	}
	if !filepath.IsAbs(*directory) || filepath.Clean(*directory) != *directory {
		return errors.New("--dir must be an absolute, clean path")
	}
	if *candidateOut != "" &&
		(!filepath.IsAbs(*candidateOut) || filepath.Clean(*candidateOut) != *candidateOut) {
		return errors.New("--candidate-out must be an absolute, clean path")
	}
	if *instructionPath != "" && (*candidateOut == "" || !absoluteClean(*instructionPath)) {
		return errors.New("--instruction requires an absolute clean path and --candidate-out")
	}
	trainAt, err := time.Parse("2006-01-02", *trainDay)
	if err != nil {
		return errors.New("--train-day must be YYYY-MM-DD")
	}
	validationAt, err := time.Parse("2006-01-02", *validationDay)
	if err != nil {
		return errors.New("--validation-day must be YYYY-MM-DD")
	}
	if !validationAt.After(trainAt) {
		return errors.New("--validation-day must be later than --train-day")
	}
	if !validationAt.Before(time.Now().UTC().Truncate(24 * time.Hour)) {
		return errors.New("--train-day and --validation-day must be complete UTC days")
	}
	if *spreadBPS == 0 || *spreadBPS >= 10_000 {
		return errors.New("--spread-bps must be between 1 and 9999")
	}

	policy, err := loadActiveShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	basePolicy := policy
	if *basePolicyPath != "" {
		basePolicy, err = loadActiveShadowPolicy(*basePolicyPath)
		if err != nil {
			return err
		}
		if err := validateShadowSearchLineage(basePolicy, policy); err != nil {
			return err
		}
	}
	if policy.Adaptive == nil && !policy.IsSell() {
		return errors.New("fixed shadow search currently requires a sell-first policy")
	}
	trainTicks, trainingProvenance, err := readShadowSearchJournal(
		filepath.Join(*directory, "shadow-"+*trainDay+".jsonl"), *trainDay, policy,
	)
	if err != nil {
		return fmt.Errorf("read training journal: %w", err)
	}
	validationTicks, validationProvenance, err := readShadowSearchJournal(
		filepath.Join(*directory, "shadow-"+*validationDay+".jsonl"), *validationDay, policy,
	)
	if err != nil {
		return fmt.Errorf("read validation journal: %w", err)
	}
	result, err := searchShadowCandidateTicks(policy, trainTicks, validationTicks, *spreadBPS)
	if err != nil {
		return err
	}
	result.TrainDay, result.ValidationDay = *trainDay, *validationDay
	if *candidateOut != "" {
		candidate, err := newShadowPaperCandidate(
			basePolicy, result, trainingProvenance, validationProvenance,
		)
		if err != nil {
			return err
		}
		if *instructionPath != "" {
			candidate.Experiment, err = loadShadowPaperExperiment(*instructionPath, candidate.Policy)
			if err != nil {
				return err
			}
			if err := candidate.validateAgainst(basePolicy); err != nil {
				return err
			}
		}
		if err := writeShadowPaperCandidate(*candidateOut, candidate); err != nil {
			return err
		}
		result.CandidatePolicySHA256 = candidate.CandidatePolicySHA256
	}
	return json.NewEncoder(output).Encode(result)
}

func validateShadowSearchLineage(base, observed shadow.Policy) error {
	baseFingerprint, err := base.Fingerprint()
	if err != nil {
		return err
	}
	observedFingerprint, err := observed.Fingerprint()
	if err != nil {
		return err
	}
	if observedFingerprint == baseFingerprint {
		return nil
	}
	if base.Adaptive != nil || observed.Adaptive != nil {
		if base.Adaptive == nil || observed.Adaptive == nil ||
			validateAdaptiveCandidateDelta(*base.Adaptive, *observed.Adaptive) != nil {
			return errors.New("shadow adaptive search policy changed fields outside the candidate parameters")
		}
		expected := base
		adaptive := *observed.Adaptive
		expected.Adaptive = &adaptive
		expectedFingerprint, err := expected.Fingerprint()
		if err != nil || expectedFingerprint != observedFingerprint {
			return errors.New("shadow adaptive search policy changed fields outside the candidate parameters")
		}
		return nil
	}
	if !base.IsSell() || observed.ReturnTrigger == nil {
		return errors.New("shadow search policy is not derived from the immutable base policy")
	}
	expected := shadowSearchPolicy(
		base, observed.Trigger.ThresholdMicros, observed.ReturnTrigger.ThresholdMicros,
	)
	expectedFingerprint, err := expected.Fingerprint()
	if err != nil || expectedFingerprint != observedFingerprint {
		return errors.New("shadow search policy changed fields outside the candidate thresholds")
	}
	return nil
}

func readShadowSearchJournal(
	path, day string, policy shadow.Policy,
) ([]shadow.Tick, shadowJournalProvenance, error) {
	records, err := journal.ReadRecords(path)
	if err != nil {
		return nil, shadowJournalProvenance{}, err
	}
	if err := validateShadowJournalDay(path, records); err != nil {
		return nil, shadowJournalProvenance{}, err
	}
	ticks, err := shadowTicksFrom(records, policy, false)
	if err != nil {
		return nil, shadowJournalProvenance{}, err
	}
	if len(records) == 0 {
		return nil, shadowJournalProvenance{}, errors.New("the journal contains no records")
	}
	for _, tick := range ticks {
		if !tick.PeriodClose && tick.PriceMicros != 0 &&
			(tick.PrimaryPrice == nil || tick.SecondaryPrice == nil) {
			return nil, shadowJournalProvenance{}, errors.New("the shadow search journal predates replayable market source evidence")
		}
	}
	if filepath.Base(path) != "shadow-"+day+".jsonl" {
		return nil, shadowJournalProvenance{}, errors.New("the shadow search journal does not match the requested UTC day")
	}
	dayStart, err := time.Parse("2006-01-02", day)
	if err != nil || len(ticks) == 0 ||
		!validShadowSearchClose(ticks[len(ticks)-1], dayStart.Add(24*time.Hour-time.Nanosecond)) {
		return nil, shadowJournalProvenance{}, errors.New("the shadow search journal has no valid terminal close")
	}
	if _, err := shadow.Replay(policy, ticks); err != nil {
		return nil, shadowJournalProvenance{}, fmt.Errorf("replay shadow search journal: %w", err)
	}
	provenance := shadowJournalProvenance{
		Day: day, Records: len(records),
		ChainHeadSHA256: records[len(records)-1].Hash,
	}
	if err := provenance.validate(); err != nil {
		return nil, shadowJournalProvenance{}, err
	}
	return ticks, provenance, nil
}

func validShadowSearchClose(tick shadow.Tick, expectedAt time.Time) bool {
	if !tick.PeriodClose || tick.Triggered || tick.Deferred || tick.Fill != nil ||
		tick.DecisionQuote != nil || tick.DecisionMissed || tick.Reason != "" ||
		tick.PrimaryPrice != nil || tick.SecondaryPrice != nil ||
		tick.NativeFeePriceMicros != 0 || tick.NativeFeePrimary != nil || tick.NativeFeeSecondary != nil ||
		tick.QuoteLowerMicros != 0 || tick.QuoteUpperMicros != 0 ||
		!tick.At.Equal(expectedAt) {
		return false
	}
	if tick.Event == shadow.EventClosed {
		return tick.PriceMicros == 0 && tick.EquityMicros == 0
	}
	return tick.Event == shadow.EventMissed && tick.PriceMicros != 0
}

func searchShadowCandidate(
	policy shadow.Policy, trainPrices, validationPrices []uint64, spreadBPS uint64,
) (shadowSearchResult, error) {
	return searchShadowCandidateScored(
		policy, trainPrices, validationPrices, spreadBPS, nil,
		func(candidate shadow.Policy) (shadowSearchScore, error) {
			return scoreShadowCandidate(candidate, trainPrices, spreadBPS)
		},
		func(candidate shadow.Policy) (shadowSearchScore, error) {
			return scoreShadowCandidate(candidate, validationPrices, spreadBPS)
		},
	)
}

func searchShadowCandidateTicks(
	policy shadow.Policy, trainTicks, validationTicks []shadow.Tick, spreadBPS uint64,
) (shadowSearchResult, error) {
	trainPrices, validationPrices := observedPrices(trainTicks), observedPrices(validationTicks)
	return searchShadowCandidateScored(
		policy, trainPrices, validationPrices, spreadBPS, nil,
		func(candidate shadow.Policy) (shadowSearchScore, error) {
			return scoreShadowCandidateTicks(candidate, trainTicks, spreadBPS)
		},
		func(candidate shadow.Policy) (shadowSearchScore, error) {
			return scoreShadowCandidateTicks(candidate, validationTicks, spreadBPS)
		},
	)
}

func searchShadowWalkForward(
	policy shadow.Policy, days []shadowWalkForwardDay, spreadBPS uint64,
) (shadowSearchResult, error) {
	return searchShadowWalkForwardForPreference(policy, policy, days, spreadBPS, "balanced")
}

func searchShadowWalkForwardForPreference(
	policy, immutableBase shadow.Policy, days []shadowWalkForwardDay, spreadBPS uint64, preference string,
) (shadowSearchResult, error) {
	var adaptiveCandidates []shadow.Policy
	if policy.Adaptive != nil {
		var err error
		adaptiveCandidates, err = adaptiveSearchPoliciesForPreference(policy, immutableBase, preference)
		if err != nil {
			return shadowSearchResult{}, err
		}
	} else if preference != "balanced" {
		return shadowSearchResult{}, errors.New("paper research preference requires an adaptive policy")
	}
	return searchShadowWalkForwardCandidates(policy, days, spreadBPS, adaptiveCandidates)
}

func searchShadowWalkForwardCandidates(
	policy shadow.Policy, days []shadowWalkForwardDay, spreadBPS uint64, adaptiveCandidates []shadow.Policy,
) (shadowSearchResult, error) {
	if len(days) != shadowWalkForwardWindows+1 {
		return shadowSearchResult{}, errors.New("walk-forward admission needs eight consecutive completed days")
	}
	admission := shadowWalkForwardAdmission{
		Version: shadowWalkForwardVersion, Status: "admitted",
		Windows: shadowWalkForwardWindows, RequiredPositiveWindows: 4,
		RequiredFullRoundTrips: 4,
		Folds:                  make([]shadowWalkForwardFold, 0, shadowWalkForwardWindows),
	}
	var final shadowSearchResult
	for index := 0; index < shadowWalkForwardWindows; index++ {
		training, validation := days[index], days[index+1]
		result, err := searchShadowCandidateScored(
			policy, observedPrices(training.Ticks), observedPrices(validation.Ticks), spreadBPS, adaptiveCandidates,
			func(candidate shadow.Policy) (shadowSearchScore, error) {
				return scoreShadowCandidateTicks(candidate, training.Ticks, spreadBPS)
			},
			func(candidate shadow.Policy) (shadowSearchScore, error) {
				return scoreShadowCandidateTicks(candidate, validation.Ticks, spreadBPS)
			},
		)
		if err != nil {
			return shadowSearchResult{}, fmt.Errorf("walk-forward fold %d: %w", index+1, err)
		}
		result.TrainDay = training.Provenance.Day
		result.ValidationDay = validation.Provenance.Day
		candidate, err := shadowSearchCandidatePolicy(policy, result.Candidate)
		if err != nil {
			return shadowSearchResult{}, err
		}
		fingerprint, err := candidate.Fingerprint()
		if err != nil {
			return shadowSearchResult{}, err
		}
		result.CandidatePolicySHA256 = fingerprint
		fold := shadowWalkForwardFold{
			TrainingJournal: training.Provenance, ValidationJournal: validation.Provenance,
			TrainingObservableBPS:   training.ObservableBPS,
			ValidationObservableBPS: validation.ObservableBPS,
			Candidate:               result.Candidate, CandidatePolicySHA256: fingerprint,
			CandidatesEvaluated: result.CandidatesEvaluated,
			Training:            result.Training, Validation: result.Validation,
		}
		admission.Folds = append(admission.Folds, fold)
		if result.Validation.VersusHoldMicros > 0 {
			admission.PositiveWindows++
		}
		if !addShadowReviewCounter(
			&admission.ValidationFullRoundTrips, result.Validation.FullRoundTrips,
		) || !addShadowReviewCounter(
			&admission.TotalCandidatesEvaluated, result.CandidatesEvaluated,
		) {
			return shadowSearchResult{}, errors.New("walk-forward counters overflow")
		}
		admission.AggregateVersusHoldMicros, err = addShadowSearchSigned(
			admission.AggregateVersusHoldMicros, result.Validation.VersusHoldMicros,
		)
		if err != nil {
			return shadowSearchResult{}, err
		}
		final = result
	}
	final.WalkForward = &admission
	final.NextStep = "run this walk-forward-admitted candidate as a forward paper challenger"
	if err := validateShadowWalkForwardAdmission(policy, final); err != nil {
		return shadowSearchResult{}, err
	}
	return final, nil
}

func validateShadowWalkForwardAdmission(base shadow.Policy, result shadowSearchResult) error {
	admission := result.WalkForward
	if admission == nil || admission.Version != shadowWalkForwardVersion ||
		admission.Status != "admitted" || admission.Windows != shadowWalkForwardWindows ||
		len(admission.Folds) != shadowWalkForwardWindows ||
		admission.RequiredPositiveWindows != 4 || admission.RequiredFullRoundTrips != 4 {
		return errors.New("shadow walk-forward admission is invalid")
	}
	var positive uint32
	var trips, candidates uint64
	var advantage int64
	for index, fold := range admission.Folds {
		if fold.TrainingJournal.validate() != nil || fold.ValidationJournal.validate() != nil ||
			fold.TrainingObservableBPS < 9_500 || fold.ValidationObservableBPS < 9_500 ||
			fold.CandidatesEvaluated == 0 || fold.Training.FullRoundTrips == 0 {
			return errors.New("shadow walk-forward fold is invalid")
		}
		trainAt, trainErr := time.Parse("2006-01-02", fold.TrainingJournal.Day)
		validationAt, validationErr := time.Parse("2006-01-02", fold.ValidationJournal.Day)
		if trainErr != nil || validationErr != nil || !validationAt.Equal(trainAt.AddDate(0, 0, 1)) {
			return errors.New("shadow walk-forward days are not consecutive")
		}
		if index > 0 && admission.Folds[index-1].ValidationJournal != fold.TrainingJournal {
			return errors.New("shadow walk-forward journal provenance is discontinuous")
		}
		candidate, err := shadowSearchCandidatePolicy(base, fold.Candidate)
		if err != nil {
			return errors.New("shadow walk-forward candidate is invalid")
		}
		fingerprint, err := candidate.Fingerprint()
		if err != nil || fingerprint != fold.CandidatePolicySHA256 {
			return errors.New("shadow walk-forward candidate fingerprint is invalid")
		}
		if fold.Validation.VersusHoldMicros > 0 {
			positive++
		}
		if !addShadowReviewCounter(&trips, fold.Validation.FullRoundTrips) ||
			!addShadowReviewCounter(&candidates, fold.CandidatesEvaluated) {
			return errors.New("shadow walk-forward counters overflow")
		}
		advantage, err = addShadowSearchSigned(advantage, fold.Validation.VersusHoldMicros)
		if err != nil {
			return err
		}
	}
	last := admission.Folds[len(admission.Folds)-1]
	outerCandidate, err := shadowSearchCandidatePolicy(base, result.Candidate)
	if err != nil {
		return errors.New("shadow walk-forward final candidate is invalid")
	}
	outerFingerprint, err := outerCandidate.Fingerprint()
	if err != nil || outerFingerprint != last.CandidatePolicySHA256 ||
		result.CandidatePolicySHA256 != last.CandidatePolicySHA256 ||
		result.TrainDay != last.TrainingJournal.Day ||
		result.ValidationDay != last.ValidationJournal.Day ||
		result.Training != last.Training || result.Validation != last.Validation ||
		result.CandidatesEvaluated != last.CandidatesEvaluated {
		return errors.New("shadow walk-forward final fold does not match the staged candidate")
	}
	if positive != admission.PositiveWindows || trips != admission.ValidationFullRoundTrips ||
		candidates != admission.TotalCandidatesEvaluated || advantage != admission.AggregateVersusHoldMicros ||
		positive < admission.RequiredPositiveWindows || trips < admission.RequiredFullRoundTrips ||
		advantage <= 0 || last.Validation.VersusHoldMicros < 0 {
		return errors.New("shadow walk-forward evidence did not pass admission")
	}
	return nil
}

func addShadowSearchSigned(left, right int64) (int64, error) {
	if right > 0 && left > math.MaxInt64-right || right < 0 && left < math.MinInt64-right {
		return 0, errors.New("walk-forward advantage overflows")
	}
	return left + right, nil
}

func searchShadowCandidateScored(
	policy shadow.Policy,
	trainPrices, validationPrices []uint64,
	spreadBPS uint64,
	adaptiveCandidates []shadow.Policy,
	trainingScore, validationScore func(shadow.Policy) (shadowSearchScore, error),
) (shadowSearchResult, error) {
	if policy.Adaptive == nil && !policy.IsSell() {
		return shadowSearchResult{}, errors.New("shadow search requires a sell-first policy")
	}
	if len(trainPrices) < 2 || len(validationPrices) < 2 {
		return shadowSearchResult{}, errors.New("training and validation each need at least two observable prices")
	}
	if spreadBPS == 0 || spreadBPS >= 10_000 {
		return shadowSearchResult{}, errors.New("shadow search spread must be between 1 and 9999 basis points")
	}
	if policy.Adaptive != nil {
		if adaptiveCandidates == nil {
			adaptiveCandidates = adaptiveSearchPolicies(policy)
		}
		return searchAdaptiveCandidateScored(policy, adaptiveCandidates, spreadBPS, trainingScore, validationScore)
	}
	levels := shadowResearchLevels(trainPrices)
	if len(levels) < 2 {
		return shadowSearchResult{}, errors.New("training prices need at least two distinct levels")
	}

	var best shadowSearchResult
	bestSet := false
	for _, buyAt := range levels {
		for _, sellAt := range levels {
			if buyAt >= sellAt {
				continue
			}
			candidate := shadowSearchPolicy(policy, sellAt, buyAt)
			training, err := trainingScore(candidate)
			if err != nil {
				return shadowSearchResult{}, err
			}
			best.CandidatesEvaluated++
			if training.FullRoundTrips == 0 {
				continue
			}
			if bestSet && (training.VersusHoldMicros < best.Training.VersusHoldMicros ||
				(training.VersusHoldMicros == best.Training.VersusHoldMicros &&
					training.MaxDrawdownMicros >= best.Training.MaxDrawdownMicros)) {
				continue
			}
			bestSet = true
			best.Candidate = shadowSearchCandidate{
				SellAtMicros: sellAt, SellAtUSD: formatUnits(sellAt, 6),
				BuyAtMicros: buyAt, BuyAtUSD: formatUnits(buyAt, 6),
			}
			best.Training = training
		}
	}
	if !bestSet {
		return shadowSearchResult{}, errors.New("no candidate completed a training round trip")
	}
	validation, err := validationScore(
		shadowSearchPolicy(policy, best.Candidate.SellAtMicros, best.Candidate.BuyAtMicros),
	)
	if err != nil {
		return shadowSearchResult{}, err
	}
	best.Status = "research_only"
	best.EvaluationMode = shadow.EvaluationResetDaily
	best.PoolModelled = true
	best.AssumedSpreadBPS = spreadBPS
	best.Validation = validation
	best.NextStep = "run this exact candidate in live shadow mode and require independent operator review"
	return best, nil
}

func searchAdaptiveCandidateScored(
	policy shadow.Policy,
	candidates []shadow.Policy,
	spreadBPS uint64,
	trainingScore, validationScore func(shadow.Policy) (shadowSearchScore, error),
) (shadowSearchResult, error) {
	var best shadowSearchResult
	bestSet := false
	for _, candidate := range candidates {
		training, err := trainingScore(candidate)
		if err != nil {
			return shadowSearchResult{}, err
		}
		best.CandidatesEvaluated++
		if training.FullRoundTrips == 0 {
			continue
		}
		if bestSet && (training.VersusHoldMicros < best.Training.VersusHoldMicros ||
			(training.VersusHoldMicros == best.Training.VersusHoldMicros &&
				training.MaxDrawdownMicros >= best.Training.MaxDrawdownMicros)) {
			continue
		}
		adaptive := *candidate.Adaptive
		bestSet = true
		best.Candidate = shadowSearchCandidate{Adaptive: &adaptive}
		best.Training = training
	}
	if !bestSet {
		return shadowSearchResult{}, errors.New("no adaptive candidate completed a training round trip")
	}
	candidate, err := shadowSearchCandidatePolicy(policy, best.Candidate)
	if err != nil {
		return shadowSearchResult{}, err
	}
	validation, err := validationScore(candidate)
	if err != nil {
		return shadowSearchResult{}, err
	}
	best.Status = "research_only"
	best.EvaluationMode = shadow.EvaluationResetDaily
	best.PoolModelled = true
	best.AssumedSpreadBPS = spreadBPS
	best.Validation = validation
	best.NextStep = "run this exact adaptive candidate as a forward paper challenger"
	return best, nil
}

func adaptiveSearchPolicies(policy shadow.Policy) []shadow.Policy {
	base := *policy.Adaptive
	dayWindow := min(uint64(1_440), (86_399-policy.SettleSeconds)/policy.TickSeconds+1)
	fastValues := []uint16{
		max(uint16(2), base.FastWindow/2), base.FastWindow,
		min(uint16(dayWindow-1), base.FastWindow*2),
	}
	slowValues := []uint16{
		max(uint16(3), base.SlowWindow/2), base.SlowWindow,
		min(uint16(dayWindow), base.SlowWindow*2),
	}
	signalValues := []uint16{base.MinimumSignalBPS}
	for _, extra := range []uint16{100, 250} {
		if uint32(base.MinimumSignalBPS)+uint32(extra) < uint32(base.MaxVolatilityBPS) &&
			uint32(base.MinimumSignalBPS)+uint32(extra) <= 2_000 {
			signalValues = append(signalValues, base.MinimumSignalBPS+extra)
		}
	}
	cooldownValues := []uint64{base.CooldownSeconds}
	if base.CooldownSeconds > 0 {
		cooldownValues = append(cooldownValues,
			max(policy.TickSeconds, base.CooldownSeconds/2),
			min(uint64(86_400), base.CooldownSeconds*2),
		)
	}
	seen := make(map[shadow.AdaptivePolicy]struct{})
	var candidates []shadow.Policy
	for _, fast := range fastValues {
		for _, slow := range slowValues {
			for _, signal := range signalValues {
				for _, cooldown := range cooldownValues {
					adaptive := base
					adaptive.FastWindow, adaptive.SlowWindow = fast, slow
					adaptive.MinimumSignalBPS, adaptive.CooldownSeconds = signal, cooldown
					if _, duplicate := seen[adaptive]; duplicate ||
						validateAdaptiveCandidateDelta(base, adaptive) != nil {
						continue
					}
					candidate := policy
					candidate.Adaptive = &adaptive
					if candidate.Validate() != nil {
						continue
					}
					seen[adaptive] = struct{}{}
					candidates = append(candidates, candidate)
				}
			}
		}
	}
	return candidates
}

func adaptiveSearchPoliciesForPreference(
	policy, immutableBase shadow.Policy, preference string,
) ([]shadow.Policy, error) {
	if policy.Adaptive == nil || immutableBase.Adaptive == nil {
		return nil, errors.New("paper research preference requires an adaptive policy")
	}
	if preference != "balanced" && preference != "more-opportunities" && preference != "more-selective" {
		return nil, errors.New("paper research preference is invalid")
	}
	candidates := adaptiveSearchPolicies(policy)
	if preference == "balanced" {
		return candidates, nil
	}
	filtered := candidates[:0]
	for _, candidate := range candidates {
		if adaptivePolicyMatchesPreference(*immutableBase.Adaptive, *candidate.Adaptive, preference) {
			filtered = append(filtered, candidate)
		}
	}
	if len(filtered) == 0 {
		return nil, errors.New("no bounded adaptive candidates match the operator research preference")
	}
	return filtered, nil
}

func adaptivePolicyMatchesPreference(
	base, candidate shadow.AdaptivePolicy, preference string,
) bool {
	switch preference {
	case "balanced":
		return true
	case "more-opportunities":
		return candidate.MinimumSignalBPS == base.MinimumSignalBPS &&
			candidate.CooldownSeconds <= base.CooldownSeconds
	case "more-selective":
		return candidate.MinimumSignalBPS >= base.MinimumSignalBPS &&
			candidate.CooldownSeconds >= base.CooldownSeconds &&
			(candidate.MinimumSignalBPS > base.MinimumSignalBPS ||
				candidate.CooldownSeconds > base.CooldownSeconds)
	default:
		return false
	}
}

func validateAdaptiveCandidateDelta(base, candidate shadow.AdaptivePolicy) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	if candidate.Version != base.Version ||
		candidate.MinimumSignalBPS < base.MinimumSignalBPS ||
		candidate.MaxVolatilityBPS != base.MaxVolatilityBPS ||
		candidate.MaxQuoteImpactBPS != base.MaxQuoteImpactBPS ||
		candidate.MaxDrawdownBPS != base.MaxDrawdownBPS ||
		base.CooldownSeconds == 0 && candidate.CooldownSeconds != 0 ||
		base.CooldownSeconds > 0 && (candidate.CooldownSeconds < base.CooldownSeconds/2 ||
			candidate.CooldownSeconds > min(uint64(86_400), base.CooldownSeconds*2)) ||
		candidate.MaxObservationGapSeconds != base.MaxObservationGapSeconds {
		return errors.New("adaptive candidate may change only windows, a bounded cooldown, or tighten its signal hurdle")
	}
	return nil
}

func shadowSearchCandidatePolicy(
	base shadow.Policy, candidate shadowSearchCandidate,
) (shadow.Policy, error) {
	if candidate.Adaptive != nil {
		if base.Adaptive == nil || candidate.SellAtMicros != 0 || candidate.BuyAtMicros != 0 ||
			candidate.SellAtUSD != "" || candidate.BuyAtUSD != "" ||
			validateAdaptiveCandidateDelta(*base.Adaptive, *candidate.Adaptive) != nil {
			return shadow.Policy{}, errors.New("adaptive search candidate is invalid")
		}
		policy := base
		adaptive := *candidate.Adaptive
		policy.Adaptive = &adaptive
		return policy, policy.Validate()
	}
	if base.Adaptive != nil || candidate.SellAtMicros == 0 || candidate.BuyAtMicros == 0 ||
		candidate.SellAtUSD != formatUnits(candidate.SellAtMicros, 6) ||
		candidate.BuyAtUSD != formatUnits(candidate.BuyAtMicros, 6) {
		return shadow.Policy{}, errors.New("fixed-threshold search candidate is invalid")
	}
	policy := shadowSearchPolicy(base, candidate.SellAtMicros, candidate.BuyAtMicros)
	return policy, policy.Validate()
}

func shadowSearchPolicy(policy shadow.Policy, sellAt, buyAt uint64) shadow.Policy {
	policy.Trigger.ThresholdMicros = sellAt
	buy := policy.Trigger
	if policy.ReturnTrigger != nil {
		buy = *policy.ReturnTrigger
	}
	buy.Direction = pricetrigger.BuyAtOrBelow
	buy.ThresholdMicros = buyAt
	policy.ReturnTrigger = &buy
	return policy
}

func scoreShadowCandidate(
	policy shadow.Policy, prices []uint64, spreadBPS uint64,
) (shadowSearchScore, error) {
	result, err := shadow.ReplayRoundTrip(
		policy, prices, modelledPool(policy, spreadBPS, policy.SlippageBPS),
	)
	if err != nil {
		return shadowSearchScore{}, err
	}
	return scoreShadowRoundTripResult(result)
}

func scoreShadowCandidateTicks(
	policy shadow.Policy, ticks []shadow.Tick, spreadBPS uint64,
) (shadowSearchScore, error) {
	result, err := shadow.ReplayRoundTripTicks(
		policy, ticks, modelledPool(policy, spreadBPS, policy.SlippageBPS),
	)
	if err != nil {
		return shadowSearchScore{}, err
	}
	return scoreShadowRoundTripResult(result)
}

func scoreShadowRoundTripResult(result shadow.RoundTripResult) (shadowSearchScore, error) {
	closing, err := result.Ledger.EquityMicros(result.ClosingPrice)
	if err != nil {
		return shadowSearchScore{}, err
	}
	hold, err := result.Ledger.HoldBenchmarkMicros(result.ClosingPrice)
	if err != nil {
		return shadowSearchScore{}, err
	}
	if closing > math.MaxInt64 || hold > math.MaxInt64 {
		return shadowSearchScore{}, errors.New("candidate result is too large to compare")
	}
	return shadowSearchScore{
		FullRoundTrips:    min(result.Counts.Sells, result.Counts.Buys),
		VersusHoldMicros:  int64(closing) - int64(hold),
		MaxDrawdownMicros: result.Ledger.MaxDrawdownMicros,
	}, nil
}

func shadowResearchLevels(prices []uint64) []uint64 {
	levels := slices.Clone(prices)
	slices.Sort(levels)
	levels = slices.Compact(levels)
	if len(levels) <= 9 {
		return levels
	}
	// ponytail: nine deciles bound the search to 36 pairs; expand the bounded
	// threshold search only if measured research demand justifies the cost.
	quantiles := make([]uint64, 0, 9)
	for tenth := 1; tenth <= 9; tenth++ {
		index := tenth * (len(levels) - 1) / 10
		quantiles = append(quantiles, levels[index])
	}
	return slices.Compact(quantiles)
}
