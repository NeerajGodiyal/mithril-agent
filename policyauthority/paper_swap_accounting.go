package policyauthority

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

// PaperSwapAccounting describes verified swap legs and separately recorded costs.
// It is not a complete wallet delta: historical evidence omits refunds from
// closing a pre-existing empty wrapped-SOL account. Native swap amounts exclude
// fees and rent, which remain separate in Finalized. Do not derive spendable
// balances or authorize another trade from this projection alone.
type PaperSwapAccounting struct {
	ClaimSHA256              string                             `json:"claim_sha256"`
	TerminalSHA256           string                             `json:"terminal_sha256"`
	TerminalAt               time.Time                          `json:"terminal_at"`
	Owner                    string                             `json:"owner"`
	Finalized                submitter.JupiterFinalizedEvidence `json:"finalized"`
	NativeSwapDebitLamports  uint64                             `json:"native_swap_debit_lamports,string"`
	NativeSwapCreditLamports uint64                             `json:"native_swap_credit_lamports,string"`
	TokenMint                string                             `json:"token_mint"`
	TokenDebitUnits          uint64                             `json:"token_debit_units,string"`
	TokenCreditUnits         uint64                             `json:"token_credit_units,string"`
}

// ReadPaperSwapAccounting revalidates a completed claim against exact recovery
// evidence. It neither writes state nor applies amounts, releases a claim, or
// authorizes another action. Consumers must persistently deduplicate the finalized
// action/request/transaction identity; repeated reads are not new fills.
func ReadPaperSwapAccounting(path string, authority Policy, request signer.Request,
	recoveryPolicy submitter.Policy,
) (PaperSwapAccounting, error) {
	return readPaperSwapAccounting(path, authority, request, func() (submitter.JupiterFinalizedEvidence, error) {
		return submitter.ReadJupiterFinalizedEvidence(recoveryPolicy, request)
	})
}

func readPaperSwapAccounting(path string, authority Policy, request signer.Request,
	readFinalized func() (submitter.JupiterFinalizedEvidence, error),
) (PaperSwapAccounting, error) {
	records, err := journal.ReadRecords(path)
	if err != nil {
		return PaperSwapAccounting{}, err
	}
	if len(records) != 2 {
		return PaperSwapAccounting{}, errors.New("swap accounting requires a completed claim")
	}
	claim, err := paperTerminalClaim(records, authority, request, records[1].At)
	if err != nil {
		return PaperSwapAccounting{}, err
	}
	evidence, err := readFinalized()
	if err != nil {
		return PaperSwapAccounting{}, err
	}
	canonical, err := json.Marshal(paperTerminal{ClaimSHA256: claim, Finalized: evidence})
	if err != nil || !bytes.Equal(canonical, records[1].Payload) {
		return PaperSwapAccounting{}, errors.New("swap accounting terminal differs from verified recovery")
	}
	delta, err := normalizedPaperSwapAccounting(evidence)
	if err != nil {
		return PaperSwapAccounting{}, err
	}
	delta.ClaimSHA256, delta.TerminalSHA256 = claim, records[1].Hash
	delta.TerminalAt = records[1].At
	delta.Owner = authority.TransactionPolicy.Source
	return delta, nil
}

func normalizedPaperSwapAccounting(evidence submitter.JupiterFinalizedEvidence) (PaperSwapAccounting, error) {
	nativeInput := evidence.InputMint == orcaswap.WrappedSOLMint
	nativeOutput := evidence.OutputMint == orcaswap.WrappedSOLMint
	if nativeInput == nativeOutput || evidence.InputMint == "" || evidence.OutputMint == "" {
		return PaperSwapAccounting{}, errors.New("swap accounting requires exactly one native SOL side")
	}
	if evidence.Verdict != txflow.VerdictFinalized && evidence.Verdict != txflow.VerdictFailed {
		return PaperSwapAccounting{}, errors.New("swap accounting requires a finalized verdict")
	}
	if evidence.Verdict == txflow.VerdictFailed &&
		(evidence.InputSpent != 0 || evidence.OutputReceived != 0 || evidence.OutputAccountRent != 0) {
		return PaperSwapAccounting{}, errors.New("failed transaction contains swap or rent effects")
	}
	delta := PaperSwapAccounting{Finalized: evidence, TokenMint: evidence.InputMint,
		TokenDebitUnits: evidence.InputSpent, NativeSwapCreditLamports: evidence.OutputReceived}
	if nativeInput {
		delta.TokenMint, delta.TokenCreditUnits = evidence.OutputMint, evidence.OutputReceived
		delta.NativeSwapDebitLamports = evidence.InputSpent
		delta.TokenDebitUnits, delta.NativeSwapCreditLamports = 0, 0
	}
	return delta, nil
}
