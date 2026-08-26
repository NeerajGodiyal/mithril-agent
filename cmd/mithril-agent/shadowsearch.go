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

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const shadowSearchUsage = `Usage: mithril-agent shadow search --policy PATH --dir PATH
                                   --train-day YYYY-MM-DD
                                   --validation-day YYYY-MM-DD
                                   [--spread-bps N]

Chooses a sell/buy threshold pair from the training day's recorded prices, then
scores that exact pair on a later untouched day. The pool is modelled at the
named spread. The result is research_only and can never authorize a trade.`

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
	Status              string                `json:"status"`
	Authorized          bool                  `json:"authorized"`
	Promotable          bool                  `json:"promotable"`
	PoolModelled        bool                  `json:"pool_modelled"`
	AssumedSpreadBPS    uint64                `json:"assumed_spread_bps"`
	TrainDay            string                `json:"train_day"`
	ValidationDay       string                `json:"validation_day"`
	CandidatesEvaluated uint64                `json:"candidates_evaluated"`
	Candidate           shadowSearchCandidate `json:"candidate"`
	Training            shadowSearchScore     `json:"training"`
	Validation          shadowSearchScore     `json:"validation"`
	NextStep            string                `json:"next_step"`
}

func runShadowSearch(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow search", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "shadow policy JSON")
	directory := flags.String("dir", "", "directory holding recorded shadow journals")
	trainDay := flags.String("train-day", "", "training UTC day, YYYY-MM-DD")
	validationDay := flags.String("validation-day", "", "later validation UTC day, YYYY-MM-DD")
	spreadBPS := flags.Uint64("spread-bps", 100, "modelled pool cost each way, in basis points")
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
	if *spreadBPS == 0 || *spreadBPS >= 10_000 {
		return errors.New("--spread-bps must be between 1 and 9999")
	}

	policy, err := loadShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	if !policy.IsSell() {
		return errors.New("shadow search currently requires a sell-first SOL/USDC policy")
	}
	trainTicks, err := readShadowTicks(
		filepath.Join(*directory, "shadow-"+*trainDay+".jsonl"), policy,
	)
	if err != nil {
		return fmt.Errorf("read training journal: %w", err)
	}
	validationTicks, err := readShadowTicks(
		filepath.Join(*directory, "shadow-"+*validationDay+".jsonl"), policy,
	)
	if err != nil {
		return fmt.Errorf("read validation journal: %w", err)
	}
	result, err := searchShadowCandidate(
		policy, observedPrices(trainTicks), observedPrices(validationTicks), *spreadBPS,
	)
	if err != nil {
		return err
	}
	result.TrainDay, result.ValidationDay = *trainDay, *validationDay
	return json.NewEncoder(output).Encode(result)
}

func searchShadowCandidate(
	policy shadow.Policy, trainPrices, validationPrices []uint64, spreadBPS uint64,
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
			training, err := scoreShadowCandidate(candidate, trainPrices, spreadBPS)
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
	validation, err := scoreShadowCandidate(
		shadowSearchPolicy(policy, best.Candidate.SellAtMicros, best.Candidate.BuyAtMicros),
		validationPrices, spreadBPS,
	)
	if err != nil {
		return shadowSearchResult{}, err
	}
	best.Status = "research_only"
	best.PoolModelled = true
	best.AssumedSpreadBPS = spreadBPS
	best.Validation = validation
	best.NextStep = "run this exact candidate in live shadow mode and require independent operator review"
	return best, nil
}

func shadowSearchPolicy(policy shadow.Policy, sellAt, buyAt uint64) shadow.Policy {
	policy.Trigger.ThresholdMicros = sellAt
	buy := policy.Trigger
	buy.Direction = pricetrigger.BuyAtOrBelow
	buy.ThresholdMicros = buyAt
	policy.ReturnTrigger = &buy
	return policy
}

func scoreShadowCandidate(
	policy shadow.Policy, prices []uint64, spreadBPS uint64,
) (shadowSearchScore, error) {
	result, err := shadow.ReplayRoundTrip(policy, prices, modelledPool(spreadBPS))
	if err != nil {
		return shadowSearchScore{}, err
	}
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
	// ponytail: nine deciles bound the search to 36 pairs; add a walk-forward
	// optimizer only if measured research demand justifies the extra machinery.
	quantiles := make([]uint64, 0, 9)
	for tenth := 1; tenth <= 9; tenth++ {
		index := tenth * (len(levels) - 1) / 10
		quantiles = append(quantiles, levels[index])
	}
	return slices.Compact(quantiles)
}
