package solanarpc

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestPacedClientWaitsBetweenRequests(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var input struct {
			ID uint64 `json:"id"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": input.ID, "result": solana.DevnetGenesisHash,
		})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
	const interval = 250 * time.Millisecond
	client, err := NewPaced("https://rpc.invalid", httpClient, interval)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	client.now = func() time.Time { return now }
	var slept time.Duration
	client.sleep = func(_ context.Context, delay time.Duration) error {
		slept += delay
		now = now.Add(delay)
		return nil
	}
	if _, err := client.GenesisHash(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GenesisHash(t.Context()); err != nil {
		t.Fatal(err)
	}
	if slept != interval {
		t.Fatalf("paced client slept %s, want %s", slept, interval)
	}
}

func TestPacedClientRejectsInvalidIntervals(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second, maxRequestInterval + time.Nanosecond} {
		if _, err := NewPaced("https://rpc.invalid", nil, interval); err == nil {
			t.Fatalf("NewPaced accepted interval %s", interval)
		}
	}
}

func TestClientRPCMethods(t *testing.T) {
	transaction, signature := rpcTestTransfer(t)
	blockhash := solana.Encode(bytes.Repeat([]byte{5}, 32))
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var input struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      uint64          `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		var result any
		switch input.Method {
		case "getGenesisHash":
			result = solana.DevnetGenesisHash
		case "getLatestBlockhash":
			assertContains(t, input.Params, `"commitment":"confirmed"`)
			assertContains(t, input.Params, `"minContextSlot":80`)
			result = map[string]any{
				"context": map[string]any{"slot": uint64(90)},
				"value": map[string]any{
					"blockhash":            blockhash,
					"lastValidBlockHeight": uint64(120),
				},
			}
		case "getBlockHeight":
			assertContains(t, input.Params, `"commitment":"finalized"`)
			result = uint64(100)
		case "getSlot":
			assertContains(t, input.Params, `"commitment":"finalized"`)
			result = uint64(89)
		case "getAccountInfo":
			assertContains(t, input.Params, `"commitment":"finalized"`)
			assertContains(t, input.Params, `"encoding":"base64"`)
			assertContains(t, input.Params, `"minContextSlot":90`)
			result = map[string]any{
				"context": map[string]any{"slot": uint64(91)},
				"value": map[string]any{
					"data":       []any{"", "base64"},
					"executable": false,
					"lamports":   uint64(1_000_000),
					"owner":      solana.Encode(make([]byte, 32)),
				},
			}
		case "getFeeForMessage":
			assertContains(t, input.Params, `"commitment":"confirmed"`)
			assertContains(t, input.Params, `"minContextSlot":90`)
			decoded, err := solana.DecodeSignedTransfer(transaction)
			if err != nil {
				t.Fatal(err)
			}
			assertContains(t, input.Params, base64.StdEncoding.EncodeToString(decoded.Message))
			result = map[string]any{
				"context": map[string]any{"slot": uint64(90)},
				"value":   uint64(5_000),
			}
		case "simulateTransaction":
			assertContains(t, input.Params, `"commitment":"confirmed"`)
			assertContains(t, input.Params, `"encoding":"base64"`)
			assertContains(t, input.Params, `"sigVerify":false`)
			assertContains(t, input.Params, `"replaceRecentBlockhash":false`)
			assertContains(t, input.Params, `"minContextSlot":90`)
			decoded, err := solana.DecodeSignedTransfer(transaction)
			if err != nil {
				t.Fatal(err)
			}
			assertContains(t, input.Params, `"addresses":["`+solana.Encode(decoded.Source[:])+`","`+
				solana.Encode(decoded.Destination[:])+`"]`)
			result = map[string]any{
				"context": map[string]any{"slot": uint64(91)},
				"value": map[string]any{
					"err":           nil,
					"unitsConsumed": uint64(150),
					"logs":          []string{"program success"},
					"accounts": []any{
						simulatedSystemAccount(900),
						simulatedSystemAccount(50),
					},
				},
			}
		case "sendTransaction":
			assertContains(t, input.Params, `"skipPreflight":false`)
			assertContains(t, input.Params, `"preflightCommitment":"confirmed"`)
			assertContains(t, input.Params, `"maxRetries":0`)
			assertContains(t, input.Params, `"minContextSlot":90`)
			assertContains(t, input.Params, base64.StdEncoding.EncodeToString(transaction))
			result = signature
		case "getSignatureStatuses":
			assertContains(t, input.Params, `"searchTransactionHistory":true`)
			result = map[string]any{"value": []any{map[string]any{
				"slot":               uint64(110),
				"err":                nil,
				"confirmationStatus": "confirmed",
			}}}
		case "getTransaction":
			assertContains(t, input.Params, `"commitment":"finalized"`)
			assertContains(t, input.Params, `"encoding":"base64"`)
			assertContains(t, input.Params, `"maxSupportedTransactionVersion":0`)
			result = map[string]any{
				"slot": uint64(110),
				"meta": map[string]any{
					"err":          nil,
					"fee":          uint64(5_000),
					"preBalances":  []uint64{100_000, 50_000, 1},
					"postBalances": []uint64{94_999, 50_001, 1},
				},
				"transaction": []any{base64.StdEncoding.EncodeToString(transaction), "base64"},
				"version":     "legacy",
			}
		default:
			t.Fatalf("unexpected method %q", input.Method)
		}
		return jsonResponse(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      input.ID,
			"result":  result,
		}), nil
	})}
	client, err := New("https://rpc.test", httpClient, false)
	if err != nil {
		t.Fatal(err)
	}
	genesis, err := client.GenesisHash(t.Context())
	if err != nil || genesis != solana.DevnetGenesisHash {
		t.Fatalf("genesis hash = %q, %v", genesis, err)
	}
	latest, err := client.LatestBlockhash(t.Context(), 80)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ContextSlot != 90 || latest.Blockhash != blockhash || latest.LastValidBlockHeight != 120 {
		t.Fatalf("unexpected latest blockhash: %+v", latest)
	}
	height, err := client.BlockHeight(t.Context())
	if err != nil || height != 100 {
		t.Fatalf("block height = %d, %v", height, err)
	}
	finalizedSlot, err := client.FinalizedSlot(t.Context())
	if err != nil || finalizedSlot != 89 {
		t.Fatalf("finalized slot = %d, %v", finalizedSlot, err)
	}
	address := solana.Encode(bytes.Repeat([]byte{8}, 32))
	account, err := client.Account(t.Context(), address, 90)
	if err != nil || account != (AccountQuote{
		ContextSlot: 91,
		Lamports:    1_000_000,
		Owner:       solana.Encode(make([]byte, 32)),
	}) {
		t.Fatalf("account = %+v, %v", account, err)
	}
	decoded, err := solana.DecodeSignedTransfer(transaction)
	if err != nil {
		t.Fatal(err)
	}
	fee, err := client.FeeForMessage(t.Context(), decoded.Message, 90)
	if err != nil || fee != (FeeQuote{ContextSlot: 90, Lamports: 5_000}) {
		t.Fatalf("message fee = %+v, %v", fee, err)
	}
	simulation, err := client.SimulateTransfer(t.Context(), decoded.Message, 90)
	if err != nil || simulation.ContextSlot != 91 || simulation.UnitsConsumed != 150 ||
		simulation.SourcePostLamports != 900 ||
		simulation.DestinationPostLamports != 50 ||
		len(simulation.LogsSHA256) != 64 || len(simulation.AccountsSHA256) != 64 {
		t.Fatalf("simulation = %+v, %v", simulation, err)
	}
	returned, err := client.SendTransaction(t.Context(), transaction, 90)
	if err != nil || returned != signature {
		t.Fatalf("send = %q, %v", returned, err)
	}
	status, err := client.SignatureStatus(t.Context(), signature)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Found || status.Slot != 110 || status.ConfirmationStatus != "confirmed" || status.Failed {
		t.Fatalf("unexpected status: %+v", status)
	}
	effect, err := client.TransactionEffect(t.Context(), signature)
	if err != nil {
		t.Fatal(err)
	}
	if effect.Slot != 110 || effect.Failed || effect.FeeLamports != 5_000 ||
		!bytes.Equal(effect.Transaction, transaction) ||
		!slices.Equal(effect.PreBalances, []uint64{100_000, 50_000, 1}) ||
		!slices.Equal(effect.PostBalances, []uint64{94_999, 50_001, 1}) {
		t.Fatalf("unexpected effect: %+v", effect)
	}
}

func TestMithrilNodeUsesProcessedCommitment(t *testing.T) {
	transaction, signature := rpcTestTransfer(t)
	decoded, err := solana.DecodeSignedTransfer(transaction)
	if err != nil {
		t.Fatal(err)
	}
	blockhash := solana.Encode(bytes.Repeat([]byte{5}, 32))
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var input struct {
			ID     uint64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		var result any
		switch input.Method {
		case "getLatestBlockhash":
			assertContains(t, input.Params, `"commitment":"processed"`)
			assertContains(t, input.Params, `"minContextSlot":80`)
			result = map[string]any{
				"context": map[string]any{"slot": uint64(90)},
				"value": map[string]any{
					"blockhash":            blockhash,
					"lastValidBlockHeight": uint64(120),
				},
			}
		case "getBlockHeight":
			assertContains(t, input.Params, `"commitment":"processed"`)
			result = uint64(100)
		case "simulateTransaction":
			assertContains(t, input.Params, `"commitment":"processed"`)
			result = map[string]any{
				"context": map[string]any{"slot": uint64(90)},
				"value": map[string]any{
					"err":           nil,
					"unitsConsumed": uint64(150),
					"logs":          []string{"program success"},
					"accounts": []any{
						simulatedSystemAccount(900),
						simulatedSystemAccount(50),
					},
				},
			}
		case "sendTransaction":
			assertContains(t, input.Params, `"preflightCommitment":"processed"`)
			assertContains(t, input.Params, `"minContextSlot":90`)
			result = signature
		default:
			t.Fatalf("unexpected method %q", input.Method)
		}
		return jsonResponse(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      input.ID,
			"result":  result,
		}), nil
	})}
	client, err := NewMithrilNode("http://127.0.0.1:8899", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.LatestBlockhash(t.Context(), 80); err != nil {
		t.Fatal(err)
	}
	if _, err := client.BlockHeight(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SimulateTransfer(t.Context(), decoded.Message, 90); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendTransaction(t.Context(), transaction, 90); err != nil {
		t.Fatal(err)
	}
}

func TestClientRejectsDuplicateResponseKeys(t *testing.T) {
	client, err := New("https://rpc.test", &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			body := `{"jsonrpc":"2.0","id":1,"result":"first","Result":"second"}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GenesisHash(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate RPC response error = %v", err)
	}
}

func TestLatestBlockhashRejectsContextBeforeMinimum(t *testing.T) {
	blockhash := solana.Encode(bytes.Repeat([]byte{7}, 32))
	client, err := New("https://rpc.test", &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(t, map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"result": map[string]any{
					"context": map[string]any{"slot": uint64(79)},
					"value": map[string]any{
						"blockhash": blockhash, "lastValidBlockHeight": uint64(120),
					},
				},
			}), nil
		}),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.LatestBlockhash(t.Context(), 80); err == nil {
		t.Fatal("latest blockhash before minContextSlot was accepted")
	}
}

func TestSimulationRejectsFailureAndStaleContext(t *testing.T) {
	transaction, _ := rpcTestTransfer(t)
	decoded, err := solana.DecodeSignedTransfer(transaction)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []any{
		map[string]any{
			"context": map[string]any{"slot": uint64(8)},
			"value":   map[string]any{"err": nil},
		},
		map[string]any{
			"context": map[string]any{"slot": uint64(9)},
			"value":   map[string]any{"err": map[string]any{"InstructionError": []any{0, "Failure"}}},
		},
		map[string]any{
			"context": map[string]any{"slot": uint64(9)},
			"value":   map[string]any{},
		},
	} {
		client := newTestClient(t, func(id uint64) any {
			return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
		})
		if _, err := client.SimulateTransfer(t.Context(), decoded.Message, 9); err == nil {
			t.Fatalf("invalid simulation was accepted: %#v", result)
		}
	}
}

func TestSimulationRejectsUnboundedOrUntypedEvidence(t *testing.T) {
	transaction, _ := rpcTestTransfer(t)
	decoded, err := solana.DecodeSignedTransfer(transaction)
	if err != nil {
		t.Fatal(err)
	}
	tooManyLogs := make([]string, maxSimulationLogLines+1)
	nonSystem := simulatedSystemAccount(900)
	nonSystem["owner"] = solana.Encode(bytes.Repeat([]byte{9}, 32))
	tests := []map[string]any{
		{
			"err":      nil,
			"logs":     tooManyLogs,
			"accounts": []any{simulatedSystemAccount(900), simulatedSystemAccount(50)},
		},
		{
			"err":      nil,
			"logs":     []string{"program success"},
			"accounts": []any{nonSystem, simulatedSystemAccount(50)},
		},
		{
			"err":      nil,
			"logs":     []string{"program success"},
			"accounts": []any{simulatedSystemAccount(900)},
		},
	}
	for _, value := range tests {
		value["unitsConsumed"] = uint64(150)
		client := newTestClient(t, func(id uint64) any {
			return map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"context": map[string]any{"slot": uint64(9)},
					"value":   value,
				},
			}
		})
		if _, err := client.SimulateTransfer(t.Context(), decoded.Message, 9); err == nil {
			t.Fatalf("invalid simulation evidence was accepted: %#v", value)
		}
	}
}

func TestTransactionEffectRejectsIncompleteOrInvalidEvidence(t *testing.T) {
	transaction, signature := rpcTestTransfer(t)
	validMeta := map[string]any{
		"err":          nil,
		"fee":          uint64(5_000),
		"preBalances":  []uint64{100_000, 50_000, 1},
		"postBalances": []uint64{94_999, 50_001, 1},
	}
	for _, result := range []any{
		map[string]any{"slot": uint64(9), "transaction": []any{base64.StdEncoding.EncodeToString(transaction), "base64"}},
		map[string]any{"slot": uint64(9), "meta": validMeta, "transaction": []any{"bad", "base64"}},
		map[string]any{"slot": uint64(9), "meta": validMeta, "transaction": []any{base64.StdEncoding.EncodeToString(transaction), "json"}},
		map[string]any{
			"slot": uint64(9),
			"meta": map[string]any{
				"err":          nil,
				"fee":          uint64(5_000),
				"preBalances":  []uint64{1},
				"postBalances": []uint64{1, 2},
			},
			"transaction": []any{base64.StdEncoding.EncodeToString(transaction), "base64"},
		},
	} {
		client := newTestClient(t, func(id uint64) any {
			return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
		})
		if _, err := client.TransactionEffect(t.Context(), signature); err == nil {
			t.Fatalf("invalid transaction effect was accepted: %#v", result)
		}
	}
}

func TestFeeForMessageRejectsIncompleteResponse(t *testing.T) {
	transaction, _ := rpcTestTransfer(t)
	decoded, err := solana.DecodeSignedTransfer(transaction)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []any{
		map[string]any{"context": map[string]any{"slot": uint64(9)}, "value": nil},
		map[string]any{"context": map[string]any{"slot": uint64(9)}, "value": uint64(0)},
		map[string]any{"context": map[string]any{"slot": uint64(0)}, "value": uint64(5_000)},
	} {
		client := newTestClient(t, func(id uint64) any {
			return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
		})
		if _, err := client.FeeForMessage(t.Context(), decoded.Message, 9); err == nil {
			t.Fatalf("incomplete fee response was accepted: %#v", result)
		}
	}
	if _, err := newTestClient(t, func(id uint64) any {
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]any{
				"context": map[string]any{"slot": uint64(8)},
				"value":   uint64(5_000),
			},
		}
	}).FeeForMessage(t.Context(), decoded.Message, 9); err == nil {
		t.Fatal("stale fee context was accepted")
	}
}

func TestFeeForMessageAcceptsValidatedNonTransferLegacyMessage(t *testing.T) {
	owner := solana.Encode(bytes.Repeat([]byte{4}, 32))
	blockhash := solana.Encode(bytes.Repeat([]byte{5}, 32))
	message, err := solana.BuildLegacyMessage(owner, blockhash, []solana.Instruction{{
		Program: "MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr",
		Data:    []byte("fee-check"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	client := newTestClient(t, func(id uint64) any {
		return map[string]any{
			"jsonrpc": "2.0", "id": id,
			"result": map[string]any{
				"context": map[string]any{"slot": uint64(9)},
				"value":   uint64(5_000),
			},
		}
	})
	fee, err := client.FeeForMessage(t.Context(), message, 9)
	if err != nil {
		t.Fatal(err)
	}
	if fee.Lamports != 5_000 || fee.ContextSlot != 9 {
		t.Fatalf("fee = %+v", fee)
	}
}

func TestAccountRequiresValidAddressAndFreshContext(t *testing.T) {
	address := solana.Encode(bytes.Repeat([]byte{8}, 32))
	account := func(lamports uint64) any {
		return map[string]any{
			"data":       []any{"", "base64"},
			"executable": false,
			"lamports":   lamports,
			"owner":      solana.Encode(make([]byte, 32)),
		}
	}
	client := newTestClient(t, func(id uint64) any {
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]any{
				"context": map[string]any{"slot": uint64(8)},
				"value":   account(0),
			},
		}
	})
	if _, err := client.Account(t.Context(), address, 9); err == nil {
		t.Fatal("stale account context was accepted")
	}
	if _, err := client.Account(t.Context(), "invalid", 9); err == nil {
		t.Fatal("invalid account address was accepted")
	}

	zero := newTestClient(t, func(id uint64) any {
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]any{
				"context": map[string]any{"slot": uint64(9)},
				"value":   account(0),
			},
		}
	})
	quote, err := zero.Account(t.Context(), address, 9)
	if err != nil || quote.Lamports != 0 {
		t.Fatalf("zero-lamport account = %+v, %v", quote, err)
	}
}

func TestAccountSliceUsesBoundedConfirmedRead(t *testing.T) {
	address := solana.Encode(bytes.Repeat([]byte{8}, 32))
	owner := solana.Encode(bytes.Repeat([]byte{9}, 32))
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var input struct {
			ID     uint64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Method != "getAccountInfo" {
			t.Fatalf("method = %q", input.Method)
		}
		assertContains(t, input.Params, `"commitment":"confirmed"`)
		assertContains(t, input.Params, `"minContextSlot":90`)
		assertContains(t, input.Params, `"dataSlice":{"length":4,"offset":12}`)
		return jsonResponse(t, map[string]any{
			"jsonrpc": "2.0", "id": input.ID,
			"result": map[string]any{
				"context": map[string]any{"slot": uint64(91)},
				"value": map[string]any{
					"data":       []any{base64.StdEncoding.EncodeToString([]byte{1, 2, 3, 4}), "base64"},
					"executable": true, "lamports": uint64(1), "owner": owner, "space": uint64(165),
				},
			},
		}), nil
	})}
	client, err := New("https://rpc.test", httpClient, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.AccountSlice(t.Context(), address, 90, 12, 4)
	if err != nil || got.ContextSlot != 91 || got.Owner != owner || !got.Executable ||
		got.DataLength != 165 ||
		!bytes.Equal(got.Data, []byte{1, 2, 3, 4}) {
		t.Fatalf("account slice = %+v, %v", got, err)
	}
	for name, args := range map[string][4]uint64{
		"zero context": {0, 0, 1, 0},
		"zero length":  {1, 0, 0, 0},
		"long slice":   {1, 0, 513, 0},
		"overflow":     {1, ^uint64(0), 2, 0},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.AccountSlice(
				t.Context(), address, args[0], args[1], args[2],
			); err == nil {
				t.Fatal("invalid account slice was accepted")
			}
		})
	}
	if _, err := client.AccountSlice(t.Context(), "invalid", 1, 0, 1); err == nil {
		t.Fatal("invalid account address was accepted")
	}
}

func TestAccountSliceRejectsMalformedEvidence(t *testing.T) {
	address := solana.Encode(bytes.Repeat([]byte{8}, 32))
	owner := solana.Encode(bytes.Repeat([]byte{9}, 32))
	validValue := func() map[string]any {
		return map[string]any{
			"data":       []any{base64.StdEncoding.EncodeToString([]byte{1, 2, 3, 4}), "base64"},
			"executable": false, "lamports": uint64(1), "owner": owner,
		}
	}
	tests := map[string]struct {
		slot  uint64
		value any
	}{
		"stale":          {8, validValue()},
		"missing":        {9, nil},
		"wrong shape":    {9, map[string]any{"data": "bad", "owner": owner}},
		"bad base64":     {9, map[string]any{"data": []any{"%%%", "base64"}, "owner": owner}},
		"wrong encoding": {9, map[string]any{"data": []any{"AQIDBA==", "base58"}, "owner": owner}},
		"short data":     {9, map[string]any{"data": []any{"AQID", "base64"}, "owner": owner}},
		"invalid owner":  {9, map[string]any{"data": []any{"AQIDBA==", "base64"}, "owner": "invalid"}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			client := newTestClient(t, func(id uint64) any {
				return map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
					"context": map[string]any{"slot": test.slot}, "value": test.value,
				}}
			})
			if _, err := client.AccountSlice(t.Context(), address, 9, 0, 4); err == nil {
				t.Fatal("malformed account slice was accepted")
			}
		})
	}
}

func TestMinimumBalanceForRentExemption(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var input struct {
			ID     uint64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Method != "getMinimumBalanceForRentExemption" {
			t.Fatalf("method = %q", input.Method)
		}
		assertContains(t, input.Params, `[165,{"commitment":"confirmed"}]`)
		return jsonResponse(t, map[string]any{
			"jsonrpc": "2.0", "id": input.ID, "result": uint64(2_039_280),
		}), nil
	})}
	client, err := New("https://rpc.test", httpClient, false)
	if err != nil {
		t.Fatal(err)
	}
	lamports, err := client.MinimumBalanceForRentExemption(t.Context(), 165)
	if err != nil || lamports != 2_039_280 {
		t.Fatalf("rent = %d, %v", lamports, err)
	}
	if _, err := client.MinimumBalanceForRentExemption(t.Context(), 0); err == nil {
		t.Fatal("zero account size was accepted")
	}
	if _, err := client.MinimumBalanceForRentExemption(t.Context(), 10<<20+1); err == nil {
		t.Fatal("unbounded account size was accepted")
	}
	zero := newTestClient(t, func(id uint64) any {
		return map[string]any{"jsonrpc": "2.0", "id": id, "result": uint64(0)}
	})
	if _, err := zero.MinimumBalanceForRentExemption(t.Context(), 165); err == nil {
		t.Fatal("zero rent was accepted")
	}
}

func TestSignatureStatusCanonicalizesFailure(t *testing.T) {
	_, signature := rpcTestTransfer(t)
	client := newTestClient(t, func(id uint64) any {
		return map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"value": []any{map[string]any{
				"slot":               uint64(9),
				"err":                map[string]any{"InstructionError": []any{0, "Custom"}},
				"confirmationStatus": "finalized",
			}},
		}}
	})
	status, err := client.SignatureStatus(t.Context(), signature)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Failed || len(status.ErrorFingerprint) != 64 {
		t.Fatalf("failure was not fingerprinted: %+v", status)
	}
}

func TestSignatureStatusRequiresExecutionResult(t *testing.T) {
	_, signature := rpcTestTransfer(t)
	client := newTestClient(t, func(id uint64) any {
		return map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"value": []any{map[string]any{
				"slot":               uint64(9),
				"confirmationStatus": "confirmed",
			}},
		}}
	})
	if _, err := client.SignatureStatus(t.Context(), signature); err == nil {
		t.Fatal("status without execution result was accepted")
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	calls := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect,
			Header:     http.Header{"Location": []string{"https://redirected.example"}},
			Body:       io.NopCloser(strings.NewReader("redirect")),
		}, nil
	})}
	client, err := New("https://rpc.test/?key=private", httpClient, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.BlockHeight(t.Context()); err == nil {
		t.Fatal("redirect response was accepted")
	}
	if calls != 1 {
		t.Fatalf("redirect triggered %d requests, want 1", calls)
	}
}

func TestReadRetriesBoundedTransientResponses(t *testing.T) {
	fixedNow := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	var calls int
	var ids []uint64
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		var input struct {
			ID uint64 `json:"id"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, input.ID)
		switch calls {
		case 1:
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Retry-After": []string{"30"}},
				Body:       io.NopCloser(strings.NewReader("credential-bearing provider body")),
			}, nil
		case 2:
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header: http.Header{
					"Retry-After": []string{fixedNow.Add(time.Second).Format(http.TimeFormat)},
				},
				Body: io.NopCloser(strings.NewReader("temporary")),
			}, nil
		default:
			return jsonResponse(t, map[string]any{
				"jsonrpc": "2.0",
				"id":      input.ID,
				"result":  uint64(42),
			}), nil
		}
	})}
	client, err := New("https://rpc.test/?key=private", httpClient, false)
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return fixedNow }
	var delays []time.Duration
	client.sleep = func(ctx context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return ctx.Err()
	}

	height, err := client.BlockHeight(t.Context())
	if err != nil || height != 42 {
		t.Fatalf("block height = %d, %v", height, err)
	}
	if calls != maxRPCAttempts {
		t.Fatalf("calls = %d, want %d", calls, maxRPCAttempts)
	}
	if !slices.Equal(ids, []uint64{ids[0], ids[0], ids[0]}) {
		t.Fatalf("retry IDs changed: %v", ids)
	}
	if !slices.Equal(delays, []time.Duration{maxRetryDelay, time.Second}) {
		t.Fatalf("retry delays = %v", delays)
	}
}

func TestReadRetriesSafeTransportFailureOnly(t *testing.T) {
	tests := []struct {
		name      string
		firstErr  error
		wantCalls int
		wantOK    bool
	}{
		{name: "connection reset", firstErr: syscall.ECONNRESET, wantCalls: 2, wantOK: true},
		{name: "permanent transport error", firstErr: errors.New("certificate rejected"), wantCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return nil, test.firstErr
				}
				var input struct {
					ID uint64 `json:"id"`
				}
				if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				return jsonResponse(t, map[string]any{
					"jsonrpc": "2.0",
					"id":      input.ID,
					"result":  uint64(42),
				}), nil
			})}
			client, err := New("https://rpc.test/?key=private", httpClient, false)
			if err != nil {
				t.Fatal(err)
			}
			client.sleep = func(context.Context, time.Duration) error { return nil }
			height, callErr := client.BlockHeight(t.Context())
			if test.wantOK {
				if callErr != nil || height != 42 {
					t.Fatalf("block height = %d, %v", height, callErr)
				}
			} else if callErr == nil {
				t.Fatal("permanent transport error was accepted")
			}
			if calls != test.wantCalls {
				t.Fatalf("calls = %d, want %d", calls, test.wantCalls)
			}
		})
	}
}

func TestReadRetryLimitDoesNotExposeProviderData(t *testing.T) {
	calls := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("credential-bearing provider body")),
		}, nil
	})}
	client, err := New("https://rpc.test/?key=private", httpClient, false)
	if err != nil {
		t.Fatal(err)
	}
	client.sleep = func(context.Context, time.Duration) error { return nil }
	_, err = client.BlockHeight(t.Context())
	if err == nil {
		t.Fatal("persistent gateway error was accepted")
	}
	if calls != maxRPCAttempts {
		t.Fatalf("calls = %d, want %d", calls, maxRPCAttempts)
	}
	for _, secret := range []string{"private", "credential-bearing provider body", "rpc.test"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("read error exposed %q: %v", secret, err)
		}
	}
}

func TestReadRetryBackoffIsBounded(t *testing.T) {
	if got := []time.Duration{retryDelay(1), retryDelay(2), retryDelay(3)}; !slices.Equal(got, []time.Duration{time.Second, 2 * time.Second, 2 * time.Second}) {
		t.Fatalf("retry delays = %v", got)
	}
}

func TestReadRetryStopsImmediatelyOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		cancel()
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("temporary")),
		}, nil
	})}
	client, err := New("https://rpc.test", httpClient, false)
	if err != nil {
		t.Fatal(err)
	}
	client.sleep = sleepContext
	_, err = client.BlockHeight(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled retry error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("canceled call made %d requests, want 1", calls)
	}
}

func TestSendTransactionNeverRetries(t *testing.T) {
	transaction, _ := rpcTestTransfer(t)
	calls := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusGatewayTimeout,
			Header:     http.Header{"Retry-After": []string{"1"}},
			Body:       io.NopCloser(strings.NewReader("credential-bearing provider body")),
		}, nil
	})}
	client, err := New("https://rpc.test/?key=private", httpClient, false)
	if err != nil {
		t.Fatal(err)
	}
	client.sleep = func(context.Context, time.Duration) error {
		t.Fatal("sendTransaction attempted to retry")
		return nil
	}
	_, err = client.SendTransaction(t.Context(), transaction, 9)
	if err == nil {
		t.Fatal("gateway timeout was accepted")
	}
	if calls != 1 {
		t.Fatalf("sendTransaction made %d requests, want 1", calls)
	}
	for _, secret := range []string{"private", "credential-bearing provider body", "rpc.test"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("sendTransaction error exposed %q: %v", secret, err)
		}
	}
}

func TestRetryableHTTPStatusSetIsExact(t *testing.T) {
	for _, status := range []int{
		http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		if !retryableHTTPStatus(status) {
			t.Errorf("status %d is not retryable", status)
		}
	}
	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusInternalServerError,
		http.StatusTemporaryRedirect,
	} {
		if retryableHTTPStatus(status) {
			t.Errorf("status %d is retryable", status)
		}
	}
}

func TestClientRejectsMalformedAndOversizedResponses(t *testing.T) {
	tests := map[string]func(uint64) any{
		"wrong id": func(uint64) any {
			return map[string]any{"jsonrpc": "2.0", "id": 999, "result": 1}
		},
		"rpc error": func(id uint64) any {
			return map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"error": map[string]any{
					"code":    -32000,
					"message": "contains-sensitive-provider-detail",
				},
			}
		},
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			client := newTestClient(t, response)
			_, err := client.BlockHeight(t.Context())
			if err == nil {
				t.Fatal("invalid response was accepted")
			}
			if strings.Contains(err.Error(), "sensitive-provider-detail") {
				t.Fatal("RPC error detail was exposed")
			}
		})
	}

	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", maxResponseBytes+1))),
		}, nil
	})}
	client, err := New("https://rpc.test", httpClient, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.BlockHeight(t.Context()); err == nil {
		t.Fatal("oversized response was accepted")
	}
}

func TestNewRequiresSafeEndpointAndDistinctIdentity(t *testing.T) {
	if _, err := New("http://rpc.example", nil, false); err == nil {
		t.Fatal("plaintext production RPC was accepted")
	}
	if _, err := New("http://rpc.example", nil, true); err == nil {
		t.Fatal("plaintext remote RPC was accepted with local HTTP enabled")
	}
	if _, err := New("http://localhost:8899", nil, true); err == nil {
		t.Fatal("DNS localhost was accepted as a loopback RPC")
	}
	for _, endpoint := range []string{
		"https://rpc.example",
		"https://localhost:8899",
		"http://rpc.example:8899",
	} {
		if _, err := NewMithrilNode(endpoint, nil); err == nil {
			t.Errorf("non-literal-loopback Mithril RPC %q was accepted", endpoint)
		}
	}
	for _, endpoint := range []string{
		"http://127.0.0.1:8899",
		"http://[::1]:8899",
		"http://[::ffff:127.0.0.1]:8899",
	} {
		if _, err := New(endpoint, nil, true); err != nil {
			t.Errorf("literal loopback RPC %q was rejected: %v", endpoint, err)
		}
		if _, err := New(endpoint, nil, false); err == nil {
			t.Errorf("loopback RPC %q was accepted without explicit opt-in", endpoint)
		}
	}
	if _, err := New("https://user:password@rpc.example", nil, false); err == nil {
		t.Fatal("URL userinfo was accepted")
	}
	first, err := New("https://rpc.example/a?key=one", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New("https://rpc.example/a?key=two", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	third, err := New("https://other-rpc.example/a?key=one", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	explicitPort, err := New("https://rpc.example:443/a?key=three", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity() != second.Identity() || first.Identity() != explicitPort.Identity() ||
		first.Identity() == third.Identity() ||
		strings.Contains(first.Identity(), "one") {
		t.Fatal("provider identities do not represent opaque provider origins")
	}
	trailingDot, err := New("https://rpc.example./a", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if trailingDot.Identity() != first.Identity() {
		t.Fatal("trailing DNS dot changed provider identity")
	}
	ipLong, err := New("https://[2001:0db8::1]/", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	ipShort, err := New("https://[2001:db8::1]:443/", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if ipLong.Identity() != ipShort.Identity() {
		t.Fatal("equivalent IP spellings changed provider identity")
	}
	transport, ok := first.http.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("default RPC transport can use an environment proxy")
	}
}

func newTestClient(t *testing.T, response func(uint64) any) *Client {
	t.Helper()
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var input struct {
			ID uint64 `json:"id"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return jsonResponse(t, response(input.ID)), nil
	})}
	client, err := New("https://rpc.test", httpClient, false)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func jsonResponse(t *testing.T, value any) *http.Response {
	t.Helper()
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(value); err != nil {
		t.Fatal(err)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body.Bytes())),
	}
}

func rpcTestTransfer(t *testing.T) ([]byte, string) {
	t.Helper()
	seed := sha256.Sum256([]byte("source"))
	key := ed25519.NewKeyFromSeed(seed[:])
	source := solana.Encode(key.Public().(ed25519.PublicKey))
	destinationSeed := sha256.Sum256([]byte("destination"))
	destination := solana.Encode(ed25519.NewKeyFromSeed(destinationSeed[:]).Public().(ed25519.PublicKey))
	blockhash := solana.Encode(bytes.Repeat([]byte{7}, 32))
	message, err := solana.BuildTransferMessage(source, destination, blockhash, 1)
	if err != nil {
		t.Fatal(err)
	}
	transaction, signature, err := solana.SignTransferMessage(key, message)
	if err != nil {
		t.Fatal(err)
	}
	return transaction, solana.Encode(signature[:])
}

func assertContains(t *testing.T, value json.RawMessage, fragment string) {
	t.Helper()
	if !bytes.Contains(value, []byte(fragment)) {
		t.Errorf("request %s does not contain %q", value, fragment)
	}
}

func simulatedSystemAccount(lamports uint64) map[string]any {
	return map[string]any{
		"data":       []any{"", "base64"},
		"executable": false,
		"lamports":   lamports,
		"owner":      solana.Encode(make([]byte, 32)),
	}
}

// A node that refuses because it knows its own state is untrustworthy must
// reach the operator as more than a bare code, but the response body is
// attacker-influenced in general, so only reasons this build already knows are
// echoed.
func TestRPCErrorEchoesOnlyKnownNodeHealthReasons(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{"diverged is known", `{"reason":"diverged"}`, "diverged"},
		{"stalled is known", `{"reason":"stalled"}`, "stalled"},
		{"unavailable is known", `{"reason":"unavailable"}`, "unavailable"},
		{"unknown reason is dropped", `{"reason":"totally-made-up"}`, ""},
		{"injected text is dropped", `{"reason":"see http://evil.example for your key"}`, ""},
		{"absent data is fine", `null`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"data":` + tc.data + `}}`
			var decoded rpcResponse
			if err := json.Unmarshal([]byte(body), &decoded); err != nil {
				t.Fatalf("decode: %v", err)
			}
			rpcErr := &RPCError{Code: decoded.Error.Code}
			if decoded.Error.Data != nil {
				if _, known := knownNodeHealthReasons[decoded.Error.Data.Reason]; known {
					rpcErr.Reason = decoded.Error.Data.Reason
				}
			}
			if rpcErr.Reason != tc.want {
				t.Fatalf("reason: got %q, want %q", rpcErr.Reason, tc.want)
			}
			if tc.want == "" && strings.Contains(rpcErr.Error(), "node reports") {
				t.Fatalf("dropped reason must not appear in the message: %q", rpcErr.Error())
			}
			if tc.want != "" && !strings.Contains(rpcErr.Error(), tc.want) {
				t.Fatalf("known reason must appear in the message: %q", rpcErr.Error())
			}
		})
	}
}
