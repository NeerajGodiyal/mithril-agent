package submitterclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/submittertransport"
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

func TestPrepareResponseRejectsFailuresWithData(t *testing.T) {
	actionID := strings.Repeat("a", 64)
	for name, response := range map[string]submittertransport.Response{
		"failed with action": {
			Status: submittertransport.StatusFailed, ActionID: actionID,
		},
		"wrong successful action": {
			Status: submittertransport.StatusOK, ActionID: strings.Repeat("b", 64),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validatePrepareResponse(response, actionID); err == nil {
				t.Fatal("invalid Mainnet prepare response was accepted")
			}
		})
	}
	if err := validatePrepareResponse(submittertransport.Response{
		Status: submittertransport.StatusFailed,
	}, actionID); err == nil || err.Error() != "Mainnet submitter service failed" {
		t.Fatalf("bounded Mainnet prepare failure = %v", err)
	}
}

func TestMainnetReadinessClientIsKeylessAndBounded(t *testing.T) {
	command := submitterTestWrapper(t)
	policy := filepath.Join(filepath.Dir(command), "submitter-policy.json")
	actionID := strings.Repeat("a", 64)
	environment := []string{
		"MITHRIL_AGENT_SUBMITTERCLIENT_HELPER=1",
		"MITHRIL_AGENT_SUBMITTERCLIENT_MODE=readiness",
		"MITHRIL_AGENT_SUBMITTERCLIENT_ACTION=" + actionID,
		"MITHRIL_AGENT_MITHRIL_RPC_URL=local-node",
		"MITHRIL_AGENT_PRIMARY_RPC_URL=primary",
		"MITHRIL_AGENT_SECONDARY_RPC_URL=secondary",
	}
	got, err := CheckMainnetReadiness(t.Context(), command, policy, environment)
	if err != nil || got != actionID {
		t.Fatalf("Mainnet readiness = %q, %v", got, err)
	}

	for _, mode := range []string{"malformed", "oversize", "mismatch"} {
		t.Run(mode, func(t *testing.T) {
			bad := append([]string(nil), environment...)
			bad[1] = "MITHRIL_AGENT_SUBMITTERCLIENT_MODE=" + mode
			if _, err := CheckMainnetReadiness(t.Context(), command, policy, bad); err == nil {
				t.Fatal("invalid Mainnet readiness response was accepted")
			}
		})
	}
}

func TestClientValidatesConfiguredControlMode(t *testing.T) {
	devnet, err := New(Config{SocketPath: "/tmp/devnet.sock"})
	if err != nil {
		t.Fatal(err)
	}
	mainnet, err := New(Config{
		SocketPath: "/tmp/mainnet.sock", ControlMode: control.ModeMainnetCanary,
	})
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().UTC().Add(time.Hour)
	devnetStatus := control.Status{
		Mode: control.ModeDevnetEnabled, ExpiresAt: expiresAt,
		MaxActions: 1, RemainingActions: 1,
	}
	mainnetStatus := control.Status{
		Mode: control.ModeMainnetCanary, ExpectedActionID: strings.Repeat("a", 64),
		ExpiresAt:  expiresAt,
		MaxActions: 1, RemainingActions: 1,
	}
	if devnet.validateControlStatus(devnetStatus) != nil ||
		mainnet.validateControlStatus(mainnetStatus) != nil {
		t.Fatal("configured control mode rejected its own status")
	}
	if devnet.validateControlStatus(mainnetStatus) == nil ||
		mainnet.validateControlStatus(devnetStatus) == nil {
		t.Fatal("configured control mode accepted another mode")
	}
	issuedAt := time.Now().UTC()
	if err := devnet.EnableMainnetCanary(
		strings.Repeat("d", 64), strings.Repeat("a", 64),
		issuedAt, issuedAt.Add(time.Minute), "wrong client mode",
	); err == nil || !strings.Contains(err.Error(), "Mainnet submitter client") {
		t.Fatalf("Devnet client Mainnet enable = %v", err)
	}
	if err := mainnet.Enable(
		strings.Repeat("d", 64), issuedAt, issuedAt.Add(time.Minute), 1, "wrong client mode",
	); err == nil || !strings.Contains(err.Error(), "Devnet submitter client") {
		t.Fatalf("Mainnet client Devnet enable = %v", err)
	}
	if _, err := New(Config{
		SocketPath: "/tmp/invalid.sock", ControlMode: "other",
	}); err == nil {
		t.Fatal("unknown control mode was accepted")
	}
}

func TestSubmitterOperationsAlwaysHaveABoundedContext(t *testing.T) {
	var nilContext context.Context
	if _, _, err := boundedOperationContext(nilContext); err == nil {
		t.Fatal("nil submitter context was accepted")
	}
	ctx, cancel, err := boundedOperationContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > controlTimeout {
		t.Fatalf("submitter deadline = %v, present=%t", deadline, ok)
	}
}

func TestClientUsesBoundedSubmitterSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket test")
	}
	directory, err := os.MkdirTemp("/tmp", "submitterclient-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "submitter.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}

	actionID := strings.Repeat("a", 64)
	signature := solana.Encode(make([]byte, 64))
	signed := signer.Response{
		Signature: signature, BlockhashContextSlot: 50, LastValidBlockHeight: 100,
	}
	serverErrors := make(chan error, 13)
	go func() {
		submissions := 0
		for range 13 {
			connection, err := listener.Accept()
			if err != nil {
				serverErrors <- err
				return
			}
			requestData, err := io.ReadAll(connection)
			if err != nil {
				_ = connection.Close()
				serverErrors <- err
				return
			}
			var request submittertransport.Request
			if err := json.Unmarshal(requestData, &request); err != nil {
				_ = connection.Close()
				serverErrors <- err
				return
			}
			response := submittertransport.Response{
				Version: submittertransport.Version, Status: submittertransport.StatusOK,
			}
			switch request.Operation {
			case submittertransport.OperationIdentity:
				response.Identity = &submittertransport.Identity{
					PublicKey: strings.Repeat("1", 64), ProfileFingerprint: strings.Repeat("f", 64),
					Source: "11111111111111111111111111111111",
				}
			case submittertransport.OperationStatus:
				response.Control = &control.Status{Mode: control.ModeNoNewActions}
			case submittertransport.OperationOperatorStatus:
				response.Control = &control.Status{Mode: control.ModeNoNewActions}
				response.Revision = strings.Repeat("d", 64)
			case submittertransport.OperationOperatorSnapshot:
				response.Identity = &submittertransport.Identity{
					PublicKey: strings.Repeat("1", 64), ProfileFingerprint: strings.Repeat("f", 64),
					Source: "11111111111111111111111111111111",
				}
				response.Control = &control.Status{Mode: control.ModeNoNewActions}
				response.Revision = strings.Repeat("e", 64)
			case submittertransport.OperationSubmit:
				response.Submission = &txflow.Submission{
					Signature: signature, LastValidBlockHeight: signed.LastValidBlockHeight,
					State: txflow.StateAccepted,
				}
				submissions++
				if submissions == 1 {
					response.Revision = strings.Repeat("0", 64)
				}
			case submittertransport.OperationPrepare:
				if request.SignerRequest != nil {
					response.ActionID = request.SignerRequest.ActionID
				}
			case submittertransport.OperationEnable:
				if request.ActionID != actionID || request.MaxActions != 1 ||
					request.ExpectedRevision != strings.Repeat("d", 64) {
					serverErrors <- errors.New("Mainnet enable request is not action-bound")
					_ = connection.Close()
					return
				}
			case submittertransport.OperationLatch:
				response.ActionID, response.Outcome = actionID, "failed"
			case submittertransport.OperationStop:
				response.Status = submittertransport.StatusRecoveryPending
			}
			if err := json.NewEncoder(connection).Encode(response); err != nil {
				_ = connection.Close()
				serverErrors <- err
				return
			}
			serverErrors <- connection.Close()
		}
	}()

	client, err := New(Config{SocketPath: path})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := client.Identity(t.Context())
	if err != nil || identity.ProfileFingerprint != strings.Repeat("f", 64) {
		t.Fatalf("identity = %+v, %v", identity, err)
	}
	status, err := client.Status()
	if err != nil || status.Mode != control.ModeNoNewActions {
		t.Fatalf("status = %+v, %v", status, err)
	}
	status, revision, err := client.OperatorStatus()
	if err != nil || status.Mode != control.ModeNoNewActions ||
		revision != strings.Repeat("d", 64) {
		t.Fatalf("operator status = %+v, %q, %v", status, revision, err)
	}
	mainnetClient, err := New(Config{
		SocketPath: path, ControlMode: control.ModeMainnetCanary,
	})
	if err != nil {
		t.Fatal(err)
	}
	issuedAt := time.Now().UTC()
	if err := mainnetClient.EnableMainnetCanary(
		revision, actionID, issuedAt, issuedAt.Add(time.Minute), "reviewed canary",
	); err != nil {
		t.Fatalf("Mainnet enable = %v", err)
	}
	operatorIdentity, status, revision, err := client.OperatorSnapshot()
	if err != nil || status.Mode != control.ModeNoNewActions ||
		operatorIdentity.ProfileFingerprint != strings.Repeat("f", 64) ||
		revision != strings.Repeat("e", 64) {
		t.Fatalf("operator snapshot = %+v, %+v, %q, %v", operatorIdentity, status, revision, err)
	}
	called := 0
	if blocked, err := client.WithSendBarrier(actionID, func() error { called++; return nil }); err != nil || blocked {
		t.Fatalf("reserve = %t, %v", blocked, err)
	}
	if blocked, err := client.WithRecoverySendBarrier(actionID, func() error { called++; return nil }); err != nil || blocked {
		t.Fatalf("recover = %t, %v", blocked, err)
	}
	if called != 2 {
		t.Fatalf("barrier operations called %d times", called)
	}
	mainnetRequest := signer.Request{Cluster: "mainnet-beta", ActionID: actionID}
	if err := client.PrepareJupiter(
		t.Context(), mainnetRequest, signer.Response{ActionID: actionID},
	); err != nil {
		t.Fatalf("Mainnet prepare = %v", err)
	}
	if err := client.StopPreservingRecovery("service_stop"); !errors.Is(err, control.ErrRecoveryPending) {
		t.Fatalf("pending stop = %v", err)
	}
	if err := client.StopForTerminal(actionID, "failed"); err != nil {
		t.Fatal(err)
	}
	latchAction, outcome, err := client.TerminalLatch()
	if err != nil || latchAction != actionID || outcome != "failed" {
		t.Fatalf("latch = %q %q, %v", latchAction, outcome, err)
	}
	if _, err := client.Submit(t.Context(), signed, 50); err == nil {
		t.Fatal("accepted a submission response containing unrelated control data")
	}
	submission, err := client.Submit(t.Context(), signed, 50)
	if err != nil || submission.Signature != signature {
		t.Fatalf("submission = %+v, %v", submission, err)
	}
	for range 13 {
		if err := <-serverErrors; err != nil {
			t.Fatal(err)
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

	t.Run("Mainnet prepare", func(t *testing.T) {
		client := newSubmitterTestClient(t, "normal")
		actionID := strings.Repeat("a", 64)
		if err := client.PrepareJupiter(
			t.Context(),
			signer.Request{Cluster: "mainnet-beta", ActionID: actionID},
			signer.Response{ActionID: actionID},
		); err != nil {
			t.Fatal(err)
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
		if arg == "--prepare-mainnet" {
			var request PrepareRequest
			if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
				os.Exit(2)
			}
			_ = json.NewEncoder(os.Stdout).Encode(submittertransport.Response{
				Version: submittertransport.Version, Status: submittertransport.StatusOK,
				ActionID: request.SignerRequest.ActionID,
			})
			os.Exit(0)
		}
		if arg == "--check-mainnet" {
			for _, candidate := range os.Args {
				if candidate == "--key" {
					os.Exit(3)
				}
			}
			actionID := os.Getenv("MITHRIL_AGENT_SUBMITTERCLIENT_ACTION")
			if mode == "mismatch" {
				actionID = strings.Repeat("b", 63)
			}
			_ = json.NewEncoder(os.Stdout).Encode(submittertransport.Response{
				Version: submittertransport.Version, Status: submittertransport.StatusOK,
				ActionID: actionID,
			})
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
