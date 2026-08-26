package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/signertransport"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/turnkeycustody"
	turnkey "github.com/tkhq/go-sdk/v2"
)

func TestRunAtContextStopsCanceledSignerBeforeProtectedState(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := runAtContext(ctx, nil, strings.NewReader(""), &bytes.Buffer{}, time.Now)
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("canceled signer operation = %v", err)
	}
}

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

	input.Reset()
	if err := json.NewEncoder(&input).Encode(signertransport.Request{
		Version: signertransport.Version, Operation: signertransport.OperationSign,
		Sign: &request,
	}); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := runSocketAt(
		policy, sourceKey, nil, &input, &output, func() time.Time { return now },
	); err != nil {
		t.Fatal(err)
	}
	var socketResponse signertransport.Response
	if err := json.Unmarshal(output.Bytes(), &socketResponse); err != nil {
		t.Fatal(err)
	}
	if socketResponse.Status != signertransport.StatusOK || socketResponse.Signed == nil ||
		socketResponse.Signed.TransactionSHA256 != response.TransactionSHA256 {
		t.Fatalf("unexpected socket signer response: %+v", socketResponse)
	}
}

func TestRunSocketDoesNotExposeInternalFailure(t *testing.T) {
	var output bytes.Buffer
	err := runSocketAt(
		signer.Policy{}, ed25519.PrivateKey{}, nil,
		strings.NewReader(`{"version":1,"operation":"identity"}`), &output, time.Now,
	)
	if err == nil {
		t.Fatal("invalid signer unexpectedly succeeded")
	}
	var response signertransport.Response
	if decodeErr := json.Unmarshal(output.Bytes(), &response); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if response.Status != signertransport.StatusFailed || response.Reason != "" {
		t.Fatalf("internal failure crossed signer boundary: %+v", response)
	}
}

func TestRunSocketRejectsMainnetWithoutBoundKeys(t *testing.T) {
	policy := signer.Policy{
		Cluster: "mainnet-beta",
		Jupiter: &jupiterswap.Policy{},
	}
	for _, request := range []signertransport.Request{
		{Version: signertransport.Version, Operation: signertransport.OperationIdentity},
		{
			Version:   signertransport.Version,
			Operation: signertransport.OperationSign,
			Sign:      &signer.Request{},
		},
	} {
		var input, output bytes.Buffer
		if err := json.NewEncoder(&input).Encode(request); err != nil {
			t.Fatal(err)
		}
		if err := runSocketAt(
			policy, ed25519.PrivateKey{}, nil, &input, &output, time.Now,
		); err == nil {
			t.Fatal("Mainnet signer request unexpectedly succeeded")
		}
		var response signertransport.Response
		if err := json.Unmarshal(output.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Status != signertransport.StatusFailed || response.Signed != nil ||
			response.Reason != "" {
			t.Fatalf("Mainnet signer request crossed the disabled boundary: %+v", response)
		}
	}
}

func TestRunMainnetIdentityRequiresSeparateBoundKeys(t *testing.T) {
	policy, walletKey, attestationKey := mainnetSignerFixture(t)
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	walletPath := filepath.Join(dir, "wallet.json")
	attestationPath := filepath.Join(dir, "attestation.json")
	writePrivateJSON(t, policyPath, policy)
	writePrivateJSON(t, walletPath, keypairValues(walletKey))
	writePrivateJSON(t, attestationPath, keypairValues(attestationKey))

	var output bytes.Buffer
	if err := runAt([]string{
		"--policy", policyPath,
		"--keypair", walletPath,
		"--attestation-keypair", attestationPath,
		"--identity",
	}, strings.NewReader(""), &output, time.Now); err != nil {
		t.Fatal(err)
	}
	var identity signertransport.Identity
	if err := json.Unmarshal(output.Bytes(), &identity); err != nil {
		t.Fatal(err)
	}
	if identity.PublicKey != policy.Source ||
		identity.AttestationPublicKey != policy.AttestationPublicKey ||
		identity.SubmitterPublicKey != policy.SubmitterPublicKey ||
		identity.ProfileSHA256 != policy.ProfileFingerprint {
		t.Fatalf("Mainnet signer identity = %+v", identity)
	}

	if err := runAt([]string{
		"--policy", policyPath, "--keypair", walletPath, "--identity",
	}, strings.NewReader(""), &bytes.Buffer{}, time.Now); err == nil ||
		!strings.Contains(err.Error(), "requires an attestation keypair") {
		t.Fatalf("missing Mainnet attestation key = %v", err)
	}

	var input bytes.Buffer
	if err := json.NewEncoder(&input).Encode(signertransport.Request{
		Version: signertransport.Version, Operation: signertransport.OperationIdentity,
	}); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := runSocketAt(
		policy, walletKey, attestationKey, &input, &output, time.Now,
	); err != nil {
		t.Fatal(err)
	}
	var response signertransport.Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != signertransport.StatusOK || response.Identity == nil ||
		*response.Identity != identity {
		t.Fatalf("Mainnet socket identity = %+v", response)
	}

	_, err := authorizeAndSign(
		policy, walletKey, attestationKey, signer.Request{}, time.Now().UTC(),
	)
	if err == nil || !strings.Contains(err.Error(), "Jupiter signing request") {
		t.Fatalf("Mainnet signer dispatch = %v", err)
	}
	if _, statErr := os.Stat(policy.AuthorizationLedgerPath); !os.IsNotExist(statErr) {
		t.Fatalf("invalid Mainnet request created a ledger: %v", statErr)
	}
}

func TestRunTurnkeyIdentityUsesExactPolicySource(t *testing.T) {
	policy, walletKey, attestationKey := mainnetSignerFixture(t)
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	walletPath := filepath.Join(dir, "wallet.json")
	attestationPath := filepath.Join(dir, "attestation.json")
	apiKeyPath := filepath.Join(dir, "turnkey.private")
	writePrivateJSON(t, policyPath, policy)
	writePrivateJSON(t, walletPath, keypairValues(walletKey))
	writePrivateJSON(t, attestationPath, keypairValues(attestationKey))
	if err := os.WriteFile(apiKeyPath, []byte(strings.Repeat("1", 64)+":p256\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	apiKey, err := turnkey.NewAPIKeyStamper(strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	verified := false
	backend, err := loadSigningBackendWithTurnkeyLoader(
		t.Context(), policy, "", apiKeyPath, apiKey.PublicKey(), "organization", policy.Source,
		func(
			ctx context.Context,
			path, publicKey string,
			config turnkeycustody.Config,
			expectedAddress string,
		) (custodySignFunc, error) {
			verified = true
			if ctx.Err() != nil || path != apiKeyPath || publicKey != apiKey.PublicKey() ||
				config.OrganizationID != "organization" || config.SignWith != policy.Source ||
				expectedAddress != policy.Source {
				t.Fatal("Turnkey address verification received the wrong binding")
			}
			return func(context.Context, signer.TransactionCustodyRequest) ([]byte, error) {
				return nil, nil
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := signerIdentityWithBackend(policy, backend, attestationKey)
	if err != nil {
		t.Fatal(err)
	}
	if !verified || identity.PublicKey != policy.Source ||
		identity.AttestationPublicKey != policy.AttestationPublicKey ||
		identity.SubmitterPublicKey != policy.SubmitterPublicKey {
		t.Fatalf("Turnkey signer identity = %+v", identity)
	}

	args := []string{
		"--policy", policyPath,
		"--turnkey-api-key", apiKeyPath,
		"--turnkey-api-public-key", apiKey.PublicKey(),
		"--turnkey-organization", "organization",
		"--turnkey-sign-with", policy.Source,
		"--attestation-keypair", attestationPath,
		"--identity",
	}
	for name, changed := range map[string][]string{
		"incomplete": {
			"--policy", policyPath,
			"--turnkey-api-key", apiKeyPath,
			"--turnkey-sign-with", policy.Source,
			"--attestation-keypair", attestationPath,
			"--identity",
		},
		"mixed backends": append([]string{"--keypair", walletPath}, args...),
		"wrong API public key": func() []string {
			changed := append([]string(nil), args...)
			for index := range changed {
				if changed[index] == apiKey.PublicKey() {
					changed[index] = "different"
				}
			}
			return changed
		}(),
		"invalid signing resource": func() []string {
			copy := append([]string(nil), args...)
			for index := range copy {
				if copy[index] == policy.Source {
					copy[index] = "\n"
				}
			}
			return copy
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := runAt(changed, strings.NewReader(""), &bytes.Buffer{}, time.Now); err == nil {
				t.Fatal("invalid Turnkey signer configuration was accepted")
			}
		})
	}
}

func TestLoadSigningBackendAcceptsVerifiedTurnkeyPrivateKeyID(t *testing.T) {
	policy, _, _ := mainnetSignerFixture(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	apiKeyPath := filepath.Join(dir, "turnkey.private")
	if err := os.WriteFile(apiKeyPath, []byte(strings.Repeat("1", 64)+":p256\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	apiKey, err := turnkey.NewAPIKeyStamper(strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	const privateKeyID = "11111111-2222-3333-4444-555555555555"
	verified := false
	backend, err := loadSigningBackendWithTurnkeyLoader(
		t.Context(), policy, "", apiKeyPath, apiKey.PublicKey(), "organization", privateKeyID,
		func(
			ctx context.Context,
			path, publicKey string,
			config turnkeycustody.Config,
			expectedAddress string,
		) (custodySignFunc, error) {
			verified = true
			if ctx.Err() != nil || path != apiKeyPath || publicKey != apiKey.PublicKey() ||
				config.OrganizationID != "organization" || config.SignWith != privateKeyID ||
				expectedAddress != policy.Source {
				t.Fatal("Turnkey private-key identity verification received the wrong binding")
			}
			return func(context.Context, signer.TransactionCustodyRequest) ([]byte, error) {
				return nil, nil
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !verified || backend.remoteSource != policy.Source || backend.signTransaction == nil {
		t.Fatal("verified Turnkey private-key ID did not create the policy-bound backend")
	}
}

func mainnetSignerFixture(t *testing.T) (signer.Policy, ed25519.PrivateKey, ed25519.PrivateKey) {
	t.Helper()
	walletSeed := sha256.Sum256([]byte("Mainnet command wallet"))
	walletKey := ed25519.NewKeyFromSeed(walletSeed[:])
	walletPublic, err := signer.PublicKey(walletKey)
	if err != nil {
		t.Fatal(err)
	}
	attestationSeed := sha256.Sum256([]byte("Mainnet command attestor"))
	attestationKey := ed25519.NewKeyFromSeed(attestationSeed[:])
	attestationPublic, err := signer.PublicKey(attestationKey)
	if err != nil {
		t.Fatal(err)
	}
	riskSeed := sha256.Sum256([]byte("Mainnet command risk authority"))
	riskKey := ed25519.NewKeyFromSeed(riskSeed[:])
	riskPublic, err := riskgrant.PublicKeyHex(riskKey)
	if err != nil {
		t.Fatal(err)
	}
	submitterSeed := sha256.Sum256([]byte("Mainnet command submitter"))
	submitterPublic, err := sealedtx.PublicKey(hex.EncodeToString(submitterSeed[:]))
	if err != nil {
		t.Fatal(err)
	}
	route := jupiterswap.Policy{
		Owner: walletPublic, InputMint: orcaswap.WrappedSOLMint,
		OutputMint:     solana.Encode(bytes.Repeat([]byte{42}, 32)),
		MaxInputAmount: 1_000_000, MinOutputAmount: 1, MaxSlippageBPS: 50,
		MaxComputeUnits: 200_000, MaxComputeUnitPriceMicroLamport: 1,
		MaxFeeLamports: 5_000, MaxTokenAccountRentLamports: 2_039_280,
		RouteGuard: commandSignerRouteGuard(),
	}
	fingerprint, err := route.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return signer.Policy{
		Cluster: "mainnet-beta", Profile: jupiterswap.ProfileName,
		ProfileVersion: jupiterswap.ProfileVersion, ProfileFingerprint: fingerprint,
		Source: walletPublic, MaxLamports: route.MaxInputAmount,
		MaxFeeLamports: route.MaxFeeLamports, DailyDebitCapLamports: 10_000_000,
		AuthorizationLedgerPath: filepath.Join(t.TempDir(), "authorization.jsonl"),
		ScheduleWindowSeconds:   3_600,
		ScheduleAnchorUnix:      time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC).Unix(),
		MaxBlockHeightWindow:    150, RiskAuthorityKeyID: "Mainnet command risk authority",
		RiskAuthorityPublicKey: riskPublic, SubmitterPublicKey: submitterPublic,
		AttestationPublicKey: attestationPublic, Jupiter: &route,
	}, walletKey, attestationKey
}

func commandSignerRouteGuard() jupiterswap.RouteGuardDeployment {
	return jupiterswap.RouteGuardDeployment{
		Program:        solana.Encode(bytes.Repeat([]byte{71}, 32)),
		ProgramData:    solana.Encode(bytes.Repeat([]byte{72}, 32)),
		DeploymentSlot: 123, CodeLength: 1, CodeSHA256: strings.Repeat("1", 64),
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

func TestRunRejectsRequestBeforeLoadingCustodyKeys(t *testing.T) {
	policy, _, _ := mainnetSignerFixture(t)
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	writePrivateJSON(t, policyPath, policy)
	request := signer.Request{
		ScheduleWindowStartUnix: policy.ScheduleAnchorUnix,
		ScheduleWindowEndUnix:   policy.ScheduleAnchorUnix + int64(policy.ScheduleWindowSeconds),
	}
	requestData, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(policy.ScheduleAnchorUnix+1, 0)
	err = runAt([]string{
		"--policy", policyPath,
		"--keypair", filepath.Join(dir, "missing-wallet.json"),
		"--attestation-keypair", filepath.Join(dir, "missing-attestation.json"),
	}, bytes.NewReader(requestData), &bytes.Buffer{}, func() time.Time { return now })
	if err == nil || !strings.Contains(err.Error(), "Jupiter signing request") ||
		strings.Contains(err.Error(), "keypair") {
		t.Fatalf("invalid request reached custody key loading: %v", err)
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
