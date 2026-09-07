package policyauthority

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func TestInitializeWalletInventoryUsesFinalizedRPC(t *testing.T) {
	policy, _ := paperTerminalRequest(t)
	_, owner, mint, err := walletInventoryBinding(policy)
	if err != nil {
		t.Fatal(err)
	}
	address, err := orcaswap.AssociatedTokenAddress(owner, mint)
	if err != nil {
		t.Fatal(err)
	}
	newRPC := func() *solanarpc.Client {
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
				result = solana.MainnetBetaGenesisHash
			case "getSlot":
				result = uint64(100)
			case "getAccountInfo":
				if len(request.Params) != 2 || !bytes.Contains(request.Params[1], []byte(`"commitment":"finalized"`)) {
					t.Error("inventory account read was not finalized")
					return
				}
				var queried string
				if err := json.Unmarshal(request.Params[0], &queried); err != nil {
					t.Error(err)
					return
				}
				program, lamports, data := orcaswap.SystemProgram, uint64(10000), []byte{}
				if queried == address {
					program, lamports, data = orcaswap.TokenProgram, 2039280, make([]byte, 165)
					mintKey, err := solana.Decode32(mint)
					if err != nil {
						t.Error(err)
						return
					}
					ownerKey, err := solana.Decode32(owner)
					if err != nil {
						t.Error(err)
						return
					}
					copy(data[:32], mintKey[:])
					copy(data[32:64], ownerKey[:])
					binary.LittleEndian.PutUint64(data[64:72], 200)
					data[108] = 1
				} else if queried != owner {
					t.Error("unexpected inventory address")
					return
				}
				result = map[string]any{"context": map[string]any{"slot": 101}, "value": map[string]any{
					"owner": program, "lamports": lamports, "space": len(data), "executable": false,
					"data": []any{base64.StdEncoding.EncodeToString(data), "base64"}}}
			default:
				t.Errorf("unexpected inventory RPC: %s", request.Method)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
				t.Error(err)
			}
		}))
		t.Cleanup(server.Close)
		client, err := solanarpc.New(server.URL, server.Client(), true)
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	a, b := newRPC(), newRPC()
	policy.JupiterProviders.PrimaryOriginSHA256 = a.Identity()
	policy.JupiterProviders.SecondaryOriginSHA256 = b.Identity()
	lifecycle, err := txflow.NewEvidenceLifecycle(a, b)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "inventory.jsonl")
	got, err := InitializeWalletInventory(t.Context(), path, policy, lifecycle, time.Now())
	if err != nil || got.Opening.NativeLamports != 10000 || got.Opening.TokenUnits != 200 || got.HeadSHA256 == "" {
		t.Fatalf("finalized RPC opening = %+v, %v", got, err)
	}
}

func TestWalletInventoryOpeningIsDurableAndConcurrent(t *testing.T) {
	policy, _ := paperTerminalRequest(t)
	_, owner, mint, err := walletInventoryBinding(policy)
	if err != nil {
		t.Fatal(err)
	}
	address, err := orcaswap.AssociatedTokenAddress(owner, mint)
	if err != nil {
		t.Fatal(err)
	}
	observation := txflow.WalletObservation{Owner: owner, TokenMint: mint, TokenAccount: address,
		GenesisHash: solana.MainnetBetaGenesisHash, PrimaryIdentity: policy.JupiterProviders.PrimaryOriginSHA256,
		SecondaryIdentity: policy.JupiterProviders.SecondaryOriginSHA256,
		NativeLamports:    9007199254740993, TokenUnits: 20, MinimumContextSlot: 100,
		MaximumContextSlot: 100 + proposalcheck.MaxEvidenceSlotSkew,
		NativePrimarySlot:  101, NativeSecondarySlot: 102, TokenPrimarySlot: 103, TokenSecondarySlot: 104}
	path := filepath.Join(t.TempDir(), "inventory.jsonl")
	now := time.Now().UTC()
	var calls atomic.Int32
	observe := func(gotOwner, gotMint string) (txflow.WalletObservation, error) {
		calls.Add(1)
		if gotOwner != owner || gotMint != mint {
			return txflow.WalletObservation{}, errors.New("unexpected identity")
		}
		return observation, nil
	}
	var results [8]WalletInventory
	var errs [8]error
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() { results[i], errs[i] = initializeWalletInventory(path, policy, now, observe) })
	}
	wg.Wait()
	var opening WalletInventory
	for i, result := range results {
		if errors.Is(errs[i], journal.ErrLocked) && result == (WalletInventory{}) {
			continue
		}
		if errs[i] != nil || result.Opening != observation || result.HeadSHA256 == "" {
			t.Fatalf("opening[%d] = %+v, %v", i, result, errs[i])
		}
		if opening.HeadSHA256 != "" && result != opening {
			t.Fatal("competing callers observed different openings")
		}
		opening = result
	}
	if calls.Load() != 1 || opening.HeadSHA256 == "" {
		t.Fatalf("opening observations = %d, want 1", calls.Load())
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(before, []byte(`"native_lamports":"9007199254740993"`)) {
		t.Fatal("opening balance lost integer precision")
	}
	again, err := initializeWalletInventory(path, policy, now.Add(time.Hour), func(string, string) (txflow.WalletObservation, error) {
		t.Fatal("restart attempted to replace opening balances")
		return txflow.WalletObservation{}, nil
	})
	if err != nil || again != opening {
		t.Fatalf("restart = %+v, %v", again, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("restart rewrote opening: %v", err)
	}

	changed := policy
	providers := *policy.JupiterProviders
	providers.PrimaryTrustDomain = "changed-provider"
	changed.JupiterProviders = &providers
	if _, err := initializeWalletInventory(path, changed, now, observe); err == nil {
		t.Fatal("changed protected provider binding reused opening")
	}
}

func TestWalletInventoryRejectsUnboundOpening(t *testing.T) {
	policy, _ := paperTerminalRequest(t)
	_, owner, mint, err := walletInventoryBinding(policy)
	if err != nil {
		t.Fatal(err)
	}
	address, err := orcaswap.AssociatedTokenAddress(owner, mint)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"error", "owner", "mint", "account", "provider", "genesis", "slot", "interval"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "inventory.jsonl")
			got, err := initializeWalletInventory(path, policy, time.Now(), func(string, string) (txflow.WalletObservation, error) {
				value := txflow.WalletObservation{Owner: owner, TokenMint: mint, TokenAccount: address,
					GenesisHash: solana.MainnetBetaGenesisHash, PrimaryIdentity: policy.JupiterProviders.PrimaryOriginSHA256,
					SecondaryIdentity:  policy.JupiterProviders.SecondaryOriginSHA256,
					MinimumContextSlot: 100, MaximumContextSlot: 100 + proposalcheck.MaxEvidenceSlotSkew,
					NativePrimarySlot: 100, NativeSecondarySlot: 100, TokenPrimarySlot: 100, TokenSecondarySlot: 100}
				switch name {
				case "error":
					return value, errors.New("provider unavailable")
				case "owner":
					value.Owner = "wrong"
				case "mint":
					value.TokenMint = orcaswap.WrappedSOLMint
				case "account":
					value.TokenAccount = owner
				case "provider":
					value.SecondaryIdentity = value.PrimaryIdentity
				case "genesis":
					value.GenesisHash = solana.DevnetGenesisHash
				case "slot":
					value.TokenPrimarySlot = 99
				case "interval":
					value.MaximumContextSlot++
				}
				return value, nil
			})
			if err == nil || got != (WalletInventory{}) {
				t.Fatalf("unbound opening = %+v, %v", got, err)
			}
			records, err := journal.ReadRecords(path)
			if err != nil || len(records) != 0 {
				t.Fatalf("failed opening persisted a record: %v", err)
			}
		})
	}
}

func TestWalletInventoryRefusesTornStateAndUninitializedEvidence(t *testing.T) {
	policy, _ := paperTerminalRequest(t)
	path := filepath.Join(t.TempDir(), "inventory.jsonl")
	if got, err := InitializeWalletInventory(t.Context(), path, policy, &txflow.Lifecycle{}, time.Now()); err == nil || got != (WalletInventory{}) {
		t.Fatalf("uninitialized lifecycle accepted: %+v, %v", got, err)
	}
	raw := []byte(`{"unfinished_order":`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := initializeWalletInventory(path, policy, time.Now(), func(string, string) (txflow.WalletObservation, error) {
		t.Fatal("torn journal triggered a replacement opening")
		return txflow.WalletObservation{}, nil
	}); err == nil || got != (WalletInventory{}) {
		t.Fatalf("torn inventory accepted: %+v, %v", got, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, raw) {
		t.Fatalf("torn evidence was changed: %v", err)
	}
}
