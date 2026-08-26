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

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/swapbuilder"
)

// Shadow mode is the only part of this system pointed at Mainnet, and it is
// safe to point there for a structural reason: the shadow package has no
// signer, no submitter, and no way to name a key. This command wires it to real
// data and nothing else.
//
// Each UTC day is an independent trial. The books open at the day's first
// observed price and close with that day's report, so one lucky day cannot be
// carried into the next and every period is scored out of sample against a hold
// benchmark that opened at the same moment. That is what makes a run of daily
// reports a walk-forward rather than a backtest.
const shadowRunUsage = `Usage: mithril-agent shadow run --policy PATH --dir PATH [options]

Watches a live market and records what the rule would have done. Nothing is
signed and nothing is submitted; no key is loaded at any point.

  --policy PATH         shadow policy JSON
  --dir PATH            directory for the daily journals and reports
  --node-command PATH   Devnet Node.js runtime for the read-only Orca adapter
  --quote-script PATH   Devnet read-only Orca adapter
  --once                take a single observation and stop

Endpoints come from the environment and are never printed, logged, or written
to the journal:

  MITHRIL_AGENT_SHADOW_RPC_URL   price reads; https, or http on loopback so it
                                 can read your own verifying node
  MITHRIL_AGENT_QUOTE_RPC_URL    the quote adapter; https only`

const (
	shadowEndpointEnvironment = "MITHRIL_AGENT_SHADOW_RPC_URL"
	quoteEndpointEnvironment  = "MITHRIL_AGENT_QUOTE_RPC_URL"
)

type shadowRunOptions struct {
	policyPath  string
	directory   string
	nodeCommand string
	quoteScript string
	once        bool
}

func runShadowRun(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := shadowRunOptions{}
	flags.StringVar(&options.policyPath, "policy", "", "shadow policy JSON")
	flags.StringVar(&options.directory, "dir", "", "journal and report directory")
	flags.StringVar(&options.nodeCommand, "node-command", "", "Node.js runtime")
	flags.StringVar(&options.quoteScript, "quote-script", "", "read-only quote adapter")
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
// not something to ignore. A shadow policy has no field that could name a key,
// so a configuration that tries to supply one cannot be loaded at all.
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
		quoter: quoter, roll: roll,
	}
	if policy.QuotePeg != nil {
		run.quotePrimary = primary
		run.quoteSecondary = pricesource.NewKraken(nil)
	}
	if run.runner, err = run.newRunner(); err != nil {
		roll.Close()
		return nil, err
	}
	return run, nil
}

// validateShadowEndpoint requires TLS for anything off-box, and permits plain
// HTTP only to loopback.
//
// The reason for the exception is practical rather than lax: reading from a
// node on the same machine cannot be intercepted, and the operator's own
// verifying node is the whole point of this system. Refusing it would push
// people toward a public endpoint instead, which is strictly worse evidence.
func validateShadowEndpoint(endpoint string) error {
	// The endpoint itself never appears in an error: it may carry a key.
	refuse := errors.New(shadowEndpointEnvironment +
		" must be an https endpoint, or http on loopback")
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return refuse
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(parsed.Hostname()) {
			return nil
		}
	}
	return refuse
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func (s *shadowRun) newRunner() (*shadow.Runner, error) {
	if s.policy.QuotePeg != nil {
		return shadow.NewRunner(
			s.policy, s.primary, s.secondary, s.quoter, s.roll,
			s.quotePrimary, s.quoteSecondary,
		)
	}
	return shadow.NewRunner(s.policy, s.primary, s.secondary, s.quoter, s.roll)
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
			if err := s.finishDay(output); err != nil {
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
			return s.finishDay(output)
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
	// Derived by replaying the day's whole journal, not from this process's
	// counters. A runner that restarted mid-day would otherwise report only
	// its own share of the day, and the stored report would not match the
	// record it sits beside.
	ticks, err := shadowTicksFrom(s.roll.Records())
	if err != nil {
		return err
	}
	replayed, err := shadow.Replay(s.policy, ticks)
	if err != nil {
		return err
	}
	report, err := shadow.BuildReport(
		s.policy, replayed.Ledger, replayed.Counts, replayed.Stats,
		replayed.ClosingPrice, from, from.Add(24*time.Hour),
	)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.roll.directory, "report-"+day+".json")
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		return errors.New("could not write the shadow report")
	}
	return report.Render(output)
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
	initialSell bool
}

type jupiterShadowQuoter struct {
	client      *jupiterquote.Client
	inputMint   string
	outputMint  string
	initialSell bool
}

func newShadowQuoter(policy shadow.Policy, options shadowRunOptions) (shadow.Quoter, error) {
	if policy.QuoteRoute.Provider == shadow.QuoteJupiter {
		client, err := jupiterquote.New(os.Getenv("MITHRIL_AGENT_JUPITER_API_KEY"))
		if err != nil {
			return nil, err
		}
		return &jupiterShadowQuoter{
			client: client, inputMint: policy.QuoteRoute.InputMint,
			outputMint: policy.QuoteRoute.OutputMint, initialSell: policy.IsSell(),
		}, nil
	}
	if policy.QuoteRoute.Provider != shadow.QuoteOrca ||
		options.nodeCommand == "" || options.quoteScript == "" {
		return nil, errors.New(
			"Devnet shadow run requires --node-command and --quote-script")
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
		client: client, pool: policy.QuoteRoute.Pool,
		inputMint: policy.QuoteRoute.InputMint, outputMint: policy.QuoteRoute.OutputMint,
		initialSell: policy.IsSell(),
	}, nil
}

func (q *shadowQuoter) Quote(
	ctx context.Context,
	owner string,
	sell bool,
	inputAmount uint64,
	slippageBPS uint16,
) (shadow.Quote, error) {
	inputMint := q.inputMint
	if sell != q.initialSell {
		inputMint = q.outputMint
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

func (q *jupiterShadowQuoter) Quote(
	ctx context.Context,
	owner string,
	sell bool,
	inputAmount uint64,
	slippageBPS uint16,
) (shadow.Quote, error) {
	inputMint, outputMint := q.inputMint, q.outputMint
	if sell != q.initialSell {
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
		InputAmount: result.InputAmount, EstimatedOutput: result.EstimatedOutput,
		MinimumOutput: result.MinimumOutput,
	}, nil
}
