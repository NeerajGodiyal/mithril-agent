package signerclient

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestValidateResponseBindsExactMessage(t *testing.T) {
	request, response := clientFixture(t)
	if err := validateResponse(request, response); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*signer.Response){
		"action": func(value *signer.Response) {
			value.ActionID = strings.Repeat("0", 64)
		},
		"height": func(value *signer.Response) {
			value.LastValidBlockHeight++
		},
		"blockhash context": func(value *signer.Response) {
			value.BlockhashContextSlot++
		},
		"sealed blockhash context": func(value *signer.Response) {
			value.SealedTransaction.Metadata.BlockhashContextSlot++
		},
		"hash": func(value *signer.Response) {
			value.MessageSHA256 = strings.Repeat("0", 64)
		},
		"fee": func(value *signer.Response) {
			value.FeeLamports++
		},
		"signature": func(value *signer.Response) {
			value.Signature = solana.Encode(bytes.Repeat([]byte{1}, 64))
		},
		"transaction hash": func(value *signer.Response) {
			value.TransactionSHA256 = strings.Repeat("0", 64)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := response
			mutate(&value)
			if err := validateResponse(request, value); err == nil {
				t.Fatal("mutated signer response was accepted")
			}
		})
	}
}

func TestDecodeResponseIsStrict(t *testing.T) {
	_, response := clientFixture(t)
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeResponse(encoded); err != nil {
		t.Fatal(err)
	}
	encoded[len(encoded)-1] = ','
	encoded = append(encoded, []byte(`"unexpected":true}`)...)
	if _, err := decodeResponse(encoded); err == nil {
		t.Fatal("unknown signer response field was accepted")
	}
	if _, err := decodeResponse([]byte(
		`{"action_id":"first","Action_ID":"second"}`,
	)); err == nil {
		t.Fatal("duplicate signer response field was accepted")
	}
}

func TestNewRejectsUnsafePaths(t *testing.T) {
	command := filepath.Join(t.TempDir(), "signer")
	if err := os.WriteFile(command, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{
		Command:     command,
		PolicyPath:  "relative",
		KeypairPath: "/private/key",
	}); err == nil {
		t.Fatal("relative policy path was accepted")
	}
}

func TestLimitedWriterStopsAtBound(t *testing.T) {
	var output bytes.Buffer
	writer := limitedWriter{writer: &output, remaining: 3}
	n, err := writer.Write([]byte("abcdef"))
	if err == nil || n != 3 || output.String() != "abc" {
		t.Fatalf("limited write = %d, %v, %q", n, err, output.String())
	}
}

func TestClientInvokesSignerBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the signer command")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the production signer requires Unix ownership checks")
	}
	request, expected := clientFixture(t)
	message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := solana.DecodeTransferMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	seed := sha256.Sum256([]byte("source"))
	key := ed25519.NewKeyFromSeed(seed[:])
	temp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := signer.Policy{
		Cluster:                 request.Cluster,
		Profile:                 request.Profile,
		ProfileVersion:          request.ProfileVersion,
		ProfileFingerprint:      request.ProfileFingerprint,
		Source:                  solana.Encode(transfer.Source[:]),
		Destination:             solana.Encode(transfer.Destination[:]),
		MaxLamports:             transfer.Lamports,
		MaxFeeLamports:          request.FeeLamports,
		DailyDebitCapLamports:   transfer.Lamports + request.FeeLamports,
		AuthorizationLedgerPath: filepath.Join(temp, "authorization.jsonl"),
		ScheduleWindowSeconds:   uint64(request.ScheduleWindowEndUnix - request.ScheduleWindowStartUnix),
		ScheduleAnchorUnix:      request.ScheduleWindowStartUnix - request.ScheduleWindowStartUnix%86_400,
		MaxBlockHeightWindow:    200,
	}
	authoritySeed := sha256.Sum256([]byte("risk-authority"))
	authorityKey := ed25519.NewKeyFromSeed(authoritySeed[:])
	authorityPublic, err := riskgrant.PublicKeyHex(authorityKey)
	if err != nil {
		t.Fatal(err)
	}
	policy.RiskAuthorityKeyID = "test-risk-authority"
	policy.RiskAuthorityPublicKey = authorityPublic
	_, policy.SubmitterPublicKey = clientSubmitterKeys(t)

	binary := filepath.Join(temp, "mithril-agent-signer")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "../cmd/mithril-agent-signer")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build signer: %v\n%s", err, output)
	}
	if err := os.Chmod(binary, 0o700); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(temp, "policy.json")
	keyPath := filepath.Join(temp, "keypair.json")
	writePrivateJSON(t, policyPath, policy)
	writePrivateJSON(t, keyPath, keypairValues(key))

	client, err := New(Config{
		Command:     binary,
		PolicyPath:  policyPath,
		KeypairPath: keyPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Sign(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Signature != expected.Signature ||
		response.MessageSHA256 != expected.MessageSHA256 ||
		response.TransactionSHA256 != expected.TransactionSHA256 {
		t.Fatal("signer process returned a different signed transaction")
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

func clientFixture(t *testing.T) (signer.Request, signer.Response) {
	t.Helper()
	seed := sha256.Sum256([]byte("source"))
	key := ed25519.NewKeyFromSeed(seed[:])
	source := solana.Encode(key.Public().(ed25519.PublicKey))
	destinationSeed := sha256.Sum256([]byte("destination"))
	destination := solana.Encode(ed25519.NewKeyFromSeed(destinationSeed[:]).Public().(ed25519.PublicKey))
	blockhash := solana.Encode(bytes.Repeat([]byte{7}, 32))
	message, err := solana.BuildTransferMessage(source, destination, blockhash, 5)
	if err != nil {
		t.Fatal(err)
	}
	profileHash := sha256.Sum256([]byte("profile"))
	profileFingerprint := hex.EncodeToString(profileHash[:])
	now := time.Now().UTC()
	scheduleAnchor := time.Date(
		now.Year(),
		now.Month(),
		now.Day(),
		0,
		0,
		0,
		0,
		time.UTC,
	).Unix()
	scheduleStart := now.Truncate(time.Hour).Unix()
	actionID, err := agent.ComputeActionID(profileFingerprint, scheduleStart)
	if err != nil {
		t.Fatal(err)
	}
	request := signer.Request{
		Domain:                  signer.RequestDomain,
		Cluster:                 "devnet",
		Profile:                 "treasury_sweep_v1",
		ProfileVersion:          1,
		ProfileFingerprint:      profileFingerprint,
		ActionID:                actionID,
		ScheduleWindowStartUnix: scheduleStart,
		ScheduleWindowEndUnix:   scheduleStart + 3_600,
		MessageBase64:           base64.StdEncoding.EncodeToString(message),
		BlockhashContextSlot:    90,
		FeeLamports:             5_000,
		FeeMinContextSlot:       90,
		PrimaryFeeContextSlot:   90,
		SecondaryFeeContextSlot: 91,
		RecentBlockhash:         blockhash,
		ObservedBlockHeight:     100,
		LastValidBlockHeight:    200,
	}
	authoritySeed := sha256.Sum256([]byte("risk-authority"))
	authorityKey := ed25519.NewKeyFromSeed(authoritySeed[:])
	authorityPublic, err := riskgrant.PublicKeyHex(authorityKey)
	if err != nil {
		t.Fatal(err)
	}
	policy := signer.Policy{
		Cluster:                request.Cluster,
		Profile:                request.Profile,
		ProfileVersion:         request.ProfileVersion,
		ProfileFingerprint:     request.ProfileFingerprint,
		Source:                 source,
		Destination:            destination,
		MaxLamports:            10,
		MaxFeeLamports:         5_000,
		DailyDebitCapLamports:  100_000,
		ScheduleWindowSeconds:  3_600,
		ScheduleAnchorUnix:     scheduleAnchor,
		MaxBlockHeightWindow:   200,
		RiskAuthorityKeyID:     "test-risk-authority",
		RiskAuthorityPublicKey: authorityPublic,
	}
	ledgerDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ledgerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy.AuthorizationLedgerPath = filepath.Join(ledgerDir, "authorization.jsonl")
	_, policy.SubmitterPublicKey = clientSubmitterKeys(t)
	messageHash := sha256.Sum256(message)
	binding, err := signer.RiskBinding(request, hex.EncodeToString(messageHash[:]))
	if err != nil {
		t.Fatal(err)
	}
	grantAt := now
	request.RiskGrant, err = riskgrant.Sign(
		authorityKey,
		policy.RiskAuthorityKeyID,
		binding,
		grantAt,
		30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := signer.AuthorizeAndSign(policy, key, request, grantAt)
	if err != nil {
		t.Fatal(err)
	}
	return request, response
}

func clientSubmitterKeys(t *testing.T) (string, string) {
	t.Helper()
	seed := sha256.Sum256([]byte("submitter"))
	privateKey := hex.EncodeToString(seed[:])
	publicKey, err := sealedtx.PublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return privateKey, publicKey
}
