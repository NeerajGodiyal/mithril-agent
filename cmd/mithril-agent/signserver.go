package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// The wallet page is exposed only through a short-lived loopback listener. The
// server verifies the returned signature before accepting it.
const (
	signServeTimeout   = 15 * time.Minute
	signServePathSize  = 16
	signMaxHeaderBytes = 8 << 10
)

const walletVerificationSessionVersion = 1

// walletVerificationSession is the non-secret routing information a local
// helper needs to automate the SSH tunnel. The random path remains the
// capability; the remote server still verifies the wallet signature itself.
type walletVerificationSession struct {
	Version    int    `json:"v"`
	RemotePort int    `json:"p"`
	Path       string `json:"t"`
}

// signatureCollector serves the signing page and waits for one verified
// signature.
type signatureCollector struct {
	challenge string
	pagePath  string
	verify    func(signature string) error

	path     string
	cspNonce string
	listener net.Listener
	done     chan string
	observed chan struct{}
	verified atomic.Bool
}

func newSignatureCollector(
	challenge, pagePath string, verify func(string) error,
) (*signatureCollector, error) {
	token := make([]byte, 2*signServePathSize)
	if _, err := rand.Read(token); err != nil {
		return nil, errors.New("generate a private URL for the signing page")
	}
	// Loopback only. A signing page reachable from the network is a phishing
	// surface, and nothing here needs to be.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on loopback: %w", err)
	}
	return &signatureCollector{
		challenge: challenge,
		pagePath:  pagePath,
		verify:    verify,
		path:      "/" + hex.EncodeToString(token[:signServePathSize]),
		cspNonce:  hex.EncodeToString(token[signServePathSize:]),
		listener:  listener,
		done:      make(chan string, 1),
		observed:  make(chan struct{}, 1),
	}, nil
}

func (c *signatureCollector) port() int {
	return c.listener.Addr().(*net.TCPAddr).Port
}

// url is what the operator opens once their tunnel is up.
func (c *signatureCollector) url() string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", c.port(), c.path)
}

// tunnelCommand is the one line that connects their laptop to this listener.
func (c *signatureCollector) tunnelCommand(host string) string {
	return fmt.Sprintf("ssh -N -L %d:127.0.0.1:%d %s", c.port(), c.port(), host)
}

// session encodes only the remote listener coordinates. It contains no key,
// signature, RPC URL, or wallet address and expires with the collector.
func (c *signatureCollector) session() (string, error) {
	document, err := json.Marshal(walletVerificationSession{
		Version:    walletVerificationSessionVersion,
		RemotePort: c.port(),
		Path:       c.path,
	})
	if err != nil {
		return "", errors.New("encode the wallet verification session")
	}
	return base64.RawURLEncoding.EncodeToString(document), nil
}

// collect serves until a verified signature arrives, the context ends, or the
// timeout expires. It returns the signature, never a partial result.
func (c *signatureCollector) collect(ctx context.Context) (string, error) {
	page := []byte(embeddedSigningPage)
	if c.pagePath != "" {
		var err error
		page, err = os.ReadFile(c.pagePath)
		if err != nil {
			return "", fmt.Errorf("read the signing page: %w", err)
		}
	}
	body := injectChallenge(string(page), c.challenge, c.path, c.cspNonce)

	mux := http.NewServeMux()
	mux.HandleFunc(c.path, func(w http.ResponseWriter, r *http.Request) {
		setSigningHeaders(w, c.cspNonce)
		if r.Method != http.MethodGet {
			http.Error(w, "get the signing page", http.StatusMethodNotAllowed)
			return
		}
		if !loopbackRequest(r) {
			http.Error(w, "loopback requests only", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, body)
	})
	mux.HandleFunc(c.path+"/signature", func(w http.ResponseWriter, r *http.Request) {
		setSigningHeaders(w, c.cspNonce)
		if r.Method != http.MethodPost {
			http.Error(w, "post the signature", http.StatusMethodNotAllowed)
			return
		}
		if !loopbackRequest(r) || !sameOriginRequest(r) {
			http.Error(w, "same-origin loopback requests only", http.StatusForbidden)
			return
		}
		mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
		if mediaType != "application/json" {
			http.Error(w, "post JSON", http.StatusUnsupportedMediaType)
			return
		}
		var payload struct {
			Signature string `json:"signature"`
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			http.Error(w, "unreadable signature", http.StatusBadRequest)
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			http.Error(w, "unreadable signature", http.StatusBadRequest)
			return
		}
		signature := strings.TrimSpace(payload.Signature)
		// Verified here, not merely received: a POST is no more trusted than a
		// pasted string, and the same check rejects both.
		if err := c.verify(signature); err != nil {
			http.Error(w, "the signature does not match the challenge", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"accepted":true}`)
		c.verified.Store(true)
		select {
		case c.done <- signature:
		default:
		}
	})
	mux.HandleFunc(c.path+"/status", func(w http.ResponseWriter, r *http.Request) {
		setSigningHeaders(w, c.cspNonce)
		if r.Method != http.MethodGet {
			http.Error(w, "get the verification status", http.StatusMethodNotAllowed)
			return
		}
		if !loopbackRequest(r) {
			http.Error(w, "loopback requests only", http.StatusForbidden)
			return
		}
		status := "waiting"
		if c.verified.Load() {
			status = "verified"
			select {
			case c.observed <- struct{}{}:
			default:
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Status string `json:"status"`
		}{Status: status})
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    signMaxHeaderBytes,
	}
	go func() { _ = server.Serve(c.listener) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()

	timeout := time.NewTimer(signServeTimeout)
	defer timeout.Stop()
	select {
	case signature := <-c.done:
		// Keep the status endpoint alive until the desktop helper observes the
		// accepted signature. Without this handshake the remote setup closes the
		// listener first, leaving the helper's SSH tunnel printing connection
		// failures even though verification succeeded.
		select {
		case <-c.observed:
		case <-time.After(2 * time.Second):
		}
		return signature, nil
	case <-timeout.C:
		return "", fmt.Errorf("no signature arrived within %s", signServeTimeout)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// injectChallenge fills the challenge in and wires the signature back, without
// editing the page on disk. The page stays a plain file anyone can audit and
// still use by hand; this only adds what the served copy needs.
func injectChallenge(page, challenge, basePath, nonce string) string {
	page = strings.ReplaceAll(page, "<style>", `<style nonce="`+nonce+`">`)
	page = strings.ReplaceAll(page, "<script>", `<script nonce="`+nonce+`">`)
	script := fmt.Sprintf(`
<script nonce=%s>
// Served by mithril-agent over a loopback tunnel: fill the challenge the setup
// command generated, and return the signature so nobody has to copy it.
(function () {
	  document.body.classList.add("served");
  var challenge = %s;
  var post = %s + "/signature";
  function fill() {
    var box = document.getElementById("challenge");
    if (!box) { return false; }
    box.value = challenge;
    box.dispatchEvent(new Event("input", { bubbles: true }));
    return true;
  }
  if (!fill()) { document.addEventListener("DOMContentLoaded", fill); }
  var out = document.getElementById("signature");
  if (!out) { return; }
  var sent = "";
  new MutationObserver(function () {
    var value = (out.textContent || "").trim();
    if (!value || value === "—" || value === sent) { return; }
    sent = value;
    fetch(post, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ signature: value })
    }).then(function (r) {
      var note = document.getElementById("status");
      if (note) {
        note.textContent = r.ok
          ? "Signature sent back to the setup command — you can close this tab."
          : "The setup command rejected that signature.";
      }
    });
  }).observe(out, { childList: true, characterData: true, subtree: true });
})();
</script>`, jsonString(nonce), jsonString(challenge), jsonString(basePath))
	if index := strings.LastIndex(page, "</body>"); index >= 0 {
		return page[:index] + script + page[index:]
	}
	return page + script
}

func setSigningHeaders(w http.ResponseWriter, nonce string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; script-src 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+
			"'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}

func loopbackRequest(r *http.Request) bool {
	host := r.Host
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	return host == "127.0.0.1" || host == "localhost"
}

func sameOriginRequest(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || origin == "http://"+r.Host
}

// jsonString quotes a value for embedding in JavaScript. encoding/json escapes
// the characters that could otherwise close the script or the string.
func jsonString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}
