package submitterclient

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/submittertransport"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const (
	maxResponseBytes       = 16 << 10
	maxPrepareRequestBytes = signer.MaxRequestBytes + 64<<10 + 1024
	maxSocketRequestBytes  = maxPrepareRequestBytes + 1024
	maxSocketResponseBytes = maxResponseBytes + 1024
	controlTimeout         = 30 * time.Second
)

type Config struct {
	Command        string
	PolicyPath     string
	PrivateKeyPath string
	SocketPath     string
	ControlMode    string
	Env            []string
}

type Request struct {
	SignerResponse signer.Response `json:"signer_response"`
	MinContextSlot uint64          `json:"min_context_slot"`
}

// PrepareRequest carries the exact checked request and sealed signer response
// into the independent Mainnet submitter boundary without authorizing a send.
type PrepareRequest struct {
	SignerRequest  signer.Request  `json:"signer_request"`
	SignerResponse signer.Response `json:"signer_response"`
}

type Client struct {
	config Config
}

type Identity struct {
	PublicKey          string `json:"public_key"`
	ProfileFingerprint string `json:"profile_sha256,omitempty"`
	Source             string `json:"source,omitempty"`
}

func New(config Config) (*Client, error) {
	switch config.ControlMode {
	case "":
		config.ControlMode = control.ModeDevnetEnabled
	case control.ModeDevnetEnabled, control.ModeMainnetCanary:
	default:
		return nil, errors.New("submitter control mode is invalid")
	}
	if config.SocketPath != "" {
		if !filepath.IsAbs(config.SocketPath) || filepath.Clean(config.SocketPath) != config.SocketPath {
			return nil, errors.New("submitter socket path must be absolute and clean")
		}
		return &Client{config: config}, nil
	}
	if err := secureexec.ValidateExecutable(config.Command); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(config.PolicyPath) || !filepath.IsAbs(config.PrivateKeyPath) {
		return nil, errors.New("submitter policy and key paths must be absolute")
	}
	if err := secureexec.ValidateEnvironment(config.Env); err != nil {
		return nil, err
	}
	config.Env = append([]string(nil), config.Env...)
	return &Client{config: config}, nil
}

func (c *Client) Identity(ctx context.Context) (Identity, error) {
	ctx, cancel, err := boundedOperationContext(ctx)
	if err != nil {
		return Identity{}, err
	}
	defer cancel()
	if c.config.SocketPath != "" {
		response, err := c.socketRoundTrip(ctx, submittertransport.Request{
			Version: submittertransport.Version, Operation: submittertransport.OperationIdentity,
		})
		if err != nil {
			return Identity{}, err
		}
		if response.Status != submittertransport.StatusOK || response.Identity == nil ||
			response.Submission != nil || response.Control != nil || response.ActionID != "" ||
			response.Outcome != "" || response.Revision != "" {
			return Identity{}, errors.New("submitter identity response is invalid")
		}
		decoded, err := hex.DecodeString(response.Identity.PublicKey)
		if err != nil || len(decoded) != 32 ||
			hex.EncodeToString(decoded) != response.Identity.PublicKey {
			return Identity{}, errors.New("submitter identity is invalid")
		}
		if response.Identity.ProfileFingerprint == "" || response.Identity.Source == "" {
			return Identity{}, errors.New("submitter identity binding is incomplete")
		}
		return Identity{
			PublicKey:          response.Identity.PublicKey,
			ProfileFingerprint: response.Identity.ProfileFingerprint,
			Source:             response.Identity.Source,
		}, nil
	}
	if err := secureexec.ValidateExecutable(c.config.Command); err != nil {
		return Identity{}, err
	}
	command := exec.CommandContext(
		ctx,
		c.config.Command,
		"--policy", c.config.PolicyPath,
		"--key", c.config.PrivateKeyPath,
		"--identity",
	)
	command.Env = secureexec.MinimalEnvironment(c.config.Env)
	command.Stdin = bytes.NewReader(nil)
	command.Stderr = &secureexec.DiscardCounter{}
	var output bytes.Buffer
	command.Stdout = &limitedWriter{writer: &output, remaining: maxResponseBytes + 1}
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		return Identity{}, errors.New("submitter identity check failed")
	}
	if output.Len() > maxResponseBytes {
		return Identity{}, errors.New("submitter identity response exceeds size limit")
	}
	var identity Identity
	if err := strictjson.Decode(output.Bytes(), &identity); err != nil {
		return Identity{}, errors.New("decode submitter identity")
	}
	decoded, err := hex.DecodeString(identity.PublicKey)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != identity.PublicKey {
		return Identity{}, errors.New("submitter identity is invalid")
	}
	return identity, nil
}

func (c *Client) Submit(
	ctx context.Context,
	response signer.Response,
	minContextSlot uint64,
) (txflow.Submission, error) {
	ctx, cancel, err := boundedOperationContext(ctx)
	if err != nil {
		return txflow.Submission{}, err
	}
	defer cancel()
	if minContextSlot == 0 || response.BlockhashContextSlot != minContextSlot {
		return txflow.Submission{}, errors.New("submitter minimum context slot does not match signer response")
	}
	if c.config.SocketPath != "" {
		result, err := c.socketRoundTrip(ctx, submittertransport.Request{
			Version: submittertransport.Version, Operation: submittertransport.OperationSubmit,
			SignerResponse: &response, MinContextSlot: minContextSlot,
		})
		if err != nil {
			return txflow.Submission{}, err
		}
		switch result.Status {
		case submittertransport.StatusRefused:
			if err := validateRefusalResponse(result); err != nil {
				return txflow.Submission{}, errors.New("submitter refusal response is invalid")
			}
			return txflow.Submission{}, submitter.ErrControlBlocked
		case submittertransport.StatusFailed:
			if err := validateEmptyResponseFields(result); err != nil {
				return txflow.Submission{}, errors.New("submitter failure response is invalid")
			}
			return txflow.Submission{}, errors.New("submitter service failed")
		case submittertransport.StatusOK:
			if result.Identity != nil || result.Submission == nil || result.Control != nil ||
				result.ActionID != "" || result.Outcome != "" || result.Revision != "" {
				return txflow.Submission{}, errors.New("submitter response is invalid")
			}
			if err := validateSubmission(*result.Submission, response); err != nil {
				return txflow.Submission{}, err
			}
			return *result.Submission, nil
		default:
			return txflow.Submission{}, errors.New("submitter service status is invalid")
		}
	}
	if err := secureexec.ValidateExecutable(c.config.Command); err != nil {
		return txflow.Submission{}, err
	}
	request := Request{SignerResponse: response, MinContextSlot: minContextSlot}
	input, err := json.Marshal(request)
	if err != nil {
		return txflow.Submission{}, errors.New("encode submitter request")
	}
	command := exec.CommandContext(
		ctx,
		c.config.Command,
		"--policy", c.config.PolicyPath,
		"--key", c.config.PrivateKeyPath,
	)
	command.Env = secureexec.MinimalEnvironment(c.config.Env)
	command.Stdin = bytes.NewReader(input)
	stderr := &secureexec.DiscardCounter{}
	command.Stderr = stderr
	var output bytes.Buffer
	command.Stdout = &limitedWriter{writer: &output, remaining: maxResponseBytes + 1}
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		return txflow.Submission{}, decodeProcessFailure(output.Bytes())
	}
	if output.Len() > maxResponseBytes {
		return txflow.Submission{}, errors.New("submitter response exceeds size limit")
	}
	var submission txflow.Submission
	if err := strictjson.Decode(output.Bytes(), &submission); err != nil {
		return txflow.Submission{}, errors.New("decode submitter response")
	}
	if err := validateSubmission(submission, response); err != nil {
		return txflow.Submission{}, err
	}
	return submission, nil
}

// PrepareJupiter validates and persists one Mainnet recovery record without
// opening an RPC or submitting a transaction.
func (c *Client) PrepareJupiter(
	ctx context.Context,
	request signer.Request,
	response signer.Response,
) error {
	ctx, cancel, err := boundedOperationContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if request.Cluster != "mainnet-beta" || request.ActionID == "" ||
		response.ActionID != request.ActionID {
		return errors.New("Mainnet prepare request does not match signer response")
	}
	if c.config.SocketPath != "" {
		result, err := c.socketRoundTrip(ctx, submittertransport.Request{
			Version: submittertransport.Version, Operation: submittertransport.OperationPrepare,
			SignerRequest: &request, SignerResponse: &response,
		})
		if err != nil {
			return err
		}
		return validatePrepareResponse(result, request.ActionID)
	}
	if err := secureexec.ValidateExecutable(c.config.Command); err != nil {
		return err
	}
	input, err := json.Marshal(PrepareRequest{
		SignerRequest: request, SignerResponse: response,
	})
	if err != nil || len(input) > maxPrepareRequestBytes {
		return errors.New("encode Mainnet prepare request")
	}
	command := exec.CommandContext(
		ctx,
		c.config.Command,
		"--policy", c.config.PolicyPath,
		"--key", c.config.PrivateKeyPath,
		"--prepare-mainnet",
	)
	command.Env = secureexec.MinimalEnvironment(c.config.Env)
	command.Stdin = bytes.NewReader(input)
	command.Stderr = &secureexec.DiscardCounter{}
	var output bytes.Buffer
	command.Stdout = &limitedWriter{writer: &output, remaining: maxResponseBytes + 1}
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		return errors.New("Mainnet submitter preparation failed")
	}
	if output.Len() > maxResponseBytes {
		return errors.New("Mainnet prepare response exceeds size limit")
	}
	var result submittertransport.Response
	if err := strictjson.Decode(output.Bytes(), &result); err != nil ||
		result.Version != submittertransport.Version {
		return errors.New("decode Mainnet prepare response")
	}
	return validatePrepareResponse(result, request.ActionID)
}

// CheckMainnetReadiness runs the submitter's keyless readiness mode. It passes
// no submitter key and accepts only the bounded public action identifier.
func CheckMainnetReadiness(
	ctx context.Context,
	commandPath,
	policyPath string,
	environment []string,
) (string, error) {
	ctx, cancel, err := boundedOperationContext(ctx)
	if err != nil {
		return "", err
	}
	defer cancel()
	if err := secureexec.ValidateExecutable(commandPath); err != nil {
		return "", err
	}
	if !filepath.IsAbs(policyPath) || filepath.Clean(policyPath) != policyPath {
		return "", errors.New("submitter policy path must be absolute and clean")
	}
	if err := secureexec.ValidateEnvironment(environment); err != nil {
		return "", err
	}
	command := exec.CommandContext(
		ctx, commandPath, "--policy", policyPath, "--check-mainnet",
	)
	command.Env = secureexec.MinimalEnvironment(environment)
	command.Stdin = bytes.NewReader(nil)
	command.Stderr = &secureexec.DiscardCounter{}
	var output bytes.Buffer
	command.Stdout = &limitedWriter{writer: &output, remaining: maxResponseBytes + 1}
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		return "", errors.New("Mainnet readiness check failed")
	}
	if output.Len() > maxResponseBytes {
		return "", errors.New("Mainnet readiness response exceeds size limit")
	}
	var response submittertransport.Response
	if err := strictjson.Decode(output.Bytes(), &response); err != nil ||
		response.Version != submittertransport.Version ||
		response.Status != submittertransport.StatusOK || response.Identity != nil ||
		response.Submission != nil || response.Control != nil || response.Outcome != "" ||
		response.Revision != "" {
		return "", errors.New("Mainnet readiness response is invalid")
	}
	decoded, err := hex.DecodeString(response.ActionID)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != response.ActionID {
		return "", errors.New("Mainnet readiness action is invalid")
	}
	return response.ActionID, nil
}

func boundedOperationContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, errors.New("submitter operation context is unavailable")
	}
	bounded, cancel := context.WithTimeout(ctx, controlTimeout)
	return bounded, cancel, nil
}

func validatePrepareResponse(response submittertransport.Response, actionID string) error {
	if response.Status == submittertransport.StatusFailed {
		if err := validateEmptyResponseFields(response); err != nil {
			return errors.New("Mainnet prepare failure response is invalid")
		}
		return errors.New("Mainnet submitter service failed")
	}
	if response.Status != submittertransport.StatusOK || response.Identity != nil ||
		response.Submission != nil || response.Control != nil || response.ActionID != actionID ||
		response.Outcome != "" || response.Revision != "" {
		return errors.New("Mainnet prepare response is invalid")
	}
	return nil
}

func validateSubmission(submission txflow.Submission, response signer.Response) error {
	if submission.Signature != response.Signature ||
		submission.LastValidBlockHeight != response.LastValidBlockHeight ||
		(submission.State != txflow.StateAccepted && submission.State != txflow.StateAmbiguous) {
		return errors.New("submitter response does not match request")
	}
	if _, err := solana.Decode64(submission.Signature); err != nil {
		return errors.New("submitter returned an invalid signature")
	}
	return nil
}

func (c *Client) Status() (control.Status, error) {
	response, err := c.controlRoundTrip(submittertransport.Request{
		Version: submittertransport.Version, Operation: submittertransport.OperationStatus,
	})
	if err != nil {
		return control.Status{}, err
	}
	if response.Status != submittertransport.StatusOK || response.Control == nil ||
		response.Identity != nil || response.Submission != nil ||
		response.ActionID != "" || response.Outcome != "" || response.Revision != "" {
		return control.Status{}, errors.New("submitter control status response is invalid")
	}
	if err := c.validateControlStatus(*response.Control); err != nil {
		return control.Status{}, err
	}
	return *response.Control, nil
}

func (c *Client) NoNewActions() (bool, error) {
	status, err := c.Status()
	return status.Mode == control.ModeNoNewActions, err
}

func (c *Client) StopPreservingRecovery(reason string) error {
	response, err := c.controlRoundTrip(submittertransport.Request{
		Version: submittertransport.Version, Operation: submittertransport.OperationStop,
		Reason: reason,
	})
	if err != nil {
		return err
	}
	if response.Status == submittertransport.StatusRecoveryPending {
		if err := validateEmptyResponseFields(response); err != nil {
			return err
		}
		return control.ErrRecoveryPending
	}
	return validateEmptyControlResponse(response)
}

func (c *Client) StopForTerminal(actionID, outcome string) error {
	response, err := c.controlRoundTrip(submittertransport.Request{
		Version: submittertransport.Version, Operation: submittertransport.OperationTerminal,
		ActionID: actionID, Outcome: outcome,
	})
	if err != nil {
		return err
	}
	return validateEmptyControlResponse(response)
}

func (c *Client) TerminalLatch() (string, string, error) {
	response, err := c.controlRoundTrip(submittertransport.Request{
		Version: submittertransport.Version, Operation: submittertransport.OperationLatch,
	})
	if err != nil {
		return "", "", err
	}
	if response.Status != submittertransport.StatusOK || response.Identity != nil ||
		response.Submission != nil || response.Control != nil || response.Revision != "" {
		return "", "", errors.New("submitter terminal latch response is invalid")
	}
	if response.ActionID == "" && response.Outcome == "" {
		return "", "", nil
	}
	decoded, err := hex.DecodeString(response.ActionID)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != response.ActionID ||
		(response.Outcome != "failed" && response.Outcome != "halted") {
		return "", "", errors.New("submitter terminal latch response is invalid")
	}
	return response.ActionID, response.Outcome, nil
}

func (c *Client) OperatorStatus() (control.Status, string, error) {
	response, err := c.controlRoundTrip(submittertransport.Request{
		Version:   submittertransport.Version,
		Operation: submittertransport.OperationOperatorStatus,
	})
	if err != nil {
		return control.Status{}, "", err
	}
	if response.Status != submittertransport.StatusOK || response.Control == nil ||
		response.Identity != nil || response.Submission != nil || response.ActionID != "" ||
		response.Outcome != "" || len(response.Revision) != 64 {
		return control.Status{}, "", errors.New("submitter operator status response is invalid")
	}
	if err := c.validateControlStatus(*response.Control); err != nil {
		return control.Status{}, "", err
	}
	decoded, err := hex.DecodeString(response.Revision)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != response.Revision {
		return control.Status{}, "", errors.New("submitter operator revision is invalid")
	}
	return *response.Control, response.Revision, nil
}

// OperatorSnapshot binds the public submitter-policy identity, control status,
// and state revision returned by one keyless operator-socket request.
func (c *Client) OperatorSnapshot() (Identity, control.Status, string, error) {
	response, err := c.controlRoundTrip(submittertransport.Request{
		Version:   submittertransport.Version,
		Operation: submittertransport.OperationOperatorSnapshot,
	})
	if err != nil {
		return Identity{}, control.Status{}, "", err
	}
	if response.Status != submittertransport.StatusOK || response.Control == nil ||
		response.Identity == nil || response.Submission != nil || response.ActionID != "" ||
		response.Outcome != "" || len(response.Revision) != 64 {
		return Identity{}, control.Status{}, "", errors.New("submitter operator status response is invalid")
	}
	if err := c.validateControlStatus(*response.Control); err != nil {
		return Identity{}, control.Status{}, "", err
	}
	decoded, err := hex.DecodeString(response.Revision)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != response.Revision {
		return Identity{}, control.Status{}, "", errors.New("submitter operator revision is invalid")
	}
	identity := Identity{
		PublicKey:          response.Identity.PublicKey,
		ProfileFingerprint: response.Identity.ProfileFingerprint,
		Source:             response.Identity.Source,
	}
	publicKey, err := hex.DecodeString(identity.PublicKey)
	if err != nil || len(publicKey) != 32 || hex.EncodeToString(publicKey) != identity.PublicKey ||
		identity.ProfileFingerprint == "" || identity.Source == "" {
		return Identity{}, control.Status{}, "", errors.New("submitter operator identity is invalid")
	}
	return identity, *response.Control, response.Revision, nil
}

func (c *Client) Enable(
	expectedRevision string,
	issuedAt,
	expiresAt time.Time,
	maxActions uint32,
	reason string,
) error {
	if c.config.ControlMode != control.ModeDevnetEnabled {
		return errors.New("Devnet enable requires a Devnet submitter client")
	}
	response, err := c.controlRoundTrip(submittertransport.Request{
		Version: submittertransport.Version, Operation: submittertransport.OperationEnable,
		ExpectedRevision: expectedRevision, IssuedAt: issuedAt, ExpiresAt: expiresAt,
		MaxActions: maxActions, Reason: reason,
	})
	if err != nil {
		return err
	}
	return validateEnableResponse(response)
}

func validateEnableResponse(response submittertransport.Response) error {
	if response.Status == submittertransport.StatusConflict {
		if err := validateEmptyResponseFields(response); err != nil {
			return err
		}
		return errors.New("control state changed while enabling; inspect status and retry")
	}
	return validateEmptyControlResponse(response)
}

// EnableMainnetCanary binds the one-action grant to the exact action named by
// the reviewed readiness receipt. The submitter operator socket rejects this
// shape for Devnet policies.
func (c *Client) EnableMainnetCanary(
	expectedRevision,
	actionID string,
	issuedAt,
	expiresAt time.Time,
	reason string,
) error {
	if c.config.ControlMode != control.ModeMainnetCanary {
		return errors.New("Mainnet canary enable requires a Mainnet submitter client")
	}
	response, err := c.controlRoundTrip(submittertransport.Request{
		Version: submittertransport.Version, Operation: submittertransport.OperationEnable,
		ExpectedRevision: expectedRevision, ActionID: actionID,
		IssuedAt: issuedAt, ExpiresAt: expiresAt, MaxActions: 1, Reason: reason,
	})
	if err != nil {
		return err
	}
	return validateEnableResponse(response)
}

func (c *Client) AcknowledgeTerminal(
	actionID,
	outcome,
	reason string,
) (control.Status, error) {
	response, err := c.controlRoundTrip(submittertransport.Request{
		Version:   submittertransport.Version,
		Operation: submittertransport.OperationAcknowledgeTerminal,
		ActionID:  actionID, Outcome: outcome, Reason: reason,
	})
	if err != nil {
		return control.Status{}, err
	}
	if response.Status != submittertransport.StatusOK || response.Control == nil ||
		response.Identity != nil || response.Submission != nil || response.ActionID != "" ||
		response.Outcome != "" || response.Revision != "" {
		return control.Status{}, errors.New("submitter operator acknowledgement response is invalid")
	}
	if err := c.validateControlStatus(*response.Control); err != nil {
		return control.Status{}, err
	}
	return *response.Control, nil
}

func (c *Client) validateControlStatus(status control.Status) error {
	if c.config.ControlMode == control.ModeMainnetCanary {
		return control.ValidateMainnetCanaryStatus(status)
	}
	return control.ValidateStatus(status)
}

func (c *Client) WithSendBarrier(actionID string, operation func() error) (bool, error) {
	if operation == nil {
		return false, errors.New("send barrier operation is required")
	}
	response, err := c.controlRoundTrip(submittertransport.Request{
		Version: submittertransport.Version, Operation: submittertransport.OperationReserve,
		ActionID: actionID,
	})
	if err != nil {
		return false, err
	}
	if response.Status == submittertransport.StatusRefused {
		if err := validateRefusalResponse(response); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := validateEmptyControlResponse(response); err != nil {
		return false, err
	}
	return false, operation()
}

func (c *Client) WithRecoverySendBarrier(actionID string, operation func() error) (bool, error) {
	if operation == nil {
		return false, errors.New("recovery send barrier operation is required")
	}
	response, err := c.controlRoundTrip(submittertransport.Request{
		Version: submittertransport.Version, Operation: submittertransport.OperationRecover,
		ActionID: actionID,
	})
	if err != nil {
		return false, err
	}
	if response.Status == submittertransport.StatusRefused {
		if err := validateRefusalResponse(response); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := validateEmptyControlResponse(response); err != nil {
		return false, err
	}
	return false, operation()
}

func validateEmptyControlResponse(response submittertransport.Response) error {
	if response.Status != submittertransport.StatusOK {
		return errors.New("submitter control response is invalid")
	}
	return validateEmptyResponseFields(response)
}

func validateEmptyResponseFields(response submittertransport.Response) error {
	if response.Identity != nil ||
		response.Submission != nil || response.Control != nil ||
		response.ActionID != "" || response.Outcome != "" || response.Revision != "" {
		return errors.New("submitter control response is invalid")
	}
	return nil
}

func (c *Client) controlRoundTrip(request submittertransport.Request) (submittertransport.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	return c.socketRoundTrip(ctx, request)
}

func validateRefusalResponse(response submittertransport.Response) error {
	if response.Identity != nil || response.Submission != nil || response.Control != nil ||
		response.ActionID != "" || response.Outcome != "" || response.Revision != "" {
		return errors.New("submitter refusal response is invalid")
	}
	return nil
}

func (c *Client) socketRoundTrip(
	ctx context.Context,
	request submittertransport.Request,
) (submittertransport.Response, error) {
	if err := validateSocket(c.config.SocketPath); err != nil {
		return submittertransport.Response{}, err
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > maxSocketRequestBytes {
		return submittertransport.Response{}, errors.New("encode submitter socket request")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", c.config.SocketPath)
	if err != nil {
		return submittertransport.Response{}, errors.New("connect to submitter service")
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return submittertransport.Response{}, errors.New("set submitter service deadline")
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = connection.SetDeadline(time.Now()) })
	defer stop()
	if _, err := io.Copy(connection, bytes.NewReader(append(encoded, '\n'))); err != nil {
		return submittertransport.Response{}, errors.New("write submitter service request")
	}
	unix, ok := connection.(*net.UnixConn)
	if !ok {
		return submittertransport.Response{}, errors.New("submitter service is not a Unix socket")
	}
	if err := unix.CloseWrite(); err != nil {
		return submittertransport.Response{}, errors.New("finish submitter service request")
	}
	data, err := io.ReadAll(io.LimitReader(connection, maxSocketResponseBytes+1))
	if err != nil {
		return submittertransport.Response{}, errors.New("read submitter service response")
	}
	if len(data) > maxSocketResponseBytes {
		return submittertransport.Response{}, errors.New("submitter service response exceeds size limit")
	}
	var response submittertransport.Response
	if err := strictjson.Decode(data, &response); err != nil {
		return submittertransport.Response{}, errors.New("decode submitter service response")
	}
	if response.Version != submittertransport.Version {
		return submittertransport.Response{}, errors.New("submitter service protocol version is invalid")
	}
	return response, nil
}

func validateSocket(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("submitter socket path must be absolute and clean")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return errors.New("submitter socket is unavailable")
	}
	if !fileowner.Trusted(info) || info.Mode().Perm()&0o007 != 0 {
		return errors.New("submitter socket is unsafe")
	}
	parentPath := filepath.Dir(path)
	parent, err := os.Lstat(parentPath)
	if err != nil || parent.Mode()&os.ModeSymlink != 0 || !parent.IsDir() ||
		!fileowner.Trusted(parent) || parent.Mode().Perm()&0o022 != 0 {
		return errors.New("submitter socket directory is unsafe")
	}
	if err := secureexec.ValidateProtectedDirectory(parentPath); err != nil {
		return errors.New("submitter socket directory is unsafe")
	}
	return nil
}

func decodeProcessFailure(data []byte) error {
	var failure struct {
		Error string `json:"error"`
	}
	if strictjson.Decode(data, &failure) == nil && failure.Error == "control_blocked" {
		return submitter.ErrControlBlocked
	}
	return errors.New("submitter process failed")
}

type limitedWriter struct {
	writer    io.Writer
	remaining int
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	originalLength := len(data)
	if w.remaining == 0 {
		return 0, errors.New("submitter output limit reached")
	}
	if len(data) > w.remaining {
		data = data[:w.remaining]
	}
	written, err := w.writer.Write(data)
	w.remaining -= written
	if err == nil && written < originalLength {
		return written, errors.New("submitter output limit reached")
	}
	return written, err
}
