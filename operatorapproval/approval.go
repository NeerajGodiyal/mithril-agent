// Package operatorapproval binds one human-readable Solana wallet signature
// to one exact Mainnet signer request. It grants no signing or submission
// capability by itself; the separately protected risk authority verifies it
// before issuing its short-lived grant.
package operatorapproval

import (
	"errors"
	"fmt"

	"github.com/Overclock-Validator/mithril-agent/internal/offchainmsg"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	Version = uint32(1)
	Domain  = "mithril-agent/mainnet-exact-request-approval-v1"
)

// Approval is the detached wallet signature supplied to the risk authority.
// The request remains the source of every reviewed amount; duplicating them in
// this artifact would create a second representation that could drift.
type Approval struct {
	Version         uint32 `json:"version"`
	Domain          string `json:"domain"`
	Approver        string `json:"approver"`
	RequestSHA256   string `json:"request_sha256"`
	SignatureBase58 string `json:"signature_base58"`
}

// Review is safe to show to the operator before asking their wallet to sign.
type Review struct {
	Challenge                  string `json:"challenge"`
	Approver                   string `json:"approver"`
	ActionID                   string `json:"action_id"`
	InputMint                  string `json:"input_mint"`
	OutputMint                 string `json:"output_mint"`
	InputAmountBaseUnits       uint64 `json:"input_amount_base_units"`
	MinimumOutputBaseUnits     uint64 `json:"minimum_output_base_units"`
	MaximumNativeDebitLamports uint64 `json:"maximum_native_debit_lamports"`
	TransactionFeeLamports     uint64 `json:"transaction_fee_lamports"`
	ScheduleWindowStartUnix    int64  `json:"schedule_window_start_unix"`
	ScheduleWindowEndUnix      int64  `json:"schedule_window_end_unix"`
	LastValidBlockHeight       uint64 `json:"last_valid_block_height"`
	RequestSHA256              string `json:"request_sha256"`
	MessageSHA256              string `json:"message_sha256"`
}

// BuildReview derives one canonical printable challenge from the exact
// already-validated request. The request hash also binds provider evidence,
// blockhash context, lookup tables, and the complete message bytes.
func BuildReview(
	approver string,
	request signer.Request,
	validated signer.ValidatedRequest,
) (Review, error) {
	if request.Cluster != "mainnet-beta" || request.JupiterCandidate == nil ||
		validated.InputMint == "" || validated.OutputMint == "" ||
		validated.InputAmount == 0 || validated.MinimumOutput == 0 ||
		validated.DebitLamports == 0 || request.FeeLamports == 0 ||
		request.ScheduleWindowEndUnix <= request.ScheduleWindowStartUnix ||
		request.LastValidBlockHeight == 0 {
		return Review{}, errors.New("operator approval request is invalid")
	}
	if _, err := solana.Decode32(approver); err != nil || approver == request.JupiterCandidate.Policy.Owner {
		return Review{}, errors.New("operator approver is invalid")
	}
	binding, err := signer.RiskBinding(request, validated.MessageSHA256)
	if err != nil {
		return Review{}, err
	}
	challenge := fmt.Sprintf(
		"Mithril Agent Mainnet trade approval v1|approver:%s|action:%s|input:%d %s|minimum-output:%d %s|max-native-debit-lamports:%d|fee-lamports:%d|starts-unix:%d|expires-unix:%d|last-valid-block-height:%d|request-sha256:%s|message-sha256:%s",
		approver, request.ActionID, validated.InputAmount, validated.InputMint,
		validated.MinimumOutput, validated.OutputMint, validated.DebitLamports,
		request.FeeLamports, request.ScheduleWindowStartUnix, request.ScheduleWindowEndUnix,
		request.LastValidBlockHeight, binding.RequestSHA256, validated.MessageSHA256,
	)
	if _, err := offchainmsg.Envelope(challenge); err != nil {
		return Review{}, errors.New("operator approval challenge is invalid")
	}
	return Review{
		Challenge: challenge, Approver: approver, ActionID: request.ActionID,
		InputMint: validated.InputMint, OutputMint: validated.OutputMint,
		InputAmountBaseUnits:       validated.InputAmount,
		MinimumOutputBaseUnits:     validated.MinimumOutput,
		MaximumNativeDebitLamports: validated.DebitLamports,
		TransactionFeeLamports:     request.FeeLamports,
		ScheduleWindowStartUnix:    request.ScheduleWindowStartUnix,
		ScheduleWindowEndUnix:      request.ScheduleWindowEndUnix,
		LastValidBlockHeight:       request.LastValidBlockHeight,
		RequestSHA256:              binding.RequestSHA256, MessageSHA256: validated.MessageSHA256,
	}, nil
}

// Create verifies the supplied signature before returning a portable artifact.
func Create(
	approver string,
	request signer.Request,
	validated signer.ValidatedRequest,
	signatureBase58 string,
) (Approval, error) {
	review, err := BuildReview(approver, request, validated)
	if err != nil {
		return Approval{}, err
	}
	approval := Approval{
		Version: Version, Domain: Domain, Approver: approver,
		RequestSHA256: review.RequestSHA256, SignatureBase58: signatureBase58,
	}
	if err := Verify(approver, request, validated, approval); err != nil {
		return Approval{}, err
	}
	return approval, nil
}

// Verify proves that the expected operator wallet approved this exact request.
func Verify(
	expectedApprover string,
	request signer.Request,
	validated signer.ValidatedRequest,
	approval Approval,
) error {
	review, err := BuildReview(expectedApprover, request, validated)
	if err != nil {
		return err
	}
	if approval.Version != Version || approval.Domain != Domain ||
		approval.Approver != expectedApprover ||
		approval.RequestSHA256 != review.RequestSHA256 {
		return errors.New("operator approval does not match the exact request")
	}
	publicKey, err := solana.Decode32(expectedApprover)
	if err != nil {
		return errors.New("operator approver is invalid")
	}
	signature, err := solana.Decode64(approval.SignatureBase58)
	if err != nil {
		return errors.New("operator approval signature is invalid")
	}
	ok, err := offchainmsg.Verify(publicKey, review.Challenge, signature)
	if err != nil || !ok {
		return errors.New("operator approval signature does not verify")
	}
	return nil
}
