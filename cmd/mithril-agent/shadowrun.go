package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/swapbuilder"
)

// Shadow mode is safe to point at Mainnet for a structural reason: the shadow
// package has no signer, no submitter, and no way to name a key. This command
// wires it to real data and nothing else.
//
// Each UTC day is an independent trial. The books open at the day's first
// observed price and close with that day's report, so one lucky day cannot be
// carried into the next and every period is scored out of sample against a hold
// benchmark that opened at the same moment. That is what makes a run of daily
// reports a walk-forward rather than a backtest.
const shadowRunUsage = `Usage: mithril-agent shadow run --policy PATH --dir PATH [options]

Watches a live market and records what the rule would have done. Nothing is
signed and nothing is submitted; no wallet signing key is loaded at any point.

  --policy PATH         shadow policy JSON
  --dir PATH            directory for the daily journals and reports
  --quote-provider NAME optional compatibility check; must match the policy
  --node-command PATH   Node.js runtime for the read-only quote adapter
  --quote-script PATH   read-only quote adapter
  --pool ADDRESS        optional compatibility check; must match the policy
  --input-mint ADDRESS  optional compatibility check; must match the policy
  --output-mint ADDRESS optional compatibility check; must match the policy
  --once                take a single observation and stop

Endpoints come from the environment and are never printed, logged, or written
to the journal:

  MITHRIL_AGENT_SHADOW_RPC_URL   price reads; https, or http on a literal
                                 loopback IP so it can read your own node
  MITHRIL_AGENT_QUOTE_RPC_URL    the Orca quote adapter; https only
  MITHRIL_AGENT_JUPITER_API_KEY  optional for tests; Jupiter recommends an API
                                 key for continuous production use`

const (
	shadowEndpointEnvironment = "MITHRIL_AGENT_SHADOW_RPC_URL"
	quoteEndpointEnvironment  = "MITHRIL_AGENT_QUOTE_RPC_URL"
	jupiterAPIKeyEnvironment  = "MITHRIL_AGENT_JUPITER_API_KEY"
	mainnetUSDCMint           = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
)

type shadowRunOptions struct {
	policyPath  string
	directory   string
	quoteSource string
	nodeCommand string
	quoteScript string
	pool        string
	inputMint   string
	outputMint  string
	once        bool
}

func runShadowRun(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := shadowRunOptions{}
	flags.StringVar(&options.policyPath, "policy", "", "shadow policy JSON")
	flags.StringVar(&options.directory, "dir", "", "journal and report directory")
	flags.StringVar(&options.quoteSource, "quote-provider", "", "must match policy when set")
	flags.StringVar(&options.nodeCommand, "node-command", "", "Node.js runtime")
	flags.StringVar(&options.quoteScript, "quote-script", "", "read-only quote adapter")
	flags.StringVar(&options.pool, "pool", "", "pool address to quote against")
	flags.StringVar(&options.inputMint, "input-mint", "", "mint being spent")
	flags.StringVar(&options.outputMint, "output-mint", "", "mint being received")
	flags.BoolVar(&options.once, "once", false, "take one observation and stop")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowRunUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("shadow run takes no positional arguments")
	}
	policy, err := loadShadowPolicy(options.policyPath)
	if err != nil {
		return err
	}
	run, err := openShadowRun(policy, options)
	if err != nil {
		return err
	}
	defer run.roll.Close()
	return run.drive(ctx, options.once, output)
}

// loadShadowPolicy reads the policy strictly: an unknown field is a refusal,
// not something to ignore. A shadow policy has no field that could name a
// wallet signing key, so a configuration that tries to supply one cannot be
// loaded at all.
func loadShadowPolicy(path string) (shadow.Policy, error) {
	if path == "" {
		return shadow.Policy{}, errors.New("this command requires --policy PATH " +
			"(create one with: mithril-agent shadow policy --out PATH ...)")
	}
	clean, err := cleanExistingPath(path)
	if err != nil {
		return shadow.Policy{}, err
	}
	var policy shadow.Policy
	if err := readStrictJSON(clean, &policy); err != nil {
		return shadow.Policy{}, err
	}
	if err := policy.Validate(); err != nil {
		return shadow.Policy{}, err
	}
	return policy, nil
}

// shadowRun holds the wiring for the whole run. A day boundary rebuilds the
// runner from exactly these dependencies, so crossing midnight cannot silently
// change what is being read or where it is recorded.
type shadowRun struct {
	policy         shadow.Policy
	primary        shadow.PriceReader
	secondary      shadow.PriceReader
	quotePrimary   shadow.PriceReader
	quoteSecondary shadow.PriceReader
	quoter         shadow.Quoter
	roll           *dailyJournal
	runner         *shadow.Runner
	// lastPrice is the most recent price actually observed. The report closes
	// on it rather than on a cost basis, which would not be a market price.
	lastPrice uint64
}

func openShadowRun(policy shadow.Policy, options shadowRunOptions) (*shadowRun, error) {
	endpoint := os.Getenv(shadowEndpointEnvironment)
	if err := validateShadowEndpoint(endpoint); err != nil {
		return nil, err
	}
	primary, err := pricesource.NewPythPush(publicAccountReader(endpoint), time.Now)
	if err != nil {
		return nil, err
	}
	var quotePrimary shadow.PriceReader
	var quoteSecondary shadow.PriceReader
	if policy.QuotePeg != nil {
		quotePrimary, err = pricesource.NewPythPushUSDC(publicAccountReader(endpoint), time.Now)
		if err != nil {
			return nil, err
		}
		quoteSecondary = pricesource.NewKraken(nil)
	}
	quoter, err := newShadowQuoter(policy, options)
	if err != nil {
		return nil, err
	}
	roll, err := newDailyJournal(options.directory)
	if err != nil {
		return nil, err
	}
	run := &shadowRun{
		policy: policy, primary: primary, secondary: pricesource.NewCoinbase(nil),
		quotePrimary: quotePrimary, quoteSecondary: quoteSecondary,
		quoter: quoter, roll: roll,
	}
	if err := roll.openFor(time.Now().UTC()); err != nil {
		roll.Close()
		return nil, err
	}
	records := roll.Records()
	if len(records) == 0 {
		run.runner, err = run.newRunner()
	} else {
		var ticks []shadow.Tick
		ticks, err = shadowTicksFrom(records, policy, true)
		if err == nil {
			run.runner, err = shadow.ResumeRunner(
				policy, run.primary, run.secondary, run.quoter, run.roll, ticks,
				run.quoteReaders()...,
			)
		}
		for index := len(ticks) - 1; index >= 0 && run.lastPrice == 0; index-- {
			run.lastPrice = ticks[index].PriceMicros
		}
	}
	if err != nil {
		roll.Close()
		return nil, err
	}
	return run, nil
}

// validateShadowEndpoint requires TLS for anything off-box, and permits plain
// HTTP only to a literal loopback IP.
//
// The reason for the exception is practical rather than lax: reading from a
// node on the same machine cannot be intercepted, and the operator's own
// verifying node is the whole point of this system. Refusing it would push
// people toward a public endpoint instead, which is strictly worse evidence.
func validateShadowEndpoint(endpoint string) error {
	// The endpoint itself never appears in an error: it may carry a key.
	refuse := errors.New(shadowEndpointEnvironment +
		" must be an https endpoint, or http on a literal loopback IP")
	if validatePublicAccountEndpoint(endpoint) != nil {
		return refuse
	}
	return nil
}

func validatePublicAccountEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("public account RPC endpoint is invalid")
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(parsed.Hostname()) {
			return nil
		}
	}
	return errors.New("public account RPC endpoint is invalid")
}

func isLoopbackHost(host string) bool {
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func (s *shadowRun) newRunner() (*shadow.Runner, error) {
	return shadow.NewRunner(
		s.policy, s.primary, s.secondary, s.quoter, s.roll, s.quoteReaders()...,
	)
}

func (s *shadowRun) quoteReaders() []shadow.PriceReader {
	if s.policy.QuotePeg == nil {
		return nil
	}
	return []shadow.PriceReader{s.quotePrimary, s.quoteSecondary}
}

// drive is the only place that knows about wall-clock time. Every decision is
// made by Step, which takes the time as an argument, so the same logic a test
// drives in a millisecond is what runs here for months.
func (s *shadowRun) drive(ctx context.Context, once bool, output io.Writer) error {
	ticker := time.NewTicker(s.policy.Tick())
	defer ticker.Stop()

	for {
		now := time.Now().UTC()
		if s.roll.RolledOver(now) {
			from, err := time.Parse("2006-01-02", s.roll.Day())
			if err != nil {
				return err
			}
			periodEnd := from.Add(24 * time.Hour)
			if err := s.runner.ClosePeriod(periodEnd.Add(-time.Nanosecond), s.lastPrice); err != nil {
				return err
			}
			if err := s.finishDayAt(output, periodEnd); err != nil {
				return err
			}
			// A new day is a new independent trial, with its own opening mark.
			fresh, err := s.newRunner()
			if err != nil {
				return err
			}
			s.runner, s.lastPrice = fresh, 0
		}
		tick, err := s.runner.Step(ctx, now)
		if err != nil {
			return err
		}
		if tick.PriceMicros != 0 {
			s.lastPrice = tick.PriceMicros
		}
		if err := printShadowTick(output, tick); err != nil {
			return err
		}
		if once {
			return nil
		}
		select {
		case <-ctx.Done():
			// A clean stop still produces the day's report, so an interrupted
			// run is not a silently discarded one.
			if err := s.runner.ClosePeriod(now, s.lastPrice); err != nil {
				return err
			}
			return s.finishDayAt(output, now)
		case <-ticker.C:
		}
	}
}

func printShadowTick(output io.Writer, tick shadow.Tick) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(tick)
}

// finishDay writes the period report next to the journal it came from.
func (s *shadowRun) finishDay(output io.Writer) error {
	if s.roll.Day() == "" {
		return nil
	}
	from, err := time.Parse("2006-01-02", s.roll.Day())
	if err != nil {
		return err
	}
	periodEnd := from.Add(24 * time.Hour)
	if err := s.runner.ClosePeriod(periodEnd.Add(-time.Nanosecond), s.lastPrice); err != nil {
		return err
	}
	return s.finishDayAt(output, periodEnd)
}

func (s *shadowRun) finishDayAt(output io.Writer, periodEnd time.Time) error {
	day := s.roll.Day()
	if day == "" || s.lastPrice == 0 {
		// Nothing was ever observed, so there is nothing to report. Writing a
		// report full of zeroes would read as a flat, uneventful day.
		return nil
	}
	from, err := time.Parse("2006-01-02", day)
	if err != nil {
		return err
	}
	if !periodEnd.After(from) || periodEnd.After(from.Add(24*time.Hour)) {
		return errors.New("shadow report end is outside its UTC day")
	}
	// Derived by replaying the day's whole journal, not from this process's
	// counters. A runner that restarted mid-day would otherwise report only
	// its own share of the day, and the stored report would not match the
	// record it sits beside.
	ticks, err := shadowTicksFrom(s.roll.Records(), s.policy, false)
	if err != nil {
		return err
	}
	replayed, err := shadow.Replay(s.policy, ticks)
	if err != nil {
		return err
	}
	recordedEnd, err := shadowReportEnd(from, replayed.PeriodEnd)
	if err != nil || !recordedEnd.Equal(periodEnd) {
		return errors.New("shadow journal does not contain the report period end")
	}
	report, err := shadow.BuildReport(
		s.policy, replayed.Ledger, replayed.Counts, replayed.Stats,
		replayed.ClosingPrice, from, recordedEnd,
	)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.roll.directory, "report-"+day+".json")
	if err := securefile.ReplacePrivate(path, append(encoded, '\n'), maxInputBytes); err != nil {
		return errors.New("could not write the shadow report")
	}
	return report.Render(output)
}

// shadowReportEnd converts the last nanosecond of a UTC day back to the
// exclusive midnight boundary shown in reports. The close marker has to stay
// in the day's own journal, while the report period ends at the next midnight.
func shadowReportEnd(from, recorded time.Time) (time.Time, error) {
	dayEnd := from.Add(24 * time.Hour)
	if recorded.Equal(dayEnd.Add(-time.Nanosecond)) {
		return dayEnd, nil
	}
	if !recorded.After(from) || recorded.After(dayEnd) {
		return time.Time{}, errors.New("shadow journal period end is outside its UTC day")
	}
	return recorded, nil
}

// shadowQuoter adapts the read-only Orca quote client to the narrow interface
// shadow mode needs. It can return amounts and nothing else: the instructions
// the client also returns are deliberately discarded here, so there is no path
// from a shadow quote to a transaction.
type shadowQuoter struct {
	client      *swapbuilder.Client
	pool        string
	inputMint   string
	outputMint  string
	primarySell bool
}

func newShadowQuoter(policy shadow.Policy, options shadowRunOptions) (shadow.Quoter, error) {
	route := policy.QuoteRoute
	if err := validateShadowRouteOverrides(route, options); err != nil {
		return nil, err
	}
	if route.Provider == shadow.QuoteJupiter {
		if options.nodeCommand != "" || options.quoteScript != "" || options.pool != "" {
			return nil, errors.New("Jupiter shadow quotes do not use --node-command, --quote-script, or --pool")
		}
		client, err := jupiterquote.New(os.Getenv(jupiterAPIKeyEnvironment))
		if err != nil {
			return nil, err
		}
		return &jupiterShadowQuoter{
			client: client, inputMint: route.InputMint, outputMint: route.OutputMint,
			primarySell: policy.IsSell(),
		}, nil
	}
	if route.Provider != shadow.QuoteOrca {
		return nil, errors.New("shadow quote provider must be orca or jupiter")
	}
	if options.nodeCommand == "" || options.quoteScript == "" {
		return nil, errors.New(
			"Orca shadow run requires --node-command and --quote-script")
	}
	// The quote adapter needs its own endpoint: it requires TLS, while the
	// price reader may be pointed at a loopback node. When only one is set,
	// the shadow endpoint is used and swapbuilder enforces its own TLS rule.
	quoteURL := os.Getenv(quoteEndpointEnvironment)
	if quoteURL == "" {
		quoteURL = os.Getenv(shadowEndpointEnvironment)
	}
	client, err := swapbuilder.New(swapbuilder.Config{
		NodeCommand: options.nodeCommand, ScriptPath: options.quoteScript,
		RPCURL: quoteURL,
	})
	if err != nil {
		return nil, err
	}
	return &shadowQuoter{
		client: client, pool: route.Pool,
		inputMint: route.InputMint, outputMint: route.OutputMint,
		primarySell: policy.IsSell(),
	}, nil
}

func validateShadowRouteOverrides(route shadow.QuoteRoute, options shadowRunOptions) error {
	for name, values := range map[string][2]string{
		"quote provider": {options.quoteSource, route.Provider},
		"pool":           {options.pool, route.Pool},
		"input mint":     {options.inputMint, route.InputMint},
		"output mint":    {options.outputMint, route.OutputMint},
	} {
		if values[0] != "" && values[0] != values[1] {
			return errors.New("shadow " + name + " does not match the policy")
		}
	}
	return nil
}

func (q *shadowQuoter) Quote(
	ctx context.Context,
	owner string,
	sell bool,
	inputAmount uint64,
	slippageBPS uint16,
) (shadow.Quote, error) {
	inputMint := q.inputMint
	if sell != q.primarySell {
		inputMint = q.outputMint
	}
	if inputMint == "" {
		return shadow.Quote{}, errors.New("shadow quote direction has no input mint")
	}
	result, err := q.client.Quote(ctx, swapbuilder.Request{
		Owner: owner, Pool: q.pool, InputMint: inputMint,
		InputAmount: inputAmount, SlippageBPS: slippageBPS,
	})
	if err != nil {
		return shadow.Quote{}, err
	}
	return shadow.Quote{
		InputAmount:     result.TokenIn,
		EstimatedOutput: result.TokenEstOut,
		MinimumOutput:   result.TokenMinOut,
	}, nil
}

// jupiterShadowQuoter exposes only the three amounts shadow accounting needs.
// Jupiter's executable instructions never cross this interface.
type jupiterShadowQuoter struct {
	client      *jupiterquote.Client
	inputMint   string
	outputMint  string
	primarySell bool
}

func (q *jupiterShadowQuoter) Quote(
	ctx context.Context,
	owner string,
	sell bool,
	inputAmount uint64,
	slippageBPS uint16,
) (shadow.Quote, error) {
	inputMint, outputMint := q.inputMint, q.outputMint
	if sell != q.primarySell {
		inputMint, outputMint = outputMint, inputMint
	}
	result, err := q.client.Quote(ctx, jupiterquote.Request{
		Taker: owner, InputMint: inputMint, OutputMint: outputMint,
		InputAmount: inputAmount, SlippageBPS: slippageBPS,
	})
	if err != nil {
		return shadow.Quote{}, err
	}
	return shadow.Quote{
		InputAmount:     result.InputAmount,
		EstimatedOutput: result.EstimatedOutput,
		MinimumOutput:   result.MinimumOutput,
	}, nil
}
