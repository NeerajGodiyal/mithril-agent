package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func servedPage(t *testing.T) string {
	t.Helper()
	page := filepath.Join(t.TempDir(), signingPageName)
	body := `<html><body><textarea id="challenge"></textarea><output id="signature">—</output></body></html>`
	if err := os.WriteFile(page, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return page
}

// A signing page reachable from the network is a phishing surface, and nothing
// about this needs to be. The operator opens the tunnel themselves; that is the
// authorisation boundary.
func TestTheSigningServerBindsLoopbackOnly(t *testing.T) {
	collector, err := newSignatureCollector("challenge-text", servedPage(t), func(string) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer collector.listener.Close()

	address := collector.listener.Addr().String()
	if !strings.HasPrefix(address, "127.0.0.1:") {
		t.Fatalf("listening on %q, want loopback only", address)
	}
	if !strings.Contains(collector.url(), "127.0.0.1:") {
		t.Errorf("the URL offered is not loopback: %s", collector.url())
	}
	// The tunnel command must forward the SAME port it is listening on, or the
	// operator forwards nothing useful and the page never loads.
	if !strings.Contains(collector.tunnelCommand("host"),
		"-L "+itoaPort(collector.port())+":127.0.0.1:"+itoaPort(collector.port())) {
		t.Errorf("the tunnel command does not forward the listening port: %s",
			collector.tunnelCommand("host"))
	}
}

// A POST is no more trusted than a pasted string. The same verification must
// reject both, or serving the page would be a way around the check.
func TestAPostedSignatureIsVerifiedNotMerelyReceived(t *testing.T) {
	rejected := errors.New("does not match")
	collector, err := newSignatureCollector("challenge-text", servedPage(t),
		func(signature string) error {
			if signature != "good-signature" {
				return rejected
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	collected := make(chan string, 1)
	go func() {
		signature, err := collector.collect(ctx)
		if err != nil {
			collected <- ""
			return
		}
		collected <- signature
	}()
	waitForServer(t, collector.url())

	// A wrong signature must be refused and must NOT end the wait.
	if status := postSignature(t, collector, "forged"); status != http.StatusBadRequest {
		t.Errorf("a forged signature returned %d, want 400", status)
	}
	select {
	case got := <-collected:
		t.Fatalf("a rejected signature ended the ceremony with %q", got)
	case <-time.After(150 * time.Millisecond):
	}

	if status := postSignature(t, collector, "good-signature"); status != http.StatusOK {
		t.Errorf("a valid signature returned %d, want 200", status)
	}
	select {
	case got := <-collected:
		if got != "good-signature" {
			t.Errorf("collected %q, want the verified signature", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a verified signature never ended the wait")
	}
}

func TestSigningServerWaitsForTheDesktopHelperToObserveSuccess(t *testing.T) {
	collector, err := newSignatureCollector("challenge", servedPage(t), func(string) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	collected := make(chan string, 1)
	go func() {
		signature, _ := collector.collect(t.Context())
		collected <- signature
	}()
	waitForServer(t, collector.url())
	if status := postSignature(t, collector, "good-signature"); status != http.StatusOK {
		t.Fatalf("signature returned %d, want 200", status)
	}
	select {
	case <-collected:
		t.Fatal("the server closed before the desktop helper observed success")
	case <-time.After(100 * time.Millisecond):
	}

	response, err := http.Get(collector.url() + "/status")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	select {
	case got := <-collected:
		if got != "good-signature" {
			t.Fatalf("collected %q, want good-signature", got)
		}
	case <-time.After(time.Second):
		t.Fatal("the server did not close after success was observed")
	}
}

// The challenge must arrive pre-filled: retyping or pasting it is the step this
// exists to remove, and a mistyped challenge produces a signature that verifies
// against nothing.
func TestTheServedPageCarriesTheChallengeAndReturnPath(t *testing.T) {
	const challenge = `mithril-agent sweep destination proof v1|agent:A|destination:B`
	body := injectChallenge(
		`<html><body><textarea id="challenge"></textarea><output id="signature">—</output></body></html>`,
		challenge, "/abc123", "test-nonce")

	if !strings.Contains(body, `"/abc123/signature"`) &&
		!strings.Contains(body, `"/abc123" + "/signature"`) {
		if !strings.Contains(body, "/abc123") {
			t.Error("the served page has no way to return the signature")
		}
	}
	// The challenge has to survive embedding intact, quoting and all.
	if !strings.Contains(body, jsonString(challenge)) {
		t.Errorf("the challenge was not embedded verbatim:\n%s", body)
	}
	// It must be added INSIDE the document, not after it.
	if strings.Index(body, "<script>") > strings.Index(body, "</body>") {
		t.Error("the script was appended after </body>")
	}
}

// A challenge containing script-closing text must not be able to break out of
// the string it is embedded in.
func TestAnEmbeddedChallengeCannotEscapeItsString(t *testing.T) {
	hostile := `x</script><script>alert(1)</script>`
	body := injectChallenge("<html><body></body></html>", hostile, "/p", "test-nonce")
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("a challenge escaped its string and injected markup:\n%s", body)
	}
}

// The page is part of the executable, so a package cannot install the binary
// while silently omitting the wallet ceremony that setup depends on.
func TestServingUsesTheEmbeddedPage(t *testing.T) {
	collector, err := newSignatureCollector("challenge", "", func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer collector.listener.Close()
	if embeddedSigningPage == "" || !strings.Contains(embeddedSigningPage, "Verify your payout wallet") {
		t.Fatal("the embedded wallet page is missing or not the reviewed page")
	}
}

func TestSigningPageUsesRestrictiveBrowserHeaders(t *testing.T) {
	collector, err := newSignatureCollector("challenge", servedPage(t), func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer collector.listener.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _, _ = collector.collect(ctx) }()
	waitForServer(t, collector.url())

	response, err := http.Get(collector.url())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	for _, header := range []string{
		"Content-Security-Policy", "Referrer-Policy", "X-Content-Type-Options",
	} {
		if response.Header.Get(header) == "" {
			t.Errorf("%s is missing", header)
		}
	}
	if policy := response.Header.Get("Content-Security-Policy"); strings.Contains(policy, "unsafe-inline") {
		t.Errorf("CSP permits inline scripts without a nonce: %s", policy)
	}
}

func TestWalletSessionContainsOnlyValidatedRoutingData(t *testing.T) {
	collector, err := newSignatureCollector("private challenge", servedPage(t), func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer collector.listener.Close()
	sessionText, err := collector.session()
	if err != nil {
		t.Fatal(err)
	}
	session, err := decodeWalletVerificationSession(sessionText)
	if err != nil {
		t.Fatal(err)
	}
	if session.RemotePort != collector.port() || session.Path != collector.path {
		t.Fatalf("session = %+v, collector = %d %s", session, collector.port(), collector.path)
	}
	if strings.Contains(sessionText, "challenge") {
		t.Fatal("the session contains the wallet challenge")
	}
}

func itoaPort(port int) string { return strconv.Itoa(port) }

// waitForServer blocks until the served page answers, so a test never races the
// listener coming up.
func waitForServer(t *testing.T, url string) {
	t.Helper()
	for attempt := 0; attempt < 100; attempt++ {
		response, err := http.Get(url)
		if err == nil {
			response.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the signing server never answered")
}

func postSignature(t *testing.T, collector *signatureCollector, signature string) int {
	t.Helper()
	response, err := http.Post(collector.url()+"/signature", "application/json",
		strings.NewReader(`{"signature":`+jsonString(signature)+`}`))
	if err != nil {
		t.Fatalf("post signature: %v", err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

// A better path nobody knows to ask for is not a better path. Serving was first
// added behind --serve, which meant the good flow existed only for someone who
// had already read the source — and the interactive wizard, where this ceremony
// actually happens, still told operators to copy a file and paste twice.
func TestServingIsTheDefaultNotAFlag(t *testing.T) {
	source, err := os.ReadFile("sweep_setup.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	// The opt-in flag must be gone; only an opt-OUT may remain.
	if strings.Contains(text, `flags.Bool("serve"`) {
		t.Error("serving is still opt-in behind --serve")
	}
	if !strings.Contains(text, `flags.Bool("no-serve"`) {
		t.Error("there is no way to turn serving off for a script that needs it off")
	}

	// The interactive wizard must reach the serving path, not the copy-a-file one.
	if !strings.Contains(text, "collectSweepSignature(") {
		t.Fatal("the interactive ceremony does not offer the served page")
	}
	wizard := text[strings.Index(text, "collectSweepSignature("):]
	if end := strings.Index(wizard, "\nfunc "); end > 0 {
		wizard = wizard[:end]
	}

	// And pasting must survive: somebody signing with the Solana CLI should not
	// be made to open a tunnel.
	if !strings.Contains(text, "paste the base58 signature here") {
		t.Error("the keyboard route was removed; CLI signers have no way through")
	}
}

// --json is a machine-readable request. Blocking it on a browser signature
// would hang any caller that only wants the challenge text.
func TestMachineReadableOutputNeverWaitsForABrowser(t *testing.T) {
	source, err := os.ReadFile("sweep_setup.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "!*asJSON") {
		t.Error("--json can be made to block waiting for a wallet")
	}
}
