package policyclient

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/operatorapproval"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/signer"
)

func TestBoundedOperationContext(t *testing.T) {
	ctx, cancel, err := boundedOperationContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > operationTimeout || time.Until(deadline) < operationTimeout-time.Second {
		t.Fatalf("default risk-authority deadline = %v", deadline)
	}

	short, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	shortDeadline, _ := short.Deadline()
	bounded, release, err := boundedOperationContext(short)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if deadline, ok := bounded.Deadline(); !ok || !deadline.Equal(shortDeadline) {
		t.Fatal("risk-authority operation replaced the caller's shorter deadline")
	}
	var nilContext context.Context
	if _, _, err := boundedOperationContext(nilContext); err == nil {
		t.Fatal("accepted a nil risk-authority operation context")
	}
}

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
		approval := operatorapproval.Approval{
			Version: operatorapproval.Version, Domain: operatorapproval.Domain,
			Approver: "approval-wallet", RequestSHA256: strings.Repeat("a", 64),
			SignatureBase58: "approval-signature",
		}
		grant, err = client.AuthorizeApproved(t.Context(), request, approval)
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

func TestClientSocketBoundary(t *testing.T) {
	key := policyClientTestKey()
	publicKey, err := riskgrant.PublicKeyHex(key)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp("/tmp", "risk-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "risk.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0o660); err != nil {
		t.Fatal(err)
	}

	observed := make(chan SocketRequest, 4)
	serve := func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		data, readErr := io.ReadAll(io.LimitReader(connection, maxSocketRequestBytes+1))
		if readErr != nil {
			return
		}
		var request SocketRequest
		if json.Unmarshal(data, &request) != nil {
			return
		}
		observed <- request
		response := SocketResponse{Version: SocketVersion, Status: SocketStatusOK}
		switch request.Operation {
		case SocketOperationIdentity:
			response.Identity = &Identity{KeyID: "risk-test", PublicKey: publicKey}
		case SocketOperationAuthorize:
			message, decodeErr := base64.StdEncoding.Strict().DecodeString(request.Authorize.MessageBase64)
			if decodeErr != nil {
				return
			}
			digest := sha256.Sum256(message)
			binding, bindingErr := signer.RiskBinding(*request.Authorize, hex.EncodeToString(digest[:]))
			if bindingErr != nil {
				return
			}
			grant, signErr := riskgrant.Sign(key, "risk-test", binding, time.Now().UTC(), 30*time.Second)
			if signErr != nil {
				return
			}
			response.Grant = &grant
		}
		_ = json.NewEncoder(connection).Encode(response)
	}

	client, err := New(Config{
		SocketPath: socket, KeyID: "risk-test", PublicKey: publicKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	go serve()
	identity, err := client.Identity(t.Context())
	if err != nil || identity.KeyID != "risk-test" || identity.PublicKey != publicKey {
		t.Fatalf("identity = %+v, %v", identity, err)
	}
	<-observed
	go serve()
	request := policyClientTestRequest()
	request.RiskGrant.SignatureBase64 = "must-be-cleared"
	grant, err := client.Authorize(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.VerifyAt(request, grant, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := <-observed; got.Approval != nil {
		t.Fatal("ordinary authorization unexpectedly carried operator approval")
	}
	approval := operatorapproval.Approval{
		Version: 1, Domain: operatorapproval.Domain,
		Approver: strings.Repeat("1", 32), RequestSHA256: strings.Repeat("a", 64),
		SignatureBase58: strings.Repeat("2", 64),
	}
	go serve()
	if _, err := client.AuthorizeApproved(t.Context(), request, approval); err != nil {
		t.Fatal(err)
	}
	if got := <-observed; got.Approval == nil || *got.Approval != approval {
		t.Fatalf("operator approval did not cross risk authority socket: %+v", got.Approval)
	}
	go serve()
	large := request
	large.JupiterCandidate = &proposalcheck.Candidate{
		AddressTables: largeAddressTableEvidence(),
	}
	if _, err := client.Authorize(t.Context(), large); err != nil {
		t.Fatalf("large portable request did not cross risk authority socket: %v", err)
	}
	<-observed
}

func largeAddressTableEvidence() []jupiterswap.AddressTableEvidence {
	contents := base64.StdEncoding.EncodeToString(make([]byte, 256*32))
	tables := make([]jupiterswap.AddressTableEvidence, 32)
	for index := range tables {
		tables[index] = jupiterswap.AddressTableEvidence{
			Address: strings.Repeat("1", 44), AddressesBase64: contents,
		}
	}
	return tables
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
	approved := false
	for _, arg := range os.Args {
		if arg == "--approved-request" {
			approved = true
		}
	}
	var request signer.Request
	if approved {
		var envelope ApprovedRequest
		if json.NewDecoder(os.Stdin).Decode(&envelope) != nil ||
			envelope.Approval.Version != operatorapproval.Version {
			os.Exit(2)
		}
		request = envelope.Request
	} else if json.NewDecoder(os.Stdin).Decode(&request) != nil {
		os.Exit(2)
	}
	if request.RiskGrant.SignatureBase64 != "" {
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
