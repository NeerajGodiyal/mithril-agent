package main

import (
	"strings"
	"testing"
)

// A provider error and a genuinely absent account are different facts, and the
// sweep setup turns the second into a warning an operator must click through.
// Decoding only the result made them identical: a rate limit came back as
// "the destination does not exist on Devnet", which it said about a wallet
// holding 8.55 SOL. Repeated, that teaches operators to dismiss the warning
// that exists to catch a genuinely unusable destination.
func TestWalletResponseSeparatesProviderErrorFromAbsence(t *testing.T) {
	// The SAME type production decodes into, so the nesting cannot drift.
	var absent walletAccountResponse
	// A real "no such account": result present, value null.
	body := `{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":null}}`
	if err := decodeWalletResponse([]byte(body), &absent); err != nil {
		t.Fatalf("a genuine absence was reported as an error: %v", err)
	}
	if absent.Result.Value != nil {
		t.Fatal("absent account decoded as present")
	}

	// A rate limit must NOT read as absence.
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"error":{"code":429,"message":"Too many requests"}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid param"}}`,
	} {
		err := decodeWalletResponse([]byte(body), &absent)
		if err == nil {
			t.Fatalf("provider error decoded as a successful absence: %s", body)
		}
		// The operator sees this text; it must not leak the endpoint.
		if strings.Contains(err.Error(), "api.devnet") {
			t.Errorf("error text leaked the endpoint: %v", err)
		}
	}

	// A present account still decodes.
	present := `{"jsonrpc":"2.0","id":1,"result":{"value":{"owner":"11111111111111111111111111111111"}}}`
	if err := decodeWalletResponse([]byte(present), &absent); err != nil {
		t.Fatal(err)
	}
	if absent.Result.Value == nil || absent.Result.Value.Owner == "" {
		t.Fatal("present account did not decode")
	}
}

// The round-trip view decides whether a leg can be funded from this. Reporting
// a provider failure as "no tokens held" would tell an operator their buy leg
// cannot run when in fact nothing was read — the same confusion that made the
// sweep setup announce every destination as absent.
func TestAbsentTokenAccountIsNotAProviderFailure(t *testing.T) {
	var result any
	absent := decodeWalletResponse([]byte(
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid param: could not find account"}}`,
	), &result)
	if !isAccountNotFound(absent) {
		t.Errorf("a genuinely absent account was treated as a failure: %v", absent)
	}
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"error":{"code":429,"message":"Too many requests"}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`,
	} {
		err := decodeWalletResponse([]byte(body), &result)
		if err == nil || isAccountNotFound(err) {
			t.Errorf("a provider failure was reported as an empty account: %v", err)
		}
	}
}

func TestWalletResponseNeverEchoesProviderErrorText(t *testing.T) {
	const providerText = "private provider detail\n\x1b[31m"
	var result any
	err := decodeWalletResponse([]byte(
		`{"jsonrpc":"2.0","id":1,"error":{"code":429,"message":"private provider detail\n\\u001b[31m"}}`,
	), &result)
	if err == nil || strings.Contains(err.Error(), "private provider") ||
		strings.Contains(err.Error(), providerText) || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("provider response text reached the operator error: %q", err)
	}
}

func TestWalletResponseRejectsAmbiguousJSONRPCEnvelope(t *testing.T) {
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":2,"result":{"value":1}}`,
		`{"jsonrpc":"1.0","id":1,"result":{"value":1}}`,
		`{"jsonrpc":"2.0","id":1,"id":1,"result":{"value":1}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"value":1}} {}`,
	} {
		var result any
		if err := decodeWalletResponse([]byte(body), &result); err == nil {
			t.Fatalf("ambiguous JSON-RPC response was accepted: %s", body)
		}
	}
}
