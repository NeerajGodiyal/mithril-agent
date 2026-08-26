package policyclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/operatorapproval"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/signer"
)

const (
	maxResponseBytes       = 64 << 10
	maxSocketRequestBytes  = signer.MaxRequestBytes + 1024
	maxSocketResponseBytes = maxResponseBytes + 1024
	operationTimeout       = 30 * time.Second

	SocketVersion            = 2
	SocketOperationIdentity  = "identity"
	SocketOperationAuthorize = "authorize"
	SocketStatusOK           = "ok"
	SocketStatusFailed       = "failed"
)

type SocketRequest struct {
	Version   uint32                     `json:"version"`
	Operation string                     `json:"operation"`
	Authorize *signer.Request            `json:"authorize_request,omitempty"`
	Approval  *operatorapproval.Approval `json:"operator_approval,omitempty"`
}

// ApprovedRequest is the foreground policy-process protocol for one Mainnet
// exact request and its detached operator approval.
type ApprovedRequest struct {
	Request  signer.Request            `json:"request"`
	Approval operatorapproval.Approval `json:"operator_approval"`
}

type SocketResponse struct {
	Version  uint32           `json:"version"`
	Status   string           `json:"status"`
	Identity *Identity        `json:"identity,omitempty"`
	Grant    *riskgrant.Grant `json:"grant,omitempty"`
}

type Config struct {
	Command     string
	PolicyPath  string
	KeypairPath string
	SocketPath  string
	KeyID       string
	PublicKey   string
	Env         []string
}

type Client struct {
	config    Config
	publicKey []byte
	now       func() time.Time
}

type Identity struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
}

func New(config Config) (*Client, error) {
	if config.KeyID == "" {
		return nil, errors.New("risk authority key ID is required")
	}
	publicKey, err := riskgrant.DecodePublicKey(config.PublicKey)
	if err != nil {
		return nil, err
	}
	if err := secureexec.ValidateEnvironment(config.Env); err != nil {
		return nil, err
	}
	config.Env = append([]string(nil), config.Env...)
	if config.SocketPath != "" {
		if !filepath.IsAbs(config.SocketPath) || filepath.Clean(config.SocketPath) != config.SocketPath {
			return nil, errors.New("risk authority socket path must be absolute and clean")
		}
		return &Client{config: config, publicKey: publicKey, now: time.Now}, nil
	}
	if err := secureexec.ValidateExecutable(config.Command); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(config.PolicyPath) || !filepath.IsAbs(config.KeypairPath) {
		return nil, errors.New("risk policy and keypair paths must be absolute")
	}
	return &Client{config: config, publicKey: publicKey, now: time.Now}, nil
}

func (c *Client) Identity(ctx context.Context) (Identity, error) {
	ctx, cancel, err := boundedOperationContext(ctx)
	if err != nil {
		return Identity{}, err
	}
	defer cancel()
	if c.config.SocketPath != "" {
		response, err := c.socketRoundTrip(ctx, SocketRequest{
			Version: SocketVersion, Operation: SocketOperationIdentity,
		})
		if err != nil {
			return Identity{}, err
		}
		if response.Status != SocketStatusOK || response.Identity == nil || response.Grant != nil ||
			response.Identity.KeyID != c.config.KeyID ||
			response.Identity.PublicKey != c.config.PublicKey {
			return Identity{}, errors.New("risk authority identity does not match configuration")
		}
		return *response.Identity, nil
	}
	if err := secureexec.ValidateExecutable(c.config.Command); err != nil {
		return Identity{}, err
	}
	command := exec.CommandContext(
		ctx,
		c.config.Command,
		"--policy", c.config.PolicyPath,
		"--keypair", c.config.KeypairPath,
		"--identity",
	)
	command.Env = secureexec.MinimalEnvironment(c.config.Env)
	command.Stdin = bytes.NewReader(nil)
	command.Stderr = &secureexec.DiscardCounter{}
	var output bytes.Buffer
	command.Stdout = &limitedWriter{writer: &output, remaining: maxResponseBytes + 1}
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		return Identity{}, errors.New("risk authority identity check failed")
	}
	if output.Len() > maxResponseBytes {
		return Identity{}, errors.New("risk authority identity response exceeds size limit")
	}
	var identity Identity
	if err := strictjson.Decode(output.Bytes(), &identity); err != nil ||
		identity.KeyID != c.config.KeyID || identity.PublicKey != c.config.PublicKey {
		return Identity{}, errors.New("risk authority identity does not match configuration")
	}
	return identity, nil
}

func (c *Client) Authorize(
	ctx context.Context,
	request signer.Request,
) (riskgrant.Grant, error) {
	return c.authorize(ctx, request, nil)
}

// AuthorizeApproved requests a grant for one exact operator-approved Mainnet
// action. The detached approval never enters the transaction signer request.
func (c *Client) AuthorizeApproved(
	ctx context.Context,
	request signer.Request,
	approval operatorapproval.Approval,
) (riskgrant.Grant, error) {
	return c.authorize(ctx, request, &approval)
}

func (c *Client) authorize(
	ctx context.Context,
	request signer.Request,
	approval *operatorapproval.Approval,
) (riskgrant.Grant, error) {
	ctx, cancel, err := boundedOperationContext(ctx)
	if err != nil {
		return riskgrant.Grant{}, err
	}
	defer cancel()
	request.RiskGrant = riskgrant.Grant{}
	if c.config.SocketPath != "" {
		response, err := c.socketRoundTrip(ctx, SocketRequest{
			Version: SocketVersion, Operation: SocketOperationAuthorize,
			Authorize: &request, Approval: approval,
		})
		if err != nil {
			return riskgrant.Grant{}, err
		}
		if response.Status == SocketStatusFailed {
			if response.Identity != nil || response.Grant != nil {
				return riskgrant.Grant{}, errors.New("risk authority failure response is invalid")
			}
			return riskgrant.Grant{}, errors.New("risk authority service failed")
		}
		if response.Status != SocketStatusOK || response.Identity != nil || response.Grant == nil {
			return riskgrant.Grant{}, errors.New("risk authority response is invalid")
		}
		if err := c.VerifyAt(request, *response.Grant, c.now().UTC()); err != nil {
			return riskgrant.Grant{}, err
		}
		return *response.Grant, nil
	}
	if err := secureexec.ValidateExecutable(c.config.Command); err != nil {
		return riskgrant.Grant{}, err
	}
	var input []byte
	if approval == nil {
		input, err = json.Marshal(request)
	} else {
		input, err = json.Marshal(ApprovedRequest{Request: request, Approval: *approval})
	}
	if err != nil || len(input) > maxSocketRequestBytes {
		return riskgrant.Grant{}, errors.New("encode risk request")
	}
	commandArgs := []string{
		"--policy", c.config.PolicyPath,
		"--keypair", c.config.KeypairPath,
	}
	if approval != nil {
		commandArgs = append(commandArgs, "--approved-request")
	}
	command := exec.CommandContext(
		ctx,
		c.config.Command,
		commandArgs...,
	)
	command.Env = secureexec.MinimalEnvironment(c.config.Env)
	command.Stdin = bytes.NewReader(input)
	stderr := &secureexec.DiscardCounter{}
	command.Stderr = stderr
	var output bytes.Buffer
	command.Stdout = &limitedWriter{writer: &output, remaining: maxResponseBytes + 1}
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		return riskgrant.Grant{}, errors.New("risk authority process failed")
	}
	if output.Len() > maxResponseBytes {
		return riskgrant.Grant{}, errors.New("risk authority response exceeds size limit")
	}
	var grant riskgrant.Grant
	if err := strictjson.Decode(output.Bytes(), &grant); err != nil {
		return riskgrant.Grant{}, errors.New("decode risk authority response")
	}
	if err := c.VerifyAt(request, grant, c.now().UTC()); err != nil {
		return riskgrant.Grant{}, err
	}
	return grant, nil
}

func boundedOperationContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, errors.New("risk authority operation context is unavailable")
	}
	bounded, cancel := context.WithTimeout(ctx, operationTimeout)
	return bounded, cancel, nil
}

func (c *Client) socketRoundTrip(ctx context.Context, request SocketRequest) (SocketResponse, error) {
	if err := validateSocket(c.config.SocketPath); err != nil {
		return SocketResponse{}, err
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > maxSocketRequestBytes {
		return SocketResponse{}, errors.New("encode risk authority socket request")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", c.config.SocketPath)
	if err != nil {
		return SocketResponse{}, errors.New("connect to risk authority service")
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return SocketResponse{}, errors.New("set risk authority service deadline")
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = connection.SetDeadline(time.Now()) })
	defer stop()
	if _, err := io.Copy(connection, bytes.NewReader(append(encoded, '\n'))); err != nil {
		return SocketResponse{}, errors.New("write risk authority service request")
	}
	unix, ok := connection.(*net.UnixConn)
	if !ok {
		return SocketResponse{}, errors.New("risk authority service is not a Unix socket")
	}
	if err := unix.CloseWrite(); err != nil {
		return SocketResponse{}, errors.New("finish risk authority service request")
	}
	data, err := io.ReadAll(io.LimitReader(connection, maxSocketResponseBytes+1))
	if err != nil {
		return SocketResponse{}, errors.New("read risk authority service response")
	}
	if len(data) > maxSocketResponseBytes {
		return SocketResponse{}, errors.New("risk authority service response exceeds size limit")
	}
	var response SocketResponse
	if err := strictjson.Decode(data, &response); err != nil {
		return SocketResponse{}, errors.New("decode risk authority service response")
	}
	if response.Version != SocketVersion {
		return SocketResponse{}, errors.New("risk authority service protocol version is invalid")
	}
	return response, nil
}

func validateSocket(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("risk authority socket path must be absolute and clean")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return errors.New("risk authority socket is unavailable")
	}
	if !fileowner.Trusted(info) || info.Mode().Perm()&0o007 != 0 {
		return errors.New("risk authority socket is unsafe")
	}
	parentPath := filepath.Dir(path)
	parent, err := os.Lstat(parentPath)
	if err != nil || parent.Mode()&os.ModeSymlink != 0 || !parent.IsDir() ||
		!fileowner.Trusted(parent) || parent.Mode().Perm()&0o022 != 0 {
		return errors.New("risk authority socket directory is unsafe")
	}
	if err := secureexec.ValidateProtectedDirectory(parentPath); err != nil {
		return errors.New("risk authority socket directory is unsafe")
	}
	return nil
}

func (c *Client) VerifyAt(
	request signer.Request,
	grant riskgrant.Grant,
	at time.Time,
) error {
	message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
	if err != nil {
		return errors.New("risk request message is not canonical base64")
	}
	digest := sha256.Sum256(message)
	binding, err := signer.RiskBinding(request, hex.EncodeToString(digest[:]))
	if err != nil {
		return err
	}
	return riskgrant.Verify(
		c.publicKey,
		c.config.KeyID,
		binding,
		grant,
		at.UTC(),
	)
}

type limitedWriter struct {
	writer    io.Writer
	remaining int
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	originalLength := len(data)
	if w.remaining == 0 {
		return 0, errors.New("risk authority output limit reached")
	}
	if len(data) > w.remaining {
		data = data[:w.remaining]
	}
	written, err := w.writer.Write(data)
	w.remaining -= written
	if err == nil && written < originalLength {
		return written, errors.New("risk authority output limit reached")
	}
	return written, err
}
