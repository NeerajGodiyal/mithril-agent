package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const proposalInventoryUsage = `Usage: mithril-agent proposal inventory --inventory PATH --authority-policy PATH [--operation show|initialize|reserve|account|admit|acquire] [--claim PATH] [--submitter-policy PATH]

Fresh admit also requires --paper-policy PATH --paper-journal PATH
--paper-bounds PATH --candidate PATH --acquisition PATH --schedule-start-unix N
--max-decision-age-seconds N --max-acquisition-age-seconds N.
Admit recovers an existing retained per-head claim before reading these inputs
or opening RPCs. Fresh admission records an unsigned claim and inventory reserve;
recovery never renews the request and is not permission to execute it.
Fresh admit requires the configured Mithril RPC for chain reads and simulation,
plus two independent evidence RPCs. Recovery requires none of these RPCs.
Acquire uses the same paper and age inputs, but forbids --candidate and
--acquisition: it acquires a Jupiter proposal into a derived private receipt.
It recovers retained claims first and never renews retained acquisition evidence.

Show verifies existing private inventory without writing or RPC. Initialize and
reserve use two configured independent RPCs for wallet reads only. Reserve and
account require the exact retained original claim; account also requires the
protected submitter policy and existing finalized recovery evidence. Account
can be retried after a terminal/accounting crash. No operation releases the
original claim, resets signer caps, grants, signs, submits, or activates trading.
All paths must be distinct, clean absolute paths. Default operation: show.`

func runProposalInventory(ctx context.Context, args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("proposal inventory", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	operation := flags.String("operation", "show", "inventory operation")
	path := flags.String("inventory", "", "protected inventory journal")
	authorityPath := flags.String("authority-policy", "", "protected authority policy")
	claimPath := flags.String("claim", "", "original claim journal")
	submitterPath := flags.String("submitter-policy", "", "protected recovery policy")
	paperPolicyPath := flags.String("paper-policy", "", "frozen paper policy")
	paperJournalPath := flags.String("paper-journal", "", "verified paper journal")
	paperBoundsPath := flags.String("paper-bounds", "", "private paper intent bounds")
	candidatePath := flags.String("candidate", "", "private unsigned candidate")
	acquisitionPath := flags.String("acquisition", "", "protected acquisition receipt")
	scheduleStart := flags.Int64("schedule-start-unix", 0, "original schedule start in Unix seconds")
	decisionAge := flags.Uint64("max-decision-age-seconds", 0, "positive decision age bound in seconds")
	acquisitionAge := flags.Uint64("max-acquisition-age-seconds", 0, "positive acquisition age bound in seconds")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, proposalInventoryUsage)
		}
		return err
	}
	paths := []string{*path, *authorityPath}
	admitFlags := map[string]bool{"paper-policy": true, "paper-journal": true, "paper-bounds": true, "candidate": true, "acquisition": true, "schedule-start-unix": true, "max-decision-age-seconds": true, "max-acquisition-age-seconds": true}
	unexpectedAdmitFlag := false
	flags.Visit(func(f *flag.Flag) {
		unexpectedAdmitFlag = unexpectedAdmitFlag || (*operation != "admit" && *operation != "acquire" && admitFlags[f.Name])
	})
	if unexpectedAdmitFlag {
		return errors.New("paper admission inputs require admit or acquire")
	}
	switch *operation {
	case "admit", "acquire":
		if *claimPath != "" || *submitterPath != "" {
			return errors.New("admit derives its claim path and does not accept a submitter policy")
		}
		if *operation == "acquire" && (*candidatePath != "" || *acquisitionPath != "") {
			return errors.New("acquire derives its receipt and does not accept candidate or acquisition paths")
		}
		for _, input := range []string{*paperPolicyPath, *paperJournalPath, *paperBoundsPath, *candidatePath, *acquisitionPath} {
			if input != "" {
				paths = append(paths, input)
			}
		}
	case "show", "initialize":
		if *claimPath != "" || *submitterPath != "" {
			return errors.New("inventory operation does not accept claim or submitter policy")
		}
	case "reserve":
		paths = append(paths, *claimPath)
		if *submitterPath != "" {
			return errors.New("reserve does not accept submitter policy")
		}
	case "account":
		paths = append(paths, *claimPath, *submitterPath)
	default:
		return errors.New("unknown inventory operation")
	}
	if flags.NArg() != 0 || !distinctAbsolutePaths(paths...) {
		return errors.New("inventory requires distinct absolute protected paths")
	}
	if *operation != "show" && now == nil {
		return errors.New("inventory mutation requires a clock")
	}
	var authority policyauthority.Policy
	if err := readStrictJSON(*authorityPath, &authority); err != nil {
		return errors.New("read protected authority policy")
	}
	if err := authority.Validate(); err != nil {
		return err
	}
	if *operation == "admit" || *operation == "acquire" {
		claim, found, err := execution.RecoverWalletPaperClaim(*path, authority, now())
		if err != nil {
			return err
		}
		if !found {
			freshPaths := []string{*path, *authorityPath, *paperPolicyPath, *paperJournalPath, *paperBoundsPath}
			if *operation == "admit" {
				freshPaths = append(freshPaths, *candidatePath, *acquisitionPath)
			}
			if !distinctAbsolutePaths(freshPaths...) ||
				*scheduleStart <= 0 || *decisionAge == 0 || *acquisitionAge == 0 ||
				*decisionAge > uint64((1<<63-1)/time.Second) || *acquisitionAge > uint64((1<<63-1)/time.Second) {
				return errors.New("fresh admission requires all protected paper inputs, schedule start, and positive bounded age seconds")
			}
			paper, err := loadShadowPolicy(*paperPolicyPath)
			if err != nil {
				return err
			}
			ticks, err := readShadowTicks(*paperJournalPath, paper)
			if err != nil {
				return err
			}
			var bounds proposalcheck.PaperIntentBounds
			if err := readStrictJSON(*paperBoundsPath, &bounds); err != nil {
				return errors.New("read private paper bounds")
			}
			var candidate proposalcheck.Candidate
			if *operation == "admit" {
				raw, err := securefile.ReadPrivate(*candidatePath, 1<<20)
				if err != nil {
					return errors.New("read private unsigned candidate")
				}
				candidate, err = proposalcheck.DecodeCandidate(raw)
				if err != nil {
					return err
				}
			}
			providers, err := openUnboundRPCProviders(os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL"),
				os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL"), os.Getenv("MITHRIL_AGENT_SECONDARY_RPC_URL"))
			if err != nil {
				return errors.New("fresh admission Mithril and evidence RPC configuration is invalid")
			}
			lifecycle, err := txflow.New(providers.mithril, providers.primary, providers.secondary)
			if err != nil {
				return err
			}
			if *operation == "acquire" {
				route := authority.TransactionPolicy.Jupiter
				if route == nil || len(ticks) == 0 || ticks[len(ticks)-1].DecisionQuote == nil {
					return errors.New("acquire requires a protected Jupiter route and recorded decision quote")
				}
				request := jupiterquote.Request{Taker: paper.Observe, InputMint: route.InputMint,
					OutputMint: route.OutputMint, InputAmount: ticks[len(ticks)-1].DecisionQuote.InputAmount,
					SlippageBPS: paper.SlippageBPS}
				if route.NativeInput() {
					request.DestinationTokenAccount, err = orcaswap.AssociatedTokenAddress(request.Taker, request.OutputMint)
					if err != nil {
						return errors.New("derive canonical output token account")
					}
				}
				claim, err = execution.AcquireWalletPaperClaim(ctx, *path, authority, paper, ticks, bounds, request,
					*scheduleStart, now(), time.Duration(*decisionAge)*time.Second, time.Duration(*acquisitionAge)*time.Second,
					proposalStrategyBuilder{}, lifecycle, providers.primary, providers.secondary, lifecycle)
			} else {
				claim, err = execution.PrepareWalletPaperClaim(ctx, *path, authority, paper, ticks, bounds, candidate,
					*scheduleStart, now(), time.Duration(*decisionAge)*time.Second, *acquisitionPath,
					time.Duration(*acquisitionAge)*time.Second, lifecycle, providers.primary, providers.secondary, lifecycle)
			}
			if err != nil {
				return err
			}
		}
		return json.NewEncoder(output).Encode(struct {
			Status    string `json:"status"`
			ClaimPath string `json:"claim_path"`
			ActionID  string `json:"action_id"`
			Binding   string `json:"binding_sha256"`
			Head      string `json:"head_sha256"`
			Recovered bool   `json:"recovered"`
			Pending   bool   `json:"pending"`
			CanSign   bool   `json:"can_sign"`
			CanSubmit bool   `json:"can_submit"`
		}{Status: "unsigned_claim_not_authorized", ClaimPath: claim.ClaimPath, ActionID: claim.Request.ActionID,
			Binding: claim.Inventory.BindingSHA256, Head: claim.Inventory.HeadSHA256, Recovered: claim.Recovered,
			Pending: claim.Inventory.PendingSHA256 != ""})
	}
	var inventory execution.WalletInventory
	var err error
	if *operation == "show" {
		inventory, err = execution.ReadWalletInventory(*path, authority)
	} else if *operation == "account" {
		var recovery submitter.Policy
		if err := readStrictJSON(*submitterPath, &recovery); err != nil {
			return errors.New("read protected submitter policy")
		}
		if err := submitter.ValidateJupiterPolicy(recovery); err != nil {
			return err
		}
		request, readErr := policyauthority.ReadClaimedPaperRequest(*claimPath, authority)
		if readErr != nil {
			return readErr
		}
		inventory, err = execution.FinalizeWalletClaim(*path, *claimPath, authority, request, recovery, now())
	} else {
		primary, secondary, openErr := openEvidenceProviders(os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL"), os.Getenv("MITHRIL_AGENT_SECONDARY_RPC_URL"))
		if openErr != nil {
			return errors.New("inventory evidence RPC configuration is invalid")
		}
		lifecycle, openErr := txflow.NewEvidenceLifecycle(primary, secondary)
		if openErr != nil {
			return openErr
		}
		if *operation == "initialize" {
			inventory, err = execution.InitializeWalletInventory(ctx, *path, authority, lifecycle, now())
		} else {
			request, readErr := policyauthority.ReadClaimedPaperRequest(*claimPath, authority)
			if readErr != nil {
				return readErr
			}
			inventory, err = execution.ReserveWalletClaim(ctx, *path, *claimPath, authority, request, lifecycle, now())
		}
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Status    string `json:"status"`
		Binding   string `json:"binding_sha256"`
		Head      string `json:"head_sha256"`
		Native    uint64 `json:"native_lamports,string"`
		Token     uint64 `json:"token_units,string"`
		Pending   bool   `json:"pending"`
		CanSign   bool   `json:"can_sign"`
		CanSubmit bool   `json:"can_submit"`
	}{Status: "inventory_verified_not_authorized", Binding: inventory.BindingSHA256, Head: inventory.HeadSHA256,
		Native: inventory.NativeLamports, Token: inventory.TokenUnits, Pending: inventory.PendingSHA256 != ""})
}
