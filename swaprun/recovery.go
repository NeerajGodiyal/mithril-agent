package swaprun

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func validateRecoveredSwap(
	actionID string,
	profile Profile,
	current *state,
	now time.Time,
) error {
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		return errors.New("recovered swap profile is invalid")
	}
	if err := validatePendingAction(actionID, current, profile, fingerprint); err != nil {
		return err
	}
	if current.built == nil || current.simulation == nil || current.signed == nil ||
		current.preSendObservation == nil || current.sendStarted == nil ||
		current.sendStartedAt.IsZero() || current.canceled != nil || current.reconciliation != nil {
		return errors.New("recovered swap state is incomplete")
	}
	if err := validateStarted(profile, *current.started, current.sendStartedAt); err != nil {
		return err
	}
	if current.sendStartedAt.Unix() >= current.started.ScheduleWindowEndUnix {
		return errors.New("recovered swap send time is outside its schedule window")
	}
	if current.sendStartedAt.After(now.Add(profile.ClockUncertaintyLimit())) {
		return errors.New("recovered swap send time is in the future")
	}

	built := current.built
	if built.BlockhashContextSlot == 0 ||
		built.BlockhashContextSlot < current.started.ObservationSlot ||
		built.ObservedBlockHeight == 0 ||
		built.FeeLamports == 0 || built.FeeLamports > profile.MaxFeeLamports ||
		built.FeeMinContextSlot != built.BlockhashContextSlot ||
		built.PrimaryFeeContextSlot < built.FeeMinContextSlot ||
		built.SecondaryFeeContextSlot < built.FeeMinContextSlot ||
		built.ObservedBlockHeight >= built.LastValidBlockHeight ||
		built.LastValidBlockHeight-built.ObservedBlockHeight > profile.MaxBlockHeightWindow {
		return errors.New("recovered swap build evidence is invalid")
	}
	if profile.isBuy() && (built.InputTokenBalance < profile.InputTokenAmount ||
		built.PrimaryInputTokenSlot < built.BlockhashContextSlot ||
		built.SecondaryInputTokenSlot < built.BlockhashContextSlot) ||
		!profile.isBuy() && (built.InputTokenBalance != 0 ||
			built.PrimaryInputTokenSlot != 0 || built.SecondaryInputTokenSlot != 0) {
		return errors.New("recovered input-token evidence is invalid")
	}
	message, err := base64.StdEncoding.Strict().DecodeString(built.MessageBase64)
	if err != nil {
		return errors.New("recovered swap message is invalid")
	}
	intent, err := decodeRouteMessage(profile, message)
	if err != nil || intent.InputAmount != profile.inputAmount() ||
		intent.MinimumOutput != built.MinimumOutput ||
		intent.RecentBlockhash != built.RecentBlockhash ||
		intent.OutputAccountMade != built.OutputAccountCreated ||
		(intent.OutputAccountMade &&
			(built.OutputAccountRent == 0 ||
				built.OutputAccountRent > profile.maxRouteRent())) ||
		(!intent.OutputAccountMade && built.OutputAccountRent != 0) ||
		(profile.isBuy() && (built.TemporaryAccountRent != intent.RentLamports ||
			built.TemporaryAccountRent == 0 || built.TemporaryAccountRent > profile.maxRouteRent())) ||
		(!profile.isBuy() && built.TemporaryAccountRent != 0) {
		return errors.New("recovered swap message is outside policy")
	}

	simulation := current.simulation
	if strings.TrimSpace(simulation.ProviderIdentity) == "" ||
		simulation.ProviderIdentity != strings.TrimSpace(simulation.ProviderIdentity) ||
		simulation.MinContextSlot != built.BlockhashContextSlot ||
		simulation.ContextSlot < simulation.MinContextSlot ||
		!validNonzeroSHA256(simulation.LogsSHA256) {
		return errors.New("recovered swap simulation evidence is invalid")
	}
	if err := ValidateObservation(profile, *current.preSendObservation, current.sendStartedAt); err != nil ||
		current.preSendObservation.Account.Slot < current.started.ObservationSlot {
		return errors.New("recovered swap pre-send observation is invalid")
	}

	response := current.signed.Response
	request := signerRequestFor(actionID, profile, fingerprint, *current.started, *built)
	transactionSHA256, err := validateSwapSignerResponse(profile, *built, message, request, response)
	if err != nil {
		return err
	}
	if current.sendStarted.Signature != response.Signature ||
		current.sendStarted.TransactionSHA256 != transactionSHA256 {
		return errors.New("recovered send marker does not match the signed transaction")
	}
	if profile.PriceTrigger == nil {
		if current.sendStarted.PriceEvidence != nil {
			return errors.New("recovered send marker has unconfigured price evidence")
		}
	} else if current.sendStarted.PriceEvidence == nil ||
		validateStoredPriceEvidence(*profile.PriceTrigger, *current.sendStarted.PriceEvidence) != nil ||
		validateLivePriceEvidence(
			*profile.PriceTrigger, *current.sendStarted.PriceEvidence, current.sendStartedAt,
		) != nil ||
		validatePriceEvidenceProgress(
			*current.started.PriceEvidence, *current.sendStarted.PriceEvidence,
		) != nil ||
		!executableMinimumSatisfies(profile, current.built.MinimumOutput) {
		return errors.New("recovered send marker has invalid price evidence")
	}
	if current.submission != nil &&
		(current.submission.Signature != response.Signature ||
			current.submission.LastValidBlockHeight != built.LastValidBlockHeight ||
			(current.submission.State != txflow.StateAccepted &&
				current.submission.State != txflow.StateAmbiguous)) {
		return errors.New("recovered submission does not match the signed transaction")
	}
	return nil
}

func validateSwapSignerResponse(
	profile Profile,
	built builtRecord,
	message []byte,
	request signer.Request,
	response signer.Response,
) (string, error) {
	messageHash := sha256.Sum256(message)
	messageSHA256 := hex.EncodeToString(messageHash[:])
	signature, err := solana.Decode64(response.Signature)
	if err != nil {
		return "", errors.New("swap signature is invalid")
	}
	owner, err := solana.Decode32(profile.owner())
	if err != nil || !ed25519.Verify(ed25519.PublicKey(owner[:]), message, signature[:]) {
		return "", errors.New("swap signature does not match its message")
	}
	signedTransaction := make([]byte, 0, 1+len(signature)+len(message))
	signedTransaction = append(signedTransaction, 1)
	signedTransaction = append(signedTransaction, signature[:]...)
	signedTransaction = append(signedTransaction, message...)
	transactionHash := sha256.Sum256(signedTransaction)
	transactionSHA256 := hex.EncodeToString(transactionHash[:])
	binding, err := signer.RiskBinding(request, messageSHA256)
	if err != nil || response.RequestSHA256 != binding.RequestSHA256 {
		return "", errors.New("signer response does not match its exact request")
	}
	if response.ActionID != request.ActionID || response.MessageSHA256 != messageSHA256 ||
		response.TransactionSHA256 != transactionSHA256 ||
		response.BlockhashContextSlot != built.BlockhashContextSlot ||
		response.FeeLamports != built.FeeLamports ||
		response.LastValidBlockHeight != built.LastValidBlockHeight {
		return "", errors.New("signer response does not match the swap")
	}
	wantMetadata := sealedtx.Metadata{
		Version:              sealedtx.Version,
		Domain:               sealedtx.Domain,
		ActionID:             request.ActionID,
		MessageSHA256:        messageSHA256,
		TransactionSHA256:    transactionSHA256,
		Signature:            response.Signature,
		BlockhashContextSlot: response.BlockhashContextSlot,
		FeeLamports:          built.FeeLamports,
		LastValidBlockHeight: built.LastValidBlockHeight,
	}
	if response.SealedTransaction.Metadata != wantMetadata ||
		!validBase64Value(response.SealedTransaction.EphemeralPublicKeyBase64) ||
		!validBase64Value(response.SealedTransaction.NonceBase64) ||
		!validBase64Value(response.SealedTransaction.CiphertextBase64) {
		return "", errors.New("sealed transaction does not match the swap")
	}
	if err := signer.VerifyResponseAttestation(
		profile.owner(), response.SignerAttestation.SubmitterPublicKey, response,
	); err != nil {
		return "", errors.New("sealed transaction attestation is invalid")
	}
	return transactionSHA256, nil
}

func signerRequestFor(
	actionID string,
	profile Profile,
	fingerprint string,
	started startedRecord,
	built builtRecord,
) signer.Request {
	return signer.Request{
		Domain: profile.requestDomain(), Cluster: profile.Cluster,
		Profile: profile.Name, ProfileVersion: profile.Version,
		ProfileFingerprint: fingerprint, ActionID: actionID,
		ScheduleWindowStartUnix: started.ScheduleWindowStartUnix,
		ScheduleWindowEndUnix:   started.ScheduleWindowEndUnix,
		MessageBase64:           built.MessageBase64,
		BlockhashContextSlot:    built.BlockhashContextSlot,
		FeeLamports:             built.FeeLamports,
		FeeMinContextSlot:       built.FeeMinContextSlot,
		PrimaryFeeContextSlot:   built.PrimaryFeeContextSlot,
		SecondaryFeeContextSlot: built.SecondaryFeeContextSlot,
		RecentBlockhash:         built.RecentBlockhash,
		ObservedBlockHeight:     built.ObservedBlockHeight,
		LastValidBlockHeight:    built.LastValidBlockHeight,
	}
}

func validBase64Value(value string) bool {
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) != 0
}

func validNonzeroSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != value {
		return false
	}
	for _, item := range decoded {
		if item != 0 {
			return true
		}
	}
	return false
}
