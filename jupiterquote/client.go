// Package jupiterquote reads executable prices and bounded, untrusted build
// proposals from Jupiter without signing or submitting a transaction.
package jupiterquote

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/bits"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	buildEndpoint      = "https://api.jup.ag/swap/v2/build"
	buildMaxAccounts   = 32
	maxResponseBytes   = 1 << 20
	maxInstructionData = 1232
	maxAddressTables   = 32
)

var ErrTemporarilyUnavailable = errors.New("Jupiter quote provider is temporarily unavailable")

type Request struct {
	Taker                   string `json:"taker"`
	InputMint               string `json:"input_mint"`
	OutputMint              string `json:"output_mint"`
	DestinationTokenAccount string `json:"destination_token_account,omitempty"`
	InputAmount             uint64 `json:"input_amount"`
	SlippageBPS             uint16 `json:"slippage_bps"`
}

type Result struct {
	InputAmount     uint64    `json:"input_amount"`
	EstimatedOutput uint64    `json:"estimated_output"`
	MinimumOutput   uint64    `json:"minimum_output"`
	ReceivedAt      time.Time `json:"-"`
	ResponseSHA256  string    `json:"-"`
}

// Validate requires the quote values to be the exact deterministic result of
// the request. This is also used after portable candidate decoding so a report
// cannot claim a stronger output floor than the transaction enforces.
func (result Result) Validate(request Request) error {
	if request.InputAmount == 0 || request.SlippageBPS == 0 || request.SlippageBPS > 500 ||
		request.InputMint == request.OutputMint {
		return errors.New("Jupiter quote request limits are invalid")
	}
	for _, value := range []string{request.Taker, request.InputMint, request.OutputMint} {
		if _, err := solana.Decode32(value); err != nil {
			return errors.New("Jupiter quote request address is invalid")
		}
	}
	if request.DestinationTokenAccount != "" {
		if _, err := solana.Decode32(request.DestinationTokenAccount); err != nil {
			return errors.New("Jupiter quote destination token account is invalid")
		}
	}
	if result.InputAmount != request.InputAmount || result.EstimatedOutput == 0 ||
		result.MinimumOutput == 0 || result.MinimumOutput > result.EstimatedOutput {
		return errors.New("Jupiter quote values do not match request")
	}
	high, low := bits.Mul64(result.EstimatedOutput, uint64(10_000-request.SlippageBPS))
	wantMinimum, remainder := bits.Div64(high, low, 10_000)
	if remainder != 0 {
		wantMinimum++
	}
	if result.MinimumOutput != wantMinimum {
		return errors.New("Jupiter quote minimum output does not match slippage")
	}
	return nil
}

// BuildResult is Jupiter's untrusted transaction proposal after bounded wire
// decoding. ClaimedAddressTables must be compared with independently sourced
// on-chain table contents before this proposal can be compiled or signed.
type BuildResult struct {
	Quote                Result
	ComputeBudget        []solana.Instruction
	Instructions         []solana.Instruction
	ClaimedAddressTables map[[32]byte][][32]byte
	RecentBlockhash      [32]byte
	LastValidBlockHeight uint64
}

// VerifyAddressTables requires the proposal's table contents to match an
// independently sourced view before the proposal is used for compilation.
func (result BuildResult) VerifyAddressTables(independent map[[32]byte][][32]byte) error {
	if len(result.ClaimedAddressTables) != len(independent) {
		return errors.New("Jupiter address tables do not match independent evidence")
	}
	for table, claimed := range result.ClaimedAddressTables {
		observed, ok := independent[table]
		if !ok || len(claimed) != len(observed) {
			return errors.New("Jupiter address tables do not match independent evidence")
		}
		for index := range claimed {
			if claimed[index] != observed[index] {
				return errors.New("Jupiter address tables do not match independent evidence")
			}
		}
	}
	return nil
}

type Client struct {
	httpClient *http.Client
	endpoint   string
	apiKey     string
}

// New returns a read-only client pinned to Jupiter's Swap V2 build endpoint.
// The endpoint is intentionally not configurable: accepting an arbitrary URL
// here would turn a credential-bearing request into an SSRF primitive. An
// empty API key uses Jupiter's lower-rate keyless access.
func New(apiKey string) (*Client, error) {
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default HTTP transport is unavailable")
	}
	transport := defaultTransport.Clone()
	// A protected service should not send its credential through an
	// ambient HTTPS_PROXY inherited from the host.
	transport.Proxy = nil
	return newClient(buildEndpoint, apiKey, &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
	})
}

func newClient(endpoint, apiKey string, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(apiKey) != apiKey ||
		strings.ContainsAny(apiKey, "\r\n") {
		return nil, errors.New("Jupiter API key must be valid when provided")
	}
	if httpClient == nil {
		return nil, errors.New("Jupiter HTTP client is required")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, errors.New("Jupiter build endpoint is invalid")
	}
	client := *httpClient
	// Never forward the API key to a redirect target. x-api-key is not one of
	// net/http's built-in sensitive redirect headers.
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{httpClient: &client, endpoint: endpoint, apiKey: apiKey}, nil
}

func (c *Client) Quote(ctx context.Context, request Request) (Result, error) {
	data, err := c.fetch(ctx, request)
	if err != nil {
		return Result{}, err
	}
	result, err := decodeResult(data, request)
	if err != nil {
		return Result{}, err
	}
	return stampResult(result, data), nil
}

// Build fetches and bounds a Jupiter transaction proposal without compiling,
// signing, simulating, or submitting it.
func (c *Client) Build(ctx context.Context, request Request) (BuildResult, error) {
	data, err := c.fetch(ctx, request)
	if err != nil {
		return BuildResult{}, err
	}
	result, err := decodeBuildResult(data, request)
	if err != nil {
		return BuildResult{}, err
	}
	result.Quote = stampResult(result.Quote, data)
	return result, nil
}

func (c *Client) fetch(ctx context.Context, request Request) ([]byte, error) {
	if request.InputAmount == 0 || request.SlippageBPS == 0 || request.SlippageBPS > 500 {
		return nil, errors.New("Jupiter quote request limits are invalid")
	}
	for name, value := range map[string]string{
		"taker": request.Taker, "input mint": request.InputMint, "output mint": request.OutputMint,
	} {
		if _, err := solana.Decode32(value); err != nil {
			return nil, errors.New(name + " is invalid")
		}
	}
	if request.InputMint == request.OutputMint {
		return nil, errors.New("Jupiter quote mints must differ")
	}
	if request.DestinationTokenAccount != "" {
		if _, err := solana.Decode32(request.DestinationTokenAccount); err != nil {
			return nil, errors.New("destination token account is invalid")
		}
	}

	endpoint, err := url.Parse(c.endpoint)
	if err != nil {
		return nil, errors.New("build Jupiter quote request")
	}
	query := endpoint.Query()
	query.Set("inputMint", request.InputMint)
	query.Set("outputMint", request.OutputMint)
	query.Set("amount", strconv.FormatUint(request.InputAmount, 10))
	query.Set("taker", request.Taker)
	query.Set("slippageBps", strconv.FormatUint(uint64(request.SlippageBPS), 10))
	// Keep proposals small enough for the signer policy to inspect the fixed
	// route accounts directly. The compiler and semantic validator remain the
	// final fail-closed boundary if a response still cannot fit.
	query.Set("maxAccounts", strconv.Itoa(buildMaxAccounts))
	query.Set("blockhashSlotsToExpiry", "150")
	query.Set("wrapAndUnwrapSol", "true")
	if request.DestinationTokenAccount != "" {
		query.Set("destinationTokenAccount", request.DestinationTokenAccount)
	}
	endpoint.RawQuery = query.Encode()

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, errors.New("build Jupiter quote request")
	}
	httpRequest.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		httpRequest.Header.Set("x-api-key", c.apiKey)
	}
	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return nil, ErrTemporarilyUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
		return nil, ErrTemporarilyUnavailable
	}
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("Jupiter quote request was refused")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, errors.New("read Jupiter quote response")
	}
	if len(data) > maxResponseBytes {
		return nil, errors.New("Jupiter quote response exceeds size limit")
	}
	return data, nil
}

type wireResult struct {
	InputMint            string              `json:"inputMint"`
	OutputMint           string              `json:"outputMint"`
	InAmount             string              `json:"inAmount"`
	OutAmount            string              `json:"outAmount"`
	OtherAmountThreshold string              `json:"otherAmountThreshold"`
	SwapMode             string              `json:"swapMode"`
	SlippageBPS          uint16              `json:"slippageBps"`
	RoutePlan            []wireRoute         `json:"routePlan"`
	ComputeBudget        []wireInstruction   `json:"computeBudgetInstructions"`
	Setup                []wireInstruction   `json:"setupInstructions"`
	Swap                 wireInstruction     `json:"swapInstruction"`
	Cleanup              *wireInstruction    `json:"cleanupInstruction"`
	Other                []wireInstruction   `json:"otherInstructions"`
	Tip                  *wireInstruction    `json:"tipInstruction"`
	AddressTables        map[string][]string `json:"addressesByLookupTableAddress"`
	Blockhash            wireBlockhash       `json:"blockhashWithMetadata"`
}

type wireRoute struct {
	BPS uint16 `json:"bps"`
}

type wireInstruction struct {
	ProgramID string        `json:"programId"`
	Accounts  []wireAccount `json:"accounts"`
	Data      string        `json:"data"`
}

type wireAccount struct {
	PublicKey string `json:"pubkey"`
	Signer    bool   `json:"isSigner"`
	Writable  bool   `json:"isWritable"`
}

type wireBlockhash struct {
	Bytes                []int  `json:"blockhash"`
	LastValidBlockHeight uint64 `json:"lastValidBlockHeight"`
}

func decodeResult(data []byte, request Request) (Result, error) {
	if err := strictjson.Validate(data); err != nil {
		return Result{}, errors.New("decode Jupiter quote response")
	}
	var response wireResult
	if err := json.Unmarshal(data, &response); err != nil {
		return Result{}, errors.New("decode Jupiter quote response")
	}
	if response.InputMint != request.InputMint || response.OutputMint != request.OutputMint ||
		response.SwapMode != "ExactIn" || response.SlippageBPS != request.SlippageBPS ||
		len(response.RoutePlan) == 0 || len(response.RoutePlan) > 64 {
		return Result{}, errors.New("Jupiter quote response does not match request")
	}
	// routePlan.bps is reporting metadata, not a transaction safety boundary.
	// Jupiter can round split allocations; the decoded instructions and output
	// floor are validated independently before a proposal can be signed.
	input, err := strconv.ParseUint(response.InAmount, 10, 64)
	if err != nil || input != request.InputAmount {
		return Result{}, errors.New("Jupiter quote input amount does not match request")
	}
	estimated, err := strconv.ParseUint(response.OutAmount, 10, 64)
	if err != nil {
		return Result{}, errors.New("Jupiter quote estimated output is invalid")
	}
	minimum, err := strconv.ParseUint(response.OtherAmountThreshold, 10, 64)
	if err != nil {
		return Result{}, errors.New("Jupiter quote minimum output is invalid")
	}
	result := Result{InputAmount: input, EstimatedOutput: estimated, MinimumOutput: minimum}
	if err := result.Validate(request); err != nil {
		return Result{}, err
	}
	return result, nil
}

func stampResult(result Result, data []byte) Result {
	hash := sha256.Sum256(data)
	result.ReceivedAt = time.Now().UTC()
	result.ResponseSHA256 = hex.EncodeToString(hash[:])
	return result
}

func decodeBuildResult(data []byte, request Request) (BuildResult, error) {
	quote, err := decodeResult(data, request)
	if err != nil {
		return BuildResult{}, err
	}
	var response wireResult
	if err := json.Unmarshal(data, &response); err != nil {
		return BuildResult{}, errors.New("decode Jupiter build response")
	}
	wireInstructions := make([]wireInstruction, 0, len(response.Setup)+len(response.Other)+3)
	wireInstructions = append(wireInstructions, response.Setup...)
	wireInstructions = append(wireInstructions, response.Swap)
	if response.Cleanup != nil {
		wireInstructions = append(wireInstructions, *response.Cleanup)
	}
	wireInstructions = append(wireInstructions, response.Other...)
	if response.Tip != nil {
		return BuildResult{}, errors.New("Jupiter build returned an unrequested tip")
	}
	if len(wireInstructions) == 0 || len(wireInstructions)+len(response.ComputeBudget) > 64 {
		return BuildResult{}, errors.New("Jupiter build instruction count is invalid")
	}
	if len(response.ComputeBudget) != 1 {
		return BuildResult{}, errors.New("Jupiter compute budget instruction is invalid")
	}
	computeBudget := make([]solana.Instruction, len(response.ComputeBudget))
	for index, wire := range response.ComputeBudget {
		instruction, err := decodeInstruction(wire, request.Taker)
		// /build promises one SetComputeUnitPrice instruction: tag 3 and a
		// little-endian uint64, with no accounts.
		if err != nil || instruction.Program != solana.ComputeBudgetProgram ||
			len(instruction.Accounts) != 0 || len(instruction.Data) != 9 ||
			instruction.Data[0] != 3 {
			return BuildResult{}, errors.New("Jupiter compute budget instruction is invalid")
		}
		computeBudget[index] = instruction
	}
	instructions := make([]solana.Instruction, len(wireInstructions))
	for index, wire := range wireInstructions {
		instruction, err := decodeInstruction(wire, request.Taker)
		if err != nil || instruction.Program == solana.ComputeBudgetProgram {
			return BuildResult{}, errors.New("Jupiter build instruction is invalid")
		}
		instructions[index] = instruction
	}
	tables, err := decodeAddressTables(response.AddressTables)
	if err != nil {
		return BuildResult{}, err
	}
	if len(response.Blockhash.Bytes) != 32 || response.Blockhash.LastValidBlockHeight == 0 {
		return BuildResult{}, errors.New("Jupiter build blockhash metadata is invalid")
	}
	var blockhash [32]byte
	for index, value := range response.Blockhash.Bytes {
		if value < 0 || value > 255 {
			return BuildResult{}, errors.New("Jupiter build blockhash metadata is invalid")
		}
		blockhash[index] = byte(value)
	}
	return BuildResult{
		Quote: quote, ComputeBudget: computeBudget,
		Instructions: instructions, ClaimedAddressTables: tables,
		RecentBlockhash: blockhash, LastValidBlockHeight: response.Blockhash.LastValidBlockHeight,
	}, nil
}

func decodeInstruction(wire wireInstruction, taker string) (solana.Instruction, error) {
	if _, err := solana.Decode32(wire.ProgramID); err != nil || len(wire.Accounts) > 255 {
		return solana.Instruction{}, errors.New("invalid instruction")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(wire.Data)
	if err != nil || len(data) > maxInstructionData || wire.ProgramID == taker {
		return solana.Instruction{}, errors.New("invalid instruction")
	}
	accounts := make([]solana.AccountMeta, len(wire.Accounts))
	for index, account := range wire.Accounts {
		if _, err := solana.Decode32(account.PublicKey); err != nil ||
			(account.Signer && account.PublicKey != taker) {
			return solana.Instruction{}, errors.New("invalid instruction")
		}
		accounts[index] = solana.AccountMeta{
			Address: account.PublicKey, Signer: account.Signer, Writable: account.Writable,
		}
	}
	return solana.Instruction{Program: wire.ProgramID, Accounts: accounts, Data: data}, nil
}

func decodeAddressTables(wire map[string][]string) (map[[32]byte][][32]byte, error) {
	if len(wire) > maxAddressTables {
		return nil, errors.New("Jupiter build has too many address tables")
	}
	tables := make(map[[32]byte][][32]byte, len(wire))
	for address, wireAccounts := range wire {
		table, err := solana.Decode32(address)
		if err != nil || len(wireAccounts) == 0 || len(wireAccounts) > 256 {
			return nil, errors.New("Jupiter build address table is invalid")
		}
		accounts := make([][32]byte, len(wireAccounts))
		for index, account := range wireAccounts {
			accounts[index], err = solana.Decode32(account)
			if err != nil {
				return nil, errors.New("Jupiter build address table is invalid")
			}
		}
		tables[table] = accounts
	}
	return tables, nil
}
