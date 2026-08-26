package jupiterquote

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	testTaker       = "11111111111111111111111111111111"
	testInputMint   = "So11111111111111111111111111111111111111112"
	testOutputMint  = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	testDestination = "ATokenGPvbdGVxr1b2hvZbsiqW5xWH25efTNsLJA8knL"
)

func TestQuoteValidatesTheJupiterResponseAgainstTheRequest(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("x-api-key") != "test-key" {
			t.Error("API key was not sent in the required header")
		}
		query := request.URL.Query()
		for name, want := range map[string]string{
			"inputMint": testInputMint, "outputMint": testOutputMint,
			"amount": "1000000", "taker": testTaker, "slippageBps": "50", "maxAccounts": "32",
			"blockhashSlotsToExpiry": "150", "wrapAndUnwrapSol": "true",
			"destinationTokenAccount": testDestination,
		} {
			if got := query.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		body := []byte(`{
          "inputMint":"` + testInputMint + `",
          "outputMint":"` + testOutputMint + `",
          "inAmount":"1000000",
          "outAmount":"150000",
          "otherAmountThreshold":"149250",
          "swapMode":"ExactIn",
          "slippageBps":50,
          "routePlan":[{"bps":10000}],
          "swapInstruction":{"programId":"ignored-by-read-only-client"}
        }`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    request,
		}, nil
	})}

	client, err := newClient("http://jupiter.test/build", "test-key", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Quote(t.Context(), Request{
		Taker: testTaker, InputMint: testInputMint, OutputMint: testOutputMint,
		DestinationTokenAccount: testDestination, InputAmount: 1_000_000, SlippageBPS: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.InputAmount != 1_000_000 || result.EstimatedOutput != 150_000 || result.MinimumOutput != 149_250 {
		t.Fatalf("quote = %+v", result)
	}
}

func TestQuoteRejectsAnInvalidDestinationTokenAccount(t *testing.T) {
	client, err := newClient("http://jupiter.test/build", "", &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid request reached the network")
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{
		Taker: testTaker, InputMint: testInputMint, OutputMint: testOutputMint,
		DestinationTokenAccount: "not-an-address", InputAmount: 1, SlippageBPS: 1,
	}
	if _, err := client.Quote(t.Context(), request); err == nil {
		t.Fatal("invalid destination token account was accepted")
	}
	if err := (Result{InputAmount: 1, EstimatedOutput: 1, MinimumOutput: 1}).Validate(request); err == nil {
		t.Fatal("portable request accepted an invalid destination token account")
	}
}

func TestQuoteUsesTheOnChainCeilingForMinimumOutput(t *testing.T) {
	request := Request{
		Taker: testTaker, InputMint: testInputMint, OutputMint: testOutputMint,
		InputAmount: 1_000_000, SlippageBPS: 50,
	}
	response := `{"inputMint":"` + testInputMint + `","outputMint":"` + testOutputMint +
		`","inAmount":"1000000","outAmount":"75002","otherAmountThreshold":"74627",` +
		`"swapMode":"ExactIn","slippageBps":50,"routePlan":[{"bps":10000}]}`
	result, err := decodeResult([]byte(response), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.MinimumOutput != 74_627 {
		t.Fatalf("minimum output = %d", result.MinimumOutput)
	}
}

func TestQuoteRefusesMismatchedOrUnsafeResponses(t *testing.T) {
	request := Request{
		Taker: testTaker, InputMint: testInputMint, OutputMint: testOutputMint,
		InputAmount: 1_000_000, SlippageBPS: 50,
	}
	valid := `{"inputMint":"` + testInputMint + `","outputMint":"` + testOutputMint +
		`","inAmount":"1000000","outAmount":"150000","otherAmountThreshold":"149250",` +
		`"swapMode":"ExactIn","slippageBps":50,"routePlan":[{"bps":10000}]}`
	for name, body := range map[string]string{
		"wrong input": strings.Replace(valid, `"1000000"`, `"999999"`, 1),
		"wrong mint":  strings.Replace(valid, testOutputMint, testInputMint, 1),
		"no route":    strings.Replace(valid, `"routePlan":[{"bps":10000}]`, `"routePlan":[]`, 1),
		"bad floor":   strings.Replace(valid, `"149250"`, `"150001"`, 1),
		"loose floor": strings.Replace(valid, `"149250"`, `"149249"`, 1),
		"wrong mode":  strings.Replace(valid, `"ExactIn"`, `"ExactOut"`, 1),
		"duplicate":   strings.Replace(valid, `"slippageBps":50`, `"slippageBps":50,"slippageBps":50`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeResult([]byte(body), request); err == nil {
				t.Fatal("unsafe response was accepted")
			}
		})
	}
}

func TestQuoteTreatsRouteAllocationAsInformational(t *testing.T) {
	request := Request{
		Taker: testTaker, InputMint: testInputMint, OutputMint: testOutputMint,
		InputAmount: 1_000_000, SlippageBPS: 50,
	}
	valid := `{"inputMint":"` + testInputMint + `","outputMint":"` + testOutputMint +
		`","inAmount":"1000000","outAmount":"150000","otherAmountThreshold":"149250",` +
		`"swapMode":"ExactIn","slippageBps":50,"routePlan":[{"bps":10000}]}`
	for name, body := range map[string]string{
		"rounded total": strings.Replace(valid, `"bps":10000`, `"bps":9999`, 1),
		"zero split":    strings.Replace(valid, `[{"bps":10000}]`, `[{"bps":0},{"bps":10000}]`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeResult([]byte(body), request); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBuildDecodesOnlyABoundedUnsignedProposal(t *testing.T) {
	const tableID = "AddressLookupTab1e1111111111111111111111111"
	body := buildResponseForTest(
		`{"programId":"ComputeBudget111111111111111111111111111111",`+
			`"accounts":[],"data":"AwEAAAAAAAAA"}`,
		`{"programId":"`+testInputMint+`","accounts":[`+
			`{"pubkey":"`+testTaker+`","isSigner":true,"isWritable":true},`+
			`{"pubkey":"`+testOutputMint+`","isSigner":false,"isWritable":true}],`+
			`"data":"AQID"}`,
		`{"`+tableID+`":["`+testInputMint+`"]}`,
	)
	result, err := decodeBuildResult([]byte(body), Request{
		Taker: testTaker, InputMint: testInputMint, OutputMint: testOutputMint,
		InputAmount: 1_000_000, SlippageBPS: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ComputeBudget) != 1 || len(result.Instructions) != 1 ||
		result.Instructions[0].Program != testInputMint ||
		len(result.Instructions[0].Accounts) != 2 || !result.Instructions[0].Accounts[0].Signer ||
		!bytes.Equal(result.Instructions[0].Data, []byte{1, 2, 3}) ||
		result.LastValidBlockHeight != 123 || result.RecentBlockhash[0] != 1 ||
		len(result.ClaimedAddressTables) != 1 {
		t.Fatalf("build result = %+v", result)
	}
	for table, accounts := range result.ClaimedAddressTables {
		if table == ([32]byte{}) || len(accounts) != 1 {
			t.Fatal("address table mapping was not decoded")
		}
	}
	if err := result.VerifyAddressTables(result.ClaimedAddressTables); err != nil {
		t.Fatal(err)
	}
	for table, accounts := range result.ClaimedAddressTables {
		mismatch := map[[32]byte][][32]byte{table: append([][32]byte(nil), accounts...)}
		mismatch[table][0] = [32]byte{}
		if err := result.VerifyAddressTables(mismatch); err == nil {
			t.Fatal("mismatched independently sourced lookup table was accepted")
		}
	}
}

func TestBuildRejectsHostileTransactionFields(t *testing.T) {
	validInstruction := `{"programId":"` + testInputMint + `","accounts":[` +
		`{"pubkey":"` + testTaker + `","isSigner":true,"isWritable":true}],"data":"AQID"}`
	validBlockhash := strings.TrimSuffix(strings.Repeat("1,", 32), ",")
	for name, body := range map[string]string{
		"foreign signer": buildResponseForTest("", strings.Replace(
			validInstruction, testTaker, testOutputMint, 1,
		), `{}`),
		"invalid instruction data": buildResponseForTest("", strings.Replace(
			validInstruction, `"AQID"`, `"AQI"`, 1,
		), `{}`),
		"signer as program": buildResponseForTest("", strings.Replace(
			validInstruction, testInputMint, testTaker, 1,
		), `{}`),
		"invalid table": buildResponseForTest("", validInstruction,
			`{"not-an-address":["`+testInputMint+`"]}`),
		"foreign compute program": buildResponseForTest(validInstruction, validInstruction, `{}`),
		"compute limit instead of price": buildResponseForTest(
			`{"programId":"ComputeBudget111111111111111111111111111111",`+
				`"accounts":[],"data":"AgEAAAA="}`,
			validInstruction, `{}`,
		),
		"compute instruction outside compute list": strings.Replace(
			buildResponseForTest(
				`{"programId":"ComputeBudget111111111111111111111111111111",`+
					`"accounts":[],"data":"AwEAAAAAAAAA"}`,
				validInstruction, `{}`,
			),
			`"otherInstructions":[]`,
			`"otherInstructions":[{"programId":"ComputeBudget111111111111111111111111111111",`+
				`"accounts":[],"data":"AwEAAAAAAAAA"}]`, 1,
		),
		"unrequested tip": strings.Replace(
			buildResponseForTest(
				`{"programId":"ComputeBudget111111111111111111111111111111",`+
					`"accounts":[],"data":"AwEAAAAAAAAA"}`,
				validInstruction, `{}`,
			),
			`"tipInstruction":null`, `"tipInstruction":`+validInstruction, 1,
		),
		"short blockhash": strings.Replace(
			buildResponseForTest("", validInstruction, `{}`),
			`"blockhash":[`+validBlockhash+`]`,
			`"blockhash":[1]`, 1,
		),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeBuildResult([]byte(body), Request{
				Taker: testTaker, InputMint: testInputMint, OutputMint: testOutputMint,
				InputAmount: 1_000_000, SlippageBPS: 50,
			}); err == nil {
				t.Fatal("hostile Jupiter build response was accepted")
			}
		})
	}
}

func TestDecodeAddressTablesUsesTheEvidenceLimit(t *testing.T) {
	tables := make(map[string][]string, maxAddressTables+1)
	for index := range maxAddressTables + 1 {
		address := make([]byte, 32)
		address[0] = byte(index + 1)
		tables[solana.Encode(address)] = []string{testInputMint}
	}
	if _, err := decodeAddressTables(tables); err == nil {
		t.Fatal("a build with more address tables than the evidence format was accepted")
	}
}

func buildResponseForTest(computeBudget, swap, addressTables string) string {
	compute := "[]"
	if computeBudget != "" {
		compute = "[" + computeBudget + "]"
	}
	blockhash := strings.TrimSuffix(strings.Repeat("1,", 32), ",")
	return `{"inputMint":"` + testInputMint + `","outputMint":"` + testOutputMint +
		`","inAmount":"1000000","outAmount":"150000","otherAmountThreshold":"149250",` +
		`"swapMode":"ExactIn","slippageBps":50,"routePlan":[{"bps":10000}],` +
		`"computeBudgetInstructions":` + compute + `,"setupInstructions":[],` +
		`"swapInstruction":` + swap + `,"cleanupInstruction":null,"otherInstructions":[],` +
		`"tipInstruction":null,"addressesByLookupTableAddress":` + addressTables + `,` +
		`"blockhashWithMetadata":{"blockhash":[` + blockhash + `],"lastValidBlockHeight":123}}`
}

func TestQuoteBoundsFailuresWithoutEchoingProviderData(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(strings.NewReader("provider-secret-payload")),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}
	client, err := newClient("http://jupiter.test/build", "test-key", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Quote(t.Context(), Request{
		Taker: testTaker, InputMint: testInputMint, OutputMint: testOutputMint,
		InputAmount: 1, SlippageBPS: 1,
	})
	if !errors.Is(err, ErrTemporarilyUnavailable) {
		t.Fatalf("rate limit error = %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("provider payload escaped into the error: %v", err)
	}
	if _, err := New("bad\nkey"); err == nil {
		t.Fatal("an unsafe API key was accepted")
	}
}

func TestInternalClientSupportsAKeylessTestEndpoint(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("x-api-key") != "" {
			t.Fatal("keyless request sent an API key header")
		}
		body := []byte(`{
          "inputMint":"` + testInputMint + `",
          "outputMint":"` + testOutputMint + `",
          "inAmount":"1",
          "outAmount":"200",
          "otherAmountThreshold":"199",
          "swapMode":"ExactIn",
          "slippageBps":50,
          "routePlan":[{"bps":10000}]
        }`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}
	client, err := newClient("http://jupiter.test/build", "", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Quote(t.Context(), Request{
		Taker: testTaker, InputMint: testInputMint, OutputMint: testOutputMint,
		InputAmount: 1, SlippageBPS: 50,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQuoteNeverForwardsTheAPIKeyThroughARedirect(t *testing.T) {
	calls := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusFound,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     http.Header{"Location": []string{"https://attacker.invalid/steal"}},
			Request:    request,
		}, nil
	})}
	client, err := newClient("http://jupiter.test/build", "test-key", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Quote(t.Context(), Request{
		Taker: testTaker, InputMint: testInputMint, OutputMint: testOutputMint,
		InputAmount: 1, SlippageBPS: 1,
	})
	if err == nil {
		t.Fatal("redirect response was accepted")
	}
	if calls != 1 {
		t.Fatalf("followed redirect with credential-bearing request: %d HTTP calls", calls)
	}
}

func TestProductionClientIgnoresAmbientProxies(t *testing.T) {
	client, err := New("test-key")
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("production Jupiter client can use an ambient proxy")
	}
}

func TestProductionClientSupportsKeylessAccess(t *testing.T) {
	client, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	if client.apiKey != "" {
		t.Fatal("keyless production client retained an API key")
	}
}

// TestLiveJupiterBuild is an explicit, read-only contract check against both
// directions of the current API. It is gated because ordinary unit tests must
// not need a network. A key raises the official rate limit but is optional.
func TestLiveJupiterBuild(t *testing.T) {
	apiKey := os.Getenv("MITHRIL_AGENT_JUPITER_API_KEY")
	taker := os.Getenv("MITHRIL_AGENT_LIVE_JUPITER_TAKER")
	if os.Getenv("MITHRIL_AGENT_LIVE_JUPITER_TEST") != "1" || taker == "" {
		t.Skip("set MITHRIL_AGENT_LIVE_JUPITER_TEST=1 and MITHRIL_AGENT_LIVE_JUPITER_TAKER")
	}
	if taker == testTaker {
		t.Fatal("the live Jupiter taker must be a wallet address, not the System Program")
	}
	client, err := New(apiKey)
	if err != nil {
		t.Fatal(err)
	}
	destinationAccount, err := orcaswap.AssociatedTokenAddress(taker, testOutputMint)
	if err != nil {
		t.Fatal(err)
	}
	sell, err := client.Build(t.Context(), Request{
		Taker: taker, InputMint: testInputMint, OutputMint: testOutputMint,
		DestinationTokenAccount: destinationAccount,
		InputAmount:             1_000_000, SlippageBPS: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Build(t.Context(), Request{
		Taker: taker, InputMint: testOutputMint, OutputMint: testInputMint,
		InputAmount: sell.Quote.MinimumOutput, SlippageBPS: 50,
	}); err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
