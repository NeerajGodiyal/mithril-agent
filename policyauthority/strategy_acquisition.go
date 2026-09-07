package policyauthority

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const strategyAcquisitionEvent = "wallet.strategy-acquisition-intent-v1"

type strategyAcquisitionIntent struct {
	StrategyPath        string               `json:"strategy_path"`
	StrategyHeadSHA256  string               `json:"strategy_head_sha256"`
	WalletHeadSHA256    string               `json:"wallet_head_sha256"`
	PolicySHA256        string               `json:"policy_sha256"`
	Request             jupiterquote.Request `json:"request"`
	MaxDecisionAgeNS    int64                `json:"max_decision_age_ns,string"`
	MaxAcquisitionAgeNS int64                `json:"max_acquisition_age_ns,string"`
}

// AcquireStrategyWalletClaim advances a ready persisted strategy through quote
// acquisition, decision persistence and unsigned wallet admission. Existing
// claims recover first; a committed decision resumes without acquiring again.
// Each acquisition generation retains its original evidence, including expiry.
// It neither observes prices nor authorizes, signs or submits transactions.
func AcquireStrategyWalletClaim(ctx context.Context, walletPath, strategyPath string,
	originalAuthority Policy, originalRecovery submitter.Policy, nextPolicy Policy,
	scheduleWindowStartUnix int64, now time.Time, maxDecisionAge, maxAcquisitionAge time.Duration,
	builder proposalcheck.Builder, evidence proposalcheck.NativeReserveEvidence,
	primary, secondary proposalcheck.FinalizedSlotReader, lifecycle *txflow.Lifecycle,
	historicalRecoveryPolicies ...submitter.Policy,
) (result WalletPaperClaim, err error) {
	started := time.Now()
	if recovered, found, err := RecoverWalletPaperClaim(walletPath, nextPolicy, now); err != nil || found {
		return recovered, err
	}
	if lifecycle == nil || evidence == nil || primary == nil || secondary == nil {
		return WalletPaperClaim{}, errors.New("strategy acquisition requires independent chain evidence")
	}
	records, err := journal.ReadRecords(strategyPath)
	if err != nil {
		return WalletPaperClaim{}, err
	}
	state, ready, err := replayStrategyJournal(records, originalAuthority, originalRecovery,
		now.Add(time.Since(started)), strategyOutcomeReader(originalAuthority, originalRecovery), historicalRecoveryPolicies...)
	if err != nil {
		return WalletPaperClaim{}, err
	}
	var seed strategySeed
	if err := decodeStrategyPayload(records[0].Payload, &seed); err != nil {
		return WalletPaperClaim{}, err
	}
	if seed.WalletPath != walletPath {
		return WalletPaperClaim{}, errors.New("strategy acquisition wallet differs from original history")
	}
	if state.Pending() {
		return PrepareStrategyWalletClaim(ctx, walletPath, strategyPath, originalAuthority, originalRecovery,
			nextPolicy, scheduleWindowStartUnix, now.Add(time.Since(started)), maxDecisionAge,
			evidence, primary, secondary, lifecycle, historicalRecoveryPolicies...)
	}
	last := records[len(records)-1]
	checkedAt := now.Add(time.Since(started))
	if !ready.ReadyForQuote || last.Type != strategyObservationEvent || seed.Policy.Adaptive == nil ||
		maxDecisionAge <= 0 || maxDecisionAge > time.Duration(seed.Policy.Adaptive.MaxObservationGapSeconds)*time.Second ||
		maxAcquisitionAge <= 0 || last.At.After(checkedAt) || last.At.Before(checkedAt.Add(-maxDecisionAge)) {
		return WalletPaperClaim{}, errors.New("strategy acquisition requires a recent ready observation")
	}
	wallet, err := ReadWalletInventory(walletPath, nextPolicy)
	if err != nil {
		return WalletPaperClaim{}, err
	}
	binding, _, _, err := walletInventoryBinding(originalAuthority)
	if err != nil || binding != wallet.BindingSHA256 || wallet.PendingSHA256 != "" ||
		state.Ledger().BaseUnits != wallet.NativeLamports || state.Ledger().QuoteUnits != wallet.TokenUnits {
		return WalletPaperClaim{}, errors.New("strategy acquisition differs from available accounted wallet")
	}
	window := int64(nextPolicy.TransactionPolicy.ScheduleWindowSeconds)
	anchor := nextPolicy.TransactionPolicy.ScheduleAnchorUnix
	if scheduleWindowStartUnix < anchor || (scheduleWindowStartUnix-anchor)%window != 0 ||
		scheduleWindowStartUnix+window <= scheduleWindowStartUnix {
		return WalletPaperClaim{}, errors.New("Jupiter signing schedule window is outside policy")
	}
	if err := signer.ValidateScheduleWindowAt(signer.Request{ScheduleWindowStartUnix: scheduleWindowStartUnix,
		ScheduleWindowEndUnix: scheduleWindowStartUnix + window}, checkedAt); err != nil {
		return WalletPaperClaim{}, err
	}
	action, err := jupiterswap.ComputeActionID(nextPolicy.TransactionPolicy.ProfileFingerprint, scheduleWindowStartUnix)
	if err != nil {
		return WalletPaperClaim{}, err
	}
	walletRecords, err := journal.ReadRecords(walletPath)
	if err != nil {
		return WalletPaperClaim{}, err
	}
	for _, record := range walletRecords[1:] {
		if record.ActionID == action {
			return WalletPaperClaim{}, errors.New("wallet action was already claimed; no new acquisition created")
		}
	}
	route := *nextPolicy.TransactionPolicy.Jupiter
	if route.NativeInput() != ready.Sell || route.Owner != seed.Policy.Observe ||
		route.MaxFeeLamports > seed.Policy.FeeLamports || ready.InputAmount > route.MaxInputAmount {
		return WalletPaperClaim{}, errors.New("strategy acquisition differs from protected direction or costs")
	}
	request := jupiterquote.Request{Taker: route.Owner, InputMint: route.InputMint, OutputMint: route.OutputMint,
		InputAmount: ready.InputAmount, SlippageBPS: min(seed.Policy.SlippageBPS, route.MaxSlippageBPS)}
	if route.NativeInput() {
		request.DestinationTokenAccount, err = orcaswap.AssociatedTokenAddress(route.Owner, route.OutputMint)
		if err != nil {
			return WalletPaperClaim{}, err
		}
	}
	acquisitionPath, err := strategyAcquisitionPath(walletPath, wallet.HeadSHA256, records)
	if err != nil {
		return WalletPaperClaim{}, err
	}
	policySHA, _, err := paperRequestHashes(nextPolicy, signer.Request{})
	if err != nil {
		return WalletPaperClaim{}, err
	}
	intent := strategyAcquisitionIntent{StrategyPath: strategyPath, StrategyHeadSHA256: last.Hash,
		WalletHeadSHA256: wallet.HeadSHA256, PolicySHA256: policySHA, Request: request,
		MaxDecisionAgeNS: int64(maxDecisionAge), MaxAcquisitionAgeNS: int64(maxAcquisitionAge)}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return WalletPaperClaim{}, err
	}
	intents, err := journal.OpenStrict(acquisitionPath + ".intent.jsonl")
	if err != nil {
		return WalletPaperClaim{}, err
	}
	intentClosed := false
	defer func() {
		if !intentClosed {
			if closeErr := intents.Close(); closeErr != nil {
				result = WalletPaperClaim{}
				err = errors.Join(err, closeErr)
			}
		}
	}()
	var acquiredRecords []journal.Record
	if _, err := os.Lstat(acquisitionPath); err == nil {
		acquiredRecords, err = journal.ReadRecords(acquisitionPath)
		if err != nil {
			return WalletPaperClaim{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return WalletPaperClaim{}, err
	}
	if len(acquiredRecords) == 0 && builder == nil {
		return WalletPaperClaim{}, errors.New("fresh strategy acquisition requires a builder")
	}
	// Persist the original opportunity before any builder call. This private
	// lock serializes acquisition retries; wallet/strategy checks still run at
	// commitment, since market observations may advance during provider work.
	if prior := intents.Records(); len(prior) != 0 {
		if len(prior) != 1 || prior[0].Type != strategyAcquisitionEvent || prior[0].ActionID != last.Hash ||
			!bytes.Equal(prior[0].Payload, encoded) || prior[0].At.Before(last.At) || prior[0].At.After(now.Add(time.Since(started))) {
			return WalletPaperClaim{}, errors.New("strategy acquisition differs from its original opportunity")
		}
	} else {
		if len(acquiredRecords) != 0 {
			return WalletPaperClaim{}, errors.New("retained strategy acquisition lacks its original opportunity")
		}
		if _, err := intents.Append(now.Add(time.Since(started)), strategyAcquisitionEvent, last.Hash, intent); err != nil {
			return WalletPaperClaim{}, err
		}
	}
	if len(acquiredRecords) == 0 {
		providers := nextPolicy.JupiterProviders
		if _, err := proposalcheck.CheckAndRecordAcquisition(ctx, acquisitionPath, maxAcquisitionAge,
			builder, evidence, primary, secondary, providers.PrimaryTrustDomain, providers.SecondaryTrustDomain,
			providers.ArchiveProbeSignature, route, request); err != nil {
			return WalletPaperClaim{}, err
		}
	}
	completed := now.Add(time.Since(started))
	acquired, err := proposalcheck.ReadAcquisition(acquisitionPath, completed, maxAcquisitionAge)
	if err != nil {
		return WalletPaperClaim{}, err
	}
	if acquired.Candidate.Request != request || acquired.ReceivedAt.Before(intents.Records()[0].At) || last.At.Before(completed.Add(-maxDecisionAge)) {
		return WalletPaperClaim{}, errors.New("strategy acquisition differs from original opportunity or expired during preparation")
	}
	// Commit independently reads the immutable intent. Release its writer first;
	// exact strategy-head and retired-receipt checks still reject intervening work.
	closeErr := intents.Close()
	intentClosed = true
	if closeErr != nil {
		return WalletPaperClaim{}, closeErr
	}
	if _, err := commitStrategyJournalDecision(strategyPath, originalAuthority, originalRecovery, nextPolicy,
		acquisitionPath, maxAcquisitionAge, completed, last.Hash, historicalRecoveryPolicies...); err != nil {
		return WalletPaperClaim{}, err
	}
	return PrepareStrategyWalletClaim(ctx, walletPath, strategyPath, originalAuthority, originalRecovery, nextPolicy,
		scheduleWindowStartUnix, now.Add(time.Since(started)), maxDecisionAge, evidence, primary, secondary,
		lifecycle, historicalRecoveryPolicies...)
}
