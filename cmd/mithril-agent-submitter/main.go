package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/submitterclient"
	"github.com/Overclock-Validator/mithril-agent/submittertransport"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const (
	maxPolicyBytes  = 64 << 10
	maxRequestBytes = 64 << 10
	maxPrepareBytes = signer.MaxRequestBytes + maxRequestBytes + 1024
	maxSocketBytes  = maxPrepareBytes + 1024
	maxControlBytes = maxRequestBytes + 1024
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mithril-agent-submitter:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("mithril-agent-submitter", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "private submitter policy JSON")
	keyPath := flags.String("key", "", "private submitter key JSON")
	identity := flags.Bool("identity", false, "print the bound public identity")
	prepareMainnet := flags.Bool("prepare-mainnet", false,
		"validate one sealed Mainnet response and persist recovery evidence without submitting")
	checkMainnet := flags.Bool("check-mainnet", false,
		"read-only readiness check for one offline-prepared Mainnet action")
	recoveryStatus := flags.Bool("recovery-status", false,
		"print bounded read-only Mainnet recovery status")
	retireMainnet := flags.Bool("retire-mainnet", false,
		"archive one never-sent Mainnet action while control is stopped")
	prepareRequestPath := flags.String("signer-request", "", "private granted signer request JSON")
	prepareResponsePath := flags.String("signer-response", "", "private sealed signer response JSON")
	socket := flags.Bool("socket", false, "serve one systemd-activated socket request on stdin/stdout")
	operatorSocket := flags.Bool("operator-socket", false,
		"serve one root-only operator socket request on stdin/stdout")
	recoverPending := flags.Bool("recover", false,
		"independently reconcile the submitter-owned pending record")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent-submitter --policy PATH [--key PATH] [--identity|--prepare-mainnet [--signer-request PATH --signer-response PATH]|--check-mainnet|--recovery-status|--retire-mainnet|--socket|--operator-socket|--recover]")
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *policyPath == "" {
		return errors.New("--policy is required")
	}
	modeCount := 0
	for _, selected := range []bool{
		*identity, *prepareMainnet, *checkMainnet, *recoveryStatus, *retireMainnet,
		*socket, *operatorSocket, *recoverPending,
	} {
		if selected {
			modeCount++
		}
	}
	if modeCount > 1 {
		return errors.New("submitter modes are mutually exclusive")
	}
	if (*prepareRequestPath == "") != (*prepareResponsePath == "") {
		return errors.New("--signer-request and --signer-response are required together")
	}
	if !*prepareMainnet && (*prepareRequestPath != "" || *prepareResponsePath != "") {
		return errors.New("--signer-request and --signer-response require --prepare-mainnet")
	}
	policyData, err := securefile.ReadPrivate(*policyPath, maxPolicyBytes)
	if err != nil {
		return fmt.Errorf("read submitter policy: %w", err)
	}
	var policy submitter.Policy
	if err := strictjson.Decode(policyData, &policy); err != nil {
		return errors.New("decode submitter policy")
	}
	if *operatorSocket {
		if *keyPath != "" {
			return errors.New("--operator-socket must not receive --key")
		}
		return runOperatorSocket(policy, input, output)
	}
	if *checkMainnet {
		if *keyPath != "" {
			return errors.New("--check-mainnet must not receive --key")
		}
		if policy.Jupiter == nil {
			return errors.New("--check-mainnet requires a Jupiter Mainnet policy")
		}
		if err := submitter.ValidateJupiterPolicy(policy); err != nil {
			return err
		}
		return checkMainnetReadiness(ctx, policy, output)
	}
	if *recoveryStatus {
		if *keyPath != "" {
			return errors.New("--recovery-status must not receive --key")
		}
		if policy.Jupiter == nil {
			return errors.New("--recovery-status requires a Jupiter Mainnet policy")
		}
		status, err := submitter.ReadJupiterRecoveryStatus(policy)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(status)
	}
	if *retireMainnet {
		if *keyPath != "" {
			return errors.New("--retire-mainnet must not receive --key")
		}
		actionID, err := submitter.RetireUnstartedJupiterRecovery(policy)
		if err != nil {
			return err
		}
		return writeSocketResponse(output, submittertransport.Response{
			Version: submittertransport.Version, Status: submittertransport.StatusOK,
			ActionID: actionID,
		})
	}
	if *recoverPending {
		if *keyPath != "" {
			return errors.New("--recover must not receive --key")
		}
		if err := validateSubmitterPolicy(policy); err != nil {
			return err
		}
		return recoverSubmission(ctx, policy)
	}
	if *keyPath == "" {
		return errors.New("--key is required")
	}
	privateKey, err := submitter.LoadPrivateKey(*keyPath)
	if err != nil {
		return errors.New("load submitter key")
	}
	if *socket {
		return runSocket(ctx, policy, privateKey, input, output)
	}
	if *identity {
		identity, err := submitterIdentity(policy, privateKey)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(identity)
	}
	if *prepareMainnet {
		return runMainnetPrepare(
			policy, privateKey, *prepareRequestPath, *prepareResponsePath, input, output,
		)
	}
	if err := policy.Validate(); err != nil {
		return err
	}
	requestData, err := io.ReadAll(io.LimitReader(input, maxRequestBytes+1))
	if err != nil {
		return err
	}
	if len(requestData) > maxRequestBytes {
		return errors.New("submitter request exceeds 64 KiB")
	}
	var request submitterclient.Request
	if err := strictjson.Decode(requestData, &request); err != nil {
		return errors.New("decode submitter request")
	}
	endpoint := os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL")
	if endpoint == "" {
		return errors.New("Mithril RPC is not configured")
	}
	node, err := solanarpc.NewMithrilNode(endpoint, nil)
	if err != nil {
		return errors.New("Mithril RPC configuration is invalid")
	}
	submission, err := submitter.Submit(
		ctx,
		policy,
		privateKey,
		node,
		request.SignerResponse,
		request.MinContextSlot,
	)
	if err != nil {
		if errors.Is(err, submitter.ErrControlBlocked) {
			if encodeErr := json.NewEncoder(output).Encode(struct {
				Error string `json:"error"`
			}{Error: "control_blocked"}); encodeErr != nil {
				return encodeErr
			}
		}
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(submission)
}

func runMainnetPrepare(
	policy submitter.Policy,
	privateKey string,
	requestPath string,
	responsePath string,
	input io.Reader,
	output io.Writer,
) error {
	if policy.Jupiter == nil {
		return errors.New("--prepare-mainnet requires a Jupiter Mainnet policy")
	}
	if err := submitter.ValidateJupiterPolicy(policy); err != nil {
		return err
	}
	var request submitterclient.PrepareRequest
	if requestPath != "" {
		requestData, err := securefile.ReadPrivate(requestPath, signer.MaxRequestBytes)
		if err != nil {
			return errors.New("read granted signer request")
		}
		responseData, err := securefile.ReadPrivate(responsePath, maxRequestBytes)
		if err != nil {
			return errors.New("read sealed signer response")
		}
		if err := strictjson.Decode(requestData, &request.SignerRequest); err != nil {
			return errors.New("decode granted signer request")
		}
		if err := strictjson.Decode(responseData, &request.SignerResponse); err != nil {
			return errors.New("decode sealed signer response")
		}
	} else {
		data, err := io.ReadAll(io.LimitReader(input, maxPrepareBytes+1))
		if err != nil {
			return err
		}
		if len(data) > maxPrepareBytes {
			return errors.New("Mainnet prepare request exceeds size limit")
		}
		if err := strictjson.Decode(data, &request); err != nil {
			return errors.New("decode Mainnet prepare request")
		}
	}
	if err := submitter.PrepareJupiterRecovery(
		policy, privateKey, request.SignerRequest, request.SignerResponse,
	); err != nil {
		return err
	}
	return writeSocketResponse(output, submittertransport.Response{
		Version: submittertransport.Version, Status: submittertransport.StatusOK,
		ActionID: request.SignerRequest.ActionID,
	})
}

func validateSubmitterPolicy(policy submitter.Policy) error {
	if policy.Jupiter != nil {
		return submitter.ValidateJupiterPolicy(policy)
	}
	return policy.Validate()
}

func submitterIdentity(
	policy submitter.Policy,
	privateKey string,
) (submittertransport.Identity, error) {
	if err := validateSubmitterPolicy(policy); err != nil {
		return submittertransport.Identity{}, err
	}
	publicKey, err := sealedtx.PublicKey(privateKey)
	if err != nil || publicKey != policy.SubmitterPublicKey {
		return submittertransport.Identity{}, errors.New("submitter key does not match policy")
	}
	return submittertransport.Identity{
		PublicKey: publicKey, ProfileFingerprint: policy.ProfileFingerprint,
		Source: policy.Source,
	}, nil
}

func runSocket(
	ctx context.Context,
	policy submitter.Policy,
	privateKey string,
	input io.Reader,
	output io.Writer,
) error {
	requestLimit := int64(maxControlBytes)
	if policy.Jupiter != nil {
		requestLimit = maxSocketBytes
	}
	data, err := io.ReadAll(io.LimitReader(input, requestLimit+1))
	if err != nil {
		return writeSocketFailure(output, err)
	}
	if int64(len(data)) > requestLimit {
		return writeSocketFailure(output, errors.New("submitter socket request exceeds size limit"))
	}
	var request submittertransport.Request
	if err := strictjson.Decode(data, &request); err != nil {
		return writeSocketFailure(output, errors.New("decode submitter socket request"))
	}
	if request.Version != submittertransport.Version {
		return writeSocketFailure(output, errors.New("submitter socket protocol version is invalid"))
	}
	identity, err := submitterIdentity(policy, privateKey)
	if err != nil {
		return writeSocketFailure(output, err)
	}
	mainnet := policy.Jupiter != nil
	if mainnet && request.Operation != submittertransport.OperationIdentity &&
		request.Operation != submittertransport.OperationPrepare {
		return writeSocketFailure(output, errors.New("Mainnet submitter operation is disabled"))
	}
	switch request.Operation {
	case submittertransport.OperationIdentity:
		if !emptyControlRequest(request) {
			return writeSocketFailure(output, errors.New("submitter identity request contains submission data"))
		}
		return writeSocketResponse(output, submittertransport.Response{
			Version: submittertransport.Version, Status: submittertransport.StatusOK,
			Identity: &identity,
		})
	case submittertransport.OperationPrepare:
		if !mainnet || request.SignerRequest == nil || request.SignerResponse == nil ||
			request.MinContextSlot != 0 || request.ActionID != "" || request.Outcome != "" ||
			request.Reason != "" || hasOperatorFields(request) {
			return writeSocketFailure(output, errors.New("Mainnet prepare request is invalid"))
		}
		if err := submitter.PrepareJupiterRecovery(
			policy, privateKey, *request.SignerRequest, *request.SignerResponse,
		); err != nil {
			return writeSocketFailure(output, err)
		}
		return writeSocketResponse(output, submittertransport.Response{
			Version: submittertransport.Version, Status: submittertransport.StatusOK,
			ActionID: request.SignerRequest.ActionID,
		})
	case submittertransport.OperationSubmit:
		if request.SignerRequest != nil || request.SignerResponse == nil || request.MinContextSlot == 0 ||
			request.ActionID != "" || request.Outcome != "" || request.Reason != "" ||
			hasOperatorFields(request) {
			return writeSocketFailure(output, errors.New("submitter socket request is missing submission data"))
		}
		encoded, err := json.Marshal(request.SignerResponse)
		if err != nil || len(encoded) > maxRequestBytes {
			return writeSocketFailure(output, errors.New("submitter request exceeds 64 KiB"))
		}
		submission, err := submitWithPolicy(ctx, policy, privateKey, *request.SignerResponse, request.MinContextSlot)
		if errors.Is(err, submitter.ErrControlBlocked) {
			return writeSocketResponse(output, submittertransport.Response{
				Version: submittertransport.Version, Status: submittertransport.StatusRefused,
			})
		}
		if err != nil {
			return writeSocketFailure(output, err)
		}
		return writeSocketResponse(output, submittertransport.Response{
			Version: submittertransport.Version, Status: submittertransport.StatusOK,
			Submission: &submission,
		})
	case submittertransport.OperationStatus:
		if !emptyControlRequest(request) {
			return writeSocketFailure(output, errors.New("submitter status request contains mutation data"))
		}
		gate, err := newControlGate(policy)
		if err != nil {
			return writeSocketFailure(output, err)
		}
		status, err := gate.Status()
		if err != nil {
			return writeSocketFailure(output, err)
		}
		return writeSocketResponse(output, submittertransport.Response{
			Version: submittertransport.Version, Status: submittertransport.StatusOK,
			Control: &status,
		})
	case submittertransport.OperationReserve, submittertransport.OperationRecover:
		if request.ActionID == "" || request.SignerRequest != nil ||
			request.SignerResponse != nil || request.MinContextSlot != 0 ||
			request.Outcome != "" || request.Reason != "" || hasOperatorFields(request) {
			return writeSocketFailure(output, errors.New("submitter barrier request is invalid"))
		}
		gate, err := newControlGate(policy)
		if err != nil {
			return writeSocketFailure(output, err)
		}
		var blocked bool
		if request.Operation == submittertransport.OperationReserve {
			blocked, err = gate.WithSendBarrier(request.ActionID, func() error { return nil })
		} else {
			blocked, err = gate.WithRecoverySendBarrier(request.ActionID, func() error { return nil })
		}
		if err != nil {
			return writeSocketFailure(output, err)
		}
		status := submittertransport.StatusOK
		if blocked {
			status = submittertransport.StatusRefused
		}
		return writeSocketResponse(output, submittertransport.Response{
			Version: submittertransport.Version, Status: status,
		})
	case submittertransport.OperationStop:
		if request.Reason == "" || request.ActionID != "" || request.Outcome != "" ||
			request.SignerRequest != nil || request.SignerResponse != nil ||
			request.MinContextSlot != 0 || hasOperatorFields(request) {
			return writeSocketFailure(output, errors.New("submitter stop request is invalid"))
		}
		gate, err := newControlGate(policy)
		if err != nil {
			return writeSocketFailure(output, err)
		}
		if err := gate.StopPreservingRecovery(request.Reason); errors.Is(err, control.ErrRecoveryPending) {
			return writeSocketResponse(output, submittertransport.Response{
				Version: submittertransport.Version, Status: submittertransport.StatusRecoveryPending,
			})
		} else if err != nil {
			return writeSocketFailure(output, err)
		}
		return writeSocketResponse(output, submittertransport.Response{
			Version: submittertransport.Version, Status: submittertransport.StatusOK,
		})
	case submittertransport.OperationTerminal:
		if request.ActionID == "" || request.Outcome == "" || request.Reason != "" ||
			request.SignerRequest != nil || request.SignerResponse != nil ||
			request.MinContextSlot != 0 || hasOperatorFields(request) {
			return writeSocketFailure(output, errors.New("submitter terminal request is invalid"))
		}
		gate, err := newControlGate(policy)
		if err != nil {
			return writeSocketFailure(output, err)
		}
		if err := gate.StopForTerminal(request.ActionID, request.Outcome); err != nil {
			return writeSocketFailure(output, err)
		}
		return writeSocketResponse(output, submittertransport.Response{
			Version: submittertransport.Version, Status: submittertransport.StatusOK,
		})
	case submittertransport.OperationLatch:
		if !emptyControlRequest(request) {
			return writeSocketFailure(output, errors.New("submitter latch request contains mutation data"))
		}
		gate, err := newControlGate(policy)
		if err != nil {
			return writeSocketFailure(output, err)
		}
		actionID, outcome, err := gate.TerminalLatch()
		if err != nil {
			return writeSocketFailure(output, err)
		}
		return writeSocketResponse(output, submittertransport.Response{
			Version: submittertransport.Version, Status: submittertransport.StatusOK,
			ActionID: actionID, Outcome: outcome,
		})
	default:
		return writeSocketFailure(output, errors.New("submitter socket operation is invalid"))
	}
}

func recoverSubmission(ctx context.Context, policy submitter.Policy) error {
	primary, secondary, err := openRecoveryProviders(policy)
	if err != nil {
		return err
	}
	lifecycle, err := txflow.NewEvidenceLifecycle(primary, secondary)
	if err != nil {
		return err
	}
	actionID, result, err := submitter.ReconcileRecovery(ctx, policy, lifecycle)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if result.Verdict != txflow.VerdictFinalized && result.Verdict != txflow.VerdictFailed {
		return nil
	}
	gate, err := newControlGate(policy)
	if err != nil {
		return err
	}
	return applyRecoveryResult(gate, actionID, result.Verdict)
}

func applyRecoveryResult(gate *control.StateFile, actionID, verdict string) error {
	switch verdict {
	case txflow.VerdictFinalized:
		return gate.ClearTerminalForFinalized(actionID)
	case txflow.VerdictFailed:
		return gate.StopForTerminal(actionID, txflow.VerdictFailed)
	default:
		return errors.New("recovery verdict is not terminal")
	}
}

func checkMainnetReadiness(
	ctx context.Context,
	policy submitter.Policy,
	output io.Writer,
) error {
	primary, secondary, err := openRecoveryProviders(policy)
	if err != nil {
		return err
	}
	endpoint := os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL")
	if endpoint == "" {
		return errors.New("Mithril RPC is not configured")
	}
	node, err := solanarpc.NewMithrilNode(endpoint, nil)
	if err != nil {
		return errors.New("Mithril RPC configuration is invalid")
	}
	lifecycle, err := txflow.New(node, primary, secondary)
	if err != nil {
		return err
	}
	actionID, err := submitter.CheckJupiterRecoveryReadiness(
		ctx, policy, lifecycle, primary, secondary,
	)
	if err != nil {
		return err
	}
	return writeSocketResponse(output, submittertransport.Response{
		Version: submittertransport.Version, Status: submittertransport.StatusOK,
		ActionID: actionID,
	})
}

func openRecoveryProviders(policy submitter.Policy) (*solanarpc.Client, *solanarpc.Client, error) {
	primaryURL := os.Getenv("MITHRIL_AGENT_PRIMARY_RPC_URL")
	secondaryURL := os.Getenv("MITHRIL_AGENT_SECONDARY_RPC_URL")
	if primaryURL == "" || secondaryURL == "" {
		return nil, nil, errors.New("two evidence RPCs are required")
	}
	primary, err := solanarpc.NewPaced(primaryURL, nil, 750*time.Millisecond)
	if err != nil {
		return nil, nil, errors.New("primary evidence RPC configuration is invalid")
	}
	secondary, err := solanarpc.NewPaced(secondaryURL, nil, 750*time.Millisecond)
	if err != nil {
		return nil, nil, errors.New("secondary evidence RPC configuration is invalid")
	}
	if policy.Evidence.Validate() != nil || primary.Identity() != policy.Evidence.PrimaryOriginSHA256 ||
		secondary.Identity() != policy.Evidence.SecondaryOriginSHA256 {
		return nil, nil, errors.New("evidence RPCs do not match submitter policy")
	}
	return primary, secondary, nil
}

func emptyControlRequest(request submittertransport.Request) bool {
	return request.SignerRequest == nil && request.SignerResponse == nil &&
		request.MinContextSlot == 0 &&
		request.ActionID == "" && request.Outcome == "" && request.Reason == "" &&
		!hasOperatorFields(request)
}

func hasOperatorFields(request submittertransport.Request) bool {
	return request.ExpectedRevision != "" || !request.IssuedAt.IsZero() ||
		!request.ExpiresAt.IsZero() || request.MaxActions != 0
}

func runOperatorSocket(
	policy submitter.Policy,
	input io.Reader,
	output io.Writer,
) error {
	if err := validateSubmitterPolicy(policy); err != nil {
		return writeSocketFailure(output, err)
	}
	data, err := io.ReadAll(io.LimitReader(input, maxControlBytes+1))
	if err != nil {
		return writeSocketFailure(output, err)
	}
	if len(data) > maxControlBytes {
		return writeSocketFailure(output, errors.New("operator socket request exceeds size limit"))
	}
	var request submittertransport.Request
	if err := strictjson.Decode(data, &request); err != nil {
		return writeSocketFailure(output, errors.New("decode operator socket request"))
	}
	if request.Version != submittertransport.Version {
		return writeSocketFailure(output, errors.New("operator socket protocol version is invalid"))
	}
	gate, err := newControlGate(policy)
	if err != nil {
		return writeSocketFailure(output, err)
	}
	switch request.Operation {
	case submittertransport.OperationOperatorStatus:
		if !emptyControlRequest(request) {
			return writeSocketFailure(output, errors.New("operator status request contains mutation data"))
		}
		status, err := gate.Status()
		if err != nil {
			return writeSocketFailure(output, err)
		}
		revision, err := gate.Revision()
		if err != nil {
			return writeSocketFailure(output, err)
		}
		return writeSocketResponse(output, submittertransport.Response{
			Version: submittertransport.Version, Status: submittertransport.StatusOK,
			Control: &status, Revision: revision,
		})
	case submittertransport.OperationOperatorSnapshot:
		if !emptyControlRequest(request) {
			return writeSocketFailure(output, errors.New("operator snapshot request contains mutation data"))
		}
		status, err := gate.Status()
		if err != nil {
			return writeSocketFailure(output, err)
		}
		revision, err := gate.Revision()
		if err != nil {
			return writeSocketFailure(output, err)
		}
		return writeSocketResponse(output, submittertransport.Response{
			Version: submittertransport.Version, Status: submittertransport.StatusOK,
			Identity: &submittertransport.Identity{
				PublicKey: policy.SubmitterPublicKey, ProfileFingerprint: policy.ProfileFingerprint,
				Source: policy.Source,
			},
			Control: &status, Revision: revision,
		})
	case submittertransport.OperationEnable:
		if request.SignerRequest != nil || request.SignerResponse != nil ||
			request.MinContextSlot != 0 ||
			request.Outcome != "" || request.Reason == "" ||
			request.ExpectedRevision == "" || request.IssuedAt.IsZero() ||
			request.ExpiresAt.IsZero() || request.MaxActions == 0 ||
			(policy.Jupiter == nil && request.ActionID != "") ||
			(policy.Jupiter != nil && request.ActionID == "") {
			return writeSocketFailure(output, errors.New("operator enable request is invalid"))
		}
		written, err := writeControlActivation(policy, request)
		if err != nil {
			return writeSocketFailure(output, err)
		}
		if !written {
			return writeSocketResponse(output, submittertransport.Response{
				Version: submittertransport.Version, Status: submittertransport.StatusConflict,
			})
		}
		return writeSocketResponse(output, submittertransport.Response{
			Version: submittertransport.Version, Status: submittertransport.StatusOK,
		})
	case submittertransport.OperationAcknowledgeTerminal:
		if request.SignerRequest != nil || request.SignerResponse != nil ||
			request.MinContextSlot != 0 ||
			request.ActionID == "" || request.Outcome == "" || request.Reason == "" ||
			hasOperatorFields(request) {
			return writeSocketFailure(output, errors.New("operator acknowledgement request is invalid"))
		}
		if err := gate.StopForTerminal(request.ActionID, request.Outcome); err != nil {
			return writeSocketFailure(output, err)
		}
		var status control.Status
		if request.Outcome == "halted" {
			status, err = gate.Status()
		} else {
			status, err = gate.AcknowledgeTerminal(
				request.ActionID, request.Outcome, request.Reason,
			)
		}
		if err != nil {
			return writeSocketFailure(output, err)
		}
		return writeSocketResponse(output, submittertransport.Response{
			Version: submittertransport.Version, Status: submittertransport.StatusOK,
			Control: &status,
		})
	default:
		return writeSocketFailure(output, errors.New("operator socket operation is invalid"))
	}
}

func newControlGate(policy submitter.Policy) (*control.StateFile, error) {
	constructor := control.NewStateFile
	if policy.Jupiter != nil {
		constructor = control.NewMainnetCanaryStateFile
	}
	gate, err := constructor(policy.ControlStatePath, policy.ProfileFingerprint, false)
	if err != nil {
		return nil, errors.New("submitter control configuration is invalid")
	}
	return gate, nil
}

func writeControlActivation(
	policy submitter.Policy,
	request submittertransport.Request,
) (bool, error) {
	if policy.Jupiter != nil {
		return control.WriteMainnetCanaryActivationForActionIfRevision(
			policy.ControlStatePath, policy.ProfileFingerprint,
			request.ExpectedRevision, request.ActionID,
			request.IssuedAt, request.ExpiresAt, request.MaxActions, request.Reason,
		)
	}
	return control.WriteDevnetActivationIfRevision(
		policy.ControlStatePath, policy.ProfileFingerprint, request.ExpectedRevision,
		request.IssuedAt, request.ExpiresAt, request.MaxActions, request.Reason,
	)
}

func submitWithPolicy(
	ctx context.Context,
	policy submitter.Policy,
	privateKey string,
	response signer.Response,
	minContextSlot uint64,
) (txflow.Submission, error) {
	endpoint := os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL")
	if endpoint == "" {
		return txflow.Submission{}, errors.New("Mithril RPC is not configured")
	}
	node, err := solanarpc.NewMithrilNode(endpoint, nil)
	if err != nil {
		return txflow.Submission{}, errors.New("Mithril RPC configuration is invalid")
	}
	return submitter.Submit(ctx, policy, privateKey, node, response, minContextSlot)
}

func writeSocketFailure(output io.Writer, cause error) error {
	if err := writeSocketResponse(output, submittertransport.Response{
		Version: submittertransport.Version, Status: submittertransport.StatusFailed,
	}); err != nil {
		return err
	}
	return cause
}

func writeSocketResponse(output io.Writer, response submittertransport.Response) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(response)
}
