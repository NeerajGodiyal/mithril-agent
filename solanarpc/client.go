package solanarpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	maxResponseBytes      = 1 << 20
	maxAccountDataBytes   = 10 << 20
	maxSimulationLogLines = 256
	maxSimulationLogBytes = 64 << 10
	maxRPCAttempts        = 3
	initialRetryDelay     = time.Second
	maxRetryDelay         = 2 * time.Second
	maxRequestInterval    = 5 * time.Second
)

type Client struct {
	endpoint *url.URL
	http     *http.Client
	identity string
	origin   string
	mithril  bool
	nextID   atomic.Uint64
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
	paceMu   sync.Mutex
	paceNext time.Time
	pace     time.Duration
}

type LatestBlockhash struct {
	ContextSlot          uint64
	Blockhash            string
	LastValidBlockHeight uint64
}

// VerificationStatus is Mithril's replay-evidence state. A generic Solana RPC
// does not implement the method that returns it.
type VerificationStatus struct {
	State          string
	Required       bool
	VerifiedSlot   uint64
	EligibleSlot   uint64
	Healthy        bool
	EvidenceServed bool
	Reason         string
}

// RootedFeedStatus binds rooted-event publication to one AccountsDB lineage.
type RootedFeedStatus struct {
	Enabled             bool
	AccountsDBRootRunID string
}

type BlockhashValidity struct {
	ContextSlot uint64
	Valid       bool
}

type FeeQuote struct {
	ContextSlot uint64
	Lamports    uint64
}

type AccountQuote struct {
	ContextSlot uint64
	Lamports    uint64
	Owner       string
	Executable  bool
	DataLength  uint64
}

type AccountDataSlice struct {
	ContextSlot uint64
	Bankhash    string
	Owner       string
	Executable  bool
	DataLength  uint64
	Data        []byte
}

// AccountData reads one complete bounded account value. It is separate from
// AccountSlice because interface metadata can exceed the small evidence slices
// used by transaction checks. The caller chooses the smallest useful bound,
// up to Solana's 10 MiB account limit; the HTTP response cap follows that
// bound instead of always allowing the largest response.
func (c *Client) AccountData(
	ctx context.Context,
	address string,
	minContextSlot,
	maxDataBytes uint64,
) (AccountDataSlice, error) {
	if _, err := solana.Decode32(address); err != nil {
		return AccountDataSlice{}, errors.New("account address is invalid")
	}
	if minContextSlot == 0 || maxDataBytes == 0 || maxDataBytes > maxAccountDataBytes {
		return AccountDataSlice{}, errors.New("account data bound is invalid")
	}
	commitment := "confirmed"
	if c.mithril {
		commitment = "processed"
	}
	raw, err := c.callBounded(ctx, "getAccountInfo", []any{
		address,
		map[string]any{
			"commitment": commitment, "encoding": "base64", "minContextSlot": minContextSlot,
		},
	}, accountDataResponseLimit(maxDataBytes))
	if err != nil {
		return AccountDataSlice{}, err
	}
	var result struct {
		Context struct {
			Slot     uint64 `json:"slot"`
			Bankhash string `json:"bankhash"`
		} `json:"context"`
		Value *rpcAccountValue `json:"value"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return AccountDataSlice{}, errors.New("decode account data")
	}
	if result.Context.Slot < minContextSlot || result.Value == nil ||
		result.Value.Space > maxDataBytes {
		return AccountDataSlice{}, errors.New("account data is missing, stale, or exceeds its bound")
	}
	if c.mithril && !validProcessedBankhash(result.Context.Bankhash) {
		return AccountDataSlice{}, errors.New("Mithril account bank identity is missing or invalid")
	}
	if _, err := solana.Decode32(result.Value.Owner); err != nil {
		return AccountDataSlice{}, errors.New("account owner is invalid")
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(result.Value.Data, &parts); err != nil || len(parts) != 2 {
		return AccountDataSlice{}, errors.New("account data encoding is invalid")
	}
	var encoded, encoding string
	if err := json.Unmarshal(parts[0], &encoded); err != nil ||
		json.Unmarshal(parts[1], &encoding) != nil || encoding != "base64" {
		return AccountDataSlice{}, errors.New("account data encoding is invalid")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || uint64(len(data)) != result.Value.Space {
		return AccountDataSlice{}, errors.New("account data has an invalid length")
	}
	return AccountDataSlice{
		ContextSlot: result.Context.Slot, Bankhash: result.Context.Bankhash, Owner: result.Value.Owner,
		Executable: result.Value.Executable, DataLength: result.Value.Space, Data: data,
	}, nil
}

func accountDataResponseLimit(maxDataBytes uint64) int64 {
	base64Bytes := 4 * ((maxDataBytes + 2) / 3)
	return int64(base64Bytes + (64 << 10))
}

type AddressLookupTable struct {
	ContextSlot uint64
	Addresses   [][32]byte
}

type Simulation struct {
	ContextSlot             uint64
	UnitsConsumed           uint64
	SourcePostLamports      uint64
	DestinationPostLamports uint64
	LogsSHA256              string
	AccountsSHA256          string
}

type LegacySimulation struct {
	ContextSlot   uint64
	Bankhash      string
	UnitsConsumed uint64
	LogsSHA256    string
}

type rpcAccountValue struct {
	Data       json.RawMessage `json:"data"`
	Executable bool            `json:"executable"`
	Lamports   uint64          `json:"lamports"`
	Owner      string          `json:"owner"`
	Space      uint64          `json:"space"`
}

type TransactionEffect struct {
	Slot              uint64
	Transaction       []byte
	FeeLamports       uint64
	Failed            bool
	ErrorFingerprint  string
	PreBalances       []uint64
	PostBalances      []uint64
	PreTokenBalances  []TokenBalance
	PostTokenBalances []TokenBalance
}

type TokenBalance struct {
	AccountIndex uint16
	Mint         string
	Owner        string
	Amount       uint64
}

type SignatureStatus struct {
	Found              bool
	Slot               uint64
	ConfirmationStatus string
	Failed             bool
	ErrorFingerprint   string
}

func New(endpoint string, httpClient *http.Client, allowHTTP bool) (*Client, error) {
	return newClient(endpoint, httpClient, allowHTTP, false)
}

// NewPaced creates an HTTPS client that serializes requests at the configured
// minimum interval. It is intended for bounded evidence providers with strict
// request limits.
func NewPaced(endpoint string, httpClient *http.Client, interval time.Duration) (*Client, error) {
	if interval <= 0 || interval > maxRequestInterval {
		return nil, errors.New("RPC request interval must be greater than zero and at most five seconds")
	}
	client, err := newClient(endpoint, httpClient, false, false)
	if err != nil {
		return nil, err
	}
	client.pace = interval
	return client, nil
}

func NewMithrilNode(endpoint string, httpClient *http.Client) (*Client, error) {
	return newClient(endpoint, httpClient, true, true)
}

func newClient(
	endpoint string,
	httpClient *http.Client,
	allowHTTP,
	mithril bool,
) (*Client, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("RPC endpoint is invalid")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" {
		return nil, errors.New("RPC endpoint has no host")
	}
	if address, err := netip.ParseAddr(host); err == nil {
		host = address.Unmap().String()
	}
	if mithril {
		address, err := netip.ParseAddr(host)
		if err != nil || !address.IsLoopback() {
			return nil, errors.New("Mithril RPC endpoint must use a literal loopback IP")
		}
	}
	if parsed.Scheme == "http" {
		address, err := netip.ParseAddr(host)
		if !allowHTTP || err != nil || !address.IsLoopback() {
			return nil, errors.New("RPC endpoint must use HTTPS")
		}
	} else if parsed.Scheme != "https" {
		return nil, errors.New("RPC endpoint must use HTTPS")
	}
	if httpClient == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		if !mithril && !allowHTTP {
			transport.DialContext = externalRPCDialContext()
		}
		httpClient = &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
		}
	}
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	} else {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return nil, errors.New("RPC endpoint has an invalid port")
		}
		port = strconv.Itoa(portNumber)
	}
	origin := strings.ToLower(parsed.Scheme) + "://" +
		net.JoinHostPort(host, port)
	sum := sha256.Sum256([]byte(origin))
	return &Client{
		endpoint: parsed,
		http:     &clientCopy,
		identity: hex.EncodeToString(sum[:]),
		origin:   origin,
		mithril:  mithril,
		now:      time.Now,
		sleep:    sleepContext,
	}, nil
}

func (c *Client) Identity() string {
	return c.identity
}

func (c *Client) Origin() string {
	return c.origin
}

func (c *Client) GenesisHash(ctx context.Context) (string, error) {
	raw, err := c.call(ctx, "getGenesisHash", []any{})
	if err != nil {
		return "", err
	}
	var hash string
	if err := json.Unmarshal(raw, &hash); err != nil {
		return "", errors.New("decode genesis hash")
	}
	if err := solana.ValidateBase58(hash, 64); err != nil {
		return "", errors.New("genesis hash is invalid")
	}
	return hash, nil
}

// VerificationStatus returns Mithril's node-specific evidence status.
func (c *Client) VerificationStatus(ctx context.Context) (VerificationStatus, error) {
	if !c.mithril {
		return VerificationStatus{}, errors.New("verification status requires a Mithril node client")
	}
	raw, err := c.call(ctx, "getVerificationStatus", []any{})
	if err != nil {
		return VerificationStatus{}, err
	}
	var wire struct {
		State          *string `json:"state"`
		Required       *bool   `json:"required"`
		VerifiedSlot   *uint64 `json:"verifiedSlot"`
		EligibleSlot   *uint64 `json:"eligibleSlot"`
		Healthy        *bool   `json:"healthy"`
		EvidenceServed *bool   `json:"evidenceServed"`
		Reason         string  `json:"reason,omitempty"`
	}
	if err := strictjson.Decode(raw, &wire); err != nil || wire.State == nil ||
		wire.Required == nil || wire.VerifiedSlot == nil || wire.EligibleSlot == nil ||
		wire.Healthy == nil || wire.EvidenceServed == nil {
		return VerificationStatus{}, errors.New("Mithril verification status is invalid")
	}
	var open, healthy, required bool
	switch *wire.State {
	case "complete":
		open, healthy, required = true, true, true
	case "incomplete":
		open, required = true, true
	case "stalled", "diverged", "unavailable":
		required = true
	case "not_applicable":
		open, healthy = true, true
	default:
		return VerificationStatus{}, errors.New("Mithril verification status is invalid")
	}
	if *wire.EvidenceServed != open ||
		*wire.Healthy != healthy || *wire.Required != required ||
		*wire.EligibleSlot < *wire.VerifiedSlot ||
		(*wire.State == "complete" && *wire.VerifiedSlot != *wire.EligibleSlot) ||
		((*wire.State == "incomplete" || *wire.State == "stalled") && *wire.VerifiedSlot >= *wire.EligibleSlot) ||
		(open && wire.Reason != "") || (!open && wire.Reason != *wire.State) {
		return VerificationStatus{}, errors.New("Mithril verification status is inconsistent")
	}
	return VerificationStatus{
		State: *wire.State, Required: *wire.Required,
		VerifiedSlot: *wire.VerifiedSlot, EligibleSlot: *wire.EligibleSlot,
		Healthy: *wire.Healthy, EvidenceServed: *wire.EvidenceServed, Reason: wire.Reason,
	}, nil
}

// RootedFeedStatus returns the current node's immutable rooted-event source
// identity. Older nodes without the method fail closed for rooted workspaces.
func (c *Client) RootedFeedStatus(ctx context.Context) (RootedFeedStatus, error) {
	if !c.mithril {
		return RootedFeedStatus{}, errors.New("rooted feed status requires a Mithril node client")
	}
	raw, err := c.call(ctx, "getRootedFeedStatus", []any{})
	if err != nil {
		return RootedFeedStatus{}, err
	}
	var wire struct {
		Enabled             *bool  `json:"enabled"`
		AccountsDBRootRunID string `json:"accountsDbRootRunId,omitempty"`
	}
	if err := strictjson.Decode(raw, &wire); err != nil || wire.Enabled == nil {
		return RootedFeedStatus{}, errors.New("Mithril rooted feed status is invalid")
	}
	if *wire.Enabled {
		if len(wire.AccountsDBRootRunID) != 8 && len(wire.AccountsDBRootRunID) != 32 ||
			strings.ToLower(wire.AccountsDBRootRunID) != wire.AccountsDBRootRunID {
			return RootedFeedStatus{}, errors.New("Mithril rooted feed status is invalid")
		}
		if _, err := hex.DecodeString(wire.AccountsDBRootRunID); err != nil {
			return RootedFeedStatus{}, errors.New("Mithril rooted feed status is invalid")
		}
	} else if wire.AccountsDBRootRunID != "" {
		return RootedFeedStatus{}, errors.New("Mithril rooted feed status is inconsistent")
	}
	return RootedFeedStatus{
		Enabled:             *wire.Enabled,
		AccountsDBRootRunID: wire.AccountsDBRootRunID,
	}, nil
}

func (c *Client) LatestBlockhash(
	ctx context.Context,
	minContextSlot uint64,
) (LatestBlockhash, error) {
	if minContextSlot == 0 {
		return LatestBlockhash{}, errors.New("minimum blockhash context slot is required")
	}
	commitment := "confirmed"
	if c.mithril {
		commitment = "processed"
	}
	raw, err := c.call(ctx, "getLatestBlockhash", []any{map[string]any{
		"commitment":     commitment,
		"minContextSlot": minContextSlot,
	}})
	if err != nil {
		return LatestBlockhash{}, err
	}
	var result struct {
		Context struct {
			Slot uint64 `json:"slot"`
		} `json:"context"`
		Value struct {
			Blockhash            string `json:"blockhash"`
			LastValidBlockHeight uint64 `json:"lastValidBlockHeight"`
		} `json:"value"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return LatestBlockhash{}, errors.New("decode latest blockhash")
	}
	if result.Context.Slot == 0 || result.Value.LastValidBlockHeight == 0 {
		return LatestBlockhash{}, errors.New("latest blockhash response is incomplete")
	}
	if result.Context.Slot < minContextSlot {
		return LatestBlockhash{}, errors.New("latest blockhash context is older than the requested minimum")
	}
	if _, err := solana.Decode32(result.Value.Blockhash); err != nil {
		return LatestBlockhash{}, errors.New("latest blockhash response is invalid")
	}
	return LatestBlockhash{
		ContextSlot:          result.Context.Slot,
		Blockhash:            result.Value.Blockhash,
		LastValidBlockHeight: result.Value.LastValidBlockHeight,
	}, nil
}

func (c *Client) BlockHeight(ctx context.Context) (uint64, error) {
	return c.blockHeight(ctx, 0)
}

// BlockHeightAt returns the node's block height only after it can evaluate the
// request at the retained transaction context.
func (c *Client) BlockHeightAt(ctx context.Context, minContextSlot uint64) (uint64, error) {
	if minContextSlot == 0 {
		return 0, errors.New("minimum block-height context slot is required")
	}
	return c.blockHeight(ctx, minContextSlot)
}

func (c *Client) blockHeight(ctx context.Context, minContextSlot uint64) (uint64, error) {
	commitment := "finalized"
	if c.mithril {
		commitment = "processed"
	}
	config := map[string]any{"commitment": commitment}
	if minContextSlot != 0 {
		config["minContextSlot"] = minContextSlot
	}
	raw, err := c.call(ctx, "getBlockHeight", []any{config})
	if err != nil {
		return 0, err
	}
	var height uint64
	if err := json.Unmarshal(raw, &height); err != nil {
		return 0, errors.New("decode block height")
	}
	if height == 0 {
		return 0, errors.New("block height is zero")
	}
	return height, nil
}

// BlockhashValid asks a standard Solana evidence RPC whether one exact recent
// blockhash remains valid at a context no older than the retained proposal.
func (c *Client) BlockhashValid(
	ctx context.Context,
	blockhash string,
	minContextSlot uint64,
) (BlockhashValidity, error) {
	if _, err := solana.Decode32(blockhash); err != nil {
		return BlockhashValidity{}, errors.New("blockhash is invalid")
	}
	if minContextSlot == 0 {
		return BlockhashValidity{}, errors.New("minimum blockhash-validity context slot is required")
	}
	raw, err := c.call(ctx, "isBlockhashValid", []any{blockhash, map[string]any{
		"commitment": "processed", "minContextSlot": minContextSlot,
	}})
	if err != nil {
		return BlockhashValidity{}, err
	}
	var result struct {
		Context struct {
			Slot uint64 `json:"slot"`
		} `json:"context"`
		Value bool `json:"value"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return BlockhashValidity{}, errors.New("decode blockhash validity")
	}
	if result.Context.Slot < minContextSlot {
		return BlockhashValidity{}, errors.New("blockhash validity context is older than the requested minimum")
	}
	return BlockhashValidity{ContextSlot: result.Context.Slot, Valid: result.Value}, nil
}

func (c *Client) FinalizedSlot(ctx context.Context) (uint64, error) {
	raw, err := c.call(ctx, "getSlot", []any{map[string]any{
		"commitment": "finalized",
	}})
	if err != nil {
		return 0, err
	}
	var slot uint64
	if err := json.Unmarshal(raw, &slot); err != nil {
		return 0, errors.New("decode finalized slot")
	}
	if slot == 0 {
		return 0, errors.New("finalized slot is zero")
	}
	return slot, nil
}

func (c *Client) MinimumBalanceForRentExemption(ctx context.Context, dataLength uint64) (uint64, error) {
	if dataLength == 0 || dataLength > 10<<20 {
		return 0, errors.New("rent-exempt account size is invalid")
	}
	raw, err := c.call(ctx, "getMinimumBalanceForRentExemption", []any{
		dataLength,
		map[string]any{"commitment": "confirmed"},
	})
	if err != nil {
		return 0, err
	}
	var lamports uint64
	if err := json.Unmarshal(raw, &lamports); err != nil || lamports == 0 {
		return 0, errors.New("rent-exempt balance is invalid")
	}
	return lamports, nil
}

func (c *Client) Account(
	ctx context.Context,
	address string,
	minContextSlot uint64,
) (AccountQuote, error) {
	if _, err := solana.Decode32(address); err != nil {
		return AccountQuote{}, errors.New("account address is invalid")
	}
	if minContextSlot == 0 {
		return AccountQuote{}, errors.New("minimum context slot is required")
	}
	raw, err := c.call(ctx, "getAccountInfo", []any{
		address,
		map[string]any{
			"commitment":     "finalized",
			"encoding":       "base64",
			"minContextSlot": minContextSlot,
		},
	})
	if err != nil {
		return AccountQuote{}, err
	}
	var result struct {
		Context struct {
			Slot uint64 `json:"slot"`
		} `json:"context"`
		Value *rpcAccountValue `json:"value"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return AccountQuote{}, errors.New("decode account information")
	}
	if result.Context.Slot < minContextSlot || result.Value == nil {
		return AccountQuote{}, errors.New("account information is missing or stale")
	}
	return parseAccountQuote(result.Value, result.Context.Slot)
}

func (c *Client) AccountSlice(
	ctx context.Context,
	address string,
	minContextSlot,
	offset,
	length uint64,
) (AccountDataSlice, error) {
	if length > 512 {
		return AccountDataSlice{}, errors.New("account data slice is invalid")
	}
	return c.accountDataRange(ctx, address, minContextSlot, offset, length, true)
}

// AccountDataRange reads up to length bytes from an account while retaining
// the account's complete data length. Unlike AccountSlice, a range ending past
// EOF is returned as the shorter available suffix so callers can safely resolve
// bounded on-chain references without first downloading the whole account.
func (c *Client) AccountDataRange(
	ctx context.Context,
	address string,
	minContextSlot,
	offset,
	length uint64,
) (AccountDataSlice, error) {
	if length > maxAccountDataBytes {
		return AccountDataSlice{}, errors.New("account data range is invalid")
	}
	return c.accountDataRange(ctx, address, minContextSlot, offset, length, false)
}

func (c *Client) accountDataRange(
	ctx context.Context,
	address string,
	minContextSlot,
	offset,
	length uint64,
	requireExact bool,
) (AccountDataSlice, error) {
	if _, err := solana.Decode32(address); err != nil {
		return AccountDataSlice{}, errors.New("account address is invalid")
	}
	if minContextSlot == 0 || length == 0 || offset > ^uint64(0)-length {
		return AccountDataSlice{}, errors.New("account data slice is invalid")
	}
	commitment := "confirmed"
	if c.mithril {
		commitment = "processed"
	}
	raw, err := c.callBounded(ctx, "getAccountInfo", []any{
		address,
		map[string]any{
			"commitment":     commitment,
			"encoding":       "base64",
			"minContextSlot": minContextSlot,
			"dataSlice": map[string]uint64{
				"offset": offset,
				"length": length,
			},
		},
	}, accountDataResponseLimit(length))
	if err != nil {
		return AccountDataSlice{}, err
	}
	var result struct {
		Context struct {
			Slot     uint64 `json:"slot"`
			Bankhash string `json:"bankhash"`
		} `json:"context"`
		Value *rpcAccountValue `json:"value"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return AccountDataSlice{}, errors.New("decode account data slice")
	}
	if result.Context.Slot < minContextSlot || result.Value == nil {
		return AccountDataSlice{}, errors.New("account data slice is missing or stale")
	}
	if c.mithril && !validProcessedBankhash(result.Context.Bankhash) {
		return AccountDataSlice{}, errors.New("Mithril account bank identity is missing or invalid")
	}
	if _, err := solana.Decode32(result.Value.Owner); err != nil {
		return AccountDataSlice{}, errors.New("account owner is invalid")
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(result.Value.Data, &parts); err != nil || len(parts) != 2 {
		return AccountDataSlice{}, errors.New("account data encoding is invalid")
	}
	var encoded, encoding string
	if err := json.Unmarshal(parts[0], &encoded); err != nil ||
		json.Unmarshal(parts[1], &encoding) != nil || encoding != "base64" {
		return AccountDataSlice{}, errors.New("account data encoding is invalid")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || offset > result.Value.Space {
		return AccountDataSlice{}, errors.New("account data slice has an invalid length")
	}
	expected := result.Value.Space - offset
	if expected > length {
		expected = length
	}
	if uint64(len(data)) != expected || requireExact && expected != length {
		return AccountDataSlice{}, errors.New("account data slice has an invalid length")
	}
	return AccountDataSlice{
		ContextSlot: result.Context.Slot,
		Bankhash:    result.Context.Bankhash,
		Owner:       result.Value.Owner,
		Executable:  result.Value.Executable,
		DataLength:  result.Value.Space,
		Data:        data,
	}, nil
}

func validProcessedBankhash(value string) bool {
	decoded, err := solana.Decode32(value)
	return err == nil && decoded != ([32]byte{})
}

// AddressLookupTable reads and decodes one complete ALT account at a coherent
// node context. Its result can be compared with a transaction builder's claim
// before compiling a v0 message.
func (c *Client) AddressLookupTable(
	ctx context.Context,
	address string,
	minContextSlot uint64,
) (AddressLookupTable, error) {
	if _, err := solana.Decode32(address); err != nil {
		return AddressLookupTable{}, errors.New("address lookup table address is invalid")
	}
	if minContextSlot == 0 {
		return AddressLookupTable{}, errors.New("minimum context slot is required")
	}
	commitment := "confirmed"
	if c.mithril {
		commitment = "processed"
	}
	raw, err := c.call(ctx, "getAccountInfo", []any{
		address,
		map[string]any{
			"commitment": commitment, "encoding": "base64", "minContextSlot": minContextSlot,
		},
	})
	if err != nil {
		return AddressLookupTable{}, err
	}
	var result struct {
		Context struct {
			Slot uint64 `json:"slot"`
		} `json:"context"`
		Value *rpcAccountValue `json:"value"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return AddressLookupTable{}, errors.New("decode address lookup table account")
	}
	if result.Context.Slot < minContextSlot || result.Value == nil ||
		result.Value.Owner != solana.AddressLookupTableProgram || result.Value.Executable {
		return AddressLookupTable{}, errors.New("address lookup table account is missing, stale, or invalid")
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(result.Value.Data, &parts); err != nil || len(parts) != 2 {
		return AddressLookupTable{}, errors.New("address lookup table data encoding is invalid")
	}
	var encoded, encoding string
	if err := json.Unmarshal(parts[0], &encoded); err != nil ||
		json.Unmarshal(parts[1], &encoding) != nil || encoding != "base64" {
		return AddressLookupTable{}, errors.New("address lookup table data encoding is invalid")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) > 56+256*32 || result.Value.Space != uint64(len(data)) {
		return AddressLookupTable{}, errors.New("address lookup table data is invalid")
	}
	addresses, err := solana.DecodeAddressLookupTable(data, result.Context.Slot)
	if err != nil {
		return AddressLookupTable{}, err
	}
	return AddressLookupTable{ContextSlot: result.Context.Slot, Addresses: addresses}, nil
}

func parseAccountQuote(value *rpcAccountValue, contextSlot uint64) (AccountQuote, error) {
	if value == nil {
		return AccountQuote{}, errors.New("account information is missing")
	}
	if _, err := solana.Decode32(value.Owner); err != nil {
		return AccountQuote{}, errors.New("account owner is invalid")
	}
	var dataParts []json.RawMessage
	if err := json.Unmarshal(value.Data, &dataParts); err != nil ||
		len(dataParts) != 2 {
		return AccountQuote{}, errors.New("account data encoding is invalid")
	}
	var encodedData, encoding string
	if err := json.Unmarshal(dataParts[0], &encodedData); err != nil ||
		json.Unmarshal(dataParts[1], &encoding) != nil ||
		encoding != "base64" {
		return AccountQuote{}, errors.New("account data encoding is invalid")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encodedData)
	if err != nil {
		return AccountQuote{}, errors.New("account data is not canonical base64")
	}
	return AccountQuote{
		ContextSlot: contextSlot,
		Lamports:    value.Lamports,
		Owner:       value.Owner,
		Executable:  value.Executable,
		DataLength:  uint64(len(data)),
	}, nil
}

func (c *Client) FeeForMessage(ctx context.Context, message []byte, minContextSlot uint64) (FeeQuote, error) {
	decoded, err := solana.DecodeLegacyMessage(message)
	if err != nil {
		return FeeQuote{}, fmt.Errorf("validate message: %w", err)
	}
	if decoded.RequiredSignatures != 1 || decoded.ReadonlySigned != 0 {
		return FeeQuote{}, errors.New("message must have exactly one writable signer")
	}
	return c.feeForMessage(ctx, message, minContextSlot)
}

// FeeForV0Message obtains a fee quote only after resolving the message and
// enforcing its expected sole-signer shape.
func (c *Client) FeeForV0Message(
	ctx context.Context,
	message []byte,
	addressTables map[[32]byte][][32]byte,
	expectedSigner string,
	minContextSlot uint64,
) (FeeQuote, error) {
	decoded, err := solana.DecodeV0Message(message, addressTables)
	if err != nil {
		return FeeQuote{}, fmt.Errorf("validate v0 message: %w", err)
	}
	if err := solana.ValidateV0MessageForSigner(decoded, expectedSigner); err != nil {
		return FeeQuote{}, fmt.Errorf("validate v0 signer shape: %w", err)
	}
	return c.feeForMessage(ctx, message, minContextSlot)
}

func (c *Client) feeForMessage(ctx context.Context, message []byte, minContextSlot uint64) (FeeQuote, error) {
	if minContextSlot == 0 {
		return FeeQuote{}, errors.New("minimum context slot is required")
	}
	commitment := "confirmed"
	if c.mithril {
		commitment = "processed"
	}
	raw, err := c.call(ctx, "getFeeForMessage", []any{
		base64.StdEncoding.EncodeToString(message),
		map[string]any{
			"commitment":     commitment,
			"minContextSlot": minContextSlot,
		},
	})
	if err != nil {
		return FeeQuote{}, err
	}
	var result struct {
		Context struct {
			Slot uint64 `json:"slot"`
		} `json:"context"`
		Value *uint64 `json:"value"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return FeeQuote{}, errors.New("decode message fee")
	}
	if result.Context.Slot < minContextSlot || result.Value == nil || *result.Value == 0 {
		return FeeQuote{}, errors.New("message fee response is incomplete")
	}
	return FeeQuote{ContextSlot: result.Context.Slot, Lamports: *result.Value}, nil
}

func (c *Client) SimulateTransfer(ctx context.Context, message []byte, minContextSlot uint64) (Simulation, error) {
	transaction, err := solana.BuildSimulationTransaction(message)
	if err != nil {
		return Simulation{}, fmt.Errorf("validate simulation message: %w", err)
	}
	transfer, err := solana.DecodeTransferMessage(message)
	if err != nil {
		return Simulation{}, fmt.Errorf("decode simulation message: %w", err)
	}
	source := solana.Encode(transfer.Source[:])
	destination := solana.Encode(transfer.Destination[:])
	if minContextSlot == 0 {
		return Simulation{}, errors.New("minimum simulation context slot is required")
	}
	commitment := "confirmed"
	if c.mithril {
		commitment = "processed"
	}
	raw, err := c.call(ctx, "simulateTransaction", []any{
		base64.StdEncoding.EncodeToString(transaction),
		map[string]any{
			"commitment":             commitment,
			"encoding":               "base64",
			"sigVerify":              false,
			"replaceRecentBlockhash": false,
			"minContextSlot":         minContextSlot,
			"accounts": map[string]any{
				"addresses": []string{source, destination},
				"encoding":  "base64",
			},
		},
	})
	if err != nil {
		return Simulation{}, err
	}
	var result struct {
		Context struct {
			Slot uint64 `json:"slot"`
		} `json:"context"`
		Value struct {
			Err           json.RawMessage     `json:"err"`
			UnitsConsumed uint64              `json:"unitsConsumed"`
			Logs          *[]string           `json:"logs"`
			Accounts      *[]*rpcAccountValue `json:"accounts"`
		} `json:"value"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return Simulation{}, errors.New("decode transaction simulation")
	}
	if result.Context.Slot < minContextSlot || len(result.Value.Err) == 0 {
		return Simulation{}, errors.New("transaction simulation response is incomplete")
	}
	if !bytes.Equal(bytes.TrimSpace(result.Value.Err), []byte("null")) {
		return Simulation{}, errors.New("transaction simulation failed")
	}
	if result.Value.Logs == nil || result.Value.Accounts == nil ||
		len(*result.Value.Accounts) != 2 {
		return Simulation{}, errors.New("transaction simulation evidence is incomplete")
	}
	logBytes := 0
	for _, line := range *result.Value.Logs {
		logBytes += len(line)
		if logBytes > maxSimulationLogBytes {
			return Simulation{}, errors.New("transaction simulation logs exceed the evidence limit")
		}
	}
	if len(*result.Value.Logs) > maxSimulationLogLines {
		return Simulation{}, errors.New("transaction simulation has too many log lines")
	}
	sourcePost, err := parseAccountQuote((*result.Value.Accounts)[0], result.Context.Slot)
	if err != nil {
		return Simulation{}, errors.New("decode simulated source account")
	}
	destinationPost, err := parseAccountQuote(
		(*result.Value.Accounts)[1],
		result.Context.Slot,
	)
	if err != nil {
		return Simulation{}, errors.New("decode simulated destination account")
	}
	systemProgram := solana.Encode(make([]byte, 32))
	for _, account := range []AccountQuote{sourcePost, destinationPost} {
		if account.Owner != systemProgram || account.Executable || account.DataLength != 0 {
			return Simulation{}, errors.New("simulated account is not a plain System account")
		}
	}
	logsHash, err := evidenceHash("mithril-agent/simulation-logs-v1", *result.Value.Logs)
	if err != nil {
		return Simulation{}, err
	}
	accountsHash, err := evidenceHash(
		"mithril-agent/simulation-accounts-v1",
		[]AccountQuote{sourcePost, destinationPost},
	)
	if err != nil {
		return Simulation{}, err
	}
	return Simulation{
		ContextSlot:             result.Context.Slot,
		UnitsConsumed:           result.Value.UnitsConsumed,
		SourcePostLamports:      sourcePost.Lamports,
		DestinationPostLamports: destinationPost.Lamports,
		LogsSHA256:              logsHash,
		AccountsSHA256:          accountsHash,
	}, nil
}

func (c *Client) SimulateLegacy(
	ctx context.Context,
	message []byte,
	minContextSlot uint64,
) (LegacySimulation, error) {
	transaction, err := solana.BuildLegacySimulationTransaction(message)
	if err != nil {
		return LegacySimulation{}, fmt.Errorf("validate simulation message: %w", err)
	}
	return c.simulateUnsigned(ctx, transaction, minContextSlot, "mithril-agent/legacy-simulation-logs-v1")
}

// SimulateV0 simulates a decoded and independently resolved version-0 message
// without supplying signing authority.
func (c *Client) SimulateV0(
	ctx context.Context,
	message []byte,
	addressTables map[[32]byte][][32]byte,
	minContextSlot uint64,
) (LegacySimulation, error) {
	transaction, err := solana.BuildV0SimulationTransaction(message, addressTables)
	if err != nil {
		return LegacySimulation{}, fmt.Errorf("validate simulation message: %w", err)
	}
	return c.simulateUnsigned(ctx, transaction, minContextSlot, "mithril-agent/v0-simulation-logs-v1")
}

func (c *Client) simulateUnsigned(
	ctx context.Context,
	transaction []byte,
	minContextSlot uint64,
	logDomain string,
) (LegacySimulation, error) {
	if minContextSlot == 0 {
		return LegacySimulation{}, errors.New("minimum simulation context slot is required")
	}
	commitment := "confirmed"
	if c.mithril {
		commitment = "processed"
	}
	raw, err := c.call(ctx, "simulateTransaction", []any{
		base64.StdEncoding.EncodeToString(transaction),
		map[string]any{
			"commitment":             commitment,
			"encoding":               "base64",
			"sigVerify":              false,
			"replaceRecentBlockhash": false,
			"minContextSlot":         minContextSlot,
		},
	})
	if err != nil {
		return LegacySimulation{}, err
	}
	var result struct {
		Context struct {
			Slot     uint64 `json:"slot"`
			Bankhash string `json:"bankhash"`
		} `json:"context"`
		Value struct {
			Err           json.RawMessage `json:"err"`
			UnitsConsumed uint64          `json:"unitsConsumed"`
			Logs          *[]string       `json:"logs"`
		} `json:"value"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return LegacySimulation{}, errors.New("decode transaction simulation")
	}
	if result.Context.Slot < minContextSlot || len(result.Value.Err) == 0 ||
		result.Value.Logs == nil {
		return LegacySimulation{}, errors.New("transaction simulation response is incomplete")
	}
	if c.mithril && !validProcessedBankhash(result.Context.Bankhash) {
		return LegacySimulation{}, errors.New("Mithril simulation bank identity is missing or invalid")
	}
	if !bytes.Equal(bytes.TrimSpace(result.Value.Err), []byte("null")) {
		return LegacySimulation{}, errors.New("transaction simulation failed")
	}
	logBytes := 0
	for _, line := range *result.Value.Logs {
		logBytes += len(line)
		if logBytes > maxSimulationLogBytes {
			return LegacySimulation{}, errors.New("transaction simulation logs exceed the evidence limit")
		}
	}
	if len(*result.Value.Logs) > maxSimulationLogLines {
		return LegacySimulation{}, errors.New("transaction simulation has too many log lines")
	}
	logsHash, err := evidenceHash(logDomain, *result.Value.Logs)
	if err != nil {
		return LegacySimulation{}, err
	}
	return LegacySimulation{
		ContextSlot:   result.Context.Slot,
		Bankhash:      result.Context.Bankhash,
		UnitsConsumed: result.Value.UnitsConsumed,
		LogsSHA256:    logsHash,
	}, nil
}

func evidenceHash(domain string, value any) (string, error) {
	encoded, err := json.Marshal(struct {
		Domain string `json:"domain"`
		Value  any    `json:"value"`
	}{Domain: domain, Value: value})
	if err != nil {
		return "", errors.New("encode simulation evidence hash")
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func (c *Client) SendTransaction(
	ctx context.Context,
	transaction []byte,
	minContextSlot uint64,
) (string, error) {
	if _, err := solana.VerifySignedTransactionEnvelope(transaction); err != nil {
		return "", fmt.Errorf("validate transaction: %w", err)
	}
	if minContextSlot == 0 {
		return "", errors.New("minimum send context slot is required")
	}
	commitment := "confirmed"
	if c.mithril {
		commitment = "processed"
	}
	raw, err := c.call(ctx, "sendTransaction", []any{
		base64.StdEncoding.EncodeToString(transaction),
		map[string]any{
			"encoding":            "base64",
			"skipPreflight":       false,
			"preflightCommitment": commitment,
			"maxRetries":          uint64(0),
			"minContextSlot":      minContextSlot,
		},
	})
	if err != nil {
		return "", err
	}
	var signature string
	if err := json.Unmarshal(raw, &signature); err != nil {
		return "", errors.New("decode sendTransaction signature")
	}
	if _, err := solana.Decode64(signature); err != nil {
		return "", errors.New("sendTransaction returned an invalid signature")
	}
	return signature, nil
}

func (c *Client) SignatureStatus(ctx context.Context, signature string) (SignatureStatus, error) {
	if _, err := solana.Decode64(signature); err != nil {
		return SignatureStatus{}, errors.New("signature is invalid")
	}
	raw, err := c.call(ctx, "getSignatureStatuses", []any{
		[]string{signature},
		map[string]any{"searchTransactionHistory": true},
	})
	if err != nil {
		return SignatureStatus{}, err
	}
	var result struct {
		Value []json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Value) != 1 {
		return SignatureStatus{}, errors.New("decode signature status")
	}
	if bytes.Equal(bytes.TrimSpace(result.Value[0]), []byte("null")) {
		return SignatureStatus{}, nil
	}
	var value struct {
		Slot               uint64          `json:"slot"`
		Err                json.RawMessage `json:"err"`
		ConfirmationStatus string          `json:"confirmationStatus"`
	}
	if err := json.Unmarshal(result.Value[0], &value); err != nil {
		return SignatureStatus{}, errors.New("decode signature status value")
	}
	if len(value.Err) == 0 {
		return SignatureStatus{}, errors.New("signature status omitted its execution result")
	}
	if value.Slot == 0 {
		return SignatureStatus{}, errors.New("signature status has no slot")
	}
	switch value.ConfirmationStatus {
	case "processed", "confirmed", "finalized":
	default:
		return SignatureStatus{}, errors.New("signature status has an invalid commitment")
	}
	fingerprint, failed, err := errorFingerprint(value.Err)
	if err != nil {
		return SignatureStatus{}, err
	}
	return SignatureStatus{
		Found:              true,
		Slot:               value.Slot,
		ConfirmationStatus: value.ConfirmationStatus,
		Failed:             failed,
		ErrorFingerprint:   fingerprint,
	}, nil
}

func (c *Client) TransactionEffect(ctx context.Context, signature string) (TransactionEffect, error) {
	if _, err := solana.Decode64(signature); err != nil {
		return TransactionEffect{}, errors.New("signature is invalid")
	}
	raw, err := c.call(ctx, "getTransaction", []any{
		signature,
		map[string]any{
			"commitment":                     "finalized",
			"encoding":                       "base64",
			"maxSupportedTransactionVersion": uint64(0),
		},
	})
	if err != nil {
		return TransactionEffect{}, err
	}
	var result struct {
		Slot uint64 `json:"slot"`
		Meta *struct {
			Err               json.RawMessage   `json:"err"`
			Fee               uint64            `json:"fee"`
			PreBalances       []uint64          `json:"preBalances"`
			PostBalances      []uint64          `json:"postBalances"`
			PreTokenBalances  []rpcTokenBalance `json:"preTokenBalances"`
			PostTokenBalances []rpcTokenBalance `json:"postTokenBalances"`
		} `json:"meta"`
		Transaction []json.RawMessage `json:"transaction"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return TransactionEffect{}, errors.New("decode finalized transaction")
	}
	if result.Slot == 0 || result.Meta == nil || len(result.Meta.Err) == 0 ||
		len(result.Meta.PreBalances) == 0 ||
		len(result.Meta.PreBalances) != len(result.Meta.PostBalances) ||
		len(result.Transaction) != 2 {
		return TransactionEffect{}, errors.New("finalized transaction response is incomplete")
	}
	var encoded, encoding string
	if err := json.Unmarshal(result.Transaction[0], &encoded); err != nil ||
		json.Unmarshal(result.Transaction[1], &encoding) != nil ||
		encoding != "base64" {
		return TransactionEffect{}, errors.New("finalized transaction encoding is invalid")
	}
	transaction, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return TransactionEffect{}, errors.New("finalized transaction is not canonical base64")
	}
	envelope, err := solana.VerifySignedTransactionEnvelope(transaction)
	if err != nil || solana.Encode(envelope.Signature[:]) != signature {
		return TransactionEffect{}, errors.New("finalized transaction signature or version is invalid")
	}
	fingerprint, failed, err := errorFingerprint(result.Meta.Err)
	if err != nil {
		return TransactionEffect{}, errors.New("finalized transaction result is invalid")
	}
	preToken, err := parseTokenBalances(result.Meta.PreTokenBalances)
	if err != nil {
		return TransactionEffect{}, errors.New("finalized pre-token balances are invalid")
	}
	postToken, err := parseTokenBalances(result.Meta.PostTokenBalances)
	if err != nil {
		return TransactionEffect{}, errors.New("finalized post-token balances are invalid")
	}
	return TransactionEffect{
		Slot:              result.Slot,
		Transaction:       transaction,
		FeeLamports:       result.Meta.Fee,
		Failed:            failed,
		ErrorFingerprint:  fingerprint,
		PreBalances:       append([]uint64(nil), result.Meta.PreBalances...),
		PostBalances:      append([]uint64(nil), result.Meta.PostBalances...),
		PreTokenBalances:  preToken,
		PostTokenBalances: postToken,
	}, nil
}

type rpcTokenBalance struct {
	AccountIndex  uint16 `json:"accountIndex"`
	Mint          string `json:"mint"`
	Owner         string `json:"owner"`
	UITokenAmount struct {
		Amount string `json:"amount"`
	} `json:"uiTokenAmount"`
}

func parseTokenBalances(values []rpcTokenBalance) ([]TokenBalance, error) {
	balances := make([]TokenBalance, len(values))
	seen := make(map[uint16]struct{}, len(values))
	for index, value := range values {
		if _, exists := seen[value.AccountIndex]; exists {
			return nil, errors.New("duplicate token balance account index")
		}
		seen[value.AccountIndex] = struct{}{}
		if _, err := solana.Decode32(value.Mint); err != nil {
			return nil, err
		}
		if value.Owner != "" {
			if _, err := solana.Decode32(value.Owner); err != nil {
				return nil, err
			}
		}
		amount, err := strconv.ParseUint(value.UITokenAmount.Amount, 10, 64)
		if err != nil {
			return nil, err
		}
		balances[index] = TokenBalance{
			AccountIndex: value.AccountIndex,
			Mint:         value.Mint,
			Owner:        value.Owner,
			Amount:       amount,
		}
	}
	return balances, nil
}

func errorFingerprint(raw json.RawMessage) (string, bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false, nil
	}
	if err := strictjson.Validate(raw); err != nil {
		return "", false, errors.New("signature status error is ambiguous")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", false, errors.New("signature status error is invalid")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", false, errors.New("canonicalize signature status error")
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), true, nil
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code int `json:"code"`
		Data *struct {
			Reason string `json:"reason"`
		} `json:"data"`
	} `json:"error"`
}

// knownNodeHealthReasons are the only reason strings that may reach an
// operator. The response body is attacker-influenced in the general case, so a
// reason is echoed only when it matches a value this build already knows;
// anything else is dropped rather than printed.
var knownNodeHealthReasons = map[string]struct{}{
	"diverged":                   {},
	"stalled":                    {},
	"unavailable":                {},
	"unknown_verification_state": {},
}

type RPCError struct {
	Code int
	// Reason is a bounded node-health reason when a Mithril node refused
	// because it knows its own state is untrustworthy. Empty otherwise.
	Reason string
}

func (e *RPCError) Error() string {
	message := "RPC returned error code " + strconv.Itoa(e.Code)
	if e.Reason != "" {
		message += " (node reports: " + e.Reason + ")"
	}
	return message
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return c.callBounded(ctx, method, params, maxResponseBytes)
}

func (c *Client) callBounded(
	ctx context.Context, method string, params any, responseLimit int64,
) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, errors.New("encode RPC request")
	}
	retryable := retryableReadMethod(method)
	for attempt := 1; attempt <= maxRPCAttempts; attempt++ {
		if err := c.waitForRequestTurn(ctx); err != nil {
			return nil, err
		}
		result, delay, retry, err := c.callOnce(ctx, id, body, responseLimit)
		if err == nil {
			return result, nil
		}
		if !retryable || !retry || attempt == maxRPCAttempts {
			return nil, err
		}
		if delay < 0 {
			delay = retryDelay(attempt)
		}
		if err := c.sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("RPC retry limit reached")
}

func (c *Client) waitForRequestTurn(ctx context.Context) error {
	if c.pace == 0 {
		return nil
	}
	c.paceMu.Lock()
	defer c.paceMu.Unlock()
	now := c.now()
	if delay := c.paceNext.Sub(now); delay > 0 {
		if err := c.sleep(ctx, delay); err != nil {
			return err
		}
		now = c.now()
	}
	c.paceNext = now.Add(c.pace)
	return nil
}

func (c *Client) callOnce(
	ctx context.Context,
	id uint64,
	body []byte,
	responseLimit int64,
) (json.RawMessage, time.Duration, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, 0, false, errors.New("create RPC request")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, false, ctxErr
		}
		return nil, -1, safeTransportFailure(err), errors.New("RPC transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, responseLimit+1))
		retry := retryableHTTPStatus(response.StatusCode)
		delay := time.Duration(-1)
		if retry {
			if parsed, ok := parseRetryAfter(response.Header.Get("Retry-After"), c.now()); ok {
				delay = parsed
			}
		}
		return nil, delay, retry, errors.New("RPC returned a non-200 status")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, false, ctxErr
		}
		return nil, -1, safeTransportFailure(err), errors.New("read RPC response")
	}
	if int64(len(data)) > responseLimit {
		return nil, 0, false, errors.New("RPC response exceeds size limit")
	}
	if err := strictjson.Validate(data); err != nil {
		return nil, 0, false, errors.New("RPC response is ambiguous")
	}
	var decoded rpcResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, 0, false, errors.New("decode RPC response")
	}
	if decoded.JSONRPC != "2.0" {
		return nil, 0, false, errors.New("RPC response has the wrong version")
	}
	var responseID uint64
	if err := json.Unmarshal(decoded.ID, &responseID); err != nil || responseID != id {
		return nil, 0, false, errors.New("RPC response ID does not match")
	}
	if decoded.Error != nil {
		rpcErr := &RPCError{Code: decoded.Error.Code}
		if decoded.Error.Data != nil {
			if _, known := knownNodeHealthReasons[decoded.Error.Data.Reason]; known {
				rpcErr.Reason = decoded.Error.Data.Reason
			}
		}
		return nil, 0, false, rpcErr
	}
	if len(bytes.TrimSpace(decoded.Result)) == 0 || bytes.Equal(bytes.TrimSpace(decoded.Result), []byte("null")) {
		return nil, 0, false, errors.New("RPC response has no result")
	}
	return bytes.Clone(decoded.Result), 0, false, nil
}

func retryableReadMethod(method string) bool {
	switch method {
	case "getGenesisHash",
		"getVerificationStatus",
		"getLatestBlockhash",
		"isBlockhashValid",
		"getBlockHeight",
		"getSlot",
		"getAccountInfo",
		"getMinimumBalanceForRentExemption",
		"getFeeForMessage",
		"simulateTransaction",
		"getSignatureStatuses",
		"getTransaction":
		return true
	default:
		return false
	}
}

func retryableHTTPStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func retryDelay(attempt int) time.Duration {
	delay := initialRetryDelay << (attempt - 1)
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseUint(value, 10, 31); err == nil {
		return min(time.Duration(seconds)*time.Second, maxRetryDelay), true
	}
	retryAt, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	delay := retryAt.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return min(delay, maxRetryDelay), true
}

func safeTransportFailure(err error) bool {
	if err == nil ||
		errors.Is(err, context.Canceled) {
		return false
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsTemporary {
		return true
	}
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
