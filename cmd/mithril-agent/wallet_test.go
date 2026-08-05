package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func solanaEncodeForTest(key ed25519.PublicKey) string { return solana.Encode(key) }

func signerLoadForTest(path string) (ed25519.PrivateKey, error) { return signer.LoadKeypair(path) }

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
