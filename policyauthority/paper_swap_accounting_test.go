package policyauthority

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func TestPaperSwapAccountingNormalization(t *testing.T) {
	for _, test := range []struct {
		name                      string
		nativeInput, failed       bool
		input, output, fee, rent  uint64
		nativeDebit, nativeCredit uint64
		tokenDebit, tokenCredit   uint64
		invalid                   bool
	}{
		{name: "native input with separate locked rent", nativeInput: true, input: 10, output: 20, fee: 5, rent: 3, nativeDebit: 10, tokenCredit: 20},
		{name: "native output with separate fee", input: 20, output: 10, fee: 5, nativeCredit: 10, tokenDebit: 20},
		{name: "failed native input records fee without swap", nativeInput: true, failed: true, fee: 5},
		{name: "failed native output records fee without swap", failed: true, fee: 5},
		{name: "failed swap is invalid", failed: true, input: 1, invalid: true},
		{name: "failed rent is invalid", failed: true, rent: 1, invalid: true},
		{name: "costs never modify swap units", nativeInput: true, input: math.MaxUint64, fee: 1, rent: 1, nativeDebit: math.MaxUint64},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := submitter.JupiterFinalizedEvidence{InputMint: "token", OutputMint: orcaswap.WrappedSOLMint,
				Verdict: txflow.VerdictFinalized, InputSpent: test.input, OutputReceived: test.output,
				FeeLamports: test.fee, OutputAccountRent: test.rent}
			if test.nativeInput {
				evidence.InputMint, evidence.OutputMint = evidence.OutputMint, evidence.InputMint
			}
			if test.failed {
				evidence.Verdict = txflow.VerdictFailed
			}
			delta, err := normalizedPaperSwapAccounting(evidence)
			if test.invalid {
				if err == nil || delta != (PaperSwapAccounting{}) {
					t.Fatalf("invalid effects accepted: %+v, %v", delta, err)
				}
				return
			}
			if err != nil || delta.Finalized != evidence || delta.TokenMint != "token" ||
				delta.NativeSwapDebitLamports != test.nativeDebit || delta.NativeSwapCreditLamports != test.nativeCredit ||
				delta.TokenDebitUnits != test.tokenDebit || delta.TokenCreditUnits != test.tokenCredit {
				t.Fatalf("swap accounting = %+v, %v", delta, err)
			}
			raw, err := json.Marshal(delta)
			if err != nil || !strings.Contains(string(raw), `"native_swap_debit_lamports":"`) {
				t.Fatalf("base-unit amounts must serialize as strings: %s, %v", raw, err)
			}
		})
	}
	for _, evidence := range []submitter.JupiterFinalizedEvidence{
		{InputMint: "token", OutputMint: "other", Verdict: txflow.VerdictFinalized},
		{InputMint: orcaswap.WrappedSOLMint, OutputMint: orcaswap.WrappedSOLMint, Verdict: txflow.VerdictFinalized},
		{InputMint: orcaswap.WrappedSOLMint, OutputMint: "token", Verdict: "submitted"},
	} {
		if _, err := normalizedPaperSwapAccounting(evidence); err == nil {
			t.Fatal("unsupported movement accepted")
		}
	}
}

func TestPaperSwapAccountingRequiresCompletedClaim(t *testing.T) {
	for _, count := range []int{0, 1, 3} {
		path := filepath.Join(t.TempDir(), "claim.jsonl")
		if count != 0 {
			store, err := journal.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			for range count {
				if _, err := store.Append(time.Now(), "pending", "action", struct{}{}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
		}
		called := false
		delta, err := readPaperSwapAccounting(path, Policy{}, signer.Request{}, func() (submitter.JupiterFinalizedEvidence, error) {
			called = true
			return submitter.JupiterFinalizedEvidence{}, nil
		})
		if err == nil || called || delta != (PaperSwapAccounting{}) {
			t.Fatalf("incomplete claim read recovery: count=%d, called=%v, delta=%+v, err=%v", count, called, delta, err)
		}
	}
}
