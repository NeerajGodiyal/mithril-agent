package submitter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const maxKeyBytes = 1024

var ErrControlBlocked = errors.New("submitter control state blocks the transaction")

type Policy struct {
	Cluster             string                `json:"cluster"`
	Profile             string                `json:"profile,omitempty"`
	ProfileFingerprint  string                `json:"profile_sha256"`
	ControlStatePath    string                `json:"control_state_path"`
	Source              string                `json:"source"`
	Destination         string                `json:"destination"`
	MaxLamports         uint64                `json:"max_lamports"`
	MaxInputTokenAmount uint64                `json:"max_input_token_amount,omitempty"`
	MaxFeeLamports      uint64                `json:"max_fee_lamports"`
	SubmitterPublicKey  string                `json:"submitter_public_key"`
	OrcaSwap            *orcaswap.Policy      `json:"orca_swap,omitempty"`
	OrcaBuy             *orcaswap.BuyPolicyV2 `json:"orca_buy,omitempty"`
}

type KeyDocument struct {
	Version    uint32 `json:"version"`
	PrivateKey string `json:"private_key"`
}

type Node interface {
	SendTransaction(context.Context, []byte, uint64) (string, error)
}

type SendGate interface {
	WithRecoverySendBarrier(string, func() error) (bool, error)
}

func (p Policy) Validate() error {
	if p.Cluster != "devnet" {
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
	decoded, err := hex.DecodeString(p.SubmitterPublicKey)
	if err != nil || len(decoded) != 32 ||
		hex.EncodeToString(decoded) != p.SubmitterPublicKey {
		return errors.New("submitter public key is invalid")
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

func Submit(
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
	var message []byte
	var signature string
	if policy.OrcaSwap == nil && policy.OrcaBuy == nil {
		decoded, err := solana.DecodeSignedTransfer(transaction)
		if err != nil {
			return txflow.Submission{}, fmt.Errorf("decode sealed transaction: %w", err)
		}
		if solana.Encode(decoded.Source[:]) != policy.Source ||
			solana.Encode(decoded.Destination[:]) != policy.Destination ||
			decoded.Lamports == 0 || decoded.Lamports > policy.MaxLamports {
			return txflow.Submission{}, errors.New("sealed transaction is outside submitter policy")
		}
		message = decoded.Message
		signature = solana.Encode(decoded.Signature[:])
	} else if policy.OrcaSwap != nil {
		decoded, err := solana.DecodeSignedLegacyTransaction(transaction)
		if err != nil {
			return txflow.Submission{}, fmt.Errorf("decode sealed transaction: %w", err)
		}
		intent, err := orcaswap.DecodeMessage(*policy.OrcaSwap, decoded.Message.Raw)
		if err != nil || intent.Owner != policy.Source || intent.InputAmount != policy.MaxLamports {
			return txflow.Submission{}, errors.New("sealed Orca swap is outside submitter policy")
		}
		message = decoded.Message.Raw
		signature = solana.Encode(decoded.Signature[:])
	} else {
		decoded, err := solana.DecodeSignedLegacyTransaction(transaction)
		if err != nil {
			return txflow.Submission{}, fmt.Errorf("decode sealed transaction: %w", err)
		}
		intent, err := orcaswap.DecodeBuyMessageV2(*policy.OrcaBuy, decoded.Message.Raw)
		if err != nil || intent.Owner != policy.Source ||
			intent.InputAmount != policy.MaxInputTokenAmount {
			return txflow.Submission{}, errors.New("sealed Orca buy is outside submitter policy")
		}
		message = decoded.Message.Raw
		signature = solana.Encode(decoded.Signature[:])
	}
	messageHash := sha256.Sum256(message)
	transactionHash := sha256.Sum256(transaction)
	expectedMetadata := sealedtx.Metadata{
		Version:              sealedtx.Version,
		Domain:               sealedtx.Domain,
		ActionID:             response.ActionID,
		MessageSHA256:        response.MessageSHA256,
		TransactionSHA256:    response.TransactionSHA256,
		Signature:            response.Signature,
		BlockhashContextSlot: response.BlockhashContextSlot,
		FeeLamports:          response.FeeLamports,
		LastValidBlockHeight: response.LastValidBlockHeight,
	}
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
	blocked, err := gate.WithRecoverySendBarrier(response.ActionID, func() error {
		returned, sendErr = node.SendTransaction(ctx, transaction, response.BlockhashContextSlot)
		return nil
	})
	if err != nil {
		return txflow.Submission{}, errors.New("inspect submitter control state")
	}
	if blocked {
		return txflow.Submission{}, ErrControlBlocked
	}
	state := txflow.StateAccepted
	if sendErr != nil {
		state = txflow.StateAmbiguous
	} else if returned != response.Signature {
		return txflow.Submission{}, errors.New("RPC returned a different transaction signature")
	}
	return txflow.Submission{
		Signature:            response.Signature,
		LastValidBlockHeight: response.LastValidBlockHeight,
		State:                state,
	}, nil
}
