package policyauthority

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

// AcquireWalletPaperClaim connects a frozen first decision to builder-backed
// acquisition and unsigned inventory admission. It never creates strategy
// history, resets adaptive risk state, signs or sends. Pending requests recover
// before fresh inputs are consulted; an existing acquisition is never renewed.
func AcquireWalletPaperClaim(ctx context.Context, path string, policy Policy,
	paperPolicy shadow.Policy, ticks []shadow.Tick, bounds proposalcheck.PaperIntentBounds,
	request jupiterquote.Request, scheduleWindowStartUnix int64, now time.Time,
	maxDecisionAge, maxAcquisitionAge time.Duration, builder proposalcheck.Builder,
	evidence proposalcheck.NativeReserveEvidence, primary, secondary proposalcheck.FinalizedSlotReader,
	lifecycle *txflow.Lifecycle,
) (WalletPaperClaim, error) {
	started := time.Now()
	actionID, actionErr := jupiterswap.ComputeActionID(policy.TransactionPolicy.ProfileFingerprint, scheduleWindowStartUnix)
	return prepareWalletPaperClaim(path, policy, now, func(claimPath string, value WalletInventory) (signer.Request, error) {
		if actionErr != nil {
			return signer.Request{}, actionErr
		}
		// Match RequestFromJupiterRecheck's policy window before persisting an
		// acquisition. ClaimPaperRequest still rechecks at preparation completion.
		window := int64(policy.TransactionPolicy.ScheduleWindowSeconds)
		if scheduleWindowStartUnix < policy.TransactionPolicy.ScheduleAnchorUnix ||
			(scheduleWindowStartUnix-policy.TransactionPolicy.ScheduleAnchorUnix)%window != 0 ||
			scheduleWindowStartUnix+window <= scheduleWindowStartUnix {
			return signer.Request{}, errors.New("Jupiter signing schedule window is outside policy")
		}
		if err := signer.ValidateScheduleWindowAt(signer.Request{ScheduleWindowStartUnix: scheduleWindowStartUnix,
			ScheduleWindowEndUnix: scheduleWindowStartUnix + window}, now.Add(time.Since(started))); err != nil {
			return signer.Request{}, err
		}
		route := *policy.TransactionPolicy.Jupiter
		market := paperPolicy.Market
		if market == "" {
			market = shadow.MarketSOLUSDC
		}
		paperRoute := shadow.MainnetMarketQuoteRoute(market, paperPolicy.IsSell())
		if paperPolicy.Cluster != shadow.Mainnet || paperPolicy.MarketEvidenceClass == shadow.MarketEvidenceDevelopmentProvisional ||
			paperPolicy.Observe != route.Owner || paperPolicy.QuoteRoute != paperRoute || paperRoute.Provider != shadow.QuoteJupiter ||
			paperRoute.InputMint != route.InputMint || paperRoute.OutputMint != route.OutputMint {
			return signer.Request{}, errors.New("wallet acquisition paper route differs from protected Jupiter policy")
		}
		if bounds.NativeBudgetLamports > value.NativeLamports ||
			(route.NativeInput() && request.InputAmount > value.NativeLamports) ||
			(!route.NativeInput() && request.InputAmount > value.TokenUnits) {
			return signer.Request{}, errors.New("paper claim sizing exceeds accounted wallet balances")
		}
		destination := ""
		if route.NativeInput() {
			var err error
			destination, err = orcaswap.AssociatedTokenAddress(route.Owner, route.OutputMint)
			if err != nil {
				return signer.Request{}, err
			}
		}
		if request.Taker != route.Owner || request.InputMint != route.InputMint || request.OutputMint != route.OutputMint ||
			request.DestinationTokenAccount != destination || request.InputAmount == 0 || request.InputAmount > bounds.MaxInputAmount ||
			request.SlippageBPS == 0 || request.SlippageBPS > route.MaxSlippageBPS || request.SlippageBPS > paperPolicy.SlippageBPS ||
			route.MaxFeeLamports > paperPolicy.FeeLamports {
			return signer.Request{}, errors.New("wallet acquisition request differs from protected route or bounds")
		}
		if bounds.ReserveLamports == 0 || bounds.NativeBudgetLamports < bounds.ReserveLamports {
			return signer.Request{}, errors.New("paper intent input or reserve exceeds budget")
		}
		// Match CheckPaperIntent's conservative cost floor before acquisition;
		// sequential subtraction also rejects overflowing caller-supplied limits.
		remaining := bounds.NativeBudgetLamports - bounds.ReserveLamports
		for _, debit := range []uint64{route.MaxFeeLamports, route.MaxTokenAccountRentLamports, route.MaxTokenAccountRentLamports} {
			if debit > remaining {
				return signer.Request{}, errors.New("paper intent native costs exceed budget")
			}
			remaining -= debit
		}
		if route.NativeInput() && request.InputAmount > remaining {
			return signer.Request{}, errors.New("paper intent native input exceeds budget")
		}
		fingerprint, err := paperPolicy.Fingerprint()
		if err != nil || fingerprint != bounds.PolicySHA256 {
			return signer.Request{}, errors.New("wallet acquisition paper policy differs from frozen bounds")
		}
		digest, err := proposalcheck.PaperEvidenceSHA256(ticks)
		if err != nil || digest != bounds.EvidenceSHA256 || len(ticks) == 0 {
			return signer.Request{}, errors.New("wallet acquisition history differs from frozen bounds")
		}
		if _, err := shadow.Replay(paperPolicy, ticks); err != nil {
			return signer.Request{}, err
		}
		last := ticks[len(ticks)-1]
		checkedAt := now.Add(time.Since(started))
		if maxDecisionAge <= 0 || last.Event != shadow.EventSignal || !last.Triggered || last.Deferred || last.DecisionQuote == nil ||
			last.DecisionQuote.InputAmount != request.InputAmount || last.At.After(checkedAt) || last.At.Before(checkedAt.Add(-maxDecisionAge)) ||
			last.DecisionQuote.ReceivedAt.IsZero() || last.DecisionQuote.ReceivedAt.After(checkedAt) || last.DecisionQuote.ReceivedAt.Before(checkedAt.Add(-maxDecisionAge)) {
			return signer.Request{}, errors.New("wallet acquisition requires a recent exact first decision")
		}
		for _, tick := range ticks[:len(ticks)-1] {
			if tick.Event == shadow.EventSignal || tick.Fill != nil || tick.DecisionMissed {
				return signer.Request{}, errors.New("wallet acquisition cannot reuse simulated proceeds or earlier signals")
			}
		}
		acquisitionPath := claimPath + ".acquisition.jsonl"
		var records []journal.Record
		if _, err := os.Lstat(acquisitionPath); err == nil {
			records, err = journal.ReadRecords(acquisitionPath)
			if err != nil {
				return signer.Request{}, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return signer.Request{}, err
		}
		if len(records) == 0 {
			providers := policy.JupiterProviders
			_, err = proposalcheck.CheckAndRecordAcquisition(ctx, acquisitionPath, maxAcquisitionAge, builder, evidence, primary, secondary,
				providers.PrimaryTrustDomain, providers.SecondaryTrustDomain, providers.ArchiveProbeSignature, route, request)
			if err != nil {
				return signer.Request{}, err
			}
		}
		// Fresh admission and recovery consume the same retained candidate bytes.
		candidate, err := proposalcheck.ReadAcquiredCandidate(acquisitionPath, now.Add(time.Since(started)), maxAcquisitionAge)
		if err != nil {
			return signer.Request{}, err
		}
		if candidate.Request != request {
			return signer.Request{}, errors.New("retained acquisition differs from requested trade")
		}
		return ClaimPaperRequest(ctx, claimPath, policy, paperPolicy, ticks, bounds, candidate,
			scheduleWindowStartUnix, now.Add(time.Since(started)), maxDecisionAge, acquisitionPath, maxAcquisitionAge, evidence, primary, secondary)
	}, func(value WalletInventory) (txflow.WalletObservation, error) {
		return observeWalletReservation(ctx, lifecycle, value)
	}, actionID)
}
