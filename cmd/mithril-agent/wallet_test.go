package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func solanaEncodeForTest(key ed25519.PublicKey) string { return solana.Encode(key) }

func signerLoadForTest(path string) (ed25519.PrivateKey, error) { return signer.LoadKeypair(path) }

type walletRoundTripFunc func(*http.Request) (*http.Response, error)

func (f walletRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// writeTestWallet creates a keypair file in the standard solana-keygen layout.
// This is a TEST helper on purpose: the product does not generate keys.
func writeTestWallet(t *testing.T, dir string) (path, address string, key ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	values := make([]uint16, ed25519.PrivateKeySize)
	for i, b := range private {
		values[i] = uint16(b)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "wallet.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, solanaEncodeForTest(public), private
}

// The address shown must be the one the key actually controls, or an operator
// could fund or inspect the wrong account.
func TestWalletCheckDerivesTheControllingAddress(t *testing.T) {
	path, _, key := writeTestWallet(t, t.TempDir())
	got, err := walletAddress(path)
	if err != nil {
		t.Fatal(err)
	}
	public, ok := key.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("bad key")
	}
	if want := solanaEncodeForTest(public); got != want {
		t.Fatalf("address = %q, want %q", got, want)
	}
}

// A key stored readably by others is a real leak; the check must refuse it
// rather than reporting on it.
func TestWalletCheckRefusesAWorldReadableKey(t *testing.T) {
	path, _, _ := writeTestWallet(t, t.TempDir())
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := walletAddress(path); err == nil {
		t.Fatal("a world-readable wallet file was accepted")
	}
}

// Creating an account is allowed for Devnet testing, but the help must never
// present it as a custody solution or suggest carrying it to Mainnet.
func TestWalletHelpKeepsCreationScopedToDevnet(t *testing.T) {
	var out bytes.Buffer
	if err := runWallet(t.Context(), []string{"--help"}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "DEVNET-ONLY") {
		t.Error("help does not scope account creation to Devnet")
	}
	if !strings.Contains(text, "never your own wallet") {
		t.Error("help does not state the agent uses a dedicated account")
	}
	// For Mainnet it must point at tooling the operator already trusts.
	for _, required := range []string{"solana-keygen", "hardware wallet", "policy-based"} {
		if !strings.Contains(text, required) {
			t.Errorf("help omits the Mainnet alternative %q", required)
		}
	}
	if !strings.Contains(text, "never asks for, imports, or transmits an existing wallet key") {
		t.Error("help does not promise it never takes the user's own key")
	}
}

// The created account must be labelled Devnet and must tell the user it is
// funded from, and separate from, their own wallet.
func TestWalletNewExplainsTheTwoTierModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	var out bytes.Buffer
	if err := runWalletNew([]string{"--file", path}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, required := range []string{
		"DEVNET-ONLY",
		"separate from your own",
		"willing to put at risk",
		"Do not reuse this key on Mainnet",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("wallet new output omits %q", required)
		}
	}
}

// A generated key must be usable by the real signer, or the account is useless.
func TestWalletNewProducesAKeyTheSignerAccepts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := runWalletNew([]string{"--file", path}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	loaded, err := signerLoadForTest(path)
	if err != nil {
		t.Fatalf("the signer rejected our own generated account: %v", err)
	}
	if len(loaded) != ed25519.PrivateKeySize {
		t.Fatalf("key is %d bytes, want %d", len(loaded), ed25519.PrivateKeySize)
	}
}

func TestWalletNewWritesAPrivateFileAndNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := runWalletNew([]string{"--file", path}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("account file mode = %o, want 0600", perm)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := runWalletNew([]string{"--file", path}, &bytes.Buffer{}); err == nil {
		t.Fatal("a second wallet new overwrote an existing account")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("the existing key changed despite the refusal")
	}
}

// The secret must never reach the terminal, a log, or the network.
func TestWalletNewNeverPrintsTheSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	var out bytes.Buffer
	if err := runWalletNew([]string{"--file", path}, &out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), string(raw[:40])) {
		t.Fatal("wallet new printed private key material")
	}
}

// Funding must use only the derived public address, request no more than the
// fixed one-SOL target, and leave the key file untouched.
func TestWalletFundTopsUpWithoutSendingThePrivateKey(t *testing.T) {
	path, address, _ := writeTestWallet(t, t.TempDir())
	keyBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	original := walletHTTP
	t.Cleanup(func() { walletHTTP = original })
	var calls int
	var requestBodies [][]byte
	walletHTTP = &http.Client{Transport: walletRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		requestBodies = append(requestBodies, body)
		var rpc struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &rpc); err != nil {
			t.Fatal(err)
		}
		calls++
		response := `{"jsonrpc":"2.0","id":1,"result":{"value":250000000}}`
		switch calls {
		case 1:
			if rpc.Method != "getBalance" {
				t.Fatalf("first method = %q, want getBalance", rpc.Method)
			}
		case 2:
			if rpc.Method != "requestAirdrop" || len(rpc.Params) != 3 {
				t.Fatalf("funding request = %q with %d params", rpc.Method, len(rpc.Params))
			}
			var gotAddress string
			var gotLamports uint64
			var config map[string]string
			if json.Unmarshal(rpc.Params[0], &gotAddress) != nil ||
				json.Unmarshal(rpc.Params[1], &gotLamports) != nil ||
				json.Unmarshal(rpc.Params[2], &config) != nil {
				t.Fatal("funding parameters did not decode")
			}
			if gotAddress != address || gotLamports != 750_000_000 || config["commitment"] != "confirmed" {
				t.Fatalf("funding parameters = %q, %d, %v", gotAddress, gotLamports, config)
			}
			response = fmt.Sprintf(
				`{"jsonrpc":"2.0","id":1,"result":%q}`,
				solana.Encode(make([]byte, ed25519.SignatureSize)),
			)
		default:
			t.Fatal("wallet fund made an unexpected extra request")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(response)),
		}, nil
	})}

	var out bytes.Buffer
	if err := runWalletFund(t.Context(), []string{"--file", path}, &out); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.Contains(out.String(), "requested successfully") {
		t.Fatalf("funding calls = %d, output = %q", calls, out.String())
	}
	for _, body := range requestBodies {
		if bytes.Contains(body, keyBefore) {
			t.Fatal("the private key was sent to the Devnet endpoint")
		}
	}
	keyAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("wallet fund changed the key file")
	}
}

func TestWalletFundIsIdempotentAtTheTarget(t *testing.T) {
	path, _, _ := writeTestWallet(t, t.TempDir())
	original := walletHTTP
	t.Cleanup(func() { walletHTTP = original })
	var calls int
	walletHTTP = &http.Client{Transport: walletRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"jsonrpc":"2.0","id":1,"result":{"value":1000000000}}`,
			)),
		}, nil
	})}

	var out bytes.Buffer
	if err := runWalletFund(t.Context(), []string{"--file", path}, &out); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(out.String(), "no funding was requested") {
		t.Fatalf("funding calls = %d, output = %q", calls, out.String())
	}
}

func TestWalletFundExplainsTheNoAccountFallback(t *testing.T) {
	path, _, _ := writeTestWallet(t, t.TempDir())
	original := walletHTTP
	t.Cleanup(func() { walletHTTP = original })
	var calls int
	walletHTTP = &http.Client{Transport: walletRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		body := `{"jsonrpc":"2.0","id":1,"result":{"value":0}}`
		if calls == 2 {
			body = `{"jsonrpc":"2.0","id":1,"error":{"code":429,"message":"private provider detail"}}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	err := runWalletFund(t.Context(), []string{"--file", path}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "https://faucet.solana.com") {
		t.Fatalf("funding failure did not name the fallback: %v", err)
	}
	if strings.Contains(err.Error(), "private provider detail") {
		t.Fatalf("funding failure leaked provider text: %v", err)
	}
}

func TestWalletCheckRejectsUnsafeInput(t *testing.T) {
	for _, args := range [][]string{
		{"check", "--file", "relative.json"},
		{"check"},
		{"check", "--file", "/tmp/w.json", "--cluster", "mainnet-beta"},
	} {
		if err := runWalletCheck(t.Context(), args[1:], &bytes.Buffer{}); err == nil {
			t.Errorf("accepted unsafe input %v", args)
		}
	}
}

func TestWalletTokenBalanceDistinguishesEmptyFromMissing(t *testing.T) {
	_, owner, _ := writeTestWallet(t, t.TempDir())
	original := walletHTTP
	t.Cleanup(func() { walletHTTP = original })

	for _, test := range []struct {
		name   string
		body   string
		exists bool
	}{
		{"empty account", `{"jsonrpc":"2.0","id":1,"result":{"value":{"amount":"0"}}}`, true},
		{"missing account", `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid param: could not find account"}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			walletHTTP = &http.Client{Transport: walletRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(test.body)),
				}, nil
			})}
			amount, exists, err := walletTokenBalance(t.Context(), owner, orcaswap.DevnetUSDCMint)
			if err != nil {
				t.Fatal(err)
			}
			if amount != 0 || exists != test.exists {
				t.Fatalf("balance = %d, exists = %v; want 0, %v", amount, exists, test.exists)
			}
		})
	}
}

func TestWalletRPCRejectsHTTPFailuresAndOversizedResponses(t *testing.T) {
	original := walletHTTP
	t.Cleanup(func() { walletHTTP = original })
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"HTTP failure", http.StatusBadGateway, `{"jsonrpc":"2.0","id":1,"result":0}`},
		{"oversized", http.StatusOK, strings.Repeat(" ", walletMaxResponse+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			walletHTTP = &http.Client{Transport: walletRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: test.status,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(test.body)),
				}, nil
			})}
			var result any
			if err := walletRPC(t.Context(), "getBalance", nil, &result); err == nil {
				t.Fatal("unsafe Devnet response was accepted")
			}
		})
	}
}

func TestWalletBalanceRejectsAMissingResult(t *testing.T) {
	original := walletHTTP
	t.Cleanup(func() { walletHTTP = original })
	walletHTTP = &http.Client{Transport: walletRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"jsonrpc":"2.0","id":1}`,
			)),
		}, nil
	})}
	if _, err := walletLamports(t.Context(), "public-address"); err == nil {
		t.Fatal("a balance response with no result was accepted as zero")
	}
}

func TestWalletHTTPClientDoesNotUseAmbientProxyOrRedirects(t *testing.T) {
	client := newWalletHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("wallet balance client can use an ambient proxy")
	}
	if err := client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("wallet balance client accepted a redirect: %v", err)
	}
}
