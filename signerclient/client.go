package signerclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
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
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const maxResponseBytes = 64 << 10

type Config struct {
	Command     string
	PolicyPath  string
	KeypairPath string
	Env         []string
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
	if !filepath.IsAbs(config.PolicyPath) || !filepath.IsAbs(config.KeypairPath) {
		return nil, errors.New("signer policy and keypair paths must be absolute")
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
		return Identity{}, errors.New("signer identity check failed")
	}
	if output.Len() > maxResponseBytes {
		return Identity{}, errors.New("signer identity response exceeds size limit")
	}
	var identity Identity
	if err := strictjson.Decode(output.Bytes(), &identity); err != nil {
		return Identity{}, errors.New("decode signer identity")
	}
	if _, err := solana.Decode32(identity.PublicKey); err != nil {
		return Identity{}, errors.New("signer identity is invalid")
	}
	return identity, nil
}

func (c *Client) Sign(ctx context.Context, request signer.Request) (signer.Response, error) {
	if err := secureexec.ValidateExecutable(c.config.Command); err != nil {
		return signer.Response{}, err
	}
	input, err := json.Marshal(request)
	if err != nil {
		return signer.Response{}, errors.New("encode signer request")
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
	var stdout bytes.Buffer
	command.Stdout = &limitedWriter{writer: &stdout, remaining: maxResponseBytes + 1}
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		return signer.Response{}, errors.New("signer process failed")
	}
	if stdout.Len() > maxResponseBytes {
		return signer.Response{}, errors.New("signer response exceeds size limit")
	}
	response, err := decodeResponse(stdout.Bytes())
	if err != nil {
		return signer.Response{}, err
	}
	if err := validateResponse(request, response); err != nil {
		return signer.Response{}, err
	}
	return response, nil
}

func decodeResponse(data []byte) (signer.Response, error) {
	var response signer.Response
	if err := strictjson.Decode(data, &response); err != nil {
		return signer.Response{}, errors.New("decode signer response")
	}
	return response, nil
}

func validateResponse(request signer.Request, response signer.Response) error {
	if response.ActionID != request.ActionID ||
		response.BlockhashContextSlot != request.BlockhashContextSlot ||
		response.LastValidBlockHeight != request.LastValidBlockHeight ||
		response.FeeLamports != request.FeeLamports {
		return errors.New("signer response does not match request identity")
	}
	message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
	if err != nil {
		return errors.New("request message is not canonical base64")
	}
	messageHash := sha256.Sum256(message)
	if response.MessageSHA256 != hex.EncodeToString(messageHash[:]) {
		return errors.New("signer response has the wrong message hash")
	}
	transactionHash, err := hex.DecodeString(response.TransactionSHA256)
	if err != nil || len(transactionHash) != sha256.Size ||
		hex.EncodeToString(transactionHash) != response.TransactionSHA256 {
		return errors.New("signer transaction hash is invalid")
	}
	signature, err := solana.Decode64(response.Signature)
	if err != nil {
		return errors.New("signer signature is invalid")
	}
	decoded, err := solana.DecodeLegacyMessage(message)
	if err != nil || len(decoded.AccountKeys) == 0 ||
		!ed25519.Verify(ed25519.PublicKey(decoded.AccountKeys[0][:]), message, signature[:]) {
		return errors.New("signer signature does not match its message")
	}
	metadata := sealedtx.Metadata{
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
	if response.SealedTransaction.Metadata != metadata ||
		response.SealedTransaction.EphemeralPublicKeyBase64 == "" ||
		response.SealedTransaction.NonceBase64 == "" ||
		response.SealedTransaction.CiphertextBase64 == "" {
		return errors.New("sealed signer transaction does not match response")
	}
	if err := signer.VerifyResponseAttestation(
		solana.Encode(decoded.AccountKeys[0][:]),
		response.SignerAttestation.SubmitterPublicKey,
		response,
	); err != nil {
		return err
	}
	return nil
}

type limitedWriter struct {
	writer    io.Writer
	remaining int
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	originalLength := len(data)
	if w.remaining == 0 {
		return 0, errors.New("signer output limit reached")
	}
	if len(data) > w.remaining {
		data = data[:w.remaining]
	}
	n, err := w.writer.Write(data)
	w.remaining -= n
	if err == nil && n < originalLength {
		return n, errors.New("signer output limit reached")
	}
	return n, err
}
