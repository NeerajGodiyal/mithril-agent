package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/operatorapproval"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/signer"
)

const proposalApprovalCreateUsage = `Usage: mithril-agent proposal approval-create [options]

Shows one exact Mainnet request, verifies approval from the separate operator
wallet, and writes a detached approval artifact. The wallet signs only the
displayed off-chain message. This command cannot sign or send a transaction.

  --request PATH           absolute private signer-request JSON path
  --authority-policy PATH  absolute protected authority-policy JSON path
  --out PATH               new absolute private approval JSON path
  --signature BASE58       optional pre-collected Solana message signature;
                           without it, a short-lived browser-wallet page opens`

type proposalApprovalCreateResult struct {
	Status            string                  `json:"status"`
	ApprovalPath      string                  `json:"approval_path"`
	Review            operatorapproval.Review `json:"review"`
	AuthorizationMade bool                    `json:"authorization_made"`
	TransactionSigned bool                    `json:"transaction_signed"`
	TransactionSent   bool                    `json:"transaction_sent"`
	ProductionEnabled bool                    `json:"production_enabled"`
}

func runProposalApprovalCreate(
	ctx context.Context,
	args []string,
	output io.Writer,
) error {
	flags := flag.NewFlagSet("proposal approval-create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	requestPath := flags.String("request", "", "private signer-request JSON path")
	policyPath := flags.String("authority-policy", "", "protected authority-policy JSON path")
	outPath := flags.String("out", "", "new private approval JSON path")
	signature := flags.String("signature", "", "pre-collected Solana message signature")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, proposalApprovalCreateUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || !distinctAbsolutePaths(*requestPath, *policyPath, *outPath) {
		return errors.New("proposal approval-create requires distinct absolute request, authority-policy, and new output paths")
	}
	if _, err := os.Lstat(*outPath); err == nil {
		return errors.New("operator approval output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("inspect operator approval output")
	}

	var policy policyauthority.Policy
	if err := readStrictJSON(*policyPath, &policy); err != nil || policy.Validate() != nil {
		return errors.New("read protected Mainnet authority policy")
	}
	raw, err := securefile.ReadPrivate(*requestPath, signer.MaxRequestBytes)
	if err != nil {
		return errors.New("read private signer request")
	}
	defer clear(raw)
	var request signer.Request
	if err := strictjson.Decode(raw, &request); err != nil {
		return errors.New("decode private signer request")
	}
	validated, err := signer.ValidateJupiterRequest(policy.TransactionPolicy, request)
	if err != nil {
		return err
	}
	defer clear(validated.Message)
	review, err := operatorapproval.BuildReview(policy.OperatorApprover, request, validated)
	if err != nil {
		return err
	}

	signatureText := *signature
	if signatureText == "" {
		signatureText, err = collectOperatorApprovalSignature(
			ctx, output, review, policy.OperatorApprover, request, validated,
		)
		if err != nil {
			return err
		}
	}
	approval, err := operatorapproval.Create(
		policy.OperatorApprover, request, validated, signatureText,
	)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(approval, "", "  ")
	if err != nil {
		return errors.New("encode operator approval")
	}
	if err := securefile.CreatePrivate(
		*outPath, append(encoded, '\n'), maxInputBytes,
	); err != nil {
		return errors.New("write private operator approval")
	}
	return json.NewEncoder(output).Encode(proposalApprovalCreateResult{
		Status:       "exact_request_approved_not_authorized",
		ApprovalPath: *outPath,
		Review:       review,
	})
}

func collectOperatorApprovalSignature(
	ctx context.Context,
	output io.Writer,
	review operatorapproval.Review,
	approver string,
	request signer.Request,
	validated signer.ValidatedRequest,
) (string, error) {
	collector, err := newSignatureCollector(review.Challenge, "", func(signature string) error {
		_, err := operatorapproval.Create(approver, request, validated, signature)
		return err
	})
	if err != nil {
		return "", err
	}
	session, err := collector.session()
	if err != nil {
		return "", err
	}
	host := hostnameForCopy()
	if _, err := fmt.Fprintf(output,
		"\nReview this exact Mainnet action before signing:\n"+
			"  Input: %d %s\n"+
			"  Minimum output: %d %s\n"+
			"  Maximum native debit: %d lamports\n"+
			"  Transaction fee: %d lamports\n"+
			"  Approval starts: %d\n"+
			"  Approval expires: %d\n\n"+
			"On YOUR Mac or Linux desktop, run:\n\n"+
			"    %s\n\n"+
			"Manual fallback:\n"+
			"    %s\n"+
			"    %s\n\n"+
			"The wallet signs only the displayed off-chain message; it cannot move funds.\n"+
			"This exact request is short-lived. Complete the remaining checks promptly; if it expires, prepare and approve a fresh request.\n"+
			"Waiting up to %s. Ctrl-C to stop.\n",
		review.InputAmountBaseUnits, review.InputMint,
		review.MinimumOutputBaseUnits, review.OutputMint,
		review.MaximumNativeDebitLamports, review.TransactionFeeLamports,
		review.ScheduleWindowStartUnix, review.ScheduleWindowEndUnix,
		walletVerifyInvocation(host, session), collector.tunnelCommand(host), collector.url(),
		signServeTimeout,
	); err != nil {
		return "", err
	}
	return collector.collect(ctx)
}

func verifyOperatorApprovalFiles(
	policy policyauthority.Policy,
	requestPath, approvalPath string,
) (operatorapproval.Review, error) {
	raw, err := securefile.ReadPrivate(requestPath, signer.MaxRequestBytes)
	if err != nil {
		return operatorapproval.Review{}, errors.New("read private approved signer request")
	}
	defer clear(raw)
	var request signer.Request
	if err := strictjson.Decode(raw, &request); err != nil {
		return operatorapproval.Review{}, errors.New("decode private approved signer request")
	}
	validated, err := signer.ValidateJupiterRequest(policy.TransactionPolicy, request)
	if err != nil {
		return operatorapproval.Review{}, err
	}
	defer clear(validated.Message)
	var approval operatorapproval.Approval
	if err := readStrictJSON(approvalPath, &approval); err != nil {
		return operatorapproval.Review{}, errors.New("read private operator approval")
	}
	if err := operatorapproval.Verify(
		policy.OperatorApprover, request, validated, approval,
	); err != nil {
		return operatorapproval.Review{}, err
	}
	return operatorapproval.BuildReview(policy.OperatorApprover, request, validated)
}
