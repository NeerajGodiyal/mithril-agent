package policyauthority

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/operatorapproval"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

type jupiterEvidence struct {
	primary, secondary string
	rechecks           int
}

func (e *jupiterEvidence) EvidenceProviderIdentities() (string, string) {
	return e.primary, e.secondary
}

func (*jupiterEvidence) VerifyGenesis(_ context.Context, expected string) error {
	if expected != solana.MainnetBetaGenesisHash {
		return errors.New("unexpected genesis")
	}
	return nil
}

func (*jupiterEvidence) VerifyFinalizedV0History(_ context.Context, signature string) error {
	if signature != solana.Encode(bytes.Repeat([]byte{7}, 64)) {
		return errors.New("unexpected archive probe")
	}
	return nil
}

func (*jupiterEvidence) VerifyUpgradeableProgramDeployment(
	_ context.Context,
	program, programData, authority string,
	deploymentSlot, minimumSlot uint64,
) error {
	if program != jupiterswap.Program || programData != jupiterswap.ProgramData ||
		authority != jupiterswap.UpgradeAuthority ||
		deploymentSlot != jupiterswap.DeploymentSlot || minimumSlot == 0 {
		return errors.New("unexpected Jupiter deployment")
	}
	return nil
}

func (*jupiterEvidence) VerifyImmutableProgramDeployment(
	_ context.Context,
	program, programData string,
	deploymentSlot, codeLength uint64,
	codeSHA256 string,
	minimumSlot uint64,
) error {
	guard := jupiterAuthorityRouteGuard()
	if program != guard.Program || programData != guard.ProgramData ||
		deploymentSlot != guard.DeploymentSlot || codeLength != guard.CodeLength ||
		codeSHA256 != guard.CodeSHA256 || minimumSlot == 0 {
		return errors.New("unexpected route guard deployment")
	}
	return nil
}

func (e *jupiterEvidence) VerifyAddressLookupTables(
	_ context.Context,
	claimed map[[32]byte][][32]byte,
	_ uint64,
) (map[[32]byte][][32]byte, error) {
	e.rechecks++
	return claimed, nil
}

func (*jupiterEvidence) FeeForV0Message(
	context.Context,
	[]byte,
	map[[32]byte][][32]byte,
	string,
	uint64,
) (txflow.FeeEvidence, error) {
	return txflow.FeeEvidence{
		Lamports: 5_000, PrimaryContextSlot: 101, SecondaryContextSlot: 102,
	}, nil
}

func (*jupiterEvidence) SimulateV0(
	context.Context,
	[]byte,
	map[[32]byte][][32]byte,
	string,
	uint64,
) (txflow.LegacySimulationEvidence, error) {
	return txflow.LegacySimulationEvidence{
		ContextSlot: 103, UnitsConsumed: 10_000,
		LogsSHA256: strings.Repeat("0", 64),
	}, nil
}

func (*jupiterEvidence) NodeBlockHeight(_ context.Context, minContextSlot uint64) (uint64, error) {
	if minContextSlot == 0 {
		return 0, errors.New("missing minimum context")
	}
	return 100, nil
}

func (*jupiterEvidence) VerifyTokenAccountRent(
	context.Context,
	uint64,
) (txflow.RentEvidence, error) {
	return txflow.RentEvidence{Lamports: 2_039_280}, nil
}

func (*jupiterEvidence) VerifyTokenOutputAccount(
	context.Context,
	string,
	string,
	string,
	uint64,
) (txflow.TokenAccountEvidence, error) {
	return txflow.TokenAccountEvidence{PrimaryContextSlot: 100, SecondaryContextSlot: 100}, nil
}

func (*jupiterEvidence) VerifyTokenInputAccount(
	context.Context,
	string,
	string,
	string,
	uint64,
	uint64,
) (txflow.TokenAccountEvidence, error) {
	return txflow.TokenAccountEvidence{
		Amount: 10, PrimaryContextSlot: 100, SecondaryContextSlot: 100,
	}, nil
}

type jupiterSlot struct {
	slot     uint64
	identity string
}

func (s jupiterSlot) FinalizedSlot(context.Context) (uint64, error) { return s.slot, nil }
func (s jupiterSlot) Identity() string                              { return s.identity }

func TestPrepareJupiterRequestRechecksExactCandidateWithoutAuthority(t *testing.T) {
	policy, candidate, evidence, primary, secondary, scheduleStart := jupiterAuthorityFixture(t)
	now := time.Unix(scheduleStart+1, 0).UTC()
	request, err := PrepareJupiterRequest(
		t.Context(), policy, candidate, scheduleStart, now,
		evidence, primary, secondary,
	)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.rechecks != 0 || request.RiskGrant.SignatureBase64 != "" ||
		request.RiskGrant.Claims.Version != 0 ||
		request.JupiterCandidate == nil || request.JupiterProviders == nil ||
		*request.JupiterProviders != *policy.JupiterProviders {
		t.Fatalf("prepared request did not remain non-authorizing: %+v", request)
	}
	if _, err := signer.ValidateJupiterRequest(policy.TransactionPolicy, request); err != nil {
		t.Fatal(err)
	}
	authoritySeed := sha256.Sum256([]byte("Jupiter authority test"))
	if _, err := Authorize(
		policy, ed25519.NewKeyFromSeed(authoritySeed[:]), request, now,
	); err == nil {
		t.Fatal("prepared Mainnet request was authorized without operator approval")
	}
	approval := jupiterOperatorApproval(t, policy, request)
	grant, err := AuthorizeApproved(
		policy, ed25519.NewKeyFromSeed(authoritySeed[:]), request, approval, now,
	)
	if err != nil || grant.SignatureBase64 == "" {
		t.Fatalf("prepared Mainnet request was not authorized: %v", err)
	}
	changedRequest := request
	changedBindings := *request.JupiterProviders
	changedBindings.PrimaryOriginSHA256 = strings.Repeat("3", 64)
	changedRequest.JupiterProviders = &changedBindings
	if _, err := AuthorizeApproved(
		policy, ed25519.NewKeyFromSeed(authoritySeed[:]), changedRequest, approval, now,
	); err == nil {
		t.Fatal("request-selected provider bindings received a risk grant")
	}

	changed := candidate
	changed.Policy.MaxInputAmount++
	if _, err := PrepareJupiterRequest(
		t.Context(), policy, changed, scheduleStart, now,
		evidence, primary, secondary,
	); err == nil {
		t.Fatal("changed candidate produced a prepared request")
	}

	wrongProviders := policy
	bindings := *policy.JupiterProviders
	bindings.PrimaryOriginSHA256 = strings.Repeat("3", 64)
	wrongProviders.JupiterProviders = &bindings
	if _, err := PrepareJupiterRequest(
		t.Context(), wrongProviders, candidate, scheduleStart, now,
		evidence, primary, secondary,
	); err == nil {
		t.Fatal("changed provider binding produced a prepared request")
	}

	if _, err := PrepareJupiterRequest(
		t.Context(), policy, candidate, scheduleStart,
		time.Unix(scheduleStart-1, 0), evidence, primary, secondary,
	); err == nil {
		t.Fatal("candidate outside its schedule window produced a prepared request")
	}
	if err := validatePrivateKey(policy.TransactionPolicy, ed25519.PrivateKey{1}); err == nil {
		t.Fatal("malformed private key was accepted")
	}
}

func jupiterAuthorityFixture(t *testing.T) (
	Policy,
	proposalcheck.Candidate,
	*jupiterEvidence,
	jupiterSlot,
	jupiterSlot,
	int64,
) {
	t.Helper()
	owner := solana.Encode(bytes.Repeat([]byte{1}, 32))
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
	limit, err := solana.SetComputeUnitLimitInstruction(100_000)
	if err != nil {
		t.Fatal(err)
	}
	priceData := make([]byte, 9)
	priceData[0] = 3
	binary.LittleEndian.PutUint64(priceData[1:], 1)
	route := jupiterswap.Policy{
		Owner: owner, InputMint: orcaswap.WrappedSOLMint,
		OutputMint: outputMint, MaxInputAmount: 10,
		MinOutputAmount: 19, MaxSlippageBPS: 50, MaxComputeUnits: 100_000,
		MaxComputeUnitPriceMicroLamport: 1, MaxFeeLamports: 10_000,
		MaxTokenAccountRentLamports: 3_000_000, RouteGuard: jupiterAuthorityRouteGuard(),
	}
	message, err := jupiterswap.BuildGuardedPolicyV0Message(
		route, owner,
		solana.Encode(bytes.Repeat([]byte{9}, 32)),
		[]solana.Instruction{
			limit,
			{Program: solana.ComputeBudgetProgram, Data: priceData},
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
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := route.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	authoritySeed := sha256.Sum256([]byte("Jupiter authority test"))
	key := ed25519.NewKeyFromSeed(authoritySeed[:])
	publicKey, err := riskgrant.PublicKeyHex(key)
	if err != nil {
		t.Fatal(err)
	}
	submitterSeed := sha256.Sum256([]byte("Jupiter submitter test"))
	submitterPublic, err := sealedtx.PublicKey(hex.EncodeToString(submitterSeed[:]))
	if err != nil {
		t.Fatal(err)
	}
	attestationSeed := sha256.Sum256([]byte("Jupiter attestation test"))
	attestationPublic := solana.Encode(
		ed25519.NewKeyFromSeed(attestationSeed[:]).Public().(ed25519.PublicKey),
	)
	approverSeed := sha256.Sum256([]byte("Jupiter operator approval test"))
	approverPublic := solana.Encode(
		ed25519.NewKeyFromSeed(approverSeed[:]).Public().(ed25519.PublicKey),
	)
	anchor := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC).Unix()
	scheduleStart := anchor + 3_600
	primary := jupiterSlot{100, strings.Repeat("1", 64)}
	secondary := jupiterSlot{101, strings.Repeat("2", 64)}
	evidence := &jupiterEvidence{primary: primary.identity, secondary: secondary.identity}
	policy := Policy{
		TransactionPolicy: signer.Policy{
			Cluster: "mainnet-beta", Profile: jupiterswap.ProfileName,
			ProfileVersion: jupiterswap.ProfileVersion, ProfileFingerprint: fingerprint,
			Source: owner, MaxLamports: 10, MaxFeeLamports: 10_000,
			DailyDebitCapLamports:   3_010_010,
			AuthorizationLedgerPath: filepath.Join(t.TempDir(), "authorization.jsonl"),
			ScheduleWindowSeconds:   3_600, ScheduleAnchorUnix: anchor,
			MaxBlockHeightWindow: 150, RiskAuthorityKeyID: "Jupiter authority",
			RiskAuthorityPublicKey: publicKey, SubmitterPublicKey: submitterPublic,
			AttestationPublicKey: attestationPublic,
			Jupiter:              &route,
		},
		JupiterProviders: &proposalcheck.ProviderBindings{
			PrimaryTrustDomain: "primary", PrimaryOriginSHA256: primary.identity,
			SecondaryTrustDomain: "secondary", SecondaryOriginSHA256: secondary.identity,
			ArchiveProbeSignature: solana.Encode(bytes.Repeat([]byte{7}, 64)),
		},
		OperatorApprover:  approverPublic,
		GrantLifetimeSecs: 30,
	}
	return policy, proposalcheck.Candidate{
		Version: proposalcheck.CandidateVersion, Policy: route,
		Request: quoteRequest, Quote: quote,
		MessageBase64:        base64.StdEncoding.EncodeToString(message),
		LastValidBlockHeight: 200,
	}, evidence, primary, secondary, scheduleStart
}

func jupiterAuthorityRouteGuard() jupiterswap.RouteGuardDeployment {
	code := []byte("authority route guard")
	hash := sha256.Sum256(code)
	return jupiterswap.RouteGuardDeployment{
		Program:        solana.Encode(bytes.Repeat([]byte{71}, 32)),
		ProgramData:    solana.Encode(bytes.Repeat([]byte{72}, 32)),
		DeploymentSlot: 123, CodeLength: uint64(len(code)), CodeSHA256: hex.EncodeToString(hash[:]),
	}
}

func jupiterOperatorApproval(
	t *testing.T,
	policy Policy,
	request signer.Request,
) operatorapproval.Approval {
	t.Helper()
	validated, err := signer.ValidateJupiterRequest(policy.TransactionPolicy, request)
	if err != nil {
		t.Fatal(err)
	}
	review, err := operatorapproval.BuildReview(policy.OperatorApprover, request, validated)
	if err != nil {
		t.Fatal(err)
	}
	seed := sha256.Sum256([]byte("Jupiter operator approval test"))
	key := ed25519.NewKeyFromSeed(seed[:])
	approval, err := operatorapproval.Create(
		policy.OperatorApprover, request, validated,
		solana.Encode(ed25519.Sign(key, []byte(review.Challenge))),
	)
	if err != nil {
		t.Fatal(err)
	}
	return approval
}
