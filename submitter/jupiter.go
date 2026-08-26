package submitter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"

	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

// ValidateJupiterResponse independently opens and validates one sealed
// Mainnet response without returning the signed transaction or submitting it.
func ValidateJupiterResponse(
	policy Policy,
	privateKey string,
	request signer.Request,
	response signer.Response,
) error {
	transaction, err := openJupiterResponse(policy, privateKey, request, response)
	clear(transaction)
	return err
}

// PrepareJupiterRecovery independently validates one sealed Mainnet response
// and durably preserves the exact transaction and lookup-table evidence needed
// before a future submission. It never opens an RPC or submits a transaction.
func PrepareJupiterRecovery(
	policy Policy,
	privateKey string,
	request signer.Request,
	response signer.Response,
) error {
	transaction, err := openJupiterResponse(policy, privateKey, request, response)
	if err != nil {
		return err
	}
	defer clear(transaction)
	return prepareJupiterRecovery(policy, request, response, transaction)
}

func openJupiterResponse(
	policy Policy,
	privateKey string,
	request signer.Request,
	response signer.Response,
) ([]byte, error) {
	message, tables, err := validateJupiterRequest(policy, request)
	if err != nil {
		return nil, err
	}
	publicKey, err := sealedtx.PublicKey(privateKey)
	if err != nil || publicKey != policy.SubmitterPublicKey {
		return nil, errors.New("submitter key does not match policy")
	}
	transaction, err := sealedtx.OpenConfidential(privateKey, response.SealedTransaction)
	if err != nil {
		return nil, err
	}
	valid := false
	defer func() {
		if !valid {
			clear(transaction)
		}
	}()
	decoded, err := solana.DecodeSignedV0Transaction(transaction, tables)
	if err != nil || !bytes.Equal(decoded.Message.Raw, message) {
		return nil, errors.New("sealed Jupiter transaction does not match the checked message")
	}
	messageHash := sha256.Sum256(message)
	transactionHash := sha256.Sum256(transaction)
	binding, err := signer.RiskBinding(request, hex.EncodeToString(messageHash[:]))
	if err != nil {
		return nil, err
	}
	if response.ActionID != request.ActionID ||
		response.RequestSHA256 != binding.RequestSHA256 ||
		response.Signature != "" || response.SealedTransaction.Metadata.Signature != "" ||
		response.MessageSHA256 != hex.EncodeToString(messageHash[:]) ||
		response.TransactionSHA256 != hex.EncodeToString(transactionHash[:]) ||
		response.BlockhashContextSlot != request.BlockhashContextSlot ||
		response.FeeLamports != request.FeeLamports ||
		response.LastValidBlockHeight != request.LastValidBlockHeight ||
		response.SealedTransaction.Metadata != responseMetadata(response) {
		return nil, errors.New("sealed Jupiter signer response is invalid")
	}
	if err := signer.VerifyResponseAttestation(
		policy.AttestationPublicKey, policy.SubmitterPublicKey, response,
	); err != nil {
		return nil, err
	}
	valid = true
	return transaction, nil
}

func validateJupiterRequest(
	policy Policy,
	request signer.Request,
) ([]byte, map[[32]byte][][32]byte, error) {
	if err := ValidateJupiterPolicy(policy); err != nil {
		return nil, nil, err
	}
	if request.JupiterCandidate == nil || request.JupiterProviders == nil ||
		*request.JupiterProviders != policy.Evidence ||
		request.Domain != jupiterswap.RequestDomain ||
		request.Cluster != policy.Cluster || request.Profile != policy.Profile ||
		request.ProfileVersion != jupiterswap.ProfileVersion ||
		request.ProfileFingerprint != policy.ProfileFingerprint ||
		request.MessageBase64 != request.JupiterCandidate.MessageBase64 ||
		request.FeeLamports == 0 || request.FeeLamports > policy.MaxFeeLamports ||
		request.BlockhashContextSlot == 0 ||
		request.FeeMinContextSlot != request.BlockhashContextSlot ||
		request.PrimaryFeeContextSlot < request.FeeMinContextSlot ||
		request.SecondaryFeeContextSlot < request.FeeMinContextSlot ||
		request.ObservedBlockHeight == 0 ||
		request.ObservedBlockHeight >= request.LastValidBlockHeight ||
		request.LastValidBlockHeight-request.ObservedBlockHeight > policy.MaxBlockHeightWindow ||
		request.LastValidBlockHeight != request.JupiterCandidate.LastValidBlockHeight ||
		request.ScheduleWindowStartUnix < policy.ScheduleAnchorUnix ||
		request.ScheduleWindowEndUnix <= request.ScheduleWindowStartUnix ||
		request.ScheduleWindowEndUnix-request.ScheduleWindowStartUnix !=
			int64(policy.ScheduleWindowSeconds) ||
		(request.ScheduleWindowStartUnix-policy.ScheduleAnchorUnix)%
			int64(policy.ScheduleWindowSeconds) != 0 ||
		slotDistance(request.PrimaryFeeContextSlot, request.SecondaryFeeContextSlot) >
			proposalcheck.MaxEvidenceSlotSkew {
		return nil, nil, errors.New("Jupiter signer request is outside submitter policy")
	}
	expectedActionID, err := jupiterswap.ComputeActionID(
		policy.ProfileFingerprint, request.ScheduleWindowStartUnix,
	)
	if err != nil || request.ActionID != expectedActionID {
		return nil, nil, errors.New("Jupiter signer request action is outside submitter policy")
	}
	message, tables, err := proposalcheck.ValidateCandidateMaterial(
		*policy.Jupiter, *request.JupiterCandidate,
	)
	if err != nil || !jupiterRequestAmountValid(
		policy, request.JupiterCandidate.Request.InputAmount,
	) {
		return nil, nil, errors.New("Jupiter candidate is outside submitter policy")
	}
	intent, err := jupiterswap.ValidateV0Message(
		*policy.Jupiter, request.JupiterCandidate.Request,
		request.JupiterCandidate.Quote, message, tables,
	)
	if err != nil || intent.RecentBlockhash != request.RecentBlockhash {
		return nil, nil, errors.New("Jupiter signer request is outside submitter policy")
	}
	return message, tables, nil
}

// ValidateJupiterPolicy verifies the independent Mainnet submitter envelope.
// Validation alone neither writes recovery evidence nor submits a transaction.
func ValidateJupiterPolicy(policy Policy) error {
	if policy.Cluster != "mainnet-beta" || policy.Profile != jupiterswap.ProfileName ||
		policy.Jupiter == nil || policy.OrcaSwap != nil || policy.OrcaBuy != nil ||
		policy.Destination != "" || policy.MaxFeeLamports == 0 ||
		policy.Source != policy.Jupiter.Owner ||
		!jupiterSubmitterAmountsValid(policy) ||
		policy.MaxFeeLamports > policy.Jupiter.MaxFeeLamports ||
		policy.ScheduleWindowSeconds < 60 || policy.ScheduleWindowSeconds > 86_400 ||
		86_400%policy.ScheduleWindowSeconds != 0 ||
		policy.ScheduleAnchorUnix <= 0 || policy.ScheduleAnchorUnix%86_400 != 0 ||
		policy.MaxBlockHeightWindow == 0 || policy.MaxBlockHeightWindow > 150 ||
		(policy.RecoveryMode != MainnetRecoveryStopOnly &&
			policy.RecoveryMode != MainnetRecoveryExactRetry) ||
		!filepath.IsAbs(policy.ControlStatePath) ||
		filepath.Clean(policy.ControlStatePath) != policy.ControlStatePath {
		return errors.New("Jupiter submitter policy is incomplete")
	}
	if err := policy.Jupiter.Validate(); err != nil {
		return errors.New("Jupiter submitter route is invalid")
	}
	if err := policy.Evidence.ValidateArchiveProbe(); err != nil {
		return errors.New("Jupiter submitter evidence providers are invalid")
	}
	attestor, err := solana.Decode32(policy.AttestationPublicKey)
	if err != nil || policy.AttestationPublicKey == policy.Source ||
		solana.Encode(attestor[:]) != policy.AttestationPublicKey {
		return errors.New("Jupiter submitter attestation key is invalid")
	}
	fingerprint, err := policy.Jupiter.Fingerprint()
	if err != nil || fingerprint != policy.ProfileFingerprint {
		return errors.New("Jupiter submitter profile fingerprint is invalid")
	}
	if err := sealedtx.ValidatePublicKey(policy.SubmitterPublicKey); err != nil {
		return err
	}
	source, err := solana.Decode32(policy.Source)
	if err != nil || policy.SubmitterPublicKey == hex.EncodeToString(source[:]) ||
		policy.SubmitterPublicKey == hex.EncodeToString(attestor[:]) {
		return errors.New("Jupiter wallet, attestation, and submitter identities must differ")
	}
	return nil
}

func jupiterSubmitterAmountsValid(policy Policy) bool {
	if policy.Jupiter == nil {
		return false
	}
	if policy.Jupiter.NativeInput() {
		return policy.MaxLamports > 0 && policy.MaxInputTokenAmount == 0 &&
			policy.MaxLamports <= policy.Jupiter.MaxInputAmount
	}
	return policy.Jupiter.NativeOutput() && policy.MaxLamports == 0 &&
		policy.MaxInputTokenAmount > 0 &&
		policy.MaxInputTokenAmount <= policy.Jupiter.MaxInputAmount
}

func jupiterRequestAmountValid(policy Policy, inputAmount uint64) bool {
	if inputAmount == 0 || policy.Jupiter == nil {
		return false
	}
	if policy.Jupiter.NativeInput() {
		return inputAmount <= policy.MaxLamports
	}
	return policy.Jupiter.NativeOutput() && inputAmount <= policy.MaxInputTokenAmount
}

func slotDistance(a, b uint64) uint64 {
	if a < b {
		return b - a
	}
	return a - b
}
