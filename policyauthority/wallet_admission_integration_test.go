package policyauthority_test

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
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

type walletAdmissionBuilder func(context.Context, jupiterquote.Request) (jupiterquote.BuildResult, error)

func (b walletAdmissionBuilder) Build(ctx context.Context, request jupiterquote.Request) (jupiterquote.BuildResult, error) {
	return b(ctx, request)
}

type unavailableAdmissionReserve struct {
	proposalcheck.NativeReserveEvidence
}

func (unavailableAdmissionReserve) VerifyNativeReserve(context.Context, string, uint64, uint64, uint64, uint64) (txflow.AccountEvidence, error) {
	return txflow.AccountEvidence{}, errors.New("injected native reserve outage after acquisition")
}

func TestPublicWalletAdmissionPreparesAndRecoversExactRequest(t *testing.T) {
	testPublicWalletAdmission(t, "prepare")
}

func TestPublicWalletAcquisitionPreparesAndRecoversExactRequest(t *testing.T) {
	for _, mode := range []string{"acquire", "acquire then reserve outage", "budget fee", "budget zero reserve", "budget below reserve", "budget costs", "budget input"} {
		t.Run(mode, func(t *testing.T) { testPublicWalletAdmission(t, mode) })
	}
}

func testPublicWalletAdmission(t *testing.T, mode string) {
	const native = uint64(20_000_000)
	var owner, mint, account string
	var requests atomic.Int32
	newRPC := func() *solanarpc.Client {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
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
				if len(request.Params) != 1 || !bytes.Contains(request.Params[0], []byte(`"commitment":"finalized"`)) {
					t.Error("nonfinalized slot read")
					return
				}
				result = uint64(101)
			case "getAccountInfo":
				if len(request.Params) != 2 || !bytes.Contains(request.Params[1], []byte(`"commitment":"finalized"`)) {
					t.Error("nonfinalized account read")
					return
				}
				var address string
				if err := json.Unmarshal(request.Params[0], &address); err != nil {
					t.Error(err)
					return
				}
				program, lamports, data := orcaswap.SystemProgram, native, []byte{}
				if address == account {
					program, lamports, data = orcaswap.TokenProgram, 2_039_280, make([]byte, 165)
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
					binary.LittleEndian.PutUint64(data[64:72], 100)
					data[108] = 1
				} else if address != owner {
					t.Error("unexpected wallet address")
					return
				}
				result = map[string]any{"context": map[string]any{"slot": 101}, "value": map[string]any{
					"owner": program, "lamports": lamports, "space": len(data), "executable": false, "data": []any{base64.StdEncoding.EncodeToString(data), "base64"}}}
			default:
				t.Errorf("unexpected wallet RPC %q", request.Method)
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
		return client
	}
	primary, secondary := newRPC(), newRPC()
	f := policyauthority.NewWalletAdmissionFixture(t, primary.Identity(), secondary.Identity(), native)
	if mode != "prepare" {
		f.Now = time.Now().UTC()
		window := int64(f.Policy.TransactionPolicy.ScheduleWindowSeconds)
		anchor := f.Policy.TransactionPolicy.ScheduleAnchorUnix
		f.Start = anchor + (f.Now.Unix()-anchor)/window*window
		f.Ticks[0].At = f.Now
		f.Ticks[0].DecisionQuote.ReceivedAt = f.Now
		var err error
		f.Bounds.EvidenceSHA256, err = proposalcheck.PaperEvidenceSHA256(f.Ticks)
		if err != nil {
			t.Fatal(err)
		}
	}
	owner, mint = f.Candidate.Policy.Owner, f.Candidate.Policy.OutputMint
	var err error
	account, err = orcaswap.AssociatedTokenAddress(owner, mint)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := txflow.NewEvidenceLifecycle(primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "wallet.jsonl")
	opening, err := execution.InitializeWalletInventory(t.Context(), path, f.Policy, lifecycle, f.Now.Add(-time.Hour))
	if err != nil || opening.NativeLamports != native || opening.TokenUnits != 100 {
		t.Fatalf("opening: %+v, %v", opening, err)
	}
	var got execution.WalletPaperClaim
	var builderCalls int
	if mode == "prepare" {
		got, err = execution.PrepareWalletPaperClaim(t.Context(), path, f.Policy, f.Paper, f.Ticks, f.Bounds, f.Candidate,
			f.Start, f.Now, f.MaxDecisionAge, f.AcquisitionPath, f.MaxAcquisitionAge, f.Evidence, f.Primary, f.Secondary, lifecycle)
	} else {
		proposal := policyauthority.WalletAdmissionBuildForTest(t, f.Candidate)
		builder := walletAdmissionBuilder(func(_ context.Context, request jupiterquote.Request) (jupiterquote.BuildResult, error) {
			builderCalls++
			if builderCalls != 1 || request != f.Candidate.Request {
				t.Fatal("acquisition repeated or changed request")
			}
			proposal.Quote.ReceivedAt = time.Now().UTC()
			proposal.Quote.ResponseSHA256 = strings.Repeat("a", 64)
			return proposal, nil
		})
		acquire := func(evidence proposalcheck.NativeReserveEvidence) (execution.WalletPaperClaim, error) {
			return execution.AcquireWalletPaperClaim(t.Context(), path, f.Policy, f.Paper, f.Ticks, f.Bounds, f.Candidate.Request,
				f.Start, time.Now().UTC(), f.MaxDecisionAge, f.MaxAcquisitionAge, builder, evidence, f.Primary, f.Secondary, lifecycle)
		}
		if strings.HasPrefix(mode, "budget ") {
			route := f.Policy.TransactionPolicy.Jupiter
			switch mode {
			case "budget fee":
				f.Paper.FeeLamports = route.MaxFeeLamports - 1
			case "budget zero reserve":
				f.Bounds.ReserveLamports = 0
			case "budget below reserve":
				f.Bounds.NativeBudgetLamports = f.Bounds.ReserveLamports - 1
			case "budget costs":
				f.Bounds.NativeBudgetLamports = f.Bounds.ReserveLamports + route.MaxFeeLamports + 2*route.MaxTokenAccountRentLamports - 1
			case "budget input":
				f.Bounds.NativeBudgetLamports = f.Bounds.ReserveLamports + route.MaxFeeLamports + 2*route.MaxTokenAccountRentLamports + f.Candidate.Request.InputAmount - 1
			}
			if result, err := acquire(f.Evidence); err == nil || !reflect.DeepEqual(result, execution.WalletPaperClaim{}) || builderCalls != 0 {
				t.Fatalf("invalid budget reached acquisition: %+v, %v, calls=%d", result, err, builderCalls)
			}
			if _, err := os.Lstat(path + ".claim-" + opening.HeadSHA256 + ".jsonl.acquisition.jsonl"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid budget created acquisition: %v", err)
			}
			return
		}
		var acquisitionBefore []byte
		acquisitionPath := path + ".claim-" + opening.HeadSHA256 + ".jsonl.acquisition.jsonl"
		if mode == "acquire then reserve outage" {
			if result, err := acquire(unavailableAdmissionReserve{f.Evidence}); err == nil || !reflect.DeepEqual(result, execution.WalletPaperClaim{}) {
				t.Fatalf("injected claim failure: %+v, %v", result, err)
			}
			acquisitionBefore, err = os.ReadFile(acquisitionPath)
			if err != nil {
				t.Fatal(err)
			}
			state, err := execution.ReadWalletInventory(path, f.Policy)
			if err != nil || state != opening {
				t.Fatalf("outage changed wallet: %+v, %v", state, err)
			}
		}
		got, err = acquire(f.Evidence)
		if err == nil {
			retained, readErr := proposalcheck.ReadAcquiredCandidate(acquisitionPath, time.Now().UTC(), f.MaxAcquisitionAge)
			if readErr != nil || !reflect.DeepEqual(retained, *got.Request.JupiterCandidate) {
				t.Fatalf("retained acquisition differs: %v", readErr)
			}
			if acquisitionBefore != nil {
				after, readErr := os.ReadFile(acquisitionPath)
				if readErr != nil || !bytes.Equal(after, acquisitionBefore) {
					t.Fatal("retry renewed acquisition")
				}
			}
			if builderCalls != 1 {
				t.Fatalf("builder calls=%d", builderCalls)
			}
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if got.Recovered || got.Inventory.PendingSHA256 == "" || got.ClaimPath != path+".claim-"+opening.HeadSHA256+".jsonl" ||
		got.Request.RiskGrant != (riskgrant.Grant{}) || got.Inventory.Pending.ActionID != got.Request.ActionID ||
		got.Inventory.NativeLamports != native || got.Inventory.TokenUnits != 100 {
		t.Fatalf("public admission: %+v", got)
	}
	retained, err := policyauthority.ReadClaimedPaperRequest(got.ClaimPath, f.Policy)
	if err != nil || !reflect.DeepEqual(retained, got.Request) {
		t.Fatalf("retained request mismatch: %v", err)
	}
	claims, err := journal.ReadRecords(got.ClaimPath)
	if err != nil || len(claims) != 1 || claims[0].Hash != got.Inventory.Pending.ClaimSHA256 {
		t.Fatalf("claim not bound to reservation: %v", err)
	}
	before := make(map[string][]byte)
	for _, file := range []string{path, got.ClaimPath} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		before[file] = raw
	}
	rpcCount := requests.Load()
	if rpcCount < 12 {
		t.Fatalf("initialization and reservation did not both read independent wallets: %d", rpcCount)
	}
	expired := time.Unix(got.Request.ScheduleWindowEndUnix, 0).UTC().Add(time.Hour)
	recovered, err := execution.PrepareWalletPaperClaim(t.Context(), path, f.Policy, shadow.Policy{}, nil, proposalcheck.PaperIntentBounds{},
		proposalcheck.Candidate{}, 0, expired, 0, "", 0, nil, nil, nil, nil)
	if err != nil || !recovered.Recovered || recovered.ClaimPath != got.ClaimPath || recovered.Inventory != got.Inventory || !reflect.DeepEqual(recovered.Request, got.Request) {
		t.Fatalf("recovery: %+v, %v", recovered, err)
	}
	if requests.Load() != rpcCount {
		t.Fatal("recovery queried wallet RPC")
	}
	if mode != "prepare" {
		acquired, err := execution.AcquireWalletPaperClaim(t.Context(), path, f.Policy, shadow.Policy{}, nil, proposalcheck.PaperIntentBounds{},
			jupiterquote.Request{}, 0, expired, 0, 0, nil, nil, nil, nil, nil)
		if err != nil || !acquired.Recovered || !reflect.DeepEqual(acquired.Request, got.Request) || requests.Load() != rpcCount || builderCalls != 1 {
			t.Fatalf("acquire recovery used new inputs: %+v, %v", acquired, err)
		}
	}
	for file, want := range before {
		raw, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(raw, want) {
			t.Fatal("recovery changed original bytes")
		}
	}
	if _, err := os.Stat(f.Policy.TransactionPolicy.AuthorizationLedgerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsigned admission touched signer ledger: %v", err)
	}
}
