package signer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func jupiterSignerFixture(t *testing.T) (Policy, Request) {
	t.Helper()
	ownerKey := signerTestKey("Jupiter wallet")
	owner, err := PublicKey(ownerKey)
	if err != nil {
		t.Fatal(err)
	}
	outputMint := solana.Encode(bytes.Repeat([]byte{2}, 32))
	inputAccount, err := orcaswap.AssociatedTokenAddress(owner, orcaswap.WrappedSOLMint)
	if err != nil {
		t.Fatal(err)
	}
	outputAccount, err := orcaswap.AssociatedTokenAddress(owner, outputMint)
	if err != nil {
		t.Fatal(err)
	}
	quoteRequest := jupiterquote.Request{
		Taker: owner, InputMint: orcaswap.WrappedSOLMint, OutputMint: outputMint,
		DestinationTokenAccount: outputAccount, InputAmount: 10, SlippageBPS: 50,
	}
	quote := jupiterquote.Result{InputAmount: 10, EstimatedOutput: 20, MinimumOutput: 20}
	transfer := make([]byte, 12)
	binary.LittleEndian.PutUint32(transfer[:4], 2)
	binary.LittleEndian.PutUint64(transfer[4:], quoteRequest.InputAmount)
	routeData := []byte{187, 100, 250, 204, 49, 196, 175, 20}
	routeData = binary.LittleEndian.AppendUint64(routeData, quoteRequest.InputAmount)
	routeData = binary.LittleEndian.AppendUint64(routeData, quote.EstimatedOutput)
	routeData = binary.LittleEndian.AppendUint16(routeData, quoteRequest.SlippageBPS)
	routeData = binary.LittleEndian.AppendUint16(routeData, 0)
	routeData = binary.LittleEndian.AppendUint16(routeData, 0)
	routeData = binary.LittleEndian.AppendUint32(routeData, 1)
	routeData = append(routeData, 17, 1, 0x10, 0x27, 0, 1)
	plan := []solana.Instruction{
		{
			Program: orcaswap.AssociatedTokenProgram,
			Accounts: []solana.AccountMeta{
				{Address: owner, Signer: true, Writable: true},
				{Address: inputAccount, Writable: true}, {Address: owner},
				{Address: orcaswap.WrappedSOLMint}, {Address: orcaswap.SystemProgram},
				{Address: orcaswap.TokenProgram},
			},
			Data: []byte{1},
		},
		{
			Program: orcaswap.SystemProgram,
			Accounts: []solana.AccountMeta{
				{Address: owner, Signer: true, Writable: true},
				{Address: inputAccount, Writable: true},
			},
			Data: transfer,
		},
		{
			Program:  orcaswap.TokenProgram,
			Accounts: []solana.AccountMeta{{Address: inputAccount, Writable: true}},
			Data:     []byte{17},
		},
		{
			Program: jupiterswap.Program,
			Accounts: []solana.AccountMeta{
				{Address: owner, Signer: true},
				{Address: inputAccount, Writable: true},
				{Address: outputAccount, Writable: true},
				{Address: orcaswap.WrappedSOLMint}, {Address: outputMint},
				{Address: orcaswap.TokenProgram}, {Address: orcaswap.TokenProgram},
				{Address: outputAccount, Writable: true},
				{Address: "D8cy77BBepLMngZx6ZukaTff5hCt1HrWyKk3Hnd9oitf"},
				{Address: jupiterswap.Program},
				{Address: solana.Encode(bytes.Repeat([]byte{3}, 32)), Writable: true},
			},
			Data: routeData,
		},
		{
			Program: orcaswap.TokenProgram,
			Accounts: []solana.AccountMeta{
				{Address: inputAccount, Writable: true},
				{Address: owner, Writable: true}, {Address: owner, Signer: true},
			},
			Data: []byte{9},
		},
	}
	limit, err := solana.SetComputeUnitLimitInstruction(250_000)
	if err != nil {
		t.Fatal(err)
	}
	priceData := make([]byte, 9)
	priceData[0] = 3
	binary.LittleEndian.PutUint64(priceData[1:], 5_000)
	jupiterPolicy := jupiterswap.Policy{
		Owner: owner, InputMint: orcaswap.WrappedSOLMint,
		OutputMint: outputMint, MaxInputAmount: 10,
		MinOutputAmount: 19, MaxSlippageBPS: 50, MaxComputeUnits: 300_000,
		MaxComputeUnitPriceMicroLamport: 10_000, MaxFeeLamports: 20_000,
		MaxTokenAccountRentLamports: 3_000_000, RouteGuard: jupiterSignerRouteGuard(),
	}
	instructions := append(
		[]solana.Instruction{limit, {Program: solana.ComputeBudgetProgram, Data: priceData}},
		plan...,
	)
	blockhash := solana.Encode(bytes.Repeat([]byte{9}, 32))
	message, err := jupiterswap.BuildGuardedPolicyV0Message(
		jupiterPolicy, owner, blockhash, instructions, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := jupiterPolicy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	ledgerDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ledgerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	authorityKey := signerTestKey("Jupiter risk authority")
	authorityPublic, err := riskgrant.PublicKeyHex(authorityKey)
	if err != nil {
		t.Fatal(err)
	}
	_, submitterPublic := signerTestSubmitterKeys(t)
	attestationPublic, err := PublicKey(signerTestKey("Jupiter response attestor"))
	if err != nil {
		t.Fatal(err)
	}
	anchor := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC).Unix()
	actionID, err := jupiterswap.ComputeActionID(fingerprint, anchor+3_600)
	if err != nil {
		t.Fatal(err)
	}
	policy := Policy{
		Cluster: "mainnet-beta", Profile: jupiterswap.ProfileName,
		ProfileVersion: jupiterswap.ProfileVersion, ProfileFingerprint: fingerprint,
		Source: owner, MaxLamports: 10, MaxFeeLamports: 20_000,
		DailyDebitCapLamports:   100_000_000,
		AuthorizationLedgerPath: filepath.Join(ledgerDir, "authorization.jsonl"),
		ScheduleWindowSeconds:   3_600, ScheduleAnchorUnix: anchor,
		MaxBlockHeightWindow: 150, RiskAuthorityKeyID: "Jupiter risk authority",
		RiskAuthorityPublicKey: authorityPublic, SubmitterPublicKey: submitterPublic,
		AttestationPublicKey: attestationPublic,
		Jupiter:              &jupiterPolicy,
	}
	candidate := proposalcheck.Candidate{
		Version: proposalcheck.CandidateVersion, Policy: jupiterPolicy,
		Request: quoteRequest, Quote: quote,
		MessageBase64:        base64.StdEncoding.EncodeToString(message),
		LastValidBlockHeight: 200,
	}
	request := Request{
		Domain: jupiterswap.RequestDomain, Cluster: policy.Cluster, Profile: policy.Profile,
		ProfileVersion: policy.ProfileVersion, ProfileFingerprint: fingerprint,
		ActionID: actionID, ScheduleWindowStartUnix: anchor + 3_600,
		ScheduleWindowEndUnix: anchor + 7_200, MessageBase64: candidate.MessageBase64,
		BlockhashContextSlot: 90, FeeLamports: 5_000, FeeMinContextSlot: 90,
		PrimaryFeeContextSlot: 90, SecondaryFeeContextSlot: 91,
		RecentBlockhash: blockhash, ObservedBlockHeight: 100, LastValidBlockHeight: 200,
		JupiterCandidate: &candidate,
		JupiterProviders: &proposalcheck.ProviderBindings{
			PrimaryTrustDomain: "primary", PrimaryOriginSHA256: strings.Repeat("1", 64),
			SecondaryTrustDomain: "secondary", SecondaryOriginSHA256: strings.Repeat("2", 64),
			ArchiveProbeSignature: solana.Encode(bytes.Repeat([]byte{7}, 64)),
		},
	}
	return policy, request
}

func jupiterSignerRouteGuard() jupiterswap.RouteGuardDeployment {
	code := []byte("signer route guard")
	hash := sha256.Sum256(code)
	return jupiterswap.RouteGuardDeployment{
		Program:        solana.Encode(bytes.Repeat([]byte{71}, 32)),
		ProgramData:    solana.Encode(bytes.Repeat([]byte{72}, 32)),
		DeploymentSlot: 123, CodeLength: uint64(len(code)), CodeSHA256: hex.EncodeToString(hash[:]),
	}
}

func grantJupiterSignerRequest(t *testing.T, policy Policy, request *Request, at time.Time) {
	t.Helper()
	validated, err := ValidateJupiterRequest(policy, *request)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := RiskBinding(*request, validated.MessageSHA256)
	if err != nil {
		t.Fatal(err)
	}
	request.RiskGrant, err = riskgrant.Sign(
		signerTestKey("Jupiter risk authority"),
		policy.RiskAuthorityKeyID,
		binding,
		at,
		30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizeAndSignJupiterUsesExactTransactionOnlyCustody(t *testing.T) {
	policy, request := jupiterSignerFixture(t)
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	grantJupiterSignerRequest(t, policy, &request, now)
	message, tables, err := proposalcheck.ValidateCandidateMaterial(
		*policy.Jupiter,
		*request.JupiterCandidate,
	)
	if err != nil {
		t.Fatal(err)
	}
	expectedRequestHash, err := immutableRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	walletKey := signerTestKey("Jupiter wallet")
	called := 0
	walletSignature := ""
	response, err := AuthorizeAndSignJupiterWith(
		context.Background(),
		policy,
		request,
		now,
		func(_ context.Context, custody TransactionCustodyRequest) ([]byte, error) {
			called++
			if custody.RequestSHA256 != expectedRequestHash ||
				custody.TimestampMS != now.UnixMilli() ||
				len(custody.Transaction) != 1+64+len(message) ||
				custody.Transaction[0] != 1 ||
				!bytes.Equal(custody.Transaction[1:65], make([]byte, 64)) ||
				!bytes.Equal(custody.Transaction[65:], message) {
				t.Fatal("custody callback did not receive the exact unsigned v0 transaction")
			}
			transaction, _, signErr := solana.SignV0Message(walletKey, message, tables)
			if signErr == nil {
				walletSignature = solana.Encode(transaction[1 : 1+ed25519.SignatureSize])
			}
			return transaction, signErr
		},
		func(_ context.Context, claims []byte) ([]byte, error) {
			return ed25519.Sign(signerTestKey("Jupiter response attestor"), claims), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if called != 1 || response.RequestSHA256 == "" ||
		response.SignerAttestation.SignatureBase64 == "" {
		t.Fatalf("Jupiter signer response is incomplete: %+v", response)
	}
	if response.Signature != "" || response.SealedTransaction.Metadata.Signature != "" {
		t.Fatal("Jupiter signature escaped the sealed submitter envelope")
	}
	encodedResponse, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if walletSignature == "" || bytes.Contains(encodedResponse, []byte(walletSignature)) {
		t.Fatal("runner-visible Jupiter response contains the transaction signature")
	}
	if err := VerifyResponseAttestation(
		policy.AttestationPublicKey,
		policy.SubmitterPublicKey,
		response,
	); err != nil {
		t.Fatal(err)
	}
	privateKey, _ := signerTestSubmitterKeys(t)
	sealed, err := sealedtx.OpenConfidential(privateKey, response.SealedTransaction)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := solana.DecodeSignedV0Transaction(sealed, tables)
	if err != nil || !bytes.Equal(decoded.Message.Raw, message) {
		t.Fatal("sealed response did not contain the exact signed Jupiter transaction")
	}
	records := authorizationRecords(t, policy.AuthorizationLedgerPath)
	if len(records) != 2 {
		t.Fatalf("Jupiter authorization ledger records = %d, want 2", len(records))
	}
	var reservation authorizationReservation
	if err := json.Unmarshal(records[1].Payload, &reservation); err != nil {
		t.Fatal(err)
	}
	if reservation.DebitLamports != 3_005_010 ||
		reservation.RequestSHA256 != response.RequestSHA256 ||
		reservation.CustodyTimestampMS != now.UnixMilli() {
		t.Fatalf("Jupiter reservation = %+v", reservation)
	}
}

func TestAuthorizeAndSignJupiterFileKeyUsesDistinctBoundKeys(t *testing.T) {
	policy, request := jupiterSignerFixture(t)
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	grantJupiterSignerRequest(t, policy, &request, now)
	response, err := AuthorizeAndSignJupiterFileKey(
		context.Background(),
		policy,
		signerTestKey("Jupiter wallet"),
		signerTestKey("Jupiter response attestor"),
		request,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyResponseAttestation(
		policy.AttestationPublicKey,
		policy.SubmitterPublicKey,
		response,
	); err != nil {
		t.Fatal(err)
	}
	_, tables, err := proposalcheck.ValidateCandidateMaterial(
		*policy.Jupiter,
		*request.JupiterCandidate,
	)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, _ := signerTestSubmitterKeys(t)
	sealed, err := sealedtx.OpenConfidential(privateKey, response.SealedTransaction)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := solana.DecodeSignedV0Transaction(sealed, tables)
	if err != nil || solana.Encode(decoded.Message.AccountKeys[0][:]) != policy.Source {
		t.Fatal("self-hosted canary did not return the exact wallet-signed transaction")
	}
}

func TestAuthorizeAndSignJupiterFileKeyRejectsIdentityDriftBeforeReservation(t *testing.T) {
	for _, test := range []struct {
		name        string
		wallet      ed25519.PrivateKey
		attestation ed25519.PrivateKey
	}{
		{
			name:        "wallet",
			wallet:      signerTestKey("wrong wallet"),
			attestation: signerTestKey("Jupiter response attestor"),
		},
		{
			name:        "attestation",
			wallet:      signerTestKey("Jupiter wallet"),
			attestation: signerTestKey("wrong attestor"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, request := jupiterSignerFixture(t)
			now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
			grantJupiterSignerRequest(t, policy, &request, now)
			if _, err := AuthorizeAndSignJupiterFileKey(
				context.Background(), policy, test.wallet, test.attestation, request, now,
			); err == nil {
				t.Fatal("mismatched self-hosted identity was accepted")
			}
			if _, err := os.Stat(policy.AuthorizationLedgerPath); !os.IsNotExist(err) {
				t.Fatalf("identity drift touched the authorization ledger: %v", err)
			}
		})
	}
}

func TestAuthorizeAndSignJupiterRejectsCustodyDrift(t *testing.T) {
	for name, mutate := range map[string]func([]byte) []byte{
		"different message": func(transaction []byte) []byte {
			transaction[len(transaction)-1] ^= 1
			return transaction
		},
		"legacy framing": func(transaction []byte) []byte {
			transaction[65] &^= 0x80
			return transaction
		},
		"wrong signature": func(transaction []byte) []byte {
			signature := ed25519.Sign(
				signerTestKey("wrong Jupiter wallet"),
				transaction[1+ed25519.SignatureSize:],
			)
			copy(transaction[1:1+ed25519.SignatureSize], signature)
			return transaction
		},
	} {
		t.Run(name, func(t *testing.T) {
			policy, request := jupiterSignerFixture(t)
			now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
			grantJupiterSignerRequest(t, policy, &request, now)
			message, tables, err := proposalcheck.ValidateCandidateMaterial(
				*policy.Jupiter,
				*request.JupiterCandidate,
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = AuthorizeAndSignJupiterWith(
				context.Background(),
				policy,
				request,
				now,
				func(context.Context, TransactionCustodyRequest) ([]byte, error) {
					transaction, _, signErr := solana.SignV0Message(
						signerTestKey("Jupiter wallet"),
						message,
						tables,
					)
					return mutate(transaction), signErr
				},
				func(_ context.Context, claims []byte) ([]byte, error) {
					return ed25519.Sign(signerTestKey("Jupiter response attestor"), claims), nil
				},
			)
			if err == nil {
				t.Fatal("custody signer drift was accepted")
			}
			records := authorizationRecords(t, policy.AuthorizationLedgerPath)
			if len(records) != 2 || records[1].Type != authorizationReserveType {
				t.Fatalf("custody attempt was not durably reserved: %+v", records)
			}
		})
	}
}

func TestAuthorizeAndSignJupiterVerifiesGrantBeforeCustody(t *testing.T) {
	policy, request := jupiterSignerFixture(t)
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	called := false
	_, err := AuthorizeAndSignJupiterWith(
		context.Background(),
		policy,
		request,
		now,
		func(context.Context, TransactionCustodyRequest) ([]byte, error) {
			called = true
			return nil, nil
		},
		func(context.Context, []byte) ([]byte, error) { return nil, nil },
	)
	if err == nil || called {
		t.Fatal("custody signer was reached before risk authorization")
	}
}

func TestAuthorizeAndSignJupiterChecksDailyCapBeforeCustody(t *testing.T) {
	policy, request := jupiterSignerFixture(t)
	policy.DailyDebitCapLamports = policy.MaxLamports + policy.MaxFeeLamports +
		policy.Jupiter.MaxTokenAccountRentLamports
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	validated, err := ValidateJupiterRequest(policy, request)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := reservationForValidated(request, validated, now)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := openAuthorizationLedger(policy, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.reserve(now, "earlier-action", reservation); err != nil {
		t.Fatal(err)
	}
	if err := ledger.close(); err != nil {
		t.Fatal(err)
	}
	beforeRecords := len(authorizationRecords(t, policy.AuthorizationLedgerPath))
	grantJupiterSignerRequest(t, policy, &request, now)
	called := false
	_, err = AuthorizeAndSignJupiterWith(
		context.Background(),
		policy,
		request,
		now,
		func(context.Context, TransactionCustodyRequest) ([]byte, error) {
			called = true
			return nil, nil
		},
		func(context.Context, []byte) ([]byte, error) { return nil, nil },
	)
	if err == nil || !strings.Contains(err.Error(), "daily debit cap") || called {
		t.Fatalf("pre-custody daily cap check = %v, called = %v", err, called)
	}
	if records := authorizationRecords(t, policy.AuthorizationLedgerPath); len(records) != beforeRecords {
		t.Fatalf("daily-cap refusal appended a reservation: %+v", records)
	}
}

func TestAuthorizeAndSignJupiterHonorsCustodyCancellation(t *testing.T) {
	policy, request := jupiterSignerFixture(t)
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	grantJupiterSignerRequest(t, policy, &request, now)
	message, tables, err := proposalcheck.ValidateCandidateMaterial(
		*policy.Jupiter, *request.JupiterCandidate,
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("before reservation", func(t *testing.T) {
		policy, request := cloneJupiterFixture(policy, request)
		policy.AuthorizationLedgerPath = filepath.Join(filepath.Dir(policy.AuthorizationLedgerPath), "canceled.jsonl")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		called := false
		_, err := AuthorizeAndSignJupiterWith(
			ctx, policy, request, now,
			func(context.Context, TransactionCustodyRequest) ([]byte, error) {
				called = true
				return nil, nil
			},
			func(context.Context, []byte) ([]byte, error) { return nil, nil },
		)
		if err == nil || !strings.Contains(err.Error(), "canceled") || called {
			t.Fatalf("pre-canceled custody = %v, called = %v", err, called)
		}
		if _, statErr := os.Stat(policy.AuthorizationLedgerPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("pre-canceled custody created a ledger: %v", statErr)
		}
	})

	t.Run("after possible signature", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		attested := false
		var firstCustody TransactionCustodyRequest
		_, err := AuthorizeAndSignJupiterWith(
			ctx, policy, request, now,
			func(callbackCtx context.Context, custody TransactionCustodyRequest) ([]byte, error) {
				if callbackCtx != ctx {
					t.Fatal("custody callback did not receive the caller context")
				}
				firstCustody = custody
				cancel()
				transaction, _, signErr := solana.SignV0Message(
					signerTestKey("Jupiter wallet"), message, tables,
				)
				return transaction, signErr
			},
			func(context.Context, []byte) ([]byte, error) {
				attested = true
				return nil, nil
			},
		)
		if err == nil || !strings.Contains(err.Error(), "canceled") || attested {
			t.Fatalf("mid-custody cancellation = %v, attested = %v", err, attested)
		}
		if records := authorizationRecords(t, policy.AuthorizationLedgerPath); len(records) != 2 {
			t.Fatalf("possible signature was not durably reserved: %+v", records)
		}

		retryNow := now.Add(time.Second)
		_, err = AuthorizeAndSignJupiterWith(
			context.Background(), policy, request, retryNow,
			func(_ context.Context, custody TransactionCustodyRequest) ([]byte, error) {
				if custody.RequestSHA256 != firstCustody.RequestSHA256 ||
					custody.TimestampMS != firstCustody.TimestampMS ||
					!bytes.Equal(custody.Transaction, firstCustody.Transaction) {
					t.Fatal("exact retry changed the durable custody request")
				}
				transaction, _, signErr := solana.SignV0Message(
					signerTestKey("Jupiter wallet"), message, tables,
				)
				return transaction, signErr
			},
			func(_ context.Context, claims []byte) ([]byte, error) {
				return ed25519.Sign(signerTestKey("Jupiter response attestor"), claims), nil
			},
		)
		if err != nil {
			t.Fatalf("exact retry after cancellation: %v", err)
		}
		if records := authorizationRecords(t, policy.AuthorizationLedgerPath); len(records) != 2 {
			t.Fatalf("exact retry duplicated reservation: %+v", records)
		}
	})

	t.Run("during attestation", func(t *testing.T) {
		policy, request := cloneJupiterFixture(policy, request)
		policy.AuthorizationLedgerPath = filepath.Join(
			filepath.Dir(policy.AuthorizationLedgerPath), "attestation-canceled.jsonl",
		)
		ctx, cancel := context.WithCancel(context.Background())
		attested := false
		_, err := AuthorizeAndSignJupiterWith(
			ctx, policy, request, now,
			func(context.Context, TransactionCustodyRequest) ([]byte, error) {
				transaction, _, signErr := solana.SignV0Message(
					signerTestKey("Jupiter wallet"), message, tables,
				)
				return transaction, signErr
			},
			func(callbackCtx context.Context, claims []byte) ([]byte, error) {
				if callbackCtx != ctx {
					t.Fatal("attestation callback did not receive the caller context")
				}
				attested = true
				cancel()
				return ed25519.Sign(signerTestKey("Jupiter response attestor"), claims), nil
			},
		)
		if err == nil || !strings.Contains(err.Error(), "attestation was canceled") || !attested {
			t.Fatalf("mid-attestation cancellation = %v, attested = %v", err, attested)
		}
		if records := authorizationRecords(t, policy.AuthorizationLedgerPath); len(records) != 2 {
			t.Fatalf("attested attempt was not durably reserved: %+v", records)
		}
	})
}

func TestRequestLimitCarriesLargeLookupTableEvidence(t *testing.T) {
	contents := base64.StdEncoding.EncodeToString(make([]byte, 256*32))
	tables := make([]jupiterswap.AddressTableEvidence, 32)
	for index := range tables {
		tables[index] = jupiterswap.AddressTableEvidence{
			Address: strings.Repeat("1", 44), AddressesBase64: contents,
		}
	}
	message := base64.StdEncoding.EncodeToString(make([]byte, 1232))
	request := Request{
		MessageBase64: message,
		JupiterCandidate: &proposalcheck.Candidate{
			MessageBase64: message, AddressTables: tables,
		},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) <= 64<<10 || len(encoded) > MaxRequestBytes {
		t.Fatalf("large portable request size = %d, limit = %d", len(encoded), MaxRequestBytes)
	}
}

func cloneJupiterFixture(policy Policy, request Request) (Policy, Request) {
	jupiterPolicy := *policy.Jupiter
	policy.Jupiter = &jupiterPolicy
	candidate := *request.JupiterCandidate
	candidate.AddressTables = append([]jupiterswap.AddressTableEvidence(nil), candidate.AddressTables...)
	request.JupiterCandidate = &candidate
	providers := *request.JupiterProviders
	request.JupiterProviders = &providers
	return policy, request
}

func TestValidateJupiterRequestChecksExactBoundedCandidate(t *testing.T) {
	basePolicy, baseRequest := jupiterSignerFixture(t)
	validated, err := ValidateJupiterRequest(basePolicy, baseRequest)
	if err != nil {
		t.Fatal(err)
	}
	if validated.AmountLamports != 10 || validated.InputAmount != 10 ||
		validated.MinimumOutput != 20 || validated.NativeDebitLamports != 5_010 ||
		validated.TemporaryRentLamports != 3_000_000 ||
		validated.DebitLamports != 3_005_010 || len(validated.MessageSHA256) != 64 {
		t.Fatalf("validated request = %+v", validated)
	}

	tests := map[string]func(*Policy, *Request){
		"protected policy": func(policy *Policy, _ *Request) { policy.Jupiter.MaxInputAmount++ },
		"funded risk authority": func(policy *Policy, _ *Request) {
			source, err := solana.Decode32(policy.Source)
			if err != nil {
				t.Fatal(err)
			}
			policy.RiskAuthorityPublicKey = hex.EncodeToString(source[:])
		},
		"funded attestor": func(policy *Policy, _ *Request) { policy.AttestationPublicKey = policy.Source },
		"risk attestor": func(policy *Policy, _ *Request) {
			key, err := riskgrant.DecodePublicKey(policy.RiskAuthorityPublicKey)
			if err != nil {
				t.Fatal(err)
			}
			policy.AttestationPublicKey = solana.Encode(key)
		},
		"funded submitter": func(policy *Policy, _ *Request) {
			source, err := solana.Decode32(policy.Source)
			if err != nil {
				t.Fatal(err)
			}
			policy.SubmitterPublicKey = hex.EncodeToString(source[:])
		},
		"risk submitter": func(policy *Policy, _ *Request) {
			policy.SubmitterPublicKey = policy.RiskAuthorityPublicKey
		},
		"low-order submitter": func(policy *Policy, _ *Request) {
			policy.SubmitterPublicKey = strings.Repeat("0", 64)
		},
		"attestor submitter": func(policy *Policy, _ *Request) {
			attestor, err := solana.Decode32(policy.AttestationPublicKey)
			if err != nil {
				t.Fatal(err)
			}
			policy.SubmitterPublicKey = hex.EncodeToString(attestor[:])
		},
		"candidate policy":   func(_ *Policy, request *Request) { request.JupiterCandidate.Policy.MaxInputAmount++ },
		"candidate request":  func(_ *Policy, request *Request) { request.JupiterCandidate.Request.InputAmount++ },
		"candidate quote":    func(_ *Policy, request *Request) { request.JupiterCandidate.Quote.MinimumOutput++ },
		"candidate message":  func(_ *Policy, request *Request) { request.JupiterCandidate.MessageBase64 += "A" },
		"candidate lifetime": func(_ *Policy, request *Request) { request.JupiterCandidate.LastValidBlockHeight++ },
		"outer message":      func(_ *Policy, request *Request) { request.MessageBase64 += "A" },
		"recent blockhash":   func(_ *Policy, request *Request) { request.RecentBlockhash += "x" },
		"action ID":          func(_ *Policy, request *Request) { request.ActionID = strings.Repeat("0", 64) },
		"fee":                func(_ *Policy, request *Request) { request.FeeLamports = 20_001 },
		"fee context skew": func(_ *Policy, request *Request) {
			request.SecondaryFeeContextSlot = request.PrimaryFeeContextSlot +
				proposalcheck.MaxEvidenceSlotSkew + 1
		},
		"block height": func(_ *Policy, request *Request) { request.ObservedBlockHeight = request.LastValidBlockHeight },
		"provider binding": func(_ *Policy, request *Request) {
			request.JupiterProviders.PrimaryTrustDomain = request.JupiterProviders.SecondaryTrustDomain
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			policy, request := cloneJupiterFixture(basePolicy, baseRequest)
			mutate(&policy, &request)
			if _, err := ValidateJupiterRequest(policy, request); err == nil {
				t.Fatal("mutated Jupiter request validated")
			}
		})
	}
}

func TestJupiterTokenInputLimitsSeparateTokenAndNativeCaps(t *testing.T) {
	policy, request := jupiterSignerFixture(t)
	policy.Jupiter.InputMint = solana.Encode(bytes.Repeat([]byte{31}, 32))
	policy.Jupiter.OutputMint = orcaswap.WrappedSOLMint
	policy.MaxLamports, policy.DailyDebitCapLamports = 0, 0
	policy.MaxInputTokenAmount = 10
	policy.DailyInputTokenCap = 20
	policy.DailyNativeFeeCapLamports = 30_000
	if !jupiterAmountPolicyValid(policy) {
		t.Fatal("bounded token-input policy was rejected")
	}
	intent := jupiterswap.MessageIntent{Intent: jupiterswap.Intent{InputAmount: 10, MinimumOutput: 20}}
	validated, err := jupiterValidatedAmounts(policy, request, intent)
	if err != nil {
		t.Fatal(err)
	}
	if validated.AmountLamports != 0 || validated.NativeDebitLamports != request.FeeLamports ||
		validated.InputAmount != 10 || validated.DebitLamports !=
		request.FeeLamports+policy.Jupiter.MaxTokenAccountRentLamports {
		t.Fatalf("token-input debit = %+v", validated)
	}

	policy.DailyNativeFeeCapLamports = 0
	if jupiterAmountPolicyValid(policy) {
		t.Fatal("token-input policy without a daily native fee cap was accepted")
	}
}

func TestJupiterNativeInputPolicyFundsOneMaximumAction(t *testing.T) {
	policy, _ := jupiterSignerFixture(t)
	minimum := policy.MaxLamports + policy.MaxFeeLamports +
		policy.Jupiter.MaxTokenAccountRentLamports
	policy.DailyDebitCapLamports = minimum - 1
	if jupiterAmountPolicyValid(policy) {
		t.Fatal("policy accepted a daily cap too small for one maximum action")
	}
	policy.DailyDebitCapLamports = minimum
	if !jupiterAmountPolicyValid(policy) {
		t.Fatal("policy rejected the exact one-action daily cap")
	}
	policy.MaxLamports = ^uint64(0)
	if jupiterAmountPolicyValid(policy) {
		t.Fatal("policy accepted an overflowing maximum debit")
	}
}

func TestJupiterTokenInputReservationSurvivesLedgerReopen(t *testing.T) {
	policy, request := jupiterSignerFixture(t)
	inputMint := solana.Encode(bytes.Repeat([]byte{31}, 32))
	policy.Jupiter.InputMint, policy.Jupiter.OutputMint = inputMint, orcaswap.WrappedSOLMint
	policy.MaxLamports, policy.DailyDebitCapLamports = 0, 0
	policy.MaxInputTokenAmount = 10
	policy.DailyInputTokenCap = 20
	policy.DailyNativeFeeCapLamports = 30_000
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	validated := ValidatedRequest{
		MessageSHA256: strings.Repeat("a", 64), InputMint: inputMint,
		OutputMint: orcaswap.WrappedSOLMint, InputAmount: 10, MinimumOutput: 20,
		NativeDebitLamports:   request.FeeLamports,
		TemporaryRentLamports: policy.Jupiter.MaxTokenAccountRentLamports,
	}
	reservation, err := tokenReservationForValidated(
		request, strings.Repeat("b", 64), validated.MessageSHA256, validated, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	reservation.CustodyTimestampMS = now.UnixMilli()
	ledger, err := openBuyAuthorizationLedger(policy, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.reserveEffective(now, request.ActionID, reservation); err != nil {
		t.Fatal(err)
	}
	if err := ledger.close(); err != nil {
		t.Fatal(err)
	}
	ledger, err = openBuyAuthorizationLedger(policy, now)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.close()
	if got := ledger.reservations[request.ActionID]; got != reservation {
		t.Fatalf("reloaded token-input reservation = %+v", got)
	}
}

func TestValidateJupiterRequestRejectsScheduleOverflow(t *testing.T) {
	policy, request := jupiterSignerFixture(t)
	policy.ScheduleAnchorUnix = int64(^uint64(0)>>1) / 86_400 * 86_400
	request.ScheduleWindowStartUnix = policy.ScheduleAnchorUnix
	request.ScheduleWindowEndUnix = request.ScheduleWindowStartUnix +
		int64(policy.ScheduleWindowSeconds)
	if _, err := ValidateJupiterRequest(policy, request); err == nil {
		t.Fatal("overflowed Jupiter schedule window validated")
	}
}

func TestCustodyTimestampRejectsUnrepresentableTime(t *testing.T) {
	const maxInt64 = int64(^uint64(0) >> 1)
	if _, err := custodyTimestampMS(time.Unix(maxInt64/1_000+1, 0)); err == nil {
		t.Fatal("unrepresentable custody timestamp was accepted")
	}
}

func TestJupiterCandidateIsHashBoundButCannotAuthorizeSigning(t *testing.T) {
	policy, request := jupiterSignerFixture(t)
	digest := sha256.Sum256(mustDecodeBase64(t, request.MessageBase64))
	base, err := RiskBinding(request, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	_, changed := cloneJupiterFixture(policy, request)
	changed.JupiterCandidate.LastValidBlockHeight++
	changedBinding, err := RiskBinding(changed, base.MessageSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if changedBinding.RequestSHA256 == base.RequestSHA256 {
		t.Fatal("Jupiter candidate did not change the immutable request hash")
	}
	if err := policy.Validate(); err == nil {
		t.Fatal("Mainnet policy reached the existing signing path")
	}
	if _, err := AuthorizeAndSign(policy, signerTestKey("unused"), request, time.Now()); err == nil {
		t.Fatal("Mainnet request was signed")
	}
	if _, err := os.Stat(policy.AuthorizationLedgerPath); !os.IsNotExist(err) {
		t.Fatalf("Mainnet refusal touched the authorization ledger: %v", err)
	}
}

func TestRequestFromJupiterRecheckBindsFreshEvidence(t *testing.T) {
	policy, request := jupiterSignerFixture(t)
	validated, err := ValidateJupiterRequest(policy, request)
	if err != nil {
		t.Fatal(err)
	}
	checked := proposalcheck.Result{
		Status:  proposalcheck.StatusCheckedNotAuthorized,
		Reason:  proposalcheck.ReasonSigningPolicyAbsent,
		Cluster: "mainnet-beta", PolicySHA256: policy.ProfileFingerprint,
		InputMint:          request.JupiterCandidate.Request.InputMint,
		OutputMint:         request.JupiterCandidate.Request.OutputMint,
		MinimumContextSlot: 110,
		InputAmount:        request.JupiterCandidate.Quote.InputAmount,
		EstimatedOutput:    request.JupiterCandidate.Quote.EstimatedOutput,
		MinimumOutput:      request.JupiterCandidate.Quote.MinimumOutput,
		MessageSHA256:      validated.MessageSHA256, FeeLamports: 6_000,
		FeeMinContextSlot: 110, PrimaryFeeContextSlot: 111,
		SecondaryFeeContextSlot: 112,
		TokenAccountRent:        3_000_000,
		MaximumDebitLamports:    6_010,
		MaximumUpfrontLamports:  3_006_010,
		LastValidBlockHeight:    request.LastValidBlockHeight,
		ObservedBlockHeight:     120,
		PrimaryTrustDomain:      request.JupiterProviders.PrimaryTrustDomain,
		PrimaryOriginSHA256:     request.JupiterProviders.PrimaryOriginSHA256,
		SecondaryTrustDomain:    request.JupiterProviders.SecondaryTrustDomain,
		SecondaryOriginSHA256:   request.JupiterProviders.SecondaryOriginSHA256,
		ArchiveProbeSignature:   request.JupiterProviders.ArchiveProbeSignature,
	}
	built, err := RequestFromJupiterRecheck(
		policy, *request.JupiterProviders, *request.JupiterCandidate, checked,
		request.ScheduleWindowStartUnix,
	)
	if err != nil {
		t.Fatal(err)
	}
	if built.BlockhashContextSlot != 110 || built.FeeLamports != 6_000 ||
		built.PrimaryFeeContextSlot != 111 || built.SecondaryFeeContextSlot != 112 ||
		built.ObservedBlockHeight != 120 || built.JupiterCandidate == request.JupiterCandidate ||
		built.JupiterProviders == request.JupiterProviders ||
		*built.JupiterProviders != *request.JupiterProviders {
		t.Fatalf("built request = %+v", built)
	}

	for name, mutate := range map[string]func(*proposalcheck.Result){
		"status":              func(value *proposalcheck.Result) { value.Status = "authorized" },
		"message":             func(value *proposalcheck.Result) { value.MessageSHA256 = strings.Repeat("0", 64) },
		"fee minimum":         func(value *proposalcheck.Result) { value.FeeMinContextSlot-- },
		"primary fee context": func(value *proposalcheck.Result) { value.PrimaryFeeContextSlot = 109 },
		"lifetime":            func(value *proposalcheck.Result) { value.LastValidBlockHeight++ },
		"authority":           func(value *proposalcheck.Result) { value.SigningEnabled = true },
		"debit":               func(value *proposalcheck.Result) { value.MaximumDebitLamports++ },
		"rent":                func(value *proposalcheck.Result) { value.TokenAccountRent++ },
		"provider":            func(value *proposalcheck.Result) { value.PrimaryTrustDomain = "other" },
		"archive probe": func(value *proposalcheck.Result) {
			value.ArchiveProbeSignature = solana.Encode(bytes.Repeat([]byte{8}, 64))
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := checked
			mutate(&changed)
			if _, err := RequestFromJupiterRecheck(
				policy, *request.JupiterProviders, *request.JupiterCandidate, changed,
				request.ScheduleWindowStartUnix,
			); err == nil {
				t.Fatal("drifted recheck result produced a signer request")
			}
		})
	}
}

func TestDevnetRejectsHiddenJupiterMaterial(t *testing.T) {
	policy, _, request := signerFixture(t)
	jupiterPolicy, jupiterRequest := jupiterSignerFixture(t)
	policy.Jupiter = jupiterPolicy.Jupiter
	if err := policy.Validate(); err == nil {
		t.Fatal("Devnet policy accepted hidden Jupiter policy")
	}
	policy.Jupiter = nil
	request.JupiterCandidate = jupiterRequest.JupiterCandidate
	if _, err := ValidateRequest(policy, request); err == nil {
		t.Fatal("Devnet request accepted hidden Jupiter candidate")
	}
	request.JupiterCandidate = nil
	request.JupiterProviders = jupiterRequest.JupiterProviders
	if _, err := ValidateRequest(policy, request); err == nil {
		t.Fatal("Devnet request accepted hidden Jupiter provider bindings")
	}
}

func mustDecodeBase64(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
