package main

import (
	"encoding/base64"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWalletVerificationSessionRejectsMalformedInput(t *testing.T) {
	valid := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"p":4321,"t":"/0123456789abcdef0123456789abcdef"}`))
	if _, err := decodeWalletVerificationSession(valid); err != nil {
		t.Fatalf("valid session was rejected: %v", err)
	}
	for name, document := range map[string]string{
		"wrong version": `{"v":2,"p":4321,"t":"/0123456789abcdef0123456789abcdef"}`,
		"bad port":      `{"v":1,"p":0,"t":"/0123456789abcdef0123456789abcdef"}`,
		"short token":   `{"v":1,"p":4321,"t":"/0123"}`,
		"unknown field": `{"v":1,"p":4321,"t":"/0123456789abcdef0123456789abcdef","x":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			encoded := base64.RawURLEncoding.EncodeToString([]byte(document))
			if _, err := decodeWalletVerificationSession(encoded); err == nil {
				t.Fatal("invalid session was accepted")
			}
		})
	}
}

func TestWalletVerificationBuildsABoundedLoopbackTunnel(t *testing.T) {
	want := []string{
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=2",
		"-N", "-L", "127.0.0.1:41000:127.0.0.1:42000", "ubuntu@node.example",
	}
	if got := walletVerifySSHArgs(41000, 42000, "ubuntu@node.example"); !reflect.DeepEqual(got, want) {
		t.Fatalf("SSH arguments = %q, want %q", got, want)
	}
}

func TestWalletVerificationCommandDoesNotPrintAPlaceholder(t *testing.T) {
	session := "short-session"
	if got := walletVerifyInvocation("YOUR_SERVER", session); got !=
		"mithril-agent wallet verify --session "+session {
		t.Fatalf("invocation = %q", got)
	}
	want := "mithril-agent wallet verify --ssh ubuntu@node.example --session " + session
	if got := walletVerifyInvocation("ubuntu@node.example", session); got != want {
		t.Fatalf("invocation = %q, want %q", got, want)
	}
}

func TestSSHTargetCannotBecomeAnOption(t *testing.T) {
	for _, target := range []string{"node", "ubuntu@node.example", "node-alias", "[::1]"} {
		if !safeSSHTarget(target) {
			t.Errorf("safe target %q was rejected", target)
		}
	}
	for _, target := range []string{"-oProxyCommand=bad", "node command", "node\ncommand", ""} {
		if safeSSHTarget(target) {
			t.Errorf("unsafe target %q was accepted", target)
		}
	}
}

func TestWalletVerificationStatusRejectsOversizedResponse(t *testing.T) {
	client := &http.Client{Transport: walletRoundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `{"status":"verified"}` + strings.Repeat(" ", walletVerifyResponseLimit)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	if _, err := readWalletVerificationStatus(client, "http://127.0.0.1/status"); err == nil {
		t.Fatal("an oversized wallet verification response was accepted")
	}
}

func TestWalletVerificationStopsWhenTheRemoteSessionExpires(t *testing.T) {
	client := &http.Client{Transport: walletRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"status":"waiting"}`)),
		}, nil
	})}
	tunnelDone := make(chan error)
	started := time.Now()
	err := waitForWalletVerification(
		t.Context(), client, "http://127.0.0.1/status", tunnelDone, 20*time.Millisecond,
	)
	if err == nil || !strings.Contains(err.Error(), "expired") || time.Since(started) > time.Second {
		t.Fatalf("session timeout err=%v elapsed=%s", err, time.Since(started))
	}
}
