package submitterclient

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

const maxResponseBytes = 16 << 10

type Config struct {
	Command        string
	PolicyPath     string
	PrivateKeyPath string
	Env            []string
}

type Request struct {
	SignerResponse signer.Response `json:"signer_response"`
	MinContextSlot uint64          `json:"min_context_slot"`
}

type Client struct {
	config Config
}

type Identity struct {
	PublicKey string `json:"public_key"`
}

func New(config Config) (*Client, error) {
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
	if err := secureexec.ValidateExecutable(c.config.Command); err != nil {
		return txflow.Submission{}, err
	}
	if minContextSlot == 0 || response.BlockhashContextSlot != minContextSlot {
		return txflow.Submission{}, errors.New("submitter minimum context slot does not match signer response")
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
	if submission.Signature != response.Signature ||
		submission.LastValidBlockHeight != response.LastValidBlockHeight ||
		(submission.State != txflow.StateAccepted && submission.State != txflow.StateAmbiguous) {
		return txflow.Submission{}, errors.New("submitter response does not match request")
	}
	if _, err := solana.Decode64(submission.Signature); err != nil {
		return txflow.Submission{}, errors.New("submitter returned an invalid signature")
	}
	return submission, nil
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
