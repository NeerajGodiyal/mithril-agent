package submitterclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func TestDecodeProcessFailureRecognizesOnlyStrictControlStop(t *testing.T) {
	if err := decodeProcessFailure([]byte(`{"error":"control_blocked"}`)); !errors.Is(err, submitter.ErrControlBlocked) {
		t.Fatalf("control stop = %v", err)
	}
	for _, data := range [][]byte{
		nil,
		[]byte(`{"error":"other"}`),
		[]byte(`{"error":"control_blocked","extra":true}`),
		[]byte(`{"error":"control_blocked","error":"other"}`),
	} {
		if err := decodeProcessFailure(data); errors.Is(err, submitter.ErrControlBlocked) {
			t.Fatalf("invalid failure %q was treated as a control stop", data)
		}
	}
}

func TestClientProcessBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix test wrapper")
	}
	t.Setenv("UNRELATED_SECRET", "must-not-reach-child")
	response := signer.Response{
		Signature: solana.Encode(make([]byte, 64)), BlockhashContextSlot: 50,
		LastValidBlockHeight: 100,
	}

	t.Run("identity and submit", func(t *testing.T) {
		client := newSubmitterTestClient(t, "normal")
		identity, err := client.Identity(t.Context())
		if err != nil || identity.PublicKey != strings.Repeat("1", 64) {
			t.Fatalf("identity = %+v, %v", identity, err)
		}
		submission, err := client.Submit(t.Context(), response, 50)
		if err != nil || submission.State != txflow.StateAccepted ||
			submission.Signature != response.Signature {
			t.Fatalf("submission = %+v, %v", submission, err)
		}
	})

	t.Run("minimum context mismatch", func(t *testing.T) {
		client := newSubmitterTestClient(t, "normal")
		if _, err := client.Submit(t.Context(), response, 51); err == nil {
			t.Fatal("mismatched minimum context slot reached the submitter")
		}
	})

	for _, mode := range []string{"malformed", "oversize", "mismatch"} {
		t.Run(mode, func(t *testing.T) {
			client := newSubmitterTestClient(t, mode)
			if _, err := client.Submit(t.Context(), response, 50); err == nil {
				t.Fatal("accepted invalid child output")
			}
		})
	}

	t.Run("timeout", func(t *testing.T) {
		client := newSubmitterTestClient(t, "sleep")
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		if _, err := client.Submit(ctx, response, 50); err == nil {
			t.Fatal("child ignored context deadline")
		}
	})

	t.Run("control stop", func(t *testing.T) {
		client := newSubmitterTestClient(t, "control_stop")
		if _, err := client.Submit(t.Context(), response, 50); !errors.Is(err, submitter.ErrControlBlocked) {
			t.Fatalf("control stop = %v", err)
		}
	})

	t.Run("executable drift", func(t *testing.T) {
		client := newSubmitterTestClient(t, "normal")
		if err := os.Chmod(client.config.Command, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Identity(t.Context()); err == nil {
			t.Fatal("executed a command that became replaceable")
		}
	})
}

func TestSubmitterClientHelperProcess(t *testing.T) {
	if os.Getenv("MITHRIL_AGENT_SUBMITTERCLIENT_HELPER") != "1" {
		return
	}
	mode := os.Getenv("MITHRIL_AGENT_SUBMITTERCLIENT_MODE")
	if os.Getenv("UNRELATED_SECRET") != "" {
		mode = "malformed"
	}
	switch mode {
	case "sleep":
		time.Sleep(time.Second)
		return
	case "oversize":
		_, _ = os.Stdout.Write(make([]byte, maxResponseBytes+2))
		os.Exit(0)
	case "malformed":
		_, _ = fmt.Fprint(os.Stdout, `{"unexpected":true}`)
		os.Exit(0)
	case "control_stop":
		_, _ = fmt.Fprint(os.Stdout, `{"error":"control_blocked"}`)
		os.Exit(1)
	}
	for _, arg := range os.Args {
		if arg == "--identity" {
			_, _ = fmt.Fprintf(os.Stdout, `{"public_key":%q}`, strings.Repeat("1", 64))
			os.Exit(0)
		}
	}
	var request Request
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		os.Exit(2)
	}
	signature := request.SignerResponse.Signature
	if mode == "mismatch" {
		signature = solana.Encode(make([]byte, 63))
	}
	_ = json.NewEncoder(os.Stdout).Encode(txflow.Submission{
		Signature:            signature,
		LastValidBlockHeight: request.SignerResponse.LastValidBlockHeight,
		State:                txflow.StateAccepted,
	})
	os.Exit(0)
}

func newSubmitterTestClient(t *testing.T, mode string) *Client {
	t.Helper()
	command := submitterTestWrapper(t)
	client, err := New(Config{
		Command:        command,
		PolicyPath:     filepath.Join(filepath.Dir(command), "policy.json"),
		PrivateKeyPath: filepath.Join(filepath.Dir(command), "key.json"),
		Env: []string{
			"MITHRIL_AGENT_SUBMITTERCLIENT_HELPER=1",
			"MITHRIL_AGENT_SUBMITTERCLIENT_MODE=" + mode,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func submitterTestWrapper(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "submitter-helper")
	contents := "#!/bin/sh\nexec " + strconv.Quote(executable) +
		" -test.run=^TestSubmitterClientHelperProcess$ -- \"$@\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
