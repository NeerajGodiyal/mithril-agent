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
	"time"

	"github.com/Overclock-Validator/mithril-agent/readiness"
	"github.com/Overclock-Validator/mithril-agent/squads"
)

// funding check answers one question: does this spending limit cap movement
// from the intended Squads vault to the intended agent account?
//
// It only reads. It cannot create a limit, change one, or move anything through
// one. The cap is enforced on-chain by the Squads program, not by this process.
const fundingUsage = `Usage: mithril-agent funding check [options]

Reads a Squads spending limit on Devnet and reports whether it is a real
funding boundary for the agent account. Read-only: it creates nothing,
changes nothing, and moves nothing.

Devnet only, deliberately. Squads v4 is the same program on both clusters,
so the boundary can be proved here before any real money is involved.

  --spending-limit ADDRESS   the spending limit account to read
  --multisig ADDRESS         the Multisig config it must belong to
  --vault-index N            the expected asset-holding vault index (0-255)
  --destination ADDRESS      the only address funds may leave for
  --max-base-units N         the largest per-period cap you accept, in the mint's base units
  --mint ADDRESS             asset being capped (default: native SOL)
  --period LIST              accepted refill periods (default: one-time,daily)
  --owner ADDRESS            operator expected to control or vote in the Multisig
  --spender ADDRESS          only key expected to use this spending limit
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
	multisig := flags.String("multisig", "", "Multisig config the limit must belong to")
	vaultIndex := flags.Uint("vault-index", 256, "expected asset-holding vault index")
	destination := flags.String("destination", "", "only permitted destination")
	maxBaseUnits := flags.Uint64("max-base-units", 0, "largest per-period cap accepted")
	mint := flags.String("mint", squads.NativeMint, "asset being capped")
	periods := flags.String("period", "one-time,daily", "accepted refill periods")
	owner := flags.String("owner", "", "your wallet: the key expected to control the vault")
	spender := flags.String("spender", "", "the key expected to spend through the limit")
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
	if *vaultIndex > 255 {
		return errors.New("funding check requires --vault-index N from 0 to 255")
	}
	if *owner != "" && *spender == "" {
		return errors.New("funding check --owner requires --spender")
	}
	expectedVaultIndex := uint8(*vaultIndex)
	expect := squads.Expectation{
		Multisig: *multisig, VaultIndex: &expectedVaultIndex,
		Destination: *destination, Mint: *mint,
		MaxAmount: *maxBaseUnits, AllowedPeriods: accepted,
	}

	limit, readErr := readSpendingLimit(ctx, *limitAddress)
	report := fundingReport(limit, expect, readErr)
	if readErr == nil && *spender != "" {
		controlChecks := verifyFundingControl(ctx, limit, *owner, *spender)
		report = readiness.NewReport(append(report.Checks, controlChecks...))
	}
	if *asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	if _, err := fmt.Fprintf(output, "Funding boundary\n\n"); err != nil {
		return err
	}
	if readErr == nil {
		vaultAddress, vaultErr := squads.VaultAddress(limit.Multisig, limit.VaultIndex)
		if vaultErr != nil {
			return vaultErr
		}
		if _, err := fmt.Fprintf(output, "Asset-holding vault (index %d): %s\n", limit.VaultIndex, vaultAddress); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "Maximum through this spending limit: %s\n\n",
			squads.ExposureNote(limit)); err != nil {
			return err
		}
		// Available right now, modelling the program's lazy period reset: an
		// exhausted limit whose period has passed has in fact refilled.
		if _, err := fmt.Fprintf(output, "Available this period (local-clock projection): %d base units\n\n",
			limit.AvailableAt(time.Now().UTC())); err != nil {
			return err
		}
		if _, err := fmt.Fprint(output,
			"Scope: this checks one spending limit; other limits on the same vault may add exposure.\n\n"); err != nil {
			return err
		}
	}
	return report.Render(output)
}

// verifyFundingControl adds only evidence the operator explicitly requested.
// A spender can be checked from the limit alone; revocability additionally
// requires reading the Multisig config.
func verifyFundingControl(
	ctx context.Context,
	limit squads.SpendingLimit,
	owner,
	spender string,
) []readiness.Check {
	if owner == "" {
		findings := squads.VerifySpender(limit, spender)
		if len(findings) != 0 {
			return fundingFindingChecks(findings)
		}
		return []readiness.Check{{
			Name: "members", Title: "Spending-limit key", State: readiness.Ready,
			Detail: "only the expected key may use this spending limit",
		}}
	}
	multisigConfig, err := readMultisig(ctx, limit.Multisig)
	if err != nil {
		return []readiness.Check{{
			Name: "multisig-control", Title: "Multisig control", State: readiness.Blocked,
			Detail: err.Error(),
			Action: "Check the Multisig config address and provider access, then re-run: " +
				"mithril-agent funding check",
		}}
	}
	revocability, findings := squads.VerifyControl(multisigConfig, limit, squads.ControlExpectation{
		MultisigAddress: limit.Multisig, Owner: owner, Spender: spender,
	})
	if len(findings) != 0 {
		return fundingFindingChecks(findings)
	}
	return []readiness.Check{{
		Name: "multisig-control", Title: "Multisig control", State: readiness.Ready,
		Detail: string(revocability),
	}}
}

// readMultisig fetches and decodes the Multisig config the limit belongs to, with the
// same owner check as the spending limit read.
func readMultisig(ctx context.Context, address string) (squads.Multisig, error) {
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
		return squads.Multisig{}, errors.New("could not read the multisig account")
	}
	if result.Result.Value == nil || len(result.Result.Value.Data) == 0 {
		return squads.Multisig{}, errors.New("the multisig account does not exist")
	}
	if result.Result.Value.Owner != squads.ProgramID {
		return squads.Multisig{}, errors.New("that account is not owned by the Squads program")
	}
	raw, err := base64.StdEncoding.DecodeString(result.Result.Value.Data[0])
	if err != nil {
		return squads.Multisig{}, errors.New("the multisig account could not be decoded")
	}
	return squads.DecodeMultisig(raw)
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
			Detail: squads.ExposureNote(limit) + "; one spending limit, not total vault exposure",
		}})
	}
	return readiness.NewReport(fundingFindingChecks(findings))
}

func fundingFindingChecks(findings []squads.Finding) []readiness.Check {
	checks := make([]readiness.Check, 0, len(findings))
	for _, finding := range findings {
		checks = append(checks, readiness.Check{
			Name: finding.Check, Title: fundingTitle(finding.Check),
			State: readiness.Blocked, Detail: finding.Problem,
			Action: "Correct this in Squads, then re-run: mithril-agent funding check",
		})
	}
	return checks
}

func fundingTitle(check string) string {
	switch check {
	case "multisig":
		return "Multisig config"
	case "vault_index":
		return "Asset-holding vault"
	case "destinations":
		return "Where funds may go"
	case "period":
		return "How often it refills"
	case "amount":
		return "How much per period"
	case "members":
		return "Spending-limit key"
	case "revocability", "config_authority", "multisig_binding", "control_expectation":
		return "Multisig control"
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
	limit, err := squads.DecodeSpendingLimit(raw)
	if err != nil {
		return squads.SpendingLimit{}, err
	}
	expectedAddress, err := squads.SpendingLimitAddress(limit.Multisig, limit.CreateKey)
	if err != nil {
		return squads.SpendingLimit{}, err
	}
	if expectedAddress != address {
		return squads.SpendingLimit{}, errors.New("the spending limit address does not match its on-chain seeds")
	}
	return limit, nil
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
