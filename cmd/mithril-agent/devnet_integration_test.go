package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func TestDevnetOnceComposesRealProcessesAndRPCClients(t *testing.T) {
	if testing.Short() {
		t.Skip("builds command processes")
	}
	if runtime.GOOS != "linux" {
		t.Skip("devnet execution requires Linux kernel clock evidence")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	if err := os.Chmod(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	signerCommand := filepath.Join(temp, "mithril-agent-signer")
	policyCommand := filepath.Join(temp, "mithril-agent-policy")
	submitterCommand := filepath.Join(temp, "mithril-agent-submitter")
	mcpCommand := filepath.Join(temp, "mcpobserve.test")
	buildCommand(t, root, signerCommand, "go", "build", "-o", signerCommand, "./cmd/mithril-agent-signer")
	buildCommand(t, root, policyCommand, "go", "build", "-o", policyCommand, "./cmd/mithril-agent-policy")
	buildCommand(t, root, submitterCommand, "go", "build", "-o", submitterCommand, "./cmd/mithril-agent-submitter")
	buildCommand(t, root, mcpCommand, "go", "test", "-c", "-o", mcpCommand, "./mcpobserve")

	sourceSeed := sha256.Sum256([]byte("integration source"))
	sourceKey := ed25519.NewKeyFromSeed(sourceSeed[:])
	source := solana.Encode(sourceKey.Public().(ed25519.PublicKey))
	destinationSeed := sha256.Sum256([]byte("integration destination"))
	destination := solana.Encode(
		ed25519.NewKeyFromSeed(destinationSeed[:]).Public().(ed25519.PublicKey),
	)
	profile := agent.Profile{
		Name:                         agent.ProfileTreasurySweepV1,
		Version:                      1,
		Cluster:                      "devnet",
		Source:                       source,
		Destination:                  destination,
		ReserveLamports:              100,
		MinTransferLamports:          10,
		MaxTransferLamports:          20,
		DailyCapLamports:             30,
		MaxFeeLamports:               5,
		ScheduleWindowSeconds:        3_600,
		ScheduleAnchorUnix:           time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		MaxClockUncertaintyMillis:    2_000,
		MaxObservationAgeSeconds:     30,
		MinHealthyObservationSeconds: 5,
		MinHealthySlotAdvance:        1,
		MaxNodeLagSlots:              150,
		MaxReconciliationSeconds:     180,
	}
	profileFingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	policy := signer.Policy{
		Cluster:                 "devnet",
		Profile:                 agent.ProfileTreasurySweepV1,
		ProfileVersion:          1,
		ProfileFingerprint:      profileFingerprint,
		Source:                  source,
		Destination:             destination,
		MaxLamports:             20,
		MaxFeeLamports:          5,
		DailyDebitCapLamports:   profile.DailyCapLamports,
		AuthorizationLedgerPath: filepath.Join(temp, "authorization.jsonl"),
		ScheduleWindowSeconds:   profile.ScheduleWindowSeconds,
		ScheduleAnchorUnix:      profile.ScheduleAnchorUnix,
		MaxBlockHeightWindow:    200,
	}
	authoritySeed := sha256.Sum256([]byte("integration risk authority"))
	authorityKey := ed25519.NewKeyFromSeed(authoritySeed[:])
	authorityPublic, err := riskgrant.PublicKeyHex(authorityKey)
	if err != nil {
		t.Fatal(err)
	}
	submitterSeed := sha256.Sum256([]byte("integration submitter"))
	submitterPrivateKey := hex.EncodeToString(submitterSeed[:])
	submitterPublicKey, err := sealedtx.PublicKey(submitterPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	policy.RiskAuthorityKeyID = "integration-risk-authority"
	policy.RiskAuthorityPublicKey = authorityPublic
	policy.SubmitterPublicKey = submitterPublicKey
	policyPath := filepath.Join(temp, "signer-policy.json")
	keypairPath := filepath.Join(temp, "keypair.json")
	riskPolicyPath := filepath.Join(temp, "risk-policy.json")
	riskKeypairPath := filepath.Join(temp, "risk-keypair.json")
	submitterPolicyPath := filepath.Join(temp, "submitter-policy.json")
	submitterKeyPath := filepath.Join(temp, "submitter-key.json")
	controlStatePath := filepath.Join(temp, "control.json")
	writeJSON(t, policyPath, policy)
	writeJSON(t, keypairPath, integrationKeypairValues(sourceKey))
	writeJSON(t, riskPolicyPath, policyauthority.Policy{
		TransactionPolicy: policy, GrantLifetimeSecs: 30,
	})
	writeJSON(t, riskKeypairPath, integrationKeypairValues(authorityKey))
	writeJSON(t, submitterPolicyPath, submitter.Policy{
		Cluster: policy.Cluster, Profile: policy.Profile,
		ProfileFingerprint: policy.ProfileFingerprint, ControlStatePath: controlStatePath,
		Source: policy.Source, Destination: policy.Destination,
		MaxLamports: policy.MaxLamports, MaxFeeLamports: policy.MaxFeeLamports,
		SubmitterPublicKey: policy.SubmitterPublicKey,
	})
	writeJSON(t, submitterKeyPath, submitter.KeyDocument{
		Version: 1, PrivateKey: submitterPrivateKey,
	})

	rpc := &integrationRPC{
		t:           t,
		blockhash:   solana.Encode(bytes.Repeat([]byte{7}, 32)),
		source:      source,
		destination: destination,
	}
	mithrilNode := httptest.NewServer(http.HandlerFunc(rpc.serve))
	defer mithrilNode.Close()
	primary := httptest.NewTLSServer(http.HandlerFunc(rpc.serve))
	defer primary.Close()
	secondary := httptest.NewTLSServer(http.HandlerFunc(rpc.serve))
	defer secondary.Close()
	trustTestServers(t, primary, secondary)
	t.Setenv("MITHRIL_AGENT_MITHRIL_RPC_URL", mithrilNode.URL)
	t.Setenv("MITHRIL_AGENT_PRIMARY_RPC_URL", primary.URL)
	t.Setenv("MITHRIL_AGENT_SECONDARY_RPC_URL", secondary.URL)
	t.Setenv("MITHRIL_MCP_PROFILE", "integration-test-helper")
	t.Setenv("MITHRIL_RPC_URL", "")

	cfg := config{Profile: profile}
	cfg.MCP.Command = mcpCommand
	cfg.MCP.Args = []string{"-test.run=^TestMCPHelperProcess$"}
	cfg.Signer.Command = signerCommand
	cfg.Signer.PolicyPath = policyPath
	cfg.Signer.KeypairPath = keypairPath
	cfg.Policy.Command = policyCommand
	cfg.Policy.PolicyPath = riskPolicyPath
	cfg.Policy.KeypairPath = riskKeypairPath
	cfg.Policy.KeyID = policy.RiskAuthorityKeyID
	cfg.Policy.PublicKey = policy.RiskAuthorityPublicKey
	cfg.Submitter.Command = submitterCommand
	cfg.Submitter.PolicyPath = submitterPolicyPath
	cfg.Submitter.PrivateKeyPath = submitterKeyPath
	cfg.Control.StatePath = controlStatePath
	configPath := filepath.Join(temp, "config.json")
	journalPath := filepath.Join(temp, "state", "events.jsonl")
	if err := os.Mkdir(filepath.Dir(journalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg.Journal.Path = journalPath
	writeJSON(t, configPath, cfg)
	if summary := checkPreflight(configPath); summary.Status != preflightOK {
		t.Fatalf("preflight summary = %+v", summary)
	}

	var readiness bytes.Buffer
	if err := runContext(t.Context(), []string{
		"devnet-check",
		"--config", configPath,
	}, &readiness); err != nil {
		t.Fatalf("%v: %s", err, readiness.String())
	}
	if !strings.Contains(readiness.String(), `"status":"ok"`) ||
		rpc.submitCount() != 0 {
		t.Fatalf("read-only readiness = %s, submissions=%d",
			readiness.String(), rpc.submitCount())
	}
	if _, err := os.Lstat(policy.AuthorizationLedgerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("readiness invoked the signer: %v", err)
	}

	seededAt := time.Now().UTC().Add(-6 * time.Second)
	seedStore, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedStore.Append(seededAt, execution.EventNodeObserved, "", agent.NodeObservation{
		Account: agent.Observation{
			Cluster:         "devnet",
			Source:          source,
			BalanceLamports: 123,
			Slot:            455,
			ObservedAt:      seededAt,
			EvidenceSource:  "mithril_mcp",
			Finality:        "local_unfinalized",
			Consistency:     "node_reported_non_atomic",
		},
		Health: agent.NodeHealth{
			Status:              "healthy",
			AssessmentScope:     "point_in_time_snapshot",
			ObservedAt:          seededAt,
			EvidenceComplete:    true,
			DivergenceArtifacts: 0,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := seedStore.Close(); err != nil {
		t.Fatal(err)
	}

	args := []string{"devnet-once", "--config", configPath}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var stopped bytes.Buffer
	if err := runContext(ctx, args, &stopped); err != nil {
		t.Fatal(err)
	}
	var stoppedResult execution.Result
	if err := json.Unmarshal(stopped.Bytes(), &stoppedResult); err != nil {
		t.Fatal(err)
	}
	if stoppedResult.Decision != "stopped" || rpc.submitCount() != 0 {
		t.Fatalf("default control result = %+v, submissions=%d",
			stoppedResult, rpc.submitCount())
	}
	issuedAt := time.Now().UTC().Add(-time.Second)
	if err := control.WriteDevnetActivation(
		cfg.Control.StatePath,
		profileFingerprint,
		issuedAt,
		issuedAt.Add(time.Hour),
		1,
		"bounded integration test",
	); err != nil {
		t.Fatal(err)
	}

	var first bytes.Buffer
	if err := runContext(ctx, args, &first); err != nil {
		t.Fatal(err)
	}
	var firstResult execution.Result
	if err := json.Unmarshal(first.Bytes(), &firstResult); err != nil {
		t.Fatal(err)
	}
	if firstResult.Decision != "complete" ||
		firstResult.Verdict != txflow.VerdictFinalized ||
		firstResult.AmountLamports != 18 ||
		firstResult.Signature == "" {
		t.Fatalf("first result = %+v", firstResult)
	}

	var second bytes.Buffer
	if err := runContext(ctx, args, &second); err != nil {
		t.Fatal(err)
	}
	var secondResult execution.Result
	if err := json.Unmarshal(second.Bytes(), &secondResult); err != nil {
		t.Fatal(err)
	}
	if !secondResult.Recovered ||
		secondResult.ActionID != firstResult.ActionID ||
		rpc.submitCount() != 1 {
		t.Fatalf("recovered result = %+v, submissions=%d", secondResult, rpc.submitCount())
	}

	lockedRuntime, err := openDevnetRuntime(configPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if competing, lockErr := journal.Open(journalPath); lockErr == nil {
		_ = competing.Close()
		t.Fatal("a second journal writer opened while the runtime was active")
	}
	if err := lockedRuntime.Close(); err != nil {
		t.Fatal(err)
	}

	loopCtx, stopLoop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stopLoop()
	loopOutput := cancelAfterLinesBuffer{remaining: 2, cancel: stopLoop}
	if err := runContext(loopCtx, []string{
		"devnet-run",
		"--config", configPath,
		"--interval", "1s",
		"--metrics-address", "127.0.0.1:0",
	}, &loopOutput); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(loopOutput.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("unattended loop completed %d cycles: %q", len(lines), loopOutput.String())
	}
	for _, line := range lines {
		var result execution.Result
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			t.Fatal(err)
		}
		if result.Decision != "complete" || !result.Recovered {
			t.Fatalf("unattended recovery result = %+v", result)
		}
	}
	if rpc.submitCount() != 1 {
		t.Fatalf("unattended loop resubmitted a finalized transfer: %d", rpc.submitCount())
	}
}

type cancelAfterLinesBuffer struct {
	bytes.Buffer
	remaining int
	cancel    context.CancelFunc
}

func (buffer *cancelAfterLinesBuffer) Write(data []byte) (int, error) {
	written, err := buffer.Buffer.Write(data)
	buffer.remaining -= bytes.Count(data[:written], []byte{'\n'})
	if buffer.remaining <= 0 {
		buffer.cancel()
	}
	return written, err
}

func buildCommand(t *testing.T, directory, output string, name string, args ...string) {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = directory
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", output, err, result)
	}
	if err := os.Chmod(output, 0o700); err != nil {
		t.Fatalf("set permissions on %s: %v", output, err)
	}
}

func integrationKeypairValues(key []byte) []uint16 {
	values := make([]uint16, len(key))
	for index, value := range key {
		values[index] = uint16(value)
	}
	return values
}

type integrationRPC struct {
	t           *testing.T
	blockhash   string
	source      string
	destination string
	mu          sync.Mutex
	signature   string
	transaction []byte
	submissions int
}

func (rpc *integrationRPC) serve(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	var result any
	switch input.Method {
	case "getGenesisHash":
		result = solana.DevnetGenesisHash
	case "getAccountInfo":
		result = rpc.accountInfo(input.Params)
	case "getSlot":
		result = uint64(455)
	case "getLatestBlockhash":
		result = map[string]any{
			"context": map[string]any{"slot": uint64(460)},
			"value": map[string]any{
				"blockhash":            rpc.blockhash,
				"lastValidBlockHeight": uint64(250),
			},
		}
	case "getBlockHeight":
		result = uint64(100)
	case "getFeeForMessage":
		result = map[string]any{
			"context": map[string]any{"slot": uint64(460)},
			"value":   uint64(5),
		}
	case "simulateTransaction":
		rpc.validateSimulation(input.Params)
		result = map[string]any{
			"context": map[string]any{"slot": uint64(460)},
			"value": map[string]any{
				"err":           nil,
				"unitsConsumed": uint64(150),
				"logs":          []string{"program success"},
				"accounts": []any{
					integrationSystemAccount(100),
					integrationSystemAccount(28),
				},
			},
		}
	case "sendTransaction":
		result = rpc.acceptTransaction(input.Params)
	case "getSignatureStatuses":
		result = map[string]any{"value": []any{rpc.finalizedStatus(input.Params)}}
	case "getTransaction":
		result = rpc.finalizedTransaction(input.Params)
	default:
		rpc.t.Errorf("unexpected RPC method %q", input.Method)
		http.Error(writer, "unexpected method", http.StatusBadRequest)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      input.ID,
		"result":  result,
	}); err != nil {
		rpc.t.Errorf("encode RPC response: %v", err)
	}
}

func integrationSystemAccount(lamports uint64) map[string]any {
	return map[string]any{
		"data":       []any{"", "base64"},
		"executable": false,
		"lamports":   lamports,
		"owner":      solana.Encode(make([]byte, 32)),
	}
}

func (rpc *integrationRPC) accountInfo(params json.RawMessage) any {
	var values []json.RawMessage
	if err := json.Unmarshal(params, &values); err != nil || len(values) != 2 {
		rpc.t.Errorf("invalid getAccountInfo parameters")
		return nil
	}
	var address string
	if err := json.Unmarshal(values[0], &address); err != nil {
		rpc.t.Errorf("invalid account address")
		return nil
	}
	lamports := uint64(0)
	switch address {
	case rpc.source:
		lamports = 123
	case rpc.destination:
		lamports = 10
	default:
		rpc.t.Errorf("unexpected account address")
		return nil
	}
	return map[string]any{
		"context": map[string]any{"slot": uint64(460)},
		"value": map[string]any{
			"data":       []any{"", "base64"},
			"executable": false,
			"lamports":   lamports,
			"owner":      solana.Encode(make([]byte, 32)),
		},
	}
}

func (rpc *integrationRPC) acceptTransaction(params json.RawMessage) string {
	var values []json.RawMessage
	if err := json.Unmarshal(params, &values); err != nil || len(values) == 0 {
		rpc.t.Errorf("invalid sendTransaction parameters")
		return ""
	}
	var encoded string
	if err := json.Unmarshal(values[0], &encoded); err != nil {
		rpc.t.Errorf("invalid transaction encoding")
		return ""
	}
	transaction, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		rpc.t.Errorf("invalid transaction base64")
		return ""
	}
	decoded, err := solana.DecodeSignedTransfer(transaction)
	if err != nil {
		rpc.t.Errorf("invalid signed transaction")
		return ""
	}
	signature := solana.Encode(decoded.Signature[:])
	rpc.mu.Lock()
	rpc.signature = signature
	rpc.transaction = bytes.Clone(transaction)
	rpc.submissions++
	rpc.mu.Unlock()
	return signature
}

func (rpc *integrationRPC) validateSimulation(params json.RawMessage) {
	var values []json.RawMessage
	if err := json.Unmarshal(params, &values); err != nil || len(values) != 2 {
		rpc.t.Errorf("invalid simulateTransaction parameters")
		return
	}
	var encoded string
	if err := json.Unmarshal(values[0], &encoded); err != nil {
		rpc.t.Errorf("invalid simulation transaction encoding")
		return
	}
	transaction, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(transaction) < 65 || transaction[0] != 1 ||
		!bytes.Equal(transaction[1:65], make([]byte, 64)) {
		rpc.t.Errorf("invalid unsigned simulation transaction")
		return
	}
	if _, err := solana.DecodeTransferMessage(transaction[65:]); err != nil {
		rpc.t.Errorf("invalid simulation message")
	}
}

func (rpc *integrationRPC) finalizedStatus(params json.RawMessage) any {
	var values []json.RawMessage
	if err := json.Unmarshal(params, &values); err != nil || len(values) == 0 {
		return nil
	}
	var signatures []string
	if err := json.Unmarshal(values[0], &signatures); err != nil || len(signatures) != 1 {
		return nil
	}
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	if signatures[0] != rpc.signature || rpc.signature == "" {
		return nil
	}
	return map[string]any{
		"slot":               uint64(470),
		"err":                nil,
		"confirmationStatus": "finalized",
	}
}

func (rpc *integrationRPC) finalizedTransaction(params json.RawMessage) any {
	var values []json.RawMessage
	if err := json.Unmarshal(params, &values); err != nil || len(values) != 2 {
		return nil
	}
	var signature string
	if err := json.Unmarshal(values[0], &signature); err != nil {
		return nil
	}
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	if signature != rpc.signature || len(rpc.transaction) == 0 {
		return nil
	}
	return map[string]any{
		"slot": uint64(470),
		"meta": map[string]any{
			"err":          nil,
			"fee":          uint64(5),
			"preBalances":  []uint64{123, 10, 1},
			"postBalances": []uint64{100, 28, 1},
		},
		"transaction": []any{base64.StdEncoding.EncodeToString(rpc.transaction), "base64"},
		"version":     "legacy",
	}
}

func (rpc *integrationRPC) submitCount() int {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	return rpc.submissions
}

func trustTestServers(t *testing.T, servers ...*httptest.Server) {
	t.Helper()
	roots := x509.NewCertPool()
	for _, server := range servers {
		certificate, err := x509.ParseCertificate(server.TLS.Certificates[0].Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		roots.AddCert(certificate)
	}
	previous := http.DefaultTransport
	previousExternalRPC := newExternalRPC
	previousPacedExternalRPC := newPacedExternalRPC
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		},
	}
	http.DefaultTransport = transport
	testClient := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	newExternalRPC = func(endpoint string) (*solanarpc.Client, error) {
		return solanarpc.New(endpoint, testClient, false)
	}
	newPacedExternalRPC = func(
		endpoint string,
		interval time.Duration,
	) (*solanarpc.Client, error) {
		return solanarpc.NewPaced(endpoint, testClient, interval)
	}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		http.DefaultTransport = previous
		newExternalRPC = previousExternalRPC
		newPacedExternalRPC = previousPacedExternalRPC
	})
}
