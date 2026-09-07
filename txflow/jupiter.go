package txflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

// ExpectedJupiter binds the exact value policy and lookup-table evidence used
// when a version-0 Jupiter transaction was checked.
type ExpectedJupiter struct {
	Signature            string                             `json:"signature"`
	TransactionSHA256    string                             `json:"transaction_sha256"`
	RecentBlockhash      string                             `json:"recent_blockhash"`
	LastValidBlockHeight uint64                             `json:"last_valid_block_height"`
	Policy               jupiterswap.Policy                 `json:"policy"`
	InputAmount          uint64                             `json:"input_amount"`
	EstimatedOutput      uint64                             `json:"estimated_output"`
	MinimumOutput        uint64                             `json:"minimum_output"`
	SlippageBPS          uint16                             `json:"slippage_bps"`
	AddressTables        []jupiterswap.AddressTableEvidence `json:"address_tables,omitempty"`
}

type JupiterEffectEvidence struct {
	TransactionSHA256   string                `json:"transaction_sha256"`
	FeeLamports         uint64                `json:"fee_lamports"`
	InputAmount         uint64                `json:"input_amount"`
	MinimumOutput       uint64                `json:"minimum_output"`
	OutputAmount        uint64                `json:"output_amount"`
	OutputAccountRent   uint64                `json:"output_account_rent_lamports,omitempty"`
	PrimaryEffectSlot   uint64                `json:"primary_effect_slot"`
	SecondaryEffectSlot uint64                `json:"secondary_effect_slot"`
	Payer               *JupiterPayerEvidence `json:"payer,omitempty"`
	Token               *JupiterTokenEvidence `json:"token,omitempty"`
}

// JupiterTokenEvidence retains the verified non-SOL token account balances.
// Missing evidence is unknown, not an empty account.
type JupiterTokenEvidence struct {
	Version   uint32 `json:"version"`
	Account   string `json:"account"`
	Mint      string `json:"mint"`
	Owner     string `json:"owner"`
	PreUnits  uint64 `json:"pre_units,string"`
	PostUnits uint64 `json:"post_units,string"`
}

// ValidTokenEffects binds retained balances to the canonical account and swap.
func (e JupiterEffectEvidence) ValidTokenEffects(policy jupiterswap.Policy, failed bool) bool {
	t := e.Token
	mint := policy.InputMint
	if policy.NativeInput() {
		mint = policy.OutputMint
	}
	account, err := orcaswap.AssociatedTokenAddress(policy.Owner, mint)
	if err != nil || t == nil || t.Version != 1 || t.Account != account ||
		t.Mint != mint || t.Owner != policy.Owner {
		return false
	}
	if failed {
		return t.PreUnits == t.PostUnits && e.OutputAmount == 0 && e.OutputAccountRent == 0
	}
	if policy.NativeInput() {
		return t.PostUnits >= t.PreUnits && t.PostUnits-t.PreUnits == e.OutputAmount
	}
	return t.PreUnits >= t.PostUnits && t.PreUnits-t.PostUnits == e.InputAmount
}

// JupiterPayerEvidence retains the verified payer balances for one transaction.
// ReclaimedInputLamports is the balance returned by closing an existing empty
// wrapped-SOL input account, not trading profit. Missing evidence is unknown.
type JupiterPayerEvidence struct {
	Version                uint32 `json:"version"`
	PreLamports            uint64 `json:"pre_lamports,string"`
	PostLamports           uint64 `json:"post_lamports,string"`
	ReclaimedInputLamports uint64 `json:"reclaimed_input_lamports,string"`
}

// ValidPayerEffects checks retained payer balances against the verified swap
// effects. It rejects historical records without payer evidence.
func (e JupiterEffectEvidence) ValidPayerEffects(nativeInput, failed bool) bool {
	p := e.Payer
	if p == nil || p.Version != 1 {
		return false
	}
	if failed {
		return p.ReclaimedInputLamports == 0 && e.OutputAmount == 0 && e.OutputAccountRent == 0 &&
			jupiterPayerEffectMatches(p.PreLamports, p.PostLamports, 0, e.FeeLamports, 0, 0)
	}
	if nativeInput {
		return jupiterPayerEffectMatches(p.PreLamports, p.PostLamports, e.InputAmount,
			e.FeeLamports, e.OutputAccountRent, p.ReclaimedInputLamports)
	}
	return p.ReclaimedInputLamports == 0 && e.OutputAccountRent == 0 &&
		jupiterPayerEffectMatches(p.PreLamports, p.PostLamports, 0, e.FeeLamports, 0, e.OutputAmount)
}

// ReconcileJupiterExpected obtains finalized evidence from two independent
// providers. It is read-only and cannot submit a transaction.
func (l *Lifecycle) ReconcileJupiterExpected(
	ctx context.Context,
	submission Submission,
	expected ExpectedJupiter,
	expectedFeeLamports uint64,
) (Reconciliation, error) {
	if submission.State != StateAccepted && submission.State != StateAmbiguous {
		return Reconciliation{}, errors.New("submission state is invalid")
	}
	if submission.LastValidBlockHeight == 0 || expectedFeeLamports == 0 ||
		expected.Signature != submission.Signature || expected.Policy.Validate() != nil ||
		expected.LastValidBlockHeight != submission.LastValidBlockHeight ||
		expectedFeeLamports > expected.Policy.MaxFeeLamports ||
		expected.InputAmount == 0 || expected.InputAmount > expected.Policy.MaxInputAmount ||
		expected.EstimatedOutput == 0 || expected.MinimumOutput < expected.Policy.MinOutputAmount ||
		expected.MinimumOutput > expected.EstimatedOutput || expected.SlippageBPS == 0 ||
		expected.SlippageBPS > expected.Policy.MaxSlippageBPS ||
		!validSHA256(expected.TransactionSHA256) {
		return Reconciliation{}, errors.New("expected Jupiter swap does not match the submission")
	}
	if _, err := solana.Decode32(expected.RecentBlockhash); err != nil {
		return Reconciliation{}, errors.New("expected Jupiter blockhash is invalid")
	}
	if _, err := solana.Decode64(expected.Signature); err != nil {
		return Reconciliation{}, errors.New("expected Jupiter signature is invalid")
	}
	if _, err := jupiterswap.DecodeAddressTables(expected.AddressTables); err != nil {
		return Reconciliation{}, errors.New("expected Jupiter address-table evidence is invalid")
	}
	primary, secondary, err := queryStatuses(ctx, l.primary, l.secondary, submission.Signature)
	if err != nil {
		return Reconciliation{}, err
	}
	result := Reconciliation{
		Signature: submission.Signature, Verdict: VerdictPending,
		PrimaryFound: primary.Found, SecondaryFound: secondary.Found,
		PrimarySlot: primary.Slot, SecondarySlot: secondary.Slot,
		PrimaryStatus: primary.ConfirmationStatus, SecondaryStatus: secondary.ConfirmationStatus,
		PrimaryFailed: primary.Failed, SecondaryFailed: secondary.Failed,
		PrimaryErrorFingerprint:   primary.ErrorFingerprint,
		SecondaryErrorFingerprint: secondary.ErrorFingerprint,
	}
	if primary.Found && secondary.Found {
		if primary.Slot != secondary.Slot || primary.Failed != secondary.Failed ||
			primary.ErrorFingerprint != secondary.ErrorFingerprint {
			result.Verdict, result.DivergenceKind = VerdictDiverged, DivergenceStatus
			return result, nil
		}
		result.Slot = primary.Slot
		if primary.ConfirmationStatus == "finalized" && secondary.ConfirmationStatus == "finalized" {
			if primary.Failed {
				result.Verdict = VerdictFailed
			} else {
				result.Verdict = VerdictFinalized
			}
			effects, err := l.verifyJupiterEffects(
				ctx, submission.Signature, expected, expectedFeeLamports, result.Slot,
				primary.Failed, primary.ErrorFingerprint,
			)
			if err != nil {
				if errors.Is(err, errFinalizedEffectsDiverged) {
					result.Verdict, result.DivergenceKind = VerdictDiverged, DivergenceEffects
					return result, nil
				}
				return Reconciliation{}, err
			}
			result.JupiterEffects = &effects
			return result, nil
		}
	}
	primaryHeight, secondaryHeight, err := queryBlockHeights(ctx, l.primary, l.secondary)
	if err != nil {
		return Reconciliation{}, err
	}
	result.PrimaryBlockHeight, result.SecondaryBlockHeight = primaryHeight, secondaryHeight
	if primaryHeight > submission.LastValidBlockHeight && secondaryHeight > submission.LastValidBlockHeight &&
		(!primary.Found || !secondary.Found) {
		result.Verdict = VerdictUnresolved
	}
	return result, nil
}

func (l *Lifecycle) verifyJupiterEffects(
	ctx context.Context,
	signature string,
	expected ExpectedJupiter,
	expectedFeeLamports, finalizedSlot uint64,
	failed bool,
	errorFingerprint string,
) (JupiterEffectEvidence, error) {
	a, b, err := queryPair(
		ctx,
		func(ctx context.Context) (solanarpc.TransactionEffect, error) {
			return l.primary.TransactionEffect(ctx, signature)
		},
		func(ctx context.Context) (solanarpc.TransactionEffect, error) {
			return l.secondary.TransactionEffect(ctx, signature)
		},
	)
	if err != nil {
		return JupiterEffectEvidence{}, errors.New("query independent finalized Jupiter effects")
	}
	if a.Slot != finalizedSlot || b.Slot != finalizedSlot || a.Failed != failed || b.Failed != failed ||
		a.ErrorFingerprint != errorFingerprint || b.ErrorFingerprint != errorFingerprint ||
		a.FeeLamports != expectedFeeLamports || b.FeeLamports != expectedFeeLamports ||
		!bytes.Equal(a.Transaction, b.Transaction) || !slices.Equal(a.PreBalances, b.PreBalances) ||
		!slices.Equal(a.PostBalances, b.PostBalances) ||
		!tokenBalancesEqual(a.PreTokenBalances, b.PreTokenBalances) ||
		!tokenBalancesEqual(a.PostTokenBalances, b.PostTokenBalances) {
		return JupiterEffectEvidence{}, errFinalizedEffectsDiverged
	}
	tables, err := jupiterswap.DecodeAddressTables(expected.AddressTables)
	if err != nil {
		return JupiterEffectEvidence{}, errFinalizedEffectsDiverged
	}
	request := jupiterquote.Request{
		Taker: expected.Policy.Owner, InputMint: expected.Policy.InputMint,
		OutputMint: expected.Policy.OutputMint, InputAmount: expected.InputAmount,
		SlippageBPS: expected.SlippageBPS,
	}
	if expected.Policy.NativeInput() {
		request.DestinationTokenAccount, err = orcaswap.AssociatedTokenAddress(
			expected.Policy.Owner, expected.Policy.OutputMint,
		)
		if err != nil {
			return JupiterEffectEvidence{}, errFinalizedEffectsDiverged
		}
	}
	quote := jupiterquote.Result{
		InputAmount: expected.InputAmount, EstimatedOutput: expected.EstimatedOutput,
		MinimumOutput: expected.MinimumOutput,
	}
	intent, embeddedSignature, err := jupiterswap.ValidateSignedV0Transaction(
		expected.Policy, request, quote, a.Transaction, tables,
	)
	if err != nil || embeddedSignature != expected.Signature ||
		intent.RecentBlockhash != expected.RecentBlockhash {
		return JupiterEffectEvidence{}, errFinalizedEffectsDiverged
	}
	transactionHash := sha256.Sum256(a.Transaction)
	if hex.EncodeToString(transactionHash[:]) != expected.TransactionSHA256 {
		return JupiterEffectEvidence{}, errFinalizedEffectsDiverged
	}
	decoded, err := solana.DecodeSignedV0Transaction(a.Transaction, tables)
	if err != nil || len(a.PreBalances) != len(a.PostBalances) ||
		len(a.PreBalances) != len(decoded.Message.AccountKeys) ||
		!tokenBalanceIndexesValid(a.PreTokenBalances, len(decoded.Message.AccountKeys)) ||
		!tokenBalanceIndexesValid(a.PostTokenBalances, len(decoded.Message.AccountKeys)) {
		return JupiterEffectEvidence{}, errFinalizedEffectsDiverged
	}
	if failed {
		if !feeOnlyEffects(a, expectedFeeLamports) {
			return JupiterEffectEvidence{}, errFinalizedEffectsDiverged
		}
		return JupiterEffectEvidence{
			TransactionSHA256: expected.TransactionSHA256, FeeLamports: expectedFeeLamports,
			InputAmount: intent.InputAmount, MinimumOutput: intent.MinimumOutput,
			PrimaryEffectSlot: a.Slot, SecondaryEffectSlot: b.Slot,
			Payer: &JupiterPayerEvidence{Version: 1, PreLamports: a.PreBalances[0], PostLamports: a.PostBalances[0]},
			Token: existingJupiterTokenEvidence(a, decoded.Message.AccountKeys, expected.Policy),
		}, nil
	}
	if !expected.Policy.NativeInput() {
		outputAmount, ok := jupiterTokenToSOLEffects(
			a, decoded, intent, expected.Policy.Owner, expected.Policy.InputMint,
			expected.InputAmount, expected.MinimumOutput, expectedFeeLamports,
		)
		if !ok {
			return JupiterEffectEvidence{}, errFinalizedEffectsDiverged
		}
		return JupiterEffectEvidence{
			TransactionSHA256: expected.TransactionSHA256,
			FeeLamports:       expectedFeeLamports, InputAmount: expected.InputAmount,
			MinimumOutput: expected.MinimumOutput, OutputAmount: outputAmount,
			PrimaryEffectSlot: a.Slot, SecondaryEffectSlot: b.Slot,
			Payer: &JupiterPayerEvidence{Version: 1, PreLamports: a.PreBalances[0], PostLamports: a.PostBalances[0]},
			Token: existingJupiterTokenEvidence(a, decoded.Message.AccountKeys, expected.Policy),
		}, nil
	}

	inputIndex := messageAccountIndex(decoded.Message.AccountKeys, intent.SourceTokenAccount)
	outputIndex := messageAccountIndex(decoded.Message.AccountKeys, intent.DestinationTokenAccount)
	if inputIndex <= 0 || outputIndex <= 0 || inputIndex == outputIndex ||
		inputIndex > int(^uint16(0)) || outputIndex > int(^uint16(0)) {
		return JupiterEffectEvidence{}, errFinalizedEffectsDiverged
	}
	preInput, preInputFound, err := tokenAmount(
		a.PreTokenBalances, uint16(inputIndex), orcaswap.WrappedSOLMint, expected.Policy.Owner,
	)
	if err != nil || preInput != 0 ||
		(a.PreBalances[inputIndex] == 0 && preInputFound) ||
		(a.PreBalances[inputIndex] != 0 && !preInputFound) {
		return JupiterEffectEvidence{}, errFinalizedEffectsDiverged
	}
	postInput, postInputFound, err := tokenAmount(
		a.PostTokenBalances, uint16(inputIndex), orcaswap.WrappedSOLMint, expected.Policy.Owner,
	)
	if err != nil || postInput != 0 || postInputFound || a.PostBalances[inputIndex] != 0 {
		return JupiterEffectEvidence{}, errFinalizedEffectsDiverged
	}
	preOutput, preOutputFound, err := tokenAmount(
		a.PreTokenBalances, uint16(outputIndex), expected.Policy.OutputMint, expected.Policy.Owner,
	)
	if err != nil {
		return JupiterEffectEvidence{}, errFinalizedEffectsDiverged
	}
	postOutput, found, err := tokenAmount(
		a.PostTokenBalances, uint16(outputIndex), expected.Policy.OutputMint, expected.Policy.Owner,
	)
	if err != nil || !found || postOutput < preOutput || postOutput-preOutput < expected.MinimumOutput {
		return JupiterEffectEvidence{}, errFinalizedEffectsDiverged
	}
	if !ownerTokenBalancesUnchanged(
		a.PreTokenBalances, a.PostTokenBalances, expected.Policy.Owner,
		uint16(inputIndex), uint16(outputIndex),
	) {
		return JupiterEffectEvidence{}, errFinalizedEffectsDiverged
	}
	outputRent := uint64(0)
	if a.PreBalances[outputIndex] == 0 {
		outputRent = a.PostBalances[outputIndex]
		if !intent.OutputAccountCreated || preOutputFound || outputRent == 0 ||
			outputRent > expected.Policy.MaxTokenAccountRentLamports {
			return JupiterEffectEvidence{}, errFinalizedEffectsDiverged
		}
	} else if !preOutputFound || a.PreBalances[outputIndex] != a.PostBalances[outputIndex] {
		return JupiterEffectEvidence{}, errFinalizedEffectsDiverged
	}
	if !jupiterPayerEffectMatches(
		a.PreBalances[0], a.PostBalances[0], expected.InputAmount, expectedFeeLamports,
		outputRent, a.PreBalances[inputIndex],
	) {
		return JupiterEffectEvidence{}, errFinalizedEffectsDiverged
	}
	return JupiterEffectEvidence{
		TransactionSHA256: expected.TransactionSHA256, FeeLamports: expectedFeeLamports,
		InputAmount: expected.InputAmount, MinimumOutput: expected.MinimumOutput,
		OutputAmount: postOutput - preOutput, OutputAccountRent: outputRent,
		PrimaryEffectSlot: a.Slot, SecondaryEffectSlot: b.Slot,
		Payer: &JupiterPayerEvidence{Version: 1, PreLamports: a.PreBalances[0],
			PostLamports: a.PostBalances[0], ReclaimedInputLamports: a.PreBalances[inputIndex]},
		Token: &JupiterTokenEvidence{Version: 1, Account: intent.DestinationTokenAccount,
			Mint: expected.Policy.OutputMint, Owner: expected.Policy.Owner,
			PreUnits: preOutput, PostUnits: postOutput},
	}, nil
}

// existingJupiterTokenEvidence requires both balance entries. In particular,
// fee-only failed transactions must not turn missing token metadata into zero.
func existingJupiterTokenEvidence(effect solanarpc.TransactionEffect, keys [][32]byte, policy jupiterswap.Policy) *JupiterTokenEvidence {
	mint := policy.InputMint
	if policy.NativeInput() {
		mint = policy.OutputMint
	}
	account, err := orcaswap.AssociatedTokenAddress(policy.Owner, mint)
	if err != nil {
		return nil
	}
	index := messageAccountIndex(keys, account)
	if index <= 0 || index > int(^uint16(0)) {
		return nil
	}
	pre, preFound, preErr := tokenAmount(effect.PreTokenBalances, uint16(index), mint, policy.Owner)
	post, postFound, postErr := tokenAmount(effect.PostTokenBalances, uint16(index), mint, policy.Owner)
	if preErr != nil || postErr != nil || !preFound || !postFound {
		return nil
	}
	return &JupiterTokenEvidence{Version: 1, Account: account, Mint: mint, Owner: policy.Owner,
		PreUnits: pre, PostUnits: post}
}

func feeOnlyEffects(effect solanarpc.TransactionEffect, fee uint64) bool {
	if len(effect.PreBalances) == 0 || effect.PreBalances[0] < effect.PostBalances[0] ||
		effect.PreBalances[0]-effect.PostBalances[0] != fee ||
		!tokenBalancesEqual(effect.PreTokenBalances, effect.PostTokenBalances) {
		return false
	}
	for index := 1; index < len(effect.PreBalances); index++ {
		if effect.PreBalances[index] != effect.PostBalances[index] {
			return false
		}
	}
	return true
}

// jupiterTokenToSOLEffects verifies the token-input/native-output exact-in
// accounting independently of the quote and signer.
func jupiterTokenToSOLEffects(
	effect solanarpc.TransactionEffect,
	decoded solana.SignedV0Transaction,
	intent jupiterswap.MessageIntent,
	owner, inputMint string,
	inputAmount, minimumOutput, fee uint64,
) (uint64, bool) {
	inputIndex := messageAccountIndex(decoded.Message.AccountKeys, intent.SourceTokenAccount)
	outputIndex := messageAccountIndex(decoded.Message.AccountKeys, intent.DestinationTokenAccount)
	if inputIndex <= 0 || outputIndex <= 0 || inputIndex == outputIndex ||
		inputIndex > int(^uint16(0)) || outputIndex > int(^uint16(0)) ||
		len(effect.PreBalances) != len(effect.PostBalances) ||
		len(effect.PreBalances) != len(decoded.Message.AccountKeys) {
		return 0, false
	}
	preInput, found, err := tokenAmount(
		effect.PreTokenBalances, uint16(inputIndex), inputMint, owner,
	)
	if err != nil || !found || preInput < inputAmount {
		return 0, false
	}
	postInput, found, err := tokenAmount(
		effect.PostTokenBalances, uint16(inputIndex), inputMint, owner,
	)
	if err != nil || !found || preInput-postInput != inputAmount ||
		effect.PreBalances[inputIndex] == 0 ||
		effect.PreBalances[inputIndex] != effect.PostBalances[inputIndex] {
		return 0, false
	}
	preOutput, preOutputFound, err := tokenAmount(
		effect.PreTokenBalances, uint16(outputIndex), orcaswap.WrappedSOLMint, owner,
	)
	if err != nil || preOutput != 0 || preOutputFound || effect.PreBalances[outputIndex] != 0 {
		return 0, false
	}
	postOutput, postOutputFound, err := tokenAmount(
		effect.PostTokenBalances, uint16(outputIndex), orcaswap.WrappedSOLMint, owner,
	)
	if err != nil || postOutput != 0 || postOutputFound || effect.PostBalances[outputIndex] != 0 ||
		!ownerTokenBalancesUnchanged(
			effect.PreTokenBalances, effect.PostTokenBalances, owner,
			uint16(inputIndex), uint16(outputIndex),
		) {
		return 0, false
	}
	if effect.PostBalances[0] > ^uint64(0)-fee {
		return 0, false
	}
	credited := effect.PostBalances[0] + fee
	if credited < effect.PreBalances[0] {
		return 0, false
	}
	outputAmount := credited - effect.PreBalances[0]
	return outputAmount, outputAmount >= minimumOutput
}

func jupiterPayerEffectMatches(pre, post, input, fee, outputRent, inputRentCredit uint64) bool {
	if input > ^uint64(0)-fee || input+fee > ^uint64(0)-outputRent {
		return false
	}
	debits := input + fee + outputRent
	if debits >= inputRentCredit {
		want := debits - inputRentCredit
		return pre >= post && pre-post == want
	}
	want := inputRentCredit - debits
	return post >= pre && post-pre == want
}
