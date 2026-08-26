package proposalcheck

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

type fakeBuilder struct {
	result jupiterquote.BuildResult
	calls  int
}

func TestJupiterLamportBoundsSeparateTokenSpendFromNativeFees(t *testing.T) {
	policy := jupiterswap.Policy{InputMint: solana.Encode(bytes.Repeat([]byte{1}, 32)), OutputMint: orcaswap.WrappedSOLMint}
	outputRent, debit, upfront, err := jupiterLamportBounds(
		policy, false, 1_000_000, 5_000, 2_039_280,
	)
	if err != nil || outputRent != 0 || debit != 5_000 || upfront != 2_044_280 {
		t.Fatalf("token-input bounds = %d, %d, %d, %v", outputRent, debit, upfront, err)
	}
	if _, _, _, err := jupiterLamportBounds(
		policy, true, 1, 1, 1,
	); err == nil {
		t.Fatal("token-input accounting accepted a persistent output account")
	}
}

func TestVerifyTradeAccountUsesDirectionSpecificEvidence(t *testing.T) {
	policy, request, _ := proposalFixture()
	nativeInput := &fakeEvidence{}
	if err := verifyTradeAccount(t.Context(), nativeInput, policy, request, 100); err != nil {
		t.Fatal(err)
	}
	if nativeInput.outputAccountCalls != 1 || nativeInput.inputAccountCalls != 0 ||
		nativeInput.outputAccountAddress != request.DestinationTokenAccount ||
		nativeInput.outputAccountMint != policy.OutputMint ||
		nativeInput.outputAccountOwner != policy.Owner ||
		nativeInput.outputAccountContextSlot != 100 {
		t.Fatalf("native-input evidence = %+v", nativeInput)
	}

	policy.InputMint = policy.OutputMint
	policy.OutputMint = orcaswap.WrappedSOLMint
	request.InputMint = policy.InputMint
	request.OutputMint = policy.OutputMint
	request.DestinationTokenAccount = ""
	request.InputAmount = 25
	inputAccount, err := orcaswap.AssociatedTokenAddress(policy.Owner, policy.InputMint)
	if err != nil {
		t.Fatal(err)
	}
	tokenInput := &fakeEvidence{}
	if err := verifyTradeAccount(t.Context(), tokenInput, policy, request, 101); err != nil {
		t.Fatal(err)
	}
	if tokenInput.inputAccountCalls != 1 || tokenInput.outputAccountCalls != 0 ||
		tokenInput.inputAccountAddress != inputAccount ||
		tokenInput.inputAccountMint != policy.InputMint ||
		tokenInput.inputAccountOwner != policy.Owner ||
		tokenInput.inputAccountAmount != request.InputAmount ||
		tokenInput.inputAccountContextSlot != 101 {
		t.Fatalf("token-input evidence = %+v", tokenInput)
	}

	tokenInput.inputAccountErr = errors.New("not funded")
	if err := verifyTradeAccount(t.Context(), tokenInput, policy, request, 101); err == nil {
		t.Fatal("token-input evidence failure was ignored")
	}
}

func (f *fakeBuilder) Build(context.Context, jupiterquote.Request) (jupiterquote.BuildResult, error) {
	f.calls++
	return f.result, nil
}

type fakeEvidence struct {
	primaryIdentity           string
	secondaryIdentity         string
	genesisErr                error
	programErr                error
	programSeen               bool
	immutableErr              error
	historyErr                error
	historySeen               bool
	tableErr                  error
	feeErr                    error
	simErr                    error
	blockHeight               uint64
	blockHeightMinContextSlot uint64
	fee                       txflow.FeeEvidence
	simulation                txflow.LegacySimulationEvidence
	simulations               []txflow.LegacySimulationEvidence
	messages                  [][]byte
	feeMessage                []byte
	rent                      txflow.RentEvidence
	rentErr                   error
	rentCalls                 int
	outputAccountErr          error
	outputAccountCalls        int
	outputAccountAddress      string
	outputAccountMint         string
	outputAccountOwner        string
	outputAccountContextSlot  uint64
	inputAccountErr           error
	inputAccountCalls         int
	inputAccountAddress       string
	inputAccountMint          string
	inputAccountOwner         string
	inputAccountAmount        uint64
	inputAccountContextSlot   uint64
	mutateInputs              bool
	mutated                   bool
}

func (f *fakeEvidence) VerifyTokenInputAccount(
	_ context.Context,
	address,
	mint,
	owner string,
	minimumAmount,
	minContextSlot uint64,
) (txflow.TokenAccountEvidence, error) {
	f.inputAccountCalls++
	f.inputAccountAddress = address
	f.inputAccountMint = mint
	f.inputAccountOwner = owner
	f.inputAccountAmount = minimumAmount
	f.inputAccountContextSlot = minContextSlot
	return txflow.TokenAccountEvidence{
		Amount: minimumAmount, PrimaryContextSlot: minContextSlot,
		SecondaryContextSlot: minContextSlot,
	}, f.inputAccountErr
}

func (f *fakeEvidence) VerifyTokenOutputAccount(
	_ context.Context,
	address,
	mint,
	owner string,
	minContextSlot uint64,
) (txflow.TokenAccountEvidence, error) {
	f.outputAccountCalls++
	f.outputAccountAddress = address
	f.outputAccountMint = mint
	f.outputAccountOwner = owner
	f.outputAccountContextSlot = minContextSlot
	return txflow.TokenAccountEvidence{
		PrimaryContextSlot: minContextSlot, SecondaryContextSlot: minContextSlot,
	}, f.outputAccountErr
}

func (f *fakeEvidence) EvidenceProviderIdentities() (string, string) {
	primary, secondary := f.primaryIdentity, f.secondaryIdentity
	if primary == "" {
		primary = primarySlot(0).Identity()
	}
	if secondary == "" {
		secondary = secondarySlot(0).Identity()
	}
	return primary, secondary
}

func (f *fakeEvidence) VerifyTokenAccountRent(
	context.Context,
	uint64,
) (txflow.RentEvidence, error) {
	f.rentCalls++
	if f.rent.Lamports == 0 {
		f.rent.Lamports = 2_039_280
	}
	return f.rent, f.rentErr
}

func (f *fakeEvidence) VerifyGenesis(_ context.Context, expected string) error {
	if expected != solana.MainnetBetaGenesisHash {
		return errors.New("wrong genesis hash")
	}
	return f.genesisErr
}

func (f *fakeEvidence) VerifyFinalizedV0History(_ context.Context, signature string) error {
	f.historySeen = signature == archiveProbeSignature()
	return f.historyErr
}

func (f *fakeEvidence) VerifyUpgradeableProgramDeployment(
	_ context.Context,
	program, programData, authority string,
	deploymentSlot, minContextSlot uint64,
) error {
	f.programSeen = program == jupiterswap.Program && programData == jupiterswap.ProgramData &&
		authority == jupiterswap.UpgradeAuthority && deploymentSlot == jupiterswap.DeploymentSlot &&
		minContextSlot != 0
	return f.programErr
}

func (f *fakeEvidence) VerifyImmutableProgramDeployment(
	_ context.Context,
	program, programData string,
	deploymentSlot, codeLength uint64,
	codeSHA256 string,
	minContextSlot uint64,
) error {
	guard := proposalRouteGuard()
	f.programSeen = f.programSeen && program == guard.Program && programData == guard.ProgramData &&
		deploymentSlot == guard.DeploymentSlot && codeLength == guard.CodeLength &&
		codeSHA256 == guard.CodeSHA256 && minContextSlot != 0
	return f.immutableErr
}

func (f *fakeEvidence) VerifyAddressLookupTables(
	_ context.Context,
	claimed map[[32]byte][][32]byte,
	_ uint64,
) (map[[32]byte][][32]byte, error) {
	return claimed, f.tableErr
}

func (f *fakeEvidence) FeeForV0Message(
	_ context.Context,
	message []byte,
	_ map[[32]byte][][32]byte,
	_ string,
	_ uint64,
) (txflow.FeeEvidence, error) {
	f.feeMessage = bytes.Clone(message)
	if f.mutateInputs && len(message) != 0 {
		message[0] ^= 1
		f.mutated = true
	}
	return f.fee, f.feeErr
}

func (f *fakeEvidence) SimulateV0(
	_ context.Context,
	message []byte,
	_ map[[32]byte][][32]byte,
	_ string,
	_ uint64,
) (txflow.LegacySimulationEvidence, error) {
	f.messages = append(f.messages, bytes.Clone(message))
	if f.mutateInputs && len(message) != 0 {
		message[len(message)-1] ^= 1
		f.mutated = true
	}
	if len(f.simulations) >= len(f.messages) {
		return f.simulations[len(f.messages)-1], f.simErr
	}
	return f.simulation, f.simErr
}

func (f *fakeEvidence) NodeBlockHeight(_ context.Context, minContextSlot uint64) (uint64, error) {
	f.blockHeightMinContextSlot = minContextSlot
	if f.blockHeight == 0 {
		return 100, nil
	}
	return f.blockHeight, nil
}

type fakeSlotReader struct {
	slot     uint64
	identity string
}

func (f fakeSlotReader) FinalizedSlot(context.Context) (uint64, error) { return f.slot, nil }
func (f fakeSlotReader) Identity() string                              { return f.identity }
func primarySlot(slot uint64) fakeSlotReader {
	return fakeSlotReader{slot: slot, identity: hex.EncodeToString(bytes.Repeat([]byte{1}, 32))}
}
func secondarySlot(slot uint64) fakeSlotReader {
	return fakeSlotReader{slot: slot, identity: hex.EncodeToString(bytes.Repeat([]byte{2}, 32))}
}

func checkedProviderBindings() ProviderBindings {
	return ProviderBindings{
		PrimaryTrustDomain: "primary-provider", PrimaryOriginSHA256: primarySlot(0).Identity(),
		SecondaryTrustDomain: "secondary-provider", SecondaryOriginSHA256: secondarySlot(0).Identity(),
		ArchiveProbeSignature: archiveProbeSignature(),
	}
}

func archiveProbeSignature() string {
	return solana.Encode(bytes.Repeat([]byte{7}, 64))
}

func TestProviderBindingsRequireDistinctCanonicalProviders(t *testing.T) {
	valid := checkedProviderBindings()
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ProviderBindings){
		"primary trust":    func(value *ProviderBindings) { value.PrimaryTrustDomain = "" },
		"same trust":       func(value *ProviderBindings) { value.SecondaryTrustDomain = value.PrimaryTrustDomain },
		"primary origin":   func(value *ProviderBindings) { value.PrimaryOriginSHA256 = "bad" },
		"same origin":      func(value *ProviderBindings) { value.SecondaryOriginSHA256 = value.PrimaryOriginSHA256 },
		"uppercase origin": func(value *ProviderBindings) { value.PrimaryOriginSHA256 = strings.Repeat("A", 64) },
		"bad archive probe": func(value *ProviderBindings) {
			value.ArchiveProbeSignature = "bad"
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := valid
			mutate(&changed)
			if err := changed.ValidateArchiveProbe(); err == nil {
				t.Fatal("invalid provider bindings were accepted")
			}
		})
	}
}

func check(
	ctx context.Context,
	builder Builder,
	evidence Evidence,
	primary, secondary FinalizedSlotReader,
	policy jupiterswap.Policy,
	request jupiterquote.Request,
) (Result, error) {
	return Check(
		ctx, builder, evidence, primary, secondary,
		"primary-provider", "secondary-provider", archiveProbeSignature(), policy, request,
	)
}

func TestCheckProducesOnlyNonAuthorizingEvidence(t *testing.T) {
	policy, request, proposal := proposalFixture()
	outputSetup := solana.Instruction{
		Program: orcaswap.AssociatedTokenProgram,
		Accounts: []solana.AccountMeta{
			{Address: request.Taker, Signer: true, Writable: true},
			{Address: request.DestinationTokenAccount, Writable: true},
			{Address: request.Taker},
			{Address: request.OutputMint},
			{Address: orcaswap.SystemProgram},
			{Address: orcaswap.TokenProgram},
		},
		Data: []byte{1},
	}
	proposal.Instructions = append(
		proposal.Instructions[:3:3],
		append([]solana.Instruction{outputSetup}, proposal.Instructions[3:]...)...,
	)
	logs := sha256.Sum256([]byte("logs"))
	builder := &fakeBuilder{result: proposal}
	evidence := &fakeEvidence{
		mutateInputs: true,
		fee: txflow.FeeEvidence{
			Lamports: 5_000, PrimaryContextSlot: 105, SecondaryContextSlot: 106,
		},
		simulations: []txflow.LegacySimulationEvidence{
			{ContextSlot: 109, UnitsConsumed: 40_001, LogsSHA256: hex.EncodeToString(logs[:])},
			{ContextSlot: 110, UnitsConsumed: 40_000, LogsSHA256: hex.EncodeToString(logs[:])},
		},
	}
	result, err := check(
		t.Context(), builder, evidence, primarySlot(100), secondarySlot(110),
		policy, request,
	)
	if err != nil {
		t.Fatal(err)
	}
	if builder.calls != 1 || result.Status != StatusCheckedNotAuthorized ||
		result.Reason != ReasonSigningPolicyAbsent || result.MinimumContextSlot != 100 ||
		len(result.PolicySHA256) != 64 || result.OutputMint != policy.OutputMint ||
		result.ComputeUnitLimit != 48_002 || result.ComputeUnitPrice != 1 ||
		result.FeeMinContextSlot != 100 || result.PrimaryFeeContextSlot != 105 ||
		result.SecondaryFeeContextSlot != 106 ||
		result.MaximumDebitLamports != request.InputAmount+result.FeeLamports ||
		result.MaximumUpfrontLamports != result.MaximumDebitLamports+result.TokenAccountRent ||
		result.ObservedBlockHeight != 100 || result.LastValidBlockHeight != 200 ||
		result.PrimaryTrustDomain != "primary-provider" ||
		result.PrimaryOriginSHA256 != primarySlot(0).Identity() ||
		result.SecondaryTrustDomain != "secondary-provider" ||
		result.SecondaryOriginSHA256 != secondarySlot(0).Identity() ||
		result.ArchiveProbeSignature != archiveProbeSignature() ||
		evidence.blockHeightMinContextSlot != result.MinimumContextSlot ||
		result.SigningEnabled || result.SubmissionEnabled || len(result.MessageSHA256) != 64 ||
		len(evidence.messages) != 2 || result.InstructionCount != 7 ||
		evidence.outputAccountCalls != 1 || !evidence.programSeen ||
		!evidence.historySeen || !evidence.mutated {
		t.Fatalf("unexpected proposal result: %+v", result)
	}
	estimateMessage, err := solana.DecodeV0Message(evidence.messages[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	finalMessage, err := solana.DecodeV0Message(evidence.messages[1], nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(estimateMessage.Instructions[0].Data[1:]); got != solana.MaxComputeUnitLimit {
		t.Fatalf("estimate compute unit limit = %d", got)
	}
	if got := binary.LittleEndian.Uint32(finalMessage.Instructions[0].Data[1:]); got != 48_002 {
		t.Fatalf("final compute unit limit = %d", got)
	}
	if !bytes.Equal(finalMessage.Instructions[1].Data, builder.result.ComputeBudget[0].Data) {
		t.Fatal("final message omitted Jupiter's compute unit price")
	}
	if !bytes.Equal(evidence.feeMessage, evidence.messages[1]) {
		t.Fatal("fee evidence was not collected for the final simulated message")
	}
	checkedMessage := result.Message()
	if !bytes.Equal(checkedMessage, evidence.messages[1]) {
		t.Fatal("result did not retain the exact checked message")
	}
	checkedMessage[0] ^= 1
	if bytes.Equal(result.Message(), checkedMessage) {
		t.Fatal("checked message aliases caller memory")
	}
}

func TestCheckExplainsUnsupportedJupiterPlanWithoutContinuing(t *testing.T) {
	policy, request, proposal := proposalFixture()
	proposal.Instructions = nil
	builder := &fakeBuilder{result: proposal}
	evidence := &fakeEvidence{}
	_, err := check(
		t.Context(), builder, evidence, primarySlot(100), secondarySlot(100), policy, request,
	)
	if err == nil || !strings.Contains(err.Error(), "no action was taken; retry later") ||
		!strings.Contains(err.Error(), "no supported route") {
		t.Fatalf("unsupported-plan error = %v", err)
	}
	if builder.calls != 1 || evidence.rentCalls != 0 || len(evidence.messages) != 0 {
		t.Fatal("unsupported Jupiter plan continued into proposal execution checks")
	}
}

func TestResultReturnsCopiesOfCheckedMaterial(t *testing.T) {
	table := [32]byte{1}
	result := Result{
		message:       []byte{1, 2, 3},
		addressTables: map[[32]byte][][32]byte{table: {{2}}},
	}
	message := result.Message()
	message[0] = 9
	if result.Message()[0] != 1 {
		t.Fatal("checked message aliases caller memory")
	}
	tables := result.AddressTables()
	tables[table][0][0] = 9
	if result.AddressTables()[table][0][0] != 2 {
		t.Fatal("checked address tables alias caller memory")
	}
}

func TestRecheckRepeatsEvidenceOverTheExactCandidate(t *testing.T) {
	policy, request, proposal := proposalFixture()
	addFallbackLookupAccounts(t, &proposal)
	logs := hex.EncodeToString(make([]byte, 32))
	checked, err := check(
		t.Context(), &fakeBuilder{result: proposal}, &fakeEvidence{
			fee: txflow.FeeEvidence{
				Lamports: 5_000, PrimaryContextSlot: 100, SecondaryContextSlot: 100,
			},
			simulations: []txflow.LegacySimulationEvidence{
				{ContextSlot: 100, UnitsConsumed: 40_000, LogsSHA256: logs},
				{ContextSlot: 100, UnitsConsumed: 40_000, LogsSHA256: logs},
			},
		}, primarySlot(100), secondarySlot(100), policy, request,
	)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := checked.Candidate()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err = DecodeCandidate(encoded)
	if err != nil {
		t.Fatal(err)
	}
	recheckEvidence := &fakeEvidence{
		fee: txflow.FeeEvidence{
			Lamports: 5_000, PrimaryContextSlot: 110, SecondaryContextSlot: 111,
		},
		simulation: txflow.LegacySimulationEvidence{
			ContextSlot: 112, UnitsConsumed: 40_000, LogsSHA256: logs,
		},
		blockHeight: 101,
	}
	rechecked, err := Recheck(
		t.Context(), recheckEvidence, primarySlot(110), secondarySlot(111),
		policy, checkedProviderBindings(), candidate,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !recheckEvidence.programSeen || !recheckEvidence.historySeen ||
		rechecked.MessageSHA256 != checked.MessageSHA256 ||
		rechecked.MinimumContextSlot != 110 || rechecked.ObservedBlockHeight != 101 ||
		recheckEvidence.blockHeightMinContextSlot != rechecked.MinimumContextSlot ||
		rechecked.SigningEnabled || rechecked.SubmissionEnabled {
		t.Fatalf("rechecked result = %+v", rechecked)
	}
	changedReport := checked
	changedReport.LastValidBlockHeight++
	changedReport.PrimaryOriginSHA256 = secondarySlot(0).Identity()
	unchangedCandidate, err := changedReport.Candidate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Recheck(
		t.Context(), recheckEvidence, primarySlot(110), secondarySlot(111),
		policy, checkedProviderBindings(), unchangedCandidate,
	); err != nil {
		t.Fatalf("public report mutation changed retained candidate: %v", err)
	}
	changedProvider := primarySlot(110)
	changedProvider.identity = hex.EncodeToString(bytes.Repeat([]byte{3}, 32))
	if _, err := Recheck(
		t.Context(), recheckEvidence, changedProvider, secondarySlot(111),
		policy, checkedProviderBindings(), candidate,
	); err == nil {
		t.Fatal("recheck accepted a changed evidence provider origin")
	}

	recheckEvidence.tableErr = errors.New("lookup table changed")
	if _, err := Recheck(
		t.Context(), recheckEvidence, primarySlot(110), secondarySlot(111),
		policy, checkedProviderBindings(), candidate,
	); err == nil {
		t.Fatal("recheck accepted changed address-table evidence")
	}
	tampered := candidate
	tamperedMessage, err := base64.StdEncoding.DecodeString(tampered.MessageBase64)
	if err != nil {
		t.Fatal(err)
	}
	tamperedMessage[len(tamperedMessage)-1] ^= 1
	tampered.MessageBase64 = base64.StdEncoding.EncodeToString(tamperedMessage)
	recheckEvidence.tableErr = nil
	if _, err := Recheck(
		t.Context(), recheckEvidence, primarySlot(110), secondarySlot(111),
		policy, checkedProviderBindings(), tampered,
	); err == nil {
		t.Fatal("recheck accepted changed candidate bytes")
	}
	changedPolicy := candidate
	changedPolicy.Policy.MaxInputAmount++
	if _, err := Recheck(
		t.Context(), recheckEvidence, primarySlot(110), secondarySlot(111),
		policy, checkedProviderBindings(), changedPolicy,
	); err == nil {
		t.Fatal("recheck accepted candidate-selected policy")
	}
}

func TestCheckRejectsInvalidProviderBindingsBeforeBuilding(t *testing.T) {
	policy, request, _ := proposalFixture()
	for name, test := range map[string]struct {
		primary, secondary             fakeSlotReader
		primaryDomain, secondaryDomain string
	}{
		"same trust domain": {
			primarySlot(100), secondarySlot(100), "same-provider", "same-provider",
		},
		"invalid trust domain": {
			primarySlot(100), secondarySlot(100), "Provider One", "provider-two",
		},
		"same origin": {
			primarySlot(100), primarySlot(100), "provider-one", "provider-two",
		},
	} {
		t.Run(name, func(t *testing.T) {
			builder := &fakeBuilder{}
			if _, err := Check(
				t.Context(), builder, &fakeEvidence{}, test.primary, test.secondary,
				test.primaryDomain, test.secondaryDomain, archiveProbeSignature(), policy, request,
			); err == nil || builder.calls != 0 {
				t.Fatal("invalid evidence bindings reached the proposal builder")
			}
		})
	}
	builder := &fakeBuilder{}
	if _, err := Check(
		t.Context(), builder, &fakeEvidence{primaryIdentity: secondarySlot(0).Identity()},
		primarySlot(100), secondarySlot(100), "provider-one", "provider-two",
		archiveProbeSignature(), policy, request,
	); err == nil || builder.calls != 0 {
		t.Fatal("slot-reader identities were allowed to differ from the evidence providers")
	}
}

func TestCheckDropsUnusedVerifiedAddressTables(t *testing.T) {
	policy, request, proposal := proposalFixture()
	proposal.ClaimedAddressTables = map[[32]byte][][32]byte{{9}: {{8}}}
	logs := hex.EncodeToString(make([]byte, 32))
	result, err := check(
		t.Context(), &fakeBuilder{result: proposal}, &fakeEvidence{
			fee: txflow.FeeEvidence{
				Lamports: 5_000, PrimaryContextSlot: 100, SecondaryContextSlot: 100,
			},
			simulations: []txflow.LegacySimulationEvidence{
				{ContextSlot: 100, UnitsConsumed: 40_000, LogsSHA256: logs},
				{ContextSlot: 100, UnitsConsumed: 40_000, LogsSHA256: logs},
			},
		}, primarySlot(100), secondarySlot(100), policy, request,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.AddressTableCount != 0 || len(result.AddressTables()) != 0 {
		t.Fatal("static proposal retained an unused verified address table")
	}
}

func TestCheckRejectsInvalidComputeEstimates(t *testing.T) {
	policy, request, proposal := proposalFixture()
	logs := hex.EncodeToString(make([]byte, 32))
	builder := &fakeBuilder{result: proposal}
	for _, units := range []uint64{0, uint64(solana.MaxComputeUnitLimit) + 1} {
		if _, err := check(
			t.Context(), builder, &fakeEvidence{simulation: txflow.LegacySimulationEvidence{
				ContextSlot: 100, UnitsConsumed: units, LogsSHA256: logs,
			}}, primarySlot(100), secondarySlot(100), policy, request,
		); err == nil {
			t.Fatalf("accepted compute estimate %d", units)
		}
	}
	if _, err := check(
		t.Context(), builder, &fakeEvidence{simulation: txflow.LegacySimulationEvidence{
			ContextSlot: 100, UnitsConsumed: 90_000, LogsSHA256: logs,
		}}, primarySlot(100), secondarySlot(100), policy, request,
	); err == nil {
		t.Fatal("accepted compute estimate above the operator policy")
	}
}

func TestCheckRejectsFinalFeeAboveOperatorCap(t *testing.T) {
	policy, request, proposal := proposalFixture()
	logs := hex.EncodeToString(make([]byte, 32))
	_, err := check(
		t.Context(), &fakeBuilder{result: proposal}, &fakeEvidence{
			fee: txflow.FeeEvidence{
				Lamports:           policy.MaxFeeLamports + 1,
				PrimaryContextSlot: 100, SecondaryContextSlot: 100,
			},
			simulations: []txflow.LegacySimulationEvidence{
				{ContextSlot: 100, UnitsConsumed: 10_000, LogsSHA256: logs},
				{ContextSlot: 100, UnitsConsumed: 10_000, LogsSHA256: logs},
			},
		}, primarySlot(100), secondarySlot(100), policy, request,
	)
	if err == nil {
		t.Fatal("accepted final fee above the operator cap")
	}
}

func TestCheckDoesNotRemoveOutputSetupWithoutAccountProof(t *testing.T) {
	policy, request, proposal := proposalFixture()
	outputAccount, err := orcaswap.AssociatedTokenAddress(request.Taker, request.OutputMint)
	if err != nil {
		t.Fatal(err)
	}
	outputATA := solana.Instruction{
		Program: orcaswap.AssociatedTokenProgram,
		Accounts: []solana.AccountMeta{
			{Address: request.Taker, Signer: true, Writable: true},
			{Address: outputAccount, Writable: true}, {Address: request.Taker},
			{Address: request.OutputMint}, {Address: orcaswap.SystemProgram},
			{Address: orcaswap.TokenProgram},
		},
		Data: []byte{1},
	}
	proposal.Instructions = append(
		proposal.Instructions[:3], append([]solana.Instruction{outputATA}, proposal.Instructions[3:]...)...,
	)
	logs := hex.EncodeToString(make([]byte, 32))
	evidence := &fakeEvidence{
		outputAccountErr: errors.New("missing"),
		fee: txflow.FeeEvidence{
			Lamports: 5_000, PrimaryContextSlot: 100, SecondaryContextSlot: 100,
		},
		rent: txflow.RentEvidence{Lamports: 2_039_280},
		simulations: []txflow.LegacySimulationEvidence{
			{ContextSlot: 100, UnitsConsumed: 10_000, LogsSHA256: logs},
			{ContextSlot: 100, UnitsConsumed: 10_000, LogsSHA256: logs},
		},
	}
	_, err = check(
		t.Context(), &fakeBuilder{result: proposal}, evidence,
		primarySlot(100), secondarySlot(100), policy, request,
	)
	if err == nil || evidence.outputAccountCalls != 1 || evidence.rentCalls != 0 {
		t.Fatal("proposal output setup was normalized without independent account proof")
	}
}

func TestCheckRequiresThePreCreatedCanonicalOutputAccount(t *testing.T) {
	policy, request, proposal := proposalFixture()
	logs := hex.EncodeToString(make([]byte, 32))
	evidence := &fakeEvidence{
		outputAccountErr: errors.New("missing"),
		fee: txflow.FeeEvidence{
			Lamports: 5_000, PrimaryContextSlot: 100, SecondaryContextSlot: 100,
		},
		simulations: []txflow.LegacySimulationEvidence{
			{ContextSlot: 100, UnitsConsumed: 10_000, LogsSHA256: logs},
			{ContextSlot: 100, UnitsConsumed: 10_000, LogsSHA256: logs},
		},
	}
	_, err := check(
		t.Context(), &fakeBuilder{result: proposal}, evidence,
		primarySlot(100), secondarySlot(100), policy, request,
	)
	if err == nil || evidence.outputAccountCalls != 1 || evidence.rentCalls != 0 ||
		evidence.outputAccountAddress != request.DestinationTokenAccount ||
		evidence.outputAccountMint != policy.OutputMint ||
		evidence.outputAccountOwner != policy.Owner ||
		evidence.outputAccountContextSlot != 100 {
		t.Fatalf("output account gate = %+v, %v", evidence, err)
	}
}

func TestCheckFailsBeforeOrAfterTheReadOnlyBoundary(t *testing.T) {
	policy, request, proposal := proposalFixture()
	builder := &fakeBuilder{}
	if _, err := check(
		t.Context(), builder, &fakeEvidence{genesisErr: errors.New("wrong cluster")},
		primarySlot(100), secondarySlot(100), policy, request,
	); err == nil || builder.calls != 0 {
		t.Fatal("a wrong cluster reached the proposal builder")
	}
	if _, err := check(
		t.Context(), builder, &fakeEvidence{}, primarySlot(100), secondarySlot(251),
		policy, request,
	); err == nil || builder.calls != 0 {
		t.Fatal("divergent finalized slots reached the proposal builder")
	}
	if _, err := check(
		t.Context(), builder, &fakeEvidence{programErr: errors.New("changed deployment")},
		primarySlot(100), secondarySlot(100), policy, request,
	); err == nil || builder.calls != 0 {
		t.Fatal("an unpinned Jupiter deployment reached the proposal builder")
	}
	if _, err := check(
		t.Context(), builder, &fakeEvidence{immutableErr: errors.New("changed guard")},
		primarySlot(100), secondarySlot(100), policy, request,
	); err == nil || builder.calls != 0 {
		t.Fatal("an unpinned route guard deployment reached the proposal builder")
	}
	if _, err := check(
		t.Context(), builder, &fakeEvidence{historyErr: errors.New("history unavailable")},
		primarySlot(100), secondarySlot(100), policy, request,
	); err == nil || builder.calls != 0 {
		t.Fatal("missing finalized transaction history reached the proposal builder")
	}

	builder.result = proposal
	if _, err := check(
		t.Context(), builder, &fakeEvidence{
			fee: txflow.FeeEvidence{
				Lamports: 5_000, PrimaryContextSlot: 100, SecondaryContextSlot: 100,
			},
			simulation: txflow.LegacySimulationEvidence{
				ContextSlot: 100, UnitsConsumed: 1, LogsSHA256: hex.EncodeToString(make([]byte, 32)),
			},
			blockHeight: 200,
		},
		primarySlot(100), secondarySlot(100),
		policy, request,
	); err == nil {
		t.Fatal("an expired proposal passed the checker")
	}
	tooLong := proposal
	tooLong.LastValidBlockHeight = 251
	if _, err := check(
		t.Context(), &fakeBuilder{result: tooLong}, &fakeEvidence{
			fee: txflow.FeeEvidence{
				Lamports: 5_000, PrimaryContextSlot: 100, SecondaryContextSlot: 100,
			},
			simulation: txflow.LegacySimulationEvidence{
				ContextSlot: 100, UnitsConsumed: 1, LogsSHA256: hex.EncodeToString(make([]byte, 32)),
			},
		}, primarySlot(100), secondarySlot(100), policy, request,
	); err == nil {
		t.Fatal("an excessive proposal lifetime passed the checker")
	}
	for name, simulationSlot := range map[string]uint64{
		"behind": 100,
		"ahead":  500,
	} {
		t.Run("Mithril simulation too far "+name, func(t *testing.T) {
			if _, err := check(
				t.Context(), builder, &fakeEvidence{
					fee: txflow.FeeEvidence{
						Lamports: 5_000, PrimaryContextSlot: 300, SecondaryContextSlot: 300,
					},
					simulation: txflow.LegacySimulationEvidence{
						ContextSlot: simulationSlot, UnitsConsumed: 1,
						LogsSHA256: hex.EncodeToString(make([]byte, 32)),
					},
				},
				primarySlot(300), secondarySlot(300), policy, request,
			); err == nil {
				t.Fatal("an out-of-window Mithril simulation passed the checker")
			}
		})
	}
	if _, err := check(
		t.Context(), builder, &fakeEvidence{
			fee: txflow.FeeEvidence{
				Lamports: 5_000, PrimaryContextSlot: 450, SecondaryContextSlot: 450,
			},
			simulation: txflow.LegacySimulationEvidence{
				ContextSlot: 451, UnitsConsumed: 1,
				LogsSHA256: hex.EncodeToString(make([]byte, 32)),
			},
		}, primarySlot(300), secondarySlot(450), policy, request,
	); err == nil {
		t.Fatal("evidence spanning more than one slot-skew window passed the checker")
	}
}

func proposalFixture() (jupiterswap.Policy, jupiterquote.Request, jupiterquote.BuildResult) {
	addresses := make([]string, 6)
	for index := range addresses {
		addresses[index] = solana.Encode(bytes.Repeat([]byte{byte(index + 1)}, 32))
	}
	request := jupiterquote.Request{
		Taker: addresses[0], InputMint: orcaswap.WrappedSOLMint, OutputMint: addresses[2],
		InputAmount: 10, SlippageBPS: 50,
	}
	inputAccount, _ := orcaswap.AssociatedTokenAddress(request.Taker, request.InputMint)
	outputAccount, _ := orcaswap.AssociatedTokenAddress(request.Taker, request.OutputMint)
	request.DestinationTokenAccount = outputAccount
	transfer := make([]byte, 12)
	binary.LittleEndian.PutUint32(transfer[:4], 2)
	binary.LittleEndian.PutUint64(transfer[4:], request.InputAmount)
	data := []byte{187, 100, 250, 204, 49, 196, 175, 20}
	data = binary.LittleEndian.AppendUint64(data, request.InputAmount)
	data = binary.LittleEndian.AppendUint64(data, 20)
	data = binary.LittleEndian.AppendUint16(data, request.SlippageBPS)
	data = binary.LittleEndian.AppendUint16(data, 0)
	data = binary.LittleEndian.AppendUint16(data, 0)
	data = binary.LittleEndian.AppendUint32(data, 1)
	data = append(data, 17, 1, 0x10, 0x27, 0, 1)
	policy := jupiterswap.Policy{
		Owner: request.Taker, InputMint: request.InputMint, OutputMint: request.OutputMint,
		MaxInputAmount: request.InputAmount, MinOutputAmount: 19,
		MaxSlippageBPS: request.SlippageBPS, MaxComputeUnits: 100_000,
		MaxComputeUnitPriceMicroLamport: 1, MaxFeeLamports: 10_000,
		MaxTokenAccountRentLamports: 3_000_000, RouteGuard: proposalRouteGuard(),
	}
	return policy, request, jupiterquote.BuildResult{
		Quote: jupiterquote.Result{InputAmount: 10, EstimatedOutput: 20, MinimumOutput: 20},
		ComputeBudget: []solana.Instruction{{
			Program: solana.ComputeBudgetProgram, Data: []byte{3, 1, 0, 0, 0, 0, 0, 0, 0},
		}},
		Instructions: []solana.Instruction{
			{
				Program: orcaswap.AssociatedTokenProgram,
				Accounts: []solana.AccountMeta{
					{Address: request.Taker, Signer: true, Writable: true},
					{Address: inputAccount, Writable: true},
					{Address: request.Taker},
					{Address: request.InputMint},
					{Address: orcaswap.SystemProgram},
					{Address: orcaswap.TokenProgram},
				},
				Data: []byte{1},
			},
			{
				Program: orcaswap.SystemProgram,
				Accounts: []solana.AccountMeta{
					{Address: request.Taker, Signer: true, Writable: true},
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
					{Address: request.Taker, Signer: true},
					{Address: inputAccount, Writable: true},
					{Address: outputAccount, Writable: true},
					{Address: request.InputMint},
					{Address: request.OutputMint},
					{Address: "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"},
					{Address: "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"},
					{Address: outputAccount, Writable: true},
					{Address: "D8cy77BBepLMngZx6ZukaTff5hCt1HrWyKk3Hnd9oitf"},
					{Address: jupiterswap.Program},
					{Address: addresses[5], Writable: true},
				},
				Data: data,
			},
			{
				Program: orcaswap.TokenProgram,
				Accounts: []solana.AccountMeta{
					{Address: inputAccount, Writable: true},
					{Address: request.Taker, Writable: true},
					{Address: request.Taker, Signer: true},
				},
				Data: []byte{9},
			},
		},
		RecentBlockhash: [32]byte{3}, LastValidBlockHeight: 200,
	}
}

func proposalRouteGuard() jupiterswap.RouteGuardDeployment {
	code := []byte("proposal route guard")
	hash := sha256.Sum256(code)
	return jupiterswap.RouteGuardDeployment{
		Program:        solana.Encode(bytes.Repeat([]byte{71}, 32)),
		ProgramData:    solana.Encode(bytes.Repeat([]byte{72}, 32)),
		DeploymentSlot: 123, CodeLength: uint64(len(code)), CodeSHA256: hex.EncodeToString(hash[:]),
	}
}
