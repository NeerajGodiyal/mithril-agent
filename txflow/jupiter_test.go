package txflow

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

func TestReconcileJupiterRequiresMatchingFinalizedEffects(t *testing.T) {
	expected, submission, effect := jupiterEffectFixture(t)
	primary := &fakeProvider{
		identity: "primary", status: finalizedStatus(false), effect: effect,
	}
	secondary := &fakeProvider{
		identity: "secondary", status: finalizedStatus(false), effect: cloneEffect(effect),
	}
	lifecycle, err := New(&fakeProvider{identity: "node"}, primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	result, err := lifecycle.ReconcileJupiterExpected(
		t.Context(), submission, expected, effect.FeeLamports,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictFinalized || result.JupiterEffects == nil ||
		result.JupiterEffects.OutputAmount < expected.MinimumOutput {
		t.Fatalf("reconciliation = %+v", result)
	}

	secondary.effect.PostTokenBalances[0].Amount--
	result, err = lifecycle.ReconcileJupiterExpected(
		t.Context(), submission, expected, effect.FeeLamports,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictDiverged || result.DivergenceKind != DivergenceEffects {
		t.Fatalf("mismatched effects = %+v", result)
	}
}

func TestReconcileJupiterRejectsWrongPayerOrOutputEffects(t *testing.T) {
	expected, submission, base := jupiterEffectFixture(t)
	for name, mutate := range map[string]func(*solanarpc.TransactionEffect){
		"payer delta": func(effect *solanarpc.TransactionEffect) { effect.PostBalances[0]++ },
		"output floor": func(effect *solanarpc.TransactionEffect) {
			effect.PostTokenBalances[0].Amount = effect.PreTokenBalances[1].Amount
		},
		"closed input remains": func(effect *solanarpc.TransactionEffect) {
			decoded, _ := solana.DecodeSignedV0Transaction(effect.Transaction, nil)
			input := messageAccountIndex(decoded.Message.AccountKeys, orcaswapATA(t, expected.Policy.Owner, orcaswap.WrappedSOLMint))
			effect.PostBalances[input] = 1
		},
		"another owner token changes": func(effect *solanarpc.TransactionEffect) {
			decoded, _ := solana.DecodeSignedV0Transaction(effect.Transaction, nil)
			input := messageAccountIndex(decoded.Message.AccountKeys, orcaswapATA(t, expected.Policy.Owner, orcaswap.WrappedSOLMint))
			output := messageAccountIndex(decoded.Message.AccountKeys, orcaswapATA(t, expected.Policy.Owner, expected.Policy.OutputMint))
			other := -1
			for index := 1; index < len(decoded.Message.AccountKeys); index++ {
				if index != input && index != output && decoded.Message.IsWritable(index) {
					other = index
					break
				}
			}
			if other < 0 {
				t.Fatal("fixture has no additional writable account")
			}
			balance := solanarpc.TokenBalance{
				AccountIndex: uint16(other), Mint: expected.Policy.OutputMint,
				Owner: expected.Policy.Owner, Amount: 7,
			}
			effect.PreTokenBalances = append(effect.PreTokenBalances, balance)
			balance.Amount++
			effect.PostTokenBalances = append(effect.PostTokenBalances, balance)
		},
	} {
		t.Run(name, func(t *testing.T) {
			effect := cloneEffect(base)
			mutate(&effect)
			primary := &fakeProvider{identity: "primary", status: finalizedStatus(false), effect: effect}
			secondary := &fakeProvider{identity: "secondary", status: finalizedStatus(false), effect: cloneEffect(effect)}
			lifecycle, err := New(&fakeProvider{identity: "node"}, primary, secondary)
			if err != nil {
				t.Fatal(err)
			}
			result, err := lifecycle.ReconcileJupiterExpected(
				t.Context(), submission, expected, effect.FeeLamports,
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.Verdict != VerdictDiverged || result.DivergenceKind != DivergenceEffects {
				t.Fatalf("mutated effects = %+v", result)
			}
		})
	}
}

func TestReconcileJupiterFailedTransactionOnlyChargesFee(t *testing.T) {
	expected, submission, effect := jupiterEffectFixture(t)
	effect.Failed = true
	effect.ErrorFingerprint = "program_error"
	effect.PostBalances = append([]uint64(nil), effect.PreBalances...)
	effect.PostBalances[0] -= effect.FeeLamports
	effect.PostTokenBalances = append([]solanarpc.TokenBalance(nil), effect.PreTokenBalances...)
	status := finalizedStatus(true)
	status.ErrorFingerprint = effect.ErrorFingerprint

	primary := &fakeProvider{identity: "primary", status: status, effect: effect}
	secondary := &fakeProvider{
		identity: "secondary", status: status, effect: cloneEffect(effect),
	}
	lifecycle, err := New(&fakeProvider{identity: "node"}, primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	result, err := lifecycle.ReconcileJupiterExpected(
		t.Context(), submission, expected, effect.FeeLamports,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictFailed || result.JupiterEffects == nil ||
		result.JupiterEffects.OutputAmount != 0 {
		t.Fatalf("failed reconciliation = %+v", result)
	}

	primary.effect.PostBalances[1]++
	secondary.effect = cloneEffect(primary.effect)
	result, err = lifecycle.ReconcileJupiterExpected(
		t.Context(), submission, expected, effect.FeeLamports,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictDiverged || result.DivergenceKind != DivergenceEffects {
		t.Fatalf("failed transaction with side effect = %+v", result)
	}
}

func TestReconcileJupiterBindsBlockhashExpiryAndFeePolicy(t *testing.T) {
	expected, submission, effect := jupiterEffectFixture(t)
	primary := &fakeProvider{identity: "primary", status: finalizedStatus(false), effect: effect}
	secondary := &fakeProvider{identity: "secondary", status: finalizedStatus(false), effect: cloneEffect(effect)}
	lifecycle, err := New(&fakeProvider{identity: "node"}, primary, secondary)
	if err != nil {
		t.Fatal(err)
	}

	wrongExpiry := expected
	wrongExpiry.LastValidBlockHeight++
	if _, err := lifecycle.ReconcileJupiterExpected(
		t.Context(), submission, wrongExpiry, effect.FeeLamports,
	); err == nil {
		t.Fatal("mismatched blockhash expiry was accepted")
	}
	wrongBlockhash := expected
	wrongBlockhash.RecentBlockhash = solana.Encode(bytes.Repeat([]byte{8}, 32))
	result, err := lifecycle.ReconcileJupiterExpected(
		t.Context(), submission, wrongBlockhash, effect.FeeLamports,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictDiverged || result.DivergenceKind != DivergenceEffects {
		t.Fatalf("mismatched blockhash result = %+v", result)
	}
	if _, err := lifecycle.ReconcileJupiterExpected(
		t.Context(), submission, expected, expected.Policy.MaxFeeLamports+1,
	); err == nil {
		t.Fatal("fee above the checked policy cap was accepted")
	}
}

func TestJupiterTokenToSOLEffectsRequireExactTokenDebitAndNativeCredit(t *testing.T) {
	effect, decoded, intent, owner, inputMint := jupiterTokenToSOLEffectFixture()
	output, ok := jupiterTokenToSOLEffects(
		effect, decoded, intent, owner, inputMint, 20, 10, effect.FeeLamports,
	)
	if !ok || output != 20 {
		t.Fatalf("reverse effects = %d, %v", output, ok)
	}

	mutations := map[string]func(*solanarpc.TransactionEffect){
		"wrong token debit": func(value *solanarpc.TransactionEffect) {
			value.PostTokenBalances[0].Amount++
		},
		"native output below floor": func(value *solanarpc.TransactionEffect) {
			value.PostBalances[0] -= 11
		},
		"source account lamports changed": func(value *solanarpc.TransactionEffect) {
			value.PostBalances[1]++
		},
		"wrapped output existed": func(value *solanarpc.TransactionEffect) {
			value.PreBalances[2] = 1
		},
		"wrapped output remains": func(value *solanarpc.TransactionEffect) {
			value.PostBalances[2] = 1
		},
		"another owner token changed": func(value *solanarpc.TransactionEffect) {
			value.PostTokenBalances[1].Amount++
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := cloneEffect(effect)
			mutate(&changed)
			if _, ok := jupiterTokenToSOLEffects(
				changed, decoded, intent, owner, inputMint, 20, 10, changed.FeeLamports,
			); ok {
				t.Fatal("accepted mutated reverse effects")
			}
		})
	}
}

func jupiterTokenToSOLEffectFixture() (
	solanarpc.TransactionEffect,
	solana.SignedV0Transaction,
	jupiterswap.MessageIntent,
	string,
	string,
) {
	keys := make([][32]byte, 4)
	for index := range keys {
		keys[index] = [32]byte{byte(index + 1)}
	}
	owner := solana.Encode(keys[0][:])
	inputMint := solana.Encode(bytes.Repeat([]byte{9}, 32))
	inputAccount := solana.Encode(keys[1][:])
	outputAccount := solana.Encode(keys[2][:])
	otherMint := solana.Encode(bytes.Repeat([]byte{8}, 32))
	effect := solanarpc.TransactionEffect{
		FeeLamports:  5,
		PreBalances:  []uint64{1_000, 2_039_280, 0, 2_039_280},
		PostBalances: []uint64{1_015, 2_039_280, 0, 2_039_280},
		PreTokenBalances: []solanarpc.TokenBalance{
			{AccountIndex: 1, Mint: inputMint, Owner: owner, Amount: 100},
			{AccountIndex: 3, Mint: otherMint, Owner: owner, Amount: 7},
		},
		PostTokenBalances: []solanarpc.TokenBalance{
			{AccountIndex: 1, Mint: inputMint, Owner: owner, Amount: 80},
			{AccountIndex: 3, Mint: otherMint, Owner: owner, Amount: 7},
		},
	}
	return effect, solana.SignedV0Transaction{Message: solana.V0Message{AccountKeys: keys}},
		jupiterswap.MessageIntent{Intent: jupiterswap.Intent{
			SourceTokenAccount: inputAccount, DestinationTokenAccount: outputAccount,
		}}, owner, inputMint
}

func jupiterEffectFixture(t *testing.T) (ExpectedJupiter, Submission, solanarpc.TransactionEffect) {
	t.Helper()
	seed := sha256.Sum256([]byte("Jupiter reconciliation signer"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	owner := solana.Encode(privateKey.Public().(ed25519.PublicKey))
	outputMint := solana.Encode(bytes.Repeat([]byte{2}, 32))
	inputAccount := orcaswapATA(t, owner, orcaswap.WrappedSOLMint)
	outputAccount := orcaswapATA(t, owner, outputMint)
	inputAmount, estimatedOutput, minimumOutput := uint64(10), uint64(20), uint64(20)
	transfer := make([]byte, 12)
	binary.LittleEndian.PutUint32(transfer[:4], 2)
	binary.LittleEndian.PutUint64(transfer[4:], inputAmount)
	routeData := []byte{187, 100, 250, 204, 49, 196, 175, 20}
	routeData = binary.LittleEndian.AppendUint64(routeData, inputAmount)
	routeData = binary.LittleEndian.AppendUint64(routeData, estimatedOutput)
	routeData = binary.LittleEndian.AppendUint16(routeData, 50)
	routeData = binary.LittleEndian.AppendUint16(routeData, 0)
	routeData = binary.LittleEndian.AppendUint16(routeData, 0)
	routeData = binary.LittleEndian.AppendUint32(routeData, 1)
	routeData = append(routeData, 17, 1, 0x10, 0x27, 0, 1)
	price := make([]byte, 9)
	price[0] = 3
	binary.LittleEndian.PutUint64(price[1:], 1)
	limit, err := solana.SetComputeUnitLimitInstruction(100_000)
	if err != nil {
		t.Fatal(err)
	}
	instructions := []solana.Instruction{
		limit,
		{Program: solana.ComputeBudgetProgram, Data: price},
		{Program: orcaswap.AssociatedTokenProgram, Accounts: []solana.AccountMeta{
			{Address: owner, Signer: true, Writable: true}, {Address: inputAccount, Writable: true},
			{Address: owner}, {Address: orcaswap.WrappedSOLMint},
			{Address: orcaswap.SystemProgram}, {Address: orcaswap.TokenProgram},
		}, Data: []byte{1}},
		{Program: orcaswap.SystemProgram, Accounts: []solana.AccountMeta{
			{Address: owner, Signer: true, Writable: true}, {Address: inputAccount, Writable: true},
		}, Data: transfer},
		{Program: orcaswap.TokenProgram, Accounts: []solana.AccountMeta{
			{Address: inputAccount, Writable: true},
		}, Data: []byte{17}},
		{Program: jupiterswap.Program, Accounts: []solana.AccountMeta{
			{Address: owner, Signer: true}, {Address: inputAccount, Writable: true},
			{Address: outputAccount, Writable: true}, {Address: orcaswap.WrappedSOLMint},
			{Address: outputMint}, {Address: orcaswap.TokenProgram},
			{Address: orcaswap.TokenProgram}, {Address: outputAccount, Writable: true},
			{Address: "D8cy77BBepLMngZx6ZukaTff5hCt1HrWyKk3Hnd9oitf"},
			{Address: jupiterswap.Program},
			{Address: solana.Encode(bytes.Repeat([]byte{3}, 32)), Writable: true},
		}, Data: routeData},
		{Program: orcaswap.TokenProgram, Accounts: []solana.AccountMeta{
			{Address: inputAccount, Writable: true}, {Address: owner, Writable: true},
			{Address: owner, Signer: true},
		}, Data: []byte{9}},
	}
	recentBlockhash := solana.Encode(bytes.Repeat([]byte{9}, 32))
	policy := jupiterswap.Policy{
		Owner: owner, InputMint: orcaswap.WrappedSOLMint, OutputMint: outputMint,
		MaxInputAmount: inputAmount, MinOutputAmount: minimumOutput, MaxSlippageBPS: 50,
		MaxComputeUnits: 100_000, MaxComputeUnitPriceMicroLamport: 1,
		MaxFeeLamports: 5_000, MaxTokenAccountRentLamports: 3_000_000,
		RouteGuard: txflowRouteGuard(),
	}
	message, err := jupiterswap.BuildGuardedPolicyV0Message(
		policy, owner, recentBlockhash, instructions, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction, signatureBytes, err := solana.SignV0Message(privateKey, message, nil)
	if err != nil {
		t.Fatal(err)
	}
	transactionHash := sha256.Sum256(transaction)
	expected := ExpectedJupiter{
		Signature: solana.Encode(signatureBytes[:]), TransactionSHA256: hex.EncodeToString(transactionHash[:]),
		RecentBlockhash: recentBlockhash, LastValidBlockHeight: 200,
		Policy:      policy,
		InputAmount: inputAmount, EstimatedOutput: estimatedOutput,
		MinimumOutput: minimumOutput, SlippageBPS: 50,
	}
	decoded, err := solana.DecodeSignedV0Transaction(transaction, nil)
	if err != nil {
		t.Fatal(err)
	}
	pre := make([]uint64, len(decoded.Message.AccountKeys))
	post := make([]uint64, len(decoded.Message.AccountKeys))
	for index := range pre {
		pre[index], post[index] = uint64(100_000_000+index), uint64(100_000_000+index)
	}
	inputIndex := messageAccountIndex(decoded.Message.AccountKeys, inputAccount)
	outputIndex := messageAccountIndex(decoded.Message.AccountKeys, outputAccount)
	const rent = uint64(2_039_280)
	pre[0], pre[inputIndex], post[inputIndex] = 1_000_000_000, rent, 0
	post[0] = pre[0] + rent - inputAmount - 5_000
	pre[outputIndex], post[outputIndex] = rent, rent
	effect := solanarpc.TransactionEffect{
		Slot: 150, Transaction: transaction, FeeLamports: 5_000,
		PreBalances: pre, PostBalances: post,
		PreTokenBalances: []solanarpc.TokenBalance{
			{AccountIndex: uint16(inputIndex), Mint: orcaswap.WrappedSOLMint, Owner: owner, Amount: 0},
			{AccountIndex: uint16(outputIndex), Mint: outputMint, Owner: owner, Amount: 100},
		},
		PostTokenBalances: []solanarpc.TokenBalance{
			{AccountIndex: uint16(outputIndex), Mint: outputMint, Owner: owner, Amount: 120},
		},
	}
	return expected, Submission{
		Signature: expected.Signature, LastValidBlockHeight: 200, State: StateAccepted,
	}, effect
}

func txflowRouteGuard() jupiterswap.RouteGuardDeployment {
	code := []byte("txflow route guard")
	hash := sha256.Sum256(code)
	return jupiterswap.RouteGuardDeployment{
		Program:        solana.Encode(bytes.Repeat([]byte{71}, 32)),
		ProgramData:    solana.Encode(bytes.Repeat([]byte{72}, 32)),
		DeploymentSlot: 123, CodeLength: uint64(len(code)), CodeSHA256: hex.EncodeToString(hash[:]),
	}
}

func finalizedStatus(failed bool) solanarpc.SignatureStatus {
	return solanarpc.SignatureStatus{
		Found: true, Slot: 150, ConfirmationStatus: "finalized", Failed: failed,
	}
}

func orcaswapATA(t *testing.T, owner, mint string) string {
	t.Helper()
	address, err := orcaswap.AssociatedTokenAddress(owner, mint)
	if err != nil {
		t.Fatal(err)
	}
	return address
}
