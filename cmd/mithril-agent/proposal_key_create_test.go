package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
)

func TestProposalKeyCreateWritesOnlyPrivateKeyFileAndPublicResult(t *testing.T) {
	for _, kind := range []string{"risk-authority", "attestation", "submitter"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "key.json")
			var output bytes.Buffer
			if err := runProposalKeyCreate([]string{
				"--kind", kind, "--out", path,
			}, &output); err != nil {
				t.Fatal(err)
			}
			var result struct {
				Status    string `json:"status"`
				Kind      string `json:"kind"`
				PublicKey string `json:"public_key"`
			}
			if err := json.Unmarshal(output.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Status != "key_created" || result.Kind != kind || result.PublicKey == "" {
				t.Fatalf("key result = %+v", result)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("key mode = %v, %v", info, err)
			}
			switch kind {
			case "risk-authority", "attestation":
				privateKey, err := signer.LoadKeypair(path)
				if err != nil {
					t.Fatal(err)
				}
				defer clear(privateKey)
				publicKey := solana.Encode(privateKey.Public().(ed25519.PublicKey))
				if kind == "risk-authority" {
					publicKey, err = riskgrant.PublicKeyHex(privateKey)
					if err != nil {
						t.Fatal(err)
					}
				}
				if result.PublicKey != publicKey {
					t.Fatal("public identity does not match generated private key")
				}
			case "submitter":
				privateKey, err := submitter.LoadPrivateKey(path)
				if err != nil {
					t.Fatal(err)
				}
				publicKey, err := sealedtx.PublicKey(privateKey)
				if err != nil || result.PublicKey != publicKey {
					t.Fatal("submitter identity does not match generated private key")
				}
			}
			if err := runProposalKeyCreate([]string{
				"--kind", kind, "--out", path,
			}, &output); err == nil {
				t.Fatal("proposal key-create overwrote an existing private key")
			}
		})
	}
}

func TestProposalKeyCreateHelpAndValidationStayOffline(t *testing.T) {
	var output bytes.Buffer
	if err := runProposalKeyCreate([]string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"offline", "never creates a provider account", "--kind", "--out"} {
		if !bytes.Contains(output.Bytes(), []byte(want)) {
			t.Fatalf("proposal key-create help is missing %q", want)
		}
	}
	if err := runProposalKeyCreate([]string{
		"--kind", "wallet", "--out", filepath.Join(t.TempDir(), "key.json"),
	}, &output); err == nil {
		t.Fatal("proposal key-create accepted a wallet key kind")
	}
}
