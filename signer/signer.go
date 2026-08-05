package signer

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/independentdecode"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	TransferRequestDomain = "mithril-agent/devnet-system-transfer-v1"
	RequestDomain         = TransferRequestDomain
	maxKeypairBytes       = 1024
)

type Policy struct {
	Cluster                   string                `json:"cluster"`
	Profile                   string                `json:"profile"`
	ProfileVersion            uint32                `json:"profile_version"`
	ProfileFingerprint        string                `json:"profile_sha256"`
	Source                    string                `json:"source"`
	Destination               string                `json:"destination"`
	MaxLamports               uint64                `json:"max_lamports"`
	MaxInputTokenAmount       uint64                `json:"max_input_token_amount,omitempty"`
	MaxFeeLamports            uint64                `json:"max_fee_lamports"`
	DailyDebitCapLamports     uint64                `json:"daily_debit_cap_lamports"`
	DailyInputTokenCap        uint64                `json:"daily_input_token_cap,omitempty"`
	DailyNativeFeeCapLamports uint64                `json:"daily_native_fee_cap_lamports,omitempty"`
	AuthorizationLedgerPath   string                `json:"authorization_ledger_path"`
	ScheduleWindowSeconds     uint64                `json:"schedule_window_seconds"`
	ScheduleAnchorUnix        int64                 `json:"schedule_anchor_unix"`
	MaxBlockHeightWindow      uint64                `json:"max_block_height_window"`
	RiskAuthorityKeyID        string                `json:"risk_authority_key_id"`
	RiskAuthorityPublicKey    string                `json:"risk_authority_public_key"`
	SubmitterPublicKey        string                `json:"submitter_public_key"`
	OrcaSwap                  *orcaswap.Policy      `json:"orca_swap,omitempty"`
	OrcaBuy                   *orcaswap.BuyPolicyV2 `json:"orca_buy,omitempty"`
}

type Request struct {
	Domain                  string          `json:"domain"`
	Cluster                 string          `json:"cluster"`
	Profile                 string          `json:"profile"`
	ProfileVersion          uint32          `json:"profile_version"`
	ProfileFingerprint      string          `json:"profile_sha256"`
	ActionID                string          `json:"action_id"`
	ScheduleWindowStartUnix int64           `json:"schedule_window_start_unix"`
	ScheduleWindowEndUnix   int64           `json:"schedule_window_end_unix"`
	MessageBase64           string          `json:"message_base64"`
	BlockhashContextSlot    uint64          `json:"blockhash_context_slot"`
	FeeLamports             uint64          `json:"fee_lamports"`
	FeeMinContextSlot       uint64          `json:"fee_min_context_slot"`
	PrimaryFeeContextSlot   uint64          `json:"primary_fee_context_slot"`
	SecondaryFeeContextSlot uint64          `json:"secondary_fee_context_slot"`
	RecentBlockhash         string          `json:"recent_blockhash"`
	ObservedBlockHeight     uint64          `json:"observed_block_height"`
	LastValidBlockHeight    uint64          `json:"last_valid_block_height"`
	RiskGrant               riskgrant.Grant `json:"risk_grant"`
}

type ValidatedRequest struct {
	Message               []byte
	MessageSHA256         string
	AmountLamports        uint64
	DebitLamports         uint64
	InputMint             string
	OutputMint            string
	InputAmount           uint64
	MinimumOutput         uint64
	NativeDebitLamports   uint64
	TemporaryRentLamports uint64
}

type Response struct {
	ActionID             string              `json:"action_id"`
	Signature            string              `json:"signature"`
	MessageSHA256        string              `json:"message_sha256"`
	TransactionSHA256    string              `json:"transaction_sha256"`
	SealedTransaction    sealedtx.Envelope   `json:"sealed_transaction"`
	BlockhashContextSlot uint64              `json:"blockhash_context_slot"`
	FeeLamports          uint64              `json:"fee_lamports"`
	LastValidBlockHeight uint64              `json:"last_valid_block_height"`
	SignerAttestation    ResponseAttestation `json:"signer_attestation"`
}

func (p Policy) Validate() error {
	if p.Cluster != "devnet" {
		return errors.New("signer policy is restricted to devnet")
	}
	isTransfer := p.Profile == agent.ProfileTreasurySweepV1 && p.ProfileVersion == 1
	isSell := p.Profile == orcaswap.ProfileName && p.ProfileVersion == orcaswap.ProfileVersion
	isBuy := p.Profile == orcaswap.BuyProfileName && p.ProfileVersion == orcaswap.BuyProfileVersion
	if !isTransfer && !isSell && !isBuy {
		return errors.New("signer policy has an unsupported profile")
	}
	fingerprint, err := hex.DecodeString(p.ProfileFingerprint)
	if err != nil || len(fingerprint) != sha256.Size ||
		hex.EncodeToString(fingerprint) != p.ProfileFingerprint {
		return errors.New("signer profile fingerprint is invalid")
	}
	source, err := solana.Decode32(p.Source)
	if err != nil {
		return fmt.Errorf("policy source: %w", err)
	}
	if isTransfer {
		destination, err := solana.Decode32(p.Destination)
		if err != nil {
			return fmt.Errorf("policy destination: %w", err)
		}
		if source == destination || p.OrcaSwap != nil || p.OrcaBuy != nil ||
			p.MaxInputTokenAmount != 0 || p.DailyInputTokenCap != 0 ||
			p.DailyNativeFeeCapLamports != 0 {
			return errors.New("policy source and destination must differ")
		}
	} else if isSell {
		if p.Destination != "" || p.OrcaSwap == nil || p.OrcaBuy != nil ||
			p.MaxInputTokenAmount != 0 || p.DailyInputTokenCap != 0 ||
			p.DailyNativeFeeCapLamports != 0 {
			return errors.New("Orca swap signer policy is incomplete")
		}
		if err := p.OrcaSwap.Validate(); err != nil {
			return err
		}
		if p.Source != p.OrcaSwap.Owner || p.MaxLamports > p.OrcaSwap.MaxInputLamports {
			return errors.New("Orca swap signer limits do not match the route policy")
		}
	} else {
		if p.Destination != "" || p.OrcaSwap != nil || p.OrcaBuy == nil ||
			p.MaxLamports != 0 || p.DailyDebitCapLamports != 0 ||
			p.MaxInputTokenAmount == 0 {
			return errors.New("Orca buy signer policy is incomplete")
		}
		if err := p.OrcaBuy.Validate(); err != nil {
			return err
		}
		if p.Source != p.OrcaBuy.Owner ||
			p.MaxInputTokenAmount > p.OrcaBuy.MaxInputTokenAmount {
			return errors.New("Orca buy signer limits do not match the route policy")
		}
	}
	if !isBuy && p.MaxLamports == 0 {
		return errors.New("signer maximum must be positive")
	}
	if p.MaxFeeLamports == 0 {
		return errors.New("signer maximum fee must be positive")
	}
	if p.ScheduleWindowSeconds < 60 || p.ScheduleWindowSeconds > 86_400 ||
		86_400%p.ScheduleWindowSeconds != 0 ||
		p.ScheduleAnchorUnix <= 0 || p.ScheduleAnchorUnix%86_400 != 0 {
		return errors.New("signer schedule policy is invalid")
	}
	if p.MaxBlockHeightWindow == 0 || p.MaxBlockHeightWindow > 300 {
		return errors.New("block-height window must be between 1 and 300")
	}
	if p.RiskAuthorityKeyID == "" {
		return errors.New("risk authority key ID is required")
	}
	if _, err := riskgrant.DecodePublicKey(p.RiskAuthorityPublicKey); err != nil {
		return err
	}
	submitterKey, err := hex.DecodeString(p.SubmitterPublicKey)
	if err != nil || len(submitterKey) != 32 ||
		hex.EncodeToString(submitterKey) != p.SubmitterPublicKey {
		return errors.New("submitter public key is invalid")
	}
	return nil
}

func (p Policy) validateAuthorization() error {
	return p.ValidateAuthorizationPolicy()
}

// ValidateAuthorizationPolicy verifies the signer policy and its durable daily limits.
func (p Policy) ValidateAuthorizationPolicy() error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Profile == orcaswap.BuyProfileName {
		if p.DailyDebitCapLamports != 0 || p.DailyInputTokenCap == 0 ||
			p.DailyInputTokenCap < p.MaxInputTokenAmount || p.DailyNativeFeeCapLamports == 0 {
			return errors.New("buy signer daily limits are invalid")
		}
	} else if p.DailyDebitCapLamports == 0 {
		return errors.New("signer daily debit cap must be positive")
	}
	if p.AuthorizationLedgerPath == "" ||
		!filepath.IsAbs(p.AuthorizationLedgerPath) ||
		filepath.Clean(p.AuthorizationLedgerPath) != p.AuthorizationLedgerPath {
		return errors.New("signer authorization ledger path must be a clean absolute path")
	}
	return nil
}

func LoadKeypair(path string) (privateKey ed25519.PrivateKey, resultErr error) {
	data, err := securefile.ReadPrivate(path, maxKeypairBytes)
	if err != nil {
		return nil, err
	}
	defer clear(data)
	var values []uint16
	// A bare `defer clear(values)` would capture the nil slice header as it is
	// NOW; Unmarshal allocates the backing array afterwards, so the key bytes
	// would survive in the heap. The closure re-reads the variable at return.
	defer func() { clear(values) }()
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, errors.New("keypair must be a JSON byte array")
	}
	if len(values) != ed25519.PrivateKeySize {
		return nil, errors.New("keypair must contain exactly 64 bytes")
	}
	privateKey = make(ed25519.PrivateKey, ed25519.PrivateKeySize)
	defer func() {
		if resultErr != nil {
			clear(privateKey)
		}
	}()
	for index, value := range values {
		if value > 255 {
			return nil, errors.New("keypair values must be bytes")
		}
		privateKey[index] = byte(value)
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || !bytes.Equal(publicKey, privateKey[ed25519.SeedSize:]) {
		return nil, errors.New("keypair public key does not match its seed")
	}
	return privateKey, nil
}

func PublicKey(privateKey ed25519.PrivateKey) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", errors.New("keypair must contain exactly 64 bytes")
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || !bytes.Equal(publicKey, privateKey[ed25519.SeedSize:]) {
		return "", errors.New("keypair public key does not match its seed")
	}
	return solana.Encode(publicKey), nil
}

func signAt(
	policy Policy,
	privateKey ed25519.PrivateKey,
	request Request,
	now time.Time,
) (Response, error) {
	validated, err := ValidateRequest(policy, request)
	if err != nil {
		return Response{}, err
	}
	publicKeyValue, err := PublicKey(privateKey)
	if err != nil || publicKeyValue != policy.Source {
		return Response{}, errors.New("signer key does not match policy")
	}
	publicKey, err := riskgrant.DecodePublicKey(policy.RiskAuthorityPublicKey)
	if err != nil {
		return Response{}, err
	}
	binding, err := RiskBinding(request, validated.MessageSHA256)
	if err != nil {
		return Response{}, err
	}
	if err := riskgrant.Verify(
		publicKey,
		policy.RiskAuthorityKeyID,
		binding,
		request.RiskGrant,
		now.UTC(),
	); err != nil {
		return Response{}, err
	}
	var transaction []byte
	var signature [64]byte
	if policy.Profile == orcaswap.ProfileName || policy.Profile == orcaswap.BuyProfileName {
		transaction, signature, err = solana.SignLegacyMessage(privateKey, validated.Message)
	} else {
		transaction, signature, err = solana.SignTransferMessage(privateKey, validated.Message)
	}
	if err != nil {
		return Response{}, err
	}
	transactionHash := sha256.Sum256(transaction)
	response := Response{
		ActionID:             request.ActionID,
		Signature:            solana.Encode(signature[:]),
		MessageSHA256:        validated.MessageSHA256,
		TransactionSHA256:    hex.EncodeToString(transactionHash[:]),
		BlockhashContextSlot: request.BlockhashContextSlot,
		FeeLamports:          request.FeeLamports,
		LastValidBlockHeight: request.LastValidBlockHeight,
	}
	response.SealedTransaction, err = sealedtx.Seal(
		policy.SubmitterPublicKey,
		sealedtx.Metadata{
			Version:              sealedtx.Version,
			Domain:               sealedtx.Domain,
			ActionID:             response.ActionID,
			MessageSHA256:        response.MessageSHA256,
			TransactionSHA256:    response.TransactionSHA256,
			Signature:            response.Signature,
			BlockhashContextSlot: response.BlockhashContextSlot,
			FeeLamports:          response.FeeLamports,
			LastValidBlockHeight: response.LastValidBlockHeight,
		},
		transaction,
		rand.Reader,
	)
	if err != nil {
		return Response{}, err
	}
	response.SignerAttestation, err = AttestResponse(
		privateKey, policy.SubmitterPublicKey, response,
	)
	if err != nil {
		return Response{}, err
	}
	if err := VerifyResponseAttestation(
		policy.Source, policy.SubmitterPublicKey, response,
	); err != nil {
		return Response{}, err
	}
	return response, nil
}

func ValidateRequest(policy Policy, request Request) (ValidatedRequest, error) {
	if err := policy.Validate(); err != nil {
		return ValidatedRequest{}, err
	}
	expectedDomain := TransferRequestDomain
	if policy.Profile == orcaswap.ProfileName {
		expectedDomain = orcaswap.RequestDomain
	} else if policy.Profile == orcaswap.BuyProfileName {
		expectedDomain = orcaswap.BuyRequestDomain
	}
	if request.Domain != expectedDomain ||
		request.Cluster != policy.Cluster ||
		request.Profile != policy.Profile ||
		request.ProfileVersion != policy.ProfileVersion ||
		request.ProfileFingerprint != policy.ProfileFingerprint {
		return ValidatedRequest{}, errors.New("signing request is outside policy")
	}
	if request.FeeLamports == 0 || request.FeeLamports > policy.MaxFeeLamports {
		return ValidatedRequest{}, errors.New("transaction fee is outside policy")
	}
	if request.BlockhashContextSlot == 0 || request.ObservedBlockHeight == 0 ||
		request.FeeMinContextSlot != request.BlockhashContextSlot ||
		request.PrimaryFeeContextSlot < request.FeeMinContextSlot ||
		request.SecondaryFeeContextSlot < request.FeeMinContextSlot {
		return ValidatedRequest{}, errors.New("transaction fee context is outside policy")
	}
	actionID, err := hex.DecodeString(request.ActionID)
	if err != nil || len(actionID) != sha256.Size {
		return ValidatedRequest{}, errors.New("action ID must be a 32-byte hex digest")
	}
	windowSeconds := int64(policy.ScheduleWindowSeconds)
	if request.ScheduleWindowStartUnix < policy.ScheduleAnchorUnix ||
		request.ScheduleWindowEndUnix-request.ScheduleWindowStartUnix != windowSeconds ||
		(request.ScheduleWindowStartUnix-policy.ScheduleAnchorUnix)%windowSeconds != 0 {
		return ValidatedRequest{}, errors.New("signing request schedule window is outside policy")
	}
	var expectedActionID string
	if policy.Profile == orcaswap.ProfileName {
		expectedActionID, err = orcaswap.ComputeActionID(
			policy.ProfileFingerprint,
			request.ScheduleWindowStartUnix,
		)
	} else if policy.Profile == orcaswap.BuyProfileName {
		expectedActionID, err = orcaswap.ComputeBuyActionID(
			policy.ProfileFingerprint,
			request.ScheduleWindowStartUnix,
		)
	} else {
		expectedActionID, err = agent.ComputeActionID(
			policy.ProfileFingerprint,
			request.ScheduleWindowStartUnix,
		)
	}
	if err != nil || request.ActionID != expectedActionID {
		return ValidatedRequest{}, errors.New("signing request action ID is outside policy")
	}
	if request.ObservedBlockHeight > request.LastValidBlockHeight {
		return ValidatedRequest{}, errors.New("transaction blockhash is already expired")
	}
	window := request.LastValidBlockHeight - request.ObservedBlockHeight
	if window == 0 || window > policy.MaxBlockHeightWindow {
		return ValidatedRequest{}, errors.New("transaction validity window is outside policy")
	}
	message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
	if err != nil {
		return ValidatedRequest{}, errors.New("message is not canonical base64")
	}
	amountLamports := uint64(0)
	extraDebitLamports := uint64(0)
	var buyIntent *orcaswap.BuyIntentV2
	if policy.Profile == orcaswap.ProfileName {
		intent, err := orcaswap.DecodeMessage(*policy.OrcaSwap, message)
		if err != nil {
			return ValidatedRequest{}, fmt.Errorf("decode Orca swap: %w", err)
		}
		if intent.Owner != policy.Source || intent.RecentBlockhash != request.RecentBlockhash {
			return ValidatedRequest{}, errors.New("decoded Orca swap does not match policy and request")
		}
		amountLamports = intent.InputAmount
		if intent.OutputAccountCreated {
			extraDebitLamports = policy.OrcaSwap.MaxOutputAccountRentLamports
		}
	} else if policy.Profile == orcaswap.BuyProfileName {
		intent, err := orcaswap.DecodeBuyMessageV2(*policy.OrcaBuy, message)
		if err != nil {
			return ValidatedRequest{}, fmt.Errorf("decode Orca buy: %w", err)
		}
		if intent.Owner != policy.Source || intent.RecentBlockhash != request.RecentBlockhash ||
			intent.InputAmount != policy.MaxInputTokenAmount {
			return ValidatedRequest{}, errors.New("decoded Orca buy does not match policy and request")
		}
		buyIntent = &intent
	} else {
		transfer, err := independentdecode.DecodeMessage(message)
		if err != nil {
			return ValidatedRequest{}, fmt.Errorf("decode transfer: %w", err)
		}
		if solana.Encode(transfer.Source[:]) != policy.Source ||
			solana.Encode(transfer.Destination[:]) != policy.Destination ||
			solana.Encode(transfer.RecentBlockhash[:]) != request.RecentBlockhash {
			return ValidatedRequest{}, errors.New("decoded transfer does not match policy and request")
		}
		amountLamports = transfer.Lamports
	}
	if buyIntent == nil {
		if amountLamports == 0 || amountLamports > policy.MaxLamports ||
			(policy.Profile == orcaswap.ProfileName && amountLamports != policy.MaxLamports) {
			return ValidatedRequest{}, errors.New("decoded transaction amount is outside policy")
		}
		if amountLamports > ^uint64(0)-request.FeeLamports ||
			amountLamports+request.FeeLamports > ^uint64(0)-extraDebitLamports {
			return ValidatedRequest{}, errors.New("transaction debit overflows")
		}
	}
	messageHash := sha256.Sum256(message)
	validated := ValidatedRequest{
		Message:        message,
		MessageSHA256:  hex.EncodeToString(messageHash[:]),
		AmountLamports: amountLamports,
		DebitLamports:  amountLamports + request.FeeLamports + extraDebitLamports,
	}
	if buyIntent != nil {
		validated.InputMint = buyIntent.InputMint
		validated.OutputMint = buyIntent.OutputMint
		validated.InputAmount = buyIntent.InputAmount
		validated.MinimumOutput = buyIntent.MinimumOutputLamports
		validated.NativeDebitLamports = request.FeeLamports
		validated.TemporaryRentLamports = buyIntent.TemporaryRentLamports
	}
	return validated, nil
}

func RiskBinding(request Request, messageSHA256 string) (riskgrant.Binding, error) {
	requestHash, err := immutableRequestHash(request)
	if err != nil {
		return riskgrant.Binding{}, err
	}
	return riskgrant.Binding{
		ActionID:             request.ActionID,
		ProfileFingerprint:   request.ProfileFingerprint,
		MessageSHA256:        messageSHA256,
		RequestSHA256:        requestHash,
		FeeLamports:          request.FeeLamports,
		LastValidBlockHeight: request.LastValidBlockHeight,
	}, nil
}
