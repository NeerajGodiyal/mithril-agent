package main

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestPaperTickerReaderBindings(t *testing.T) {
	for _, market := range []string{"SOL/USDC", "JUP/USDC"} {
		t.Run(market, func(t *testing.T) {
			args := []string{"--adaptive", "--market", market, "--drawdown-stop-bps", "750"}
			if market == "SOL/USDC" {
				args = append(args, "--budget-sol", "1")
			} else {
				args = append(args, "--budget-usdc", "25")
			}
			base, err := loadShadowPolicy(generatedPolicyPath(t, args...))
			if err != nil {
				t.Fatal(err)
			}
			if err := validatePaperPolicySources(base); err != nil {
				t.Fatal(err)
			}
			if paperUsesKrakenTicker(base) {
				t.Fatal("default changed source family")
			}
			policy, err := loadShadowPolicy(generatedPolicyPath(t, append(args, "--kraken-source", "ticker-batch")...))
			if err != nil {
				t.Fatal(err)
			}
			batch, readers, err := newPaperTickerReaders(policy)
			if err != nil || batch == nil {
				t.Fatalf("ticker readers: %v", err)
			}
			feeds := []string{policy.Trigger.Feed, policy.QuotePeg.Feed}
			if policy.NativeFeePrice != nil {
				feeds = append(feeds, policy.NativeFeePrice.Feed)
			}
			for i, feed := range feeds {
				reader, ok := readers[i].(*pricesource.KrakenTickerReader)
				want, err := pricesource.KrakenTickerIdentitySHA256(feed)
				if !ok || err != nil || reader.IdentitySHA256() != want {
					t.Fatal("wrong concrete ticker reader")
				}
			}
			for _, field := range []string{"market", "return", "peg", "native"} {
				mixed := policy
				switch field {
				case "market":
					mixed.Trigger.SecondarySourceSHA256 = base.Trigger.SecondarySourceSHA256
				case "return":
					value := *mixed.ReturnTrigger
					value.SecondarySourceSHA256 = base.ReturnTrigger.SecondarySourceSHA256
					mixed.ReturnTrigger = &value
				case "peg":
					value := *mixed.QuotePeg
					value.SecondarySourceSHA256 = base.QuotePeg.SecondarySourceSHA256
					mixed.QuotePeg = &value
				case "native":
					if mixed.NativeFeePrice == nil {
						continue
					}
					value := *mixed.NativeFeePrice
					value.SecondarySourceSHA256 = base.NativeFeePrice.SecondarySourceSHA256
					mixed.NativeFeePrice = &value
				}
				if _, _, err := newPaperTickerReaders(mixed); err == nil {
					t.Fatalf("accepted mixed %s source", field)
				}
			}
			if market == "SOL/USDC" {
				t.Setenv(shadowEndpointEnvironment, "https://api.mainnet-beta.solana.com")
				strategy, err := strategyPriceReaders(policy, time.Now)
				if err != nil {
					t.Fatal(err)
				}
				for _, i := range []int{1, 3} {
					if _, ok := strategy[i].(*pricesource.KrakenTickerReader); !ok {
						t.Fatal("strategy did not select ticker reader")
					}
				}
			}
		})
	}
}

func TestOpenShadowRunSelectsJUPTickerWithoutStartupReads(t *testing.T) {
	t.Setenv(shadowEndpointEnvironment, "https://api.mainnet-beta.solana.com")
	t.Setenv(jupiterAPIKeyEnvironment, "")
	t.Setenv(pricesource.KrakenRateStateEnvironment, filepath.Join(privateTestDirectory(t), "rate"))
	for _, source := range []string{"pre-trade", "ticker-batch"} {
		t.Run(source, func(t *testing.T) {
			path := generatedPolicyPath(t, "--adaptive", "--market", "JUP/USDC", "--budget-usdc", "25", "--drawdown-stop-bps", "750", "--kraken-source", source)
			policy, err := loadShadowPolicy(path)
			if err != nil {
				t.Fatal(err)
			}
			// A canceled context cannot perform a successful startup price read.
			// No portfolio means construction should not need one.
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			run, err := openShadowRun(ctx, policy, shadowRunOptions{policyPath: path, directory: privateTestDirectory(t)})
			if err != nil {
				t.Fatal(err)
			}
			defer run.roll.Close()
			if (run.krakenBatch != nil) != (source == "ticker-batch") {
				t.Fatal("constructor selected wrong batch family")
			}
			readers := []shadow.PriceReader{run.secondary, run.quoteSecondary, run.nativeSecondary}
			identities := []string{policy.Trigger.SecondarySourceSHA256, policy.QuotePeg.SecondarySourceSHA256, policy.NativeFeePrice.SecondarySourceSHA256}
			for i, reader := range readers {
				if reader.IdentitySHA256() != identities[i] {
					t.Fatal("constructor changed pinned secondary identity")
				}
				if source == "ticker-batch" {
					if _, ok := reader.(*pricesource.KrakenTickerReader); !ok {
						t.Fatal("constructor did not install concrete ticker reader")
					}
				} else if _, ok := reader.(*pricesource.Kraken); !ok {
					t.Fatal("default constructor changed concrete reader")
				}
			}
		})
	}
}

type tickerPrimary struct {
	identity string
	before   func()
}

func (s tickerPrimary) IdentitySHA256() string { return s.identity }
func (s tickerPrimary) Latest(_ context.Context, feed string) (pricetrigger.Sample, error) {
	if s.before != nil {
		s.before()
	}
	price := uint64(150_000_000)
	if feed == pricetrigger.FeedUSDCUSD {
		price = 1_000_000
	}
	return pricetrigger.Sample{SourceSHA256: s.identity, Feed: feed, PriceMicros: price, ConfidenceMicros: 1, PublishedAt: time.Now().UTC()}, nil
}

func TestShadowDriveRefreshesTickerStartupAndRollover(t *testing.T) {
	t.Setenv(pricesource.KrakenRateStateEnvironment, filepath.Join(privateTestDirectory(t), "rate"))
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(24*time.Hour - time.Second)
		policy, err := loadShadowPolicy(generatedPolicyPath(t, "--adaptive", "--kraken-source", "ticker-batch"))
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		var dates []time.Time
		batch, err := pricesource.NewKrakenTickerBatch(&http.Client{Transport: walletRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Path != "/0/public/Ticker" || r.URL.Query().Get("pair") != "SOLUSD,USDCUSD" {
				t.Error("unexpected batch request")
			}
			date := time.Now().UTC().Truncate(time.Second)
			dates = append(dates, date)
			return &http.Response{StatusCode: 200, Header: http.Header{"Date": {date.Format(http.TimeFormat)}}, Body: io.NopCloser(strings.NewReader(`{"error":[],"result":{"SOL/USD":{"a":["150","1","1"],"b":["150","1","1"]},"USDC/USD":{"a":["1","1","1"],"b":["1","1","1"]}}}`))}, nil
		})}, []string{pricetrigger.FeedSOLUSD, pricetrigger.FeedUSDCUSD})
		if err != nil {
			t.Fatal(err)
		}
		secondary, _ := batch.Reader(pricetrigger.FeedSOLUSD)
		peg, _ := batch.Reader(pricetrigger.FeedUSDCUSD)
		startup, err := secondary.Latest(t.Context(), pricetrigger.FeedSOLUSD)
		if err != nil {
			t.Fatal(err)
		}
		again, err := peg.Latest(t.Context(), pricetrigger.FeedUSDCUSD)
		if err != nil || calls != 1 || !again.PublishedAt.Equal(startup.PublishedAt) {
			t.Fatal("startup did not retain exact batch date")
		}
		root := privateTestDirectory(t)
		roll, err := newDailyJournal(root)
		if err != nil {
			t.Fatal(err)
		}
		defer roll.Close()
		if err := roll.openFor(time.Now()); err != nil {
			t.Fatal(err)
		}
		alerts, err := paperstatus.OpenWriter(filepath.Join(root, "alerts.json"))
		if err != nil {
			t.Fatal(err)
		}
		reads := 0
		run := &shadowRun{policy: policy, roll: roll, alerts: alerts, krakenBatch: batch, secondary: secondary, quoteSecondary: peg, quoter: liveStubQuoter{estimated: 21_525}}
		run.policySHA256, err = policy.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		run.primary = tickerPrimary{identity: policy.Trigger.PrimarySourceSHA256, before: func() {
			reads++
			if reads == 1 {
				time.Sleep(2 * time.Second)
			}
		}}
		run.quotePrimary = tickerPrimary{identity: policy.QuotePeg.PrimarySourceSHA256}
		run.runner, err = run.newRunner()
		if err != nil {
			t.Fatal(err)
		}
		output := &cadenceTickWriter{}
		if err := run.drive(t.Context(), true, output); err != nil {
			t.Fatal(err)
		}
		if calls != 3 || reads != 2 || len(output.ticks) != 1 {
			t.Fatalf("startup/rollover calls=%d reads=%d ticks=%d", calls, reads, len(output.ticks))
		}
		if output.ticks[0].SecondaryPrice == nil || !output.ticks[0].SecondaryPrice.PublishedAt.Equal(dates[2]) {
			t.Fatal("rollover reused prior batch timestamp")
		}
		time.Sleep(15 * time.Second)
		if err := run.drive(t.Context(), true, output); err != nil {
			t.Fatal(err)
		}
		if calls != 4 || len(output.ticks) != 2 || !output.ticks[1].SecondaryPrice.PublishedAt.Equal(dates[3]) || !dates[3].After(dates[2]) {
			t.Fatal("next drive reused a prior observation")
		}
	})
}
