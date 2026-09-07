package policyauthority

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func walletAccountingFixture(t *testing.T, initialNative ...uint64) (string, string, Policy, signer.Request, WalletInventory, submitter.JupiterFinalizedWalletEvidence, time.Time) {
	t.Helper()
	policy, request := paperTerminalRequest(t)
	now := time.Unix(request.ScheduleWindowStartUnix+2, 0).UTC()
	dir := t.TempDir()
	path, claimPath := filepath.Join(dir, "inventory.jsonl"), filepath.Join(dir, "claim.jsonl")
	seedWalletAccountingClaim(t, claimPath, policy, request, now.Add(-time.Second))
	native := uint64(10000)
	if len(initialNative) != 0 {
		native = initialNative[0]
	}
	p := policy.TransactionPolicy.Jupiter
	account, err := orcaswap.AssociatedTokenAddress(p.Owner, p.OutputMint)
	if err != nil {
		t.Fatal(err)
	}
	observation := txflow.WalletObservation{Owner: p.Owner, TokenMint: p.OutputMint, TokenAccount: account,
		GenesisHash: solana.MainnetBetaGenesisHash, PrimaryIdentity: policy.JupiterProviders.PrimaryOriginSHA256,
		SecondaryIdentity: policy.JupiterProviders.SecondaryOriginSHA256, NativeLamports: native, TokenUnits: 100,
		MinimumContextSlot: 100, MaximumContextSlot: 100 + proposalcheck.MaxEvidenceSlotSkew,
		NativePrimarySlot: 100, NativeSecondarySlot: 100, TokenPrimarySlot: 100, TokenSecondarySlot: 100}
	opening, err := initializeWalletInventory(path, policy, now.Add(-time.Second), func(string, string) (txflow.WalletObservation, error) { return observation, nil })
	if err != nil {
		t.Fatal(err)
	}
	validated, err := signer.ValidateJupiterRequest(policy.TransactionPolicy, request)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := signer.RiskBinding(request, validated.MessageSHA256)
	if err != nil {
		t.Fatal(err)
	}
	balances := submitter.JupiterFinalizedWalletEvidence{
		Finalized: submitter.JupiterFinalizedEvidence{ActionID: request.ActionID, RequestSHA256: binding.RequestSHA256,
			TransactionSHA256: strings.Repeat("d", 64), Verdict: txflow.VerdictFinalized, FinalizedSlot: 150, PrimaryEffectSlot: 150, SecondaryEffectSlot: 150,
			InputMint: p.InputMint, OutputMint: p.OutputMint, InputSpent: 10, OutputReceived: 20, FeeLamports: 5000},
		Payer: txflow.JupiterPayerEvidence{Version: 1, PreLamports: native, PostLamports: native - 5010},
		Token: txflow.JupiterTokenEvidence{Version: 1, Account: account, Mint: p.OutputMint, Owner: p.Owner, PreUnits: 100, PostUnits: 120}}
	return path, claimPath, policy, request, opening, balances, now
}

func seedWalletAccountingClaim(t *testing.T, claimPath string, policy Policy, request signer.Request, at time.Time) {
	t.Helper()
	const event = "paper.unsigned-request-claim-v1"
	hash := func(domain string, value any) string {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(append([]byte(event+"/"+domain+"\x00"), raw...))
		return hex.EncodeToString(digest[:])
	}
	claim := struct {
		PaperIntentSHA256 string `json:"paper_intent_sha256"`
		PolicySHA256      string `json:"policy_sha256"`
		RequestSHA256     string `json:"request_sha256"`
		MaxDecisionAgeNS  int64  `json:"max_decision_age_ns,string"`
		AcquisitionSHA256 string `json:"acquisition_sha256"`
	}{strings.Repeat("a", 64), hash("policy", policy), hash("request", request), int64(time.Minute), strings.Repeat("b", 64)}
	store, err := journal.OpenStrict(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Append(at, event, request.ActionID, claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePaperRequestClaim(record, policy, request); err != nil {
		t.Fatal(err)
	}
}

func TestWalletAccountingReserveApplyRestart(t *testing.T) {
	path, claimPath, policy, request, opening, balances, now := walletAccountingFixture(t)
	observe := func(WalletInventory) (txflow.WalletObservation, error) { return opening.Opening, nil }
	pending, err := reserveWalletClaim(path, claimPath, policy, request, now, observe)
	if err != nil || pending.PendingSHA256 == "" || pending.NativeLamports != 10000 {
		t.Fatalf("reserve: %+v, %v", pending, err)
	}
	again, err := reserveWalletClaim(path, claimPath, policy, request, now.Add(time.Second), func(WalletInventory) (txflow.WalletObservation, error) {
		t.Fatal("repeat refreshed reservation")
		return txflow.WalletObservation{}, nil
	})
	if err != nil || again != pending {
		t.Fatalf("repeat reserve: %+v, %v", again, err)
	}
	if _, err := recordPaperTerminal(claimPath, policy, request, now.Add(time.Second), func() (submitter.JupiterFinalizedEvidence, error) { return balances.Finalized, nil }); err != nil {
		t.Fatal(err)
	}
	read := func() (submitter.JupiterFinalizedWalletEvidence, error) { return balances, nil }
	var results [6]WalletInventory
	var errs [6]error
	locked, err := journal.OpenStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() {
			results[i], errs[i] = applyFinalizedWalletClaim(path, claimPath, policy, request, now.Add(2*time.Second), read)
		})
	}
	wg.Wait()
	if err := locked.Close(); err != nil {
		t.Fatal(err)
	}
	for i, result := range results {
		if !errors.Is(errs[i], journal.ErrLocked) || result != (WalletInventory{}) {
			t.Fatalf("contender[%d]: %+v, %v", i, result, errs[i])
		}
	}
	applied, err := applyFinalizedWalletClaim(path, claimPath, policy, request, now.Add(2*time.Second), read)
	if err != nil || applied.PendingSHA256 != "" || applied.NativeLamports != 4990 || applied.TokenUnits != 120 || applied.LastFinalizedSlot != 150 {
		t.Fatalf("apply: %+v, %v", applied, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := applyFinalizedWalletClaim(path, claimPath, policy, request, now.Add(3*time.Second), read)
	if err != nil || repeated != applied {
		t.Fatalf("repeated apply: %+v, %v", repeated, err)
	}
	reopened, err := initializeWalletInventory(path, policy, now.Add(time.Hour), func(string, string) (txflow.WalletObservation, error) {
		t.Fatal("restart reset opening")
		return txflow.WalletObservation{}, nil
	})
	if err != nil || reopened != applied {
		t.Fatalf("restart: %+v, %v", reopened, err)
	}
	if _, err := reserveWalletClaim(path, claimPath, policy, request, now.Add(time.Hour), observe); err == nil {
		t.Fatal("accounted action reserved again")
	}
	balances.Token.PostUnits++
	if _, err := applyFinalizedWalletClaim(path, claimPath, policy, request, now.Add(time.Hour), read); err == nil {
		t.Fatal("changed balances reused accounting")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected/repeated action changed journal: %v", err)
	}
	records, err := journal.ReadRecords(path)
	if err != nil || len(records) != 3 {
		t.Fatalf("records=%d: %v", len(records), err)
	}
}

func TestWalletAccountingRejectsDriftAndStaleObservations(t *testing.T) {
	for _, name := range []string{"native drift", "token drift", "stale", "provider", "wrong head"} {
		t.Run(name, func(t *testing.T) {
			path, claimPath, policy, request, opening, _, now := walletAccountingFixture(t)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			observation := opening.Opening
			switch name {
			case "native drift":
				observation.NativeLamports++
			case "token drift":
				observation.TokenUnits++
			case "stale":
				observation.MinimumContextSlot--
				observation.MaximumContextSlot--
			case "provider":
				observation.PrimaryIdentity = strings.Repeat("e", 64)
			}
			if name == "wrong head" {
				if err := validateWalletReservation(opening, WalletReservation{ClaimSHA256: strings.Repeat("a", 64), ActionID: request.ActionID, PreviousHeadSHA256: strings.Repeat("b", 64), Observation: observation}, policy); err == nil {
					t.Fatal("wrong inventory head accepted")
				}
			} else if got, err := reserveWalletClaim(path, claimPath, policy, request, now, func(WalletInventory) (txflow.WalletObservation, error) { return observation, nil }); err == nil || got != (WalletInventory{}) {
				t.Fatalf("invalid reservation: %+v, %v", got, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("invalid reservation changed journal")
			}
		})
	}
}

func TestWalletAccountingRejectsTimeBeforeTerminal(t *testing.T) {
	path, claimPath, policy, request, opening, balances, now := walletAccountingFixture(t)
	if _, err := reserveWalletClaim(path, claimPath, policy, request, now, func(WalletInventory) (txflow.WalletObservation, error) { return opening.Opening, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := recordPaperTerminal(claimPath, policy, request, now.Add(2*time.Second), func() (submitter.JupiterFinalizedEvidence, error) { return balances.Finalized, nil }); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := applyFinalizedWalletClaim(path, claimPath, policy, request, now.Add(time.Second), func() (submitter.JupiterFinalizedWalletEvidence, error) { return balances, nil })
	if err == nil || got != (WalletInventory{}) {
		t.Fatalf("accounting predating terminal accepted: %+v, %v", got, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("backdated accounting changed journal")
	}
}

func TestWalletAccountingRejectsBrokenContinuityAndTornJournal(t *testing.T) {
	path, claimPath, policy, request, opening, balances, now := walletAccountingFixture(t)
	pending, err := reserveWalletClaim(path, claimPath, policy, request, now, func(WalletInventory) (txflow.WalletObservation, error) { return opening.Opening, nil })
	if err != nil {
		t.Fatal(err)
	}
	receipt := walletAccounting{ReservationSHA256: pending.PendingSHA256, TerminalSHA256: strings.Repeat("f", 64), TerminalAt: pending.PendingAt, Balances: balances}
	if err := validateWalletAccounting(pending, receipt); err != nil {
		t.Fatalf("valid continuity rejected: %v", err)
	}
	for name, mutate := range map[string]func(*walletAccounting){
		"terminal before reservation": func(v *walletAccounting) { v.TerminalAt = pending.PendingAt.Add(-time.Nanosecond) },
		"missing terminal time":       func(v *walletAccounting) { v.TerminalAt = time.Time{} },
		"native pre":                  func(v *walletAccounting) { v.Balances.Payer.PreLamports++ },
		"token pre":                   func(v *walletAccounting) { v.Balances.Token.PreUnits++ },
		"native post":                 func(v *walletAccounting) { v.Balances.Payer.PostLamports++ },
		"token post":                  func(v *walletAccounting) { v.Balances.Token.PostUnits++ },
		"same slot": func(v *walletAccounting) {
			v.Balances.Finalized.FinalizedSlot = 100
			v.Balances.Finalized.PrimaryEffectSlot = 100
			v.Balances.Finalized.SecondaryEffectSlot = 100
		},
		"effect slot": func(v *walletAccounting) { v.Balances.Finalized.SecondaryEffectSlot++ },
		"reservation": func(v *walletAccounting) { v.ReservationSHA256 = strings.Repeat("a", 64) },
		"action":      func(v *walletAccounting) { v.Balances.Finalized.ActionID = strings.Repeat("b", 64) },
		"owner":       func(v *walletAccounting) { v.Balances.Token.Owner = orcaswap.SystemProgram },
	} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			mutate(&changed)
			if err := validateWalletAccounting(pending, changed); err == nil {
				t.Fatal("broken continuity accepted")
			}
		})
	}
	if err := validateWalletReservation(pending, pending.Pending, policy); err == nil {
		t.Fatal("another pending reservation accepted")
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"unfinished":`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := applyFinalizedWalletClaim(path, claimPath, policy, request, now.Add(time.Second), func() (submitter.JupiterFinalizedWalletEvidence, error) {
		t.Fatal("torn inventory read recovery")
		return balances, nil
	})
	if err == nil || got != (WalletInventory{}) {
		t.Fatalf("torn inventory accepted: %+v, %v", got, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("torn inventory repaired")
	}
}

func TestWalletAccountingFailedTradeChargesFeeOnly(t *testing.T) {
	path, claimPath, policy, request, opening, balances, now := walletAccountingFixture(t)
	if _, err := reserveWalletClaim(path, claimPath, policy, request, now, func(WalletInventory) (txflow.WalletObservation, error) { return opening.Opening, nil }); err != nil {
		t.Fatal(err)
	}
	balances.Finalized.Verdict = txflow.VerdictFailed
	balances.Finalized.InputSpent, balances.Finalized.OutputReceived = 0, 0
	balances.Payer.PostLamports = 5000
	balances.Token.PostUnits = 100
	if _, err := recordPaperTerminal(claimPath, policy, request, now.Add(time.Second), func() (submitter.JupiterFinalizedEvidence, error) { return balances.Finalized, nil }); err != nil {
		t.Fatal(err)
	}
	got, err := applyFinalizedWalletClaim(path, claimPath, policy, request, now.Add(time.Second), func() (submitter.JupiterFinalizedWalletEvidence, error) { return balances, nil })
	if err != nil || got.NativeLamports != 5000 || got.TokenUnits != 100 || got.PendingSHA256 != "" {
		t.Fatalf("failed accounting: %+v, %v", got, err)
	}
}

func TestWalletAccountingProgressesTwoClaimsWithoutReapplyingFirst(t *testing.T) {
	path, firstPath, policy, first, opening, firstBalances, now := walletAccountingFixture(t, 30000)
	policyBytes, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	ledger := policy.TransactionPolicy.AuthorizationLedgerPath
	if _, err := os.Stat(ledger); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture authorization ledger exists: %v", err)
	}
	second := first
	second.ScheduleWindowStartUnix = first.ScheduleWindowEndUnix
	second.ScheduleWindowEndUnix += int64(policy.TransactionPolicy.ScheduleWindowSeconds)
	second.ActionID, err = jupiterswap.ComputeActionID(first.ProfileFingerprint, second.ScheduleWindowStartUnix)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := signer.ValidateJupiterRequest(policy.TransactionPolicy, second)
	if err != nil {
		t.Fatalf("second scheduled request is invalid: %v", err)
	}
	binding, err := signer.RiskBinding(second, validated.MessageSHA256)
	if err != nil {
		t.Fatal(err)
	}
	secondNow := time.Unix(second.ScheduleWindowStartUnix+2, 0).UTC()
	secondPath := filepath.Join(t.TempDir(), "second-claim.jsonl")
	seedWalletAccountingClaim(t, secondPath, policy, second, secondNow.Add(-time.Second))
	firstOriginal, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	secondOriginal, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reserveWalletClaim(path, firstPath, policy, first, now, func(WalletInventory) (txflow.WalletObservation, error) { return opening.Opening, nil }); err != nil {
		t.Fatal(err)
	}
	pendingBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reserveWalletClaim(path, secondPath, policy, second, secondNow, func(WalletInventory) (txflow.WalletObservation, error) {
		t.Fatal("pending action permitted a second observation")
		return txflow.WalletObservation{}, nil
	}); err == nil {
		t.Fatal("second claim admitted while first pending")
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(pendingBytes, unchanged) {
		t.Fatal("rejected second claim changed pending state")
	}
	if _, err := recordPaperTerminal(firstPath, policy, first, now.Add(time.Second), func() (submitter.JupiterFinalizedEvidence, error) { return firstBalances.Finalized, nil }); err != nil {
		t.Fatal(err)
	}
	afterFirst, err := applyFinalizedWalletClaim(path, firstPath, policy, first, now.Add(2*time.Second), func() (submitter.JupiterFinalizedWalletEvidence, error) { return firstBalances, nil })
	if err != nil || afterFirst.NativeLamports != 24990 || afterFirst.TokenUnits != 120 {
		t.Fatalf("first accounting: %+v, %v", afterFirst, err)
	}
	secondObservation := opening.Opening
	secondObservation.NativeLamports, secondObservation.TokenUnits = 24990, 120
	secondObservation.MinimumContextSlot, secondObservation.MaximumContextSlot = 160, 160+proposalcheck.MaxEvidenceSlotSkew
	secondObservation.NativePrimarySlot, secondObservation.NativeSecondarySlot = 160, 160
	secondObservation.TokenPrimarySlot, secondObservation.TokenSecondarySlot = 160, 160
	if _, err := reserveWalletClaim(path, secondPath, policy, second, secondNow, func(value WalletInventory) (txflow.WalletObservation, error) {
		if value != afterFirst {
			t.Fatal("second observation did not receive accounted inventory")
		}
		return secondObservation, nil
	}); err != nil {
		t.Fatal(err)
	}
	secondBalances := firstBalances
	secondBalances.Finalized.ActionID, secondBalances.Finalized.RequestSHA256 = second.ActionID, binding.RequestSHA256
	secondBalances.Finalized.TransactionSHA256 = strings.Repeat("e", 64)
	secondBalances.Finalized.FinalizedSlot, secondBalances.Finalized.PrimaryEffectSlot, secondBalances.Finalized.SecondaryEffectSlot = 170, 170, 170
	secondBalances.Payer.PreLamports, secondBalances.Payer.PostLamports = 24990, 19980
	secondBalances.Token.PreUnits, secondBalances.Token.PostUnits = 120, 140
	if _, err := recordPaperTerminal(secondPath, policy, second, secondNow.Add(time.Second), func() (submitter.JupiterFinalizedEvidence, error) { return secondBalances.Finalized, nil }); err != nil {
		t.Fatal(err)
	}
	final, err := applyFinalizedWalletClaim(path, secondPath, policy, second, secondNow.Add(2*time.Second), func() (submitter.JupiterFinalizedWalletEvidence, error) { return secondBalances, nil })
	if err != nil || final.NativeLamports != 19980 || final.TokenUnits != 140 || final.LastFinalizedSlot != 170 || final.PendingSHA256 != "" {
		t.Fatalf("second accounting: %+v, %v", final, err)
	}
	firstOutcome, err := readWalletStrategyOutcome(path, firstPath, afterFirst.HeadSHA256, policy, first, secondNow.Add(time.Hour),
		func() (submitter.JupiterFinalizedWalletEvidence, error) { return firstBalances, nil })
	if err != nil || firstOutcome.Amounts.PreBaseUnits != 30000 || firstOutcome.Amounts.PostBaseUnits != 24990 || firstOutcome.ActionID != first.ActionID {
		t.Fatalf("historical strategy outcome after later accounting: %+v, %v", firstOutcome, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	claimBefore := make(map[string][]byte)
	for claimPath, original := range map[string][]byte{firstPath: firstOriginal, secondPath: secondOriginal} {
		data, err := os.ReadFile(claimPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(data, original) {
			t.Fatal("original claim bytes changed")
		}
		claimBefore[claimPath] = data
	}
	repeated, err := applyFinalizedWalletClaim(path, firstPath, policy, first, secondNow.Add(3*time.Second), func() (submitter.JupiterFinalizedWalletEvidence, error) { return firstBalances, nil })
	if err != nil || repeated != final {
		t.Fatalf("first repeat reverted final inventory: %+v, %v", repeated, err)
	}
	restarted, err := initializeWalletInventory(path, policy, secondNow.Add(time.Hour), func(string, string) (txflow.WalletObservation, error) {
		t.Fatal("restart replaced opening")
		return txflow.WalletObservation{}, nil
	})
	if err != nil || restarted != final {
		t.Fatalf("two-action restart: %+v, %v", restarted, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("first replay changed accounted journal")
	}
	for claimPath, original := range claimBefore {
		data, err := os.ReadFile(claimPath)
		if err != nil || !bytes.Equal(data, original) {
			t.Fatal("accounting changed completed claim journal")
		}
	}
	records, err := journal.ReadRecords(path)
	if err != nil || len(records) != 5 {
		t.Fatalf("two-action records=%d: %v", len(records), err)
	}
	afterPolicy, err := json.Marshal(policy)
	if err != nil || !bytes.Equal(policyBytes, afterPolicy) {
		t.Fatal("accounting changed policy caps or ledger path")
	}
	if _, err := os.Stat(ledger); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("accounting touched authorization ledger: %v", err)
	}
}

func TestFinalizeWalletClaimPreservesCrashBoundary(t *testing.T) {
	for _, mode := range []string{"complete", "second read fails", "missing recovery", "unreserved", "wrong claim"} {
		t.Run(mode, func(t *testing.T) {
			path, claimPath, policy, request, opening, balances, now := walletAccountingFixture(t)
			policyBefore, err := json.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "unreserved" {
				if _, err := reserveWalletClaim(path, claimPath, policy, request, now, func(WalletInventory) (txflow.WalletObservation, error) { return opening.Opening, nil }); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "wrong claim" {
				request.ScheduleWindowStartUnix = request.ScheduleWindowEndUnix
				request.ScheduleWindowEndUnix += int64(policy.TransactionPolicy.ScheduleWindowSeconds)
				request.ActionID, err = jupiterswap.ComputeActionID(request.ProfileFingerprint, request.ScheduleWindowStartUnix)
				if err != nil {
					t.Fatal(err)
				}
				claimPath = filepath.Join(t.TempDir(), "foreign.jsonl")
				seedWalletAccountingClaim(t, claimPath, policy, request, time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC())
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			claimBefore, err := os.ReadFile(claimPath)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			unavailable := errors.New("recovery unavailable")
			read := func() (submitter.JupiterFinalizedWalletEvidence, error) {
				calls++
				if mode == "unreserved" || mode == "wrong claim" {
					t.Fatal("unreserved claim read recovery")
				}
				if mode == "missing recovery" || (mode == "second read fails" && calls == 2) {
					return submitter.JupiterFinalizedWalletEvidence{}, unavailable
				}
				return balances, nil
			}
			got, err := finalizeWalletClaim(path, claimPath, policy, request, now.Add(time.Second), read)
			if mode == "unreserved" || mode == "wrong claim" || mode == "missing recovery" {
				if err == nil || got != (WalletInventory{}) {
					t.Fatalf("invalid finalization: %+v, %v", got, err)
				}
				after, e := os.ReadFile(path)
				if e != nil || !bytes.Equal(before, after) {
					t.Fatal("rejected finalization changed inventory")
				}
				after, e = os.ReadFile(claimPath)
				if e != nil || !bytes.Equal(claimBefore, after) {
					t.Fatal("rejected finalization appended terminal")
				}
				return
			}
			if mode == "second read fails" {
				if !errors.Is(err, unavailable) || got != (WalletInventory{}) || calls != 2 {
					t.Fatalf("partial finalization: %+v, %v, reads=%d", got, err, calls)
				}
				records, e := journal.ReadRecords(claimPath)
				if e != nil || len(records) != 2 {
					t.Fatalf("terminal not retained: %d, %v", len(records), e)
				}
				pending, e := ReadWalletInventory(path, policy)
				if e != nil || pending.PendingSHA256 == "" || pending.NativeLamports != 10000 || pending.TokenUnits != 100 {
					t.Fatalf("partial finalization credited inventory: %+v, %v", pending, e)
				}
				after, e := os.ReadFile(path)
				if e != nil || !bytes.Equal(before, after) {
					t.Fatal("failed application changed inventory")
				}
				got, err = finalizeWalletClaim(path, claimPath, policy, request, now.Add(2*time.Second), read)
			}
			if err != nil || got.PendingSHA256 != "" || got.NativeLamports != 4990 || got.TokenUnits != 120 {
				t.Fatalf("finalization: %+v, %v", got, err)
			}
			inventoryFinal, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			claimFinal, e := os.ReadFile(claimPath)
			if e != nil {
				t.Fatal(e)
			}
			again, e := finalizeWalletClaim(path, claimPath, policy, request, now.Add(3*time.Second), read)
			if e != nil || again != got {
				t.Fatalf("repeat finalization: %+v, %v", again, e)
			}
			for file, want := range map[string][]byte{path: inventoryFinal, claimPath: claimFinal} {
				raw, e := os.ReadFile(file)
				if e != nil || !bytes.Equal(raw, want) {
					t.Fatal("finalization repeat changed durable bytes")
				}
			}
			records, e := journal.ReadRecords(path)
			if e != nil || len(records) != 3 {
				t.Fatalf("inventory records=%d: %v", len(records), e)
			}
			records, e = journal.ReadRecords(claimPath)
			if e != nil || len(records) != 2 {
				t.Fatalf("claim records=%d: %v", len(records), e)
			}
			policyAfter, e := json.Marshal(policy)
			if e != nil || !bytes.Equal(policyBefore, policyAfter) {
				t.Fatal("finalization changed caps")
			}
			if _, e := os.Stat(policy.TransactionPolicy.AuthorizationLedgerPath); !errors.Is(e, os.ErrNotExist) {
				t.Fatalf("finalization touched authorization ledger: %v", e)
			}
		})
	}
}
