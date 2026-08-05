package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestRunSignsPolicyBoundRequest(t *testing.T) {
	sourceSeed := sha256.Sum256([]byte("source"))
	sourceKey := ed25519.NewKeyFromSeed(sourceSeed[:])
	source := solana.Encode(sourceKey.Public().(ed25519.PublicKey))
	destinationSeed := sha256.Sum256([]byte("destination"))
	destinationKey := ed25519.NewKeyFromSeed(destinationSeed[:])
	destination := solana.Encode(destinationKey.Public().(ed25519.PublicKey))
	blockhash := solana.Encode(bytes.Repeat([]byte{9}, 32))
	message, err := solana.BuildTransferMessage(source, destination, blockhash, 10)
	if err != nil {
		t.Fatal(err)
	}

	profileHash := sha256.Sum256([]byte("profile"))
	profileFingerprint := hex.EncodeToString(profileHash[:])
	scheduleAnchor := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC).Unix()
	scheduleStart := scheduleAnchor + 3_600
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := signer.Policy{
		Cluster:                 "devnet",
		Profile:                 "treasury_sweep_v1",
		ProfileVersion:          1,
		ProfileFingerprint:      profileFingerprint,
		Source:                  source,
		Destination:             destination,
		MaxLamports:             20,
		MaxFeeLamports:          5_000,
		DailyDebitCapLamports:   20_000,
		AuthorizationLedgerPath: filepath.Join(dir, "authorization.jsonl"),
		ScheduleWindowSeconds:   3_600,
		ScheduleAnchorUnix:      scheduleAnchor,
		MaxBlockHeightWindow:    200,
	}
	actionID, err := agent.ComputeActionID(profileFingerprint, scheduleStart)
	if err != nil {
		t.Fatal(err)
	}
	request := signer.Request{
		Domain:                  signer.RequestDomain,
		Cluster:                 policy.Cluster,
		Profile:                 policy.Profile,
		ProfileVersion:          policy.ProfileVersion,
		ProfileFingerprint:      policy.ProfileFingerprint,
		ActionID:                actionID,
		ScheduleWindowStartUnix: scheduleStart,
		ScheduleWindowEndUnix:   scheduleStart + int64(policy.ScheduleWindowSeconds),
		MessageBase64:           base64.StdEncoding.EncodeToString(message),
		BlockhashContextSlot:    90,
		FeeLamports:             5_000,
		FeeMinContextSlot:       90,
		PrimaryFeeContextSlot:   90,
		SecondaryFeeContextSlot: 91,
		RecentBlockhash:         blockhash,
		ObservedBlockHeight:     100,
		LastValidBlockHeight:    250,
	}
	now := time.Unix(scheduleStart+1, 0).UTC()
	authoritySeed := sha256.Sum256([]byte("risk-authority"))
	authorityKey := ed25519.NewKeyFromSeed(authoritySeed[:])
	authorityPublic, err := riskgrant.PublicKeyHex(authorityKey)
	if err != nil {
		t.Fatal(err)
	}
	policy.RiskAuthorityKeyID = "test-risk-authority"
	policy.RiskAuthorityPublicKey = authorityPublic
	submitterSeed := sha256.Sum256([]byte("submitter"))
	submitterPrivateKey := hex.EncodeToString(submitterSeed[:])
	submitterPublicKey, err := sealedtx.PublicKey(submitterPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	policy.SubmitterPublicKey = submitterPublicKey
	messageHash := sha256.Sum256(message)
	binding, err := signer.RiskBinding(request, hex.EncodeToString(messageHash[:]))
	if err != nil {
		t.Fatal(err)
	}
	request.RiskGrant, err = riskgrant.Sign(
		authorityKey,
		policy.RiskAuthorityKeyID,
		binding,
		now,
		30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(dir, "policy.json")
	keyPath := filepath.Join(dir, "keypair.json")
	writePrivateJSON(t, policyPath, policy)
	writePrivateJSON(t, keyPath, keypairValues(sourceKey))

	var input bytes.Buffer
	if err := json.NewEncoder(&input).Encode(request); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runAt(
		[]string{"--policy", policyPath, "--keypair", keyPath},
		&input,
		&output,
		func() time.Time { return now },
	); err != nil {
		t.Fatal(err)
	}
	var response signer.Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	transaction, err := sealedtx.Open(submitterPrivateKey, response.SealedTransaction)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := solana.DecodeSignedTransfer(transaction)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Lamports != 10 || response.ActionID != request.ActionID ||
		response.BlockhashContextSlot != request.BlockhashContextSlot ||
		response.SealedTransaction.Metadata.BlockhashContextSlot != request.BlockhashContextSlot {
		t.Fatalf("unexpected signer response: %+v", response)
	}
}

func TestRunRejectsUnknownRequestField(t *testing.T) {
	if err := decodeStrictJSON([]byte(`{"domain":"x","extra":true}`), &signer.Request{}); err == nil {
		t.Fatal("unknown request field unexpectedly accepted")
	}
	if err := decodeStrictJSON(
		[]byte(`{"domain":"first","Domain":"second"}`),
		&signer.Request{},
	); err == nil {
		t.Fatal("duplicate request field unexpectedly accepted")
	}
}

func writePrivateJSON(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func keypairValues(key []byte) []uint16 {
	values := make([]uint16, len(key))
	for index, value := range key {
		values[index] = uint16(value)
	}
	return values
}
