package policyauthority

import (
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

// WalletStrategyOutcome projects one durably accounted transaction, not a paper
// settlement. KnownAt is when accounting was recorded, never a backdated trading
// observation. Consumers must bind it to their original pending decision and
// retain the accounting identity across restarts before advancing a strategy.
type WalletStrategyOutcome struct {
	WalletBindingSHA256 string                  `json:"wallet_binding_sha256"`
	PreviousHeadSHA256  string                  `json:"previous_head_sha256"`
	ClaimSHA256         string                  `json:"claim_sha256"`
	ActionID            string                  `json:"action_id"`
	RequestSHA256       string                  `json:"request_sha256"`
	TransactionSHA256   string                  `json:"transaction_sha256"`
	KnownAt             time.Time               `json:"known_at"`
	TerminalAt          time.Time               `json:"terminal_at"`
	Amounts             shadow.AccountedOutcome `json:"amounts"`
}

// ReadWalletStrategyOutcome verifies an exact accounting record against its
// reserved claim and full retained finalized recovery evidence. It performs no
// RPC or journal mutation and grants no authority. Recovery uses its file lock.
// Historical outcomes remain readable after later wallet actions; a reservation
// or an unaccounted terminal alone is not a strategy outcome.
func ReadWalletStrategyOutcome(path, claimPath, accountingSHA256 string, policy Policy,
	request signer.Request, recoveryPolicy submitter.Policy, now time.Time,
) (WalletStrategyOutcome, error) {
	return readWalletStrategyOutcome(path, claimPath, accountingSHA256, policy, request, now,
		func() (submitter.JupiterFinalizedWalletEvidence, error) {
			return submitter.ReadJupiterFinalizedWalletEvidence(recoveryPolicy, request)
		})
}

func readWalletStrategyOutcome(path, claimPath, accountingSHA256 string, policy Policy,
	request signer.Request, now time.Time, readBalances func() (submitter.JupiterFinalizedWalletEvidence, error),
) (WalletStrategyOutcome, error) {
	if !validHexDigest(accountingSHA256) || now.IsZero() {
		return WalletStrategyOutcome{}, errors.New("strategy outcome requires an accounting identity and observation time")
	}
	binding, owner, mint, err := walletInventoryBinding(policy)
	if err != nil {
		return WalletStrategyOutcome{}, err
	}
	records, err := journal.ReadRecords(path)
	if err != nil {
		return WalletStrategyOutcome{}, err
	}
	if _, err := openingWalletInventory(records, policy, binding, owner, mint); err != nil {
		return WalletStrategyOutcome{}, err
	}
	for index, record := range records {
		if record.Hash != accountingSHA256 {
			continue
		}
		if record.Type != walletAccountingEvent || index == 0 || records[index-1].Type != walletReservationEvent ||
			record.ActionID != request.ActionID || record.At.After(now) {
			return WalletStrategyOutcome{}, errors.New("strategy outcome is not a known accounted action")
		}
		var receipt walletAccounting
		var reservation WalletReservation
		if err := strictjson.Decode(record.Payload, &receipt); err != nil {
			return WalletStrategyOutcome{}, err
		}
		if err := strictjson.Decode(records[index-1].Payload, &reservation); err != nil {
			return WalletStrategyOutcome{}, err
		}
		var balances submitter.JupiterFinalizedWalletEvidence
		delta, err := readPaperSwapAccounting(claimPath, policy, request, func() (submitter.JupiterFinalizedEvidence, error) {
			var err error
			balances, err = readBalances()
			return balances.Finalized, err
		})
		if err != nil {
			return WalletStrategyOutcome{}, err
		}
		if balances != receipt.Balances || delta.ClaimSHA256 != reservation.ClaimSHA256 ||
			delta.TerminalSHA256 != receipt.TerminalSHA256 || delta.TerminalAt != receipt.TerminalAt {
			return WalletStrategyOutcome{}, errors.New("strategy outcome differs from reserved claim or finalized recovery")
		}
		f := balances.Finalized
		return WalletStrategyOutcome{WalletBindingSHA256: binding, PreviousHeadSHA256: reservation.PreviousHeadSHA256,
			ClaimSHA256: delta.ClaimSHA256, ActionID: f.ActionID, RequestSHA256: f.RequestSHA256,
			TransactionSHA256: f.TransactionSHA256, KnownAt: record.At, TerminalAt: delta.TerminalAt,
			Amounts: shadow.AccountedOutcome{AccountingSHA256: record.Hash, TokenMint: mint,
				Success: f.Verdict == txflow.VerdictFinalized, Sell: f.InputMint == orcaswap.WrappedSOLMint,
				SpentUnits: f.InputSpent, ReceivedUnits: f.OutputReceived, FeeLamports: f.FeeLamports,
				PreBaseUnits: balances.Payer.PreLamports, PreQuoteUnits: balances.Token.PreUnits,
				PostBaseUnits: balances.Payer.PostLamports, PostQuoteUnits: balances.Token.PostUnits,
				ReclaimedInputLamports: balances.Payer.ReclaimedInputLamports, OutputAccountRent: f.OutputAccountRent}}, nil
	}
	return WalletStrategyOutcome{}, errors.New("strategy accounting identity is absent from wallet history")
}
