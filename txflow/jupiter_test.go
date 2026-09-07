package txflow

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
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
	payer := result.JupiterEffects.Payer
	if payer == nil || payer.PreLamports != effect.PreBalances[0] ||
		payer.PostLamports != effect.PostBalances[0] || payer.ReclaimedInputLamports != 2_039_280 ||
		!result.JupiterEffects.ValidPayerEffects(true, false) {
		t.Fatalf("retained payer evidence = %+v", payer)
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
	if !result.JupiterEffects.ValidPayerEffects(true, true) {
		t.Fatalf("failed payer evidence = %+v", result.JupiterEffects.Payer)
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
	return jupiterDirectionalEffectFixture(t, true)
}

func jupiterDirectionalEffectFixture(t *testing.T, nativeInput bool, createOutput ...bool) (ExpectedJupiter, Submission, solanarpc.TransactionEffect) {
	t.Helper()
	seed := sha256.Sum256([]byte("Jupiter reconciliation signer"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	owner := solana.Encode(privateKey.Public().(ed25519.PublicKey))
	inputMint, outputMint := orcaswap.WrappedSOLMint, solana.Encode(bytes.Repeat([]byte{2}, 32))
	if !nativeInput {
		inputMint, outputMint = outputMint, inputMint
	}
	inputAccount := orcaswapATA(t, owner, inputMint)
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
			{Address: outputAccount, Writable: true}, {Address: inputMint},
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
	if !nativeInput {
		instructions[2].Accounts[1].Address = outputAccount
		instructions[5].Accounts[7] = solana.AccountMeta{Address: jupiterswap.Program}
		instructions[6].Accounts[0].Address = outputAccount
		instructions = []solana.Instruction{instructions[0], instructions[1], instructions[2], instructions[5], instructions[6]}
	}
	created := nativeInput && len(createOutput) != 0 && createOutput[0]
	if created {
		ata := instructions[2]
		ata.Accounts = append([]solana.AccountMeta(nil), ata.Accounts...)
		ata.Accounts[1].Address, ata.Accounts[3].Address = outputAccount, outputMint
		instructions = append(instructions[:5:5], append([]solana.Instruction{ata}, instructions[5:]...)...)
	}
	recentBlockhash := solana.Encode(bytes.Repeat([]byte{9}, 32))
	policy := jupiterswap.Policy{
		Owner: owner, InputMint: inputMint, OutputMint: outputMint,
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
	request := jupiterquote.Request{Taker: owner, InputMint: inputMint, OutputMint: outputMint,
		InputAmount: inputAmount, SlippageBPS: 50}
	if nativeInput {
		request.DestinationTokenAccount = outputAccount
	}
	if _, _, err := jupiterswap.ValidateSignedV0Transaction(policy, request,
		jupiterquote.Result{InputAmount: inputAmount, EstimatedOutput: estimatedOutput, MinimumOutput: minimumOutput},
		transaction, nil); created {
		if err == nil {
			t.Fatal("protected output account creation was accepted")
		}
	} else if err != nil {
		t.Fatalf("invalid directional transaction fixture: %v", err)
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
	if !nativeInput {
		effect.PreBalances[inputIndex], effect.PostBalances[inputIndex] = rent, rent
		effect.PreBalances[outputIndex], effect.PostBalances[outputIndex] = 0, 0
		effect.PostBalances[0] = effect.PreBalances[0] + estimatedOutput - effect.FeeLamports
		effect.PreTokenBalances = []solanarpc.TokenBalance{{AccountIndex: uint16(inputIndex), Mint: inputMint, Owner: owner, Amount: 100}}
		effect.PostTokenBalances = []solanarpc.TokenBalance{{AccountIndex: uint16(inputIndex), Mint: inputMint, Owner: owner, Amount: 100 - inputAmount}}
	}
	if created {
		effect.PreBalances[outputIndex] = 0
		effect.PostBalances[0] -= rent
		effect.PreTokenBalances = effect.PreTokenBalances[:1]
		effect.PostTokenBalances[0].Amount = estimatedOutput
	}
	return expected, Submission{
		Signature: expected.Signature, LastValidBlockHeight: 200, State: StateAccepted,
	}, effect
}

func TestJupiterPayerEvidenceEquations(t *testing.T) {
	for _, tc := range []struct {
		name                                        string
		nativeInput, failed                         bool
		input, output, fee, rent, refund, pre, post uint64
	}{
		{"new input", true, false, 10, 20, 5, 0, 0, 100, 85},
		{"new output rent", true, false, 10, 20, 5, 30, 0, 100, 55},
		{"refund exceeds debit", true, false, 10, 20, 5, 0, 30, 100, 115},
		{"refund equals debit", true, false, 10, 20, 5, 0, 15, 100, 100},
		{"native output", false, false, 10, 20, 5, 0, 0, 100, 115},
		{"output below fee", false, false, 10, 2, 5, 0, 0, 100, 97},
		{"failed", true, true, 10, 0, 5, 0, 0, 100, 95},
		{"failed spends remaining balance", true, true, 10, 0, 5, 0, 0, 5, 0},
		{"large balance", true, false, 10, 20, 5, 0, 0, ^uint64(0), ^uint64(0) - 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := JupiterEffectEvidence{InputAmount: tc.input, OutputAmount: tc.output,
				FeeLamports: tc.fee, OutputAccountRent: tc.rent,
				Payer: &JupiterPayerEvidence{Version: 1, PreLamports: tc.pre,
					PostLamports: tc.post, ReclaimedInputLamports: tc.refund}}
			if !e.ValidPayerEffects(tc.nativeInput, tc.failed) {
				t.Fatalf("valid payer rejected: %+v", e.Payer)
			}
			for name, mutate := range map[string]func(*JupiterEffectEvidence){
				"pre":     func(v *JupiterEffectEvidence) { v.Payer.PreLamports-- },
				"post":    func(v *JupiterEffectEvidence) { v.Payer.PostLamports++ },
				"refund":  func(v *JupiterEffectEvidence) { v.Payer.ReclaimedInputLamports++ },
				"version": func(v *JupiterEffectEvidence) { v.Payer.Version++ },
				"missing": func(v *JupiterEffectEvidence) { v.Payer = nil },
			} {
				t.Run(name, func(t *testing.T) {
					changed, payer := e, *e.Payer
					changed.Payer = &payer
					mutate(&changed)
					if changed.ValidPayerEffects(tc.nativeInput, tc.failed) {
						t.Fatal("changed payer accepted")
					}
				})
			}
		})
	}
	e := JupiterEffectEvidence{InputAmount: ^uint64(0), FeeLamports: 1,
		Payer: &JupiterPayerEvidence{Version: 1}}
	if e.ValidPayerEffects(true, false) {
		t.Fatal("overflowing debit accepted")
	}
	e.InputAmount = 0
	if e.ValidPayerEffects(true, true) {
		t.Fatal("zero balances accepted with nonzero fee")
	}
	payer := JupiterPayerEvidence{Version: 1, PreLamports: ^uint64(0)}
	data, err := json.Marshal(payer)
	if err != nil || !bytes.Contains(data, []byte(`"pre_lamports":"18446744073709551615"`)) {
		t.Fatalf("lossless payer encoding = %s, %v", data, err)
	}
}

func TestReconcileJupiterRetainsNativeOutputPayer(t *testing.T) {
	expected, submission, effect := jupiterDirectionalEffectFixture(t, false)
	primary := &fakeProvider{identity: "primary", status: finalizedStatus(false), effect: effect}
	secondary := &fakeProvider{identity: "secondary", status: finalizedStatus(false), effect: cloneEffect(effect)}
	lifecycle, err := NewEvidenceLifecycle(primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	result, err := lifecycle.ReconcileJupiterExpected(t.Context(), submission, expected, effect.FeeLamports)
	if err != nil || result.Verdict != VerdictFinalized || result.JupiterEffects == nil ||
		!result.JupiterEffects.ValidPayerEffects(false, false) {
		t.Fatalf("native output payer reconciliation = %+v, %v", result, err)
	}
	payer := result.JupiterEffects.Payer
	if payer.PreLamports != effect.PreBalances[0] || payer.PostLamports != effect.PostBalances[0] || payer.ReclaimedInputLamports != 0 {
		t.Fatalf("native output payer = %+v", payer)
	}
}

func TestReconcileJupiterRetainsExactTokenBalances(t *testing.T) {
	for _, nativeInput := range []bool{true, false} {
		for _, mode := range []string{"success", "created", "failed", "failed zero", "failed missing metadata", "failed missing account"} {
			if mode == "created" && !nativeInput {
				continue
			}
			t.Run(fmt.Sprintf("native_input=%t/%s", nativeInput, mode), func(t *testing.T) {
				expected, submission, effect := jupiterDirectionalEffectFixture(t, nativeInput, mode == "created")
				failed := mode != "success" && mode != "created"
				status := finalizedStatus(failed)
				if mode == "failed zero" {
					for i := range effect.PreTokenBalances {
						if effect.PreTokenBalances[i].Mint != orcaswap.WrappedSOLMint {
							effect.PreTokenBalances[i].Amount = 0
						}
					}
				}
				if failed {
					effect.Failed, effect.ErrorFingerprint = true, "program_error"
					status.ErrorFingerprint = effect.ErrorFingerprint
					effect.PostBalances = append([]uint64(nil), effect.PreBalances...)
					effect.PostBalances[0] -= effect.FeeLamports
					effect.PostTokenBalances = append([]solanarpc.TokenBalance(nil), effect.PreTokenBalances...)
				}
				if mode == "failed missing metadata" || mode == "failed missing account" {
					if mode == "failed missing account" {
						for _, balance := range effect.PreTokenBalances {
							if balance.Mint != orcaswap.WrappedSOLMint {
								effect.PreBalances[balance.AccountIndex], effect.PostBalances[balance.AccountIndex] = 0, 0
							}
						}
					}
					effect.PreTokenBalances, effect.PostTokenBalances = nil, nil
				}
				a := &fakeProvider{identity: "primary", status: status, effect: effect}
				b := &fakeProvider{identity: "secondary", status: status, effect: cloneEffect(effect)}
				lifecycle, err := NewEvidenceLifecycle(a, b)
				if err != nil {
					t.Fatal(err)
				}
				got, err := lifecycle.ReconcileJupiterExpected(t.Context(), submission, expected, effect.FeeLamports)
				if mode == "created" {
					if err != nil || got.Verdict != VerdictDiverged || got.JupiterEffects != nil {
						t.Fatalf("protected account creation accepted: %+v, %v", got, err)
					}
					return
				}
				verdict := VerdictFinalized
				if failed {
					verdict = VerdictFailed
				}
				if err != nil || got.Verdict != verdict || got.JupiterEffects == nil {
					t.Fatalf("reconciliation = %+v, %v", got, err)
				}
				if mode == "failed missing metadata" || mode == "failed missing account" {
					if got.JupiterEffects.Token != nil {
						t.Fatal("missing token evidence became a zero holding")
					}
					return
				}
				token := got.JupiterEffects.Token
				wantPre := uint64(100)
				wantPost := uint64(100)
				if !failed {
					if nativeInput {
						wantPost = 120
					} else {
						wantPost = 100 - expected.InputAmount
					}
				}
				if mode == "created" {
					wantPre, wantPost = 0, expected.EstimatedOutput
				}
				if mode == "failed zero" {
					wantPre, wantPost = 0, 0
				}
				if token == nil || token.PreUnits != wantPre || token.PostUnits != wantPost || !got.JupiterEffects.ValidTokenEffects(expected.Policy, failed) {
					t.Fatalf("token evidence = %+v", token)
				}
				for name, mutate := range map[string]func(*JupiterTokenEvidence){
					"version": func(v *JupiterTokenEvidence) { v.Version++ },
					"account": func(v *JupiterTokenEvidence) { v.Account = expected.Policy.Owner },
					"owner":   func(v *JupiterTokenEvidence) { v.Owner = orcaswap.SystemProgram },
					"mint":    func(v *JupiterTokenEvidence) { v.Mint = orcaswap.WrappedSOLMint },
					"pre":     func(v *JupiterTokenEvidence) { v.PreUnits++ },
					"post":    func(v *JupiterTokenEvidence) { v.PostUnits++ },
				} {
					t.Run(name, func(t *testing.T) {
						changed, value := *got.JupiterEffects, *token
						mutate(&value)
						changed.Token = &value
						if changed.ValidTokenEffects(expected.Policy, failed) {
							t.Fatal("tampered token evidence accepted")
						}
					})
				}
			})
		}
	}
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
