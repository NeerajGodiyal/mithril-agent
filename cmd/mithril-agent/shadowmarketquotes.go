package main

import (
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/marketadmission"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const marketRecordedQuoteVersion = "recorded-route-quotes-v1"

type marketRecordedQuoteCheck struct {
	At                  time.Time `json:"at"`
	NotBefore           time.Time `json:"not_before,omitzero"`
	Side                string    `json:"side"`
	InputAmount         uint64    `json:"input_amount,string"`
	Status              string    `json:"status"`
	RecordedInputAmount uint64    `json:"recorded_input_amount,string"`
	ReceivedAt          time.Time `json:"received_at,omitzero"`
	ResponseSHA256      string    `json:"response_sha256,omitempty"`
}

type marketRecordedQuoteReplay struct {
	Status          string                     `json:"status"`
	Counts          shadow.RoundTripCounts     `json:"counts"`
	FilteredReasons map[string]uint64          `json:"filtered_reasons,omitempty"`
	QuoteChecks     []marketRecordedQuoteCheck `json:"quote_checks"`
}

type marketRecordedQuoteLane struct {
	Partition      string                    `json:"partition"`
	From           time.Time                 `json:"from"`
	Through        time.Time                 `json:"through"`
	Baseline       marketRecordedQuoteReplay `json:"baseline"`
	ObservedNative marketRecordedQuoteReplay `json:"observed_native_cost"`
}

func replayMarketRecordedQuotes(policy shadow.Policy, artifact marketadmission.ProvisionalArtifact,
	points []marketadmission.ProvisionalReplayPoint, ticks []shadow.Tick,
) (marketRecordedQuoteLane, error) {
	byTime := make(map[time.Time]marketadmission.ProvisionalReplayPoint, len(points))
	for _, point := range points {
		at := point.At.UTC()
		if _, exists := byTime[at]; exists {
			return marketRecordedQuoteLane{}, errors.New("recorded quotes have duplicate observation times")
		}
		byTime[at] = point
	}
	var lane marketRecordedQuoteLane
	baseline, observed, err := shadow.ReplayObservedNativeTimedObservationComparison(policy, ticks,
		marketRecordedQuoteLookup(artifact, byTime, &lane.Baseline.QuoteChecks),
		marketRecordedQuoteLookup(artifact, byTime, &lane.ObservedNative.QuoteChecks))
	if err != nil {
		return marketRecordedQuoteLane{}, err
	}
	finishMarketRecordedQuoteReplay(&lane.Baseline, baseline)
	finishMarketRecordedQuoteReplay(&lane.ObservedNative, observed)
	return lane, nil
}

func finishMarketRecordedQuoteReplay(output *marketRecordedQuoteReplay, replay shadow.RoundTripResult) {
	output.Counts, output.FilteredReasons = replay.Counts, replay.FilteredReasons
	output.Status = "matched_requested_quotes"
	if len(output.QuoteChecks) == 0 {
		output.Status = "no_quote_requests"
	}
	if replay.Counts.Missed > 0 {
		output.Status = "missed_replay_steps"
	}
	for _, check := range output.QuoteChecks {
		if check.Status != "matched" {
			output.Status = "incomplete_quote_evidence"
			break
		}
	}
}

// marketRecordedQuoteLookup uses only the verified observation's exact quote.
// A reverse quote for a different inventory amount is missing evidence, not a
// scalable price curve. No later observation or modeled quote can replace it.
func marketRecordedQuoteLookup(artifact marketadmission.ProvisionalArtifact,
	points map[time.Time]marketadmission.ProvisionalReplayPoint, checks *[]marketRecordedQuoteCheck,
) func(time.Time, time.Time, uint64, bool, uint64) (shadow.Quote, error) {
	return func(at, notBefore time.Time, _ uint64, sell bool, amount uint64) (shadow.Quote, error) {
		point, ok := points[at.UTC()]
		quote := point.Buy
		input, output, side := artifact.Candidate.QuoteMint, artifact.Candidate.BaseMint, "buy"
		if sell {
			quote = point.Sell
			input, output, side = output, input, "sell"
		}
		check := marketRecordedQuoteCheck{At: at, NotBefore: notBefore, Side: side, InputAmount: amount,
			RecordedInputAmount: quote.InputAmount, ReceivedAt: quote.ReceivedAt,
			ResponseSHA256: quote.ResponseSHA256, Status: "matched"}
		request := jupiterquote.Request{Taker: artifact.Observe, InputMint: input, OutputMint: output,
			InputAmount: amount, SlippageBPS: artifact.Candidate.QuoteSlippageBPS}
		result := jupiterquote.Result{InputAmount: quote.InputAmount,
			EstimatedOutput: quote.EstimatedOutput, MinimumOutput: quote.MinimumOutput}
		switch {
		case !ok || !point.Available:
			check.Status = "observation_unavailable"
		case quote.ReceivedAt.IsZero() || quote.ReceivedAt.After(at) || quote.ReceivedAt.Before(point.Bucket) || quote.ReceivedAt.Before(notBefore):
			check.Status = "quote_time_unavailable"
		case quote.InputMint != input || quote.OutputMint != output || !validLowerSHA256(quote.ResponseSHA256) ||
			quote.LatencyMillis > artifact.Thresholds.MaximumQuoteLatencyMillis:
			check.Status = "quote_evidence_invalid"
		case quote.InputAmount != amount:
			check.Status = "input_amount_not_recorded"
		case result.Validate(request) != nil:
			check.Status = "quote_evidence_invalid"
		}
		*checks = append(*checks, check)
		if check.Status != "matched" {
			return shadow.Quote{}, errors.New("recorded route quote unavailable")
		}
		return shadow.Quote{InputAmount: quote.InputAmount, EstimatedOutput: quote.EstimatedOutput,
			MinimumOutput: quote.MinimumOutput}, nil
	}
}
