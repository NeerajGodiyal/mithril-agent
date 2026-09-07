package policyauthority

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

// The inherited evidence verifies the offline unsigned message fixture. Only
// its finalized slots advance to the concrete accounted wallet's next context.
type strategyClaimEvidence struct {
	*jupiterEvidence
	slot  uint64
	guard jupiterswap.RouteGuardDeployment
}

func (e strategyClaimEvidence) VerifyImmutableProgramDeployment(_ context.Context, program, programData string, deploymentSlot, codeLength uint64, codeSHA256 string, minimumSlot uint64) error {
	if program != e.guard.Program || programData != e.guard.ProgramData || deploymentSlot != e.guard.DeploymentSlot || codeLength != e.guard.CodeLength || codeSHA256 != e.guard.CodeSHA256 || minimumSlot == 0 {
		return errors.New("unexpected strategy fixture route guard deployment")
	}
	return nil
}

func (e strategyClaimEvidence) FeeForV0Message(context.Context, []byte, map[[32]byte][][32]byte, string, uint64) (txflow.FeeEvidence, error) {
	return txflow.FeeEvidence{Lamports: 5000, PrimaryContextSlot: e.slot, SecondaryContextSlot: e.slot}, nil
}

func (e strategyClaimEvidence) SimulateV0(context.Context, []byte, map[[32]byte][][32]byte, string, uint64) (txflow.LegacySimulationEvidence, error) {
	return txflow.LegacySimulationEvidence{ContextSlot: e.slot, UnitsConsumed: 10000, LogsSHA256: strings.Repeat("0", 64)}, nil
}

type strategyClaimRPC struct {
	*solanarpc.Client
	identity string
}

func (r strategyClaimRPC) Identity() string { return r.identity }

// Retain the fixture's protected provider identities while exercising the real
// finalized RPC decoder against two independent local test transports.
func strategyClaimLifecycle(t *testing.T, wallet WalletInventory) *txflow.Lifecycle {
	t.Helper()
	newRPC := func(identity string) strategyClaimRPC {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				ID     uint64            `json:"id"`
				Method string            `json:"method"`
				Params []json.RawMessage `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			var result any
			switch request.Method {
			case "getGenesisHash":
				result = wallet.Opening.GenesisHash
			case "getSlot":
				result = wallet.LastFinalizedSlot
			case "getAccountInfo":
				if len(request.Params) != 2 || !bytes.Contains(request.Params[1], []byte(`"commitment":"finalized"`)) {
					t.Error("nonfinalized reservation read")
					return
				}
				var address string
				if err := json.Unmarshal(request.Params[0], &address); err != nil {
					t.Error(err)
					return
				}
				program, lamports, data := orcaswap.SystemProgram, wallet.NativeLamports, []byte{}
				if address == wallet.Opening.TokenAccount {
					program, lamports, data = orcaswap.TokenProgram, 2039280, make([]byte, 165)
					mint, err := solana.Decode32(wallet.Opening.TokenMint)
					if err != nil {
						t.Error(err)
						return
					}
					owner, err := solana.Decode32(wallet.Opening.Owner)
					if err != nil {
						t.Error(err)
						return
					}
					copy(data[:32], mint[:])
					copy(data[32:64], owner[:])
					binary.LittleEndian.PutUint64(data[64:72], wallet.TokenUnits)
					data[108] = 1
				} else if address != wallet.Opening.Owner {
					t.Error("unexpected reservation address")
					return
				}
				result = map[string]any{"context": map[string]any{"slot": wallet.LastFinalizedSlot}, "value": map[string]any{"owner": program, "lamports": lamports, "space": len(data), "executable": false, "data": []any{base64.StdEncoding.EncodeToString(data), "base64"}}}
			default:
				t.Errorf("unexpected reservation RPC %q", request.Method)
				return
			}
			if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
				t.Error(err)
			}
		}))
		t.Cleanup(server.Close)
		client, err := solanarpc.New(server.URL, server.Client(), true)
		if err != nil {
			t.Fatal(err)
		}
		return strategyClaimRPC{Client: client, identity: identity}
	}
	lifecycle, err := txflow.NewEvidenceLifecycle(newRPC(wallet.Opening.PrimaryIdentity), newRPC(wallet.Opening.SecondaryIdentity))
	if err != nil {
		t.Fatal(err)
	}
	return lifecycle
}

func TestPrepareStrategyWalletClaimConnected(t *testing.T) {
	for _, mode := range []string{"prepare and recover", "reservation failure and recover", "completion expiry", "changed policy", "reused window"} {
		t.Run(mode, func(t *testing.T) {
			path, seed, authority, recovery, next, acquisition, at := strategyDecisionFixture(t)
			if !reflect.DeepEqual(authority, next) {
				t.Fatal("continuation fixture changed original controls")
			}
			if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, at); err != nil {
				t.Fatal(err)
			}
			wallet, err := ReadWalletInventory(seed.WalletPath, next)
			if err != nil {
				t.Fatal(err)
			}
			observedWallet := wallet
			if mode == "reservation failure and recover" {
				// Both real RPC decoders report a changed balance, so the
				// reservation fails after the unsigned claim was retained.
				observedWallet.NativeLamports--
			}
			lifecycle := strategyClaimLifecycle(t, observedWallet)
			base := &jupiterEvidence{primary: wallet.Opening.PrimaryIdentity, secondary: wallet.Opening.SecondaryIdentity}
			evidence := &paperReserveEvidence{Evidence: strategyClaimEvidence{jupiterEvidence: base, slot: wallet.LastFinalizedSlot, guard: next.TransactionPolicy.Jupiter.RouteGuard}, mutate: func(account *txflow.AccountEvidence) {
				account.PrimaryLamports, account.SecondaryLamports = wallet.NativeLamports, wallet.NativeLamports
			}}
			primary := jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.PrimaryIdentity}
			secondary := jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.SecondaryIdentity}
			window := int64(next.TransactionPolicy.ScheduleWindowSeconds)
			anchor := next.TransactionPolicy.ScheduleAnchorUnix
			start := anchor + (at.Unix()-anchor)/window*window
			original, err := ReadClaimedPaperRequest(seed.ClaimPath, authority)
			if err != nil {
				t.Fatal(err)
			}
			if start == original.ScheduleWindowStartUnix {
				t.Fatal("fixture did not advance the original schedule")
			}
			paths := []string{path, seed.WalletPath, seed.ClaimPath, acquisition}
			before := make(map[string][]byte)
			for _, p := range paths {
				before[p], err = os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed policy" {
				next.GrantLifetimeSecs++
			}
			if mode == "reused window" {
				start = original.ScheduleWindowStartUnix
			}
			var got WalletPaperClaim
			invoke := func() {
				got, err = PrepareStrategyWalletClaim(t.Context(), seed.WalletPath, path, authority, recovery, next, start, at, time.Minute, evidence, primary, secondary, lifecycle)
			}
			if mode == "completion expiry" {
				evidence.delay = 2 * time.Minute
				synctest.Test(t, func(t *testing.T) { invoke() })
			} else {
				invoke()
			}
			if mode == "reservation failure and recover" {
				if err == nil || !strings.Contains(err.Error(), "wallet balances changed outside accounted trades") || got.Request.ActionID != "" {
					t.Fatalf("changed wallet balance did not fail reservation: %v", err)
				}
				claimPath := seed.WalletPath + ".claim-" + wallet.HeadSHA256 + ".jsonl"
				retained, readErr := ReadClaimedPaperRequest(claimPath, next)
				if readErr != nil || retained.ActionID == "" || retained.ActionID == original.ActionID {
					t.Fatalf("reservation failure lost the concrete next claim: %v", readErr)
				}
				unchanged, readErr := ReadWalletInventory(seed.WalletPath, next)
				if readErr != nil || unchanged.PendingSHA256 != "" || !reflect.DeepEqual(unchanged, wallet) {
					t.Fatalf("failed reservation changed accounted wallet: %v", readErr)
				}
				data, readErr := os.ReadFile(seed.WalletPath)
				if readErr != nil || !bytes.Equal(before[seed.WalletPath], data) {
					t.Fatalf("failed reservation appended wallet evidence: %v", readErr)
				}
				// This is the independently read retained state, not a
				// successful preparation result or a repaired reservation.
				got = WalletPaperClaim{ClaimPath: claimPath, Request: retained, Inventory: unchanged}
				err = nil
			} else if mode != "prepare and recover" {
				if err == nil || got.Request.ActionID != "" {
					t.Fatalf("invalid continuation accepted: %v", err)
				}
				for _, p := range paths {
					after, readErr := os.ReadFile(p)
					if readErr != nil || !bytes.Equal(before[p], after) {
						t.Fatalf("rejection mutated %s: %v", p, readErr)
					}
				}
				if mode == "completion expiry" && (evidence.calls != 1 || !strings.Contains(err.Error(), "recency")) {
					t.Fatalf("did not expire after evidence: calls=%d err=%v", evidence.calls, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "prepare and recover" && (got.Recovered || got.Inventory.PendingSHA256 == "" || got.Inventory.Pending.PreviousHeadSHA256 != wallet.HeadSHA256 || got.Inventory.Pending.ActionID != got.Request.ActionID || got.Request.ActionID == original.ActionID || got.Request.ProfileFingerprint != original.ProfileFingerprint) {
				t.Fatalf("continuation identity/reservation mismatch: recovered=%v", got.Recovered)
			}
			retained, err := ReadClaimedPaperRequest(got.ClaimPath, next)
			if err != nil || !reflect.DeepEqual(retained, got.Request) {
				t.Fatalf("exact retained request: %v", err)
			}
			for _, p := range []string{path, seed.ClaimPath, acquisition} {
				after, readErr := os.ReadFile(p)
				if readErr != nil || !bytes.Equal(before[p], after) {
					t.Fatalf("preparation changed original evidence: %v", readErr)
				}
			}
			claimBefore, err := os.ReadFile(got.ClaimPath)
			if err != nil {
				t.Fatal(err)
			}
			walletBefore, err := os.ReadFile(seed.WalletPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(acquisition, acquisition+".unavailable"); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path, path+".unavailable"); err != nil {
				t.Fatal(err)
			}
			retried, err := PrepareStrategyWalletClaim(t.Context(), seed.WalletPath, path, Policy{}, recovery, next, 0, at.Add(24*time.Hour), 0, nil, nil, nil, nil)
			if err != nil || !retried.Recovered || retried.ClaimPath != got.ClaimPath || !reflect.DeepEqual(retried.Request, got.Request) || !reflect.DeepEqual(retried.Inventory, got.Inventory) {
				t.Fatalf("expired recovery did not preserve exact claim: %v", err)
			}
			for p, want := range map[string][]byte{got.ClaimPath: claimBefore, seed.WalletPath: walletBefore} {
				after, readErr := os.ReadFile(p)
				if readErr != nil || !bytes.Equal(want, after) {
					t.Fatalf("recovery mutated durable state: %v", readErr)
				}
			}
			records, err := journal.ReadRecords(got.ClaimPath)
			if err != nil || len(records) != 1 {
				t.Fatalf("claim append count: %d, %v", len(records), err)
			}
		})
	}
}
