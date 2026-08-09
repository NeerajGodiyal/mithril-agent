package main

import (
	"errors"
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
	for _, absent := range []string{
		"the Devnet endpoint refused the read (-32602): Invalid param: could not find account",
		"could not find account",
	} {
		if !isAccountNotFound(errors.New(absent)) {
			t.Errorf("a genuinely absent account was treated as a failure: %q", absent)
		}
	}
	for _, failure := range []string{
		"the Devnet endpoint refused the read (429): Too many requests",
		"the Devnet endpoint could not be reached",
		"the Devnet endpoint returned an unreadable response",
		// These two were swallowed by matching bare "not found" / "invalid
		// param": a broken or rate-limited endpoint read as an empty wallet.
		"the Devnet endpoint refused the read (-32601): Method not found",
		"the Devnet endpoint refused the read (-32602): Invalid params",
	} {
		if isAccountNotFound(errors.New(failure)) {
			t.Errorf("a provider failure was reported as an empty account: %q", failure)
		}
	}
}
