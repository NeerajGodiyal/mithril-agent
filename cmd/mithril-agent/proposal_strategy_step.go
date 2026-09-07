package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func runProposalStrategyStep(ctx context.Context, output io.Writer, path, walletPath string,
	authority policyauthority.Policy, originalRecovery, currentRecovery submitter.Policy,
	nextPolicyPath, buyPolicyPath, sellPolicyPath string, decisionAge, acquisitionAge uint64, now func() time.Time,
	historical ...submitter.Policy,
) error {
	reconciled, err := policyauthority.ReconcileStrategyJournal(path, walletPath, authority, originalRecovery, currentRecovery, now(), historical...)
	if err != nil {
		return err
	}
	if currentRecovery != (submitter.Policy{}) {
		historical = append(append([]submitter.Policy(nil), historical...), currentRecovery)
	}
	if buyPolicyPath != "" {
		nextPolicyPath = buyPolicyPath
		if reconciled.Strategy.State.NextSell() {
			nextPolicyPath = sellPolicyPath
		}
	}
	prepare := reconciled.BlockedReason == "claim_not_prepared"
	resumeAcquisition := reconciled.BlockedReason == "acquisition_pending"
	if reconciled.BlockedReason != "" && ((!prepare && !resumeAcquisition) || nextPolicyPath == "") {
		return writeStrategyStep(output, reconciled, nil, nil)
	}
	if !prepare && !resumeAcquisition {
		if reconciled.Strategy.State.Pending() {
			return errors.New("strategy step cannot observe while an order is pending")
		}
		if reconciled.Strategy.State.RiskHalted() {
			reconciled.BlockedReason = "risk_halted"
			return writeStrategyStep(output, reconciled, nil, nil)
		}
		policy := reconciled.Strategy.State.Ledger().Policy
		readers, err := strategyPriceReaders(policy, now)
		if err != nil {
			return err
		}
		primary, secondary, err := shadow.ReadPricePair(ctx, readers[0], readers[1], policy.Trigger.Feed)
		if err != nil {
			return errors.New("strategy market observations unavailable")
		}
		quotePrimary, quoteSecondary, err := shadow.ReadPricePair(ctx, readers[2], readers[3], policy.QuotePeg.Feed)
		if err != nil {
			return errors.New("strategy quote-currency observations unavailable")
		}
		// Validate all original sample timestamps against one post-read host time.
		observedAt := now()
		decision, err := policyauthority.ObserveStrategyJournal(path, authority, originalRecovery, observedAt,
			primary, secondary, quotePrimary, quoteSecondary, historical...)
		if err != nil {
			return err
		}
		if !decision.ReadyForQuote {
			reconciled.Strategy, err = policyauthority.ReadStrategyJournalStatus(path, authority, originalRecovery, now(), historical...)
			if err != nil {
				return err
			}
			return writeStrategyStep(output, reconciled, &decision, &observedAt)
		}
	}
	if nextPolicyPath == "" || decisionAge == 0 || decisionAge > uint64((1<<63-1)/time.Second) ||
		(!prepare && (acquisitionAge == 0 || acquisitionAge > uint64((1<<63-1)/time.Second))) {
		return errors.New("strategy opportunity requires a next authority policy and positive bounded age seconds")
	}
	var next policyauthority.Policy
	if err := readStrictJSON(nextPolicyPath, &next); err != nil {
		return errors.New("read next authority policy")
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if buyPolicyPath != "" && (next.TransactionPolicy.Jupiter == nil ||
		next.TransactionPolicy.Jupiter.NativeInput() != reconciled.Strategy.State.NextSell()) {
		return errors.New("selected strategy authority policy differs from the verified buy/sell direction")
	}
	schedule, err := activeScheduleWindowStart(next.TransactionPolicy, now())
	if err != nil {
		return err
	}
	providers, err := openUnboundRPCProviders(os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL"), os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL"), os.Getenv("MITHRIL_AGENT_SECONDARY_RPC_URL"))
	if err != nil {
		return errors.New("fresh strategy RPC configuration is invalid")
	}
	lifecycle, err := txflow.New(providers.mithril, providers.primary, providers.secondary)
	if err != nil {
		return err
	}
	var claim policyauthority.WalletPaperClaim
	if prepare {
		claim, err = policyauthority.PrepareStrategyWalletClaim(ctx, walletPath, path, authority, originalRecovery, next,
			schedule, now(), time.Duration(decisionAge)*time.Second, lifecycle, providers.primary, providers.secondary, lifecycle, historical...)
	} else {
		claim, err = policyauthority.AcquireStrategyWalletClaim(ctx, walletPath, path, authority, originalRecovery, next,
			schedule, now(), time.Duration(decisionAge)*time.Second, time.Duration(acquisitionAge)*time.Second,
			proposalStrategyBuilder{}, lifecycle, providers.primary, providers.secondary, lifecycle, historical...)
	}
	if err != nil {
		return err
	}
	return writeStrategyClaim(output, claim)
}

func strategyPriceReaders(policy shadow.Policy, now func() time.Time) ([4]shadow.PriceReader, error) {
	var readers [4]shadow.PriceReader
	legacySOL := policy.Version == shadow.LegacyVersion && policy.Market == ""
	if now == nil || policy.Market != shadow.MarketSOLUSDC && !legacySOL {
		return readers, errors.New("strategy observations require a clock and supported SOL/USDC policy")
	}
	if err := validatePaperPolicySources(policy); err != nil {
		return readers, err
	}
	endpoint := os.Getenv(shadowEndpointEnvironment)
	if err := validateShadowEndpoint(endpoint); err != nil {
		return readers, err
	}
	accountReader := publicAccountReader(endpoint)
	primary, err := pricesource.NewPythPush(accountReader, now)
	if err != nil {
		return readers, err
	}
	quotePrimary, err := pricesource.NewPythPushUSDC(accountReader, now)
	if err != nil {
		return readers, err
	}
	if paperUsesKrakenTicker(policy) {
		_, secondary, err := newPaperTickerReaders(policy)
		if err != nil {
			return readers, err
		}
		return [4]shadow.PriceReader{primary, secondary[0], quotePrimary, secondary[1]}, nil
	}
	return [4]shadow.PriceReader{primary, pricesource.NewKrakenSOL(nil), quotePrimary, pricesource.NewKraken(nil)}, nil
}

func writeStrategyStep(output io.Writer, result policyauthority.StrategyReconciliation, decision *shadow.AccountedDecision, observedAt *time.Time) error {
	acquisition := ""
	if result.BlockedReason == "acquisition_pending" {
		acquisition = result.AcquisitionPath
	}
	return json.NewEncoder(output).Encode(struct {
		Status      string                    `json:"status"`
		Head        string                    `json:"current_head_sha256"`
		Accounting  string                    `json:"accounting_sha256,omitempty"`
		Blocked     string                    `json:"blocked_reason,omitempty"`
		Acquisition string                    `json:"acquisition_path,omitempty"`
		Pending     bool                      `json:"strategy_pending"`
		Decision    *shadow.AccountedDecision `json:"observation_decision,omitempty"`
		ObservedAt  *time.Time                `json:"observation_at,omitempty"`
		CanSign     bool                      `json:"can_sign"`
		CanSubmit   bool                      `json:"can_submit"`
	}{Status: "strategy_step_not_authorized", Head: result.Strategy.HeadSHA256, Accounting: result.AccountingSHA256,
		Blocked: result.BlockedReason, Acquisition: acquisition, Pending: result.Strategy.State.Pending(), Decision: decision, ObservedAt: observedAt})
}
