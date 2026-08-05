package txflow

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

func TestReconcileBuyRequiresMatchingFinalizedEffects(t *testing.T) {
	transaction, expected, inputIndex, temporaryIndex := signedBuy(t)
	status := solanarpc.SignatureStatus{
		Found: true, Slot: 150, ConfirmationStatus: "finalized",
	}
	effect := buyEffect(transaction, expected, inputIndex, temporaryIndex, false)
	primary := &fakeProvider{identity: "a", status: status, effect: effect}
	secondary := &fakeProvider{identity: "b", status: status, effect: cloneEffect(effect)}
	lifecycle, err := New(&fakeProvider{identity: "node"}, primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	submission := Submission{
		Signature: expected.Signature, LastValidBlockHeight: 200, State: StateAccepted,
	}
	reconcile := func() Reconciliation {
		t.Helper()
		result, err := lifecycle.ReconcileBuyExpected(t.Context(), submission, expected, 5_000)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result := reconcile()
	if result.Verdict != VerdictFinalized || result.BuyEffects == nil ||
		result.BuyEffects.OutputLamports != expected.MinimumOutput+7 {
		t.Fatalf("finalized buy = %+v", result)
	}

	tests := map[string]func(*solanarpc.TransactionEffect){
		"wrong input debit": func(value *solanarpc.TransactionEffect) {
			value.PostTokenBalances[0].Amount++
		},
		"input account rent changed": func(value *solanarpc.TransactionEffect) {
			value.PostBalances[inputIndex]++
		},
		"temporary native balance before": func(value *solanarpc.TransactionEffect) {
			value.PreBalances[temporaryIndex] = 1
		},
		"temporary native balance after": func(value *solanarpc.TransactionEffect) {
			value.PostBalances[temporaryIndex] = 1
		},
		"temporary token balance remains": func(value *solanarpc.TransactionEffect) {
			value.PostTokenBalances = append(value.PostTokenBalances, solanarpc.TokenBalance{
				AccountIndex: uint16(temporaryIndex), Mint: orcaswap.WrappedSOLMint,
				Owner: expected.Policy.Owner, Amount: 1,
			})
		},
		"output below floor": func(value *solanarpc.TransactionEffect) {
			value.PostBalances[0] = value.PreBalances[0] - 5_000 + expected.MinimumOutput - 1
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := cloneEffect(effect)
			mutate(&changed)
			primary.effect = changed
			secondary.effect = cloneEffect(changed)
			result := reconcile()
			if result.Verdict != VerdictDiverged || result.DivergenceKind != DivergenceEffects {
				t.Fatalf("result = %+v", result)
			}
		})
	}

	primary.effect = cloneEffect(effect)
	secondary.effect = cloneEffect(effect)
	secondary.effect.PostBalances[0]++
	result = reconcile()
	if result.Verdict != VerdictDiverged || result.DivergenceKind != DivergenceEffects {
		t.Fatalf("provider disagreement = %+v", result)
	}

	corrupt := cloneEffect(effect)
	corrupt.Transaction[len(corrupt.Transaction)-1] ^= 1
	digest := sha256.Sum256(corrupt.Transaction)
	expected.TransactionSHA256 = hex.EncodeToString(digest[:])
	primary.effect = corrupt
	secondary.effect = cloneEffect(corrupt)
	result = reconcile()
	if result.Verdict != VerdictDiverged || result.DivergenceKind != DivergenceEffects {
		t.Fatalf("unexpected finalized transaction = %+v", result)
	}
}

func TestReconcileFailedBuyRequiresFeeOnlyEffects(t *testing.T) {
	transaction, expected, inputIndex, temporaryIndex := signedBuy(t)
	status := solanarpc.SignatureStatus{
		Found: true, Slot: 150, ConfirmationStatus: "finalized",
		Failed: true, ErrorFingerprint: "program_error",
	}
	effect := buyEffect(transaction, expected, inputIndex, temporaryIndex, true)
	primary := &fakeProvider{identity: "a", status: status, effect: effect}
	secondary := &fakeProvider{identity: "b", status: status, effect: cloneEffect(effect)}
	lifecycle, err := New(&fakeProvider{identity: "node"}, primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	submission := Submission{
		Signature: expected.Signature, LastValidBlockHeight: 200, State: StateAmbiguous,
	}
	result, err := lifecycle.ReconcileBuyExpected(t.Context(), submission, expected, 5_000)
	if err != nil || result.Verdict != VerdictFailed || result.BuyEffects == nil {
		t.Fatalf("failed buy = %+v, %v", result, err)
	}
	changed := cloneEffect(effect)
	changed.PostTokenBalances[0].Amount++
	primary.effect = changed
	secondary.effect = cloneEffect(changed)
	result, err = lifecycle.ReconcileBuyExpected(t.Context(), submission, expected, 5_000)
	if err != nil || result.Verdict != VerdictDiverged ||
		result.DivergenceKind != DivergenceEffects {
		t.Fatalf("failed buy mutation = %+v, %v", result, err)
	}
}

func signedBuy(t *testing.T) ([]byte, ExpectedBuy, int, int) {
	t.Helper()
	seed := sha256.Sum256([]byte("buy-owner"))
	key := ed25519.NewKeyFromSeed(seed[:])
	owner := solana.Encode(key.Public().(ed25519.PublicKey))
	policy := buyPolicy(owner)
	message, err := solana.BuildLegacyMessage(
		owner,
		solana.Encode(bytes.Repeat([]byte{9}, 32)),
		buyInstructions(t, policy),
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction, signature, err := solana.SignLegacyMessage(key, message)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := orcaswap.DecodeBuyMessageV2(policy, message)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := solana.DecodeLegacyMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	inputIndex := messageAccountIndex(decoded.AccountKeys, policy.InputTokenAccount)
	temporaryIndex := messageAccountIndex(decoded.AccountKeys, intent.TemporaryWSOLAccount)
	if inputIndex <= 0 || temporaryIndex <= 0 {
		t.Fatal("buy accounts were not compiled")
	}
	digest := sha256.Sum256(transaction)
	return transaction, ExpectedBuy{
		Signature:         solana.Encode(signature[:]),
		TransactionSHA256: hex.EncodeToString(digest[:]),
		Policy:            policy,
		InputAmount:       1_000,
		MinimumOutput:     45_348,
	}, inputIndex, temporaryIndex
}

func buyPolicy(owner string) orcaswap.BuyPolicyV2 {
	inputTokenAccount, err := orcaswap.AssociatedTokenAddress(owner, orcaswap.DevnetUSDCMint)
	if err != nil {
		panic(err)
	}
	return orcaswap.BuyPolicyV2{
		Owner: owner, Pool: orcaswap.DevnetPool,
		TokenMintA:               orcaswap.WrappedSOLMint,
		TokenMintB:               orcaswap.DevnetUSDCMint,
		InputTokenAccount:        inputTokenAccount,
		TokenVaultA:              "C9zLV5zWF66j3rZj3uuhDqvfuA8esJyWnruGzDW9qEj2",
		TokenVaultB:              "7DM3RMz2yzUB8yPRQM3FMZgdFrwZGMsabsfsKopWktoX",
		Oracle:                   "2KEWNc3b6EfqoWQpfKQMHh4mhRyKXYRdPbtGRTJX3Cip",
		ProgramData:              orcaswap.WhirlpoolProgramData,
		UpgradeAuthority:         orcaswap.WhirlpoolUpgradeAuth,
		DeploymentSlot:           orcaswap.WhirlpoolDeploySlot,
		MaxInputTokenAmount:      1_000,
		MinOutputLamports:        45_348,
		MaxSlippageBPS:           100,
		MaxTemporaryRentLamports: orcaswap.DefaultMaxTemporaryRentLamports,
	}
}

func buyInstructions(t *testing.T, policy orcaswap.BuyPolicyV2) []solana.Instruction {
	t.Helper()
	ownerKey, err := solana.Decode32(policy.Owner)
	if err != nil {
		t.Fatal(err)
	}
	tokenProgram, err := solana.Decode32(orcaswap.TokenProgram)
	if err != nil {
		t.Fatal(err)
	}
	seed := []byte("1785688960889")
	hash := sha256.New()
	_, _ = hash.Write(ownerKey[:])
	_, _ = hash.Write(seed)
	_, _ = hash.Write(tokenProgram[:])
	temporary := solana.Encode(hash.Sum(nil))
	create := binary.LittleEndian.AppendUint32(nil, 3)
	create = append(create, ownerKey[:]...)
	create = binary.LittleEndian.AppendUint64(create, uint64(len(seed)))
	create = append(create, seed...)
	create = binary.LittleEndian.AppendUint64(create, 2_039_280)
	create = binary.LittleEndian.AppendUint64(create, 165)
	create = append(create, tokenProgram[:]...)
	initialize := append([]byte{18}, ownerKey[:]...)
	swap := make([]byte, 49)
	copy(swap, []byte{43, 4, 237, 11, 26, 201, 30, 98})
	binary.LittleEndian.PutUint64(swap[8:16], 1_000)
	binary.LittleEndian.PutUint64(swap[16:24], 45_348)
	copy(swap[40:], []byte{1, 0, 1, 1, 0, 0, 0, 6, 2})
	return []solana.Instruction{
		{Program: orcaswap.SystemProgram, Accounts: []solana.AccountMeta{
			{Address: policy.Owner, Signer: true, Writable: true},
			{Address: temporary, Writable: true}, {Address: policy.Owner, Signer: true},
		}, Data: create},
		{Program: orcaswap.TokenProgram, Accounts: []solana.AccountMeta{
			{Address: temporary, Writable: true}, {Address: policy.TokenMintA},
		}, Data: initialize},
		{Program: orcaswap.WhirlpoolProgram, Accounts: []solana.AccountMeta{
			{Address: orcaswap.TokenProgram}, {Address: orcaswap.TokenProgram},
			{Address: orcaswap.MemoProgram}, {Address: policy.Owner, Signer: true},
			{Address: policy.Pool, Writable: true}, {Address: policy.TokenMintA},
			{Address: policy.TokenMintB}, {Address: temporary, Writable: true},
			{Address: policy.TokenVaultA, Writable: true},
			{Address: policy.InputTokenAccount, Writable: true},
			{Address: policy.TokenVaultB, Writable: true},
			{Address: "7knZZ461yySGbSEHeBUwEpg3VtAkQy8B9tp78RGgyUHE", Writable: true},
			{Address: "CpoSFo3ajrizueggtJr2ZjvYgdtkgugXtvhqcwkyCkKP", Writable: true},
			{Address: "9iGzy4mQtJadZDuH8seBFQGiqcb6wyp2KW4M6NKHvsAW", Writable: true},
			{Address: policy.Oracle, Writable: true},
			{Address: "3aBJJLAR3QxGcGsesNXeW3f64Rv3TckF7EQ6sXtAuvGM", Writable: true},
			{Address: "A1vrG379E5ttoaWmyQBiunsMdyrpoUp7mSQwu8DgLcip", Writable: true},
		}, Data: swap},
		{Program: orcaswap.TokenProgram, Accounts: []solana.AccountMeta{
			{Address: temporary, Writable: true}, {Address: policy.Owner, Writable: true},
			{Address: policy.Owner, Signer: true},
		}, Data: []byte{9}},
	}
}

func buyEffect(
	transaction []byte,
	expected ExpectedBuy,
	inputIndex,
	temporaryIndex int,
	failed bool,
) solanarpc.TransactionEffect {
	decoded, _ := solana.DecodeSignedLegacyTransaction(transaction)
	pre := make([]uint64, len(decoded.Message.AccountKeys))
	post := make([]uint64, len(decoded.Message.AccountKeys))
	for index := range pre {
		pre[index] = uint64(100_000_000 + index)
		post[index] = pre[index]
	}
	pre[temporaryIndex], post[temporaryIndex] = 0, 0
	post[0] -= 5_000
	preTokens := []solanarpc.TokenBalance{{
		AccountIndex: uint16(inputIndex), Mint: expected.Policy.TokenMintB,
		Owner: expected.Policy.Owner, Amount: 10_000,
	}}
	postTokens := append([]solanarpc.TokenBalance(nil), preTokens...)
	errorFingerprint := ""
	if failed {
		errorFingerprint = "program_error"
	} else {
		post[0] += expected.MinimumOutput + 7
		postTokens[0].Amount -= expected.InputAmount
	}
	return solanarpc.TransactionEffect{
		Slot: 150, Transaction: bytes.Clone(transaction), FeeLamports: 5_000,
		Failed: failed, ErrorFingerprint: errorFingerprint,
		PreBalances: pre, PostBalances: post,
		PreTokenBalances: preTokens, PostTokenBalances: postTokens,
	}
}
