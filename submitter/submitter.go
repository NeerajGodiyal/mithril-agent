package submitter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const maxKeyBytes = 1024

const (
	MainnetRecoveryStopOnly   = "stop_only"
	MainnetRecoveryExactRetry = "exact_retry"
)

var ErrControlBlocked = errors.New("submitter control state blocks the transaction")

type Policy struct {
	Cluster               string                         `json:"cluster"`
	Profile               string                         `json:"profile,omitempty"`
	ProfileFingerprint    string                         `json:"profile_sha256"`
	ControlStatePath      string                         `json:"control_state_path"`
	Source                string                         `json:"source"`
	Destination           string                         `json:"destination"`
	MaxLamports           uint64                         `json:"max_lamports"`
	MaxInputTokenAmount   uint64                         `json:"max_input_token_amount,omitempty"`
	MaxFeeLamports        uint64                         `json:"max_fee_lamports"`
	ScheduleWindowSeconds uint64                         `json:"schedule_window_seconds,omitempty"`
	ScheduleAnchorUnix    int64                          `json:"schedule_anchor_unix,omitempty"`
	MaxBlockHeightWindow  uint64                         `json:"max_block_height_window,omitempty"`
	RecoveryMode          string                         `json:"recovery_mode,omitempty"`
	SubmitterPublicKey    string                         `json:"submitter_public_key"`
	AttestationPublicKey  string                         `json:"attestation_public_key,omitempty"`
	Evidence              proposalcheck.ProviderBindings `json:"evidence"`
	OrcaSwap              *orcaswap.Policy               `json:"orca_swap,omitempty"`
	OrcaBuy               *orcaswap.BuyPolicyV2          `json:"orca_buy,omitempty"`
	Jupiter               *jupiterswap.Policy            `json:"jupiter,omitempty"`
}

type KeyDocument struct {
	Version    uint32 `json:"version"`
	PrivateKey string `json:"private_key"`
}

type Node interface {
	GenesisHash(context.Context) (string, error)
	BlockHeight(context.Context) (uint64, error)
	SendTransaction(context.Context, []byte, uint64) (string, error)
}

type SendGate interface {
	WithRecoverySendBarrier(string, func() error) (bool, error)
}

func (p Policy) Validate() error {
	if p.Cluster != "devnet" || p.Jupiter != nil || p.ScheduleWindowSeconds != 0 ||
		p.ScheduleAnchorUnix != 0 || p.MaxBlockHeightWindow != 0 ||
		p.RecoveryMode != "" || p.AttestationPublicKey != "" {
		return errors.New("submitter policy is restricted to devnet")
	}
	fingerprint, err := hex.DecodeString(p.ProfileFingerprint)
	if err != nil || len(fingerprint) != sha256.Size ||
		hex.EncodeToString(fingerprint) != p.ProfileFingerprint {
		return errors.New("submitter profile fingerprint is invalid")
	}
	if !filepath.IsAbs(p.ControlStatePath) || filepath.Clean(p.ControlStatePath) != p.ControlStatePath {
		return errors.New("submitter control state path is invalid")
	}
	source, err := solana.Decode32(p.Source)
	if err != nil {
		return errors.New("submitter source is invalid")
	}
	if p.OrcaSwap == nil && p.OrcaBuy == nil {
		destination, err := solana.Decode32(p.Destination)
		if err != nil || source == destination || p.MaxInputTokenAmount != 0 ||
			(p.Profile != "" && p.Profile != "treasury_sweep_v1") {
			return errors.New("submitter destination is invalid")
		}
	} else if p.OrcaSwap != nil {
		if p.Profile != orcaswap.ProfileName || p.Destination != "" ||
			p.MaxInputTokenAmount != 0 ||
			p.OrcaBuy != nil || p.OrcaSwap.Validate() != nil || p.Source != p.OrcaSwap.Owner ||
			p.MaxLamports > p.OrcaSwap.MaxInputLamports {
			return errors.New("submitter Orca route is invalid")
		}
	} else {
		if p.Profile != orcaswap.BuyProfileName || p.Destination != "" ||
			p.MaxLamports != 0 || p.MaxInputTokenAmount == 0 ||
			p.OrcaBuy.Validate() != nil || p.Source != p.OrcaBuy.Owner ||
			p.MaxInputTokenAmount > p.OrcaBuy.MaxInputTokenAmount {
			return errors.New("submitter Orca buy route is invalid")
		}
	}
	if (p.OrcaBuy == nil && p.MaxLamports == 0) || p.MaxFeeLamports == 0 {
		return errors.New("submitter amount and fee limits are required")
	}
	if err := sealedtx.ValidatePublicKey(p.SubmitterPublicKey); err != nil {
		return err
	}
	return nil
}

func LoadPrivateKey(path string) (string, error) {
	data, err := securefile.ReadPrivate(path, maxKeyBytes)
	if err != nil {
		return "", err
	}
	defer clear(data)
	var document KeyDocument
	if err := strictjson.Decode(data, &document); err != nil || document.Version != 1 {
		return "", errors.New("submitter key document is invalid")
	}
	if _, err := sealedtx.PublicKey(document.PrivateKey); err != nil {
		return "", err
	}
	return document.PrivateKey, nil
}

// Submit opens the exact control gate named by the protected policy before
// validating and broadcasting one Devnet transaction.
func Submit(
	ctx context.Context,
	policy Policy,
	privateKey string,
	node Node,
	response signer.Response,
	minContextSlot uint64,
) (txflow.Submission, error) {
	if err := policy.Validate(); err != nil {
		return txflow.Submission{}, err
	}
	gate, err := control.NewStateFile(
		policy.ControlStatePath, policy.ProfileFingerprint, false,
	)
	if err != nil {
		return txflow.Submission{}, errors.New("submitter control configuration is invalid")
	}
	return submitWithGate(ctx, policy, privateKey, node, gate, response, minContextSlot)
}

func submitWithGate(
	ctx context.Context,
	policy Policy,
	privateKey string,
	node Node,
	gate SendGate,
	response signer.Response,
	minContextSlot uint64,
) (txflow.Submission, error) {
	if err := policy.Validate(); err != nil {
		return txflow.Submission{}, err
	}
	if err := policy.Evidence.Validate(); err != nil {
		return txflow.Submission{}, errors.New("submitter evidence providers are not bound")
	}
	if node == nil || gate == nil || minContextSlot == 0 {
		return txflow.Submission{}, errors.New("submitter node, control gate, and minimum context slot are required")
	}
	if response.BlockhashContextSlot != minContextSlot {
		return txflow.Submission{}, errors.New("signer response has the wrong minimum context slot")
	}
	publicKey, err := sealedtx.PublicKey(privateKey)
	if err != nil || publicKey != policy.SubmitterPublicKey {
		return txflow.Submission{}, errors.New("submitter key does not match policy")
	}
	transaction, err := sealedtx.Open(privateKey, response.SealedTransaction)
	if err != nil {
		return txflow.Submission{}, err
	}
	decoded, err := decodeTransaction(policy, transaction)
	if err != nil {
		return txflow.Submission{}, err
	}
	message := decoded.message
	signature := decoded.signature
	messageHash := sha256.Sum256(message)
	transactionHash := sha256.Sum256(transaction)
	expectedMetadata := responseMetadata(response)
	if response.SealedTransaction.Metadata != expectedMetadata ||
		response.Signature != signature ||
		response.MessageSHA256 != hex.EncodeToString(messageHash[:]) ||
		response.TransactionSHA256 != hex.EncodeToString(transactionHash[:]) ||
		response.FeeLamports == 0 || response.FeeLamports > policy.MaxFeeLamports ||
		response.LastValidBlockHeight == 0 {
		return txflow.Submission{}, errors.New("sealed signer response is invalid")
	}
	if err := signer.VerifyResponseAttestation(
		policy.Source, policy.SubmitterPublicKey, response,
	); err != nil {
		return txflow.Submission{}, err
	}
	var returned string
	var sendErr error
	var validationErr error
	blocked, err := gate.WithRecoverySendBarrier(response.ActionID, func() error {
		genesis, queryErr := node.GenesisHash(ctx)
		if queryErr != nil || genesis != solana.DevnetGenesisHash {
			validationErr = errors.New("submitter node does not match Devnet")
			return validationErr
		}
		height, queryErr := node.BlockHeight(ctx)
		if queryErr != nil || height == 0 {
			validationErr = errors.New("submitter block height is unavailable")
			return validationErr
		}
		if height >= response.LastValidBlockHeight {
			validationErr = errors.New("transaction has insufficient block-height headroom for submission")
			return validationErr
		}
		if prepareErr := prepareRecovery(policy, response, transaction); prepareErr != nil {
			validationErr = errors.New("persist exact submission recovery evidence")
			return validationErr
		}
		returned, sendErr = node.SendTransaction(ctx, transaction, response.BlockhashContextSlot)
		return nil
	})
	if err != nil {
		if validationErr != nil {
			return txflow.Submission{}, validationErr
		}
		return txflow.Submission{}, errors.New("inspect submitter control state")
	}
	if blocked {
		return txflow.Submission{}, ErrControlBlocked
	}
	state := txflow.StateAccepted
	if sendErr != nil || returned != response.Signature {
		state = txflow.StateAmbiguous
	}
	return txflow.Submission{
		Signature:            response.Signature,
		LastValidBlockHeight: response.LastValidBlockHeight,
		State:                state,
	}, nil
}

func responseMetadata(response signer.Response) sealedtx.Metadata {
	return sealedtx.Metadata{
		Version: sealedtx.Version, Domain: sealedtx.Domain,
		ActionID: response.ActionID, MessageSHA256: response.MessageSHA256,
		TransactionSHA256: response.TransactionSHA256, Signature: response.Signature,
		BlockhashContextSlot: response.BlockhashContextSlot,
		FeeLamports:          response.FeeLamports,
		LastValidBlockHeight: response.LastValidBlockHeight,
	}
}
