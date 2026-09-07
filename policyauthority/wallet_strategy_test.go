package policyauthority

import (
	"bytes"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func TestWalletStrategyOutcomeRequiresExactAccountedEvidence(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "fee-only failure"}[failed], func(t *testing.T) {
			path, claimPath, policy, request, opening, balances, now := walletAccountingFixture(t)
			if failed {
				balances.Finalized.Verdict = txflow.VerdictFailed
				balances.Finalized.InputSpent, balances.Finalized.OutputReceived = 0, 0
				balances.Payer.PostLamports = opening.NativeLamports - balances.Finalized.FeeLamports
				balances.Token.PostUnits = opening.TokenUnits
			}
			pending, err := reserveWalletClaim(path, claimPath, policy, request, now,
				func(WalletInventory) (txflow.WalletObservation, error) { return opening.Opening, nil })
			if err != nil {
				t.Fatal(err)
			}
			read := func() (submitter.JupiterFinalizedWalletEvidence, error) { return balances, nil }
			if _, err := readWalletStrategyOutcome(path, claimPath, pending.HeadSHA256, policy, request, now.Add(time.Hour), read); err == nil {
				t.Fatal("reservation became a strategy outcome")
			}
			if _, err := recordPaperTerminal(claimPath, policy, request, now.Add(time.Second),
				func() (submitter.JupiterFinalizedEvidence, error) { return balances.Finalized, nil }); err != nil {
				t.Fatal(err)
			}
			if _, err := readWalletStrategyOutcome(path, claimPath, pending.HeadSHA256, policy, request, now.Add(time.Hour), read); err == nil {
				t.Fatal("unaccounted terminal became a strategy outcome")
			}
			accounted, err := applyFinalizedWalletClaim(path, claimPath, policy, request, now.Add(2*time.Second), read)
			if err != nil {
				t.Fatal(err)
			}
			records, err := journal.ReadRecords(path)
			if err != nil {
				t.Fatal(err)
			}
			known := records[len(records)-1].At
			before, claimBefore := walletAdmissionBytes(t, path), walletAdmissionBytes(t, claimPath)
			got, err := readWalletStrategyOutcome(path, claimPath, accounted.HeadSHA256, policy, request, known, read)
			if err != nil || got.Amounts.AccountingSHA256 != accounted.HeadSHA256 || got.WalletBindingSHA256 != opening.BindingSHA256 ||
				got.PreviousHeadSHA256 != opening.HeadSHA256 || got.ClaimSHA256 != pending.Pending.ClaimSHA256 ||
				got.ActionID != request.ActionID || got.RequestSHA256 != balances.Finalized.RequestSHA256 ||
				got.TransactionSHA256 != balances.Finalized.TransactionSHA256 || got.KnownAt != known ||
				got.TerminalAt.After(got.KnownAt) || got.Amounts.TokenMint != opening.Opening.TokenMint ||
				got.Amounts.Success == failed || !got.Amounts.Sell || got.Amounts.PreBaseUnits != opening.NativeLamports ||
				got.Amounts.PostBaseUnits != accounted.NativeLamports || got.Amounts.PreQuoteUnits != opening.TokenUnits ||
				got.Amounts.PostQuoteUnits != accounted.TokenUnits || got.Amounts.SpentUnits != balances.Finalized.InputSpent ||
				got.Amounts.ReceivedUnits != balances.Finalized.OutputReceived || got.Amounts.FeeLamports != balances.Finalized.FeeLamports {
				t.Fatalf("accounted strategy projection: %+v, %v", got, err)
			}
			again, err := readWalletStrategyOutcome(path, claimPath, accounted.HeadSHA256, policy, request, known.Add(time.Hour), read)
			if err != nil || again != got {
				t.Fatalf("restart changed exact outcome: %+v, %v", again, err)
			}
			if _, err := readWalletStrategyOutcome(path, claimPath, accounted.HeadSHA256, policy, request, known.Add(-time.Nanosecond), read); err == nil {
				t.Fatal("future accounting leaked into earlier strategy time")
			}
			if _, err := readWalletStrategyOutcome(path, claimPath, accounted.HeadSHA256, policy, request, known,
				func() (submitter.JupiterFinalizedWalletEvidence, error) {
					return balances, errors.New("recovery unavailable")
				}); err == nil {
				t.Fatal("missing recovery became a strategy outcome")
			}
			balances.Payer.PostLamports++
			if _, err := readWalletStrategyOutcome(path, claimPath, accounted.HeadSHA256, policy, request, known, read); err == nil {
				t.Fatal("changed recovery was trusted")
			}
			if !bytes.Equal(before, walletAdmissionBytes(t, path)) || !bytes.Equal(claimBefore, walletAdmissionBytes(t, claimPath)) {
				t.Fatal("strategy projection changed source journals")
			}
			if _, err := os.Stat(policy.TransactionPolicy.AuthorizationLedgerPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("strategy projection touched signer allowance")
			}
		})
	}
}
