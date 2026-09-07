package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const proposalStrategyUsage = `Usage: mithril-agent proposal strategy --operation show|initialize|observe|account|prepare|acquire|step|retire-acquisition|cancel-decision [options]

Common verified history inputs:
  --strategy PATH                  protected strategy journal
  --authority-policy PATH          original authority policy
  --submitter-policy PATH          original recovery policy
  --historical-submitter-policy PATH
                                   repeat for original later-leg policies (64 unique files total)

Initialize also requires --inventory PATH --claim PATH --paper-policy PATH
--paper-journal PATH --paper-bounds PATH. It preserves the original reserved
wallet opening and first-action history; it never creates simulated proceeds.

Observe requires --observation PATH: private strict JSON with at, primary,
secondary, quote_primary and quote_secondary. The four samples use existing
price-trigger fields; at is their original host observation time, not a renewed
timestamp, and cannot be in the future. Missing observations are never invented.

Account requires --accounting-sha256 HEX from finalized wallet accounting.
For a continuation, also pass --decision-sha256 HEX and
--next-submitter-policy PATH for that exact leg's original recovery policy.
Account delivers already finalized accounting to strategy; use proposal inventory
account first. It does not finalize a transaction or credit the wallet twice.

Prepare and acquire require --inventory PATH --next-authority-policy PATH
--schedule-start-unix N --max-decision-age-seconds N. Acquire additionally needs
--max-acquisition-age-seconds N and acquires only for a ready recorded observation.
Prepare consumes an already committed acquired decision. Both recover any retained
claim before reading original history, fresh strategy or RPC/builder dependencies.
Recovery is recovery-only, including after expiry, never fresh execution authority.
If recovered=true and pending=false, use proposal inventory reserve with the
exact returned claim before accounting; do not reacquire or replace it.
Fresh preparation uses MITHRIL_AGENT_MITHRIL_RPC_URL and two independent evidence
RPCs; acquire also uses the existing protected Jupiter API configuration.

Step runs one unsigned recovery-first cycle. It requires --inventory PATH and
the common original history inputs. For a pending continuation, provide its exact
--next-submitter-policy PATH or include its policy among the historical inputs.
Historical selection requires one distinct policy matching the pending claim's
protected execution envelope; missing or ambiguous matches stop without probing
alternative recovery archives. An explicit next policy never falls back.
Step delivers verified finalized accounting before
reading new prices, and stops on unresolved claims. New opportunities require
--next-authority-policy PATH and both positive age limits. It derives the current
schedule window from that protected policy, never resets it. Market observations
use the pinned Pyth/Kraken sources and MITHRIL_AGENT_SHADOW_RPC_URL; these advisory
prices do not replace the independent execution evidence. No timer is activated.

Instead of --next-authority-policy, step accepts the pair --buy-authority-policy
PATH --sell-authority-policy PATH. Buy means USDC to SOL; sell means SOL to USDC.
Verified strategy state after reconciliation selects one original protected
policy. Only the selected policy is loaded when admission is needed; an unresolved
claim still blocks new work. Policies, spending limits and recovery history are
never rewritten. Recovery selection uses the pending claim, not the next direction.

Retire-acquisition requires --inventory PATH --acquisition PATH and the original
--next-authority-policy PATH for that acquisition. It records an expired,
uncommitted opportunity as retired while retaining its evidence. No claim,
reservation or committed decision may exist for it. It cannot cancel an order,
reset balances or spending limits, or prove a transaction expired on-chain.
The next acquisition requires a fresh observation; no RPC or signing is performed.

Cancel-decision requires --inventory PATH --decision-sha256 HEX. It cancels only
that expired continuation decision when no claim or reservation was created.
Expiry comes from retained original evidence, not a replacement age limit.
It preserves balances, strategy history and spending limits and never cancels a
submitted order. A fresh observation is required before another acquisition.

Show (default) is read-only and projects verified state and pending decision ID.
No operation grants, signs, sends, resets spending caps or enables autonomous
trading. Keep all historical policies and recovery archives for restart replay.`

func runProposalStrategy(ctx context.Context, args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("proposal strategy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	op := flags.String("operation", "show", "strategy operation")
	path := flags.String("strategy", "", "protected strategy journal")
	authorityPath := flags.String("authority-policy", "", "original authority policy")
	recoveryPath := flags.String("submitter-policy", "", "original recovery policy")
	inventoryPath := flags.String("inventory", "", "protected inventory journal")
	claimPath := flags.String("claim", "", "original claim journal")
	acquisitionPath := flags.String("acquisition", "", "original uncommitted acquisition journal")
	paperPolicyPath := flags.String("paper-policy", "", "original paper policy")
	paperJournalPath := flags.String("paper-journal", "", "original paper history")
	boundsPath := flags.String("paper-bounds", "", "original paper bounds")
	observationPath := flags.String("observation", "", "original four-source observation")
	nextAuthorityPath := flags.String("next-authority-policy", "", "next protected authority policy")
	buyAuthorityPath := flags.String("buy-authority-policy", "", "protected USDC-to-SOL authority policy for step")
	sellAuthorityPath := flags.String("sell-authority-policy", "", "protected SOL-to-USDC authority policy for step")
	nextRecoveryPath := flags.String("next-submitter-policy", "", "exact continuation recovery policy")
	accounting := flags.String("accounting-sha256", "", "exact wallet accounting hash")
	decision := flags.String("decision-sha256", "", "exact continuation decision hash")
	schedule := flags.Int64("schedule-start-unix", 0, "original schedule start")
	decisionAge := flags.Uint64("max-decision-age-seconds", 0, "decision age limit")
	acquisitionAge := flags.Uint64("max-acquisition-age-seconds", 0, "acquisition age limit")
	var historicalPaths []string
	flags.Func("historical-submitter-policy", "original later-leg recovery policy", func(value string) error {
		if len(historicalPaths) >= 64 {
			return errors.New("too many historical recovery policy files")
		}
		historicalPaths = append(historicalPaths, value)
		return nil
	})
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, proposalStrategyUsage)
		}
		return err
	}
	allowed := map[string]string{
		"show": "", "initialize": " inventory claim paper-policy paper-journal paper-bounds ",
		"observe": " observation ", "account": " accounting-sha256 decision-sha256 next-submitter-policy ",
		"prepare":            " inventory next-authority-policy schedule-start-unix max-decision-age-seconds ",
		"acquire":            " inventory next-authority-policy schedule-start-unix max-decision-age-seconds max-acquisition-age-seconds ",
		"step":               " inventory next-authority-policy buy-authority-policy sell-authority-policy next-submitter-policy max-decision-age-seconds max-acquisition-age-seconds ",
		"retire-acquisition": " inventory acquisition next-authority-policy ",
		"cancel-decision":    " inventory decision-sha256 ",
	}
	operationFlags, known := allowed[*op]
	if !known || flags.NArg() != 0 || now == nil {
		return errors.New("strategy requires a known operation, no positional arguments and a clock")
	}
	var inappropriate bool
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "operation", "strategy", "authority-policy", "submitter-policy", "historical-submitter-policy":
		default:
			inappropriate = inappropriate || !strings.Contains(operationFlags, " "+f.Name+" ")
		}
	})
	if inappropriate {
		return errors.New("strategy flag is not applicable to this operation")
	}
	if (*buyAuthorityPath == "") != (*sellAuthorityPath == "") ||
		(*buyAuthorityPath != "" && (*nextAuthorityPath != "" || !distinctAbsolutePaths(*buyAuthorityPath, *sellAuthorityPath))) {
		return errors.New("strategy step requires distinct paired buy/sell policy paths or one next policy, not both")
	}
	var next policyauthority.Policy
	if *op == "prepare" || *op == "acquire" {
		if !distinctAbsolutePaths(*inventoryPath, *nextAuthorityPath) {
			return errors.New("strategy admission requires distinct protected inventory and next policy paths")
		}
		if err := readStrictJSON(*nextAuthorityPath, &next); err != nil {
			return errors.New("read next authority policy")
		}
		if err := next.Validate(); err != nil {
			return err
		}
		retained, found, err := policyauthority.RecoverWalletPaperClaim(*inventoryPath, next, now())
		if err != nil {
			return err
		}
		if found {
			return writeStrategyClaim(output, retained)
		}
		if *schedule <= 0 || *decisionAge == 0 || *decisionAge > uint64((1<<63-1)/time.Second) ||
			(*op == "acquire" && (*acquisitionAge == 0 || *acquisitionAge > uint64((1<<63-1)/time.Second))) {
			return errors.New("fresh strategy admission requires a schedule and positive bounded age seconds")
		}
	}
	inputs := []string{*authorityPath, *recoveryPath}
	journals := []string{*path}
	switch *op {
	case "initialize":
		journals = append(journals, *inventoryPath, *claimPath)
		inputs = append(inputs, *paperPolicyPath, *paperJournalPath, *boundsPath)
	case "observe":
		inputs = append(inputs, *observationPath)
	case "account":
		if !validSHA256(*accounting) || (*decision == "") != (*nextRecoveryPath == "") || (*decision != "" && !validSHA256(*decision)) {
			return errors.New("strategy accounting requires exact hashes and a paired continuation recovery policy")
		}
		if *nextRecoveryPath != "" {
			inputs = append(inputs, *nextRecoveryPath)
		}
	case "prepare", "acquire":
		journals = append(journals, *inventoryPath)
		inputs = append(inputs, *nextAuthorityPath)
	case "retire-acquisition":
		journals = append(journals, *inventoryPath, *acquisitionPath)
		inputs = append(inputs, *nextAuthorityPath)
	case "cancel-decision":
		journals = append(journals, *inventoryPath)
		if !validSHA256(*decision) {
			return errors.New("strategy cancellation requires the exact continuation decision hash")
		}
	case "step":
		journals = append(journals, *inventoryPath)
		for _, file := range []string{*nextAuthorityPath, *buyAuthorityPath, *sellAuthorityPath, *nextRecoveryPath} {
			if file != "" {
				inputs = append(inputs, file)
			}
		}
	}
	if !distinctAbsolutePaths(journals...) {
		return errors.New("strategy journals require distinct clean absolute paths")
	}
	inputs = append(inputs, historicalPaths...)
	if !distinctAbsolutePaths(*authorityPath, *recoveryPath) {
		return errors.New("strategy requires distinct original authority and recovery policy paths")
	}
	for _, input := range inputs {
		for _, protected := range journals {
			if !distinctAbsolutePaths(protected, input) {
				return errors.New("strategy requires distinct clean absolute journal and input paths")
			}
		}
	}
	unique := map[string]bool{*recoveryPath: true}
	if *nextRecoveryPath != "" {
		unique[*nextRecoveryPath] = true
	}
	for _, file := range historicalPaths {
		if unique[file] {
			return errors.New("strategy historical recovery policy files must not repeat")
		}
		unique[file] = true
	}
	if len(unique) > 64 {
		return errors.New("strategy supports at most 64 unique recovery policy files")
	}
	var authority policyauthority.Policy
	if err := readStrictJSON(*authorityPath, &authority); err != nil {
		return errors.New("read original authority policy")
	}
	if err := authority.Validate(); err != nil {
		return err
	}
	loadRecovery := func(file string) (submitter.Policy, error) {
		var value submitter.Policy
		if err := readStrictJSON(file, &value); err != nil {
			return value, errors.New("read original recovery policy")
		}
		return value, submitter.ValidateJupiterPolicy(value)
	}
	originalRecovery, err := loadRecovery(*recoveryPath)
	if err != nil {
		return err
	}
	var historical []submitter.Policy
	for _, file := range historicalPaths {
		value, err := loadRecovery(file)
		if err != nil {
			return err
		}
		historical = append(historical, value)
	}
	switch *op {
	case "cancel-decision":
		status, err := policyauthority.CancelStrategyDecision(*path, *inventoryPath,
			authority, originalRecovery, *decision, now(), historical...)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(struct {
			Status    string `json:"status"`
			Head      string `json:"current_head_sha256"`
			Decision  string `json:"canceled_decision_sha256"`
			CanSign   bool   `json:"can_sign"`
			CanSubmit bool   `json:"can_submit"`
		}{Status: "strategy_decision_canceled_not_authorized", Head: status.HeadSHA256, Decision: *decision})
	case "retire-acquisition":
		if err := readStrictJSON(*nextAuthorityPath, &next); err != nil {
			return errors.New("read original acquisition authority policy")
		}
		status, err := policyauthority.RetireStrategyAcquisition(*path, *inventoryPath, *acquisitionPath,
			authority, originalRecovery, next, now(), historical...)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(struct {
			Status    string `json:"status"`
			Head      string `json:"current_head_sha256"`
			CanSign   bool   `json:"can_sign"`
			CanSubmit bool   `json:"can_submit"`
		}{Status: "strategy_acquisition_retired_not_authorized", Head: status.HeadSHA256})
	case "step":
		var current submitter.Policy
		if *nextRecoveryPath != "" {
			current, err = loadRecovery(*nextRecoveryPath)
			if err != nil {
				return err
			}
		}
		return runProposalStrategyStep(ctx, output, *path, *inventoryPath, authority, originalRecovery, current,
			*nextAuthorityPath, *buyAuthorityPath, *sellAuthorityPath, *decisionAge, *acquisitionAge, now, historical...)
	case "initialize":
		paper, err := loadShadowPolicy(*paperPolicyPath)
		if err != nil {
			return err
		}
		ticks, err := readShadowTicks(*paperJournalPath, paper)
		if err != nil {
			return err
		}
		var bounds proposalcheck.PaperIntentBounds
		if err := readStrictJSON(*boundsPath, &bounds); err != nil {
			return errors.New("read original paper bounds")
		}
		_, err = policyauthority.InitializeStrategyJournal(*path, *inventoryPath, *claimPath, authority, originalRecovery, paper, ticks, bounds, now(), historical...)
		if err != nil {
			return err
		}
	case "observe":
		var observation struct {
			At             time.Time           `json:"at"`
			Primary        pricetrigger.Sample `json:"primary"`
			Secondary      pricetrigger.Sample `json:"secondary"`
			QuotePrimary   pricetrigger.Sample `json:"quote_primary"`
			QuoteSecondary pricetrigger.Sample `json:"quote_secondary"`
		}
		if err := readStrictJSON(*observationPath, &observation); err != nil {
			return errors.New("read original strategy observation")
		}
		if observation.At.IsZero() || observation.At.After(now()) {
			return errors.New("strategy observation time is missing or future")
		}
		value, err := policyauthority.ObserveStrategyJournal(*path, authority, originalRecovery, observation.At,
			observation.Primary, observation.Secondary, observation.QuotePrimary, observation.QuoteSecondary, historical...)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(struct {
			Status    string                   `json:"status"`
			Decision  shadow.AccountedDecision `json:"decision"`
			CanSign   bool                     `json:"can_sign"`
			CanSubmit bool                     `json:"can_submit"`
		}{Status: "strategy_observation_not_authorized", Decision: value})
	case "account":
		if *decision == "" {
			_, err = policyauthority.ApplyStrategyJournalOutcome(*path, authority, originalRecovery, *accounting, now(), historical...)
		} else {
			leg, loadErr := loadRecovery(*nextRecoveryPath)
			if loadErr != nil {
				return loadErr
			}
			_, err = policyauthority.ApplyStrategyContinuationOutcome(*path, authority, originalRecovery, leg, *decision, *accounting, now(), historical...)
			historical = append(historical, leg)
		}
		if err != nil {
			return err
		}
	case "prepare", "acquire":
		providers, err := openUnboundRPCProviders(os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL"), os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL"), os.Getenv("MITHRIL_AGENT_SECONDARY_RPC_URL"))
		if err != nil {
			return errors.New("fresh strategy RPC configuration is invalid")
		}
		lifecycle, err := txflow.New(providers.mithril, providers.primary, providers.secondary)
		if err != nil {
			return err
		}
		var claim policyauthority.WalletPaperClaim
		if *op == "acquire" {
			claim, err = policyauthority.AcquireStrategyWalletClaim(ctx, *inventoryPath, *path, authority, originalRecovery, next,
				*schedule, now(), time.Duration(*decisionAge)*time.Second, time.Duration(*acquisitionAge)*time.Second,
				proposalStrategyBuilder{}, lifecycle, providers.primary, providers.secondary, lifecycle, historical...)
		} else {
			claim, err = policyauthority.PrepareStrategyWalletClaim(ctx, *inventoryPath, *path, authority, originalRecovery, next,
				*schedule, now(), time.Duration(*decisionAge)*time.Second, lifecycle, providers.primary, providers.secondary, lifecycle, historical...)
		}
		if err != nil {
			return err
		}
		return writeStrategyClaim(output, claim)
	}
	status, err := policyauthority.ReadStrategyJournalStatus(*path, authority, originalRecovery, now(), historical...)
	if err != nil {
		return err
	}
	quote, _ := status.State.PendingQuote()
	return json.NewEncoder(output).Encode(struct {
		Status          string        `json:"status"`
		Head            string        `json:"head_sha256"`
		PendingDecision string        `json:"pending_decision_sha256,omitempty"`
		Pending         bool          `json:"pending"`
		NextSell        bool          `json:"next_sell"`
		RiskHalted      bool          `json:"risk_halted"`
		Ledger          shadow.Ledger `json:"ledger"`
		Quote           shadow.Quote  `json:"pending_quote"`
		CanSign         bool          `json:"can_sign"`
		CanSubmit       bool          `json:"can_submit"`
	}{Status: "strategy_verified_not_authorized", Head: status.HeadSHA256, PendingDecision: status.PendingDecisionSHA256,
		Pending: status.State.Pending(), NextSell: status.State.NextSell(), RiskHalted: status.State.RiskHalted(), Ledger: status.State.Ledger(), Quote: quote})
}

// Retained acquisitions and committed decisions must not depend on unused
// builder configuration. Construct the existing client only for a fresh build.
type proposalStrategyBuilder struct{}

func (proposalStrategyBuilder) Build(ctx context.Context, request jupiterquote.Request) (jupiterquote.BuildResult, error) {
	client, err := jupiterquote.New(os.Getenv(jupiterAPIKeyEnvironment))
	if err != nil {
		return jupiterquote.BuildResult{}, err
	}
	return client.Build(ctx, request)
}

func writeStrategyClaim(output io.Writer, claim policyauthority.WalletPaperClaim) error {
	return json.NewEncoder(output).Encode(struct {
		Status    string `json:"status"`
		ClaimPath string `json:"claim_path"`
		ActionID  string `json:"action_id"`
		Head      string `json:"head_sha256"`
		Recovered bool   `json:"recovered"`
		Pending   bool   `json:"pending"`
		CanSign   bool   `json:"can_sign"`
		CanSubmit bool   `json:"can_submit"`
	}{Status: "unsigned_claim_not_authorized", ClaimPath: claim.ClaimPath, ActionID: claim.Request.ActionID,
		Head: claim.Inventory.HeadSHA256, Recovered: claim.Recovered, Pending: claim.Inventory.PendingSHA256 != ""})
}
