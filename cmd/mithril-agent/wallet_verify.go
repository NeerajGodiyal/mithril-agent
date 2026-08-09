package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const walletVerifyUsage = `Usage: mithril-agent wallet verify --session SESSION [--ssh TARGET] [--no-open]

Run this on the Mac or Linux desktop that has your browser wallet. It opens a
temporary SSH tunnel to the waiting Mithril Agent setup, opens the local wallet
page, and closes the tunnel after the payout wallet is verified.

TARGET is the same SSH host, alias, or user@host you normally use. If omitted,
the command asks for it. SESSION is the short-lived value printed by remote
setup. It contains no key or wallet address and stops working when setup exits.`

const (
	walletVerifyConnectTimeout = 20 * time.Second
	walletVerifyPollInterval   = 200 * time.Millisecond
	walletVerifyResponseLimit  = 4 << 10
)

var (
	walletVerifyCommand = exec.CommandContext
	walletVerifyOpen    = openBrowser
)

func runWalletVerify(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("wallet verify", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sshTarget := flags.String("ssh", "", "SSH host, alias, or user@host for the agent server")
	sessionText := flags.String("session", "", "short-lived verification session from setup")
	noOpen := flags.Bool("no-open", false, "print the local URL instead of opening a browser")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, walletVerifyUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *sessionText == "" {
		return errors.New("wallet verify requires --session SESSION")
	}
	if *sshTarget == "" {
		if !stdinIsTerminal() {
			return errors.New("wallet verify needs --ssh TARGET when no terminal is available")
		}
		answer, err := newPrompter(os.Stdin, output, true).ask(
			"SSH host or alias for the Mithril server", "")
		if err != nil {
			return err
		}
		*sshTarget = answer
	}
	if !safeSSHTarget(*sshTarget) {
		return errors.New("the SSH target contains unsupported characters")
	}
	session, err := decodeWalletVerificationSession(*sessionText)
	if err != nil {
		return err
	}
	localPort, err := availableLoopbackPort()
	if err != nil {
		return err
	}

	tunnelCtx, stopTunnel := context.WithCancel(ctx)
	defer stopTunnel()
	command := walletVerifyCommand(tunnelCtx, "ssh",
		walletVerifySSHArgs(localPort, session.RemotePort, *sshTarget)...)
	command.Stdin = os.Stdin
	command.Stdout = io.Discard
	command.Stderr = output
	if err := command.Start(); err != nil {
		return fmt.Errorf("start SSH tunnel: %w", err)
	}
	tunnelDone := make(chan error, 1)
	go func() { tunnelDone <- command.Wait() }()

	url := "http://127.0.0.1:" + strconv.Itoa(localPort) + session.Path
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: (&net.Dialer{
				Timeout:   2 * time.Second,
				KeepAlive: -1,
			}).DialContext,
			DisableKeepAlives: true,
		},
	}
	if err := waitForWalletPage(ctx, client, url, tunnelDone); err != nil {
		stopTunnel()
		return err
	}
	if _, err := fmt.Fprintln(output,
		"Wallet verification is ready. This only proves where profit may be sent; it cannot spend from that wallet."); err != nil {
		stopTunnel()
		return err
	}
	if *noOpen {
		if _, err := fmt.Fprintln(output, "Open in the browser that has Phantom or Solflare:\n  "+url); err != nil {
			stopTunnel()
			return err
		}
	} else if err := walletVerifyOpen(ctx, url); err != nil {
		if _, writeErr := fmt.Fprintf(output,
			"Could not open a browser automatically: %v\nOpen this URL instead:\n  %s\n", err, url); writeErr != nil {
			stopTunnel()
			return writeErr
		}
	}

	if err := waitForWalletVerification(ctx, client, url+"/status", tunnelDone); err != nil {
		stopTunnel()
		return err
	}
	stopTunnel()
	select {
	case <-tunnelDone:
	case <-time.After(2 * time.Second):
	}
	_, err = fmt.Fprintln(output, "Payout wallet verified. Return to the setup terminal.")
	return err
}

func walletVerifyInvocation(target, session string) string {
	if target == "" || target == "YOUR_SERVER" {
		return "mithril-agent wallet verify --session " + session
	}
	return "mithril-agent wallet verify --ssh " + target + " --session " + session
}

func walletVerifySSHArgs(localPort, remotePort int, target string) []string {
	forward := "127.0.0.1:" + strconv.Itoa(localPort) + ":127.0.0.1:" +
		strconv.Itoa(remotePort)
	return []string{
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=2",
		"-N", "-L", forward, target,
	}
}

func decodeWalletVerificationSession(text string) (walletVerificationSession, error) {
	encoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(text))
	if err != nil || len(encoded) > 512 {
		return walletVerificationSession{}, errors.New("the wallet verification session is invalid")
	}
	var session walletVerificationSession
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&session); err != nil {
		return walletVerificationSession{}, errors.New("the wallet verification session is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return walletVerificationSession{}, errors.New("the wallet verification session is invalid")
	}
	if session.Version != walletVerificationSessionVersion ||
		session.RemotePort < 1 || session.RemotePort > 65535 || !validWalletSessionPath(session.Path) {
		return walletVerificationSession{}, errors.New("the wallet verification session is invalid")
	}
	return session, nil
}

func validWalletSessionPath(path string) bool {
	if len(path) != 1+2*signServePathSize || path[0] != '/' {
		return false
	}
	_, err := hex.DecodeString(path[1:])
	return err == nil
}

func safeSSHTarget(target string) bool {
	if target == "" || target[0] == '-' {
		return false
	}
	for _, char := range target {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || strings.ContainsRune("@._-:[]", char) {
			continue
		}
		return false
	}
	return true
}

func availableLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, errors.New("find an available local port")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, errors.New("release the temporary local port")
	}
	return port, nil
}

func waitForWalletPage(
	ctx context.Context,
	client *http.Client,
	url string,
	tunnelDone <-chan error,
) error {
	deadline := time.NewTimer(walletVerifyConnectTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(walletVerifyPollInterval)
	defer ticker.Stop()
	for {
		if walletPageReady(client, url) {
			return nil
		}
		select {
		case err := <-tunnelDone:
			if err == nil {
				return errors.New("the SSH tunnel ended before the wallet page was ready")
			}
			return fmt.Errorf("the SSH tunnel ended before the wallet page was ready: %w", err)
		case <-deadline.C:
			return errors.New("the wallet page did not become ready; check the SSH target and that remote setup is still waiting")
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func walletPageReady(client *http.Client, url string) bool {
	response, err := client.Get(url)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, walletVerifyResponseLimit))
	return response.StatusCode == http.StatusOK
}

func waitForWalletVerification(
	ctx context.Context,
	client *http.Client,
	statusURL string,
	tunnelDone <-chan error,
) error {
	ticker := time.NewTicker(walletVerifyPollInterval)
	defer ticker.Stop()
	for {
		status, err := readWalletVerificationStatus(client, statusURL)
		if err == nil && status == "verified" {
			return nil
		}
		select {
		case err := <-tunnelDone:
			if err == nil {
				return errors.New("wallet verification ended before a signature was accepted")
			}
			return fmt.Errorf("wallet verification ended before a signature was accepted: %w", err)
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func readWalletVerificationStatus(client *http.Client, url string) (string, error) {
	response, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("wallet verification status is unavailable")
	}
	var result struct {
		Status string `json:"status"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, walletVerifyResponseLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return "", errors.New("wallet verification status is unreadable")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", errors.New("wallet verification status is unreadable")
	}
	if result.Status != "waiting" && result.Status != "verified" {
		return "", errors.New("wallet verification returned an unknown status")
	}
	return result.Status, nil
}
