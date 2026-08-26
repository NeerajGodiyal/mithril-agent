package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The offline walkthrough must work with no network at all, so a reviewer on a
// locked-down machine still sees the audit-chain evidence.
func TestWalkthroughOfflineProvesTheAuditChain(t *testing.T) {
	var out bytes.Buffer
	if err := runWalkthrough(t.Context(), []string{"--offline"}, &out); err != nil {
		t.Fatalf("offline walkthrough failed: %v", err)
	}
	text := out.String()
	for _, required := range []string{"VERIFIED", "REJECTED"} {
		if !strings.Contains(text, required) {
			t.Errorf("walkthrough output no longer contains %q", required)
		}
	}
	// The safety claim matters, its casing does not.
	if !strings.Contains(strings.ToLower(text), "cannot place a trade") {
		t.Error("walkthrough no longer states that it cannot place a trade")
	}
	// A missing format argument silently degrades the report into noise.
	if strings.Contains(text, "%!") {
		t.Errorf("walkthrough output has a broken format verb:\n%s", text)
	}
	if strings.Contains(text, "The price sources, the comparison rule") {
		t.Error("offline walkthrough claims that skipped price checks were proved")
	}
	for _, unproved := range []string{"node evidence accepted", "two sources agreed"} {
		if strings.Contains(text, unproved) {
			t.Errorf("offline walkthrough records unproved event %q", unproved)
		}
	}
	if !strings.Contains(text, "did not\n    prove the live price inputs") {
		t.Error("offline walkthrough does not clearly limit its evidence")
	}
}

// It must never overstate what it demonstrated. Claiming the live trade path
// was proved is the specific dishonesty that would mislead a reviewer.
func TestWalkthroughStatesWhatItDidNotProve(t *testing.T) {
	var out bytes.Buffer
	if err := runWalkthrough(t.Context(), []string{"--offline"}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "did NOT prove") {
		t.Fatal("walkthrough no longer states its limits")
	}
	if !strings.Contains(text, "prepared Linux host") {
		t.Error("walkthrough does not say the real trade needs the prepared host")
	}
	// It must not imply it performed or could perform a trade.
	for _, forbidden := range []string{"trade executed", "swap completed", "funds moved"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Errorf("walkthrough implies it traded: %q", forbidden)
		}
	}
	if strings.Contains(strings.ToLower(text), "disposable wallet") {
		t.Error("walkthrough contradicts the dedicated limited-risk wallet model")
	}
}

func TestWalkthroughRejectsArguments(t *testing.T) {
	var out bytes.Buffer
	if err := runWalkthrough(t.Context(), []string{"extra"}, &out); err == nil {
		t.Fatal("walkthrough accepted a stray argument")
	}
}

func TestPublicAccountRejectsUntrustedRPCFailures(t *testing.T) {
	original := walkthroughHTTP
	t.Cleanup(func() { walkthroughHTTP = original })
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"HTTP failure", http.StatusBadGateway, `{"jsonrpc":"2.0","id":1,"result":{}}`},
		{"RPC failure", http.StatusOK, `{"jsonrpc":"2.0","id":1,"error":{"code":-32005}}`},
		{"wrong response ID", http.StatusOK, `{"jsonrpc":"2.0","id":2,"result":{}}`},
		{"oversized response", http.StatusOK, strings.Repeat(" ", walkthroughRPCResponseLimit+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			walkthroughHTTP = &http.Client{Transport: walletRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: test.status,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(test.body)),
				}, nil
			})}
			if _, err := publicAccount(t.Context(), "https://rpc.invalid", "11111111111111111111111111111111"); err == nil {
				t.Fatal("untrusted account response was accepted")
			}
		})
	}
}

func TestPublicAccountClientDoesNotUseAmbientProxyOrRedirect(t *testing.T) {
	client := newPublicAccountHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("public account client can send a protected endpoint through an ambient proxy")
	}
	if client.CheckRedirect == nil {
		t.Fatal("public account client can follow a provider redirect")
	}
	request, err := http.NewRequest(http.MethodGet, "https://rpc.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect check = %v", err)
	}
}

func TestPublicAccountAcceptsTheStandardSolanaAccountShape(t *testing.T) {
	original := walkthroughHTTP
	t.Cleanup(func() { walkthroughHTTP = original })
	walkthroughHTTP = &http.Client{Transport: walletRoundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":42},"value":{` +
			`"data":["AQI=","base64"],"executable":false,"lamports":1,` +
			`"owner":"11111111111111111111111111111111","rentEpoch":0,"space":2}}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	account, err := publicAccount(
		t.Context(), "https://rpc.invalid", "11111111111111111111111111111111",
	)
	if err != nil {
		t.Fatal(err)
	}
	if account.ContextSlot != 42 || account.Owner != "11111111111111111111111111111111" ||
		!bytes.Equal(account.Data, []byte{1, 2}) {
		t.Fatalf("account = %+v", account)
	}
}
