package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

type freshnessQuoter struct{ calls int }

func (q *freshnessQuoter) Quote(context.Context, string, bool, uint64, uint16) (shadow.Quote, error) {
	q.calls++
	return shadow.Quote{}, nil
}

func TestPaperTickerRetainedHTTPDateRefusesStaleAndFutureObservations(t *testing.T) {
	for _, mode := range []string{"stale", "future"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(pricesource.KrakenRateStateEnvironment, filepath.Join(privateTestDirectory(t), "rate"))
			synctest.Test(t, func(t *testing.T) {
				policy, err := loadShadowPolicy(generatedPolicyPath(t, "--adaptive", "--kraken-source", "ticker-batch"))
				if err != nil {
					t.Fatal(err)
				}
				date := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Minute)
				if mode == "future" {
					date = date.Add(20 * time.Minute)
				}
				calls := 0
				batch, err := pricesource.NewKrakenTickerBatch(&http.Client{Transport: walletRoundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: 200, Header: http.Header{"Date": {date.Format(http.TimeFormat)}}, Body: io.NopCloser(strings.NewReader(`{"error":[],"result":{"SOL/USD":{"a":["150","1","1"],"b":["150","1","1"]},"USDC/USD":{"a":["1","1","1"],"b":["1","1","1"]}}}`))}, nil
				})}, []string{pricetrigger.FeedSOLUSD, pricetrigger.FeedUSDCUSD})
				if err != nil {
					t.Fatal(err)
				}
				secondary, err := batch.Reader(pricetrigger.FeedSOLUSD)
				if err != nil {
					t.Fatal(err)
				}
				peg, err := batch.Reader(pricetrigger.FeedUSDCUSD)
				if err != nil {
					t.Fatal(err)
				}
				roll, err := newDailyJournal(privateTestDirectory(t))
				if err != nil {
					t.Fatal(err)
				}
				defer roll.Close()
				quoter := &freshnessQuoter{}
				runner, err := shadow.NewRunner(policy, tickerPrimary{identity: policy.Trigger.PrimarySourceSHA256}, secondary, quoter, roll, tickerPrimary{identity: policy.QuotePeg.PrimarySourceSHA256}, peg)
				if err != nil {
					t.Fatal(err)
				}
				for range 2 {
					observation := runner.Observe(t.Context())
					tick, err := runner.StepObservation(t.Context(), time.Now().UTC(), observation)
					// The batch date also belongs to USDC; its freshness guard runs first.
					if err != nil || tick.Event != shadow.EventUnobservable || tick.Reason != shadow.ReasonQuoteCurrencyInvalid || tick.Fill != nil || tick.DecisionQuote != nil {
						t.Fatalf("freshness refusal = %s/%s, %v", tick.Event, tick.Reason, err)
					}
					sample, err := secondary.Latest(t.Context(), pricetrigger.FeedSOLUSD)
					if err != nil || !sample.PublishedAt.Equal(date) {
						t.Fatal("retained response date was renewed")
					}
					time.Sleep(time.Second)
				}
				if calls != 1 || quoter.calls != 0 {
					t.Fatal("invalid retained observation fetched again or quoted")
				}
				observations := 0
				for _, record := range roll.Records() {
					var tick shadow.Tick
					if err := json.Unmarshal(record.Payload, &tick); err != nil {
						t.Fatal(err)
					}
					if tick.Event == "" {
						continue
					}
					observations++
					if tick.Event != shadow.EventUnobservable || tick.Fill != nil || tick.DecisionQuote != nil {
						t.Fatal("invalid date advanced journal execution")
					}
				}
				if observations != 2 {
					t.Fatal("missing refusal journal evidence")
				}
			})
		})
	}
}
