package policyclient

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/signer"
)

func TestClientProcessBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix test wrapper")
	}
	key := policyClientTestKey()
	publicKey, err := riskgrant.PublicKeyHex(key)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("UNRELATED_SECRET", "must-not-reach-child")

	t.Run("identity and grant", func(t *testing.T) {
		client := newPolicyTestClient(t, publicKey, "normal")
		identity, err := client.Identity(t.Context())
		if err != nil || identity.KeyID != "risk-test" || identity.PublicKey != publicKey {
			t.Fatalf("identity = %+v, %v", identity, err)
		}
		request := policyClientTestRequest()
		request.RiskGrant.SignatureBase64 = "must-be-cleared"
		grant, err := client.Authorize(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.VerifyAt(request, grant, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	})

	for _, mode := range []string{"malformed", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			client := newPolicyTestClient(t, publicKey, mode)
			if _, err := client.Identity(t.Context()); err == nil {
				t.Fatal("accepted invalid child output")
			}
		})
	}

	t.Run("timeout", func(t *testing.T) {
		client := newPolicyTestClient(t, publicKey, "sleep")
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		if _, err := client.Identity(ctx); err == nil {
			t.Fatal("child ignored context deadline")
		}
	})

	t.Run("identity mismatch", func(t *testing.T) {
		client := newPolicyTestClient(t, publicKey, "wrong_identity")
		if _, err := client.Identity(t.Context()); err == nil {
			t.Fatal("accepted the wrong child identity")
		}
	})

	t.Run("executable drift", func(t *testing.T) {
		client := newPolicyTestClient(t, publicKey, "normal")
		if err := os.Chmod(client.config.Command, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Identity(t.Context()); err == nil {
			t.Fatal("executed a command that became replaceable")
		}
	})
}

func TestPolicyClientHelperProcess(t *testing.T) {
	if os.Getenv("MITHRIL_AGENT_POLICYCLIENT_HELPER") != "1" {
		return
	}
	mode := os.Getenv("MITHRIL_AGENT_POLICYCLIENT_MODE")
	if os.Getenv("UNRELATED_SECRET") != "" {
		mode = "malformed"
	}
	switch mode {
	case "sleep":
		time.Sleep(time.Second)
		return
	case "oversize":
		_, _ = os.Stdout.Write(make([]byte, maxResponseBytes+2))
		os.Exit(0)
	case "malformed":
		_, _ = fmt.Fprint(os.Stdout, `{"unexpected":true}`)
		os.Exit(0)
	case "wrong_identity":
		_ = json.NewEncoder(os.Stdout).Encode(Identity{KeyID: "wrong", PublicKey: strings.Repeat("0", 64)})
		os.Exit(0)
	}
	for _, arg := range os.Args {
		if arg == "--identity" {
			publicKey, _ := riskgrant.PublicKeyHex(policyClientTestKey())
			_ = json.NewEncoder(os.Stdout).Encode(Identity{KeyID: "risk-test", PublicKey: publicKey})
			os.Exit(0)
		}
	}
	var request signer.Request
	if json.NewDecoder(os.Stdin).Decode(&request) != nil || request.RiskGrant.SignatureBase64 != "" {
		os.Exit(2)
	}
	message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
	if err != nil {
		os.Exit(2)
	}
	digest := sha256.Sum256(message)
	binding, err := signer.RiskBinding(request, hex.EncodeToString(digest[:]))
	if err != nil {
		os.Exit(2)
	}
	grant, err := riskgrant.Sign(
		policyClientTestKey(),
		"risk-test",
		binding,
		time.Now().UTC(),
		30*time.Second,
	)
	if err != nil {
		os.Exit(2)
	}
	_ = json.NewEncoder(os.Stdout).Encode(grant)
	os.Exit(0)
}

func newPolicyTestClient(t *testing.T, publicKey, mode string) *Client {
	t.Helper()
	command := policyTestWrapper(t)
	client, err := New(Config{
		Command: command, PolicyPath: filepath.Join(filepath.Dir(command), "policy.json"),
		KeypairPath: filepath.Join(filepath.Dir(command), "key.json"),
		KeyID:       "risk-test", PublicKey: publicKey,
		Env: []string{
			"MITHRIL_AGENT_POLICYCLIENT_HELPER=1",
			"MITHRIL_AGENT_POLICYCLIENT_MODE=" + mode,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func policyTestWrapper(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "policy-helper")
	contents := "#!/bin/sh\nexec " + strconv.Quote(executable) +
		" -test.run=^TestPolicyClientHelperProcess$ -- \"$@\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func policyClientTestKey() ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("policy client test key"))
	return ed25519.NewKeyFromSeed(seed[:])
}

func policyClientTestRequest() signer.Request {
	digest := strings.Repeat("1", 64)
	return signer.Request{
		ProfileFingerprint:      digest,
		ActionID:                digest,
		MessageBase64:           base64.StdEncoding.EncodeToString([]byte("message")),
		BlockhashContextSlot:    1,
		FeeLamports:             1,
		FeeMinContextSlot:       1,
		PrimaryFeeContextSlot:   1,
		SecondaryFeeContextSlot: 1,
		ObservedBlockHeight:     1,
		LastValidBlockHeight:    1,
	}
}
