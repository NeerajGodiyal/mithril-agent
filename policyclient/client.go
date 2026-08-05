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
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/signer"
)

const maxResponseBytes = 64 << 10

type Config struct {
	Command     string
	PolicyPath  string
	KeypairPath string
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
	if err := secureexec.ValidateExecutable(config.Command); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(config.PolicyPath) || !filepath.IsAbs(config.KeypairPath) {
		return nil, errors.New("risk policy and keypair paths must be absolute")
	}
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
	return &Client{config: config, publicKey: publicKey, now: time.Now}, nil
}

func (c *Client) Identity(ctx context.Context) (Identity, error) {
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
	if err := secureexec.ValidateExecutable(c.config.Command); err != nil {
		return riskgrant.Grant{}, err
	}
	request.RiskGrant = riskgrant.Grant{}
	input, err := json.Marshal(request)
	if err != nil {
		return riskgrant.Grant{}, errors.New("encode risk request")
	}
	command := exec.CommandContext(
		ctx,
		c.config.Command,
		"--policy", c.config.PolicyPath,
		"--keypair", c.config.KeypairPath,
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
