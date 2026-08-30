package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestRunRequiresFixedCredentialAndActivatedListener(t *testing.T) {
	for _, args := range [][]string{nil, {"--credential", "other"}, {"--credential", paperCredentialName, "extra"}} {
		if err := run(t.Context(), args, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted args %v", args)
		}
	}
	t.Setenv("CREDENTIALS_DIRECTORY", "/run/credentials/test")
	previous := activatedListener
	activatedListener = func() (net.Listener, error) { return nil, errors.New("activation unavailable") }
	t.Cleanup(func() { activatedListener = previous })
	if err := run(t.Context(), []string{"--credential", paperCredentialName}, &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), "activation unavailable") {
		t.Fatalf("activation error = %v", err)
	}
}

func TestRunHelpDoesNotOpenActivatedSocket(t *testing.T) {
	previous := activatedListener
	called := false
	activatedListener = func() (net.Listener, error) {
		called = true
		return nil, errors.New("unexpected")
	}
	t.Cleanup(func() { activatedListener = previous })
	var output bytes.Buffer
	if err := run(t.Context(), []string{"-h"}, &output); err != nil || called ||
		!strings.Contains(output.String(), "Usage:") {
		t.Fatalf("help=%q called=%v err=%v", output.String(), called, err)
	}
}

func TestSystemdListenerRejectsMismatchedActivation(t *testing.T) {
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "1")
	t.Setenv("LISTEN_FDNAMES", "wrong-name")
	if _, err := systemdUnixListener(); err == nil {
		t.Fatal("mismatched activated socket accepted")
	}
}
