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

const shadowSearchUsage = `Usage: mithril-agent shadow search --policy PATH --dir PATH
                                   --train-day YYYY-MM-DD
                                   --validation-day YYYY-MM-DD
                                   [--base-policy PATH]
                                   [--spread-bps N] [--candidate-out PATH]

Chooses a sell/buy threshold pair from one completed UTC day's recorded prices,
then scores that exact pair on a later completed, untouched UTC day using a
fresh reset book on each day. The pool is modelled at the
named spread. The result is research_only and can never authorize a trade.
When --candidate-out is set, it also writes one immutable, paper-only policy
bound to the base policy and both journals' verified chain heads.`

type shadowSearchScore struct {
	FullRoundTrips    uint64 `json:"full_round_trips"`
	VersusHoldMicros  int64  `json:"versus_hold_micros"`
	MaxDrawdownMicros uint64 `json:"max_drawdown_micros"`
}

type shadowSearchCandidate struct {
	SellAtMicros uint64 `json:"sell_at_micros"`
	SellAtUSD    string `json:"sell_at_usd"`
	BuyAtMicros  uint64 `json:"buy_at_micros"`
	BuyAtUSD     string `json:"buy_at_usd"`
}

type shadowSearchResult struct {
	Status                string                `json:"status"`
	EvaluationMode        string                `json:"evaluation_mode"`
	Authorized            bool                  `json:"authorized"`
	Promotable            bool                  `json:"promotable"`
	PoolModelled          bool                  `json:"pool_modelled"`
	AssumedSpreadBPS      uint64                `json:"assumed_spread_bps"`
	TrainDay              string                `json:"train_day"`
	ValidationDay         string                `json:"validation_day"`
	CandidatesEvaluated   uint64                `json:"candidates_evaluated"`
	CandidatePolicySHA256 string                `json:"candidate_policy_sha256,omitempty"`
	Candidate             shadowSearchCandidate `json:"candidate"`
	Training              shadowSearchScore     `json:"training"`
	Validation            shadowSearchScore     `json:"validation"`
	NextStep              string                `json:"next_step"`
}

func runShadowSearch(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow search", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "shadow policy JSON")
	basePolicyPath := flags.String("base-policy", "", "immutable base policy for an iterative candidate")
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
	if !policy.IsSell() {
		return errors.New("shadow search currently requires a sell-first SOL/USDC policy")
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
		policy, trainPrices, validationPrices, spreadBPS,
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
		policy, trainPrices, validationPrices, spreadBPS,
		func(candidate shadow.Policy) (shadowSearchScore, error) {
			return scoreShadowCandidateTicks(candidate, trainTicks, spreadBPS)
		},
		func(candidate shadow.Policy) (shadowSearchScore, error) {
			return scoreShadowCandidateTicks(candidate, validationTicks, spreadBPS)
		},
	)
}

func searchShadowCandidateScored(
	policy shadow.Policy,
	trainPrices, validationPrices []uint64,
	spreadBPS uint64,
	trainingScore, validationScore func(shadow.Policy) (shadowSearchScore, error),
) (shadowSearchResult, error) {
	if !policy.IsSell() {
		return shadowSearchResult{}, errors.New("shadow search requires a sell-first policy")
	}
	if len(trainPrices) < 2 || len(validationPrices) < 2 {
		return shadowSearchResult{}, errors.New("training and validation each need at least two observable prices")
	}
	if spreadBPS == 0 || spreadBPS >= 10_000 {
		return shadowSearchResult{}, errors.New("shadow search spread must be between 1 and 9999 basis points")
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
		policy, prices, modelledPool(spreadBPS, policy.SlippageBPS),
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
		policy, ticks, modelledPool(spreadBPS, policy.SlippageBPS),
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
