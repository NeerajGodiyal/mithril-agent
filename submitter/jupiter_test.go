package submitter

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestValidateJupiterResponseCannotReachSubmission(t *testing.T) {
	policy, privateKey, request, response := jupiterSubmitterFixture(t)
	if err := ValidateJupiterResponse(policy, privateKey, request, response); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*Policy, *signer.Request, *signer.Response){
		"amount cap": func(policy *Policy, _ *signer.Request, _ *signer.Response) {
			policy.MaxLamports--
		},
		"funded attestor": func(policy *Policy, _ *signer.Request, _ *signer.Response) {
			policy.AttestationPublicKey = policy.Source
		},
		"candidate policy": func(_ *Policy, request *signer.Request, _ *signer.Response) {
			candidate := *request.JupiterCandidate
			candidate.Policy.MaxInputAmount++
			request.JupiterCandidate = &candidate
		},
		"outer message": func(_ *Policy, request *signer.Request, _ *signer.Response) {
			request.MessageBase64 += "A"
		},
		"request hash": func(_ *Policy, _ *signer.Request, response *signer.Response) {
			response.RequestSHA256 = strings.Repeat("0", 64)
		},
		"metadata": func(_ *Policy, _ *signer.Request, response *signer.Response) {
			response.SealedTransaction.Metadata.FeeLamports++
		},
		"fee context skew": func(_ *Policy, request *signer.Request, _ *signer.Response) {
			request.SecondaryFeeContextSlot = request.PrimaryFeeContextSlot +
				proposalcheck.MaxEvidenceSlotSkew + 1
		},
		"request provider": func(_ *Policy, request *signer.Request, _ *signer.Response) {
			providers := *request.JupiterProviders
			providers.PrimaryTrustDomain = "other"
			request.JupiterProviders = &providers
		},
		"request archive probe": func(_ *Policy, request *signer.Request, _ *signer.Response) {
			providers := *request.JupiterProviders
			providers.ArchiveProbeSignature = solana.Encode(bytes.Repeat([]byte{8}, 64))
			request.JupiterProviders = &providers
		},
		"policy provider": func(policy *Policy, _ *signer.Request, _ *signer.Response) {
			policy.Evidence.PrimaryTrustDomain = "other"
		},
		"policy archive probe": func(policy *Policy, _ *signer.Request, _ *signer.Response) {
			policy.Evidence.ArchiveProbeSignature = solana.Encode(bytes.Repeat([]byte{8}, 64))
		},
		"schedule overflow": func(policy *Policy, request *signer.Request, _ *signer.Response) {
			policy.ScheduleAnchorUnix = int64(^uint64(0)>>1) / 86_400 * 86_400
			request.ScheduleWindowStartUnix = policy.ScheduleAnchorUnix
			request.ScheduleWindowEndUnix = request.ScheduleWindowStartUnix +
				int64(policy.ScheduleWindowSeconds)
		},
	} {
		t.Run(name, func(t *testing.T) {
			changedPolicy := policy
			changedRequest := request
			changedResponse := response
			mutate(&changedPolicy, &changedRequest, &changedResponse)
			if err := ValidateJupiterResponse(
				changedPolicy, privateKey, changedRequest, changedResponse,
			); err == nil {
				t.Fatal("drifted Jupiter response validated")
			}
		})
	}

	node := &submitterTestNode{returned: response.Signature}
	if _, err := submitWithGate(
		t.Context(), policy, privateKey, node,
		submitterTestGate{allowed: true}, response, request.BlockhashContextSlot,
	); err == nil || node.transaction != nil {
		t.Fatal("read-only Jupiter validation reached the funded submission path")
	}
}

func TestJupiterSubmitterTokenInputUsesItsTokenCap(t *testing.T) {
	policy, _, _, _ := jupiterSubmitterFixture(t)
	policy.Jupiter.InputMint = solana.Encode(bytes.Repeat([]byte{31}, 32))
	policy.Jupiter.OutputMint = orcaswap.WrappedSOLMint
	policy.MaxLamports = 0
	policy.MaxInputTokenAmount = 10
	if !jupiterSubmitterAmountsValid(policy) || !jupiterRequestAmountValid(policy, 10) ||
		jupiterRequestAmountValid(policy, 11) {
		t.Fatal("token-input submitter cap was not applied independently")
	}
	policy.MaxInputTokenAmount = 0
	if jupiterSubmitterAmountsValid(policy) {
		t.Fatal("token-input submitter policy without a token cap was accepted")
	}
}

func TestJupiterPolicyRequiresSeparateDeliveryIdentity(t *testing.T) {
	policy, _, _, _ := jupiterSubmitterFixture(t)
	lowOrder := policy
	lowOrder.SubmitterPublicKey = strings.Repeat("0", 64)
	if err := ValidateJupiterPolicy(lowOrder); err == nil {
		t.Fatal("low-order submitter identity validated")
	}
	for name, publicKey := range map[string]string{
		"funded wallet": policy.Source,
		"attestor":      policy.AttestationPublicKey,
	} {
		t.Run(name, func(t *testing.T) {
			decoded, err := solana.Decode32(publicKey)
			if err != nil {
				t.Fatal(err)
			}
			changed := policy
			changed.SubmitterPublicKey = hex.EncodeToString(decoded[:])
			if err := ValidateJupiterPolicy(changed); err == nil {
				t.Fatal("reused submitter identity validated")
			}
		})
	}
}

func TestJupiterPolicyRequiresExplicitRecoveryMode(t *testing.T) {
	policy, _, _, _ := jupiterSubmitterFixture(t)
	for _, mode := range []string{"", "retry_anything"} {
		changed := policy
		changed.RecoveryMode = mode
		if err := ValidateJupiterPolicy(changed); err == nil {
			t.Fatalf("recovery mode %q validated", mode)
		}
	}
	policy.RecoveryMode = MainnetRecoveryExactRetry
	if err := ValidateJupiterPolicy(policy); err != nil {
		t.Fatalf("explicit exact retry mode was rejected: %v", err)
	}
}

func jupiterSubmitterFixture(t *testing.T) (Policy, string, signer.Request, signer.Response) {
	t.Helper()
	return jupiterSubmitterFixtureAt(t, time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC).Unix())
}

func jupiterSubmitterFixtureAt(t *testing.T, at int64) (Policy, string, signer.Request, signer.Response) {
	t.Helper()
	walletSeed := sha256.Sum256([]byte("Jupiter submitter wallet"))
	walletKey := ed25519.NewKeyFromSeed(walletSeed[:])
	owner := solana.Encode(walletKey.Public().(ed25519.PublicKey))
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
	blockhash := solana.Encode(bytes.Repeat([]byte{9}, 32))
	route := jupiterswap.Policy{
		Owner: owner, InputMint: orcaswap.WrappedSOLMint,
		OutputMint: outputMint, MaxInputAmount: 10,
		MinOutputAmount: 19, MaxSlippageBPS: 50, MaxComputeUnits: 100_000,
		MaxComputeUnitPriceMicroLamport: 1, MaxFeeLamports: 10_000,
		MaxTokenAccountRentLamports: 3_000_000, RouteGuard: jupiterSubmitterRouteGuard(),
	}
	message, err := jupiterswap.BuildGuardedPolicyV0Message(route, owner, blockhash, []solana.Instruction{
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
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := route.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	anchor := time.Unix(at, 0).UTC().Truncate(24 * time.Hour).Unix()
	scheduleStart := time.Unix(at+3_600, 0).UTC().Truncate(time.Hour).Unix()
	actionID, err := jupiterswap.ComputeActionID(fingerprint, scheduleStart)
	if err != nil {
		t.Fatal(err)
	}
	candidate := proposalcheck.Candidate{
		Version: proposalcheck.CandidateVersion, Policy: route,
		Request: quoteRequest, Quote: quote,
		MessageBase64:        base64.StdEncoding.EncodeToString(message),
		LastValidBlockHeight: 200,
	}
	request := signer.Request{
		Domain: jupiterswap.RequestDomain, Cluster: "mainnet-beta",
		Profile: jupiterswap.ProfileName, ProfileVersion: jupiterswap.ProfileVersion,
		ProfileFingerprint: fingerprint, ActionID: actionID,
		ScheduleWindowStartUnix: scheduleStart, ScheduleWindowEndUnix: scheduleStart + 3_600,
		MessageBase64: candidate.MessageBase64, BlockhashContextSlot: 100,
		FeeLamports: 5_000, FeeMinContextSlot: 100,
		PrimaryFeeContextSlot: 101, SecondaryFeeContextSlot: 102,
		RecentBlockhash: blockhash, ObservedBlockHeight: 100,
		LastValidBlockHeight: 200, JupiterCandidate: &candidate,
		JupiterProviders: &proposalcheck.ProviderBindings{
			PrimaryTrustDomain: "primary", PrimaryOriginSHA256: strings.Repeat("1", 64),
			SecondaryTrustDomain: "secondary", SecondaryOriginSHA256: strings.Repeat("2", 64),
			ArchiveProbeSignature: solana.Encode(bytes.Repeat([]byte{7}, 64)),
		},
	}
	transaction, _, err := solana.SignV0Message(walletKey, message, nil)
	if err != nil {
		t.Fatal(err)
	}
	submitterSeed := sha256.Sum256([]byte("Jupiter submitter envelope"))
	privateKey := hex.EncodeToString(submitterSeed[:])
	publicKey, err := sealedtx.PublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	messageHash := sha256.Sum256(message)
	transactionHash := sha256.Sum256(transaction)
	binding, err := signer.RiskBinding(request, hex.EncodeToString(messageHash[:]))
	if err != nil {
		t.Fatal(err)
	}
	response := signer.Response{
		ActionID: actionID, RequestSHA256: binding.RequestSHA256,
		MessageSHA256:        hex.EncodeToString(messageHash[:]),
		TransactionSHA256:    hex.EncodeToString(transactionHash[:]),
		BlockhashContextSlot: request.BlockhashContextSlot, FeeLamports: request.FeeLamports,
		LastValidBlockHeight: request.LastValidBlockHeight,
	}
	response.SealedTransaction, err = sealedtx.SealConfidential(
		publicKey, responseMetadata(response), transaction, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	attestationSeed := sha256.Sum256([]byte("Jupiter response attestor"))
	attestationKey := ed25519.NewKeyFromSeed(attestationSeed[:])
	attestationPublic := solana.Encode(attestationKey.Public().(ed25519.PublicKey))
	response.SignerAttestation, err = signer.AttestResponse(attestationKey, publicKey, response)
	if err != nil {
		t.Fatal(err)
	}
	return Policy{
		Cluster: "mainnet-beta", Profile: jupiterswap.ProfileName,
		ProfileFingerprint: fingerprint, ControlStatePath: "/private/control.json",
		Source: owner, MaxLamports: 10, MaxFeeLamports: 10_000,
		ScheduleWindowSeconds: 3_600, ScheduleAnchorUnix: anchor,
		MaxBlockHeightWindow: 150,
		RecoveryMode:         MainnetRecoveryStopOnly,
		SubmitterPublicKey:   publicKey, AttestationPublicKey: attestationPublic,
		Evidence: *request.JupiterProviders,
		Jupiter:  &route,
	}, privateKey, request, response
}

func jupiterSubmitterRouteGuard() jupiterswap.RouteGuardDeployment {
	code := []byte("submitter route guard")
	hash := sha256.Sum256(code)
	return jupiterswap.RouteGuardDeployment{
		Program:        solana.Encode(bytes.Repeat([]byte{71}, 32)),
		ProgramData:    solana.Encode(bytes.Repeat([]byte{72}, 32)),
		DeploymentSlot: 123, CodeLength: uint64(len(code)), CodeSHA256: hex.EncodeToString(hash[:]),
	}
}
