package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/policyclient"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestSocketServesIdentityAndAuthorization(t *testing.T) {
	policy, key, request, now := socketFixture(t)

	for name, socketRequest := range map[string]policyclient.SocketRequest{
		"identity": {
			Version: policyclient.SocketVersion, Operation: policyclient.SocketOperationIdentity,
		},
		"authorize": {
			Version: policyclient.SocketVersion, Operation: policyclient.SocketOperationAuthorize,
			Authorize: &request,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var input, output bytes.Buffer
			if err := json.NewEncoder(&input).Encode(socketRequest); err != nil {
				t.Fatal(err)
			}
			if err := runSocketAt(policy, key, &input, &output, func() time.Time { return now }); err != nil {
				t.Fatal(err)
			}
			var response policyclient.SocketResponse
			if err := json.Unmarshal(output.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Version != policyclient.SocketVersion || response.Status != policyclient.SocketStatusOK {
				t.Fatalf("response = %+v", response)
			}
			if name == "identity" && (response.Identity == nil || response.Grant != nil) {
				t.Fatalf("identity response = %+v", response)
			}
			if name == "authorize" && (response.Identity != nil || response.Grant == nil) {
				t.Fatalf("authorization response = %+v", response)
			}
		})
	}
}

func TestSocketDoesNotExposeInternalFailure(t *testing.T) {
	var output bytes.Buffer
	err := runSocketAt(
		policyauthority.Policy{}, ed25519.PrivateKey{},
		strings.NewReader(`{"version":1,"operation":"identity"}`),
		&output, time.Now,
	)
	if err == nil {
		t.Fatal("invalid risk authority unexpectedly succeeded")
	}
	var response policyclient.SocketResponse
	if decodeErr := json.Unmarshal(output.Bytes(), &response); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if response.Status != policyclient.SocketStatusFailed ||
		response.Identity != nil || response.Grant != nil {
		t.Fatalf("internal failure crossed risk authority boundary: %+v", response)
	}
}

func TestGrantedRequestOutputAttachesGrantWithoutChangingRequest(t *testing.T) {
	policy, key, request, now := socketFixture(t)
	directory := t.TempDir()
	policyPath := filepath.Join(directory, "policy.json")
	keyPath := filepath.Join(directory, "keypair.json")
	policyData, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	keyValues := make([]uint16, len(key))
	for i, value := range key {
		keyValues[i] = uint16(value)
	}
	keyData, err := json.Marshal(keyValues)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, policyData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyData, 0o600); err != nil {
		t.Fatal(err)
	}

	var input, output bytes.Buffer
	if err := json.NewEncoder(&input).Encode(request); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{
		"--policy", policyPath, "--keypair", keyPath, "--granted-request",
	}, &input, &output, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	var granted signer.Request
	if err := json.Unmarshal(output.Bytes(), &granted); err != nil {
		t.Fatal(err)
	}
	if granted.RiskGrant.SignatureBase64 == "" {
		t.Fatal("complete request is missing its risk grant")
	}
	granted.RiskGrant = riskgrant.Grant{}
	if !reflect.DeepEqual(granted, request) {
		t.Fatal("authority changed the request while attaching its grant")
	}
}

func TestOperatorApprovalFileIsForegroundOnly(t *testing.T) {
	for _, mode := range []string{"--identity", "--socket"} {
		if err := run([]string{
			"--policy", "/missing/policy", "--keypair", "/missing/key",
			mode, "--operator-approval", "/missing/approval",
		}, strings.NewReader(""), io.Discard, time.Now); err == nil ||
			!strings.Contains(err.Error(), "foreground authorization") {
			t.Fatalf("%s operator approval boundary = %v", mode, err)
		}
	}
}

func socketFixture(t *testing.T) (policyauthority.Policy, ed25519.PrivateKey, signer.Request, time.Time) {
	t.Helper()
	seed := sha256.Sum256([]byte("risk authority socket"))
	authority := ed25519.NewKeyFromSeed(seed[:])
	authorityPublic, err := riskgrant.PublicKeyHex(authority)
	if err != nil {
		t.Fatal(err)
	}
	sourceSeed := sha256.Sum256([]byte("risk socket source"))
	sourceKey := ed25519.NewKeyFromSeed(sourceSeed[:])
	source := solana.Encode(sourceKey.Public().(ed25519.PublicKey))
	destinationSeed := sha256.Sum256([]byte("risk socket destination"))
	destination := solana.Encode(ed25519.NewKeyFromSeed(destinationSeed[:]).Public().(ed25519.PublicKey))
	submitterSeed := sha256.Sum256([]byte("risk socket submitter"))
	submitterPublic, err := sealedtx.PublicKey(hex.EncodeToString(submitterSeed[:]))
	if err != nil {
		t.Fatal(err)
	}
	profileFingerprint := strings.Repeat("1", 64)
	anchor := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	transactionPolicy := signer.Policy{
		Cluster: "devnet", Profile: "treasury_sweep_v1", ProfileVersion: 1,
		ProfileFingerprint: profileFingerprint, Source: source, Destination: destination,
		MaxLamports: 50, MaxFeeLamports: 5_000, DailyDebitCapLamports: 100_000,
		AuthorizationLedgerPath: filepath.Join(t.TempDir(), "authorization.jsonl"),
		ScheduleWindowSeconds:   3_600, ScheduleAnchorUnix: anchor.Unix(),
		MaxBlockHeightWindow: 200, RiskAuthorityKeyID: "risk-socket",
		RiskAuthorityPublicKey: authorityPublic, SubmitterPublicKey: submitterPublic,
	}
	blockhash := solana.Encode(bytes.Repeat([]byte{7}, 32))
	message, err := solana.BuildTransferMessage(source, destination, blockhash, 42)
	if err != nil {
		t.Fatal(err)
	}
	windowStart := anchor.Add(time.Hour).Unix()
	actionID, err := agent.ComputeActionID(profileFingerprint, windowStart)
	if err != nil {
		t.Fatal(err)
	}
	request := signer.Request{
		Domain: signer.RequestDomain, Cluster: "devnet", Profile: "treasury_sweep_v1",
		ProfileVersion: 1, ProfileFingerprint: profileFingerprint, ActionID: actionID,
		ScheduleWindowStartUnix: windowStart, ScheduleWindowEndUnix: windowStart + 3_600,
		MessageBase64: base64.StdEncoding.EncodeToString(message), BlockhashContextSlot: 90,
		FeeLamports: 5_000, FeeMinContextSlot: 90, PrimaryFeeContextSlot: 90,
		SecondaryFeeContextSlot: 91, RecentBlockhash: blockhash,
		ObservedBlockHeight: 100, LastValidBlockHeight: 250,
	}
	return policyauthority.Policy{
		TransactionPolicy: transactionPolicy, GrantLifetimeSecs: 30,
	}, authority, request, time.Unix(windowStart+1, 0).UTC()
}
