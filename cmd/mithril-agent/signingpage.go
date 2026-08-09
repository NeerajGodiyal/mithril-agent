package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const signingPageName = "sign-destination-proof.html"

// embeddedSigningPage keeps the wallet ceremony in the executable that serves
// it. Installation cannot accidentally omit or mismatch a security-sensitive
// page while the source file remains directly reviewable.
//
//go:embed sign-destination-proof.html
var embeddedSigningPage string

// openBrowser opens a local file or loopback URL on the operator's desktop.
// It never falls back to a shell, so the target cannot become a command.
func openBrowser(ctx context.Context, target string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "/usr/bin/open", []string{target}
	case "linux":
		name, args = "/usr/bin/xdg-open", []string{target}
	default:
		return fmt.Errorf("no known opener for %s", runtime.GOOS)
	}
	if _, err := os.Stat(name); err != nil {
		return fmt.Errorf("%s is not available", filepath.Base(name))
	}
	timed, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(timed, name, args...).Run()
}

// hostnameForCopy names this machine the way the OPERATOR can reach it.
//
// os.Hostname() is the obvious answer and the wrong one: it returns the name
// the server calls itself ("mithril-node"), which the operator's laptop
// generally cannot resolve. Pasting a tunnel command built from it fails with a
// DNS error that looks like the tool is broken.
//
// SSH_CONNECTION is the right source, and it is evidence rather than a guess:
// sshd sets it to "client-ip client-port SERVER-IP server-port", and the server
// address in it is by definition one the client just connected to
// successfully. The login name comes from SUDO_USER when a service account is
// in the middle, because that is who owns the SSH session.
//
// When there is no SSH session — a desktop, or a shell that lost the variable —
// a placeholder is better than a plausible-looking name that will not resolve.
func hostnameForCopy() string {
	fields := strings.Fields(os.Getenv("SSH_CONNECTION"))
	if len(fields) < 3 || fields[2] == "" {
		return "YOUR_SERVER"
	}
	address := fields[2]
	target := address
	if login := sshLoginName(); login != "" {
		target = login + "@" + address
	}
	// This string is printed for the operator to paste into a shell. Treat the
	// environment as untrusted and fall back to a visible placeholder rather
	// than printing shell syntax as part of the suggested SSH target.
	if !safeSSHTarget(target) {
		return "YOUR_SERVER"
	}
	return target
}

// sshLoginName is who owns the SSH session, which is not necessarily who this
// process runs as: a service account reached through sudo still belongs to the
// operator who logged in.
func sshLoginName() string {
	for _, name := range []string{"SUDO_USER", "LOGNAME", "USER"} {
		if value := os.Getenv(name); value != "" && value != "root" {
			return value
		}
	}
	return ""
}
