package signer

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func signerTestKey(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte(label))
	return ed25519.NewKeyFromSeed(seed[:])
}

func signerTestSubmitterKeys(t *testing.T) (string, string) {
	t.Helper()
	seed := sha256.Sum256([]byte("submitter"))
	privateKey := hex.EncodeToString(seed[:])
	publicKey, err := sealedtx.PublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return privateKey, publicKey
}

func signerFixture(t *testing.T) (Policy, ed25519.PrivateKey, Request) {
	t.Helper()
	privateKey := signerTestKey("source")
	source := solana.Encode(privateKey.Public().(ed25519.PublicKey))
	destination := solana.Encode(signerTestKey("destination").Public().(ed25519.PublicKey))
	blockhash := solana.Encode(bytes.Repeat([]byte{7}, 32))
	message, err := solana.BuildTransferMessage(source, destination, blockhash, 42)
	if err != nil {
		t.Fatal(err)
	}
	profileHash := sha256.Sum256([]byte("profile"))
	profileFingerprint := hex.EncodeToString(profileHash[:])
	scheduleAnchor := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC).Unix()
	scheduleStart := scheduleAnchor + 3_600
	ledgerDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ledgerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := Policy{
		Cluster:                 "devnet",
		Profile:                 "treasury_sweep_v1",
		ProfileVersion:          1,
		ProfileFingerprint:      profileFingerprint,
		Source:                  source,
		Destination:             destination,
		MaxLamports:             50,
		MaxFeeLamports:          5_000,
		DailyDebitCapLamports:   100_000,
		AuthorizationLedgerPath: filepath.Join(ledgerDir, "authorization.jsonl"),
		ScheduleWindowSeconds:   3_600,
		ScheduleAnchorUnix:      scheduleAnchor,
		MaxBlockHeightWindow:    200,
	}
	authorityKey := signerTestKey("risk-authority")
	authorityPublic, err := riskgrant.PublicKeyHex(authorityKey)
	if err != nil {
		t.Fatal(err)
	}
	policy.RiskAuthorityKeyID = "test-risk-authority"
	policy.RiskAuthorityPublicKey = authorityPublic
	_, policy.SubmitterPublicKey = signerTestSubmitterKeys(t)
	actionID, err := agent.ComputeActionID(profileFingerprint, scheduleStart)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{
		Domain:                  RequestDomain,
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
	grantSignerRequest(t, policy, &request, time.Unix(scheduleStart+1, 0).UTC())
	return policy, privateKey, request
}

func grantSignerRequest(t *testing.T, policy Policy, request *Request, at time.Time) {
	t.Helper()
	message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(message)
	binding, err := RiskBinding(*request, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	grant, err := riskgrant.Sign(
		signerTestKey("risk-authority"),
		policy.RiskAuthorityKeyID,
		binding,
		at,
		30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.RiskGrant = grant
}

func TestSignRevalidatesAndSignsExactMessage(t *testing.T) {
	policy, privateKey, request := signerFixture(t)
	response, err := signAt(
		policy,
		privateKey,
		request,
		time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	submitterPrivateKey, _ := signerTestSubmitterKeys(t)
	transaction, err := sealedtx.Open(submitterPrivateKey, response.SealedTransaction)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := solana.DecodeSignedTransfer(transaction)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Lamports != 42 || response.Signature != solana.Encode(decoded.Signature[:]) ||
		response.BlockhashContextSlot != request.BlockhashContextSlot ||
		response.SealedTransaction.Metadata.BlockhashContextSlot != request.BlockhashContextSlot {
		t.Fatalf("response/transaction mismatch: %+v / %+v", response, decoded)
	}
}

func TestSignRejectsPolicyAndSemanticDrift(t *testing.T) {
	basePolicy, baseKey, baseRequest := signerFixture(t)
	tests := map[string]func(*Policy, *Request, *ed25519.PrivateKey){
		"mainnet": func(policy *Policy, _ *Request, _ *ed25519.PrivateKey) {
			policy.Cluster = "mainnet-beta"
		},
		"wrong request cluster": func(_ *Policy, request *Request, _ *ed25519.PrivateKey) {
			request.Cluster = "testnet"
		},
		"unsupported version": func(policy *Policy, request *Request, _ *ed25519.PrivateKey) {
			policy.ProfileVersion = 2
			request.ProfileVersion = 2
		},
		"expired": func(_ *Policy, request *Request, _ *ed25519.PrivateKey) {
			request.ObservedBlockHeight = request.LastValidBlockHeight + 1
		},
		"long validity": func(_ *Policy, request *Request, _ *ed25519.PrivateKey) {
			request.LastValidBlockHeight = request.ObservedBlockHeight + 201
		},
		"wrong blockhash claim": func(_ *Policy, request *Request, _ *ed25519.PrivateKey) {
			request.RecentBlockhash = solana.Encode(bytes.Repeat([]byte{8}, 32))
		},
		"wrong key": func(_ *Policy, _ *Request, key *ed25519.PrivateKey) {
			*key = signerTestKey("wrong")
		},
		"bad action ID": func(_ *Policy, request *Request, _ *ed25519.PrivateKey) {
			request.ActionID = "00"
		},
		"wrong profile fingerprint": func(_ *Policy, request *Request, _ *ed25519.PrivateKey) {
			request.ProfileFingerprint = strings.Repeat("0", 64)
		},
		"wrong schedule window": func(_ *Policy, request *Request, _ *ed25519.PrivateKey) {
			request.ScheduleWindowStartUnix++
		},
		"fee above policy": func(_ *Policy, request *Request, _ *ed25519.PrivateKey) {
			request.FeeLamports++
		},
		"stale fee evidence": func(_ *Policy, request *Request, _ *ed25519.PrivateKey) {
			request.SecondaryFeeContextSlot = request.FeeMinContextSlot - 1
		},
		"fee minimum differs from blockhash": func(_ *Policy, request *Request, _ *ed25519.PrivateKey) {
			request.FeeMinContextSlot--
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			policy := basePolicy
			request := baseRequest
			key := ed25519.PrivateKey(bytes.Clone(baseKey))
			mutate(&policy, &request, &key)
			if _, err := signAt(
				policy,
				key,
				request,
				time.Unix(baseRequest.ScheduleWindowStartUnix+1, 0).UTC(),
			); err == nil {
				t.Fatal("mutated request was signed")
			}
		})
	}
}

func TestRiskGrantBindsFreshnessEvidence(t *testing.T) {
	policy, privateKey, request := signerFixture(t)
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	tests := map[string]func(*Request){
		"blockhash and fee context": func(value *Request) {
			value.BlockhashContextSlot++
			value.FeeMinContextSlot++
			value.PrimaryFeeContextSlot++
			value.SecondaryFeeContextSlot++
		},
		"primary fee context":   func(value *Request) { value.PrimaryFeeContextSlot++ },
		"secondary fee context": func(value *Request) { value.SecondaryFeeContextSlot++ },
		"observed block height": func(value *Request) { value.ObservedBlockHeight++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := request
			mutate(&changed)
			if _, err := ValidateRequest(policy, changed); err != nil {
				t.Fatalf("mutation should remain policy-valid: %v", err)
			}
			if _, err := signAt(policy, privateKey, changed, now); err == nil ||
				!strings.Contains(err.Error(), "risk grant binding") {
				t.Fatalf("changed evidence error = %v", err)
			}
		})
	}
}

func TestRiskBindingHashesEveryUnsignedRequestField(t *testing.T) {
	_, _, request := signerFixture(t)
	base, err := RiskBinding(request, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Request){
		"domain":                func(value *Request) { value.Domain += "x" },
		"cluster":               func(value *Request) { value.Cluster += "x" },
		"profile":               func(value *Request) { value.Profile += "x" },
		"profile version":       func(value *Request) { value.ProfileVersion++ },
		"profile fingerprint":   func(value *Request) { value.ProfileFingerprint = strings.Repeat("b", 64) },
		"action":                func(value *Request) { value.ActionID = strings.Repeat("b", 64) },
		"window start":          func(value *Request) { value.ScheduleWindowStartUnix++ },
		"window end":            func(value *Request) { value.ScheduleWindowEndUnix++ },
		"message":               func(value *Request) { value.MessageBase64 += "A" },
		"blockhash context":     func(value *Request) { value.BlockhashContextSlot++ },
		"fee":                   func(value *Request) { value.FeeLamports++ },
		"fee minimum":           func(value *Request) { value.FeeMinContextSlot++ },
		"primary fee context":   func(value *Request) { value.PrimaryFeeContextSlot++ },
		"secondary fee context": func(value *Request) { value.SecondaryFeeContextSlot++ },
		"recent blockhash":      func(value *Request) { value.RecentBlockhash += "x" },
		"observed height":       func(value *Request) { value.ObservedBlockHeight++ },
		"last valid height":     func(value *Request) { value.LastValidBlockHeight++ },
	}
	if got, want := len(tests)+1, reflect.TypeFor[Request]().NumField(); got != want {
		t.Fatalf("request hash coverage has %d fields, want %d", got, want)
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := request
			mutate(&changed)
			binding, err := RiskBinding(changed, base.MessageSHA256)
			if err != nil {
				t.Fatal(err)
			}
			if binding.RequestSHA256 == base.RequestSHA256 {
				t.Fatal("request field did not change the canonical hash")
			}
		})
	}
	changedGrant := request
	changedGrant.RiskGrant.SignatureBase64 = "ignored"
	changedGrant.RiskGrant.Claims.Version++
	binding, err := RiskBinding(changedGrant, base.MessageSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if binding.RequestSHA256 != base.RequestSHA256 {
		t.Fatal("risk grant changed the unsigned request hash")
	}
}

func TestImmutableRequestHashCanonicalVector(t *testing.T) {
	request := Request{
		Domain: "domain", Cluster: "devnet", Profile: "profile", ProfileVersion: 2,
		ProfileFingerprint: strings.Repeat("1", 64), ActionID: strings.Repeat("2", 64),
		ScheduleWindowStartUnix: 100, ScheduleWindowEndUnix: 200,
		MessageBase64: "AQID", BlockhashContextSlot: 300, FeeLamports: 4_000,
		FeeMinContextSlot: 300, PrimaryFeeContextSlot: 301, SecondaryFeeContextSlot: 302,
		RecentBlockhash: "blockhash", ObservedBlockHeight: 500, LastValidBlockHeight: 600,
	}
	got, err := immutableRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	const want = "3760b9455cc13848352acc1893edca5ae3296985fb25cefb39a3b914bf065d8e"
	if got != want {
		t.Fatalf("canonical request hash = %s", got)
	}
}

func TestSignRejectsDebitOverflow(t *testing.T) {
	policy, privateKey, request := signerFixture(t)
	policy.MaxLamports = ^uint64(0)
	policy.MaxFeeLamports = 1
	request.FeeLamports = 1
	message, err := solana.BuildTransferMessage(
		policy.Source,
		policy.Destination,
		request.RecentBlockhash,
		^uint64(0),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.MessageBase64 = base64.StdEncoding.EncodeToString(message)
	grantSignerRequest(
		t,
		policy,
		&request,
		time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC(),
	)
	if _, err := signAt(
		policy,
		privateKey,
		request,
		time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC(),
	); err == nil {
		t.Fatal("overflowing transaction debit was signed")
	}
}

func TestLoadKeypairRequiresPrivateStableFile(t *testing.T) {
	privateKey := signerTestKey("source")
	values := make([]uint16, len(privateKey))
	for index, value := range privateKey {
		values[index] = uint16(value)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "keypair.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadKeypair(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded, privateKey) {
		t.Fatal("loaded key differs")
	}
	base64Path := filepath.Join(filepath.Dir(path), "base64-keypair.json")
	base64Encoded, err := json.Marshal([]byte(privateKey))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base64Path, base64Encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeypair(base64Path); err == nil {
		t.Fatal("base64 keypair string was accepted as a Solana byte array")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeypair(path); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("public keypair error = %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(path), "keypair-link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeypair(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink keypair error = %v", err)
	}
}
