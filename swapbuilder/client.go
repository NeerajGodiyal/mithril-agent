package swapbuilder

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	maxBuilderRequestBytes  = 8 << 10
	maxBuilderResponseBytes = 64 << 10
	maxHealthResponseBytes  = 64
	maxInstructions         = 16
	maxAccounts             = 64
	maxInstructionData      = 512
)

var (
	errQuoteProcessFailed          = errors.New("Orca quote process failed")
	ErrQuoteTemporarilyUnavailable = errors.New("Orca quote provider is temporarily unavailable")
)

type Config struct {
	NodeCommand string
	ScriptPath  string
	RPCURL      string
	SocketPath  string
}

type Request struct {
	Owner       string
	Pool        string
	InputMint   string
	InputAmount uint64
	SlippageBPS uint16
}

type Result struct {
	Instructions         []solana.Instruction
	TokenIn              uint64
	TokenEstOut          uint64
	TokenMinOut          uint64
	TradeEnableTimestamp time.Time
}

type wireRequest struct {
	Owner       string `json:"owner"`
	Pool        string `json:"pool"`
	InputMint   string `json:"input_mint"`
	InputAmount string `json:"input_amount"`
	SlippageBPS uint16 `json:"slippage_bps"`
}

type wireInstruction struct {
	Program    string               `json:"program"`
	Accounts   []solana.AccountMeta `json:"accounts"`
	DataBase64 string               `json:"data_base64"`
}

type wireResult struct {
	Instructions         []wireInstruction `json:"instructions"`
	TokenIn              string            `json:"token_in"`
	TokenEstOut          string            `json:"token_est_out"`
	TokenMinOut          string            `json:"token_min_out"`
	TradeEnableTimestamp string            `json:"trade_enable_timestamp"`
}

type Client struct {
	config Config
}

func New(config Config) (*Client, error) {
	direct := config.NodeCommand != "" || config.ScriptPath != "" || config.RPCURL != ""
	socket := config.SocketPath != ""
	if direct == socket {
		return nil, errors.New("configure exactly one Orca quote transport")
	}
	if socket {
		if err := validateSocketConfig(config.SocketPath); err != nil {
			return nil, err
		}
		return &Client{config: config}, nil
	}
	if err := validateDirect(config); err != nil {
		return nil, err
	}
	return &Client{config: config}, nil
}

func validateSocketConfig(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("Orca quote socket must be an absolute clean path")
	}
	if _, err := os.Lstat(path); err == nil {
		return validateSocket(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("inspect Orca quote socket")
	}
	parent := filepath.Dir(path)
	if _, err := os.Lstat(parent); err == nil {
		if err := validateSocketDirectory(parent); err != nil {
			return errors.New("Orca quote socket directory is not protected")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("inspect Orca quote socket directory")
	}
	return nil
}

// Health checks local quote readiness without contacting an RPC provider.
func (c *Client) Health(ctx context.Context) error {
	if c.config.SocketPath == "" {
		if err := validateDirect(c.config); err != nil {
			return ErrQuoteTemporarilyUnavailable
		}
		return nil
	}
	if err := validateSocket(c.config.SocketPath); err != nil {
		return ErrQuoteTemporarilyUnavailable
	}
	response, closeTransport, err := c.socketRequest(ctx, http.MethodGet, "/health", nil, 2*time.Second)
	if err != nil {
		return ErrQuoteTemporarilyUnavailable
	}
	defer closeTransport()
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxHealthResponseBytes+1))
	if err != nil || len(data) > maxHealthResponseBytes || response.StatusCode != http.StatusOK {
		return ErrQuoteTemporarilyUnavailable
	}
	var health struct {
		Status string `json:"status"`
	}
	if strictjson.Decode(data, &health) != nil || health.Status != "ok" {
		return ErrQuoteTemporarilyUnavailable
	}
	return nil
}

// SelfTest loads the direct adapter and its pinned dependencies without a quote.
func (c *Client) SelfTest(ctx context.Context) error {
	if c.config.SocketPath != "" || validateDirect(c.config) != nil {
		return errors.New("direct Orca quote builder is required")
	}
	runContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(
		runContext,
		c.config.NodeCommand,
		"--conditions=node",
		c.config.ScriptPath,
		"--self-test",
	)
	command.Env = secureexec.MinimalEnvironment([]string{
		"MITHRIL_AGENT_QUOTE_RPC_URL=" + c.config.RPCURL,
	})
	command.Stderr = &secureexec.DiscardCounter{}
	var stdout bytes.Buffer
	command.Stdout = &limitedWriter{writer: &stdout, remaining: maxHealthResponseBytes + 1}
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil || stdout.Len() > maxHealthResponseBytes {
		return errors.New("Orca quote self-test failed")
	}
	var result struct {
		Status string `json:"status"`
	}
	if strictjson.Decode(stdout.Bytes(), &result) != nil || result.Status != "ok" {
		return errors.New("Orca quote self-test failed")
	}
	return nil
}

func (c *Client) Quote(ctx context.Context, request Request) (Result, error) {
	if request.InputAmount == 0 || request.SlippageBPS == 0 || request.SlippageBPS > 500 {
		return Result{}, errors.New("swap quote request limits are invalid")
	}
	for name, value := range map[string]string{
		"owner": request.Owner, "pool": request.Pool, "input mint": request.InputMint,
	} {
		if _, err := solana.Decode32(value); err != nil {
			return Result{}, errors.New(name + " is invalid")
		}
	}
	input, err := json.Marshal(wireRequest{
		Owner:       request.Owner,
		Pool:        request.Pool,
		InputMint:   request.InputMint,
		InputAmount: strconv.FormatUint(request.InputAmount, 10),
		SlippageBPS: request.SlippageBPS,
	})
	if err != nil {
		return Result{}, errors.New("encode swap quote request")
	}
	if c.config.SocketPath != "" {
		return c.quoteSocket(ctx, input, request)
	}
	runContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(runContext, c.config.NodeCommand,
		"--conditions=node", c.config.ScriptPath)
	command.Env = secureexec.MinimalEnvironment([]string{
		"MITHRIL_AGENT_QUOTE_RPC_URL=" + c.config.RPCURL,
	})
	command.Stdin = bytes.NewReader(input)
	command.Stderr = &secureexec.DiscardCounter{}
	var stdout bytes.Buffer
	command.Stdout = &limitedWriter{writer: &stdout, remaining: maxBuilderResponseBytes + 1}
	// Killing the child on context expiry does not end Run: it waits for the
	// output pipes to close, which a surviving grandchild holds open. Without
	// this the 15s bound is not enforced.
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		return Result{}, quoteProcessError(err)
	}
	if stdout.Len() > maxBuilderResponseBytes {
		return Result{}, errors.New("Orca quote response exceeds size limit")
	}
	return decodeResult(stdout.Bytes(), request)
}

func (c *Client) quoteSocket(
	ctx context.Context,
	input []byte,
	request Request,
) (Result, error) {
	if err := validateSocket(c.config.SocketPath); err != nil {
		return Result{}, ErrQuoteTemporarilyUnavailable
	}
	response, closeTransport, err := c.socketRequest(
		ctx, http.MethodPost, "/quote", bytes.NewReader(input), 15*time.Second,
	)
	if err != nil {
		return Result{}, ErrQuoteTemporarilyUnavailable
	}
	defer closeTransport()
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBuilderResponseBytes+1))
	if err != nil {
		return Result{}, errors.New("read Orca quote service response")
	}
	if len(data) > maxBuilderResponseBytes {
		return Result{}, errors.New("Orca quote response exceeds size limit")
	}
	if response.StatusCode == http.StatusServiceUnavailable {
		return Result{}, ErrQuoteTemporarilyUnavailable
	}
	if response.StatusCode != http.StatusOK {
		return Result{}, errors.New("Orca quote service failed")
	}
	return decodeResult(data, request)
}

func (c *Client) socketRequest(
	ctx context.Context,
	method string,
	path string,
	body io.Reader,
	timeout time.Duration,
) (*http.Response, func(), error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", c.config.SocketPath)
		},
		DisableKeepAlives: true,
	}
	httpClient := &http.Client{Transport: transport}
	runContext, cancel := context.WithTimeout(ctx, timeout)
	httpRequest, err := http.NewRequestWithContext(
		runContext,
		method,
		"http://quote.local"+path,
		body,
	)
	if err != nil {
		cancel()
		transport.CloseIdleConnections()
		return nil, func() {}, errors.New("create Orca quote service request")
	}
	if method == http.MethodPost {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	response, err := httpClient.Do(httpRequest)
	if err != nil {
		cancel()
		transport.CloseIdleConnections()
		return nil, func() {}, err
	}
	closeTransport := func() {
		cancel()
		transport.CloseIdleConnections()
	}
	return response, closeTransport, nil
}

func validateDirect(config Config) error {
	if err := secureexec.ValidateExecutable(config.NodeCommand); err != nil {
		return err
	}
	if err := secureexec.ValidateProtectedFile(config.ScriptPath); err != nil {
		return errors.New("Orca quote script must be a protected regular file")
	}
	endpoint, err := url.Parse(config.RPCURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" ||
		endpoint.User != nil || endpoint.Fragment != "" {
		return errors.New("Orca quote RPC URL is required")
	}
	return nil
}

func validateSocket(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("Orca quote socket must be an absolute clean path")
	}
	if err := validateSocketDirectory(filepath.Dir(path)); err != nil {
		return errors.New("Orca quote socket directory is not protected")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("inspect Orca quote socket")
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 ||
		info.Mode().Perm()&0o007 != 0 || !trustedSocketOwner(info) {
		return errors.New("Orca quote socket is not protected")
	}
	return nil
}

func validateSocketDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() ||
		info.Mode().Perm()&0o022 != 0 || !trustedSocketDirectoryOwner(info) {
		return errors.New("socket directory is unsafe")
	}
	return secureexec.ValidateProtectedDirectory(filepath.Dir(path))
}

func trustedSocketOwner(info os.FileInfo) bool {
	return fileowner.Trusted(info) ||
		(fileowner.TrustedGroup(info) && info.Mode().Perm()&0o060 == 0o060)
}

func trustedSocketDirectoryOwner(info os.FileInfo) bool {
	return fileowner.Trusted(info) ||
		(fileowner.TrustedGroup(info) && info.Mode().Perm()&0o010 == 0o010)
}

func quoteProcessError(err error) error {
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 75 {
		return ErrQuoteTemporarilyUnavailable
	}
	return errQuoteProcessFailed
}

func decodeResult(data []byte, request Request) (Result, error) {
	var wire wireResult
	if err := strictjson.Decode(data, &wire); err != nil {
		return Result{}, errors.New("decode Orca quote response")
	}
	if len(wire.Instructions) == 0 || len(wire.Instructions) > maxInstructions {
		return Result{}, errors.New("Orca quote instruction count is invalid")
	}
	tokenIn, err := strconv.ParseUint(wire.TokenIn, 10, 64)
	if err != nil || tokenIn != request.InputAmount {
		return Result{}, errors.New("Orca quote input amount does not match request")
	}
	minOut, err := strconv.ParseUint(wire.TokenMinOut, 10, 64)
	if err != nil || minOut == 0 {
		return Result{}, errors.New("Orca quote minimum output is invalid")
	}
	estOut, err := strconv.ParseUint(wire.TokenEstOut, 10, 64)
	if err != nil || estOut == 0 || minOut > estOut {
		return Result{}, errors.New("Orca quote estimated output is invalid")
	}
	tradeUnix, err := strconv.ParseInt(wire.TradeEnableTimestamp, 10, 64)
	if err != nil || tradeUnix < 0 {
		return Result{}, errors.New("Orca quote trade enable time is invalid")
	}
	result := Result{
		Instructions:         make([]solana.Instruction, len(wire.Instructions)),
		TokenIn:              tokenIn,
		TokenEstOut:          estOut,
		TokenMinOut:          minOut,
		TradeEnableTimestamp: time.Unix(tradeUnix, 0).UTC(),
	}
	for index, instruction := range wire.Instructions {
		if _, err := solana.Decode32(instruction.Program); err != nil ||
			len(instruction.Accounts) > maxAccounts {
			return Result{}, errors.New("Orca quote instruction is invalid")
		}
		decodedData, err := base64.StdEncoding.Strict().DecodeString(instruction.DataBase64)
		if err != nil || len(decodedData) > maxInstructionData {
			return Result{}, errors.New("Orca quote instruction data is invalid")
		}
		for _, account := range instruction.Accounts {
			if _, err := solana.Decode32(account.Address); err != nil {
				return Result{}, errors.New("Orca quote instruction account is invalid")
			}
			if account.Signer && account.Address != request.Owner {
				return Result{}, errors.New("Orca quote requires an unexpected signer")
			}
		}
		result.Instructions[index] = solana.Instruction{
			Program:  instruction.Program,
			Accounts: append([]solana.AccountMeta(nil), instruction.Accounts...),
			Data:     decodedData,
		}
	}
	return result, nil
}

type limitedWriter struct {
	writer    io.Writer
	remaining int
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	original := len(data)
	if w.remaining == 0 {
		return 0, errors.New("Orca quote output limit reached")
	}
	if len(data) > w.remaining {
		data = data[:w.remaining]
	}
	written, err := w.writer.Write(data)
	w.remaining -= written
	if err == nil && written < original {
		return written, errors.New("Orca quote output limit reached")
	}
	return written, err
}
