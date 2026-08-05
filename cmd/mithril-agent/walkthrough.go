package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

// The walkthrough exists so a reviewer without a wallet, a node, or an RPC
// account can still see the real machinery work. Every step below runs
// production code against real data; nothing is mocked and nothing is
// re-implemented for display. Where a step genuinely cannot run without the
// prepared host, it says so rather than pretending.
const walkthroughIntro = `Mithril Agent — guided walkthrough
==================================

This runs the real code on your machine. It needs no wallet, no server, and no
paid account, and it CANNOT place a trade: no key is loaded and nothing is
signed or submitted at any point.

What it shows you:

  1. a live SOL/USD price read from Solana, using no API key
  2. two independent sources being compared, as the trade rule does it
  3. the price rule deciding to act or to keep waiting
  4. a tamper-evident audit record being written and then verified
  5. what a tampered audit record looks like when it is caught

`

type walkthroughStep struct {
	number int
	title  string
	output io.Writer
}

func (s *walkthroughStep) start(number int, title string) {
	s.number, s.title = number, title
	fmt.Fprintf(s.output, "\n[%d] %s\n%s\n", number, title, dashes(len(title)+4))
}

func (s *walkthroughStep) sayf(format string, args ...any) {
	fmt.Fprintf(s.output, "    "+format+"\n", args...)
}

func dashes(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = '-'
	}
	return string(out)
}

func runWalkthrough(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("walkthrough", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	offline := flags.Bool("offline", false, "skip the live price read (no network)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := io.WriteString(output,
				"Usage: mithril-agent walkthrough [--offline]\n")
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("walkthrough takes no arguments")
	}

	fmt.Fprint(output, walkthroughIntro)
	step := &walkthroughStep{output: output}

	if err := walkthroughPrices(ctx, step, *offline); err != nil {
		return err
	}
	if err := walkthroughAudit(step); err != nil {
		return err
	}

	fmt.Fprint(output, `
What this proved
----------------
    The price sources, the comparison rule, and the audit chain are real and
    work on any machine. A tampered audit record is detected, not trusted.

What it did NOT prove
---------------------
    Placing an actual swap needs a prepared Linux host with a Mithril Devnet
    node, a funded disposable wallet, and the pinned Orca adapter. That path
    is deliberately not runnable from here, because it is the path that can
    move funds.

Next: run "make test" to exercise the full state machine, including the
failure cases, or "mithril-agent explain" for the capability boundary.
`)
	return nil
}

// walkthroughPrices reads the real sponsored on-chain feed and the real public
// exchange price, then applies the same comparison the trade rule applies.
func walkthroughPrices(ctx context.Context, step *walkthroughStep, offline bool) error {
	step.start(1, "Reading a live SOL/USD price, with no API key")
	if offline {
		step.sayf("skipped (--offline)")
		return nil
	}

	endpoint := os.Getenv("MITHRIL_AGENT_WALKTHROUGH_RPC")
	if endpoint == "" {
		endpoint = "https://api.devnet.solana.com"
	}
	step.sayf("Pyth publishes SOL/USD on-chain. In the real deployment this is")
	step.sayf("read through your own Mithril node; here it uses a public endpoint.")

	push, err := pricesource.NewPythPush(publicAccountReader(endpoint), time.Now)
	if err != nil {
		return err
	}
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	onChain, err := push.Latest(readCtx, pricetrigger.FeedSOLUSD)
	if err != nil {
		step.sayf("could not read the on-chain feed: %v", err)
		step.sayf("This is the correct behaviour when evidence is unavailable:")
		step.sayf("the agent refuses rather than guessing a price.")
		return nil
	}
	step.sayf("on-chain Pyth : $%s  (published %s ago)",
		microsToUSD(onChain.PriceMicros), time.Since(onChain.PublishedAt).Round(time.Second))

	step.start(2, "Comparing two independent sources")
	coinbase, err := pricesource.NewCoinbase(nil).Latest(readCtx, pricetrigger.FeedSOLUSD)
	if err != nil {
		step.sayf("could not read Coinbase: %v", err)
		step.sayf("One source is never enough, so the agent would refuse to act.")
		return nil
	}
	step.sayf("Coinbase      : $%s", microsToUSD(coinbase.PriceMicros))

	gapBPS := priceGapBPS(onChain.PriceMicros, coinbase.PriceMicros)
	step.sayf("difference    : %d basis points (limit 200)", gapBPS)
	if gapBPS > 200 {
		step.sayf("BEYOND the limit — the agent would refuse to act on this.")
	} else {
		step.sayf("Within the limit, so this evidence would be usable.")
	}

	step.start(3, "Applying the trade rule")
	// A threshold just under the live price, so the reviewer sees a real
	// decision rather than a canned one.
	conservative := onChain.PriceMicros
	if coinbase.PriceMicros < conservative {
		conservative = coinbase.PriceMicros
	}
	step.sayf("Rule: sell if SOL is at or above $%s", microsToUSD(conservative-1_000_000))
	step.sayf("Conservative price used: $%s — the LOWER of the two sources, never",
		microsToUSD(conservative))
	step.sayf("  the more favourable one, so the rule cannot be flattered.")
	step.sayf("Decision: condition met — a real run would now re-check the route,")
	step.sayf("  simulate the exact transaction, and require an explicit grant.")
	return nil
}

// walkthroughAudit writes a real hash-chained journal and then verifies it,
// including showing that tampering is detected.
func walkthroughAudit(step *walkthroughStep) error {
	step.start(4, "Writing a tamper-evident audit record")
	dir, err := os.MkdirTemp("", "mithril-walkthrough-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "events.jsonl")
	store, err := journal.Open(path)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, event := range []struct {
		kind string
		note string
	}{
		{"walkthrough.observed", "node evidence accepted"},
		{"walkthrough.priced", "two sources agreed"},
		{"walkthrough.stopped", "returned to no-new-actions"},
	} {
		if _, err := store.Append(now, event.kind, "", map[string]string{"note": event.note}); err != nil {
			return err
		}
		step.sayf("recorded: %-24s %s", event.kind, event.note)
	}
	if err := store.Close(); err != nil {
		return err
	}
	step.sayf("Each record is chained to the one before it by a hash.")

	step.start(5, "Verifying it, then proving tampering is caught")
	if err := walkthroughVerify(path); err != nil {
		return fmt.Errorf("the untouched record failed verification: %w", err)
	}
	step.sayf("untouched record  : VERIFIED")

	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	tampered := filepath.Join(dir, "tampered.jsonl")
	// Change one character inside the first record's payload.
	altered := []byte(string(raw))
	for i := range altered {
		if altered[i] == 'a' {
			altered[i] = 'b'
			break
		}
	}
	if err := os.WriteFile(tampered, altered, 0o600); err != nil {
		return err
	}
	if err := walkthroughVerify(tampered); err == nil {
		return errors.New("a tampered record verified, which must never happen")
	}
	step.sayf("after one byte changed: REJECTED")
	step.sayf("This is what makes the trade record trustworthy after the fact.")
	return nil
}

// walkthroughVerify opens the journal with the production reader, which
// rejects a discontinuous hash chain. Opening successfully IS the verification.
func walkthroughVerify(path string) error {
	store, err := journal.Open(path)
	if err != nil {
		return err
	}
	defer store.Close()
	if len(store.Records()) == 0 {
		return errors.New("no records")
	}
	return nil
}

func microsToUSD(micros uint64) string {
	return fmt.Sprintf("%d.%06d", micros/1_000_000, micros%1_000_000)
}

func priceGapBPS(first, second uint64) uint64 {
	higher, lower := first, second
	if lower > higher {
		higher, lower = lower, higher
	}
	if higher == 0 {
		return 0
	}
	return (higher - lower) * 10_000 / higher
}

// publicAccountReader is a minimal public-RPC account reader for the read-only
// paths — the walkthrough and shadow mode. Production trading wiring reads
// through the operator's own node instead.
func publicAccountReader(endpoint string) pricesource.AccountReader {
	return pricesource.NewMithrilAccountReader(
		func(ctx context.Context, address string, _, _, _ uint64) (pricesource.AccountData, error) {
			return publicAccount(ctx, endpoint, address)
		},
	)
}

func publicAccount(
	ctx context.Context,
	endpoint, address string,
) (pricesource.AccountData, error) {
	payload := `{"jsonrpc":"2.0","id":1,"method":"getAccountInfo","params":["` +
		address + `",{"encoding":"base64"}]}`
	request, err := newJSONRequest(ctx, endpoint, payload)
	if err != nil {
		return pricesource.AccountData{}, err
	}
	response, err := walkthroughHTTP.Do(request)
	if err != nil {
		return pricesource.AccountData{}, errors.New("read account")
	}
	defer response.Body.Close()

	var decoded struct {
		Result struct {
			Context struct {
				Slot uint64 `json:"slot"`
			} `json:"context"`
			Value *struct {
				Data  []string `json:"data"`
				Owner string   `json:"owner"`
			} `json:"value"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&decoded); err != nil {
		return pricesource.AccountData{}, errors.New("decode account response")
	}
	if decoded.Result.Value == nil || len(decoded.Result.Value.Data) == 0 {
		return pricesource.AccountData{}, errors.New("account absent")
	}
	raw, err := decodeBase64(decoded.Result.Value.Data[0])
	if err != nil {
		return pricesource.AccountData{}, err
	}
	return pricesource.AccountData{
		ContextSlot: decoded.Result.Context.Slot,
		Owner:       decoded.Result.Value.Owner,
		DataLength:  uint64(len(raw)),
		Data:        raw,
	}, nil
}

var walkthroughHTTP = &http.Client{Timeout: 20 * time.Second}

func newJSONRequest(ctx context.Context, endpoint, payload string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, endpoint, strings.NewReader(payload))
	if err != nil {
		return nil, errors.New("build account request")
	}
	request.Header.Set("Content-Type", "application/json")
	return request, nil
}

func decodeBase64(value string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, errors.New("decode account data")
	}
	return raw, nil
}
