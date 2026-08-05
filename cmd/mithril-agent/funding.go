package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Overclock-Validator/mithril-agent/readiness"
	"github.com/Overclock-Validator/mithril-agent/squads"
)

// funding check answers one question: is the cap between the operator's own
// money and the agent's account real, and is it aimed at the right place?
//
// It only reads. It cannot create a limit, change one, or move anything through
// one — that is the point. A funding boundary is worth having precisely because
// it is enforced by a program this software does not control; if this command
// could use it, the boundary would only be as good as this software.
const fundingUsage = `Usage: mithril-agent funding check [options]

Reads a Squads spending limit on Devnet and reports whether it is a real
funding boundary for the agent account. Read-only: it creates nothing,
changes nothing, and moves nothing.

Devnet only, deliberately. Squads v4 is the same program on both clusters,
so the boundary can be proved here before any real money is involved.

  --spending-limit ADDRESS   the spending limit account to read
  --multisig ADDRESS         the vault it must belong to
  --destination ADDRESS      the only address funds may leave for
  --max-lamports N           the largest per-period cap you accept
  --mint ADDRESS             asset being capped (default: native SOL)
  --period LIST              accepted refill periods (default: one-time,daily)
  --json                     emit stable JSON for automation`

func runFunding(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, err := fmt.Fprintln(output, fundingUsage)
		return err
	}
	if args[0] != "check" {
		return fmt.Errorf("unknown funding command %q", args[0])
	}
	return runFundingCheck(ctx, args[1:], output)
}

func runFundingCheck(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("funding check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	limitAddress := flags.String("spending-limit", "", "spending limit account")
	multisig := flags.String("multisig", "", "vault the limit must belong to")
	destination := flags.String("destination", "", "only permitted destination")
	maxLamports := flags.Uint64("max-lamports", 0, "largest per-period cap accepted")
	mint := flags.String("mint", squads.NativeMint, "asset being capped")
	periods := flags.String("period", "one-time,daily", "accepted refill periods")
	asJSON := flags.Bool("json", false, "emit stable JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, fundingUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("funding check takes no positional arguments")
	}
	accepted, err := parsePeriods(*periods)
	if err != nil {
		return err
	}
	if *limitAddress == "" {
		return errors.New("funding check requires --spending-limit ADDRESS")
	}
	expect := squads.Expectation{
		Multisig: *multisig, Destination: *destination, Mint: *mint,
		MaxAmount: *maxLamports, AllowedPeriods: accepted,
	}

	limit, readErr := readSpendingLimit(ctx, *limitAddress)
	report := fundingReport(limit, expect, readErr)
	if *asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	if _, err := fmt.Fprintf(output, "Funding boundary\n\n"); err != nil {
		return err
	}
	if readErr == nil {
		if _, err := fmt.Fprintf(output, "Most that can ever leave the vault: %s\n\n",
			squads.ExposureNote(limit)); err != nil {
			return err
		}
	}
	return report.Render(output)
}

// fundingReport turns the findings into the same readiness shape doctor uses,
// so "is this ready" means one thing across the whole tool.
func fundingReport(
	limit squads.SpendingLimit,
	expect squads.Expectation,
	readErr error,
) readiness.Report {
	if readErr != nil {
		return readiness.NewReport([]readiness.Check{{
			Name: "spending-limit", Title: "Spending limit", State: readiness.Blocked,
			Detail: readErr.Error(),
			Action: "Check the address is a Squads spending limit on Devnet, then re-run: " +
				"mithril-agent funding check --spending-limit ADDRESS ...",
		}})
	}
	findings := squads.Verify(limit, expect)
	if len(findings) == 0 {
		return readiness.NewReport([]readiness.Check{{
			Name: "spending-limit", Title: "Funding boundary", State: readiness.Ready,
			Detail: squads.ExposureNote(limit),
		}})
	}
	checks := make([]readiness.Check, 0, len(findings))
	for _, finding := range findings {
		checks = append(checks, readiness.Check{
			Name: finding.Check, Title: fundingTitle(finding.Check),
			State: readiness.Blocked, Detail: finding.Problem,
			Action: "Correct this in Squads, then re-run: mithril-agent funding check",
		})
	}
	return readiness.NewReport(checks)
}

func fundingTitle(check string) string {
	switch check {
	case "multisig":
		return "Vault"
	case "destinations":
		return "Where funds may go"
	case "period":
		return "How often it refills"
	case "amount":
		return "How much per period"
	case "members":
		return "Who may spend"
	case "mint":
		return "Asset"
	default:
		return "Expectation"
	}
}

// readSpendingLimit fetches and decodes the account, checking that it is owned
// by the Squads program. Without the owner check, any account that happened to
// start with the right eight bytes would be read as a funding boundary.
func readSpendingLimit(ctx context.Context, address string) (squads.SpendingLimit, error) {
	var result struct {
		Result struct {
			Value *struct {
				Data  []string `json:"data"`
				Owner string   `json:"owner"`
			} `json:"value"`
		} `json:"result"`
	}
	if err := walletRPC(ctx, "getAccountInfo",
		[]any{address, map[string]any{"encoding": "base64"}}, &result); err != nil {
		return squads.SpendingLimit{}, errors.New("could not read the spending limit account")
	}
	if result.Result.Value == nil || len(result.Result.Value.Data) == 0 {
		return squads.SpendingLimit{}, errors.New("the spending limit account does not exist")
	}
	if result.Result.Value.Owner != squads.ProgramID {
		return squads.SpendingLimit{}, errors.New("that account is not owned by the Squads program")
	}
	raw, err := base64.StdEncoding.DecodeString(result.Result.Value.Data[0])
	if err != nil {
		return squads.SpendingLimit{}, errors.New("the spending limit account could not be decoded")
	}
	return squads.DecodeSpendingLimit(raw)
}

func parsePeriods(value string) ([]squads.Period, error) {
	named := map[string]squads.Period{
		"one-time": squads.OneTime, "daily": squads.Daily,
		"weekly": squads.Weekly, "monthly": squads.Monthly,
	}
	var periods []squads.Period
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		if part == "" {
			continue
		}
		period, ok := named[part]
		if !ok {
			return nil, fmt.Errorf("unknown period %q", part)
		}
		periods = append(periods, period)
	}
	if len(periods) == 0 {
		return nil, errors.New("at least one accepted period is required")
	}
	return periods, nil
}
