package txflow

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

func TestReconcileSwapRequiresMatchingFinalizedEffects(t *testing.T) {
	transaction, expected, outputIndex := signedSwap(t)
	status := solanarpc.SignatureStatus{
		Found: true, Slot: 150, ConfirmationStatus: "finalized",
	}
	effect := swapEffect(transaction, expected, outputIndex, false)
	primary := &fakeProvider{identity: "a", status: status, effect: effect}
	secondary := &fakeProvider{identity: "b", status: status, effect: cloneEffect(effect)}
	lifecycle, err := New(&fakeProvider{identity: "node"}, primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	submission := Submission{
		Signature: expected.Signature, LastValidBlockHeight: 200, State: StateAccepted,
	}
	result, err := lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictFinalized || result.SwapEffects == nil ||
		result.SwapEffects.OutputAmount != expected.MinimumOutput+7 {
		t.Fatalf("finalized swap = %+v", result)
	}
	decoded, err := solana.DecodeSignedLegacyTransaction(transaction)
	if err != nil {
		t.Fatal(err)
	}
	inputIndex := messageAccountIndex(decoded.Message.AccountKeys, expected.Policy.InputTokenAccount)
	if inputIndex < 0 {
		t.Fatal("swap input account was not compiled")
	}
	withoutExistingInput := cloneEffect(effect)
	withoutExistingInput.PostBalances[0] -= withoutExistingInput.PreBalances[inputIndex]
	withoutExistingInput.PreBalances[inputIndex] = 0
	primary.effect = withoutExistingInput
	secondary.effect = cloneEffect(withoutExistingInput)
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil || result.Verdict != VerdictFinalized {
		t.Fatalf("new input account swap = %+v, %v", result, err)
	}

	invalidConservation := cloneEffect(effect)
	invalidConservation.PostBalances[0]++
	primary.effect = invalidConservation
	secondary.effect = cloneEffect(invalidConservation)
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil || result.Verdict != VerdictDiverged ||
		result.DivergenceKind != DivergenceEffects {
		t.Fatalf("invalid native conservation = %+v, %v", result, err)
	}

	overflow := cloneEffect(effect)
	overflow.PreBalances[0] = math.MaxUint64
	primary.effect = overflow
	secondary.effect = cloneEffect(overflow)
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil || result.Verdict != VerdictDiverged ||
		result.DivergenceKind != DivergenceEffects {
		t.Fatalf("overflowing native conservation = %+v, %v", result, err)
	}

	carryOnlyOverflow := cloneEffect(effect)
	carryOnlyOverflow.PreBalances[0] = math.MaxUint64
	carryOnlyOverflow.PreBalances[inputIndex] = 1
	carryOnlyOverflow.PostBalances[0] = math.MaxUint64 - (5_000 + expected.InputAmount - 1)
	carryOnlyOverflow.PostBalances[inputIndex] = 0
	primary.effect = carryOnlyOverflow
	secondary.effect = cloneEffect(carryOnlyOverflow)
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil || result.Verdict != VerdictDiverged ||
		result.DivergenceKind != DivergenceEffects {
		t.Fatalf("equal wrapped native conservation = %+v, %v", result, err)
	}

	missingOutputIdentity := cloneEffect(effect)
	missingOutputIdentity.PreTokenBalances = nil
	primary.effect = missingOutputIdentity
	secondary.effect = cloneEffect(missingOutputIdentity)
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil || result.Verdict != VerdictDiverged ||
		result.DivergenceKind != DivergenceEffects {
		t.Fatalf("missing output identity = %+v, %v", result, err)
	}

	invalidIndex := cloneEffect(effect)
	outOfRange := solanarpc.TokenBalance{
		AccountIndex: uint16(len(decoded.Message.AccountKeys)),
		Mint:         expected.Policy.OutputMint, Owner: expected.Policy.Owner,
	}
	invalidIndex.PreTokenBalances = append(invalidIndex.PreTokenBalances, outOfRange)
	invalidIndex.PostTokenBalances = append(invalidIndex.PostTokenBalances, outOfRange)
	primary.effect = invalidIndex
	secondary.effect = cloneEffect(invalidIndex)
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil || result.Verdict != VerdictDiverged ||
		result.DivergenceKind != DivergenceEffects {
		t.Fatalf("out-of-range token balance = %+v, %v", result, err)
	}

	primary.effect = cloneEffect(effect)
	secondary.effect = cloneEffect(effect)
	secondary.effect.PostBalances[0]++
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictDiverged || result.DivergenceKind != DivergenceEffects {
		t.Fatalf("native balance disagreement = %+v", result)
	}

	secondary.effect = cloneEffect(effect)
	secondary.effect.PostTokenBalances[0].Amount++
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictDiverged || result.DivergenceKind != DivergenceEffects {
		t.Fatalf("token balance disagreement = %+v", result)
	}
}

func TestReconcileSwapStatusTransitions(t *testing.T) {
	_, baseExpected, _ := signedSwap(t)
	baseSubmission := Submission{
		Signature: baseExpected.Signature, LastValidBlockHeight: 200, State: StateAccepted,
	}
	confirmed := solanarpc.SignatureStatus{
		Found: true, Slot: 150, ConfirmationStatus: "confirmed",
	}
	tests := map[string]struct {
		mutate         func(*fakeProvider, *fakeProvider, *Submission, *ExpectedSwap)
		wantVerdict    string
		wantDivergence string
		wantErr        bool
	}{
		"one provider pending": {
			mutate: func(_, secondary *fakeProvider, _ *Submission, _ *ExpectedSwap) {
				secondary.status = solanarpc.SignatureStatus{}
			},
			wantVerdict: VerdictPending,
		},
		"missing after expiry": {
			mutate: func(primary, secondary *fakeProvider, _ *Submission, _ *ExpectedSwap) {
				secondary.status = solanarpc.SignatureStatus{}
				primary.height, secondary.height = 201, 201
			},
			wantVerdict: VerdictUnresolved,
		},
		"status slot disagreement": {
			mutate: func(_, secondary *fakeProvider, _ *Submission, _ *ExpectedSwap) {
				secondary.status.Slot++
			},
			wantVerdict: VerdictDiverged, wantDivergence: DivergenceStatus,
		},
		"status error disagreement": {
			mutate: func(primary, secondary *fakeProvider, _ *Submission, _ *ExpectedSwap) {
				primary.status.Failed, secondary.status.Failed = true, true
				primary.status.ErrorFingerprint = "first"
				secondary.status.ErrorFingerprint = "second"
			},
			wantVerdict: VerdictDiverged, wantDivergence: DivergenceStatus,
		},
		"nonfinal agreement": {wantVerdict: VerdictPending},
		"invalid submission state": {
			mutate: func(_, _ *fakeProvider, submission *Submission, _ *ExpectedSwap) {
				submission.State = "invalid"
			},
			wantErr: true,
		},
		"signature mismatch": {
			mutate: func(_, _ *fakeProvider, submission *Submission, _ *ExpectedSwap) {
				submission.Signature = solana.Encode(make([]byte, ed25519.SignatureSize))
			},
			wantErr: true,
		},
		"transaction hash malformed": {
			mutate: func(_, _ *fakeProvider, _ *Submission, expected *ExpectedSwap) {
				expected.TransactionSHA256 = "invalid"
			},
			wantErr: true,
		},
		"status query failure": {
			mutate: func(primary, _ *fakeProvider, _ *Submission, _ *ExpectedSwap) {
				primary.statusErr = errors.New("status unavailable")
			},
			wantErr: true,
		},
		"height query failure": {
			mutate: func(_, secondary *fakeProvider, _ *Submission, _ *ExpectedSwap) {
				secondary.status = solanarpc.SignatureStatus{}
				secondary.heightErr = errors.New("height unavailable")
			},
			wantErr: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			primary := &fakeProvider{identity: "a", status: confirmed, height: 100}
			secondary := &fakeProvider{identity: "b", status: confirmed, height: 100}
			submission, expected := baseSubmission, baseExpected
			if test.mutate != nil {
				test.mutate(primary, secondary, &submission, &expected)
			}
			lifecycle, err := New(&fakeProvider{identity: "node"}, primary, secondary)
			if err != nil {
				t.Fatal(err)
			}
			result, err := lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
			if test.wantErr {
				if err == nil {
					t.Fatalf("invalid status transition accepted: %+v", result)
				}
				return
			}
			if err != nil || result.Verdict != test.wantVerdict ||
				result.DivergenceKind != test.wantDivergence {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}

func TestReconcileFailedSwapRejectsNonFeeBalanceChanges(t *testing.T) {
	transaction, expected, outputIndex := signedSwap(t)
	status := solanarpc.SignatureStatus{
		Found: true, Slot: 150, ConfirmationStatus: "finalized",
		Failed: true, ErrorFingerprint: "program_error",
	}
	effect := swapEffect(transaction, expected, outputIndex, true)
	primary := &fakeProvider{identity: "a", status: status, effect: effect}
	secondary := &fakeProvider{identity: "b", status: status, effect: cloneEffect(effect)}
	lifecycle, err := New(&fakeProvider{identity: "node"}, primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	submission := Submission{
		Signature: expected.Signature, LastValidBlockHeight: 200, State: StateAmbiguous,
	}
	result, err := lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictFailed || result.SwapEffects == nil {
		t.Fatalf("failed swap = %+v", result)
	}

	tokenMutation := cloneEffect(effect)
	tokenMutation.PostTokenBalances[0].Amount++
	primary.effect = tokenMutation
	secondary.effect = cloneEffect(tokenMutation)
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil || result.Verdict != VerdictDiverged ||
		result.DivergenceKind != DivergenceEffects {
		t.Fatalf("failed swap token mutation = %+v, %v", result, err)
	}

	feeMutation := cloneEffect(effect)
	feeMutation.PostBalances[0]++
	primary.effect = feeMutation
	secondary.effect = cloneEffect(feeMutation)
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil || result.Verdict != VerdictDiverged ||
		result.DivergenceKind != DivergenceEffects {
		t.Fatalf("failed swap fee mutation = %+v, %v", result, err)
	}

	primary.effect = cloneEffect(effect)
	secondary.effect = cloneEffect(effect)
	primary.effect.PostBalances[1]++
	secondary.effect.PostBalances[1]++
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictDiverged || result.DivergenceKind != DivergenceEffects {
		t.Fatalf("failed swap balance mutation = %+v", result)
	}
}

func TestReconcileSwapBoundsFreshOutputAccountRent(t *testing.T) {
	transaction, expected, outputIndex := signedSwapMode(t, true)
	status := solanarpc.SignatureStatus{
		Found: true, Slot: 150, ConfirmationStatus: "finalized",
	}
	effect := swapEffect(transaction, expected, outputIndex, false)
	primary := &fakeProvider{identity: "a", status: status, effect: effect}
	secondary := &fakeProvider{identity: "b", status: status, effect: cloneEffect(effect)}
	lifecycle, err := New(&fakeProvider{identity: "node"}, primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	submission := Submission{
		Signature: expected.Signature, LastValidBlockHeight: 200, State: StateAccepted,
	}
	result, err := lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil || result.Verdict != VerdictFinalized {
		t.Fatalf("idempotent existing output account = %+v, %v", result, err)
	}

	missingIdentity := cloneEffect(effect)
	missingIdentity.PreTokenBalances = nil
	primary.effect = missingIdentity
	secondary.effect = cloneEffect(missingIdentity)
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil || result.Verdict != VerdictDiverged {
		t.Fatalf("missing existing output identity = %+v, %v", result, err)
	}

	changedIdentity := cloneEffect(effect)
	changedIdentity.PreTokenBalances[0].Mint = expected.Policy.InputMint
	primary.effect = changedIdentity
	secondary.effect = cloneEffect(changedIdentity)
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil || result.Verdict != VerdictDiverged {
		t.Fatalf("changed existing output identity = %+v, %v", result, err)
	}

	changedLamports := cloneEffect(effect)
	changedLamports.PostBalances[outputIndex]++
	changedLamports.PostBalances[0]--
	primary.effect = changedLamports
	secondary.effect = cloneEffect(changedLamports)
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil || result.Verdict != VerdictDiverged {
		t.Fatalf("changed existing output lamports = %+v, %v", result, err)
	}

	drainedLamports := cloneEffect(effect)
	drainedLamports.PostBalances[outputIndex]--
	drainedLamports.PostBalances[0]++
	primary.effect = drainedLamports
	secondary.effect = cloneEffect(drainedLamports)
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil || result.Verdict != VerdictDiverged {
		t.Fatalf("drained existing output lamports = %+v, %v", result, err)
	}

	const outputRent = uint64(2_039_280)
	freshOutput := cloneEffect(effect)
	freshOutput.PreBalances[outputIndex] = 0
	freshOutput.PostBalances[outputIndex] = outputRent
	freshOutput.PostBalances[0] -= outputRent
	freshOutput.PreTokenBalances = nil
	primary.effect = freshOutput
	secondary.effect = cloneEffect(freshOutput)
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil || result.Verdict != VerdictFinalized {
		t.Fatalf("fresh output account = %+v, %v", result, err)
	}
	decoded, err := solana.DecodeSignedLegacyTransaction(transaction)
	if err != nil {
		t.Fatal(err)
	}
	inputIndex := messageAccountIndex(decoded.Message.AccountKeys, expected.Policy.InputTokenAccount)
	if inputIndex < 0 {
		t.Fatal("swap input account was not compiled")
	}
	freshAccounts := cloneEffect(freshOutput)
	freshAccounts.PostBalances[0] -= freshAccounts.PreBalances[inputIndex]
	freshAccounts.PreBalances[inputIndex] = 0
	primary.effect = freshAccounts
	secondary.effect = cloneEffect(freshAccounts)
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil || result.Verdict != VerdictFinalized {
		t.Fatalf("fresh input and output accounts = %+v, %v", result, err)
	}

	excess := expected.Policy.MaxOutputAccountRentLamports - outputRent + 1
	primary.effect.PostBalances[0] -= excess
	primary.effect.PostBalances[outputIndex] += excess
	secondary.effect.PostBalances[0] -= excess
	secondary.effect.PostBalances[outputIndex] += excess
	result, err = lifecycle.ReconcileSwapExpected(t.Context(), submission, expected, 5_000)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictDiverged || result.DivergenceKind != DivergenceEffects {
		t.Fatalf("excess output-account rent = %+v", result)
	}
}

func TestTokenBalancesEqualRejectsDuplicateIndexes(t *testing.T) {
	first := solanarpc.TokenBalance{AccountIndex: 1, Mint: "mint-a", Owner: "owner", Amount: 10}
	second := solanarpc.TokenBalance{AccountIndex: 2, Mint: "mint-b", Owner: "owner", Amount: 20}
	if !tokenBalancesEqual(
		[]solanarpc.TokenBalance{first, second},
		[]solanarpc.TokenBalance{second, first},
	) {
		t.Fatal("equal token balances in different order were rejected")
	}
	if tokenBalancesEqual(
		[]solanarpc.TokenBalance{first, second},
		[]solanarpc.TokenBalance{first, first},
	) {
		t.Fatal("duplicate right-side token balance was accepted")
	}
	if tokenBalancesEqual(
		[]solanarpc.TokenBalance{first, first},
		[]solanarpc.TokenBalance{first, second},
	) {
		t.Fatal("duplicate left-side token balance was accepted")
	}
}

func signedSwap(t *testing.T) ([]byte, ExpectedSwap, uint16) {
	return signedSwapMode(t, false)
}

func signedSwapMode(t *testing.T, createOutput bool) ([]byte, ExpectedSwap, uint16) {
	t.Helper()
	seed := sha256.Sum256([]byte("swap-owner"))
	key := ed25519.NewKeyFromSeed(seed[:])
	owner := solana.Encode(key.Public().(ed25519.PublicKey))
	policy := swapPolicy(owner)
	instructions := swapInstructions(policy)
	if createOutput {
		instructions = append(
			append([]solana.Instruction{}, instructions[0], outputATAInstruction(policy)),
			instructions[1:]...,
		)
	}
	message, err := solana.BuildLegacyMessage(
		owner, solana.Encode(bytes.Repeat([]byte{9}, 32)), instructions,
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction, signature, err := solana.SignLegacyMessage(key, message)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := solana.DecodeLegacyMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	outputIndex := -1
	for index, account := range decoded.AccountKeys {
		if solana.Encode(account[:]) == policy.OutputTokenAccount {
			outputIndex = index
		}
	}
	if outputIndex < 0 {
		t.Fatal("swap output account was not compiled")
	}
	digest := sha256.Sum256(transaction)
	return transaction, ExpectedSwap{
		Signature: solana.Encode(signature[:]), TransactionSHA256: hex.EncodeToString(digest[:]),
		Policy: policy, InputAmount: 1_000_000, MinimumOutput: 21_525,
	}, uint16(outputIndex)
}

func outputATAInstruction(policy orcaswap.Policy) solana.Instruction {
	return solana.Instruction{
		Program: orcaswap.AssociatedTokenProgram,
		Accounts: []solana.AccountMeta{
			{Address: policy.Owner, Signer: true, Writable: true},
			{Address: policy.OutputTokenAccount, Writable: true},
			{Address: policy.Owner}, {Address: policy.OutputMint},
			{Address: orcaswap.SystemProgram}, {Address: orcaswap.TokenProgram},
		},
		Data: []byte{1},
	}
}

func swapPolicy(owner string) orcaswap.Policy {
	inputTokenAccount, err := orcaswap.AssociatedTokenAddress(owner, orcaswap.WrappedSOLMint)
	if err != nil {
		panic(err)
	}
	outputTokenAccount, err := orcaswap.AssociatedTokenAddress(owner, orcaswap.DevnetUSDCMint)
	if err != nil {
		panic(err)
	}
	return orcaswap.Policy{
		Owner: owner, Pool: "3KBZiL2g8C7tiJ32hTv5v3KM7aK9htpqTw4cTXz1HvPt",
		InputMint:          orcaswap.WrappedSOLMint,
		OutputMint:         "BRjpCHtyQLNCo8gqRUr8jtdAj5AjPYQaoqbvcZiHok1k",
		InputTokenAccount:  inputTokenAccount,
		OutputTokenAccount: outputTokenAccount,
		TokenVaultA:        "C9zLV5zWF66j3rZj3uuhDqvfuA8esJyWnruGzDW9qEj2",
		TokenVaultB:        "7DM3RMz2yzUB8yPRQM3FMZgdFrwZGMsabsfsKopWktoX",
		Oracle:             "2KEWNc3b6EfqoWQpfKQMHh4mhRyKXYRdPbtGRTJX3Cip",
		ProgramData:        orcaswap.WhirlpoolProgramData,
		UpgradeAuthority:   orcaswap.WhirlpoolUpgradeAuth,
		DeploymentSlot:     orcaswap.WhirlpoolDeploySlot,
		MaxInputLamports:   1_000_000, MinOutputAmount: 1, MaxSlippageBPS: 100,
		MaxOutputAccountRentLamports: orcaswap.DefaultMaxOutputAccountRentLamports,
	}
}

func swapInstructions(policy orcaswap.Policy) []solana.Instruction {
	ata := func(account, mint string) solana.Instruction {
		return solana.Instruction{
			Program: orcaswap.AssociatedTokenProgram,
			Accounts: []solana.AccountMeta{
				{Address: policy.Owner, Signer: true, Writable: true},
				{Address: account, Writable: true}, {Address: policy.Owner},
				{Address: mint}, {Address: orcaswap.SystemProgram},
				{Address: orcaswap.TokenProgram},
			}, Data: []byte{1},
		}
	}
	transfer := make([]byte, 12)
	binary.LittleEndian.PutUint32(transfer[:4], 2)
	binary.LittleEndian.PutUint64(transfer[4:], 1_000_000)
	swap := make([]byte, 49)
	copy(swap, []byte{43, 4, 237, 11, 26, 201, 30, 98})
	binary.LittleEndian.PutUint64(swap[8:16], 1_000_000)
	binary.LittleEndian.PutUint64(swap[16:24], 21_525)
	copy(swap[40:], []byte{1, 1, 1, 1, 0, 0, 0, 6, 2})
	return []solana.Instruction{
		ata(policy.InputTokenAccount, policy.InputMint),
		{Program: orcaswap.SystemProgram, Accounts: []solana.AccountMeta{
			{Address: policy.Owner, Signer: true, Writable: true},
			{Address: policy.InputTokenAccount, Writable: true},
		}, Data: transfer},
		{Program: orcaswap.TokenProgram, Accounts: []solana.AccountMeta{
			{Address: policy.InputTokenAccount, Writable: true},
		}, Data: []byte{17}},
		{Program: orcaswap.WhirlpoolProgram, Accounts: []solana.AccountMeta{
			{Address: orcaswap.TokenProgram}, {Address: orcaswap.TokenProgram},
			{Address: orcaswap.MemoProgram}, {Address: policy.Owner, Signer: true},
			{Address: policy.Pool, Writable: true}, {Address: policy.InputMint},
			{Address: policy.OutputMint}, {Address: policy.InputTokenAccount, Writable: true},
			{Address: policy.TokenVaultA, Writable: true},
			{Address: policy.OutputTokenAccount, Writable: true},
			{Address: policy.TokenVaultB, Writable: true},
			{Address: "7knZZ461yySGbSEHeBUwEpg3VtAkQy8B9tp78RGgyUHE", Writable: true},
			{Address: "CpoSFo3ajrizueggtJr2ZjvYgdtkgugXtvhqcwkyCkKP", Writable: true},
			{Address: "9iGzy4mQtJadZDuH8seBFQGiqcb6wyp2KW4M6NKHvsAW", Writable: true},
			{Address: policy.Oracle, Writable: true},
			{Address: "3aBJJLAR3QxGcGsesNXeW3f64Rv3TckF7EQ6sXtAuvGM", Writable: true},
			{Address: "A1vrG379E5ttoaWmyQBiunsMdyrpoUp7mSQwu8DgLcip", Writable: true},
		}, Data: swap},
		{Program: orcaswap.TokenProgram, Accounts: []solana.AccountMeta{
			{Address: policy.InputTokenAccount, Writable: true},
			{Address: policy.Owner, Writable: true}, {Address: policy.Owner, Signer: true},
		}, Data: []byte{9}},
	}
}

func swapEffect(
	transaction []byte,
	expected ExpectedSwap,
	outputIndex uint16,
	failed bool,
) solanarpc.TransactionEffect {
	decoded, _ := solana.DecodeSignedLegacyTransaction(transaction)
	pre := make([]uint64, len(decoded.Message.AccountKeys))
	post := make([]uint64, len(decoded.Message.AccountKeys))
	for index := range pre {
		pre[index] = uint64(100_000_000 + index)
		post[index] = pre[index]
	}
	post[0] -= 5_000
	if !failed {
		post[0] -= expected.InputAmount
		inputIndex := messageAccountIndex(decoded.Message.AccountKeys, expected.Policy.InputTokenAccount)
		if inputIndex < 0 {
			panic("swap input account was not compiled")
		}
		pre[inputIndex] = 2_039_280
		post[inputIndex] = 0
		post[0] += pre[inputIndex]
	}
	preTokens := []solanarpc.TokenBalance{{
		AccountIndex: outputIndex, Mint: expected.Policy.OutputMint,
		Owner: expected.Policy.Owner, Amount: 10,
	}}
	postTokens := append([]solanarpc.TokenBalance(nil), preTokens...)
	errorFingerprint := ""
	if failed {
		errorFingerprint = "program_error"
	} else {
		postTokens[0].Amount += expected.MinimumOutput + 7
	}
	return solanarpc.TransactionEffect{
		Slot: 150, Transaction: bytes.Clone(transaction), FeeLamports: 5_000,
		Failed: failed, ErrorFingerprint: errorFingerprint,
		PreBalances: pre, PostBalances: post,
		PreTokenBalances: preTokens, PostTokenBalances: postTokens,
	}
}

func cloneEffect(effect solanarpc.TransactionEffect) solanarpc.TransactionEffect {
	effect.Transaction = bytes.Clone(effect.Transaction)
	effect.PreBalances = append([]uint64(nil), effect.PreBalances...)
	effect.PostBalances = append([]uint64(nil), effect.PostBalances...)
	effect.PreTokenBalances = append([]solanarpc.TokenBalance(nil), effect.PreTokenBalances...)
	effect.PostTokenBalances = append([]solanarpc.TokenBalance(nil), effect.PostTokenBalances...)
	return effect
}
