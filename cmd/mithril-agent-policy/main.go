package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/operatorapproval"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/policyclient"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/signer"
)

const (
	maxPolicyBytes  = 64 << 10
	maxRequestBytes = signer.MaxRequestBytes
	maxSocketBytes  = maxRequestBytes + 1024
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, time.Now); err != nil {
		fmt.Fprintln(os.Stderr, "mithril-agent-policy:", err)
		os.Exit(1)
	}
}

func run(
	args []string,
	input io.Reader,
	output io.Writer,
	now func() time.Time,
) error {
	flags := flag.NewFlagSet("mithril-agent-policy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "private risk policy JSON")
	keypairPath := flags.String("keypair", "", "private risk authority keypair JSON")
	identity := flags.Bool("identity", false, "print the bound public identity")
	socket := flags.Bool("socket", false, "serve one systemd-activated socket request on stdin/stdout")
	grantedRequest := flags.Bool("granted-request", false, "print the complete request with its bound grant")
	approvedRequest := flags.Bool("approved-request", false, "read a Mainnet request and detached operator approval")
	operatorApprovalPath := flags.String("operator-approval", "", "private detached operator approval JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent-policy --policy PATH --keypair PATH [--identity|--socket] [--approved-request|--operator-approval PATH] [--granted-request]")
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *policyPath == "" || *keypairPath == "" {
		return errors.New("--policy and --keypair are required")
	}
	if (*identity && *socket) || (*identity && *grantedRequest) || (*socket && *grantedRequest) {
		return errors.New("--identity, --socket, and --granted-request are mutually exclusive")
	}
	if *approvedRequest && *operatorApprovalPath != "" {
		return errors.New("--approved-request and --operator-approval are mutually exclusive")
	}
	if (*identity || *socket) && (*approvedRequest || *operatorApprovalPath != "") {
		return errors.New("operator approval is only valid for a foreground authorization")
	}
	policyData, err := securefile.ReadPrivate(*policyPath, maxPolicyBytes)
	if err != nil {
		return fmt.Errorf("read policy: %w", err)
	}
	var policy policyauthority.Policy
	if err := strictjson.Decode(policyData, &policy); err != nil {
		return errors.New("decode risk policy")
	}
	privateKey, err := signer.LoadKeypair(*keypairPath)
	if err != nil {
		return fmt.Errorf("load risk authority keypair: %w", err)
	}
	defer clear(privateKey)
	if *socket {
		return runSocketAt(policy, privateKey, input, output, now)
	}
	if *identity {
		if err := policy.Validate(); err != nil {
			return err
		}
		publicKey, err := riskgrant.PublicKeyHex(privateKey)
		if err != nil || publicKey != policy.TransactionPolicy.RiskAuthorityPublicKey {
			return errors.New("risk authority key does not match policy")
		}
		return json.NewEncoder(output).Encode(struct {
			KeyID     string `json:"key_id"`
			PublicKey string `json:"public_key"`
		}{
			KeyID:     policy.TransactionPolicy.RiskAuthorityKeyID,
			PublicKey: publicKey,
		})
	}
	requestLimit := int64(maxRequestBytes)
	if *approvedRequest {
		requestLimit = maxSocketBytes
	}
	requestData, err := io.ReadAll(io.LimitReader(input, requestLimit+1))
	if err != nil {
		return err
	}
	if int64(len(requestData)) > requestLimit {
		return errors.New("risk request exceeds size limit")
	}
	var request signer.Request
	var grant riskgrant.Grant
	if *approvedRequest {
		var approved policyclient.ApprovedRequest
		if err := strictjson.Decode(requestData, &approved); err != nil {
			return errors.New("decode approved risk request")
		}
		request = approved.Request
		grant, err = policyauthority.AuthorizeApproved(
			policy, privateKey, request, approved.Approval, now().UTC(),
		)
	} else {
		if err := strictjson.Decode(requestData, &request); err != nil {
			return errors.New("decode risk request")
		}
		if *operatorApprovalPath == "" {
			grant, err = policyauthority.Authorize(policy, privateKey, request, now().UTC())
		} else {
			approvalData, readErr := securefile.ReadPrivate(*operatorApprovalPath, maxPolicyBytes)
			if readErr != nil {
				return errors.New("read private operator approval")
			}
			var approval operatorapproval.Approval
			if decodeErr := strictjson.Decode(approvalData, &approval); decodeErr != nil {
				return errors.New("decode private operator approval")
			}
			grant, err = policyauthority.AuthorizeApproved(
				policy, privateKey, request, approval, now().UTC(),
			)
		}
	}
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	if *grantedRequest {
		request.RiskGrant = grant
		return encoder.Encode(request)
	}
	return encoder.Encode(grant)
}

func runSocketAt(
	policy policyauthority.Policy,
	privateKey ed25519.PrivateKey,
	input io.Reader,
	output io.Writer,
	now func() time.Time,
) error {
	data, err := io.ReadAll(io.LimitReader(input, maxSocketBytes+1))
	if err != nil {
		return writeSocketFailure(output, err)
	}
	if len(data) > maxSocketBytes {
		return writeSocketFailure(output, errors.New("risk authority socket request exceeds size limit"))
	}
	var request policyclient.SocketRequest
	if err := strictjson.Decode(data, &request); err != nil {
		return writeSocketFailure(output, errors.New("decode risk authority socket request"))
	}
	if request.Version != policyclient.SocketVersion {
		return writeSocketFailure(output, errors.New("risk authority socket protocol version is invalid"))
	}

	switch request.Operation {
	case policyclient.SocketOperationIdentity:
		if request.Authorize != nil || request.Approval != nil {
			return writeSocketFailure(output, errors.New("risk authority identity request contains authorization data"))
		}
		if err := policy.Validate(); err != nil {
			return writeSocketFailure(output, err)
		}
		publicKey, err := riskgrant.PublicKeyHex(privateKey)
		if err != nil || publicKey != policy.TransactionPolicy.RiskAuthorityPublicKey {
			return writeSocketFailure(output, errors.New("risk authority key does not match policy"))
		}
		return writeSocketResponse(output, policyclient.SocketResponse{
			Version: policyclient.SocketVersion,
			Status:  policyclient.SocketStatusOK,
			Identity: &policyclient.Identity{
				KeyID: policy.TransactionPolicy.RiskAuthorityKeyID, PublicKey: publicKey,
			},
		})
	case policyclient.SocketOperationAuthorize:
		if request.Authorize == nil {
			return writeSocketFailure(output, errors.New("risk authority socket request is missing authorization data"))
		}
		encoded, err := json.Marshal(request.Authorize)
		if err != nil || len(encoded) > maxRequestBytes {
			return writeSocketFailure(output, errors.New("risk request exceeds size limit"))
		}
		request.Authorize.RiskGrant = riskgrant.Grant{}
		var grant riskgrant.Grant
		if request.Approval == nil {
			grant, err = policyauthority.Authorize(
				policy, privateKey, *request.Authorize, now().UTC(),
			)
		} else {
			grant, err = policyauthority.AuthorizeApproved(
				policy, privateKey, *request.Authorize, *request.Approval, now().UTC(),
			)
		}
		if err != nil {
			return writeSocketFailure(output, err)
		}
		return writeSocketResponse(output, policyclient.SocketResponse{
			Version: policyclient.SocketVersion, Status: policyclient.SocketStatusOK, Grant: &grant,
		})
	default:
		return writeSocketFailure(output, errors.New("risk authority socket operation is invalid"))
	}
}

func writeSocketFailure(output io.Writer, cause error) error {
	if err := writeSocketResponse(output, policyclient.SocketResponse{
		Version: policyclient.SocketVersion, Status: policyclient.SocketStatusFailed,
	}); err != nil {
		return err
	}
	return cause
}

func writeSocketResponse(output io.Writer, response policyclient.SocketResponse) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(response)
}
