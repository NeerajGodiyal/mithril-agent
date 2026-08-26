package jupiterswap

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

const maxLiveIDLBytes = 1 << 20

// TestLiveCurrentJupiterIDL proves the on-chain IDL used to configure the
// hosted signing policy still describes both route_v2 contracts validated here.
// It is an explicit read-only test with no wallet, signature, or submission.
func TestLiveCurrentJupiterIDL(t *testing.T) {
	endpoint := os.Getenv("MITHRIL_AGENT_LIVE_MAINNET_RPC_URL")
	if os.Getenv("MITHRIL_AGENT_LIVE_JUPITER_TEST") != "1" || endpoint == "" {
		t.Skip("set MITHRIL_AGENT_LIVE_JUPITER_TEST=1 and MITHRIL_AGENT_LIVE_MAINNET_RPC_URL")
	}
	idl := fetchLiveJupiterIDL(t, endpoint)
	routeAccounts := []string{
		"user_transfer_authority",
		"user_source_token_account",
		"user_destination_token_account",
		"source_mint",
		"destination_mint",
		"source_token_program",
		"destination_token_program",
		"destination_token_account",
		"event_authority",
		"program",
	}
	sharedAccounts := []string{
		"program_authority",
		"user_transfer_authority",
		"source_token_account",
		"program_source_token_account",
		"program_destination_token_account",
		"destination_token_account",
		"source_mint",
		"destination_mint",
		"source_token_program",
		"destination_token_program",
		"event_authority",
		"program",
	}
	routeArgs := []string{
		"in_amount",
		"quoted_out_amount",
		"slippage_bps",
		"platform_fee_bps",
		"positive_slippage_bps",
		"route_plan",
	}
	sharedArgs := append([]string{"id"}, routeArgs...)
	foundRoute, foundShared := 0, 0
	for _, instruction := range idl.Instructions {
		switch instruction.Name {
		case "route_v2", "routeV2":
			foundRoute++
			if !bytes.Equal(instruction.Discriminator, routeV2Discriminator[:]) ||
				!slices.Equal(fieldNames(instruction.Accounts), routeAccounts) ||
				!slices.Equal(fieldNames(instruction.Args), routeArgs) {
				t.Fatal("live Jupiter route_v2 IDL no longer matches the validated contract")
			}
		case "shared_accounts_route_v2", "sharedAccountsRouteV2":
			foundShared++
			if !bytes.Equal(instruction.Discriminator, sharedAccountsRouteV2Discriminator[:]) ||
				!slices.Equal(fieldNames(instruction.Accounts), sharedAccounts) ||
				!slices.Equal(fieldNames(instruction.Args), sharedArgs) {
				t.Fatal("live Jupiter shared route_v2 IDL no longer matches the validated contract")
			}
		}
	}
	if foundRoute != 1 || foundShared != 1 {
		t.Fatalf("live Jupiter IDL contains %d route_v2 and %d shared route_v2 instructions",
			foundRoute, foundShared)
	}
}

type liveIDLField struct {
	Name string `json:"name"`
}

type liveJupiterIDL struct {
	Instructions []struct {
		Name          string         `json:"name"`
		Discriminator []byte         `json:"discriminator"`
		Accounts      []liveIDLField `json:"accounts"`
		Args          []liveIDLField `json:"args"`
	} `json:"instructions"`
}

func fieldNames(fields []liveIDLField) []string {
	names := make([]string, len(fields))
	for index, field := range fields {
		names[index] = field.Name
	}
	return names
}

func fetchLiveJupiterIDL(t *testing.T, endpoint string) liveJupiterIDL {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.Fragment != "" {
		t.Fatal("live Mainnet RPC endpoint is invalid")
	}
	programKey, err := solana.Decode32(Program)
	if err != nil {
		t.Fatal("pinned Jupiter program is invalid")
	}
	base, _, err := solana.FindProgramAddress(nil, Program)
	if err != nil {
		t.Fatal("derive Jupiter IDL base")
	}
	baseKey, err := solana.Decode32(base)
	if err != nil {
		t.Fatal("derived Jupiter IDL base is invalid")
	}
	hash := sha256.New()
	_, _ = hash.Write(baseKey[:])
	_, _ = hash.Write([]byte("anchor:idl"))
	_, _ = hash.Write(programKey[:])
	idlAddress := solana.Encode(hash.Sum(nil))
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "getAccountInfo",
		"params": []any{
			idlAddress,
			map[string]any{"encoding": "base64", "commitment": "finalized"},
		},
	})
	if err != nil {
		t.Fatal("encode Jupiter IDL request")
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal("build Jupiter IDL request")
	}
	request.Header.Set("content-type", "application/json")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("fetch Jupiter IDL")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("Jupiter IDL RPC returned a non-success status")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxLiveIDLBytes+1))
	if err != nil || len(data) > maxLiveIDLBytes {
		t.Fatal("Jupiter IDL RPC response is invalid")
	}
	var rpc struct {
		Result struct {
			Value *struct {
				Data       []string `json:"data"`
				Executable bool     `json:"executable"`
				Owner      string   `json:"owner"`
				Space      uint64   `json:"space"`
			} `json:"value"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &rpc); err != nil ||
		(len(bytes.TrimSpace(rpc.Error)) != 0 && !bytes.Equal(bytes.TrimSpace(rpc.Error), []byte("null"))) ||
		rpc.Result.Value == nil || rpc.Result.Value.Owner != Program || rpc.Result.Value.Executable ||
		len(rpc.Result.Value.Data) != 2 || rpc.Result.Value.Data[1] != "base64" {
		t.Fatal("Jupiter IDL account response is invalid")
	}
	accountData, err := base64.StdEncoding.Strict().DecodeString(rpc.Result.Value.Data[0])
	if err != nil || uint64(len(accountData)) != rpc.Result.Value.Space || len(accountData) < 44 {
		t.Fatal("Jupiter IDL account data is invalid")
	}
	accountDiscriminator := sha256.Sum256([]byte("internal:IdlAccount"))
	length := int(binary.LittleEndian.Uint32(accountData[40:44]))
	if !bytes.Equal(accountData[:8], accountDiscriminator[:8]) ||
		length < 1 || length > len(accountData)-44 {
		t.Fatal("Jupiter IDL account layout is invalid")
	}
	compressed := bytes.NewReader(accountData[44 : 44+length])
	reader, err := zlib.NewReader(compressed)
	if err != nil {
		t.Fatal("Jupiter IDL compression is invalid")
	}
	decoded, err := io.ReadAll(io.LimitReader(reader, maxLiveIDLBytes+1))
	closeErr := reader.Close()
	if err != nil || closeErr != nil || len(decoded) > maxLiveIDLBytes {
		t.Fatal("Jupiter IDL decompression failed")
	}
	var idl liveJupiterIDL
	if err := json.Unmarshal(decoded, &idl); err != nil || len(idl.Instructions) == 0 {
		t.Fatal("Jupiter IDL JSON is invalid")
	}
	return idl
}
