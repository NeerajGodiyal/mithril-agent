package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/marketadmission"
)

func TestMarketRecordedQuoteLookupExactTimeAmountAndEvidence(t *testing.T) {
	_, artifact, points, _ := marketCostFixture(t)
	first := points[0]
	if !first.Available || first.Buy.InputAmount == 0 || first.Sell.InputAmount == 0 {
		t.Fatal("fixture has no verified quotes")
	}
	for _, name := range []string{"buy", "sell", "wrong_amount", "future", "old", "before_settlement", "settlement_boundary", "missing", "rejected", "mint", "hash", "slippage", "latency"} {
		t.Run(name, func(t *testing.T) {
			point := first
			sell := name == "sell"
			amount := point.Buy.InputAmount
			if sell {
				amount = point.Sell.InputAmount
			}
			switch name {
			case "wrong_amount":
				amount++
			case "future":
				point.Buy.ReceivedAt = point.At.Add(time.Nanosecond)
			case "old":
				point.Buy.ReceivedAt = point.Bucket.Add(-time.Nanosecond)
			case "rejected":
				point.Available = false
			case "mint":
				point.Buy.InputMint = point.Buy.OutputMint
			case "hash":
				point.Buy.ResponseSHA256 = ""
			case "slippage":
				point.Buy.MinimumOutput--
			case "latency":
				point.Buy.LatencyMillis = artifact.Thresholds.MaximumQuoteLatencyMillis + 1
			}
			at := point.At
			var notBefore time.Time
			if name == "before_settlement" {
				notBefore = point.Buy.ReceivedAt.Add(time.Nanosecond)
			}
			if name == "settlement_boundary" {
				notBefore = point.Buy.ReceivedAt
			}
			if name == "missing" {
				at = at.Add(time.Nanosecond)
			}
			var checks []marketRecordedQuoteCheck
			lookup := marketRecordedQuoteLookup(artifact,
				map[time.Time]marketadmission.ProvisionalReplayPoint{point.At.UTC(): point}, &checks)
			got, err := lookup(at, notBefore, 123, sell, amount)
			matched := name == "buy" || name == "sell" || name == "settlement_boundary"
			if (err == nil) != matched || len(checks) != 1 || (checks[0].Status == "matched") != matched {
				t.Fatalf("matched=%v, error=%v, checks=%+v", matched, err, checks)
			}
			if matched {
				want := point.Buy
				if sell {
					want = point.Sell
				}
				if got.InputAmount != want.InputAmount || got.EstimatedOutput != want.EstimatedOutput ||
					got.MinimumOutput != want.MinimumOutput || checks[0].ResponseSHA256 != want.ResponseSHA256 {
					t.Fatal("recorded quote was changed")
				}
			} else if !reflect.ValueOf(got).IsZero() {
				t.Fatal("unavailable quote returned modeled output")
			}
		})
	}
	// Identical price arguments must still select different timestamp-bound quotes.
	second := points[1]
	second.Buy.ResponseSHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	var checks []marketRecordedQuoteCheck
	lookup := marketRecordedQuoteLookup(artifact, map[time.Time]marketadmission.ProvisionalReplayPoint{
		first.At.UTC(): first, second.At.UTC(): second,
	}, &checks)
	for _, point := range []marketadmission.ProvisionalReplayPoint{first, second} {
		if _, err := lookup(point.At, time.Time{}, 123, false, point.Buy.InputAmount); err != nil {
			t.Fatal(err)
		}
	}
	if checks[0].ResponseSHA256 == checks[1].ResponseSHA256 {
		t.Fatal("lookup reused quote at another observation time")
	}
}

func TestMarketRecordedQuoteReplayMarksSizeMismatchIncomplete(t *testing.T) {
	policy, artifact, points, _ := marketCostFixture(t)
	policy.Adaptive.FastWindow, policy.Adaptive.SlowWindow = 2, 4
	for i := range points {
		price := uint64(2_000_000 + i*30_000)
		points[i].MarketPrimary.PriceMicros = price
		points[i].MarketSecondary.PriceMicros = price
		points[i].Buy.InputAmount++
	}
	ticks, err := provisionalMarketTicks(policy, points)
	if err != nil {
		t.Fatal(err)
	}
	lane, err := replayMarketRecordedQuotes(policy, artifact, points, ticks)
	if err != nil {
		t.Fatal(err)
	}
	for _, replay := range []marketRecordedQuoteReplay{lane.Baseline, lane.ObservedNative} {
		if replay.Status != "incomplete_quote_evidence" || len(replay.QuoteChecks) == 0 ||
			replay.Counts.Buys != 0 || replay.Counts.Sells != 0 || replay.Counts.Missed == 0 {
			t.Fatalf("mismatched amount fabricated a complete path: %+v", replay)
		}
		for _, check := range replay.QuoteChecks {
			if check.Status != "input_amount_not_recorded" {
				t.Fatalf("wrong missing evidence reason: %+v", check)
			}
		}
	}
	points[1].At = points[0].At
	if _, err := replayMarketRecordedQuotes(policy, artifact, points, ticks); err == nil {
		t.Fatal("duplicate observation times accepted")
	}
}

func TestMarketRecordedQuoteCommandIsBoundedAndNonMutating(t *testing.T) {
	policy, artifact, points, args := marketCostFixture(t)
	args[len(args)-1] = marketRecordedQuoteVersion
	before := make(map[string][]byte)
	for _, path := range []string{args[1], args[3], args[5]} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = data
	}
	var output bytes.Buffer
	if err := runShadowMarketPaperCheck(args, &output); err != nil {
		t.Fatal(err)
	}
	var result marketPaperCostComparison
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Experiment != "provisional-"+marketRecordedQuoteVersion ||
		result.AdmissionEvidence || result.TradingEnabled || len(result.Lanes) != 0 ||
		len(result.RecordedQuoteLanes) != 2 || result.Journal != artifact.Journal {
		t.Fatalf("invalid recorded quote result: %+v", result)
	}
	for index, lane := range result.RecordedQuoteLanes {
		if lane.From != artifact.From.Add(time.Duration(index)*80*time.Minute) {
			t.Fatal("partition starts at wrong time")
		}
		for _, replay := range []marketRecordedQuoteReplay{lane.Baseline, lane.ObservedNative} {
			if len(replay.QuoteChecks) == 0 && replay.Status != "no_quote_requests" {
				t.Fatal("no-request lane claims quote coverage")
			}
		}
	}
	for path, want := range before {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("input changed: %s", path)
		}
	}
	for _, flag := range []string{"--result-out", "--candidate-policy-out", "--dashboard-status"} {
		path := filepath.Join(t.TempDir(), "forbidden.json")
		var rejected bytes.Buffer
		if err := runShadowMarketPaperCheck(append(append([]string{}, args...), flag, path), &rejected); err == nil || rejected.Len() != 0 {
			t.Fatalf("accepted output flag %s", flag)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("created forbidden output")
		}
	}
	wantError := errors.New("writer failed")
	if err := writeMarketPaperCostExperiment(writerFunc(func([]byte) (int, error) { return 0, wantError }),
		policy, artifact, points, marketRecordedQuoteVersion); !errors.Is(err, wantError) {
		t.Fatalf("writer error = %v", err)
	}
}
