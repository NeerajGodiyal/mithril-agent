package submitter

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func TestSubmitAcceptsOnlyExactSealedBuy(t *testing.T) {
	policy, privateKey, response, transaction := buySubmitterFixture(t)
	node := &submitterTestNode{returned: response.Signature}
	submission, err := Submit(
		t.Context(), policy, privateKey, node,
		submitterTestGate{allowed: true}, response, 90,
	)
	if err != nil {
		t.Fatal(err)
	}
	if submission.State != txflow.StateAccepted || node.minSlot != 90 ||
		!bytes.Equal(node.transaction, transaction) {
		t.Fatalf("buy submission = %+v", submission)
	}

	changed := policy
	changed.MaxInputTokenAmount--
	node = &submitterTestNode{returned: response.Signature}
	if _, err := Submit(
		t.Context(), changed, privateKey, node,
		submitterTestGate{allowed: true}, response, 90,
	); err == nil || node.transaction != nil {
		t.Fatal("buy outside the submitter input cap reached the node")
	}
}

func buySubmitterFixture(t *testing.T) (Policy, string, signer.Response, []byte) {
	t.Helper()
	seed := sha256.Sum256([]byte("buy submitter owner"))
	ownerKey := ed25519.NewKeyFromSeed(seed[:])
	owner := solana.Encode(ownerKey.Public().(ed25519.PublicKey))
	inputAccount, err := orcaswap.AssociatedTokenAddress(owner, orcaswap.DevnetUSDCMint)
	if err != nil {
		t.Fatal(err)
	}
	route := orcaswap.BuyPolicyV2{
		Owner: owner, Pool: orcaswap.DevnetPool,
		TokenMintA: orcaswap.WrappedSOLMint, TokenMintB: orcaswap.DevnetUSDCMint,
		InputTokenAccount:   inputAccount,
		TokenVaultA:         "C9zLV5zWF66j3rZj3uuhDqvfuA8esJyWnruGzDW9qEj2",
		TokenVaultB:         "7DM3RMz2yzUB8yPRQM3FMZgdFrwZGMsabsfsKopWktoX",
		Oracle:              "2KEWNc3b6EfqoWQpfKQMHh4mhRyKXYRdPbtGRTJX3Cip",
		ProgramData:         orcaswap.WhirlpoolProgramData,
		UpgradeAuthority:    orcaswap.WhirlpoolUpgradeAuth,
		DeploymentSlot:      orcaswap.WhirlpoolDeploySlot,
		MaxInputTokenAmount: 1_000, MinOutputLamports: 45_348,
		MaxSlippageBPS: 100, MaxTemporaryRentLamports: 3_000_000,
	}
	message, err := solana.BuildLegacyMessage(
		owner, solana.Encode(bytes.Repeat([]byte{7}, 32)),
		buySubmitterInstructions(t, route),
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction, signature, err := solana.SignLegacyMessage(ownerKey, message)
	if err != nil {
		t.Fatal(err)
	}
	submitterSeed := sha256.Sum256([]byte("buy submitter"))
	privateKey := hex.EncodeToString(submitterSeed[:])
	publicKey, err := sealedtx.PublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	messageHash := sha256.Sum256(message)
	transactionHash := sha256.Sum256(transaction)
	actionHash := sha256.Sum256([]byte("buy action"))
	response := signer.Response{
		ActionID: hex.EncodeToString(actionHash[:]), Signature: solana.Encode(signature[:]),
		MessageSHA256:        hex.EncodeToString(messageHash[:]),
		TransactionSHA256:    hex.EncodeToString(transactionHash[:]),
		BlockhashContextSlot: 90,
		FeeLamports:          5_000, LastValidBlockHeight: 200,
	}
	response.SealedTransaction, err = sealedtx.Seal(publicKey, sealedtx.Metadata{
		Version: sealedtx.Version, Domain: sealedtx.Domain, ActionID: response.ActionID,
		MessageSHA256: response.MessageSHA256, TransactionSHA256: response.TransactionSHA256,
		Signature: response.Signature, BlockhashContextSlot: response.BlockhashContextSlot,
		FeeLamports:          response.FeeLamports,
		LastValidBlockHeight: response.LastValidBlockHeight,
	}, transaction, nil)
	if err != nil {
		t.Fatal(err)
	}
	response.SignerAttestation, err = signer.AttestResponse(ownerKey, publicKey, response)
	if err != nil {
		t.Fatal(err)
	}
	return Policy{
		Cluster: "devnet", Profile: orcaswap.BuyProfileName,
		ProfileFingerprint: hex.EncodeToString(actionHash[:]),
		ControlStatePath:   "/private/control.json", Source: owner,
		MaxInputTokenAmount: 1_000, MaxFeeLamports: 5_000,
		SubmitterPublicKey: publicKey, OrcaBuy: &route,
	}, privateKey, response, transaction
}

func buySubmitterInstructions(t *testing.T, policy orcaswap.BuyPolicyV2) []solana.Instruction {
	t.Helper()
	owner, err := solana.Decode32(policy.Owner)
	if err != nil {
		t.Fatal(err)
	}
	tokenProgram, err := solana.Decode32(orcaswap.TokenProgram)
	if err != nil {
		t.Fatal(err)
	}
	seed := []byte("1785688960889")
	hash := sha256.New()
	_, _ = hash.Write(owner[:])
	_, _ = hash.Write(seed)
	_, _ = hash.Write(tokenProgram[:])
	temporary := solana.Encode(hash.Sum(nil))
	create := binary.LittleEndian.AppendUint32(nil, 3)
	create = append(create, owner[:]...)
	create = binary.LittleEndian.AppendUint64(create, uint64(len(seed)))
	create = append(create, seed...)
	create = binary.LittleEndian.AppendUint64(create, 2_039_280)
	create = binary.LittleEndian.AppendUint64(create, 165)
	create = append(create, tokenProgram[:]...)
	initialize := append([]byte{18}, owner[:]...)
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
