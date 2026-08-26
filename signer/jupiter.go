package signer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

// TransactionCustodyRequest binds one durable authorization to the exact
// transaction and provider timestamp used for every custody retry.
type TransactionCustodyRequest struct {
	RequestSHA256 string
	TimestampMS   int64
	Transaction   []byte
}

type custodyBinding struct {
	requestSHA256      string
	custodyTimestampMS int64
}

// AuthorizeAndSignJupiterFileKey is the vendor-account-free, self-hosted canary
// adapter. It keeps the funded wallet and the zero-funds response attestor as
// distinct keys and still routes every request through the complete Jupiter
// policy, risk grant, durable cap, sealing, and attestation boundary. The signer
// command can call it only when both keys are configured; generated services and
// the live submit path remain restricted to Devnet.
func AuthorizeAndSignJupiterFileKey(
	ctx context.Context,
	policy Policy,
	walletKey ed25519.PrivateKey,
	attestationKey ed25519.PrivateKey,
	request Request,
	now time.Time,
) (Response, error) {
	if err := ValidateJupiterPolicy(policy); err != nil {
		return Response{}, err
	}
	walletPublic, err := PublicKey(walletKey)
	if err != nil || walletPublic != policy.Source {
		return Response{}, errors.New("Jupiter custody key does not match policy")
	}
	attestationPublic, err := PublicKey(attestationKey)
	if err != nil || attestationPublic != policy.AttestationPublicKey {
		return Response{}, errors.New("Jupiter attestation key does not match policy")
	}
	if request.JupiterCandidate == nil {
		return Response{}, errors.New("Jupiter signing request is outside policy")
	}
	_, tables, err := proposalcheck.ValidateCandidateMaterial(
		*policy.Jupiter,
		*request.JupiterCandidate,
	)
	if err != nil {
		return Response{}, err
	}
	return AuthorizeAndSignJupiterWith(
		ctx,
		policy,
		request,
		now,
		func(callbackCtx context.Context, custody TransactionCustodyRequest) ([]byte, error) {
			if callbackCtx.Err() != nil {
				return nil, callbackCtx.Err()
			}
			if custody.RequestSHA256 == "" || custody.TimestampMS <= 0 ||
				len(custody.Transaction) < 1+ed25519.SignatureSize+1 ||
				custody.Transaction[0] != 1 ||
				!bytes.Equal(
					custody.Transaction[1:1+ed25519.SignatureSize],
					make([]byte, ed25519.SignatureSize),
				) {
				return nil, errors.New("Jupiter custody transaction is invalid")
			}
			transaction, _, err := solana.SignV0Message(
				walletKey,
				custody.Transaction[1+ed25519.SignatureSize:],
				tables,
			)
			return transaction, err
		},
		func(callbackCtx context.Context, message []byte) ([]byte, error) {
			if callbackCtx.Err() != nil {
				return nil, callbackCtx.Err()
			}
			return ed25519.Sign(attestationKey, message), nil
		},
	)
}

// AuthorizeAndSignJupiterWith is the transaction-only custody boundary for one
// already-authorized Mainnet request. The explicitly configured file-key signer
// command calls it, but no live submitter does. signTransaction must evaluate
// the complete canonical transaction and return that exact transaction with its
// sole signature filled. The caller context is propagated to both remote
// callbacks.
func AuthorizeAndSignJupiterWith(
	ctx context.Context,
	policy Policy,
	request Request,
	now time.Time,
	signTransaction func(context.Context, TransactionCustodyRequest) ([]byte, error),
	attest func(context.Context, []byte) ([]byte, error),
) (Response, error) {
	if ctx == nil {
		return Response{}, errors.New("Jupiter custody context is unavailable")
	}
	if ctx.Err() != nil {
		return Response{}, errors.New("Jupiter custody signing was canceled")
	}
	if err := ValidateJupiterPolicy(policy); err != nil {
		return Response{}, err
	}
	if signTransaction == nil || attest == nil {
		return Response{}, errors.New("Jupiter custody signer is unavailable")
	}
	if err := ValidateScheduleWindowAt(request, now); err != nil {
		return Response{}, err
	}
	now = now.UTC()
	validated, err := ValidateJupiterRequest(policy, request)
	if err != nil {
		return Response{}, err
	}
	if err := verifyRiskGrant(policy, request, validated, now); err != nil {
		return Response{}, err
	}
	custodyTimestamp, err := custodyTimestampMS(now)
	if err != nil {
		return Response{}, err
	}
	if ctx.Err() != nil {
		return Response{}, errors.New("Jupiter custody signing was canceled")
	}
	if policy.Jupiter.NativeInput() {
		return authorizeAndSignJupiterNative(
			ctx, policy, request, validated, now, custodyTimestamp, signTransaction, attest,
		)
	}
	return authorizeAndSignJupiterToken(
		ctx, policy, request, validated, now, custodyTimestamp, signTransaction, attest,
	)
}

func authorizeAndSignJupiterNative(
	ctx context.Context,
	policy Policy,
	request Request,
	validated ValidatedRequest,
	now time.Time,
	custodyTimestamp int64,
	signTransaction func(context.Context, TransactionCustodyRequest) ([]byte, error),
	attest func(context.Context, []byte) ([]byte, error),
) (Response, error) {
	reservation, err := reservationForValidated(request, validated, now)
	if err != nil {
		return Response{}, err
	}
	reservation.CustodyTimestampMS = custodyTimestamp
	ledger, err := openAuthorizationLedger(policy, now)
	if err != nil {
		return Response{}, err
	}
	reservation, err = ledger.reserveEffective(now, request.ActionID, reservation)
	if err != nil {
		_ = ledger.close()
		return Response{}, err
	}
	if reservation.CustodyTimestampMS <= 0 {
		_ = ledger.close()
		return Response{}, errors.New("Jupiter custody request time is unavailable")
	}
	response, signErr := signJupiterAt(
		ctx, policy, request, now,
		custodyBinding{reservation.RequestSHA256, reservation.CustodyTimestampMS},
		signTransaction, attest,
	)
	if signErr != nil {
		_ = ledger.close()
		return Response{}, signErr
	}
	confirmed, err := reservationFor(policy, request, response, now)
	if err != nil {
		_ = ledger.close()
		return Response{}, err
	}
	if !sameAuthorizationReservation(confirmed, reservation) {
		_ = ledger.close()
		return Response{}, errors.New("Jupiter custody response changed its authorization debit")
	}
	if err := ledger.close(); err != nil {
		return Response{}, err
	}
	return response, nil
}

func authorizeAndSignJupiterToken(
	ctx context.Context,
	policy Policy,
	request Request,
	validated ValidatedRequest,
	now time.Time,
	custodyTimestamp int64,
	signTransaction func(context.Context, TransactionCustodyRequest) ([]byte, error),
	attest func(context.Context, []byte) ([]byte, error),
) (Response, error) {
	requestHash, err := immutableRequestHash(request)
	if err != nil {
		return Response{}, err
	}
	reservation, err := tokenReservationForValidated(
		request, requestHash, validated.MessageSHA256, validated, now,
	)
	if err != nil {
		return Response{}, err
	}
	reservation.CustodyTimestampMS = custodyTimestamp
	ledger, err := openBuyAuthorizationLedger(policy, now)
	if err != nil {
		return Response{}, err
	}
	reservation, err = ledger.reserveEffective(now, request.ActionID, reservation)
	if err != nil {
		_ = ledger.close()
		return Response{}, err
	}
	response, signErr := signJupiterAt(
		ctx, policy, request, now,
		custodyBinding{reservation.RequestSHA256, reservation.CustodyTimestampMS},
		signTransaction, attest,
	)
	if signErr != nil {
		_ = ledger.close()
		return Response{}, signErr
	}
	confirmed, err := jupiterTokenReservationFor(policy, request, response, now)
	if err != nil {
		_ = ledger.close()
		return Response{}, err
	}
	if !sameBuyAuthorizationReservation(confirmed, reservation) {
		_ = ledger.close()
		return Response{}, errors.New("Jupiter custody response changed its token authorization debit")
	}
	if err := ledger.close(); err != nil {
		return Response{}, err
	}
	return response, nil
}

func jupiterTokenReservationFor(
	policy Policy,
	request Request,
	response Response,
	now time.Time,
) (buyAuthorizationReservation, error) {
	requestHash, err := immutableRequestHash(request)
	if err != nil {
		return buyAuthorizationReservation{}, err
	}
	message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
	if err != nil {
		return buyAuthorizationReservation{}, errors.New("decode Jupiter token authorization message")
	}
	digest := sha256.Sum256(message)
	messageHash := hex.EncodeToString(digest[:])
	if response.MessageSHA256 != messageHash {
		return buyAuthorizationReservation{}, errors.New("signed message binding does not match")
	}
	validated, err := ValidateJupiterRequest(policy, request)
	if err != nil {
		return buyAuthorizationReservation{}, errors.New("decode Jupiter token authorization debit")
	}
	return tokenReservationForValidated(request, requestHash, messageHash, validated, now)
}

func signJupiterAt(
	ctx context.Context,
	policy Policy,
	request Request,
	now time.Time,
	binding custodyBinding,
	signTransaction func(context.Context, TransactionCustodyRequest) ([]byte, error),
	attest func(context.Context, []byte) ([]byte, error),
) (Response, error) {
	validated, err := ValidateJupiterRequest(policy, request)
	if err != nil {
		return Response{}, err
	}
	if err := verifyRiskGrant(policy, request, validated, now); err != nil {
		return Response{}, err
	}
	_, tables, err := proposalcheck.ValidateCandidateMaterial(
		*policy.Jupiter,
		*request.JupiterCandidate,
	)
	if err != nil {
		return Response{}, err
	}
	unsigned, err := solana.BuildUnsignedV0Transaction(validated.Message, tables)
	if err != nil {
		return Response{}, err
	}
	requestHash, err := immutableRequestHash(request)
	if err != nil {
		return Response{}, err
	}
	if requestHash != binding.requestSHA256 || binding.custodyTimestampMS <= 0 {
		return Response{}, errors.New("Jupiter custody request binding is unavailable")
	}
	transaction, err := signTransaction(ctx, TransactionCustodyRequest{
		RequestSHA256: requestHash,
		TimestampMS:   binding.custodyTimestampMS,
		Transaction:   unsigned,
	})
	defer clear(transaction)
	if ctx.Err() != nil {
		return Response{}, errors.New("Jupiter custody signing was canceled")
	}
	if err != nil {
		return Response{}, errors.New("Jupiter custody signer failed")
	}
	decoded, err := solana.DecodeSignedV0Transaction(transaction, tables)
	if err != nil || !bytes.Equal(decoded.Message.Raw, validated.Message) ||
		solana.Encode(decoded.Message.AccountKeys[0][:]) != policy.Source {
		return Response{}, errors.New("Jupiter custody signer returned a different transaction")
	}
	response, err := buildConfidentialResponse(
		policy,
		request,
		validated.Message,
		transaction,
		decoded.Signature,
		policy.AttestationPublicKey,
		func(message []byte) ([]byte, error) {
			return attest(ctx, message)
		},
	)
	if err != nil {
		return Response{}, err
	}
	if ctx.Err() != nil {
		return Response{}, errors.New("Jupiter custody attestation was canceled")
	}
	return response, nil
}

func custodyTimestampMS(now time.Time) (int64, error) {
	seconds := now.UTC().Unix()
	millis := int64(now.UTC().Nanosecond()) / int64(time.Millisecond)
	const maxInt64 = int64(^uint64(0) >> 1)
	if seconds <= 0 || seconds > (maxInt64-millis)/1_000 {
		return 0, errors.New("trusted signer time cannot identify a custody request")
	}
	return seconds*1_000 + millis, nil
}

// ValidateJupiterRequest applies signer-owned policy to one exact portable
// Mainnet proposal without authorizing or signing it.
func ValidateJupiterRequest(policy Policy, request Request) (ValidatedRequest, error) {
	if err := ValidateJupiterPolicy(policy); err != nil {
		return ValidatedRequest{}, err
	}
	if request.JupiterCandidate == nil || request.JupiterProviders == nil ||
		request.JupiterProviders.ValidateArchiveProbe() != nil ||
		request.Domain != jupiterswap.RequestDomain ||
		request.Cluster != policy.Cluster || request.Profile != policy.Profile ||
		request.ProfileVersion != policy.ProfileVersion ||
		request.ProfileFingerprint != policy.ProfileFingerprint ||
		request.MessageBase64 != request.JupiterCandidate.MessageBase64 ||
		request.LastValidBlockHeight != request.JupiterCandidate.LastValidBlockHeight {
		return ValidatedRequest{}, errors.New("Jupiter signing request is outside policy")
	}
	if request.FeeLamports == 0 || request.FeeLamports > policy.MaxFeeLamports ||
		!validFeeContext(request) {
		return ValidatedRequest{}, errors.New("Jupiter transaction fee context is outside policy")
	}
	if !validScheduleWindow(policy, request) {
		return ValidatedRequest{}, errors.New("Jupiter signing schedule window is outside policy")
	}
	actionID, err := jupiterswap.ComputeActionID(
		policy.ProfileFingerprint,
		request.ScheduleWindowStartUnix,
	)
	if err != nil || request.ActionID != actionID {
		return ValidatedRequest{}, errors.New("Jupiter signing action ID is outside policy")
	}
	if request.ObservedBlockHeight >= request.LastValidBlockHeight ||
		request.LastValidBlockHeight-request.ObservedBlockHeight > policy.MaxBlockHeightWindow {
		return ValidatedRequest{}, errors.New("Jupiter transaction validity window is outside policy")
	}

	message, tables, err := proposalcheck.ValidateCandidateMaterial(
		*policy.Jupiter,
		*request.JupiterCandidate,
	)
	if err != nil {
		return ValidatedRequest{}, err
	}
	canonical, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
	if err != nil || base64.StdEncoding.EncodeToString(canonical) != request.MessageBase64 ||
		!bytes.Equal(canonical, message) {
		return ValidatedRequest{}, errors.New("Jupiter signing message is not canonical")
	}
	intent, err := jupiterswap.ValidateV0Message(
		*policy.Jupiter,
		request.JupiterCandidate.Request,
		request.JupiterCandidate.Quote,
		message,
		tables,
	)
	if err != nil || intent.RecentBlockhash != request.RecentBlockhash ||
		intent.InputAmount == 0 {
		return ValidatedRequest{}, errors.New("Jupiter signing message is outside policy")
	}
	digest := sha256.Sum256(message)
	validated, err := jupiterValidatedAmounts(policy, request, intent)
	if err != nil {
		return ValidatedRequest{}, err
	}
	validated.Message = message
	validated.MessageSHA256 = hex.EncodeToString(digest[:])
	return validated, nil
}

func jupiterValidatedAmounts(
	policy Policy,
	request Request,
	intent jupiterswap.MessageIntent,
) (ValidatedRequest, error) {
	if policy.Jupiter == nil || intent.InputAmount == 0 || request.FeeLamports == 0 {
		return ValidatedRequest{}, errors.New("Jupiter transaction amounts are invalid")
	}
	rent := policy.Jupiter.MaxTokenAccountRentLamports
	amountLamports := uint64(0)
	nativeDebit := request.FeeLamports
	if policy.Jupiter.NativeInput() {
		if intent.InputAmount > policy.MaxLamports || policy.MaxLamports == 0 {
			return ValidatedRequest{}, errors.New("Jupiter native input exceeds signer policy")
		}
		amountLamports = intent.InputAmount
		if intent.InputAmount > ^uint64(0)-nativeDebit {
			return ValidatedRequest{}, errors.New("Jupiter transaction debit overflows")
		}
		nativeDebit += intent.InputAmount
		if intent.OutputAccountCreated {
			if rent > ^uint64(0)-policy.Jupiter.MaxTokenAccountRentLamports {
				return ValidatedRequest{}, errors.New("Jupiter transaction debit overflows")
			}
			rent += policy.Jupiter.MaxTokenAccountRentLamports
		}
	} else {
		if !policy.Jupiter.NativeOutput() || intent.OutputAccountCreated ||
			intent.InputAmount > policy.MaxInputTokenAmount || policy.MaxInputTokenAmount == 0 {
			return ValidatedRequest{}, errors.New("Jupiter token input exceeds signer policy")
		}
	}
	if nativeDebit > ^uint64(0)-rent {
		return ValidatedRequest{}, errors.New("Jupiter transaction debit overflows")
	}
	return ValidatedRequest{
		AmountLamports:        amountLamports,
		DebitLamports:         nativeDebit + rent,
		InputMint:             request.JupiterCandidate.Request.InputMint,
		OutputMint:            request.JupiterCandidate.Request.OutputMint,
		InputAmount:           intent.InputAmount,
		MinimumOutput:         intent.MinimumOutput,
		NativeDebitLamports:   nativeDebit,
		TemporaryRentLamports: rent,
	}, nil
}

// RequestFromJupiterRecheck converts one exact read-only recheck into an
// unsigned signer request. It neither grants authority nor signs anything.
func RequestFromJupiterRecheck(
	policy Policy,
	expectedProviders proposalcheck.ProviderBindings,
	candidate proposalcheck.Candidate,
	checked proposalcheck.Result,
	scheduleWindowStartUnix int64,
) (Request, error) {
	if err := ValidateJupiterPolicy(policy); err != nil {
		return Request{}, err
	}
	if err := expectedProviders.ValidateArchiveProbe(); err != nil {
		return Request{}, err
	}
	window := int64(policy.ScheduleWindowSeconds)
	if scheduleWindowStartUnix < policy.ScheduleAnchorUnix ||
		(scheduleWindowStartUnix-policy.ScheduleAnchorUnix)%window != 0 ||
		scheduleWindowStartUnix+window <= scheduleWindowStartUnix {
		return Request{}, errors.New("Jupiter signing schedule window is outside policy")
	}
	message, tables, err := proposalcheck.ValidateCandidateMaterial(*policy.Jupiter, candidate)
	if err != nil {
		return Request{}, err
	}
	intent, err := jupiterswap.ValidateV0Message(
		*policy.Jupiter, candidate.Request, candidate.Quote, message, tables,
	)
	if err != nil {
		return Request{}, errors.New("Jupiter recheck message is outside policy")
	}
	fingerprint, err := policy.Jupiter.Fingerprint()
	if err != nil {
		return Request{}, err
	}
	digest := sha256.Sum256(message)
	if checked.Status != proposalcheck.StatusCheckedNotAuthorized ||
		checked.Reason != proposalcheck.ReasonSigningPolicyAbsent ||
		checked.Cluster != policy.Cluster || checked.PolicySHA256 != fingerprint ||
		checked.InputMint != candidate.Request.InputMint ||
		checked.OutputMint != candidate.Request.OutputMint ||
		checked.InputAmount != candidate.Request.InputAmount ||
		checked.EstimatedOutput != candidate.Quote.EstimatedOutput ||
		checked.MinimumOutput != candidate.Quote.MinimumOutput ||
		checked.MessageSHA256 != hex.EncodeToString(digest[:]) ||
		checked.FeeLamports == 0 || checked.FeeMinContextSlot == 0 ||
		checked.FeeMinContextSlot != checked.MinimumContextSlot ||
		checked.PrimaryFeeContextSlot < checked.FeeMinContextSlot ||
		checked.SecondaryFeeContextSlot < checked.FeeMinContextSlot ||
		checked.LastValidBlockHeight != candidate.LastValidBlockHeight ||
		checked.ObservedBlockHeight == 0 || checked.SigningEnabled || checked.SubmissionEnabled ||
		checked.PrimaryTrustDomain != expectedProviders.PrimaryTrustDomain ||
		checked.PrimaryOriginSHA256 != expectedProviders.PrimaryOriginSHA256 ||
		checked.SecondaryTrustDomain != expectedProviders.SecondaryTrustDomain ||
		checked.SecondaryOriginSHA256 != expectedProviders.SecondaryOriginSHA256 ||
		checked.ArchiveProbeSignature != expectedProviders.ArchiveProbeSignature {
		return Request{}, errors.New("Jupiter recheck result does not match candidate")
	}
	actionID, err := jupiterswap.ComputeActionID(fingerprint, scheduleWindowStartUnix)
	if err != nil {
		return Request{}, err
	}
	detached := candidate
	detached.AddressTables = append(
		[]jupiterswap.AddressTableEvidence(nil), candidate.AddressTables...,
	)
	request := Request{
		Domain: jupiterswap.RequestDomain, Cluster: policy.Cluster,
		Profile: policy.Profile, ProfileVersion: policy.ProfileVersion,
		ProfileFingerprint: fingerprint, ActionID: actionID,
		ScheduleWindowStartUnix: scheduleWindowStartUnix,
		ScheduleWindowEndUnix:   scheduleWindowStartUnix + window,
		MessageBase64:           candidate.MessageBase64,
		BlockhashContextSlot:    checked.MinimumContextSlot,
		FeeLamports:             checked.FeeLamports,
		FeeMinContextSlot:       checked.FeeMinContextSlot,
		PrimaryFeeContextSlot:   checked.PrimaryFeeContextSlot,
		SecondaryFeeContextSlot: checked.SecondaryFeeContextSlot,
		RecentBlockhash:         intent.RecentBlockhash,
		ObservedBlockHeight:     checked.ObservedBlockHeight,
		LastValidBlockHeight:    checked.LastValidBlockHeight,
		JupiterCandidate:        &detached,
		JupiterProviders:        &expectedProviders,
	}
	validated, err := ValidateJupiterRequest(policy, request)
	if err != nil {
		return Request{}, err
	}
	if checked.TokenAccountRent == 0 ||
		checked.TokenAccountRent > policy.Jupiter.MaxTokenAccountRentLamports ||
		(intent.OutputAccountCreated && checked.OutputAccountRent != checked.TokenAccountRent) ||
		(!intent.OutputAccountCreated && checked.OutputAccountRent != 0) {
		return Request{}, errors.New("Jupiter recheck debit is invalid")
	}
	maximumDebit := checked.FeeLamports
	if policy.Jupiter.NativeInput() {
		if checked.InputAmount > ^uint64(0)-maximumDebit ||
			checked.InputAmount+maximumDebit > ^uint64(0)-checked.OutputAccountRent {
			return Request{}, errors.New("Jupiter recheck debit is invalid")
		}
		maximumDebit += checked.InputAmount + checked.OutputAccountRent
	} else if !policy.Jupiter.NativeOutput() || checked.OutputAccountRent != 0 {
		return Request{}, errors.New("Jupiter recheck debit is invalid")
	}
	if maximumDebit > ^uint64(0)-checked.TokenAccountRent ||
		checked.MaximumDebitLamports != maximumDebit ||
		checked.MaximumUpfrontLamports != maximumDebit+checked.TokenAccountRent ||
		checked.MaximumUpfrontLamports > validated.DebitLamports {
		return Request{}, errors.New("Jupiter recheck debit exceeds signer policy")
	}
	return request, nil
}

// ValidateJupiterPolicy verifies the complete signer-owned Mainnet envelope.
// Validation alone neither signs nor submits a transaction.
func ValidateJupiterPolicy(policy Policy) error {
	if policy.Cluster != "mainnet-beta" || policy.Profile != jupiterswap.ProfileName ||
		policy.ProfileVersion != jupiterswap.ProfileVersion || policy.Jupiter == nil ||
		policy.OrcaSwap != nil || policy.OrcaBuy != nil || policy.Destination != "" ||
		policy.MaxFeeLamports == 0 {
		return errors.New("Jupiter signer policy is incomplete")
	}
	if err := policy.Jupiter.Validate(); err != nil || policy.Source != policy.Jupiter.Owner ||
		!jupiterAmountPolicyValid(policy) ||
		policy.MaxFeeLamports > policy.Jupiter.MaxFeeLamports {
		return errors.New("Jupiter signer limits do not match protected policy")
	}
	fingerprint, err := policy.Jupiter.Fingerprint()
	if err != nil || policy.ProfileFingerprint != fingerprint {
		return errors.New("Jupiter signer profile fingerprint is invalid")
	}
	if policy.ScheduleWindowSeconds < 60 || policy.ScheduleWindowSeconds > 86_400 ||
		86_400%policy.ScheduleWindowSeconds != 0 || policy.ScheduleAnchorUnix <= 0 ||
		policy.ScheduleAnchorUnix%86_400 != 0 || policy.MaxBlockHeightWindow == 0 ||
		policy.MaxBlockHeightWindow > 150 {
		return errors.New("Jupiter signer schedule or lifetime policy is invalid")
	}
	if policy.AuthorizationLedgerPath == "" ||
		!filepath.IsAbs(policy.AuthorizationLedgerPath) ||
		filepath.Clean(policy.AuthorizationLedgerPath) != policy.AuthorizationLedgerPath {
		return errors.New("Jupiter signer authorization ledger path is invalid")
	}
	if policy.RiskAuthorityKeyID == "" {
		return errors.New("risk authority key ID is required")
	}
	riskAuthority, err := riskgrant.DecodePublicKey(policy.RiskAuthorityPublicKey)
	if err != nil {
		return err
	}
	attestor, attestorErr := solana.Decode32(policy.AttestationPublicKey)
	source, sourceErr := solana.Decode32(policy.Source)
	if err := sealedtx.ValidatePublicKey(policy.SubmitterPublicKey); err != nil {
		return err
	}
	if attestorErr != nil || sourceErr != nil || attestor == source ||
		bytes.Equal(source[:], riskAuthority) ||
		bytes.Equal(attestor[:], riskAuthority) ||
		policy.SubmitterPublicKey == hex.EncodeToString(source[:]) ||
		policy.SubmitterPublicKey == policy.RiskAuthorityPublicKey ||
		policy.SubmitterPublicKey == hex.EncodeToString(attestor[:]) ||
		solana.Encode(attestor[:]) != policy.AttestationPublicKey {
		return errors.New("Jupiter wallet, risk authority, attestation, and submitter identities must differ")
	}
	return nil
}

func jupiterAmountPolicyValid(policy Policy) bool {
	if policy.Jupiter == nil {
		return false
	}
	if policy.Jupiter.NativeInput() {
		if policy.MaxLamports > ^uint64(0)-policy.MaxFeeLamports ||
			policy.MaxLamports+policy.MaxFeeLamports >
				^uint64(0)-policy.Jupiter.MaxTokenAccountRentLamports {
			return false
		}
		minimumDailyDebit := policy.MaxLamports + policy.MaxFeeLamports +
			policy.Jupiter.MaxTokenAccountRentLamports
		return policy.MaxLamports > 0 &&
			policy.MaxLamports <= policy.Jupiter.MaxInputAmount &&
			policy.DailyDebitCapLamports >= minimumDailyDebit &&
			policy.MaxInputTokenAmount == 0 && policy.DailyInputTokenCap == 0 &&
			policy.DailyNativeFeeCapLamports == 0
	}
	return policy.Jupiter.NativeOutput() && policy.MaxLamports == 0 &&
		policy.DailyDebitCapLamports == 0 && policy.MaxInputTokenAmount > 0 &&
		policy.MaxInputTokenAmount <= policy.Jupiter.MaxInputAmount &&
		policy.DailyInputTokenCap >= policy.MaxInputTokenAmount &&
		policy.DailyNativeFeeCapLamports > 0
}
